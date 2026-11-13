#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""设置页侧栏 / 时间线 / 看板的组合态探针驱动：起无头服务 → 跑 ui_matrix_probe.js → 收断言。

与既有脚本的分工：
  · probe_pause_cycle.py   —— 暂停 / 恢复这一条主线（不冻钟、到点处置、恢复不补偿）；
  · probe_renderer.py      —— 页面版式与静态渲染（休息窗、外卖窗、督休专员、配置页控件）；
  · **本脚本** —— 几类提醒同时在场时侧栏三格写什么、时间线列几条、看板数字对不上对不上。

隔离口径与其余探针一致：配置用出厂默认的一份副本放临时目录，WR_SETTINGS_PATH 指过去，
跑完断言仓库里真实的 config/settings.json 一个字节没变。

用法：python scripts/probe/probe_ui_matrix.py
前置：go build -tags server -o build/bin/rest_server.exe ./cmd/workerrest
退出码 0 = 全部通过。
"""

import hashlib
import json
import os
import shutil
import socket
import subprocess
import sys
import tempfile
import time
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(os.path.dirname(HERE))
EXE = os.path.join(ROOT, "build", "bin", "rest_server.exe")
FACTORY = os.path.join(ROOT, "internal", "settings", "default_settings.json")
PROBE_JS = os.path.join(HERE, "ui_matrix_probe.js")

try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass


def free_port(preferred):
    with socket.socket() as s:
        try:
            s.bind(("127.0.0.1", preferred))
            return preferred
        except OSError:
            pass
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def http_get(url, timeout=2):
    try:
        with urllib.request.urlopen(url, timeout=timeout) as r:
            return r.status
    except Exception:
        return 0


def digest(path):
    if not os.path.exists(path):
        return ""
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(65536), b""):
            h.update(chunk)
    return h.hexdigest()


def main():
    if not os.path.exists(EXE):
        print("找不到 %s（先 go build -tags server -o build/bin/rest_server.exe ./cmd/workerrest）" % EXE)
        return 2
    if not os.path.exists(PROBE_JS):
        print("找不到 %s" % PROBE_JS)
        return 2

    real_cfg = os.path.join(ROOT, "config", "settings.json")
    before = digest(real_cfg)

    iso = tempfile.mkdtemp(prefix="wr_uimatrix_")
    iso_settings = os.path.join(iso, "settings.json")
    with open(FACTORY, encoding="utf-8") as f:
        cfg = json.load(f)
    # 基线：短休息 25 分钟倒计时（20 秒）+ 喝水提醒 45 分钟；长休息与外卖关掉。
    # 其余场景由探针自己用 saveSettings 改（深合并，只给局部对象）。
    cfg["short"] = dict(cfg.get("short") or {}, enabled=True, mode="countdown",
                        intervalMs=25 * 60 * 1000, durationMs=20000, notifyBeforeMs=0,
                        postponeMs=120000, pauseAction="postpone", slots=[])
    cfg["long"] = dict(cfg.get("long") or {}, enabled=False)
    cfg["takeout"] = dict(cfg.get("takeout") or {}, enabled=False, slots=[])
    cfg["customBreaks"] = [{
        "id": "cb-water",
        "name": "喝水提醒",
        "config": {"enabled": True, "mode": "countdown", "intervalMs": 45 * 60 * 1000,
                   "durationMs": 20000, "notifyBeforeMs": 0, "postponeMs": 120000,
                   "pauseAction": "postpone", "slots": []},
    }]
    # 自动判定全关：无头模式下它们本来也不跑，配置侧关干净免得将来变成环境依赖
    cfg["dndPause"] = False
    cfg["fullscreenPause"] = False
    cfg["castMirrorPause"] = False
    cfg["castWirelessPause"] = False
    cfg["idlePauseMin"] = 60
    cfg.setdefault("appPause", {})["enabled"] = False
    with open(iso_settings, "w", encoding="utf-8") as f:
        json.dump(cfg, f, ensure_ascii=False, indent=2)

    port = free_port(29746)
    env = dict(os.environ)
    env["WAILS_SERVER_PORT"] = str(port)
    env["WAILS_SERVER_HOST"] = "127.0.0.1"
    env["WR_SETTINGS_PATH"] = iso_settings
    env.pop("ELECTRON_RUN_AS_NODE", None)
    base = "http://127.0.0.1:%d" % port
    log_path = os.path.join(iso, "server.log")
    logf = open(log_path, "w", encoding="utf-8")
    proc = subprocess.Popen([EXE], cwd=ROOT, env=env, stdout=logf, stderr=subprocess.STDOUT)
    rc = 0
    try:
        up = False
        for _ in range(60):
            if proc.poll() is not None:
                break
            if http_get(base + "/") == 200:
                up = True
                break
            time.sleep(0.5)
        if not up:
            print("无头服务没起来，日志尾部：")
            print(open(log_path, encoding="utf-8", errors="replace").read()[-1500:])
            return 1

        node = shutil.which("node")
        if not node:
            print("找不到 node")
            return 2
        penv = dict(os.environ)
        penv["WR_BASE"] = base
        r = subprocess.run([node, PROBE_JS], cwd=ROOT, env=penv, text=True,
                           encoding="utf-8", errors="replace", timeout=300, capture_output=True)
        print(r.stdout or "")
        if "结果：" not in (r.stdout or ""):
            print("!! 探针没有输出断言汇总，脚本可能提前退出")
            rc = 1
        if r.returncode != 0:
            rc = 1
            err = (r.stderr or "").strip()
            if err:
                print("--- stderr ---\n" + err[-2000:])
        if rc != 0:
            print("--- 服务日志尾部 ---")
            print(open(log_path, encoding="utf-8", errors="replace").read()[-1500:])
    finally:
        if proc.poll() is None:
            proc.terminate()
            try:
                proc.wait(timeout=5)
            except Exception:
                proc.kill()
        logf.close()
        shutil.rmtree(iso, ignore_errors=True)

    after = digest(real_cfg)
    if before != after:
        print("真实 config/settings.json 被改写了：%s" % real_cfg)
        return 1
    print("真实 settings.json 未被改动")
    return rc


if __name__ == "__main__":
    sys.exit(main())
