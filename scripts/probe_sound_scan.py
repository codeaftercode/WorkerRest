#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""提示音目录体检探针：随包 sounds/ 多出来的音频 → 询问导入；配置里的死条目 → 询问删除。

对着**真实二进制**（-tags server 的无头服务）走前端同一条 HTTP 调用链，断言五件事：
  1. 启动扫描同时报出「新音频」与「条目文件已不存在」，且内置 4 条不算新文件；
  2. ImportScannedSounds 把文件**原地登记**成相对名条目 —— 不复制、不改名、
     不生成用户音频目录（复制一份会让原文件下次启动又被当成「新」，变成每次开机都问）；
  3. 导入过的项从待办里消失，缺失待办仍在（两段各自独立）；
  4. DropMissingSounds 只删待办里的那个 id，导入的条目与 singleId 都不受牵连；
  5. 全部处理完 GetSoundScanNotice 归 null，**重启后不再重复提示**。

用法：python scripts/probe_sound_scan.py [--port 29752] [--keep]
退出码 0 = 全部通过。

前置：go build -tags server -o build/bin/chibingren_server.exe .（脚本不重建二进制）
"""

import argparse
import json
import os
import shutil
import socket
import subprocess
import sys
import time
import urllib.request
import secrets

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)
TEMP = os.environ.get("TEMP", ROOT)
EXE = os.path.join(ROOT, "build", "bin", "chibingren_server.exe")
REPORT = os.path.join(TEMP, "wr_sound_scan_report.txt")

try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass

checks = 0
failures = []


def check(ok, label, detail=""):
    global checks
    checks += 1
    print(("  ok   " if ok else "  FAIL ") + label + (("  | " + str(detail)) if detail else ""))
    if not ok:
        failures.append(label + (("  | " + str(detail)) if detail else ""))
    return ok


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


def rpc(base, name, args=()):
    payload = {
        "object": 0,
        "method": 0,
        "args": {
            "call-id": secrets.token_hex(16),
            "methodName": "main.App." + name,
            "args": list(args),
        },
    }
    req = urllib.request.Request(
        base + "/wails/runtime", data=json.dumps(payload).encode("utf-8"), method="POST"
    )
    req.add_header("Content-Type", "application/json")
    req.add_header("x-wails-client-id", secrets.token_hex(16))
    try:
        with urllib.request.urlopen(req, timeout=15) as r:
            raw = r.read().decode("utf-8", "replace")
    except Exception as e:  # noqa: BLE001
        return None, repr(e)
    try:
        return json.loads(raw), raw
    except Exception:  # noqa: BLE001
        return None, raw


def wait_ready(base, seconds=25):
    end = time.time() + seconds
    while time.time() < end:
        data, _raw = rpc(base, "GetState")
        if isinstance(data, dict):
            return True
        time.sleep(0.3)
    return False


def read_json(path):
    with open(path, encoding="utf-8") as f:
        return json.load(f)


def start(exe_local, isodir, port, logpath):
    env = dict(os.environ)
    env["WAILS_SERVER_PORT"] = str(port)
    # 不设 WR_SETTINGS_PATH：要验的是**便携自然落点**（exe 旁 config/settings.json，
    # 随包提示音目录 = exe 旁 sounds/）
    env.pop("WR_SETTINGS_PATH", None)
    env.pop("WR_STATS_PATH", None)
    logf = open(logpath, "a", encoding="utf-8", errors="replace")
    return subprocess.Popen([exe_local], cwd=isodir, env=env, stdout=logf, stderr=subprocess.STDOUT)


def stop(proc):
    if proc is not None and proc.poll() is None:
        proc.terminate()
        try:
            proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            proc.kill()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--port", type=int, default=29752)
    ap.add_argument("--keep", action="store_true", help="保留临时目录（排查用）")
    args = ap.parse_args()

    if not os.path.isfile(EXE):
        print("找不到 %s —— 先 go build -tags server -o build/bin/chibingren_server.exe ." % EXE)
        return 2

    iso = os.path.join(TEMP, "wr_sound_scan_probe")
    shutil.rmtree(iso, ignore_errors=True)
    os.makedirs(os.path.join(iso, "config"), exist_ok=True)
    cfgpath = os.path.join(iso, "config", "settings.json")

    # 出厂配置 + 一条「文件根本不存在」的条目。seeded=True 关掉首次种入内置 4 条，
    # 否则列表会被预置项污染，断言就分不清「用户条目」和「补进来的内置音」。
    cfg = read_json(os.path.join(ROOT, "default_settings.json"))
    cfg.setdefault("sound", {})
    cfg["sound"]["seeded"] = True
    cfg["sound"]["mode"] = "single"
    cfg["sound"]["singleId"] = "sm-ghost"
    cfg["sound"]["music"] = [
        {"id": "sm-ghost", "file": "ghost-missing.mp3", "name": "幽灵提示音", "enabled": True}
    ]
    with open(cfgpath, "w", encoding="utf-8", newline="\n") as f:
        json.dump(cfg, f, ensure_ascii=False, indent=2)
        f.write("\n")

    # 随包提示音目录：4 个内置音 + 用户丢进来的一个
    snd = os.path.join(iso, "sounds")
    os.makedirs(snd, exist_ok=True)
    for n in ("short_start.wav", "short_end.wav", "long_start.wav", "long_end.wav"):
        src = os.path.join(ROOT, "sounds", n)
        if os.path.isfile(src):
            shutil.copy2(src, os.path.join(snd, n))
    with open(os.path.join(snd, "my-alarm.mp3"), "wb") as f:
        f.write(b"\xff\xfb" + b"a" * 400)

    # exe 必须**拷进 iso** 再跑：直接跑 build/bin/ 那份会被判成开发态（resourcesRoot
    # 向上找 go.mod），扫描目录会跑回仓库根，探针就测不到便携落点了。
    exe_local = os.path.join(iso, "rest.exe")
    shutil.copy2(EXE, exe_local)
    logpath = os.path.join(TEMP, "wr_sound_scan_probe.log")
    open(logpath, "w").close()

    port = free_port(args.port)
    base = "http://127.0.0.1:%d" % port
    print("提示音目录体检探针：%s  (port=%d, iso=%s)" % (EXE, port, iso))

    proc = None
    try:
        proc = start(exe_local, iso, port, logpath)
        check(wait_ready(base), "无头服务已就绪", base)

        # ---- 第 1 轮：扫描结果 ----
        notice, raw = rpc(base, "GetSoundScanNotice")
        ok = isinstance(notice, dict)
        check(ok, "启动扫描有结果（GetSoundScanNotice 非 null）", raw)
        if ok:
            new_files = [f.get("file") for f in (notice.get("new") or [])]
            miss_ids = [m.get("id") for m in (notice.get("missing") or [])]
            check(new_files == ["my-alarm.mp3"],
                  "只把未登记的音频报成「新」（内置 4 条不算）", new_files)
            check(miss_ids == ["sm-ghost"], "把条目文件缺失报出来", miss_ids)
            check(str(notice.get("dir", "")).replace("\\", "/").endswith("/sounds"),
                  "扫描的是 exe 旁的 sounds/", notice.get("dir"))
            nf = (notice.get("new") or [{}])[0]
            check(nf.get("name") == "my-alarm", "新音频的显示名去掉了扩展名", nf.get("name"))
            check(isinstance(nf.get("size"), int) and nf.get("size") > 0, "带文件大小", nf.get("size"))

        # ---- 导入：原地登记 ----
        r, raw = rpc(base, "ImportScannedSounds", [["my-alarm.mp3"]])
        check(isinstance(r, dict) and r.get("imported") == 1, "导入 1 个新提示音", r or raw)
        check(os.path.isfile(os.path.join(snd, "my-alarm.mp3")),
              "原文件仍在（没有复制 / 移动 / 改名）", "")
        check(not os.path.exists(os.path.join(iso, "config", "sounds")),
              "没有生成导入副本目录（原地登记，不复制）", "")

        cfg2 = read_json(cfgpath)
        music2 = cfg2.get("sound", {}).get("music", [])
        hit = [m for m in music2 if m.get("file") == "my-alarm.mp3"]
        check(len(hit) == 1, "落盘配置里出现该条目", [m.get("file") for m in music2])
        if hit:
            check(not os.path.isabs(hit[0].get("file", "")),
                  "登记的是相对文件名（播放走内置音路由）", hit[0].get("file"))
            check(bool(hit[0].get("id")) and hit[0].get("enabled") is True,
                  "条目带 id 且默认启用", hit[0])

        # ---- 待办各自独立 ----
        n2, _ = rpc(base, "GetSoundScanNotice")
        if check(isinstance(n2, dict), "还有缺失待办时 notice 仍在", n2):
            check(not (n2.get("new") or []), "导入过的项从待办里消失", n2.get("new"))
            check([m.get("id") for m in (n2.get("missing") or [])] == ["sm-ghost"],
                  "缺失待办不受影响", n2.get("missing"))

        # ---- 删除死条目 ----
        r, raw = rpc(base, "DropMissingSounds", [["sm-ghost"]])
        check(isinstance(r, dict) and r.get("removed") == 1, "删掉 1 条文件缺失的条目", r or raw)
        cfg3 = read_json(cfgpath)
        snd3 = cfg3.get("sound", {})
        ids3 = [m.get("id") for m in snd3.get("music", [])]
        files3 = [m.get("file") for m in snd3.get("music", [])]
        check("sm-ghost" not in ids3, "配置里不再有幽灵条目", ids3)
        check("my-alarm.mp3" in files3, "导入的那条没被误删", files3)
        check(snd3.get("singleId") not in ("sm-ghost", ""),
              "singleId 没留下死引用", snd3.get("singleId"))

        # ---- 待办清空 ----
        n3, raw3 = rpc(base, "GetSoundScanNotice")
        check(n3 is None, "全部处理完 notice 归 null", raw3)

        stop(proc)
        proc = None

        # ---- 第 2 轮：重启后不再重复提示 ----
        proc = start(exe_local, iso, port, logpath)
        check(wait_ready(base), "重启后就绪", base)
        n4, raw4 = rpc(base, "GetSoundScanNotice")
        check(n4 is None, "重启后不再重复提示（导入过的文件不再算「新」）", raw4)
    finally:
        stop(proc)

    text = open(logpath, encoding="utf-8", errors="replace").read()
    check("[sound] 随包提示音目录" in text, "日志留痕：启动扫描结果", "")
    check("[sound] 已导入" in text, "日志留痕：导入动作", "")
    check("[sound] 已从配置删除" in text, "日志留痕：删除动作", "")
    check("panic" not in text.lower(), "日志里没有 panic", "")

    with open(REPORT, "w", encoding="utf-8") as f:
        f.write("提示音目录体检探针：%d 项断言，失败 %d 项\n\n" % (checks, len(failures)))
        f.write("\n".join(failures) if failures else "（全部通过）")
        f.write("\n\n--- 日志尾部 ---\n" + text[-2500:])
    print("\n结果：%d 项断言，失败 %d 项" % (checks, len(failures)))
    print("报告：%s" % REPORT)
    if not args.keep:
        shutil.rmtree(iso, ignore_errors=True)
    return 0 if not failures else 1


if __name__ == "__main__":
    sys.exit(main())
