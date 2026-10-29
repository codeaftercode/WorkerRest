/**
 * 验证用：钉住「设置窗口尺寸」这一层。由 verify_window_size.py 起无头服务后调用
 * （WR_BASE 注入），只读页面 + 点一次勾选框，不改真实配置（服务用的是隔离副本）。
 *
 * 出厂尺寸**不写死在这个脚本里**：python 侧从 default_settings.json 读出来，用
 * WR_DEF_W / WR_DEF_H 传进来 —— 用户改默认尺寸时只需改配置，脚本不用跟着动。
 *
 * 三组断言：
 *   ① 勾选框存在、在「设置窗口」卡片里、出厂默认是勾上的；
 *   ② 关于页在「出厂尺寸」恰好不出垂直滚动条，把高度降 10px 就出（证明这个高度不是随便写的）；
 *   ③ 最小宽度 760 下标签页没有横向裁切，降到 740 就裁（同理）。
 */
const { chromium } = require("playwright");

const BASE = process.env.WR_BASE || "http://127.0.0.1:29731";
const DEF_W = parseInt(process.env.WR_DEF_W || "913", 10);
const DEF_H = parseInt(process.env.WR_DEF_H || "857", 10);
const MIN_W = parseInt(process.env.WR_MIN_W || "760", 10); // 实测的布局下界
const delay = (ms) => new Promise((r) => setTimeout(r, ms));
const TABS = ["general", "timing", "supervisor", "shortcuts", "about"]; // stats 自带横向滚动，不参与

async function clickTab(page, tab) {
  await page.evaluate(`(function(){
    var b=document.querySelector('#nav .nav-item[data-tab="${tab}"]');
    if(b) b.click();
    var c=document.getElementById('content'); if(c) c.scrollTop=0;
  })()`);
  await delay(140);
}

// 横向裁切量（跳过自身或祖先可横向滚动的容器：那是刻意的滚动区）
async function maxClip(page) {
  return await page.evaluate(`(function(){
    function scrollable(el){
      for(var n=el;n && n.nodeType===1 && n.id!=='content';n=n.parentElement){
        var ox=getComputedStyle(n).overflowX;
        if(ox==='auto'||ox==='scroll'||ox==='hidden') return true;
      }
      return false;
    }
    var worst=0, sel='';
    var all=document.querySelectorAll('#content .tab.active *');
    for (var i=0;i<all.length;i++){
      var el=all[i];
      if (!el.offsetParent) continue;
      var over=el.scrollWidth-el.clientWidth;
      if (over>worst && !scrollable(el)){
        var cls=(el.className||'').toString().split(' ').slice(0,2).join('.');
        worst=over; sel=el.tagName.toLowerCase()+(cls?'.'+cls:'')+(el.id?'#'+el.id:'');
      }
    }
    return {over:worst, sel:sel};
  })()`);
}

async function aboutOverflow(page, w, h) {
  await page.setViewportSize({ width: w, height: h });
  await delay(180);
  await clickTab(page, "about");
  await delay(600); // 等 appInfo() 回填版本 / 发布时间，关于页高度才稳定
  return await page.evaluate(`(function(){
    var c=document.getElementById('content');
    return { overflow: c.scrollHeight-c.clientHeight, clientH: c.clientHeight, scrollH: c.scrollHeight };
  })()`);
}

(async () => {
  const browser = await chromium.launch({
    channel: "msedge", headless: true, args: ["--disable-gpu", "--no-sandbox"],
  });
  const page = await browser.newPage({ viewport: { width: DEF_W, height: DEF_H } });
  await page.route("**/favicon.ico", (r) => r.fulfill({ status: 204, body: "" }));
  await page.goto(BASE + "/settings.html", { waitUntil: "domcontentloaded" });
  await delay(1800);

  // ① 勾选框
  const opt = await page.evaluate(`(function(){
    var box=document.getElementById('win-remember');
    var card=box?box.closest('.card'):null;
    var h3=card?card.querySelector('h3'):null;
    return {
      exists: !!box,
      type: box?box.type:null,
      checked: box?box.checked:null,
      cardTitle: h3?h3.textContent:null,
      label: box?(box.closest('label')||{}).textContent||'':null,
      hasTip: !!(box && box.closest('label') && box.closest('label').querySelector('.q-mark'))
    };
  })()`);

  // 取消勾选：触发 change → autoSave → Go 落盘（python 侧读隔离配置断言）
  await page.evaluate(`(function(){
    var box=document.getElementById('win-remember');
    if(!box) return;
    box.checked=false;
    box.dispatchEvent(new Event('change',{bubbles:true}));
  })()`);
  await delay(900);

  // ② 关于页高度边界：出厂尺寸 / 矮 10px / 再宽 80px
  const hOk = await aboutOverflow(page, DEF_W, DEF_H);
  const hBad = await aboutOverflow(page, DEF_W, DEF_H - 10);
  const hWide = await aboutOverflow(page, DEF_W + 80, DEF_H);

  // ③ 宽度边界
  await page.setViewportSize({ width: MIN_W, height: 620 });
  await delay(180);
  let clipMin = 0, clipMinSel = "";
  for (const tab of TABS) {
    await clickTab(page, tab);
    const m = await maxClip(page);
    if (m.over > clipMin) { clipMin = m.over; clipMinSel = tab + ":" + m.sel; }
  }
  await page.setViewportSize({ width: MIN_W - 20, height: 620 });
  await delay(180);
  let clipNarrow = 0, clipNarrowSel = "";
  for (const tab of TABS) {
    await clickTab(page, tab);
    const m = await maxClip(page);
    if (m.over > clipNarrow) { clipNarrow = m.over; clipNarrowSel = tab + ":" + m.sel; }
  }

  console.log("WIN_OPT " + JSON.stringify({
    opt, hOk, hBad, hWide,
    clipMin, clipMinSel, clipNarrow, clipNarrowSel
  }));

  await browser.close();
})().catch((e) => { console.error(e); process.exit(1); });
