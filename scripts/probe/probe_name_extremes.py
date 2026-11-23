#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""题名 + 墨块「极端用例」探针（判定端 + 立绘生成端）。

为什么需要这一份（与另外两个题名探针的分工见 name_extremes.js 头部）：
  · probe_role_title.py        只跑 15 位内置立绘 —— alpha 形状、宽高比都在“正常范围”
  · probe_role_title_stress.py 只换名字，立绘仍是内置的一张
本探针把**立绘本身**换成用户可能导入的极端图（全透明 / 无 alpha / 1×1 / 1600×2200 /
全景 / 只有下半身 / 人物贴左 / 人物靠右 / 6px 细条 / 坏文件 / 翻转 / 隐藏名称），
再把窗口拖到 420×300、改 DPI、二阶段缩放、连续 resize，验“位置真按像素算”。

立绘经 `WR_SUPERVISORS_DIR` 指向临时目录 —— 与用户导入走同一条
ScanRoles → ToDataURL → <img src> 路径，不为测试开后门。

断言分三层：
  A. 已定位的**每一条**：整块在窗内、不压对话框、不越对话框、单列、不溢出、
     字号 15~26、重叠 ≤ 20%、墨块 viewBox == 牌面实测像素、层序牌 > 立绘、
     牌心锚点（未被迫 clamp 时 0.17±0.02）、横向缝（slack ≥ 32 时必须 ≥ 32）、
     省略必须是**字素簇**边界（不能把 🇨🇳 拆成两个字母、把家庭 emoji 拆成单人）。
  B. 未定位的每一条：干净隐藏 —— 不残留半截牌、文本恢复原名、无 JS 错误。
  C. 元断言：有立绘、有头部的角色在正常窗口下必须真的有名字显示；靠右人物必须
     用上 32px 舒适缝；200 字重排不能卡住窗口；DPI 1.25/2/3 与 1x 几何一致；
     resize 风暴后终态与直接重排一致；SealGen 缺失时退回 CSS 纯色墨块。

用法：python scripts/probe/probe_name_extremes.py [--role 极端正常] [--keep]
退出码 0 = 全部通过。
"""

import argparse
import hashlib
import json
import math
import os
import shutil
import socket
import struct
import subprocess
import sys
import tempfile
import time
import urllib.request
import zlib

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(os.path.dirname(HERE))
EXE = os.path.join(ROOT, "build", "bin", "rest_server.exe")
FACTORY = os.path.join(ROOT, "internal", "settings", "default_settings.json")
SUP_DIR = os.path.join(ROOT, "assets", "supervisors")
PROBE_JS = os.path.join(HERE, "name_extremes.js")
OUT = os.path.join(tempfile.gettempdir(), "wr_name_extremes")
BASE_ROLE = "医生"

try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass

# ---- 统计 ----
S = {
    "checks": 0, "fail": 0, "rows": 0, "pos": 0, "hidden": 0,
    "fs_min": 99.0, "fs_max": 0.0, "ov_max": 0.0, "sync_max": 0.0,
    "elided": 0, "toe": 0, "gap32": 0, "clean_hide": 0,
    # 牌底到对话框顶的最小渲染间隙（RT_GAP 口径 12px）。以前这条判据用布局坐标去减
    # 渲染坐标，间隙会凭空少几像素，全靠 9.5 这个魔数盖着；现在记最小值，压到 12 以下
    # 一眼就能看见。
    "gap_min": 1e9,
}
FAILS = []
HIDE_BY_ROLE = {}


def check(ok, label, detail="", quiet=False):
    S["checks"] += 1
    if ok:
        return True
    S["fail"] += 1
    FAILS.append("%s  — %s" % (label, detail))
    if not quiet or S["fail"] <= 80:
        print("  [FAIL] %s%s" % (label, ("  — %s" % detail) if detail else ""))
    return False


# ─────────────────────────── PNG 生成 ───────────────────────────

COLOR = (206, 188, 158, 255)


def _chunk(typ, data):
    return (struct.pack(">I", len(data)) + typ + data +
            struct.pack(">I", zlib.crc32(typ + data) & 0xFFFFFFFF))


def write_png(path, w, h, spans):
    """spans(y) -> [(x0, x1), ...]（含端点，前景色不透明，其余全透明）。"""
    raw = bytearray()
    fg = bytes(COLOR)
    for y in range(h):
        row = bytearray(w * 4)
        for x0, x1 in spans(y) or []:
            a = max(0, int(x0))
            b = min(w - 1, int(x1))
            if b >= a:
                row[a * 4:(b + 1) * 4] = fg * (b - a + 1)
        raw.append(0)
        raw.extend(row)
    png = b"\x89PNG\r\n\x1a\n"
    png += _chunk(b"IHDR", struct.pack(">IIBBBBB", w, h, 8, 6, 0, 0, 0))
    png += _chunk(b"IDAT", zlib.compress(bytes(raw), 6))
    png += _chunk(b"IEND", b"")
    with open(path, "wb") as f:
        f.write(png)


def silhouette(w, h, cx=0.5, head_r=0.17, body_half=0.30):
    """头（椭圆）+ 上身（渐宽梯形）。只用于造 alpha 形状，不追求像真人。"""
    head_h = h * 0.30

    def spans(y):
        fy = y / float(h)
        if fy < 0.30:
            t = (fy - 0.15) / 0.15
            k = max(0.0, 1.0 - t * t)
            dx = head_r * w * math.sqrt(k)
            return [(cx * w - dx, cx * w + dx)]
        t = (fy - 0.30) / 0.70
        half = body_half * w * (0.72 + 0.75 * t)
        return [(cx * w - half, cx * w + half)]
    return spans


def full_rect(w, h):
    return lambda y: [(0, w - 1)]


def bottom_only(w, h):
    return lambda y: [(0, w - 1)] if y >= h * 0.68 else []


def strip(w, h):
    return lambda y: [(0, w - 1)]


def write_role(root, dirname, name, image_writer, geom=None, broken=False, copy_from=None):
    d = os.path.join(root, dirname)
    os.makedirs(d, exist_ok=True)
    if broken:
        with open(os.path.join(d, "figure.png"), "wb") as f:
            f.write(b"\x89PNG\r\n\x1a\nnot really a png")
    elif copy_from:
        shutil.copyfile(copy_from, os.path.join(d, "figure.webp"))
    else:
        image_writer(os.path.join(d, "figure.png"))
    g = {"sizePct": 80, "posXPct": 5, "posYPct": 0, "flip": False, "showName": True}
    if geom:
        g.update(geom)
    meta = {
        "name": name, "enabled": True,
        "image": "figure.png" if not copy_from else "figure.webp",
        "geom": g,
        "messages": {"order": "fixed", "items": [{"id": "1", "text": "极端用例台词。", "enabled": True}]},
    }
    with open(os.path.join(d, "meta.json"), "w", encoding="utf-8", newline="\n") as f:
        json.dump(meta, f, ensure_ascii=False, indent=2)


def build_roles(root):
    """返回 [(dir, name, kind, name_set)]。kind 只用于报告与“期望隐藏”分类。"""
    base_fig = os.path.join(SUP_DIR, BASE_ROLE, "figure.webp")
    roles = []

    def add(dirname, name, kind, name_set="full"):
        roles.append((dirname, name, kind, name_set))

    write_role(root, "normal", "极端正常", None, copy_from=base_fig)
    add("normal", "极端正常", "normal")

    write_role(root, "flip", "极端翻转", None, copy_from=base_fig, geom={"flip": True})
    add("flip", "极端翻转", "flip")

    write_role(root, "noname", "极端隐藏名", None, copy_from=base_fig, geom={"showName": False})
    add("noname", "极端隐藏名", "noname", "few")

    write_role(root, "blank", "极端全透明", lambda p: write_png(p, 600, 900, lambda y: []))
    add("blank", "极端全透明", "blank", "few")

    write_role(root, "opaque", "极端无透明", lambda p: write_png(p, 600, 900, full_rect(600, 900)))
    add("opaque", "极端无透明", "opaque", "few")

    write_role(root, "tiny", "极端一像素", lambda p: write_png(p, 1, 1, full_rect(1, 1)))
    add("tiny", "极端一像素", "tiny", "few")

    write_role(root, "huge", "极端巨图", lambda p: write_png(p, 1600, 2200, silhouette(1600, 2200)))
    add("huge", "极端巨图", "huge")

    write_role(root, "wide", "极端全景", lambda p: write_png(p, 1600, 240, silhouette(1600, 240, body_half=0.42)))
    add("wide", "极端全景", "wide")

    write_role(root, "tall", "极端窄高", lambda p: write_png(p, 280, 1600, silhouette(280, 1600, head_r=0.30, body_half=0.34)))
    add("tall", "极端窄高", "tall")

    write_role(root, "flushleft", "极端贴左", lambda p: write_png(p, 600, 900, silhouette(600, 900, cx=0.20, head_r=0.22)))
    add("flushleft", "极端贴左", "flushleft")

    write_role(root, "rightside", "极端靠右", lambda p: write_png(p, 600, 900, silhouette(600, 900, cx=0.78)))
    add("rightside", "极端靠右", "rightside")

    write_role(root, "bottomonly", "极端仅下半身", lambda p: write_png(p, 600, 900, bottom_only(600, 900)))
    add("bottomonly", "极端仅下半身", "bottomonly", "few")

    write_role(root, "strip", "极端细条", lambda p: write_png(p, 6, 900, strip(6, 900)))
    add("strip", "极端细条", "strip")

    write_role(root, "corrupt", "极端坏图", None, broken=True)
    add("corrupt", "极端坏图", "corrupt", "few")
    return roles


# ─────────────────────────── 服务驱动 ───────────────────────────

def free_port():
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def wait_up(base, proc, timeout=40.0):
    t0 = time.time()
    while time.time() - t0 < timeout:
        if proc.poll() is not None:
            return False
        try:
            urllib.request.urlopen(base + "/", timeout=2)
            return True
        except Exception:
            time.sleep(0.4)
    return False


def run_role(sup_root, dirname, name, kind, name_set):
    iso = tempfile.mkdtemp(prefix="wr_ne_")
    logf = None
    proc = None
    try:
        cfg = json.load(open(FACTORY, encoding="utf-8"))
        sup = cfg.setdefault("supervisor", {})
        sup["enabled"] = True
        sup["selection"] = "fixed"
        sup["fixedRole"] = name
        sup["order"] = [name]
        # 一个 break 会话默认活 20s；本探针要在一个会话里跑 5 种窗口 + 3 个 DPI
        # 上下文，必然超时（表现为 break.html 里 figure 仍 hidden）。把时长拉到
        # 15 分钟，让“会话过期”不再伪装成“版式起不来”。
        cfg.setdefault("short", {})["durationMs"] = 900000
        cfg.setdefault("long", {})["durationMs"] = 900000
        cfg_path = os.path.join(iso, "settings.json")
        with open(cfg_path, "w", encoding="utf-8") as f:
            json.dump(cfg, f, ensure_ascii=False, indent=2)

        port = free_port()
        base = "http://127.0.0.1:%d" % port
        env = dict(os.environ)
        env.update({
            "WAILS_SERVER_PORT": str(port),
            "WAILS_SERVER_HOST": "127.0.0.1",
            "WR_SETTINGS_PATH": cfg_path,
            "WR_SUPERVISORS_DIR": sup_root,
            "WR_LOG_LEVEL": "warn",
        })
        logp = os.path.join(iso, "server.log")
        logf = open(logp, "w", encoding="utf-8")
        proc = subprocess.Popen([EXE], cwd=ROOT, env=env, stdout=logf, stderr=subprocess.STDOUT)
        if not wait_up(base, proc):
            tail = ""
            try:
                tail = open(logp, encoding="utf-8", errors="replace").read()[-800:]
            except Exception:
                pass
            return None, "服务没起来：" + tail

        penv = dict(os.environ)
        penv.update({
            "WR_BASE": base, "WR_OUT": OUT, "WR_ROLE": name, "WR_KIND": kind,
            "WR_NAME_SET": name_set,
            "WR_PRELOAD": os.path.join(ROOT, "scripts", "probe_preload.js"),
            "NODE_PATH": os.path.join(ROOT, "scripts", "node_modules"),
        })
        r = subprocess.run(["node", PROBE_JS], cwd=ROOT, env=penv, text=True,
                           encoding="utf-8", errors="replace", timeout=1200,
                           capture_output=True)
        rows = None
        for line in (r.stdout or "").splitlines():
            if line.startswith("WR_JSON "):
                rows = json.loads(line[len("WR_JSON "):])
        if rows is None:
            return None, "探针没有回传数据：" + ((r.stderr or "")[-1200:])
        return rows, ""
    finally:
        if proc is not None and proc.poll() is None:
            proc.terminate()
            try:
                proc.wait(timeout=8)
            except Exception:
                proc.kill()
        if logf:
            logf.close()
        shutil.rmtree(iso, ignore_errors=True)


# ─────────────────────────── 判定 ───────────────────────────

def has_lone_surrogate(s):
    i = 0
    while i < len(s):
        c = ord(s[i])
        if 0xD800 <= c <= 0xDBFF:
            if i + 1 >= len(s) or not (0xDC00 <= ord(s[i + 1]) <= 0xDFFF):
                return True
            i += 2
            continue
        if 0xDC00 <= c <= 0xDFFF:
            return True
        i += 1
    return False


def valid_grapheme_elide(full_g, shown_g, full, shown):
    if shown == full:
        return True
    if has_lone_surrogate(shown) and not has_lone_surrogate(full):
        return False
    for i, g in enumerate(shown_g):
        if g != "…":
            continue
        head, tail = shown_g[:i], shown_g[i + 1:]
        if head != full_g[:len(head)]:
            continue
        if tail and tail != full_g[len(full_g) - len(tail):]:
            continue
        if len(head) + len(tail) < len(full_g):
            return True
    return False


def judge_row(role, kind, d):
    scene = d.get("scene")
    case = d.get("case")
    label = "%s [%s] %s" % (role, scene, case)
    if d.get("error"):
        return check(False, label + "：采集成功", d.get("error"))
    if d.get("case") == "SCENE":
        return check(False, label + "：版式起得来", d.get("error"))
    if d.get("skipped"):
        return check(d.get("skipped") is True, label + "：按预期跳过（该立绘上没有可放置的名字）",
                     d.get("reason"), quiet=True)

    S["rows"] += 1
    if isinstance(d.get("syncMs"), (int, float)):
        S["sync_max"] = max(S["sync_max"], d["syncMs"])
    if d.get("errs"):
        check(False, label + "：无 JS 错误", str(d["errs"])[:400], quiet=True)

    # ---- 附加场景 ----
    if case == "SealGen缺失":
        ok = check(d.get("positioned") is True and d.get("visible") is True,
                   label + "：生成器缺失时牌子仍可见", "pos=%s" % d.get("positioned"))
        ok &= check(d.get("inkType") == "css-fallback",
                    label + "：退回 CSS 纯色墨块（不是裸字）", d.get("inkType"))
        return ok
    if case == "墨块复用":
        return check(d.get("sealKey") and d.get("sealKey") == d.get("sealKeyFirst"),
                     label + "：同名同尺寸复用同一枚墨块", "first=%s now=%s" % (d.get("sealKeyFirst"), d.get("sealKey")))
    if case == "二阶段缩放":
        return check(isinstance(d.get("rLeft"), (int, float)) and d["rLeft"] >= -0.5,
                     label + "：二阶段放大后左缘不出窗（首字不被裁）",
                     "rLeft=%s lLeft=%s scale=%s" % (d.get("rLeft"), d.get("lLeft"), d.get("scale")))
    if case == "resize风暴×40":
        rec = d.get("recomputed") or {}
        ok = check(rec.get("positioned") is True, label + "：风暴后仍能定位")
        ok &= check(abs((d.get("lLeft") or 0) - (rec.get("lLeft") or 0)) <= 2.0 and
                    abs((d.get("lTop") or 0) - (rec.get("lTop") or 0)) <= 2.0,
                    label + "：风暴终态与直接重排一致（±2px）",
                    "storm=(%s,%s) fresh=(%s,%s)" % (d.get("lLeft"), d.get("lTop"), rec.get("lLeft"), rec.get("lTop")))
        ok &= check((d.get("fs") or 0) == (rec.get("fs") or 0),
                    label + "：字号一致", "%s / %s" % (d.get("fs"), rec.get("fs")))
        return ok

    if scene and scene.startswith("dpr"):
        one = d.get("dpr1") or {}
        ok = True
        for k in ("fs", "mid"):
            ok &= check(abs((d.get(k) or 0) - (one.get(k) or 0)) <= (0.001 if k == "fs" else 0.01),
                        label + "：%s 与 1x 一致" % k, "%s / %s" % (d.get(k), one.get(k)), quiet=True)
        for k in ("lLeft", "lTop"):
            ok &= check(abs((d.get(k) or 0) - (one.get(k) or 0)) <= 1.5,
                        label + "：%s 与 1x 一致（±1.5px）" % k, "%s / %s" % (d.get(k), one.get(k)), quiet=True)
        return ok

    name = d.get("name") or ""
    full = d.get("fullName")
    positioned = d.get("positioned") is True

    # 原名必须原样保留（省略只改显示，不动落盘名）
    check(full == name, label + "：原名未被改写", "%r != %r" % (full, name), quiet=True)

    if not positioned:
        S["hidden"] += 1
        HIDE_BY_ROLE.setdefault(role, []).append("%s/%s" % (scene, case))
        ok = check(d.get("visible") is False, label + "：未定位时牌子不可见",
                   "visible=%s" % d.get("visible"), quiet=True)
        ok &= check(d.get("text") == name, label + "：干净隐藏（文本恢复原名）",
                    "%r" % (d.get("text"),), quiet=True)
        ok &= check(not d.get("errs"), label + "：放弃显示时无 JS 错误", d.get("errs"), quiet=True)
        return ok

    S["pos"] += 1
    ok = check(d.get("visible") is True, label + "：牌子可见", "", quiet=True)
    ok &= check(d.get("vertical") is True, label + "：竖排", "", quiet=True)
    ok &= check(d.get("hiddenClass") is False, label + "：没有被 hidden 类藏着", "", quiet=True)

    fs = d.get("fs")
    ok &= check(isinstance(fs, (int, float)) and 15 - 1e-6 <= fs <= 26 + 1e-6,
                label + "：字号 15~26px", fs, quiet=True)
    if isinstance(fs, (int, float)):
        S["fs_min"] = min(S["fs_min"], fs)
        S["fs_max"] = max(S["fs_max"], fs)
    ok &= check(d.get("singleColumn") is True, label + "：单列（没有折列）",
                "contentWidth=%s fs=%s" % (d.get("contentWidth"), fs), quiet=True)
    ok &= check(d.get("overflow") is False, label + "：文字不溢出牌面", "", quiet=True)

    ov = d.get("ov")
    if isinstance(ov, (int, float)):
        S["ov_max"] = max(S["ov_max"], ov)
    ok &= check(isinstance(ov, (int, float)) and ov <= 0.205,
                label + "：重叠 ≤ 20%", ov, quiet=True)

    # 整块在窗内 / 不压对话框（用布局坐标，二阶段的 scale 不算越界）
    lleft, lright = d.get("lLeft"), d.get("lRight")
    ltop = d.get("lTop")
    appw, apptop = d.get("appW") or 0, d.get("appTop") or 0
    dialog = d.get("dialogTop")
    ok &= check(isinstance(lleft, (int, float)) and lleft >= 5.0,
                label + "：左缘不出窗", "lLeft=%s" % lleft, quiet=True)
    ok &= check(isinstance(lright, (int, float)) and lright <= appw - 5.0,
                label + "：右缘不出窗", "lRight=%s appW=%s" % (lright, appw), quiet=True)
    ok &= check(isinstance(ltop, (int, float)) and ltop - apptop + (d.get("padTop") or 0) >= -4.5,
                label + "：上缘只许裁内边距", "top=%s padTop=%s" % (ltop, d.get("padTop")), quiet=True)
    if isinstance(dialog, (int, float)):
        # 产品口径是留 RT_GAP(12)。这条量的是**渲染坐标**下的间隙：牌底取
        # getBoundingClientRect().bottom，与 dialogTop 同源。以前用 offsetTop 链算出的
        # 布局牌底去减渲染的 dialogTop，两个坐标系混着比，间隙会凭空少几像素 ——
        # 于是判据被压到 9.5 才不红，真回归（牌压进对话框，差 20px+）也一样抓得住，
        # 但“少了几像素”这件事就被 9.5 这个魔数盖住了。
        rb = d.get("rBottom")
        if not isinstance(rb, (int, float)):
            # 不许静默退回旧判据：那条判据本身就是错的（混坐标系），退回等于让一条
            # 已知错误的断言继续充数。缺字段只可能是页面与探针版本不匹配。
            ok &= check(False, label + "：牌底间隙可测量（缺 rBottom，页面与探针版本不匹配）",
                        rb, quiet=True)
        else:
            S["gap_min"] = min(S["gap_min"], dialog - rb)
            ok &= check(rb <= dialog - 11.5,
                        label + "：牌底不压对话框，留出 RT_GAP 12px",
                        "渲染间隙=%.2f（bottom=%.2f dialog=%.2f）" % (dialog - rb, rb, dialog),
                        quiet=True)

    # 竖向锚点：未被迫 clamp 时牌心必须在 0.17（头肩中点）
    mid = d.get("mid")
    vis_top = d.get("visTop")
    clamped_top = isinstance(vis_top, (int, float)) and isinstance(ltop, (int, float)) \
        and ltop - apptop <= vis_top + 2.5
    # 牌底是否“顶到对话框上方”（用来放宽下面的牌心断言）。同样必须用渲染坐标：
    # 布局口径量到 9.23px 的那些用例，渲染口径其实是 14.56px —— 两个数不同源，
    # 拿布局值配 13.5 的阈值会把这个开关判成“被夹”，实际上只是坐标系差值。
    # 渲染口径下“贴着”= 间隙 ≈14.5（产品按 RT_GAP 12 夹，但布局/渲染之间还有 2.5px 的
    # 位移），所以阈值放到 16.5，宁可放宽牌心断言也不误红。
    _rb = d.get("rBottom")
    clamped_bottom = isinstance(dialog, (int, float)) and isinstance(_rb, (int, float)) \
        and dialog - _rb <= 16.5
    if d.get("toe"):
        S["toe"] += 1
    if isinstance(mid, (int, float)):
        # 420×300 低于产品最小窗口（760×600）：牌顶被窗口上缘夹住，牌心必然下移，
        # 那里只验“不出窗、不崩”。最小尺寸及以上仍要求 0.30 以内。
        tight = (d.get("winH") or 0) < 600
        ok &= check(mid <= (0.45 if tight else 0.30),
                    label + "：牌心不落到胸口以下", mid, quiet=True)
        if not d.get("toe") and not clamped_top and not clamped_bottom:
            ok &= check(abs(mid - 0.17) <= 0.02,
                        label + "：牌心落在头肩中点（0.17±0.02）", mid, quiet=True)

    # 横向缝：有空间就必须让开
    gap, slack = d.get("gap"), d.get("slack")
    if isinstance(slack, (int, float)) and slack >= 32.5:
        S["gap32"] += 1
        ok &= check(isinstance(gap, (int, float)) and gap >= 31.5,
                    label + "：左侧有空间时拉开 32px 舒适缝", "gap=%s slack=%s" % (gap, slack), quiet=True)

    # 墨块：现场生成、viewBox == 实测像素、层序牌在上
    ok &= check(d.get("inkType") == "data:seal", label + "：墨块是现场生成的 data:URI",
                d.get("inkType"), quiet=True)
    vb = d.get("inkVB")
    box = d.get("box") or {}
    ok &= check(isinstance(vb, list) and len(vb) == 2 and box.get("w") and box.get("h") and
                abs(vb[0] - box["w"]) <= 1.5 and abs(vb[1] - box["h"]) <= 1.5,
                label + "：墨块 viewBox == 牌面像素（没被拉伸）",
                "vb=%s box=%s" % (vb, box), quiet=True)
    nl, fl = d.get("nameLayer"), d.get("figLayer")
    ok &= check(isinstance(nl, int) and (fl is None or nl > fl),
                label + "：牌画在立绘之上", "name=%s fig=%s" % (nl, fl), quiet=True)

    # 省略必须是字素簇边界
    shown_g = (d.get("graphemes") or {}).get("shown")
    full_g = (d.get("graphemes") or {}).get("full")
    text = d.get("text")
    if isinstance(shown_g, list) and isinstance(full_g, list) and isinstance(text, str):
        if text != name:
            S["elided"] += 1
            ok &= check(valid_grapheme_elide(full_g, shown_g, name, text),
                        label + "：省略按字素簇切（不拆 emoji / 国旗 / 组合符）",
                        "full_g=%s shown_g=%s" % (full_g[:6], shown_g[:6]), quiet=True)
            ok &= check(d.get("elided") is True, label + "：省略标记与文本一致", "", quiet=True)
    return ok


def judge_role(role, kind, rows):
    total_rows = len(rows)
    pos = sum(1 for r in rows if r.get("positioned") is True and r.get("case") not in
              ("SealGen缺失", "墨块复用", "二阶段缩放", "resize风暴×40"))
    print("\n[%s] kind=%s  采集 %d 条 / 已定位 %d 条" % (role, kind, total_rows, pos))

    for d in rows:
        judge_row(role, kind, d)

    # 元断言 1：有头有身的立绘在正常窗口里必须真的有名字
    must_show = {"normal", "flip", "tall", "rightside", "strip", "huge"}
    if kind in must_show:
        for scene in ("break@1100x720", "break@760x980", "takeout@880x560"):
            got = [r for r in rows if r.get("scene") == scene and r.get("positioned") is True]
            check(len(got) >= 1, "%s [%s]：至少有一个名字能显示" % (role, scene),
                  "已定位 0 条")

    # 元断言 2：靠右的人物左侧有大空间，必须用上 32px 舒适缝
    if kind == "rightside":
        got = [r for r in rows if r.get("positioned") is True and isinstance(r.get("slack"), (int, float))]
        check(any(r["slack"] >= 32.5 and (r.get("gap") or 0) >= 31.5 for r in got),
              "%s：左侧大空间时用上舒适缝" % role,
              "slack/gap=%s" % [(r.get("slack"), r.get("gap")) for r in got[:4]])

    # 元断言 3：翻转立绘不能因为剖面没镜像而整块不显示
    if kind == "flip":
        ten = [r for r in rows if r.get("case") == "10字" and r.get("scene") == "break@1100x720"]
        check(ten and ten[0].get("positioned") is True and (ten[0].get("fs") or 0) >= 20,
              "%s：翻转后 10 字仍有大字号位置" % role,
              "pos=%s fs=%s" % (ten[0].get("positioned") if ten else None, ten[0].get("fs") if ten else None))


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--role", action="append", default=[], help="只跑指定角色名（可多次）")
    ap.add_argument("--keep", action="store_true", help="保留临时立绘目录")
    args = ap.parse_args()

    if not os.path.exists(EXE):
        print("缺少 %s（先 go build -tags server ...）" % EXE)
        return 2
    if not os.path.exists(PROBE_JS):
        print("缺少 %s" % PROBE_JS)
        return 2
    os.makedirs(OUT, exist_ok=True)

    sup_root = tempfile.mkdtemp(prefix="wr_ne_roles_")
    try:
        roles = build_roles(sup_root)
        if args.role:
            want = set(args.role)
            roles = [r for r in roles if r[1] in want or r[0] in want]
        print("立绘目录 %s（%d 个角色）" % (sup_root, len(roles)))
        for dirname, name, kind, name_set in roles:
            rows, err = run_role(sup_root, dirname, name, kind, name_set)
            if rows is None:
                check(False, "%s：采集" % name, err)
                continue
            judge_role(name, kind, rows)
    finally:
        if not args.keep:
            shutil.rmtree(sup_root, ignore_errors=True)
        else:
            print("保留立绘目录：%s" % sup_root)

    print("\n" + "=" * 70)
    print("结果：%d 项 / 失败 %d" % (S["checks"], S["fail"]))
    print("用例 %d（定位 %d / 隐藏 %d）| 字号 %.1f~%.1f | 最大重叠 %.3f | 最大重排 %.1fms"
          % (S["rows"], S["pos"], S["hidden"], S["fs_min"] if S["fs_min"] < 99 else 0,
             S["fs_max"], S["ov_max"], S["sync_max"]))
    print("省略 %d 条 | 往上让 %d 条 | 用上 32px 缝 %d 条 | 舒适缝覆盖 %d"
          % (S["elided"], S["toe"], S["gap32"], S["clean_hide"]))
    if S["gap_min"] < 1e9:
        print("牌底↔对话框最小渲染间隙 %.2fpx（RT_GAP 口径 12）" % S["gap_min"])
    if HIDE_BY_ROLE:
        print("\n各角色隐藏用例（用于区分“物理放不下”与“算错了”）：")
        for r, items in HIDE_BY_ROLE.items():
            print("  %s: %d 条" % (r, len(items)))
            print("    " + ", ".join(items[:8]) + (" …" if len(items) > 8 else ""))
    if FAILS:
        print("\n失败明细（最多 30 条）：")
        for f in FAILS[:30]:
            print("  - " + f)
    return 0 if S["fail"] == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
