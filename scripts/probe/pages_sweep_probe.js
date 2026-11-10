"use strict";
// 七页面全谱健康扫描：一次性把 web/dist 里全部 7 个页面都真渲染一遍。
//
// 与既有探针的分工（这一支补的是“资产网络层 + 全局不变量”）：
//   · probe_static_audit.py  —— 静态读源码：文件在不在、脚本集合对不对、有没有内联脚本；
//   · renderer_probe.js      —— 各页面的内容深度：文案、排版、立绘摆位、外卖出图；
//   · pause_page_probe.js    —— 暂停小窗的交互与可访问性；
//   · 本文件 —— 把 7 个页面放进同一张表，逐页断言同一组不变量：
//      ① 页面自身 200；② 零 console error / 零 pageerror；
//      ③ 零失败请求（任何子资源 404、请求被中止都要冒出来）；
//      ④ html[lang] 与 <title> 齐全；⑤ 无横向溢出；
//      ⑥ 所有 <img> 真的解码成功（naturalWidth>0）；
//      ⑦ 样式表真的生效（有 <link> 的页面要求 styleSheets 非空且 body 拿到了非默认字体）。
//
// 为什么值得单独做：静态审计只能看见“源码里写了哪些引用”，看不见
// “运行到某个时机才去取 seal-ink-7.svg / sitting_risk.webp”这类动态引用；
// 而内容探针用的是 mock shim，网络层错误会被它自己的断言淹掉。
// 7 个页面共用一次服务启动，成本很低（秒级）。

const { chromium } = require("playwright");

const BASE = process.env.WR_BASE;
const OUT = process.env.WR_OUT || "";

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

// 七个页面 + 各自的“壳”契约。
// 注意：break / takeout 在没有真实会话时只会画出空壳，这里不要求内容——
// 内容由 renderer_probe 用 mock shim 单独验，本支只看“壳在不在、有没有报错”。
const PAGES = [
  {
    name: "index 面板",
    path: "/index.html?win=index",
    w: 1000,
    h: 760,
    ids: ["engine", "env", "short", "long", "rolePick", "preview", "quit", "log", "roles"],
    vis: ["engine", "env", "short", "long", "rolePick", "preview", "quit", "log"],
    styles: true,
    // 面板首屏就拉环境信息与专员列表，这两处是真实绑定 → 空列表说明绑定断了
    extra: (d) => [
      [d.envDd > 0, "环境信息已渲染（真实绑定返回）", d.envDd + " 项"],
      [d.roles > 0, "督休专员磁贴已渲染", d.roles],
    ],
  },
  {
    name: "settings 设置",
    path: "/settings.html?win=settings",
    w: 1010,
    h: 900,
    ids: [
      "titlebar", "appName", "idlePauseMin",
      "tab-general", "tab-timing", "tab-supervisor", "tab-stats", "tab-shortcuts", "tab-about",
    ],
    // 只有当前 tab 是可见的，其余 tab 容器按设计隐藏 —— 所以它们只要求“存在”
    vis: ["titlebar", "appName", "idlePauseMin", "tab-general"],
    styles: true,
    // 六个导航项 + 六个 tab 容器是设置页的骨架，缺一个就是“点了没反应”的那类 bug
    extra: (d) => [
      [d.navs === 6, "六个导航项齐全", d.navs],
      [d.tabs === 6, "六个 tab 容器齐全", d.tabs],
    ],
  },
  {
    name: "break 休息窗",
    path: "/break.html?win=break&sup=0",
    w: 1100,
    h: 720,
    ids: ["app", "vignette", "skip-link", "dialog-name", "figure", "figure-img", "content", "messages", "bar"],
    // 没有真实会话时只有空壳：立绘 / 台词 / 进度条按设计隐藏
    vis: ["app", "content"],
    styles: true,
    transparent: true,
  },
  {
    name: "prebreak 预备窗",
    path: "/prebreak.html?win=prebreak&tail=" + encodeURIComponent(" 秒进入休息"),
    w: 400,
    h: 240,
    ids: ["app", "pb-title", "pb-countdown", "pb-seconds", "pb-post", "pb-tips", "pb-now", "pb-postpone", "pb-skip", "pb-pause"],
    vis: ["app", "pb-title", "pb-countdown", "pb-seconds", "pb-now", "pb-postpone", "pb-skip"],
    styles: true,
    // 倒计时是这一页的唯一职能：数字必须在跳
    extra: (d) => [[d.seconds > 0, "倒计时秒数有值", d.seconds]],
  },
  {
    name: "pause 暂停小窗",
    path: "/pause.html?win=pause&text=" + encodeURIComponent("已暂停 1 小时 30 分"),
    w: 300,
    h: 76,
    ids: ["card", "text", "resume", "dismiss"],
    vis: ["card", "text", "resume", "dismiss"],
    styles: true,
    transparent: true,
  },
  {
    name: "takeout 外卖窗",
    path: "/takeout.html?win=takeout&sup=0",
    w: 880,
    h: 560,
    ids: [
      "app", "vignette", "figure", "figure-img", "tk-badges", "tk-msg",
      "redpacket", "rp-qr", "tk-ordered", "tk-postpone", "tk-skip", "dialog-name", "supervisor-line",
    ],
    vis: ["app"],
    styles: true,
    transparent: true,
  },
  {
    name: "sound 音频宿主",
    path: "/sound.html?win=sound",
    w: 200,
    h: 120,
    ids: [],
    vis: [],
    styles: false, // 这一页刻意只有一行 body，没有样式表
  },
];

// 页面全量度量的采集（一次 evaluate 拿全，避免反复往返）
async function measure(page) {
  return await page.evaluate(() => {
    const de = document.documentElement;
    const imgs = [...document.images].map((i) => ({
      src: (i.currentSrc || i.src || "").split("/").pop(),
      ok: i.complete && i.naturalWidth > 0,
      // 还没赋 src 的 <img> 是合法占位，不算失败
      pending: !i.getAttribute("src"),
    }));
    const navs = document.querySelectorAll(".nav-item").length;
    const tabs = document.querySelectorAll(".tab").length;
    const secondsEl = document.getElementById("pb-seconds");
    return {
      lang: de.getAttribute("lang") || "",
      title: (document.title || "").trim(),
      overflowX: de.scrollWidth - de.clientWidth,
      imgs,
      sheets: document.styleSheets.length,
      font: getComputedStyle(document.body).fontFamily || "",
      navs,
      tabs,
      seconds: secondsEl ? Number((secondsEl.textContent || "").replace(/[^0-9]/g, "")) : 0,
      envDd: document.querySelectorAll("#env dd").length,
      roles: document.getElementById("roles") ? document.querySelectorAll("#roles > *").length : null,
      visibleIds: Object.fromEntries(
        [...document.querySelectorAll("[id]")].map((el) => {
          const r = el.getBoundingClientRect();
          return [el.id, r.width > 0 && r.height > 0];
        }),
      ),
    };
  });
}

async function sweep(browser, spec) {
  console.log("\n[" + spec.name + "] " + spec.path);
  const ctx = await browser.newContext({
    viewport: { width: spec.w, height: spec.h },
    deviceScaleFactor: 1,
  });
  const page = await ctx.newPage();
  const errors = [];
  const bad = []; // 失败请求：404 或网络层失败
  const okAssets = new Set();

  page.on("console", (m) => {
    if (m.type() === "error") errors.push(m.text().slice(0, 180));
  });
  page.on("pageerror", (e) => errors.push("pageerror: " + String(e && e.message).slice(0, 180)));
  page.on("response", (r) => {
    const u = r.url();
    if (u.startsWith("data:") || u.startsWith("blob:")) return;
    const p = u.replace(BASE, "");
    if (r.status() >= 400) bad.push({ p, why: "HTTP " + r.status() });
    else okAssets.add(p);
  });
  page.on("requestfailed", (r) => {
    const u = r.url();
    if (u.startsWith("data:") || u.startsWith("blob:")) return;
    bad.push({ p: u.replace(BASE, ""), why: (r.failure() || {}).errorText || "failed" });
  });
  let status = 0;
  try {
    await page.route("**/favicon.ico", (r) => r.fulfill({ status: 204, body: "" }));
    const resp = await page.goto(BASE + spec.path, { waitUntil: "domcontentloaded", timeout: 20000 });
    status = resp ? resp.status() : 0;
    await sleep(1400); // 让首屏绑定往返与计时器都跑起来
    const d = await measure(page);

    check(status === 200, spec.name + " · 页面 200", status);
    check(errors.length === 0, spec.name + " · 零 JS 报错", errors.slice(0, 2).join(" ; ") || "干净");

    // 失败请求分两类：
    //  · 同一个 URL 后来又成功取到了 —— Wails 运行时自己的探路请求（它先 fetch 一下
    //    /wails/custom.js 判断在不在，那次 fetch 会被它自己 abort，紧接着用 <script>
    //    正式加载，200 通过）。资源确实拿到了，不是我们的资产出了问题；
    //  · 从头到尾没拿到 —— 这才是真失败（404 / 连接中断）。
    // 判据是“这个 URL 有没有成功过”，不是按路径白名单放行，所以仍然能抓住
    // “custom.js 真没了”这种情况：那时 okAssets 里不会有它。
    const gotOk = new Set([...okAssets]);
    const realBad = bad.filter((b) => !gotOk.has(b.p));
    const benign = bad.length - realBad.length;
    check(realBad.length === 0, spec.name + " · 零失败请求",
      (realBad.length ? realBad.slice(0, 3).map((b) => b.p + " ← " + b.why).join(" ; ")
        : okAssets.size + " 个资产全部取到") + (benign ? "（另有 " + benign + " 次运行时的探路请求被它自己 abort，随后同一 URL 加载成功）" : ""));
    check(/^zh/i.test(d.lang), spec.name + " · html[lang] 是中文", d.lang);
    check(d.title.length > 0, spec.name + " · 有 <title>", d.title);
    check(d.overflowX <= 1, spec.name + " · 无横向溢出", "scrollWidth-clientWidth=" + d.overflowX);

    const brokenImgs = d.imgs.filter((i) => !i.ok && !i.pending);
    check(brokenImgs.length === 0, spec.name + " · 所有 <img> 解码成功",
      brokenImgs.length ? brokenImgs.map((i) => i.src).join(",") : d.imgs.length + " 枚");

    if (spec.styles) {
      check(d.sheets > 0, spec.name + " · 样式表已加载", d.sheets);
      // 没有样式表时 body 的 fontFamily 是浏览器默认（含 "Times New Roman"）；
      // 有样式就一定换成了项目自己那套 —— 这一条能抓住“css 404 了但没人报错”
      check(!/Times New Roman/i.test(d.font), spec.name + " · 样式真的生效（body 字体不是浏览器默认）",
        d.font.slice(0, 60));
    }

    const missing = spec.ids.filter((id) => !(id in d.visibleIds));
    check(missing.length === 0, spec.name + " · 关键元素都在", missing.length ? "缺 " + missing.join(",") : spec.ids.length + " 个");
    // 列为 vis 的还要求“可见”而不是“存在”：display:none 的骨架对用户等于没有
    const hidden = (spec.vis || []).filter((id) => id in d.visibleIds && !d.visibleIds[id]);
    check(hidden.length === 0, spec.name + " · 该露的都露出来了（非 0×0）",
      hidden.length ? "隐藏 " + hidden.join(",") : (spec.vis || []).length + " 个可见");

    if (spec.extra) {
      for (const [ok, label, detail] of spec.extra(d)) check(ok, spec.name + " · " + label, detail);
    }
    return true;
  } catch (e) {
    check(false, spec.name + " · 打开页面失败：" + (e && e.message));
    return false;
  } finally {
    if (OUT) {
      try {
        await page.screenshot({
          path: OUT + "/sweep_" + spec.name.split(" ")[0] + ".png",
          fullPage: false,
          omitBackground: !!spec.transparent,
        });
      } catch (e) {
        /* 截图失败不影响断言 */
      }
    }
    await ctx.close();
  }
}

async function main() {
  const browser = await chromium.launch({ channel: "msedge", headless: true });
  try {
    for (const spec of PAGES) await sweep(browser, spec);
  } finally {
    await browser.close();
  }
  console.log("\n结果：" + checks + " 项断言，失败 " + failures.length + " 项");
  for (const f of failures) console.log("  - " + f);
  return failures.length === 0 ? 0 : 1;
}

main().then((c) => process.exit(c));
