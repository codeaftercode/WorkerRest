#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""一次性对比：组标题竖条距文字的几档间距（临时工具，定稿后可删）。

用法：python scripts/oneoff/shot_title_deco.py [输出目录]
产物：<输出目录>/deco_<档位>.png（“触发条件 + 弹窗设置”两组同框）
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

VARIANTS = {
    "a_gap4": {"left": "-7px", "w": "3px", "h": "13px"},
    "b_gap7": {"left": "-10px", "w": "3px", "h": "13px"},
    "c_thick": {"left": "-8px", "w": "3px", "h": "15px"},
}


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


SHOT = r"""
const { chromium } = require('playwright-core');
(async () => {
  const base = process.env.WR_BASE;
  const out = process.env.WR_OUT;
  const variants = JSON.parse(process.env.WR_VARIANTS);
  const browser = await chromium.launch({ channel: 'msedge', headless: true,
    args: ['--force-device-scale-factor=1'] });
  const page = await browser.newPage({ viewport: { width: 874, height: 900 }, deviceScaleFactor: 2 });
  await page.goto(base + '/settings.html', { waitUntil: 'load' });
  await page.waitForFunction(() => typeof window.api === 'object' && window.api !== null, null, { timeout: 15000 });
  await page.waitForTimeout(900);
  await page.evaluate(() => {
    [...document.querySelectorAll('.nav-item')].find((b) => b.dataset.tab === 'timing').click();
  });
  await page.waitForTimeout(700);
  const geo = await page.evaluate(() => {
    const b = (sel) => { const el = document.querySelector(sel); if (!el) return null;
      const r = el.getBoundingClientRect(); return { l: Math.round(r.left), w: Math.round(r.width) }; };
    const t = document.querySelector('#short-card .pg[data-pg="trigger"] .pg-title');
    const pb = t ? getComputedStyle(t, '::before') : null;
    return { title: b('#short-card .pg[data-pg="trigger"] .pg-title'),
      body: b('#short-card .pg[data-pg="trigger"] > .pg-body'),
      cardLeft: b('#short-card').l, fold: b('#short-card > .card-head > .card-fold'),
      padL: t ? getComputedStyle(t).paddingLeft : null,
      beforeLeft: pb ? pb.left : null, beforeW: pb ? pb.width : null, beforeH: pb ? pb.height : null };
  });
  for (const [name, v] of Object.entries(variants)) {
    await page.addStyleTag({ content: `.pg-title::before{left:${v.left}!important;width:${v.w}!important;height:${v.h}!important;margin-top:calc(${v.h} / -2)!important;}` });
    await page.waitForTimeout(150);
    await page.locator('#short-card').screenshot({ path: out + '/deco_' + name + '.png' });
  }
  console.log(JSON.stringify(geo));
  await browser.close();
})().catch((e) => { console.error(e); process.exit(1); });
"""


def main():
    outdir = sys.argv[1] if len(sys.argv) > 1 else os.path.join(tempfile.gettempdir(), "wr_deco")
    os.makedirs(outdir, exist_ok=True)
    iso = os.path.join(tempfile.gettempdir(), "wr_deco_cfg")
    shutil.rmtree(iso, ignore_errors=True)
    os.makedirs(iso)
    cfg_path = os.path.join(iso, "settings.json")
    with open(FACTORY, encoding="utf-8") as f:
        cfg = json.load(f)
    sh = cfg.setdefault("short", {})
    sh["mode"] = "fixed"
    sh["slots"] = [
        {"hour": 9, "minute": 30, "enabled": True, "repeat": "daily", "days": []},
        {"hour": 14, "minute": 0, "enabled": True, "repeat": "weekdays", "days": []},
    ]
    with open(cfg_path, "w", encoding="utf-8") as f:
        json.dump(cfg, f, ensure_ascii=False, indent=2)

    port = free_port(29773)
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
        js_path = os.path.join(HERE, "_tmp_shot_deco.js")
        with open(js_path, "w", encoding="utf-8") as f:
            f.write(SHOT)
        try:
            penv = dict(os.environ)
            penv["WR_BASE"] = base
            penv["WR_OUT"] = outdir
            penv["WR_VARIANTS"] = json.dumps(VARIANTS)
            r = subprocess.run([shutil.which("node"), js_path], cwd=os.path.join(ROOT, "scripts"),
                               env=penv, text=True, encoding="utf-8", errors="replace",
                               capture_output=True, timeout=180)
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
