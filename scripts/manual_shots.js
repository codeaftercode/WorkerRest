/**
 * 操作手册截图脚本：用 Playwright 驱动系统 Edge，把真实 web/dist 页面指向
 * 无头服务，截出手册需要的各张图（含深色主题、展开面板、确认框、自定义提醒卡、
 * 休息窗口蓄能容器、外卖优惠券窗等当前真实界面）。
 *
 * 与 renderer_probe.js 同源的 open()/shot() 思路；本文件只负责截图，不做断言。
 * 由 manual_shots.py 启动无头服务后调用（WR_BASE / WR_OUT 由它注入）。
 */
const { chromium } = require("playwright");
const path = require("path");
const fs = require("fs");

const BASE = process.env.WR_BASE || "http://127.0.0.1:29731";
const OUT = process.env.WR_OUT || path.join(process.env.TEMP || ".", "wr_manual_shots");
const delay = (ms) => new Promise((r) => setTimeout(r, ms));
const PRELOAD = path.join(__dirname, "probe_preload.js");

async function open(browser, url, w, h, opts) {
  const o = opts || {};
  const page = await browser.newPage({ viewport: { width: w, height: h } });
  if (o.preload) {
    const src = fs.readFileSync(o.preload, "utf-8");
    await page.addInitScript(src);
  }
  await page.route("**/favicon.ico", (route) => route.fulfill({ status: 204, body: "" }));
  await page.goto(url, { waitUntil: "domcontentloaded" });
  await delay(o.wait || 1400);
  return page;
}
// WR_ONLY=m_about,m_stats 只重拍这几张：改了一处界面时不必把 16 张全部重拍，
// 但**流程仍然完整跑一遍**（页面状态是逐步累积出来的，跳过步骤会拍出错误的状态），
// 只是不落盘别的图。build_manual.py 用的仍是目录里整批 16 张。
const ONLY = (process.env.WR_ONLY || "").split(",").map((s) => s.trim()).filter(Boolean);

async function shot(page, name) {
  if (ONLY.length && !ONLY.includes(name)) return;
  await page.screenshot({ path: path.join(OUT, name + ".png") });
  console.log("[shot] " + name);
}

async function run() {
  fs.mkdirSync(OUT, { recursive: true });
  const browser = await chromium.launch({
    channel: "msedge",
    headless: true,
    args: ["--disable-gpu", "--no-sandbox"],
  });

  // ============ 0. 设置页（主界面） ============
  const set = await open(browser, BASE + "/settings.html", 1000, 760, { preload: PRELOAD });

  // 0a. 浅色 · 通用配置（默认页）
  await shot(set, "m_settings_general_light");

  // 0b. 切深色主题再截一次
  await set.evaluate(`(function(){var r=document.querySelector('#content input[name=theme][value=dark]');if(r){r.checked=true;r.dispatchEvent(new Event('change',{bubbles:true}));} })()`);
  await delay(700);
  await shot(set, "m_settings_general_dark");
  // 切回浅色（后续页签在浅色下截）
  await set.evaluate(`(function(){var r=document.querySelector('#content input[name=theme][value=light]');if(r){r.checked=true;r.dispatchEvent(new Event('change',{bubbles:true}));} })()`);
  await delay(500);

  // 0c. 时间规划：截图不能跟着**真实配置**漂 —— 用户把短休息改成固定时间后，
  // 截图里就没有“间隔”那一行了，手册正文的表格会对不上。这里钉一份确定的状态：
  // 短休息 = 固定时间 + 两条时刻（正好展示“提醒时间”那块清单），长休息 = 倒计时，
  // 于是同一张图上两种计时方法各出现一次。再把“提醒时间”面板摊开。
  await set.evaluate(`(function(){var b=document.querySelector('#nav .nav-item[data-tab="timing"]');if(b)b.click();})()`);
  await delay(700);
  // 先注入一个自定义提醒卡（喝水提醒）+ 钉住短 / 长休息的计时方法，再重渲染时间规划页
  await set.evaluate(`(function(){return window.api.saveSettings({short:{mode:'fixed',slots:[{hour:9,minute:30,enabled:true,repeat:'daily',days:[]},{hour:14,minute:0,enabled:true,repeat:'weekly',days:[1,2,3,4,5]}]},long:{mode:'countdown',slots:[]},customBreaks:[{id:'cb-manual',name:'喝水提醒',enabled:true,config:{enabled:true,mode:'countdown',intervalMs:180000,durationMs:20000,notifyBeforeMs:10000,postponeMs:120000,slots:[]},messages:{order:'fixed',fixedId:'',items:[]}}]});})()`);
  await delay(800);
  await set.reload({ waitUntil: "domcontentloaded" });
  await delay(1400);
  await set.evaluate(`(function(){var b=document.querySelector('#nav .nav-item[data-tab="timing"]');if(b)b.click();})()`);
  await delay(600);
  // 「提醒时间」这一层不再折叠（2026-11-10 起常驻展开），截图前无需再点开
  await delay(300);
  await shot(set, "m_timing");

  // 0c-2. 自定义提醒卡：滚动到“喝水提醒”卡与“新建自定义提醒”虚线卡
  await set.evaluate(`(function(){var c=document.querySelector('#tab-timing > .card[data-cb="cb-manual"]');if(c)c.scrollIntoView({block:'start'});else{var a=document.getElementById('cb-add-card');if(a)a.scrollIntoView({block:'start'});} window.scrollBy(0,-10);})()`);
  await delay(500);
  await shot(set, "m_custom_card");

  // 0d. 提醒内容面板 + 批量添加弹窗（短休息第一条的批量添加）
  // 先确保短休息提醒内容面板已展开，再点“＋”打开批量框
  await set.evaluate(`(function(){var p=document.querySelector('.msg-panel[data-kind="short"]');if(p){var b=p.querySelector('.msg-toggle');if(b&&p.querySelector('.msg-body').classList.contains('hidden'))b.click();}})()`);
  await delay(400);
  await set.evaluate(`(function(){var p=document.querySelector('.msg-panel[data-kind="short"]');if(p){var add=p.querySelector('.msg-add');if(add)add.click();}})()`);
  await delay(500);
  await shot(set, "m_batch_add");
  // 关掉批量框（取消）
  await set.evaluate(`(function(){var c=document.getElementById('msg-batch-cancel');if(c)c.click();})()`);
  await delay(300);

  // 0e. 删除确认弹窗：展开短休息提醒内容后点第一条的“删除”
  await set.evaluate(`(function(){var p=document.querySelector('.msg-panel[data-kind="short"]');if(p){var b=p.querySelector('.msg-toggle');if(b&&p.querySelector('.msg-body').classList.contains('hidden'))b.click();}})()`);
  await delay(400);
  await set.evaluate(`(function(){var del=document.querySelector('.msg-panel[data-kind="short"] .msg-item .btn.mini[title="删除"]');if(del)del.click();})()`);
  await delay(500);
  await shot(set, "m_confirm");
  // 取消确认框（不真删）
  await set.evaluate(`(function(){var c=document.getElementById('cf-cancel');if(c)c.click();})()`);
  await delay(300);

  // 0f. 督休专员页（画廊）
  await set.evaluate(`(function(){var b=document.querySelector('#nav .nav-item[data-tab="supervisor"]');if(b)b.click();})()`);
  await delay(700);
  await shot(set, "m_supervisor");

  // 0g. 督休专员 · 高级选项（尺寸与位置）展开
  // 注意：必须点 <summary> 才能开合 —— 直接对 <details> 调 click() 不会切换展开态，
  // 之前这步一直没生效（拍出来的两张图逐字节相同）。
  // 展开后再滚到视口中间，否则内容留在折叠线以下，截图里只能看到标题行。
  await set.evaluate(`(function(){var s=document.querySelector('#sup-advanced');if(s&&!s.open){var h=s.querySelector('summary');if(h)h.click();}})()`);
  await delay(600);
  await set.evaluate(`(function(){var s=document.querySelector('#sup-advanced');if(s)s.scrollIntoView({block:'center'});})()`);
  await delay(400);
  await shot(set, "m_supervisor_advanced");
  // 收起高级选项并回到顶部，避免影响后续
  await set.evaluate(`(function(){var s=document.querySelector('#sup-advanced');if(s&&s.open){var h=s.querySelector('summary');if(h)h.click();}})()`);
  await delay(300);
  await set.evaluate(`(function(){var c=document.getElementById('content');if(c)c.scrollTop=0;})()`);
  await delay(200);

  // 0h. 统计报表页
  await set.evaluate(`(function(){var b=document.querySelector('#nav .nav-item[data-tab="stats"]');if(b)b.click();})()`);
  await delay(1000);
  await shot(set, "m_stats");

  // 0i. 快捷键页
  await set.evaluate(`(function(){var b=document.querySelector('#nav .nav-item[data-tab="shortcuts"]');if(b)b.click();})()`);
  await delay(600);
  await shot(set, "m_shortcuts");

  // 0j. 关于页
  await set.evaluate(`(function(){var b=document.querySelector('#nav .nav-item[data-tab="about"]');if(b)b.click();})()`);
  await delay(600);
  await shot(set, "m_about");

  // ============ 1. 启动一次会话，供休息 / 外卖窗口读取 ============
  const main = await open(browser, BASE + "/", 1000, 760);
  await delay(800);

  // 1a. 提前提示窗：内置版式
  const pre = await open(browser, BASE + "/prebreak.html?win=prebreak&postpone=1", 400, 240, { transparent: true });
  await delay(700);
  await shot(pre, "m_prebreak");

  // 1b. 提前提示窗：自定义提醒版式（先让服务端记住一个自定义提醒）
  await main.evaluate(`(function(){return window.api.saveSettings({customBreaks:[{id:'cb-manual',name:'喝水提醒',enabled:true,config:{enabled:true,mode:'countdown',intervalMs:180000,durationMs:20000,notifyBeforeMs:180000,postponeMs:120000,slots:[]},messages:{order:'fixed',fixedId:'',items:[]}}]});})()`);
  await delay(1000);
  const preC = await open(browser, BASE + "/prebreak.html?win=prebreak&custom=1&tail=" + encodeURIComponent(" 秒显示喝水提醒"), 400, 172, { transparent: true });
  await delay(700);
  await shot(preC, "m_prebreak_custom");

  // 1c. 休息窗口（横屏）：先造会话
  await main.evaluate(`(function(){return window.api.startBreak('short');})()`);
  await delay(1200);
  const wide = await open(browser, BASE + "/break.html?win=break", 1100, 720);
  await delay(1200);
  await shot(wide, "m_break_wide");

  // 1d. 休息窗口（竖屏）
  const tall = await open(browser, BASE + "/break.html?win=break", 760, 980);
  await delay(1200);
  await shot(tall, "m_break_tall");

  // 1e. 外卖提醒窗口
  await main.evaluate(`(function(){return window.api.previewTakeout();})()`);
  await delay(800);
  const tk = await open(browser, BASE + "/takeout.html?win=takeout", 880, 560, { transparent: true });
  await delay(1000);
  await shot(tk, "m_takeout");

  await browser.close();
  console.log("[shot] done -> " + OUT);
}

run().catch((err) => {
  console.error("[shot] 失败:", err);
  process.exit(1);
});
