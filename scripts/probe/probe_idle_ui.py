#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""“系统空闲”这一行的 UI 探针驱动：起无头服务 → 跑 idle_ui_probe.js → 收断言。

与 probe_settings_ui.py 同一套起服务 / 隔离配置 / 清理口径，只是本支只跑一个脚本，
回答三件事：勾选态与配置是否双向一致、分钟数边界夹紧对不对、这一行的排版（文字列、
行高、溢出、暗色、最小宽）有没有坏。

用法：python scripts/probe/probe_idle_ui.py
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
PROBE_JS = os.path.join(HERE, "idle_ui_probe.js")
OUT = os.path.join(tempfile.gettempdir(), "wr_idle_ui")

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
        print("找不到 %s" % EXE)
        return 2
    if not os.path.exists(PROBE_JS):
        print("找不到 %s" % PROBE_JS)
        return 2

    real_cfg = os.path.join(ROOT, "config", "settings.json")
    before = digest(real_cfg)

    iso = tempfile.mkdtemp(prefix="wr_idle_ui_")
    iso_settings = os.path.join(iso, "settings.json")
    with open(FACTORY, encoding="utf-8") as f:
        cfg = json.load(f)
    # 出厂态：系统空闲开、5 分钟。断言里的“初始值”就以它为准
    with open(iso_settings, "w", encoding="utf-8") as f:
        json.dump(cfg, f, ensure_ascii=False, indent=2)

    port = free_port(29751)
    log_path = os.path.join(iso, "server.log")
    env = dict(os.environ)
    env["WAILS_SERVER_PORT"] = str(port)
    env["WAILS_SERVER_HOST"] = "127.0.0.1"
    env["WR_SETTINGS_PATH"] = iso_settings
    env["WR_LOG_LEVEL"] = "debug"
    env.pop("ELECTRON_RUN_AS_NODE", None)
    base = "http://127.0.0.1:%d" % port
    logf = open(log_path, "w", encoding="utf-8")
    proc = subprocess.Popen([EXE], cwd=ROOT, env=env, stdout=logf, stderr=subprocess.STDOUT)
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

        penv = dict(os.environ)
        penv["WR_BASE"] = base
        penv["WR_OUT"] = OUT
        node = shutil.which("node")
        if not node:
            print("找不到 node")
            return 2
        r = subprocess.run([node, PROBE_JS], cwd=ROOT, env=penv, text=True,
                           encoding="utf-8", errors="replace", timeout=300, capture_output=True)
        print(r.stdout or "")
        rc = r.returncode
        if "结果：" not in (r.stdout or ""):
            print("!! 探针没有输出断言汇总，脚本可能提前退出")
            rc = 1
        if r.stderr and r.stderr.strip():
            print("--- stderr ---\n" + r.stderr.strip()[-2000:])
    finally:
        if proc.poll() is None:
            proc.terminate()
            try:
                proc.wait(timeout=5)
            except Exception:
                proc.kill()
        logf.close()
        shutil.rmtree(iso, ignore_errors=True)

    if before != digest(real_cfg):
        print("真实 config/settings.json 被改写了：%s" % real_cfg, file=sys.stderr)
        return 1
    print("真实 settings.json 未被改动")
    return 0 if rc == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
