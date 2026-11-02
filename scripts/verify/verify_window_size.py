#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""验证用：设置窗口尺寸这一层的端到端断言（无头服务 + Playwright + 隔离配置）。

断言分两块：
  · 页面侧（window_size_verify.js）：勾选框在「设置窗口」卡片里、出厂默认勾上；
    关于页在**出厂尺寸**下恰好不滚、高度减 10px 就滚；最小宽度 760 下无横向裁切、740 有。
  · 配置侧（本脚本）：取消勾选后 settings.json 的 window.rememberSize 真的变成 false，
    且 lastWidth / lastHeight 仍是 0（页面无权写尺寸）。这两条只有落到文件上才验得到。

出厂尺寸不写死在本脚本里：一律从 default_settings.json 现读，再用环境变量传给页面探针 ——
用户改默认尺寸时只需改配置，脚本与断言自动跟随。

隔离：WR_SETTINGS_PATH 指向 %TEMP% 的副本，收尾不碰真实 config/settings.json。
"""
import json
import os
import shutil
import socket
import subprocess
import sys
import time
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(os.path.dirname(HERE))
EXE = os.path.join(ROOT, "build", "bin", "chibingren_server.exe")
PROBE = os.path.join(HERE, "window_size_verify.js")
TEMP = os.environ.get("TEMP", ROOT)
ISO = os.path.join(TEMP, "wr_winsize_verify_iso")
LOG = os.path.join(TEMP, "wr_winsize_verify_server.log")

failures = []
checks = 0


def check(ok, label, detail=""):
    global checks
    checks += 1
    print(("  ok   " if ok else "  FAIL ") + label + (("  | " + str(detail)) if detail else ""))
    if not ok:
        failures.append(label + (("  | " + str(detail)) if detail else ""))
    return ok


def free_port(preferred):
    for cand in (preferred, 0):
        with socket.socket() as s:
            try:
                s.bind(("127.0.0.1", cand))
                return s.getsockname()[1]
            except OSError:
                continue
    raise SystemExit("没有可用端口")


def wait_up(base, proc, timeout=25.0):
    t0 = time.time()
    while time.time() - t0 < timeout:
        if proc.poll() is not None:
            return False
        try:
            with urllib.request.urlopen(base + "/", timeout=2) as r:
                if r.status == 200:
                    return True
        except Exception:
            pass
        time.sleep(0.4)
    return False


def main():
    try:
        sys.stdout.reconfigure(encoding="utf-8", errors="replace")
        sys.stderr.reconfigure(encoding="utf-8", errors="replace")
    except Exception:
        pass

    node_cmd = shutil.which("node")
    if not node_cmd:
        print("找不到 node")
        return 2
    # Playwright 装在 scripts/node_modules（历史位置是仓库根，两处都认）
    pw_dir = next((d for d in (os.path.join(ROOT, "scripts", "node_modules"),
                               os.path.join(ROOT, "node_modules"))
                   if os.path.isdir(os.path.join(d, "playwright"))), "")
    if not pw_dir:
        print("找不到 playwright")
        return 2

    if os.environ.get("WR_NO_BUILD") != "1":
        print("构建无头服务…")
        b = subprocess.run(
            ["go", "build", "-tags", "server", "-o", EXE, "-ldflags", "-s -w", "./cmd/workerrest"],
            cwd=ROOT, capture_output=True, text=True, encoding="utf-8", errors="replace",
        )
        if b.returncode != 0:
            print("构建失败:\n" + (b.stderr or b.stdout))
            return 2

    port = free_port(29743)
    base = "http://127.0.0.1:%d" % port

    # 隔离配置：**从出厂默认起步**（拷真实配置会带上用户改过的 rememberSize，
    # 「出厂默认勾上」这条就验不到了）
    shutil.rmtree(ISO, ignore_errors=True)
    os.makedirs(ISO, exist_ok=True)
    iso_settings = os.path.join(ISO, "settings.json")
    with open(os.path.join(ROOT, "internal", "settings", "default_settings.json"), "r", encoding="utf-8") as f:
        defaults = json.load(f)
    with open(iso_settings, "w", encoding="utf-8") as f:
        json.dump(defaults, f, ensure_ascii=False, indent=2)

    # 出厂尺寸从配置现读（不写死）：断言与页面探针都跟随它
    win_def = defaults.get("window") or {}
    DEF_W, DEF_H = int(win_def.get("width") or 0), int(win_def.get("height") or 0)
    if DEF_W <= 0 or DEF_H <= 0:
        print("default_settings.json 缺 window.width / height，先补配置再跑")
        return 2
    MIN_W = 760  # 实测的布局下界（window_size.go 顶部有推导）
    print("出厂尺寸 %d×%d，最小宽度 %d" % (DEF_W, DEF_H, MIN_W))

    env = dict(os.environ)
    env["WAILS_SERVER_PORT"] = str(port)
    env["WAILS_SERVER_HOST"] = "127.0.0.1"
    env["WR_SETTINGS_PATH"] = iso_settings
    env.pop("ELECTRON_RUN_AS_NODE", None)

    logf = open(LOG, "w", encoding="utf-8")
    srv = subprocess.Popen([EXE], cwd=ROOT, env=env, stdout=logf, stderr=subprocess.STDOUT)
    try:
        if not wait_up(base, srv):
            print(open(LOG, encoding="utf-8", errors="replace").read()[-2000:])
            return 1
        print("服务就绪 %s" % base)
        penv = dict(os.environ)
        penv["WR_BASE"] = base
        penv["WR_DEF_W"] = str(DEF_W)
        penv["WR_DEF_H"] = str(DEF_H)
        penv["WR_MIN_W"] = str(MIN_W)
        r = subprocess.run([node_cmd, PROBE], cwd=ROOT, env=penv,
                           capture_output=True, text=True, timeout=300,
                           encoding="utf-8", errors="replace")
        if r.returncode != 0:
            print("--- stderr ---\n" + (r.stderr or "")[-3000:])
            return 1

        line = ""
        for ln in (r.stdout or "").splitlines():
            if ln.startswith("WIN_OPT "):
                line = ln[len("WIN_OPT "):]
        if not line:
            print("探针没有输出 WIN_OPT：\n" + (r.stdout or "")[-2000:])
            return 1
        data = json.loads(line)

        print("页面侧：")
        opt = data["opt"]
        check(opt["exists"], "勾选框存在（#win-remember）")
        check(opt["type"] == "checkbox", "是复选框", opt["type"])
        check(opt["cardTitle"] == "设置窗口", "位于「设置窗口」卡片内", opt["cardTitle"])
        check("自动记录窗口尺寸" in (opt["label"] or ""), "标签文案正确", opt["label"])
        check(opt["hasTip"], "带「?」说明（解释勾选/不勾的区别）")
        check(opt["checked"] is True, "出厂默认勾选（首次启动用默认尺寸、之后沿用上次）")

        h_ok, h_bad, h_wide = data["hOk"], data["hBad"], data["hWide"]
        check(h_ok["overflow"] == 0,
              "关于页在出厂尺寸 %d×%d 恰好不出垂直滚动条" % (DEF_W, DEF_H), h_ok)
        check(h_bad["overflow"] > 0,
              "高度降到 %d 就出现滚动条（%d 是边界而不是随手放大）" % (DEF_H - 10, DEF_H), h_bad)
        check(h_wide["overflow"] == 0,
              "再宽 80px（%d×%d）也不出滚动条（宽度过了断点后关于页高度不再变）"
              % (DEF_W + 80, DEF_H), h_wide)

        check(data["clipMin"] == 0,
              "%d 宽下各标签页无横向裁切" % MIN_W, "%s" % data["clipMinSel"])
        check(data["clipNarrow"] > 0,
              "%d 宽下确实出现横向裁切（%d 是实测下界）" % (MIN_W - 20, MIN_W),
              "%s over=%s" % (data["clipNarrowSel"], data["clipNarrow"]))

        print("配置侧：")
        with open(iso_settings, "r", encoding="utf-8") as f:
            saved = json.load(f)
        win = saved.get("window") or {}
        check(win.get("rememberSize") is False,
              "取消勾选后 window.rememberSize 落盘为 false", win.get("rememberSize"))
        check(win.get("width") == DEF_W and win.get("height") == DEF_H,
              "默认尺寸未被页面改动", "%sx%s（期望 %dx%d）"
              % (win.get("width"), win.get("height"), DEF_W, DEF_H))
        check(win.get("lastWidth") == 0 and win.get("lastHeight") == 0,
              "页面无权写 lastWidth/lastHeight（仍为 0）",
              "%s/%s" % (win.get("lastWidth"), win.get("lastHeight")))
    finally:
        if srv.poll() is None:
            srv.kill()

    print()
    if failures:
        print("失败 %d / %d" % (len(failures), checks))
        for f in failures:
            print("  - " + f)
        return 1
    print("全部通过 %d / %d" % (checks, checks))
    return 0


if __name__ == "__main__":
    sys.exit(main())
