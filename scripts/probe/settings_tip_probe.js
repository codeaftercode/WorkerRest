/**
 * 设置页“?”提示探针（Playwright 驱动系统 Edge）。
 *
 * 三件事必须同时成立，缺一条用户就会觉得“提示不好用”：
 *   1. 触发范围：指针落在“?”上、或落在它左侧那段文字上，都要弹出同一条说明
 *      （原生 title 只能挂在一个元素上，这就是当初换成自绘浮层的原因）；
 *   2. 位置观感：圆圈贴紧文字、圆心与汉字的**字形中心**同高 —— 不是行盒中心，
 *      行盒 19px 而汉字只占 12px，按行盒居中会让圆圈低半格；
 *   3. 浮层本身：不超出视口、不盖住窗口外、长文案会自动换行、层级高于下拉菜单。
 *
 * 字形中心用 canvas 的 measureText 加「把左侧最后一个字选成 Range」算：字符盒 top 加
 * fontBoundingBoxAscent 就是基线，再用 actualBoundingBox 的墨迹高/深取墨迹中心。
 * 两条老路都不能用：Range 整段选中的 rect 是行盒（19px），会把对齐判成偏低 5px；
 * 往父元素插 0×0 inline-block 取 top 在 flex 行里拿到的是项盒顶、根本不是基线
 * （2026-11-08 就是因为这个，“? 浮在文字上方 5px”被量成了 0 偏差）。
 *
 * 运行：由 probe_settings_ui.py 起好无头服务后 `node settings_tip_probe.js` 拉起。
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

// 每个“?”的几何：间距（到左侧文字最后一个字）+ 圆心差（到汉字字形中心）
//
// 基线怎么量：**不能**往父元素里插一个 0×0 的 inline-block —— 父元素是 flex 行时
// 它就成了一个 flex 项，取到的 top 是项盒顶而不是基线（旧版就是这么量的，于是
// “? 浮在文字上方 5px”也能量出 0 偏差、一路绿灯）。这里改成：把左侧文字的最后
// 一个字选成 Range，字符盒 top + 字体的 fontBoundingBoxAscent 即基线。
const GEOM = `(function () {
  const out = [];
  // 左侧文字的“最内层文本节点”：裸文本 / .q-tip-host 包裹 / .cdrop-cur / .cdrop-label
  function lastTextNode(start) {
    let n = start;
    while (n && n.nodeType === 3 && !n.textContent.trim()) n = n.previousSibling;
    while (n && n.nodeType === 1) { n = n.lastChild; }
    return n && n.nodeType === 3 ? n : null;
  }
  for (const q of document.querySelectorAll('.q-mark')) {
    const r = q.getBoundingClientRect();
    if (!r.width || !r.height) continue;            // 隐藏 tab / 收起菜单里的不算
    const node = lastTextNode(q.previousSibling);
    if (!node) { out.push({ text: '(无前置文字)', gap: null, off: null }); continue; }
    const raw = node.textContent;
    const ch = (raw.trim().slice(-1)) || '字';
    const idx = raw.lastIndexOf(ch);
    const chr = document.createRange();
    chr.setStart(node, idx);
    chr.setEnd(node, idx + 1);
    const cr = chr.getBoundingClientRect();
    const cs = getComputedStyle(q.parentElement);
    const c = document.createElement('canvas').getContext('2d');
    c.font = cs.fontStyle + ' ' + cs.fontWeight + ' ' + cs.fontSize + ' ' + cs.fontFamily;
    const m = c.measureText(ch);
    // 字符盒 top 是“基线上缘”，加字体 ascent 就落到基线上；再用字形墨迹高/深
    // 取墨迹中心 —— 汉字只占行盒中间那 12px，按行盒量会把已对齐判成偏低。
    const baseline = cr.top + (m.fontBoundingBoxAscent || 0);
    const glyphMid = baseline - (m.actualBoundingBoxAscent || 0) / 2 + (m.actualBoundingBoxDescent || 0) / 2;
    out.push({
      text: raw.trim().slice(-10),
      host: q.closest('.cdrop-item') ? 'item' : q.closest('.cdrop-btn') ? 'btn' : 'text',
      gap: Math.round((r.left - cr.right) * 10) / 10,
      off: Math.round(((r.top + r.bottom) / 2 - glyphMid) * 10) / 10,
      dia: Math.round(r.width * 10) / 10,
    });
  }
  return out;
})()`;

const TIP = `(function () {
  const t = document.getElementById('q-tip');
  if (!t) return null;
  const r = t.getBoundingClientRect();
  return {
    hidden: t.classList.contains('hidden'),
    shown: t.classList.contains('show'),
    text: t.textContent,
    left: r.left, right: r.right, top: r.top, bottom: r.bottom,
    w: r.width, h: r.height,
    vw: window.innerWidth, vh: window.innerHeight,
  };
})()`;

(async () => {
  fs.mkdirSync(OUT, { recursive: true });
  const browser = await chromium.launch({ channel: "msedge", headless: true });
  const page = await browser.newPage({ viewport: { width: 874, height: 857 } });
  await page.addInitScript({ path: PRELOAD });
  await page.goto(BASE + "/settings.html", { waitUntil: "networkidle" });
  await delay(1300);

  console.log("[?] 静态契约");
  const statics = await page.evaluate(() => {
    const qs = [...document.querySelectorAll(".q-mark")];
    return {
      count: qs.length,
      noTip: qs.filter((q) => !(q.dataset.tip || "").trim()).length,
      withTitle: qs.filter((q) => q.hasAttribute("title")).length,
      noRole: qs.filter((q) => q.getAttribute("role") !== "note").length,
      noLabel: qs.filter((q) => !(q.getAttribute("aria-label") || "").trim()).length,
      noHost: qs.filter((q) => {
        const p = q.previousElementSibling;
        return !p || !p.classList.contains("q-tip-host");
      }).length,
      hasTipBox: !!document.getElementById("q-tip"),
      role: (document.getElementById("q-tip") || {}).getAttribute
        ? document.getElementById("q-tip").getAttribute("role")
        : null,
    };
  });
  check(statics.hasTipBox, "页面里有浮层单例 #q-tip");
  check(statics.role === "tooltip", "浮层 role=tooltip", statics.role);
  check(statics.count >= 12, "“?”数量不低于基线", statics.count);
  check(statics.noTip === 0, "每个“?”都有 data-tip", statics.noTip + " 个缺文案");
  check(statics.withTitle === 0, "不再保留原生 title（避免双提示）", statics.withTitle + " 个仍有 title");
  check(statics.noRole === 0, "每个“?”都是 role=note", statics.noRole);
  check(statics.noLabel === 0, "每个“?”都有 aria-label（读屏有话说）", statics.noLabel);
  check(statics.noHost === 0, "每个“?”左侧文字已打标（可触发）", statics.noHost);

  console.log("[?] 间距与垂直对齐（对汉字字形中心）");
  const geo = await page.evaluate(GEOM);
  const gapped = geo.filter((g) => g.gap != null);
  check(gapped.length >= 8, "量到的“?”数量", gapped.length);
  const worstGap = gapped.reduce((a, b) => (Math.abs(b.gap) > Math.abs(a.gap) ? b : a), gapped[0] || { gap: 0 });
  check(Math.abs(worstGap.gap) <= 4, "圆圈与左侧文字间距 ≤ 4px", "最大 " + worstGap.gap + "px（" + worstGap.text + "）");
  const worstOff = gapped.reduce((a, b) => (Math.abs(b.off) > Math.abs(a.off) ? b : a), gapped[0] || { off: 0 });
  check(Math.abs(worstOff.off) <= 1, "圆心与汉字字形中心偏差 ≤ 1px", "最大 " + worstOff.off + "px（" + worstOff.text + "）");
  const dia = gapped.reduce((a, b) => (b.dia > a.dia ? b : a), gapped[0] || { dia: 0 });
  check(dia.dia <= 13.5, "圆圈直径不超过正文字高（13px）", dia.dia);

  console.log("[?] 指向左侧文字");
  const firstTip = await page.evaluate(() => document.querySelector(".q-mark").dataset.tip);
  await page.locator(".q-tip-host").first().hover();
  await delay(400);
  const byText = await page.evaluate(TIP);
  check(byText && !byText.hidden && byText.shown, "浮层出现");
  check(byText && byText.text === firstTip, "文案与 data-tip 一致", (byText && byText.text || "").slice(0, 18));
  check(byText && byText.w > 40 && byText.w <= 321, "宽度受 max-width 约束", Math.round(byText ? byText.w : 0));
  check(byText && byText.left >= 0 && byText.right <= byText.vw, "不超出视口左右", Math.round(byText ? byText.left : 0) + ".." + Math.round(byText ? byText.right : 0));
  check(byText && byText.top >= 0 && byText.bottom <= byText.vh, "不超出视口上下", Math.round(byText ? byText.top : 0) + ".." + Math.round(byText ? byText.bottom : 0));
  const gapBelow = await page.evaluate(() => {
    const q = document.querySelector(".q-mark").getBoundingClientRect();
    const t = document.getElementById("q-tip").getBoundingClientRect();
    return t.top - q.bottom;
  });
  check(gapBelow >= 4 && gapBelow <= 14, "浮层贴在圆圈下方", Math.round(gapBelow) + "px");
  await page.screenshot({ path: path.join(OUT, "tip_by_text.png") });

  console.log("[?] 指向问号本身");
  const secondTip = await page.evaluate(() => document.querySelectorAll(".q-mark")[1].dataset.tip);
  await page.locator(".q-mark").nth(1).hover();
  await delay(350);
  const byMark = await page.evaluate(TIP);
  check(byMark && byMark.shown, "浮层出现");
  check(byMark && byMark.text === secondTip, "文案跟着换", (byMark && byMark.text || "").slice(0, 18));

  console.log("[?] 移开后收起");
  await page.mouse.move(600, 760);
  await delay(400);
  const away = await page.evaluate(TIP);
  check(away && away.hidden && !away.shown, "浮层收起");

  console.log("[?] 长文案换行");
  await page.locator("label:has(#fullscreenPause)").hover();
  await delay(450);
  const longTip = await page.evaluate(TIP);
  check(longTip && longTip.h > 50, "长文案换行（高度 > 单行）", Math.round(longTip ? longTip.h : 0));
  check(longTip && longTip.w <= 321, "长文案不顶破 320px", Math.round(longTip ? longTip.w : 0));
  check(longTip && longTip.right <= longTip.vw && longTip.left >= 0, "长文案仍在视口内", Math.round(longTip ? longTip.left : 0));
  await page.screenshot({ path: path.join(OUT, "tip_long.png") });

  // 2026-11-10：「暂停休息时」的每项不再各挂一个“?”（下拉里点开一次满屏问号），
  // 改成选中项那一句摊在控件下方；它虽然仍是自绘下拉，但菜单里已没有“?”。显示器那块
  // 是**多选**，每项只有分辨率 / 主屏标注，也没有“?”。原来这一段（菜单项的“?”能弹、
  // 浮层层级高于菜单、圆圈贴着文字）因此没有宿主了，删掉比钉一个不存在的控件诚实。
  // “?”紧贴文字这条规则仍由上面的字段行 / 卡片标题断言守着。

  console.log("[?] 深色主题");
  await page.click('.nav-item[data-tab="general"]');
  await delay(300);
  // 上一段在“时间规划”页很靠下的位置展开过外卖卡的下拉，页面滚动位置与鼠标都停在那里。
  // 切页后先把滚动位置与鼠标归位，再 hover —— 否则 hover 头一步是“滚进来”，
  // 滚动会把刚排好的浮层躲开，看起来像“深色下不弹”。
  await page.evaluate(() => {
    for (const el of document.querySelectorAll("*")) {
      if (el.scrollTop) el.scrollTop = 0;
    }
    window.scrollTo(0, 0);
  });
  await page.mouse.move(4, 4);
  await delay(150);
  await page.evaluate(() => document.documentElement.setAttribute("data-theme", "dark"));
  await page.locator("label:has(#dndPause)").hover();
  await delay(450);
  const dark = await page.evaluate(() => {
    const t = document.getElementById("q-tip");
    const card = document.querySelector("#tab-general .card");
    const cs = getComputedStyle(t);
    return {
      shown: t.classList.contains("show"),
      bg: cs.backgroundColor,
      cardBg: getComputedStyle(card).backgroundColor,
      color: cs.color,
    };
  });
  check(dark.shown, "深色下同样弹出");
  check(dark.bg !== dark.cardBg, "深色下浮层与卡片不同色（有层次）", dark.bg + " vs " + dark.cardBg);
  await page.screenshot({ path: path.join(OUT, "tip_dark.png") });

  await browser.close();
  console.log("");
  console.log("结果：失败 " + failures.length + " 项 / 共 " + checks + " 项断言");
  process.exit(failures.length ? 1 : 0);
})().catch((e) => {
  console.error("FAILED", e);
  process.exit(1);
});
