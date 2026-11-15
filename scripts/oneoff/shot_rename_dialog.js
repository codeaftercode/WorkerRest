/**
 * 一次性：截图 + 断言“重命名”命名框的图标与命名要求文案（临时工具）。
 *
 * 验三件事：
 *   1. 督休专员右键 → 重命名：图标是铅笔（内联 svg），说明文字是命名要求；
 *   2. 新建自定义提醒（同为 promptDialog，不传 icon）：图标是默认的那个“＋”，
 *      铅笔不会泄漏给下一个用这个框的流程；
 *   3. 重命名刚才新建的自定义提醒：同一枚铅笔。
 *
 * 运行：python scripts/oneoff/shot_rename_dialog.py
 */
const { chromium } = require("playwright");
const path = require("path");
const fs = require("fs");

const BASE = process.env.WR_BASE || "http://127.0.0.1:29741";
const OUT = process.env.WR_OUT || path.join(process.env.TEMP || ".", "wr_rename_dialog");
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

async function main() {
  if (!fs.existsSync(OUT)) fs.mkdirSync(OUT, { recursive: true });
  const browser = await chromium.launch({ channel: "msedge", headless: true });
  const ctx = await browser.newContext({ viewport: { width: 1000, height: 820 }, deviceScaleFactor: 2 });
  const page = await ctx.newPage();
  page.setDefaultTimeout(8000);
  try {
    await page.addInitScript({ content: fs.readFileSync(PRELOAD, "utf-8") });
    await page.goto(BASE + "/settings.html", { waitUntil: "domcontentloaded" });
    for (let i = 0; i < 40; i++) {
      if (await page.evaluate(() => typeof window.api === "object" && window.api !== null)) break;
      await delay(150);
    }
    await delay(700);

    const modalState = () => page.evaluate(() => {
      const box = document.getElementById("prompt-modal");
      const icon = document.getElementById("pm-icon");
      const desc = document.getElementById("pm-desc");
      const first = icon && icon.firstElementChild;
      return {
        open: !!box && !box.classList.contains("hidden"),
        title: document.getElementById("pm-title").textContent.trim(),
        iconHTML: icon ? icon.innerHTML.trim() : "",
        iconTag: first ? first.tagName.toLowerCase() : "",
        pathCount: icon ? icon.querySelectorAll("svg path").length : 0,
        desc: desc ? desc.textContent.trim() : "",
        descBox: desc ? Math.round(desc.getBoundingClientRect().height) : 0,
      };
    });
    const fillPrompt = (v) => page.evaluate((val) => {
      const inp = document.getElementById("pm-input");
      inp.value = val;
      inp.dispatchEvent(new Event("input", { bubbles: true }));
    }, v);
    const closeModal = async () => {
      await page.evaluate(() => document.getElementById("pm-cancel")?.click());
      await delay(250);
    };

    // ---------- 一、督休专员重命名 ----------
    console.log("\n[一] 立绘库右键 → 重命名");
    await page.locator('.nav-item[data-tab="supervisor"]').click();
    await delay(600);
    const tileName = await page.evaluate(() => {
      const tile = document.querySelector("#sup-gallery .role-item");
      return tile ? tile.dataset.name : "";
    });
    check(!!tileName, "立绘库有磁贴可右键", tileName);
    if (tileName) {
      await page.evaluate((name) => {
        const tile = [...document.querySelectorAll("#sup-gallery .role-item")]
          .find((el) => el.dataset.name === name);
        const r = tile.getBoundingClientRect();
        tile.dispatchEvent(new MouseEvent("contextmenu", {
          bubbles: true, cancelable: true, clientX: r.left + 20, clientY: r.top + 20
        }));
      }, tileName);
      await delay(300);
      await page.evaluate(() => {
        document.querySelector('#sup-role-menu .role-menu-item[data-act="rename"]')?.click();
      });
      await delay(500);

      const st = await modalState();
      check(st.open, "重命名框打开", st.title);
      check(st.title === "重命名", "标题是“重命名”", st.title);
      check(st.iconTag === "svg", "图标换成了内联 svg（铅笔），不再是文本“＋”",
        st.iconTag || `（文本：${st.iconHTML}）`);
      check(st.pathCount === 2, "铅笔有两条 path（笔身 + 下划线）", String(st.pathCount));
      check(/名称要求/.test(st.desc) && /30 个字符/.test(st.desc) && /不能以点开头/.test(st.desc),
        "说明文字是命名要求（含“30 个字符 / 不能以点开头”）", st.desc);
      check(/不能包含/.test(st.desc) && /< >/.test(st.desc),
        "列出不能输入的 9 个字符", st.desc);
      check(/重名/.test(st.desc), "写明不能重名", st.desc);
      check(!/显示在立绘库磁贴/.test(st.desc), "旧的“名称会显示在…”说明已去掉", st.desc);
      check(st.desc.split("\n").length === 3, "命名要求分三行（一条一行）",
        JSON.stringify(st.desc.split("\n")));
      check(st.descBox > 0 && st.descBox <= 90, "说明文字排版正常（三行内）", `${st.descBox}px`);
      await page.screenshot({ path: path.join(OUT, "rename_role.png") });
      // 单独截这枚图标（DPR=2 → 68px），用来判断它在 34px 圆里的分量是否协调
      await page.locator("#pm-icon").screenshot({ path: path.join(OUT, "icon.png") });
      await closeModal();
    }

    // ---------- 二、新建自定义提醒：默认图标不泄漏 ----------
    console.log("\n[二] 新建自定义提醒（不传 icon）");
    await page.locator('.nav-item[data-tab="timing"]').click();
    await delay(500);
    await page.evaluate(() => document.getElementById("cb-add")?.click());
    await delay(400);
    let st = await modalState();
    check(st.open, "新建自定义提醒框打开", st.title);
    check(st.iconTag === "" && st.iconHTML === "＋",
      "图标是默认的文本“＋”（铅笔没泄漏）", st.iconTag || st.iconHTML);
    await page.screenshot({ path: path.join(OUT, "add_custom.png") });
    await fillPrompt("探针提醒");
    await page.evaluate(() => document.getElementById("pm-ok")?.click());
    await delay(900);
    const cardOK = await page.evaluate(() =>
      !![...document.querySelectorAll(".card-custom")].find((c) => c.querySelector("h3")?.textContent === "探针提醒"));
    check(cardOK, "自定义提醒卡真的建出来了", "探针提醒");

    // ---------- 三、重命名自定义提醒：铅笔 ----------
    console.log("\n[三] 自定义提醒卡 → 重命名");
    if (cardOK) {
      await page.evaluate(() => document.querySelector(".card-custom .cb-rename")?.click());
      await delay(450);
      st = await modalState();
      check(st.open && st.title === "重命名自定义提醒", "自定义提醒重命名框打开", st.title);
      check(st.iconTag === "svg" && st.pathCount === 2, "同一枚铅笔图标",
        `${st.iconTag}/${st.pathCount}`);
      await page.screenshot({ path: path.join(OUT, "rename_custom.png") });
      await closeModal();
    }

    // ---------- 四、导入立绘 → 命名：与重命名框同一段命名要求 ----------
    console.log("\n[四] 新增立绘 → 命名（同一段 ROLE_NAME_RULES）");
    const stubbed = await page.evaluate(() => {
      if (!window.api || typeof window.api.pickSupervisorImage !== "function") return false;
      window.api.pickSupervisorImage = () => Promise.resolve({ name: "探针立绘", saved: "探针立绘" });
      return true;
    });
    if (stubbed) {
      await page.locator('.nav-item[data-tab="supervisor"]').click();
      await delay(400);
      await page.evaluate(() => document.getElementById("sup-gallery-add")?.click());
      await delay(500);
      st = await modalState();
      check(st.open && st.title === "命名", "命名框打开", st.title);
      check(st.iconTag === "" && st.iconHTML === "＋", "导入立绘仍用“＋”（新增语义）",
        st.iconTag || st.iconHTML);
      check(st.desc.split("\n").length === 3 && /30 个字符/.test(st.desc) &&
        /不能以点开头/.test(st.desc) && /不能包含/.test(st.desc),
        "导入命名框也用同一段命名要求", JSON.stringify(st.desc.split("\n")));
      await page.screenshot({ path: path.join(OUT, "naming_import.png") });
      await closeModal();
    } else {
      check(false, "预载里能替换 pickSupervisorImage 以打开命名框");
    }
  } finally {
    await browser.close();
  }
  console.log(`\n结果：${checks} 项，失败 ${failures.length}`);
  if (failures.length) {
    for (const f of failures) console.log("  FAIL " + f);
    process.exit(1);
  }
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
