#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""跨天探针：软件连续运行跨过零点时，统计报表能不能跟着翻页。

验的是三件在真实使用里最容易坏、平时又看不到的事（通宵挂着设置页才会暴露）：
  1. 「日」范围的右端要自己推到新的一天（默认范围必须现算，不能缓存在内存里）；
  2. 新一天的数据要出现在表格里（跨天那一刻页面要主动重画）；
  3. 用户手动选过的范围不能被跨天重置。

做法：起一个无头服务（配置与统计都隔离到临时目录），用 Playwright 打开真实页面，
把页面时钟伪造到「今天 23:59:40」，再推到「次日 00:00:40」，比对前后两次快照。
后端时间改不了也不影响 —— 范围、桶、重画都在前端。

前端改动后**必须先重建无头服务**（本脚本会自动构建）。
用法：python scripts/probe_crossday.py [--port 29755] [--keep]
"""

import argparse
import json
import os
import shutil
import socket
import subprocess
import sys
import time
import urllib.request
from datetime import date, timedelta

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(os.path.dirname(HERE))
EXE = os.path.join(ROOT, "build", "bin", "chibingren_server.exe")
PROBE = os.path.join(HERE, "crossday_probe.js")
TEMP = os.environ.get("TEMP", ROOT)
OUT = os.path.join(TEMP, "wr_crossday")
ISO = os.path.join(TEMP, "wr_crossday_iso")
REPORT = os.path.join(TEMP, "wr_crossday_report.txt")
LOG = os.path.join(TEMP, "wr_crossday_server.log")
REAL = os.path.join(ROOT, "config", "settings.json")

try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass

failures = []
checks = 0


def check(ok, label, detail=""):
    global checks
    checks += 1
    line = ("  ok   " if ok else "  FAIL ") + label + (("  | " + str(detail)) if detail else "")
    print(line)
    if not ok:
        failures.append(label)
    return ok


def free_port(preferred):
    for p in (preferred, 0):
        s = socket.socket()
        try:
            s.bind(("127.0.0.1", p))
            got = s.getsockname()[1]
            s.close()
            return got
        except OSError:
            s.close()
    return 0


def wait_up(base, proc, timeout=25):
    t0 = time.time()
    while time.time() - t0 < timeout:
        if proc.poll() is not None:
            return False
        try:
            with urllib.request.urlopen(base + "/", timeout=1) as r:
                if r.status == 200:
                    return True
        except Exception:
            time.sleep(0.3)
    return False


def seed_stats(path, d1, d2):
    """前 29 天各 2 小时；今天 1 小时；明天 5 小时（跨天后应排在最新一行，值一眼能认）。"""
    base = {"workMs": 7200000, "breakMs": 600000, "shortMs": 600000,
            "shortCount": 2, "longCount": 0, "customCount": 0, "customCounts": {},
            "postponeCount": 0, "skipCount": 0, "endEarlyCount": 0,
            "takeoutOrdered": 0, "takeoutSkipped": 0, "takeoutPostponed": 0,
            "maxStreakMs": 1800000, "workSegments": [1800000]}
    days = {}
    for i in range(29, 0, -1):
        days[(date.fromisoformat(d1) - timedelta(days=i)).isoformat()] = dict(base)
    days[d1] = dict(base, workMs=3600000)
    days[d2] = dict(base, workMs=18000000)
    with open(path, "w", encoding="utf-8") as f:
        json.dump(days, f, ensure_ascii=False, indent=2)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--port", type=int, default=29755)
    ap.add_argument("--keep", action="store_true")
    args = ap.parse_args()

    node_cmd = shutil.which("node")
    if not node_cmd:
        print("找不到 node（本探针用 Playwright 驱动系统 Edge）")
        return 2
    if not os.path.isdir(os.path.join(ROOT, "node_modules", "playwright")):
        print("找不到 playwright（先在项目根 npm install）")
        return 2

    print("构建无头服务…")
    b = subprocess.run(
        ["go", "build", "-tags", "server", "-o", os.path.join("build", "bin", "chibingren_server.exe"),
         "-ldflags", "-s -w", "."],
        cwd=ROOT, capture_output=True, text=True, encoding="utf-8", errors="replace",
    )
    if b.returncode != 0:
        print("构建失败:\n" + (b.stderr or b.stdout))
        return 2

    d1 = date.today().isoformat()
    d2 = (date.today() + timedelta(days=1)).isoformat()

    # 配置与统计双双隔离：WR_SETTINGS_PATH 决定配置，WR_STATS_PATH 钉死统计落点，
    # 真实 config/ 一个字节都不会被动。
    shutil.rmtree(ISO, ignore_errors=True)
    os.makedirs(ISO, exist_ok=True)
    os.makedirs(OUT, exist_ok=True)
    cfg_path = os.path.join(ISO, "settings.json")
    if os.path.exists(REAL):
        shutil.copyfile(REAL, cfg_path)
        cfg = json.load(open(cfg_path, encoding="utf-8"))
        cfg.setdefault("short", {}).update(
            {"enabled": True, "mode": "countdown", "intervalMs": 600000, "notifyBeforeMs": 0})
        cfg["long"] = dict(cfg.get("long") or {}, enabled=False)
        cfg["takeout"] = dict(cfg.get("takeout") or {}, enabled=False)
        with open(cfg_path, "w", encoding="utf-8") as f:
            json.dump(cfg, f, ensure_ascii=False, indent=2)
    stats_path = os.path.join(ISO, "stats.json")
    seed_stats(stats_path, d1, d2)

    port = free_port(args.port)
    base = "http://127.0.0.1:%d" % port
    print("跨天探针：服务 %s port=%d（隔离配置 %s）" % (EXE, port, ISO))

    env = dict(os.environ)
    env["WAILS_SERVER_PORT"] = str(port)
    env["WAILS_SERVER_HOST"] = "127.0.0.1"
    env["WR_SETTINGS_PATH"] = cfg_path
    env["WR_STATS_PATH"] = stats_path
    env.pop("ELECTRON_RUN_AS_NODE", None)
    logf = open(LOG, "w", encoding="utf-8")
    srv = subprocess.Popen([EXE], cwd=ROOT, env=env, stdout=logf, stderr=subprocess.STDOUT)

    try:
        if not check(wait_up(base, srv), "无头服务就绪", "rc=%s" % srv.poll()):
            print(open(LOG, encoding="utf-8", errors="replace").read()[-2000:])
            return 1

        penv = dict(os.environ)
        penv.update({"WR_BASE": base, "WR_OUT": OUT, "WR_DAY1": d1, "WR_DAY2": d2})
        r = subprocess.run([node_cmd, PROBE], cwd=ROOT, env=penv, capture_output=True,
                           text=True, timeout=180, encoding="utf-8", errors="replace")
        if not check(r.returncode == 0 and (r.stdout or "").strip().startswith("{"),
                     "页面探针正常结束", "rc=%s %s" % (r.returncode, (r.stderr or "")[-300:].replace("\n", " "))):
            print("--- stdout ---\n" + (r.stdout or "")[-1500:])
            return 1
        m = json.loads(r.stdout)
    finally:
        srv.terminate()
        try:
            srv.wait(5)
        except Exception:
            srv.kill()
        logf.close()

    b, a = m["before"], m["after"]
    mb, ma = m["manualBefore"], m["manualAfter"]
    md1, md2 = d1[5:], d2[5:]

    check(m.get("rendered") is True, "统计页渲染完成（表格有数据行）")
    check(str(b.get("label") or "").endswith(md1), "跨天前：「日」范围右端 = 今天", b.get("label"))
    check(str(a.get("label") or "").endswith(md2), "跨天后：范围标签自动推到次日", a.get("label"))
    check(str(b.get("newest") or "").startswith(d1), "跨天前：表格最新一行 = 今天", b.get("newest"))
    check(str(a.get("newest") or "").startswith(d2), "跨天后：表格最新一行 = 次日（新一天的数据出现了）", a.get("newest"))
    check((a.get("rowCount") or 0) == 32, "跨天后表格仍是 30 个日桶 + 表头 + 合计", a.get("rowCount"))
    check(mb.get("label") == "01-05 ~ 01-12", "手动范围：按用户设定的范围", mb.get("label"))
    check(ma.get("label") == "01-05 ~ 01-12", "手动范围：跨天后不被重置成默认范围", ma.get("label"))
    check(not m.get("errs"), "页面无未捕获 JS 异常", (m.get("errs") or [])[:2])

    with open(REPORT, "w", encoding="utf-8") as f:
        f.write("跨天探针：%d 项，失败 %d 项\n" % (checks, len(failures)))
        f.write(json.dumps(m, ensure_ascii=False, indent=2))
    print("\n结果：%d 项，失败 %d 项（报告 %s）" % (checks, len(failures), REPORT))
    if failures:
        print("失败：", failures)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
