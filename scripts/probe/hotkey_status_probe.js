"use strict";
// 快捷键页：注册结果 toast + 「检测冲突」按钮。
//
// 2026-11-19 第三轮口径（用户要求）：结果不再占行内位置（右侧常驻文字会被读成
// “这个字段的取值”，四行同时写绿更像一列勾选清单），改为用户每做一个动作就弹一条
// 会自动消失的 toast —— **成功和失败都要弹**，检测结果也走同一条通道。
// 这里的要害是：
//   · 四行里不许再残留任何结果位（一个 .sc-note 都不该有）；
//   · 录键 / 清空 / 点检测三个动作都必须有结果，且说清是哪一项；
//   · toast 必须自己消失，不能变成另一种常驻提示；
//   · 检测按钮是主题色实体按钮，不是卡头的透明文本动作；
//   · 点进录入框必须先挂起四项热键、离开立刻恢复 —— 不挂起的话，按下的组合键会被系统
//     投给热键线程并执行动作（想录 Ctrl+Alt+P，结果真的推迟了一次休息）。
//     这里只验前端的调用序列；Go 侧“挂起是否真的注销了系统热键”由
//     cmd/workerrest/hotkeys_test.go 的 TestHotkeyCaptureReleasesRegistration 验。
//
// server 模式没有热键管理器（Go 侧 a.hotkeys == nil，见 hotkeys.go），真服务只能走到
// “unsupported”那一支；其余状态是在 window.api 上包一层代理模拟的。真实注册失败
// （真的被别的程序占用）只能在真机 GUI 上验，见 docs 的验收记录。
//
// 环境变量：WR_BASE（服务地址）、WR_OUT（截图目录）。

const { chromium } = require("playwright");
const path = require("path");

const BASE = process.env.WR_BASE || "http://127.0.0.1:29741";

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
  return ok;
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// 行 id / Go 侧配置键 / 显示名。与 settings.js 的 SHORTCUT_KEYS、SHORTCUT_LABELS 同一份口径。
const ROWS = [
  ["sc-postpone", "postpone", "推迟当前休息"],
  ["sc-endEarly", "endEarly", "提前结束休息"],
  ["sc-pause", "pause", "暂停 / 恢复休息"],
  ["sc-nextBreak", "nextBreak", "立即休息"],
];

const OK_LIST = ROWS.map(([, key]) => ({ key, accel: "Ctrl+Alt+X", state: "ok" }));
// 「恢复默认」的回包（mock）：四项出厂组合键。真正的默认值只在 Go 侧
// （internal/settings/default_settings.json），探针这里验的是“前端照回包回填”，
// 不重复抄一份真理 —— 所以这几个字面量只是 mock 的输入。
const DEFAULT_LIST = [
  { key: "postpone", accel: "Ctrl+Alt+P", state: "ok" },
  { key: "endEarly", accel: "Ctrl+Alt+X", state: "ok" },
  { key: "pause", accel: "Ctrl+Alt+S", state: "ok" },
  { key: "nextBreak", accel: "Ctrl+Alt+B", state: "ok" },
];
const shot = (page, name) =>
  page
    .screenshot({ path: path.join(process.env.WR_OUT || ".", name), fullPage: false })
    .catch(() => {});

// toast 的可见性与几何。#toast 平时靠 .hidden 收起（display:none → rect 全 0）。
// tone 一起读回来：失败/冲突必须 err（红底），做成的事 ok（绿底）。
async function toastState(page) {
  return page.evaluate(() => {
    const t = document.getElementById("toast");
    const r = t.getBoundingClientRect();
    const cs = getComputedStyle(t);
    return {
      hidden: t.classList.contains("hidden"),
      text: t.textContent,
      err: t.classList.contains("err"),
      ok: t.classList.contains("ok"),
      bg: cs.backgroundColor,
      fg: cs.color,
      left: Math.round(r.left),
      right: Math.round(r.right),
      top: Math.round(r.top),
      bottom: Math.round(r.bottom),
      vw: window.innerWidth,
      vh: window.innerHeight,
    };
  });
}

// 步骤之间立刻收掉上一条 toast：否则「上一条还在屏上」会让下一条的断言看不出真假。
// toast 自己的定时器不清也无妨——下一次 toast() 会 clearTimeout 掉它。
async function hideToast(page) {
  await page.evaluate(() => {
    const t = document.getElementById("toast");
    t.classList.add("hidden");
    t.textContent = "";
  });
}

// 颜色判据读的是真实计算值（不是 class 名自证）。两套主题各自的 --*-weak 都是
// 低饱和浅/深底，所以只比通道大小 + 余量：红底 R 明显大，绿底 G 明显大。
const channels = (css) => (String(css).match(/\d+(\.\d+)?/g) || []).map(Number);
const looksRed = (css) => {
  const [r, g, b] = channels(css);
  return r - g >= 6 && r - b >= 6;
};
const looksGreen = (css) => {
  const [r, g, b] = channels(css);
  return g - r >= 6 && g - b >= 6;
};

async function setCheck(page, list) {
  await page.evaluate((l) => {
    window.__WR_MOCK__.check = l;
  }, list);
}

async function clickCheck(page) {
  await hideToast(page);
  await page.click("#sc-check");
  await sleep(320);
}

async function main() {
  // 与其它探针同一口径：走本机 Edge（playwright 自带 chromium 不保证已下载）
  const browser = await chromium.launch({ channel: "msedge", headless: true });
  const page = await browser.newPage({ viewport: { width: 1000, height: 900 } });

  // window.api 是 shim.js 在页面脚本里直接赋值的：用 setter 抓住它那一刻，
  // 换成一层只拦三个快捷键方法的代理（其余原样转发）。
  // 初始 status 从 sessionStorage 读，便于“重新加载页面”时模拟“上次就注册失败”。
  await page.addInitScript(() => {
    let seed = [];
    try {
      seed = JSON.parse(sessionStorage.getItem("__wrMockStatus") || "[]");
    } catch (e) {
      seed = [];
    }
    window.__WR_MOCK__ = {
      status: seed,
      check: null,
      onStatus: null,
      captures: [],
      // 改键结果：默认成功。用例可往 setResult 里塞 {ok:false,state:"conflict",kept:…}
      // （按候选组合键索引），或直接给一个函数。delay 用来验“失败时输入框连一瞬间都不变”。
      setResult: {},
      setCalls: [],
      // 恢复默认的结果：默认成功并回一份“出厂四键”。用例可换成失败态
      // （ok:false + key = 被占用的那一项），验“点名 + 四项都不动”。
      resetResult: null,
      resetCalls: 0,
    };
    let raw = null;
    Object.defineProperty(window, "api", {
      configurable: true,
      get() {
        return raw;
      },
      set(v) {
        raw = new Proxy(v, {
          get(target, prop) {
            if (prop === "hotkeyStatus") return () => Promise.resolve(window.__WR_MOCK__.status);
            if (prop === "checkHotkeyConflicts") return () => Promise.resolve(window.__WR_MOCK__.check);
            if (prop === "setHotkeyCapture")
              return (on) => {
                window.__WR_MOCK__.captures.push(!!on);
                return Promise.resolve();
              };
            // 改键 = 一次原子提交（Go 侧按候选配置真注册，成功才落盘）。
            // 这里只模拟它的返回：ok=true 表示已经生效且已写进配置。
            if (prop === "setShortcut")
              return (key, accel) => {
                const rec = { key: String(key), accel: String(accel) };
                window.__WR_MOCK__.setCalls.push(rec);
                const m = window.__WR_MOCK__.setResult || {};
                const r = typeof m === "function" ? m(rec) : m[rec.accel];
                const out = Object.assign(
                  {
                    ok: true,
                    state: rec.accel ? "ok" : "disabled",
                    key: rec.key,
                    accel: rec.accel,
                    kept: "",
                    list: window.__WR_MOCK__.status || [],
                  },
                  r || {}
                );
                const delay = (r && r.delay) || 0;
                return new Promise((resolve) => setTimeout(() => resolve(out), delay));
              };
            if (prop === "onHotkeyStatus")
              return (cb) => {
                window.__WR_MOCK__.onStatus = cb;
              };
            // 恢复默认：Go 侧整组原子提交，前端只照回包回填。这里模拟它的返回，
            // 顺便数一下调用次数（“取消”不该惊动 Go）。
            if (prop === "resetShortcuts")
              return () => {
                window.__WR_MOCK__.resetCalls++;
                const out = Object.assign(
                  {
                    ok: true,
                    state: "ok",
                    key: "",
                    list: window.__WR_MOCK__.status || [],
                  },
                  window.__WR_MOCK__.resetResult || {}
                );
                return Promise.resolve(out);
              };
            return Reflect.get(target, prop, target);
          },
        });
      },
    });
  });

  page.on("pageerror", (e) => check(false, "页面无未捕获异常", e.message));

  const openShortcuts = async () => {
    await page.waitForSelector("#tab-shortcuts", { state: "attached" });
    await page.click('.nav-item[data-tab="shortcuts"]');
    await page.waitForSelector("#tab-shortcuts.active", { timeout: 5000 });
    await sleep(300);
  };

  await page.goto(BASE + "/settings.html", { waitUntil: "domcontentloaded" });
  await openShortcuts();

  // ---- 1. 结构：四行、改名、行内不再有结果位 ----
  const labels = await page.$$eval("#tab-shortcuts .row.field > label", (els) =>
    els.map((e) => e.textContent.trim())
  );
  check(labels.length === 4, "四行快捷键齐全", labels.join(" / "));
  check(labels[3] === "立即休息", "第四行改名为「立即休息」", labels[3]);
  check(!labels.some((t) => t.includes("跳转到")), "页面上不再出现「跳转到下一次休息」");

  const notes = await page.$$eval("#tab-shortcuts .sc-note", (els) => els.length);
  check(notes === 0, "行内不再有任何结果位（.sc-note 一个都不剩）", notes);

  const widths = await page.$$eval("#tab-shortcuts input.shortcut", (els) =>
    els.map((e) => Math.round(e.getBoundingClientRect().width))
  );
  check(
    widths.length === 4 && widths.every((w) => w === widths[0]),
    "四行录入框等宽（回到其它字段行的 180，不再给结果文字让位）",
    widths.join(",")
  );

  // ---- 2. 卡底动作区：一条虚线把「四项配置」和「整卡动作」分开，两个按钮并排左对齐 ----
  // 2026-11-23（用户要求）：检测冲突从卡头行尾挪到虚线下方左侧，旁边加「恢复默认」；
  // 原来说明判定边界的那段小字删掉了。
  const btn = await page.evaluate(() => {
    const b = document.getElementById("sc-check");
    if (!b) return null;
    const r = document.getElementById("sc-reset");
    const cs = getComputedStyle(b);
    const probe = document.createElement("span");
    probe.style.color = "var(--accent)";
    document.body.appendChild(probe);
    const accent = getComputedStyle(probe).color;
    probe.style.color = "var(--on-accent)";
    const onAccent = getComputedStyle(probe).color;
    probe.style.color = "var(--border)";
    const borderC = getComputedStyle(probe).color;
    probe.remove();
    const foot = b.closest(".sc-foot");
    const fs = foot ? getComputedStyle(foot) : null;
    const br = b.getBoundingClientRect();
    const rr = r ? r.getBoundingClientRect() : null;
    const fr = foot ? foot.getBoundingClientRect() : null;
    const label = document.querySelector("#tab-shortcuts .row.field > label");
    const lr = label ? label.getBoundingClientRect() : null;
    const tab = document.getElementById("tab-shortcuts");
    return {
      inHead: !!b.closest(".card-head"),
      inFoot: !!foot,
      resetInFoot: !!(r && r.closest(".sc-foot")),
      cls: b.className,
      visible: !!(b.offsetWidth && b.offsetHeight),
      resetVisible: !!(r && r.offsetWidth && r.offsetHeight),
      bg: cs.backgroundColor,
      color: cs.color,
      borderColor: cs.borderTopColor,
      accent,
      onAccent,
      borderC,
      height: Math.round(br.height),
      resetHeight: rr ? Math.round(rr.height) : 0,
      left: Math.round(br.left),
      right: Math.round(br.right),
      resetLeft: rr ? Math.round(rr.left) : 0,
      resetRight: rr ? Math.round(rr.right) : 0,
      footLeft: fr ? Math.round(fr.left) : 0,
      labelLeft: lr ? Math.round(lr.left) : 0,
      footTop: fr ? Math.round(fr.top) : 0,
      // 虚线必须在两个按钮**之上**（footer 的 border-top），否则就是把动作压在了配置里
      btnTop: Math.round(br.top),
      footBorderStyle: fs ? fs.borderTopStyle : "",
      footBorderWidth: fs ? fs.borderTopWidth : "",
      footBorderColor: fs ? fs.borderTopColor : "",
      hints: tab ? tab.innerText.includes("键盘钩子") || tab.innerText.includes("无法探测") : true,
    };
  });
  check(btn && btn.visible, "「检测冲突」按钮可见");
  check(btn && btn.resetVisible, "「恢复默认」按钮可见");
  check(btn && btn.inFoot && !btn.inHead, "两个按钮都在卡底动作区（不在卡头行尾）");
  check(btn && btn.resetInFoot, "「恢复默认」与检测按钮同在一个动作区");
  check(
    btn && /\bprimary\b/.test(btn.cls) && !/\bghost\b/.test(btn.cls),
    "检测按钮仍是主题色实体样式（primary）",
    btn && btn.cls
  );
  check(
    btn && btn.bg === btn.accent && btn.borderColor === btn.accent,
    "检测按钮底色与描边 = --accent",
    btn && `${btn.bg} vs ${btn.accent}`
  );
  check(btn && btn.color === btn.onAccent, "检测按钮文字 = --on-accent（对比度由主题保证）", btn && btn.color);
  check(
    btn && btn.footBorderStyle === "dashed" && btn.footBorderWidth === "1px" && btn.footBorderColor === btn.borderC,
    "动作区上方是一条 1px dashed var(--border) 的水平虚线（与关于页软件信息卡同款）",
    btn && `${btn.footBorderStyle} ${btn.footBorderWidth} ${btn.footBorderColor} vs ${btn.borderC}`
  );
  check(btn && btn.btnTop > btn.footTop, "按钮在虚线下方", btn && `虚线 ${btn.footTop} / 按钮 ${btn.btnTop}`);
  check(
    btn && Math.abs(btn.footLeft - btn.labelLeft) <= 1 && Math.abs(btn.left - btn.labelLeft) <= 1,
    "动作区与检测按钮都贴着正文左缘（与四行标签同一条竖线）",
    btn && `虚线起 ${btn.footLeft} / 按钮 ${btn.left} / 标签 ${btn.labelLeft}`
  );
  check(
    btn && btn.resetLeft > btn.right && btn.resetLeft - btn.right <= 16,
    "「恢复默认」紧挨在检测按钮右侧（8px 间距）",
    btn && `检测右缘 ${btn.right} / 恢复默认左缘 ${btn.resetLeft}`
  );
  check(btn && btn.height === btn.resetHeight, "两个按钮等高", btn && `${btn.height} / ${btn.resetHeight}`);
  check(btn && !btn.hints, "判定边界那段小字已从页面删掉（不再出现“键盘钩子 / 无法探测”）");

  // ---- 3. 初始状态静默：一份全 ok 的注册结果不该在打开页面时弹东西 ----
  await sleep(400);
  const initial = await toastState(page);
  check(initial.hidden, "打开设置页不弹 toast（一切正常不打扰）", initial.text);

  // ---- 4. 检测：全部无冲突 → toast 报总数，并自动消失 ----
  await setCheck(page, OK_LIST);
  await clickCheck(page);
  const okToast = await toastState(page);
  check(!okToast.hidden, "检测无冲突：弹出结果 toast");
  check(
    okToast.text === "未发现冲突，已启用的 4 项均可正常使用",
    "无冲突文案报“已启用几项”",
    okToast.text
  );
  check(
    okToast.ok && !okToast.err && looksGreen(okToast.bg),
    "检测通过：toast 是绿的（不是中性的面板色）",
    `${okToast.bg} / err=${okToast.err}`
  );
  await shot(page, "hotkey_toast_ok.png");

  // 检测结果的 toast 是 5s：等它自己消失，确认不是常驻提示
  await sleep(5400);
  const gone = await toastState(page);
  check(gone.hidden, "toast 自动消失（不是另一种常驻提示）");

  // ---- 5. 检测：一项被占用 → 点名哪一项，其余正常 ----
  await setCheck(
    page,
    OK_LIST.map((s) => (s.key === "pause" ? { ...s, state: "conflict" } : s))
  );
  await clickCheck(page);
  const badToast = await toastState(page);
  check(
    badToast.text === "1 项不可用：暂停 / 恢复休息，其余 3 项正常",
    "被占用时点名动作并报其余正常数",
    badToast.text
  );
  check(
    badToast.err && !badToast.ok && looksRed(badToast.bg),
    "查到冲突：toast 是红的",
    `${badToast.bg} / ok=${badToast.ok}`
  );
  await shot(page, "hotkey_toast_conflict.png");

  // ---- 6. 检测拿不到结论（Go 返回 null）→ 不谎报“无冲突” ----
  await setCheck(page, null);
  await clickCheck(page);
  const failToast = await toastState(page);
  check(
    failToast.text === "检测失败，请稍后重试",
    "检测超时/失败：如实说失败，不冒充测得的结果",
    failToast.text
  );
  check(
    failToast.err && looksRed(failToast.bg),
    "检测失败：toast 是红的（上一轮“无冲突”的绿色不能留在上面）",
    `${failToast.bg} / err=${failToast.err}`
  );

  // ---- 7. 录入期间挂起全局热键（否则按下的组合键会直接触发动作）----
  await hideToast(page);
  await page.evaluate(() => {
    window.__WR_MOCK__.captures.length = 0;
    window.__WR_MOCK__.setCalls.length = 0;
    window.__WR_MOCK__.setResult = {};
  });
  await page.click("#sc-postpone");
  await sleep(150);
  const capFocus = await page.evaluate(() => window.__WR_MOCK__.captures.slice());
  check(
    capFocus.length === 1 && capFocus[0] === true,
    "点进录入框：立即挂起四项全局热键（否则按下的组合键被系统投给热键线程执行动作）",
    JSON.stringify(capFocus)
  );

  await page.keyboard.press("Control+Alt+K");
  await sleep(250);
  const typed = await page.evaluate(() => ({
    val: document.getElementById("sc-postpone").value,
    msg: document.getElementById("toast").textContent,
    calls: window.__WR_MOCK__.setCalls.slice(),
  }));
  check(typed.val === "Ctrl+Alt+K", "录入成功：组合键写进输入框", typed.val);
  check(
    typed.msg === "“推迟当前休息”已改为 Ctrl+Alt+K",
    "改键成功也弹一条结果（带动作名与组合键）",
    typed.msg
  );
  const typedToast = await toastState(page);
  check(
    typedToast.ok && !typedToast.err && looksGreen(typedToast.bg),
    "改键成功：toast 是绿的",
    `${typedToast.bg} / err=${typedToast.err}`
  );
  check(
    typed.calls.length === 1 &&
      typed.calls[0].key === "postpone" &&
      typed.calls[0].accel === "Ctrl+Alt+K",
    "提交的是配置键名 + 候选组合键（Go 侧按它试注册）",
    JSON.stringify(typed.calls)
  );
  const capDone = await page.evaluate(() => window.__WR_MOCK__.captures.slice());
  check(
    capDone.length >= 2 && capDone[capDone.length - 1] === false,
    "录入完成后立即恢复注册（不能让热键一直挂着）",
    JSON.stringify(capDone)
  );

  // ---- 7b. 冲突：原快捷键原地不动，输入框连一瞬间都不出现那个键 ----
  // delay=400 是有意的：如果前端还残留“先写值再回滚”的老逻辑，150ms 时就能看到
  // 输入框里是新键 —— 那正是用户说的“先存再报错”。
  await hideToast(page);
  const beforeVal = await page.$eval("#sc-postpone", (e) => e.value);
  await page.evaluate(() => {
    window.__WR_MOCK__.setCalls.length = 0;
    window.__WR_MOCK__.setResult = {
      "Ctrl+Alt+J": { ok: false, state: "conflict", kept: "Ctrl+Alt+K", delay: 400 },
    };
  });
  await page.click("#sc-postpone");
  await page.keyboard.press("Control+Alt+J");
  await sleep(150);
  const midway = await page.$eval("#sc-postpone", (e) => e.value);
  check(
    midway === beforeVal,
    "等待注册结果的过程中，输入框仍是原快捷键（没有先写进去再回滚）",
    `${beforeVal} → ${midway}`
  );
  await sleep(350);
  const blocked = await page.evaluate(() => ({
    val: document.getElementById("sc-postpone").value,
    msg: document.getElementById("toast").textContent,
    hidden: document.getElementById("toast").classList.contains("hidden"),
    calls: window.__WR_MOCK__.setCalls.slice(),
  }));
  check(
    blocked.val === beforeVal && beforeVal !== "",
    "被占用：输入框保持原快捷键",
    `${beforeVal} → ${blocked.val}`
  );
  check(
    !blocked.hidden && blocked.msg.includes("已被其他程序占用") && blocked.msg.includes(beforeVal),
    "冲突时弹提示，并说明保留的是原来那个键",
    blocked.msg
  );
  // 上一条是成功（绿），这条必须把绿色摘掉换红：同一个 #toast 节点复用。
  const blockedToast = await toastState(page);
  check(
    blockedToast.err && !blockedToast.ok && looksRed(blockedToast.bg),
    "改键失败：toast 是红的（绿底不会残留）",
    `${blockedToast.bg} / ok=${blockedToast.ok}`
  );
  check(
    blocked.calls.length === 1 && blocked.calls[0].accel === "Ctrl+Alt+J",
    "冲突判定来自 Go 侧的注册结果（前端只提交候选值）",
    JSON.stringify(blocked.calls)
  );
  await shot(page, "hotkey_preflight_block.png");

  // ---- 7c. 测不了（线程未就绪 / 超时）→ 按老行为保存，不能变成新的拦截 ----
  await hideToast(page);
  await page.evaluate(() => {
    window.__WR_MOCK__.setResult = {};
  });
  await page.click("#sc-postpone");
  await page.keyboard.press("Control+Alt+Q");
  await sleep(300);
  const passed = await page.$eval("#sc-postpone", (e) => e.value);
  check(passed === "Ctrl+Alt+Q", "Go 侧测不了时照常写值（测不了 ≠ 有冲突）", passed);

  // ---- 8. 两项配同一个键：本地校验直接拦住，不惊动系统 ----
  await hideToast(page);
  await page.evaluate(() => {
    window.__WR_MOCK__.setCalls.length = 0;
  });
  await page.click("#sc-endEarly");
  await page.keyboard.press("Control+Alt+Q"); // 与第一行刚刚设的值相同
  await sleep(250);
  const dup = await page.evaluate(() => ({
    msg: document.getElementById("toast").textContent,
    calls: window.__WR_MOCK__.setCalls.length,
    warn: document.getElementById("sc-warn").textContent,
    err: document.getElementById("toast").classList.contains("err"),
    bg: getComputedStyle(document.getElementById("toast")).backgroundColor,
  }));
  check(
    dup.msg.includes("用了同一个组合键") && dup.calls === 0,
    "两项同键：录入阶段就拦住，不提交给 Go",
    `${dup.msg} / calls=${dup.calls}`
  );
  check(dup.err && looksRed(dup.bg), "录入被本地校验拦下：toast 也是红的", dup.bg);
  // 行内 sc-warn 说的是“配置里真实存在的撞车”（手改 settings.json 那种）；
  // 录入被拒时输入框还是旧值，配置里并没有重复 —— 不该跟着亮红。
  check(dup.warn === "", "行内汇总不谎报撞车（配置里其实没有重复值）", dup.warn);

  // ---- 9. 广播：只在状态真变且有问题时说话 ----
  await hideToast(page);
  await page.evaluate((list) => {
    if (window.__WR_MOCK__.onStatus) window.__WR_MOCK__.onStatus(list);
  }, OK_LIST);
  await sleep(250);
  const quietOk = await toastState(page);
  check(quietOk.hidden, "全 ok 的广播不弹提示（任何设置项保存都会重注册）", quietOk.text);

  await page.evaluate((list) => {
    if (window.__WR_MOCK__.onStatus) window.__WR_MOCK__.onStatus(list);
  }, OK_LIST.map((s) => (s.key === "pause" ? { ...s, state: "conflict" } : s)));
  await sleep(250);
  const quietBad = await toastState(page);
  check(
    !quietBad.hidden &&
      quietBad.text.includes("暂停 / 恢复休息") &&
      quietBad.text.includes("检测冲突"),
    "别的路径改动配置导致注册失败：弹一条汇总（含复查入口）",
    quietBad.text
  );
  check(
    quietBad.err && looksRed(quietBad.bg),
    "被动发现注册失败：汇总 toast 是红的",
    quietBad.bg
  );

  // ---- 9b. 窗口失焦兜底：输入框不保证收到 blur，热键必须自己回来 ----
  await page.evaluate(() => {
    window.__WR_MOCK__.captures.length = 0;
    window.dispatchEvent(new Event("blur"));
  });
  await sleep(80);
  const capBlur = await page.evaluate(() => window.__WR_MOCK__.captures.slice());
  check(
    capBlur.length >= 1 && capBlur[capBlur.length - 1] === false,
    "窗口失焦兜底恢复热键（挂起漏恢复 = 四项快捷键永久失效）",
    JSON.stringify(capBlur)
  );

  // ---- 10. 清空键（Esc）也要有反馈 ----
  await hideToast(page);
  await page.evaluate(() => {
    window.__WR_MOCK__.setResult = {};
  });
  // 先保证这一行有值：Esc 的语义是“清空已设的键”，本来就空时不该假装清空
  await page.click("#sc-pause");
  await page.keyboard.press("Control+Alt+W");
  await sleep(250);
  await hideToast(page);
  await page.click("#sc-pause");
  await page.keyboard.press("Escape");
  await sleep(250);
  const clearMsg = await toastState(page);
  check(
    clearMsg.text === "已清空“暂停 / 恢复休息”的快捷键",
    "Esc 清空也给一条结果",
    clearMsg.text
  );
  check(
    clearMsg.ok && looksGreen(clearMsg.bg),
    "清空成功：toast 是绿的",
    clearMsg.bg
  );
  const cleared = await page.$eval("#sc-pause", (e) => e.value);
  check(cleared === "", "清空后输入框为空", cleared);
  const capEsc = await page.evaluate(() => window.__WR_MOCK__.captures.slice());
  check(
    capEsc[capEsc.length - 1] === false,
    "Esc 清空后同样恢复热键",
    JSON.stringify(capEsc)
  );

  // ---- 11. 打开设置时“上次就注册失败”的键要显形（弹一条汇总）----
  // 走真实加载路径：seed 写进 sessionStorage，reload 后 initScript 用它当初始 status。
  await page.evaluate((list) => {
    sessionStorage.setItem("__wrMockStatus", JSON.stringify(list));
  }, [{ key: "pause", accel: "Ctrl+Alt+S", state: "conflict" }]);
  await page.reload({ waitUntil: "domcontentloaded" });
  await sleep(800);
  const onLoad = await toastState(page);
  check(
    !onLoad.hidden && onLoad.text.includes("暂停 / 恢复休息") && onLoad.text.includes("检测冲突"),
    "页面加载时：上次注册失败的键弹一条汇总（含动作名与复查入口）",
    onLoad.text
  );
  check(onLoad.err && looksRed(onLoad.bg), "加载时的失败汇总也是红的", onLoad.bg);
  await page.evaluate(() => sessionStorage.removeItem("__wrMockStatus"));
  await openShortcuts();

  // ---- 12. 760 窄窗：输入框等宽、卡片与 toast 都不越界 ----
  await page.setViewportSize({ width: 760, height: 900 });
  await sleep(400);
  await setCheck(page, OK_LIST);
  await clickCheck(page);
  const narrow = await page.evaluate(() => {
    const card = document.querySelector("#tab-shortcuts .card");
    const cr = card.getBoundingClientRect();
    const ins = [...document.querySelectorAll("#tab-shortcuts input.shortcut")].map((e) =>
      Math.round(e.getBoundingClientRect().width)
    );
    const t = document.getElementById("toast").getBoundingClientRect();
    return {
      cardOverflow: card.scrollWidth - card.clientWidth,
      widths: ins,
      toastLeft: Math.round(t.left),
      toastRight: Math.round(t.right),
      vw: window.innerWidth,
    };
  });
  check(narrow.cardOverflow <= 1, "760 宽：卡片无横向溢出", narrow.cardOverflow);
  check(
    narrow.widths.every((w) => w === narrow.widths[0]) && narrow.widths[0] >= 160,
    "760 宽：四行录入框仍等宽、不被压窄",
    narrow.widths.join(",")
  );
  check(
    narrow.toastLeft >= 0 && narrow.toastRight <= narrow.vw,
    "760 宽：toast 不超出窗口",
    `${narrow.toastLeft}~${narrow.toastRight} / ${narrow.vw}`
  );
  await shot(page, "hotkey_toast_760.png");

  // ---- 13. 恢复默认：确认后按 Go 回包回填；取消 / 失败都不动配置 ----
  await page.setViewportSize({ width: 1000, height: 900 });
  await sleep(300);
  await hideToast(page);
  // 先把第一行改成自定义键，才看得出“恢复”有没有真的动它
  await page.click("#sc-postpone");
  await page.keyboard.press("Control+Alt+U");
  await sleep(300);
  const beforeReset = await page.$eval("#sc-postpone", (e) => e.value);

  // 13a. 取消：确认框只是确认，不该惊动 Go
  await page.evaluate(() => {
    window.__WR_MOCK__.resetCalls = 0;
  });
  await page.click("#sc-reset");
  await sleep(200);
  const dlg = await page.evaluate(() => ({
    open: !document.getElementById("confirm-modal").classList.contains("hidden"),
    title: document.getElementById("cf-title").textContent,
  }));
  check(dlg.open && dlg.title.includes("恢复默认"), "点「恢复默认」先弹确认框", dlg.title);
  await page.click("#cf-cancel");
  await sleep(200);
  const cancelCalls = await page.evaluate(() => window.__WR_MOCK__.resetCalls);
  check(cancelCalls === 0, "确认框取消：不提交给 Go（一次调用都没有）", cancelCalls);
  const afterCancel = await page.$eval("#sc-postpone", (e) => e.value);
  check(afterCancel === beforeReset, "确认框取消：输入框原样", `${beforeReset} → ${afterCancel}`);

  // 13b. 成功：四项按回包回填（前端不自己算默认值）
  await hideToast(page);
  await page.evaluate((list) => {
    window.__WR_MOCK__.resetResult = { ok: true, state: "ok", key: "", list };
  }, DEFAULT_LIST);
  await page.click("#sc-reset");
  await sleep(200);
  await page.click("#cf-ok");
  await sleep(300);
  const filled = await page.evaluate(() => ({
    vals: ["sc-postpone", "sc-endEarly", "sc-pause", "sc-nextBreak"].map(
      (id) => document.getElementById(id).value
    ),
    toast: document.getElementById("toast").textContent,
    ok: document.getElementById("toast").classList.contains("ok"),
    bg: getComputedStyle(document.getElementById("toast")).backgroundColor,
  }));
  check(
    filled.vals.join(",") === DEFAULT_LIST.map((s) => s.accel).join(","),
    "恢复默认后四个输入框 = Go 回包里的组合键",
    filled.vals.join(",")
  );
  check(
    filled.toast.includes("已恢复默认") && filled.ok && looksGreen(filled.bg),
    "恢复默认成功：toast 是绿的并说清做了什么",
    `${filled.toast} / ${filled.bg}`
  );

  // 13c. 失败：点名被占用的那一项，四项都不动（不能先刷成默认再报错）
  await hideToast(page);
  const now = filled.vals.slice();
  await page.evaluate((vals) => {
    window.__WR_MOCK__.resetResult = {
      ok: false,
      state: "conflict",
      key: "pause",
      list: ["postpone", "endEarly", "pause", "nextBreak"].map((key, i) => ({
        key,
        accel: vals[i],
        state: "ok",
      })),
    };
  }, now);
  await page.click("#sc-reset");
  await sleep(200);
  await page.click("#cf-ok");
  await sleep(300);
  const failed = await page.evaluate(() => ({
    vals: [...document.querySelectorAll("#tab-shortcuts input.shortcut")].map((e) => e.value),
    toast: document.getElementById("toast").textContent,
    err: document.getElementById("toast").classList.contains("err"),
    bg: getComputedStyle(document.getElementById("toast")).backgroundColor,
  }));
  check(failed.err && looksRed(failed.bg), "恢复默认失败：toast 是红的", failed.bg);
  check(
    failed.toast.includes("暂停 / 恢复休息") && failed.toast.includes("未改动"),
    "失败时点名是哪一项的默认键被占用，并说明四项都没改",
    failed.toast
  );
  check(
    failed.vals.join(",") === now.join(","),
    "失败时输入框保持原值（不刷成默认值）",
    failed.vals.join(",")
  );
  await shot(page, "hotkey_foot.png");

  await browser.close();
  console.log("\n结果：%d 项，失败 %d 项", checks, failures.length);
  if (failures.length) {
    console.log("失败项：");
    for (const f of failures) console.log("  - " + f);
  }
  process.exit(failures.length ? 1 : 0);
}

main().catch((e) => {
  console.error(e);
  process.exit(2);
});
