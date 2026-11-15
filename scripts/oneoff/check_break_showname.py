#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""一次性验证：提醒窗口的竖排名牌受 geom.showName 控制。

同 shot_break_stage2.py 的路子（无头服务 + 替身 shim 喂会话数据），只是把同一份
会话跑两遍：一遍 geom.showName = true，一遍 false，对比 #dialog-name 的状态。

要验的就是「设置页那个复选框到底管不管真机」：
  - true  → 牌子可见、有文字、位置照旧
  - false → 牌子 display:none（不是透明度 0、不是压在画面外），立绘与对话框不动

产物：<输出目录>/break_name_on.png、break_name_off.png
"""

import base64
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
ROLE_DIR = os.path.join(ROOT, "assets", "supervisors", "医生")

VIEW_W, VIEW_H = 960, 600

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


def role_payload(show_name):
    meta = json.load(open(os.path.join(ROLE_DIR, "meta.json"), encoding="utf-8"))
    img = os.path.join(ROLE_DIR, meta.get("image") or "figure.webp")
    ext = os.path.splitext(img)[1].lower().lstrip(".")
    mime = {"webp": "image/webp", "png": "image/png", "jpg": "image/jpeg",
            "jpeg": "image/jpeg", "gif": "image/gif"}.get(ext, "image/webp")
    b64 = base64.b64encode(open(img, "rb").read()).decode()
    items = [i for i in meta["messages"]["items"] if i.get("enabled", True)]
    geom = dict(meta.get("geom") or {})
    geom["showName"] = show_name
    return {
        "image": "data:%s;base64,%s" % (mime, b64),
        "roleName": meta.get("name", ""),
        "name": meta.get("name", ""),
        "gaze": True,
        "geom": geom,
        "lines": {"urge": items[0]["text"], "command": items[-1]["text"]},
    }


SHIM_TPL = """window.api = {
  breakCurrent: async function () { return (%(data)s); },
  action: function () {},
  closeWindow: function () {},
  on: function () {},
  off: function () {},
  supActivity: async function () {},
  onSupActivity: function (cb) { window.__WR_SUP_FIRE = cb; }
};
"""

SHOT = r"""
const fs = require('fs');
const { chromium } = require('playwright-core');

async function probe(page, shim, outPath) {
  await page.route('**/shim.js', (r) => r.fulfill({
    status: 200, contentType: 'application/javascript', body: fs.readFileSync(shim, 'utf-8') }));
  await page.route('**/favicon.ico', (r) => r.fulfill({ status: 204, body: '' }));
  const errs = [];
  page.on('pageerror', (e) => errs.push(String((e && e.message) || e)));
  await page.goto(process.env.WR_BASE + '/break.html?win=break', { waitUntil: 'load' });
  await page.waitForFunction(() => {
    const t = document.getElementById('figure-trim');
    const img = document.getElementById('figure-img');
    return !!t && getComputedStyle(t).visibility === 'visible'
      && !!img && img.complete && img.naturalWidth > 0;
  }, null, { timeout: 15000 });
  await page.waitForTimeout(900);
  const st = await page.evaluate(() => {
    const n = document.getElementById('dialog-name');
    const b = n ? n.getBoundingClientRect() : null;
    const cs = n ? getComputedStyle(n) : null;
    const fig = document.getElementById('figure');
    const fb = fig ? fig.getBoundingClientRect() : null;
    return {
      text: n ? (n.textContent || '').trim() : null,
      cls: n ? n.className : null,
      display: cs ? cs.display : null,
      visibility: cs ? cs.visibility : null,
      rect: b ? { w: +b.width.toFixed(1), h: +b.height.toFixed(1), x: +b.x.toFixed(1) } : null,
      figure: fb ? { w: +fb.width.toFixed(1), h: +fb.height.toFixed(1) } : null,
    };
  });
  await page.screenshot({ path: outPath });
  return { ...st, errors: errs.slice(0, 4) };
}

(async () => {
  const browser = await chromium.launch({ channel: 'msedge', headless: true,
    args: ['--force-device-scale-factor=1'] });
  const mk = () => browser.newPage({ viewport: { width: Number(process.env.WR_W),
    height: Number(process.env.WR_H) }, deviceScaleFactor: 1 });
  const onPage = await mk();
  const offPage = await mk();
  const on = await probe(onPage, process.env.WR_SHIM_ON, process.env.WR_OUT + '/break_name_on.png');
  const off = await probe(offPage, process.env.WR_SHIM_OFF, process.env.WR_OUT + '/break_name_off.png');
  console.log(JSON.stringify({ on, off }));
  await browser.close();
})().catch((e) => { console.error(e); process.exit(1); });
"""


def main():
    outdir = sys.argv[1] if len(sys.argv) > 1 else os.path.join(tempfile.gettempdir(), "wr_showname")
    os.makedirs(outdir, exist_ok=True)

    iso = os.path.join(tempfile.gettempdir(), "wr_showname_iso")
    shutil.rmtree(iso, ignore_errors=True)
    os.makedirs(iso)
    cfg_path = os.path.join(iso, "settings.json")
    shutil.copy(FACTORY, cfg_path)

    shim_paths = {}
    for flag, name in ((True, "on"), (False, "off")):
        data = {
            "id": "check-showname-" + name, "type": "short", "name": "短休息",
            "endsAt": int(time.time() * 1000) + 287000, "durationMs": 300000,
            "preview": False,
            "appearance": {"color": "#4f7cff", "transparency": 0.05, "fullscreen": False},
            "messages": ["起来走动两分钟，让眼睛离开屏幕。"],
            "endEarlyShortcut": "Ctrl+Alt+X",
            "supervisor": role_payload(flag),
        }
        p = os.path.join(iso, "shim_%s.js" % name)
        with open(p, "w", encoding="utf-8") as f:
            f.write(SHIM_TPL % {"data": json.dumps(data, ensure_ascii=False)})
        shim_paths[name] = p

    port = free_port(29767)
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

        js_path = os.path.join(HERE, "_tmp_check_showname.js")
        with open(js_path, "w", encoding="utf-8") as f:
            f.write(SHOT)
        penv = dict(os.environ)
        penv.update({"WR_BASE": base, "WR_OUT": outdir, "WR_W": str(VIEW_W),
                     "WR_H": str(VIEW_H), "WR_SHIM_ON": shim_paths["on"],
                     "WR_SHIM_OFF": shim_paths["off"]})
        node = shutil.which("node")
        try:
            r = subprocess.run([node, js_path], cwd=os.path.join(ROOT, "scripts"), env=penv,
                               text=True, encoding="utf-8", errors="replace",
                               capture_output=True, timeout=180)
        finally:
            if os.path.exists(js_path):
                os.remove(js_path)
        if r.returncode != 0:
            print(r.stdout.strip() or r.stderr.strip()[-1200:])
            return 1

        res = json.loads(r.stdout.strip().splitlines()[-1])
        on, off = res["on"], res["off"]
        fails = 0

        def check(cond, msg, extra=""):
            nonlocal fails
            if not cond:
                fails += 1
            print(("  ok   " if cond else "  FAIL ") + msg + ("  | " + str(extra) if extra else ""))

        print("showName=true :", json.dumps(on, ensure_ascii=False))
        print("showName=false:", json.dumps(off, ensure_ascii=False))
        check(on["display"] != "none" and on["rect"] and on["rect"]["w"] > 0,
              "showName=true 时名字牌可见", on["display"])
        check(bool(on["text"]), "showName=true 时有名字文字", on["text"])
        check(off["display"] == "none", "showName=false 时名字牌 display:none", off["display"])
        check(off["rect"] and off["rect"]["w"] == 0, "showName=false 时占位为 0（不残留空位）", off["rect"])
        check(on["figure"] == off["figure"], "两种状态下立绘盒尺寸一致（开关不影响版式）",
              "%s vs %s" % (on["figure"], off["figure"]))
        check(not on["errors"] and not off["errors"], "两次渲染都没有页面报错",
              on["errors"] + off["errors"])
        print("\n结果：%d 项不合格" % fails)
        print("截图：" + os.path.join(outdir, "break_name_on.png"))
        print("      " + os.path.join(outdir, "break_name_off.png"))
        return 1 if fails else 0
    finally:
        if proc.poll() is None:
            proc.terminate()
            try:
                proc.wait(timeout=5)
            except Exception:
                proc.kill()
        log.close()
        shutil.rmtree(iso, ignore_errors=True)


if __name__ == "__main__":
    sys.exit(main())
