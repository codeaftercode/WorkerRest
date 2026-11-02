#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""设置页 UI 全扫描的驱动：起无头服务 → 跑三个 JS 探针 → 收断言。

三个 JS 探针分工：
  · settings_ui_probe.js     —— 广度扫描：六个 tab 的可见控件、可访问名、无横向溢出、
                                关键控件值与配置一致、十个对话框的静态语义契约；
  · settings_modals_probe.js —— 交互动作：对话框真点击打开、焦点进框/归位、Esc 关闭、
                                Tab 焦点陷阱、点遮罩关闭；
  · settings_flow_probe.js   —— 写入链路：控件改动 → 落盘 → 读回 → 主题 / 快捷键 /
                                导入校验 / 恢复默认。

为什么单独一个驱动：JS 侧只负责「打开页面 + 断言」，起服务、隔离配置、清理进程
这些 Python 侧更顺手（与 probe_renderer.py 同一分工）。三个脚本共用一个服务，
省掉两次冷启动。

隔离口径：配置用出厂默认的一份副本放到临时目录，`WR_SETTINGS_PATH` 指过去；
跑完断言仓库里真实的 config/settings.json 没被动过。

用法：python scripts/probe/probe_settings_ui.py
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
import urllib.error
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(os.path.dirname(HERE))
EXE = os.path.join(ROOT, "build", "bin", "chibingren_server.exe")
FACTORY = os.path.join(ROOT, "internal", "settings", "default_settings.json")
PROBE_JS = [
    os.path.join(HERE, "settings_ui_probe.js"),
    os.path.join(HERE, "settings_modals_probe.js"),
    os.path.join(HERE, "settings_flow_probe.js"),
]
OUT = os.path.join(tempfile.gettempdir(), "wr_settings_ui")

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
        print("找不到 %s" % EXE)
        return 2
    for js in PROBE_JS:
        if not os.path.exists(js):
            print("找不到 %s" % js)
            return 2

    real_cfg = os.path.join(ROOT, "config", "settings.json")
    before = digest(real_cfg)

    iso = tempfile.mkdtemp(prefix="wr_ui_")
    iso_settings = os.path.join(iso, "settings.json")
    with open(FACTORY, encoding="utf-8") as f:
        cfg = json.load(f)
    # 让立绘库、提示音面板都有内容可渲染（出厂默认里它们本来就是开的，这里显式确保）
    cfg.setdefault("supervisor", {})["enabled"] = True
    cfg.setdefault("sound", {})["enabled"] = True
    with open(iso_settings, "w", encoding="utf-8") as f:
        json.dump(cfg, f, ensure_ascii=False, indent=2)

    port = free_port(29741)
    log_path = os.path.join(iso, "server.log")
    env = dict(os.environ)
    env["WAILS_SERVER_PORT"] = str(port)
    env["WAILS_SERVER_HOST"] = "127.0.0.1"
    env["WR_SETTINGS_PATH"] = iso_settings
    env["WR_LOG_LEVEL"] = "debug"
    env.pop("ELECTRON_RUN_AS_NODE", None)
    base = "http://127.0.0.1:%d" % port
    logf = open(log_path, "w", encoding="utf-8")
    proc = subprocess.Popen([EXE], cwd=ROOT, env=env, stdout=logf, stderr=subprocess.STDOUT)
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

        penv = dict(os.environ)
        penv["WR_BASE"] = base
        penv["WR_OUT"] = OUT
        node = shutil.which("node")
        if not node:
            print("找不到 node")
            return 2
        rc = 0
        for js in PROBE_JS:
            print("\n===== %s =====" % os.path.basename(js))
            # 捕获而不是继承 stdout：继承时 node 的输出直奔文件、Python 的 print 还在
            # 块缓冲里，日志顺序会变成「所有断言在前、所有分节标题在后」，出问题极难对位。
            r = subprocess.run([node, js], cwd=ROOT, env=penv, text=True,
                               encoding="utf-8", errors="replace", timeout=300,
                               capture_output=True)
            print(r.stdout or "")
            if "结果：" not in (r.stdout or ""):
                # 脚本没跑到收尾就退了（例如开浏览器失败）：不能让「0 项断言」冒充通过
                rc = 1
                print("!! %s 没有输出断言汇总，脚本可能提前退出" % os.path.basename(js))
            if r.returncode != 0:
                rc = 1
                err = (r.stderr or "").strip()
                if err:
                    print("--- stderr ---\n" + err[-2000:])
        if rc != 0:
            print("设置页探针有失败项（见上）", file=sys.stderr)
            return 1
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
        print("真实 config/settings.json 被改写了：%s" % real_cfg, file=sys.stderr)
        return 1
    print("真实 settings.json 未被改动")
    print("截图：%s" % OUT)
    return 0


if __name__ == "__main__":
    sys.exit(main())
