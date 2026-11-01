/**
 * 测量用：设置窗口的最小可用宽度 + 关于页恰好不出垂直滚动条的高度。
 *
 * 它只**测量并打印数字**，不改任何配置；判定逻辑（取哪个值）留在人手里，
 * 因为「最小宽度」不是纯数学结果 —— 要同时看有没有横向裁切、控件有没有被挤扁。
 * 由 probe_window_size.py 起无头服务后注入 WR_BASE 调用。
 *
 * 三个口径：
 *   1) 宽度扫描：固定高度，逐档量「内容区横向裁切量」。只算**真正会把内容画到
 *      盒子外面**的元素 —— 自身或祖先带 overflow-x:auto/scroll 的（统计报表的表格
 *      横向滚动条、时间线等）属于刻意设计的滚动容器，不算裁切。
 *   2) 每档宽度的关于页所需视口高度（含 #content 的 padding / border）。
 *   3) 选定宽度上的高度细扫：第一次不溢出就是「恰好不出垂直滚动条」的高度。
 */
const { chromium } = require("playwright");

const BASE = process.env.WR_BASE || "http://127.0.0.1:29731";
const delay = (ms) => new Promise((r) => setTimeout(r, ms));
const TABS = ["general", "timing", "supervisor", "stats", "shortcuts", "about"];

async function clickTab(page, tab) {
  await page.evaluate(`(function(){
    var b=document.querySelector('#nav .nav-item[data-tab="${tab}"]');
    if(b) b.click();
    var c=document.getElementById('content'); if(c) c.scrollTop=0;
  })()`);
  await delay(130);
}

// 横向裁切量：跳过「自身或祖先可横向滚动」的元素（那是刻意的滚动容器）
async function clipOverflow(page) {
  return await page.evaluate(`(function(){
    // 走到 #content 就停：它自己的 overflow-x 会被 overflow-y:auto 带成 auto，
    // 若把它也算进去，所有元素都会被误判成「在可滚动容器里」→ 永远测不出裁切。
    function scrollable(el){
      for(var n=el;n && n.nodeType===1 && n.id!=='content';n=n.parentElement){
        var ox=getComputedStyle(n).overflowX;
        if(ox==='auto'||ox==='scroll'||ox==='hidden') return true;
      }
      return false;
    }
    var worst={sel:'',over:0}, worstNoScrollable={sel:'',over:0};
    var all=document.querySelectorAll('#content .tab.active *');
    for (var i=0;i<all.length;i++){
      var el=all[i];
      if (!el.offsetParent || el===document.getElementById('content')) continue;
      var over=el.scrollWidth-el.clientWidth;
      if (over<=0) continue;
      var cls=(el.className||'').toString().split(' ').slice(0,2).join('.');
      var sel=el.tagName.toLowerCase()+(cls?'.'+cls:'')+(el.id?'#'+el.id:'');
      if (over>worstNoScrollable.over && !scrollable(el)) worstNoScrollable={sel:sel,over:over};
      if (over>worst.over) worst={sel:sel,over:over};
    }
    var c=document.getElementById('content');
    // 最窄的可见输入控件：判断有没有被挤到不可用
    var minCtl=1e9, minSel='';
    var ctls=document.querySelectorAll('#content .tab.active input:not([type=checkbox]):not([type=radio]), #content .tab.active select');
    for (var j=0;j<ctls.length;j++){
      var e2=ctls[j];
      if (!e2.offsetParent) continue;
      var w=Math.round(e2.getBoundingClientRect().width);
      if (w<minCtl){ minCtl=w; minSel=e2.id||e2.className||e2.tagName; }
    }
    return {
      clipOver: worstNoScrollable.over, clipSel: worstNoScrollable.sel,
      anyOver: worst.over, anySel: worst.sel,
      contentOver: Math.max(0, c.scrollWidth-c.clientWidth),
      minCtlW: minCtl===1e9?null:minCtl, minCtlSel: minSel
    };
  })()`);
}

// 关于页所需的最小视口高度（含 titlebar / content padding / border）
async function aboutNeedH(page) {
  return await page.evaluate(`(function(){
    var c=document.getElementById('content');
    var tab=document.getElementById('tab-about');
    var cs=getComputedStyle(c);
    var need=tab.offsetHeight
           + parseFloat(cs.paddingTop) + parseFloat(cs.paddingBottom)
           + (parseFloat(cs.borderTopWidth)||0) + (parseFloat(cs.borderBottomWidth)||0);
    var tb=document.getElementById('titlebar').getBoundingClientRect().height;
    return { needContentH: Math.round(need), titlebarH: Math.round(tb),
             viewportH: Math.round(need+tb), tabH: tab.offsetHeight };
  })()`);
}

(async () => {
  const browser = await chromium.launch({
    channel: "msedge", headless: true, args: ["--disable-gpu", "--no-sandbox"],
  });
  const page = await browser.newPage({ viewport: { width: 980, height: 940 } });
  await page.route("**/favicon.ico", (r) => r.fulfill({ status: 204, body: "" }));
  await page.goto(BASE + "/settings.html", { waitUntil: "domcontentloaded" });
  await delay(1800); // 等 getSettings / appInfo 回来

  // ---------- 1) 宽度扫描（求最小可用宽度） ----------
  // 高度取 620：**故意让每个标签页都出现垂直滚动条** —— 滚动条会吃掉约 15px 可用宽度，
  // 真实窗口高度一变就可能有滚动条，所以最小宽度必须在「有滚动条」这个最坏情况下成立。
  const sweepHeights = (process.env.WR_SWEEP_H || "620,940").split(",").map(Number);
  const widthRows = [];
  for (const sh of sweepHeights) {
    for (let w = 720; w <= 880; w += 10) {
      await page.setViewportSize({ width: w, height: sh });
      await delay(160);
      const per = {};
      let clip = 0;
      for (const tab of TABS) {
        await clickTab(page, tab);
        const m = await clipOverflow(page);
        per[tab] = m;
        // 统计报表页是 10 列数据表（自带横向滚动），它的裁切不计入最小宽度
        if (tab !== "stats" && m.clipOver > 0) clip = Math.max(clip, m.clipOver);
      }
      widthRows.push({ h: sh, w, maxClip: clip, per });
    }
  }
  console.log("WIDTH_SWEEP " + JSON.stringify(widthRows));

  // ---------- 2) 各候选宽度的关于页所需高度 ----------
  const cand = (process.env.WR_PROBE_WIDTHS || "800,820,840,860,900").split(",").map(Number);
  const needRows = [];
  for (const w of cand) {
    await page.setViewportSize({ width: w, height: 1150 });
    await delay(200);
    await clickTab(page, "about");
    await delay(700);
    const m = await aboutNeedH(page);
    needRows.push({ w, ...m });
  }
  console.log("ABOUT_NEED " + JSON.stringify(needRows));

  // ---------- 3) 选定宽度上的高度细扫 ----------
  const probeW = parseInt(process.env.WR_PROBE_WIDTH || "800", 10);
  const probeNeed = (needRows.find((r) => r.w === probeW) || needRows[0]).viewportH;
  await page.setViewportSize({ width: probeW, height: probeNeed + 60 });
  await delay(300);
  await clickTab(page, "about");
  await delay(800);
  const heightRows = [];
  for (let h = probeNeed - 40; h <= probeNeed + 40; h += 2) {
    await page.setViewportSize({ width: probeW, height: h });
    await delay(110);
    const m = await page.evaluate(`(function(){
      var c=document.getElementById('content');
      return { h:${h}, overflow: c.scrollHeight-c.clientHeight,
               clientH: c.clientHeight, scrollH: c.scrollHeight };
    })()`);
    heightRows.push(m);
  }
  console.log("HEIGHT_FINE " + JSON.stringify(heightRows));

  await browser.close();
})().catch((e) => { console.error(e); process.exit(1); });
