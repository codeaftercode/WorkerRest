/**
 * 督休专员卡片内预览面板：层序 + 换人过渡的密集帧采集（数据采集端）。
 *
 * 两件事：
 *   1) 名字牌（含墨块 / 碎点）与立绘的层序 —— 必须压在立绘之下（真机 .dialog-name=7、
 *      .stage=8）。用 elementFromPoint 在重叠处实测“谁在上”，再配合可见状态。
 *   2) 换人时的过渡帧：CPU 限速放大解码窗口，每 30ms 采一次 trim 可见性、src、
 *      naturalWidth 与 img 的 alpha 采样，并落屏截图。
 */
const { chromium } = require("playwright");
const fs = require("fs");
const path = require("path");

const BASE = process.env.WR_BASE;
const OUT = process.env.WR_OUT;
const PRELOAD = process.env.WR_PRELOAD;
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

const DUMP = `(() => {
  const box = document.getElementById('sup-preview');
  const img = document.getElementById('sup-preview-img');
  if (!box || !img) return { error: 'no preview' };
  const r = (el) => { const b = el.getBoundingClientRect();
    return { x: +b.x.toFixed(1), y: +b.y.toFixed(1), w: +b.width.toFixed(1), h: +b.height.toFixed(1) }; };
  const fig = box.querySelector('.sup-preview-fig');
  const trim = document.getElementById('sup-preview-trim');
  const name = document.getElementById('sup-preview-name');
  const content = box.querySelector('.sup-preview-content');
  const csImg = getComputedStyle(img);
  const csTrim = getComputedStyle(trim);
  // 位图 alpha 抽样：把当前 img 画进 24x24 canvas，四角应全透明（人物不占角）
  let alpha = null;
  try {
    const c = document.createElement('canvas'); c.width = 24; c.height = 24;
    const g = c.getContext('2d');
    g.drawImage(img, 0, 0, 24, 24);
    const pick = (x, y) => g.getImageData(x, y, 1, 1).data[3];
    alpha = { tl: pick(0, 0), tr: pick(23, 0), bl: pick(0, 23), br: pick(23, 23) };
  } catch (e) { alpha = 'err:' + (e && e.message); }
  // 层序实测：牌与立绘可见框相交区域取一个点，问浏览器那里的最上层是谁
  let hit = null;
  if (name && name.offsetWidth) {
    const nr = name.getBoundingClientRect();
    const fr = fig.getBoundingClientRect();
    const x0 = Math.max(nr.left, fr.left), x1 = Math.min(nr.right, fr.right);
    const y0 = Math.max(nr.top, fr.top), y1 = Math.min(nr.bottom, fr.bottom);
    if (x1 > x0 + 1 && y1 > y0 + 1) {
      const el = document.elementFromPoint((x0 + x1) / 2, (y0 + y1) / 2);
      hit = { overlap: true, top: el ? (el.id || el.className || el.tagName) : null };
    } else hit = { overlap: false };
  }
  return {
    stage: box.dataset.stage,
    srcLen: (img.getAttribute('src') || '').length,
    nat: [img.naturalWidth, img.naturalHeight],
    complete: img.complete,
    trimVis: csTrim.visibility,
    trimTfInline: trim.style.transform,
    figZ: getComputedStyle(fig).zIndex,
    nameZ: name ? getComputedStyle(name).zIndex : null,
    nameRect: name ? r(name) : null,
    nameDisp: name ? getComputedStyle(name).display : null,
    nameTxt: name ? name.textContent : null,
    contentZ: content ? getComputedStyle(content).zIndex : null,
    imgFilter: csImg.filter,
    imgOpacity: csImg.opacity,
    alpha,
    hit,
    imgRect: r(img),
  };
})()`;

(async () => {
  fs.mkdirSync(OUT, { recursive: true });
  const b = await chromium.launch({ channel: "msedge", headless: true });
  const page = await b.newPage({ viewport: { width: 1180, height: 1000 }, deviceScaleFactor: 2 });
  page.on("pageerror", (e) => console.log("PAGEERROR " + (e && e.message)));
  if (PRELOAD && fs.existsSync(PRELOAD)) await page.addInitScript({ path: PRELOAD });
  await page.goto(BASE + "/settings.html", { waitUntil: "domcontentloaded" });
  await page.waitForFunction("typeof window.api === 'object' && window.api !== null");
  await sleep(700);
  await page.click('.nav-item[data-tab="supervisor"]');
  await sleep(1600);

  const names = await page.evaluate(() => Array.from(
    document.querySelectorAll("#sup-gallery .role-item"),
    (e) => (e.querySelector(".role-cap") || e).textContent.trim()));

  const cdp = await page.context().newCDPSession(page);
  await cdp.send("Emulation.setCPUThrottlingRate", { rate: 6 });   // 放大解码窗口

  const rows = [];
  const picks = names.slice(1, 4);       // 换人（第 0 位已在台上）
  for (let i = 0; i < picks.length; i++) {
    const idx = i + 1;
    await page.click(`#sup-gallery .role-item:nth-child(${idx}) .role-cap`);
    const frames = [];
    for (let f = 0; f < 14; f++) {
      const st = await page.evaluate(DUMP);
      frames.push(st);
      await page.locator("#sup-preview").screenshot({ path: path.join(OUT, `f_${i}_${String(f).padStart(2, "0")}.png`) });
      await sleep(30);
    }
    await sleep(600);
    await page.locator("#sup-preview").screenshot({ path: path.join(OUT, `steady_${i}.png`) });
    rows.push({ idx, name: picks[i], frames, steady: await page.evaluate(DUMP) });
  }
  await cdp.send("Emulation.setCPUThrottlingRate", { rate: 1 });

  // 层序：稳定态 + 二阶段各量一次
  const layers = await page.evaluate(`(() => {
    const box = document.getElementById('sup-preview');
    const out = {};
    for (const [k, sel] of [['fig', '.sup-preview-fig'], ['name', '#sup-preview-name'], ['content', '.sup-preview-content']]) {
      const el = box.querySelector(sel);
      out[k] = el ? { z: getComputedStyle(el).zIndex, pos: getComputedStyle(el).position } : null;
    }
    out.order = Array.from(box.children, (e) => e.className || e.id);
    return out;
  })()`);

  // 重叠场景：把立绘推到最左、放到最大 —— 名字牌这时必然压在人物身上，
  // 直观检验“立绘在上层、牌被挡住一部分”（用户 2026-11-17 提的第二条规则）。
  // ⚠️ 滑条会写盘（commitGeomToRole → 内置素材的 meta.json），跑完必须还原，
  // 否则仓库里的 assets/supervisors 会被探针改脏。滑条值这里会写回原值；但 SaveRole
  // 序列化时还会补一个默认字段 `showName: true`，那一条得跑完手动 `git checkout --
  // assets/supervisors` 抹掉（只是补默认值，不影响功能）。
  const geom0 = await page.evaluate(() => ({
    posx: document.getElementById("sup-posx").value,
    size: document.getElementById("sup-size").value,
  }));
  await page.evaluate(() => {
    const set = (id, v) => { const el = document.getElementById(id); el.value = String(v); el.dispatchEvent(new Event("input", { bubbles: true })); };
    set("sup-posx", 0);
    set("sup-size", 95);
  });
  await sleep(900);
  await page.locator("#sup-preview").screenshot({ path: path.join(OUT, "overlap.png") });
  const overlap = await page.evaluate(DUMP);

  await page.evaluate(() => { document.getElementById("sup-preview").dataset.stage = "2"; });
  await sleep(700);
  await page.locator("#sup-preview").screenshot({ path: path.join(OUT, "stage2.png") });
  const stage2 = await page.evaluate(DUMP);

  // 还原滑条（写回原值 → meta.json 回到原样）
  await page.evaluate((g) => {
    const set = (id, v) => { const el = document.getElementById(id); el.value = String(v); el.dispatchEvent(new Event("input", { bubbles: true })); };
    set("sup-posx", g.posx);
    set("sup-size", g.size);
  }, geom0);
  await sleep(1200);

  await b.close();
  console.log("WR_JSON " + JSON.stringify({ names, rows, layers, overlap, stage2 }));
})().catch((e) => { console.error(e); process.exit(1); });
