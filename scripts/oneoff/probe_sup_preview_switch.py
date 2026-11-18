#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""一次性：督休专员卡片内预览面板两个问题的复现（驱动端）。

1) 名字牌（含墨块 / 碎点）与立绘的层序：应压在立绘之下。
2) 切换专员时立绘出现矩形色块（透明度没处理好）。

隔离口径同 shot_supervisor.py：临时 settings.json + WR_SETTINGS_PATH，
督休专员目录用仓库自带的（只读）。输出到 %TEMP%/wr_sup_switch，不入库。
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
EXE = os.path.join(ROOT, "build", "bin", "rest_server.exe")
FACTORY = os.path.join(ROOT, "internal", "settings", "default_settings.json")
PROBE_JS = os.path.join(HERE, "probe_sup_preview_switch.js")
OUT = os.path.join(tempfile.gettempdir(), "wr_sup_switch")


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
    real_cfg = os.path.join(ROOT, "config", "settings.json")
    before = digest(real_cfg)
    os.makedirs(OUT, exist_ok=True)

    iso = tempfile.mkdtemp(prefix="wr_sw_")
    iso_settings = os.path.join(iso, "settings.json")
    with open(FACTORY, encoding="utf-8") as f:
        cfg = json.load(f)
    cfg.setdefault("supervisor", {})["enabled"] = True
    with open(iso_settings, "w", encoding="utf-8") as f:
        json.dump(cfg, f, ensure_ascii=False, indent=2)

    port = free_port(29771)
    env = dict(os.environ)
    env["WAILS_SERVER_PORT"] = str(port)
    env["WAILS_SERVER_HOST"] = "127.0.0.1"
    env["WR_SETTINGS_PATH"] = iso_settings
    env.pop("ELECTRON_RUN_AS_NODE", None)
    base = "http://127.0.0.1:%d" % port
    logf = open(os.path.join(iso, "server.log"), "w", encoding="utf-8")
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
            print("服务没起来")
            return 1
        penv = dict(os.environ)
        penv.update({
            "WR_BASE": base,
            "WR_OUT": OUT,
            "WR_PRELOAD": os.path.join(ROOT, "scripts", "probe_preload.js"),
            "NODE_PATH": os.path.join(ROOT, "scripts", "node_modules"),
        })
        r = subprocess.run(["node", PROBE_JS], cwd=ROOT, env=penv, text=True,
                           encoding="utf-8", errors="replace", timeout=300,
                           capture_output=True)
        data = None
        for line in (r.stdout or "").splitlines():
            if line.startswith("WR_JSON "):
                data = json.loads(line[len("WR_JSON "):])
        if data is None:
            print("探针没有回传数据：")
            print((r.stdout or "")[-2000:])
            print((r.stderr or "")[-3000:])
            return 1
        with open(os.path.join(OUT, "report.json"), "w", encoding="utf-8") as f:
            json.dump(data, f, ensure_ascii=False, indent=2)
        print("names:", data["names"])
        print("layers:", json.dumps(data["layers"], ensure_ascii=False))
        for row in data["rows"]:
            print("---", row["idx"], row["name"])
            for i, st in enumerate(row["frames"]):
                print("  f%-2d nat=%-11s srcLen=%-7d vis=%-8s tf=%-40s alpha=%s hit=%s" % (
                    i, str(st.get("nat")), st.get("srcLen", 0), st.get("trimVis"),
                    (st.get("trimTfInline") or "")[:40], st.get("alpha"),
                    json.dumps(st.get("hit"), ensure_ascii=False)))
            st = row["steady"]
            print("  steady nat=%s vis=%s nameZ=%s figZ=%s contentZ=%s filter=%s" % (
                st.get("nat"), st.get("trimVis"), st.get("nameZ"), st.get("figZ"),
                st.get("contentZ"), (st.get("imgFilter") or "")[:40]))
        print("二阶段:", json.dumps(data["stage2"], ensure_ascii=False)[:500])
        print("输出目录：", OUT)
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
        print("!! 真实 config/settings.json 被改写")
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
