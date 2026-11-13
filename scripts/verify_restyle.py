# -*- coding: utf-8 -*-
"""重设计落地后的完整性验证。"""
import io, re, sys

CSS = r"D:\Projects\personal\WorkerRest\cmd\workerrest\web\dist\settings.css"
HTML = r"D:\Projects\personal\WorkerRest\cmd\workerrest\web\dist\settings.html"

ok = True

with io.open(CSS, encoding="utf-8") as f:
    css = f.read()

# 1. 花括号配平
opens, closes = css.count("{"), css.count("}")
print("花括号: { =", opens, ", } =", closes, "→", "OK" if opens == closes else "FAIL")
ok &= opens == closes

# 2. 令牌定义与引用
defined = set(re.findall(r"(--[a-z0-9-]+)\s*:", css))
used = set(re.findall(r"var\(\s*(--[a-z0-9-]+)", css))
missing = sorted(used - defined)
unused = sorted(defined - used - {"--radius"})
print("已定义令牌:", len(defined), "，被引用:", len(used))
if missing:
    print("未定义却被引用:", missing); ok = False
else:
    print("引用完整性: OK")

# 3. 残留的旧硬编码阴影（rgba 黑影 box-shadow，排除语义保留位）
leftover = [l for l in css.splitlines()
            if re.search(r"box-shadow:[^;]*rgba\(0,\s*0,\s*0", l)
            and not re.search(r"inset|role|sw-|chip|thumb|pad|hue|preview", l)]
print("残留黑色 box-shadow:", len(leftover))
for l in leftover:
    print("   ", l.strip()[:120])

# 4. emoji 是否清干净
with io.open(HTML, encoding="utf-8") as f:
    html = f.read()
emoji = re.findall(r"[\U0001F000-\U0001FAFF\u2600-\u27BF]", html)
print("HTML 残留 emoji:", emoji if emoji else "无 → OK")
ok &= not emoji

# 5. 半档字号是否清干净
half = re.findall(r"font-size:\s*(?:11\.5|12\.5)px", css)
print("残留半档字号:", len(half), "→", "OK" if not half else "FAIL")
ok &= not half

# 6. 统计行数
print("settings.css 行数:", css.count("\n") + 1)
sys.exit(0 if ok else 1)
