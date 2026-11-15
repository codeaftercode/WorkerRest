#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""一次性：督休专员页布局改造后的目检截图（亮 / 暗 / 窄窗 / 宽窗）。

隔离口径与 probe_settings_ui.py 相同：出厂默认配置拷到临时目录，WR_SETTINGS_PATH
指过去；跑完断言仓库里真实的 config/settings.json 没被动过。
输出到 %TEMP%/wr_sup_shots，不入库。
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
OUT = os.path.join(tempfile.gettempdir(), "wr_sup_shots")


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
    from playwright.sync_api import sync_playwright

    real_cfg = os.path.join(ROOT, "config", "settings.json")
    before = digest(real_cfg)
    os.makedirs(OUT, exist_ok=True)

    iso = tempfile.mkdtemp(prefix="wr_shot_")
    iso_settings = os.path.join(iso, "settings.json")
    with open(FACTORY, encoding="utf-8") as f:
        cfg = json.load(f)
    cfg.setdefault("supervisor", {})["enabled"] = True
    with open(iso_settings, "w", encoding="utf-8") as f:
        json.dump(cfg, f, ensure_ascii=False, indent=2)

    port = free_port(29761)
    env = dict(os.environ)
    env["WAILS_SERVER_PORT"] = str(port)
    env["WAILS_SERVER_HOST"] = "127.0.0.1"
    env["WR_SETTINGS_PATH"] = iso_settings
    env.pop("ELECTRON_RUN_AS_NODE", None)
    base = "http://127.0.0.1:%d" % port
    logf = open(os.path.join(iso, "server.log"), "w", encoding="utf-8")
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
            print("服务没起来")
            return 1

        with sync_playwright() as p:
            b = p.chromium.launch(channel="msedge", headless=True)
            preload = os.path.join(ROOT, "scripts", "probe_preload.js")
            for name, w, h, theme in [
                ("sup_1180_light", 1180, 1000, "light"),
                ("sup_1180_dark", 1180, 1000, "dark"),
                ("sup_760", 760, 1000, "light"),
                ("sup_1280", 1280, 1060, "light"),
            ]:
                page = b.new_page(viewport={"width": w, "height": h})
                page.add_init_script(path=preload)
                page.goto(base + "/settings.html", wait_until="domcontentloaded")
                page.wait_for_function("typeof window.api === 'object' && window.api !== null")
                page.wait_for_timeout(600)
                if theme == "dark":
                    page.evaluate("document.documentElement.setAttribute('data-theme','dark')")
                page.click('.nav-item[data-tab="supervisor"]')
                page.wait_for_timeout(900)
                page.screenshot(path=os.path.join(OUT, name + ".png"))
                page.close()
            b.close()
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
    print("截图目录：%s" % OUT)
    if before != after:
        print("!! 真实 config/settings.json 被改写")
        return 1
    print("真实 settings.json 未被改动")
    return 0


if __name__ == "__main__":
    sys.exit(main())
