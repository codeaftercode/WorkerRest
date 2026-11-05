/**
 * 测量用：系统缩放（DPI）对设置窗口布局的影响。
 *
 * 回答两个问题：
 *   1) 缩放本身会不会改变布局？—— 窗口的 Width/Height 是 **DIP**（Wails v3 的
 *      bounds 走 absoluteToRelativeDipPoint，与 CSS px 同尺度），所以 874×857 的
 *      窗口在 125% / 150% 下 CSS 视口仍是 874×857 个 CSS px。这里用不同的
 *      devicePixelRatio 渲染同一个视口，把每页的布局指纹抓下来对比 —— 如果指纹
 *      一致，说明「缩放不改布局」这个前提是真的，而不是我们假设出来的。
 *   2) 缩放**间接**改了什么？—— fitSizeToScreen 把窗口夹进主屏工作区时用的是
 *      WorkArea / ScaleFactor。缩放越大、屏幕越小，夹出来的窗口越矮。这里按
 *      fitSizeToScreen 的公式复算常见「分辨率 × 缩放」组合，再在夹出来的尺寸上
 *      量每页的真实溢出量与横向裁切。
 *
 * 只测量，不写配置、不改源码。由 probe_dpi_layout.py 起无头服务后注入 WR_BASE。
 */
const { chromium } = require("playwright");

const BASE = process.env.WR_BASE || "http://127.0.0.1:29731";
const delay = (ms) => new Promise((r) => setTimeout(r, ms));
const TABS = ["general", "timing", "supervisor", "stats", "shortcuts", "about"];

// 与 internal/settings/settings_window.go 保持一致的常量
const DEF_W = 874;
const DEF_H = 857;
const MIN_W = 760;
// 设计下限 600 **不再参与夹紧**：工作区比它矮时窗口跟着矮（见 fitSizeInWorkArea）。
// 留着它只是为了让下面的报表能说清「窗口比设计下限矮了多少」。
const MIN_H = 600;

async function clickTab(page, tab) {
  await page.evaluate(`(function(){
    var b=document.querySelector('#nav .nav-item[data-tab="${tab}"]');
    if(b) b.click();
    var c=document.getElementById('content'); if(c) c.scrollTop=0;
  })()`);
  await delay(140);
}

/**
 * 当前激活标签页的布局指纹。
 *
 * hSum / elems 是「所有可见元素矩形高度之和」：任何字体度量、行高、换行的变化都会
 * 让它漂移，比只量 #content.scrollHeight 灵敏得多（后者可能刚好落在同一个整数上）。
 *
 * 横向裁切只算「真正会画到盒子外」的元素：自身或祖先带 overflow-x:auto/scroll 的
 * 属于刻意的滚动容器（统计页数据表），不算裁切。走到 #content 就停 —— 它自己的
 * overflow-x 被 overflow-y:auto 带成 auto，算进去会让所有元素都被判成「在滚动容器里」。
 */
async function metrics(page) {
  return await page.evaluate(() => {
    function scrollable(el) {
      for (let n = el; n && n.nodeType === 1 && n.id !== "content"; n = n.parentElement) {
        const ox = getComputedStyle(n).overflowX;
        if (ox === "auto" || ox === "scroll" || ox === "hidden") return true;
      }
      return false;
    }
    const c = document.getElementById("content");
    const t = document.querySelector(".tab.active");
    let hSum = 0, n = 0, maxClip = 0, clipSel = "";
    for (const el of t.querySelectorAll("*")) {
      const r = el.getBoundingClientRect();
      if (r.width === 0 && r.height === 0) continue;
      hSum += Math.round(r.height);
      n++;
      const over = el.scrollWidth - el.clientWidth;
      if (over > maxClip && !scrollable(el)) {
        maxClip = over;
        const cls = (el.className || "").toString().split(" ").filter(Boolean).slice(0, 2).join(".");
        clipSel = el.tagName.toLowerCase() + (cls ? "." + cls : "") + (el.id ? "#" + el.id : "");
      }
    }
    return {
      dpr: Number(window.devicePixelRatio.toFixed(3)),
      vw: window.innerWidth,
      vh: window.innerHeight,
      contentClientH: c.clientHeight,
      contentScrollH: c.scrollHeight,
      need: c.scrollHeight - c.clientHeight,
      tabH: t.offsetHeight,
      elems: n,
      hSum,
      maxClip,
      clipSel,
    };
  });
}

/**
 * 滚到底，检查最后一个块是否完整可见。
 *
 * 「需要滚动」和「滚了也看不全」是两件事：底部 padding / border 吃掉一截时，
 * 用户滚到底仍然看不到最后一行。这一条才是「内容能不能显示全」的真正判据。
 */
async function tailReachable(page) {
  return await page.evaluate(() => {
    const c = document.getElementById("content");
    c.scrollTop = c.scrollHeight;
    const t = document.querySelector(".tab.active");
    const kids = [...t.children].filter((el) => el.getBoundingClientRect().height > 0);
    const last = kids[kids.length - 1];
    const cr = c.getBoundingClientRect();
    const lr = last.getBoundingClientRect();
    const r = {
      scrollTop: Math.round(c.scrollTop),
      gapBelow: Math.round(cr.bottom - lr.bottom),
      ok: lr.bottom <= cr.bottom + 1 && lr.top >= cr.top - 1,
      lastCls: (last.className || last.tagName).toString().split(" ")[0],
    };
    c.scrollTop = 0;
    return r;
  });
}

// fitSizeToScreen 的复算（逐行对应 cmd/workerrest/window_size.go 的 fitSizeInWorkArea）：
// WorkArea 是物理像素，窗口尺寸是 DIP，所以先按缩放折回 DIP 再比。
// Go 侧用的是 int() 截断，这里必须 Math.trunc 对齐，否则会有 1px 的口径漂移。
function fitToScreen(physW, physH, taskbarPhys, scale) {
  const waW = physW;
  const waH = physH - taskbarPhys;
  let maxW = Math.trunc(waW / scale) - 16;
  let maxH = Math.trunc(waH / scale) - 40;
  if (maxW < MIN_W) maxW = MIN_W;
  if (maxH < 1) maxH = Math.trunc(waH / scale); // 工作区矮到留不出余量：不再留边
  if (maxH < 1) maxH = 1;
  let w = DEF_W,
    h = DEF_H;
  if (w > maxW) w = maxW;
  if (h > maxH) h = maxH;
  return {
    w,
    h,
    maxW,
    maxH,
    waDipW: Math.trunc(waW / scale),
    waDipH: Math.trunc(waH / scale),
  };
}

// 常见的「物理分辨率 × 缩放」组合。taskbar 取 Windows 默认的 40 物理像素 @100%，
// 实际会随缩放一起放大，这里按 40×scale 折（Win11 任务栏高度跟着 DPI 走）。
const SCREENS = [
  { name: "1920×1080", w: 1920, h: 1080 },
  { name: "2560×1440", w: 2560, h: 1440 },
  { name: "1366×768", w: 1366, h: 768 },
  { name: "2880×1800", w: 2880, h: 1800 },
  { name: "3840×2160", w: 3840, h: 2160 },
];
const SCALES = [1, 1.25, 1.5, 2, 3];

(async () => {
  const browser = await chromium.launch({
    channel: "msedge",
    headless: true,
    args: ["--disable-gpu", "--no-sandbox"],
  });

  // ---------- 阶段 1：同一视口、不同 devicePixelRatio ----------
  const fp = {}; // dpr -> { tab -> metrics }
  for (const dpr of [1, 1.25, 1.5, 2, 2.5]) {
    const ctx = await browser.newContext({
      viewport: { width: DEF_W, height: DEF_H },
      deviceScaleFactor: dpr,
    });
    const page = await ctx.newPage();
    await page.route("**/favicon.ico", (r) => r.fulfill({ status: 204, body: "" }));
    await page.goto(BASE + "/settings.html?win=settings", { waitUntil: "domcontentloaded" });
    await delay(1600);
    fp[dpr] = {};
    for (const tab of TABS) {
      await clickTab(page, tab);
      fp[dpr][tab] = await metrics(page);
    }
    await ctx.close();
  }
  console.log("DPR_FINGERPRINT " + JSON.stringify(fp));

  // ---------- 阶段 2：真实夹紧尺寸下的溢出与裁切 ----------
  const ctx = await browser.newContext({ viewport: { width: DEF_W, height: DEF_H } });
  const page = await ctx.newPage();
  await page.route("**/favicon.ico", (r) => r.fulfill({ status: 204, body: "" }));
  await page.goto(BASE + "/settings.html?win=settings", { waitUntil: "domcontentloaded" });
  await delay(1600);

  const rows = [];
  for (const sc of SCREENS) {
    for (const scale of SCALES) {
      const taskbar = Math.round(40 * scale);
      const fit = fitToScreen(sc.w, sc.h, taskbar, scale);
      await page.setViewportSize({ width: fit.w, height: fit.h });
      await delay(220);
      const per = {};
      for (const tab of TABS) {
        await clickTab(page, tab);
        per[tab] = await metrics(page);
        if (tab === "about") per[tab].tail = await tailReachable(page);
      }
      rows.push({
        screen: sc.name,
        scale,
        win: [fit.w, fit.h],
        waDip: [fit.waDipW, fit.waDipH],
        clamped: fit.w !== DEF_W || fit.h !== DEF_H,
        per,
      });
    }
  }
  console.log("CLAMP_MATRIX " + JSON.stringify(rows));

  // ---------- 阶段 3：宽度下限 ----------
  const widthRows = [];
  for (const w of [MIN_W, 800, DEF_W, 913]) {
    await page.setViewportSize({ width: w, height: DEF_H });
    await delay(200);
    const per = {};
    for (const tab of TABS) {
      await clickTab(page, tab);
      const m = await metrics(page);
      per[tab] = { maxClip: m.maxClip, clipSel: m.clipSel, need: m.need };
    }
    widthRows.push({ w, per });
  }
  console.log("WIDTH_BOUNDS " + JSON.stringify(widthRows));

  await ctx.close();
  await browser.close();
})().catch((e) => {
  console.error(e);
  process.exit(1);
});
