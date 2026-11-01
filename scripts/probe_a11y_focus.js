// 设置页对话框焦点管理的行为探针（只读，不改任何数据）。
//
// 为什么需要它：静态 DOM 审计（build/a11y_audit.js）只能证明「有 role / aria-modal /
// aria-labelledby」，证明不了「焦点真的进得去、跑不出、回得来」—— 那三条正是审计
// 域8「8 个 role=dialog 无焦点管理证据」要的证据。这里用系统 Edge 打开 settings.html，
// 把 /wails/runtime.js 换成只回 null 的桩（不连真实后端、不碰配置），再逐个对话框验证：
//
//   1) 打开后焦点在 .modal-box 内（不是还留在触发按钮上）
//   2) Tab 到最后一个控件后再按 Tab 回到第一个（Shift+Tab 反向同理）
//   3) 关闭后焦点回到打开它的那个控件
//
// 用法：node scripts/probe_a11y_focus.js        （退出码 0 = 全通过）
const { chromium } = require('playwright-core');
const path = require('path');

const ROOT = path.resolve(__dirname, '..');
const DIST = path.join(ROOT, 'cmd', 'workerrest', 'frontend', 'dist');

const MODALS = [
  'batch-modal',
  'confirm-modal',
  'prompt-modal',
  'app-pick-modal',
  'color-modal',
  'update-modal',
  'updated-modal',
  'stats-modal',
  'snd-scan-modal',
  'notices-modal',
];

let failed = 0;
function check(ok, label, detail) {
  if (ok) {
    console.log('  ok   ' + label);
  } else {
    failed++;
    console.log('  FAIL ' + label + (detail === undefined ? '' : '  | ' + JSON.stringify(detail)));
  }
}

// 假运行时：所有绑定调用都 resolve(null)；事件订阅什么都不做。
// shim.js 对 null 有兜底分支，页面能正常初始化，但不会请求任何真实数据。
const RUNTIME_STUB = `
export const Call = { ByName: async () => null };
export const Events = { On: () => {}, Off: () => {}, Emit: () => {} };
export default { Call, Events };
`;

(async () => {
  const browser = await chromium.launch({ channel: 'msedge', headless: true });
  const page = await browser.newPage({ viewport: { width: 913, height: 857 } });
  page.on('pageerror', () => {}); // 初始化阶段的报错与本次验证无关（后端是桩）
  await page.route('**/wails/runtime.js', (route) =>
    route.fulfill({ status: 200, contentType: 'text/javascript', body: RUNTIME_STUB }),
  );

  const url = 'file:///' + path.resolve(DIST, 'settings.html').replace(/\\/g, '/');
  await page.goto(url, { waitUntil: 'domcontentloaded' });
  await page.waitForTimeout(300);

  console.log('[a11y] 静态语义');
  const statics = await page.evaluate((ids) => {
    const sel =
      'a[href], button:not([disabled]), input:not([disabled]):not([type="hidden"]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';
    return ids.map((id) => {
      const box = document.querySelector('#' + id + ' .modal-box');
      return {
        id,
        hasBox: !!box,
        role: box ? box.getAttribute('role') : null,
        modal: box ? box.getAttribute('aria-modal') : null,
        labelledby: box ? box.getAttribute('aria-labelledby') : null,
        labelTarget: box ? !!document.getElementById(box.getAttribute('aria-labelledby') || '') : false,
        focusables: box ? box.querySelectorAll(sel).length : 0,
      };
    });
  }, MODALS);
  for (const s of statics) {
    check(s.hasBox, s.id + ' 有 .modal-box');
    check(s.role === 'dialog' || s.role === 'alertdialog', s.id + ' role=dialog/alertdialog', s.role);
    check(s.modal === 'true', s.id + ' aria-modal=true', s.modal);
    check(s.labelledby && s.labelTarget, s.id + ' aria-labelledby 指向真实标题', s.labelledby);
    check(s.focusables > 0, s.id + ' 内含可聚焦控件', s.focusables);
  }
  const live = await page.evaluate(() => {
    const el = document.getElementById('sr-status');
    return el
      ? { role: el.getAttribute('role'), live: el.getAttribute('aria-live'), atomic: el.getAttribute('aria-atomic') }
      : null;
  });
  check(!!live && live.role === 'status' && live.live === 'polite', 'toast 有常驻 role=status/aria-live=polite 活区', live);

  console.log('[a11y] 焦点：进得去 / 跑不出 / 回得来');
  for (const id of MODALS) {
    // 触发元素用一个临时插入的可见按钮：真实页面里由「点它的那个控件」扮演这个角色，
    // 而页面上现成的按钮大多在未激活的 tab / 折叠区里（display:none 收不了焦点）。
    const opened = await page.evaluate((modalId) => {
      let opener = document.getElementById('probe-opener');
      if (!opener) {
        opener = document.createElement('button');
        opener.id = 'probe-opener';
        opener.textContent = 'opener';
        document.body.appendChild(opener);
      }
      opener.focus();
      document.getElementById(modalId).classList.remove('hidden');
      return document.activeElement === opener;
    }, id);
    check(opened, id + ' 打开前焦点在触发控件上');
    await page.waitForTimeout(60);

    const inside = await page.evaluate((modalId) => {
      const box = document.querySelector('#' + modalId + ' .modal-box');
      const a = document.activeElement;
      return { inBox: !!box && box.contains(a), tag: a ? a.tagName + (a.id ? '#' + a.id : '') : null };
    }, id);
    check(inside.inBox, id + ' 打开后焦点进入对话框', inside.tag);

    // Tab 循环：把焦点放到最后一个控件，按 Tab 应回到第一个
    const ring = await page.evaluate((modalId) => {
      const box = document.querySelector('#' + modalId + ' .modal-box');
      const sel =
        'a[href], button:not([disabled]), input:not([disabled]):not([type="hidden"]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';
      const items = [...box.querySelectorAll(sel)].filter((el) => el.offsetParent !== null);
      items[items.length - 1].focus();
      return { count: items.length, first: items[0].id || items[0].tagName };
    }, id);
    await page.keyboard.press('Tab');
    await page.waitForTimeout(20);
    const afterTab = await page.evaluate((modalId) => {
      const box = document.querySelector('#' + modalId + ' .modal-box');
      const a = document.activeElement;
      return { inBox: !!box && box.contains(a), id: a ? a.id || a.tagName : null };
    }, id);
    check(
      afterTab.inBox && (ring.count === 1 || afterTab.id === ring.first),
      id + ' Tab 在框内循环（' + ring.count + ' 个控件）',
      afterTab,
    );

    // Shift+Tab 从第一个应回到最后一个
    await page.evaluate((modalId) => {
      const box = document.querySelector('#' + modalId + ' .modal-box');
      const sel =
        'a[href], button:not([disabled]), input:not([disabled]):not([type="hidden"]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';
      const items = [...box.querySelectorAll(sel)].filter((el) => el.offsetParent !== null);
      items[0].focus();
    }, id);
    await page.keyboard.press('Shift+Tab');
    await page.waitForTimeout(20);
    const afterShift = await page.evaluate((modalId) => {
      const box = document.querySelector('#' + modalId + ' .modal-box');
      const a = document.activeElement;
      return { inBox: !!box && box.contains(a), id: a ? a.id || a.tagName : null };
    }, id);
    check(afterShift.inBox, id + ' Shift+Tab 不跑出对话框', afterShift);

    // 关闭：焦点回到触发控件
    await page.evaluate((modalId) => {
      document.getElementById(modalId).classList.add('hidden');
    }, id);
    await page.waitForTimeout(40);
    const back = await page.evaluate(() => {
      const a = document.activeElement;
      return a ? a.id || a.tagName : null;
    });
    check(back === 'probe-opener', id + ' 关闭后焦点回到触发控件', back);
  }

  await browser.close();
  console.log('');
  console.log('结果：失败 ' + failed + ' 项');
  process.exit(failed === 0 ? 0 : 1);
})().catch((e) => {
  console.error('FAILED', e);
  process.exit(1);
});
