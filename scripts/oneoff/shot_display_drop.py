#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""一次性截图：把“显示的屏幕”面板（含底部刷新行）拍下来，人工核对版式。

用法：python scripts/oneoff/shot_display_drop.py
产物：%TEMP%/wr_shot/display_drop.png、display_drop_open.png
与其他截图脚本同一分工：Python 起无头服务 + 隔离配置，Node/Playwright 只负责拍。
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
OUT = os.path.join(tempfile.gettempdir(), "wr_shot")

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


JS = r"""
const { chromium } = require('playwright-core');
(async () => {
  const base = process.env.WR_BASE;
  const out = process.env.WR_OUT;
  const browser = await chromium.launch({ channel: 'msedge', headless: true,
    args: ['--force-device-scale-factor=1'] });
  const page = await browser.newPage({ viewport: { width: 1180, height: 900 } });
  await page.goto(base + '/settings.html', { waitUntil: 'load' });
  await page.waitForFunction(() => {
    const h = document.getElementById('ap-displays');
    return !!h && !!h.querySelector('.cdrop-btn');
  }, null, { timeout: 8000 }).catch(() => {});
  await page.evaluate(() => document.getElementById('ap-displays').scrollIntoView({ block: 'center' }));
  await new Promise(r => setTimeout(r, 400));
  const row = await page.evaluate(() => {
    const h = document.getElementById('ap-displays');
    const b = h.getBoundingClientRect();
    return { x: b.x - 140, y: b.y - 20, width: 700, height: 120 };
  });
  await page.screenshot({ path: out + '/display_drop.png', clip: row });
  await page.click('#ap-displays .cdrop-btn');
  await new Promise(r => setTimeout(r, 350));
  const menu = await page.evaluate(() => {
    const m = document.querySelector('#ap-displays .cdrop-menu').getBoundingClientRect();
    return { x: Math.max(0, m.x - 140), y: Math.max(0, m.y - 10), width: m.width + 180, height: m.height + 30 };
  });
  await page.screenshot({ path: out + '/display_drop_open.png', clip: menu });
  console.log('shot ok');
  await browser.close();
})().catch((e) => { console.error(e); process.exit(1); });
"""


def main():
    os.makedirs(OUT, exist_ok=True)
    iso = os.path.join(tempfile.gettempdir(), "wr_shot_cfg")
    shutil.rmtree(iso, ignore_errors=True)
    os.makedirs(iso)
    cfg_path = os.path.join(iso, "settings.json")
    shutil.copy(FACTORY, cfg_path)

    port = free_port(29751)
    base = "http://127.0.0.1:%d" % port
    env = dict(os.environ)
    env["WAILS_SERVER_PORT"] = str(port)
    env["WAILS_SERVER_HOST"] = "127.0.0.1"
    env["WR_SETTINGS_PATH"] = cfg_path
    env["WAILS_LOG_LEVEL"] = "error"
    env.pop("ELECTRON_RUN_AS_NODE", None)
    log = open(os.path.join(iso, "server.log"), "w", encoding="utf-8")
    proc = subprocess.Popen([EXE], cwd=ROOT, env=env, stdout=log, stderr=subprocess.STDOUT)
    try:
        for _ in range(60):
            if http_get(base + "/") == 200:
                break
            time.sleep(0.5)
        else:
            print("服务没起来", file=sys.stderr)
            return 1
        # 必须落在 scripts/ 里：playwright-core 装在 scripts/node_modules，
        # 写在 temp 目录里 require 会解析不到（node 只按脚本所在目录往上找 node_modules）。
        js_path = os.path.join(HERE, "_tmp_shot.js")
        with open(js_path, "w", encoding="utf-8") as f:
            f.write(JS)
        penv = dict(os.environ)
        penv["WR_BASE"] = base
        penv["WR_OUT"] = OUT
        node = shutil.which("node")
        try:
            r = subprocess.run([node, js_path], cwd=os.path.join(ROOT, "scripts"), env=penv,
                               text=True, encoding="utf-8", errors="replace",
                               capture_output=True, timeout=180)
        finally:
            if os.path.exists(js_path):
                os.remove(js_path)
        print(r.stdout.strip() or r.stderr.strip()[-800:])
        return 0 if r.returncode == 0 else 1
    finally:
        if proc.poll() is None:
            proc.terminate()
            try:
                proc.wait(timeout=5)
            except Exception:
                proc.kill()
        log.close()
        json.dump({}, open(os.path.join(iso, "done"), "w"))


if __name__ == "__main__":
    sys.exit(main())
