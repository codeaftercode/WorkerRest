#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""Go 版便携目录打包脚本。

把构建好的发布版 exe 与 roles/ 角色目录组装成对外发布的便携目录并打成 zip：

    release/WorkerRest-<版本>-portable.zip

与 Electron 版 make_portable.mjs 对齐的分工：
  - Go 单二进制 + embed 前端资源，没有 asar / koffi / marker 这些概念，因此组装
    结构只有两样：chibingren.exe + roles/（22 MB 内置角色模板）。
  - 运行时会在 exe 同目录落下 config/、sounds/、stats.json 等数据（见 app.go 的
    resourcesRoot：打包态取 exe 旁，便携优先），组装发布目录时一律清掉这些残留。

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
ROLES_SRC = os.path.join(ROOT, "roles")  # 角色目录随 Go 工程一起（模块根下）
EXE_NAME = "chibingren.exe"
EXE_SRC = os.path.join(ROOT, "build", "bin", EXE_NAME)
TEMP = os.environ.get("TEMP", ROOT)

# 运行产物目录：便携目录跑过一次后会留下这些，组装发布目录时清掉
RUNTIME_REL = ["config", "data", "sounds", "images", "stats.json"]

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
    """把 exe + roles/ 组装进 dest（可分发便携目录）。"""
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
    return True


def zip_dir(zip_path: str, src_dir: str) -> bool:
    """打 zip（含顶层目录：解压得到 WorkerRest-<版本>-portable/）。"""
    if os.path.exists(zip_path):
        os.remove(zip_path)
    top = os.path.basename(src_dir)
    parent = os.path.dirname(src_dir)
    print(f"[make-portable] zip -> {zip_path}")
    with zipfile.ZipFile(zip_path, "w", zipfile.ZIP_DEFLATED, compresslevel=9) as zf:
        for root, _dirs, files in os.walk(src_dir):
            for name in files:
                fp = os.path.join(root, name)
                arc = os.path.join(top, os.path.relpath(fp, src_dir))
                zf.write(fp, arc)
    return os.path.exists(zip_path)


def verify_zip(zip_path: str, top: str) -> bool:
    """校验 zip 内容：条目数 + exe/roles 关键文件在正确位置（带顶层目录前缀）。"""
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
    ok = test is None and has_exe and has_roles and has_role_json
    print(
        f"[make-portable] zip 校验: entries={len(names)} exe={has_exe} roles={has_roles}"
        f" role_json={has_role_json} png={n_roles} crc={test is None}"
    )
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
        ("虚拟角色目录", os.path.join(dest, "roles", "supervisor_default", "role.json")),
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
