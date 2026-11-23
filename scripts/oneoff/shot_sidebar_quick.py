#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""一次性：拍侧栏底部「休息提醒」快捷操作组，确认去掉 legend 装饰后的实际渲染。

用法：python scripts/oneoff/shot_sidebar_quick.py [输出目录]
产物：<目录>/quick_<主题>_<宽>.png（整块侧栏，含 nav 上下文）
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
  const page = await browser.newPage({ viewport: { width: 874, height: 900 }, deviceScaleFactor: 4 });
  page.on('pageerror', (e) => errs.push(String(e && e.message || e)));
  await page.goto(base + '/settings.html', { waitUntil: 'load' });
  await page.waitForFunction(() => typeof window.api === 'object' && window.api !== null, null, { timeout: 15000 });
  await page.waitForTimeout(900);

  // 量：legend 的盒子与它背后实际透出的颜色（用 elementFromPoint 反查 + 取计算样式）
  const geo = await page.evaluate(() => {
    const lg = document.querySelector('.quick-group > legend');
    const fg = document.querySelector('.quick-group');
    if (!lg || !fg) return null;
    const r = lg.getBoundingClientRect();
    const fr = fg.getBoundingClientRect();
    const cs = getComputedStyle(lg);
    const before = getComputedStyle(lg, '::before');
    return {
      legend: { x: r.x, y: r.y, w: r.width, h: r.height },
      fieldset: { x: fr.x, y: fr.y, w: fr.width, h: fr.height },
      bg: cs.backgroundColor,
      beforeContent: before.content,
      beforeW: before.width,
      padding: cs.padding,
    };
  });
  console.log('GEO ' + JSON.stringify(geo));

  for (const theme of ['light', 'dark']) {
    await page.evaluate((t) => document.documentElement.setAttribute('data-theme', t), theme);
    await page.waitForTimeout(250);
    await page.locator('#sidebar').screenshot({ path: out + '/quick_' + theme + '_874.png' });
    await page.locator('.quick-group').screenshot({ path: out + '/group_' + theme + '.png' });
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
    outdir = sys.argv[1] if len(sys.argv) > 1 else os.path.join(tempfile.gettempdir(), "wr_quick")
    os.makedirs(outdir, exist_ok=True)
    iso = os.path.join(tempfile.gettempdir(), "wr_quick_cfg")
    shutil.rmtree(iso, ignore_errors=True)
    os.makedirs(iso)
    cfg_path = os.path.join(iso, "settings.json")
    with open(FACTORY, encoding="utf-8") as f:
        cfg = json.load(f)
    with open(cfg_path, "w", encoding="utf-8") as f:
        json.dump(cfg, f, ensure_ascii=False, indent=2)

    port = free_port(29811)
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
        js_path = os.path.join(HERE, "_tmp_shot_quick.js")
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
