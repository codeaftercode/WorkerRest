#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""渲染层探针驱动：把真实的 web/dist 页面指向无头服务，在浏览器里跑一遍
完整的“shim.js → Go 绑定 → 渲染层”链路，并断言 DOM 与截图。

与 smoke_server.py 的分工：
  - smoke_server.py：直接打 HTTP 端点，验绑定能不能按名解析、数据对不对；
  - 本脚本：把**真实页面**装进浏览器，验前端那层胶水（动态 import 运行时、
    Call.ByName、CSP、事件通道）能不能把数据渲染出来。名字错/装载失败/被 CSP 拦，
    只有这一层看得见。

前置：先构建无头服务（python 会自动构建）
    go build -tags server -o build/bin/rest_server.exe ./cmd/workerrest
用法：python scripts/probe_renderer.py [--port 29732] [--keep]
"""

import argparse
import hashlib
import json
import os
import re
import shutil
import socket
import subprocess
import sys
import time
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(os.path.dirname(HERE))  # 项目根（原 rewrite-go，现已提升为顶层）
EXE = os.path.join(ROOT, "build", "bin", "rest_server.exe")
DIST = os.path.join(ROOT, "cmd", "workerrest", "web", "dist")
# 印泥变体档数：与 scripts/make_seal_ink.py 的 VARIANTS、两份 JS 的 SEAL_VARIANTS 一致。
# 这里只断言“挑中的那一档在 1~N 之间且文件存在”——不断言具体是几号（那是随机的）。
SEAL_VARIANTS = 6


def role_dir(root, name):
    """在 supervisors/ 下按专员名反查目录（目录名是名字的投影，可能被清洗过）。"""
    sup = os.path.join(root, "supervisors")
    if not os.path.isdir(sup):  # 仓库布局：随包资源收在 assets/ 下
        sup = os.path.join(root, "assets", "supervisors")
    if not os.path.isdir(sup):
        return None
    for dn in os.listdir(sup):
        p = os.path.join(sup, dn, "meta.json")
        if not os.path.isfile(p):
            continue
        try:
            with open(p, encoding="utf-8") as f:
                d = json.load(f)
        except Exception:
            continue
        nm = str((d or {}).get("name") or dn).strip() or dn
        if nm == name:
            return os.path.join(sup, dn)
    return None


PROBE = os.path.join(HERE, "renderer_probe.js")
TEMP = os.environ.get("TEMP", ROOT)
OUT = os.path.join(TEMP, "wr_go_probe")
REPORT = os.path.join(TEMP, "wr_go_probe_report.txt")
LOG = os.path.join(TEMP, "wr_go_probe_server.log")
# 隔离副本：探针也绝不能碰用户的真实配置（加载路径会补种预置条目并回写）
ISO = os.path.join(TEMP, "wr_probe_iso")


# 内置督休专员的固定出场顺序（与 Go 侧 internal/role 的 DefaultRoleOrder 同源）
# 注意：这是**顺序**清单，不是“个数”清单 —— 专员的实际个数从磁盘现算（见 scan_role_names）。
BUILTIN_ROLE_ORDER = [
    "秦始皇", "神秘黑客", "中尉军官", "熊猫侠客",
    "医生", "经济学家", "法官", "体能教练",
    "毒舌损友", "野蛮女友", "霸道总裁", "温柔学姐",
    "眼镜主管", "画家", "魔法师",
]


def _valid_role_dir(name):
    """与 Go 侧 validRoleDirName 同口径（目录名层面的合法性）。"""
    if not name or name == ".." or name.startswith("."):
        return False
    return not any(ch in name for ch in '/\\:*?"<>|')


def scan_role_names(root):
    """按 Go 侧 scanRoles 的口径列出**磁盘上实际存在**的督休专员名。

    身份就是名字（meta.json 里没有 key 字段），目录名是它的文件系统投影。
    顺序：自带清单顺序在前（只含磁盘上确实存在的），其余按目录名排序。
    写死“内置 17 个”是错的 —— 用户在设置页删掉某个专员后目录真的没了，
    断言会必然误报红。这里从磁盘现算，删一个少一个都不误报。
    """
    sup = os.path.join(root, "supervisors")
    if not os.path.isdir(sup):  # 仓库布局：随包资源收在 assets/ 下
        sup = os.path.join(root, "assets", "supervisors")
    recs = []
    if os.path.isdir(sup):
        for dn in sorted(os.listdir(sup)):
            if not _valid_role_dir(dn):
                continue
            p = os.path.join(sup, dn, "meta.json")
            if not os.path.isfile(p):
                continue
            try:
                with open(p, encoding="utf-8") as f:
                    d = json.load(f)
            except Exception:
                continue
            nm = str((d or {}).get("name") or dn).strip() or dn
            recs.append((nm, dn))
    out, used = [], set()
    for bn in BUILTIN_ROLE_ORDER:
        for rn, _dn in recs:
            if rn == bn and rn not in used:
                out.append(rn)
                used.add(rn)
                break
    out += [rn for rn, _dn in recs if rn not in used]
    return out


def expected_order(disk_keys, saved_order):
    """applyRoleOrder 的 Python 口径：保存的顺序在前（只取磁盘上还在的），
    其余按扫描顺序追加。"""
    dset = set(disk_keys)
    out, used = [], set()
    for k in saved_order or []:
        if k in dset and k not in used:
            out.append(k)
            used.add(k)
    out += [k for k in disk_keys if k not in used]
    return out


# 期望的督休专员数 / 顺序：从磁盘现算（内置清单增删不用再改断言）
DISK_ROLE_NAMES = scan_role_names(ROOT)
EXPECTED_ROLE_COUNT = len(DISK_ROLE_NAMES)

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


def _rel_lum_hex(hexs):
    """WCAG 相对亮度（提前提示窗的深浅档位判定与 Go / JS 侧同口径）。
    返回 None = 不是合法的 6 位十六进制。"""
    h = str(hexs or "").lstrip("#")
    if len(h) != 6:
        return None
    try:
        c = [int(h[i:i + 2], 16) for i in (0, 2, 4)]
    except ValueError:
        return None

    def ch(v):
        v /= 255.0
        return v / 12.92 if v <= 0.03928 else ((v + 0.055) / 1.055) ** 2.4

    return 0.2126 * ch(c[0]) + 0.7152 * ch(c[1]) + 0.0722 * ch(c[2])


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

    # Playwright 驱动系统 Edge：node 走 PATH，playwright 装在 scripts/node_modules
    #（历史位置是仓库根，两处都认）。
    node_cmd = shutil.which("node")
    if not node_cmd:
        print("找不到 node（渲染层探针改用 Playwright 驱动 Edge，需要 node 环境）")
        return 2
    playwright_mod = next((os.path.join(d, "playwright") for d in
                          (os.path.join(ROOT, "scripts", "node_modules"), os.path.join(ROOT, "node_modules"))
                          if os.path.isdir(os.path.join(d, "playwright"))), "")
    if not playwright_mod:
        print("找不到 playwright（先在本目录 npm install）")
        return 2

    # 构建无头服务（保证探针跑的是当前源码）
    print("构建无头服务…")
    b = subprocess.run(
        ["go", "build", "-tags", "server", "-o", os.path.join("build", "bin", "rest_server.exe"), "-ldflags", "-s -w", "./cmd/workerrest"],
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
    # 提前提示窗需要一个**真的处于提前提示区间**的服务：把短休息改成“10 分钟一次、
    # 提前 10 分钟提示”，`nextAt - notifyBeforeMs <= now` 立刻成立 → 启动后第一跳就进 pre。
    # 改的是隔离副本（真实配置在末尾仍有 SHA-256 断言守着）。
    # ---- 开跑前的配置快照 ----
    # 断言必须拿“页面启动时读到的配置”比对，不能用跑完再读的那份：探针在过程里会
    # 改配置（兜底还原本身也有 bug 历史），拿被改过的值去比开场态只会误报。
    seed_cfg = {}
    if os.path.exists(iso_settings):
        cfg_seed = json.load(open(iso_settings, encoding="utf-8"))
        cfg_seed.setdefault("short", {}).update({
            "enabled": True, "mode": "countdown",
            "intervalMs": 600000, "notifyBeforeMs": 600000,
        })
        cfg_seed["long"] = dict(cfg_seed.get("long") or {}, enabled=False)
        cfg_seed["takeout"] = dict(cfg_seed.get("takeout") or {}, enabled=False)
        # 督休专员总开关也要播成 true：预览面板的名字牌与台词框只有启用时才渲染
        # （Go 侧 SupervisorPreview 在 enabled=false 时直接返回 nil）；跟着用户真实
        # 开关漂移，这几条断言会无缘无故变红。
        cfg_seed["supervisor"] = dict(cfg_seed.get("supervisor") or {}, enabled=True)
        with open(iso_settings, "w", encoding="utf-8") as f:
            json.dump(cfg_seed, f, ensure_ascii=False, indent=2)
        seed_cfg = cfg_seed

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
        sup_cfg = cfg.get("supervisor") or {}
        fixed_key = sup_cfg.get("fixedRole")
        # 只有“固定轮换”模式下，会话里的专员才**必然**是 fixedRole 那位。顺序 / 随机模式
        # 会轮换到别的专员（用户可能就这么设的：实机配置被设置页改成过 sequence），那时拿
        # fixedRole 当基准会把正确行为判成红。所以非固定模式只要求“说话人是磁盘上真实存在
        # 的某个专员”，固定模式下再额外卡死等于 fixedRole。
        role_names = set(scan_role_names(ROOT))
        sel_mode = sup_cfg.get("selection") or "random"
        # fixed_role_name：设置页“提醒内容”面板展示的那位（永远跟 fixedRole 走，与轮换
        # 模式无关）；want_role：**会话**里会出场的那位（只有固定模式才必然等于前者）。
        fixed_role_name = ""
        if fixed_key:
            rp = os.path.join(role_dir(ROOT, fixed_key) or "", "meta.json")
            if os.path.isfile(rp):
                fixed_role_name = json.load(open(rp, encoding="utf-8")).get("name") or ""
        want_role = fixed_role_name if sel_mode == "fixed" else ""
        print("   （对齐基准：settings=%s role=%s mode=%s）"
              % (iso_settings if cfg else "未找到", fixed_key or "-", sel_mode))

        # ---- 设置页（真正的主界面）----
        # 这一节的分量最重：设置页没有自己的默认值，拿到 getSettings() 就直接填表，
        # 少一个字段 / 某个绑定没接上 → fillForm() 抛错 → 整页停摆。所以既断言
        # “页面无错”，也断言“关键值与配置文件逐项对齐”。
        stg = m.get("settings") or {}
        check(stg.get("shim") == "go", "设置页跑在 Go 后端上（window.__WR_SHIM=go）", stg.get("shim"))
        check(stg.get("win") == "settings", "设置页窗口标识已注入（__WR_WIN=settings）", stg.get("win"))
        check(not (stg.get("consoleErrors") or []), "设置页加载期间无控制台错误",
              (stg.get("consoleErrors") or [])[:2])
        check(not (stg.get("errs") or []), "设置页无未捕获 JS 异常", (stg.get("errs") or [])[:2])
        # 主题：删除了“跟随系统”选项，默认浅色
        theme_opts = stg.get("themeOptions") or []
        check("system" not in theme_opts, "主题选项不含“跟随系统”", theme_opts)
        check(stg.get("themeChecked") == "light", "主题默认选中“浅色”", stg.get("themeChecked"))
        check(stg.get("themeSet"), "设置页已应用主题（data-theme 已写入）", stg.get("themeSet"))
        check((stg.get("navCount") or 0) >= 6, "侧栏页签渲染出来（>=6 个）", stg.get("navCount"))
        # 预览被后端拒弹（真实提醒窗口正在显示）时的反馈：点了不能毫无动静
        pbt = m.get("previewBusyToast") or {}
        check(not pbt.get("err"), "预览拒弹提示：预览按钮与 toast 节点都在", pbt.get("err"))
        check(pbt.get("shown") is True, "预览被拒时弹出提示条", pbt.get("shown"))
        check("无法预览" in (pbt.get("text") or ""), "提示文案说明“暂时无法预览”", pbt.get("text"))
        check(pbt.get("stubRestored") is True, "探针用完已还原 api.previewBreak（不污染后续断言）",
              pbt.get("stubRestored"))
        check((stg.get("tabCount") or 0) >= 6, "各页签内容区都在 DOM 里", stg.get("tabCount"))
        check((stg.get("supGallery") or 0) == EXPECTED_ROLE_COUNT,
              "督休专员画廊渲染出 %d 个督休专员（ListRoles → DOM）" % EXPECTED_ROLE_COUNT,
              stg.get("supGallery"))
        check((stg.get("supGalleryAdd") or 0) == 1, "画廊末尾恰好 1 个“新建督休专员”磁贴",
              stg.get("supGalleryAdd"))
        # 预览面板的名字牌：必须与真实提醒窗口同款“竖排金字 + 红印泥墨块 + 牌外碎点环”。
        # 旧版是圆角胶囊小徽标（纯 --accent 底），预览与真机对不上 —— 这几条钉住四个特征：
        # 竖排、双层 clip、牌底是墨块图、碎点挂在负 inset 的伪元素上。
        _pn = stg.get("supPreviewName") or {}
        check(bool(_pn.get("text")),
              "督休专员预览：名字牌承载说话人名（不再是台词里手写）", _pn.get("text"))
        check(str(_pn.get("writing") or "").startswith("vertical"),
              "督休专员预览：名字牌是竖排（vertical-rl）", _pn.get("writing"))
        check(str(_pn.get("clip") or "").replace(" ", "") == "text,border-box",
              "督休专员预览：名字牌是双层背景（金字裁进 text + 墨块裁进 border-box）",
              _pn.get("clip"))
        check(_pn.get("serif") is True,
              "督休专员预览：名字牌用宋体栈（与真机一致）", _pn.get("serif"))
        _pink = str(_pn.get("ink") or "")
        _pspk = str(_pn.get("specks") or "")
        _m1 = re.search(r"seal-ink-(\d+)\.svg", _pink)
        _m2 = re.search(r"seal-specks-(\d+)\.svg", _pspk)
        check(bool(_m1) and 1 <= int(_m1.group(1)) <= SEAL_VARIANTS,
              "督休专员预览：牌底用的是 1~%d 号印泥变体" % SEAL_VARIANTS, _pink)
        check(bool(_m1) and bool(_m2) and _m1.group(1) == _m2.group(1)
              and os.path.isfile(os.path.join(DIST, _pink))
              and os.path.isfile(os.path.join(DIST, _pspk)),
              "督休专员预览：碎点与牌底同下标且文件都在包里",
              "ink=%s specks=%s" % (_pink, _pspk))
        _pai = _pn.get("afterInset")
        check(isinstance(_pai, (int, float)) and _pai < -1,
              "督休专员预览：碎点层是比盒子大一圈的伪元素（负 inset）",
              "inset=%s content=%s" % (_pai, _pn.get("afterContent")))
        # 位置：真机里牌面钉在立绘头部左侧的空白区（贴左、中上部、永在对话条之上），
        # 预览按面板百分比锚。这里必须用“切到督休专员页签之后”量到的那份几何 ——
        # 首屏其余页签是 display:none，rect 全是 0（越界会误报成 dx=0/dy=0）。
        _pg = m.get("supPreviewGeom") or {}
        _gdx, _gdy = _pg.get("dx"), _pg.get("dy")
        _gbw, _gbh = _pg.get("boxW"), _pg.get("boxH")
        check(_pg.get("shown") is True and _pg.get("text"),
              "督休专员预览：名字牌已显示且有文本", _pg.get("text"))
        check(all(isinstance(v, (int, float)) for v in (_gdx, _gdy, _gbw, _gbh))
              and 0 <= _gdx <= 40 and 0 <= _gdy < _gbh * 0.55,
              "督休专员预览：名字牌在面板左上（立绘头部左侧的空白区）",
              "dx=%s dy=%s box=%sx%s" % (_gdx, _gdy, _gbw, _gbh))
        check(_pg.get("aboveContent") is True,
              "督休专员预览：名字牌不压台词框（真机里它在头部左侧，不在对话条上）",
              "aboveContent=%s plateH=%s" % (_pg.get("aboveContent"), _pg.get("plateH")))
        # 磁贴的 key 集合 == 磁盘上的专员集合（不多不少）：只比个数会漏掉“渲染出
        # 另一个专员”这类错位，比集合才抓得住。个数本身从磁盘现算 —— 用户在应用里
        # 删掉一个内置专员后目录真的没了，写死“17 个”会必然误报红。
        gallery_keys = [k for k in str(stg.get("supGalleryNames") or "").split(",") if k]
        check(sorted(gallery_keys) == sorted(DISK_ROLE_NAMES),
              "画廊磁贴的督休专员集合 == 磁盘上的集合（不多不少）",
              "%d vs %d" % (len(gallery_keys), len(DISK_ROLE_NAMES)))
        # 画廊顺序：拖过 = 用户保存的 supervisor.order；没拖过 = 内置清单顺序
        # （两者是同一个来源：内置清单顺序就是顺序轮换的出场次序）。
        # 期望顺序从磁盘现算：保存的顺序里可能有已被删掉的专员（key 不在磁盘上），
        # 那部分要被跳过、其余按扫描顺序补到末尾 —— 与 Go 侧 applyRoleOrder 同口径。
        pre_saved = m.get("roleDragPre") or []
        want_gallery = ",".join(expected_order(DISK_ROLE_NAMES, pre_saved))
        check(str(stg.get("supGalleryNames") or "") == want_gallery,
              "画廊顺序 == 用户保存的顺序（未拖过时 == 内置清单顺序，也是顺序轮换的出场次序）",
              stg.get("supGalleryNames"))
        # 拖拽排序（拖第 1 块到第 3 块之前 → 期望 [2,1,3,4…]）
        drag = m.get("roleDrag") or {}
        before = drag.get("before") or []
        after = drag.get("after") or []
        saved = drag.get("saved") or []
        reloaded = drag.get("reloaded") or []
        if len(before) >= 3:
            want_after = [before[1], before[0]] + before[2:]
            check(after == want_after,
                  "拖动立绘后画廊顺序 = 拖动源插到目标之前",
                  "%s vs %s" % (after[:4], want_after[:4]))
            check(saved == want_after,
                  "拖动后的顺序已落盘（supervisor.order）",
                  "%s vs %s" % (saved[:4], want_after[:4]))
            check(reloaded == want_after,
                  "刷新页面后顺序仍在（持久化生效）",
                  "%s vs %s" % (reloaded[:4], want_after[:4]))
            cfg_order = (cfg.get("supervisor") or {}).get("order") or []
            check(cfg_order == want_after,
                  "配置文件里的 supervisor.order == 拖动后的顺序",
                  "%s vs %s" % (cfg_order[:4], want_after[:4]))
        else:
            check(False, "拖拽排序用例跑起来了（画廊至少 3 块磁贴）", before)
        check((stg.get("supMsgItems") or 0) >= 1,
              "督休专员台词面板渲染出条目（ListRoles 真的下发了 messages）",
              "items=%s empty=%s" % (stg.get("supMsgItems"), stg.get("supMsgEmpty")))
        panel_role = stg.get("supMsgRole")
        # “提醒内容”面板标题 = 设置页当前正在编辑的那位，**不是** fixedRole：
        # 固定模式下初始编辑对象按 fixedRole 选，两者必然相等；随机/顺序模式下按
        # “第一个已勾选”选（见 settings.js 的 ensureSupEditKey），fixedRole 只是个
        # 遗留值（2026-11-02 用户切模式后 fixedRole 停在“神秘黑客”、面板显示“秦始皇”
        # 曾把这里判红）。所以只有固定模式才卡死等于 fixedRole。
        if sel_mode == "fixed" and fixed_role_name:
            check(panel_role == fixed_role_name, "台词面板标题 == 固定模式下的 fixedRole",
                  "%s vs %s" % (panel_role, fixed_role_name))
        else:
            check(panel_role in role_names, "台词面板标题是磁盘上某个督休专员名", panel_role)
        # 关于页“软件信息”卡：一行“版本”（版本号 | 发布时间）+ 一行“许可条款 | 开源鸣谢”
        # + 该行行尾的协议标识与运行环境。版本值由 -ldflags 注入（源码里是 dev 占位），所以只断言
        # “形如 v1.2.3 或 dev”，不钉具体版本号 —— 否则每次发版都要改探针。
        ver = str(stg.get("dVersion") or "").strip()
        check(bool(re.match(r"^(v\d+\.\d+\.\d+|dev)$", ver)),
              "关于页显示版本号（v<主>.<次>.<修订> 或 dev）", ver)
        check(bool(str(stg.get("dBuildTime") or "").strip()), "关于页显示发布时间（与版本同一行）",
              stg.get("dBuildTime"))
        # 运行环境压进了“许可条款 | 开源鸣谢”那一行的行尾（原本是独立一行，为省高度删掉），
        # 所以断言它重新出现、且两个指纹都在 —— 只断言非空的话，只填了 Go 也算过。
        rt = str(stg.get("dRuntime") or "").strip()
        check("Go " in rt and "Wails " in rt, "关于页行尾显示运行环境（Go 与 Wails 版本）", rt)

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
        check(stg.get("fullscreenPause") == bool(cfg.get("fullscreenPause")),
              "全屏或演示暂停勾选态 == 配置 fullscreenPause",
              "%s vs %s" % (stg.get("fullscreenPause"), cfg.get("fullscreenPause")))
        fsrt = m.get("fsPauseRoundTrip") or {}
        check(fsrt.get("on") is True,
              "全屏应用开关：页面勾上后配置真的是 true",
              fsrt.get("on"))
        check(fsrt.get("off") is False,
              "全屏或演示开关：页面取消后配置真的变成 false",
              fsrt.get("off"))
        # 收尾必须回到初值：真实配置本来开着这个开关时，探针不能把它留成 false
        # （否则下一条“勾选态 == 磁盘值”自相矛盾，2026-10-29 实测踩到）
        check(fsrt.get("restored") == bool(stg.get("fullscreenPause")),
              "全屏或演示开关：探针收尾把配置还原成初值",
              "%s vs %s" % (fsrt.get("restored"), stg.get("fullscreenPause")))
        # 共享屏幕时隐藏：默认开（Go 侧 Flag(true,…)），缺键时页面也必须呈现勾选态。
        # 比对基准是**开跑前的快照** seed_cfg —— 不能用收尾时读的 cfg：探针中途做过
        # 开关往返，收尾那份已经是“还原后”的值，而还原逻辑读错了字段（见
        # renderer_probe.js 的 sgWas 注释），历史上恒把 false 改成 true，于是用户真的
        # 关掉过这个开关时，这条断言会稳定误报成 False vs True。
        expect_hide = (seed_cfg.get("appearance") or {}).get("hideOnShare", True) is not False
        check(bool(stg.get("apHideOnShare")) == expect_hide,
              "共享屏幕时隐藏勾选态 == 配置 appearance.hideOnShare（缺省为开）",
              "%s vs %s" % (stg.get("apHideOnShare"), expect_hide))
        sg = m.get("shareGuardRoundTrip") or {}
        check(sg.get("off") is False,
              "共享屏幕时隐藏：页面取消后配置真的是 false", sg.get("off"))
        check(sg.get("on") is True,
              "共享屏幕时隐藏：页面勾上后配置真的是 true", sg.get("on"))
        check(bool(sg.get("restored")) == expect_hide,
              "共享屏幕时隐藏：探针收尾把配置还原成初值",
              "%s vs %s" % (sg.get("restored"), expect_hide))
        check(stg.get("supSelection") == (cfg.get("supervisor") or {}).get("selection"),
              "督休专员选择方式 == 配置 supervisor.selection",
              "%s vs %s" % (stg.get("supSelection"), (cfg.get("supervisor") or {}).get("selection")))
        check(same_num(stg.get("apWidthPct"), (cfg.get("appearance") or {}).get("widthPct")),
              "提醒窗口宽度百分比 == 配置 appearance.widthPct",
              "%s vs %s" % (stg.get("apWidthPct"), (cfg.get("appearance") or {}).get("widthPct")))
        # 透明度滑条：步长必须 0.01（用户 2026-10-23 要求“每次最少可以调 0.01”），
        # 范围仍是 0 ~ 0.5。默认值（现为 0）属配置层口径，由 Go 侧 TestAppearanceTransparencyDefault 覆盖。
        check(stg.get("apOpacityStep") == "0.01",
              "透明度滑条步长 = 0.01", stg.get("apOpacityStep"))
        check(stg.get("apOpacityRange") == ["0", "0.5"],
              "透明度滑条范围 = 0 ~ 0.5", stg.get("apOpacityRange"))
        # 侧栏状态格（下次外卖 / 自定义提醒）：名称在左、时间在右，两段都必须单行 ——
        # 名字长时由 fitStatusCells 缩字号，这里守住“不换行 / 不裁字 / 不挤出卡片”三条底线。
        cells = stg.get("statusCells") or []
        check(len(cells) >= 2, "侧栏状态格已渲染（状态 + 下次提醒）", "%d 个" % len(cells))
        for c in cells:
            cell_label = c.get("k") or ""
            cell_detail = "kOver=%s vOver=%s cellOver=%s 宽=%s 字号=%s" % (
                c.get("kOver"), c.get("vOver"), c.get("cellOver"), c.get("width"), c.get("kFs"))
            check((c.get("kOver") or 0) <= 1 and (c.get("vOver") or 0) <= 1
                  and (c.get("cellOver") or 0) <= 1,
                  "侧栏状态格《%s》不裁字、不溢出卡片" % cell_label, cell_detail)
            kfs = c.get("kFs") or 12
            check((c.get("kH") or 0) <= kfs * 1.9,
                  "侧栏状态格《%s》名称保持单行" % cell_label,
                  "高=%s 字号=%s" % (c.get("kH"), kfs))
        # 压力测试：注入 20 字名字后必须缩字号/中间省略，仍然不裁字、不溢出、不换行
        ss = m.get("statusStress") or {}
        if ss:
            ss_detail = "显示=%s 字号=%s kOver=%s cellOver=%s title=%s" % (
                ss.get("text"), ss.get("kFs"), ss.get("kOver"), ss.get("cellOver"),
                (ss.get("title") or "")[:12])
            check((ss.get("kOver") or 0) <= 1,
                  "侧栏状态格·20 字名称不裁字（缩字号/省略生效）", ss_detail)
            check((ss.get("cellOver") or 0) <= 0,
                  "侧栏状态格·20 字名称不溢出卡片", ss_detail)
            check((ss.get("kH") or 0) <= (ss.get("kFs") or 12) * 1.9,
                  "侧栏状态格·20 字名称仍单行", ss_detail)
        else:
            check(False, "侧栏状态格压力采集到数据（statusStress）", "缺失")
        # 暂停态文案：手动暂停 / 系统空闲 / 勿扰 / 应用占用一律显示“暂停休息”，
        # 原因只挂在 title 上（用户 2026-10-24 要求去掉“（已暂停：…）”长尾巴）。
        ps = m.get("pauseStatus") or {}
        if ps:
            pv = ps.get("v") or ""
            check(pv == "暂停休息", "暂停时状态格显示“暂停休息”",
                  "%s | title=%s" % (pv, ps.get("title")))
            check("努力工作中" not in pv and "已暂停" not in pv,
                  "暂停时状态格不再出现“努力工作中（已暂停：…）”", pv)
        else:
            check(False, "暂停态采集到数据（pauseStatus）", "缺失")
        check(bool(m.get("pauseResumed")) and "暂停休息" not in (m.get("pauseResumed") or ""),
              "恢复后状态格回到工作状态文案", m.get("pauseResumed"))
        check(same_num(stg.get("tkLead"), (cfg.get("takeout") or {}).get("leadSec")),
              "外卖提前通知秒数 == 配置 takeout.leadSec",
              "%s vs %s" % (stg.get("tkLead"), (cfg.get("takeout") or {}).get("leadSec")))

        # ---- 页签切换：切到每一页都要真的显示出来（切换时抛错会让内容空白）----
        tabs = m.get("tabs") or {}
        for key, label in (("timing", "时间规划"), ("supervisor", "督休专员"), ("stats", "统计报表"),
                           ("shortcuts", "快捷键"), ("about", "关于")):
            t = tabs.get(key) or {}
            check(t.get("active") == "tab-" + key and t.get("visible") is True,
                  "切到“%s”页签后内容可见" % label,
                  "active=%r visible=%s" % (t.get("active"), t.get("visible")))
        check(not (stg.get("consoleErrorsAfterTabs") or []), "切换页签期间无控制台错误",
              (stg.get("consoleErrorsAfterTabs") or [])[:2])

        # ---- 右边缘拖动带：滚动条不许贴住窗口最外缘 ----
        # 无边框窗口是 WS_THICKFRAME + 整窗 extend-frame，系统给的拖动带只有
        # SM_CXSIZEFRAME ≈ 4px（各 DPI 都是 4 DIP）；WebView 的垂直滚动条 10px 宽、正好
        # 盖在这 4px 上，于是“页面一出现滚动条，右边框就再也拖不动窗口”（2026-10-26）。
        # 修复点：给 #content 加 6px 透明右边框，Chrome 会把滚动条排到边框内侧。
        eg = m.get("edgeGutter") or {}
        if eg:
            egd = "viewport=%s contentRight=%s borderRight=%s padRight=%s scrolls=%s" % (
                eg.get("viewport"), eg.get("contentRight"), eg.get("borderRight"),
                eg.get("padRight"), eg.get("scrolls"))
            check(eg.get("scrolls") is True, "督休专员页出现垂直滚动条（拖动带压力前提）", egd)
            check((eg.get("borderRight") or 0) >= 6,
                  "内容区右侧留出 ≥6px 边（滚动条不贴窗口最外缘）", egd)
            check(abs((eg.get("contentRight") or 0) - (eg.get("viewport") or 0)) <= 1,
                  "内容区右边框落在视口内（最外 6px 真的属于这条边）", egd)
            check((eg.get("borderColor") or "").replace(" ", "") in ("rgba(0,0,0,0)", "transparent"),
                  "右侧留边是透明的（不引入可见色带）", egd)
        else:
            check(False, "采集到右边缘拖动带数据（edgeGutter）", "缺失")

        # ---- 督休专员重名：提交重名 → 拒绝保存、对话框不关、要求重新输入 ----
        # （需求原文：“导入立绘时…提交名称时检查重名，若有重名的，拒绝保存，
        #   要求用户重新输入名称”。这条断言把该行为钉死在真实链路上。）
        dn = m.get("dupName") or {}
        if dn.get("skipped"):
            check(False, "督休专员重名用例跑起来了（画廊至少 2 块磁贴）", dn.get("skipped"))
        else:
            check(dn.get("opened", {}).get("visible") is True,
                  "右键 → “重命名”能弹出命名框", dn.get("opened"))
            rj = dn.get("rejected") or {}
            check(rj.get("stillOpen") is True,
                  "提交重名后命名框**不关闭**（拒绝保存，要求用户重新输入）",
                  "stillOpen=%s err=%r" % (rj.get("stillOpen"), rj.get("errText")))
            check(rj.get("errShown") is True and bool(rj.get("errText")),
                  "重名时框内给出错误提示（不是一闪而过的 toast）",
                  "shown=%s text=%r" % (rj.get("errShown"), rj.get("errText")))
            check(rj.get("inputKept") == dn.get("bName"),
                  "被拒后输入框里已输入的名字原样保留（用户不用重打一遍）",
                  "%r vs %r" % (rj.get("inputKept"), dn.get("bName")))
            check(rj.get("focused") == "pm-input",
                  "被拒后焦点回到输入框（可以直接改）", rj.get("focused"))
            check(dn.get("diskA") == dn.get("aName"),
                  "重名被拒后磁盘上第一个专员的显示名**没被改**（零副作用）",
                  "%r vs %r" % (dn.get("diskA"), dn.get("aName")))
            check(dn.get("goReject") is False,
                  "Go 侧权威判定：SaveRole 改成别人的名字返回 false", dn.get("goReject"))
            cl = dn.get("cleared") or {}
            check(cl.get("hidden") is True,
                  "用户一改动输入框，旧的错误提示就消失", cl)
            check(dn.get("closed") is True, "取消后命名框正常关闭（不影响后续用例）", dn.get("closed"))

        # ---- 候选专员画廊：滚到顶/底后继续滚 → 整页跟着滚（滚动链）----
        # （需求原文：“当鼠标在候选专员的立绘列表中滚动时，若立绘已经滚动到顶或到底，
        #   继续滚已经滚不动了，则令整个页面继续向上或向下滚动。”
        #   实现：.gallery-body 的 overscroll-behavior-y 改成 auto ——
        #   contain 会**掐断**滚动链，滚到边界后滚轮就没反应了。）
        gs = m.get("galleryScroll") or {}
        if gs.get("skipped"):
            check(False, "画廊滚动链用例跑起来了（列表可滚且页面可滚）", gs.get("skipped"))
        else:
            init = gs.get("init") or {}
            check(init.get("osb") == "auto",
                  "画廊列表的纵向 overscroll-behavior 是 auto（不掐断滚动链）",
                  init.get("osb"))
            # A：列表已到底，继续下滚 → 外层往下走
            dn2 = (gs.get("down") or {}).get("before") or {}
            da = (gs.get("down") or {}).get("after") or {}
            check(da.get("c", 0) > dn2.get("c", 0),
                  "立绘列表滚到底后继续下滚 → **整个页面**跟着往下滚",
                  "content %s → %s" % (dn2.get("c"), da.get("c")))
            check(da.get("g") == dn2.get("g"),
                  "到边界后继续滚，画廊自身不再动（内容没被越界拖动）",
                  "gallery %s → %s" % (dn2.get("g"), da.get("g")))
            # B：列表已到顶，继续上滚 → 外层往上走
            ub = (gs.get("up") or {}).get("before") or {}
            ua = (gs.get("up") or {}).get("after") or {}
            check(ua.get("c", 0) < ub.get("c", 0),
                  "立绘列表滚到顶后继续上滚 → **整个页面**跟着往上滚",
                  "content %s → %s" % (ub.get("c"), ua.get("c")))
            # C：列表在中间 → 只滚列表，外层不动（不能变成“一滚就带动整页”）
            mb = (gs.get("middle") or {}).get("before") or {}
            ma = (gs.get("middle") or {}).get("after") or {}
            check(ma.get("g", 0) != mb.get("g", 0),
                  "列表在中间时滚轮照常滚动列表本身",
                  "gallery %s → %s" % (mb.get("g"), ma.get("g")))
            check(ma.get("c") == mb.get("c"),
                  "列表在中间时**不会**误带动整页（滚动链只在边界触发）",
                  "content %s → %s" % (mb.get("c"), ma.get("c")))

        # “检查更新”点下去必须立刻有反馈（2026-10-26：原先点了毫无动静，用户会反复点）
        ul = m.get("updateLoading") or {}
        if ul:
            rt = ul.get("right") or {}
            du = ul.get("during") or {}
            af = ul.get("after") or {}
            check(rt.get("btnText") == "检查中…" and rt.get("disabled") is True,
                  "点“检查更新”后按钮立刻变“检查中…”并禁用",
                  "btnText=%r disabled=%s" % (rt.get("btnText"), rt.get("disabled")))
            check(rt.get("pill") == "正在检查更新…" and "loading" in str(rt.get("pillClass")),
                  "点“检查更新”后状态条立刻显示“正在检查更新…”",
                  "%r class=%r" % (rt.get("pill"), rt.get("pillClass")))
            check(du.get("btnText") == "检查中…" and du.get("pill") == "正在检查更新…",
                  "检查未返回前一直保持加载态",
                  "btnText=%r pill=%r" % (du.get("btnText"), du.get("pill")))
            check(af.get("btnText") == "检查更新" and af.get("disabled") is False,
                  "检查结束后按钮文案与可点状态复原",
                  "btnText=%r disabled=%s" % (af.get("btnText"), af.get("disabled")))
            check(af.get("pill") == "已是最新版本" and "loading" not in str(af.get("pillClass")),
                  "检查结束后状态条显示结果、不再是加载态",
                  "%r class=%r" % (af.get("pill"), af.get("pillClass")))
        else:
            check(False, "采集到“检查更新”加载态数据（updateLoading）", "缺失")

        # 手动检查发现新版本 → 必须弹窗，且不受“该版本已被忽略”“本会话已为同版本弹过”
        # 的限制（这两条门控只服务自动检查；用户主动点了就必须拿到结果）
        uf = m.get("updateModalForce") or {}
        if uf:
            for tag, label in (("first", "首次手动检查"),
                               ("second", "同版本再次手动检查")):
                r = uf.get(tag) or {}
                check(r.get("shown") is True and r.get("visible") is True,
                      "%s发现新版本时弹出更新说明弹窗" % label,
                      "shown=%s visible=%s" % (r.get("shown"), r.get("visible")))
                check(str(r.get("ver") or "") == "v9.9.9",
                      "%s弹窗版本号按 normVer 显示" % label, r.get("ver"))
                check(r.get("items") == 2,
                      "%s弹窗里列出更新条目" % label, r.get("items"))
                check(r.get("closed") is True and r.get("btnText") == "检查更新",
                      "%s关闭弹窗后按钮复原" % label,
                      "closed=%s btnText=%r" % (r.get("closed"), r.get("btnText")))
        else:
            check(False, "采集到手动检查弹窗数据（updateModalForce）", "缺失")

        # 声音试听：URL 算对还不够，要浏览器真能解码它 —— 这一条把 /sounds/ 资源服务
        # 与渲染层的 <audio> 串起来（真发声由 probe_gui_boot.py 覆盖）。
        sp = m.get("soundPreview") or {}
        check(sp.get("url") == "/sounds/short_start.wav",
              "试听 URL 指向资源服务的内置提示音", sp.get("url"))
        check(sp.get("ok") is True and float(sp.get("duration") or 0) > 0,
              "浏览器能解码内置提示音（loadedmetadata 拿到时长）",
              "ok=%s duration=%s error=%s" % (sp.get("ok"), sp.get("duration"), sp.get("error")))

        # ---- 统计页：报表图真的画出来了（数据来自 Go 侧的 stats.go）----
        # 空表也能画坐标轴，所以判据是“图形节点数够多”而不是“有 svg 就行”。
        stt = m.get("stats") or {}
        check(stt.get("svg") is True, "统计页报表图 SVG 已画出", stt.get("svg"))
        check((stt.get("marks") or 0) >= 6, "报表图里有真实图形节点（不是空白容器）", stt.get("marks"))
        check(str(stt.get("date") or "").startswith("（") and str(stt.get("date") or "").endswith("）"),
              "统计页日期取自 Go 的 today.date", stt.get("date"))
        check(any(w in str(stt.get("date") or "") for w in
                  ("周一", "周二", "周三", "周四", "周五", "周六", "周日")),
              "日期括号里带星期", stt.get("date"))
        check((stt.get("cards") or 0) == 4, "统计页共 4 张卡（今日看板 / 休息报表 / 外卖报表 / 自定义）", stt.get("cards"))
        check(str(stt.get("firstTitle") or "").startswith("今日看板"),
              "统计页首张卡标题是“今日看板”", stt.get("firstTitle"))
        comp = stt.get("comp") or {}
        check(comp.get("panels") == 3, "今天卡渲染出 3 块面板", comp.get("panels"))
        check(comp.get("segs") == 3, "时间构成画出 3 段堆叠条", comp.get("segs"))
        check(comp.get("bars") == 7, "休息 4 行 + 久坐 3 行，共 7 行条形", comp.get("bars"))
        # 今天卡不再挂任何“?”说明（时间构成 / 休息 / 久坐 都不带）
        check(comp.get("qmarks") == 0, "今天卡没有说明图标", comp.get("qmarks"))
        check("软件运行" in str(comp.get("total") or ""),
              "时间构成右上角写“软件运行 …”", comp.get("total"))
        labels = comp.get("barLabels") or []
        check(labels[:4] == ["完成", "推迟", "跳过", "提前结束"],
              "休息四行 = 完成 / 推迟 / 跳过 / 提前结束", labels[:4])
        check(labels[4:] == ["最长", "平均", "中位数"],
              "久坐三行 = 最长 / 平均 / 中位数", labels[4:])
        early_val = str((comp.get("barVals") or [""])[3] or "")
        check(early_val.endswith("次"), "提前结束显示的是次数", early_val)

        # 四项的数据源 = 后端 today 的四个计数，且“完成”只取短 + 长休息：
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
                  "“%s”= 后端 %s 计数（自定义提醒 %d 次不计入）" %
                  (name, name, int(bk.get("customCount") or 0)),
                  "%s vs %d" % (got, n))
        # 久坐的值是时长（不是次数）：末尾一定是“秒”或“分”
        sed_vals = (comp.get("barVals") or [])[4:]
        check(len(sed_vals) == 3 and all(v.endswith(("秒", "分")) for v in sed_vals),
              "久坐三行显示的是时长", sed_vals)
        check("分钟" in str(comp.get("sedNote") or ""),
              "久坐面板有一句小结（提到 45 分钟基准）", comp.get("sedNote"))
        check(not (stt.get("errs") or []), "统计页无未捕获 JS 异常", (stt.get("errs") or [])[:2])

        # 休息报表：表格最后一列是“提前结束次数”（紧跟在“推迟次数”之后），
        # 图例也多出“提前结束次数”一条折线。
        heads = [str(h) for h in (stt.get("rcHeads") or [])]
        check(len(heads) == 10, "休息报表表格 10 列（新增提前结束）", heads)
        check(heads[-2].startswith("推迟次数") and heads[-1].startswith("提前结束次数"),
              "“提前结束次数”列排在“推迟次数”之后", heads[-2:])
        legend = [str(x) for x in (stt.get("restLegend") or [])]
        check(len(legend) == 6, "休息报表图例 6 项（2 根柱 + 4 条折线，新增提前结束）", legend)
        check(legend and legend[-1] == "提前结束次数",
              "图例最后一项是“提前结束次数”", legend[-1:] if legend else [])

        # ---- 主窗口（调试控制台） ----
        idx = m.get("index") or {}
        check(idx.get("shim") == "go", "页面确认跑在 Go 后端上（window.__WR_SHIM=go）", idx.get("shim"))
        check(idx.get("roles") == EXPECTED_ROLE_COUNT,
              "主窗口渲染出 %d 个督休专员磁贴（ListRoles → DOM）" % EXPECTED_ROLE_COUNT,
              idx.get("roles"))
        check(idx.get("rolesLoaded") == idx.get("roles") and idx.get("roles"),
              "全部督休专员预览图以 data URL 成功装载", "%s/%s" % (idx.get("rolesLoaded"), idx.get("roles")))
        check((idx.get("optCount") or 0) == (idx.get("roles") or 0) + 1, "督休专员下拉选项 = 督休专员数 + 1 个默认项", idx.get("optCount"))
        # 环境卡片是 dt（标签）/dd（值）成对出现：把“标签 → 值”折成 dict 再核对，
        # 比在一大串文本里找关键字靠谱（旧写法只收 dd，标签永远匹配不上）
        envpairs = idx.get("env") or []
        envmap = {}
        for k, v in zip(envpairs[0::2], envpairs[1::2]):
            envmap[str(k).strip()] = str(v).strip()
        check(envmap.get("引擎") == "go+wails3", "环境卡片：引擎 = go+wails3", envmap.get("引擎"))
        check(envmap.get("督休专员数") == str(EXPECTED_ROLE_COUNT),
              "环境卡片：督休专员数 = %d" % EXPECTED_ROLE_COUNT, envmap.get("督休专员数"))
        check(bool(envmap.get("督休专员目录")), "环境卡片：显示督休专员目录路径",
              envmap.get("督休专员目录"))
        check(bool(envmap.get("配置文件")), "环境卡片：显示配置文件落点", envmap.get("配置文件"))

        # ---- 会话 ----
        sid = m.get("sessionId") or ""
        check(isinstance(sid, str) and sid.startswith("short-"), "前端触发的会话 id 正常", sid)

        # ---- 提醒窗口（横屏 / 竖屏）----
        for key, label in (("breakWide", "横屏"), ("breakTall", "竖屏")):
            b = m.get(key) or {}
            check(b.get("shim") == "go", "%s提醒窗口：shim 生效" % label, b.get("shim"))
            check(b.get("hasSupervisor") is True, "%s提醒窗口：进入 has-supervisor 版式" % label, b.get("hasSupervisor"))
            check(b.get("figureHidden") is False, "%s提醒窗口：立绘已显示" % label, b.get("figureHidden"))
            figprefix = str(b.get("figureSrcPrefix", ""))
            check(figprefix.startswith("data:image/") and ";base64," in figprefix,
                  "%s提醒窗口：立绘来自 Go 的 data URL" % label, b.get("figureSrcPrefix"))
            nat = str(b.get("figureNatural") or "0x0")
            try:
                nw, nh = (int(x) for x in nat.split("x"))
            except Exception:
                nw, nh = 0, 0
            check(nw > 100 and nh > 100, "%s提醒窗口：立绘真的解码成功（浏览器解出像素尺寸）" % label, nat)
            check(b.get("lineVisible") is True, "%s提醒窗口：台词气泡可见" % label, b.get("lineVisible"))
            spk = b.get("speaker")
            if want_role:
                check(spk == want_role, "%s提醒窗口：说话人 == 配置固定督休专员的名字" % label,
                      "%s vs %s" % (spk, want_role))
            else:
                check(spk in role_names,
                      "%s提醒窗口：说话人 == 磁盘上某个督休专员的名字（非固定轮换模式）" % label, spk)
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
            # ---- 倒计时数字 vs 圆环：不越界（含每秒 1.22 倍弹跳峰值）+ 不能太小 ----
            # break.css 里字号由“环尺寸 × 位数”派生，此处现量 4 组（有/无督休专员 ×
            # 3/4 位、无专员 2/3 位）。cornerRatio = 数字框四角到环心距离 / 内径半径，
            # >1 就是探出圆环；colRatio = 环内整列（数字+百分比+两行题字）峰值高 / 内径直径。
            # 有专员版式环更小、环内其它元素字号更大，正是原先 3 位就溢出的场景。
            cds = b.get("countdownFit") or []
            check(len(cds) == 4, "%s提醒窗口：倒计时适配取到 4 组（有/无专员 × 位数）" % label, len(cds))
            try:
                want_digits = str(len(str(max(1, round((want_dur or 300000) / 1000)))))
            except Exception:
                want_digits = "3"
            check(b.get("countdownDigits") == want_digits,
                  "%s提醒窗口：位数按总时长写入（不是按当前数字，否则每秒字号都在变）" % label,
                  "%s vs %s" % (b.get("countdownDigits"), want_digits))
            for g in cds:
                tag = "%s提醒窗口：倒计时 %s 位%s" % (
                    label, g.get("digits"), "（有督休专员）" if g.get("hasSup") else "（无督休专员）")
                check((g.get("cornerRatio") or 9) <= 0.98,
                      tag + "——数字框在环内径圆内（含弹跳峰值）", g.get("cornerRatio"))
                check((g.get("colRatio") or 9) <= 1.0,
                      tag + "——环内整列不溢出（数字+百分比+两行题字）", g.get("colRatio"))
                ring = g.get("ring") or 0
                font = g.get("font") or 0
                ratio = (font / ring) if ring else 0
                floor = 0.28 if g.get("digits") == 3 else 0.20
                check(ratio >= floor,
                      tag + "——字号不小于环尺寸的 %d%%（不能为了不溢出缩成小字）" % int(floor * 100),
                      round(ratio, 3))
                check(font >= 40, tag + "——字号不低于 40px（可读下限）", font)
            if want_color:
                check(str(b.get("accent", "")).lower() == want_color.lower(),
                      "%s提醒窗口：--accent == 配置外观色" % label, "%s vs %s" % (b.get("accent"), want_color))
            check(b.get("badgeHidden") is True, "%s提醒窗口：真实休息不带预览角标（preview=false）" % label, b.get("badgeHidden"))
            # 立绘几何：内置专员统一“高度 80 / 距左 5 / 距底 0”（2026-10-28 用户要求）。
            # 这三个值是 break.js 从 meta.json 的 geom 写到 documentElement 上的，
            # 所以这一条直接证明“打包出去的那份配置真的按 80/5 渲染”。
            check(b.get("figVars") == ["80", "5", "0"],
                  "%s提醒窗口：立绘按 高度 80%% / 距左 5%% / 距底 0 渲染" % label, b.get("figVars"))

            # ---- 督休专员版式：游戏对话画面（底部全宽对话框），是 supervisorLayout 的核心 ----
            # 同一个会话，只换窗口尺寸（横 1100x720 / 竖 760x980），两档都应进入对话框
            # 版式：supMetrics.mode == "dialog"、名字标签亮出、倒计时环中心落在
            # “立绘右缘~窗口右缘”“对话框顶~窗口顶”两个中点的交点上、
            # “提前结束”在环下方与环中心对齐、对话框横贯全宽。
            sm = b.get("supMetrics") or {}
            check(sm.get("mode") == "dialog",
                  "%s提醒窗口：版式是游戏对话画面（supMetrics.mode == dialog）" % label, sm.get("mode"))
            check(isinstance(sm.get("figLeft"), int) and isinstance(sm.get("figRight"), int),
                  "%s提醒窗口：supMetrics 记录了立绘去透明后的左右缘" % label,
                  "%r / %r" % (sm.get("figLeft"), sm.get("figRight")))
            # 独立对拍（2026-10-14）：figLeft/figRight/figTop 必须与“图片自然像素
            # alpha bbox + .trim 计算变换矩阵”独立反推的可见边一致。这条断言不读
            # 页面的摆位公式，专门抓“trim 补偿几何算错”这类 bug——曾经的错法：
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
            ref_role = want_role or spk
            if ref_role:
                check(b.get("dialogName") == ref_role and b.get("dialogNameShown") is True,
                      "%s提醒窗口：名字标签亮在对话框左上角" % label,
                      "%r / %s（基准 %r）" % (b.get("dialogName"), b.get("dialogNameShown"), ref_role))
            # 督休专员题名（2026-10-30 改版）：竖排金色宋体，钉在立绘头部左侧的
            # 空白区，位置与字号都由渲染层按立绘 alpha 剖面实测，不再有固定 left，
            # 所以不能锁像素值。改锁三条语义约束：
            #   ① 已定位 + 竖排   ② 零遮挡（不压立绘）   ③ 字号不低于可读下限
            # “零遮挡”是这一版的核心承诺：渲染层把覆盖区内的不透明像素列数写进
            # data-rt-hit，探针只断言它为 0，不管题名具体落在哪个像素上。
            check(b.get("dialogNamePositioned") is True,
                  "%s提醒窗口：题名已完成按立绘的实测定位" % label,
                  "positioned=%s hit=%s" % (b.get("dialogNamePositioned"), b.get("dialogNameHit")))
            check(b.get("dialogNameVertical") is True,
                  "%s提醒窗口：题名是竖排（writing-mode: vertical-rl）" % label,
                  "%s" % b.get("dialogNameVertical"))
            check(b.get("dialogNameHit") == 0,
                  "%s提醒窗口：题名零遮挡（一个不透明像素都不压立绘）" % label,
                  "hit=%s fs=%s" % (b.get("dialogNameHit"), b.get("dialogNameFs")))
            _dfs = b.get("dialogNameFs")
            check(isinstance(_dfs, (int, float)) and _dfs >= 15,
                  "%s提醒窗口：题名字号 >= 15px（再小宋体在 1x 屏就发虚）" % label,
                  "%s" % _dfs)
            check(b.get("dialogNameOverflow") is False,
                  "%s提醒窗口：题名文字没有溢出牌面（红底完整包住名字）" % label,
                  "overflow=%s" % b.get("dialogNameOverflow"))
            # 红印泥牌底必须真的画出来（2026-10-30 实测踩坑）：底色若是
            # background-color，会被第一层 text 裁切吞掉，牌面实际透明 ——
            # 只有内阴影+内圈高光看着“像红底”，金主题下直接融进背景。
            # 只能断言计算值：双层 image + 双值 clip，两者缺一即红。
            _clip = (b.get("dialogNameClip") or "").replace(" ", "")
            check(_clip == "text,border-box",
                  "%s提醒窗口：题名牌的 background-clip 是双值列表（text + border-box）" % label,
                  "%r" % b.get("dialogNameClip"))
            check((b.get("dialogNameLayers") or 0) >= 2,
                  "%s提醒窗口：题名牌是双层背景（金字渐变 + 墨块底）" % label,
                  "layers=%s" % b.get("dialogNameLayers"))
            # 牌底必须是墨块图（url），不能退回纯色/渐变矩形：不规则轮廓是这一版的设计要求
            check(b.get("dialogNameInk") is True,
                  "%s提醒窗口：牌底是墨块图（印泥不规则轮廓）" % label,
                  "ink=%s" % b.get("dialogNameInk"))
            # 每次出场随机换一枚“章”：两个变量必须指向同一档，且该档文件真的在包里。
            # 只认“写死的 url”的旧实现永远报第 1 档，这条会红。
            _seal = b.get("dialogNameSeal") or {}
            _ink, _spk = str(_seal.get("ink") or ""), str(_seal.get("specks") or "")
            _mi = re.search(r"seal-ink-(\d+)\.svg", _ink)
            _ms = re.search(r"seal-specks-(\d+)\.svg", _spk)
            check(bool(_mi) and 1 <= int(_mi.group(1)) <= SEAL_VARIANTS,
                  "%s提醒窗口：牌底挑的是 1~%d 号印泥变体" % (label, SEAL_VARIANTS),
                  "ink=%s" % _ink)
            check(bool(_ms) and bool(_mi) and _ms.group(1) == _mi.group(1),
                  "%s提醒窗口：碎点与牌底是同一枚章（同一下标）" % label,
                  "ink=%s specks=%s" % (_ink, _spk))
            check(all(os.path.isfile(os.path.join(DIST, f)) for f in (_ink, _spk) if f),
                  "%s提醒窗口：选中的印泥/碎点文件在包里" % label,
                  "ink=%s specks=%s" % (_ink, _spk))
            # 碎点必须在 1x 下看得见 —— 这条只能量源文件的半径，因为“看不见”在截图里
            # 是 1px 的浅色点，任何像素断言都会抖。换算：牌子最窄 35px（2 字名），
            # ::after 画布 = 1.18 倍盒子 → 1 单位 = 0.413px，r=2.6 才够 2.1px 直径。
            # 上一版 r≤2.1（直径 1.7px）实测要放大 8 倍才看得出，等于没有。
            _spk_max = 0.0
            _spk_path = os.path.join(DIST, _spk) if _spk else ""
            if _spk_path and os.path.isfile(_spk_path):
                with open(_spk_path, encoding="utf-8") as _sf:
                    _svg = _sf.read()
                for _mm in re.finditer(r'<circle[^>]*\sr="([\d.]+)"', _svg):
                    _spk_max = max(_spk_max, float(_mm.group(1)))
                for _mm in re.finditer(r'<ellipse[^>]*\srx="([\d.]+)"', _svg):
                    _spk_max = max(_spk_max, float(_mm.group(1)))
            check(_spk_max >= 2.6,
                  "%s提醒窗口：碎点粒度够大（最窄 35px 牌子上 ≥2.1px 直径）" % label,
                  "max_r=%.2f in %s" % (_spk_max, _spk))
            _ai = _seal.get("afterInset")
            check(isinstance(_ai, (int, float)) and _ai < -1,
                  "%s提醒窗口：碎点层是比盒子大一圈的伪元素（负 inset）" % label,
                  "inset=%s content=%s" % (_ai, _seal.get("afterContent")))
            # 字外侧必须还有红：padding 要大于墨块的边界余量（横向 9% 盒宽、纵向 3.5% 盒高）。
            # 竖排 10 字（盒高 227）曾经是负的 —— 字头字脚压在背景上，肉眼看不出但像素能抓到。
            _rp = b.get("dialogNameRedPad") or {}
            _rx, _ry = _rp.get("x"), _rp.get("y")
            check(isinstance(_rx, (int, float)) and _rx >= 0.5,
                  "%s提醒窗口：文字左右两侧仍有余量（墨块横向盖得住）" % label,
                  "x=%s" % _rx)
            check(isinstance(_ry, (int, float)) and _ry >= 2.0,
                  "%s提醒窗口：文字上下仍有余量（墨块纵向盖得住）" % label,
                  "y=%s" % _ry)
            # 二阶段的“凑近 + 脉动”是 CSS scale，立绘绕“盒底边中点（双脚）”放大。
            # 题名牌必须绕同一个点放大：原点若留在牌心，8% 的缩放会把牌面往人物身上
            # 挪十几像素（实测 49px 的头部位移里，牌面只跟上 0，看上去牌子掉在原地）。
            _dz = b.get("dialogNameZoom") or {}
            _dx, _dy = _dz.get("dx"), _dz.get("dy")
            check(isinstance(_dx, (int, float)) and isinstance(_dy, (int, float))
                  and abs(_dx) <= 1.5 and abs(_dy) <= 1.5,
                  "%s提醒窗口：题名牌与立绘共用同一个缩放原点（一起凑近，不各放大各的）" % label,
                  "dx=%s dy=%s scale=%s" % (_dx, _dy, _dz.get("scale")))
            # scale 可能算成 "none"（没挂上缩放规则），不能直接 float() —— 那会抛异常
            # 把整份报告打挂，而不是记一条失败。
            _sc = str(_dz.get("scale") or "none").replace(",", " ").split()
            try:
                _sx = float(_sc[0])
            except (ValueError, IndexError):
                _sx = 0.0
            check(_sx > 1.0,
                  "%s提醒窗口：题名牌确实跟着立绘缩放（scale > 1）" % label,
                  "scale=%s" % _dz.get("scale"))
            # 用布局框（offset*，不含变换）判定：rect 里含 1.03 / 1.08 的“凑近”缩放，
            # 绕立绘双脚放大时牌子边缘本来就会外扩（立绘自己也一样），那不是布局错。
            # 这里要钉的是“求解出来就没出窗”——正是这条此前抓过极左立绘下牌子被钳边的问题。
            _lay = b.get("dialogNameLayout") or {}
            _dl, _dt = _lay.get("left"), _lay.get("top")
            _dr = (_dl + _lay.get("w")) if _dl is not None and _lay.get("w") is not None else None
            _db = (_dt + _lay.get("h")) if _dt is not None and _lay.get("h") is not None else None
            _vpw = b.get("viewportW") or 0
            _vph = b.get("viewportH") or 0
            check(None not in (_dl, _dt, _dr, _db) and _dl >= 0 and _dt >= 0
                  and _dr <= _vpw and _db <= _vph,
                  "%s提醒窗口：题名布局框完整落在窗口内（不含凑近缩放）" % label,
                  "rect=(%s,%s)-(%s,%s) 窗口 %sx%s" % (_dl, _dt, _dr, _db, _vpw, _vph))
            # 题名已搬出对话条：底边必须在对话框顶边以上（不能压到台词与倒计时）
            _ci = b.get("contentInline") or {}
            if _ci.get("rectBottom") is not None and _ci.get("rectH") is not None and _db is not None:
                _ci_top = _ci["rectBottom"] - _ci["rectH"]
                check(_db <= _ci_top + 1,
                      "%s提醒窗口：题名不侵入对话条（底边在对话框顶边以上）" % label,
                      "%s vs 对话框顶 %s" % (_db, _ci_top))
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
            # ---- 对话框内的“提醒内容”：四类提醒窗口必须同一套排版 ----
            # 用户报障（2026-10-30）：外卖窗口的提醒内容不居中、自定义提醒的字样式与
            # 短休/长休两个样。根因是两处各写各的（自定义 15px/400/opacity .72，外卖
            # 的 .content 是 display:block + text-align:left）。现在三者同挂 .dialog-text。
            _dt = b.get("dialogText") or {}
            check("dialog-text" in str(_dt.get("cls") or ""),
                  "%s提醒窗口：提醒内容挂在 .dialog-text（四类窗口共用一套排版）" % label,
                  _dt.get("cls"))
            check(_dt.get("align") == "center"
                  and isinstance(_dt.get("offCenter"), (int, float))
                  and abs(_dt["offCenter"]) <= 0.5,
                  "%s提醒窗口：提醒内容水平居中" % label,
                  "align=%s offCenter=%s" % (_dt.get("align"), _dt.get("offCenter")))
            check(_dt.get("weight") in ("700", "bold"),
                  "%s提醒窗口：提醒内容字重 700（与其它提醒窗口一致）" % label, _dt.get("weight"))
            _op = _dt.get("opacity")
            check(isinstance(_op, (int, float)) and _op >= 0.999,
                  "%s提醒窗口：提醒内容不透明（旧版自定义提醒是 0.72 的淡小字）" % label, _op)
            check(isinstance(_dt.get("fs"), (int, float)) and _dt["fs"] >= 15 - 0.01,
                  "%s提醒窗口：提醒内容字号不低于 15px 下限（不缩到看不清）" % label, _dt.get("fs"))
            _ovy = _dt.get("overflowY")
            check(isinstance(_ovy, (int, float)) and _ovy <= 1.0,
                  "%s提醒窗口：提醒内容不溢出对话框底边" % label,
                  "overflowY=%s boxH=%s tall=%s" % (_ovy, _dt.get("boxH"), _dt.get("tall")))
            # 溢出兜底：缩到 15px 仍装不下时必须让框长高（.tall），而不是把字裁掉
            check(_ovy is None or _ovy <= 1.0 or _dt.get("tall") is True,
                  "%s提醒窗口：装不下时走 .tall 长高（绝不裁字）" % label,
                  "overflowY=%s tall=%s" % (_ovy, _dt.get("tall")))
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
                # 环中心垂直锚点（2026-10-13 新规格）=“立绘可见上缘(figTop, 去透明) 与 框顶”的中点，
                # 不再是“窗口顶与框顶”的中点；二阶段立绘缩放不改变锚点（layoutSupDialog 冻结）。
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
            if sk.get("shown"):
                # “提前结束”固定在窗口顶部正中，与预览模式的 #preview-badge 同一位置
                # （top:18px 是元素顶边），不再跟随倒计时环。
                check(abs((sk.get("rectTop") or 0) - 18) <= 2,
                      "%s提醒窗口：“提前结束”贴在窗口顶部（与预览提示同位）" % label,
                      "rectTop=%s" % sk.get("rectTop"))
                if vpw:
                    skip_cx = ((sk.get("rectLeft") or 0) + (sk.get("rectRight") or 0)) / 2
                    check(abs(skip_cx - vpw / 2) <= 3,
                          "%s提醒窗口：“提前结束”水平居中" % label,
                          "%s vs %s" % (round(skip_cx), vpw / 2))
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
        check(pb.get("pauseShown") is True, "提前提示窗：内置版式带“暂停计时”", pb.get("pauseShown"))
        check(pb.get("postponeShown") is True,
              "提前提示窗：默认配置（可推迟>0）显示“推迟”按钮", pb.get("postponeShown"))
        check(pb.get("buttons") == ["立即休息", "推迟休息", "跳过休息", "暂停计时"],
              "提前提示窗：四个按钮文案（内置版式）", pb.get("buttons"))
        check(not (pb.get("consoleErrors") or []), "提前提示窗：页面无控制台错误",
              (pb.get("consoleErrors") or [])[:2])
        check((pb.get("appHeight") or 0) >= 200, "提前提示窗：240 高窗口里内容铺得开", pb.get("appHeight"))

        pbc = m.get("preBreakCustom") or {}
        check(pbc.get("shim") == "go", "自定义提醒版式：shim 生效", pbc.get("shim"))
        check(pbc.get("title") == "喝水提醒时间到",
              "自定义提醒版式：标题是“XX时间到”（来自 Go 的 PreBreakState）", pbc.get("title"))
        check(pbc.get("post") == " 秒显示喝水提醒",
              "自定义提醒版式：倒计时后缀带提醒名（来自 URL 的 tail）", repr(pbc.get("post")))
        check(pbc.get("tipsShown") is False, "自定义提醒版式：不显示两行说明", pbc.get("tipsShown"))
        check(pbc.get("pauseShown") is False, "自定义提醒版式：没有“暂停计时”", pbc.get("pauseShown"))
        check(pbc.get("postponeShown") is True,
              "自定义提醒版式：默认配置（可推迟>0）显示“推迟”按钮", pbc.get("postponeShown"))
        # “暂停计时”是被加 .hidden 藏起来的（元素还在 DOM 里），所以这里仍是 4 项；
        # 它到底露没露由上面的 pauseShown 断言。
        check(pbc.get("buttons") == ["立即提醒", "推迟提醒", "跳过提醒", "暂停计时"],
              "自定义提醒版式：三颗按钮都改说“提醒”", pbc.get("buttons"))
        check(not (pbc.get("consoleErrors") or []), "自定义提醒版式：页面无控制台错误",
              (pbc.get("consoleErrors") or [])[:2])
        check((pbc.get("appHeight") or 0) >= 140, "自定义提醒版式：172 高窗口里内容铺得开",
              pbc.get("appHeight"))

        # ---- 提前提示窗：底色跟随“提醒窗口 → 背景颜色”+ 文字自动配色 ----
        # 底色由主进程下发（PreBreakInfo.BgColor ← appearance.color），渲染层再按它的
        # 亮度决定浅底深字 / 深底浅字。只断言“颜色变了”拦不住“深字配深底”，
        # 所以这里按 WCAG 真算对比度。
        pb_lum = _rel_lum_hex(pb.get("bgSource"))
        check(bool(pb.get("bgSource")) and pb_lum is not None,
              "提前提示窗：底色取自“提醒窗口 → 背景颜色”（不是写死的深色）", pb.get("bgSource"))
        check(pb_lum is not None and pb.get("lumMode") == ("light" if pb_lum >= 0.45 else "dark"),
              "提前提示窗：深浅档位与底色亮度自洽", [pb.get("lumMode"), pb_lum])
        pb_ct = pb.get("contrast") or {}
        check((pb_ct.get("text") or 0) >= 4.5, "提前提示窗：标题文字对底色对比度 ≥4.5", pb_ct.get("text"))
        check((pb_ct.get("textTop") or 0) >= 4.5, "提前提示窗：渐变上端文字同样看得清", pb_ct.get("textTop"))
        check((pb_ct.get("num") or 0) >= 3.5, "提前提示窗：倒计时数字对底色对比度 ≥3.5", pb_ct.get("num"))
        # 两行说明是固定的红 / 绿语义色，只在深浅两档之间换 —— 深底上糊成一片就等于没有
        check((pb_ct.get("tipBad") or 0) >= 4.0, "提前提示窗：红字说明对底色对比度 ≥4", pb_ct.get("tipBad"))
        check((pb_ct.get("tipGood") or 0) >= 4.0, "提前提示窗：绿字说明对底色对比度 ≥4", pb_ct.get("tipGood"))
        # 主按钮是主色当底、文字按底色亮度反选：按钮自己必须“从面板里跳出来”，
        # 上面的字也得读得清（浅色配置下这条最容易翻车）
        check((pb_ct.get("primaryBtn") or 0) >= 3.5,
              "提前提示窗：主按钮文字对按钮底对比度 ≥3.5", pb_ct.get("primaryBtn"))

        # 亮色配置（亮黄）：必须整窗换成浅底 + 深字，而不是继续沿用深底那套浅色文字
        pbl = m.get("preBreakLightBg") or {}
        check(pbl.get("bgSource") == "#ffd400", "浅色背景色：底色就是配置里的颜色", pbl.get("bgSource"))
        check(pbl.get("lumMode") == "light", "浅色背景色：判成浅底（走深字那一档）", pbl.get("lumMode"))
        check(pbl.get("tipsShown") is True,
              "浅色背景色：内置版式的两行说明仍在（这条就是要验它们看得清）", pbl.get("tipsShown"))
        check(pbl.get("textColor") != pb.get("textColor"),
              "浅色背景色：文字色跟着反转（与深底那一档不同）",
              [pbl.get("textColor"), pb.get("textColor")])
        pbl_ct = pbl.get("contrast") or {}
        check((pbl_ct.get("text") or 0) >= 4.5, "浅色背景色：标题文字对浅底对比度 ≥4.5", pbl_ct.get("text"))
        check((pbl_ct.get("num") or 0) >= 3.5, "浅色背景色：倒计时数字对浅底对比度 ≥3.5", pbl_ct.get("num"))
        check((pbl_ct.get("tipBad") or 0) >= 4.0, "浅色背景色：红字说明对浅底对比度 ≥4", pbl_ct.get("tipBad"))
        check((pbl_ct.get("tipGood") or 0) >= 4.0, "浅色背景色：绿字说明对浅底对比度 ≥4", pbl_ct.get("tipGood"))
        check((pbl_ct.get("primaryBtn") or 0) >= 3.5,
              "浅色背景色：主按钮文字对按钮底对比度 ≥3.5", pbl_ct.get("primaryBtn"))
        check(not (pbl.get("consoleErrors") or []), "浅色背景色：页面无控制台错误",
              (pbl.get("consoleErrors") or [])[:2])

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
              "外卖窗口：“推迟”按钮带时长文案", tk.get("postponeBtn"))
        check(tk.get("postponeShown") is True,
              "外卖窗口：默认配置（可推迟>0）显示“推迟”按钮", tk.get("postponeShown"))
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
            # 提醒内容：曾经因为 .content 是 display:block + text-align:left，整句贴
            # 在对话框左边；字号虽对但不会缩，长文本把框撑成两倍高。
            _tdt = tk.get("dialogText") or {}
            check("dialog-text" in str(_tdt.get("cls") or ""),
                  "外卖窗口：提醒内容挂在 .dialog-text（与休息窗口同一套）", _tdt.get("cls"))
            check(_tdt.get("align") == "center"
                  and isinstance(_tdt.get("offCenter"), (int, float))
                  and abs(_tdt["offCenter"]) <= 0.5,
                  "外卖窗口：提醒内容水平居中",
                  "align=%s offCenter=%s" % (_tdt.get("align"), _tdt.get("offCenter")))
            check(_tdt.get("contentAlign") == "center",
                  "外卖窗口：.content 的 text-align 也是 center",
                  _tdt.get("contentAlign"))
            check(tk.get("badgesJustify") == "center",
                  "外卖窗口：状态徽标行也居中（block 父级管不到 flex 子项）",
                  tk.get("badgesJustify"))
            check(_tdt.get("weight") in ("700", "bold"),
                  "外卖窗口：提醒内容字重 700（与休息窗口一致）", _tdt.get("weight"))
            _top = _tdt.get("opacity")
            check(isinstance(_top, (int, float)) and _top >= 0.999,
                  "外卖窗口：提醒内容不透明", _top)
            _tovy = _tdt.get("overflowY")
            check(isinstance(_tovy, (int, float)) and _tovy <= 1.0,
                  "外卖窗口：提醒内容不溢出对话框底边",
                  "overflowY=%s boxH=%s tall=%s" % (_tovy, _tdt.get("boxH"), _tdt.get("tall")))
            check(isinstance(_tdt.get("fs"), (int, float)) and _tdt["fs"] >= 15 - 0.01,
                  "外卖窗口：提醒内容字号不低于 15px 下限", _tdt.get("fs"))
            if info.get("message"):
                check(tk.get("lineText") == info.get("message") and tk.get("lineShown") is True,
                      "外卖窗口：台词 == 外卖提醒文字（不显示督休专员自己的台词）",
                      "%r vs %r" % (tk.get("lineText"), info.get("message")))
            tk_role = want_role or tk.get("speaker")
            if tk_role:
                check(tk.get("dialogName") == tk_role and tk.get("dialogNameShown") is True,
                      "外卖窗口：题名已显示（钉在立绘头部左侧，不再挂在对话框沿上）",
                      "%r / %s（基准 %r）" % (tk.get("dialogName"), tk.get("dialogNameShown"), tk_role))
                # 与休息窗口同一套约束：竖排 + 已定位 + 零遮挡 + 字号达标
                check(tk.get("dialogNameVertical") is True,
                      "外卖窗口：题名是竖排（writing-mode: vertical-rl）",
                      tk.get("dialogNameVertical"))
                check(tk.get("dialogNamePositioned") is True,
                      "外卖窗口：题名已完成按立绘的实测定位",
                      "positioned=%s hit=%s" % (tk.get("dialogNamePositioned"), tk.get("dialogNameHit")))
                check(tk.get("dialogNameHit") == 0,
                      "外卖窗口：题名零遮挡（一个不透明像素都不压立绘）",
                      "hit=%s fs=%s" % (tk.get("dialogNameHit"), tk.get("dialogNameFs")))
                _tkfs = tk.get("dialogNameFs")
                check(isinstance(_tkfs, (int, float)) and _tkfs >= 15,
                      "外卖窗口：题名字号 >= 15px（再小宋体在 1x 屏就发虚）", "%s" % _tkfs)
                check(tk.get("dialogNameOverflow") is False,
                      "外卖窗口：题名文字没有溢出牌面（红底完整包住名字）",
                      "overflow=%s" % tk.get("dialogNameOverflow"))
                # 与休息窗口同一条：牌底必须是第二层 background-image + 双值 clip
                _tkclip = (tk.get("dialogNameClip") or "").replace(" ", "")
                check(_tkclip == "text,border-box",
                      "外卖窗口：题名牌的 background-clip 是双值列表（text + border-box）",
                      "%r" % tk.get("dialogNameClip"))
                check((tk.get("dialogNameLayers") or 0) >= 2,
                      "外卖窗口：题名牌是双层背景（金字渐变 + 墨块底）",
                      "layers=%s" % tk.get("dialogNameLayers"))
                check(tk.get("dialogNameInk") is True,
                      "外卖窗口：牌底是墨块图（印泥不规则轮廓）",
                      "ink=%s" % tk.get("dialogNameInk"))
                _tseal = tk.get("dialogNameSeal") or {}
                _tink, _tspk = str(_tseal.get("ink") or ""), str(_tseal.get("specks") or "")
                _ti = re.search(r"seal-ink-(\d+)\.svg", _tink)
                _ts = re.search(r"seal-specks-(\d+)\.svg", _tspk)
                check(bool(_ti) and 1 <= int(_ti.group(1)) <= SEAL_VARIANTS
                      and bool(_ts) and _ts.group(1) == _ti.group(1),
                      "外卖窗口：牌底与碎点是同一枚随机印章",
                      "ink=%s specks=%s" % (_tink, _tspk))
                check(all(os.path.isfile(os.path.join(DIST, f)) for f in (_tink, _tspk) if f),
                      "外卖窗口：选中的印泥/碎点文件在包里",
                      "ink=%s specks=%s" % (_tink, _tspk))
                _trp = tk.get("dialogNameRedPad") or {}
                check(isinstance(_trp.get("x"), (int, float)) and _trp.get("x") >= 0.5
                      and isinstance(_trp.get("y"), (int, float)) and _trp.get("y") >= 2.0,
                      "外卖窗口：文字四周仍有余量（墨块盖得住）",
                      "x=%s y=%s" % (_trp.get("x"), _trp.get("y")))
                _tz = tk.get("dialogNameZoom") or {}
                _tdx, _tdy = _tz.get("dx"), _tz.get("dy")
                check(isinstance(_tdx, (int, float)) and isinstance(_tdy, (int, float))
                      and abs(_tdx) <= 1.5 and abs(_tdy) <= 1.5,
                      "外卖窗口：题名牌与立绘共用同一个缩放原点（同一套几何）",
                      "dx=%s dy=%s scale=%s" % (_tdx, _tdy, _tz.get("scale")))
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
                    # （2026-10-14 终版：立绘上缘与“显示督休专员名称的对话框顶”两线中点；
                    # 之前误用窗顶 y=0）。“外卖提醒”标题浮在框外上方，框贴顶会把标题顶
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
                    # 标题可见：“外卖提醒”胶囊整枚落在窗口内（曾被贴顶红包顶出窗外）
                    rt = tk.get("rpTitleInline") or {}
                    check(bool(rt.get("shown")) and (rt.get("rectTop") or -99) >= 0
                          and (rt.get("rectBottom") or -99) <= (rp.get("rectTop") or 0),
                          "外卖窗口：“外卖提醒”标题完整可见（窗口内且悬于框上方）",
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

        # ---- “可推迟”为 0：提前提示窗与外卖窗口都不显示“推迟”按钮 ----
        # 场景由 renderer_probe.js 的 5b 段造出：short.postponeMs=0、takeout.postponeMin=0，
        # URL 带 postpone=0（真实链路里主进程生成的地址同样带这个参数）
        pbz = m.get("preBreakNoPostpone") or {}
        check(pbz.get("postponeShown") is False,
              "可推迟为 0：提前提示窗不显示“推迟”按钮", pbz.get("postponeShown"))
        # 按钮是被 .hidden 藏起来的（元素与文案都还在 DOM 里），不是被删掉。
        # 文案用词兼容内置 / 自定义两种版式（轮询会把版式切成上一次记住的 custom）
        check(any("推迟" in b for b in (pbz.get("buttons") or [])),
              "可推迟为 0：提前提示窗“推迟”按钮只是被隐藏（DOM 与文案仍在）",
              pbz.get("buttons"))
        check(not (pbz.get("consoleErrors") or []),
              "可推迟为 0：提前提示窗页面无控制台错误",
              (pbz.get("consoleErrors") or [])[:2])
        tkz = m.get("takeoutNoPostpone") or {}
        check(tkz.get("postponeShown") is False,
              "可推迟为 0：外卖窗口不显示“推迟”按钮", tkz.get("postponeShown"))
        check(str(tkz.get("postponeBtn") or "").startswith("推迟"),
              "可推迟为 0：外卖窗口“推迟”按钮只是被隐藏（文案仍在）",
              tkz.get("postponeBtn"))
        check(not (tkz.get("consoleErrors") or []),
              "可推迟为 0：外卖窗口页面无控制台错误",
              (tkz.get("consoleErrors") or [])[:2])

        # ---- 全局键鼠活动 → 督休专员二阶段（系统级检测的可观测端）----
        # 二阶段的触发源必须是“系统级键鼠活动”，不能只在提醒窗口内监听：
        # Go 侧 sup:active 广播（takeout.go 轮询 user32!GetLastInputInfo）→ shim on()
        # → break.js noteActivity。断言分两步：
        #   ① 事件真的到达页面（__WR_SUP_ACTIVE 计数增加）—— 这条是链路证明；
        #   ② 页面把它当二阶段处理（data-stage 升到 2）。
        # 不校验“触发前一阶段”：探针跑在真机上，用户自己的键鼠活动也会产生
        # sup:active（这正是本功能要的），先验状态不可控。
        ga = m.get("globalActivity") or {}
        check(not ga.get("error"), "提醒窗口：SupActivity 绑定调用成功", ga.get("error"))
        n0 = ga.get("pulsesBefore")
        n1 = ga.get("pulsesAfter")
        check(isinstance(n0, int) and isinstance(n1, int) and n1 > n0,
              "提醒窗口：全局键鼠活动广播（sup:active）真的到达页面", "%s -> %s" % (n0, n1))
        check(ga.get("stage") == "2",
              "提醒窗口：全局键鼠活动把督休专员推进二阶段", ga.get("stage"))
        # 二阶段立绘放大 8%（头顶抬高约 50px）。题名牌的相对位置必须不变 ——
        # 归一化到立绘盒高后比较，窗口尺寸与缩放都自动消掉。容差 0.004 ≈ 2.6px
        # （留给 1.1s 脉动相位差与 scale/transform 的合成顺序，那点误差 < 1px）。
        _pv0, _pv1 = ga.get("plateVBefore"), ga.get("plateVAfter")
        check(isinstance(_pv0, (int, float)) and isinstance(_pv1, (int, float))
              and abs(_pv1 - _pv0) <= 0.004,
              "提醒窗口：进入二阶段后题名牌与立绘同步凑近（相对位置不漂移）",
              "%s -> %s" % (_pv0, _pv1))
        # 二阶段的命令台词比一阶段的提醒内容长，但对话框高度必须恒定：以前是
        # min-height，台词换行就把框撑高、倒计时环跟着位移（用户报障 2026-10-25）。
        check(abs((ga.get("boxHAfter") or 0) - (ga.get("boxHBefore") or 0)) <= 0.5,
              "提醒窗口：进入二阶段后对话框高度不变",
              "%s -> %s（台词 %s 字）" % (ga.get("boxHBefore"), ga.get("boxHAfter"), ga.get("lineChars")))
        # 台词在框内居中偏上：负偏移＝文字中心高于框中心（不允许压到框底）
        off = ga.get("lineOffset")
        check(isinstance(off, (int, float)) and -40 <= off <= 0,
              "提醒窗口：台词落在对话框中线以上且不贴底", "偏移 %spx" % off)

        # ---- 禁缩放守卫（提醒窗口 / 预览窗口）----
        # 2026-10-25 用户报障：触屏两指捏合会把窗口内容放大缩小。三个页面都引了
        # web/dist/nozoom.js（touch-action 去 pinch-zoom + 拦 Ctrl 滚轮/键盘），
        # Go 侧另有 --disable-pinch（浏览器进程级，无头探针验不到，只能在真机看）。
        # 这里锁住“守卫装上了、入口都堵住、普通滚轮没被误伤”这三件事。
        nz = m.get("nozoom") or {}
        if not nz:
            check(False, "禁缩放守卫采集到数据（nozoom）", "缺失")
        for key, label in (("prebreak", "提前提示窗"), ("break", "提醒窗口"), ("takeout", "外卖窗口")):
            d = nz.get(key) or {}
            check(d.get("loaded") is True, "%s：已加载禁缩放守卫（__WR_NOZOOM）" % label, d.get("loaded"))
            # touch-action 里不能有 pinch-zoom：pan-x pan-y 允许平移/滚动，禁掉捏合
            check(d.get("touchAction") == "pan-x pan-y",
                  "%s：<html> 的 touch-action 去掉了 pinch-zoom" % label, d.get("touchAction"))
            check(d.get("ctrlWheelBlocked") is True,
                  "%s：Ctrl+滚轮（触控板捏合）被拦下" % label, d.get("ctrlWheelBlocked"))
            # 普通滚轮必须放行：break.js / takeout.js 靠 wheel 记用户活动，
            # 拦死会让倒计时把“还在用电脑”误判成“离开”
            check(d.get("plainWheelPassed") is True,
                  "%s：普通滚轮照旧放行（活动上报不受影响）" % label, d.get("plainWheelPassed"))
            check(d.get("ctrlKeyBlocked") is True,
                  "%s：Ctrl+'+' 键盘缩放被拦下" % label, d.get("ctrlKeyBlocked"))
            check(d.get("multiTouchBlocked") is True,
                  "%s：双指触摸（捏合的原始输入）被拦下" % label, d.get("multiTouchBlocked"))

        # ---- 静态不变式：四类提醒窗口共用同一套提醒内容排版 ----
        # 运行时断言只覆盖探针真正打开的那两种窗口（休息 / 外卖）。自定义提醒与
        # 短休/长休共用同一套 CSS，所以这里再从源码层面钉一遍“共用”本身：
        #   · 两份 CSS 的 body.has-supervisor .dialog-text 声明块逐字相同；
        #   · 两份 CSS 的 --dlg-fs / --dlg-fs-min / --dlg-h 三个值相同；
        #   · 两份 JS 的 fitDialogText() 逐字相同（缩字号 + .tall 长高的唯一实现）；
        #   · 三个元素都真的挂上了 .dialog-text（漏一个就退回各写各的）。
        def _read(rel):
            with open(os.path.join(DIST, rel), encoding="utf-8") as f:
                return f.read().replace("\r\n", "\n")

        def _block(src, head, close="\n}"):
            """从 head 起截到“行首 close”为止。CSS 声明块收在行首的 }，
            JS 函数收在两格缩进的 }（close="\n  }"）—— 用错会把后面整份文件都算进去。"""
            i = src.find(head)
            if i < 0:
                return None
            j = src.find(close, i)
            return None if j < 0 else src[i:j + len(close)].strip()

        try:
            _bc, _tc = _read("break.css"), _read("takeout.css")
            _bj, _tj = _read("break.js"), _read("takeout.js")
            _bh, _th = _read("break.html"), _read("takeout.html")
        except Exception as _e:      # 文件缺失时给一条失败而不是把报告打挂
            _bc = _tc = _bj = _tj = _bh = _th = ""
            check(False, "提醒内容排版：两份 CSS / 两份 JS / 两份 HTML 都能读到", _e)

        _d1 = _block(_bc, "body.has-supervisor .dialog-text {")
        _d2 = _block(_tc, "body.has-supervisor .dialog-text {")
        check(bool(_d1) and _d1 == _d2,
              "提醒内容排版：两份 CSS 的 .dialog-text 声明块逐字相同",
              "break=%s takeout=%s" % (bool(_d1), bool(_d2)))
        for _k in ("--dlg-fs:", "--dlg-fs-min:", "--dlg-h:"):
            _v1 = re.search(re.escape(_k) + r"\s*([^;]+);", _bc)
            _v2 = re.search(re.escape(_k) + r"\s*([^;]+);", _tc)
            check(bool(_v1) and bool(_v2) and _v1.group(1).strip() == _v2.group(1).strip(),
                  "提醒内容排版：两份 CSS 的 %s 同值" % _k.rstrip(":"),
                  "%s vs %s" % (_v1 and _v1.group(1).strip(), _v2 and _v2.group(1).strip()))
        _f1 = _block(_bj, "  function fitDialogText(content) {", "\n  }")
        _f2 = _block(_tj, "  function fitDialogText(content) {", "\n  }")
        check(bool(_f1) and _f1 == _f2,
              "提醒内容排版：两份 JS 的 fitDialogText() 逐字相同",
              "break=%s takeout=%s" % (len(_f1 or ""), len(_f2 or "")))
        check("--dlg-fs-min" in (_f1 or "") and "classList.add(\"tall\")" in (_f1 or ""),
              "提醒内容排版：自适应逻辑同时有“15px 下限”与“.tall 长高”两条出口",
              "min=%s tall=%s" % ("--dlg-fs-min" in (_f1 or ""),
                                  'classList.add("tall")' in (_f1 or "")))
        check('class="supervisor-line dialog-text' in _bh,
              "提醒内容排版：break.html 的台词挂 .dialog-text")
        check('class="messages dialog-text' in _bh,
              "提醒内容排版：break.html 的自定义提醒内容挂 .dialog-text")
        check('class="supervisor-line dialog-text' in _th,
              "提醒内容排版：takeout.html 的台词挂 .dialog-text")

        # ---- 不启用督休专员：四类窗口的提醒内容排版 ----
        # 这一组走替身 shim（supervisor:null），走的是同一份 HTML/CSS/JS。
        # 运行时断言：四类的字号/字重/透明度/对齐完全一致，居中、不越界；
        # 外卖另外钉块序（红包 → 徽标 → 内容 → 按钮）与“外卖提醒”标题不压内容。
        _ns = m.get("noSup") or {}
        check(set(_ns.keys()) == {"break_short", "break_custom", "takeout_qr", "takeout_noqr"},
              "无督休专员：四类窗口都量到了", sorted(_ns.keys()))
        _base = _ns.get("break_short") or {}
        for _k, _v in sorted(_ns.items()):
            check(_v.get("error") is None, "无督休专员/%s：量到可见的提醒内容" % _k, _v.get("error"))
            check(_v.get("hasSup") is False, "无督休专员/%s：确实没有 has-supervisor" % _k, _v.get("hasSup"))
            check(_v.get("weight") == "600" and _v.get("opacity") == 1,
                  "无督休专员/%s：字重 600、不透明" % _k,
                  "%s / %s" % (_v.get("weight"), _v.get("opacity")))
            check(_v.get("align") == "center", "无督休专员/%s：水平居中（text-align）" % _k, _v.get("align"))
            # offCenter 是个“0 就是合格”的值，不能写成 `or 99` —— 0 是 falsy，
            # 会取到兜底的 99 把“刚好居中”判成失败（已踩过一次）。
            _oc = _v.get("offCenter")
            check(isinstance(_oc, (int, float)) and abs(_oc) <= 0.5,
                  "无督休专员/%s：文字中心与窗口中线重合" % _k, _oc)
            check(_v.get("overX") == 0 and _v.get("overY") == 0,
                  "无督休专员/%s：文字不越出窗口" % _k,
                  "x=%s y=%s" % (_v.get("overX"), _v.get("overY")))
            check(_v.get("wrap") == "anywhere",
                  "无督休专员/%s：长串可折行（overflow-wrap）" % _k, _v.get("wrap"))
            if _base and _base.get("fs"):
                check(abs((_v.get("fs") or 0) - _base["fs"]) < 0.01,
                      "无督休专员/%s：字号与休息窗口一致" % _k,
                      "%s vs %s" % (_v.get("fs"), _base.get("fs")))
            _ce = _v.get("consoleErrors") or []
            check(not _ce, "无督休专员/%s：无 console 错误" % _k, _ce[:2])
        # 外卖块序：红包 → 徽标 → 提醒内容 → 按钮，且“外卖提醒”标题不压内容
        for _k in ("takeout_qr", "takeout_noqr"):
            _v = _ns.get(_k) or {}
            check(_v.get("orderAsc") is True,
                  "无督休专员/%s：块序为 红包→徽标→内容→按钮" % _k, _v.get("order"))
            check(all((_g or 0) > 0 for _g in (_v.get("gaps") or [0])),
                  "无督休专员/%s：相邻块依次向下排列，无重叠" % _k, _v.get("gaps"))
        _qr = _ns.get("takeout_qr") or {}
        check(_qr.get("titleOverlap") == 0,
              "无督休专员/外卖：“外卖提醒”标题未压到提醒内容", _qr.get("titleOverlap"))
        check((_qr.get("titleGap") or -99) > 0,
              "无督休专员/外卖：标题与提醒内容之间留有空隙", _qr.get("titleGap"))
        _noqr = _ns.get("takeout_noqr") or {}
        check(_qr.get("fs") and _qr.get("fs") == _noqr.get("fs"),
              "无督休专员/外卖：红包开不开不影响字号",
              "%s vs %s" % (_qr.get("fs"), _noqr.get("fs")))

        # 源码级：无督休专员那一段也是“只定义一次”
        _n1 = _block(_bc, "body:not(.has-supervisor) .dialog-text {")
        _n2 = _block(_tc, "body:not(.has-supervisor) .dialog-text {")
        check(bool(_n1) and _n1 == _n2,
              "无督休专员：两份 CSS 的 .dialog-text 声明块逐字相同",
              "break=%s takeout=%s" % (len(_n1 or ""), len(_n2 or "")))
        check(_n1 and "font-size" in _n1 and "overflow-wrap" in _n1,
              "无督休专员：共享块里同时给了字号与折行", bool(_n1))
        # 三处元素不再各写一份字号（写过就又会岔开）
        _msgrule = _block(_bc, "body:not(.has-supervisor) .messages {")
        check(bool(_msgrule) and "font-size" not in _msgrule and "font-weight" not in _msgrule,
              "无督休专员：break.css 的 .messages 不再自己写字号字重", _msgrule is not None)
        _tkmsg = _block(_tc, ".tk-msg {")
        check(bool(_tkmsg) and "font-size" not in _tkmsg and "opacity" not in _tkmsg,
              "无督休专员：takeout.css 的 .tk-msg 不再自己写字号透明度", _tkmsg is not None)
        check('class="tk-msg dialog-text' in _th,
              "无督休专员：takeout.html 的提醒内容挂 .dialog-text")

        # ---- 截图 ----
        # 提前提示窗是 400x240 / 400x172 的小窗，字节数天然比整页截图少，阈值单独给
        for name, min_sz in (
            ("settings.png", 20000), ("settings_timing.png", 20000), ("settings_supervisor.png", 20000),
            ("settings_stats.png", 20000), ("settings_shortcuts.png", 20000), ("settings_about.png", 20000),
            ("index.png", 20000), ("break_wide.png", 20000), ("break_tall.png", 20000),
            ("prebreak.png", 4000), ("prebreak_custom.png", 4000), ("takeout.png", 4000),
            ("prebreak_no_postpone.png", 4000), ("takeout_no_postpone.png", 4000),
            # 不启用督休专员的两张：整页深色渐变，与 takeout 同量级
            ("nosup_break_short.png", 20000), ("nosup_takeout_qr.png", 20000),
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
