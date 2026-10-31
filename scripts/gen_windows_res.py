#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""生成 Windows 资源文件 rsrc_windows_amd64.syso（图标 + VERSIONINFO + MANIFEST）。

用 goversioninfo（github.com/josephspurrier/goversioninfo）一个工具同时生成三类资源，
替换早期只嵌图标、没有 VERSIONINFO 也没有应用清单的 `rsrc` 方案（审计 域2-4）。

三类资源：
  - RT_GROUP_ICON / RT_ICON —— exe 文件图标（多尺寸，从 frontend/dist/icon.png 现算）
  - RT_VERSION(16) —— 文件/产品版本、公司、描述等（资源管理器「属性 → 详细信息」可见）
  - RT_MANIFEST(24) —— 应用清单：longPathAware（Win10 1607+ 支持 >260 字符路径）
    与 supportedOS（让系统正确识别新系统，避免兼容模式误判）

不签名：VERSIONINFO / MANIFEST 只是编进 PE .rsrc 节的资源，与 Authenticode 签名无关，
没有证书也能生成。DPI 感知**不写进清单** —— Wails 运行时已 SetProcessDpiAwarenessContext
（PER_MONITOR_V2），清单里再声明 dpiAware 会与之冲突。

版本号规则（与 ldflags 注入的 appVersion 口径一致，见 make_portable.py）：
  版本 = 最近 tag（去 v，例如 v0.9.5 → 0.9.5）；不足 4 段时第 4 段补 0（→ 0.9.5.0）。
  数字段（FixedFileInfo.FileVersion/ProductVersion）与字符串段（StringFileInfo 的
  FileVersion/ProductVersion）**相同**，都是这个四段版本号，不做 .dev 特殊处理。

产物直接覆盖仓库根 rsrc_windows_amd64.syso（随源码入库，go build 自动链接），
make_portable.py / ldflags 都不改。生成可复现：同一份 icon.png + 同一版本号 → 同一 sha256。

用法：python scripts/gen_windows_res.py [--check]
  --check  只核对当前 syso 是否与「按当前源码/版本号重算」一致，不一致返回 1（不写文件）
"""

import argparse
import os
import re
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)

ICON_SRC = os.path.join(ROOT, "frontend", "dist", "icon.png")
SYSO_OUT = os.path.join(ROOT, "rsrc_windows_amd64.syso")
VERSIONINFO_JSON = os.path.join(ROOT, "versioninfo.json")
MANIFEST_SRC = os.path.join(ROOT, "app.manifest")

# goversioninfo 可执行文件：优先 PATH，其次用户 GOPATH/bin（本项目不引入 go 依赖）
GOVERSIONINFO = "goversioninfo"

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


def git(*args: str) -> str:
    r = subprocess.run(["git", *args], cwd=ROOT, capture_output=True, text=True,
                       encoding="utf-8", errors="replace")
    return r.stdout.strip()


def derive_version() -> tuple:
    """返回 (数字四段 tuple, 字符串版本)。

    规则：版本 = 最近 tag（去 v）；不足 4 段时第 4 段补 0。
    数字段与字符串段**相同**，不再用 .dev 特殊处理。
    """
    last = git("describe", "--tags", "--abbrev=0")
    if not last:
        # 仓库没有任何 tag：用 0.0.0.0 占位
        return (0, 0, 0, 0), "0.0.0.0"
    m = re.match(r"^v(\d+)\.(\d+)\.(\d+)(?:\.(\d+))?$", last)
    if not m:
        raise RuntimeError(f"最近 tag 格式不符合 v主.次.修订[.构建]：{last}")
    major, minor, patch, build = m.groups()
    build = int(build) if build else 0
    return (int(major), int(minor), int(patch), build), f"{major}.{minor}.{patch}.{build}"


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


def generate(work_dir: str) -> str:
    """在 work_dir 里生成 syso 并返回其路径（不直接覆盖仓库文件，便于 --check 对比）。"""
    num, ver_str = derive_version()
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


def main() -> int:
    ap = argparse.ArgumentParser(description="生成 Windows 资源（图标+VERSIONINFO+MANIFEST）")
    ap.add_argument("--check", action="store_true",
                    help="只核对当前 syso 与重算结果是否一致，不写文件")
    args = ap.parse_args()

    import tempfile

    if not os.path.isfile(ICON_SRC):
        print(f"[gen-winres] FAIL: 找不到图标 {ICON_SRC}")
        return 1

    work = tempfile.mkdtemp(prefix="wr-winres-")
    try:
        new_syso = generate(work)
        if args.check:
            if not os.path.isfile(SYSO_OUT):
                print("[gen-winres] FAIL: 仓库根没有 rsrc_windows_amd64.syso")
                return 1
            with open(new_syso, "rb") as f:
                new_hash = _sha256(f.read())
            with open(SYSO_OUT, "rb") as f:
                old_hash = _sha256(f.read())
            if new_hash != old_hash:
                print("[gen-winres] FAIL: syso 过期（图标/版本号/清单有变，需重新生成）")
                print(f"   当前 {old_hash}")
                print(f"   重算 {new_hash}")
                return 1
            print("[gen-winres] syso 与当前源码一致")
            return 0
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


def _sha256(data: bytes) -> str:
    import hashlib
    return hashlib.sha256(data).hexdigest()


if __name__ == "__main__":
    sys.exit(main())
