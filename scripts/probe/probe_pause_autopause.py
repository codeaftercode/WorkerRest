#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""自动暂停（应用占用）的端到端探针：真 GUI 产物 + 真实进程占用 + 真实窗口关闭。

为什么不能用无头服务验：`-tags server` 构建**故意不启动**会改调度器状态的几条监视器
（冒烟机始终空闲，会把调度器随机挂起、毁掉确定性断言）。所以「系统判定 → 暂停 → 收窗 →
条件消失 → 恢复 + 重锚」这条链路只有跑真 GUI 才验得到。

判据用**真实进程**触发，不靠开关配置假装：配置里把 `ping.exe` 列成「指定应用运行时暂停」，
探针自己起一个 `ping -t 127.0.0.1`（无窗口、无交互），杀掉了条件就消失。

验的事：
  1. 目标进程起来后，日志出现「命中 ping.exe，暂停提醒」与「暂停（应用占用）」；
  2. 暂停那一刻**所有提醒窗口被收掉**（[autoPause] 自动暂停：收掉全部提醒窗口）；
  3. 暂停期间到点也不弹休息窗（这一段时间里没有新的「开始休息」日志）；
  4. 进程退出后自动恢复（不需要点按钮），并且**内置倒计时重新计时**（不是接着冻结的旧账走）。
  5. 全程没有 panic。

隔离：WR_SETTINGS_PATH 指向临时副本；结尾断言真实 config/settings.json 一个字节没变。

用法：python scripts/probe/probe_pause_autopause.py [--keep-exe]
退出码 0 = 全部通过。
"""

import argparse
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
# 与 probe_gui_boot.py 同口径：GUI 产物带 -H windowsgui 拿不到 stdout，单独构建一个
# 去掉该标记的探针产物（build/bin 属 .gitignore）
EXE = os.path.join(ROOT, "build", "bin", "chibingren_probe.exe")
ISO = os.path.join(TEMP, "wr_pause_auto_iso")
LOG = os.path.join(TEMP, "wr_pause_auto.log")

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


def wait_for(text_of, needle, timeout):
    """轮询日志直到出现 needle（返回 True）或超时（返回 False）。"""
    t0 = time.time()
    while time.time() - t0 < timeout:
        if needle in text_of():
            return True
        time.sleep(0.3)
    return False


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--keep-exe", action="store_true", help="跑完保留探针产物")
    args = ap.parse_args()

    factory = os.path.join(ROOT, "internal", "settings", "default_settings.json")
    real_cfg = os.path.join(ROOT, "config", "settings.json")
    before = digest(real_cfg)

    rc, err = build(EXE)
    if rc != 0:
        print("构建探针产物失败：%s" % err)
        return 2

    shutil.rmtree(ISO, ignore_errors=True)
    os.makedirs(ISO, exist_ok=True)
    iso = os.path.join(ISO, "settings.json")
    with open(factory, encoding="utf-8") as f:
        cfg = json.load(f)

    # 短休息 8 秒一条、3 秒、不提前提示：观察窗里一定会碰到「休息中被打断」这一支。
    cfg["short"] = dict(cfg.get("short") or {}, enabled=True, mode="countdown",
                        intervalMs=8000, durationMs=3000, notifyBeforeMs=0, postponeMs=60000,
                        slots=[])
    cfg["long"] = dict(cfg.get("long") or {}, enabled=False)
    cfg["takeout"] = dict(cfg.get("takeout") or {}, enabled=False)
    cfg["customBreaks"] = []
    # 其余自动原因全关：这条用例只验「应用占用」这一条，别的任何一条命中都会让
    # 「恢复」永远等不到（勿扰 / 全屏 / 投屏的条件消失不自动恢复）
    cfg["dndPause"] = False
    cfg["fullscreenPause"] = False
    cfg["castMirrorPause"] = False
    cfg["castWirelessPause"] = False
    cfg["idlePauseMin"] = 60
    cfg["appPause"] = {
        "enabled": True,
        "seeded": True,
        "items": [{"id": "probe-ping", "exe": "ping.exe", "name": "探针占位进程", "enabled": True}],
    }
    with open(iso, "w", encoding="utf-8") as f:
        json.dump(cfg, f, ensure_ascii=False, indent=2)

    env = dict(os.environ)
    env["WR_SETTINGS_PATH"] = iso
    env.pop("ELECTRON_RUN_AS_NODE", None)

    log_at = [0]  # 已经读到多少字节（增量读，避免反复整文件拼字符串）

    print("自动暂停探针：%s" % EXE)
    pinger = None
    proc = None
    with open(LOG, "w", encoding="utf-8", errors="replace") as logf:

        def text():
            logf.flush()
            with open(LOG, encoding="utf-8", errors="replace") as f:
                return f.read()

        proc = subprocess.Popen([EXE], cwd=ROOT, env=env, stdout=logf, stderr=subprocess.STDOUT)
        try:
            if not check(wait_for(text, "[sys] 空闲=", 20), "GUI 产物起来了（系统能力摘要落日志）"):
                return 1

            # 先等一次休息窗口：这样「暂停时收掉全部提醒窗口」才有对象可收
            got_break = wait_for(text, "[scheduler] 开始休息 short", 25)
            check(got_break, "观察窗内先弹出过一次真实休息窗口")

            # 起目标进程（无窗口、无交互）
            flags = 0
            if os.name == "nt":
                flags = getattr(subprocess, "CREATE_NO_WINDOW", 0)
            pinger = subprocess.Popen(["ping", "-n", "600", "127.0.0.1"],
                                      stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                                      creationflags=flags)

            hit = wait_for(text, "[appPause] 命中", 20)
            check(hit, "目标进程起来后命中「指定应用占用」")
            check("[scheduler] 暂停（应用占用）" in text(), "调度器真的进入了暂停（原因=应用占用）")
            check("[autoPause] 自动暂停：收掉全部提醒窗口" in text(),
                  "暂停那一刻收掉了屏幕上的提醒窗口（含正在进行的休息窗）")

            # 暂停期间不该再有新的休息窗口冒出来
            mark = text()
            time.sleep(10)
            during = text()[len(mark):]
            check("开始休息" not in during, "暂停期间到点也不弹休息窗", during.strip().splitlines()[:2])

            # 杀进程 → 条件消失 → 自动恢复（不需要点按钮）
            pinger.terminate()
            pinger.wait(timeout=10)
            pinger = None
            resumed = wait_for(text, "[appPause] 目标应用已退出", 20)
            check(resumed, "进程退出后自动恢复（没让用户去点「恢复休息」）")
            check("[scheduler] 恢复（应用占用）" in text(), "调度器真的退出了暂停")
            check("[scheduler] 自动暂停恢复：内置倒计时重新计时" in text(),
                  "恢复时内置倒计时重新计时（不是接着冻结的旧账走）")

            # 恢复后提醒照常回来
            mark = text()
            back = wait_for(lambda: text()[len(mark):], "开始休息 short", 25)
            check(back, "恢复之后休息窗口照常弹出来（没有停摆）")

            body = text()
            check("panic" not in body.lower(), "全程日志无 panic")
            check(proc.poll() is None, "观察期内主进程一直活着", "rc=%s" % proc.poll())
        finally:
            if pinger is not None and pinger.poll() is None:
                pinger.terminate()
                try:
                    pinger.wait(timeout=5)
                except Exception:
                    pinger.kill()
            if proc.poll() is None:
                proc.terminate()
                try:
                    proc.wait(timeout=8)
                except subprocess.TimeoutExpired:
                    proc.kill()

    print("\n--- 日志尾部 ---")
    print(open(LOG, encoding="utf-8", errors="replace").read()[-1200:])

    if before != digest(real_cfg):
        check(False, "真实 config/settings.json 未被改写", real_cfg)
    else:
        check(True, "真实 config/settings.json 未被改写")

    print("\n结果：%d 项断言，失败 %d 项" % (checks, len(failures)))
    for f in failures:
        print("  - " + f)
    if not args.keep_exe:
        try:
            os.remove(EXE)
        except OSError:
            pass
    return 0 if not failures else 1


if __name__ == "__main__":
    sys.exit(main())
