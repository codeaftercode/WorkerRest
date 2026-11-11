"use strict";
// 设置页“控制器 → 配置键”全量接线矩阵。
//
// 已有的三层探针各有分工：settings_ui_probe 扫“有哪些控件、名字对不对、有没有溢出”，
// settings_modals_probe 验弹窗焦点，settings_flow_probe 验几条写入链路。
// 缺的是**逐个操作**：一个复选框接错配置键、或者根本没接（死控件），上面三层都发现不了。
//
// 这一支对六个 tab 里每个可见且可逆的控件做三步：
//   ① 操作它 → 配置必须有变化（死控件会报“没有变化”）
//   ② 记录变化落在哪些叶子（打印成接线表，也是本轮验证的产物）
//   ③ 操作回去 → 配置必须逐字还原（不可逆的控件说明它有隐藏副作用）
// 不可逆 / 需要确认框的控件（清空统计、恢复默认、删除专员…）不在这一支里硬点：
// 它们由 probe_bindings_full.py（每个绑定真调）与 settings_flow（恢复默认取消）覆盖。

const { chromium } = require("playwright");

const BASE = process.env.WR_BASE;
const PRELOAD = process.env.WR_PRELOAD;

let checks = 0;
const failures = [];
const wiring = []; // { tab, id, paths }

function check(ok, label, detail) {
  checks++;
  const d = detail === undefined || detail === null ? "" : "  | " + String(detail);
  if (ok) console.log("  ok   " + label + d);
  else {
    console.log("  FAIL " + label + d);
    failures.push(label);
  }
  return ok;
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// 深比较，产出“路径: 旧 → 新”的清单
function diffPaths(a, b, prefix = "") {
  const out = [];
  const isObj = (v) => v && typeof v === "object" && !Array.isArray(v);
  const keys = new Set([...Object.keys(a || {}), ...Object.keys(b || {})]);
  for (const k of keys) {
    const pa = a ? a[k] : undefined;
    const pb = b ? b[k] : undefined;
    const p = prefix ? prefix + "." + k : k;
    if (isObj(pa) && isObj(pb)) {
      out.push(...diffPaths(pa, pb, p));
    } else if (JSON.stringify(pa) !== JSON.stringify(pb)) {
      out.push(p + ": " + JSON.stringify(pa) + " → " + JSON.stringify(pb));
    }
  }
  return out;
}

async function waitApi(page) {
  for (let i = 0; i < 40; i++) {
    const ok = await page.evaluate(() => typeof window.api === "object" && window.api !== null);
    if (ok) return true;
    await sleep(250);
  }
  return false;
}

// 等配置稳定：连续两次读取相同即认为保存已落地
async function readCfg(page) {
  return await page.evaluate(() => window.api.getSettings());
}

async function waitSettled(page, tries = 24) {
  let last = JSON.stringify(await readCfg(page));
  for (let i = 0; i < tries; i++) {
    await sleep(60);
    const now = JSON.stringify(await readCfg(page));
    if (now === last) return JSON.parse(now);
    last = now;
  }
  return JSON.parse(last);
}

// 把某个控件“操作一下再操作回去”：返回 { changedPaths, restorePaths, openedModal }
async function poke(page, handle, kind) {
  const before = await readCfg(page);
  // 单选组的“原状态”不在被点的那枚上，而在组里当下选中的那枚上。
  // 记下来才能真还原：只记“我这枚该不该选中”会把上一个用例选中的值留在组里。
  const radioBefore = await page.evaluate((sel) => {
    const el = document.querySelector(sel);
    if (!el || el.type !== "radio") return null;
    const cur = document.querySelector(`input[name="${el.name}"]:checked`);
    return { name: el.name, value: cur ? cur.value : "" };
  }, handle);
  const openedModal = await page.evaluate((sel) => {
    const el = document.querySelector(sel);
    if (!el) return "missing";
    if (el.tagName === "INPUT" && el.type === "radio") {
      // 单选组：点“已选中”的那枚不会触发 change（状态没变），天真地 click 一下
      // 只会得到“配置无变化”的假失败。已选中时先点同组另一枚——由“操作回去”
      // 那一步点回本枚，于是这一对动作仍然完整验证了“点它 → 配置就是它”。
      if (!el.checked) el.click();
      else {
        const other = [...document.querySelectorAll(`input[name="${el.name}"]`)].find(
          (x) => x !== el && !x.disabled,
        );
        if (other) other.click();
      }
    } else if (el.tagName === "INPUT" && el.type === "checkbox") {
      el.click();
    } else if (el.tagName === "INPUT" && (el.type === "range" || el.type === "number")) {
      const min = el.min === "" ? -1e9 : Number(el.min);
      const max = el.max === "" ? 1e9 : Number(el.max);
      const cur = Number(el.value);
      // 挑一个“与原值明显不同、又落在合法区间内”的值
      let next = cur + (max - min) / 10;
      if (next > max) next = cur - (max - min) / 10;
      next = Math.max(min, Math.min(max, next));
      el.value = String(next);
      el.dispatchEvent(new Event("input", { bubbles: true }));
      el.dispatchEvent(new Event("change", { bubbles: true }));
    } else if (el.tagName === "INPUT" && el.type === "text") {
      el.value = el.value + "x";
      el.dispatchEvent(new Event("input", { bubbles: true }));
      el.dispatchEvent(new Event("change", { bubbles: true }));
    }
    return null;
  }, handle);

  const after = await waitSettled(page);
  const changedPaths = diffPaths(before, after);

  // 有确认框弹出的控件（例如“关闭提醒”要确认）：取消它，按“需人工确认”处理
  const modal = await page.evaluate(() => {
    const m = [...document.querySelectorAll(".modal")].find((x) => !x.classList.contains("hidden"));
    if (!m) return null;
    return m.id;
  });
  if (modal) {
    await page.keyboard.press("Escape");
    await sleep(150);
  }

  // 操作回去
  await page.evaluate(({ sel, radioBefore }) => {
    const el = document.querySelector(sel);
    if (!el) return;
    if (el.tagName === "INPUT" && el.type === "radio") {
      // 还原到“这轮开始前组里选中的那枚”；已选中就别点（点了也不发 change）
      const want = radioBefore
        ? document.querySelector(`input[name="${radioBefore.name}"][value="${radioBefore.value}"]`)
        : el;
      if (want && !want.checked) want.click();
    } else if (el.tagName === "INPUT" && el.type === "checkbox") {
      el.click();
    } else if (el.tagName === "INPUT" && (el.type === "range" || el.type === "number")) {
      el.value = el.dataset.wrOrig;
      el.dispatchEvent(new Event("input", { bubbles: true }));
      el.dispatchEvent(new Event("change", { bubbles: true }));
    } else if (el.tagName === "INPUT" && el.type === "text") {
      el.value = el.dataset.wrOrig;
      el.dispatchEvent(new Event("input", { bubbles: true }));
      el.dispatchEvent(new Event("change", { bubbles: true }));
    }
  }, { sel: handle, radioBefore });
  const back = await waitSettled(page);
  const restorePaths = diffPaths(before, back);

  return { changedPaths, restorePaths, modal };
}

// 督休专员的尺寸 / 位置 / 翻转：改的是 supervisor/<专员>/meta.json 的 geom，
// 不是 settings.json。用 listSupervisorRoles() 读回来验“接到了没 / 可不可逆”。
async function verifyGeomControls(page) {
  console.log("  ---- 立绘尺寸与位置（写 meta.json 的 geom，不走 settings）");
  // 高级选项是折叠面板，展开才能量到元素
  await page.evaluate(() => {
    const d = document.getElementById("sup-advanced");
    if (d) d.open = true;
  });
  await sleep(200);

  const roleName = await page.evaluate(() => {
    const el = document.getElementById("sup-adv-role");
    return (el && el.textContent ? el.textContent : "").trim();
  });
  if (!roleName || roleName === "—") {
    check(false, "立绘几何控件：取不到当前编辑的专员名");
    return;
  }

  const readGeom = async () =>
    await page.evaluate(async (name) => {
      const roles = await window.api.listSupervisorRoles();
      const r = (roles || []).find((x) => x && x.name === name);
      return r ? JSON.stringify(r.geom || {}) : "";
    }, roleName);

  const base = await readGeom();
  if (!base) {
    check(false, "立绘几何控件：读不回“" + roleName + "”的 geom");
    return;
  }
  const baseObj = JSON.parse(base);

  const IDS = ["sup-flip", "sup-size", "sup-posx", "sup-posy"];
  for (const id of IDS) {
    const orig = await page.evaluate((sel) => {
      const el = document.getElementById(sel);
      return el ? { type: el.type, value: el.value, checked: el.checked } : null;
    }, id);
    if (!orig) {
      check(false, "立绘几何控件：找不到 #" + id);
      continue;
    }
    await page.evaluate((sel) => {
      const el = document.getElementById(sel);
      if (el.type === "checkbox") el.click();
      else {
        const min = el.min === "" ? 0 : Number(el.min);
        const max = el.max === "" ? 100 : Number(el.max);
        let next = Number(el.value) + (max - min) / 10;
        if (next > max) next = Number(el.value) - (max - min) / 10;
        el.value = String(Math.max(min, Math.min(max, next)));
        // 这两个事件在真实交互里都发生，缺一个就验不到整条链路：
        //   input  → readGeomFromForm + applySupGeometry + commitGeomToRole（改内存、刷预览）
        //   change → bindAutoSaveEl 全量绑的 autoSave → collectForm + persistRole（写 meta.json）
        // 只发 input 时 meta.json 不会变，看起来像“死控件”，实际是少走了一步。
        el.dispatchEvent(new Event("input", { bubbles: true }));
        el.dispatchEvent(new Event("change", { bubbles: true }));
      }
    }, id);
    await sleep(350);
    const changed = await readGeom();
    check(changed !== base, "supervisor · " + id + " 写进了 meta.json 的 geom", changed);

    // 还原
    await page.evaluate(({ sel, o }) => {
      const el = document.getElementById(sel);
      if (!el) return;
      if (o.type === "checkbox") {
        if (el.checked !== o.checked) el.click();
      } else {
        el.value = o.value;
        el.dispatchEvent(new Event("input", { bubbles: true }));
        el.dispatchEvent(new Event("change", { bubbles: true }));
      }
    }, { sel: id, o: orig });
    await sleep(350);
    const back = await readGeom();
    check(back === base, "supervisor · " + id + " 可逆（还原后 geom 逐字一致）", back);
  }
  // geom 的镜像：状态卡摘要与滑条读数应跟着变（这里只确认读数是数字）
  const summary = await page.evaluate(() => {
    const el = document.getElementById("sup-geom-summary");
    return el ? el.textContent.trim() : "";
  });
  check(/[0-9]/.test(summary), "高级选项摘要显示 geo 数值", summary);
  void baseObj;
}

// 空闲门（“☐ 系统空闲达 [30] 秒后提醒”）：一个勾选框 + 一个秒数框 + 框右侧的补语。
// 秒数就是秒数 —— 填 90 显示 90，不折算成“1 分 30 秒”
//（2026-11-09 用户定稿，撤掉了上一版的“分 + 秒”双框与自动进位）。
// 输入框最小 1、默认 30；未勾时置灰不可编辑（这条门槛不生效），勾上才可编辑。
// 不勾 = 不启用（0 在界面上已不可达）。
//
// 为什么不走上面的单控件 poke：这一组要验的是“控件组整体 + 两个配置键”的行为
// （idleGate 开关与 idleGateMs 时长要分头落盘），单控件那套只比 value 是否可逆。
async function verifyIdleGateControls(page) {
  console.log("\n[timing] 空闲门（系统空闲秒）");
  await page.evaluate(() => {
    [...document.querySelectorAll(".nav-item")].find((b) => b.dataset.tab === "timing").click();
  });
  await sleep(400);

  const viewOf = async (id) =>
    await page.evaluate((k) => {
      const box = document.getElementById(k + "-idleGate");
      const sec = document.getElementById(k + "-idleGate-sec");
      if (!box || !sec) return null;
      const row = box.closest(".row");
      const label = row ? row.querySelector("label") : null;
      const tail = row ? row.querySelector(".unit") : null;
      const lb = label ? label.getBoundingClientRect() : null;
      const sb = sec.getBoundingClientRect();
      return {
        checked: box.checked,
        sec: sec.value,
        min: sec.getAttribute("min"),
        label: label ? label.textContent.trim() : "",
        tail: tail ? tail.textContent.trim() : "",
        disabled: sec.disabled,
        // 标签是否单行（2026-11-11 改成一整句之后它比 200px 的标签列宽 5px，
        // 靠 nowrap 溢进列间那道 16px 的沟）：包成两行会让这一行突兀地高出一截。
        labelH: lb ? Math.round(lb.height) : -1,
        labelRight: lb ? Math.round(lb.right) : -1,
        secLeft: Math.round(sb.left),
        lineH: label ? Math.round(parseFloat(getComputedStyle(label).lineHeight) || 0) : -1,
        // 标签内容不许溢出自己的盒子：这整句比默认的 200px 标签列宽 5px，靠 `.idle-gate`
        // 把列间距里的 13px 借给标签列才装得下（DPI 探针把任何 scrollWidth>clientWidth 都算裁切）
        labelOver: label ? label.scrollWidth - label.clientWidth : -1,
      };
    }, id);

  // 操作一组空闲门控件并等保存落地：先勾选、再填秒数（与真人操作同向）。
  const setGate = async (id, opt) => {
    await page.evaluate(
      ({ k, w, s }) => {
        const box = document.getElementById(k + "-idleGate");
        if (box && box.checked !== w) box.click();
        const secEl = document.getElementById(k + "-idleGate-sec");
        if (secEl && s !== undefined && s !== null) {
          secEl.value = String(s);
          secEl.dispatchEvent(new Event("change", { bubbles: true }));
        }
      },
      { k: id, w: !!opt.check, s: opt.sec },
    );
    return await waitSettled(page);
  };

  // ① 三张内置卡：控件在场、文案、默认不勾、默认 30 秒、min=1
  for (const id of ["short", "long", "tk"]) {
    const v = await viewOf(id);
    if (!check(!!v, "空闲门 · " + id + " 控件在场")) continue;
    // 2026-11-11 用户改的名：整行读作“☐ 只在空闲时提醒，空闲时长 [30] 秒”
    //（原写法“系统空闲达 [30] 秒再提醒”，句尾“再提醒”与标签前半句重复）
    check(
      v.label === "只在空闲时提醒，空闲时长?",
      "空闲门 · " + id + " 标签文案（“只在空闲时提醒，空闲时长”）",
      v.label,
    );
    check(v.tail === "秒", "空闲门 · " + id + " 秒数框右侧是单位“秒”", v.tail);
    // 一整句比 200px 的标签列宽 5px —— 靠 `.idle-gate` 把列沟里的 13px 借给标签列才装下。
    // 它必须仍然单行（两行会让这行高出一截），且内容不许溢出自己的盒子（DPI 探针的裁切记号）。
    check(
      v.labelH > 0 && v.labelH <= v.lineH + 4,
      "空闲门 · " + id + " 标签单行显示（没在“空闲时/长”之间断成两行）",
      v.labelH + "px / 行高 " + v.lineH,
    );
    check(
      v.labelOver === 0 && v.secLeft - v.labelRight >= 2,
      "空闲门 · " + id + " 标签既没溢出、也没顶到秒数框（列沟借了 13px 给标签列）",
      JSON.stringify({ over: v.labelOver, gap: v.secLeft - v.labelRight }),
    );
    check(v.disabled === true, "空闲门 · " + id + " 未勾时秒数框置灰不可编辑", String(v.disabled));
    check(v.checked === false, "空闲门 · " + id + " 默认不勾");
    check(v.sec === "30", "空闲门 · " + id + " 默认 30 秒", v.sec);
    check(v.min === "1", "空闲门 · " + id + " 最小值为 1", v.min);
  }

  // ② 用户要求：时间控件放右侧、与下方输入框同列。这里直接量几何位置 ——
  //    改版式时最容易破坏的就是“看起来差不多”，量一把才知道真的同一竖线。
  //    2026-11-10「间隔」搬进左侧面板后不再是全宽字段行（面板内是单列布局，
  //    输入框跟着面板走），所以对照物换成仍在组外的「提醒时长」—— 它代表全卡
  //    那条控件列（509），空闲门这一行也还在面板之外，两者必须严格同列。
  const align = await page.evaluate(() => {
    const g = document.getElementById("short-idleGate-sec");
    const i = document.getElementById("short-duration");
    if (!g || !i) return null;
    const a = g.getBoundingClientRect();
    const b = i.getBoundingClientRect();
    return {
      gate: Math.round(a.left),
      input: Math.round(b.left),
      wGate: Math.round(a.width),
      wInput: Math.round(b.width),
    };
  });
  check(
    !!align && Math.abs(align.gate - align.input) <= 1,
    "空闲门 · 输入框与「提醒时长」左缘同列（全卡那条控件列）",
    JSON.stringify(align),
  );
  check(
    !!align && Math.abs(align.wGate - align.wInput) <= 1,
    "空闲门 · 输入框与「提醒时长」同宽",
    JSON.stringify(align),
  );

  // ③ 填 90：原样显示 90，配置落 90000 —— 不折算成“1 分 30 秒”
  let cfg = await setGate("short", { check: true, sec: 90 });
  let v = await viewOf("short");
  check(v.sec === "90", "空闲门 · 填 90 秒原样显示，不折算成 1 分 30 秒", v.sec);
  check(v.disabled === false, "空闲门 · 勾上后秒数框恢复可编辑", String(v.disabled));
  check(
    cfg.short.idleGate === true && cfg.short.idleGateMs === 90000,
    "空闲门 · 勾选 + 90 秒写入 idleGate / idleGateMs",
    cfg.short.idleGate + " / " + cfg.short.idleGateMs,
  );

  // ④ 下限：填 0 钳到 1 秒（“不启用”只能靠不勾，不能靠填 0）
  cfg = await setGate("short", { check: true, sec: 0 });
  v = await viewOf("short");
  check(
    cfg.short.idleGateMs === 1000 && v.sec === "1",
    "空闲门 · 填 0 钳到最小 1 秒",
    cfg.short.idleGateMs + " / " + v.sec,
  );

  // ⑤ 上限：填 9999 秒钳到 1800 秒（与 Go 的 idleGateMaxMs 同源）
  cfg = await setGate("short", { check: true, sec: 9999 });
  v = await viewOf("short");
  check(
    cfg.short.idleGateMs === 1800000 && v.sec === "1800",
    "空闲门 · 超上限钳到 1800 秒",
    cfg.short.idleGateMs + " / " + v.sec,
  );

  // ⑥ 跨卡片接线：外卖卡的勾选落在 takeout 上（不是 short）
  cfg = await setGate("tk", { check: true, sec: 45 });
  check(
    cfg.takeout.idleGate === true && cfg.takeout.idleGateMs === 45000,
    "空闲门 · 外卖卡写入 takeout.idleGate / takeout.idleGateMs",
    cfg.takeout.idleGate + " / " + cfg.takeout.idleGateMs,
  );
  cfg = await setGate("tk", { check: false, sec: 30 });

  // ⑦ 自定义提醒也有这个控件（用户口径：所有提醒都要有）
  await page.click("#cb-add");
  await sleep(250);
  await page.fill("#pm-input", "空闲门测试");
  await page.click("#pm-ok");
  await waitSettled(page);
  const cbID = await page.evaluate(() => {
    const cards = [...document.querySelectorAll("#tab-timing > .card[data-cb]")];
    const c = cards[cards.length - 1];
    if (!c) return "";
    const box = c.querySelector('.pg[data-pg="trigger"] .idle-gate input[type=checkbox]');
    return box ? box.id.replace(/-idleGate$/, "") : "";
  });
  check(!!cbID, "空闲门 · 自定义提醒卡也渲染了这个控件", cbID);
  if (cbID) {
    cfg = await setGate(cbID, { check: true, sec: 120 });
    const cb = (cfg.customBreaks || []).find((x) => x.id === cbID);
    check(
      !!cb && cb.config.idleGate === true && cb.config.idleGateMs === 120000,
      "空闲门 · 自定义提醒写入 config.idleGate / config.idleGateMs",
      cb ? cb.config.idleGate + " / " + cb.config.idleGateMs : "配置里找不到这张卡",
    );
    // 控件位置：2026-11-09 起卡内参数分成「触发条件 / 弹窗设置」两组，系统空闲属于前者、
    // 暂停休息时属于后者 —— 这里按**组**断言，不再按“行的绝对序号”
    // 2026-11-10：暂停休息时从组末提到组首（用户要求，它是最常用的那一行），
    // 所以断言从「在提醒时长之后」翻成「在提醒时长之前，且是组内第一行」。
    const order = await page.evaluate((id) => {
      const card = document.querySelector(`#tab-timing > .card[data-cb="${id}"]`);
      if (!card) return null;
      const pg = [...card.querySelectorAll(":scope > .pg")];
      const trig = pg.find((p) => p.dataset.pg === "trigger");
      const rem = pg.find((p) => p.dataset.pg === "reminder");
      // 候选单元：字段行 + 计时方法面板（面板不是 .row，但同样算组里的成员；
      // “暂停休息时”是 .row.field，已经算在 .row 里，不必单列）
      const idx = (g, re) => (g ? [...g.querySelectorAll(".row, .mode-panes")].findIndex((r) => re.test(r.textContent)) : -2);
      return {
        titles: pg.map((p) => (p.querySelector(".pg-title") || {}).textContent || ""),
        // 2026-11-10 起计时方法是上下两块面板（不是 .row），按它的文案找位置
        mode: idx(trig, /倒计时/),
        gate: idx(trig, /只在空闲时提醒/),
        dur: idx(rem, /提醒时长/),
        pause: idx(rem, /暂停休息时/),
      };
    }, cbID);
    check(
      !!order && order.titles.join("/") === "触发条件/弹窗设置",
      "分组 · 自定义卡也克隆出「触发条件 / 弹窗设置」两组",
      order ? order.titles.join("/") : "找不到卡片",
    );
    check(
      !!order && order.mode >= 0 && order.gate > order.mode,
      "分组 · 系统空闲在「触发条件」组内、计时方法之后",
      JSON.stringify(order),
    );
    check(
      !!order && order.pause === 0 && order.dur > order.pause,
      "分组 · 暂停休息时是「弹窗设置」组内第一行（提醒时长之前）",
      JSON.stringify(order),
    );
    // 卡头对齐（2026-11-10）：自定义卡的摘要不再和 ✎ / ✕ 包在一起，回到标题右侧；
    // 图标组自带 auto margin 推到行尾，预览与总开关跟在它后面 —— 于是预览右缘、
    // 开关左右缘必须与短休息卡逐像素同线（否则上下两张卡的卡头会错位半格）。
    const cbAlign = await page.evaluate((id) => {
      const card = document.querySelector(`#tab-timing > .card[data-cb="${id}"]`);
      const short = document.getElementById("short-card");
      const box = (root, sel) => {
        const el = root && root.querySelector(sel);
        if (!el) return null;
        const b = el.getBoundingClientRect();
        return { l: Math.round(b.left), r: Math.round(b.right) };
      };
      return {
        sumInHead: !!card && !!card.querySelector(":scope > .card-head > .adv-summary"),
        act: box(card, ":scope > .card-head > .card-head-act"),
        actRef: box(short, ":scope > .card-head > .card-head-act"),
        sw: box(card, ":scope > .card-head > .card-switch"),
        swRef: box(short, ":scope > .card-head > .card-switch"),
      };
    }, cbID);
    check(
      !!cbAlign && cbAlign.sumInHead &&
        !!cbAlign.act && !!cbAlign.actRef &&
        Math.abs(cbAlign.act.r - cbAlign.actRef.r) <= 1 &&
        !!cbAlign.sw && !!cbAlign.swRef &&
        Math.abs(cbAlign.sw.l - cbAlign.swRef.l) <= 1 &&
        Math.abs(cbAlign.sw.r - cbAlign.swRef.r) <= 1,
      "卡头 · 自定义卡摘要紧跟标题，预览 / 开关与内置卡同一条竖线",
      JSON.stringify(cbAlign),
    );
    // 删掉这张卡（走确认弹窗），配置回到出厂的空数组
    await page.evaluate(() => {
      const cards = [...document.querySelectorAll("#tab-timing > .card[data-cb]")];
      const c = cards[cards.length - 1];
      const del = c && c.querySelector(".cb-del");
      if (del) del.click();
    });
    await sleep(250);
    await page.click("#cf-ok");
    cfg = await waitSettled(page);
    check(
      (cfg.customBreaks || []).length === 0,
      "空闲门 · 删掉测试卡片后自定义提醒回到空",
      (cfg.customBreaks || []).length,
    );
  }

  // ⑧ 勾掉开关时保留时长（下次勾回来还是原值）
  cfg = await setGate("short", { check: true, sec: 75 });
  cfg = await setGate("short", { check: false });
  check(
    cfg.short.idleGate === false && cfg.short.idleGateMs === 75000,
    "空闲门 · 取消勾选保留时长",
    cfg.short.idleGate + " / " + cfg.short.idleGateMs,
  );

  // ⑨ 还原短休息成出厂形态（不勾 + 30 秒）
  cfg = await setGate("short", { check: false, sec: 30 });
  v = await viewOf("short");
  check(
    cfg.short.idleGate === false && cfg.short.idleGateMs === 30000 && v.checked === false,
    "空闲门 · 短休息还原成不勾 + 30 秒",
    cfg.short.idleGate + " / " + cfg.short.idleGateMs,
  );
}

// 「计时方法」与「提醒时长（秒）」（2026-11-09 用户改版）：
//   · “触发条件” → “计时方法”；
//   · “时长（秒）” → “提醒时长（秒）”，长休息原来按分钟（1–60），现在也按秒（5–3600），
//     内部仍存 durationMs —— 所以填 420 秒必须落成 420000、回填仍是 420。
// 单位搞反是这类改动最容易出的事故：填 5 落成 300000（5 分钟），界面上一眼看不出差别，
// 要等休息窗口开了 5 分钟才发现。这一组就是钉这个。
async function verifyBreakDurationUnit(page) {
  console.log("\n[timing] 计时方法 / 提醒时长（秒）");
  await page.evaluate(() => {
    [...document.querySelectorAll(".nav-item")].find((b) => b.dataset.tab === "timing").click();
  });
  await sleep(300);

  const labels = await page.evaluate(() => {
    const pick = (id) => {
      const el = document.getElementById(id);
      if (!el) return null;
      const lab = document.querySelector(`label[for="${id}"]`);
      // 2026-11-09 起单位不再折进标签写成“提醒时长（秒）”，而是跟在框右侧的 .unit，
      // 所以“这个字段叫什么 + 单位是什么”要分头取（也顺带钉住单位没丢）
      const row = el.closest(".row");
      const unit = row ? row.querySelector(".field-line > .unit") : null;
      return {
        label: lab ? lab.textContent.trim() : "",
        unit: unit ? unit.textContent.trim() : "",
        min: el.getAttribute("min"),
        max: el.getAttribute("max"),
      };
    };
    const modeLabel = (hostId) => {
      // 2026-11-09 起计时方法是段控（radiogroup），可访问名挂在容器上，不再是 .cdrop-btn
      const host = document.getElementById(hostId);
      return host ? host.getAttribute("aria-label") : null;
    };
    return {
      short: pick("short-duration"),
      long: pick("long-duration"),
      shortMode: modeLabel("short-mode"),
      longMode: modeLabel("long-mode"),
    };
  });
  check(
    !!labels.short && labels.short.label === "提醒时长" && labels.short.unit === "秒",
    "时长 · 短休息标签与单位（标签写名字、单位在框右侧）",
    JSON.stringify(labels.short),
  );
  check(
    !!labels.long && labels.long.label === "提醒时长" && labels.long.unit === "秒",
    "时长 · 长休息标签与单位（原来是“时长（分钟）”）",
    JSON.stringify(labels.long),
  );
  check(labels.shortMode === "计时方法", "计时方法 · 短休息段控有 aria-label", labels.shortMode);
  check(labels.longMode === "计时方法", "计时方法 · 长休息段控有 aria-label", labels.longMode);
  check(
    !!labels.long && labels.long.min === "5" && labels.long.max === "3600",
    "时长 · 长休息改成秒后量程放宽成 5–3600",
    JSON.stringify(labels.long),
  );

  const setDuration = async (id, v) => {
    await page.evaluate(
      ({ k, val }) => {
        const el = document.getElementById(k + "-duration");
        if (!el) return;
        el.value = String(val);
        el.dispatchEvent(new Event("change", { bubbles: true }));
      },
      { k: id, val: v },
    );
    return await waitSettled(page);
  };

  // 长休息填 420 秒：落盘 420000，回填 420
  let cfg = await setDuration("long", 420);
  check(cfg.long.durationMs === 420000, "时长 · 长休息填 420 秒落成 420000ms", cfg.long.durationMs);
  const back = await page.evaluate(() => document.getElementById("long-duration").value);
  check(back === "420", "时长 · 长休息 420 秒回填仍是 420", back);

  // 短休息一直是秒，这里只防回退
  cfg = await setDuration("short", 45);
  check(cfg.short.durationMs === 45000, "时长 · 短休息填 45 秒落成 45000ms", cfg.short.durationMs);

  // 还原成出厂值（长 300 秒 / 短 20 秒）
  cfg = await setDuration("long", 300);
  cfg = await setDuration("short", 20);
  check(
    cfg.long.durationMs === 300000 && cfg.short.durationMs === 20000,
    "时长 · 已还原成出厂 300 / 20 秒",
    cfg.long.durationMs + " / " + cfg.short.durationMs,
  );

  // 「暂停休息时」自绘下拉（2026-11-10 二次回退：并排三档段控被否 —— 三格把控件列
  // 挤窄、字号还得缩，用户原话“把其他控件都挤小了”，要求回下拉、保留每项前面的图标）。
  // 钉四件事：三项与顺序、默认“推迟提醒”、每项和触发器都带图标、交互落盘；
  // 外加版式三条（同行 / 与同组输入框同列 / 宽度不铺满控件列），这三条不随控件形态变。
  // 另钉两条“已经删掉”的东西，防止回潮：选中项左侧不许再有竖条、下拉下方不许再有说明文字。
  const seg = await page.evaluate(() => {
    const host = document.getElementById("tk-pause");
    if (!host) return null;
    const btn = host.querySelector(".cdrop-btn");
    const items = [...host.querySelectorAll(".cdrop-item")];
    const on = items.find((it) => it.getAttribute("aria-selected") === "true");
    const pb = on ? getComputedStyle(on, "::before") : null;
    const box = (el) => {
      if (!el) return null;
      const r = el.getBoundingClientRect();
      return { l: Math.round(r.left), t: Math.round(r.top), r: Math.round(r.right), b: Math.round(r.bottom) };
    };
    const lbBox = box(host.parentElement.querySelector("label"));
    const segBox = box(host);
    const leadBox = box(document.getElementById("tk-lead"));
    return {
      label: btn ? btn.getAttribute("aria-label") : null,
      haspopup: btn ? btn.getAttribute("aria-haspopup") : null,
      value: host.dataset.value,
      btnText: btn ? (btn.querySelector(".cdrop-cur") || {}).textContent || "" : "",
      // 图标是 SVG 内层节点（path / circle）：数子节点就知道有没有画出来
      btnIco: btn ? btn.querySelectorAll(".cdrop-ico > *").length : 0,
      values: items.map((it) => it.dataset.value),
      labels: items.map((it) => (it.querySelector(".cdrop-label") || {}).textContent || ""),
      icons: items.map((it) => it.querySelectorAll(".cdrop-ico > *").length),
      selected: items.filter((it) => it.getAttribute("aria-selected") === "true").map((it) => it.dataset.value),
      // 选中项左侧竖条（.cdrop-item[aria-selected=true]::before）已删：无规则时
      // getComputedStyle 的 content 是 none、宽高为 auto
      beforeContent: pb ? pb.content : "?",
      beforeWidth: pb ? pb.width : "?",
      // 整行只该剩 label 与下拉两个元素（说明文字原来占第三个）
      rowKids: [...host.parentElement.children].map((e) => e.tagName.toLowerCase()),
      // 全页不该再有 .pause-hint（外卖卡 + 每张自定义卡都没有）
      hints: document.querySelectorAll(".pause-hint").length,
      sameRow: !!lbBox && !!segBox && lbBox.t < segBox.b && lbBox.b > segBox.t,
      segL: segBox ? segBox.l : -1,
      leadL: leadBox ? leadBox.l : -2,
      width: segBox ? segBox.r - segBox.l : 0,
    };
  });
  check(!!seg && seg.label === "暂停休息时" && seg.haspopup === "listbox",
    "暂停休息时 · 自绘下拉，可访问名“暂停休息时”、aria-haspopup=listbox",
    JSON.stringify(seg && { l: seg.label, h: seg.haspopup }));
  check(!!seg && seg.values.join("/") === "remind/postpone/skip",
    "暂停休息时 · 三项依次是 正常提醒 / 推迟提醒 / 跳过提醒", seg && seg.values.join("/"));
  check(!!seg && seg.icons.every((n) => n > 0),
    "暂停休息时 · 每个选项前面都有图标", seg && seg.icons.join("/"));
  check(!!seg && seg.btnIco > 0,
    "暂停休息时 · 触发器上也有当前项的图标", seg && seg.btnIco);
  check(!!seg && seg.selected.join("/") === "postpone" && seg.btnText === "推迟提醒",
    "暂停休息时 · 默认选中“推迟提醒”（按钮文字与 aria-selected 一致）",
    seg && { sel: seg.selected, text: seg.btnText });
  // 2026-11-10 下午按用户要求删掉：选中项左侧蓝色竖条、下拉下方那行说明文字
  check(!!seg && (seg.beforeContent === "none" || seg.beforeWidth === "auto" || seg.beforeWidth === "0px"),
    "暂停休息时 · 选中项左侧没有竖条（::before 无内容）",
    seg && { c: seg.beforeContent, w: seg.beforeWidth });
  check(!!seg && seg.rowKids.join("/") === "label/div" && seg.hints === 0,
    "暂停休息时 · 下拉下方没有说明文字（整行只有标签与下拉）",
    seg && { kids: seg.rowKids.join("/"), hints: seg.hints });
  // ① 同行：标签的纵向区间与控件相交（段控时代之前的旧版曾把它拆成两行）
  check(!!seg && seg.sameRow,
    "版式 · “暂停休息时”标签与下拉在同一行", seg && seg.sameRow);
  // ② 同列：下拉左缘 = 同组输入框左缘（差值 ≤ 1px 容忍亚像素取整）
  check(!!seg && seg.leadL > 0 && Math.abs(seg.segL - seg.leadL) <= 1,
    "版式 · 下拉左缘与“提前通知”输入框左缘同列", seg && { seg: seg.segL, input: seg.leadL });
  // ③ 宽度回到输入框那一档：下拉 180px，不再铺满控件列（段控就是这么把版面挤坏的）
  check(!!seg && seg.width <= 200,
    "版式 · 下拉宽度 ≤ 200（与输入框同档，不铺满控件列）", seg && seg.width);

  // 点开菜单 → 点“跳过提醒”：落盘、按钮值与图标换、说明换、菜单收起
  await page.click("#tk-pause .cdrop-btn");
  const opened = await page.evaluate(() => {
    const root = document.querySelector("#tk-pause .cdrop");
    return {
      open: root.classList.contains("open"),
      expanded: root.querySelector(".cdrop-btn").getAttribute("aria-expanded"),
    };
  });
  check(opened.open && opened.expanded === "true",
    "暂停休息时 · 点按钮展开菜单（aria-expanded=true）", JSON.stringify(opened));
  await page.click('#tk-pause .cdrop-item[data-value="skip"]');
  const skipCfg = await waitSettled(page);
  const afterSkip = await page.evaluate(() => {
    const host = document.getElementById("tk-pause");
    const root = host.querySelector(".cdrop");
    return {
      value: host.dataset.value,
      text: root.querySelector(".cdrop-cur").textContent,
      open: root.classList.contains("open"),
      selected: [...root.querySelectorAll(".cdrop-item")]
        .filter((it) => it.getAttribute("aria-selected") === "true")
        .map((it) => it.dataset.value).join("/"),
      ico: root.querySelector(".cdrop-btn .cdrop-ico").children.length,
    };
  });
  check(skipCfg.takeout.pauseAction === "skip",
    "暂停休息时 · 选“跳过提醒”落盘 pauseAction=skip", skipCfg.takeout.pauseAction);
  check(afterSkip.value === "skip" && afterSkip.text === "跳过提醒" && afterSkip.selected === "skip",
    "暂停休息时 · 选中态、dataset.value 与按钮文字同步", JSON.stringify(afterSkip));
  check(!afterSkip.open, "暂停休息时 · 选中后菜单自动收起", afterSkip.open);
  check(afterSkip.ico > 0, "暂停休息时 · 触发器图标跟着当前项换（跳过 = 带斜杠的圆）", afterSkip.ico);

  // 键盘模型（listbox）：按钮上 ↓ 展开并把焦点放到当前项；Escape 收起、焦点回按钮
  await page.focus("#tk-pause .cdrop-btn");
  await page.keyboard.press("ArrowDown");
  await page.waitForTimeout(80);
  const kb = await page.evaluate(() => {
    const root = document.querySelector("#tk-pause .cdrop");
    const a = document.activeElement;
    return {
      open: root.classList.contains("open"),
      isOption: a ? a.getAttribute("role") === "option" : false,
      focusVal: a ? a.dataset.value || "" : "",
    };
  });
  check(kb.open && kb.isOption && kb.focusVal === "skip",
    "暂停休息时 · 方向键展开且焦点落在当前项", JSON.stringify(kb));
  await page.keyboard.press("Escape");
  await page.waitForTimeout(60);
  const esc = await page.evaluate(() => {
    const root = document.querySelector("#tk-pause .cdrop");
    const a = document.activeElement;
    return { open: root.classList.contains("open"), back: !!a && a.classList.contains("cdrop-btn") };
  });
  check(!esc.open && esc.back, "暂停休息时 · Escape 收起并回按钮", JSON.stringify(esc));

  // 还原默认（推迟提醒）
  await page.click("#tk-pause .cdrop-btn");
  await page.click('#tk-pause .cdrop-item[data-value="postpone"]');
  await waitSettled(page);

  // 数字框宽度（2026-11-10 下午用户要求）：时间规划页所有 .field-line 数值框 180px、
  // 与全局输入框和“暂停休息时”下拉同宽，不许再退回 80px 的“小方框”，且左对齐。
  const nums = await page.evaluate(() => {
    const out = [];
    for (const el of document.querySelectorAll('.tab.active .field-line > input[type="number"]')) {
      const r = el.getBoundingClientRect();
      if (!r.width) continue; // 隐藏行（倒计时 / 固定时间互斥、面板收起）跳过
      out.push({
        id: el.id,
        w: Math.round(r.width),
        align: getComputedStyle(el).textAlign,
        right: Math.round(r.right),
        cardR: Math.round(el.closest('.card').getBoundingClientRect().right),
      });
    }
    return out;
  });
  check(nums.length >= 3, "数字框 · 取到时间规划页的数值框", nums.length);
  check(nums.every((n) => Math.abs(n.w - 180) <= 1),
    "数字框 · 宽度 180（与下拉 / 全局输入框同宽，不铺满也不收成小方框）",
    nums.map((n) => n.id + "=" + n.w).join(" "));
  check(nums.every((n) => n.align === "start" || n.align === "left"),
    "数字框 · 数字左对齐（与全站输入框一致，不再居中）",
    nums.map((n) => n.id + "=" + n.align).join(" "));
  check(nums.every((n) => n.right < n.cardR),
    "数字框 · 没有任何一个顶出卡片右缘", nums.map((n) => n.id).join("/"));
}

// 参数分组（2026-11-09）：「计时方法 / 间隔 / 提醒时间 / 系统空闲」收进标题为“触发条件”
// 的一组，另外三个数（提醒时长 / 提前通知 / 推迟时长）收进“弹窗设置”一组。倒计时只显示间隔、
// 固定时间只显示提醒时间。这里钉三件事：
//   ① 三张内置卡（含只有固定时刻的外卖卡）都有这两个组、标题与顺序一致；
//   ② 切计时方法时“间隔 / 提醒时间”在组内互换，切回原状；
//   ③ 分组不破坏列对齐 —— 组内标签左缘 == 组标题 / 卡头左缘；所有数值框同左缘同宽。
//      （加粗体、加底色都无所谓，加水平内边距才是灾难：整列会平移十几像素。）
// 2026-11-09 卡片整体重设计后，卡级开关从正文第一行搬进了卡头（.card-switch），
// 于是「启用行在两组之前」这条改钉「卡头在两组之前」；「预览」也不再落在控件列
// （它现在贴在卡头右端），控件列的断言改成“数值框互相同左缘同宽”。
async function verifyParamGroups(page) {
  console.log("\n[timing] 参数分组（触发条件 / 弹窗设置）");
  await page.evaluate(() => {
    [...document.querySelectorAll(".nav-item")].find((b) => b.dataset.tab === "timing").click();
  });
  await sleep(350);

  const struct = await page.evaluate(() => {
    const out = {};
    for (const id of ["short", "long", "tk"]) {
      const card = document.getElementById(id + "-card");
      if (!card) {
        out[id] = null;
        continue;
      }
      const pg = [...card.querySelectorAll(":scope > .pg")];
      const g = (k) => pg.find((p) => p.dataset.pg === k);
      const trig = g("trigger");
      const rem = g("reminder");
      // 拼字符串时剔除参数说明的“?”圆圈：它的 textContent 是 “?”，会把
      // “提前通知 + 秒”拼成 “提前通知?秒”，这条断言就再也对不上了。
      // 问号本身是否挂在文字末尾，由后面的专项断言负责。
      const t = (el) => {
        if (!el) return "";
        const c = el.cloneNode(true);
        for (const q of c.querySelectorAll(".q-mark")) q.remove();
        return c.textContent.replace(/\s+/g, "");
      };
      out[id] = {
        titles: pg.map((p) => (p.querySelector(".pg-title") || {}).textContent || ""),
        trig: t(trig),
        rem: t(rem),
        // 卡头必须在两组之前：卡级身份 / 开关 / 摘要 / 预览在正文参数上面，
        // 参数不会跑到卡头上面去
        headAbove: (() => {
          const head = card.querySelector(":scope > .card-head");
          return !!head && !!pg[0] && !!(head.compareDocumentPosition(pg[0]) & Node.DOCUMENT_POSITION_FOLLOWING);
        })(),
        // 卡头里必须有这张卡的总开关（拨动开关），且它是卡头里唯一的 checkbox
        headSwitch: !!card.querySelector(":scope > .card-head > .card-switch > input[type=checkbox]"),
      };
    }
    return out;
  });

  for (const id of ["short", "long", "tk"]) {
    const s = struct[id];
    if (!check(!!s, "分组 · " + id + " 卡片在场")) continue;
    check(
      s.titles.join("/") === "触发条件/弹窗设置",
      "分组 · " + id + " 两个组标题与顺序",
      s.titles.join("/"),
    );
    check(s.headAbove, "分组 · " + id + " 卡头在两组之前");
    check(s.headSwitch, "分组 · " + id + " 总开关在卡头里（拨动开关）");
    check(
      /只在空闲时提醒，空闲时长/.test(s.trig) && /提醒时间|＋添加/.test(s.trig),
      "分组 · " + id + " “空闲门 / 时刻表”在“触发条件”组里",
      s.trig.slice(0, 60),
    );
    check(
      !/提醒时长|提前通知|推迟时长/.test(s.trig),
      "分组 · " + id + " “弹窗设置”组那三个数没有混进“触发条件”",
      s.trig.slice(0, 60),
    );
    check(
      /提前通知秒/.test(s.rem) && /推迟时长分钟/.test(s.rem),
      "分组 · " + id + " “提前通知 / 推迟时长”在“弹窗设置”组里",
      s.rem.slice(0, 60),
    );
  }
  // “推迟时长 / 提前通知”的浮层提示与下限（2026-11-10）：三个入口（short / long / tk）
  // 文案必须逐字一致；推迟时长最小 1 分钟 —— “0 = 不显示推迟按钮”的语义已整体删除，
  // 输入框的下限与 Go 侧 clamp 必须同值。
  // 提示挂在**左侧文字末尾的“?”**上（不是数字框的 title）：挂在输入框上时，用户得先
  // 把指针移到框上才知道有说明，而“这个标签是什么意思”本来就是文字的疑问。
  const attrs = await page.evaluate(() => {
    const out = {};
    for (const id of ["short", "long", "tk"]) {
      const p = document.getElementById(id + "-postpone");
      const n = document.getElementById(id + "-notify") || document.getElementById(id + "-lead");
      const mark = (input) => {
        const lab = input ? input.closest(".row")?.querySelector("label") : null;
        if (!lab) return null;
        const q = lab.querySelector(".q-mark");
        return q
          ? {
              tip: q.dataset.tip || "",
              // “?”必须是 label 的最后一个孩子 —— 说明的是这段文字，所以要吊在文字末尾
              last: lab.lastElementChild === q,
              // 视觉上要跟文字同一行（inline 上下文），不能被拆成独立一块
              display: getComputedStyle(q).display,
            }
          : null;
      };
      out[id] = {
        min: p ? p.min : "",
        pTitle: p ? p.getAttribute("title") : null,
        nTitle: n ? n.getAttribute("title") : null,
        pmark: mark(p),
        nmark: mark(n),
      };
    }
    return out;
  });
  for (const id of ["short", "long", "tk"]) {
    const a = attrs[id] || {};
    check(a.min === "1", "推迟时长 · " + id + " 输入框最小值是 1 分钟", a.min);
    check(
      !!a.pmark && a.pmark.tip === "若触发推迟提醒，每次推迟的时长",
      "推迟时长 · " + id + " 浮层提示挂在文字末尾的“?”上",
      JSON.stringify(a.pmark),
    );
    check(!!a.pmark && a.pmark.last, "推迟时长 · " + id + " “?”在标签文字末尾", JSON.stringify(a.pmark));
    check(
      !!a.nmark && a.nmark.tip === "到达提醒时间前，在屏幕右下角显示提前通知小窗口",
      "提前通知 · " + id + " 浮层提示挂在文字末尾的“?”上",
      JSON.stringify(a.nmark),
    );
    check(!!a.nmark && a.nmark.last, "提前通知 · " + id + " “?”在标签文字末尾", JSON.stringify(a.nmark));
    // 输入框上不能再留 title：同一个说明出现两套提示（系统气泡 + 自绘浮层）
    check(a.pTitle === null, "推迟时长 · " + id + " 输入框上不再挂 title", a.pTitle);
    check(a.nTitle === null, "提前通知 · " + id + " 输入框上不再挂 title", a.nTitle);
  }

  // 短 / 长休息有“计时方法”（段控：倒计时 / 固定时间），外卖提醒只有固定时刻 ——
  // 组的成员因此不同，这条也要钉住。段控的可访问名是“计时方法”，但可见文本里没有
  // 这三个字（靠两段的标签表达），所以按“倒计时”找。
  check(/倒计时/.test(struct.short?.trig || ""), "分组 · 短休息“触发条件”含计时方法");
  check(/倒计时/.test(struct.long?.trig || ""), "分组 · 长休息“触发条件”含计时方法");
  check(!/倒计时/.test(struct.tk?.trig || ""), "分组 · 外卖提醒没有计时方法（只有固定时刻）");
  check(/提醒时长秒/.test(struct.short?.rem || ""), "分组 · 短休息“提醒”含提醒时长");

  // ② 倒计时 ↔ 固定时间：**两侧面板的选中态互换**
  // 2026-11-10 起两个面板并列、内容同时可见可编辑（不再“藏一边显示一边”），
  // 所以这里钉的是 .on 落在哪一块，而不是谁被 display:none。
  const snap = async (id) =>
    await page.evaluate((k) => {
      const card = document.getElementById(k + "-card");
      const host = document.getElementById(k + "-mode");
      const panes = [...host.querySelectorAll(":scope > .mode-pane")];
      const vis = (el) => !!el && el.getBoundingClientRect().height > 0;
      return {
        on: panes.map((p) => p.dataset.mode + ":" + p.classList.contains("on")),
        intervalVisible: vis(document.getElementById(k + "-interval")),
        slotVisible: vis(card.querySelector(".slot-fixed")),
        slotBodyHidden: card.querySelector(".slot-body").classList.contains("hidden"),
        summary: (document.getElementById(k + "-summary") || {}).textContent || "",
      };
    }, id);

  const pickMode = async (id, text) => {
    const hit = await page.evaluate(
      ({ k, t }) => {
        // 2026-11-10 晚起可点的是 .pane-radio（圆点 + 面板名），面板头只是这一行的容器
        const opts = [...document.querySelectorAll(`#${k}-mode .pane-radio`)];
        const el = opts.find((e) => ((e.querySelector(".pane-label") || {}).textContent || "").trim() === t);
        if (!el) return false;
        el.click();
        return true;
      },
      { k: id, t: text },
    );
    if (hit) await waitSettled(page);
    await sleep(200);
    return hit;
  };

  let v = await snap("short");
  check(
    v.on.join(",") === "countdown:true,fixed:false" && v.intervalVisible && v.slotVisible,
    "切换 · 倒计时生效：左侧选中，两侧内容都可见",
    JSON.stringify(v),
  );
  check(await pickMode("short", "固定时间"), "切换 · 在面板头里点“固定时间”");
  v = await snap("short");
  check(
    v.on.join(",") === "countdown:false,fixed:true",
    "切换 · 固定时间生效：选中态移到下块面板",
    JSON.stringify(v),
  );
  // 2026-11-10 起「提醒时间」这一层不再折叠（用户：不准折叠，必须展开），
  // 所以切到固定时间时看到的就是清单本身，不再有“自动摊开”这回事。
  check(v.slotBodyHidden === false, "切换 · “提醒时间”清单常驻展开（内容体没有 hidden）", JSON.stringify(v));
  check(/^固定时间/.test(v.summary), "切换 · 卡片右上摘要跟着变成固定时间", v.summary);

  await pickMode("short", "倒计时");
  v = await snap("short");
  check(v.on.join(",") === "countdown:true,fixed:false", "切换 · 切回倒计时已还原", JSON.stringify(v));

  // ③ 对齐：组已经不是盒子了（2026-11-10 晚：取消折叠、取消边框），
  // 取消 -13px 负外边距之后，所有左缘天然回到卡片内容那条竖线 —— 这一组要钉三件事：
  //   · 组、面板块、系统空闲行、弹窗设置字段行、提醒内容标题**同一条竖线**（用户要的“三项对齐”）
  //   · 组头是**静态标题**：没有按钮、没有 aria-expanded、点它不会收起内容
  //   · 面板头是一句完整的话：圆点 → 面板名 → 「每隔 [框] 分钟提醒一次」/「＋添加」同一行；
  //     时刻表仍比面板名退后一级（层次只剩缩进这一条线索）
  const geo = await page.evaluate(() => {
    const r = (sel) => {
      const el = document.querySelector(sel);
      if (!el) return null;
      const b = el.getBoundingClientRect();
      return { l: Math.round(b.left), r: Math.round(b.right), w: Math.round(b.width), h: Math.round(b.height) };
    };
    // 文字左缘 = 元素左缘 + 内边距（.pg-body 没有内边距了，这个式子在两处都成立）
    const textLeft = (el) => {
      if (!el) return null;
      const b = el.getBoundingClientRect();
      return Math.round(b.left + (parseFloat(getComputedStyle(el).paddingLeft) || 0));
    };
    const card = document.getElementById("short-card");
    const trig = card.querySelector('.pg[data-pg="trigger"]');
    const rem = card.querySelector('.pg[data-pg="reminder"]');
    const msgPanel = card.querySelector(":scope > .msg-panel");
    const panes = [...document.querySelectorAll("#short-mode > .mode-pane")];
    const onPane = panes.find((p) => p.classList.contains("on"));
    const offPane = panes.find((p) => !p.classList.contains("on"));
    const csT = getComputedStyle(trig);
    const csTB = getComputedStyle(trig.querySelector(":scope > .pg-body"));
    const headBtn = trig.querySelector(".pg-head > button, .pg-head > .msg-toggle");
    const countHead = panes[0] && panes[0].querySelector(":scope > .pane-head");
    const fixedHead = panes[1] && panes[1].querySelector(":scope > .pane-head");
    const label0 = countHead && countHead.querySelector(".pane-label");
    const pre0 = countHead && countHead.querySelector(".pane-pre");
    const unit0 = countHead && countHead.querySelector(".pane-inline > .unit");
    return {
      cardHead: r("#short-card > .card-head"),
      // 2026-11-11 起卡头盒子向左多伸 24px（正文缩进 24、卡头用同量负左边距抵消），
      // 于是“对齐基准”从卡头盒子换成**卡头标题文字**和那枚折叠箭头。
      h3: r("#short-card > .card-head > h3"),
      fold: r("#short-card > .card-head > .card-fold"),
      pgHead: r('.pg[data-pg="trigger"] > .pg-head'),
      pgTitle: textLeft(trig.querySelector(".pg-head > .pg-title")),
      pgBody: textLeft(trig.querySelector(":scope > .pg-body")),
      msgToggle: textLeft(msgPanel.querySelector(".msg-head > .msg-toggle")),
      // 三项对齐的对照点：面板块 / 系统空闲整行 / 弹窗设置的字段行
      pane: panes[0] ? r("#short-mode > .mode-pane") : null,
      gate: r('.pg[data-pg="trigger"] .idle-gate'),
      gateLabel: r('.pg[data-pg="trigger"] .idle-gate > label'),
      remLabel: r('.pg[data-pg="reminder"] .row > label'),
      // 面板头那一句话的四个部件（2026-11-10 晚：参数并进标题行）
      paneTitle: textLeft(label0),
      paneLabel: r(".pane-head .pane-label"),
      panePre: r(".pane-head .pane-pre"),
      paneUnit: r(".pane-inline > .unit"),
      // 固定时间头里的“＋添加”
      paneAdd: r("#short-mode .pane-head > .slot-add"),
      // 缩进台阶仅剩的一处：时刻表仍比面板名退后一级
      slotBody: r("#short-card .mode-pane[data-mode='fixed'] .slot-body"),
      // 组本身：不许有边框 / 底色 / 负外边距；内容体不许有内边距与上边框
      pgBorder: csT.borderTopWidth,
      pgBg: csT.backgroundColor,
      pgMarginL: parseFloat(csT.marginLeft) || 0,
      pgBodyPad: parseFloat(csTB.paddingLeft) || 0,
      pgBodyBorder: csTB.borderTopWidth,
      cardBg: getComputedStyle(card).backgroundColor,
      // 组头是静态标题：不该找到按钮
      headBtn: !!headBtn,
      headAria: trig.querySelector(".pg-head").getAttribute("aria-expanded"),
      // 面板底色：未选中 = --bg 中性灰，选中 = 品牌浅蓝；面板内控件是白底
      paneOffBg: offPane ? getComputedStyle(offPane).backgroundColor : "",
      paneOnBg: onPane ? getComputedStyle(onPane).backgroundColor : "",
      paneInputBg: getComputedStyle(document.querySelector("#short-interval")).backgroundColor,
      outerInputBg: getComputedStyle(document.getElementById("short-duration")).backgroundColor,
      // 面板头里的间隔框：宽度必须还是全站那 180，不要被挤成小方框
      paneInputW: Math.round(document.querySelector("#short-interval").getBoundingClientRect().width),
      remInput: r("#short-duration"),
      gateInput: r("#short-idleGate-sec"),
      remUnit: r('.pg[data-pg="reminder"] .row .field-line > .unit'),
      headSwitch: r("#short-card > .card-head > .card-switch"),
      headAct: r("#short-preview"),
      headSum: r("#short-summary"),
      actBorder: getComputedStyle(document.getElementById("short-preview")).borderTopColor,
      actBg: getComputedStyle(document.getElementById("short-preview")).backgroundColor,
      trigInput: r("#short-interval"),
      paneBody: r('#short-mode .mode-pane .pane-body'),
      // 「提醒内容」仍是可折叠的描边分区（长内容），与参数组已经不是同一种长相
      msgBg: getComputedStyle(msgPanel).backgroundColor,
      msgBorder: getComputedStyle(msgPanel).borderTopWidth,
      // 组标题要比行标签更深更重，才能先被读到
      titleSize: parseFloat(getComputedStyle(trig.querySelector(".pg-title")).fontSize),
      titleWeight: getComputedStyle(trig.querySelector(".pg-title")).fontWeight,
      titleColor: getComputedStyle(trig.querySelector(".pg-title")).color,
      labelColor: getComputedStyle(rem.querySelector(".row > label")).color,
      labelSize: parseFloat(getComputedStyle(rem.querySelector(".row > label")).fontSize),
    };
  });
  // 组退成“文字标题 + 内容”：border 0 / 背景透出卡片 / 没有负外边距
  check(
    !!geo && geo.pgBorder === "0px" && geo.pgBg === "rgba(0, 0, 0, 0)" &&
      geo.pgMarginL === 0 && geo.cardBg !== geo.pgBg,
    "分组 · 组不再是一个盒子（无边框、无底色、无负外边距）",
    geo ? `${geo.pgBorder} · ${geo.pgBg} · margin ${geo.pgMarginL} · card ${geo.cardBg}` : "量不到",
  );
  // 内容体也不再“抵消”任何东西：没有内边距、没有上边框
  check(
    !!geo && geo.pgBodyPad === 0 && geo.pgBodyBorder === "0px",
    "分组 · 组内容体没有内边距与上边框（组已经不是盒子，无需抵消）",
    geo ? `pad ${geo.pgBodyPad} · border ${geo.pgBodyBorder}` : "量不到",
  );
  // 取消折叠：组头里没有按钮、没有 aria-expanded
  check(
    !!geo && geo.headBtn === false && geo.headAria === null,
    "分组 · 组头是静态标题（没有折叠按钮、没有 aria-expanded）",
    geo ? `btn ${geo.headBtn} · aria ${geo.headAria}` : "量不到",
  );
  // 点标题不应收起内容（旧实现的残留事件最容易留在这里）
  const foldProbe = await page.evaluate(async () => {
    const pg = document.querySelector('#short-card .pg[data-pg="trigger"]');
    const body = pg.querySelector(":scope > .pg-body");
    const before = { h: Math.round(body.getBoundingClientRect().height), hidden: body.classList.contains("hidden") };
    const t = pg.querySelector(".pg-head > .pg-title");
    if (t) {
      t.click();
      const head = pg.querySelector(".pg-head");
      if (head) head.click();
    }
    await new Promise((r) => setTimeout(r, 260));
    return {
      before,
      after: { h: Math.round(body.getBoundingClientRect().height), hidden: body.classList.contains("hidden") },
      open: pg.classList.contains("open"),
    };
  });
  check(
    foldProbe.before.h > 0 && foldProbe.after.h === foldProbe.before.h &&
      !foldProbe.after.hidden && !foldProbe.open,
    "分组 · 点组标题不会收起内容（组不可折叠，也不带 .open 类）",
    JSON.stringify(foldProbe),
  );
  // 用户要的“三项对齐”（2026-11-11 起基准换成卡头标题文字）：卡头标题 / 组标题 / 组内容 /
  // 面板块 / 系统空闲行 / 弹窗设置字段行 / 提醒内容标题 —— 正文整体缩进 24 后，
  // 这一列从卡头盒子左缘（293）挪到标题文字左缘（317）。
  check(
    !!geo && geo.cardHead && geo.h3 && geo.pgTitle !== null && geo.pgBody !== null &&
      geo.pane && geo.gate && geo.gateLabel && geo.remLabel && geo.msgToggle !== null &&
      Math.abs(geo.pgTitle - geo.h3.l) <= 1 &&
      Math.abs(geo.pgBody - geo.h3.l) <= 1 &&
      Math.abs(geo.pane.l - geo.h3.l) <= 1 &&
      Math.abs(geo.gate.l - geo.h3.l) <= 1 &&
      Math.abs(geo.gateLabel.l - geo.h3.l) <= 1 &&
      Math.abs(geo.remLabel.l - geo.h3.l) <= 1 &&
      Math.abs(geo.msgToggle - geo.h3.l) <= 1,
    "分组 · 正文七点全缩进到卡头标题文字那条竖线",
    JSON.stringify(geo && {
      h3: geo.h3 && geo.h3.l,
      title: geo.pgTitle, body: geo.pgBody,
      pane: geo.pane && geo.pane.l, gate: geo.gate && geo.gate.l,
      rem: geo.remLabel && geo.remLabel.l, msg: geo.msgToggle,
    }),
  );
  // 缩进的由来（用户原话：「各提醒卡片内容都应该增加缩进，至少要比卡片标题左侧的折叠按钮
  // 缩进更大」）。判据写成“正文左缘 ≥ 折叠箭头右缘 + 4”：
  //   · 折叠箭头 284~306，正文 317 → 11px 余量；
  //   · 退回 293（紧贴卡头盒子）就是 -13，缩进只给 16 时也只有 3px，两者都会红 ——
  //     用户要的是“肉眼能看出更深”，4px 是这条下限。
  check(
    !!geo && geo.fold && geo.h3 &&
      geo.pgBody - (geo.fold.l + geo.fold.w) >= 4,
    "分组 · 正文比卡头折叠按钮更深（不是被按钮压着）",
    JSON.stringify(geo && {
      foldR: geo.fold && geo.fold.l + geo.fold.w,
      body: geo.pgBody,
      gap: geo.fold && geo.pgBody - (geo.fold.l + geo.fold.w),
    }),
  );
  // 面板头读成一句话（2026-11-10 晚第五次调整）：圆点 → 面板名 → 「每隔」→ 间隔框 → 单位，
  // 从左到右严格递增、同一行、间距 ≤ 16。谁把参数拆回下面一行（或把它挪到面板名前面），都红。
  const sentence = geo && geo.paneLabel && geo.panePre && geo.trigInput && geo.paneUnit
    ? {
        labelR: geo.paneLabel.l + geo.paneLabel.w,
        preL: geo.panePre.l,
        inputL: geo.trigInput.l,
        inputR: geo.trigInput.l + geo.trigInput.w,
        unitL: geo.paneUnit.l,
        labelT: geo.paneLabel.h,
        inputT: geo.trigInput.h,
      }
    : null;
  check(
    !!sentence &&
      sentence.preL > sentence.labelR && sentence.preL - sentence.labelR <= 16 &&
      sentence.inputL > sentence.preL + 10 && Math.abs(sentence.inputL - sentence.preL - 25) <= 12 &&
      sentence.unitL > sentence.inputR && sentence.unitL - sentence.inputR <= 16,
    "面板 · 倒计时那一行读成一句“◯ 倒计时 每隔 [30] 分钟提醒一次”（同一行、依次向右）",
    JSON.stringify(sentence),
  );
  // 固定时间头里的“＋添加”紧跟面板名（用户要求把它并进标题行，不再有“提醒时间”子标签行）
  check(
    !!geo && geo.paneAdd && geo.paneLabel &&
      geo.paneAdd.l > geo.paneLabel.l + geo.paneLabel.w &&
      geo.paneAdd.l - (geo.paneLabel.l + geo.paneLabel.w) <= 24,
    "面板 · “＋添加”紧跟在「固定时间」后面（并进标题行）",
    JSON.stringify(geo && { add: geo.paneAdd, label: geo.paneLabel }),
  );
  // 缩进的层次只剩一处：时刻表仍比面板名退后一级（16 + 8 + 24 = 48）
  check(
    !!geo && geo.slotBody && geo.pane &&
      geo.slotBody.l - geo.pane.l === 48,
    "缩进 · 时刻表比面板名退后一级（48 = 圆点 16 + 间距 8 + 台阶 24）",
    JSON.stringify(geo && { slot: geo.slotBody && geo.slotBody.l, pane: geo.pane && geo.pane.l }),
  );
  // 面板没有底色（2026-11-10 晚第四次调整，用户：有缩进足以体现层次，不需要背景色块）。
  // 选中 / 未选中都是透明的，映射到卡片白底 —— 互斥只由面板头那枚圆点表达。
  // 这条是反断言：谁把 background 加回来（哪怕是 --accent-weak 那种极浅一档），就红。
  check(
    !!geo && geo.paneOffBg === "rgba(0, 0, 0, 0)" && geo.paneOnBg === "rgba(0, 0, 0, 0)",
    "面板 · 两块都没有底色（选中 / 未选中都是透明的，层次只靠缩进）",
    geo ? `${geo.paneOffBg} vs ${geo.paneOnBg}` : "量不到",
  );
  // 底色去掉之后，面板内的控件必须回到全局那一套 —— 上一版为了对抗淡底给过它们白底，
  // 现在卡片白底直接透出，再保留白底就成了“白底上贴白框”。
  check(
    !!geo && geo.paneInputBg === geo.outerInputBg,
    "面板 · 面板内控件与面板外输入框同一底色（不再为对抗面板底色单独改白）",
    geo ? `in ${geo.paneInputBg} / out ${geo.outerInputBg}` : "量不到",
  );
  // 面板头里的间隔框（2026-11-11 收窄）：用户要求“能完整显示 4 个数字即可”，
  // 于是从全站的 180 收到 64。判据不写死 64 —— 写“宽度够放 4 位数、又不比 4 位数宽太多”，
  // 这样换成 62 或 66 都不会误报，但谁把它改回 180 或者收成 40 都会红。
  const numFit = geo
    ? {
        w: geo.paneInputW,
        textW: await page.evaluate(() => {
          const el = document.getElementById("short-interval");
          const cs = getComputedStyle(el);
          const cv = document.createElement("canvas").getContext("2d");
          cv.font = `${cs.fontWeight} ${cs.fontSize} ${cs.fontFamily}`;
          return Math.round(cv.measureText("9999").width);
        }),
        padW: await page.evaluate(() => {
          const cs = getComputedStyle(document.getElementById("short-interval"));
          return Math.round((parseFloat(cs.paddingLeft) || 0) + (parseFloat(cs.paddingRight) || 0) + 2);
        }),
      }
    : null;
  check(
    !!numFit && numFit.w >= numFit.textW + numFit.padW && numFit.w <= numFit.textW + numFit.padW + 26,
    "面板 · 标题行里的间隔框只比“4 位数 + 内边距 + 原生步进箭头”略宽（不再占 180）",
    numFit ? `框 ${numFit.w} = 数字 ${numFit.textW} + 内边距 ${numFit.padW}` : "量不到",
  );
  // 亮度比较：组标题应当比行标签更深（浅色主题下 rgb 越大越亮）
  check(
    !!geo && (() => {
      const lum = (c) => {
        const m = String(c).match(/\d+/g) || [];
        return (+m[0]) * 0.299 + (+m[1]) * 0.587 + (+m[2]) * 0.114;
      };
      return Math.abs(lum(geo.titleColor) - lum(geo.labelColor)) >= 12;
    })(),
    "分组 · 组标题与行标签是不同的灰度（标题先于行标签被读到）",
    geo ? geo.titleColor + " vs " + geo.labelColor : "量不到",
  );
  // 组标题 13/700：比组内子标签（12/700 灰）与字段标签都重一档
  check(
    !!geo && geo.titleSize === 13 && (geo.titleWeight === "700" || geo.titleWeight === "bold"),
    "分组 · 组标题 13px / 700（先于内容被读到）",
    geo ? `${geo.titleSize}px / ${geo.titleWeight}` : "量不到",
  );
  // 数值框 + 框外单位是同一套度量单元：面板外的两个框同左缘、同宽，单位紧跟
  check(
    !!geo && geo.remInput && geo.gateInput && geo.remUnit &&
      Math.abs(geo.gateInput.l - geo.remInput.l) <= 1 &&
      Math.abs(geo.remInput.w - geo.gateInput.w) <= 1 &&
      geo.remUnit.l > geo.remInput.l + geo.remInput.w &&
      geo.remUnit.l - (geo.remInput.l + geo.remInput.w) <= 12,
    "分组 · 面板外的数值框同左缘同宽，单位紧跟在框右侧",
    JSON.stringify(geo && { rem: geo.remInput, gate: geo.gateInput, unit: geo.remUnit }),
  );
  // 整句话不许顶出卡片：单位右缘还在卡片内容右缘之内
  check(
    !!geo && geo.paneUnit && geo.cardHead && geo.paneUnit.l + geo.paneUnit.w <= geo.cardHead.l + geo.cardHead.w + 1,
    "面板 · “分钟提醒一次”没有顶出卡片内容右缘（窄窗口靠框收缩让位）",
    JSON.stringify(geo && { unitR: geo.paneUnit && geo.paneUnit.l + geo.paneUnit.w, cardR: geo.cardHead && geo.cardHead.l + geo.cardHead.w }),
  );
  // 「提醒内容」仍是可折叠的描边分区 —— 组取消边框之后，长内容这一层还留着盒子，
  // 两者必须能被区分开（同一个 1px 描边 + 头部按钮）
  check(
    !!geo && geo.msgBorder === "1px" && geo.msgBg === "rgba(0, 0, 0, 0)",
    "分组 · 「提醒内容」仍是描边折叠分区（长内容与参数组已分属两种长相）",
    geo ? geo.msgBg + " · border " + geo.msgBorder : "量不到",
  );
  // 卡头顺序（2026-11-09 二次调整）：标题 → 摘要 → 预览 → 总开关。
  // 开关**收在行尾**而不是紧跟标题：贴着标题时它读起来像标题的后缀（“短休息〔开〕”），
  // 而且那是一枚实心品牌色胶囊，会比 14px 灰标题先被看到。摘要反过来要紧跟标题 ——
  // 它是这张卡的当前节奏，属于标题的信息；若把摘要也推去行尾，左端只剩一个 42px 宽的
  // 灰标题，整行重量全压在右侧控件簇上，怎么摆都失衡。
  check(
    !!geo && geo.headSwitch && geo.headAct && geo.headSum &&
      geo.headSum.l > geo.cardHead.l &&
      geo.headAct.l > geo.headSum.l + geo.headSum.w &&
      geo.headAct.l + geo.headAct.w <= geo.headSwitch.l &&
      Math.abs(geo.headSwitch.l + geo.headSwitch.w - (geo.cardHead.l + geo.cardHead.w)) <= 2,
    "分组 · 卡头内顺序：标题 → 摘要 → 预览 → 总开关（开关收在行尾、不贴标题）",
    JSON.stringify(geo && { sum: geo.headSum, act: geo.headAct, sw: geo.headSwitch, head: geo.cardHead }),
  );
  check(
    !!geo && geo.headSum && geo.headAct &&
      geo.headSum.l - geo.cardHead.l < 200 &&
      geo.headSum.l + geo.headSum.w < geo.headAct.l,
    "分组 · 卡头摘要紧跟标题（不飘到行尾，左端才是完整信息块）",
    JSON.stringify(geo && { sum: geo.headSum, head: geo.cardHead }),
  );
  // 2026-11-10 卡头重排：预览从描边胶囊降级为**文本动作**，与总开关之间留出一段空白，
  // 行尾只留总开关一枚实体件（原来两枚 20px 高的小圆角件并排只隔 2px，会被读成“两个按钮”）。
  const actGap = geo && geo.headSwitch && geo.headAct
    ? geo.headSwitch.l - (geo.headAct.l + geo.headAct.w)
    : -1;
  check(
    !!geo && geo.actBorder === "rgba(0, 0, 0, 0)" && geo.actBg === "rgba(0, 0, 0, 0)" &&
      actGap >= 18,
    "卡头 · 预览是文本动作（无描边无底色），与总开关之间留出空白",
    geo ? `${geo.actBorder} / ${geo.actBg} · gap ${actGap}` : "量不到",
  );

  // 跨卡对齐（2026-11-10）：卡头里的同类元素必须在上下同一条竖线上 ——
  // ▾ 与标题左缘、预览右缘、开关左右缘。摘要紧跟标题，宽度随内容自然变化，不参与这条。
  // 自定义卡的 ✎ / ✕ 是后插的组，最容易把预览与开关挤出对齐线，所以它单独在
  // verifyIdleGate 的自定义卡段里与短休息卡对拍。
  const headAlign = await page.evaluate(() => {
    const cards = [...document.querySelectorAll("#tab-timing > .card")].filter((c) =>
      c.querySelector(":scope > .card-head > .card-switch > input"),
    );
    return cards.map((c) => {
      const box = (sel) => {
        const el = c.querySelector(sel);
        if (!el) return null;
        const b = el.getBoundingClientRect();
        return { l: Math.round(b.left), r: Math.round(b.right) };
      };
      return {
        name: ((c.querySelector(":scope > .card-head h3") || {}).textContent || "").trim(),
        h3: box(":scope > .card-head > h3"),
        act: box(":scope > .card-head > .card-head-act"),
        sw: box(":scope > .card-head > .card-switch"),
      };
    });
  });
  const sameLine = (vals) =>
    vals.length >= 2 && vals.every((v) => v !== null && Math.abs(v - vals[0]) <= 1);
  check(
    headAlign.length >= 3 &&
      sameLine(headAlign.map((x) => x.h3 && x.h3.l)) &&
      sameLine(headAlign.map((x) => x.act && x.act.r)) &&
      sameLine(headAlign.map((x) => x.sw && x.sw.l)) &&
      sameLine(headAlign.map((x) => x.sw && x.sw.r)),
    "卡头 · 各卡标题左缘 / 预览右缘 / 开关左右缘都在同一条竖线上",
    JSON.stringify(headAlign),
  );

  // 卡头总开关是控件、不是折叠热区（2026-11-09）：点它只切开关，卡片不能跟着收起。
  // 三层都要验 —— 透明 input 本身、轨道 span（点到圆点或上下边缘）、键盘空格。
  // 曾经漏掉 span 这一层：input 被全局 `input[type=checkbox]{width:16px}` 钉成 16px 没铺满，
  // 点轨道边缘时 target 是 span，冒泡到 onFoldClick 把卡收起来了。折叠本身仍要能折叠，
  // 所以顺手验一次箭头；全部验完把开关与折叠态恢复原样，别把状态漏给后面的断言。
  const foldState = () =>
    page.evaluate(() => ({
      closed: document.getElementById("short-card").classList.contains("closed"),
      on: document.getElementById("short-enabled").checked,
    }));
  await page.evaluate(() => {
    document.getElementById("short-card").classList.remove("closed");
  });
  const beforeSwitch = await foldState();
  await page.click("#short-card .card-head > .card-switch");
  const afterLabel = await foldState();
  check(
    !afterLabel.closed && afterLabel.on !== beforeSwitch.on,
    "卡头 · 点总开关只切开关、不折叠卡片",
    JSON.stringify({ before: beforeSwitch, after: afterLabel }),
  );
  const track = await page.evaluate(() => {
    const b = document.querySelector("#short-card .card-switch-track").getBoundingClientRect();
    return { x: b.left, y: b.top, w: b.width, h: b.height };
  });
  // 关闭态（2026-11-10）：拨到 off 后整卡正文降透明度、卡头不降 —— 关掉这件事不能只有
  // 那枚 34×20 的小胶囊在说，卡内控件照旧全亮时用户扫一眼看不出这张卡是停着的。
  // 只降透明度、不禁用控件（关掉是“暂时不弹”，参数仍要能改）。
  // 正文的 opacity 带 0.2s 过渡，必须等它落定再量，否则量到的是过渡中的 1。
  await page.waitForTimeout(300);
  const offState = await page.evaluate(() => {
    const card = document.getElementById("short-card");
    const body = card.querySelector(":scope > .pg");
    return {
      off: card.classList.contains("off"),
      body: parseFloat(getComputedStyle(body).opacity),
      head: parseFloat(getComputedStyle(card.querySelector(":scope > .card-head")).opacity),
    };
  });
  check(
    offState.off === !afterLabel.on &&
      offState.head === 1 &&
      (afterLabel.on ? offState.body === 1 : offState.body < 0.7),
    "卡头 · 关闭时正文降透明度、卡头不变（关闭态一眼可见）",
    JSON.stringify({ on: afterLabel.on, ...offState }),
  );
  await page.mouse.click(track.x + track.w - 3, track.y + 2); // 轨道右上角 = span，不是 input
  const afterEdge = await foldState();
  check(
    !afterEdge.closed,
    "卡头 · 点轨道边缘（命中 span）也不折叠",
    JSON.stringify(afterEdge),
  );
  await page.evaluate(() => document.getElementById("short-enabled").focus());
  await page.keyboard.press("Space");
  await page.waitForTimeout(120);
  const afterKey = await foldState();
  check(!afterKey.closed, "卡头 · 键盘空格切开关不折叠", JSON.stringify(afterKey));
  await page.click("#short-card .card-fold");
  await page.waitForTimeout(120);
  check(
    (await foldState()).closed,
    "卡头 · 折叠箭头仍能折叠卡片",
    JSON.stringify(await foldState()),
  );
  await page.click("#short-card .card-fold");
  await page.waitForTimeout(120);
  const restored = await foldState();
  if (restored.on !== beforeSwitch.on) {
    await page.click("#short-card .card-head > .card-switch");
    await page.waitForTimeout(120);
  }
  check(
    !(await foldState()).closed,
    "卡头 · 验完恢复原状（展开）",
    JSON.stringify(await foldState()),
  );
  check(
    await page.evaluate(() => {
      const card = document.getElementById("short-card");
      return card.classList.contains("off") === !document.getElementById("short-enabled").checked;
    }),
    "卡头 · 恢复后关闭态跟着开关同步（没有残留 .off）",
  );
  check(
    !!geo && geo.titleSize < geo.labelSize,
    "分组 · 组标题字号小于行标签",
    geo ? geo.titleSize + " < " + geo.labelSize : "量不到",
  );

  // ④ 暗色主题下也成立（组标题走 --text，两套主题各一份变量）
  const lightColor = await page.evaluate(
    () => getComputedStyle(document.querySelector("#short-card .pg .pg-title")).color,
  );
  await page.evaluate(() => {
    document.documentElement.dataset.theme = "dark";
  });
  await sleep(250);
  const dark = await page.evaluate(() => {
    const t = document.querySelector('#short-card .pg[data-pg="trigger"] .pg-title');
    return { color: getComputedStyle(t).color };
  });
  check(dark.color !== lightColor, "分组 · 组标题颜色跟随主题（暗色下换色）", lightColor + " → " + dark.color);
  await page.evaluate(() => {
    document.documentElement.dataset.theme = "light";
  });
  await sleep(200);

  // ⑤ 组不可折叠（2026-11-10 晚，用户给参考图：触发条件 / 弹窗设置取消折叠、取消边框）。
  // 这一条取代了原来那三段“点开 / 点收 / 自定义卡也能开合”的用例 —— 现在组头里
  // 根本没有按钮，点标题也不该有任何反应。只改 CSS 不删 JS 是这里最典型的错法，
  // 所以断言写死：没有 button、没有 aria-expanded、没有 .msg-arrow、组不带 .open。
  const pgHead = await page.evaluate(() => {
    const pg = document.querySelector('#short-card .pg[data-pg="trigger"]');
    const head = pg.querySelector(":scope > .pg-head");
    const title = head.querySelector(".pg-title");
    const cs = title ? getComputedStyle(title) : null;
    return {
      btns: head.querySelectorAll("button").length,
      toggles: head.querySelectorAll(".msg-toggle").length,
      arrows: head.querySelectorAll(".msg-arrow").length,
      aria: head.getAttribute("aria-expanded"),
      open: pg.classList.contains("open"),
      // 标题自己也必须是“一段文字”：没有内边距、没有圆角、没有 cursor:pointer
      pad: cs ? cs.paddingTop + "/" + cs.paddingLeft : null,
      radius: cs ? cs.borderTopLeftRadius : null,
      cursor: cs ? cs.cursor : null,
      text: title ? title.textContent.trim() : null,
    };
  });
  check(
    pgHead.btns === 0 && pgHead.toggles === 0 && pgHead.arrows === 0 &&
      pgHead.aria === null && pgHead.open === false,
    "分组 · 组头里没有折叠按钮 / 箭头 / aria-expanded，组也不带 .open",
    JSON.stringify(pgHead),
  );
  check(
    pgHead.text === "触发条件" && pgHead.pad === "0px/0px" && pgHead.radius === "0px" &&
      pgHead.cursor !== "pointer",
    "分组 · 组标题是一段静态文字（无内边距、无圆角、不是可点区域）",
    JSON.stringify(pgHead),
  );
  // ⑥ 组标题的装饰（2026-11-11，用户：「触发条件」「弹窗设置」标题加一点装饰）。
  // 判据只钉“它是记号，不是盒子”，以及“它没有把文字推开”：
  //   · 竖条 2~5px × 11~18px，绝对定位 —— 一旦有人把它做成 padding / flex 前置元素，
  //     gap 那条就会变成 0，随之“标题左缘 = 标题文字左缘”那条也会红；
  //   · 竖条右缘到文字左缘留 2~10px；
  //   · 竖条整体落在“卡片左缘 → 正文左缘”这段缩进区里（不贴卡边、不压正文）；
  //   · background-image 必须是 gradient —— `color-mix()` 万一不被支持，整条渐变会失效
  //     退成 none，这条会第一个红（比肉眼看截图可靠）。
  const deco = await page.evaluate(() => {
    const card = document.getElementById("short-card").getBoundingClientRect();
    return [...document.querySelectorAll("#short-card .pg .pg-title")].map((t) => {
      const cs = getComputedStyle(t, "::before");
      const tb = t.getBoundingClientRect();
      const left = parseFloat(cs.left) || 0;
      const w = parseFloat(cs.width) || 0;
      const pg = t.closest(".pg");
      const bodyEl = pg && pg.querySelector(":scope > .pg-body");
      return {
        text: t.textContent.trim(),
        content: cs.content,
        pos: cs.position,
        w,
        h: parseFloat(cs.height) || 0,
        gap: -left - w,
        // 缩进区里的位置：竖条左缘、竖条右缘（都是相对卡片左缘）
        inset: Math.round(tb.left + left - card.left),
        insetR: Math.round(tb.left + left + w - card.left),
        // 正文左缘（相对卡片左缘）—— 正文体没有内边距，这个左缘就是文字左缘
        bodyInset: bodyEl ? Math.round(bodyEl.getBoundingClientRect().left - card.left) : null,
        bg: cs.backgroundImage,
        pad: getComputedStyle(t).paddingLeft,
      };
    });
  });
  check(
    deco.length >= 2 &&
      deco.every(
        (d) =>
          d.content !== "none" && d.pos === "absolute" &&
          d.w >= 2 && d.w <= 5 && d.h >= 11 && d.h <= 18 &&
          d.gap >= 2 && d.gap <= 10 &&
          d.inset >= 10 && d.bodyInset !== null && d.insetR <= d.bodyInset &&
          d.bg !== "none" && d.pad === "0px",
      ),
    "分组 · 组标题左侧的装饰竖条：绝对定位、不推开文字、落在卡片缩进区里",
    JSON.stringify(deco),
  );
  // 全页扫一遍：所有 .pg 都必须常驻展开（这三段是同一份模板复制的，漏一处就会露出来）
  const allPg = await page.evaluate(() =>
    [...document.querySelectorAll(".pg")].map((pg) => {
      const body = pg.querySelector(":scope > .pg-body");
      return {
        pg: pg.dataset.pg,
        open: pg.classList.contains("open"),
        hidden: body ? body.classList.contains("hidden") : null,
        display: body ? getComputedStyle(body).display : null,
        h: body ? Math.round(body.getBoundingClientRect().height) : 0,
        btns: pg.querySelectorAll(":scope > .pg-head button").length,
      };
    }),
  );
  check(
    allPg.length >= 6 &&
      allPg.every((p) => !p.open && p.hidden === false && p.display !== "none" && p.h > 0 && p.btns === 0),
    "分组 · 全页所有组都常驻展开（无 .open、无 hidden、有实际高度、头部无按钮）",
    JSON.stringify(allPg),
  );
  // 克隆出来的自定义卡继承同一份静态标题：同样没有按钮、内容常驻
  const cloned = await page.evaluate(() => {
    const card = document.querySelector("#tab-timing > .card[data-cb]");
    if (!card) return null;
    const pg = card.querySelector('.pg[data-pg="reminder"]');
    const body = pg && pg.querySelector(":scope > .pg-body");
    return {
      btns: pg ? pg.querySelectorAll(":scope > .pg-head button").length : -1,
      open: pg ? pg.classList.contains("open") : null,
      hidden: body ? body.classList.contains("hidden") : null,
      h: body ? Math.round(body.getBoundingClientRect().height) : 0,
    };
  });
  check(
    cloned === null || (cloned.btns === 0 && cloned.open === false && cloned.hidden === false && cloned.h > 0),
    "分组 · 自定义卡（克隆）的组同样不可折叠",
    JSON.stringify(cloned),
  );
  // 「提醒时间」这一层**整体不可折叠**（2026-11-10 用户要求：不准折叠，必须展开）。
  // 全页扫一遍：任何 .slot-fixed 都不许再挂折叠开关，内容体也不许有 hidden 类。
  // 扫全页而不是只看短休息卡，是因为这三块是同一段模板复制出来的，漏改一处就会露出来。
  // 2026-11-10 晚起短 / 长休息的「提醒时间」子标签行**并进了面板头**（“固定时间 ＋添加”），
  // 因此这两块的 .slot-fixed 里没有 .slot-head；外卖卡的「提醒时间」是组内独立子标签，
  // 仍然保留 head —— 两种长相各自钉住，别让谁把 head 又塞回休息卡里。
  const slots = await page.evaluate(() =>
    [...document.querySelectorAll(".slot-fixed")].map((p) => ({
      toggle: p.querySelectorAll(".msg-toggle").length,
      head: !!p.querySelector(".slot-head .slot-title"),
      inPane: !!p.closest(".pane-body"),
      bodyHidden: !!(p.querySelector(".slot-body") || {}).classList?.contains("hidden"),
    })),
  );
  check(
    slots.length >= 3 &&
      slots.every((s) => s.toggle === 0 && s.head !== s.inPane && !s.bodyHidden),
    "时刻表 · 全部「提醒时间」都不折叠（休息卡里的并进面板头、外卖卡仍留子标签）",
    JSON.stringify(slots),
  );
}

// 计时方法：上下两块面板（.mode-panes）
//
// 2026-11-10 由两段式段控改成面板（用户要求）：**上面倒计时、下面固定时间**，
// 选哪种方式，那块的东西就在那块，不用先看上面一排选择、再低头去下面一组行里找。
// 起初是左右并列，实机太挤（每块只剩 ~380，时刻行只能把框收到 72/68），当天改成
// 上下排列：每块回到整行宽度，时刻行恢复满宽五列。
// 2026-11-10 晚第五次调整（用户：删掉两句副标题、把「间隔」并进倒计时标题行、
// 把「添加」并进固定时间标题行、删掉「提醒时间」那一行）：面板头变成一句完整的话，
// 面板里只剩真正从属的东西（时刻表 / 每 N 次）。
// 这一组钉住最容易回退的七处：
//   · 语义：radiogroup / radio / aria-checked 互斥、整组只有一个 tab 停靠点，
//     role=radio 落在 .pane-radio（圆点 + 面板名）上，不是整行 —— 整行还装着输入框
//   · 归属：倒计时的间隔框在**标题行**里、固定时间的时刻表在**内容体**里
//   · 文案：面板头只有面板名（副标题「每隔一段时间」「每天指定时刻」已删）
//   · 交互：点圆点 / 面板名 → 选中态互换 + 配置落盘 + 摘要跟着变；两块内容都可见可编辑
//   · 不去连带：点间隔框、点面板头空白处都不许切边（2026-11-11 用户要求）
//   · 键盘：方向键在两块之间换位、Home / End 到首末
//   · 几何：两块等宽、上下排列且挨得紧（行间 2~8px）、两块头等高、
//     左缘与组同一条竖线、时刻表退后一级缩进
async function verifyModePanes(page) {
  console.log("\n[timing] 计时方法（上下两块面板）");
  await page.evaluate(() => {
    [...document.querySelectorAll(".nav-item")].find((b) => b.dataset.tab === "timing").click();
  });
  await sleep(350);

  // ① 结构 / 语义 / 文案 / 内容归属
  const st = await page.evaluate(() => {
    const one = (k) => {
      const host = document.getElementById(k + "-mode");
      if (!host) return null;
      const panes = [...host.querySelectorAll(":scope > .mode-pane")];
      const info = (p) => {
        const head = p.querySelector(":scope > .pane-head");
        const radio = p.querySelector(":scope > .pane-head > .pane-radio");
        const body = p.querySelector(":scope > .pane-body");
        return {
          mode: p.dataset.mode,
          on: p.classList.contains("on"),
          role: radio && radio.getAttribute("role"),
          checked: radio && radio.getAttribute("aria-checked"),
          tab: radio ? radio.tabIndex : null,
          b: radio ? ((radio.querySelector(".pane-label") || {}).textContent || "") : "",
          // 副标题（.pane-txt > i）已删：面板头里不该再有第二段解释文字
          sub: head ? ((head.querySelector(".pane-txt i, .pane-head i") || {}).textContent || "") : "",
          ico: !!(head && head.querySelector("svg")),
          // 单选圆点是 .pane-radio 的 ::before（不占 DOM），读伪元素样式钉存在与两态
          dot: radio
            ? (() => {
                const cs = getComputedStyle(radio, "::before");
                return { w: cs.width, h: cs.height, r: cs.borderRadius, bg: cs.backgroundImage };
              })()
            : null,
          // 参数并进标题行：倒计时的间隔框在 head 里；固定时间的“＋添加”也在 head 里
          intervalInHead: !!(head && head.querySelector("#" + k + "-interval")),
          addInHead: !!(head && head.querySelector(".slot-add")),
          intervalInBody: !!(body && body.querySelector("#" + k + "-interval")),
          hasSlot: !!(body && body.querySelector(".slot-fixed")),
        };
      };
      return {
        cls: host.className,
        role: host.getAttribute("role"),
        label: host.getAttribute("aria-label"),
        panes: panes.map(info),
      };
    };
    return { short: one("short"), long: one("long"), tk: one("tk") };
  });

  for (const id of ["short", "long"]) {
    const s = st[id];
    if (!check(!!s, "面板 · " + id + " 有计时方法控件")) continue;
    check(/mode-panes/.test(s.cls), "面板 · " + id + " 是 .mode-panes（不再是段控宿主）", s.cls);
    check(
      s.role === "radiogroup" && s.label === "计时方法",
      "面板 · " + id + " role=radiogroup 且可访问名为“计时方法”",
      s.role + " / " + s.label,
    );
    check(
      s.panes.length === 2 && s.panes[0].mode === "countdown" && s.panes[1].mode === "fixed",
      "面板 · " + id + " 上块倒计时、下块固定时间（顺序不许反）",
      s.panes.map((p) => p.mode).join("/"),
    );
    check(
      s.panes.every((p) => p.role === "radio"),
      "面板 · " + id + " 两块的圆点按钮都是 role=radio（不是整行）",
      s.panes.map((p) => p.role).join(","),
    );
    check(
      s.panes.filter((p) => p.checked === "true").length === 1,
      "面板 · " + id + " aria-checked 恰好一侧为 true",
      s.panes.map((p) => p.checked).join(","),
    );
    check(
      s.panes.filter((p) => p.tab === 0).length === 1,
      "面板 · " + id + " 整组只有一个 tab 停靠点（另一块 -1）",
      s.panes.map((p) => p.tab).join(","),
    );
    // 面板头只有面板名：副标题「每隔一段时间」「每天指定时刻」按用户要求删掉了
    check(
      s.panes.map((p) => p.b).join("|") === "倒计时|固定时间" &&
        s.panes.every((p) => p.sub === ""),
      "面板 · " + id + " 面板头只有面板名（副标题已删）",
      s.panes.map((p) => p.b + "/" + p.sub).join("|"),
    );
    // 图标已按用户要求去掉，换成单选圆点 —— 两者一起钉：谁把 svg 加回来、或把圆点删掉，都红
    check(!s.panes.some((p) => p.ico), "面板 · " + id + " 面板头里已经没有图标 svg");
    // 16px：与「系统空闲」那行原生勾选框同尺寸（全局 input[type=checkbox] 就是 16×16），
    // 两行的行首因此同大 —— 用户明确要求“单选圆点与复选框大小相同”
    check(
      s.panes.every((p) => p.dot && p.dot.w === "16px" && p.dot.h === "16px" && p.dot.r === "50%"),
      "面板 · " + id + " 两块头部各有一个 16px 的正圆（与系统空闲复选框同尺寸）",
      s.panes.map((p) => (p.dot ? p.dot.w + "/" + p.dot.r : "null")).join(","),
    );
    // 互斥的两态：选中那块中心实心（径向渐变），未选中那块只有一圈空环
    const onPane = s.panes.find((p) => p.on);
    check(
      !!onPane && /radial-gradient/.test(onPane.dot.bg),
      "互斥 · 选中那块圆点中心是实心的",
      onPane && onPane.dot.bg,
    );
    check(
      s.panes.filter((p) => !p.on).every((p) => p.dot.bg === "none"),
      "互斥 · 未选中那块圆点是空心的（只画外圈，不提前填充）",
      s.panes.filter((p) => !p.on).map((p) => p.dot.bg).join("|"),
    );
    // 内容归属（2026-11-10 晚）：倒计时的间隔框并进了**标题行**，固定时间的“＋添加”
    // 也并进了标题行；时刻表留在内容体里。谁把间隔框搬回下面的行、或把时刻表搬进标题行，都红。
    check(
      s.panes[0].intervalInHead && !s.panes[0].intervalInBody && !s.panes[0].hasSlot &&
        s.panes[1].addInHead && !s.panes[1].intervalInHead && s.panes[1].hasSlot,
      "面板 · " + id + " 间隔框在倒计时标题行、时刻表在固定时间内容体",
      s.panes
        .map((p) => (p.intervalInHead ? "间隔@头" : "") + (p.addInHead ? "添加@头" : "") + (p.hasSlot ? "时刻表@体" : ""))
        .join("/"),
    );
  }
  check(!st.tk, "面板 · 外卖提醒没有计时方法（只有固定时刻）", st.tk && st.tk.panes.length);

  // ② 点击 → 选中态 / 摘要 / 配置（两侧内容都可见，不再有显隐）
  const clickPane = async (id, mode) => {
    const hit = await page.evaluate(
      ({ k, m }) => {
        const el = document.querySelector(`#${k}-mode .mode-pane[data-mode="${m}"] > .pane-head > .pane-radio`);
        if (!el) return false;
        el.click();
        return true;
      },
      { k: id, m: mode },
    );
    if (hit) await waitSettled(page);
    await sleep(220);
    return hit;
  };
  const viewLong = async () =>
    await page.evaluate(() => {
      const host = document.getElementById("long-mode");
      const panes = [...host.querySelectorAll(":scope > .mode-pane")];
      const vis = (el) => !!el && el.getBoundingClientRect().height > 0;
      return {
        value: host.dataset.value,
        on: panes.map((p) => p.dataset.mode + ":" + p.classList.contains("on")),
        checked: panes.map((p) => (p.querySelector(".pane-radio") || {}).getAttribute?.("aria-checked")),
        intervalVisible: vis(document.getElementById("long-interval")),
        slotVisible: vis(document.querySelector("#long-card .slot-fixed")),
        summary: ((document.getElementById("long-summary") || {}).textContent || "").trim(),
      };
    });

  check(await clickPane("long", "fixed"), "面板 · 点“固定时间”那一块");
  let cfg = await readCfg(page);
  let ui = await viewLong();
  check(cfg.long.mode === "fixed", "面板 · 写入 long.mode=fixed", cfg.long.mode);
  check(ui.value === "fixed" && ui.checked.join(",") === "false,true", "面板 · 选中态互换", ui.checked.join(","));
  check(
    ui.on.join(",") === "countdown:false,fixed:true",
    "面板 · .on 只落在固定时间那一块",
    ui.on.join(","),
  );
  check(
    ui.intervalVisible === true && ui.slotVisible === true,
    "面板 · 两块内容同时可见（上下排列，不是二选一隐藏）",
    JSON.stringify({ i: ui.intervalVisible, s: ui.slotVisible }),
  );
  check(/^固定时间/.test(ui.summary), "面板 · 卡片摘要跟着变", ui.summary);

  check(await clickPane("long", "countdown"), "面板 · 点回“倒计时”");
  cfg = await readCfg(page);
  ui = await viewLong();
  check(cfg.long.mode === "countdown", "面板 · 写回 long.mode=countdown", cfg.long.mode);
  check(ui.on.join(",") === "countdown:true,fixed:false", "面板 · 选中态回到上面那块", ui.on.join(","));

  // ③ 只有“点圆点 / 面板名”才算选择（2026-11-11 用户要求）。
  //    此前这里挂着一条 pointerdown 委托：在未选中那块的间隔框上按下就自动切过去。
  //    用户不要这个“顺手切边”—— 改一个数字和换一种计时方式不该是同一个动作。
  //    这条断言正着量：在未选中那块的**其他控件**上按下并点击，选中态、aria 与配置都不许动。
  await clickPane("long", "fixed");
  const auto = await page.evaluate(() => {
    const host = document.getElementById("long-mode");
    const pane = host.querySelector('.mode-pane[data-mode="countdown"]');
    const poke = (el) => {
      if (!el) return;
      el.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, cancelable: true }));
      el.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
      el.dispatchEvent(new PointerEvent("pointerup", { bubbles: true, cancelable: true }));
    };
    poke(document.getElementById("long-interval")); // 点“每隔 [30] 分钟”那个框
    poke(pane.querySelector(".pane-head")); // 点面板头的空白处（不在圆点与面板名上）
    return {
      value: host.dataset.value,
      on: [...host.querySelectorAll(":scope > .mode-pane")].map((p) => p.dataset.mode + ":" + p.classList.contains("on")),
      checked: [...host.querySelectorAll(".pane-radio")].map((r) => r.getAttribute("aria-checked")),
    };
  });
  await waitSettled(page);
  await sleep(180);
  const autoCfg = await readCfg(page);
  check(
    auto.value === "fixed" && autoCfg.long.mode === "fixed" && auto.checked.join(",") === "false,true",
    "面板 · 点间隔框 / 面板头空白处**不会**连带勾选（换方式只能点圆点或面板名）",
    JSON.stringify({ ui: auto.on, aria: auto.checked, cfg: autoCfg.long.mode }),
  );

  // ④ 键盘：焦点落在选中那一块的圆点按钮上，方向键换位
  await clickPane("long", "countdown");
  await page.evaluate(() => {
    document.querySelector("#long-mode .mode-pane.on > .pane-head > .pane-radio").focus();
  });
  const from = await page.evaluate(() => document.activeElement.closest(".mode-pane").dataset.mode);
  await page.keyboard.press("ArrowRight");
  await sleep(220);
  const kbd = await page.evaluate(() => {
    const host = document.getElementById("long-mode");
    const panes = [...host.querySelectorAll(":scope > .mode-pane")];
    return {
      focus: document.activeElement.closest(".mode-pane")
        ? document.activeElement.closest(".mode-pane").dataset.mode
        : null,
      on: panes.map((p) => p.classList.contains("on")),
    };
  });
  check(kbd.focus !== from, "面板 · ArrowRight 把焦点移到另一半", from + " → " + kbd.focus);
  check(
    kbd.on.join(",") === (from === "countdown" ? "false,true" : "true,false"),
    "面板 · 键盘换位同时改选中态",
    kbd.on.join(","),
  );
  // 回到起始值：这组断言不许给后面的用例留状态
  await clickPane("long", from);
  await page.keyboard.press("Tab");

  // ⑤ 几何：两块上下排列、等宽、左缘与组同一条竖线、时刻表退后一级
  const geo = await page.evaluate(() => {
    const r = (el) => {
      if (!el) return null;
      const b = el.getBoundingClientRect();
      return { l: Math.round(b.left), r: Math.round(b.right), w: Math.round(b.width), h: Math.round(b.height), t: Math.round(b.top), b: Math.round(b.bottom) };
    };
    const card = document.getElementById("short-card");
    const host = document.getElementById("short-mode");
    const pgBox = card.querySelector('.pg[data-pg="trigger"]');
    const panes = [...host.querySelectorAll(":scope > .mode-pane")];
    const radios = panes.map((p) => p.querySelector(":scope > .pane-head > .pane-radio"));
    const fixedBody = panes[1] && panes[1].querySelector(":scope > .pane-body");
    // 内容左缘 = 内容体左缘 + 左内边距（.pane-body 就是那条 48px 缩进）
    const contentL = (el) => {
      if (!el) return -1;
      const b = el.getBoundingClientRect();
      return Math.round(b.left + (parseFloat(getComputedStyle(el).paddingLeft) || 0));
    };
    return {
      host: r(host),
      pgBox: r(pgBox),
      top: r(panes[0]),
      bottom: r(panes[1]),
      topContentL: panes[0] ? contentL(panes[0].querySelector(":scope > .pane-body")) : -1,
      bottomContentL: panes[1] ? contentL(fixedBody) : -1,
      slotBodyL: fixedBody ? Math.round(fixedBody.querySelector(".slot-body").getBoundingClientRect().left) : -1,
      topOp: panes[0] ? parseFloat(getComputedStyle(panes[0]).opacity) : -1,
      bottomOp: panes[1] ? parseFloat(getComputedStyle(panes[1]).opacity) : -1,
      // 组内字段行（系统空闲门）的标签：面板这一行的圆点必须与它同一条竖线
      gateLabel: r(card.querySelector('.pg[data-pg="trigger"] .idle-gate > label')),
      // 系统空闲那行的原生勾选框：圆点要与它同左缘、同尺寸
      gateCheck: (() => {
        const cb = card.querySelector("#short-idleGate");
        if (!cb) return null;
        const b = cb.getBoundingClientRect();
        return { l: Math.round(b.left), w: Math.round(b.width) };
      })(),
      // 圆点是 .pane-radio 的 ::before，而且 .pane-radio 没有内边距 —— 它的左缘就是圆点左缘
      dotL: radios[0] ? Math.round(radios[0].getBoundingClientRect().left) : 0,
      dotL2: radios[1] ? Math.round(radios[1].getBoundingClientRect().left) : 0,
      dotD: radios[0] ? Math.round(parseFloat(getComputedStyle(radios[0], "::before").width)) : -1,
      countHead: r(panes[0] && panes[0].querySelector(":scope > .pane-head")),
      fixedHead: r(panes[1] && panes[1].querySelector(":scope > .pane-head")),
      row: r(card.querySelector('.pg[data-pg="reminder"] .row')),
      field: r(card.querySelector("#short-duration")),
      interval: r(document.getElementById("short-interval")),
      intervalInHead: !!(
        panes[0] &&
        panes[0].querySelector(":scope > .pane-head #short-interval")
      ),
    };
  });
  check(
    !!geo.top && !!geo.bottom && Math.abs(geo.top.w - geo.bottom.w) <= 1,
    "面板 · 两块等宽（上下排列不是两块各占半宽）",
    JSON.stringify({ top: geo.top && geo.top.w, bottom: geo.bottom && geo.bottom.w }),
  );
  // 两行要读成一组（2026-11-11 用户：“距离小一点、联系紧密一些”）：间距只由 .mode-panes 的
  // gap 给，头自己的上下内边距已清零，所以这里量到的就是那 4px。留 2~8 的窗口 ——
  // 写死 4 会把“手一抖改成 6”也算回归，而 0 会让两行贴到一起。
  check(
    !!geo.top && !!geo.bottom && geo.top.b <= geo.bottom.t &&
      geo.bottom.t - geo.top.b >= 2 && geo.bottom.t - geo.top.b <= 8,
    "面板 · 两行挨得紧（行间只留 2~8px，不再被两层留白加起来松开）",
    JSON.stringify({ topBottom: geo.top && geo.top.b, bottomTop: geo.bottom && geo.bottom.t }),
  );
  check(
    !!geo.top && !!geo.bottom &&
      Math.abs(geo.top.l - geo.bottom.l) <= 1 && Math.abs(geo.top.r - geo.bottom.r) <= 1,
    "面板 · 两块左缘右缘各自齐平（一条竖线到底）",
    JSON.stringify({ top: [geo.top.l, geo.top.r], bottom: [geo.bottom.l, geo.bottom.r] }),
  );
  // 两行等高：倒计时那行里装着 31px 的输入框，固定时间那行只有字 —— 不靠 min-height
  // 撑一下，两行的圆点会差 6px，看着像没对齐（收紧行距之后这个偏差更显眼）。
  check(
    !!geo.countHead && !!geo.fixedHead && geo.countHead.h === geo.fixedHead.h,
    "面板 · 两个面板头等高（两行的圆点在同一条水平节奏上）",
    JSON.stringify({ count: geo.countHead && geo.countHead.h, fixed: geo.fixedHead && geo.fixedHead.h }),
  );
  // 组取消边框后没有需要抵消的东西：面板块左缘 = 组左缘 = 卡片内容左缘
  check(
    !!geo.top && !!geo.pgBox && geo.top.l === geo.pgBox.l,
    "面板 · 面板块左缘与组左缘齐平（组不再是盒子，没有负外边距要抵）",
    JSON.stringify({ pane: geo.top && geo.top.l, pg: geo.pgBox && geo.pgBox.l }),
  );
  // 层次只剩缩进这一处：固定时间的时刻表相对面板块退后一级 48（16 + 8 + 24）。
  // 间隔框已并进标题行，不再参与缩进 —— 谁把它搬回下面、又忘了缩进，这条不会管，
  // 但「内容归属」那条会红。
  check(
    !!geo.bottom && geo.slotBodyL - geo.bottom.l === 48,
    "面板 · 时刻表比面板块退后一级缩进（48 = 圆点 16 + 间距 8 + 台阶 24）",
    JSON.stringify({ slot: geo.slotBodyL, pane: geo.bottom && geo.bottom.l }),
  );
  // 对齐的最后一环：圆点就是面板这一行的行首 —— 左缘与「系统空闲」的勾选框重合，
  // 直径也相同（同一个 16px）；两块面板的圆点也同一条竖线。
  check(
    !!geo.dotL && !!geo.gateCheck && geo.dotL === geo.gateCheck.l && geo.dotL === geo.dotL2 &&
      geo.dotD === geo.gateCheck.w && geo.dotD === 16,
    "面板 · 圆点与系统空闲复选框同左缘、同尺寸（16×16），两块圆点同线",
    JSON.stringify(geo && { dotL: geo.dotL, dotL2: geo.dotL2, gate: geo.gateCheck, d: geo.dotD }),
  );
  // 间隔框住在倒计时面板头那一行里（不是下面又长出一行）
  check(
    !!geo.interval && !!geo.countHead && geo.intervalInHead &&
      geo.interval.t >= geo.countHead.t - 1 &&
      geo.interval.b <= geo.countHead.b + 1 &&
      geo.interval.r <= geo.countHead.r + 1,
    "面板 · 间隔框落在倒计时标题行内（右缘不越出这一行）",
    JSON.stringify(geo && { interval: geo.interval, head: geo.countHead }),
  );
  check(
    !!geo.bottom && !!geo.pgBox && geo.bottom.r === geo.pgBox.r,
    "面板 · 面板块右缘与组右缘齐平（左右对称）",
    JSON.stringify({ pane: geo.bottom && geo.bottom.r, pg: geo.pgBox && geo.pgBox.r }),
  );
  check(
    !!geo.host && !!geo.field && geo.field.h > 0 && geo.host.h >= geo.field.h + 20,
    "面板 · 比字段行的输入框高一截（组里的主开关）",
    (geo.host && geo.host.h) + " vs " + (geo.field && geo.field.h),
  );
  // 未选中那块不许压淡：压淡会让它的字变浅、读起来像禁用，而它其实仍可编辑。
  check(
    geo.topOp === 1 && geo.bottomOp === 1,
    "面板 · 两块都不压淡（未选中也不做成“禁用”长相）",
    JSON.stringify({ top: geo.topOp, bottom: geo.bottomOp }),
  );
}

// 提醒时间（固定时刻表）的版式。
//
// 它曾经是“卡片里的一个描边小卡片，里面每条时刻再套一层描边”，加上组的标题与发丝线
// 一共四层盒子；外框与卡片内缘齐平、内容却缩进 12px，全卡唯一的左对齐线在那里断掉；
// 头部 `▸提醒时间` 13px/600 深色比组标题 12px/700 灰字还抢眼；行尾孤悬一枚 `＋`。
// 现在改成组内无框清单（子标签行 + 行间发丝线 + 每条时刻一个网格行）。
// 这一组只盯最容易回退的五处，功能链路仍归下面的控件矩阵：
//   · 面板不许再画框（描边 / 圆角 / 水平内边距）
//   · 子标签与组内行标签、与卡头同一条竖线
//   · “＋ 添加”必须紧跟子标签右侧，不许回到行尾
//   · 时刻 / 重复 / 删除三列在两个行之间严格对齐，删除键收在行尾
//   · 时刻是一个原生 time 框（HH:MM），不是“时 + 分”两个窄框
async function verifySlotPanel(page) {
  console.log("\n[timing] 提醒时间（时刻表）版式");
  await page.evaluate(() => {
    [...document.querySelectorAll(".nav-item")].find((b) => b.dataset.tab === "timing").click();
  });
  await sleep(350);

  const pickMode = async (id, text) => {
    const hit = await page.evaluate(
      ({ k, t }) => {
        const opts = [...document.querySelectorAll(`#${k}-mode .pane-radio`)];
        const el = opts.find((e) => ((e.querySelector(".pane-label") || {}).textContent || "").trim() === t);
        if (!el) return false;
        el.click();
        return true;
      },
      { k: id, t: text },
    );
    if (hit) await waitSettled(page);
    await sleep(250);
    return hit;
  };

  check(await pickMode("short", "固定时间"), "时刻表 · 短休息切到固定时间");
  for (let i = 0; i < 2; i++) {
    await page.click("#short-add");
    await waitSettled(page);
  }
  await sleep(300);

  const geo = await page.evaluate(() => {
    const card = document.getElementById("short-card");
    const panel = card.querySelector('.pg[data-pg="trigger"] .slot-fixed');
    if (!panel) return null;
    const cs = getComputedStyle(panel);
    const body = panel.querySelector(".slot-body");
    // 2026-11-10 晚：子标签「提醒时间」与「＋添加」都并进了固定时间面板头 ——
    // 清单这一层只剩 tk-list 本身，所以这里量的是「清单左缘」而不是「子标签左缘」。
    const add = card.querySelector('#short-mode .mode-pane[data-mode="fixed"] > .pane-head > .slot-add');
    const paneHead = card.querySelector('#short-mode .mode-pane[data-mode="fixed"] > .pane-head');
    // 时刻表住在**固定时间面板**里：它对齐的是面板内容区，不是全卡那条标签列。
    const paneBody = panel.closest(".pane-body");
    const pb = paneBody ? paneBody.getBoundingClientRect() : null;
    const pbPadL = paneBody ? parseFloat(getComputedStyle(paneBody).paddingLeft) || 0 : 0;
    const pbPadR = paneBody ? parseFloat(getComputedStyle(paneBody).paddingRight) || 0 : 0;
    const paneL = pb ? Math.round(pb.left + pbPadL) : -1;
    const paneR = pb ? Math.round(pb.right - pbPadR) : -1;
    const L = (el) => (el ? Math.round(el.getBoundingClientRect().left) : -1);
    const R = (el) => (el ? Math.round(el.getBoundingClientRect().right) : -1);
    const rows = [...panel.querySelectorAll(".tk-item")].map((r) => {
      const t = r.querySelector(".tk-time input[type=time]");
      const rep = r.querySelector(".tk-repeat");
      const del = r.querySelector(".btn.danger");
      const days = r.querySelector(".tk-days");
      const rs = getComputedStyle(r);
      const mid = (el) => (el ? Math.round((el.getBoundingClientRect().top + el.getBoundingClientRect().bottom) / 2) : -1);
      const rb = r.getBoundingClientRect();
      return {
        type: t ? t.type : "",
        value: t ? t.value : "",
        disp: rs.display,
        cols: rs.gridTemplateColumns.split(" ").length,
        timeL: L(t),
        repL: L(rep),
        delR: R(del),
        rowR: R(r),
        rowH: Math.round(rb.height),
        // 垂直中心线是“不换行”的直接证据：三个控件高度不同（时刻 29 / 下拉 29 /
        // 删除键更矮），居中对齐时它们的 top 本来就差几像素，只有中心线必须重合。
        timeT: mid(t),
        repT: mid(rep),
        delT: mid(del),
        daysT: mid(days),
        daysR: R(days),
        daysH: days ? Math.round(days.getBoundingClientRect().height) : -1,
        chipW: days && days.firstElementChild
          ? Math.round(days.firstElementChild.getBoundingClientRect().width)
          : -1,
        chipCount: days ? days.children.length : 0,
        chipRows: days ? new Set([...days.children].map((c) => Math.round(c.getBoundingClientRect().top))).size : -1,
        // 周期框是不是“文字显示全”：同款式的 select 在 width:auto 下的自然宽度
        // （文字 + 原生箭头 + 内边距）就是“放得下”的客观下限。
        repW: Math.round(rep.getBoundingClientRect().width),
        repNatural: (() => {
          if (!rep) return -1;
          const s = rep.cloneNode(true);
          s.style.cssText = "position:absolute;left:-9999px;top:0;width:auto;";
          document.body.appendChild(s);
          const w = Math.round(s.getBoundingClientRect().width);
          s.remove();
          return w;
        })(),
      };
    });
    return {
      border: [cs.borderTopWidth, cs.borderLeftWidth],
      radius: cs.borderRadius,
      panelPadL: parseFloat(cs.paddingLeft) || 0,
      bodyPadL: body ? parseFloat(getComputedStyle(body).paddingLeft) || 0 : -1,
      listL: body ? L(body.querySelector(".tk-list")) : -1,
      addL: L(add),
      addInHead: !!(paneHead && paneHead.contains(add)),
      paneL,
      paneR,
      // 面板头里不许再有“提醒时间”这类子标签（已并进标题行）
      headCaptions: paneHead ? paneHead.querySelectorAll(".slot-title").length : -1,
      // 2026-11-10 起这一层不准折叠：头部不许再有折叠开关，内容体不许挂 hidden
      headToggle: panel.querySelectorAll(".msg-toggle").length,
      bodyHidden: body ? body.classList.contains("hidden") : null,
      bodyH: body ? Math.round(body.getBoundingClientRect().height) : -1,
      rows,
    };
  });

  if (check(!!geo, "时刻表 · 面板在场")) {
    check(
      geo.border[0] === "0px" && geo.border[1] === "0px" && geo.radius === "0px",
      "时刻表 · 面板不再画框（无描边 / 无圆角）",
      geo.border.join(" / ") + " / " + geo.radius,
    );
    check(
      geo.panelPadL === 0 && geo.bodyPadL === 0,
      "时刻表 · 面板与内容区都没有水平内边距（否则整列右移）",
      geo.panelPadL + " / " + geo.bodyPadL,
    );
    check(
      Math.abs(geo.listL - geo.paneL) <= 1,
      "时刻表 · 清单与所在面板的内容左缘同一条线",
      JSON.stringify({ list: geo.listL, pane: geo.paneL }),
    );
    check(
      geo.addL > 0 && geo.addInHead,
      "时刻表 · “＋ 添加”住在固定时间面板头里（不再有自己的子标签行）",
      JSON.stringify({ add: geo.addL, inHead: geo.addInHead }),
    );
    check(
      geo.headCaptions === 0,
      "时刻表 · 面板头里没有「提醒时间」这类子标签（已并进标题行）",
      geo.headCaptions,
    );
    // 2026-11-10 用户要求：「提醒时间」不准折叠，必须展开。
    check(
      geo.headToggle === 0 && geo.bodyHidden === false && geo.bodyH > 40,
      "时刻表 · 不折叠：头部没有开关、清单常驻展开",
      JSON.stringify({ toggle: geo.headToggle, hidden: geo.bodyHidden, h: geo.bodyH }),
    );
    const rows = geo.rows || [];
    check(rows.length === 2, "时刻表 · 两条时刻都渲染成行", rows.length);
    check(
      rows.length === 2 && rows[1].disp === "grid" && rows[1].cols === 5,
      "时刻表 · 一条时刻是 5 列网格（勾选 / 时刻 / 重复 / 星期 / 删除）",
      JSON.stringify(rows.map((r) => r.disp + ":" + r.cols)),
    );
    check(
      rows.length === 2 &&
        rows[0].timeL === rows[1].timeL &&
        rows[0].repL === rows[1].repL &&
        rows[0].delR === rows[1].delR,
      "时刻表 · 两行的时刻 / 重复 / 删除纵向对齐",
      JSON.stringify(rows.map((r) => [r.timeL, r.repL, r.delR])),
    );
    check(
      rows.length === 2 && rows.every((r) => r.rowR - r.delR <= 8 && r.rowR - r.delR >= 0),
      "时刻表 · 删除键收在行尾",
      JSON.stringify(rows.map((r) => r.rowR - r.delR)),
    );
    // 用户要求：收窄时间框 / 周期框并去掉时钟图标之后，这一行必须**不换行且完整显示**
    check(
      rows.length === 2 &&
        rows.every((r) => Math.abs(r.timeT - r.repT) <= 1 && Math.abs(r.repT - r.delT) <= 1),
      "时刻表 · 时刻 / 重复 / 删除在同一行（面板内不换行）",
      JSON.stringify(rows.map((r) => [r.timeT, r.repT, r.delT])),
    );
    check(
      rows.length === 2 && rows.every((r) => r.rowR <= geo.paneR + 1),
      "时刻表 · 每行右缘不越出面板内容区（完整显示、没被裁）",
      JSON.stringify({ paneR: geo.paneR, rows: rows.map((r) => r.rowR) }),
    );
    check(
      rows.length === 2 && rows.every((r) => r.type === "time" && /^\d{2}:\d{2}$/.test(r.value)),
      "时刻表 · 时刻是一个原生 time 框（HH:MM）",
      JSON.stringify(rows.map((r) => r.type + " " + r.value)),
    );

    // 时钟图标 + 时刻面板（2026-11-11 晚改版）。
    // 图标还在框内，但**不再是浏览器那枚**：原生 `::-webkit-calendar-picker-indicator`
    // 点出来的是 WebView2 自带的白底方角 popup，DOM 与样式都在页面之外，一行 CSS 也碰不到
    //（用户原话：“点击时钟时出现的时间选择控件太丑了”）。现在 indicator 全部 `display:none`，
    // 图标由 `.tk-clock` 这枚真按钮画，点了弹自绘的 `.tp` 面板。
    // 谁把原生弹层放回来、或把按钮删了，下面两条都会红。
    const picker = await page.evaluate(() => {
      const out = [];
      for (const sheet of document.styleSheets) {
        let rules = [];
        try {
          rules = [...sheet.cssRules];
        } catch (e) {
          continue;
        }
        for (const r of rules) {
          if (!r.selectorText || !r.selectorText.includes("::-webkit-calendar-picker-indicator")) continue;
          out.push({ sel: r.selectorText, display: r.style.display || "" });
        }
      }
      return out;
    });
    check(
      picker.length >= 1 && picker.every((p) => p.display === "none"),
      "时刻表 · 原生时刻弹层已关闭（indicator 全部 display:none，不再弹 WebView2 那个）",
      JSON.stringify(picker),
    );
    // 自绘图标：真按钮（可聚焦、有 aria-label、声明它会开一个 dialog），盖在框内右端
    const clock = await page.evaluate(() => {
      const time = document.querySelector("#short-card .slot-fixed .tk-time");
      const inp = time && time.querySelector("input[type=time]");
      const btn = time && time.querySelector(".tk-clock");
      if (!inp || !btn) return null;
      const ib = inp.getBoundingClientRect();
      const bb = btn.getBoundingClientRect();
      const svg = btn.querySelector("svg");
      return {
        tag: btn.tagName,
        label: btn.getAttribute("aria-label"),
        popup: btn.getAttribute("aria-haspopup"),
        svg: svg ? Math.round(svg.getBoundingClientRect().width) : 0,
        insideRight: bb.right <= ib.right + 1 && bb.left > ib.left,
        sameMid: Math.abs((bb.top + bb.bottom) / 2 - (ib.top + ib.bottom) / 2) <= 1,
        w: Math.round(bb.width),
      };
    });
    check(
      !!clock && clock.tag === "BUTTON" && !!clock.label && clock.popup === "dialog" && clock.svg >= 12,
      "时刻表 · 时钟图标是自绘的真按钮（可聚焦、有名字、声明会开面板）",
      JSON.stringify(clock),
    );
    check(
      !!clock && clock.insideRight && clock.sameMid,
      "时刻表 · 图标落在时间框右端内、与框同一条中心线（不是挂在框外）",
      JSON.stringify(clock && { insideRight: clock.insideRight, sameMid: clock.sameMid, w: clock.w }),
    );
    // 图标要放得下，框还得放得下它 + “23:59”：68 = 数字 + 左内边距 8 + 图标位 18
    const timeFit = await page.evaluate(() => {
      const el = document.querySelector('#short-card .slot-fixed input[type="time"]');
      const cs = getComputedStyle(el);
      const cv = document.createElement("canvas").getContext("2d");
      cv.font = `${cs.fontWeight} ${cs.fontSize} ${cs.fontFamily}`;
      return {
        w: Math.round(el.getBoundingClientRect().width),
        textW: Math.round(cv.measureText("23:59").width),
        padL: Math.round(parseFloat(cs.paddingLeft) || 0),
        padR: Math.round(parseFloat(cs.paddingRight) || 0),
        clip: el.scrollWidth - el.clientWidth,
      };
    });
    check(
      timeFit.w >= timeFit.textW + timeFit.padL + timeFit.padR && timeFit.w <= 84 && timeFit.clip <= 1,
      "时刻表 · 时间框放得下“23:59”+ 图标位（数字没被裁）",
      JSON.stringify(timeFit),
    );

    // 点图标 → 自绘面板：挂 body、fixed 定位、两列 listbox（时 24 / 分 60），
    // 初值与 input 一致，高亮带正好套在选中那行上，面板右缘与输入框右缘对齐。
    const tf0 = await page.evaluate(
      () => document.querySelector("#short-card .slot-fixed input[type=time]").value,
    );
    await page.evaluate(() => document.querySelector("#short-card .slot-fixed .tk-clock").click());
    await sleep(300);
    const tp = await page.evaluate(() => {
      const el = document.querySelector(".tp");
      if (!el) return null;
      const r = el.getBoundingClientRect();
      const cs = getComputedStyle(el);
      const cols = [...el.querySelectorAll(".tp-col")];
      const band = el.querySelector(".tp-band").getBoundingClientRect();
      const sel = el.querySelector(".tp-col .tp-item[aria-selected='true']");
      const sb = sel ? sel.getBoundingClientRect() : null;
      const inp = document.querySelector("#short-card .slot-fixed input[type=time]");
      const ib = inp.getBoundingClientRect();
      return {
        pos: cs.position,
        onBody: el.parentElement === document.body,
        role: el.getAttribute("role"),
        name: el.getAttribute("aria-label"),
        cols: cols.map((c) => ({
          name: c.getAttribute("aria-label"),
          role: c.getAttribute("role"),
          n: c.children.length,
          snap: getComputedStyle(c).scrollSnapType,
        })),
        selected: [...el.querySelectorAll(".tp-item[aria-selected='true']")].map((e) => e.textContent).join(":"),
        bandMid: Math.round(band.top + band.height / 2),
        selMid: sb ? Math.round(sb.top + sb.height / 2) : -1,
        alignRight: Math.abs(Math.round(r.right) - Math.round(ib.right)) <= 2,
        below: Math.round(r.top) >= Math.round(ib.bottom),
        // 焦点必须落在面板里那一项上：否则键盘方向键没有落脚点
        focused: document.activeElement && document.activeElement.className,
        w: Math.round(r.width),
        h: Math.round(r.height),
      };
    });
    check(
      !!tp && tp.pos === "fixed" && tp.onBody && tp.role === "dialog" && !!tp.name,
      "面板 · 点时钟弹出的是自绘 dialog（fixed、挂 body、有可访问名）",
      JSON.stringify(tp && { pos: tp.pos, onBody: tp.onBody, role: tp.role, name: tp.name }),
    );
    check(
      !!tp && tp.cols.length === 2 && tp.cols[0].n === 24 && tp.cols[1].n === 60 &&
        tp.cols.every((c) => c.role === "listbox" && /y/i.test(c.snap)),
      "面板 · 时 / 分两列 listbox（24 与 60 项），列内按行吸附",
      JSON.stringify(tp && tp.cols),
    );
    check(
      !!tp && tp.selected === tf0,
      "面板 · 打开时高亮的就是当前值（与输入框里的时刻一致）",
      JSON.stringify(tp && { panel: tp.selected, input: tf0 }),
    );
    check(
      !!tp && Math.abs(tp.bandMid - tp.selMid) <= 1,
      "面板 · 高亮带正中套在选中那行上（带不动、数字滚）",
      JSON.stringify({ band: tp && tp.bandMid, sel: tp && tp.selMid }),
    );
    check(
      !!tp && tp.alignRight && tp.below,
      "面板 · 贴着输入框下方展开、右缘与输入框对齐",
      JSON.stringify(tp && { alignRight: tp.alignRight, below: tp.below }),
    );
    check(
      !!tp && /tp-item/.test(tp.focused || ""),
      "面板 · 打开后焦点落在面板里（方向键才有落脚点）",
      tp && tp.focused,
    );
    // 悬停底色与焦点描边都跟着这一行的圆角走，所以圆角必须**按列**给：
    // 两列之间只隔 1px 分隔线，内侧若也是圆角，行底色的两端就会在分隔线左右各咬一个豁口，
    // 一行淡蓝在中间断成两截。判据取「哪两个角是 0」，不写成字符串比对（各引擎的
    // 简写归一化不一样）；朝外那两角要与 .tp-band 同半径，悬停色块才与高亮带同形。
    const corner = await page.evaluate(() => {
      const radius = (el) => {
        const v = getComputedStyle(el).borderRadius.split(/\s+/).map((s) => parseFloat(s) || 0);
        // 简写可能是 1 / 2 / 4 值：补齐成 [左上, 右上, 右下, 左下]
        if (v.length === 1) return [v[0], v[0], v[0], v[0]];
        if (v.length === 2) return [v[0], v[1], v[0], v[1]];
        if (v.length === 3) return [v[0], v[1], v[2], v[1]];
        return v;
      };
      const one = (u) => {
        const col = document.querySelector(`.tp-col[data-unit='${u}']`);
        const it = col && col.querySelector(".tp-item");
        return it ? { unit: col.dataset.unit, r: radius(it) } : null;
      };
      const band = document.querySelector(".tp-band");
      return { h: one("h"), m: one("m"), band: band ? radius(band)[0] : -1 };
    });
    check(
      !!corner.h && !!corner.m &&
        corner.h.r[1] === 0 && corner.h.r[2] === 0 &&          // 小时列：右上、右下是方角
        corner.m.r[0] === 0 && corner.m.r[3] === 0,            // 分钟列：左上、左下是方角
      "面板 · 悬停圆角只圆朝外那一侧（小时列右侧、分钟列左侧都是方角，不在分隔线两边豁口）",
      JSON.stringify({ h: corner.h && corner.h.r, m: corner.m && corner.m.r }),
    );
    check(
      !!corner.h && !!corner.m && corner.band > 0 &&
        corner.h.r[0] === corner.band && corner.h.r[3] === corner.band &&
        corner.m.r[1] === corner.band && corner.m.r[2] === corner.band,
      "面板 · 朝外两角与高亮带同半径（悬停色块与高亮带同形）",
      JSON.stringify({ band: corner.band, h: corner.h && corner.h.r, m: corner.m && corner.m.r }),
    );
    // 滚动即选：滚完 110ms 回写 input 并走原 change 链路（落盘 + 摘要）
    await page.evaluate(() => {
      const cols = [...document.querySelectorAll(".tp-col")];
      cols[0].scrollTop = 5 * 30;
      cols[1].scrollTop = 20 * 30;
      cols[0].dispatchEvent(new Event("scroll"));
      cols[1].dispatchEvent(new Event("scroll"));
    });
    await sleep(400);
    const tpPicked = await page.evaluate(() => ({
      value: document.querySelector("#short-card .slot-fixed input[type=time]").value,
      selected: [...document.querySelectorAll(".tp-item[aria-selected='true']")].map((e) => e.textContent).join(":"),
      summary: (document.getElementById("short-summary") || {}).textContent || "",
    }));
    check(
      tpPicked.value === "05:20" && tpPicked.selected === "05:20",
      "面板 · 滚动停下即回写输入框（时 / 分两列各管各的）",
      JSON.stringify(tpPicked),
    );
    check(
      /05:20/.test(tpPicked.summary) || /条/.test(tpPicked.summary),
      "面板 · 改完时刻后卡片摘要跟着刷新（走的是原来那条 change 链路）",
      tpPicked.summary,
    );
    // Esc 关闭 + 焦点还给按钮（键盘用户不能掉进虚空）
    await page.keyboard.press("Escape");
    await sleep(250);
    const tpClosed = await page.evaluate(() => ({
      gone: !document.querySelector(".tp"),
      focus: document.activeElement && document.activeElement.className,
    }));
    check(
      tpClosed.gone && /tk-clock/.test(tpClosed.focus || ""),
      "面板 · Esc 收起，并把焦点还给时钟按钮",
      JSON.stringify(tpClosed),
    );
    // 改回 09:30，后面几条断言按原来的两条时刻继续
    await page.evaluate(() => {
      const el = document.querySelector("#short-card .slot-fixed input[type=time]");
      el.value = "09:30";
      el.dispatchEvent(new Event("change"));
    });
    await sleep(280);
    // 周期选择框要比“放得下文字的最小宽度”还宽一点（用户：里面的文字要显示全）。
    // 客观判据是同一个 select 在 width:auto 下的自然宽度 —— 那就是文字 + 原生箭头 + 内边距。
    check(
      rows.length === 2 && rows.every((r) => r.repW >= r.repNatural && r.repW <= r.repNatural + 24),
      "时刻表 · 周期下拉放得下「每天 / 每周」与原生箭头（宽度 ≥ 自然宽度）",
      JSON.stringify(rows.map((r) => r.repW + " vs " + r.repNatural)),
    );

    // 「每一行绝对不能换行」（2026-11-11 用户原话）：把第一条改成「每周」，七个星期方块
    // 必须全部落在第一行 —— 旧实现是行宽 < 520px 就把它们整块挪到第二行。
    await page.evaluate(() => {
      const sel = document.querySelector('#short-card .slot-fixed .tk-repeat');
      sel.value = "weekly";
      sel.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await sleep(250);
    const weekly = await page.evaluate(() => {
      const r = document.querySelector('#short-card .slot-fixed .tk-item');
      const days = r.querySelector(".tk-days");
      const rb = r.getBoundingClientRect();
      const chips = [...days.children].map((c) => c.getBoundingClientRect());
      return {
        rowH: Math.round(rb.height),
        daysH: Math.round(days.getBoundingClientRect().height),
        chipRows: new Set(chips.map((c) => Math.round(c.top))).size,
        chipCount: chips.length,
        chipW: Math.round(chips[0].width),
        daysR: Math.round(days.getBoundingClientRect().right),
        rowR: Math.round(rb.right),
        // 五个子项的中心线（时刻 / 周期 / 星期 / 删除，勾选框 16px 也在同一行）
        mids: [
          r.querySelector(".tk-time"),
          r.querySelector(".tk-repeat"),
          days,
          r.querySelector(".btn.danger"),
        ].map((el) => {
          const b = el.getBoundingClientRect();
          return Math.round((b.top + b.bottom) / 2);
        }),
      };
    });
    check(
      weekly.chipCount === 7 && weekly.chipRows === 1 && weekly.daysH <= 28,
      "时刻表 · “每周”时七个星期方块排在一行（不掉到第二行）",
      JSON.stringify(weekly),
    );
    check(
      weekly.mids.every((m) => Math.abs(m - weekly.mids[0]) <= 1),
      "时刻表 · 星期方块与时刻 / 周期 / 删除同一条中心线（整行只有一行）",
      JSON.stringify(weekly.mids),
    );

    // 最窄窗口（760，程序允许的最小宽度）下同样不许换行：星期方块可以一起变窄，
    // 但必须仍是一行、仍不越出行尾。
    await page.setViewportSize({ width: 760, height: 900 });
    await sleep(400);
    const narrow = await page.evaluate(() => {
      const r = document.querySelector('#short-card .slot-fixed .tk-item');
      const days = r.querySelector(".tk-days");
      const chips = [...days.children].map((c) => c.getBoundingClientRect());
      const rb = r.getBoundingClientRect();
      return {
        rowH: Math.round(rb.height),
        chipRows: new Set(chips.map((c) => Math.round(c.top))).size,
        chipW: Math.round(chips[0].width),
        chipMin: Math.min(...chips.map((c) => Math.round(c.width))),
        daysR: Math.round(days.getBoundingClientRect().right),
        rowR: Math.round(rb.right),
        overflow: Math.round(rb.right - days.getBoundingClientRect().right),
      };
    });
    check(
      narrow.chipRows === 1 && narrow.rowH <= 52 && narrow.daysR <= narrow.rowR,
      "时刻表 · 760 宽（最小窗口）下仍是一行：星期方块一起收窄，不掉行也不越界",
      JSON.stringify(narrow),
    );
    check(
      narrow.chipMin >= 17,
      "时刻表 · 窄窗口下星期方块仍有 17px 以上（字母还看得清）",
      JSON.stringify({ min: narrow.chipMin, w: narrow.chipW }),
    );
    await page.setViewportSize({ width: 1010, height: 900 });
    await sleep(300);
    // 还原成「每天」，后面的段落按默认状态继续
    await page.evaluate(() => {
      const sel = document.querySelector('#short-card .slot-fixed .tk-repeat');
      sel.value = "daily";
      sel.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await sleep(250);
  }

  // 点一下固定时间面板头（旧版这里是「提醒时间」子标签，再早是折叠开关）：清单不许收起。
  // 这条是防回潮的关键 —— 谁把 msg-toggle 加回来，上面那条 headToggle 会红，
  // 这条也不会被“默认展开”糊弄过去。面板头里点的是“＋添加”旁边的空白（不是圆点），
  // 免得顺手换了计时方式。
  await page.evaluate(() => {
    const h = document.querySelector('#short-mode .mode-pane[data-mode="fixed"] > .pane-head');
    if (h) h.click();
  });
  await sleep(250);
  const afterHeadClick = await page.evaluate(() => {
    const b = document.querySelector('#short-card .pg[data-pg="trigger"] .slot-body');
    if (!b) return null;
    return { hidden: b.classList.contains("hidden"), h: Math.round(b.getBoundingClientRect().height) };
  });
  check(
    !!afterHeadClick && afterHeadClick.hidden === false && afterHeadClick.h > 40,
    "时刻表 · 点头部不会收起清单（这一层不可折叠）",
    JSON.stringify(afterHeadClick),
  );

  // 收尾同时验空状态：删掉刚加的两条（✕ → 确认），列表应退回“一行提示”
  for (let i = 0; i < 2; i++) {
    const hit = await page.evaluate(() => {
      const r = document.querySelector('#short-card .pg[data-pg="trigger"] .tk-item');
      const b = r && r.querySelector(".btn.danger");
      if (!b) return false;
      b.click();
      return true;
    });
    if (!hit) break;
    await sleep(300);
    await page.click("#cf-ok");
    await waitSettled(page);
  }
  const empty = await page.evaluate(() => {
    const e = document.querySelector('#short-card .pg[data-pg="trigger"] .slot-body .tk-empty');
    if (!e) return null;
    const cs = getComputedStyle(e);
    return { style: cs.borderTopStyle, align: cs.textAlign, radius: cs.borderRadius };
  });
  check(
    !!empty && empty.style === "solid" && empty.align === "left",
    "时刻表 · 空状态是一行左对齐提示，不再是虚线框",
    JSON.stringify(empty),
  );

  await pickMode("short", "倒计时");
}

async function main() {  const browser = await chromium.launch({ channel: "msedge", headless: true });
  const ctx = await browser.newContext({ viewport: { width: 1010, height: 900 }, deviceScaleFactor: 1 });
  const page = await ctx.newPage();
  const errors = [];
  page.on("console", (m) => {
    if (m.type() === "error" && !/favicon/i.test(m.text())) errors.push(m.text().slice(0, 200));
  });
  page.on("pageerror", (e) => errors.push("pageerror: " + e.message.slice(0, 200)));

  try {
    if (PRELOAD) await page.addInitScript({ path: PRELOAD });
    await page.route("**/favicon.ico", (r) => r.fulfill({ status: 204, body: "" }));
    await page.goto(BASE + "/settings.html?win=settings", { waitUntil: "domcontentloaded" });
    if (!(await waitApi(page))) {
      check(false, "设置页装载完成且 window.api 就绪");
      return 1;
    }
    await sleep(600);

    // 预热保存一次：第一次 autoSave 会顺带把若干缺省值写进配置，
    // 不先“跑熟”的话，第一个控件的 diff 会混进一堆无关字段。
    await page.evaluate(() => {
      const el = document.getElementById("idlePauseMin");
      el.value = String(Number(el.value));
      el.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await waitSettled(page);

    const TABS = ["general", "timing", "supervisor", "shortcuts", "about"];
    const skipped = [];

    for (const tab of TABS) {
      console.log("\n[" + tab + "] 控件接线");
      await page.evaluate((t) => {
        const btn = [...document.querySelectorAll(".nav-item")].find((b) => b.dataset.tab === t);
        btn.click();
      }, tab);
      await sleep(400);

      // 枚举该 tab 里可见、可逆的控件（复选框 / 数值 / 文本 / 单选 / 滑块）
      const handles = await page.evaluate((t) => {
        const root = document.getElementById("tab-" + t);
        const out = [];
        const seen = new Set();
        // 这几枚不写配置，而是写 supervisor/<专员>/meta.json 的 geom：
        // 由下面专门的一段用 listSupervisorRoles 验证（口径不同，不能混进来）
        const GEOM_IDS = new Set(["sup-flip", "sup-size", "sup-posx", "sup-posy"]);
        for (const el of root.querySelectorAll("input")) {
          const type = el.type;
          if (!["checkbox", "radio", "number", "range", "text"].includes(type)) continue;
          if (el.disabled || el.readOnly) continue;
          if (GEOM_IDS.has(el.id)) continue;
          // 空闲门（勾选框 + 秒数框 + 隐藏的分框）是**一组**控件：秒数框进位会把分框
          // 露出来，而“操作回去”只回填秒数、分里还留着上一次的值，会被误判成不可逆。
          // 由 verifyIdleGateControls 按控件组整体验。
          if (/-idleGate/.test(el.id || "")) continue;
          const r = el.getBoundingClientRect();
          if (r.width === 0 && r.height === 0) continue; // 隐藏（折叠面板没展开）
          let sel;
          if (el.id) sel = "#" + CSS.escape(el.id);
          else if (type === "radio") {
            // 主题单选没有 id：按 name + value 定位（本来就是一对一）
            sel = `#${root.id} input[name="${el.name}"][value="${el.value}"]`;
          } else {
            const name = el.getAttribute("name");
            if (!name) continue;
            sel = `#${root.id} input[name="${name}"]`;
          }
          if (seen.has(sel)) continue;
          seen.add(sel);
          out.push({
            sel,
            id: el.id || (el.name ? el.name + "=" + el.value : sel),
            type,
          });
        }
        return out;
      }, tab);

      let okCount = 0;
      for (const h of handles) {
        // 记下原值，供“操作回去”用
        await page.evaluate((sel) => {
          const el = document.querySelector(sel);
          if (el && el.type !== "checkbox" && el.type !== "radio") el.dataset.wrOrig = el.value;
        }, h.sel);

        const r = await poke(page, h.sel, h.type);
        if (r.modal) {
          skipped.push(h.id + "（弹确认框 " + r.modal + "）");
          continue;
        }
        const wired = r.changedPaths.length > 0;
        const restored = r.restorePaths.length === 0;
        if (!wired) skipped.push(h.id + "（操作后配置无变化）");
        check(wired, tab + " · " + h.id + " 接到了配置", r.changedPaths.slice(0, 3).join(" ; "));
        if (wired) {
          wiring.push({ tab, id: h.id, paths: r.changedPaths });
          check(restored, tab + " · " + h.id + " 可逆（操作回去后配置逐字还原）",
            r.restorePaths.slice(0, 3).join(" ; "));
          okCount++;
        }
      }
      console.log("  ---- " + tab + " 覆盖控件 " + okCount + " / " + handles.length);

      // 督休专员的“尺寸 / 位置 / 翻转”不写 settings，而是写 supervisor/<专员>/meta.json：
      // 用同一套 diff 会误判成“死控件”。这里改成按专员读回来验（口径与设置页一致）。
      if (tab === "supervisor") {
        await verifyGeomControls(page);
      }
    }

    // 空闲门这一组控件（勾选框 + 秒数框）按整体验，见函数头注释
    await verifyIdleGateControls(page);

    // 计时方法 / 提醒时长（秒）：长休息从“分钟”改“秒”的单位回归
    await verifyBreakDurationUnit(page);

    // 参数分组：计时方法 / 间隔 / 提醒时间 / 系统空闲 收进“触发条件”组
    await verifyParamGroups(page);
    // 计时方法的两段式段控：语义 / 文案 / 点击 / 键盘 / 几何
    await verifyModePanes(page);
    // 提醒时间（固定时刻表）的版式：无框清单 + 五列网格，见函数头注释
    await verifySlotPanel(page);

    // 统计页：图表与表格由数据驱动，控件都是“导出 / 清空”这类不可逆动作，
    // 这里只验渲染，不点按钮（按钮由绑定级探针覆盖）。
    //
    // 三张图都是 DOM/SVG 折线（.rc-plot 里由 setupRestChart 画 svg），**没有 canvas**：
    // 早先按 canvas 数量断言会得到 0，属于量错了对象。
    console.log("\n[stats] 渲染与只读控件");
    await page.evaluate(() => {
      [...document.querySelectorAll(".nav-item")].find((b) => b.dataset.tab === "stats").click();
    });
    await sleep(900);
    const statsInfo = await page.evaluate(() => {
      const tab = document.getElementById("tab-stats");
      const tables = [...tab.querySelectorAll("table")];
      const plots = ["rc-plot", "tk-plot", "ck-plot"].map((id) => {
        const el = document.getElementById(id);
        if (!el) return { id, exists: false, svg: 0, kids: 0 };
        return {
          id,
          exists: true,
          svg: el.querySelectorAll("svg").length,
          kids: el.children.length,
        };
      });
      return {
        tables: tables.length,
        headed: tables.filter((t) => t.querySelectorAll("th").length > 0).length,
        plots,
        visible: tab.getBoundingClientRect().height > 100,
      };
    });
    check(statsInfo.visible, "统计页可见");
    check(statsInfo.tables >= 3, "统计页至少三张表（休息 / 外卖 / 自定义）", statsInfo.tables);
    check(statsInfo.headed >= 3, "三张表都渲染了表头", statsInfo.headed);
    const plotsOk = statsInfo.plots.filter((p) => p.exists && p.kids > 0).length;
    check(plotsOk === 3, "三张图表都画出了内容（SVG 折线，本页没有 canvas）",
      JSON.stringify(statsInfo.plots.map((p) => [p.id, p.kids, p.svg])));

    // 全流程零 JS 报错
    check(errors.length === 0, "全程无 JS 报错", errors.slice(0, 3).join(" ; ") || "干净");

    // 接线表（写进日志，便于人工核对“这个开关到底改了哪个键”）
    console.log("\n---- 接线表 ----");
    for (const w of wiring) console.log("  " + w.tab + "/" + w.id + " → " + w.paths.join(" , "));
    console.log("\n---- 未计入的控件 ----");
    for (const s of skipped) console.log("  · " + s);
  } catch (e) {
    check(false, "探针异常：" + (e && e.message));
  } finally {
    await ctx.close();
    await browser.close();
  }

  console.log("\n结果：" + checks + " 项断言，失败 " + failures.length + " 项");
  for (const f of failures) console.log("  - " + f);
  return failures.length === 0 ? 0 : 1;
}

main().then((c) => process.exit(c));
