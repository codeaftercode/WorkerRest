// 自绘下拉（.cdrop-host）键盘可达性的行为探针（只读，不改任何数据）。
//
// 为什么需要它：cdrop 不是原生 <select>，是按钮 + role=listbox 拼出来的。静态语义
// （aria-haspopup / aria-expanded / role=option）证明不了“键盘真的进得去、选得中、回得来”。
// 这里用系统 Edge 打开 settings.html，把 /wails/runtime.js 换成只回 null 的桩，然后验证：
//
//   1) 按钮有可访问名（aria-label），且 aria-haspopup=listbox / aria-expanded 初始 false
//   2) 聚焦按钮按 ArrowDown → 菜单展开、焦点落到第一项（option）
//   3) 菜单内 ArrowDown 移动焦点、Home/End 跳到首末
//   4) 菜单内 Enter 勾选 → 菜单**不关**、该选项 aria-selected=true（多选语义）
//   5) 菜单内 Escape → 收起、焦点回按钮
//
// 2026-11-10：产品里最后一个**单选**自绘下拉（“暂停休息时”）改成了三档段控，于是这里
// 改锚“通用配置 → 在以下屏幕显示”的多选下拉（#ap-displays）——它是全站最需要键盘
// 可达性的一个控件（插拔屏、历史记录、刷新都在里面）。段控的键盘模型由
// settings_options_probe.js 的 verifyModeSeg / 暂停休息时那几条断言负责。
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
const FACTORY = JSON.parse(
  fs.readFileSync(path.join(ROOT, 'internal', 'settings', 'default_settings.json'), 'utf8'),
);
// 出厂 displayIds 是空数组（启动时由 Go 侧探测主屏写入）。这里显式勾上桩里的两块屏：
// ① 真实运行时的配置不会停在空中数组；② 多选下拉“至少留一块”的兜底（syncSelection
// 把主屏勾上、但 effective() 仍是空）会让 dataset.value 与界面不一致，干扰键盘用例。
FACTORY.displayIds = ['D1', 'D2'];
const DEFAULT_SETTINGS = JSON.stringify(FACTORY);
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
    // 两块假屏：多选下拉没有数据就只剩“未检测到显示器”一行，键盘模型无从验证。
    // 只用 label / 分辨率这类展示字段，不掺 ID 语义（真枚举由 Go 侧负责）。
    if (name === "main.App.ListDisplays") return [
      { id: "D1", label: "测试屏 A", width: 1920, height: 1080, isPrimary: true },
      { id: "D2", label: "测试屏 B", width: 2560, height: 1440, isPrimary: false },
    ];
    if (name === "main.App.KnownDisplays") return [];
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

  // 目标：「通用配置」页“在以下屏幕显示”的多选下拉（#ap-displays）。它在页签里，
  // 默认不可见（section.tab 是 display:none，按钮收不了焦点），先切过去，与真实用户路径一致。
  await page.evaluate(() => {
    document.querySelector('.nav-item[data-tab="general"]').click();
  });
  await page.waitForTimeout(300);
  const host = '#ap-displays';

  // 1) 静态语义
  console.log('[cdrop] 静态语义');
  const stat = await page.evaluate((host) => {
    const btn = document.querySelector(host + ' .cdrop-btn');
    const opts = document.querySelector(host + ' .cdrop-opts');
    if (!btn || !opts) return null;
    const items = opts.querySelectorAll("[role='option']");
    return {
      label: btn.getAttribute('aria-label'),
      haspopup: btn.getAttribute('aria-haspopup'),
      expanded: btn.getAttribute('aria-expanded'),
      listRole: opts.getAttribute('role'),
      multi: opts.getAttribute('aria-multiselectable'),
      itemCount: items.length,
      itemRole: items[0] ? items[0].getAttribute('role') : null,
      itemLabel: items[0] ? (items[0].querySelector('.cdrop-label') || {}).textContent : null,
      itemSelected: items[0] ? items[0].getAttribute('aria-selected') : null,
    };
  }, host);
  check(!!stat, '显示器多选下拉有 .cdrop-btn 与 .cdrop-opts', stat);
  if (stat) {
    check(stat.label === '显示的屏幕', '按钮有 aria-label="显示的屏幕"', stat.label);
    check(stat.haspopup === 'listbox', '按钮 aria-haspopup=listbox', stat.haspopup);
    check(stat.expanded === 'false', '初始 aria-expanded=false', stat.expanded);
    check(stat.listRole === 'listbox' && stat.multi === 'true',
      '选项容器 role=listbox 且 aria-multiselectable=true', { r: stat.listRole, m: stat.multi });
    check(stat.itemRole === 'option', '选项 role=option', stat.itemRole);
    check(stat.itemCount >= 2, '至少有 2 个选项（桩里喂了两块屏）', stat.itemCount);
    check(stat.itemLabel === '测试屏 A', '选项用的是显示器名字（label 字段）', stat.itemLabel);
    check(stat.itemSelected === 'true', '配置里勾着的屏 aria-selected=true', stat.itemSelected);
  }

  // 2) ArrowDown 展开并聚焦第一项
  console.log('[cdrop] 键盘导航');
  await page.evaluate((host) => document.querySelector(host + ' .cdrop-btn').focus(), host);
  await page.keyboard.press('ArrowDown');
  await page.waitForTimeout(60);
  const opened = await page.evaluate((host) => {
    const root = document.querySelector(host).querySelector('.cdrop');
    const a = document.activeElement;
    const first = root.querySelector('.cdrop-opts [role="option"]');
    return {
      open: root.classList.contains('open'),
      expanded: root.querySelector('.cdrop-btn').getAttribute('aria-expanded'),
      focusIsOption: a ? a.getAttribute('role') === 'option' : false,
      focusIsFirst: !!first && a === first,
      focused: a ? a.dataset.value : null,
    };
  }, host);
  check(opened.open, 'ArrowDown 展开菜单', opened);
  check(opened.expanded === 'true', '展开后 aria-expanded=true', opened.expanded);
  check(opened.focusIsOption, '焦点落到一个 option 上', opened.focused);
  check(opened.focusIsFirst, '焦点落在第一项', opened.focused);

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

  // Home / End 跳到首末（只认选项，不认底部的“刷新”）
  await page.keyboard.press('End');
  await page.waitForTimeout(20);
  const atEnd = await page.evaluate((host) => {
    const opts = document.querySelector(host).querySelectorAll('.cdrop-opts [role="option"]');
    return document.activeElement === opts[opts.length - 1];
  }, host);
  check(atEnd, 'End 跳到末项');
  await page.keyboard.press('Home');
  await page.waitForTimeout(20);
  const atHome = await page.evaluate((host) => {
    const opts = document.querySelector(host).querySelectorAll('.cdrop-opts [role="option"]');
    return document.activeElement === opts[0];
  }, host);
  check(atHome, 'Home 跳到首项');

  // 4) Enter 切换勾选：多选不关菜单，aria-selected 跟着翻，按钮摘要跟着变
  //    （焦点已由 Home 落在第一块“测试屏 A”上，它出厂就勾着 → Enter 是**取消**勾选，
  //     留下的只有第二块，摘要因此从“2 个显示器”变成“测试屏 B”）
  const chosen = await page.evaluate(() => {
    const a = document.activeElement;
    return a ? a.dataset.value : null;
  });
  await page.keyboard.press('Enter');
  await page.waitForTimeout(80);
  const picked = await page.evaluate((host) => {
    const root = document.querySelector(host).querySelector('.cdrop');
    const opt = root.querySelector('.cdrop-opts [data-value="' + document.activeElement.dataset.value + '"]');
    return {
      open: root.classList.contains('open'),
      selected: opt ? opt.getAttribute('aria-selected') : null,
      summary: (root.querySelector('.cdrop-cur') || {}).textContent || '',
      value: document.querySelector(host).dataset.value || '',
    };
  }, host);
  check(picked.open, 'Enter 切换勾选后菜单不关（多选语义）', picked.open);
  check(picked.selected === 'false', 'Enter 后该项 aria-selected 翻成 false', picked.selected);
  check(picked.value.indexOf(chosen) < 0 && picked.value.indexOf('D2') >= 0,
    '勾选结果写进 host.dataset.value（摘掉 D1、留下 D2）', { chosen, ...picked });
  check(picked.summary === '测试屏 B', '按钮上的摘要跟着更新成剩下那块', picked.summary);

  // 5) Escape 关闭：焦点回按钮
  await page.keyboard.press('Escape');
  await page.waitForTimeout(30);
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
