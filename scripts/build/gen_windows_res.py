#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""生成 Windows 资源文件 rsrc_windows_amd64.syso（图标 + VERSIONINFO + MANIFEST）。

用 goversioninfo（github.com/josephspurrier/goversioninfo）一个工具同时生成三类资源，
替换早期只嵌图标、没有 VERSIONINFO 也没有应用清单的 `rsrc` 方案（审计 域2-4）。

三类资源：
  - RT_GROUP_ICON / RT_ICON —— exe 文件图标（多尺寸，从 web/dist/icon.png 现算）
  - RT_VERSION(16) —— 文件/产品版本、公司、描述等（资源管理器「属性 → 详细信息」可见）
  - RT_MANIFEST(24) —— 应用清单：longPathAware（Win10 1607+ 支持 >260 字符路径）
    与 supportedOS（让系统正确识别新系统，避免兼容模式误判）

不签名：VERSIONINFO / MANIFEST 只是编进 PE .rsrc 节的资源，与 Authenticode 签名无关，
没有证书也能生成。DPI 感知**不写进清单** —— Wails 运行时已 SetProcessDpiAwarenessContext
（PER_MONITOR_V2），清单里再声明 dpiAware 会与之冲突。

版本号规则（与 ldflags 注入的 appVersion 口径一致，见 make_portable.py）：
  版本 = **HEAD 上的 tag**（去 v，例如 v0.9.5 → 0.9.5）；不足 4 段时第 4 段补 0（→ 0.9.5.0）。
  HEAD 没打 tag 时退回「最近可达 tag」（与 version.py 的 latest_tag 同源）。也可用
  `--version` 显式指定（打包脚本在 go build 前用本次 tag 版本临时生成，见 --version）。
  数字段（FixedFileInfo.FileVersion/ProductVersion）与字符串段（StringFileInfo 的
  FileVersion/ProductVersion）**相同**，都是这个四段版本号，不做 .dev 特殊处理。

产物直接覆盖仓库根 rsrc_windows_amd64.syso（随源码入库，go build 自动链接），
make_portable.py / ldflags 都不改。生成可复现：同一份 icon.png + 同一版本号 → 同一 sha256。

用法：python scripts/gen_windows_res.py [--check | --version X.Y.Z --out PATH]
  --check   只核对现有 syso 的图标/manifest 内容是否与当前源码一致（不比对版本号），
            不一致返回 1（不写文件）。
  --version  显式指定版本号（如 0.9.6），配合 --out 在打包时于临时位置生成一份正确版本
            的 syso，供 go build 链接 —— 消除「打 tag 后仓库 syso 版本号还是旧的，必须
            先 commit 一份再挪 tag」的死锁。不与 --check 同时用。
  --out      与 --version 连用：把生成的 syso 写到指定路径，而非仓库根。
"""

import argparse
import os
import re
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(os.path.dirname(HERE))
MAIN_DIR = os.path.join(ROOT, "cmd", "workerrest")  # main 包所在（含 embed 资源与 .syso）

# 复用 version.py 的 tag 语义（tag_at_head / latest_tag / sort_key），
# 保证这里读到的版本号与 make_portable.py 逐字同源 —— 两个脚本对「版本是什么」
# 的定义不再各写一份正则，避免「打 tag 后一边读到 0.9.6、一边还停在 0.9.5」的漂移。
sys.path.insert(0, HERE)
import version as version_mod  # noqa: E402

ICON_SRC = os.path.join(MAIN_DIR, "web", "dist", "icon.png")
SYSO_OUT = os.path.join(MAIN_DIR, "rsrc_windows_amd64.syso")
VERSIONINFO_JSON = os.path.join(ROOT, "build", "windows", "versioninfo.json")
MANIFEST_SRC = os.path.join(ROOT, "build", "windows", "app.manifest")

# 图标多尺寸（与早期 rsrc 方案的 sizes=[16,24,32,48,64,128,256] 一致）
ICO_SIZES = [16, 24, 32, 48, 64, 128, 256]

# VERSIONINFO 固定字段（与软件定位一致，见 main.go / README）
COMPANY_NAME = "goodgoodlive"
PRODUCT_NAME = "吃饼人也敢休息？（WorkerRest）"
FILE_DESCRIPTION = "吃饼人也敢休息？— 休息提醒"
INTERNAL_NAME = "rest"
ORIGINAL_FILENAME = "rest.exe"
LEGAL_COPYRIGHT = "© goodgoodlive"


def find_goversioninfo() -> str:
    """定位 goversioninfo：PATH → 用户 GOPATH/bin。找不到抛 RuntimeError。"""
    found = None
    for d in os.environ.get("PATH", "").split(os.pathsep):
        p = os.path.join(d, "goversioninfo.exe") if d else ""
        if p and os.path.isfile(p):
            found = p
            break
    if not found:
        gopath = os.environ.get("GOPATH", os.path.join(os.path.expanduser("~"), "go"))
        p = os.path.join(gopath, "bin", "goversioninfo.exe")
        if os.path.isfile(p):
            found = p
    if not found:
        raise RuntimeError(
            "找不到 goversioninfo，请先安装："
            "go install github.com/josephspurrier/goversioninfo/cmd/goversioninfo@latest"
        )
    return found


def derive_version(explicit: str = "") -> tuple:
    """返回 (数字四段 tuple, 字符串版本)。

    `explicit` 非空时直接用它（形如 "0.9.6" 或 "0.9.6.0"），供打包脚本在「tag 已打、
    但 syso 尚未 commit」的窗口里，用本次 tag 版本显式生成一份正确的 syso ——
    这样就不需要「重新生成 → commit → 挪 tag」的手工四步。

    否则版本来源与 make_portable.py 逐字同源（见 version.py）：
      优先 HEAD 上的 tag（tag_at_head），没打 tag 才退回最近可达 tag（latest_tag）。
    不足 4 段时第 4 段补 0；数字段与字符串段相同，不做 .dev 特殊处理。
    """
    if explicit:
        m = re.match(r"^(\d+)\.(\d+)\.(\d+)(?:\.(\d+))?$", explicit)
        if not m:
            raise RuntimeError(f"显式版本格式不符合 X.Y.Z[.W]：{explicit}")
        major, minor, patch, build = m.groups()
        build = int(build) if build else 0
        return (int(major), int(minor), int(patch), build), f"{major}.{minor}.{patch}.{build}"
    tag = version_mod.tag_at_head() or version_mod.latest_tag()
    if not tag:
        # 仓库没有任何 tag：用 0.0.0.0 占位
        return (0, 0, 0, 0), "0.0.0.0"
    m = re.match(r"^v(\d+)\.(\d+)\.(\d+)$", tag)
    if not m:
        raise RuntimeError(f"版本 tag 格式不符合 v主.次.修订：{tag}")
    major, minor, patch = m.groups()
    return (int(major), int(minor), int(patch), 0), f"{major}.{minor}.{patch}.0"


def make_ico(png: str, ico: str) -> None:
    """用 Pillow 从 icon.png 生成多尺寸 ICO。"""
    from PIL import Image  # noqa: F401  (延迟导入，仅此处需要 Pillow)

    img = Image.open(png)
    if img.mode != "RGBA":
        img = img.convert("RGBA")
    img.save(ico, format="ICO", sizes=[(s, s) for s in ICO_SIZES])


def write_versioninfo_json(path: str, num: tuple, ver_str: str) -> None:
    import json

    doc = {
        "FixedFileInfo": {
            "FileVersion": {
                "Major": num[0], "Minor": num[1], "Patch": num[2], "Build": num[3],
            },
            "ProductVersion": {
                "Major": num[0], "Minor": num[1], "Patch": num[2], "Build": num[3],
            },
            "FileFlagsMask": "3f",
            "FileFlags": "00",
            "FileOS": "040004",
            "FileType": "01",
            "FileSubType": "00",
        },
        "StringFileInfo": {
            "Comments": "",
            "CompanyName": COMPANY_NAME,
            "FileDescription": FILE_DESCRIPTION,
            "FileVersion": ver_str,
            "InternalName": INTERNAL_NAME,
            "LegalCopyright": LEGAL_COPYRIGHT,
            "LegalTrademarks": "",
            "OriginalFilename": ORIGINAL_FILENAME,
            "PrivateBuild": "",
            "ProductName": PRODUCT_NAME,
            "ProductVersion": ver_str,
            "SpecialBuild": "",
        },
        "VarFileInfo": {
            "Translation": {"LangID": "0804", "CharsetID": "04B0"},
        },
    }
    with open(path, "w", encoding="utf-8", newline="\n") as f:
        f.write(json.dumps(doc, ensure_ascii=False, indent=2) + "\n")


MANIFEST_XML = """<?xml version="1.0" encoding="utf-8"?>
<assembly xmlns="urn:schemas-microsoft-com:asm.v1" manifestVersion="1.0">
  <compatibility xmlns="urn:schemas-microsoft-com:compatibility.v1">
    <application>
      <!-- Windows 10 / 11 -->
      <supportedOS Id="{8e0f7a12-bfb3-4fe8-b9a5-48fd50a15a9a}" />
      <!-- Windows 8.1 -->
      <supportedOS Id="{1f676c76-80e1-4239-95bb-83d0f6d0da78}" />
      <!-- Windows 8 -->
      <supportedOS Id="{4a2f28e3-53b9-4441-ba9c-d69d4a4a6e38}" />
      <!-- Windows 7 -->
      <supportedOS Id="{35138b9a-5d96-4fbd-8e2d-a2440225f93a}" />
    </application>
  </compatibility>
  <application xmlns="urn:schemas-microsoft-com:asm.v3">
    <windowsSettings>
      <longPathAware xmlns="http://schemas.microsoft.com/SMI/2016/WindowsSettings">true</longPathAware>
    </windowsSettings>
  </application>
</assembly>
"""


def write_manifest(path: str) -> None:
    with open(path, "w", encoding="utf-8", newline="\n") as f:
        f.write(MANIFEST_XML)


def generate(work_dir: str, version: str = "") -> str:
    """在 work_dir 里生成 syso 并返回其路径（不直接覆盖仓库文件，便于 --check 对比）。

    version 非空时显式指定版本号（跳过 git 现算），供打包脚本用。
    """
    num, ver_str = derive_version(version)
    ico = os.path.join(work_dir, "icon.ico")
    make_ico(ICON_SRC, ico)
    vjson = os.path.join(work_dir, "versioninfo.json")
    write_versioninfo_json(vjson, num, ver_str)
    mf = os.path.join(work_dir, "app.manifest")
    write_manifest(mf)

    tool = find_goversioninfo()
    out = os.path.join(work_dir, "resource.syso")
    r = subprocess.run(
        [tool, "-64", "-icon", ico, "-manifest", mf, "-o", out, vjson],
        cwd=work_dir, capture_output=True, text=True, encoding="utf-8", errors="replace",
    )
    if r.returncode != 0:
        raise RuntimeError("goversioninfo 生成失败：\n" + ((r.stdout or "") + (r.stderr or "")))
    if not os.path.isfile(out):
        raise RuntimeError("goversioninfo 未产出 resource.syso")
    print(f"[gen-winres] 版本 {ver_str}")
    return out


def _sha256(data: bytes) -> str:
    import hashlib
    return hashlib.sha256(data).hexdigest()


def _syso_file_version(syso_path: str) -> str:
    """从 syso（COFF object）里提取 VERSIONINFO 的 FileVersion 字符串。

    goversioninfo 把 VERSIONINFO 编成 COFF 的 .rsrc 节，字符串以 UTF-16LE 存储。
    这里不解析 COFF 结构 —— 直接在字节里搜 UTF-16LE 编码的 "FileVersion" 键，
    取它后面紧跟的值（形如 "0.9.5.0"），与 verify_windows_res.py 从 exe 里取值的
    正则口径一致。找不到返回空串。
    """
    data = open(syso_path, "rb").read()
    text = data.decode("utf-16-le", "replace")
    m = re.search(r"FileVersion\x00+(.*?)\x00", text, re.DOTALL)
    if not m:
        return ""
    return m.group(1).rstrip("\x00")


def main() -> int:
    ap = argparse.ArgumentParser(description="生成 Windows 资源（图标+VERSIONINFO+MANIFEST）")
    ap.add_argument("--check", action="store_true",
                    help="只核对现有 syso 的版本号与图标/清单内容是否仍匹配当前源码，不写文件")
    ap.add_argument("--version", default="",
                    help="显式指定版本号（如 0.9.6），跳过 git 现算；打包脚本用")
    ap.add_argument("--out", default="",
                    help="把 syso 写到指定路径而非仓库根（配合 --version 用于打包时临时生成）")
    args = ap.parse_args()

    import tempfile

    if not os.path.isfile(ICON_SRC):
        print(f"[gen-winres] FAIL: 找不到图标 {ICON_SRC}")
        return 1

    if args.version and args.check:
        print("[gen-winres] FAIL: --version 与 --check 互斥")
        return 1

    if not os.path.isfile(SYSO_OUT):
        # 没有 syso：--check 直接判缺；非 --check 则生成一份。
        if args.check:
            print("[gen-winres] FAIL: 仓库根没有 rsrc_windows_amd64.syso")
            return 1

    work = tempfile.mkdtemp(prefix="wr-winres-")
    try:
        if args.check:
            # 只比对「图标 + manifest 内容」是否与当前源码一致，**不比对版本号**。
            # 版本号的一致性由 make_portable.py 的 check_winres 用
            # verify_windows_res.py --expect-version 直接校验最终 exe（build_exe 已用
            # 本次 tag 版本显式生成 syso），这里再拿「仓库 syso 的版本号 == git 最新 tag」
            # 来卡，就是「打了新 tag → 必须 commit 一份新 syso → tag 又不在 HEAD」的死锁。
            #
            # 做法：读出现有 syso 里的 FileVersion，用**同一个版本号**重算一份 syso，
            # 再比全 hash —— 版本号相同，hash 的差异就只能来自图标/清单内容变化。
            got_ver = _syso_file_version(SYSO_OUT)
            if not got_ver:
                print("[gen-winres] FAIL: 读不到 syso 里的 FileVersion（syso 损坏？）")
                return 1
            new_syso = generate(work, got_ver)
            with open(new_syso, "rb") as f:
                new_hash = _sha256(f.read())
            with open(SYSO_OUT, "rb") as f:
                old_hash = _sha256(f.read())
            if new_hash != old_hash:
                print("[gen-winres] FAIL: syso 的图标/清单内容与当前源码不一致（需重新生成）")
                print(f"    当前 {old_hash}")
                print(f"    重算 {new_hash}")
                return 1
            print(f"[gen-winres] syso 的图标/清单与当前源码一致（版本 {got_ver}）")
            return 0

        # 显式版本（打包用）：只生成 syso 到指定位置，不动仓库文件，也不落 versioninfo.json/app.manifest
        if args.version:
            import shutil
            new_syso = generate(work, args.version)
            dest = args.out or SYSO_OUT
            shutil.copy2(new_syso, dest)
            with open(dest, "rb") as f:
                print(f"[gen-winres] 已生成 {dest} "
                      f"(版本 {args.version}, sha256={_sha256(f.read())})")
            return 0

        new_syso = generate(work)
        # 正常生成：覆盖仓库根 syso，并把 versioninfo.json / app.manifest 落盘留档
        import shutil
        shutil.copy2(new_syso, SYSO_OUT)
        shutil.copy2(os.path.join(work, "versioninfo.json"), VERSIONINFO_JSON)
        shutil.copy2(os.path.join(work, "app.manifest"), MANIFEST_SRC)
        with open(SYSO_OUT, "rb") as f:
            print(f"[gen-winres] 已写入 {os.path.relpath(SYSO_OUT, ROOT)} "
                  f"(sha256={_sha256(f.read())})")
        print(f"[gen-winres] 已写入 {os.path.relpath(VERSIONINFO_JSON, ROOT)}")
        print(f"[gen-winres] 已写入 {os.path.relpath(MANIFEST_SRC, ROOT)}")
        return 0
    finally:
        import shutil
        shutil.rmtree(work, ignore_errors=True)


if __name__ == "__main__":
    sys.exit(main())
