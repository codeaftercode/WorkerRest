/**
 * 一次性：督休专员卡片撑满可视高度的验收测量（2026-11-18）。
 * 断言（每个视口）：无横向溢出；舞台仍 16:9；卡片不超出可视区或页面可滚；
 * 台词列表 ≥180；宽 1180 系列里卡片高度随视口高度单调不减；
 * 折叠态不得撑满整屏。
 * 用法：WR_BASE=http://127.0.0.1:29773 node verify_sup_fill.js
 */
const { chromium } = require("playwright");
const path = require("path");

const BASE = process.env.WR_BASE || "http://127.0.0.1:29773";
const PRELOAD = path.join(__dirname, "..", "probe_preload.js");
const delay = (ms) => new Promise((r) => setTimeout(r, ms));

const MEASURE = () => {
  const q = (s) => document.querySelector(s);
  const box = (s) => {
    const el = q(s);
    if (!el) return null;
    const b = el.getBoundingClientRect();
    return { y: +b.y.toFixed(1), h: +b.height.toFixed(1), w: +b.width.toFixed(1), bottom: +b.bottom.toFixed(1) };
  };
  const content = q("#content");
  const cs = getComputedStyle(content);
  return {
    content: {
      client: content.clientHeight,
      clientW: content.clientWidth,
      scroll: content.scrollHeight,
      scrollW: content.scrollWidth,
      padTop: parseFloat(cs.paddingTop),
      padLeft: parseFloat(cs.paddingLeft),
      padRight: parseFloat(cs.paddingRight),
      overflowX: content.scrollWidth - content.clientWidth,
    },
    card: box("#sup-main"),
    grid: box("#sup-main .sup-grid"),
    stage: box("#tab-supervisor .sup-stage"),
    galleryBody: box("#sup-gallery-body"),
    preview: box("#sup-preview"),
    geom: box("#sup-geom"),
    msg: box(".sup-msg"),
    msgList: box(".sup-stage .msg-panel .msg-list"),
    msgCount: document.querySelectorAll("#tab-supervisor .msg-list .msg-item").length,
    closed: q("#sup-main").classList.contains("closed"),
  };
};

async function open(browser, w, h) {
  const ctx = await browser.newContext({ viewport: { width: w, height: h }, deviceScaleFactor: 1 });
  const page = await ctx.newPage();
  await page.addInitScript({ path: PRELOAD });
  await page.goto(BASE + "/settings.html", { waitUntil: "domcontentloaded" });
  for (let i = 0; i < 40; i++) {
    if (await page.evaluate(() => typeof window.api === "object" && window.api !== null)) break;
    await delay(150);
  }
  await page.click('.nav-item[data-tab="supervisor"]');
  await page.waitForTimeout(500);
  return { ctx, page };
}

(async () => {
  const browser = await chromium.launch({ channel: "msedge", headless: true });
  const fails = [];
  const ck = (ok, label, detail = "") => {
    console.log((ok ? "  ok   " : "  FAIL ") + label + (detail ? "  | " + detail : ""));
    if (!ok) fails.push(label + " " + detail);
  };

  const cases = [
    [1180, 600], [1180, 700], [1180, 835], [1180, 900], [1180, 1000], [1180, 1200], [1180, 1400],
    [760, 835], [1440, 900],
    // 宽度占满（2026-11-18 第二轮）：卡片不再封 1040，宽屏要真的到边
    [1680, 900], [1920, 900], [1920, 1080], [2560, 900], [2560, 1440],
  ];
  let prevH = 0;
  for (const [w, hgt] of cases) {
    const { ctx, page } = await open(browser, w, hgt);
    const r = await page.evaluate(MEASURE);
    await ctx.close();
    const viewH = r.content.padTop + r.content.client;
    console.log(
      `\n视口 ${w}x${hgt}: content client=${r.content.client} scroll=${r.content.scroll} overflowX=${r.content.overflowX}\n` +
        `  card h${r.card.h} (可视 ${viewH}) | grid h${r.grid.h} | 名单体 h${r.galleryBody.h}\n` +
        `  preview ${r.preview.w}x${r.preview.h} 比例 ${(r.preview.w / r.preview.h).toFixed(3)} | geom h${r.geom.h}` +
        ` | msg h${r.msg.h} | 列表 h${r.msgList.h}（${r.msgCount} 条）`
    );
    ck(r.content.overflowX <= 0, `${w}x${hgt} 无横向溢出`, `overflowX=${r.content.overflowX}`);
    // 宽度占满：卡宽 = 内容区可视宽 - 左右内边距（#content 的 16 / 6），且舞台贴着右列两边
    const availW = r.content.clientW - r.content.padLeft - r.content.padRight;
    ck(Math.abs(r.card.w - availW) <= 1, `${w}x${hgt} 卡片宽度占满内容区`, `card=${r.card.w} 可用=${availW}`);
    ck(Math.abs(r.preview.w - r.stage.w) <= 0.5, `${w}x${hgt} 舞台宽度 = 右列宽`, `pre=${r.preview.w} stage=${r.stage.w}`);
    // 舞台：不窄于 16:9（宽扁允许）、高度不超过 560 封顶
    ck(r.preview.w / r.preview.h >= 16 / 9 - 0.05 || r.preview.h <= 205, `${w}x${hgt} 舞台不窄于 16:9`,
      (r.preview.w / r.preview.h).toFixed(3));
    ck(r.preview.h <= 761, `${w}x${hgt} 舞台高度 ≤760`, `h=${r.preview.h}`);
    ck(r.card.h <= viewH + 1 || r.content.scroll > r.content.client, `${w}x${hgt} 卡片不超出可视区或页面可滚`);
    ck(r.msgList.h >= 179, `${w}x${hgt} 台词列表 ≥180`, `h=${r.msgList.h}`);
    if (w === 1180) {
      ck(r.card.h >= prevH - 1, `${w}x${hgt} 卡片高度随视口高度单调不减`, `h=${r.card.h} 上一档=${prevH}`);
      prevH = r.card.h;
    }
  }

  // 折叠态：收起后卡片高度不该跟着撑满
  const { ctx, page } = await open(browser, 1180, 1200);
  await page.click("#sup-main .card-fold");
  await page.waitForTimeout(400);
  const folded = await page.evaluate(MEASURE);
  await ctx.close();
  console.log(`\n折叠态：card h${folded.card.h} closed=${folded.closed}`);
  ck(folded.closed, "折叠态 class 生效");
  ck(folded.card.h < 120, "折叠后卡片不高（不撑满）", `h=${folded.card.h}`);

  await browser.close();
  console.log(`\n结果：${fails.length} 项不合格`);
  process.exitCode = fails.length ? 1 : 0;
})();
