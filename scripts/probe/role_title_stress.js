/**
 * 题名牌“极端用例”压力探针（数据采集端）。
 *
 * 与 probe_role_title.js 的分工：那个探针钉住**真实专员名**跑三种版式，验的是
 * “内置名单下不压脸”；这个探针把名字换成 26 组极端输入（1 字 / 30 字上限 /
 * 200 字 / emoji / ZWJ / 泰文 / 阿拉伯文 / HTML 标签 / 首尾空格 / 换行 / 空名 …），
 * 在**同一张真实立绘**上反复重排，验的是“任何名字都不越界、不折列、不糊、不崩”。
 *
 * 名字注入走生产同一条路径：改 `dataset.fullName` + `textContent`，清掉 `data-rt`，
 * 再派发 `resize`（bindSupContentLayout 的监听器就是生产用的重排入口）。不调内部
 * 函数、不改产品代码 —— 测到的就是用户在设置里改名之后牌面的真实行为。
 *
 * 附加场景（都在 break 1100×720 上跑，最后一项换 context）：
 *   · 改名风暴：12 次改名 + 重排连续同步执行，终态必须是最后一个名字（无残留/竞态）
 *   · 同尺寸重复：同一名字连测两次，sealKey 必须复用（墨块不重画）
 *   · 窗口扫掠：760×520 → 760×1400 → 1600×600 → 1100×720（真实 resize，不重载）
 *   · 立绘极端：--fig-size 100% / --fig-x 0（贴左，触发 edge 贴法）/ --fig-x 100（贴右）
 *   · DPI 对照：deviceScaleFactor=2 重开一个 context，同样三个名字，与 1x 结果对比
 *
 * 只用真实 web/dist 与真实 Go 绑定；配置走 WR_SETTINGS_PATH 的隔离副本。
 */
const { chromium } = require("playwright");
const fs = require("fs");
const path = require("path");

const BASE = process.env.WR_BASE;
const OUT = process.env.WR_OUT;
const ROLE = process.env.WR_ROLE;
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// 造 n 个**互不相同**的汉字：重复串（"长长长…"）会让“首…尾”的省略校验失去区分度
const cjk = (n) => {
  let s = "";
  for (let i = 0; i < n; i++) s += String.fromCharCode(0x4e00 + ((i * 37) % 1800));
  return s;
};

const NAMES = [
  ["1字-汉字", "王"],
  ["1字-拉丁", "A"],
  ["2字", "吃饼"],
  ["10字", "图书馆角落的温柔学姐"],
  ["16字-含全角括号", "资深前端架构师王小明（平台组）"],
  ["21字-英文词组", "Head of the Department"],
  ["29字-无空格长词", "Christopher-Featherstonehaugh"],
  ["30字-UI上限", cjk(30)],
  ["42字", cjk(42)],
  ["60字-Go上限", cjk(60)],
  ["120字", cjk(120)],
  ["200字", cjk(200)],
  ["emoji", "✨饼人✨"],
  ["ZWJ家庭", "👨‍👩‍👧‍👦家庭群"],
  ["日文", "こんにちは世界です"],
  ["韩文", "안녕하세요세계입니다"],
  ["泰文", "สวัสดีชาวโลก"],
  ["阿拉伯文-RTL", "مرحبا بالعالم"],
  ["含省略号", "前半…后半"],
  ["HTML标签", "<b>粗体</b>"],
  ["首尾空格", "  前后空格  "],
  ["中间点", "Dr. Who"],
  ["含换行", "第一行\n第二行"],
  ["全角拉丁", "ＡＢＣＤＥＦＧＨＩＪＫ"],
  ["31拉丁带空格", "a b c d e f g h i j k l m n o p q"],
  ["空名", ""],
];

const SHOTS = new Set(["30字-UI上限", "200字", "emoji", "泰文", "阿拉伯文-RTL", "空名"]);

const SCENES = [
  ["break", 1100, 720],
  ["break", 760, 980],
  ["takeout", 880, 560],
];

// 一次采集：牌面几何 + 文字 + 墨块 + 是否压界。全部用 DOM/计算样式，不碰内部变量。
const SNAP = `(function(){
  var n = document.getElementById('dialog-name');
  if (!n) return { error: 'no #dialog-name' };
  var box = document.querySelector('.content');
  var cs = getComputedStyle(n);
  var af = getComputedStyle(n, '::after');
  var r = n.getBoundingClientRect();
  // 布局位置（不含 scale / transform）：二阶段“凑近”给 .dialog-name 挂了
  // scale 1.08（脉动到 1.095），getBoundingClientRect 量到的是**放大后**的盒子，
  // 会平白多出几像素“越界”。算法保证的是布局位置，所以几何断言用 offsetLeft/Top。
  var op = n.offsetParent;
  var opr = op ? op.getBoundingClientRect() : { left: 0, top: 0 };
  var lLeft = opr.left + n.offsetLeft;
  var lTop = opr.top + n.offsetTop;
  var rg = document.createRange();
  rg.selectNodeContents(n);
  var tr = rg.getBoundingClientRect();
  var padX = (parseFloat(cs.paddingLeft) || 0) + (parseFloat(cs.paddingRight) || 0);
  var padY = (parseFloat(cs.paddingTop) || 0) + (parseFloat(cs.paddingBottom) || 0);
  var cw = r.width - padX;
  var chb = r.height - padY;
  var fs = parseFloat(cs.fontSize) || 0;
  var ls = parseFloat(cs.letterSpacing) || 0;
  var ink = cs.backgroundImage || '';
  /* 2026-11-18：牌外碎点层整层删除了。::after 现在没有规则，backgroundImage 恒为
     'none'；这条只用来守住“没有谁把碎点偷偷加回来”。 */
  var afBg = af.backgroundImage || 'none';
  function vb(bg) {
    var i = bg.indexOf('base64,');
    if (i < 0) return null;
    try {
      var s = bg.slice(i + 7).split('"')[0].split(')')[0];
      var dec = atob(s);
      var m = dec.match(/viewBox="0 0 ([0-9.]+) ([0-9.]+)"/);
      return m ? [Math.round(+m[1]), Math.round(+m[2])] : null;
    } catch (e) { return null; }
  }
  return {
    positioned: n.dataset.rt === '1',
    hiddenClass: n.classList.contains('hidden'),
    visible: cs.visibility === 'visible' && r.width > 0 && r.height > 0,
    fs: n.dataset.rtFs != null ? Number(n.dataset.rtFs) : fs,
    fsComputed: Math.round(fs * 100) / 100,
    hit: n.dataset.rtHit != null ? Number(n.dataset.rtHit) : null,
    // 牌面被立绘覆盖的面积占比（0~1）：2026-11-17 起“零遮挡”改成“允许少许重叠”，
    // 立绘在上层把牌的一条边压住。断言的判据就是这个值。
    ov: n.dataset.rtOv != null ? Number(n.dataset.rtOv) : null,
    // 是否走了求解器的“第二级”（先完全不碰、全字号都放不下才允许少量重叠）。
    // 这个标记用来守住“必要时才压一点”：如果绝大多数用例都走了放宽轮，
    // 说明算法又在“能压就压”，哪怕每条都合法也该报警。
    relax: n.dataset.rtRelax === '1',
    text: n.textContent,
    fullName: n.dataset.fullName || null,
    elided: n.dataset.rtElided === '1',
    vertical: cs.writingMode === 'vertical-rl',
    // 单列：内容盒宽只装得下**一列**（折列时是 fs*1.26 的整数倍）
    columns: Math.max(1, Math.round(cw / (fs * 1.26 + ls)) * 1),
    contentWidth: Math.round(cw),
    overflow: tr.width > cw + 6.5 || tr.height > chb + 6.5,
    rangeBox: { w: Math.round(tr.width), h: Math.round(tr.height) },
    box: { w: n.offsetWidth, h: n.offsetHeight },
    left: Math.round(r.left * 100) / 100,
    top: Math.round(r.top * 100) / 100,
    right: Math.round(r.right * 100) / 100,
    bottom: Math.round(r.bottom * 100) / 100,
    // 布局盒（scale 之前）：几何断言只认这四个数
    lLeft: Math.round(lLeft * 100) / 100,
    lTop: Math.round(lTop * 100) / 100,
    lRight: Math.round((lLeft + n.offsetWidth) * 100) / 100,
    lBottom: Math.round((lTop + n.offsetHeight) * 100) / 100,
    // 视觉盒在窗口外的量（二阶段凑近时会外扩，属于设计；只记录，边界单独放宽）
    vOutLeft: Math.round(Math.min(0, r.left) * 100) / 100,
    vOutRight: Math.round(Math.max(0, r.right - innerWidth) * 100) / 100,
    vOutBottom: box
      ? Math.round(Math.max(0, r.bottom - box.getBoundingClientRect().top) * 100) / 100
      : null,
    scale: cs.scale || 'none',
    // 牌与立绘在**最近公共祖先的层叠上下文**里的绘制次序（数值大 = 画在上面）。
    // 规则（2026-11-18 反转，原 2026-11-17 版本是“立绘在上层”）：牌允许与立绘少量
    // 重叠，且**牌必须在上层** —— 牌顶下沉到颈线后右缘压在袖口上，立绘在上会把最后
    // 一列字形切掉。所以 nameLayer 必须 > figLayer。不能直接比两个元素的 z-index：
    // 休息窗口里立绘在 .stage(8) 这个层叠上下文内部（自身 7），牌在 #app 下（9）——
    // 比的是各自祖先进到公共层后的数值。
    nameLayer: (function () {
      var el = n, root = el && el.parentElement;
      if (!el || !root) return null;
      var z = getComputedStyle(el).zIndex;
      return z === 'auto' ? 0 : Number(z);
    })(),
    figLayer: (function () {
      var el = n, fig = document.getElementById('figure');
      if (!el || !fig || fig.classList.contains('hidden')) return null;
      var root = el.parentElement;
      var p = fig;
      while (p && p.parentElement !== root) p = p.parentElement;
      if (!p) return null;
      var z = getComputedStyle(p).zIndex;
      return z === 'auto' ? 0 : Number(z);
    })(),
    // 上内边距：牌面上缘允许出窗的量就是它（不带字的那截墨块）
    padTop: parseFloat(cs.paddingTop) || 0,
    dialogTop: box ? Math.round(box.getBoundingClientRect().top * 100) / 100 : null,
    winW: innerWidth,
    winH: innerHeight,
    docScrollW: document.documentElement.scrollWidth,
    docScrollH: document.documentElement.scrollHeight,
    inkType: ink.indexOf('data:image/svg+xml') >= 0 ? 'data:seal'
      : (ink.indexOf('linear-gradient') >= 0 ? 'css-fallback' : 'none'),
    sealKey: n.dataset.sealKey || null,
    inkVB: vb(ink),
    speckType: /data:image\\/svg\\+xml/.test(afBg) ? 'data:seal'
      : (afBg === 'none' ? 'none' : 'other'),
    childNodes: n.children.length,
    fontFamily: cs.fontFamily,
    dpr: devicePixelRatio,
  };
})()`;

function newErrors(page) {
  const errs = [];
  page.on("pageerror", (e) => errs.push(String(e)));
  return errs;
}

async function settle(page, tries = 100) {
  for (let i = 0; i < tries; i++) {
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

// 走生产同一条路径改名：dataset.fullName + textContent + 清 data-rt + resize 重排
async function applyName(page, name) {
  const r = await page.evaluate((v) => {
    const n = document.getElementById("dialog-name");
    if (!n) return { error: "no #dialog-name" };
    const t0 = performance.now();
    n.dataset.fullName = v;
    n.textContent = v;
    delete n.dataset.rt;
    delete n.dataset.rtElided;
    delete n.dataset.rtFs;
    delete n.dataset.rtHit;
    delete n.dataset.rtOv;
    void n.offsetWidth;
    window.dispatchEvent(new Event("resize"));
    return { ms: Math.round((performance.now() - t0) * 100) / 100 };
  }, name);
  const snap = await page.evaluate(SNAP);
  snap.layoutMs = r.ms;
  return snap;
}

async function openScene(browser, main, scene, w, h) {
  if (scene === "takeout") {
    await main.evaluate("(function(){ return window.api.previewTakeout(); })()");
  } else {
    await main.evaluate("(function(){ return window.api.startBreak('short'); })()");
  }
  await sleep(700);
  for (let attempt = 0; attempt < 3; attempt++) {
    const page = await browser.newPage({ viewport: { width: w, height: h } });
    const errs = newErrors(page);
    await page.goto(BASE + "/" + scene + ".html", { waitUntil: "load", timeout: 30000 });
    const ready = await settle(page);
    if (ready) return { page, errs };
    await sleep(600);
    await page.close().catch(() => {});
  }
  throw new Error("版式起不来（未定位）：" + scene);
}

function row(snap, meta) {
  return Object.assign({}, snap, meta, { role: ROLE });
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
  let dpr1 = {};

  for (const [scene, w, h] of SCENES) {
    let handle;
    try {
      handle = await openScene(browser, main, scene, w, h);
    } catch (e) {
      rows.push({ error: String((e && e.message) || e), scene: scene, case: "SCENE", name: "" });
      continue;
    }
    const { page, errs } = handle;
    const tag = scene + "@" + w + "x" + h;

    // ---- 1. 极端名字矩阵 ----
    for (const [id, name] of NAMES) {
      const before = errs.length;
      const snap = await applyName(page, name);
      const meta = {
        scene: tag, case: id, name: name, nameLen: [...name].length,
        errs: errs.slice(before),
      };
      rows.push(row(snap, meta));
      if (SHOTS.has(id) && scene === "break" && w === 1100) {
        await page.screenshot({ path: path.join(OUT, "stress_" + id + ".png") });
      }
      // 空名之后必须能恢复：状态不残留（dataset / hidden / visibility）
      if (id === "空名") {
        const b2 = errs.length;
        const rec = await applyName(page, "恢复测试名字");
        rows.push(row(rec, {
          scene: tag, case: "空名后恢复", name: "恢复测试名字", nameLen: 6,
          errs: errs.slice(b2),
        }));
      }
      if (scene === "break" && w === 1100) dpr1[id] = snap;
    }

    // ---- 2. 附加场景：只在横屏休息窗上跑 ----
    if (scene === "break" && w === 1100) {
      // 2a 改名风暴：12 次「改名 + 重排」同步连续执行
      const storm = ["短", "中等长度的名字", cjk(30), "🎯", cjk(8), "A", cjk(42), "最后落定"];
      const before = errs.length;
      const st = await page.evaluate((names) => {
        const n = document.getElementById("dialog-name");
        const t0 = performance.now();
        for (const s of names) {
          n.dataset.fullName = s;
          n.textContent = s;
          delete n.dataset.rt;
          window.dispatchEvent(new Event("resize"));
        }
        return { ms: Math.round((performance.now() - t0) * 100) / 100 };
      }, storm);
      const snap = await page.evaluate(SNAP);
      snap.layoutMs = st.ms;
      rows.push(row(snap, {
        scene: tag, case: "改名风暴×8", name: storm[storm.length - 1],
        nameLen: [...storm[storm.length - 1]].length, stormExpect: storm[storm.length - 1],
        errs: errs.slice(before),
      }));

      // 2b 同尺寸重复：sealKey 必须复用（墨块不重画）
      const a1 = await applyName(page, cjk(30));
      const a2 = await applyName(page, cjk(30));
      rows.push(row(a2, {
        scene: tag, case: "同尺寸重复", name: cjk(30), nameLen: 30,
        sealKeyFirst: a1.sealKey, firstMs: a1.layoutMs, errs: [],
      }));

      // 2c 窗口扫掠（真实 resize，不重载页面）
      for (const [vw, vh] of [[760, 520], [760, 1400], [1600, 600], [1100, 720]]) {
        await page.setViewportSize({ width: vw, height: vh });
        await sleep(350);
        const s = await page.evaluate(SNAP);
        rows.push(row(s, {
          scene: "resize", case: "扫掠 " + vw + "x" + vh, name: s.fullName || "",
          nameLen: [...(s.fullName || "")].length, errs: [],
        }));
      }

      // 2d 立绘极端：只用**滑条真能拖到**的区间（settings.js 的 SUP_GEOM_LIMITS：
      // 高度 20~100、距左 0~30、距底 0~40）。越界值 Go 会夹回去，测了也不算数。
      for (const [fx, fy, size] of [[0, 0, 100], [30, 0, 100], [30, 40, 100], [0, 40, 20]]) {
        const before2 = errs.length;
        await page.evaluate((v) => {
          const st = document.documentElement.style;
          st.setProperty("--fig-x", String(v.fx));
          st.setProperty("--fig-y", String(v.fy));
          st.setProperty("--fig-size", String(v.size));
          void document.body.offsetHeight;
          window.dispatchEvent(new Event("resize"));
        }, { fx, fy, size });
        await sleep(250);
        const s = await page.evaluate(SNAP);
        rows.push(row(s, {
          scene: "fig", case: "立绘 x=" + fx + " y=" + fy + " size=" + size,
          name: s.fullName || "",
          nameLen: [...(s.fullName || "")].length, errs: errs.slice(before2),
        }));
      }
    }

    await page.close().catch(() => {});
    await sleep(300);
  }

  // ---- 3. DPI=2 对照（新 context，同一个休息会话）----
  try {
    const ctx2 = await browser.newContext({
      viewport: { width: 1100, height: 720 }, deviceScaleFactor: 2,
    });
    const p2 = await ctx2.newPage();
    const errs2 = newErrors(p2);
    await p2.goto(BASE + "/break.html", { waitUntil: "load", timeout: 30000 });
    if (await settle(p2)) {
      for (const id of ["10字", "30字-UI上限", "200字"]) {
        const name = (NAMES.find((x) => x[0] === id) || ["", ""])[1];
        const s = await applyName(p2, name);
        rows.push(row(s, {
          scene: "dpr2", case: id, name: name, nameLen: [...name].length,
          dpr1: dpr1[id] || null, errs: errs2.slice(-1),
        }));
      }
    } else {
      rows.push({ error: "DPI=2 页面未定位", scene: "dpr2", case: "SCENE", name: "" });
    }
    await ctx2.close();
  } catch (e) {
    rows.push({ error: String((e && e.message) || e), scene: "dpr2", case: "SCENE", name: "" });
  }

  fs.writeFileSync(path.join(OUT, "role_title_stress_" + ROLE + ".json"),
    JSON.stringify(rows, null, 2));
  console.log("WR_JSON " + JSON.stringify(rows));
  await browser.close();
})().catch((e) => {
  console.error("FAIL " + ((e && e.stack) || e));
  process.exit(1);
});
