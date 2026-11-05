#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""GUI 端到端验证：设置窗口的初始尺寸、拖拽记录、二次启动沿用（真窗口，不是无头）。

无头冒烟验不到这一层：`-tags server` 根本不建窗口，配置有没有真的接到
WebviewWindowOptions 上只有真跑一次才看得见。这里用 Win32 读窗口矩形 + DPI 折成 DIP，
与配置里的数字比对。

三段：
  ① 首次启动（出厂默认）→ 与 default_settings.json 的 window.width/height 一致
  ② 拖成「默认 −120 / −100」→ 等防抖 → settings.json 里 lastWidth/lastHeight 记下来
  ③ 重启 → 沿用上次尺寸；把 rememberSize 改成 false 再重启 → 回到出厂尺寸

出厂尺寸不写死：从 default_settings.json 现读，改配置即改断言。

隔离：WR_SETTINGS_PATH 指向 %TEMP% 的副本，不碰真实配置。窗口矩形包含无边框窗口的
不可见拖动边框（约 8px），所以断言留 ±20 DIP 容差 —— 这里要验的是「配置生效了没」，
不是逐像素对齐。
"""
import ctypes
import ctypes.wintypes as wt
import json
import os
import shutil
import subprocess
import sys
import time

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(os.path.dirname(HERE))
EXE = os.path.join(ROOT, "build", "bin", "chibingren_winprobe.exe")
TEMP = os.environ.get("TEMP", ROOT)
ISO = os.path.join(TEMP, "wr_winsize_gui_iso")
LOG = os.path.join(TEMP, "wr_winsize_gui.log")
TOL = 24  # DIP

user32 = ctypes.windll.user32
dwmapi = ctypes.windll.dwmapi
user32.SetProcessDPIAware()

failures = []
checks = 0


def check(ok, label, detail=""):
    global checks
    checks += 1
    print(("  ok   " if ok else "  FAIL ") + label + (("  | " + str(detail)) if detail else ""))
    if not ok:
        failures.append(label + (("  | " + str(detail)) if detail else ""))
    return ok


def windows_of(pid):
    found = []

    @ctypes.WINFUNCTYPE(wt.BOOL, wt.HWND, wt.LPARAM)
    def cb(hwnd, _):
        p = wt.DWORD()
        user32.GetWindowThreadProcessId(hwnd, ctypes.byref(p))
        if p.value == pid and user32.IsWindowVisible(hwnd):
            n = user32.GetWindowTextLengthW(hwnd)
            if n > 0:
                found.append(hwnd)
        return True

    user32.EnumWindows(cb, 0)
    return found


def visible_rect(hwnd):
    """窗口的可见矩形（物理像素）：DWM 扩展边框扣掉无边框窗口的不可见拖动带。"""
    r = wt.RECT()
    hr = dwmapi.DwmGetWindowAttribute(hwnd, 9, ctypes.byref(r), ctypes.sizeof(r))  # DWMWA_EXTENDED_FRAME_BOUNDS
    if hr != 0:
        user32.GetWindowRect(hwnd, ctypes.byref(r))
    return r.left, r.top, r.right - r.left, r.bottom - r.top


def dip_size(hwnd):
    _, _, w, h = visible_rect(hwnd)
    dpi = user32.GetDpiForWindow(hwnd) or 96
    return round(w * 96 / dpi), round(h * 96 / dpi), dpi


def client_dip(hwnd):
    """客户区尺寸折成 DIP。

    这才是网页真正拿到的视口：无边框窗口靠 WM_NCCALCSIZE 把客户区撑满整个窗口，
    所以它应当**精确等于**配置里的 Width/Height。若小了一截，说明有不可见边框吃了
    客户区，CSS 视口就比配置窄，而「关于页恰好不出滚动条 = 857」这类实测数字会失效。
    """
    r = wt.RECT()
    user32.GetClientRect(hwnd, ctypes.byref(r))
    dpi = user32.GetDpiForWindow(hwnd) or 96
    return (round((r.right - r.left) * 96 / dpi), round((r.bottom - r.top) * 96 / dpi), dpi)


def wait_window(pid, timeout=25.0):
    t0 = time.time()
    while time.time() - t0 < timeout:
        ws = windows_of(pid)
        if ws:
            time.sleep(1.2)  # 等窗口定位 / 首帧
            return max(ws, key=lambda h: visible_rect(h)[2] * visible_rect(h)[3])
        time.sleep(0.3)
    return None


def start(cfg_path):
    env = dict(os.environ)
    env["WR_SETTINGS_PATH"] = cfg_path
    env["WR_LOG_FILE"] = LOG
    logf = open(LOG, "a", encoding="utf-8")
    return subprocess.Popen([EXE], cwd=ROOT, env=env, stdout=logf, stderr=subprocess.STDOUT)


def kill(proc):
    subprocess.run(["taskkill", "/PID", str(proc.pid), "/T", "/F"],
                   capture_output=True)
    try:
        proc.wait(timeout=10)
    except Exception:
        pass


def read_cfg(path):
    with open(path, "r", encoding="utf-8") as f:
        return json.load(f)


def main():
    try:
        sys.stdout.reconfigure(encoding="utf-8", errors="replace")
    except Exception:
        pass

    if os.environ.get("WR_NO_BUILD") != "1":
        print("构建 GUI 产物（带控制台，便于看日志）…")
        b = subprocess.run(
            ["go", "build", "-o", EXE, "-ldflags", "-s -w", "./cmd/workerrest"],
            cwd=ROOT, capture_output=True, text=True, encoding="utf-8", errors="replace",
        )
        if b.returncode != 0:
            print("构建失败:\n" + (b.stderr or b.stdout))
            return 2

    shutil.rmtree(ISO, ignore_errors=True)
    os.makedirs(ISO, exist_ok=True)
    cfg_path = os.path.join(ISO, "settings.json")
    with open(os.path.join(ROOT, "internal", "settings", "default_settings.json"), "r", encoding="utf-8") as f:
        defaults = json.load(f)
    with open(cfg_path, "w", encoding="utf-8") as f:
        json.dump(defaults, f, ensure_ascii=False, indent=2)
    if os.path.exists(LOG):
        os.remove(LOG)

    # 出厂尺寸从配置现读（不写死）：用户改默认尺寸后本脚本无需改动。
    # 拖拽目标刻意与默认值拉开 ≥100 DIP（大于 TOL），否则「沿用上次」与「用了默认」
    # 在容差内无法区分，测试会退化成假绿。
    wd = defaults.get("window") or {}
    DW, DH = int(wd.get("width") or 0), int(wd.get("height") or 0)
    if DW <= 0 or DH <= 0:
        print("default_settings.json 缺 window.width / height")
        return 2
    TW, TH = DW - 120, DH - 100
    print("出厂尺寸 %d×%d，拖拽目标 %d×%d" % (DW, DH, TW, TH))

    # ---------- ① 首次启动 = 出厂默认尺寸 ----------
    print("① 首次启动（出厂默认 %d×%d）…" % (DW, DH))
    proc = start(cfg_path)
    hwnd = wait_window(proc.pid)
    if not hwnd:
        kill(proc)
        print("没有出现窗口；日志：\n" + open(LOG, encoding="utf-8", errors="replace").read()[-2000:])
        return 1
    w1, h1, dpi = dip_size(hwnd)
    check(abs(w1 - DW) <= TOL, "首次启动宽度 = %d DIP" % DW, "%d DIP（dpi=%d）" % (w1, dpi))
    check(abs(h1 - DH) <= TOL, "首次启动高度 = %d DIP" % DH, "%d DIP" % h1)
    # 客户区 = 网页视口。窗口矩形带不可见拖动边框（所以上面留 ±24 容差），客户区不该带：
    # 这一条把「CSS 视口 == 配置尺寸」钉死，整个 DPI 缩放结论都建立在它上面。
    cw, ch, _ = client_dip(hwnd)
    check(abs(cw - DW) <= 2, "客户区宽度 = 网页视口 = %d CSS px" % DW, "%d" % cw)
    check(abs(ch - DH) <= 2, "客户区高度 = 网页视口 = %d CSS px" % DH, "%d" % ch)

    # ---------- ② 拖成 TW×TH → 落盘 ----------
    print("② 把窗口改成 %d×%d，等防抖落盘…" % (TW, TH))
    left, top, _, _ = visible_rect(hwnd)
    user32.MoveWindow(hwnd, left, top, round(TW * dpi / 96), round(TH * dpi / 96), True)
    time.sleep(2.4)  # 防抖 1s + 写盘
    cfg = read_cfg(cfg_path)
    win = cfg.get("window") or {}
    lw, lh = win.get("lastWidth"), win.get("lastHeight")
    check(lw is not None and abs(int(lw) - TW) <= TOL, "记录到 lastWidth ≈ %d" % TW, lw)
    check(lh is not None and abs(int(lh) - TH) <= TOL, "记录到 lastHeight ≈ %d" % TH, lh)
    check(win.get("width") == DW and win.get("height") == DH,
          "默认尺寸没被顶掉", "%sx%s" % (win.get("width"), win.get("height")))
    kill(proc)

    # ---------- ③ 重启沿用；关掉开关回默认 ----------
    print("③ 重启（rememberSize=true，应沿用 %d×%d）…" % (TW, TH))
    proc = start(cfg_path)
    hwnd = wait_window(proc.pid)
    if not hwnd:
        kill(proc)
        print("第二次启动没有窗口")
        return 1
    w2, h2, _ = dip_size(hwnd)
    check(abs(w2 - TW) <= TOL, "重启后沿用上次宽度 %d" % TW, w2)
    check(abs(h2 - TH) <= TOL, "重启后沿用上次高度 %d" % TH, h2)
    kill(proc)

    print("④ 关掉「自动记录」再重启（应回到 %d×%d）…" % (DW, DH))
    cfg = read_cfg(cfg_path)
    cfg["window"]["rememberSize"] = False
    with open(cfg_path, "w", encoding="utf-8") as f:
        json.dump(cfg, f, ensure_ascii=False, indent=2)
    proc = start(cfg_path)
    hwnd = wait_window(proc.pid)
    if not hwnd:
        kill(proc)
        print("第三次启动没有窗口")
        return 1
    w3, h3, _ = dip_size(hwnd)
    check(abs(w3 - DW) <= TOL, "关掉开关后回到默认宽度 %d" % DW, w3)
    check(abs(h3 - DH) <= TOL, "关掉开关后回到默认高度 %d" % DH, h3)
    kill(proc)

    print()
    if failures:
        print("失败 %d / %d" % (len(failures), checks))
        for f in failures:
            print("  - " + f)
        return 1
    print("全部通过 %d / %d" % (checks, checks))
    return 0


if __name__ == "__main__":
    sys.exit(main())
