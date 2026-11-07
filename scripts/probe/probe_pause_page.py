#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""暂停提示小窗（pause.html）的驱动：起无头服务 → 跑 pause_page_probe.js → 收断言。

为什么单独一支：这扇窗 300×76、每小时才出现一次、只活 7.5 秒，真机上几乎没法手动盯；
而它承担的正是“开会 / 投屏被自动暂停”这个最容易出事的场景。既有探针里 pause.html
从未被渲染过（静态审计才第一次扫到它），所以补这一支。

隔离口径与其余探针一致：配置用出厂默认的一份副本放临时目录，WR_SETTINGS_PATH 指过去，
跑完断言仓库里真实的 config/settings.json 一个字节没变。

用法：python scripts/probe/probe_pause_page.py
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
PROBE_JS = os.path.join(HERE, "pause_page_probe.js")
PRELOAD = os.path.join(ROOT, "scripts", "probe_preload.js")
OUT = os.path.join(tempfile.gettempdir(), "wr_pause_page")

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

    os.makedirs(OUT, exist_ok=True)
    real_cfg = os.path.join(ROOT, "config", "settings.json")
    before = digest(real_cfg)

    iso = tempfile.mkdtemp(prefix="wr_pausepage_")
    iso_settings = os.path.join(iso, "settings.json")
    with open(FACTORY, encoding="utf-8") as f:
        cfg = json.load(f)
    # 这一支只看页面本身：把调度相关的都关掉，免得后台真弹窗干扰页面度量
    cfg["short"] = dict(cfg.get("short") or {}, enabled=False)
    cfg["long"] = dict(cfg.get("long") or {}, enabled=False)
    cfg["takeout"] = dict(cfg.get("takeout") or {}, enabled=False)
    cfg["customBreaks"] = []
    cfg["idlePauseMin"] = 60
    cfg.setdefault("appPause", {})["enabled"] = False
    with open(iso_settings, "w", encoding="utf-8") as f:
        json.dump(cfg, f, ensure_ascii=False, indent=2)

    port = free_port(29757)
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
        penv["WR_SHOT_DIR"] = OUT
        if os.path.exists(PRELOAD):
            penv["WR_PRELOAD"] = PRELOAD
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
