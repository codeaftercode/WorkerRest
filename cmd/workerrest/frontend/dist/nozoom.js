/*
 * 禁缩放守卫（提醒窗口 / 预览窗口专用）
 * ---------------------------------------------------------------------------
 * 背景（2026-10-25 用户报障）：在触屏设备上两指同时操作，会把窗口内容放大 /
 * 缩小；带 Ctrl 的滚轮（Windows 精确式触控板的双指捏合就合成为它）同理。
 * 提醒窗口和预览窗口都是按固定版式排好的小窗，任何缩放都会让文字溢出、
 * 按钮错位，属于必须堵死的输入路径。
 *
 * 三处来源，分别用对应手段堵：
 *   1) 触屏双指捏合（WebView2 的 Page Scale 缩放）：在**合成器层**就被当成
 *      页面缩放处理，touchmove 的 preventDefault 拦不住 —— 唯一可靠的开关是
 *      touch-action。有效触摸行为里不含 pinch-zoom 时，合成器根本不会起缩放。
 *      挂在 <html> 上即覆盖整页（有效值 = 被触碰元素到根的交集）。
 *      ✅ 主手段。Go 侧另加了 WebView2 的 --disable-pinch 开关（见 main.go），
 *      两层叠加：即使某天脚本没加载到，浏览器进程那一层也已经关掉了捏合。
 *   2) Ctrl + 滚轮 / 触控板捏合、Ctrl + '+'/'-'/'0'（页面缩放 / ZoomFactor）：
 *      这类事件可取消，preventDefault 即可。**只拦带 Ctrl 的**，普通滚轮照旧
 *      放行 —— break.js / takeout.js 还靠 wheel 记「用户活动」，拦死会把
 *      活动上报一起断掉，休息倒计时会被误判成「离开」。
 *   3) 多指触摸的原始事件（兜底）：万一某个 WebView2 版本不认 touch-action，
 *      两指同时按下的 touchstart / touchmove 一律拦下。单指不动，点击、拖动、
 *      滚动都照常可用。
 *
 * 必须用**外链**引入（三个页面都带 CSP default-src 'self'，内联脚本会被拦），
 * 且放在 <head> 里尽早执行：监听器用 capture 捕获，早注册才能抢在页面自己的
 * 处理器之前判定。
 */
(function () {
  "use strict";

  // 幂等：重复引入（比如页面自己又引了一次）不叠加监听器
  if (window.__WR_NOZOOM) return;
  window.__WR_NOZOOM = true;

  var root = document.documentElement;
  var LOCKED = "pan-x pan-y"; // 允许平移/滚动，不允许 pinch-zoom

  // 1) 触屏捏合：见文件头第 1 条。<html> 一定已存在；<body> 可能还没解析出来，
  //    所以两处都设，body 用 DOMContentLoaded 补一刀（touch-action 不继承，
  //    但有效值是祖先链的交集，根上设了本已足够，这里是显式冗余）。
  if (root) root.style.touchAction = LOCKED;
  function lockBody() {
    if (document.body) document.body.style.touchAction = LOCKED;
  }
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", lockBody, { once: true });
  } else {
    lockBody();
  }

  // 2) Ctrl + 滚轮 / 触控板捏合：只拦带修饰键的，普通滚轮放行（见文件头第 2 条）。
  //    capture + passive:false 缺一不可：浏览器默认把 wheel 当 passive，
  //    passive 监听器里的 preventDefault 会被直接忽略。
  document.addEventListener("wheel", function (e) {
    if (e.ctrlKey || e.metaKey) e.preventDefault();
  }, { capture: true, passive: false });

  // 3) Ctrl + '+' / '=' / '-' / '_' / '0'（WebView2 的浏览器级缩放快捷键）。
  //    只 preventDefault，**不** stopPropagation：页面自己的快捷键（break.js
  //    的「跳过 / 推迟」等可配置组合键）也要照常收到事件，否则用户把快捷键
  //    配成 Ctrl+0 时会莫名失效。
  document.addEventListener("keydown", function (e) {
    if (!(e.ctrlKey || e.metaKey) || e.altKey) return;
    var k = e.key;
    if (k === "+" || k === "=" || k === "-" || k === "_" || k === "0") {
      e.preventDefault();
    }
  }, { capture: true });

  // 4) 多指触摸兜底：两指及以上一律拦下（见文件头第 3 条）
  function blockMultiTouch(e) {
    if (e.touches && e.touches.length > 1) e.preventDefault();
  }
  document.addEventListener("touchstart", blockMultiTouch, { capture: true, passive: false });
  document.addEventListener("touchmove", blockMultiTouch, { capture: true, passive: false });

  // 5) WebKit 风格的 gesture 事件（Chromium / WebView2 不发，留着兜底，
  //    万一以后换渲染引擎也不会退化）
  ["gesturestart", "gesturechange", "gestureend"].forEach(function (type) {
    document.addEventListener(type, function (e) { e.preventDefault(); },
      { capture: true, passive: false });
  });
})();
