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
 * 系统 Edge 的 channel=msedge，无需下载额外浏览器）。不再依赖 上一版 ——
 * 上一版旧工程已删除，这里用 Playwright 等价替代原来的 BrowserWindow +
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
  // 上一版探针一致，这里只对 transparent 页面用透明背景，其余用浅灰。
  if (o.transparent) {
    // Playwright 截图默认白底；透明窗口在 上一版 里截出来是透明底。
    // 这里用 addStyleTag 把 body 背景设成固定色，保证截图可判读（probe_renderer.py
    // 只断言「非空且非白屏」，不比对透明通道）。
  }
  if (o.preload) {
    // 原 上一版 preload 注入 window.__WR_WIN="settings"：用 addInitScript 等价实现
    const preloadSrc = fs.readFileSync(o.preload, "utf-8");
    await page.addInitScript(preloadSrc);
  }
  // consoleErrors：把页面里的 JS 错误收集起来给断言用。
  // 必须**在 goto 之前**挂上——设置页是 216KB 的一整坨脚本，出错就在加载那一刻。
  // 拦截 favicon：浏览器会自动请求 /favicon.ico，而 Go 服务的资源里只有 icon.png
  // （前端产物没有 favicon.ico），会 404。上一版时代这条被 webContents 吞掉，
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
  // 自绘下拉（.cdrop-host）没有 .value，当前值挂在宿主的 dataset.value 上
  var dval = function (id) { var e = document.getElementById(id); return e ? (e.dataset.value || null) : null; };
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
    themeOptions: Array.prototype.map.call(
      document.querySelectorAll('input[name=theme]'),
      function (el) { return el.value; }
    ),
    themeChecked: (function () {
      var el = document.querySelector('input[name=theme]:checked');
      return el ? el.value : null;
    })(),
    bodyClass: document.body.className,
    // 侧栏状态格（下次外卖/自定义提醒）：名称在左、时间在右，两段都必须单行显示。
    // 采集每格的实际字号、高度与溢出量，供 probe 断言「没换行 / 没裁字 / 没挤出卡片」。
    statusCells: Array.prototype.map.call(
      document.querySelectorAll('#data-status .status-cell'),
      function (c) {
        var k = c.querySelector('.k'), v = c.querySelector('.v');
        var kcs = k ? getComputedStyle(k) : null;
        return {
          k: k ? k.textContent : '',
          v: v ? v.textContent : '',
          kOver: k ? k.scrollWidth - k.clientWidth : 0,
          vOver: v ? v.scrollWidth - v.clientWidth : 0,
          cellOver: c.scrollWidth - c.clientWidth,
          kH: k ? Math.round(k.getBoundingClientRect().height * 10) / 10 : 0,
          kFs: kcs ? parseFloat(kcs.fontSize) : 0,
          width: Math.round(c.getBoundingClientRect().width),
        };
      }
    ),
    apOpacity: val('ap-opacity'),
    apOpacityStep: (function () {
      var el = document.getElementById('ap-opacity');
      return el ? String(el.step) : null;
    })(),
    apOpacityRange: (function () {
      var el = document.getElementById('ap-opacity');
      return el ? [String(el.min), String(el.max)] : null;
    })(),
    apWidthPct: val('ap-widthPct'),
    apHeightPct: val('ap-heightPct'),
    apFullscreen: chk('ap-fullscreen'),
    apAllScreens: chk('ap-allScreens'),
    sndVolume: val('snd-volume'),
    sndMode: val('snd-mode'),
    shortMode: dval('short-mode'),
    shortInterval: val('short-interval'),
    shortDuration: val('short-duration'),
    shortNotify: val('short-notify'),
    shortPostpone: val('short-postpone'),
    longMode: dval('long-mode'),
    longDuration: val('long-duration'),
    longEveryN: val('longEveryNShort'),
    tkEnabled: chk('tk-enabled'),
    tkLead: val('tk-lead'),
    tkPostpone: val('tk-postpone'),
    supEnabled: chk('sup-enabled'),
    supSelection: (function () { var b = document.getElementById('sup-selection-btn'); return b ? (b.dataset.mode || null) : null; })(),
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
// 独立反推立绘「去掉透明部分后」的可见边——完全不依赖页面 layoutSupDialog 的公式：
// ① 从图片自然像素扫 alpha bbox；② 读 .trim 的计算变换矩阵（translate+scale，
//    不含 img 自身的二阶段缩放）；③ 内容在 .trim 局部坐标的 bbox（img 盒铺满
//    .trim，object-fit:contain + 底对齐 ⇒ 绘制区高 fill*H 贴盒底）；④ 按
//    transform-origin: left bottom 做原点修正，映射到窗口坐标。
// 用于对拍 supMetrics 的 figLeft/figRight/figTop（同基准：figure 父容器相对坐标）。
// 假设：.gaze 跟随层无位移（探针不触发鼠标活动 ⇒ 恒等变换）；水平翻转按 sup-flip 处理。
// ⚠️ 拼接方式：本常量是**函数声明**，只能拼进 IIFE 内部（见 PROBE_BREAK / PROBE_TAKEOUT
// 的 `(function () {` + FIG_VISIBLE_FN + 后续体）。绝不能当前缀拼在整串最前面——
// page.evaluate 的表达式若以 function 开头，Playwright 的 normalizeEvaluationExpression
// 会按 /^(async)?\s*function(\s|\()/ 给它包一层括号，「函数声明 + IIFE」被并成单个
// 表达式 E1(E2)()：helper 先被执行、其返回值（对象）再被当函数调用，直接 TypeError
// "(intermediate value)(...) is not a function"（2026-10-14 踩坑：整串以 function
// 开头 ⇒ 探针 break 段整个挂掉）。
const FIG_VISIBLE_FN = `function figureVisibleProbe() {
  var figure = document.getElementById('figure');
  var host = document.getElementById('figure-trim');
  var img = document.getElementById('figure-img');
  if (!figure || !host || !img || !img.naturalWidth || !img.naturalHeight) return null;
  var cw = img.naturalWidth, ch = img.naturalHeight;
  var cv = document.createElement('canvas');
  cv.width = cw; cv.height = ch;
  var ctx = cv.getContext('2d');
  ctx.drawImage(img, 0, 0);
  var data;
  try { data = ctx.getImageData(0, 0, cw, ch).data; } catch (e) { return null; }
  var x0 = cw, y0 = ch, x1 = -1, y1 = -1;
  for (var y = 0; y < ch; y++) {
    for (var x = 0; x < cw; x++) {
      if (data[(y * cw + x) * 4 + 3] >= 8) {
        if (x < x0) x0 = x;
        if (x > x1) x1 = x;
        if (y < y0) y0 = y;
        if (y > y1) y1 = y;
      }
    }
  }
  if (x1 < 0) return null;
  var cs = getComputedStyle(host);
  var m = cs.transform;
  var a = 1, b = 0, c = 0, d = 1, e = 0, f = 0;
  if (m && m !== 'none') {
    if (m.indexOf('matrix(') !== 0) return null;
    var parts = m.slice(7, m.length - 1).split(',');
    if (parts.length !== 6) return null;
    a = parseFloat(parts[0]); b = parseFloat(parts[1]); c = parseFloat(parts[2]);
    d = parseFloat(parts[3]); e = parseFloat(parts[4]); f = parseFloat(parts[5]);
  }
  var fb = figure.getBoundingClientRect();
  var W = fb.width, H = fb.height;
  if (W <= 0 || H <= 0) return null;
  var ar = cw / ch;
  var fill = Math.min(1, W / (ar * H));
  var flipped = document.body.classList.contains('sup-flip');
  var paintH = fill * H;
  var lx0 = flipped ? W - ((x1 + 1) / cw) * W : (x0 / cw) * W;
  var lx1 = flipped ? W - (x0 / cw) * W : ((x1 + 1) / cw) * W;
  var ly0 = H - paintH + (y0 / ch) * paintH;
  var ly1 = H - paintH + ((y1 + 1) / ch) * paintH;
  var ox = 0, oy = H;
  var wx0 = fb.left + ox + a * (lx0 - ox) + c * (ly0 - oy) + e;
  var wy0 = fb.top + oy + b * (lx0 - ox) + d * (ly0 - oy) + f;
  var wx1 = fb.left + ox + a * (lx1 - ox) + c * (ly1 - oy) + e;
  var wy1 = fb.top + oy + b * (lx1 - ox) + d * (ly1 - oy) + f;
  var cr = figure.parentElement ? figure.parentElement.getBoundingClientRect() : { left: 0, top: 0 };
  return {
    left: Math.round(wx0 - cr.left), right: Math.round(wx1 - cr.left),
    top: Math.round(wy0 - cr.top), bottom: Math.round(wy1 - cr.top),
    alpha: { x0: x0, y0: y0, x1: x1, y1: y1, cw: cw, ch: ch },
    matrix: { a: a, d: d, e: e, f: f },
    figBox: { w: Math.round(W), h: Math.round(H) },
    fill: Math.round(fill * 1000) / 1000,
    flipped: flipped
  };
}`;
const PROBE_BREAK = `(function () {
` + FIG_VISIBLE_FN + `
  var app = document.getElementById('app');
  var cs = getComputedStyle(document.documentElement);
  var line = document.getElementById('supervisor-line');
  var img = document.getElementById('figure-img');
  // 督休专员版式（游戏对话画面）：layoutSupDialog 把倒计时环的摆位写进 .bar 的
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
  var nameEl = document.getElementById('dialog-name');
  var nameRect = nameEl ? nameEl.getBoundingClientRect() : null;
  return {
    shim: window.__WR_SHIM || null,
    hasSupervisor: document.body.classList.contains('has-supervisor'),
    hasSup: document.body.classList.contains('has-supervisor'),
    viewportW: window.innerWidth,
    viewportH: window.innerHeight,
    figureHidden: document.getElementById('figure').classList.contains('hidden'),
    // 前缀留长一点：立绘可能是 png 也可能是 webp，22 字符会把 ";base64," 截掉
    figureSrcPrefix: img ? String(img.src).slice(0, 40) : null,
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
    appBlRadius: app ? getComputedStyle(app).borderBottomLeftRadius : null,
    appBrRadius: app ? getComputedStyle(app).borderBottomRightRadius : null,
    badgeHidden: document.getElementById('preview-badge').classList.contains('hidden'),
    supMetrics: supMetrics,
    figureVisible: (typeof figureVisibleProbe === 'function' ? figureVisibleProbe() : null),
    contentInline: content ? {
      rectW: Math.round(contentRect.width), rectH: Math.round(contentRect.height),
      rectLeft: Math.round(contentRect.left), rectRight: Math.round(contentRect.right),
      rectBottom: Math.round(contentRect.bottom),
      blRadius: getComputedStyle(content).borderBottomLeftRadius,
      brRadius: getComputedStyle(content).borderBottomRightRadius,
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
    // 名字标签：横屏 left:56px、竖屏 left:46px（与外卖窗口同口径）
    dialogNameLeft: nameRect ? Math.round(nameRect.left) : null,
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
    postponeShown: shown('pb-postpone'),
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
const PROBE_STATS = `(async function () {
  // 后端 today 原值：用来核对「今天卡」四项（完成 / 推迟 / 跳过 / 提前结束）的渲染口径
  var bk = { shortCount: 0, longCount: 0, customCount: 0, endEarlyCount: 0, postponeCount: 0, skipCount: 0 };
  try {
    var gs = await window.api.getStats();
    var tt = (gs && gs.today) || {};
    bk = {
      shortCount: tt.shortCount || 0,
      longCount: tt.longCount || 0,
      customCount: tt.customCount || 0,
      endEarlyCount: tt.endEarlyCount || 0,
      postponeCount: tt.postponeCount || 0,
      skipCount: tt.skipCount || 0
    };
  } catch (e) { bk.err = String(e); }
  var plot = document.getElementById('rc-plot');
  var svg = plot ? plot.querySelector('svg') : null;
  var marks = svg ? svg.querySelectorAll('rect, path, line, circle, polyline, text').length : 0;
  var dateEl = document.getElementById('stats-date');
  // 「今天」卡已并入构成卡并改名「今日看板」：统计页应只剩 4 张卡，且首张标题就是「今日看板」
  var cards = document.querySelectorAll('#tab-stats > .card');
  var firstH3 = cards.length ? cards[0].querySelector('h3') : null;
  var comp = (function () {
    var box = document.getElementById('stats-comp');
    if (!box) return null;
    var segs = box.querySelectorAll('.comp-stack .seg');
    var vis = 0;
    for (var j = 0; j < segs.length; j++) {
      if (getComputedStyle(segs[j]).display !== 'none') vis++;
    }
    var bars = box.querySelectorAll('.cbar');
    var bv = [];
    var bl = [];
    for (var q = 0; q < bars.length; q++) {
      var cell = bars[q].querySelector('.bv');
      bv.push(cell ? cell.textContent.trim() : '?');
      var lb = bars[q].querySelector('.bl');
      bl.push(lb ? lb.textContent.trim() : '?');
    }
    var tot = document.getElementById('comp-total');
    return {
      panels: box.querySelectorAll('.comp-panel').length,
      segs: segs.length,
      visibleSegs: vis,
      bars: bars.length,
      barVals: bv,
      barLabels: bl,
      total: tot ? tot.textContent.trim() : null,
      // 久坐面板底部的小结（把「最长一段」翻译成有没有坐过头）
      sedNote: (document.getElementById('comp-sed-note') || {}).textContent || '',
      // 今天卡的说明图标：用户要求全部删掉（时间构成 / 休息动作 / 久坐），应为 0
      qmarks: box.querySelectorAll('.comp-h i, .comp-h .q-mark').length
    };
  })();
  // 休息报表：表头列（含排序箭头，判据用 startsWith）与图例项
  var rcTable = document.getElementById('rc-table');
  var rcHeads = [];
  if (rcTable) {
    var ths = rcTable.querySelectorAll('tr:first-child th');
    for (var t = 0; t < ths.length; t++) rcHeads.push(ths[t].textContent.trim());
  }
  var restLegend = [];
  var lgItems = document.querySelectorAll('#rc-legend .rc-legend-item');
  for (var lg = 0; lg < lgItems.length; lg++) restLegend.push(lgItems[lg].textContent.trim());
  return {
    rcHeads: rcHeads,
    restLegend: restLegend,
    date: dateEl ? dateEl.textContent : null,
    cards: cards.length,
    firstTitle: firstH3 ? firstH3.textContent.trim() : null,
    comp: comp,
    backend: bk,
    svg: !!svg,
    marks: marks,
    tableRows: document.querySelectorAll('.rest-chart table tbody tr').length,
    errs: window.__WR_ERRORS || []
  };
})()`;
const NOZOOM_PROBE = `(function () {
  // 禁缩放守卫（frontend/dist/nozoom.js）：提醒 / 预览窗口不许手势或滚轮缩放。
  // 这里验三件事：脚本装上了（window.__WR_NOZOOM）、touch-action 里去掉了
  // pinch-zoom、各类缩放入口都被拦下但普通滚轮没被拦死（活动上报还指着它）。
  var out = {
    loaded: window.__WR_NOZOOM === true,
    touchAction: "",
    bodyTouchAction: null,
    ctrlWheelBlocked: false,
    plainWheelPassed: false,
    ctrlKeyBlocked: false,
    multiTouchBlocked: false
  };
  out.touchAction = getComputedStyle(document.documentElement).touchAction;
  out.bodyTouchAction = document.body ? getComputedStyle(document.body).touchAction : null;
  // 合成事件派发到 <html>：document 上的 capture 监听器一定会走到。
  var wheel = function (init) {
    var e = new WheelEvent("wheel", { bubbles: true, cancelable: true, deltaY: init.deltaY, ctrlKey: !!init.ctrlKey });
    document.documentElement.dispatchEvent(e);
    return e;
  };
  out.ctrlWheelBlocked = wheel({ ctrlKey: true, deltaY: -120 }).defaultPrevented === true;
  out.plainWheelPassed = wheel({ deltaY: -120 }).defaultPrevented === false;
  var k = new KeyboardEvent("keydown", { key: "=", ctrlKey: true, bubbles: true, cancelable: true });
  document.documentElement.dispatchEvent(k);
  out.ctrlKeyBlocked = k.defaultPrevented === true;
  // 双指触摸：TouchEvent 在无触屏的无头浏览器里构造不出来，用一个普通 Event
  // 挂上 touches 即可触发守卫里的 touches.length > 1 分支。
  var t = new Event("touchmove", { bubbles: true, cancelable: true });
  t.touches = [{}, {}];
  document.documentElement.dispatchEvent(t);
  out.multiTouchBlocked = t.defaultPrevented === true;
  return out;
})()`;
const PROBE_TAKEOUT = `(async function () {
` + FIG_VISIBLE_FN + `
  // 量布局前等字体加载 + 两帧：红包框 width:max-content 的宽度依赖 .rp-info 文字
  // 渲染宽度，字体未加载时会量到 fallback 字体的宽度（与最终不一致），导致摆位
  // left 与渲染后的红包框中心对不上。等 fonts.ready + rAF 让 RO 触发的 relayout
  // 跑完，量到的就是真实用户看到的状态。
  if (document.fonts && document.fonts.ready) { await document.fonts.ready; }
  await new Promise(function(r){ requestAnimationFrame(function(){ requestAnimationFrame(r); }); });
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
    clock: txt('rp-time'),
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
    postponeShown: shown('tk-postpone'),
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
    figureVisible: (typeof figureVisibleProbe === 'function' ? figureVisibleProbe() : null),
    contentRect: (function () {
      var c = document.querySelector('.content');
      if (!c) return null;
      var r = c.getBoundingClientRect();
      var ccs = getComputedStyle(c);
      return { h: Math.round(r.height), left: Math.round(r.left), right: Math.round(r.right),
               top: Math.round(r.top), bottom: Math.round(r.bottom), offsetTop: Math.round(c.offsetTop),
               blRadius: ccs.borderBottomLeftRadius, brRadius: ccs.borderBottomRightRadius };
    })(),
    appBlRadius: (function () {
      var a = document.getElementById('app');
      return a ? getComputedStyle(a).borderBottomLeftRadius : null;
    })(),
    appBrRadius: (function () {
      var a = document.getElementById('app');
      return a ? getComputedStyle(a).borderBottomRightRadius : null;
    })(),
    redpacketInline: (function () {
      var rp = document.getElementById('redpacket');
      if (!rp) return null;
      var r = rp.getBoundingClientRect();
      return { left: rp.style.left, bottom: rp.style.bottom, shown: !rp.classList.contains('hidden'),
               rectLeft: Math.round(r.left), rectTop: Math.round(r.top),
               rectRight: Math.round(r.right), rectBottom: Math.round(r.bottom) };
    })(),
    rpTitleInline: (function () {
      var t = document.querySelector('.rp-title');
      if (!t) return null;
      var r = t.getBoundingClientRect();
      return { shown: !!(r.width > 0 && r.height > 0),
               rectLeft: Math.round(r.left), rectTop: Math.round(r.top),
               rectRight: Math.round(r.right), rectBottom: Math.round(r.bottom) };
    })(),
    accent: cs.getPropertyValue('--accent').trim(),
    rounded: (document.getElementById('app') || {}).dataset ? document.getElementById('app').dataset.rounded : null,
    errors: window.__WR_ERRORS || []
  };
})()`;

// ---- 主流程：Playwright 驱动 Edge，业务顺序与旧版 上一版探针一致 ----
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

  // ---- 0a. 侧栏状态格超长名称压力：注入 20 字名字，验证「缩字号 → 中间省略」链路。
  //      三条底线：不换行、不裁半截字（scrollWidth 不超 clientWidth）、不溢出卡片。
  //      测完把原文还原，免得影响后面的截图与页签断言。
  await set.evaluate(() => {
    const cells = document.querySelectorAll("#data-status .status-cell");
    const cell = cells[cells.length - 1];
    const k = cell.querySelector(".k"), v = cell.querySelector(".v");
    window.__wrStress = { k: k.textContent, v: v.textContent };
    k.style.fontSize = ""; v.style.fontSize = ""; cell.style.columnGap = "";
    delete cell.dataset.fitDone;
    delete k.dataset.full;
    delete k.dataset.truncated;
    k.textContent = "下次一二三四五六七八九十一二三四五六提醒"; // 20 字
    v.textContent = "今日已无提醒";
  });
  await delay(400);
  metrics.statusStress = await set.evaluate(() => {
    const cells = document.querySelectorAll("#data-status .status-cell");
    const cell = cells[cells.length - 1];
    const k = cell.querySelector(".k"), v = cell.querySelector(".v");
    const r = {
      text: k.textContent,
      kFs: parseFloat(getComputedStyle(k).fontSize),
      kOver: k.scrollWidth - k.clientWidth,
      vOver: v.scrollWidth - v.clientWidth,
      cellOver: cell.scrollWidth - cell.clientWidth,
      kH: Math.round(k.getBoundingClientRect().height * 10) / 10,
      title: k.getAttribute("title") || "",
    };
    k.textContent = window.__wrStress.k;
    v.textContent = window.__wrStress.v;
    delete cell.dataset.fitDone;
    return r;
  });
  console.log("[probe] statusStress:", JSON.stringify(metrics.statusStress));
  await delay(300);

  // ---- 0a2. 暂停态文案：手动暂停、自动暂停（空闲 / 勿扰 / 应用占用）在状态格里
  //      统一显示「暂停休息」，原因只留在 title 上（用户 2026-10-24 要求去掉长尾巴）。
  await set.evaluate(() => window.api.action("pauseToggle"));
  await delay(700);
  metrics.pauseStatus = await set.evaluate(() => {
    const cell = document.querySelector("#data-status .status-cell");
    return {
      k: cell.querySelector(".k").textContent,
      v: cell.querySelector(".v").textContent,
      title: cell.getAttribute("title") || "",
    };
  });
  console.log("[probe] pauseStatus:", JSON.stringify(metrics.pauseStatus));
  await set.evaluate(() => window.api.action("pauseToggle")); // 复原，别影响后续断言
  await delay(600);
  metrics.pauseResumed = await set.evaluate(
    () => document.querySelector("#data-status .status-cell .v").textContent,
  );
  console.log("[probe] pauseResumed:", JSON.stringify(metrics.pauseResumed));

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
  const pre = await open(browser, BASE + "/prebreak.html?postpone=1", 400, 240, {
    transparent: true, frame: false, onConsole: pbErr,
  });
  await delay(700);
  metrics.preBreak = await pre.evaluate(PROBE_PREBREAK);
  metrics.preBreak.consoleErrors = pbErr;
  await shot(pre, "prebreak");
  console.log("[probe] preBreak:", JSON.stringify(metrics.preBreak));
  // 禁缩放守卫（提醒窗口 / 预览窗口通用，见 frontend/dist/nozoom.js）
  metrics.nozoom = {};
  metrics.nozoom.prebreak = await pre.evaluate(NOZOOM_PROBE);
  console.log("[probe] nozoom/prebreak:", JSON.stringify(metrics.nozoom.prebreak));

  // ---- 1c. 提前提示窗：自定义提醒版式 ----
  // supervisor:{enabled:true}：探针断言（has-supervisor 版式 / 立绘解码）以「启用」为前提，
  // 必须在隔离副本上显式打开，不跟随用户真实配置的开关状态。
  await main.evaluate(
    "(function(){ return window.api.saveSettings({" +
      "short:{enabled:false}, long:{enabled:false}, takeout:{enabled:false}," +
      "supervisor:{enabled:true}," +
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
    BASE + "/prebreak.html?custom=1&tail=" + encodeURIComponent(" 秒显示喝水提醒") + "&postpone=1",
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
  metrics.nozoom.break = await wide.evaluate(NOZOOM_PROBE);
  console.log("[probe] nozoom/break:", JSON.stringify(metrics.nozoom.break));

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
  metrics.nozoom.takeout = await tk.evaluate(NOZOOM_PROBE);
  console.log("[probe] nozoom/takeout:", JSON.stringify(metrics.nozoom.takeout));

  // ---- 5b. 「可推迟」为 0：提前提示窗与外卖窗口都不显示「推迟」按钮 ----
  await main.evaluate(
    "(function(){ return window.api.saveSettings({" +
      "short:{postponeMs:0}, takeout:{postponeMin:0}}); })()",
  );
  await delay(1200);
  const pbZErr = [];
  const preZ = await open(
    browser,
    BASE + "/prebreak.html?custom=0&postpone=0&tail=" + encodeURIComponent(" 秒进入休息"),
    400, 240,
    { transparent: true, frame: false, onConsole: pbZErr },
  );
  await delay(700);
  metrics.preBreakNoPostpone = await preZ.evaluate(PROBE_PREBREAK);
  metrics.preBreakNoPostpone.consoleErrors = pbZErr;
  await shot(preZ, "prebreak_no_postpone");
  console.log("[probe] preBreakNoPostpone:", JSON.stringify(metrics.preBreakNoPostpone));

  await main.evaluate("(function(){ return window.api.previewTakeout(); })()");
  const tkZErr = [];
  const tkZ = await open(browser, BASE + "/takeout.html", 880, 560, {
    transparent: true, frame: false, onConsole: tkZErr,
  });
  metrics.takeoutNoPostpone = await tkZ.evaluate(PROBE_TAKEOUT);
  metrics.takeoutNoPostpone.consoleErrors = tkZErr;
  await shot(tkZ, "takeout_no_postpone");
  console.log("[probe] takeoutNoPostpone:", JSON.stringify(metrics.takeoutNoPostpone));
  // 还原配置（收尾 / 后续场景回到默认态）
  await main.evaluate(
    "(function(){ return window.api.saveSettings({" +
      "short:{postponeMs:120000}, takeout:{postponeMin:10}}); })()",
  );
  await delay(800);

  await browser.close();
  fs.writeFileSync(path.join(OUT, "metrics.json"), JSON.stringify(metrics, null, 2));
  console.log("[probe] done ->", OUT);
}

run().catch((err) => {
  console.error("[probe] 失败:", err);
  process.exit(1);
});
