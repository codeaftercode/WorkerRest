#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""缩放（DPI）对设置窗口布局的影响 —— 起无头服务 + 隔离配置，跑 probe_dpi_layout.js。

回答用户的问题：“设置窗口有默认尺寸，系统缩放会不会让样式出问题？关于页能不能显示全？”

三段：
  ① 同一视口换 devicePixelRatio（1 / 1.25 / 1.5 / 2 / 2.5）→ 布局指纹必须一致。
     这一条验的是“窗口尺寸是 DIP = CSS px”这个前提，不一致就说明某处写死了物理像素。
  ② 常见“分辨率 × 缩放”组合 → 按 fitSizeToScreen 复算真实窗口尺寸，量每页溢出、
     横向裁切、以及滚到底能不能看到最后一块。
  ③ 宽度下限（760）与常见宽度 → 横向裁切必须为 0。

只读：服务用隔离配置启动，不碰真实 config/settings.json。
"""
import json
import os
import shutil
import socket
import subprocess
import sys
import time
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(os.path.dirname(HERE))
EXE = os.path.join(ROOT, "build", "bin", "rest_server.exe")
PROBE = os.path.join(HERE, "probe_dpi_layout.js")
TEMP = os.environ.get("TEMP", ROOT)
ISO = os.path.join(TEMP, "wr_dpi_iso")
LOG = os.path.join(TEMP, "wr_dpi_server.log")

checks = 0
failures = []


def check(ok, label, detail=""):
    global checks
    checks += 1
    print(("  ok   " if ok else "  FAIL ") + label + (("  | " + str(detail)) if detail else ""))
    if not ok:
        failures.append(label + (("  | " + str(detail)) if detail else ""))
    return ok


def free_port(preferred):
    for cand in (preferred, 0):
        with socket.socket() as s:
            try:
                s.bind(("127.0.0.1", cand))
                return s.getsockname()[1]
            except OSError:
                continue
    raise SystemExit("没有可用端口")


def wait_up(base, proc, timeout=25.0):
    t0 = time.time()
    while time.time() - t0 < timeout:
        if proc.poll() is not None:
            return False
        try:
            with urllib.request.urlopen(base + "/", timeout=2) as r:
                if r.status == 200:
                    return True
        except Exception:
            pass
        time.sleep(0.4)
    return False


def section(tag, text):
    print("\n[%s] %s" % (tag, text))


def main():
    try:
        sys.stdout.reconfigure(encoding="utf-8", errors="replace")
        sys.stderr.reconfigure(encoding="utf-8", errors="replace")
    except Exception:
        pass

    node_cmd = shutil.which("node")
    if not node_cmd:
        print("找不到 node")
        return 2
    pw_dir = next((d for d in (os.path.join(ROOT, "scripts", "node_modules"),
                               os.path.join(ROOT, "node_modules"))
                   if os.path.isdir(os.path.join(d, "playwright"))), "")
    if not pw_dir:
        print("找不到 playwright")
        return 2

    if os.environ.get("WR_NO_BUILD") != "1":
        print("构建无头服务…")
        b = subprocess.run(
            ["go", "build", "-tags", "server", "-o", EXE, "-ldflags", "-s -w", "./cmd/workerrest"],
            cwd=ROOT, capture_output=True, text=True, encoding="utf-8", errors="replace",
        )
        if b.returncode != 0:
            print("构建失败:\n" + (b.stderr or b.stdout))
            return 2

    port = free_port(29761)
    base = "http://127.0.0.1:%d" % port

    shutil.rmtree(ISO, ignore_errors=True)
    os.makedirs(ISO, exist_ok=True)
    iso_settings = os.path.join(ISO, "settings.json")
    real = os.path.join(ROOT, "config", "settings.json")
    if os.path.exists(real):
        shutil.copyfile(real, iso_settings)
    else:
        open(iso_settings, "w", encoding="utf-8").write("{}")

    env = dict(os.environ)
    env["WAILS_SERVER_PORT"] = str(port)
    env["WAILS_SERVER_HOST"] = "127.0.0.1"
    env["WR_SETTINGS_PATH"] = iso_settings
    env.pop("ELECTRON_RUN_AS_NODE", None)

    logf = open(LOG, "w", encoding="utf-8")
    srv = subprocess.Popen([EXE], cwd=ROOT, env=env, stdout=logf, stderr=subprocess.STDOUT)
    try:
        if not wait_up(base, srv):
            print(open(LOG, encoding="utf-8", errors="replace").read()[-2000:])
            return 1
        penv = dict(os.environ)
        penv["WR_BASE"] = base
        r = subprocess.run([node_cmd, PROBE], cwd=ROOT, env=penv,
                           capture_output=True, text=True, timeout=900,
                           encoding="utf-8", errors="replace")
        out = r.stdout or ""
        if r.returncode != 0:
            print(out)
            print("--- stderr ---\n" + (r.stderr or "")[-3000:])
            return 1
    finally:
        if srv.poll() is None:
            srv.kill()

    data = {}
    for line in out.splitlines():
        for tag in ("DPR_FINGERPRINT", "CLAMP_MATRIX", "WIDTH_BOUNDS"):
            if line.startswith(tag + " "):
                data[tag] = json.loads(line[len(tag) + 1:])

    for tag in ("DPR_FINGERPRINT", "CLAMP_MATRIX", "WIDTH_BOUNDS"):
        if tag not in data:
            print("没拿到 %s 段的输出：\n%s" % (tag, out[-2000:]))
            return 1

    TABS = ["general", "timing", "supervisor", "stats", "shortcuts", "about"]
    CN = {"general": "常规", "timing": "时间", "supervisor": "督休",
          "stats": "统计", "shortcuts": "快捷键", "about": "关于"}

    # ---------- ① 缩放本身不改布局 ----------
    section("1", "同一视口（874×857 CSS px）换 5 档设备像素比：布局必须逐像素一致")
    fp = data["DPR_FINGERPRINT"]
    base_dpr = sorted(fp.keys(), key=float)[0]
    base_m = fp[base_dpr]
    print("      基线 dpr=%s 的关于页：内容区 %dpx 可视 / %dpx 需要，溢出 %dpx"
          % (base_dpr, base_m["about"]["contentClientH"], base_m["about"]["contentScrollH"],
             base_m["about"]["need"]))
    for tab in TABS:
        worst = 0
        worst_dpr = ""
        for dpr, per in fp.items():
            if dpr == base_dpr:
                continue
            d = abs(per[tab]["hSum"] - base_m[tab]["hSum"])
            d2 = abs(per[tab]["tabH"] - base_m[tab]["tabH"])
            if max(d, d2) > worst:
                worst = max(d, d2)
                worst_dpr = "%s(dpr=%s)" % (CN[tab], dpr)
        check(worst <= 1, "缩放不改“%s”页布局（元素高度之和差 ≤1px）" % CN[tab],
              "最大差 %dpx %s" % (worst, ("@ " + worst_dpr) if worst_dpr else ""))
    # 每档 dpr 的 CSS 视口都必须还是 874×857 —— 若某个 dpr 下 innerWidth 变了，
    # 说明窗口尺寸被当成物理像素用了
    for dpr, per in fp.items():
        vw = per["about"]["vw"]
        vh = per["about"]["vh"]
        check(vw == 874 and vh == 857, "dpr=%s 时 CSS 视口仍是 874×857" % dpr,
              "%d×%d" % (vw, vh))

    # 用户问的那一条：默认尺寸下关于页能不能显示全
    check(base_m["about"]["need"] <= 0, "默认 874×857 下关于页不出垂直滚动条（内容全在首屏）",
          "可视 %dpx / 需要 %dpx，溢出 %dpx" % (base_m["about"]["contentClientH"],
                                                base_m["about"]["contentScrollH"],
                                                base_m["about"]["need"]))
    print("      默认尺寸下各页的溢出量（0 = 一屏放得下）：")
    for tab in TABS:
        m = base_m[tab]
        print("        %-5s %s" % (CN[tab], "一屏放得下" if m["need"] <= 0
                                  else "需滚动 %dpx" % m["need"]))

    # ---------- ② 真实夹紧尺寸 ----------
    section("2", "常见“分辨率 × 缩放”组合：窗口被夹成多少、关于页够不够")
    rows = data["CLAMP_MATRIX"]
    print("      %-12s %-6s %-11s %-11s %-9s %s"
          % ("屏幕", "缩放", "工作区DIP", "窗口DIP", "关于页溢出", "结论"))
    clamped_need_scroll = 0
    for row in rows:
        ab = row["per"]["about"]
        need = ab["need"]
        tail = ab.get("tail") or {}
        if need > 0:
            clamped_need_scroll += 1
        flag = "不滚" if need <= 0 else "需滚动 %dpx" % need
        print("      %-12s %-6s %-11s %-11s %-9s %s"
              % (row["screen"], "%g" % row["scale"],
                 "%dx%d" % tuple(row["waDip"]), "%dx%d" % tuple(row["win"]),
                 need if need > 0 else 0, flag))

    # 关于页在每种夹出来的尺寸下：横向不能裁切；滚到底必须能看到最后一块
    bad_clip = [(r["screen"], r["scale"], r["win"], r["per"]["about"]["maxClip"],
                 r["per"]["about"]["clipSel"]) for r in rows if r["per"]["about"]["maxClip"] > 0]
    check(not bad_clip, "关于页在全部 25 种组合下都无横向裁切",
          bad_clip[:3] if bad_clip else "0 处")
    bad_tail = [(r["screen"], r["scale"], r["win"], r["per"]["about"]["tail"])
                for r in rows if not (r["per"]["about"].get("tail") or {}).get("ok", True)]
    check(not bad_tail, "关于页滚到底都能看到最后一块",
          bad_tail[:3] if bad_tail else "%d 种组合" % len(rows))
    # 其余页也不能横向裁切（统计页的数据表自带横向滚动，已在采集端排除）
    for tab in ("general", "timing", "supervisor", "shortcuts"):
        bad = [(r["screen"], r["scale"], r["per"][tab]["clipSel"])
               for r in rows if r["per"][tab]["maxClip"] > 0]
        check(not bad, "“%s”页在全部组合下都无横向裁切" % CN[tab],
              bad[:3] if bad else "0 处")

    # 窗口不能比工作区还高（否则底部落到屏幕/任务栏外）。2026-11-05 修的就是这一条：
    # 那时最小高度 600 兜底会把窗口顶出工作区，8/25 种组合红。
    over_screen = [(r["screen"], r["scale"], r["win"], r["waDip"])
                   for r in rows if r["win"][1] > r["waDip"][1]]
    check(not over_screen, "窗口高度不会超过工作区高度（否则底部跑到屏幕外）",
          over_screen if over_screen else "0 处")
    # 反向：被夹过的窗口不能夹到贴死工作区底边（要留 40 DIP 余量）。
    # 注意只在“确实被夹”时才成立 —— 默认 857 本来就比工作区矮的屏属于正常放得下。
    too_short = [(r["screen"], r["scale"], r["win"][1], r["waDip"][1])
                 for r in rows
                 if r["win"][1] != 857 and r["win"][1] < r["waDip"][1] - 40]
    check(not too_short, "被夹过的窗口仍留 40 DIP 余量（没贴死工作区底边）",
          too_short if too_short else "0 处")

    print("\n      关于页需要滚动的情形：%d / %d 种组合" % (clamped_need_scroll, len(rows)))
    need_list = sorted({(r["win"][1], r["per"]["about"]["need"]) for r in rows
                        if r["per"]["about"]["need"] > 0})
    for h, n in need_list:
        print("        窗口高 %d → 溢出 %dpx（需要 %dpx）" % (h, n, h + n))

    # ---------- ③ 宽度下限 ----------
    section("3", "宽度 760 / 800 / 874 / 913：横向裁切必须为 0")
    for row in data["WIDTH_BOUNDS"]:
        w = row["w"]
        worst = max((row["per"][t]["maxClip"], t) for t in TABS)
        check(worst[0] == 0, "宽度 %d 下六页都无横向裁切" % w,
              "最大 %dpx @ %s" % (worst[0], CN[worst[1]]))

    # ---------- 汇总 ----------
    print("\n结果：%d 项，失败 %d 项" % (checks, len(failures)))
    if failures:
        for f in failures:
            print("  ✗ " + f)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
