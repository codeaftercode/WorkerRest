#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""非独占全屏询问窗的端到端验证（真实 GUI 进程 + 真全屏窗口）。

这条链路里有两件事只有真窗口能验：

  1. **询问窗是不是真的弹出来了**。判据层（QUNS=2）+ 几何层（前台窗口铺满 rcMonitor）
     都有纯函数单测，但“这两层判完真的建了一扇窗”只有真进程能看。
  2. **它不能抢前台焦点**。询问窗带 WS_EX_NOACTIVATE，这是全项目唯一一扇“不激活”的窗：
     一旦激活，无边框游戏会被踢出全屏（而 QUNS 正是按“前台窗口铺满整屏”判定的），
     这一段全屏就自己结束了。这一条没有任何离线办法验证，只能在这里看
     GetForegroundWindow() 是不是还停在我们造的那扇全屏窗上。

做法：本脚本自己造一扇“盖满整屏的无边框 WS_POPUP 窗”当靶子（不用去开真的游戏），
再按配置跑八组（对应设置页「非独占全屏」三档）：

  A 对照组  fullscreenBusyMode=off（出厂默认）→ 12 秒内**不许**出现询问窗；
  B 主用例  fullscreenBusyMode=ask → 出现询问窗（标题「全屏询问」）、前台焦点没被抢走、
            **12 秒不答按「暂停」结案**（实测墙钟 11.94 秒，断言区间 [11.0, 15.5]）、
            窗自散、同一段观察 10 秒不许再弹；撤掉靶子再立一扇（新的一段）必须再问一次；
  C~F       四个按钮的答复（暂停 / 本次不暂停 / 今天不问 / 永不询问）：真实鼠标点击
            （系统级注入，走 WebView2 自己的命中与分发），回传值、落盘、本段不再问逐条验；
  G         pause 档（直接暂停，不问）；
  H         名单命中（「全屏时不暂停的应用」里的 exe 不问）。

用法：python scripts/probe/probe_fs_ask.py [--no-build] [--only B,C]
退出码 0 = 全部通过。跑完断言真实配置（config/settings.json）没被动过。
"""

import argparse
import ctypes
import ctypes.wintypes as wt
import hashlib
import json
import os
import shutil
import subprocess
import sys
import time

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(os.path.dirname(HERE))
EXE = os.path.join(ROOT, "build", "bin", "rest_fsask_probe.exe")
DEFAULTS = os.path.join(ROOT, "internal", "settings", "default_settings.json")
REAL_CFG = os.path.join(ROOT, "config", "settings.json")
TEMP = os.environ.get("TEMP", ROOT)
ISO = os.path.join(TEMP, "wr_fs_ask_iso")
LOG = os.path.join(TEMP, "wr_fs_ask.log")

user32 = ctypes.WinDLL("user32", use_last_error=True)
kernel32 = ctypes.WinDLL("kernel32", use_last_error=True)
gdi32 = ctypes.WinDLL("gdi32", use_last_error=True)

# 靶子的底色。原来是“什么都不画”的窗（class brush 为 NULL → 不擦背景），
# 底下是什么颜色完全看运气，于是没法用它判“询问窗的圆角外是不是真的透明”。
# 涂成一块纯色之后，圆角外那圈像素要么是这块蓝（透明生效）、要么是别的颜色（没生效）。
TARGET_RGB = (0, 120, 215)
TARGET_COLORREF = TARGET_RGB[0] | (TARGET_RGB[1] << 8) | TARGET_RGB[2] << 16

gdi32.CreateSolidBrush.argtypes = [wt.DWORD]
gdi32.CreateSolidBrush.restype = wt.HBRUSH
user32.GetDC.argtypes = [wt.HWND]
user32.GetDC.restype = wt.HDC
user32.ReleaseDC.argtypes = [wt.HWND, wt.HDC]
# GetPixel 在 gdi32（GetDC / ReleaseDC 在 user32）—— 放到 user32 上会在导入时就报
# “function 'GetPixel' not found”，整个探针一行都跑不起来。
gdi32.GetPixel.argtypes = [wt.HDC, ctypes.c_int, ctypes.c_int]
gdi32.GetPixel.restype = wt.DWORD

# 与被测程序同一口径：按监视器 DPI 取坐标，否则 GetMonitorInfo 与窗口矩形不同尺度
try:
    user32.SetProcessDpiAwarenessContext(ctypes.c_void_p(-4))  # PER_MONITOR_AWARE_V2
except Exception:
    user32.SetProcessDPIAware()

WS_POPUP = 0x80000000
WS_VISIBLE = 0x10000000
WS_EX_TOPMOST = 0x00000008
SW_SHOW = 5
MONITOR_DEFAULTTOPRIMARY = 1

ASK_TITLE = "全屏询问"
# 询问窗的超时是 12 秒（fs_ask.go 的 fsAskTimeoutMs）；多给 3 秒余量再看日志
ASK_TIMEOUT = 12.0
POLL = 0.3

# 四个按钮在窗客户区里的中心（DIP，相对客户区左上角）。
# 来自 Playwright 以 420×200 视口渲染 fsask.html 后 getBoundingClientRect 的实测值，
# 版面是 flex 等分 + 固定 32 高，不随内容变化 —— 写死它比运行期再猜一遍稳。
BTN_ORDER = ["pause", "skip", "today", "never"]
BTN_CX = [63.75, 161.25, 258.75, 356.25]
BTN_CY = 123.5

MOUSEEVENTF_LEFTDOWN = 0x0002
MOUSEEVENTF_LEFTUP = 0x0004

failures = []
checks = 0


def check(ok, label, detail=""):
    global checks
    checks += 1
    print(("  ok   " if ok else "  FAIL ") + label + (("  | " + str(detail)) if detail else ""))
    if not ok:
        failures.append(label + (("  | " + str(detail)) if detail else ""))
    return ok


# ---------------------------------------------------------------- 靶子：全屏窗


class MONITORINFO(ctypes.Structure):
    _fields_ = [
        ("cbSize", wt.DWORD),
        ("rcMonitor", wt.RECT),
        ("rcWork", wt.RECT),
        ("dwFlags", wt.DWORD),
    ]


class WNDCLASSEX(ctypes.Structure):
    _fields_ = [
        ("cbSize", wt.UINT),
        ("style", wt.UINT),
        ("lpfnWndProc", ctypes.c_void_p),
        ("cbClsExtra", ctypes.c_int),
        ("cbWndExtra", ctypes.c_int),
        ("hInstance", wt.HINSTANCE),
        ("hIcon", wt.HICON),
        ("hCursor", wt.HANDLE),
        ("hbrBackground", wt.HBRUSH),
        ("lpszMenuName", wt.LPCWSTR),
        ("lpszClassName", wt.LPCWSTR),
        ("hIconSm", wt.HICON),
    ]


WNDPROC = ctypes.WINFUNCTYPE(ctypes.c_ssize_t, wt.HWND, wt.UINT, wt.WPARAM, wt.LPARAM)

# DefWindowProcW 必须显式声明参数类型：不声明时 ctypes 按默认 c_int 转 LPARAM，
# 64 位下指针值一超范围就抛 OverflowError（回调里异常会被静默吞掉，窗只是不响应消息）。
user32.DefWindowProcW.argtypes = [wt.HWND, wt.UINT, wt.WPARAM, wt.LPARAM]
user32.DefWindowProcW.restype = ctypes.c_ssize_t
user32.SetForegroundWindow.argtypes = [wt.HWND]
user32.SetForegroundWindow.restype = wt.BOOL
user32.GetForegroundWindow.restype = wt.HWND
user32.AttachThreadInput.argtypes = [wt.DWORD, wt.DWORD, wt.BOOL]
user32.AttachThreadInput.restype = wt.BOOL
kernel32.GetCurrentThreadId.restype = wt.DWORD


def _noop_proc(hwnd, msg, wp, lp):
    # 只需要一扇“什么都不做”的窗：DefWindowProc 会让它正常参与前台 / Z 序
    return user32.DefWindowProcW(hwnd, msg, wp, lp)


_proc_ref = WNDPROC(_noop_proc)  # 必须留引用，回调被回收 = 进程崩
_CLASS = "WrProbeFullScreen"


def primary_monitor_rect():
    hmon = user32.MonitorFromPoint(wt.POINT(0, 0), MONITOR_DEFAULTTOPRIMARY)
    mi = MONITORINFO()
    mi.cbSize = ctypes.sizeof(MONITORINFO)
    if not user32.GetMonitorInfoW(hmon, ctypes.byref(mi)):
        return None
    return mi.rcMonitor


def force_foreground(hwnd):
    """把 hwnd 顶到前台并确认成功。

    直接 SetForegroundWindow 经常失败：前台锁只把这份权限给“当前前台进程”或最后
    收到输入的那个进程，而本脚本是从终端拉起来的（前台属于终端）。标准做法是临时把
    自己的输入队列挂到前台窗口的线程上，再设置前台，然后立刻摘掉。
    """
    for _ in range(6):
        pump()
        if user32.GetForegroundWindow() == hwnd:
            return True
        fg = user32.GetForegroundWindow()
        tid_fg = user32.GetWindowThreadProcessId(fg, None)
        tid_me = kernel32.GetCurrentThreadId()
        attached = False
        if tid_fg and tid_fg != tid_me:
            attached = bool(user32.AttachThreadInput(tid_me, tid_fg, True))
        user32.BringWindowToTop(hwnd)
        user32.SetForegroundWindow(hwnd)
        user32.SetFocus(hwnd)
        if attached:
            user32.AttachThreadInput(tid_me, tid_fg, False)
        time.sleep(0.25)
    return user32.GetForegroundWindow() == hwnd


def make_fullscreen_window(title):
    """造一扇盖满整屏、无边框、置顶的窗并抢到前台（几何全屏判定的靶子）。"""
    hinst = kernel32.GetModuleHandleW(None)
    wc = WNDCLASSEX()
    wc.cbSize = ctypes.sizeof(WNDCLASSEX)
    wc.lpfnWndProc = ctypes.cast(_proc_ref, ctypes.c_void_p)
    wc.hInstance = hinst
    wc.lpszClassName = _CLASS
    # 纯色底：圆角外那圈像素要拿它当“透明生效”的证据（见 TARGET_RGB 的注释）
    wc.hbrBackground = gdi32.CreateSolidBrush(TARGET_COLORREF)
    if not user32.RegisterClassExW(ctypes.byref(wc)):
        err = ctypes.get_last_error()
        # 1410 = 类已注册（同一进程重复调用），可以继续
        if err != 1410:
            print("  RegisterClassExW 失败: %d" % err)
            return 0
    r = primary_monitor_rect()
    if r is None:
        return 0
    hwnd = user32.CreateWindowExW(
        WS_EX_TOPMOST, _CLASS, title, WS_POPUP | WS_VISIBLE,
        r.left, r.top, r.right - r.left, r.bottom - r.top,
        None, None, hinst, None,
    )
    if not hwnd:
        print("  CreateWindowExW 失败: %d" % ctypes.get_last_error())
        return 0
    user32.ShowWindow(hwnd, SW_SHOW)
    user32.SetWindowPos(hwnd, wt.HWND(-1), r.left, r.top, r.right - r.left, r.bottom - r.top, 0x0040)
    if not force_foreground(hwnd):
        print("  ⚠ 抢不到前台：几何判定只会看真正的前台窗口，本次测量可能不成立")
    return hwnd


def destroy(hwnd):
    if hwnd:
        user32.DestroyWindow(hwnd)


# ---------------------------------------------------------------- 进程 / 窗口


def title_of(hwnd):
    n = user32.GetWindowTextLengthW(hwnd)
    if n <= 0:
        return ""
    buf = ctypes.create_unicode_buffer(n + 1)
    user32.GetWindowTextW(hwnd, buf, n + 1)
    return buf.value


def proc_windows(pid, visible_only=False):
    out = []

    @ctypes.WINFUNCTYPE(wt.BOOL, wt.HWND, wt.LPARAM)
    def cb(hwnd, _):
        p = wt.DWORD()
        user32.GetWindowThreadProcessId(hwnd, ctypes.byref(p))
        if p.value == pid and (not visible_only or user32.IsWindowVisible(hwnd)):
            out.append(hwnd)
        return True

    user32.EnumWindows(cb, 0)
    return out


def find_titled(pid, title, visible_only=True):
    for hwnd in proc_windows(pid, visible_only):
        if title_of(hwnd) == title:
            return hwnd
    return 0


def visible_titles(pid):
    return sorted({title_of(h) for h in proc_windows(pid, True) if title_of(h)})


def wait_for(fn, timeout=25.0, step=POLL):
    t0 = time.time()
    while time.time() - t0 < timeout:
        pump()
        v = fn()
        if v:
            return v
        time.sleep(step)
    return None


# 靶子窗是本进程造的，而 Windows 的“未响应”判定看的是**创建窗口那个线程有没有在泵消息**。
# 不泵的下场很具体（实测）：窗口 5 秒后被标成“未响应”，SetForegroundWindow 交给它的请求
# 被系统忽略，前台落到 dwm —— 于是几何判定看到的是桌面而不是全屏窗，这一段全屏在软件眼里
# 结束、窗在 5 秒时自毁。真实的全屏应用（游戏、播放器）都有自己的消息循环，所以探针也必须
# 有，否则测的根本不是真实场景。
PM_REMOVE = 0x0001

user32.PeekMessageW.argtypes = [ctypes.POINTER(wt.MSG), wt.HWND, wt.UINT, wt.UINT, wt.UINT]
user32.PeekMessageW.restype = wt.BOOL
user32.TranslateMessage.argtypes = [ctypes.POINTER(wt.MSG)]
user32.TranslateMessage.restype = wt.BOOL
user32.DispatchMessageW.argtypes = [ctypes.POINTER(wt.MSG)]
user32.DispatchMessageW.restype = ctypes.c_ssize_t


def pump():
    msg = wt.MSG()
    while user32.PeekMessageW(ctypes.byref(msg), None, 0, 0, PM_REMOVE):
        user32.TranslateMessage(ctypes.byref(msg))
        user32.DispatchMessageW(ctypes.byref(msg))


def keep_foreground(hwnd, fn, timeout=25.0, step=POLL):
    """边等 fn 成立、边把前台钉在靶子上。

    为什么需要：前台锁松开之后，别的窗口（终端、任务栏）随时可能把前台拿走，
    而几何判定只看**此刻**的前台窗口 —— 一旦被拿走，这一段全屏在软件眼里就结束了，
    探针会以为“它没反应”。每次采样前先确认前台还是靶子，不是就再顶一次。
    """
    t0 = time.time()
    while time.time() - t0 < timeout:
        pump()
        v = fn()
        if v:
            return v
        if user32.GetForegroundWindow() != hwnd:
            force_foreground(hwnd)
        time.sleep(step)
    return None


# ---------------------------------------------------------------- 点询问窗的按钮

user32.GetDpiForWindow.argtypes = [wt.HWND]
user32.GetDpiForWindow.restype = wt.UINT
user32.ClientToScreen.argtypes = [wt.HWND, ctypes.POINTER(wt.POINT)]
user32.ClientToScreen.restype = wt.BOOL
user32.SetCursorPos.argtypes = [ctypes.c_int, ctypes.c_int]
user32.SetCursorPos.restype = wt.BOOL
user32.GetCursorPos.argtypes = [ctypes.POINTER(wt.POINT)]
user32.GetCursorPos.restype = wt.BOOL
user32.mouse_event.argtypes = [wt.DWORD, wt.DWORD, wt.DWORD, wt.DWORD, ctypes.c_void_p]
user32.mouse_event.restype = None
user32.GetWindowLongPtrW.argtypes = [wt.HWND, ctypes.c_int]
user32.GetWindowLongPtrW.restype = ctypes.c_ssize_t
user32.WindowFromPoint.argtypes = [wt.POINT]
user32.WindowFromPoint.restype = wt.HWND
user32.GetAncestor.argtypes = [wt.HWND, wt.UINT]
user32.GetAncestor.restype = wt.HWND
user32.GetWindowThreadProcessId.argtypes = [wt.HWND, ctypes.POINTER(wt.DWORD)]
user32.GetWindowThreadProcessId.restype = wt.DWORD
user32.GetClassNameW.argtypes = [wt.HWND, wt.LPWSTR, ctypes.c_int]
user32.GetClassNameW.restype = ctypes.c_int
user32.GetClientRect.argtypes = [wt.HWND, ctypes.POINTER(wt.RECT)]
user32.GetWindowRect.argtypes = [wt.HWND, ctypes.POINTER(wt.RECT)]
user32.IsZoomed.argtypes = [wt.HWND]

GWL_EXSTYLE = -20
GA_ROOT = 2
WS_EX_TOPMOST = 0x00000008
WS_EX_TOOLWINDOW = 0x00000080
WS_EX_APPWINDOW = 0x00040000
WS_EX_NOREDIRECTIONBITMAP = 0x00200000
WS_EX_CONTROLPARENT = 0x00010000
WS_EX_NOACTIVATE = 0x08000000


def dpi_scale(hwnd):
    """窗的 DPI 缩放比。CSS 像素是 DIP，ClientToScreen 给的是物理像素，必须换算。"""
    try:
        dpi = user32.GetDpiForWindow(hwnd)
    except Exception:
        dpi = 0
    if not dpi:
        return 1.0
    return dpi / 96.0


def cursor_pos():
    pt = wt.POINT()
    user32.GetCursorPos(ctypes.byref(pt))
    return (pt.x, pt.y)


def hit_info(pt):
    """某屏幕坐标命中的窗口：顶层祖先、pid、类名。

    WebView2 的渲染窗属于 msedgewebview2.exe（与主进程不同 pid），所以判定“点落在页面上”
    不能比 pid，要比**顶层祖先是不是询问窗**（GetAncestor GA_ROOT）。
    """
    h = user32.WindowFromPoint(wt.POINT(pt[0], pt[1]))
    root = user32.GetAncestor(h, GA_ROOT) or h
    hp = wt.DWORD()
    user32.GetWindowThreadProcessId(h, ctypes.byref(hp))
    buf = ctypes.create_unicode_buffer(128)
    user32.GetClassNameW(h, buf, 128)
    return {"hit": h, "root": root, "pid": hp.value, "cls": buf.value}


def btn_screen_pos(ask_hwnd, answer):
    """某个按钮中心的屏幕坐标（DIP 设计值 × 窗的缩放比）。"""
    i = BTN_ORDER.index(answer)
    scale = dpi_scale(ask_hwnd)
    origin = wt.POINT(0, 0)
    if not user32.ClientToScreen(ask_hwnd, ctypes.byref(origin)):
        return None
    return (int(round(origin.x + BTN_CX[i] * scale)),
            int(round(origin.y + BTN_CY * scale)))


def click_ask_button(ask_hwnd, answer):
    """在询问窗上真实点击某个按钮（系统级鼠标注入）。

    为什么不投 WM_LBUTTONDOWN：WebView2 的命中与事件处理都在它自己的渲染子窗里，
    给顶层窗投消息到不了渲染层。真实输入事件由系统按 Z 序与命中测试分发，WebView2
    会正常当成用户点击 —— 这也正是“用户真能点”这件事本身。

    窗是 WS_EX_NOACTIVATE，点击不需要激活，所以这条路径不会被 NOACTIVATE 挡住；
    反过来它也让本方法能顺带证明“点了之后前台没被抢走”。
    """
    pos = btn_screen_pos(ask_hwnd, answer)
    if not pos:
        return None, None
    x, y = pos
    # 点之前先记下命中谁：Z 序若被压在下面，点击会直接穿透到全屏窗上，
    # 现象是“按钮点了没反应”，而根因跟按钮无关。
    info = hit_info((x, y))
    user32.SetCursorPos(x, y)
    time.sleep(0.08)
    user32.mouse_event(MOUSEEVENTF_LEFTDOWN, 0, 0, 0, None)
    time.sleep(0.05)
    user32.mouse_event(MOUSEEVENTF_LEFTUP, 0, 0, 0, None)
    return (x, y), info


def ex_style(hwnd):
    return user32.GetWindowLongPtrW(hwnd, GWL_EXSTYLE) & 0xFFFFFFFF


def screen_pixel(x, y):
    """屏幕某点的颜色 (r,g,b)；读不到返回 None。"""
    hdc = user32.GetDC(None)
    if not hdc:
        return None
    try:
        c = gdi32.GetPixel(hdc, int(x), int(y))
    finally:
        user32.ReleaseDC(None, hdc)
    if c == 0xFFFFFFFF:  # CLR_INVALID
        return None
    return (c & 0xFF, (c >> 8) & 0xFF, (c >> 16) & 0xFF)


def near_color(got, want, tol=26):
    """按通道比颜色。屏幕合成有轻微偏差（HDR / 色彩管理），给一点容差。"""
    if got is None:
        return False
    return all(abs(a - b) <= tol for a, b in zip(got, want))


# ---------------------------------------------------------------- 系统信号


IFACE = None  # 保留变量名只为可读性：真正用的是下面这组 argtypes/restype
_shell32 = ctypes.WinDLL("shell32", use_last_error=True)
_shell32.SHQueryUserNotificationState.argtypes = [ctypes.POINTER(wt.DWORD)]
_shell32.SHQueryUserNotificationState.restype = ctypes.c_long


def read_quns():
    """SHQueryUserNotificationState：只用来在诊断行里说明“系统此刻报的是第几档”。"""
    v = wt.DWORD()
    hr = _shell32.SHQueryUserNotificationState(ctypes.byref(v))
    return v.value if hr == 0 else None


QUNS_TEXT = {
    1: "not-present", 2: "busy（非独占全屏档）", 3: "d3d-fullscreen（独占全屏）",
    4: "presentation（演示设置）", 5: "accepts-notifications", 6: "quiet-time", 7: "app",
}


def quns_text():
    q = read_quns()
    if q is None:
        return "QUNS=读取失败"
    return "QUNS=%d %s" % (q, QUNS_TEXT.get(q, "未知"))


def start(cfg_path):
    env = dict(os.environ)
    env["WR_SETTINGS_PATH"] = cfg_path
    env["WR_LOG_FILE"] = LOG
    env["WR_LOG_LEVEL"] = "debug"
    logf = open(LOG, "a", encoding="utf-8")
    return subprocess.Popen([EXE], cwd=ROOT, env=env, stdout=logf, stderr=subprocess.STDOUT)


def kill(proc):
    subprocess.run(["taskkill", "/PID", str(proc.pid), "/T", "/F"], capture_output=True)
    try:
        proc.wait(timeout=10)
    except Exception:
        pass


def log_text():
    try:
        with open(LOG, encoding="utf-8", errors="replace") as f:
            return f.read()
    except FileNotFoundError:
        return ""


def wait_log(needle, timeout):
    return wait_for(lambda: needle in log_text(), timeout=timeout)


def wait_any_log(needles, timeout):
    """等任意一条文案出现，返回命中的那条。

    为什么需要：「12 秒结案」有两条通路 —— 定时器（fsAskArmTimeout）与拍内 deadline
    （step 的 `in.Now >= st.deadline`）。两者都在第 12 秒到期，谁先跑谁赢，日志措辞
    因此有「询问超时」与「全屏询问结案」两种。它们都表示“同一件事真的发生了”，
    断言只认其中一条就会随机红。
    """
    t0 = time.time()
    while time.time() - t0 < timeout:
        pump()
        txt = log_text()
        for n in needles:
            if n in txt:
                return n
        time.sleep(POLL)
    return None


def make_cfg(busy_mode, app_name, skip=None):
    with open(DEFAULTS, encoding="utf-8") as f:
        cfg = json.load(f)
    cfg["appName"] = app_name
    cfg["startMinimized"] = True
    cfg["checkUpdateOnStart"] = False
    # 其余自动原因全部关掉：这一轮只验“非独占全屏”这一条，别让勿扰/空闲/投屏插进来
    cfg["idlePauseMin"] = 600
    cfg["dndPause"] = False
    cfg["fullscreenPause"] = False
    cfg["castMirrorPause"] = False
    cfg["castWirelessPause"] = False
    cfg.setdefault("appPause", {})["enabled"] = False
    cfg.setdefault("sound", {})["enabled"] = False
    for k in ("short", "long"):
        cfg[k]["enabled"] = False
    cfg.setdefault("takeout", {})["enabled"] = False
    cfg["fullscreenBusyMode"] = busy_mode
    cfg["fullscreenSkipApps"] = skip or []
    shutil.rmtree(ISO, ignore_errors=True)
    os.makedirs(ISO, exist_ok=True)
    p = os.path.join(ISO, "settings.json")
    with open(p, "w", encoding="utf-8") as f:
        json.dump(cfg, f, ensure_ascii=False, indent=2)
    return p


ISO_CFG = os.path.join(ISO, "settings.json")


def read_iso_cfg():
    try:
        with open(ISO_CFG, encoding="utf-8") as f:
            return json.load(f)
    except Exception:
        return {}


def skip_items():
    """隔离配置里的名单（归一化后的形状）。"""
    items = read_iso_cfg().get("fullscreenSkipApps") or []
    return items if isinstance(items, list) else []


# 靶子窗是本进程（Python）造的，所以被测程序从 Toolhelp 看到的 exe 是 python.exe ——
# 不是 rest_fsask_probe.exe。所有“名单里该出现谁”的断言都必须按这个算。
TARGET_EXE = os.path.basename(sys.executable).lower()


def exe_base():
    return TARGET_EXE


def digest(path):
    if not os.path.exists(path):
        return ""
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(65536), b""):
            h.update(chunk)
    return h.hexdigest()


# ---------------------------------------------------------------- 用例


def start_case(mode, app_name, skip=None):
    """起进程 → 立全屏靶子 → 返回 (proc, 靶子 hwnd)；调用方负责收尾。

    监视链每 3 秒一拍、状态机再做 2 拍进入去抖，所以“立窗之后”要等 6 秒以上才可能有反应。
    """
    cfg = make_cfg(mode, app_name, skip)
    if os.path.exists(LOG):
        os.remove(LOG)
    proc = start(cfg)
    got = wait_for(lambda: find_titled(proc.pid, app_name, visible_only=False), timeout=25)
    if not got:
        return proc, 0
    hwnd = make_fullscreen_window("WrProbeFullScreen")
    return proc, hwnd


def ask_gone(pid, timeout=8):
    return bool(wait_for(lambda: not find_titled(pid, ASK_TITLE), timeout=timeout))


def still_quiet(pid, target, secs):
    """在 secs 秒内盯着：不许再冒出询问窗（期间继续把靶子钉在前台）。"""
    again = keep_foreground(target, lambda: find_titled(pid, ASK_TITLE), timeout=secs)
    return again


BTN_TEXT = {"pause": "暂停", "skip": "本次不暂停", "today": "今天不问", "never": "永不询问"}


def log_tail(n=14):
    lines = [x for x in log_text().splitlines() if x.strip()]
    return " / ".join(lines[-n:])


def case_answer(tag, answer, app_name, occlusion=False):
    """ask 档 → 等询问窗 → 真点一个按钮 → 断言日志、落盘、窗自散、不复问。

    四条例行里唯一需要分支的是“答完之后应该留下什么”：
      pause  → 立刻暂停，名单不动
      skip   → 不暂停、名单不动，本段不再问
      today  → 落盘 mode=today（until = 下一个本地零点），本段不再问
      never  → 落盘 mode=never，新的一段也不再问
    """
    print("\n[%s] ask 档 → 点「%s」" % (tag, BTN_TEXT[answer]))
    proc, target = start_case("ask", app_name)
    ask = 0
    try:
        if not check(bool(target), "%s · 进程与全屏靶子就位" % tag):
            return
        ask = keep_foreground(target, lambda: find_titled(proc.pid, ASK_TITLE), timeout=22)
        if not check(bool(ask), "%s · 询问窗出现" % tag, visible_titles(proc.pid)):
            return

        # 窗的几何与扩展样式：420×200（按 DPI 缩放）、置顶、不占任务栏、不抢焦点
        scale = dpi_scale(ask)
        cr = wt.RECT()
        user32.GetClientRect(ask, ctypes.byref(cr))
        wr = wt.RECT()
        user32.GetWindowRect(ask, ctypes.byref(wr))
        ex = ex_style(ask)
        print("       客户区 %d×%d（缩放 %.2f）/ ExStyle 0x%08X / 位置 (%d,%d)"
              % (cr.right, cr.bottom, scale, ex, wr.left, wr.top))
        check(abs(cr.right - 420 * scale) <= 2 and abs(cr.bottom - 200 * scale) <= 2,
              "%s · 询问窗尺寸 420×200（DIP）" % tag, "%d×%d" % (cr.right, cr.bottom))
        need = (WS_EX_TOPMOST, WS_EX_TOOLWINDOW, WS_EX_CONTROLPARENT, WS_EX_NOACTIVATE)
        check(all(ex & b for b in need), "%s · ExStyle 含 TOPMOST/TOOLWINDOW/CONTROLPARENT/NOACTIVATE" % tag,
              "0x%08X" % ex)
        check(not user32.IsZoomed(ask), "%s · 询问窗没被最大化" % tag)

        before_lines = len(log_text().splitlines())
        # 窗一建出来页面还没加载完：WebView2 要先导航、再加载 shim 与 wails/runtime.js，
        # 这期间点下去落在空白页上（按钮根本还不存在）。等页面就绪再点 —— 这也是真实用户
        # 看到窗、看清文字之后再点的节奏。1.2 秒是本机实测加载时间的 2 倍余量。
        time.sleep(1.2)

        if occlusion:
            # 主动把全屏靶子重新顶到 TOPMOST 组顶：同为 TOPMOST 时**后抬者在上**，询问窗
            # 会被整个盖住（用户看不见、点击穿透到底下的全屏内容）。守卫必须自己抬回来。
            #
            # 这条以前的做法是“每 4 拍无条件抬一次”（≤480ms），2026-11-23 改成“采样发现
            # 被盖住才抬”：正常情况下零噪音，被盖住时下一个 120ms tick 就恢复。判据因此也
            # 收紧到 ≤700ms —— 放宽到 480ms 就分不出新旧实现，等于没测。
            bpos = btn_screen_pos(ask, answer)
            if check(bool(bpos), "%s · 拿到按钮中心坐标" % tag, bpos):
                user32.SetWindowPos(target, wt.HWND(-1), 0, 0, 0, 0, 0x0013)
                covered = hit_info(bpos)["root"] != ask
                if covered:
                    t0 = time.time()
                    back = wait_for(lambda: hit_info(bpos)["root"] == ask,
                                    timeout=2.0, step=0.02)
                    dt = (time.time() - t0) * 1000
                    check(bool(back), "%s · 被全屏应用压住后能自己抬回来" % tag,
                          "恢复 %.0fms" % dt)
                    check(dt <= 700, "%s · 抬回延迟 ≤700ms（守卫 120ms 一拍）" % tag,
                          "%.0fms" % dt)
                else:
                    # 打压与“守卫刚好抬了一次”撞在同一瞬间：这次没构造出被压状态。
                    # 不计失败（那是采样竞态，不是产品行为），但要明说，别让它悄悄过去。
                    print("       注意：打压瞬间询问窗仍在上层，本次未构造出被压状态"
                          "（守卫恰好刚抬过一次）")

        pos, info = click_ask_button(ask, answer)
        check(bool(pos), "%s · 鼠标注入到按钮中心" % tag, pos)
        if info:
            print("       按钮中心命中：root=%s pid=%d class=%s"
                  % (info["root"], info["pid"], info["cls"] or "?"))
        # 这一条是“看得见且点得到”的直接判据：按钮坐标的顶层祖先必须是询问窗自己。
        # 落在别的窗上就说明 Z 序被压住了 —— 点击会穿透到下面那扇全屏窗，日志里一条都不会有。
        check(bool(info) and info["root"] == ask,
              "%s · 按钮坐标上确实是询问窗（Z 序没被全屏应用压住）" % tag,
              "" if not info else "root=%s ask=%s class=%s" % (info["root"], ask, info["cls"]))

        # 点击不能把前台抢走：NOACTIVATE 的意义就在这一步（点击本身也算一次激活尝试）
        samples = []
        for _ in range(3):
            pump()
            samples.append(user32.GetForegroundWindow())
            time.sleep(0.15)
        check(all(s != ask for s in samples),
              "%s · 点击后询问窗没有变成前台（点也不抢焦点）" % tag,
              "foreground=%s ask=%s" % (samples[-1], ask))

        want = {
            "pause": "暂停提醒（用户选择暂停）",
            "skip": "本次不暂停（本段不再询问）",
            "today": "今天不再询问",
            "never": "已加入“全屏时不暂停”名单",
        }[answer]
        got = wait_log(want, 4)
        if not got:
            # 极少数情况下第一下没落在按钮上（页面重排 / WebView2 刚拿到输入）。
            # 补点一次并把这件事打出来 —— 不做无限重试，真坏了（绑定名错等）两次都会红。
            print("       第一次点击未见效，补点一次")
            click_ask_button(ask, answer)
            got = wait_log(want, 5)
        if not got:
            after = log_text().splitlines()[before_lines:]
            # 只看**本次新增**的行：上一段残留的 FullScreenAskAnswer 会让诊断误报“已收到”
            sent = any("FullScreenAskAnswer" in ln for ln in after)
            check(bool(info) and info["root"] == ask,
                  "%s · 补点前按钮坐标仍在询问窗上" % tag,
                  "" if not info else "root=%s class=%s" % (info["root"], info["cls"]))
            print("       点击未见效：Go 侧%s收到绑定调用 / 点后日志：%s"
                  % ("已" if sent else "未", " / ".join(after[-6:]) or "（没有新行）"))
        check(bool(got), "%s · 日志出现「%s」" % (tag, want), log_tail())

        check(ask_gone(proc.pid), "%s · 答复后询问窗自己收掉" % tag, visible_titles(proc.pid))

        items = skip_items()
        hit = [it for it in items if str(it.get("exe", "")).lower() == exe_base().lower()]
        if answer in ("skip", "pause"):
            check(not hit, "%s · 名单没有被写进东西（这次答复不该记住应用）" % tag, items)
        elif answer == "today":
            now_ms = int(time.time() * 1000)
            check(len(hit) == 1 and hit[0].get("mode") == "today",
                  "%s · 名单落盘 mode=today" % tag, items)
            if hit:
                until = int(hit[0].get("until") or 0)
                lt = time.localtime(until / 1000.0)
                check(until > now_ms and (lt.tm_hour, lt.tm_min, lt.tm_sec) == (0, 0, 0),
                      "%s · until 是下一个本地零点" % tag,
                      time.strftime("%Y-%m-%d %H:%M:%S", lt))
        else:  # never
            check(len(hit) == 1 and hit[0].get("mode") == "never",
                  "%s · 名单落盘 mode=never" % tag, items)

        if answer == "pause":
            check(not still_quiet(proc.pid, target, 4),
                  "%s · 已暂停后不再问（本段已结案）" % tag)
        elif answer in ("skip", "today"):
            check(not still_quiet(proc.pid, target, 15),
                  "%s · 同一段全屏不再询问" % tag)
        elif answer == "never":
            # 新的一段：撤靶（等 QUNS 退回 5，别在迟滞窗口里立新靶）→ 再立一扇。
            # 名单命中就该彻底安静。
            destroy(target)
            wait_for(lambda: read_quns() == 5, timeout=20)
            target = make_fullscreen_window("WrProbeFullScreen2")
            again = keep_foreground(target, lambda: find_titled(proc.pid, ASK_TITLE), timeout=20)
            check(not again, "%s · 新的一段全屏也不再询问（名单命中）" % tag,
                  visible_titles(proc.pid))
    finally:
        destroy(target)
        kill(proc)


def case_skip_list(app_name):
    """名单里已经有这个应用（mode=never）时，ask 档从第一拍起就完全安静。"""
    print("\n[H] 名单命中：fullscreenSkipApps 预置本应用为 never")
    proc, target = start_case("ask", app_name,
                              skip=[{"exe": exe_base(), "label": "靶子程序", "mode": "never", "until": 0}])
    try:
        if not check(bool(target), "H · 进程与靶子就位"):
            return
        ask = keep_foreground(target, lambda: find_titled(proc.pid, ASK_TITLE), timeout=20)
        check(not ask, "H · 名单里的应用全屏时不弹询问窗", visible_titles(proc.pid))
        check("询问是否暂停" not in log_text(), "H · 日志里一次询问都没起过")
    finally:
        destroy(target)
        kill(proc)


def case_pause_mode(app_name):
    """pause 档：不问，直接暂停。"""
    print("\n[G] pause 档：不询问，直接暂停")
    proc, target = start_case("pause", app_name)
    try:
        if not check(bool(target), "G · 进程与靶子就位"):
            return
        check(bool(wait_log("检测到非独占全屏", 20)),
              "G · 检测到非独占全屏后直接落了暂停", log_tail())
        check("询问是否暂停" not in log_text(), "G · 这一档不建询问窗")
        ask = wait_for(lambda: find_titled(proc.pid, ASK_TITLE), timeout=4)
        check(not ask, "G · 全程没有询问窗", visible_titles(proc.pid))
    finally:
        destroy(target)
        kill(proc)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--no-build", action="store_true")
    ap.add_argument("--only", default="", help="只跑指定章节（逗号分隔的字母，如 B,C）")
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

    only = {x.strip().upper() for x in args.only.split(",") if x.strip()}

    before = digest(REAL_CFG)
    if os.path.exists(LOG):
        os.remove(LOG)
    app_name = "吃饼人也敢休息？"

    # ---------- A 对照组：出厂默认（不处理） ----------
    if not only or "A" in only:
        print("\n[A] 对照组：fullscreenBusyMode=off（出厂默认）")
        proc, target = start_case("off", app_name)
        try:
            if not check(bool(target), "A · 进程起来了，全屏靶子已立在主屏上（盖满 rcMonitor）"):
                return 1
            print("       诊断：%s / 前台是靶子：%s" % (quns_text(), user32.GetForegroundWindow() == target))
            # 12 秒：足够让 watcher 跑完 4 拍 + 3 秒，还看不见询问窗就说明这一档真的不处理
            ask = keep_foreground(target, lambda: find_titled(proc.pid, ASK_TITLE), timeout=ASK_TIMEOUT)
            check(not ask, "A · 出厂默认：非独占全屏不询问、也不弹窗",
                  "banner=%s" % (title_of(ask) if ask else "无"))
            check("询问是否暂停" not in log_text(), "A · 日志里没有起过询问")
        finally:
            destroy(target)
            kill(proc)

    # ---------- B 主用例：ask 档（超时结案 + 不抢前台 + 换段再问） ----------
    if not only or "B" in only:
        print("\n[B] 主用例：fullscreenBusyMode=ask")
        proc, target = start_case("ask", app_name)
        ask_hwnd = 0
        try:
            if not check(bool(target), "B · 进程起来了，全屏靶子已立在主屏上"):
                return 1
            print("       诊断：%s / 前台是靶子：%s" % (quns_text(), user32.GetForegroundWindow() == target))
            ask_hwnd = keep_foreground(target, lambda: find_titled(proc.pid, ASK_TITLE), timeout=22)
            check(bool(ask_hwnd), "B · 检测到非独占全屏后弹出询问窗「%s」" % ASK_TITLE,
                  "%s / 可见窗：%s" % (quns_text(), visible_titles(proc.pid)))
            # 计时起点：看到窗的这一刻。发现延迟 <= POLL(0.3s)，所以实测值只会**略大于**真实时长。
            t_ask = time.time() if ask_hwnd else 0.0
            check(bool(wait_log("[fsAsk] 非独占全屏", 3)), "B · 日志记下这一段的成因与应用名")

            # —— 本探针最要紧的一条：询问窗不许抢前台 ——
            # 抢了的话无边框游戏会被踢出全屏，而 QUNS 正是按“前台铺满整屏”判的，
            # 这一段全屏会当场结束 —— 询问窗自己把前提毁掉。
            #
            # 判定口径：只看“前台有没有变成询问窗”。被终端/任务栏拿走不算（探针是在终端里
            # 跑的，前台锁本来就不稳），但一旦询问窗成了前台，QUNS 会从 2 掉回 5，这一段
            # 全屏就自己结束了 —— 那才是缺陷。
            samples = []
            quns_seen = set()
            for _ in range(8):
                pump()  # 靶子要一直有消息泵，否则会被判“未响应”（见 pump 的注释）
                samples.append(user32.GetForegroundWindow())
                q = read_quns()
                if q is not None:
                    quns_seen.add(q)
                time.sleep(0.4)
            hijacked = [s for s in samples if s == ask_hwnd]
            check(not hijacked, "B · 询问窗没有抢走前台焦点（WS_EX_NOACTIVATE 生效）",
                  "采样 8 次，询问窗当前台 %d 次；foreground=%s target=%s ask=%s"
                  % (len(hijacked), samples[-1], target, ask_hwnd))
            check(quns_seen == {2}, "B · 询问期间 QUNS 一直是 2（这段全屏没被打断）",
                  sorted(quns_seen))

            # 询问期间不许有第二扇窗冒出来（休息窗 / 提前提示都不许）
            titles_mid = visible_titles(proc.pid)
            extra = [t for t in titles_mid if t not in (app_name, ASK_TITLE)]
            check(not extra, "B · 询问期间没有多出别的窗（休息窗没被惊动）", extra or "只有询问窗")

            # —— 窗口属性：这三条只能问系统，单测只能验“我们提交了什么” ——
            #
            # ExStyle 是整份替换（Wails 只在它非零时覆盖自己那份默认值），所以这里比的是
            # “系统实际收到的位”。缺 NOREDIRECTIONBITMAP 时单测看不出来（那是 Wails 的默认位），
            # 真机上也只表现在圆角外那圈像素上 —— 2026-11-23 就是这样漏了一位。
            if ask_hwnd:
                es = ex_style(ask_hwnd)
                for name, bit in (
                    ("TOPMOST（压在其它窗之上）", WS_EX_TOPMOST),
                    ("TOOLWINDOW（不占任务栏 / Alt-Tab）", WS_EX_TOOLWINDOW),
                    ("CONTROLPARENT（Tab 在窗内控件间走）", WS_EX_CONTROLPARENT),
                    ("NOACTIVATE（点击不抢前台）", WS_EX_NOACTIVATE),
                    ("NOREDIRECTIONBITMAP（Translucent 透明合成）", WS_EX_NOREDIRECTIONBITMAP),
                ):
                    check(es & bit == bit, "B · ExStyle 含 WS_EX_%s" % name, "exstyle=0x%08X" % es)
                check(es & WS_EX_APPWINDOW == 0, "B · ExStyle 不含 APPWINDOW（不进任务栏）",
                      "exstyle=0x%08X" % es)

                # 几何：420×200 DIP。GetWindowRect 给物理像素，必须按该窗的 DPI 换算，
                # 否则 200% 缩放下会得到“840×400 不等于 420×200”的假红。
                wr = wt.RECT()
                if user32.GetWindowRect(ask_hwnd, ctypes.byref(wr)):
                    sc = dpi_scale(ask_hwnd) or 1.0
                    got_w = (wr.right - wr.left) / sc
                    got_h = (wr.bottom - wr.top) / sc
                    check(abs(got_w - 420) <= 2 and abs(got_h - 200) <= 2,
                          "B · 窗尺寸是 420×200 DIP",
                          "实测 %.1f×%.1f（scale=%.2f）" % (got_w, got_h, sc))

                    # 圆角外那圈必须透出全屏内容（这里是靶子的纯蓝）。角上 3 DIP 处：
                    # 卡片圆角 14px，圆心在 (14,14)，点 (3,3) 到圆心 15.6 > 14 → 落在圆弧外。
                    # 判据只要求“蓝通道显著高于红通道”，比绝对颜色耐阴影 / 色彩管理。
                    d = max(1, int(round(3 * sc)))
                    pts = [(wr.left + d, wr.top + d), (wr.right - d - 1, wr.top + d)]
                    got = [screen_pixel(*p) for p in pts]
                    translucent = any(g and g[2] > g[0] + 40 for g in got)
                    check(translucent,
                          "B · 圆角外是透明的（透出全屏画面，不是一块实底）",
                          "角上像素 %s，靶子底色 rgb%s" % (got, TARGET_RGB))


            # 等结案。这里必须自己钉前台而不是调 wait_any_log：前台一旦被终端拿走，
            # QUNS 会从 2 掉回 5，这一段全屏在软件眼里就结束了 —— 结案文案会变成
            # “条件消失”，计时断言随之失去意义（探针会误报成“没超时”）。
            concluded = None
            while time.time() - t_ask < ASK_TIMEOUT + 10:
                pump()
                if user32.GetForegroundWindow() != target:
                    force_foreground(target)
                txt = log_text()
                for n in ("询问超时，按暂停处理", "全屏询问结案"):
                    if n in txt:
                        concluded = n
                        break
                if concluded:
                    break
                time.sleep(POLL)
            elapsed = time.time() - t_ask
            check(bool(concluded), "B · 不答复则 12 秒后按「暂停」结案（不静默放过）",
                  concluded or quns_text())
            # 只断言“日志里有结案”是不够的：定时器若跑成 25 秒、或拍内 deadline 压根没生效，
            # 那条断言照样绿。这里量真实墙钟 —— 下限 11.0 是发现延迟 + POLL 的余量，
            # 上限 15.5 挡住“结案了但晚得多”（比如要等下一拍才结算）。
            if ask_hwnd:
                check(11.0 <= elapsed <= 15.5,
                      "B · 结案发生在 12 秒（实测墙钟，不含发现延迟 <=0.3s）",
                      "%.2f 秒" % elapsed)
            check(bool(wait_log("检测到非独占全屏：暂停提醒", 5)),
                  "B · 结案那一刻真的落了暂停")
            check(bool(wait_for(lambda: not find_titled(proc.pid, ASK_TITLE), timeout=8)),
                  "B · 结案后询问窗自己收掉（不留一扇悬空的窗）")

            # 结案后**同一段**（靶子没撤、全屏还在）不许再问：已经暂停了，这一段的询问
            # 已经结束。这条钉的是一次真实发生过的死循环 —— 段键或守卫任一失灵，
            # 就会每 6 秒弹一次“要不要暂停”，永远弹不完。
            reask = keep_foreground(target, lambda: find_titled(proc.pid, ASK_TITLE), timeout=10)
            check(not reask, "B · 结案后同一段不再重复询问（观察 10 秒）",
                  "又弹了一次" if reask else "10 秒内没有第二次")

            # 撤掉靶子 = 这一段全屏结束；等 QUNS 真的退回 5（实测有约 6 秒迟滞），
            # 再立一扇 = 干净的新段，必须重新问。
            destroy(target)
            wait_for(lambda: read_quns() == 5, timeout=20)
            target = make_fullscreen_window("WrProbeFullScreen2")
            ask2 = keep_foreground(target, lambda: find_titled(proc.pid, ASK_TITLE), timeout=22)
            check(bool(ask2), "B · 换一段全屏会重新询问（段键含 HWND）", visible_titles(proc.pid))
        finally:
            destroy(target)
            kill(proc)

    # ---------- C~F 四个按钮的答复 ----------
    for tag, answer in (("C", "pause"), ("D", "skip"), ("E", "today"), ("F", "never")):
        if not only or tag in only:
            case_answer(tag, answer, app_name, occlusion=(tag == "C"))

    # ---------- G pause 档 / H 名单命中 ----------
    if not only or "G" in only:
        case_pause_mode(app_name)
    if not only or "H" in only:
        case_skip_list(app_name)

    # ---------- 收尾：配置文件一个字节都没动 ----------
    print("\n[Z] 隔离")
    check(digest(REAL_CFG) == before, "Z · 真实配置没被改动（sha256 一致）")

    print("\n结果：%d 项断言，失败 %d 项" % (checks, len(failures)))
    for f in failures:
        print("  - " + f)
    return 0 if not failures else 1


if __name__ == "__main__":
    sys.exit(main())
