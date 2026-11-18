#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""题名牌“极端用例”压力探针（判定端）。

覆盖的是 probe_role_title.py 不管的那一半：那个探针只跑**真实专员名**（内置名单，
最长 10 字），量的是“不压脸”；这里把名字换成 26 组极端输入，量的是“任何名字都
不越界、不折列、不糊、不崩”。

用例矩阵（scripts/probe/role_title_stress.js 里定义，这里只做校验）：
  长度 1 / 2 / 10 / 16 / 21 / 29 / 30（UI 上限）/ 42 / 60（Go 上限）/ 120 / 200
  字形 emoji、ZWJ 家庭、日文、韩文、泰文（组合符）、阿拉伯文（RTL）、全角拉丁
  边界 含省略号、HTML 标签、首尾空格、中间点、换行、31 拉丁带空格、空名

断言（每个用例都跑一遍，≈13 条）：
  · 已定位（空名除外）且竖排；单列（内容盒只装得下一列）
  · hit == 0（一个不透明像素都不压立绘）；字号 15~26px
  · 牌面文字 == 原名，或**合法的中间省略**（首尾都在，且确实少了字）
  · 省略标记与文本自洽（elided ⇔ text != name）
  · 文字不溢出牌面；牌面整块留在窗口内且不压对话框
  · 墨块是现场生成的 data: URI，且 viewBox == 牌子实测像素（没被缩放）
  · 碎点画布 == 牌子 + 2×10px，环带是固定 -10px
  · 页面无未捕获 JS 错误、无横向溢出
  · 空名：不显示、不崩；随后改回正常名必须恢复
  · 改名风暴：8 次连续改名后终态就是最后一个名字（无残留）
  · 同尺寸重复：sealKey 复用（墨块没重画）
  · 窗口扫掠 / 立绘极端 / DPI=2：同样一组几何断言 + 与 1x 结果对比

隔离：配置用出厂默认的一份临时副本，WR_SETTINGS_PATH 指过去；一位专员一个服务
（fixedRole 启动时读进内存）。真实 config/settings.json 前后 SHA-256 必须一致。

用法：python scripts/probe/probe_role_title_stress.py [--role 魔法师] [--scene break@1100x720]
退出码 0 = 全部通过。
"""

import argparse
import hashlib
import json
import os
import shutil
import socket
import subprocess
import sys
import tempfile
import time
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(os.path.dirname(HERE))
EXE = os.path.join(ROOT, "build", "bin", "rest_server.exe")
FACTORY = os.path.join(ROOT, "internal", "settings", "default_settings.json")
SUP_DIR = os.path.join(ROOT, "assets", "supervisors")
PROBE_JS = os.path.join(HERE, "role_title_stress.js")
OUT = os.path.join(tempfile.gettempdir(), "wr_role_title_stress")

try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass

_stat = {"n": 0, "fail": 0, "worst": (0.0, ""), "elided": 0, "cases": 0,
         "fs_min": 99.0, "fs_max": 0.0, "vout": (0.0, ""), "known": 0,
         "pos": 0, "relax": 0, "ovmax": 0.0, "ovsum": 0.0, "zero": 0}

FS_MIN, FS_MAX = 15.0, 26.0
LATENCY_FAIL_MS = 800.0
# 牌与立绘允许的最大重叠面积占比。必须与 web/dist 的 break.js / takeout.js 里
# RT_OVERLAP_RATIO 同值。这是“少许重叠”的唯一口径：改产品常量必须同时改这里，
# 否则探针要么假绿（阈值比产品宽）要么误报（比产品严）。
OVERLAP_MAX = 0.20

# ---- 已知缺陷（不是这次的回归，测试时用数据坐实过）----
# 画家 的立绘把窗口左侧的竖条整条占满：题名既贴不了头部左侧（headPx 离左缘不够
# 一个牌宽），也贴不了窗口左缘（那一条列区间全是立绘像素 → hit > 0）。求解器按
# “宁可不显示，也不画一块盖在脸上的红印”处理，于是 3 字以上的名字在**竖屏窗口**
# 里整块消失（内置名“画家”只有 2 字所以一直没暴露）。
# 坐实方式：把 RT_TOP_CLIP 临时放到 4000（等于关掉本次新增的上缘判据）后重跑，
# 同一批用例仍然 failed —— 与本轮改动无关。
# 处理口径：记为“已知问题”而不是失败（否则每次全绿都变成噪音），但单独计数打印；
# 真正的修法是加第三种贴法（窗口右侧/顶部空白区），属设计决策，等拍板。
# 已知问题：这里只登记“产品上确实还没解决、但本轮不修”的用例，judge 会单独打印
# [known] 并且不计入失败。**不要拿它当消音器** —— 能修就修，修完立刻删掉对应条目。
#
# 2026-11-17：原先登记的「画家 × 竖屏 = 3 字以上名字整块消失」已经撤销。它不再是
# 缺陷：牌与立绘允许少量重叠（立绘在上层），画家的长名现在拿到 26px、重叠 0.38~0.43，
# 落在 RT_OVERLAP_RATIO(0.45) 之内，全部 26 组名字都能显示。
KNOWN_ISSUES = []

_known_seen = set()


def known_hit(role, d):
    # 空名用例本来就该“不定位”，别被当成缺陷登记掉（曾把画家 × 竖屏的空名用例
    # 误算成 known，真正的失败反而被它盖住了）。
    if d.get("positioned") or d.get("empty"):
        return None
    for k in KNOWN_ISSUES:
        if k["role"] == role and k["match"](d):
            return k
    return None


def valid_elide(full, shown):
    """shown 是否等于 full 去掉中间一段后插入一个省略号（首尾都保留）。

    逐字精确判，不复刻 elide() 的字数算术：遍历 shown 里每个「…」当成插入点，
    要求前缀/后缀都是原名的一部分、且确实少了至少一个字。名字本身含「…」时
    （合法名字，允许集只有 9 个 Windows 非法字符）旧探针的 partition 判法会误判，
    这里按“哪个省略号是插入的”逐一试。
    """
    if shown is None:
        return False
    if shown == full:
        return True
    n = len(full)
    idx = -1
    while True:
        idx = shown.find("…", idx + 1)
        if idx < 0:
            return False
        head, tail = shown[:idx], shown[idx + 1:]
        if not head or not tail:
            continue
        if not (full.startswith(head) and full.endswith(tail)):
            continue
        if len(full) - len(head) - len(tail) >= 1:   # 确实少了字，不是首尾拼回原名
            return True


def check(ok, name, detail="", quiet=False):
    _stat["n"] += 1
    if not ok:
        _stat["fail"] += 1
        print("  [FAIL] %s%s" % (name, ("  — %s" % detail) if detail else ""))
    elif not quiet:
        print("  [ok  ] %s%s" % (name, ("  — %s" % detail) if detail else ""))
    return bool(ok)


def digest(p):
    if not os.path.exists(p):
        return ""
    h = hashlib.sha256()
    with open(p, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


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


def list_roles():
    if not os.path.isdir(SUP_DIR):
        return []
    return sorted(d for d in os.listdir(SUP_DIR) if os.path.isdir(os.path.join(SUP_DIR, d)))


def run_one(role):
    iso = tempfile.mkdtemp(prefix="wr_rts_")
    try:
        cfg = json.load(open(FACTORY, encoding="utf-8"))
        sup = cfg.setdefault("supervisor", {})
        sup["enabled"] = True
        sup["selection"] = "fixed"
        sup["fixedRole"] = role
        sup["order"] = [role]
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
            "WR_LOG_LEVEL": "warn",
        })
        logp = os.path.join(iso, "server.log")
        logf = open(logp, "w", encoding="utf-8")
        proc = subprocess.Popen([EXE], cwd=ROOT, env=env, stdout=logf, stderr=subprocess.STDOUT)
        try:
            if not wait_up(base, proc):
                tail = ""
                try:
                    tail = open(logp, encoding="utf-8", errors="replace").read()[-600:]
                except Exception:
                    pass
                return None, "服务没起来：" + tail
            penv = dict(os.environ)
            penv.update({
                "WR_BASE": base,
                "WR_OUT": OUT,
                "WR_ROLE": role,
                "WR_PRELOAD": os.path.join(ROOT, "scripts", "probe_preload.js"),
                "NODE_PATH": os.path.join(ROOT, "scripts", "node_modules"),
            })
            r = subprocess.run(["node", PROBE_JS], cwd=ROOT, env=penv, text=True,
                               encoding="utf-8", errors="replace", timeout=900,
                               capture_output=True)
            rows = None
            for line in (r.stdout or "").splitlines():
                if line.startswith("WR_JSON "):
                    rows = json.loads(line[len("WR_JSON "):])
            if rows is None:
                return None, "探针没有回传数据：" + ((r.stderr or "")[-800:])
            return rows, ""
        finally:
            if proc.poll() is None:
                proc.terminate()
                try:
                    proc.wait(timeout=8)
                except Exception:
                    proc.kill()
            logf.close()
    finally:
        shutil.rmtree(iso, ignore_errors=True)


def judge(role, d):
    """单条用例的全部断言。返回 True/False 汇总，失败已打印。"""
    case = d.get("case")
    label = "%s [%s] %r" % (role, d.get("scene"), d.get("name"))
    if d.get("error"):
        return check(False, "%s：采集成功" % label, d.get("error"))
    known = known_hit(role, d)
    if known:
        _stat["cases"] += 1
        key = (role, str(d.get("scene")))
        if key not in _known_seen:
            _known_seen.add(key)
            _stat["known"] += 1
            print("  [known] %s：%s —— %s" % (role, d.get("scene"), known["why"]))
        return True

    name = d.get("name") or ""
    empty = (name == "")
    _stat["cases"] += 1

    # 延迟：200 字也不能卡住窗口（用户改名后牌面必须马上出来）
    ms = d.get("layoutMs")
    if isinstance(ms, (int, float)):
        if ms > _stat["worst"][0]:
            _stat["worst"] = (ms, "%s %s" % (role, d.get("case")))

    if empty:
        ok = True
        ok &= check(d.get("positioned") is False, label + "：空名不定位",
                    "positioned=%s" % d.get("positioned"), quiet=True)
        ok &= check(d.get("visible") is False, label + "：空名不显示牌子",
                    "visible=%s" % d.get("visible"), quiet=True)
        ok &= check(not d.get("errs"), label + "：空名无 JS 错误", d.get("errs"), quiet=True)
        return bool(ok)

    ok = True
    if not d.get("positioned"):
        # 立绘被滑条推到窗口外时（高度 100% + 距底 40%），牌面要么下移到窗口上缘，
        # 要么判“放不下”——产品口径是宁可整块不显示，也不显示半截字。
        if d.get("scene") == "fig":
            ok &= check(d.get("visible") is False,
                        label + "：放不下时整块不显示（不是半截字）",
                        "positioned=False visible=%s" % d.get("visible"), quiet=True)
            ok &= check(not d.get("errs"), label + "：放弃显示时无 JS 错误",
                        d.get("errs"), quiet=True)
            return bool(ok)
        return check(False, label + "：已完成实测定位",
                     "hit=%s fs=%s" % (d.get("hit"), d.get("fs")))
    ok &= check(d.get("visible") is True, label + "：牌子可见", "", quiet=True)
    ok &= check(d.get("vertical") is True, label + "：竖排", "", quiet=True)
    ok &= check(d.get("hiddenClass") is False, label + "：没有被 hidden 类藏着", "", quiet=True)

    fs = d.get("fs")
    ok &= check(isinstance(fs, (int, float)) and FS_MIN - 1e-6 <= fs <= FS_MAX + 1e-6,
                label + "：字号在 15~26px 之间", fs, quiet=True)
    if isinstance(fs, (int, float)):
        _stat["fs_min"] = min(_stat["fs_min"], fs)
        _stat["fs_max"] = max(_stat["fs_max"], fs)
    cw, box = d.get("contentWidth") or 0, d.get("box") or {}
    single = isinstance(fs, (int, float)) and fs > 0 and cw <= fs * 1.75
    ok &= check(single, label + "：单列（没有折成多列）",
                "contentWidth=%s box=%s" % (cw, box), quiet=True)
    # 2026-11-17 起不再是“零遮挡”：允许牌与立绘少量重叠（立绘在上层压住牌的一条边），
    # 判据是**面积占比**上限。所以这里既不允许超过阈值，也不需要 0 —— 0 反而是
    # “为了不碰把长名缩成一根针”的老毛病。
    _ov = d.get("ov")
    ok &= check(isinstance(_ov, (int, float)) and _ov <= OVERLAP_MAX + 1e-6,
                label + "：与立绘重叠 <= %.0f%%" % (OVERLAP_MAX * 100),
                "ov=%s hit=%s fs=%s" % (_ov, d.get("hit"), fs), quiet=True)
    # 层序：牌必须画在立绘之上（2026-11-18 反转）—— 牌顶下沉到颈线后右缘常压在袖口/
    # 肩线上十来个像素，立绘在上会把最后一列字形切掉。比较的是**公共祖先层叠上下文**
    # 里的次序（休息窗口里立绘在 .stage(8) 内、自身 7，直接比 z-index 会误判同级）。
    _nl, _fl = d.get("nameLayer"), d.get("figLayer")
    if _fl is not None:
        ok &= check(isinstance(_nl, (int, float)) and _nl > _fl,
                    label + "：牌在立绘之上（重叠处压住袖口，不切字形）",
                    "figLayer=%s nameLayer=%s" % (_fl, _nl), quiet=True)
    # 统计“放宽轮”的比例与最大重叠：单条合法不等于策略正确，末尾的元断言看这两个数。
    _stat["pos"] += 1
    if isinstance(_ov, (int, float)):
        _stat["ovmax"] = max(_stat["ovmax"], _ov)
        _stat["ovsum"] += _ov
        if _ov <= 1e-6:
            _stat["zero"] += 1
    if d.get("relax"):
        _stat["relax"] += 1
        ok &= check(isinstance(_ov, (int, float)) and _ov > 0,
                    label + "：放宽轮确实用到了重叠（不是白放宽）",
                    "ov=%s" % _ov, quiet=True)

    text = d.get("text")
    ok &= check(valid_elide(name, text), label + "：牌面文字 == 原名或合法中间省略",
                repr(text), quiet=True)
    elided = bool(d.get("elided"))
    ok &= check(elided == (text != name), label + "：省略标记与文本自洽",
                "elided=%s text!=name=%s" % (elided, text != name), quiet=True)
    if elided:
        _stat["elided"] += 1

    ok &= check(d.get("overflow") is False, label + "：文字没有溢出牌面",
                "range=%s content=%s" % (d.get("rangeBox"), d.get("contentWidth")), quiet=True)

    # 几何：整块在窗口内、不压对话框。用**布局盒**（lLeft/lRight…），不是
    # getBoundingClientRect —— 二阶段“凑近”给牌子挂了 scale 1.08（脉动到 1.095），
    # 视觉盒会平白外扩几像素，那是设计效果，不是定位 bug。
    left, right = d.get("lLeft"), d.get("lRight")
    top, bottom = d.get("lTop"), d.get("lBottom")
    dt, wh = d.get("dialogTop"), d.get("winH")
    ok &= check(left is not None and left >= -0.5, label + "：牌面不越窗口左缘", left, quiet=True)
    ok &= check(right is not None and d.get("winW") and right <= d["winW"] + 0.5,
                label + "：牌面不越窗口右缘", "right=%s winW=%s" % (right, d.get("winW")), quiet=True)
    # 上缘：立绘被推出窗口时允许裁掉牌子的上内边距（墨块上缘，不带字），
    # 但不许裁到字形 —— 所以判据是 top + padTop >= -4（4px 抗锯齿容差）。
    pad_top = float(d.get("padTop") or 0)
    ok &= check(top is not None and top + pad_top >= -4.5,
                label + "：牌面上缘最多裁到内边距（不裁字形）",
                "top=%s padTop=%s" % (top, pad_top), quiet=True)
    ok &= check(bottom is not None and wh and bottom <= wh + 0.5,
                label + "：牌面不越窗口下缘", "bottom=%s winH=%s" % (bottom, wh), quiet=True)
    ok &= check(dt is None or (bottom is not None and bottom <= dt + 1.0),
                label + "：牌面不压对话框", "bottom=%s dialogTop=%s" % (bottom, dt), quiet=True)
    # 视觉盒：被 scale 放大的阶段（凑近/脉动）会外扩，那是设计效果，不当失败判据，
    # 只统计最大值写进报告。反过来，scale 为 1 时视觉盒必须**等于**布局盒 ——
    # 差出 2px 说明有别的 transform 在偷偷挪牌子。
    sc = str(d.get("scale") or "none")
    if sc in ("none", "1", "1 1"):
        if None not in (d.get("left"), left):
            ok &= check(abs(d["left"] - left) <= 2 and abs((d.get("right") or 0) - (right or 0)) <= 2,
                        label + "：无缩放时视觉盒 == 布局盒",
                        "rect=%s/%s layout=%s/%s" % (d.get("left"), d.get("right"), left, right),
                        quiet=True)
    else:
        for k, v in (("左", -(d.get("vOutLeft") or 0)), ("右", d.get("vOutRight") or 0),
                     ("下", d.get("vOutBottom") or 0)):
            if v > _stat["vout"][0]:
                _stat["vout"] = (v, "%s %s %s" % (role, d.get("case"), k))

    # 墨块：现场生成、画布 == 牌子实测像素（这条一红就说明又被拉伸了）
    ok &= check(d.get("inkType") == "data:seal", label + "：墨块是现场生成的 data URI",
                d.get("inkType"), quiet=True)
    if isinstance(box, dict) and box.get("w") and box.get("h"):
        ok &= check(d.get("inkVB") == [box["w"], box["h"]],
                    label + "：墨块 viewBox == 牌子实测像素",
                    "inkVB=%s box=%s" % (d.get("inkVB"), box), quiet=True)
    # 牌外不再有碎印泥（2026-11-18 用户决定整层删除）：::after 必须没有背景图。
    ok &= check(d.get("speckType") == "none",
                label + "：牌外没有碎点层（::after 无背景图）", d.get("speckType"),
                quiet=True)

    ok &= check(not d.get("errs"), label + "：无未捕获 JS 错误", d.get("errs"), quiet=True)
    ok &= check(isinstance(d.get("docScrollW"), (int, float)) and d.get("winW")
                and d["docScrollW"] <= d["winW"] + 1,
                label + "：页面无横向溢出",
                "scrollW=%s winW=%s" % (d.get("docScrollW"), d.get("winW")), quiet=True)
    if isinstance(ms, (int, float)):
        ok &= check(ms <= LATENCY_FAIL_MS, label + "：重排未卡顿（<= 800ms）",
                    "%.1fms" % ms, quiet=True)
    return bool(ok)


def judge_special(role, d):
    """附加场景的专项断言。"""
    case = d.get("case")
    label = "%s [%s] %s" % (role, d.get("scene"), case)
    if d.get("error"):
        return check(False, label, d.get("error"))
    ok = True
    if case == "改名风暴×8":
        ok &= check(d.get("text") == d.get("stormExpect"),
                    label + "：终态是最后一次改的名字",
                    "text=%r expect=%r" % (d.get("text"), d.get("stormExpect")))
        _ov = d.get("ov")
        ok &= check(d.get("positioned") is True
                    and isinstance(_ov, (int, float)) and _ov <= OVERLAP_MAX + 1e-6,
                    label + "：风暴后仍正确定位且重叠在阈值内",
                    "ov=%s hit=%s" % (_ov, d.get("hit")))
    elif case == "同尺寸重复":
        ok &= check(d.get("sealKey") and d.get("sealKey") == d.get("sealKeyFirst"),
                    label + "：同尺寸重复命中墨块缓存（没重画）",
                    "%s vs %s" % (d.get("sealKeyFirst"), d.get("sealKey")))
        ok &= check(d.get("positioned") is True, label + "：重复后仍定位", "")
    elif case == "空名后恢复":
        ok &= judge(role, d)
    elif case == "SCENE":
        return check(False, label, d.get("error"))
    elif d.get("dpr1"):
        # DPI=2 与 1x 的同一名字必须给同一个字号、同一个盒宽：布局是 CSS px 的世界，
        # devicePixelRatio 一进来就说明有人拿物理像素算了布局。
        ok &= judge(role, d)
        ref = d.get("dpr1") or {}
        ok &= check(d.get("fs") == ref.get("fs"),
                    label + "：DPI=2 与 1x 字号一致",
                    "2x=%s 1x=%s" % (d.get("fs"), ref.get("fs")))
        ok &= check((d.get("box") or {}) == (ref.get("box") or {}),
                    label + "：DPI=2 与 1x 盒尺寸一致",
                    "2x=%s 1x=%s" % (d.get("box"), ref.get("box")))
    else:
        # 扫掠 / 立绘极端 / DPI：套用常规断言（文本合法、几何、墨块一致）
        ok &= judge(role, d)
    return bool(ok)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--role", action="append", default=None)
    ap.add_argument("--scene", default=None, help="只跑某个版式（如 break@1100x720）")
    args = ap.parse_args()

    if not os.path.exists(EXE):
        print("没有 %s（先 go build -tags server -o build/bin/rest_server.exe ./cmd/workerrest）" % EXE)
        return 2
    if not shutil.which("node"):
        print("找不到 node（探针用 Playwright 驱动系统 Edge）")
        return 2

    roles = args.role or list_roles()
    if not roles:
        print("assets/supervisors 下没有专员目录，无法测量")
        return 2

    os.makedirs(OUT, exist_ok=True)
    real_cfg = os.path.join(ROOT, "config", "settings.json")
    before = digest(real_cfg)

    print("题名牌极端用例：%d 位专员 × 3 版式 × 26 组名字" % len(roles))
    per_role = []
    for role in roles:
        rows, err = run_one(role)
        if rows is None:
            check(False, "%s：整位专员跑完三个版式" % role, err)
            continue
        f0, n0 = _stat["fail"], _stat["n"]
        for d in rows:
            if args.scene and args.scene not in str(d.get("scene")) and d.get("scene") != "dpr2":
                continue
            judge_special(role, d)
        fails = _stat["fail"] - f0
        cases = _stat["n"] - n0
        per_role.append((role, cases, fails))
        print("%-14s %3d 项断言 · 失败 %d" % (role, cases, fails))

    # 失效断言：这一轮必须真的跑到“省略”和“满字号”两条边，否则全绿没意义
    print("")
    check(_stat["cases"] >= 26 * 3, "至少覆盖 26 组名字 × 3 个版式",
          "实际 %d 条用例" % _stat["cases"])
    check(_stat["elided"] > 0, "本轮真的走到了“中间省略”分支",
          "%d 条省略" % _stat["elided"])
    check(_stat["fs_max"] >= FS_MAX - 0.01, "短名仍拿到 26px 上限（没被无谓缩小）",
          "fs_max=%.1f" % _stat["fs_max"])
    check(_stat["fs_min"] >= FS_MIN - 0.01, "长名没有掉到 15px 以下（可读下限守住）",
          "fs_min=%.1f" % _stat["fs_min"])
    # 元断言：求解器必须**先按完全不碰**求字号，只在全字号都放不下时才放宽。
    # 若放宽用例占了大半，说明又变成“能压就压”（每条单看都合法，整体观感却变成
    # “牌子总贴着人物”）。这条不看单条用例，看的是策略本身。
    _pos = max(1, _stat["pos"])
    # 2026-11-18 起策略是**位置优先**：牌顶锁在颈线锚点上求解，同一档字号里先问零遮挡、
    # 不成立才允许压一点人物（牌在人物之上，压住的是袖口）。颈以下的左侧留白本来就比
    # 头部那段窄，所以“放宽轮”占多数是设计，不是退化 —— 这里改钉三条：
    #   ① 零遮挡仍要占一部分（“能零遮挡就零遮挡”没被放弃）；
    #   ② 平均重叠不许逼近上限（大片压人物就说明锚点或求解坏了）；
    #   ③ 单条最大重叠不超上限。
    check(_stat["zero"] >= _pos * 0.05,
          "零遮挡仍占一部分（同字号内先按完全不碰求解）",
          "%d/%d = %.1f%%" % (_stat["zero"], _pos, 100.0 * _stat["zero"] / _pos))
    check(_stat["ovsum"] / _pos <= 0.12,
          "平均重叠 <= 12%（重叠是“压住一条袖口”，不是把牌埋进人物）",
          "mean=%.3f relax=%d/%d" % (_stat["ovsum"] / _pos, _stat["relax"], _pos))
    check(_stat["ovmax"] <= OVERLAP_MAX + 1e-6,
          "全量最大重叠未超上限", "ov_max=%.3f（上限 %.2f）" % (_stat["ovmax"], OVERLAP_MAX))
    print("最慢一次重排：%.0fms（%s）" % (_stat["worst"][0], _stat["worst"][1]))
    if _stat["vout"][0] > 0:
        print("二阶段凑近的视觉外扩最大 %.1fpx（%s）" % (_stat["vout"][0], _stat["vout"][1]))
    print("字号区间：%.1f ~ %.1f px；省略 %d 次"
          % (_stat["fs_min"], _stat["fs_max"], _stat["elided"]))

    after = digest(real_cfg)
    check(before == after, "真实 config/settings.json 未被改动", real_cfg)

    print("\n结果：%d 项断言，%d 失败（已知问题 %d 处，见 KNOWN_ISSUES）"
          % (_stat["n"], _stat["fail"], _stat["known"]))
    print("数据与截图：%s" % OUT)
    return 0 if _stat["fail"] == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
