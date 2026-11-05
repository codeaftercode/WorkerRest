#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""提醒窗口端到端：真 GUI 进程 → 真实调度器 / 真实全局热键 → 真实窗口。

无头（-tags server）构建根本不建窗口，所以「配置有没有真的接到 WebviewWindowOptions
上」「窗口是不是真的弹出来、尺寸对不对、到点会不会自己关」这三件事在无头探针里
一条都验不到。本脚本用真产物 + Win32 枚举窗口补这一层。

三段（每段各自起一次进程，互不干扰）：
  A 短休息全流程：倒计时模式 45s + 提前 20s
       → 约 25s 弹「即将休息」，约 45s 弹「休息一下」，约 65s 自己关掉
       → 顺带把日志里 preBreak 算出的 %dx%d @(%d,%d) 与真实窗口矩形对齐（计算 = 实际）
  B 外卖提醒：把一个「已经过了提前线」的时间槽放进去（slot=现在+2min, lead=300s）
       → 提前线已过 → 秒级内触发，窗口应为 100% 工作区（这轮 appearance.fullscreen=true）
  C 全局热键 → 立即休息：SendInput 发 Ctrl+Alt+Shift+F24（与 shortcuts.nextBreak 对应）
       → 5s 内出现「休息一下」

为什么这三段必须分开起进程：A 跑完会重排下一次休息，B/C 混进去会被残留的调度线干扰。

隔离：WR_SETTINGS_PATH 指向 %TEMP% 的配置副本；并把「自动暂停」的四条来源
（空闲 / 勿扰 / 全屏 / 投屏 / 应用占用）全部关掉 —— 探针机全程无输入，
不关的话空闲监视会在跑的过程中把休息挂起，断言随机变红，看起来像产品坏了。

用法：python scripts/probe/probe_windows_e2e.py [--no-build] [--keep]
退出码 0 = 全部通过。
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
ISO = os.path.join(TEMP, "wr_wins_e2e_iso")
LOG = os.path.join(TEMP, "wr_wins_e2e.log")

user32 = ctypes.windll.user32
dwmapi = ctypes.windll.dwmapi
user32.SetProcessDPIAware()

TOL = 24  # DIP：无边框窗口的不可见拖动带 + DWM 取整

failures = []
skipped = []
checks = 0


def check(ok, label, detail=""):
    global checks
    checks += 1
    print(("  ok   " if ok else "  FAIL ") + label + (("  | " + str(detail)) if detail else ""))
    if not ok:
        failures.append(label + (("  | " + str(detail)) if detail else ""))
    return ok


def skip(label, why):
    print("  --   " + label + "  | 无法验证：" + why)
    skipped.append(label + " ← " + why)


# ---------------- Win32 小工具 ----------------

def titled_windows(pid):
    """该进程下「可见且有标题」的窗口 → [(hwnd, title)]。

    只认有标题的：WebView2 自己会建若干无标题的隐藏子窗，全算进来会淹没断言。
    """
    found = []

    @ctypes.WINFUNCTYPE(wt.BOOL, wt.HWND, wt.LPARAM)
    def cb(hwnd, _):
        p = wt.DWORD()
        user32.GetWindowThreadProcessId(hwnd, ctypes.byref(p))
        if p.value == pid and user32.IsWindowVisible(hwnd):
            n = user32.GetWindowTextLengthW(hwnd)
            if n > 0:
                buf = ctypes.create_unicode_buffer(n + 1)
                user32.GetWindowTextW(hwnd, buf, n + 1)
                found.append((hwnd, buf.value))
        return True

    user32.EnumWindows(cb, 0)
    return found


def visible_rect(hwnd):
    """可见矩形（物理像素）：DWM 扩展边框扣掉无边框窗口的不可见拖动带。"""
    r = wt.RECT()
    hr = dwmapi.DwmGetWindowAttribute(hwnd, 9, ctypes.byref(r), ctypes.sizeof(r))
    if hr != 0:
        user32.GetWindowRect(hwnd, ctypes.byref(r))
    return r.left, r.top, r.right - r.left, r.bottom - r.top


def dip_rect(hwnd):
    x, y, w, h = visible_rect(hwnd)
    dpi = user32.GetDpiForWindow(hwnd) or 96
    return (round(x * 96 / dpi), round(y * 96 / dpi),
            round(w * 96 / dpi), round(h * 96 / dpi), dpi)


# MONITORINFO + GetMonitorInfoW：工作区（扣任务栏）。窗口尺寸是相对工作区算的，
# 用整屏去比会永远差一条任务栏的高度（48 DIP），看起来像 bug。
class MONITORINFO(ctypes.Structure):
    _fields_ = [
        ("cbSize", wt.DWORD),
        ("rcMonitor", wt.RECT),
        ("rcWork", wt.RECT),
        ("dwFlags", wt.DWORD),
    ]


MONITOR_DEFAULTTONEAREST = 2
user32.MonitorFromWindow.restype = wt.HMONITOR
user32.MonitorFromWindow.argtypes = [wt.HWND, wt.DWORD]
# 不声明 argtypes 的话 64 位下 byref 会被截成 32 位，SendInput 直接返回 0（看起来像被拦）
user32.SendInput.restype = wt.UINT
user32.SendInput.argtypes = [wt.UINT, ctypes.c_void_p, ctypes.c_int]


def work_area(hwnd):
    mon = user32.MonitorFromWindow(hwnd, MONITOR_DEFAULTTONEAREST)
    mi = MONITORINFO()
    mi.cbSize = ctypes.sizeof(MONITORINFO)
    if not user32.GetMonitorInfoW(mon, ctypes.byref(mi)):
        return None
    dpi = user32.GetDpiForWindow(hwnd) or 96
    r = mi.rcWork
    return (
        round(r.left * 96 / dpi), round(r.top * 96 / dpi),
        round((r.right - r.left) * 96 / dpi), round((r.bottom - r.top) * 96 / dpi),
    )


# ---------------- 进程 ----------------

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


def log_tail(n=2500):
    if not os.path.exists(LOG):
        return ""
    with open(LOG, encoding="utf-8", errors="replace") as f:
        return f.read()[-n:]


def poll_timeline(pid, seconds, interval=0.4):
    """按时间采样该进程的「有标题可见窗口」集合，返回 [(t, {title: rect})]。

    时间线而不是单点快照：「弹出来了」「又自己关掉了」这两件事都必须看见，
    只在固定时刻截一次图，等于把生命周期砍成一张照片。
    """
    t0 = time.time()
    out = []
    while time.time() - t0 < seconds:
        snap = {}
        for hwnd, title in titled_windows(pid):
            snap[title] = dip_rect(hwnd)
        out.append((time.time() - t0, snap))
        time.sleep(interval)
    return out


def first_seen(timeline, title):
    for t, snap in timeline:
        if title in snap:
            return t, snap[title]
    return None, None


def last_seen(timeline, title):
    t_last = None
    for t, snap in timeline:
        if title in snap:
            t_last = t
    return t_last


# ---------------- 配置 ----------------

def base_cfg():
    with open(DEFAULTS, encoding="utf-8") as f:
        cfg = json.load(f)
    # 探针机全程无人的输入：空闲监视最先跳出来把休息挂起。
    # 这里把五条自动暂停来源全关掉，只留调度器本身的链路要验。
    cfg["idlePauseMin"] = 600
    cfg["dndPause"] = False
    cfg["fullscreenPause"] = False
    cfg["castMirrorPause"] = False
    cfg["castWirelessPause"] = False
    cfg.setdefault("appPause", {})["enabled"] = False
    # 别在探针里偷偷联网检查更新，也别在用户机器上放声音
    cfg["checkUpdateOnStart"] = False
    cfg.setdefault("sound", {})["enabled"] = False
    cfg.setdefault("supervisor", {})["enabled"] = True
    cfg["startMinimized"] = False
    return cfg


def write_cfg(cfg):
    os.makedirs(ISO, exist_ok=True)
    p = os.path.join(ISO, "settings.json")
    with open(p, "w", encoding="utf-8") as f:
        json.dump(cfg, f, ensure_ascii=False, indent=2)
    return p


def reset_log():
    if os.path.exists(LOG):
        os.remove(LOG)


def wait_main_window(pid, timeout=30.0):
    t0 = time.time()
    while time.time() - t0 < timeout:
        ws = titled_windows(pid)
        if ws:
            time.sleep(0.8)
            return ws
        time.sleep(0.3)
    return []


# ---------------- 三段 ----------------

def phase_break():
    print("\n[A] 短休息全流程：倒计时 45s，提前 20s")
    cfg = base_cfg()
    cfg["short"] = {
        "enabled": True, "mode": "countdown", "intervalMs": 45000,
        "durationMs": 20000, "notifyBeforeMs": 20000, "postponeMs": 120000, "slots": [],
    }
    cfg["long"]["enabled"] = False
    cfg["takeout"]["enabled"] = False
    cfg["customBreaks"] = []
    cfg["appearance"]["fullscreen"] = False
    cfg["appearance"]["widthPct"] = 95
    cfg["appearance"]["heightPct"] = 95
    cfg_path = write_cfg(cfg)
    reset_log()

    proc = start(cfg_path)
    try:
        if not wait_main_window(proc.pid):
            check(False, "A · 主窗口起来了", log_tail(1200))
            return
        # 45s 到点 + 20s 时长 + 建模/启动抖动；采样 95s 足够看到「弹 → 关」全生命周期
        tl = poll_timeline(proc.pid, 95)

        t_pre, r_pre = first_seen(tl, "即将休息")
        t_brk, r_brk = first_seen(tl, "休息一下")
        check(t_pre is not None, "A · 提前提示窗「即将休息」弹出来了",
              ("%.1fs" % t_pre) if t_pre is not None else "95s 内没出现")
        check(t_brk is not None, "A · 休息窗「休息一下」弹出来了",
              ("%.1fs" % t_brk) if t_brk is not None else "95s 内没出现")

        if t_pre is not None and t_brk is not None:
            gap = t_brk - t_pre
            # 提前量 20s：允许 ±7s（窗口建模 + 采样粒度 + 调度器 1s tick）
            check(13 <= gap <= 30, "A · 「提前 20s」真的提前了约 20s", "实测 %.1fs 后进入休息" % gap)
            check(t_pre <= 40, "A · 提前提示在首轮倒计时里就到点（40s 内）", "%.1fs" % t_pre)

        if r_brk is not None:
            wa = work_area_of_pid(proc.pid)
            if wa:
                _, _, ww, wh = wa
                # appearance 95% / 95%：宽度按工作区百分比，高度同理
                check(abs(r_brk[2] - ww * 0.95) <= TOL,
                      "A · 休息窗宽度 = 工作区的 95%", "窗口 %d / 工作区 %d" % (r_brk[2], ww))
                check(abs(r_brk[3] - wh * 0.95) <= TOL,
                      "A · 休息窗高度 = 工作区的 95%", "窗口 %d / 工作区 %d" % (r_brk[3], wh))

        t_gone = last_seen(tl, "休息一下")
        if t_brk is not None and t_gone is not None:
            alive = t_gone - t_brk
            # durationMs=20s：至少撑 15s（少于这个数说明被什么提前收掉了），
            # 且必须在 95s 采样窗内消失（不消失 = 关不掉）
            check(alive >= 15, "A · 休息窗至少撑满了休息时长", "存活 %.1fs" % alive)
            check(t_gone < 94, "A · 休息结束后窗自动关掉", "最后可见 %.1fs，之后消失" % t_gone)

        # 日志里 preBreak 自己算的尺寸必须与真实窗口一致 —— 「算得对」和「建得对」是两件事
        tail = log_tail(6000)
        got = parse_prebreak_log(tail)
        if not got:
            check(False, "A · 日志里有提前提示窗的尺寸记录")
        elif r_pre is not None:
            lw, lh, lx, ly = got
            check(abs(r_pre[2] - lw) <= TOL and abs(r_pre[3] - lh) <= TOL,
                  "A · 日志算出的尺寸 = 实际窗口尺寸",
                  "日志 %dx%d / 实际 %dx%d" % (lw, lh, r_pre[2], r_pre[3]))
            check(abs(r_pre[0] - lx) <= TOL and abs(r_pre[1] - ly) <= TOL,
                  "A · 日志算出的位置 = 实际窗口位置",
                  "日志 (%d,%d) / 实际 (%d,%d)" % (lx, ly, r_pre[0], r_pre[1]))
    finally:
        kill(proc)


def work_area_of_pid(pid):
    for hwnd, _ in titled_windows(pid):
        wa = work_area(hwnd)
        if wa:
            return wa
    return None


def parse_prebreak_log(text):
    """从 `[prebreak] 提前提示窗已弹出 custom=%v %dx%d @(%d,%d) title=%q` 里取尺寸与位置。"""
    import re
    m = re.search(r"提前提示窗已弹出 custom=\S+ (\d+)x(\d+) @\((-?\d+),(-?\d+)\)", text)
    if not m:
        return None
    return tuple(int(x) for x in m.groups())


def phase_takeout():
    print("\n[B] 外卖提醒：提前线落到「当前这一分钟」→ 预通知立刻弹，主窗在我定时刻弹")
    cfg = base_cfg()
    # 不安排自动休息，只看外卖这一条链路（预通知窗与休息预通知共用一扇窗，混着来分不清）
    cfg["short"]["enabled"] = False
    cfg["long"]["enabled"] = False
    cfg["customBreaks"] = []
    now = time.time()
    # 提前线判定是「now + 提前量 命中原定分钟」。把槽设在 now+60s 所在的那一分钟，
    # 提前量也给 60s：启动后 now+60 恰好落在槽的那一分钟里 → 预通知秒级弹出，
    # 原定时刻（≤60s 后）主窗再弹一次。两个时刻都是确定的，不用碰运气。
    lead = 60
    target = time.localtime(now + lead)
    slot_hour, slot_min = target.tm_hour, target.tm_min
    cfg["takeout"] = {
        "enabled": True, "leadSec": lead, "postponeMin": 2, "pauseAction": "postpone",
        "slots": [{
            "id": "e2e-takeout", "hour": slot_hour, "minute": slot_min,
            "repeat": "daily", "days": [], "enabled": True,
        }],
    }
    cfg["appearance"]["fullscreen"] = True  # 另一条分支：铺满工作区
    cfg_path = write_cfg(cfg)
    reset_log()

    proc = start(cfg_path)
    try:
        if not wait_main_window(proc.pid):
            check(False, "B · 主窗口起来了", log_tail(1200))
            return
        tl = poll_timeline(proc.pid, 80)

        t_pre, _ = first_seen(tl, "即将休息")
        check(t_pre is not None and t_pre <= 10, "B · 进入提前区间时弹出预通知小窗",
              ("%.1fs" % t_pre) if t_pre is not None else "80s 内没出现")

        t_tk, r_tk = first_seen(tl, "外卖提醒")
        check(t_tk is not None, "B · 原定时刻弹出外卖主窗",
              ("%.1fs" % t_tk) if t_tk is not None else "80s 内没出现（槽 %02d:%02d）" % (slot_hour, slot_min))
        if t_tk is not None:
            wa = work_area_of_pid(proc.pid)
            if wa:
                _, _, ww, wh = wa
                check(abs(r_tk[2] - ww) <= TOL and abs(r_tk[3] - wh) <= TOL,
                      "B · fullscreen=true 时外卖窗铺满工作区",
                      "窗口 %dx%d / 工作区 %dx%d" % (r_tk[2], r_tk[3], ww, wh))
            # 预通知必须在主窗起来时退场：两扇窗叠在一起会让人点错
            if t_pre is not None:
                at_fire = [snap for t, snap in tl if abs(t - t_tk) < 0.5]
                gone = all("即将休息" not in snap for snap in at_fire)
                check(gone, "B · 主窗弹出时预通知小窗已经收掉",
                      "同刻快照：" + (list(at_fire[0].keys()) and str(list(at_fire[0].keys())) or "空"))
        tail = log_tail(4000)
        check("触发提醒" in tail, "B · 日志记下了这次触发",
              ([ln for ln in tail.splitlines() if "触发提醒" in ln][-1:] or ["没有该行"])[0])
    finally:
        kill(proc)


# ---------------- 热键注入 ----------------

VK_F24 = 0x87
KEYEVENTF_KEYUP = 0x0002
INPUT_KEYBOARD = 1


class KEYBDINPUT(ctypes.Structure):
    _fields_ = [
        ("wVk", wt.WORD), ("wScan", wt.WORD), ("dwFlags", wt.DWORD),
        ("time", wt.DWORD), ("dwExtraInfo", ctypes.c_size_t),
    ]


class MOUSEINPUT(ctypes.Structure):
    _fields_ = [
        ("dx", wt.LONG), ("dy", wt.LONG), ("mouseData", wt.DWORD),
        ("dwFlags", wt.DWORD), ("time", wt.DWORD), ("dwExtraInfo", ctypes.c_size_t),
    ]


class HARDWAREINPUT(ctypes.Structure):
    _fields_ = [("uMsg", wt.DWORD), ("wParamL", wt.WORD), ("wParamH", wt.WORD)]


class _INPUTUNION(ctypes.Union):
    _fields_ = [("mi", MOUSEINPUT), ("ki", KEYBDINPUT), ("hi", HARDWAREINPUT)]


class INPUT(ctypes.Structure):
    _fields_ = [("type", wt.DWORD), ("u", _INPUTUNION)]


def press_hotkey():
    """发 Ctrl+Alt+Shift+F24。RegisterHotKey 是系统级投递，不需要窗口有焦点。

    用 SendInput 而不是 keybd_event：只有它能如实返回「插进去几条事件」。
    返回 False = 一条都没进去（沙箱 / 会话隔离拦了合成输入），
    这时「热键没生效」不能算产品失败，只能记为「无法测量」。
    """
    seq = [(0x11, 0), (0x12, 0), (0x10, 0), (VK_F24, 0),      # Ctrl Alt Shift F24 按下
           (VK_F24, KEYEVENTF_KEYUP), (0x10, KEYEVENTF_KEYUP),
           (0x12, KEYEVENTF_KEYUP), (0x11, KEYEVENTF_KEYUP)]  # 逆序松开
    arr = (INPUT * len(seq))()
    for i, (vk, flags) in enumerate(seq):
        arr[i].type = INPUT_KEYBOARD
        arr[i].u.ki = KEYBDINPUT(wVk=vk, wScan=0, dwFlags=flags, time=0, dwExtraInfo=0)
    n = user32.SendInput(len(seq), ctypes.byref(arr), ctypes.sizeof(INPUT))
    return n == len(seq)


def phase_hotkey():
    print("\n[C] 全局热键 → 立即休息")
    combo = "Ctrl+Alt+Shift+F24"
    cfg = base_cfg()
    # 倒计时拉得很远：这一轮的休息窗只可能来自热键，不会与调度线混淆
    cfg["short"]["intervalMs"] = 3600000
    cfg["short"]["notifyBeforeMs"] = 0
    cfg["long"]["enabled"] = False
    cfg["takeout"]["enabled"] = False
    cfg["customBreaks"] = []
    cfg["appearance"]["fullscreen"] = False
    cfg["shortcuts"] = {"postpone": "", "endEarly": "", "pause": "", "nextBreak": combo}
    cfg_path = write_cfg(cfg)
    reset_log()

    proc = start(cfg_path)
    try:
        if not wait_main_window(proc.pid):
            check(False, "C · 主窗口起来了", log_tail(1200))
            return
        time.sleep(1.2)  # 等热键注册完成
        tail = log_tail(2500)
        if "注册失败" in tail:
            check(False, "C · %s 注册成功（未被占用）" % combo,
                  [ln for ln in tail.splitlines() if "注册失败" in ln][-1])
            return
        if "无法识别" in tail:
            check(False, "C · %s 能被解析" % combo,
                  [ln for ln in tail.splitlines() if "无法识别" in ln][-1])
            return

        if not press_hotkey():
            skip("C · 热键触发休息窗", "SendInput 被环境拦下，注入不出去")
            return

        tl = poll_timeline(proc.pid, 12)
        t_brk, _ = first_seen(tl, "休息一下")
        check(t_brk is not None, "C · 按 %s 后弹出休息窗" % combo,
              ("%.1fs" % t_brk) if t_brk is not None else "12s 内没出现")
    finally:
        kill(proc)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--no-build", action="store_true")
    ap.add_argument("--keep", action="store_true", help="保留隔离目录")
    ap.add_argument("--only", default="", help="只跑某一段：A / B / C")
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
        print("找不到 %s（去掉 --no-build 先构建一次）" % EXE)
        return 2

    shutil.rmtree(ISO, ignore_errors=True)
    os.makedirs(ISO, exist_ok=True)

    todo = (args.only or "ABC").upper()
    try:
        if "A" in todo:
            phase_break()
        if "B" in todo:
            phase_takeout()
        if "C" in todo:
            phase_hotkey()
    finally:
        if not args.keep:
            shutil.rmtree(ISO, ignore_errors=True)

    print("\n结果：%d 项断言，失败 %d 项" % (checks, len(failures)))
    for f in failures:
        print("  - " + f)
    if skipped:
        print("无法验证 %d 项：" % len(skipped))
        for s in skipped:
            print("  · " + s)
    if os.path.exists(LOG):
        print("日志：%s" % LOG)
    return 0 if not failures else 1


if __name__ == "__main__":
    sys.exit(main())
