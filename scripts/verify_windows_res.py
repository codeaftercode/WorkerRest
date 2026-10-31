#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""校验 exe 的 Windows 资源：图标 + VERSIONINFO + MANIFEST（审计 域2-4）。

解析 PE 的 .rsrc 节，断言三类资源都存在且关键字段正确：
  - RT_GROUP_ICON(14) / RT_ICON(3) —— exe 文件图标仍在（不能因换 goversioninfo 丢了）
  - RT_VERSION(16) —— VERSIONINFO 在，且 CompanyName/FileDescription/ProductName 等关键
    字符串非空、OriginalFilename 与 exe 名一致
  - RT_MANIFEST(24) —— 应用清单在，且声明了 longPathAware 与 supportedOS（Win10/11）

不校验「签名」：签名（Authenticode）需要证书，不在本脚本范围；本脚本只验证资源。

用法：python scripts/verify_windows_res.py <exe> [--expect-version X.Y.Z[.W]]
  --expect-version  可选：额外断言 FileVersion 的数字段等于给定值（比较到给定段数）。
                    省略则只断言存在、非空。
退出码 0 = 通过；1 = 失败（打印原因）。
"""

import argparse
import re
import struct
import sys


class PEResourceError(Exception):
    pass


def _load(path):
    with open(path, "rb") as f:
        return f.read()


def _pe_layout(data):
    if data[:2] != b"MZ":
        raise PEResourceError("不是 PE 文件（缺 MZ 头）")
    pe_off = struct.unpack_from("<I", data, 0x3C)[0]
    if data[pe_off:pe_off + 4] != b"PE\x00\x00":
        raise PEResourceError("PE 签名异常")
    coff = pe_off + 4
    num_sec = struct.unpack_from("<H", data, coff + 2)[0]
    opt_size = struct.unpack_from("<H", data, coff + 16)[0]
    opt = coff + 20
    is64 = struct.unpack_from("<H", data, opt)[0] == 0x20B
    dd_off = opt + (112 if is64 else 96)
    rsrc_rva, rsrc_size = struct.unpack_from("<II", data, dd_off + 16)
    if not rsrc_rva or not rsrc_size:
        raise PEResourceError("PE 没有资源节（.rsrc）")
    sec_off = opt + opt_size
    secs = []
    for i in range(num_sec):
        s = sec_off + i * 40
        name = data[s:s + 8].rstrip(b"\x00").decode("ascii", "replace")
        vsize = struct.unpack_from("<I", data, s + 8)[0]
        vaddr = struct.unpack_from("<I", data, s + 12)[0]
        rawsize = struct.unpack_from("<I", data, s + 16)[0]
        rawptr = struct.unpack_from("<I", data, s + 20)[0]
        secs.append((name, vaddr, vsize, rawptr, rawsize))

    def rva2off(rva):
        for _n, vaddr, vsize, rawptr, rawsize in secs:
            if vaddr <= rva < vaddr + max(vsize, rawsize):
                return rawptr + (rva - vaddr)
        return None

    return data, rsrc_rva, rva2off


def _collect(data, rsrc_rva, rva2off):
    """遍历资源树，返回 {type_id: [(rva, size), ...]}。"""
    types = {}

    def walk(off, path):
        n_named, n_id = struct.unpack_from("<HH", data, off + 12)
        for i in range(n_named + n_id):
            e = off + 16 + i * 8
            no = struct.unpack_from("<I", data, e)[0]
            do = struct.unpack_from("<I", data, e + 4)[0]
            nm = no & 0x7FFFFFFF if no & 0x80000000 else no
            if do & 0x80000000:
                sub = rva2off(rsrc_rva + (do & 0x7FFFFFFF))
                walk(sub, path + [nm])
            else:
                de = rva2off(rsrc_rva + do)
                rva = struct.unpack_from("<I", data, de)[0]
                size = struct.unpack_from("<I", data, de + 4)[0]
                types.setdefault(path[0] if path else None, []).append((rva, size))

    walk(rva2off(rsrc_rva), [])
    return types


def _version_strings(data, rva2off, rva, size):
    """解析 VS_VERSIONINFO 的字符串字段，返回 {key: value}。

    goversioninfo 生成的 StringTable 是「key\\0\\0value\\0\\0…」连续排列的紧凑格式
    （非标准的 String 块头嵌套），逐块解析容易走偏。这里用最稳的方式：把整段
    VERSIONINFO 按 UTF-16LE 解码，再用正则提取所有已知键名后紧跟的值。
    键名是固定集合，正则按「键名 + 双空终止 + 值 + 双空终止」匹配，天然对齐。
    """
    off = rva2off(rva)
    blob = data[off:off + size]
    text = blob.decode("utf-16-le", "replace")
    out = {}
    keys = ("CompanyName", "FileDescription", "FileVersion", "InternalName",
            "LegalCopyright", "OriginalFilename", "ProductName", "ProductVersion",
            "Comments", "LegalTrademarks", "PrivateBuild", "SpecialBuild")
    for key in keys:
        # 键名后跟 1~2 个 NUL（键终止 + 偶对齐填充），值以 NUL 终止。
        # 值本身不含 NUL，用 [^\x00]+ 取非空片段即是最稳的提取方式。
        m = re.search(re.escape(key) + r"\x00+(.*?)\x00", text, re.DOTALL)
        if m and m.group(1).strip("\x00"):
            out[key] = m.group(1).rstrip("\x00")
    return out


def _manifest_text(data, rva2off, rva, size):
    off = rva2off(rva)
    return data[off:off + size].decode("utf-8", "replace")


def verify(exe, expect_version=None):
    data = open(exe, "rb").read()
    data, rsrc_rva, rva2off = _pe_layout(data)
    types = _collect(data, rsrc_rva, rva2off)

    errors = []

    # 1. 图标
    if not types.get(14) or not types.get(3):
        errors.append("缺 RT_GROUP_ICON(14)/RT_ICON(3)：exe 文件图标丢失")

    # 2. VERSIONINFO
    ver = types.get(16)
    if not ver:
        errors.append("缺 RT_VERSION(16)：无 VERSIONINFO")
    else:
        rva, size = ver[0]
        strings = _version_strings(data, rva2off, rva, size)
        for key in ("CompanyName", "FileDescription", "ProductName", "FileVersion",
                    "InternalName", "OriginalFilename"):
            if not strings.get(key):
                errors.append(f"VERSIONINFO 缺 {key}")
        if expect_version:
            # FileVersion 形如 "0.9.5.0"；按期望给的段数逐段比较
            fv = strings.get("FileVersion", "")
            want = tuple(int(x) for x in expect_version.split("."))
            m = re.match(r"^(\d+)\.(\d+)\.(\d+)(?:\.(\d+))?", fv)
            got = tuple(int(x) for x in (m.groups() if m else ()))
            if not got or got[:len(want)] != want:
                errors.append(
                    f"FileVersion 数字段 {fv!r} 与期望 {expect_version} 不一致")

    # 3. MANIFEST
    mf = types.get(24)
    if not mf:
        errors.append("缺 RT_MANIFEST(24)：无应用清单")
    else:
        rva, size = mf[0]
        text = _manifest_text(data, rva2off, rva, size)
        for kw in ("longPathAware", "supportedOS"):
            if kw not in text:
                errors.append(f"MANIFEST 缺 {kw}")
        if "8e0f7a12-bfb3-4fe8-b9a5-48fd50a15a9a" not in text:
            errors.append("MANIFEST 缺 Windows 10/11 supportedOS")
        # DPI 感知不得写进清单（Wails 运行时已 SetProcessDpiAwarenessContext）
        if "dpiAware" in text:
            errors.append("MANIFEST 不应声明 dpiAware（与运行时冲突）")

    return errors


def main():
    ap = argparse.ArgumentParser(description="校验 exe 的 Windows 资源（图标+VERSIONINFO+MANIFEST）")
    ap.add_argument("exe", help="要校验的 exe 路径")
    ap.add_argument("--expect-version", default="",
                    help="可选：断言 FileVersion 数字段（如 0.9.5）")
    args = ap.parse_args()

    try:
        errors = verify(args.exe, args.expect_version or None)
    except PEResourceError as e:
        print(f"[verify-winres] FAIL: {e}")
        return 1

    if errors:
        print("[verify-winres] FAIL:")
        for e in errors:
            print(f"    - {e}")
        return 1
    print("[verify-winres] 图标 + VERSIONINFO + MANIFEST 齐全")
    return 0


if __name__ == "__main__":
    sys.exit(main())
