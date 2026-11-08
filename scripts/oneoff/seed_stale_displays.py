#!/usr/bin/env python3
"""往配置里塞几条“曾经检测到、现在没接”的假显示器，方便手动测“显示的屏幕”下拉。

真机上没法凭空造一块被拔掉的屏 —— 得先接过一次才会进目录（displayKnown）。
这个脚本直接写配置，省得为了看灰色分组去拔线。

    python scripts/oneoff/seed_stale_displays.py           # 注入（幂等，重复跑不会重复加）
    python scripts/oneoff/seed_stale_displays.py --clean   # 全部清掉

注入的都是 displayKnown 里“当前枚举不到”的 ID，所以启动后一律落在灰色分组；
其中 GSM5B7F…（LG 27UL850）同时写进 displayIds，用来验证“勾过的历史屏照样显示打勾”。

改完配置要**重启软件**：配置在启动时读一次，运行中的实例下次保存会把它覆盖回去。
"""

from __future__ import annotations

import argparse
import json
import os
import shutil
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
DEFAULT_CFG = os.path.join(ROOT, "config", "settings.json")

# 真实风格的 PnP ID（厂商三字母 + 型号 + 接口哈希），肉眼看不出来是假的
FAKE = [
    {
        "id": "GSM5B7F#4&2f9c8a71&0&UID4357",
        "label": "LG 27UL850",
        "width": 3840,
        "height": 2160,
        "lastSeen": "2026-11-06T18:20:00+08:00",
    },
    {
        "id": "SAM0DB7#5&2a1f3e4c&0&UID4353",
        "label": "SAMSUNG S27R650",
        "width": 1920,
        "height": 1080,
        "lastSeen": "2026-11-03T09:05:00+08:00",
    },
    {
        "id": "HWP2A19#5&3b7d1f20&0&UID4355",
        "label": "AOC Q27G2S/D",
        "width": 2560,
        "height": 1440,
        "lastSeen": "2026-10-28T21:47:00+08:00",
    },
    {
        "id": "APP9C3E#4&1c5e2b90&0&UID4360",
        "label": "DELL U2412M",
        "width": 1920,
        "height": 1200,
        "lastSeen": "2026-10-15T14:12:00+08:00",
    },
]
# 这一条同时进 displayIds：验证“配置里勾着的历史屏要显示打勾”
TICKED = FAKE[0]["id"]


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--config", default=DEFAULT_CFG, help="settings.json 路径")
    ap.add_argument("--clean", action="store_true", help="删除这批假数据")
    args = ap.parse_args()

    path = os.path.abspath(args.config)
    if not os.path.isfile(path):
        print(f"找不到配置文件：{path}", file=sys.stderr)
        return 1

    with open(path, encoding="utf-8") as f:
        cfg = json.load(f)

    ids = {k["id"] for k in FAKE}

    known = [k for k in (cfg.get("displayKnown") or []) if k.get("id") not in ids]
    picked = [x for x in (cfg.get("displayIds") or []) if x not in ids]

    if not args.clean:
        known.extend(FAKE)
        if TICKED not in picked:
            picked.append(TICKED)

    cfg["displayKnown"] = known
    cfg["displayIds"] = picked

    # 万一脚本把配置写坏了，手上还有一份原件
    shutil.copy2(path, path + ".bak-seed")
    with open(path, "w", encoding="utf-8", newline="\n") as f:
        json.dump(cfg, f, ensure_ascii=False, indent=2)
        f.write("\n")

    action = "已清除" if args.clean else "已注入"
    print(f"{action} {len(FAKE)} 条假显示器 → {path}")
    print(f"  displayIds  = {picked}")
    print(f"  displayKnown 剩 {len(known)} 条（其中假数据 "
          f"{0 if args.clean else len(FAKE)} 条）")
    print("改完请重启软件再看设置页——配置只在启动时读一次。")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
