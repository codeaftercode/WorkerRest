#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""按行号区间把一个 Go 源文件切成多个文件。

切分原则：纯物理搬移，不改任何函数/类型/逻辑，只按行号区间切片。
每个切片文件 = 原文件的 package 声明 + 原 import 块（自动修剪未用 import）+ 切片内容。

用法：
    python split_go.py <源文件> <输出文件名1>:<起始行>:<结束行> [<输出文件名2>:<起始>:<结束> ...]

行号区间为 [start, end] 闭区间（1-based，含两端）。
"""

import re
import sys


def read_lines(path):
    with open(path, "r", encoding="utf-8") as f:
        return f.readlines()


def parse_header(lines):
    """返回 (package 行, import 块行列表, 正文起始行 index)。"""
    pkg_line = ""
    import_lines = []
    body_start = 0
    i = 0
    n = len(lines)
    # 找 package
    while i < n:
        s = lines[i]
        if s.strip().startswith("package "):
            pkg_line = s
            i += 1
            break
        i += 1
    # 跳过 package 后的空行与注释，找 import
    while i < n:
        s = lines[i]
        if s.strip() == "":
            i += 1
            continue
        if s.strip().startswith("import"):
            break
        if s.strip().startswith("//go:build") or s.strip().startswith("//go:embed"):
            i += 1
            continue
        # 非 import 开头，说明没有 import 块
        body_start = i
        return pkg_line, import_lines, body_start
    if i >= n:
        return pkg_line, import_lines, i
    # 解析 import 块
    if "(" in lines[i]:
        # 块形式
        import_lines.append(lines[i])  # import (
        i += 1
        while i < n:
            import_lines.append(lines[i])
            if lines[i].strip() == ")":
                i += 1
                break
            i += 1
        body_start = i
    else:
        # 单行形式 import "xxx"
        import_lines.append(lines[i])
        i += 1
        body_start = i
    return pkg_line, import_lines, body_start


def import_entries(import_lines):
    """从 import 块提取 (path, pkg, raw) 列表。

    pkg 是默认包名（用于判断「是否被使用」），raw 是原始 import 语句文本
    （含别名，如 `_ "embed"`），生成时原样写出。
    """
    entries = []
    for ln in import_lines:
        s = ln.strip()
        if s in ("", "(", ")"):
            continue
        if s.startswith("import "):
            s = s[len("import "):].strip()
        s = s.rstrip(";")
        m = re.match(r'^(?:([A-Za-z_][A-Za-z0-9_]*)\s+)?["`]([^"`]+)["`]$', s)
        if m:
            alias, path = m.group(1), m.group(2)
            pkg = alias if alias else path.rsplit("/", 1)[-1]
            if alias is None:
                pkg = re.sub(r"[^A-Za-z0-9_]", "_", pkg)
            raw = s  # 保留原始文本（含别名）
            entries.append((path, pkg, raw))
    return entries


def split_file(src, slices):
    lines = read_lines(src)
    pkg_line, import_lines, body_start = parse_header(lines)
    all_imports = import_entries(import_lines)
    # body_start 是正文起始的 0-based index，对应的 1-based 行号 = body_start+1
    body_start_line = body_start + 1

    for out_name, start, end in slices:
        # 1-based 行号转 0-based index；切片区间必须在正文范围内（>= body_start_line）
        if start < body_start_line:
            start = body_start_line
        start_idx = start - 1
        end_idx = end  # 切片是 lines[start_idx:end_idx]
        body = lines[start_idx:end_idx]

        # 判断每个 import 是否被 body 使用
        body_text = "".join(body)
        used_imports = []
        for path, pkg, raw in all_imports:
            keep = False
            # blank import（别名 _）：看切片里是否有对应指令。
            # `_ "embed"` 服务于 `//go:embed` 指令。
            if pkg == "_":
                if path.endswith("/embed") or path == "embed":
                    keep = "//go:embed" in body_text
                else:
                    # 其它 blank import（如 _ "image/png" 的 side-effect）保守保留
                    keep = True
            elif re.search(r"\b" + re.escape(pkg) + r"\.", body_text):
                keep = True
            if keep:
                used_imports.append(raw)

        # 生成新文件
        out = [pkg_line]
        out.append("\n")
        if used_imports:
            out.append("import (\n")
            for raw in used_imports:
                out.append("\t" + raw + "\n")
            out.append(")\n")
        out.append("\n")
        out.extend(body)

        with open(out_name, "w", encoding="utf-8", newline="\n") as f:
            f.writelines(out)
        print("生成 %s（%d-%d 行，%d 个 import）" % (out_name, start, end, len(used_imports)))


def main():
    if len(sys.argv) < 3:
        print(__doc__)
        sys.exit(1)
    src = sys.argv[1]
    slices = []
    for spec in sys.argv[2:]:
        parts = spec.split(":")
        if len(parts) != 3:
            print("切片格式应为 输出文件:起始行:结束行，得到: %s" % spec)
            sys.exit(1)
        out_name, start, end = parts[0], int(parts[1]), int(parts[2])
        slices.append((out_name, start, end))
    split_file(src, slices)


if __name__ == "__main__":
    main()
