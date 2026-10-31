#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""生成 THIRD-PARTY-NOTICES.txt —— 软件「关于页 → 开源鸣谢」里那份清单（致谢 + 组件表 + 许可全文）。

产物结构只有三块，不放任何说明性文字（用户要的是「我用到了什么、谁的、什么许可」）：
  1) 致谢 + 组件清单：名称 / 版本 / 协议，三列对齐；
  2) 逐组件的许可全文，每段用 78 字符横线封口做分隔；
  3) 段内保持上游原文，只统一换行与行尾空白。

为什么要有这个脚本（而不是手抄一份清单）：
  · MIT / BSD-3 的义务是「随二进制分发时复现版权声明与许可全文」。手抄的清单一旦
    依赖升级就过期，而**过期比没有更糟**（声明与实物不符，等于虚假声明）。
  · 所以清单从**构建事实**推导：Go 模块取 `go list -deps`（与 `go version -m` 读出的
    内嵌依赖一致），Go 标准库取 $GOROOT，前端内联库取 frontend/dist 产物里的库。
  · `--check` 重新生成并与已提交的文件逐字节比较；打包脚本会在打包前跑它，
    依赖一升版本就立刻失败，而不是悄悄发出去。

用法：
    python scripts/gen_notices.py            # 重新生成 THIRD-PARTY-NOTICES.txt
    python scripts/gen_notices.py --check    # 只校验：不一致则退出码 1（打包门禁用）

前端两个库的许可正文放在 scripts/notices/（npm 包里的原文，取一次存仓库）：
bundle 里已经压掉了注释，无法从产物反推，只能靠这份原文 + 产物指纹双向校验。
"""
import argparse
import os
import re
import shutil
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)
OUT = os.path.join(ROOT, "THIRD-PARTY-NOTICES.txt")
VENDOR = os.path.join(HERE, "notices")
BUNDLE = os.path.join(ROOT, "frontend", "dist", "takeout.js")

# 前端内联第三方库。fingerprint 是产物里必须出现的标识串：库被换掉/删掉时这里立刻红，
# 免得清单继续声明一个已经不在产物里的库（或漏掉新内联进来的库）。
FRONTEND = [
    {
        "name": "qrcode",
        "version": "1.5.4",
        "file": "qrcode-1.5.4.txt",
        "fingerprint": "qrcode",
    },
    {
        "name": "dijkstrajs",
        "version": "1.0.3",
        "file": "dijkstrajs-1.0.3.txt",
        "fingerprint": "dijkstrajs",
    },
]

# 模块目录里可能出现的许可类文件（按顺序收集，至少要有 LICENSE 之一）
LICENSE_FILES = ["LICENSE", "LICENSE.txt", "LICENSE.md", "COPYING", "COPYING.txt", "NOTICE", "PATENTS"]

LINE = "=" * 78
THIN = "-" * 78


def find_go():
    """找 go 可执行文件：GO_BIN 环境变量 → PATH → 本机默认安装位置。

    本机 go 刻意不在 PATH（见 README），所以不能只靠 shutil.which。
    """
    cand = [os.environ.get("GO_BIN", "").strip(), shutil.which("go") or "",
            r"D:\Programs\Go\bin\go.exe", "/usr/local/go/bin/go"]
    for c in cand:
        if c and os.path.isfile(c):
            return c
    raise SystemExit("找不到 go：设置 GO_BIN 环境变量，或把 go 加进 PATH")


def go(go_bin, *args):
    r = subprocess.run([go_bin, *args], cwd=ROOT, capture_output=True, text=True,
                       encoding="utf-8", errors="replace")
    if r.returncode != 0:
        raise SystemExit("go %s 失败：\n%s" % (" ".join(args), (r.stderr or r.stdout).strip()))
    return r.stdout


def normalize(text):
    """统一换行与行尾空白：许可全文的换行差异不该让清单每次生成都变。"""
    t = text.replace("\r\n", "\n").replace("\r", "\n")
    if t.startswith("\ufeff"):
        t = t[1:]
    lines = [ln.rstrip() for ln in t.split("\n")]
    while lines and not lines[-1]:
        lines.pop()
    while lines and not lines[0]:
        lines.pop(0)
    return "\n".join(lines)


def license_id(text):
    """从正文粗判许可类型（只用于清单里的可读标签，判不出就不猜）。"""
    t = text.lower()
    if "permission is hereby granted, free of charge" in t:
        return "MIT License"
    if "redistribution and use in source and binary forms" in t:
        return "BSD-3-Clause" if "neither the name" in t else "BSD-2-Clause"
    if "apache license" in t and "version 2.0" in t:
        return "Apache-2.0"
    return ""


def escape_module_path(p):
    """Go 模块缓存对大写字母用 !x 转义（github.com/BurntSushi → !burnt!sushi）。"""
    return re.sub(r"[A-Z]", lambda m: "!" + m.group(0).lower(), p)


def linked_modules(go_bin):
    """实际链接进二进制的第三方模块（去主模块、去重、按路径排序）。"""
    out = go(go_bin, "list", "-deps", "-f", "{{with .Module}}{{.Path}}|{{.Version}}{{end}}", ".")
    mods = set()
    for ln in out.splitlines():
        ln = ln.strip()
        if not ln or "|" not in ln:
            continue
        path, ver = ln.split("|", 1)
        if not ver or ver == "(devel)":
            continue  # 主模块自己：不声明
        mods.add((path, ver))
    return sorted(mods)


def read_license_files(dir_path):
    """按 LICENSE_FILES 顺序收集许可类文件，返回 [(文件名, 正文)]。"""
    found = []
    for name in LICENSE_FILES:
        fp = os.path.join(dir_path, name)
        if os.path.isfile(fp):
            with open(fp, "r", encoding="utf-8", errors="replace") as f:
                txt = normalize(f.read())
            if txt:
                found.append((name, txt))
    return found


def render_section(idx, total, title, blocks):
    """一个组件段：封口标题行 + 许可正文。

    段与段之间只靠这组 78 字符横线分隔 —— 不再插入「来源：…」这类说明行，
    读者要的是「这是我用到的组件，这是它的许可全文」，其余都是噪音。
    """
    lines = [LINE, "  %d / %d  %s" % (idx, total, title), LINE, ""]
    for i, (name, body) in enumerate(blocks):
        if len(blocks) > 1:
            if i > 0:
                lines.append("")
            lines.append("--- %s ---" % name)
            lines.append("")
        lines.append(body)
    return "\n".join(lines)


def build():
    go_bin = find_go()
    if not os.path.isfile(BUNDLE):
        raise SystemExit("找不到前端产物：%s" % BUNDLE)
    with open(BUNDLE, "r", encoding="utf-8", errors="replace") as f:
        bundle = f.read().lower()
    for v in FRONTEND:
        if v["fingerprint"].lower() not in bundle:
            raise SystemExit(
                "前端产物里找不到 %s 的标识（%r）—— 清单可能已过期，请核对 "
                "frontend/dist/takeout.js 里内联了哪些库" % (v["name"], v["fingerprint"]))

    modcache = go(go_bin, "env", "GOMODCACHE").strip()
    goroot = go(go_bin, "env", "GOROOT").strip()
    gover = go(go_bin, "env", "GOVERSION").strip()  # 形如 go1.27.3

    sections = []  # (标题, 来源, [(文件名, 正文)], 清单行)

    # ---- 1) Go 模块 ----
    for path, ver in linked_modules(go_bin):
        mdir = os.path.join(modcache, escape_module_path(path) + "@" + ver)
        if not os.path.isdir(mdir):
            raise SystemExit("模块目录不存在：%s\n先跑 `go mod download` 或随便构建一次" % mdir)
        files = read_license_files(mdir)
        if not files or not any(n.startswith("LICENSE") or n.startswith("COPYING") for n, _ in files):
            raise SystemExit("模块 %s@%s 里找不到许可文件（%s）" % (path, ver, mdir))
        lic = license_id(files[0][1]) or "见全文"
        sections.append({"name": path, "version": ver, "license": lic, "blocks": files})

    # ---- 2) Go 标准库 / 运行时 ----
    go_files = read_license_files(goroot)
    if not go_files:
        raise SystemExit("GOROOT 里找不到 LICENSE：%s" % goroot)
    go_lic = license_id(go_files[0][1]) or "见全文"
    sections.append({"name": "Go standard library & runtime", "version": gover,
                     "license": go_lic, "blocks": go_files})

    # ---- 3) 前端内联库 ----
    for v in FRONTEND:
        fp = os.path.join(VENDOR, v["file"])
        if not os.path.isfile(fp):
            raise SystemExit("缺少前端库许可原文：%s" % fp)
        with open(fp, "r", encoding="utf-8", errors="replace") as f:
            body = normalize(f.read())
        lic = license_id(body) or "见全文"
        sections.append({"name": v["name"], "version": v["version"], "license": lic,
                         "blocks": [("", body)]})

    total = len(sections)
    # 组件清单做成列对齐（名称 / 版本 / 协议三列），列宽按当期最长项现算 ——
    # 固定列宽会在「github.com/mattn/go-colorable」这种长路径上错位。
    name_w = max(len(s["name"]) for s in sections)
    ver_w = max(len(s["version"]) for s in sections)
    # 不写文件级标题：「开源鸣谢」已经是弹窗标题，正文再顶一遍等于重复。
    # 文件开头直接就是致谢，随后组件清单，再逐段许可全文。
    head = [
        "本程序使用了下列开源项目与第三方库。",
        "感谢它们的作者与每一位贡献者，是这些工作让本项目得以存在。",
        "",
        "组件清单",
        THIN,
    ]
    for i, s in enumerate(sections, 1):
        head.append("  %2d. %s  %s  %s" % (
            i, s["name"].ljust(name_w), s["version"].ljust(ver_w), s["license"]))
    head.append(THIN)

    parts = ["\n".join(head)]
    for i, s in enumerate(sections, 1):
        title = "%s  %s  —  %s" % (s["name"], s["version"], s["license"])
        parts.append(render_section(i, total, title, s["blocks"]))
    return "\n\n".join(parts) + "\n"


def main():
    try:
        sys.stdout.reconfigure(encoding="utf-8", errors="replace")
        sys.stderr.reconfigure(encoding="utf-8", errors="replace")
    except Exception:
        pass

    ap = argparse.ArgumentParser(description="生成 / 校验 THIRD-PARTY-NOTICES.txt")
    ap.add_argument("--check", action="store_true", help="只校验，不一致则退出码 1")
    args = ap.parse_args()

    text = build()
    blob = text.encode("utf-8")

    if args.check:
        if not os.path.isfile(OUT):
            print("[notices] FAIL: 清单不存在，先跑 python scripts/gen_notices.py")
            return 1
        with open(OUT, "rb") as f:
            old = f.read()
        if old != blob:
            print("[notices] FAIL: 清单与当前构建不一致 —— 依赖或 Go 版本变了。")
            print("        重新生成：python scripts/gen_notices.py")
            return 1
        print("[notices] ok: 清单与当前构建一致（%d 字节）" % len(blob))
        return 0

    with open(OUT, "wb") as f:
        f.write(blob)
    print("[notices] 已生成 %s（%d 字节）" % (OUT, len(blob)))
    return 0


if __name__ == "__main__":
    sys.exit(main())
