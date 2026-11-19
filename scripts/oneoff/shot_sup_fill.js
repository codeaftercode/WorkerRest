/**
 * 一次性：督休专员页撑满高度的视觉取样 + 其它 tab 的回归（卡片不该跟着撑满）。
 * 用法：WR_BASE=http://127.0.0.1:29773 WR_OUT=... node shot_sup_fill.js
 */
const { chromium } = require("playwright");
const path = require("path");
const fs = require("fs");

const BASE = process.env.WR_BASE || "http://127.0.0.1:29773";
const OUT = process.env.WR_OUT || path.join(process.env.TEMP || ".", "wr_sup_fill");
const PRELOAD = path.join(__dirname, "..", "probe_preload.js");
const delay = (ms) => new Promise((r) => setTimeout(r, ms));

async function open(browser, w, h) {
  const ctx = await browser.newContext({ viewport: { width: w, height: h }, deviceScaleFactor: 1 });
  const page = await ctx.newPage();
  await page.addInitScript({ path: PRELOAD });
  await page.goto(BASE + "/settings.html", { waitUntil: "domcontentloaded" });
  for (let i = 0; i < 40; i++) {
    if (await page.evaluate(() => typeof window.api === "object" && window.api !== null)) break;
    await delay(150);
  }
  await page.waitForTimeout(400);
  return { ctx, page };
}

const cardHeights = () =>
  [...document.querySelectorAll(".tab.active .card")].map((c) => ({
    id: c.id || c.className,
    h: +c.getBoundingClientRect().height.toFixed(1),
  }));

(async () => {
  fs.mkdirSync(OUT, { recursive: true });
  const browser = await chromium.launch({ channel: "msedge", headless: true });

  for (const [w, h] of [[874, 857], [1180, 900], [1440, 900], [760, 835], [1180, 1200]]) {
    const { ctx, page } = await open(browser, w, h);
    await page.click('.nav-item[data-tab="supervisor"]');
    await page.waitForTimeout(600);
    await page.screenshot({ path: path.join(OUT, `sup_${w}x${h}.png`) });
    // 滚到底再看一眼列表底 / 卡片下边框
    await page.evaluate(() => {
      const c = document.querySelector("#content");
      c.scrollTop = c.scrollHeight;
    });
    await page.waitForTimeout(300);
    await page.screenshot({ path: path.join(OUT, `sup_${w}x${h}_bottom.png`) });
    await ctx.close();
  }

  // 折叠态
  {
    const { ctx, page } = await open(browser, 1180, 1200);
    await page.click('.nav-item[data-tab="supervisor"]');
    await page.waitForTimeout(400);
    await page.click("#sup-main .card-fold");
    await page.waitForTimeout(400);
    await page.screenshot({ path: path.join(OUT, "sup_folded_1180x1200.png") });
    await ctx.close();
  }

  // 回归：其它 tab 的卡片高度不随视口高度变化
  for (const tab of ["timing", "general", "shortcuts"]) {
    const hs = [];
    for (const h of [700, 1200]) {
      const { ctx, page } = await open(browser, 1180, h);
      await page.click(`.nav-item[data-tab="${tab}"]`);
      await page.waitForTimeout(500);
      hs.push(await page.evaluate(cardHeights));
      await ctx.close();
    }
    const same = JSON.stringify(hs[0]) === JSON.stringify(hs[1]);
    console.log(`${tab}: 700=${JSON.stringify(hs[0])}  1200=${JSON.stringify(hs[1])}  ${same ? "ok 不随高度变" : "!! 变了"}`);
  }

  await browser.close();
  console.log("截图目录：" + OUT);
})();
