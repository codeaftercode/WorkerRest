#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""一次性：量设置页「卡片 → 窗口四边」的实际留白。

隔离口径同 probe_settings_ui.py（出厂配置副本 + WR_SETTINGS_PATH）。
输出一行 JSON：每页的 nav 右缘、卡片左/上/右/下与视口边的距离。
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


JS = """
() => {
  const cs = getComputedStyle(document.getElementById('content'));
  const c = document.getElementById('content').getBoundingClientRect();
  const nav = document.getElementById('nav');
  const navR = nav ? nav.getBoundingClientRect() : null;
  const tab = document.querySelector('.tab.active');
  const cards = tab ? [...tab.querySelectorAll(':scope > .card')] : [];
  const first = cards[0];
  const fr = first ? first.getBoundingClientRect() : null;
  const out = {
    vw: window.innerWidth, vh: window.innerHeight,
    pad: [cs.paddingTop, cs.paddingRight, cs.paddingBottom, cs.paddingLeft].join(' '),
    brd: cs.borderRightWidth + ' / ' + cs.borderBottomWidth,
    contentL: Math.round(c.left), contentR: Math.round(c.right),
    navR: navR ? Math.round(navR.right) : null,
    cardCount: cards.length,
  };
  if (fr) {
    out.card = {
      left: Math.round(fr.left), top: Math.round(fr.top),
      right: Math.round(fr.right), bottom: Math.round(fr.bottom),
      gapL: Math.round(fr.left - (navR ? navR.right : 0)),
      gapR: Math.round(window.innerWidth - fr.right),
      gapT: Math.round(fr.top),
      gapB: Math.round(window.innerHeight - fr.bottom),
      w: Math.round(fr.width),
    };
  }
  return out;
}
"""


def main():
    from playwright.sync_api import sync_playwright

    real_cfg = os.path.join(ROOT, "config", "settings.json")
    before = digest(real_cfg)
    iso = tempfile.mkdtemp(prefix="wr_measure_")
    iso_settings = os.path.join(iso, "settings.json")
    with open(FACTORY, encoding="utf-8") as f:
        cfg = json.load(f)
    cfg.setdefault("supervisor", {})["enabled"] = True
    with open(iso_settings, "w", encoding="utf-8") as f:
        json.dump(cfg, f, ensure_ascii=False, indent=2)

    port = free_port(29771)
    env = dict(os.environ)
    env["WAILS_SERVER_PORT"] = str(port)
    env["WAILS_SERVER_HOST"] = "127.0.0.1"
    env["WR_SETTINGS_PATH"] = iso_settings
    env.pop("ELECTRON_RUN_AS_NODE", None)
    base = "http://127.0.0.1:%d" % port
    logf = open(os.path.join(iso, "server.log"), "w", encoding="utf-8")
    proc = subprocess.Popen([EXE], cwd=ROOT, env=env, stdout=logf, stderr=subprocess.STDOUT)
    res = {}
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

        preload = os.path.join(ROOT, "scripts", "probe_preload.js")
        with sync_playwright() as p:
            b = p.chromium.launch(channel="msedge", headless=True)
            for w, h in [(874, 857), (1000, 820), (1280, 900)]:
                page = b.new_page(viewport={"width": w, "height": h})
                page.add_init_script(path=preload)
                page.goto(base + "/settings.html", wait_until="domcontentloaded")
                page.wait_for_function("typeof window.api === 'object' && window.api !== null")
                page.wait_for_timeout(500)
                for tab in ["general", "timing", "supervisor", "shortcuts", "about"]:
                    page.click('.nav-item[data-tab="%s"]' % tab)
                    page.wait_for_timeout(350)
                    res["%dx%d/%s" % (w, h, tab)] = page.evaluate(JS)
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

    if before != digest(real_cfg):
        print("!! 真实 config/settings.json 被改写")
        return 1
    print(json.dumps(res, ensure_ascii=False, indent=1))
    return 0


if __name__ == "__main__":
    sys.exit(main())
