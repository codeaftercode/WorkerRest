#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""一次性截图 + 几何采样：「时间规划」页的提醒卡片（临时工具，用完可删）。

用法：python scripts/oneoff/shot_timing.py [输出目录]
产物：<输出目录>/timing_<宽>.png（整页）、timing_<宽>_short.png（短休息卡）
      + 终端打印控件几何（标签列 / 控件列 / 分段控件宽度）

为什么要在无头服务里拍：dist 是固化产物，真窗口拍不到；无头服务渲染同一份 HTML/CSS/JS，
只是数据由 shim 喂进来，所以版式与真窗口逐像素同源，尺寸还能自己定。
注意：dist 改动后必须先重建 build/bin/rest_server.exe（//go:embed 固化），否则这里还是旧画面。
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

WIDTHS = [874, 800]

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


SHOT = r"""
const { chromium } = require('playwright-core');
(async () => {
  const base = process.env.WR_BASE;
  const out = process.env.WR_OUT;
  const w = Number(process.env.WR_W);
  const browser = await chromium.launch({ channel: 'msedge', headless: true,
    args: ['--force-device-scale-factor=1'] });
  const page = await browser.newPage({ viewport: { width: w, height: 900 },
    deviceScaleFactor: 2 });
  const errs = [];
  page.on('pageerror', (e) => errs.push(String(e && e.message || e)));
  page.on('console', (m) => { if (m.type() === 'error') errs.push(m.text()); });
  await page.goto(base + '/settings.html', { waitUntil: 'load' });
  await page.waitForFunction(() => typeof window.api === 'object' && window.api !== null,
    null, { timeout: 15000 });
  await page.waitForTimeout(900);
  await page.evaluate(() => {
    [...document.querySelectorAll('.nav-item')].find((b) => b.dataset.tab === 'timing').click();
  });
  await page.waitForTimeout(700);
  // 短休息：固定时间 + 摊开时刻表（三种态里最复杂的一种）
  await page.locator("#short-card .slot-fixed .msg-toggle").click();
  await page.waitForTimeout(300);
  const geo = await page.evaluate(() => {
    const r = (sel) => {
      const el = document.querySelector(sel);
      if (!el) return null;
      const b = el.getBoundingClientRect();
      return { l: Math.round(b.left), w: Math.round(b.width), h: Math.round(b.height) };
    };
    return {
      card: r('#short-card'),
      head: r('#short-card > .card-head'),
      h3: r('#short-card > .card-head > h3'),
      sw: r('#short-card > .card-head > .card-switch'),
      sum: r('#short-summary'),
      preview: r('#short-preview'),
      interval: r('#short-interval'),
      duration: r('#short-duration'),
      gate: r('#short-idleGate-sec'),
      mode: r('#short-mode'),
      trigLabel: r('#short-card .pg[data-pg="trigger"] .row > label'),
      trigHead: r('#short-card .pg[data-pg="trigger"] > .pg-head'),
      remHead: r('#short-card .pg[data-pg="reminder"] > .pg-head'),
      msgHead: r('#short-card > .msg-panel > .msg-head'),
      msgBody: r('#short-card > .msg-panel > .msg-body'),
      slotHead: r('#short-card .slot-head'),
    };
  });
  await page.locator('#short-card').screenshot({ path: out + '/timing_' + w + '_short.png' });
  await page.locator('#long-card').screenshot({ path: out + '/timing_' + w + '_long.png' });
  // 关闭态：卡头开关拨到 off，正文应当整体降透明度（卡头不降）
  await page.locator('#long-card .card-head .card-switch').click();
  await page.waitForTimeout(450);
  await page.locator('#long-card').screenshot({ path: out + '/timing_' + w + '_long_off.png' });
  const offGeo = await page.evaluate(() => {
    const card = document.getElementById('long-card');
    const body = card.querySelector('.pg');
    const head = card.querySelector('.card-head');
    return {
      cls: card.className,
      bodyOpacity: getComputedStyle(body).opacity,
      headOpacity: getComputedStyle(head).opacity,
      summary: (document.getElementById('long-summary') || {}).textContent || '',
    };
  });
  await page.locator('#long-card .card-head .card-switch').click();
  await page.waitForTimeout(350);
  await page.locator('#tk-card').screenshot({ path: out + '/timing_' + w + '_tk.png' });
  await page.screenshot({ path: out + '/timing_' + w + '.png', fullPage: true });
  // 深色同一张卡：段控的凹槽/浮起是这套里最依赖明度差的地方，两套主题都要看一眼
  await page.evaluate(() => document.documentElement.setAttribute('data-theme', 'dark'));
  await page.waitForTimeout(300);
  await page.locator('#short-card').screenshot({ path: out + '/timing_' + w + '_short_dark.png' });
  await page.evaluate(() => document.documentElement.setAttribute('data-theme', 'light'));
  await page.waitForTimeout(200);
  console.log(JSON.stringify({ w, geo, offGeo, errors: errs.slice(0, 5) }));
  await browser.close();
})().catch((e) => { console.error(e); process.exit(1); });
"""


def main():
    outdir = sys.argv[1] if len(sys.argv) > 1 else os.path.join(tempfile.gettempdir(), "wr_timing")
    os.makedirs(outdir, exist_ok=True)

    iso = os.path.join(tempfile.gettempdir(), "wr_timing_cfg")
    shutil.rmtree(iso, ignore_errors=True)
    os.makedirs(iso)
    cfg_path = os.path.join(iso, "settings.json")
    with open(FACTORY, encoding="utf-8") as f:
        cfg = json.load(f)
    # 让短休息停在「固定时间 + 两条时刻」，长休息停在倒计时 —— 三种态一屏看全
    sh = cfg.setdefault("short", {})
    sh["mode"] = "fixed"
    sh["slots"] = [
        {"hour": 9, "minute": 30, "enabled": True, "repeat": "daily", "days": []},
        {"hour": 14, "minute": 0, "enabled": True, "repeat": "weekdays", "days": []},
        {"hour": 21, "minute": 15, "enabled": False, "repeat": "daily", "days": []},
    ]
    cfg.setdefault("long", {})["mode"] = "countdown"
    with open(cfg_path, "w", encoding="utf-8") as f:
        json.dump(cfg, f, ensure_ascii=False, indent=2)

    port = free_port(29771)
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
            print(open(os.path.join(iso, "server.log"), encoding="utf-8", errors="replace").read()[-1200:])
            return 1

        js_path = os.path.join(HERE, "_tmp_shot_timing.js")
        with open(js_path, "w", encoding="utf-8") as f:
            f.write(SHOT)
        try:
            for w in WIDTHS:
                penv = dict(os.environ)
                penv["WR_BASE"] = base
                penv["WR_OUT"] = outdir
                penv["WR_W"] = str(w)
                node = shutil.which("node")
                r = subprocess.run([node, js_path], cwd=os.path.join(ROOT, "scripts"), env=penv,
                                   text=True, encoding="utf-8", errors="replace",
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
