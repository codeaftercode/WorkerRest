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
      return {
        checked: box.checked,
        sec: sec.value,
        min: sec.getAttribute("min"),
        label: label ? label.textContent.trim() : "",
        tail: tail ? tail.textContent.trim() : "",
        disabled: sec.disabled,
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
    // 2026-11-09 起标签与右侧尾巴拼成一句话：“系统空闲达 [30] 秒再提醒”
    check(v.label === "系统空闲达?", "空闲门 · " + id + " 标签文案（“系统空闲达”）", v.label);
    check(v.tail === "秒再提醒", "空闲门 · " + id + " 秒数框右侧补完后半句", v.tail);
    check(v.disabled === true, "空闲门 · " + id + " 未勾时秒数框置灰不可编辑", String(v.disabled));
    check(v.checked === false, "空闲门 · " + id + " 默认不勾");
    check(v.sec === "30", "空闲门 · " + id + " 默认 30 秒", v.sec);
    check(v.min === "1", "空闲门 · " + id + " 最小值为 1", v.min);
  }

  // ② 用户要求：时间控件放右侧、与下方输入框同列。这里直接量几何位置 ——
  //    改版式时最容易破坏的就是“看起来差不多”，量一把才知道真的同一竖线。
  const align = await page.evaluate(() => {
    const g = document.getElementById("short-idleGate-sec");
    const i = document.getElementById("short-interval");
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
    "空闲门 · 输入框与下方“间隔（分钟）”左缘同列",
    JSON.stringify(align),
  );
  check(
    !!align && Math.abs(align.wGate - align.wInput) <= 1,
    "空闲门 · 输入框与下方输入框同宽",
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
    // 控件位置：2026-11-09 起卡内参数分成「触发方式 / 弹窗设置」两组，系统空闲属于前者、
    // 暂停休息时属于后者 —— 这里按**组**断言，不再按“行的绝对序号”
    // 2026-11-10：暂停休息时从组末提到组首（用户要求，它是最常用的那一行），
    // 所以断言从「在提醒时长之后」翻成「在提醒时长之前，且是组内第一行」。
    const order = await page.evaluate((id) => {
      const card = document.querySelector(`#tab-timing > .card[data-cb="${id}"]`);
      if (!card) return null;
      const pg = [...card.querySelectorAll(":scope > .pg")];
      const trig = pg.find((p) => p.dataset.pg === "trigger");
      const rem = pg.find((p) => p.dataset.pg === "reminder");
      // 候选单元：字段行 + 计时方法段控（段控不是 .row，但同样算组里的成员；
      // “暂停休息时”是 .row.field，已经算在 .row 里，不必单列）
      const idx = (g, re) => (g ? [...g.querySelectorAll(".row, .mode-seg")].findIndex((r) => re.test(r.textContent)) : -2);
      return {
        titles: pg.map((p) => (p.querySelector(".pg-title") || {}).textContent || ""),
        // 2026-11-09 起计时方法是段控（不是 .row），按它的文案找位置
        mode: idx(trig, /倒计时/),
        gate: idx(trig, /系统空闲/),
        dur: idx(rem, /提醒时长/),
        pause: idx(rem, /暂停休息时/),
      };
    }, cbID);
    check(
      !!order && order.titles.join("/") === "触发方式/弹窗设置",
      "分组 · 自定义卡也克隆出「触发方式 / 弹窗设置」两组",
      order ? order.titles.join("/") : "找不到卡片",
    );
    check(
      !!order && order.mode >= 0 && order.gate > order.mode,
      "分组 · 系统空闲在「触发方式」组内、计时方法之后",
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
//   · “触发方式” → “计时方法”；
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

// 参数分组（2026-11-09）：「计时方法 / 间隔 / 提醒时间 / 系统空闲」收进标题为“触发方式”
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
  console.log("\n[timing] 参数分组（触发方式 / 弹窗设置）");
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
      s.titles.join("/") === "触发方式/弹窗设置",
      "分组 · " + id + " 两个组标题与顺序",
      s.titles.join("/"),
    );
    check(s.headAbove, "分组 · " + id + " 卡头在两组之前");
    check(s.headSwitch, "分组 · " + id + " 总开关在卡头里（拨动开关）");
    check(
      /系统空闲达/.test(s.trig) && /提醒时间/.test(s.trig),
      "分组 · " + id + " “系统空闲 / 提醒时间”在“触发方式”组里",
      s.trig.slice(0, 60),
    );
    check(
      !/提醒时长|提前通知|推迟时长/.test(s.trig),
      "分组 · " + id + " “弹窗设置”组那三个数没有混进“触发方式”",
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
  check(/倒计时/.test(struct.short?.trig || ""), "分组 · 短休息“触发方式”含计时方法");
  check(/倒计时/.test(struct.long?.trig || ""), "分组 · 长休息“触发方式”含计时方法");
  check(!/倒计时/.test(struct.tk?.trig || ""), "分组 · 外卖提醒没有计时方法（只有固定时刻）");
  check(/提醒时长秒/.test(struct.short?.rem || ""), "分组 · 短休息“提醒”含提醒时长");

  // ② 倒计时 ↔ 固定时间：组内“间隔 / 提醒时间”互换
  const snap = async (id) =>
    await page.evaluate((k) => {
      const card = document.getElementById(k + "-card");
      const q = (sel) => card.querySelector(sel);
      return {
        intervalHidden: q(".bk-countdown") ? q(".bk-countdown").classList.contains("hidden") : null,
        slotHidden: q(".slot-fixed") ? q(".slot-fixed").classList.contains("hidden") : null,
        slotBodyHidden: q(".slot-body") ? q(".slot-body").classList.contains("hidden") : null,
        summary: (document.getElementById(k + "-summary") || {}).textContent || "",
      };
    }, id);

  const pickMode = async (id, text) => {
    const hit = await page.evaluate(
      ({ k, t }) => {
        const opts = [...document.querySelectorAll(`#${k}-mode .mode-opt`)];
        const el = opts.find((e) => ((e.querySelector("b") || {}).textContent || "").trim() === t);
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
  check(v.intervalHidden === false && v.slotHidden === true, "切换 · 倒计时显示“间隔”、藏起“提醒时间”", JSON.stringify(v));
  check(await pickMode("short", "固定时间"), "切换 · 在段控里点“固定时间”");
  v = await snap("short");
  check(v.intervalHidden === true && v.slotHidden === false, "切换 · 固定时间藏起“间隔”、显示“提醒时间”", JSON.stringify(v));
  // 2026-11-10 起「提醒时间」这一层不再折叠（用户：不准折叠，必须展开），
  // 所以切到固定时间时看到的就是清单本身，不再有“自动摊开”这回事。
  check(v.slotBodyHidden === false, "切换 · “提醒时间”清单常驻展开（内容体没有 hidden）", JSON.stringify(v));
  check(/^固定时间/.test(v.summary), "切换 · 卡片右上摘要跟着变成固定时间", v.summary);

  await pickMode("short", "倒计时");
  v = await snap("short");
  check(v.intervalHidden === false && v.slotHidden === true, "切换 · 切回倒计时已还原", JSON.stringify(v));

  // ③ 对齐：组只是“加了标题”，不能把列推开
  const geo = await page.evaluate(() => {
    const r = (sel) => {
      const el = document.querySelector(sel);
      if (!el) return null;
      const b = el.getBoundingClientRect();
      return { l: Math.round(b.left), w: Math.round(b.width) };
    };
    // 头部行 / 内容体的“文字左缘”= 元素左缘 + 内边距。盒子的头部与内容体都是整宽铺满的
    // （悬停底色要铺满整盒），所以量容器左缘没有意义，要量它把文字放到哪一列。
    const textLeft = (el) => {
      if (!el) return null;
      const b = el.getBoundingClientRect();
      return Math.round(b.left + (parseFloat(getComputedStyle(el).paddingLeft) || 0));
    };
    const card = document.getElementById("short-card");
    const trig = card.querySelector('.pg[data-pg="trigger"]');
    const msgPanel = document.querySelector("#short-card > .msg-panel");
    return {
      cardHead: r("#short-card > .card-head"),
      pgHead: r('.pg[data-pg="trigger"] > .pg-head'),
      // 组标题 / 组内容区的文字列（2026-11-10：分区改成描边盒，头部与内容体都是整宽元素）
      pgTitleLeft: textLeft(trig.querySelector(".pg-head > .msg-toggle")),
      pgBodyLeft: textLeft(trig.querySelector(":scope > .pg-body")),
      msgToggleLeft: textLeft(msgPanel.querySelector(".msg-head > .msg-toggle")),
      msgHead: r("#short-card > .msg-panel > .msg-head"),
      trigLabel: r('.pg[data-pg="trigger"] .row > label'),
      remLabel: r('.pg[data-pg="reminder"] .row > label'),
      headSwitch: r("#short-card > .card-head > .card-switch"),
      headAct: r("#short-preview"),
      headSum: r("#short-summary"),
      // 预览是文本动作：无描边、无底色（否则它和总开关并排会被读成“两个按钮”）
      actBorder: getComputedStyle(document.getElementById("short-preview")).borderTopColor,
      actBg: getComputedStyle(document.getElementById("short-preview")).backgroundColor,
      trigInput: r("#short-interval"),
      remInput: r("#short-duration"),
      gateInput: r("#short-idleGate-sec"),
      remUnit: r('.pg[data-pg="reminder"] .row .field-line > .unit'),
      // 组标题的字号：应当比行标签（14px）小一档，收起时也能一眼分出层级
      titleSize: parseFloat(getComputedStyle(trig.querySelector(".pg-title")).fontSize),
      labelSize: parseFloat(
        getComputedStyle(trig.querySelector(".row > label")).fontSize,
      ),
      // 盒子本身不吃内边距（外层 -13px 外边距留给描边与内容体的 12px 内边距），
      // 内容体的 12px + 1px 描边正好抵回原位 —— 与 2026-11-10 之前“灰面靠内边距撑开”的
      // 写法不同：那时是 padding 抵 margin，现在是 body 的 padding 抵 margin 与 border。
      pgPadL: parseFloat(getComputedStyle(trig).paddingLeft) || 0,
      bodyPadL: parseFloat(getComputedStyle(trig.querySelector(":scope > .pg-body")).paddingLeft) || 0,
      bodyBorderL: parseFloat(getComputedStyle(trig).borderLeftWidth) || 0,
      mL: parseFloat(getComputedStyle(trig).marginLeft) || 0,
      mR: parseFloat(getComputedStyle(trig).marginRight) || 0,
      // 分区不再铺底色（2026-11-10 撤灰底）：底色必须与卡片同色，边界只由 1px 描边承担
      pgBg: getComputedStyle(trig).backgroundColor,
      cardBg: getComputedStyle(document.getElementById("short-card")).backgroundColor,
      pgBorder: getComputedStyle(trig).borderTopWidth + " " + getComputedStyle(trig).borderTopColor,
      // 组标题要比行标签更深，才能先被读到
      titleColor: getComputedStyle(trig.querySelector(".pg-title")).color,
      labelColor: getComputedStyle(trig.querySelector(".row > label")).color,
      // 「提醒内容」这个折叠分区与 .pg 必须是同一套外壳 —— 卡内只有一种分区语言
      msgBg: getComputedStyle(msgPanel).backgroundColor,
      msgBorder: getComputedStyle(msgPanel).borderTopWidth,
      // 组内输入框是灰底（--bg），与白色卡片区分开，控件的“输入面”看得见
      inputBg: getComputedStyle(document.getElementById("short-duration")).backgroundColor,
    };
  });
  // 盒子本身不吃内边距：外层 -13px 由「1px 描边 + 内容体 12px 内边距」抵回原位，
  // 于是 .row.field 的网格坐标仍等于全卡片坐标（标签列不会整体右移）。
  check(
    !!geo && geo.pgPadL === 0 &&
      Math.abs(-geo.mL - (geo.bodyPadL + geo.bodyBorderL)) <= 0 &&
      Math.abs(-geo.mR - (geo.bodyPadL + geo.bodyBorderL)) <= 0,
    "分组 · 盒子的负外边距被“描边 + 内容体内边距”抵掉（内容盒不平移）",
    geo ? `margin ${geo.mL}/${geo.mR} · border ${geo.bodyBorderL} + body pad ${geo.bodyPadL} · pg pad ${geo.pgPadL}` : "量不到",
  );
  // 2026-11-10 按用户要求撤掉卡内灰底：分区不再自己铺底色（透明 = 直接露出卡片白），
  // 边界由 1px 描边承担
  check(
    !!geo && (geo.pgBg === "rgba(0, 0, 0, 0)" || geo.pgBg === geo.cardBg) &&
      /^1px/.test(geo.pgBorder),
    "分组 · 分区不再铺底色（与卡片同底），只靠 1px 描边划界",
    geo ? geo.pgBg + " vs " + geo.cardBg + " · border " + geo.pgBorder : "量不到",
  );
  check(
    !!geo && geo.msgBg === geo.pgBg && geo.msgBorder === "1px",
    "分组 · 「提醒内容」折叠分区与参数组同一套外壳（同底 + 同描边）",
    geo ? geo.msgBg + " vs " + geo.pgBg + " · border " + geo.msgBorder : "量不到",
  );  check(
    !!geo && geo.inputBg !== geo.pgBg,
    "分组 · 组内输入框底色不同于分区（输入面看得见）",
    geo ? geo.inputBg + " vs " + geo.pgBg : "量不到",
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
  // 卡头 / 组标题 / 组内容体 / 折叠分区标题的文字列必须在同一条竖线上。
  // 2026-11-10 起分区是描边盒：头部与内容体都整宽铺满（悬停底色要铺满整盒），所以量的是
  // 它们把文字放到哪一列（左缘 + 内边距），不是容器左缘 —— 容器左缘比卡头左 12px。
  check(
    !!geo && geo.cardHead && geo.pgTitleLeft !== null && geo.trigLabel && geo.remLabel &&
      geo.msgToggleLeft !== null && geo.pgBodyLeft !== null &&
      Math.abs(geo.trigLabel.l - geo.remLabel.l) <= 1 &&
      Math.abs(geo.pgTitleLeft - geo.cardHead.l) <= 1 &&
      Math.abs(geo.pgBodyLeft - geo.cardHead.l) <= 1 &&
      Math.abs(geo.trigLabel.l - geo.cardHead.l) <= 1 &&
      Math.abs(geo.msgToggleLeft - geo.cardHead.l) <= 1,
    "分组 · 卡头 / 组标题 / 组内容体 / 折叠分区标题同一条竖线",
    JSON.stringify(geo && {
      head: geo.cardHead && geo.cardHead.l,
      pgTitle: geo.pgTitleLeft,
      pgBody: geo.pgBodyLeft,
      trig: geo.trigLabel && geo.trigLabel.l,
      rem: geo.remLabel && geo.remLabel.l,
      msgToggle: geo.msgToggleLeft,
    }),
  );
  // 数值框 + 框外单位是同一套度量单元：所有数值框同左缘、同宽（含系统空闲门的秒数框），
  // 单位紧跟在框右侧（不再折进标签写成“提醒时长（秒）”）
  check(
    !!geo && geo.remInput && geo.gateInput && geo.trigInput && geo.remUnit &&
      Math.abs(geo.remInput.l - geo.trigInput.l) <= 1 &&
      Math.abs(geo.gateInput.l - geo.remInput.l) <= 1 &&
      Math.abs(geo.trigInput.w - geo.remInput.w) <= 1 &&
      Math.abs(geo.remInput.w - geo.gateInput.w) <= 1 &&
      geo.remUnit.l > geo.remInput.l + geo.remInput.w &&
      geo.remUnit.l - (geo.remInput.l + geo.remInput.w) <= 12,
    "分组 · 数值框同左缘同宽，单位紧跟在框右侧",
    JSON.stringify(geo && {
      trig: geo.trigInput, rem: geo.remInput, gate: geo.gateInput, unit: geo.remUnit,
    }),
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

  // ⑤ 组的开合（2026-11-10）：三块分区同构 —— 组头是一枚 .msg-toggle 按钮，内容体是
  // .pg-body。点一下要真的收起（内容体 display:none、aria-expanded 变 false、▸ 转回朝右），
  // 再点一下要复原。这一条钉的是“可折叠”本身：只改样式不改接线时，头部会点不动。
  const pgHead = await page.evaluate(() => {
    const btn = document.querySelector('#short-card .pg[data-pg="trigger"] > .pg-head > .msg-toggle');
    if (!btn) return null;
    const arrow = btn.querySelector(".msg-arrow");
    const cs = getComputedStyle(btn);
    return {
      tag: btn.tagName,
      expanded: btn.getAttribute("aria-expanded"),
      radius: cs.borderTopLeftRadius,
      padding: cs.paddingTop + "/" + cs.paddingLeft,
      arrow: arrow ? arrow.textContent.trim() : null,
      arrowFont: arrow ? getComputedStyle(arrow).fontSize : null,
    };
  });
  check(
    !!pgHead && pgHead.tag === "BUTTON" && pgHead.expanded === "true" &&
      pgHead.arrow === "▸" && pgHead.radius !== "0px" && /^9px\/12px$/.test(pgHead.padding),
    "分组 · 组头是一枚可折叠按钮（▸ + 标题，圆角与内边距与其它分区一致）",
    JSON.stringify(pgHead),
  );
  const foldStateOf = (kind) =>
    page.evaluate((k) => {
      const pg = document.querySelector(`#short-card .pg[data-pg="${k}"]`);
      const body = pg.querySelector(":scope > .pg-body");
      const btn = pg.querySelector(":scope > .pg-head > .msg-toggle");
      const arrow = btn.querySelector(".msg-arrow");
      return {
        open: pg.classList.contains("open"),
        bodyHidden: getComputedStyle(body).display === "none",
        expanded: btn.getAttribute("aria-expanded"),
        arrow: getComputedStyle(arrow).transform,
      };
    }, kind);
  const openBefore = await foldStateOf("trigger");
  await page.click('#short-card .pg[data-pg="trigger"] > .pg-head > .msg-toggle');
  await sleep(400); // 箭头有 .15s 旋转过渡，等到位再量，否则比到的是中间帧
  const closed = await foldStateOf("trigger");
  check(
    openBefore.bodyHidden === false && closed.open === false && closed.bodyHidden === true &&
      closed.expanded === "false" && closed.arrow !== openBefore.arrow,
    "分组 · 点组头收起内容体（display:none、aria-expanded=false、▸ 转回朝右）",
    JSON.stringify({ openBefore, closed }),
  );
  // 收起时整组只剩一行标题：高度应当接近头部自己
  const closedBox = await page.evaluate(() => {
    const pg = document.querySelector('#short-card .pg[data-pg="trigger"]');
    const head = pg.querySelector(":scope > .pg-head");
    const r = (el) => Math.round(el.getBoundingClientRect().height);
    return { box: r(pg), head: r(head) };
  });
  check(
    closedBox.box - closedBox.head <= 3,
    "分组 · 收起后整组只剩一行标题（高度 = 头部高度）",
    JSON.stringify(closedBox),
  );
  await page.click('#short-card .pg[data-pg="trigger"] > .pg-head > .msg-toggle');
  await sleep(400);
  const reopened = await foldStateOf("trigger");
  check(
    reopened.open === true && reopened.bodyHidden === false && reopened.expanded === "true" &&
      reopened.arrow === openBefore.arrow,
    "分组 · 再点一次复原（展开、内容体回来、箭头转回朝下）",
    JSON.stringify(reopened),
  );
  // 克隆出来的自定义卡也必须能开合（克隆不复制事件，漏了重挂就点不动）
  const cloned = await page.evaluate(() => {
    const card = document.querySelector("#tab-timing > .card[data-cb]");
    if (!card) return null;
    const pg = card.querySelector('.pg[data-pg="reminder"]');
    const btn = pg && pg.querySelector(":scope > .pg-head > .msg-toggle");
    if (btn) btn.click();
    const body = pg && pg.querySelector(":scope > .pg-body");
    const out = {
      open: pg ? pg.classList.contains("open") : null,
      bodyHidden: body ? getComputedStyle(body).display === "none" : null,
      expanded: btn ? btn.getAttribute("aria-expanded") : null,
    };
    if (btn) btn.click(); // 复原，别把状态漏给后面的用例
    return out;
  });
  check(
    cloned === null || (cloned.open === false && cloned.bodyHidden === true && cloned.expanded === "false"),
    "分组 · 自定义卡（克隆）的两个组同样能开合",
    JSON.stringify(cloned),
  );
  // 「提醒时间」这一层**整体不可折叠**（2026-11-10 用户要求：不准折叠，必须展开）。
  // 全页扫一遍：任何 .slot-fixed 都不许再挂折叠开关，内容体也不许有 hidden 类。
  // 扫全页而不是只看短休息卡，是因为这三块是同一段模板复制出来的，漏改一处就会露出来。
  const slots = await page.evaluate(() =>
    [...document.querySelectorAll(".slot-fixed")].map((p) => ({
      toggle: p.querySelectorAll(".msg-toggle").length,
      head: !!p.querySelector(".slot-head .slot-title"),
      bodyHidden: !!(p.querySelector(".slot-body") || {}).classList?.contains("hidden"),
    })),
  );
  check(
    slots.length >= 3 && slots.every((s) => s.toggle === 0 && s.head && !s.bodyHidden),
    "时刻表 · 全部「提醒时间」都不折叠（没有开关、没有 hidden）",
    JSON.stringify(slots),
  );
}

// 计时方法：两段式单选（.mode-seg）
//
// 2026-11-09 由自绘下拉改成段控。理由：这是全卡唯一“要先做个决定”的地方，
// 两个选项互斥，而且直接决定组里出现“间隔”还是“提醒时间”。折进下拉里，用户得
// 先点开才知道“有几种、每种什么意思”；摊成并排两段（各带一句人话解释）之后，
// “两种触发方式”本身就成了版式的一部分。
// 这一组钉住最容易回退的五处：
//   · 语义：radiogroup / radio / aria-checked 互斥、整组只有一个 tab 停靠点
//   · 文案：两段各自的副标题（用户正是靠它区分两种方式）
//   · 交互：点击 → 选中态互换 + 配置落盘 + 摘要与组内显隐跟着变
//   · 键盘：方向键在组内换项（漏了这段，键盘用户进不去第二段）
//   · 几何：整宽（与组标题竖条同左缘、与组内字段行同右缘）、比字段行高
async function verifyModeSeg(page) {
  console.log("\n[timing] 计时方法（两段式段控）");
  await page.evaluate(() => {
    [...document.querySelectorAll(".nav-item")].find((b) => b.dataset.tab === "timing").click();
  });
  await sleep(350);

  // ① 结构 / 语义 / 文案
  const st = await page.evaluate(() => {
    const one = (k) => {
      const host = document.getElementById(k + "-mode");
      if (!host) return null;
      const opts = [...host.querySelectorAll(".mode-opt")];
      return {
        cls: host.className,
        role: host.getAttribute("role"),
        label: host.getAttribute("aria-label"),
        count: opts.length,
        texts: opts.map(
          (o) => ((o.querySelector("b") || {}).textContent || "") + "/" + ((o.querySelector("i") || {}).textContent || ""),
        ),
        roles: opts.map((o) => o.getAttribute("role")),
        checked: opts.map((o) => o.getAttribute("aria-checked")),
        tabs: opts.map((o) => o.tabIndex),
        icons: opts.map((o) => !!o.querySelector("svg.mode-ico")),
      };
    };
    return { short: one("short"), long: one("long"), tk: one("tk") };
  });

  for (const id of ["short", "long"]) {
    const s = st[id];
    if (!check(!!s, "段控 · " + id + " 有计时方法控件")) continue;
    check(/mode-seg/.test(s.cls), "段控 · " + id + " 是 .mode-seg（不再是下拉宿主）", s.cls);
    check(
      s.role === "radiogroup" && s.label === "计时方法",
      "段控 · " + id + " role=radiogroup 且可访问名为“计时方法”",
      s.role + " / " + s.label,
    );
    check(s.count === 2 && s.roles.every((r) => r === "radio"), "段控 · " + id + " 两段，都是 role=radio", s.count);
    check(
      s.checked.filter((c) => c === "true").length === 1,
      "段控 · " + id + " aria-checked 恰好一段为 true",
      s.checked.join(","),
    );
    check(
      s.tabs.filter((t) => t === 0).length === 1,
      "段控 · " + id + " 整组只有一个 tab 停靠点（另一个 -1）",
      s.tabs.join(","),
    );
    check(
      s.texts.join("|") === "倒计时/每隔一段时间|固定时间/每天指定时刻",
      "段控 · " + id + " 两段的标签与解释文案",
      s.texts.join("|"),
    );
    check(s.icons.every(Boolean), "段控 · " + id + " 每段都有图标");
  }
  check(!st.tk, "段控 · 外卖提醒没有计时方法（只有固定时刻）", st.tk && st.tk.count);

  // ② 点击 → 选中态 / 显隐 / 摘要 / 配置
  const clickOpt = async (id, mode) => {
    const hit = await page.evaluate(
      ({ k, m }) => {
        const el = document.querySelector(`#${k}-mode .mode-opt[data-mode="${m}"]`);
        if (!el) return false;
        el.click();
        return true;
      },
      { k: id, m: mode },
    );
    if (hit) await waitSettled(page);
    await sleep(200);
    return hit;
  };
  const viewLong = async () =>
    await page.evaluate(() => {
      const host = document.getElementById("long-mode");
      const card = document.getElementById("long-card");
      return {
        value: host.dataset.value,
        checked: [...host.querySelectorAll(".mode-opt")].map((o) => o.getAttribute("aria-checked")),
        intervalHidden: card.querySelector(".bk-countdown").classList.contains("hidden"),
        slotHidden: card.querySelector(".slot-fixed").classList.contains("hidden"),
        summary: (document.getElementById("long-summary") || {}).textContent.trim(),
      };
    });

  check(await clickOpt("long", "fixed"), "段控 · 点“固定时间”");
  let cfg = await readCfg(page);
  let ui = await viewLong();
  check(cfg.long.mode === "fixed", "段控 · 写入 long.mode=fixed", cfg.long.mode);
  check(ui.value === "fixed" && ui.checked.join(",") === "false,true", "段控 · 选中态互换", ui.checked.join(","));
  check(
    ui.intervalHidden === true && ui.slotHidden === false,
    "段控 · 固定时间藏“间隔”、显示“提醒时间”",
    JSON.stringify(ui),
  );
  check(/^固定时间/.test(ui.summary), "段控 · 卡片摘要跟着变", ui.summary);

  check(await clickOpt("long", "countdown"), "段控 · 点回“倒计时”");
  cfg = await readCfg(page);
  ui = await viewLong();
  check(cfg.long.mode === "countdown", "段控 · 写回 long.mode=countdown", cfg.long.mode);
  check(
    ui.intervalHidden === false && ui.slotHidden === true,
    "段控 · 倒计时还原“间隔”、藏起“提醒时间”",
    JSON.stringify(ui),
  );

  // ③ 键盘：焦点落在选中段上，方向键换项（radio 组的本分）
  await page.evaluate(() => {
    document.querySelector("#long-mode .mode-opt.on").focus();
  });
  const from = await page.evaluate(() => document.activeElement.dataset.mode);
  await page.keyboard.press("ArrowRight");
  await sleep(220);
  const kbd = await page.evaluate(() => {
    const host = document.getElementById("long-mode");
    return {
      focus: document.activeElement ? document.activeElement.dataset.mode : null,
      checked: [...host.querySelectorAll(".mode-opt")].map((o) => o.getAttribute("aria-checked")),
    };
  });
  check(kbd.focus !== from, "段控 · ArrowRight 把焦点移到另一段", from + " → " + kbd.focus);
  check(
    kbd.checked.join(",") === (from === "countdown" ? "false,true" : "true,false"),
    "段控 · 键盘换项同时改选中态",
    kbd.checked.join(","),
  );
  // 回到起始值：这组断言不许给后面的用例留状态
  await clickOpt("long", from);
  await page.keyboard.press("Tab");

  // ④ 几何：整宽、与组标题文字同左缘、与组内字段行同右缘、比字段行高
  const geo = await page.evaluate(() => {
    const r = (el) => {
      if (!el) return null;
      const b = el.getBoundingClientRect();
      return { l: Math.round(b.left), r: Math.round(b.right), h: Math.round(b.height) };
    };
    const card = document.getElementById("short-card");
    const btn = card.querySelector('.pg[data-pg="trigger"] .pg-head > .msg-toggle');
    return {
      seg: r(document.getElementById("short-mode")),
      // 量头部按钮把文字放到哪一列（按钮整宽铺满，左缘比文字左 12px）
      head: btn
        ? {
            l: Math.round(btn.getBoundingClientRect().left + (parseFloat(getComputedStyle(btn).paddingLeft) || 0)),
          }
        : null,
      row: r(card.querySelector('.pg[data-pg="reminder"] .row')),
      field: r(card.querySelector('#short-duration')),
    };
  });
  check(
    !!geo.seg && !!geo.head && Math.abs(geo.seg.l - geo.head.l) <= 1,
    "段控 · 左缘与组标题文字同一条线",
    JSON.stringify({ seg: geo.seg, head: geo.head }),
  );
  check(
    !!geo.seg && !!geo.row && Math.abs(geo.seg.r - geo.row.r) <= 2,
    "段控 · 右缘与组内字段行齐平",
    JSON.stringify({ seg: geo.seg, row: geo.row }),
  );
  check(
    !!geo.seg && !!geo.field && geo.seg.h >= geo.field.h + 8,
    "段控 · 比字段行的输入框高一截（组里的主开关）",
    geo.seg.h + " vs " + (geo.field && geo.field.h),
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
        const opts = [...document.querySelectorAll(`#${k}-mode .mode-opt`)];
        const el = opts.find((e) => ((e.querySelector("b") || {}).textContent || "").trim() === t);
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
    const cap = panel.querySelector(".slot-head .slot-title");
    const add = panel.querySelector(".slot-add");
    // 组里第一个“看得见的”行标签：fixed 模式下“间隔”行是 display:none，
    // 直接取 .row > label 会拿到那条 0×0 的，量出来的左缘是 0。
    const trigLabel = [...card.querySelectorAll('.pg[data-pg="trigger"] .row')]
      .map((r) => r.querySelector(":scope > label"))
      .find((l) => l && l.getBoundingClientRect().width > 0) || null;
    // 卡头（▾ 标题 + 拨动开关 + 摘要 + 预览）是卡里最靠上的容器，它的左缘就是全卡的左对齐线。
    // 原来这里比的是正文第一行的“启用”行 —— 那一行已经并进卡头，改钉卡头容器本身
    // （用容器而不是 h3：h3 前面还有折叠箭头，它的左缘天然比容器右 24px）。
    const enableLabel = card.querySelector(":scope > .card-head");
    const L = (el) => (el ? Math.round(el.getBoundingClientRect().left) : -1);
    const R = (el) => (el ? Math.round(el.getBoundingClientRect().right) : -1);
    const rows = [...panel.querySelectorAll(".tk-item")].map((r) => {
      const t = r.querySelector(".tk-time input[type=time]");
      const rep = r.querySelector(".tk-repeat");
      const del = r.querySelector(".btn.danger");
      const rs = getComputedStyle(r);
      return {
        type: t ? t.type : "",
        value: t ? t.value : "",
        disp: rs.display,
        cols: rs.gridTemplateColumns.split(" ").length,
        timeL: L(t),
        repL: L(rep),
        delR: R(del),
        rowR: R(r),
      };
    });
    return {
      border: [cs.borderTopWidth, cs.borderLeftWidth],
      radius: cs.borderRadius,
      panelPadL: parseFloat(cs.paddingLeft) || 0,
      bodyPadL: body ? parseFloat(getComputedStyle(body).paddingLeft) || 0 : -1,
      capL: L(cap),
      capR: R(cap),
      addL: L(add),
      trigL: L(trigLabel),
      enableL: L(enableLabel),
      capSize: parseFloat(getComputedStyle(cap).fontSize),
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
      Math.abs(geo.capL - geo.trigL) <= 1 && Math.abs(geo.capL - geo.enableL) <= 1,
      "时刻表 · 子标签与组内标签、卡头同一条竖线",
      JSON.stringify({ cap: geo.capL, trig: geo.trigL, enable: geo.enableL }),
    );
    check(
      geo.addL > geo.capR && geo.addL - geo.capR <= 60,
      "时刻表 · “＋ 添加”紧跟在子标签右侧（不在行尾）",
      geo.capR + " → " + geo.addL,
    );
    check(
      geo.capSize < 14,
      "时刻表 · 子标签字号小于行标签（层级在组标题之下）",
      geo.capSize + "px",
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
    check(
      rows.length === 2 && rows.every((r) => r.type === "time" && /^\d{2}:\d{2}$/.test(r.value)),
      "时刻表 · 时刻是一个原生 time 框（HH:MM）",
      JSON.stringify(rows.map((r) => r.type + " " + r.value)),
    );
  }

  // 点一下子标签行（旧版这里是折叠开关）：清单不许收起。这条是防回潮的关键 ——
  // 谁把 msg-toggle 加回来，上面那条 headToggle 会红，这条也不会被“默认展开”糊弄过去。
  await page.evaluate(() => {
    document.querySelector('#short-card .pg[data-pg="trigger"] .slot-head')?.click();
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

    // 参数分组：计时方法 / 间隔 / 提醒时间 / 系统空闲 收进“触发方式”组
    await verifyParamGroups(page);
    // 计时方法的两段式段控：语义 / 文案 / 点击 / 键盘 / 几何
    await verifyModeSeg(page);
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
