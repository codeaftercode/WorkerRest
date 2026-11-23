/**
 * 提前提示页（prebreak.html）的页面层验证 —— 纯前端，不碰 Go、不碰配置。
 *
 * 为什么单独一层：这扇窗有两种版式（内置 400×240 / 自定义 400×172）、四个动作按钮、
 * 一套按底色算出来的配色，还有“暂停中仍留在屏幕上”这一种形态。真机探针只能看见
 * “窗弹出来了、点了有反应”，看不见“暂停时那个按钮该不该在”、“长名字会不会把按钮
 * 挤出窗口”。这些都在这里钉。
 *
 * 本轮的出发点是一条组合缺陷（2026-11-23 全量审计发现）：
 *   暂停中留下的提前提示窗上，「暂停计时」按钮照旧露着，而它走的是 pauseToggle ——
 *   已经在暂停里再按一下就是**恢复**，还会顺手把勿扰 / 投屏那些自动原因记成“用户已忽略”。
 *   按钮写着暂停、按下去是恢复，用户完全没法预期。修复是暂停态把它收起来，这里钉住。
 *
 * 运行：node scripts/probe/prebreak_page_probe.js
 * 退出码 0 = 全部通过。截图落在 %TEMP%/wr_prebreak_page。
 */
const { chromium } = require("playwright");
const path = require("path");
const fs = require("fs");
const { pathToFileURL } = require("url");

const HERE = __dirname;
const ROOT = path.resolve(HERE, "..", "..");
const PAGE = path.join(ROOT, "cmd", "workerrest", "web", "dist", "prebreak.html");
const OUT = process.env.WR_OUT || path.join(process.env.TEMP || ".", "wr_prebreak_page");

// 与 Go 侧 preBreakWidth / preBreakHeightDef / preBreakHeightCus 同值
const VIEW_DEF = { width: 400, height: 240 };
const VIEW_CUS = { width: 400, height: 172 };

let checks = 0;
const failures = [];
function check(ok, label, detail) {
  checks++;
  const tail = detail === undefined ? "" : "  | " + JSON.stringify(detail);
  if (ok) console.log("  ok   " + label + tail);
  else {
    console.log("  FAIL " + label + tail);
    failures.push(label);
  }
}

// 打开一页：先按真实地址加载（首帧的 URL 参数会先生效），再把 preBreakState 换成桩，
// 等两拍轮询（500ms 一次）让它按 state 重画。
async function open(browser, { query = "", vp = VIEW_DEF, state }) {
  const page = await browser.newPage({ viewport: vp });
  const errs = [];
  page.on("pageerror", (e) => errs.push(String(e)));
  page.on("console", (m) => {
    if (m.type() === "error") errs.push("console: " + m.text());
  });
  await page.goto(pathToFileURL(PAGE).href + query, { waitUntil: "load" });
  await page.evaluate((st) => {
    window.__acts = [];
    // prebreak.js 在加载时就 `var api = window.api` 抓住了这个对象 —— 改它的方法有效，
    // 换整个 window.api 无效（换掉的是另一个对象）。
    window.api.preBreakState = () => Promise.resolve(st);
    window.api.preBreakAction = (a) => {
      window.__acts.push(a);
      return Promise.resolve(null);
    };
  }, state);
  await page.waitForTimeout(1100);
  return { page, errs };
}

const vis = (page, sel) =>
  page.evaluate((s) => {
    const el = document.querySelector(s);
    if (!el) return null;
    const cs = getComputedStyle(el);
    return cs.display !== "none" && cs.visibility !== "hidden";
  }, sel);

const txt = (page, sel) => page.evaluate((s) => document.querySelector(s)?.textContent ?? null, sel);

// file:// 下没有 Wails 运行时：shim.js 首次 call() 会 import('/wails/runtime.js') 失败并
// 打一条 console.error。这是**夹具的已知噪音**，不是页面缺陷 —— 判据放在“页面自己有没有
// 抛错”上，所以把这一条滤掉（连同它的堆栈一起，堆栈里没有别的信息）。
const SHIM_NOISE = "加载 /wails/runtime.js 失败";
const realErrs = (errs) => errs.filter((e) => !e.includes(SHIM_NOISE));

// 版式是否装得下：所有可见子节点都必须落在 #app 的盒子内（下沿不许出头）
const fit = (page) =>
  page.evaluate(() => {
    const app = document.getElementById("app");
    const ar = app.getBoundingClientRect();
    const over = [];
    app.querySelectorAll("*").forEach((el) => {
      if (getComputedStyle(el).display === "none") return;
      if (!el.getClientRects().length) return;
      const r = el.getBoundingClientRect();
      if (r.height === 0) return;
      if (r.bottom > ar.bottom + 0.5 || r.right > ar.right + 0.5 || r.left < ar.left - 0.5) {
        over.push({
          tag: el.tagName.toLowerCase() + (el.id ? "#" + el.id : ""),
          bottom: Math.round((r.bottom - ar.bottom) * 10) / 10,
          right: Math.round((r.right - ar.right) * 10) / 10,
        });
      }
    });
    return {
      over,
      scrollH: app.scrollHeight,
      clientH: app.clientHeight,
      bodyScrollW: document.body.scrollWidth,
      vw: window.innerWidth,
      vh: window.innerHeight,
    };
  });

const BASE = (over) => ({
  paused: false,
  title: "您已连续工作较长时间，建议您短休息",
  remainMs: 9800,
  custom: false,
  countdownTail: " 秒进入休息",
  allowPostpone: true,
  bgColor: "#4f7cff",
  ...over,
});

(async () => {
  fs.mkdirSync(OUT, { recursive: true });
  const browser = await chromium.launch({ channel: "msedge", headless: true });

  // ---- A. 内置版式·正常倒计时 ----
  console.log("\n[A] 内置版式（400×240）· 正常倒计时");
  {
    const { page, errs } = await open(browser, {
      query: "?custom=0&tail=" + encodeURIComponent(" 秒进入休息") + "&bg=" + encodeURIComponent("#4f7cff"),
      state: BASE({}),
    });
    check((await txt(page, "#pb-title")).includes("短休息"), "标题取主进程下发的 title", await txt(page, "#pb-title"));
    check((await txt(page, "#pb-seconds")) === "10", "剩余 9800ms 上取整显示 10 秒", await txt(page, "#pb-seconds"));
    check((await txt(page, "#pb-post")) === " 秒进入休息", "后缀取自 URL 的 tail（首帧就位）", await txt(page, "#pb-post"));
    for (const [sel, label] of [
      ["#pb-now", "立即休息"],
      ["#pb-postpone", "推迟休息"],
      ["#pb-skip", "跳过休息"],
      ["#pb-pause", "暂停计时"],
    ]) {
      check(await vis(page, sel), `内置版式显示「${label}」`, sel);
    }
    check(await vis(page, "#pb-tips"), "内置版式显示两行说明（红/绿）");
    check(
      (await page.evaluate(() => document.documentElement.dataset.pbBg)) === "#4f7cff",
      "底色按 URL 的 bg 落实（首帧不闪默认蓝）",
      await page.evaluate(() => document.documentElement.dataset.pbBg),
    );

    // 点“立即休息 / 推迟 / 跳过 / 暂停”各自回传对应动作
    for (const [sel, want] of [
      ["#pb-now", "breakNow"],
      ["#pb-postpone", "postpone"],
      ["#pb-skip", "skip"],
      ["#pb-pause", "pause"],
    ]) {
      await page.click(sel);
      const got = await page.evaluate(() => window.__acts.slice());
      check(got[got.length - 1] === want, `点「${want}」回传 ${want}`, got);
      await page.evaluate(() => (window.__acts = []));
    }

    const f = await fit(page);
    check(f.over.length === 0, "内置版式没有元素溢出窗口", f.over.slice(0, 3));
    check(!(f.bodyScrollW > f.vw), "内置版式没有横向滚动", f);
    check(realErrs(errs).length === 0, "内置版式无脚本错误", realErrs(errs));
    await page.screenshot({ path: path.join(OUT, "a_builtin.png") });
    await page.close();
  }

  // ---- B. 自定义版式（172 高）：没有两行说明、没有“暂停计时”，按钮改“…提醒” ----
  console.log("\n[B] 自定义版式（400×172）");
  {
    const { page, errs } = await open(browser, {
      query: "?custom=1&tail=" + encodeURIComponent(" 秒显示喝水提醒"),
      vp: VIEW_CUS,
      state: BASE({ custom: true, title: "喝水时间到", countdownTail: " 秒显示喝水提醒" }),
    });
    check(!(await vis(page, "#pb-tips")), "自定义版式隐藏两行说明");
    check(!(await vis(page, "#pb-pause")), "自定义版式没有「暂停计时」（暂停全局开关另有入口）");
    check((await txt(page, "#pb-now")) === "立即提醒", "按钮文案改成“立即提醒”", await txt(page, "#pb-now"));
    check((await txt(page, "#pb-postpone")) === "推迟提醒", "按钮文案改成“推迟提醒”");
    check((await txt(page, "#pb-skip")) === "跳过提醒", "按钮文案改成“跳过提醒”");
    check((await txt(page, "#pb-title")) === "喝水时间到", "标题是“XX时间到”", await txt(page, "#pb-title"));
    check((await txt(page, "#pb-post")).includes("显示喝水提醒"), "倒计时后缀带提醒名", await txt(page, "#pb-post"));
    const f = await fit(page);
    check(f.over.length === 0, "自定义版式没有元素溢出窗口", f.over.slice(0, 3));
    check(realErrs(errs).length === 0, "自定义版式无脚本错误", realErrs(errs));
    await page.screenshot({ path: path.join(OUT, "b_custom.png") });
    await page.close();
  }

  // ---- C. 暂停态：标题“已暂停”、秒数“--”、没有“暂停计时” ----
  console.log("\n[C] 暂停态（窗被留着显示）");
  {
    const { page, errs } = await open(browser, {
      query: "?custom=0&tail=" + encodeURIComponent(" 秒进入休息"),
      state: BASE({ paused: true }),
    });
    check((await txt(page, "#pb-title")) === "已暂停", "暂停时标题写“已暂停”", await txt(page, "#pb-title"));
    check(!(await vis(page, "#pb-countdown")), "暂停时整行倒计时收起（不留“还有 --”这种半截话）");
    check((await txt(page, "#pb-seconds")) === "--", "暂停时秒数写“--”（即便被藏起来也不跳数字）", await txt(page, "#pb-seconds"));
    check((await txt(page, "#pb-post")) === "", "暂停时清空倒计时后缀");
    check(!(await vis(page, "#pb-pause")), "暂停时收起「暂停计时」（它走 toggle，按下去会变成恢复）");
    check(await vis(page, "#pb-now"), "暂停时仍可「立即休息」（窗留着就是让人选的）");
    check(await vis(page, "#pb-skip"), "暂停时仍可「跳过」");
    check(await vis(page, "#pb-postpone"), "暂停时仍可「推迟」");
    check(await vis(page, "#pb-tips"), "暂停时保留两行说明（不清版式）");
    check(
      (await page.evaluate(() => document.documentElement.dataset.pbBg)) === "#4f7cff",
      "底色与暂停态无关，仍是用户配的颜色",
      await page.evaluate(() => document.documentElement.dataset.pbBg),
    );
    const f = await fit(page);
    check(f.over.length === 0, "暂停态没有元素溢出窗口", f.over.slice(0, 3));
    check(realErrs(errs).length === 0, "暂停态无脚本错误", realErrs(errs));
    await page.screenshot({ path: path.join(OUT, "c_paused.png") });
    await page.close();
  }

  // ---- D. 暂停 → 恢复：按钮要自己回来（别把暂停态的隐藏留成永久） ----
  console.log("\n[D] 暂停后恢复（状态在同一页里翻转）");
  {
    const { page, errs } = await open(browser, {
      query: "?custom=0&tail=" + encodeURIComponent(" 秒进入休息"),
      state: BASE({ paused: true }),
    });
    check(!(await vis(page, "#pb-pause")), "前置：暂停态没有「暂停计时」");
    await page.evaluate(() => {
      const st = { paused: false, title: "您已连续工作较长时间，建议您短休息", remainMs: 7000,
        custom: false, countdownTail: " 秒进入休息", allowPostpone: true, bgColor: "#4f7cff" };
      window.api.preBreakState = () => Promise.resolve(st);
    });
    await page.waitForTimeout(1100);
    check(await vis(page, "#pb-pause"), "恢复后「暂停计时」回来（隐藏不是一次性的）");
    check(await vis(page, "#pb-countdown"), "恢复后倒计时行回来");
    check((await txt(page, "#pb-seconds")) === "7", "恢复后秒数重新跳动", await txt(page, "#pb-seconds"));
    check(realErrs(errs).length === 0, "翻转过程无脚本错误", realErrs(errs));
    await page.close();
  }

  // ---- E. 不允许推迟：藏掉推迟按钮 ----
  console.log("\n[E] 未勾「允许推迟提醒」");
  {
    const { page } = await open(browser, {
      query: "?custom=0&nopp=1",
      state: BASE({ allowPostpone: false }),
    });
    check(!(await vis(page, "#pb-postpone")), "没勾允许推迟时藏掉「推迟」");
    check(await vis(page, "#pb-skip"), "「跳过」仍在（总得有个出口）");
    await page.close();
  }

  // ---- F. 名字取到上限（30 字）时不把按钮挤出窗口 ----
  console.log("\n[F] 名字上限 30 字（自定义版式 172 高，最挤的一种）");
  {
    const name = "一二三四五六七八九十一二三四五六七八九十一二三四五六七八九十";
    const title = name + "时间到";
    const { page, errs } = await open(browser, {
      query: "?custom=1&tail=" + encodeURIComponent(" 秒显示" + name + "提醒"),
      vp: VIEW_CUS,
      state: BASE({ custom: true, title, countdownTail: " 秒显示" + name + "提醒" }),
    });
    const f = await fit(page);
    check(f.over.length === 0, "30 字名字不把任何元素挤出 400×172", f.over.slice(0, 3));
    const btn = await page.evaluate(() => {
      const r = document.querySelector(".actions").getBoundingClientRect();
      return { bottom: Math.round(r.bottom), vh: window.innerHeight, h: Math.round(r.height) };
    });
    check(btn.bottom <= btn.vh, "三个按钮整体仍在视口内", btn);
    check(realErrs(errs).length === 0, "长名字无脚本错误", realErrs(errs));
    await page.screenshot({ path: path.join(OUT, "f_longname.png") });
    await page.close();
  }

  // ---- G. 极端底色：浅底 / 深底都要有对比度够的字 ----
  console.log("\n[G] 底色深浅两档");
  {
    for (const [bg, want] of [
      ["#f5f5f5", "light"],
      ["#101018", "dark"],
    ]) {
      const { page } = await open(browser, { query: "?bg=" + encodeURIComponent(bg), state: BASE({ bgColor: bg }) });
      const got = await page.evaluate(() => document.documentElement.dataset.pbLum);
      check(got === want, `底色 ${bg} 判为${want === "light" ? "浅" : "深"}底`, got);
      const textColor = await page.evaluate(() => getComputedStyle(document.querySelector(".title")).color);
      const [r, g, b] = textColor.match(/\d+/g).map(Number);
      const lum = (0.2126 * r + 0.7152 * g + 0.0722 * b) / 255;
      check(
        want === "light" ? lum < 0.4 : lum > 0.6,
        `底色 ${bg} 的文字色与之相反（不会糊成一片）`,
        { textColor, lum: Math.round(lum * 100) / 100 },
      );
      await page.close();
    }
  }

  await browser.close();
  console.log(`\n[prebreak] ${checks - failures.length}/${checks} 通过；截图：${OUT}`);
  if (failures.length) {
    console.log("[prebreak] 失败项：");
    for (const f of failures) console.log("  - " + f);
    process.exit(1);
  }
})().catch((e) => {
  console.error(e);
  process.exit(1);
});
