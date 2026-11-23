/**
 * 全屏询问页（fsask.html）的页面层验证 —— 纯前端，不碰 Go、不碰配置。
 *
 * 与 probe_fs_ask.py 的分工：
 *   · probe_fs_ask.py       —— 真进程 + 真全屏靶子 + 系统级鼠标注入，验“窗弹不弹、
 *                              抢不抢前台、点了真的结案并落盘”；
 *   · 本脚本（页面层）      —— 只把 fsask.html 用 file:// 打开、把 window.api 换成记录器，
 *                              验“页面自己该负责的事”：四个按钮的形状与回传、canMark=0
 *                              的禁用语义、倒计时、超长应用名不撑破 420×200。
 *
 * 为什么这层值得单独一排断言：真窗点击只能证明“点下去有答复回来”，而按钮是不是四个、
 * data-answer 有没有写错、禁用态还回不回传、长标题会不会把倒计时挤出窗口 —— 这些在
 * 真窗里看不见（窗只有 420×200，日志里只有一条答复）。页面回归放在这里最便宜。
 *
 * 运行：node scripts/probe/fsask_page_probe.js
 * 退出码 0 = 全部通过。截图落在 %TEMP%/wr_fsask_page（三张：全量、宿主进程、超长名）。
 */
const { chromium } = require("playwright");
const path = require("path");
const fs = require("fs");
const { pathToFileURL } = require("url");

const HERE = __dirname;
const ROOT = path.resolve(HERE, "..", "..");
const PAGE = path.join(ROOT, "cmd", "workerrest", "web", "dist", "fsask.html");
const OUT = process.env.WR_OUT || path.join(process.env.TEMP || ".", "wr_fsask_page");

const VIEW = { width: 420, height: 200 }; // 与 Go 侧 fsAskWidth/fsAskHeight 同值

let checks = 0;
const failures = [];
function check(ok, label, detail) {
  checks++;
  const tail = detail === undefined ? "" : "  | " + JSON.stringify(detail);
  if (ok) {
    console.log("  ok   " + label + tail);
  } else {
    console.log("  FAIL " + label + tail);
    failures.push(label);
  }
}

async function open(browser, query) {
  const page = await browser.newPage({ viewport: VIEW });
  const errs = [];
  page.on("pageerror", (e) => errs.push(String(e)));
  page.on("console", (m) => {
    if (m.type() === "error") errs.push("console: " + m.text());
  });
  await page.goto(pathToFileURL(PAGE).href + query, { waitUntil: "load" });
  await page.waitForTimeout(250);
  // shim.js 会定义 window.api；这里整份换成一个只记录调用的假实现 ——
  // 页面在被点击后应当调用 api.fullScreenAskAnswer(answer)，记录它就知道回传了什么。
  await page.evaluate(() => {
    window.__ans = [];
    window.api = {
      fullScreenAskAnswer: (a) => {
        window.__ans.push(a);
        return Promise.resolve(null);
      },
    };
  });
  return { page, errs };
}

async function btnRects(page) {
  return page.evaluate(() =>
    Array.from(document.querySelectorAll("#actions .btn")).map((x) => {
      const r = x.getBoundingClientRect();
      return { cx: r.left + r.width / 2, cy: r.top + r.height / 2, w: +r.width.toFixed(1), h: +r.height.toFixed(1) };
    })
  );
}

async function main() {
  if (!fs.existsSync(PAGE)) {
    console.error("找不到 " + PAGE + "：web/dist 是固化产物，先确认它被提交进仓库了");
    return 2;
  }
  fs.mkdirSync(OUT, { recursive: true });
  const browser = await chromium.launch({ channel: "msedge", headless: true });

  // ---------- 1. 四个按钮的形状、文案与点击回传 ----------
  {
    const { page, errs } = await open(
      browser,
      "?win=fsask&exe=a.exe&name=%E6%B5%8B%E8%AF%95%E5%BA%94%E7%94%A8&canMark=1&secs=12"
    );
    const shape = await page.evaluate(() => {
      const btns = Array.from(document.querySelectorAll("#actions .btn"));
      return {
        n: btns.length,
        answers: btns.map((x) => x.dataset.answer),
        texts: btns.map((x) => x.textContent.trim()),
        disabled: btns.map((x) => x.disabled),
        who: document.getElementById("who").textContent,
        left: document.getElementById("left").textContent,
      };
    });
    const rects = await btnRects(page);
    check(shape.n === 4, "四个按钮", shape.n);
    check(
      JSON.stringify(shape.answers) === JSON.stringify(["pause", "skip", "today", "never"]),
      "data-answer 顺序与取值（Go 侧按这些字符串分支）",
      shape.answers
    );
    check(
      JSON.stringify(shape.texts) === JSON.stringify(["暂停", "本次不暂停", "今天不问", "永不询问"]),
      "按钮文案",
      shape.texts
    );
    check(shape.disabled.every((d) => !d), "canMark=1 时四个都可点", shape.disabled);
    check(shape.who === "测试应用", "应用名由 URL 解码后落到 #who", shape.who);
    check(shape.left === "12 秒后自动暂停", "倒计时文案取 secs", shape.left);
    check(
      rects.every((r) => r.w > 40 && r.h === 32),
      "按钮等高 32、宽度均分（版面不随文案变化）",
      rects.map((r) => r.w)
    );

    // 真实鼠标坐标点击：验 pointer 命中，而不是只看 DOM 上有没有 onclick
    const want = ["pause", "skip", "today", "never"];
    for (let i = 0; i < 4; i++) {
      await page.mouse.click(rects[i].cx, rects[i].cy);
      await page.waitForTimeout(60);
      const got = await page.evaluate(() => window.__ans.slice());
      check(
        JSON.stringify(got) === JSON.stringify(want.slice(0, i + 1)),
        "点第 " + (i + 1) + " 个按钮 → " + want[i],
        got
      );
    }
    check(errs.length === 0, "无页面错误", errs);
    await page.screenshot({ path: path.join(OUT, "full.png") });
    await page.close();
  }

  // ---------- 2. canMark=0：宿主进程（照片 / UWP）不许“记住这个应用” ----------
  // 这两个按钮会把应用写进“全屏时不暂停”名单，而名单按 exe 名匹配 —— 宿主进程写进去
  // 等于把照片、电影和电视一起静音。Go 侧 canMark 由 windnd.CanMark 算，页面只负责禁用。
  {
    const { page, errs } = await open(
      browser,
      "?win=fsask&exe=ApplicationFrameHost.exe&name=%E7%85%A7%E7%89%87&canMark=0&secs=12"
    );
    const g = await page.evaluate(() => {
      const btns = Array.from(document.querySelectorAll("#actions .btn"));
      return {
        disabled: btns.map((x) => x.disabled),
        titles: btns.map((x) => x.title || ""),
        opacity: btns.map((x) => +getComputedStyle(x).opacity),
      };
    });
    check(
      JSON.stringify(g.disabled) === JSON.stringify([false, false, true, true]),
      "canMark=0：今天不问 / 永不询问 被禁用",
      g.disabled
    );
    check(g.titles[2].includes("宿主进程") && g.titles[2] === g.titles[3], "禁用按钮带原因 title", g.titles.slice(2));
    check(g.opacity[2] < 0.5 && g.opacity[3] < 0.5, "禁用态视觉降透明", g.opacity);

    const rects = await btnRects(page);
    await page.mouse.click(rects[2].cx, rects[2].cy);
    await page.mouse.click(rects[3].cx, rects[3].cy);
    await page.waitForTimeout(120);
    const ans = await page.evaluate(() => window.__ans.slice());
    check(ans.length === 0, "点击禁用按钮不回传任何答复", ans);
    await page.mouse.click(rects[0].cx, rects[0].cy);
    await page.waitForTimeout(60);
    check(
      JSON.stringify(await page.evaluate(() => window.__ans)) === JSON.stringify(["pause"]),
      "canMark=0 时「暂停」仍可用"
    );
    check(errs.length === 0, "canMark=0 无页面错误", errs);
    await page.screenshot({ path: path.join(OUT, "host.png") });
    await page.close();
  }

  // ---------- 3. 倒计时：逐秒递减、归零改写、secs=0 整块隐藏 ----------
  // 页面倒计时只是视觉效果（真正的结案是 Go 侧的 12 秒定时器 + 拍内 deadline），
  // 但它一旦不动或归零后仍显示“1 秒后”，用户就会以为窗卡死了。
  {
    const { page } = await open(browser, "?win=fsask&name=x&canMark=1&secs=3");
    const t0 = await page.evaluate(() => document.getElementById("left").textContent);
    const w0 = await page.evaluate(() => document.getElementById("bar").style.width);
    await page.waitForTimeout(1100);
    const t1 = await page.evaluate(() => document.getElementById("left").textContent);
    await page.waitForTimeout(2100);
    const t2 = await page.evaluate(() => document.getElementById("left").textContent);
    check(t0 === "3 秒后自动暂停" && t1 === "2 秒后自动暂停", "倒计时逐秒递减", [t0, t1]);
    check(t2 === "时间到，按暂停处理", "归零后文案改为「时间到，按暂停处理」", t2);
    check(w0 === "100.0%" || w0 === "100%", "进度条初始满格", w0);
    await page.close();
  }
  {
    const { page } = await open(browser, "?win=fsask&name=y&canMark=1&secs=0");
    const g = await page.evaluate(() => ({
      barHidden: document.getElementById("bar-wrap").hidden,
      leftHidden: document.getElementById("left").hidden,
    }));
    check(g.barHidden && g.leftHidden, "secs=0 时倒计时条与文字一起隐藏", g);
    await page.close();
  }

  // ---------- 4. 超长应用名：单行省略，不撑破 420×200 ----------
  {
    const long = "X".repeat(60) + " 一个特别长的浏览器标题 全屏";
    const { page } = await open(browser, "?win=fsask&name=" + encodeURIComponent(long) + "&canMark=1&secs=12");
    const g = await page.evaluate(() => {
      const h = document.getElementById("head");
      const card = document.getElementById("card");
      return {
        headScroll: h.scrollWidth,
        headClient: h.clientWidth,
        overflowX: document.documentElement.scrollWidth > document.documentElement.clientWidth,
        cardH: +card.getBoundingClientRect().height.toFixed(1),
        bodyH: document.body.clientHeight,
      };
    });
    check(g.headScroll > g.headClient, "超长应用名真的溢出（说明测到了）", g);
    check(!g.overflowX, "长名字被单行省略号截断，不撑出横向滚动", g.overflowX);
    check(Math.abs(g.cardH - g.bodyH) <= 1, "卡片高度撑满窗口（420×200 不溢出）", g);
    await page.screenshot({ path: path.join(OUT, "long-name.png") });
    await page.close();
  }

  // ---------- 5. 任何元素都不许越出窗口 ----------
  {
    const { page } = await open(browser, "?win=fsask&name=%E6%B5%8B%E8%AF%95&canMark=1&secs=12");
    const esc = await page.evaluate(() => {
      const W = document.documentElement.clientWidth;
      const H = document.documentElement.clientHeight;
      const bad = [];
      for (const el of document.querySelectorAll("body *")) {
        const r = el.getBoundingClientRect();
        if (!r.width && !r.height) continue;
        if (r.left < -0.5 || r.top < -0.5 || r.right > W + 0.5 || r.bottom > H + 0.5) {
          bad.push({
            sel: el.id || el.className,
            l: +r.left.toFixed(1),
            t: +r.top.toFixed(1),
            r: +r.right.toFixed(1),
            b: +r.bottom.toFixed(1),
          });
        }
      }
      return { W, H, bad };
    });
    check(esc.bad.length === 0, "420×200 内没有元素越界", esc);
    await page.close();
  }

  await browser.close();
  console.log("\n结果：%d 项断言，失败 %d 项", checks, failures.length);
  for (const f of failures) console.log("  - " + f);
  console.log("截图目录：" + OUT);
  return failures.length ? 1 : 0;
}

main().then(
  (code) => process.exit(code),
  (e) => {
    console.error(e);
    process.exit(2);
  }
);
