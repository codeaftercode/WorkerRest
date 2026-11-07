# -*- coding: utf-8 -*-
"""印泥墨块 SVG 生成器（生成 web/dist/seal-ink-N.svg + seal-specks-N.svg）

对照参考图（朱红印泥“端午节”）后确定的几条：
  1) 内部要“实”：不做大面积飞白，只有极淡纸纹 + 边沿零星小孔
  2) 轮廓要“手按感”：大块低频隆起 + 少量深咬口 + 高频位移把边撕毛
  3) 色要饱：基色 #a5261a（金 P10 对比 ≈4.0:1，够 26px 大字也够 17px 小字）
  4) 印泥没吃到纸上的地方要有“细小断裂”（mask 里挖空，透出窗口背景）

为什么要一组变体：同一枚章连盖两次也不会一模一样。每次出现由 JS 随机挑一张写进
--rt-ink / --rt-specks，轮廓胖瘦、咬口位置、撕裂走向、断裂、碎点全都不同。

为什么碎点单独一层（seal-specks-N.svg）：墨块的画布 = 牌子的盒子，而毛边位移就有
6 个单位（≈3px），盒子里那 9% 的余量全被毛边吃掉，碎点画在同一张图上必然和边糊成
一片（实测六个变体一个点都看不见）。碎点改挂在 .dialog-name::after（inset:-9%，
比盒子大一圈），它不参与 background-clip:text，也不会盖住金字（字是盒子内层的
background-clip:text 画的，碎点只落在牌子外的环带上）。
"""
import math
import os
import random
import sys

W, H = 100.0, 200.0
VARIANTS = 6


def harmonics(rng, ks, amp):
    return [(k, rng.uniform(0, 2 * math.pi), rng.uniform(0.45, 1.0) * amp) for k in ks]


def blob_points(seed, a, b, p, lumps, bites, n=128):
    rng = random.Random(seed)
    low = harmonics(rng, [2], lumps * 1.6)          # 整体一胖一瘦
    mid = harmonics(rng, [3, 5], lumps)             # 大隆起
    high = harmonics(rng, [9, 15], lumps * 0.35)    # 小起伏
    bts = [(rng.uniform(0, 2 * math.pi), rng.uniform(0.02, 0.07)) for _ in range(bites)]
    pts = []
    for i in range(n):
        t = 2 * math.pi * i / n
        ct, st = math.cos(t), math.sin(t)
        x = a * math.copysign(abs(ct) ** (2 / p), ct)
        y = b * math.copysign(abs(st) ** (2 / p), st)
        r = 1.0
        for k, ph, am in low + mid + high:
            r += am * math.cos(k * t + ph)
        for bt, depth in bts:
            d = math.atan2(math.sin(t - bt), math.cos(t - bt))
            r -= depth * math.exp(-(d / 0.075) ** 2)
        pts.append((W / 2 + x * r, H / 2 + y * r))
    return pts


def catmull(pts):
    n = len(pts)
    d = "M%.1f %.1f" % pts[0]
    for i in range(n):
        p0, p1, p2, p3 = pts[(i - 1) % n], pts[i], pts[(i + 1) % n], pts[(i + 2) % n]
        c1 = (p1[0] + (p2[0] - p0[0]) / 6.0, p1[1] + (p2[1] - p0[1]) / 6.0)
        c2 = (p2[0] - (p3[0] - p1[0]) / 6.0, p2[1] - (p3[1] - p1[1]) / 6.0)
        d += "C%.1f %.1f %.1f %.1f %.1f %.1f" % (c1[0], c1[1], c2[0], c2[1], p2[0], p2[1])
    return d + "Z"


def cuts(seed, a, b):
    """mask 里挖掉的东西：细小断裂（线）+ 缺墨小孔（点）。

    断裂短、带折、偶有分叉，第一条从边沿咬进去；这些都画成 mask 里的“黑”，
    挖空处透出的是窗口背景 —— 与真实印章“缺墨”一致。金色笔画是另一层
    background-clip:text，压在墨块之上，所以断裂不会伤到字。
    """
    rng = random.Random(seed + 71)
    out = []
    for i in range(rng.randint(2, 3)):
        edge = (i == 0)
        t = rng.uniform(0, 2 * math.pi)
        rr = rng.uniform(0.95, 1.02) if edge else rng.uniform(0.2, 0.62)
        x = W / 2 + a * math.cos(t) * rr
        y = H / 2 + b * math.sin(t) * rr
        ang = math.atan2(math.sin(t) * a, math.cos(t) * b) + rng.uniform(-0.7, 0.7)
        pts = ["M%.1f %.1f" % (x, y)]
        segs = rng.randint(2, 4)
        total = rng.uniform(11, 23)
        for _ in range(segs):
            ang += rng.uniform(-0.8, 0.8)
            step = total / segs * rng.uniform(0.65, 1.35)
            x += math.cos(ang) * step
            y += math.sin(ang) * step
            pts.append("L%.1f %.1f" % (x, y))
            if rng.random() < 0.25:                     # 分叉
                a2 = ang + rng.choice([-1, 1]) * rng.uniform(0.5, 1.1)
                L2 = total * rng.uniform(0.18, 0.4)
                pts.append("M%.1f %.1f L%.1f %.1f"
                           % (x, y, x + math.cos(a2) * L2, y + math.sin(a2) * L2))
        out.append('<path d="%s" fill="none" stroke="#000" stroke-width="%.1f" '
                   'stroke-linecap="round" stroke-linejoin="round" opacity="%.2f"/>'
                   % (" ".join(pts), rng.uniform(1.7, 2.9),
                      rng.choice([0.8, 0.9, 1.0])))
    # 缺墨小孔：1~3 个，位置在牌面内部，像沾得不匀
    for _ in range(rng.randint(1, 3)):
        t = rng.uniform(0, 2 * math.pi)
        rr = rng.uniform(0.25, 0.8)
        out.append('<circle cx="%.1f" cy="%.1f" r="%.1f" opacity="%.2f"/>'
                   % (W / 2 + a * math.cos(t) * rr, H / 2 + b * math.sin(t) * rr,
                      rng.uniform(0.8, 1.8), rng.choice([0.6, 0.75, 0.9])))
    return out


def build(seed, a=41.0, b=93.0, p=5.0, lumps=0.024, bites=3,
          fray_scale=11.0, fray_freq="0.07 0.042", fray_seed=11,
          base="#a5261a", edge="#6b1510",
          holes_op=0.10, grain_op=0.05, grain_freq="0.5", tilt=0.0):
    path = catmull(blob_points(seed, a, b, p, lumps, bites))
    hole_layer = ""
    if holes_op > 0:
        hole_layer = ('<rect x="0" y="0" width="100" height="200" filter="url(#holes)" '
                      'fill="#000" opacity="%.2f"/>' % holes_op)
    cr = "\n      " + "\n      ".join(cuts(seed, a, b))
    rot = (' transform="rotate(%.2f 50 100)"' % tilt) if tilt else ""
    return f'''<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 200" preserveAspectRatio="none">
  <defs>
    <!-- 轮廓只写一次：mask / 底色 / 压边三处都用 <use>（整份 SVG 从 15KB 降到 ~8KB，
         否则同一个 d 要重复三遍，6 个变体就是 90KB） -->
    <path id="blob" d="{path}"/>
    <!-- 边缘撕裂：低频大抖（撕裂）+ 高频细抖（纤维） -->
    <filter id="fray" x="-15%" y="-11%" width="130%" height="122%">
      <feTurbulence type="fractalNoise" baseFrequency="{fray_freq}" numOctaves="4"
                    seed="{fray_seed}" result="w"/>
      <feDisplacementMap in="SourceGraphic" in2="w" scale="{fray_scale}"
                         xChannelSelector="R" yChannelSelector="G"/>
    </filter>
    <!-- 边缘零星小孔：只在轮廓附近明显，内部极淡 -->
    <filter id="holes" x="0" y="0" width="100%" height="100%">
      <feTurbulence type="fractalNoise" baseFrequency="0.22" numOctaves="3" seed="13"/>
      <feColorMatrix type="matrix" values="0 0 0 0 0  0 0 0 0 0  0 0 0 0 0  .5 .5 .5 0 0"/>
      <feComponentTransfer><feFuncA type="discrete"
        tableValues="0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 1"/></feComponentTransfer>
    </filter>
    <!-- 纸纹：低频、极淡，只负责“不是纯色块” -->
    <filter id="grain" x="0" y="0" width="100%" height="100%">
      <feTurbulence type="fractalNoise" baseFrequency="{grain_freq}" numOctaves="2" seed="4"/>
      <feColorMatrix type="saturate" values="0"/>
      <feComponentTransfer><feFuncA type="linear" slope="0.6"/></feComponentTransfer>
    </filter>
    <!-- 浓淡层：0→0 的径向，看不出边 -->
    <radialGradient id="shadeA" cx="30%" cy="74%" r="62%">
      <stop offset="0" stop-color="#480c09" stop-opacity="0.42"/>
      <stop offset="1" stop-color="#480c09" stop-opacity="0"/>
    </radialGradient>
    <radialGradient id="shadeB" cx="72%" cy="24%" r="58%">
      <stop offset="0" stop-color="#480c09" stop-opacity="0.30"/>
      <stop offset="1" stop-color="#480c09" stop-opacity="0"/>
    </radialGradient>
    <radialGradient id="hi" cx="54%" cy="48%" r="42%">
      <stop offset="0" stop-color="#c8422c" stop-opacity="0.26"/>
      <stop offset="1" stop-color="#c8422c" stop-opacity="0"/>
    </radialGradient>
    <mask id="ink">
      <use href="#blob" fill="#fff"/>{hole_layer}{cr}
    </mask>
  </defs>

  <g filter="url(#fray)">
    <g mask="url(#ink)"{rot}>
      <use href="#blob" fill="{base}"/>
      <!-- 浓淡：两片偏心积墨 + 一片提亮，全部渐变到 0 透明度（硬椭圆会被一眼看穿） -->
      <rect x="0" y="0" width="100" height="200" fill="url(#shadeA)"/>
      <rect x="0" y="0" width="100" height="200" fill="url(#shadeB)"/>
      <rect x="0" y="0" width="100" height="200" fill="url(#hi)"/>
      <!-- 边沿一线压深：让轮廓“吃”进背景，不做外发光 -->
      <use href="#blob" fill="none" stroke="{edge}" stroke-opacity="0.5" stroke-width="2.0"/>
      <rect x="0" y="0" width="100" height="200" filter="url(#grain)" opacity="{grain_op}"
            style="mix-blend-mode:multiply"/>
    </g>
  </g>
</svg>
'''


def build_specks(seed, base="#a5261a"):
    """牌子四周的细碎印泥（单独一层，画布 = ::after 那圈 -9% 环带）。

    点位只落在画布外缘这一圈：::after 比牌子盒子大 9%，牌子的红色边缘还要
    再往里 ~9 个单位，所以这一圈恒在印泥外，碎点永远飘在红块外面/边沿上。

    尺度的唯一判据是“1x 下看不看得见”——不能按画布单位凭感觉给：
      牌子 35px 宽时 1 单位 = 0.41px，r=0.7 的圆点直径只有 0.58px（实测看不见）
      → 圆点半径取 1.5~2.6（偶尔 2.8~3.6），1x 直径 2.5~3.0px，才是一粒印泥渣
    同理半径越大越要离画布边远一点，否则被 SVG 视口裁成直边（d 的下限跟着 r 走）。
    """
    rng = random.Random(seed + 31)
    out = []
    for _ in range(rng.randint(8, 11)):
        side = rng.randint(0, 3)
        r = rng.uniform(2.8, 3.6) if rng.random() < 0.28 else rng.uniform(1.5, 2.6)
        t = rng.uniform(0.03, 0.97)              # 沿这条边走的比例
        if side in (0, 1):                       # 上 / 下边：band 单位是 H 的比例
            d = rng.uniform(max(0.026, (r + 0.8) / H), 0.095)
            x = t * W
            y = d * H if side == 0 else (1 - d) * H
        else:                                    # 左 / 右边：band 单位是 W 的比例
            d = rng.uniform(max(0.05, (r + 0.8) / W), 0.14)
            x = d * W if side == 2 else (1 - d) * W
            y = t * H
        op = rng.choice([0.55, 0.68, 0.8, 0.92])
        if rng.random() < 0.28:                  # 擦出来的拖痕
            out.append('<ellipse cx="%.1f" cy="%.1f" rx="%.1f" ry="%.1f" '
                       'transform="rotate(%.0f %.1f %.1f)" opacity="%.2f"/>'
                       % (x, y, r * rng.uniform(1.6, 2.4), r * rng.uniform(0.28, 0.42),
                          rng.uniform(0, 180), x, y, op * 0.85))
        else:
            out.append('<circle cx="%.1f" cy="%.1f" r="%.1f" opacity="%.2f"/>'
                       % (x, y, r, op))
            if rng.random() < 0.5:               # 一小簇
                for _ in range(rng.randint(1, 2)):
                    out.append('<circle cx="%.1f" cy="%.1f" r="%.1f" opacity="%.2f"/>'
                               % (x + rng.uniform(-4, 4), y + rng.uniform(-5, 5),
                                  r * rng.uniform(0.3, 0.55), op * rng.uniform(0.7, 0.9)))
    body = "\n  ".join(s.replace("<circle", '<circle fill="%s"' % base)
                        .replace("<ellipse", '<ellipse fill="%s"' % base) for s in out)
    return f'''<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 200" preserveAspectRatio="none">
  <!-- 细碎印泥：只出现在外缘一圈，配合 .dialog-name::after 的负 inset 使用 -->
  {body}
</svg>
'''


def variant_params(i):
    """第 i 个变体（1 起）。a/b 由 safe_ab 兜底，p 只改“方的程度”不动中轴。"""
    rng = random.Random(90210 + i * 7919)
    return dict(
        seed=100000 * i + rng.randint(1, 99999),
        p=rng.uniform(4.2, 6.4),
        lumps=rng.uniform(0.018, 0.034),
        bites=rng.randint(2, 4),
        fray_scale=rng.uniform(7.0, 9.0),
        fray_freq=rng.choice(["0.07 0.042", "0.075 0.045", "0.065 0.04"]),
        fray_seed=rng.randint(1, 400),
        holes_op=rng.uniform(0.07, 0.12),
        grain_op=rng.uniform(0.04, 0.06),
        grain_freq=rng.choice(["0.5", "0.42", "0.55"]),
        tilt=rng.uniform(-0.8, 0.8),
    )


def safe_ab(params, a=41.0, b=93.0):
    """把 a/b 微调（只缩不放）到“旋转 + 位移之后仍不顶出 viewBox”为止。

    顶出去会被 SVG 视口裁成直边 —— 那是唯一会让墨块瞬间破功的情况，
    所以宁可让个别变体小一圈（内缩 5~8 单位，字照样完全包住）。
    """
    th = math.radians(params["tilt"])
    half = params["fray_scale"] * 0.5
    pts, bx, by, aa, bb = [], 0.0, 0.0, a, b
    for k in (1.0, 0.985, 0.97, 0.955, 0.94, 0.925):
        aa, bb = a * k, b * k
        pts = blob_points(params["seed"], aa, bb, params["p"], params["lumps"], params["bites"])
        bx = max(abs((q[0] - 50) * math.cos(th) - (q[1] - 100) * math.sin(th)) for q in pts) + half
        by = max(abs((q[0] - 50) * math.sin(th) + (q[1] - 100) * math.cos(th)) for q in pts) + half
        if bx <= 47.6 and by <= 97.2:
            break
    return aa, bb, pts, bx, by


def main():
    count = VARIANTS
    if len(sys.argv) > 1:
        count = int(sys.argv[1])
    root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
    out_dir = os.path.join(root, "cmd", "workerrest", "web", "dist")
    total = 0
    for i in range(1, count + 1):
        params = variant_params(i)
        a, b, pts, bx, by = safe_ab(params)
        for name, svg in (("seal-ink-%d.svg" % i, build(a=a, b=b, **params)),
                          ("seal-specks-%d.svg" % i, build_specks(params["seed"]))):
            with open(os.path.join(out_dir, name), "wb") as f:
                f.write(svg.encode("utf-8"))
            total += len(svg.encode("utf-8"))
        inset = min(min(q[0] for q in pts), min(q[1] for q in pts),
                    100 - max(q[0] for q in pts), 200 - max(q[1] for q in pts))
        print("变体 %d  内缩 %4.1f  外扩 %4.1f/50 , %5.1f/100  p=%.1f 倾角 %+.2f"
              % (i, inset, bx, by, params["p"], params["tilt"]))
    print("%d 组变体，共 %d 字节" % (count, total))


if __name__ == "__main__":
    main()
