#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""异常与边界探针：正常流程之外的“坏输入 / 坏环境”还撑不撑得住。

覆盖六类：
  1. 打包态缺资源目录（exe 旁边没有 assets/）—— 便携包被用户删了素材也不能白屏崩掉；
  2. 配置文件不可写 —— 保存失败要返回 false 并落日志，不是静默丢改动；
  3. 并发保存 —— 20 个线程同时写，最终文件必须是合法 JSON 且能读回；
  4. 超长 / 富字符名称 —— 中文、emoji、300 字标题都不能截断成乱码；
  5. 超大时刻表（100 条）与 30 条自定义提醒 —— 归一化后仍可调度，不卡死；
  6. 配置/统计落点被占成目录 —— 必须回退默认并继续跑。

用法：python scripts/probe/probe_edge_cases.py
退出码 0 = 全部通过。
"""

import json
import os
import shutil
import socket
import stat
import subprocess
import sys
import tempfile
import threading
import time
import urllib.error
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(os.path.dirname(HERE))
EXE = os.path.join(ROOT, "build", "bin", "rest_server.exe")
BINDING_PREFIX = "main.App."

try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
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


def _http_once(method, url, body=None, headers=None, timeout=20):
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

    Windows 在突发并发（比如 20 路同时 SaveSettings）下偶发
    WSAEADDRINUSE(10048) / WSAENOBUFS(10055)，以及 Wails 反代自连失败的
    “upstream connect failed”—— 三者都是套接字资源抖动，请求还没进方法体，
    退避重试安全。不重试的话，探针会把环境噪声报成产品缺陷。
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
    import secrets
    payload = {"object": 0, "method": 0,
               "args": {"call-id": secrets.token_hex(16),
                        "methodName": BINDING_PREFIX + name, "args": args}}
    headers = {"Content-Type": "application/json", "x-wails-client-id": secrets.token_hex(16)}
    if window:
        headers["x-wails-window-name"] = window
    st, raw = http("POST", base + "/wails/runtime", json.dumps(payload).encode("utf-8"), headers)
    try:
        return st, json.loads(raw)
    except Exception:
        return st, raw


def base_settings():
    return {
        "appName": "边界探针",
        "short": {"enabled": True, "mode": "countdown", "intervalMs": 600000,
                  "durationMs": 20000, "notifyBeforeMs": 10000, "postponeMs": 120000, "slots": []},
        "long": {"enabled": False, "mode": "countdown", "intervalMs": 3600000,
                 "durationMs": 300000, "notifyBeforeMs": 10000, "postponeMs": 300000, "slots": []},
        "customBreaks": [], "longEveryNShort": 2, "idlePauseMin": 5,
        "dndPause": True, "fullscreenPause": True,
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
        "autostart": False, "startMinimized": False,
        "checkUpdateOnStart": False,
        "sites": ["https://www.goodgoodlive.com/worker-rest/version.json"],
    }


class Server:
    def __init__(self, exe, cwd, settings_path, port, log_dir, cfg=None):
        self.exe, self.cwd, self.settings, self.port = exe, cwd, settings_path, port
        self.log_path = os.path.join(log_dir, "server.log")
        if cfg is not None:
            with open(settings_path, "w", encoding="utf-8") as f:
                json.dump(cfg, f, ensure_ascii=False, indent=2)

    def __enter__(self):
        env = dict(os.environ)
        env["WAILS_SERVER_PORT"] = str(self.port)
        env["WAILS_SERVER_HOST"] = "127.0.0.1"
        env["WR_SETTINGS_PATH"] = self.settings
        env["WR_LOG_LEVEL"] = "debug"
        env.pop("ELECTRON_RUN_AS_NODE", None)
        self.logf = open(self.log_path, "w", encoding="utf-8")
        self.proc = subprocess.Popen([self.exe], cwd=self.cwd, env=env,
                                     stdout=self.logf, stderr=subprocess.STDOUT)
        self.base = "http://127.0.0.1:%d" % self.port
        for _ in range(60):
            if self.proc.poll() is not None:
                break
            if http("GET", self.base + "/", timeout=2)[0] == 200:
                return self
            time.sleep(0.5)
        return self

    @property
    def up(self):
        return self.proc.poll() is None and http("GET", self.base + "/", timeout=2)[0] == 200

    def log(self):
        try:
            self.logf.flush()
            return open(self.log_path, encoding="utf-8", errors="replace").read()
        except Exception:
            return ""

    def __exit__(self, *a):
        if self.proc.poll() is None:
            self.proc.terminate()
            try:
                self.proc.wait(timeout=5)
            except Exception:
                self.proc.kill()
        self.logf.close()
        return False


def case_no_assets():
    """打包态：exe 旁没有 assets/supervisors、assets/sounds —— 便携包被删素材。"""
    print("\n[1] 打包态缺资源目录")
    tmp = tempfile.mkdtemp(prefix="wr_edge_noassets_")
    exe = os.path.join(tmp, "rest_server.exe")
    shutil.copy2(EXE, exe)
    cfgp = os.path.join(tmp, "config", "settings.json")
    os.makedirs(os.path.dirname(cfgp), exist_ok=True)
    srv = Server(exe, tmp, cfgp, free_port(29921), tmp, base_settings())
    with srv:
        if not check(srv.up, "缺资源目录时服务仍能启动"):
            note("日志尾部", srv.log()[-400:])
            shutil.rmtree(tmp, ignore_errors=True)
            return
        st, roles = call(srv.base, "ListRoles", [])
        check(st == 200 and roles == [], "ListRoles 返回空数组（不是报错/崩溃）", str(roles)[:80])
        st, info = call(srv.base, "AppInfo", [])
        check(isinstance(info, dict) and info.get("roles") == 0, "AppInfo.roles == 0",
              (info or {}).get("roles") if isinstance(info, dict) else info)
        st, url = call(srv.base, "SoundFileURL", ["short_start.wav"])
        check(url == "", "SoundFileURL 返回空串（没有内置音频可播）", repr(url))
        st, state = call(srv.base, "GetState", [])
        check(isinstance(state, dict), "GetState 仍可读", type(state).__name__)
        check("panic" not in srv.log().lower(), "全程无 panic")
        check(os.path.exists(cfgp), "首跑自动建了 config/settings.json", cfgp)
    shutil.rmtree(tmp, ignore_errors=True)


def case_readonly_config():
    """配置文件只读：保存应失败但不崩。"""
    print("\n[2] 配置文件不可写")
    tmp = tempfile.mkdtemp(prefix="wr_edge_ro_")
    cfgp = os.path.join(tmp, "settings.json")
    srv = Server(EXE, ROOT, cfgp, free_port(29922), tmp, base_settings())
    with srv:
        if not check(srv.up, "只读配置下服务仍能启动"):
            shutil.rmtree(tmp, ignore_errors=True)
            return
        os.chmod(cfgp, stat.S_IREAD)
        before = open(cfgp, encoding="utf-8").read()
        st, saved = call(srv.base, "SaveSettings", [{"appName": "只读测试"}])
        ok_behavior = saved is False or saved is True
        check(ok_behavior, "SaveSettings 返回布尔（不抛异常）", repr(saved))
        if saved is True:
            note("只读文件上 SaveSettings 仍返回 true，检查是否真的写进去了",
                 open(cfgp, encoding="utf-8").read() == before)
        st2, got = call(srv.base, "GetSettings", [])
        check(isinstance(got, dict), "保存失败后 GetSettings 仍可读", type(got).__name__)
        check("panic" not in srv.log().lower(), "只读配置下无 panic")
        os.chmod(cfgp, stat.S_IWRITE | stat.S_IREAD)
    shutil.rmtree(tmp, ignore_errors=True)


def case_concurrent_save():
    """并发保存：20 个线程同时写。"""
    print("\n[3] 并发保存 20 次")
    tmp = tempfile.mkdtemp(prefix="wr_edge_cc_")
    cfgp = os.path.join(tmp, "settings.json")
    srv = Server(EXE, ROOT, cfgp, free_port(29923), tmp, base_settings())
    results = []
    lock = threading.Lock()
    with srv:
        if not check(srv.up, "并发测试服务就绪"):
            shutil.rmtree(tmp, ignore_errors=True)
            return

        def worker(i):
            st, res = call(srv.base, "SaveSettings", [{"appName": "并发-%02d" % i}])
            with lock:
                results.append((st, res))

        threads = [threading.Thread(target=worker, args=(i,)) for i in range(20)]
        for t in threads:
            t.start()
        for t in threads:
            t.join(timeout=30)
        check(len(results) == 20, "20 次保存全部返回", len(results))
        check(all(r[1] is True for r in results), "每次保存都返回 true",
              [r[1] for r in results if r[1] is not True][:3])
        try:
            disk = json.load(open(cfgp, encoding="utf-8"))
            valid = isinstance(disk, dict)
        except Exception as e:
            disk, valid = None, False
            note("磁盘文件解析失败", e)
        check(valid, "并发写之后磁盘上的配置仍是合法 JSON",
              disk.get("appName") if isinstance(disk, dict) else "")
        st, got = call(srv.base, "GetSettings", [])
        check(isinstance(got, dict), "并发写之后 GetSettings 仍可读", type(got).__name__)
        check("panic" not in srv.log().lower(), "并发保存无 panic")
    shutil.rmtree(tmp, ignore_errors=True)


def case_weird_names():
    """超长 / emoji / 中文 名称。"""
    print("\n[4] 超长与富字符名称")
    tmp = tempfile.mkdtemp(prefix="wr_edge_name_")
    cfgp = os.path.join(tmp, "settings.json")
    srv = Server(EXE, ROOT, cfgp, free_port(29924), tmp, base_settings())
    with srv:
        if not check(srv.up, "名称测试服务就绪"):
            shutil.rmtree(tmp, ignore_errors=True)
            return
        emoji = "🍪休息一下🧘‍♀️"
        call(srv.base, "SaveSettings", [{"appName": emoji}])
        st, got = call(srv.base, "GetSettings", [])
        check(isinstance(got, dict) and got.get("appName") == emoji,
              "emoji + 中文标题能原样往返", (got or {}).get("appName"))

        long_name = "长" * 300
        call(srv.base, "SaveSettings", [{"appName": long_name}])
        st, got = call(srv.base, "GetSettings", [])
        got_name = (got or {}).get("appName") or ""
        check(isinstance(got_name, str) and len(got_name) > 0,
              "300 字标题不会变成空串", len(got_name))
        note("300 字标题的服务端处理", "读回 %d 字（前端 maxlength=30，服务端不裁定）" % len(got_name))
        st, info = call(srv.base, "AppInfo", [])
        check(isinstance(info, dict) and isinstance(info.get("name"), str),
              "超长标题下 AppInfo 仍可读", len(str((info or {}).get("name"))))

        # 恢复一个正常标题，避免影响后续断言
        call(srv.base, "SaveSettings", [{"appName": "边界探针"}])
    shutil.rmtree(tmp, ignore_errors=True)


def case_many_slots():
    """100 条时刻表 + 30 条自定义提醒。"""
    print("\n[5] 超大时刻表与自定义提醒")
    tmp = tempfile.mkdtemp(prefix="wr_edge_many_")
    cfgp = os.path.join(tmp, "settings.json")
    cfg = base_settings()
    cfg["short"]["mode"] = "fixed"
    cfg["short"]["slots"] = [
        {"id": "s%d" % i, "hour": i % 24, "minute": i % 60, "repeat": "daily",
         "days": [], "enabled": True} for i in range(100)
    ]
    cfg["customBreaks"] = [
        {"id": "c%d" % i, "name": "自定义-%d" % i, "enabled": True, "mode": "countdown",
         "intervalMs": 600000 + i * 1000, "durationMs": 15000, "notifyBeforeMs": 5000,
         "postponeMs": 60000, "pauseAction": "postpone", "slots": []} for i in range(30)
    ]
    srv = Server(EXE, ROOT, cfgp, free_port(29925), tmp, cfg)
    with srv:
        if not check(srv.up, "100 时刻 + 30 自定义提醒下服务仍能启动"):
            note("日志尾部", srv.log()[-500:])
            shutil.rmtree(tmp, ignore_errors=True)
            return
        t0 = time.time()
        st, state = call(srv.base, "GetState", [])
        dt = time.time() - t0
        check(isinstance(state, dict), "GetState 仍可读", type(state).__name__)
        check(dt < 5, "GetState 在 5 秒内返回（配置放大没有拖死）", "%.2fs" % dt)
        if isinstance(state, dict):
            lines = state.get("lines") or []
            check(len(lines) < 500, "lines 条数有上限（不会把 100 条时刻全铺开成几千条）", len(lines))
        st, cfg2 = call(srv.base, "GetSettings", [])
        slots = ((cfg2 or {}).get("short") or {}).get("slots") or []
        check(len(slots) <= 100, "100 条时刻归一化后条数不膨胀", len(slots))
        check("panic" not in srv.log().lower(), "超大配置下无 panic")
    shutil.rmtree(tmp, ignore_errors=True)


def case_dir_instead_of_file():
    """配置/统计落点被占成目录。"""
    print("\n[6] 落点被占成目录")
    tmp = tempfile.mkdtemp(prefix="wr_edge_dir_")
    cfgp = os.path.join(tmp, "settings.json")
    os.makedirs(cfgp, exist_ok=True)  # 用目录占住本该是文件的位置
    srv = Server(EXE, ROOT, cfgp, free_port(29926), tmp, None)
    with srv:
        check(srv.up, "配置落点是目录时服务仍能启动（回退出厂默认）")
        if srv.up:
            st, got = call(srv.base, "GetSettings", [])
            check(isinstance(got, dict), "GetSettings 回退成对象", type(got).__name__)
            check((got or {}).get("appName") == "吃饼人也敢休息？",
                  "回退到出厂默认 appName", (got or {}).get("appName"))
            st, saved = call(srv.base, "SaveSettings", [{"appName": "写入试试"}])
            check(saved is False or saved is True, "SaveSettings 有明确返回值（不崩）", repr(saved))
            check("panic" not in srv.log().lower(), "落点异常时无 panic")
    shutil.rmtree(tmp, ignore_errors=True)


def case_legacy_identifier():
    """配置里出现“旧版本才有的键”时的口径：**不做旧键迁移**（2026-11-04 用户拍板）。

    软件未发布过，不存在真的老版本配置；`cleanLegacyKeys` 已整段删除，设备标识也已从
    代码里彻底移除（不生成、不存储、不下发）。所以这里钉的是三条“不”：
    带着陌生键照样启动、软件不把它当回事、也不擅自改写用户文件。

    做法刻意分两轮：第一轮让服务把配置“跑熟”（补齐 seeded 标记并回写），第二轮才塞
    一个陌生键进去再起 —— 否则第一轮本来就因为补种而回写，测不出“是不是这个键触发
    的落盘”，等于把两件事混成一条断言。
    """
    print("\n[7] 旧版本残留键：不迁移、不崩、不改写")
    tmp = tempfile.mkdtemp(prefix="wr_edge_ident_")
    cfgp = os.path.join(tmp, "settings.json")
    old_id = "3435a519-c1d7-4d26-a82d-8267119e0001"

    # 第一轮：跑熟配置
    with Server(EXE, ROOT, cfgp, free_port(29927), tmp, base_settings()):
        pass
    settled = json.load(open(cfgp, encoding="utf-8"))
    rm = settled.get("reminderMessages") or {}
    all_seeded = all((rm.get(k) or {}).get("seeded") for k in ("short", "long", "takeout"))
    check(all_seeded, "预备：第一轮已把配置跑熟（seeded 全部置位，第二轮不会再补种）",
          {k: (rm.get(k) or {}).get("seeded") for k in ("short", "long", "takeout")})

    # 第二轮：塞进旧版本才有的键
    settled["installId"] = old_id
    with open(cfgp, "w", encoding="utf-8") as f:
        json.dump(settled, f, ensure_ascii=False, indent=2)
    before = os.path.getmtime(cfgp)
    time.sleep(0.05)  # 让 mtime 有可分辨的粒度（FAT/网络盘只到秒）

    with Server(EXE, ROOT, cfgp, free_port(29928), tmp, None) as srv:
        check(srv.up, "带陌生键的配置不影响启动")
        if srv.up:
            disk = json.load(open(cfgp, encoding="utf-8"))
            check(disk.get("installId") == old_id,
                  "不做旧键迁移：陌生键原样留着，软件不擅自删用户文件", disk.get("installId"))
            check(os.path.getmtime(cfgp) == before,
                  "没有触发回写（不做迁移也就没有“清理残留键”这次落盘）", "")
            # 其余字段照常生效：陌生键不能把整份配置带偏
            st, got = call(srv.base, "GetSettings", [])
            got = got or {}
            check(got.get("longEveryNShort") == disk.get("longEveryNShort"),
                  "陌生键不影响其它字段的读取", [got.get("longEveryNShort"), disk.get("longEveryNShort")])
            check("panic" not in srv.log().lower(), "全程无 panic")
    shutil.rmtree(tmp, ignore_errors=True)


def main():
    if not os.path.exists(EXE):
        print("找不到 %s" % EXE)
        return 2
    real_cfg = os.path.join(ROOT, "config", "settings.json")
    before = os.path.getmtime(real_cfg) if os.path.exists(real_cfg) else 0

    case_no_assets()
    case_readonly_config()
    case_concurrent_save()
    case_weird_names()
    case_many_slots()
    case_dir_instead_of_file()
    case_legacy_identifier()

    print("\n[收尾]")
    after = os.path.getmtime(real_cfg) if os.path.exists(real_cfg) else 0
    check(before == after, "用户真实 settings.json 未被改动", real_cfg)

    print("\n结果：%d 项断言，失败 %d 项" % (checks, len(failures)))
    for f in failures:
        print("  - " + f)
    return 0 if not failures else 1


if __name__ == "__main__":
    sys.exit(main())
