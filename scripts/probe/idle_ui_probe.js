"use strict";
// “系统空闲”这一行的 UI 探针：勾选态 / 分钟数 / 禁用态 / 句尾问号 / 与下面四行的文字列对齐。
//
// 为什么单独一支：settings_ui_probe 只扫“控件在不在、名字对不对”，settings_options_probe
// 只扫“操作它配置变没变”。这一行真正容易坏的是**排版**（框夹在句中、文字列错位）和
// **两态耦合**（取消勾选后分钟框还能编辑、重载后状态没回来），两样都扫不到。
//
// 只读 + 可逆：改过的控件最后都改回原值。

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
    failures.push(label + (detail === undefined ? "" : "  | " + detail));
  }
  return ok;
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function waitApi(page) {
  for (let i = 0; i < 40; i++) {
    if (await page.evaluate(() => typeof window.api === "object" && window.api !== null)) return true;
    await sleep(250);
  }
  return false;
}

async function cfg(page) {
  return await page.evaluate(() => window.api.getSettings());
}

// 等配置稳定：连续两次读取相同即认为保存已落地
async function settled(page) {
  let last = JSON.stringify(await cfg(page));
  for (let i = 0; i < 40; i++) {
    await sleep(70);
    const now = JSON.stringify(await cfg(page));
    if (now === last) return JSON.parse(now);
    last = now;
  }
  return JSON.parse(last);
}

// 取某个 label 里“文字那一段”的左缘（不含勾选框本身）—— 判断两行文字是否同一列
async function textLeft(page, sel) {
  return await page.evaluate((s) => {
    const el = document.querySelector(s);
    if (!el) return null;
    const tn = [...el.childNodes].find((n) => n.nodeType === 3 && n.textContent.trim());
    if (!tn) return null;
    const r = document.createRange();
    r.selectNodeContents(tn);
    const b = r.getBoundingClientRect();
    return { x: Math.round(b.x), y: Math.round(b.y), w: Math.round(b.width) };
  }, sel);
}

async function rect(page, sel) {
  return await page.evaluate((s) => {
    const el = document.querySelector(s);
    if (!el) return null;
    const b = el.getBoundingClientRect();
    return { x: Math.round(b.x), y: Math.round(b.y), w: Math.round(b.width), h: Math.round(b.height) };
  }, sel);
}

async function overflowOf(page, sel) {
  return await page.evaluate((s) => {
    const el = document.querySelector(s);
    return el ? el.scrollWidth - el.clientWidth : -1;
  }, sel);
}

// 改一个数字框并让它走完 collectForm + 自动保存
async function setNumber(page, sel, value) {
  await page.evaluate(
    ({ s, v }) => {
      const el = document.querySelector(s);
      el.value = v;
      el.dispatchEvent(new Event("change", { bubbles: true }));
    },
    { s: sel, v: value },
  );
  return await settled(page);
}

async function setCheckbox(page, sel, on) {
  await page.evaluate(
    ({ s, v }) => {
      const el = document.querySelector(s);
      if (el.checked !== v) el.click();
    },
    { s: sel, v: on },
  );
  return await settled(page);
}

async function load(page) {
  await page.goto(BASE + "/settings.html?win=settings", { waitUntil: "domcontentloaded" });
  if (!(await waitApi(page))) throw new Error("window.api 没就绪");
  // 等回填完成：分钟框有值即认为 fillForm 跑过
  for (let i = 0; i < 40; i++) {
    const v = await page.evaluate(() => document.getElementById("idlePauseMin").value);
    if (v !== "") return;
    await sleep(150);
  }
  throw new Error("设置页没回填完");
}

async function main() {
  const browser = await chromium.launch({ channel: "msedge", headless: true });
  const ctx = await browser.newContext({ viewport: { width: 874, height: 857 }, deviceScaleFactor: 1 });
  const page = await ctx.newPage();
  const errors = [];
  page.on("console", (m) => {
    if (m.type() === "error" && !/favicon/i.test(m.text())) errors.push(m.text().slice(0, 200));
  });
  page.on("pageerror", (e) => errors.push("pageerror: " + e.message.slice(0, 200)));

  try {
    await page.route("**/favicon.ico", (r) => r.fulfill({ status: 204, body: "" }));
    await load(page);
    const c0 = await cfg(page);

    // ---------- ① 出厂 / 回填形态 ----------
    const s0 = await page.evaluate(() => ({
      has: !!document.getElementById("idlePause"),
      checked: document.getElementById("idlePause").checked,
      disabled: document.getElementById("idlePauseMin").disabled,
      min: document.getElementById("idlePauseMin").value,
    }));
    check(s0.has, "控件 #idlePause（勾选框）存在");
    check(s0.checked === c0.idlePause, "勾选态 == 配置 idlePause", `ui=${s0.checked} cfg=${c0.idlePause}`);
    check(String(s0.min) === String(c0.idlePauseMin), "分钟数 == 配置 idlePauseMin", `ui=${s0.min} cfg=${c0.idlePauseMin}`);
    check(s0.disabled === !c0.idlePause, "分钟框可用性跟着勾选态走", `disabled=${s0.disabled}`);

    // ---------- ② 文案与问号 ----------
    const tail = await page.evaluate(() => {
      const row = document.querySelector(".row.mixed");
      const q = row.querySelector(".tail .q-mark");
      const afterLabel = document.querySelector('label:has(#idlePause) .q-mark');
      return {
        tailText: row.querySelector(".tail").textContent.trim(),
        tailTitle: q ? q.getAttribute("title") : "",
        qInTail: !!q,
        qAfterLabel: !!afterLabel,
      };
    });
    check(tail.tailText.startsWith("分钟"), "句尾单位仍是「分钟」", tail.tailText);
    check(tail.qInTail, "“?”挂在句尾（.tail 内）");
    check(!tail.qAfterLabel, "名称后没有多余的“?”（会把句子断开）");
    check(/无鼠标动作|无键盘输入/.test(tail.tailTitle), "“?”的说明写清了判据", tail.tailTitle);

    const appText = await page.evaluate(() => document.querySelector("#appPause-enabled").parentElement.textContent.trim());
    check(appText.endsWith("："), "「运行应用：」带冒号（它是下面列表的引子）", appText);

    // ---------- ③ 文字列与行高（874 宽） ----------
    const idleText = await textLeft(page, 'label:has(#idlePause)');
    const dndText = await textLeft(page, 'label:has(#dndPause)');
    const castText = await textLeft(page, 'label:has(#castMirrorPause)');
    const appTextLeft = await textLeft(page, 'label:has(#appPause-enabled)');
    check(idleText && dndText && idleText.x === dndText.x, "「系统空闲」与「勿扰/专注」文字同列",
      `idle=${idleText && idleText.x} dnd=${dndText && dndText.x}`);
    // 复制投影是同一条并排行里第 4 个选项，x 自然不同 —— 该钉的是“还在同一行”，
    // 不是“同一列”。四项挤成两行才是这一行加控件之后真会出的事故。
    check(dndText && castText && dndText.y === castText.y, "四条系统状态选项仍并排一行",
      `dnd.y=${dndText && dndText.y} cast.y=${castText && castText.y}`);
    check(idleText && appTextLeft && idleText.x === appTextLeft.x, "与「运行应用：」文字同列",
      `idle=${idleText && idleText.x} app=${appTextLeft && appTextLeft.x}`);

    const rowR = await rect(page, ".row.mixed");
    const cbR = await rect(page, "#idlePause");
    const minR = await rect(page, "#idlePauseMin");
    check(rowR && rowR.h <= 44, "整行没折行（高度 ≤ 44px）", `h=${rowR && rowR.h}`);
    check(cbR && minR && cbR.x < minR.x, "顺序是 勾选框 → 数字框", `cb=${cbR && cbR.x} min=${minR && minR.x}`);
    check((await overflowOf(page, ".row.mixed")) === 0, "874 宽无横向溢出");

    // ---------- ④ 两态耦合：取消勾选 → 分钟框禁用 → 落盘 ----------
    const c1 = await setCheckbox(page, "#idlePause", false);
    const s1 = await page.evaluate(() => ({
      disabled: document.getElementById("idlePauseMin").disabled,
      min: document.getElementById("idlePauseMin").value,
    }));
    check(c1.idlePause === false, "取消勾选写进配置（idlePause=false）", `cfg=${c1.idlePause}`);
    check(s1.disabled, "取消勾选后分钟框立即禁用");
    check(String(c1.idlePauseMin) === String(s0.min), "取消勾选不清分钟数（两个键独立）",
      `${s0.min} → ${c1.idlePauseMin}`);

    // 重载：两态都要从配置恢复，不能回到“勾着”的默认态
    await load(page);
    const s2 = await page.evaluate(() => ({
      checked: document.getElementById("idlePause").checked,
      disabled: document.getElementById("idlePauseMin").disabled,
      min: document.getElementById("idlePauseMin").value,
    }));
    check(s2.checked === false, "重载后仍是未勾选（不是回到默认开）");
    check(s2.disabled === true, "重载后分钟框仍是禁用");
    check(String(s2.min) === String(s1.min), "重载后分钟数原样", `ui=${s2.min}`);

    // ---------- ⑤ 重新勾上：分钟框恢复可编辑 ----------
    const c2 = await setCheckbox(page, "#idlePause", true);
    const s3 = await page.evaluate(() => document.getElementById("idlePauseMin").disabled);
    check(c2.idlePause === true, "重新勾选写进配置（idlePause=true）");
    check(s3 === false, "重新勾选后分钟框恢复可编辑");

    // ---------- ⑥ 分钟数边界 ----------
    const c3 = await setNumber(page, "#idlePauseMin", "0");
    check(c3.idlePauseMin === 1, "填 0 夹到下限 1", `cfg=${c3.idlePauseMin}`);
    const c4 = await setNumber(page, "#idlePauseMin", "121");
    check(c4.idlePauseMin === 120, "填 121 夹到上限 120", `cfg=${c4.idlePauseMin}`);
    const c5 = await setNumber(page, "#idlePauseMin", "45");
    check(c5.idlePauseMin === 45, "填 45 原样保存", `cfg=${c5.idlePauseMin}`);
    // 「暂停休息时」的“?”说明里带着当前分钟数：改完必须同步，不能还停在旧数字。
    const hint45 = await page.evaluate(() =>
      document.querySelector(".pause-scope-help")?.getAttribute("title") || "");
    check(/系统空闲\s*45\s*分钟/.test(hint45), "改分钟数后「暂停休息时」提示语同步成 45", hint45);
    const c6 = await setNumber(page, "#idlePauseMin", "");
    check(c6.idlePauseMin === 5, "清空回落默认 5", `cfg=${c6.idlePauseMin}`);
    const hint5 = await page.evaluate(() =>
      document.querySelector(".pause-scope-help")?.getAttribute("title") || "");
    check(/系统空闲\s*5\s*分钟/.test(hint5), "回落 5 后提示语也跟着回退", hint5);

    // 口径现状（不是缺陷判定）：取消勾选后这条提示语仍列「系统空闲 N 分钟自动暂停」，
    // 因为原设计列的是**所有可能**导致暂停的情况（勿扰 / 应用占用那条也照列，
    // 不管开关勾没勾）。这里钉住行为，是否要按开关状态改写由产品口径决定。
    await setCheckbox(page, "#idlePause", false);
    const hintOff = await page.evaluate(() =>
      document.querySelector(".pause-scope-help")?.getAttribute("title") || "");
    check(/系统空闲/.test(hintOff), "取消勾选后提示语仍列空闲（既有口径：列全部可能情况）", hintOff);
    await setCheckbox(page, "#idlePause", true);

    await load(page);
    const s4 = await page.evaluate(() => document.getElementById("idlePauseMin").value);
    check(String(s4) === "5", "重载后分钟框显示夹紧后的值（不是空/怪值）", `ui=${s4}`);

    // ---------- ⑦ 可访问名 ----------
    const a11y = await page.evaluate(() => {
      const cb = document.getElementById("idlePause");
      const num = document.getElementById("idlePauseMin");
      const lab = cb.closest("label");
      return {
        cbLabel: lab ? lab.textContent.trim() : "",
        numAria: num.getAttribute("aria-label"),
      };
    });
    check(/系统空闲/.test(a11y.cbLabel), "勾选框有可读名（label 里含「系统空闲」）", a11y.cbLabel);
    check(a11y.numAria === "系统空闲阈值（分钟）", "数字框有 aria-label（不再和勾选框抢 label）", a11y.numAria);

    // ---------- ⑧ 暗色 / 窄宽 两档几何 ----------
    await page.evaluate(() => document.documentElement.setAttribute("data-theme", "dark"));
    await sleep(150);
    const darkIdle = await textLeft(page, 'label:has(#idlePause)');
    const darkDnd = await textLeft(page, 'label:has(#dndPause)');
    check(darkIdle && darkDnd && darkIdle.x === darkDnd.x, "暗色下文字仍同列", `idle=${darkIdle && darkIdle.x}`);
    await page.evaluate(() => document.documentElement.removeAttribute("data-theme"));

    await page.setViewportSize({ width: 760, height: 857 });
    await sleep(200);
    const nIdle = await textLeft(page, 'label:has(#idlePause)');
    const nDnd = await textLeft(page, 'label:has(#dndPause)');
    const nRow = await rect(page, ".row.mixed");
    check(nIdle && nDnd && nIdle.x === nDnd.x, "760 宽（最小窗口）文字仍同列", `idle=${nIdle && nIdle.x}`);
    check(nRow && nRow.h <= 44, "760 宽不折行", `h=${nRow && nRow.h}`);
    check((await overflowOf(page, ".row.mixed")) === 0, "760 宽无横向溢出");

    // ---------- 收尾：还原成“开 + 5 分钟” ----------
    await setCheckbox(page, "#idlePause", true);
    const cEnd = await setNumber(page, "#idlePauseMin", "5");
    check(cEnd.idlePause === true && cEnd.idlePauseMin === 5, "收尾还原为 开 + 5 分钟",
      `pause=${cEnd.idlePause} min=${cEnd.idlePauseMin}`);

    check(errors.length === 0, "全程无 JS 报错", errors.slice(0, 3).join(" ; ") || "干净");
  } catch (e) {
    check(false, "探针异常：" + (e && e.message));
  } finally {
    await ctx.close();
    await browser.close();
  }

  console.log("\n结果：" + checks + " 项断言，失败 " + failures.length + " 项");
  for (const f of failures) console.log("  - " + f);
  return failures.length === 0 ? 0 : 1;
}

main().then((c) => process.exit(c));
