/**
 * 设置页十个对话框的**交互**探针（Playwright 驱动系统 Edge）。
 *
 * 与 settings_ui_probe.js 的分工：那个只做**静态契约**（role / aria-modal /
 * aria-labelledby / 有可聚焦项 / 宽度在视口内）；本脚本管**打开与关闭的整条动作**：
 *
 *   1. 真触发：点页面上真实存在的按钮把对话框打开（不是直接改 class），
 *      断言「可见 + 焦点落进 .modal-box」；
 *   2. Esc 关闭：十个都必须能关（其中五个历史上只能点按钮关，见 settings.js 的
 *      ESC_DISMISS 兜底表）；
 *   3. 焦点归位：关掉之后焦点必须回到打开它的那个元素，不能掉到 <body>；
 *   4. Tab 陷阱：对话框打开时连按 Tab 焦点不得跑到框外，Shift+Tab 从第一个回绕到最后；
 *   5. 点遮罩关闭：支持的对话框点空白处要关；
 *   6. 无对话框时按 Esc 是空操作（不能误关别的东西、不能报错）。
 *
 * 分两类覆盖，如实标注：
 *   · 有真实触发器的六个 → 走真点击；
 *   · 没有触发器的四个（统计损坏提示 / 提示音体检 / 更新提示 / 更新完成 —— 它们的
 *     打开条件是 Go 侧的通知或联网检查结果，无头环境造不出来）→ 用「改 class」打开，
 *     这本身也是 settings.js 里唯一的开关方式（焦点管理就挂在 class 的 MutationObserver
 *     上），所以断言同样有意义，只是标注为「class 开关」。
 *
 * 运行：由 probe_settings_ui.py 起好无头服务后 `node settings_modals_probe.js` 拉起。
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
const notes = [];
function check(ok, label, detail = "") {
  checks++;
  console.log(("  " + (ok ? "ok  " : "FAIL") + " " + label + (detail ? "  | " + detail : "")).replace(/\n/g, " "));
  if (!ok) failures.push(label + (detail ? "  | " + detail : ""));
  return ok;
}
function note(label, detail = "") {
  notes.push(label + (detail ? "  | " + detail : ""));
  console.log("  ·    " + label + (detail ? "  | " + detail : ""));
}

// 有真实触发器的六个。trigger 是「点它就能打开」的选择器；pre 是展开折叠容器用的。
const TRIGGERED = [
  { id: "color-modal", tab: "general", trigger: "#ap-color-open", what: "外观色块" },
  {
    id: "app-pick-modal", tab: "general", trigger: "#app-pause-pick", what: "从运行的应用选择",
    pre: "#app-advanced summary", // 应用列表默认折叠在 details 里
  },
  {
    id: "prompt-modal", tab: "general", trigger: "#app-pause-add", what: "手动输入应用名称",
    pre: "#app-advanced summary",
  },
  {
    // 提醒内容面板在「时间规划」tab 里（不是通用 tab），批量按钮在面板标题行、默认就可见
    id: "batch-modal", tab: "timing",
    trigger: '.msg-panel[data-kind="short"] .msg-add', what: "批量新增提醒内容",
  },
  {
    id: "confirm-modal", tab: "timing",
    trigger: '.msg-panel[data-kind="short"] .msg-item button[title="删除"]',
    what: "删除提醒内容",
    pre: '.msg-panel[data-kind="short"] .msg-toggle', // 行本身在默认收起的 .msg-body 里
  },
  { id: "notices-modal", tab: "about", trigger: "#d-notices", what: "开源鸣谢" },
];

// 没有真实触发器的四个：打开条件是 Go 侧通知 / 联网检查结果，无头环境造不出来。
// dismiss 是它们自己的关闭按钮 id（Esc 兜底就是点这个按钮）。
const CLASS_ONLY = [
  { id: "stats-modal", dismiss: "st-ok", what: "统计文件损坏提示" },
  { id: "snd-scan-modal", dismiss: "ss-close", what: "提示音目录体检" },
  { id: "update-modal", dismiss: "up-later", what: "发现新版本" },
  { id: "updated-modal", dismiss: "upd-ok", what: "更新完成" },
];

async function activeInfo(page) {
  return page.evaluate(() => {
    const el = document.activeElement;
    if (!el || el === document.body) return { tag: "BODY", inModal: false, id: "", cls: "" };
    return {
      tag: el.tagName.toLowerCase(),
      id: el.id || "",
      cls: String(el.className || ""),
      title: el.getAttribute("title") || "",
      inModal: !!el.closest(".modal"),
    };
  });
}

async function modalVisible(page, id) {
  return page.evaluate((mid) => {
    const el = document.getElementById(mid);
    if (!el) return false;
    return !el.classList.contains("hidden") && el.getBoundingClientRect().width > 0;
  }, id);
}

async function closeAll(page) {
  await page.evaluate(() => {
    document.querySelectorAll(".modal").forEach((m) => m.classList.add("hidden"));
  });
  await delay(60);
}

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

    // ---------- 一、有真实触发器的六个 ----------
    console.log("\n[A] 真实点击打开 → 焦点进框 → Esc 关闭 → 焦点归位");
    for (const m of TRIGGERED) {
      await page.locator(`.nav-item[data-tab="${m.tab}"]`).click();
      await delay(220);
      const vis = async (loc) => (await loc.count()) > 0 && (await loc.isVisible());
      const trg = page.locator(m.trigger).first();
      // 触发控件藏在折叠容器里时先展开（只展开、不折叠：每个用例只跑一次）
      if (m.pre && !(await vis(trg))) {
        const pre = page.locator(m.pre).first();
        if (await vis(pre)) {
          await pre.click();
          await delay(200);
        }
      }
      if (!(await trg.count()) || !(await trg.isVisible())) {
        check(false, `对话框 ${m.id}（${m.what}）能找到可见触发控件 ${m.trigger}`);
        continue;
      }
      // 先把焦点放到触发控件上：dialog 关闭后要「回到打开它的那个元素」
      await trg.focus();
      const trgKey = await page.evaluate((sel) => {
        const el = document.querySelector(sel);
        if (!el) return null;
        return { id: el.id || "", title: el.getAttribute("title") || "", cls: String(el.className || "") };
      }, m.trigger);
      await trg.click();
      await delay(260);

      const opened = await modalVisible(page, m.id);
      if (!check(opened, `对话框 ${m.id}（${m.what}）点触发控件后打开`)) {
        await closeAll(page);
        continue;
      }
      const a1 = await activeInfo(page);
      check(a1.inModal, `对话框 ${m.id} 打开后焦点在框内`, `${a1.tag}${a1.id ? "#" + a1.id : ""}.${a1.cls}`);

      await page.screenshot({ path: path.join(OUT, `modal_${m.id}.png`) });

      await page.keyboard.press("Escape");
      await delay(240);
      const closed = !(await modalVisible(page, m.id));
      check(closed, `对话框 ${m.id} 按 Esc 关闭`);

      const a2 = await activeInfo(page);
      const back = trgKey && (a2.id === trgKey.id) &&
        (trgKey.id !== "" || (a2.title === trgKey.title && a2.cls === trgKey.cls));
      check(!!back && !a2.inModal, `对话框 ${m.id} 关闭后焦点回到触发控件`,
        `active=${a2.tag}${a2.id ? "#" + a2.id : ""}${a2.title ? "[title=" + a2.title + "]" : ""}`);
      await closeAll(page);
    }

    // ---------- 二、没有触发器的四个：class 开关 + Esc ----------
    console.log("\n[B] 无触发器（class 开关）：焦点进框 → Esc 关闭 → 焦点归位");
    await page.locator('.nav-item[data-tab="about"]').click();
    await delay(200);
    for (const m of CLASS_ONLY) {
      // 先让页面上有个确定的焦点作为「打开者」
      const openerOk = await page.evaluate(() => {
        const el = document.getElementById("d-notices");
        if (!el) return false;
        el.focus();
        return document.activeElement === el;
      });
      if (!check(openerOk, `[${m.id}] 预备：先聚焦页面上的按钮`)) continue;

      await page.evaluate((mid) => document.getElementById(mid).classList.remove("hidden"), m.id);
      await delay(240);
      const opened = await modalVisible(page, m.id);
      if (!check(opened, `对话框 ${m.id}（${m.what}）改 class 后可见`)) {
        await closeAll(page);
        continue;
      }
      const a1 = await activeInfo(page);
      check(a1.inModal, `对话框 ${m.id} 打开后焦点在框内`, `${a1.tag}${a1.id ? "#" + a1.id : ""}`);

      await page.keyboard.press("Escape");
      await delay(260);
      check(!(await modalVisible(page, m.id)), `对话框 ${m.id} 按 Esc 关闭（走它自己的 ${m.dismiss} 按钮）`);
      const a2 = await activeInfo(page);
      check(a2.id === "d-notices" && !a2.inModal, `对话框 ${m.id} 关闭后焦点回到打开前的按钮`,
        `active=${a2.tag}${a2.id ? "#" + a2.id : ""}`);
      await closeAll(page);
    }

    // ---------- 三、Esc 不会漏到「自己有 Esc 处理」的对话框之外 ----------
    console.log("\n[C] Esc 的边界：没有对话框时是空操作");
    await page.locator('.nav-item[data-tab="general"]').click();
    await delay(200);
    const anyOpen = await page.evaluate(() =>
      [...document.querySelectorAll(".modal")].some((m) => !m.classList.contains("hidden")));
    check(!anyOpen, "预备：此刻没有任何对话框开着");
    const beforeUrl = page.url();
    await page.keyboard.press("Escape");
    await delay(150);
    const stillFine = await page.evaluate(() => !!document.querySelector(".tab.active"));
    check(stillFine && page.url() === beforeUrl, "无对话框时按 Esc 不改页面状态（不误关、不跳转）");

    // ---------- 四、Tab 陷阱 ----------
    console.log("\n[D] Tab 焦点陷阱（打开鸣谢对话框连按 20 次 Tab）");
    await page.locator('.nav-item[data-tab="about"]').click();
    await delay(200);
    await page.locator("#d-notices").click();
    await delay(400);
    if (await modalVisible(page, "notices-modal")) {
      let escaped = false;
      let leakedTo = "";
      for (let i = 0; i < 20; i++) {
        await page.keyboard.press("Tab");
        await delay(30);
        const a = await activeInfo(page);
        if (!a.inModal) {
          escaped = true;
          leakedTo = `${a.tag}${a.id ? "#" + a.id : ""}`;
          break;
        }
      }
      check(!escaped, "连按 20 次 Tab，焦点始终在对话框内", escaped ? "漏到 " + leakedTo : "");
      // Shift+Tab 从第一个回绕到最后
      const wrap = await page.evaluate(() => {
        const m = document.getElementById("notices-modal");
        const box = m.querySelector(".modal-box");
        const items = [...box.querySelectorAll(
          'a[href], button:not([disabled]), input:not([disabled]):not([type="hidden"]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])'
        )].filter((el) => el.offsetParent !== null);
        if (items.length < 2) return { ok: false, why: "可聚焦项不足 2 个" };
        items[0].focus();
        return { ok: document.activeElement === items[0], first: items[0].className, last: items[items.length - 1].className };
      });
      await page.keyboard.press("Shift+Tab");
      await delay(80);
      const wrappedTo = await page.evaluate(() => {
        const m = document.getElementById("notices-modal");
        return m.contains(document.activeElement);
      });
      check(wrap.ok && wrappedTo, "Shift+Tab 在第一个控件上回绕（焦点仍在框内）", wrap.why || "");
      await page.keyboard.press("Escape");
      await delay(200);
      check(!(await modalVisible(page, "notices-modal")), "Tab 陷阱测试后 Esc 能正常关掉");
    } else {
      check(false, "打开鸣谢对话框（Tab 陷阱测试的前提）");
    }

    // ---------- 五、点遮罩关闭 ----------
    console.log("\n[E] 点遮罩关闭（color / confirm）");
    for (const m of [
      { id: "color-modal", tab: "general", trigger: "#ap-color-open" },
      {
        id: "confirm-modal", tab: "timing",
        trigger: '.msg-panel[data-kind="short"] .msg-item button[title="删除"]',
        pre: '.msg-panel[data-kind="short"] .msg-toggle',
      },
    ]) {
      await page.locator(`.nav-item[data-tab="${m.tab}"]`).click();
      await delay(200);
      const trg = page.locator(m.trigger).first();
      if (!(await trg.count())) {
        check(false, `[${m.id}] 找得到触发控件`);
        continue;
      }
      if (m.pre && !(await trg.isVisible())) {
        const pre = page.locator(m.pre).first();
        if ((await pre.count()) && (await pre.isVisible())) {
          await pre.click();
          await delay(200);
        }
      }
      await trg.click();
      await delay(240);
      if (!(await modalVisible(page, m.id))) {
        check(false, `[${m.id}] 打开（点遮罩测试前提）`);
        continue;
      }
      // 遮罩左上角一定是 .modal 自己（.modal-box 居中）
      await page.locator("#" + m.id).click({ position: { x: 4, y: 4 } });
      await delay(240);
      check(!(await modalVisible(page, m.id)), `点遮罩空白处关闭 ${m.id}`);
      await closeAll(page);
    }

    // ---------- 六、全程无 JS 错误 ----------
    check(consoleErrors.length === 0, "全流程没有任何 JS 报错", consoleErrors.slice(0, 3).join(" ; "));
  } finally {
    await browser.close();
  }
  return finish();
}

function finish() {
  console.log(`\n结果：${checks} 项断言，失败 ${failures.length} 项`);
  if (notes.length) for (const n of notes) console.log("  · " + n);
  if (failures.length) {
    for (const f of failures) console.log("  - " + f);
    process.exitCode = 1;
  }
}

main().catch((e) => {
  console.error("[settings-modals] 探针异常:", (e && e.stack) || e);
  process.exitCode = 2;
});
