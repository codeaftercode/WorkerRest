#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""真机双屏验证：提醒窗口到底弹在哪几块屏上。

为什么必须有这个探针：`displayIds` 的语义（具体 ID 快照 / 空=主屏 / 失配回退主屏）
在 Go 侧有单测，但「窗口真的落到那块屏上了吗」只有跑起来才知道 —— 中间还隔着两层：
  ① display.Info（QueryDisplayConfig 的物理像素矩形）与 Wails Screen.WorkArea 的对齐；
  ② 窗口建出来后有没有被 Wails 的 InitialPosition 或系统 DPI 虚拟化挪走。
这两层任何一层错，用户看到的就是「勾了副屏，提醒还是弹在主屏」，而日志一切正常。

做法：一组一条进程，隔离配置（WR_SETTINGS_PATH），让短休息 6 秒后自己弹出来，
然后用 Win32 枚举本进程的顶层窗口，按窗口中心点判定它落在哪块屏上。

场景（每个都同时断言「数量对不对」和「屏对不对」）：
  primary    勾主屏的 ID                     → 主屏 1 扇
  both       勾满两块                          → 每块各 1 扇（这是用户想要的那个行为）
  secondary  只勾副屏                          → 只有副屏 1 扇，主屏不许有
  stale      勾了一个已经不存在的 ID            → 回退主屏 1 扇，绝不「一扇都不弹」
  prebreak   只勾副屏 + 提前提示                → 提前小窗跟着勾选走

判据：
  - 窗口中心点必须落在目标屏矩形内（严格，无容差 —— 居中窗口离边几百像素）
  - 窗口尺寸 ≈ 该屏矩形 × widthPct/heightPct（容差 12%），尺寸对不上说明它按另一块屏
    算过尺寸只是位置碰巧对
  - 非目标屏上一扇都不许有（数量断言已覆盖，这里再显式数一遍便于读报告）

前置：至少两块活动屏，否则退出码 3（= 无法测量，与「失败」区分）。
隔离：WR_SETTINGS_PATH / WR_LOG_FILE 指向 %TEMP%，绝不碰真实配置。
用法：python scripts/probe/probe_display_target.py [--keep] [--no-build]
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
EXE = os.path.join(ROOT, "build", "bin", "chibingren_disp.exe")
HELPER = os.path.join(HERE, "display_ids.go")
TEMP = os.environ.get("TEMP", ROOT)
ISO = os.path.join(TEMP, "wr_disptarget_iso")
LOG = os.path.join(TEMP, "wr_disptarget.log")
DEFAULT_CFG = os.path.join(ROOT, "internal", "settings", "default_settings.json")

TITLE_BREAK = "休息一下"
TITLE_PREBREAK = "即将休息"

user32 = ctypes.windll.user32
dwmapi = ctypes.windll.dwmapi
user32.SetProcessDPIAware()  # 必须：否则拿到的矩形是 DPI 虚拟化后的，与 Go 侧物理像素对不上

failures = []
checks = 0


def check(ok, label, detail=""):
    global checks
    checks += 1
    print(("  ok   " if ok else "  FAIL ") + label + (("  | " + str(detail)) if detail else ""))
    if not ok:
        failures.append(label + (("  | " + str(detail)) if detail else ""))
    return ok


def go_exe():
    """找 go：PATH → GOROOT → 常见安装位置（开发机上 go 不在 PATH，见 docs/DEVELOPMENT.md）。"""
    found = shutil.which("go")
    if found:
        return found
    cands = []
    if os.environ.get("GOROOT"):
        cands.append(os.path.join(os.environ["GOROOT"], "bin", "go.exe"))
    cands += [r"D:\Programs\Go\bin\go.exe", r"C:\Programs\Go\bin\go.exe"]
    for c in cands:
        if os.path.isfile(c):
            return c
    return None


def go_env():
    env = dict(os.environ)
    if not env.get("GOCACHE"):
        base = os.environ.get("LOCALAPPDATA") or TEMP
        env["GOCACHE"] = os.path.join(base, "go-build")
    return env


def list_displays():
    """借 Go 侧 display.List 拿清单：探针不自己维护第二套 DISPLAYCONFIG 结构体。"""
    g = go_exe()
    if not g:
        raise SystemExit("找不到 go")
    r = subprocess.run([g, "run", HELPER], cwd=ROOT, capture_output=True, text=True,
                       encoding="utf-8", errors="replace", env=go_env())
    if r.returncode != 0 or not (r.stdout or "").strip():
        raise SystemExit("display_ids 失败（rc=%s）：%s" % (r.returncode, (r.stderr or "").strip()[:400]))
    data = json.loads(r.stdout.strip().splitlines()[-1])
    if not data.get("ok"):
        raise SystemExit("枚举不可用：" + str(data.get("error")))
    return data["displays"]


def build():
    print("构建 GUI 产物…")
    g = go_exe()
    if not g:
        raise SystemExit("找不到 go")
    b = subprocess.run([g, "build", "-o", EXE, "-ldflags", "-s -w", "./cmd/workerrest"],
                       cwd=ROOT, capture_output=True, text=True,
                       encoding="utf-8", errors="replace", env=go_env())
    if b.returncode != 0:
        print("构建失败:\n" + (b.stderr or b.stdout))
        sys.exit(2)


# ---------- 配置播种 ----------

def make_cfg(display_ids, scene="short"):
    with open(DEFAULT_CFG, "r", encoding="utf-8") as f:
        cfg = json.load(f)
    off = {"enabled": False, "mode": "countdown", "intervalMs": 1800000,
           "durationMs": 300000, "notifyBeforeMs": 0, "postponeMs": 60000, "slots": []}
    cfg["short"] = dict(off)
    cfg["long"] = dict(off)
    cfg["customBreaks"] = []
    cfg["takeout"] = dict(cfg.get("takeout") or {}, enabled=False, leadSec=0, slots=[])

    on = {"enabled": True, "mode": "countdown", "intervalMs": 6000,
          "durationMs": 300000, "notifyBeforeMs": 0, "postponeMs": 60000, "slots": []}
    if scene == "short":
        cfg["short"] = dict(on)
    elif scene == "prebreak":
        pb = dict(on)
        pb["intervalMs"] = 14000
        pb["notifyBeforeMs"] = 6000
        cfg["short"] = pb
    else:
        raise SystemExit("未知场景：" + scene)

    # 其它「会让提醒不弹」的开关全关：探针要验的是显示器选择，不是这些
    cfg["appPause"] = {"enabled": False, "apps": []}
    cfg["idlePauseMin"] = 0
    cfg["dndPause"] = False
    cfg["fullscreenPause"] = False
    cfg["castWirelessPause"] = False
    cfg["castMirrorPause"] = False
    cfg["startMinimized"] = True
    cfg["sound"] = dict(cfg.get("sound") or {}, enabled=False)
    cfg["supervisor"] = dict(cfg.get("supervisor") or {}, enabled=False)
    cfg["displayIds"] = list(display_ids)
    ap = dict(cfg.get("appearance") or {})
    ap["hideOnShare"] = False
    ap["alwaysOnTop"] = True
    ap["blur"] = False       # 模糊会让「窗口在不在」难判定，这里不需要它
    ap["fullscreen"] = False
    cfg["appearance"] = ap

    if not os.path.isdir(ISO):
        os.makedirs(ISO)
    path = os.path.join(ISO, "settings.json")
    with open(path, "w", encoding="utf-8") as f:
        json.dump(cfg, f, ensure_ascii=False, indent=2)
    return path, cfg["appearance"]


# ---------- 窗口枚举 ----------

def kill_all():
    subprocess.run(["taskkill", "/IM", os.path.basename(EXE), "/T", "/F"], capture_output=True)


def windows_titled(proc_pid, title):
    """本进程里标题匹配且可见的顶层窗口 → [(hwnd, (x,y,w,h))]，取 DWM 可见边界。"""
    found = []

    @ctypes.WINFUNCTYPE(wt.BOOL, wt.HWND, wt.LPARAM)
    def cb(hwnd, _):
        pid = wt.DWORD()
        user32.GetWindowThreadProcessId(hwnd, ctypes.byref(pid))
        if pid.value != proc_pid or not user32.IsWindowVisible(hwnd):
            return True
        n = user32.GetWindowTextLengthW(hwnd)
        if n <= 0:
            return True
        buf = ctypes.create_unicode_buffer(n + 1)
        user32.GetWindowTextW(hwnd, buf, n + 1)
        if buf.value.strip() != title:
            return True
        r = wt.RECT()
        hr = dwmapi.DwmGetWindowAttribute(hwnd, 9, ctypes.byref(r), ctypes.sizeof(r))
        if hr != 0:
            user32.GetWindowRect(hwnd, ctypes.byref(r))
        w, h = r.right - r.left, r.bottom - r.top
        if w > 50 and h > 50:
            found.append((hwnd, (r.left, r.top, w, h)))
        return True

    user32.EnumWindows(cb, 0)
    return found


def wait_windows(proc_pid, title, want, timeout, settle=1.8):
    """等窗口数量达到 want，再多等 settle 秒确认没有迟到的第 N+1 扇。"""
    deadline = time.time() + timeout
    wins = []
    while time.time() < deadline:
        wins = windows_titled(proc_pid, title)
        if len(wins) >= want:
            break
        time.sleep(0.25)
    time.sleep(settle)
    return windows_titled(proc_pid, title)


def screen_of(rect, displays):
    """窗口中心点落在哪块屏（返回下标；都在外面返回 None）。"""
    x, y, w, h = rect
    cx, cy = x + w // 2, y + h // 2
    for i, d in enumerate(displays):
        if d["x"] <= cx < d["x"] + d["width"] and d["y"] <= cy < d["y"] + d["height"]:
            return i
    return None


# ---------- 单场景 ----------

def run_case(name, display_ids, expect_ix, scene="short", note=""):
    """expect_ix：本场景期望「每块屏各一扇」的屏下标列表。"""
    title = TITLE_PREBREAK if scene == "prebreak" else TITLE_BREAK
    print("\n—— %s：%s ——" % (name, note or "、".join(str(x) for x in display_ids)))
    kill_all()
    time.sleep(0.4)
    cfg_path, ap = make_cfg(display_ids, scene)
    env = dict(os.environ)
    env["WR_SETTINGS_PATH"] = cfg_path
    env["WR_LOG_FILE"] = LOG
    logf = open(LOG, "a", encoding="utf-8")
    proc = subprocess.Popen([EXE], cwd=ROOT, env=env, stdout=logf, stderr=subprocess.STDOUT)
    try:
        want = len(expect_ix)
        wins = wait_windows(proc.pid, title, want, timeout=45)
        print("  「%s」共 %d 扇：%s" % (title, len(wins), [w[1] for w in wins]))
        if not check(len(wins) == want, "窗口数量 = %d" % want, "实际 %d" % len(wins)):
            return
        landed = [screen_of(r, DISPLAYS) for _, r in wins]
        want_set = sorted(expect_ix)
        check(sorted(x for x in landed if x is not None) == want_set,
              "落屏 = %s" % [DISPLAYS[i]["label"] for i in want_set],
              "实际 = %s" % [("-" if i is None else DISPLAYS[i]["label"]) for i in landed])
        # 尺寸按目标屏算：勾了副屏却按主屏尺寸建窗，位置对、尺寸也对不上。
        # 提前提示窗例外：它是固定 400×240 的小窗（不随屏尺寸缩放），只验落点。
        if scene == "prebreak":
            print("  （提前提示窗是固定小窗，不参与 95% 尺寸断言）")
            return
        for (hwnd, rect), ix in zip(wins, landed):
            if ix is None:
                continue
            d = DISPLAYS[ix]
            ew = d["width"] * ap["widthPct"] / 100.0
            eh = d["height"] * ap["heightPct"] / 100.0
            check(abs(rect[2] - ew) <= max(24, ew * 0.12) and abs(rect[3] - eh) <= max(24, eh * 0.12),
                  "%s 上窗口尺寸符合该屏 %d%% 口径" % (d["label"], ap["widthPct"]),
                  "窗口 %dx%d，期望约 %dx%d" % (rect[2], rect[3], round(ew), round(eh)))
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


def main():
    try:
        sys.stdout.reconfigure(encoding="utf-8", errors="replace")
    except Exception:
        pass
    ap = argparse.ArgumentParser()
    ap.add_argument("--no-build", action="store_true")
    ap.add_argument("--keep", action="store_true", help="保留隔离目录与日志")
    args = ap.parse_args()

    global DISPLAYS
    DISPLAYS = list_displays()
    print("当前接入 %d 块屏：" % len(DISPLAYS))
    for d in DISPLAYS:
        print("  %-16s %4dx%-4d @ (%5d,%5d)%s" % (d["label"], d["width"], d["height"],
                                                  d["x"], d["y"], "  · 主屏" if d["isPrimary"] else ""))
    if len(DISPLAYS) < 2:
        print("\n只有一块活动屏，无法验证多屏落点 → 退出码 3（无法测量）")
        return 3

    if not args.no_build:
        build()
    elif not os.path.isfile(EXE):
        print("--no-build 但 %s 不存在" % EXE)
        return 2

    primary = next(i for i, d in enumerate(DISPLAYS) if d["isPrimary"])
    other = next(i for i in range(len(DISPLAYS)) if i != primary)
    pid_primary = DISPLAYS[primary]["id"]
    pid_other = DISPLAYS[other]["id"]

    run_case("只勾主屏", [pid_primary], [primary], note="勾主屏的稳定 ID")
    run_case("空配置", [], [primary], note="空数组必须等同主屏，绝不是「所有屏」")
    run_case("勾满两块", [pid_primary, pid_other], list(range(len(DISPLAYS))),
             note="用户想要的：每块各一扇")
    run_case("只勾副屏", [pid_other], [other], note="主屏上一扇都不许有")
    run_case("勾了已拔掉的屏", ["DEL0000#0&0&0&UID0"], [primary],
             note="失配必须回退主屏，不能一扇都不弹")
    run_case("提前提示跟随勾选", [pid_other], [other], scene="prebreak",
             note="只勾副屏 → 提前小窗也在副屏")

    print("\n—— 汇总 ——")
    print("  检查项 %d，失败 %d" % (checks, len(failures)))
    for f in failures:
        print("  FAIL " + f)

    if not args.keep:
        shutil.rmtree(ISO, ignore_errors=True)
    return 0 if not failures else 1


if __name__ == "__main__":
    try:
        sys.exit(main())
    except KeyboardInterrupt:
        kill_all()
        sys.exit(130)
