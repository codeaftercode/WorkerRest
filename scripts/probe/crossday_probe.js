// 跨天探针（页面侧）：把页面时钟伪造到「今天 23:59:40」，再推到「次日 00:00:40」，
// 采集「日」范围的右端与表格首行，交给 probe_crossday.py 断言。
//
// 为什么必须伪造时钟：后端的 Today() 按真实时间算，改不了；但报表范围、桶的生成、
// 跨天重画全在前端，用 FakeDate 顶掉 window.Date 就能真实走一遍「软件通宵没关」。
const { chromium } = require("playwright");
const BASE = process.env.WR_BASE;
const DAY1 = process.env.WR_DAY1; // 页面初始时间的日期（= 真实今天）
const DAY2 = process.env.WR_DAY2; // 跨天后的日期

function tsAt(day, hh, mm, ss) {
  const [y, m, d] = day.split("-").map(Number);
  return new Date(y, m - 1, d, hh, mm, ss).getTime();
}

(async () => {
  const offset1 = tsAt(DAY1, 23, 59, 40) - Date.now();
  const offset2 = offset1 + 60 * 1000;

  const browser = await chromium.launch({ channel: "msedge", headless: true });
  const ctx = await browser.newContext({ viewport: { width: 1400, height: 1000 } });
  await ctx.addInitScript((initOffset) => {
    const RealDate = Date;
    let off = initOffset;
    window.__wrSetOffset = (v) => {
      off = v;
    };
    class FakeDate extends RealDate {
      constructor(...a) {
        if (a.length === 0) super(RealDate.now() + off);
        else super(...a);
      }
      static now() {
        return RealDate.now() + off;
      }
    }
    FakeDate.parse = RealDate.parse;
    FakeDate.UTC = RealDate.UTC;
    window.Date = FakeDate;
  }, offset1);

  const page = await ctx.newPage();
  const errs = [];
  page.on("pageerror", (e) => errs.push(String(e).slice(0, 200)));
  await page.route("**/favicon.ico", (r) => r.fulfill({ status: 204, body: "" }));
  await page.goto(BASE + "/settings.html");

  // init() 是异步的（先 await getSettings），nav 监听器绑上之前点不动 —— 循环点到真的切过去
  const waitStats = async () => {
    for (let i = 0; i < 40; i++) {
      await page.evaluate(() => document.querySelector('.nav-item[data-tab="stats"]').click());
      await page.waitForTimeout(250);
      if (await page.evaluate(() => !!document.querySelector("#tab-stats.active"))) break;
    }
    for (let i = 0; i < 40; i++) {
      if ((await page.evaluate(() => document.querySelectorAll("#rc-table tr").length)) > 1) return true;
      await page.waitForTimeout(250);
    }
    return false;
  };

  const snap = () =>
    page.evaluate(() => {
      const label = document.getElementById("rc-range-label");
      const tbl = document.getElementById("rc-table");
      const rows = tbl
        ? [...tbl.querySelectorAll("tr")].map((r) => [...r.children].map((c) => c.textContent.trim()))
        : [];
      return {
        label: label ? label.textContent.trim() : null,
        rowCount: rows.length,
        // 表格按日期倒序：第一行数据 = 最新那天
        newest: rows[1] ? rows[1].slice(0, 2).join("|") : null,
      };
    });

  const rendered = await waitStats();
  const before = await snap();
  await page.evaluate((o) => window.__wrSetOffset(o), offset2);
  await page.waitForTimeout(2800); // 1 秒定时器发现日期变了 → 重画报表
  const after = await snap();

  // 手动指定的范围不能被跨天重置（只有默认范围才现算）
  await page.evaluate(() => {
    localStorage.setItem("wr-rcv2-range-day", JSON.stringify({ start: "2026-01-05", end: "2026-01-12" }));
  });
  await page.reload();
  await waitStats();
  const manualBefore = await snap();
  await page.evaluate((o) => window.__wrSetOffset(o), offset2 + 86400000);
  await page.waitForTimeout(2800);
  const manualAfter = await snap();

  console.log(JSON.stringify({ day1: DAY1, day2: DAY2, rendered, before, after, manualBefore, manualAfter, errs }));
  await browser.close();
})().catch((e) => {
  console.error("FAILED", e);
  process.exit(1);
});
