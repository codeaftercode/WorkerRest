#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""暂停 / 恢复端到端探针的驱动：起无头服务 → 跑 pause_cycle_probe.js → 收断言。

与既有脚本的分工：
  · 单元测试（internal/scheduler + cmd/workerrest）验判据与状态机；
  · 本脚本验「真页面点下去 → 真绑定 → 真调度器」这条链路：
    按钮文案翻转、nextAt 冻结与平移、暂停期间到点不弹窗、恢复后照常弹；
  · 自动暂停（勿扰 / 投屏 / 全屏 / 应用占用 / 空闲）走 probe_pause_autopause.py：
    那几条判据来自系统状态，而无头模式**故意不启动**对应监视器。

隔离口径与其余探针一致：配置用出厂默认的一份副本放临时目录，WR_SETTINGS_PATH 指过去，
跑完断言仓库里真实的 config/settings.json 一个字节没变。

用法：python scripts/probe/probe_pause_cycle.py
退出码 0 = 全部通过。
"""

import hashlib
import json
import os
import shutil
import socket
import subprocess
import sys
import tempfile
import time
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(os.path.dirname(HERE))
EXE = os.path.join(ROOT, "build", "bin", "chibingren_server.exe")
FACTORY = os.path.join(ROOT, "internal", "settings", "default_settings.json")
PROBE_JS = os.path.join(HERE, "pause_cycle_probe.js")

try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass


def free_port(preferred):
    with socket.socket() as s:
        try:
            s.bind(("127.0.0.1", preferred))
            return preferred
        except OSError:
            pass
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def http_get(url, timeout=2):
    try:
        with urllib.request.urlopen(url, timeout=timeout) as r:
            return r.status
    except Exception:
        return 0


def digest(path):
    if not os.path.exists(path):
        return ""
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(65536), b""):
            h.update(chunk)
    return h.hexdigest()


def main():
    if not os.path.exists(EXE):
        print("找不到 %s（先 go build -tags server -o build/bin/chibingren_server.exe ./cmd/workerrest）" % EXE)
        return 2
    if not os.path.exists(PROBE_JS):
        print("找不到 %s" % PROBE_JS)
        return 2

    real_cfg = os.path.join(ROOT, "config", "settings.json")
    before = digest(real_cfg)

    iso = tempfile.mkdtemp(prefix="wr_pause_")
    iso_settings = os.path.join(iso, "settings.json")
    with open(FACTORY, encoding="utf-8") as f:
        cfg = json.load(f)
    # 短休息 6 秒一条、3 秒、不提前提示：探针十几秒内就能看到「到点 / 被暂停挡住 / 恢复后照常弹」
    cfg["short"] = dict(cfg.get("short") or {}, enabled=True, mode="countdown",
                        intervalMs=6000, durationMs=3000, notifyBeforeMs=0, postponeMs=60000,
                        slots=[])
    cfg["long"] = dict(cfg.get("long") or {}, enabled=False)
    cfg["takeout"] = dict(cfg.get("takeout") or {}, enabled=False)
    cfg["customBreaks"] = []
    # 自动判定一条都不开：无头模式下它们本来就不跑，但配置侧也关干净，
    # 免得将来无头模式启用监视器时这条用例变成「环境依赖」
    cfg["dndPause"] = False
    cfg["fullscreenPause"] = False
    cfg["castMirrorPause"] = False
    cfg["castWirelessPause"] = False
    cfg["idlePauseMin"] = 60
    cfg.setdefault("appPause", {})["enabled"] = False
    with open(iso_settings, "w", encoding="utf-8") as f:
        json.dump(cfg, f, ensure_ascii=False, indent=2)

    port = free_port(29745)
    env = dict(os.environ)
    env["WAILS_SERVER_PORT"] = str(port)
    env["WAILS_SERVER_HOST"] = "127.0.0.1"
    env["WR_SETTINGS_PATH"] = iso_settings
    env.pop("ELECTRON_RUN_AS_NODE", None)
    base = "http://127.0.0.1:%d" % port
    log_path = os.path.join(iso, "server.log")
    logf = open(log_path, "w", encoding="utf-8")
    proc = subprocess.Popen([EXE], cwd=ROOT, env=env, stdout=logf, stderr=subprocess.STDOUT)
    rc = 0
    try:
        up = False
        for _ in range(60):
            if proc.poll() is not None:
                break
            if http_get(base + "/") == 200:
                up = True
                break
            time.sleep(0.5)
        if not up:
            print("无头服务没起来，日志尾部：")
            print(open(log_path, encoding="utf-8", errors="replace").read()[-1500:])
            return 1

        node = shutil.which("node")
        if not node:
            print("找不到 node")
            return 2
        penv = dict(os.environ)
        penv["WR_BASE"] = base
        r = subprocess.run([node, PROBE_JS], cwd=ROOT, env=penv, text=True,
                           encoding="utf-8", errors="replace", timeout=300, capture_output=True)
        print(r.stdout or "")
        if "结果：" not in (r.stdout or ""):
            print("!! 探针没有输出断言汇总，脚本可能提前退出")
            rc = 1
        if r.returncode != 0:
            rc = 1
            err = (r.stderr or "").strip()
            if err:
                print("--- stderr ---\n" + err[-2000:])
        if rc != 0:
            print("--- 服务日志尾部 ---")
            print(open(log_path, encoding="utf-8", errors="replace").read()[-1500:])
    finally:
        if proc.poll() is None:
            proc.terminate()
            try:
                proc.wait(timeout=5)
            except Exception:
                proc.kill()
        logf.close()
        shutil.rmtree(iso, ignore_errors=True)

    after = digest(real_cfg)
    if before != after:
        print("真实 config/settings.json 被改写了：%s" % real_cfg)
        return 1
    print("真实 settings.json 未被改动")
    return rc


if __name__ == "__main__":
    sys.exit(main())
