// 自绘下拉（.cdrop-host）键盘可达性的行为探针（只读，不改任何数据）。
//
// 为什么需要它：cdrop 不是原生 <select>，是按钮 + role=listbox 拼出来的。静态语义
// （aria-haspopup / aria-expanded / role=option）证明不了“键盘真的进得去、选得中、回得来”。
// 这里用系统 Edge 打开 settings.html，把 /wails/runtime.js 换成只回 null 的桩，然后验证：
//
//   1) 按钮有可访问名（aria-label），且 aria-haspopup=listbox / aria-expanded 初始 false
//   2) 聚焦按钮按 ArrowDown → 菜单展开、焦点落到当前项（option）
//   3) 菜单内 ArrowDown/ArrowUp 移动焦点、Home/End 跳到首末
//   4) 菜单内 Enter 选中 → 菜单收起、焦点回按钮、按钮显示新值
//   5) 菜单内 Escape → 收起、焦点回按钮
//
// 用法：node scripts/probe_cdrop_a11y.js        （退出码 0 = 全通过）
const { chromium } = require('playwright-core');
const path = require('path');

const ROOT = path.resolve(__dirname, '..');
const DIST = path.join(ROOT, 'cmd', 'workerrest', 'web', 'dist');

let failed = 0;
function check(ok, label, detail) {
  if (ok) {
    console.log('  ok   ' + label);
  } else {
    failed++;
    console.log('  FAIL ' + label + (detail === undefined ? '' : '  | ' + JSON.stringify(detail)));
  }
}

// 假运行时：默认全回 null，但 GetSettings 返回仓库里的出厂配置（default_settings.json）——
// 设置页没有自己的默认值，拿到 null 会整页停摆，下拉就挂不上。这里用真实出厂配置喂它，
// 数据不会过时，且与本软件的“恢复默认设置”同源。
//
// 为什么不直接 route /wails/runtime.js：shim.js 用 import("/wails/runtime.js") 动态加载，
// 而 file:// 页面里这个绝对 specifier 在 Chromium 里解析失败（base URL 是 about:blank），
// route 根本拦不到。所以改走 shim.js 本身：把它的 runtime() 换成“直接 resolve 内联桩”，
// shim 其余逻辑（call/on/str/arr、window.api 形状、__WR_SHIM）原样保留。
const fs = require('fs');
const SHIM_SRC = fs.readFileSync(path.join(DIST, 'shim.js'), 'utf8');
const DEFAULT_SETTINGS = JSON.stringify(
  JSON.parse(fs.readFileSync(path.join(ROOT, 'internal', 'settings', 'default_settings.json'), 'utf8')),
);
// 内联桩：Call.ByName 对 GetSettings 返回出厂配置、ListRoles 返回空数组、
// BuiltinMsgDefaults 返回“三类提醒的空种子”（空 kinds，避免硬编码会过时的文案），
// 其余 null；Events 全 no-op。
const INLINE_RUNTIME = `Promise.resolve({
  Call: { ByName: async function (name) {
    if (name === "main.App.GetSettings") return ${DEFAULT_SETTINGS};
    if (name === "main.App.ListRoles") return [];
    if (name === "main.App.BuiltinMsgDefaults") {
      return { kinds: { short: [], long: [], takeout: [] } };
    }
    if (name === "main.App.AppInfo") return { version: "0.0.0", buildTime: "" };
    return null;
  } },
  Events: { On: function(){}, Off: function(){}, Emit: function(){} },
})`;
// 把 shim 里“import("/wails/runtime.js").catch(…)”换成上面的内联桩；结构上保留 .catch 链
// 的写法不变，只替换 import 表达式本身，语义等价且对 shim 无侵入。
const SHIM_STUB = SHIM_SRC.replace(
  /import\("\/wails\/runtime\.js"\)/,
  INLINE_RUNTIME,
);
if (SHIM_STUB === SHIM_SRC) {
  throw new Error('未能定位 shim.js 的 runtime 加载表达式，桩替换失败');
}

(async () => {
  const browser = await chromium.launch({ channel: 'msedge', headless: true });
  const page = await browser.newPage({ viewport: { width: 913, height: 857 } });
  page.on('pageerror', () => {}); // 后端是桩，初始化阶段报错与本次验证无关
  await page.route('**/shim.js', (route) =>
    route.fulfill({ status: 200, contentType: 'application/javascript', body: SHIM_STUB }),
  );
  const url = 'file:///' + path.resolve(DIST, 'settings.html').replace(/\\/g, '/');
  await page.goto(url, { waitUntil: 'domcontentloaded' });
  await page.waitForTimeout(500);

  // 目标：短休息的“触发方式”下拉（#short-mode），它在“时间规划”tab 里，默认折叠
  // （section.tab 是 display:none，按钮收不了焦点）。先切过去，与真实用户路径一致。
  await page.evaluate(() => {
    document.querySelector('.nav-item[data-tab="timing"]').click();
  });
  await page.waitForTimeout(100);
  const host = '#short-mode';

  // 1) 静态语义
  console.log('[cdrop] 静态语义');
  const stat = await page.evaluate((host) => {
    const btn = document.querySelector(host + ' .cdrop-btn');
    const menu = document.querySelector(host + ' .cdrop-menu');
    if (!btn || !menu) return null;
    return {
      label: btn.getAttribute('aria-label'),
      haspopup: btn.getAttribute('aria-haspopup'),
      expanded: btn.getAttribute('aria-expanded'),
      menuRole: menu.getAttribute('role'),
      itemCount: menu.children.length,
      itemRole: menu.children[0] ? menu.children[0].getAttribute('role') : null,
    };
  }, host);
  check(!!stat, '短休息“触发方式”有 .cdrop-btn 与 .cdrop-menu', stat);
  if (stat) {
    check(stat.label === '触发方式', '按钮有 aria-label="触发方式"', stat.label);
    check(stat.haspopup === 'listbox', '按钮 aria-haspopup=listbox', stat.haspopup);
    check(stat.expanded === 'false', '初始 aria-expanded=false', stat.expanded);
    check(stat.menuRole === 'listbox', '菜单 role=listbox', stat.menuRole);
    check(stat.itemRole === 'option', '菜单项 role=option', stat.itemRole);
    check(stat.itemCount >= 2, '菜单至少有 2 个选项', stat.itemCount);
  }

  // 2) ArrowDown 展开并聚焦当前项
  console.log('[cdrop] 键盘导航');
  await page.evaluate((host) => document.querySelector(host + ' .cdrop-btn').focus(), host);
  await page.keyboard.press('ArrowDown');
  await page.waitForTimeout(40);
  const opened = await page.evaluate((host) => {
    const root = document.querySelector(host).querySelector('.cdrop');
    const a = document.activeElement;
    return {
      open: root.classList.contains('open'),
      expanded: root.querySelector('.cdrop-btn').getAttribute('aria-expanded'),
      focusIsOption: a ? a.getAttribute('role') === 'option' : false,
      focusSelected: a ? a.getAttribute('aria-selected') : null,
    };
  }, host);
  check(opened.open, 'ArrowDown 展开菜单', opened);
  check(opened.expanded === 'true', '展开后 aria-expanded=true', opened.expanded);
  check(opened.focusIsOption, '焦点落到一个 option 上', opened.focusSelected);
  check(opened.focusSelected === 'true', '焦点项即当前选中项', opened.focusSelected);

  // 3) ArrowDown 移动到下一项
  const before = await page.evaluate(() => {
    const a = document.activeElement;
    return a ? a.dataset.value : null;
  });
  await page.keyboard.press('ArrowDown');
  await page.waitForTimeout(20);
  const after = await page.evaluate(() => {
    const a = document.activeElement;
    return a ? a.dataset.value : null;
  });
  check(before !== after, 'ArrowDown 移动焦点到下一项', { before, after });

  // Home / End 跳到首末
  await page.keyboard.press('End');
  await page.waitForTimeout(20);
  const atEnd = await page.evaluate((host) => {
    const menu = document.querySelector(host + ' .cdrop-menu');
    const a = document.activeElement;
    return a === menu.children[menu.children.length - 1];
  }, host);
  check(atEnd, 'End 跳到末项');
  await page.keyboard.press('Home');
  await page.waitForTimeout(20);
  const atHome = await page.evaluate((host) => {
    const menu = document.querySelector(host + ' .cdrop-menu');
    return document.activeElement === menu.children[0];
  }, host);
  check(atHome, 'Home 跳到首项');

  // 4) Enter 选中：收起、焦点回按钮、显示新值
  await page.keyboard.press('ArrowDown'); // 到第二项
  await page.waitForTimeout(20);
  const chosen = await page.evaluate(() => {
    const a = document.activeElement;
    return a ? a.dataset.value : null;
  });
  await page.keyboard.press('Enter');
  await page.waitForTimeout(40);
  const picked = await page.evaluate((host) => {
    const root = document.querySelector(host).querySelector('.cdrop');
    const a = document.activeElement;
    return {
      open: root.classList.contains('open'),
      value: root.dataset.value,
      focusBack: a ? a.classList.contains('cdrop-btn') : false,
      label: root.querySelector('.cdrop-cur').textContent,
    };
  }, host);
  check(!picked.open, 'Enter 选中后菜单收起', picked);
  check(picked.value === chosen, '选中值已写入 host.dataset.value', { chosen, value: picked.value });
  check(picked.focusBack, 'Enter 选中后焦点回按钮', picked.label);

  // 5) Escape 关闭：焦点回按钮
  await page.keyboard.press('ArrowDown');
  await page.waitForTimeout(20);
  await page.keyboard.press('Escape');
  await page.waitForTimeout(20);
  const esc = await page.evaluate((host) => {
    const root = document.querySelector(host).querySelector('.cdrop');
    const a = document.activeElement;
    return { open: root.classList.contains('open'), focusBack: a ? a.classList.contains('cdrop-btn') : false };
  }, host);
  check(!esc.open, 'Escape 收起菜单', esc);
  check(esc.focusBack, 'Escape 后焦点回按钮', esc);

  await browser.close();
  console.log('');
  console.log('结果：失败 ' + failed + ' 项');
  process.exit(failed === 0 ? 0 : 1);
})().catch((e) => {
  console.error('FAILED', e);
  process.exit(1);
});
