#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""督休专员题名「零遮挡」全专员探针（驱动端）。

为什么需要它：题名的位置与字号由渲染层按立绘 alpha 剖面反解（break.js 与
takeout.js 各一份 layoutRoleTitle）。这个反解有一处对称性极易漏：剖面 minY[]
按**原图列序**量，屏幕映射按**显示方向**算 —— 立绘水平翻转时两者互为镜像。

2026-11-02 实测到漏掉这一步的后果：命中判定去查了图片的另半边，牌面明明落在
人物左侧的空白里（实拍牌底是纯背景），判据却在右侧对称位置数到一片像素，于是
「每列都算遮挡」—— 魔法师 hit=34，正好等于区间列数 34，字号被一路压到下限
15px。只有几何配置 flip=true 的专员才触发，主探针（probe_renderer 跑真实配置
的那一位）抽不到就等于没测。所以这里**逐个专员**钉住 fixedRole 各跑一遍。

覆盖：每位专员 × 三个版式（横屏休息窗 / 竖屏休息窗 / 外卖提醒窗）。两份题名
实现（break.js、takeout.js）都必须被跑到 —— 只测一份等于只测一半。

断言（数据由 probe_role_title.js 采，判定在这里）：
  · 已定位（data-rt=1）且是竖排
  · hit == 0（一个不透明像素都不压立绘）
  · 字号 >= 15px（1x 屏宋体的可读下限）
  · 牌面文字 == 该专员名（角色串了 / 空名字当场暴露）
  · 文字不溢出牌面（Range 判据，容差 6.5px）
  · 无未捕获 JS 错误
  · 元断言：至少要有一位翻转专员、也要有非翻转对照组 —— 否则这轮等于没测到
    关键路径（全 false 说明几何数据坏了，"全绿"是假的）

隔离：配置用出厂默认的一份临时副本，WR_SETTINGS_PATH 指过去；真实
config/settings.json 前后 SHA-256 必须一致。一位专员一个服务是必须的：
fixedRole 在服务启动时读进内存。

用法：python scripts/probe/probe_role_title.py [--role 魔法师 ...]
退出码 0 = 全部通过。
"""

import argparse
import hashlib
import json
import os
import shutil
import socket
import subprocess
import sys
import tempfile
import time
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(os.path.dirname(HERE))
EXE = os.path.join(ROOT, "build", "bin", "chibingren_server.exe")
FACTORY = os.path.join(ROOT, "internal", "settings", "default_settings.json")
SUP_DIR = os.path.join(ROOT, "assets", "supervisors")
PROBE_JS = os.path.join(HERE, "probe_role_title.js")
OUT = os.path.join(tempfile.gettempdir(), "wr_role_title")

try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass

_stat = {"n": 0, "fail": 0}


def check(ok, name, detail=""):
    _stat["n"] += 1
    if not ok:
        _stat["fail"] += 1
    mark = "ok  " if ok else "FAIL"
    print("  [%s] %s%s" % (mark, name, ("  — %s" % detail) if detail else ""))
    return bool(ok)


def digest(p):
    if not os.path.exists(p):
        return ""
    h = hashlib.sha256()
    with open(p, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def free_port():
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def wait_up(base, proc, timeout=40.0):
    t0 = time.time()
    while time.time() - t0 < timeout:
        if proc.poll() is not None:
            return False
        try:
            urllib.request.urlopen(base + "/", timeout=2)
            return True
        except Exception:
            time.sleep(0.4)
    return False


def list_roles():
    """磁盘现算，绝不写死内置专员个数/名单（增删专员不该改探针）。"""
    if not os.path.isdir(SUP_DIR):
        return []
    return sorted(d for d in os.listdir(SUP_DIR) if os.path.isdir(os.path.join(SUP_DIR, d)))


def run_one(role):
    """起一个隔离服务，把这位专员的三个场景量回来。"""
    iso = tempfile.mkdtemp(prefix="wr_rt_")
    try:
        cfg = json.load(open(FACTORY, encoding="utf-8"))
        sup = cfg.setdefault("supervisor", {})
        sup["enabled"] = True
        # 固定显示：会话里的督休专员必然就是 fixedRole 那位。随机/顺序模式下
        # 抽到谁不可控，量到的 hit 也就对不上「这个专员的立绘」。
        sup["selection"] = "fixed"
        sup["fixedRole"] = role
        sup["order"] = [role]          # 出厂 order 为空 = 用内置顺序；这里显式点名
        cfg_path = os.path.join(iso, "settings.json")
        with open(cfg_path, "w", encoding="utf-8") as f:
            json.dump(cfg, f, ensure_ascii=False, indent=2)

        port = free_port()
        base = "http://127.0.0.1:%d" % port
        env = dict(os.environ)
        env.update({
            "WAILS_SERVER_PORT": str(port),
            "WAILS_SERVER_HOST": "127.0.0.1",
            "WR_SETTINGS_PATH": cfg_path,
            "WR_LOG_LEVEL": "warn",
        })
        logp = os.path.join(iso, "server.log")
        logf = open(logp, "w", encoding="utf-8")
        proc = subprocess.Popen([EXE], cwd=ROOT, env=env, stdout=logf, stderr=subprocess.STDOUT)
        try:
            if not wait_up(base, proc):
                tail = ""
                try:
                    tail = open(logp, encoding="utf-8", errors="replace").read()[-600:]
                except Exception:
                    pass
                return None, "服务没起来：" + tail
            penv = dict(os.environ)
            penv.update({
                "WR_BASE": base,
                "WR_OUT": OUT,
                "WR_ROLE": role,
                "WR_PRELOAD": os.path.join(ROOT, "scripts", "probe_preload.js"),
                "NODE_PATH": os.path.join(ROOT, "scripts", "node_modules"),
            })
            r = subprocess.run(["node", PROBE_JS], cwd=ROOT, env=penv, text=True,
                               encoding="utf-8", errors="replace", timeout=300,
                               capture_output=True)
            rows = None
            for line in (r.stdout or "").splitlines():
                if line.startswith("WR_JSON "):
                    rows = json.loads(line[len("WR_JSON "):])
            if rows is None:
                return None, "探针没有回传数据：" + ((r.stderr or "")[-600:])
            return rows, ""
        finally:
            if proc.poll() is None:
                proc.terminate()
                try:
                    proc.wait(timeout=8)
                except Exception:
                    proc.kill()
            logf.close()
    finally:
        shutil.rmtree(iso, ignore_errors=True)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--role", action="append", default=None,
                    help="只跑指定专员（可多次；默认全跑）")
    args = ap.parse_args()

    if not os.path.exists(EXE):
        print("没有 %s（先 go build -tags server -o build/bin/chibingren_server.exe ./cmd/workerrest）" % EXE)
        return 2
    try:
        import playwright  # noqa: F401
    except Exception:
        pass
    if not shutil.which("node"):
        print("找不到 node（探针用 Playwright 驱动系统 Edge）")
        return 2

    roles = args.role or list_roles()
    if not roles:
        print("assets/supervisors 下没有专员目录，无法测量")
        return 2

    os.makedirs(OUT, exist_ok=True)
    real_cfg = os.path.join(ROOT, "config", "settings.json")
    before = digest(real_cfg)

    print("督休专员题名零遮挡：%d 位专员 × 3 个版式" % len(roles))
    flips = {"on": 0, "off": 0}
    for role in roles:
        print("\n[%s]" % role)
        rows, err = run_one(role)
        if rows is None:
            check(False, "%s：整位专员跑完三个版式" % role, err)
            continue
        for d in rows:
            label = "%s %s" % (role, d.get("scene"))
            if d.get("error"):
                check(False, label + "：题名元素存在", d.get("error"))
                continue
            flips["on" if d.get("flip") else "off"] += 1
            check(d.get("positioned") is True, label + "：题名已完成实测定位",
                  "hit=%s fs=%s flip=%s" % (d.get("hit"), d.get("fs"), d.get("flip")))
            check(d.get("vertical") is True, label + "：题名是竖排", d.get("vertical"))
            check(d.get("hit") == 0, label + "：零遮挡（一个不透明像素都不压立绘）",
                  "hit=%s fs=%s flip=%s" % (d.get("hit"), d.get("fs"), d.get("flip")))
            fs = d.get("fs")
            check(isinstance(fs, (int, float)) and fs >= 15,
                  label + "：题名字号 >= 15px", fs)
            check(d.get("text") == role, label + "：牌面文字 == 该专员名", repr(d.get("text")))
            check(d.get("overflow") is False, label + "：文字没有溢出牌面",
                  "box=%s" % (d.get("box"),))
            check(not d.get("errs"), label + "：页面无未捕获 JS 错误", d.get("errs"))

    # 关键路径的「失效断言」：翻转命中判据就是这次修的 bug，若这一轮一位翻转
    # 专员都没抽到，全绿毫无意义（几何数据坏了也会是这样）。
    check(flips["on"] > 0, "本轮覆盖到「水平翻转」立绘（误报 bug 的触发条件）",
          "flip=on %d / off %d" % (flips["on"], flips["off"]))
    check(flips["off"] > 0, "本轮同时有「未翻转」对照（不是只测镜像那一侧）",
          "flip=on %d / off %d" % (flips["on"], flips["off"]))

    after = digest(real_cfg)
    check(before == after, "真实 config/settings.json 未被改动", real_cfg)

    print("\n结果：%d 项，%d 失败" % (_stat["n"], _stat["fail"]))
    print("数据：%s" % OUT)
    return 0 if _stat["fail"] == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
