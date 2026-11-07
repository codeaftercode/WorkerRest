#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""配置矩阵探针：把每个有多种取值的配置项都真存一遍，再看读回来是什么。

分两段：
  A. 归一化矩阵（一个服务，反复 SaveSettings → GetSettings）
     覆盖 short/long 的 mode 与 slots、sound.mode/volume、supervisor.selection、
     reminderMessages.order、takeout.pauseAction/leadSec/slots、window、appPause、
     dndPause/fullscreenPause、sites 过滤，以及“类型错误的值不得让整份配置坏掉”。
  B. 启动态矩阵（每个变体单起一个服务）
     nextAt/nextSource/nextType 与 lines 的类型序列 —— 这些只在启动时算一次。

判据分两类，写清楚免得把“设计如此”误判成 bug：
  · 归一化组（normalizeForSave 覆盖的键）：非法值必须被折回合法集合；
  · 透传组（theme / appearance / shortcuts 等前端负责的键）：只要求“存得住、读得回、
    不让服务崩”，具体取值由前端控件约束，服务端不做二次裁决。

用法：python scripts/probe/probe_config_matrix.py
退出码 0 = 全部通过。
"""

import copy
import json
import os
import re
import secrets
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
EXE = os.path.join(ROOT, "build", "bin", "rest_server.exe")
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
    print(("  ok   " if ok else "  FAIL ") + label + (("  | " + str(detail)) if detail else ""))
    if not ok:
        failures.append(label + (("  | " + str(detail)) if detail else ""))
    return ok


def note(label, detail=""):
    print("  ·    " + label + (("  | " + str(detail)) if detail else ""))


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


def _http_once(method, url, body=None, headers=None, timeout=15):
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

def http(method, url, body=None, headers=None, timeout=15):
    """带重试的 HTTP。

    Windows 在突发并发（比如 20 路同时 SaveSettings）下偶发 WSAEADDRINUSE(10048) /
    WSAENOBUFS(10055)，以及 Wails 反代自连失败的 upstream connect failed —— 都是套接字
    资源抖动、请求还没进方法体，退避重试安全。不重试的话，探针会把环境噪声报成
    产品缺陷。
    """
    last = (0, "")
    for attempt in range(6):
        st, raw = _http_once(method, url, body=body, headers=headers, timeout=timeout)
        if st != 0 and "upstream connect failed" not in str(raw):
            return st, raw
        last = (st, raw)
        if not any(k in str(raw) for k in ("10048", "10055", "timed out", "upstream connect failed")):
            return last
        time.sleep(0.08 * (attempt + 1))
    return last


def call(base, name, args, window=""):
    payload = {
        "object": 0, "method": 0,
        "args": {"call-id": secrets.token_hex(16), "methodName": BINDING_PREFIX + name, "args": args},
    }
    headers = {"Content-Type": "application/json", "x-wails-client-id": secrets.token_hex(16)}
    if window:
        headers["x-wails-window-name"] = window
    status, raw = http("POST", base + "/wails/runtime", json.dumps(payload).encode("utf-8"), headers)
    try:
        return status, json.loads(raw)
    except Exception:
        return status, raw


def base_settings():
    return {
        "appName": "矩阵探针",
        "short": {"enabled": True, "mode": "countdown", "intervalMs": 600000,
                  "durationMs": 20000, "notifyBeforeMs": 10000, "postponeMs": 120000, "slots": []},
        "long": {"enabled": False, "mode": "countdown", "intervalMs": 3600000,
                 "durationMs": 300000, "notifyBeforeMs": 10000, "postponeMs": 300000, "slots": []},
        "customBreaks": [],
        "longEveryNShort": 2,
        "idlePauseMin": 5,
        "dndPause": True,
        "fullscreenPause": True,
        "appPause": {"enabled": False, "seeded": True, "items": []},
        "theme": "light",
        "appearance": {"color": "#4f7cff", "transparency": 0.05, "blur": True,
                       "fullscreen": False, "alwaysOnTop": True, "widthPct": 95,
                       "heightPct": 95, "hideOnShare": True},
        "displayIds": [],
        "window": {"width": 874, "height": 857, "rememberSize": False,
                   "lastWidth": 0, "lastHeight": 0},
        "sound": {"enabled": False, "volume": 0.6, "sound": "short_start.wav", "custom": "",
                  "mode": "single", "singleId": "", "music": []},
        "shortcuts": {"postpone": "Ctrl+Alt+P", "endEarly": "Ctrl+Alt+X",
                      "pause": "Ctrl+Alt+S", "nextBreak": "Ctrl+Alt+B"},
        "reminderMessages": {"short": {"order": "random", "fixedId": "", "items": []},
                             "long": {"order": "random", "fixedId": "", "items": []},
                             "takeout": {"order": "random", "fixedId": "", "items": []}},
        "supervisor": {"enabled": False, "gaze": False, "selection": "random",
                       "fixedRole": "", "order": []},
        "takeout": {"enabled": False, "leadSec": 10, "postponeMin": 2,
                    "pauseAction": "postpone", "slots": []},
        "autostart": False,
        "startMinimized": False,
        "checkUpdateOnStart": False,
        "sites": ["https://www.goodgoodlive.com/worker-rest/version.json"],
    }


def start_server(iso_dir, port, cfg):
    os.makedirs(iso_dir, exist_ok=True)
    iso_settings = os.path.join(iso_dir, "settings.json")
    log_path = os.path.join(iso_dir, "server.log")
    with open(iso_settings, "w", encoding="utf-8") as f:
        json.dump(cfg, f, ensure_ascii=False, indent=2)
    env = dict(os.environ)
    env["WAILS_SERVER_PORT"] = str(port)
    env["WAILS_SERVER_HOST"] = "127.0.0.1"
    env["WR_SETTINGS_PATH"] = iso_settings
    env["WR_LOG_LEVEL"] = "debug"
    env.pop("ELECTRON_RUN_AS_NODE", None)
    logf = open(log_path, "w", encoding="utf-8")
    proc = subprocess.Popen([EXE], cwd=ROOT, env=env, stdout=logf, stderr=subprocess.STDOUT)
    base = "http://127.0.0.1:%d" % port
    for _ in range(60):
        if proc.poll() is not None:
            break
        st, _ = http("GET", base + "/", timeout=2)
        if st == 200:
            return proc, base, log_path, iso_settings
        time.sleep(0.5)
    print("--- 服务日志（未就绪） ---")
    try:
        print(open(log_path, encoding="utf-8", errors="replace").read()[-1500:])
    except Exception:
        pass
    return None, base, log_path, iso_settings


def stop_server(proc):
    if proc and proc.poll() is None:
        proc.terminate()
        try:
            proc.wait(timeout=5)
        except Exception:
            proc.kill()


def at(cfg, *path):
    cur = cfg
    for k in path:
        if not isinstance(cur, dict):
            return None
        cur = cur.get(k)
    return cur


def save_get(base, patch):
    """存一次补丁，返回读回的整份配置。"""
    call(base, "SaveSettings", [patch])
    st, cfg = call(base, "GetSettings", [])
    return cfg if isinstance(cfg, dict) else {}


# ---------------------------------------------------------------- A. 归一化矩阵
def phase_a():
    print("\n[A] 归一化矩阵：非法值必须折回合法集合")
    iso = tempfile.mkdtemp(prefix="wr_matrix_a_")
    port = free_port(29881)
    cfg0 = base_settings()
    proc, base, log_path, iso_settings = start_server(iso, port, cfg0)
    try:
        if not check(proc is not None, "A 段服务就绪", "port=%d" % port):
            return

        # ---- 休息模式 ----
        cfg = save_get(base, {"short": {"mode": "bogus-mode"}})
        v = at(cfg, "short", "mode")
        check(v == "countdown", "short.mode=非法值 → 折回 countdown", v)

        cfg = save_get(base, {"short": {"mode": "fixed",
                                        "slots": [{"id": "s1", "hour": 9, "minute": 30,
                                                   "repeat": "daily", "days": [], "enabled": True}]}})
        v = at(cfg, "short", "mode")
        n = len(at(cfg, "short", "slots") or [])
        check(v == "fixed" and n == 1, "short.mode=fixed + 一个时刻 → 保留", "%s / %d 个时刻" % (v, n))

        cfg = save_get(base, {"short": {"mode": "countdown"}})

        # ---- 时长 / 间隔为 0：归一化不管这两个键（只管 mode/slots），
        #      所以判据不是“被钳制”，而是“运行时不失控”—— 用 3 秒内的计划日志条数当尺子。
        pos0 = os.path.getsize(log_path)
        save_get(base, {"short": {"durationMs": 0, "intervalMs": 0}})
        time.sleep(3)
        with open(log_path, encoding="utf-8", errors="replace") as f:
            f.seek(pos0)
            burst = f.read()
        plan_lines = burst.count("[plan]")
        check(plan_lines < 40, "short.intervalMs=0 时三秒内计划重排不失控（<40 条日志）", plan_lines)
        check("panic" not in burst.lower(), "极端间隔下无 panic",
              burst[-160:] if "panic" in burst.lower() else "")
        st2, s0 = call(base, "GetState", [])
        check(isinstance(s0, dict), "极端间隔下 GetState 仍可读", type(s0).__name__)
        note("durationMs=0/intervalMs=0 的服务端处理", "不钳制，靠计划层与调度器自身兜底")
        save_get(base, {"short": {"durationMs": 20000, "intervalMs": 600000}})

        cfg = save_get(base, {"short": {"durationMs": "abc"}})
        raw_d = at(cfg, "short", "durationMs")
        st2, s1 = call(base, "GetState", [])
        check(isinstance(s1, dict), "durationMs=字符串时服务仍可读状态（原始值 %r 未污染运行路径）" % raw_d,
              type(s1).__name__)
        save_get(base, {"short": {"durationMs": 20000}})

        # ---- 时刻表越界 ----
        cfg = save_get(base, {"short": {"mode": "fixed", "slots": [
            {"id": "bad1", "hour": 99, "minute": -5, "repeat": "daily", "days": [], "enabled": True},
            {"id": "bad2", "hour": -1, "minute": 61, "repeat": "weekly", "days": [], "enabled": True},
        ]}})
        slots = at(cfg, "short", "slots") or []
        ok = all(0 <= s.get("hour", -1) <= 23 and 0 <= s.get("minute", -1) <= 59 for s in slots)
        check(ok, "时刻表 hour/minute 越界 → clamp 到 0-23 / 0-59",
              [(s.get("hour"), s.get("minute")) for s in slots])
        cfg = save_get(base, {"short": {"mode": "countdown", "slots": []}})

        # ---- 提示音：mode 的合法集合是 single / random / sequence（music 是列表字段而非 mode）----
        cfg = save_get(base, {"sound": {"mode": "bogus"}})
        m = at(cfg, "sound", "mode")
        check(m == "single", "sound.mode=非法值 → 折回 single", m)
        for good in ("random", "sequence"):
            cfg = save_get(base, {"sound": {"mode": good}})
            check(at(cfg, "sound", "mode") == good, "sound.mode=%s 保留" % good, at(cfg, "sound", "mode"))
        cfg = save_get(base, {"sound": {"mode": "single"}})

        # 音量越界：raw 不钳制，判据是“真正播的时候用的是不是合法音量”—— 从播放日志里取。
        def played_volume(level):
            save_get(base, {"sound": {"enabled": True, "volume": level, "mode": "single"}})
            mark = os.path.getsize(log_path)
            call(base, "StartBreak", ["short"])
            time.sleep(0.8)
            call(base, "Action", ["break", "endBreak"], window="break")
            time.sleep(0.3)
            with open(log_path, encoding="utf-8", errors="replace") as f:
                f.seek(mark)
                text = f.read()
            mt = re.search(r"volume=([0-9.]+)", text)
            return float(mt.group(1)) if mt else None, text

        got_v, text_v = played_volume(2)
        if got_v is None:
            note("音量越界：日志里没有 volume= 字样（无头下可能不落这条）", text_v[-120:].replace("\n", " "))
        else:
            check(0 <= got_v <= 1, "sound.volume=2 时实际播放音量被收进 0..1", got_v)
        got_v2, _ = played_volume(-1)
        if got_v2 is not None:
            check(0 <= got_v2 <= 1, "sound.volume=-1 时实际播放音量被收进 0..1", got_v2)
        save_get(base, {"sound": {"enabled": False, "volume": 0.6}})

        # ---- 督休专员选择 ----
        for bad in ("bogus", "", "   "):
            cfg = save_get(base, {"supervisor": {"selection": bad}})
            v = at(cfg, "supervisor", "selection")
            check(v in ("random", "fixed", "sequence"), "supervisor.selection=%r → 折回合法值" % bad, v)
        for good in ("fixed", "sequence", "random"):
            cfg = save_get(base, {"supervisor": {"selection": good}})
            check(at(cfg, "supervisor", "selection") == good,
                  "supervisor.selection=%s 保留" % good, at(cfg, "supervisor", "selection"))

        # ---- 提醒内容顺序 ----
        for bad in ("bogus", "", "FIXED"):
            cfg = save_get(base, {"reminderMessages": {"short": {"order": bad}}})
            v = at(cfg, "reminderMessages", "short", "order")
            check(v in ("fixed", "random", "sequence"), "reminderMessages.short.order=%r → 折回合法值" % bad, v)
        cfg = save_get(base, {"reminderMessages": {"short": {"order": "fixed", "fixedId": "根本不存在"}}})
        v = at(cfg, "reminderMessages", "short", "order")
        fid = at(cfg, "reminderMessages", "short", "fixedId")
        check(v in ("fixed", "random", "sequence"),
              "order=fixed 但 fixedId 指向不存在的条目 → 不致崩、仍是合法 order", "%s / %r" % (v, fid))
        cfg = save_get(base, {"reminderMessages": {"short": {"order": "random", "fixedId": ""}}})

        # ---- 外卖 ----
        cfg = save_get(base, {"takeout": {"pauseAction": "bogus"}})
        v = at(cfg, "takeout", "pauseAction")
        check(v == "postpone", "takeout.pauseAction=非法值 → 折回 postpone（默认）", v)
        for good in ("remind", "postpone", "skip"):
            cfg = save_get(base, {"takeout": {"pauseAction": good}})
            check(at(cfg, "takeout", "pauseAction") == good,
                  "takeout.pauseAction=%s 保留" % good, at(cfg, "takeout", "pauseAction"))
        cfg = save_get(base, {"takeout": {"leadSec": -10, "postponeMin": -3}})
        lead = at(cfg, "takeout", "leadSec")
        post = at(cfg, "takeout", "postponeMin")
        check(isinstance(lead, (int, float)) and lead >= 0, "takeout.leadSec=-10 → clamp ≥0", lead)
        check(isinstance(post, (int, float)) and post >= 0, "takeout.postponeMin=-3 → clamp ≥0", post)
        cfg = save_get(base, {"takeout": {"pauseAction": "postpone", "leadSec": 10, "postponeMin": 2}})

        # ---- 开关类：类型错误的原始值不要求被改写（读取侧用 Bool 带默认），
        #      判据是“不污染运行路径”：保存后服务照样能起状态、值仍是可解释的。
        cfg = save_get(base, {"dndPause": 0,
                              "fullscreenPause": "false",
                              "appPause": {"enabled": "true"}, "window": {"rememberSize": "yes"}})
        note("类型错误的开关值（服务端保留原值，读取侧取默认）",
             "dndPause=%r fullscreenPause=%r appPause.enabled=%r rememberSize=%r" % (
                 at(cfg, "dndPause"), at(cfg, "fullscreenPause"),
                 at(cfg, "appPause", "enabled"), at(cfg, "window", "rememberSize")))
        check(at(cfg, "appPause", "enabled") is False,
              "appPause.enabled='true'（字符串）被折成 false，而不是当成真值", at(cfg, "appPause", "enabled"))
        check(at(cfg, "window", "rememberSize") is True,
              "window.rememberSize='yes' 被归成 true（非空字符串按“开”处理）",
              at(cfg, "window", "rememberSize"))
        st3, s3 = call(base, "GetState", [])
        check(isinstance(s3, dict), "类型错误的开关不影响服务状态读取", type(s3).__name__)
        save_get(base, {"dndPause": True, "fullscreenPause": True,
                        "appPause": {"enabled": False}, "window": {"rememberSize": False}})

        # ---- window 尺寸 ----
        cfg = save_get(base, {"window": {"width": 0, "height": -20}})
        w = at(cfg, "window", "width")
        h = at(cfg, "window", "height")
        check(isinstance(w, (int, float)) and w > 0, "window.width=0 → 折回正数", w)
        check(isinstance(h, (int, float)) and h > 0, "window.height=-20 → 折回正数", h)

        # ---- Number 键的宽高百分比（透传组：只记录不裁决）----
        cfg = save_get(base, {"appearance": {"widthPct": 150, "heightPct": 10}})
        note("appearance.widthPct=150 的服务端行为（前端滑块限定 50-100，服务端不二次裁决）",
             "读回 %r / %r" % (at(cfg, "appearance", "widthPct"), at(cfg, "appearance", "heightPct")))
        cfg = save_get(base, {"appearance": {"widthPct": 95, "heightPct": 95}})

        # ---- sites：raw 原样存（前端写入的是候选列表），但“用的时候”必须只剩
        #      https / 回环 —— 判据取自 CheckUpdate 的耗时与日志，而不是读回的 raw。
        save_get(base, {"sites": ["http://evil.example.com/v.json", "ftp://x/y.json"]})
        mark = os.path.getsize(log_path)
        t0 = time.time()
        ck = call(base, "CheckUpdate", [])[1]
        dt = time.time() - t0
        time.sleep(0.3)
        with open(log_path, encoding="utf-8", errors="replace") as f:
            f.seek(mark)
            text = f.read()
        check(dt < 10, "只剩非法源时 CheckUpdate 立刻结束（不会真去连 evil.example.com）",
              "%.1fs" % dt)
        check(isinstance(ck, dict), "CheckUpdate 对全非法源返回对象而非抛错", str(ck)[:100])
        check("evil.example.com" not in text,
              "日志里没有对内网/明文源的请求痕迹", text[-160:].replace("\n", " ") if "evil" in text else "")
        save_get(base, {"sites": ["https://www.goodgoodlive.com/worker-rest/version.json"]})

        # ---- 未知键 / 顶层类型 ----
        cfg = save_get(base, {"完全未知的键": {"a": 1}})
        check(isinstance(cfg, dict) and "appName" in cfg, "写入未知键不会让整份配置坏掉",
              len(cfg.keys()) if isinstance(cfg, dict) else type(cfg).__name__)
        # ---- 入参类型：不是对象时，Wails 参数绑定层就该拦住（方法体根本不该跑）----
        st, res = call(base, "SaveSettings", ["不是对象"])
        text = res if isinstance(res, str) else json.dumps(res, ensure_ascii=False)
        check("could not parse argument" in text or "TypeError" in text,
              "SaveSettings(字符串入参) 被参数绑定层拒绝（方法体不执行）", text[:100])
        before_null = at(call(base, "GetSettings", [])[1] or {}, "appName")
        st, res = call(base, "SaveSettings", [None])
        after_null = at(call(base, "GetSettings", [])[1] or {}, "appName")
        check(res is True and before_null == after_null,
              "SaveSettings(null) 是 no-op：返回 true 但配置一点没变", "%r → %r" % (before_null, after_null))

        # ---- appearance 的实际生效面（会话里下发）----
        # 先确保没有残留会话：BreakCurrent 拿的是“当前会话”，若上一次休息没真正结束，
        # 这里读到的就是旧会话的外观（默认值），会误判成“配置没生效”。
        call(base, "Action", ["break", "endBreak"], window="break")
        time.sleep(0.4)
        st_idle, st0 = call(base, "GetState", [])
        phase_now = st0.get("phase") if isinstance(st0, dict) else None
        check(isinstance(st0, dict) and phase_now != "break",
              "外观测试前已无进行中的休息会话", phase_now if phase_now else str(st0)[:80])
        cfg = save_get(base, {"appearance": {"color": "#123456", "transparency": 0.42,
                                             "blur": False, "alwaysOnTop": False}})
        check(at(cfg, "appearance", "color") == "#123456", "appearance.color 已保存",
              at(cfg, "appearance", "color"))
        sid2 = call(base, "StartBreak", ["short"])[1]
        check(isinstance(sid2, str) and sid2 != "", "外观测试的休息会话已开起来", str(sid2)[:40])
        st, sess = call(base, "BreakCurrent", ["break"])
        ap = (sess or {}).get("appearance") if isinstance(sess, dict) else None
        if isinstance(ap, dict):
            check(ap.get("color") == "#123456", "休息会话下发的 appearance.color 取自配置", ap.get("color"))
            check(ap.get("transparency") == 0.42, "休息会话下发的 transparency 取自配置",
                  ap.get("transparency"))
            check(ap.get("blur") is False, "休息会话下发的 blur 取自配置", ap.get("blur"))
        else:
            check(False, "休息会话带 appearance 对象", str(sess)[:120])
        call(base, "Action", ["break", "endBreak"], window="break")

        # ---- 透传组：theme / shortcuts 存得住 ----
        cfg = save_get(base, {"theme": "dark",
                              "shortcuts": {"postpone": "Ctrl+Alt+1", "endEarly": "",
                                            "pause": "乱码", "nextBreak": "Ctrl+Alt+B"}})
        check(at(cfg, "theme") == "dark", "theme=dark 原样保留（前端负责渲染）", at(cfg, "theme"))
        note("乱码 / 空快捷键写入后的读回值（服务端不裁决）",
             at(cfg, "shortcuts"))
    finally:
        stop_server(proc)
        shutil.rmtree(iso, ignore_errors=True)


# ---------------------------------------------------------------- B. 启动态矩阵
def boot_state(cfg, port, prefix):
    """起一个服务并取一次 GetState，统一返回 (state, iso_dir, handle)。

    handle = (proc, base, log_path)；服务起不来时 state 与 handle 都是 None
    （start_server 已经把日志尾巴打出来了）。
    """
    iso = tempfile.mkdtemp(prefix=prefix)
    proc, base, log_path, _ = start_server(iso, port, cfg)
    if proc is None:
        return None, iso, None
    # 服务起来了不等于反代已经能转发：连跑多个变体时偶发 502 / upstream connect failed，
    # 那一下 GetState 会拿到空。这里重试几次再判“起不来”—— 否则会把套接字抖动报成
    # “某个配置组合起不来”，是最容易误导人的那种假红。
    state = None
    for i in range(8):
        st, got = call(base, "GetState", [])
        if isinstance(got, dict):
            state = got
            break
        if proc.poll() is not None:
            break
        time.sleep(0.25 * (i + 1))
    return state, iso, (proc, base, log_path)


def phase_b():
    print("\n[B] 启动态矩阵：nextAt / nextSource / nextType / lines 类型序列")

    # B1 countdown
    cfg = base_settings()
    cfg["short"] = {"enabled": True, "mode": "countdown", "intervalMs": 60000,
                    "durationMs": 20000, "notifyBeforeMs": 10000, "postponeMs": 120000, "slots": []}
    cfg["long"]["enabled"] = False
    state, iso, h = boot_state(cfg, free_port(29891), "wr_matrix_b1_")
    try:
        if check(state is not None, "B1 countdown 服务就绪", ""):
            now = int(time.time() * 1000)
            delta = (state.get("nextAt") or 0) - now
            check(state.get("nextSource") == "countdown", "B1 nextSource=countdown", state.get("nextSource"))
            check(state.get("nextType") == "short", "B1 nextType=short", state.get("nextType"))
            check(55000 <= delta <= 68000, "B1 nextAt ≈ now+60s", "%d ms" % delta)
    finally:
        if h:
            stop_server(h[0])
        shutil.rmtree(iso, ignore_errors=True)

    # B2 fixed slots
    cfg = base_settings()
    cfg["short"] = {"enabled": True, "mode": "fixed", "intervalMs": 600000,
                    "durationMs": 20000, "notifyBeforeMs": 10000, "postponeMs": 120000,
                    "slots": [{"id": "s1", "hour": 21, "minute": 50, "repeat": "daily",
                               "days": [], "enabled": True},
                              {"id": "s2", "hour": 3, "minute": 15, "repeat": "daily",
                               "days": [], "enabled": True}]}
    cfg["long"]["enabled"] = False
    state, iso, h = boot_state(cfg, free_port(29892), "wr_matrix_b2_")
    try:
        if check(state is not None, "B2 fixed 服务就绪", ""):
            check(state.get("nextSource") == "fixed", "B2 nextSource=fixed", state.get("nextSource"))
            lines = state.get("lines") or []
            check(len(lines) > 0, "B2 lines 非空（固定时刻会展开成多条）", len(lines))
            check(all(l.get("source") == "fixed" for l in lines),
                  "B2 lines 全部 source=fixed", set(l.get("source") for l in lines))
    finally:
        if h:
            stop_server(h[0])
        shutil.rmtree(iso, ignore_errors=True)

    # B3 全关
    cfg = base_settings()
    cfg["short"]["enabled"] = False
    cfg["long"]["enabled"] = False
    cfg["takeout"]["enabled"] = False
    cfg["customBreaks"] = []
    state, iso, h = boot_state(cfg, free_port(29893), "wr_matrix_b3_")
    try:
        if check(state is not None, "B3 全关服务就绪", ""):
            check(state.get("nextAt") in (0, None), "B3 全部关闭 → nextAt 为 0/null", state.get("nextAt"))
            check(state.get("nextSource") in (None, "", "none"),
                  "B3 全部关闭 → nextSource 为空", repr(state.get("nextSource")))
            check((state.get("lines") or []) == [], "B3 全部关闭 → lines 为空",
                  len(state.get("lines") or []))
    finally:
        if h:
            stop_server(h[0])
        shutil.rmtree(iso, ignore_errors=True)

    # B4 只开长休息
    cfg = base_settings()
    cfg["short"]["enabled"] = False
    cfg["long"] = {"enabled": True, "mode": "countdown", "intervalMs": 120000,
                   "durationMs": 300000, "notifyBeforeMs": 10000, "postponeMs": 300000, "slots": []}
    state, iso, h = boot_state(cfg, free_port(29894), "wr_matrix_b4_")
    try:
        if check(state is not None, "B4 只开长休息服务就绪", ""):
            check(state.get("nextType") == "long", "B4 nextType=long", state.get("nextType"))
            check(state.get("nextSource") == "countdown", "B4 nextSource=countdown", state.get("nextSource"))
    finally:
        if h:
            stop_server(h[0])
        shutil.rmtree(iso, ignore_errors=True)

    # B5 longEveryNShort：1 比 3 的长休息更密
    def long_count(n):
        cfg = base_settings()
        cfg["short"]["intervalMs"] = 60000
        cfg["long"] = {"enabled": True, "mode": "countdown", "intervalMs": 600000,
                       "durationMs": 300000, "notifyBeforeMs": 10000, "postponeMs": 300000, "slots": []}
        cfg["longEveryNShort"] = n
        st, iso, h = boot_state(cfg, free_port(29895 + n), "wr_matrix_b5_%d_" % n)
        try:
            if not st:
                return None, []
            # lines 只放“最近几跳”，跨一天的分布要看 timeline（今日时间线）。
            items = st.get("timeline") or []
            return sum(1 for l in items if l.get("type") == "long"), items
        finally:
            if h:
                stop_server(h[0])
            shutil.rmtree(iso, ignore_errors=True)

    c1, lines1 = long_count(1)
    c3, lines3 = long_count(3)
    if check(c1 is not None and c3 is not None, "B5 longEveryNShort 两个变体都起得来", "%s / %s" % (c1, c3)):
        check(c1 > c3, "longEveryNShort=1 的长休息条数多于 =3（每 N 次短休息插一次长）",
              "N=1 → %d 条，N=3 → %d 条" % (c1, c3))

    # B6 自定义提醒
    cfg = base_settings()
    cfg["customBreaks"] = [{"id": "c1", "name": "喝水", "enabled": True, "mode": "countdown",
                            "intervalMs": 90000, "durationMs": 15000, "notifyBeforeMs": 5000,
                            "postponeMs": 60000, "pauseAction": "postpone", "slots": []}]
    state, iso, h = boot_state(cfg, free_port(29899), "wr_matrix_b6_")
    try:
        if check(state is not None, "B6 自定义提醒服务就绪", ""):
            lines = state.get("lines") or []
            names = [l.get("name") for l in lines]
            check(any("喝水" in str(n) for n in names), "B6 自定义提醒进入 lines", names[:5])
    finally:
        if h:
            stop_server(h[0])
        shutil.rmtree(iso, ignore_errors=True)

    # B7 rememberSize + lastWidth/lastHeight 保留
    cfg = base_settings()
    cfg["window"] = {"width": 874, "height": 857, "rememberSize": True,
                     "lastWidth": 1010, "lastHeight": 900}
    state, iso, h = boot_state(cfg, free_port(29901), "wr_matrix_b7_")
    try:
        if check(state is not None, "B7 rememberSize 变体服务就绪", ""):
            st, got = call(h[1], "GetSettings", [])
            win = at(got, "window") or {}
            check(win.get("lastWidth") == 1010 and win.get("lastHeight") == 900,
                  "B7 rememberSize=true 时 lastWidth/lastHeight 原样保留",
                  (win.get("lastWidth"), win.get("lastHeight")))
    finally:
        if h:
            stop_server(h[0])
        shutil.rmtree(iso, ignore_errors=True)


def main():
    if not os.path.exists(EXE):
        print("找不到 %s" % EXE)
        return 2
    real_cfg = os.path.join(ROOT, "config", "settings.json")
    before = os.path.getmtime(real_cfg) if os.path.exists(real_cfg) else 0

    phase_a()
    phase_b()

    after = os.path.getmtime(real_cfg) if os.path.exists(real_cfg) else 0
    print("\n[收尾]")
    check(before == after, "用户真实 settings.json 未被改动", real_cfg)

    print("\n结果：%d 项断言，失败 %d 项" % (checks, len(failures)))
    if failures:
        for f in failures:
            print("  - " + f)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
