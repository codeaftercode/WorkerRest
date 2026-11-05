#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""静态资产审计：七个页面 + 全部前端脚本/样式的「引号级」完整性。

为什么单独一层：无头/真机探针只能在**跑起来之后**发现问题，而漏引一个脚本、
重复一个 id、CSP 拦掉一段内联脚本，往往表现为「某个功能悄悄不工作」而不是报错。
这一层不需要浏览器、几秒钟跑完，适合每次改前端都过一遍。

检查项：
  [1] 每个页面引用的本地资源（脚本 / 样式 / 图片 / 字体）都存在
  [2] 页面里没有内联脚本（CSP `default-src 'self'` 会静默拦掉，2026-10-25 踩过）
  [3] 同一页面内 id 不重复
  [4] 每个页面有 lang 与 <title>（可访问性与窗口标题）
  [5] 每个 JS 通过 `node --check`（语法错误在浏览器里只报一行，容易漏）
  [6] CSS 里 url(...) 引用的本地文件存在
  [7] JS 里 getElementById / $("x") 引用的 id，本页 html 或脚本自建里确实存在
  [8] 页面引用的脚本文件集合与 EXPECTED_SCRIPTS 一致（防止漏引 / 多引死文件）

退出码 0 = 全部通过。
"""

import os
import re
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(os.path.dirname(HERE))
DIST = os.path.join(ROOT, "cmd", "workerrest", "web", "dist")

PAGES = {
    "index.html": ["shim.js", "index.js"],
    "settings.html": ["shim.js", "role_thumb.js", "settings.js"],
    "break.html": ["nozoom.js", "break-early.js", "shim.js", "break.js"],
    "prebreak.html": ["nozoom.js", "shim.js", "prebreak.js"],
    "pause.html": ["nozoom.js", "shim.js", "pause.js"],
    "takeout.html": ["nozoom.js", "shim.js", "takeout.js"],
    "sound.html": [],
}

checks = 0
failures = []


def check(ok, label, detail=""):
    global checks
    checks += 1
    if ok:
        print("  ok   %s%s" % (label, ("  | " + str(detail)) if detail else ""))
    else:
        print("  FAIL %s%s" % (label, ("  | " + str(detail)) if detail else ""))
        failures.append(label)
    return ok


def read(p):
    with open(p, encoding="utf-8", errors="replace") as f:
        return f.read()


def local_refs(html):
    """页面里引用到的本地资源路径（相对 dist）"""
    out = []
    for m in re.finditer(r'(?:src|href)\s*=\s*"([^"]+)"', html):
        u = m.group(1).strip()
        if not u or u.startswith(("http://", "https://", "data:", "#", "mailto:", "javascript:")):
            continue
        out.append(u.lstrip("./"))
    return out


def ids_of(html):
    return re.findall(r'\bid\s*=\s*"([^"]+)"', html)


def audit_pages():
    print("\n[1-4] 页面资产、内联脚本、id 唯一性、lang/title")
    for name in PAGES:
        p = os.path.join(DIST, name)
        if not check(os.path.exists(p), "%s 存在" % name):
            continue
        html = read(p)

        missing = [u for u in local_refs(html) if not os.path.exists(os.path.join(DIST, u.split("?")[0]))]
        check(not missing, "%s 引用的本地资源都存在" % name, missing or "全部命中")

        inline = re.findall(r"<script(?![^>]*\bsrc=)[^>]*>", html, re.I)
        check(not inline, "%s 没有内联脚本（CSP 会静默拦掉）" % name, inline or "无")

        ids = ids_of(html)
        dup = sorted({i for i in ids if ids.count(i) > 1})
        check(not dup, "%s 的 id 不重复" % name, dup or "%d 个 id" % len(ids))

        has_lang = re.search(r"<html[^>]*\blang=", html, re.I) is not None
        has_title = re.search(r"<title>[^<]*</title>", html, re.I) is not None
        check(has_lang and has_title, "%s 有 lang 与 title" % name,
              "lang=%s title=%s" % (has_lang, has_title))


def audit_js_syntax():
    print("\n[5] 前端脚本语法（node --check）")
    js = sorted(f for f in os.listdir(DIST) if f.endswith(".js"))
    bad = []
    for f in js:
        r = subprocess.run(["node", "--check", os.path.join(DIST, f)],
                           capture_output=True, text=True, encoding="utf-8", errors="replace")
        if r.returncode != 0:
            bad.append((f, (r.stderr or "").strip().splitlines()[:1]))
    check(not bad, "%d 个脚本语法都能解析" % len(js), bad or "")


def audit_css_refs():
    print("\n[6] 样式里 url(...) 引用的本地文件")
    css = sorted(f for f in os.listdir(DIST) if f.endswith(".css"))
    missing = []
    for f in css:
        text = read(os.path.join(DIST, f))
        for m in re.finditer(r"url\(\s*['\"]?([^'\")]+)['\"]?\s*\)", text):
            u = m.group(1).strip()
            if u.startswith(("data:", "http", "#")):
                continue
            if not os.path.exists(os.path.join(DIST, u.lstrip("./").split("?")[0])):
                missing.append((f, u))
    check(not missing, "%d 个样式表里的引用都存在" % len(css), missing or "")


# JS 里动态创建 id 的三种写法（自建元素不进 html）
DYNAMIC_ID_RE = [
    r'\.id\s*=\s*"([^"]+)"',
    r'\.id\s*=\s*`([^`$]+)`',
    r'id\s*=\s*"([^"$]+)"',
]


def dynamic_ids(js_text):
    out = set()
    for pat in DYNAMIC_ID_RE:
        out.update(re.findall(pat, js_text))
    return out


def audit_id_refs():
    print("\n[7] JS 引用的 id 是否真的存在")
    # 全局兜底：任何脚本里动态创建的 id 都算存在（同源页面之间会互相借元素）
    all_js = ""
    for f in sorted(x for x in os.listdir(DIST) if x.endswith(".js")):
        all_js += read(os.path.join(DIST, f))
    dyn = dynamic_ids(all_js)

    for name, scripts in PAGES.items():
        p = os.path.join(DIST, name)
        if not os.path.exists(p):
            continue
        html = read(p)
        have = set(ids_of(html)) | dyn
        refs = set()
        for s in scripts:
            sp = os.path.join(DIST, s)
            if not os.path.exists(sp):
                continue
            text = read(sp)
            refs.update(re.findall(r'getElementById\(\s*"([^"]+)"\s*\)', text))
            # 本项目统一用 const $ = (id) => document.getElementById(id)
            refs.update(re.findall(r'\$\(\s*"([A-Za-z][\w-]*)"\s*\)', text))
        missing = sorted(r for r in refs if r not in have)
        check(not missing, "%s 的脚本引用的 id 都存在" % name,
              missing or "%d 个引用" % len(refs))


def audit_script_sets():
    print("\n[8] 页面引用的脚本集合与预期一致")
    for name, expected in PAGES.items():
        p = os.path.join(DIST, name)
        if not os.path.exists(p):
            continue
        html = read(p)
        got = [m.group(1).lstrip("./") for m in re.finditer(r'<script[^>]*\bsrc="([^"]+)"', html, re.I)]
        check(got == expected, "%s 的脚本顺序/集合正确" % name, got)
        unused = [s for s in os.listdir(DIST) if s.endswith(".js") and s not in
                  sum(PAGES.values(), []) + ["role_thumb.js"]]
        if unused:  # 只在最后一个页面报一次
            pass
    # 反过来：dist 里的 .js 应该都被某个页面引用（role_thumb.js 只被渲染层动态加载）
    used = set(sum(PAGES.values(), [])) | {"role_thumb.js"}
    orphans = sorted(f for f in os.listdir(DIST) if f.endswith(".js") and f not in used)
    check(not orphans, "dist 里没有没人引用的脚本", orphans or "")


def main():
    if not os.path.isdir(DIST):
        print("找不到 %s" % DIST)
        return 2
    audit_pages()
    audit_js_syntax()
    audit_css_refs()
    audit_id_refs()
    audit_script_sets()

    print("\n结果：%d 项断言，失败 %d 项" % (checks, len(failures)))
    for f in failures:
        print("  - " + f)
    return 0 if not failures else 1


if __name__ == "__main__":
    try:
        sys.stdout.reconfigure(encoding="utf-8", errors="replace")
    except Exception:
        pass
    sys.exit(main())
