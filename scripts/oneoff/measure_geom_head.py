#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""一次性：量“立绘调节”标题行在多档视口下挤不挤。

隔离口径同 measure_sup_preview.py（出厂配置副本 + WR_SETTINGS_PATH）。
关心三件事：
  1. 一行装不装得下（head 的 scrollWidth 是否超过 clientWidth）；
  2. 右边距：恢复默认按钮右缘到 head 右内壁还剩多少（小于 6px 就是贴边了）；
  3. 标签没被压成两行（offsetHeight 与 lineHeight 对比）。
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
  const head = document.querySelector(".sup-geom-head");
  const cs = getComputedStyle(head);
  const r = (el) => { const b = el.getBoundingClientRect(); return {x: +b.x.toFixed(1), right: +b.right.toFixed(1), w: +b.width.toFixed(1), h: +b.height.toFixed(1)}; };
  const kids = [...head.children].map((el) => ({
    cls: el.className || el.tagName,
    txt: (el.textContent || "").trim().slice(0, 12),
    ...r(el),
    lineH: Math.round(parseFloat(getComputedStyle(el).lineHeight) || 0),
    display: getComputedStyle(el).display,
  }));
  const hb = head.getBoundingClientRect();
  const padR = parseFloat(cs.paddingRight);
  const last = kids[kids.length - 1];
  return {
    head: {w: +hb.width.toFixed(1), h: +hb.height.toFixed(1),
           clientW: head.clientWidth, scrollW: head.scrollWidth,
           padL: parseFloat(cs.paddingLeft), padR},
    gap: cs.gap,
    kids,
    // 恢复默认按钮右缘 → head 右内壁
    btnGapRight: +(head.getBoundingClientRect().right - padR - last.right).toFixed(1),
    summaryShown: kids.some((k) => k.cls.indexOf("adv-summary") >= 0 && k.display !== "none"),
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

    iso = tempfile.mkdtemp(prefix="wr_geomhead_")
    iso_settings = os.path.join(iso, "settings.json")
    with open(FACTORY, encoding="utf-8") as f:
        cfg = json.load(f)
    cfg.setdefault("supervisor", {})["enabled"] = True
    with open(iso_settings, "w", encoding="utf-8") as f:
        json.dump(cfg, f, ensure_ascii=False, indent=2)

    port = free_port(29773)
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
            for w, h in [(760, 1000), (900, 1000), (1020, 1000), (1180, 1000), (1280, 1000)]:
                page = b.new_page(viewport={"width": w, "height": h})
                page.add_init_script(path=preload)
                page.goto(base + "/settings.html", wait_until="domcontentloaded")
                page.wait_for_function("typeof window.api === 'object' && window.api !== null")
                page.wait_for_timeout(500)
                page.click('.nav-item[data-tab="supervisor"]')
                page.wait_for_timeout(600)
                m = page.evaluate(MEASURE)
                page.close()

                over = m["head"]["scrollW"] - m["head"]["clientW"]
                btn_gap = m["btnGapRight"]
                two_line = [k["txt"] for k in m["kids"]
                            if k["lineH"] > 0 and k["h"] > k["lineH"] * 1.6]
                # 按钮是 margin-left:auto，正常就贴 head 右内壁（差值 0）；
                # 负数 = 被挤出内壁（窄窗溢出）
                ok = over <= 0 and btn_gap >= 0 and not two_line
                if not ok:
                    fails += 1
                print(f"视口 {w}x{h}: head {m['head']['w']}x{m['head']['h']} "
                      f"溢出 {over} | 按钮右边距 {btn_gap} | gap {m['gap']} "
                      f"| 摘要 {'显示' if m['summaryShown'] else '隐藏'} "
                      f"{'OK' if ok else '!! ' + str(two_line)}")
                for k in m["kids"]:
                    print(f"    {k['txt'] or '(空)':<10} x={k['x']:<7} w={k['w']:<7} h={k['h']:<6} {k['cls'][:28]}")
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
