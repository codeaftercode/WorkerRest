/**
 * 一次性工具：裁剪 PNG 的指定区域并放大，用来肉眼检查 1x 渲染细节。
 * 用法：node scripts/oneoff/crop_png.js <in.png> <x> <y> <w> <h> <zoom> [out.png]
 */
const { chromium } = require("playwright");
const fs = require("fs");
const path = require("path");

const [inp, x, y, w, h, zoom, outp] = process.argv.slice(2);
if (!inp) {
  console.error("用法：node crop_png.js <in.png> <x> <y> <w> <h> <zoom> [out.png]");
  process.exit(2);
}
const data = "data:image/png;base64," + fs.readFileSync(inp).toString("base64");

(async () => {
  const b = await chromium.launch({ channel: "msedge" });
  const page = await b.newPage();
  const url = await page.evaluate(async ({ data, x, y, w, h, zoom }) => {
    const img = new Image();
    img.src = data;
    await img.decode();
    const K = Number(zoom) || 4;
    const c = document.createElement("canvas");
    c.width = w * K; c.height = h * K;
    const ctx = c.getContext("2d");
    ctx.imageSmoothingEnabled = false;
    ctx.drawImage(img, x, y, w, h, 0, 0, c.width, c.height);
    return c.toDataURL();
  }, { data, x: +x, y: +y, w: +w, h: +h, zoom: +zoom || 4 });
  const out = outp || path.join(process.env.TEMP, "wr_crop.png");
  fs.writeFileSync(out, Buffer.from(url.split(",")[1], "base64"));
  console.log("WR_OUT " + out);
  await b.close();
})();
