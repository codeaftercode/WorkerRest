/**
 * 一次性：宽屏常见分辨率下的观感截图（1920×1080 / 2560×1440 / 1440×900）。
 * 用法：WR_BASE=http://127.0.0.1:29773 WR_OUT=... node shot_sup_wide.js
 */
const { chromium } = require("playwright");
const path = require("path");
const fs = require("fs");

const BASE = process.env.WR_BASE || "http://127.0.0.1:29773";
const OUT = process.env.WR_OUT || path.join(process.env.TEMP || ".", "wr_sup_wide");
const PRELOAD = path.join(__dirname, "..", "probe_preload.js");
const delay = (ms) => new Promise((r) => setTimeout(r, ms));

(async () => {
  fs.mkdirSync(OUT, { recursive: true });
  const browser = await chromium.launch({ channel: "msedge", headless: true });
  for (const [w, h] of [[1440, 900], [1920, 1080], [2560, 1440]]) {
    const ctx = await browser.newContext({ viewport: { width: w, height: h }, deviceScaleFactor: 1 });
    const page = await ctx.newPage();
    await page.addInitScript({ path: PRELOAD });
    await page.goto(BASE + "/settings.html", { waitUntil: "domcontentloaded" });
    for (let i = 0; i < 40; i++) {
      if (await page.evaluate(() => typeof window.api === "object" && window.api !== null)) break;
      await delay(150);
    }
    await page.click('.nav-item[data-tab="supervisor"]');
    await page.waitForTimeout(700);
    const m = await page.evaluate(() => {
      const p = document.querySelector("#sup-preview").getBoundingClientRect();
      const c = document.getElementById("content");
      return { w: Math.round(p.width), h: Math.round(p.height), scroll: c.scrollHeight, client: c.clientHeight };
    });
    await page.screenshot({ path: path.join(OUT, `sup_hi_${w}x${h}.png`) });
    await ctx.close();
    console.log(`${w}x${h}: 舞台 ${m.w}x${m.h} (${(m.w / m.h).toFixed(2)}) 页面 ${m.scroll}/${m.client}`);
  }
  await browser.close();
  console.log("截图：" + OUT);
})();
