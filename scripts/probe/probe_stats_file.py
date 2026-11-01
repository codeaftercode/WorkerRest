#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""统计文件自愈探针：首跑自建 data/stats.json、损坏时改名备份 + 重建 + 弹窗告知。

对着**真实二进制**（-tags server 的无头服务）跑，走前端同一条 HTTP 调用链，断言四件事：
  1. 没有 data/ 与 stats.json 时，启动后程序自己把目录和空表建出来（打包不再预置它们）；
  2. 空表内容是合法 JSON 且只含 `_customNames` 保留键（与 marshalLocked 同格式）；
  3. 把文件改坏后重启：原内容被改名成 `stats_<日期_时间>.json` 原样保留，
     原路径换回空表，且 GetStatsFileNotice 给出「备份在哪 / 为什么判定损坏」；
  4. AckStatsFileNotice 之后说明清空（同一次运行不再弹）。

用法：python scripts/probe_stats_file.py [--port 29751] [--keep]
退出码 0 = 全部通过。

前置：go build -tags server -o build/bin/chibingren_server.exe .（脚本不重建二进制）
"""

import argparse
import glob
import json
import os
import shutil
import socket
import subprocess
import sys
import time
import urllib.error
import urllib.request
import secrets

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(os.path.dirname(HERE))
TEMP = os.environ.get("TEMP", ROOT)
EXE = os.path.join(ROOT, "build", "bin", "chibingren_server.exe")
REPORT = os.path.join(TEMP, "wr_stats_file_probe_report.txt")

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
    req = urllib.request.Request(base + "/wails/runtime", data=json.dumps(payload).encode("utf-8"), method="POST")
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
    """等无头服务起监听：能拿到 GetState 就算就绪。"""
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
    # 不设 WR_SETTINGS_PATH / WR_STATS_PATH：要验的是**便携自然落点**
    #（exe 旁 config/settings.json → 统计落到 exe 旁 data/stats.json）
    env.pop("WR_SETTINGS_PATH", None)
    env.pop("WR_STATS_PATH", None)
    logf = open(logpath, "a", encoding="utf-8", errors="replace")
    return subprocess.Popen([exe_local], cwd=isodir, env=env, stdout=logf, stderr=subprocess.STDOUT)


def stop(proc):
    if proc.poll() is None:
        proc.terminate()
        try:
            proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            proc.kill()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--port", type=int, default=29751)
    ap.add_argument("--keep", action="store_true", help="保留临时目录（排查用）")
    args = ap.parse_args()

    if not os.path.isfile(EXE):
        print("找不到 %s —— 先 go build -tags server -o build/bin/chibingren_server.exe ." % EXE)
        return 2

    iso = os.path.join(TEMP, "wr_stats_file_probe")
    shutil.rmtree(iso, ignore_errors=True)
    os.makedirs(os.path.join(iso, "config"), exist_ok=True)
    # 出厂配置：优先用仓库里那份（与打包写进 config/settings.json 的同源）
    factory = os.path.join(ROOT, "default_settings.json")
    shutil.copy2(factory, os.path.join(iso, "config", "settings.json"))
    # exe 必须**拷进 iso** 再跑：直接跑 build/bin/ 里的那份会被判成开发态
    #（resourcesRoot 向上找 go.mod），落点会跑回仓库根，探针就测不到便携落点了。
    exe_local = os.path.join(iso, "rest.exe")
    shutil.copy2(EXE, exe_local)
    logpath = os.path.join(TEMP, "wr_stats_file_probe.log")
    open(logpath, "w").close()

    stats = os.path.join(iso, "data", "stats.json")
    port = free_port(args.port)
    base = "http://127.0.0.1:%d" % port
    print("统计文件自愈探针：%s  (port=%d, iso=%s)" % (EXE, port, iso))

    # ---- 第 1 轮：没有 data/ 与 stats.json ----
    check(not os.path.exists(stats), "起跑前 data/stats.json 不存在（模拟首跑 / 新装）", stats)
    proc = start(exe_local, iso, port, logpath)
    try:
        up = wait_ready(base)
        check(up, "无头服务已就绪", base)
        # Load 在 ServiceStartup 里同步跑完，服务就绪时文件应已建好
        ok = False
        for _ in range(40):
            if os.path.isfile(stats):
                ok = True
                break
            time.sleep(0.25)
        check(ok, "首跑自动创建 data/stats.json", stats)
        if ok:
            try:
                data = read_json(stats)
                check(isinstance(data, dict) and "_customNames" in data and len(data) == 1,
                      "自建空表是合法空表（只含 _customNames）", data)
            except Exception as e:  # noqa: BLE001
                check(False, "自建空表可解析", repr(e))
        notice, raw = rpc(base, "GetStatsFileNotice")
        check(notice is None and str(raw).strip() in ("null", "", "None"),
              "首跑不产生「文件损坏」说明（GetStatsFileNotice 为 null）", raw)
    finally:
        stop(proc)

    # ---- 第 2 轮：把统计文件改坏 ----
    bad = b"{ this is not json"
    with open(stats, "wb") as f:
        f.write(bad)
    proc = start(exe_local, iso, port, logpath)
    try:
        up = wait_ready(base)
        check(up, "损坏文件后服务仍能起来（不 panic）", base)
        backups = glob.glob(os.path.join(iso, "data", "stats_*.json"))
        check(len(backups) == 1, "损坏文件已改名备份（data/stats_<日期_时间>.json）", backups)
        if backups:
            with open(backups[0], "rb") as f:
                check(f.read() == bad, "备份内容 == 损坏前的原始字节（数据没被销毁）", backups[0])
            name = os.path.basename(backups[0])
            check(name.startswith("stats_") and name.endswith(".json"),
                  "备份命名符合 stats_<日期_时间>.json", name)
        if os.path.isfile(stats):
            try:
                data = read_json(stats)
                check(isinstance(data, dict) and set(data.keys()) == {"_customNames"},
                      "原路径已换回空表", data)
            except Exception as e:  # noqa: BLE001
                check(False, "重建后的空表可解析", repr(e))
        else:
            check(False, "原路径已换回空表", "文件不存在")

        notice, raw = rpc(base, "GetStatsFileNotice")
        ok = isinstance(notice, dict)
        check(ok, "GetStatsFileNotice 返回处置说明（界面据此弹窗）", raw)
        if ok:
            check(bool(notice.get("backup")) and os.path.isfile(str(notice["backup"])),
                  "说明里的备份路径真实存在", notice.get("backup"))
            check(bool(notice.get("reason")), "说明里带判定原因", notice.get("reason"))
            check(bool(notice.get("at")), "说明里带处置时刻", notice.get("at"))
            check(notice.get("file") == stats, "说明里的文件路径 = 统计落点", notice.get("file"))
        ack, _ = rpc(base, "AckStatsFileNotice")
        check(isinstance(ack, dict) and ack.get("ok") is True, "AckStatsFileNotice 返回 {ok:true}", ack)
        after, raw2 = rpc(base, "GetStatsFileNotice")
        check(after is None, "Ack 之后说明清空（同一次运行不再弹）", raw2)
    finally:
        stop(proc)

    text = open(logpath, encoding="utf-8", errors="replace").read()
    check("[stats] 统计文件损坏" in text, "日志留痕：损坏 → 备份 → 重建", "")
    check("panic" not in text.lower(), "日志里没有 panic", "")

    with open(REPORT, "w", encoding="utf-8") as f:
        f.write("统计文件自愈探针：%d 项断言，失败 %d 项\n\n" % (checks, len(failures)))
        f.write("\n".join(failures) if failures else "（全部通过）")
        f.write("\n\n--- 日志尾部 ---\n" + text[-2500:])
    print("\n结果：%d 项断言，失败 %d 项" % (checks, len(failures)))
    print("报告：%s" % REPORT)
    if not args.keep:
        shutil.rmtree(iso, ignore_errors=True)
    return 0 if not failures else 1


if __name__ == "__main__":
    sys.exit(main())
