/**
 * 暂停 / 恢复的端到端探针（真页面 + 真绑定 + 真调度器）。
 *
 * 验的是“点下去之后世界真的变成那样”，而不是“某个纯函数返回 true”。
 * 口径（2026-11-12 用户拍定）：**暂停不冻钟** —— 无论手动还是自动暂停，侧栏里的
 * 长 / 短休息、外卖、自定义提醒倒计时都照常往下掉；到 0 时按该提醒卡片里的
 * “暂停休息时”处置（跳过 / 正常提醒 / 推迟），恢复时也不做任何补偿平移。
 *
 * 四段场景：
 *   1. “暂停休息时＝正常提醒”：暂停中倒计时照常走到 0 → 休息窗在暂停里弹出来
 *      （侧栏同时显示“暂停休息”与“正在休息”格），恢复后它照常走完、不被收掉；
 *   2. 目标时刻不因暂停而挪动：暂停前后 rest.at 一模一样（既没有“冻结平移”，
 *      也没有“恢复重锚”），剩余时间就是真实墙钟在扣；
 *   3. “暂停休息时＝推迟提醒”：暂停中到点不弹窗，计划被挪到 now + 推迟时长，
 *      侧栏那一格显示的是推迟后的剩余（不是停在 0:00）；
 *   4. 连做两轮暂停 / 恢复，确认反复切换不留脏状态（原因表不涨、段起点每次重记）。
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

const BASE = process.env.WR_BASE || "http://127.0.0.1:29745";
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

// 改配置：设置页的每个控件都走同一条 autoSave → api.saveSettings（深合并，给局部对象即可）
async function patchSettings(page, patch) {
  await page.evaluate(async (p) => await window.api.saveSettings(p), patch);
  // 保存会触发 replan + 状态推送，等一拍让新计划落地
  await sleep(700);
}

// 轮询到 predicted(state) 成立（调度器是 500ms 一跳，状态不是同步翻转的）
async function waitState(page, predicted, timeoutMs = 10000, stepMs = 200) {
  const t0 = Date.now();
  let last = null;
  while (Date.now() - t0 < timeoutMs) {
    last = await getState(page);
    if (predicted(last)) return last;
    await sleep(stepMs);
  }
  return last;
}

// 侧栏状态格：[{ k, text, led }]。led 格子的可读时间在 .sr-only 里（“1分”这种），
// 数码管本身只有段亮灭，读不出数。
async function statusCells(page) {
  return await page.evaluate(() => {
    const out = [];
    for (const cell of document.querySelectorAll("#data-status .status-cell")) {
      const k = cell.querySelector(".k");
      const v = cell.querySelector(".v");
      const sr = cell.querySelector(".sr-only");
      out.push({
        k: k ? k.textContent.trim() : "",
        text: (sr ? sr.textContent : v ? v.textContent : "").trim(),
        led: !!(v && v.classList.contains("v-led")),
      });
    }
    return out;
  });
}

// “9分56秒 / 1分 / 42秒 / 1小时3分” → 毫秒（读不出就返回 -1）
function parseRemain(text) {
  const s = String(text || "").trim();
  if (!s) return -1;
  let ms = 0;
  let hit = false;
  const h = s.match(/(\d+)\s*小时/);
  const m = s.match(/(\d+)\s*分/);
  const sec = s.match(/(\d+)\s*秒/);
  if (h) { ms += Number(h[1]) * 3600_000; hit = true; }
  if (m) { ms += Number(m[1]) * 60_000; hit = true; }
  if (sec) { ms += Number(sec[1]) * 1000; hit = true; }
  return hit ? ms : -1;
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

// 等到“没在休息”且下一跳在 maxAhead 毫秒之内（给下一段场景一个确定的起点）
async function waitFreshCountdown(page, maxAhead) {
  const st = await waitState(
    page,
    (s) => s.phase !== "break" && s.rest && s.rest.at - Date.now() > 200 && s.rest.at - Date.now() <= maxAhead,
    12000
  );
  return st;
}

async function main() {
  const browser = await chromium.launch({ channel: "msedge", headless: true });
  let rc = 0;
  try {
    const page = await openPage(browser);
    if (!check(page !== null, "settings.html 装载完成且 window.api 就绪")) {
      throw new Error("页面打不开，后续断言无法进行");
    }

    const st0 = await getState(page);
    check(st0.paused === false, "初始未暂停", JSON.stringify({ paused: st0.paused, phase: st0.phase }));
    check(
      Array.isArray(st0.pauseReasons) && st0.pauseReasons.length === 0,
      "初始暂停原因表为空",
      JSON.stringify(st0.pauseReasons)
    );
    check(st0.rest != null && st0.rest.at > 0, "初始已有排期（驱动脚本把短休息配成 6 秒一条）", JSON.stringify(st0.rest));
    const label0 = await page.textContent("#qa-pause");
    check(label0.trim() === "暂停休息", "按钮初始文案是“暂停休息”", label0);

    // ---------- 场景 1：暂停休息时＝正常提醒 → 暂停中到点照样弹 ----------
    await patchSettings(page, { short: { pauseAction: "remind" } });
    const s1 = await waitFreshCountdown(page, 7000);
    check(s1.phase !== "break" && s1.rest != null, "场景 1 起点：倒计时在跑、没在休息", JSON.stringify(s1.rest));

    await page.click("#qa-pause");
    const p1 = await waitState(page, (s) => s.paused === true);
    check(p1.paused === true, "点“暂停休息”后状态真的进入暂停");
    check(
      Array.isArray(p1.pauseReasons) && p1.pauseReasons.length === 1 && p1.pauseReasons[0] === "manual",
      "暂停原因恰好是 manual 一条（没被别的判定掺进来）",
      JSON.stringify(p1.pauseReasons)
    );
    check(p1.pausedAt > 0 && p1.pauseDeadline === 0, "记下段起点 pausedAt，且无限手动暂停没有到期时刻", JSON.stringify({ at: p1.pausedAt, d: p1.pauseDeadline }));
    const labelP = await page.textContent("#qa-pause");
    check(labelP.trim() === "恢复休息", "暂停后按钮文案变成“恢复休息”", labelP);

    // 暂停中倒计时照常往下掉：目标时刻固定不动，剩余时间按真实墙钟减少
    const target1 = p1.rest ? p1.rest.at : 0;
    const remainA = target1 - Date.now();
    await sleep(1500);
    const d1 = await getState(page);
    check(d1.rest && d1.rest.at === target1, "暂停中目标时刻固定不动（盯的是原来那一刻）", `${target1} → ${d1.rest && d1.rest.at}`);
    const remainB = target1 - Date.now();
    check(
      remainB <= remainA - 1200,
      "暂停中剩余时间照真实墙钟在减少（没有冻在暂停那一刻）",
      `${remainA}ms → ${remainB}ms`
    );

    // 到 0 → 按“正常提醒”办：暂停里照样弹出休息窗
    const b1 = await waitState(page, (s) => s.phase === "break" && s.breakEndsAt > 0, 12000);
    check(b1.phase === "break" && b1.breakEndsAt > 0, "暂停中倒计时到 0，休息窗照常弹出", JSON.stringify({ phase: b1.phase, ends: b1.breakEndsAt }));
    check(b1.paused === true && b1.breakWhilePaused === true, "这次休息被标记为“暂停中发起”（按真实时钟走完）", JSON.stringify({ paused: b1.paused, whilePaused: b1.breakWhilePaused }));
    const cells1 = await statusCells(page);
    const stCell1 = cells1.find((c) => c.k === "状态");
    check(stCell1 && stCell1.text === "暂停休息", "侧栏状态格仍写“暂停休息”", JSON.stringify(cells1));
    check(
      cells1.some((c) => c.k === "正在休息" && c.led),
      "侧栏照常出现“正在休息”倒计时刻度（暂停里也显示休息剩余）",
      JSON.stringify(cells1)
    );

    // 恢复 → 这次休息继续走完，不被收掉
    await page.click("#qa-pause");
    const r1 = await waitState(page, (s) => s.paused === false);
    check(r1.paused === false && r1.breakWhilePaused === false, "恢复后暂停标记全部清掉", JSON.stringify({ paused: r1.paused, whilePaused: r1.breakWhilePaused }));
    // 要么还在走（phase=break），要么已经自然走完（ends 已过期）——
    // 只有“phase 回 idle 但 ends 还在未来”才是被恢复提前掐掉
    check(
      r1.phase === "break" || r1.breakEndsAt <= Date.now(),
      "恢复不该把正在进行的休息作废",
      JSON.stringify({ phase: r1.phase, ends: r1.breakEndsAt })
    );
    // 等这次休息自然结束，给下一段一个干净起点
    await waitState(page, (s) => s.breakEndsAt === 0 && s.phase !== "break", 12000);

    // ---------- 场景 2：暂停不影响目标时刻（既不冻结平移，也不恢复重锚） ----------
    // 间隔拉长到 2 分钟，保证这一段全程不会到点
    await patchSettings(page, { short: { intervalMs: 120000, durationMs: 3000, notifyBeforeMs: 0 } });
    const s2 = await waitFreshCountdown(page, 130000);
    const target2 = s2.rest ? s2.rest.at : 0;
    check(target2 - Date.now() > 60000, "场景 2 起点：下一跳在 1 分钟之外", `${target2 - Date.now()}ms`);

    await page.click("#qa-pause");
    await waitState(page, (s) => s.paused === true);
    await sleep(1500);
    const d2 = await getState(page);
    check(d2.rest && d2.rest.at === target2, "暂停期间目标时刻不变（没有“冻结平移”那套补偿）", `${target2} → ${d2.rest && d2.rest.at}`);

    await page.click("#qa-pause");
    const r2 = await waitState(page, (s) => s.paused === false);
    check(r2.paused === false && r2.pausedAt === 0 && r2.pauseDeadline === 0, "恢复后暂停状态全部归零", JSON.stringify({ paused: r2.paused, at: r2.pausedAt, d: r2.pauseDeadline }));
    check(r2.rest && r2.rest.at === target2, "恢复时不平移目标时刻（不平移就对了）", `${target2} → ${r2.rest && r2.rest.at}`);
    const labelR2 = await page.textContent("#qa-pause");
    check(labelR2.trim() === "暂停休息", "恢复后按钮文案回到“暂停休息”", labelR2);

    // ---------- 场景 3：暂停休息时＝推迟 → 到点不弹窗，计划挪到 now + 推迟时长 ----------
    const postponeMs = 60000;
    await patchSettings(page, { short: { intervalMs: 5000, durationMs: 3000, pauseAction: "postpone", postponeMs } });
    const s3 = await waitFreshCountdown(page, 8000);
    const target3 = s3.rest ? s3.rest.at : 0;
    check(target3 > 0 && target3 - Date.now() <= 8000, "场景 3 起点：下一跳在 8 秒之内", `${target3 - Date.now()}ms`);

    await page.click("#qa-pause");
    await waitState(page, (s) => s.paused === true);
    await sleep(1200);
    const d3 = await getState(page);
    check(d3.rest && d3.rest.at === target3, "暂停中目标时刻仍然不动", `${target3} → ${d3.rest && d3.rest.at}`);

    // 等到原定时刻过去，看它怎么处置
    const after3 = await waitState(page, (s) => Date.now() > target3 + 400, 12000, 300);
    const moved = await waitState(page, (s) => s.rest && s.rest.at > target3 + 1000, 12000, 300);
    // breakEndsAt 是上一次休息的遗留值（结束时不清零），判“没在休息”只看 phase
    check(moved.phase !== "break", "推迟：到点不弹休息窗", JSON.stringify({ phase: moved.phase, ends: moved.breakEndsAt }));
    check(moved.paused === true && moved.pauseReasons.length === 1, "推迟期间仍在暂停态（没有被偷偷恢复）", JSON.stringify({ paused: moved.paused, reasons: moved.pauseReasons }));
    const delay = moved.rest ? moved.rest.at - target3 : -1;
    check(
      delay >= 55000 && delay <= 67000,
      "推迟：计划被挪到原定时刻 + 推迟时长（约 60 秒）",
      `delay=${delay}ms（到点时刻 ${after3 && after3.rest ? after3.rest.at : "?"}）`
    );
    const cells3 = await statusCells(page);
    const rest3 = cells3.find((c) => c.k.startsWith("下次"));
    const restMs = rest3 ? parseRemain(rest3.text) : -1;
    check(
      restMs >= 50000 && restMs <= 70000,
      "推迟后侧栏那一格显示推迟后的剩余（没有停在 0 秒）",
      JSON.stringify({ cell: rest3, ms: restMs })
    );

    await page.click("#qa-pause");
    const r3 = await waitState(page, (s) => s.paused === false);
    const keptAt = moved.rest ? moved.rest.at : 0;
    check(r3.paused === false, "推迟场景下恢复也正常");
    check(r3.rest && r3.rest.at === keptAt, "恢复不做补偿平移（推迟后的时刻原样保留）", `${keptAt} → ${r3.rest && r3.rest.at}`);

    // ---------- 场景 4：连做两轮，确认不留脏状态 ----------
    for (let round = 1; round <= 2; round++) {
      await page.click("#qa-pause");
      const a = await waitState(page, (s) => s.paused === true);
      await sleep(400);
      const b = await getState(page);
      check(
        b.pauseReasons.length === 1,
        `第 ${round} 轮：暂停原因不累积（反复切换不会越挂越多）`,
        JSON.stringify(b.pauseReasons)
      );
      check(b.pausedAt >= a.pausedAt, `第 ${round} 轮：段起点是新的`, `${a.pausedAt} → ${b.pausedAt}`);
      await page.click("#qa-pause");
      const c = await waitState(page, (s) => s.paused === false);
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
