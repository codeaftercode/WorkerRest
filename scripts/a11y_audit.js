// 发布前可访问性审计（只读）：用系统 Edge 打开 frontend/dist 的静态 DOM，
// 统计交互控件的可访问名、label 关联、img alt、tab 序、焦点可见性线索。
// JS 全部阻断，只看 HTML 静态语义 —— 与「页面脚本未执行时」的语义等价。
//
// 2026-10-31 从 build/ 挪到这里：build/ 是被 gitignore 的产物目录，审计工具放那儿
// 等于没有版本；结果 json 仍写回 build/（那是产物）。命名用 textContent 而不是
// innerText：<details> 收起时 innerText 取不到文字，会把有文字的按钮误报成无名控件。
//
// 用法：node scripts/a11y_audit.js        （退出码 0 = 无无名控件）
const { chromium } = require('playwright-core');
const path = require('path');
const fs = require('fs');

const ROOT = path.resolve(__dirname, '..');
const OUT_DIR = path.join(ROOT, 'build');

const PAGES = ['settings.html', 'break.html', 'takeout.html', 'prebreak.html'];

(async () => {
  const browser = await chromium.launch({
    channel: 'msedge',
    headless: true,
    args: ['--force-device-scale-factor=1'],
  });
  const out = {};
  for (const f of PAGES) {
    const page = await browser.newPage({ viewport: { width: 913, height: 857 } });
    await page.route('**/*.js', (r) => r.abort());
    const url = 'file:///' + path.resolve(ROOT, 'cmd/workerrest/frontend/dist', f).replace(/\\/g, '/');
    await page.goto(url, { waitUntil: 'domcontentloaded' }).catch(() => {});
    out[f] = await page.evaluate(() => {
      // 注意用 textContent 而不是 innerText：`<details>` 收起时其内容的 innerText 是空串，
      // 会把「有文字、只是当前没展开」的按钮误报成无名控件（2026-10-31 误报过 3 个）。
      const txt = (el) => (el.textContent || '').trim().replace(/\s+/g, ' ');
      const labelledBy = (el) => {
        const ids = (el.getAttribute('aria-labelledby') || '').split(/\s+/).filter(Boolean);
        return ids.map((i) => (document.getElementById(i)?.innerText || '').trim()).join(' ').trim();
      };
      const nameOf = (el) => {
        const a = (el.getAttribute('aria-label') || '').trim();
        if (a) return { name: a, from: 'aria-label' };
        const lb = labelledBy(el);
        if (lb) return { name: lb, from: 'aria-labelledby' };
        const t = txt(el);
        if (t) return { name: t, from: 'text' };
        const ti = (el.getAttribute('title') || '').trim();
        if (ti) return { name: ti, from: 'title' };
        if (el.id) {
          const l = document.querySelector(`label[for="${CSS.escape(el.id)}"]`);
          if (l && txt(l)) return { name: txt(l), from: 'label[for]' };
        }
        const wrap = el.closest('label');
        if (wrap && txt(wrap)) return { name: txt(wrap), from: 'wrapping-label' };
        const ph = (el.getAttribute('placeholder') || '').trim();
        if (ph) return { name: ph, from: 'placeholder' };
        return { name: '', from: 'none' };
      };
      const interactive = [...document.querySelectorAll('button, [role="button"], a[href], input, select, textarea, [tabindex]:not([tabindex="-1"])')];
      const unnamed = interactive
        .map((el) => ({ tag: el.tagName.toLowerCase(), id: el.id || '', cls: el.className || '', ...nameOf(el) }))
        .filter((x) => !x.name);
      const imgs = [...document.querySelectorAll('img')].map((i) => ({ src: i.getAttribute('src'), alt: i.getAttribute('alt') }));
      const noAlt = imgs.filter((i) => i.alt === null);
      const emptyAlt = imgs.filter((i) => i.alt === '');
      const tabbable = interactive.filter((el) => !el.disabled && el.tabIndex >= 0).map((el) => el.tagName.toLowerCase() + (el.id ? '#' + el.id : ''));
      // 焦点可见性线索：有没有全局 outline:none 且无替代
      const outlineNone = [...document.styleSheets].flatMap((s) => { try { return [...s.cssRules]; } catch { return []; } })
        .map((r) => r.cssText || '').filter((t) => /outline\s*:\s*(none|0)/i.test(t)).slice(0, 8);
      const dialogs = [...document.querySelectorAll('[role="dialog"], dialog, [aria-modal="true"]')].length;
      const liveRegions = [...document.querySelectorAll('[aria-live], [role="status"], [role="alert"]')].length;
      return {
        counts: {
          button: document.querySelectorAll('button').length,
          divOnclick: document.querySelectorAll('div[onclick], span[onclick]').length,
          input: document.querySelectorAll('input').length,
          select: document.querySelectorAll('select').length,
          a: document.querySelectorAll('a[href]').length,
          label: document.querySelectorAll('label').length,
          img: imgs.length,
        },
        unnamedCount: unnamed.length,
        unnamedIds: unnamed.map((x) => x.id || x.tag + '.' + String(x.cls).split(' ')[0]),
        unnamedSample: unnamed.slice(0, 12),
        imgNoAlt: noAlt,
        imgEmptyAlt: emptyAlt.length,
        tabbableCount: tabbable.length,
        tabbableFirst: tabbable.slice(0, 10),
        outlineNoneRules: outlineNone,
        dialogs,
        liveRegions,
        lang: document.documentElement.getAttribute('lang'),
        title: document.title,
        viewportMeta: !!document.querySelector('meta[name="viewport"]'),
      };
    });
    await page.close();
  }
  await browser.close();
  fs.mkdirSync(OUT_DIR, { recursive: true });
  fs.writeFileSync(path.join(OUT_DIR, 'a11y_audit.json'), JSON.stringify(out, null, 2), 'utf8');
  console.log(JSON.stringify(out, null, 2));
  const bad = Object.entries(out).filter(([, v]) => v.unnamedCount > 0);
  if (bad.length) {
    console.error('无名控件：' + bad.map(([k, v]) => k + '=' + v.unnamedCount).join(' '));
    process.exit(1);
  }
})().catch((e) => { console.error('FAILED', e); process.exit(1); });
