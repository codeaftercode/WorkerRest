/**
 * 墨块质感探针（数据采集端）。
 *
 * 只跑纯函数，不需要启动服务：把 SealGen.inkSVG 画进 canvas，按像素统计
 * "中央保护带有没有露底"。判定在 probe_seal_ink.py。
 * 2026-11-18 起牌外碎点层整层删除（用户要求），这里顺手钉住"生成器上不再有
 * specksSVG"——否则谁把它加回来，CSS 那边早就没地方铺了，会变成死代码。
 *
 * 为什么要按像素而不是读源码参数：墨块是 SVG + 四个滤镜（fray / holes / grain）
 * 叠出来的，路径写对了不代表渲染后中央是完整的 —— feDisplacementMap 会把轮廓
 * 抖 ±3px、mask 里的孔会挖穿。只有真渲染出来数像素才算数。
 */
const { chromium } = require("playwright");
const path = require("path");

const SEAL = path.join(__dirname, "..", "..", "cmd", "workerrest", "web", "dist", "seal.js");

/* 覆盖真机出现过的牌面尺寸：30x60（预览）、37x418（超长名）、44x132（三字）、
   50x88（两字）、56x260（长名）；90x40 只走横牌兜底分支，防语法错。
   2026-11-18 补极端高度：50x60（单字极限）、50x420（16 字左右）、50x620（30 字在
   15px 下能顶到的高度）、30x220（预览长名）—— 高度是撕毛噪声的采样轴，越长的牌
   越容易把 2~3px 的毛边拉成“纵向橡皮筋”。 */
const CASES = [
  { w: 30, h: 60 },
  { w: 30, h: 220 },
  { w: 37, h: 418 },
  { w: 44, h: 132 },
  { w: 50, h: 60 },
  { w: 50, h: 88 },
  { w: 50, h: 420 },
  { w: 50, h: 620 },
  { w: 56, h: 260 },
  { w: 90, h: 40 },
];
const SEEDS = [1, 7, 42, 1234, 20260117, 987654321, 3735928559, 555, 8888, 31337,
  11, 222, 3333, 44, 505, 666, 7777, 88, 999, 1010];

(async () => {
  const b = await chromium.launch({ channel: "msedge" });
  const page = await b.newPage();
  await page.setContent("<body style='margin:0;background:#fff'></body>");
  await page.addScriptTag({ path: SEAL });

  const rows = await page.evaluate(async ({ cases, seeds }) => {
    const b64 = (s) => "data:image/svg+xml;base64," + btoa(unescape(encodeURIComponent(s)));

    async function renderOn(svg, w, h, bg) {
      const img = new Image();
      img.src = b64(svg);
      await img.decode();
      const c = document.createElement("canvas");
      c.width = w; c.height = h;
      const ctx = c.getContext("2d");
      ctx.fillStyle = bg;
      ctx.fillRect(0, 0, w, h);
      ctx.drawImage(img, 0, 0, w, h);
      return ctx.getImageData(0, 0, w, h).data;
    }
    const lum = (px, i) => 0.299 * px[i] + 0.587 * px[i + 1] + 0.114 * px[i + 2];

    /* 同一张图在白底与黑底各画一次，逐像素反解 alpha：
         alpha = 1 − (L白 − L黑) / 255
       为什么不能只看"是不是白像素"：mask 里的针孔只有 6~9% 的透明度损失，
       在白底上像素只是从 #a5261a 变成略亮的粉红，隔着"非白"判据完全看不出来。
       反解 alpha 之后，孔就是 alpha 0.93 的坑，和"整块没渲染"的 alpha 0 分得清清楚楚。 */
    async function alphaMap(svg, w, h) {
      const wp = await renderOn(svg, w, h, "#ffffff");
      const bp = await renderOn(svg, w, h, "#000000");
      const a = new Float32Array(w * h);
      for (let i = 0, n = w * h; i < n; i++) {
        const d = (lum(wp, i * 4) - lum(bp, i * 4)) / 255;
        a[i] = Math.min(1, Math.max(0, 1 - d));
      }
      return { wp, a };
    }

    const out = [];
    for (const cs of cases) {
      for (const seed of seeds) {
        const svg = SealGen.inkSVG(cs.w, cs.h, seed, { shadow: false });
        const { wp, a } = await alphaMap(svg, cs.w, cs.h);
        const alphaAt = (x, y) => a[y * cs.w + x];
        const isInk = (x, y) => {
          const i = (y * cs.w + x) * 4;
          return wp[i] < 235 || wp[i + 1] < 235 || wp[i + 2] < 235;
        };
        const vertical = cs.h >= cs.w;
        const shortLen = vertical ? cs.w : cs.h;
        const longLen = vertical ? cs.h : cs.w;

        /* 中央保护带：短轴中间 30%，长轴两头各留一段。
           端头留白不能只取长轴的 10%：50x60 的单字牌只有 6px，而轮廓内缩 + 撕毛
           位移在端头能吃掉 8~11px（实测 seed 20260117 的露底像素全在 y=6/7），
           那是**牌子的圆角端头**，不是中央针孔。所以再叠一条 shortSide×22% 的下限。 */
        const c0 = Math.floor(shortLen * 0.35), c1 = Math.ceil(shortLen * 0.65);
        const endPad = Math.max(Math.floor(longLen * 0.10), Math.round(shortLen * 0.22));
        const a0 = endPad, a1 = longLen - endPad;

        let tot = 0, hole = 0, holeMin = 1, whole = 0;
        for (let y = 0; y < cs.h; y++) {
          for (let x = 0; x < cs.w; x++) {
            if (isInk(x, y)) whole++;
            const acr = vertical ? x : y;
            const alg = vertical ? y : x;
            if (acr < c0 || acr >= c1 || alg < a0 || alg >= a1) continue;
            tot++;
            const al = alphaAt(x, y);
            if (al < holeMin) holeMin = al;
            if (al < 0.985) hole++;      /* 透底 = 针孔 / 裂口 / 半透明 */
          }
        }
        /* 中央带里最长的"整行/整列透底"：真正的横断会在这里顶出几十像素，
           边缘那点抗锯齿顶多 1px。 */
        let gapRun = 0, gapMax = 0;
        for (let a2 = a0; a2 < a1; a2++) {
          let any = false;
          for (let c2 = c0; c2 < c1; c2++) {
            const x = vertical ? c2 : a2, y = vertical ? a2 : c2;
            if (alphaAt(x, y) >= 0.985) { any = true; break; }
          }
          if (any) gapRun = 0; else { gapRun++; if (gapRun > gapMax) gapMax = gapRun; }
        }

        /* 边带（距短轴边缘 12%）的墨水覆盖率：<1 说明边缘有撕口，不是一刀切的矩形 */
        const e0 = Math.max(1, Math.round(shortLen * 0.12));
        let eTot = 0, eInk = 0;
        for (let y = 0; y < cs.h; y++) {
          for (let x = 0; x < cs.w; x++) {
            const acr = vertical ? x : y;
            if (acr >= e0 && acr < shortLen - e0) continue;
            eTot++;
            if (isInk(x, y)) eInk++;
          }
        }

        out.push({
          size: cs.w + "x" + cs.h,
          seed,
          cover: +(whole / (cs.w * cs.h)).toFixed(3),
          holePct: +(hole / Math.max(1, tot) * 100).toFixed(2),
          alphaMin: +holeMin.toFixed(3),
          gapMax,
          edgeCover: +(eInk / Math.max(1, eTot)).toFixed(3),
        });
      }
    }
    return out;
  }, { cases: CASES, seeds: SEEDS });

  const gen = await page.evaluate(() => ({
    hasSpecks: typeof SealGen.specksSVG === "function",
    hasApply: typeof SealGen.applyTo === "function",
  }));

  /* 纯函数契约（不渲染）：同种子必须逐字相同、异种子必须不同、viewBox 必须等于
     请求的像素尺寸；applyTo 必须按“尺寸+种子”复用，且拒绝 4px 以下的退化盒
     （牌子没量出来就画墨块会得到一个 12x12 的小红点，反而更难看）。 */
  const contract = await page.evaluate(() => {
    const out = {};
    const a = SealGen.inkSVG(50, 300, 123, { shadow: true });
    const b = SealGen.inkSVG(50, 300, 123, { shadow: true });
    const c = SealGen.inkSVG(50, 300, 124, { shadow: true });
    out.sameSeed = a === b;
    out.diffSeed = a !== c;
    out.viewBox = /viewBox="0 0 50 300"/.test(a);
    out.seedZero = (() => { try { return SealGen.inkSVG(50, 300, 0, {}).length > 0; } catch (e) { return false; } })();
    out.seedMax = (() => { try { return SealGen.inkSVG(50, 300, 4294967295, {}).length > 0; } catch (e) { return false; } })();
    const el = document.createElement("div");
    el.style.cssText = "position:absolute;width:50px;height:300px";
    document.body.appendChild(el);
    out.apply1 = SealGen.applyTo(el, 50, 300, 7);
    const k1 = el.dataset.sealKey;
    out.apply2 = SealGen.applyTo(el, 50, 300, 7);
    out.reuse = k1 === el.dataset.sealKey;
    const v1 = el.style.getPropertyValue("--rt-ink");
    out.hasInkVar = /^url\("data:image\/svg\+xml/.test(v1);
    out.apply3 = SealGen.applyTo(el, 50, 301, 7);
    out.sizeChangesKey = k1 !== el.dataset.sealKey;
    const el2 = document.createElement("div");
    document.body.appendChild(el2);
    out.rejectTiny = SealGen.applyTo(el2, 4, 4, 7) === false && !el2.dataset.sealKey;
    el.remove(); el2.remove();
    return out;
  });
  console.log("WR_JSON " + JSON.stringify({ rows, gen, contract, cases: CASES, seeds: SEEDS }));
  await b.close();
})();
