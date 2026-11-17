// 发布前可访问性审计（只读）：用系统 Edge 打开 web/dist 的静态 DOM，
// 统计交互控件的可访问名、label 关联、img alt、tab 序、焦点可见性线索。
// JS 全部阻断，只看 HTML 静态语义 —— 与“页面脚本未执行时”的语义等价。
//
// 2026-10-31 从 build/ 挪到这里：build/ 是被 gitignore 的产物目录，审计工具放那儿
// 等于没有版本；结果 json 仍写回 build/（那是产物）。命名用 textContent 而不是
// innerText：<details> 收起时 innerText 取不到文字，会把有文字的按钮误报成无名控件。
//
// 2026-11-16 重写判定层（此前四条死检查）：
//   1. outlineNoneRules 原来走 document.styleSheets —— file:// 下跨源样式表的
//      cssRules 抛 SecurityError 被 catch 吞掉，恒为空（settings.css:1223 明明有
//      outline:none 却报 0）。改为 fs 直读 dist/*.css 文本，离线、确定、不依赖
//      浏览器跨源策略。
//   2. 每页两趟：static（禁 JS，语义等于“脚本没执行时”）+ runtime（放行 JS）。
//      无名控件判定用 runtime 趟 —— 那才是用户看到的界面。break.html 的按钮
//      由 JS 生成，只看 static 趟等于没审。若 runtime 趟连一个可交互元素都没有，
//      该页判“页面可能没渲染成功”，不许静默通过。
//   3. pause.html 补进 PAGES（原来漏了，等于没审）。
//   4. 每个采集字段要么参与判定，要么带一条 notJudged 理由 —— 打开 json 一眼
//      能看出“哪些是真绿、哪些是没查”，不被一个 0 骗过去。
//
// 用法：node scripts/a11y_audit.js
// 退出码 0 = 判定项全过；1 = 有判定项失败（附明细）；2 = 审计自身故障。
const { chromium } = require('playwright-core');
const path = require('path');
const fs = require('fs');

const ROOT = path.resolve(__dirname, '..');
const OUT_DIR = path.join(ROOT, 'build');
const DIST = path.join(ROOT, 'cmd', 'workerrest', 'web', 'dist');

const PAGES = ['settings.html', 'break.html', 'takeout.html', 'prebreak.html', 'pause.html'];

// ---- CSS 静态扫描：outline:none 且无替代画法 ----
// 为什么不用 document.styleSheets：file:// 下跨源样式表 cssRules 抛 SecurityError，
// 被 catch{return[]} 吞掉后这个检查恒为空 —— 它曾是四条死检查之一。
function scanOutlineNone() {
  const rules = [];
  for (const f of fs.readdirSync(DIST).filter((n) => n.endsWith('.css'))) {
    const text = fs.readFileSync(path.join(DIST, f), 'utf8');
    // 跨行也找得到：把整个文件按声明切粗粒度状态机扫。
    const re = /outline\s*:\s*(?:none|0)\s*([^;{}]*)\s*(;|\})/g;
    let m;
    while ((m = re.exec(text))) {
      const at = m.index;
      // 回溯找这条声明所在的选择器（同一规则块的开头）
      const braceOpen = text.lastIndexOf('{', at);
      const braceClose = text.indexOf('}', at);
      const ruleStart = Math.max(text.lastIndexOf('}', at), text.lastIndexOf('*/', at), 0);
      let selector = '';
      if (braceOpen > ruleStart) {
        selector = text.slice(Math.max(braceOpen - 200, ruleStart), braceOpen).trim();
        // 取选择器串的最后一个逗号段（多选择器时只报命中的那段意义不大，整段都报）
      }
      // 规则块内有没有焦点环替代：box-shadow / outline-offset / 自定义 outline /
      // drop-shadow 滤镜（svg 不是 CSS 盒，box-shadow 画不上，只能走 filter）。
      // 注意先把空白全部去掉再判：`outline\s*:\s*(?!none|0)` 这种写法里 \s* 会回溯到
      // 零宽，让前瞻在「空格」上通过 —— `outline: none` 被判成「有替代」就是这么来的。
      const block = braceOpen >= 0 && braceClose > braceOpen ? text.slice(braceOpen, braceClose) : text.slice(at, at + 200);
      const flat = block.replace(/\s+/g, '').toLowerCase();
      const hasAlt =
        flat.includes('box-shadow') ||
        flat.includes('outline-offset') ||
        flat.includes('filter:drop-shadow') ||
        /outline:(?!none;|none$|0;|0$)/.test(flat);
      rules.push({
        file: f,
        selector: (selector.split('\n').pop() || '').trim().slice(-120),
        declaration: 'outline: ' + (m[1].trim() || 'none'),
        focusRingAlternative: hasAlt,
      });
    }
  }
  return rules;
}

(async () => {
  const cssOutlineNone = scanOutlineNone();
  // 判定口径克制：只判「outline:none 且同一规则块内没有 box-shadow /
  // outline-offset / 自定义 outline 替代」的条数。有替代的不算缺陷 ——
  // 用 box-shadow 画焦点环是正常写法，全报会制造假红。
  const outlineNoAlt = cssOutlineNone.filter((r) => !r.focusRingAlternative);

  const browser = await chromium.launch({
    channel: 'msedge',
    headless: true,
    args: ['--force-device-scale-factor=1'],
  });

  // 采集函数：一趟页面 DOM 的全部字段（static / runtime 两趟共用）
  const collect = () =>
    page_eval_collect();

  async function page_eval_collect(page) {
    return await page.evaluate(() => {
      // 注意用 textContent 而不是 innerText：`<details>` 收起时其内容的 innerText 是空串，
      // 会把“有文字、只是当前没展开”的按钮误报成无名控件（2026-10-31 误报过 3 个）。
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
      const dialogs = [...document.querySelectorAll('[role="dialog"], dialog, [aria-modal="true"]')];
      const liveRegions = [...document.querySelectorAll('[aria-live], [role="status"], [role="alert"]')];
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
        dialogs: dialogs.length,
        dialogNamed: dialogs.filter((d) => nameOf(d).name).length,
        liveRegions: liveRegions.length,
        lang: document.documentElement.getAttribute('lang'),
        title: document.title,
        viewportMeta: !!document.querySelector('meta[name="viewport"]'),
      };
    });
  }

  const out = {};
  for (const f of PAGES) {
    const url = 'file:///' + path.resolve(DIST, f).replace(/\\/g, '/');
    out[f] = {};
    // ---- 第一趟：禁 JS（语义 = “脚本没执行时”） ----
    {
      const page = await browser.newPage({ viewport: { width: 913, height: 857 } });
      await page.route('**/*.js', (r) => r.abort());
      await page.goto(url, { waitUntil: 'domcontentloaded' }).catch(() => {});
      out[f].static = await page_eval_collect(page);
      await page.close();
    }
    // ---- 第二趟：放行 JS，等渲染稳定 ----
    {
      const page = await browser.newPage({ viewport: { width: 913, height: 857 } });
      await page.goto(url, { waitUntil: 'load' }).catch(() => {});
      await page.waitForTimeout(600); // 给运行时生成的控件一点时间
      out[f].runtime = await page_eval_collect(page);
      // 设置页补一组「实底主按钮」的对比度实测：这里的白字对 accent 曾经只有
      // 3.71:1（< AA 4.5），而全仓没有任何断言盖住它（2026-11-16 报告 §5.7 第 14 条）。
      // 深色主题按设计用「深字压浅 accent」（settings.css 深色段注释写了比值），不在这一趟。
      if (f === 'settings.html') {
        out[f].contrastProbe = await page.evaluate(() => {
          const el = document.querySelector('.btn.primary');
          if (!el) return null;
          const s = getComputedStyle(el);
          const m = /rgba?\(\s*(\d+)\s*,\s*(\d+)\s*,\s*(\d+)/;
          const fg = m.exec(s.color), bg = m.exec(s.backgroundColor);
          if (!fg || !bg) return null;
          const ch = (x) => { x /= 255; return x <= 0.03928 ? x / 12.92 : Math.pow((x + 0.055) / 1.055, 2.4); };
          const lum = (c) => 0.2126 * ch(+c[1]) + 0.7152 * ch(+c[2]) + 0.0722 * ch(+c[3]);
          const l1 = lum(fg), l2 = lum(bg);
          return {
            color: s.color, background: s.backgroundColor,
            fontSize: s.fontSize, fontWeight: s.fontWeight,
            ratio: Math.round(((Math.max(l1, l2) + 0.05) / (Math.min(l1, l2) + 0.05)) * 100) / 100,
          };
        });
      }
      await page.close();
    }
  }
  await browser.close();

  out._css = {
    outlineNoneRules: cssOutlineNone,
    outlineNoneWithoutFocusAlternative: outlineNoAlt.length,
  };

  // ---- 判定层：每个字段要么判定，要么 notJudged 写明理由 ----
  const failures = [];
  for (const f of PAGES) {
    const rt = out[f].runtime;
    const st = out[f].static;
    if (!rt || !st) {
      failures.push(f + ': 采集失败（页面可能根本没打开）');
      continue;
    }
    if (rt.unnamedCount > 0) {
      failures.push(f + ': 无名控件 ' + rt.unnamedCount + ' 个（' + rt.unnamedIds.slice(0, 8).join(', ') + '）');
    }
    // 休息窗这类按钮由 JS 生成的页，static 趟控件数 0 是常态；runtime 趟一个
    // 可交互元素都没有则说明页面没渲染成功 —— “无名控件 0” 对它是空结论，
    // 必须显式失败，不许静默通过（2026-11-16 前的旧版在这里全绿假通过）。
    if (rt.tabbableCount === 0 && rt.counts.button === 0 && rt.counts.input === 0 && rt.counts.select === 0 && rt.counts.a === 0) {
      failures.push(f + ': runtime 趟没有任何可交互元素 —— 页面可能没渲染成功，本页审计是空结论');
    }
    if (rt.imgNoAlt.length > 0) {
      failures.push(f + ': ' + rt.imgNoAlt.length + ' 张 img 缺 alt 属性（WCAG 1.1.1 A 级）');
    }
    if (!rt.lang) {
      failures.push(f + ': <html> 缺 lang 属性（WCAG 3.1.1 A 级）');
    }
    if (!rt.title) {
      failures.push(f + ': 缺 <title>');
    }
    if (rt.dialogs > 0 && rt.dialogNamed === 0) {
      failures.push(f + ': 有 ' + rt.dialogs + ' 个 dialog，没有一个带可访问名');
    }
    // notJudged：采集但不判定的字段，理由显式写进 json，别让人猜
    out[f].notJudged = {
      imgEmptyAlt: 'alt="" 是装饰图的合法写法，不算缺陷',
      liveRegions: '静态审计验证不了播报内容与时机，需要行为级测试',
      tabbableFirst: '焦点顺序只采集（顺序对错要看交互语义，静态审计判不了）',
    };
    // 设置页主按钮：13px/600 按正文对待，AA 要 4.5（--on-accent 白字对 --accent）
    if (f === 'settings.html') {
      const ct = out[f].contrastProbe;
      if (!ct) {
        failures.push('settings.html: 没取到 .btn.primary 的计算样式（对比度未测）');
      } else if ((ct.ratio || 0) < 4.5) {
        failures.push(
          'settings.html: 主按钮文字对比度 ' + ct.ratio + ':1 < 4.5（' +
            ct.fontSize + '/' + ct.fontWeight + ' ' + ct.color + ' on ' + ct.background + '）',
        );
      }
    }
  }
  if (outlineNoAlt.length > 0) {
    failures.push(
      'CSS: ' + outlineNoAlt.length + ' 条 outline:none 无焦点环替代（' +
        outlineNoAlt.slice(0, 6).map((r) => r.file + ' ' + r.selector).join(' | ') + '）',
    );
  }

  fs.mkdirSync(OUT_DIR, { recursive: true });
  fs.writeFileSync(path.join(OUT_DIR, 'a11y_audit.json'), JSON.stringify(out, null, 2), 'utf8');
  // 控制台只打判定相关的摘要；完整字段看 json
  for (const f of PAGES) {
    const rt = (out[f] && out[f].runtime) || { counts: {} };
    console.log(f + '  button=' + rt.counts.button + ' input=' + rt.counts.input + ' unnamed=' + rt.unnamedCount + ' imgNoAlt=' + (rt.imgNoAlt || []).length + ' lang=' + JSON.stringify(rt.lang) + ' staticUnnamed=' + ((out[f].static || {}).unnamedCount));
  }
  console.log('CSS outline:none 总数=' + cssOutlineNone.length + '，无替代=' + outlineNoAlt.length);

  if (failures.length) {
    console.error('审计失败项：');
    for (const x of failures) console.error('  - ' + x);
    process.exit(1);
  }
  console.log('判定项全部通过（notJudged 字段见 json）');
})().catch((e) => { console.error('FAILED', e); process.exit(2); });
