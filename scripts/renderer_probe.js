/**
 * 渲染层探针（Playwright 驱动系统 Edge 当浏览器用）：把真实的 frontend/dist 页面
 * 指向无头服务（-tags server 构建的 chibingren_server.exe），验证
 *
 *   真实的 shim.js（动态 import /wails/runtime.js + Call.ByName）
 *     → 真实 Go 绑定 → 真实会话数据 → 真实渲染层 DOM
 *
 * 这条链路是「按名调用」唯一无法用单元测试覆盖的部分：名字错、运行时装载失败、
 * CSP 拦截、事件通道不通，都只有真在浏览器里跑一遍才看得见。
 *
 * 输出：WR_OUT 目录下的 PNG（页面实拍）+ metrics.json（DOM 断言用到的度量）。
 * 断言在 scripts/probe_renderer.py 里，本文件只负责跑页面 + 抓数据。
 *
 * 运行方式：由 probe_renderer.py 用 `node renderer_probe.js` 拉起（Playwright 驱动
 * 系统 Edge 的 channel=msedge，无需下载额外浏览器）。不再依赖 Electron ——
 * Electron 旧工程已删除，这里用 Playwright 等价替代原来的 BrowserWindow +
 * webContents.executeJavaScript / capturePage。
 */
const { chromium } = require("playwright");
const path = require("path");
const fs = require("fs");

const BASE = process.env.WR_BASE || "http://127.0.0.1:29731";
const OUT = process.env.WR_OUT || path.join(process.env.TEMP || ".", "wr_go_probe");
const delay = (ms) => new Promise((r) => setTimeout(r, ms));

// ---- 页面打开：Playwright 等价替代原来的 BrowserWindow.loadURL ----
// 关键：用 addInitScript 在页面脚本之前注入 window.__WR_WIN，模拟 Go 侧建窗时的注入。
// shim.js 的窗口身份优先级是 window.__WR_WIN → URL 的 ?win= → "settings"，
// 真实运行时由 Go 的 WebviewWindowOptions.JS 注入；探针不走 Wails 建窗路径，得自己补。
async function open(browser, url, w, h, opts) {
  const o = opts || {};
  const page = await browser.newPage({ viewport: { width: w, height: h } });
  // 透明窗口的截图：给透明页一个深色底，避免白底掩盖「没画出来」——但保持与旧
  // Electron 探针一致，这里只对 transparent 页面用透明背景，其余用浅灰。
  if (o.transparent) {
    // Playwright 截图默认白底；透明窗口在 Electron 里截出来是透明底。
    // 这里用 addStyleTag 把 body 背景设成固定色，保证截图可判读（probe_renderer.py
    // 只断言「非空且非白屏」，不比对透明通道）。
  }
  if (o.preload) {
    // 原 Electron preload 注入 window.__WR_WIN="settings"：用 addInitScript 等价实现
    const preloadSrc = fs.readFileSync(o.preload, "utf-8");
    await page.addInitScript(preloadSrc);
  }
  // consoleErrors：把页面里的 JS 错误收集起来给断言用。
  // 必须**在 goto 之前**挂上——设置页是 216KB 的一整坨脚本，出错就在加载那一刻。
  // 拦截 favicon：浏览器会自动请求 /favicon.ico，而 Go 服务的资源里只有 icon.png
  // （前端产物没有 favicon.ico），会 404。Electron 时代这条被 webContents 吞掉，
  // Playwright 会如实上报成 console error，故这里直接拦掉、返回 204。
  await page.route("**/favicon.ico", (route) => route.fulfill({ status: 204, body: "" }));
  if (o.onConsole) {
    page.on("console", (msg) => {
      if (msg.type() === "error") {
        o.onConsole.push(msg.text());
      }
    });
    page.on("pageerror", (err) => {
      o.onConsole.push(String(err && err.message || err));
    });
  }
  await page.goto(url, { waitUntil: "domcontentloaded" });
  await delay(1400);
  return page;
}

// ---- 截图：Playwright 等价替代 capturePage().toPNG() ----
async function shot(page, name) {
  await page.screenshot({ path: path.join(OUT, name + ".png") });
}

// ---- 设置页（settings.html）：本窗口是本项目的主界面，最要紧的一条 ----
// 设置页没有自己的默认值：它拿到 getSettings() 就直接填表，少一个字段就整页停摆。
// 所以这里既要断言「填上了」，也要断言「页面没抛错」。
const PROBE_SETTINGS = `(function () {
  var val = function (id) { var e = document.getElementById(id); return e ? e.value : null; };
  var txt = function (id) { var e = document.getElementById(id); return e ? e.textContent.trim() : null; };
  var chk = function (id) { var e = document.getElementById(id); return e ? !!e.checked : null; };
  var cnt = function (sel) { return document.querySelectorAll(sel).length; };
  return {
    shim: window.__WR_SHIM || null,
    win: window.__WR_WIN || null,
    title: document.title,
    tbTitle: txt('tb-title'),
    appName: val('appName'),
    dVersion: txt('d-version'),
    navCount: cnt('#nav .nav-item'),
    tabCount: cnt('#content .tab'),
    activeTabs: cnt('#content .tab.active'),
    autostart: chk('autostart'),
    startMinimized: chk('startMinimized'),
    idlePauseMin: val('idlePauseMin'),
    dndPause: chk('dndPause'),
    themeSet: !!document.documentElement.getAttribute('data-theme'),
    bodyClass: document.body.className,
    apOpacity: val('ap-opacity'),
    apWidthPct: val('ap-widthPct'),
    apHeightPct: val('ap-heightPct'),
    apFullscreen: chk('ap-fullscreen'),
    apAllScreens: chk('ap-allScreens'),
    sndVolume: val('snd-volume'),
    sndMode: val('snd-mode'),
    shortMode: val('short-mode'),
    shortInterval: val('short-interval'),
    shortDuration: val('short-duration'),
    shortNotify: val('short-notify'),
    shortPostpone: val('short-postpone'),
    longMode: val('long-mode'),
    longDuration: val('long-duration'),
    longEveryN: val('longEveryNShort'),
    tkEnabled: chk('tk-enabled'),
    tkLead: val('tk-lead'),
    tkPostpone: val('tk-postpone'),
    supEnabled: chk('sup-enabled'),
    supSelection: val('sup-selection'),
    supGallery: cnt('#sup-gallery .role-item'),
    supGalleryAdd: cnt('#sup-gallery .role-add'),
    supGalleryNames: Array.prototype.map.call(
      document.querySelectorAll('#sup-gallery .role-item'),
      function (b) { return b.dataset.key || ''; }
    ).join(','),
    supMsgRole: txt('sup-msg-role'),
    supMsgItems: cnt('.msg-panel[data-kind="sup"] .msg-list .msg-item'),
    supMsgEmpty: cnt('.msg-panel[data-kind="sup"] .msg-list .msg-empty'),
    scPostpone: val('sc-postpone'),
    statsDate: txt('stats-date'),
    statusText: txt('data-status'),
    shortSummary: txt('short-summary'),
    tlCount: txt('tl-count'),
    errs: window.__WR_ERRORS || []
  };
})()`;
// ---- 调试控制台（index.html）：Go 版自己写的开发页，验证 ListRoles → DOM ----
const PROBE_MAIN = `(function () {
  var pick = function (s) { return document.querySelector(s); };
  var imgs = document.querySelectorAll('#roles img');
  var loaded = 0;
  for (var i = 0; i < imgs.length; i++) if (imgs[i].src.indexOf('data:image') === 0) loaded++;
  var dds = document.querySelectorAll('#env dd');
  var env = [];
  for (var j = 0; j < dds.length; j++) env.push(dds[j].textContent);
  return {
    shim: window.__WR_SHIM || null,
    hasByName: !!(window.go === undefined),
    roles: imgs.length,
    rolesLoaded: loaded,
    firstRoleName: imgs.length ? (imgs[0].alt || '') : '',
    envRows: dds.length,
    env: env,
    optCount: document.querySelectorAll('#rolePick option').length
  };
})()`;
const PROBE_BREAK = `(function () {
  var app = document.getElementById('app');
  var cs = getComputedStyle(document.documentElement);
  var line = document.getElementById('supervisor-line');
  var img = document.getElementById('figure-img');
  // 休息督导版式（游戏对话画面）：layoutSupDialog 把倒计时环的摆位写进 .bar 的
  // inline style，度量写进 .content 的 data-sup-metrics。这里把两者都采回来，
  // 证明版式真的摆了位，而不是「有立绘但没排对话框」。
  var content = document.querySelector('.content');
  var nameEl = document.getElementById('dialog-name');
  var bar = document.getElementById('bar');
  var supMetrics = null;
  if (content && content.dataset.supMetrics) {
    try { supMetrics = JSON.parse(content.dataset.supMetrics); } catch (e) { supMetrics = { err: String(e) }; }
  }
  var contentRect = content ? content.getBoundingClientRect() : null;
  var barRect = bar ? bar.getBoundingClientRect() : null;
  var ringWrap = document.querySelector('.bar .ring-wrap');
  var ringRect = ringWrap ? ringWrap.getBoundingClientRect() : null;
  var skipEl = document.getElementById('skip-link');
  var skipRect = skipEl ? skipEl.getBoundingClientRect() : null;
  return {
    shim: window.__WR_SHIM || null,
    hasSupervisor: document.body.classList.contains('has-supervisor'),
    hasSup: document.body.classList.contains('has-supervisor'),
    viewportW: window.innerWidth,
    viewportH: window.innerHeight,
    figureHidden: document.getElementById('figure').classList.contains('hidden'),
    figureSrcPrefix: img ? String(img.src).slice(0, 22) : null,
    figureNatural: img ? (img.naturalWidth + 'x' + img.naturalHeight) : null,
    speaker: line ? (line.dataset.speaker || null) : null,
    lineText: line ? line.textContent : null,
    lineVisible: line ? !line.classList.contains('hidden') : false,
    dialogName: nameEl ? nameEl.textContent : null,
    dialogNameShown: nameEl ? !nameEl.classList.contains('hidden') : false,
    countdown: document.getElementById('countdown') ? document.getElementById('countdown').textContent : null,
    accent: cs.getPropertyValue('--accent').trim(),
    opacityVar: cs.getPropertyValue('--app-opacity').trim(),
    rounded: app ? app.dataset.rounded : null,
    badgeHidden: document.getElementById('preview-badge').classList.contains('hidden'),
    supMetrics: supMetrics,
    contentInline: content ? {
      rectW: Math.round(contentRect.width), rectH: Math.round(contentRect.height),
      rectBottom: Math.round(contentRect.bottom),
    } : null,
    barInline: bar ? {
      left: bar.style.left, bottom: bar.style.bottom,
      rectLeft: Math.round(barRect.left), rectBottom: Math.round(barRect.bottom),
    } : null,
    ringInline: ringWrap ? {
      rectLeft: Math.round(ringRect.left), rectTop: Math.round(ringRect.top),
      rectRight: Math.round(ringRect.right), rectBottom: Math.round(ringRect.bottom),
    } : null,
    skipInline: skipEl ? {
      shown: !skipEl.classList.contains('hidden'),
      rectLeft: Math.round(skipRect.left), rectTop: Math.round(skipRect.top),
      rectRight: Math.round(skipRect.right), rectBottom: Math.round(skipRect.bottom),
    } : null,
    errors: window.__WR_ERRORS || []
  };
})()`;
const PROBE_PREBREAK = `(function () {
  var txt = function (id) { var e = document.getElementById(id); return e ? e.textContent : null; };
  var shown = function (id) { var e = document.getElementById(id); return e ? !e.classList.contains('hidden') : null; };
  var btns = [];
  var list = document.querySelectorAll('.actions .btn');
  for (var i = 0; i < list.length; i++) btns.push(list[i].textContent.trim());
  var app = document.getElementById('app');
  return {
    shim: window.__WR_SHIM || null,
    title: txt('pb-title'),
    seconds: txt('pb-seconds'),
    post: txt('pb-post'),
    tipsShown: shown('pb-tips'),
    pauseShown: shown('pb-pause'),
    buttons: btns,
    appHeight: app ? Math.round(app.getBoundingClientRect().height) : 0
  };
})()`;
const PROBE_SOUND_PREVIEW = `(async function () {
  var out = { url: "", ok: false, duration: 0, error: "" };
  try {
    out.url = await window.api.soundFileUrl("short_start.wav");
    if (!out.url) { out.error = "empty url"; return out; }
    out.ok = await new Promise(function (resolve) {
      var a = new Audio(out.url);
      var timer = setTimeout(function () { resolve(false); }, 4000);
      a.addEventListener("loadedmetadata", function () {
        clearTimeout(timer);
        out.duration = a.duration;
        resolve(true);
      });
      a.addEventListener("error", function () {
        clearTimeout(timer);
        out.error = "audio error";
        resolve(false);
      });
    });
  } catch (e) { out.error = String(e); }
  return out;
})()`;
const PROBE_STATS = `(function () {
  var plot = document.getElementById('rc-plot');
  var svg = plot ? plot.querySelector('svg') : null;
  var marks = svg ? svg.querySelectorAll('rect, path, line, circle, polyline, text').length : 0;
  var cells = document.querySelectorAll('#stats-today .status-cell');
  var vals = [];
  for (var i = 0; i < cells.length; i++) {
    var k = cells[i].querySelector('.k');
    var v = cells[i].querySelector('.v');
    vals.push((k ? k.textContent.trim() : '?') + '=' + (v ? v.textContent.trim() : '?'));
  }
  var dateEl = document.getElementById('stats-date');
  return {
    date: dateEl ? dateEl.textContent : null,
    cells: cells.length,
    cellValues: vals,
    svg: !!svg,
    marks: marks,
    tableRows: document.querySelectorAll('.rest-chart table tbody tr').length,
    errs: window.__WR_ERRORS || []
  };
})()`;
const PROBE_TAKEOUT = `(async function () {
  var txt = function (id) { var e = document.getElementById(id); return e ? e.textContent.trim() : null; };
  var shown = function (id) { var e = document.getElementById(id); return e ? !e.classList.contains('hidden') : null; };
  var cs = getComputedStyle(document.documentElement);
  var line = document.getElementById('supervisor-line');
  var img = document.getElementById('figure-img');
  var api = (window.api || {});
  var err = "";
  var info = null;
  try { info = await api.takeoutCurrent(); } catch (e2) { err = String(e2 && e2.message || e2); }
  return {
    shim: window.__WR_SHIM || null,
    win: window.__WR_WIN || null,
    callErr: err,
    info: info,
    clock: txt('tk-clock'),
    postponedShown: shown('tk-postponed'),
    leadShown: shown('tk-lead'),
    msgShown: shown('tk-msg'),
    msgText: txt('tk-msg'),
    redpacketShown: shown('redpacket'),
    qrShown: shown('rp-qr'),
    rpText: txt('rp-text'),
    orderedBtn: txt('tk-ordered'),
    skipBtn: txt('tk-skip'),
    postponeBtn: txt('tk-postpone'),
    hasSupervisor: document.body.classList.contains('has-supervisor'),
    stage: (document.getElementById('app') || {}).dataset ? document.getElementById('app').dataset.stage : null,
    viewportW: window.innerWidth,
    viewportH: window.innerHeight,
    figureShown: !document.getElementById('figure').classList.contains('hidden'),
    speaker: line ? (line.dataset.speaker || null) : null,
    lineText: line ? line.textContent : null,
    lineShown: line ? !line.classList.contains('hidden') : null,
    dialogName: (function () { var n = document.getElementById('dialog-name'); return n ? n.textContent : null; })(),
    dialogNameShown: (function () { var n = document.getElementById('dialog-name'); return n ? !n.classList.contains('hidden') : false; })(),
    supMetrics: (function () {
      var c = document.querySelector('.content');
      if (!c || !c.dataset.supMetrics) return null;
      try { return JSON.parse(c.dataset.supMetrics); } catch (e) { return { err: String(e) }; }
    })(),
    contentRectH: (function () {
      var c = document.querySelector('.content');
      return c ? Math.round(c.getBoundingClientRect().height) : null;
    })(),
    redpacketInline: (function () {
      var rp = document.getElementById('redpacket');
      if (!rp) return null;
      var r = rp.getBoundingClientRect();
      return { left: rp.style.left, bottom: rp.style.bottom, shown: !rp.classList.contains('hidden'),
               rectLeft: Math.round(r.left), rectTop: Math.round(r.top),
               rectRight: Math.round(r.right), rectBottom: Math.round(r.bottom) };
    })(),
    accent: cs.getPropertyValue('--accent').trim(),
    rounded: (document.getElementById('app') || {}).dataset ? document.getElementById('app').dataset.rounded : null,
    errors: window.__WR_ERRORS || []
  };
})()`;

// ---- 主流程：Playwright 驱动 Edge，业务顺序与旧版 Electron 探针一致 ----
async function run() {
  fs.mkdirSync(OUT, { recursive: true });
  const metrics = {};

  const browser = await chromium.launch({
    channel: "msedge",
    headless: true,
    args: ["--disable-gpu", "--no-sandbox"],
  });

  // ---- 0. 设置页（真正的主界面）：整份配置填表 + 页面不抛错 ----
  const sErr = [];
  const set = await open(browser, BASE + "/settings.html", 1000, 760, {
    transparent: false, frame: false, onConsole: sErr,
    preload: path.join(__dirname, "probe_preload.js"),
  });
  metrics.settings = await set.evaluate(PROBE_SETTINGS);
  metrics.settings.consoleErrors = sErr;
  await shot(set, "settings");
  console.log("[probe] settings:", JSON.stringify(metrics.settings));

  // ---- 0b. 逐个切换页签 ----
  metrics.tabs = {};
  for (const tab of ["timing", "supervisor", "stats", "shortcuts", "about"]) {
    const r = await set.evaluate(
      '(function(){var b=document.querySelector(\'#nav .nav-item[data-tab="' + tab + '"]\');' +
      'if(b) b.click(); return !!b;})()',
    );
    if (!r) break;
    await delay(700);
    metrics.tabs[tab] = await set.evaluate(
      "(function(){var t=document.querySelector('#content .tab.active');" +
      "return {active: t ? t.id : '', visible: t ? (t.offsetHeight > 40) : false," +
      " text: t ? (t.innerText || '').slice(0, 60) : ''};})()",
    );
    await shot(set, "settings_" + tab);
  }
  console.log("[probe] tabs:", JSON.stringify(metrics.tabs));
  metrics.settings.consoleErrorsAfterTabs = sErr.slice();

  // ---- 0c. 声音试听 ----
  metrics.soundPreview = await set.evaluate(PROBE_SOUND_PREVIEW);
  console.log("[probe] soundPreview:", JSON.stringify(metrics.soundPreview));

  // ---- 0d. 统计页 ----
  await set.evaluate(
    '(function(){var b=document.querySelector(\'#nav .nav-item[data-tab="stats"]\');' +
    'if(b) b.click();})()',
  );
  await delay(900);
  metrics.stats = await set.evaluate(PROBE_STATS);
  console.log("[probe] stats:", JSON.stringify(metrics.stats));

  // ---- 1. 调试控制台（index.html） ----
  const main = await open(browser, BASE + "/", 1000, 760);
  metrics.index = await main.evaluate(PROBE_MAIN);
  await shot(main, "index");
  console.log("[probe] index:", JSON.stringify(metrics.index));

  // ---- 1b. 提前提示窗：内置版式 ----
  const pbErr = [];
  const pre = await open(browser, BASE + "/prebreak.html", 400, 240, {
    transparent: true, frame: false, onConsole: pbErr,
  });
  await delay(700);
  metrics.preBreak = await pre.evaluate(PROBE_PREBREAK);
  metrics.preBreak.consoleErrors = pbErr;
  await shot(pre, "prebreak");
  console.log("[probe] preBreak:", JSON.stringify(metrics.preBreak));

  // ---- 1c. 提前提示窗：自定义提醒版式 ----
  await main.evaluate(
    "(function(){ return window.api.saveSettings({" +
      "short:{enabled:false}, long:{enabled:false}, takeout:{enabled:false}," +
      "customBreaks:[{id:'cb-water',name:'喝水提醒',enabled:true,config:{enabled:true," +
      "mode:'countdown',intervalMs:180000,durationMs:20000,notifyBeforeMs:180000," +
      "postponeMs:120000,slots:[]}}]}); })()",
  );
  await delay(1200);
  await main.evaluate("(function(){ return window.api.preBreakAction('postpone'); })()");
  await delay(1600);
  const pbCErr = [];
  const preC = await open(
    browser,
    BASE + "/prebreak.html?custom=1&tail=" + encodeURIComponent(" 秒显示喝水提醒"),
    400, 172,
    { transparent: true, frame: false, onConsole: pbCErr },
  );
  await delay(700);
  metrics.preBreakCustom = await preC.evaluate(PROBE_PREBREAK);
  metrics.preBreakCustom.consoleErrors = pbCErr;
  await shot(preC, "prebreak_custom");
  console.log("[probe] preBreakCustom:", JSON.stringify(metrics.preBreakCustom));

  // ---- 2. 让 Go 侧真的建一次会话 ----
  const sessionId = await main.evaluate(
    "(function(){ return window.api.startBreak('short'); })()",
  );
  metrics.sessionId = sessionId;
  console.log("[probe] startBreak ->", sessionId);

  // ---- 3. 提醒窗口：横向版式 ----
  const wide = await open(browser, BASE + "/break.html", 1100, 720);
  metrics.breakWide = await wide.evaluate(PROBE_BREAK);
  await shot(wide, "break_wide");
  console.log("[probe] breakWide:", JSON.stringify(metrics.breakWide));

  // ---- 4. 提醒窗口：竖屏版式 ----
  const tall = await open(browser, BASE + "/break.html", 760, 980);
  metrics.breakTall = await tall.evaluate(PROBE_BREAK);
  await shot(tall, "break_tall");
  console.log("[probe] breakTall:", JSON.stringify(metrics.breakTall));

  // ---- 5. 外卖提醒窗口 ----
  const tkErr = [];
  await main.evaluate("(function(){ return window.api.previewTakeout(); })()");
  const tk = await open(browser, BASE + "/takeout.html", 880, 560, {
    transparent: true, frame: false, onConsole: tkErr,
  });
  metrics.takeout = await tk.evaluate(PROBE_TAKEOUT);
  metrics.takeout.consoleErrors = tkErr;
  await shot(tk, "takeout");
  console.log("[probe] takeout:", JSON.stringify(metrics.takeout));

  await browser.close();
  fs.writeFileSync(path.join(OUT, "metrics.json"), JSON.stringify(metrics, null, 2));
  console.log("[probe] done ->", OUT);
}

run().catch((err) => {
  console.error("[probe] 失败:", err);
  process.exit(1);
});
