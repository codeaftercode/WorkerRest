#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""「请勿打扰」/「全屏或演示」两条判据的只读诊断（仅 Windows）。

为什么要它：Win11 通知中心手动开「请勿打扰」、以及「开始专注」启动专注会话，
都**不改变** SHQueryUserNotificationState 的返回值（QUNS 恒为 5）。软件改读
WNF_SHEL_QUIETHOURS_ACTIVE_PROFILE_CHANGED 的勿扰档位，本脚本把两条原始信号
并排打出来，用于人工核对「用户按了勿扰 → 档位确实变了」。

用法：
    python scripts/probe/probe_dnd_wnf.py              # 采一次
    python scripts/probe/probe_dnd_wnf.py --watch 20   # 连续 20 秒，每秒一行

验证步骤：先跑 --watch，期间在通知中心点开「请勿打扰」再点关，观察
「勿扰档位」在 0 ↔ 1/2 之间变化；QUNS 列可因全屏窗口同时变化，但勿扰判定不依赖它。
只读，不修改任何系统设置。
"""

import argparse
import ctypes
import sys
import time
from ctypes import wintypes

if sys.platform != "win32":
    sys.exit("仅支持 Windows")

ntdll = ctypes.WinDLL("ntdll", use_last_error=True)
shell32 = ctypes.WinDLL("shell32", use_last_error=True)

# WNF_SHEL_QUIETHOURS_ACTIVE_PROFILE_CHANGED；数据 0=关 1=仅优先 2=仅闹钟
WNF_QUIET_HOURS_ACTIVE = 0x0D83063EA3BF1C75

NtQueryWnfStateData = ntdll.NtQueryWnfStateData
NtQueryWnfStateData.argtypes = [
    ctypes.POINTER(ctypes.c_uint64),  # StateName
    ctypes.c_void_p,                  # TypeId
    ctypes.c_void_p,                  # ExplicitScope
    ctypes.POINTER(wintypes.DWORD),   # ChangeStamp
    ctypes.c_void_p,                  # Buffer
    ctypes.POINTER(wintypes.DWORD),   # BufferSize
]
NtQueryWnfStateData.restype = ctypes.c_long  # NTSTATUS (LONG, 负值 = 失败)

SHQueryUserNotificationState = shell32.SHQueryUserNotificationState
SHQueryUserNotificationState.argtypes = [ctypes.POINTER(wintypes.DWORD)]
SHQueryUserNotificationState.restype = ctypes.c_long  # HRESULT

QUNS_TEXT = {
    1: "not-present", 2: "busy(全屏应用/非独占)", 3: "d3d-fullscreen(全屏应用/独占)",
    4: "presentation(演示设置)", 5: "accepts-notifications",
    6: "quiet-time", 7: "app",
}
PROFILE_TEXT = {0: "off", 1: "priority-only", 2: "alarms-only"}


def read_quns():
    value = wintypes.DWORD()
    hr = SHQueryUserNotificationState(ctypes.byref(value))
    return value.value if hr == 0 else None


def read_quiet_profile():
    name = ctypes.c_uint64(WNF_QUIET_HOURS_ACTIVE)
    stamp = wintypes.DWORD()
    buf = wintypes.DWORD()
    size = wintypes.DWORD(ctypes.sizeof(buf))
    status = NtQueryWnfStateData(ctypes.byref(name), None, None,
                                 ctypes.byref(stamp), ctypes.byref(buf), ctypes.byref(size))
    if status < 0 or size.value < ctypes.sizeof(buf):
        return None
    return buf.value


def sample():
    q, p = read_quns(), read_quiet_profile()
    q_txt = "读取失败" if q is None else "%d %s" % (q, QUNS_TEXT.get(q, "未知"))
    p_txt = "读取失败" if p is None else "%d %s" % (p, PROFILE_TEXT.get(p, "未知档位"))
    flags = []
    if p:
        flags.append("勿扰=开")
    # QUNS 2 / 3 / 4 合并为一条「全屏或演示」开关；具体成因看左边 QUNS 列。
    if q in (2, 3, 4):
        flags.append("全屏或演示=开")
    return "QUNS=%-28s 勿扰档位=%-18s 判定：%s" % (
        q_txt, p_txt, "、".join(flags) if flags else "两条都不成立")


def main():
    ap = argparse.ArgumentParser(description="只读诊断「请勿打扰」/「全屏或演示」判据")
    ap.add_argument("--watch", type=int, default=0, metavar="SEC",
                    help="连续采样指定的秒数（每秒一行），用于手动切换勿扰时观察")
    args = ap.parse_args()

    if args.watch <= 0:
        print(sample())
        return
    end = time.time() + args.watch
    prev = None
    print("连续采样 %d 秒；现在可以去通知中心开关「请勿打扰」……" % args.watch)
    while time.time() < end:
        line = sample()
        mark = "  <== 变化" if prev is not None and line != prev else ""
        print("[%s] %s%s" % (time.strftime("%H:%M:%S"), line, mark), flush=True)
        prev = line
        time.sleep(1)


if __name__ == "__main__":
    main()
