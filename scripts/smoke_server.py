#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""无头冒烟：直接打 Wails 运行时的 HTTP 端点，验证「前端 → Go 绑定」这条链路。

为什么不用 GUI 跑：Wails v3 支持 `-tags server` 构建出无头服务（没有原生窗口，
但仍然提供资源服务 + `/wails/runtime` 的绑定调用端点）。这份脚本用与
`/wails/runtime.js` 里 `Call.ByName` **完全相同的请求形状**去调绑定，
所以验证的就是前端真正走的那条路：

    POST /wails/runtime
    {"object":0,"method":0,
     "args":{"call-id":"<id>","methodName":"workerrest.App.<方法>","args":[...]}}

断言分三层：
  1. 静态资源可服务（/、/break.html、/wails/runtime.js）；
  2. 契约里每个「已接通」的绑定都能按名解析（不允许 "unknown bound method name"）；
  3. 会话数据与**真实配置/角色目录**逐项对齐（时长、提醒内容、外观、角色名与几何）。

用法：python scripts/smoke_server.py [--port 29731] [--keep]
退出码 0 = 全部通过；非 0 = 有失败项（会逐条打印）。
"""

import argparse
import hashlib
import json
import os
import shutil
import socket
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)  # 项目根（原 rewrite-go，现已提升为顶层）
EXE = os.path.join(ROOT, "build", "bin", "chibingren_server.exe")
TEMP = os.environ.get("TEMP", ROOT)
REPORT = os.path.join(TEMP, "wr_go_smoke_report.txt")
LOG = os.path.join(TEMP, "wr_go_smoke_server.log")
# 隔离副本目录：绝不让冒烟碰用户的真实配置（Go 侧首次运行 / 补 installId 时会落盘）
ISO = os.path.join(TEMP, "wr_smoke_iso")

# Windows 控制台默认不是 UTF-8：断言标签里有中文，先固定 stdout 编码，
# 否则 print 会抛 UnicodeEncodeError（或被重定向成乱码，报告就不可读了）
try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
    sys.stderr.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass

# 内置角色的固定出场顺序（与 electron/src/supervisor.ts 的 BUILTIN_ROLE_META 同源）
BUILTIN_ROLE_ORDER = [
    "supervisor_default", "role_instructor", "role_executive", "role_mentor",
    "role_doctor", "role_coach", "role_warden", "role_shopkeeper", "role_ai",
]

# 绑定全限定名前缀：main 包在构建产物里的 PkgPath 就是 "main"（不是模块名——
# 这与 `go test` 里 reflect 拿到的值不同，见 rewrite-go/contract.go 的说明）。
# 真值可用 WR_LOG_LEVEL=debug 跑一次，日志里会打出 fqn=main.App.Xxx。
BINDING_PREFIX = "main.App."

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


def http(method, url, body=None, headers=None, timeout=15):
    req = urllib.request.Request(url, data=body, method=method)
    for k, v in (headers or {}).items():
        req.add_header(k, v)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            return r.status, r.read().decode("utf-8", "replace")
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode("utf-8", "replace")
    except Exception as e:  # 连接被拒 / 超时
        return 0, repr(e)


def call(base, name, args, window=""):
    """按 /wails/runtime.js 的 Call.ByName 形状调一个绑定。"""
    payload = {
        "object": 0,
        "method": 0,
        "args": {
            "call-id": uuid.uuid4().hex,
            "methodName": BINDING_PREFIX + name,
            "args": args,
        },
    }
    headers = {"Content-Type": "application/json", "x-wails-client-id": uuid.uuid4().hex}
    if window:
        headers["x-wails-window-name"] = window
    status, raw = http("POST", base + "/wails/runtime", json.dumps(payload).encode("utf-8"), headers)
    try:
        return status, json.loads(raw)
    except Exception:
        return status, raw


def read_json(path):
    with open(path, encoding="utf-8") as f:
        return json.load(f)


def find_real_settings():
    """找本机真实的 settings.json（与 Go 侧 settingsCandidates 同源：唯一候选 =
    运行目录的 config/settings.json，不再检查 %APPDATA%）。"""
    p = os.path.join(ROOT, "config", "settings.json")
    return p if os.path.exists(p) else None


def stats_file_for_settings(cfg_path):
    """与 Go 侧 statsFileForConfig 同口径：配置在 config/ 目录 → 上级 data/stats.json，
    否则与配置同目录。"""
    if not cfg_path:
        return ""
    d = os.path.dirname(cfg_path)
    if os.path.basename(d).lower() == "config":
        return os.path.join(os.path.dirname(d), "data", "stats.json")
    return os.path.join(d, "stats.json")


def _digest(path):
    """文件的 SHA-256（不存在返回 None）——用来断言真实配置一个字节都没被改"""
    if not path or not os.path.isfile(path):
        return None
    with open(path, "rb") as f:
        return hashlib.sha256(f.read()).hexdigest()


def _postpone_ms(cfg, typename):
    """从（隔离副本的）配置里取某个休息身份的 postponeMs。

    口径与 Go 侧 breakConfigOf 一致：键缺失 / 类型不符一律按默认 120000。
    取不到这个身份时返回 None（调用方据此跳过该断言，不硬凑）。
    """
    default = 120_000
    if not isinstance(cfg, dict) or not typename:
        return None
    if typename in ("short", "long"):
        m = cfg.get(typename)
        if not isinstance(m, dict):
            return default
        v = m.get("postponeMs")
        return v if isinstance(v, (int, float)) and not isinstance(v, bool) else default
    for it in (cfg.get("customBreaks") or []):
        if not isinstance(it, dict) or it.get("id") != typename:
            continue
        sub = it.get("config")
        if not isinstance(sub, dict):
            return default
        v = sub.get("postponeMs")
        return v if isinstance(v, (int, float)) and not isinstance(v, bool) else default
    return None


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--port", type=int, default=29731)
    ap.add_argument("--keep", action="store_true", help="退出时不关服务（排查用）")
    args = ap.parse_args()

    if not os.path.isfile(EXE):
        print("找不到 %s —— 先跑：go build -tags server -o build/bin/chibingren_server.exe ." % EXE)
        return 2

    port = free_port(args.port)
    base = "http://127.0.0.1:%d" % port
    print("无头服务冒烟：%s  (port=%d)" % (EXE, port))

    # ---- 配置隔离：拷一份真实配置到临时目录，用 WR_SETTINGS_PATH 独占指向它 ----
    # 为什么必须隔离：加载路径在「补生成 installId / 种入预置条目」时会回写配置文件。
    # 直接跑在真实配置上会改写用户的数据（第一次实测就把键序动掉了），这是不可接受的。
    real_settings = find_real_settings()
    real_digest = _digest(real_settings)
    # 统计文件按配置落点推导（config/ → 上级 data/stats.json）—— 它同样一个字节都不许被冒烟碰。
    # 第 8 段会断言「统计落点确实在隔离目录里」，这里再留一份真实文件的指纹兜底。
    real_stats = stats_file_for_settings(real_settings)
    real_stats_digest = _digest(real_stats)
    shutil.rmtree(ISO, ignore_errors=True)
    os.makedirs(ISO, exist_ok=True)
    iso_settings = os.path.join(ISO, "settings.json")
    if real_settings:
        shutil.copyfile(real_settings, iso_settings)

    env = dict(os.environ)
    env["WAILS_SERVER_PORT"] = str(port)
    env["WAILS_SERVER_HOST"] = "127.0.0.1"
    env["WR_SETTINGS_PATH"] = iso_settings
    # 打开 Wails 的 Debug 日志：日志里会列出真实注册的绑定全限定名（fqn=...），
    # 「按名调用找不到绑定」时这是唯一能直接看到真值的地方
    env["WR_LOG_LEVEL"] = "debug"
    env.pop("ELECTRON_RUN_AS_NODE", None)
    logf = open(LOG, "w", encoding="utf-8")
    proc = subprocess.Popen([EXE], cwd=ROOT, env=env, stdout=logf, stderr=subprocess.STDOUT)

    try:
        # ---- 等服务起来 ----
        up = False
        for _ in range(60):
            if proc.poll() is not None:
                break
            st, _raw = http("GET", base + "/", timeout=2)
            if st == 200:
                up = True
                break
            time.sleep(0.5)
        if not check(up, "服务在 15 秒内就绪", "rc=%s" % proc.poll()):
            print("--- 服务日志 ---")
            print(open(LOG, encoding="utf-8", errors="replace").read()[-3000:])
            return 1

        # ---- 1. 静态资源 ----
        st, body = http("GET", base + "/")
        check(st == 200 and "吃饼人也敢休" in body, "GET / 返回主窗口页面", "status=%s len=%d" % (st, len(body)))
        st, body = http("GET", base + "/break.html")
        check(st == 200 and "supervisor-line" in body, "GET /break.html 返回提醒窗口页面", "status=%s" % st)
        st, body = http("GET", base + "/wails/runtime.js")
        check(st == 200 and "ByID" in body and "ByName" in body, "GET /wails/runtime.js 提供运行时（含 ByID/ByName）", "status=%s len=%d" % (st, len(body)))

        # ---- 2. 按名解析：每个已接通的绑定都不许是 unknown bound method ----
        # 会**产生副作用**的绑定一律用「多传一个参数」探它是否注册（应是参数错误、
        # 方法体不执行），不然一次探测就把服务状态改了。两个已知的：
        #   - QuitApp 会真的退出；
        #   - StartBreak 会真的开始一次休息（Go 版走调度器：一旦 phase=break，
        #     后续再调 StartBreak 会返回空串 —— 那是**正确**的幂等行为，不是故障。
        #     所以真正的「开始一次休息」留给第 3 段，这里只探名字。
        probe = [
            ("BreakCurrent", ["break"], True),
            ("Action", ["break", "不再需要"], True),
            ("CloseWindow", ["break"], True),
            ("SupActivity", ["break"], True),
            ("GetState", [], True),
            ("PreviewBreak", ["short", ""], True),
            ("StartBreak", ["short", "bogus-extra-arg"], False),  # 预期参数错误，不真的开始休息
            ("AppInfo", [], True),
            ("ListRoles", [], True),
            ("OpenExternal", ["http://example.com"], True),  # 非 https：应被拒但方法本身可解析
            ("GetSettings", [], True),
            ("SaveSettings", [{}], True),
            ("QuitApp", ["bogus-extra-arg"], False),  # 预期参数错误，绝不能是「名字未知」
        ]
        for name, a, expect_ok in probe:
            st, res = call(base, name, a, window="break")
            text = res if isinstance(res, str) else json.dumps(res, ensure_ascii=False)
            unknown = "unknown bound method" in text
            if expect_ok:
                check(st == 200 and not unknown, "绑定 %s 按名解析并可调用" % name, "status=%s %s" % (st, text[:160]))
            else:
                check(not unknown, "绑定 %s 已注册（用错误参数探测，不真的执行）" % name, "status=%s %s" % (st, text[:160]))

        # ---- 3. 数据与真实配置 / 角色目录对齐 ----
        st, sid = call(base, "StartBreak", ["short"])
        check(st == 200 and isinstance(sid, str) and sid.startswith("short-"), "StartBreak 返回会话 id", "status=%s %s" % (st, sid))

        st, sess = call(base, "BreakCurrent", ["break"], window="break")
        check(st == 200 and isinstance(sess, dict), "BreakCurrent 返回会话对象", "status=%s %s" % (st, str(sess)[:200]))

        # 对齐基准用**隔离副本**（服务实际读的那一份），不是真实配置：
        # 服务可能已在加载时归一化并回写副本，拿副本读才是「服务眼里的配置」。
        cfg = read_json(iso_settings) if os.path.exists(iso_settings) else {}
        print("   （对齐基准配置：%s）" % (iso_settings if cfg else "未找到，跳过配置对齐"))

        if isinstance(sess, dict) and cfg:
            short = cfg.get("short") or {}
            check(sess.get("durationMs") == short.get("durationMs"),
                  "会话时长 == settings.json short.durationMs",
                  "%s vs %s" % (sess.get("durationMs"), short.get("durationMs")))
            appear = cfg.get("appearance") or {}
            check((sess.get("appearance") or {}).get("color") == appear.get("color"),
                  "外观色 == settings.json appearance.color",
                  "%s vs %s" % ((sess.get("appearance") or {}).get("color"), appear.get("color")))
            rm = ((cfg.get("reminderMessages") or {}).get("short") or {})
            want_msg = ""
            for it in (rm.get("items") or []):
                if it.get("id") == rm.get("fixedId"):
                    want_msg = it.get("text") or ""
            msgs = sess.get("messages") or []
            check(isinstance(msgs, list) and len(msgs) >= 1 and msgs[0] != "",
                  "messages 至少 1 条且非空", msgs)
            if want_msg:
                check(msgs and msgs[0] == want_msg, "messages[0] == 配置里固定选中的提醒内容",
                      "%r vs %r" % (msgs[0] if msgs else None, want_msg))

        sup = (sess or {}).get("supervisor") if isinstance(sess, dict) else None
        check(isinstance(sup, dict), "会话带督休专员信息", type(sup).__name__)
        if isinstance(sup, dict):
            img = sup.get("image") or ""
            check(img.startswith("data:image/") and len(img) > 5000, "立绘以 data URL 下发", "prefix=%s len=%d" % (img[:22], len(img)))
            check(bool(sup.get("name")), "角色有显示名", sup.get("name"))
            lines = sup.get("lines") or {}
            check(bool(lines.get("urge")), "第一段劝说台词非空", lines.get("urge"))
            check(bool(lines.get("command")), "第二段命令台词非空（来自 role.json 台词池）", lines.get("command"))
            geom = sup.get("geom") or {}
            check(all(k in geom for k in ("sizePct", "posXPct", "posYPct")),
                  "几何字段齐全（sizePct/posXPct/posYPct）", geom)

            # 与真实配置里固定的那个角色目录对齐
            fixed = (cfg.get("supervisor") or {}).get("fixedKey") if cfg else None
            if fixed:
                rp = os.path.join(ROOT, "roles", fixed, "role.json")
                if os.path.isfile(rp):
                    rc = read_json(rp)
                    check(sup.get("name") == rc.get("name"),
                          "角色名 == roles/%s/role.json name" % fixed,
                          "%s vs %s" % (sup.get("name"), rc.get("name")))
                    rg = rc.get("geom") or {}
                    check(geom.get("sizePct") == rg.get("sizePct") and geom.get("posXPct") == rg.get("posXPct"),
                          "角色几何 == roles/%s/role.json geom" % fixed,
                          "%s vs %s" % (geom, rg))
                    cmds = [i.get("text") for i in ((rc.get("messages") or {}).get("items") or []) if i.get("enabled")]
                    check(lines.get("command") in cmds,
                          "命令台词取自该角色 role.json 的启用条目",
                          repr(lines.get("command")))
                else:
                    print("   （角色 %s 的 role.json 不存在，跳过角色对齐）" % fixed)

        # ---- ListRoles / AppInfo / GetState ----
        st, roles = call(base, "ListRoles", [])
        check(st == 200 and isinstance(roles, list) and len(roles) > 0, "ListRoles 返回角色目录扫描结果",
              "status=%s n=%s" % (st, len(roles) if isinstance(roles, list) else roles))
        if isinstance(roles, list) and roles:
            keys = [r.get("key") for r in roles]
            check(all(r.get("thumb", "").startswith("data:image/") for r in roles),
                  "每个角色都带缩略图 data URL（字段名 thumb，与设置页一致）", keys)
            # 顺序 = BUILTIN_ROLE_META 的内置清单顺序（立绘库磁贴与顺序轮换的出场次序）；
            # 不是目录名字典序 —— 这里与其后追加的自定义角色拼起来校验前缀
            check(keys[:len(BUILTIN_ROLE_ORDER)] == BUILTIN_ROLE_ORDER,
                  "内置角色按 BUILTIN_ROLE_META 的顺序在前", keys)
            check(all(r.get("geom", {}).get("sizePct") for r in roles),
                  "每个角色都带完整 geom（尺寸/位置滑条要用）", keys[:2])
            check(all(isinstance(r.get("messages", {}).get("items"), list) for r in roles),
                  "每个角色都带 messages（设置页台词面板要用）", keys[:2])
            if cfg:
                fixed = (cfg.get("supervisor") or {}).get("fixedKey")
                check(fixed in keys, "配置里固定的角色 %s 在扫描结果中" % fixed, keys)

        st, info = call(base, "AppInfo", [])
        check(st == 200 and isinstance(info, dict) and info.get("engine") == "go+wails3",
              "AppInfo 返回环境信息", info)
        if isinstance(info, dict) and isinstance(roles, list):
            check(info.get("roles") == len(roles), "AppInfo.roles == ListRoles 长度", info.get("roles"))

        st, state = call(base, "GetState", [])
        check(st == 200 and isinstance(state, dict), "GetState 返回状态快照", str(state)[:200])
        if isinstance(state, dict):
            check(state.get("serverMode") is True, "GetState.serverMode 为 true（无头构建）", state.get("serverMode"))
            check(bool(state.get("rolesRoot")), "GetState.rolesRoot 已解析", state.get("rolesRoot"))
            check(state.get("settings") == iso_settings, "GetState.settings == 隔离副本路径（WR_SETTINGS_PATH 独占生效）",
                  "%s vs %s" % (state.get("settings"), iso_settings))

        # ---- 4. 调度器状态机：动作类绑定真的推动了状态机 ----
        # 走到这里 StartBreak("short") 已经穿过调度器（phase=break、occurrence 已消费），
        # 正好先验「休息中」的形状，再用 Action 把状态机逐个动作推一遍、每步回读核验。
        # 这里验的是**绑定真的接到了状态机**上（不是只返回了个形状对的壳）——
        # 具体规则的正确性由计划层对拍（verify_plan_parity.py）与 scheduler_test 覆盖。
        st, st0 = call(base, "GetState", [])
        check(st == 200 and isinstance(st0, dict), "GetState 基准快照可取", str(st0)[:120])
        if isinstance(st0, dict):
            check(st0.get("phase") in ("idle", "pre", "break"),
                  "phase 是 idle/pre/break 之一", st0.get("phase"))
            check(st0.get("phase") == "break",
                  "StartBreak 之后 phase == break（动作确实进了调度器）", st0.get("phase"))
            check(st0.get("paused") is False or st0.get("paused") is True, "paused 是布尔", st0.get("paused"))
            check(isinstance(st0.get("shortsSinceLong"), int)
                  and not isinstance(st0.get("shortsSinceLong"), bool),
                  "shortsSinceLong 是整数", st0.get("shortsSinceLong"))
            check(isinstance(st0.get("pauseReasons"), list), "pauseReasons 是数组", st0.get("pauseReasons"))
            check(isinstance(st0.get("lines"), list), "lines 是数组", type(st0.get("lines")).__name__)
            check(isinstance(st0.get("timeline"), list), "timeline 是数组", type(st0.get("timeline")).__name__)
            check(isinstance(st0.get("fixedNext"), dict), "fixedNext 是对象", type(st0.get("fixedNext")).__name__)
            check(st0.get("rest") is None or isinstance(st0.get("rest"), dict),
                  "rest 是对象或 null", type(st0.get("rest")).__name__)
            check(st0.get("other") is None or isinstance(st0.get("other"), dict),
                  "other 是对象或 null", type(st0.get("other")).__name__)
            check(st0.get("nextSource") in (None, "countdown", "fixed"),
                  "nextSource 是 countdown/fixed/null", st0.get("nextSource"))
            if cfg:
                want = (cfg.get("short") or {}).get("durationMs")
                got = int(st0.get("breakEndsAt") or 0) - int(st0.get("breakStartedAt") or 0)
                check(isinstance(want, (int, float)) and got == want,
                      "休息中 breakEndsAt - breakStartedAt == short.durationMs",
                      "%s vs %s" % (got, want))
            tl = st0.get("timeline") or []
            cd_items = [it for it in tl if it.get("source") == "countdown"]
            fx_items = [it for it in tl if it.get("source") == "fixed"]
            check(all("paused" in it for it in cd_items),
                  "时间线上的倒计时项都带 paused 键", len(cd_items))
            check(all("paused" not in it for it in fx_items),
                  "时间线上的固定项都不带 paused 键", len(fx_items))
            for it in (st0.get("lines") or []):
                check(all(k in it for k in ("type", "name", "at", "source")),
                      "挂起线字段齐全（type/name/at/source）", it)
                break

        # ① 提前结束：回等待态，并把这一次短休息记进长短交替计数
        call(base, "Action", ["settings", "endBreak"])
        st, st1 = call(base, "GetState", [])
        if isinstance(st1, dict):
            check(st1.get("phase") == "idle", "endBreak 之后 phase 回到 idle", st1.get("phase"))
            check(st1.get("shortsSinceLong") == 1,
                  "endBreak 把一次短休息记进长短交替计数", st1.get("shortsSinceLong"))
            if st1.get("nextAt"):
                nt = st1.get("nextType")
                ref = st1.get("rest") if nt in ("short", "long") else st1.get("other")
                check(isinstance(ref, dict) and ref.get("at") == st1.get("nextAt"),
                      "全局 next（%s）落在状态卡的对应分组里" % nt,
                      "%s vs %s" % (ref, st1.get("nextAt")))
                check(st1.get("nextSource") in ("countdown", "fixed"),
                      "有安排时 nextSource 非空", st1.get("nextSource"))

        # ② 暂停 / 恢复往返：pauseReasons 与 pausedAt 都要动
        call(base, "Action", ["settings", "pauseToggle"])
        st, st2 = call(base, "GetState", [])
        if isinstance(st2, dict):
            check(st2.get("paused") is True, "pauseToggle → paused == true", st2.get("paused"))
            check(st2.get("pauseReasons") == ["manual"], "pauseReasons == ['manual']", st2.get("pauseReasons"))
            check(isinstance(st2.get("pausedAt"), int) and int(st2.get("pausedAt") or 0) > 0,
                  "暂停记下了 pausedAt", st2.get("pausedAt"))
        call(base, "Action", ["settings", "pauseToggle"])
        st, st3 = call(base, "GetState", [])
        if isinstance(st3, dict):
            check(st3.get("paused") is False, "再次 pauseToggle → paused == false", st3.get("paused"))
            check(st3.get("pauseReasons") == [], "恢复后 pauseReasons 清空", st3.get("pauseReasons"))

        # ③ 推迟：nextAt 应重锚到「现在 + 该身份的 postponeMs」（不是原地不动）
        if isinstance(st3, dict) and st3.get("nextAt"):
            pm = _postpone_ms(cfg, st3.get("nextType"))
            t0 = int(time.time() * 1000)
            call(base, "Action", ["settings", "postpone"])
            st, st4 = call(base, "GetState", [])
            check(st == 200 and isinstance(st4, dict), "postpone 后仍能取到状态", str(st4)[:120])
            if isinstance(st4, dict) and pm is not None:
                delta = int(st4.get("nextAt") or 0) - t0
                # 全局 nextAt = min(倒计时线, 固定时刻线)，postpone 只推迟倒计时线。
                # 若某条固定线恰落在 (t0, t0+pm] 内，重锚后的全局 next 会被它接管——
                # 这时 nextAt ≠ now+pm 是产品合法行为（隔离副本来自真实配置，
                # 固定线时刻会随一天中的运行时间变化），豁免之。
                ats = {int(v) for v in (st3.get("fixedNext") or {}).values()
                       if isinstance(v, (int, float))}
                took_over = (int(st4.get("nextAt") or 0) in ats
                             and any(t0 < a <= t0 + pm for a in ats))
                check(abs(delta - pm) <= 3000 or took_over,
                      "postpone 把 nextAt 推到「现在 + %s 的 postponeMs」（固定线接管时除外）" % st3.get("nextType"),
                      "delta=%d want=%d ats=%s" % (delta, pm, sorted(ats)))

        # ④ 重置周期：长短交替计数归零
        call(base, "Action", ["settings", "resetCycle"])
        st, st5 = call(base, "GetState", [])
        if isinstance(st5, dict):
            check(st5.get("shortsSinceLong") == 0,
                  "resetCycle 把 shortsSinceLong 归零", st5.get("shortsSinceLong"))
            check(st5.get("phase") in ("idle", "pre", "break"), "重置后 phase 仍合法", st5.get("phase"))

        # ---- 5. 系统能力：空闲 / 勿扰 / 进程 / 自启动 ----
        # 无头模式下**不启动**会改调度器状态的三条监视（冒烟机始终空闲，会把调度器
        # 随机挂起、把上面那些确定性断言搞成掷骰子），但探测本身是照常做的 ——
        # 所以这里验的是「几个系统调用真的通了、字段形状对」，不是「暂停链路跑起来了」
        # （暂停链路由 sys_test.go 用假宿主 + 真进程名驱动）。
        st, probe = call(base, "SysProbe", [])
        check(st == 200 and isinstance(probe, dict), "SysProbe 返回系统能力自检对象", str(probe)[:200])
        if isinstance(probe, dict):
            idle = probe.get("idle") or {}
            check(isinstance(idle, dict) and idle.get("supported") is True,
                  "空闲检测可用（user32!GetLastInputInfo）", idle)
            check(isinstance(idle.get("idleMs"), int) and not isinstance(idle.get("idleMs"), bool)
                  and 0 <= idle["idleMs"] <= 86400000,
                  "idleMs 是 [0, 24h] 的整数", idle.get("idleMs"))
            check(isinstance(idle.get("active"), bool), "idle.active 是布尔", idle.get("active"))

            dnd = probe.get("dnd") or {}
            check(isinstance(dnd, dict) and isinstance(dnd.get("supported"), bool)
                  and isinstance(dnd.get("active"), bool) and bool(dnd.get("detail")),
                  "勿扰探测返回 {supported, active, detail}", dnd)

            procs = probe.get("processes") or {}
            check(isinstance(procs, dict) and procs.get("supported") is True,
                  "进程枚举可用（Toolhelp32）", procs.get("detail"))
            check(isinstance(procs.get("names"), list) and len(procs["names"]) > 5,
                  "枚举到的进程数 > 5（不像空壳）", len(procs.get("names") or []))

            auto = probe.get("autostart") or {}
            check(isinstance(auto, dict) and auto.get("valueName") == "com.lwb.workerrest",
                  "自启动值名与 Electron 版一致（怕留注册表孤儿项）", auto.get("valueName"))
            check(isinstance(auto.get("exists"), bool) and isinstance(auto.get("wanted"), bool),
                  "自启动读回 {exists, wanted} 两个布尔", auto)

        # ListRunningApps 必须是 **{supported, names, detail} 对象**：
        # 设置页按 r.supported 分流，返回数组会让 .supported 恒为 undefined，
        # 「从运行中的程序选择」永远停在「当前平台不支持」。
        st, running = call(base, "ListRunningApps", [])
        check(st == 200 and isinstance(running, dict) and running.get("supported") is True,
              "ListRunningApps 返回对象且 supported == true（不是字符串数组）", str(running)[:200])
        if isinstance(running, dict):
            names = [str(n).lower() for n in (running.get("names") or [])]
            check(bool(names), "ListRunningApps.names 非空", len(names))
            check("chibingren_server.exe" in names,
                  "运行中程序列表里有本服务自己的进程", [n for n in names if "chibingren" in n][:3])

        # ---- 6. 提前提示窗口（右下角小窗）：prebreak:state / prebreak:action ----
        # 无头构建没有原生窗口，但**状态与动作照常可用**，所以这一节验两件事：
        #   ① PreBreakState 的六个字段真的从调度器来（渲染层每 500ms 轮询它刷新倒计时，
        #      少一个字段不会报错、只会静默显示错内容）；
        #   ② 四个动作真的汇聚到调度器（不是只返回了个形状对的壳）。
        # 「那个小窗真的弹出来了」由 scripts/probe_gui_boot.py 拉起 GUI 产物读日志覆盖。

        def arm_prebreak():
            """把配置改成「马上进入提前提示区间」：短休息 60 秒倒计时、提前 60 秒提示。

            每次动作前都要重来一遍：推迟 / 跳过会把 nextAt 推到两分钟外，不再落在提前
            区间里，不重设就等不到下一次 pre（调度器只在 idle→pre 那一刻发通知）。
            """
            call(base, "SaveSettings", [{
                "short": {"enabled": True, "mode": "countdown",
                          "intervalMs": 60000, "notifyBeforeMs": 60000},
                "long": {"enabled": False},
                "takeout": {"enabled": False},
                "customBreaks": [],
            }])
            time.sleep(1.3)  # 调度器 500ms 一跳，等它把 phase 推成 pre

        arm_prebreak()
        st, gs_pre = call(base, "GetState", [])
        check(isinstance(gs_pre, dict) and gs_pre.get("phase") == "pre",
              "进入提前区间后调度器 phase == pre", (gs_pre or {}).get("phase"))

        st, pb = call(base, "PreBreakState", [])
        check(st == 200 and isinstance(pb, dict), "PreBreakState 返回状态对象", str(pb)[:200])
        if isinstance(pb, dict):
            for k in ("phase", "paused", "remainMs", "title", "custom", "countdownTail"):
                check(k in pb, "PreBreakState 带字段 %s（渲染层直接读它）" % k, sorted(pb.keys()))
            check(pb.get("phase") == "pre", "PreBreakState.phase == pre", pb.get("phase"))
            check(pb.get("custom") is False, "内置短休息走内置版式（custom=false）", pb.get("custom"))
            check(pb.get("title") == "您已连续工作较长时间，建议您短暂休息",
                  "标题是内置劝说文案（自定义提醒才用「XX时间到」）", pb.get("title"))
            check(pb.get("countdownTail") == " 秒进入休息",
                  "倒计时后缀是内置那套（含前导空格，接在数字后面）", repr(pb.get("countdownTail")))
            rm = pb.get("remainMs")
            check(isinstance(rm, int) and not isinstance(rm, bool) and 0 < rm <= 60000,
                  "remainMs 是 (0, 60s] 的整数", rm)

        # 暂停 / 恢复往返：暂停要关窗（onPaused），恢复时仍在提前区间内要重新弹出来（onPreNotify）
        call(base, "PreBreakAction", ["pause"])
        time.sleep(0.3)
        st, pb_paused = call(base, "PreBreakState", [])
        check(isinstance(pb_paused, dict) and pb_paused.get("paused") is True,
              "PreBreakAction(pause) → paused == true（与托盘暂停同一入口）",
              (pb_paused or {}).get("paused"))
        call(base, "PreBreakAction", ["pause"])
        time.sleep(0.3)
        st, pb_resumed = call(base, "PreBreakState", [])
        check(isinstance(pb_resumed, dict) and pb_resumed.get("paused") is False,
              "再次 PreBreakAction(pause) → 恢复", (pb_resumed or {}).get("paused"))

        # 推迟：nextAt 重锚到「现在 + short.postponeMs」
        arm_prebreak()
        mapi_short = _postpone_ms(read_json(iso_settings), "short")
        t_pb = int(time.time() * 1000)
        call(base, "PreBreakAction", ["postpone"])
        st, gs_post = call(base, "GetState", [])
        if isinstance(gs_post, dict) and mapi_short is not None:
            check(gs_post.get("phase") == "idle", "推迟后 phase 回到 idle", gs_post.get("phase"))
            delta = int(gs_post.get("nextAt") or 0) - t_pb
            check(abs(delta - mapi_short) <= 3000,
                  "PreBreakAction(postpone) 把 nextAt 推到「现在 + short.postponeMs」",
                  "delta=%d want=%d" % (delta, mapi_short))

        # 跳过：重排下一条、且不进入休息。
        # phase 只断言 != "break"：arm_prebreak 的配置（间隔 60s = 提前 60s）下，
        # 跳过重排出的新线恰好落在提前区间边界上，调度器下一个 tick 把 phase 推成
        # pre 是**产品合法行为**——是否已经 tick 到是 500ms 轮询相位的掷骰子。
        arm_prebreak()
        st, gs_before_skip = call(base, "GetState", [])
        before_skip = int((gs_before_skip or {}).get("nextAt") or 0)
        call(base, "PreBreakAction", ["skip"])
        st, gs_skip = call(base, "GetState", [])
        if isinstance(gs_skip, dict):
            check(gs_skip.get("phase") != "break", "跳过不开始休息（重排线可立即进入提前区间）",
                  gs_skip.get("phase"))
            check(int(gs_skip.get("nextAt") or 0) != before_skip,
                  "跳过重排了下一条（nextAt 变了）",
                  "%s -> %s" % (before_skip, gs_skip.get("nextAt")))

        # 立即休息：与托盘「立即休息」同一入口，真的开始一次休息
        arm_prebreak()
        call(base, "PreBreakAction", ["breakNow"])
        time.sleep(0.3)
        st, gs_now = call(base, "GetState", [])
        check(isinstance(gs_now, dict) and gs_now.get("phase") == "break",
              "PreBreakAction(breakNow) → phase == break（真的进了调度器）",
              (gs_now or {}).get("phase"))

        # 未知动作必须被拒绝：静默无事是「按名调用」链路最容易漏的坑
        st_bad, bad = call(base, "PreBreakAction", ["fly"])
        bad_text = bad if isinstance(bad, str) else json.dumps(bad, ensure_ascii=False)
        check(st_bad != 200 or "未知的提前提示动作" in bad_text,
              "未知动作被拒绝（返回错误而不是静默无事）",
              "status=%s %s" % (st_bad, bad_text[:160]))

        # ---- 7. 提示音：内置音资源 / 自定义音路由 / 播放链路 ----
        # 无头构建放不出声（没有承载 <audio> 的原生窗口），但**选曲与播放指令这条链路照走**，
        # 所以这一节验三件事：
        #   ① 内置提示音真的随包可访问（资源服务 /sounds/…）——「开箱就有声」的前提；
        #   ② 用户导入的音乐能经本进程的只读路由取到，且白名单外的文件被拒；
        #   ③ 休息开始 / 结束时真的按配置播了那一首（日志留痕）。
        # 「真的发出声音」只有 GUI 下才验得到，由 scripts/probe_gui_boot.py 覆盖。

        st, u_builtin = call(base, "SoundFileURL", ["short_start.wav"])
        check(st == 200 and u_builtin == "/sounds/short_start.wav",
              "SoundFileURL(内置音) → 资源服务的 /sounds/…", u_builtin)
        st, u_missing = call(base, "SoundFileURL", ["nope.wav"])
        check(st == 200 and u_missing == "",
              "SoundFileURL(不存在的文件) → 空串（渲染层据此提示「找不到该音频文件」）",
              repr(u_missing))

        status, wav = http("GET", base + "/sounds/short_start.wav")
        check(status == 200 and wav.startswith("RIFF"),
              "资源服务能取到内置提示音（RIFF 头）",
              "status=%s head=%r len=%d" % (status, wav[:4], len(wav)))

        # 用户导入的音乐：造一条放在「配置目录旁的 sounds/」（Go 版 customSoundDir 的落点）
        music_dir = os.path.join(ISO, "sounds")
        os.makedirs(music_dir, exist_ok=True)
        my_music = os.path.join(music_dir, "music-smoke.wav")
        with open(my_music, "wb") as f:
            f.write(b"RIFF" + b"\x00" * 2044)

        st, u_custom = call(base, "SoundFileURL", [my_music])
        check(st == 200 and isinstance(u_custom, str) and u_custom.startswith("/__sound/?p="),
              "SoundFileURL(自定义音) → 本进程的只读路由", u_custom)
        if isinstance(u_custom, str) and u_custom:
            status, got = http("GET", base + u_custom)
            check(status == 200 and got.startswith("RIFF"),
                  "自定义音可经只读路由取到", "status=%s len=%d" % (status, len(got)))

        # 白名单：音频目录之外的文件一律 403 —— 这条路由绝不能变成任意文件读取服务
        outsider = os.path.join(ISO, "outsider.wav")
        with open(outsider, "wb") as f:
            f.write(b"RIFF" + b"\x00" * 64)
        status, _ = http("GET", base + "/__sound/?p=" + urllib.parse.quote(outsider))
        check(status == 403, "音频目录外的文件被拒（403）", status)

        # 播放链路：把音乐列表换成「只有刚造的那一条」的单曲循环，于是每次播放都该是它。
        # 轮换（随机 / 顺序）的规则在单测里逐条钉死了，这里只需要「真的走到播放」。
        #
        # 连带把「声音测试」这条自定义提醒也建出来：下一步要用它当 StartBreak 的身份，
        # 而前面 arm_prebreak 已经把 customBreaks 清空过。
        def set_sound(enabled, single_id):
            call(base, "SaveSettings", [{
                "short": {"enabled": False},
                "long": {"enabled": False},
                "customBreaks": [{"id": "cb-snd", "name": "声音测试", "enabled": True,
                                  "config": {"enabled": True, "mode": "countdown",
                                             "intervalMs": 60000, "durationMs": 5000,
                                             "notifyBeforeMs": 0, "postponeMs": 60000,
                                             "slots": []}}],
                "sound": {
                    "enabled": enabled, "volume": 0.6, "mode": "single",
                    "singleId": single_id, "seeded": True,
                    "music": [{"id": "sm-smoke", "file": my_music, "name": "冒烟音乐",
                               "enabled": True}],
                },
            }])
            time.sleep(0.4)

        # 只统计「改配置之后」的播放日志（前面第 6 段的立即休息也播过一次）
        def count_plays():
            if not os.path.exists(LOG):
                return 0
            with open(LOG, encoding="utf-8", errors="replace") as f:
                return len([l for l in f if "[sound] 播放" in l])

        set_sound(True, "sm-smoke")
        play_mark = count_plays()

        call(base, "Action", ["settings", "endBreak"])  # 第 6 段留下的那次休息：结束 → 播
        time.sleep(0.4)
        call(base, "StartBreak", ["cb-snd"])            # 真开一次休息 → 播
        time.sleep(0.4)
        call(base, "Action", ["settings", "endBreak"])  # 再结束 → 播
        time.sleep(0.4)

        # ---------------------------------------------------------- 第 8 段：使用统计
        print("\n[8] 使用统计（天记录 / 周期聚合 / 清空）")
        st, probe = call(base, "StatsProbe", [])
        check(st == 200 and isinstance(probe, dict), "StatsProbe 返回统计自检对象", str(probe)[:200])
        if isinstance(probe, dict):
            # 落点必须隔离：统计文件跟 WR_SETTINGS_PATH 的隔离副本放同一目录
            pfile = str(probe.get("file") or "")
            check(pfile.startswith(ISO), "统计文件落在隔离目录里（不碰用户真实统计）", pfile)

        st, gs = call(base, "GetStats", [])
        check(st == 200 and isinstance(gs, dict), "GetStats 返回对象", str(gs)[:160])
        today_stats = gs.get("today") if isinstance(gs, dict) else None
        periods = gs.get("periods") if isinstance(gs, dict) else None
        check(isinstance(today_stats, dict), "GetStats.today 是对象", type(today_stats).__name__)
        # 占位时期这里给的是 map —— 契约要的是**数组**，长度 4、顺序 周/月/季/年
        check(isinstance(periods, list) and len(periods) == 4,
              "GetStats.periods 是长度 4 的数组（周/月/季/年）",
              "%s len=%s" % (type(periods).__name__,
                             len(periods) if isinstance(periods, list) else "-"))
        if isinstance(periods, list) and len(periods) == 4:
            pkeys = [str(p.get("key") or "") for p in periods]
            check("-W" in pkeys[0] and "-Q" in pkeys[2] and pkeys[3].isdigit(),
                  "周期顺序为 周 → 月 → 季 → 年", pkeys)
            check(all(isinstance(p.get("workMs"), int) for p in periods),
                  "每个周期都带 workMs（聚合字段齐全）", "")

        # today 的字段清单就是渲染层的读法（statCells 直接遍历它）
        STAT_KEYS = ("date", "workMs", "breakMs", "awayMs", "shortMs", "longMs",
                     "shortCount", "longCount", "customCount", "customCounts",
                     "postponeCount", "postponeMs", "skipCount", "skipMs",
                     "takeoutOrdered", "takeoutSkipped", "takeoutPostponed")
        today_date = today_stats.get("date") if isinstance(today_stats, dict) else None
        if isinstance(today_stats, dict):
            missing_keys = [k for k in STAT_KEYS if k not in today_stats]
            check(not missing_keys, "today 带全部 17 个字段", "缺 %s" % missing_keys)
            # 占位时期这里给的是空数组 —— 契约要的是对象（id → 次数）
            check(isinstance(today_stats.get("customCounts"), dict),
                  "customCounts 是对象（不是数组）", type(today_stats.get("customCounts")).__name__)
            # 工作累加器每秒一跳：跑到这里至少记了一笔，说明记账链路真的在跑（不是返回空表）
            check(isinstance(today_stats.get("workMs"), int) and today_stats["workMs"] > 0,
                  "工作累加器真的在记账（today.workMs > 0）", today_stats.get("workMs"))

        st, hist = call(base, "GetHistory", ["", ""])
        check(st == 200 and isinstance(hist, dict), "GetHistory 返回对象", str(hist)[:160])
        if isinstance(hist, dict):
            check(isinstance(hist.get("days"), list), "GetHistory.days 是数组",
                  type(hist.get("days")).__name__)
            check(isinstance(hist.get("customNames"), dict),
                  "GetHistory.customNames 是对象（不是数组）",
                  type(hist.get("customNames")).__name__)
            hist_days = hist.get("days") or []
            if today_date:
                check(any(d.get("date") == today_date for d in hist_days),
                      "历史明细里有今天这条记录", "%d 天" % len(hist_days))

        # 清空：返回 {ok:true}，随后 today 归零（清空会把工作段起点一并挪到现在）
        st, clr = call(base, "ClearStats", [])
        check(st == 200 and isinstance(clr, dict) and clr.get("ok") is True,
              "ClearStats 返回 {ok:true}", clr)
        time.sleep(0.2)
        st, gs2 = call(base, "GetStats", [])
        t2 = gs2.get("today") if isinstance(gs2, dict) else None
        if isinstance(t2, dict):
            check(int(t2.get("workMs") or 0) <= 2000, "清空后工作时长归零（最多又走了 1 秒）",
                  t2.get("workMs"))
            check(int(t2.get("shortCount") or 0) == 0 and int(t2.get("takeoutOrdered") or 0) == 0,
                  "清空后次数类字段全部归零", t2.get("shortCount"))
        check(os.path.exists(os.path.join(ISO, "stats.json")),
              "统计文件真的落了盘（清空会立刻写一次）", os.path.join(ISO, "stats.json"))

        # ---------------------------------------------------------- 第 9 段：外卖窗口
        print("\n[9] 外卖提醒（会话构建 / 动作记账 / 关窗）")
        # 初始无会话：TakeoutCurrent 返回 nil（或 false，取决于 Wails 的 null 序列化）
        st, tk0 = call(base, "TakeoutCurrent", ["takeout"], window="takeout")
        check(st == 200 and (tk0 is None or tk0 is False),
              "初始无外卖会话（TakeoutCurrent 返回空）", tk0)
        # 预览弹一次外卖：无头下不建窗，但会话照常构建（server 模式短路窗口创建）
        st, pv = call(base, "PreviewTakeout", ["takeout"], window="takeout")
        check(st == 200, "PreviewTakeout 可调用", "status=%s %s" % (st, str(pv)[:120]))
        st, tk = call(base, "TakeoutCurrent", ["takeout"], window="takeout")
        check(st == 200 and isinstance(tk, dict), "TakeoutCurrent 返回会话对象",
              "status=%s %s" % (st, str(tk)[:200]))
        if isinstance(tk, dict):
            # 渲染层 takeout.ts 读的字段：少一个就「Cannot read properties of undefined」
            TK_KEYS = ("id", "at", "hour", "minute", "firedAt", "preview", "meal",
                       "leadMin", "postponeMin", "postponedCount", "message",
                       "appearance", "supervisor", "redPacket", "redPacketData")
            missing = [k for k in TK_KEYS if k not in tk]
            check(not missing, "外卖会话带全部渲染层字段", "缺 %s" % missing)
            check(tk.get("preview") is True, "预览会话 preview=true", tk.get("preview"))
            check(bool(tk.get("meal")), "会话带餐段文案（早餐/午餐/…）", tk.get("meal"))
            check(tk.get("meal") in ("早餐", "午餐", "下午茶", "晚餐", "夜宵"),
                  "餐段文案在合法枚举内", tk.get("meal"))
            check(isinstance(tk.get("appearance"), dict)
                  and "color" in tk.get("appearance", {}),
                  "会话带外观（含颜色）", tk.get("appearance"))
            check(isinstance(tk.get("redPacketData"), (dict, type(None))),
                  "redPacketData 是对象或 null", tk.get("redPacketData"))
            # 红包开启时地址要指向 installId；这里配置默认关闭 → null
            # （真实配置里 redPacket 可能开着，所以不断言具体值，只断形状）
            if tk.get("redPacket"):
                rd = tk.get("redPacketData") or {}
                check(str(rd.get("url") or "").startswith("https://nav.hcctc.cn/api/cps/id="),
                      "红包开启时地址指向本机专属领取地址", rd.get("url"))
        # 动作：预览态点「不点了」→ 只关会话，不记真实统计（预览不算次数）
        st, _ = call(base, "TakeoutAction", ["takeout", "skip"], window="takeout")
        check(st == 200, "TakeoutAction(skip) 可调用", "status=%s" % st)
        st, tk2 = call(base, "TakeoutCurrent", ["takeout"], window="takeout")
        check(tk2 is None or tk2 is False, "动作后会话已清空", tk2)

        # ------------------------------------------------ 第 10 段：文件选择 / 导入导出 / 更新
        # 无头（server）模式下没有原生窗口，这些绑定一律走「取消 / 失败」路径——
        # 这里验的是「绑定可调用、返回形状正确的取消/失败值、不会因为没窗口而崩溃」。
        print("\n[10] 文件选择 / 导入导出 / 更新检查（无头走取消/失败路径）")
        st, r = call(base, "PickSupervisorImage", [], window="settings")
        check(st == 200 and r is None, "PickSupervisorImage 无头返回 null（取消）", r)
        st, r = call(base, "PickAppExe", [], window="settings")
        check(st == 200 and r is None, "PickAppExe 无头返回 null（取消）", r)
        st, r = call(base, "AddMusicSound", [], window="settings")
        check(st == 200 and isinstance(r, dict) and r.get("canceled") is True,
              "AddMusicSound 无头返回 {canceled:true}", r)
        st, ok = call(base, "ExportSettings", [], window="settings")
        check(st == 200 and ok is False, "ExportSettings 无头返回 false", ok)
        st, ok = call(base, "ImportSettings", [], window="settings")
        check(st == 200 and ok is False, "ImportSettings 无头返回 false", ok)
        # 更新检查：无头不发网络，返回确定性 error 形状（status + current + message 齐全）
        st, up = call(base, "CheckUpdate", [], window="settings")
        check(st == 200 and isinstance(up, dict) and up.get("status") == "error"
              and up.get("current") and up.get("message"),
              "CheckUpdate 无头返回确定性 error 形状", up)
        st, us = call(base, "UpdateStatus", [], window="settings")
        check(st == 200 and isinstance(us, dict) and "status" in us and "current" in us,
              "UpdateStatus 返回 {status,current}", us)

        # ---- 收尾：关窗清会话 ----
        st, _ = call(base, "CloseWindow", ["break"], window="break")
        st, sess2 = call(base, "BreakCurrent", ["break"], window="break")
        check(sess2 is None or sess2 is False, "CloseWindow 后会话已清空", sess2)

        # ---- 启动日志里的契约自检 ----
        time.sleep(0.3)
        proc.terminate()
        try:
            proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            proc.kill()
        logtext = open(LOG, encoding="utf-8", errors="replace").read()
        check("前端契约自检通过" in logtext, "启动日志里有契约自检通过", "")
        check("前端契约缺失绑定" not in logtext, "启动日志里没有契约缺失告警", "")
        # 计划摘要落日志：一眼能从日志看出「下一次休息是什么、什么时候」
        check("[plan]" in logtext, "启动日志里有计划摘要（[plan] 行）", "")
        check("[scheduler] 开始休息" in logtext, "日志记录了调度器真实开始过一次休息", "")
        # 系统能力探测结果落日志（排查「自启动没生效 / 空闲不暂停」时第一眼要看的就是它）
        check("[sys]" in logtext, "启动日志里有系统能力摘要（[sys] 行）", "")
        check("无头模式：不启动空闲 / 勿扰 / 应用占用监视" in logtext,
              "无头模式明确跳过了会改状态的监视", "")
        # 提前提示窗口：无头下不建窗，但「进入了提前区间」与「按预期跳过建窗」都要留痕
        check("[prebreak] 即将休息" in logtext,
              "日志记录了进入提前提示区间（onPreNotify 真的被调度器调到）", "")
        check("[prebreak] server 模式无原生窗口" in logtext,
              "无头下提前提示窗按预期跳过建窗（状态与动作照常可用）", "")
        # 提示音：无头下退化成「算出该播哪一首 + 说明不发声」，日志要留痕
        all_play = [l for l in logtext.splitlines() if "[sound] 播放" in l]
        played = all_play[play_mark:]
        check(len(played) >= 3,
              "休息开始 / 结束都真的播了提示音（改配置后至少 3 次）",
              "新增 %d 条（此前 %d 条）" % (len(played), play_mark))
        check(bool(played) and all("music-smoke.wav" in l for l in played),
              "播的就是配置里选中的那一首（单曲循环口径）", played[:2] if played else "")
        check("[sound] server 模式不发声" in logtext,
              "无头模式明确说明「不发声但曲目已算出」", "")
        # 统计：启动与清空都要留痕。**退出那次无条件落盘在 ServiceShutdown 里，
        # 而冒烟是硬杀进程、不走优雅退出**，所以这里不断言它（会假红）。
        check("[stats] 已启动" in logtext,
              "启动日志里有统计启动（含落点路径）", "")
        check("[stats] 已清空全部统计数据" in logtext, "清空统计有留痕", "")
        # 外卖：预览动作要留痕（预览态的 skip 记「推迟（预览，忽略）」之外的「不点了」）
        check("[takeout] 用户选择 不点了" in logtext,
              "外卖动作留痕（预览态点「不点了」）", "")

        # ---- 隔离铁律：真实配置一个字节都不许被改 ----
        # 加载路径在「补 installId / 种入预置条目」时会回写，所以冒烟必须走隔离副本；
        # 这条断言就是防呆：哪天有人把 WR_SETTINGS_PATH 去掉，这里立刻红。
        if real_settings:
            check(_digest(real_settings) == real_digest,
                  "用户真实 settings.json 未被冒烟改写", real_settings)

    finally:
        if args.keep:
            print("（--keep：服务仍在运行，pid=%s port=%d）" % (proc.pid, port))
        elif proc.poll() is None:
            proc.kill()

    # ---- 报告 ----
    tail = open(LOG, encoding="utf-8", errors="replace").read()[-1500:] if os.path.exists(LOG) else ""
    report = (
        "无头绑定冒烟：%d 项，失败 %d 项\n\n" % (checks, len(failures))
        + ("\n".join(failures) if failures else "（全部通过）")
        + "\n\n--- 服务日志尾部 ---\n" + tail
    )
    with open(REPORT, "w", encoding="utf-8") as f:
        f.write(report)
    print("\n结果：%d 项断言，失败 %d 项" % (checks, len(failures)))
    print("报告：%s" % REPORT)
    return 0 if not failures else 1


if __name__ == "__main__":
    sys.exit(main())
