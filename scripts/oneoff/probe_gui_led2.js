/**
 * 真窗口诊断（第二轮）：点侧栏按钮时，数码管的数字变化节奏是否被打断，
 * 并顺带量“点下去 → 目标页真的显示出来”的延迟。
 *
 * 判据：数字相邻两次变化的间隔常态 ~1000ms；若某段 > 1300ms，就是用户说的
 * “卡了一秒”。输出只给汇总 + 超限的那几段，避免刷屏。
 */
const { chromium } = require("playwright-core");
const path = require("path");
const fs = require("fs");

const CDP = process.env.CDP_URL || "http://127.0.0.1:9333";
const OUT = process.env.WR_OUT || path.join(process.env.TEMP || ".", "wr_gui_led2");
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

  const switches = [];
  const tabs = ["timing", "supervisor", "stats", "about", "general"];

  await delay(2500);
  for (const tab of tabs) {
    const t0 = await page.evaluate(() => Math.round(performance.now()));
    await page.click(`.nav-item[data-tab="${tab}"]`, { noWaitAfter: true });
    // 等目标页真的 active（applyTab 里要先解码、等两帧才切）
    let shown = null;
    try {
      await page.waitForFunction(
        (t) => document.getElementById("tab-" + t)?.classList.contains("active"),
        tab,
        { timeout: 8000, polling: 16 }
      );
      shown = await page.evaluate(() => Math.round(performance.now()));
    } catch (e) {
      shown = -1;
    }
    switches.push({ tab, clickAt: t0, shownAt: shown, latency: shown < 0 ? -1 : shown - t0 });
    await delay(3000);
  }

  const ticks = await page.evaluate(() => window.__ticks.slice());
  const gaps = [];
  for (let i = 1; i < ticks.length; i++) gaps.push({ at: ticks[i], ms: ticks[i] - ticks[i - 1] });
  const bad = gaps.filter((g) => g.ms > 1300);
  const result = {
    tickCount: ticks.length,
    gaps: gaps.length,
    maxGap: gaps.length ? Math.max(...gaps.map((g) => g.ms)) : 0,
    bad,
    switches,
    firstTicks: ticks.slice(0, 6),
  };

  fs.writeFileSync(path.join(OUT, "led2.json"), JSON.stringify(result, null, 2), "utf-8");
  console.log(JSON.stringify(result, null, 2));
  await browser.close();
})().catch((e) => {
  console.error(e && e.stack ? e.stack : e);
  process.exit(1);
});
