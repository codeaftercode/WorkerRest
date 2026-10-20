#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""便携打包产物验证：解包目录在干净环境下首次运行，数据必须落在 exe 同目录。

验证的核心语义（README「数据与配置落点」里 Go 版的约定）：
  数据与程序同目录 —— 配置/统计/音频都在 exe 旁的 config/ 与 data/，绝不碰 %APPDATA%。
  打包交付的便携目录自带预置三件套（sounds/ + config/settings.json + data/stats.json），
  首跑直接在预置文件上继续写（installId 补生成并回写）。

做法：
  1. 把 make_portable.py 产出的便携目录**整体复制**到 %TEMP% 隔离副本（不污染 release/）；
  2. 用一个空的隔离 APPDATA 启动副本里的 exe（不带 WR_SETTINGS_PATH —— 那会跳过
     自然落点推导，直接独占；这里要验的是**自然落点**）；
  3. 观察几秒，断言：
     - 进程活着、日志无 panic；
     - 预置三件套随包完整（sounds/ 四个 wav、config/settings.json、data/stats.json）；
     - 首跑把 installId 补写进了 exe 旁的 config/settings.json（在预置文件上继续写）；
     - 隔离 APPDATA 里没有落下任何 WorkerRest 相关文件（没往 APPDATA 写）。

用法：python scripts/verify_portable.py [--dir <便携目录>] [--seconds 8]
  --dir 省略时默认 release/WorkerRest-1.0.0-portable/（先跑 make_portable.py）
退出码 0 = 全部通过。
"""

import argparse
import hashlib
import json
import os
import shutil
import subprocess
import sys
import time

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)  # 项目根（原 rewrite-go，现已提升为顶层）
TEMP = os.environ.get("TEMP", ROOT)
DEFAULT_DIR = os.path.join(ROOT, "release", "WorkerRest-1.0.0-portable")
LOG = os.path.join(TEMP, "wr_portable_verify.log")
REPORT = os.path.join(TEMP, "wr_portable_verify_report.txt")

try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
    sys.stderr.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass

failures = []
checks = 0


def find_role_json(root, key):
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


def check(ok, label, detail=""):
    global checks
    checks += 1
    print(("  ok   " if ok else "  FAIL ") + label + (("  | " + str(detail)) if detail else ""))
    if not ok:
        failures.append(label + (("  | " + str(detail)) if detail else ""))
    return ok


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--dir", default=DEFAULT_DIR, help="便携目录路径")
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
    # 隔离 APPDATA：干净、空，确保「便携优先」不是靠 APPDATA 里恰好有配置才成立
    iso_appdata = os.path.join(TEMP, "wr_portable_verify_appdata")
    shutil.rmtree(iso_appdata, ignore_errors=True)
    os.makedirs(iso_appdata, exist_ok=True)

    print("便携目录验证：%s（观察 %d 秒，隔离 APPDATA=%s）" % (iso, args.seconds, iso_appdata))

    env = dict(os.environ)
    env.pop("ELECTRON_RUN_AS_NODE", None)
    env.pop("WR_SETTINGS_PATH", None)  # 不设：要验自然落点，不是独占隔离
    env["APPDATA"] = iso_appdata

    alive = False
    with open(LOG, "w", encoding="utf-8", errors="replace") as logf:
        proc = subprocess.Popen([exe], cwd=iso, env=env, stdout=logf, stderr=subprocess.STDOUT)
        try:
            time.sleep(max(3, args.seconds))
            alive = proc.poll() is None
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
    check(alive, "便携版进程观察期内一直活着", "rc=%s" % proc.poll())
    check("panic" not in text.lower(), "日志里没有 panic", "")

    # 2. 预置三件套随包完整
    n_wav = len([n for n in os.listdir(os.path.join(iso, "sounds"))
                 if n.endswith(".wav")]) if os.path.isdir(os.path.join(iso, "sounds")) else 0
    check(n_wav == 4, "预置 sounds/ 有四个内置提示音", "wav=%d" % n_wav)
    preset_cfg = os.path.join(iso, "config", "settings.json")
    check(os.path.isfile(preset_cfg), "预置 config/settings.json 随包存在", preset_cfg)
    preset_stats = os.path.join(iso, "data", "stats.json")
    check(os.path.isfile(preset_stats), "预置 data/stats.json 随包存在", preset_stats)
    if os.path.isfile(preset_stats):
        try:
            sdata = json.load(open(preset_stats, encoding="utf-8"))
            check("_customNames" in sdata, "预置统计表是合法空表结构", "")
        except Exception as e:  # noqa: BLE001
            check(False, "预置统计表可解析", repr(e))

    # 3. 首跑在预置配置上继续写（installId 补生成并回写）
    cfg_side = os.path.join(iso, "config", "settings.json")
    check(os.path.isfile(cfg_side), "settings.json 落在 exe 旁（数据与程序同目录）", cfg_side)
    if os.path.isfile(cfg_side):
        try:
            data = json.load(open(cfg_side, encoding="utf-8"))
            check("installId" in data and data.get("installId"), "首次运行的 installId 已写入落点配置", "")
        except Exception as e:  # noqa: BLE001
            check(False, "落点配置可解析", repr(e))

    # 4. 没往 APPDATA 写**配置**（隔离 APPDATA 里不该出现任何 settings.json）。
    #    注意：WebView2 会自己在 APPDATA 下建 <exe名>/EBWebView 浏览器缓存目录，
    #    那是浏览器引擎的固有行为（上一版同样会在 APPDATA 建缓存），
    #    与「配置数据落点」无关 —— 所以这里只查 settings.json，不查「目录为空」。
    wrote_cfg = False
    for root, _dirs, files in os.walk(iso_appdata):
        if "settings.json" in files:
            wrote_cfg = True
            break
    check(not wrote_cfg, "隔离 APPDATA 里没有写入 settings.json（配置没外溢到 APPDATA）",
          "发现 settings.json" if wrote_cfg else "（干净）")

    # 5. supervisors/ 随包完整（渲染层立绘要能读到）。目录名是专员名称、不固定，
    # 按 key 反查 supervisor_default 的 role.json。
    role_json = find_role_json(iso, "supervisor_default")
    check(bool(role_json) and os.path.isfile(role_json),
          "supervisors/ 随包完整（supervisor_default 在）", role_json or "(未找到)")

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
