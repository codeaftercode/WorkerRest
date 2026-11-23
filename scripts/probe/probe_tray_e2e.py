#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""系统托盘端到端：真实 GUI 进程 → 托盘的宿主窗口 → 单击打开设置 / 右键弹出菜单。

托盘的文案逻辑（tooltip、菜单状态行、“约 X 分钟后”）有纯函数单测（tray_test.go），
但“点了到底有没有反应”永远只有真窗口能验。这里不去碰任务栏的像素，而是直接找到
Wails 给托盘建的那个**消息专属窗口**（HWND_MESSAGE 的子窗，EnumWindows 看不到它），
往里投递它自己注册的回调消息（WM_USER+1，lParam 带鼠标事件），看真实反应：

  A 对照组：不最小化启动 → 设置窗口一开始就可见
  B 主用例：startMinimized=true 启动 → 设置窗口不可见
            → 向托盘宿主投 WM_LBUTTONUP → 设置窗口出现（这就是“单击打开设置”）
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
EXE = os.path.join(ROOT, "build", "bin", "rest_winprobe.exe")
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


def make_cfg(start_minimized, short_on=False):
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
    if short_on:
        # C 段要验“立即休息 / 提前结束休息”这条链：短休息必须真开，
        # 且间隔拉到 30 分钟 —— 断言窗口内它不会自己弹出来干扰状态行。
        cfg["short"].update({"enabled": True, "mode": "countdown",
                             "intervalMs": 30 * 60 * 1000, "durationMs": 20000,
                             "notifyBeforeMs": 10000, "slots": []})
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


# ---------- 菜单内容读取与点击（2026-11-23 补：C 段） ----------
#
# 为什么不用鼠标点坐标：菜单项位置随文案长度变化（"暂停 30 分钟" ↔ "改到 30 分钟后结束"），
# 坐标会飘。也为什么不猜命令 ID：Wails 的 id 是 menu 遍历时的自增序号，
# 加一项菜单就全错位。这里走 **MN_GETHMENU（0x01E1）** 向 #32768 菜单窗口要真实的
# HMENU，再按**文案**查项 —— 这是 owner-draw 菜单的标准问法，也是唯一不依赖内部序号的路。
# 点动作则向托盘宿主投 WM_COMMAND（wParam = 命令 ID）：Wails 的 wndProc 就是拿
# `wParam & 0xffff` 去 menuMapping 里查，和真点一下走同一条分支。

MN_GETHMENU = 0x01E1
MF_BYPOSITION = 0x400
WM_COMMAND = 0x0111
WM_CANCELMODE = 0x001F

user32.SendMessageW.restype = ctypes.c_void_p
user32.SendMessageW.argtypes = [wt.HWND, wt.UINT, ctypes.c_size_t, ctypes.c_ssize_t]
user32.GetMenuItemCount.argtypes = [ctypes.c_void_p]
user32.GetMenuItemCount.restype = ctypes.c_int
user32.GetMenuStringW.argtypes = [ctypes.c_void_p, wt.UINT, ctypes.c_wchar_p, ctypes.c_int, wt.UINT]
user32.GetMenuStringW.restype = ctypes.c_int
user32.GetMenuItemID.argtypes = [ctypes.c_void_p, ctypes.c_int]
user32.GetMenuItemID.restype = wt.UINT
user32.GetSubMenu.argtypes = [ctypes.c_void_p, ctypes.c_int]
user32.GetSubMenu.restype = ctypes.c_void_p


def _walk_menu(hmenu):
    out = []
    if not hmenu:
        return out
    for i in range(user32.GetMenuItemCount(hmenu)):
        buf = ctypes.create_unicode_buffer(512)
        user32.GetMenuStringW(hmenu, i, buf, 512, MF_BYPOSITION)
        mid = int(user32.GetMenuItemID(hmenu, i))
        sub = user32.GetSubMenu(hmenu, i)
        node = {"pos": i, "text": buf.value,
                # 0xFFFFFFFF = 有子菜单的项；0 = 分隔符。两者都不能当命令 ID 投递
                "id": None if mid in (0, 0xFFFFFFFF) else mid}
        if sub:
            node["children"] = _walk_menu(sub)
        out.append(node)
    return out


def open_menu(pid, tray, timeout=6.0):
    """右键打开菜单并读出项表（不关闭）。返回 (menu_hwnd, items)。"""
    user32.PostMessageW(tray, WM_USER_SYSTRAY, 0, WM_RBUTTONUP)
    menus = wait_for(lambda: menu_windows(pid), timeout=timeout)
    if not menus:
        return 0, []
    return menus[0], _walk_menu(user32.SendMessageW(menus[0], MN_GETHMENU, 0, 0))


def close_menu(menu_hwnd):
    if menu_hwnd:
        user32.PostMessageW(menu_hwnd, WM_CANCELMODE, 0, 0)
        time.sleep(0.45)


def read_menu(pid, tray, tries=4):
    """开一次菜单、读完就关。每次都是新鲜文案（状态行每秒在变）。

    刚关掉的那扇菜单窗口还在销毁队列里时，`menu_windows` 可能先枚举到它 ——
    对已失效的 hwnd 发 MN_GETHMENU 会拿回 0，读出来是一张空表
    （2026-11-23 实测：连续读两次，第二次状态行是空串）。所以读到空表就重开。
    """
    for _ in range(tries):
        h, items = open_menu(pid, tray)
        close_menu(h)
        if items and any(it.get("text") for it in items):
            return items
        time.sleep(0.35)
    return []


def flat_items(items, prefix=""):
    for it in items:
        if it.get("text"):
            yield prefix + it["text"], it
        for c in it.get("children") or []:
            yield from flat_items([c], prefix + it["text"] + " / ")


def looks_like(name, want):
    """菜单文案匹配：忽略尾部的快捷键标注（"\\tCtrl+…"）与省略号差异。"""
    got = name.split("\t")[0].strip()
    return got == want or got.rstrip("…") == want.rstrip("…")


def find_menu_item(pid, tray, want):
    for path, it in flat_items(read_menu(pid, tray)):
        leaf = path.split(" / ")[-1]
        if looks_like(leaf, want):
            return it, path
    return None, ""


def click_menu_item(pid, tray, want):
    """按文案找到项并向托盘宿主投 WM_COMMAND。返回 (ok, detail)。"""
    it, path = find_menu_item(pid, tray, want)
    if it is None:
        return False, "菜单里没有 %r" % want
    if not it.get("id"):
        return False, "%r 没有命令 ID（是子菜单或分隔符）" % want
    user32.PostMessageW(tray, WM_COMMAND, it["id"], 0)
    return True, "id=%d（%s）" % (it["id"], path)


def status_line(pid, tray):
    """菜单第一项就是状态行。

    不要按 "状态：" 前缀找：文案前缀本身就不统一（见 tray_test.go）——
    默认分支是"下次短休息：约 10 分钟后"，只有休息 / 暂停 / 未安排这些分支
    才写"状态：…"。按位置取才是稳的。
    """
    items = read_menu(pid, tray)
    for it in items:
        if it.get("pos") == 0 and it.get("text"):
            return it["text"]
    for path, it in flat_items(items):
        if it.get("text", "").startswith("状态："):
            return it["text"]
    return ""


def wait_status(pid, tray, pred, timeout=10.0, step=0.6):
    t0 = time.time()
    last = ""
    while time.time() - t0 < timeout:
        last = status_line(pid, tray)
        if last and pred(last):
            return last
        time.sleep(step)
    return last



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
        # 先确认窗口对象存在（只是不可见）——“不存在”和“藏着”是两回事
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

    # ---------- C 菜单项动作：点下去世界真的变了 ----------
    print("\n[C] 托盘菜单项的真实动作（按文案找项，不猜 id 顺序）")
    cfg_path, app_name = make_cfg(True, short_on=True)
    proc = start(cfg_path)
    try:
        hwnd = wait_for(lambda: find_titled(proc.pid, app_name))
        host = wait_for(lambda: message_only_windows(proc.pid))
        if not hwnd or not host:
            check(False, "C · 托盘与设置窗口就绪")
            return 1
        tray = host[0]
        time.sleep(2.0)  # 等首拍 refreshTray 把状态行写完

        items = read_menu(proc.pid, tray)
        texts = [p for p, _ in flat_items(items)]
        for want in ("立即休息", "重置休息周期", "暂停休息", "设置…", "开机自启动", "退出"):
            check(want in texts, "C · 菜单项在：%s" % want)
        timed = [p for p, _ in flat_items(items) if " / " in p]
        check(len(timed) >= 4, "C · 「暂停一段时间」下四项都在", timed)

        # 第一次读菜单可能撞上"窗口刚建、文字还没填好"，用 wait_status 等它非空
        st = wait_status(proc.pid, tray, lambda s: True, 6)
        check("下次短休息" in st, "C · 初始状态行报下次短休息", repr(st))

        # 立即休息 → 休息中
        ok, detail = click_menu_item(proc.pid, tray, "立即休息")
        check(ok, "C · 点到「立即休息」", detail)
        st = wait_status(proc.pid, tray, lambda s: "休息中" in s, 10)
        check("休息中" in st, "C · 立即休息后状态行变「休息中」", st)

        # 同一项文案翻成「提前结束休息」→ 点它结束
        it, path = find_menu_item(proc.pid, tray, "提前结束休息")
        check(it is not None, "C · 休息中同一项翻成「提前结束休息」", path or "没翻")
        if it is not None:
            ok, detail = click_menu_item(proc.pid, tray, "提前结束休息")
            check(ok, "C · 点到「提前结束休息」", detail)
            st = wait_status(proc.pid, tray, lambda s: "休息中" not in s, 10)
            check("休息中" not in st, "C · 提前结束后状态行离开休息中", st)

        # 子菜单「暂停 30 分钟」→ 定时暂停，状态行给剩余时长。
        # 必须趁"还没暂停"时点：暂停中子菜单标签会整体翻成"改到 30 分钟后结束"，
        # 那时再按"暂停 30 分钟"找当然找不到（旧版探针就是这么红的）。
        ok, detail = click_menu_item(proc.pid, tray, "暂停 30 分钟")
        check(ok, "C · 点到子菜单「暂停 30 分钟」", detail)
        st = wait_status(proc.pid, tray, lambda s: "暂停中" in s, 10)
        check("还有 30 分钟" in st, "C · 定时暂停后状态行报剩余 30 分钟", st)
        it, path = find_menu_item(proc.pid, tray, "改到 30 分钟后结束")
        check(it is not None, "C · 定时暂停中子菜单翻成「改到 30 分钟后结束」", path or "没翻")

        # 恢复（定时暂停中同一项叫「恢复休息」）
        ok, detail = click_menu_item(proc.pid, tray, "恢复休息")
        check(ok, "C · 点到「恢复休息」", detail)
        st = wait_status(proc.pid, tray, lambda s: "暂停" not in s, 12)
        check("暂停" not in st, "C · 恢复后状态行不再提暂停", st)

        # 一键「暂停休息」→ 已暂停 + 同一项翻成「恢复休息」
        ok, detail = click_menu_item(proc.pid, tray, "暂停休息")
        check(ok, "C · 点到「暂停休息」", detail)
        st = wait_status(proc.pid, tray, lambda s: "已暂停" in s, 10)
        check("已暂停" in st, "C · 暂停后状态行变「已暂停」", st)
        it, path = find_menu_item(proc.pid, tray, "恢复休息")
        check(it is not None, "C · 暂停中同一项翻成「恢复休息」", path or "没翻")
        ok, detail = click_menu_item(proc.pid, tray, "恢复休息")
        check(ok, "C · 再点「恢复休息」回到工作", detail)

        # 设置… → 窗口出现（启动时是隐藏的，所以这条同时证明“唤出”而不是“本来就在”）
        check(not user32.IsWindowVisible(hwnd), "C · 前面几步都没把设置窗口带出来",
              "visible=%d" % user32.IsWindowVisible(hwnd))
        ok, detail = click_menu_item(proc.pid, tray, "设置…")
        check(ok, "C · 点到「设置…」", detail)
        shown = wait_for(lambda: user32.IsWindowVisible(hwnd), timeout=6.0)
        check(bool(shown), "C · 「设置…」真的把设置窗口唤出来了")

        # 退出（最后一步：它会结束进程）
        ok, detail = click_menu_item(proc.pid, tray, "退出")
        check(ok, "C · 点到「退出」", detail)
        try:
            rc = proc.wait(timeout=8)
            # 退出码不做断言：这里要证明的是“真的自己退出了”，不是某个码
            check(True, "C · 「退出」后进程自行结束", "rc=%d" % rc)
        except subprocess.TimeoutExpired:
            check(False, "C · 「退出」后进程自行结束", "8 秒内没退出")
    finally:
        kill(proc)

    print("\n结果：%d 项断言，失败 %d 项" % (checks, len(failures)))
    for f in failures:
        print("  - " + f)
    print("日志：%s" % LOG)
    return 0 if not failures else 1


if __name__ == "__main__":
    sys.exit(main())
