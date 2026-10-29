/**
 * 验证用截图：打开设置页 → 进入「关于」页 → 截取整个关于内容（浅色 + 深色），
 * 并读出关键字段文本，确认关于页与设置页其它标签页样式一致（统一 .card / .row / .btn 体系）。
 * 仅用于本地验证，不写入真实配置。由 verify_about.py 起服务后调用（WR_BASE / WR_OUT 注入）。
 */
const { chromium } = require("playwright");
const path = require("path");
const fs = require("fs");

const BASE = process.env.WR_BASE || "http://127.0.0.1:29731";
const OUT = process.env.WR_OUT || path.join(process.env.TEMP || ".", "wr_about_shots");
const delay = (ms) => new Promise((r) => setTimeout(r, ms));
const PRELOAD = path.join(__dirname, "probe_preload.js");

async function open(browser, url, w, h, o) {
  const opts = o || {};
  const page = await browser.newPage({ viewport: { width: w, height: h } });
  if (opts.preload) {
    const src = fs.readFileSync(opts.preload, "utf-8");
    await page.addInitScript(src);
  }
  await page.route("**/favicon.ico", (r) => r.fulfill({ status: 204, body: "" }));
  await page.goto(url, { waitUntil: "domcontentloaded" });
  await delay(opts.wait || 1600);
  return page;
}
async function shotEl(page, selector, name) {
  const el = page.locator(selector);
  await el.screenshot({ path: path.join(OUT, name + ".png") });
  console.log("[shot] " + name + " -> " + selector);
}

(async () => {
  fs.mkdirSync(OUT, { recursive: true });
  const browser = await chromium.launch({
    channel: "msedge",
    headless: true,
    args: ["--disable-gpu", "--no-sandbox"],
  });

  const set = await open(browser, BASE + "/settings.html", 1000, 820, { preload: PRELOAD, wait: 1800 });

  // 进入关于页
  await set.evaluate(`(function(){var b=document.querySelector('#nav .nav-item[data-tab="about"]');if(b)b.click();})()`);
  await delay(1500); // 等 appInfo() 异步回填版本 / 运行环境 / 构建时间
  // 截整个 #tab-about 前先回到内容区顶部：切换标签页会保留上一个页面的 scrollTop，
  // 否则 element.screenshot 只截到「视口内那一段」，顶部横幅会被挤出画面。
  await set.evaluate(`(function(){var c=document.getElementById('content');if(c)c.scrollTop=0;})()`);
  await delay(200);

  // 浅色：截整个关于内容（与其它标签页一致：满宽 .card 流）
  await shotEl(set, "#tab-about", "verify_about_light");
  // 视口截图：带上自绘标题栏，用来确认标题栏图标（icon.png 按 20px 显示）没 404
  await set.screenshot({ path: path.join(OUT, "verify_settings_viewport.png") });
  console.log("[shot] verify_settings_viewport -> viewport");

  // 切深色主题
  await set.evaluate(`(function(){var r=document.querySelector('#content input[name=theme][value=dark]');if(r){r.checked=true;r.dispatchEvent(new Event('change',{bubbles:true}));} })()`);
  await delay(800);
  await shotEl(set, "#tab-about", "verify_about_dark");

  // 读出关键字段，确认渲染正确
  const data = await set.evaluate(`(function(){
    var g=function(id){var e=document.getElementById(id);return e?e.textContent:null;};
    var tab=document.getElementById('tab-about');
    var cards=tab.querySelectorAll('.card');
    var firstCard=cards[0];
    // 退出软件按钮是否为红色：与注入的 var(--danger) 参考元素对比
    var quit=document.getElementById('d-quit');
    var ref=document.createElement('span'); ref.style.color='var(--danger)'; ref.style.display='none';
    document.body.appendChild(ref);
    var quitColor = quit ? getComputedStyle(quit).color : null;
    var dangerColor = getComputedStyle(ref).color;
    ref.remove();
    // 数据隐私说明文案：应已去掉「全部功能」
    var noteText='';
    var nr=tab.querySelector('.note .note-row span');
    if(nr) noteText=nr.textContent;
    return {
      appName: g('d-app-name'),
      version: g('d-version-val'),
      runtime: g('d-runtime'),
      build: g('d-build-time'),
      pillOkText: g('d-update-pill-text'),
      updateLineGone: !document.getElementById('d-update-status'),
      copyright: (tab.querySelector('.bottom-bar .copyright')||{}).textContent || "",
      cardCount: cards.length,
      h3Titles: Array.prototype.map.call(tab.querySelectorAll('.card > .card-head h3'), function(h){return h.textContent;}),
      pillCount: tab.querySelectorAll('.pill').length,
      rowsCount: tab.querySelectorAll('.rows .row').length,
      hasAppHead: !!tab.querySelector('.app-head'),
      hasAppIcon: !!tab.querySelector('.app-icon'),
      hasPill: !!tab.querySelector('.pill'),
      hasNote: !!tab.querySelector('.note'),
      hasBespokeBtn: !!tab.querySelector('.btn-soft, .btn-quit'),
      hasCheck: !!document.getElementById('d-checkupdate'),
      hasGo: !!document.getElementById('d-update-go'),
      hasClear: !!document.getElementById('d-clearstats'),
      hasReset: !!document.getElementById('d-reset'),
      hasQuit: !!document.getElementById('d-quit'),
      hasAuto: !!document.getElementById('chk-update'),
      checkUpdatePrimary: (function(){var b=document.getElementById('d-checkupdate');return b?b.className.indexOf('primary')>=0:null;})(),
      checkUpdateSoft: (function(){var b=document.getElementById('d-checkupdate');return b?b.className.indexOf('btn-soft')>=0:null;})(),
      checkLeftOfButton: (function(){var c=document.getElementById('chk-update'),b=document.getElementById('d-checkupdate');if(!c||!b)return null;var cr=c.getBoundingClientRect(),br=b.getBoundingClientRect();return cr.right<=br.left+2;})(),
      footBeforeRows: (function(){var f=document.querySelector('#tab-about .card-foot'),r=document.querySelector('#tab-about .rows');if(!f||!r)return null;return (f.compareDocumentPosition(r)&Node.DOCUMENT_POSITION_FOLLOWING)!==0;})(),
      hasManual: !!document.getElementById('d-manual'),
      // 各页面标题（h2）应全部删除
      h2Total: document.querySelectorAll('#content .tab h2').length,
      // 软件信息卡片头的「版本与更新设置」副标题应删除
      versionSubtitleGone: !Array.prototype.some.call(tab.querySelectorAll('.card-head p'), function(p){return p.textContent.indexOf('版本与更新设置')>=0;}),
      // 退出软件红色校验
      quitColor: quitColor,
      dangerColor: dangerColor,
      quitIsDanger: quitColor===dangerColor,
      noteText: noteText,
      noteNoFullFeature: noteText.indexOf('全部功能')<0,
      // 卡片满宽一致性：卡片宽度应与 #tab-about 内容宽度基本一致（与其它页面同宽）
      cardWidth: firstCard ? firstCard.offsetWidth : 0,
      tabWidth: tab.offsetWidth,
      widthRatio: firstCard && tab.offsetWidth ? (firstCard.offsetWidth / tab.offsetWidth) : 0,
      // 顶部横幅：宽度须与下方卡片同宽、高度恒定、底色为图片米色
      bannerWidth: (function(){var b=tab.querySelector('.about-banner');return b?b.offsetWidth:0;})(),
      bannerHeight: (function(){var b=tab.querySelector('.about-banner');return b?b.offsetHeight:0;})(),
      bannerImgWidth: (function(){var i=tab.querySelector('.about-banner img');return i?Math.round(i.getBoundingClientRect().width):0;})(),
      bannerImgLoaded: (function(){var i=tab.querySelector('.about-banner img');return !!(i&&i.complete&&i.naturalWidth>0);})(),
      bannerBg: (function(){var b=tab.querySelector('.about-banner');return b?getComputedStyle(b).backgroundColor:null;})(),
      // 诊断：截图前内容区滚动位置与关于页总高（横幅被挤出画面的常见原因）
      contentScrollTop: (function(){var c=document.getElementById('content');return c?c.scrollTop:null;})(),
      tabHeight: tab.offsetHeight,
      bannerTop: (function(){var b=tab.querySelector('.about-banner');return b?Math.round(b.getBoundingClientRect().top):null;})(),
      // 全项目唯一图标 icon.png：标题栏必须真的加载出 256 的图（404 时 naturalWidth=0）
      titlebarIconLoaded: (function(){var i=document.querySelector('#titlebar .tb-icon');return !!(i&&i.complete&&i.naturalWidth>0);})(),
      titlebarIconNatural: (function(){var i=document.querySelector('#titlebar .tb-icon');return i?i.naturalWidth:0;})(),
      titlebarIconRendered: (function(){var i=document.querySelector('#titlebar .tb-icon');return i?Math.round(i.getBoundingClientRect().width):0;})()
    };
  })()`);
  console.log("ABOUT_FIELDS " + JSON.stringify(data, null, 0));

  // 响应式：窗口变宽时横幅宽度仍跟随卡片、高度不变（固定高度、只改宽度）
  const resp = [];
  for (const w of [900, 1000, 1300, 1600]) {
    await set.setViewportSize({ width: w, height: 820 });
    await delay(260);
    resp.push(await set.evaluate(`(function(){
      var tab=document.getElementById('tab-about');
      var b=tab.querySelector('.about-banner'), c=tab.querySelector('.card');
      var i=tab.querySelector('.about-banner img');
      return {w:${w}, banner:b?b.offsetWidth:0, card:c?c.offsetWidth:0,
              bannerH:b?b.offsetHeight:0, imgW:i?Math.round(i.getBoundingClientRect().width):0};
    })()`));
  }
  console.log("ABOUT_RESPONSIVE " + JSON.stringify(resp, null, 0));

  await browser.close();
})().catch((e) => { console.error(e); process.exit(1); });
