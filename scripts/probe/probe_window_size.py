#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""测量用：起无头服务 + 隔离配置，跑 probe_window_size.js 打印两组测量数据。

只读：不写配置、不改源码；服务用隔离配置启动，收尾不碰真实 config/settings.json。
"""
import os
import shutil
import socket
import subprocess
import sys
import time
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(os.path.dirname(HERE))
EXE = os.path.join(ROOT, "build", "bin", "chibingren_server.exe")
PROBE = os.path.join(HERE, "probe_window_size.js")
TEMP = os.environ.get("TEMP", ROOT)
ISO = os.path.join(TEMP, "wr_winsize_iso")
LOG = os.path.join(TEMP, "wr_winsize_server.log")


def free_port(preferred):
    for cand in (preferred, 0):
        with socket.socket() as s:
            try:
                s.bind(("127.0.0.1", cand))
                return s.getsockname()[1]
            except OSError:
                continue
    raise SystemExit("没有可用端口")


def wait_up(base, proc, timeout=25.0):
    t0 = time.time()
    while time.time() - t0 < timeout:
        if proc.poll() is not None:
            return False
        try:
            with urllib.request.urlopen(base + "/", timeout=2) as r:
                if r.status == 200:
                    return True
        except Exception:
            pass
        time.sleep(0.4)
    return False


def main():
    try:
        sys.stdout.reconfigure(encoding="utf-8", errors="replace")
        sys.stderr.reconfigure(encoding="utf-8", errors="replace")
    except Exception:
        pass

    node_cmd = shutil.which("node")
    if not node_cmd:
        print("找不到 node")
        return 2
    if not os.path.isdir(os.path.join(ROOT, "node_modules", "playwright")):
        print("找不到 playwright")
        return 2

    if os.environ.get("WR_NO_BUILD") != "1":
        print("构建无头服务…")
        b = subprocess.run(
            ["go", "build", "-tags", "server", "-o", EXE, "-ldflags", "-s -w", "."],
            cwd=ROOT, capture_output=True, text=True, encoding="utf-8", errors="replace",
        )
        if b.returncode != 0:
            print("构建失败:\n" + (b.stderr or b.stdout))
            return 2

    port = free_port(29741)
    base = "http://127.0.0.1:%d" % port

    shutil.rmtree(ISO, ignore_errors=True)
    os.makedirs(ISO, exist_ok=True)
    iso_settings = os.path.join(ISO, "settings.json")
    real = os.path.join(ROOT, "config", "settings.json")
    if os.path.exists(real):
        shutil.copyfile(real, iso_settings)
    else:
        open(iso_settings, "w", encoding="utf-8").write("{}")

    env = dict(os.environ)
    env["WAILS_SERVER_PORT"] = str(port)
    env["WAILS_SERVER_HOST"] = "127.0.0.1"
    env["WR_SETTINGS_PATH"] = iso_settings
    env.pop("ELECTRON_RUN_AS_NODE", None)

    logf = open(LOG, "w", encoding="utf-8")
    srv = subprocess.Popen([EXE], cwd=ROOT, env=env, stdout=logf, stderr=subprocess.STDOUT)
    try:
        if not wait_up(base, srv):
            print(open(LOG, encoding="utf-8", errors="replace").read()[-2000:])
            return 1
        print("服务就绪 %s" % base)
        penv = dict(os.environ)
        penv["WR_BASE"] = base
        r = subprocess.run([node_cmd, PROBE], cwd=ROOT, env=penv,
                           capture_output=True, text=True, timeout=600,
                           encoding="utf-8", errors="replace")
        print(r.stdout or "")
        if r.returncode != 0:
            print("--- stderr ---\n" + (r.stderr or "")[-3000:])
            return 1
    finally:
        if srv.poll() is None:
            srv.kill()
    return 0


if __name__ == "__main__":
    sys.exit(main())
