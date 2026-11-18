#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""督休专员题名“贴合立绘”全专员探针（驱动端）。

为什么需要它：题名的位置与字号由渲染层按立绘 alpha 剖面反解（break.js 与
takeout.js 各一份 layoutRoleTitle）。这个反解有一处对称性极易漏：剖面 minY[]
按**原图列序**量，屏幕映射按**显示方向**算 —— 立绘水平翻转时两者互为镜像。

2026-11-02 实测到漏掉这一步的后果：命中判定去查了图片的另半边，牌面明明落在
人物左侧的空白里（实拍牌底是纯背景），判据却在右侧对称位置数到一片像素，于是
“每列都算遮挡”—— 魔法师 hit=34，正好等于区间列数 34，字号被一路压到下限
15px。只有几何配置 flip=true 的专员才触发，主探针（probe_renderer 跑真实配置
的那一位）抽不到就等于没测。所以这里**逐个专员**钉住 fixedRole 各跑一遍。

覆盖：每位专员 × 三个版式（横屏休息窗 / 竖屏休息窗 / 外卖提醒窗）。两份题名
实现（break.js、takeout.js）都必须被跑到 —— 只测一份等于只测一半。

断言（数据由 probe_role_title.js 采，判定在这里）：
  · 已定位（data-rt=1）且是竖排
  · 与立绘重叠面积 <= 20%（不再要求零遮挡：立绘在上层，允许挡住牌的一条边）
  · 字号 >= 15px（1x 屏宋体的可读下限）
  · 牌面文字 == 该专员名（角色串了 / 空名字当场暴露）
  · 文字不溢出牌面（Range 判据，容差 6.5px）
  · 无未捕获 JS 错误
  · 元断言：至少要有一位翻转专员、也要有非翻转对照组 —— 否则这轮等于没测到
    关键路径（全 false 说明几何数据坏了，"全绿"是假的）

隔离：配置用出厂默认的一份临时副本，WR_SETTINGS_PATH 指过去；真实
config/settings.json 前后 SHA-256 必须一致。一位专员一个服务是必须的：
fixedRole 在服务启动时读进内存。

用法：python scripts/probe/probe_role_title.py [--role 魔法师 ...]
退出码 0 = 全部通过。
"""

import argparse
import hashlib
import json
import math
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
PROBE_JS = os.path.join(HERE, "probe_role_title.js")
OUT = os.path.join(tempfile.gettempdir(), "wr_role_title")
# 牌与立绘允许的最大重叠面积占比。必须与 web/dist 的 break.js / takeout.js 里
# RT_OVERLAP_RATIO 同值 —— 这是“少许重叠”的唯一口径，两边不一致就等于没测。
OVERLAP_MAX = 0.20

try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass

_stat = {"n": 0, "fail": 0}


def valid_elide(full, shown):
    """牌面文字是否合法：等于原名，或“首段 + … + 尾段”（中间省略，首尾都在）。

    前半比后半多留一个字 —— 与 layoutRoleTitle 里的 elide() 同一个口径。
    注意这里的“合法”只校验形状，不校验字数：字数由几何反解，探针不该复刻那套算术。
    """
    if shown is None:
        return False
    if shown == full:
        return True
    if "…" not in shown:
        return False
    head, _, tail = shown.partition("…")
    if not head or not tail:
        return False
    if not (full.startswith(head) and full.endswith(tail)):
        return False
    if len(head) + len(tail) >= len(full):   # 首尾重叠 = 其实没省略掉任何字
        return False
    return len(head) == math.ceil((len(head) + len(tail)) / 2)


def check(ok, name, detail=""):
    _stat["n"] += 1
    if not ok:
        _stat["fail"] += 1
    mark = "ok  " if ok else "FAIL"
    print("  [%s] %s%s" % (mark, name, ("  — %s" % detail) if detail else ""))
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
    """磁盘现算，绝不写死内置专员个数/名单（增删专员不该改探针）。"""
    if not os.path.isdir(SUP_DIR):
        return []
    return sorted(d for d in os.listdir(SUP_DIR) if os.path.isdir(os.path.join(SUP_DIR, d)))


def run_one(role):
    """起一个隔离服务，把这位专员的三个场景量回来。"""
    iso = tempfile.mkdtemp(prefix="wr_rt_")
    try:
        cfg = json.load(open(FACTORY, encoding="utf-8"))
        sup = cfg.setdefault("supervisor", {})
        sup["enabled"] = True
        # 固定显示：会话里的督休专员必然就是 fixedRole 那位。随机/顺序模式下
        # 抽到谁不可控，量到的 hit 也就对不上“这个专员的立绘”。
        sup["selection"] = "fixed"
        sup["fixedRole"] = role
        sup["order"] = [role]          # 出厂 order 为空 = 用内置顺序；这里显式点名
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
                               encoding="utf-8", errors="replace", timeout=300,
                               capture_output=True)
            rows = None
            for line in (r.stdout or "").splitlines():
                if line.startswith("WR_JSON "):
                    rows = json.loads(line[len("WR_JSON "):])
            if rows is None:
                return None, "探针没有回传数据：" + ((r.stderr or "")[-600:])
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


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--role", action="append", default=None,
                    help="只跑指定专员（可多次；默认全跑）")
    args = ap.parse_args()

    if not os.path.exists(EXE):
        print("没有 %s（先 go build -tags server -o build/bin/rest_server.exe ./cmd/workerrest）" % EXE)
        return 2
    try:
        import playwright  # noqa: F401
    except Exception:
        pass
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

    print("督休专员题名零遮挡：%d 位专员 × 3 个版式" % len(roles))
    flips = {"on": 0, "off": 0}
    toe_n = 0
    wide_n = 0
    for role in roles:
        print("\n[%s]" % role)
        rows, err = run_one(role)
        if rows is None:
            check(False, "%s：整位专员跑完三个版式" % role, err)
            continue
        for d in rows:
            label = "%s %s" % (role, d.get("scene"))
            if d.get("error"):
                check(False, label + "：题名元素存在", d.get("error"))
                continue
            flips["on" if d.get("flip") else "off"] += 1
            check(d.get("positioned") is True, label + "：题名已完成实测定位",
                  "hit=%s fs=%s flip=%s" % (d.get("hit"), d.get("fs"), d.get("flip")))
            check(d.get("vertical") is True, label + "：题名是竖排", d.get("vertical"))
            # 单列是硬规则（2026-11-17）：长名允许缩字号、允许中间省略，但不准折列
            check(d.get("singleColumn") is True, label + "：题名单列（未折列）",
                  "text=%r" % (d.get("text"),))
            # 2026-11-17 起不再是“零遮挡”：允许牌与立绘少量重叠，立绘在上层压住牌
            # 的一条边（这比“为了不碰把长名缩成一根针”或“整块不显示”都好）。
            # 判据换成面积占比上限，与 web/dist 的 RT_OVERLAP_RATIO 同值。
            _ov = d.get("ov")
            check(isinstance(_ov, (int, float)) and _ov <= OVERLAP_MAX + 1e-6,
                  label + "：与立绘重叠 <= %.0f%%（牌在人物之上，压住的是袖口/肩线）"
                  % (OVERLAP_MAX * 100),
                  "ov=%s hit=%s fs=%s flip=%s"
                  % (_ov, d.get("hit"), d.get("fs"), d.get("flip")))
            # 允许重叠的前提是**牌在立绘之上**（2026-11-18 由“立绘在上层”反转）：
            # 牌顶下沉到颈线后右缘常压在袖口上十来个像素，若立绘在上会把最后一列
            # 字形切掉（实测“温柔学姐”的“姐”只剩半个）。比较的是公共祖先层叠上下文
            # 里的次序 —— 休息窗口里立绘在 .stage(8) 内、自身 z-index 7，直接比数值
            # 会误判成与牌同级。
            _nl, _fl = d.get("nameLayer"), d.get("figLayer")
            if _fl is not None:
                check(isinstance(_nl, (int, float)) and _nl > _fl,
                      label + "：题名在立绘之上（重叠处压住袖口，不切字形）",
                      "figLayer=%s nameLayer=%s" % (_fl, _nl))
            # 竖向锚点：牌**中心**必须落在“头顶到肩膀的中点”。0.17 是真机的 RT_MID_RATIO
            # （15 张内置立绘的肩线中位数 33.1% 取半）。上界 0.26 挡住“牌心往胸口掉”，
            # 收紧带 0.15~0.19 只对**没往回收**的用例判 —— 往回收是牌心那一档放不下时
            # 往上让（见 allowToe），那时牌心会明显高于锚点。
            _band = d.get("band")
            _mid = d.get("mid")
            _toe = d.get("toe") is True
            if _toe:
                toe_n += 1
            check(isinstance(_mid, (int, float)) and _mid <= 0.26,
                  label + "：牌心不落到肩膀以下（<= 可见高度 26%）",
                  "mid=%s band=%s toe=%s" % (_mid, _band, _toe))
            check(_toe or (isinstance(_mid, (int, float)) and 0.15 <= _mid <= 0.19),
                  label + "：牌心落在头顶到肩膀的中点（可见高度 15%~19%）",
                  "mid=%s band=%s toe=%s" % (_mid, _band, _toe))
            # 横向缝：RT_GAP(12) 只保证“不糊在一起”——26px 的字只有半个字宽，
            # 牌右缘几乎贴着鬓角，看着就是压在脸上。所以真机在左侧富余时把缝拉到
            # RT_GAP_COMFORT(32)，放不下才退回 12 甚至压人（后者由上面的重叠上限兜底）。
            # gap/slack 都是真机按 alpha 剖面逐图算的，不是 per-role 常量：用户上传
            # 新立绘走同一条路径，这条断言自动跟着变。
            _gap, _slack = d.get("gap"), d.get("slack")
            check(isinstance(_gap, (int, float)) and isinstance(_slack, (int, float)),
                  label + "：横向缝可测量", "gap=%s slack=%s" % (_gap, _slack))
            # 阈值取 32.5 而不是 32：slack ∈ [32, 32.5) 时“刚好放得下”，wide 与 leftMin
            # 只差不到半像素，真机会因为浮点比较回退到最小缝 —— 那是合法边界，不该判红。
            check(not isinstance(_slack, (int, float)) or _slack < 32.5 or
                  (isinstance(_gap, (int, float)) and _gap >= 31.5),
                  label + "：左侧放得下时拉开 32px 舒适缝（不贴脸）",
                  "gap=%s slack=%s" % (_gap, _slack))
            if isinstance(_slack, (int, float)) and _slack >= 32.5:
                wide_n += 1
            fs = d.get("fs")
            check(isinstance(fs, (int, float)) and fs >= 15,
                  label + "：题名字号 >= 15px", fs)
            # 牌面文字：要么就是原名，要么是原名掐掉中间一段的合法省略
            check(valid_elide(role, d.get("text")),
                  label + "：牌面文字 == 专员名（或合法中间省略）", repr(d.get("text")))
            check(d.get("overflow") is False, label + "：文字没有溢出牌面",
                  "box=%s" % (d.get("box"),))
            check(not d.get("errs"), label + "：页面无未捕获 JS 错误", d.get("errs"))

    # 关键路径的“失效断言”：翻转命中判据就是这次修的 bug，若这一轮一位翻转
    # 专员都没抽到，全绿毫无意义（几何数据坏了也会是这样）。
    check(flips["on"] > 0, "本轮覆盖到“水平翻转”立绘（误报 bug 的触发条件）",
          "flip=on %d / off %d" % (flips["on"], flips["off"]))
    check(flips["off"] > 0, "本轮同时有“未翻转”对照（不是只测镜像那一侧）",
          "flip=on %d / off %d" % (flips["on"], flips["off"]))
    # 元断言：往上让（牌心被抬离锚点）只能是少数。占多数说明锚点比例或立绘几何
    # 出了问题 —— 那时全绿也没意义，因为“牌心落在头肩中点”这件事根本没生效。
    _tot = sum(flips.values())
    check(_tot > 0 and toe_n <= _tot * 0.4,
          "往上让的用例 <= 40%（绝大多数专员真的落在头肩中点锚点上）",
          "toe=%d / %d" % (toe_n, _tot))
    # 元断言：舒适缝不能是“写了但走不到”的死代码。横屏/外卖窗里人物左侧通常有
    # 几十像素富余，真有这么多用例走不到，说明缝的判据或几何量错了。
    check(_tot > 0 and wide_n >= _tot * 0.3,
          "至少 30% 的用例真的用上了 32px 舒适缝（“往左让”这条路径被覆盖）",
          "wide=%d / %d" % (wide_n, _tot))

    after = digest(real_cfg)
    check(before == after, "真实 config/settings.json 未被改动", real_cfg)

    print("\n结果：%d 项，%d 失败" % (_stat["n"], _stat["fail"]))
    print("数据：%s" % OUT)
    return 0 if _stat["fail"] == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
