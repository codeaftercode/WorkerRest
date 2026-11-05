/**
 * 暂停 / 恢复的端到端探针（真页面 + 真绑定 + 真调度器）。
 *
 * 验的是「点下去之后世界真的变成那样」，而不是「某个纯函数返回 true」：
 *   1. 点「暂停休息」→ 状态真翻转（paused / reasons / pausedAt / deadline），按钮文案跟着变；
 *   2. 暂停期间**倒计时冻结**：nextAt 不动，到点也不弹休息窗（phase 不会变 break）；
 *   3. 再点一次恢复 → nextAt 整体平移了「暂停时长」这段（手动暂停是冻结平移，不是重锚），
 *      状态干净归零（pausedAt / pauseDeadline 都是 0）；
 *   4. 恢复之后排期照旧工作：冻结住的那次休息照样会到点弹出来；
 *   5. 连做两轮，确认反复切换不会留下脏状态（原因表不涨、段起点每次重记）。
 *
 * 只覆盖手动暂停：自动暂停的判据来自系统状态（勿扰 / 投屏 / 应用占用 / 空闲），
 * 无头模式**故意不启动**那几条监视器（冒烟机始终空闲，会毁掉确定性），
 * 那部分由 probe_pause_autopause.py 用真 GUI 产物 + 真实进程占用验。
 *
 * 运行：由 probe_pause_cycle.py 起好无头服务后 `node pause_cycle_probe.js`。
 * 环境变量：WR_BASE（服务地址）。
 */
const { chromium } = require("playwright");
const path = require("path");

const BASE = process.env.WR_BASE || "http://127.0.0.1:29741";
const PRELOAD = path.join(__dirname, "..", "probe_preload.js");

let checks = 0;
const failures = [];
function check(ok, label, detail = "") {
  checks++;
  console.log(("  " + (ok ? "ok  " : "FAIL") + " " + label + (detail ? "  | " + detail : "")).replace(/\n/g, " "));
  if (!ok) failures.push(label + (detail ? "  | " + detail : ""));
  return ok;
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function getState(page) {
  return await page.evaluate(async () => await window.api.getState());
}

// 轮询到 predicted(state) 成立（调度器是 500ms 一跳，状态不是同步翻转的）
async function waitState(page, label, predicted, timeoutMs = 8000) {
  const t0 = Date.now();
  let last = null;
  while (Date.now() - t0 < timeoutMs) {
    last = await getState(page);
    if (predicted(last)) return last;
    await sleep(200);
  }
  return last;
}

// 打开设置页并等 window.api 就绪。
//
// 重试是必要的：无头服务刚起来的那一两秒里，`/wails/runtime.js` 偶尔会先于静态资源
// 就绪，页面拿到 404 后 shim 的 window.api 就装不上（实测 5 次里撞到 1 次）。重开一页
// 立刻恢复，与被测逻辑无关 —— 但探针不能因此报一堆假失败。
async function openPage(browser, attempts = 3) {
  let page = null;
  for (let i = 0; i < attempts; i++) {
    const ctx = await browser.newContext({ viewport: { width: 1000, height: 820 }, deviceScaleFactor: 1 });
    page = await ctx.newPage();
    page.setDefaultTimeout(8000);
    await page.addInitScript({ path: PRELOAD });
    try {
      await page.goto(BASE + "/settings.html", { waitUntil: "domcontentloaded" });
      let ready = false;
      for (let k = 0; k < 40 && !ready; k++) {
        ready = await page.evaluate(() => typeof window.api === "object" && window.api !== null);
        if (!ready) await sleep(250);
      }
      if (ready) return page;
    } catch (e) {
      console.log("  ..  第 " + (i + 1) + " 次打开设置页失败：" + (e && e.message ? e.message : e));
    }
    await ctx.close().catch(() => {});
  }
  return null;
}

async function main() {
  const browser = await chromium.launch({ channel: "msedge", headless: true });
  let rc = 0;
  try {
    const page = await openPage(browser);
    if (!check(page !== null, "settings.html 装载完成且 window.api 就绪")) {
      throw new Error("页面打不开，后续断言无法进行");
    }

    // ---------- 第一轮：暂停 → 冻结 → 恢复 ----------

    const st0 = await getState(page);
    check(st0.paused === false, "初始未暂停", JSON.stringify({ paused: st0.paused, nextAt: st0.nextAt }));
    check(
      Array.isArray(st0.pauseReasons) && st0.pauseReasons.length === 0,
      "初始暂停原因表为空",
      JSON.stringify(st0.pauseReasons)
    );
    check(st0.nextAt > 0, "初始已有排期（配置里短休息是 6 秒一条）", "nextAt=" + st0.nextAt);

    const label0 = await page.textContent("#qa-pause");
    check(label0.trim() === "暂停休息", "按钮初始文案是「暂停休息」", label0);

    await page.click("#qa-pause");
    const stP = await waitState(page, "paused", (s) => s.paused === true);
    check(stP.paused === true, "点「暂停休息」后状态真的进入暂停");
    check(
      Array.isArray(stP.pauseReasons) && stP.pauseReasons.length === 1 && stP.pauseReasons[0] === "manual",
      "暂停原因恰好是 manual 一条（没被别的判定掺进来）",
      JSON.stringify(stP.pauseReasons)
    );
    check(stP.pausedAt > 0, "记下了段起点 pausedAt", "pausedAt=" + stP.pausedAt);
    check(stP.pauseDeadline === 0, "无限手动暂停不该有到期时刻", "deadline=" + stP.pauseDeadline);

    const label1 = await page.textContent("#qa-pause");
    check(label1.trim() === "恢复休息", "暂停后按钮文案变成「恢复休息」", label1);

    // 冻结：暂停期间 nextAt 不许动
    const frozenAt = stP.nextAt;
    await sleep(2600);
    const stF = await getState(page);
    check(stF.nextAt === frozenAt, "暂停期间 nextAt 冻结不动（手动暂停是冻结平移）", `${frozenAt} → ${stF.nextAt}`);
    check(stF.phase !== "break", "暂停期间到点也不弹休息窗（phase 没有变成 break）", "phase=" + stF.phase);
    check(stF.breakEndsAt === 0, "暂停期间没有休息窗口挂着", "breakEndsAt=" + stF.breakEndsAt);

    // 恢复
    await page.click("#qa-pause");
    const stR = await waitState(page, "resumed", (s) => s.paused === false);
    check(stR.paused === false, "再点一次真的恢复");
    check(stR.pausedAt === 0, "恢复后段起点归零", "pausedAt=" + stR.pausedAt);
    check(stR.pauseDeadline === 0, "恢复后到期时刻归零", "deadline=" + stR.pauseDeadline);
    const label2 = await page.textContent("#qa-pause");
    check(label2.trim() === "暂停休息", "恢复后按钮文案回到「暂停休息」", label2);

    // 平移量 ≈ 暂停时长（2.6s 冻结 + 两次点击的等待）
    const shift = stR.nextAt - frozenAt;
    check(
      shift >= 1500 && shift <= 6000,
      "恢复后排期整体平移了约一段暂停时长（冻结平移，不是重锚）",
      "shift=" + shift + "ms"
    );

    // 恢复后排期照常工作：冻结住的那次休息仍然会到点弹出来
    const stBreak = await waitState(page, "break", (s) => s.breakEndsAt > 0, 12000);
    check(stBreak.breakEndsAt > 0, "恢复后冻结的那次休息照常到点弹出（没有丢一次）", "breakEndsAt=" + stBreak.breakEndsAt);

    // 等这次休息走完，给第二轮一个干净起点
    await waitState(page, "break-over", (s) => s.breakEndsAt === 0 && s.phase !== "break", 12000);

    // ---------- 第二轮：连做两次，确认不留脏状态 ----------
    for (let round = 1; round <= 2; round++) {
      await page.click("#qa-pause");
      const a = await waitState(page, "paused2", (s) => s.paused === true);
      await sleep(400);
      const b = await getState(page);
      check(
        b.pauseReasons.length === 1,
        `第 ${round} 轮：暂停原因不累积（反复切换不会越挂越多）`,
        JSON.stringify(b.pauseReasons)
      );
      check(b.pausedAt >= a.pausedAt, `第 ${round} 轮：段起点是新的`, `${a.pausedAt} → ${b.pausedAt}`);
      await page.click("#qa-pause");
      const c = await waitState(page, "resumed2", (s) => s.paused === false);
      check(
        c.paused === false && c.pausedAt === 0 && c.pauseDeadline === 0,
        `第 ${round} 轮：恢复后状态干净`,
        JSON.stringify({ paused: c.paused, pausedAt: c.pausedAt, deadline: c.pauseDeadline })
      );
    }
  } catch (e) {
    console.log("  FAIL 探针执行异常: " + (e && e.message ? e.message : e));
    failures.push("探针执行异常");
    rc = 1;
  } finally {
    await browser.close().catch(() => {});
  }
  console.log("\n结果：" + checks + " 项断言，失败 " + failures.length + " 项");
  if (failures.length) {
    for (const f of failures) console.log("  - " + f);
    rc = 1;
  }
  return rc;
}

main().then((c) => process.exit(c)).catch((e) => {
  console.error(e);
  process.exit(1);
});
