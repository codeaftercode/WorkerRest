#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""验证用：构建无头服务 + 隔离配置 + 起服务 + 跑 about_shot.js 截关于页 + 关闭。"""
import os
import shutil
import socket
import subprocess
import sys
import time
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)
EXE = os.path.join(ROOT, "build", "bin", "chibingren_server.exe")
PROBE = os.path.join(HERE, "about_shot.js")
TEMP = os.environ.get("TEMP", ROOT)
OUT = os.path.join(TEMP, "wr_about_shots")
ISO = os.path.join(TEMP, "wr_about_iso")
LOG = os.path.join(TEMP, "wr_about_server.log")


def free_port(preferred):
    for cand in (preferred, 0):
        with socket.socket() as s:
            try:
                s.bind(("127.0.0.1", cand))
                return s.getsockname()[1]
            except OSError:
                continue
    raise SystemExit("没有可用端口")


def wait_up(base, proc, timeout=20.0):
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
        print("找不到 node"); return 2
    if not os.path.isdir(os.path.join(ROOT, "node_modules", "playwright")):
        print("找不到 playwright"); return 2

    print("构建无头服务…")
    b = subprocess.run(
        ["go", "build", "-tags", "server", "-o", EXE, "-ldflags", "-s -w", "."],
        cwd=ROOT, capture_output=True, text=True, encoding="utf-8", errors="replace",
    )
    if b.returncode != 0:
        print("构建失败:\n" + (b.stderr or b.stdout)); return 2

    port = free_port(29737)
    base = "http://127.0.0.1:%d" % port

    # 隔离配置：绝不碰真实文件
    real_settings = os.path.join(ROOT, "config", "settings.json")
    shutil.rmtree(ISO, ignore_errors=True)
    os.makedirs(ISO, exist_ok=True)
    iso_settings = os.path.join(ISO, "settings.json")
    if os.path.exists(real_settings):
        shutil.copyfile(real_settings, iso_settings)
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
            print(open(LOG, encoding="utf-8", errors="replace").read()[-2000:]); return 1
        print("服务就绪 %s" % base)
        penv = dict(os.environ)
        penv["WR_BASE"] = base
        penv["WR_OUT"] = OUT
        r = subprocess.run([node_cmd, PROBE], cwd=ROOT, env=penv,
                           capture_output=True, text=True, timeout=120,
                           encoding="utf-8", errors="replace")
        print(r.stdout or "")
        if r.returncode != 0:
            print("--- stderr ---\n" + (r.stderr or "")[-2000:]); return 1
        for f in sorted(x for x in os.listdir(OUT) if x.endswith(".png")):
            print("  %s (%d bytes)" % (f, os.path.getsize(os.path.join(OUT, f))))
    finally:
        if srv.poll() is None:
            srv.kill()
    return 0


if __name__ == "__main__":
    sys.exit(main())
