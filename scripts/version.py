#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""版本号来源：只认 git tag，格式 ``v<主>.<次>.<修订>``（如 v0.9.0）。

代码里不再写死版本号 —— app.go 的 appVersion 只是 "dev" 占位符，真实版本由打包时
``-ldflags "-X main.appVersion=<tag 去掉 v>"`` 注入。打 tag 即发版。

CLI（显示最近一次 tag 的版本号）：
    python scripts/version.py          # v0.9.0
    python scripts/version.py --bare   # 0.9.0

被 make_portable.py 引用时提供：
    tag_at_head()       当前提交上的 tag（没打 tag 返回 None）
    latest_tag()        最近一次 tag（没有 tag 返回 None）
    prev_tag(tag)       某个 tag 之前最近的一个 tag
    commits_between()   两个 tag 之间的提交标题（时间顺序，旧→新）
"""

import os
import re
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)  # 项目根

# 版本 tag 的唯一定义：v主.次.修订（不许带后缀，避免排序/比较歧义）
TAG_RE = re.compile(r"^v(\d+)\.(\d+)\.(\d+)$")

try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
    sys.stderr.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass


def _git(*args):
    """跑一条 git 命令，返回去尾空白的 stdout；失败（含无 tag）返回 None，不抛异常。"""
    try:
        r = subprocess.run(
            ["git", *args], cwd=ROOT, stdout=subprocess.PIPE, stderr=subprocess.PIPE
        )
    except OSError:
        return None
    if r.returncode != 0:
        return None
    return r.stdout.decode("utf-8", "replace").strip()


def is_release_tag(tag):
    """是否形如 v1.2.3"""
    return bool(tag) and bool(TAG_RE.match(tag))


def sort_key(tag):
    """版本大小排序键（越大越新）；不合规范的按 (0,0,0)。"""
    m = TAG_RE.match(tag or "")
    return tuple(int(x) for x in m.groups()) if m else (0, 0, 0)


def tag_at_head():
    """当前提交（HEAD）上的 tag：多个时取版本最高的；没打 tag 返回 None。"""
    out = _git("tag", "--points-at", "HEAD") or ""
    tags = [t.strip() for t in out.splitlines() if is_release_tag(t.strip())]
    return max(tags, key=sort_key) if tags else None


def latest_tag():
    """最近一次 tag：先取 HEAD 可达的最近 tag，取不到再取全部 tag 里版本最高的。"""
    t = _git("describe", "--tags", "--abbrev=0")
    if is_release_tag(t):
        return t
    out = _git("tag", "--list", "v*", "--sort=-v:refname") or ""
    tags = [x.strip() for x in out.splitlines() if is_release_tag(x.strip())]
    return tags[0] if tags else None


def prev_tag(tag):
    """tag 之前最近的一个 tag（从它的父提交往前找）；没有更早的 tag 返回 None。"""
    if not tag:
        return None
    t = _git("describe", "--tags", "--abbrev=0", tag + "^")
    return t if is_release_tag(t) else None


def commits_between(prev, cur=None):
    """prev..cur 之间的提交标题，按时间顺序（旧→新），合并提交不计。

    prev 为空（首个 tag）时返回空列表 —— 没有「上一个 tag」就没有可列的范围。
    """
    if not prev:
        return []
    rng = f"{prev}..{cur}" if cur else f"{prev}..HEAD"
    out = _git("log", "--reverse", "--no-merges", "--pretty=format:%s", rng)
    if out is None:
        return []
    return [ln.strip() for ln in out.splitlines() if ln.strip()]


def main(argv):
    if any(a in ("-h", "--help") for a in argv):
        print(__doc__)
        return 0
    bare = "--bare" in argv
    tag = latest_tag()
    if not tag:
        print("[version] 还没有任何 tag（格式：v主.次.修订，如 v0.9.0）", file=sys.stderr)
        return 1
    print(tag[1:] if bare else tag)
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
