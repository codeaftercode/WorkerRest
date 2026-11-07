"use strict";
// 暂停提示小窗（pause.html）的渲染与交互探针。
//
// 这扇窗 300×76、每小时出现一次、只活 7.5 秒 —— 真机上几乎不可能手动盯住它，
// 而它承载的正是“正在开会 / 投屏时被暂停”这个最容易出事的场景。所以单独验：
// 文案有没有真的渲染、超长会不会撑破版式、两个按钮点得到、Esc 走的是“只关不恢复”、
// 以及页面里绝不出现“休息”二字（那是 pause_notice.go 措辞约束的运行时侧）。

const { chromium } = require("playwright");

const BASE = process.env.WR_BASE;
const OUT = process.env.WR_SHOT_DIR || ".";
const PRELOAD = process.env.WR_PRELOAD;

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

async function openPage(browser, url, width, height) {
  const ctx = await browser.newContext({ viewport: { width, height }, deviceScaleFactor: 1 });
  const page = await ctx.newPage();
  const errors = [];
  const failedReq = [];
  page.on("console", (m) => {
    if (m.type() === "error" && !/favicon/i.test(m.text())) errors.push(m.text().slice(0, 200));
  });
  page.on("pageerror", (e) => errors.push("pageerror: " + e.message.slice(0, 200)));
  page.on("requestfailed", (r) => {
    const u = r.url();
    if (!/favicon/i.test(u)) failedReq.push(u.replace(BASE, "") + ":" + (r.failure() || {}).errorText);
  });
  page.on("response", (r) => {
    if (r.status() >= 400 && !/favicon/i.test(r.url())) failedReq.push(r.url().replace(BASE, "") + ":" + r.status());
  });
  if (PRELOAD) await page.addInitScript({ path: PRELOAD });
  // 无边框小窗没有 favicon，Wails 的静态服务会给 404 —— 这是页面之外的噪声
  // （与 web/dist 里只放 icon.png 一致），拦成 204 免得混进 console 断言。
  await page.route("**/favicon.ico", (r) => r.fulfill({ status: 204, body: "" }));
  await page.goto(url, { waitUntil: "domcontentloaded" });
  return { ctx, page, errors, failedReq };
}

async function waitApi(page) {
  for (let i = 0; i < 40; i++) {
    const ok = await page.evaluate(() => typeof window.api === "object" && window.api !== null);
    if (ok) return true;
    await sleep(250);
  }
  return false;
}

// 把 api 的两个方法换成记录器：验证“按钮 → 绑定名”这条接线，不依赖运行时传输层
const PATCH_API = () => {
  window.__calls = [];
  const wrap = (name) => () => {
    window.__calls.push(name);
    return Promise.resolve(null);
  };
  if (window.api) {
    window.api.pauseNoticeResume = wrap("pauseNoticeResume");
    window.api.pauseNoticeDismiss = wrap("pauseNoticeDismiss");
  }
};

async function main() {
  const browser = await chromium.launch({ channel: "msedge", headless: true });
  let rc = 0;
  try {
    // ---- 1. 常规文案：300×76 的真实窗口尺寸 ----
    console.log("\n[1] 300×76 + 正常文案");
    const text = "已暂停 1 小时 12 分钟";
    const a = await openPage(
      browser,
      BASE + "/pause.html?win=pause&text=" + encodeURIComponent(text),
      300,
      76
    );
    if (!(await waitApi(a.page))) {
      check(false, "pause.html 装载完成且 window.api 就绪");
      return 1;
    }
    await sleep(300); // 等淡入动画（160ms）结束，避免把 opacity 中间态量成“不可见”
    check(a.errors.length === 0, "无 console error / pageerror", a.errors.join(" ; ") || "干净");
    check(a.failedReq.length === 0, "无 4xx/失败的资源请求", a.failedReq.join(" ; ") || "干净");

    const m = await a.page.evaluate(() => {
      const card = document.getElementById("card");
      const t = document.getElementById("text");
      const resume = document.getElementById("resume");
      const dismiss = document.getElementById("dismiss");
      const rc = card.getBoundingClientRect();
      const rt = t.getBoundingClientRect();
      const rr = resume.getBoundingClientRect();
      const rd = dismiss.getBoundingClientRect();
      const cs = getComputedStyle(card);
      const ts = getComputedStyle(t);
      return {
        text: t.textContent,
        cardW: Math.round(rc.width),
        cardH: Math.round(rc.height),
        opacity: Number(getComputedStyle(card).opacity),
        radius: cs.borderTopLeftRadius,
        textRight: Math.round(rt.right),
        resumeLeft: Math.round(rr.left),
        dismissLeft: Math.round(rd.left),
        textColor: ts.color,
        overflowX: document.documentElement.scrollWidth - window.innerWidth,
        bodyText: document.body.innerText,
        resumeH: Math.round(rr.height),
        dismissH: Math.round(rd.height),
        resumeLabel: resume.textContent.trim(),
        dismissLabel: dismiss.getAttribute("aria-label"),
      };
    });

    check(m.text === text, "文案由 URL 下发并渲染", m.text);
    check(m.cardW === 300 && m.cardH === 76, "卡片铺满 300×76 窗口", m.cardW + "×" + m.cardH);
    check(m.opacity > 0.9, "淡入结束、完全可见", m.opacity);
    check(m.overflowX <= 0, "没有横向溢出（无边框窗不该出现滚动条）", m.overflowX);
    check(m.textRight <= m.resumeLeft, "文案区不压到“恢复”按钮",
      "text.right=" + m.textRight + " resume.left=" + m.resumeLeft);
    check(m.resumeLeft < m.dismissLeft, "“恢复”在关闭按钮左侧", m.resumeLeft + " < " + m.dismissLeft);
    check(m.resumeH >= 28 && m.dismissH >= 22, "两个按钮都够大（点得中）",
      m.resumeH + "px / " + m.dismissH + "px");
    check(m.resumeLabel === "恢复", "“恢复”按钮文字正确", m.resumeLabel);
    check(m.dismissLabel === "关闭提示", "关闭按钮有可访问名", m.dismissLabel);
    check(!m.bodyText.includes("休息"), "渲染后的页面里不出现“休息”二字", m.bodyText.replace(/\n/g, " / "));
    // 深色玻璃 + 浅色字：亮度差要够（这扇窗会被旁人看到，必须一眼看清）
    const lum = (c) => {
      const [r, g, b] = c.match(/\d+/g).map(Number);
      return 0.2126 * r + 0.7152 * g + 0.0722 * b;
    };
    check(lum(m.textColor) > 200, "文字是浅色（深底上可读）", m.textColor);

    // ---- 2. 接线：点按钮真的调到对应绑定 ----
    console.log("\n[2] 按钮与 Esc 的接线");
    await a.page.evaluate(PATCH_API);
    await a.page.click("#resume");
    await sleep(60);
    let calls = await a.page.evaluate(() => window.__calls.slice());
    check(calls.length === 1 && calls[0] === "pauseNoticeResume", "点“恢复”调 pauseNoticeResume", calls);

    await a.page.evaluate(() => { window.__calls = []; });
    await a.page.click("#dismiss");
    await sleep(60);
    calls = await a.page.evaluate(() => window.__calls.slice());
    check(calls.length === 1 && calls[0] === "pauseNoticeDismiss", "点 × 调 pauseNoticeDismiss", calls);

    await a.page.evaluate(() => { window.__calls = []; });
    await a.page.keyboard.press("Escape");
    await sleep(60);
    calls = await a.page.evaluate(() => window.__calls.slice());
    check(calls.length === 1 && calls[0] === "pauseNoticeDismiss",
      "Esc = 只关提示、不恢复（与系统浮层一致）", calls);

    // 其它键不该**额外**触发任何东西。先移开焦点：上一步点过 ×，焦点还在按钮上，
    // 此时按 Enter / Space 就是“激活当前焦点按钮”—— 那是正确的键盘可达行为，
    // 不属于“误触”。这里要验的是“没有焦点时按这些键什么也不发生”。
    await a.page.evaluate(() => {
      window.__calls = [];
      if (document.activeElement && document.activeElement.blur) document.activeElement.blur();
    });
    await a.page.keyboard.press("Enter");
    await a.page.keyboard.press("Space");
    await sleep(60);
    calls = await a.page.evaluate(() => window.__calls.slice());
    check(calls.length === 0, "无焦点时其余按键不触发绑定（不会误恢复）", calls);

    // 反过来：按钮获得焦点时 Enter 就是激活它 —— 键盘用户必须能这么用
    await a.page.evaluate(() => {
      window.__calls = [];
      document.getElementById("resume").focus();
    });
    await a.page.keyboard.press("Enter");
    await sleep(60);
    calls = await a.page.evaluate(() => window.__calls.slice());
    check(calls.length === 1 && calls[0] === "pauseNoticeResume",
      "焦点在“恢复”上按 Enter 能激活（键盘可达）", calls);

    // ---- 3. 超长文案：省略号 + 不撑破版式 ----
    console.log("\n[3] 超长文案");
    const long = "已暂停 " + "很久很久".repeat(12);
    const b = await openPage(browser, BASE + "/pause.html?win=pause&text=" + encodeURIComponent(long), 300, 76);
    await waitApi(b.page);
    await sleep(250);
    const m2 = await b.page.evaluate(() => {
      const t = document.getElementById("text");
      const card = document.getElementById("card").getBoundingClientRect();
      const resume = document.getElementById("resume").getBoundingClientRect();
      return {
        clipped: t.scrollWidth > t.clientWidth + 1,
        cardW: Math.round(card.width),
        resumeVisible: resume.width > 0 && resume.right <= window.innerWidth + 0.5,
        overflowX: document.documentElement.scrollWidth - window.innerWidth,
        ellipsis: getComputedStyle(t).textOverflow,
      };
    });
    check(m2.clipped, "超长文案被截断（不换行）", "scrollWidth > clientWidth");
    check(m2.ellipsis === "ellipsis", "截断处是省略号而不是硬切", m2.ellipsis);
    check(m2.overflowX <= 0, "超长文案也没撑出横向滚动", m2.overflowX);
    check(m2.resumeVisible, "“恢复”按钮仍完整可见（没被文案挤出去）");
    check(m2.cardW === 300, "卡片宽度仍锁在窗口宽度", m2.cardW);

    // ---- 4. 无 text 参数：退回占位文案，不崩 ----
    console.log("\n[4] 缺 text 参数");
    const c = await openPage(browser, BASE + "/pause.html?win=pause", 300, 76);
    await waitApi(c.page);
    await sleep(200);
    const m3 = await c.page.evaluate(() => document.getElementById("text").textContent);
    check(m3.length > 0, "没有 text 参数时仍有一句占位文案（不是空白窗）", m3);
    check(c.errors.length === 0, "缺参数时无 JS 报错", c.errors.join(" ; ") || "干净");

    // ---- 5. 截图存档 ----
    await a.page.screenshot({ path: OUT + "/pause_notice.png" });
    await b.page.screenshot({ path: OUT + "/pause_notice_long.png" });
    console.log("\n截图：" + OUT + "/pause_notice*.png");

    await a.ctx.close();
    await b.ctx.close();
    await c.ctx.close();
  } catch (e) {
    console.log("  FAIL 探针异常：" + (e && e.message));
    failures.push("异常");
    rc = 1;
  } finally {
    await browser.close();
  }

  console.log("\n结果：" + checks + " 项断言，失败 " + failures.length + " 项");
  for (const f of failures) console.log("  - " + f);
  return failures.length === 0 ? rc : 1;
}

main().then((c) => process.exit(c));
