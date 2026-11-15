/**
 * 真窗口诊断（第三轮）：把 window.api.getState 人为拖慢 1.5s，看侧栏数码管还走不走。
 *
 * 为什么要这么测：用户机器上“点侧栏按钮卡一秒”复现不出来，但这台机器 IPC/磁盘快。
 * 原来数码管的读数完全靠每秒那次 getState 往返驱动 —— 往返一旦慢半秒以上，数字就
 * 停一拍再跳。这里直接把往返拖慢，模拟慢机器，检验“读数是否已与往返解耦”。
 */
const { chromium } = require("playwright-core");
const path = require("path");
const fs = require("fs");

const CDP = process.env.CDP_URL || "http://127.0.0.1:9333";
const OUT = process.env.WR_OUT || path.join(process.env.TEMP || ".", "wr_gui_led3");
const delay = (ms) => new Promise((r) => setTimeout(r, ms));

(async () => {
  fs.mkdirSync(OUT, { recursive: true });
  const browser = await chromium.connectOverCDP(CDP);
  let page = null;
  for (const ctx of browser.contexts()) for (const p of ctx.pages()) if (!page && /settings\.html/.test(p.url())) page = p;
  if (!page) throw new Error("没找到设置窗口");

  await page.waitForSelector("#data-status .led", { timeout: 30000 });
  await delay(1500);

  await page.evaluate(() => {
    window.__ticks = [];
    const sig = () => {
      const led = document.querySelector("#data-status .led");
      return led ? Array.prototype.map.call(led.querySelectorAll(".dg"), (d) => d.dataset.d).join("") : "-";
    };
    let last = sig();
    window.__timer = setInterval(() => {
      const s = sig();
      if (s !== last) {
        last = s;
        window.__ticks.push(Math.round(performance.now()));
      }
    }, 60);
  });

  // 前半程：正常往返
  await delay(4000);
  const before = await page.evaluate(() => window.__ticks.slice());

  // 注入慢往返：每个 getState 多等 1500ms
  await page.evaluate(() => {
    const api = window.api;
    const orig = api.getState.bind(api);
    api.getState = () => new Promise((r) => setTimeout(r, 1500)).then(() => orig());
    window.__slowAt = Math.round(performance.now());
  });

  await delay(12000);
  await page.click('.nav-item[data-tab="timing"]', { noWaitAfter: true });
  await delay(4000);
  await page.click('.nav-item[data-tab="supervisor"]', { noWaitAfter: true });
  await delay(4000);

  const raw = await page.evaluate(() => ({ ticks: window.__ticks.slice(), slowAt: window.__slowAt }));
  const ticks = raw.ticks;
  const gaps = [];
  for (let i = 1; i < ticks.length; i++) gaps.push({ at: ticks[i], ms: ticks[i] - ticks[i - 1] });
  const slowGaps = gaps.filter((g) => g.at > raw.slowAt);
  const result = {
    slowAt: raw.slowAt,
    tickCount: ticks.length,
    normalMaxGap: Math.max(...gaps.filter((g) => g.at < raw.slowAt).map((g) => g.ms), 0),
    slowMaxGap: slowGaps.length ? Math.max(...slowGaps.map((g) => g.ms)) : 0,
    slowBad: slowGaps.filter((g) => g.ms > 1300),
    beforeCount: before.length,
  };

  fs.writeFileSync(path.join(OUT, "led3.json"), JSON.stringify(result, null, 2), "utf-8");
  console.log(JSON.stringify(result, null, 2));
  await browser.close();
})().catch((e) => {
  console.error(e && e.stack ? e.stack : e);
  process.exit(1);
});
