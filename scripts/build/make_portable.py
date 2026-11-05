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
                            份文件，天然零漂移；里面没有 seeded 标记与条目，
                            首跑由 Go 侧补种并回写）
  - **不预置 data/ 与 stats.json**：用户的统计数据不属于安装包。首跑由 stats.go 的
    Load 自己建 data/stats.json（见其四情形说明）；文件损坏时也会改名备份再重建。
  - 运行时残留（exe 同目录跑过一次后留下的 config/data/sounds 等）组装前一律清掉。

版本号：**只认当前提交上的 git tag**（格式 v主.次.修订，见 scripts/version.py）。
  - 当前提交没有 tag → 直接停止打包，提示先打 tag，并显示最近一次 tag 供参考；
  - 有 tag → 用它作为本次构建版本：exe 内注入去掉 v 的版本号（ldflags），
    产物文件名用该版本号，version.json 里写完整 tag。
  - 工作树有未提交改动 → 停止：编出来的是磁盘上的代码，而 tag 指的是另一个快照。
  app.go 的 appVersion 只是 "dev" 占位符，不是版本来源。

version.json 的 changes：上一个 tag 到当前 tag 之间的提交标题，按时间顺序（旧→新）
自动编号（"1. xxx"）；首个 tag 没有上一个 tag，changes 留空。

version.json 的 sha256 / size：**由本脚本从刚生成的 rest.zip 现算**，不是手填。
客户端只认「哈希对得上」的包（对不上就丢弃、换镜像、全失败则拒绝更新），
所以这两个字段缺失时打包直接失败 —— 没有哈希的清单等于没有更新通道。

用法：python scripts/build/make_portable.py [--no-build] [--out DIR] [--allow-dirty]
       双击 scripts/build/打包.bat 是同一件事（它会自己挑一个 3.9+ 的解释器，
       并校验版本 —— PATH 上的 python 未必是 3.x）。
  --no-build    跳过 go build，直接用 build/bin/rest.exe 组装（已构建过时用）
  --out DIR     交付根目录（默认 release/；产物落在其下 WorkerRest-<tag>/ 里）
  --allow-dirty 允许在未提交的工作树上打包：**只用于排查构建问题**。默认脏树即停 ——
                tag 指的是提交，工作树里的未提交改动不在那个 tag 里，打出来的包与
                「发布包 = 源码」这条审计链对不上。

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
ROOT = os.path.dirname(os.path.dirname(HERE))  # 项目根（原 rewrite-go，现已提升为顶层）
MAIN_DIR = os.path.join(ROOT, "cmd", "workerrest")  # main 包所在（含 embed 资源与 .syso）

# 同目录的 version.py（版本号只认 git tag）—— 显式加 sys.path，便于任意 cwd 下运行
if HERE not in sys.path:
    sys.path.insert(0, HERE)
import version as gitver  # noqa: E402

ROLES_SRC = os.path.join(ROOT, "assets", "supervisors")  # 内置专员模板（仓库里收在 assets/ 下）
SOUNDS_SRC = os.path.join(ROOT, "assets", "sounds")  # 内置提示音源文件（打包预置进便携包）
# 出厂默认的唯一源文件：internal/settings/default_settings.json 就是 //go:embed 嵌进 exe 的那份，
# 打包时再复制成便携包的 config/settings.json。全仓只此一份，不再有「根目录副本要跟着同步」的坑。
DEFAULT_SETTINGS_SRC = os.path.join(ROOT, "internal", "settings", "default_settings.json")
EXE_NAME = "rest.exe"
EXE_SRC = os.path.join(ROOT, "build", "bin", EXE_NAME)
TEMP = os.environ.get("TEMP", ROOT)

# 运行产物目录：便携目录跑过一次后会留下这些，组装发布目录时清掉
# （logs/ 是 2026-10 起 logging.go 默认写的 <exe>/logs/app.log，不属于安装包内容）
RUNTIME_REL = ["config", "data", "sounds", "images", "stats.json", "logs"]

# 开源鸣谢清单与项目许可证：两者都**只**内嵌进 exe（notices.go / license.go），
# 不再随便携包散放。理由是分发形态决定的：更新包 rest.zip 只换 exe，目录里那份文件
# 从首装那天起就再也不会变 —— 与其发一份「一致地过期」的快照，不如只留 exe 里那份，
# 它跟着版本走。用户要文件就点关于页的「另存为」，那是这两段文本唯一的出口。
NOTICES_NAME = "THIRD-PARTY-NOTICES.txt"

# 项目自己的许可证：和鸣谢清单是两个方向 —— 清单是「我用了别人的什么」，
# LICENSE 是「别人可以拿这个项目做什么」。真源在仓库根（GitHub/Gitee 认这个位置），
# go:embed 不能跨目录引用 ../../LICENSE，所以 cmd/workerrest/ 下放了一份逐字节副本，
# 打包前在这里比对两者 —— 改了许可证忘了刷新副本，出包就停。
LICENSE_NAME = "LICENSE"
LICENSE_SRC = os.path.join(ROOT, LICENSE_NAME)
LICENSE_EMBED_SRC = os.path.join(MAIN_DIR, LICENSE_NAME)
# MIT 正文的四段关键条款（授权 / 保留声明 / 免责 / 责任限制）。只校验存在与关键词，
# 不逐字比对正文 —— 换行、版权人、年份都可能被改，那几处不属于「完整性」。
LICENSE_REQUIRED = [
    "Permission is hereby granted, free of charge",
    "The above copyright notice and this permission notice shall be included",
    'THE SOFTWARE IS PROVIDED "AS IS"',
    "IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE",
]

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


def _move(src: str, dst: str) -> None:
    """原子替换文件，跨磁盘时自动回退到 shutil.move（copy+delete）。

    os.replace 在同盘是原子改名，但 Windows 上跨盘会抛 WinError 17
    （「系统无法将文件移到不同的磁盘驱动器」）。$TEMP 与项目目录不在同一盘时
    （如本机 $TEMP 被指向 C 盘、项目在 D 盘），构建期临时 syso 落在 $TEMP 而
    目标 syso 在项目根，直接 os.replace 必然失败。这里对跨盘失败做一次降级。
    """
    try:
        os.replace(src, dst)
    except OSError:
        # 跨盘：move 会退化为 copy2 + unlink，语义等价（目标已存在则覆盖）
        shutil.move(src, dst)


def build_exe(version: str) -> bool:
    """构建发布版 exe（带版本号 + 发布时间注入 + windowsgui + 去符号 + 去路径）。

    -trimpath 是**分发必需**：不加它，每个源文件都会以 `D:/Projects/.../xxx.go`
    的形式进二进制，等于把开发机的目录结构、用户名一起发出去（panic 栈、
    runtime.Caller、DWARF 全是它）。加了之后文件路径变成 `workerrest/xxx.go`。
    注意它与 -X main.buildTime=<当前时间> 天然互斥于「逐字节可复现」——
    剥离路径是为了不泄漏，不是为了 bit-for-bit 复现。

    构建前用**本次 tag 版本**在临时目录重生成一份 syso 并临时替换仓库根的
    rsrc_windows_amd64.syso，go build 链接完再恢复。这是为了消除「打了新 tag →
    仓库里 syso 版本号还是旧的 → 必须先 commit 一份新 syso → tag 又不在 HEAD 上」的
    死锁：VERSIONINFO 版本号必须跟 tag 走，但 syso 又随源码入库（commit 会顶掉 tag），
    所以只能在构建这瞬间用显式版本号现算一份，不动仓库、不留脏文件。
    """
    exe = EXE_SRC
    # 发布时间用 RFC3339 且无空格（ISO 格式），避免破坏单条 ldflags 字符串；
    # 前端「版本 | 发布时间」行在时间为空时回退显示「本地构建」。
    build_time = datetime.now().astimezone().isoformat()
    ldflags = f'-X main.appVersion={version} -X main.buildTime={build_time} -H windowsgui -s -w'

    syso = os.path.join(MAIN_DIR, "rsrc_windows_amd64.syso")
    gen = os.path.join(HERE, "gen_windows_res.py")
    backup = syso + ".buildbak"
    tmp_syso = None
    try:
        # 用本次 tag 版本生成正确版本的 syso（临时），并临时替换仓库根的，供 go build 链接
        import tempfile
        fd, tmp_syso = tempfile.mkstemp(prefix="wr-syso-", suffix=".syso")
        os.close(fd)
        r = subprocess.run(
            [sys.executable, gen, "--version", version, "--out", tmp_syso],
            cwd=MAIN_DIR, capture_output=True, text=True, encoding="utf-8", errors="replace",
        )
        if r.returncode != 0:
            out = ((r.stdout or "") + (r.stderr or "")).strip()
            print(f"[make-portable] FAIL: 生成 syso 失败\n{out}")
            return False
        print("[make-portable] " + ((r.stdout or "").strip().splitlines()[-1] if r.stdout else ""))
        if os.path.isfile(syso):
            _move(syso, backup)
        _move(tmp_syso, syso)
        tmp_syso = None

        print(f"[make-portable] go build -trimpath -> {exe} (version={version})")
        r = subprocess.run(
            ["go", "build", "-trimpath", "-o", exe, "-ldflags", ldflags, "."],
            cwd=MAIN_DIR,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
        )
        if r.returncode != 0:
            out = r.stdout.decode("utf-8", "replace")
            print(f"[make-portable] FAIL: go build 失败\n{out}")
            return False
        return os.path.exists(exe)
    finally:
        # 无论成败都恢复仓库根 syso，绝不留脏文件或半替换状态
        if os.path.isfile(backup):
            _move(backup, syso)
        if tmp_syso and os.path.isfile(tmp_syso):
            os.remove(tmp_syso)


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
    # 开源鸣谢清单与项目许可证不拷：两者都内嵌在 exe 里（notices.go / license.go），
    # 关于页有两个入口可读、可另存为。散放副本只会变成首装那天的过期快照。
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
    # 首跑直接在预置文件上继续写（补种提醒内容条目 / 指定应用 / 提示音后回写）。
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
    # 许可文本两份都**不该**在包里：鸣谢清单与 MIT 正文都内嵌进 exe（notices.go /
    # license.go），关于页各有入口可读可导出。目录里再出现同名文件，就是一份从首装那天
    # 起不再更新的快照 —— 用户分不清哪份是当前版本，比没有更糟。
    stray_license = [n for n in names
                     if n in (f"{top}/{NOTICES_NAME}", f"{top}/{LICENSE_NAME}")]
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
        and n_sounds == 4 and has_cfg and has_cleaner
        and no_data and not stray_license and not escaped
    )
    print(
        f"[make-portable] zip 校验: entries={len(names)} exe={has_exe} supervisors={has_roles}"
        f" role_meta={has_role_meta} img={n_roles} sounds={n_sounds} cfg={has_cfg}"
        f" cleaner={has_cleaner} no_data={no_data} stray_license={len(stray_license)}"
        f" escaped={len(escaped)} crc={test is None}"
    )
    if stray_license:
        print(f"[make-portable] FAIL: 包里不该有许可文本（已内嵌进 exe）: {stray_license}")
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
    if not os.path.isdir(sup):  # 仓库布局：随包资源收在 assets/ 下
        sup = os.path.join(root, "assets", "supervisors")
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
# 的数据源）与便携包里的 config/settings.json（新装首跑读它，由本脚本从 internal/settings 复制）。
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
    """exe 内嵌的出厂默认必须与 internal/settings/default_settings.json 语义一致，返回空串表示通过。"""
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

    源码布局：main 包在 cmd/workerrest/（含其 embed 资源），纯逻辑在 internal/。
    两处都要扫，go.mod/go.sum 留在模块根。
    """
    files = []

    def add_go(dirpath: str) -> None:
        for name in sorted(os.listdir(dirpath)):
            if name.endswith(".go") and not name.endswith("_test.go"):
                files.append(os.path.join(dirpath, name))

    def add_embeds(dirpath: str) -> None:
        for name in sorted(os.listdir(dirpath)):
            if not name.endswith(".go"):
                continue
            p = os.path.join(dirpath, name)
            try:
                with open(p, encoding="utf-8", errors="replace") as f:
                    src = f.read()
            except OSError:
                continue
            for m in _EMBED_DIRECTIVE.finditer(src):
                for raw in m.group(1).split():
                    pat = raw[4:] if raw.startswith("all:") else raw
                    # embed 路径相对「声明它的包目录」解析
                    for hit in glob.glob(os.path.join(dirpath, pat), recursive=True):
                        if os.path.isdir(hit):
                            for d2, _dirs, names in os.walk(hit):
                                files.extend(os.path.join(d2, n) for n in names)
                        else:
                            files.append(hit)

    add_go(MAIN_DIR)
    add_embeds(MAIN_DIR)
    for sub in sorted(os.listdir(os.path.join(ROOT, "internal"))):
        d = os.path.join(ROOT, "internal", sub)
        if os.path.isdir(d):
            add_go(d)
            add_embeds(d)
    for name in ("go.mod", "go.sum"):
        p = os.path.join(ROOT, name)
        if os.path.isfile(p):
            files.append(p)
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


def check_winres(exe: str, version: str) -> str:
    """exe 必须带图标 + VERSIONINFO + MANIFEST 三类 Windows 资源，返回空串表示通过。

    审计 域2-4 逮到过「exe 既无 VERSIONINFO 也无应用清单」：.syso 只嵌了图标
    （早期 rsrc 方案），资源管理器属性页看不到版本号，也没有 longPathAware /
    supportedOS 清单。这里让 verify_windows_res.py 解析 PE 的 .rsrc 节断言三类资源
    齐全，把「发出去的 exe 缺版本信息」挡在打包之前。

    版本号一致性：exe 的 VERSIONINFO 必须等于**本次 tag 版本**（build_exe 已用该版本
    显式生成 syso，这里用 --expect-version 复核 exe 里确实编进了对的版本，防「生成的
    syso 没用上 / 版本传错」这类静默漂移）。不再拿「仓库 syso 的版本号 == git 最新 tag」
    来卡 —— 那正是「打 tag 后 syso 必须 commit 一份新版本」死锁的来源：版本号只在
    build_exe 这一瞬间用显式值现算，仓库里的 syso 保持上一个 tag 的版本即可，不必
    为它多提交一次。

    图标/manifest 内容一致性：仍跑 gen_windows_res.py --check，但只核对图标与清单的
    内容没漂移（版本号不参与比对）—— 图标/清单变了却忘了重新生成，还是要挡。
    """
    script = os.path.join(HERE, "verify_windows_res.py")
    if not os.path.isfile(script):
        return f"找不到 {script}"
    r = subprocess.run([sys.executable, script, exe, "--expect-version", version],
                       cwd=ROOT, capture_output=True, text=True,
                       encoding="utf-8", errors="replace")
    out = ((r.stdout or "") + (r.stderr or "")).strip()
    if r.returncode != 0:
        return out or "exe 的 Windows 资源（图标/VERSIONINFO/MANIFEST）校验失败"
    print("[make-portable] " + (out or "Windows 资源（图标/VERSIONINFO/MANIFEST）ok"))

    # 图标/清单内容没漂移（版本号不比对，见函数 docstring）
    gen = os.path.join(HERE, "gen_windows_res.py")
    if not os.path.isfile(gen):
        return f"找不到 {gen}"
    r2 = subprocess.run([sys.executable, gen, "--check"],
                        cwd=ROOT, capture_output=True, text=True,
                        encoding="utf-8", errors="replace")
    out2 = ((r2.stdout or "") + (r2.stderr or "")).strip()
    if r2.returncode != 0:
        return out2 or "syso 的图标/清单内容与当前源码不一致"
    print("[make-portable] " + (out2 or "syso 图标/清单与源码一致"))
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


def check_toolchain() -> str:
    """发布工具链必须与 go.mod 声明的 `toolchain` 指令一致，返回空串表示通过。

    go.mod 的 toolchain 指令是「发布用哪个 Go 构建」的唯一真源（有了它，任何机器上
    默认 GOTOOLCHAIN=auto 都会切到同一版本）。这里在构建前显式校验「实际运行的 go
    == 声明的版本」：一旦有人用 GOTOOLCHAIN=local 或别的 go 入口悄悄换了工具链，
    编进 exe 的运行时版本就和清单里声明的不一致 —— 声明与实物不符，比漏声明更糟。

    这条与 check_notices 里 gen_notices.py 的同名校验互补：那边管「清单里写的版本
    对不对」，这边管「构建 exe 的工具链对不对」，两头都钉住才算钉死。
    """
    go_mod = os.path.join(ROOT, "go.mod")
    try:
        with open(go_mod, encoding="utf-8") as f:
            text = f.read()
    except OSError as e:
        return f"读不到 go.mod：{e}"
    m = re.search(r"^toolchain\s+(go[\d.]+)\s*$", text, re.M)
    if not m:
        # 没声明 toolchain 指令：退回 go 指令版本，但这是「未固定」状态，也应提醒
        m = re.search(r"^go\s+([\d.]+)\s*$", text, re.M)
        if not m:
            return "go.mod 里既没有 toolchain 指令也没有 go 指令"
        return ""  # 无 toolchain 指令：不强制，交给 gen_notices 的兜底
    declared = m.group(1)

    actual = subprocess.run(["go", "env", "GOVERSION"], cwd=ROOT,
                            capture_output=True, text=True,
                            encoding="utf-8", errors="replace").stdout.strip()
    if actual != declared:
        return (f"工具链版本漂移：go.mod 声明 {declared}，但实际运行的 go 是 {actual}。"
                f"请用声明的工具链构建（默认 GOTOOLCHAIN=auto 会自动切换），"
                f"或更新 go.mod 的 toolchain 指令。")
    return ""


def autostart_facts() -> dict:
    """从 internal/autostart 的 Windows 实现解析值名与两处键路径（唯一真源在 Go 源码里）。

    清脚本里硬编码了一份，打包时逐字比对。不比对的话，改了值名脚本还在删旧名字 ——
    这种失败对用户是静默的：屏幕上说「已删除」，开机照样启动，而且无从察觉。

    自启动逻辑已下沉到 internal/autostart（值名 runValueName 是 const；runKeyPath /
    approvedKeyPath 是 var 块里的变量，单测会替换成临时键）。Windows 实现里才有真值。
    """
    path = os.path.join(ROOT, "internal", "autostart", "autostart_windows.go")
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


def check_license() -> str:
    """项目许可证：必须是 MIT 正文、带版权行，且许可全文完整（返回空串表示通过）。

    只查「文件在不在」不够 —— 一份被截断、或者只有一句「MIT」的 LICENSE 同样能让
    打包成功，而 MIT 的有效性依赖正文逐条保留（授权 + 声明保留义务 + 免责声明）。
    """
    if not os.path.isfile(LICENSE_SRC):
        return f"仓库根缺 {LICENSE_NAME}：项目许可的正文没了"
    text = open(LICENSE_SRC, encoding="utf-8", errors="replace").read()
    if not text.strip():
        return f"{LICENSE_NAME} 是空文件"
    # 折叠空白后再找条款：MIT 正文的断行位置各不相同（比如责任限制那句常在
    # AUTHORS 与 COPYRIGHT 之间换行），按行找会漏，按「连续空白当空格」找才稳。
    flat = " ".join(text.split())
    missing = [s for s in LICENSE_REQUIRED if s not in flat]
    if missing:
        return f"{LICENSE_NAME} 不像完整 MIT 正文，缺：{missing}"
    if "Copyright (c)" not in text:
        return f"{LICENSE_NAME} 缺版权行（Copyright (c) <年份> <权利人>）"
    # 内嵌源必须与仓库根逐字节相同：go:embed 不能引用 ../../LICENSE，cmd/workerrest/
    # 下那份副本才是真正编进 exe 的字节。副本漂移时，用户从关于页看到的是旧正文 ——
    # 界面上看不出来，测试与打包才发现，所以两边都钉（license_test.go 是另一道）。
    if not os.path.isfile(LICENSE_EMBED_SRC):
        return f"缺内嵌源 {os.path.relpath(LICENSE_EMBED_SRC, ROOT)}（go:embed 只能用同目录文件）"
    root_bytes = open(LICENSE_SRC, "rb").read()
    embed_bytes = open(LICENSE_EMBED_SRC, "rb").read()
    if root_bytes != embed_bytes:
        return (f"内嵌的 {LICENSE_NAME} 与仓库根不一致（{len(embed_bytes)} vs {len(root_bytes)} 字节）："
                f"改完许可证要同步副本 cp {LICENSE_NAME} "
                f"{os.path.relpath(LICENSE_EMBED_SRC, ROOT)}")
    print(f"[make-portable] {LICENSE_NAME} ok（MIT，{len(text)} 字节，内嵌副本一致）")
    return ""


def check_worktree_clean() -> str:
    """工作树必须与 tag 指向的那个提交完全一致，返回空串表示通过。

    版本号的语义是「这个 tag 里的代码」。工作树带着未提交改动时，`go build` 编进去的是
    磁盘上的东西，而 tag 指的是另一个快照 —— 包能打出来、测试也全绿，但「发布包 = 源码」
    这条审计链就断了：用户/审计方按 tag 检出对不上二进制的行为。

    这个坑很隐蔽：改动通常是「顺手没提交」的几个文件，打包时不会有任何异常提示。
    所以在这里硬停。真要在脏树上试打包（排查构建问题），显式加 --allow-dirty，别默认放行。

    只认 git status（含未跟踪文件）：.gitignore 里的 build/bin、release/ 等不算脏，
    所以打过一次包不会自己把自己拦住。
    """
    try:
        # core.quotepath=false：不这样，中文路径会变成 "\344\270\255..." 八进制转义，
        # 提示信息对人就没用了
        r = subprocess.run(["git", "-c", "core.quotepath=false", "status", "--porcelain"],
                           cwd=ROOT, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    except OSError as e:
        return f"跑不了 git（{e}）：无法确认工作树与 tag 一致"
    if r.returncode != 0:
        return "git status 失败：" + r.stdout.decode("utf-8", "replace").strip()
    out = r.stdout.decode("utf-8", "replace").strip()
    if not out:
        return ""
    lines = out.splitlines()
    shown = "\n".join("    " + ln for ln in lines[:12])
    more = "" if len(lines) <= 12 else f"\n    ……另有 {len(lines) - 12} 项"
    return ("工作树不干净：%d 项未提交改动。tag 指的是提交，工作树里的改动不在这个 tag 里，"
            "打出来的包会与 tag 对不上。\n    先 commit（或 stash）再打包；"
            "只是排查构建问题时才用 --allow-dirty：\n%s%s" % (len(lines), shown, more))


def main() -> int:
    ap = argparse.ArgumentParser(description="Go 版便携目录打包")
    ap.add_argument("--no-build", action="store_true", help="跳过 go build，直接用已有 exe")
    ap.add_argument("--out", default="", help="交付根目录（默认 release/，其下再建 WorkerRest-<tag>/）")
    ap.add_argument("--allow-dirty", action="store_true",
                    help="允许在未提交的工作树上打包（仅供排查构建问题，交付禁用）")
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

    # 0b. 工作树必须就是 tag 指向的那份代码（脏树打出来的包与 tag 对不上）
    if args.allow_dirty:
        print("[make-portable] 警告：--allow-dirty 已放行，产物与 tag 可能不一致（不可交付）")
    else:
        why = check_worktree_clean()
        if why:
            print("[make-portable] FAIL: " + why)
            return 1
        print("[make-portable] 工作树干净（== tag 指向的提交）")

    # 1. 构建 exe 之前先核对许可清单（它要被编进 exe，过期就必须在这里停）
    why = check_notices()
    if why:
        print("[make-portable] FAIL: " + why)
        return 1

    # 1a. 发布工具链必须与 go.mod 声明一致（否则编进 exe 的运行时版本与清单不符）
    why = check_toolchain()
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
    why = check_winres(EXE_SRC, version)
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

    # 1f. 项目许可证：正文要完整，且内嵌副本必须与仓库根逐字节相同（用户只看得到内嵌那份）
    why = check_license()
    if why:
        print("[make-portable] FAIL: " + why)
        return 1

    # 2. 组装（在系统临时目录做，避免 release/ 历史产物被锁）
    stage_parent = os.path.join(TEMP, "wr-portable-build")
    dest = os.path.join(stage_parent, PORTABLE_DIR_NAME)
    if not assemble(dest, version):
        print("[make-portable] FAIL: 组装便携目录失败")
        return 1

    # 3. 结构核验
    checks = [
        ("exe", os.path.join(dest, EXE_NAME)),
        ("督休专员目录", find_role_meta(dest, "眼镜主管")),
        ("预置 sounds/", os.path.join(dest, "sounds", "short_start.wav")),
        ("预置 config/settings.json", os.path.join(dest, "config", "settings.json")),
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
