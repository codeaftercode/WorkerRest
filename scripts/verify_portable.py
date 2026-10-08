#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""便携打包产物验证：解包目录在干净环境下首次运行，数据必须落在 exe 同目录。

验证的核心语义（README「数据目录双形态」里 Go 版的约定）：
  「便携优先」——exe 旁的 config/settings.json 是第一候选，找不到才回退 %APPDATA%。
  所以一个干净的便携目录首次启动，settings.json 必须落在 exe 旁，且绝不碰 APPDATA。

做法：
  1. 把 make_portable.py 产出的便携目录**整体复制**到 %TEMP% 隔离副本（不污染 release/）；
  2. 用一个空的隔离 APPDATA 启动副本里的 exe（不带 WR_SETTINGS_PATH —— 那会跳过
     便携优先逻辑，直接独占；这里要验的是**自然落点**）；
  3. 观察几秒，断言：
     - 进程活着、日志无 panic；
     - config/settings.json 落在了 exe 旁（便携优先生效）；
     - 隔离 APPDATA 里没有落下任何 WorkerRest 相关目录（没往 APPDATA 写）。

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
    exe_src = os.path.join(src, "chibingren.exe")
    if not os.path.isfile(exe_src):
        print("找不到 %s —— 先跑 make_portable.py" % exe_src)
        return 2

    # 隔离副本：复制整个便携目录（含 roles/），在临时位置跑，不污染 release/
    iso = os.path.join(TEMP, "wr_portable_verify_iso")
    shutil.rmtree(iso, ignore_errors=True)
    shutil.copytree(src, iso)
    exe = os.path.join(iso, "chibingren.exe")
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

    # 2. 数据落在 exe 旁（便携优先）
    cfg_side = os.path.join(iso, "config", "settings.json")
    check(os.path.isfile(cfg_side), "settings.json 落在 exe 旁（便携优先生效）", cfg_side)
    if os.path.isfile(cfg_side):
        try:
            data = json.load(open(cfg_side, encoding="utf-8"))
            check("installId" in data, "首次运行的 installId 已写入落点配置", "")
        except Exception as e:  # noqa: BLE001
            check(False, "落点配置可解析", repr(e))

    # 3. 没往 APPDATA 写**配置**（隔离 APPDATA 里不该出现任何 settings.json）。
    #    注意：WebView2 会自己在 APPDATA 下建 <exe名>/EBWebView 浏览器缓存目录，
    #    那是浏览器引擎的固有行为（Electron 版同样会在 APPDATA 建缓存），
    #    与「配置数据落点」无关 —— 所以这里只查 settings.json，不查「目录为空」。
    wrote_cfg = False
    for root, _dirs, files in os.walk(iso_appdata):
        if "settings.json" in files:
            wrote_cfg = True
            break
    check(not wrote_cfg, "隔离 APPDATA 里没有写入 settings.json（配置没外溢到 APPDATA）",
          "发现 settings.json" if wrote_cfg else "（干净）")

    # 4. roles/ 随包完整（渲染层立绘要能读到）
    role_json = os.path.join(iso, "roles", "supervisor_default", "role.json")
    check(os.path.isfile(role_json), "roles/ 随包完整（supervisor_default 在）", role_json)

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
