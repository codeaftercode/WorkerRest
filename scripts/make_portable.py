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
  - 预置两件套（运行数据随包分发，首跑直接在预置文件上继续写）：
      sounds/               四个内置提示音（源文件在模块根 sounds/，不再 embed 进二进制）
      config/settings.json  出厂默认配置（与 go:embed 的 default_settings.json 同一
                            份文件，天然零漂移；固定键是空串，首跑自动补生成并回写）
  - **不预置 data/ 与 stats.json**：用户的统计数据不属于安装包。首跑由 stats.go 的
    Load 自己建 data/stats.json（见其四情形说明）；文件损坏时也会改名备份再重建。
  - 运行时残留（exe 同目录跑过一次后留下的 config/data/sounds 等）组装前一律清掉。

版本号：**只认当前提交上的 git tag**（格式 v主.次.修订，见 scripts/version.py）。
  - 当前提交没有 tag → 直接停止打包，提示先打 tag，并显示最近一次 tag 供参考；
  - 有 tag → 用它作为本次构建版本：exe 内注入去掉 v 的版本号（ldflags），
    产物文件名用该版本号，version.json 里写完整 tag。
  app.go 的 appVersion 只是 "dev" 占位符，不是版本来源。

version.json 的 changes：上一个 tag 到当前 tag 之间的提交标题，按时间顺序（旧→新）
自动编号（"1. xxx"）；首个 tag 没有上一个 tag，changes 留空。

version.json 的 sha256 / size：**由本脚本从刚生成的 rest.zip 现算**，不是手填。
客户端只认「哈希对得上」的包（对不上就丢弃、换镜像、全失败则拒绝更新），
所以这两个字段缺失时打包直接失败 —— 没有哈希的清单等于没有更新通道。

用法：python scripts/make_portable.py [--no-build] [--out DIR]
  --no-build   跳过 go build，直接用 build/bin/rest.exe 组装（已构建过时用）
  --out DIR    交付根目录（默认 release/；产物落在其下 WorkerRest-<tag>/ 里）

退出码 0 = 打包成功且便携 zip / 更新包 zip / version.json 全部校验通过；
       2 = 当前提交没有版本 tag（未开始构建）；
       1 = 其它失败。
"""

import argparse
import glob
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
# （logs/ 是 2026-10 起 logging.go 默认写的 <exe>/logs/app.log，不属于安装包内容）
RUNTIME_REL = ["config", "data", "sounds", "images", "stats.json", "logs"]

# 开源鸣谢清单：同一份文件有两个去处 ——（a）被 //go:embed 嵌进 exe（见 notices.go），
# （b）随便携包放在根目录。只发 exe 的用户靠内嵌 + 关于页入口，收整包的用户直接能读。
NOTICES_NAME = "THIRD-PARTY-NOTICES.txt"
NOTICES_SRC = os.path.join(ROOT, NOTICES_NAME)

# 清除开机自启动项脚本：便携版没有安装器（删文件夹就算卸载），但用户勾过「开机自启动」
# 就往注册表里写过一项，删了文件夹之后它指向一个不存在的 exe，任务管理器「启动」页里
# 还留着一条。随包放这个小脚本专门清它 —— 名字必须如实说明「只清这一项」，
# 不能叫「卸载」：它不删任何文件，叫卸载会让用户以为删完了。
CLEANER_NAME = "清除开机自启动项.bat"
CLEANER_SRC = os.path.join(HERE, CLEANER_NAME)

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
    """构建发布版 exe（带版本号 + 发布时间注入 + windowsgui + 去符号 + 去路径）。

    -trimpath 是**分发必需**：不加它，每个源文件都会以 `D:/Projects/.../xxx.go`
    的形式进二进制，等于把开发机的目录结构、用户名一起发出去（panic 栈、
    runtime.Caller、DWARF 全是它）。加了之后文件路径变成 `workerrest/xxx.go`。
    注意它与 -X main.buildTime=<当前时间> 天然互斥于「逐字节可复现」——
    剥离路径是为了不泄漏，不是为了 bit-for-bit 复现。
    """
    exe = EXE_SRC
    # 发布时间用 RFC3339 且无空格（ISO 格式），避免破坏单条 ldflags 字符串；
    # 前端「版本 | 发布时间」行在时间为空时回退显示「本地构建」。
    build_time = datetime.now().astimezone().isoformat()
    ldflags = f'-X main.appVersion={version} -X main.buildTime={build_time} -H windowsgui -s -w'
    print(f"[make-portable] go build -trimpath -> {exe} (version={version})")
    r = subprocess.run(
        ["go", "build", "-trimpath", "-o", exe, "-ldflags", ldflags, "."],
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
    # 开源鸣谢清单：与内嵌进 exe 的那份逐字节相同（打包前已 --check 过）
    if os.path.isfile(NOTICES_SRC):
        shutil.copy2(NOTICES_SRC, os.path.join(dest, NOTICES_NAME))
    # 清除开机自启动项脚本：便携版没有卸载器，用户删文件夹前用它把注册表里那一项清掉
    if os.path.isfile(CLEANER_SRC):
        shutil.copy2(CLEANER_SRC, os.path.join(dest, CLEANER_NAME))
    copy_roles(dest)
    # 清掉运行时残留（supervisors/ 里理论上没有，但 exe 同目录可能被组装源污染）
    for rel in RUNTIME_REL:
        p = os.path.join(dest, rel)
        if os.path.isdir(p):
            shutil.rmtree(p, ignore_errors=True)
        elif os.path.exists(p):
            os.remove(p)
    # 预置两件套：sounds/（内置提示音）、config/settings.json（出厂默认）。
    # 首跑直接在预置文件上继续写（固定键由 Go 侧补生成并回写）。
    # data/ 与 stats.json **不进包**：统计是用户数据，首跑由 stats.go 的 Load 自己建
    # （缺就建空表、坏就改名备份后重建），更新时也不会被安装包覆盖或删除。
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
    # 开源鸣谢清单必须在根目录：它是 MIT / BSD-3「随分发复现」义务的载体之一，
    # 内嵌进 exe 的那份用户未必看得到，包里这份是「打开就能读」的最短路径
    has_notices = f"{top}/{NOTICES_NAME}" in names
    # 清启动项脚本：便携版「卸载」只剩这一处残留，脚本丢了用户就只能自己手改注册表
    has_cleaner = f"{top}/{CLEANER_NAME}" in names
    # 统计是用户数据：安装包与更新包里**必须没有** data/（首跑自动创建，见 stats.go Load）
    data_entries = [n for n in names if n.startswith(f"{top}/data/") or n == f"{top}/stats.json"]
    no_data = not data_entries
    # 顶层目录硬保证：每个条目都必须落在 top/ 里，且不允许出现反斜杠条目
    # （反斜杠会被一部分解压工具当文件名的一部分，解压后文件散落一地）
    escaped = [n for n in names if not n.startswith(top + "/") or "\\" in n]
    ok = (
        test is None and has_exe and has_roles and has_role_meta
        and n_sounds == 4 and has_cfg and has_notices and has_cleaner
        and no_data and not escaped
    )
    print(
        f"[make-portable] zip 校验: entries={len(names)} exe={has_exe} supervisors={has_roles}"
        f" role_meta={has_role_meta} img={n_roles} sounds={n_sounds} cfg={has_cfg}"
        f" notices={has_notices} cleaner={has_cleaner} no_data={no_data}"
        f" escaped={len(escaped)} crc={test is None}"
    )
    if not has_notices:
        print(f"[make-portable] FAIL: 包里缺 {top}/{NOTICES_NAME}（开源鸣谢清单）")
    if not has_cleaner:
        print(f"[make-portable] FAIL: 包里缺 {top}/{CLEANER_NAME}（清除开机自启动项脚本）")
    if data_entries:
        print(f"[make-portable] FAIL: 包里不该有统计文件 / data 目录: {data_entries[:5]}")
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


def sha256_of(p: str) -> str:
    h = hashlib.sha256()
    with open(p, "rb") as f:
        for chunk in iter(lambda: f.read(65536), b""):
            h.update(chunk)
    return h.hexdigest()


def write_version_json(dest: str, tag: str, changes, package_path: str, date_text: str = "") -> bool:
    """生成自动更新清单：version 用 tag 全称，date 用 YYYY.MM.DD，changes 已编号，
    sha256/size 由 package_path（rest.zip）现算 —— 客户端拿它校验下载下来的包。"""
    if not package_path or not os.path.isfile(package_path):
        print("[make-portable] FAIL: 更新包不存在，无法计算 sha256（先确认第 6 步成功）")
        return False
    doc = {
        "version": tag,
        "date": date_text or datetime.now().strftime("%Y.%m.%d"),
        "url": list(UPDATE_BASE_URLS),
        "sites": list(UPDATE_SITES),
        "changes": list(changes),
        "sha256": sha256_of(package_path),
        "size": os.path.getsize(package_path),
    }
    try:
        with open(dest, "w", encoding="utf-8", newline="\n") as f:
            f.write(json.dumps(doc, ensure_ascii=False, indent=2) + "\n")
    except OSError as e:
        print(f"[make-portable] FAIL: 写 version.json 失败: {e}")
        return False
    print(f"[make-portable] version.json sha256={doc['sha256']} size={doc['size']}")
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


# ---- exe 新鲜度：`--no-build` 的防线 ----
# build/bin 里会留着上一轮的 exe（审计 域2-3 逮到过一条 6 天前的产物），`--no-build`
# 会把它原样打进发布包。check_embedded_defaults 只能发现「内嵌默认与磁盘不一致」，
# 代码层面的过期（改了逻辑、修了 bug、换了前端资源）在那里完全看不出来 ——
# 所以再补一条时间线核对：exe 必须比**所有会被编进它的东西**新。
_EMBED_DIRECTIVE = re.compile(r"^\s*//go:embed\s+(.+)$", re.MULTILINE)


def _compiled_sources() -> list:
    """可能改变 exe 内容的文件全集：非测试 *.go、go.mod/go.sum、`//go:embed` 的资源。

    直接解析 `//go:embed` 指令而不是手写一份资源清单：漏写清单的代价是「新加的内嵌
    资源改了也放行」，而那正是这条防线要防的事。`all:` 前缀只表示「连隐藏文件一起」，
    剥掉后按普通 glob 处理。
    """
    files = []
    for name in sorted(os.listdir(ROOT)):
        if name.endswith(".go") and not name.endswith("_test.go"):
            files.append(os.path.join(ROOT, name))
    for name in ("go.mod", "go.sum"):
        p = os.path.join(ROOT, name)
        if os.path.isfile(p):
            files.append(p)
    for name in sorted(os.listdir(ROOT)):
        if not name.endswith(".go"):
            continue
        try:
            with open(os.path.join(ROOT, name), encoding="utf-8", errors="replace") as f:
                src = f.read()
        except OSError:
            continue
        for m in _EMBED_DIRECTIVE.finditer(src):
            for raw in m.group(1).split():
                pat = raw[4:] if raw.startswith("all:") else raw
                for hit in glob.glob(os.path.join(ROOT, pat), recursive=True):
                    if os.path.isdir(hit):
                        for dirpath, _dirs, names in os.walk(hit):
                            files.extend(os.path.join(dirpath, n) for n in names)
                    else:
                        files.append(hit)
    seen, out = set(), []
    for p in files:
        if p not in seen:
            seen.add(p)
            out.append(p)
    return out


def check_exe_freshness(exe: str) -> str:
    """exe 不能比源码/资源旧，返回空串表示通过。

    1 秒容差：go build 与源文件写入可能落在同一秒，亚秒级差异不构成「过期」；
    真正过期的产物差的是小时/天，不会被容差放过。
    """
    try:
        exe_m = os.path.getmtime(exe)
    except OSError as e:  # noqa: BLE001
        return f"取 {exe} 的修改时间失败：{e}"
    newer = []
    for p in _compiled_sources():
        try:
            m = os.path.getmtime(p)
        except OSError:
            continue
        if m > exe_m + 1:
            newer.append((m, p))
    if not newer:
        return ""
    newer.sort(reverse=True)
    shown = "\n".join(
        f"    {datetime.fromtimestamp(m):%Y-%m-%d %H:%M:%S}  {os.path.relpath(p, ROOT)}"
        for m, p in newer[:5]
    )
    return (
        "exe 比源码旧，拒绝打包（--no-build 最常踩这个坑：build/bin 里留着上一轮的产物）\n"
        f"    exe       {datetime.fromtimestamp(exe_m):%Y-%m-%d %H:%M:%S}  {exe}\n"
        f"  比 exe 新的文件共 {len(newer)} 个（按时间倒序取前 5）：\n{shown}\n"
        "  处理：去掉 --no-build 重新构建，或先手动 go build 再打包。"
    )


def check_winres(exe: str) -> str:
    """exe 必须带图标 + VERSIONINFO + MANIFEST 三类 Windows 资源，返回空串表示通过。

    审计 域2-4 逮到过「exe 既无 VERSIONINFO 也无应用清单」：.syso 只嵌了图标
    （早期 rsrc 方案），资源管理器属性页看不到版本号，也没有 longPathAware /
    supportedOS 清单。这里让 verify_windows_res.py 解析 PE 的 .rsrc 节断言三类资源
    齐全，把「发出去的 exe 缺版本信息」挡在打包之前。

    同时跑 gen_windows_res.py --check：syso 里的 VERSIONINFO 版本号是「最近的 tag（去 v，
    不足四段补 0）」，随源码入库。打了新 tag 后如果忘了重新生成 syso，这里
    能发现「syso 的版本号与当前 git 版本不一致」—— 和 ldflags 注入的 appVersion 一样，
    版本来源唯一（git tag），任何一处漂移都要挡。
    """
    script = os.path.join(HERE, "verify_windows_res.py")
    if not os.path.isfile(script):
        return f"找不到 {script}"
    r = subprocess.run([sys.executable, script, exe],
                       cwd=ROOT, capture_output=True, text=True,
                       encoding="utf-8", errors="replace")
    out = ((r.stdout or "") + (r.stderr or "")).strip()
    if r.returncode != 0:
        return out or "exe 的 Windows 资源（图标/VERSIONINFO/MANIFEST）校验失败"
    print("[make-portable] " + (out or "Windows 资源（图标/VERSIONINFO/MANIFEST）ok"))

    # syso 与当前 git 版本一致（tag 变了要重新生成，否则 VERSIONINFO 版本号是旧的）
    gen = os.path.join(HERE, "gen_windows_res.py")
    if not os.path.isfile(gen):
        return f"找不到 {gen}"
    r2 = subprocess.run([sys.executable, gen, "--check"],
                        cwd=ROOT, capture_output=True, text=True,
                        encoding="utf-8", errors="replace")
    out2 = ((r2.stdout or "") + (r2.stderr or "")).strip()
    if r2.returncode != 0:
        return out2 or "syso 与当前 git 版本不一致"
    print("[make-portable] " + (out2 or "syso 与当前版本一致"))
    return ""


def check_notices() -> str:
    """开源鸣谢清单必须与当前构建一致，返回空串表示通过。

    必须在 `go build` **之前**跑：清单是被 //go:embed 编进 exe 的，一旦编进去，
    后面的所有校验都看不出问题（内嵌与仓库文件会"一致地过期"）。
    依赖升版本 / 换 Go 版本后忘了重新生成，就在这里挡住，而不是发给用户一份
    声明与实际链接版本不符的包 —— 那比漏声明更糟。
    """
    script = os.path.join(HERE, "gen_notices.py")
    if not os.path.isfile(script):
        return f"找不到 {script}"
    r = subprocess.run([sys.executable, script, "--check"], cwd=ROOT,
                       capture_output=True, text=True, encoding="utf-8", errors="replace")
    out = ((r.stdout or "") + (r.stderr or "")).strip()
    if r.returncode != 0:
        return out or "开源鸣谢清单与当前构建不一致"
    print("[make-portable] " + (out or "开源鸣谢清单 ok"))
    return ""


def autostart_facts() -> dict:
    """从 sys_autostart.go 解析出值名与两处键路径（唯一真源在 Go 源码里）。

    清脚本里硬编码了一份，打包时逐字比对。不比对的话，改了值名脚本还在删旧名字 ——
    这种失败对用户是静默的：屏幕上说「已删除」，开机照样启动，而且无从察觉。
    """
    path = os.path.join(ROOT, "sys_autostart.go")
    try:
        with open(path, encoding="utf-8") as f:
            src = f.read()
    except OSError:
        return {}
    out = {}
    m = re.search(r'^const runValueName\s*=\s*"([^"]+)"', src, re.M)
    if m:
        out["value"] = m.group(1)
    for key, name in (("run", "runKeyPath"), ("approved", "approvedKeyPath")):
        m = re.search(r"^\s*%s\s*=\s*`([^`]+)`" % name, src, re.M)
        if m:
            out[key] = m.group(1)
    return out


def check_cleaner() -> str:
    """随包的「清除开机自启动项」脚本必须存在、可被 cmd 正确读取、且与 Go 侧同源。"""
    if not os.path.isfile(CLEANER_SRC):
        return f"找不到 {CLEANER_SRC}"
    raw = open(CLEANER_SRC, "rb").read()
    if raw.startswith(b"\xef\xbb\xbf"):
        return f"{CLEANER_NAME} 带 UTF-8 BOM：cmd 会把首行读成 '\ufeff@echo off' 并报错"
    try:
        text = raw.decode("gbk")
    except UnicodeDecodeError as e:
        # 中文 Windows 的 cmd 用 OEM 代码页 936 读批处理；存成 UTF-8 会满屏乱码
        return f"{CLEANER_NAME} 不是 GBK 编码：{e}"
    if raw.count(b"\r\n") != raw.count(b"\n"):
        return f"{CLEANER_NAME} 有裸 LF 换行：cmd 的标签/多行块会解析异常"
    facts = autostart_facts()
    if not facts.get("value"):
        return "解析不出 sys_autostart.go 的 runValueName（清脚本无法核对）"
    if facts["value"] not in text:
        return (f"{CLEANER_NAME} 里的值名与 sys_autostart.go 不一致："
                f"源码是 {facts['value']}，脚本里没有")
    for key in ("run", "approved"):
        want = "HKCU\\" + facts.get(key, "")
        if not facts.get(key):
            return f"解析不出 sys_autostart.go 的 {key}KeyPath"
        if want not in text:
            return f"{CLEANER_NAME} 缺少键路径 {want}"
    print(f"[make-portable] 清启动项脚本 ok（值名 {facts['value']}）")
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

    # 1. 构建 exe 之前先核对许可清单（它要被编进 exe，过期就必须在这里停）
    why = check_notices()
    if why:
        print("[make-portable] FAIL: " + why)
        return 1

    # 1b. 构建 exe
    if not args.no_build:
        if not build_exe(version):
            return 1
    if not os.path.exists(EXE_SRC):
        print(f"[make-portable] FAIL: 找不到 {EXE_SRC}（先构建，或用 --no-build 前先 go build）")
        return 1

    # 1c. exe 必须比源码新：`--no-build` 拿上一轮产物是合法用法，但「上一轮」必须是
    #     在**当前源码**上构建的。刚 go build 过时这里天然通过（成本只有一次 mtime 遍历），
    #     过期就停 —— 发出去的是代码没变的旧行为，比打不出包更糟。
    why = check_exe_freshness(EXE_SRC)
    if why:
        print("[make-portable] FAIL: " + why)
        return 1
    print("[make-portable] exe 比源码新")

    # 1c2. exe 必须带图标 + VERSIONINFO + MANIFEST（审计 域2-4）：缺版本信息/清单的
    #       exe 发出去后属性页看不到版本号、也没有长路径感知与 supportedOS 声明。
    why = check_winres(EXE_SRC)
    if why:
        print("[make-portable] FAIL: " + why)
        return 1

    # 1d. exe 里「恢复默认设置」用的内嵌默认，必须与要进包的 default_settings.json 一致。
    #     不构建（--no-build）拿上一轮 exe 时最容易在这里翻车，所以卡在组装之前。
    why = check_embedded_defaults(EXE_SRC)
    if why:
        print("[make-portable] FAIL: " + why)
        return 1
    print("[make-portable] 内嵌出厂默认 == default_settings.json")

    # 1e. 随包的清启动项脚本：编码/换行能被 cmd 正确读取，且值名与 Go 源码同源
    why = check_cleaner()
    if why:
        print("[make-portable] FAIL: " + why)
        return 1

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
        ("开源鸣谢清单", os.path.join(dest, NOTICES_NAME)),
        ("清除开机自启动项脚本", os.path.join(dest, CLEANER_NAME)),
    ]
    ok = True
    for name, p in checks:
        has = os.path.exists(p)
        if not has:
            ok = False
        print(f"[make-portable] {name}: {'ok' if has else 'MISSING'}（{p}）")

    # 反向核验：交付目录里**不许**有统计文件及其目录 —— 那是用户数据，首跑由程序自建
    stale = [os.path.join(dest, "data"), os.path.join(dest, "stats.json")]
    stale = [x for x in stale if os.path.exists(x)]
    if stale:
        ok = False
    print(f"[make-portable] 不预置统计文件: {'ok' if not stale else 'UNEXPECTED'}（{stale}）")

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
    final_pkg = ""
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

    # 7. 自动更新清单 version.json（changes = 上一个 tag 到当前 tag 的提交标题；
    #    sha256/size = 上面那个 rest.zip 的现算值，客户端据此校验下载的包）
    version_json = ""
    if ok:
        changes = [f"{i}. {c}" for i, c in enumerate(gitver.commits_between(prev, tag), 1)]
        vj = os.path.join(release_dir, "version.json")
        if write_version_json(vj, tag, changes, final_pkg):
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
