#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""一次性：正文缩进 24 之后，逐张卡拍一遍（临时工具，定稿可删）。

用法：python scripts/oneoff/shot_cards.py [输出目录]
产物：<输出目录>/card_<name>_<宽>.png，name ∈ tl / short / long / tk
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

SHOT = r"""
const { chromium } = require('playwright-core');
(async () => {
  const base = process.env.WR_BASE;
  const out = process.env.WR_OUT;
  const browser = await chromium.launch({ channel: 'msedge', headless: true });
  const errs = [];
  for (const w of [874, 760]) {
    const page = await browser.newPage({ viewport: { width: w, height: 900 }, deviceScaleFactor: 2 });
    page.on('pageerror', (e) => errs.push(String(e && e.message || e)));
    await page.goto(base + '/settings.html', { waitUntil: 'load' });
    await page.waitForFunction(() => typeof window.api === 'object' && window.api !== null, null, { timeout: 15000 });
    await page.waitForTimeout(900);
    await page.evaluate(() => {
      [...document.querySelectorAll('.nav-item')].find((b) => b.dataset.tab === 'timing').click();
    });
    await page.waitForTimeout(700);
    for (const id of ['tl-card', 'short-card', 'long-card', 'tk-card']) {
      const loc = page.locator('#' + id);
      if (await loc.count()) await loc.screenshot({ path: out + '/card_' + id + '_' + w + '.png' });
    }
    if (w === 874) {
      await page.evaluate(() => document.documentElement.setAttribute('data-theme', 'dark'));
      await page.waitForTimeout(300);
      await page.locator('#short-card').screenshot({ path: out + '/card_short-card_874_dark.png' });
      await page.evaluate(() => document.documentElement.setAttribute('data-theme', 'light'));
    }
    await page.close();
  }
  console.log(JSON.stringify({ errors: errs.slice(0, 5) }));
  await browser.close();
})().catch((e) => { console.error(e); process.exit(1); });
"""


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
    outdir = sys.argv[1] if len(sys.argv) > 1 else os.path.join(tempfile.gettempdir(), "wr_cards")
    os.makedirs(outdir, exist_ok=True)
    iso = os.path.join(tempfile.gettempdir(), "wr_cards_cfg")
    shutil.rmtree(iso, ignore_errors=True)
    os.makedirs(iso)
    cfg_path = os.path.join(iso, "settings.json")
    with open(FACTORY, encoding="utf-8") as f:
        cfg = json.load(f)
    sh = cfg.setdefault("short", {})
    sh["mode"] = "fixed"
    sh["slots"] = [
        {"hour": 7, "minute": 42, "enabled": True, "repeat": "daily", "days": []},
        {"hour": 9, "minute": 45, "enabled": True, "repeat": "weekly", "days": [1, 2, 3, 4, 5]},
        {"hour": 15, "minute": 20, "enabled": False, "repeat": "weekly", "days": [6, 7]},
    ]
    tk = cfg.setdefault("takeout", {})
    tk["mode"] = "fixed"
    tk["slots"] = [
        {"hour": 11, "minute": 30, "enabled": True, "repeat": "weekly", "days": [1, 2, 3, 4, 5]},
        {"hour": 17, "minute": 10, "enabled": True, "repeat": "daily", "days": []},
    ]
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
        js_path = os.path.join(HERE, "_tmp_shot_cards.js")
        with open(js_path, "w", encoding="utf-8") as f:
            f.write(SHOT)
        try:
            penv = dict(os.environ)
            penv["WR_BASE"] = base
            penv["WR_OUT"] = outdir
            r = subprocess.run([shutil.which("node"), js_path], cwd=os.path.join(ROOT, "scripts"),
                               env=penv, text=True, encoding="utf-8", errors="replace",
                               capture_output=True, timeout=300)
            print(r.stdout.strip() or r.stderr.strip()[-1200:])
            if r.returncode != 0:
                return 1
        finally:
            if os.path.exists(js_path):
                os.remove(js_path)
        print("[out] " + outdir)
        return 0
    finally:
        if proc.poll() is None:
            proc.terminate()
            try:
                proc.wait(timeout=5)
            except Exception:
                proc.kill()
        log.close()


if __name__ == "__main__":
    sys.exit(main())
