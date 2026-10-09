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
PROBE = os.path.join(HERE, "renderer_probe.js")
TEMP = os.environ.get("TEMP", ROOT)
OUT = os.path.join(TEMP, "wr_go_probe")
REPORT = os.path.join(TEMP, "wr_go_probe_report.txt")
LOG = os.path.join(TEMP, "wr_go_probe_server.log")
# 隔离副本：探针也绝不能碰用户的真实配置（加载路径会回写 installId / 预置条目）
ISO = os.path.join(TEMP, "wr_probe_iso")

# 内置角色的固定出场顺序（与 electron/src/supervisor.ts 的 BUILTIN_ROLE_META 同源）。
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
            rp = os.path.join(ROOT, "roles", fixed_key, "role.json")
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
        check((stg.get("navCount") or 0) >= 6, "侧栏页签渲染出来（>=6 个）", stg.get("navCount"))
        check((stg.get("tabCount") or 0) >= 6, "各页签内容区都在 DOM 里", stg.get("tabCount"))
        check((stg.get("supGallery") or 0) == 9, "督休专员画廊渲染出 9 个角色（ListRoles → DOM）",
              stg.get("supGallery"))
        check((stg.get("supGalleryAdd") or 0) == 1, "画廊末尾恰好 1 个「新建角色」磁贴",
              stg.get("supGalleryAdd"))
        check(str(stg.get("supGalleryNames") or "") == BUILTIN_ROLE_ORDER,
              "画廊顺序 == BUILTIN_ROLE_META 的内置顺序（顺序轮换的出场次序）",
              stg.get("supGalleryNames"))
        check((stg.get("supMsgItems") or 0) >= 1,
              "角色台词面板渲染出条目（ListRoles 真的下发了 messages）",
              "items=%s empty=%s" % (stg.get("supMsgItems"), stg.get("supMsgEmpty")))
        check(stg.get("supMsgRole") == want_role, "台词面板标题 == 当前角色名",
              "%s vs %s" % (stg.get("supMsgRole"), want_role))
        check("Wails" in str(stg.get("dVersion") or ""), "版本卡显示 Go 引擎（不再是 Electron）",
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
        check(same_num(stg.get("tkLead"), (cfg.get("takeout") or {}).get("leadMin")),
              "外卖提前通知分钟 == 配置 takeout.leadMin",
              "%s vs %s" % (stg.get("tkLead"), (cfg.get("takeout") or {}).get("leadMin")))

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
        check((stt.get("cells") or 0) == 8, "统计卡渲染出 8 个指标格", stt.get("cells"))
        check(not (stt.get("errs") or []), "统计页无未捕获 JS 异常", (stt.get("errs") or [])[:2])

        # ---- 主窗口（调试控制台） ----
        idx = m.get("index") or {}
        check(idx.get("shim") == "go", "页面确认跑在 Go 后端上（window.__WR_SHIM=go）", idx.get("shim"))
        check(idx.get("roles") == 9, "主窗口渲染出 9 个角色磁贴（ListRoles → DOM）", idx.get("roles"))
        check(idx.get("rolesLoaded") == idx.get("roles") and idx.get("roles"),
              "全部角色预览图以 data URL 成功装载", "%s/%s" % (idx.get("rolesLoaded"), idx.get("roles")))
        check((idx.get("optCount") or 0) == (idx.get("roles") or 0) + 1, "角色下拉选项 = 角色数 + 1 个默认项", idx.get("optCount"))
        envtxt = " ".join(idx.get("env") or [])
        check("go+wails3" in envtxt, "环境卡片显示引擎 go+wails3", (idx.get("env") or [])[:2])
        check("roles" in envtxt or "角色" in envtxt, "环境卡片显示角色目录", envtxt[:80])

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
                check(b.get("speaker") == want_role, "%s提醒窗口：说话人 == 配置固定角色的名字" % label,
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
                  "%s提醒窗口：supMetrics 记录了立绘左右缘（量的是补偿层）" % label,
                  "%r / %r" % (sm.get("figLeft"), sm.get("figRight")))
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
                # 对话框与窗口边缘留出呼吸间距（不贴边）：左右有内缩、左右对称、底部离窗底有空隙
                check((ci.get("rectLeft") or 0) >= 4 and (ci.get("rectRight") or 0) <= vpw - 4,
                      "%s提醒窗口：对话框左右与窗口边缘有间距（不贴边）" % label,
                      "left=%s right=%s vpw=%s" % (ci.get("rectLeft"), ci.get("rectRight"), vpw))
                check(abs((ci.get("rectW") or 0) - (vpw - 2 * (ci.get("rectLeft") or 0))) <= 2,
                      "%s提醒窗口：对话框左右内缩对称（宽度 == 视口宽 - 2*左距）" % label,
                      "w=%s vpw=%s left=%s" % (ci.get("rectW"), vpw, ci.get("rectLeft")))
                check((ci.get("rectBottom") or vph) <= vph - 4,
                      "%s提醒窗口：对话框底部与窗口边缘有间距（不贴底）" % label,
                      "bottom=%s vph=%s" % (ci.get("rectBottom"), vph))
            bi = b.get("barInline") or {}
            check(bool(bi.get("left")) and bool(bi.get("bottom")),
                  "%s提醒窗口：倒计时环被 inline 摆位（left/bottom 已写入）" % label,
                  "%r / %r" % (bi.get("left"), bi.get("bottom")))
            ri = b.get("ringInline") or {}
            if ri.get("rectLeft") is not None and sm.get("midX") is not None and ci.get("rectBottom") is not None:
                cx = (ri.get("rectLeft", 0) + ri.get("rectRight", 0)) / 2
                cy = (ri.get("rectTop", 0) + ri.get("rectBottom", 0)) / 2
                want_cx = sm.get("midX")
                # 框顶 y（距窗顶）= 框底边 y - 框高（实测，含内缩），
                # 环中心 = 「窗口上边缘(0) 与 框顶」的中点 = 框顶 / 2
                want_cy = ((ci.get("rectBottom") or 0) - (ci.get("rectH") or 0)) / 2
                check(abs(cx - want_cx) <= 3,
                      "%s提醒窗口：环中心 == 立绘右缘与窗口右缘的中间" % label,
                      "%s vs %s" % (round(cx), want_cx))
                check(abs(cy - want_cy) <= 3,
                      "%s提醒窗口：环中心 == 对话框顶与窗口顶的中间" % label,
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
        check(pb.get("title") == "您已连续工作较长时间，建议您短暂休息",
              "提前提示窗：标题是内置劝说文案（来自 Go 的 PreBreakState）", pb.get("title"))
        ok_sec = False
        try:
            ok_sec = 0 < int(pb.get("seconds")) <= 600
        except Exception:
            ok_sec = False
        check(ok_sec, "提前提示窗：倒计时是秒数（来自剩余毫秒）", pb.get("seconds"))
        check(pb.get("post") == " 秒进入休息", "提前提示窗：内置倒计时后缀", repr(pb.get("post")))
        check(pb.get("tipsShown") is True, "提前提示窗：内置版式显示两行说明", pb.get("tipsShown"))
        check(pb.get("pauseShown") is True, "提前提示窗：内置版式带「暂停计时」", pb.get("pauseShown"))
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
        # 有角色时：外卖窗口同样进入游戏对话画面版式；角色没有二阶段、
        # 台词恒为外卖提醒文字；红包中心落在两个锚点中点的交点上
        tksm = tk.get("supMetrics") or {}
        if tk.get("hasSupervisor"):
            check(tksm.get("mode") == "dialog",
                  "外卖窗口：版式是游戏对话画面（supMetrics.mode == dialog）", tksm.get("mode"))
            check(tk.get("stage") == "1",
                  "外卖窗口：角色没有二阶段（data-stage 恒为 1）", tk.get("stage"))
            if info.get("message"):
                check(tk.get("lineText") == info.get("message") and tk.get("lineShown") is True,
                      "外卖窗口：台词 == 外卖提醒文字（不显示角色自己的台词）",
                      "%r vs %r" % (tk.get("lineText"), info.get("message")))
            if want_role:
                check(tk.get("dialogName") == want_role and tk.get("dialogNameShown") is True,
                      "外卖窗口：名字标签亮在对话框左上角",
                      "%r / %s" % (tk.get("dialogName"), tk.get("dialogNameShown")))
            rp = tk.get("redpacketInline") or {}
            if tk.get("redpacketShown"):
                check(bool(rp.get("left")) and bool(rp.get("bottom")),
                      "外卖窗口：红包区被 inline 摆位（中心对准两个中点的交点）",
                      "%r / %r" % (rp.get("left"), rp.get("bottom")))
                if rp.get("rectLeft") is not None and tksm.get("midX") is not None and tk.get("contentRect"):
                    rcx = (rp.get("rectLeft", 0) + rp.get("rectRight", 0)) / 2
                    rcy = (rp.get("rectTop", 0) + rp.get("rectBottom", 0)) / 2
                    tk_cr = tk.get("contentRect") or {}
                    want_rcx = tksm.get("midX")
                    # 框顶 y = 框底边 y - 框高（实测，含内缩），红包中心 = 框顶与窗顶的中点
                    want_rcy = ((tk_cr.get("bottom") or 0) - (tk_cr.get("h") or 0)) / 2
                    check(abs(rcx - want_rcx) <= 3,
                          "外卖窗口：红包中心 == 立绘右缘与窗口右缘的中间",
                          "%s vs %s" % (round(rcx), want_rcx))
                    check(abs(rcy - want_rcy) <= 3,
                          "外卖窗口：红包中心 == 对话框顶与窗口顶的中间",
                          "%s vs %s" % (round(rcy), round(want_rcy)))
        # 有角色时红包/提醒内容/台词链路照常；无角色时也不该有 JS 错误
        check(not (tk.get("errors") or []), "外卖窗口：页面无 JS 错误", (tk.get("errors") or [])[:2])
        check(not (tk.get("consoleErrors") or []), "外卖窗口：页面无控制台错误",
              (tk.get("consoleErrors") or [])[:2])

        # ---- 截图 ----
        # 提前提示窗是 400x240 / 400x172 的小窗，字节数天然比整页截图少，阈值单独给
        for name, min_sz in (
            ("settings.png", 20000), ("settings_timing.png", 20000), ("settings_supervisor.png", 20000),
            ("settings_stats.png", 20000), ("settings_shortcuts.png", 20000), ("settings_about.png", 20000),
            ("index.png", 20000), ("break_wide.png", 20000), ("break_tall.png", 20000),
            ("prebreak.png", 4000), ("prebreak_custom.png", 4000), ("takeout.png", 4000),
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
