"use strict";
(() => {
  // renderer/figureTrim.ts
  var ALPHA_MIN = 8;
  var MAX_SAMPLE = 2048;
  var IGNORE = 8e-3;
  var MIN_CONTENT = 0.25;
  var MAX_SCALE = 4;
  // 兜底值必须**字段齐全**：applyFigureTrim 会无条件读 rawLeft/rawRight/rawTop/rawBottom
  // 写 dataset.trim。2026-11-18 实测：用户导入一张全透明 PNG（或 alpha 全低于
  // ALPHA_MIN）时 measureFigureTrim 走 bottom<0 的早退分支返回这里，缺字段会在
  // `t.rawLeft.toFixed(4)` 抛 TypeError，立绘 trim 永远写不进去、题名也不再定位。
  var NONE = { left: 0, right: 0, top: 0, bottom: 0, rawLeft: 0, rawRight: 0,
               rawTop: 0, rawBottom: 0, content: 1, scale: 1, transform: "none" };
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
  var GEOM_FALLBACK = { sizePct: 80, posXPct: 5, posYPct: 0, flip: false, showName: true };
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
    // 督休专员题名：竖排金色宋体，落在立绘颈部左侧的空白区（.dialog-name）。
    // 位置与字号由 layoutRoleTitle() 按 alpha 剖面实测写入；这里只填文本，并清掉
    // 上一轮的 data-rt —— 换专员后旧位置对新立绘是错的，重新定位成功前保持不可见。
    // 是否出场由 geom.showName 决定（缺字段 = 显示；设置页“立绘调节 → 显示名称”）。
    const g = sup.geom ?? GEOM_FALLBACK;
    const showName = g.showName !== false;
    const nameEl = document.getElementById("dialog-name");
    if (nameEl) {
      delete nameEl.dataset.rt;
      // 每次出场换一枚“章”：牌底轮廓/咬口/断裂一起换（同一个种子）
      rollSealSeed(nameEl);
      if (sup.name && showName) {
        nameEl.textContent = sup.name;
        nameEl.classList.remove("hidden");
      } else {
        nameEl.textContent = "";
        nameEl.classList.add("hidden");
      }
    }
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
      // “全局事件真的到达本页”，而不是靠 data-stage 反推（窗口内事件也会升级，会掩盖）。
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
    // “渲染后的像素”反解出来的，进去就全错。所以把同一套变换镜像一份写过去：
    // 同一个原点（立绘盒底边中点 = 双脚，见 layoutRoleTitle 写的 --rt-ox/--rt-oy）、
    // 同一组函数、同一顺序 ⇒ 数学上等价于“牌面焊在立绘上”：立绘怎么偏，牌面就怎么偏，
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
  // “立绘可见右缘~窗口右缘”与“立绘可见上缘~对话框上边缘”两个中点的交点上
  // （环中心对准；“提前结束”已固定在窗口顶部，不参与这里的摆位）。
  // 摆位结果写入 supMetrics 供探针断言。
  var CD_EDGE = 10;
  var frozenFig = null;          // 立绘可见边（去透明）的冻结锚点（窗口坐标）
  var lastLayoutKey = "";        // 仅在窗口/立绘盒（含位置）/trim 就绪变化时重算，避免二阶段缩放导致倒计时位移
  function clamp(v, lo, hi) {
    return Math.min(Math.max(v, lo), hi);
  }
  // 立绘“去掉透明部分后”的可见边缘（窗口坐标）——摆位锚点的唯一来源。
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
  // 立绘的“alpha 逐列剖面”。包围盒在这里不够用：立绘底部常有刀鞘/衣袍向左探出
  // （始皇帝包围盒左缘 44px，而头部那一段的最左不透明列在 148px），照包围盒摆名字
  // 会一头撞在人物身上。所以单独量一份 minY[x]（每列最上面的不透明像素行号）。
  // “头部段”= 图片顶部这一段。不能只看头顶那 16%：题名是竖排的，长一点的会向下
  // 伸到下颌/肩线附近，那里的内容（散落的发丝、肩甲）比头顶更靠左。按 16% 定的右缘
  // 到下面就会贴上去（竖屏“温柔学姐”实测 hit=1）。26% 能盖住题名的实际下探深度。
  var HEAD_BAND = 0.26;
  var PROFILE_W = 360;    // 剖面采样宽度：只用来找列，不需要高清
  // 题名避让用**实质遮挡**阈值：ALPHA_MIN(8) 太敏感，会把发丝边缘的抗锯齿（≈5%
  // 不透明度，压上去肉眼无感）也算成“有人”，于是把字号一路压到下限。32/255≈12.5%。
  var PROFILE_ALPHA = 32;
  var profileCache = new WeakMap();

  function measureHeadProfile(img) {
    // complete 必须一起判：dataURL/webp 在“数据到齐但还没解码”时 naturalWidth 就
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
    // solid = 逐像素的“不透明”位图（1 = alpha 达标）。为什么不能只用 minY：
    // 判断“牌被挡住多少”必须是**面积**，而 minY 只说“这一列最上面有东西”。
    // 细长物（举起的画笔、发丝、衣角）会让某一列的 minY 很高，但那一列下面大半是
    // 空的 —— 用“牌底 − minY”会把它当成整列被盖住。实测：画家举起的画笔斜穿过
    // 10 字牌的上角，逐列 minY 差值算出 38% 重叠，实际只压掉几十个像素，肉眼完全
    // 看不出遮挡。这笔内存（360×H ≈ 200KB，只缓存当前立绘）换的是判据不说谎。
    const minY = new Int32Array(cw).fill(ch);
    const solid = new Uint8Array(cw * ch);
    // rowLo / rowHi = 每一行最左 / 最右的不透明列（空行记 cw / -1）。
    // 2026-11-18 加：牌子挪到“头部以下”之后，横向上要贴的已经不是头部那一列了 ——
    // 必须按**牌子自己那一段纵向跨度**重新量人物左缘（颈以下谁都可能探出肩膀/手臂/
    // 刀鞘）。只留头部一条 headLeft 是旧锚点的口径，牌一往下就会量错。
    const rowLo = new Int32Array(ch).fill(cw);
    const rowHi = new Int32Array(ch).fill(-1);
    for (let y = 0; y < ch; y++) {
      for (let x = 0; x < cw; x++) {
        if (px[(y * cw + x) * 4 + 3] >= PROFILE_ALPHA) {
          solid[y * cw + x] = 1;
          if (minY[x] === ch) minY[x] = y;
          if (x < rowLo[y]) rowLo[y] = x;
          if (x > rowHi[y]) rowHi[y] = x;
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
    const profile = { w: cw, h: ch, minY, solid, rowLo, rowHi, headLeft, headRight };
    // 头部段整列为空时**绝不缓存**：正常立绘不会这样，出现它基本都是这次采样赶上了
    // 半解码。缓存下来的话后续重试永远拿到这份空剖面，名字就彻底不显示了。
    if (headLeft >= 0) profileCache.set(img, { src: img.src, profile });
    return profile;
  }

  var RT_GAP = 12;      // 题名右缘距“立绘在该跨度内的最左列”的**最小**留白
  // “舒适缝”：左侧有富余空间时把牌再往左让一点（用户 2026-11-18：「有一些名称离立绘
  // 头部太近了，有一种压迫感。如果左侧有很大的空间，可以适当往左一些」）。
  // 12px 对 26px 的字只有半个字宽，牌右缘几乎贴着鬓角，看着就是压在脸上；32px ≈ 1.2 字
  // 宽，眼睛能读出“牌和人是两件事”。上限不能再大：人到窗口左缘之间若很宽（宽屏、人物
  // 偏右），缝无上限会把牌推成一张飘在半空的标签，和人物失去隶属感。
  // ⚠️ 它不是“位置”而是**缝的上限**：每张立绘的落点仍由 alpha 剖面逐图反解（bodyLeft 由
  // 牌自身纵向跨度量出来），左侧放不下就自动退回 RT_GAP —— 没有任何 per-role 的常量，
  // 用户新上传的立绘走的是同一条路径。
  var RT_GAP_COMFORT = 32;
  var RT_EDGE = 6;      // 题名离窗口左缘的最小留白
  // 竖向锚点：牌**中心**落在“头顶到肩膀的中点”（2026-11-18 用户第二次改）。
  // 第一次锚的是**牌顶**（0.28），而牌高约占可见高度 15%，于是牌心实际落到 37% ≈ 胸口，
  // 看着“能被放多低就放多低、离头太远”；用户要的是整块名称的中心对上头顶—肩线的一半。
  //
  // 0.17 怎么来的：15 张内置立绘逐张量轮廓，肩线（行宽首次 ≥ 头宽×1.12）占可见高度
  // 27.6%~42.2%，中位数 33.1%，取一半 = 16.5%，圆到 0.17。
  // 试过逐图检测肩线（把牌心锚在每个立绘自己的肩上），弃用：判据依赖“头宽”，而帽子
  // （魔法师帽檐比肩还宽）、斗篷、举起的画笔会让头宽量错，实测逐图值与固定值只差 10px
  // 上下 —— 换来的是“某些专员偏高、某些偏低”的不一致，不如一条比例整齐。
  // 放不下时牌会往上让（见 measure 里的 toeUp），那时牌心随之上移。
  var RT_MID_RATIO = 0.17;
  var RT_TOP_STEP = 8;  // 往上让的步长（px）
  var RT_MIN_FS = 15;   // 字号下限：1x 屏 14px 已发虚，15 是能读的底线
  var RT_MAX_FS = 26;
  var RT_STEP = 0.5;
  // 允许牌与立绘**少量**重叠的上限（牌在立绘之上，重叠处压住的是袖口/肩线，见
  // .dialog-name 的 z-index 说明）。口径 = 逐像素统计：牌矩形内有多少不透明立绘像素 /
  // 矩形面积。
  // ⚠️ 这是**同一档字号内的第二选择**：先问零遮挡，不成立才允许压一点（见 ladder）。
  // 2026-11-18 牌心移到头肩之间后，牌右缘贴着的是脸颊/发缘，那一侧的留白比肩膀旁边
  // 还紧 —— 零遮挡往往要靠缩字号换来。26px 压 5~20% 鬓角，比缩成 20px 的“一根针”
  // 好看，所以顺序是“先保住字号，再谈压多少”，但仍不允许压成“牌的一半埋在人物里”。
  // 对 26px 字的牌，20% 约等于被吃掉 10px 宽的一条，字形仍完整（牌在人物之上）。
  var RT_OVERLAP_RATIO = 0.20;

  // 印泥：牌子不再用预制 SVG。预制件 viewBox 恒为 0 0 100 200，靠
  // preserveAspectRatio="none" 铺满盒子 —— 短名 50×88 时缩放 0.50/0.44 基本等比，
  // 长名 37×418 时却是 0.37/2.09（各向异性 5.6 倍）：毛边噪声被纵向拉成 ~50px 波长
  // 的"橡皮筋"。现在按牌子的**实际像素尺寸**现场生成（seal.js）。这里只定"这一枚
  // 章的种子"，画在 applySealInk 里做 ——
  // 真尺寸要等 layoutRoleTitle 量完字号才有。
  //
  // 兜底也不再挂预制图（2026-11-17 撤掉 12 个 SVG）：牌子的可见性由 data-rt 门控，
  // 正常路径下预制件连一帧都渲染不出来，只有 seal.js 整个缺失时才会露；而为这条
  // 不可达的路径养 12 个文件 + 一处 404 风险不划算。现在兜底是 CSS 里 --rt-ink 的
  // fallback（纯色暗朱墨块），零字节、不依赖 IO，也就没有了“指向不存在文件”的路。
  function rollSealSeed(nameEl) {
    if (!nameEl) return;
    // 每次出场换一枚章（"同一枚章连盖两次也不该一模一样"）。种子用随机 32 位，
    // 现场生成的形状因此不是有限几档。
    nameEl.dataset.sealSeed = String(1 + Math.floor(Math.random() * 4294967000));
    // 尺寸缓存作废：换章了，同尺寸也得重画
    delete nameEl.dataset.sealKey;
  }

  // 真尺寸出来之后再画墨块。牌子宽高 = padding + 字号，字号没定就量不到。
  function applySealInk(nameEl, w, h) {
    if (!nameEl) return;
    var G = window.SealGen;
    if (!G || !G.applyTo) return;                      // 没加载就停在 CSS 兜底墨块上
    G.applyTo(nameEl, w, h, Number(nameEl.dataset.sealSeed || 0) >>> 0);
  }

  // 把名字切成**字素簇**（一个“看起来的字”），用于中间省略。
  // 为什么不能用 [...name]：那只是按码点切。家庭 emoji「👨‍👩‍👧‍👦」是 7 个码点，
  // 国旗「🇨🇳」是 2 个区域指示符，组合音标「é」是 e + U+0301 —— 按码点截断会把
  // 它们劈成单人、半个国旗（渲染成一个字母）、孤立声调符，牌面上就是一堆乱码。
  // Intl.Segmenter 在 WebView2（Chromium 87+）都有；拿不到就退回码点切，
  // 至少不会把代理对劈成两半。
  var graphemeSeg = null;
  function splitGraphemes(s) {
    if (!graphemeSeg && typeof Intl !== "undefined" && Intl.Segmenter) {
      try { graphemeSeg = new Intl.Segmenter(undefined, { granularity: "grapheme" }); }
      catch (e) { graphemeSeg = null; }
    }
    if (graphemeSeg) {
      try {
        return Array.from(graphemeSeg.segment(s), function (x) { return x.segment; });
      } catch (e) { /* 落到下面的码点切 */ }
    }
    return Array.from(s);
  }

  // 把题名钉到“立绘头肩之间、人物左侧的空白区”，按立绘 alpha 剖面反解字号与贴法。
  // 竖向（2026-11-18 第二次改）：牌**中心**落在立绘**可见高度的 17%** 处 —— 也就是
  // “头顶到肩膀的中点”（用户口径：整块名称的中间对齐这个位置，不许离头太远）。
  // 上一版把这个位置当成牌**顶**来用，牌高占可见高度约 15%，于是牌中心实际掉到 37%
  // （胸口），看着像“能放多低就放多低”。横向上“贴哪一列”按**牌子自己那段跨度**量
  // 人物最左列：牌现在横跨脸侧，贴的对象是头/发，不是肩膀。
  // 只有先钉住右缘再一路缩字号，才能同时保证“不压人”和“不出窗”——固定字号再找位置，
  // 长名字必然二选一失败。
  //
  // 2026-11-17 定的一条硬规则：**绝不拆成多列**。名字超出可用空间时只允许两种降级：
  //   ① 缩字号（26 → 15，15 是 1x 屏宋体的可读下限）；
  //   ② 15px 仍装不下 → 对**显示文本**做中间省略（落盘名不变，只影响牌面）。
  // 旧版是“缩不动就折 2~3 列”，实测把 42 字名横拉成 290×147 的扁饼、直接压住兜帽，
  // 而且那句 hit 断言还测不出来（见下面 measure() 里“区间右界”的说明）。
  function layoutRoleTitle(figure, nameEl, box, maxBottom) {
    if (!figure || !nameEl || nameEl.classList.contains("hidden")) return null;
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
    // “图片绘制区在页面里的绝对位置”，好把剖面坐标反算回去。
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

    // 原名单独存一份：textContent 会被省略改写，重排 key、二次布局都必须拿到原名，
    // 否则“省略后的短名”会被当成新名字，一轮轮越排越短。
    const fullName = nameEl.dataset.fullName || nameEl.textContent || "";
    if (!fullName) return null;
    nameEl.dataset.fullName = fullName;
    const chars = splitGraphemes(fullName);

    // 牌底上界 = 对话框顶（由 layoutSupDialog 实测传入）：牌是一列往下长的，
    // 没有这条的话 30 字在 720 高的窗口里会一路压到对话框上。拿不到就退窗口底。
    const bottomLimit = maxBottom > 0 ? maxBottom - RT_GAP : appRect.bottom - 8;

    // 中间省略：保留首尾，省略号占一格；前半多留一个字（识别信息通常在前面）。
    function elide(keep) {
      if (keep >= chars.length) return fullName;
      if (keep <= 1) return "…";
      const head = Math.ceil((keep - 1) / 2);
      const tail = Math.max(0, keep - 1 - head);
      return chars.slice(0, head).join("") + "…" +
        (tail > 0 ? chars.slice(chars.length - tail).join("") : "");
    }

    // 立绘**可见区**的下缘与高度。竖向锚点按“可见高度”取比例：trim 之后盒子比人物高
    // （透明边距），拿盒子高算比例会把锚点整体压低一截。
    const visBottom = Math.min(box.bottom, appRect.bottom);
    const visH = Math.max(1, visBottom - visTop);
    // 目标**牌心**：立绘**可见高度**的 RT_MID_RATIO —— 头顶到肩线的中点（用户 2026-11-18
    // 的口径：“整个名称的中间位于头顶到肩膀中间，不要离开头部太远”）。
    // 牌顶由它反推（wantMid - 半牌高）：字号越大牌顶越高、牌心不动，长名也就不会因为
    // “往下长”把中心甩到胸口。放不下时牌往上让，牌心随之上移（见 measure 的 toeUp）。
    const wantMid = visTop + visH * RT_MID_RATIO;

    // “牌这一竖条里，立绘最左到哪”——按**屏幕方向**取跨度内所有行的最左不透明列
    // （flip 时镜像），返回 CSS px；整段都是空白则返回 null。
    // 为什么不能沿用头部那一列：牌挪到颈以下后，肩膀/手臂/刀鞘都比头更靠左，
    // 贴着 headLeft 摆会整条压进人物身上。
    function bodyLeftIn(topCss, botCss) {
      const y0 = Math.max(0, Math.floor(((topCss - imgTop) / drawH) * pf.h));
      const y1 = Math.min(pf.h - 1, Math.ceil(((botCss - imgTop) / drawH) * pf.h));
      let xMin = pf.w;
      for (let y = y0; y <= y1; y++) {
        const lo = pf.rowLo[y];
        if (lo >= pf.w) continue;                    // 整行空
        const x = flip ? pf.w - 1 - pf.rowHi[y] : lo;
        if (x < xMin) xMin = x;
      }
      return xMin === pf.w ? null : imgLeft + (xMin / pf.w) * drawW;
    }

    // 左缘下界。一阶段/二阶段的“凑近 + 脉动”是 CSS scale（原点 = 立绘脚底中点），牌被
    // 一起放大后会往窗口外撑：贴着窗口边的牌会被裁掉一截字形。所以把“放大后左缘仍在
    // 窗内”反解成布局坐标下的最小 left。
    // 缩放系数取“当前值”与**峰值**中的大者：排版时可能还在一阶段（1.03），之后切到
    // 二阶段（1.08）或脉动峰值（1.095）时牌会再往左甩 5~10px，不按峰值预留就会露出去。
    // ⚠️ 峰值与 break.css 的 `#app[data-stage='2'] ... scale: 1.08 1.1016` 和
    // `@keyframes figure-pulse 50% { scale: 1.095 ... }` 同源，改那边要同步这里。
    // 推导：渲染左缘 = left·sx + footX·(1-sx) ≥ RT_EDGE ⟹ left ≥ (RT_EDGE + footX(sx-1))/sx
    const RT_SCALE_PEAK = 1.10;
    const ib0 = img.getBoundingClientRect();
    const footX = ib0.left + ib0.width / 2 - appRect.left;
    const sxNow = parseFloat(getComputedStyle(nameEl).scale);
    const sx = Math.max(isFinite(sxNow) && sxNow > 0 ? sxNow : 1, RT_SCALE_PEAK);
    const leftMin = Math.max(RT_EDGE, (RT_EDGE + (sx - 1) * footX) / sx);

    // 量一次：写文本 + 字号 → 自然尺寸（单列，不写 max-height/width）→ 三条判据。
    // 注意 order：每次都要先把 left/top 钉回 0 再量，shrink-to-fit 元素否则量到的
    // 是上一轮的字号（74 当 35 那种）。
    //
    // mode 是牌的**贴法**，只有两种：
    //   "head" = 右缘贴在离“自身跨度内立绘最左列”一段缝的位置。缝取两档：左侧空间
    //            富余时是 RT_GAP_COMFORT（32，不贴脸），放不下退回 RT_GAP（12）。短名
    //            的常态；
    //   "edge" = 左缘贴窗口（RT_EDGE）。一列窄牌离人物更远，换来的是一整条竖向空间。
    // 为什么需要两种：单列牌是**往下长**的，贴着人物时往下一二十像素就会碰到肩膀/手臂。
    // 实测内置专员“图书馆角落的温柔学姐”（10 字）在 head 模式下全字号都 hit>0，只能
    // 截成 7 字；退到人物外缘（窗口左侧）就能整名放下。先 head 后 edge，取字号大的那个
    // —— 短名永远不会被赶到窗边去。
    function measure(f, text, mode, limit, allowToe) {
      if (nameEl.textContent !== text) nameEl.textContent = text;
      nameEl.style.setProperty("--rt-fs", f + "px");
      nameEl.style.left = "0px";
      nameEl.style.top = "0px";
      nameEl.style.maxHeight = "";
      nameEl.style.width = "";
      void nameEl.offsetWidth;
      const wpx = nameEl.offsetWidth;
      const hpx = nameEl.offsetHeight;
      if (!wpx || !hpx) return null;
      const stick = mode === "edge";

      // 判据 ②：牌矩形内被立绘覆盖的**面积占比**（0 = 完全不碰）。
      // ⚠️ 区间右界必须用**牌的真实右缘**（left + wpx），不能用锚点：牌比空白区
      // 宽的时候 left 会被 RT_EDGE 钳在左边，牌实际一直伸到 left + wpx，超出锚点的
      // 那一段（正是压脸的那一段）若不算进区间，hit 就会报 0 —— 2026-11-17 实测
      // 42 字名牌宽 290px 压住兜帽，hit 仍为 0，就是踩的这里。
      // ⚠️ minY/solid 是按**原图列序**量的（canvas 直接 drawImage，没有翻转），而
      // imgLeft/drawW 是**显示方向**的映射。立绘水平翻转时（flip），屏幕 x 增大
      // = 原图列减小，列区间必须镜像 —— 漏掉这一步就去查了图片的另半边：牌面
      // 明明落在人物左侧的空白里，判据却在右侧对称位置数到一堆像素，于是“每列
      // 都算遮挡”（实测魔法师 hit=34，正好等于区间列数 34）。bandX 早就按 flip
      // 取过头部左缘，这里是同一件事的另一半，漏了就成了两套坐标系混用。
      // 口径必须是**逐像素面积**，不能只拿列的 minY 做差：细长物（举起的画笔、发丝）
      // 会让某一列“最上面有东西”却只占几个像素，按列差值算会把一整列都记成被盖住
      // （实测把 0.5% 的遮挡算成 38%）。
      function overlapAt(top, left) {
        let cx0 = Math.max(0, Math.floor(((left - imgLeft) / drawW) * pf.w));
        let cx1 = Math.min(pf.w - 1, Math.ceil(((left + wpx - imgLeft) / drawW) * pf.w));
        if (flip) {
          const mirrored = pf.w - 1 - cx1;
          cx1 = pf.w - 1 - cx0;
          cx0 = Math.max(0, mirrored);
        }
        const y0 = Math.max(0, Math.floor(((top - imgTop) / drawH) * pf.h));
        const y1 = Math.min(pf.h - 1, Math.ceil(((top + hpx - imgTop) / drawH) * pf.h));
        const solid = pf.solid;
        let hit = 0, cols = 0;
        for (let x = cx0; x <= cx1; x++) {
          let colHit = 0;
          for (let y = y0; y <= y1; y++) {
            if (solid[y * pf.w + x]) colHit++;
          }
          if (colHit) { cols++; hit += colHit; }
        }
        const area = Math.max(1, (cx1 - cx0 + 1) * (y1 - y0 + 1));
        return { cols, ratio: y1 >= y0 ? hit / area : 0 };
      }

      // 牌顶下界：**窗口上缘**，不再是立绘可见上缘。
      // 2026-11-18 第三次改：牌心钉在头肩中点后，长牌（10 字 ≈ 314px，是可见高度
      // 576 的一半多）若还要求牌顶不高于头顶，就只能在“牌心掉到胸口”和“把名字截短”
      // 之间二选一。用户的口径是**中心**对齐头肩中点，所以允许长牌向上伸进头顶上方
      // 的空白（那里本来就只有背景），只守住窗口上缘；牌底仍不许压对话框。
      // 短牌（≤6 字）的牌顶本来就低于头顶，这条改动对它们没有影响。
      // 立绘被推出窗口时 visTop 可能是负数，窗口上缘仍是 0（app 顶边）；
      // 判据 ① 允许裁掉牌子的上内边距（那截墨块不带字），但不许裁到字形。
      const topFloor = Math.max(0, appRect.top);

      // 一次“落位 + 量遮挡”：竖向位置决定纵向跨度 → 跨度决定贴哪一列 → 列决定遮挡。
      // 三者互相依赖，所以每次试探都整组重算。
      function at(top) {
        const bl = bodyLeftIn(top, top + hpx);
        // 锚点同时受两头上限约束：不能越过头部左侧空白（rightPx），也不能越过
        // 自己这段跨度里人物最左的那一列（bl - RT_GAP）。
        const anchor = bl == null ? rightPx : Math.min(rightPx, bl - RT_GAP);
        // 先按**最小缝**求一个可行位：这一步决定字号能多大，不能被“好看”干扰。
        const leftSnug = stick ? leftMin : Math.max(leftMin, anchor - wpx);
        // 再试**舒适缝**：左侧空间够就整块往左挪到“牌右缘离人物 RT_GAP_COMFORT”。
        // 前提是挪完左缘仍在 leftMin 之右 —— 放不下就原地不动，绝不为了拉开距离去
        // 贴窗口边（scale 后会裁字）。挪左只会让重叠变少，所以不会把已经成立的字号
        // 挤掉；反过来，原本压着鬓角的档位可能因此变成零遮挡，字号还能更大。
        let left = leftSnug;
        if (!stick && bl != null) {
          const wide = bl - wpx - RT_GAP_COMFORT;
          if (wide >= leftMin && wide < left) left = wide;
        }
        return { top, left, ov: overlapAt(top, left) };
      }

      // 竖向落位：牌**中心**钉在 wantMid 上（用户 2026-11-18 的口径），牌顶因此等于
      // wantMid - 半牌高 —— 同一个锚点对 2 字牌和 10 字牌都成立，长名不会因为“往下长”
      // 而把中心甩到胸口。牌顶可以伸到头顶上方（长牌居中时必然如此），下界是窗口上缘；
      // 牌底不许压对话框（让到 bottomLimit 之上；让不下就缩字号/省略）。
      let topPx = Math.min(wantMid - hpx / 2, bottomLimit - hpx);
      if (topPx < topFloor) topPx = topFloor;
      let cur = at(topPx);
      let toeUp = false;
      // 往上让：只有当调用方允许（allowToe，说明目标锚点上所有字号都不成立）时才走。
      // 方向只能是**向上**：牌心往下走就到胸口、肩线，人物在那一段更宽（肩、手臂、
      // 刀鞘），左侧留白反而没了 —— 那正是用户这次要改掉的观感。往上让到头部那档，
      // 头发通常比肩窄，留白更多，代价是牌心比锚点高一点（探针记 data-rt-toe）。
      // 步长 8px、上限二十来步，比一次排版便宜得多。
      while (allowToe && cur.ov.ratio > limit && cur.top > topFloor + 0.5) {
        topPx = Math.max(topFloor, cur.top - RT_TOP_STEP);
        cur = at(topPx);
        toeUp = true;
      }
      const left = cur.left;
      // 判据 ①：整块留在窗口内、不压对话框、不裁字形。
      // 上界用 topPx + padTop：立绘被推出窗口时允许裁掉牌子的**上内边距**（那截是墨块
      // 上缘，不带字），但不许裁到字形 —— 字形被切一半比没有名字更难看，也让“竖排
      // 金字”认不出来。裁到字形就判不可行，退到更小的字号/省略，最终宁可整块不显示。
      const fits = left + wpx <= appRect.width - RT_EDGE &&
        topPx + padTop >= -RT_TOP_CLIP &&
        topPx + hpx <= bottomLimit;
      // 判据 ②：重叠面积占比不超过 limit。limit = 0 表示“完全不许碰”（第一轮），
      // 只有第一轮所有字号都不成立时才会用 RT_OVERLAP_RATIO 再试一轮（第二轮）。
      const ok = fits && cur.ov.ratio <= limit;
      return { fs: f, left, wpx, hpx, hits: cur.ov.cols, ratio: cur.ov.ratio,
               top: topPx, ok, text, mode, toeUp };
    }

    const MODES = ["head", "edge"];
    // 立绘被滑条推出窗口时（高度 100% / 距底 40%），牌面上缘会跟着头部出窗。
    // 允许裁掉牌子的上内边距（不带字的那截墨块），最多再吃 4px 字形抗锯齿边 ——
    // 再多就是“字被切一半”，宁可整块不显示。
    const padTop = parseFloat(getComputedStyle(nameEl).paddingTop) || 0;
    const RT_TOP_CLIP = 4;
    // 扫字号阶梯：两种贴法各试一次，取字号大的（head 已经拿到 26px 就不必再试 edge）。
    // 同一档字号内**先问零遮挡、再问少量遮挡** —— 这样“能零遮挡就一定零遮挡”的原则不变，
    // 但不会为了零遮挡把字号一路缩到 15（2026-11-18：牌心既然钉在头肩中点，右侧留白
    // 本来就比肩旁窄，若坚持零遮挡只能靠缩字号，字形会明显小一圈）。
    // allowToe=false 时牌心锁在目标锚点上（用户要的位置），true 才允许往上让。
    function ladder(text, allowToe) {
      let win = null;
      for (let i = 0; i < MODES.length; i++) {
        for (let f = RT_MAX_FS; f >= RT_MIN_FS - 1e-6; f -= RT_STEP) {
          let m = measure(f, text, MODES[i], 0, allowToe);
          if (!m || !m.ok) {
            m = measure(f, text, MODES[i], RT_OVERLAP_RATIO, allowToe);
            // 只有**真的压到了人物**才打 relaxed 标记：这一次尝试用的是放宽的阈值，但
            // 稀有字/emoji 的回落字体在两次调用之间可能刚解析完、尺寸变小，同一个字号
            // 第二次量出来 ov 可能就是 0 —— 那属于“本来就零遮挡”，不该记成“放宽了”。
            // 阈值取 0.1%：rtOv 只保留三位小数，更小的重叠会打成 "0.000"，探针读到的
            // 就是“放宽了却零遮挡”这种自相矛盾的组合。
            if (m) m.relaxed = m.ok && m.ratio >= 0.001;
          }
          if (m && m.ok) {
            if (!win || m.fs > win.fs) win = m;
            break;
          }
        }
        if (win && win.fs >= RT_MAX_FS - 1e-6) break;
      }
      return win;
    }

    // 求解顺序（2026-11-18 改）：**先守住竖向锚点，再谈字号**。
    //   ① 牌心锁在头肩中点上求最大字号；
    //   ② 这个位置全档都不成立，才允许牌往上让（牌心跟着上移，探针记 data-rt-toe）。
    // 旧顺序是“先零遮挡、再缩字号”，于是宁可把牌顶退回人物头顶也不肯让它压一点人物 ——
    // 结果就是牌子永远挂在脸边，正是这次用户要改的东西。
    function best(text) {
      return ladder(text, false) || ladder(text, true);
    }

    let sol = best(fullName);
    let relaxed = !!(sol && sol.relaxed);
    // ② 全名放不下 → 15px 下逐字递减求“能放下的最大字数”（省略号也算一格）。
    //    先用最省空间的姿态（贴窗口 + 允许让位 + 允许遮挡）探一次“这个字数有没有戏”，
    //    有戏再交给 best() 按上面那套顺序把字号抬回来 —— 否则长名永远停在 15px 的
    //    “一根针”上，白省了字数。
    if (!sol) {
      for (let keep = chars.length - 1; keep >= 3; keep--) {
        const m = measure(RT_MIN_FS, elide(keep), "edge", RT_OVERLAP_RATIO, true);
        if (m && m.ok) {
          sol = best(elide(keep));
          if (sol) { relaxed = !!sol.relaxed; break; }
        }
      }
    }
    // 连“首…尾”都放不下（窗口极窄 / 立绘贴左）：不写 data-rt，牌子保持 CSS 的
    // visibility:hidden。宁可没有名字，也不画一块盖在脸上的红印。
    if (!sol) {
      nameEl.textContent = fullName;
      nameEl.style.maxHeight = "";
      nameEl.style.width = "";
      delete nameEl.dataset.rt;
      delete nameEl.dataset.rtRelax;
      delete nameEl.dataset.rtOv;
      return null;
    }

    nameEl.style.setProperty("--rt-fs", sol.fs.toFixed(1) + "px");
    const plateX = Math.round(sol.left - appRect.left);
    // 用 sol.top 而不是 wantMid - hpx/2 现算：牌底压对话框 / 牌顶高过可见上缘 / 目标
    // 锚点全档放不下时牌会往上让（measure 的 toeUp），让到哪就是 sol.top。
    const plateY = Math.round(sol.top - appRect.top);
    nameEl.style.left = plateX + "px";
    nameEl.style.top = plateY + "px";
    // 缩放原点 = 立绘**渲染后**盒子的底边中点（双脚），换算到牌子自身的坐标系
    //（牌子的 border-box 左上角就是它的 inline left/top）。二阶段的凑近 / 脉动是
    // CSS 的 scale 属性（img 的原点是 50% 100%），牌子必须绕同一个点放大 —— 否则
    // 8% 的缩放会让牌面往人物身上挪十几像素，“头部左侧空白区”当场失效。
    // ⚠️ 直接量渲染后的 rect，不要用上面那套 trim 反算：反算只要有一项输入过期
    //（trim 还没到、换过图、盒宽与 paint 宽不等），原点就整体偏掉 —— 实测外卖窗口
    // 曾偏 13.5px，牌面放大时肉眼可见地往人物身上靠。量 rect 的话，每次重排都自愈。
    // 牌子自己已经被 scale 放大过，所以必须用未变换的布局位置（plateX/plateY）。
    const ib = img.getBoundingClientRect();
    nameEl.style.setProperty("--rt-ox", (ib.left + ib.width / 2 - (appRect.left + plateX)).toFixed(1) + "px");
    nameEl.style.setProperty("--rt-oy", (ib.bottom - (appRect.top + plateY)).toFixed(1) + "px");
    // 探针读这几个值做“重叠占比 / 字号达标 / 省略合规”断言，不写死像素位置。
    // rtHit 保留“被覆盖的列数”只作诊断；真正的判据是 rtOv（面积占比，0~1）。
    nameEl.dataset.rtHit = String(sol.hits);
    nameEl.dataset.rtOv = sol.ratio.toFixed(3);
    // 横向缝的实测值（px）：rtGap = 牌右缘到“牌这一竖条里人物最左列”的距离，
    // rtSlack = 左侧还能腾出多少（贴到 leftMin 且不改字号时的上限）。
    // 两个数一起才能判断缝是“被空间卡住”还是“主动让开的舒适缝”（slack 够时
    // gap 应当达到 RT_GAP_COMFORT）。探针据此查“有空间却没让开”的回归，
    // 不写死任何像素位置 —— 立绘、窗口尺寸、字号一变，这两个数自己会跟着变。
    const blFinal = bodyLeftIn(sol.top, sol.top + sol.hpx);
    if (blFinal == null) {
      delete nameEl.dataset.rtGap;
      delete nameEl.dataset.rtSlack;
    } else {
      nameEl.dataset.rtGap = (blFinal - (sol.left + sol.wpx)).toFixed(1);
      nameEl.dataset.rtSlack = (blFinal - leftMin - sol.wpx).toFixed(1);
    }
    // 是否走了“放宽重叠”那一轮：探针据此区分「本来就不用碰」和「实在放不下才压一点」，
    // 两者都合法，但比例失控说明阈值或立绘几何出了问题。
    if (relaxed) nameEl.dataset.rtRelax = "1";
    else delete nameEl.dataset.rtRelax;
    // 是否因为“颈以下左侧留白不够”往回收了（牌顶没落在目标锚点上）。探针据此区分
    // 「本来就该在头部以下」和「立绘把左缘占满、只能退回头部左侧」—— 后者是合法降级，
    // 但不该占多数；占多数说明锚点比例或立绘几何出了问题。
    if (sol.toeUp) nameEl.dataset.rtToe = "1";
    else delete nameEl.dataset.rtToe;
    nameEl.dataset.rtFs = sol.fs.toFixed(1);
    // 牌面被省略时打一个标记：探针据此断言“显示名 = 原名去掉中间一段”（首尾都在），
    // 而不是把“名字对不上”当成串了专员。落盘名不受影响。
    if (sol.text !== fullName) nameEl.dataset.rtElided = "1";
    else delete nameEl.dataset.rtElided;
    // 墨块按**最终盒子的实测像素**现场生成。必须放在写完 --rt-fs 之后：
    // 牌子的宽高 = padding + 字号，字号没落盘前量到的是上一轮的尺寸。
    // 直接量 offsetWidth/Height（而不是复用 sol.wpx），是因为 measure() 循环里
    // 最后一次试的档位可能已经失败、字号已被写坏，多一次 reflow 换一个不会错的尺寸。
    applySealInk(nameEl, nameEl.offsetWidth, nameEl.offsetHeight);
    // 定位成功才放出来（CSS 里 .dialog-name 默认 visibility:hidden）
    nameEl.dataset.rt = "1";
    return { left: sol.left, top: sol.top, fs: sol.fs, hit: sol.hits, ov: sol.ratio };
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
    // 位置进 key 是必须的：用户可拖动“立绘尺寸/水平/垂直”滑条、更换立绘、
    // 上传新图，都表现为 figure 盒变化，锚点必须跟着重算（不能写死）。
    // 二阶段只是对 img 做 scale 变换——后代变换不改 figure 布局盒，也不改
    // cRect 与 trim 存在性 → 不触发重算 → 倒计时位置在二阶段保持不动。
    const key = Math.round(cRect.width) + "x" + Math.round(cRect.height) + "|" +
      Math.round(figBox.left) + "," + Math.round(figBox.top) + " " +
      Math.round(figBox.width) + "x" + Math.round(figBox.height) + "|" + (haveTrim ? "T" : "t") +
      // 名字也进 key：换专员时可能只换名字不换图（或同名不同图），题名必须跟着重排。
    // 用 dataset.fullName 而不是 textContent：长名会被中间省略改写文本，拿改过的
    // 文本当 key 会让每一轮都判定“名字变了”而反复重排。
      "|" + (nameEl ? (nameEl.dataset.fullName || nameEl.textContent) : "");
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
    // 只在 key 变化时算，会赶上“图还没解码完”那一轮——那时量不到剖面，属于“没算出来”
    // 而不是“算出没有”，缓存住就再也不重试，名字会一直不显示（竖屏实测踩到）。所以
    // 定位成功（写 data-rt）之前每轮都重试；成功后只在 key 变化时重算。
    if (nameEl && (keyChanged || !nameEl.dataset.rt)) {
      // 牌底不能压到对话框：题名现在是**单列**往下长，没有“折列折回来”这条路，
      // 必须把对话框顶当成硬上界交给求解器（拿不到 rect 时它退窗口底）。
      layoutRoleTitle(figure, nameEl, figBox, content.getBoundingClientRect().top);
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
      // bar 是 column flex：提醒名称在环上（“提前结束”已经移出 bar、固定到窗口顶部）。
      // 用 ring-wrap 的实际布局位置（距 bar 顶的 offsetTop）反推“环中心距 bar 底”，
      // 这样无论环上下各放了什么，环中心都能精确落在 midY 上（名称移到环上方后，
      // 旧式“barH - ringH 当作环下占位”会把环整体抬高，故改为实测 offsetTop）。
      const ringFromTop = (ringWrap ? ringWrap.offsetTop : 0) + ringH / 2;
      const ringFromBottom = Math.max(0, barH - ringFromTop);
      // clamp 的合法区间是“bar 相对对话框”的坐标：窗口坐标的边界先算好再减去内缩量
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
    // 不再用 translateY 二次“对中”：那条老公式把文字挪到对话框的几何中心，与 CSS
    // 里“上边距小、下边距大”刻意的视觉偏上相互抵消，还会把文字推进下方内边距
    // （实测自定义提醒被推下 3px，看着像溢出）。
  }

  // ---- 对话框内“提醒内容”的统一自适应（短休 / 长休 / 外卖 / 自定义共用）----
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
    // 还有徽标/按钮，它们照样占高，框会自己长高 —— 但那不影响“同一段文字在哪个
    // 窗口里字号都一样”，这正是“保持相同样式”要的（扣掉按钮高度反而会让外卖
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
    // “题名还没定位成功”时走，定位成功即刻停，不会常态轮询。
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
  // 把“设置 → 快捷键”录入的组合键字符串（如 "Ctrl+Alt+X" / "Super+Shift+F1" /
  // "Ctrl+ "）解析成匹配器，让休息窗口的本地 keydown 与全局快捷键完全一致。
  // 解析失败（格式非法 / 缺修饰键）返回 null —— 对应“未配置”，本地不再兜底触发。
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
  // 粒子层：倒计时周围轻柔上浮的光点，渲染“放松”氛围。仅无督休专员模式启用
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
  // 蓄能容器：把倒计时当作一只“储存能量的空心容器”——
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
  // 液体颜色随背景（--accent）自动适配：同色相保证“协调”，提亮 + 高饱和保证“明显”。
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

  // 外界能量汲取：从四面八方径向射向容器壁，撞壁后迸出吸收涟漪并被“吸进”容器。
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
    // 一次性揭示窗口：消除“先无立绘、后有立绘”闪烁——启用督休专员时整窗先透明，
    // 立绘就绪（load/error）后淡入；无立绘时立即显示。超时兜底防异常卡透明。
    let revealed = false;
    const reveal = () => {
      if (revealed) return;
      revealed = true;
      document.documentElement.style.opacity = "1";
    };
    // 会话拉取要能扛住“窗口已建、会话还在路上”的空档：主进程是**先写会话再建窗**，
    // 但若上一次预览的窗口正在关闭、或主进程正在换会话，首帧这一次可能读到 nil。
    // 此前直接 reveal + return，窗口就停在只剩背景的空壳上 —— 用户看到的就是
    // “预览窗口不显示督休专员”，再点一次预览又好（那次读到了会话）。
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
    // 单窗多条：休息期间到点的其他提醒（自定义 / 外卖）挂右上角，不另开窗。
    // 首帧按会话里已有的清单铺一遍，之后由主进程推送整份清单（见 break:extras）。
    renderExtras(data.extras || []);
    if (api.onBreakExtras) api.onBreakExtras(renderExtras);

    function fmtClock(ms) {
      const d = new Date(Number(ms) || 0);
      return String(d.getHours()).padStart(2, "0") + ":" + String(d.getMinutes()).padStart(2, "0");
    }

    function renderExtras(list) {
      const box = document.getElementById("extra-notes");
      if (!box) return;
      box.textContent = "";
      const items = Array.isArray(list) ? list : [];
      if (!items.length) {
        box.classList.add("hidden");
        return;
      }
      box.classList.remove("hidden");
      for (const ex of items) box.appendChild(renderExtra(ex));
    }

    function renderExtra(ex) {
      const card = document.createElement("div");
      card.className = "extra-note";
      const head = document.createElement("div");
      head.className = "extra-head";
      const nm = document.createElement("span");
      nm.className = "extra-name";
      nm.textContent = ex.name || "";
      const at = document.createElement("span");
      at.className = "extra-at";
      at.textContent = ex.at ? fmtClock(ex.at) : "";
      head.appendChild(nm);
      head.appendChild(at);
      card.appendChild(head);
      if (ex.text) {
        const tx = document.createElement("div");
        tx.className = "extra-text";
        tx.textContent = ex.text;
        card.appendChild(tx);
      }
      // 外卖副条带按钮：走的是外卖模块自己的 TakeoutAction（推迟 / 已点好），
      // 与外卖窗口上的按钮同一套记账，不在这里另算一遍。
      if (ex.takeout) {
        const acts = document.createElement("div");
        acts.className = "extra-actions";
        // 外卖卡没勾「允许推迟提醒」（postponeMin = 0）时不给“推迟”按钮，
        // 只留“知道了”；主窗口那边与 TakeoutAction 的守卫同口径。
        const mins = Number(ex.takeout.postponeMin) || 0;
        if (mins > 0) {
          const later = document.createElement("button");
          later.type = "button";
          later.className = "extra-btn";
          later.textContent = "推迟 " + mins + " 分钟";
          later.addEventListener("click", () => api.takeoutAction("postpone"));
          acts.appendChild(later);
        }
        const ok = document.createElement("button");
        ok.type = "button";
        ok.className = "extra-btn";
        ok.textContent = "知道了";
        ok.addEventListener("click", () => api.takeoutAction("ordered"));
        acts.appendChild(ok);
        card.appendChild(acts);
      }
      return card;
    }
    // 环内两行直排铭文“整理思绪 / 积蓄能量”已静态写在 break.html 的
    // #inscribe-lines 中（进度百分数下方，非弧形），无需在此设置。
    if (data.supervisor) {
      const isCustom = typeof data.type === "string" && data.type.indexOf("cb-") === 0;
      if (isCustom) {
        // 自定义提醒：督休专员只作背景立绘，不显示其台词（提醒内容）、不进入第二阶段；
        // 对话区只展示自定义提醒自己的“提醒内容”（messages 保持可见，不被隐藏）。
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
    // 环内数字字号按“本次倒计时最多能有几位”定（系数表见 break.css 的 --cd-w）。
    // 必须用**总秒数的位数**、不是当前数字的位数：否则 100→99 会突然放大一档，
    // 读数每秒都在变胖变瘦。JS 没写这个属性时 CSS 按 2 位兜底。
    const ringWrap = document.querySelector(".ring-wrap");
    if (ringWrap && total > 0) {
      const digits = String(Math.ceil(total / 1000)).length;
      ringWrap.dataset.cdDigits = String(Math.min(5, Math.max(1, digits)));
    }
    // 生成“蓄能容器”几何：液态能量、气泡、外壁溢出光丝与脉冲
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
      // 蓄能进度 = 流逝占比（容器从空到满）；中心“进度”与此同步增长
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
            .getPropertyValue("--accent") || "").trim() || "#3d6bf0";
          startParticles(pcanvas, accent);
        } catch (e) { /* 装饰层异常不影响主体 */ }
      }
    }
    if (data.preview) {
      // 预览也复刻真实“满罐多停留 1 秒”的体验
      window.setTimeout(() => api.closeWindow(), Math.max(0, data.endsAt - Date.now()) + 1000);
    }
    const skip = document.getElementById("skip-link");
    // 顶部那个位置留给“预览模式 · 点击任意处关闭”：预览本来就点任意处关闭，再挂一个
    // “提前结束”既重复、又会和 badge 叠在同一处。
    // 另一条不显示的理由是「弹窗设置 → 允许提前结束」没勾（allowEarlyEnd === false）：
    // 连按钮带快捷键一起停用。字段缺失按**允许**算 —— 老会话 / 预览快照不带这个键时，
    // 行为与以前完全一致。
    const allowEndEarly = data.allowEarlyEnd !== false;
    if (!data.preview && allowEndEarly) skip.classList.remove("hidden");
    // 文案随“设置 → 快捷键”的 endEarly 走：配置了就显示“提前结束（Ctrl+Alt+X）”，
    // 没配置（禁用）就只显示“提前结束”。
    skip.textContent = data.endEarlyShortcut
      ? `提前结束（${data.endEarlyShortcut}）`
      : "提前结束";
    skip.addEventListener("click", (e) => {
      e.preventDefault();
      if (data.preview) api.closeWindow();
      else api.action("endBreak");
    });
    window.addEventListener("keydown", (e) => {
      // 按钮没显示时快捷键同样不响应（用户口径：「不勾则不显示，也不响应提前结束快捷键」）。
      // 全局快捷键那一层由 Go 的 schedEndBreak 再拦一次 —— 窗口不在前台时也得管住。
      if (allowEndEarly && matchAccelerator(e, data.endEarlyShortcut)) {
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
    // “没有督休专员”的样子露出来 —— 正是用户报的“有时候预览窗里没有督休专员”。
    // 宁可变透明多等一会儿，也不要露出空窗。
    window.setTimeout(reveal, 4000);
  }
  main();
})();
