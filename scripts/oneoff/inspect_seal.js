/**
 * 一次性观察器：把 SealGen 生成的墨块按 6 倍放大铺出来，供肉眼比对。
 * 用法：node scripts/oneoff/inspect_seal.js [输出目录]
 * 不做断言，只是"显微镜"。
 */
const { chromium } = require("playwright");
const fs = require("fs");
const path = require("path");
const G = require(path.join(__dirname, "..", "..", "cmd", "workerrest", "web", "dist", "seal.js")).SealGen;

const OUT = process.argv[2] || path.join(process.env.TEMP || ".", "wr_seal_inspect");
fs.mkdirSync(OUT, { recursive: true });

const CASES = [
  { label: "50x88 短名", w: 50, h: 88 },
  { label: "44x132 三字", w: 44, h: 132 },
  { label: "37x300 长名", w: 37, h: 300 },
  { label: "37x418 超长", w: 37, h: 418 },
];
const SEEDS = [1, 20260117, 987654321];
const PAD = 4;              /* 牌外留一点边，方便看撕毛轮廓；碎点层已删（2026-11-18） */
const ZOOM = 6;

const b64 = (s) => "data:image/svg+xml;base64," + Buffer.from(s, "utf8").toString("base64");

const cells = [];
for (const c of CASES) {
  for (const seed of SEEDS) {
    const ink = b64(G.inkSVG(c.w, c.h, seed, { shadow: true }));
    const W = c.w + PAD * 2, H = c.h + PAD * 2;
    cells.push(`<div class="cell" style="width:${W * ZOOM}px;height:${H * ZOOM}px">
      <div class="box" style="width:${W}px;height:${H}px;transform:scale(${ZOOM})">
        <div class="plate" style="left:${PAD}px;top:${PAD}px;width:${c.w}px;height:${c.h}px;background-image:url('${ink}')"></div>
      </div>
      <div class="cap">${c.label} · ${seed}</div>
    </div>`);
  }
}

const html = `<!doctype html><meta charset="utf-8"><style>
  body { margin:0; background:#141210; color:#bbb; font:12px/1.4 system-ui,sans-serif; }
  .row { display:flex; gap:28px; padding:20px; align-items:flex-start; flex-wrap:wrap; }
  .cell { position:relative; flex:0 0 auto; }
  .box { position:absolute; left:0; top:0; transform-origin:top left; }
  .plate { position:absolute; background-size:100% 100%; background-repeat:no-repeat; }
  .cap { position:absolute; left:0; bottom:-20px; white-space:nowrap; }
</style>
<div class="row">${cells.join("")}</div>`;

(async () => {
  const b = await chromium.launch({ channel: "msedge" });
  const page = await b.newPage({ viewport: { width: 1400, height: 900 }, deviceScaleFactor: 1 });
  await page.setContent(html, { waitUntil: "load" });
  await page.waitForTimeout(400);
  await page.screenshot({ path: path.join(OUT, "grid.png"), fullPage: true });
  const n = await page.locator(".cell").count();
  for (let i = 0; i < n; i++) {
    const c = CASES[Math.floor(i / SEEDS.length)];
    await page.locator(".cell").nth(i).screenshot({
      path: path.join(OUT, `${c.w}x${c.h}_seed${SEEDS[i % SEEDS.length]}.png`),
    });
  }
  console.log("WR_OUT " + OUT);
  await b.close();
})();
