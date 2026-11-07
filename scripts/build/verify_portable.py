#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""便携打包产物验证：解包目录在干净环境下首次运行，数据必须落在 exe 同目录。

验证的核心语义（README“数据与配置落点”里 Go 版的约定）：
  数据与程序同目录 —— 配置/统计/音频都在 exe 旁的 config/ 与 data/，绝不碰 %APPDATA%。
  打包交付的便携目录自带预置两件套（sounds/ + config/settings.json），首跑直接在预置
  文件上继续写（补种提醒内容条目 / 指定应用 / 提示音，并把结果回写）。**data/ 与 stats.json 不进包**（统计是用户
  数据，更新时不能被安装包覆盖）：首跑由 stats.go 的 Load 自己建出 data/stats.json。

做法：
  1. 把 make_portable.py 产出的便携目录**整体复制**到 %TEMP% 隔离副本（不污染 release/）；
  2. 用一个空的隔离 APPDATA 启动副本里的 exe（不带 WR_SETTINGS_PATH —— 那会跳过
     自然落点推导，直接独占；这里要验的是**自然落点**）；
  3. 观察几秒，断言：
     - 进程活着、日志无 panic；
     - 预置两件套随包完整（sounds/ 四个 wav、config/settings.json）；
     - 发布包里**没有** data/ 与 stats.json（统计文件由首跑自行创建）；
     - 首跑把补种的预置条目写进了 exe 旁的 config/settings.json（在预置文件上继续写）；
     - 首跑自动创建了 exe 旁的 data/stats.json，且内容是合法空表；
     - 隔离 APPDATA 里没有落下任何 WorkerRest 相关文件（没往 APPDATA 写）。

用法：python scripts/verify_portable.py [--dir <便携目录>] [--seconds 8]
  --dir 省略时自动取 release/WorkerRest-<tag>/WorkerRest-portable/（先跑 make_portable.py）
退出码 0 = 全部通过。
"""

import argparse
import hashlib
import json
import os
import re
import shutil
import subprocess
import sys
import time

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(os.path.dirname(HERE))  # 项目根（原 rewrite-go，现已提升为顶层）
MAIN_DIR = os.path.join(ROOT, "cmd", "workerrest")  # main 包所在（内嵌资源与 exe 同目录）

# 同目录的 version.py（release 子目录名里带 tag，按 tag 拼路径最准）
if HERE not in sys.path:
    sys.path.insert(0, HERE)
import version as gitver  # noqa: E402
from make_portable import dump_embedded_defaults  # noqa: E402  （打包侧同一套导出逻辑）
from make_portable import CLEANER_NAME, autostart_facts  # noqa: E402  （清启动项脚本同源核对）
TEMP = os.environ.get("TEMP", ROOT)
LOG = os.path.join(TEMP, "wr_portable_verify.log")
REPORT = os.path.join(TEMP, "wr_portable_verify_report.txt")


def default_dir():
    """release/WorkerRest-<tag>/WorkerRest-portable/（tag 取最近一次，目录名会变）。

    先按 git tag 拼路径；拼出来的不存在时，扫 release/WorkerRest-*/WorkerRest-portable/
    取版本号最大的那个兜底。"""
    rel = os.path.join(ROOT, "release")
    try:
        tag = gitver.latest_tag() or ""
    except Exception:  # noqa: BLE001
        tag = ""
    if tag:
        p = os.path.join(rel, "WorkerRest-" + tag, "WorkerRest-portable")
        if os.path.isdir(p):
            return p
    best = None
    if os.path.isdir(rel):
        for name in os.listdir(rel):
            m = re.match(r"^WorkerRest-(v?\d+\.\d+\.\d+)$", name)
            if not m:
                continue
            p = os.path.join(rel, name, "WorkerRest-portable")
            if not os.path.isdir(p):
                continue
            key = tuple(int(x) for x in m.group(1).lstrip("v").split("."))
            if best is None or key > best[0]:
                best = (key, p)
    if best:
        return best[1]
    return os.path.join(rel, "WorkerRest-v0.9.0", "WorkerRest-portable")

try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
    sys.stderr.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass

failures = []
checks = 0


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


UUID_RE = re.compile(r"^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$")


def find_uuid_like(node, out=None):
    """递归找出配置里任何 UUID 形态的字符串。

    机器标识机制已整体删除，配置文件里不该再出现这种形态的值；这里用形态匹配而不是
    只查某个键名，防止以后又冒出一个存储位置（键名换了但形态没换）。
    """
    if out is None:
        out = []
    if isinstance(node, dict):
        for k, v in node.items():
            if isinstance(k, str) and UUID_RE.match(k):
                out.append(k)
            find_uuid_like(v, out)
    elif isinstance(node, list):
        for v in node:
            find_uuid_like(v, out)
    elif isinstance(node, str) and UUID_RE.match(node.strip()):
        out.append(node)
    return out


def check(ok, label, detail=""):
    global checks
    checks += 1
    print(("  ok   " if ok else "  FAIL ") + label + (("  | " + str(detail)) if detail else ""))
    if not ok:
        failures.append(label + (("  | " + str(detail)) if detail else ""))
    return ok


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--dir", default=default_dir(), help="便携目录路径")
    ap.add_argument("--seconds", type=int, default=8, help="观察多久（默认 8 秒）")
    args = ap.parse_args()

    src = os.path.abspath(args.dir)
    exe_src = os.path.join(src, "rest.exe")
    if not os.path.isfile(exe_src):
        print("找不到 %s —— 先跑 make_portable.py" % exe_src)
        return 2

    # 隔离副本：复制整个便携目录（含 supervisors/），在临时位置跑，不污染 release/
    iso = os.path.join(TEMP, "wr_portable_verify_iso")
    shutil.rmtree(iso, ignore_errors=True)
    shutil.copytree(src, iso)
    exe = os.path.join(iso, "rest.exe")
    # 隔离 APPDATA：干净、空，确保“便携优先”不是靠 APPDATA 里恰好有配置才成立
    iso_appdata = os.path.join(TEMP, "wr_portable_verify_appdata")
    shutil.rmtree(iso_appdata, ignore_errors=True)
    os.makedirs(iso_appdata, exist_ok=True)

    # 发布包里不许带统计文件：它是用户数据，首跑由程序自建（改动前的老包会带 data/）
    shipped_data = os.path.join(src, "data")
    shipped_stats = os.path.join(src, "stats.json")
    check(not os.path.exists(shipped_data) and not os.path.exists(shipped_stats),
          "发布包里没有 data/ 与 stats.json（统计文件不随包分发）",
          shipped_data if os.path.exists(shipped_data) else shipped_stats)

    print("便携目录验证：%s（观察 %d 秒，隔离 APPDATA=%s）" % (iso, args.seconds, iso_appdata))

    env = dict(os.environ)
    env.pop("ELECTRON_RUN_AS_NODE", None)
    env.pop("WR_SETTINGS_PATH", None)  # 不设：要验自然落点，不是独占隔离
    env["APPDATA"] = iso_appdata

    alive = False
    alive_rc = None  # 观察期结束那一刻的退出码（None = 还活着）；必须在 terminate 之前取
    with open(LOG, "w", encoding="utf-8", errors="replace") as logf:
        proc = subprocess.Popen([exe], cwd=iso, env=env, stdout=logf, stderr=subprocess.STDOUT)
        try:
            time.sleep(max(3, args.seconds))
            alive_rc = proc.poll()
            alive = alive_rc is None
        finally:
            if proc.poll() is None:
                proc.terminate()
                try:
                    proc.wait(timeout=8)
                except subprocess.TimeoutExpired:
                    proc.kill()
    text = open(LOG, encoding="utf-8", errors="replace").read()

    try:
        body = "\n--- 便携版日志尾部 ---\n" + text[-2000:]
    except Exception:
        body = ""
    print(body)

    # 1. 进程活着、无 panic
    # 附注用 alive_rc 而不是 proc.poll()：后者在 terminate 之后取，永远是非 0，
    # 读报告的人会以为“进程明明退出了却判活着”。
    check(alive, "便携版进程观察期内一直活着", "rc=%s" % alive_rc)
    check("panic" not in text.lower(), "日志里没有 panic", "")

    # 2. 预置三件套随包完整
    n_wav = len([n for n in os.listdir(os.path.join(iso, "sounds"))
                 if n.endswith(".wav")]) if os.path.isdir(os.path.join(iso, "sounds")) else 0
    check(n_wav == 4, "预置 sounds/ 有四个内置提示音", "wav=%d" % n_wav)
    preset_cfg = os.path.join(iso, "config", "settings.json")
    check(os.path.isfile(preset_cfg), "预置 config/settings.json 随包存在", preset_cfg)
    # 许可文本：两份都内嵌进 exe（notices.go / license.go），便携目录里**不该**有同名文件。
    # 目录里那份从首装那天起就不再变（更新包 rest.zip 只换 exe），留着只会让人读到过期快照。
    # 反向断言比“存在”更值钱：以后谁把拷贝逻辑加回来，这条会立刻红。
    stray = [n for n in ("THIRD-PARTY-NOTICES.txt", "LICENSE")
             if os.path.exists(os.path.join(iso, n))]
    check(not stray, "许可文本不散放在目录里（已内嵌进 exe）", "stray=%s" % stray)
    # 首跑自建的空表（不再是“预置文件”）：Load 见文件缺失就建目录 + 写空表
    runtime_stats = os.path.join(iso, "data", "stats.json")
    check(os.path.isfile(runtime_stats), "首跑自动创建 data/stats.json", runtime_stats)
    if os.path.isfile(runtime_stats):
        try:
            sdata = json.load(open(runtime_stats, encoding="utf-8"))
            check(isinstance(sdata, dict) and "_customNames" in sdata,
                  "自建的统计表是合法空表结构", "")
        except Exception as e:  # noqa: BLE001
            check(False, "自建统计表可解析", repr(e))

    # 3. 首跑在预置配置上继续写：出厂那份没有 seeded 标记也没有条目，首跑补种后必须落盘
    cfg_side = os.path.join(iso, "config", "settings.json")
    check(os.path.isfile(cfg_side), "settings.json 落在 exe 旁（数据与程序同目录）", cfg_side)
    if os.path.isfile(cfg_side):
        try:
            data = json.load(open(cfg_side, encoding="utf-8"))
            rm = data.get("reminderMessages") or {}
            kinds = ("short", "long", "takeout")
            missing = [k for k in kinds
                       if not (rm.get(k) or {}).get("seeded")
                       or not (rm.get(k) or {}).get("items")]
            check(not missing, "首跑把补种的提醒内容条目回写进了落点配置",
                  "缺 %s" % ",".join(missing) if missing else "")
            # 机器标识机制已删除：落点配置里不许出现该键（手改或老配置也会在加载时被摘掉）
            check("installId" not in data, "落点配置不含任何机器标识键", "")
            ids = find_uuid_like(data)
            check(not ids, "配置里没有任何 UUID 形态的串（软件不再生成设备标识）",
                  "命中 %s" % ids[:2])
        except Exception as e:  # noqa: BLE001
            check(False, "落点配置可解析", repr(e))

    # 3b. 出厂默认更新源：预置配置的 sites 必须与同版本 version.json 的 sites 一致。
    #     version.json 下发的 sites 会整体覆盖客户端本地 sites，两边不一致就会出现
    #     “装完查 A、检查一次更新后又跳回 B”。
    vj = os.path.join(os.path.dirname(src), "version.json")  # release/WorkerRest-<tag>/
    cfg_sites = []
    try:
        with open(cfg_side, encoding="utf-8") as f:
            cfg_sites = [str(x).strip() for x in (json.load(f).get("sites") or [])]
    except Exception as e:  # noqa: BLE001
        check(False, "预置配置能读出 sites", repr(e))
    if os.path.isfile(vj):
        try:
            with open(vj, encoding="utf-8") as f:
                vj_doc = json.load(f)
            vj_sites = [str(x).strip() for x in (vj_doc.get("sites") or [])]
            check(cfg_sites == vj_sites,
                  "预置配置的 sites == version.json 的 sites（发布更新源不漂移）",
                  "%s vs %s" % (cfg_sites, vj_sites))
            # 3b-2. 清单里的 sha256 必须真的是同目录 rest.zip 的哈希：客户端只认它，
            #       写错（或改了包没重算）会让所有用户卡在“校验失败，已拒绝更新”。
            pkg = os.path.join(os.path.dirname(vj), "rest.zip")
            want = str(vj_doc.get("sha256") or "").strip().lower()
            if os.path.isfile(pkg):
                h = hashlib.sha256()
                with open(pkg, "rb") as pf:
                    for chunk in iter(lambda: pf.read(65536), b""):
                        h.update(chunk)
                got = h.hexdigest()
                check(want == got, "version.json 的 sha256 == rest.zip 实际哈希", "%s vs %s" % (want, got))
                check(int(vj_doc.get("size") or 0) == os.path.getsize(pkg),
                      "version.json 的 size == rest.zip 体积",
                      "%s vs %s" % (vj_doc.get("size"), os.path.getsize(pkg)))
            else:
                check(False, "同版本 rest.zip 在 version.json 旁边", pkg)
        except Exception as e:  # noqa: BLE001
            check(False, "version.json 可解析", repr(e))
    else:
        check(False, "同版本 version.json 在便携目录的上级目录里", vj)

    # 3c. “恢复默认设置”用的 exe 内嵌默认，必须与随包的预置配置一致。
    #     不一致时，新装看到的是预置文件的值、点“恢复默认”跳回 exe 里的旧值，
    #     界面上完全看不出来（--no-build 用上一轮 exe 时就会这样）。
    #     比的是**原始**预置文件（src/ 下那份），不是 iso 里已被首跑改写的副本。
    try:
        embedded = json.loads(dump_embedded_defaults(exe).decode("utf-8"))
        with open(os.path.join(src, "config", "settings.json"), encoding="utf-8") as f:
            shipped = json.load(f)
        same = embedded == shipped
        diff = ""
        if not same:
            keys = sorted(set(embedded) | set(shipped))
            diff = [k for k in keys if embedded.get(k) != shipped.get(k)][:6]
        check(same, "exe 内嵌出厂默认 == 随包预置 config/settings.json",
              "（一致）" if same else "不一致的顶层键：%s" % diff)
    except Exception as e:  # noqa: BLE001
        check(False, "能取出 exe 内嵌出厂默认并与预置配置比对", repr(e))

    # 3d. 构建路径卫生：exe 里不能带构建机的绝对源码路径（`-trimpath` 的意义）。
    #     判据不写死本机路径，而用“盘符绝对路径 + .go 结尾”这个特征：
    #     未加 -trimpath 时每个源文件都会以 D:/.../xxx.go 的形式进二进制（panic 栈、
    #     运行时符号），加了之后统一变成 workerrest/xxx.go，不可能再出现盘符。
    try:
        with open(exe, "rb") as ef:
            blob = ef.read()
        leaks = []
        for sep in ("\\", "/"):
            needle = ROOT.replace("\\", sep).encode("utf-8")
            if needle in blob:
                leaks.append(needle.decode("utf-8", "replace"))
        if ROOT.encode("utf-16-le") in blob:
            leaks.append(ROOT + "（UTF-16）")
        drive_go = re.findall(rb"[A-Za-z]:[\\/][^\x00\r\n]{0,120}?\.go\b", blob)
        check(not leaks and not drive_go,
              "exe 不含构建机绝对路径（-trimpath 生效）",
              "泄漏 %d 处，样例：%s" % (len(leaks) + len(drive_go),
                                        (leaks + [x.decode("latin1") for x in drive_go[:3]])[:3])
              if (leaks or drive_go) else "（干净）")
    except Exception as e:  # noqa: BLE001
        check(False, "能扫描 exe 的构建路径泄漏", repr(e))

    # 4. 没往 APPDATA 写**配置**（隔离 APPDATA 里不该出现任何 settings.json）。
    #    注意：WebView2 会自己在 APPDATA 下建 <exe名>/EBWebView 浏览器缓存目录，
    #    那是浏览器引擎的固有行为（上一版同样会在 APPDATA 建缓存），
    #    与“配置数据落点”无关 —— 所以这里只查 settings.json，不查“目录为空”。
    wrote_cfg = False
    for root, _dirs, files in os.walk(iso_appdata):
        if "settings.json" in files:
            wrote_cfg = True
            break
    check(not wrote_cfg, "隔离 APPDATA 里没有写入 settings.json（配置没外溢到 APPDATA）",
          "发现 settings.json" if wrote_cfg else "（干净）")

    # 5. supervisors/ 随包完整（渲染层立绘要能读到）。名字即身份，按专员名反查目录。
    role_meta = find_role_meta(iso, "眼镜主管")
    check(bool(role_meta) and os.path.isfile(role_meta),
          "supervisors/ 随包完整（眼镜主管 在）", role_meta or "(未找到)")

    # 6. 清启动项脚本：便携版没有卸载器，用户“卸载”= 删文件夹，注册表里那一项就成死路径。
    #    脚本丢了等于只能自己手改注册表；值名不一致等于屏幕上说删掉了、开机照样启动。
    cleaner = os.path.join(src, CLEANER_NAME)
    if check(os.path.isfile(cleaner), "随包清除开机自启动项脚本在", cleaner):
        with open(cleaner, "rb") as f:
            ship_raw = f.read()
        with open(os.path.join(ROOT, "scripts", "build", CLEANER_NAME), "rb") as f:
            repo_raw = f.read()
        check(ship_raw == repo_raw, "随包清启动项脚本 == 仓库脚本（逐字节）", "")
        facts = autostart_facts()
        try:
            ship_text = ship_raw.decode("gbk")
        except UnicodeDecodeError as e:  # noqa: BLE001
            ship_text = ""
            check(False, "随包脚本是 GBK 编码（cmd 按 936 读取）", repr(e))
        if ship_text:
            check(bool(facts.get("value")) and facts["value"] in ship_text,
                  "脚本里的值名 == sys_autostart.go 的 runValueName",
                  facts.get("value") or "(源码解析失败)")

    with open(REPORT, "w", encoding="utf-8") as f:
        f.write("便携版验证：%d 项断言，失败 %d 项\n\n" % (checks, len(failures)))
        f.write("\n".join(failures) if failures else "（全部通过）")
        f.write("\n\n--- 日志尾部 ---\n" + text[-2500:])
    print("\n结果：%d 项断言，失败 %d 项" % (checks, len(failures)))
    print("报告：%s" % REPORT)

    # 清理隔离副本（保留 release/ 交付物）
    shutil.rmtree(iso, ignore_errors=True)
    shutil.rmtree(iso_appdata, ignore_errors=True)
    return 0 if not failures else 1


if __name__ == "__main__":
    sys.exit(main())
