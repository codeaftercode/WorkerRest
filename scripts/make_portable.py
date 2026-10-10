#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""Go 版便携目录打包脚本。

把构建好的发布版 exe 与随包资源组装成对外发布的便携目录并打成 zip：

    release/WorkerRest-<版本>-portable.zip

组装结构：
  - chibingren.exe + roles/（22 MB 内置督休专员模板）；
  - 预置三件套（运行数据随包分发，首跑直接在预置文件上继续写）：
      sounds/               四个内置提示音（源文件在模块根 sounds/，不再 embed 进二进制）
      config/settings.json  出厂默认配置（与 go:embed 的 default_settings.json 同一
                            份文件，天然零漂移；installId 是空串，首跑自动补生成并回写）
      data/stats.json       空统计表（与 stats.go 空表落盘格式一致）
  - 运行时残留（exe 同目录跑过一次后留下的 config/data/sounds 等）组装前一律清掉。

版本号：优先取 -ldflags 注入的 main.appVersion（脚本会读 app.go 里的默认值兜底），
与二进制保持一致 —— 避免「zip 名字写 1.0.0、exe 里报 1.0.0」之外的分裂。

用法：python scripts/make_portable.py [--version 1.0.0] [--no-build] [--out DIR]
  --no-build   跳过 go build，直接用 build/bin/chibingren.exe 组装（已构建过时用）
  --out DIR    交付目录（默认 release/；被锁时可指向别处，脚本会拷回）

退出码 0 = 打包成功且 zip 校验通过。
"""

import argparse
import hashlib
import os
import re
import shutil
import subprocess
import sys
import zipfile

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)  # 项目根（原 rewrite-go，现已提升为顶层）
ROLES_SRC = os.path.join(ROOT, "roles")  # 督休专员目录随 Go 工程一起（模块根下）
SOUNDS_SRC = os.path.join(ROOT, "sounds")  # 内置提示音源文件（打包预置进便携包）
DEFAULT_SETTINGS_SRC = os.path.join(ROOT, "default_settings.json")  # 出厂默认（go:embed 同一份）
EXE_NAME = "chibingren.exe"
EXE_SRC = os.path.join(ROOT, "build", "bin", EXE_NAME)
TEMP = os.environ.get("TEMP", ROOT)

# 运行产物目录：便携目录跑过一次后会留下这些，组装发布目录时清掉
RUNTIME_REL = ["config", "data", "sounds", "images", "stats.json"]

# 空统计表预置内容：与 stats.go marshalLocked 的空表落盘格式一致（缩进 2 空格）
EMPTY_STATS_JSON = '{\n  "_customNames": {}\n}\n'

# 交付目录默认 release/（项目内；被 IDE/杀软锁住时用 --out 重定向）
DEFAULT_OUT = os.path.join(ROOT, "release")


def app_version_default() -> str:
    """从 app.go 读默认版本号（ldflags 注入的兜底值，二者同源）。"""
    src = os.path.join(ROOT, "app.go")
    try:
        with open(src, "r", encoding="utf-8") as f:
            txt = f.read()
    except OSError:
        return "1.0.0"
    m = re.search(r'appVersion\s*=\s*"([^"]+)"', txt)
    return m.group(1) if m else "1.0.0"


def build_exe(version: str) -> bool:
    """构建发布版 exe（带版本号注入 + windowsgui + 去符号）。"""
    exe = EXE_SRC
    ldflags = f'-X main.appVersion={version} -H windowsgui -s -w'
    print(f"[make-portable] go build -> {exe} (version={version})")
    r = subprocess.run(
        ["go", "build", "-o", exe, "-ldflags", ldflags, "."],
        cwd=ROOT,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
    )
    if r.returncode != 0:
        out = r.stdout.decode("utf-8", "replace")
        print(f"[make-portable] FAIL: go build 失败\n{out}")
        return False
    return os.path.exists(exe)


def assemble(dest: str, version: str) -> bool:
    """把 exe + roles/ + 预置三件套组装进 dest（可分发便携目录）。"""
    if os.path.exists(dest):
        shutil.rmtree(dest, ignore_errors=True)
    os.makedirs(dest, exist_ok=True)
    shutil.copy2(EXE_SRC, os.path.join(dest, EXE_NAME))
    shutil.copytree(ROLES_SRC, os.path.join(dest, "roles"))
    # 清掉运行时残留（roles/ 里理论上没有，但 exe 同目录可能被组装源污染）
    for rel in RUNTIME_REL:
        p = os.path.join(dest, rel)
        if os.path.isdir(p):
            shutil.rmtree(p, ignore_errors=True)
        elif os.path.exists(p):
            os.remove(p)
    # 预置三件套：sounds/（内置提示音）、config/settings.json（出厂默认）、
    # data/stats.json（空统计表）。首跑直接在预置文件上继续写（installId 由
    # Go 侧 ensureInstallID 补生成并回写）。
    shutil.copytree(SOUNDS_SRC, os.path.join(dest, "sounds"))
    os.makedirs(os.path.join(dest, "config"), exist_ok=True)
    shutil.copy2(DEFAULT_SETTINGS_SRC, os.path.join(dest, "config", "settings.json"))
    os.makedirs(os.path.join(dest, "data"), exist_ok=True)
    with open(os.path.join(dest, "data", "stats.json"), "w", encoding="utf-8", newline="\n") as f:
        f.write(EMPTY_STATS_JSON)
    return True


def zip_dir(zip_path: str, src_dir: str) -> bool:
    """打 zip（含顶层目录：解压得到 WorkerRest-<版本>-portable/）。

    arcname 必须显式用正斜杠拼：解压工具对 zip 条目分隔符的口径不一，
    反斜杠条目会被当成文件名的一部分，解压后文件散落一地。
    """
    if os.path.exists(zip_path):
        os.remove(zip_path)
    top = os.path.basename(src_dir)
    print(f"[make-portable] zip -> {zip_path}")
    with zipfile.ZipFile(zip_path, "w", zipfile.ZIP_DEFLATED, compresslevel=9) as zf:
        for root, _dirs, files in os.walk(src_dir):
            for name in files:
                fp = os.path.join(root, name)
                rel = os.path.relpath(fp, src_dir).replace(os.sep, "/")
                zf.write(fp, top + "/" + rel)
    return os.path.exists(zip_path)


def verify_zip(zip_path: str, top: str) -> bool:
    """校验 zip 内容：条目数 + exe/roles/预置三件套在正确位置（带顶层目录前缀）。"""
    try:
        with zipfile.ZipFile(zip_path, "r") as zf:
            names = zf.namelist()
            test = zf.testzip()
    except Exception as e:  # noqa: BLE001
        print(f"[make-portable] zip 校验异常: {e}")
        return False
    has_exe = f"{top}/{EXE_NAME}" in names
    has_roles = any(n.startswith(f"{top}/roles/") for n in names)
    has_role_json = any(n.endswith("/role.json") for n in names)
    n_roles = sum(1 for n in names if n.startswith(f"{top}/roles/") and n.endswith(".png"))
    # 预置三件套：四个内置提示音 + 出厂默认配置 + 空统计表
    n_sounds = sum(1 for n in names if n.startswith(f"{top}/sounds/") and n.endswith(".wav"))
    has_cfg = f"{top}/config/settings.json" in names
    has_stats = f"{top}/data/stats.json" in names
    # 顶层目录硬保证：每个条目都必须落在 top/ 里，且不允许出现反斜杠条目
    # （反斜杠会被一部分解压工具当文件名的一部分，解压后文件散落一地）
    escaped = [n for n in names if not n.startswith(top + "/") or "\\" in n]
    ok = (
        test is None and has_exe and has_roles and has_role_json
        and n_sounds == 4 and has_cfg and has_stats and not escaped
    )
    print(
        f"[make-portable] zip 校验: entries={len(names)} exe={has_exe} roles={has_roles}"
        f" role_json={has_role_json} png={n_roles} sounds={n_sounds} cfg={has_cfg}"
        f" stats={has_stats} escaped={len(escaped)} crc={test is None}"
    )
    if escaped:
        print(f"[make-portable] FAIL: 有条目跑到顶层目录外或含反斜杠: {escaped[:5]}")
    if not ok:
        print(f"[make-portable] FAIL: zip 内容不完整（crc 异常项={test}）")
    return ok


def md5(p: str) -> str:
    h = hashlib.md5()
    with open(p, "rb") as f:
        for chunk in iter(lambda: f.read(65536), b""):
            h.update(chunk)
    return h.hexdigest()


def main() -> int:
    ap = argparse.ArgumentParser(description="Go 版便携目录打包")
    ap.add_argument("--version", default="", help="版本号（默认读 app.go 的 appVersion）")
    ap.add_argument("--no-build", action="store_true", help="跳过 go build，直接用已有 exe")
    ap.add_argument("--out", default="", help="交付目录（默认 release/）")
    args = ap.parse_args()

    version = args.version or app_version_default()
    out_dir = os.path.abspath(args.out) if args.out else DEFAULT_OUT

    # 1. 构建 exe
    if not args.no_build:
        if not build_exe(version):
            return 1
    if not os.path.exists(EXE_SRC):
        print(f"[make-portable] FAIL: 找不到 {EXE_SRC}（先构建，或用 --no-build 前先 go build）")
        return 1

    # 2. 组装（在系统临时目录做，避免 release/ 历史产物被锁）
    stage_parent = os.path.join(TEMP, "wr-portable-build")
    dest = os.path.join(stage_parent, f"WorkerRest-{version}-portable")
    assemble(dest, version)

    # 3. 结构核验
    checks = [
        ("exe", os.path.join(dest, EXE_NAME)),
        ("督休专员目录", os.path.join(dest, "roles", "supervisor_default", "role.json")),
        ("预置 sounds/", os.path.join(dest, "sounds", "short_start.wav")),
        ("预置 config/settings.json", os.path.join(dest, "config", "settings.json")),
        ("预置 data/stats.json", os.path.join(dest, "data", "stats.json")),
    ]
    ok = True
    for name, p in checks:
        has = os.path.exists(p)
        if not has:
            ok = False
        print(f"[make-portable] {name}: {'ok' if has else 'MISSING'}（{p}）")

    # 4. 打 zip + 校验
    zip_path = os.path.join(stage_parent, f"WorkerRest-{version}-portable.zip")
    if ok and zip_dir(zip_path, dest):
        ok = ok and verify_zip(zip_path, f"WorkerRest-{version}-portable")

    # 5. 交付：拷回项目 release/（含 zip 与散装目录）
    os.makedirs(out_dir, exist_ok=True)
    final_zip = os.path.join(out_dir, f"WorkerRest-{version}-portable.zip")
    if ok:
        shutil.copy2(zip_path, final_zip)
        # 散装目录也拷回（用户可直接解压/拷贝使用，不依赖 zip）
        final_dir = os.path.join(out_dir, f"WorkerRest-{version}-portable")
        if os.path.exists(final_dir):
            shutil.rmtree(final_dir, ignore_errors=True)
        shutil.copytree(dest, final_dir)
        print(f"[make-portable] 交付 zip: {final_zip} ({os.path.getsize(final_zip)} bytes)")
        print(f"[make-portable] 交付目录: {final_dir}")

    # 清掉临时组装目录
    shutil.rmtree(stage_parent, ignore_errors=True)
    print(f"[make-portable] {'DONE' if ok else 'FAILED'}")
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
