#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""渲染层探针驱动：把真实的 frontend/dist 页面指向无头服务，在浏览器里跑一遍
完整的「shim.js → Go 绑定 → 渲染层」链路，并断言 DOM 与截图。

与 smoke_server.py 的分工：
  - smoke_server.py：直接打 HTTP 端点，验绑定能不能按名解析、数据对不对；
  - 本脚本：把**真实页面**装进浏览器，验前端那层胶水（动态 import 运行时、
    Call.ByName、CSP、事件通道）能不能把数据渲染出来。名字错/装载失败/被 CSP 拦，
    只有这一层看得见。

前置：先构建无头服务（python 会自动构建）
    go build -tags server -o build/bin/chibingren_server.exe .
用法：python scripts/probe_renderer.py [--port 29732] [--keep]
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
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)  # 项目根（原 rewrite-go，现已提升为顶层）
EXE = os.path.join(ROOT, "build", "bin", "chibingren_server.exe")


def role_dir(root, key):
    """在 supervisors/ 下按 key 反查某个督休专员的目录（目录名是专员名称，不固定）。"""
    sup = os.path.join(root, "supervisors")
    if not os.path.isdir(sup):
        return None
    for name in os.listdir(sup):
        p = os.path.join(sup, name, "role.json")
        if not os.path.isfile(p):
            continue
        try:
            with open(p, encoding="utf-8") as f:
                d = json.load(f)
        except Exception:
            continue
        if d.get("key") == key:
            return os.path.join(sup, name)
    return None
PROBE = os.path.join(HERE, "renderer_probe.js")
TEMP = os.environ.get("TEMP", ROOT)
OUT = os.path.join(TEMP, "wr_go_probe")
REPORT = os.path.join(TEMP, "wr_go_probe_report.txt")
LOG = os.path.join(TEMP, "wr_go_probe_server.log")
# 隔离副本：探针也绝不能碰用户的真实配置（加载路径会回写 installId / 预置条目）
ISO = os.path.join(TEMP, "wr_probe_iso")

# 内置督休专员的固定出场顺序（与 历史源码/supervisor.ts 的 BUILTIN_ROLE_META 同源）。
# 画廊磁贴顺序与「顺序轮换」都按它来 —— 写成按目录名字典序就是回归。
BUILTIN_ROLE_ORDER = ",".join((
    "supervisor_default", "role_instructor", "role_executive", "role_mentor",
    "role_doctor", "role_coach", "role_warden", "role_shopkeeper", "role_ai",
))

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


def free_port(preferred):
    for cand in (preferred, 0):
        with socket.socket() as s:
            try:
                s.bind(("127.0.0.1", cand))
                return s.getsockname()[1]
            except OSError:
                continue
    raise SystemExit("没有可用端口")


def wait_up(base, proc, timeout=20.0):
    t0 = time.time()
    while time.time() - t0 < timeout:
        if proc.poll() is not None:
            return False
        try:
            with urllib.request.urlopen(base + "/", timeout=2) as r:
                if r.status == 200:
                    return True
        except Exception:
            pass
        time.sleep(0.4)
    return False


def find_real_settings():
    """与 Go 侧 settingsCandidates 同源：唯一候选 = 运行目录的 config/settings.json
    （不再检查 %APPDATA%）。只用于隔离与指纹对拍，找不到也不影响探针运行。"""
    p = os.path.join(ROOT, "config", "settings.json")
    return p if os.path.exists(p) else None


def digest(path):
    """文件的 SHA-256（不存在返回 None）——用来断言真实配置一个字节都没被改"""
    if not path or not os.path.isfile(path):
        return None
    with open(path, "rb") as f:
        return hashlib.sha256(f.read()).hexdigest()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--port", type=int, default=29732)
    ap.add_argument("--keep", action="store_true")
    args = ap.parse_args()

    # Playwright 驱动系统 Edge：node 走 PATH，playwright 在项目根的 node_modules。
    node_cmd = shutil.which("node")
    if not node_cmd:
        print("找不到 node（渲染层探针改用 Playwright 驱动 Edge，需要 node 环境）")
        return 2
    playwright_mod = os.path.join(ROOT, "node_modules", "playwright")
    if not os.path.isdir(playwright_mod):
        print("找不到 playwright（先在本目录 npm install）")
        return 2

    # 构建无头服务（保证探针跑的是当前源码）
    print("构建无头服务…")
    b = subprocess.run(
        ["go", "build", "-tags", "server", "-o", os.path.join("build", "bin", "chibingren_server.exe"), "-ldflags", "-s -w", "."],
        cwd=ROOT, capture_output=True, text=True,
        encoding="utf-8", errors="replace",
    )
    if b.returncode != 0:
        print("构建失败:\n" + (b.stderr or b.stdout))
        return 2

    port = free_port(args.port)
    base = "http://127.0.0.1:%d" % port
    print("渲染层探针：服务 %s  port=%d" % (EXE, port))

    # ---- 配置隔离（与 smoke_server.py 同一套理由）：拷一份真实配置到临时目录，
    # 用 WR_SETTINGS_PATH 独占指向它；对齐基准也改读这一份。真实配置绝不被改动。
    real_settings = find_real_settings()
    real_digest = digest(real_settings)
    shutil.rmtree(ISO, ignore_errors=True)
    os.makedirs(ISO, exist_ok=True)
    iso_settings = os.path.join(ISO, "settings.json")
    if real_settings:
        shutil.copyfile(real_settings, iso_settings)
    # 提前提示窗需要一个**真的处于提前提示区间**的服务：把短休息改成「10 分钟一次、
    # 提前 10 分钟提示」，`nextAt - notifyBeforeMs <= now` 立刻成立 → 启动后第一跳就进 pre。
    # 改的是隔离副本（真实配置在末尾仍有 SHA-256 断言守着）。
    if os.path.exists(iso_settings):
        cfg_seed = json.load(open(iso_settings, encoding="utf-8"))
        cfg_seed.setdefault("short", {}).update({
            "enabled": True, "mode": "countdown",
            "intervalMs": 600000, "notifyBeforeMs": 600000,
        })
        cfg_seed["long"] = dict(cfg_seed.get("long") or {}, enabled=False)
        cfg_seed["takeout"] = dict(cfg_seed.get("takeout") or {}, enabled=False)
        with open(iso_settings, "w", encoding="utf-8") as f:
            json.dump(cfg_seed, f, ensure_ascii=False, indent=2)

    env = dict(os.environ)
    env["WAILS_SERVER_PORT"] = str(port)
    env["WAILS_SERVER_HOST"] = "127.0.0.1"
    env["WR_SETTINGS_PATH"] = iso_settings
    env.pop("ELECTRON_RUN_AS_NODE", None)
    logf = open(LOG, "w", encoding="utf-8")
    srv = subprocess.Popen([EXE], cwd=ROOT, env=env, stdout=logf, stderr=subprocess.STDOUT)

    try:
        if not check(wait_up(base, srv), "无头服务就绪", "rc=%s" % srv.poll()):
            print(open(LOG, encoding="utf-8", errors="replace").read()[-2000:])
            return 1

        # ---- 跑渲染探针（node + Playwright 驱动系统 Edge）----
        penv = dict(os.environ)
        penv["WR_BASE"] = base
        penv["WR_OUT"] = OUT
        # Playwright 从 node_modules 解析（cwd=ROOT 时 node 自动向上找）
        r = subprocess.run(
            [node_cmd, PROBE],
            cwd=ROOT, env=penv, capture_output=True, text=True, timeout=180,
            encoding="utf-8", errors="replace",
        )
        if not check(r.returncode == 0, "渲染探针进程正常结束", "rc=%s %s" % (r.returncode, (r.stderr or "")[-400:].replace("\n", " "))):
            print("--- 探针 stdout ---\n" + (r.stdout or "")[-2000:])
            print("--- 探针 stderr ---\n" + (r.stderr or "")[-2000:])
            return 1

        mpath = os.path.join(OUT, "metrics.json")
        if not check(os.path.isfile(mpath), "探针产出 metrics.json", mpath):
            return 1
        m = json.load(open(mpath, encoding="utf-8"))

        # 对齐基准用**隔离副本**（服务实际读的那一份）：服务可能已在加载时归一化并回写副本
        cfg = json.load(open(iso_settings, encoding="utf-8")) if os.path.exists(iso_settings) else {}
        want_color = (cfg.get("appearance") or {}).get("color")
        want_dur = (cfg.get("short") or {}).get("durationMs")
        rm = ((cfg.get("reminderMessages") or {}).get("short") or {})
        want_msg = ""
        for it in (rm.get("items") or []):
            if it.get("id") == rm.get("fixedId"):
                want_msg = it.get("text") or ""
        fixed_key = (cfg.get("supervisor") or {}).get("fixedKey")
        want_role = ""
        if fixed_key:
            rp = os.path.join(role_dir(ROOT, fixed_key) or "", "role.json")
            if os.path.isfile(rp):
                want_role = json.load(open(rp, encoding="utf-8")).get("name") or ""
        print("   （对齐基准：settings=%s role=%s）" % (iso_settings if cfg else "未找到", fixed_key or "-"))

        # ---- 设置页（真正的主界面）----
        # 这一节的分量最重：设置页没有自己的默认值，拿到 getSettings() 就直接填表，
        # 少一个字段 / 某个绑定没接上 → fillForm() 抛错 → 整页停摆。所以既断言
        # 「页面无错」，也断言「关键值与配置文件逐项对齐」。
        stg = m.get("settings") or {}
        check(stg.get("shim") == "go", "设置页跑在 Go 后端上（window.__WR_SHIM=go）", stg.get("shim"))
        check(stg.get("win") == "settings", "设置页窗口标识已注入（__WR_WIN=settings）", stg.get("win"))
        check(not (stg.get("consoleErrors") or []), "设置页加载期间无控制台错误",
              (stg.get("consoleErrors") or [])[:2])
        check(not (stg.get("errs") or []), "设置页无未捕获 JS 异常", (stg.get("errs") or [])[:2])
        # 主题：删除了「跟随系统」选项，默认浅色
        theme_opts = stg.get("themeOptions") or []
        check("system" not in theme_opts, "主题选项不含「跟随系统」", theme_opts)
        check(stg.get("themeChecked") == "light", "主题默认选中「浅色」", stg.get("themeChecked"))
        check(stg.get("themeSet"), "设置页已应用主题（data-theme 已写入）", stg.get("themeSet"))
        check((stg.get("navCount") or 0) >= 6, "侧栏页签渲染出来（>=6 个）", stg.get("navCount"))
        check((stg.get("tabCount") or 0) >= 6, "各页签内容区都在 DOM 里", stg.get("tabCount"))
        check((stg.get("supGallery") or 0) == 9, "督休专员画廊渲染出 9 个督休专员（ListRoles → DOM）",
              stg.get("supGallery"))
        check((stg.get("supGalleryAdd") or 0) == 1, "画廊末尾恰好 1 个「新建督休专员」磁贴",
              stg.get("supGalleryAdd"))
        check(str(stg.get("supGalleryNames") or "") == BUILTIN_ROLE_ORDER,
              "画廊顺序 == BUILTIN_ROLE_META 的内置顺序（顺序轮换的出场次序）",
              stg.get("supGalleryNames"))
        check((stg.get("supMsgItems") or 0) >= 1,
              "督休专员台词面板渲染出条目（ListRoles 真的下发了 messages）",
              "items=%s empty=%s" % (stg.get("supMsgItems"), stg.get("supMsgEmpty")))
        check(stg.get("supMsgRole") == want_role, "台词面板标题 == 当前督休专员名",
              "%s vs %s" % (stg.get("supMsgRole"), want_role))
        check("Wails" in str(stg.get("dVersion") or ""), "版本卡显示 Go 引擎（不再含旧版标识）",
              stg.get("dVersion"))

        # 关键值 vs 配置文件（类型对齐：输入框是字符串，配置里可能是数字）
        def same_num(got, want):
            try:
                return abs(float(got) - float(want)) < 1e-6
            except Exception:
                return False

        check(stg.get("appName") == (cfg.get("appName") or None), "软件名输入框 == 配置 appName",
              "%r vs %r" % (stg.get("appName"), cfg.get("appName")))
        if isinstance(cfg.get("short"), dict):
            check(stg.get("shortMode") == cfg["short"].get("mode"), "短休息触发方式 == 配置 short.mode",
                  "%s vs %s" % (stg.get("shortMode"), cfg["short"].get("mode")))
            check(same_num(stg.get("shortDuration"), (cfg["short"].get("durationMs") or 0) / 1000.0),
                  "短休息时长（秒）== 配置 short.durationMs/1000",
                  "%s vs %s" % (stg.get("shortDuration"), (cfg["short"].get("durationMs") or 0) / 1000.0))
            check(same_num(stg.get("shortInterval"), (cfg["short"].get("intervalMs") or 0) / 60000.0),
                  "短休息间隔（分钟）== 配置 short.intervalMs/60000",
                  "%s vs %s" % (stg.get("shortInterval"), (cfg["short"].get("intervalMs") or 0) / 60000.0))
        check(same_num(stg.get("idlePauseMin"), cfg.get("idlePauseMin")),
              "空闲暂停分钟数 == 配置 idlePauseMin",
              "%s vs %s" % (stg.get("idlePauseMin"), cfg.get("idlePauseMin")))
        check(stg.get("autostart") == bool(cfg.get("autostart")),
              "开机自启动勾选态 == 配置 autostart",
              "%s vs %s" % (stg.get("autostart"), cfg.get("autostart")))
        check(stg.get("dndPause") == bool(cfg.get("dndPause")),
              "勿扰暂停勾选态 == 配置 dndPause",
              "%s vs %s" % (stg.get("dndPause"), cfg.get("dndPause")))
        check(stg.get("supSelection") == (cfg.get("supervisor") or {}).get("selection"),
              "督休专员选择方式 == 配置 supervisor.selection",
              "%s vs %s" % (stg.get("supSelection"), (cfg.get("supervisor") or {}).get("selection")))
        check(same_num(stg.get("apWidthPct"), (cfg.get("appearance") or {}).get("widthPct")),
              "提醒窗口宽度百分比 == 配置 appearance.widthPct",
              "%s vs %s" % (stg.get("apWidthPct"), (cfg.get("appearance") or {}).get("widthPct")))
        check(same_num(stg.get("tkLead"), (cfg.get("takeout") or {}).get("leadSec")),
              "外卖提前通知秒数 == 配置 takeout.leadSec",
              "%s vs %s" % (stg.get("tkLead"), (cfg.get("takeout") or {}).get("leadSec")))

        # ---- 页签切换：切到每一页都要真的显示出来（切换时抛错会让内容空白）----
        tabs = m.get("tabs") or {}
        for key, label in (("timing", "时间规划"), ("supervisor", "督休专员"), ("stats", "统计报表"),
                           ("shortcuts", "快捷键"), ("about", "关于")):
            t = tabs.get(key) or {}
            check(t.get("active") == "tab-" + key and t.get("visible") is True,
                  "切到「%s」页签后内容可见" % label,
                  "active=%r visible=%s" % (t.get("active"), t.get("visible")))
        check(not (stg.get("consoleErrorsAfterTabs") or []), "切换页签期间无控制台错误",
              (stg.get("consoleErrorsAfterTabs") or [])[:2])

        # 声音试听：URL 算对还不够，要浏览器真能解码它 —— 这一条把 /sounds/ 资源服务
        # 与渲染层的 <audio> 串起来（真发声由 probe_gui_boot.py 覆盖）。
        sp = m.get("soundPreview") or {}
        check(sp.get("url") == "/sounds/short_start.wav",
              "试听 URL 指向资源服务的内置提示音", sp.get("url"))
        check(sp.get("ok") is True and float(sp.get("duration") or 0) > 0,
              "浏览器能解码内置提示音（loadedmetadata 拿到时长）",
              "ok=%s duration=%s error=%s" % (sp.get("ok"), sp.get("duration"), sp.get("error")))

        # ---- 统计页：报表图真的画出来了（数据来自 Go 侧的 stats.go）----
        # 空表也能画坐标轴，所以判据是「图形节点数够多」而不是「有 svg 就行」。
        stt = m.get("stats") or {}
        check(stt.get("svg") is True, "统计页报表图 SVG 已画出", stt.get("svg"))
        check((stt.get("marks") or 0) >= 6, "报表图里有真实图形节点（不是空白容器）", stt.get("marks"))
        check(str(stt.get("date") or "").startswith("（") and str(stt.get("date") or "").endswith("）"),
              "统计页日期取自 Go 的 today.date", stt.get("date"))
        check((stt.get("cards") or 0) == 4, "统计页共 4 张卡（今天 / 休息报表 / 外卖报表 / 自定义）", stt.get("cards"))
        check(str(stt.get("firstTitle") or "").startswith("今天"),
              "统计页首张卡标题是「今天」", stt.get("firstTitle"))
        comp = stt.get("comp") or {}
        check(comp.get("panels") == 3, "今天卡渲染出 3 块面板", comp.get("panels"))
        check(comp.get("segs") == 3, "时间构成画出 3 段堆叠条", comp.get("segs"))
        check(comp.get("bars") == 7, "休息 4 行 + 久坐 3 行，共 7 行条形", comp.get("bars"))
        # 今天卡不再挂任何「?」说明（时间构成 / 休息 / 久坐 都不带）
        check(comp.get("qmarks") == 0, "今天卡没有说明图标", comp.get("qmarks"))
        check("软件运行" in str(comp.get("total") or ""),
              "时间构成右上角写「软件运行 …」", comp.get("total"))
        labels = comp.get("barLabels") or []
        check(labels[:4] == ["完成", "推迟", "跳过", "提前结束"],
              "休息四行 = 完成 / 推迟 / 跳过 / 提前结束", labels[:4])
        check(labels[4:] == ["最长", "平均", "中位数"],
              "久坐三行 = 最长 / 平均 / 中位数", labels[4:])
        early_val = str((comp.get("barVals") or [""])[3] or "")
        check(early_val.endswith("次"), "提前结束显示的是次数", early_val)

        # 四项的数据源 = 后端 today 的四个计数，且「完成」只取短 + 长休息：
        # 自定义提醒（customCount）绝不能混进完成（这条等式就是防它溜进去的哨兵）。
        bk = stt.get("backend") or {}
        check(not bk.get("err"), "统计页能读回后端 today 快照", bk.get("err"))
        vals = comp.get("barVals") or []
        want = [
            ("完成", int(bk.get("shortCount") or 0) + int(bk.get("longCount") or 0)),
            ("推迟", int(bk.get("postponeCount") or 0)),
            ("跳过", int(bk.get("skipCount") or 0)),
            ("提前结束", int(bk.get("endEarlyCount") or 0)),
        ]
        for i, (name, n) in enumerate(want):
            got = str(vals[i] if i < len(vals) else "")
            check(got == "%d 次" % n,
                  "「%s」= 后端 %s 计数（自定义提醒 %d 次不计入）" %
                  (name, name, int(bk.get("customCount") or 0)),
                  "%s vs %d" % (got, n))
        # 久坐的值是时长（不是次数）：末尾一定是「秒」或「分」
        sed_vals = (comp.get("barVals") or [])[4:]
        check(len(sed_vals) == 3 and all(v.endswith(("秒", "分")) for v in sed_vals),
              "久坐三行显示的是时长", sed_vals)
        check("分钟" in str(comp.get("sedNote") or ""),
              "久坐面板有一句小结（提到 45 分钟基准）", comp.get("sedNote"))
        check(not (stt.get("errs") or []), "统计页无未捕获 JS 异常", (stt.get("errs") or [])[:2])

        # 休息报表：表格最后一列是「提前结束次数」（紧跟在「推迟次数」之后），
        # 图例也多出「提前结束次数」一条折线。
        heads = [str(h) for h in (stt.get("rcHeads") or [])]
        check(len(heads) == 10, "休息报表表格 10 列（新增提前结束）", heads)
        check(heads[-2].startswith("推迟次数") and heads[-1].startswith("提前结束次数"),
              "「提前结束次数」列排在「推迟次数」之后", heads[-2:])
        legend = [str(x) for x in (stt.get("restLegend") or [])]
        check(len(legend) == 6, "休息报表图例 6 项（2 根柱 + 4 条折线，新增提前结束）", legend)
        check(legend and legend[-1] == "提前结束次数",
              "图例最后一项是「提前结束次数」", legend[-1:] if legend else [])

        # ---- 主窗口（调试控制台） ----
        idx = m.get("index") or {}
        check(idx.get("shim") == "go", "页面确认跑在 Go 后端上（window.__WR_SHIM=go）", idx.get("shim"))
        check(idx.get("roles") == 9, "主窗口渲染出 9 个督休专员磁贴（ListRoles → DOM）", idx.get("roles"))
        check(idx.get("rolesLoaded") == idx.get("roles") and idx.get("roles"),
              "全部督休专员预览图以 data URL 成功装载", "%s/%s" % (idx.get("rolesLoaded"), idx.get("roles")))
        check((idx.get("optCount") or 0) == (idx.get("roles") or 0) + 1, "督休专员下拉选项 = 督休专员数 + 1 个默认项", idx.get("optCount"))
        envtxt = " ".join(idx.get("env") or [])
        check("go+wails3" in envtxt, "环境卡片显示引擎 go+wails3", (idx.get("env") or [])[:2])
        check("roles" in envtxt or "督休专员" in envtxt, "环境卡片显示督休专员目录", envtxt[:80])

        # ---- 会话 ----
        sid = m.get("sessionId") or ""
        check(isinstance(sid, str) and sid.startswith("short-"), "前端触发的会话 id 正常", sid)

        # ---- 提醒窗口（横屏 / 竖屏）----
        for key, label in (("breakWide", "横屏"), ("breakTall", "竖屏")):
            b = m.get(key) or {}
            check(b.get("shim") == "go", "%s提醒窗口：shim 生效" % label, b.get("shim"))
            check(b.get("hasSupervisor") is True, "%s提醒窗口：进入 has-supervisor 版式" % label, b.get("hasSupervisor"))
            check(b.get("figureHidden") is False, "%s提醒窗口：立绘已显示" % label, b.get("figureHidden"))
            check(str(b.get("figureSrcPrefix", "")).startswith("data:image/png;base64,"),
                  "%s提醒窗口：立绘来自 Go 的 data URL" % label, b.get("figureSrcPrefix"))
            nat = str(b.get("figureNatural") or "0x0")
            try:
                nw, nh = (int(x) for x in nat.split("x"))
            except Exception:
                nw, nh = 0, 0
            check(nw > 100 and nh > 100, "%s提醒窗口：立绘真的解码成功（浏览器解出像素尺寸）" % label, nat)
            check(b.get("lineVisible") is True, "%s提醒窗口：台词气泡可见" % label, b.get("lineVisible"))
            if want_role:
                check(b.get("speaker") == want_role, "%s提醒窗口：说话人 == 配置固定督休专员的名字" % label,
                      "%s vs %s" % (b.get("speaker"), want_role))
            if want_msg:
                check(b.get("lineText") == want_msg, "%s提醒窗口：气泡文字 == 配置里的提醒内容" % label,
                      "%r vs %r" % (b.get("lineText"), want_msg))
            cd = b.get("countdown")
            ok_cd = False
            try:
                ok_cd = 0 < int(cd) <= (want_dur // 1000 if want_dur else 60)
            except Exception:
                ok_cd = False
            check(ok_cd, "%s提醒窗口：倒计时是秒数且在时长范围内" % label, cd)
            if want_color:
                check(str(b.get("accent", "")).lower() == want_color.lower(),
                      "%s提醒窗口：--accent == 配置外观色" % label, "%s vs %s" % (b.get("accent"), want_color))
            check(b.get("badgeHidden") is True, "%s提醒窗口：真实休息不带预览角标（preview=false）" % label, b.get("badgeHidden"))

            # ---- 督休专员版式：游戏对话画面（底部全宽对话框），是 supervisorLayout 的核心 ----
            # 同一个会话，只换窗口尺寸（横 1100x720 / 竖 760x980），两档都应进入对话框
            # 版式：supMetrics.mode == "dialog"、名字标签亮出、倒计时环中心落在
            # 「立绘右缘~窗口右缘」「对话框顶~窗口顶」两个中点的交点上、
            # 「提前结束」在环下方与环中心对齐、对话框横贯全宽。
            sm = b.get("supMetrics") or {}
            check(sm.get("mode") == "dialog",
                  "%s提醒窗口：版式是游戏对话画面（supMetrics.mode == dialog）" % label, sm.get("mode"))
            check(isinstance(sm.get("figLeft"), int) and isinstance(sm.get("figRight"), int),
                  "%s提醒窗口：supMetrics 记录了立绘去透明后的左右缘" % label,
                  "%r / %r" % (sm.get("figLeft"), sm.get("figRight")))
            # 独立对拍（2026-10-14）：figLeft/figRight/figTop 必须与「图片自然像素
            # alpha bbox + .trim 计算变换矩阵」独立反推的可见边一致。这条断言不读
            # 页面的摆位公式，专门抓「trim 补偿几何算错」这类 bug——曾经的错法：
            # 顶缘漏减底部透明边距 bottom、左右缘直接取 .trim 变换后外框（含缩放后的
            # 透明边距），锚点整体偏高/偏外，用户换立绘/改尺寸后必错位。
            fv = b.get("figureVisible") or {}
            if fv and all(isinstance(sm.get(k), int) for k in ("figLeft", "figRight", "figTop")):
                # figureVisible（独立反推）的键名是 left/right/top，与 supMetrics 的
                # figLeft/figRight/figTop **不同名**——必须映射着比。曾按同名直接
                # fv.get("figLeft") 取到 None，int - None 把断言段整个崩掉（TypeError），
                # 报告都没写出来。两侧都验一下是 int 再比。
                for _sk, _fk, _n in (("figLeft", "left", "左缘"),
                                     ("figRight", "right", "右缘"),
                                     ("figTop", "top", "上缘")):
                    if not isinstance(fv.get(_fk), int):
                        check(False, "%s提醒窗口：独立反推缺少可见%s（键 %s）" % (label, _n, _fk),
                              "keys=%s" % sorted(fv.keys()))
                        continue
                    check(abs(sm.get(_sk) - fv.get(_fk)) <= 2,
                          "%s提醒窗口：supMetrics.%s == 独立反推的立绘去透明可见%s" % (label, _sk, _n),
                          "%s vs %s（alpha=%s figBox=%s）" % (sm.get(_sk), fv.get(_fk), fv.get("alpha"), fv.get("figBox")))
            check(isinstance(sm.get("cdX"), int) and sm.get("cdX", 0) > 0,
                  "%s提醒窗口：倒计时环的水平锚点已算出（cdX）" % label, sm.get("cdX"))
            if want_role:
                check(b.get("dialogName") == want_role and b.get("dialogNameShown") is True,
                      "%s提醒窗口：名字标签亮在对话框左上角" % label,
                      "%r / %s" % (b.get("dialogName"), b.get("dialogNameShown")))
            ci = b.get("contentInline") or {}
            check((ci.get("rectW") or 0) > 0 and (ci.get("rectH") or 0) > 0,
                  "%s提醒窗口：对话框有实际占位" % label, "%sx%s" % (ci.get("rectW"), ci.get("rectH")))
            vpw = b.get("viewportW") or 0
            vph = b.get("viewportH") or 0
            if vpw:
                # 对话框与窗口重叠（贴边）：左右贴死窗口两侧且对称、底部贴死窗底
                # （rectLeft 可能为 0，不能用 or 兜底——0 是 falsy，会被误判）
                rl = ci.get("rectLeft")
                rr = ci.get("rectRight")
                check(rl is not None and rl <= 1 and rr is not None and rr >= vpw - 1,
                      "%s提醒窗口：对话框左右与窗口重叠（贴边）" % label,
                      "left=%s right=%s vpw=%s" % (rl, rr, vpw))
                if rl is not None:
                    check(abs((ci.get("rectW") or 0) - (vpw - 2 * rl)) <= 2,
                          "%s提醒窗口：对话框左右对称（宽度 == 视口宽 - 2*左距）" % label,
                          "w=%s vpw=%s left=%s" % (ci.get("rectW"), vpw, rl))
                check((ci.get("rectBottom") or 0) >= vph - 1,
                      "%s提醒窗口：对话框底部与窗口重叠（贴底）" % label,
                      "bottom=%s vph=%s" % (ci.get("rectBottom"), vph))
                # 对话框是 fixed、不受 #app 的 overflow 裁剪：贴边后底部两角必须
                # 自己圆出与窗口一致的圆角（全屏时双方都是 0px 直角，同样相等）
                check(ci.get("blRadius") == b.get("appBlRadius") and ci.get("brRadius") == b.get("appBrRadius"),
                      "%s提醒窗口：对话框底部两角圆角 == 窗口圆角" % label,
                      "content=%s/%s app=%s/%s" % (ci.get("blRadius"), ci.get("brRadius"),
                                                   b.get("appBlRadius"), b.get("appBrRadius")))
            bi = b.get("barInline") or {}
            check(bool(bi.get("left")) and bool(bi.get("bottom")),
                  "%s提醒窗口：倒计时环被 inline 摆位（left/bottom 已写入）" % label,
                  "%r / %r" % (bi.get("left"), bi.get("bottom")))
            ri = b.get("ringInline") or {}
            if ri.get("rectLeft") is not None and sm.get("midX") is not None and ci.get("rectBottom") is not None:
                cx = (ri.get("rectLeft", 0) + ri.get("rectRight", 0)) / 2
                cy = (ri.get("rectTop", 0) + ri.get("rectBottom", 0)) / 2
                want_cx = sm.get("midX")
                # 框顶 y（距窗顶）= 框底边 y - 框高（实测）。
                # 环中心垂直锚点（2026-10-13 新规格）=「立绘可见上缘(figTop, 去透明) 与 框顶」的中点，
                # 不再是「窗口顶与框顶」的中点；二阶段立绘缩放不改变锚点（layoutSupDialog 冻结）。
                box_top = (ci.get("rectBottom") or 0) - (ci.get("rectH") or 0)
                # 容差 14px：环放大后（2026-10-13 督休环扩到 32vh/30vw）在窄/竖窗口里
                # 精确居中到中点会让环缘贴到窗口边，触发 layoutSupDialog 的边缘保护
                # clamp（CD_EDGE=10px），环心向窗内合法内移数像素——视觉仍在正确区域。
                # 真实摆位回归（公式错位）会偏几十像素，14px 容差仍能抓住。
                check(abs(cx - want_cx) <= 14,
                      "%s提醒窗口：环中心 == 立绘右缘与窗口右缘的中间" % label,
                      "%s vs %s" % (round(cx), want_cx))
                if sm.get("figTop") is not None:
                    want_cy = (sm.get("figTop") + box_top) / 2
                    check(abs(cy - want_cy) <= 14,
                          "%s提醒窗口：环中心 == 立绘上缘与对话框顶的中间" % label,
                          "%s vs %s" % (round(cy), round(want_cy)))
            sk = b.get("skipInline") or {}
            if sk.get("shown") and ri.get("rectBottom") is not None:
                check(sk.get("rectTop", 0) >= ri.get("rectBottom", 0) - 2,
                      "%s提醒窗口：「提前结束」在倒计时环的下方" % label,
                      "skip_top=%s ring_bottom=%s" % (sk.get("rectTop"), ri.get("rectBottom")))
                skip_cx = (sk.get("rectLeft", 0) + sk.get("rectRight", 0)) / 2
                ring_cx = (ri.get("rectLeft", 0) + ri.get("rectRight", 0)) / 2
                check(abs(skip_cx - ring_cx) <= 3,
                      "%s提醒窗口：「提前结束」与环中心水平对齐" % label,
                      "%s vs %s" % (round(skip_cx), round(ring_cx)))
            if bi.get("rectBottom") is not None and ci.get("rectBottom") is not None:
                check(bi.get("rectBottom") <= ci.get("rectBottom"),
                      "%s提醒窗口：倒计时环挂在对话框上方（不压台词）" % label,
                      "%s vs %s" % (bi.get("rectBottom"), ci.get("rectBottom")))
            check(not (b.get("errors") or []), "%s提醒窗口：页面无 JS 错误" % label, b.get("errors"))

        # ---- 提前提示窗（右下角小窗）----
        # 页面本身是渲染层产物，标题 / 倒计时 / 版式全部来自 Go 的 PreBreakState；
        # 这条链路（按名调用 + 500ms 轮询 + URL 带版式）只有真跑一遍才看得见。
        pb = m.get("preBreak") or {}
        check(pb.get("shim") == "go", "提前提示窗：shim 生效", pb.get("shim"))
        check(pb.get("title") in ("您已连续工作较长时间，建议您短休息",
                                    "您已连续工作较长时间，建议您长休息"),
              "提前提示窗：标题是内置劝说文案（按长短休息区分，来自 Go 的 PreBreakState）", pb.get("title"))
        ok_sec = False
        try:
            ok_sec = 0 < int(pb.get("seconds")) <= 600
        except Exception:
            ok_sec = False
        check(ok_sec, "提前提示窗：倒计时是秒数（来自剩余毫秒）", pb.get("seconds"))
        check(pb.get("post") == " 秒进入休息", "提前提示窗：内置倒计时后缀", repr(pb.get("post")))
        check(pb.get("tipsShown") is True, "提前提示窗：内置版式显示两行说明", pb.get("tipsShown"))
        check(pb.get("pauseShown") is True, "提前提示窗：内置版式带「暂停计时」", pb.get("pauseShown"))
        check(pb.get("postponeShown") is True,
              "提前提示窗：默认配置（可推迟>0）显示「推迟」按钮", pb.get("postponeShown"))
        check(pb.get("buttons") == ["立即休息", "推迟休息", "跳过休息", "暂停计时"],
              "提前提示窗：四个按钮文案（内置版式）", pb.get("buttons"))
        check(not (pb.get("consoleErrors") or []), "提前提示窗：页面无控制台错误",
              (pb.get("consoleErrors") or [])[:2])
        check((pb.get("appHeight") or 0) >= 200, "提前提示窗：240 高窗口里内容铺得开", pb.get("appHeight"))

        pbc = m.get("preBreakCustom") or {}
        check(pbc.get("shim") == "go", "自定义提醒版式：shim 生效", pbc.get("shim"))
        check(pbc.get("title") == "喝水提醒时间到",
              "自定义提醒版式：标题是「XX时间到」（来自 Go 的 PreBreakState）", pbc.get("title"))
        check(pbc.get("post") == " 秒显示喝水提醒",
              "自定义提醒版式：倒计时后缀带提醒名（来自 URL 的 tail）", repr(pbc.get("post")))
        check(pbc.get("tipsShown") is False, "自定义提醒版式：不显示两行说明", pbc.get("tipsShown"))
        check(pbc.get("pauseShown") is False, "自定义提醒版式：没有「暂停计时」", pbc.get("pauseShown"))
        check(pbc.get("postponeShown") is True,
              "自定义提醒版式：默认配置（可推迟>0）显示「推迟」按钮", pbc.get("postponeShown"))
        # 「暂停计时」是被加 .hidden 藏起来的（元素还在 DOM 里），所以这里仍是 4 项；
        # 它到底露没露由上面的 pauseShown 断言。
        check(pbc.get("buttons") == ["立即提醒", "推迟提醒", "跳过提醒", "暂停计时"],
              "自定义提醒版式：三颗按钮都改说「提醒」", pbc.get("buttons"))
        check(not (pbc.get("consoleErrors") or []), "自定义提醒版式：页面无控制台错误",
              (pbc.get("consoleErrors") or [])[:2])
        check((pbc.get("appHeight") or 0) >= 140, "自定义提醒版式：172 高窗口里内容铺得开",
              pbc.get("appHeight"))

        # ---- 外卖提醒窗口（takeout.html）----
        # 会话来自 Go 的 TakeoutCurrent（预览触发 → 会话 → 渲染），固定时刻、三个按钮、红包区。
        tk = m.get("takeout") or {}
        check(tk.get("shim") == "go", "外卖窗口：shim 生效", tk.get("shim"))
        check(tk.get("callErr") == "", "外卖窗口：previewTakeout → takeoutCurrent 无调用错误", tk.get("callErr"))
        info = tk.get("info") or {}
        check(isinstance(info, dict) and bool(info.get("meal")),
              "外卖窗口：拿到的会话带餐段文案", info.get("meal"))
        check(info.get("meal") in ("早餐", "午餐", "下午茶", "晚餐", "夜宵"),
              "外卖窗口：餐段在合法枚举内", info.get("meal"))
        check(tk.get("orderedBtn") == "已点好" and tk.get("skipBtn") == "不点了",
              "外卖窗口：三个按钮文案（已点好 / 不点了）",
              "%s / %s" % (tk.get("orderedBtn"), tk.get("skipBtn")))
        check(str(tk.get("postponeBtn") or "").startswith("推迟"),
              "外卖窗口：「推迟」按钮带时长文案", tk.get("postponeBtn"))
        check(tk.get("postponeShown") is True,
              "外卖窗口：默认配置（可推迟>0）显示「推迟」按钮", tk.get("postponeShown"))
        # 时钟是正向走的（固定时刻无倒计时），格式 HH:MM:SS
        ok_clock = False
        try:
            import re as _re
            ok_clock = bool(_re.match(r"^\d{2}:\d{2}:\d{2}$", str(tk.get("clock") or "")))
        except Exception:
            ok_clock = False
        check(ok_clock, "外卖窗口：正向时钟是 HH:MM:SS（不做倒计时）", tk.get("clock"))
        if want_color:
            check(str(tk.get("accent") or "").lower() == want_color.lower(),
                  "外卖窗口：--accent == 配置外观色", "%s vs %s" % (tk.get("accent"), want_color))
        # 有督休专员时：外卖窗口同样进入游戏对话画面版式；督休专员没有二阶段、
        # 台词恒为外卖提醒文字；红包中心落在两个锚点中点的交点上
        tksm = tk.get("supMetrics") or {}
        if tk.get("hasSupervisor"):
            check(tksm.get("mode") == "dialog",
                  "外卖窗口：版式是游戏对话画面（supMetrics.mode == dialog）", tksm.get("mode"))
            # 独立对拍（2026-10-14，与提醒窗口同一套）：红包锚点用的 figLeft/figRight/
            # figTop 必须与独立反推的立绘去透明可见边一致（像素 alpha bbox + .trim 矩阵），
            # 曾经的错法（顶缘漏减 bottom、左右缘取含透明边距的变换外框）会让红包整体偏高。
            tkfv = tk.get("figureVisible") or {}
            if tkfv and all(isinstance(tksm.get(k), int) for k in ("figLeft", "figRight", "figTop")):
                # 键名映射同提醒窗口段：独立反推是 left/right/top，supMetrics 是
                # figLeft/figRight/figTop；两侧都验 int 再比（曾因同名误取崩过断言段）。
                for _sk, _fk, _n in (("figLeft", "left", "左缘"),
                                     ("figRight", "right", "右缘"),
                                     ("figTop", "top", "上缘")):
                    if not isinstance(tkfv.get(_fk), int):
                        check(False, "外卖窗口：独立反推缺少可见%s（键 %s）" % (_n, _fk),
                              "keys=%s" % sorted(tkfv.keys()))
                        continue
                    check(abs(tksm.get(_sk) - tkfv.get(_fk)) <= 2,
                          "外卖窗口：supMetrics.%s == 独立反推的立绘去透明可见%s" % (_sk, _n),
                          "%s vs %s（alpha=%s figBox=%s）" % (tksm.get(_sk), tkfv.get(_fk), tkfv.get("alpha"), tkfv.get("figBox")))
            check(tk.get("stage") == "1",
                  "外卖窗口：督休专员没有二阶段（data-stage 恒为 1）", tk.get("stage"))
            if info.get("message"):
                check(tk.get("lineText") == info.get("message") and tk.get("lineShown") is True,
                      "外卖窗口：台词 == 外卖提醒文字（不显示督休专员自己的台词）",
                      "%r vs %r" % (tk.get("lineText"), info.get("message")))
            if want_role:
                check(tk.get("dialogName") == want_role and tk.get("dialogNameShown") is True,
                      "外卖窗口：名字标签亮在对话框左上角",
                      "%r / %s" % (tk.get("dialogName"), tk.get("dialogNameShown")))
            rp = tk.get("redpacketInline") or {}
            if tk.get("contentRect"):
                # 对话框是 fixed、不受 #app 的 overflow 裁剪：贴边后底部两角必须
                # 自己圆出与窗口一致的圆角（全屏时双方都是 0px 直角，同样相等）
                tk_cr = tk.get("contentRect")
                check(tk_cr.get("blRadius") == tk.get("appBlRadius") and tk_cr.get("brRadius") == tk.get("appBrRadius"),
                      "外卖窗口：对话框底部两角圆角 == 窗口圆角",
                      "content=%s/%s app=%s/%s" % (tk_cr.get("blRadius"), tk_cr.get("brRadius"),
                                                   tk.get("appBlRadius"), tk.get("appBrRadius")))
            if tk.get("redpacketShown"):
                check(bool(rp.get("left")) and bool(rp.get("bottom")),
                      "外卖窗口：红包区被 inline 摆位（中心对准两个中点的交点）",
                      "%r / %r" % (rp.get("left"), rp.get("bottom")))
                if rp.get("rectLeft") is not None and tksm.get("midX") is not None and tk.get("contentRect"):
                    rcx = (rp.get("rectLeft", 0) + rp.get("rectRight", 0)) / 2
                    rcy = (rp.get("rectTop", 0) + rp.get("rectBottom", 0)) / 2
                    tk_cr = tk.get("contentRect") or {}
                    want_rcx = tksm.get("midX")
                    # 水平：红包中心 == 立绘可见右缘与窗口右缘的中点（midX 由页面按此公式算出）
                    check(abs(rcx - want_rcx) <= 14,
                          "外卖窗口：红包中心 == 立绘右缘与窗口右缘的中间",
                          "%s vs %s" % (round(rcx), want_rcx))
                    # 垂直：理想中心 = (立绘去透明上缘 figTop + 底部对话框顶 dialogTop) / 2
                    # （2026-10-14 终版：立绘上缘与「显示督休专员名称的对话框顶」两线中点；
                    # 之前误用窗顶 y=0）。「外卖提醒」标题浮在框外上方，框贴顶会把标题顶
                    # 出窗口——两线中点离对话框顶不足（框高+标题高）时整组（标题+框）下移
                    # 到刚好留出 TOP_MARGIN（=CD_EDGE=10）：
                    #   midY == max((figTop + dialogTop)/2, TOP_MARGIN + unitH/2)
                    #   红包框心 = midY + titleExtra/2
                    unit = tksm.get("unitH") or 0
                    _cr = tk.get("contentRect") or {}
                    _dialog_top = _cr.get("offsetTop") or _cr.get("top") or 0  # 与页面 dialogTop 同源
                    fig_top = tksm.get("figTop") or 0
                    want_rcy = max((fig_top + _dialog_top) / 2, 10 + unit / 2)
                    # 容差 1px：页面 midY 走 JS Math.round（半数向上），属取整边界非逻辑偏差
                    check(abs((tksm.get("midY") or 0) - want_rcy) <= 1,
                          "外卖窗口：红包中点公式 == max(立绘上缘与对话框顶中点, 标题净空下移)",
                          "%s vs %s（unitH=%s figTop=%s dialogTop=%s）" % (
                            tksm.get("midY"), round(want_rcy), tksm.get("unitH"),
                            fig_top, _dialog_top))
                    # 标题可见：「外卖提醒」胶囊整枚落在窗口内（曾被贴顶红包顶出窗外）
                    rt = tk.get("rpTitleInline") or {}
                    check(bool(rt.get("shown")) and (rt.get("rectTop") or -99) >= 0
                          and (rt.get("rectBottom") or -99) <= (rp.get("rectTop") or 0),
                          "外卖窗口：「外卖提醒」标题完整可见（窗口内且悬于框上方）",
                          "titleTop=%s titleBottom=%s rpTop=%s" % (rt.get("rectTop"),
                                                                   rt.get("rectBottom"), rp.get("rectTop")))
                    # 红包框中心 == 单元中心 + titleExtra/2（框比整组中心低半标题）
                    want_box_cy = (tksm.get("midY") or 0) + (tksm.get("titleExtra") or 0) / 2
                    check(abs(rcy - want_box_cy) <= 2,
                          "外卖窗口：红包框中心 == 单元中心下移半标题",
                          "rcy=%s want=%s（titleExtra=%s）" % (round(rcy), round(want_box_cy),
                                                              tksm.get("titleExtra")))
        # 有督休专员时红包/提醒内容/台词链路照常；无督休专员时也不该有 JS 错误
        check(not (tk.get("errors") or []), "外卖窗口：页面无 JS 错误", (tk.get("errors") or [])[:2])
        check(not (tk.get("consoleErrors") or []), "外卖窗口：页面无控制台错误",
              (tk.get("consoleErrors") or [])[:2])

        # ---- 「可推迟」为 0：提前提示窗与外卖窗口都不显示「推迟」按钮 ----
        # 场景由 renderer_probe.js 的 5b 段造出：short.postponeMs=0、takeout.postponeMin=0，
        # URL 带 postpone=0（真实链路里主进程生成的地址同样带这个参数）
        pbz = m.get("preBreakNoPostpone") or {}
        check(pbz.get("postponeShown") is False,
              "可推迟为 0：提前提示窗不显示「推迟」按钮", pbz.get("postponeShown"))
        # 按钮是被 .hidden 藏起来的（元素与文案都还在 DOM 里），不是被删掉。
        # 文案用词兼容内置 / 自定义两种版式（轮询会把版式切成上一次记住的 custom）
        check(any("推迟" in b for b in (pbz.get("buttons") or [])),
              "可推迟为 0：提前提示窗「推迟」按钮只是被隐藏（DOM 与文案仍在）",
              pbz.get("buttons"))
        check(not (pbz.get("consoleErrors") or []),
              "可推迟为 0：提前提示窗页面无控制台错误",
              (pbz.get("consoleErrors") or [])[:2])
        tkz = m.get("takeoutNoPostpone") or {}
        check(tkz.get("postponeShown") is False,
              "可推迟为 0：外卖窗口不显示「推迟」按钮", tkz.get("postponeShown"))
        check(str(tkz.get("postponeBtn") or "").startswith("推迟"),
              "可推迟为 0：外卖窗口「推迟」按钮只是被隐藏（文案仍在）",
              tkz.get("postponeBtn"))
        check(not (tkz.get("consoleErrors") or []),
              "可推迟为 0：外卖窗口页面无控制台错误",
              (tkz.get("consoleErrors") or [])[:2])

        # ---- 截图 ----
        # 提前提示窗是 400x240 / 400x172 的小窗，字节数天然比整页截图少，阈值单独给
        for name, min_sz in (
            ("settings.png", 20000), ("settings_timing.png", 20000), ("settings_supervisor.png", 20000),
            ("settings_stats.png", 20000), ("settings_shortcuts.png", 20000), ("settings_about.png", 20000),
            ("index.png", 20000), ("break_wide.png", 20000), ("break_tall.png", 20000),
            ("prebreak.png", 4000), ("prebreak_custom.png", 4000), ("takeout.png", 4000),
            ("prebreak_no_postpone.png", 4000), ("takeout_no_postpone.png", 4000),
        ):
            p = os.path.join(OUT, name)
            sz = os.path.getsize(p) if os.path.isfile(p) else 0
            check(sz > min_sz, "截图 %s 非空且非白屏" % name, "%d bytes" % sz)

        # ---- 隔离铁律：真实配置一个字节都不许被改 ----
        if real_settings:
            check(digest(real_settings) == real_digest,
                  "用户真实 settings.json 未被探针改写", real_settings)

    finally:
        if args.keep:
            print("（--keep：服务仍在运行 pid=%s port=%d）" % (srv.pid, port))
        elif srv.poll() is None:
            srv.kill()

    tail = open(LOG, encoding="utf-8", errors="replace").read()[-1200:] if os.path.exists(LOG) else ""
    report = (
        "渲染层探针：%d 项断言，失败 %d 项\n\n" % (checks, len(failures))
        + ("\n".join(failures) if failures else "（全部通过）")
        + "\n\n截图目录：%s\n\n--- 服务日志尾部 ---\n%s" % (OUT, tail)
    )
    open(REPORT, "w", encoding="utf-8").write(report)
    print("\n结果：%d 项断言，失败 %d 项" % (checks, len(failures)))
    print("截图：%s\n报告：%s" % (OUT, REPORT))
    return 0 if not failures else 1


if __name__ == "__main__":
    sys.exit(main())
