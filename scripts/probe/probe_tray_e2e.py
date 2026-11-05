#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""系统托盘端到端：真实 GUI 进程 → 托盘的宿主窗口 → 单击打开设置 / 右键弹出菜单。

托盘的文案逻辑（tooltip、菜单状态行、「约 X 分钟后」）有纯函数单测（tray_test.go），
但「点了到底有没有反应」永远只有真窗口能验。这里不去碰任务栏的像素，而是直接找到
Wails 给托盘建的那个**消息专属窗口**（HWND_MESSAGE 的子窗，EnumWindows 看不到它），
往里投递它自己注册的回调消息（WM_USER+1，lParam 带鼠标事件），看真实反应：

  A 对照组：不最小化启动 → 设置窗口一开始就可见
  B 主用例：startMinimized=true 启动 → 设置窗口不可见
            → 向托盘宿主投 WM_LBUTTONUP → 设置窗口出现（这就是「单击打开设置」）
            → 向托盘宿主投 WM_RBUTTONUP → 出现 #32768 菜单窗口（右键菜单真的弹了）

为什么消息号写死 WM_USER+1 与 WM_LBUTTONUP：这两个值来自 Wails v3.0.0
（pkg/application/systemtray_windows.go 的 wmUserSystray / wndProc），是框架契约。
升级 Wails 后这里若失败，先去看那个文件的常量，别急着改断言。

验不到的部分（需要人眼）：托盘图标是否真的出现在通知区、提示文案的字体与换行。
"""
import argparse
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
DEFAULTS = os.path.join(ROOT, "internal", "settings", "default_settings.json")
TEMP = os.environ.get("TEMP", ROOT)
ISO = os.path.join(TEMP, "wr_tray_e2e_iso")
LOG = os.path.join(TEMP, "wr_tray_e2e.log")

user32 = ctypes.windll.user32
user32.SetProcessDPIAware()

HWND_MESSAGE = wt.HWND(-3)
WM_USER = 0x0400
WM_USER_SYSTRAY = WM_USER + 1  # Wails: wmUserSystray
WM_LBUTTONUP = 0x0202
WM_RBUTTONUP = 0x0205
WND_CLASS = "WailsWebviewWindow"  # Wails: options.Windows.WndClass
APP_ID = "wails-app-com.goodgoodlive.workerrest"  # 单实例窗口类名 = <appid>-sic

failures = []
checks = 0


def check(ok, label, detail=""):
    global checks
    checks += 1
    print(("  ok   " if ok else "  FAIL ") + label + (("  | " + str(detail)) if detail else ""))
    if not ok:
        failures.append(label + (("  | " + str(detail)) if detail else ""))
    return ok


def title_of(hwnd):
    n = user32.GetWindowTextLengthW(hwnd)
    if n <= 0:
        return ""
    buf = ctypes.create_unicode_buffer(n + 1)
    user32.GetWindowTextW(hwnd, buf, n + 1)
    return buf.value


def class_of(hwnd):
    cls = ctypes.create_unicode_buffer(256)
    user32.GetClassNameW(hwnd, cls, 256)
    return cls.value


def proc_windows(pid, visible_only=False):
    out = []

    @ctypes.WINFUNCTYPE(wt.BOOL, wt.HWND, wt.LPARAM)
    def cb(hwnd, _):
        p = wt.DWORD()
        user32.GetWindowThreadProcessId(hwnd, ctypes.byref(p))
        if p.value == pid:
            if visible_only and not user32.IsWindowVisible(hwnd):
                return True
            out.append(hwnd)
        return True

    user32.EnumWindows(cb, 0)
    return out


def message_only_windows(pid):
    """托盘宿主窗口是 HWND_MESSAGE 的子窗。

    注意不能用 EnumChildWindows(HWND_MESSAGE)：消息专属窗口不会被它枚举出来
    （实测返回空数组，而 FindWindowEx 能逐个捞到）。这里按 FindWindowEx 的
    链式遍历走，再用进程号 + 类名筛。
    """
    out = []
    cur = 0
    while True:
        hwnd = user32.FindWindowExW(HWND_MESSAGE, wt.HWND(cur), None, None)
        if not hwnd:
            break
        cur = hwnd
        p = wt.DWORD()
        user32.GetWindowThreadProcessId(hwnd, ctypes.byref(p))
        if p.value == pid and class_of(hwnd) == WND_CLASS:
            out.append(hwnd)
    return out


def single_instance_windows(pid, app_id):
    """单实例哨兵窗口（类名 <appid>-sic）：它存在 = 重复启动会被它挡住。"""
    out = []
    cur = 0
    want = app_id + "-sic"
    while True:
        hwnd = user32.FindWindowExW(HWND_MESSAGE, wt.HWND(cur), None, None)
        if not hwnd:
            break
        cur = hwnd
        p = wt.DWORD()
        user32.GetWindowThreadProcessId(hwnd, ctypes.byref(p))
        if p.value == pid and class_of(hwnd) == want:
            out.append(hwnd)
    return out


def find_titled(pid, title, visible_only=False):
    for hwnd in proc_windows(pid, visible_only):
        if title_of(hwnd) == title:
            return hwnd
    return 0


def start(cfg_path):
    env = dict(os.environ)
    env["WR_SETTINGS_PATH"] = cfg_path
    env["WR_LOG_FILE"] = LOG
    logf = open(LOG, "a", encoding="utf-8")
    return subprocess.Popen([EXE], cwd=ROOT, env=env, stdout=logf, stderr=subprocess.STDOUT)


def kill(proc):
    subprocess.run(["taskkill", "/PID", str(proc.pid), "/T", "/F"], capture_output=True)
    try:
        proc.wait(timeout=10)
    except Exception:
        pass


def make_cfg(start_minimized):
    with open(DEFAULTS, encoding="utf-8") as f:
        cfg = json.load(f)
    cfg["startMinimized"] = start_minimized
    cfg["checkUpdateOnStart"] = False
    cfg["idlePauseMin"] = 600
    cfg["dndPause"] = False
    cfg["fullscreenPause"] = False
    cfg["castMirrorPause"] = False
    cfg["castWirelessPause"] = False
    cfg.setdefault("appPause", {})["enabled"] = False
    cfg.setdefault("sound", {})["enabled"] = False
    cfg["short"]["enabled"] = False
    cfg["long"]["enabled"] = False
    cfg["takeout"]["enabled"] = False
    os.makedirs(ISO, exist_ok=True)
    p = os.path.join(ISO, "settings.json")
    with open(p, "w", encoding="utf-8") as f:
        json.dump(cfg, f, ensure_ascii=False, indent=2)
    return p, cfg.get("appName") or "吃饼人也敢休息？"


def wait_for(fn, timeout=25.0, step=0.3):
    t0 = time.time()
    while time.time() - t0 < timeout:
        v = fn()
        if v:
            return v
        time.sleep(step)
    return None


def menu_windows(pid):
    """右键菜单是窗口类 #32768 的弹出窗口（TrackPopupMenu 建的）。"""
    return [h for h in proc_windows(pid) if class_of(h) == "#32768"]


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--no-build", action="store_true")
    args = ap.parse_args()
    try:
        sys.stdout.reconfigure(encoding="utf-8", errors="replace")
    except Exception:
        pass

    if not args.no_build:
        print("构建 GUI 产物（带控制台，便于看日志）…")
        b = subprocess.run(
            ["go", "build", "-o", EXE, "-ldflags", "-s -w", "./cmd/workerrest"],
            cwd=ROOT, capture_output=True, text=True, encoding="utf-8", errors="replace",
        )
        if b.returncode != 0:
            print("构建失败:\n" + (b.stderr or b.stdout))
            return 2
    elif not os.path.exists(EXE):
        print("找不到 %s" % EXE)
        return 2

    shutil.rmtree(ISO, ignore_errors=True)
    if os.path.exists(LOG):
        os.remove(LOG)

    # ---------- A 对照组：正常启动，设置窗口一开始就可见 ----------
    print("\n[A] 对照组：不最小化启动")
    cfg_path, app_name = make_cfg(False)
    proc = start(cfg_path)
    try:
        hwnd = wait_for(lambda: find_titled(proc.pid, app_name, visible_only=True))
        check(bool(hwnd), "A · 设置窗口一开始就可见", app_name)
        host = wait_for(lambda: message_only_windows(proc.pid))
        check(bool(host), "A · 托盘宿主窗口已建（HWND_MESSAGE 下的 %s）" % WND_CLASS,
              ("%d 个" % len(host)) if host else "没找到")
        sic = wait_for(lambda: single_instance_windows(proc.pid, APP_ID))
        check(bool(sic), "A · 单实例哨兵窗口在（重复启动会被挡）", APP_ID + "-sic")
    finally:
        kill(proc)

    # ---------- B 主用例：最小化启动 → 托盘单击唤出 ----------
    print("\n[B] startMinimized=true：设置窗口先隐藏，托盘单击唤出")
    cfg_path, app_name = make_cfg(True)
    proc = start(cfg_path)
    try:
        # 先确认窗口对象存在（只是不可见）——「不存在」和「藏着」是两回事
        hwnd = wait_for(lambda: find_titled(proc.pid, app_name))
        if not hwnd:
            check(False, "B · 设置窗口对象已创建", "25s 内没找到标题为 %s 的窗口" % app_name)
            return 1
        check(not user32.IsWindowVisible(hwnd), "B · 启动时设置窗口是隐藏的（startMinimized 生效）",
              "visible=%d" % user32.IsWindowVisible(hwnd))

        host = wait_for(lambda: message_only_windows(proc.pid))
        if not host:
            check(False, "B · 托盘宿主窗口已建")
            return 1
        tray = host[0]

        # 单击：Wails 的 wndProc 把 lParam&0xffff 当鼠标事件分发
        user32.PostMessageW(tray, WM_USER_SYSTRAY, 0, WM_LBUTTONUP)
        shown = wait_for(lambda: user32.IsWindowVisible(hwnd), timeout=6.0)
        check(bool(shown), "B · 托盘单击后设置窗口被唤出")

        # 右键：应当弹出菜单（窗口类 #32768）
        user32.PostMessageW(tray, WM_USER_SYSTRAY, 0, WM_RBUTTONUP)
        menus = wait_for(lambda: menu_windows(proc.pid), timeout=6.0)
        check(bool(menus), "B · 托盘右键弹出菜单",
              ("%d 个 #32768 窗口" % len(menus)) if menus else "6s 内没出现菜单窗口")
        # 关掉菜单：不然 TrackPopupMenu 占着 UI 线程，后面的断言与退出都会变慢
        if menus:
            user32.PostMessageW(menus[0], 0x001F, 0, 0)  # WM_CANCELMODE
            time.sleep(0.4)
    finally:
        kill(proc)

    print("\n结果：%d 项断言，失败 %d 项" % (checks, len(failures)))
    for f in failures:
        print("  - " + f)
    print("日志：%s" % LOG)
    return 0 if not failures else 1


if __name__ == "__main__":
    sys.exit(main())
