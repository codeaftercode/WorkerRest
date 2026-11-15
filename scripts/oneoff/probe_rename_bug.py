#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""一次性：验证「候选专员重命名」链路（改名 → 落盘 → 配置指向跟着换 → 改回）。

做法：
  1. supervisors/ 先整目录备份到 %TEMP%，收尾无论成败都还原（改名会动真实目录名）；
  2. 隔离配置：出厂默认拷到临时目录，WR_SETTINGS_PATH 指过去；
     并把 supervisor 设成「固定显示 = 秦始皇」、order 里也放名字，
     用来验证改名后 fixedRole / order 有没有跟着换；
  3. 走 UI 真改一次名（不打桩），断言：对话框关闭、磁盘上出现新名字、
     旧名字消失、配置里的 fixedRole 与 order 都换成新名字；
  4. 再走 UI 改回原名，确认可以反复改（第二次改名同样要过）。
输出到 stdout，不入库。
"""

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
EXE = os.path.join(ROOT, "build", "bin", "rest_server.exe")
FACTORY = os.path.join(ROOT, "internal", "settings", "default_settings.json")
SUPS = os.path.join(ROOT, "supervisors")
OLD = "秦始皇"
TMPNAME = "改名验证专用"


def free_port(preferred):
    with socket.socket() as s:
        try:
            s.bind(("127.0.0.1", preferred))
            return preferred
        except OSError:
            pass
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def http_get(url, timeout=2):
    try:
        with urllib.request.urlopen(url, timeout=timeout) as r:
            return r.status
    except Exception:
        return 0


def rename_via_ui(page, old, new):
    """右键磁贴 → 重命名 → 填新名 → 点确定，返回对话框状态。"""
    page.evaluate(
        """(name) => {
            const tile = [...document.querySelectorAll('#sup-gallery .role-item')]
                .find(el => el.dataset.name === name);
            const r = tile.getBoundingClientRect();
            tile.dispatchEvent(new MouseEvent('contextmenu', {
                bubbles: true, cancelable: true,
                clientX: r.left + 20, clientY: r.top + 20
            }));
        }""",
        old,
    )
    page.wait_for_timeout(300)
    page.evaluate(
        """() => {
            const b = document.querySelector('#sup-role-menu .role-menu-item[data-act="rename"]');
            if (b) b.click();
        }"""
    )
    page.wait_for_timeout(500)
    page.evaluate(
        """(v) => {
            const inp = document.getElementById('pm-input');
            inp.value = v;
            inp.dispatchEvent(new Event('input', { bubbles: true }));
        }""",
        new,
    )
    page.evaluate("() => document.getElementById('pm-ok').click()")
    page.wait_for_timeout(1400)
    return page.evaluate(
        """() => {
            const box = document.getElementById('prompt-modal');
            const err = document.getElementById('pm-error');
            return {
                modalOpen: !!box && !box.classList.contains('hidden'),
                errText: (err && !err.classList.contains('hidden')) ? err.textContent.trim() : '',
                tiles: [...document.querySelectorAll('#sup-gallery .role-item')]
                    .map(el => el.dataset.name)
            };
        }"""
    )


def main():
    from playwright.sync_api import sync_playwright

    backup = None
    if os.path.isdir(SUPS):
        backup = tempfile.mkdtemp(prefix="wr_sups_bak_")
        shutil.copytree(SUPS, os.path.join(backup, "supervisors"))
        print("已备份 supervisors →", os.path.join(backup, "supervisors"))

    iso = tempfile.mkdtemp(prefix="wr_rename_")
    iso_settings = os.path.join(iso, "settings.json")
    with open(FACTORY, encoding="utf-8") as f:
        cfg = json.load(f)
    sup = cfg.setdefault("supervisor", {})
    sup["enabled"] = True
    sup["selection"] = "fixed"
    sup["fixedRole"] = OLD
    sup["order"] = [OLD, "医生", "画家"]
    with open(iso_settings, "w", encoding="utf-8") as f:
        json.dump(cfg, f, ensure_ascii=False, indent=2)

    port = free_port(29793)
    env = dict(os.environ)
    env["WAILS_SERVER_PORT"] = str(port)
    env["WAILS_SERVER_HOST"] = "127.0.0.1"
    env["WR_SETTINGS_PATH"] = iso_settings
    env.pop("ELECTRON_RUN_AS_NODE", None)
    base = "http://127.0.0.1:%d" % port
    logf = open(os.path.join(iso, "server.log"), "w", encoding="utf-8")
    proc = subprocess.Popen([EXE], cwd=ROOT, env=env, stdout=logf, stderr=subprocess.STDOUT)
    rc = 1
    try:
        up = False
        for _ in range(60):
            if proc.poll() is not None:
                break
            if http_get(base + "/") == 200:
                up = True
                break
            time.sleep(0.5)
        if not up:
            print("服务没起来")
            return 1

        with sync_playwright() as p:
            b = p.chromium.launch(channel="msedge", headless=True)
            page = b.new_page(viewport={"width": 1180, "height": 1000})
            page.add_init_script(path=os.path.join(ROOT, "scripts", "probe_preload.js"))
            page.goto(base + "/settings.html", wait_until="domcontentloaded")
            page.wait_for_function("typeof window.api === 'object' && window.api !== null")
            page.wait_for_timeout(600)
            page.click('.nav-item[data-tab="supervisor"]')
            page.wait_for_timeout(900)

            print("\n---- 第一次改名：%s → %s" % (OLD, TMPNAME))
            r1 = rename_via_ui(page, OLD, TMPNAME)
            print(json.dumps(r1, ensure_ascii=False))
            disk1 = page.evaluate("() => window.api.listSupervisorRoles()")
            cfg1 = page.evaluate("() => window.api.getSettings()")
            sup1 = (cfg1 or {}).get("supervisor") or {}
            s1 = {
                "新名在磁盘": any(x["name"] == TMPNAME for x in disk1),
                "旧名已消失": not any(x["name"] == OLD for x in disk1),
                "fixedRole": sup1.get("fixedRole"),
                "order": sup1.get("order"),
            }
            print(json.dumps(s1, ensure_ascii=False))

            print("\n---- 第二次改名：%s → %s" % (TMPNAME, OLD))
            r2 = rename_via_ui(page, TMPNAME, OLD)
            print(json.dumps(r2, ensure_ascii=False))
            disk2 = page.evaluate("() => window.api.listSupervisorRoles()")
            cfg2 = page.evaluate("() => window.api.getSettings()")
            sup2 = (cfg2 or {}).get("supervisor") or {}
            s2 = {
                "旧名已恢复": any(x["name"] == OLD for x in disk2),
                "临时名已消失": not any(x["name"] == TMPNAME for x in disk2),
                "fixedRole": sup2.get("fixedRole"),
                "order": sup2.get("order"),
                "专员数": len(disk2),
            }
            print(json.dumps(s2, ensure_ascii=False))

            ok = (
                not r1["modalOpen"] and not r1["errText"]
                and s1["新名在磁盘"] and s1["旧名已消失"]
                and s1["fixedRole"] == TMPNAME and TMPNAME in (s1["order"] or [])
                and not r2["modalOpen"] and not r2["errText"]
                and s2["旧名已恢复"] and s2["临时名已消失"]
                and s2["fixedRole"] == OLD and OLD in (s2["order"] or [])
            )
            print("\n结论：", "通过" if ok else "不通过")
            rc = 0 if ok else 1
            b.close()
    finally:
        if proc.poll() is None:
            proc.terminate()
            try:
                proc.wait(timeout=5)
            except Exception:
                proc.kill()
        logf.close()
        shutil.rmtree(iso, ignore_errors=True)
        if backup:
            shutil.rmtree(SUPS, ignore_errors=True)
            shutil.copytree(os.path.join(backup, "supervisors"), SUPS)
            shutil.rmtree(backup, ignore_errors=True)
            print("已从备份还原 supervisors（%d 个条目）" % len(os.listdir(SUPS)))
    return rc


if __name__ == "__main__":
    sys.exit(main())
