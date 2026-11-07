#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""共享屏幕时隐藏：真机验证“提醒窗口到底能不能被捕获到”。

为什么必须有这个探针：Wails v3 的 `ContentProtectionEnabled` 只对顶层 HWND 调
SetWindowDisplayAffinity，而本项目的提醒窗口是半透明无边框窗、内容由 WebView2
的渲染进程合成。只设顶层时 GetWindowDisplayAffinity 回读是 0x11（看着“成功了”），
可捕获结果里照样有整块内容 —— 这个自相矛盾只有真抓一次图才看得见。

做法（单进程、无外部依赖）：
  ① 起一个隔离配置的实例：一次只让**一类**提醒在 6 秒后弹出（其余全关），
     startMinimized 让设置窗口不出现；窗口底色改成品红 #ff00ff —— 判据是
     “窗口矩形里有多少像素偏品红”，不看“和基线差多少”（后台桌面会自己动，
     差异法实测三次里两次误报）。
  ② 等该类窗口出现，再抓整屏、裁出窗口矩形，数品红占比
  ③ 对照组（只跑短休息）：
     - hideOnShare=false：品红占比必须很高 → 证明抓图链路**确实能看见**提醒窗口
       （否则“看不见”这个结论毫无意义，抓图本身坏了也会显示成通过）
  ④ 实验组（hideOnShare=true）：五类窗口逐个跑一遍，品红占比必须都接近 0 ——
     short / long / custom 走同一段建窗代码（休息窗口），prebreak 是另一段
     （提前提示小窗），takeout 也是另一段（外卖提醒），三条路径必须各自验证，
     不能拿一类的结论推另一类。
  ⑤ 顺带记录顶层 / 后代窗口回读到的亲和性（跨进程读不到属正常，写进报告便于排障）

用屏幕 DC 的 BitBlt 抓图（不依赖 GDI+ / PIL）：这是截图类工具与“共享窗口”预览
最常走的一条路径，也会尊重 WDA 亲和性。

前置预检（2026-11 加）：跑之前先抓一块桌面，判断“到底有没有画面可抓”。远程会话、
锁屏、部分虚拟显卡下 BitBlt 会拿到一整块黑图，这时对照组必然数不到品红 —— 没有
预检的话，探针会把“我看不见屏幕”报成“捕获保护没生效”，结论正好反了。预检不过
直接返回退出码 3（= 无法测量），与“失败”区分开。

隔离：WR_SETTINGS_PATH 指向 %TEMP% 的副本，绝不碰用户真实配置。
用法：python scripts/probe_capture_guard.py [--keep] [--no-build]
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
EXE = os.path.join(ROOT, "build", "bin", "rest_capguard.exe")
TEMP = os.environ.get("TEMP", ROOT)
ISO = os.path.join(TEMP, "wr_capguard_iso")
LOG = os.path.join(TEMP, "wr_capguard.log")
REPORT = os.path.join(TEMP, "wr_capguard_report.txt")

user32 = ctypes.windll.user32
gdi32 = ctypes.windll.gdi32
user32.SetProcessDPIAware()

SRCCOPY = 0x00CC0020
WDA_EXCLUDEFROMCAPTURE = 0x11
WDA_MONITOR = 0x01

# 判据：窗口底色被探针配置成纯品红（#ff00ff），数窗口矩形内“明显偏品红”的像素占比。
#
# 早先比的是“与窗口出现前的基线之差”，结果不可靠：窗口背后是用户自己的桌面，
# 两次抓图之间后台页面自己滚动/重绘，差异能凭空冲到 6%，甚至把“整屏换了内容”
# 当成 99% 泄漏（实测三次里两次误报红）。品红占比不需要静态背景 —— 桌面不是品红，
# 提醒窗口一出现就是大面积品红，有没有被排除一目了然。
MAGENTA_VISIBLE = 0.40   # 看得见时整窗都被染色
MAGENTA_HIDDEN = 0.05    # 被排除时只剩背后桌面，偶尔有一点品红也不会超过这个量级
MAGENTA_MIN_RG = 25      # R、B 都比 G 高这么多才算“偏品红”

# 场景表：键 → (中文名, 窗口标题, 等窗口出现的超时秒数)
# 短 / 长 / 自定义三种休息的标题都是“休息一下”（同一个建窗函数），所以一次只放一类出来，
# 否则分不清抓到的是哪一种。外卖按“时:分”触发，得等最多一分钟。
SCENES = {
    "short":    ("短休息",     "休息一下", 40),
    "long":     ("长休息",     "休息一下", 40),
    "custom":   ("自定义提醒", "休息一下", 40),
    "prebreak": ("提前提示窗", "即将休息", 50),
    "takeout":  ("外卖提醒",   "外卖提醒", 100),
}

failures = []
checks = 0
DUMP = False


def check(ok, label, detail=""):
    global checks
    checks += 1
    print(("  ok   " if ok else "  FAIL ") + label + (("  | " + str(detail)) if detail else ""))
    if not ok:
        failures.append(label + (("  | " + str(detail)) if detail else ""))


class BITMAPINFOHEADER(ctypes.Structure):
    _fields_ = [
        ("biSize", wt.DWORD), ("biWidth", wt.LONG), ("biHeight", wt.LONG),
        ("biPlanes", wt.WORD), ("biBitCount", wt.WORD), ("biCompression", wt.DWORD),
        ("biSizeImage", wt.DWORD), ("biXPelsPerMeter", wt.LONG),
        ("biYPelsPerMeter", wt.LONG), ("biClrUsed", wt.DWORD), ("biClrImportant", wt.DWORD),
    ]


class BITMAPINFO(ctypes.Structure):
    _fields_ = [("bmiHeader", BITMAPINFOHEADER), ("bmiColors", wt.DWORD * 3)]


def virtual_screen():
    x = user32.GetSystemMetrics(76)  # SM_XVIRTUALSCREEN
    y = user32.GetSystemMetrics(77)
    w = user32.GetSystemMetrics(78)
    h = user32.GetSystemMetrics(79)
    return x, y, w, h


def grab(x, y, w, h):
    """抓屏幕上一块矩形，返回 bytes（BGRA，行序自上而下）。"""
    hdc = user32.GetDC(0)
    mem = gdi32.CreateCompatibleDC(hdc)
    bmp = gdi32.CreateCompatibleBitmap(hdc, w, h)
    old = gdi32.SelectObject(mem, bmp)
    gdi32.BitBlt(mem, 0, 0, w, h, hdc, x, y, SRCCOPY)
    bi = BITMAPINFO()
    bi.bmiHeader.biSize = ctypes.sizeof(BITMAPINFOHEADER)
    bi.bmiHeader.biWidth = w
    bi.bmiHeader.biHeight = -h  # 负 = 自上而下
    bi.bmiHeader.biPlanes = 1
    bi.bmiHeader.biBitCount = 32
    bi.bmiHeader.biCompression = 0  # BI_RGB
    buf = ctypes.create_string_buffer(w * h * 4)
    got = gdi32.GetDIBits(mem, bmp, 0, h, buf, ctypes.byref(bi), 0)
    gdi32.SelectObject(mem, old)
    gdi32.DeleteObject(bmp)
    gdi32.DeleteDC(mem)
    user32.ReleaseDC(0, hdc)
    if got == 0:
        raise RuntimeError("GetDIBits 失败")
    return buf.raw


def diff_ratio(a, b, w, h):
    """两块同尺寸画面的“明显不同”像素占比（通道差之和 > 24 算不同）。

    抽样看（每 3 像素取 1 个）：窗口动辄一百多万像素，纯 Python 逐像素跑要好几秒，
    而这里只判“整块内容在不在”，20% / 1.5% 的阈值对抽样完全不敏感。
    """
    if len(a) != len(b) or not a:
        return 1.0
    step = 3
    diff = 0
    total = 0
    for row in range(0, h, step):
        base = row * w * 4
        for col in range(0, w, step):
            i = base + col * 4
            d = abs(a[i] - b[i]) + abs(a[i + 1] - b[i + 1]) + abs(a[i + 2] - b[i + 2])
            total += 1
            if d > 24:
                diff += 1
    return diff / total if total else 1.0


def magenta_ratio(data, w, h):
    """窗口矩形内“明显偏品红”的像素占比（抽样，够用且快）。"""
    if not data:
        return 0.0
    step = 3
    hit = 0
    total = 0
    for row in range(0, h, step):
        base = row * w * 4
        for col in range(0, w, step):
            i = base + col * 4
            b, g, r = data[i], data[i + 1], data[i + 2]
            total += 1
            if r - g >= MAGENTA_MIN_RG and b - g >= MAGENTA_MIN_RG:
                hit += 1
    return hit / total if total else 0.0


def desktop_lit_ratio():
    """抓一块桌面，返回“非黑像素占比”—— 判断这台机器到底有没有画面可抓。

    远程会话 / 锁屏 / 部分虚拟显卡下 BitBlt 会返回整块黑图（实测过：mean=0、
    非黑 0.0%，而 qwinsta 还显示 Console）。这种环境下“窗口没被抓到”与“屏幕
    本身就看不见”无法区分，必须先把结论降级成“无法测量”，否则会把环境限制
    报成产品缺陷。
    """
    vx, vy, vw, vh = virtual_screen()
    w, h = max(1, min(vw, 480)), max(1, min(vh, 360))
    data = grab(vx, vy, w, h)
    step = 7
    lit = total = 0
    for row in range(0, h, step):
        base = row * w * 4
        for col in range(0, w, step):
            i = base + col * 4
            total += 1
            if data[i] > 16 or data[i + 1] > 16 or data[i + 2] > 16:
                lit += 1
    return (lit / total) if total else 0.0


def find_window(title, timeout=30.0):
    """按标题找可见窗口，返回 (hwnd, rect)。"""
    found = []

    @ctypes.WINFUNCTYPE(wt.BOOL, wt.HWND, wt.LPARAM)
    def cb(hwnd, _):
        if not user32.IsWindowVisible(hwnd):
            return True
        n = user32.GetWindowTextLengthW(hwnd)
        if n <= 0:
            return True
        buf = ctypes.create_unicode_buffer(n + 1)
        user32.GetWindowTextW(hwnd, buf, n + 1)
        if buf.value.strip() == title:
            r = wt.RECT()
            user32.GetWindowRect(hwnd, ctypes.byref(r))
            if r.right - r.left > 50 and r.bottom - r.top > 50:
                found.append((hwnd, (r.left, r.top, r.right - r.left, r.bottom - r.top)))
        return True

    deadline = time.time() + timeout
    while time.time() < deadline:
        found.clear()
        user32.EnumWindows(cb, 0)
        if found:
            return found[0]
        time.sleep(0.2)
    return None, None


def affinity_of(hwnd):
    aff = wt.DWORD(0)
    ok = user32.GetWindowDisplayAffinity(wt.HWND(hwnd), ctypes.byref(aff))
    return aff.value if ok else None


def child_affinities(hwnd):
    """枚举所有后代窗口的亲和性（用于排障：真正上屏的是 Chromium 的子窗口）。"""
    out = []

    @ctypes.WINFUNCTYPE(wt.BOOL, wt.HWND, wt.LPARAM)
    def cb(child, _):
        cls = ctypes.create_unicode_buffer(128)
        user32.GetClassNameW(child, cls, 128)
        out.append((cls.value, affinity_of(child)))
        return True

    user32.EnumChildWindows(wt.HWND(hwnd), cb, 0)
    return out


def build():
    print("构建 GUI 产物…")
    env = dict(os.environ)
    env.setdefault("GOCACHE", os.path.join(os.environ.get("LOCALAPPDATA", TEMP), "go-build"))
    b = subprocess.run(["go", "build", "-o", EXE, "-ldflags", "-s -w", "./cmd/workerrest"],
                       cwd=ROOT, capture_output=True, text=True,
                       encoding="utf-8", errors="replace", env=env)
    if b.returncode != 0:
        print("构建失败:\n" + (b.stderr or b.stdout))
        sys.exit(2)


def make_cfg(hide_on_share, scene="short"):
    with open(os.path.join(ROOT, "internal", "settings", "default_settings.json"), "r", encoding="utf-8") as f:
        cfg = json.load(f)
    # 先全关：一次只放一类提醒出来，窗口矩形里不会混进别的窗口，抓图才有意义
    off = {"enabled": False, "mode": "countdown", "intervalMs": 1800000,
           "durationMs": 300000, "notifyBeforeMs": 0, "postponeMs": 60000, "slots": []}
    cfg["short"] = dict(off)
    cfg["long"] = dict(off)
    cfg["customBreaks"] = []
    cfg["takeout"] = dict(cfg.get("takeout") or {}, enabled=False, leadSec=0, slots=[])

    # 6 秒后弹、持续 5 分钟：够等窗口出现、够抓图，又不会自己消失
    on = {"enabled": True, "mode": "countdown", "intervalMs": 6000,
          "durationMs": 300000, "notifyBeforeMs": 0, "postponeMs": 60000, "slots": []}
    if scene == "short":
        cfg["short"] = dict(on)
    elif scene == "long":
        cfg["long"] = dict(on)
    elif scene == "custom":
        cfg["customBreaks"] = [{"id": "cb-probe", "name": "探针提醒", "config": dict(on)}]
    elif scene == "prebreak":
        # 提前提示窗：14 秒后开始休息、提前 6 秒提示 —— 窗口 8 秒左右出现，
        # 到 14 秒才切走，留得下“等亲和性 2.8 秒 + 抓图”的余量
        pb = dict(on)
        pb["intervalMs"] = 14000
        pb["notifyBeforeMs"] = 6000
        cfg["short"] = pb
    elif scene == "takeout":
        # 外卖只在“时:分”命中时触发（isTimeSlotDue 精确到分钟），所以挑未来的
        # 下一分钟：+70 秒保证落在下一分钟里，整分钟内随时命中，最多等 60 秒
        t = time.localtime(time.time() + 70)
        cfg["takeout"] = {
            "enabled": True, "leadSec": 0, "postponeMin": 10, "pauseAction": "postpone",
            "slots": [{"id": "probe", "hour": t.tm_hour, "minute": t.tm_min,
                       "repeat": "daily", "days": [], "enabled": True}],
        }
    else:
        raise SystemExit("未知场景：" + scene)

    cfg["appPause"] = {"enabled": False, "apps": []}
    cfg["idlePauseMin"] = 0
    cfg["dndPause"] = False
    cfg["fullscreenPause"] = False
    # 投屏检测读的是显示拓扑：跑探针的机器如果正好在复制模式（= 正在镜像），
    # 调度器会一启动就“暂停（投屏）”，后面所有“到点必弹”的断言都会踩空，
    # 而失败信息指向的是投屏、跟被测的捕获保护毫无关系。两条判据都必须显式关掉。
    cfg["castWirelessPause"] = False
    cfg["castMirrorPause"] = False
    cfg["startMinimized"] = True
    cfg["displayIds"] = []
    cfg["sound"] = dict(cfg.get("sound") or {}, enabled=False)
    cfg["supervisor"] = dict(cfg.get("supervisor") or {}, enabled=False)
    ap = dict(cfg.get("appearance") or {})
    # 品红底色 + 近乎不透明：让“窗在不在”变成一个有确定色彩特征的判断题，
    # 不依赖背后桌面是什么（见文件顶部说明）
    ap["color"] = "#ff00ff"
    ap["transparency"] = 0.02
    ap["hideOnShare"] = bool(hide_on_share)
    ap["alwaysOnTop"] = True
    cfg["appearance"] = ap
    path = os.path.join(ISO, "settings.json")
    with open(path, "w", encoding="utf-8") as f:
        json.dump(cfg, f, ensure_ascii=False, indent=2)
    return path


def kill_all():
    subprocess.run(["taskkill", "/IM", os.path.basename(EXE), "/T", "/F"],
                   capture_output=True)


def run_case(hide_on_share, scene="short"):
    """跑一组，返回 (是否成功, 说明, 诊断信息)。"""
    label, title, timeout = SCENES[scene]
    case = "开" if hide_on_share else "关"
    print(f"\n—— 场景：共享屏幕时隐藏 = {case} · {label} ——")
    kill_all()
    cfg_path = make_cfg(hide_on_share, scene)
    env = dict(os.environ)
    env["WR_SETTINGS_PATH"] = cfg_path
    env["WR_LOG_FILE"] = LOG
    logf = open(LOG, "a", encoding="utf-8")
    proc = subprocess.Popen([EXE], cwd=ROOT, env=env, stdout=logf, stderr=subprocess.STDOUT)
    try:
        # 基线：桌面 + 托盘（提醒窗口还没出来）
        time.sleep(1.2)
        vx, vy, vw, vh = virtual_screen()
        base = grab(vx, vy, vw, vh)
        hwnd, rect = find_window(title, timeout=timeout)
        if not hwnd:
            return False, f"{label}窗口没出现（配置未触发？）", {}
        # 等 Wails 把亲和性设完（显示后异步设一次）
        time.sleep(2.8)
        x, y, w, h = rect
        # 裁到虚拟屏内，避免抓出界
        x0, y0 = max(x, vx), max(y, vy)
        x1, y1 = min(x + w, vx + vw), min(y + h, vy + vh)
        cw, ch = x1 - x0, y1 - y0
        if cw < 50 or ch < 50:
            return False, f"{label}窗口矩形不在屏幕内", {}
        now = grab(vx, vy, vw, vh)
        base_crop = grab_crop(base, vw, vx, vy, x0, y0, cw, ch)
        now_crop = grab_crop(now, vw, vx, vy, x0, y0, cw, ch)
        ratio = diff_ratio(base_crop, now_crop, cw, ch)
        magenta = magenta_ratio(now_crop, cw, ch)
        if DUMP:
            tag = ("on" if hide_on_share else "off") + "_" + scene
            save_bmp(os.path.join(TEMP, f"wr_cap_{tag}_before.bmp"), base_crop, cw, ch)
            save_bmp(os.path.join(TEMP, f"wr_cap_{tag}_after.bmp"), now_crop, cw, ch)
            print(f"  快照已存 {TEMP}\\wr_cap_{tag}_*.bmp")
        diag = {
            "scene": scene,
            "title": title,
            "hwnd": hex(hwnd),
            "rect": (x, y, w, h),
            "top_affinity": affinity_of(hwnd),
            "children": child_affinities(hwnd),
            "diff": round(ratio, 4),
            "magenta": round(magenta, 4),
        }
        print(f"  窗口 {x},{y} {w}x{h}  顶层亲和性={diag['top_affinity']}  "
              f"品红占比={magenta:.2%}（差异 {ratio:.2%}，仅供参考）")
        for cls, aff in diag["children"]:
            print(f"    子窗口 {cls:<28} 亲和性={aff}")
        return True, "", diag
    finally:
        try:
            proc.terminate()
        except Exception:
            pass
        kill_all()
        try:
            proc.wait(timeout=10)
        except Exception:
            pass
        logf.close()


def grab_crop(full, full_w, vx, vy, x, y, w, h):
    """从整屏快照里裁一块（快照行序自上而下，4 字节/像素）。"""
    out = bytearray()
    for row in range(h):
        off = ((y - vy + row) * full_w + (x - vx)) * 4
        out += full[off:off + w * 4]
    return bytes(out)


def save_bmp(path, data, w, h):
    """把 BGRA 快照写成 32 位 BMP 24 位——排障时直接看图，比看百分比快得多。"""
    row_size = ((w * 3 + 3) // 4) * 4
    pad = row_size - w * 3
    pixels = bytearray()
    for row in range(h - 1, -1, -1):  # BMP 行序自下而上
        off = row * w * 4
        for col in range(w):
            i = off + col * 4
            pixels += bytes((data[i + 2], data[i + 1], data[i]))  # BGR
        pixels += b"\x00" * pad
    info = ctypes.create_string_buffer(40)
    hdr = BITMAPINFOHEADER()
    hdr.biSize = 40
    hdr.biWidth = w
    hdr.biHeight = h
    hdr.biPlanes = 1
    hdr.biBitCount = 24
    hdr.biSizeImage = len(pixels)
    ctypes.memmove(info, ctypes.byref(hdr), 40)
    file_size = 14 + 40 + len(pixels)
    with open(path, "wb") as f:
        f.write(b"BM")
        f.write(int(file_size).to_bytes(4, "little"))
        f.write(b"\x00\x00\x00\x00")
        f.write((54).to_bytes(4, "little"))
        f.write(info.raw)
        f.write(bytes(pixels))


def main():
    try:
        sys.stdout.reconfigure(encoding="utf-8", errors="replace")
    except Exception:
        pass
    ap = argparse.ArgumentParser()
    ap.add_argument("--no-build", action="store_true")
    ap.add_argument("--keep", action="store_true", help="保留隔离目录与日志")
    ap.add_argument("--dump", action="store_true", help="把抓到的画面导出为 BMP，方便肉眼看")
    ap.add_argument("--only", default="",
                    help="只跑指定场景（逗号分隔）：" + ",".join(SCENES))
    args = ap.parse_args()
    if args.only:
        scenes = [x.strip() for x in args.only.split(",") if x.strip()]
        bad = [x for x in scenes if x not in SCENES]
        if bad:
            raise SystemExit("未知场景：" + ",".join(bad) + "；可选：" + ",".join(SCENES))
    else:
        scenes = list(SCENES)
    global DUMP
    DUMP = args.dump

    if not args.no_build:
        build()

    shutil.rmtree(ISO, ignore_errors=True)
    os.makedirs(ISO, exist_ok=True)
    if os.path.exists(LOG):
        os.remove(LOG)

    lines = []
    print("共享屏幕时隐藏：真机捕获验证")

    # 预检：屏幕抓不到东西就别往下跑 —— 否则对照组必然“失败”，把环境限制写成缺陷
    try:
        lit = desktop_lit_ratio()
    except Exception as e:
        lit = 0.0
        print("  桌面预检抓图异常：%r" % (e,))
    print("  桌面预检：非黑像素 %.1f%%" % (lit * 100))
    if lit < 0.02:
        print()
        print("结论：本机无法测量（桌面截屏整块是黑的，非黑像素 %.1f%%）。" % (lit * 100))
        print("      BitBlt 抓不到画面时，“窗口被隐藏”与“屏幕本身看不见”无法区分，")
        print("      因此本项既不算通过也不算失败。请在有人值守的交互式桌面上重跑。")
        return 3

    # 对照组只跑短休息：它证明“抓图链路能看见提醒窗口”，一类就够
    ok1, why1, d1 = run_case(False, "short")
    check(ok1, "对照组（开关=关·短休息）跑通", why1)
    if ok1:
        check(d1["magenta"] >= MAGENTA_VISIBLE,
              "开关=关时提醒窗口确实被抓进画面（探针链路有效）",
              f"品红 {d1['magenta']:.2%} ≥ {MAGENTA_VISIBLE:.0%}")

    # 实验组：每一类提醒窗口各自跑一遍 —— 三条建窗路径（休息 / 提前提示 / 外卖）
    # 是独立的代码，一类的结论推不到另一类上
    diags = [d1] if ok1 else []
    for scene in scenes:
        label = SCENES[scene][0]
        ok, why, d = run_case(True, scene)
        check(ok, f"实验组（开关=开·{label}）跑通", why)
        if not ok:
            continue
        diags.append(d)
        check(d["top_affinity"] == WDA_EXCLUDEFROMCAPTURE,
              f"{label}：顶层窗口亲和性 = WDA_EXCLUDEFROMCAPTURE(0x11)",
              d["top_affinity"])
        check(d["magenta"] <= MAGENTA_HIDDEN,
              f"{label}：不出现在捕获结果里",
              f"品红 {d['magenta']:.2%} ≤ {MAGENTA_HIDDEN:.0%}")

    print("\n诊断：")
    for d in diags:
        lines.append(json.dumps(d, ensure_ascii=False))
        print("  " + lines[-1])

    with open(REPORT, "w", encoding="utf-8") as f:
        f.write("\n".join(lines) + "\n")

    print(f"\n{checks - len(failures)}/{checks} 通过")
    if failures:
        print("失败项：")
        for x in failures:
            print("  - " + x)
    if not args.keep:
        shutil.rmtree(ISO, ignore_errors=True)
    return 0 if not failures else 1


if __name__ == "__main__":
    sys.exit(main())
