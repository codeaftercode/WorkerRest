#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""GUI 启动探针：把**带控制台**的 GUI 产物拉起来约 12 秒，读日志，确认
「系统能力监视器、提前提示窗、提示音宿主在真实 GUI 路径下真的起来了、且没有 panic」。

为什么需要它（与 smoke_server.py 的分工）：
  - `smoke_server.py` 走 `-tags server` 无头构建。那条路上「会改调度器状态的三条监视」
    是**故意不启动**的（冒烟机始终空闲，会把调度器随机挂起、毁掉确定性断言）。
    所以「GUI 下它们真的起来了」这件事，无头冒烟**看不见**，只有真跑一次 GUI 才验得到。
  - 本脚本不验渲染（那交给 probe_renderer.py）：容器/CI 里 WebView2 的渲染进程常常起不来
    （日志里会刷 `webview2: process failed`），但**主进程的启动链路、日志、系统能力探测
    完全不受影响**，断言就只打在这一层。

隔离：`WR_SETTINGS_PATH` 指向 `%TEMP%` 里的真实配置副本（加载路径会回写固定键 /
预置条目）；启动路径本来就不碰注册表（只有用户改「开机自启动」时才写）。

用法：python scripts/probe_gui_boot.py [--seconds 12] [--keep-exe]
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
ROOT = os.path.dirname(HERE)  # 项目根（原 rewrite-go，现已提升为顶层）
TEMP = os.environ.get("TEMP", ROOT)
# 普通 GUI 产物带 `-H windowsgui`（没有控制台，stdout 拿不到），所以单独构建一个
# **去掉该标记**、其余参数一致的探针产物。它在 build/bin 下，属 .gitignore 范围。
EXE = os.path.join(ROOT, "build", "bin", "chibingren_probe.exe")
ISO = os.path.join(TEMP, "wr_gui_probe_iso")
LOG = os.path.join(TEMP, "wr_gui_probe.log")
REPORT = os.path.join(TEMP, "wr_gui_boot_report.txt")

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


def find_real_settings():
    """与 smoke_server.py 同口径：唯一候选 = 运行目录的 config/settings.json
    （不再检查 %APPDATA%）。只用于隔离与指纹对拍。"""
    p = os.path.join(ROOT, "config", "settings.json")
    return p if os.path.exists(p) else None


def digest(path):
    if not path or not os.path.isfile(path):
        return None
    with open(path, "rb") as f:
        return hashlib.sha256(f.read()).hexdigest()


def build(exe):
    env = dict(os.environ)
    env.pop("ELECTRON_RUN_AS_NODE", None)
    r = subprocess.run(
        ["go", "build", "-o", os.path.relpath(exe, ROOT), "-ldflags", "-s -w", "."],
        cwd=ROOT, env=env, capture_output=True,
    )
    return r.returncode, (r.stderr or b"").decode("utf-8", "replace")[:400]


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--seconds", type=int, default=12, help="拉起来后观察多久（默认 12）")
    ap.add_argument("--keep-exe", action="store_true", help="跑完保留探针产物")
    args = ap.parse_args()

    rc, err = build(EXE)
    if rc != 0:
        print("构建探针产物失败：%s" % err)
        return 2
    print("GUI 启动探针：%s（观察 %d 秒）" % (EXE, args.seconds))

    real = find_real_settings()
    before = digest(real)
    shutil.rmtree(ISO, ignore_errors=True)
    os.makedirs(ISO, exist_ok=True)
    iso = os.path.join(ISO, "settings.json")
    if real:
        shutil.copyfile(real, iso)
    elif not os.path.exists(iso):
        # 本机还没有真实配置（新落点 config/settings.json 尚未生成）：拿出厂默认打底，
        # 探针在任何机器上都要能自给自足地验提前提示窗与提示音链路
        default_cfg = os.path.join(ROOT, "default_settings.json")
        if os.path.exists(default_cfg):
            shutil.copyfile(default_cfg, iso)
    # 观察窗口内要让两件事都真的发生：
    #   ① 提前提示窗弹出来 —— 短休息「8 秒一次、提前 8 秒提示」，`nextAt - notifyBeforeMs <= now`
    #      立刻成立，启动后调度器第一跳（500ms）就进 pre 阶段；
    #   ② 提示音放出来一次 —— 8 秒后真的开始休息（每次 3 秒），于是「休息开始 → 播提示音」
    #      与「休息结束 → 再播一次」都落在窗口里。
    # 改的是隔离副本（真实配置在末尾仍有 SHA-256 断言守着）。
    if os.path.exists(iso):
        cfg_seed = json.load(open(iso, encoding="utf-8"))
        cfg_seed.setdefault("short", {}).update({
            "enabled": True, "mode": "countdown",
            "intervalMs": 8000, "notifyBeforeMs": 8000, "durationMs": 3000,
        })
        cfg_seed["long"] = dict(cfg_seed.get("long") or {}, enabled=False)
        cfg_seed["takeout"] = dict(cfg_seed.get("takeout") or {}, enabled=False)
        # 系统勿扰也会让调度器一启动就「暂停（勿扰）」，pre 阶段永远不到，
        # 「提前提示窗建出来了」两条断言在开着专注助手的机器上必挂 —— 与它要验的东西无关。
        # 显式关掉，探针才真的自给自足（idlePauseMin 观察 12 秒内到不了，不用管）。
        cfg_seed["dndPause"] = False
        with open(iso, "w", encoding="utf-8") as f:
            json.dump(cfg_seed, f, ensure_ascii=False, indent=2)

    env = dict(os.environ)
    env["WR_SETTINGS_PATH"] = iso
    env.pop("ELECTRON_RUN_AS_NODE", None)

    alive = False
    with open(LOG, "w", encoding="utf-8", errors="replace") as logf:
        proc = subprocess.Popen([EXE], cwd=ROOT, env=env, stdout=logf, stderr=subprocess.STDOUT)
        try:
            time.sleep(max(3, args.seconds))
            alive = proc.poll() is None
        finally:
            if proc.poll() is None:
                proc.terminate()
                try:
                    proc.wait(timeout=8)
                except subprocess.TimeoutExpired:
                    proc.kill()
    text = open(LOG, encoding="utf-8", errors="replace").read()

    try:
        body = "\n--- GUI 日志尾部 ---\n" + text[-2500:]
    except Exception:
        body = ""
    print(body)

    check(alive, "GUI 进程观察期内一直活着（没在启动阶段崩掉）", "rc=%s" % proc.poll())
    check("启动完成" in text, "启动日志有 [boot] 启动完成", "")
    check("panic" not in text.lower(), "日志里没有 panic", "")
    check("[sys] 空闲=" in text, "系统能力探测摘要落日志（[sys] 行）", "")
    check("[idle] 采样已启动" in text, "空闲采样真的启动了（user32!GetLastInputInfo 可用）", "")
    check("无头模式：不启动空闲" not in text,
          "GUI 构建**不是**无头分支（三条监视都应启动）", "")
    check("[plan]" in text, "计划摘要落日志（调度器真的起来了）", "")
    # 提前提示窗：无头冒烟结构性看不见的那块 —— GUI 下「真的建窗」只有这里验得到
    check("[prebreak] 即将休息" in text,
          "进入提前提示区间（调度器 → onPreNotify 这条链路在 GUI 下通了）", "")
    check("[prebreak] 提前提示窗已弹出" in text,
          "提前提示窗真的建出来了（GUI 路径，无头构建会跳过）", "")
    check("跳过创建（内容已就绪" not in text,
          "GUI 构建没走到「无头跳过建窗」那条分支", "")
    # 提示音：宿主窗口与播放链路同样只有 GUI 下才存在（无头构建连窗都不建）
    check("[sound] 播放宿主窗口已创建" in text,
          "播放宿主窗口真的建出来了（隐藏 1×1，承载 <audio>）", "")
    # 这行由 Wails 的 assetFileServer 经 **slog** 打出（kv 形式）：
    #   msg="[AssetFileServerFS] Handling request" url=/sound.html file=sound.html
    # 所以只能断言 url=... 这一段 —— 早期写的「Handling request url=/sound.html」
    # 把 msg 与 kv 用空格硬拼起来，slog 输出里 msg 是带引号的，永远匹配不上
    # （而且当时 appLogger 返回 nil → 这些行被 slog 丢进 io.Discard，压根没有）。
    check("url=/sound.html" in text,
          "宿主页面真的被资源服务交付了（sound.html 请求进了 AssetFileServer）", "")
    check("[sound] 播放" in text,
          "休息开始 / 结束时真的走了播放链路（日志留痕）", "")
    check("跳过本次播放" not in text,
          "没有哪次播放因为「宿主还没就绪」被丢掉（ExecJS 自带排队，不该再加门禁）", "")
    # 启动路径绝不碰注册表：只有用户改「开机自启动」时才写
    check("autostart] 写入自启动" not in text and "autostart] 删除自启动" not in text,
          "启动路径没有写注册表", "")
    if real:
        check(digest(real) == before, "用户真实 settings.json 未被改动", real)

    if not args.keep_exe and os.path.isfile(EXE):
        try:
            os.remove(EXE)
        except OSError:
            pass

    with open(REPORT, "w", encoding="utf-8") as f:
        f.write("GUI 启动探针：%d 项断言，失败 %d 项\n\n" % (checks, len(failures)))
        f.write("\n".join(failures) if failures else "（全部通过）")
        f.write("\n\n--- GUI 日志尾部 ---\n" + text[-3000:])
    print("\n结果：%d 项断言，失败 %d 项" % (checks, len(failures)))
    print("报告：%s" % REPORT)
    return 0 if not failures else 1


if __name__ == "__main__":
    sys.exit(main())
