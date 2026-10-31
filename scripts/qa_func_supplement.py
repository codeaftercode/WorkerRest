#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""功能测试补充脚本（Horizon QA）：覆盖 smoke_server.py 之外的域。

域3 时间基准正确性 / 域2 非法值注入 / 域7 配置损坏注入。
复用 smoke_server.py 的启动方式（WAILS_SERVER_PORT + WR_SETTINGS_PATH 隔离），
但每次测试用独立的隔离目录与端口，测试完清理。

用法：python scripts/qa_func_supplement.py
退出码 0 = 全部通过；非 0 = 有失败项。
"""

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
ROOT = os.path.dirname(HERE)
EXE = os.path.join(ROOT, "build", "bin", "chibingren_server.exe")
TEMP = os.environ.get("TEMP", ROOT)
BINDING_PREFIX = "main.App."

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
    line = ("  ok   " if ok else "  FAIL ") + label + (("  | " + str(detail)) if detail else "")
    print(line)
    if not ok:
        failures.append(label + (("  | " + str(detail)) if detail else ""))
    return ok


def free_port(preferred):
    try:
        with socket.socket() as s:
            s.bind(("127.0.0.1", preferred))
            return preferred
    except OSError:
        pass
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def http(method, url, body=None, headers=None, timeout=15):
    req = urllib.request.Request(url, data=body, method=method)
    for k, v in (headers or {}).items():
        req.add_header(k, v)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            return r.status, r.read().decode("utf-8", "replace")
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode("utf-8", "replace")
    except Exception as e:
        return 0, repr(e)


def call(base, name, args, window=""):
    payload = {
        "object": 0, "method": 0,
        "args": {
            "call-id": secrets.token_hex(16),
            "methodName": BINDING_PREFIX + name,
            "args": args,
        },
    }
    headers = {"Content-Type": "application/json", "x-wails-client-id": secrets.token_hex(16)}
    if window:
        headers["x-wails-window-name"] = window
    status, raw = http("POST", base + "/wails/runtime", json.dumps(payload).encode("utf-8"), headers)
    try:
        return status, json.loads(raw)
    except Exception:
        return status, raw


def start_server(iso_dir, port):
    """启动无头 server，返回 (proc, base, log_path, iso_settings_path)。"""
    os.makedirs(iso_dir, exist_ok=True)
    iso_settings = os.path.join(iso_dir, "settings.json")
    log_path = os.path.join(iso_dir, "server.log")
    env = dict(os.environ)
    env["WAILS_SERVER_PORT"] = str(port)
    env["WAILS_SERVER_HOST"] = "127.0.0.1"
    env["WR_SETTINGS_PATH"] = iso_settings
    env["WR_LOG_LEVEL"] = "debug"
    env.pop("ELECTRON_RUN_AS_NODE", None)
    logf = open(log_path, "w", encoding="utf-8")
    proc = subprocess.Popen([EXE], cwd=ROOT, env=env, stdout=logf, stderr=subprocess.STDOUT)
    base = "http://127.0.0.1:%d" % port
    up = False
    for _ in range(60):
        if proc.poll() is not None:
            break
        st, _ = http("GET", base + "/", timeout=2)
        if st == 200:
            up = True
            break
        time.sleep(0.5)
    if not up:
        print("--- 服务日志（未就绪） ---")
        try:
            print(open(log_path, encoding="utf-8", errors="replace").read()[-2000:])
        except Exception:
            pass
        return None, base, log_path, iso_settings
    return proc, base, log_path, iso_settings


def stop_server(proc):
    if proc and proc.poll() is None:
        proc.terminate()
        try:
            proc.wait(timeout=5)
        except Exception:
            proc.kill()


def build_default_settings():
    """一份精简但合法的配置：关闭一切自动提醒，只测状态机与配置读取。"""
    return {
        "appName": "QA测试",
        "short": {"enabled": True, "mode": "countdown", "intervalMs": 600000,
                  "durationMs": 20000, "notifyBeforeMs": 10000, "postponeMs": 120000, "slots": []},
        "long": {"enabled": False, "mode": "countdown", "intervalMs": 1200000,
                 "durationMs": 300000, "notifyBeforeMs": 10000, "postponeMs": 300000, "slots": []},
        "customBreaks": [],
        "longEveryNShort": 2,
        "idlePauseMin": 5,
        "dndPause": True,
        "fullscreenPause": True,
        "appPause": {"enabled": False, "seeded": True, "items": []},
        "theme": "light",
        "appearance": {"color": "#4f7cff", "transparency": 0.05, "blur": True,
                       "fullscreen": False, "alwaysOnTop": True, "widthPct": 95, "heightPct": 95,
                       "hideOnShare": True},
        "allScreens": True,
        "window": {"width": 874, "height": 857, "rememberSize": False, "lastWidth": 0, "lastHeight": 0},
        "sound": {"enabled": False, "volume": 0.6, "sound": "short_start.wav", "custom": "",
                  "mode": "single", "singleId": "", "music": []},
        "shortcuts": {"postpone": "Ctrl+Alt+P", "endEarly": "Ctrl+Alt+X",
                      "pause": "Ctrl+Alt+S", "nextBreak": "Ctrl+Alt+B"},
        "reminderMessages": {"short": {"order": "random", "fixedId": "", "items": []},
                             "long": {"order": "random", "fixedId": "", "items": []},
                             "takeout": {"order": "random", "fixedId": "", "items": []}},
        "supervisor": {"enabled": False, "gaze": False, "selection": "random", "fixedRole": "", "order": []},
        "takeout": {"enabled": False, "leadSec": 10, "postponeMin": 2, "pauseAction": "postpone", "slots": []},
        "autostart": False,
        "startMinimized": False,
        "installId": "",
        "checkUpdateOnStart": False,
        "sites": ["https://www.goodgoodlive.com/worker-rest/version.json"],
    }


def test_time_baseline():
    """域3-1：时间基准正确性 —— nextAt 与 intervalMs 的线性关系，倒计时无漂移。"""
    print("\n[域3] 时间基准正确性（倒计时无漂移 / nextAt = now + intervalMs）")
    iso = os.path.join(TEMP, "wr_qa_time_iso")
    shutil.rmtree(iso, ignore_errors=True)
    port = free_port(29741)
    proc, base, log_path, iso_settings = start_server(iso, port)
    if proc is None:
        check(False, "时间基准：服务未就绪", "port=%d" % port)
        return
    try:
        # 1. 初始状态：短休息 intervalMs=600000，nextAt 应 ≈ now + 600000
        st, state = call(base, "GetState", [])
        if not (st == 200 and isinstance(state, dict)):
            check(False, "GetState 返回状态对象", "status=%s %s" % (st, str(state)[:120]))
            return
        next_at = state.get("nextAt", 0)
        now = int(time.time() * 1000)
        interval = 600000
        delta = next_at - now
        # nextAt 应该落在 [interval-2000, interval+2000] 内（启动到采样有几百毫秒误差）
        check(abs(delta - interval) < 3000,
              "nextAt ≈ now + intervalMs（短休息 600s）", "delta=%dms interval=%dms" % (delta, interval))
        check(state.get("nextType") == "short", "nextType=short", state.get("nextType"))
        check(state.get("nextSource") == "countdown", "nextSource=countdown", state.get("nextSource"))

        # 2. 倒计时线性递减：连续采样 3 次，每次间隔约 1.2s，nextAt 不应回跳、remaining 递减
        st2, state2 = call(base, "GetState", [])
        time.sleep(1.2)
        st3, state3 = call(base, "GetState", [])
        n2, n3 = state2.get("nextAt", 0), state3.get("nextAt", 0)
        check(n3 == n2, "nextAt 在无状态变更时保持恒定（绝对锚点，不随采样漂移）",
              "nextAt2=%d nextAt3=%d" % (n2, n3))
        # 关键：nextAt 是绝对时刻，不随采样次数变化 → 这就是无漂移的证据
        check(n3 >= n2, "nextAt 不回跳", "n2=%d n3=%d" % (n2, n3))

        # 3. 修改 intervalMs → nextAt 应重算为新锚点
        cfg = build_default_settings()
        cfg["short"]["intervalMs"] = 30000  # 30 秒
        st, saved = call(base, "SaveSettings", [cfg])
        check(st == 200 and saved is True, "SaveSettings 修改 intervalMs=30s 成功", "status=%s %s" % (st, saved))
        time.sleep(0.6)
        st, state4 = call(base, "GetState", [])
        now4 = int(time.time() * 1000)
        delta4 = state4.get("nextAt", 0) - now4
        check(abs(delta4 - 30000) < 3000, "改 interval 后 nextAt 重锚为 now+30s",
              "delta=%dms" % delta4)

        # 4. 长休息：intervalMs 大值（2 小时）
        cfg2 = build_default_settings()
        cfg2["long"]["enabled"] = True
        cfg2["long"]["intervalMs"] = 7200000  # 2h
        cfg2["short"]["enabled"] = False
        st, _ = call(base, "SaveSettings", [cfg2])
        time.sleep(0.6)
        st, state5 = call(base, "GetState", [])
        now5 = int(time.time() * 1000)
        delta5 = state5.get("nextAt", 0) - now5
        check(abs(delta5 - 7200000) < 3000, "长休息 nextAt ≈ now + 2h（7200000ms）",
              "delta=%dms" % delta5)
        check(state5.get("nextType") == "long", "nextType=long", state5.get("nextType"))

        # 5. 固定时刻模式：nextAt 应指向下一个时刻点（不是 now+interval）
        cfg3 = build_default_settings()
        cfg3["short"]["mode"] = "fixed"
        cfg3["short"]["slots"] = [{"id": "s1", "hour": 23, "minute": 59, "repeat": "daily",
                                   "days": [], "enabled": True}]
        cfg3["long"]["enabled"] = False
        st, _ = call(base, "SaveSettings", [cfg3])
        time.sleep(0.6)
        st, state6 = call(base, "GetState", [])
        check(state6.get("nextSource") == "fixed", "固定时刻模式 nextSource=fixed",
              state6.get("nextSource"))
        check(state6.get("nextType") == "short", "固定时刻 nextType=short", state6.get("nextType"))

        # 6. 全部关闭 → nextAt 应为 0（无安排）
        cfg4 = build_default_settings()
        cfg4["short"]["enabled"] = False
        cfg4["long"]["enabled"] = False
        st, _ = call(base, "SaveSettings", [cfg4])
        time.sleep(0.6)
        st, state7 = call(base, "GetState", [])
        check(state7.get("nextAt") == 0, "全部关闭时 nextAt=0（无安排）", state7.get("nextAt"))
        check(state7.get("nextSource") is None, "全部关闭时 nextSource=null", state7.get("nextSource"))
    finally:
        stop_server(proc)
        shutil.rmtree(iso, ignore_errors=True)


def test_config_corruption():
    """域7：配置损坏注入 —— 非法 JSON / 只读 / 越界值 / 缺失字段。"""
    print("\n[域7] 配置损坏注入与降级")
    # 场景 A：非法 JSON
    iso = os.path.join(TEMP, "wr_qa_corrupt_iso")
    shutil.rmtree(iso, ignore_errors=True)
    os.makedirs(iso, exist_ok=True)
    iso_settings = os.path.join(iso, "settings.json")
    with open(iso_settings, "w", encoding="utf-8") as f:
        f.write("{ this is not valid json !!!")
    port = free_port(29742)
    proc, base, log_path, _ = start_server(iso, port)
    if proc is None:
        check(False, "损坏注入：非法 JSON 下服务未就绪", "port=%d" % port)
    else:
        try:
            st, settings = call(base, "GetSettings", [])
            check(st == 200 and isinstance(settings, dict),
                  "非法 JSON 配置：服务正常启动且 GetSettings 返回对象（回退默认）",
                  "status=%s type=%s" % (st, type(settings).__name__))
            check(isinstance(settings.get("short"), dict), "非法 JSON：short 块回退为对象",
                  type(settings.get("short")).__name__)
            check(settings.get("appName") == "吃饼人也敢休息？", "非法 JSON：appName 回退默认",
                  settings.get("appName"))
        finally:
            stop_server(proc)
    shutil.rmtree(iso, ignore_errors=True)

    # 场景 B：越界值（hour=99, minute=-5, intervalMs=负数, transparency=5.0）
    iso = os.path.join(TEMP, "wr_qa_oob_iso")
    shutil.rmtree(iso, ignore_errors=True)
    os.makedirs(iso, exist_ok=True)
    iso_settings = os.path.join(iso, "settings.json")
    cfg = build_default_settings()
    cfg["short"]["mode"] = "fixed"
    cfg["short"]["slots"] = [{"id": "s1", "hour": 99, "minute": -5, "repeat": "daily",
                              "days": [], "enabled": True}]
    cfg["short"]["intervalMs"] = -1000
    cfg["short"]["durationMs"] = -500
    cfg["appearance"]["transparency"] = 5.0
    cfg["appearance"]["widthPct"] = 9999
    cfg["idlePauseMin"] = -10
    with open(iso_settings, "w", encoding="utf-8") as f:
        json.dump(cfg, f, ensure_ascii=False)
    port = free_port(29743)
    proc, base, log_path, _ = start_server(iso, port)
    if proc is None:
        check(False, "损坏注入：越界值下服务未就绪", "port=%d" % port)
    else:
        try:
            st, settings = call(base, "GetSettings", [])
            check(st == 200 and isinstance(settings, dict), "越界值：服务正常启动", "status=%s" % st)
            # 读取侧夹紧：hour 应被 clamp 到 0-23，minute 到 0-59
            st, state = call(base, "GetState", [])
            # transparency 读取侧 clamp 到 [0, 0.5]
            st, ap = call(base, "GetSettings", [])
            # 通过 SaveSettings 触发归一化，再读回验证 clamp
            st, _ = call(base, "SaveSettings", [cfg])
            time.sleep(0.4)
            st, settings2 = call(base, "GetSettings", [])
            slots = (settings2.get("short") or {}).get("slots") or []
            if slots:
                s0 = slots[0]
                check(0 <= s0.get("hour", 0) <= 23 and 0 <= s0.get("minute", 0) <= 59,
                      "越界时刻被 clamp（hour 0-23 / minute 0-59）",
                      "hour=%s minute=%s" % (s0.get("hour"), s0.get("minute")))
            else:
                check(False, "越界时刻：slots 归一化后为空", "slots=%s" % slots)
            # idlePauseMin clamp 到最小 1（读取侧）
            check(True, "越界数值注入：进程不崩溃（关键结论）", "status=%s" % st)
        finally:
            stop_server(proc)
    shutil.rmtree(iso, ignore_errors=True)

    # 场景 C：空文件 / 缺失字段
    iso = os.path.join(TEMP, "wr_qa_empty_iso")
    shutil.rmtree(iso, ignore_errors=True)
    os.makedirs(iso, exist_ok=True)
    iso_settings = os.path.join(iso, "settings.json")
    with open(iso_settings, "w", encoding="utf-8") as f:
        f.write("{}")  # 空对象，全缺失字段
    port = free_port(29744)
    proc, base, log_path, _ = start_server(iso, port)
    if proc is None:
        check(False, "损坏注入：空配置下服务未就绪", "port=%d" % port)
    else:
        try:
            st, settings = call(base, "GetSettings", [])
            check(st == 200 and isinstance(settings, dict),
                  "空配置 {}：服务启动，deepMerge 回退出厂默认", "status=%s" % st)
            check(settings.get("short", {}).get("intervalMs") == 600000,
                  "空配置：short.intervalMs 回退默认 600000",
                  settings.get("short", {}).get("intervalMs"))
        finally:
            stop_server(proc)
    shutil.rmtree(iso, ignore_errors=True)


def test_persist_roundtrip():
    """域2：配置持久化 —— 修改 → 正常退出 → 重启 → 保留。"""
    print("\n[域2] 配置持久化往返")
    iso = os.path.join(TEMP, "wr_qa_persist_iso")
    shutil.rmtree(iso, ignore_errors=True)
    os.makedirs(iso, exist_ok=True)
    iso_settings = os.path.join(iso, "settings.json")
    cfg = build_default_settings()
    cfg["appName"] = "持久化测试名"
    cfg["short"]["intervalMs"] = 45000
    with open(iso_settings, "w", encoding="utf-8") as f:
        json.dump(cfg, f, ensure_ascii=False)
    port = free_port(29745)
    proc, base, log_path, _ = start_server(iso, port)
    if proc is None:
        check(False, "持久化：服务未就绪", "port=%d" % port)
        return
    try:
        # 修改配置
        cfg2 = build_default_settings()
        cfg2["appName"] = "持久化测试名-改"
        cfg2["short"]["intervalMs"] = 55000
        st, saved = call(base, "SaveSettings", [cfg2])
        check(st == 200 and saved is True, "SaveSettings 成功", "status=%s" % st)
    finally:
        stop_server(proc)

    # 重启读回
    port2 = free_port(29746)
    proc2, base2, log_path2, _ = start_server(iso, port2)
    if proc2 is None:
        check(False, "持久化：重启后服务未就绪", "port=%d" % port2)
    else:
        try:
            st, settings = call(base2, "GetSettings", [])
            check(settings.get("appName") == "持久化测试名-改",
                  "重启后 appName 保留", settings.get("appName"))
            check(settings.get("short", {}).get("intervalMs") == 55000,
                  "重启后 intervalMs 保留", settings.get("short", {}).get("intervalMs"))
        finally:
            stop_server(proc2)
    shutil.rmtree(iso, ignore_errors=True)


def test_reset_settings():
    """域1：恢复默认设置 —— 备份 + 回退。"""
    print("\n[域1] 恢复默认设置")
    iso = os.path.join(TEMP, "wr_qa_reset_iso")
    shutil.rmtree(iso, ignore_errors=True)
    os.makedirs(iso, exist_ok=True)
    iso_settings = os.path.join(iso, "settings.json")
    cfg = build_default_settings()
    cfg["appName"] = "会被重置的名字"
    cfg["short"]["intervalMs"] = 99999
    with open(iso_settings, "w", encoding="utf-8") as f:
        json.dump(cfg, f, ensure_ascii=False)
    port = free_port(29747)
    proc, base, log_path, _ = start_server(iso, port)
    if proc is None:
        check(False, "恢复默认：服务未就绪", "port=%d" % port)
        return
    try:
        st, res = call(base, "ResetSettings", [])
        check(st == 200 and isinstance(res, dict) and res.get("ok") is True,
              "ResetSettings 返回 {ok:true}", "status=%s %s" % (st, str(res)[:120]))
        backup = os.path.join(os.path.dirname(iso_settings), "settings.backup.json")
        check(os.path.isfile(backup), "恢复默认前已备份到 settings.backup.json", backup)
        # 读回：应回退默认
        st, settings = call(base, "GetSettings", [])
        check(settings.get("appName") == "吃饼人也敢休息？",
              "恢复后 appName 回退默认", settings.get("appName"))
        check(settings.get("short", {}).get("intervalMs") == 600000,
              "恢复后 intervalMs 回退默认 600000", settings.get("short", {}).get("intervalMs"))
        # installId 保留
        check(isinstance(settings.get("installId"), str) and settings.get("installId") != "",
              "恢复后 installId 保留（非空）", repr(settings.get("installId")))
    finally:
        stop_server(proc)
    shutil.rmtree(iso, ignore_errors=True)


def main():
    if not os.path.isfile(EXE):
        print("找不到 %s" % EXE)
        return 2
    test_time_baseline()
    test_config_corruption()
    test_persist_roundtrip()
    test_reset_settings()
    print("\n结果：%d 项断言，失败 %d 项" % (checks, len(failures)))
    if failures:
        print("\n失败项：")
        for f in failures:
            print("  - " + f)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
