"use strict";
(() => {
  // renderer/prebreak.ts
  var api = window.api;
  var secondsEl = document.getElementById("pb-seconds");
  var postEl = document.getElementById("pb-post");
  var titleEl = document.getElementById("pb-title");
  var tipsEl = document.getElementById("pb-tips");
  var pauseBtn = document.getElementById("pb-pause");
  var nowBtn = document.getElementById("pb-now");
  var postponeBtn = document.getElementById("pb-postpone");
  var skipBtn = document.getElementById("pb-skip");
  document.getElementById("pb-now").addEventListener("click", () => void api.preBreakAction("breakNow"));
  document.getElementById("pb-postpone").addEventListener("click", () => void api.preBreakAction("postpone"));
  document.getElementById("pb-skip").addEventListener("click", () => void api.preBreakAction("skip"));
  document.getElementById("pb-pause").addEventListener("click", () => void api.preBreakAction("pause"));
  var BTN_LABELS = {
    builtin: { now: "立即休息", postpone: "推迟休息", skip: "跳过休息" },
    custom: { now: "立即提醒", postpone: "推迟提醒", skip: "跳过提醒" }
  };
  function applyVariant(custom, tail) {
    postEl.textContent = tail;
    tipsEl.classList.toggle("hidden", custom);
    pauseBtn.classList.toggle("hidden", custom);
    const label = custom ? BTN_LABELS.custom : BTN_LABELS.builtin;
    nowBtn.textContent = label.now;
    postponeBtn.textContent = label.postpone;
    skipBtn.textContent = label.skip;
  }

  // ---------- 底色 / 文字色自适应 ----------
  // 小窗底色跟随“通用配置 → 提醒窗口 → 背景颜色”（appearance.color，与休息窗口同一个键），
  // 由主进程经 URL 的 bg 参数（首帧）与 PreBreakState.bgColor（轮询）下发。
  //
  // 颜色是用户随便调的，所以文字色绝不能写死：这里把配置色当“主色”，派生出两档底色
  //（亮色配置 → 浅底，暗色配置 → 深底），再按底色算出对比度够用的文字 / 边框 / 按钮 /
  // 红绿两行说明。判定用 WCAG 相对亮度，不用 HSL 的 l —— 后者对饱和度敏感，
  // 中灰和纯黄的 l 差别很小，但眼睛看到的明暗差很多。
  var DEFAULT_BG = "#4f7cff";
  // 把配置色当“亮色”的门槛（相对亮度）。0.45 约等于 #b9b9b9：比它亮走浅底，否则深底。
  var LIGHT_ENOUGH = 0.45;

  function parseHex(v) {
    var s = String(v || "").trim().replace(/^#/, "");
    if (s.length === 3) s = s.charAt(0) + s.charAt(0) + s.charAt(1) + s.charAt(1) + s.charAt(2) + s.charAt(2);
    if (!/^[0-9a-fA-F]{6}$/.test(s)) return null;
    var n = parseInt(s, 16);
    return [(n >> 16) & 255, (n >> 8) & 255, n & 255];
  }
  function toHex(c) {
    return "#" + c.map(function (v) { return ("0" + v.toString(16)).slice(-2); }).join("");
  }
  function rgbStr(c) { return "rgb(" + c[0] + ", " + c[1] + ", " + c[2] + ")"; }
  function rgbaStr(c, a) { return "rgba(" + c[0] + ", " + c[1] + ", " + c[2] + ", " + a + ")"; }
  function rgbToHsl(c) {
    var r = c[0] / 255, g = c[1] / 255, b = c[2] / 255;
    var mx = Math.max(r, g, b), mn = Math.min(r, g, b);
    var h = 0, s = 0, l = (mx + mn) / 2;
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
  function hslToRgb(h, s, l) {
    h = (((h % 360) + 360) % 360) / 360;
    if (s <= 0) {
      var v0 = Math.round(l * 255);
      return [v0, v0, v0];
    }
    var q = l < 0.5 ? l * (1 + s) : l + s - l * s;
    var p = 2 * l - q;
    var f = function (t) {
      if (t < 0) t += 1;
      if (t > 1) t -= 1;
      if (t < 1 / 6) return p + (q - p) * 6 * t;
      if (t < 1 / 2) return q;
      if (t < 2 / 3) return p + (q - p) * (2 / 3 - t) * 6;
      return p;
    };
    return [Math.round(f(h + 1 / 3) * 255), Math.round(f(h) * 255), Math.round(f(h - 1 / 3) * 255)];
  }
  function relLum(c) {
    var ch = function (v) {
      v /= 255;
      return v <= 0.03928 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4);
    };
    return 0.2126 * ch(c[0]) + 0.7152 * ch(c[1]) + 0.0722 * ch(c[2]);
  }
  function contrast(a, b) {
    var l1 = relLum(a), l2 = relLum(b);
    return (Math.max(l1, l2) + 0.05) / (Math.min(l1, l2) + 0.05);
  }
  function mix(a, b, t) {
    return [
      Math.round(a[0] + (b[0] - a[0]) * t),
      Math.round(a[1] + (b[1] - a[1]) * t),
      Math.round(a[2] + (b[2] - a[2]) * t),
    ];
  }
  // 把颜色朝 toward 方向挪，直到与 bg 的对比度够 want（最多挪 24 步，够不到就取最接近的）。
  // 用于“主色本身当字色 / 当按钮底”时兜底：配置色被调成极深或极浅都不会糊进底色。
  function ensureContrast(c, bg, want, toward) {
    var out = c.slice();
    for (var i = 0; i < 24 && contrast(out, bg) < want; i++) out = mix(out, toward, 0.12);
    return out;
  }

  var appliedBg = "";
  function applyPalette(bgColor) {
    var src = parseHex(bgColor) || parseHex(DEFAULT_BG);
    // 轮询每 500ms 一次，颜色没变就不重算、不重写 DOM
    if (toHex(src) === appliedBg) return;
    appliedBg = toHex(src);

    var c = rgbToHsl(src);
    var light = relLum(src) >= LIGHT_ENOUGH;
    // 底色两档都保留配置色的色相：换颜色时窗口整体跟着变，而不是永远一块深灰。
    // 饱和度压一档 —— 底色铺满整个小窗，原色饱和度直接用会很刺眼。
    var sBg = Math.min(c.s, light ? 0.55 : 0.62);
    var top = hslToRgb(c.h, sBg, light ? 0.95 : 0.26);
    var bot = hslToRgb(c.h, sBg, light ? 0.86 : 0.12);
    // 文字与派生色的“拉离方向”：浅底往深里走，深底往亮里走
    var text = light ? [24, 28, 38] : [238, 241, 248];
    var away = light ? [16, 19, 27] : [255, 255, 255];

    var root = document.documentElement.style;
    root.setProperty("--accent", toHex(src));
    root.setProperty("--pb-bg-1", rgbStr(top));
    root.setProperty("--pb-bg-2", rgbStr(bot));
    root.setProperty("--text", rgbStr(text));
    root.setProperty("--text-2", rgbaStr(text, light ? 0.68 : 0.66));
    root.setProperty("--border", light ? "rgba(0, 0, 0, 0.14)" : "rgba(255, 255, 255, 0.14)");
    root.setProperty("--btn-bg", light ? "rgba(0, 0, 0, 0.05)" : "rgba(255, 255, 255, 0.08)");
    root.setProperty("--btn-bg-hover", light ? "rgba(0, 0, 0, 0.10)" : "rgba(255, 255, 255, 0.14)");
    // 两行说明的红 / 绿：深底用提亮版（纯 #f00 在深底上又暗又刺眼），
    // 浅底用压暗版（提亮版压在浅底上对比不到 2:1，等于看不见）
    root.setProperty("--tip-bad", light ? "#b3141c" : "#ff9a9a");
    root.setProperty("--tip-good", light ? "#0b6b36" : "#63e29b");
    // 倒计时数字：主色本身当字用，深到糊进底色时（如深蓝配深蓝底）往文字方向提亮
    root.setProperty("--num", rgbStr(ensureContrast(src, bot, 4, away)));
    // 主按钮：主色当底，文字颜色按按钮底的实际亮度反选；主色与面板贴太近时
    // 把按钮底推离面板（否则按钮会“消失”在背景里）
    var pBg = ensureContrast(src, bot, 3, away);
    root.setProperty("--btn-primary-bg", rgbStr(pBg));
    root.setProperty("--btn-primary-text", relLum(pBg) > 0.3 ? "#12151e" : "#ffffff");
    // 给探针 / 排查用的标记（不参与渲染）
    document.documentElement.dataset.pbLum = light ? "light" : "dark";
    document.documentElement.dataset.pbBg = toHex(src);
  }

  // 版式（自定义 / 内置）与倒计时后缀由主进程经 URL 下发，首帧就位；
  // 轮询里的同名字段只是备份（query 缺失的旧地址才回退到 state）
  var urlBg = null;
  {
    const q = new URLSearchParams(location.search);
    applyVariant(q.get("custom") === "1", q.get("tail") ?? " 秒进入休息");
    // 首帧就把底色铺成用户配的颜色（地址里带了 bg），避免先闪一下内置默认蓝
    urlBg = q.get("bg");
    if (urlBg) applyPalette(urlBg);
  }
  async function poll() {
    try {
      const st = await api.preBreakState();
      // 底色与暂停态无关：暂停中也要保持用户配的颜色。
      // 地址里带了的 bg 优先，轮询值只作旧地址的备份。
      if (urlBg === null && st.bgColor) applyPalette(st.bgColor);
      if (st.paused) {
        titleEl.textContent = "已暂停";
        secondsEl.textContent = "--";
        postEl.textContent = "";
      } else {
        titleEl.textContent = st.title || "您已连续工作较长时间，建议您短休息";
        secondsEl.textContent = String(Math.max(0, Math.ceil(st.remainMs / 1e3)));
        applyVariant(st.custom, st.countdownTail);
      }
    } catch {
    }
  }
  poll();
  setInterval(poll, 500);
})();
