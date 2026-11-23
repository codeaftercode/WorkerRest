#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""一次性排查：提醒窗到底"没画出来"，还是"画了但屏幕 BitBlt 抓不到"。

**2026-11-23 实测结论（留档，别再走这条弯路）**：
  窗口 78,50 2964x1884、可见=1、亲和性=0；两条路径的品红都是 0.00% ——
  `BitBlt(GetDC(NULL))` 抓到的是**底下的 WorkBuddy 界面**，
  `PrintWindow(PW_RENDERFULLCONTENT)` 抓到的是**整块黑图**。
  两者都不能证明"窗口没画出来"：Wails 把提醒窗建成 BackgroundTypeTranslucent，
  窗口带 `WS_EX_NOREDIRECTIONBITMAP`，本来就没有 DWM 重定向表面，GDI 侧拿不到像素。
  所以 probe_capture_guard.py 的品红判据在**任何** WebView2 + Translucent 窗口上都恒红，
  已在该探针里改成"无法测量 + 人工验证步骤"。

保留本脚本的理由：下次再有人想用 PrintWindow 抓 WebView2 内容时，这里有现成的反例。

2026-11-23 probe_capture_guard 的对照组红了（开关=关时窗口应该被记成一片品红，
实测 0.00%），实验组的"0%"因此毫无意义。两种可能必须分开：

  A. 窗口真的没渲染 —— 那就是产品缺陷，得查 WebView2/合成；
  B. 窗口渲染了，但屏幕 DC 的 BitBlt 抓不到 DirectComposition 表面 ——
     那是抓图链路的口径问题，探针该改判据或降级成"无法测量"。

判别方法：同一次运行里两条抓图路径都走一遍——
  ① BitBlt(GetDC(NULL))   —— 截图工具走的路，也是本探针的判据；
  ② PrintWindow(hwnd, PW_RENDERFULLCONTENT) —— 直接问窗口自己"你画了什么"。
② 有品红而 ① 没有 = B；两条都没有 = A。

用法：python scripts/oneoff/printwindow_check.py
"""
import ctypes
import ctypes.wintypes as wt
import os
import shutil
import subprocess
import sys
import time

HERE = os.path.dirname(os.path.abspath(__file__))
PROBE = os.path.join(os.path.dirname(HERE), "probe")
sys.path.insert(0, PROBE)

import probe_capture_guard as P  # noqa: E402

user32 = P.user32
gdi32 = P.gdi32


def grab_window(hwnd, w, h):
    """PrintWindow(PW_RENDERFULLCONTENT) 抓窗口自身内容，返回 BGRA bytes。"""
    hdc = user32.GetDC(0)
    mem = gdi32.CreateCompatibleDC(hdc)
    bmp = gdi32.CreateCompatibleBitmap(hdc, w, h)
    old = gdi32.SelectObject(mem, bmp)
    ok = user32.PrintWindow(hwnd, mem, 2)  # PW_RENDERFULLCONTENT
    bi = P.BITMAPINFO()
    bi.bmiHeader.biSize = ctypes.sizeof(P.BITMAPINFOHEADER)
    bi.bmiHeader.biWidth = w
    bi.bmiHeader.biHeight = -h
    bi.bmiHeader.biPlanes = 1
    bi.bmiHeader.biBitCount = 32
    bi.bmiHeader.biCompression = 0
    buf = ctypes.create_string_buffer(w * h * 4)
    got = gdi32.GetDIBits(mem, bmp, 0, h, buf, ctypes.byref(bi), 0)
    gdi32.SelectObject(mem, old)
    gdi32.DeleteObject(bmp)
    gdi32.DeleteDC(mem)
    user32.ReleaseDC(0, hdc)
    return ok, (buf.raw if got else b"")


def main():
    label, title, timeout = P.SCENES["short"]
    P.kill_all()
    os.makedirs(P.ISO, exist_ok=True)
    cfg_path = P.make_cfg(False, "short")  # 对照组口径：共享时隐藏 = 关
    env = dict(os.environ)
    env["WR_SETTINGS_PATH"] = cfg_path
    env["WR_LOG_FILE"] = P.LOG
    logf = open(P.LOG, "a", encoding="utf-8")
    proc = subprocess.Popen([P.EXE], cwd=P.ROOT, env=env, stdout=logf, stderr=subprocess.STDOUT)
    try:
        print("等窗口出现（%s，最多 %.0fs）…" % (title, timeout))
        hwnd, rect = P.find_window(title, timeout=timeout)
        if not hwnd:
            print("窗口没出现 —— 这条用例本身就没跑起来，先查配置")
            return 2
        x, y, w, h = rect
        print("窗口 %d,%d %dx%d  亲和性=%s  可见=%s" % (
            x, y, w, h, P.affinity_of(hwnd), user32.IsWindowVisible(hwnd)))
        time.sleep(3.0)

        # 先确认屏幕 DC 抓到的就是桌面（对照组上次抓到的正是 WorkBuddy 界面）
        vx, vy, vw, vh = P.virtual_screen()
        screen = P.grab(vx, vy, vw, vh)
        crop = P.grab_crop(screen, vw, vx, vy, x, y, w, h)
        m_screen = P.magenta_ratio(crop, w, h)
        P.save_bmp(os.path.join(P.TEMP, "wr_check_screen.bmp"), crop, w, h)
        print("① 屏幕 BitBlt  ：品红 %.2f%%（窗口矩形）" % (m_screen * 100))

        ok, raw = grab_window(hwnd, w, h)
        if not ok or not raw:
            print("② PrintWindow 失败（ok=%s, %d 字节）" % (ok, len(raw)))
            return 1
        m_print = P.magenta_ratio(raw, w, h)
        P.save_bmp(os.path.join(P.TEMP, "wr_check_printwindow.bmp"), raw, w, h)
        print("② PrintWindow ：品红 %.2f%%（窗口自身）" % (m_print * 100))

        print()
        if m_print >= 0.4 and m_screen < 0.05:
            print("结论：**窗口画了，屏幕 BitBlt 抓不到**（DirectComposition 表面）。")
            print("      → 探针判据要改：屏幕口径无法建立“看得见”的基线，")
            print("        应改判“无法测量”，或换 PrintWindow 作为可见性判据。")
        elif m_print < 0.05:
            print("结论：**窗口真的没画出来**（PrintWindow 也是空的）→ 产品缺陷方向。")
            print("      → 下一步查 WebView2 是否加载、appearance.color 是否到了页面。")
        else:
            print("结论：两条路径都有品红但比例不高，需人工看图。")
        print("快照：%s\\wr_check_*.bmp" % P.TEMP)
        return 0
    finally:
        try:
            proc.terminate()
        except Exception:
            pass
        P.kill_all()


if __name__ == "__main__":
    sys.exit(main())
