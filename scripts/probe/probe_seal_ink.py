#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""印泥墨块质感探针：中间完整、牌外无碎点（驱动端）。

为什么需要它：墨块不是图片，是 seal.js 现场用 SVG 滤镜画出来的 ——
轮廓用 feDisplacementMap 撕毛（±3px）、mask 里用 feTurbulence 挖孔、
再叠 grain 做颜色层次。**路径写对了不等于渲染后中央是完整的**：
  · 裂口（cuts）若从内部随机点起步，37px 宽的长牌上就是一道把块面纵劈
    成两半的黑线（2026-11-17 用户实测反馈的老行为）；
  · 针孔（mask 里的 #holes）若铺满全块，中央会变成麻点，同样是"碎"。
所以判据必须落在**真渲染出来的像素**上，而不是读参数。

断言（数据由 seal_ink_probe.js 采，判定在这里）：
  · cover    牌面矩形内墨色像素 ≥ 72%（墨块真的画上了 —— 也防止"整块没渲染
             所以中央很干净"这种假绿）
  · holePct  中央保护带（短轴中间 30%、长轴两头各留 10%）露底像素 ≤ 0.5%
  · gapMax   中央保护带里"整行/整列全空"的最大连续长度 ≤ 1px（真正的横断会顶到
             几十像素，边缘那点抗锯齿顶多 1）
  · edgeCover 边带（距短轴边缘 12%）墨色占比在 22%~60%：既要有撕口（不能是
             一刀切的矩形），也不能被啃空
  · gen.hasSpecks 必须是 False：2026-11-18 起牌外碎点层整层删除（用户决定），
             生成器上不该再有 specksSVG —— 留着就是死代码，谁加回来这条会红

覆盖：真机出现过的 5 种牌面尺寸（30x60 预览 / 37x418 超长 / 44x132 三字 /
50x88 两字 / 56x260 长名）+ 横牌兜底分支 90x40，每种 20 个种子。
纯函数探针，不启动服务、不写配置、不碰素材。

用法：python scripts/probe/probe_seal_ink.py
退出码 0 = 全部通过。
"""

import json
import math
import os
import shutil
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(os.path.dirname(HERE))
PROBE_JS = os.path.join(HERE, "seal_ink_probe.js")

# 阈值：全部对着改后实测的分布留余量（见文件头注释）
COVER_MIN = 0.72
HOLE_MAX = 0.5          # %
GAP_MAX = 1             # px
EDGE_MIN, EDGE_MAX = 0.22, 0.60

ok_n = 0
fail = []


def check(cond, label, detail=""):
    global ok_n
    if cond:
        ok_n += 1
        print("  ok   %s" % label)
    else:
        fail.append(label)
        print("  FAIL %s   %s" % (label, detail))


def main():
    if not shutil.which("node"):
        print("找不到 node（探针用 Playwright 驱动系统 Edge）")
        return 2
    if not os.path.exists(PROBE_JS):
        print("缺少 %s" % PROBE_JS)
        return 2

    env = dict(os.environ)
    env["NODE_PATH"] = os.path.join(ROOT, "scripts", "node_modules")
    r = subprocess.run(["node", PROBE_JS], cwd=ROOT, env=env, text=True,
                       encoding="utf-8", errors="replace", timeout=600,
                       capture_output=True)
    data = None
    for line in (r.stdout or "").splitlines():
        if line.startswith("WR_JSON "):
            data = json.loads(line[len("WR_JSON "):])
    rows = (data or {}).get("rows")
    if rows is None:
        print("探针没有回传数据：%s" % ((r.stderr or r.stdout or "")[-800:]))
        return 2

    print("印泥墨块：%d 组尺寸 × %d 个种子 = %d 块" % (
        len({x["size"] for x in rows}), len(rows) // len({x["size"] for x in rows}), len(rows)))

    by = {}
    for x in rows:
        by.setdefault(x["size"], []).append(x)

    for size, rs in by.items():
        print("\n[%s]" % size)
        worst_hole = max(rs, key=lambda x: x["holePct"])
        worst_gap = max(rs, key=lambda x: x["gapMax"])
        check(all(x["cover"] >= COVER_MIN for x in rs),
              "墨块铺满牌面（cover ≥ %.2f）" % COVER_MIN,
              "min=%.3f" % min(x["cover"] for x in rs))
        check(all(x["holePct"] <= HOLE_MAX for x in rs),
              "中央不带针孔（露底 ≤ %.1f%%）" % HOLE_MAX,
              "max=%.2f%% seed=%d" % (worst_hole["holePct"], worst_hole["seed"]))
        check(all(x["gapMax"] <= GAP_MAX for x in rs),
              "中央不断裂（最长全空 ≤ %dpx）" % GAP_MAX,
              "max=%d seed=%d" % (worst_gap["gapMax"], worst_gap["seed"]))
        check(all(EDGE_MIN <= x["edgeCover"] <= EDGE_MAX for x in rs),
              "边缘有撕口但不啃空（%.2f~%.2f）" % (EDGE_MIN, EDGE_MAX),
              "range=%.2f~%.2f" % (min(x["edgeCover"] for x in rs),
                                   max(x["edgeCover"] for x in rs)))

    print("\n[碎点层]")
    gen = (data or {}).get("gen") or {}
    check(gen.get("hasApply") is True, "SealGen.applyTo 仍在（墨块照常挂载）", gen)
    check(gen.get("hasSpecks") is False,
          "生成器上已无 specksSVG（2026-11-18 删掉牌外碎点）", gen)

    print("\n[纯函数契约]")
    ct = (data or {}).get("contract") or {}
    check(ct.get("sameSeed") is True, "同尺寸同种子逐字相同（可复用、无隐藏随机）", ct)
    check(ct.get("diffSeed") is True, "换种子形状必须变（每次出场换一枚章）", ct)
    check(ct.get("viewBox") is True, "viewBox 等于请求的像素尺寸（不被拉伸）", ct)
    check(ct.get("seedZero") is True and ct.get("seedMax") is True,
          "种子 0 与 2^32-1 不抛异常（边界不炸）", ct)
    check(ct.get("apply1") is True and ct.get("apply2") is True,
          "applyTo 正常挂载", ct)
    check(ct.get("reuse") is True, "同尺寸同种子复用（sealKey 不变）", ct)
    check(ct.get("hasInkVar") is True, "--rt-ink 是 data:image/svg+xml", ct)
    check(ct.get("sizeChangesKey") is True, "尺寸变化后重新生成", ct)
    check(ct.get("rejectTiny") is True, "退化盒（4px 以下）拒绝生成（不画小红点）", ct)

    # 元断言：不同尺寸必须真的换算了中央带，否则"全绿"只是没量到
    sizes = sorted(by)
    check(len(sizes) >= 8, "覆盖 ≥ 8 种牌面尺寸", "sizes=%s" % ",".join(sizes))
    check(any(int(s.split("x")[0]) > int(s.split("x")[1]) for s in sizes),
          "覆盖了横牌兜底分支", "sizes=%s" % ",".join(sizes))
    check(any(int(s.split("x")[1]) >= 600 for s in sizes),
          "覆盖超长牌（高 ≥ 600px）", "sizes=%s" % ",".join(sizes))

    print("\n结果：%d ok / %d fail" % (ok_n, len(fail)))
    if fail:
        print("失败：")
        for f in fail:
            print("  - %s" % f)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
