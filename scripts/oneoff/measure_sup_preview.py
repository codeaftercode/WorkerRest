#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""一次性：量督休专员页预览面板与右列/卡片边框的对齐精度。

隔离口径同 shot_supervisor.py（出厂配置副本 + WR_SETTINGS_PATH）。
断言：预览面板左右两边与右列（.sup-stage）严丝合缝、右缘与卡片外框只差 1px 边框。
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

MEASURE = r"""
() => {
  const q = (s) => document.querySelector(s);
  const r = (el) => { const b = el.getBoundingClientRect(); return {x: +b.x.toFixed(2), y: +b.y.toFixed(2), w: +b.width.toFixed(2), h: +b.height.toFixed(2), right: +b.right.toFixed(2), bottom: +b.bottom.toFixed(2)}; };
  const grid = q('#sup-main .sup-grid');
  const gal = q('#sup-gallery-panel');
  const stage = q('.sup-stage');
  const pre = q('#sup-preview');
  const geom = q('#sup-geom');
  const msg = q('.sup-msg');
  const cs = getComputedStyle(grid);
  return {
    grid: r(grid), gal: r(gal), stage: r(stage), pre: r(pre), geom: r(geom), msg: r(msg),
    gridBorder: {l: parseFloat(cs.borderLeftWidth), r: parseFloat(cs.borderRightWidth), t: parseFloat(cs.borderTopWidth)},
    preMaxH: getComputedStyle(pre).maxHeight,
    ratio: +(pre.getBoundingClientRect().width / pre.getBoundingClientRect().height).toFixed(3),
  };
}
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

    iso = tempfile.mkdtemp(prefix="wr_geo_")
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

    fails = 0
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
            for w, h in [(760, 1000), (1180, 1000), (1280, 1000), (1440, 1060), (1680, 1100)]:
                page = b.new_page(viewport={"width": w, "height": h})
                page.add_init_script(path=preload)
                page.goto(base + "/settings.html", wait_until="domcontentloaded")
                page.wait_for_function("typeof window.api === 'object' && window.api !== null")
                page.wait_for_timeout(500)
                page.click('.nav-item[data-tab="supervisor"]')
                page.wait_for_timeout(600)
                m = page.evaluate(MEASURE)
                page.close()

                pre, stage, grid = m["pre"], m["stage"], m["grid"]
                # 右列内容区（去掉 1px 外框）
                inner_right = grid["right"] - m["gridBorder"]["r"]
                d_left = round(pre["x"] - stage["x"], 2)
                d_right = round(inner_right - pre["right"], 2)
                d_w = round(stage["w"] - pre["w"], 2)
                ok = abs(d_left) <= 0.5 and abs(d_right) <= 0.5 and abs(d_w) <= 0.5
                if not ok:
                    fails += 1
                print(f"视口 {w}x{h}: 预览 {pre['w']}x{pre['h']} 比例 {m['ratio']} "
                      f"| 左差 {d_left} 右差 {d_right} 宽差 {d_w} "
                      f"| max-height={m['preMaxH']} {'OK' if ok else '!! 不齐'}")
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
    if before != after:
        print("!! 真实 config/settings.json 被改写")
        fails += 1
    print(f"\n结果：{fails} 项不合格")
    return 1 if fails else 0


if __name__ == "__main__":
    sys.exit(main())
