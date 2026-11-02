"use strict";
(() => {
  // renderer/figureTrim.ts
  var ALPHA_MIN = 8;
  var MAX_SAMPLE = 2048;
  var IGNORE = 8e-3;
  var MIN_CONTENT = 0.25;
  var MAX_SCALE = 4;
  var NONE = { left: 0, right: 0, top: 0, bottom: 0, content: 1, scale: 1, transform: "none" };
  function norm(v) {
    return v < IGNORE ? 0 : v;
  }
  function measureFigureTrim(img) {
    const w = img.naturalWidth;
    const h = img.naturalHeight;
    if (!w || !h) return NONE;
    const s = Math.min(1, MAX_SAMPLE / Math.max(w, h));
    const cw = Math.max(1, Math.round(w * s));
    const ch = Math.max(1, Math.round(h * s));
    const cv = document.createElement("canvas");
    cv.width = cw;
    cv.height = ch;
    const ctx = cv.getContext("2d", { willReadFrequently: true });
    if (!ctx) return NONE;
    ctx.clearRect(0, 0, cw, ch);
    ctx.drawImage(img, 0, 0, cw, ch);
    let px;
    try {
      px = ctx.getImageData(0, 0, cw, ch).data;
    } catch {
      return NONE;
    }
    let top = -1;
    let bottom = -1;
    let left = cw;
    let right = -1;
    for (let y = 0; y < ch; y++) {
      const row = y * cw * 4;
      let has = false;
      for (let x = 0; x < cw; x++) {
        if (px[row + x * 4 + 3] >= ALPHA_MIN) {
          has = true;
          if (x < left) left = x;
          if (x > right) right = x;
        }
      }
      if (has) {
        if (top < 0) top = y;
        bottom = y;
      }
    }
    if (bottom < 0 || right < 0) return NONE;
    const out = {
      left: norm(left / cw),
      right: norm((cw - 1 - right) / cw),
      top: norm(top / ch),
      bottom: norm((ch - 1 - bottom) / ch),
      // 未过 IGNORE 阈值的原始边距（摆位锚点专用）：IGNORE 会把 <0.8% 的边距归零，
      // 对变换无害（那点边距本就看不见），但锚点必须指向像素的真实位置——
      // 否则立绘上缘差 0.8%*立绘高（≈5px），换不同督休专员（如 role_ai 上缘 0.78%）
      // 时倒计时锚点会系统性偏高。
      rawLeft: left / cw,
      rawRight: (cw - 1 - right) / cw,
      rawTop: top / ch,
      rawBottom: (ch - 1 - bottom) / ch,
      content: 1,
      scale: 1,
      transform: "none"
    };
    out.content = 1 - out.top - out.bottom;
    if (out.content < MIN_CONTENT) return NONE;
    return out;
  }
  var measured = /* @__PURE__ */ new WeakMap();
  function applyFigureTrim(host, img, flip = false) {
    host.style.transformOrigin = "left bottom";
    const run = () => {
      host.style.transform = "none";
      const t = measureFigureTrim(img);
      const left = flip ? t.right : t.left;
      const right = flip ? t.left : t.right;
      const rendered = host.getBoundingClientRect();
      const scale = t.content >= MIN_CONTENT ? Math.min(MAX_SCALE, 1 / t.content) : 1;
      const ar = img.naturalHeight > 0 ? img.naturalWidth / img.naturalHeight : 0;
      // ⚠️ fill 必须按**布局盒**算，不能读 img 的 getBoundingClientRect：img 自身
      // 可能带着 Stage 变换（#app[data-stage='1'] 的 scale(1.03) scaleY(1.006)，
      // 纵向 1.03618 > 横向 1.03）。立绘图常在变换生效之后才加载完，那时量到的是
      // 变换后尺寸——W/H 比自然 aspect 小 0.6%，fill 被压成 0.994（应为 1.0），
      // trim 的平移量整体偏小、立绘可见边与摆位锚点一起算歪（实测顶缘差 ~4px）。
      // host 的变换已在函数开头重置为 none，其 rect 即布局盒（img 盒撑满 host：
      // height:100%，宽度随 shrink-to-fit 链相等），且不受 img 自身变换影响。
      const fill = rendered.height > 0 && ar > 0 ? Math.min(1, rendered.width / ar / rendered.height) : 1;
      const needTrim = left > 0 || t.top > 0 || t.bottom > 0;
      const transform = needTrim ? `translate(${(-left * scale * 100).toFixed(3)}%, ${(t.bottom * scale * fill * 100).toFixed(3)}%) scale(${scale.toFixed(4)})` : "none";
      host.style.transform = transform;
      host.style.visibility = "visible";
      img.dataset.trim = JSON.stringify({
        left: +left.toFixed(4),
        right: +right.toFixed(4),
        top: +t.top.toFixed(4),
        bottom: +t.bottom.toFixed(4),
        // 像素真实边距（未过 IGNORE）：摆位锚点用，语义与 left/right 一致（含 flip 交换）
        rawLeft: +(flip ? t.rawRight : t.rawLeft).toFixed(4),
        rawRight: +(flip ? t.rawLeft : t.rawRight).toFixed(4),
        rawTop: +t.rawTop.toFixed(4),
        rawBottom: +t.rawBottom.toFixed(4),
        scale: +scale.toFixed(4),
        fill: +fill.toFixed(4),
        flip,
        transform
      });
    };
    const cacheKey = `${img.src}|${flip ? "F" : "-"}`;
    if (measured.get(img) === cacheKey && img.dataset.trim) {
      const cached = JSON.parse(img.dataset.trim);
      host.style.transform = cached.transform;
      host.style.visibility = "visible";
      return;
    }
    measured.set(img, cacheKey);
    if (img.complete && img.naturalWidth > 0) {
      run();
      return;
    }
    host.style.visibility = "hidden";
    img.addEventListener("load", run, { once: true });
    img.addEventListener("error", () => {
      img.dataset.trim = JSON.stringify({ transform: "none", flip });
      host.style.visibility = "visible";
    }, { once: true });
  }

  // renderer/supervisorStage.ts（默认值同 Go 的 supGeomDefault：高度 80%、距左 5%、贴底）
  var GEOM_FALLBACK = { sizePct: 80, posXPct: 5, posYPct: 0, flip: false };
  var ESCALATE_WEIGHT = 3;
  var RELAX_MS = 6e3;
  function setupSupervisor(sup, stage1Text, opts) {
    opts = opts || {};
    const showLine = opts.showLine !== false;            // 默认显示督休专员台词
    const allowEscalate = opts.allowEscalate !== false;  // 默认允许升级到第二阶段
    const app = document.getElementById("app");
    const figure = document.getElementById("figure");
    const gaze = document.getElementById("gaze");
    const img = document.getElementById("figure-img");
    const line = document.getElementById("supervisor-line");
    document.body.classList.add("has-supervisor");
    figure.classList.remove("hidden");
    if (showLine) {
      line.classList.remove("hidden");
      if (sup.name) line.dataset.speaker = sup.name;
      else delete line.dataset.speaker;
    } else {
      line.classList.add("hidden");
      delete line.dataset.speaker;
    }
    // 督休专员题名：竖排金色宋体，钉在立绘头部左侧的空白区（.dialog-name）。
    // 位置与字号由 layoutRoleTitle() 按 alpha 剖面实测写入；这里只填文本，并清掉
    // 上一轮的 data-rt —— 换专员后旧位置对新立绘是错的，重新定位成功前保持不可见。
    const nameEl = document.getElementById("dialog-name");
    if (nameEl) {
      delete nameEl.dataset.rt;
      // 每次出场换一枚「章」：牌底轮廓/断裂与四周碎点一起换（同一下标）
      pickSealInk(nameEl);
      if (sup.name) {
        nameEl.textContent = sup.name;
        nameEl.classList.remove("hidden");
      } else {
        nameEl.textContent = "";
        nameEl.classList.add("hidden");
      }
    }
    const g = sup.geom ?? GEOM_FALLBACK;
    const flip = g.flip === true;
    document.body.classList.toggle("sup-flip", flip);
    const vars = document.documentElement.style;
    vars.setProperty("--fig-size", String(g.sizePct));
    vars.setProperty("--fig-x", String(g.posXPct));
    vars.setProperty("--fig-y", String(g.posYPct));
    img.src = sup.image;
    applyFigureTrim(document.getElementById("figure-trim"), img, flip);
    const supApi = window.api;
    const stage1 = showLine ? (stage1Text || sup.lines.urge) : "";
    let stage = 1;
    let weight = 0;
    let relaxTimer;
    const applyStage = (next) => {
      if (next === stage) return;
      stage = next;
      app.dataset.stage = String(next);
      if (showLine) line.textContent = next === 2 ? sup.lines.command || stage1 : stage1;
      // 框高固定后文本换行数变化不再触发 ResizeObserver，这里显式做一次兜底缩字号
      fitDialogText(document.getElementById("content"));
    };
    app.dataset.stage = "1";
    if (showLine) line.textContent = stage1;
    const relax = () => {
      window.clearTimeout(relaxTimer);
      relaxTimer = window.setTimeout(() => {
        weight = 0;
        applyStage(1);
      }, RELAX_MS);
    };
    const noteActivity = (w, broadcast) => {
      weight = Math.min(weight + w, 12);
      if (weight >= ESCALATE_WEIGHT) applyStage(2);
      relax();
      if (broadcast) supApi?.supActivity?.();
    };
    if (allowEscalate) {
      let lastMove = 0;
      window.addEventListener("mousemove", () => {
        const now = performance.now();
        if (now - lastMove < 500) return;
        lastMove = now;
        noteActivity(1, true);
      });
      window.addEventListener("mousedown", () => noteActivity(3, true), { passive: true });
      window.addEventListener("keydown", () => noteActivity(3, true), { passive: true });
      window.addEventListener("wheel", () => noteActivity(3, true), { passive: true });
      window.addEventListener("touchstart", () => noteActivity(3, true), { passive: true });
      // 系统级键鼠活动：Go 侧轮询 user32!GetLastInputInfo，用户在**任意应用**里敲键 /
      // 动鼠标都会广播过来（与窗口焦点无关）。页面侧留一个诊断计数，探针据此确认
      // 「全局事件真的到达本页」，而不是靠 data-stage 反推（窗口内事件也会升级，会掩盖）。
      supApi?.onSupActivity?.(() => {
        window.__WR_SUP_ACTIVE = (window.__WR_SUP_ACTIVE || 0) + 1;
        noteActivity(3, false);
      });
    }
    if (!sup.gaze) return;
    gaze.style.transformOrigin = "50% 100%";
    let targetNX = 0;
    let targetNY = 0;
    let curNX = 0;
    let curNY = 0;
    window.addEventListener("mousemove", (e) => {
      targetNX = e.clientX / window.innerWidth * 2 - 1;
      targetNY = e.clientY / window.innerHeight * 2 - 1;
    });
    // 题名牌要跟着立绘一起动，但它不能搬进 .gaze 那条 DOM 链：.trim 的补偿缩放
    // （立绘可见区越窄放得越大，1.0~2.0）会把牌面文字一起放大，而牌面位置是按
    // 「渲染后的像素」反解出来的，进去就全错。所以把同一套变换镜像一份写过去：
    // 同一个原点（立绘盒底边中点 = 双脚，见 layoutRoleTitle 写的 --rt-ox/--rt-oy）、
    // 同一组函数、同一顺序 ⇒ 数学上等价于「牌面焊在立绘上」：立绘怎么偏，牌面就怎么偏，
    // 中间那条缝一个像素都不变。
    // 唯一要换算的是平移量 —— .gaze 的位移发生在 .trim 的缩放空间里，渲染出来是 scale
    // 倍，所以镜像时乘上它（skew 的角度、scaleY 的比值都不受均匀缩放影响）。
    let trimKey = "";
    let trimScale = 1;
    const trimScaleNow = () => {
      const raw = img.dataset.trim || "";
      if (raw !== trimKey) {
        trimKey = raw;
        try { trimScale = JSON.parse(raw).scale || 1; } catch (e) { trimScale = 1; }
      }
      return trimScale;
    };
    // 帧内只在字符串真的变了才写：插值收敛后每帧算出来是同一个值，
    // 无脑赋值会让 Chromium 每帧重算样式 + 重绘整块牌面（它有 drop-shadow 与
    // background-clip: text，重绘不便宜）。
    let lastGazeTf = "";
    let lastPlateTf = "";
    const frame = () => {
      curNX += (targetNX - curNX) * 0.075;
      curNY += (targetNY - curNY) * 0.075;
      const h = Math.max(1, gaze.offsetHeight);
      const tx = curNX * 14;
      const ty = Math.max(0, curNY) * 8;
      const lean = -curNX * 0.9;
      const sy = 1 + Math.max(0, -curNY) * 8 / h;
      const gtf = `translate3d(${tx.toFixed(2)}px, ${ty.toFixed(2)}px, 0) skewX(${lean.toFixed(2)}deg) scaleY(${sy.toFixed(4)})`;
      if (gtf !== lastGazeTf) { lastGazeTf = gtf; gaze.style.transform = gtf; }
      if (nameEl) {
        const k = trimScaleNow();
        const ptf = `translate3d(${(tx * k).toFixed(2)}px, ${(ty * k).toFixed(2)}px, 0) skewX(${lean.toFixed(2)}deg) scaleY(${sy.toFixed(4)})`;
        if (ptf !== lastPlateTf) { lastPlateTf = ptf; nameEl.style.transform = ptf; }
      }
      requestAnimationFrame(frame);
    };
    requestAnimationFrame(frame);
  }

  // renderer/supervisorLayout.ts —— 游戏对话画面版式
  // .content 已是底部全宽对话框（CSS 定版），这里只负责把倒计时环浮到
  // 「立绘可见右缘~窗口右缘」与「立绘可见上缘~对话框上边缘」两个中点的交点上
  // （环中心对准；「提前结束」已固定在窗口顶部，不参与这里的摆位）。
  // 摆位结果写入 supMetrics 供探针断言。
  var CD_EDGE = 10;
  var frozenFig = null;          // 立绘可见边（去透明）的冻结锚点（窗口坐标）
  var lastLayoutKey = "";        // 仅在窗口/立绘盒（含位置）/trim 就绪变化时重算，避免二阶段缩放导致倒计时位移
  function clamp(v, lo, hi) {
    return Math.min(Math.max(v, lo), hi);
  }
  // 立绘「去掉透明部分后」的可见边缘（窗口坐标）——摆位锚点的唯一来源。
  // DOM 链：figure(绝对定位盒) > .trim(透明边距补偿) > .gaze > .mirror > img，
  // img 盒撑满 .trim/.figure（height:100%，宽度由 shrink-to-fit 链决定）。
  // .trim 的补偿变换 = translate(-left*s%, bottom*s*fill%) scale(s)，
  // transform-origin: left bottom。设立绘盒左缘 x0、底缘 yB、盒宽 W、盒高 H，
  // 把 img 盒映射到屏幕后，像素内容的四条可见边：
  //   · 左缘 = x0 + s*W*(rawLeft - left)
  //   · 右缘 = x0 + s*W*(1 - rawRight - left)
  //   · 顶缘 = yB - s*fill*H*(1 - rawTop - bottom)
  //   · 底缘 = yB - s*fill*H*(rawBottom - bottom)
  // 其中 left/bottom 是变换**实际使用**的边距（过 IGNORE，可能被归零），
  // rawLeft/rawTop 等是像素**真实**边距（未归零）——二者只在边距 <0.8% 时不同。
  // s/fill/边距来自 img.dataset.trim（applyFigureTrim 量出，量取时 Stage 变换
  // 不参与——fill 按布局盒量），W/H 取立绘盒。
  // ⚠️ 曾经的 bug：① 顶缘漏减 bottom；② 左右缘直接取 .trim 变换后外框（含缩放后
  // 的透明边距）；③ fill 读的是 img 变换后的 rect（Stage 纵向缩放更大 → 被压成
  // 0.994）。三者都让锚点整体偏高/偏外，用户换立绘、改尺寸后必错位。
  // trim 尚未就绪（图片未加载/量失败）时退化为立绘自身外框。
  function figureVisibleRect(figure, fallbackTop) {
    const box = figure.getBoundingClientRect();
    const img = figure.querySelector("img");
    let t = null;
    try { t = img ? JSON.parse(img.dataset.trim || "null") : null; } catch (e) { t = null; }
    if (!t || typeof t.scale !== "number" || box.width <= 0 || box.height <= 0) {
      return { left: box.left, right: box.right, top: fallbackTop, bottom: box.bottom };
    }
    const s = t.scale;
    const fill = typeof t.fill === "number" ? t.fill : 1;
    const l = typeof t.left === "number" ? t.left : 0;
    const bt = typeof t.bottom === "number" ? t.bottom : 0;
    const rl = typeof t.rawLeft === "number" ? t.rawLeft : l;
    const rr = typeof t.rawRight === "number" ? t.rawRight : (typeof t.right === "number" ? t.right : 0);
    const rtp = typeof t.rawTop === "number" ? t.rawTop : (typeof t.top === "number" ? t.top : 0);
    const rbt = typeof t.rawBottom === "number" ? t.rawBottom : bt;
    return {
      left: box.left + s * box.width * (rl - l),
      right: box.left + s * box.width * (1 - rr - l),
      top: box.bottom - s * fill * box.height * (1 - rtp - bt),
      bottom: box.bottom - s * fill * box.height * (rbt - bt)
    };
  }
  // ── 督休专员题名（竖排金色宋体）─────────────────────────────────────────
  // 立绘的「alpha 逐列剖面」。包围盒在这里不够用：立绘底部常有刀鞘/衣袍向左探出
  // （始皇帝包围盒左缘 44px，而头部那一段的最左不透明列在 148px），照包围盒摆名字
  // 会一头撞在人物身上。所以单独量一份 minY[x]（每列最上面的不透明像素行号）。
  // 「头部段」= 图片顶部这一段。不能只看头顶那 16%：题名是竖排的，长一点的会向下
  // 伸到下颌/肩线附近，那里的内容（散落的发丝、肩甲）比头顶更靠左。按 16% 定的右缘
  // 到下面就会贴上去（竖屏「温柔学姐」实测 hit=1）。26% 能盖住题名的实际下探深度。
  var HEAD_BAND = 0.26;
  var PROFILE_W = 360;    // 剖面采样宽度：只用来找列，不需要高清
  // 题名避让用**实质遮挡**阈值：ALPHA_MIN(8) 太敏感，会把发丝边缘的抗锯齿（≈5%
  // 不透明度，压上去肉眼无感）也算成「有人」，于是把字号一路压到下限。32/255≈12.5%。
  var PROFILE_ALPHA = 32;
  var profileCache = new WeakMap();

  function measureHeadProfile(img) {
    // complete 必须一起判：dataURL/webp 在「数据到齐但还没解码」时 naturalWidth 就
    // 已经有值了，此时 drawImage 画出来是空的或残缺的，会量出一个假剖面。
    if (!img || !img.complete || !img.naturalWidth || !img.naturalHeight) return null;
    const hit = profileCache.get(img);
    if (hit && hit.src === img.src) return hit.profile;
    const w = img.naturalWidth;
    const h = img.naturalHeight;
    const cw = Math.min(PROFILE_W, w);
    const ch = Math.max(1, Math.round((h * cw) / w));
    const cv = document.createElement("canvas");
    cv.width = cw;
    cv.height = ch;
    const ctx = cv.getContext("2d", { willReadFrequently: true });
    if (!ctx) return null;
    ctx.clearRect(0, 0, cw, ch);
    ctx.drawImage(img, 0, 0, cw, ch);
    let px;
    try {
      px = ctx.getImageData(0, 0, cw, ch).data;
    } catch (e) {
      return null;   // 跨域/解码失败：题名退回不显示，不影响主流程
    }
    // minY[x] = 该列最上面的不透明像素行号；整列全空则保持 ch
    const minY = new Int32Array(cw).fill(ch);
    for (let x = 0; x < cw; x++) {
      for (let y = 0; y < ch; y++) {
        if (px[(y * cw + x) * 4 + 3] >= PROFILE_ALPHA) {
          minY[x] = y;
          break;
        }
      }
    }
    // 头部段内的最左 / 最右不透明列
    const bandEnd = Math.round(ch * HEAD_BAND);
    let headLeft = -1;
    let headRight = -1;
    for (let x = 0; x < cw; x++) {
      if (minY[x] <= bandEnd) {
        if (headLeft < 0) headLeft = x;
        headRight = x;
      }
    }
    const profile = { w: cw, h: ch, minY, headLeft, headRight };
    // 头部段整列为空时**绝不缓存**：正常立绘不会这样，出现它基本都是这次采样赶上了
    // 半解码。缓存下来的话后续重试永远拿到这份空剖面，名字就彻底不显示了。
    if (headLeft >= 0) profileCache.set(img, { src: img.src, profile });
    return profile;
  }

  var RT_GAP = 12;      // 题名右缘距「头部段最左列」的留白
  var RT_EDGE = 6;      // 题名离窗口左缘的最小留白
  var RT_MIN_FS = 15;   // 字号下限：1x 屏 14px 已发虚，15 是能读的底线
  var RT_MAX_FS = 26;
  var RT_STEP = 0.5;

  // 印泥变体：同一枚章连盖两次也不该一模一样。每次出场随机挑一组（牌子 + 周围碎点
  // 用同一个下标，保证「牌子和溅出来的碎点属于同一枚章」），并避开上一次用过的那张。
  // 变体由 scripts/make_seal_ink.py 生成，CSS 里的 url("seal-ink-1.svg") 只是兜底。
  var SEAL_VARIANTS = 6;
  var lastSeal = 0;
  function pickSealInk(nameEl) {
    if (!nameEl) return;
    // SEAL_VARIANTS 是 var（有声明提升但赋值在后面）：万一首帧就调用，退回 6
    var i = 1 + Math.floor(Math.random() * (SEAL_VARIANTS || 6));
    if (i === lastSeal) i = (i % SEAL_VARIANTS) + 1;   // 不连着用同一张
    lastSeal = i;
    nameEl.style.setProperty("--rt-ink", 'url("seal-ink-' + i + '.svg")');
    nameEl.style.setProperty("--rt-specks", 'url("seal-specks-' + i + '.svg")');
  }

  // 把题名钉到「立绘头部左侧的空白区」，并按零遮挡反解字号。
  // 只有先钉住右缘（= headPx - RT_GAP）再一路缩字号，才能同时保证「不压人」和
  // 「不出窗」——固定字号再找位置，长名字必然二选一失败。
  function layoutRoleTitle(figure, nameEl, box) {
    if (!figure || !nameEl || nameEl.classList.contains("hidden")) return null;
    if (!nameEl.textContent) return null;
    if (!box || box.width <= 0 || box.height <= 0) return null;
    const img = figure.querySelector("img");
    const pf = measureHeadProfile(img);
    if (!pf || pf.headLeft < 0) return null;
    let t = null;
    try { t = JSON.parse(img.dataset.trim || "null"); } catch (e) { t = null; }
    const s = t && typeof t.scale === "number" ? t.scale : 1;
    const fill = t && typeof t.fill === "number" ? t.fill : 1;
    const l = t && typeof t.left === "number" ? t.left : 0;
    const rawTop = t && typeof t.rawTop === "number" ? t.rawTop : 0;
    const flip = !!(t && t.flip);

    // 与 figureVisibleRect() 同一套映射（见该函数上方注释），这里只是需要
    // 「图片绘制区在页面里的绝对位置」，好把剖面坐标反算回去。
    const drawW = s * box.width;
    const drawH = s * fill * box.height;
    const imgLeft = box.left - drawW * l;
    const imgTop = box.bottom - drawH;
    const visTop = imgTop + drawH * rawTop;

    // 头部段最左列；水平翻转时头部镜像，要取原图的最右列
    const bandX = flip ? pf.w - 1 - pf.headRight : pf.headLeft;
    if (bandX < 0) return null;
    const headPx = imgLeft + (bandX / pf.w) * drawW;
    const rightPx = headPx - RT_GAP;

    const appEl = document.getElementById("app") || document.body;
    const appRect = appEl.getBoundingClientRect();

    // 零遮挡求解：从 RT_MAX_FS 起每档 −0.5px，直到「一个不透明像素都不压 +
    // 整块没被 RT_EDGE 钳住」。capH > 0 时给整块一个最大高度 —— vertical-rl
    // 据此折出第二列（右→左，中文竖排的读序），长名就能留在头部左侧的同一片
    // 空白里，不必一路缩到发虚。capH 生效时同时限制列数（maxCols）：实测 13 字名
    // 在 26px 会折成 3 列 98px 宽的金字块，压过头顶、读起来像「一块招牌」；
    // 限 2 列后整块是 40px 上下的一条窄带，与 3~4 字名的观感才连得上。
    // 注意 order：每次都要先把 left/top 钉回 0 再量，shrink-to-fit 元素否则
    // 量到的是上一轮的字号（74 当 35 那种）。
    // 题名现在带红牌（padding 来自 CSS），所以要先用「不折列」量出一列的自然尺寸，
    // 再自己算列数与宽度：Chromium 的 shrink-to-fit 在竖排 + max-height 下**只按
    // 一列算宽**，溢出列会整列漏到红底外面（实测 13 字名有 6 个字露在牌外）。
    const cs0 = getComputedStyle(nameEl);
    const padX = parseFloat(cs0.paddingLeft) || 0;
    const padY = parseFloat(cs0.paddingTop) || 0;
    const nChars = [...(nameEl.textContent || "")].length;
    function solve(capH, maxCols) {
      let f = RT_MAX_FS, l = 0, hit = 0, ok = false, cw = 0;
      for (; f >= RT_MIN_FS - 1e-6; f -= RT_STEP) {
        nameEl.style.setProperty("--rt-fs", f + "px");
        nameEl.style.left = "0px";
        nameEl.style.top = "0px";
        // ① 不折列，量一列的自然宽高（都是内容盒尺寸）
        nameEl.style.maxHeight = "none";
        nameEl.style.width = "auto";
        void nameEl.offsetWidth;
        const oneW = Math.max(1, nameEl.offsetWidth - padX * 2);
        const oneH = Math.max(1, nameEl.offsetHeight - padY * 2);
        // ② 需要折列就把宽度写死 = 列数 × 一列推进量
        let wpx, hpx, fits = true;
        cw = 0;
        if (capH > 0) {
          const innerH = Math.max(1, capH - padY * 2);
          // 列数不能拿「总高 / 段高」折算：每列只排得下整数个字。必须先用
          // 单字推进量（oneH / 字数）算出每列容量，再算需要几列 —— 否则 13 字在
          // 21px 会算成 2 列，实际要 3 列，整整一列漏到牌外（实测 scrollWidth 89 > 68）。
          const adv = oneH / Math.max(1, nChars);
          const perCol = Math.max(1, Math.floor(innerH / adv + 1e-6));
          const need = Math.max(1, Math.ceil(nChars / perCol));
          fits = need <= maxCols;                       // 列数不够 = 这个字号装不下
          // 宽度一律按**实际需要的列数**给，宁可宽也不漏字；全局 box-sizing:border-box，
          // 写 width 是含 padding 的宽（少加一次 padding 就会挤窄内容盒、多折一列）
          cw = Math.ceil(oneW * need) + padX * 2;
          nameEl.style.maxHeight = Math.round(capH) + "px";
          nameEl.style.width = cw + "px";
          void nameEl.offsetWidth;
          wpx = nameEl.offsetWidth;
          hpx = nameEl.offsetHeight;
        } else {
          wpx = oneW + padX * 2;
          hpx = oneH + padY * 2;
        }
        if (!wpx || !hpx) break;
        l = Math.max(RT_EDGE, rightPx - wpx);
        // 题名矩形 → 剖面列区间；数「该区间内比题名底更靠上的不透明像素」
        // ⚠️ minY 是按**原图列序**量的（canvas 直接 drawImage，没有翻转），而
        // imgLeft/drawW 是**显示方向**的映射。立绘水平翻转时（flip），屏幕 x 增大
        // = 原图列减小，列区间必须镜像 —— 漏掉这一步就去查了图片的另半边：牌面
        // 明明落在人物左侧的空白里，判据却在右侧对称位置数到一堆像素，于是「每列
        // 都算遮挡」（实测魔法师 hit=34，正好等于区间列数 34；字号被一路压到 15px
        // 下限，而实拍牌面底下是纯背景）。bandX 早就按 flip 取过头部左缘，这里
        // 是同一件事的另一半，漏了就成了两套坐标系混用。
        let cx0 = Math.max(0, Math.floor(((l - imgLeft) / drawW) * pf.w));
        let cx1 = Math.min(pf.w - 1, Math.ceil(((rightPx - imgLeft) / drawW) * pf.w));
        if (flip) {
          const mirrored = pf.w - 1 - cx1;
          cx1 = pf.w - 1 - cx0;
          cx0 = Math.max(0, mirrored);
        }
        const cy = ((visTop + hpx - imgTop) / drawH) * pf.h;
        hit = 0;
        for (let x = cx0; x <= cx1; x++) {
          if (pf.minY[x] < cy) hit++;
        }
        if (fits && !hit && rightPx - wpx >= RT_EDGE) { ok = true; break; }
      }
      return { fs: Math.max(RT_MIN_FS, f), left: l, hits: hit, ok, cap: capH, cw };
    }

    // 先按单列解（短名的常态，绝不能给它折列）。只有单列撑不住时才允许折 2 列重解：
    //   · 单列在下限字号都压到人（实测 12 字的「图书馆角落的温柔学姐」15px 仍 hit=7）
    //   · 单列虽然零遮挡，但已被逼到下限附近（13 字名 → 15px，太小）
    let sol = solve(0, 0);
    if (!sol.ok || sol.fs <= RT_MIN_FS + 1) {
      const bandTopFrac = Math.min(Math.max(rawTop, 0), 0.85);
      const bandH = Math.max(drawH * (HEAD_BAND - bandTopFrac), drawH * 0.10);
      let alt = solve(bandH, 2);
      if (!alt.ok) {                     // 2 列装不下（13 字这种）才退到 3 列
        const a3 = solve(bandH, 3);
        if (a3.ok) alt = a3;
      }
      const muchBigger = alt.ok && (!sol.ok || alt.fs > sol.fs + 2);
      if (muchBigger || (!sol.ok && alt.hits < sol.hits)) sol = alt;
    }

    nameEl.style.maxHeight = sol.cap > 0 ? Math.round(sol.cap) + "px" : "none";
    nameEl.style.width = sol.cw > 0 ? sol.cw + "px" : "";
    nameEl.style.setProperty("--rt-fs", sol.fs.toFixed(1) + "px");
    const plateX = Math.round(sol.left - appRect.left);
    const plateY = Math.round(visTop - appRect.top);
    nameEl.style.left = plateX + "px";
    nameEl.style.top = plateY + "px";
    // 缩放原点 = 立绘**渲染后**盒子的底边中点（双脚），换算到牌子自身的坐标系
    //（牌子的 border-box 左上角就是它的 inline left/top）。二阶段的凑近 / 脉动是
    // CSS 的 scale 属性（img 的原点是 50% 100%），牌子必须绕同一个点放大 —— 否则
    // 8% 的缩放会让牌面往人物身上挪十几像素，「头部左侧空白区」当场失效。
    // ⚠️ 直接量渲染后的 rect，不要用上面那套 trim 反算：反算只要有一项输入过期
    //（trim 还没到、换过图、盒宽与 paint 宽不等），原点就整体偏掉 —— 实测外卖窗口
    // 曾偏 13.5px，牌面放大时肉眼可见地往人物身上靠。量 rect 的话，每次重排都自愈。
    // 牌子自己已经被 scale 放大过，所以必须用未变换的布局位置（plateX/plateY）。
    const ib = img.getBoundingClientRect();
    nameEl.style.setProperty("--rt-ox", (ib.left + ib.width / 2 - (appRect.left + plateX)).toFixed(1) + "px");
    nameEl.style.setProperty("--rt-oy", (ib.bottom - (appRect.top + plateY)).toFixed(1) + "px");
    // 探针读这两个值做「零遮挡 / 字号达标」断言，不写死像素位置
    nameEl.dataset.rtHit = String(sol.hits);
    nameEl.dataset.rtFs = sol.fs.toFixed(1);
    // 定位成功才放出来（CSS 里 .dialog-name 默认 visibility:hidden）
    nameEl.dataset.rt = "1";
    return { left: sol.left, top: visTop, fs: sol.fs, hit: sol.hits };
  }

  function layoutSupDialog(figure, content, measure) {
    const container = figure?.parentElement ?? null;
    if (!container || !figure || !content) return;
    const cRect = container.getBoundingClientRect();
    if (cRect.width <= 0) return;
    // 先把提醒内容的字号定下来（可能改内容高度 → 下面所有 rect 都得在它之后量）
    fitDialogText(content);
    const figBox = figure.getBoundingClientRect();
    const figImg = figure.querySelector("img");
    const haveTrim = !!(figImg && figImg.dataset.trim);
    const nameEl = document.getElementById("dialog-name");
    // 锚点重算条件：窗口尺寸 / 立绘盒（含 left/top 位置）/ trim 是否就绪。
    // 位置进 key 是必须的：用户可拖动「立绘尺寸/水平/垂直」滑条、更换立绘、
    // 上传新图，都表现为 figure 盒变化，锚点必须跟着重算（不能写死）。
    // 二阶段只是对 img 做 scale 变换——后代变换不改 figure 布局盒，也不改
    // cRect 与 trim 存在性 → 不触发重算 → 倒计时位置在二阶段保持不动。
    const key = Math.round(cRect.width) + "x" + Math.round(cRect.height) + "|" +
      Math.round(figBox.left) + "," + Math.round(figBox.top) + " " +
      Math.round(figBox.width) + "x" + Math.round(figBox.height) + "|" + (haveTrim ? "T" : "t") +
      // 名字也进 key：换专员时可能只换名字不换图（或同名不同图），题名必须跟着重排
      "|" + (nameEl ? nameEl.textContent : "");
    let vis;
    const keyChanged = key !== lastLayoutKey && figBox.width > 0;
    if (keyChanged) {
      vis = figureVisibleRect(figure, figBox.top);
      frozenFig = vis;
      lastLayoutKey = key;
    } else {
      // key 未变（含二阶段缩放）时沿用冻结锚点；trim 未就绪的极早期兜底：
      // 用 figure 自身外框（会随 trim 到达、key 变化而校正）。
      vis = frozenFig || figureVisibleRect(figure, figBox.top);
    }
    // 题名位置依赖立绘的 alpha 剖面（循环里要反复改字号再强制布局）。若跟着 vis 一起
    // 只在 key 变化时算，会赶上「图还没解码完」那一轮——那时量不到剖面，属于「没算出来」
    // 而不是「算出没有」，缓存住就再也不重试，名字会一直不显示（竖屏实测踩到）。所以
    // 定位成功（写 data-rt）之前每轮都重试；成功后只在 key 变化时重算。
    if (nameEl && (keyChanged || !nameEl.dataset.rt)) {
      layoutRoleTitle(figure, nameEl, figBox);
    }
    const figLeft = clamp(vis.left - cRect.left, 0, cRect.width);
    const figRight = clamp(vis.right - cRect.left, 0, cRect.width);
    const figTop = clamp(vis.top - cRect.top, 0, cRect.height);
    // 立绘站位已由 CSS 的 --fig-* 变量定死，这里不再碰 figure/content 的定位。
    // 对话框（.content）与窗口边缘的偏移由 CSS 决定（当前贴边为 0），bar 的
    // 包含块就是它——所以摆位全部用实测 rect 换算，不假设贴边。两个锚点中点：
    //   水平 midX = (立绘可见右缘 + 窗口右缘) / 2
    //   垂直 midY = (立绘可见上缘 + 对话框上边缘) / 2（二者实测，偏移自动跟随）
    // bar 是 column flex（名称在上、环在下），环中心距 bar 底用 ring-wrap 的
    // offsetTop 实测反推，让环中心（而非 bar 中心）落在 midY 上。
    const bar = content.querySelector(".bar");
    const boxRect = content.getBoundingClientRect();
    const boxH = boxRect.height;
    const boxLeft = boxRect.left - cRect.left; // 对话框左缘相对窗口（= 水平内缩量）
    const boxBottomGap = cRect.bottom - boxRect.bottom; // 对话框底缘距窗底（= 底部内缩量）
    const boxTop = boxRect.top - cRect.top; // 对话框顶缘相对窗口
    const midX = (figRight + cRect.width) / 2;
    const midY = (figTop + boxTop) / 2;
    let cdX = midX - boxLeft;
    let cdBottom = cRect.height - midY - boxBottomGap;
    if (bar) {
      const ringWrap = bar.querySelector(".ring-wrap");
      const barW = bar.offsetWidth || 84;
      const ringH = (ringWrap && ringWrap.offsetHeight) || 84;
      const barH = bar.offsetHeight || ringH;
      // bar 是 column flex：提醒名称在环上（「提前结束」已经移出 bar、固定到窗口顶部）。
      // 用 ring-wrap 的实际布局位置（距 bar 顶的 offsetTop）反推「环中心距 bar 底」，
      // 这样无论环上下各放了什么，环中心都能精确落在 midY 上（名称移到环上方后，
      // 旧式「barH - ringH 当作环下占位」会把环整体抬高，故改为实测 offsetTop）。
      const ringFromTop = (ringWrap ? ringWrap.offsetTop : 0) + ringH / 2;
      const ringFromBottom = Math.max(0, barH - ringFromTop);
      // clamp 的合法区间是「bar 相对对话框」的坐标：窗口坐标的边界先算好再减去内缩量
      cdX = clamp(
        midX - boxLeft - barW / 2,
        CD_EDGE - boxLeft,
        Math.max(CD_EDGE - boxLeft, cRect.width - boxLeft - barW - CD_EDGE)
      );
      cdBottom = clamp(
        cRect.height - midY - ringFromBottom - boxBottomGap,
        CD_EDGE - boxBottomGap,
        Math.max(CD_EDGE - boxBottomGap, cRect.height - boxBottomGap - barH - CD_EDGE)
      );
      bar.style.left = `${Math.round(cdX)}px`;
      bar.style.bottom = `${Math.round(cdBottom)}px`;
      bar.style.right = "auto";
    }
    content.dataset.supMetrics = JSON.stringify({
      mode: "dialog",
      figLeft: Math.round(figLeft),
      figRight: Math.round(figRight),
      figTop: Math.round(figTop),
      cdX: Math.round(cdX),
      cdBottom: Math.round(cdBottom),
      midX: Math.round(midX),
      midY: Math.round(midY),
      src: haveTrim ? "trim" : "figure"
    });
    // 台词 / 提醒内容的垂直居中由 .content 的 flex（justify-content: center）负责，
    // 不再用 translateY 二次「对中」：那条老公式把文字挪到对话框的几何中心，与 CSS
    // 里「上边距小、下边距大」刻意的视觉偏上相互抵消，还会把文字推进下方内边距
    // （实测自定义提醒被推下 3px，看着像溢出）。
  }

  // ---- 对话框内「提醒内容」的统一自适应（短休 / 长休 / 外卖 / 自定义共用）----
  // break.js 与 takeout.js 里的这一段必须逐字相同：四个窗口的提醒内容是一套排版。
  // JS 只负责两件 CSS 做不到的事：
  //   1) 装不下就逐级缩字号（下限 --dlg-fs-min = 15px，再小就看不清了）；
  //   2) 缩到下限仍装不下，让对话框长高（.content.tall），绝不裁字。
  // 文字可用高度的算法见函数内注释；每次调用都先归零再算，结果只取决于窗口尺寸
  // 与文本本身，反复重排不会漂移。
  function cssLenPx(raw, base) {
    var n = parseFloat(raw);
    if (!(n > 0)) return 0;
    var s = String(raw).trim();
    if (/vh$/.test(s)) return (n * (base || window.innerHeight)) / 100;
    if (/vw$/.test(s)) return (n * window.innerWidth) / 100;
    return n;
  }
  function fitDialogText(content) {
    if (!content) return;
    // 取**可见**的那一个 .dialog-text：短休/长休/外卖用 .supervisor-line 说提醒内容，
    // 自定义提醒用 .messages 放自己的提醒内容，两者互斥显示（隐藏的那个高度是 0）。
    // 只取第一个匹配会拿到隐藏的 supervisor-line，直接 return —— 自定义提醒当时
    // 就是这么漏掉的（长文本溢出对话框 20px 没人管）。
    var els = content.querySelectorAll(".dialog-text");
    var el = null;
    for (var i = 0; i < els.length; i++) {
      if (els[i].offsetHeight > 0) { el = els[i]; break; }
    }
    if (!el) return;
    var cst = getComputedStyle(content);
    var padY = parseFloat(cst.paddingTop) + parseFloat(cst.paddingBottom);
    // 文字可用高度 = 对话框应有高度 − 上下内边距。四个窗口共用这一个公式：外卖框里
    // 还有徽标/按钮，它们照样占高，框会自己长高 —— 但那不影响「同一段文字在哪个
    // 窗口里字号都一样」，这正是「保持相同样式」要的（扣掉按钮高度反而会让外卖
    // 的同一句话比休息窗口小 5px，且长文本白缩一次、框照样长高）。
    var boxH = cssLenPx(cst.getPropertyValue("--dlg-h")) || 0.22 * window.innerHeight;
    var budget = boxH - padY;
    el.style.fontSize = "";
    content.classList.remove("tall");
    var minFs = cssLenPx(cst.getPropertyValue("--dlg-fs-min")) || 15;
    var fs = parseFloat(getComputedStyle(el).fontSize) || minFs;
    var guard = 0;
    while (el.offsetHeight > budget + 1 && fs > minFs + 0.01 && guard++ < 40) {
      fs = Math.max(minFs, fs - 1);
      el.style.fontSize = fs + "px";
    }
    if (el.offsetHeight > budget + 1) content.classList.add("tall");
  }

  function bindSupContentLayout(figure, content, measure) {
    const relayout = () => layoutSupDialog(figure, content, measure);
    if (!figure || !content) return relayout;
    relayout();
    const container = figure.parentElement;
    if (typeof ResizeObserver !== "undefined") {
      const ro = new ResizeObserver(relayout);
      ro.observe(figure);
      if (container) ro.observe(container);
      if (measure) ro.observe(measure);
      ro.observe(content);
    }
    if (measure && typeof MutationObserver !== "undefined") {
      new MutationObserver(relayout).observe(measure, {
        attributes: true,
        attributeFilter: ["style"]
      });
    }
    window.addEventListener("resize", relayout);
    // 题名的定位要读立绘的 alpha 剖面，立绘还没解码完时量不到 → 那一轮等于白跑。
    // ResizeObserver/MutationObserver 未必会再响一次（figure 高度是 vh 百分比，图片
    // 就绪并不改变它），所以除了监听 img 的 load，再补一串短定时兜底重排：只在
    // 「题名还没定位成功」时走，定位成功即刻停，不会常态轮询。
    const im = figure.querySelector("img");
    if (im) {
      if (im.complete && im.naturalWidth > 0) relayout();
      else {
        im.addEventListener("load", relayout, { once: true });
        im.addEventListener("error", relayout, { once: true });
      }
    }
    let tries = 0;
    const tick = () => {
      const n = document.getElementById("dialog-name");
      if (n && n.dataset.rt) return;          // 已定位，收工
      if (tries++ >= 16) return;              // 约 1.3s 还没成，放弃（极端情况名字不显示）
      relayout();
      window.setTimeout(tick, 80);
    };
    window.setTimeout(tick, 80);
    return relayout;
  }

  // renderer/break.ts
  var api = window.api;
  // 把「设置 → 快捷键」录入的组合键字符串（如 "Ctrl+Alt+X" / "Super+Shift+F1" /
  // "Ctrl+ "）解析成匹配器，让休息窗口的本地 keydown 与全局快捷键完全一致。
  // 解析失败（格式非法 / 缺修饰键）返回 null —— 对应「未配置」，本地不再兜底触发。
  function parseAccelerator(acc) {
    if (!acc || typeof acc !== "string") return null;
    const parts = acc.split("+");
    if (parts.length < 2) return null;
    let ctrl = false, alt = false, shift = false, meta = false;
    for (let i = 0; i < parts.length - 1; i++) {
      const m = parts[i].trim().toLowerCase();
      if (m === "ctrl") ctrl = true;
      else if (m === "alt") alt = true;
      else if (m === "shift") shift = true;
      else if (m === "super" || m === "meta" || m === "win") meta = true;
      else return null; // 不认识的修饰键 → 无法匹配
    }
    return { ctrl, alt, shift, meta, key: parts[parts.length - 1] };
  }
  function matchAccelerator(e, acc) {
    const p = parseAccelerator(acc);
    if (!p) return false;
    if (e.ctrlKey !== p.ctrl) return false;
    if (e.altKey !== p.alt) return false;
    if (e.shiftKey !== p.shift) return false;
    if (e.metaKey !== p.meta) return false;
    const ek = e.key.length === 1 ? e.key.toUpperCase() : e.key;
    return ek === p.key;
  }
  function renderMessage(text) {
    const p = document.createElement("p");
    p.textContent = text;
    return p;
  }
  // 粒子层：倒计时周围轻柔上浮的光点，渲染「放松」氛围。仅无督休专员模式启用
  // （督休专员模式由 CSS 隐藏画布）。纯装饰、不参与信息呈现，低透明度、低速。
  function startParticles(canvas, color) {
    try {
      const ctx = canvas.getContext("2d");
      if (!ctx) return;
      const dpr = window.devicePixelRatio || 1;
      const SIZE = 340;
      canvas.width = SIZE * dpr;
      canvas.height = SIZE * dpr;
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
      const N = 30;
      const rnd = (a, b) => a + Math.random() * (b - a);
      const ps = [];
      for (let i = 0; i < N; i++) {
        ps.push({
          x: rnd(0, SIZE), y: rnd(0, SIZE),
          r: i < 4 ? rnd(2.4, 3.8) : rnd(0.8, 2.8),
          vy: rnd(0.15, 0.6),
          a: rnd(0.15, 0.55), ph: rnd(0, Math.PI * 2)
        });
      }
      let t = 0;
      ctx.shadowColor = color;
      function tick() {
        t += 0.016;
        ctx.clearRect(0, 0, SIZE, SIZE);
        ctx.shadowBlur = 6;
        for (const p of ps) {
          p.y -= p.vy;
          p.x += Math.sin(t * 0.6 + p.ph) * 0.25;
          if (p.y < -4) { p.y = SIZE + 4; p.x = rnd(0, SIZE); }
          ctx.beginPath();
          ctx.arc(p.x, p.y, p.r, 0, Math.PI * 2);
          ctx.fillStyle = color;
          ctx.globalAlpha = p.a * (0.6 + 0.4 * Math.sin(t + p.ph));
          ctx.fill();
        }
        ctx.shadowBlur = 0;
        ctx.globalAlpha = 1;
        requestAnimationFrame(tick);
      }
      tick();
    } catch (e) { /* 装饰层异常不影响倒计时主体 */ }
  }
  // 蓄能容器：把倒计时当作一只「储存能量的空心容器」——
  // 起始为空，随着休息推进（剩余时间流逝 = 蓄能累积），内部液态能量自下而上
  // 上涨、表面波动，直至蓄满整个容器；容器壁外缘有能量溢出（光丝迸发 + 扩散脉冲）。
  var SVGNS = "http://www.w3.org/2000/svg";
  var WALL_R = 82;                        // 容器外壁半径（SVG 用户坐标）
  var LIQ_R = 78;                         // 液态能量裁剪半径（容器内部）
  var LIQ_BOTTOM = 100 + LIQ_R + 12;      // 液面以下填充延伸，保证裁满下半圆
  var targetFill = 0, currentFill = 0, energyRunning = false, lastEnergyT = 0;
  var liquidEl = null, liquidSurfEl = null, glossEl = null;
  var bubbles = [], wisps = [], pulses = [];
  var intakeStreaks = [], intakeRipples = [];

  function surfaceYOf(fill) { return 100 + LIQ_R - fill * 2 * LIQ_R; }

  // 液面：两条不同波长/相位的正弦叠加 → 液态波动
  function liquidWave(fill, x, t) {
    var sy = surfaceYOf(fill);
    var a1 = 3.0, a2 = 1.5;
    var k1 = (Math.PI * 2) / 72, k2 = (Math.PI * 2) / 41;
    return sy + a1 * Math.sin(k1 * x + t * 1.7) + a2 * Math.sin(k2 * x - t * 2.4);
  }
  function liquidPath(fill, t) {
    var x0 = 100 - LIQ_R - 2, x1 = 100 + LIQ_R + 2, d = "", step = 3, i = 0;
    for (var x = x0; x <= x1 + 0.001; x += step, i++) {
      d += (i === 0 ? "M" : "L") + x.toFixed(2) + "," + liquidWave(fill, x, t).toFixed(2) + " ";
    }
    d += "L" + (100 + LIQ_R + 2) + "," + LIQ_BOTTOM + " L" + (100 - LIQ_R - 2) + "," + LIQ_BOTTOM + " Z";
    return d;
  }
  function liquidSurfacePath(fill, t) {
    var x0 = 100 - LIQ_R - 2, x1 = 100 + LIQ_R + 2, d = "", step = 3, i = 0;
    for (var x = x0; x <= x1 + 0.001; x += step, i++) {
      d += (i === 0 ? "M" : "L") + x.toFixed(2) + "," + liquidWave(fill, x, t).toFixed(2) + " ";
    }
    return d;
  }
  function buildBubbles(group) {
    if (!group) return;
    for (var i = 0; i < 7; i++) {
      var c = document.createElementNS(SVGNS, "circle");
      var b = {
        el: c,
        x: 100 + (Math.random() * 2 - 1) * (LIQ_R - 12),
        y: 100 + Math.random() * LIQ_R,
        r: 1.4 + Math.random() * 2.4,
        vy: 10 + Math.random() * 16,
        ph: Math.random() * 6.28
      };
      c.setAttribute("cx", b.x.toFixed(1));
      c.setAttribute("cy", b.y.toFixed(1));
      c.setAttribute("r", b.r.toFixed(1));
      group.appendChild(c);
      bubbles.push(b);
    }
  }
  function updateBubbles(dt, surfaceY, t) {
    for (var i = 0; i < bubbles.length; i++) {
      var b = bubbles[i];
      b.y -= b.vy * dt;
      if (b.y <= surfaceY + 3) { b.y = LIQ_BOTTOM - 4; b.x = 100 + (Math.random() * 2 - 1) * (LIQ_R - 12); }
      var op = 0.55 * (1 - (b.y - surfaceY) / (LIQ_BOTTOM - surfaceY));
      b.el.setAttribute("cx", (b.x + Math.sin(t * 2 + b.ph) * 1.2).toFixed(1));
      b.el.setAttribute("cy", b.y.toFixed(1));
      b.el.setAttribute("opacity", Math.max(0.05, op).toFixed(2));
    }
  }
  function resetWisp(w, init) {
    w.angle = Math.random() * Math.PI * 2;
    w.rad = WALL_R - 3 + Math.random() * 4;
    w.speed = 12 + Math.random() * 22;
    w.maxLife = 1.1 + Math.random() * 1.4;
    w.life = init ? Math.random() * w.maxLife : 0;
    w.size = 1.6 + Math.random() * 2.6;
  }
  function buildOverflow(group) {
    if (!group) return;
    for (var i = 0; i < 16; i++) {
      var c = document.createElementNS(SVGNS, "circle");
      c.setAttribute("class", "overflow-wisp");
      c.setAttribute("r", "2");
      group.appendChild(c);
      var w = { el: c, angle: 0, rad: 0, speed: 0, life: 0, maxLife: 1, size: 2 };
      resetWisp(w, true);
      wisps.push(w);
    }
    for (var j = 0; j < 3; j++) {
      var p = document.createElementNS(SVGNS, "circle");
      p.setAttribute("class", "pulse-ring");
      group.appendChild(p);
      pulses.push({ el: p, life: j * 0.9, maxLife: 2.6, max: 46 });
    }
  }
  function updateOverflow(dt, fill) {
    var intensity = 0.18 + 0.82 * fill; // 蓄得越满，溢出越强
    for (var i = 0; i < wisps.length; i++) {
      var w = wisps[i];
      w.life += dt;
      if (w.life >= w.maxLife) resetWisp(w, false);
      w.rad += w.speed * dt;
      var k = w.life / w.maxLife;
      var op = Math.sin(Math.PI * k) * intensity;
      w.el.setAttribute("cx", (100 + w.rad * Math.cos(w.angle)).toFixed(1));
      w.el.setAttribute("cy", (100 + w.rad * Math.sin(w.angle)).toFixed(1));
      w.el.setAttribute("r", (w.size * (0.6 + 0.4 * Math.sin(Math.PI * k))).toFixed(2));
      w.el.setAttribute("opacity", Math.max(0, op).toFixed(3));
    }
    for (var j = 0; j < pulses.length; j++) {
      var p = pulses[j];
      p.life += dt;
      if (p.life >= p.maxLife) p.life = 0;
      var kk = p.life / p.maxLife;
      p.el.setAttribute("cx", "100");
      p.el.setAttribute("cy", "100");
      p.el.setAttribute("r", (WALL_R + p.max * kk).toFixed(1));
      p.el.setAttribute("stroke-width", (3 * (1 - kk)).toFixed(2));
      p.el.setAttribute("opacity", (0.5 * (1 - kk) * intensity).toFixed(3));
    }
  }
  // 液体颜色随背景（--accent）自动适配：同色相保证「协调」，提亮 + 高饱和保证「明显」。
  // 把推导出的提亮同源色写进 --liquid-accent（汲取光丝 / 吸收涟漪 / 液面高光共用），
  // 并直接改写液态渐变与气泡渐变的色标。
  function hexToRgb(hex) {
    if (!hex) return null;
    var h = String(hex).replace("#", "").trim();
    if (h.length === 3) h = h.split("").map(function (c) { return c + c; }).join("");
    if (h.length !== 6) return null;
    var n = parseInt(h, 16);
    if (isNaN(n)) return null;
    return { r: (n >> 16) & 255, g: (n >> 8) & 255, b: n & 255 };
  }
  function rgbToHsl(r, g, b) {
    r /= 255; g /= 255; b /= 255;
    var mx = Math.max(r, g, b), mn = Math.min(r, g, b), h = 0, s = 0, l = (mx + mn) / 2;
    if (mx !== mn) {
      var d = mx - mn;
      s = l > 0.5 ? d / (2 - mx - mn) : d / (mx + mn);
      if (mx === r) h = (g - b) / d + (g < b ? 6 : 0);
      else if (mx === g) h = (b - r) / d + 2;
      else h = (r - g) / d + 4;
      h /= 6;
    }
    return { h: h * 360, s: s, l: l };
  }
  function hsl(h, s, l) { return "hsl(" + h.toFixed(1) + ", " + (s * 100).toFixed(0) + "%, " + (l * 100).toFixed(0) + "%)"; }
  function applyAdaptiveLiquid(hex) {
    var rgb = hexToRgb(hex);
    if (!rgb) return;
    var c = rgbToHsl(rgb.r, rgb.g, rgb.b);
    var S = Math.min(1, Math.max(0.5, c.s + 0.12)); // 保证足够鲜艳
    var lg = document.getElementById("liquidGrad");
    if (lg) {
      var ss = lg.querySelectorAll("stop");
      var stops = [
        [0, hsl(c.h, Math.min(1, S + 0.08), 0.88)],
        [34, hsl(c.h, S, 0.66)],
        [72, hsl(c.h, S, 0.5)],
        [100, hsl(c.h, S, 0.36)]
      ];
      stops.forEach(function (st, i) {
        if (ss[i]) {
          ss[i].setAttribute("offset", st[0] + "%");
          ss[i].setAttribute("stop-color", st[1]);
        }
      });
    }
    // 提亮同源色：汲取光丝 / 涟漪 / 液面高光都走它，既协调又从暗背景里跳出来
    document.documentElement.style.setProperty("--liquid-accent", hsl(c.h, Math.min(1, S + 0.05), 0.8));
    var surf = document.getElementById("liquid-surface");
    if (surf) surf.style.stroke = hsl(c.h, Math.max(0, S - 0.2), 0.96);
    var bg = document.getElementById("bubbleGrad");
    if (bg) {
      var bs = bg.querySelectorAll("stop");
      if (bs[0]) bs[0].setAttribute("stop-color", hsl(c.h, 0.25, 0.98));
      if (bs[1]) bs[1].setAttribute("stop-color", hsl(c.h, S, 0.62));
    }
  }

  // 外界能量汲取：从四面八方径向射向容器壁，撞壁后迸出吸收涟漪并被「吸进」容器。
  // 强度随蓄能反向变化：越空吸得越猛，将满时收束。
  function resetIntake(w, init) {
    w.angle = Math.random() * Math.PI * 2;
    w.rad = WALL_R + 26 + Math.random() * 44;  // 从容器外缘之外射入
    w.speed = 30 + Math.random() * 36;          // 向内速度（px/s，SVG 用户单位）
    w.len = 7 + Math.random() * 8;              // 光丝拖尾长度（细锐，无箭头）
    w.life = init ? Math.random() : 0;          // 错峰：初始化时随机散布在途中
    if (init) w.rad = WALL_R + (1 - w.life) * 44; // 让初始散布在整段行程上
  }
  function buildIntake(group) {
    if (!group) return;
    // 密集的细锐光丝（无末端亮头/箭头），从四面八方射向容器壁、被吸收 —— 30 条
    for (var i = 0; i < 30; i++) {
      var ln = document.createElementNS(SVGNS, "line");
      ln.setAttribute("class", "intake-streak");
      group.appendChild(ln);
      var w = { ln: ln, angle: 0, rad: 0, speed: 0, len: 0, life: 0 };
      resetIntake(w, true);
      intakeStreaks.push(w);
    }
    for (var j = 0; j < 6; j++) {
      var rp = document.createElementNS(SVGNS, "circle");
      rp.setAttribute("class", "intake-ripple");
      rp.setAttribute("opacity", "0");
      group.appendChild(rp);
      intakeRipples.push({ el: rp, life: 0, max: 0.6 });
    }
  }
  function spawnRipple(angle) {
    for (var i = 0; i < intakeRipples.length; i++) {
      if (intakeRipples[i].life <= 0) { intakeRipples[i].life = intakeRipples[i].max; return; }
    }
  }
  function updateIntake(dt, fill) {
    var intensity = 0.22 + 0.78 * (1 - Math.min(1, Math.max(0, fill)));
    for (var i = 0; i < intakeStreaks.length; i++) {
      var w = intakeStreaks[i];
      w.rad -= w.speed * dt;
      if (w.rad <= WALL_R - 2) { spawnRipple(w.angle); resetIntake(w, false); continue; }
      var hx = 100 + w.rad * Math.cos(w.angle), hy = 100 + w.rad * Math.sin(w.angle);
      var tr = w.rad + w.len, tx = 100 + tr * Math.cos(w.angle), ty = 100 + tr * Math.sin(w.angle);
      w.ln.setAttribute("x1", tx.toFixed(1)); w.ln.setAttribute("y1", ty.toFixed(1));
      w.ln.setAttribute("x2", hx.toFixed(1)); w.ln.setAttribute("y2", hy.toFixed(1));
      // 临近壁时渐隐（被吸收），远离壁时全亮
      var op = intensity * Math.max(0, Math.min(1, (w.rad - WALL_R) / 16));
      w.ln.setAttribute("opacity", op.toFixed(3));
    }
    for (var k = 0; k < intakeRipples.length; k++) {
      var r = intakeRipples[k];
      if (r.life > 0) {
        r.life -= dt;
        var p = 1 - r.life / r.max;
        r.el.setAttribute("r", (WALL_R + 2 + p * 12).toFixed(1));
        r.el.setAttribute("stroke-width", (2.6 * (1 - p)).toFixed(2));
        r.el.setAttribute("opacity", (0.6 * (1 - p) * Math.max(0.3, intensity)).toFixed(3));
      } else {
        r.el.setAttribute("opacity", "0");
      }
    }
  }

  function buildEnergy(svg) {
    if (!svg) return;
    buildBubbles(svg.querySelector("#bubbles"));
    buildOverflow(svg.querySelector("#energy-overflow"));
    buildIntake(svg.querySelector("#energy-intake"));
    liquidEl = svg.querySelector("#liquid");
    liquidSurfEl = svg.querySelector("#liquid-surface");
    glossEl = svg.querySelector("#liquid-gloss");
  }
  function animateEnergy(now) {
    if (!energyRunning) return;
    var dt = Math.min(0.05, (now - lastEnergyT) / 1000);
    lastEnergyT = now;
    var t = now / 1000;
    currentFill += (targetFill - currentFill) * Math.min(1, dt * 2.4);
    if (Math.abs(targetFill - currentFill) < 0.0008) currentFill = targetFill;
    var sy = surfaceYOf(currentFill);
    if (liquidEl) liquidEl.setAttribute("d", liquidPath(currentFill, t));
    if (liquidSurfEl) liquidSurfEl.setAttribute("d", liquidSurfacePath(currentFill, t));
    if (glossEl) {
      glossEl.setAttribute("cy", (sy + 9).toFixed(1));
      glossEl.setAttribute("opacity", currentFill > 0.02 ? "0.22" : "0");
    }
    updateBubbles(dt, sy, t);
    updateOverflow(dt, currentFill);
    updateIntake(dt, currentFill);
    var wrap = document.querySelector(".ring-wrap");
    if (wrap) wrap.classList.toggle("energy-full", currentFill >= 0.995);
    requestAnimationFrame(animateEnergy);
  }

  async function main() {
    // 一次性揭示窗口：消除「先无立绘、后有立绘」闪烁——启用督休专员时整窗先透明，
    // 立绘就绪（load/error）后淡入；无立绘时立即显示。超时兜底防异常卡透明。
    let revealed = false;
    const reveal = () => {
      if (revealed) return;
      revealed = true;
      document.documentElement.style.opacity = "1";
    };
    // 会话拉取要能扛住「窗口已建、会话还在路上」的空档：主进程是**先写会话再建窗**，
    // 但若上一次预览的窗口正在关闭、或主进程正在换会话，首帧这一次可能读到 nil。
    // 此前直接 reveal + return，窗口就停在只剩背景的空壳上 —— 用户看到的就是
    // 「预览窗口不显示督休专员」，再点一次预览又好（那次读到了会话）。
    // 所以这里重试几次，真读不到才退回空窗（并保留兜底揭示）。
    let data = await api.breakCurrent();
    for (let i = 0; i < 5 && !data; i++) {
      await new Promise((r) => setTimeout(r, 120));
      data = await api.breakCurrent();
    }
    if (!data) { reveal(); return; }
    document.documentElement.style.setProperty("--accent", data.appearance.color);
    // 液体颜色随背景（accent）自适配：同色相协调、提亮高饱和明显
    applyAdaptiveLiquid(data.appearance.color);
    document.documentElement.style.setProperty("--app-opacity", String(1 - data.appearance.transparency));
    document.getElementById("app").dataset.rounded = data.appearance.fullscreen ? "0" : "1";
    const msgBox = document.getElementById("messages");
    for (const m of data.messages) msgBox.appendChild(renderMessage(m));
    // 提醒名称（短休/长休/外卖/自定义）显示在倒计时环外上方——督休专员模式
    // 同样显示（与无督休专员模式一致；它与对话框左上角的督休专员名字标签是两回事）
    const nameEl = document.getElementById("remind-name");
    if (nameEl) {
      nameEl.textContent = data.name || "";
      if (data.name) nameEl.classList.remove("hidden");
    }
    // 环内两行直排铭文「整理思绪 / 积蓄能量」已静态写在 break.html 的
    // #inscribe-lines 中（进度百分数下方，非弧形），无需在此设置。
    if (data.supervisor) {
      const isCustom = typeof data.type === "string" && data.type.indexOf("cb-") === 0;
      if (isCustom) {
        // 自定义提醒：督休专员只作背景立绘，不显示其台词（提醒内容）、不进入第二阶段；
        // 对话区只展示自定义提醒自己的「提醒内容」（messages 保持可见，不被隐藏）。
        setupSupervisor(data.supervisor, "", { showLine: false, allowEscalate: false });
      } else {
        msgBox.classList.add("hidden");
        setupSupervisor(data.supervisor, data.messages[0] ?? "");
      }
      bindSupContentLayout(
        document.getElementById("figure"),
        document.querySelector(".content"),
        document.getElementById("figure-trim")
      );
      // 立绘就绪后再揭示整窗（load/error 或已缓存 complete），避免空白对话框先闪现
      const figImg = document.getElementById("figure-img");
      if (figImg && figImg.complete && figImg.naturalWidth > 0) reveal();
      else if (figImg) {
        figImg.addEventListener("load", reveal, { once: true });
        figImg.addEventListener("error", reveal, { once: true });
      }
    } else {
      reveal();
    }
    const countdown = document.getElementById("countdown");
    const pctEl = document.getElementById("progress-pct");
    const total = data.durationMs;
    // 环内数字字号按「本次倒计时最多能有几位」定（系数表见 break.css 的 --cd-w）。
    // 必须用**总秒数的位数**、不是当前数字的位数：否则 100→99 会突然放大一档，
    // 读数每秒都在变胖变瘦。JS 没写这个属性时 CSS 按 2 位兜底。
    const ringWrap = document.querySelector(".ring-wrap");
    if (ringWrap && total > 0) {
      const digits = String(Math.ceil(total / 1000)).length;
      ringWrap.dataset.cdDigits = String(Math.min(5, Math.max(1, digits)));
    }
    // 生成「蓄能容器」几何：液态能量、气泡、外壁溢出光丝与脉冲
    buildEnergy(document.querySelector(".ring-wrap svg"));
    // 启动液态能量动画（独立于倒计时 tick，保证液面波动丝滑）
    energyRunning = true;
    lastEnergyT = performance.now();
    requestAnimationFrame(animateEnergy);
    let lastSec = -1;
    function frame() {
      const remain = Math.max(0, data.endsAt - Date.now());
      const sec = Math.ceil(remain / 1e3);
      // 每秒翻数：重触发弹跳高光动画，让倒计时有生命力
      if (sec !== lastSec) {
        lastSec = sec;
        countdown.textContent = String(sec);
        countdown.classList.remove("tick");
        void countdown.offsetWidth;
        countdown.classList.add("tick");
      }
      // 蓄能进度 = 流逝占比（容器从空到满）；中心「进度」与此同步增长
      targetFill = total > 0 ? 1 - remain / total : 0;
      if (pctEl && total > 0) pctEl.textContent = Math.round(targetFill * 100) + "%";
      if (remain <= 0) {
        targetFill = 1;
        if (pctEl) pctEl.textContent = "100%";
        countdown.textContent = "0";
        return;
      }
      requestAnimationFrame(frame);
    }
    frame();
    if (!data.supervisor) {
      const pcanvas = document.getElementById("particles");
      if (pcanvas) {
        try {
          const accent = (getComputedStyle(document.documentElement)
            .getPropertyValue("--accent") || "").trim() || "#4f7cff";
          startParticles(pcanvas, accent);
        } catch (e) { /* 装饰层异常不影响主体 */ }
      }
    }
    if (data.preview) {
      // 预览也复刻真实「满罐多停留 1 秒」的体验
      window.setTimeout(() => api.closeWindow(), Math.max(0, data.endsAt - Date.now()) + 1000);
    }
    const skip = document.getElementById("skip-link");
    // 顶部那个位置留给「预览模式 · 点击任意处关闭」：预览本来就点任意处关闭，再挂一个
    // 「提前结束」既重复、又会和 badge 叠在同一处。
    if (!data.preview) skip.classList.remove("hidden");
    // 文案随「设置 → 快捷键」的 endEarly 走：配置了就显示「提前结束（Ctrl+Alt+X）」，
    // 没配置（禁用）就只显示「提前结束」。
    skip.textContent = data.endEarlyShortcut
      ? `提前结束（${data.endEarlyShortcut}）`
      : "提前结束";
    skip.addEventListener("click", (e) => {
      e.preventDefault();
      if (data.preview) api.closeWindow();
      else api.action("endBreak");
    });
    window.addEventListener("keydown", (e) => {
      if (matchAccelerator(e, data.endEarlyShortcut)) {
        e.preventDefault();
        if (data.preview) api.closeWindow();
        else api.action("endBreak");
      }
    });
    if (data.preview) {
      document.getElementById("preview-badge").classList.remove("hidden");
      document.body.addEventListener("click", () => api.closeWindow());
    }
    // 兜底：任何异常导致立绘既没 load 也没 error 时，最多 4s 后强制揭示，
    // 避免窗口一直透明不可见。
    // 为什么不是 1.5s：1.5s 到点时立绘（webp + data URL）常常还在解码，窗口会先以
    // 「没有督休专员」的样子露出来 —— 正是用户报的「有时候预览窗里没有督休专员」。
    // 宁可变透明多等一会儿，也不要露出空窗。
    window.setTimeout(reveal, 4000);
  }
  main();
})();
