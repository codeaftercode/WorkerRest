#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""一次性：起无头服务 + 跑单个探针 JS（临时工具，用完可删）。

用法：python scripts/oneoff/run_one_probe.py scripts/probe/settings_tip_probe.js
"""
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


def http_get(url):
    try:
        with urllib.request.urlopen(url, timeout=2) as r:
            return r.status
    except Exception:
        return 0


def main():
    js = sys.argv[1] if len(sys.argv) > 1 else ""
    if not os.path.exists(js):
        print("找不到 %s" % js)
        return 2
    iso = tempfile.mkdtemp(prefix="wr_one_")
    cfg_path = os.path.join(iso, "settings.json")
    with open(FACTORY, encoding="utf-8") as f:
        cfg = json.load(f)
    cfg.setdefault("supervisor", {})["enabled"] = True
    cfg.setdefault("sound", {})["enabled"] = True
    with open(cfg_path, "w", encoding="utf-8") as f:
        json.dump(cfg, f, ensure_ascii=False, indent=2)

    port = free_port(29781)
    base = "http://127.0.0.1:%d" % port
    env = dict(os.environ)
    env["WAILS_SERVER_PORT"] = str(port)
    env["WAILS_SERVER_HOST"] = "127.0.0.1"
    env["WR_SETTINGS_PATH"] = cfg_path
    env["WR_LOG_LEVEL"] = "error"
    env.pop("ELECTRON_RUN_AS_NODE", None)
    log = open(os.path.join(iso, "server.log"), "w", encoding="utf-8")
    proc = subprocess.Popen([EXE], cwd=ROOT, env=env, stdout=log, stderr=subprocess.STDOUT)
    try:
        for _ in range(60):
            if http_get(base + "/") == 200:
                break
            time.sleep(0.5)
        else:
            print("服务没起来")
            return 1
        penv = dict(os.environ)
        penv["WR_BASE"] = base
        penv["WR_OUT"] = iso
        node = shutil.which("node")
        r = subprocess.run([node, js], cwd=ROOT, env=penv, text=True,
                           encoding="utf-8", errors="replace", capture_output=True, timeout=300)
        print(r.stdout or "")
        if r.stderr.strip():
            print("--- stderr ---\n" + r.stderr.strip()[-1500:])
        return r.returncode
    finally:
        if proc.poll() is None:
            proc.terminate()
            try:
                proc.wait(timeout=5)
            except Exception:
                proc.kill()
        log.close()
        shutil.rmtree(iso, ignore_errors=True)


if __name__ == "__main__":
    sys.exit(main())
