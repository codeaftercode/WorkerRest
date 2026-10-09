"use strict";
(() => {
  // renderer/figureTrim.ts
  var ALPHA_MIN = 8;
  var MAX_SAMPLE = 512;
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
      const rendered = img.getBoundingClientRect();
      const scale = t.content >= MIN_CONTENT ? Math.min(MAX_SCALE, 1 / t.content) : 1;
      const ar = img.naturalHeight > 0 ? img.naturalWidth / img.naturalHeight : 0;
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
  // 「立绘可见右缘~窗口右缘」与「对话框上边缘~窗口上边缘」两个中点的交点上
  // （环中心对准；「提前结束」在 bar 内跟随环居中下沉）。摆位结果写入
  // supMetrics 供探针断言。
  var CD_EDGE = 10;
  function clamp(v, lo, hi) {
    return Math.min(Math.max(v, lo), hi);
  }
  function pickMeasure(figure, measure) {
    if (!measure) return figure;
    return measure.getBoundingClientRect().width > 0 ? measure : figure;
  }
  function trimFactor(figure) {
    const img = figure.querySelector("img");
    const raw = img?.dataset.trim;
    if (!raw) return 1;
    try {
      const t = JSON.parse(raw);
      const scale = typeof t.scale === "number" ? t.scale : 1;
      const fill = typeof t.fill === "number" ? t.fill : 1;
      const top = typeof t.top === "number" ? t.top : 0;
      const f = scale * fill * (1 - top);
      return f > 0 ? f : 1;
    } catch {
      return 1;
    }
  }
  function figureTop(figure, fallback) {
    const box = figure.getBoundingClientRect();
    if (box.height <= 0) return fallback;
    const h = box.height * trimFactor(figure);
    return h > 0 ? box.bottom - h : fallback;
  }
  function layoutSupDialog(figure, content, measure) {
    const container = figure?.parentElement ?? null;
    if (!container || !figure || !content) return;
    const cRect = container.getBoundingClientRect();
    if (cRect.width <= 0) return;
    const measureEl = pickMeasure(figure, measure);
    const fRect = measureEl.getBoundingClientRect();
    const figLeft = clamp(fRect.left - cRect.left, 0, cRect.width);
    const figRight = clamp(fRect.right - cRect.left, 0, cRect.width);
    const figTop = clamp(figureTop(figure, fRect.top) - cRect.top, 0, cRect.height);
    // 立绘站位已由 CSS 的 --fig-* 变量定死，这里不再碰 figure/content 的定位。
    // 对话框（.content）与窗口边缘的偏移由 CSS 决定（当前贴边为 0），bar 的
    // 包含块就是它——所以摆位全部用实测 rect 换算，不假设贴边。两个锚点中点：
    //   水平 midX = (立绘可见右缘 + 窗口右缘) / 2
    //   垂直 midY = (对话框上边缘 + 窗口上边缘) / 2（框顶实测，偏移自动跟随）
    // bar 是 column flex（环在上、skip 在下），环中心距 bar 底 = ringH/2 + skip 占位，
    // 所以用 ring-wrap 的高度反推 bottom，让环中心（而非 bar 中心）落在 midY 上。
    const bar = content.querySelector(".bar");
    const boxRect = content.getBoundingClientRect();
    const boxH = boxRect.height;
    const boxLeft = boxRect.left - cRect.left; // 对话框左缘相对窗口（= 水平内缩量）
    const boxBottomGap = cRect.bottom - boxRect.bottom; // 对话框底缘距窗底（= 底部内缩量）
    const boxTop = boxRect.top - cRect.top; // 对话框顶缘相对窗口
    const midX = (figRight + cRect.width) / 2;
    const midY = boxTop / 2;
    let cdX = midX - boxLeft;
    let cdBottom = cRect.height - midY - boxBottomGap;
    if (bar) {
      const ringWrap = bar.querySelector(".ring-wrap");
      const barW = bar.offsetWidth || 84;
      const ringH = (ringWrap && ringWrap.offsetHeight) || 84;
      const barH = bar.offsetHeight || ringH;
      // bar 是 column flex：环在上，「提前结束」挂在环下（占 barH - ringH 的高度）。
      // 要让环中心（而非 bar 中心）落在 midY 上，bottom 得先把 skip 的占位扣掉。
      const tailH = Math.max(0, barH - ringH);
      // clamp 的合法区间是「bar 相对对话框」的坐标：窗口坐标的边界先算好再减去内缩量
      cdX = clamp(
        midX - boxLeft - barW / 2,
        CD_EDGE - boxLeft,
        Math.max(CD_EDGE - boxLeft, cRect.width - boxLeft - barW - CD_EDGE)
      );
      cdBottom = clamp(
        cRect.height - midY - ringH / 2 - tailH - boxBottomGap,
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
      src: measureEl === measure ? "trim" : "figure"
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
  var RING_LEN = 2 * Math.PI * 88;
  function renderMessage(text) {
    const p = document.createElement("p");
    p.textContent = text;
    return p;
  }
  async function main() {
    const info = await api.breakCurrent();
    if (!info) return;
    const data = info;
    document.documentElement.style.setProperty("--accent", data.appearance.color);
    document.documentElement.style.setProperty("--app-opacity", String(1 - data.appearance.transparency));
    document.getElementById("app").dataset.rounded = data.appearance.fullscreen ? "0" : "1";
    const msgBox = document.getElementById("messages");
    for (const m of data.messages) msgBox.appendChild(renderMessage(m));
    if (data.supervisor) {
      msgBox.classList.add("hidden");
      setupSupervisor(data.supervisor, data.messages[0] ?? "");
      bindSupContentLayout(
        document.getElementById("figure"),
        document.querySelector(".content"),
        document.getElementById("figure-trim")
      );
    }
    const ring = document.getElementById("ring");
    const countdown = document.getElementById("countdown");
    const total = data.durationMs;
    function frame() {
      const remain = Math.max(0, data.endsAt - Date.now());
      const sec = Math.ceil(remain / 1e3);
      countdown.textContent = String(sec);
      ring.style.strokeDashoffset = String(RING_LEN * (1 - remain / total));
      if (remain <= 0) return;
      requestAnimationFrame(frame);
    }
    frame();
    if (data.preview) {
      window.setTimeout(() => api.closeWindow(), Math.max(0, data.endsAt - Date.now()));
    }
    const skip = document.getElementById("skip-link");
    skip.classList.remove("hidden");
    skip.addEventListener("click", (e) => {
      e.preventDefault();
      if (data.preview) api.closeWindow();
      else api.action("endBreak");
    });
    window.addEventListener("keydown", (e) => {
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "x") {
        e.preventDefault();
        if (data.preview) api.closeWindow();
        else api.action("endBreak");
      }
    });
    if (data.preview) {
      document.getElementById("preview-badge").classList.remove("hidden");
      document.body.addEventListener("click", () => api.closeWindow());
    }
  }
  main();
})();
