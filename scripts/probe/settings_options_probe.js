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
