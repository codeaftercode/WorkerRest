/**
 * 设置页“写了什么就存什么”的功能流探针（Playwright 驱动系统 Edge）。
 *
 * 已覆盖的三层分工：
 *   · settings_ui_probe.js     —— 广度：六个 tab 的控件、可访问名、溢出、静态契约；
 *   · settings_modals_probe.js —— 对话框：打开 / 焦点 / Esc / Tab 陷阱 / 遮罩；
 *   · 本脚本                    —— **写入链路**：动一下控件 → 配置里真的变了 ——
 *     以及几条只在 JS 侧成立的判定（服务端不管）：
 *
 *   1. 快捷键录入：Ctrl+Alt+K 录进去 → settings.shortcuts 落盘；Esc 清空；
 *      裸键（F5）与重复键**整条拒收**（框里保持原值），只按修饰键不出值；
 *   2. 提示音：音量滑块 / 总开关 / 播放方式按钮（single → random → sequence）；
 *   3. 主题：切深色 → html[data-theme] 与 settings.theme 同步；
 *   4. 外卖：提前通知填超范围（999）→ 存进去被折到 300（归一化在服务端，这里验整条链路）；
 *   5. 恢复默认设置：点下去弹确认框，**取消**后配置一字未改（防误触丢配置）。
 *
 * 运行：由 probe_settings_ui.py 起好无头服务后 `node settings_flow_probe.js` 拉起。
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

// autoSave 有防抖，落盘不是同步的：读配置前等一会儿，别把“还没存”当成没生效。
const SAVE_WAIT = 900;

async function main() {
  if (!fs.existsSync(OUT)) fs.mkdirSync(OUT, { recursive: true });
  const browser = await chromium.launch({ channel: "msedge", headless: true });
  const ctx = await browser.newContext({ viewport: { width: 1000, height: 820 }, deviceScaleFactor: 1 });
  const page = await ctx.newPage();
  page.setDefaultTimeout(8000);
  try {
    const consoleErrors = [];
    page.on("pageerror", (e) => consoleErrors.push("pageerror: " + e.message));
    page.on("console", (m) => {
      if (m.type() !== "error") return;
      const t = m.text();
      if (/favicon|net::ERR_|Failed to load resource/i.test(t)) return;
      consoleErrors.push("console: " + t);
    });
    await page.addInitScript({ content: fs.readFileSync(PRELOAD, "utf-8") });
    await page.goto(BASE + "/settings.html", { waitUntil: "domcontentloaded" });
    let ready = false;
    for (let i = 0; i < 40; i++) {
      ready = await page.evaluate(() => typeof window.api === "object" && window.api !== null);
      if (ready) break;
      await delay(150);
    }
    if (!check(ready, "settings.html 装载完成且 window.api 就绪")) return;
    await delay(700);

    const cfg = async () => await page.evaluate(async () => await window.api.getSettings());
    const tab = async (id) => {
      await page.locator(`.nav-item[data-tab="${id}"]`).click();
      await delay(200);
    };

    // ---------- 零、自动暂停 · 两个投屏开关 ----------
    // 两个开关各测一遍“UI → 配置”：拆开之后最容易出的错是两行绑到同一个键上，
    // 只测其中一个的话，这种错在界面上看起来完全正常。
    // 后端一启动就会探测投屏：本机若正好在复制模式 / 接了无线屏，调度器会直接处于暂停状态。
    // F0 只验“界面 → 配置”链路，不依赖调度器状态；需要“到点必弹”的探针自己在播种时关掉两个键。
    // 注：2026-11-03 修正镜像判据后，扩展双屏不再误触发（旧判据比 sourceInfo.id，
    // 双显卡机器上两个适配器的 source 0 撞车），但播种保持不动——探针要能在任何真机上跑。
    console.log("\n[F0] 自动暂停 · 两个投屏开关：点击 → 落盘 → 复原");
    await tab("general");
    for (const [id, key, label] of [
      ["#castWirelessPause", "castWirelessPause", "无线投放"],
      ["#castMirrorPause", "castMirrorPause", "复制模式"],
    ]) {
      const other = key === "castMirrorPause" ? "castWirelessPause" : "castMirrorPause";
      const before = await page.isChecked(id);
      const otherBefore = (await cfg())[other];
      await page.click(id);
      await delay(SAVE_WAIT);
      const after = (await cfg())[key];
      const otherAfter = (await cfg())[other];
      check(after === !before, `${label}自动暂停开关翻转落盘`, `ui ${before} → cfg ${after}`);
      // 拆开之后最容易出的错是两行绑到同一个键：只测单个开关的话，界面看起来完全正常
      check(otherAfter === otherBefore, `${label}翻转不牵连另一开关`,
        `${other} ${otherBefore} → ${otherAfter}`);
      await page.click(id);
      await delay(SAVE_WAIT);
      const back = (await cfg())[key];
      check(back === before, `${label}自动暂停开关翻回来落盘`, `cfg ${back}`);
    }

    // ---------- 零·一、请勿打扰 / 全屏或演示：两条独立开关 ----------
    // 2026-11-06 从一条合并开关拆出来：最容易出的错是两行绑到同一个键、或改一条
    // 顺带把另一条也写了。每条都验“翻转落盘 + 另一条的配置与界面都不动 + 翻回复原”。
    console.log("\n[F0·1] 请勿打扰 / 全屏或演示：两条开关互不牵连");
    const pair = [
      ["#dndPause", "dndPause", "请勿打扰"],
      ["#fullscreenPause", "fullscreenPause", "全屏或演示"],
    ];
    for (const [id, key, label] of pair) {
      const before = await page.isChecked(id);
      const othersBefore = {};
      for (const [, k] of pair) {
        if (k !== key) othersBefore[k] = (await cfg())[k];
      }
      await page.click(id);
      await delay(SAVE_WAIT);
      const after = (await cfg())[key];
      check(after === !before, `${label}开关翻转落盘`, `ui ${before} → cfg ${after}`);
      for (const [otherId, k] of pair) {
        if (k === key) continue;
        check((await cfg())[k] === othersBefore[k], `${label}翻转不牵连 ${k} 的配置`,
          `${othersBefore[k]} → ${(await cfg())[k]}`);
        check(await page.isChecked(otherId) === othersBefore[k],
          `${label}翻转不牵连 ${k} 的界面勾选`, otherId);
      }
      await page.click(id);
      await delay(SAVE_WAIT);
      check((await cfg())[key] === before, `${label}开关翻回来落盘`, `cfg ${(await cfg())[key]}`);
    }

    // ---------- 零·二、显示的屏幕（多选下拉） ----------
    // 单屏机器也能测“最后一块不许取消”——那是最容易被改坏的一条保护：
    // 全不勾 = 提醒窗口一块屏都不弹，用户只会以为软件坏了。
    console.log("\n[F0·2] 显示的屏幕：多选勾选 → 落盘，最后一块不给取消");
    await page.waitForFunction(() => {
      const h = document.getElementById("ap-displays");
      return !!h && !!h.querySelector(".cdrop-btn");
    }, null, { timeout: 5000 }).catch(() => {});
    const dispN = await page.evaluate(async () => {
      const list = await window.api.listDisplays();
      return Array.isArray(list) ? list.length : 0;
    });
    if (dispN === 0) {
      console.log("  [skip] 本机枚举不到显示器（远程会话？），下拉是禁用态，跳过写入断言");
    } else {
      const dispBefore = JSON.stringify((await cfg()).displayIds || []);
      await page.click("#ap-displays .cdrop-btn");
      await delay(150);
      const opened = await page.evaluate(() => {
        const h = document.getElementById("ap-displays");
        const menu = h.querySelector(".cdrop-menu");
        return {
          hidden: menu.classList.contains("hidden"),
          opts: menu.querySelectorAll('[role="option"]').length,
        };
      });
      check(!opened.hidden, "点按钮展开多选面板", JSON.stringify(opened));
      check(opened.opts === dispN, "面板选项数 == ListDisplays 返回的屏数",
        `opts=${opened.opts} n=${dispN}`);

      // 面板底部的“刷新”：显示器列表只在页面加载时枚举一次，热插拔之后靠它补救。
      // 断言“刷新本身不写配置”——它只是重新枚举，勾选归用户。
      const refreshBefore = JSON.stringify((await cfg()).displayIds || []);
      await page.click("#ap-displays .cdrop-refresh");
      await page.waitForFunction(() => {
        const b = document.querySelector("#ap-displays .cdrop-refresh");
        return !!b && !b.classList.contains("busy");
      }, null, { timeout: 5000 }).catch(() => {});
      await delay(200);
      const afterRefresh = await page.evaluate(() => ({
        opts: document.querySelectorAll("#ap-displays [role='option']").length,
        open: !document.querySelector("#ap-displays .cdrop-menu").classList.contains("hidden"),
        note: (document.querySelector("#ap-displays .cdrop-foot-note") || {}).textContent || "",
      }));
      check(afterRefresh.opts === dispN, "点“刷新”重新枚举后选项数仍 == ListDisplays 的屏数",
        `opts=${afterRefresh.opts} n=${dispN}`);
      check(afterRefresh.open, "刷新后面板不收起（就地替换选项）", String(afterRefresh.open));
      check(!/检测到\s*\d+\s*个/.test(afterRefresh.note), "刷新后刷新行左边依旧不报屏数",
        afterRefresh.note);
      check(JSON.stringify((await cfg()).displayIds || []) === refreshBefore,
        "刷新本身不写配置（勾选归用户）", refreshBefore);

      const OPT = "#ap-displays .cdrop-menu [role='option']";
      if (dispN === 1) {
        // 只有一块屏：点它就是取消最后一块，必须被拒绝
        await page.locator(OPT).nth(0).click();
        await delay(SAVE_WAIT);
        const sel = await page.locator(OPT).nth(0).getAttribute("aria-selected");
        const keep = (await cfg()).displayIds || [];
        check(sel === "true", "只有一块屏时取消被拒绝，仍保持勾选", `selected=${sel}`);
        check(JSON.stringify(keep) === dispBefore, "被拒绝的操作没有写进配置",
          `${dispBefore} → ${JSON.stringify(keep)}`);
      } else {
        // 面板里的勾选态来自用户配置，**不能按位置猜**：出厂只有主屏被勾，但接过副屏
        // 并勾选过的机器会两块都勾着。早先这里硬点“第 1 个选项”（单屏时代写法，点它
        // 等于取消最后一块），改成“第 2 个选项”之后又栽在双屏全勾的机器上——第一步
        // 同样等于取消，后面五条断言连锁误报。两次都是同一个根因：拿位置当前置。
        // 现在先归一化成“只勾一块”，再按“已勾的第一块”keepIdx /“未勾的第一块”addIdx 定位。
        const readSel = () => page.evaluate((sel) =>
          [...document.querySelectorAll(sel)].map(o => o.getAttribute("aria-selected") === "true"), OPT);

        let states = await readSel();
        let selCount = states.filter(Boolean).length;
        check(selCount >= 1, "面板里至少有一块屏处于勾选态", JSON.stringify(states));
        let guard = 0;
        while (selCount > 1 && guard++ < dispN) {
          const dropIdx = states.findIndex(Boolean);
          await page.locator(OPT).nth(dropIdx).click();
          await delay(SAVE_WAIT);
          states = await readSel();
          check(states[dropIdx] === false, "多块都勾着时可以取消其中一块，剩下的仍生效",
            `idx=${dropIdx} → ${JSON.stringify(states)}`);
          selCount = states.filter(Boolean).length;
        }
        check(selCount === 1, "归一化后只剩一块勾选", `n=${selCount} states=${JSON.stringify(states)}`);

        const keepIdx = states.findIndex(Boolean);
        const addIdx = states.findIndex(s => !s);
        if (addIdx < 0 || keepIdx < 0) {
          check(false, "存在可勾选 / 可取消的屏供后续断言使用",
            `keep=${keepIdx} add=${addIdx} states=${JSON.stringify(states)}`);
        } else {
          // 勾上未勾的那一块
          await page.locator(OPT).nth(addIdx).click();
          await delay(SAVE_WAIT);
          const addSel = await page.locator(OPT).nth(addIdx).getAttribute("aria-selected");
          const addOpen = await page.evaluate(() => {
            const h = document.getElementById("ap-displays");
            return !h.querySelector(".cdrop-menu").classList.contains("hidden");
          });
          check(addOpen, "勾选后菜单不收起（多选要能连着勾）", `open=${addOpen}`);
          check(addSel === "true", "点未勾选的屏会把它勾上", `aria-selected=${addSel}`);
          const afterAdd = (await cfg()).displayIds || [];
          check(afterAdd.length === 2, "勾第二块后落盘成两块的具体 ID 快照",
            JSON.stringify(afterAdd));

          // 取消一块：点已勾的那块，这时还剩一块，必须允许
          await page.locator(OPT).nth(keepIdx).click();
          await delay(SAVE_WAIT);
          const delSel = await page.locator(OPT).nth(keepIdx).getAttribute("aria-selected");
          const afterDel = (await cfg()).displayIds || [];
          check(delSel === "false", "两块以上时可以取消某一块", `aria-selected=${delSel}`);
          check(afterDel.length === 1, "取消一块后配置只剩另一块", JSON.stringify(afterDel));

          // 复原：再点一次勾回来 → 勾满时写的是具体 ID 快照，不是空数组
          await page.locator(OPT).nth(keepIdx).click();
          await delay(SAVE_WAIT);
          const restored = (await cfg()).displayIds || [];
          check(restored.length === 2,
            "勾满后写具体 ID 快照（不是空数组，插新屏不会被自动带上）", JSON.stringify(restored));

          // 最后一块不许取消：先取消掉 keepIdx，只剩 addIdx 那块，再点它
          await page.locator(OPT).nth(keepIdx).click();
          await delay(SAVE_WAIT);
          await page.locator(OPT).nth(addIdx).click();
          await delay(SAVE_WAIT);
          const lastSel = await page.locator(OPT).nth(addIdx).getAttribute("aria-selected");
          const afterLast = (await cfg()).displayIds || [];
          check(lastSel === "true" && afterLast.length === 1,
            "最后一块不允许取消（全不勾 = 一个窗都不弹）", `selected=${lastSel} n=${afterLast.length}`);
          // 收尾：把 keepIdx 勾回去，尽量让结束态贴近运行前。**不作断言**——隔离配置
          // 每轮都从真实配置重拷，结束态不影响下一次运行，也不碰用户数据。
          // （产品路径已单独验证：勾选后立刻 Esc 关面板，300ms 内照样落盘。）
          await page.locator(OPT).nth(keepIdx).click();
          await delay(SAVE_WAIT);
        }
      }
      await page.keyboard.press("Escape");
    }

    // ---------- 零·二b、刷新能反映热插拔（桩模拟，不依赖真拔线） ----------
    // 真机拔线没法自动化，所以把 listDisplays 换成一个“多一块假屏 / 少一块真屏”的桩，
    // 钉住四条口径：
    //   ① 点“刷新”后选项跟着变；
    //   ② 新出现的屏默认**未勾选**（快照语义，插新屏不该被自动带上）；
    //   ③ 被拔掉的屏从面板消失，但**配置里仍然保留**（接回去还在）；
    //   ④ 刷新本身不写配置。
    if (dispN === 0) {
      console.log("  [skip] 本机枚举不到显示器，热插拔桩模拟跳过");
    } else {
      console.log("\n[F0·2b] 点“刷新”能反映热插拔（桩模拟）");
      const fake = { id: "__fake_display__", label: "FAKE 测试屏", width: 1024, height: 768,
                     x: 9999, y: 9999, isPrimary: false };
      const cfgBeforeHotplug = JSON.stringify((await cfg()).displayIds || []);
      const waitRefresh = async () => {
        await page.waitForFunction(() => {
          const b = document.querySelector("#ap-displays .cdrop-refresh");
          return !!b && !b.classList.contains("busy");
        }, null, { timeout: 5000 }).catch(() => {});
        await delay(200);
      };

      // ① 多一块假屏
      await page.evaluate(async (f) => {
        const real = await window.api.listDisplays();
        window.__wrRealListDisplays = window.api.listDisplays;
        window.api.listDisplays = async () => real.concat([f]);
      }, fake);
      await page.click("#ap-displays .cdrop-btn");
      await delay(150);
      await page.click("#ap-displays .cdrop-refresh");
      await waitRefresh();
      const added = await page.evaluate(() => {
        const opt = document.querySelector("#ap-displays [data-value='__fake_display__']");
        return {
          opts: document.querySelectorAll("#ap-displays [role='option']").length,
          present: !!opt,
          selected: opt ? opt.getAttribute("aria-selected") : null,
          note: (document.querySelector("#ap-displays .cdrop-foot-note") || {}).textContent || "",
        };
      });
      check(added.present, "刷新后新接入的屏出现在选项里", JSON.stringify(added));
      check(added.opts === dispN + 1, "选项数 +1", `opts=${added.opts} n=${dispN}`);
      check(added.selected === "false", "新接入的屏默认未勾选（插新屏不被自动带上）",
        `aria-selected=${added.selected}`);
      check(!/检测到\s*\d+\s*个/.test(added.note), "新屏接上后刷新行左边仍不报屏数", added.note);
      check(JSON.stringify((await cfg()).displayIds || []) === cfgBeforeHotplug,
        "刷新本身不写配置", cfgBeforeHotplug);

      // ② 拔掉一块“配置里勾着”的屏（配置为空时没有可拔的对象，跳过这一段）
      //
      // 远程桌面 / 无头会话里唯一那块屏的 ID 是会话级的（DEFAULT_MONITOR#… / pos:x,y），
      // 按规则不进历史目录 —— “拔掉后落到历史栏”在这块屏上根本无从发生。这时改用刷新桩
      // 造出来的假屏（__fake_display__，非会话级）：先把它补进勾选，再从枚举里拔掉，
      // 验证的仍是同一条链路（记住过 → 历史栏 → 只读 → 删除）。
      let hotplugBaseline = cfgBeforeHotplug;
      const cfgIds = JSON.parse(cfgBeforeHotplug);
      const sessionScoped = (id) => /^pos:/i.test(id) || /^DEFAULT_MONITOR#/i.test(id);
      // “拔屏”的前提是这块屏此刻确实在枚举结果里。隔离配置从别处拷来、或换了接口、
      // 重装驱动后 ID 变了时，勾着的 ID 可能一块都不在当前列表里 —— 按 ID 过滤删不到
      // 任何东西，opts-1 的期望根本不成立。这属于场景不适用，不是断言失败。
      const listedIds = await page.evaluate(async () =>
        (await window.__wrRealListDisplays()).map((d) => d.id));
      let ticked = cfgIds.find((id) => listedIds.includes(id) && !sessionScoped(id));
      let expectedOpts = dispN - 1;
      if (!ticked) {
        ticked = "__fake_display__";
        expectedOpts = dispN;
        await page.evaluate(async (id) => {
          const s = await window.api.getSettings();
          if (!(s.displayIds || []).includes(id)) {
            s.displayIds = (s.displayIds || []).concat([id]);
          }
          // 桩只替换前端的枚举结果，不经过 Go 侧的“记住这次枚举”，所以历史目录得手写一条：
          // 拔掉之后“这条屏以前见过”才有据可依。
          if (!(s.displayKnown || []).some((k) => k.id === id)) {
            s.displayKnown = (s.displayKnown || []).concat([{
              id: id, label: "FAKE 测试屏", width: 1024, height: 768,
              lastSeen: "2026-11-01T09:00:00+08:00",
            }]);
          }
          await window.api.saveSettings(s);
        }, ticked);
        // 面板的勾选基准是打开那一刻的配置快照，改完盘必须重新加载才看得见
        await page.reload({ waitUntil: "networkidle" });
        await delay(1600);
        await page.evaluate(() => { window.__wrRealListDisplays = window.api.listDisplays; });
        await page.click("#ap-displays .cdrop-btn");
        await delay(250);
        hotplugBaseline = JSON.stringify((await cfg()).displayIds || []);
        console.log("  [桩] 勾着的屏不在枚举里或是会话级虚拟屏，改用假屏验证“拔掉→历史栏”");
      }
      {
      if (ticked === "__fake_display__") {
        // 先让假屏“接上”：桩返回 真实列表 + 假屏 → 刷新后它是已连接项、勾着、不在历史栏，
        // 再拔掉才谈得上“落到历史栏”。
        await page.evaluate(async (f) => {
          const all = await window.__wrRealListDisplays();
          window.api.listDisplays = async () => all.concat([f]);
        }, fake);
        await page.click("#ap-displays .cdrop-refresh");
        await waitRefresh();
        const attached = await page.evaluate((id) => {
          const opt = document.querySelector(`#ap-displays .cdrop-opts [data-value='${id}']`);
          return {
            inOpts: !!opt,
            sel: opt ? opt.getAttribute("aria-selected") : null,
            stale: !!document.querySelector(`#ap-displays .cdrop-stale[data-value='${id}']`),
          };
        }, ticked);
        check(attached.inOpts && attached.sel === "true" && !attached.stale,
          "假屏接上后是已连接项（勾着、不在历史栏）", JSON.stringify(attached));
      }
      await page.evaluate(async (drop) => {
        const all = await window.__wrRealListDisplays();
        window.api.listDisplays = async () => all.filter((d) => d.id !== drop);
      }, ticked);
      await page.click("#ap-displays .cdrop-refresh");
      await waitRefresh();
      const removed = await page.evaluate((drop) => {
        const row = document.querySelector(`#ap-displays .cdrop-stale[data-value='${drop}']`);
        return {
          opts: document.querySelectorAll("#ap-displays [role='option']").length,
          inOpts: !!document.querySelector(`#ap-displays .cdrop-opts [data-value='${drop}']`),
          stale: row
            ? {
                role: row.getAttribute("role"),
                sel: row.getAttribute("aria-selected"),
                del: !!row.querySelector(".cdrop-del"),
                name: (row.querySelector(".cdrop-label") || {}).textContent || "",
              }
            : null,
          note: (document.querySelector("#ap-displays .cdrop-foot-note") || {}).textContent || "",
        };
      }, ticked);
      check(!removed.inOpts, "被拔掉的屏从“已连接”选项里消失", JSON.stringify(removed));
      check(removed.opts === expectedOpts, "已连接选项数按桩减少",
        `opts=${removed.opts} want=${expectedOpts}`);
      check(!!removed.stale, "被拔掉的屏落到下面的历史栏（不再凭空消失）", JSON.stringify(removed.stale));
      check(removed.stale && removed.stale.role === "group",
        "历史行不是可选项（点不动，只能删）", removed.stale && removed.stale.role);
      check(removed.stale && removed.stale.sel === "true",
        "配置里勾着的历史屏仍显示勾选", removed.stale && removed.stale.sel);
      check(removed.stale && removed.stale.del, "历史行右侧有删除按钮");
      // 只剩一块屏的机器上“拔掉唯一一块”会拔到 0 个选项：此时列表全空，底部该给的是
      // 可重试的“点刷新重新检测”。还有已连接项时，刷新行左边什么都不写 —— 被拔掉的屏
      // 由下面的灰色分组承担说明，不再数一遍。按剩余数量分支断言，两种机器都成立。
      check(
        removed.opts === 0 ? removed.note.includes("刷新") : !/检测到\s*\d+\s*个/.test(removed.note),
        removed.opts === 0
          ? "拔到 0 块时底部提示改为“点刷新重新检测”"
          : "刷新行左边不再报屏数",
        removed.note,
      );
      const cfgAfterRemove = JSON.parse(JSON.stringify((await cfg()).displayIds || []));
      check(cfgAfterRemove.includes(ticked),
        "被拔掉的屏仍保留在配置里（接回去还在，面板上看不见就删不掉）",
        JSON.stringify(cfgAfterRemove));
      }

      // ③ 恢复真实枚举：选项回到原样，配置依旧不动
      await page.evaluate(() => {
        window.api.listDisplays = window.__wrRealListDisplays;
        delete window.__wrRealListDisplays;
      });
      await page.click("#ap-displays .cdrop-refresh");
      await waitRefresh();
      const backToReality = await page.evaluate(() => ({
        opts: document.querySelectorAll("#ap-displays [role='option']").length,
        fake: !!document.querySelector("#ap-displays .cdrop-opts [data-value='__fake_display__']"),
        fakeStale: !!document.querySelector("#ap-displays .cdrop-stale[data-value='__fake_display__']"),
      }));
      check(backToReality.opts === dispN, "换回真实枚举后选项数复原",
        `opts=${backToReality.opts} n=${dispN}`);
      // 假屏只在前端桩里存在过。走过假屏分支时它被手写进了历史目录（模拟“以前见过”），
      // 换回真实枚举后应当退回历史栏，而不是从面板上凭空消失；否则整个 `[data-value]`
      // 都不该命中它。
      check(!backToReality.fake && (ticked === "__fake_display__"
          ? backToReality.fakeStale
          : !backToReality.fakeStale),
        ticked === "__fake_display__"
          ? "换回真实枚举后假屏退回历史栏（它确实“以前见过”）"
          : "桩造出来的假屏已消失",
        `opt=${backToReality.fake} stale=${backToReality.fakeStale}`);
      check(JSON.stringify((await cfg()).displayIds || []) === hotplugBaseline,
        "整轮刷新下来配置一字未改", hotplugBaseline);
      await page.keyboard.press("Escape");
      await delay(120);

      // ---------- 零·二c、历史显示器：排在已连接项后面、置灰只读、可删除 ----------
      // 真机没法凭空多一块屏，所以往配置里塞一条“幽灵”目录（同时加进勾选，验证
      // “勾过的那块要显示为已勾选”）。写完必须**重新加载页面**：面板的勾选基准是
      // 打开页面那一刻的配置快照，绕过它直接改盘，面板当然看不见。
      console.log("\n[F0·2c] 历史显示器：置灰、只读、可删除");
      const ghost = {
        id: "__ghost_display__", label: "GHOST 测试屏", width: 1920, height: 1200,
        lastSeen: "2026-11-01T10:00:00+08:00",
      };
      const idsBeforeGhost = JSON.parse(JSON.stringify((await cfg()).displayIds || []));
      await page.evaluate(async (g) => {
        const s = await window.api.getSettings();
        s.displayKnown = (s.displayKnown || []).concat([g]);
        s.displayIds = (s.displayIds || []).concat([g.id]);
        await window.api.saveSettings(s);
      }, ghost);
      await page.reload({ waitUntil: "networkidle" });
      await delay(1600);
      await page.click("#ap-displays .cdrop-btn");
      await delay(300);
      const ghostView = await page.evaluate(() => {
        const menu = document.querySelector("#ap-displays .cdrop-menu");
        const row = menu.querySelector(".cdrop-stale[data-value='__ghost_display__']");
        const opts = [...menu.querySelectorAll(".cdrop-opts [role='option']")];
        const det = opts[0] ? opts[0].querySelector(".cdrop-label") : null;
        const order = [...menu.querySelectorAll("[role='option'], .cdrop-stale")]
          .map((e) => (e.classList.contains("cdrop-stale") ? "stale" : "opt"));
        const firstStale = order.indexOf("stale");
        return {
          exists: !!row,
          order,
          orderOk: firstStale > 0 && order.slice(firstStale).every((x) => x === "stale"),
          role: row && row.getAttribute("role"),
          sel: row && row.getAttribute("aria-selected"),
          del: !!(row && row.querySelector(".cdrop-del")),
          aria: row && row.getAttribute("aria-label"),
          group: (menu.querySelector(".cdrop-group") || {}).textContent || "",
          staleColor: row ? getComputedStyle(row.querySelector(".cdrop-label")).color : "",
          optColor: det ? getComputedStyle(det).color : "",
          note: document.querySelector("#ap-displays .cdrop-foot-note").textContent,
        };
      });
      check(ghostView.exists, "历史显示器出现在面板里（曾经检测到就算）", JSON.stringify(ghostView.order));
      check(ghostView.orderOk, "已连接的排在前面、历史的排在后面", ghostView.order.join(","));
      check(ghostView.group.includes("历史记录"), "历史栏有分组说明文字", ghostView.group);
      check(ghostView.role === "group", "历史行是 role=group（不是可选项）", ghostView.role);
      check(ghostView.sel === "true", "勾过的那块历史屏显示为已勾选", ghostView.sel);
      check(ghostView.del, "历史行右侧给出“删除”按钮");
      check(ghostView.staleColor !== ghostView.optColor,
        "历史行文字置灰（与已连接项不同色）", `${ghostView.staleColor} vs ${ghostView.optColor}`);
      check(/未连接/.test(ghostView.aria || "") && /已勾选/.test(ghostView.aria || ""),
        "历史行的读屏文案说清“未连接 / 勾没勾”", ghostView.aria);
      // 枚举不到屏时（远程会话 / 无桌面）底部那句“点刷新重新检测”是兜底，不算文案堆砌
      check(!/检测到\s*\d+\s*个/.test(ghostView.note),
        "有历史项时刷新行左边不堆文案", ghostView.note);

      // 复选框只读：点行（不是删除）不改勾选
      const idsBeforeClick = JSON.stringify((await cfg()).displayIds || []);
      await page.click("#ap-displays .cdrop-stale[data-value='__ghost_display__'] .cdrop-label");
      await delay(SAVE_WAIT);
      check(JSON.stringify((await cfg()).displayIds || []) === idsBeforeClick,
        "点灰色行不改变勾选（复选框只读）", idsBeforeClick);

      // 删除要先过确认框：取消不动配置，确认才真删
      const delSel = "#ap-displays .cdrop-stale[data-value='__ghost_display__'] .cdrop-del";
      await page.locator(delSel).click();
      await delay(250);
      const promptShown = await page.evaluate(() => {
        const box = document.getElementById("confirm-modal");
        return {
          open: !box.classList.contains("hidden"),
          title: (document.getElementById("cf-title") || {}).textContent || "",
          ok: (document.getElementById("cf-ok") || {}).textContent || "",
        };
      });
      check(promptShown.open, "点“删除”先弹确认框", JSON.stringify(promptShown));
      check(/删除/.test(promptShown.title + promptShown.ok), "确认框文案说的是“删除”", promptShown.title);

      await page.click("#cf-cancel");
      await delay(300);
      const afterCancel = await page.evaluate(() => ({
        still: !!document.querySelector("#ap-displays .cdrop-stale[data-value='__ghost_display__']"),
        menuOpen: !document.querySelector("#ap-displays .cdrop-menu").classList.contains("hidden"),
      }));
      check(afterCancel.still, "取消后那条历史记录还在");
      check(afterCancel.menuOpen, "取消后面板没被确认框顶掉", String(afterCancel.menuOpen));
      check(JSON.stringify((await cfg()).displayIds || []) === idsBeforeClick,
        "取消不动配置", idsBeforeClick);

      await page.locator(delSel).click();
      await delay(250);
      await page.click("#cf-ok");
      await delay(SAVE_WAIT);
      const goneView = await page.evaluate(() => ({
        gone: !document.querySelector("#ap-displays .cdrop-stale[data-value='__ghost_display__']"),
        open: !document.querySelector("#ap-displays .cdrop-menu").classList.contains("hidden"),
      }));
      const cfgAfterGhost = JSON.parse(JSON.stringify(await cfg()));
      check(goneView.gone, "删除后历史行消失");
      check(goneView.open, "删除后面板不收起（可以连着删）", String(goneView.open));
      check(!(cfgAfterGhost.displayKnown || []).some((k) => k.id === "__ghost_display__"),
        "删除后历史目录里不留它", JSON.stringify((cfgAfterGhost.displayKnown || []).map((k) => k.id)));
      const idsAfterGhost = cfgAfterGhost.displayIds || [];
      check(!idsAfterGhost.includes("__ghost_display__"), "删除后勾选里不留它", JSON.stringify(idsAfterGhost));
      if (idsBeforeGhost.length) {
        check(idsBeforeGhost.every((id) => idsAfterGhost.includes(id)),
          "其余真实屏的勾选不受影响", JSON.stringify(idsAfterGhost));
      }
      // 面板只往下展开：条目一多（接了屏 + 一串历史屏）就会掉出窗口下沿。
      // 打开时按剩余高度限高、菜单自己滚，底部“刷新”钉在面板底 —— 否则要滚到底才够得着。
      const fit = await page.evaluate(() => {
        const m = document.querySelector("#ap-displays .cdrop-menu");
        const f = document.querySelector("#ap-displays .cdrop-foot");
        const mr = m.getBoundingClientRect();
        const fr = f.getBoundingClientRect();
        return {
          maxH: m.style.maxHeight || "",
          bottom: Math.round(mr.bottom), vh: window.innerHeight,
          scrollable: m.scrollHeight > m.clientHeight + 1,
          footInside: fr.bottom <= mr.bottom + 1 && fr.top >= mr.top - 1,
        };
      });
      check(fit.bottom <= fit.vh + 1, "面板不越出窗口下沿", JSON.stringify(fit));
      check(fit.footInside, "“刷新”钉在面板底部（限高滚动时也够得着）", JSON.stringify(fit));
      await page.keyboard.press("Escape");
      await delay(120);

      // ---------- 零·二d、换会话后勾选按“数量”沿用（远程 ↔ 真机） ----------
      // 真机没法切会话，所以直接模拟“配置里躺着两枚当前枚举不到的 ID”。面板必须按
      // “勾了几块”把它们映射到当前屏上，而不是只留主屏 —— 否则用户在两块屏上勾的两块，
      // 切一次会话就静默退化成一块（2026-11-08 用户问的场景）。
      console.log("\n[F0·2d] 换会话后勾选按数量沿用");
      const idsBeforeForeign = JSON.parse(JSON.stringify((await cfg()).displayIds || []));
      const foreign = ["OTHER_SESSION#1", "OTHER_SESSION#2"];
      await page.evaluate(async (ids) => {
        const s = await window.api.getSettings();
        s.displayIds = ids.slice();
        s.displayKnown = (s.displayKnown || []).concat(
          ids.filter((id) => !(s.displayKnown || []).some((k) => k.id === id))
            .map((id) => ({ id: id, label: "OTHER 会话屏", width: 1920, height: 1080,
                            lastSeen: "2026-10-01T09:00:00+08:00" })));
        await window.api.saveSettings(s);
      }, foreign);
      await page.reload({ waitUntil: "networkidle" });
      await delay(1600);
      await page.click("#ap-displays .cdrop-btn");
      await delay(250);
      const carried = await page.evaluate(() => {
        const opts = [...document.querySelectorAll("#ap-displays [role='option']")];
        const host = document.getElementById("ap-displays");
        return {
          opts: opts.length,
          on: opts.filter((o) => o.getAttribute("aria-selected") === "true").length,
          value: JSON.parse(host.dataset.value || "[]"),
        };
      });
      const wantOn = Math.min(2, carried.opts);
      check(carried.on === wantOn, "勾选全失配时按“勾了几块”沿用到当前屏",
        JSON.stringify(carried) + ` want=${wantOn}`);
      check(foreign.every((id) => carried.value.includes(id)),
        "沿用不改写配置（勾选仍是那套快照）", JSON.stringify(carried.value));
      await page.keyboard.press("Escape");
      await delay(120);
      // 恢复，别把“别的会话”留给后面的段落
      await page.evaluate(async (ids) => {
        const s = await window.api.getSettings();
        s.displayIds = ids.slice();
        await window.api.saveSettings(s);
      }, idsBeforeForeign);
    }

    // ---------- 一、快捷键录入 ----------
    console.log("\n[F1] 快捷键录入：合法组合写进去、裸键与重复键整条拒收");
    await tab("shortcuts");
    const before = (await cfg()).shortcuts || {};

    // 1) 合法组合：Ctrl+Alt+K → sc-pause
    await page.locator("#sc-pause").focus();
    await page.keyboard.press("Control+Alt+KeyK");
    await delay(150);
    const v1 = await page.inputValue("#sc-pause");
    check(v1 === "Ctrl+Alt+K", "Ctrl+Alt+K 录入到“暂停 / 恢复休息”", v1);
    await delay(SAVE_WAIT);
    const c1 = (await cfg()).shortcuts || {};
    check(c1.pause === "Ctrl+Alt+K", "快捷键 pause 已落盘", c1.pause);

    // 2) Esc 清空 = 禁用该热键
    await page.locator("#sc-pause").focus();
    await page.keyboard.press("Escape");
    await delay(150);
    const v2 = await page.inputValue("#sc-pause");
    check(v2 === "", "Esc 清空录入框（= 禁用这个热键）", JSON.stringify(v2));
    await delay(SAVE_WAIT);
    const c2 = (await cfg()).shortcuts || {};
    check(c2.pause === "", "清空后 shortcuts.pause 落盘为空串", JSON.stringify(c2.pause));

    // 3) 裸键 F5：必须整条拒收，框里保持原值
    const keep = await page.inputValue("#sc-postpone");
    await page.locator("#sc-postpone").focus();
    await page.keyboard.press("F5");
    await delay(200);
    const v3 = await page.inputValue("#sc-postpone");
    check(v3 === keep, "裸键 F5 被拒收，录入框保持原值", `${JSON.stringify(keep)} → ${JSON.stringify(v3)}`);

    // 4) 重复键，两个方向都要测（判定是“只在靠后那一栏标红/拒收”，见 shortcutIssue）：
    //    a. 往**靠后**的栏（跳转下一次休息）录一个和前面相同的键 → 整条拒收、保持原值；
    //    b. 往**靠前**的栏（推迟）录一个和后面相同的键 → 写入，但后面那栏被标红并给出说明。
    //    配置里写的是 Ctrl / Super，Playwright 的键名是 Control / Meta，先映射
    const PW_KEY = { Ctrl: "Control", Alt: "Alt", Shift: "Shift", Super: "Meta" };
    const pressCombo = async (sel, combo) => {
      const parts = combo.split("+");
      const mods = parts.slice(0, -1).map((k) => PW_KEY[k] || k);
      const main = parts.slice(-1)[0];
      await page.locator(sel).focus();
      for (const k of mods) await page.keyboard.down(k);
      await page.keyboard.press(main);
      for (const k of mods.slice().reverse()) await page.keyboard.up(k);
      await delay(220);
    };
    const postponeKey = await page.inputValue("#sc-postpone");
    const nextKey = await page.inputValue("#sc-nextBreak");
    if (!nextKey || !postponeKey) {
      check(false, "预备：postpone 与 nextBreak 都有值（重复键用例需要）");
    } else {
      // a. 靠后的栏录成和靠前相同的键 → 拒收
      await pressCombo("#sc-nextBreak", postponeKey);
      const vNext = await page.inputValue("#sc-nextBreak");
      check(vNext === nextKey, `靠后的“跳转下一次休息”录成 ${postponeKey}（与推迟撞车）被拒收`,
        `${JSON.stringify(nextKey)} → ${JSON.stringify(vNext)}`);

      // b. 靠前的栏录成和靠后相同的键 → 接受，但后面那栏标红 + 出现汇总说明
      await pressCombo("#sc-postpone", nextKey);
      const vPost = await page.inputValue("#sc-postpone");
      check(vPost === nextKey, `靠前的“推迟”录成 ${nextKey} 会被接受（判定归到靠后那一栏）`, vPost);
      const mark = await page.evaluate((otherId) => {
        const el = document.getElementById(otherId);
        const warn = document.getElementById("sc-warn");
        return {
          bad: el.classList.contains("bad"),
          title: el.title || "",
          warnHidden: warn ? warn.classList.contains("hidden") : true,
          warnText: warn ? (warn.textContent || "").trim() : "",
        };
      }, "sc-nextBreak");
      check(mark.bad, "重复键把靠后那一栏标红（.bad）", `title=${mark.title}`);
      check(!mark.warnHidden && /同一个组合键/.test(mark.warnText), "页面给出“和某某用了同一个组合键”的说明",
        mark.warnText.slice(0, 40));

      // 复原：把推迟写回原键，红框与说明都应消失
      await pressCombo("#sc-postpone", postponeKey);
      const cleared = await page.evaluate(() => {
        const el = document.getElementById("sc-nextBreak");
        const warn = document.getElementById("sc-warn");
        return { bad: el.classList.contains("bad"), warnHidden: warn ? warn.classList.contains("hidden") : true };
      });
      check(!cleared.bad && cleared.warnHidden, "改回不重复的键后红框与说明都消失",
        `bad=${cleared.bad} warnHidden=${cleared.warnHidden}`);
    }

    // 5) 只按住修饰键不出值
    await page.locator("#sc-endEarly").focus();
    const prevEnd = await page.inputValue("#sc-endEarly");
    await page.keyboard.down("Control");
    await page.keyboard.down("Alt");
    await delay(120);
    const during = await page.inputValue("#sc-endEarly");
    await page.keyboard.up("Alt");
    await page.keyboard.up("Control");
    check(during === prevEnd, "只按住修饰键不产生组合键值", `${JSON.stringify(prevEnd)} → ${JSON.stringify(during)}`);

    // 6) 回填一致性：界面上的四个键值 == settings.shortcuts
    const nowSc = (await cfg()).shortcuts || {};
    const ui = {};
    for (const [id, key] of [["sc-postpone", "postpone"], ["sc-endEarly", "endEarly"],
                             ["sc-pause", "pause"], ["sc-nextBreak", "nextBreak"]]) {
      ui[key] = await page.inputValue("#" + id);
    }
    const same = Object.keys(ui).every((k) => String(ui[k]) === String(nowSc[k] ?? ""));
    check(same, "四个快捷键输入框的值与 settings.shortcuts 逐项一致", JSON.stringify(ui));

    // ---------- 二、提示音 ----------
    console.log("\n[F2] 提示音：音量 / 总开关 / 播放方式");
    await tab("general");
    const sndBefore = (await cfg()).sound || {};

    // 音量：取滑块自己刻度的另一个端点
    const volSet = await page.locator("#snd-volume").evaluate((el) => {
      const min = Number(el.min || 0);
      const max = Number(el.max || 1);
      const target = Number(el.value) > (min + max) / 2 ? min + 0.05 : max - 0.05;
      el.value = String(target);
      el.dispatchEvent(new Event("input", { bubbles: true }));
      el.dispatchEvent(new Event("change", { bubbles: true }));
      return target;
    });
    await delay(SAVE_WAIT);
    const snd1 = (await cfg()).sound || {};
    check(Math.abs(Number(snd1.volume) - volSet) < 0.011, "音量滑块改动落盘 = 滑块当前值",
      `ui=${volSet} cfg=${snd1.volume}`);
    const volLabel = ((await page.textContent("#snd-volume-val")) || "").trim();
    check(volLabel !== "", "音量数值标签有文字", volLabel);

    // 总开关：关掉 → 配置 false → 打开复原
    const enBefore = await page.isChecked("#snd-enabled");
    await page.click("#snd-enabled");
    await delay(SAVE_WAIT);
    const snd2 = (await cfg()).sound || {};
    check(snd2.enabled === !enBefore, "提示音总开关翻转落盘", `ui ${enBefore} → cfg ${snd2.enabled}`);
    await page.click("#snd-enabled");
    await delay(SAVE_WAIT);
    const snd3 = (await cfg()).sound || {};
    check(snd3.enabled === enBefore, "提示音总开关翻回来落盘", `cfg ${snd3.enabled}`);

    // 播放方式按钮：点一次切一个值，三次回到原点（合法集合 single/random/sequence）
    const modes = [];
    for (let i = 0; i < 3; i++) {
      await page.click("#snd-mode");
      await delay(SAVE_WAIT);
      modes.push(((await cfg()).sound || {}).mode);
    }
    const allowed = ["single", "random", "sequence"];
    const allLegal = modes.every((m) => allowed.includes(m));
    check(allLegal, "播放方式每次点击都落在合法集合内", modes.join(" → "));
    check(new Set(modes).size === 3, "连点三次覆盖三种播放方式（不是原地打转）", modes.join(" → "));
    check(String(modes[2]) === String(sndBefore.mode), "点满一圈回到起始播放方式",
      `${sndBefore.mode} → ${modes.join(" → ")}`);

    // ---------- 三、主题 ----------
    console.log("\n[F3] 主题切换：单选按钮 → html[data-theme] + settings.theme");
    const themeBefore = (await cfg()).theme;
    const target = themeBefore === "dark" ? "light" : "dark";
    await page.locator(`input[name="theme"][value="${target}"]`).check();
    await delay(SAVE_WAIT);
    const dt = await page.evaluate(() => document.documentElement.dataset.theme);
    const themeAfter = (await cfg()).theme;
    check(themeAfter === target, `设置里切到 ${target} 后配置跟着变`, `cfg=${themeAfter}`);
    check(dt === target, `html[data-theme] == ${target}（样式真的换了，不只是存了个值）`, `data-theme=${dt}`);
    await page.locator(`input[name="theme"][value="${themeBefore}"]`).check();
    await delay(SAVE_WAIT);
    check((await cfg()).theme === themeBefore, "再切回原主题", `cfg=${(await cfg()).theme}`);

    // ---------- 四、外卖提前通知：超范围要被折回 ----------
    console.log("\n[F4] 外卖提前通知填 999 秒 → 存进去折到 300");
    await tab("timing");
    // 外卖卡片可能收着：先展开
    const tkCard = page.locator("#tk-card");
    const tkLead = page.locator("#tk-lead");
    if (!(await tkLead.isVisible())) {
      const head = tkCard.locator(".card-head, summary, .fold-head").first();
      if (await head.count()) {
        await head.click();
        await delay(220);
      }
    }
    if (await tkLead.isVisible()) {
      await tkLead.fill("999");
      await tkLead.dispatchEvent("change");
      await delay(SAVE_WAIT);
      const lead = ((await cfg()).takeout || {}).leadSec;
      check(Number(lead) === 300, "leadSec=999 落盘时折到 300", lead);
      await tkLead.fill("10");
      await tkLead.dispatchEvent("change");
      await delay(SAVE_WAIT);
    } else {
      check(false, "找不到可用的“外卖提前通知”输入框（#tk-lead）");
    }

    // ---------- 五、恢复默认设置：取消后一字未改 ----------
    console.log("\n[F5] 恢复默认设置：取消 → 配置一字未改");
    await tab("about");
    const snap = JSON.stringify(await cfg());
    const reset = page.locator("#d-reset");
    if (await reset.count()) {
      await reset.click();
      await delay(320);
      const opened = await page.evaluate(() => {
        const m = document.querySelectorAll(".modal");
        for (const el of m) if (!el.classList.contains("hidden")) return el.id;
        return "";
      });
      check(opened === "confirm-modal", "点“恢复默认设置”弹出确认框", opened || "（没弹）");
      if (opened === "confirm-modal") {
        await page.click("#cf-cancel");
        await delay(400);
      }
      const after = JSON.stringify(await cfg());
      check(after === snap, "取消后配置没有任何字段被改（快照逐字相同）",
        after === snap ? "相同" : "有差异");
    } else {
      check(false, "关于页有“恢复默认设置”按钮（#d-reset）");
    }

    // ---------- 六、全程无 JS 错误 ----------
    check(consoleErrors.length === 0, "全流程没有任何 JS 报错", consoleErrors.slice(0, 3).join(" ; "));
    await page.screenshot({ path: path.join(OUT, "flow_last.png") });
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
  console.error("[settings-flow] 探针异常:", (e && e.stack) || e);
  process.exitCode = 2;
});
