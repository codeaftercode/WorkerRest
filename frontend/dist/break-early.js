/*
 * 休息窗口首帧可见性预置（必须在 break.css / 页面脚本之前同步执行）
 * ---------------------------------------------------------------------------
 * 背景：启用督休专员时，立绘由 break.js 异步取回（Go 侧读 webp → data URL），
 * 首帧一定是「还没有立绘」的半成品。为了不闪，break.css 里把 <html> 默认设成
 * 透明，等立绘就绪再淡入。
 *
 * 但「没有督休专员」的场合（用户关掉总开关 / ?sup 缺失）不会有任何人来把透明度
 * 改回来 —— 那就得在首帧前就把它设成 1，否则整个窗口永远透明。
 * 这个判断只依赖 URL 里的 ?sup=1，没有任何异步步骤，所以必须**尽早**执行：
 * 放进 break.js 就太晚了（那时 CSS 已经按透明画过一帧，用户能看到一次闪白）。
 *
 * 为什么是外链：break.html 带 CSP `default-src 'self'`，内联 <script> 会被拦掉
 * （2026-10-25 排查：这条内联脚本自从加上 CSP 起就从未执行过，报 CSP violation）。
 * 需要改这段逻辑时改本文件，不要搬回页面里。
 */
(function () {
  "use strict";
  try {
    var p = new URLSearchParams(location.search);
    if (p.get("sup") !== "1") {
      document.documentElement.style.opacity = "1";
    }
  } catch (e) {
    // 老 WebView 没有 URLSearchParams：当作「无督休专员」直接显示，
    // 与 sup 缺省同义（宁可无淡入，也不要整窗透明）
    document.documentElement.style.opacity = "1";
  }
})();
