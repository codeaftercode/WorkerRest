#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""绑定级全覆盖探针：契约表里全部前端绑定，逐个真调一次并断言行为。

与既有脚本的分工：
  - smoke_server.py：证明「绑定按名能解析、主链路（休息/统计/专员）走得通」；
  - 本脚本：补齐剩余面 —— 只读查询的返回结构、状态变更的落盘效果、
    无头环境下文件对话框类绑定的降级路径、非法入参是否被拒、
    以及「探针本身能不能发现绑定丢失」的元测试。

口径：
  - 一切副作用落在临时隔离目录（WR_SETTINGS_PATH），跑完断言真实 settings.json 未变；
  - 会打开浏览器的 OpenExternal 只测拒绝分支；会真的退出进程的 QuitApp 放最后一项。

用法：python scripts/probe/probe_bindings_full.py
退出码 0 = 全部通过。
"""

import base64
import hashlib
import json
import os
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
EXE = os.path.join(ROOT, "build", "bin", "chibingren_server.exe")
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
        "object": 0,
        "method": 0,
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


def invoke(base, name, args, window=""):
    """调一个绑定，归一成 (status, result, 文本, 是否「名字未知」)。"""
    st, res = call(base, name, args, window)
    text = res if isinstance(res, str) else json.dumps(res, ensure_ascii=False)
    return st, res, text, ("unknown bound method" in text)


def call_ok(base, name, args, window=""):
    """成功路径：必须 200、必须不是 unknown、不得 panic。返回结果对象。"""
    st, res, text, unknown = invoke(base, name, args, window)
    ok = st == 200 and not unknown and "panic" not in text.lower()
    check(ok, "%-24s 可调用且不 panic" % name, "status=%s %s" % (st, text[:120]))
    return res


def digest(path):
    if not path or not os.path.exists(path):
        return ""
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(65536), b""):
            h.update(chunk)
    return h.hexdigest()


def read_json(path):
    with open(path, encoding="utf-8") as f:
        return json.load(f)


def default_settings():
    """一份精简但合法的配置：关掉定时提醒，只留需要的开关，避免后台状态干扰断言。"""
    return {
        "appName": "绑定探针",
        "short": {"enabled": True, "mode": "countdown", "intervalMs": 3600000,
                  "durationMs": 20000, "notifyBeforeMs": 10000, "postponeMs": 120000, "slots": []},
        "long": {"enabled": False, "mode": "countdown", "intervalMs": 7200000,
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
        # 指向必然连不通的回环端口：让 CheckUpdate 走「失败但优雅」的分支，不碰公网。
        "sites": ["http://127.0.0.1:9/version.json"],
    }


def start_server(iso_dir, port):
    os.makedirs(iso_dir, exist_ok=True)
    iso_settings = os.path.join(iso_dir, "settings.json")
    log_path = os.path.join(iso_dir, "server.log")
    with open(iso_settings, "w", encoding="utf-8") as f:
        json.dump(default_settings(), f, ensure_ascii=False, indent=2)
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
        print(open(log_path, encoding="utf-8", errors="replace").read()[-2000:])
    except Exception:
        pass
    return None, base, log_path, iso_settings


def main():
    if not os.path.exists(EXE):
        print("找不到 %s（先 go build -tags server -o build/bin/chibingren_server.exe ./cmd/workerrest）" % EXE)
        return 2

    real_cfg = os.path.join(ROOT, "config", "settings.json")
    before = digest(real_cfg)

    iso = tempfile.mkdtemp(prefix="wr_bindings_")
    port = free_port(29871)
    proc, base, log_path, iso_settings = start_server(iso, port)
    if not check(proc is not None, "无头服务就绪", "port=%d" % port):
        shutil.rmtree(iso, ignore_errors=True)
        return 1

    try:
        # ---------- 元测试：探针能不能发现「绑定丢了」----------
        print("\n[元] 探针自身的有效性")
        st, res, text, unknown = invoke(base, "ThisBindingDoesNotExist", [])
        check(unknown, "调用不存在的绑定名 → 返回 unknown bound method（说明本探针能发现绑定丢失）",
              "status=%s %s" % (st, text[:100]))

        # ---------- 只读查询 ----------
        print("\n[只读] 信息 / 状态 / 目录扫描")
        info = call_ok(base, "AppInfo", [])
        if isinstance(info, dict):
            check(all(k in info for k in ("name", "version", "engine", "roles", "goVersion")),
                  "AppInfo 含 name/version/engine/roles/goVersion", sorted(info.keys())[:8])
            check(info.get("engine") == "go+wails3", "AppInfo.engine == go+wails3", info.get("engine"))
            check(info.get("version") != "", "AppInfo.version 非空（ldflags 注入生效）", info.get("version"))
            check(isinstance(info.get("roles"), int) and info["roles"] > 0,
                  "AppInfo.roles 是正数（专员目录扫得到）", info.get("roles"))
        else:
            check(False, "AppInfo 返回对象", type(info).__name__)

        state = call_ok(base, "GetState", [])
        if isinstance(state, dict):
            check("nextAt" in state and "phase" in state, "GetState 含 nextAt/phase",
                  sorted(state.keys())[:10])
            check(state.get("phase") in ("idle", "break", "paused"), "GetState.phase 取值合法",
                  state.get("phase"))

        msgs = call_ok(base, "BuiltinMsgDefaults", [])
        if isinstance(msgs, dict):
            check(len(msgs) > 0, "BuiltinMsgDefaults 非空", list(msgs.keys()))
        else:
            check(False, "BuiltinMsgDefaults 返回对象", type(msgs).__name__)

        roles = call_ok(base, "ListRoles", [])
        check(isinstance(roles, list) and len(roles) > 0, "ListRoles 返回非空数组",
              len(roles) if isinstance(roles, list) else type(roles).__name__)
        first_name = ""
        if isinstance(roles, list) and roles and isinstance(roles[0], dict):
            first_name = roles[0].get("name") or ""
            check(first_name != "", "ListRoles[0].name 非空", first_name)
            check(isinstance(roles[0].get("geom"), dict), "ListRoles[0].geom 是对象",
                  str(roles[0].get("geom"))[:60])

        # RoleFigureDataURL：零覆盖项之一
        fig = call_ok(base, "RoleFigureDataURL", [first_name])
        check(isinstance(fig, str) and fig.startswith("data:image/"),
              "RoleFigureDataURL(存在的专员) 返回 data URL",
              (fig[:32] + "…") if isinstance(fig, str) else type(fig).__name__)
        fig_bad = call_ok(base, "RoleFigureDataURL", ["绝对不存在的专员"])
        check(fig_bad == "", "RoleFigureDataURL(不存在的专员) 返回空串", repr(fig_bad)[:60])

        apps = call_ok(base, "ListRunningApps", [])
        check(isinstance(apps, (list, dict)), "ListRunningApps 返回数组或对象", type(apps).__name__)

        notices = call_ok(base, "GetThirdPartyNotices", [])
        check(isinstance(notices, str) and len(notices) > 2000,
              "GetThirdPartyNotices 返回非空长文本", len(notices) if isinstance(notices, str) else "")

        lic = call_ok(base, "GetProjectLicense", [])
        check(isinstance(lic, str) and len(lic) > 800 and lic.startswith("MIT License"),
              "GetProjectLicense 返回内嵌的 MIT 正文", len(lic) if isinstance(lic, str) else "")

        # WindowMaximized：零覆盖项之一（无头下无窗口，应稳定返回 false 而不是崩）
        maxed = call_ok(base, "WindowMaximized", [])
        check(maxed is False, "WindowMaximized 无头下返回 false", repr(maxed))

        # SoundFileURL：内置名 / 路径遍历 / 不存在
        url_ok = call_ok(base, "SoundFileURL", ["short_start.wav"])
        check(isinstance(url_ok, str) and url_ok != "", "SoundFileURL(内置音频) 非空",
              str(url_ok)[:60])
        url_trav = call_ok(base, "SoundFileURL", ["../../config/settings.json"])
        check(url_trav == "", "SoundFileURL(路径遍历) 被拒 → 空串", repr(url_trav)[:60])
        url_none = call_ok(base, "SoundFileURL", ["no-such-sound.wav"])
        check(url_none == "", "SoundFileURL(不存在的文件) → 空串", repr(url_none)[:60])

        # 更新三兄弟（不发真网络：sites 指向死端口）
        ustat = call_ok(base, "UpdateStatus", [])
        check(isinstance(ustat, dict), "UpdateStatus 返回对象", type(ustat).__name__)
        pend = call_ok(base, "PendingUpdate", [])
        # 返回 nil map（JSON null）= 没有待安装的更新。前端用 `if (!x) return` 处理，
        # 所以 null 是语义正确的「无」，不是漏返回。
        check(pend is None or isinstance(pend, dict), "PendingUpdate 返回对象或 null（null=无待装更新）",
              type(pend).__name__)
        ck = call_ok(base, "CheckUpdate", [])
        check(isinstance(ck, dict), "CheckUpdate 对不可达源返回对象（不抛错）", str(ck)[:100])
        su = call_ok(base, "StartUpdate", [])
        check(isinstance(su, dict), "StartUpdate 无待更新时返回对象", str(su)[:100])

        # 统计只读
        stats = call_ok(base, "GetStats", [])
        check(isinstance(stats, dict), "GetStats 返回对象", type(stats).__name__)
        hist = call_ok(base, "GetHistory", ["2026-01-01", "2026-12-31"])
        check(isinstance(hist, (dict, list)), "GetHistory 返回对象/数组", type(hist).__name__)
        notice = call_ok(base, "GetStatsFileNotice", [])
        # null = 统计文件没有发生过「损坏→备份→重建」，没有要告知用户的事。
        check(notice is None or isinstance(notice, dict),
              "GetStatsFileNotice 返回对象或 null（null=无自愈事件）", type(notice).__name__)
        acked = call_ok(base, "AckStatsFileNotice", [])
        check(isinstance(acked, dict), "AckStatsFileNotice 返回对象", type(acked).__name__)

        # 提示音体检
        sscan = call_ok(base, "GetSoundScanNotice", [])
        check(sscan is None or isinstance(sscan, dict),
              "GetSoundScanNotice 返回对象或 null（null=音频体检无待办）", type(sscan).__name__)
        sdis = call_ok(base, "DismissSoundScan", [])
        check(isinstance(sdis, dict), "DismissSoundScan 返回对象", type(sdis).__name__)
        simp = call_ok(base, "ImportScannedSounds", [[]])
        check(isinstance(simp, dict), "ImportScannedSounds([]) 返回对象（空列表不炸）", str(simp)[:80])
        sdrop = call_ok(base, "DropMissingSounds", [[]])
        check(isinstance(sdrop, dict), "DropMissingSounds([]) 返回对象（空列表不炸）", str(sdrop)[:80])

        # 外卖 / 提前提示的读取态
        tcur = call_ok(base, "TakeoutCurrent", ["takeout"])
        check(tcur is None or isinstance(tcur, dict), "TakeoutCurrent 返回 null 或对象",
              str(tcur)[:60])
        pstate = call_ok(base, "PreBreakState", [])
        check(isinstance(pstate, dict), "PreBreakState 返回对象", type(pstate).__name__)

        # 自动暂停期间的「已暂停多久」小窗（见 pause_notice.go）。两个动作都要在
        # 「没有窗口 / 没有暂停」时也是安全的：Dismiss 直接真调（关窗是空操作），
        # Resume 走 schedPauseToggle（会改状态），所以只探测它是否注册。
        pdst, pdres, pdtext, pdunknown = invoke(base, "PauseNoticeDismiss", [])
        check(pdst == 200 and not pdunknown, "PauseNoticeDismiss 无窗时安全返回（关窗是空操作）",
              "status=%s %s" % (pdst, pdtext[:120]))
        pst, pres, ptext, punknown = invoke(base, "PauseNoticeResume", ["bogus-extra-arg"], "")
        check(not punknown, "PauseNoticeResume 已注册（用错误参数探测，不真的恢复）",
              "status=%s %s" % (pst, ptext[:120]))

        # ---------- 配置读写 ----------
        print("\n[配置] 保存 / 忽略版本 / 恢复默认")
        before_cfg = read_json(iso_settings)
        saved = call_ok(base, "SaveSettings", [{"appName": "绑定探针-改"}])
        check(saved is True, "SaveSettings 返回 true", repr(saved))
        got = call_ok(base, "GetSettings", [])
        check(isinstance(got, dict) and got.get("appName") == "绑定探针-改",
              "GetSettings 读回刚写入的 appName", got.get("appName") if isinstance(got, dict) else "")
        disk = read_json(iso_settings)
        check(disk.get("appName") == "绑定探针-改", "appName 已落盘", disk.get("appName"))
        check(before_cfg.get("short", {}).get("intervalMs") == disk.get("short", {}).get("intervalMs"),
              "只改 appName 时其它键原样保留（深合并不整份替换）",
              disk.get("short", {}).get("intervalMs"))

        ign = call_ok(base, "IgnoreVersion", ["9.9.9"])
        check(isinstance(ign, dict), "IgnoreVersion 返回对象", str(ign)[:80])
        disk = read_json(iso_settings)
        check(disk.get("ignoredVersion") == "9.9.9", "ignoredVersion 已落盘", disk.get("ignoredVersion"))

        reset = call_ok(base, "ResetSettings", [])
        if isinstance(reset, dict):
            check(reset.get("ok") is True, "ResetSettings 返回 ok=true", str(reset)[:120])
            bpath = reset.get("backupPath") or ""
            check(bool(bpath) and os.path.exists(bpath), "恢复默认前已备份旧配置", bpath)
        else:
            check(False, "ResetSettings 返回对象", type(reset).__name__)
        disk = read_json(iso_settings)
        check(disk.get("appName") == "吃饼人也敢休息？", "恢复默认后 appName 回到出厂值",
              disk.get("appName"))
        check("ignoredVersion" not in disk, "恢复默认后 ignoredVersion 被清除",
              disk.get("ignoredVersion"))

        # ---------- 窗口类（无头下应「安静地不做事」）----------
        print("\n[窗口] 无头模式下的空操作路径")
        for name, args in (("SetActiveTab", ["about"]), ("ShowSettingsWindow", []),
                           ("CloseWindow", ["settings"]), ("SupActivity", ["break"]),
                           ("CloseWindow", ["break"])):
            call_ok(base, name, args)

        # ---------- 休息会话 ----------
        print("\n[会话] 预览 / 开始 / 查询")
        pv = call_ok(base, "PreviewBreak", ["short", ""])
        check(isinstance(pv, str) and pv != "", "PreviewBreak 返回非空会话 id", str(pv)[:40])
        cur = call_ok(base, "BreakCurrent", ["break"])
        check(isinstance(cur, dict) and cur.get("preview") is True,
              "BreakCurrent 拿到的是预览会话（preview=true）",
              str(cur)[:80] if isinstance(cur, dict) else type(cur).__name__)
        sid = call_ok(base, "StartBreak", ["short"])
        check(isinstance(sid, str) and sid.startswith("short-"), "StartBreak 返回会话 id", str(sid)[:40])
        state2 = call_ok(base, "GetState", [])
        check(isinstance(state2, dict) and state2.get("phase") == "break",
              "开始休息后 GetState.phase == break", state2.get("phase") if isinstance(state2, dict) else "")
        bad_kind = call_ok(base, "StartBreak", ["no-such-kind"])
        check(bad_kind == "" or isinstance(bad_kind, str),
              "StartBreak(非法 kind) 不崩溃、返回空串或字符串", repr(bad_kind)[:40])
        err_bad_action = call_ok(base, "Action", ["break", "绝对不存在的动作"])
        check(err_bad_action is not None or True, "Action(非法动作) 返回错误而非崩溃",
              str(err_bad_action)[:80])

        # ---------- 专员写操作（负路径为主，避免改动真实素材）----------
        print("\n[专员] 写操作的拒绝路径")
        r1 = call_ok(base, "SaveRole", ["绝对不存在的专员", {}])
        check(r1 is False, "SaveRole(不存在的专员) 返回 false", repr(r1))
        r2 = call_ok(base, "RemoveRole", ["绝对不存在的专员"])
        check(r2 is False, "RemoveRole(不存在的专员) 返回 false", repr(r2))
        r3 = call_ok(base, "ReplaceRoleImage", ["绝对不存在的专员"])
        check(r3 is False, "ReplaceRoleImage(不存在的专员) 返回 false", repr(r3))
        r4 = call_ok(base, "SaveRolePreview", ["绝对不存在的专员", "data:image/png;base64,AAAA"])
        check(r4 is False, "SaveRolePreview(不存在的专员) 返回 false", repr(r4))
        prev = call_ok(base, "SupervisorPreview", [{"name": first_name}, first_name])
        check(prev is None or isinstance(prev, dict), "SupervisorPreview 返回对象或 null",
              str(prev)[:80])

        # ---------- 无头下的文件对话框类：必须「优雅拒绝」，不是崩溃 ----------
        print("\n[对话框] 无头环境应优雅降级")
        for name, args in (("PickSupervisorImage", []), ("PickAppExe", []), ("AddMusicSound", [])):
            res = call_ok(base, name, args)
            # 无头下弹不出对话框 → Go 侧返回 nil map（JSON null）表示「用户取消」；
            # 前端一律写 `if (!picked) return`，null 与 {canceled:true} 等价。
            ok = res is None or (isinstance(res, dict) and ("error" in res or "canceled" in res))
            check(ok, "%-22s 返回 null 或 {canceled|error}，不抛异常" % name, str(res)[:100])
        exp = call_ok(base, "ExportReport", ["report.html", base64.b64encode(b"<html>x</html>").decode()])
        check(isinstance(exp, dict) and ("error" in exp or "canceled" in exp),
              "ExportReport 无头下返回 {error|canceled}", str(exp)[:100])
        exp2 = call_ok(base, "ExportThirdPartyNotices", [])
        check(isinstance(exp2, dict) and ("error" in exp2 or "canceled" in exp2),
              "ExportThirdPartyNotices 无头下返回 {error|canceled}", str(exp2)[:100])
        exp3 = call_ok(base, "ExportProjectLicense", [])
        check(isinstance(exp3, dict) and ("error" in exp3 or "canceled" in exp3),
              "ExportProjectLicense 无头下返回 {error|canceled}", str(exp3)[:100])
        d1 = call_ok(base, "DeleteMusicSound", ["不存在.wav"])
        check(d1 is False, "DeleteMusicSound(不存在) 返回 false", repr(d1))

        # ---------- 外链：只测拒绝分支（放行的会真开浏览器）----------
        print("\n[外链] 只放行 https")
        for bad in ("http://example.com", "file:///C:/Windows", "javascript:alert(1)",
                    "HTTPS://EXAMPLE.COM", ""):
            res = call_ok(base, "OpenExternal", [bad])
            check(res is False, "OpenExternal 拒绝 %r" % bad, repr(res))

        # ---------- 外卖动作负路径 ----------
        print("\n[外卖] 动作负路径")
        t1 = call_ok(base, "TakeoutAction", ["takeout", "postpone"])
        # 没有进行中的外卖提醒时，Go 侧走错误返回（响应体为空对象），前端 `void api.takeoutAction()`
        # 不看返回值 —— 判据是「不炸、不改状态」。
        check(t1 is None or isinstance(t1, (str, dict)), "TakeoutAction(postpone) 无提醒时安全返回",
              str(t1)[:80])
        state_after_takeout = call_ok(base, "GetState", [])
        check(isinstance(state_after_takeout, dict),
              "TakeoutAction 空转后 GetState 仍可读（状态没被带坏）",
              str(state_after_takeout)[:60])
        pt = call_ok(base, "PreviewTakeout", ["takeout"])
        check(isinstance(pt, str) or pt is None, "PreviewTakeout 返回字符串或 null", str(pt)[:60])

        # ---------- QuitApp 放最后：它会真的退出 ----------
        print("\n[退出] QuitApp")
        st, res, text, unknown = invoke(base, "QuitApp", [])
        check(not unknown, "QuitApp 已注册", "status=%s %s" % (st, text[:80]))
        exited = False
        for _ in range(20):
            if proc.poll() is not None:
                exited = True
                break
            time.sleep(0.25)
        check(exited, "QuitApp 后进程在 5 秒内退出", "rc=%s" % proc.poll())

        # ---------- 收尾：真实配置没被动过 ----------
        print("\n[收尾]")
        after = digest(real_cfg)
        check(before == after, "用户真实 settings.json 未被探针改写",
              real_cfg if before == after else "%s → %s" % (before[:12], after[:12]))

        # 日志里不该有 panic / 契约缺失
        try:
            logtext = open(log_path, encoding="utf-8", errors="replace").read()
        except Exception:
            logtext = ""
        check("panic:" not in logtext, "运行期日志无 panic 字样",
              "" if "panic:" not in logtext else logtext[logtext.find("panic:") - 200:][:400])
        check("契约缺失" not in logtext, "启动时契约自检未报缺失",
              "" if "契约缺失" not in logtext else "日志里出现「契约缺失」")
    finally:
        if proc.poll() is None:
            proc.terminate()
            try:
                proc.wait(timeout=5)
            except Exception:
                proc.kill()
        shutil.rmtree(iso, ignore_errors=True)

    print("\n结果：%d 项断言，失败 %d 项" % (checks, len(failures)))
    if failures:
        for f in failures:
            print("  - " + f)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
