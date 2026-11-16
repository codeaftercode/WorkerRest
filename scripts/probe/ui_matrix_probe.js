/**
 * 设置页「侧栏状态 / 今日时间线 / 统计看板」的**组合态**端到端探针。
 *
 * 与既有脚本的分工：
 *   · probe_pause_cycle.js：暂停 / 恢复这一条主线（不冻钟、到点处置、恢复不补偿）；
 *   · renderer_probe.js：页面版式与静态渲染（含休息窗、外卖窗、督休专员）；
 *   · **本脚本**：把「几类提醒同时在场」时侧栏三格该写什么、时间线该列几条、
 *     看板数字该等于哪个绑定值，用真页面 + 真绑定 + 真调度器逐条钉住。
 *
 * 七段场景（配置由 probe_ui_matrix.py 预置：短休息 25 分钟倒计时 + 喝水提醒 45 分钟）：
 *   1. 基线：两格都是数码管（短休息 / 喝水提醒），时间线列出两条且来源写“倒计时”；
 *   2. 跨午夜：短休息改成 00:10 固定时刻 → 那一格写“明天 00:10”（不是“今日已无提醒”）；
 *   3. 全关：三格收成一格“下次休息提醒 / 未安排”，「立即休息」禁用并说明原因；
 *   4. 手动暂停：状态格写“暂停休息”、原因挂在 title；“恢复休息”后回到“努力工作中”；
 *   5. 休息中 + 另一条提醒仍在排队：侧栏同时有“正在休息”与“下次喝水提醒”两格
 *      （旧版这里固定写“—”，把排在休息之后的提醒整段藏掉）；
 *   6. 时间线展开：行内容 = 时刻 + 名称 + 来源，条数与绑定一致；
 *   7. 统计看板：完成 / 推迟 / 跳过三个数字必须等于 getStats() 的对应计数。
 *
 * 运行：由 probe_ui_matrix.py 起好无头服务后 `node ui_matrix_probe.js`。
 * 环境变量：WR_BASE（服务地址）。
 */
const { chromium } = require("playwright");
const path = require("path");

const BASE = process.env.WR_BASE || "http://127.0.0.1:29746";
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

async function patchSettings(page, patch) {
  await page.evaluate(async (p) => await window.api.saveSettings(p), patch);
  await sleep(700);
}

async function waitState(page, pred, timeoutMs = 12000, stepMs = 200) {
  const t0 = Date.now();
  let last = null;
  while (Date.now() - t0 < timeoutMs) {
    last = await getState(page);
    if (pred(last)) return last;
    await sleep(stepMs);
  }
  return last;
}

// 侧栏状态格：{k, text, led, title}
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
        title: cell.getAttribute("title") || "",
      });
    }
    return out;
  });
}

async function timelineInfo(page) {
  return await page.evaluate(() => {
    const count = document.getElementById("tl-count");
    const rows = Array.from(document.querySelectorAll("#tl-list .tl-row")).map((r) => ({
      time: (r.querySelector(".tl-time") || {}).textContent || "",
      name: (r.querySelector(".tl-name") || {}).textContent || "",
      src: (r.querySelector(".tl-src") || {}).textContent || "",
    }));
    return { count: count ? count.textContent.trim() : "", rows };
  });
}

async function openPage(browser, attempts = 3) {
  let page = null;
  for (let i = 0; i < attempts; i++) {
    const ctx = await browser.newContext({ viewport: { width: 1000, height: 820 }, deviceScaleFactor: 1 });
    page = await ctx.newPage();
    page.setDefaultTimeout(8000);
    await page.addInitScript({ path: PRELOAD });
    // 时间线默认折叠：本探针要读行内容，先让它展开（页面读 localStorage 决定初始状态）
    await page.addInitScript(() => {
      try { localStorage.setItem("wr-tl-expanded", "1"); } catch (e) {}
    });
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
    await page.close().catch(() => {});
  }
  throw new Error("设置页打不开（window.api 一直没就绪）");
}

const SHORT_25 = { enabled: true, mode: "countdown", intervalMs: 25 * 60_000, durationMs: 20_000, notifyBeforeMs: 0, postponeMs: 120_000, pauseAction: "postpone", slots: [] };
const WATER_45 = { config: { enabled: true, mode: "countdown", intervalMs: 45 * 60_000, durationMs: 20_000, notifyBeforeMs: 0, postponeMs: 120_000, pauseAction: "postpone", slots: [] } };

async function main() {
  const browser = await chromium.launch({ channel: "msedge" });
  const page = await openPage(browser);
  const pageErrors = [];
  page.on("pageerror", (e) => pageErrors.push(String(e && e.message ? e.message : e)));

  // ---------- 1. 基线：两格数码管 + 时间线两条 ----------
  await patchSettings(page, {
    short: SHORT_25,
    long: { enabled: false },
    takeout: { enabled: false },
    customBreaks: [{ id: "cb-water", name: "喝水提醒", ...WATER_45 }],
  });
  await waitState(page, (st) => st && st.nextType === "short");
  let cells = await statusCells(page);
  check(cells.length === 3, "基线：侧栏三格（状态 / 下次短休息 / 下次喝水提醒）", JSON.stringify(cells.map((c) => c.k)));
  check(cells[0] && cells[0].text === "努力工作中", "基线：状态格写“努力工作中”", cells[0] && cells[0].text);
  check(cells[1] && cells[1].k === "下次短休息" && cells[1].led, "基线：短休息那一格是数码管倒计时", JSON.stringify(cells[1]));
  check(cells[2] && cells[2].k === "下次喝水提醒" && cells[2].led, "基线：自定义提醒那一格是数码管倒计时", JSON.stringify(cells[2]));
  const m1 = String((cells[1] || {}).text || "").match(/(\d+)\s*分/);
  check(!!m1 && Number(m1[1]) >= 23 && Number(m1[1]) <= 25, "基线：短休息剩余≈25 分钟（读 .sr-only）", (cells[1] || {}).text);

  let tl = await timelineInfo(page);
  check(/^待触发 \d+ 条$/.test(tl.count), "基线：时间线标题是“待触发 N 条”", tl.count);
  const tlNames = tl.rows.map((r) => r.name);
  check(tlNames.includes("短休息") && tlNames.includes("喝水提醒"),
    "基线：时间线同时列出短休息与喝水提醒", JSON.stringify(tlNames));
  check(tl.rows.every((r) => r.src === "倒计时"), "基线：两条来源都写“倒计时”", JSON.stringify(tl.rows.map((r) => r.src)));
  check(tl.rows.every((r) => /^\d{2}:\d{2}$/.test(r.time)), "基线：时间线每行都有 HH:MM", JSON.stringify(tl.rows.map((r) => r.time)));

  // ---------- 2. 跨午夜：明天 00:10 ----------
  await patchSettings(page, {
    short: { enabled: true, mode: "fixed", durationMs: 20_000, notifyBeforeMs: 0, postponeMs: 120_000, pauseAction: "postpone", slots: [{ id: "s-mid", hour: 0, minute: 10, repeat: "daily", days: [], enabled: true }] },
    customBreaks: [],
  });
  await waitState(page, (st) => st && st.rest && st.rest.name === "短休息");
  cells = await statusCells(page);
  const now = new Date();
  const pastMidnightSlot = now.getHours() > 0 || (now.getHours() === 0 && now.getMinutes() > 10);
  const wantText = pastMidnightSlot ? "明天 00:10" : "00:10";
  check(cells[1] && cells[1].text === wantText,
    "跨午夜：固定时刻 00:10 写成“明天 00:10”（不写“今日已无提醒”）", (cells[1] || {}).text);
  check(cells[1] && cells[1].led === false, "跨午夜：跨天那一格不是数码管（没有时间可倒数）", JSON.stringify(cells[1]));

  // ---------- 3. 全关：未安排 + 立即休息禁用 ----------
  await patchSettings(page, { short: { enabled: false }, long: { enabled: false }, takeout: { enabled: false }, customBreaks: [] });
  await waitState(page, (st) => st && st.nextAt === 0);
  cells = await statusCells(page);
  check(cells.length === 2 && cells[1].k === "下次休息提醒" && cells[1].text === "未安排",
    "全关：三格收成“下次休息提醒 / 未安排”", JSON.stringify(cells));
  const qa = await page.evaluate(() => {
    const b = document.getElementById("qa-break");
    return b ? { disabled: b.disabled, title: b.title || "" } : null;
  });
  check(qa && qa.disabled === true, "全关：「立即休息」禁用（点了也不会有反应）", JSON.stringify(qa));
  check(qa && qa.title.length > 0, "全关：禁用原因写在 title 里", qa && qa.title);
  tl = await timelineInfo(page);
  check(tl.count === "今天没有未触发的提醒" && tl.rows.length === 0, "全关：时间线为空", tl.count);

  // ---------- 4. 手动暂停 / 恢复 ----------
  await patchSettings(page, { short: SHORT_25, customBreaks: [{ id: "cb-water", name: "喝水提醒", ...WATER_45 }] });
  await waitState(page, (st) => st && st.nextType === "short");
  await page.click("#qa-pause");
  let st = await waitState(page, (s) => s && s.paused === true);
  cells = await statusCells(page);
  check(cells[0].text === "暂停休息", "暂停：状态格写“暂停休息”", cells[0].text);
  check(cells[0].title.includes("暂停原因：手动暂停"), "暂停：原因挂在 title 上（悬停可看）", cells[0].title);
  check(cells[1].led === true, "暂停：倒计时照常是数码管（不冻钟）", JSON.stringify(cells[1]));
  const pauseBtn = await page.evaluate(() => (document.getElementById("qa-pause") || {}).textContent);
  check(String(pauseBtn).trim() === "恢复休息", "暂停：按钮翻成“恢复休息”", pauseBtn);
  // 恢复
  await page.click("#qa-pause");
  st = await waitState(page, (s) => s && s.paused === false);
  cells = await statusCells(page);
  check(cells[0].text === "努力工作中", "恢复：状态格回到“努力工作中”", cells[0].text);
  check(!cells[0].title, "恢复：暂停原因 title 清空", cells[0].title);

  // ---------- 5. 休息中 + 另一条提醒仍在排队 ----------
  await patchSettings(page, { short: Object.assign({}, SHORT_25, { durationMs: 60_000 }) });
  await waitState(page, (st2) => st2 && st2.nextType === "short");
  await page.click("#qa-break");
  st = await waitState(page, (s) => s && s.phase === "break");
  check(st && st.phase === "break", "休息中：点了「立即休息」真的进入休息", st && st.phase);
  cells = await statusCells(page);
  check(cells.some((c) => c.k === "正在休息" && c.led), "休息中：多出「正在休息」数码管格", JSON.stringify(cells.map((c) => c.k)));
  check(cells.some((c) => c.k === "下次喝水提醒"),
    "休息中：仍报「下次喝水提醒」（旧版这里固定写“—”，把排队中的提醒藏掉了）",
    JSON.stringify(cells.map((c) => c.k)));
  check(cells[0].text === "休息中", "休息中：状态格写“休息中”", cells[0].text);
  const skipLabel = await page.evaluate(() => (document.getElementById("qa-skip") || {}).textContent);
  check(String(skipLabel).trim() === "提前结束", "休息中：「跳过本次」翻成“提前结束”", skipLabel);
  // 收尾：提前结束这次休息，别把它留到后面的场景
  await page.click("#qa-skip");
  st = await waitState(page, (s) => s && s.phase !== "break");

  // ---------- 5b. 自定义提醒在弹：那一格不叫“提前结束”，而且是灰的 ----------
  // 口径（2026-11-15）：只有短 / 长休息才配“提前结束”。喝水提醒弹窗时这一格
  // 既不许写成“提前结束”（它结束不了那条提醒），也不许留着可点 —— 此刻人已经在
  // 休息里，“跳过本次”也没有目标可跳。
  await patchSettings(page, {
    short: Object.assign({}, SHORT_25, { enabled: false }),
    customBreaks: [{
      id: "cb-water", name: "喝水提醒",
      config: Object.assign({}, WATER_45.config, { intervalMs: 3_000, durationMs: 8_000 }),
    }],
  });
  st = await waitState(page, (s) => s && s.phase === "break" && s.breakType === "cb-water", 25_000);
  check(st && st.breakType === "cb-water",
    "自定义提醒在弹：快照带出 breakType=cb-water（不是拿 nextType 猜）", st && st.breakType);
  const foreignSkip = await page.evaluate(() => {
    const b = document.getElementById("qa-skip");
    return { text: (b || {}).textContent, disabled: !!(b && b.disabled), title: (b || {}).title || "" };
  });
  check(String(foreignSkip.text).trim() === "跳过本次",
    "自定义提醒在弹：「跳过本次」不写“提前结束”", foreignSkip.text);
  check(foreignSkip.disabled === true, "自定义提醒在弹：那一格是灰的", JSON.stringify(foreignSkip));
  check(String(foreignSkip.title).includes("自定义提醒"),
    "自定义提醒在弹：灰的原因写在 title 上", foreignSkip.title);
  // 等它自己走完（8 秒），别把它留给后面的时间线场景
  st = await waitState(page, (s) => s && s.phase !== "break", 30_000);
  check(st && st.phase !== "break", "自定义提醒：走完自动收窗", st && st.phase);

  // ---------- 6. 时间线：行内容与来源 ----------
  await patchSettings(page, {
    short: { enabled: true, mode: "fixed", durationMs: 20_000, notifyBeforeMs: 0, postponeMs: 120_000, pauseAction: "postpone", slots: [{ id: "s-f1", hour: 23, minute: 50, repeat: "daily", days: [], enabled: true }] },
    customBreaks: [{ id: "cb-water", name: "喝水提醒", ...WATER_45 }],
  });
  await sleep(900);
  st = await getState(page);
  tl = await timelineInfo(page);
  check(tl.count === `待触发 ${(st.timeline || []).length} 条`,
    "时间线：条数与绑定 st.timeline 一致", tl.count + " vs " + (st.timeline || []).length);
  check(tl.rows.length === (st.timeline || []).length, "时间线：行数与条数一致", tl.rows.length);
  const fixedRow = tl.rows.find((r) => r.name === "短休息");
  check(!!fixedRow && fixedRow.src === "固定时刻", "时间线：固定时刻那条来源写“固定时刻”", JSON.stringify(fixedRow));
  const cdRow = tl.rows.find((r) => r.name === "喝水提醒");
  check(!!cdRow && cdRow.src === "倒计时", "时间线：倒计时那条来源写“倒计时”", JSON.stringify(cdRow));
  check(tl.rows.every((r, i) => i === 0 || r.time >= tl.rows[i - 1].time), "时间线：按时刻升序", JSON.stringify(tl.rows.map((r) => r.time)));

  // ---------- 7. 统计看板与绑定一致 ----------
  await page.evaluate(() => document.querySelector('.nav-item[data-tab="stats"]').click());
  await sleep(800);
  const board = await page.evaluate(async () => {
    const t = (await window.api.getStats()).today; // 绑定返回 {today, periods}
    const pick = (sel) => {
      const el = document.querySelector(sel);
      return el ? el.querySelector(".bv").textContent.trim() : "";
    };
    return {
      date: (document.getElementById("stats-date") || {}).textContent || "",
      done: pick('[data-b="br-done"]'),
      post: pick('[data-b="br-post"]'),
      skip: pick('[data-b="br-skip"]'),
      early: pick('[data-b="br-early"]'),
      t: {
        shortCount: t.shortCount || 0,
        longCount: t.longCount || 0,
        postponeCount: t.postponeCount || 0,
        skipCount: t.skipCount || 0,
        endEarlyCount: t.endEarlyCount || 0,
      },
    };
  });
  check(/（\d{4}-\d{2}-\d{2} 周[一二三四五六日]）/.test(board.date),
    "看板：日期写成（YYYY-MM-DD 周X）", board.date);
  const numOf = (s) => Number(String(s).replace(/[^\d]/g, "")) || 0;
  check(numOf(board.done) === board.t.shortCount + board.t.longCount,
    "看板：完成次数 = 短 + 长（绑定口径）", board.done + " vs " + (board.t.shortCount + board.t.longCount));
  check(numOf(board.post) === board.t.postponeCount, "看板：推迟次数 = 绑定 postponeCount", board.post + " vs " + board.t.postponeCount);
  check(numOf(board.skip) === board.t.skipCount, "看板：跳过次数 = 绑定 skipCount", board.skip + " vs " + board.t.skipCount);
  check(numOf(board.early) === board.t.endEarlyCount, "看板：提前结束次数 = 绑定 endEarlyCount", board.early + " vs " + board.t.endEarlyCount);

  check(pageErrors.length === 0, "全程无 JS 异常", JSON.stringify(pageErrors));

  await page.close().catch(() => {});
  await browser.close();

  console.log("");
  console.log("结果：" + (checks - failures.length) + " / " + checks + " 通过");
  if (failures.length) {
    console.log("失败项：");
    for (const f of failures) console.log("  - " + f);
    process.exit(1);
  }
}

main().catch((e) => {
  console.error("探针异常：" + (e && e.stack ? e.stack : e));
  process.exit(2);
});
