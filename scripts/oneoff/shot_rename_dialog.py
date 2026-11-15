#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""一次性：起无头服务 → 跑 shot_rename_dialog.js（命名框图标 / 命名要求文案）。

与 run_one_probe.py 同一套隔离口径，多拷一份 supervisors：本探针会右键重命名
督休专员（虽然最后取消），目录隔离掉才不会碰仓库里的立绘库。

用法：python scripts/oneoff/shot_rename_dialog.py
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
JS = os.path.join(HERE, "shot_rename_dialog.js")

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
    iso = tempfile.mkdtemp(prefix="wr_shot_")
    cfg_path = os.path.join(iso, "settings.json")
    with open(FACTORY, encoding="utf-8") as f:
        cfg = json.load(f)
    cfg.setdefault("supervisor", {})["enabled"] = True
    with open(cfg_path, "w", encoding="utf-8") as f:
        json.dump(cfg, f, ensure_ascii=False, indent=2)

    port = free_port(29791)
    base = "http://127.0.0.1:%d" % port
    env = dict(os.environ)
    env["WAILS_SERVER_PORT"] = str(port)
    env["WAILS_SERVER_HOST"] = "127.0.0.1"
    env["WR_SETTINGS_PATH"] = cfg_path
    env["WR_LOG_LEVEL"] = "error"
    env.pop("ELECTRON_RUN_AS_NODE", None)
    sup_src = os.path.join(ROOT, "assets", "supervisors")
    if os.path.isdir(sup_src):
        iso_sups = os.path.join(iso, "supervisors")
        shutil.copytree(sup_src, iso_sups)
        env["WR_SUPERVISORS_DIR"] = iso_sups
    log = open(os.path.join(iso, "server.log"), "w", encoding="utf-8")
    proc = subprocess.Popen([EXE], cwd=ROOT, env=env, stdout=log, stderr=subprocess.STDOUT)
    out_dir = os.path.join(tempfile.gettempdir(), "wr_rename_dialog")
    try:
        for _ in range(60):
            if http_get(base + "/") == 200:
                break
            time.sleep(0.5)
        else:
            print("服务没起来，日志：")
            print(open(os.path.join(iso, "server.log"), encoding="utf-8", errors="replace").read()[-1200:])
            return 1
        penv = dict(os.environ)
        penv["WR_BASE"] = base
        penv["WR_OUT"] = out_dir
        node = shutil.which("node")
        r = subprocess.run([node, JS], cwd=ROOT, env=penv, text=True,
                           encoding="utf-8", errors="replace", capture_output=True, timeout=300)
        print(r.stdout or "")
        if r.stderr.strip():
            print("--- stderr ---\n" + r.stderr.strip()[-1500:])
        print("截图目录：%s" % out_dir)
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
