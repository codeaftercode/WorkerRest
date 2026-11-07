/**
 * 督休专员题名“零遮挡”全专员探针（数据采集端）。
 *
 * 背景：题名的位置与字号是渲染层按立绘 alpha 剖面反解出来的（break.js /
 * takeout.js 各一份 layoutRoleTitle）。这个反解有一处极易漏的对称性：剖面
 * minY[] 是按**原图列序**量的，而屏幕映射是**显示方向**的 —— 立绘水平翻转时
 * 两者互为镜像。2026-11-02 就漏了这一步：命中判定去查图片的另半边，牌面明明
 * 在人物左侧空白里，却“每列都算遮挡”（魔法师 hit=34 = 区间列数 34），字号被
 * 一路压到下限 15px。而这种专员只有几何配置里 flip=true 才会触发，主探针
 * （probe_renderer，跑真实配置的那一位）抽不到就等于没测。
 *
 * 所以这里逐个专员跑：把 fixedRole 钉成某一位 → 起服务 → 三个场景各量一次
 * 题名的定位结果。断言在 probe_role_title.py（Python 侧收数、Python 侧判定，
 * 与 probe_renderer.py 同一分工）。
 *
 * 只用真实 web/dist 与真实 Go 绑定；配置走 WR_SETTINGS_PATH 的隔离副本。
 * 一个专员一个服务是必须的：fixedRole 在服务启动时读进内存。
 */
const { chromium } = require("playwright");
const fs = require("fs");
const path = require("path");

const BASE = process.env.WR_BASE;
const OUT = process.env.WR_OUT;
const ROLE = process.env.WR_ROLE;
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// 三种版式各来一次：横屏休息窗、竖屏休息窗、外卖提醒窗（题名同一套算法，
// 但 takeout.js 是另一份拷贝 —— 只测其中一份等于只测一半）。
const SCENES = [
  ["break", 1100, 720],
  ["break", 760, 980],
  ["takeout", 880, 560],
];

const INFO = `(function(){
  const n = document.getElementById('dialog-name');
  const img = document.getElementById('figure-img');
  if (!n) return { error: '没有题名元素' };
  let trim = null;
  try { trim = JSON.parse((img && img.dataset.trim) || 'null'); } catch (e) { trim = null; }
  const cs = getComputedStyle(n);
  const r = n.getBoundingClientRect();
  // 牌面必须包住所有字：竖排 + 折列时 Chromium 的 shrink-to-fit 只按一列算宽，
  // 溢出列会整列漏到红底外面。Range 的横向包围盒天生比 content box 宽 2.6~4.1px
  //（字形侧边距），所以留 6.5px 容差 —— 真漏出去的一整列是 17~26px。
  const rg = document.createRange();
  rg.selectNodeContents(n);
  const t = rg.getBoundingClientRect();
  const cw = r.width - (parseFloat(cs.paddingLeft) || 0) - (parseFloat(cs.paddingRight) || 0);
  const chb = r.height - (parseFloat(cs.paddingTop) || 0) - (parseFloat(cs.paddingBottom) || 0);
  return {
    positioned: n.dataset.rt === '1',
    hit: n.dataset.rtHit != null ? Number(n.dataset.rtHit) : null,
    fs: n.dataset.rtFs != null ? Number(n.dataset.rtFs) : null,
    text: n.textContent,
    flip: !!(trim && trim.flip),
    // 竖排标志：横排时牌子的“头在左上”这套几何全都不成立
    vertical: cs.writingMode === 'vertical-rl',
    overflow: t.width > cw + 6.5 || t.height > chb + 6.5,
    box: { w: Math.round(r.width), h: Math.round(r.height) },
  };
})()`;

async function settle(page) {
  for (let i = 0; i < 100; i++) {
    const ok = await page.evaluate(() => {
      const n = document.getElementById("dialog-name");
      const img = document.getElementById("figure-img");
      return !!(n && n.dataset.rt === "1" && img && img.complete && img.naturalWidth);
    });
    if (ok) return true;
    await sleep(100);
  }
  return false;
}

(async () => {
  const browser = await chromium.launch({ channel: "msedge" });
  const preload = fs.readFileSync(process.env.WR_PRELOAD, "utf-8");
  const main = await browser.newPage({ viewport: { width: 1000, height: 760 } });
  await main.addInitScript(preload);
  await main.goto(BASE + "/", { waitUntil: "load", timeout: 30000 });
  for (let i = 0; i < 60; i++) {
    if (await main.evaluate(() => typeof window.api === "object" && window.api !== null)) break;
    await sleep(200);
  }

  const rows = [];
  for (const [scene, w, h] of SCENES) {
    if (scene === "takeout") {
      await main.evaluate("(function(){ return window.api.previewTakeout(); })()");
    } else {
      await main.evaluate("(function(){ return window.api.startBreak('short'); })()");
    }
    await sleep(700);
    // 未定位就重开一次页面：Windows 侧套接字偶发抖动会让 shim 没连上，
    // 那种失败与题名算法无关，不该判成“压到立绘”。
    let info = null;
    let errs = [];
    for (let attempt = 0; attempt < 2; attempt++) {
      const page = await browser.newPage({ viewport: { width: w, height: h } });
      errs = [];
      page.on("pageerror", (e) => errs.push(String(e)));
      await page.goto(BASE + "/" + scene + ".html", { waitUntil: "load", timeout: 30000 });
      const ready = await settle(page);
      if (!ready) await sleep(1500);
      info = await page.evaluate(INFO);
      info.ready = ready;
      await page.close().catch(() => {});
      if (info && info.positioned) break;
      await sleep(600);
    }
    info.scene = scene + "@" + w + "x" + h;
    info.role = ROLE;
    info.errs = errs;
    rows.push(info);
  }

  fs.writeFileSync(path.join(OUT, "role_title_" + ROLE + ".json"), JSON.stringify(rows, null, 2));
  console.log("WR_JSON " + JSON.stringify(rows));
  await browser.close();
})().catch((e) => {
  console.error("FAIL " + ((e && e.stack) || e));
  process.exit(1);
});
