"use strict";
// 一次性校验：自建一张自定义提醒卡，确认它的「暂停休息时」是模板自带的**同一行**
// （不是 buildCustomBreakCard 里现造的第二份），位置在“弹窗设置”组首位、能落盘。
//
// 为什么要单独验：2026-11-12 把这一行从“自定义卡现造”改成“短休息卡模板自带 + 克隆继承”，
// 克隆路径一旦没把 id 改对，页面上会出现两行（模板继承一份 + 防御分支再补一份），
// 而 settings_options_probe 只扫出厂就有的卡（出处默认没有自定义整醒）。

const { chromium } = require("playwright");

const BASE = process.env.WR_BASE;
let checks = 0;
const failures = [];
function check(ok, label, detail) {
  checks++;
  const d = detail === undefined || detail === null ? "" : "  | " + String(detail);
  if (ok) console.log("  ok   " + label + d);
  else {
    console.log("  FAIL " + label + d);
    failures.push(label);
  }
}
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function waitApi(page) {
  for (let i = 0; i < 40; i++) {
    if (await page.evaluate(() => typeof window.api === "object" && window.api !== null)) return true;
    await sleep(250);
  }
  return false;
}

async function cfgOf(page) {
  return await page.evaluate(() => window.api.getSettings());
}

async function main() {
  const browser = await chromium.launch({ channel: "msedge", headless: true });
  const ctx = await browser.newContext({ viewport: { width: 1010, height: 900 }, deviceScaleFactor: 1 });
  const page = await ctx.newPage();
  const errors = [];
  page.on("console", (m) => {
    if (m.type() === "error" && !/favicon/i.test(m.text())) errors.push(m.text().slice(0, 200));
  });
  page.on("pageerror", (e) => errors.push("pageerror: " + e.message.slice(0, 200)));
  try {
    await page.route("**/favicon.ico", (r) => r.fulfill({ status: 204, body: "" }));
    await page.goto(BASE + "/settings.html?win=settings", { waitUntil: "domcontentloaded" });
    if (!(await waitApi(page))) {
      check(false, "设置页装载完成且 window.api 就绪");
      return 1;
    }
    await sleep(600);
    await page.evaluate(() => {
      [...document.querySelectorAll(".nav-item")].find((b) => b.dataset.tab === "timing").click();
    });
    await sleep(400);

    await page.click("#cb-add");
    await page.waitForSelector("#pm-input", { state: "visible" });
    await page.fill("#pm-input", "暂停行校验");
    await page.click("#pm-ok");
    await sleep(900);

    const info = await page.evaluate(() => {
      const card = [...document.querySelectorAll("#tab-timing > .card[data-cb]")].pop();
      if (!card) return null;
      const hosts = [...card.querySelectorAll('.cdrop-host[id$="-pause"]')];
      const host = hosts[0];
      const pg = host && host.closest('.pg[data-pg="reminder"]');
      const first = pg && pg.querySelector(":scope > .pg-body > .row");
      const row = host && host.closest(".row.field");
      const btn = host && host.querySelector(".cdrop-btn");
      return {
        id: card.dataset.cb,
        hostCount: hosts.length,
        hostId: host ? host.id : null,
        isFirstRow: !!row && row === first,
        label: row ? ((row.querySelector("label") || {}).textContent || "") : "",
        name: btn ? btn.getAttribute("aria-label") : null,
        value: host ? host.dataset.value : null,
        btnText: btn ? ((btn.querySelector(".cdrop-cur") || {}).textContent || "") : "",
        pgBodyKids: pg ? [...pg.querySelectorAll(":scope > .pg-body > .row > label")].map((l) => l.textContent.replace("?", "").trim()) : [],
      };
    });
    check(!!info, "新建自定义提醒后卡片存在");
    if (!info) return 1;
    check(info.hostCount === 1, "自定义卡里“暂停休息时”只有一份下拉宿主（模板继承的那一份）", JSON.stringify(info));
    check(info.hostId === info.id + "-pause", "克隆时 id 已改写成 <cb-id>-pause", info.hostId);
    check(info.isFirstRow, "它在“弹窗设置”组里是第一行", JSON.stringify(info.pgBodyKids));
    check(/^暂停休息时/.test(info.label) && info.name === "暂停休息时",
      "标签与可访问名都是“暂停休息时”", info.label);
    check(info.value === "postpone" && info.btnText === "推迟提醒",
      "默认“推迟提醒”", info.value + " / " + info.btnText);

    const before = await cfgOf(page);
    await page.click(`#${info.hostId} .cdrop-btn`);
    await page.click(`#${info.hostId} .cdrop-item[data-value="skip"]`);
    await sleep(900);
    const after = await cfgOf(page);
    const cb = (after.customBreaks ?? []).find((c) => c.id === info.id);
    check(!!cb && cb.config.pauseAction === "skip",
      "选“跳过提醒”落盘到这张卡自己的 config.pauseAction", cb && cb.config.pauseAction);
    check(after.short.pauseAction === before.short.pauseAction && after.long.pauseAction === before.long.pauseAction,
      "短 / 长休息的那两份没被连带改掉",
      JSON.stringify({ short: after.short.pauseAction, long: after.long.pauseAction }));

    check(errors.length === 0, "没有 console error / pageerror", errors.join(" ; "));
  } finally {
    await browser.close();
  }
  console.log("\n结果：" + (failures.length ? "失败 " + failures.length + " 项" : "失败 0 项") + " / 共 " + checks + " 项断言");
  return failures.length ? 1 : 0;
}

main().then((rc) => process.exit(rc)).catch((e) => {
  console.error(e);
  process.exit(1);
});
