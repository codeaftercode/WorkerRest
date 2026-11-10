/**
 * 设置页 UI 全扫描（Playwright 驱动系统 Edge）：六个 tab 逐个过一遍。
 *
 * 与 probe_renderer.js 的分工：那个脚本偏“关键链路的深断言”（休息窗、外卖窗、
 * 立绘几何、消息池）；本脚本偏“广度”—— 把设置页每个 tab 的**全部可见控件**
 * 扫一遍，逐条断言：
 *
 *   1. 切过去之后这个 tab 真的是 active 且可见（不是样式没生效、也不是被别的 tab 盖住）；
 *   2. 控件总数不低于基线（控件被误删 / 渲染中断会立刻掉下去）；
 *   3. 每个控件都有可访问名（与 a11y_audit.js 同口径，但这里只看可见控件）；
 *   4. 内容区没有横向溢出（新增控件把布局撑破是重构后最常见的视觉回归）；
 *   5. 关键控件的值与 settings 绑定一致（写入侧改了字段名，读侧就空白）；
 *   6. 十个 .modal 的语义契约齐全（role / aria-modal / aria-labelledby 指向存在 / 有可聚焦项）。
 *
 * 运行：由 probe_settings_ui.py 起好无头服务后 `node settings_ui_probe.js` 拉起。
 * 环境变量：WR_BASE（服务地址）、WR_OUT（截图目录）。
 */
const { chromium } = require("playwright");
const path = require("path");
const fs = require("fs");

const BASE = process.env.WR_BASE || "http://127.0.0.1:29741";
const OUT = process.env.WR_OUT || path.join(process.env.TEMP || ".", "wr_settings_ui");
const PRELOAD = path.join(__dirname, "..", "probe_preload.js");
const delay = (ms) => new Promise((r) => setTimeout(r, ms));

let checks = 0;
const failures = [];
function check(ok, label, detail = "") {
  checks++;
  console.log(("  " + (ok ? "ok  " : "FAIL") + " " + label + (detail ? "  | " + detail : "")).replace(/\n/g, " "));
  if (!ok) failures.push(label + (detail ? "  | " + detail : ""));
  return ok;
}

// 每个 tab 的控件数基线：取当前实现的九成，掉下去就说明有大片控件没渲染出来。
const TABS = [
  { id: "general", nav: "通用配置", min: 40 },
  { id: "timing", nav: "时间规划", min: 25 },
  { id: "supervisor", nav: "督休专员", min: 10 },
  { id: "stats", nav: "统计报表", min: 6 },
  { id: "shortcuts", nav: "快捷键", min: 4 },
  { id: "about", nav: "关于", min: 3 },
];

const MODALS = [
  "batch-modal", "confirm-modal", "prompt-modal", "app-pick-modal", "color-modal",
  "update-modal", "updated-modal", "stats-modal", "snd-scan-modal", "notices-modal",
];

async function main() {
  if (!fs.existsSync(OUT)) fs.mkdirSync(OUT, { recursive: true });
  const browser = await chromium.launch({ channel: "msedge", headless: true });
  const ctx = await browser.newContext({ viewport: { width: 1000, height: 820 }, deviceScaleFactor: 1 });
  const page = await ctx.newPage();
  // 单个操作最多等 8 秒：卡住时立刻暴露，而不是把整个探针拖到超时。
  page.setDefaultTimeout(8000);
  try {

  const consoleErrors = [];
  page.on("pageerror", (e) => consoleErrors.push("pageerror: " + e.message));
  page.on("console", (m) => {
    if (m.type() !== "error") return;
    const t = m.text();
    // favicon 之类与页面无关的噪声不算
    if (/favicon|net::ERR_|Failed to load resource/i.test(t)) return;
    consoleErrors.push("console: " + t);
  });
  await page.addInitScript({ content: fs.readFileSync(PRELOAD, "utf-8") });

  // shim.js 装好 window.api 才算页面真的跑起来了
  await page.goto(BASE + "/settings.html", { waitUntil: "domcontentloaded" });
  let apiReady = false;
  for (let i = 0; i < 40; i++) {
    apiReady = await page.evaluate(() => typeof window.api === "object" && window.api !== null);
    if (apiReady) break;
    await delay(150);
  }
  if (!check(apiReady, "settings.html 装载完成且 window.api 就绪")) {
    return finish();
  }
  await delay(600);

  // 读一份配置，用来校验“界面上的值来自 settings”
  const cfg = await page.evaluate(async () => await window.api.getSettings());
  const roles = await page.evaluate(async () => await window.api.listSupervisorRoles());

  for (const tab of TABS) {
    const nav = page.locator(`.nav-item[data-tab="${tab.id}"]`);
    await nav.click();
    await delay(220);
    const section = page.locator(`#tab-${tab.id}`);
    const visible = await section.isVisible();
    const active = await section.evaluate((el) => el.classList.contains("active"));
    check(visible && active, `tab ${tab.id}（${tab.nav}）切换后 active 且可见`, `visible=${visible} active=${active}`);

    // 控件扫描：只看可见的（隐藏的 tab 里也有控件，不能混算）
    const info = await section.evaluate((root) => {
      const sel = 'input:not([type="hidden"]), button, select, textarea, [role="switch"], [role="checkbox"], [role="radio"], .cdrop-btn';
      const all = [...root.querySelectorAll(sel)].filter((el) => {
        const r = el.getBoundingClientRect();
        return r.width > 0 && r.height > 0 && getComputedStyle(el).visibility !== "hidden";
      });
      function accName(el) {
        const aria = (el.getAttribute("aria-label") || "").trim();
        if (aria) return aria;
        const lb = el.getAttribute("aria-labelledby");
        if (lb) {
          const t = lb.split(/\s+/).map((id) => document.getElementById(id))
            .filter(Boolean).map((e) => (e.textContent || "").trim()).join(" ").trim();
          if (t) return t;
        }
        if (el.id) {
          const l = root.querySelector(`label[for="${CSS.escape(el.id)}"]`);
          if (l && (l.textContent || "").trim()) return l.textContent.trim();
        }
        const wrap = el.closest("label");
        if (wrap && (wrap.textContent || "").trim()) return wrap.textContent.trim();
        const title = (el.getAttribute("title") || "").trim();
        if (title) return title;
        return (el.textContent || "").trim();
      }
      const unnamed = all.filter((el) => !accName(el))
        .map((el) => el.tagName.toLowerCase() + (el.id ? "#" + el.id : "") + (el.className ? "." + String(el.className).split(" ")[0] : ""));
      return { count: all.length, unnamed };
    });
    check(info.count >= tab.min, `tab ${tab.id} 可见控件数 ≥ ${tab.min}`, info.count);
    check(info.unnamed.length === 0, `tab ${tab.id} 所有可见控件都有可访问名`, info.unnamed.slice(0, 6).join(", "));

    // 横向溢出：内容区不该出现滚动条
    const of = await section.evaluate((root) => {
      const c = root.closest(".content") || root;
      return { sw: c.scrollWidth, cw: c.clientWidth };
    });
    check(of.sw <= of.cw + 2, `tab ${tab.id} 内容区无横向溢出`, `scrollWidth=${of.sw} clientWidth=${of.cw}`);

    // 首屏截图。注意不能指望 fullPage：内容区是内层滚动容器（.content overflow），
    // body 高度等于视口，fullPage 只会得到同一张图。要靠下的卡片得先 scrollIntoView。
    await page.screenshot({ path: path.join(OUT, `tab_${tab.id}.png`) });
  }

  // ---------- 值与 settings 的一致性 ----------
  await page.locator('.nav-item[data-tab="general"]').click();
  await delay(150);
  // “提醒窗口”卡片在首屏之外，单独补一张 —— 里面有“显示的屏幕”多选下拉，
  // 是靠肉眼复核对齐 / 溢出的唯一凭证
  await page.locator("#ap-displays").scrollIntoViewIfNeeded();
  await delay(150);
  await page.screenshot({ path: path.join(OUT, "tab_general_windows.png") });
  const appName = await page.inputValue("#appName");
  check(appName === (cfg.appName || ""), "通用 tab 的标题输入框 == settings.appName",
    `ui=${appName} cfg=${cfg.appName}`);
  const auto = await page.isChecked("#autostart");
  check(auto === !!cfg.autostart, "开机自启动开关状态 == settings.autostart", `ui=${auto} cfg=${cfg.autostart}`);
  const idle = await page.inputValue("#idlePauseMin");
  check(String(idle) === String(cfg.idlePauseMin), "空闲分钟数 == settings.idlePauseMin",
    `ui=${idle} cfg=${cfg.idlePauseMin}`);
  // 两个投屏开关：缺键按“开”（后端 Flag(true,…) 同口径），所以判据是 !== false，
  // 不能写成 !cfg.xxx —— 老配置里没有这个键时会把“开”误判成“关”。
  for (const [id, key, label] of [
    ["#castWirelessPause", "castWirelessPause", "无线投放"],
    ["#castMirrorPause", "castMirrorPause", "复制模式"],
  ]) {
    const chk = await page.isChecked(id);
    check(chk === (cfg[key] !== false), `${label}自动暂停开关状态 == settings.${key}`,
      `ui=${chk} cfg=${cfg[key]}`);
  }

  // “显示的屏幕”多选下拉：选项来自现场枚举，**不能写死数量**（探针机器屏数不同）。
  // 挂载是异步的（fillForm 之后才拉列表），所以先等按钮出现再断言。
  await page.waitForFunction(() => {
    const host = document.getElementById("ap-displays");
    return !!host && !!host.querySelector(".cdrop-btn");
  }, null, { timeout: 5000 }).catch(() => {});
  const disp = await page.evaluate(async () => {
    const list = await window.api.listDisplays();
    const host = document.getElementById("ap-displays");
    const btn = host && host.querySelector(".cdrop-btn");
    const opts = host ? Array.from(host.querySelectorAll('[role="option"]')) : [];
    const box = host && host.querySelector('[role="listbox"]');
    return {
      n: Array.isArray(list) ? list.length : -1,
      btn: !!btn,
      disabled: btn ? !!btn.disabled : null,
      label: btn ? btn.getAttribute("aria-label") : null,
      opts: opts.length,
      selected: opts.filter((o) => o.getAttribute("aria-selected") === "true").length,
      multi: box ? box.getAttribute("aria-multiselectable") : null,
      text: btn ? btn.textContent.trim() : "",
    };
  });
  check(disp.btn, "“显示的屏幕”下拉已挂载", JSON.stringify(disp));
  if (disp.n > 0) {
    check(!disp.disabled, "枚举成功时下拉可用", `disabled=${disp.disabled}`);
    check(disp.opts === disp.n, "下拉选项数 == ListDisplays 返回的屏数",
      `opts=${disp.opts} n=${disp.n}`);
    check(disp.multi === "true", "多选下拉的 listbox 带 aria-multiselectable", disp.multi);
    check(disp.label === "显示的屏幕", "下拉按钮有可访问名", disp.label);
    // 勾选态必须与配置对得上。出厂 displayIds 是空数组，启动加载时会被补成主屏的**具体 ID**
    // （见 settings 包的 fillPrimaryDisplayID），所以正常情况下面看到的是一个 ID；
    // 空列表只在“枚举不可用”时还留着，同样按“只勾主屏”算。
    const ids = Array.isArray(cfg.displayIds) ? cfg.displayIds : [];
    const onlyPrimary = ids.length === 0;
    const wantSel = onlyPrimary ? 1 : Math.min(ids.length, disp.n);
    check(disp.selected === wantSel,
      "勾选数 == 配置（空配置按“只主屏”兜底，不是“全部”）",
      `selected=${disp.selected} want=${wantSel} ids=${JSON.stringify(ids)}`);
    check(disp.text !== "所有屏幕", "按钮不再出现“所有屏幕”这种动态全选文案", disp.text);
    // 底部刷新行：显示器热插拔，列表只在页面加载时枚举一次，必须有显式入口重新检测。
    // 顺带钉住结构：刷新按钮不能落在 listbox 里面，否则读屏会把它念成一块屏、
    // 上下键也会停在它上面（探针数选项用的是 [role=option]，能过；读屏过不了）。
    const foot = await page.evaluate(() => {
      const host = document.getElementById("ap-displays");
      const f = host.querySelector(".cdrop-foot");
      const btnR = host.querySelector(".cdrop-refresh");
      const box = host.querySelector('[role="listbox"]');
      return {
        hasFoot: !!f,
        note: f ? f.querySelector(".cdrop-foot-note").textContent.trim() : "",
        label: btnR ? btnR.getAttribute("aria-label") : null,
        inListbox: !!(box && box.querySelector(".cdrop-refresh")),
        optsInBox: box ? box.querySelectorAll('[role="option"]').length : -1,
      };
    });
    check(foot.hasFoot, "多选面板有底部的刷新行", JSON.stringify(foot));
    // 已连接的屏就列在上面，再数一遍“检测到 N 块”是噪音；空串或枚举失败时的
    // “点刷新重新检测”都不含这个口径，两种环境都成立。
    check(!/检测到\s*\d+\s*个/.test(foot.note),
      "刷新按钮左边不报“检测到 N 块”", foot.note);
    check(!!foot.label, "刷新按钮有可访问名", String(foot.label));
    check(!foot.inListbox, "刷新按钮不在 listbox 内（否则会被读成一块屏）", String(foot.inListbox));
    check(foot.optsInBox === disp.n, "listbox 里只有显示器选项", `opts=${foot.optsInBox} n=${disp.n}`);
  } else {
    // 枚举不到屏时不给一个死掉的控件：按钮仍可点开，面板里只剩“刷新”，
    // 用户接好屏当场就能重试，不用重启软件。
    check(disp.text.includes("未检测到"), "枚举失败时触发器说明现状", disp.text);
  }
  // 键盘可达：自绘控件最容易漏的就是这条（鼠标能点 ≠ Tab 能到 ≠ 读屏能念）
  if (disp.n > 0) {
    await page.focus("#ap-displays .cdrop-btn");
    await page.keyboard.press("Enter");
    await delay(120);
    const kbdOpen = await page.evaluate(() => ({
      expanded: document.querySelector("#ap-displays .cdrop-btn").getAttribute("aria-expanded"),
      role: document.activeElement && document.activeElement.getAttribute("role"),
    }));
    check(kbdOpen.expanded === "true", "Enter 展开多选面板", kbdOpen.expanded);
    check(kbdOpen.role === "option", "展开后焦点落在第一个选项", kbdOpen.role);
    await page.keyboard.press("Escape");
    await delay(120);
    const kbdClosed = await page.evaluate(() => ({
      expanded: document.querySelector("#ap-displays .cdrop-btn").getAttribute("aria-expanded"),
      onBtn: document.activeElement && document.activeElement.classList.contains("cdrop-btn"),
    }));
    check(kbdClosed.expanded === "false", "Esc 收起多选面板", kbdClosed.expanded);
    check(kbdClosed.onBtn, "收起后焦点回到按钮（不会丢到 body）", String(kbdClosed.onBtn));
  }
  // 手动刷新：点一下就重新枚举一次，面板不重挂、勾选与配置都不动（刷新本身不写配置）。
  // 这条钉的是“热插拔之后有没有补救入口”——显示器列表只在页面加载时枚举一次。
  if (disp.n > 0) {
    await page.click("#ap-displays .cdrop-btn");
    await delay(150);
    // 左列名字、右列分辨率：两列各司其职（2026-11-08 用户定的版式）。
    // 名字列绝不能写成“2560×1440”这种分辨率 —— 那等于把右列的话说第二遍，
    // 用户还会以为这块屏没有名字。左/右按几何位置判，不按 DOM 顺序判。
    const cols = await page.evaluate(() => {
      const host = document.getElementById("ap-displays");
      return Array.from(host.querySelectorAll('[role="option"]')).map((o) => {
        const l = o.querySelector(".cdrop-label");
        const h = o.querySelector(".cdrop-hint");
        const lr = l && l.getBoundingClientRect();
        const hr = h && h.getBoundingClientRect();
        return {
          label: l ? l.textContent.trim() : "",
          hint: h ? h.textContent.trim() : "",
          leftOfHint: !!(lr && hr && lr.right <= hr.left + 1),
          sameRow: !!(lr && hr && Math.abs((lr.top + lr.bottom) / 2 - (hr.top + hr.bottom) / 2) < 6),
        };
      });
    });
    const resLike = /^\d+\s*[×x]\s*\d+$/;
    check(cols.length === disp.opts, "每块屏都渲染了名字列与提示列",
      `cols=${cols.length} opts=${disp.opts}`);
    check(cols.every((c) => c.label && !resLike.test(c.label)),
      "选项左列是显示器名字，不是分辨率", JSON.stringify(cols.map((c) => c.label)));
    check(cols.every((c) => /^\d+\s*×\s*\d+/.test(c.hint)),
      "选项右列是分辨率", JSON.stringify(cols.map((c) => c.hint)));
    check(cols.every((c) => c.leftOfHint && c.sameRow),
      "名字在分辨率左侧、两者同一行", JSON.stringify(cols.map((c) => [c.leftOfHint, c.sameRow])));
    const beforeRefresh = await page.evaluate(() => ({
      opts: document.querySelectorAll("#ap-displays [role='option']").length,
      val: document.getElementById("ap-displays").dataset.value,
    }));
    await page.click("#ap-displays .cdrop-refresh");
    await page.waitForFunction(() => {
      const b = document.querySelector("#ap-displays .cdrop-refresh");
      return !!b && !b.classList.contains("busy");
    }, null, { timeout: 5000 }).catch(() => {});
    await delay(200);
    const afterRefresh = await page.evaluate(() => ({
      opts: document.querySelectorAll("#ap-displays [role='option']").length,
      val: document.getElementById("ap-displays").dataset.value,
      open: !document.querySelector("#ap-displays .cdrop-menu").classList.contains("hidden"),
      busy: document.querySelector("#ap-displays .cdrop-refresh").classList.contains("busy"),
      toast: (document.getElementById("toast") || {}).textContent || "",
    }));
    check(afterRefresh.opts === beforeRefresh.opts, "刷新后选项数不变（屏没增减）",
      `${beforeRefresh.opts} → ${afterRefresh.opts}`);
    check(afterRefresh.val === beforeRefresh.val, "刷新不改动勾选（刷新本身不写配置）",
      `${beforeRefresh.val} → ${afterRefresh.val}`);
    check(afterRefresh.open, "刷新后面板仍打开（就地替换选项，不重挂控件）",
      String(afterRefresh.open));
    check(!afterRefresh.busy, "刷新结束后按钮恢复可点", String(afterRefresh.busy));
    check(afterRefresh.toast.includes("检测到"), "刷新有可见反馈（提示条）", afterRefresh.toast);
    await page.keyboard.press("Escape");
    await delay(120);
  }

  await page.locator('.nav-item[data-tab="shortcuts"]').click();
  await delay(150);
  const sc = cfg.shortcuts || {};
  for (const [id, key] of [["sc-postpone", "postpone"], ["sc-endEarly", "endEarly"],
                           ["sc-pause", "pause"], ["sc-nextBreak", "nextBreak"]]) {
    const v = await page.inputValue("#" + id);
    check(String(v) === String(sc[key] || ""), `快捷键 ${key} 输入框 == settings.shortcuts.${key}`,
      `ui=${v} cfg=${sc[key]}`);
  }

  await page.locator('.nav-item[data-tab="timing"]').click();
  await delay(150);
  const iv = await page.inputValue("#short-interval");
  check(Number(iv) > 0, "时间规划 tab 的短休息间隔是正数", iv);
  const shEnabled = await page.isChecked("#short-enabled");
  check(shEnabled === !!(cfg.short && cfg.short.enabled), "短休息开关 == settings.short.enabled",
    `ui=${shEnabled} cfg=${cfg.short && cfg.short.enabled}`);

  await page.locator('.nav-item[data-tab="supervisor"]').click();
  // 立绘库是异步铺的（先读角色清单、再铺磁贴），固定等 200ms 在机器忙的时候会扑空：
  // 2026-11-09 与手册构建并发跑过一回，tiles=0 之后下面取 .role-sel 直接 8 秒超时崩掉
  // 整支探针，看着像“控件坏了”。改成等第一块磁贴出现（最多 5 秒），真的没渲染就
  // catch 掉、让下面的 check 如实报 FAIL，而不是抛异常中断。
  await page.waitForSelector("#sup-gallery .role-item", { timeout: 5000 }).catch(() => {});
  const tiles = await page.locator("#sup-gallery .role-tile, #sup-gallery > *").count();
  check(tiles >= (roles.length || 1), "立绘库磁贴数 ≥ ListRoles 返回的专员数",
    `tiles=${tiles} roles=${roles.length}`);

  // ---------- 勾选入口只在单选框/复选框上（2026-11-02 用户要求）----------
  // 点立绘磁贴 = 只切换“正在编辑的专员”，不许改勾选态；点名字前那个小方块才是勾选/取消。
  // 两种模式各验一次：固定显示=单选框（落 fixedRole）、随机/顺序=复选框（落 enabled）。
  {
    const sel = '#sup-gallery .role-item .role-sel';
    const read = () => page.evaluate(async () => {
      const s = await window.api.getSettings();
      const rs = await window.api.listSupervisorRoles();
      return {
        selection: (s && s.supervisor && s.supervisor.selection) || "",
        fixedRole: (s && s.supervisor && s.supervisor.fixedRole) || "",
        enabled: Object.fromEntries((rs || []).map((r) => [r.name, !!r.enabled])),
      };
    });
    const stateOf = () => page.evaluate(() =>
      [...document.querySelectorAll("#sup-gallery .role-item")].map((el) => ({
        name: el.dataset.name,
        on: el.querySelector(".role-sel")?.getAttribute("aria-checked") === "true",
        active: el.classList.contains("active"),
      })));

    const before = await read();
    const marks = await stateOf();
    // 优先挑一个“当前没勾上”的磁贴：勾上它才能看出“点立绘有没有偷偷改勾选”
    const target = marks.find((m) => !m.on) || marks[marks.length - 1];
    if (target && marks.length > 1) {
      const idx = marks.findIndex((m) => m.name === target.name);

      await page.locator("#sup-gallery .role-item").nth(idx).locator("img").click();
      await delay(400);
      const afterTile = await read();
      const marksTile = await stateOf();
      check(
        afterTile.fixedRole === before.fixedRole &&
          JSON.stringify(afterTile.enabled) === JSON.stringify(before.enabled),
        "点立绘磁贴不改勾选态（fixedRole / enabled 都没动）",
        `fixed ${before.fixedRole}→${afterTile.fixedRole}`,
      );
      check(marksTile[idx].on === target.on,
        "点立绘磁贴后该磁贴的选择指示不变", `on=${marksTile[idx].on}`);
      check(marksTile[idx].active === true, "点立绘磁贴切换了正在编辑的专员（.active 落到它上面）");

      await page.locator("#sup-gallery .role-item").nth(idx).locator(".role-sel").click();
      await delay(500);
      const afterSel = await read();
      const marksSel = await stateOf();
      if (before.selection === "fixed") {
        check(afterSel.fixedRole === target.name, "点单选框把该专员设为固定显示",
          `fixedRole=${afterSel.fixedRole}`);
        check(marksSel.filter((m) => m.on).length === 1, "单选框互斥：同时只有一个被选中",
          marksSel.filter((m) => m.on).map((m) => m.name).join(","));
        // 还原：点回原来那一枚
        const back = marks.findIndex((m) => m.name === before.fixedRole);
        if (back >= 0) {
          await page.locator("#sup-gallery .role-item").nth(back).locator(".role-sel").click();
          await delay(400);
        }
      } else {
        check(afterSel.enabled[target.name] === !before.enabled[target.name],
          "点复选框翻转该专员的启用态", `enabled=${afterSel.enabled[target.name]}`);
        check(marksSel[idx].on === !target.on, "复选框的 aria-checked 跟着变");
        // 还原：再点一次取消
        await page.locator("#sup-gallery .role-item").nth(idx).locator(".role-sel").click();
        await delay(400);
      }
      const restored = await read();
      check(
        restored.fixedRole === before.fixedRole &&
          JSON.stringify(restored.enabled) === JSON.stringify(before.enabled),
        "勾选态已还原（探针不留副作用）",
      );
    } else {
      check(false, "候选专员列表至少要有两个磁贴才能验勾选入口", `n=${marks.length}`);
    }
    check(await page.locator(sel).first().getAttribute("aria-checked") !== null,
      "选择指示带 aria-checked（不是纯装饰）");

    // 白勾要居中：当初是 border 拼的 L 形 + rotate(45deg)，墨迹天生朝右下角歪，
    // 左边一大块留白、右边贴着边框（2026-11-02 用户指出）。现在改成 clip-path
    // 形状铺满内框，这里钉住“不退回 border L 形”。
    const ink = await page.evaluate(() => {
      const el = document.querySelector("#sup-gallery .role-sel.on");
      if (!el) return null;
      const after = getComputedStyle(el, "::after");
      return {
        kind: el.classList.contains("role-check") ? "check" : "radio",
        clip: after.clipPath,
        top: after.top, right: after.right, bottom: after.bottom, left: after.left,
        transform: after.transform,
      };
    });
    if (ink && ink.kind === "check") {
      check(
        /^polygon/.test(ink.clip) && ink.transform === "none" &&
          ink.top === "0px" && ink.right === "0px" && ink.bottom === "0px" && ink.left === "0px",
        "复选框白勾是 clip-path 形状铺满内框（border L 形 + rotate 会挤到右下角）",
        JSON.stringify(ink));
    } else if (ink) {
      check(ink.clip === "none" && new Set([ink.top, ink.right, ink.bottom, ink.left]).size === 1,
        "单选框圆点四边等距（居中）", JSON.stringify(ink));
    }
  }

  // ---------- 交互：开关 / 滑块 / 自绘下拉 ----------
  await page.locator('.nav-item[data-tab="general"]').click();
  await delay(150);
  for (const id of ["dndPause", "fullscreenPause"]) {
    const sel = "#" + id;
    const before = await page.isChecked(sel);
    await page.click(sel);
    await delay(80);
    const mid = await page.isChecked(sel);
    await page.click(sel);
    await delay(80);
    const after = await page.isChecked(sel);
    check(mid !== before && after === before, id + " 开关点两次：状态翻转后复原",
      `${before} → ${mid} → ${after}`);
  }

  // 透明度滑块的定义域是 0~0.5（不是 0~100），按它自己的刻度取中点再断言标签变了
  const opBefore = await page.inputValue("#ap-opacity");
  const opBeforeText = ((await page.textContent("#ap-opacity-val")) || "").trim();
  const midVal = await page.locator("#ap-opacity").evaluate((el) => {
    const min = Number(el.min || 0);
    const max = Number(el.max || 1);
    const mid = (min + max) / 2;
    el.value = String(mid);
    el.dispatchEvent(new Event("input", { bubbles: true }));
    el.dispatchEvent(new Event("change", { bubbles: true }));
    return String(mid);
  });
  await delay(120);
  const opVal = ((await page.textContent("#ap-opacity-val")) || "").trim();
  check(opVal !== opBeforeText && opVal !== "", "透明度滑块改动后数值标签跟着变",
    `${opBefore}(${opBeforeText}) → ${midVal}(${opVal})`);

  // 自绘下拉在“时间规划”tab 的提醒卡里（通用 tab 的那几个收在折叠区里，不可见）
  await page.locator('.nav-item[data-tab="timing"]').click();
  await delay(180);
  const cdrop = page.locator(".tab.active .cdrop-host .cdrop-btn:visible").first();
  if (await cdrop.count()) {
    await cdrop.click();
    await delay(120);
    const open = await page.evaluate(() => {
      const m = [...document.querySelectorAll(".cdrop-menu")].find((el) => el.offsetParent !== null);
      return !!m;
    });
    check(open, "自绘下拉点开后菜单可见");
    await page.keyboard.press("Escape");
    await delay(120);
    const closed = await page.evaluate(() => {
      const m = [...document.querySelectorAll(".cdrop-menu")].find((el) => el.offsetParent !== null);
      return !m;
    });
    check(closed, "Escape 后自绘下拉收起");
    const focusBack = await page.evaluate(() => (document.activeElement || {}).className || "");
    check(String(focusBack).includes("cdrop-btn"), "Escape 后焦点回到下拉按钮", focusBack);
  } else {
    check(false, "页面上能找到自绘下拉（.cdrop-btn）");
  }

  // ---------- 十个弹窗的静态契约 ----------
  // 注意：.modal 默认 display:none，直接量 box 的宽度全是 0。这里**临时**摘掉 hidden
  // 量一次尺寸再挂回去（settings.js 的 MutationObserver 会顺手管焦点，不影响后续断言，
  // 因为这段已经是最后一步）。
  const modalReport = await page.evaluate((ids) => {
    const vw = window.innerWidth;
    return ids.map((id) => {
      const el = document.getElementById(id);
      if (!el) return { id, missing: true };
      const box = el.querySelector(".modal-box");
      const role = box ? box.getAttribute("role") : null;
      const modal = box ? box.getAttribute("aria-modal") : null;
      const lb = box ? box.getAttribute("aria-labelledby") : null;
      const lbTarget = lb ? document.getElementById(lb) : null;
      const focusables = box ? [...box.querySelectorAll(
        'a[href], button:not([disabled]), input:not([disabled]):not([type="hidden"]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])'
      )].length : 0;
      const wasHidden = el.classList.contains("hidden");
      if (wasHidden) el.classList.remove("hidden");
      const rect = box ? box.getBoundingClientRect() : { width: 0, left: 0, right: 0 };
      if (wasHidden) el.classList.add("hidden");
      return {
        id, role, modal, lb,
        lbText: lbTarget ? (lbTarget.textContent || "").trim().slice(0, 20) : null,
        focusables, w: rect.width, left: rect.left, right: rect.right, vw, missing: false,
      };
    });
  }, MODALS);

  for (const m of modalReport) {
    if (m.missing) {
      check(false, `弹窗 ${m.id} 存在`);
      continue;
    }
    check(["dialog", "alertdialog"].includes(m.role), `弹窗 ${m.id} 有 role`, m.role);
    check(m.modal === "true", `弹窗 ${m.id} 声明 aria-modal=true`, m.modal);
    check(!!m.lbText, `弹窗 ${m.id} 的 aria-labelledby 指向有文字的标题`, `${m.lb} → ${m.lbText}`);
    check(m.focusables >= 1, `弹窗 ${m.id} 至少有一个可聚焦控件`, m.focusables);
    check(m.w > 0 && m.left >= -1 && m.right <= m.vw + 1,
      `弹窗 ${m.id} 宽度在视口内且不越界`,
      `w=${Math.round(m.w)} left=${Math.round(m.left)} right=${Math.round(m.right)} vw=${m.vw}`);
  }

  // ---------- 侧栏状态卡：“等待空闲” ----------
  // 空闲门到点但人还在忙时，时间格要直接写“等待空闲”——不是“约 0 分钟后”，也不是
  // 一串 0 的数码管。做法：把 getState 换成一份造好的状态，等页面自己那一秒一次的
  // 轮询重画，量完立刻换回去（不动后端、不写配置）。
  await page.evaluate(async () => {
    const real = await window.api.getState();
    window.__wrRealGetState = window.api.getState;
    const at = Date.now() - 1000;
    const fake = JSON.parse(JSON.stringify(real));
    fake.phase = "idle";
    fake.paused = false;
    fake.pauseReasons = [];
    fake.pausedAt = 0;
    fake.breakEndsAt = 0;
    fake.nextType = "short";
    fake.nextName = "短休息";
    fake.nextAt = at;
    fake.idleWait = true;
    fake.rest = { name: "短休息", at: at, idleWait: true };
    fake.other = { name: "喝水提醒", at: at - 1000, idleWait: true };
    window.api.getState = async () => fake;
  });
  await delay(1400);
  const idleCells = await page.evaluate(() =>
    [...document.querySelectorAll("#data-status .status-cell")].map((c) => ({
      k: (c.querySelector(".k") || {}).textContent || "",
      v: (c.querySelector(".v") || {}).textContent || "",
      led: !!c.querySelector(".v-led"),
    })),
  );
  await page.evaluate(() => {
    window.api.getState = window.__wrRealGetState;
  });
  check(idleCells.length >= 2, "状态卡渲染出状态格", JSON.stringify(idleCells));
  const idleRest = idleCells.find((c) => c.k.indexOf("短休息") >= 0) || {};
  check(
    idleRest.v === "等待空闲" && idleRest.led === false,
    "等待空闲：下次休息那一格写文字、不画数码管",
    JSON.stringify(idleRest),
  );
  const idleOther = idleCells.find((c) => c.k.indexOf("喝水") >= 0) || {};
  check(
    idleOther.v === "等待空闲",
    "等待空闲：自定义提醒那一格同样写文字（不被“今天/明天”判据藏掉）",
    JSON.stringify(idleOther),
  );
  await delay(1100); // 让页面按真实状态再画一次，后面的断言看到的才是真状态

  // ---------- 全程无 JS 错误 ----------
  check(consoleErrors.length === 0, "六个 tab 走完没有任何 JS 报错",
    consoleErrors.slice(0, 3).join(" ; "));

  } finally {
    await browser.close();
  }
  return finish();
}

function finish() {
  console.log(`\n结果：${checks} 项断言，失败 ${failures.length} 项`);
  if (failures.length) {
    for (const f of failures) console.log("  - " + f);
    process.exitCode = 1;
  }
}

main().catch((e) => {
  console.error("[settings-ui] 探针异常:", e && e.stack || e);
  process.exitCode = 2;
});
