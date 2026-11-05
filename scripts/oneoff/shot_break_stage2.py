#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""一次性截图：短休息提醒窗口 **第二阶段**（督休专员凑近、红色告警底、命令台词）。

用法：python scripts/oneoff/shot_break_stage2.py [输出目录]
产物：<输出目录>/break_stage2_raw.png（原图）、break_stage2.webp / .jpg（压缩件）

为什么走无头服务 + Playwright 而不是屏幕抓图：
  1. 提醒窗口带 WDA_EXCLUDEFROMCAPTURE，任何走官方捕获通路的截图都拍不到它
     （见 README「副作用：想截图也拍不到」）；
  2. 真窗口的尺寸受屏幕工作区约束，做出来的图比需要的更大。
  无头服务渲染的是同一份 HTML/CSS/JS，只是数据由替身 shim 喂进来，
  所以画面与真窗口逐像素同源，尺寸还能自己定。

二阶段怎么进：break.js 的 noteActivity 在 stage1 累计权重到 3 就升到 stage2。
页面对外只暴露全局键鼠心跳（onSupActivity），这里把它存下来再触发一次
（权重 +3），等价于「用户在任意应用里敲了一下键」——真实路径，不是改 DOM 硬造。
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
EXE = os.path.join(ROOT, "build", "bin", "chibingren_server.exe")
FACTORY = os.path.join(ROOT, "internal", "settings", "default_settings.json")
ROLE_DIR = os.path.join(ROOT, "assets", "supervisors", "医生")

# 截图尺寸：默认尺寸的 1/2 左右。窗口越小，压缩后越省字节，而版式与真窗口一致
# （布局全是 vh / vw / 百分比，没有断点，缩小不改变结构）。
VIEW_W, VIEW_H = 960, 600

# 二阶段的台词取自角色自己的台词池（meta.json 的 messages.items），不自己编。
COMMAND_ID = "5"

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


def role_payload():
    """把磁盘上的医生专员读成 shim 要喂的那份 supervisor 数据。"""
    meta = json.load(open(os.path.join(ROLE_DIR, "meta.json"), encoding="utf-8"))
    img = os.path.join(ROLE_DIR, meta.get("image") or "figure.webp")
    ext = os.path.splitext(img)[1].lower().lstrip(".")
    mime = {"webp": "image/webp", "png": "image/png", "jpg": "image/jpeg",
            "jpeg": "image/jpeg", "gif": "image/gif"}.get(ext, "image/webp")
    b64 = base64.b64encode(open(img, "rb").read()).decode()
    items = [i for i in meta["messages"]["items"] if i.get("enabled", True)]
    command = next((i["text"] for i in items if i["id"] == COMMAND_ID), items[-1]["text"])
    return {
        "image": "data:%s;base64,%s" % (mime, b64),
        "roleName": meta.get("name", ""),
        "name": meta.get("name", ""),
        "gaze": True,
        "geom": meta.get("geom"),
        "lines": {"urge": items[0]["text"], "command": command},
    }


SHIM_TPL = r"""// 替身 shim：只实现休息窗口用到的那几个方法（breakCurrent / action /
// closeWindow / supActivity / onSupActivity）。真实运行时这几样由 Wails 绑定提供。
window.__WR_SUP_FIRE = null;
window.api = {
  breakCurrent: async function () { return (
    %(data)s
  ); },
  action: function () {},
  closeWindow: function () {},
  on: function () {},
  off: function () {},
  supActivity: async function () {},
  // 全局键鼠心跳的注册口：真运行时由 Go 轮询 GetLastInputInfo 后广播，
  // 这里把它存下来，截图脚本自己触发一次 → 二阶段。
  onSupActivity: function (cb) { window.__WR_SUP_FIRE = cb; }
};
"""

SHOT = r"""
const fs = require('fs');
const { chromium } = require('playwright-core');
(async () => {
  const base = process.env.WR_BASE;
  const out = process.env.WR_OUT;
  const w = Number(process.env.WR_W), h = Number(process.env.WR_H);
  const shim = fs.readFileSync(process.env.WR_SHIM, 'utf-8');
  const browser = await chromium.launch({ channel: 'msedge', headless: true,
    args: ['--force-device-scale-factor=1'] });
  const page = await browser.newPage({ viewport: { width: w, height: h },
    deviceScaleFactor: 1 });
  const errs = [];
  page.on('pageerror', (e) => errs.push(String(e && e.message || e)));
  page.on('console', (m) => { if (m.type() === 'error') errs.push(m.text()); });
  // 替身 shim 必须在 goto 之前挂上
  await page.route('**/shim.js', (r) => r.fulfill({
    status: 200, contentType: 'application/javascript', body: shim }));
  await page.route('**/favicon.ico', (r) => r.fulfill({ status: 204, body: '' }));
  await page.goto(base + '/break.html?win=break', { waitUntil: 'load' });
  // 立绘解码 + 透明边距测量完成后 .trim 才可见（figureTrim），此时才是「画好了」
  await page.waitForFunction(() => {
    const t = document.getElementById('figure-trim');
    const img = document.getElementById('figure-img');
    return !!t && getComputedStyle(t).visibility === 'visible'
      && !!img && img.complete && img.naturalWidth > 0;
  }, null, { timeout: 15000 });
  await page.waitForTimeout(1200);

  const before = await page.evaluate(() => document.getElementById('app').dataset.stage);
  // 触发一次全局键鼠心跳（权重 +3）→ 二阶段
  await page.evaluate(() => { window.__WR_SUP_FIRE && window.__WR_SUP_FIRE(); });
  await page.waitForFunction(() => document.getElementById('app').dataset.stage === '2',
    null, { timeout: 5000 });
  // 脉动周期 1.1s，取波峰附近（立绘最大、轮廓光最亮）再拍
  await page.waitForTimeout(620);

  const info = await page.evaluate(() => {
    const app = document.getElementById('app');
    const line = document.getElementById('supervisor-line');
    const img = document.getElementById('figure-img');
    return {
      stage: app.dataset.stage,
      line: line ? line.textContent : null,
      speaker: line ? (line.dataset.speaker || null) : null,
      plate: (document.getElementById('dialog-name') || {}).textContent || null,
      figure: img ? { w: img.clientWidth, h: img.clientHeight } : null,
      pct: (document.getElementById('progress-pct') || {}).textContent || null,
    };
  });
  await page.screenshot({ path: out + '/break_stage2_raw.png' });
  console.log(JSON.stringify({ before, ...info, errors: errs.slice(0, 5) }));
  await browser.close();
})().catch((e) => { console.error(e); process.exit(1); });
"""


def compress(raw, outdir):
    """压到尽可能小，同时不让观感掉档。

    这图只有一块纯色渐变底 + 一张立绘 + 几个字，最适合的是有损 WebP：
    相比 JPEG 同视觉质量下再省 25%~35%，而且没有 JPEG 在渐变上的色块（
    这张图的背景正是一整片蓝紫渐变，JPEG 低质量下会出现明显 banding）。
    JPEG 也出一份作兜底，最终留哪个看字节数。
    """
    from PIL import Image

    im = Image.open(raw).convert("RGB")
    res = {}
    # q82 是逐档试出来的：74 / 78 / 82 / 86 / 90 五档的字节数是 21.3 / 24.0 / 27.5 /
    # 33.0 / 39.6 KB，82 往后体积涨得快、观感已经看不出差别，往前开始丢立绘发丝细节。
    for name, kw in (
        ("break_stage2.webp", dict(format="WEBP", quality=82, method=6)),
        ("break_stage2.jpg", dict(format="JPEG", quality=86, optimize=True,
                                  progressive=True, subsampling=0)),
    ):
        p = os.path.join(outdir, name)
        im.save(p, **kw)
        res[name] = os.path.getsize(p)
    return res


def main():
    outdir = sys.argv[1] if len(sys.argv) > 1 else os.path.join(tempfile.gettempdir(), "wr_shot")
    os.makedirs(outdir, exist_ok=True)

    iso = os.path.join(tempfile.gettempdir(), "wr_shot_stage2")
    shutil.rmtree(iso, ignore_errors=True)
    os.makedirs(iso)
    cfg_path = os.path.join(iso, "settings.json")
    shutil.copy(FACTORY, cfg_path)

    sup = role_payload()
    data = {
        "id": "shot-stage2", "type": "short", "name": "短休息",
        "endsAt": int(time.time() * 1000) + 287000, "durationMs": 300000,
        "preview": False,
        "appearance": {"color": "#4f7cff", "transparency": 0.05, "fullscreen": False},
        # stage1 的提醒内容（真实短休息窗的第一句就是它）
        "messages": ["起来走动两分钟，让眼睛离开屏幕。"],
        "endEarlyShortcut": "Ctrl+Alt+X",
        "supervisor": sup,
    }
    shim_path = os.path.join(iso, "shim_stub.js")
    with open(shim_path, "w", encoding="utf-8") as f:
        f.write(SHIM_TPL % {"data": json.dumps(data, ensure_ascii=False)})

    port = free_port(29761)
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

        # 必须在 scripts/ 下跑：playwright-core 装在 scripts/node_modules
        js_path = os.path.join(HERE, "_tmp_shot_stage2.js")
        with open(js_path, "w", encoding="utf-8") as f:
            f.write(SHOT)
        penv = dict(os.environ)
        penv["WR_BASE"] = base
        penv["WR_OUT"] = outdir
        penv["WR_W"] = str(VIEW_W)
        penv["WR_H"] = str(VIEW_H)
        penv["WR_SHIM"] = shim_path
        node = shutil.which("node")
        try:
            r = subprocess.run([node, js_path], cwd=os.path.join(ROOT, "scripts"), env=penv,
                               text=True, encoding="utf-8", errors="replace",
                               capture_output=True, timeout=180)
        finally:
            if os.path.exists(js_path):
                os.remove(js_path)
        print(r.stdout.strip() or r.stderr.strip()[-1200:])
        if r.returncode != 0:
            return 1

        raw = os.path.join(outdir, "break_stage2_raw.png")
        if not os.path.exists(raw):
            print("没生成截图", file=sys.stderr)
            return 1
        sizes = compress(raw, outdir)
        print("[raw png] %d bytes" % os.path.getsize(raw))
        for k, v in sizes.items():
            print("[%s] %d bytes" % (k, v))
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
