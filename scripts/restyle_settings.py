# -*- coding: utf-8 -*-
"""WorkerRest 设置窗口视觉重设计落地脚本（2026-11-13，规格 docs/design/settings-restyle-spec.md）。
三阶段：① 原文逐条字面替换（全部断言命中，任一失配即中止不落盘）
        ② 全局归并（圆角阶梯 / 字号档位，替换全部并报告条数）
        ③ 替换后恢复例外（断言命中）。"""
import io, sys

CSS = r"D:\Projects\personal\WorkerRest\cmd\workerrest\web\dist\settings.css"
HTML = r"D:\Projects\personal\WorkerRest\cmd\workerrest\web\dist\settings.html"

def apply(path, pairs, label, strict=True):
    with io.open(path, "r", encoding="utf-8", newline="") as f:
        text = f.read()
    if strict:
        errs = []
        for i, (old, new) in enumerate(pairs):
            if text.count(old) < 1:
                errs.append("  [%02d] 未命中（0 次）: %r" % (i, old[:100]))
        if errs:
            print(label, "失配，中止：")
            print("\n".join(errs))
            sys.exit(1)
    counts = []
    for old, new in pairs:
        n = text.count(old)
        counts.append(n)
        text = text.replace(old, new)
    with io.open(path, "w", encoding="utf-8", newline="") as f:
        f.write(text)
    print(label, "OK，%d 条，命中数:" % len(pairs), counts)

# ============================================================ CSS · 阶段①：原文精确替换
P1 = []

# --- A. 令牌块（浅/深）整体重写 ---
P1.append((
""":root {
  --bg: #f5f6fa;
  --panel: #ffffff;
  --border: #e6e8f0;
  --text: #1f2430;
  --text-2: #6b7280;
  --accent: #4f7cff;
  --accent-weak: #eef2ff;
  --danger: #e5484d;
  --danger-weak: #fdeced;
  --radius: 14px;
  /* 关于页顶部横幅的底色：取自 sitting_risk.webp 左右边缘像素均值，
     横幅比图片宽时用它补两侧空白 —— 必须与图片边缘同色，否则会出现竖直接缝。 */
  --about-banner-bg: #e0d6ce;
  /* 字段行：标签列定宽 → 控件左缘恒在一条竖线上，标签↔控件的距离与窗口宽度无关 */
  --label-w: 200px;
  --row-gap: 16px;
}

[data-theme='dark'] {
  --bg: #12141c;
  --panel: #1b1e2b;
  --border: #2a2e40;
  --text: #e8eaf2;
  --text-2: #9aa1b5;
  --accent: #6d95ff;
  --accent-weak: #232842;
  --danger-weak: #3b2226;
}""",
""":root {
  /* 中性面：bg 页面 / panel 卡片与浮层 / panel-2 凹面（输入底、悬停、内嵌面） */
  --bg: #f5f6fa;
  --panel: #ffffff;
  --panel-2: #f3f4f9;
  /* 两级边框：常规分隔 / 加强（悬停、可交互描边、pill-local） */
  --border: #e6e8f0;
  --border-strong: #d5d9e6;
  /* 三级文字灰：正文 / 次要说明 / 占位与微注 */
  --text: #1f2430;
  --text-2: #6b7280;
  --text-3: #98a0af;
  /* 唯一强调色（蓝相不动）：本体 / 悬停 / 弱底 / 实底上的文字 / 焦点外圈 */
  --accent: #4f7cff;
  --accent-hover: #3d6bf0;
  --accent-weak: #eef2ff;
  --on-accent: #ffffff;
  --ring: rgba(79, 124, 255, 0.18);
  /* 语义三件套 ×3（色相锚定图表色板：success=#34a46f 系、warn=#e0a23f 系） */
  --success: #34a46f;   --success-fg: #1e7a4d;  --success-weak: #e6f5ed;
  --warn: #c77d1a;      --warn-fg: #96600b;     --warn-weak: #faf1de;
  --danger: #e5484d;    --danger-fg: #b93037;   --danger-weak: #fdeced;
  --danger-active: #c93b3f;
  /* 反色浮层（tooltip / 图表提示）：浅色主题用深底，任何底色上都可读 */
  --tip-bg: #252a3a;
  --tip-fg: #f2f4fa;
  --overlay: rgba(23, 26, 38, 0.45);
  /* 圆角四档：控件 / 浮层与徽标 / 卡片容器 / 胶囊 */
  --radius: 13px;
  --r-sm: 6px;
  --r-md: 9px;
  --r-lg: 13px;
  --r-pill: 999px;
  /* 阴影四档（纸面秩序：静置近乎不可见，浮层靠描边分层） */
  --sh-1: 0 1px 2px rgba(20, 24, 40, 0.05);
  --sh-2: 0 2px 8px rgba(20, 24, 40, 0.08);
  --sh-3: 0 1px 3px rgba(20, 24, 40, 0.06), 0 4px 16px rgba(20, 24, 40, 0.10);
  --sh-4: 0 2px 8px rgba(20, 24, 40, 0.08), 0 12px 32px rgba(20, 24, 40, 0.16);
  /* 关于页顶部横幅的底色：取自 sitting_risk.webp 左右边缘像素均值，
     横幅比图片宽时用它补两侧空白 —— 必须与图片边缘同色，否则会出现竖直接缝。 */
  --about-banner-bg: #e0d6ce;
  /* 字段行：标签列定宽 → 控件左缘恒在一条竖线上（2026-11-13 由 200px 放宽到 216px，
     14px 下容纳约 15 个汉字，废除「借 13px 给标签列」的补丁；列距 16→12） */
  --label-w: 216px;
  --row-gap: 12px;
}

[data-theme='dark'] {
  --bg: #12141c;
  --panel: #1b1e2b;
  --panel-2: #151823;
  --border: #2a2e40;
  --border-strong: #3c425c;
  --text: #e8eaf2;
  --text-2: #9aa1b5;
  --text-3: #6f7690;
  --accent: #6d95ff;
  --accent-hover: #85a6ff;
  --accent-weak: #232842;
  /* 深色实底强调色上不用白字：#6d95ff 上白字对比约 2.4:1，近黑约 5:1 */
  --on-accent: #0f1730;
  --ring: rgba(109, 149, 255, 0.25);
  --success: #3cb374;   --success-fg: #4cc47f;  --success-weak: #17301f;
  --warn: #e3a63d;      --warn-fg: #e3a63d;     --warn-weak: #322712;
  --danger: #f16a6f;    --danger-fg: #f16a6f;   --danger-weak: #3b2226;
  --danger-active: #d95c61;
  --tip-bg: #3a415c;
  --tip-fg: #e8eaf2;
  --overlay: rgba(5, 7, 14, 0.55);
  --sh-1: 0 1px 2px rgba(0, 0, 0, 0.35);
  --sh-2: 0 4px 12px rgba(0, 0, 0, 0.40);
  --sh-3: 0 2px 6px rgba(0, 0, 0, 0.35), 0 8px 24px rgba(0, 0, 0, 0.45);
  --sh-4: 0 4px 12px rgba(0, 0, 0, 0.40), 0 16px 48px rgba(0, 0, 0, 0.55);
  /* 深色浮层分层三件套：比 --border 亮一档的描边 + 顶缘高光，漏一处就糊成一团 */
  --float-border: #343a52;
  --float-inset: inset 0 1px 0 rgba(255, 255, 255, 0.05);
}"""))

# --- B. 标题栏：安静化 ---
P1.append((
""".tb-title { font-size: 13px; font-weight: 700; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; max-width: 260px; }""",
""".tb-title { font-size: 12px; font-weight: 600; color: var(--text-2); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; max-width: 260px; }"""))
P1.append((
"""  gap: 9px;
  padding-left: 12px;
  background: var(--panel);
  border-bottom: 1px solid var(--border);
}""",
"""  gap: 9px;
  padding-left: 12px;
  background: transparent;
  border-bottom: 1px solid var(--border);
}"""))
P1.append((
"""#titlebar #wb-close:active { background: #c93b3f; color: #ffffff; }""",
"""#titlebar #wb-close:active { background: var(--danger-active); color: #ffffff; }"""))

# --- C. 侧栏：宽度 240→224，导航换新语言 ---
P1.append((
"""#sidebar {
  width: 240px;""",
"""#sidebar {
  width: 224px;"""))
P1.append((
""".nav-item {
  display: flex; align-items: center; gap: 10px;
  padding: 10px 12px;
  border: none; background: transparent;
  border-radius: 10px;
  font-size: 14px; color: var(--text);
  cursor: pointer; text-align: left;
}
.nav-item:hover { background: var(--accent-weak); }
.nav-item.active { background: var(--accent); color: #fff; }
.nav-item .icon { font-size: 16px; }""",
""".nav-item {
  display: flex; align-items: center; gap: 10px;
  padding: 10px 12px;
  border: none; background: transparent;
  border-radius: var(--r-md);
  font-size: 14px; font-weight: 500; color: var(--text-2);
  cursor: pointer; text-align: left;
  transition: background 0.12s ease, color 0.12s ease;
}
.nav-item:hover { background: var(--panel-2); color: var(--text); }
.nav-item.active { background: var(--accent-weak); color: var(--accent); }
.nav-item .icon { display: inline-flex; flex: none; }
.nav-item .icon svg { width: 17px; height: 17px; display: block; }"""))
P1.append((
""".sidebar-footer .status-cell { background: var(--bg); border-radius: 8px; padding: 6px 10px; }""",
""".sidebar-footer .status-cell { background: var(--panel-2); border-radius: var(--r-sm); padding: 6px 10px; }"""))

# --- D. 按钮主色走 on-accent / hover 走 accent-hover ---
P1.append((
""".btn.primary { background: var(--accent); border-color: var(--accent); color: #fff; font-weight: 600; }
.btn.primary:hover { filter: brightness(1.08); color: #fff; }""",
""".btn.primary { background: var(--accent); border-color: var(--accent); color: var(--on-accent); font-weight: 600; }
.btn.primary:hover { background: var(--accent-hover); border-color: var(--accent-hover); color: var(--on-accent); }"""))

# --- E. 卡片几何：内边距 16×12、卡头 32 ---
P1.append((
""".card {
  background: var(--panel);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  padding: 18px 20px;
  margin-bottom: 16px;
}""",
""".card {
  background: var(--panel);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  padding: 12px 16px;
  margin-bottom: 16px;
}"""))
P1.append((
""".card-head { display: flex; align-items: center; gap: 8px; height: 26px; margin-bottom: 16px; }""",
""".card-head { display: flex; align-items: center; gap: 8px; height: 32px; margin-bottom: 16px; }"""))
P1.append((
""".tl-head { display: flex; align-items: center; gap: 8px; width: 100%; height: 26px; background: none; border: none; padding: 0; font: inherit; font-weight: 600; font-size: 14px; color: var(--text-2); cursor: pointer; text-align: left; }""",
""".tl-head { display: flex; align-items: center; gap: 8px; width: 100%; height: 32px; background: none; border: none; padding: 0; font: inherit; font-weight: 600; font-size: 14px; color: var(--text-2); cursor: pointer; text-align: left; }"""))
P1.append((
"""#tab-timing .card { padding-left: calc(20px + var(--body-indent)); }""",
"""#tab-timing .card { padding-left: calc(16px + var(--body-indent)); }"""))
P1.append((
"""  border-color: transparent;
  background: rgba(128, 128, 128, 0.12);
  color: var(--text);
}""",
"""  border-color: transparent;
  background: var(--panel-2);
  color: var(--text);
}"""))
P1.append((
""".card-head:hover .card-fold, .tl-head:hover .card-fold { background: rgba(128, 128, 128, 0.18); color: var(--text); }""",
""".card-head:hover .card-fold, .tl-head:hover .card-fold { background: var(--panel-2); color: var(--text); }"""))

# --- F. 卡头开关：off 轨道 panel-2 + 加强描边，拇指用 --panel ---
P1.append((
"""  background: color-mix(in srgb, var(--text-2) 26%, var(--panel));
  border: 1px solid var(--border);
  transition: background 0.18s ease, border-color 0.18s ease;""",
"""  background: var(--panel-2);
  border: 1px solid var(--border-strong);
  transition: background 0.18s ease, border-color 0.18s ease;"""))
P1.append((
"""  transform: translateX(14px); background: #fff;""",
"""  transform: translateX(14px); background: var(--panel);"""))

# --- G. 度量行：废除 13px 标签列补丁（标签列 216 后不再需要） ---
P1.append((
"""/* 这句话（“只在空闲时提醒，空闲时长”，14px）自然宽度 205px，比 200px 的标签列宽 5px。
   **不能靠 nowrap 让它溢出去**：DPI 探针把“任何元素 scrollWidth > clientWidth”都记作横向裁切
   （它正是当年 `.pg` 那个 -13px 负外边距被抓出来的同一条判据），一溢就是 4 个宽度全红。
   正解是把这道沟里的 13px 借给标签列：213 + 3 = 200 + 16，**两者之和不变**，
   控件列仍然一格不差地落在 509（探针钉着“与「提醒时长」左缘同列”）。
   不去动 --label-w：它是全卡共用的，为一个长句子改它，别处的控件列会整体右移。
   选择器要写足两级（.row.field.idle-gate）：`.row.field` 的 grid-template-columns 是 (0,2,0)，
   只写 .idle-gate 会被它压住 —— 实测那样只剩 column-gap 生效，控件列反而左移 13px。 */
.row.field.idle-gate {
  grid-template-columns: calc(var(--label-w) + 13px) minmax(0, 1fr);
  column-gap: calc(var(--row-gap) - 13px);
}""",
"""/* 「只在空闲时提醒，空闲时长」（14px，自然宽约 205px）在 216px 标签列里放得下：
   2026-11-13 标签列 200→216 后，原「借 13px 给标签列」的补丁整段删除，
   本行控件列与其他字段行同列。 */"""))

# --- H. 组内行高统一 40 ---
P1.append((
"""/* 组内行比卡片其它地方紧一档（40 → 36）：一组的几行读起来要像一段，
   而不是几根各自独立的行。卡片外的行（督休专员、统计页）保持 40 的宽松节奏。 */
.pg .row { min-height: 36px; padding: 3px 0; }""",
"""/* 组内行与卡片其它行统一 40（2026-11-13 规格：40/36 双节奏是意外产物不是层级），
   一组的从属关系靠组标题与组间虚线表达，不再靠行高差。 */
.pg .row { min-height: 40px; padding: 4px 0; }"""))

# --- I. 组标题字重 600 封顶（slot-title） ---
P1.append((
"""  font-size: 12px; font-weight: 700; letter-spacing: .04em; color: var(--text-2);""",
"""  font-size: 12px; font-weight: 600; letter-spacing: .04em; color: var(--text-2);"""))

# --- J. 基础输入框：凹底 + 透明描边 + hover 描边 + focus 环 ---
P1.append((
"""input[type='number'], input[type='text'], input[type='time'], select {
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: 8px;
  color: var(--text);
  padding: 7px 10px;
  font-size: 13px;
  width: 180px;
}""",
"""input[type='number'], input[type='text'], input[type='time'], select {
  background: var(--panel-2);
  border: 1px solid transparent;
  border-radius: var(--r-md);
  color: var(--text);
  padding: 7px 10px;
  font-size: 14px;
  width: 180px;
}
input[type='number']:hover, input[type='text']:hover,
input[type='time']:hover, select:hover { border-color: var(--border-strong); }"""))
P1.append((
"""input[type='date'] {
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: 8px;
  color: var(--text);
  padding: 6px 10px;
  font-size: 13px;""",
"""input[type='date'] {
  background: var(--panel-2);
  border: 1px solid transparent;
  border-radius: var(--r-md);
  color: var(--text);
  padding: 6px 10px;
  font-size: 14px;"""))
P1.append((
"""input[type='date']:hover { border-color: var(--accent); }""",
"""input[type='date']:hover { border-color: var(--border-strong); }"""))
P1.append((
"""  outline: none;
  border-color: var(--accent);
  box-shadow: 0 0 0 2px var(--accent-weak);
}""",
"""  outline: none;
  border-color: var(--accent);
  box-shadow: 0 0 0 3px var(--ring);
}"""))
P1.append((
""".modal-input:focus { outline: none; border-color: var(--accent); }""",
""".modal-input:focus { outline: none; border-color: var(--accent); box-shadow: 0 0 0 3px var(--ring); }"""))
# 时刻表的 time 框保持 13px：68px 宽度预算按 13px 等宽数字核算
P1.append((
"""  width: 68px; padding: 6px 18px 6px 8px; text-align: left;
  font-variant-numeric: tabular-nums;
  color-scheme: light;""",
"""  width: 68px; padding: 6px 18px 6px 8px; text-align: left;
  font-size: 13px; /* 68px 宽度预算按 13px 等宽数字核算，随全局归并到 14 会顶破 */
  font-variant-numeric: tabular-nums;
  color-scheme: light;"""))

# --- K. 自绘下拉：凹底触发器 + 浮层令牌 ---
P1.append((
""".cdrop-btn {
  display: flex; align-items: center; gap: 6px;
  width: 100%; box-sizing: border-box;
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: 8px;
  color: var(--text);
  padding: 7px 10px;
  font-size: 13px; font-family: inherit; text-align: left;
  cursor: pointer;
}
.cdrop-btn:hover { border-color: var(--accent); }
.cdrop.open .cdrop-btn { border-color: var(--accent); box-shadow: 0 0 0 2px var(--accent-weak); }""",
""".cdrop-btn {
  display: flex; align-items: center; gap: 6px;
  width: 100%; box-sizing: border-box;
  background: var(--panel-2);
  border: 1px solid transparent;
  border-radius: var(--r-md);
  color: var(--text);
  padding: 7px 10px;
  font-size: 14px; font-family: inherit; text-align: left;
  cursor: pointer;
}
.cdrop-btn:hover { border-color: var(--border-strong); }
.cdrop.open .cdrop-btn { border-color: var(--accent); box-shadow: 0 0 0 3px var(--ring); }"""))
P1.append((
"""  border-radius: 8px;
  box-shadow: 0 10px 26px rgba(0, 0, 0, 0.22);
  padding: 4px;""",
"""  border-radius: var(--r-md);
  box-shadow: var(--sh-3);
  padding: 4px;"""))
P1.append((
""".cdrop-menu.hidden { display: none; }""",
""".cdrop-menu.hidden { display: none; }
[data-theme='dark'] .cdrop-menu { border-color: var(--float-border); box-shadow: var(--sh-3), var(--float-inset); }"""))
P1.append((
""".cdrop-item:hover { background: var(--bg); }
.cdrop-item:focus-visible { background: var(--bg); outline: 2px solid var(--accent); outline-offset: -2px; }""",
""".cdrop-item:hover { background: var(--panel-2); }
.cdrop-item:focus-visible { background: var(--panel-2); outline: 2px solid var(--accent); outline-offset: -2px; }"""))
P1.append((
"""  border: 1.5px solid var(--border); border-radius: 4px; background: var(--bg);""",
"""  border: 1.5px solid var(--border); border-radius: 4px; background: var(--panel-2);"""))

# --- L. tooltip 反色（? 浮层 + 图表提示同族） ---
P1.append((
"""  padding: 9px 11px;
  border-radius: 8px;
  border: 1px solid color-mix(in srgb, var(--text) 12%, var(--border));
  background: var(--panel);
  box-shadow: 0 10px 26px rgba(0, 0, 0, 0.18);
  color: var(--text);""",
"""  padding: 9px 11px;
  border-radius: var(--r-sm);
  border: 0;
  background: var(--tip-bg);
  box-shadow: var(--sh-2);
  color: var(--tip-fg);"""))
P1.append((
"""/* 深色下背景提亮一档：这里 --panel 与卡片同色，不提亮就只能靠边框分辨 */
[data-theme='dark'] .q-tip {
  background: color-mix(in srgb, var(--text) 7%, var(--panel));
  border-color: color-mix(in srgb, var(--text) 16%, var(--border));
  box-shadow: 0 12px 30px rgba(0, 0, 0, 0.55);
}
""",
""""""))
P1.append((
"""  background: rgba(22, 24, 32, 0.92); color: #fff;
  border: 1px solid rgba(255, 255, 255, 0.25); border-radius: 8px;
  padding: 7px 10px; font-size: 12px; line-height: 1.7;
  box-shadow: 0 4px 14px rgba(0, 0, 0, 0.25); white-space: nowrap;""",
"""  background: var(--tip-bg); color: var(--tip-fg);
  border: 0; border-radius: var(--r-sm);
  padding: 7px 10px; font-size: 12px; line-height: 1.7;
  box-shadow: var(--sh-2); white-space: nowrap;"""))
P1.append((
""".rc-tip .rc-tip-d { color: rgba(255, 255, 255, 0.72); }""",
""".rc-tip .rc-tip-d { color: color-mix(in srgb, var(--tip-fg) 72%, transparent); }"""))
P1.append((
""".rc-tip .ckv-0 { color: rgba(255, 255, 255, 0.45); }""",
""".rc-tip .ckv-0 { color: color-mix(in srgb, var(--tip-fg) 45%, transparent); }"""))

# --- M. 时刻选择面板 / 各浮层阴影走令牌 + 深色补浮层描边 ---
P1.append((
"""  border-radius: 10px;
  box-shadow: 0 14px 34px rgba(0, 0, 0, .26);
  user-select: none;
}""",
"""  border-radius: var(--r-md);
  box-shadow: var(--sh-3);
  user-select: none;
}"""))
P1.append((
"""[data-theme='dark'] .tp { box-shadow: 0 16px 40px rgba(0, 0, 0, .58); }""",
"""[data-theme='dark'] .tp { border-color: var(--float-border); box-shadow: var(--sh-3), var(--float-inset); }"""))
P1.append((
"""  box-shadow: 0 6px 18px rgba(0, 0, 0, 0.18);""",
"""  box-shadow: var(--sh-3);"""))
P1.append((
"""  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.14); padding: 10px 12px; min-width: 320px;""",
"""  box-shadow: var(--sh-3); padding: 10px 12px; min-width: 320px;"""))
P1.append((
"""  box-shadow: 0 8px 28px rgba(0, 0, 0, 0.18);""",
"""  box-shadow: var(--sh-3);"""))

# --- N. 弹窗：overlay 令牌 + sh-4 + 深色浮层描边 ---
P1.append((
"""  background: rgba(0, 0, 0, 0.45);
  display: flex; align-items: center; justify-content: center;""",
"""  background: var(--overlay);
  display: flex; align-items: center; justify-content: center;"""))
P1.append((
""".modal-box {
  width: 460px; max-width: calc(100vw - 60px);
  background: var(--panel); border: 1px solid var(--border);
  border-radius: 14px; padding: 18px;
  box-shadow: 0 12px 40px rgba(0, 0, 0, 0.25);
}""",
""".modal-box {
  width: 460px; max-width: calc(100vw - 60px);
  background: var(--panel); border: 1px solid var(--border);
  border-radius: var(--r-lg); padding: 18px;
  box-shadow: var(--sh-4);
}
[data-theme='dark'] .modal-box { border-color: var(--float-border); box-shadow: var(--sh-4), var(--float-inset); }"""))
P1.append((
"""  border: 1px solid var(--border); border-radius: 10px;
  background: var(--bg); color: var(--text);
  padding: 10px; font: inherit; line-height: 1.6;""",
"""  border: 1px solid transparent; border-radius: var(--r-md);
  background: var(--panel-2); color: var(--text);
  padding: 10px; font: inherit; line-height: 1.6;
}
.modal-box textarea:hover { border-color: var(--border-strong); }
.modal-box textarea:focus { outline: none; border-color: var(--accent); box-shadow: 0 0 0 3px var(--ring);"""))

# --- O. pill 四态走令牌 ---
P1.append((
"""#tab-about .pill.ok { background: rgba(28, 194, 127, .15); color: #0f9d6b; }
#tab-about .pill.ok i { background: #1cc27f; box-shadow: 0 0 0 3px rgba(28, 194, 127, .22); }
#tab-about .pill.warn { background: rgba(245, 166, 35, .18); color: #b9760a; }
#tab-about .pill.warn i { background: #f5a623; box-shadow: 0 0 0 3px rgba(245, 166, 35, .24); }
#tab-about .pill.err { background: var(--danger-weak); color: var(--danger); }
#tab-about .pill.err i { background: var(--danger); box-shadow: 0 0 0 3px rgba(229, 72, 77, .22); }
#tab-about .pill.local { background: rgba(91, 127, 232, .15); color: #4a6bd6; }
#tab-about .pill.local i { background: #5b7fe8; box-shadow: 0 0 0 3px rgba(91, 127, 232, .18); }""",
"""#tab-about .pill.ok { background: var(--success-weak); color: var(--success-fg); }
#tab-about .pill.ok i { background: var(--success); box-shadow: 0 0 0 3px var(--success-weak); }
#tab-about .pill.warn { background: var(--warn-weak); color: var(--warn-fg); }
#tab-about .pill.warn i { background: var(--warn); box-shadow: 0 0 0 3px var(--warn-weak); }
#tab-about .pill.err { background: var(--danger-weak); color: var(--danger-fg); }
#tab-about .pill.err i { background: var(--danger); box-shadow: 0 0 0 3px var(--danger-weak); }
#tab-about .pill.local { background: var(--panel-2); color: var(--text-2); border: 1px solid var(--border-strong); }
#tab-about .pill.local i { background: var(--text-3); box-shadow: none; }"""))

# --- P. 实底强调色上的文字统一 on-accent ---
P1.append((
""".tk-day.on { background: var(--accent); border-color: var(--accent); color: #fff; }""",
""".tk-day.on { background: var(--accent); border-color: var(--accent); color: var(--on-accent); }"""))
P1.append((
""".role-menu-item:hover { background: var(--accent); color: #fff; }""",
""".role-menu-item:hover { background: var(--accent); color: var(--on-accent); }"""))
P1.append((
""".rc-tool.on { background: var(--accent); border-color: var(--accent); color: #fff; }""",
""".rc-tool.on { background: var(--accent); border-color: var(--accent); color: var(--on-accent); }"""))
P1.append((
""".rc-unit.on { background: var(--accent); color: #fff; }""",
""".rc-unit.on { background: var(--accent); color: var(--on-accent); }"""))
P1.append((
"""  content: ''; position: absolute; left: 2px; top: 3px; width: 6px; height: 3px;
  border-left: 1.8px solid #fff; border-bottom: 1.8px solid #fff;
  transform: rotate(-45deg);""",
"""  content: ''; position: absolute; left: 2px; top: 3px; width: 6px; height: 3px;
  border-left: 1.8px solid var(--on-accent); border-bottom: 1.8px solid var(--on-accent);
  transform: rotate(-45deg);"""))

# --- Q. toast 面板化 ---
P1.append((
"""#toast {
  position: fixed; bottom: 26px; left: 50%; transform: translateX(-50%);
  background: #1f2430; color: #fff; padding: 10px 22px;
  border-radius: 999px; font-size: 13px; z-index: 99;
  box-shadow: 0 8px 24px rgba(0,0,0,.18);
}""",
"""#toast {
  position: fixed; bottom: 26px; left: 50%; transform: translateX(-50%);
  background: var(--panel); color: var(--text); padding: 10px 18px;
  border: 1px solid var(--border-strong); border-radius: var(--r-md);
  font-size: 13px; z-index: 99;
  box-shadow: var(--sh-3);
}"""))

# --- R. 杂项令牌化 ---
P1.append((
""".cf-icon.prompt { background: rgba(79, 124, 255, 0.15); color: var(--accent); }""",
""".cf-icon.prompt { background: var(--accent-weak); color: var(--accent); }"""))
P1.append((
""".comp-note.over { color: #e05555; }""",
""".comp-note.over { color: var(--danger-fg); }"""))
P1.append((
"""  margin: 10px 0 0; padding: 6px;
  background: var(--bg); border: 1px solid var(--border); border-radius: 10px;""",
"""  margin: 10px 0 0; padding: 6px;
  background: var(--panel-2); border: 1px solid var(--border); border-radius: var(--r-md);"""))
# cf-detail（三行格式，等宽块底色走凹面）
P1.append((
"""  background: var(--bg);
  border: 1px solid var(--border); border-radius: 10px;
  color: var(--text); font-size: 13px; line-height: 1.5;""",
"""  background: var(--panel-2);
  border: 1px solid var(--border); border-radius: var(--r-md);
  color: var(--text); font-size: 13px; line-height: 1.5;"""))
# nt-body 与 up-changes 共用的单行内嵌面（出现 2 次，全部替换）
P1.append((
"""background: var(--bg); border: 1px solid var(--border); border-radius: 10px;""",
"""background: var(--panel-2); border: 1px solid var(--border); border-radius: var(--r-md);"""))

apply(CSS, P1, "CSS 阶段①（原文精确替换）", strict=True)

# ============================================================ CSS · 阶段②：全局归并（无需断言）
P2 = [
    # 圆角阶梯归并
    ("border-radius: 999px;", "border-radius: var(--r-pill);"),
    ("border-radius: 14px;", "border-radius: var(--r-lg);"),
    ("border-radius: 12px;", "border-radius: var(--r-lg);"),
    ("border-radius: 10px;", "border-radius: var(--r-md);"),
    ("border-radius: 9px;", "border-radius: var(--r-md);"),
    ("border-radius: 8px;", "border-radius: var(--r-md);"),
    ("border-radius: 7px;", "border-radius: var(--r-sm);"),
    ("border-radius: 6px;", "border-radius: var(--r-sm);"),
    ("border-radius: 5px;", "border-radius: var(--r-sm);"),
    # 字号归并（例外在阶段③恢复）
    ("font-size: 12.5px;", "font-size: 12px;"),
    ("font-size: 11.5px;", "font-size: 11px;"),
    ("font-size: 13px;", "font-size: 14px;"),
]
apply(CSS, P2, "CSS 阶段②（全局归并）", strict=False)

# ============================================================ CSS · 阶段③：恢复例外（断言）
P3 = [
    # .btn 保持 13px（卡头 22px 动作按钮的行盒依赖它）
    (("  padding: 8px 14px;\n  font-size: 14px;"),
     ("  padding: 8px 14px;\n  font-size: 13px;")),
    # .cf-detail 等宽块保持 13px
    (("  color: var(--text); font-size: 14px; line-height: 1.5;"),
     ("  color: var(--text); font-size: 13px; line-height: 1.5;")),
    # 侧栏状态格数值保持 13px
    ((".sidebar-footer .status-cell .v { font-size: 14px; margin-top: 2px; }"),
     (".sidebar-footer .status-cell .v { font-size: 13px; margin-top: 2px; }")),
    # .pg-title 字重 600 封顶
    (("  font-size: 14px; font-weight: 700; line-height: 1.35;"),
     ("  font-size: 14px; font-weight: 600; line-height: 1.35;")),
]
apply(CSS, P3, "CSS 阶段③（恢复例外）", strict=True)

# ============================================================ HTML：侧栏 emoji → 统一描边 SVG
SVG_PRE = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">'
GEAR = SVG_PRE + '<circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 1 1-4 0v-.09a1.65 1.65 0 0 0-1-1.51 1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 1 1 0-4h.09a1.65 1.65 0 0 0 1.51-1 1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06a1.65 1.65 0 0 0 1.82.33h.09a1.65 1.65 0 0 0 1-1.51V3a2 2 0 1 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82v.09a1.65 1.65 0 0 0 1.51 1H21a2 2 0 1 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z"/></svg>'
CLOCK = SVG_PRE + '<circle cx="12" cy="12" r="8.5"/><path d="M12 7.5V12l3 2"/></svg>'
SHIELD = SVG_PRE + '<path d="M12 3l7 2.8v5.2c0 4.5-2.9 8-7 10-4.1-2-7-5.5-7-10V5.8L12 3z"/><path d="M9.3 12.2l1.9 1.9 3.5-3.9"/></svg>'
CHART = SVG_PRE + '<path d="M4.5 20h15"/><path d="M7.5 20v-5.5"/><path d="M12 20V9.5"/><path d="M16.5 20v-15"/></svg>'
KEYS = SVG_PRE + '<rect x="2.5" y="6.5" width="19" height="11" rx="2"/><path d="M6 10h.01M9.5 10h.01M13 10h.01M16.5 10h.01"/><path d="M6 13h.01M9.5 13h.01M13 13h.01M16.5 13h.01"/><path d="M9 16h6"/></svg>'
INFO = SVG_PRE + '<circle cx="12" cy="12" r="8.5"/><path d="M12 11v5"/><path d="M12 8h.01"/></svg>'

html_pairs = [
    ('<span class="icon">⚙️</span>通用配置', '<span class="icon">' + GEAR + '</span>通用配置'),
    ('<span class="icon">⏱️</span>时间规划', '<span class="icon">' + CLOCK + '</span>时间规划'),
    ('<span class="icon">🕵️</span>督休专员', '<span class="icon">' + SHIELD + '</span>督休专员'),
    ('<span class="icon">📊</span>统计报表', '<span class="icon">' + CHART + '</span>统计报表'),
    ('<span class="icon">⌨️</span>快捷键', '<span class="icon">' + KEYS + '</span>快捷键'),
    ('<span class="icon">🗂️</span>关于', '<span class="icon">' + INFO + '</span>关于'),
]
apply(HTML, html_pairs, "HTML（导航图标）", strict=True)
print("全部完成")
