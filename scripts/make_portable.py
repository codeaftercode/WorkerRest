#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""Go 版便携目录打包脚本。

把构建好的发布版 exe 与随包资源组装成对外发布的便携目录、打成 zip，并生成
自动更新用的单文件包与版本清单：

    release/WorkerRest-<版本>-portable.zip   便携目录 zip（解压即用）
    release/WorkerRest-<版本>-portable/      同名散装目录
    release/rest.7z                          只含 rest.exe 的自动更新包
    release/version.json                     自动更新清单

组装结构：
  - rest.exe + supervisors/（22 MB 内置督休专员模板，子目录按专员名称命名）；
  - 预置三件套（运行数据随包分发，首跑直接在预置文件上继续写）：
      sounds/               四个内置提示音（源文件在模块根 sounds/，不再 embed 进二进制）
      config/settings.json  出厂默认配置（与 go:embed 的 default_settings.json 同一
                            份文件，天然零漂移；installId 是空串，首跑自动补生成并回写）
      data/stats.json       空统计表（与 stats.go 空表落盘格式一致）
  - 运行时残留（exe 同目录跑过一次后留下的 config/data/sounds 等）组装前一律清掉。

版本号：**只认当前提交上的 git tag**（格式 v主.次.修订，见 scripts/version.py）。
  - 当前提交没有 tag → 直接停止打包，提示先打 tag，并显示最近一次 tag 供参考；
  - 有 tag → 用它作为本次构建版本：exe 内注入去掉 v 的版本号（ldflags），
    产物文件名用该版本号，version.json 里写完整 tag。
  app.go 的 appVersion 只是 "dev" 占位符，不是版本来源。

version.json 的 changes：上一个 tag 到当前 tag 之间的提交标题，按时间顺序（旧→新）
自动编号（"1. xxx"）；首个 tag 没有上一个 tag，changes 留空。

用法：python scripts/make_portable.py [--no-build] [--out DIR]
  --no-build   跳过 go build，直接用 build/bin/rest.exe 组装（已构建过时用）
  --out DIR    交付目录（默认 release/；被锁时可指向别处，脚本会拷回）

退出码 0 = 打包成功且 zip / 7z / version.json 全部校验通过；
       2 = 当前提交没有版本 tag（未开始构建）；
       1 = 其它失败。
"""

import argparse
import hashlib
import json
import os
import re
import shutil
import subprocess
import sys
import zipfile
from datetime import datetime

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)  # 项目根（原 rewrite-go，现已提升为顶层）

# 同目录的 version.py（版本号只认 git tag）—— 显式加 sys.path，便于任意 cwd 下运行
if HERE not in sys.path:
    sys.path.insert(0, HERE)
import version as gitver  # noqa: E402

ROLES_SRC = os.path.join(ROOT, "supervisors")  # 督休专员目录随 Go 工程一起（模块根下）
SOUNDS_SRC = os.path.join(ROOT, "sounds")  # 内置提示音源文件（打包预置进便携包）
DEFAULT_SETTINGS_SRC = os.path.join(ROOT, "default_settings.json")  # 出厂默认（go:embed 同一份）
EXE_NAME = "rest.exe"
EXE_SRC = os.path.join(ROOT, "build", "bin", EXE_NAME)
TEMP = os.environ.get("TEMP", ROOT)

# 运行产物目录：便携目录跑过一次后会留下这些，组装发布目录时清掉
RUNTIME_REL = ["config", "data", "sounds", "images", "stats.json"]

# 空统计表预置内容：与 stats.go marshalLocked 的空表落盘格式一致（缩进 2 空格）
EMPTY_STATS_JSON = '{\n  "_customNames": {}\n}\n'

# 交付目录默认 release/（项目内；被 IDE/杀软锁住时用 --out 重定向）
DEFAULT_OUT = os.path.join(ROOT, "release")

# 自动更新清单里的更新源：url 是目录数组（update.go 补全规则：不以 .7z 结尾就拼
# /rest.7z），sites 由同一批目录派生（各自 + version.json）。改服务器地址只改这里。
UPDATE_BASE_URLS = [
    "https://nav.hcctc.cn/temp/",
    "https://www.goodgoodlive.com/worker-rest/",
]

# 7-Zip 命令行：SEVEN_ZIP 环境变量优先，其次 PATH，最后找默认安装目录
SEVEN_ZIP_CANDIDATES = [
    os.path.join(os.environ.get("ProgramFiles", r"C:\Program Files"), "7-Zip", "7z.exe"),
    os.path.join(
        os.environ.get("ProgramFiles(x86)", r"C:\Program Files (x86)"), "7-Zip", "7z.exe"
    ),
]


def suggest_next(tag: str) -> str:
    """给「还没打 tag」的提示建议下一个版本号：最近 tag 的修订号 +1。"""
    m = re.match(r"^v(\d+)\.(\d+)\.(\d+)$", tag or "")
    if not m:
        return "v0.9.0"
    major, minor, patch = (int(x) for x in m.groups())
    return f"v{major}.{minor}.{patch + 1}"


def build_exe(version: str) -> bool:
    """构建发布版 exe（带版本号 + 构建时间注入 + windowsgui + 去符号）。"""
    exe = EXE_SRC
    # 构建时间用 RFC3339 且无空格（ISO 格式），避免破坏单条 ldflags 字符串；
    # 前端「构建时间」行在为空时回退显示「本地构建」。
    build_time = datetime.now().astimezone().isoformat()
    ldflags = f'-X main.appVersion={version} -X main.buildTime={build_time} -H windowsgui -s -w'
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
    """把 exe + supervisors/ + 预置三件套组装进 dest（可分发便携目录）。"""
    if os.path.exists(dest):
        shutil.rmtree(dest, ignore_errors=True)
    os.makedirs(dest, exist_ok=True)
    shutil.copy2(EXE_SRC, os.path.join(dest, EXE_NAME))
    shutil.copytree(ROLES_SRC, os.path.join(dest, "supervisors"))
    # 清掉运行时残留（supervisors/ 里理论上没有，但 exe 同目录可能被组装源污染）
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
    """校验 zip 内容：条目数 + exe/supervisors/预置三件套在正确位置（带顶层目录前缀）。"""
    try:
        with zipfile.ZipFile(zip_path, "r") as zf:
            names = zf.namelist()
            test = zf.testzip()
    except Exception as e:  # noqa: BLE001
        print(f"[make-portable] zip 校验异常: {e}")
        return False
    has_exe = f"{top}/{EXE_NAME}" in names
    has_roles = any(n.startswith(f"{top}/supervisors/") for n in names)
    has_role_json = any(n.endswith("/role.json") for n in names)
    n_roles = sum(1 for n in names if n.startswith(f"{top}/supervisors/") and n.endswith(".png"))
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
        f"[make-portable] zip 校验: entries={len(names)} exe={has_exe} supervisors={has_roles}"
        f" role_json={has_role_json} png={n_roles} sounds={n_sounds} cfg={has_cfg}"
        f" stats={has_stats} escaped={len(escaped)} crc={test is None}"
    )
    if escaped:
        print(f"[make-portable] FAIL: 有条目跑到顶层目录外或含反斜杠: {escaped[:5]}")
    if not ok:
        print(f"[make-portable] FAIL: zip 内容不完整（crc 异常项={test}）")
    return ok


def find_7z() -> str:
    """找 7z 命令行：SEVEN_ZIP 环境变量 → PATH → 7-Zip 默认安装目录；都没有返回 ""。"""
    env = os.environ.get("SEVEN_ZIP", "").strip()
    if env and os.path.isfile(env):
        return env
    found = shutil.which("7z") or shutil.which("7za")
    if found:
        return found
    for p in SEVEN_ZIP_CANDIDATES:
        if os.path.isfile(p):
            return p
    return ""


def make_7z(src_exe: str, dest_7z: str) -> tuple:
    """把单个 exe 压成 7z（包内只有一层 rest.exe，自动更新解压后直接可用）。

    返回 (ok, 错误说明)。压缩后回读条目，确认包里只有这一个文件 —— 自动更新按
    文件名找新 exe，多一层目录或多一个文件都可能让替换走偏。
    """
    seven = find_7z()
    if not seven:
        return False, "找不到 7z 命令：请安装 7-Zip，或把 7z.exe 加进 PATH / 设 SEVEN_ZIP"
    if not os.path.isfile(src_exe):
        return False, f"找不到待压缩的 exe：{src_exe}"
    if os.path.exists(dest_7z):
        os.remove(dest_7z)
    name = os.path.basename(src_exe)
    print(f"[make-portable] 7z -> {dest_7z}（仅 {name}）")
    r = subprocess.run(
        [seven, "a", "-t7z", "-mx=9", "-y", dest_7z, name],
        cwd=os.path.dirname(src_exe),
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
    )
    if r.returncode != 0 or not os.path.exists(dest_7z):
        out = r.stdout.decode("utf-8", "replace")
        return False, f"7z 压缩失败（rc={r.returncode}）：\n{out}"
    # -ba：裸列表（无表头），每行最后一列是包内路径
    r2 = subprocess.run(
        [seven, "l", "-ba", dest_7z], stdout=subprocess.PIPE, stderr=subprocess.STDOUT
    )
    listing = r2.stdout.decode("utf-8", "replace")
    entries = [ln.split()[-1] for ln in listing.splitlines() if ln.strip()]
    if entries != [name]:
        return False, f"rest.7z 内容异常：期望只有 {name}，实际 {entries}"
    print(f"[make-portable] 7z 校验: entries={entries} size={os.path.getsize(dest_7z)}")
    return True, ""


def write_version_json(dest: str, tag: str, changes, date_text: str = "") -> bool:
    """生成自动更新清单：version 用 tag 全称，date 用 YYYY.MM.DD，changes 已编号。"""
    doc = {
        "version": tag,
        "date": date_text or datetime.now().strftime("%Y.%m.%d"),
        "url": list(UPDATE_BASE_URLS),
        "sites": [u + "version.json" for u in UPDATE_BASE_URLS],
        "changes": list(changes),
    }
    try:
        with open(dest, "w", encoding="utf-8", newline="\n") as f:
            f.write(json.dumps(doc, ensure_ascii=False, indent=2) + "\n")
    except OSError as e:
        print(f"[make-portable] FAIL: 写 version.json 失败: {e}")
        return False
    return True


def md5(p: str) -> str:
    h = hashlib.md5()
    with open(p, "rb") as f:
        for chunk in iter(lambda: f.read(65536), b""):
            h.update(chunk)
    return h.hexdigest()


def find_role_json(root: str, key: str):
    """在 supervisors/ 下按 key 反查某个督休专员的 role.json（目录名是专员名称，不固定）。"""
    sup = os.path.join(root, "supervisors")
    if not os.path.isdir(sup):
        return None
    for name in os.listdir(sup):
        p = os.path.join(sup, name, "role.json")
        if not os.path.isfile(p):
            continue
        try:
            with open(p, encoding="utf-8") as f:
                d = json.load(f)
        except Exception:
            continue
        if d.get("key") == key:
            return p
    return None


def main() -> int:
    ap = argparse.ArgumentParser(description="Go 版便携目录打包")
    ap.add_argument("--no-build", action="store_true", help="跳过 go build，直接用已有 exe")
    ap.add_argument("--out", default="", help="交付目录（默认 release/）")
    args = ap.parse_args()

    out_dir = os.path.abspath(args.out) if args.out else DEFAULT_OUT

    # 0. 版本号只认 tag：当前提交没打 tag 就停下，别产出「没版本号」的包
    tag = gitver.tag_at_head()
    if not tag:
        last = gitver.latest_tag()
        print("[make-portable] FAIL: 当前提交没有版本 tag，已停止打包。")
        print("[make-portable] 请先给这次提交打 tag（格式 v主.次.修订）：")
        if last:
            print(f"[make-portable]     git tag {suggest_next(last)}    # 基于最近的 {last}")
            print(f"[make-portable] 最近一次 tag：{last}")
        else:
            print("[make-portable]     git tag v0.9.0    # 仓库里还没有任何 tag")
        return 2

    version = tag[1:]  # 0.9.0 —— exe 内注入 / 产物文件名（不带 v）
    prev = gitver.prev_tag(tag)
    print(f"[make-portable] 版本: {tag}（exe 内注入 {version}）")
    if prev:
        print(f"[make-portable] changes 取 {prev}..{tag}")
    else:
        print("[make-portable] 没有更早的 tag：version.json 的 changes 留空")

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
        ("督休专员目录", find_role_json(dest, "supervisor_default")),
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

    # 6. 自动更新包 rest.7z（只含 rest.exe，自动更新下载的就是它）
    if ok:
        stage_7z = os.path.join(stage_parent, "rest.7z")
        made, why = make_7z(EXE_SRC, stage_7z)
        if made:
            final_7z = os.path.join(out_dir, "rest.7z")
            shutil.copy2(stage_7z, final_7z)
            print(f"[make-portable] 更新包: {final_7z} ({os.path.getsize(final_7z)} bytes)")
        else:
            ok = False
            print(f"[make-portable] FAIL: {why}")

    # 7. 自动更新清单 version.json（changes = 上一个 tag 到当前 tag 的提交标题）
    if ok:
        changes = [f"{i}. {c}" for i, c in enumerate(gitver.commits_between(prev, tag), 1)]
        vj = os.path.join(out_dir, "version.json")
        if write_version_json(vj, tag, changes):
            print(f"[make-portable] 版本清单: {vj}")
            for c in changes:
                print(f"    {c}")
        else:
            ok = False

    # 清掉临时组装目录
    shutil.rmtree(stage_parent, ignore_errors=True)
    print(f"[make-portable] {'DONE' if ok else 'FAILED'}")
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
