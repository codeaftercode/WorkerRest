"use strict";
(() => {
  // 历史源码/types.ts
  var SUP_GEOM_DEFAULT = { sizePct: 72, posXPct: 4, posYPct: 0, flip: false };
  var SUP_GEOM_LIMITS = {
    sizePct: [20, 100],
    // 距左上限 30%：再往右立绘会挤掉右侧台词的位置（右侧内容要摆在「督休专员右边缘 ↔ 窗口右边缘」中间）
    posXPct: [0, 30],
    posYPct: [0, 40]
  };
  function normalizeSupGeom(raw) {
    const pick = (k) => {
      const [min, max] = SUP_GEOM_LIMITS[k];
      const v = raw ? raw[k] : void 0;
      return typeof v === "number" && Number.isFinite(v) ? Math.min(max, Math.max(min, Math.round(v))) : SUP_GEOM_DEFAULT[k];
    };
    return {
      sizePct: pick("sizePct"),
      posXPct: pick("posXPct"),
      posYPct: pick("posYPct"),
      // 只认真正的布尔 true：老配置没有这个字段，一律按"不翻转"
      flip: raw?.flip === true
    };
  }
  var SUPERVISOR_SELECTIONS = [
    { value: "fixed", label: "\u56FA\u5B9A\u663E\u793A", hint: "\u6BCF\u6B21\u63D0\u9192\u90FD\u7528\u540C\u4E00\u4E2A\u7ACB\u7ED8\uFF1B\u70B9\u9009\u7ACB\u7ED8\u524D\u7684\u5355\u9009\u6846\u6307\u5B9A" },
    { value: "random", label: "\u968F\u673A\u663E\u793A", hint: "\u6BCF\u6B21\u63D0\u9192\u5728\u5DF2\u52FE\u9009\u7684\u7ACB\u7ED8\u4E2D\u968F\u673A\u9009\u4E00\u4E2A" },
    { value: "sequence", label: "\u987A\u5E8F\u5FAA\u73AF", hint: "\u5728\u5DF2\u52FE\u9009\u7684\u7ACB\u7ED8\u95F4\u6309\u5217\u8868\u987A\u5E8F\u8F6E\u6362" }
  ];
  var WEEKDAYS = [
    { value: 1, label: "\u5468\u4E00", short: "\u4E00" },
    { value: 2, label: "\u5468\u4E8C", short: "\u4E8C" },
    { value: 3, label: "\u5468\u4E09", short: "\u4E09" },
    { value: 4, label: "\u5468\u56DB", short: "\u56DB" },
    { value: 5, label: "\u5468\u4E94", short: "\u4E94" },
    { value: 6, label: "\u5468\u516D", short: "\u516D" },
    { value: 7, label: "\u5468\u65E5", short: "\u65E5" }
  ];
  var DEFAULT_WEEKDAYS = [1, 2, 3, 4, 5];
  function isoWeekday(d) {
    const w = d.getDay();
    return w === 0 ? 7 : w;
  }
  function nextSlotAt(slots, from = Date.now()) {
    let best = null;
    for (const slot of slots) {
      if (!slot.enabled) continue;
      for (let d = 0; d < 8; d++) {
        const day = new Date(from);
        day.setHours(0, 0, 0, 0);
        day.setDate(day.getDate() + d);
        if (slot.repeat === "weekly" && !slot.days.includes(isoWeekday(day))) continue;
        const at = new Date(day);
        at.setHours(slot.hour, slot.minute, 0, 0);
        if (at.getTime() <= from) continue;
        if (!best || at.getTime() < best.at) best = { at: at.getTime(), slot };
        break;
      }
    }
    return best;
  }
  var TAKEOUT_LEAD_LIMITS = [0, 120];
  var TAKEOUT_LEAD_DEFAULT = 0;
  var TAKEOUT_POSTPONE_LIMITS = [0, 120];
  var TAKEOUT_POSTPONE_DEFAULT = 10;
  var APP_PAUSE_PRESETS = [
    { exe: "WeMeetApp.exe", label: "\u817E\u8BAF\u4F1A\u8BAE" },
    { exe: "Feishu.exe", label: "\u98DE\u4E66" },
    { exe: "DingTalk.exe", label: "\u9489\u9489" },
    { exe: "WXWork.exe", label: "\u4F01\u4E1A\u5FAE\u4FE1" },
    { exe: "ms-teams.exe", label: "Microsoft Teams" },
    { exe: "Zoom.exe", label: "Zoom" },
    { exe: "CiscoCollabHost.exe", label: "Webex" },
    { exe: "WeLink.exe", label: "\u534E\u4E3A\u4E91 WeLink" }
  ];
  function appPauseLabel(exe) {
    const k = String(exe ?? "").trim().toLowerCase();
    return APP_PAUSE_PRESETS.find((p) => p.exe.toLowerCase() === k)?.label ?? "";
  }
  var APP_NAME_MAX = 30;
  function normalizeAppName(raw) {
    return String(raw ?? "").replace(/\s+/g, " ").trim().slice(0, APP_NAME_MAX);
  }
  function syncAppName(name, fromExe, toExe) {
    const cur = normalizeAppName(name);
    if (cur && cur !== appPauseLabel(fromExe)) return cur;
    return appPauseLabel(toExe);
  }
  function exeBasename(raw) {
    const s = String(raw ?? "").trim();
    if (!s) return "";
    const seg = s.split(/[\\/]/).filter(Boolean).pop() ?? "";
    return seg.trim().slice(0, 120);
  }
  var BREAK_MODES = [
    { value: "countdown", label: "\u5012\u8BA1\u65F6", hint: "\u6BCF\u9694\u4E00\u6BB5\u65F6\u95F4\u63D0\u9192\u4E00\u6B21" },
    { value: "fixed", label: "\u56FA\u5B9A\u65F6\u95F4", hint: "\u5230\u8BBE\u5B9A\u7684\u65F6\u523B\u63D0\u9192" }
  ];
  var CUSTOM_BREAK_PREFIX = "cb-";
  function defaultCustomBreakConfig() {
    return {
      enabled: true,
      mode: "countdown",
      intervalMs: 10 * 6e4,
      durationMs: 2e4,
      notifyBeforeMs: 1e4,
      postponeMs: 2 * 6e4,
      slots: []
    };
  }
  function breakEntries(s) {
    const out = [
      { id: "short", name: "\u77ED\u4F11\u606F", config: s.short },
      { id: "long", name: "\u957F\u4F11\u606F", config: s.long }
    ];
    for (const c of s.customBreaks ?? []) out.push({ id: c.id, name: c.name, config: c.config });
    return out;
  }
  function breakConfigById(s, id) {
    const e = breakEntries(s).find((x) => x.id === id);
    return e ? e.config : null;
  }
  function pauseReasonText(reason) {
    switch (reason) {
      case "idle":
        return "\u7CFB\u7EDF\u7A7A\u95F2";
      case "manual":
        return "\u624B\u52A8\u6682\u505C";
      default:
        return reason;
    }
  }

  // 历史源码/fixedTime.ts
  var pad2 = (n) => String(n).padStart(2, "0");
  function slotTimeText(slot) {
    return `${pad2(slot.hour)}:${pad2(slot.minute)}`;
  }
  function describeTimeSlot(slot) {
    const time = slotTimeText(slot);
    if (slot.repeat === "daily") return `\u6BCF\u5929 ${time}`;
    const names = WEEKDAYS.filter((d) => slot.days.includes(d.value)).map((d) => d.short);
    return `\u6BCF\u5468 ${names.join("\u3001") || "\uFF08\u672A\u9009\uFF09"} ${time}`;
  }

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
      // 未过 IGNORE 阈值的原始边距（摆位锚点专用，与 break/takeout 同一套）：
      // IGNORE 会把 <0.8% 的边距归零，对变换无害，但锚点必须指向像素真实位置。
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
      // ⚠️ fill 必须按**布局盒**算，不能读 img 的 getBoundingClientRect：img 自身
      // 可能带着 Stage 变换（scale(1.03) scaleY(1.006)，纵向 1.03618 > 横向 1.03），
      // 立绘常在变换生效之后才加载完——量到变换后尺寸会把 fill 压成 0.994（应 1.0），
      // trim 平移量整体偏小、预览摆位一起算歪。host 变换已重置为 none，其 rect 即布局盒。
      const rendered = host.getBoundingClientRect();
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

  // renderer/supervisorLayout.ts（对话框版式）：预览面板与休息窗口共用同一套
  // 摆位规则——底部悬浮对话框（内缩、实测框顶定中点），台词裸排在框里。
  function clamp(v, lo, hi) {
    return Math.min(Math.max(v, lo), hi);
  }
  // 立绘「去掉透明部分后」的可见边缘（容器坐标）——与 break/takeout 同一套几何：
  // .trim 补偿变换 translate(-left*s%, bottom*s*fill%) scale(s)（origin: left bottom）
  // 把 img 盒（铺满立绘盒，盒左缘 x0、底缘 yB、盒宽 W、盒高 H）映射到屏幕，
  // 像素内容的四条可见边：
  //   · 左缘 = x0 + s*W*(rawLeft - left)
  //   · 右缘 = x0 + s*W*(1 - rawRight - left)
  //   · 顶缘 = yB - s*fill*H*(1 - rawTop - bottom)
  //   · 底缘 = yB - s*fill*H*(rawBottom - bottom)
  // left/bottom 是变换实际使用的边距（过 IGNORE，可能被归零），rawLeft/rawTop 等是
  // 像素真实边距（未归零）——二者只在边距 <0.8% 时不同。
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
  // 与休息窗口的 layoutSupDialog 同一套规则（见 break.js）：锚点中点
  //   水平 midX = (立绘可见右缘 + 容器右缘) / 2
  //   垂直 midY = (对话框上边缘 + 容器上边缘) / 2（框顶实测，内缩自动跟随）。
  // 对话框（.sup-preview-content）与预览面板边缘的偏移由 CSS 决定（当前贴边
  // 为 0），摆位用实测 rect 换算。预览里没有倒计时环（.bar 不存在），环分支
  // 保留与 break.js 同构。
  var CD_EDGE = 10;
  function layoutSupDialog(figure, content, measure) {
    const container = figure?.parentElement ?? null;
    if (!container || !figure || !content) return;
    const cRect = container.getBoundingClientRect();
    if (cRect.width <= 0) return;
    const figBox = figure.getBoundingClientRect();
    const figImg = figure.querySelector("img");
    const haveTrim = !!(figImg && figImg.dataset.trim);
    const vis = figureVisibleRect(figure, figBox.top);
    const figLeft = clamp(vis.left - cRect.left, 0, cRect.width);
    const figRight = clamp(vis.right - cRect.left, 0, cRect.width);
    const figTop = clamp(vis.top - cRect.top, 0, cRect.height);
    const bar = content.querySelector(".bar");
    const boxRect = content.getBoundingClientRect();
    const boxLeft = boxRect.left - cRect.left; // 对话框左缘相对容器（= 水平内缩量）
    const boxBottomGap = cRect.bottom - boxRect.bottom; // 对话框底缘距容器底（= 底部内缩量）
    const boxTop = boxRect.top - cRect.top; // 对话框顶缘相对容器
    const midX = (figRight + cRect.width) / 2;
    const midY = boxTop / 2;
    let cdX = midX - boxLeft;
    let cdBottom = cRect.height - midY - boxBottomGap;
    if (bar) {
      const ringWrap = bar.querySelector(".ring-wrap");
      const barW = bar.offsetWidth || 84;
      const ringH = (ringWrap && ringWrap.offsetHeight) || 84;
      const barH = bar.offsetHeight || ringH;
      const tailH = Math.max(0, barH - ringH);
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

  // renderer/settings.ts
  var S;
  var ROLES = [];
  var curSupStage = 1;
  var supPreview = null;
  var supEditKey = "";
  var $ = (id) => document.getElementById(id);
  function applyTitlebarMaximized(max) {
    document.getElementById("titlebar")?.classList.toggle("maximized", max);
    const btn = document.getElementById("wb-max");
    if (btn) {
      const label = max ? "\u8FD8\u539F" : "\u6700\u5927\u5316";
      btn.title = label;
      btn.setAttribute("aria-label", label);
    }
  }
  function toast(msg) {
    const t = $("toast");
    t.textContent = msg;
    t.classList.remove("hidden");
    setTimeout(() => t.classList.add("hidden"), 2200);
  }
  var cfResolve = null;
  function confirmDialog(opts) {
    $("cf-title").textContent = opts.title;
    const desc = $("cf-desc");
    desc.textContent = opts.desc ?? "";
    desc.classList.toggle("hidden", !opts.desc);
    const detail = $("cf-detail");
    detail.textContent = opts.detail ?? "";
    detail.classList.toggle("hidden", !opts.detail);
    $("cf-ok").textContent = opts.okText ?? "\u5220\u9664";
    $("cf-cancel").textContent = opts.cancelText ?? "\u53D6\u6D88";
    $("confirm-modal").classList.remove("hidden");
    setTimeout(() => $("cf-cancel").focus(), 0);
    return new Promise((resolve) => {
      cfResolve = resolve;
    });
  }
  function closeConfirm(ok) {
    const box = $("confirm-modal");
    if (!box || box.classList.contains("hidden")) return;
    box.classList.add("hidden");
    const resolve = cfResolve;
    cfResolve = null;
    resolve?.(ok);
  }
  var pmResolve = null;
  function promptDialog(opts) {
    $("pm-title").textContent = opts.title;
    const desc = $("pm-desc");
    desc.textContent = opts.desc ?? "";
    desc.classList.toggle("hidden", !opts.desc);
    const label = $("pm-label");
    label.textContent = opts.label ?? "";
    label.classList.toggle("hidden", !opts.label);
    const input = $("pm-input");
    input.value = opts.value ?? "";
    input.placeholder = opts.placeholder ?? "";
    $("pm-ok").textContent = opts.okText ?? "\u786E\u5B9A";
    $("prompt-modal").classList.remove("hidden");
    setTimeout(() => {
      input.focus();
      input.select();
    }, 0);
    return new Promise((resolve) => {
      pmResolve = resolve;
    });
  }
  function closePrompt(v) {
    const box = $("prompt-modal");
    if (!box || box.classList.contains("hidden")) return;
    box.classList.add("hidden");
    const resolve = pmResolve;
    pmResolve = null;
    resolve?.(v);
  }
  function currentGeomKey() {
    return supEditKey;
  }
  function currentRoleName() {
    return ROLES.find((r) => r.key === supEditKey)?.name ?? "\u2014";
  }
  function geomOf(key) {
    return ROLES.find((r) => r.key === key)?.geom ?? null;
  }
  function persistRole(key) {
    const role = ROLES.find((r) => r.key === key);
    if (!role) return;
    void window.api.saveSupervisorRole(key, {
      name: role.name,
      enabled: role.enabled,
      geom: role.geom,
      messages: role.messages
    });
  }
  function ensureSupEditKey() {
    if (supEditKey && ROLES.some((r) => r.key === supEditKey)) return;
    if (S.supervisor.selection === "fixed") {
      // 固定模式：按 fixedKey 选（不再要求 enabled）
      supEditKey = ROLES.find((r) => r.key === S.supervisor.fixedKey)?.key ?? ROLES[0]?.key ?? "";
      return;
    }
    // 随机 / 顺序：优先已勾选立绘，否则列表第一个
    const firstEnabled = ROLES.find((r) => r.enabled);
    supEditKey = firstEnabled?.key ?? ROLES[0]?.key ?? "";
  }
  function commitGeomToRole(g) {
    const role = ROLES.find((r) => r.key === currentGeomKey());
    if (!role) return false;
    role.geom = { ...g };
    return true;
  }
  function applySelectionUI() {
    const btn = $("sup-selection-btn");
    if (!btn) return;
    applyModeIcon(btn, S.supervisor.selection ?? "fixed");
  }
  function fillSupervisorImageName() {
    const name = currentRoleName();
    const geomRole = $("sup-geom-role");
    if (geomRole) geomRole.textContent = name;
    const msgRole = $("sup-msg-role");
    if (msgRole) msgRole.textContent = name;
    const advRole = $("sup-adv-role");
    if (advRole) advRole.textContent = name;
    for (const btn of document.querySelectorAll("#sup-gallery .role-item")) {
      btn.classList.toggle("active", btn.dataset.key === supEditKey);
    }
  }
  function selectRole(key) {
    const role = ROLES.find((r) => r.key === key);
    if (!role) return;
    supEditKey = key;
    if (S.supervisor.selection === "fixed") {
      // 固定显示：点选即把该立绘设为固定项（单选框语义：同时取消其它选中）
      if (S.supervisor.fixedKey !== key) {
        S.supervisor.fixedKey = key;
        autoSave();
      }
    } else {
      // 随机 / 顺序显示：点选即切换该立绘的勾选态（复选框语义）
      role.enabled = !role.enabled;
      persistRole(key);
      autoSave();
    }
    fillSupervisorImageName();
    fillGeomControls(normalizeSupGeom(geomOf(supEditKey)));
    void refreshSupervisorPreview();
    void renderRoleGallery();
  }
  async function renameRole(key) {
    const role = ROLES.find((r) => r.key === key);
    if (!role) return;
    const input = await promptDialog({
      title: "\u91CD\u547D\u540D",
      desc: "\u540D\u79F0\u4F1A\u663E\u793A\u5728\u7ACB\u7ED8\u5E93\u78C1\u8D34\u3001\u53F0\u8BCD\u6C14\u6CE1\u7684\u8BF4\u8BDD\u4EBA\u4E0E\u63D0\u9192\u5185\u5BB9\u9762\u677F\u6807\u9898\u3002",
      value: role.name,
      placeholder: "\u540D\u79F0",
      okText: "\u786E\u5B9A"
    });
    if (input === null) return;
    role.name = input.trim() || role.name;
    persistRole(key);
    fillSupervisorImageName();
    autoSave();
    await refreshSupervisorPreview();
    await renderRoleGallery();
    toast(`\u5DF2\u91CD\u547D\u540D\u4E3A\u300C${role.name}\u300D`);
  }
  async function replaceRoleImage(key) {
    const role = ROLES.find((r) => r.key === key);
    if (!role) return;
    const ok = await window.api.replaceSupervisorImage(key);
    if (!ok) return;
    // 换图后缩略图要重读，否则磁贴仍显示旧图
    ROLES = await window.api.listSupervisorRoles();
    if (supEditKey === key) {
      // 换的是当前编辑督休专员：预览要重读（Go 侧会按新立绘重新组装会话）
      await refreshSupervisorPreview();
    }
    await renderRoleGallery();
    fillSupervisorImageName();
    toast(`\u5DF2\u66F4\u65B0\u300C${role.name}\u300D\u7684\u7ACB\u7ED8\u56FE\u7247`);
  }
  // 启用/禁用菜单项已移除：随机/顺序模式改由列表复选框直接控制 role.enabled
  async function removeRole(key) {
    const role = ROLES.find((r) => r.key === key);
    if (!role) return;
    const ok = await confirmDialog({
      title: "\u5220\u9664",
      desc: `\u786E\u5B9A\u5220\u9664\u300C${role.name}\u300D\uFF1F`,
      detail: "\u4F1A\u8FDE\u540C\u76EE\u5F55\uFF08\u914D\u7F6E\u3001\u7ACB\u7ED8\u3001\u9884\u89C8\u56FE\uFF09\u4E00\u5E76\u5220\u9664\uFF0C\u4E14\u65E0\u6CD5\u6062\u590D\u3002",
      okText: "\u5220\u9664"
    });
    if (!ok) return;
    await window.api.removeSupervisorRole(key);
    ROLES = ROLES.filter((r) => r.key !== key);
    if (S.supervisor.fixedKey === key) {
      S.supervisor.fixedKey = ROLES[0]?.key ?? "";
    }
    ensureSupEditKey();
    fillSupervisorImageName();
    autoSave();
    await refreshSupervisorPreview();
    await renderRoleGallery();
    toast("\u5DF2\u5220\u9664");
  }
  async function addCustomRole() {
    const picked = await window.api.pickSupervisorImage();
    if (!picked) return;
    const base = picked.name.trim() || "\u81EA\u5B9A\u4E49\u7ACB\u7ED8";
    const input = await promptDialog({
      title: "\u547D\u540D",
      desc: "\u7ED9\u8FD9\u4E2A\u7ACB\u7ED8\u8D77\u4E00\u4E2A\u540D\u5B57\uFF08\u663E\u793A\u5728\u7ACB\u7ED8\u5E93\u78C1\u8D34\u4E0E\u63D0\u9192\u5185\u5BB9\u9762\u677F\u6807\u9898\uFF09\u3002",
      value: base,
      placeholder: "\u4F8B\u5982\uFF1A\u6211\u7684\u4E0A\u53F8",
      okText: "\u6DFB\u52A0"
    });
    if (input === null) {
      await window.api.removeSupervisorRole(picked.key);
      return;
    }
    const name = input.trim() || base;
    await window.api.saveSupervisorRole(picked.key, { name });
    ROLES = await window.api.listSupervisorRoles();
    supEditKey = picked.key;
    if (S.supervisor.selection === "fixed" && !S.supervisor.fixedKey) S.supervisor.fixedKey = picked.key;
    fillSupervisorImageName();
    autoSave();
    await refreshSupervisorPreview();
    await renderRoleGallery();
    toast(`\u5DF2\u6DFB\u52A0\u300C${name}\u300D`);
  }
  function closeRoleMenu() {
    document.getElementById("sup-role-menu")?.remove();
  }
  function openRoleMenu(key, x, y) {
    closeRoleMenu();
    const role = ROLES.find((r) => r.key === key);
    if (!role) return;
    const menu = document.createElement("div");
    menu.id = "sup-role-menu";
    const items = [
      { act: "preview", label: "\u9884\u89C8" },
      { act: "rename", label: "\u91CD\u547D\u540D" },
      { act: "replace", label: "\u4FEE\u6539\u56FE\u7247" },
      { act: "del", label: "\u5220\u9664", danger: true }
    ];
    for (const it of items) {
      const b = document.createElement("button");
      b.type = "button";
      b.className = "role-menu-item" + (it.danger ? " danger" : "");
      b.dataset.act = it.act;
      b.textContent = it.label;
      b.addEventListener("click", () => {
        closeRoleMenu();
        if (it.act === "preview") void previewRoleFromMenu(key);
        else if (it.act === "rename") void renameRole(key);
        else if (it.act === "replace") void replaceRoleImage(key);
        else void removeRole(key);
      });
      menu.appendChild(b);
    }
    document.body.appendChild(menu);
    const rect = menu.getBoundingClientRect();
    menu.style.left = `${Math.max(8, Math.min(x, window.innerWidth - rect.width - 8))}px`;
    menu.style.top = `${Math.max(8, Math.min(y, window.innerHeight - rect.height - 8))}px`;
  }
  function bindRoleMenuDismiss() {
    document.addEventListener("click", (e) => {
      const menu = document.getElementById("sup-role-menu");
      if (menu && !menu.contains(e.target)) closeRoleMenu();
    });
    document.addEventListener("keydown", (e) => {
      if (e.key === "Escape") closeRoleMenu();
    });
  }
  async function previewRoleFromMenu(key) {
    autoSave();
    await window.api.previewBreak("short", key);
  }
  var GALLERY_H_KEY = "wr.galleryH";
  function galleryMinHeight() {
    const body = $("sup-gallery-body");
    const box = $("sup-gallery");
    const first = box.querySelector(".role-item") ?? box.querySelector(".role-add");
    if (!first) return 0;
    const rowH = first.getBoundingClientRect().height;
    if (rowH < 40) return 0;
    const gap = parseFloat(getComputedStyle(box).rowGap) || 0;
    const padV = (el) => {
      const s = getComputedStyle(el);
      return parseFloat(s.paddingTop) + parseFloat(s.paddingBottom);
    };
    return Math.round(rowH * 2 + gap + padV(body) + padV(box) + 1);
  }
  function applyGalleryHeight(stored) {
    const body = $("sup-gallery-body");
    const min = galleryMinHeight();
    if (min <= 0) return;
    body.style.minHeight = String(min) + "px";
    const h = Math.max(min, Math.round(stored ?? 0) || min);
    body.style.height = String(h) + "px";
  }
  function setupGalleryPanel() {
    const panel = $("sup-gallery-panel");
    const body = $("sup-gallery-body");
    panel.querySelector(".msg-toggle").addEventListener("click", () => {
      panel.classList.toggle("open");
      body.classList.toggle("hidden");
    });
    const grip = $("sup-gallery-resize");
    grip.addEventListener("mousedown", (e) => {
      e.preventDefault();
      grip.classList.add("dragging");
      const startY = e.clientY;
      const startH = body.getBoundingClientRect().height;
      const onMove = (ev) => {
        applyGalleryHeight(startH + (ev.clientY - startY));
      };
      const onUp = () => {
        grip.classList.remove("dragging");
        document.removeEventListener("mousemove", onMove);
        document.removeEventListener("mouseup", onUp);
        localStorage.setItem(GALLERY_H_KEY, String(Math.round(body.getBoundingClientRect().height)));
      };
      document.addEventListener("mousemove", onMove);
      document.addEventListener("mouseup", onUp);
    });
    const ro = new ResizeObserver(() => {
      if (grip.classList.contains("dragging")) return;
      const min = galleryMinHeight();
      if (min <= 0) return;
      body.style.minHeight = String(min) + "px";
      if (!(Number(localStorage.getItem(GALLERY_H_KEY)) || 0)) body.style.height = String(min) + "px";
    });
    ro.observe($("sup-gallery"));
    applyGalleryHeight(Number(localStorage.getItem(GALLERY_H_KEY)) || null);
  }
  async function renderRoleGallery() {
    const box = $("sup-gallery");
    box.innerHTML = "";
    const fixedMode = S.supervisor.selection === "fixed";
    for (const r of ROLES) {
      const btn = document.createElement("button");
      btn.type = "button";
      // 未勾选不改磁贴样式，仅复选框呈未选态
      btn.className = "role-item";
      btn.dataset.key = r.key;
      const img = document.createElement("img");
      if (r.thumb) img.src = r.thumb;
      img.alt = r.name;
      const hint = document.createElement("span");
      hint.className = "role-hint";
      hint.textContent = "\u53F3\u952E\u64CD\u4F5C";
      // 选择指示：固定模式=单选框（按 fixedKey），随机/顺序=复选框（按 enabled）
      const sel = document.createElement("span");
      sel.className = "role-sel " + (fixedMode ? "role-radio" : "role-check");
      sel.classList.toggle("on", fixedMode ? r.key === S.supervisor.fixedKey : r.enabled);
      const cap = document.createElement("span");
      cap.className = "role-cap";
      cap.textContent = r.name;
      const capRow = document.createElement("span");
      capRow.className = "role-cap-row";
      capRow.append(sel, cap);
      btn.append(img, hint, capRow);
      btn.addEventListener("click", () => selectRole(r.key));
      btn.addEventListener("contextmenu", (e) => {
        e.preventDefault();
        e.stopPropagation();
        openRoleMenu(r.key, e.clientX, e.clientY);
      });
      box.appendChild(btn);
    }
    const add = document.createElement("button");
    add.type = "button";
    add.className = "role-add";
    add.title = "\u65B0\u589E\uFF1A\u652F\u6301 PNG / JPG / WebP\uFF0C\u5EFA\u8BAE\u900F\u660E\u80CC\u666F\u7684\u7AD6\u7248\u4EBA\u7269\u56FE\uFF1B\u56FE\u7247\u4F1A\u590D\u5236\u5230\u914D\u7F6E\u76EE\u5F55\uFF0C\u539F\u6587\u4EF6\u79FB\u52A8\u6216\u5220\u9664\u540E\u4ECD\u53EF\u7528";
    add.innerHTML = '<span class="role-add-box"><span class="role-add-plus">\uFF0B</span><span class="role-add-hint">PNG / JPG / WebP<br>\u900F\u660E\u5E95\u7AD6\u7248\u56FE</span></span><span class="role-add-cap">\u65B0\u589E</span>';
    add.addEventListener("click", () => void addCustomRole());
    box.appendChild(add);
    $("sup-gallery-count").textContent = String(ROLES.length);
    fillSupervisorImageName();
    applyGalleryHeight(Number(localStorage.getItem(GALLERY_H_KEY)) || null);
  }
  function updateGeomSummary(g) {
    const el = $("sup-geom-summary");
    if (!el) return;
    const custom = g.sizePct !== SUP_GEOM_DEFAULT.sizePct || g.posXPct !== SUP_GEOM_DEFAULT.posXPct || g.posYPct !== SUP_GEOM_DEFAULT.posYPct || g.flip !== SUP_GEOM_DEFAULT.flip;
    el.textContent = `${custom ? "\u5DF2\u8C03\u6574 \xB7 " : ""}\u9AD8\u5EA6 ${g.sizePct}% \xB7 \u8DDD\u5DE6 ${g.posXPct}% \xB7 \u8DDD\u5E95 ${g.posYPct}%` + (g.flip ? " \xB7 \u5DF2\u7FFB\u8F6C" : "");
    el.classList.toggle("customized", custom);
    el.title = "\u70B9\u5F00\u300C\u9AD8\u7EA7\u9009\u9879\u300D\u53EF\u8C03\u6574\u5F53\u524D\u7ACB\u7ED8\u7684\u663E\u793A\u5C3A\u5BF8\u4E0E\u4F4D\u7F6E\uFF0C\u4EE5\u53CA\u662F\u5426\u6C34\u5E73\u7FFB\u8F6C";
  }
  var supRelayout = null;
  function applySupGeometry(g) {
    const box = $("sup-preview");
    box.style.setProperty("--fig-size", String(g.sizePct));
    box.style.setProperty("--fig-x", String(g.posXPct));
    box.style.setProperty("--fig-y", String(g.posYPct));
    box.classList.toggle("sup-flip", g.flip);
    const pImg = $("sup-preview-img");
    if (pImg && pImg.src) applyFigureTrim($("sup-preview-trim"), pImg, g.flip);
    $("sup-size-val").textContent = `${g.sizePct}%`;
    $("sup-posx-val").textContent = `${g.posXPct}%`;
    $("sup-posy-val").textContent = `${g.posYPct}%`;
    updateGeomSummary(g);
    supRelayout ??= bindSupContentLayout(
      document.querySelector(".sup-preview-fig"),
      document.querySelector(".sup-preview-content"),
      $("sup-preview-trim")
    );
    supRelayout();
  }
  function fillGeomControls(g) {
    $("sup-size").value = String(g.sizePct);
    $("sup-posx").value = String(g.posXPct);
    $("sup-posy").value = String(g.posYPct);
    $("sup-flip").checked = g.flip;
    applySupGeometry(g);
  }
  function clampGeom(el, key) {
    const [min, max] = SUP_GEOM_LIMITS[key];
    return clampNum(el, min, max, SUP_GEOM_DEFAULT[key]);
  }
  function readGeomFromForm() {
    return {
      sizePct: clampGeom($("sup-size"), "sizePct"),
      posXPct: clampGeom($("sup-posx"), "posXPct"),
      posYPct: clampGeom($("sup-posy"), "posYPct"),
      flip: $("sup-flip").checked
    };
  }
  function renderSupervisorPreview(stage) {
    curSupStage = stage;
    const box = $("sup-preview");
    box.dataset.stage = String(stage);
    box.style.setProperty("--accent", S.appearance.color);
    const off = !S.supervisor.enabled;
    // 顶层「启用」开关：未启用时整块隐藏立绘与对话框（CSS 据 data-enabled 隐藏），
    // 只留背景 + 中央关闭提示，避免「无立绘却挂着对话框」的错位观感
    box.dataset.enabled = off ? "0" : "1";
    const badge = $("sup-preview-badge");
    badge.classList.toggle("hidden", !off);
    badge.textContent = off ? "\u529F\u80FD\u5DF2\u5173\u95ED \xB7 \u63D0\u9192\u7A97\u53E3\u4E0D\u4F1A\u663E\u793A\u7763\u4F11\u4E13\u5458" : "";
    $("sup-preview-line").textContent = supPreview ? stage === 2 ? supPreview.lines.command || supPreview.lines.urge : supPreview.lines.urge : "\u65E0\u6CD5\u8BFB\u53D6\u7ACB\u7ED8\u6587\u4EF6";
    // 说话人由对话框左上角的名字牌承载（与休息窗口的 dialog-name 同款），不再挂在台词上
    const previewName = $("sup-preview-name");
    if (supPreview?.name) {
      previewName.textContent = supPreview.name;
      previewName.classList.remove("hidden");
    } else {
      previewName.textContent = "";
      previewName.classList.add("hidden");
    }
    if (supPreview) {
      const img = $("sup-preview-img");
      img.src = supPreview.image;
      applyFigureTrim($("sup-preview-trim"), img, supPreview.geom.flip === true);
    }
  }
  async function refreshSupervisorPreview(keepUrge = false) {
    const prevUrge = supPreview?.lines.urge;
    supPreview = await window.api.previewSupervisor(S.supervisor, supEditKey);
    if (supPreview) {
      if (keepUrge && prevUrge) supPreview.lines.urge = prevUrge;
      fillGeomControls(supPreview.geom);
    }
    renderMsgList("sup");
    renderSupervisorPreview(curSupStage);
  }
  var previewAudio = null;
  var previewFile = "";
  function fmtPreviewTime(sec) {
    if (!isFinite(sec) || sec < 0) sec = 0;
    const m = Math.floor(sec / 60);
    const s = Math.floor(sec % 60);
    return `${m}:${String(s).padStart(2, "0")}`;
  }
  function stopSoundPreview() {
    const bar = $("snd-player");
    if (previewAudio) {
      previewAudio.pause();
      previewAudio.src = "";
      previewAudio = null;
    }
    previewFile = "";
    if (bar) bar.classList.add("hidden");
  }
  async function previewSoundFile(file, name) {
    stopSoundPreview();
    const url = await window.api.soundFileUrl(file);
    if (!url) {
      toast("\u627E\u4E0D\u5230\u8BE5\u97F3\u9891\u6587\u4EF6");
      return;
    }
    const a = new Audio(url);
    a.volume = Math.min(1, Math.max(0, S.sound.volume));
    previewAudio = a;
    previewFile = file;
    const bar = $("snd-player");
    const seek = $("snd-player-seek");
    const time = $("snd-player-time");
    const toggle = $("snd-player-toggle");
    $("snd-player-name").textContent = name;
    seek.value = "0";
    time.textContent = "0:00 / \u2026";
    toggle.textContent = "\u6682\u505C";
    bar.classList.remove("hidden");
    a.addEventListener("timeupdate", () => {
      if (a !== previewAudio || !a.duration) return;
      seek.value = String(Math.round(a.currentTime / a.duration * 1e3));
      time.textContent = `${fmtPreviewTime(a.currentTime)} / ${fmtPreviewTime(a.duration)}`;
    });
    a.addEventListener("ended", () => {
      if (a === previewAudio) stopSoundPreview();
    });
    a.addEventListener("error", () => {
      if (a === previewAudio) {
        toast("\u8BD5\u542C\u5931\u8D25\uFF1A\u6587\u4EF6\u4E0D\u53EF\u7528");
        stopSoundPreview();
      }
    });
    void a.play().catch(() => {
      toast("\u8BD5\u542C\u5931\u8D25\uFF1A\u6587\u4EF6\u4E0D\u53EF\u7528");
      stopSoundPreview();
    });
  }
  function bindSoundPlayer() {
    const seek = $("snd-player-seek");
    const toggle = $("snd-player-toggle");
    seek.addEventListener("input", () => {
      const a = previewAudio;
      if (a && isFinite(a.duration) && a.duration > 0) a.currentTime = parseFloat(seek.value) / 1e3 * a.duration;
    });
    toggle.addEventListener("click", () => {
      const a = previewAudio;
      if (!a) return;
      if (a.paused) {
        void a.play().catch(() => void 0);
        toggle.textContent = "\u6682\u505C";
      } else {
        a.pause();
        toggle.textContent = "\u7EE7\u7EED";
      }
    });
    $("snd-player-stop").addEventListener("click", stopSoundPreview);
  }
  function renderSoundMusic() {
    const list = $("snd-music-list");
    if (!list) return;
    list.textContent = "";
    const music = Array.isArray(S.sound.music) ? S.sound.music : [];
    const singleMode = S.sound.mode === "single";
    if (music.length === 0) {
      const empty = document.createElement("p");
      empty.className = "hint";
      empty.textContent = "\u5217\u8868\u5DF2\u7A7A\uFF1A\u64AD\u653E\u65F6\u4E0D\u4F1A\u518D\u6709\u63D0\u793A\u97F3\uFF0C\u70B9\u6807\u9898\u884C\u7684\u300C\uFF0B\u300D\u6DFB\u52A0\u3002";
      list.appendChild(empty);
      return;
    }
    for (const m of music) {
      const row = document.createElement("div");
      row.className = "tk-item" + (!singleMode && !m.enabled ? " disabled" : "");
      const handle = document.createElement("span");
      handle.className = "msg-handle";
      handle.textContent = "\u283F";
      handle.title = "\u62D6\u52A8\u8C03\u6574\u987A\u5E8F";
      let ctrl;
      if (singleMode) {
        ctrl = document.createElement("input");
        ctrl.type = "radio";
        ctrl.name = "snd-single";
        ctrl.value = m.id;
        ctrl.checked = m.id === S.sound.singleId;
        ctrl.title = "\u5355\u66F2\u5FAA\u73AF\u64AD\u653E\u8FD9\u9996";
        ctrl.addEventListener("change", () => {
          if (!ctrl.checked) return;
          S.sound.singleId = m.id;
          autoSave();
        });
      } else {
        ctrl = document.createElement("input");
        ctrl.type = "checkbox";
        ctrl.checked = m.enabled;
        ctrl.title = "\u542F\u7528 / \u505C\u7528\uFF1A\u662F\u5426\u53C2\u4E0E\u968F\u673A / \u987A\u5E8F\u64AD\u653E";
        ctrl.addEventListener("change", () => {
          m.enabled = ctrl.checked;
          row.classList.toggle("disabled", !ctrl.checked);
          autoSave();
        });
      }
      const name = document.createElement("span");
      name.className = "snd-music-name";
      name.textContent = m.name;
      name.title = m.file;
      const test = document.createElement("button");
      test.className = "btn mini";
      test.textContent = "\u8BD5\u542C";
      test.addEventListener("click", () => {
        void previewSoundFile(m.file, m.name);
      });
      const del = document.createElement("button");
      del.className = "btn mini danger";
      del.textContent = "\u2715";
      del.title = "\u5220\u9664\u8FD9\u9996\u97F3\u4E50";
      del.addEventListener("click", async () => {
        const ok = await confirmDialog({
          title: "\u5220\u9664\u8FD9\u9996\u97F3\u4E50\uFF1F",
          desc: "\u5220\u9664\u540E\u5C06\u4ECE\u97F3\u4E50\u5217\u8868\u79FB\u9664\uFF0C\u5BFC\u5165\u7684\u97F3\u9891\u526F\u672C\u4E5F\u4F1A\u4E00\u5E76\u6E05\u9664\u3002",
          detail: m.name,
          okText: "\u5220\u9664"
        });
        if (!ok) return;
        if (previewFile === m.file) stopSoundPreview();
        S.sound.music = music.filter((x) => x !== m);
        if (S.sound.singleId === m.id) S.sound.singleId = "";
        void window.api.deleteMusicSound(m.file);
        renderSoundMusic();
        autoSave();
        toast("\u5DF2\u5220\u9664");
      });
      row.append(handle, ctrl, name, test, del);
      list.appendChild(row);
      handle.addEventListener("mousedown", () => {
        row.draggable = true;
      });
      row.addEventListener("dragstart", (e) => {
        row.classList.add("dragging");
        if (e.dataTransfer) {
          e.dataTransfer.setData("text/plain", m.id);
          e.dataTransfer.effectAllowed = "move";
        }
      });
      row.addEventListener("dragend", () => {
        row.classList.remove("dragging");
        row.draggable = false;
      });
      row.addEventListener("dragover", (e) => {
        e.preventDefault();
        if (e.dataTransfer) e.dataTransfer.dropEffect = "move";
        row.classList.add("drop-target");
      });
      row.addEventListener("dragleave", () => row.classList.remove("drop-target"));
      row.addEventListener("drop", (e) => {
        e.preventDefault();
        row.classList.remove("drop-target");
        const dragId = e.dataTransfer?.getData("text/plain");
        if (!dragId || dragId === m.id) return;
        const from = S.sound.music.findIndex((i) => i.id === dragId);
        const to = S.sound.music.findIndex((i) => i.id === m.id);
        if (from < 0 || to < 0) return;
        const [moved] = S.sound.music.splice(from, 1);
        S.sound.music.splice(to, 0, moved);
        renderSoundMusic();
        autoSave();
      });
    }
  }
  /* 播放方式：三种循环策略的图标与名称。图标随当前模式切换（单曲循环=带「1」的
     循环箭头、随机播放=交叉箭头、顺序循环=循环箭头），常驻只显示图标，悬浮由
     title 显示文字名称；点击在三者间循环切换。 */
  var SND_MODE = {
    single: {
      label: "单曲循环",
      icon: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"><path d="m17 2 4 4-4 4"/><path d="M3 11v-1a4 4 0 0 1 4-4h14"/><path d="m7 22-4-4 4-4"/><path d="M21 13v1a4 4 0 0 1-4 4H3"/><path d="M11 10h1v4"/></svg>'
    },
    random: {
      label: "随机播放",
      icon: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"><path d="M2 18h1.4c1.3 0 2.5-.6 3.3-1.7l6.1-8.6c.7-1.1 1.9-1.7 3.2-1.7H22"/><path d="m18 2 4 4-4 4"/><path d="M2 6h1.9c1.5 0 2.9.9 3.6 2.2"/><path d="M22 18h-4.7c-1.3 0-2.5-.6-3.3-1.7l-.5-.7"/><path d="m18 14 4 4-4 4"/></svg>'
    },
    sequence: {
      label: "顺序循环",
      icon: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"><path d="m17 2 4 4-4 4"/><path d="M3 11v-1a4 4 0 0 1 4-4h14"/><path d="m7 22-4-4 4-4"/><path d="M21 13v1a4 4 0 0 1-4 4H3"/></svg>'
    }
  };
  var SND_MODE_NEXT = { single: "random", random: "sequence", sequence: "single" };
  function setSoundModeUI(mode) {
    const btn = $("snd-mode");
    if (!btn) return;
    if (!SND_MODE[mode]) mode = "single";
    const info = SND_MODE[mode];
    btn.dataset.mode = mode;
    btn.setAttribute("aria-label", "播放方式：" + info.label);
    btn.title = info.label;
    const ico = btn.querySelector(".snd-mode-ico");
    if (ico) ico.innerHTML = info.icon;
  }
  // 提醒内容 / 立绘的「显示方式」图标按钮：复用声音卡三态图标（固定显示↔单曲循环、
  // 随机显示↔随机播放、顺序循环↔顺序循环），但标签改为提醒语境；点击循环切换。
  var DISPLAY_MODE = {
    fixed: { label: "固定显示", icon: SND_MODE.single.icon },
    random: { label: "随机显示", icon: SND_MODE.random.icon },
    sequence: { label: "顺序循环", icon: SND_MODE.sequence.icon }
  };
  var DISPLAY_MODE_NEXT = { fixed: "random", random: "sequence", sequence: "fixed" };
  function applyModeIcon(btn, mode) {
    if (!btn) return;
    if (!DISPLAY_MODE[mode]) mode = "fixed";
    const info = DISPLAY_MODE[mode];
    btn.dataset.mode = mode;
    btn.setAttribute("aria-label", "显示方式：" + info.label);
    btn.title = info.label;
    const ico = btn.querySelector(".snd-mode-ico");
    if (ico) ico.innerHTML = info.icon;
  }
  var pad22 = (n) => String(n).padStart(2, "0");
  function dayDiffOf(at) {
    const d = new Date(at);
    const now = /* @__PURE__ */ new Date();
    return Math.round(
      (new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime() - new Date(now.getFullYear(), now.getMonth(), now.getDate()).getTime()) / 864e5
    );
  }
  function nextSlotDayLabel(at) {
    const d = new Date(at);
    const dayDiff = dayDiffOf(at);
    const day = dayDiff === 0 ? "\u4ECA\u5929" : dayDiff === 1 ? "\u660E\u5929" : WEEKDAYS.find((w) => w.value === isoWeekday(d))?.label ?? "";
    return `${day} ${pad22(d.getHours())}:${pad22(d.getMinutes())}`;
  }
  function slotNextText(next) {
    if (!next) return "";
    if (dayDiffOf(next.at) > 0) return "\u4ECA\u65E5\u5DF2\u65E0\u63D0\u9192";
    const mins = Math.max(0, Math.round((next.at - Date.now()) / 6e4));
    const left = mins >= 60 ? `${Math.floor(mins / 60)} \u5C0F\u65F6 ${mins % 60} \u5206` : `${mins} \u5206`;
    return `\u4E0B\u6B21 ${nextSlotDayLabel(next.at)}\uFF08${left}\u540E\uFF09`;
  }
  function defaultSlotTime() {
    const d = /* @__PURE__ */ new Date();
    // 向上取到下一个 10 分钟边界：保证新建的提醒时间一定在未来，
    // 否则落在过去的默认时间会被「今日提醒时间线」过滤掉，看起来像"没刷新"。
    const total = Math.floor((d.getHours() * 60 + d.getMinutes()) / 10) * 10 + 10;
    return { hour: Math.floor(total / 60) % 24, minute: total % 60 };
  }
  function newSlot() {
    const { hour, minute } = defaultSlotTime();
    return { id: crypto.randomUUID(), hour, minute, repeat: "daily", days: [], enabled: true };
  }
  function updateTakeoutSummary() {
    const el = $("tk-summary");
    if (!el || !S.takeout) return;
    const t = S.takeout;
    const active = t.slots.filter((s) => s.enabled);
    if (!t.enabled) {
      el.textContent = "\u5DF2\u5173\u95ED";
      el.classList.remove("customized");
      return;
    }
    if (active.length === 0) {
      el.textContent = "\u56FA\u5B9A\u65F6\u95F4 \xB7 \u8FD8\u6CA1\u6709\u63D0\u9192\u65F6\u523B";
      el.classList.remove("customized");
      return;
    }
    const plannedAt = LAST_STATE?.fixedNext?.takeout;
    const cfgNext = nextSlotAt(t.slots);
    const next = plannedAt !== void 0 ? { at: plannedAt } : cfgNext && { at: cfgNext.at - t.leadMin * 6e4 };
    const lead = t.leadMin > 0 ? `\u63D0\u524D ${t.leadMin} \u5206\u901A\u77E5 \xB7 ` : "";
    el.textContent = `\u56FA\u5B9A\u65F6\u95F4 \xB7 ${active.length} \u6761 \xB7 ${lead}${slotNextText(next)}`;
    el.classList.add("customized");
  }
  function renderSlotRow(slot, opts) {
    const row = document.createElement("div");
    row.className = "tk-item" + (slot.enabled ? "" : " disabled");
    const on = document.createElement("input");
    on.type = "checkbox";
    on.checked = slot.enabled;
    on.title = "\u542F\u7528 / \u505C\u7528\u8FD9\u6761\u63D0\u9192";
    on.addEventListener("change", () => {
      slot.enabled = on.checked;
      row.classList.toggle("disabled", !on.checked);
      opts.onChange();
    });
    const time = document.createElement("div");
    time.className = "tk-time";
    const hh = document.createElement("input");
    hh.type = "number";
    hh.min = "0";
    hh.max = "23";
    hh.value = String(slot.hour);
    hh.title = "\u5C0F\u65F6\uFF080~23\uFF0C24 \u5C0F\u65F6\u5236\uFF09";
    const mm = document.createElement("input");
    mm.type = "number";
    mm.min = "0";
    mm.max = "59";
    mm.value = String(slot.minute);
    mm.title = "\u5206\u949F\uFF080~59\uFF09";
    const hhText = document.createElement("span");
    hhText.textContent = "\u65F6";
    const mmText = document.createElement("span");
    mmText.textContent = "\u5206";
    hh.addEventListener("change", () => {
      slot.hour = clampNum(hh, 0, 23, 12);
      hh.value = String(slot.hour);
      opts.onChange();
    });
    mm.addEventListener("change", () => {
      slot.minute = clampNum(mm, 0, 59, 0);
      mm.value = String(slot.minute);
      opts.onChange();
    });
    time.append(hh, hhText, mm, mmText);
    const repeat = document.createElement("select");
    repeat.className = "tk-repeat";
    for (const opt of [{ v: "daily", t: "\u6BCF\u5929" }, { v: "weekly", t: "\u6BCF\u5468" }]) {
      const o = document.createElement("option");
      o.value = opt.v;
      o.textContent = opt.t;
      repeat.appendChild(o);
    }
    repeat.value = slot.repeat;
    const days = document.createElement("div");
    days.className = "tk-days" + (slot.repeat === "weekly" ? "" : " hidden");
    const chips = [];
    for (const w of WEEKDAYS) {
      const lab = document.createElement("label");
      lab.className = "tk-day" + (slot.days.includes(w.value) ? " on" : "");
      lab.title = w.label;
      const box = document.createElement("input");
      box.type = "checkbox";
      box.checked = slot.days.includes(w.value);
      box.addEventListener("change", () => {
        slot.days = box.checked ? [.../* @__PURE__ */ new Set([...slot.days, w.value])].sort((a, b) => a - b) : slot.days.filter((v) => v !== w.value);
        lab.classList.toggle("on", box.checked);
        opts.onChange();
      });
      const txt = document.createElement("span");
      txt.textContent = w.short;
      lab.append(box, txt);
      days.appendChild(lab);
      chips.push({ value: w.value, box, label: lab });
    }
    repeat.addEventListener("change", () => {
      slot.repeat = repeat.value === "weekly" ? "weekly" : "daily";
      if (slot.repeat === "weekly" && slot.days.length === 0) {
        slot.days = [...DEFAULT_WEEKDAYS];
        for (const c of chips) {
          c.box.checked = slot.days.includes(c.value);
          c.label.classList.toggle("on", c.box.checked);
        }
      }
      if (slot.repeat === "daily") slot.days = [];
      days.classList.toggle("hidden", slot.repeat !== "weekly");
      opts.onChange();
    });
    const del = document.createElement("button");
    del.className = "btn mini danger";
    del.textContent = "\u2715";
    del.title = "\u5220\u9664\u8FD9\u6761\u63D0\u9192";
    del.addEventListener("click", () => {
      void confirmDialog({
        title: `\u5220\u9664${opts.what}\uFF1F`,
        desc: "\u5220\u9664\u540E\u7ACB\u5373\u751F\u6548\uFF0C\u65E0\u6CD5\u64A4\u9500\u3002",
        detail: describeTimeSlot(slot),
        okText: "\u5220\u9664"
      }).then((ok) => {
        if (ok) opts.onRemove(slot);
      });
    });
    row.append(on, time, repeat, days, del);
    return row;
  }
  function renderSlotList(listEl, slots, emptyText, opts) {
    if (!listEl) return;
    listEl.innerHTML = "";
    if (slots.length === 0) {
      const empty = document.createElement("div");
      empty.className = "tk-empty";
      empty.textContent = emptyText;
      listEl.appendChild(empty);
      return;
    }
    for (const slot of slots) listEl.appendChild(renderSlotRow(slot, opts));
  }
  function renderTakeoutList() {
    const t = S.takeout;
    renderSlotList($("tk-list"), t.slots, "\u8FD8\u6CA1\u6709\u5916\u5356\u63D0\u9192\u65F6\u95F4\uFF0C\u70B9\u6807\u9898\u884C\u53F3\u4FA7\u7684\u300C\uFF0B\u300D\u65B0\u589E\u4E00\u6761", {
      what: "\u8FD9\u6761\u5916\u5356\u63D0\u9192",
      onChange: () => {
        updateTakeoutSummary();
        autoSave();
        refreshStatusNow();
      },
      onRemove: (slot) => {
        t.slots = t.slots.filter((s) => s !== slot);
        renderTakeoutList();
        autoSave();
        refreshStatusNow();
      }
    });
    updateTakeoutSummary();
  }
  function shortBreakCard() {
    for (const c of document.querySelectorAll("#tab-timing > .card")) {
      const h = c.querySelector(":scope > .card-head h3");
      if (h && h.textContent === "\u77ED\u4F11\u606F") return c;
    }
    return null;
  }
  function customCardEl(id) {
    return document.querySelector(`#tab-timing > .card[data-cb="${id}"]`);
  }
  function buildCustomBreakCard(cb) {
    const src = shortBreakCard();
    if (!src) return null;
    const card = src.cloneNode(true);
    const rewrite = (el) => {
      if (el.id && (el.id === "short" || el.id.startsWith("short-"))) {
        el.id = cb.id + el.id.slice("short".length);
      }
      for (const attr of ["data-break", "data-kind"]) {
        if (el.getAttribute(attr) === "short") el.setAttribute(attr, cb.id);
      }
      for (const child of Array.from(el.children)) rewrite(child);
    };
    rewrite(card);
    card.dataset.cb = cb.id;
    card.classList.add("card-custom");
    const head = card.querySelector(":scope > .card-head");
    head.querySelector("h3").textContent = cb.name;
    const sum = head.querySelector(".adv-summary");
    sum.textContent = "\u2014";
    const actions = document.createElement("div");
    actions.className = "card-actions";
    sum.replaceWith(actions);
    actions.appendChild(sum);
    const del = mkBtn("\u5220\u9664", "\u5220\u9664\u8FD9\u6761\u81EA\u5B9A\u4E49\u63D0\u9192", () => void removeCustomBreak(cb.id));
    del.classList.add("cb-del");
    actions.appendChild(del);
    const pvBtn = card.querySelector(`#${cb.id}-preview`);
    if (pvBtn) {
      pvBtn.addEventListener("click", async () => {
        autoSave();
        await window.api.previewBreak(cb.id);
      });
    }
    card.querySelector(".tk-list").innerHTML = "";
    const msgList = card.querySelector(".msg-list");
    msgList.innerHTML = "";
    const trailingHint = msgList.nextElementSibling;
    if (trailingHint && trailingHint.tagName === "P") trailingHint.remove();
    const panel = card.querySelector(".msg-panel");
    panel.classList.remove("open");
    panel.querySelector(".msg-body").classList.add("hidden");
    const slotFold = card.querySelector(":scope > .slot-fixed");
    slotFold?.classList.remove("open");
    slotFold?.querySelector(".slot-body")?.classList.add("hidden");
    return card;
  }
  function setupCustomBreakCard(card, cb) {
    const id = cb.id;
    // 「新增」出的卡片插在 bindAutoSave 之后，全局绑定扫不到它：卡内全部
    // input/select 在这里显式补绑。漏绑的后果是触发方式/间隔/时长等改动不落盘、
    // 调度器不重算 ——「倒计时切固定时间且无时刻」后下次提醒与今日时间线
    // 仍显示按倒计时算出的旧时刻（bug 实测来源）。
    for (const el of card.querySelectorAll("input, select")) bindAutoSaveEl(el);
    $(`${id}-mode`).addEventListener("change", () => {
      const c = breakCfg(id);
      if (!c) return;
      c.mode = $(`${id}-mode`).value === "fixed" ? "fixed" : "countdown";
      applyBreakModeUI();
      updateBreakSummary(id);
    });
    $(`${id}-add`).addEventListener("click", async () => {
      const c = breakCfg(id);
      if (!c) return;
      const slot = newSlot();
      c.slots.push(slot);
      renderBreakList(id);
      await autoSave();
      refreshStatusNow();
      toast(`\u5DF2\u6DFB\u52A0 ${slotTimeText(slot)} \u7684${cb.name}`);
    });
    for (const suffix of ["enabled", "interval"]) {
      $(`${id}-${suffix}`).addEventListener("change", () => updateBreakSummary(id));
    }
    setupMsgPanel(card.querySelector(".msg-panel"));
  }
  function renderCustomCard(cb) {
    const addCard = $("cb-add-card");
    const card = buildCustomBreakCard(cb);
    if (!addCard || !card) return;
    customCardEl(cb.id)?.remove();
    addCard.before(card);
    setupCustomBreakCard(card, cb);
    fillBreakForm(cb.id);
    renderMsgList(cb.id);
  }
  function renderCustomCards() {
    for (const el of document.querySelectorAll("#tab-timing > .card[data-cb]")) el.remove();
    const addCard = $("cb-add-card");
    if (!addCard) return;
    for (const cb of S.customBreaks ?? []) {
      const card = buildCustomBreakCard(cb);
      if (!card) continue;
      addCard.before(card);
      setupCustomBreakCard(card, cb);
    }
  }
  // 已被占用的提醒名称：内置（短休息/长休息/外卖提醒）+ 已有自定义提醒名称；新增时不得与之重名。
  function reservedReminderNames() {
    const names = ["\u77ED\u4F11\u606F", "\u957F\u4F11\u606F", "\u5916\u5356\u63D0\u9192"];
    for (const cb of S.customBreaks ?? []) names.push(cb.name);
    return names;
  }
  async function addCustomBreak() {
    const input = await promptDialog({
      title: "\u65B0\u5EFA\u81EA\u5B9A\u4E49\u63D0\u9192",
      label: "\u63D0\u9192\u540D\u79F0\uFF1A",
      placeholder: "\u4F8B\u5982\uFF1A\u559D\u6C34\u63D0\u9192",
      okText: "\u521B\u5EFA"
    });
    if (input === null) return;
    const name = input.trim().slice(0, 24);
    if (!name) {
      toast("\u540D\u79F0\u4E0D\u80FD\u4E3A\u7A7A");
      return;
    }
    if (reservedReminderNames().includes(name)) {
      toast(`\u63D0\u9192\u540D\u79F0\u300C${name}\u300D\u5DF2\u5B58\u5728\uFF0C\u8BF7\u6362\u4E00\u4E2A`);
      return;
    }
    const cb = {
      id: `${CUSTOM_BREAK_PREFIX}${crypto.randomUUID()}`,
      name,
      config: defaultCustomBreakConfig(),
      // 提醒内容**默认为空**：不预置任何内置条目，用户自己加几条就是几条（默认固定显示）
      messages: { order: "fixed", fixedId: "", items: [] }
    };
    S.customBreaks = [...S.customBreaks ?? [], cb];
    renderCustomCard(cb);
    applyBreakModeUI();
    autoSave();
    refreshStatusNow();
    toast(`\u5DF2\u65B0\u5EFA\u81EA\u5B9A\u4E49\u63D0\u9192\u300C${name}\u300D`);
  }
  async function removeCustomBreak(id) {
    const cb = (S.customBreaks ?? []).find((c) => c.id === id);
    if (!cb) return;
    const ok = await confirmDialog({
      title: `\u5220\u9664\u81EA\u5B9A\u4E49\u63D0\u9192\u300C${cb.name}\u300D\uFF1F`,
      desc: "\u5220\u9664\u540E\u7ACB\u5373\u751F\u6548\uFF0C\u65E0\u6CD5\u64A4\u9500\uFF08\u5DF2\u7ECF\u8BB0\u8FC7\u7684\u4F11\u606F\u4ECD\u7559\u5728\u7EDF\u8BA1\u91CC\uFF09\u3002",
      detail: cb.config.mode === "fixed" ? `\u56FA\u5B9A\u65F6\u95F4 \xB7 ${cb.config.slots.length} \u6761\u65F6\u523B` : `\u5012\u8BA1\u65F6 \xB7 \u6BCF ${Math.round(cb.config.intervalMs / 6e4)} \u5206\u949F\u4E00\u6B21`,
      okText: "\u5220\u9664"
    });
    if (!ok) return;
    S.customBreaks = (S.customBreaks ?? []).filter((c) => c.id !== id);
    customCardEl(id)?.remove();
    applyBreakModeUI();
    autoSave();
    refreshStatusNow();
    toast(`\u5DF2\u5220\u9664\u81EA\u5B9A\u4E49\u63D0\u9192\u300C${cb.name}\u300D`);
  }
  function allBreakIds() {
    return ["short", "long", ...(S.customBreaks ?? []).map((c) => c.id)];
  }
  function breakCfg(id) {
    return breakConfigById(S, id);
  }
  function activeSlotCount(id) {
    return (breakCfg(id)?.slots ?? []).filter((s) => s.enabled).length;
  }
  function updateBreakSummary(id) {
    const el = $(`${id}-summary`);
    const c = breakCfg(id);
    if (!el || !c) return;
    if (!c.enabled) {
      el.textContent = "\u5DF2\u5173\u95ED";
      el.classList.remove("customized");
      return;
    }
    if (c.mode === "countdown") {
      el.textContent = `\u5012\u8BA1\u65F6 \xB7 \u6BCF ${Math.round(c.intervalMs / 6e4)} \u5206\u949F\u4E00\u6B21`;
      el.classList.add("customized");
      return;
    }
    const n = activeSlotCount(id);
    if (n === 0) {
      el.textContent = "\u56FA\u5B9A\u65F6\u95F4 \xB7 \u8FD8\u6CA1\u6709\u4F11\u606F\u65F6\u523B";
      el.classList.remove("customized");
      return;
    }
    const plannedAt = LAST_STATE?.fixedNext?.[id];
    const next = plannedAt !== void 0 ? { at: plannedAt } : nextSlotAt(c.slots);
    el.textContent = `\u56FA\u5B9A\u65F6\u95F4 \xB7 ${n} \u6761 \xB7 ${slotNextText(next)}`;
    el.classList.add("customized");
  }
  function renderBreakList(id) {
    const c = breakCfg(id);
    if (!c) return;
    renderSlotList($(`${id}-list`), c.slots, "\u8FD8\u6CA1\u6709\u63D0\u9192\u65F6\u95F4\uFF0C\u70B9\u6807\u9898\u884C\u53F3\u4FA7\u7684\u300C\uFF0B\u300D\u65B0\u589E\u4E00\u6761", {
      what: "\u8FD9\u6761\u4F11\u606F\u65F6\u523B",
      onChange: () => {
        updateBreakSummary(id);
        autoSave();
        refreshStatusNow();
      },
      onRemove: (slot) => {
        c.slots = c.slots.filter((s) => s !== slot);
        renderBreakList(id);
        autoSave();
        refreshStatusNow();
      }
    });
    updateBreakSummary(id);
  }
  function fillBreakForm(id) {
    const c = breakCfg(id);
    if (!c) return;
    $(`${id}-enabled`).checked = c.enabled;
    fillBreakModeSelect($(`${id}-mode`), c.mode);
    $(`${id}-interval`).value = String(Math.round(c.intervalMs / 6e4));
    $(`${id}-duration`).value = id === "long" ? String(Math.round(c.durationMs / 6e4)) : String(Math.round(c.durationMs / 1e3));
    $(`${id}-notify`).value = String(Math.round(c.notifyBeforeMs / 1e3));
    $(`${id}-postpone`).value = String(Math.round(c.postponeMs / 6e4));
    renderBreakList(id);
  }
  function fillBreakModeSelect(el, value) {
    el.innerHTML = "";
    for (const m of BREAK_MODES) {
      const o = document.createElement("option");
      o.value = m.value;
      o.textContent = m.label;
      o.title = m.hint;
      el.appendChild(o);
    }
    el.value = value;
  }
  function applyBreakModeUI() {
    for (const id of allBreakIds()) {
      const c = breakCfg(id);
      if (!c) continue;
      const fixed = c.mode === "fixed";
      for (const el of document.querySelectorAll(`.bk-countdown[data-break="${id}"]`)) {
        el.classList.toggle("hidden", fixed);
      }
      for (const el of document.querySelectorAll(`.slot-fixed[data-break="${id}"]`)) {
        el.classList.toggle("hidden", !fixed);
      }
    }
    const bothCountdown = S.short.mode === "countdown" && S.long.mode === "countdown";
    $("longEveryNRow")?.classList.toggle("hidden", !bothCountdown);
  }
  function fillForm() {
    $("appName").value = S.appName ?? "";
    $("tb-title").textContent = S.appName || "\u5403\u997C\u4EBA\u4E5F\u6562\u4F11\u606F\uFF1F";
    const themeVal = S.theme === "light" || S.theme === "dark" ? S.theme : "light";
    document.querySelector(`input[name=theme][value="${themeVal}"]`).checked = true;
    S.theme = themeVal;
    $("idlePauseMin").value = String(Math.max(1, Math.round(S.idlePauseMin)));
    $("longEveryNShort").value = String(S.longEveryNShort);
    $("autostart").checked = S.autostart;
    $("startMinimized").checked = S.startMinimized;
    $("dndPause").checked = S.dndPause;
    $("chk-update").checked = S.checkUpdateOnStart;
    $("appPause-enabled").checked = S.appPause.enabled;
    renderAppList();
    for (const id of allBreakIds()) fillBreakForm(id);
    applyBreakModeUI();
    const a = S.appearance;
    setColorChip(normalizeHex(a.color) ?? DEFAULT_ACCENT);
    $("ap-opacity").value = String(a.transparency);
    $("ap-opacity-val").textContent = a.transparency.toFixed(2);
    $("ap-widthPct").value = String(a.widthPct);
    $("ap-heightPct").value = String(a.heightPct);
    $("ap-fullscreen").checked = a.fullscreen;
    $("ap-alwaysOnTop").checked = a.alwaysOnTop;
    $("ap-allScreens").checked = S.allScreens;
    $("snd-enabled").checked = S.sound.enabled;
    $("snd-volume").value = String(S.sound.volume);
    $("snd-volume-val").textContent = `${Math.round(S.sound.volume * 100)}%`;
    setSoundModeUI(S.sound.mode ?? "single");
    renderSoundMusic();
    $("sup-enabled").checked = S.supervisor.enabled;
    applySelectionUI();
    ensureSupEditKey();
    fillSupervisorImageName();
    fillGeomControls(normalizeSupGeom(geomOf(currentGeomKey())));
    $("tk-enabled").checked = S.takeout.enabled;
    $("tk-lead").value = String(S.takeout.leadMin);
    $("tk-postpone").value = String(S.takeout.postponeMin);
    $("tk-redpacket").checked = S.takeout.redPacket;
    $("sc-postpone").value = S.shortcuts.postpone;
    $("sc-endEarly").value = S.shortcuts.endEarly;
    $("sc-pause").value = S.shortcuts.pause;
    $("sc-nextBreak").value = S.shortcuts.nextBreak;
  }
  function collectForm() {
    S.theme = document.querySelector("input[name=theme]:checked").value;
    S.appName = ($("appName").value.trim() || "\u5403\u997C\u4EBA\u4E5F\u6562\u4F11\u606F\uFF1F").slice(0, 30);
    $("tb-title").textContent = S.appName;
    S.idlePauseMin = clampNum($("idlePauseMin"), 1, 120, 5);
    S.longEveryNShort = clampNum($("longEveryNShort"), 1, 20, 2);
    S.autostart = $("autostart").checked;
    S.startMinimized = $("startMinimized").checked;
    S.dndPause = $("dndPause").checked;
    S.checkUpdateOnStart = $("chk-update").checked;
    S.appPause.enabled = $("appPause-enabled").checked;
    const readBreak = (id) => {
      const c = breakCfg(id);
      if (!c) return;
      c.enabled = $(`${id}-enabled`).checked;
      c.mode = $(`${id}-mode`).value === "fixed" ? "fixed" : "countdown";
      c.intervalMs = clampNum($(`${id}-interval`), 1, 480, 10) * 6e4;
      const bySec = id !== "long";
      const dv = clampNum($(`${id}-duration`), bySec ? 5 : 1, bySec ? 600 : 60, 20);
      c.durationMs = bySec ? dv * 1e3 : dv * 6e4;
      c.notifyBeforeMs = clampNum($(`${id}-notify`), 0, 300, 10) * 1e3;
      c.postponeMs = clampNum($(`${id}-postpone`), 0, 120, 2) * 6e4;
    };
    for (const id of allBreakIds()) readBreak(id);
    const a = S.appearance;
    a.transparency = parseFloat($("ap-opacity").value);
    a.widthPct = clampNum($("ap-widthPct"), 20, 100, 60);
    a.heightPct = clampNum($("ap-heightPct"), 20, 100, 60);
    a.fullscreen = $("ap-fullscreen").checked;
    a.alwaysOnTop = $("ap-alwaysOnTop").checked;
    S.allScreens = $("ap-allScreens").checked;
    S.sound.enabled = $("snd-enabled").checked;
    S.sound.volume = parseFloat($("snd-volume").value);
    S.sound.mode = $("snd-mode").dataset.mode || "single";
    S.supervisor.enabled = $("sup-enabled").checked;
    S.takeout.enabled = $("tk-enabled").checked;
    S.takeout.leadMin = clampNum($("tk-lead"), TAKEOUT_LEAD_LIMITS[0], TAKEOUT_LEAD_LIMITS[1], TAKEOUT_LEAD_DEFAULT);
    S.takeout.postponeMin = clampNum($("tk-postpone"), TAKEOUT_POSTPONE_LIMITS[0], TAKEOUT_POSTPONE_LIMITS[1], TAKEOUT_POSTPONE_DEFAULT);
    S.takeout.redPacket = $("tk-redpacket").checked;
    S.shortcuts.postpone = $("sc-postpone").value.trim();
    S.shortcuts.endEarly = $("sc-endEarly").value.trim();
    S.shortcuts.pause = $("sc-pause").value.trim();
    S.shortcuts.nextBreak = $("sc-nextBreak").value.trim();
  }
  function clampNum(el, min, max, fallback) {
    const v = parseInt(el.value, 10);
    if (Number.isNaN(v)) return fallback;
    return Math.min(max, Math.max(min, v));
  }
  var MSG_KINDS = ["short", "long", "takeout"];
  var MSG_KIND_LABEL = { short: "\u77ED\u4F11\u606F", long: "\u957F\u4F11\u606F", takeout: "\u5916\u5356\u63D0\u9192" };
  var msgBatchTarget = "short";
  var BUILTIN_MSGS = null;
  function msgCfgOf(target) {
    const custom = (S.customBreaks ?? []).find((c) => c.id === target);
    if (custom) return custom.messages;
    if (target !== "sup") {
      const kind = target;
      const cfg = S.reminderMessages[kind] ?? (S.reminderMessages[kind] = { order: "fixed", fixedId: "", items: [] });
      if (!cfg.seeded) {
        cfg.seeded = true;
        if (cfg.items.length === 0 && BUILTIN_MSGS) {
          cfg.items = BUILTIN_MSGS.kinds[kind].map((i) => ({ id: i.id, text: i.text, enabled: true }));
          if (cfg.order === "fixed" && !cfg.fixedId) cfg.fixedId = cfg.items[0]?.id ?? "";
        }
      }
      return cfg;
    }
    const key = currentGeomKey();
    if (!key) return null;
    const role = ROLES.find((r) => r.key === key);
    if (!role) return null;
    if (!role.messages || !Array.isArray(role.messages.items)) {
      role.messages = { order: "random", items: [] };
    }
    return role.messages;
  }
  function msgTargetLabel(target) {
    if (target === "sup") return currentRoleName();
    const custom = (S.customBreaks ?? []).find((c) => c.id === target);
    return custom ? custom.name : MSG_KIND_LABEL[target] ?? target;
  }
  function renderMsgList(target) {
    const panel = document.querySelector(`.msg-panel[data-kind="${target}"]`);
    if (!panel) return;
    const list = panel.querySelector(".msg-list");
    const cfg = msgCfgOf(target);
    const order = cfg?.order ?? "fixed";
    const fixedMode = order === "fixed";
    const modeBtn = panel.querySelector(".snd-mode-btn");
    if (modeBtn) applyModeIcon(modeBtn, order);
    list.innerHTML = "";
    if (!cfg) {
      const d = document.createElement("div");
      d.className = "msg-empty";
      d.textContent = "\u6B63\u5728\u8BC6\u522B\u5F53\u524D\u7ACB\u7ED8\uFF0C\u8BF7\u7A0D\u5019\u2026";
      list.appendChild(d);
      return;
    }
    if (cfg.items.length === 0) {
      const d = document.createElement("div");
      d.className = "msg-empty";
      d.textContent = target === "takeout" ? "\u6682\u65E0\u63D0\u9192\u5185\u5BB9\uFF1A\u5916\u5356\u63D0\u9192\u7A97\u53E3\u91CC\u5C06\u663E\u793A\u300C\u5916\u5356\u63D0\u9192\u300D" : target === "sup" ? "\u6682\u65E0\u63D0\u9192\u5185\u5BB9\uFF1A\u5347\u7EA7\u9636\u6BB5\u5C06\u6CBF\u7528\u7B2C\u4E00\u9636\u6BB5\u7684\u53F0\u8BCD" : "\u6682\u65E0\u63D0\u9192\u5185\u5BB9\uFF1A\u63D0\u9192\u7A97\u53E3\u91CC\u5C06\u663E\u793A\u8BE5\u63D0\u9192\u7684\u540D\u79F0";
      list.appendChild(d);
      return;
    }
    for (const item of cfg.items) list.appendChild(buildMsgRow(target, item, fixedMode));
  }
  function buildMsgRow(target, item, fixedMode) {
    const cfg = msgCfgOf(target);
    const row = document.createElement("div");
    row.className = "msg-item" + (!fixedMode && !item.enabled ? " disabled" : "");
    row.dataset.id = item.id;
    const handle = document.createElement("span");
    handle.className = "msg-handle";
    handle.title = "\u62D6\u52A8\u8C03\u6574\u987A\u5E8F";
    handle.textContent = "\u283F";
    let ctrl;
    if (fixedMode) {
      ctrl = document.createElement("input");
      ctrl.type = "radio";
      ctrl.name = `msg-single-${target}`;
      ctrl.checked = item.id === cfg?.fixedId;
      ctrl.title = "\u56FA\u5B9A\u663E\u793A\u8FD9\u4E00\u6761";
      ctrl.addEventListener("change", () => {
        if (!ctrl.checked || !cfg) return;
        cfg.fixedId = item.id;
        autoSave();
        if (target === "sup") void refreshSupervisorPreview(true);
      });
    } else {
      ctrl = document.createElement("input");
      ctrl.type = "checkbox";
      ctrl.checked = item.enabled;
      ctrl.title = "\u542F\u7528/\u7981\u7528\uFF1A\u662F\u5426\u53C2\u4E0E\u968F\u673A / \u987A\u5E8F\u53D6\u66F2";
      ctrl.addEventListener("change", () => {
        item.enabled = ctrl.checked;
        row.classList.toggle("disabled", !ctrl.checked);
        autoSave();
        if (target === "sup") void refreshSupervisorPreview(true);
      });
    }
    const txt = document.createElement("input");
    txt.type = "text";
    txt.value = item.text;
    txt.placeholder = "\u63D0\u9192\u5185\u5BB9\uFF08\u7EAF\u6587\u672C\uFF09";
    txt.addEventListener("change", () => {
      item.text = txt.value;
      autoSave();
    });
    const del = mkBtn("\u2715", "\u5220\u9664", () => {
      void confirmDialog({
        title: "\u5220\u9664\u8FD9\u6761\u63D0\u9192\u5185\u5BB9\uFF1F",
        desc: "\u5220\u9664\u540E\u7ACB\u5373\u751F\u6548\uFF0C\u65E0\u6CD5\u64A4\u9500\u3002",
        detail: item.text || "(\u7A7A)",
        okText: "\u5220\u9664"
      }).then((ok) => {
        if (!ok) return;
        const c = msgCfgOf(target);
        if (!c) return;
        c.items = c.items.filter((i) => i.id !== item.id);
        renderMsgList(target);
        autoSave();
        if (target === "sup") void refreshSupervisorPreview(true);
      });
    });
    del.classList.add("danger");
    row.append(handle, ctrl, txt, del);
    handle.addEventListener("mousedown", () => {
      row.draggable = true;
    });
    row.addEventListener("dragstart", (e) => {
      row.classList.add("dragging");
      if (e.dataTransfer) {
        e.dataTransfer.setData("text/plain", item.id);
        e.dataTransfer.effectAllowed = "move";
      }
    });
    row.addEventListener("dragend", () => {
      row.classList.remove("dragging");
      row.draggable = false;
    });
    row.addEventListener("dragover", (e) => {
      e.preventDefault();
      if (e.dataTransfer) e.dataTransfer.dropEffect = "move";
      row.classList.add("drop-target");
    });
    row.addEventListener("dragleave", () => row.classList.remove("drop-target"));
    row.addEventListener("drop", (e) => {
      e.preventDefault();
      row.classList.remove("drop-target");
      const dragId = e.dataTransfer?.getData("text/plain");
      if (!dragId || dragId === item.id) return;
      const cfg2 = msgCfgOf(target);
      if (!cfg2) return;
      const from = cfg2.items.findIndex((i) => i.id === dragId);
      const to = cfg2.items.findIndex((i) => i.id === item.id);
      if (from < 0 || to < 0) return;
      const [moved] = cfg2.items.splice(from, 1);
      cfg2.items.splice(to, 0, moved);
      renderMsgList(target);
      autoSave();
    });
    return row;
  }
  function renderAllMsgLists() {
    for (const kind of MSG_KINDS) renderMsgList(kind);
    for (const cb of S.customBreaks ?? []) renderMsgList(cb.id);
    renderMsgList("sup");
  }
  var msgPanelBound = /* @__PURE__ */ new WeakSet();
  function setupMsgPanel(panel) {
    if (msgPanelBound.has(panel)) return;
    msgPanelBound.add(panel);
    const target = panel.dataset.kind ?? "short";
    panel.querySelector(".msg-toggle").addEventListener("click", () => {
      panel.classList.toggle("open");
      panel.querySelector(".msg-body").classList.toggle("hidden");
    });
    const modeBtn = panel.querySelector(".snd-mode-btn");
    if (modeBtn) {
      modeBtn.addEventListener("click", () => {
        const cfg = msgCfgOf(target);
        if (!cfg) return;
        const cur = modeBtn.dataset.mode || cfg.order || "fixed";
        const next = DISPLAY_MODE_NEXT[cur] || "fixed";
        cfg.order = next;
        applyModeIcon(modeBtn, next);
        renderMsgList(target);
        autoSave();
      });
    }
    panel.querySelector(".msg-add").addEventListener("click", () => {
      msgBatchTarget = target;
      $("batch-title").textContent = `\u6279\u91CF\u65B0\u589E\u63D0\u9192\u5185\u5BB9\uFF08${msgTargetLabel(target)}\uFF09`;
      $("msg-batch-text").value = "";
      $("batch-modal").classList.remove("hidden");
      $("msg-batch-text").focus();
    });
  }
  function setupMsgPanels() {
    renderAllMsgLists();
    for (const panel of document.querySelectorAll(".msg-panel")) setupMsgPanel(panel);
  }
  function mkBtn(label, title, onClick) {
    const b = document.createElement("button");
    b.className = "btn mini";
    b.textContent = label;
    b.title = title;
    b.addEventListener("click", onClick);
    return b;
  }
  function acceleratorFromEvent(e) {
    if (["Control", "Shift", "Alt", "Meta"].includes(e.key)) return "";
    const parts = [];
    if (e.ctrlKey) parts.push("Ctrl");
    if (e.altKey) parts.push("Alt");
    if (e.shiftKey) parts.push("Shift");
    if (e.metaKey) parts.push("Super");
    const k = e.key.length === 1 ? e.key.toUpperCase() : e.key;
    parts.push(k);
    return parts.join("+");
  }
  function setupShortcutCapture() {
    for (const id of ["sc-postpone", "sc-endEarly", "sc-pause", "sc-nextBreak"]) {
      const input = $(id);
      input.addEventListener("keydown", (e) => {
        e.preventDefault();
        e.stopPropagation();
        if (e.key === "Escape") {
          input.value = "";
          input.blur();
          autoSave();
          return;
        }
        const acc = acceleratorFromEvent(e);
        if (acc) {
          input.value = acc;
          input.blur();
          autoSave();
        }
      });
    }
  }
  function fmtRemain(ms) {
    const s = Math.max(0, Math.ceil(ms / 1e3));
    if (s < 60) return `${s}\u79D2`;
    const m = Math.floor(s / 60);
    const r = s % 60;
    if (m < 60) return r > 0 ? `${m}\u5206${r}\u79D2` : `${m}\u5206`;
    const h = Math.floor(m / 60);
    const mm = m % 60;
    let out = `${h}\u5C0F\u65F6`;
    if (mm > 0) out += `${mm}\u5206`;
    if (r > 0) out += `${r}\u79D2`;
    return out;
  }
  var LAST_STATE = null;
  function renderStatus(st) {
    LAST_STATE = st;
    // 「暂停休息/恢复休息」互斥显示：暂停中只显示「恢复休息」，否则只显示「暂停休息」
    const qaPause = $("qa-pause");
    if (qaPause) qaPause.textContent = st.paused ? "\u6062\u590D\u4F11\u606F" : "\u6682\u505C\u4F11\u606F";
    // 「推迟休息」按钮：当前下次提醒的「可推迟」为 0 分钟时不显示 —— 推迟 0 分钟
    // 等于立即再弹一次，没有意义。提前提示窗 / 外卖窗口的「推迟」按钮同样按此显隐。
    const qaPostpone = $("qa-postpone");
    if (qaPostpone) {
      const cfg = st.nextType ? breakCfg(st.nextType) : null;
      qaPostpone.classList.toggle("hidden", !(cfg && cfg.postponeMs > 0));
    }
    const phaseText = st.phase === "break" ? "\u4F11\u606F\u4E2D" : st.phase === "pre" ? "\u5373\u5C06\u4F11\u606F" : "\u52AA\u529B\u5DE5\u4F5C\u4E2D";
    const phaseVal = st.paused ? `${phaseText}\uFF08\u5DF2\u6682\u505C${st.pauseReasons.length ? "\uFF1A" + st.pauseReasons.map(pauseReasonText).join("\u3001") : ""}\uFF09` : phaseText;
    const now = st.paused ? st.pausedAt : Date.now();
    const otherKey = (name) => name.endsWith("\u63D0\u9192") ? `\u4E0B\u6B21${name}` : `\u4E0B\u6B21${name}\u63D0\u9192`;
    const todayOnly = (at) => dayDiffOf(at) > 0 ? "\u4ECA\u65E5\u5DF2\u65E0\u63D0\u9192" : fmtRemain(Math.max(0, at - now));
    // 「下次提醒」取外卖/自定义里最近的一次（st.other 已合并两者）；
    // 仅当该最近提醒落在「今天」（at 在当天剩余时间）才显示，否则隐藏整格。
    const otherCell = st.other && dayDiffOf(st.other.at) === 0
      ? [otherKey(st.other.name), todayOnly(st.other.at)]
      : null;
    const cells = [["\u72B6\u6001", phaseVal]];
    if (st.phase === "break") {
      cells.push(["\u6B63\u5728\u4F11\u606F", fmtRemain(Math.max(0, st.breakEndsAt - now))]);
      if (otherCell) cells.push(["\u4E0B\u6B21\u63D0\u9192", "\u2014"]);
    } else {
      cells.push(
        st.rest ? [`\u4E0B\u6B21${st.rest.name}`, todayOnly(st.rest.at)] : ["\u4E0B\u6B21\u4F11\u606F\u63D0\u9192", "\u672A\u5B89\u6392"]
      );
      if (otherCell) cells.push(otherCell);
    }
    $("data-status").innerHTML = cells.map(([k, v]) => `<div class="status-cell"><div class="k">${k}</div><div class="v">${v}</div></div>`).join("");
    updateTakeoutSummary();
    for (const t of allBreakIds()) updateBreakSummary(t);
    renderTimeline(st);
  }
  var tlExpanded = localStorage.getItem("wr-tl-expanded") === "1";
  function setupCardFolds() {
    for (const card of document.querySelectorAll(".tab .card.foldable[id]")) {
      card.classList.toggle("closed", localStorage.getItem(`wr-fold-${card.id}`) === "1");
    }
    const onFoldClick = (e) => {
      const t = e.target;
      const head = t.closest(".card-head");
      const card = head?.closest(".card");
      if (!head || !card || card.id === "tl-card") return;
      if (!card.classList.contains("foldable")) return;
      if (t.closest("button") && !t.closest(".card-fold")) return;
      if (t instanceof HTMLInputElement) return;
      if (!card.id) return;
      const closed = card.classList.toggle("closed");
      localStorage.setItem(`wr-fold-${card.id}`, closed ? "1" : "0");
    };
    $("tab-timing").addEventListener("click", onFoldClick);
    $("tab-stats").addEventListener("click", onFoldClick);
  }
  document.addEventListener("click", (e) => {
    const btn = e.target.closest(".slot-head > .msg-toggle");
    if (!btn) return;
    const panel = btn.closest(".slot-fixed");
    if (!panel) return;
    const open = panel.classList.toggle("open");
    panel.querySelector(".slot-body")?.classList.toggle("hidden", !open);
  });
  var TL_COLOR_FIXED = { short: "#2f9e63", long: "#4c6ef5", takeout: "#e8890c" };
  var TL_COLOR_PALETTE = ["#c2255c", "#9c36b5", "#0c8599", "#e8590c", "#5f3dc4", "#2b8a3e", "#1971c2", "#d9480f"];
  function tlColorOf(it) {
    const fixed = TL_COLOR_FIXED[it.type];
    if (fixed) return fixed;
    let h = 0;
    for (let i = 0; i < it.key.length; i++) h = h * 31 + it.key.charCodeAt(i) >>> 0;
    return TL_COLOR_PALETTE[h % TL_COLOR_PALETTE.length];
  }
  function renderTimeline(st) {
    const card = document.getElementById("tl-card");
    const count = document.getElementById("tl-count");
    const body = document.getElementById("tl-body");
    const list = document.getElementById("tl-list");
    const empty = document.getElementById("tl-empty");
    if (!card || !count || !body || !list || !empty) return;
    const items = st?.timeline ?? [];
    count.textContent = items.length > 0 ? `\u5F85\u89E6\u53D1 ${items.length} \u6761` : "\u4ECA\u5929\u6CA1\u6709\u672A\u89E6\u53D1\u7684\u63D0\u9192";
    body.classList.toggle("hidden", !tlExpanded);
    card.classList.toggle("open", tlExpanded);
    empty.classList.toggle("hidden", items.length > 0);
    if (!tlExpanded) return;
    list.textContent = "";
    for (const it of items) {
      const row = document.createElement("div");
      row.className = "tl-row";
      row.style.setProperty("--tl-c", tlColorOf(it));
      const time = document.createElement("span");
      time.className = "tl-time";
      time.textContent = new Date(it.at).toTimeString().slice(0, 5);
      const dot = document.createElement("span");
      dot.className = "tl-dot";
      const name = document.createElement("span");
      name.className = "tl-name";
      name.textContent = it.name;
      const src = document.createElement("span");
      src.className = "tl-src";
      src.textContent = it.source === "countdown" ? "\u5012\u8BA1\u65F6" : "\u56FA\u5B9A\u65F6\u523B";
      row.append(time, dot, name, src);
      if (it.paused) {
        const badge = document.createElement("span");
        badge.className = "tl-badge pause";
        badge.textContent = "\u5DF2\u6682\u505C";
        row.append(badge);
      }
      list.append(row);
    }
  }
  function applyTheme() {
    document.documentElement.dataset.theme = S.theme;
  }
  function fmtDur(ms) {
    const s = Math.round(ms / 1e3);
    if (s < 60) return `${s} \u79D2`;
    const m = Math.floor(s / 60);
    if (m < 60) return `${m} \u5206 ${s % 60} \u79D2`;
    const h = Math.floor(m / 60);
    if (h < 24) return `${h} \u5C0F\u65F6 ${m % 60} \u5206`;
    const d = Math.floor(h / 24);
    return `${d} \u5929 ${h % 24} \u5C0F\u65F6`;
  }
  function statCells(d) {
    return [
      ["\u5DE5\u4F5C\u65F6\u957F", fmtDur(d.workMs)],
      ["\u4F11\u606F\u65F6\u957F", fmtDur(d.breakMs)],
      ["\u5B8C\u6210\u4F11\u606F", `\u77ED ${d.shortCount} \u6B21 \xB7 \u957F ${d.longCount} \u6B21 \xB7 \u81EA\u5B9A\u4E49 ${d.customCount} \u6B21`],
      ["\u63A8\u8FDF\u4F11\u606F", `${d.postponeCount} \u6B21 \xB7 \u7D2F\u8BA1 ${fmtDur(d.postponeMs)}`],
      ["\u8DF3\u8FC7\u4F11\u606F", `${d.skipCount} \u6B21 \xB7 \u8DF3\u8FC7 ${fmtDur(d.skipMs)}`],
      ["\u70B9\u5916\u5356", `${d.takeoutOrdered} \u6B21`],
      ["\u53D6\u6D88\u70B9\u5916\u5356", `${d.takeoutSkipped} \u6B21`],
      ["\u63A8\u8FDF\u70B9\u5916\u5356", `${d.takeoutPostponed} \u6B21`]
    ];
  }
  var STAT_TIPS = {
    \u5DE5\u4F5C\u65F6\u957F: "\u4E0D\u5728\u4F11\u606F\u4E2D\u3001\u4E5F\u6CA1\u6709\u957F\u65F6\u95F4\u65E0\u952E\u9F20\u8F93\u5165\u7684\u65F6\u95F4\u3002\u624B\u52A8\u6682\u505C\u63D0\u9192 / \u52FF\u6270 / \u6307\u5B9A\u5E94\u7528\u5360\u7528\u671F\u95F4\u4EBA\u4ECD\u5728\u7528\u7535\u8111\uFF0C\u540C\u6837\u8BA1\u5165\uFF1B\u8FDE\u7EED\u65E0\u8F93\u5165\u8D85\u8FC7\u300C\u7CFB\u7EDF\u7A7A\u95F2 N \u5206\u949F\u300D\u7684\u90A3\u6BB5\u4E0E\u7CFB\u7EDF\u7761\u7720\u6574\u6BB5\u90FD\u7B97\u300C\u79BB\u5F00\u300D\uFF0C\u4E0D\u8BA1\u5165\u5DE5\u4F5C\u65F6\u957F\u3002",
    \u4F11\u606F\u65F6\u957F: "\u6309\u5B9E\u9645\u4F11\u606F\u65F6\u95F4\u7B97\uFF0C\u63D0\u524D\u7ED3\u675F\u53EA\u7B97\u5B9E\u9645\u90E8\u5206\u3002",
    \u8DF3\u8FC7\u4F11\u606F: "\u8FD9\u91CC\u7684\u65F6\u95F4\u662F\u88AB\u8DF3\u8FC7\u90A3\u51E0\u6B21\u4F11\u606F\u7684\u65F6\u957F\u5408\u8BA1\u3002"
  };
  function statCellsHtml(d) {
    return statCells(d).map(([k, v]) => {
      const tip = STAT_TIPS[k];
      const head = tip ? `<div class="k has-tip" data-tip="${tip}">${k}<span class="k-tip" aria-hidden="true">?</span></div>` : `<div class="k">${k}</div>`;
      return `<div class="status-cell">${head}<div class="v">${v}</div></div>`;
    }).join("");
  }
  async function renderStats() {
    const data = await window.api.getStats();
    const t = data.today;
    $("stats-date").textContent = `\uFF08${t.date}\uFF09`;
    $("stats-today").innerHTML = statCellsHtml(t);
    void renderRestChart();
    void renderTakeoutChart();
    void renderCustomReportChart();
  }
  var RC_NS = "wr-rcv2";
  function rcLoadStr(key, oldKey = "") {
    const nk = `${RC_NS}-${key}`;
    let v = localStorage.getItem(nk);
    if (v === null && oldKey) {
      v = localStorage.getItem(oldKey);
      if (v !== null) localStorage.setItem(nk, v);
    }
    return v;
  }
  function rcSaveStr(key, v) {
    localStorage.setItem(`${RC_NS}-${key}`, v);
  }
  var RC_W = 760;
  var RC_H = 300;
  var RC_M = { top: 16, right: 46, bottom: 26, left: 44 };
  var RC_WINDOWS = { day: 30, week: 26, month: 13, quarter: 5, year: 2 };
  var RC_UNITS = [
    { u: "day", label: "\u65E5" },
    { u: "week", label: "\u5468" },
    { u: "month", label: "\u6708" },
    { u: "quarter", label: "\u5B63" },
    { u: "year", label: "\u5E74" }
  ];
  var RC_SERIES = [
    { id: "long", label: "\u957F\u4F11\u606F\u65F6\u95F4", kind: "bar", cls: "rc-long" },
    { id: "short", label: "\u77ED\u4F11\u606F\u65F6\u95F4", kind: "bar", cls: "rc-short" },
    { id: "rest", label: "\u4F11\u606F\u6B21\u6570", kind: "line", cls: "rc-l-rest" },
    { id: "skip", label: "\u8DF3\u8FC7\u6B21\u6570", kind: "line", cls: "rc-l-skip" },
    { id: "postpone", label: "\u63A8\u8FDF\u6B21\u6570", kind: "line", cls: "rc-l-post" }
  ];
  var rcUnit = rcLoadStr("unit", "wr-rc-unit") || "";
  if (!RC_WINDOWS[rcUnit]) rcUnit = "day";
  var rcShowEmpty = rcLoadStr("empty") === "1";
  function rcLegendOn() {
    const def = {
      long: true,
      short: true,
      rest: true,
      skip: true,
      postpone: true
    };
    try {
      const raw = rcLoadStr("on", "wr-rc-on");
      return { ...def, ...JSON.parse(raw || "{}") };
    } catch {
      return def;
    }
  }
  function fmtHms(ms) {
    const s = Math.round(ms / 1e3);
    const h = Math.floor(s / 3600);
    const m = Math.floor(s % 3600 / 60);
    if (h > 0) return `${h} \u65F6 ${m} \u5206 ${s % 60} \u79D2`;
    if (m > 0) return `${m} \u5206 ${s % 60} \u79D2`;
    return `${s % 60} \u79D2`;
  }
  function rcDayStr(d) {
    const p = (n) => String(n).padStart(2, "0");
    return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
  }
  function rcMonday(d) {
    const x = new Date(d.getFullYear(), d.getMonth(), d.getDate());
    const wd = x.getDay() === 0 ? 7 : x.getDay();
    x.setDate(x.getDate() - (wd - 1));
    return x;
  }
  function rcIsoWeek(d) {
    const t = new Date(d.getFullYear(), d.getMonth(), d.getDate());
    t.setDate(t.getDate() - (t.getDay() + 6) % 7 + 3);
    const firstThu = new Date(t.getFullYear(), 0, 4);
    firstThu.setDate(firstThu.getDate() - (firstThu.getDay() + 6) % 7 + 3);
    return 1 + Math.round((t.getTime() - firstThu.getTime()) / (7 * 24 * 3600 * 1e3));
  }
  function rcDefaultRange(unit) {
    const now = /* @__PURE__ */ new Date();
    const y = now.getFullYear();
    const m = now.getMonth();
    const ym = (d) => `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}`;
    if (unit === "day") {
      return { start: rcDayStr(new Date(y, m, now.getDate() - (RC_WINDOWS.day - 1))), end: rcDayStr(now) };
    }
    if (unit === "week") {
      const end = rcMonday(now);
      const start = rcMonday(now);
      start.setDate(start.getDate() - (RC_WINDOWS.week - 1) * 7);
      return { start: rcDayStr(start), end: rcDayStr(end) };
    }
    if (unit === "month") {
      return { start: ym(new Date(y, m - (RC_WINDOWS.month - 1), 1)), end: ym(now) };
    }
    if (unit === "quarter") {
      const q0 = Math.floor(m / 3);
      return {
        start: ym(new Date(y, (q0 - (RC_WINDOWS.quarter - 1)) * 3, 1)),
        end: ym(new Date(y, q0 * 3, 1))
      };
    }
    return { start: String(y - (RC_WINDOWS.year - 1)), end: String(y) };
  }
  var rcRangeCache = {};
  function rcGetRange(unit) {
    let r = rcRangeCache[unit];
    if (!r) {
      try {
        const raw = JSON.parse(rcLoadStr(`range-${unit}`, `wr-rc-range-${unit}`) || "null");
        if (raw && typeof raw.start === "string" && typeof raw.end === "string" && raw.start <= raw.end) {
          r = { start: raw.start, end: raw.end };
        }
      } catch {
      }
      if (!r) r = rcDefaultRange(unit);
      rcRangeCache[unit] = r;
    }
    return r;
  }
  function rcSetRange(unit, r) {
    const norm2 = r.start <= r.end ? { start: r.start, end: r.end } : { start: r.end, end: r.start };
    rcRangeCache[unit] = norm2;
    rcSaveStr(`range-${unit}`, JSON.stringify(norm2));
  }
  function rcResetRange(unit) {
    delete rcRangeCache[unit];
    localStorage.removeItem(`${RC_NS}-range-${unit}`);
  }
  function rcAllRange(unit) {
    const e = rcEarliest;
    if (!e) return null;
    const now = /* @__PURE__ */ new Date();
    const ym = (d) => `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}`;
    const [ey, em, ed] = e.split("-").map(Number);
    if (unit === "day") return { start: e, end: rcDayStr(now) };
    if (unit === "week") {
      return { start: rcDayStr(rcMonday(new Date(ey, em - 1, ed))), end: rcDayStr(rcMonday(now)) };
    }
    if (unit === "month") return { start: `${ey}-${String(em).padStart(2, "0")}`, end: ym(now) };
    if (unit === "quarter") {
      return {
        start: ym(new Date(ey, Math.floor((em - 1) / 3) * 3, 1)),
        end: ym(new Date(now.getFullYear(), Math.floor(now.getMonth() / 3) * 3, 1))
      };
    }
    return { start: String(ey), end: String(now.getFullYear()) };
  }
  function rcPrevRange(unit, range, len) {
    const pd = (s, addDays) => {
      const [y2, m, d] = s.split("-").map(Number);
      return rcDayStr(new Date(y2, m - 1, d + addDays));
    };
    if (unit === "day") return { start: pd(range.start, -len), end: pd(range.start, -1) };
    if (unit === "week") return { start: pd(range.start, -7 * len), end: pd(range.start, -7) };
    const back = (s, months) => {
      const [y2, m] = s.split("-").map(Number);
      const d = new Date(y2, m - 1 - months, 1);
      return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}`;
    };
    if (unit === "month") return { start: back(range.start, len), end: back(range.start, 1) };
    if (unit === "quarter") return { start: back(range.start, len * 3), end: back(range.start, 3) };
    const y = Number(range.start);
    return { start: String(y - len), end: String(y - 1) };
  }
  var rcEarliest;
  async function rcGetEarliest() {
    if (rcEarliest === void 0) {
      try {
        const data = await window.api.getHistory(null, null);
        const ds = data.days ?? [];
        rcEarliest = ds.length ? ds[0].date : null;
      } catch {
        rcEarliest = null;
      }
    }
    return rcEarliest;
  }
  function rcAnchorToDay(unit, a) {
    if (unit === "day" || unit === "week") return a;
    if (unit === "year") return `${a}-01-01`;
    return `${a}-01`;
  }
  function rcBuckets(unit, range) {
    const out = [];
    const mk = (key, label, title, start, end) => ({
      key,
      label,
      title,
      start,
      end,
      workMs: 0,
      awayMs: 0,
      longMs: 0,
      shortMs: 0,
      breakMs: 0,
      restCount: 0,
      skipCount: 0,
      postponeCount: 0,
      takeoutOrdered: 0,
      takeoutSkipped: 0,
      takeoutPostponed: 0,
      customCounts: {}
    });
    const parseDay = (s) => {
      const [yy, mm, dd] = s.split("-").map(Number);
      return new Date(yy, mm - 1, dd);
    };
    const cap = 800;
    if (unit === "day" || unit === "week") {
      const sd = parseDay(range.start);
      const stepDays = unit === "day" ? 1 : 7;
      const total = Math.round((parseDay(range.end).getTime() - sd.getTime()) / 864e5 / stepDays);
      for (let i = 0; i <= total && out.length < cap; i++) {
        const d = new Date(sd.getFullYear(), sd.getMonth(), sd.getDate() + i * stepDays);
        if (unit === "day") {
          const k = rcDayStr(d);
          out.push(mk(k, k.slice(5), k, k, k));
        } else {
          const sun = new Date(d.getFullYear(), d.getMonth(), d.getDate() + 6);
          const wk = rcIsoWeek(d);
          const s = rcDayStr(d);
          out.push(
            mk(
              `${d.getFullYear()}-W${wk}`,
              `${d.getFullYear()}W${wk}`,
              `${d.getFullYear()} \u5E74\u7B2C ${wk} \u5468\uFF08${s.slice(5)} ~ ${rcDayStr(sun).slice(5)}\uFF09`,
              s,
              rcDayStr(sun)
            )
          );
        }
      }
    } else if (unit === "month" || unit === "quarter") {
      const stepM = unit === "month" ? 1 : 3;
      const [sy, sm] = range.start.split("-").map(Number);
      const [ey, em] = range.end.split("-").map(Number);
      const totalM = (ey * 12 + em - 1 - (sy * 12 + sm - 1)) / stepM;
      for (let i = 0; i <= totalM && out.length < cap; i++) {
        const d = new Date(sy, sm - 1 + i * stepM, 1);
        const e = new Date(d.getFullYear(), d.getMonth() + stepM, 0);
        const s = rcDayStr(d);
        if (unit === "month") {
          out.push(
            mk(
              `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}`,
              `${String(d.getFullYear()).slice(2)}/${d.getMonth() + 1}`,
              `${d.getFullYear()} \u5E74 ${d.getMonth() + 1} \u6708`,
              s,
              rcDayStr(e)
            )
          );
        } else {
          const q = Math.floor(d.getMonth() / 3) + 1;
          out.push(
            mk(
              `${d.getFullYear()}-Q${q}`,
              `${String(d.getFullYear()).slice(2)}Q${q}`,
              `${d.getFullYear()} \u5E74\u7B2C ${q} \u5B63\u5EA6`,
              s,
              rcDayStr(e)
            )
          );
        }
      }
    } else {
      for (let yy = Number(range.start); yy <= Number(range.end) && out.length < cap; yy++) {
        out.push(mk(String(yy), String(yy), `${yy} \u5E74`, `${yy}-01-01`, `${yy}-12-31`));
      }
    }
    return out;
  }
  var rcSeq = 0;
  var rcLastDays = [];
  var rcPrevOf = null;
  function rcFillBuckets(buckets, days) {
    for (const d of days) {
      const b = buckets.find((x) => d.date >= x.start && d.date <= x.end);
      if (!b) continue;
      b.workMs += d.workMs;
      b.longMs += d.longMs;
      b.shortMs += d.shortMs;
      b.breakMs += d.breakMs;
      b.awayMs += d.awayMs ?? 0;
      b.restCount += d.shortCount + d.longCount + d.customCount;
      b.skipCount += d.skipCount;
      b.postponeCount += d.postponeCount;
      b.takeoutOrdered += d.takeoutOrdered;
      b.takeoutSkipped += d.takeoutSkipped;
      b.takeoutPostponed += d.takeoutPostponed;
      for (const [id, v] of Object.entries(d.customCounts ?? {})) {
        b.customCounts[id] = (b.customCounts[id] ?? 0) + v;
      }
    }
  }
  function rcEmptyBucket(b) {
    return b.workMs <= 0 && b.breakMs <= 0 && b.restCount <= 0 && b.skipCount <= 0 && b.postponeCount <= 0;
  }
  function rcRenderBadges(shown, unit, prev) {
    const box = $("rc-badges");
    if (!box) return;
    const work = shown.reduce((s, b) => s + b.workMs, 0);
    const brk = shown.reduce((s, b) => s + b.breakMs, 0);
    const avg = shown.length ? brk / shown.length : 0;
    const top = shown.reduce((m, b) => !m || b.breakMs > m.breakMs ? b : m, null);
    const unitLabel = { day: "\u65E5\u5747", week: "\u5468\u5747", month: "\u6708\u5747", quarter: "\u5B63\u5747", year: "\u5E74\u5747" }[unit];
    const delta = (cur, old) => {
      if (old <= 0) return "";
      const pct = Math.round((cur - old) / old * 100);
      return pct === 0 ? "\u8F83\u4E0A\u671F\u6301\u5E73" : `\u8F83\u4E0A\u671F ${pct > 0 ? "+" : "\u2212"}${Math.abs(pct)}%`;
    };
    const badge = (k, v, sub) => `<div class="rc-badge"><span class="k">${k}</span><b>${v}</b>${sub ? `<i>${sub}</i>` : ""}</div>`;
    box.innerHTML = badge("\u5DE5\u4F5C\u65F6\u95F4", fmtDur(work), delta(work, prev.work)) + badge("\u4F11\u606F\u65F6\u95F4", fmtDur(brk), delta(brk, prev.brk)) + badge(`${unitLabel}\u4F11\u606F`, shown.length ? fmtDur(avg) : "\u2014", `${shown.length} \u4E2A\u5468\u671F`) + badge("\u6700\u957F\u4F11\u606F", top ? fmtDur(top.breakMs) : "\u2014", top ? top.title : "");
  }
  async function renderRestChart(animate = false) {
    const seq = ++rcSeq;
    const range = rcGetRange(rcUnit);
    const buckets = rcBuckets(rcUnit, range);
    const prevBuckets = rcBuckets(rcUnit, rcPrevRange(rcUnit, range, buckets.length));
    const anchor = rcAnchorToDay(rcUnit, prevBuckets[0]?.start ?? range.start);
    const data = await window.api.getHistory(anchor, null);
    if (seq !== rcSeq) return;
    rcLastDays = data.days ?? [];
    rcFillBuckets(buckets, rcLastDays);
    rcFillBuckets(prevBuckets, rcLastDays);
    const shown = rcShowEmpty ? buckets : buckets.filter((b) => !rcEmptyBucket(b));
    const fullIdx = new Map(buckets.map((b, i) => [b.key, i]));
    rcPrevOf = (b) => {
      const i = fullIdx.get(b.key);
      return i !== void 0 && i > 0 ? buckets[i - 1] : null;
    };
    rcRenderBadges(shown, rcUnit, {
      work: prevBuckets.reduce((s, b) => s + b.workMs, 0),
      brk: prevBuckets.reduce((s, b) => s + b.breakMs, 0)
    });
    rcDraw(shown, animate);
    rcRenderTable(buckets.filter((b) => !rcEmptyBucket(b)));
  }
  var RC_SORT_GET = {
    period: (b) => b.start,
    work: (b) => b.workMs,
    break: (b) => b.breakMs,
    long: (b) => b.longMs,
    short: (b) => b.shortMs,
    away: (b) => b.awayMs,
    rest: (b) => b.restCount,
    skip: (b) => b.skipCount,
    post: (b) => b.postponeCount
  };
  var RC_TABLE_COLS = [
    { key: "period", label: "\u5468\u671F" },
    { key: "work", label: "\u5DE5\u4F5C\u65F6\u95F4" },
    { key: "break", label: "\u4F11\u606F\u603B\u65F6\u95F4" },
    { key: "long", label: "\u957F\u4F11\u606F" },
    { key: "short", label: "\u77ED\u4F11\u606F" },
    { key: "away", label: "\u79BB\u5F00\u65F6\u95F4" },
    { key: "rest", label: "\u4F11\u606F\u6B21\u6570" },
    { key: "skip", label: "\u8DF3\u8FC7\u6B21\u6570" },
    { key: "post", label: "\u63A8\u8FDF\u6B21\u6570" }
  ];
  var rcTableSort = { key: "period", dir: "asc" };
  var rcTableData = [];
  function rcRenderTable(rows) {
    const t = $("rc-table");
    if (!t) return;
    rcTableData = rows;
    const head = "<tr>" + RC_TABLE_COLS.map((c) => {
      const arrow = c.key === rcTableSort.key ? rcTableSort.dir === "asc" ? " \u25B2" : " \u25BC" : "";
      const num = c.key === "period" ? "" : " num";
      return `<th class="sortable${num}" data-key="${c.key}" title="\u70B9\u6309\u6392\u5E8F">${c.label}${arrow}</th>`;
    }).join("") + "</tr>";
    const dir = rcTableSort.dir === "asc" ? 1 : -1;
    const get = RC_SORT_GET[rcTableSort.key];
    const sorted = [...rows].sort((a, b) => {
      const va = get(a);
      const vb = get(b);
      return (va < vb ? -1 : va > vb ? 1 : 0) * dir;
    });
    const cells = (b) => `<td>${b.title}</td><td class="num rcv-work" data-v="${b.workMs}">${fmtDur(b.workMs)}</td><td class="num" data-v="${b.breakMs}">${fmtDur(b.breakMs)}</td><td class="num rcv-long" data-v="${b.longMs}">${fmtDur(b.longMs)}</td><td class="num rcv-short" data-v="${b.shortMs}">${fmtDur(b.shortMs)}</td><td class="num" data-v="${b.awayMs}">${fmtDur(b.awayMs)}</td><td class="num rcv-rest" data-v="${b.restCount}">${b.restCount}</td><td class="num rcv-skip" data-v="${b.skipCount}">${b.skipCount}</td><td class="num rcv-post" data-v="${b.postponeCount}">${b.postponeCount}</td>`;
    const body = rows.length ? sorted.map((b) => `<tr>${cells(b)}</tr>`).join("") : '<tr><td colspan="9" class="rc-empty">\u6682\u65E0\u6570\u636E</td></tr>';
    const sum = (f) => rows.reduce((s, b) => s + f(b), 0);
    const total = rows.length ? `<tr class="total"><td>\u5408\u8BA1</td><td class="num rcv-work">${fmtDur(sum((b) => b.workMs))}</td><td class="num">${fmtDur(sum((b) => b.breakMs))}</td><td class="num rcv-long">${fmtDur(sum((b) => b.longMs))}</td><td class="num rcv-short">${fmtDur(sum((b) => b.shortMs))}</td><td class="num">${fmtDur(sum((b) => b.awayMs))}</td><td class="num rcv-rest">${sum((b) => b.restCount)}</td><td class="num rcv-skip">${sum((b) => b.skipCount)}</td><td class="num rcv-post">${sum((b) => b.postponeCount)}</td></tr>` : "";
    t.innerHTML = head + body + total;
  }
  var rcTip = null;
  function rcHideTip() {
    if (rcTip) rcTip.style.display = "none";
  }
  function rcShowTip(b, x, y) {
    if (!rcTip) {
      rcTip = document.createElement("div");
      rcTip.className = "rc-tip";
      document.body.append(rcTip);
    }
    let env = "";
    const prev = rcPrevOf?.(b) ?? null;
    if (prev) {
      const pct = (cur, old) => {
        if (old <= 0) return cur > 0 ? "\u2014" : "";
        const p = Math.round((cur - old) / old * 100);
        return p === 0 ? "\u6301\u5E73" : `${p > 0 ? "+" : "\u2212"}${Math.abs(p)}%`;
      };
      const bp = pct(b.breakMs, prev.breakMs);
      const wp = pct(b.workMs, prev.workMs);
      if (bp || wp) env = `<span class="rc-tip-d">\u8F83\u4E0A\u4E00\u671F\u3000\u4F11\u606F ${bp || "\u2014"} \xB7 \u5DE5\u4F5C ${wp || "\u2014"}</span>`;
    }
    rcTip.innerHTML = `<b>${b.title}</b><span class="rcv-work">\u5DE5\u4F5C\u65F6\u95F4 ${fmtHms(b.workMs)}</span><span>\u4F11\u606F\u603B\u65F6\u95F4 ${fmtHms(b.breakMs)}</span><span class="rcv-long">\u957F\u4F11\u606F\u65F6\u95F4 ${fmtHms(b.longMs)}</span><span class="rcv-short">\u77ED\u4F11\u606F\u65F6\u95F4 ${fmtHms(b.shortMs)}</span><span>\u79BB\u5F00\u65F6\u95F4 ${fmtHms(b.awayMs)}</span><span class="rcv-rest">\u4F11\u606F ${b.restCount} \u6B21</span><span class="rcv-skip">\u8DF3\u8FC7 ${b.skipCount} \u6B21</span><span class="rcv-post">\u63A8\u8FDF ${b.postponeCount} \u6B21</span>` + env;
    rcTip.style.display = "block";
    const r = rcTip.getBoundingClientRect();
    rcTip.style.left = `${Math.max(8, Math.min(window.innerWidth - r.width - 8, x + 14))}px`;
    rcTip.style.top = `${Math.max(8, Math.min(window.innerHeight - r.height - 8, y - r.height - 12))}px`;
  }
  function rcSmoothPath(pts) {
    if (pts.length < 2) return "";
    let d = `M ${pts[0].x.toFixed(1)} ${pts[0].y.toFixed(1)}`;
    for (let i = 1; i < pts.length; i++) {
      const p0 = pts[i - 2] ?? pts[i - 1];
      const p1 = pts[i - 1];
      const p2 = pts[i];
      const p3 = pts[i + 1] ?? p2;
      d += ` C ${(p1.x + (p2.x - p0.x) / 6).toFixed(1)} ${(p1.y + (p2.y - p0.y) / 6).toFixed(1)}, ${(p2.x - (p3.x - p1.x) / 6).toFixed(1)} ${(p2.y - (p3.y - p1.y) / 6).toFixed(1)}, ${p2.x.toFixed(1)} ${p2.y.toFixed(1)}`;
    }
    return d;
  }
  function rcProbeAxis(dataMax) {
    const UNITS = [10, 20, 50, 100, 200, 500, 1e3, 2e3, 5e3, 1e4, 2e4, 5e4, 1e5];
    let max = 10;
    for (const u of UNITS) {
      max = Math.ceil(Math.max(0, dataMax) / u) * u;
      if (max <= dataMax) max += u;
      if (max / u <= 8) break;
    }
    const STEPS = [1, 2, 5, 10, 20, 50, 100, 200, 500, 1e3, 2e3, 5e3, 1e4];
    let step = 1;
    for (const s of STEPS) {
      if (max % s === 0 && max / s >= 3 && max / s <= 8) step = s;
    }
    return { max, step };
  }
  var rcZoom = null;
  var rcDrag = null;
  var rcDragMoved = false;
  function rcDraw(buckets, animate = false) {
    const box = $("rc-plot");
    const on = rcLegendOn();
    const n = buckets.length;
    if (rcZoom && (rcZoom.total !== n || rcZoom.o >= n)) rcZoom = null;
    const zo = rcZoom ? Math.min(rcZoom.o, Math.max(0, n - rcZoom.n)) : 0;
    const zv = rcZoom ? Math.min(rcZoom.n, n - zo) : n;
    const view = zv === n ? buckets : buckets.slice(zo, zo + zv);
    const iw = RC_W - RC_M.left - RC_M.right;
    const ih = RC_H - RC_M.top - RC_M.bottom;
    const barParts = RC_SERIES.filter((s) => s.kind === "bar" && on[s.id]);
    const lineParts = RC_SERIES.filter((s) => s.kind === "line" && on[s.id]);
    const maxRestMin = Math.ceil(
      buckets.reduce((mv, b) => Math.max(mv, b.longMs + b.shortMs), 0) / 6e4
    );
    const maxCnt = buckets.reduce(
      (mv, b) => Math.max(mv, b.restCount, b.skipCount, b.postponeCount),
      0
    );
    const axC = rcProbeAxis(maxCnt);
    const axM = rcProbeAxis(maxRestMin);
    const yMs = (v) => RC_M.top + ih * (1 - v / (axM.max * 6e4));
    const yC = (v) => RC_M.top + ih * (1 - v / axC.max);
    const step = iw / Math.max(1, zv);
    const bw = Math.min(34, Math.max(3, step * 0.6));
    const showDots = zv <= 60;
    const showPeaks = zv <= 40;
    const grad = (id, color, o0, o1) => `<linearGradient id="${id}" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="${color}" stop-opacity="${o0}"/><stop offset="1" stop-color="${color}" stop-opacity="${o1}"/></linearGradient>`;
    const defs = `<defs>` + grad("rc-g-long", "#9a6bff", 1, 0.72) + grad("rc-g-short", "#34a46f", 1, 0.72) + grad("rc-a-rest", "#2fa3b5", 0.2, 0) + grad("rc-a-skip", "#e05555", 0.2, 0) + grad("rc-a-post", "#e0a23f", 0.2, 0) + `</defs>`;
    let g = "";
    g += `<line class="rc-axis" x1="${RC_M.left}" y1="${RC_M.top}" x2="${RC_M.left}" y2="${(RC_M.top + ih).toFixed(1)}"/><line class="rc-axis" x1="${RC_W - RC_M.right}" y1="${RC_M.top}" x2="${RC_W - RC_M.right}" y2="${(RC_M.top + ih).toFixed(1)}"/>`;
    for (let v = 0; v <= axC.max; v += axC.step) {
      const yy = yC(v).toFixed(1);
      g += `<line class="grid" x1="${RC_M.left}" y1="${yy}" x2="${RC_W - RC_M.right}" y2="${yy}"/><text class="tick" x="${RC_M.left - 6}" y="${+yy + 4}" text-anchor="end">${v}</text>`;
    }
    for (let v = 0; v <= axM.max; v += axM.step) {
      g += `<text class="tick" x="${RC_W - RC_M.right + 6}" y="${(yMs(v * 6e4) + 4).toFixed(1)}">${v}</text>`;
    }
    g += `<text class="tick rc-cap" x="${RC_M.left - 6}" y="10" text-anchor="end">\u6B21\u6570</text><text class="tick rc-cap" x="${RC_W - RC_M.right + 6}" y="10">\u5206\u949F</text>`;
    const withData = buckets.filter((b) => !rcEmptyBucket(b));
    if (withData.length) {
      const avgMs = withData.reduce((s, b) => s + b.breakMs, 0) / withData.length;
      const avgMin = avgMs / 6e4;
      if (avgMin > 0 && avgMin < axM.max) {
        const yy = yMs(avgMs).toFixed(1);
        g += `<line class="rc-avgline" x1="${RC_M.left}" y1="${yy}" x2="${RC_W - RC_M.right}" y2="${yy}"/><text class="rc-avglabel" x="${RC_W - RC_M.right - 4}" y="${+yy - 4}" text-anchor="end">\u5E73\u5747 ${fmtDur(avgMs)}</text>`;
      }
    }
    const barTop = (x, y, w, h, gid) => {
      const r = Math.min(3, w / 2, h);
      if (r < 0.5) {
        return `<rect class="bar" x="${x.toFixed(1)}" y="${y.toFixed(1)}" width="${w.toFixed(1)}" height="${h.toFixed(1)}" style="fill:url(#${gid})"/>`;
      }
      return `<path class="bar" d="M ${x.toFixed(1)} ${(y + r).toFixed(1)} Q ${x.toFixed(1)} ${y.toFixed(1)} ${(x + r).toFixed(1)} ${y.toFixed(1)} L ${(x + w - r).toFixed(1)} ${y.toFixed(1)} Q ${(x + w).toFixed(1)} ${y.toFixed(1)} ${(x + w).toFixed(1)} ${(y + r).toFixed(1)} L ${(x + w).toFixed(1)} ${(y + h).toFixed(1)} L ${x.toFixed(1)} ${(y + h).toFixed(1)} Z" style="fill:url(#${gid})"/>`;
    };
    view.forEach((b, i) => {
      if (rcEmptyBucket(b)) return;
      const cx = RC_M.left + step * i + step / 2;
      let acc = 0;
      const vis = barParts.map((s) => ({ s, v: s.id === "long" ? b.longMs : b.shortMs })).filter((x) => x.v > 0);
      vis.forEach(({ s, v }, vi) => {
        const yTop = yMs(acc + v);
        const h = yMs(acc) - yTop;
        acc += v;
        const x = cx - bw / 2;
        if (vi === vis.length - 1) g += barTop(x, yTop, bw, h, `rc-g-${s.id}`);
        else {
          g += `<rect class="bar" x="${x.toFixed(1)}" y="${yTop.toFixed(1)}" width="${bw.toFixed(1)}" height="${h.toFixed(1)}" style="fill:url(#rc-g-${s.id})"/>`;
        }
      });
    });
    for (const s of lineParts) {
      const val = (b) => s.id === "rest" ? b.restCount : s.id === "skip" ? b.skipCount : b.postponeCount;
      const segs = [];
      let pts = [];
      view.forEach((b, i) => {
        const p = { x: RC_M.left + step * i + step / 2, y: yC(val(b)), b };
        if (rcEmptyBucket(b)) {
          if (pts.length) segs.push(pts);
          pts = [];
        } else pts.push(p);
      });
      if (pts.length) segs.push(pts);
      const base = yC(0).toFixed(1);
      for (const seg of segs) {
        const d = rcSmoothPath(seg);
        if (d) {
          if (seg.length >= 2) {
            const first = seg[0];
            const last = seg[seg.length - 1];
            g += `<path class="rc-area ${s.cls}" d="${d} L ${last.x.toFixed(1)} ${base} L ${first.x.toFixed(1)} ${base} Z" style="fill:url(#rc-a-${s.id})"/>`;
          }
          g += `<path class="rc-line ${s.cls}" d="${d}"${animate ? ' pathLength="1"' : ""}/>`;
          if (showPeaks && seg.length >= 2) {
            const pk = seg.reduce((m, p) => val(p.b) > val(m.b) ? p : m, seg[0]);
            if (val(pk.b) > 0) {
              g += `<text class="rc-peak ${s.cls}" x="${pk.x.toFixed(1)}" y="${(pk.y - 7).toFixed(1)}" text-anchor="middle">${val(pk.b)}</text>`;
            }
          }
        }
        if (showDots) {
          seg.forEach((p) => {
            g += `<circle class="rc-dot ${s.cls}" cx="${p.x.toFixed(1)}" cy="${p.y.toFixed(1)}" r="2.6"/>`;
          });
        }
      }
    }
    const labelEvery = zv <= 10 ? 1 : Math.ceil(zv / 10);
    view.forEach((b, i) => {
      if (i % labelEvery === 0) {
        g += `<text class="tick" x="${(RC_M.left + step * i + step / 2).toFixed(1)}" y="${RC_H - 8}" text-anchor="middle">${b.label}</text>`;
      }
    });
    if (view.length === 0 || withData.length === 0) {
      g += `<text class="tick" x="${(RC_M.left + iw / 2).toFixed(1)}" y="${(RC_M.top + ih / 2).toFixed(1)}" text-anchor="middle">\u6682\u65E0\u6570\u636E</text>`;
    }
    g += `<line class="rc-cursor" x1="0" y1="${RC_M.top}" x2="0" y2="${(RC_M.top + ih).toFixed(1)}" style="display:none"/>`;
    g += `<rect class="rc-hit" x="${RC_M.left}" y="${RC_M.top}" width="${iw.toFixed(1)}" height="${ih.toFixed(1)}"/>`;
    box.innerHTML = `<svg viewBox="0 0 ${RC_W} ${RC_H}" role="img" tabindex="0" aria-label="\u4F11\u606F\u62A5\u8868\u56FE"${animate ? ' class="anim"' : ""}>${defs}${g}</svg>`;
    const svg = box.querySelector("svg");
    if (!svg) return;
    svg.style.cursor = rcZoom ? "grab" : "";
    const cursor = svg.querySelector(".rc-cursor");
    const centers = view.map((b, i) => ({ b, cx: RC_M.left + step * i + step / 2 }));
    let kbX = null;
    const nearest = (xSvg) => {
      if (!centers.length) return null;
      let best = centers[0];
      for (const c of centers) if (Math.abs(c.cx - xSvg) < Math.abs(best.cx - xSvg)) best = c;
      return xSvg < RC_M.left - 6 || xSvg > RC_W - RC_M.right + 6 ? null : best;
    };
    const placeCursor = (c, clientY) => {
      if (!cursor) return;
      const rect = svg.getBoundingClientRect();
      cursor.setAttribute("x1", c.cx.toFixed(1));
      cursor.setAttribute("x2", c.cx.toFixed(1));
      cursor.style.display = "";
      rcShowTip(c.b, rect.left + c.cx / RC_W * rect.width, clientY);
    };
    const xOf = (e) => {
      const rect = svg.getBoundingClientRect();
      return (e.clientX - rect.left) / rect.width * RC_W;
    };
    svg.addEventListener("mousemove", (e) => {
      const best = nearest(xOf(e));
      if (!best || !cursor) {
        if (cursor) cursor.style.display = "none";
        rcHideTip();
        return;
      }
      kbX = best.cx;
      placeCursor(best, e.clientY);
    });
    svg.addEventListener("mouseleave", () => {
      if (cursor) cursor.style.display = "none";
      rcHideTip();
      rcDrag = null;
    });
    svg.addEventListener("wheel", (e) => {
      if (!n) return;
      e.preventDefault();
      const horiz = Math.abs(e.deltaX) > Math.abs(e.deltaY) || e.shiftKey;
      if (horiz && rcZoom) {
        const per = iw / rcZoom.n;
        const o = Math.round(rcZoom.o + (e.deltaX || e.deltaY) / per);
        rcZoom = { ...rcZoom, o: Math.max(0, Math.min(n - rcZoom.n, o)) };
        rcDraw(buckets);
        return;
      }
      const rect = svg.getBoundingClientRect();
      const xSvg = (e.clientX - rect.left) / rect.width * RC_W;
      const anchor = Math.max(0, Math.min(n - 1, Math.floor((xSvg - RC_M.left) / (iw / n))));
      const cur = rcZoom ?? { o: 0, n, total: n };
      let wn = Math.round(cur.n * (e.deltaY < 0 ? 1 / 1.35 : 1.35));
      wn = Math.min(n, Math.max(3, wn));
      if (wn >= n) {
        rcZoom = null;
      } else {
        const o = Math.round(anchor - (anchor - cur.o) * wn / cur.n);
        rcZoom = { o: Math.max(0, Math.min(n - wn, o)), n: wn, total: n };
      }
      rcDraw(buckets);
    });
    svg.addEventListener("mousedown", (e) => {
      rcDrag = rcZoom ? { startX: e.clientX, o0: rcZoom.o } : null;
      rcDragMoved = false;
      if (rcDrag) svg.style.cursor = "grabbing";
    });
    svg.addEventListener("mousemove", (e) => {
      if (!rcDrag || !rcZoom) return;
      if (e.buttons === 0) {
        rcDrag = null;
        svg.style.cursor = "grab";
        return;
      }
      const dx = e.clientX - rcDrag.startX;
      if (!rcDragMoved && Math.abs(dx) < 3) return;
      rcDragMoved = true;
      const o = Math.round(rcDrag.o0 - dx / (iw / rcZoom.n));
      const o2 = Math.max(0, Math.min(n - rcZoom.n, o));
      if (o2 !== rcZoom.o) {
        rcZoom = { ...rcZoom, o: o2 };
        rcDraw(buckets);
      }
    });
    svg.addEventListener("mouseup", () => {
      rcDrag = null;
      svg.style.cursor = rcZoom ? "grab" : "";
    });
    svg.addEventListener("click", (e) => {
      if (rcDragMoved) {
        rcDragMoved = false;
        return;
      }
      const best = nearest(xOf(e));
      if (!best || rcUnit === "day") return;
      e.stopPropagation();
      const rect = svg.getBoundingClientRect();
      rcOpenDetail(best.b, rect.left + best.cx / RC_W * rect.width);
    });
    svg.addEventListener("keydown", (e) => {
      if (e.key === "Escape") {
        if (cursor) cursor.style.display = "none";
        rcHideTip();
        return;
      }
      if (e.key !== "ArrowLeft" && e.key !== "ArrowRight") return;
      e.preventDefault();
      if (!centers.length || !cursor) return;
      let idx = 0;
      if (kbX !== null) {
        idx = centers.reduce((bi, c, i) => Math.abs(c.cx - kbX) < Math.abs(centers[bi].cx - kbX) ? i : bi, 0);
        idx = e.key === "ArrowRight" ? Math.min(centers.length - 1, idx + 1) : Math.max(0, idx - 1);
      }
      kbX = centers[idx].cx;
      const rect = svg.getBoundingClientRect();
      placeCursor(centers[idx], rect.top + (RC_M.top + ih / 2) * (rect.height / RC_H));
    });
  }
  var rcDetailEl = null;
  function rcCloseDetail() {
    if (rcDetailEl) rcDetailEl.classList.remove("open");
  }
  function rcOpenDetail(b, clientX) {
    const host = document.getElementById("rest-chart");
    if (!host) return;
    if (!rcDetailEl) {
      rcDetailEl = document.createElement("div");
      rcDetailEl.className = "rc-detail";
      host.append(rcDetailEl);
    }
    const days = rcLastDays.filter((d) => d.date >= b.start && d.date <= b.end).sort((x, y) => x.date < y.date ? -1 : 1);
    const rows = days.map(
      (d) => `<tr><td>${d.date}</td><td>${fmtDur(d.workMs)}</td><td>${fmtDur(d.breakMs)}</td><td>${fmtDur(d.longMs)}</td><td>${fmtDur(d.shortMs)}</td><td>${fmtDur(d.awayMs ?? 0)}</td><td>${d.shortCount + d.longCount + d.customCount}/${d.skipCount}/${d.postponeCount}</td></tr>`
    ).join("");
    rcDetailEl.innerHTML = `<div class="rc-detail-head"><b>${b.title}</b><button type="button" class="rc-detail-x" title="\u5173\u95ED">\u2715</button></div><div class="rc-detail-scroll"><table class="stats-table"><tr><th>\u65E5\u671F</th><th>\u5DE5\u4F5C</th><th>\u4F11\u606F\u603B</th><th>\u957F\u4F11</th><th>\u77ED\u4F11</th><th>\u79BB\u5F00</th><th>\u4F11/\u8DF3/\u63A8</th></tr>` + (rows || '<tr><td colspan="7" class="rc-empty">\u8BE5\u5468\u671F\u6CA1\u6709\u9010\u65E5\u6570\u636E</td></tr>') + "</table></div>";
    rcDetailEl.classList.add("open");
    rcDetailEl.querySelector(".rc-detail-x")?.addEventListener("click", rcCloseDetail);
    const hr = host.getBoundingClientRect();
    rcDetailEl.style.left = `${Math.max(8, Math.min(hr.width - 292, clientX - hr.left - 140))}px`;
    rcDetailEl.style.top = "44px";
    rcHideTip();
  }
  function rcRenderControls() {
    $("rc-units").innerHTML = RC_UNITS.map(
      (x) => `<button type="button" class="rc-unit${x.u === rcUnit ? " on" : ""}" data-u="${x.u}">${x.label}</button>`
    ).join("");
    rcRenderRange();
    const on = rcLegendOn();
    $("rc-legend").innerHTML = RC_SERIES.map(
      (s) => `<button type="button" class="rc-legend-item${on[s.id] ? "" : " off"}" data-s="${s.id}" title="\u70B9\u51FB\u663E\u793A / \u9690\u85CF"><i class="${s.cls} ${s.kind}"></i>${s.label}</button>`
    ).join("");
  }
  function rcRangeLabel(unit, r) {
    if (unit === "day") {
      return r.start.slice(0, 4) === r.end.slice(0, 4) ? `${r.start.slice(5)} ~ ${r.end.slice(5)}` : `${r.start} ~ ${r.end}`;
    }
    if (unit === "week") {
      const wk = (day) => {
        const [y, m, d] = day.split("-").map(Number);
        return rcIsoWeek(new Date(y, m - 1, d));
      };
      const y1 = r.start.slice(0, 4);
      const y2 = r.end.slice(0, 4);
      return y1 === y2 ? `${y1} \u5E74\u7B2C ${wk(r.start)} \u5468 ~ \u7B2C ${wk(r.end)} \u5468` : `${y1} \u5E74\u7B2C ${wk(r.start)} \u5468 ~ ${y2} \u5E74\u7B2C ${wk(r.end)} \u5468`;
    }
    if (unit === "quarter") {
      const q = (a) => `${a.slice(0, 4)}Q${Math.floor(Number(a.slice(5, 7)) / 3) + 1}`;
      return `${q(r.start)} ~ ${q(r.end)}`;
    }
    return `${r.start} ~ ${r.end}`;
  }
  function rcOpts(list, sel) {
    if (sel && !list.some((x) => x.v === sel)) list.unshift({ v: sel, t: `\u9009\u5B9A\uFF1A${sel}` });
    return list.map((x) => `<option value="${x.v}"${x.v === sel ? " selected" : ""}>${x.t}</option>`).join("");
  }
  function rcWeekOpts(sel) {
    const list = [];
    const mon = rcMonday(/* @__PURE__ */ new Date());
    let weeks = 110;
    if (rcEarliest) {
      const [y, m, d] = rcEarliest.split("-").map(Number);
      const e0 = rcMonday(new Date(y, m - 1, d));
      weeks = Math.round((mon.getTime() - e0.getTime()) / 864e5 / 7) + 1;
    }
    for (let i = 0; i < weeks; i++) {
      const d = new Date(mon.getFullYear(), mon.getMonth(), mon.getDate() - i * 7);
      const v = rcDayStr(d);
      const sun = new Date(d.getFullYear(), d.getMonth(), d.getDate() + 6);
      list.push({
        v,
        t: `${d.getFullYear()} \u5E74\u7B2C ${rcIsoWeek(d)} \u5468\uFF08${v.slice(5)} ~ ${rcDayStr(sun).slice(5)}\uFF09`
      });
    }
    return rcOpts(list, sel);
  }
  function rcMonthOpts(sel) {
    const list = [];
    const now = /* @__PURE__ */ new Date();
    let months = 36;
    if (rcEarliest) {
      const [y, m] = rcEarliest.split("-").map(Number);
      months = now.getFullYear() * 12 + now.getMonth() - (y * 12 + m - 1) + 1;
    }
    for (let i = 0; i < months; i++) {
      const d = new Date(now.getFullYear(), now.getMonth() - i, 1);
      list.push({
        v: `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}`,
        t: `${d.getFullYear()} \u5E74 ${d.getMonth() + 1} \u6708`
      });
    }
    return rcOpts(list, sel);
  }
  function rcQuarterOpts(sel) {
    const list = [];
    const now = /* @__PURE__ */ new Date();
    const q0 = Math.floor(now.getMonth() / 3);
    let qs = 16;
    if (rcEarliest) {
      const [y, m] = rcEarliest.split("-").map(Number);
      qs = now.getFullYear() * 4 + q0 - (y * 4 + Math.floor((m - 1) / 3)) + 1;
    }
    for (let i = 0; i < qs; i++) {
      const d = new Date(now.getFullYear(), (q0 - i) * 3, 1);
      list.push({
        v: `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}`,
        t: `${d.getFullYear()} \u5E74\u7B2C ${Math.floor(d.getMonth() / 3) + 1} \u5B63\u5EA6`
      });
    }
    return rcOpts(list, sel);
  }
  function rcYearOpts(sel) {
    const list = [];
    const y = (/* @__PURE__ */ new Date()).getFullYear();
    let ys = 6;
    if (rcEarliest) ys = y - Number(rcEarliest.slice(0, 4)) + 1;
    for (let i = 0; i < ys; i++) list.push({ v: String(y - i), t: `${y - i} \u5E74` });
    return rcOpts(list, sel);
  }
  function rcPickerHtml(unit, r) {
    const row = (span, ctl) => `<div class="rc-pop-row"><span>${span}</span>${ctl}</div>`;
    if (unit === "day") {
      const min = rcEarliest ? ` min="${rcEarliest}"` : "";
      return row("\u4ECE", `<input type="date" class="rc-in" id="rc-rs" value="${r.start}"${min}>`) + row("\u5230", `<input type="date" class="rc-in" id="rc-re" value="${r.end}"${min}>`);
    }
    const opts = unit === "week" ? rcWeekOpts : unit === "month" ? rcMonthOpts : unit === "quarter" ? rcQuarterOpts : unit === "year" ? rcYearOpts : null;
    if (!opts) return "";
    return row("\u4ECE", `<select class="rc-in" id="rc-rs">${opts(r.start)}</select>`) + row("\u5230", `<select class="rc-in" id="rc-re">${opts(r.end)}</select>`);
  }
  function rcRenderRange() {
    const box = $("rc-range");
    const r = rcGetRange(rcUnit);
    box.innerHTML = `<button type="button" class="rc-range-btn" id="rc-range-btn" title="\u9009\u62E9\u65F6\u95F4\u8303\u56F4"><span id="rc-range-label">${rcRangeLabel(rcUnit, r)}</span><span class="rc-caret">\u25BE</span></button><div class="rc-pop" id="rc-pop">${rcPickerHtml(rcUnit, r)}<div class="rc-pop-foot"><button type="button" class="btn ghost" id="rc-range-all" title="\u4ECE\u6700\u65E9\u8BB0\u5F55\u4E00\u76F4\u770B\u5230\u5F53\u524D\u5468\u671F">\u5168\u90E8\u6570\u636E</button><button type="button" class="btn ghost" id="rc-range-reset">\u6062\u590D\u9ED8\u8BA4</button></div></div>`;
    $("rc-range-btn").addEventListener("click", () => box.classList.toggle("open"));
    $("rc-range-all").addEventListener("click", () => {
      const all = rcAllRange(rcUnit);
      if (!all) {
        toast("\u8FD8\u6CA1\u6709\u7EDF\u8BA1\u6570\u636E");
        return;
      }
      rcSetRange(rcUnit, all);
      box.classList.remove("open");
      rcRenderControls();
      void renderRestChart(true);
    });
    $("rc-range-reset").addEventListener("click", () => {
      rcResetRange(rcUnit);
      rcRenderControls();
      void renderRestChart(true);
    });
    const startCtl = box.querySelector("#rc-rs");
    const endCtl = box.querySelector("#rc-re");
    const apply = () => {
      if (!startCtl || !endCtl) return;
      rcSetRange(rcUnit, { start: startCtl.value, end: endCtl.value });
      const label = $("rc-range-label");
      if (label) label.textContent = rcRangeLabel(rcUnit, rcGetRange(rcUnit));
      void renderRestChart(true);
    };
    startCtl?.addEventListener("change", apply);
    endCtl?.addEventListener("change", apply);
  }
  function rcRangeFilePart(unit, r) {
    if (unit === "quarter") {
      const q = (a) => {
        const [y, m] = a.split("-").map(Number);
        return `${y}Q${Math.floor((m - 1) / 3) + 1}`;
      };
      return { s: q(r.start), e: q(r.end) };
    }
    return { s: r.start.replace(/-/g, ""), e: r.end.replace(/-/g, "") };
  }
  function rcExportName(ext) {
    const ul = RC_UNITS.find((x) => x.u === rcUnit)?.label ?? rcUnit;
    const fp = rcRangeFilePart(rcUnit, rcGetRange(rcUnit));
    return `\u4F11\u606F\u62A5\u8868_${ul}_${fp.s}_${fp.e}.${ext}`;
  }
  // 报表导出统一走「另存为」对话框：blob 转 base64 交给 Go 侧 ExportReport，
  // 由用户选择存储路径（上一版时代的 a.download 落点不可控，Wails 下也不可靠）。
  // 取消静默、失败 toast、成功 toast 落盘路径。
  async function saveExportFile(name, blob) {
    const b64 = await new Promise((res) => {
      const fr = new FileReader();
      fr.onload = () => res(String(fr.result ?? "").split(",")[1] || "");
      fr.onerror = () => res("");
      fr.readAsDataURL(blob);
    });
    if (!b64) {
      toast("\u5BFC\u51FA\u5931\u8D25");
      return;
    }
    const r = await window.api.exportReport(name, b64);
    if (!r || r.canceled) return;
    if (r.error) {
      toast(r.error);
      return;
    }
    toast(`\u5DF2\u5BFC\u51FA\u5230 ${r.path}`);
  }
  function setupRestChart() {
    $("rc-units").addEventListener("click", (e) => {
      const b = e.target.closest(".rc-unit");
      if (!b) return;
      rcUnit = b.dataset.u;
      rcSaveStr("unit", rcUnit);
      rcRenderControls();
      void renderRestChart(true);
    });
    $("rc-legend").addEventListener("click", (e) => {
      const b = e.target.closest(".rc-legend-item");
      if (!b) return;
      const on = rcLegendOn();
      const id = b.dataset.s;
      on[id] = !on[id];
      rcSaveStr("on", JSON.stringify(on));
      rcRenderControls();
      void renderRestChart();
    });
    $("rc-legend").addEventListener("dblclick", (e) => {
      const b = e.target.closest(".rc-legend-item");
      if (!b) return;
      const id = b.dataset.s;
      const on = rcLegendOn();
      const only = Object.keys(on).every((k) => on[k] === (k === id));
      const all = { long: true, short: true, rest: true, skip: true, postpone: true };
      const none = { long: false, short: false, rest: false, skip: false, postpone: false };
      rcSaveStr("on", JSON.stringify(only ? all : { ...none, [id]: true }));
      rcRenderControls();
      void renderRestChart();
    });
    const emptyBtn = $("rc-empty-btn");
    emptyBtn.classList.toggle("on", rcShowEmpty);
    emptyBtn.addEventListener("click", () => {
      rcShowEmpty = !rcShowEmpty;
      rcSaveStr("empty", rcShowEmpty ? "1" : "0");
      emptyBtn.classList.toggle("on", rcShowEmpty);
      void renderRestChart();
    });
    $("rc-exp-csv").addEventListener("click", () => {
      const rows = [...rcTableData].sort((a2, b) => a2.start < b.start ? -1 : 1);
      const q = (v) => `"${String(v).replace(/"/g, '""')}"`;
      const lines = ["\u5468\u671F,\u5F00\u59CB,\u7ED3\u675F,\u5DE5\u4F5C\u65F6\u95F4,\u4F11\u606F\u603B\u65F6\u95F4,\u957F\u4F11\u606F,\u77ED\u4F11\u606F,\u79BB\u5F00\u65F6\u95F4,\u4F11\u606F\u6B21\u6570,\u8DF3\u8FC7\u6B21\u6570,\u63A8\u8FDF\u6B21\u6570"];
      for (const b of rows) {
        lines.push(
          [
            b.title,
            b.start,
            b.end,
            fmtDur(b.workMs),
            fmtDur(b.breakMs),
            fmtDur(b.longMs),
            fmtDur(b.shortMs),
            fmtDur(b.awayMs),
            b.restCount,
            b.skipCount,
            b.postponeCount
          ].map(q).join(",")
        );
      }
      if (rows.length) {
        const sum = (f) => rows.reduce((s, b) => s + f(b), 0);
        lines.push(
          [
            "\u5408\u8BA1",
            "",
            "",
            fmtDur(sum((b) => b.workMs)),
            fmtDur(sum((b) => b.breakMs)),
            fmtDur(sum((b) => b.longMs)),
            fmtDur(sum((b) => b.shortMs)),
            fmtDur(sum((b) => b.awayMs)),
            sum((b) => b.restCount),
            sum((b) => b.skipCount),
            sum((b) => b.postponeCount)
          ].map(q).join(",")
        );
      }
      void saveExportFile(rcExportName("csv"), new Blob(["\uFEFF" + lines.join("\r\n")], { type: "text/csv;charset=utf-8" }));
    });
    $("rc-exp-png").addEventListener("click", () => {
      const svg = document.querySelector("#rc-plot svg");
      if (!svg) return;
      try {
        const clone = svg.cloneNode(true);
        clone.setAttribute("xmlns", "http://www.w3.org/2000/svg");
        const cs = getComputedStyle(document.querySelector(".rest-chart") ?? document.body);
        const v = (name, fb) => cs.getPropertyValue(name).trim() || fb;
        const st = document.createElementNS("http://www.w3.org/2000/svg", "style");
        st.textContent = `text{fill:${v("--text-2", "#888888")};font-size:11px;font-variant-numeric:tabular-nums;font-family:${cs.fontFamily};}.rc-cap{font-weight:600;}.grid{stroke:${v("--border", "#dddddd")};stroke-width:1;}.rc-axis{stroke:${v("--text-2", "#888888")};stroke-opacity:0.5;}.rc-avgline{stroke:${v("--text-2", "#888888")};stroke-opacity:0.6;stroke-dasharray:5 4;}.rc-avglabel{fill:${v("--text", "#333333")};font-size:11px;font-weight:600;}.rc-peak{font-size:10px;font-weight:600;}.rc-peak.rc-l-rest{fill:#2fa3b5;}.rc-peak.rc-l-skip{fill:#e05555;}.rc-peak.rc-l-post{fill:#e0a23f;}path.rc-line{fill:none;stroke-width:2;}.rc-l-rest{stroke:#2fa3b5;}.rc-l-skip{stroke:#e05555;}.rc-l-post{stroke:#e0a23f;}circle.rc-dot.rc-l-rest{fill:#2fa3b5;}circle.rc-dot.rc-l-skip{fill:#e05555;}circle.rc-dot.rc-l-post{fill:#e0a23f;}.rc-cursor{display:none;}`;
        clone.insertBefore(st, clone.firstChild);
        const xml = new XMLSerializer().serializeToString(clone);
        const img = new Image();
        img.onload = () => {
          try {
            const cv = document.createElement("canvas");
            cv.width = RC_W * 2;
            cv.height = RC_H * 2;
            const ctx = cv.getContext("2d");
            if (!ctx) return;
            ctx.fillStyle = v("--card", "#ffffff");
            ctx.fillRect(0, 0, cv.width, cv.height);
            ctx.drawImage(img, 0, 0, cv.width, cv.height);
            cv.toBlob((blob) => {
              if (!blob) return;
              void saveExportFile(rcExportName("png"), blob);
            }, "image/png");
          } catch {
            toast("\u5BFC\u51FA\u56FE\u7247\u5931\u8D25");
          }
        };
        img.src = "data:image/svg+xml;charset=utf-8," + encodeURIComponent(xml);
      } catch {
        toast("\u5BFC\u51FA\u56FE\u7247\u5931\u8D25");
      }
    });
    document.addEventListener("click", (e) => {
      const open = document.querySelector(".rc-range.open");
      if (open && !open.contains(e.target)) open.classList.remove("open");
      const det = document.querySelector(".rc-detail.open");
      if (det && !det.contains(e.target)) det.classList.remove("open");
    });
    $("rc-table").addEventListener("click", (e) => {
      const th = e.target.closest("th.sortable");
      if (!th) return;
      const key = th.dataset.key;
      if (rcTableSort.key === key) rcTableSort.dir = rcTableSort.dir === "asc" ? "desc" : "asc";
      else rcTableSort = { key, dir: "desc" };
      rcRenderTable(rcTableData);
    });
    rcRenderControls();
    void rcGetEarliest().then((e) => {
      if (e) rcRenderControls();
    });
  }
  var TK_NS = "wr-tkv2";
  function tkLoadStr(key) {
    return localStorage.getItem(`${TK_NS}-${key}`);
  }
  function tkSaveStr(key, v) {
    localStorage.setItem(`${TK_NS}-${key}`, v);
  }
  var TK_SERIES = [
    { id: "ord", label: "\u70B9\u5916\u5356\u6B21\u6570", cls: "tk-l-ord" },
    { id: "can", label: "\u53D6\u6D88\u6B21\u6570", cls: "tk-l-can" },
    { id: "post", label: "\u63A8\u8FDF\u6B21\u6570", cls: "tk-l-post" }
  ];
  var tkUnit = tkLoadStr("unit") || "";
  if (!RC_WINDOWS[tkUnit]) tkUnit = "day";
  var tkShowEmpty = tkLoadStr("empty") === "1";
  function tkLegendOn() {
    const def = { ord: true, can: true, post: true };
    try {
      const raw = tkLoadStr("on");
      return { ...def, ...JSON.parse(raw || "{}") };
    } catch {
      return def;
    }
  }
  var tkRangeCache = {};
  function tkGetRange(unit) {
    let r = tkRangeCache[unit];
    if (!r) {
      try {
        const raw = JSON.parse(tkLoadStr(`range-${unit}`) || "null");
        if (raw && typeof raw.start === "string" && typeof raw.end === "string" && raw.start <= raw.end) {
          r = { start: raw.start, end: raw.end };
        }
      } catch {
      }
      if (!r) r = rcDefaultRange(unit);
      tkRangeCache[unit] = r;
    }
    return r;
  }
  function tkSetRange(unit, r) {
    const norm2 = r.start <= r.end ? { start: r.start, end: r.end } : { start: r.end, end: r.start };
    tkRangeCache[unit] = norm2;
    tkSaveStr(`range-${unit}`, JSON.stringify(norm2));
  }
  function tkResetRange(unit) {
    delete tkRangeCache[unit];
    localStorage.removeItem(`${TK_NS}-range-${unit}`);
  }
  function tkEmptyBucket(b) {
    return b.takeoutOrdered <= 0 && b.takeoutSkipped <= 0 && b.takeoutPostponed <= 0;
  }
  var tkSeq = 0;
  var tkLastDays = [];
  var tkPrevOf = null;
  function tkRenderBadges(shown, unit, prev) {
    const box = $("tk-badges");
    if (!box) return;
    const ord = shown.reduce((s, b) => s + b.takeoutOrdered, 0);
    const can = shown.reduce((s, b) => s + b.takeoutSkipped, 0);
    const post = shown.reduce((s, b) => s + b.takeoutPostponed, 0);
    const unitLabel = { day: "\u65E5\u5747", week: "\u5468\u5747", month: "\u6708\u5747", quarter: "\u5B63\u5747", year: "\u5E74\u5747" }[unit];
    const delta = (cur, old) => {
      if (old <= 0) return "";
      const pct = Math.round((cur - old) / old * 100);
      return pct === 0 ? "\u8F83\u4E0A\u671F\u6301\u5E73" : `\u8F83\u4E0A\u671F ${pct > 0 ? "+" : "\u2212"}${Math.abs(pct)}%`;
    };
    const badge = (k, v, sub) => `<div class="rc-badge"><span class="k">${k}</span><b>${v}</b>${sub ? `<i>${sub}</i>` : ""}</div>`;
    box.innerHTML = badge("\u70B9\u5916\u5356\u6B21\u6570", String(ord), delta(ord, prev.ord)) + badge("\u53D6\u6D88\u6B21\u6570", String(can), delta(can, prev.can)) + badge("\u63A8\u8FDF\u6B21\u6570", String(post), delta(post, prev.post)) + badge(
      `${unitLabel}\u70B9\u5916\u5356`,
      shown.length ? String(Math.round(ord / shown.length * 10) / 10) : "\u2014",
      `${shown.length} \u4E2A\u5468\u671F`
    );
  }
  async function renderTakeoutChart(animate = false) {
    const seq = ++tkSeq;
    const range = tkGetRange(tkUnit);
    const buckets = rcBuckets(tkUnit, range);
    const prevBuckets = rcBuckets(tkUnit, rcPrevRange(tkUnit, range, buckets.length));
    const anchor = rcAnchorToDay(tkUnit, prevBuckets[0]?.start ?? range.start);
    const data = await window.api.getHistory(anchor, null);
    if (seq !== tkSeq) return;
    tkLastDays = data.days ?? [];
    rcFillBuckets(buckets, tkLastDays);
    rcFillBuckets(prevBuckets, tkLastDays);
    const shown = tkShowEmpty ? buckets : buckets.filter((b) => !tkEmptyBucket(b));
    const fullIdx = new Map(buckets.map((b, i) => [b.key, i]));
    tkPrevOf = (b) => {
      const i = fullIdx.get(b.key);
      return i !== void 0 && i > 0 ? buckets[i - 1] : null;
    };
    tkRenderBadges(shown, tkUnit, {
      ord: prevBuckets.reduce((s, b) => s + b.takeoutOrdered, 0),
      can: prevBuckets.reduce((s, b) => s + b.takeoutSkipped, 0),
      post: prevBuckets.reduce((s, b) => s + b.takeoutPostponed, 0)
    });
    tkDraw(shown, animate);
    tkRenderTable(buckets.filter((b) => !tkEmptyBucket(b)));
  }
  var TK_SORT_GET = {
    period: (b) => b.start,
    ord: (b) => b.takeoutOrdered,
    can: (b) => b.takeoutSkipped,
    post: (b) => b.takeoutPostponed
  };
  var TK_TABLE_COLS = [
    { key: "period", label: "\u5468\u671F" },
    { key: "ord", label: "\u70B9\u5916\u5356\u6B21\u6570" },
    { key: "can", label: "\u53D6\u6D88\u6B21\u6570" },
    { key: "post", label: "\u63A8\u8FDF\u6B21\u6570" }
  ];
  var tkTableSort = { key: "period", dir: "asc" };
  var tkTableData = [];
  function tkRenderTable(rows) {
    const t = $("tk-table");
    if (!t) return;
    tkTableData = rows;
    const head = "<tr>" + TK_TABLE_COLS.map((c) => {
      const arrow = c.key === tkTableSort.key ? tkTableSort.dir === "asc" ? " \u25B2" : " \u25BC" : "";
      const num = c.key === "period" ? "" : " num";
      return `<th class="sortable${num}" data-key="${c.key}" title="\u70B9\u6309\u6392\u5E8F">${c.label}${arrow}</th>`;
    }).join("") + "</tr>";
    const dir = tkTableSort.dir === "asc" ? 1 : -1;
    const get = TK_SORT_GET[tkTableSort.key];
    const sorted = [...rows].sort((a, b) => {
      const va = get(a);
      const vb = get(b);
      return (va < vb ? -1 : va > vb ? 1 : 0) * dir;
    });
    const cells = (b) => `<td>${b.title}</td><td class="num tkv-ord" data-v="${b.takeoutOrdered}">${b.takeoutOrdered}</td><td class="num tkv-can" data-v="${b.takeoutSkipped}">${b.takeoutSkipped}</td><td class="num tkv-post" data-v="${b.takeoutPostponed}">${b.takeoutPostponed}</td>`;
    const body = rows.length ? sorted.map((b) => `<tr>${cells(b)}</tr>`).join("") : '<tr><td colspan="4" class="rc-empty">\u6682\u65E0\u6570\u636E</td></tr>';
    const sum = (f) => rows.reduce((s, b) => s + f(b), 0);
    const total = rows.length ? `<tr class="total"><td>\u5408\u8BA1</td><td class="num tkv-ord">${sum((b) => b.takeoutOrdered)}</td><td class="num tkv-can">${sum((b) => b.takeoutSkipped)}</td><td class="num tkv-post">${sum((b) => b.takeoutPostponed)}</td></tr>` : "";
    t.innerHTML = head + body + total;
  }
  var tkTip = null;
  function tkHideTip() {
    if (tkTip) tkTip.style.display = "none";
  }
  function tkShowTip(b, x, y) {
    if (!tkTip) {
      tkTip = document.createElement("div");
      tkTip.className = "rc-tip";
      document.body.append(tkTip);
    }
    let env = "";
    const prev = tkPrevOf?.(b) ?? null;
    if (prev) {
      const pct = (cur, old) => {
        if (old <= 0) return cur > 0 ? "\u2014" : "";
        const p = Math.round((cur - old) / old * 100);
        return p === 0 ? "\u6301\u5E73" : `${p > 0 ? "+" : "\u2212"}${Math.abs(p)}%`;
      };
      const op = pct(b.takeoutOrdered, prev.takeoutOrdered);
      const cp = pct(b.takeoutSkipped, prev.takeoutSkipped);
      const pp = pct(b.takeoutPostponed, prev.takeoutPostponed);
      if (op || cp || pp) {
        env = `<span class="rc-tip-d">\u8F83\u4E0A\u4E00\u671F\u3000\u70B9\u5916\u5356 ${op || "\u2014"} \xB7 \u53D6\u6D88 ${cp || "\u2014"} \xB7 \u63A8\u8FDF ${pp || "\u2014"}</span>`;
      }
    }
    tkTip.innerHTML = `<b>${b.title}</b><span class="tkv-ord">\u70B9\u5916\u5356 ${b.takeoutOrdered} \u6B21</span><span class="tkv-can">\u53D6\u6D88 ${b.takeoutSkipped} \u6B21</span><span class="tkv-post">\u63A8\u8FDF ${b.takeoutPostponed} \u6B21</span>` + env;
    tkTip.style.display = "block";
    const r = tkTip.getBoundingClientRect();
    tkTip.style.left = `${Math.max(8, Math.min(window.innerWidth - r.width - 8, x + 14))}px`;
    tkTip.style.top = `${Math.max(8, Math.min(window.innerHeight - r.height - 8, y - r.height - 12))}px`;
  }
  var tkZoom = null;
  var tkDrag = null;
  var tkDragMoved = false;
  function tkDraw(buckets, animate = false) {
    const box = $("tk-plot");
    const on = tkLegendOn();
    const n = buckets.length;
    if (tkZoom && (tkZoom.total !== n || tkZoom.o >= n)) tkZoom = null;
    const zo = tkZoom ? Math.min(tkZoom.o, Math.max(0, n - tkZoom.n)) : 0;
    const zv = tkZoom ? Math.min(tkZoom.n, n - zo) : n;
    const view = zv === n ? buckets : buckets.slice(zo, zo + zv);
    const iw = RC_W - RC_M.left - RC_M.right;
    const ih = RC_H - RC_M.top - RC_M.bottom;
    const lineParts = TK_SERIES.filter((s) => on[s.id]);
    const maxCnt = buckets.reduce(
      (mv, b) => Math.max(mv, b.takeoutOrdered, b.takeoutSkipped, b.takeoutPostponed),
      0
    );
    const ax = rcProbeAxis(maxCnt);
    const yC = (v) => RC_M.top + ih * (1 - v / ax.max);
    const step = iw / Math.max(1, zv);
    const showDots = zv <= 60;
    const showPeaks = zv <= 40;
    const grad = (id, color, o0, o1) => `<linearGradient id="${id}" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="${color}" stop-opacity="${o0}"/><stop offset="1" stop-color="${color}" stop-opacity="${o1}"/></linearGradient>`;
    const defs = `<defs>` + grad("tk-a-ord", "#2fa3b5", 0.2, 0) + grad("tk-a-can", "#e05555", 0.2, 0) + grad("tk-a-post", "#e0a23f", 0.2, 0) + `</defs>`;
    let g = "";
    g += `<line class="rc-axis" x1="${RC_M.left}" y1="${RC_M.top}" x2="${RC_M.left}" y2="${(RC_M.top + ih).toFixed(1)}"/>`;
    for (let v = 0; v <= ax.max; v += ax.step) {
      const yy = yC(v).toFixed(1);
      g += `<line class="grid" x1="${RC_M.left}" y1="${yy}" x2="${RC_W - RC_M.right}" y2="${yy}"/><text class="tick" x="${RC_M.left - 6}" y="${+yy + 4}" text-anchor="end">${v}</text>`;
    }
    g += `<text class="tick rc-cap" x="${RC_M.left - 6}" y="10" text-anchor="end">\u6B21\u6570</text>`;
    const withData = buckets.filter((b) => !tkEmptyBucket(b));
    if (withData.length) {
      const avg = withData.reduce((s, b) => s + b.takeoutOrdered, 0) / withData.length;
      if (avg > 0 && avg < ax.max) {
        const yy = yC(avg).toFixed(1);
        const txt = avg < 10 ? String(Math.round(avg * 10) / 10) : String(Math.round(avg));
        g += `<line class="rc-avgline" x1="${RC_M.left}" y1="${yy}" x2="${RC_W - RC_M.right}" y2="${yy}"/><text class="rc-avglabel" x="${RC_W - RC_M.right - 4}" y="${+yy - 4}" text-anchor="end">\u5E73\u5747 ${txt} \u6B21</text>`;
      }
    }
    for (const s of lineParts) {
      const val = (b) => s.id === "ord" ? b.takeoutOrdered : s.id === "can" ? b.takeoutSkipped : b.takeoutPostponed;
      const segs = [];
      let pts = [];
      view.forEach((b, i) => {
        const p = { x: RC_M.left + step * i + step / 2, y: yC(val(b)), b };
        if (tkEmptyBucket(b)) {
          if (pts.length) segs.push(pts);
          pts = [];
        } else pts.push(p);
      });
      if (pts.length) segs.push(pts);
      const base = yC(0).toFixed(1);
      for (const seg of segs) {
        const d = rcSmoothPath(seg);
        if (d) {
          if (seg.length >= 2) {
            const first = seg[0];
            const last = seg[seg.length - 1];
            g += `<path class="rc-area ${s.cls}" d="${d} L ${last.x.toFixed(1)} ${base} L ${first.x.toFixed(1)} ${base} Z" style="fill:url(#tk-a-${s.id})"/>`;
          }
          g += `<path class="rc-line ${s.cls}" d="${d}"${animate ? ' pathLength="1"' : ""}/>`;
          if (showPeaks && seg.length >= 2) {
            const pk = seg.reduce((m, p) => val(p.b) > val(m.b) ? p : m, seg[0]);
            if (val(pk.b) > 0) {
              g += `<text class="rc-peak ${s.cls}" x="${pk.x.toFixed(1)}" y="${(pk.y - 7).toFixed(1)}" text-anchor="middle">${val(pk.b)}</text>`;
            }
          }
        }
        if (showDots) {
          seg.forEach((p) => {
            g += `<circle class="rc-dot ${s.cls}" cx="${p.x.toFixed(1)}" cy="${p.y.toFixed(1)}" r="2.6"/>`;
          });
        }
      }
    }
    const labelEvery = zv <= 10 ? 1 : Math.ceil(zv / 10);
    view.forEach((b, i) => {
      if (i % labelEvery === 0) {
        g += `<text class="tick" x="${(RC_M.left + step * i + step / 2).toFixed(1)}" y="${RC_H - 8}" text-anchor="middle">${b.label}</text>`;
      }
    });
    if (view.length === 0 || withData.length === 0) {
      g += `<text class="tick" x="${(RC_M.left + iw / 2).toFixed(1)}" y="${(RC_M.top + ih / 2).toFixed(1)}" text-anchor="middle">\u6682\u65E0\u6570\u636E</text>`;
    }
    g += `<line class="rc-cursor" x1="0" y1="${RC_M.top}" x2="0" y2="${(RC_M.top + ih).toFixed(1)}" style="display:none"/>`;
    g += `<rect class="rc-hit" x="${RC_M.left}" y="${RC_M.top}" width="${iw.toFixed(1)}" height="${ih.toFixed(1)}"/>`;
    box.innerHTML = `<svg viewBox="0 0 ${RC_W} ${RC_H}" role="img" tabindex="0" aria-label="\u5916\u5356\u62A5\u8868\u56FE"${animate ? ' class="anim"' : ""}>${defs}${g}</svg>`;
    const svg = box.querySelector("svg");
    if (!svg) return;
    svg.style.cursor = tkZoom ? "grab" : "";
    const cursor = svg.querySelector(".rc-cursor");
    const centers = view.map((b, i) => ({ b, cx: RC_M.left + step * i + step / 2 }));
    let kbX = null;
    const nearest = (xSvg) => {
      if (!centers.length) return null;
      let best = centers[0];
      for (const c of centers) if (Math.abs(c.cx - xSvg) < Math.abs(best.cx - xSvg)) best = c;
      return xSvg < RC_M.left - 6 || xSvg > RC_W - RC_M.right + 6 ? null : best;
    };
    const placeCursor = (c, clientY) => {
      if (!cursor) return;
      const rect = svg.getBoundingClientRect();
      cursor.setAttribute("x1", c.cx.toFixed(1));
      cursor.setAttribute("x2", c.cx.toFixed(1));
      cursor.style.display = "";
      tkShowTip(c.b, rect.left + c.cx / RC_W * rect.width, clientY);
    };
    const xOf = (e) => {
      const rect = svg.getBoundingClientRect();
      return (e.clientX - rect.left) / rect.width * RC_W;
    };
    svg.addEventListener("mousemove", (e) => {
      const best = nearest(xOf(e));
      if (!best || !cursor) {
        if (cursor) cursor.style.display = "none";
        tkHideTip();
        return;
      }
      kbX = best.cx;
      placeCursor(best, e.clientY);
    });
    svg.addEventListener("mouseleave", () => {
      if (cursor) cursor.style.display = "none";
      tkHideTip();
      tkDrag = null;
    });
    svg.addEventListener("wheel", (e) => {
      if (!n) return;
      e.preventDefault();
      const horiz = Math.abs(e.deltaX) > Math.abs(e.deltaY) || e.shiftKey;
      if (horiz && tkZoom) {
        const per = iw / tkZoom.n;
        const o = Math.round(tkZoom.o + (e.deltaX || e.deltaY) / per);
        tkZoom = { ...tkZoom, o: Math.max(0, Math.min(n - tkZoom.n, o)) };
        tkDraw(buckets);
        return;
      }
      const rect = svg.getBoundingClientRect();
      const xSvg = (e.clientX - rect.left) / rect.width * RC_W;
      const anchor = Math.max(0, Math.min(n - 1, Math.floor((xSvg - RC_M.left) / (iw / n))));
      const cur = tkZoom ?? { o: 0, n, total: n };
      let wn = Math.round(cur.n * (e.deltaY < 0 ? 1 / 1.35 : 1.35));
      wn = Math.min(n, Math.max(3, wn));
      if (wn >= n) {
        tkZoom = null;
      } else {
        const o = Math.round(anchor - (anchor - cur.o) * wn / cur.n);
        tkZoom = { o: Math.max(0, Math.min(n - wn, o)), n: wn, total: n };
      }
      tkDraw(buckets);
    });
    svg.addEventListener("mousedown", (e) => {
      tkDrag = tkZoom ? { startX: e.clientX, o0: tkZoom.o } : null;
      tkDragMoved = false;
      if (tkDrag) svg.style.cursor = "grabbing";
    });
    svg.addEventListener("mousemove", (e) => {
      if (!tkDrag || !tkZoom) return;
      if (e.buttons === 0) {
        tkDrag = null;
        svg.style.cursor = "grab";
        return;
      }
      const dx = e.clientX - tkDrag.startX;
      if (!tkDragMoved && Math.abs(dx) < 3) return;
      tkDragMoved = true;
      const o = Math.round(tkDrag.o0 - dx / (iw / tkZoom.n));
      const o2 = Math.max(0, Math.min(n - tkZoom.n, o));
      if (o2 !== tkZoom.o) {
        tkZoom = { ...tkZoom, o: o2 };
        tkDraw(buckets);
      }
    });
    svg.addEventListener("mouseup", () => {
      tkDrag = null;
      svg.style.cursor = tkZoom ? "grab" : "";
    });
    svg.addEventListener("click", (e) => {
      if (tkDragMoved) {
        tkDragMoved = false;
        return;
      }
      const best = nearest(xOf(e));
      if (!best || tkUnit === "day") return;
      e.stopPropagation();
      const rect = svg.getBoundingClientRect();
      tkOpenDetail(best.b, rect.left + best.cx / RC_W * rect.width);
    });
    svg.addEventListener("keydown", (e) => {
      if (e.key === "Escape") {
        if (cursor) cursor.style.display = "none";
        tkHideTip();
        return;
      }
      if (e.key !== "ArrowLeft" && e.key !== "ArrowRight") return;
      e.preventDefault();
      if (!centers.length || !cursor) return;
      let idx = 0;
      if (kbX !== null) {
        idx = centers.reduce((bi, c, i) => Math.abs(c.cx - kbX) < Math.abs(centers[bi].cx - kbX) ? i : bi, 0);
        idx = e.key === "ArrowRight" ? Math.min(centers.length - 1, idx + 1) : Math.max(0, idx - 1);
      }
      kbX = centers[idx].cx;
      const rect = svg.getBoundingClientRect();
      placeCursor(centers[idx], rect.top + (RC_M.top + ih / 2) * (rect.height / RC_H));
    });
  }
  var tkDetailEl = null;
  function tkCloseDetail() {
    if (tkDetailEl) tkDetailEl.classList.remove("open");
  }
  function tkOpenDetail(b, clientX) {
    const host = document.getElementById("takeout-chart");
    if (!host) return;
    if (!tkDetailEl) {
      tkDetailEl = document.createElement("div");
      tkDetailEl.className = "rc-detail";
      host.append(tkDetailEl);
    }
    const days = tkLastDays.filter((d) => d.date >= b.start && d.date <= b.end).sort((x, y) => x.date < y.date ? -1 : 1);
    const rows = days.map(
      (d) => `<tr><td>${d.date}</td><td>${d.takeoutOrdered}</td><td>${d.takeoutSkipped}</td><td>${d.takeoutPostponed}</td></tr>`
    ).join("");
    tkDetailEl.innerHTML = `<div class="rc-detail-head"><b>${b.title}</b><button type="button" class="rc-detail-x" title="\u5173\u95ED">\u2715</button></div><div class="rc-detail-scroll"><table class="stats-table"><tr><th>\u65E5\u671F</th><th>\u70B9\u5916\u5356</th><th>\u53D6\u6D88</th><th>\u63A8\u8FDF</th></tr>` + (rows || '<tr><td colspan="4" class="rc-empty">\u8BE5\u5468\u671F\u6CA1\u6709\u9010\u65E5\u6570\u636E</td></tr>') + "</table></div>";
    tkDetailEl.classList.add("open");
    tkDetailEl.querySelector(".rc-detail-x")?.addEventListener("click", tkCloseDetail);
    const hr = host.getBoundingClientRect();
    tkDetailEl.style.left = `${Math.max(8, Math.min(hr.width - 292, clientX - hr.left - 140))}px`;
    tkDetailEl.style.top = "44px";
    tkHideTip();
  }
  function tkRenderControls() {
    $("tk-units").innerHTML = RC_UNITS.map(
      (x) => `<button type="button" class="rc-unit${x.u === tkUnit ? " on" : ""}" data-u="${x.u}">${x.label}</button>`
    ).join("");
    tkRenderRange();
    const on = tkLegendOn();
    $("tk-legend").innerHTML = TK_SERIES.map(
      (s) => `<button type="button" class="rc-legend-item${on[s.id] ? "" : " off"}" data-s="${s.id}" title="\u70B9\u51FB\u663E\u793A / \u9690\u85CF"><i class="${s.cls} line"></i>${s.label}</button>`
    ).join("");
  }
  function tkRenderRange() {
    const box = $("tk-range");
    const r = tkGetRange(tkUnit);
    box.innerHTML = `<button type="button" class="rc-range-btn" id="tk-range-btn" title="\u9009\u62E9\u65F6\u95F4\u8303\u56F4"><span id="tk-range-label">${rcRangeLabel(tkUnit, r)}</span><span class="rc-caret">\u25BE</span></button><div class="rc-pop">${rcPickerHtml(tkUnit, r).replaceAll('id="rc-rs"', 'id="tk-rs"').replaceAll('id="rc-re"', 'id="tk-re"')}<div class="rc-pop-foot"><button type="button" class="btn ghost" id="tk-range-all" title="\u4ECE\u6700\u65E9\u8BB0\u5F55\u4E00\u76F4\u770B\u5230\u5F53\u524D\u5468\u671F">\u5168\u90E8\u6570\u636E</button><button type="button" class="btn ghost" id="tk-range-reset">\u6062\u590D\u9ED8\u8BA4</button></div></div>`;
    $("tk-range-btn").addEventListener("click", () => box.classList.toggle("open"));
    $("tk-range-all").addEventListener("click", () => {
      const all = rcAllRange(tkUnit);
      if (!all) {
        toast("\u8FD8\u6CA1\u6709\u7EDF\u8BA1\u6570\u636E");
        return;
      }
      tkSetRange(tkUnit, all);
      box.classList.remove("open");
      tkRenderControls();
      void renderTakeoutChart(true);
    });
    $("tk-range-reset").addEventListener("click", () => {
      tkResetRange(tkUnit);
      tkRenderControls();
      void renderTakeoutChart(true);
    });
    const startCtl = box.querySelector("#tk-rs");
    const endCtl = box.querySelector("#tk-re");
    const apply = () => {
      if (!startCtl || !endCtl) return;
      tkSetRange(tkUnit, { start: startCtl.value, end: endCtl.value });
      const label = $("tk-range-label");
      if (label) label.textContent = rcRangeLabel(tkUnit, tkGetRange(tkUnit));
      void renderTakeoutChart(true);
    };
    startCtl?.addEventListener("change", apply);
    endCtl?.addEventListener("change", apply);
  }
  function tkExportName(ext) {
    const ul = RC_UNITS.find((x) => x.u === tkUnit)?.label ?? tkUnit;
    const fp = rcRangeFilePart(tkUnit, tkGetRange(tkUnit));
    return `\u5916\u5356\u62A5\u8868_${ul}_${fp.s}_${fp.e}.${ext}`;
  }
  function setupTakeoutChart() {
    $("tk-units").addEventListener("click", (e) => {
      const b = e.target.closest(".rc-unit");
      if (!b) return;
      tkUnit = b.dataset.u;
      tkSaveStr("unit", tkUnit);
      tkRenderControls();
      void renderTakeoutChart(true);
    });
    $("tk-legend").addEventListener("click", (e) => {
      const b = e.target.closest(".rc-legend-item");
      if (!b) return;
      const on = tkLegendOn();
      const id = b.dataset.s;
      on[id] = !on[id];
      tkSaveStr("on", JSON.stringify(on));
      tkRenderControls();
      void renderTakeoutChart();
    });
    $("tk-legend").addEventListener("dblclick", (e) => {
      const b = e.target.closest(".rc-legend-item");
      if (!b) return;
      const id = b.dataset.s;
      const on = tkLegendOn();
      const only = Object.keys(on).every((k) => on[k] === (k === id));
      const all = { ord: true, can: true, post: true };
      const none = { ord: false, can: false, post: false };
      tkSaveStr("on", JSON.stringify(only ? all : { ...none, [id]: true }));
      tkRenderControls();
      void renderTakeoutChart();
    });
    const emptyBtn = $("tk-empty-btn");
    emptyBtn.classList.toggle("on", tkShowEmpty);
    emptyBtn.addEventListener("click", () => {
      tkShowEmpty = !tkShowEmpty;
      tkSaveStr("empty", tkShowEmpty ? "1" : "0");
      emptyBtn.classList.toggle("on", tkShowEmpty);
      void renderTakeoutChart();
    });
    $("tk-exp-csv").addEventListener("click", () => {
      const rows = [...tkTableData].sort((a2, b) => a2.start < b.start ? -1 : 1);
      const q = (v) => `"${String(v).replace(/"/g, '""')}"`;
      const lines = ["\u5468\u671F,\u5F00\u59CB,\u7ED3\u675F,\u70B9\u5916\u5356\u6B21\u6570,\u53D6\u6D88\u6B21\u6570,\u63A8\u8FDF\u6B21\u6570"];
      for (const b of rows) {
        lines.push(
          [b.title, b.start, b.end, b.takeoutOrdered, b.takeoutSkipped, b.takeoutPostponed].map(q).join(",")
        );
      }
      if (rows.length) {
        const sum = (f) => rows.reduce((s, b) => s + f(b), 0);
        lines.push(
          ["\u5408\u8BA1", "", "", sum((b) => b.takeoutOrdered), sum((b) => b.takeoutSkipped), sum((b) => b.takeoutPostponed)].map(q).join(",")
        );
      }
      void saveExportFile(tkExportName("csv"), new Blob(["\uFEFF" + lines.join("\r\n")], { type: "text/csv;charset=utf-8" }));
    });
    $("tk-exp-png").addEventListener("click", () => {
      const svg = document.querySelector("#tk-plot svg");
      if (!svg) return;
      try {
        const clone = svg.cloneNode(true);
        clone.setAttribute("xmlns", "http://www.w3.org/2000/svg");
        const cs = getComputedStyle(document.querySelector(".rest-chart") ?? document.body);
        const v = (name, fb) => cs.getPropertyValue(name).trim() || fb;
        const st = document.createElementNS("http://www.w3.org/2000/svg", "style");
        st.textContent = `text{fill:${v("--text-2", "#888888")};font-size:11px;font-variant-numeric:tabular-nums;font-family:${cs.fontFamily};}.rc-cap{font-weight:600;}.grid{stroke:${v("--border", "#dddddd")};stroke-width:1;}.rc-axis{stroke:${v("--text-2", "#888888")};stroke-opacity:0.5;}.rc-avgline{stroke:${v("--text-2", "#888888")};stroke-opacity:0.6;stroke-dasharray:5 4;}.rc-avglabel{fill:${v("--text", "#333333")};font-size:11px;font-weight:600;}.rc-peak{font-size:10px;font-weight:600;}.rc-peak.tk-l-ord{fill:#2fa3b5;}.rc-peak.tk-l-can{fill:#e05555;}.rc-peak.tk-l-post{fill:#e0a23f;}path.rc-line{fill:none;stroke-width:2;}.tk-l-ord{stroke:#2fa3b5;}.tk-l-can{stroke:#e05555;}.tk-l-post{stroke:#e0a23f;}circle.rc-dot.tk-l-ord{fill:#2fa3b5;}circle.rc-dot.tk-l-can{fill:#e05555;}circle.rc-dot.tk-l-post{fill:#e0a23f;}.rc-cursor{display:none;}`;
        clone.insertBefore(st, clone.firstChild);
        const xml = new XMLSerializer().serializeToString(clone);
        const img = new Image();
        img.onload = () => {
          try {
            const cv = document.createElement("canvas");
            cv.width = RC_W * 2;
            cv.height = RC_H * 2;
            const ctx = cv.getContext("2d");
            if (!ctx) return;
            ctx.fillStyle = v("--card", "#ffffff");
            ctx.fillRect(0, 0, cv.width, cv.height);
            ctx.drawImage(img, 0, 0, cv.width, cv.height);
            cv.toBlob((blob) => {
              if (!blob) return;
              void saveExportFile(tkExportName("png"), blob);
            }, "image/png");
          } catch {
            toast("\u5BFC\u51FA\u56FE\u7247\u5931\u8D25");
          }
        };
        img.src = "data:image/svg+xml;charset=utf-8," + encodeURIComponent(xml);
      } catch {
        toast("\u5BFC\u51FA\u56FE\u7247\u5931\u8D25");
      }
    });
    $("tk-table").addEventListener("click", (e) => {
      const th = e.target.closest("th.sortable");
      if (!th) return;
      const key = th.dataset.key;
      if (tkTableSort.key === key) tkTableSort.dir = tkTableSort.dir === "asc" ? "desc" : "asc";
      else tkTableSort = { key, dir: "desc" };
      tkRenderTable(tkTableData);
    });
    tkRenderControls();
    void rcGetEarliest().then((e) => {
      if (e) tkRenderControls();
    });
  }
  var CK_NS = "wr-ckv2";
  function ckLoadStr(key) {
    return localStorage.getItem(`${CK_NS}-${key}`);
  }
  function ckSaveStr(key, v) {
    localStorage.setItem(`${CK_NS}-${key}`, v);
  }
  function ckEsc(s) {
    return s.replace(/[&<>"']/g, (c) => `&#${c.charCodeAt(0)};`);
  }
  var CK_PALETTE = ["#2fa3b5", "#e05555", "#e0a23f", "#7b68d9", "#4caf7d", "#d96aa8", "#5b8def", "#9a7b4f"];
  var ckSeries = [];
  var ckUnit = ckLoadStr("unit") || "";
  if (!RC_WINDOWS[ckUnit]) ckUnit = "day";
  var ckShowEmpty = ckLoadStr("empty") === "1";
  function ckLegendOn() {
    const raw = ckLoadStr("on");
    try {
      return JSON.parse(raw || "{}");
    } catch {
      return {};
    }
  }
  var ckRangeCache = {};
  function ckGetRange(unit) {
    let r = ckRangeCache[unit];
    if (!r) {
      try {
        const raw = JSON.parse(ckLoadStr(`range-${unit}`) || "null");
        if (raw && typeof raw.start === "string" && typeof raw.end === "string" && raw.start <= raw.end) {
          r = { start: raw.start, end: raw.end };
        }
      } catch {
      }
      if (!r) r = rcDefaultRange(unit);
      ckRangeCache[unit] = r;
    }
    return r;
  }
  function ckSetRange(unit, r) {
    const norm2 = r.start <= r.end ? { start: r.start, end: r.end } : { start: r.end, end: r.start };
    ckRangeCache[unit] = norm2;
    ckSaveStr(`range-${unit}`, JSON.stringify(norm2));
  }
  function ckResetRange(unit) {
    delete ckRangeCache[unit];
    localStorage.removeItem(`${CK_NS}-range-${unit}`);
  }
  function ckVal(b, id) {
    return b.customCounts[id] ?? 0;
  }
  function ckBucketTotal(b) {
    return Object.values(b.customCounts).reduce((s, v) => s + v, 0);
  }
  function ckEmptyBucket(b) {
    return ckBucketTotal(b) <= 0;
  }
  var ckSeq = 0;
  var ckLastDays = [];
  var ckPrevOf = null;
  function ckRenderBadges(shown, unit, prevTotal, top, topN, seriesCount) {
    const box = $("ck-badges");
    if (!box) return;
    const total = shown.reduce((s, b) => s + ckBucketTotal(b), 0);
    const unitLabel = { day: "\u65E5\u5747", week: "\u5468\u5747", month: "\u6708\u5747", quarter: "\u5B63\u5747", year: "\u5E74\u5747" }[unit];
    let env = "";
    if (prevTotal > 0) {
      const pct = Math.round((total - prevTotal) / prevTotal * 100);
      env = pct === 0 ? "\u8F83\u4E0A\u671F\u6301\u5E73" : `\u8F83\u4E0A\u671F ${pct > 0 ? "+" : "\u2212"}${Math.abs(pct)}%`;
    }
    const badge = (k, v, sub) => `<div class="rc-badge"><span class="k">${k}</span><b>${v}</b>${sub ? `<i>${sub}</i>` : ""}</div>`;
    box.innerHTML = badge("\u89E6\u53D1\u6B21\u6570", String(total), env) + badge(
      `${unitLabel}\u89E6\u53D1`,
      shown.length ? String(Math.round(total / shown.length * 10) / 10) : "\u2014",
      `${shown.length} \u4E2A\u5468\u671F`
    ) + badge("\u6700\u6D3B\u8DC3\u63D0\u9192", top ? `${topN} \u6B21` : "\u2014", top ? ckEsc(top.label) : "") + badge("\u63D0\u9192\u6761\u6570", String(seriesCount), seriesCount ? "\u542B\u5DF2\u5220\u9664\u63D0\u9192\u7684\u5386\u53F2" : "");
  }
  async function renderCustomReportChart(animate = false) {
    const seq = ++ckSeq;
    const range = ckGetRange(ckUnit);
    const buckets = rcBuckets(ckUnit, range);
    const prevBuckets = rcBuckets(ckUnit, rcPrevRange(ckUnit, range, buckets.length));
    const anchor = rcAnchorToDay(ckUnit, prevBuckets[0]?.start ?? range.start);
    const data = await window.api.getHistory(anchor, null);
    if (seq !== ckSeq) return;
    ckLastDays = data.days ?? [];
    rcFillBuckets(buckets, ckLastDays);
    rcFillBuckets(prevBuckets, ckLastDays);
    const seen = /* @__PURE__ */ new Set();
    for (const b of [...buckets, ...prevBuckets]) for (const id of Object.keys(b.customCounts)) seen.add(id);
    const names = data.customNames ?? {};
    const live = (S.customBreaks ?? []).map((cb) => ({ id: cb.id, label: cb.name, color: "" }));
    const hist = [...seen].filter((id) => !live.some((s) => s.id === id)).sort((a, b) => a.localeCompare(b)).map((id) => ({ id, label: names[id] || "\u5DF2\u5220\u9664\u7684\u63D0\u9192", color: "" }));
    const series = [...live, ...hist];
    const order = series.map((s) => s.id).sort();
    for (const s of series) s.color = CK_PALETTE[order.indexOf(s.id) % CK_PALETTE.length];
    ckSeries = series;
    const shown = ckShowEmpty ? buckets : buckets.filter((b) => !ckEmptyBucket(b));
    const fullIdx = new Map(buckets.map((b, i) => [b.key, i]));
    ckPrevOf = (b) => {
      const i = fullIdx.get(b.key);
      return i !== void 0 && i > 0 ? buckets[i - 1] : null;
    };
    const perId = /* @__PURE__ */ new Map();
    for (const b of shown) for (const [id, v] of Object.entries(b.customCounts)) perId.set(id, (perId.get(id) ?? 0) + v);
    let top = null;
    let topN = 0;
    for (const s of series) {
      const n = perId.get(s.id) ?? 0;
      if (n > topN) {
        top = s;
        topN = n;
      }
    }
    ckRenderBadges(shown, ckUnit, prevBuckets.reduce((s, b) => s + ckBucketTotal(b), 0), top, topN, series.length);
    ckDraw(shown, animate);
    ckRenderLegend();
    ckRenderTable(buckets.filter((b) => !ckEmptyBucket(b)));
  }
  function ckRenderLegend() {
    const box = $("ck-legend");
    if (!box) return;
    const on = ckLegendOn();
    box.innerHTML = ckSeries.map(
      (s) => `<button type="button" class="rc-legend-item${on[s.id] === false ? " off" : ""}" data-s="${s.id}" title="\u70B9\u51FB\u663E\u793A / \u9690\u85CF"><i class="line" style="background:${s.color}"></i>${ckEsc(s.label)}</button>`
    ).join("");
  }
  var ckTableSort = { key: "period", dir: "asc" };
  var ckTableData = [];
  function ckRenderTable(rows) {
    const t = $("ck-table");
    if (!t) return;
    ckTableData = rows;
    const th = (key, label) => {
      const arrow = key === ckTableSort.key ? ckTableSort.dir === "asc" ? " \u25B2" : " \u25BC" : "";
      const num = key === "period" ? "" : " num";
      return `<th class="sortable${num}" data-key="${key}" title="\u70B9\u6309\u6392\u5E8F">${ckEsc(label)}${arrow}</th>`;
    };
    const head = "<tr>" + th("period", "\u5468\u671F") + ckSeries.map((s) => th(s.id, s.label)).join("") + "</tr>";
    const dir = ckTableSort.dir === "asc" ? 1 : -1;
    const sorted = [...rows].sort((a, b) => {
      const va = ckTableSort.key === "period" ? a.start : ckVal(a, ckTableSort.key);
      const vb = ckTableSort.key === "period" ? b.start : ckVal(b, ckTableSort.key);
      return (va < vb ? -1 : va > vb ? 1 : 0) * dir;
    });
    const cells = (b) => `<td>${b.title}</td>` + ckSeries.map((s) => `<td class="num" data-v="${ckVal(b, s.id)}">${ckVal(b, s.id)}</td>`).join("");
    const body = rows.length ? sorted.map((b) => `<tr>${cells(b)}</tr>`).join("") : '<tr><td colspan="2" class="rc-empty">\u6682\u65E0\u6570\u636E</td></tr>';
    const total = rows.length ? '<tr class="total"><td>\u5408\u8BA1</td>' + ckSeries.map((s) => `<td class="num">${rows.reduce((sum, b) => sum + ckVal(b, s.id), 0)}</td>`).join("") + "</tr>" : "";
    t.innerHTML = head + body + total;
  }
  var ckTip = null;
  function ckHideTip() {
    if (ckTip) ckTip.style.display = "none";
  }
  function ckShowTip(b, x, y) {
    if (!ckTip) {
      ckTip = document.createElement("div");
      ckTip.className = "rc-tip";
      document.body.append(ckTip);
    }
    let env = "";
    const prev = ckPrevOf?.(b) ?? null;
    if (prev) {
      const cur = ckBucketTotal(b);
      const old = ckBucketTotal(prev);
      if (old > 0 || cur > 0) {
        const p = old > 0 ? Math.round((cur - old) / old * 100) : null;
        env = `<span class="rc-tip-d">\u8F83\u4E0A\u4E00\u671F ${p === null ? "\u2014" : p === 0 ? "\u6301\u5E73" : `${p > 0 ? "+" : "\u2212"}${Math.abs(p)}%`}</span>`;
      }
    }
    const rows = ckSeries.filter((s) => ckVal(b, s.id) > 0).map((s) => `<span style="color:${s.color}">${ckEsc(s.label)} ${ckVal(b, s.id)} \u6B21</span>`).join("");
    ckTip.innerHTML = `<b>${b.title}</b><span>\u89E6\u53D1 ${ckBucketTotal(b)} \u6B21</span>` + (rows || (ckBucketTotal(b) <= 0 ? "<span>\u8BE5\u5468\u671F\u6CA1\u6709\u89E6\u53D1\u8BB0\u5F55</span>" : "")) + env;
    ckTip.style.display = "block";
    const r = ckTip.getBoundingClientRect();
    ckTip.style.left = `${Math.max(8, Math.min(window.innerWidth - r.width - 8, x + 14))}px`;
    ckTip.style.top = `${Math.max(8, Math.min(window.innerHeight - r.height - 8, y - r.height - 12))}px`;
  }
  var ckZoom = null;
  var ckDrag = null;
  var ckDragMoved = false;
  function ckDraw(buckets, animate = false) {
    const box = $("ck-plot");
    const on = ckLegendOn();
    const n = buckets.length;
    if (ckZoom && (ckZoom.total !== n || ckZoom.o >= n)) ckZoom = null;
    const zo = ckZoom ? Math.min(ckZoom.o, Math.max(0, n - ckZoom.n)) : 0;
    const zv = ckZoom ? Math.min(ckZoom.n, n - zo) : n;
    const view = zv === n ? buckets : buckets.slice(zo, zo + zv);
    const iw = RC_W - RC_M.left - RC_M.right;
    const ih = RC_H - RC_M.top - RC_M.bottom;
    const lineParts = ckSeries.filter((s) => on[s.id] !== false);
    const maxCnt = buckets.reduce((mv, b) => Math.max(mv, ckBucketTotal(b)), 0);
    const ax = rcProbeAxis(maxCnt);
    const yC = (v) => RC_M.top + ih * (1 - v / ax.max);
    const step = iw / Math.max(1, zv);
    const showDots = zv <= 60;
    const showPeaks = zv <= 40;
    const defs = `<defs>` + ckSeries.map(
      (s) => `<linearGradient id="ck-a-${s.id}" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="${s.color}" stop-opacity="0.2"/><stop offset="1" stop-color="${s.color}" stop-opacity="0"/></linearGradient>`
    ).join("") + `</defs>`;
    let g = "";
    g += `<line class="rc-axis" x1="${RC_M.left}" y1="${RC_M.top}" x2="${RC_M.left}" y2="${(RC_M.top + ih).toFixed(1)}"/>`;
    for (let v = 0; v <= ax.max; v += ax.step) {
      const yy = yC(v).toFixed(1);
      g += `<line class="grid" x1="${RC_M.left}" y1="${yy}" x2="${RC_W - RC_M.right}" y2="${yy}"/><text class="tick" x="${RC_M.left - 6}" y="${+yy + 4}" text-anchor="end">${v}</text>`;
    }
    g += `<text class="tick rc-cap" x="${RC_M.left - 6}" y="10" text-anchor="end">\u6B21\u6570</text>`;
    const withData = buckets.filter((b) => !ckEmptyBucket(b));
    if (withData.length) {
      const avg = withData.reduce((s, b) => s + ckBucketTotal(b), 0) / withData.length;
      if (avg > 0 && avg < ax.max) {
        const yy = yC(avg).toFixed(1);
        const txt = avg < 10 ? String(Math.round(avg * 10) / 10) : String(Math.round(avg));
        g += `<line class="rc-avgline" x1="${RC_M.left}" y1="${yy}" x2="${RC_W - RC_M.right}" y2="${yy}"/><text class="rc-avglabel" x="${RC_W - RC_M.right - 4}" y="${+yy - 4}" text-anchor="end">\u5E73\u5747 ${txt} \u6B21</text>`;
      }
    }
    for (const s of lineParts) {
      const segs = [];
      let pts = [];
      view.forEach((b, i) => {
        const p = { x: RC_M.left + step * i + step / 2, y: yC(ckVal(b, s.id)), b };
        if (ckEmptyBucket(b)) {
          if (pts.length) segs.push(pts);
          pts = [];
        } else pts.push(p);
      });
      if (pts.length) segs.push(pts);
      const base = yC(0).toFixed(1);
      for (const seg of segs) {
        const d = rcSmoothPath(seg);
        if (d) {
          if (seg.length >= 2) {
            const first = seg[0];
            const last = seg[seg.length - 1];
            g += `<path class="rc-area" d="${d} L ${last.x.toFixed(1)} ${base} L ${first.x.toFixed(1)} ${base} Z" style="fill:url(#ck-a-${s.id})"/>`;
          }
          g += `<path class="rc-line" d="${d}" stroke="${s.color}"${animate ? ' pathLength="1"' : ""}/>`;
          if (showPeaks && seg.length >= 2) {
            const pk = seg.reduce((m, p) => ckVal(p.b, s.id) > ckVal(m.b, s.id) ? p : m, seg[0]);
            if (ckVal(pk.b, s.id) > 0) {
              g += `<text class="rc-peak" x="${pk.x.toFixed(1)}" y="${(pk.y - 7).toFixed(1)}" text-anchor="middle" fill="${s.color}">${ckVal(pk.b, s.id)}</text>`;
            }
          }
        }
        if (showDots) {
          seg.forEach((p) => {
            g += `<circle class="rc-dot" cx="${p.x.toFixed(1)}" cy="${p.y.toFixed(1)}" r="2.6" fill="${s.color}"/>`;
          });
        }
      }
    }
    const labelEvery = zv <= 10 ? 1 : Math.ceil(zv / 10);
    view.forEach((b, i) => {
      if (i % labelEvery === 0) {
        g += `<text class="tick" x="${(RC_M.left + step * i + step / 2).toFixed(1)}" y="${RC_H - 8}" text-anchor="middle">${b.label}</text>`;
      }
    });
    if (view.length === 0 || withData.length === 0) {
      g += `<text class="tick" x="${(RC_M.left + iw / 2).toFixed(1)}" y="${(RC_M.top + ih / 2).toFixed(1)}" text-anchor="middle">\u6682\u65E0\u6570\u636E</text>`;
    }
    g += `<line class="rc-cursor" x1="0" y1="${RC_M.top}" x2="0" y2="${(RC_M.top + ih).toFixed(1)}" style="display:none"/>`;
    g += `<rect class="rc-hit" x="${RC_M.left}" y="${RC_M.top}" width="${iw.toFixed(1)}" height="${ih.toFixed(1)}"/>`;
    box.innerHTML = `<svg viewBox="0 0 ${RC_W} ${RC_H}" role="img" tabindex="0" aria-label="\u81EA\u5B9A\u4E49\u63D0\u9192\u62A5\u8868\u56FE"${animate ? ' class="anim"' : ""}>${defs}${g}</svg>`;
    const svg = box.querySelector("svg");
    if (!svg) return;
    svg.style.cursor = ckZoom ? "grab" : "";
    const cursor = svg.querySelector(".rc-cursor");
    const centers = view.map((b, i) => ({ b, cx: RC_M.left + step * i + step / 2 }));
    let kbX = null;
    const nearest = (xSvg) => {
      if (!centers.length) return null;
      let best = centers[0];
      for (const c of centers) if (Math.abs(c.cx - xSvg) < Math.abs(best.cx - xSvg)) best = c;
      return xSvg < RC_M.left - 6 || xSvg > RC_W - RC_M.right + 6 ? null : best;
    };
    const placeCursor = (c, clientY) => {
      if (!cursor) return;
      const rect = svg.getBoundingClientRect();
      cursor.setAttribute("x1", c.cx.toFixed(1));
      cursor.setAttribute("x2", c.cx.toFixed(1));
      cursor.style.display = "";
      ckShowTip(c.b, rect.left + c.cx / RC_W * rect.width, clientY);
    };
    const xOf = (e) => {
      const rect = svg.getBoundingClientRect();
      return (e.clientX - rect.left) / rect.width * RC_W;
    };
    svg.addEventListener("mousemove", (e) => {
      const best = nearest(xOf(e));
      if (!best || !cursor) {
        if (cursor) cursor.style.display = "none";
        ckHideTip();
        return;
      }
      kbX = best.cx;
      placeCursor(best, e.clientY);
    });
    svg.addEventListener("mouseleave", () => {
      if (cursor) cursor.style.display = "none";
      ckHideTip();
      ckDrag = null;
    });
    svg.addEventListener("wheel", (e) => {
      if (!n) return;
      e.preventDefault();
      const horiz = Math.abs(e.deltaX) > Math.abs(e.deltaY) || e.shiftKey;
      if (horiz && ckZoom) {
        const per = iw / ckZoom.n;
        const o = Math.round(ckZoom.o + (e.deltaX || e.deltaY) / per);
        ckZoom = { ...ckZoom, o: Math.max(0, Math.min(n - ckZoom.n, o)) };
        ckDraw(buckets);
        return;
      }
      const rect = svg.getBoundingClientRect();
      const xSvg = (e.clientX - rect.left) / rect.width * RC_W;
      const anchor = Math.max(0, Math.min(n - 1, Math.floor((xSvg - RC_M.left) / (iw / n))));
      const cur = ckZoom ?? { o: 0, n, total: n };
      let wn = Math.round(cur.n * (e.deltaY < 0 ? 1 / 1.35 : 1.35));
      wn = Math.min(n, Math.max(3, wn));
      if (wn >= n) {
        ckZoom = null;
      } else {
        const o = Math.round(anchor - (anchor - cur.o) * wn / cur.n);
        ckZoom = { o: Math.max(0, Math.min(n - wn, o)), n: wn, total: n };
      }
      ckDraw(buckets);
    });
    svg.addEventListener("mousedown", (e) => {
      ckDrag = ckZoom ? { startX: e.clientX, o0: ckZoom.o } : null;
      ckDragMoved = false;
      if (ckDrag) svg.style.cursor = "grabbing";
    });
    svg.addEventListener("mousemove", (e) => {
      if (!ckDrag || !ckZoom) return;
      if (e.buttons === 0) {
        ckDrag = null;
        svg.style.cursor = "grab";
        return;
      }
      const dx = e.clientX - ckDrag.startX;
      if (!ckDragMoved && Math.abs(dx) < 3) return;
      ckDragMoved = true;
      const o = Math.round(ckDrag.o0 - dx / (iw / ckZoom.n));
      const o2 = Math.max(0, Math.min(n - ckZoom.n, o));
      if (o2 !== ckZoom.o) {
        ckZoom = { ...ckZoom, o: o2 };
        ckDraw(buckets);
      }
    });
    svg.addEventListener("mouseup", () => {
      ckDrag = null;
      svg.style.cursor = ckZoom ? "grab" : "";
    });
    svg.addEventListener("click", (e) => {
      if (ckDragMoved) {
        ckDragMoved = false;
        return;
      }
      const best = nearest(xOf(e));
      if (!best || ckUnit === "day") return;
      e.stopPropagation();
      const rect = svg.getBoundingClientRect();
      ckOpenDetail(best.b, rect.left + best.cx / RC_W * rect.width);
    });
    svg.addEventListener("keydown", (e) => {
      if (e.key === "Escape") {
        if (cursor) cursor.style.display = "none";
        ckHideTip();
        return;
      }
      if (e.key !== "ArrowLeft" && e.key !== "ArrowRight") return;
      e.preventDefault();
      if (!centers.length || !cursor) return;
      let idx = 0;
      if (kbX !== null) {
        idx = centers.reduce((bi, c, i) => Math.abs(c.cx - kbX) < Math.abs(centers[bi].cx - kbX) ? i : bi, 0);
        idx = e.key === "ArrowRight" ? Math.min(centers.length - 1, idx + 1) : Math.max(0, idx - 1);
      }
      kbX = centers[idx].cx;
      const rect = svg.getBoundingClientRect();
      placeCursor(centers[idx], rect.top + (RC_M.top + ih / 2) * (rect.height / RC_H));
    });
  }
  var ckDetailEl = null;
  function ckCloseDetail() {
    if (ckDetailEl) ckDetailEl.classList.remove("open");
  }
  function ckOpenDetail(b, clientX) {
    const host = document.getElementById("custom-chart");
    if (!host) return;
    if (!ckDetailEl) {
      ckDetailEl = document.createElement("div");
      ckDetailEl.className = "rc-detail";
      host.append(ckDetailEl);
    }
    const days = ckLastDays.filter((d) => d.date >= b.start && d.date <= b.end).sort((x, y) => x.date < y.date ? -1 : 1);
    const span = Math.max(1, ckSeries.length + 1);
    const rows = days.map(
      (d) => `<tr><td>${d.date}</td>` + ckSeries.map((s) => `<td>${d.customCounts?.[s.id] ?? 0}</td>`).join("") + "</tr>"
    ).join("");
    ckDetailEl.innerHTML = `<div class="rc-detail-head"><b>${b.title}</b><button type="button" class="rc-detail-x" title="\u5173\u95ED">\u2715</button></div><div class="rc-detail-scroll"><table class="stats-table"><tr><th>\u65E5\u671F</th>${ckSeries.map((s) => `<th>${ckEsc(s.label)}</th>`).join("")}</tr>` + (rows || `<tr><td colspan="${span}" class="rc-empty">\u8BE5\u5468\u671F\u6CA1\u6709\u9010\u65E5\u6570\u636E</td></tr>`) + "</table></div>";
    ckDetailEl.classList.add("open");
    ckDetailEl.querySelector(".rc-detail-x")?.addEventListener("click", ckCloseDetail);
    const hr = host.getBoundingClientRect();
    ckDetailEl.style.left = `${Math.max(8, Math.min(hr.width - 292, clientX - hr.left - 140))}px`;
    ckDetailEl.style.top = "44px";
    ckHideTip();
  }
  function ckRenderControls() {
    $("ck-units").innerHTML = RC_UNITS.map(
      (x) => `<button type="button" class="rc-unit${x.u === ckUnit ? " on" : ""}" data-u="${x.u}">${x.label}</button>`
    ).join("");
    ckRenderRange();
  }
  function ckRenderRange() {
    const box = $("ck-range");
    const r = ckGetRange(ckUnit);
    box.innerHTML = `<button type="button" class="rc-range-btn" id="ck-range-btn" title="\u9009\u62E9\u65F6\u95F4\u8303\u56F4"><span id="ck-range-label">${rcRangeLabel(ckUnit, r)}</span><span class="rc-caret">\u25BE</span></button><div class="rc-pop">${rcPickerHtml(ckUnit, r).replaceAll('id="rc-rs"', 'id="ck-rs"').replaceAll('id="rc-re"', 'id="ck-re"')}<div class="rc-pop-foot"><button type="button" class="btn ghost" id="ck-range-all" title="\u4ECE\u6700\u65E9\u8BB0\u5F55\u4E00\u76F4\u770B\u5230\u5F53\u524D\u5468\u671F">\u5168\u90E8\u6570\u636E</button><button type="button" class="btn ghost" id="ck-range-reset">\u6062\u590D\u9ED8\u8BA4</button></div></div>`;
    $("ck-range-btn").addEventListener("click", () => box.classList.toggle("open"));
    $("ck-range-all").addEventListener("click", () => {
      const all = rcAllRange(ckUnit);
      if (!all) {
        toast("\u8FD8\u6CA1\u6709\u7EDF\u8BA1\u6570\u636E");
        return;
      }
      ckSetRange(ckUnit, all);
      box.classList.remove("open");
      ckRenderControls();
      void renderCustomReportChart(true);
    });
    $("ck-range-reset").addEventListener("click", () => {
      ckResetRange(ckUnit);
      ckRenderControls();
      void renderCustomReportChart(true);
    });
    const startCtl = box.querySelector("#ck-rs");
    const endCtl = box.querySelector("#ck-re");
    const apply = () => {
      if (!startCtl || !endCtl) return;
      ckSetRange(ckUnit, { start: startCtl.value, end: endCtl.value });
      const label = $("ck-range-label");
      if (label) label.textContent = rcRangeLabel(ckUnit, ckGetRange(ckUnit));
      void renderCustomReportChart(true);
    };
    startCtl?.addEventListener("change", apply);
    endCtl?.addEventListener("change", apply);
  }
  function ckExportName(ext) {
    const ul = RC_UNITS.find((x) => x.u === ckUnit)?.label ?? ckUnit;
    const fp = rcRangeFilePart(ckUnit, ckGetRange(ckUnit));
    return `\u81EA\u5B9A\u4E49\u63D0\u9192\u62A5\u8868_${ul}_${fp.s}_${fp.e}.${ext}`;
  }
  function setupCustomReportChart() {
    $("ck-units").addEventListener("click", (e) => {
      const b = e.target.closest(".rc-unit");
      if (!b) return;
      ckUnit = b.dataset.u;
      ckSaveStr("unit", ckUnit);
      ckRenderControls();
      void renderCustomReportChart(true);
    });
    $("ck-legend").addEventListener("click", (e) => {
      const b = e.target.closest(".rc-legend-item");
      if (!b) return;
      const on = ckLegendOn();
      const id = b.dataset.s ?? "";
      on[id] = on[id] === false;
      ckSaveStr("on", JSON.stringify(on));
      ckRenderLegend();
      void renderCustomReportChart();
    });
    $("ck-legend").addEventListener("dblclick", (e) => {
      const b = e.target.closest(".rc-legend-item");
      if (!b) return;
      const id = b.dataset.s ?? "";
      const on = ckLegendOn();
      const only = ckSeries.every((s) => on[s.id] !== false === (s.id === id));
      const next = {};
      if (only) for (const s of ckSeries) next[s.id] = true;
      else {
        for (const s of ckSeries) next[s.id] = false;
        next[id] = true;
      }
      ckSaveStr("on", JSON.stringify(next));
      ckRenderLegend();
      void renderCustomReportChart();
    });
    const emptyBtn = $("ck-empty-btn");
    emptyBtn.classList.toggle("on", ckShowEmpty);
    emptyBtn.addEventListener("click", () => {
      ckShowEmpty = !ckShowEmpty;
      ckSaveStr("empty", ckShowEmpty ? "1" : "0");
      emptyBtn.classList.toggle("on", ckShowEmpty);
      void renderCustomReportChart();
    });
    $("ck-exp-csv").addEventListener("click", () => {
      const rows = [...ckTableData].sort((a2, b) => a2.start < b.start ? -1 : 1);
      const q = (v) => `"${String(v).replace(/"/g, '""')}"`;
      const lines = [["\u5468\u671F", "\u5F00\u59CB", "\u7ED3\u675F", ...ckSeries.map((s) => s.label)].map(q).join(",")];
      for (const b of rows) {
        lines.push([b.title, b.start, b.end, ...ckSeries.map((s) => ckVal(b, s.id))].map(q).join(","));
      }
      if (rows.length) {
        lines.push(
          ["\u5408\u8BA1", "", "", ...ckSeries.map((s) => rows.reduce((sum, b) => sum + ckVal(b, s.id), 0))].map(q).join(",")
        );
      }
      void saveExportFile(ckExportName("csv"), new Blob(["\uFEFF" + lines.join("\r\n")], { type: "text/csv;charset=utf-8" }));
    });
    $("ck-exp-png").addEventListener("click", () => {
      const svg = document.querySelector("#ck-plot svg");
      if (!svg) return;
      try {
        const clone = svg.cloneNode(true);
        clone.setAttribute("xmlns", "http://www.w3.org/2000/svg");
        const cs = getComputedStyle(document.querySelector(".rest-chart") ?? document.body);
        const v = (name, fb) => cs.getPropertyValue(name).trim() || fb;
        const st = document.createElementNS("http://www.w3.org/2000/svg", "style");
        st.textContent = `text{fill:${v("--text-2", "#888888")};font-size:11px;font-variant-numeric:tabular-nums;font-family:${cs.fontFamily};}.rc-cap{font-weight:600;}.grid{stroke:${v("--border", "#dddddd")};stroke-width:1;}.rc-axis{stroke:${v("--text-2", "#888888")};stroke-opacity:0.5;}.rc-avgline{stroke:${v("--text-2", "#888888")};stroke-opacity:0.6;stroke-dasharray:5 4;}.rc-avglabel{fill:${v("--text", "#333333")};font-size:11px;font-weight:600;}.rc-peak{font-size:10px;font-weight:600;}path.rc-line{fill:none;stroke-width:2;}.rc-cursor{display:none;}`;
        clone.insertBefore(st, clone.firstChild);
        const xml = new XMLSerializer().serializeToString(clone);
        const img = new Image();
        img.onload = () => {
          try {
            const cv = document.createElement("canvas");
            cv.width = RC_W * 2;
            cv.height = RC_H * 2;
            const ctx = cv.getContext("2d");
            if (!ctx) return;
            ctx.fillStyle = v("--card", "#ffffff");
            ctx.fillRect(0, 0, cv.width, cv.height);
            ctx.drawImage(img, 0, 0, cv.width, cv.height);
            cv.toBlob((blob) => {
              if (!blob) return;
              void saveExportFile(ckExportName("png"), blob);
            }, "image/png");
          } catch {
            toast("\u5BFC\u51FA\u56FE\u7247\u5931\u8D25");
          }
        };
        img.src = "data:image/svg+xml;charset=utf-8," + encodeURIComponent(xml);
      } catch {
        toast("\u5BFC\u51FA\u56FE\u7247\u5931\u8D25");
      }
    });
    $("ck-table").addEventListener("click", (e) => {
      const th = e.target.closest("th.sortable");
      if (!th) return;
      const key = th.dataset.key ?? "period";
      if (ckTableSort.key === key) ckTableSort.dir = ckTableSort.dir === "asc" ? "desc" : "asc";
      else ckTableSort = { key, dir: "desc" };
      ckRenderTable(ckTableData);
    });
    ckRenderControls();
    void rcGetEarliest().then((e) => {
      if (e) ckRenderControls();
    });
  }
  var runningApps = [];
  var APP_NAME_MIN_W = 72;
  var APP_NAME_MAX_W = 190;
  var nameMeasure = document.createElement("canvas").getContext("2d");
  function fitNameInput(input) {
    const cs = getComputedStyle(input);
    const pad = parseFloat(cs.paddingLeft) + parseFloat(cs.paddingRight) + parseFloat(cs.borderLeftWidth) + parseFloat(cs.borderRightWidth);
    const ref = input.value || input.placeholder || "";
    let w = ref.length * 8;
    if (nameMeasure) {
      nameMeasure.font = `${cs.fontStyle} ${cs.fontWeight} ${cs.fontSize} ${cs.fontFamily}`;
      w = nameMeasure.measureText(ref).width;
    }
    const px = Math.ceil(w + pad + 8);
    input.style.width = `${Math.min(APP_NAME_MAX_W, Math.max(APP_NAME_MIN_W, px))}px`;
  }
  function buildAppRow(item) {
    const row = document.createElement("div");
    row.className = "tk-item" + (item.enabled ? "" : " disabled");
    row.dataset.appId = item.id;
    const on = document.createElement("input");
    on.type = "checkbox";
    on.checked = item.enabled;
    on.title = "\u542F\u7528 / \u505C\u7528\u8FD9\u6761\u89C4\u5219";
    on.addEventListener("change", () => {
      item.enabled = on.checked;
      row.classList.toggle("disabled", !on.checked);
      autoSave();
    });
    const exe = document.createElement("input");
    exe.type = "text";
    exe.className = "app-exe";
    exe.value = item.exe;
    exe.placeholder = "\u53EF\u6267\u884C\u6587\u4EF6\u540D\uFF0C\u5982 Code.exe";
    exe.title = "\u53EF\u6267\u884C\u6587\u4EF6\u540D\uFF1A\u5FFD\u7565\u8DEF\u5F84\u3001\u4E0D\u533A\u5206\u5927\u5C0F\u5199\uFF0C\u7528\u5B83\u8DDF\u6B63\u5728\u8FD0\u884C\u7684\u7A0B\u5E8F\u6BD4\u5BF9";
    const name = document.createElement("input");
    name.type = "text";
    name.className = "app-name";
    name.value = item.name;
    name.maxLength = APP_NAME_MAX;
    name.placeholder = appPauseLabel(item.exe) || "\u8F6F\u4EF6\u540D\uFF08\u53EF\u9009\uFF09";
    name.title = "\u8F6F\u4EF6\u540D\uFF1A\u53EA\u5F71\u54CD\u663E\u793A\uFF0C\u53EF\u7559\u7A7A\uFF1B\u81EA\u5DF1\u6539\u8FC7\u4E4B\u540E\u5C31\u4E0D\u4F1A\u518D\u88AB\u81EA\u52A8\u6539\u540D\u8986\u76D6";
    name.addEventListener("input", () => fitNameInput(name));
    name.addEventListener("change", () => {
      item.name = normalizeAppName(name.value);
      name.value = item.name;
      fitNameInput(name);
      autoSave();
    });
    exe.addEventListener("change", () => {
      const v = exeBasename(exe.value);
      const clash = !v || S.appPause.items.some((i) => i !== item && i.exe.toLowerCase() === v.toLowerCase());
      if (clash) {
        toast(v ? `${v} \u5DF2\u5728\u5217\u8868\u4E2D` : "\u53EF\u6267\u884C\u6587\u4EF6\u540D\u4E0D\u80FD\u4E3A\u7A7A");
        exe.value = item.exe;
        return;
      }
      const from = item.exe;
      item.exe = v;
      exe.value = v;
      item.name = syncAppName(item.name, from, v);
      name.value = item.name;
      name.placeholder = appPauseLabel(v) || "\u8F6F\u4EF6\u540D\uFF08\u53EF\u9009\uFF09";
      fitNameInput(name);
      autoSave();
    });
    const del = mkBtn("\u2715", "\u5220\u9664\u8FD9\u6761\u89C4\u5219", () => {
      void confirmDialog({
        title: "\u5220\u9664\u8FD9\u6761\u5E94\u7528\u89C4\u5219\uFF1F",
        desc: "\u5220\u9664\u540E\u7ACB\u5373\u751F\u6548\uFF0C\u65E0\u6CD5\u64A4\u9500\u3002",
        detail: item.name ? `${item.name}\uFF08${item.exe}\uFF09` : item.exe,
        okText: "\u5220\u9664"
      }).then((ok) => {
        if (!ok) return;
        S.appPause.items = S.appPause.items.filter((i) => i.id !== item.id);
        renderAppList();
        autoSave();
      });
    });
    del.classList.add("danger");
    row.append(on, exe, name, del);
    return row;
  }
  function renderAppList() {
    const list = $("app-pause-list");
    if (!list || !S.appPause) return;
    const sum = document.getElementById("app-advanced-summary");
    if (sum) sum.textContent = S.appPause.items.length > 0 ? `${S.appPause.items.length} \u9879` : "\u7A7A";
    list.innerHTML = "";
    if (S.appPause.items.length === 0) {
      const empty = document.createElement("div");
      empty.className = "tk-empty";
      empty.textContent = "\u8FD8\u6CA1\u6709\u6307\u5B9A\u5E94\u7528\uFF0C\u70B9\u4E0A\u65B9\u6309\u94AE\u6DFB\u52A0";
      list.appendChild(empty);
      return;
    }
    for (const item of S.appPause.items) list.appendChild(buildAppRow(item));
    for (const el of list.querySelectorAll(".app-name")) fitNameInput(el);
  }
  function focusAppName(id) {
    const row = Array.from(document.querySelectorAll("#app-pause-list .tk-item")).find(
      (r) => r.dataset.appId === id
    );
    const input = row ? row.querySelector(".app-name") : null;
    if (!input) return;
    input.focus();
    input.select();
  }
  function addApps(names) {
    if (!S.appPause) return [];
    const have = new Set(S.appPause.items.map((i) => i.exe.toLowerCase()));
    const added = [];
    for (const raw of names) {
      const exe = exeBasename(raw);
      const key = exe.toLowerCase();
      if (!exe || have.has(key)) continue;
      have.add(key);
      const item = { id: crypto.randomUUID(), exe, name: appPauseLabel(exe), enabled: true };
      S.appPause.items.push(item);
      added.push(item);
    }
    if (added.length > 0) {
      renderAppList();
      autoSave();
    }
    return added;
  }
  function renderAppPickItems(filter) {
    const list = $("app-pick-list");
    if (!list) return;
    const kw = filter.trim().toLowerCase();
    const items = S.appPause ? S.appPause.items : [];
    const have = new Set(items.map((i) => i.exe.toLowerCase()));
    const names = runningApps.filter((n) => !kw || n.toLowerCase().includes(kw));
    list.innerHTML = "";
    if (names.length === 0) {
      const d = document.createElement("div");
      d.className = "tk-empty";
      d.textContent = runningApps.length === 0 ? "\u6CA1\u6709\u8BFB\u5230\u6B63\u5728\u8FD0\u884C\u7684\u7A0B\u5E8F" : "\u6CA1\u6709\u5339\u914D\u7684\u7A0B\u5E8F";
      list.appendChild(d);
      return;
    }
    for (const name of names) {
      const dup = have.has(name.toLowerCase());
      const row = document.createElement("div");
      row.className = "pk-item" + (dup ? " disabled" : "");
      const box = document.createElement("input");
      box.type = "checkbox";
      box.value = name;
      box.disabled = dup;
      const txt = document.createElement("span");
      txt.textContent = name;
      row.append(box, txt);
      const presetLabel = appPauseLabel(name);
      if (presetLabel) {
        const tag = document.createElement("span");
        tag.className = "app-alias";
        tag.textContent = presetLabel;
        row.appendChild(tag);
      }
      if (dup) {
        const tag = document.createElement("em");
        tag.textContent = "\u5DF2\u5728\u5217\u8868";
        row.appendChild(tag);
      }
      row.addEventListener("click", (e) => {
        if (box.disabled || e.target === box) return;
        box.checked = !box.checked;
      });
      list.appendChild(row);
    }
  }
  function openAppPickModal() {
    const modal = $("app-pick-modal");
    const list = $("app-pick-list");
    const note = $("app-pick-note");
    const filter = $("app-pick-filter");
    if (!modal || !list || !note || !filter) return;
    filter.value = "";
    list.innerHTML = "";
    note.textContent = "\u6B63\u5728\u8BFB\u53D6\u6B63\u5728\u8FD0\u884C\u7684\u7A0B\u5E8F\u2026";
    modal.classList.remove("hidden");
    void window.api.listRunningApps().then((r) => {
      if (modal.classList.contains("hidden")) return;
      if (!r || !r.supported) {
        note.textContent = r && r.detail || "\u5F53\u524D\u5E73\u53F0\u4E0D\u652F\u6301\u8BFB\u53D6\u7A0B\u5E8F\u5217\u8868\uFF0C\u8BF7\u6539\u7528\u300C\u9009\u62E9\u7A0B\u5E8F\u6587\u4EF6\u300D\u6216\u300C\u624B\u52A8\u6DFB\u52A0\u300D\u3002";
        return;
      }
      runningApps = r.names;
      note.textContent = `\u5171 ${runningApps.length} \u4E2A\u6B63\u5728\u8FD0\u884C\u7684\u7A0B\u5E8F\uFF0C\u52FE\u9009\u540E\u70B9\u300C\u6DFB\u52A0\u300D`;
      renderAppPickItems("");
      filter.focus();
    });
  }
  function setupAppPause() {
    const modal = $("app-pick-modal");
    $("app-pause-add").addEventListener("click", () => {
      void promptDialog({
        title: "\u624B\u52A8\u6DFB\u52A0\u5E94\u7528",
        desc: "\u586B\u53EF\u6267\u884C\u6587\u4EF6\u540D\uFF0C\u4F8B\u5982 chrome.exe\uFF1B\u4EFB\u52A1\u7BA1\u7406\u5668\u300C\u8BE6\u7EC6\u4FE1\u606F\u300D\u91CC\u80FD\u770B\u5230\u3002\u6DFB\u52A0\u540E\u5C31\u80FD\u7ED9\u5B83\u8D77\u4E2A\u540D\u5B57\u3002",
        placeholder: "\u4F8B\u5982 chrome.exe",
        okText: "\u6DFB\u52A0"
      }).then((v) => {
        if (v === null) return;
        const added = addApps([v]);
        if (added.length === 0) {
          toast("\u6CA1\u6709\u65B0\u589E\uFF08\u540D\u79F0\u4E3A\u7A7A\u6216\u5DF2\u5728\u5217\u8868\u4E2D\uFF09");
          return;
        }
        toast(`\u5DF2\u6DFB\u52A0 ${added[0].exe}`);
        focusAppName(added[0].id);
      });
    });
    $("app-pause-file").addEventListener("click", async () => {
      const r = await window.api.pickAppExe();
      if (!r) return;
      const added = addApps([r.exe]);
      if (added.length === 0) {
        toast(`${r.exe} \u5DF2\u5728\u5217\u8868\u4E2D`);
        return;
      }
      toast(`\u5DF2\u6DFB\u52A0 ${added[0].exe}`);
      focusAppName(added[0].id);
    });
    $("app-pause-pick").addEventListener("click", () => openAppPickModal());
    $("app-pick-cancel").addEventListener("click", () => modal.classList.add("hidden"));
    modal.addEventListener("click", (e) => {
      if (e.target === modal) modal.classList.add("hidden");
    });
    $("app-pick-filter").addEventListener("input", (e) => {
      renderAppPickItems(e.target.value);
    });
    $("app-pick-ok").addEventListener("click", () => {
      const names = Array.from(
        $("app-pick-list").querySelectorAll("input[type=checkbox]:checked")
      ).map((b) => b.value);
      modal.classList.add("hidden");
      const added = addApps(names);
      toast(added.length > 0 ? `\u5DF2\u6DFB\u52A0 ${added.length} \u4E2A\u5E94\u7528` : "\u6CA1\u6709\u65B0\u589E\uFF08\u672A\u52FE\u9009\u6216\u90FD\u5DF2\u5728\u5217\u8868\u4E2D\uFF09");
    });
    document.addEventListener("keydown", (e) => {
      if (modal.classList.contains("hidden")) return;
      if (e.key === "Escape") {
        e.preventDefault();
        modal.classList.add("hidden");
      }
    });
  }
  var ACCENT_PRESETS = [
    { hex: "#4f7cff", name: "\u7ECF\u5178\u84DD" },
    { hex: "#2d9cdb", name: "\u5929\u9752" },
    { hex: "#12a594", name: "\u677E\u77F3\u7EFF" },
    { hex: "#2e9e5b", name: "\u68EE\u6797\u7EFF" },
    { hex: "#77a82c", name: "\u6A44\u6984\u7EFF" },
    { hex: "#e0a020", name: "\u7425\u73C0" },
    { hex: "#ed7a35", name: "\u843D\u65E5\u6A58" },
    { hex: "#e2544c", name: "\u6731\u7802\u7EA2" },
    { hex: "#d8478f", name: "\u73AB\u7EA2" },
    { hex: "#8b5cf6", name: "\u7D2B\u7F57\u5170" },
    { hex: "#9a6a4f", name: "\u6469\u5361\u68D5" },
    { hex: "#5a6474", name: "\u77F3\u58A8\u7070" }
  ];
  var DEFAULT_ACCENT = "#4f7cff";
  function normalizeHex(raw) {
    const s = String(raw ?? "").trim().replace(/^#/, "");
    if (/^[0-9a-f]{3}$/i.test(s)) return `#${s[0]}${s[0]}${s[1]}${s[1]}${s[2]}${s[2]}`.toLowerCase();
    if (/^[0-9a-f]{6}$/i.test(s)) return `#${s}`.toLowerCase();
    return null;
  }
  function hexToRgb(hex) {
    const h = hex.replace("#", "");
    return [parseInt(h.slice(0, 2), 16), parseInt(h.slice(2, 4), 16), parseInt(h.slice(4, 6), 16)];
  }
  function rgbToHex(r, g, b) {
    const c = (n) => Math.round(Math.min(255, Math.max(0, n))).toString(16).padStart(2, "0");
    return `#${c(r)}${c(g)}${c(b)}`;
  }
  function rgbToHsv(r, g, b) {
    const rr = r / 255;
    const gg = g / 255;
    const bb = b / 255;
    const max = Math.max(rr, gg, bb);
    const min = Math.min(rr, gg, bb);
    const d = max - min;
    let h = 0;
    if (d > 0) {
      if (max === rr) h = (gg - bb) / d % 6;
      else if (max === gg) h = (bb - rr) / d + 2;
      else h = (rr - gg) / d + 4;
      h *= 60;
      if (h < 0) h += 360;
    }
    return { h, s: max === 0 ? 0 : d / max, v: max };
  }
  function hsvToRgb(h, s, v) {
    const hh = (h % 360 + 360) % 360 / 60;
    const c = v * s;
    const x = c * (1 - Math.abs(hh % 2 - 1));
    const m = v - c;
    let rgb;
    if (hh < 1) rgb = [c, x, 0];
    else if (hh < 2) rgb = [x, c, 0];
    else if (hh < 3) rgb = [0, c, x];
    else if (hh < 4) rgb = [0, x, c];
    else if (hh < 5) rgb = [x, 0, c];
    else rgb = [c, 0, x];
    return [(rgb[0] + m) * 255, (rgb[1] + m) * 255, (rgb[2] + m) * 255];
  }
  var cpHex = DEFAULT_ACCENT;
  var cpH = 220;
  var cpS = 1;
  var cpV = 1;
  function setColorChip(hex) {
    $("ap-color-open").style.background = hex;
  }
  function cpRender(writeHex = true) {
    $("cp-pad").style.background = `linear-gradient(to top, #000000, rgba(0, 0, 0, 0)), linear-gradient(to right, #ffffff, rgba(255, 255, 255, 0)), hsl(${Math.round(cpH)}, 100%, 50%)`;
    $("cp-pad-thumb").style.left = `${cpS * 100}%`;
    $("cp-pad-thumb").style.top = `${(1 - cpV) * 100}%`;
    $("cp-hue-thumb").style.left = `${cpH / 360 * 100}%`;
    $("cp-preview").style.background = cpHex;
    if (writeHex) $("cp-hex").value = cpHex;
    for (const sw of document.querySelectorAll("#color-presets .cp-sw")) {
      sw.classList.toggle("on", sw.dataset.hex === cpHex);
    }
  }
  function cpSetColor(hex, syncHsv, writeHex = true) {
    const n = normalizeHex(hex);
    if (!n) return false;
    cpHex = n;
    if (syncHsv) {
      const [r, g, b] = hexToRgb(n);
      const hsv = rgbToHsv(r, g, b);
      cpH = hsv.h;
      cpS = hsv.s;
      cpV = hsv.v;
    }
    cpRender(writeHex);
    return true;
  }
  function cpPreviewOnly() {
    S.appearance.color = cpHex;
    setColorChip(cpHex);
    if (document.getElementById("sup-preview")) renderSupervisorPreview(curSupStage);
  }
  function cpCommit() {
    S.appearance.color = cpHex;
    setColorChip(cpHex);
    autoSave();
  }
  function cpFromHsv(live) {
    const [r, g, b] = hsvToRgb(cpH, cpS, cpV);
    if (!cpSetColor(rgbToHex(r, g, b), false)) return;
    if (live) cpPreviewOnly();
    else cpCommit();
  }
  function cpFlushHex() {
    const el = $("cp-hex");
    const n = normalizeHex(el.value);
    if (!n) {
      el.value = cpHex;
      return;
    }
    if (n !== cpHex) {
      cpSetColor(n, true);
      cpCommit();
      return;
    }
    el.value = cpHex;
  }
  function resetAccentColor() {
    cpSetColor(DEFAULT_ACCENT, true);
    cpCommit();
    toast("\u5DF2\u6062\u590D\u9ED8\u8BA4\u989C\u8272");
  }
  function closeColorPicker() {
    cpFlushHex();
    $("color-modal").classList.add("hidden");
  }
  function bindDragArea(el, onMove, onEnd) {
    const at = (e) => {
      const r = el.getBoundingClientRect();
      if (r.width <= 0 || r.height <= 0) return [0, 0];
      return [
        Math.min(1, Math.max(0, (e.clientX - r.left) / r.width)),
        Math.min(1, Math.max(0, (e.clientY - r.top) / r.height))
      ];
    };
    el.addEventListener("pointerdown", (e) => {
      e.preventDefault();
      const [nx, ny] = at(e);
      onMove(nx, ny);
      const move = (ev) => {
        const [mx, my] = at(ev);
        onMove(mx, my);
      };
      const up = () => {
        window.removeEventListener("pointermove", move);
        window.removeEventListener("pointerup", up);
        if (onEnd) onEnd();
      };
      window.addEventListener("pointermove", move);
      window.addEventListener("pointerup", up);
    });
  }
  function buildPresetSwatches() {
    const box = $("color-presets");
    if (box.childElementCount > 0) return;
    for (const p of ACCENT_PRESETS) {
      const b = document.createElement("button");
      b.type = "button";
      b.className = "cp-sw";
      b.dataset.hex = p.hex;
      b.title = `${p.name} ${p.hex}`;
      b.setAttribute("aria-label", p.name);
      b.style.background = p.hex;
      box.appendChild(b);
    }
  }
  function setupColorPicker() {
    buildPresetSwatches();
    setColorChip(normalizeHex(S.appearance.color) ?? DEFAULT_ACCENT);
    $("ap-color-open").addEventListener("click", () => {
      cpSetColor(normalizeHex(S.appearance.color) ?? DEFAULT_ACCENT, true);
      $("color-modal").classList.remove("hidden");
      $("cp-done").focus();
    });
    $("cp-done").addEventListener("click", closeColorPicker);
    $("color-modal").addEventListener("click", (e) => {
      if (e.target === $("color-modal")) closeColorPicker();
    });
    document.addEventListener("keydown", (e) => {
      if ($("color-modal").classList.contains("hidden")) return;
      if (e.key === "Escape") {
        e.preventDefault();
        closeColorPicker();
      }
    });
    $("color-presets").addEventListener("click", (e) => {
      const sw = e.target.closest(".cp-sw");
      if (sw && sw.dataset.hex && cpSetColor(sw.dataset.hex, true)) cpCommit();
    });
    bindDragArea(
      $("cp-pad"),
      (nx, ny) => {
        cpS = nx;
        cpV = 1 - ny;
        cpFromHsv(true);
      },
      () => cpFromHsv(false)
    );
    bindDragArea(
      $("cp-hue"),
      (nx) => {
        cpH = nx * 360;
        cpFromHsv(true);
      },
      () => cpFromHsv(false)
    );
    const hexIn = $("cp-hex");
    hexIn.addEventListener("input", () => {
      const n = normalizeHex(hexIn.value);
      if (n && cpSetColor(n, true, false)) cpCommit();
    });
    hexIn.addEventListener("change", cpFlushHex);
    hexIn.addEventListener("keydown", (e) => {
      if (e.key === "Enter") {
        e.preventDefault();
        cpFlushHex();
      }
    });
    $("cp-reset").addEventListener("click", resetAccentColor);
  }
  async function autoSave() {
    collectForm();
    await window.api.saveSettings(S);
    if (supEditKey) persistRole(supEditKey);
    applyTheme();
    if (supPreview !== null || document.getElementById("sup-preview")) renderSupervisorPreview(curSupStage);
  }
  // 新增 / 删除提醒时间后，立即向设置页重拉一次状态：刷新「今日提醒时间线」与
  // 侧栏「下次外卖提醒 / 自定义提醒」迷你卡。后端 saveSettings 已会重算并推送，
  // 这里再拉一次是为消除保存往返带来的可见延迟，确保「立即」反映。
  async function refreshStatusNow() {
    try {
      const raw = await window.api.getState();
      renderStatus(raw);
    } catch (e) {
      console.error("[refreshStatusNow] 拉取状态失败", e);
    }
  }
  function bindAutoSave() {
    for (const el of document.querySelectorAll(".tab input, .tab select")) {
      if (el.closest("#tab-stats")) continue;
      bindAutoSaveEl(el);
    }
  }
  // 单个控件补绑自动保存；dataset 标记防重复绑定（动态创建的卡片会和这里的
  // 全量扫描先后碰到同一批控件）。change 而非 input：与设置页其余保存口径一致。
  function bindAutoSaveEl(el) {
    if (el.dataset.wrAutosave) return;
    el.dataset.wrAutosave = "1";
    el.addEventListener("change", autoSave);
  }
  async function init() {
    S = await window.api.getSettings();
    applyTheme();
    for (const btn of document.querySelectorAll(".nav-item")) {
      btn.addEventListener("click", () => {
        document.querySelectorAll(".nav-item").forEach((b) => b.classList.remove("active"));
        document.querySelectorAll(".tab").forEach((t) => t.classList.remove("active"));
        btn.classList.add("active");
        $(`tab-${btn.dataset.tab}`).classList.add("active");
        if (btn.dataset.tab === "stats") {
          void renderStats();
        }
      });
    }
    renderCustomCards();
    setupCardFolds();
    setupRestChart();
    setupTakeoutChart();
    setupCustomReportChart();
    fillForm();
    ROLES = await window.api.listSupervisorRoles();
    ensureSupEditKey();
    fillSupervisorImageName();
    fillGeomControls(normalizeSupGeom(geomOf(currentGeomKey())));
    setupShortcutCapture();
    BUILTIN_MSGS = await window.api.builtinMsgDefaults();
    setupMsgPanels();
    renderTakeoutList();
    setupAppPause();
    setupColorPicker();
    bindAutoSave();
    window.addEventListener("focus", () => {
      if (document.querySelector("#tab-stats.active")) void renderStats();
    });
    bindSoundPlayer();
    $("snd-music-toggle").addEventListener("click", () => {
      const body = $("snd-music-body");
      const open = body.classList.toggle("hidden") === false;
      $("snd-music-panel").classList.toggle("open", open);
    });
    $("snd-mode").addEventListener("click", () => {
      const cur = $("snd-mode").dataset.mode || "single";
      S.sound.mode = SND_MODE_NEXT[cur] || "single";
      setSoundModeUI(S.sound.mode);
      renderSoundMusic();
      autoSave();
    });
    $("snd-music-add").addEventListener("click", async () => {
      const r = await window.api.addMusicSound();
      if (r.canceled) return;
      if (!r.items || r.items.length === 0) {
        toast(r.error ?? "\u6DFB\u52A0\u5931\u8D25");
        return;
      }
      for (const it of r.items) {
        S.sound.music.push({
          id: `sm-${crypto.randomUUID()}`,
          file: it.file,
          name: it.name,
          enabled: true
        });
      }
      renderSoundMusic();
      autoSave();
      toast(r.items.length > 1 ? `\u5DF2\u6DFB\u52A0 ${r.items.length} \u9996\u97F3\u4E50` : "\u5DF2\u6DFB\u52A0\u5230\u97F3\u4E50\u5217\u8868");
    });
    setupGalleryPanel();
    void renderRoleGallery();
    const supSelBtn = $("sup-selection-btn");
    supSelBtn.addEventListener("click", () => {
      const cur = supSelBtn.dataset.mode || S.supervisor.selection || "fixed";
      const next = DISPLAY_MODE_NEXT[cur] || "fixed";
      S.supervisor.selection = next;
      applyModeIcon(supSelBtn, next);
      autoSave();
      void renderRoleGallery();
    });
    $("sup-gallery-add").addEventListener("click", () => void addCustomRole());
    bindRoleMenuDismiss();
    setInterval(() => {
      renderSupervisorPreview(curSupStage === 1 ? 2 : 1);
    }, 5e3);
    $("sup-enabled").addEventListener("change", () => {
      // 实时同步表单态，避免依赖 saveSettings 的落盘时机——勾选/取消立刻反映到预览
      S.supervisor.enabled = $("sup-enabled").checked;
      renderSupervisorPreview(curSupStage);
    });
    // 督休专员卡的「预览」：先等配置落盘（含「启用」勾选），再弹真实预览窗——
    // 这样预览窗能如实反映当前「启用」状态（Go 侧总开关现在始终生效）
    $("sup-break-preview").addEventListener("click", async () => {
      await autoSave();
      await window.api.previewBreak("short", supEditKey);
    });
    for (const id of ["sup-size", "sup-posx", "sup-posy"]) {
      $(id).addEventListener("input", () => {
        const g = readGeomFromForm();
        applySupGeometry(g);
        commitGeomToRole(g);
      });
    }
    $("sup-flip").addEventListener("change", () => {
      const g = readGeomFromForm();
      applySupGeometry(g);
      commitGeomToRole(g);
      autoSave();
    });
    $("sup-geom-reset").addEventListener("click", () => {
      const g = { ...SUP_GEOM_DEFAULT };
      commitGeomToRole(g);
      fillGeomControls(g);
      autoSave();
      toast("\u5DF2\u6062\u590D\u9ED8\u8BA4\u5C3A\u5BF8\u4E0E\u4F4D\u7F6E");
    });
    $("tk-add").addEventListener("click", async () => {
      const slot = newSlot();
      S.takeout.slots.push(slot);
      renderTakeoutList();
      await autoSave();
      refreshStatusNow();
      toast(`\u5DF2\u6DFB\u52A0 ${slotTimeText(slot)} \u7684\u5916\u5356\u63D0\u9192`);
    });
    $("cb-add").addEventListener("click", () => void addCustomBreak());
    for (const t of ["short", "long"]) {
      $(`${t}-mode`).addEventListener("change", () => {
        const cfg = breakCfg(t);
        if (!cfg) return;
        cfg.mode = $(`${t}-mode`).value === "fixed" ? "fixed" : "countdown";
        applyBreakModeUI();
        updateBreakSummary(t);
      });
      $(`${t}-add`).addEventListener("click", async () => {
        const cfg = breakCfg(t);
        if (!cfg) return;
        const slot = newSlot();
        cfg.slots.push(slot);
        renderBreakList(t);
        await autoSave();
        refreshStatusNow();
        toast(`\u5DF2\u6DFB\u52A0 ${slotTimeText(slot)} \u7684${t === "short" ? "\u77ED" : "\u957F"}\u4F11\u606F`);
      });
      for (const id of [`${t}-enabled`, `${t}-interval`]) {
        $(id).addEventListener("change", () => updateBreakSummary(t));
      }
      $(`${t}-preview`).addEventListener("click", async () => {
        autoSave();
        await window.api.previewBreak(t);
      });
    }
    $("tl-card").classList.toggle("open", tlExpanded);
    $("tl-toggle").addEventListener("click", () => {
      tlExpanded = !tlExpanded;
      localStorage.setItem("wr-tl-expanded", tlExpanded ? "1" : "0");
      renderTimeline(LAST_STATE);
    });
    $("tk-preview").addEventListener("click", async () => {
      autoSave();
      await window.api.previewTakeout();
    });
    $("tk-enabled").addEventListener("change", () => updateTakeoutSummary());
    const batchModal = $("batch-modal");
    $("msg-batch-cancel").addEventListener("click", () => batchModal.classList.add("hidden"));
    batchModal.addEventListener("click", (e) => {
      if (e.target === batchModal) batchModal.classList.add("hidden");
    });
    $("msg-batch-ok").addEventListener("click", () => {
      const lines = $("msg-batch-text").value.split(/\r?\n/).map((l) => l.trim()).filter(Boolean);
      if (lines.length === 0) {
        batchModal.classList.add("hidden");
        return;
      }
      const cfg = msgCfgOf(msgBatchTarget);
      if (!cfg) {
        batchModal.classList.add("hidden");
        return;
      }
      for (const text of lines) {
        cfg.items.push({ id: crypto.randomUUID(), text, enabled: true });
      }
      batchModal.classList.add("hidden");
      renderMsgList(msgBatchTarget);
      autoSave();
      toast(`\u5DF2\u6DFB\u52A0 ${lines.length} \u6761\u63D0\u9192\u5185\u5BB9`);
    });
    $("cf-cancel").addEventListener("click", () => closeConfirm(false));
    $("cf-ok").addEventListener("click", () => closeConfirm(true));
    $("confirm-modal").addEventListener("click", (e) => {
      if (e.target === $("confirm-modal")) closeConfirm(false);
    });
    document.addEventListener("keydown", (e) => {
      if ($("confirm-modal").classList.contains("hidden")) return;
      if (e.key === "Escape") {
        e.preventDefault();
        closeConfirm(false);
        return;
      }
      if (e.key === "Enter") {
        if (document.activeElement instanceof HTMLButtonElement) return;
        e.preventDefault();
        closeConfirm(true);
      }
    });
    $("pm-cancel").addEventListener("click", () => closePrompt(null));
    $("pm-ok").addEventListener("click", () => closePrompt($("pm-input").value));
    $("prompt-modal").addEventListener("click", (e) => {
      if (e.target === $("prompt-modal")) closePrompt(null);
    });
    $("pm-input").addEventListener("keydown", (e) => {
      if (e.key === "Enter") {
        e.preventDefault();
        closePrompt($("pm-input").value);
      }
    });
    document.addEventListener("keydown", (e) => {
      if ($("prompt-modal").classList.contains("hidden")) return;
      if (e.key === "Escape") {
        e.preventDefault();
        closePrompt(null);
      }
    });
    $("ap-preview-short").addEventListener("click", async () => {
      autoSave();
      await window.api.previewBreak("short");
    });
    $("ap-opacity").addEventListener("input", () => {
      $("ap-opacity-val").textContent = parseFloat($("ap-opacity").value).toFixed(2);
    });
    $("snd-volume").addEventListener("input", () => {
      $("snd-volume-val").textContent = `${Math.round(parseFloat($("snd-volume").value) * 100)}%`;
    });
    $("qa-break").addEventListener("click", () => void window.api.action("breakNow"));
    $("qa-pause").addEventListener("click", () => void window.api.action("pauseToggle"));
    $("qa-postpone").addEventListener("click", () => void window.api.action("postpone"));
    $("qa-skip").addEventListener("click", () => void window.api.action("skip"));
    $("qa-reset").addEventListener("click", () => void window.api.action("resetCycle"));
    $("wb-min").addEventListener("click", () => void window.api.action("winMin"));
    $("wb-max").addEventListener("click", () => void window.api.action("winMaxToggle"));
    $("wb-close").addEventListener("click", () => void window.api.action("winClose"));
    document.querySelector("#titlebar .tb-drag")?.addEventListener("dblclick", () => {
      void window.api.action("winMaxToggle");
    });
    applyTitlebarMaximized(await window.api.windowMaximized());
    window.api.onWindowMaximized((max) => applyTitlebarMaximized(max));
    $("d-clearstats").addEventListener("click", () => {
      void (async () => {
        const ok = await confirmDialog({
          title: "\u6E05\u7A7A\u7EDF\u8BA1\u6570\u636E",
          desc: "\u786E\u5B9A\u6E05\u7A7A\u5168\u90E8\u4F7F\u7528\u7EDF\u8BA1\u6570\u636E\uFF1F",
          detail: "\u5C06\u5220\u9664\u672C\u5730 stats.json \u91CC\u7684\u5168\u90E8\u5DE5\u4F5C/\u4F11\u606F/\u5916\u5356\u7EDF\u8BA1\uFF0C\u4E14\u65E0\u6CD5\u6062\u590D\u3002",
          okText: "\u6E05\u7A7A"
        });
        if (!ok) return;
        await window.api.clearStats();
        toast("\u7EDF\u8BA1\u6570\u636E\u5DF2\u6E05\u7A7A");
        void renderStats();
      })();
    });
    $("d-quit").addEventListener("click", () => void window.api.quit());
    const info = await window.api.appInfo();
    $("d-version").textContent = `\u5403\u997C\u4EBA\u4E5F\u6562\u4F11\u606F\uFF1F v${info.version} \xB7 ${info.electron} \xB7 \u672C\u5730\u4F18\u5148`;
    const updateLine = $("d-update-status");
    const updateGo = $("d-update-go");
    const renderUpdate = (st) => {
      if (st.status === "update-available") {
        updateLine.textContent = `\u53D1\u73B0\u65B0\u7248\u672C v${st.latest}\uFF08\u5F53\u524D v${st.current}\uFF09`;
        updateLine.classList.remove("hidden");
        updateGo.classList.remove("hidden");
      } else if (st.status === "up-to-date") {
        updateLine.textContent = `\u5DF2\u662F\u6700\u65B0\u7248\u672C\uFF08v${st.current}\uFF09`;
        updateLine.classList.remove("hidden");
        updateGo.classList.add("hidden");
      } else if (st.status === "error") {
        updateLine.textContent = `\u68C0\u67E5\u66F4\u65B0\u5931\u8D25\uFF1A${st.message ?? "\u66F4\u65B0\u6E90\u4E0D\u53EF\u8FBE"}`;
        updateLine.classList.remove("hidden");
        updateGo.classList.add("hidden");
      } else {
        updateLine.classList.add("hidden");
        updateGo.classList.add("hidden");
      }
    };
    renderUpdate(await window.api.updateStatus());
    window.api.onUpdateStatus(renderUpdate);
    $("d-checkupdate").addEventListener("click", async () => {
      const btn = $("d-checkupdate");
      btn.disabled = true;
      try {
        const st = await window.api.checkUpdate();
        renderUpdate(st);
        if (st.status === "update-available") toast(`\u53D1\u73B0\u65B0\u7248\u672C v${st.latest}`);
        else if (st.status === "up-to-date") toast("\u5DF2\u662F\u6700\u65B0\u7248\u672C");
        else toast(`\u68C0\u67E5\u66F4\u65B0\u5931\u8D25\uFF08${st.message ?? "\u66F4\u65B0\u6E90\u4E0D\u53EF\u8FBE"}\uFF09`);
      } finally {
        btn.disabled = false;
      }
    });
    updateGo.addEventListener("click", () => {
      void window.api.updateStatus().then((st) => {
        if (st.status === "update-available" && st.url) void window.api.openExternal(st.url);
      });
    });
    $("d-manual").addEventListener("click", async () => {
      const ok = await window.api.openManual();
      if (!ok) toast("\u624B\u518C\u6587\u4EF6\u7F3A\u5931\uFF0C\u8BF7\u91CD\u65B0\u5B89\u88C5\u8F6F\u4EF6");
    });
    $("d-reset").addEventListener("click", () => {
      void (async () => {
        const ok = await confirmDialog({
          title: "\u6062\u590D\u9ED8\u8BA4\u8BBE\u7F6E",
          desc: "\u786E\u5B9A\u628A\u5168\u90E8\u8BBE\u7F6E\u6062\u590D\u4E3A\u51FA\u5382\u9ED8\u8BA4\uFF1F",
          detail: "\u5F53\u524D\u914D\u7F6E\u4F1A\u5148\u5907\u4EFD\u6210 settings.backup.json\uFF08\u6BCF\u6B21\u8986\u76D6\uFF09\u3002\u5B89\u88C5 ID \u4FDD\u7559\uFF0C\u865A\u62DF\u89D2\u8272\u76EE\u5F55\u4E0E\u5DF2\u5BFC\u5165\u7684\u97F3\u4E50\u6587\u4EF6\u4E0D\u53D7\u5F71\u54CD\uFF1B\u5F00\u673A\u81EA\u542F\u52A8\u4F1A\u4E00\u5E76\u5173\u95ED\u3002",
          okText: "\u6062\u590D\u9ED8\u8BA4"
        });
        if (!ok) return;
        const r = await window.api.resetSettings();
        if (!r.ok) {
          toast(`\u6062\u590D\u5931\u8D25\uFF1A${r.message ?? "\u672A\u77E5\u9519\u8BEF"}`);
          return;
        }
        toast("\u5DF2\u6062\u590D\u9ED8\u8BA4\u8BBE\u7F6E\uFF08\u539F\u914D\u7F6E\u5DF2\u5907\u4EFD\uFF09");
        S = await window.api.getSettings();
        fillForm();
        renderAllMsgLists();
        renderTakeoutList();
        await refreshSupervisorPreview();
      })();
    });
    for (const a of document.querySelectorAll("#tab-about .ext-link")) {
      a.addEventListener("click", (e) => {
        e.preventDefault();
        const url = a.dataset.url;
        if (url) void window.api.openExternal(url);
      });
    }
    await refreshSupervisorPreview();
    window.api.onState((raw) => renderStatus(raw));
    window.api.onAutostart((v) => {
      $("autostart").checked = v;
    });
    renderStatus(await window.api.getState());
    setInterval(async () => {
      renderStatus(await window.api.getState());
      updateTakeoutSummary();
      for (const t of allBreakIds()) updateBreakSummary(t);
    }, 1e3);
  }
  init();
})();
