/**
 * 题名 + 墨块「极端用例」探针（数据采集端）。
 *
 * 与另外两个题名探针的分工：
 *   probe_role_title.js        —— 15 位内置专员 × 3 版式，验“真实立绘下不压脸”
 *   role_title_stress.js       —— 26 组极端**名字** × 3 版式，验“任何名字不越界”
 *   本文件                      —— 极端**立绘** + 极端**窗口** + 极端**字形簇**，
 *                                 验“用户上传任何图 / 任何系统设置都不崩、不错位”
 *
 * 为什么要有第三份：前两份都拿内置 `assets/supervisors` 当输入，立绘的 alpha 形状、
 * 宽高比、可见区位置全在“正常范围”内。用户能导入任意 PNG/WebP（全透明、无 alpha、
 * 1×1、全景、只有下半身、人物贴左缘……），还能拖窗口到 420×300、改 DPI、翻转立绘。
 * 这些才是“位置必须动态计算”这句话真正要覆盖的输入空间。
 *
 * 立绘全部由 probe_name_extremes.py 现场生成到临时目录，经 `WR_SUPERVISORS_DIR`
 * 指给服务 —— 走的是**用户导入**的同一条 ScanRoles / ToDataURL / <img src> 路径，
 * 不是给测试开后门。名字仍走生产重排入口（dataset.fullName + textContent + resize）。
 *
 * 采集字段全部来自 DOM / 计算样式，不调内部函数；判定在 probe_name_extremes.py。
 */
const { chromium } = require("playwright");
const fs = require("fs");
const path = require("path");

const BASE = process.env.WR_BASE;
const OUT = process.env.WR_OUT;
const ROLE = process.env.WR_ROLE;
const KIND = process.env.WR_KIND || "normal";
const NAME_SET = process.env.WR_NAME_SET || "full";
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

const SEG = (() => {
  try { return new Intl.Segmenter(undefined, { granularity: "grapheme" }); } catch (e) { return null; }
})();
const graphemes = (s) => (SEG ? Array.from(SEG.segment(s), (x) => x.segment) : Array.from(s));

const cjk = (n) => {
  let s = "";
  for (let i = 0; i < n; i++) s += String.fromCharCode(0x4e00 + ((i * 37) % 1800));
  return s;
};

// 字形簇矩阵：每一项都是“一个用户可能真的输入的名字”。
// 重点不是长度，而是**一个视觉字符由几个码点组成**——省略若按码点切，就会把
// 家庭 emoji 拆成一串单人、把国旗拆成两个字母、把组合音标拆成孤立的声调符。
const NAMES = [
  ["1字", "王"],
  ["3字", "吃饼人"],
  ["10字", "图书馆角落的温柔学姐"],
  ["30字-UI上限", cjk(30)],
  ["60字-Go上限", cjk(60)],
  ["200字", cjk(200)],
  ["ZWJ家庭×20", "👨‍👩‍👧‍👦".repeat(20)],
  ["国旗×20", "🇨🇳🇯🇵🇺🇸🇬🇧🇫🇷🇩🇪".repeat(20)],
  ["肤色/职业emoji×20", "👍🏽👩🏽‍💻🧑🏿‍🚀".repeat(20)],
  ["组合音标×30", "e\u0301".repeat(30)],
  ["CJK扩展B×20", "𠮷𠀀𪚥".repeat(20)],
  ["RTL阿拉伯文", "مرحبا بالعالم"],
  ["全空白", "   "],
  ["换行制表", "第一行\n第二行\t制表"],
  ["零宽/双向控制", "a\u200b\u200bb\u2066c\u2069"],
  ["HTML片段", "<b>粗体</b>&amp;"],
  ["孤立代理项", "\uD800abc"],
];

const FEW = new Set(["3字", "10字", "国旗×20", "ZWJ家庭×20", "全空白"]);
const ACTIVE = NAME_SET === "few" ? NAMES.filter((n) => FEW.has(n[0])) : NAMES;

const SCENES = [
  ["break", 1100, 720],
  ["break", 760, 980],
  // 产品允许的最小窗口（SettingsWindowMinWidth/MinHeight = 760×600）：空间最紧，
  // 长牌只能靠省略，必须真的跑一遍。
  ["break", 760, 600],
  // 低于产品最小尺寸，只验“不崩、不出窗”，不要求锚点（见 Python 侧 judge）。
  ["break", 420, 300],
  ["takeout", 880, 560],
];
// 4K 只在“正常立绘”上跑一遍：窗口巨大时人物左侧可能有上千像素空白，
// 这是“舒适缝不许无上限外推”的唯一实测点。
if (KIND === "normal") SCENES.push(["break", 2560, 1440]);

const SNAP = `(function(){
  var n = document.getElementById('dialog-name');
  if (!n) return { error: 'no #dialog-name' };
  var fig = document.getElementById('figure');
  var img = document.getElementById('figure-img');
  var app = document.getElementById('app') || document.body;
  var box = document.querySelector('.content');
  var cs = getComputedStyle(n);
  var r = n.getBoundingClientRect();
  var appR = app.getBoundingClientRect();
  var op = n.offsetParent;
  var opr = op ? op.getBoundingClientRect() : { left: 0, top: 0 };
  var lLeft = opr.left + n.offsetLeft;
  var lTop = opr.top + n.offsetTop;
  var padX = (parseFloat(cs.paddingLeft) || 0) + (parseFloat(cs.paddingRight) || 0);
  var padY = (parseFloat(cs.paddingTop) || 0) + (parseFloat(cs.paddingBottom) || 0);
    var cw = n.offsetWidth - padX;
    var fs = parseFloat(cs.fontSize) || 0;
    var rg = document.createRange();
    rg.selectNodeContents(n);
    var tr = rg.getBoundingClientRect();
    // 溢出判据用**渲染后**的内容盒（含二阶段 scale）：Range 的 rect 是渲染坐标，
    // 拿布局宽高去比会在 stage1 的 1.036 上误报 3~4px。
    var cwR = r.width - padX;
    var chR = r.height - padY;
  var trim = null;
  try { trim = JSON.parse((img && img.dataset.trim) || 'null'); } catch (e) {}

  // 竖向锚点用**布局坐标**：渲染 rect 含二阶段 scale，会把牌心比例算成 0.19。
  var geo = null;
  if (trim && img && fig) {
    var fb = fig.getBoundingClientRect();
    var drawH = (trim.scale || 1) * (trim.fill || 1) * fb.height;
    var visTop = fb.bottom - drawH + drawH * (trim.rawTop || 0);
    var visH = Math.max(1, Math.min(fb.bottom, innerHeight) - visTop);
    var top = parseFloat(n.style.top);
    if (isFinite(top) && n.offsetHeight) {
      var rel = top - (visTop - appR.top);
      geo = { rel: rel, h: n.offsetHeight, visH: visH, visTop: visTop - appR.top,
              band: rel / visH, mid: (rel + n.offsetHeight / 2) / visH };
    }
  }
  function layer(el, ancestorRoot) {
    if (!el) return null;
    var p = el;
    while (p && p.parentElement !== ancestorRoot) p = p.parentElement;
    if (!p) return null;
    var z = getComputedStyle(p).zIndex;
    return z === 'auto' ? 0 : Number(z);
  }
  // 墨块 SVG 的 viewBox 必须等于牌子的**实测像素**，否则方向性毛边会被拉伸成橡皮筋。
  var inkVB = (function (bg) {
    var i = (bg || '').indexOf('base64,');
    if (i < 0) return null;
    try {
      var s = bg.slice(i + 7).split('"')[0].split(')')[0];
      var dec = atob(s);
      var m = dec.match(/viewBox="0 0 ([0-9.]+) ([0-9.]+)"/);
      return m ? [Math.round(+m[1]), Math.round(+m[2])] : null;
    } catch (e) { return null; }
  })(cs.backgroundImage || '');

  var seg = null;
  try { seg = new Intl.Segmenter(undefined, { granularity: 'grapheme' }); } catch (e) {}
  var cut = function (s) { return seg ? Array.from(seg.segment(s), function (x) { return x.segment; }) : Array.from(s); };
  var full = n.dataset.fullName || n.textContent || '';

  return {
    positioned: n.dataset.rt === '1',
    hiddenClass: n.classList.contains('hidden'),
    visible: cs.visibility === 'visible' && r.width > 0 && r.height > 0,
    text: n.textContent,
    fullName: n.dataset.fullName != null ? n.dataset.fullName : null,
    elided: n.dataset.rtElided === '1',
    fs: n.dataset.rtFs != null ? Number(n.dataset.rtFs) : null,
    fsComputed: Math.round(fs * 100) / 100,
    hit: n.dataset.rtHit != null ? Number(n.dataset.rtHit) : null,
    ov: n.dataset.rtOv != null ? Number(n.dataset.rtOv) : null,
    relax: n.dataset.rtRelax === '1',
    toe: n.dataset.rtToe === '1',
    gap: n.dataset.rtGap != null ? Number(n.dataset.rtGap) : null,
    slack: n.dataset.rtSlack != null ? Number(n.dataset.rtSlack) : null,
    vertical: cs.writingMode === 'vertical-rl',
    singleColumn: cw <= (fs || 16) * 1.75,
    contentWidth: Math.round(cw),
    overflow: tr.width > cwR + 6.5 || tr.height > chR + 6.5,
    box: { w: n.offsetWidth, h: n.offsetHeight },
    lLeft: Math.round(lLeft * 100) / 100,
    lTop: Math.round(lTop * 100) / 100,
    lRight: Math.round((lLeft + n.offsetWidth) * 100) / 100,
    lBottom: Math.round((lTop + n.offsetHeight) * 100) / 100,
    // 牌底的**渲染坐标**。lBottom 走 offsetTop 链（布局值，不含 transform / scale），
    // 而 dialogTop 走 getBoundingClientRect（渲染值，含 scale / translate）——
    // 两个坐标系混着相减，间隙会凭空少掉几像素。判“牌有没有压进对话框”要两边同源。
    rBottom: Math.round((r.bottom - appR.top) * 100) / 100,
    rLeft: Math.round(r.left * 100) / 100,
    rTop: Math.round(r.top * 100) / 100,
    winW: innerWidth,
    winH: innerHeight,
    appW: Math.round(appR.width),
    appH: Math.round(appR.height),
    appTop: Math.round(appR.top * 100) / 100,
    padTop: parseFloat(cs.paddingTop) || 0,
    dialogTop: box ? Math.round((box.getBoundingClientRect().top - appR.top) * 100) / 100 : null,
    mid: geo ? Math.round(geo.mid * 1000) / 1000 : null,
    band: geo ? Math.round(geo.band * 1000) / 1000 : null,
    visTop: geo ? Math.round(geo.visTop * 100) / 100 : null,
    visH: geo ? Math.round(geo.visH * 100) / 100 : null,
    plateH: geo ? geo.h : null,
    scale: cs.scale || 'none',
    transformOrigin: cs.transformOrigin,
    nameLayer: layer(n, n.parentElement),
    figLayer: layer(fig, n.parentElement),
    inkType: (cs.backgroundImage || '').indexOf('data:image/svg+xml') >= 0 ? 'data:seal'
      : ((cs.backgroundImage || '').indexOf('linear-gradient') >= 0 ? 'css-fallback' : 'none'),
    inkVB: inkVB,
    sealKey: n.dataset.sealKey || null,
    childNodes: n.children.length,
    dpr: devicePixelRatio,
    graphemes: { full: cut(full), shown: cut(n.textContent) },
    img: (function () {
      if (!img || !img.naturalWidth) return { w: 0, h: 0, hasInk: false, headInk: 0, inkPct: 0 };
      var key = 'k' + img.naturalWidth + 'x' + img.naturalHeight + '|' + img.src.length;
      window.__wrImgCache = window.__wrImgCache || {};
      if (window.__wrImgCache[key]) return window.__wrImgCache[key];
      var W = 120, H = Math.max(1, Math.round(img.naturalHeight * W / img.naturalWidth));
      var cv = document.createElement('canvas');
      cv.width = W; cv.height = H;
      var ctx = cv.getContext('2d', { willReadFrequently: true });
      var out = { w: img.naturalWidth, h: img.naturalHeight, hasInk: false, headInk: 0,
                  inkPct: 0, leftPct: 1, rightPct: 0 };
      try {
        ctx.drawImage(img, 0, 0, W, H);
        var d = ctx.getImageData(0, 0, W, H).data;
        var ink = 0, head = 0, minX = W, maxX = -1;
        for (var y = 0; y < H; y++) {
          var rowInk = false;
          for (var x = 0; x < W; x++) {
            if (d[(y * W + x) * 4 + 3] >= 32) {
              ink++; rowInk = true;
              if (x < minX) minX = x;
              if (x > maxX) maxX = x;
            }
          }
          if (rowInk && y < H * 0.26) head++;
        }
        out.hasInk = ink > 0;
        out.headInk = head;
        out.inkPct = Math.round((ink / (W * H)) * 1000) / 10;
        out.leftPct = minX < W ? Math.round((minX / W) * 1000) / 1000 : 1;
        out.rightPct = maxX >= 0 ? Math.round((maxX / W) * 1000) / 1000 : 0;
      } catch (e) {}
      window.__wrImgCache[key] = out;
      return out;
    })(),
  };
})()`;

function newErrors(page) {
  const errs = [];
  page.on("pageerror", (e) => errs.push(String(e)));
  return errs;
}

async function ready(page, tries = 150) {
  for (let i = 0; i < tries; i++) {
    const ok = await page.evaluate(() => {
      const img = document.getElementById("figure-img");
      const fig = document.getElementById("figure");
      return !!(img && img.complete && img.dataset.trim && fig && !fig.classList.contains("hidden"));
    });
    if (ok) return true;
    await sleep(100);
  }
  return false;
}

async function applyName(page, name) {
  const sync = await page.evaluate((v) => {
    const n = document.getElementById("dialog-name");
    if (!n) return { error: "no #dialog-name" };
    const t0 = performance.now();
    n.dataset.fullName = v;
    n.textContent = v;
    ["rt", "rtElided", "rtFs", "rtHit", "rtOv", "rtRelax", "rtToe", "rtGap", "rtSlack"]
      .forEach((k) => delete n.dataset[k]);
    void n.offsetWidth;
    window.dispatchEvent(new Event("resize"));
    return { syncMs: Math.round((performance.now() - t0) * 100) / 100 };
  }, name);
  // 稀有字/emoji 的回落字体可能在这一两帧里才解析完；等定位稳定再量。
  for (let i = 0; i < 6; i++) {
    await page.waitForTimeout(60);
    const positioned = await page.evaluate(() => {
      const n = document.getElementById("dialog-name");
      return n && n.dataset.rt === "1";
    });
    if (positioned) break;
  }
  const snap = await page.evaluate(SNAP);
  snap.syncMs = sync.syncMs;
  return snap;
}

async function openScene(browser, main, scene, w, h) {
  if (scene === "takeout") {
    await main.evaluate("(function(){ return window.api.previewTakeout(); })()");
  } else {
    await main.evaluate("(function(){ return window.api.startBreak('short'); })()");
  }
  await sleep(700);
  for (let attempt = 0; attempt < 3; attempt++) {
    const page = await browser.newPage({ viewport: { width: w, height: h } });
    const errs = newErrors(page);
    await page.goto(BASE + "/" + scene + ".html", { waitUntil: "load", timeout: 30000 });
    if (await ready(page)) return { page, errs };
    await sleep(600);
    await page.close().catch(() => {});
  }
  throw new Error("版式起不来（未定位）：" + scene + " " + w + "x" + h);
}

(async () => {
  const browser = await chromium.launch({ channel: "msedge" });
  const preload = fs.readFileSync(process.env.WR_PRELOAD, "utf-8");
  const main = await browser.newPage({ viewport: { width: 1000, height: 760 } });
  await main.addInitScript(preload);
  await main.goto(BASE + "/", { waitUntil: "load", timeout: 30000 });
  for (let i = 0; i < 60; i++) {
    if (await main.evaluate(() => typeof window.api === "object" && window.api !== null)) break;
    await sleep(200);
  }

  const rows = [];
  let base = {};

  for (const [scene, w, h] of SCENES) {
    let handle;
    try {
      handle = await openScene(browser, main, scene, w, h);
    } catch (e) {
      rows.push({ error: String((e && e.message) || e), scene: scene + "@" + w + "x" + h, case: "SCENE" });
      continue;
    }
    const { page, errs } = handle;
    const tag = scene + "@" + w + "x" + h;

    for (const [id, name] of ACTIVE) {
      const before = errs.length;
      let snap;
      try {
        snap = await applyName(page, name);
      } catch (e) {
        rows.push({ error: String((e && e.message) || e), scene: tag, case: id, name: name });
        continue;
      }
      snap.scene = tag;
      snap.case = id;
      snap.name = name;
      snap.nameCp = [...name].length;
      snap.nameG = graphemes(name).length;
      snap.errs = errs.slice(before);
      rows.push(snap);
      if (scene === "break" && w === 1100) base[id] = snap;
    }

    // ---- 附加场景只在横屏 1100×720 上跑（其余版式共用同一份 layoutRoleTitle）----
    // 先用一个 1 字名探一次“这张图到底能不能放名字”。全透明 / 坏图 / 仅下半身 /
    // 关闭显示名称的角色，整块牌子永远不出现 —— 拿它们跑“生成器缺失”“二阶段”
    // 只会得到一堆无意义的红，掩盖真正的问题。
    if (scene === "break" && w === 1100) {
      const can = await applyName(page, "章");
      if (!can.positioned) {
        rows.push({
          scene: tag, case: "附加场景跳过", name: "章", nameCp: 1, nameG: 1,
          skipped: true, reason: "这张立绘上没有可放置的名字", errs: [],
        });
      } else {
        // 1) 墨块生成器缺失：必须退回 CSS 纯色墨块，名字仍可见（不能变成裸字）
        const before = errs.length;
        const sealed = await page.evaluate(() => {
          window.__sealBackup = window.SealGen;
          window.SealGen = undefined;
          // 模拟“生成器从一开始就没加载”：上一枚墨块的 inline 变量必须先清掉，
          // 否则 CSS 仍有旧 data URI，测到的不是兜底路径。
          const n = document.getElementById("dialog-name");
          n.style.removeProperty("--rt-ink");
          delete n.dataset.sealKey;
          return true;
        });
        const fb = await applyName(page, "章");
        rows.push(Object.assign({}, fb, {
          scene: tag, case: "SealGen缺失", name: "章", nameCp: 1, nameG: 1,
          errs: errs.slice(before), sealBackup: sealed,
        }));
        await page.evaluate(() => { window.SealGen = window.__sealBackup; delete window.__sealBackup; });

        // 2) 墨块复用：同名同尺寸连测两次，sealKey 必须不变（没有每帧重画）
        const s1 = await applyName(page, "章");
        const s2 = await applyName(page, "章");
        rows.push(Object.assign({}, s2, {
          scene: tag, case: "墨块复用", name: "章", nameCp: 1, nameG: 1,
          sealKeyFirst: s1.sealKey, errs: [],
        }));

        // 3) 二阶段缩放：牌子在布局坐标里必须留出峰值 1.10 的左缘余量，
        //    放大后渲染左缘不能为负（否则首字被窗口裁掉）。
        const beforeStage = errs.length;
        const st = await page.evaluate(() => {
          const app = document.getElementById("app");
          app.dataset.stage = "2";
          return true;
        });
        await page.waitForTimeout(800);
        const s2stage = await page.evaluate(SNAP);
        rows.push(Object.assign({}, s2stage, {
          scene: tag, case: "二阶段缩放", name: s2stage.fullName || "", nameCp: 0, nameG: 0,
          errs: errs.slice(beforeStage), forcedStage: st,
        }));
        await page.evaluate(() => { document.getElementById("app").dataset.stage = "1"; });
        await page.waitForTimeout(600);

        // 4) 连续 resize 风暴：40 次真实视口变化后回到原位，终态必须与“直接重排”一致
        const beforeStorm = errs.length;
        const sizes = [[900, 640], [1200, 800], [760, 980], [1100, 720]];
        const t0 = Date.now();
        for (let i = 0; i < 40; i++) {
          await page.setViewportSize({ width: sizes[i % 4][0], height: sizes[i % 4][1] });
          await page.waitForTimeout(25);
        }
        await page.setViewportSize({ width: 1100, height: 720 });
        await page.waitForTimeout(500);
        const stormSnap = await page.evaluate(SNAP);
        const stormMs = Date.now() - t0;
        const after = await applyName(page, stormSnap.fullName || "章");
        rows.push(Object.assign({}, stormSnap, {
          scene: tag, case: "resize风暴×40", name: stormSnap.fullName || "", nameCp: 0, nameG: 0,
          stormMs: stormMs, recomputed: after, errs: errs.slice(beforeStorm),
        }));
      }
    }

    await page.close().catch(() => {});
    await sleep(250);
  }

  // ---- DPI 对照：1.25 / 2 / 3 各开一个 context，三个名字与 1x 比对 ----
  // 一张放不下任何名字的立绘（全透明 / 坏图）在 DPI 下同样放不下，比 0 与 0 没有
  // 信息量；直接记跳过，避免“页面未就绪”把真正的 DPI 回归淹掉。
  const hasPos = Object.values(base).some((s) => s && s.positioned === true);
  if (!hasPos) {
    for (const dpr of [1.25, 2, 3]) {
      rows.push({ scene: "dpr" + dpr, case: "附加场景跳过", skipped: true,
                  reason: "1x 下没有任何名字能定位" });
    }
  } else {
    try {
      // 一个 break 会话只活 durationMs；四个版式跑完可能已经超时，DPI 前重开一次。
      await main.evaluate("(function(){ return window.api.startBreak('short'); })()");
      await sleep(500);
      for (const dpr of [1.25, 2, 3]) {
        const ctx = await browser.newContext({ viewport: { width: 1100, height: 720 }, deviceScaleFactor: dpr });
        const p = await ctx.newPage();
        const errs = newErrors(p);
        await p.goto(BASE + "/break.html", { waitUntil: "load", timeout: 30000 });
        if (await ready(p)) {
          for (const id of ["10字", "ZWJ家庭×20", "国旗×20"]) {
            const nm = (NAMES.find((x) => x[0] === id) || ["", ""])[1];
            const s = await applyName(p, nm);
            rows.push(Object.assign({}, s, {
              scene: "dpr" + dpr, case: id, name: nm, nameCp: [...nm].length,
              nameG: graphemes(nm).length, dpr1: base[id] || null, errs: errs.slice(-2),
            }));
          }
        } else {
          rows.push({ error: "DPI 页面未就绪", scene: "dpr" + dpr, case: "SCENE" });
        }
        await ctx.close();
      }
    } catch (e) {
      rows.push({ error: String((e && e.message) || e), scene: "dpr", case: "SCENE" });
    }
  }

  fs.writeFileSync(path.join(OUT, "name_extremes_" + ROLE + ".json"), JSON.stringify(rows, null, 2));
  console.log("WR_JSON " + JSON.stringify(rows));
  await browser.close();
})().catch((e) => {
  console.error("FAIL " + ((e && e.stack) || e));
  process.exit(1);
});
