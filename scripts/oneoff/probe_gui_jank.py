#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""真窗口测量驱动：构建 GUI 产物（临时路径）→ 隔离配置启动 → CDP 连 WebView2 → 跑被测脚本。

用法：GUI_SCRIPT=probe_gui_led2.js python scripts/oneoff/probe_gui_jank.py
前提一：本机有桌面会话（GUI 探针 probe_window_gui.py 也是这么跑的）。
前提二：为了让 CDP 能连上真窗口，需要**临时**给 main.go 的 AdditionalBrowserArgs
        加一个只认环境变量的调试端口（用完还原，别提交）：

            func measureArgs() []string {
                if p := os.Getenv("WR_DEVTOOLS_PORT"); p != "" {
                    return []string{"--remote-debugging-port=" + p}
                }
                return nil
            }
            // ……
            AdditionalBrowserArgs: append([]string{
                "--autoplay-policy=no-user-gesture-required",
                "--disable-pinch",
                "--disable-direct-composition",
            }, measureArgs()...),

被测脚本（scripts/oneoff/）：
  · probe_gui_led2.js  点侧栏各页时，数码管数字的跳秒节奏 + 点下去到页面显示的延迟
  · probe_gui_led3.js  把 window.api.getState 拖慢 1.5s，验证读数是否已与每秒往返解耦
"""
import hashlib
import json
import os
import shutil
import socket
import subprocess
import sys
import time
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(os.path.dirname(HERE))
TEMP = os.environ.get("TEMP", ROOT)
EXE = os.path.join(TEMP, "wr_gui_jank", "rest_gui.exe")
ISO = os.path.join(TEMP, "wr_gui_jank_iso")
OUT = os.path.join(TEMP, "wr_gui_jank")
LOG = os.path.join(TEMP, "wr_gui_jank_gui.log")
REAL = os.path.join(ROOT, "config", "settings.json")


def digest(p):
    if not p or not os.path.isfile(p):
        return None
    with open(p, "rb") as f:
        return hashlib.sha256(f.read()).hexdigest()


def free_port():
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def cdp_up(port, timeout=30.0):
    t0 = time.time()
    url = "http://127.0.0.1:%d/json/version" % port
    while time.time() - t0 < timeout:
        try:
            with urllib.request.urlopen(url, timeout=1) as r:
                if r.status == 200:
                    return True
        except Exception:
            time.sleep(0.4)
    return False


def main():
    try:
        sys.stdout.reconfigure(encoding="utf-8", errors="replace")
    except Exception:
        pass
    real_digest = digest(REAL)

    os.makedirs(os.path.dirname(EXE), exist_ok=True)
    print("构建 GUI 产物（临时路径，不动 build/bin）…")
    b = subprocess.run(
        ["go", "build", "-trimpath", "-o", EXE, "-ldflags", "-s -w", "./cmd/workerrest"],
        cwd=ROOT, capture_output=True, text=True, encoding="utf-8", errors="replace",
    )
    if b.returncode != 0:
        print("构建失败:\n" + (b.stderr or b.stdout)[-3000:])
        return 2

    shutil.rmtree(ISO, ignore_errors=True)
    os.makedirs(ISO, exist_ok=True)
    iso = os.path.join(ISO, "settings.json")
    if os.path.isfile(REAL):
        shutil.copyfile(REAL, iso)
        cfg = json.load(open(iso, encoding="utf-8"))
        cfg.setdefault("short", {}).update({"enabled": True, "mode": "countdown", "intervalMs": 3600000, "notifyBeforeMs": 60000})
        cfg["long"] = dict(cfg.get("long") or {}, enabled=False)
        cfg["takeout"] = dict(cfg.get("takeout") or {}, enabled=False)
        cfg["supervisor"] = dict(cfg.get("supervisor") or {}, enabled=True)
        cfg.setdefault("window", {})["startMinimized"] = False
        with open(iso, "w", encoding="utf-8") as f:
            json.dump(cfg, f, ensure_ascii=False, indent=2)

    port = free_port()
    env = dict(os.environ)
    env["WR_SETTINGS_PATH"] = iso
    env["WR_DEVTOOLS_PORT"] = str(port)
    env.pop("ELECTRON_RUN_AS_NODE", None)
    logf = open(LOG, "w", encoding="utf-8")
    proc = subprocess.Popen([EXE], cwd=ROOT, env=env, stdout=logf, stderr=subprocess.STDOUT)
    try:
        if not cdp_up(port):
            print("CDP 没起来（窗口没建 / WebView2 没带调试端口），看日志尾部：")
            print(open(LOG, encoding="utf-8", errors="replace").read()[-2000:])
            return 2
        node = shutil.which("node")
        penv = dict(os.environ)
        penv["CDP_URL"] = "http://127.0.0.1:%d" % port
        penv["WR_OUT"] = OUT
        penv.pop("ELECTRON_RUN_AS_NODE", None)
        r = subprocess.run(
            [node, os.path.join("scripts", "oneoff", os.environ.get("GUI_SCRIPT", "probe_gui_jank.js"))],
            cwd=ROOT, env=penv, capture_output=True, text=True, timeout=300,
            encoding="utf-8", errors="replace",
        )
        print(r.stdout or "")
        if r.returncode != 0:
            print("--- node stderr ---")
            print((r.stderr or "")[-3000:])
            return 1
        print("真实配置未被改动:", digest(REAL) == real_digest)
        return 0
    finally:
        proc.terminate()
        try:
            proc.wait(timeout=8)
        except Exception:
            proc.kill()


if __name__ == "__main__":
    sys.exit(main())
