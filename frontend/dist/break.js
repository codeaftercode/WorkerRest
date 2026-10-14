"use strict";
(() => {
  // renderer/figureTrim.ts
  var ALPHA_MIN = 8;
  var MAX_SAMPLE = 2048;
  var IGNORE = 8e-3;
  var MIN_CONTENT = 0.25;
  var MAX_SCALE = 4;
  var NONE = { left: 0, right: 0, top: 0, bottom: 0, content: 1, scale: 1, transform: "none" };
  function norm(v) {
    return v < IGNORE ? 0 : v;
  }
  function measureFigureTrim(img) {
    const w = img.naturalWidth;
    const h = img.naturalHeight;
    if (!w || !h) return NONE;
    const s = Math.min(1, MAX_SAMPLE / Math.max(w, h));
    const cw = Math.max(1, Math.round(w * s));
    const ch = Math.max(1, Math.round(h * s));
    const cv = document.createElement("canvas");
    cv.width = cw;
    cv.height = ch;
    const ctx = cv.getContext("2d", { willReadFrequently: true });
    if (!ctx) return NONE;
    ctx.clearRect(0, 0, cw, ch);
    ctx.drawImage(img, 0, 0, cw, ch);
    let px;
    try {
      px = ctx.getImageData(0, 0, cw, ch).data;
    } catch {
      return NONE;
    }
    let top = -1;
    let bottom = -1;
    let left = cw;
    let right = -1;
    for (let y = 0; y < ch; y++) {
      const row = y * cw * 4;
      let has = false;
      for (let x = 0; x < cw; x++) {
        if (px[row + x * 4 + 3] >= ALPHA_MIN) {
          has = true;
          if (x < left) left = x;
          if (x > right) right = x;
        }
      }
      if (has) {
        if (top < 0) top = y;
        bottom = y;
      }
    }
    if (bottom < 0 || right < 0) return NONE;
    const out = {
      left: norm(left / cw),
      right: norm((cw - 1 - right) / cw),
      top: norm(top / ch),
      bottom: norm((ch - 1 - bottom) / ch),
      // 未过 IGNORE 阈值的原始边距（摆位锚点专用）：IGNORE 会把 <0.8% 的边距归零，
      // 对变换无害（那点边距本就看不见），但锚点必须指向像素的真实位置——
      // 否则立绘上缘差 0.8%*立绘高（≈5px），换不同角色（如 role_ai 上缘 0.78%）
      // 时倒计时锚点会系统性偏高。
      rawLeft: left / cw,
      rawRight: (cw - 1 - right) / cw,
      rawTop: top / ch,
      rawBottom: (ch - 1 - bottom) / ch,
      content: 1,
      scale: 1,
      transform: "none"
    };
    out.content = 1 - out.top - out.bottom;
    if (out.content < MIN_CONTENT) return NONE;
    return out;
  }
  var measured = /* @__PURE__ */ new WeakMap();
  function applyFigureTrim(host, img, flip = false) {
    host.style.transformOrigin = "left bottom";
    const run = () => {
      host.style.transform = "none";
      const t = measureFigureTrim(img);
      const left = flip ? t.right : t.left;
      const right = flip ? t.left : t.right;
      const rendered = host.getBoundingClientRect();
      const scale = t.content >= MIN_CONTENT ? Math.min(MAX_SCALE, 1 / t.content) : 1;
      const ar = img.naturalHeight > 0 ? img.naturalWidth / img.naturalHeight : 0;
      // ⚠️ fill 必须按**布局盒**算，不能读 img 的 getBoundingClientRect：img 自身
      // 可能带着 Stage 变换（#app[data-stage='1'] 的 scale(1.03) scaleY(1.006)，
      // 纵向 1.03618 > 横向 1.03）。立绘图常在变换生效之后才加载完，那时量到的是
      // 变换后尺寸——W/H 比自然 aspect 小 0.6%，fill 被压成 0.994（应为 1.0），
      // trim 的平移量整体偏小、立绘可见边与摆位锚点一起算歪（实测顶缘差 ~4px）。
      // host 的变换已在函数开头重置为 none，其 rect 即布局盒（img 盒撑满 host：
      // height:100%，宽度随 shrink-to-fit 链相等），且不受 img 自身变换影响。
      const fill = rendered.height > 0 && ar > 0 ? Math.min(1, rendered.width / ar / rendered.height) : 1;
      const needTrim = left > 0 || t.top > 0 || t.bottom > 0;
      const transform = needTrim ? `translate(${(-left * scale * 100).toFixed(3)}%, ${(t.bottom * scale * fill * 100).toFixed(3)}%) scale(${scale.toFixed(4)})` : "none";
      host.style.transform = transform;
      host.style.visibility = "visible";
      img.dataset.trim = JSON.stringify({
        left: +left.toFixed(4),
        right: +right.toFixed(4),
        top: +t.top.toFixed(4),
        bottom: +t.bottom.toFixed(4),
        // 像素真实边距（未过 IGNORE）：摆位锚点用，语义与 left/right 一致（含 flip 交换）
        rawLeft: +(flip ? t.rawRight : t.rawLeft).toFixed(4),
        rawRight: +(flip ? t.rawLeft : t.rawRight).toFixed(4),
        rawTop: +t.rawTop.toFixed(4),
        rawBottom: +t.rawBottom.toFixed(4),
        scale: +scale.toFixed(4),
        fill: +fill.toFixed(4),
        flip,
        transform
      });
    };
    const cacheKey = `${img.src}|${flip ? "F" : "-"}`;
    if (measured.get(img) === cacheKey && img.dataset.trim) {
      const cached = JSON.parse(img.dataset.trim);
      host.style.transform = cached.transform;
      host.style.visibility = "visible";
      return;
    }
    measured.set(img, cacheKey);
    if (img.complete && img.naturalWidth > 0) {
      run();
      return;
    }
    host.style.visibility = "hidden";
    img.addEventListener("load", run, { once: true });
    img.addEventListener("error", () => {
      img.dataset.trim = JSON.stringify({ transform: "none", flip });
      host.style.visibility = "visible";
    }, { once: true });
  }

  // renderer/supervisorStage.ts
  var GEOM_FALLBACK = { sizePct: 72, posXPct: 4, posYPct: 0, flip: false };
  var ESCALATE_WEIGHT = 3;
  var RELAX_MS = 6e3;
  function setupSupervisor(sup, stage1Text) {
    const app = document.getElementById("app");
    const figure = document.getElementById("figure");
    const gaze = document.getElementById("gaze");
    const img = document.getElementById("figure-img");
    const line = document.getElementById("supervisor-line");
    document.body.classList.add("has-supervisor");
    figure.classList.remove("hidden");
    line.classList.remove("hidden");
    if (sup.name) line.dataset.speaker = sup.name;
    else delete line.dataset.speaker;
    // 游戏对话画面：名字挂在对话框左上角的凸出标签上（.dialog-name）
    const nameEl = document.getElementById("dialog-name");
    if (nameEl) {
      if (sup.name) {
        nameEl.textContent = sup.name;
        nameEl.classList.remove("hidden");
      } else {
        nameEl.textContent = "";
        nameEl.classList.add("hidden");
      }
    }
    const g = sup.geom ?? GEOM_FALLBACK;
    const flip = g.flip === true;
    document.body.classList.toggle("sup-flip", flip);
    const vars = document.documentElement.style;
    vars.setProperty("--fig-size", String(g.sizePct));
    vars.setProperty("--fig-x", String(g.posXPct));
    vars.setProperty("--fig-y", String(g.posYPct));
    img.src = sup.image;
    applyFigureTrim(document.getElementById("figure-trim"), img, flip);
    const supApi = window.api;
    const stage1 = stage1Text || sup.lines.urge;
    let stage = 1;
    let weight = 0;
    let relaxTimer;
    const applyStage = (next) => {
      if (next === stage) return;
      stage = next;
      app.dataset.stage = String(next);
      line.textContent = next === 2 ? sup.lines.command || stage1 : stage1;
    };
    app.dataset.stage = "1";
    line.textContent = stage1;
    const relax = () => {
      window.clearTimeout(relaxTimer);
      relaxTimer = window.setTimeout(() => {
        weight = 0;
        applyStage(1);
      }, RELAX_MS);
    };
    const noteActivity = (w, broadcast) => {
      weight = Math.min(weight + w, 12);
      if (weight >= ESCALATE_WEIGHT) applyStage(2);
      relax();
      if (broadcast) supApi?.supActivity?.();
    };
    let lastMove = 0;
    window.addEventListener("mousemove", () => {
      const now = performance.now();
      if (now - lastMove < 500) return;
      lastMove = now;
      noteActivity(1, true);
    });
    window.addEventListener("mousedown", () => noteActivity(3, true), { passive: true });
    window.addEventListener("keydown", () => noteActivity(3, true), { passive: true });
    window.addEventListener("wheel", () => noteActivity(3, true), { passive: true });
    window.addEventListener("touchstart", () => noteActivity(3, true), { passive: true });
    supApi?.onSupActivity?.(() => noteActivity(3, false));
    if (!sup.gaze) return;
    gaze.style.transformOrigin = "50% 100%";
    let targetNX = 0;
    let targetNY = 0;
    let curNX = 0;
    let curNY = 0;
    window.addEventListener("mousemove", (e) => {
      targetNX = e.clientX / window.innerWidth * 2 - 1;
      targetNY = e.clientY / window.innerHeight * 2 - 1;
    });
    const frame = () => {
      curNX += (targetNX - curNX) * 0.075;
      curNY += (targetNY - curNY) * 0.075;
      const h = Math.max(1, gaze.offsetHeight);
      const tx = (curNX * 14).toFixed(2);
      const ty = (Math.max(0, curNY) * 8).toFixed(2);
      const lean = (-curNX * 0.9).toFixed(2);
      const sy = (1 + Math.max(0, -curNY) * 8 / h).toFixed(4);
      gaze.style.transform = `translate3d(${tx}px, ${ty}px, 0) skewX(${lean}deg) scaleY(${sy})`;
      requestAnimationFrame(frame);
    };
    requestAnimationFrame(frame);
  }

  // renderer/supervisorLayout.ts —— 游戏对话画面版式
  // .content 已是底部全宽对话框（CSS 定版），这里只负责把倒计时环浮到
  // 「立绘可见右缘~窗口右缘」与「立绘可见上缘~对话框上边缘」两个中点的交点上
  // （环中心对准；「提前结束」在 bar 内跟随环居中下沉）。摆位结果写入
  // supMetrics 供探针断言。
  var CD_EDGE = 10;
  var frozenFig = null;          // 立绘可见边（去透明）的冻结锚点（窗口坐标）
  var lastLayoutKey = "";        // 仅在窗口/立绘盒（含位置）/trim 就绪变化时重算，避免二阶段缩放导致倒计时位移
  function clamp(v, lo, hi) {
    return Math.min(Math.max(v, lo), hi);
  }
  // 立绘「去掉透明部分后」的可见边缘（窗口坐标）——摆位锚点的唯一来源。
  // DOM 链：figure(绝对定位盒) > .trim(透明边距补偿) > .gaze > .mirror > img，
  // img 盒撑满 .trim/.figure（height:100%，宽度由 shrink-to-fit 链决定）。
  // .trim 的补偿变换 = translate(-left*s%, bottom*s*fill%) scale(s)，
  // transform-origin: left bottom。设立绘盒左缘 x0、底缘 yB、盒宽 W、盒高 H，
  // 把 img 盒映射到屏幕后，像素内容的四条可见边：
  //   · 左缘 = x0 + s*W*(rawLeft - left)
  //   · 右缘 = x0 + s*W*(1 - rawRight - left)
  //   · 顶缘 = yB - s*fill*H*(1 - rawTop - bottom)
  //   · 底缘 = yB - s*fill*H*(rawBottom - bottom)
  // 其中 left/bottom 是变换**实际使用**的边距（过 IGNORE，可能被归零），
  // rawLeft/rawTop 等是像素**真实**边距（未归零）——二者只在边距 <0.8% 时不同。
  // s/fill/边距来自 img.dataset.trim（applyFigureTrim 量出，量取时 Stage 变换
  // 不参与——fill 按布局盒量），W/H 取立绘盒。
  // ⚠️ 曾经的 bug：① 顶缘漏减 bottom；② 左右缘直接取 .trim 变换后外框（含缩放后
  // 的透明边距）；③ fill 读的是 img 变换后的 rect（Stage 纵向缩放更大 → 被压成
  // 0.994）。三者都让锚点整体偏高/偏外，用户换立绘、改尺寸后必错位。
  // trim 尚未就绪（图片未加载/量失败）时退化为立绘自身外框。
  function figureVisibleRect(figure, fallbackTop) {
    const box = figure.getBoundingClientRect();
    const img = figure.querySelector("img");
    let t = null;
    try { t = img ? JSON.parse(img.dataset.trim || "null") : null; } catch (e) { t = null; }
    if (!t || typeof t.scale !== "number" || box.width <= 0 || box.height <= 0) {
      return { left: box.left, right: box.right, top: fallbackTop, bottom: box.bottom };
    }
    const s = t.scale;
    const fill = typeof t.fill === "number" ? t.fill : 1;
    const l = typeof t.left === "number" ? t.left : 0;
    const bt = typeof t.bottom === "number" ? t.bottom : 0;
    const rl = typeof t.rawLeft === "number" ? t.rawLeft : l;
    const rr = typeof t.rawRight === "number" ? t.rawRight : (typeof t.right === "number" ? t.right : 0);
    const rtp = typeof t.rawTop === "number" ? t.rawTop : (typeof t.top === "number" ? t.top : 0);
    const rbt = typeof t.rawBottom === "number" ? t.rawBottom : bt;
    return {
      left: box.left + s * box.width * (rl - l),
      right: box.left + s * box.width * (1 - rr - l),
      top: box.bottom - s * fill * box.height * (1 - rtp - bt),
      bottom: box.bottom - s * fill * box.height * (rbt - bt)
    };
  }
  function layoutSupDialog(figure, content, measure) {
    const container = figure?.parentElement ?? null;
    if (!container || !figure || !content) return;
    const cRect = container.getBoundingClientRect();
    if (cRect.width <= 0) return;
    const figBox = figure.getBoundingClientRect();
    const figImg = figure.querySelector("img");
    const haveTrim = !!(figImg && figImg.dataset.trim);
    // 锚点重算条件：窗口尺寸 / 立绘盒（含 left/top 位置）/ trim 是否就绪。
    // 位置进 key 是必须的：用户可拖动「立绘尺寸/水平/垂直」滑条、更换立绘、
    // 上传新图，都表现为 figure 盒变化，锚点必须跟着重算（不能写死）。
    // 二阶段只是对 img 做 scale 变换——后代变换不改 figure 布局盒，也不改
    // cRect 与 trim 存在性 → 不触发重算 → 倒计时位置在二阶段保持不动。
    const key = Math.round(cRect.width) + "x" + Math.round(cRect.height) + "|" +
      Math.round(figBox.left) + "," + Math.round(figBox.top) + " " +
      Math.round(figBox.width) + "x" + Math.round(figBox.height) + "|" + (haveTrim ? "T" : "t");
    let vis;
    if (key !== lastLayoutKey && figBox.width > 0) {
      vis = figureVisibleRect(figure, figBox.top);
      frozenFig = vis;
      lastLayoutKey = key;
    } else {
      // key 未变（含二阶段缩放）时沿用冻结锚点；trim 未就绪的极早期兜底：
      // 用 figure 自身外框（会随 trim 到达、key 变化而校正）。
      vis = frozenFig || figureVisibleRect(figure, figBox.top);
    }
    const figLeft = clamp(vis.left - cRect.left, 0, cRect.width);
    const figRight = clamp(vis.right - cRect.left, 0, cRect.width);
    const figTop = clamp(vis.top - cRect.top, 0, cRect.height);
    // 立绘站位已由 CSS 的 --fig-* 变量定死，这里不再碰 figure/content 的定位。
    // 对话框（.content）与窗口边缘的偏移由 CSS 决定（当前贴边为 0），bar 的
    // 包含块就是它——所以摆位全部用实测 rect 换算，不假设贴边。两个锚点中点：
    //   水平 midX = (立绘可见右缘 + 窗口右缘) / 2
    //   垂直 midY = (立绘可见上缘 + 对话框上边缘) / 2（二者实测，偏移自动跟随）
    // bar 是 column flex（名称在上、环居中、skip 在下），环中心距 bar 底用
    // ring-wrap 的 offsetTop 实测反推，让环中心（而非 bar 中心）落在 midY 上。
    const bar = content.querySelector(".bar");
    const boxRect = content.getBoundingClientRect();
    const boxH = boxRect.height;
    const boxLeft = boxRect.left - cRect.left; // 对话框左缘相对窗口（= 水平内缩量）
    const boxBottomGap = cRect.bottom - boxRect.bottom; // 对话框底缘距窗底（= 底部内缩量）
    const boxTop = boxRect.top - cRect.top; // 对话框顶缘相对窗口
    const midX = (figRight + cRect.width) / 2;
    const midY = (figTop + boxTop) / 2;
    let cdX = midX - boxLeft;
    let cdBottom = cRect.height - midY - boxBottomGap;
    if (bar) {
      const ringWrap = bar.querySelector(".ring-wrap");
      const barW = bar.offsetWidth || 84;
      const ringH = (ringWrap && ringWrap.offsetHeight) || 84;
      const barH = bar.offsetHeight || ringH;
      // bar 是 column flex：提醒名称在环上、「提前结束」在环下。环在中间，
      // 用 ring-wrap 的实际布局位置（距 bar 顶的 offsetTop）反推「环中心距 bar 底」，
      // 这样无论环上下各放了什么，环中心都能精确落在 midY 上（名称移到环上方后，
      // 旧式「barH - ringH 当作环下占位」会把环整体抬高，故改为实测 offsetTop）。
      const ringFromTop = (ringWrap ? ringWrap.offsetTop : 0) + ringH / 2;
      const ringFromBottom = Math.max(0, barH - ringFromTop);
      // clamp 的合法区间是「bar 相对对话框」的坐标：窗口坐标的边界先算好再减去内缩量
      cdX = clamp(
        midX - boxLeft - barW / 2,
        CD_EDGE - boxLeft,
        Math.max(CD_EDGE - boxLeft, cRect.width - boxLeft - barW - CD_EDGE)
      );
      cdBottom = clamp(
        cRect.height - midY - ringFromBottom - boxBottomGap,
        CD_EDGE - boxBottomGap,
        Math.max(CD_EDGE - boxBottomGap, cRect.height - boxBottomGap - barH - CD_EDGE)
      );
      bar.style.left = `${Math.round(cdX)}px`;
      bar.style.bottom = `${Math.round(cdBottom)}px`;
      bar.style.right = "auto";
    }
    content.dataset.supMetrics = JSON.stringify({
      mode: "dialog",
      figLeft: Math.round(figLeft),
      figRight: Math.round(figRight),
      figTop: Math.round(figTop),
      cdX: Math.round(cdX),
      cdBottom: Math.round(cdBottom),
      midX: Math.round(midX),
      midY: Math.round(midY),
      src: haveTrim ? "trim" : "figure"
    });
  }
  function bindSupContentLayout(figure, content, measure) {
    const relayout = () => layoutSupDialog(figure, content, measure);
    if (!figure || !content) return relayout;
    relayout();
    const container = figure.parentElement;
    if (typeof ResizeObserver !== "undefined") {
      const ro = new ResizeObserver(relayout);
      ro.observe(figure);
      if (container) ro.observe(container);
      if (measure) ro.observe(measure);
      ro.observe(content);
    }
    if (measure && typeof MutationObserver !== "undefined") {
      new MutationObserver(relayout).observe(measure, {
        attributes: true,
        attributeFilter: ["style"]
      });
    }
    window.addEventListener("resize", relayout);
    return relayout;
  }

  // renderer/break.ts
  var api = window.api;
  // 把「设置 → 快捷键」录入的组合键字符串（如 "Ctrl+Alt+X" / "Super+Shift+F1" /
  // "Ctrl+ "）解析成匹配器，让休息窗口的本地 keydown 与全局快捷键完全一致。
  // 解析失败（格式非法 / 缺修饰键）返回 null —— 对应「未配置」，本地不再兜底触发。
  function parseAccelerator(acc) {
    if (!acc || typeof acc !== "string") return null;
    const parts = acc.split("+");
    if (parts.length < 2) return null;
    let ctrl = false, alt = false, shift = false, meta = false;
    for (let i = 0; i < parts.length - 1; i++) {
      const m = parts[i].trim().toLowerCase();
      if (m === "ctrl") ctrl = true;
      else if (m === "alt") alt = true;
      else if (m === "shift") shift = true;
      else if (m === "super" || m === "meta" || m === "win") meta = true;
      else return null; // 不认识的修饰键 → 无法匹配
    }
    return { ctrl, alt, shift, meta, key: parts[parts.length - 1] };
  }
  function matchAccelerator(e, acc) {
    const p = parseAccelerator(acc);
    if (!p) return false;
    if (e.ctrlKey !== p.ctrl) return false;
    if (e.altKey !== p.alt) return false;
    if (e.shiftKey !== p.shift) return false;
    if (e.metaKey !== p.meta) return false;
    const ek = e.key.length === 1 ? e.key.toUpperCase() : e.key;
    return ek === p.key;
  }
  function renderMessage(text) {
    const p = document.createElement("p");
    p.textContent = text;
    return p;
  }
  // 粒子层：倒计时周围轻柔上浮的光点，渲染「放松」氛围。仅无督休专员模式启用
  // （督休专员模式由 CSS 隐藏画布）。纯装饰、不参与信息呈现，低透明度、低速。
  function startParticles(canvas, color) {
    try {
      const ctx = canvas.getContext("2d");
      if (!ctx) return;
      const dpr = window.devicePixelRatio || 1;
      const SIZE = 340;
      canvas.width = SIZE * dpr;
      canvas.height = SIZE * dpr;
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
      const N = 30;
      const rnd = (a, b) => a + Math.random() * (b - a);
      const ps = [];
      for (let i = 0; i < N; i++) {
        ps.push({
          x: rnd(0, SIZE), y: rnd(0, SIZE),
          r: i < 4 ? rnd(2.4, 3.8) : rnd(0.8, 2.8),
          vy: rnd(0.15, 0.6),
          a: rnd(0.15, 0.55), ph: rnd(0, Math.PI * 2)
        });
      }
      let t = 0;
      ctx.shadowColor = color;
      function tick() {
        t += 0.016;
        ctx.clearRect(0, 0, SIZE, SIZE);
        ctx.shadowBlur = 6;
        for (const p of ps) {
          p.y -= p.vy;
          p.x += Math.sin(t * 0.6 + p.ph) * 0.25;
          if (p.y < -4) { p.y = SIZE + 4; p.x = rnd(0, SIZE); }
          ctx.beginPath();
          ctx.arc(p.x, p.y, p.r, 0, Math.PI * 2);
          ctx.fillStyle = color;
          ctx.globalAlpha = p.a * (0.6 + 0.4 * Math.sin(t + p.ph));
          ctx.fill();
        }
        ctx.shadowBlur = 0;
        ctx.globalAlpha = 1;
        requestAnimationFrame(tick);
      }
      tick();
    } catch (e) { /* 装饰层异常不影响倒计时主体 */ }
  }
  // 蓄能容器：把倒计时当作一只「储存能量的空心容器」——
  // 起始为空，随着休息推进（剩余时间流逝 = 蓄能累积），内部液态能量自下而上
  // 上涨、表面波动，直至蓄满整个容器；容器壁外缘有能量溢出（光丝迸发 + 扩散脉冲）。
  var SVGNS = "http://www.w3.org/2000/svg";
  var WALL_R = 82;                        // 容器外壁半径（SVG 用户坐标）
  var LIQ_R = 78;                         // 液态能量裁剪半径（容器内部）
  var LIQ_BOTTOM = 100 + LIQ_R + 12;      // 液面以下填充延伸，保证裁满下半圆
  var targetFill = 0, currentFill = 0, energyRunning = false, lastEnergyT = 0;
  var liquidEl = null, liquidSurfEl = null, glossEl = null;
  var bubbles = [], wisps = [], pulses = [];
  var intakeStreaks = [], intakeRipples = [];

  function surfaceYOf(fill) { return 100 + LIQ_R - fill * 2 * LIQ_R; }

  // 液面：两条不同波长/相位的正弦叠加 → 液态波动
  function liquidWave(fill, x, t) {
    var sy = surfaceYOf(fill);
    var a1 = 3.0, a2 = 1.5;
    var k1 = (Math.PI * 2) / 72, k2 = (Math.PI * 2) / 41;
    return sy + a1 * Math.sin(k1 * x + t * 1.7) + a2 * Math.sin(k2 * x - t * 2.4);
  }
  function liquidPath(fill, t) {
    var x0 = 100 - LIQ_R - 2, x1 = 100 + LIQ_R + 2, d = "", step = 3, i = 0;
    for (var x = x0; x <= x1 + 0.001; x += step, i++) {
      d += (i === 0 ? "M" : "L") + x.toFixed(2) + "," + liquidWave(fill, x, t).toFixed(2) + " ";
    }
    d += "L" + (100 + LIQ_R + 2) + "," + LIQ_BOTTOM + " L" + (100 - LIQ_R - 2) + "," + LIQ_BOTTOM + " Z";
    return d;
  }
  function liquidSurfacePath(fill, t) {
    var x0 = 100 - LIQ_R - 2, x1 = 100 + LIQ_R + 2, d = "", step = 3, i = 0;
    for (var x = x0; x <= x1 + 0.001; x += step, i++) {
      d += (i === 0 ? "M" : "L") + x.toFixed(2) + "," + liquidWave(fill, x, t).toFixed(2) + " ";
    }
    return d;
  }
  function buildBubbles(group) {
    if (!group) return;
    for (var i = 0; i < 7; i++) {
      var c = document.createElementNS(SVGNS, "circle");
      var b = {
        el: c,
        x: 100 + (Math.random() * 2 - 1) * (LIQ_R - 12),
        y: 100 + Math.random() * LIQ_R,
        r: 1.4 + Math.random() * 2.4,
        vy: 10 + Math.random() * 16,
        ph: Math.random() * 6.28
      };
      c.setAttribute("cx", b.x.toFixed(1));
      c.setAttribute("cy", b.y.toFixed(1));
      c.setAttribute("r", b.r.toFixed(1));
      group.appendChild(c);
      bubbles.push(b);
    }
  }
  function updateBubbles(dt, surfaceY, t) {
    for (var i = 0; i < bubbles.length; i++) {
      var b = bubbles[i];
      b.y -= b.vy * dt;
      if (b.y <= surfaceY + 3) { b.y = LIQ_BOTTOM - 4; b.x = 100 + (Math.random() * 2 - 1) * (LIQ_R - 12); }
      var op = 0.55 * (1 - (b.y - surfaceY) / (LIQ_BOTTOM - surfaceY));
      b.el.setAttribute("cx", (b.x + Math.sin(t * 2 + b.ph) * 1.2).toFixed(1));
      b.el.setAttribute("cy", b.y.toFixed(1));
      b.el.setAttribute("opacity", Math.max(0.05, op).toFixed(2));
    }
  }
  function resetWisp(w, init) {
    w.angle = Math.random() * Math.PI * 2;
    w.rad = WALL_R - 3 + Math.random() * 4;
    w.speed = 12 + Math.random() * 22;
    w.maxLife = 1.1 + Math.random() * 1.4;
    w.life = init ? Math.random() * w.maxLife : 0;
    w.size = 1.6 + Math.random() * 2.6;
  }
  function buildOverflow(group) {
    if (!group) return;
    for (var i = 0; i < 16; i++) {
      var c = document.createElementNS(SVGNS, "circle");
      c.setAttribute("class", "overflow-wisp");
      c.setAttribute("r", "2");
      group.appendChild(c);
      var w = { el: c, angle: 0, rad: 0, speed: 0, life: 0, maxLife: 1, size: 2 };
      resetWisp(w, true);
      wisps.push(w);
    }
    for (var j = 0; j < 3; j++) {
      var p = document.createElementNS(SVGNS, "circle");
      p.setAttribute("class", "pulse-ring");
      group.appendChild(p);
      pulses.push({ el: p, life: j * 0.9, maxLife: 2.6, max: 46 });
    }
  }
  function updateOverflow(dt, fill) {
    var intensity = 0.18 + 0.82 * fill; // 蓄得越满，溢出越强
    for (var i = 0; i < wisps.length; i++) {
      var w = wisps[i];
      w.life += dt;
      if (w.life >= w.maxLife) resetWisp(w, false);
      w.rad += w.speed * dt;
      var k = w.life / w.maxLife;
      var op = Math.sin(Math.PI * k) * intensity;
      w.el.setAttribute("cx", (100 + w.rad * Math.cos(w.angle)).toFixed(1));
      w.el.setAttribute("cy", (100 + w.rad * Math.sin(w.angle)).toFixed(1));
      w.el.setAttribute("r", (w.size * (0.6 + 0.4 * Math.sin(Math.PI * k))).toFixed(2));
      w.el.setAttribute("opacity", Math.max(0, op).toFixed(3));
    }
    for (var j = 0; j < pulses.length; j++) {
      var p = pulses[j];
      p.life += dt;
      if (p.life >= p.maxLife) p.life = 0;
      var kk = p.life / p.maxLife;
      p.el.setAttribute("cx", "100");
      p.el.setAttribute("cy", "100");
      p.el.setAttribute("r", (WALL_R + p.max * kk).toFixed(1));
      p.el.setAttribute("stroke-width", (3 * (1 - kk)).toFixed(2));
      p.el.setAttribute("opacity", (0.5 * (1 - kk) * intensity).toFixed(3));
    }
  }
  // 液体颜色随背景（--accent）自动适配：同色相保证「协调」，提亮 + 高饱和保证「明显」。
  // 把推导出的提亮同源色写进 --liquid-accent（汲取光丝 / 吸收涟漪 / 液面高光共用），
  // 并直接改写液态渐变与气泡渐变的色标。
  function hexToRgb(hex) {
    if (!hex) return null;
    var h = String(hex).replace("#", "").trim();
    if (h.length === 3) h = h.split("").map(function (c) { return c + c; }).join("");
    if (h.length !== 6) return null;
    var n = parseInt(h, 16);
    if (isNaN(n)) return null;
    return { r: (n >> 16) & 255, g: (n >> 8) & 255, b: n & 255 };
  }
  function rgbToHsl(r, g, b) {
    r /= 255; g /= 255; b /= 255;
    var mx = Math.max(r, g, b), mn = Math.min(r, g, b), h = 0, s = 0, l = (mx + mn) / 2;
    if (mx !== mn) {
      var d = mx - mn;
      s = l > 0.5 ? d / (2 - mx - mn) : d / (mx + mn);
      if (mx === r) h = (g - b) / d + (g < b ? 6 : 0);
      else if (mx === g) h = (b - r) / d + 2;
      else h = (r - g) / d + 4;
      h /= 6;
    }
    return { h: h * 360, s: s, l: l };
  }
  function hsl(h, s, l) { return "hsl(" + h.toFixed(1) + ", " + (s * 100).toFixed(0) + "%, " + (l * 100).toFixed(0) + "%)"; }
  function applyAdaptiveLiquid(hex) {
    var rgb = hexToRgb(hex);
    if (!rgb) return;
    var c = rgbToHsl(rgb.r, rgb.g, rgb.b);
    var S = Math.min(1, Math.max(0.5, c.s + 0.12)); // 保证足够鲜艳
    var lg = document.getElementById("liquidGrad");
    if (lg) {
      var ss = lg.querySelectorAll("stop");
      var stops = [
        [0, hsl(c.h, Math.min(1, S + 0.08), 0.88)],
        [34, hsl(c.h, S, 0.66)],
        [72, hsl(c.h, S, 0.5)],
        [100, hsl(c.h, S, 0.36)]
      ];
      stops.forEach(function (st, i) {
        if (ss[i]) {
          ss[i].setAttribute("offset", st[0] + "%");
          ss[i].setAttribute("stop-color", st[1]);
        }
      });
    }
    // 提亮同源色：汲取光丝 / 涟漪 / 液面高光都走它，既协调又从暗背景里跳出来
    document.documentElement.style.setProperty("--liquid-accent", hsl(c.h, Math.min(1, S + 0.05), 0.8));
    var surf = document.getElementById("liquid-surface");
    if (surf) surf.style.stroke = hsl(c.h, Math.max(0, S - 0.2), 0.96);
    var bg = document.getElementById("bubbleGrad");
    if (bg) {
      var bs = bg.querySelectorAll("stop");
      if (bs[0]) bs[0].setAttribute("stop-color", hsl(c.h, 0.25, 0.98));
      if (bs[1]) bs[1].setAttribute("stop-color", hsl(c.h, S, 0.62));
    }
  }

  // 外界能量汲取：从四面八方径向射向容器壁，撞壁后迸出吸收涟漪并被「吸进」容器。
  // 强度随蓄能反向变化：越空吸得越猛，将满时收束。
  function resetIntake(w, init) {
    w.angle = Math.random() * Math.PI * 2;
    w.rad = WALL_R + 26 + Math.random() * 44;  // 从容器外缘之外射入
    w.speed = 30 + Math.random() * 36;          // 向内速度（px/s，SVG 用户单位）
    w.len = 7 + Math.random() * 8;              // 光丝拖尾长度（细锐，无箭头）
    w.life = init ? Math.random() : 0;          // 错峰：初始化时随机散布在途中
    if (init) w.rad = WALL_R + (1 - w.life) * 44; // 让初始散布在整段行程上
  }
  function buildIntake(group) {
    if (!group) return;
    // 密集的细锐光丝（无末端亮头/箭头），从四面八方射向容器壁、被吸收 —— 30 条
    for (var i = 0; i < 30; i++) {
      var ln = document.createElementNS(SVGNS, "line");
      ln.setAttribute("class", "intake-streak");
      group.appendChild(ln);
      var w = { ln: ln, angle: 0, rad: 0, speed: 0, len: 0, life: 0 };
      resetIntake(w, true);
      intakeStreaks.push(w);
    }
    for (var j = 0; j < 6; j++) {
      var rp = document.createElementNS(SVGNS, "circle");
      rp.setAttribute("class", "intake-ripple");
      rp.setAttribute("opacity", "0");
      group.appendChild(rp);
      intakeRipples.push({ el: rp, life: 0, max: 0.6 });
    }
  }
  function spawnRipple(angle) {
    for (var i = 0; i < intakeRipples.length; i++) {
      if (intakeRipples[i].life <= 0) { intakeRipples[i].life = intakeRipples[i].max; return; }
    }
  }
  function updateIntake(dt, fill) {
    var intensity = 0.22 + 0.78 * (1 - Math.min(1, Math.max(0, fill)));
    for (var i = 0; i < intakeStreaks.length; i++) {
      var w = intakeStreaks[i];
      w.rad -= w.speed * dt;
      if (w.rad <= WALL_R - 2) { spawnRipple(w.angle); resetIntake(w, false); continue; }
      var hx = 100 + w.rad * Math.cos(w.angle), hy = 100 + w.rad * Math.sin(w.angle);
      var tr = w.rad + w.len, tx = 100 + tr * Math.cos(w.angle), ty = 100 + tr * Math.sin(w.angle);
      w.ln.setAttribute("x1", tx.toFixed(1)); w.ln.setAttribute("y1", ty.toFixed(1));
      w.ln.setAttribute("x2", hx.toFixed(1)); w.ln.setAttribute("y2", hy.toFixed(1));
      // 临近壁时渐隐（被吸收），远离壁时全亮
      var op = intensity * Math.max(0, Math.min(1, (w.rad - WALL_R) / 16));
      w.ln.setAttribute("opacity", op.toFixed(3));
    }
    for (var k = 0; k < intakeRipples.length; k++) {
      var r = intakeRipples[k];
      if (r.life > 0) {
        r.life -= dt;
        var p = 1 - r.life / r.max;
        r.el.setAttribute("r", (WALL_R + 2 + p * 12).toFixed(1));
        r.el.setAttribute("stroke-width", (2.6 * (1 - p)).toFixed(2));
        r.el.setAttribute("opacity", (0.6 * (1 - p) * Math.max(0.3, intensity)).toFixed(3));
      } else {
        r.el.setAttribute("opacity", "0");
      }
    }
  }

  function buildEnergy(svg) {
    if (!svg) return;
    buildBubbles(svg.querySelector("#bubbles"));
    buildOverflow(svg.querySelector("#energy-overflow"));
    buildIntake(svg.querySelector("#energy-intake"));
    liquidEl = svg.querySelector("#liquid");
    liquidSurfEl = svg.querySelector("#liquid-surface");
    glossEl = svg.querySelector("#liquid-gloss");
  }
  function animateEnergy(now) {
    if (!energyRunning) return;
    var dt = Math.min(0.05, (now - lastEnergyT) / 1000);
    lastEnergyT = now;
    var t = now / 1000;
    currentFill += (targetFill - currentFill) * Math.min(1, dt * 2.4);
    if (Math.abs(targetFill - currentFill) < 0.0008) currentFill = targetFill;
    var sy = surfaceYOf(currentFill);
    if (liquidEl) liquidEl.setAttribute("d", liquidPath(currentFill, t));
    if (liquidSurfEl) liquidSurfEl.setAttribute("d", liquidSurfacePath(currentFill, t));
    if (glossEl) {
      glossEl.setAttribute("cy", (sy + 9).toFixed(1));
      glossEl.setAttribute("opacity", currentFill > 0.02 ? "0.22" : "0");
    }
    updateBubbles(dt, sy, t);
    updateOverflow(dt, currentFill);
    updateIntake(dt, currentFill);
    var wrap = document.querySelector(".ring-wrap");
    if (wrap) wrap.classList.toggle("energy-full", currentFill >= 0.995);
    requestAnimationFrame(animateEnergy);
  }

  async function main() {
    // 一次性揭示窗口：消除「先无立绘、后有立绘」闪烁——启用督休专员时整窗先透明，
    // 立绘就绪（load/error）后淡入；无立绘时立即显示。超时兜底防异常卡透明。
    let revealed = false;
    const reveal = () => {
      if (revealed) return;
      revealed = true;
      document.documentElement.style.opacity = "1";
    };
    const info = await api.breakCurrent();
    if (!info) { reveal(); return; }
    const data = info;
    document.documentElement.style.setProperty("--accent", data.appearance.color);
    // 液体颜色随背景（accent）自适配：同色相协调、提亮高饱和明显
    applyAdaptiveLiquid(data.appearance.color);
    document.documentElement.style.setProperty("--app-opacity", String(1 - data.appearance.transparency));
    document.getElementById("app").dataset.rounded = data.appearance.fullscreen ? "0" : "1";
    const msgBox = document.getElementById("messages");
    for (const m of data.messages) msgBox.appendChild(renderMessage(m));
    // 提醒名称（短休/长休/外卖/自定义）显示在倒计时环外上方——督休专员模式
    // 同样显示（与无督休专员模式一致；它与对话框左上角的督休专员名字标签是两回事）
    const nameEl = document.getElementById("remind-name");
    if (nameEl) {
      nameEl.textContent = data.name || "";
      if (data.name) nameEl.classList.remove("hidden");
    }
    // 环内两行直排铭文「整理思绪 / 积蓄能量」已静态写在 break.html 的
    // #inscribe-lines 中（进度百分数下方，非弧形），无需在此设置。
    if (data.supervisor) {
      msgBox.classList.add("hidden");
      setupSupervisor(data.supervisor, data.messages[0] ?? "");
      bindSupContentLayout(
        document.getElementById("figure"),
        document.querySelector(".content"),
        document.getElementById("figure-trim")
      );
      // 立绘就绪后再揭示整窗（load/error 或已缓存 complete），避免空白对话框先闪现
      const figImg = document.getElementById("figure-img");
      if (figImg && figImg.complete && figImg.naturalWidth > 0) reveal();
      else if (figImg) {
        figImg.addEventListener("load", reveal, { once: true });
        figImg.addEventListener("error", reveal, { once: true });
      }
    } else {
      reveal();
    }
    const countdown = document.getElementById("countdown");
    const pctEl = document.getElementById("progress-pct");
    const total = data.durationMs;
    // 生成「蓄能容器」几何：液态能量、气泡、外壁溢出光丝与脉冲
    buildEnergy(document.querySelector(".ring-wrap svg"));
    // 启动液态能量动画（独立于倒计时 tick，保证液面波动丝滑）
    energyRunning = true;
    lastEnergyT = performance.now();
    requestAnimationFrame(animateEnergy);
    let lastSec = -1;
    function frame() {
      const remain = Math.max(0, data.endsAt - Date.now());
      const sec = Math.ceil(remain / 1e3);
      // 每秒翻数：重触发弹跳高光动画，让倒计时有生命力
      if (sec !== lastSec) {
        lastSec = sec;
        countdown.textContent = String(sec);
        countdown.classList.remove("tick");
        void countdown.offsetWidth;
        countdown.classList.add("tick");
      }
      // 蓄能进度 = 流逝占比（容器从空到满）；中心「进度」与此同步增长
      targetFill = total > 0 ? 1 - remain / total : 0;
      if (pctEl && total > 0) pctEl.textContent = Math.round(targetFill * 100) + "%";
      if (remain <= 0) {
        targetFill = 1;
        if (pctEl) pctEl.textContent = "100%";
        countdown.textContent = "0";
        return;
      }
      requestAnimationFrame(frame);
    }
    frame();
    if (!data.supervisor) {
      const pcanvas = document.getElementById("particles");
      if (pcanvas) {
        try {
          const accent = (getComputedStyle(document.documentElement)
            .getPropertyValue("--accent") || "").trim() || "#4f7cff";
          startParticles(pcanvas, accent);
        } catch (e) { /* 装饰层异常不影响主体 */ }
      }
    }
    if (data.preview) {
      // 预览也复刻真实「满罐多停留 1 秒」的体验
      window.setTimeout(() => api.closeWindow(), Math.max(0, data.endsAt - Date.now()) + 1000);
    }
    const skip = document.getElementById("skip-link");
    skip.classList.remove("hidden");
    // 文案随「设置 → 快捷键」的 endEarly 走：配置了就显示「提前结束（Ctrl+Alt+X）」，
    // 没配置（禁用）就只显示「提前结束」。
    skip.textContent = data.endEarlyShortcut
      ? `提前结束（${data.endEarlyShortcut}）`
      : "提前结束";
    skip.addEventListener("click", (e) => {
      e.preventDefault();
      if (data.preview) api.closeWindow();
      else api.action("endBreak");
    });
    window.addEventListener("keydown", (e) => {
      if (matchAccelerator(e, data.endEarlyShortcut)) {
        e.preventDefault();
        if (data.preview) api.closeWindow();
        else api.action("endBreak");
      }
    });
    if (data.preview) {
      document.getElementById("preview-badge").classList.remove("hidden");
      document.body.addEventListener("click", () => api.closeWindow());
    }
    // 兜底：任何异常导致立绘既没 load 也没 error 时，最多 1.5s 后强制揭示，
    // 避免窗口一直透明不可见。
    window.setTimeout(reveal, 1500);
  }
  main();
})();
