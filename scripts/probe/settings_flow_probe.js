/**
 * 设置页「写了什么就存什么」的功能流探针（Playwright 驱动系统 Edge）。
 *
 * 已覆盖的三层分工：
 *   · settings_ui_probe.js     —— 广度：六个 tab 的控件、可访问名、溢出、静态契约；
 *   · settings_modals_probe.js —— 对话框：打开 / 焦点 / Esc / Tab 陷阱 / 遮罩；
 *   · 本脚本                    —— **写入链路**：动一下控件 → 配置里真的变了 ——
 *     以及几条只在 JS 侧成立的判定（服务端不管）：
 *
 *   1. 快捷键录入：Ctrl+Alt+K 录进去 → settings.shortcuts 落盘；Esc 清空；
 *      裸键（F5）与重复键**整条拒收**（框里保持原值），只按修饰键不出值；
 *   2. 提示音：音量滑块 / 总开关 / 播放方式按钮（single → random → sequence）；
 *   3. 主题：切深色 → html[data-theme] 与 settings.theme 同步；
 *   4. 外卖：提前通知填超范围（999）→ 存进去被折到 300（归一化在服务端，这里验整条链路）；
 *   5. 恢复默认设置：点下去弹确认框，**取消**后配置一字未改（防误触丢配置）。
 *
 * 运行：由 probe_settings_ui.py 起好无头服务后 `node settings_flow_probe.js` 拉起。
 * 环境变量：WR_BASE（服务地址）、WR_OUT（截图目录）。
 */
const { chromium } = require("playwright");
const path = require("path");
const fs = require("fs");

const BASE = process.env.WR_BASE || "http://127.0.0.1:29741";
const OUT = process.env.WR_OUT || path.join(process.env.TEMP || ".", "wr_settings_ui");
const PRELOAD = path.join(__dirname, "..", "probe_preload.js");
const delay = (ms) => new Promise((r) => setTimeout(r, ms));

let checks = 0;
const failures = [];
function check(ok, label, detail = "") {
  checks++;
  console.log(("  " + (ok ? "ok  " : "FAIL") + " " + label + (detail ? "  | " + detail : "")).replace(/\n/g, " "));
  if (!ok) failures.push(label + (detail ? "  | " + detail : ""));
  return ok;
}

// autoSave 有防抖，落盘不是同步的：读配置前等一会儿，别把「还没存」当成没生效。
const SAVE_WAIT = 900;

async function main() {
  if (!fs.existsSync(OUT)) fs.mkdirSync(OUT, { recursive: true });
  const browser = await chromium.launch({ channel: "msedge", headless: true });
  const ctx = await browser.newContext({ viewport: { width: 1000, height: 820 }, deviceScaleFactor: 1 });
  const page = await ctx.newPage();
  page.setDefaultTimeout(8000);
  try {
    const consoleErrors = [];
    page.on("pageerror", (e) => consoleErrors.push("pageerror: " + e.message));
    page.on("console", (m) => {
      if (m.type() !== "error") return;
      const t = m.text();
      if (/favicon|net::ERR_|Failed to load resource/i.test(t)) return;
      consoleErrors.push("console: " + t);
    });
    await page.addInitScript({ content: fs.readFileSync(PRELOAD, "utf-8") });
    await page.goto(BASE + "/settings.html", { waitUntil: "domcontentloaded" });
    let ready = false;
    for (let i = 0; i < 40; i++) {
      ready = await page.evaluate(() => typeof window.api === "object" && window.api !== null);
      if (ready) break;
      await delay(150);
    }
    if (!check(ready, "settings.html 装载完成且 window.api 就绪")) return;
    await delay(700);

    const cfg = async () => await page.evaluate(async () => await window.api.getSettings());
    const tab = async (id) => {
      await page.locator(`.nav-item[data-tab="${id}"]`).click();
      await delay(200);
    };

    // ---------- 一、快捷键录入 ----------
    console.log("\n[F1] 快捷键录入：合法组合写进去、裸键与重复键整条拒收");
    await tab("shortcuts");
    const before = (await cfg()).shortcuts || {};

    // 1) 合法组合：Ctrl+Alt+K → sc-pause
    await page.locator("#sc-pause").focus();
    await page.keyboard.press("Control+Alt+KeyK");
    await delay(150);
    const v1 = await page.inputValue("#sc-pause");
    check(v1 === "Ctrl+Alt+K", "Ctrl+Alt+K 录入到「暂停 / 恢复休息」", v1);
    await delay(SAVE_WAIT);
    const c1 = (await cfg()).shortcuts || {};
    check(c1.pause === "Ctrl+Alt+K", "快捷键 pause 已落盘", c1.pause);

    // 2) Esc 清空 = 禁用该热键
    await page.locator("#sc-pause").focus();
    await page.keyboard.press("Escape");
    await delay(150);
    const v2 = await page.inputValue("#sc-pause");
    check(v2 === "", "Esc 清空录入框（= 禁用这个热键）", JSON.stringify(v2));
    await delay(SAVE_WAIT);
    const c2 = (await cfg()).shortcuts || {};
    check(c2.pause === "", "清空后 shortcuts.pause 落盘为空串", JSON.stringify(c2.pause));

    // 3) 裸键 F5：必须整条拒收，框里保持原值
    const keep = await page.inputValue("#sc-postpone");
    await page.locator("#sc-postpone").focus();
    await page.keyboard.press("F5");
    await delay(200);
    const v3 = await page.inputValue("#sc-postpone");
    check(v3 === keep, "裸键 F5 被拒收，录入框保持原值", `${JSON.stringify(keep)} → ${JSON.stringify(v3)}`);

    // 4) 重复键，两个方向都要测（判定是「只在靠后那一栏标红/拒收」，见 shortcutIssue）：
    //    a. 往**靠后**的栏（跳转下一次休息）录一个和前面相同的键 → 整条拒收、保持原值；
    //    b. 往**靠前**的栏（推迟）录一个和后面相同的键 → 写入，但后面那栏被标红并给出说明。
    //    配置里写的是 Ctrl / Super，Playwright 的键名是 Control / Meta，先映射
    const PW_KEY = { Ctrl: "Control", Alt: "Alt", Shift: "Shift", Super: "Meta" };
    const pressCombo = async (sel, combo) => {
      const parts = combo.split("+");
      const mods = parts.slice(0, -1).map((k) => PW_KEY[k] || k);
      const main = parts.slice(-1)[0];
      await page.locator(sel).focus();
      for (const k of mods) await page.keyboard.down(k);
      await page.keyboard.press(main);
      for (const k of mods.slice().reverse()) await page.keyboard.up(k);
      await delay(220);
    };
    const postponeKey = await page.inputValue("#sc-postpone");
    const nextKey = await page.inputValue("#sc-nextBreak");
    if (!nextKey || !postponeKey) {
      check(false, "预备：postpone 与 nextBreak 都有值（重复键用例需要）");
    } else {
      // a. 靠后的栏录成和靠前相同的键 → 拒收
      await pressCombo("#sc-nextBreak", postponeKey);
      const vNext = await page.inputValue("#sc-nextBreak");
      check(vNext === nextKey, `靠后的「跳转下一次休息」录成 ${postponeKey}（与推迟撞车）被拒收`,
        `${JSON.stringify(nextKey)} → ${JSON.stringify(vNext)}`);

      // b. 靠前的栏录成和靠后相同的键 → 接受，但后面那栏标红 + 出现汇总说明
      await pressCombo("#sc-postpone", nextKey);
      const vPost = await page.inputValue("#sc-postpone");
      check(vPost === nextKey, `靠前的「推迟」录成 ${nextKey} 会被接受（判定归到靠后那一栏）`, vPost);
      const mark = await page.evaluate((otherId) => {
        const el = document.getElementById(otherId);
        const warn = document.getElementById("sc-warn");
        return {
          bad: el.classList.contains("bad"),
          title: el.title || "",
          warnHidden: warn ? warn.classList.contains("hidden") : true,
          warnText: warn ? (warn.textContent || "").trim() : "",
        };
      }, "sc-nextBreak");
      check(mark.bad, "重复键把靠后那一栏标红（.bad）", `title=${mark.title}`);
      check(!mark.warnHidden && /同一个组合键/.test(mark.warnText), "页面给出「和某某用了同一个组合键」的说明",
        mark.warnText.slice(0, 40));

      // 复原：把推迟写回原键，红框与说明都应消失
      await pressCombo("#sc-postpone", postponeKey);
      const cleared = await page.evaluate(() => {
        const el = document.getElementById("sc-nextBreak");
        const warn = document.getElementById("sc-warn");
        return { bad: el.classList.contains("bad"), warnHidden: warn ? warn.classList.contains("hidden") : true };
      });
      check(!cleared.bad && cleared.warnHidden, "改回不重复的键后红框与说明都消失",
        `bad=${cleared.bad} warnHidden=${cleared.warnHidden}`);
    }

    // 5) 只按住修饰键不出值
    await page.locator("#sc-endEarly").focus();
    const prevEnd = await page.inputValue("#sc-endEarly");
    await page.keyboard.down("Control");
    await page.keyboard.down("Alt");
    await delay(120);
    const during = await page.inputValue("#sc-endEarly");
    await page.keyboard.up("Alt");
    await page.keyboard.up("Control");
    check(during === prevEnd, "只按住修饰键不产生组合键值", `${JSON.stringify(prevEnd)} → ${JSON.stringify(during)}`);

    // 6) 回填一致性：界面上的四个键值 == settings.shortcuts
    const nowSc = (await cfg()).shortcuts || {};
    const ui = {};
    for (const [id, key] of [["sc-postpone", "postpone"], ["sc-endEarly", "endEarly"],
                             ["sc-pause", "pause"], ["sc-nextBreak", "nextBreak"]]) {
      ui[key] = await page.inputValue("#" + id);
    }
    const same = Object.keys(ui).every((k) => String(ui[k]) === String(nowSc[k] ?? ""));
    check(same, "四个快捷键输入框的值与 settings.shortcuts 逐项一致", JSON.stringify(ui));

    // ---------- 二、提示音 ----------
    console.log("\n[F2] 提示音：音量 / 总开关 / 播放方式");
    await tab("general");
    const sndBefore = (await cfg()).sound || {};

    // 音量：取滑块自己刻度的另一个端点
    const volSet = await page.locator("#snd-volume").evaluate((el) => {
      const min = Number(el.min || 0);
      const max = Number(el.max || 1);
      const target = Number(el.value) > (min + max) / 2 ? min + 0.05 : max - 0.05;
      el.value = String(target);
      el.dispatchEvent(new Event("input", { bubbles: true }));
      el.dispatchEvent(new Event("change", { bubbles: true }));
      return target;
    });
    await delay(SAVE_WAIT);
    const snd1 = (await cfg()).sound || {};
    check(Math.abs(Number(snd1.volume) - volSet) < 0.011, "音量滑块改动落盘 = 滑块当前值",
      `ui=${volSet} cfg=${snd1.volume}`);
    const volLabel = ((await page.textContent("#snd-volume-val")) || "").trim();
    check(volLabel !== "", "音量数值标签有文字", volLabel);

    // 总开关：关掉 → 配置 false → 打开复原
    const enBefore = await page.isChecked("#snd-enabled");
    await page.click("#snd-enabled");
    await delay(SAVE_WAIT);
    const snd2 = (await cfg()).sound || {};
    check(snd2.enabled === !enBefore, "提示音总开关翻转落盘", `ui ${enBefore} → cfg ${snd2.enabled}`);
    await page.click("#snd-enabled");
    await delay(SAVE_WAIT);
    const snd3 = (await cfg()).sound || {};
    check(snd3.enabled === enBefore, "提示音总开关翻回来落盘", `cfg ${snd3.enabled}`);

    // 播放方式按钮：点一次切一个值，三次回到原点（合法集合 single/random/sequence）
    const modes = [];
    for (let i = 0; i < 3; i++) {
      await page.click("#snd-mode");
      await delay(SAVE_WAIT);
      modes.push(((await cfg()).sound || {}).mode);
    }
    const allowed = ["single", "random", "sequence"];
    const allLegal = modes.every((m) => allowed.includes(m));
    check(allLegal, "播放方式每次点击都落在合法集合内", modes.join(" → "));
    check(new Set(modes).size === 3, "连点三次覆盖三种播放方式（不是原地打转）", modes.join(" → "));
    check(String(modes[2]) === String(sndBefore.mode), "点满一圈回到起始播放方式",
      `${sndBefore.mode} → ${modes.join(" → ")}`);

    // ---------- 三、主题 ----------
    console.log("\n[F3] 主题切换：单选按钮 → html[data-theme] + settings.theme");
    const themeBefore = (await cfg()).theme;
    const target = themeBefore === "dark" ? "light" : "dark";
    await page.locator(`input[name="theme"][value="${target}"]`).check();
    await delay(SAVE_WAIT);
    const dt = await page.evaluate(() => document.documentElement.dataset.theme);
    const themeAfter = (await cfg()).theme;
    check(themeAfter === target, `设置里切到 ${target} 后配置跟着变`, `cfg=${themeAfter}`);
    check(dt === target, `html[data-theme] == ${target}（样式真的换了，不只是存了个值）`, `data-theme=${dt}`);
    await page.locator(`input[name="theme"][value="${themeBefore}"]`).check();
    await delay(SAVE_WAIT);
    check((await cfg()).theme === themeBefore, "再切回原主题", `cfg=${(await cfg()).theme}`);

    // ---------- 四、外卖提前通知：超范围要被折回 ----------
    console.log("\n[F4] 外卖提前通知填 999 秒 → 存进去折到 300");
    await tab("timing");
    // 外卖卡片可能收着：先展开
    const tkCard = page.locator("#tk-card");
    const tkLead = page.locator("#tk-lead");
    if (!(await tkLead.isVisible())) {
      const head = tkCard.locator(".card-head, summary, .fold-head").first();
      if (await head.count()) {
        await head.click();
        await delay(220);
      }
    }
    if (await tkLead.isVisible()) {
      await tkLead.fill("999");
      await tkLead.dispatchEvent("change");
      await delay(SAVE_WAIT);
      const lead = ((await cfg()).takeout || {}).leadSec;
      check(Number(lead) === 300, "leadSec=999 落盘时折到 300", lead);
      await tkLead.fill("10");
      await tkLead.dispatchEvent("change");
      await delay(SAVE_WAIT);
    } else {
      check(false, "找不到可用的「外卖提前通知」输入框（#tk-lead）");
    }

    // ---------- 五、恢复默认设置：取消后一字未改 ----------
    console.log("\n[F5] 恢复默认设置：取消 → 配置一字未改");
    await tab("about");
    const snap = JSON.stringify(await cfg());
    const reset = page.locator("#d-reset");
    if (await reset.count()) {
      await reset.click();
      await delay(320);
      const opened = await page.evaluate(() => {
        const m = document.querySelectorAll(".modal");
        for (const el of m) if (!el.classList.contains("hidden")) return el.id;
        return "";
      });
      check(opened === "confirm-modal", "点「恢复默认设置」弹出确认框", opened || "（没弹）");
      if (opened === "confirm-modal") {
        await page.click("#cf-cancel");
        await delay(400);
      }
      const after = JSON.stringify(await cfg());
      check(after === snap, "取消后配置没有任何字段被改（快照逐字相同）",
        after === snap ? "相同" : "有差异");
    } else {
      check(false, "关于页有「恢复默认设置」按钮（#d-reset）");
    }

    // ---------- 六、全程无 JS 错误 ----------
    check(consoleErrors.length === 0, "全流程没有任何 JS 报错", consoleErrors.slice(0, 3).join(" ; "));
    await page.screenshot({ path: path.join(OUT, "flow_last.png") });
  } finally {
    await browser.close();
  }
  return finish();
}

function finish() {
  console.log(`\n结果：${checks} 项断言，失败 ${failures.length} 项`);
  if (failures.length) {
    for (const f of failures) console.log("  - " + f);
    process.exitCode = 1;
  }
}

main().catch((e) => {
  console.error("[settings-flow] 探针异常:", (e && e.stack) || e);
  process.exitCode = 2;
});
