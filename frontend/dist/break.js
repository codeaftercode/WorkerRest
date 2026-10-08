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

  // renderer/supervisorLayout.ts
  var DEFAULTS = { gap: 28, maxWidth: 520, minWidth: 224, ratio: 0.72 };
  var PORTRAIT_RATIO = 1.02;
  var TAIL_INSET = 22;
  var BUBBLE = ".supervisor-line, .sup-preview-line";
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
  function figureCenterX(measureEl, fallback) {
    const img = measureEl.querySelector("img");
    const r = img?.getBoundingClientRect();
    const raw = img?.dataset.trim;
    if (!r || r.width <= 0) return fallback;
    if (!raw) return r.left + r.width / 2;
    try {
      const t = JSON.parse(raw);
      const l = typeof t.left === "number" ? t.left : 0;
      const rr = typeof t.right === "number" ? t.right : 0;
      return r.left + r.width * (1 + l - rr) / 2;
    } catch {
      return r.left + r.width / 2;
    }
  }
  function layoutSupContent(figure, content, measure, opts = {}) {
    const container = figure?.parentElement ?? null;
    if (!container || !figure || !content) return;
    const o = { ...DEFAULTS, ...opts };
    const cRect = container.getBoundingClientRect();
    if (cRect.width <= 0) return;
    const measureEl = pickMeasure(figure, measure);
    const gap = o.gap ?? 0;
    const mode = cRect.height > cRect.width * PORTRAIT_RATIO ? "top" : "side";
    if (container.dataset.supLayout !== mode) container.dataset.supLayout = mode;
    const bubble = content.querySelector(BUBBLE);
    let figLeft = 0;
    let figRight = 0;
    let figTop = 0;
    let width = 0;
    let tailX = 0;
    if (mode === "top") {
      width = Math.max(0, Math.round(cRect.width - gap * 2));
      content.style.left = `${Math.round(gap)}px`;
      content.style.width = `${width}px`;
      content.style.transform = "none";
      content.style.top = "auto";
      content.style.bottom = `${Math.round(gap)}px`;
      if (figure.style.maxHeight === "") {
        figure.dataset.supNatural = String(Math.round(figure.getBoundingClientRect().height));
      }
      const natural = Number(figure.dataset.supNatural) || Math.round(cRect.height);
      figure.style.removeProperty("max-height");
      let boxH = figure.getBoundingClientRect().height;
      for (let i = 0; i < 3 && boxH > 1; i++) {
        const head = figureTop(figure, 0) - cRect.top;
        const need = content.offsetHeight + gap * 2;
        if (head >= need || need >= cRect.height) break;
        boxH = Math.max(1, Math.round(boxH * ((cRect.height - need) / Math.max(1, cRect.height - head))));
        figure.style.maxHeight = `${Math.min(boxH, natural)}px`;
      }
      const fRect = measureEl.getBoundingClientRect();
      figLeft = clamp(fRect.left - cRect.left, 0, cRect.width);
      figRight = clamp(fRect.right - cRect.left, 0, cRect.width);
      figTop = clamp(figureTop(figure, fRect.top) - cRect.top, 0, cRect.height);
      content.style.bottom = `${Math.round(cRect.height - figTop + gap)}px`;
      if (content.offsetHeight > Math.max(0, figTop - gap * 2)) {
        content.style.top = `${Math.round(gap)}px`;
        content.style.bottom = "auto";
      }
      const center = figureCenterX(measureEl, (figLeft + figRight) / 2);
      tailX = center;
      const bRect0 = bubble?.getBoundingClientRect();
      if (bubble && bRect0 && bRect0.width > 0) {
        bubble.style.transform = "none";
        const bRect = bubble.getBoundingClientRect();
        const cr = content.getBoundingClientRect();
        const dx = clamp(
          center - (bRect.left + bRect.width / 2),
          cr.left - bRect.left,
          cr.right - bRect.right
        );
        bubble.style.transform = Math.abs(dx) >= 0.5 ? `translateX(${Math.round(dx)}px)` : "none";
        const x = clamp(
          center - (bRect.left + dx),
          Math.min(TAIL_INSET, bRect.width / 2),
          Math.max(bRect.width - TAIL_INSET, bRect.width / 2)
        );
        tailX = x;
        bubble.style.setProperty("--tail-x", `${Math.round(x)}px`);
      }
    } else {
      const fRect = measureEl.getBoundingClientRect();
      figLeft = clamp(fRect.left - cRect.left, 0, cRect.width);
      figRight = clamp(fRect.right - cRect.left, 0, cRect.width);
      figTop = clamp(figureTop(figure, fRect.top) - cRect.top, 0, cRect.height);
      const band = cRect.width - figRight;
      const avail = Math.max(0, band - gap * 2);
      width = Math.min(o.maxWidth ?? Infinity, avail * (o.ratio ?? 1));
      if (width < (o.minWidth ?? 0)) width = Math.min(o.minWidth ?? 0, avail);
      width = Math.max(0, Math.round(width));
      const cs = getComputedStyle(content);
      const padX = (parseFloat(cs.paddingLeft) || 0) + (parseFloat(cs.paddingRight) || 0);
      content.style.width = `${width + padX}px`;
      content.style.left = `${Math.round(figRight + band / 2 - width / 2)}px`;
      const contentH = content.offsetHeight;
      const floor = contentH / 2 + 8;
      const desired = Math.max(cRect.height / 3, (cRect.height / 3 + figTop) / 2);
      const cap = Math.max(cRect.height / 3, figTop - contentH / 2 - gap);
      const centerY = Math.max(Math.min(desired, cap), floor);
      content.style.top = `${Math.round(centerY)}px`;
      content.style.bottom = "";
      content.style.removeProperty("transform");
      figure.style.removeProperty("max-height");
      delete figure.dataset.supNatural;
      bubble?.style.removeProperty("--tail-x");
      bubble?.style.removeProperty("transform");
    }
    content.style.right = "auto";
    content.dataset.supMetrics = JSON.stringify({
      mode,
      figLeft: Math.round(figLeft),
      figRight: Math.round(figRight),
      figTop: Math.round(figTop),
      width,
      tailX: Math.round(tailX),
      top: mode === "side" ? Math.round(parseFloat(content.style.top) || 0) : void 0,
      src: measureEl === measure ? "trim" : "figure"
    });
  }
  function bindSupContentLayout(figure, content, measure, opts = {}) {
    const relayout = () => layoutSupContent(figure, content, measure, opts);
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
