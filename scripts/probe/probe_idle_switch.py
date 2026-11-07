#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""“系统空闲”开关的端到端探针：真 GUI 产物 + 真实键鼠空闲 + 真实统计落盘。

为什么必须跑真 GUI：`-tags server` 的无头产物**故意不启动**空闲监视（冒烟机上始终无人
操作，空闲会把调度器一直挂起），所以“系统判定 → 暂停 → 离开记账”这条链只有 GUI 产物能验。

两个用例，各自独立隔离配置、独立进程：

  A. 勾选（idlePause=true，阈值 1 分钟）：真实空闲跨过阈值 → 日志出现“已连续无输入”
     与“暂停（idle）”；统计里离开段打开，首段从 workMs 搬进 awayMs，且离开期间
     workMs 不再增长（空闲不算工作）。
  B. 取消勾选（idlePause=false，阈值同为 1 分钟）：同样等过阈值 → 一条空闲日志都没有、
     不暂停；统计 awayMs 恒为 0，而 workMs 持续增长（关闭后这段时间算工作，这是连带影响）。

统计口径（别再写“awayMs 每秒增长”这种断言）：生产链路里 OnAwayTick 没有周期调用，
AwayMs 只在 BeginAway 搬入「最后一次输入 → 判定时刻」这一段，尾账要到 EndAway
（人回来 / 睡眠醒来 / 关掉开关）才一次性补齐。所以离开期间落盘的 awayMs 是**冻结**的，
这是既有设计而非缺陷。

ON 用例的观察窗里**用户随时可能碰一下键鼠**（Go / 无头探针在旁边跑、切窗口看进度都会
被 GetLastInputInfo 记成输入），一碰就走 IdleStepBack → EndAway，于是 awayMs 补尾账、
workMs 重新起算。别假设「整段观察窗都停在离开态」，按日志里有没有「恢复（idle）」
分两种口径断言，两条路径都是产品正确行为。

隔离：WR_SETTINGS_PATH / WR_STATS_PATH 都指向临时目录；结尾断言真实 config/settings.json
一个字节没变。全程不产生任何真实键鼠输入 —— 探针自己也不点鼠标（Playwright 那样的
CDP 事件不算系统输入，这里干脆一个都不发）。

用法：python scripts/probe/probe_idle_switch.py [--keep-exe]
退出码 0 = 全部通过。
"""

import argparse
import glob
import hashlib
import json
import os
import shutil
import subprocess
import sys
import time

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(os.path.dirname(HERE))
TEMP = os.environ.get("TEMP", ROOT)
# 与 probe_gui_boot.py / probe_pause_autopause.py 同口径：GUI 产物带 -H windowsgui 拿不到
# stdout，单独构建一个去掉该标记的探针产物（build/bin 属 .gitignore）
EXE = os.path.join(ROOT, "build", "bin", "rest_probe.exe")
ISO_ROOT = os.path.join(TEMP, "wr_idle_switch_iso")
LOG_ROOT = os.path.join(TEMP, "wr_idle_switch_log")

# 阈值 1 分钟是设置里允许的最小值；再短就得改代码，不允许
IDLE_MIN = 1
# 等过阈值的观察窗：阈值 + 15 秒（采样 1 秒一跳，留足余量）
OBSERVE_S = IDLE_MIN * 60 + 15

try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
    sys.stderr.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass

failures = []
checks = 0


def check(ok, label, detail=""):
    global checks
    checks += 1
    print(("  ok   " if ok else "  FAIL ") + label + (("  | " + str(detail)) if detail else ""))
    if not ok:
        failures.append(label + (("  | " + str(detail)) if detail else ""))
    return ok


def digest(path):
    if not path or not os.path.isfile(path):
        return None
    with open(path, "rb") as f:
        return hashlib.sha256(f.read()).hexdigest()


def build(exe):
    env = dict(os.environ)
    env.pop("ELECTRON_RUN_AS_NODE", None)
    r = subprocess.run(
        ["go", "build", "-o", os.path.relpath(exe, ROOT), "-ldflags", "-s -w", "./cmd/workerrest"],
        cwd=ROOT, env=env, capture_output=True,
    )
    return r.returncode, (r.stderr or b"").decode("utf-8", "replace")[:400]


def read_stats(path):
    """读落盘统计里今天那一条，返回 dict（没有就空 dict）。"""
    try:
        with open(path, encoding="utf-8") as f:
            data = json.load(f)
    except Exception:
        return {}
    days = [k for k in data if not k.startswith("_")]
    if not days:
        return {}
    return data[max(days)]


def idle_secs():
    """当前系统的“连续无输入秒数”（与产品同一个 user32!GetLastInputInfo）。
    只用来在超时的时候说清“是本机一直在动，还是链路断了”。"""
    try:
        import ctypes

        class LII(ctypes.Structure):
            _fields_ = [("cbSize", ctypes.c_uint), ("dwTime", ctypes.c_uint)]

        u = ctypes.windll.user32
        k = ctypes.windll.kernel32
        i = LII()
        i.cbSize = ctypes.sizeof(i)
        if not u.GetLastInputInfo(ctypes.byref(i)):
            return None
        return ((k.GetTickCount() & 0xFFFFFFFF) - i.dwTime) / 1000.0
    except Exception:
        return None


def make_config(factory, iso, idle_on):
    with open(factory, encoding="utf-8") as f:
        cfg = json.load(f)
    # 只留“系统空闲”这一条：别的自动原因一旦命中，就会把观察窗搅乱
    # （勿扰 / 全屏 / 投屏的条件消失不自动恢复，暂停会一直挂着）
    cfg["dndPause"] = False
    cfg["fullscreenPause"] = False
    cfg["castMirrorPause"] = False
    cfg["castWirelessPause"] = False
    cfg["appPause"] = {"enabled": False, "seeded": True, "items": []}
    # 休息全部关掉：观察窗里不该冒出任何休息窗
    cfg["short"] = dict(cfg.get("short") or {}, enabled=False)
    cfg["long"] = dict(cfg.get("long") or {}, enabled=False)
    cfg["takeout"] = dict(cfg.get("takeout") or {}, enabled=False)
    cfg["customBreaks"] = []
    # 也不许自动检查更新（会联网、会写配置）
    cfg["checkUpdateOnStart"] = False
    cfg["startMinimized"] = True
    cfg["idlePause"] = idle_on
    cfg["idlePauseMin"] = IDLE_MIN
    path = os.path.join(iso, "settings.json")
    with open(path, "w", encoding="utf-8") as f:
        json.dump(cfg, f, ensure_ascii=False, indent=2)
    return path


def run_case(name, idle_on):
    print("\n=== 用例 %s：idlePause=%s，阈值 %d 分钟 ===" % (name, idle_on, IDLE_MIN))
    iso = os.path.join(ISO_ROOT, name)
    shutil.rmtree(iso, ignore_errors=True)
    os.makedirs(iso, exist_ok=True)
    log_path = os.path.join(LOG_ROOT, name + ".log")
    stats_path = os.path.join(iso, "stats.json")
    cfg_path = make_config(os.path.join(ROOT, "internal", "settings", "default_settings.json"), iso, idle_on)

    env = dict(os.environ)
    env["WR_SETTINGS_PATH"] = cfg_path
    env["WR_STATS_PATH"] = stats_path
    env.pop("ELECTRON_RUN_AS_NODE", None)

    p = None
    with open(log_path, "w", encoding="utf-8", errors="replace") as logf:

        def text():
            logf.flush()
            with open(log_path, encoding="utf-8", errors="replace") as f:
                return f.read()

        def wait_for(needle, timeout, peak=None):
            t0 = time.time()
            while time.time() - t0 < timeout:
                if needle in text():
                    return True
                cur = idle_secs()
                if peak is not None and cur is not None and cur > peak["v"]:
                    peak["v"] = cur
                time.sleep(0.3)
            return False

        p = subprocess.Popen([EXE], cwd=ROOT, env=env, stdout=logf, stderr=subprocess.STDOUT)
        t0 = time.time()
        try:
            if not check(wait_for("[sys] 空闲=", 25), "GUI 产物起来了（系统能力摘要落日志）"):
                return
            # 采样链路确实可用，否则“没暂停”可能只是因为读不到空闲
            head = text()
            sysline = [ln for ln in head.splitlines() if "[sys] 空闲=" in ln][:1]
            check(bool(sysline) and "不可用" not in sysline[0], "空闲探测可用（GetLastInputInfo）", sysline)

            if idle_on:
                peak = {"v": 0.0}
                hit = wait_for("[scheduler] 暂停（idle）", 180, peak)
                check(hit, "真实空闲跨过阈值后进入了空闲暂停",
                      "" if hit else "观察窗内最长连续无输入仅 %.0fs（阈值 %ds）—— 这段时间机器一直有人操作，测不到"
                      % (peak["v"], IDLE_MIN * 60))
                # 没测到就是没测到：后面的统计断言全都以「已判离开」为前提，硬跑只会
                # 造出一串误导性的 FAIL。如实记一条「无法测量」并跳过，不要伪造结论。
                if not hit:
                    print("  ..   ON 用例本轮无法测量：本机观察窗内始终有人操作，"
                          "阈值 %d 分钟达不到。需真机静置后重跑 --only on" % IDLE_MIN)
                    check("panic" not in text().lower(), "全程日志无 panic")
                    check(p.poll() is None, "观察期内主进程一直活着", "rc=%s" % p.poll())
                    print("--- 日志尾部（%s）---" % name)
                    print(text_tail(log_path))
                    return
                body = text()
                check("[idle] 已连续无输入" in body, "日志记下了“已连续无输入 ≥ 阈值”")
                # 暂停那一刻 BeginAway 把「最后一次输入 → 判定时刻」从 workMs 搬进 awayMs。
                # 注意：软件启动到判离开之间可能已经累计了几十秒真实工作（本机有人操作），
                # 所以 workMs 不一定接近 0，能钉死的只有「搬入量 ≈ 阈值」。
                time.sleep(3)
                first = read_stats(stats_path)
                away1, work1 = first.get("awayMs", 0), first.get("workMs", 0)
                check(away1 > 0, "暂停后统计文件里已有离开时长（首段搬入）", "awayMs=%s" % away1)
                check(away1 >= (IDLE_MIN * 60 - 5) * 1000,
                      "搬入量≈阈值前那一段空闲（不是零星一跳）", "awayMs=%s" % away1)

                time.sleep(14)
                second = read_stats(stats_path)
                away2, work2 = second.get("awayMs", 0), second.get("workMs", 0)
                # 两条路径都成立的不变量：账只往前走，不倒退、不重复搬。
                check(away2 >= away1, "离开时长不倒退（恢复时只会补尾账）",
                      "awayMs %s → %s" % (away1, away2))
                check(work2 >= work1, "工作时长不倒退",
                      "workMs %s → %s" % (work1, work2))

                resumed = "[idle] 检测到键鼠活动" in text()
                if resumed:
                    # 恢复可能发生在上面两次采样之前 / 之间，单次快照差不足以证明增长；
                    # 但恢复之后工作累加器必然每秒继续入账，再等一拍即可稳定验证。
                    check("恢复（idle）" in text(), "恢复链路成对出现在日志里")
                    check(away2 >= away1, "恢复时尾账已补齐（离开时长不减少）",
                          "awayMs %s → %s" % (away1, away2))
                    time.sleep(6)
                    third = read_stats(stats_path)
                    work3 = third.get("workMs", 0)
                    check(work3 > work2, "恢复后工作时长重新起算（持续增长）",
                          "workMs %s → %s" % (work2, work3))
                    check(third.get("awayMs", 0) == away2,
                          "恢复后离开段已结束、不再累加",
                          "awayMs %s → %s" % (away2, third.get("awayMs", 0)))
                else:
                    # 仍停在离开态：落盘值冻结，不算工作、不重复记账。
                    check(away2 == away1, "离开期间不重复记账（尾账留到 EndAway 补）",
                          "awayMs %s → %s" % (away1, away2))
                    check(work2 == work1, "离开期间工作时长不再增长（空闲不算工作）",
                          "workMs %s → %s" % (work1, work2))
                print("  ..   本轮路径：%s" % ("观察窗内检测到键鼠活动（恢复）" if resumed else "持续离开"))
                check("开始休息" not in text(), "观察窗内没有冒出休息窗（前提配置生效）")
            else:
                # 观察窗过半时取一次基准，末尾再取一次：证明 workMs 是**持续增长**的，
                # 而不是启动时写了个常量（那也能骗过「≥1 分钟」这一条）。
                time.sleep(max(0, (OBSERVE_S - 12) - (time.time() - t0)))
                mid = read_stats(stats_path)
                time.sleep(max(0, OBSERVE_S - (time.time() - t0)))
                body = text()
                check("[idle] 已连续无输入" not in body, "关掉后日志里没有任何空闲判定")
                check("暂停（idle）" not in body, "关掉后调度器从未因空闲暂停")
                check("暂停（" not in body, "关掉后根本没暂停过")
                st = read_stats(stats_path)
                check(st.get("awayMs", 0) == 0, "关掉后统计的离开时长为 0", "awayMs=%s" % st.get("awayMs"))
                check(st.get("workMs", 0) >= (IDLE_MIN * 60) * 1000,
                      "关掉后这段时间照常算工作（≥1 分钟）", "workMs=%s" % st.get("workMs"))
                check(st.get("workMs", 0) > mid.get("workMs", 0),
                      "关掉后工作时长持续增长（不是只写了一次）",
                      "workMs %s → %s" % (mid.get("workMs"), st.get("workMs")))

            check("panic" not in text().lower(), "全程日志无 panic")
            check(p.poll() is None, "观察期内主进程一直活着", "rc=%s" % p.poll())
        finally:
            if p.poll() is None:
                p.terminate()
                try:
                    p.wait(timeout=9)
                except subprocess.TimeoutExpired:
                    p.kill()
                time.sleep(0.5)

    print("--- 日志尾部（%s）---" % name)
    print(text_tail(log_path))


def text_tail(path, n=900):
    try:
        with open(path, encoding="utf-8", errors="replace") as f:
            return f.read()[-n:]
    except Exception:
        return ""


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--keep-exe", action="store_true", help="跑完保留探针产物")
    ap.add_argument("--only", choices=["on", "off"], help="只跑其中一个用例")
    args = ap.parse_args()

    real_cfg = os.path.join(ROOT, "config", "settings.json")
    before = digest(real_cfg)

    rc, err = build(EXE)
    if rc != 0:
        print("构建探针产物失败：%s" % err)
        return 2

    shutil.rmtree(LOG_ROOT, ignore_errors=True)
    os.makedirs(LOG_ROOT, exist_ok=True)

    print("系统空闲开关探针：%s" % EXE)
    if args.only != "off":
        run_case("on", True)
    if args.only != "on":
        run_case("off", False)

    print("\n结果：%d 项断言，失败 %d 项" % (checks, len(failures)))
    for f in failures:
        print("  - " + f)
    if before != digest(real_cfg):
        check(False, "真实 config/settings.json 未被改写", real_cfg)
    else:
        check(True, "真实 config/settings.json 未被改写")
    if not args.keep_exe:
        try:
            os.remove(EXE)
        except OSError:
            pass
    return 0 if not failures else 1


if __name__ == "__main__":
    sys.exit(main())
