#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""Go 版便携目录打包脚本。

把构建好的发布版 exe 与随包资源组装成对外发布的便携目录、打成 zip，并生成
自动更新用的单文件包与版本清单：

    release/WorkerRest-<tag>/                本次发布的交付目录（tag 形如 v0.9.1）
        WorkerRest-portable/                   便携散装目录（解压即用，不带版本号）
        WorkerRest-portable.zip                便携目录 zip（不带版本号）
        rest.zip                               只含 rest.exe 的自动更新包（最大压缩）
        version.json                           自动更新清单

所有产物先落在 release/WorkerRest-<tag>/ 里再交付：同一版本的东西不会和别的版本
混在一起，目录名 / 压缩包名本身不带版本号（版本只在父目录名与 version.json 里）。

组装结构：
  - rest.exe + supervisors/（约 1 MB 内置督休专员模板，立绘已转 webp，子目录按专员名称命名）；
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
  --out DIR    交付根目录（默认 release/；产物落在其下 WorkerRest-<tag>/ 里）

退出码 0 = 打包成功且便携 zip / 更新包 zip / version.json 全部校验通过；
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

# 自动更新清单里的更新源，两个字段口径不同：
#   - url   更新包**下载**目录数组（update.go 补全规则：不以 .zip 结尾就拼 /rest.zip），
#           按顺序容错，第一项失败取下一项。
#   - sites **检查更新**地址数组（各指向一份 version.json）。客户端启动时先查本地
#           config/settings.json 的 sites，一旦远程 version.json 下发了非空 sites 就整体
#           覆盖本地。所以这里必须与 default_settings.json 的 sites 逐字一致，否则会出现
#           「装完查 A，检查一次更新后又跳回 B」。
# 官方地址只有 goodgoodlive 一个（软件里也把它当兜底源）。要加镜像就在这儿往数组里追加，
# 或者发布后直接改 version.json 的 url —— 程序按顺序容错，不针对任何具体域名做特殊处理。
UPDATE_BASE_URLS = [
    "https://www.goodgoodlive.com/worker-rest/",
]
UPDATE_SITES = [
    "https://www.goodgoodlive.com/worker-rest/version.json",
]

# 便携散装目录 / 便携 zip 的名字：固定不带版本号（版本只体现在父目录名里）
PORTABLE_DIR_NAME = "WorkerRest-portable"
PORTABLE_ZIP_NAME = "WorkerRest-portable.zip"

# 自动更新包文件名：必须与 update.go 的 packageName 一致（url 数组项按它补全）
UPDATE_PACKAGE_NAME = "rest.zip"

# 7-Zip 命令行：只用来生成最大压缩的 zip（SEVEN_ZIP 环境变量优先，其次 PATH，
# 最后找默认安装目录）；找不到时回退 Python zipfile level 9。
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


def copy_roles(dest: str) -> None:
    """把 supervisors/ 里「带 meta.json」的子目录逐个拷进包。

    只拷 meta.json 齐全的目录：scanRoles 不认没有 meta.json 的子目录（等于不存在），
    打进去只是白占体积，用户看到目录还会以为「这个专员怎么没显示」。所以纯素材目录
    （只有立绘、还没做 meta.json）天然不进包，不用另维护排除名单。
    """
    out = os.path.join(dest, "supervisors")
    os.makedirs(out, exist_ok=True)
    skipped = []
    for name in sorted(os.listdir(ROLES_SRC)):
        src = os.path.join(ROLES_SRC, name)
        if not os.path.isdir(src):
            continue
        if not os.path.isfile(os.path.join(src, "meta.json")):
            skipped.append(name)
            continue
        shutil.copytree(src, os.path.join(out, name))
    print(f"[make-portable] 督休专员目录: {len(os.listdir(out))} 个")
    if skipped:
        print(f"[make-portable] 跳过无 meta.json 的素材目录: {', '.join(skipped)}")


def assemble(dest: str, version: str) -> bool:
    """把 exe + supervisors/ + 预置三件套组装进 dest（可分发便携目录）。"""
    if os.path.exists(dest):
        shutil.rmtree(dest, ignore_errors=True)
    os.makedirs(dest, exist_ok=True)
    shutil.copy2(EXE_SRC, os.path.join(dest, EXE_NAME))
    copy_roles(dest)
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
    # 预置配置写「exe 内嵌的那份」而不是直接拷仓库文件：点「恢复默认设置」拿到的就是内嵌
    # 那份，字节同源才不会有「首装看到 A、恢复默认回 B」这种界面上看不出来的错位。
    try:
        cfg_bytes = dump_embedded_defaults(os.path.join(dest, EXE_NAME))
    except RuntimeError as e:  # noqa: BLE001
        print(f"[make-portable] FAIL: {e}")
        return False
    with open(os.path.join(dest, "config", "settings.json"), "wb") as f:
        f.write(cfg_bytes)
    os.makedirs(os.path.join(dest, "data"), exist_ok=True)
    with open(os.path.join(dest, "data", "stats.json"), "w", encoding="utf-8", newline="\n") as f:
        f.write(EMPTY_STATS_JSON)
    return True


def zip_dir(zip_path: str, src_dir: str) -> bool:
    """打 zip（含顶层目录：解压得到 WorkerRest-portable/，目录名不带版本号）。

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
    has_role_meta = any(n.endswith("/meta.json") for n in names)
    # 立绘可能是 png 也可能是 webp（2026-10 起随包统一 webp），两种都算
    n_roles = sum(
        1 for n in names
        if n.startswith(f"{top}/supervisors/")
        and n.lower().endswith((".png", ".webp", ".jpg", ".jpeg", ".gif"))
    )
    # 预置三件套：四个内置提示音 + 出厂默认配置 + 空统计表
    n_sounds = sum(1 for n in names if n.startswith(f"{top}/sounds/") and n.endswith(".wav"))
    has_cfg = f"{top}/config/settings.json" in names
    has_stats = f"{top}/data/stats.json" in names
    # 顶层目录硬保证：每个条目都必须落在 top/ 里，且不允许出现反斜杠条目
    # （反斜杠会被一部分解压工具当文件名的一部分，解压后文件散落一地）
    escaped = [n for n in names if not n.startswith(top + "/") or "\\" in n]
    ok = (
        test is None and has_exe and has_roles and has_role_meta
        and n_sounds == 4 and has_cfg and has_stats and not escaped
    )
    print(
        f"[make-portable] zip 校验: entries={len(names)} exe={has_exe} supervisors={has_roles}"
        f" role_meta={has_role_meta} img={n_roles} sounds={n_sounds} cfg={has_cfg}"
        f" stats={has_stats} escaped={len(escaped)} crc={test is None}"
    )
    if escaped:
        print(f"[make-portable] FAIL: 有条目跑到顶层目录外或含反斜杠: {escaped[:5]}")
    if not ok:
        print(f"[make-portable] FAIL: zip 内容不完整（crc 异常项={test}）")
    return ok


def find_7z() -> str:
    """找 7-Zip 命令行（只用于生成最大压缩 zip）：
    SEVEN_ZIP 环境变量 → PATH → 7-Zip 默认安装目录；都没有返回 ""（回退 Python）。"""
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


def make_update_zip(src_exe: str, dest_zip: str) -> tuple:
    """把单个 exe 压成「最大压缩」的 zip（包内只有一层 rest.exe，自动更新解压后
    直接可用）。

    压缩方式：优先 7-Zip CLI 的 deflate 极限档
    （-tzip -mx=9 -mfb=258 -mpass=15）；找不到 7-Zip 时回退 Python zipfile
    （ZIP_DEFLATED level 9）。产物都是标准 zip，Go 侧用 archive/zip 解压，两种来源
    都能读。

    返回 (ok, 错误说明)。压完回读条目，确认包里只有这一个文件 —— 自动更新按
    文件名找新 exe，多一层目录或多一个文件都可能让替换走偏。
    """
    if not os.path.isfile(src_exe):
        return False, f"找不到待压缩的 exe：{src_exe}"
    if os.path.exists(dest_zip):
        os.remove(dest_zip)
    name = os.path.basename(src_exe)
    seven = find_7z()
    if seven:
        print(f"[make-portable] 更新包 zip -> {dest_zip}（7-Zip -mx=9 极限压缩，仅 {name}）")
        r = subprocess.run(
            [seven, "a", "-tzip", "-mx=9", "-mfb=258", "-mpass=15", "-y", dest_zip, name],
            cwd=os.path.dirname(src_exe),
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
        )
        if r.returncode != 0 or not os.path.exists(dest_zip):
            out = r.stdout.decode("utf-8", "replace")
            return False, f"7-Zip 生成 zip 失败（rc={r.returncode}）：\n{out}"
    else:
        print(
            f"[make-portable] 更新包 zip -> {dest_zip}"
            f"（未找到 7-Zip，回退 Python zipfile level 9，仅 {name}）"
        )
        try:
            with zipfile.ZipFile(dest_zip, "w", zipfile.ZIP_DEFLATED, compresslevel=9) as zf:
                zf.write(src_exe, name)
        except OSError as e:
            return False, f"Python zipfile 打包失败：{e}"
    # 回读校验：crc 正常、条目恰好一个、且就是 name（不依赖 7z 列表，两种来源都适用）
    try:
        with zipfile.ZipFile(dest_zip, "r") as zf:
            entries = zf.namelist()
            bad = zf.testzip()
    except (OSError, zipfile.BadZipFile) as e:
        return False, f"更新包 zip 读回失败：{e}"
    if bad is not None:
        return False, f"更新包 zip 有损坏条目：{bad}"
    if entries != [name]:
        return False, f"{UPDATE_PACKAGE_NAME} 内容异常：期望只有 {name}，实际 {entries}"
    print(f"[make-portable] 更新包校验: entries={entries} size={os.path.getsize(dest_zip)}")
    return True, ""


def write_version_json(dest: str, tag: str, changes, date_text: str = "") -> bool:
    """生成自动更新清单：version 用 tag 全称，date 用 YYYY.MM.DD，changes 已编号。"""
    doc = {
        "version": tag,
        "date": date_text or datetime.now().strftime("%Y.%m.%d"),
        "url": list(UPDATE_BASE_URLS),
        "sites": list(UPDATE_SITES),
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


def find_role_meta(root: str, name: str):
    """在 supervisors/ 下按专员名反查 meta.json（目录名是名字的投影，可能被清洗过）。"""
    sup = os.path.join(root, "supervisors")
    if not os.path.isdir(sup):
        return None
    for dn in os.listdir(sup):
        p = os.path.join(sup, dn, "meta.json")
        if not os.path.isfile(p):
            continue
        try:
            with open(p, encoding="utf-8") as f:
                d = json.load(f)
        except Exception:
            continue
        nm = str((d or {}).get("name") or dn).strip() or dn
        if nm == name:
            return p
    return None


def check_default_sites() -> str:
    """出厂默认配置的 sites 必须与 UPDATE_SITES 逐字一致，返回空串表示通过。

    便携包里的 config/settings.json 就是 default_settings.json 的副本，而 version.json
    下发的 sites 会整体覆盖客户端本地 sites —— 两边不一致就会出现「装完查 A、检查一次
    更新后又跳回 B」。发布前在这里挡住，比发出去再发现便宜。
    """
    try:
        with open(DEFAULT_SETTINGS_SRC, encoding="utf-8") as f:
            doc = json.load(f)
    except (OSError, ValueError) as e:
        return f"读 default_settings.json 失败：{e}"
    got = doc.get("sites")
    if not isinstance(got, list):
        return f"default_settings.json 的 sites 不是数组：{got!r}"
    if [str(x).strip() for x in got] != list(UPDATE_SITES):
        return (f"default_settings.json 的 sites 与 UPDATE_SITES 不一致：\n"
                f"    default_settings.json = {got}\n"
                f"    UPDATE_SITES         = {list(UPDATE_SITES)}")
    return ""


# ---- 「恢复默认设置」的内嵌默认 vs 随包预置配置 ----
# 两条路径吃的是两份可能不同的东西：exe 内嵌的 default_settings.json（go:embed，恢复默认
# 的数据源）与便携包里的 config/settings.json（新装首跑读它，由本脚本从仓库根复制）。
# 正常流程（每次 go build 后再组装）两者天然同源，但 `--no-build` 用上一轮 exe 时就会分叉：
# 包里是新值、点「恢复默认」回旧值，界面上完全看不出来。所以组装前必须让 exe 自己吐出来核对。
_DUMP_CACHE = {}


def dump_embedded_defaults(exe: str) -> bytes:
    """让 exe 把内嵌的出厂默认原样写到临时文件并读回（`--dump-default-settings <out>`）。

    失败抛 RuntimeError。最常见原因是 exe 是旧构建（还不认识这个参数，会当普通参数直接
    启动 GUI 挂着），因此带超时 + 「文件是否真的写到」两重判断。"""
    if exe in _DUMP_CACHE:
        return _DUMP_CACHE[exe]
    out = os.path.join(TEMP, "wr-embedded-default.json")
    if os.path.exists(out):
        os.remove(out)
    try:
        subprocess.run(
            [exe, "--dump-default-settings", out],
            cwd=os.path.dirname(exe), timeout=30,
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
        )
    except subprocess.TimeoutExpired:
        raise RuntimeError("exe 对 --dump-default-settings 无响应（很可能是没重新构建的旧版本）")
    except OSError as e:  # noqa: BLE001
        raise RuntimeError(f"无法执行 {exe}：{e}")
    if not os.path.exists(out):
        raise RuntimeError("exe 没有导出内嵌默认（旧版本不支持该参数？先重新构建 exe）")
    with open(out, "rb") as f:
        data = f.read()
    _DUMP_CACHE[exe] = data
    return data


def _flatten(v, prefix=""):
    """把嵌套 JSON 摊成 {键路径: 值}，用于打印「到底哪一项不一致」。"""
    if isinstance(v, dict):
        out = {}
        for k, vv in v.items():
            out.update(_flatten(vv, f"{prefix}.{k}" if prefix else str(k)))
        return out
    return {prefix: v}


def diff_defaults(a, b, limit: int = 8) -> str:
    fa, fb = _flatten(a), _flatten(b)
    lines = []
    for k in sorted(set(fa) | set(fb)):
        va, vb = fa.get(k, "<缺>"), fb.get(k, "<缺>")
        if va != vb:
            lines.append(f"    {k}: exe内嵌={va!r}  文件={vb!r}")
            if len(lines) >= limit:
                lines.append("    …（只列前 %d 处）" % limit)
                break
    return "\n".join(lines)


def check_embedded_defaults(exe: str) -> str:
    """exe 内嵌的出厂默认必须与仓库根 default_settings.json 语义一致，返回空串表示通过。"""
    try:
        mine = json.loads(dump_embedded_defaults(exe).decode("utf-8"))
    except (RuntimeError, ValueError, UnicodeDecodeError) as e:  # noqa: BLE001
        return f"取 exe 内嵌默认失败：{e}"
    try:
        with open(DEFAULT_SETTINGS_SRC, encoding="utf-8") as f:
            disk = json.load(f)
    except (OSError, ValueError) as e:  # noqa: BLE001
        return f"读 default_settings.json 失败：{e}"
    if mine != disk:
        return ("exe 内嵌的出厂默认与 default_settings.json 不一致（exe 是旧构建？"
                "--no-build 最容易踩这个）：\n" + diff_defaults(mine, disk))
    return ""


def main() -> int:
    ap = argparse.ArgumentParser(description="Go 版便携目录打包")
    ap.add_argument("--no-build", action="store_true", help="跳过 go build，直接用已有 exe")
    ap.add_argument("--out", default="", help="交付根目录（默认 release/，其下再建 WorkerRest-<tag>/）")
    args = ap.parse_args()

    out_dir = os.path.abspath(args.out) if args.out else DEFAULT_OUT

    # 出厂默认更新源必须与发布清单的 sites 一致，不一致直接停（别产出会「跳源」的包）
    why = check_default_sites()
    if why:
        print("[make-portable] FAIL: " + why)
        return 1

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

    # 1b. exe 里「恢复默认设置」用的内嵌默认，必须与要进包的 default_settings.json 一致。
    #     不构建（--no-build）拿上一轮 exe 时最容易在这里翻车，所以卡在组装之前。
    why = check_embedded_defaults(EXE_SRC)
    if why:
        print("[make-portable] FAIL: " + why)
        return 1
    print("[make-portable] 内嵌出厂默认 == default_settings.json")

    # 2. 组装（在系统临时目录做，避免 release/ 历史产物被锁）
    stage_parent = os.path.join(TEMP, "wr-portable-build")
    dest = os.path.join(stage_parent, PORTABLE_DIR_NAME)
    assemble(dest, version)

    # 3. 结构核验
    checks = [
        ("exe", os.path.join(dest, EXE_NAME)),
        ("督休专员目录", find_role_meta(dest, "眼镜主管")),
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
    zip_path = os.path.join(stage_parent, PORTABLE_ZIP_NAME)
    if ok and zip_dir(zip_path, dest):
        ok = ok and verify_zip(zip_path, PORTABLE_DIR_NAME)

    # 5. 交付：本次版本一个子目录 release/WorkerRest-<tag>/，四种产物都放进去
    release_dir = os.path.join(out_dir, f"WorkerRest-{tag}")
    final_zip = os.path.join(release_dir, PORTABLE_ZIP_NAME)
    if ok:
        os.makedirs(release_dir, exist_ok=True)
        shutil.copy2(zip_path, final_zip)
        # 散装目录也拷回（用户可直接解压/拷贝使用，不依赖 zip）
        final_dir = os.path.join(release_dir, PORTABLE_DIR_NAME)
        if os.path.exists(final_dir):
            shutil.rmtree(final_dir, ignore_errors=True)
        shutil.copytree(dest, final_dir)
        print(f"[make-portable] 交付根: {release_dir}")
        print(f"[make-portable] 交付 zip: {final_zip} ({os.path.getsize(final_zip)} bytes)")
        print(f"[make-portable] 交付目录: {final_dir}")

    # 6. 自动更新包 rest.zip（只含 rest.exe，最大压缩；自动更新下载的就是它）
    if ok:
        stage_pkg = os.path.join(stage_parent, UPDATE_PACKAGE_NAME)
        made, why = make_update_zip(EXE_SRC, stage_pkg)
        if made:
            final_pkg = os.path.join(release_dir, UPDATE_PACKAGE_NAME)
            shutil.copy2(stage_pkg, final_pkg)
            print(f"[make-portable] 更新包: {final_pkg} ({os.path.getsize(final_pkg)} bytes)")
        else:
            ok = False
            print(f"[make-portable] FAIL: {why}")

    # 7. 自动更新清单 version.json（changes = 上一个 tag 到当前 tag 的提交标题）
    version_json = ""
    if ok:
        changes = [f"{i}. {c}" for i, c in enumerate(gitver.commits_between(prev, tag), 1)]
        vj = os.path.join(release_dir, "version.json")
        if write_version_json(vj, tag, changes):
            version_json = vj
            print(f"[make-portable] 版本清单: {vj}")
            for c in changes:
                print(f"    {c}")
        else:
            ok = False

    # 清掉临时组装目录
    shutil.rmtree(stage_parent, ignore_errors=True)
    print(f"[make-portable] {'DONE' if ok else 'FAILED'}")

    # 收尾提醒：changes 是提交标题的机械摘要，发布前通常要人工改写
    if ok and version_json:
        print("[make-portable] " + "-" * 60)
        print("[make-portable] 下一步：修改 version.json 里的 changes")
        print("[make-portable]   上面列出的条目是「上一个 tag..当前 tag」的提交标题，")
        print("[make-portable]   面向用户发布前请手动改写成可读的变更说明，例如")
        print("[make-portable]     \"changes\": [\"1. 修复了一些已知问题\"]")
        print(f"[make-portable]   待编辑文件：{version_json}")
        print("[make-portable] " + "-" * 60)
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
