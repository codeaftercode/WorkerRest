#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""一次性截图 + 几何采样：「快捷键」页与前三页的版式对齐（临时工具，用完可删）。

用法：python scripts/oneoff/shot_shortcuts.py [输出目录]
产物：<输出目录>/sc_<宽>_<页>.png、sc_<宽>_full.png，终端打印各页的卡 / 行 / 控件的左缘。

隔离口径同 shot_timing.py（出厂配置副本 + WR_SETTINGS_PATH）。
注意：dist 改动后必须先重建 build/bin/rest_server.exe（//go:embed 固化）。
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

WIDTHS = [1180, 900, 760]

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

  const geom = async (tab, sel) => page.evaluate(([tab, sel]) => {
    const q = (s) => document.querySelector(s);
    const r = (el) => { if (!el) return null; const b = el.getBoundingClientRect();
      return { l: +b.left.toFixed(1), t: +b.top.toFixed(1), w: +b.width.toFixed(1),
               h: +b.height.toFixed(1), r: +b.right.toFixed(1) }; };
    const tabEl = q('#tab-' + tab);
    const cards = [...tabEl.querySelectorAll(':scope > .card')];
    const c0 = cards[0];
    const cs = c0 ? getComputedStyle(c0) : null;
    const head = c0 ? c0.querySelector(':scope > .card-head') : null;
    const h3 = head ? head.querySelector('h3') : null;
    const rows = c0 ? [...c0.querySelectorAll(':scope > .row')] : [];
    const firstField = tabEl.querySelector(sel) || tabEl.querySelector('.row.field, .row.check');
    const lbl = firstField ? firstField.querySelector('label') : null;
    const ctl = firstField ? firstField.querySelector('input, .inline, .slider-row, select') : null;
    return {
      tab: r(tabEl),
      cardCount: cards.length,
      card: r(c0),
      cardPad: cs ? { l: cs.paddingLeft, r: cs.paddingRight, t: cs.paddingTop, b: cs.paddingBottom } : null,
      cardRadius: cs ? cs.borderRadius : null,
      cardBorder: cs ? cs.borderTopWidth + ' ' + cs.borderTopColor : null,
      cardBg: cs ? cs.backgroundColor : null,
      hasHead: !!head,
      head: r(head),
      fold: head ? r(head.querySelector('.card-fold')) : null,
      h3: r(h3),
      h3Text: h3 ? h3.textContent.trim() : null,
      h3Size: h3 ? getComputedStyle(h3).fontSize + '/' + getComputedStyle(h3).fontWeight : null,
      rows: rows.map((x) => r(x)),
      label: r(lbl),
      labelText: lbl ? lbl.textContent.trim() : null,
      labelSize: lbl ? getComputedStyle(lbl).fontSize + '/' + getComputedStyle(lbl).color : null,
      ctl: r(ctl),
      ctlH: ctl ? getComputedStyle(ctl).height + ' ' + getComputedStyle(ctl).borderRadius : null,
      hints: [...tabEl.querySelectorAll(':scope > .card > .hint, :scope > .hint')].map((x) => r(x)),
    };
  }, [tab, sel]);

  const pages = [
    ['general', '#gen-behavior .row'],
    ['timing', '#short-card .row'],
    ['supervisor', '#sup-main .row'],
    ['shortcuts', '#sc-postpone'],
  ];
  const res = {};
  for (const [tab, sel] of pages) {
    await page.evaluate((t) => {
      [...document.querySelectorAll('.nav-item')].find((b) => b.dataset.tab === t).click();
    }, tab);
    await page.waitForTimeout(650);
    res[tab] = await geom(tab, sel);
    await page.evaluate(() => document.activeElement && document.activeElement.blur());
    await page.waitForTimeout(120);
    await page.screenshot({ path: out + '/sc_' + w + '_' + tab + '.png' });
  }
  // 撞车状态：两行用同一个组合键，看红框 + 错误行 + 说明三者在卡底怎么排
  await page.evaluate(() => {
    const a = document.getElementById('sc-postpone');
    const b = document.getElementById('sc-pause');
    a.value = b.value || 'Ctrl+Alt+S';
    a.classList.add('bad');
    const w = document.getElementById('sc-warn');
    w.textContent = '“推迟当前休息”和“暂停 / 恢复休息”用了同一个组合键（只有它生效）';
    w.classList.remove('hidden');
  });
  await page.waitForTimeout(300);
  await page.screenshot({ path: out + '/sc_' + w + '_shortcuts_warn.png' });
  // 深色只拍快捷键页
  await page.evaluate(() => document.documentElement.setAttribute('data-theme', 'dark'));
  await page.waitForTimeout(320);
  await page.evaluate(() => {
    [...document.querySelectorAll('.nav-item')].find((b) => b.dataset.tab === 'shortcuts').click();
  });
  await page.waitForTimeout(500);
  await page.screenshot({ path: out + '/sc_' + w + '_shortcuts_dark.png' });
  await page.evaluate(() => document.documentElement.setAttribute('data-theme', 'light'));
  await page.waitForTimeout(200);
  console.log(JSON.stringify({ w, res, errors: errs.slice(0, 5) }));
  await browser.close();
})().catch((e) => { console.error(e); process.exit(1); });
"""


def main():
    outdir = sys.argv[1] if len(sys.argv) > 1 else os.path.join(tempfile.gettempdir(), "wr_sc")
    os.makedirs(outdir, exist_ok=True)

    iso = os.path.join(tempfile.gettempdir(), "wr_sc_cfg")
    shutil.rmtree(iso, ignore_errors=True)
    os.makedirs(iso)
    cfg_path = os.path.join(iso, "settings.json")
    with open(FACTORY, encoding="utf-8") as f:
        cfg = json.load(f)
    cfg.setdefault("supervisor", {})["enabled"] = True
    cfg.setdefault("sound", {})["enabled"] = True
    with open(cfg_path, "w", encoding="utf-8") as f:
        json.dump(cfg, f, ensure_ascii=False, indent=2)

    port = free_port(29781)
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

        js_path = os.path.join(HERE, "_tmp_shot_sc.js")
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
                print(r.stdout.strip() or r.stderr.strip()[-1500:])
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
