/**
 * 印泥墨块 · 运行时生成
 *
 * 2026-11-18：**牌外那圈碎印泥整层删除**（用户：「外围碎点似乎不好看，去掉外围碎点」）。
 *   原先另有一张 specksSVG 铺在 ::after（盒子外扩 10px）上，与墨块同一枚种子。删掉后
 *   ::after 整条规则也一起撤了 —— 没有背景图的空伪元素只会在命中测试里多一层，
 *   留着比删掉更容易被误认为“碎点还在”。
 *   代价：牌子的手作感弱一点，换来的是牌面轮廓干净、也不再有“贴着窗口左缘被裁掉
 *   半颗点”的边界情况（RT_EDGE=6 < BAND=10 那条已知瑕疵随之消失）。
 *
 * 为什么不再"预制 6 组 SVG 拉伸"：
 *   预制的 viewBox 恒为 0 0 100 200，靠 preserveAspectRatio="none" 硬铺满盒子。
 *   短名 50×88 时缩放 0.50/0.44，基本等比；长名 37×418 时却是 0.37/2.09 ——
 *   各向异性 5.6 倍。墨块没有可识别几何，拉伸本身还忍得住，但**噪声被一起拉了**：
 *     · feTurbulence 的毛边波长被纵向拉成 ~50px，"纤维撕裂"变成"橡皮筋"。
 *   这里把 viewBox 换成**牌面的实际像素尺寸**，所有尺度按短边比例给，
 *   于是任何牌面比例下毛边粗细、咬口深度、断裂长度都是同一个物理量级。
 *
 * 用法（CSS 一行都不用改：--rt-ink 的值本来就是 url(...)）：
 *   SealGen.applyTo(el, w, h, seed)   // w/h = 牌子的 border-box 实测尺寸
 * 注意调用时机：必须**量完最终字号**之后 —— 墨块画在 border-box 上，
 * 牌子尺寸由 padding + 字号决定，字号没定就量不到真实尺寸。
 * 兜底不再是文件：本文件没加载、或生成抛异常时，--rt-ink 保持未设，
 * CSS 里的 var() 落到 linear-gradient 暗朱墨块 —— 牌子仍是红块，不是裸字，且没有 404。
 *
 * 轮廓采样按**弧长**均匀（每 3.2px 一个控制点），不能按角度：1:11 的椭圆上
 * 两条长边只分到 3% 的采样点，中段没有控制点，毛边在长边上直接消失
 * （实测红块两侧变成笔直的两条线）。
 */
(function (root) {
  "use strict";

  var BASE = "#a5261a";   // 三份 CSS 的 --rt-ink 兜底同值（墨块本体中位数）
  var EDGE = "#6b1510";

  /* 32 位种子随机（mulberry32）：同一 seed 在任何机器上出同一张图 */
  function rng32(seed) {
    var a = seed >>> 0;
    return function () {
      a = (a + 0x6d2b79f5) >>> 0;
      var t = a;
      t = Math.imul(t ^ (t >>> 15), 1 | t);
      t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
      return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
    };
  }

  /* 字符串 → 稳定 seed（预留：若要让"同一专员永远同一枚章"） */
  function hashSeed(str) {
    var h = 2166136261 >>> 0;
    for (var i = 0; i < str.length; i++) {
      h ^= str.charCodeAt(i);
      h = Math.imul(h, 16777619) >>> 0;
    }
    return h >>> 0;
  }

  /* 超椭圆（p 越大越方）。中心必须显式传入：漏掉就是"轮廓一半在画布外" */
  function superellipse(cx, cy, a, b, p, n) {
    var out = new Array(n);
    for (var i = 0; i < n; i++) {
      var t = (2 * Math.PI * i) / n;
      var ct = Math.cos(t), st = Math.sin(t);
      out[i] = [
        cx + a * (ct < 0 ? -1 : 1) * Math.pow(Math.abs(ct), 2 / p),
        cy + b * (st < 0 ? -1 : 1) * Math.pow(Math.abs(st), 2 / p),
      ];
    }
    return out;
  }

  /* 按弧长取点：返回 [x, y, 外法向x, 外法向y] */
  function sampleAt(base, cum, total, s) {
    var m = base.length;
    var target = (s - Math.floor(s)) * total;
    var lo = 0, hi = m;
    while (lo + 1 < hi) {
      var mid = (lo + hi) >> 1;
      if (cum[mid] <= target) lo = mid; else hi = mid;
    }
    var seg = cum[lo + 1] - cum[lo] || 1;
    var f = (target - cum[lo]) / seg;
    var p1 = base[lo % m], p2 = base[(lo + 1) % m];
    var dx = p2[0] - p1[0], dy = p2[1] - p1[1];
    var len = Math.hypot(dx, dy) || 1;
    /* 超椭圆沿 t 增大是逆时针，外法向 = 切线顺时针转 90° = (dy, -dx) */
    return [p1[0] + dx * f, p1[1] + dy * f, dy / len, -dx / len];
  }

  function catmull(pts) {
    var n = pts.length;
    var d = "M" + pts[0][0].toFixed(1) + " " + pts[0][1].toFixed(1);
    for (var i = 0; i < n; i++) {
      var p0 = pts[(i - 1 + n) % n], p1 = pts[i],
          p2 = pts[(i + 1) % n], p3 = pts[(i + 2) % n];
      var c1x = p1[0] + (p2[0] - p0[0]) / 6, c1y = p1[1] + (p2[1] - p0[1]) / 6;
      var c2x = p2[0] - (p3[0] - p1[0]) / 6, c2y = p2[1] - (p3[1] - p1[1]) / 6;
      d += "C" + c1x.toFixed(1) + " " + c1y.toFixed(1) + " " +
           c2x.toFixed(1) + " " + c2y.toFixed(1) + " " +
           p2[0].toFixed(1) + " " + p2[1].toFixed(1);
    }
    return d + "Z";
  }

  /**
   * 轮廓：超椭圆基准 + 弧长域的隆起/咬口。
   * 幅度一律按**短边**比例（短名上合计 ≈±1px，与旧预制件观感对齐）。
   */
  function outline(w, h, rnd, inset) {
    var a = Math.max(2, w / 2 - inset);
    var b = Math.max(2, h / 2 - inset);
    var p = 4.2 + rnd() * 2.2;
    var shortSide = Math.min(w, h);
    var M = 720;
    var base = superellipse(w / 2, h / 2, a, b, p, M);
    var cum = new Float64Array(M + 1);
    for (var i = 1; i <= M; i++) {
      var q = base[i % M], r = base[i - 1];
      cum[i] = cum[i - 1] + Math.hypot(q[0] - r[0], q[1] - r[1]);
    }
    var total = cum[M];

    var lumps = [];
    var nl = 3 + Math.floor(rnd() * 3);
    for (var j = 0; j < nl; j++) {
      lumps.push({
        k: 1 + Math.floor(rnd() * 6),
        ph: rnd() * 2 * Math.PI,
        amp: shortSide * 0.022 * (0.45 + rnd() * 0.55),
      });
    }
    /* 咬口：不许深过左右 padding，否则会啃到金字 */
    var maxBite = shortSide * 0.055;
    var bites = [];
    var nb = 2 + Math.floor(rnd() * 2);
    for (var k2 = 0; k2 < nb; k2++) {
      bites.push({
        s0: rnd(),
        depth: maxBite * (0.40 + rnd() * 0.60),
        width: 7 / total + rnd() * 9 / total,
      });
    }
    function offsetAt(s) {
      var d = 0;
      for (var i2 = 0; i2 < lumps.length; i2++) {
        d += lumps[i2].amp * Math.cos(2 * Math.PI * lumps[i2].k * s + lumps[i2].ph);
      }
      for (var j2 = 0; j2 < bites.length; j2++) {
        var ds = s - bites[j2].s0;
        ds -= Math.round(ds);                       /* 环形最短距离 */
        d -= bites[j2].depth * Math.exp(-Math.pow(ds / bites[j2].width, 2));
      }
      return d;
    }

    /* 每 3.2px 一个控制点；上限 400 防超长牌把 path 撑成几十 KB */
    var N = Math.max(88, Math.min(400, Math.round(total / 3.2)));
    var pts = [];
    for (var s2 = 0; s2 < N; s2++) {
      var smp = sampleAt(base, cum, total, s2 / N);
      var off = offsetAt(s2 / N);
      pts.push([smp[0] + smp[2] * off, smp[1] + smp[3] * off]);
    }
    return { d: catmull(pts), total: total };
  }

  /**
   * mask 里挖掉的东西：边缘的缺墨咬口 + 少量针孔。
   *
   * 2026-11-17 改（用户：「墨块中间要完整不要断裂」）：
   *   旧版每条裂口从**随机内部点**起步、顺着长轴走 shortSide×(0.14~0.24)，还带分叉，
   *   最长约 0.34×短边 —— 37px 宽的长牌上就是一道把块面纵劈成两半的黑线，短名
   *   （50×88）上也常有一道从边界直插中央的缝。
   *   现在只有一条规则：**裂口一律从边界往里咬，且不许进入中央保护带**。
   *   沿短轴量，距中轴 < shortSide×0.26 的区间（≈ 中间 52%）是保护区，
   *   路径上任何一个点越界就地收口。于是无论种子怎么抽，中轴沿线恒是完整的一条红。
   *   针孔同理，只落在两侧边带里 —— 中央留白（针孔在中央也是"碎"）。
   *
   * 尺寸仍取"短名实测的像素值"：预置版 11~23 个 viewBox 单位 ×0.5 = 5.5~11.5px、
   * stroke 1.7~2.9 ×0.5 = 0.85~1.45px。照抄单位会让一条划痕横穿 37px 的块面。
   */
  function cuts(rnd, w, h, inset) {
    var shortSide = Math.min(w, h);
    var vertical = h >= w;                       /* 竖牌：短轴 = 水平方向 */
    var coreHalf = shortSide * 0.26;             /* 距中轴这么近就算中央 */
    var maxDepth = shortSide / 2 - coreHalf;     /* 边带厚度 = 允许咬进去的最大深度 */

    /* 距最近一条长边的距离（沿短轴量）。越界 = 咬进中央了。 */
    function depthOk(x, y) {
      var d = vertical ? Math.min(x, w - x) : Math.min(y, h - y);
      return d <= maxDepth + 0.5;                /* +0.5 是给折线离散化的余量 */
    }

    var out = [];
    var n = 2 + Math.floor(rnd() * 2);

    /* 起点坐标：恒在边界上（再往外 0.8px，保证"从外面咬进来"）。
       贴着短轴的那两条边只在边带内下口 —— 否则一开口就在中央保护带里。 */
    var lo = Math.max(0.5, inset * 0.5);
    var hi = Math.max(lo + 0.5, maxDepth);
    function bandAt(len) {
      return rnd() < 0.5 ? lo + rnd() * (hi - lo) : len - lo - rnd() * (hi - lo);
    }
    function alongLong(inset2, len) {              /* 沿长轴自由取点（端头留 inset2） */
      var a = Math.min(inset2, len / 2 - 0.5);
      return a + rnd() * Math.max(0.5, len - a * 2);
    }

    for (var i = 0; i < n; i++) {
      var side = Math.floor(rnd() * 4);            /* 0 上 1 下 2 左 3 右 */
      var x, y, ang;
      if (vertical) {
        if (side === 0)      { x = bandAt(w);           y = -0.8; ang = Math.PI / 2; }
        else if (side === 1) { x = bandAt(w);           y = h + 0.8; ang = -Math.PI / 2; }
        else if (side === 2) { x = -0.8;                y = alongLong(inset, h); ang = 0; }
        else                 { x = w + 0.8;             y = alongLong(inset, h); ang = Math.PI; }
      } else {
        if (side === 0)      { x = alongLong(inset, w); y = -0.8; ang = Math.PI / 2; }
        else if (side === 1) { x = alongLong(inset, w); y = h + 0.8; ang = -Math.PI / 2; }
        else if (side === 2) { x = -0.8;                y = bandAt(h); ang = 0; }
        else                 { x = w + 0.8;             y = bandAt(h); ang = Math.PI; }
      }
      ang += (rnd() - 0.5) * 0.9;                  /* 咬口不必垂直，但不许拐向中央 */

      var pts = ["M" + x.toFixed(1) + " " + y.toFixed(1)];
      var segs = 2 + Math.floor(rnd() * 3);
      var total = shortSide * (0.16 + rnd() * 0.10);
      var drew = 0;
      for (var s = 0; s < segs; s++) {
        ang += (rnd() - 0.5) * 1.0;
        var step = (total / segs) * (0.65 + rnd() * 0.7);
        var nx = x + Math.cos(ang) * step;
        var ny = y + Math.sin(ang) * step;
        if (!depthOk(nx, ny)) break;               /* 碰到中央保护带：就地收口 */
        x = nx;
        y = ny;
        pts.push("L" + x.toFixed(1) + " " + y.toFixed(1));
        drew++;
        if (rnd() < 0.22) {                        /* 偶有分叉（同样不许进中央） */
          var a2 = ang + (rnd() < 0.5 ? -1 : 1) * (0.5 + rnd() * 0.6);
          var L2 = total * (0.18 + rnd() * 0.22);
          var bx = x + Math.cos(a2) * L2, by = y + Math.sin(a2) * L2;
          if (depthOk(bx, by)) {
            pts.push("M" + x.toFixed(1) + " " + y.toFixed(1) +
                     "L" + bx.toFixed(1) + " " + by.toFixed(1));
            drew++;
          }
        }
      }
      if (!drew) continue;                         /* 一步都没走成就不留空 path */
      out.push('<path d="' + pts.join(" ") + '" fill="none" stroke="#000" stroke-width="' +
               (0.85 + rnd() * 0.6).toFixed(2) +
               '" stroke-linecap="round" stroke-linejoin="round" opacity="' +
               [0.8, 0.9, 1.0][Math.floor(rnd() * 3)].toFixed(2) + '"/>');
    }
    for (var p2 = 0, np = Math.floor(rnd() * 2); p2 < np; p2++) {
      /* 针孔只在边带里：中央的孔同样算"断"。抽 8 次还落不进边带就放弃这一颗 */
      var px = 0, py = 0, ok = false;
      for (var g = 0; g < 8 && !ok; g++) {
        px = inset + rnd() * (w - inset * 2);
        py = inset + rnd() * (h - inset * 2);
        ok = depthOk(px, py);
      }
      if (!ok) continue;
      out.push('<circle cx="' + px.toFixed(1) + '" cy="' + py.toFixed(1) +
               '" r="' + (0.4 + rnd() * 0.5).toFixed(2) + '" opacity="' +
               [0.6, 0.75, 0.9][Math.floor(rnd() * 3)].toFixed(2) + '"/>');
    }
    return out.join("\n      ");
  }

  /**
   * 牌底墨块。w/h = 牌子的 border-box 像素尺寸。
   */
  /**
   * @param {number} w    牌子 border-box 宽（px）
   * @param {number} h    牌子 border-box 高（px）
   * @param {number|string} seed 32 位种子
   * @param {{shadow?: boolean}} [opts] shadow=true 时把投影**画进 SVG**
   *   为什么不是 CSS filter: drop-shadow：滤镜区域恒为盒子各外扩 10%，也就是
   *   37px 宽的牌子上只有 3.7px —— 3px 模糊的投影还没散开就被裁成一条硬边。
   *   画进 SVG 后元素上不挂 filter，影子跟着撕毛后的轮廓走，也不会被滤镜区域切。
   */
  function inkSVG(w, h, seed, opts) {
    w = Math.max(12, Math.round(w));
    h = Math.max(12, Math.round(h));
    var withShadow = !!(opts && opts.shadow);
    var sd = typeof seed === "string" ? hashSeed(seed) : (seed >>> 0);
    var rnd = rng32(sd);

    /* 内缩：短边 6%（≈ 预置版 100 宽里半轴 41 的比例），毛边位移一半在盒内、
       一半压在边界上 —— 预置版也是这么"顶格"画的，红块看着才饱满 */
    var shortSide = Math.min(w, h);
    /* feDisplacementMap 的 scale 是**总跨度**（±scale/2）；短边 14%（上限 6.2）
       → 37px 宽上 ±2.5px，与预置版短名实测的 ±1.3px 同量级 */
    var frayScale = Math.min(6.2, shortSide * 0.14);
    var inset = Math.max(1.8, shortSide * 0.06);
    var shape = outline(w, h, rnd, inset);

    /* 噪声波长按 px 给（横向 2.8px、纵向 3.6px）：任何尺寸下"纤维"粗细一致。
       预置版是 14.3/23.8 个 viewBox 单位，映射到长牌上被纵向拉成 ~50px 波长。 */
    var frayFx = "0.357";
    var frayFy = "0.278";
    var holesFreq = (1 / 2.3).toFixed(4);
    var grainFreq = (1 / 1.4).toFixed(4);
    var holesOp = (0.055 + rnd() * 0.035).toFixed(3);
    var grainOp = (0.04 + rnd() * 0.02).toFixed(3);

    /* 针孔（#holes）只洒在两侧边带：中央的孔在红块上既是"碎"、也是"断"的观感来源
       —— 2026-11-17「墨块中间要完整」，中央只留颜色层次的 grain（不透明的暗噪），
       不再露底色。coreHalf 与 cuts() 同一条公式（短边的 26%）。 */
    var shortSideH = Math.min(w, h);
    var bandW = Math.max(2, shortSideH / 2 - shortSideH * 0.26);
    var holesRects = h >= w
      ? '\n      <rect x="0" y="0" width="' + bandW.toFixed(1) + '" height="' + h +
        '" filter="url(#holes)" fill="#000" opacity="' + holesOp + '"/>' +
        '\n      <rect x="' + (w - bandW).toFixed(1) + '" y="0" width="' + bandW.toFixed(1) +
        '" height="' + h + '" filter="url(#holes)" fill="#000" opacity="' + holesOp + '"/>'
      : '\n      <rect x="0" y="0" width="' + w + '" height="' + bandW.toFixed(1) +
        '" filter="url(#holes)" fill="#000" opacity="' + holesOp + '"/>' +
        '\n      <rect x="0" y="' + (h - bandW).toFixed(1) + '" width="' + w + '" height="' +
        bandW.toFixed(1) + '" filter="url(#holes)" fill="#000" opacity="' + holesOp + '"/>';

    return '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ' + w + " " + h +
      '" preserveAspectRatio="none">' +
      "\n  <defs>" +
      '\n    <path id="blob" d="' + shape.d + '"/>' +
      /* 轮廓只写一次，mask / 底色 / 压边三处共用 */
      '\n    <filter id="fray" x="-8%" y="-8%" width="116%" height="116%">' +
      '\n      <feTurbulence type="fractalNoise" baseFrequency="' + frayFx + " " + frayFy +
      '" numOctaves="4" seed="' + Math.floor(rnd() * 400) + '" result="w"/>' +
      '\n      <feDisplacementMap in="SourceGraphic" in2="w" scale="' + frayScale.toFixed(2) +
      '" xChannelSelector="R" yChannelSelector="G"/>' +
      '\n    </filter>' +
      '\n    <filter id="holes" x="0" y="0" width="100%" height="100%">' +
      '\n      <feTurbulence type="fractalNoise" baseFrequency="' + holesFreq +
      '" numOctaves="3" seed="13"/>' +
      '\n      <feColorMatrix type="matrix" values="0 0 0 0 0  0 0 0 0 0  0 0 0 0 0  .5 .5 .5 0 0"/>' +
      '\n      <feComponentTransfer><feFuncA type="discrete" tableValues="0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 1"/></feComponentTransfer>' +
      '\n    </filter>' +
      '\n    <filter id="grain" x="0" y="0" width="100%" height="100%">' +
      '\n      <feTurbulence type="fractalNoise" baseFrequency="' + grainFreq + '" numOctaves="2" seed="4"/>' +
      '\n      <feColorMatrix type="saturate" values="0"/>' +
      '\n      <feComponentTransfer><feFuncA type="linear" slope="0.6"/></feComponentTransfer>' +
      '\n    </filter>' +
      '\n    <radialGradient id="shadeA" cx="30%" cy="74%" r="62%">' +
      '\n      <stop offset="0" stop-color="#480c09" stop-opacity="0.42"/>' +
      '\n      <stop offset="1" stop-color="#480c09" stop-opacity="0"/>' +
      '\n    </radialGradient>' +
      '\n    <radialGradient id="shadeB" cx="72%" cy="24%" r="58%">' +
      '\n      <stop offset="0" stop-color="#480c09" stop-opacity="0.30"/>' +
      '\n      <stop offset="1" stop-color="#480c09" stop-opacity="0"/>' +
      '\n    </radialGradient>' +
      '\n    <radialGradient id="hi" cx="54%" cy="48%" r="42%">' +
      '\n      <stop offset="0" stop-color="#c8422c" stop-opacity="0.26"/>' +
      '\n      <stop offset="1" stop-color="#c8422c" stop-opacity="0"/>' +
      '\n    </radialGradient>' +
      /* 投影：等效于 CSS drop-shadow(0 2px 3px rgba(0,0,0,.55))。
         套在 #fray 上而不是直接用 #blob —— 影子必须跟着**撕毛后的**轮廓走，否则
         毛边外凸的地方会露出红块、内凹的地方会在牌面上留一圈暗色描边。 */
      (withShadow
        ? '\n    <filter id="ds" x="-20%" y="-20%" width="140%" height="140%">' +
          '\n      <feGaussianBlur stdDeviation="1.5"/>' +
          '\n    </filter>'
        : "") +
      '\n    <mask id="ink">' +
      '\n      <use href="#blob" fill="#fff"/>' +
      holesRects +
      "\n      " + cuts(rnd, w, h, inset) +
      "\n    </mask>" +
      "\n  </defs>" +
      (withShadow
        ? '\n  <g transform="translate(0,2)" filter="url(#ds)">' +
          '\n    <use href="#blob" fill="#000" opacity="0.55" filter="url(#fray)"/>' +
          '\n  </g>'
        : "") +
      '\n  <g filter="url(#fray)">' +
      '\n    <g mask="url(#ink)">' +
      '\n      <use href="#blob" fill="' + BASE + '"/>' +
      '\n      <rect x="0" y="0" width="' + w + '" height="' + h + '" fill="url(#shadeA)"/>' +
      '\n      <rect x="0" y="0" width="' + w + '" height="' + h + '" fill="url(#shadeB)"/>' +
      '\n      <rect x="0" y="0" width="' + w + '" height="' + h + '" fill="url(#hi)"/>' +
      '\n      <use href="#blob" fill="none" stroke="' + EDGE + '" stroke-opacity="0.5" stroke-width="1.4"/>' +
      '\n      <rect x="0" y="0" width="' + w + '" height="' + h + '" filter="url(#grain)" opacity="' + grainOp + '" style="mix-blend-mode:multiply"/>' +
      "\n    </g>" +
      "\n  </g>" +
      "\n</svg>";
  }

  /* data URI 用 base64，不能用百分号转义 + 单引号：SVG 属性改单引号后，
     CSS 的 url('...') 会被第一个单引号截断，背景图整个失效（实测踩到）。
     base64 只有 A-Za-z0-9+/=，任何引号包裹方式都安全；代价是体积 +33%。 */
  function toDataURI(svg) {
    var b64;
    if (typeof Buffer !== "undefined") {
      b64 = Buffer.from(svg, "utf8").toString("base64");
    } else {
      b64 = btoa(unescape(encodeURIComponent(svg)));
    }
    return "data:image/svg+xml;base64," + b64;
  }

  /**
   * 把墨块挂到元素上（带缓存）。
   * @returns {boolean} 是否挂上；false 时不会写任何变量，元素继续吃 CSS 的纯色兜底
   *
   * 缓存键含尺寸和种子：同一专员重排一次（字号没变）直接返回，不重复生成
   * （生成 0.3~0.4ms + 14KB 字符串，布局循环里重算会白烧）。
   */
  function applyTo(el, w, h, seed) {
    if (!el || !(w > 4) || !(h > 4)) return false;
    var sd = (typeof seed === "string" ? hashSeed(seed) : (seed >>> 0));
    var key = Math.round(w) + "x" + Math.round(h) + "#" + sd;
    if (el.dataset.sealKey === key) return true;
    try {
      el.style.setProperty("--rt-ink", 'url("' + toDataURI(
        inkSVG(w, h, sd, { shadow: true })
      ) + '")');
      el.dataset.sealKey = key;
      return true;
    } catch (e) {
      /* 抛异常就干脆不写变量：CSS 的 var() 落到纯色墨块，绝不把牌子弄成裸字。
         千万不能退而写一个坏 url —— 变量一旦有值，CSS fallback 就不接管了。 */
      return false;
    }
  }

  root.SealGen = {
    inkSVG: inkSVG,
    toDataURI: toDataURI,
    applyTo: applyTo,
    hashSeed: hashSeed,
  };
})(typeof window !== "undefined" ? window : this);
