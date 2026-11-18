# 去掉牌外碎印泥（2026-11-18）

对象：题名牌（`.dialog-name`）牌外那圈细碎印泥。

用户反馈：「外围碎点似乎不好看，去掉外围碎点」。

## 一、改了什么

碎点本来是一条**独立图层**：`seal.js` 用同一枚种子生成第二张 SVG（`specksSVG`），
写进 `--rt-specks`，由 `.dialog-name::after`（`inset: -10px`，比牌子大一圈）
铺成背景图。整层删掉，墨块本体（`--rt-ink` + `inkSVG`）不动。

| 位置 | 改动 |
|---|---|
| `web/dist/seal.js` | 删 `specksSVG()`、`BAND` 常量、`applyTo` 里写 `--rt-specks` 的那行、`SealGen.specksSVG` 导出 |
| `web/dist/break.css` | 删 `.dialog-name::after` 整条规则（含 22 行说明注释） |
| `web/dist/takeout.css` | 同上 |
| `web/dist/settings.css` | 删 `.sup-preview .dialog-name::after` |
| `docs/design/assets/settings.css` | 同上（设计稿副本，保持逐项一致） |
| `break.js` / `takeout.js` / `settings.js` | 只改注释（"与四周碎点一起换"→"咬口/断裂一起换"） |

保留不动：墨块的撕毛轮廓、缺墨咬口、中央保护带、投影（`inkSVG` 的 `shadow`）。
投影当初搬进 SVG 的原因之一（CSS filter 只画到外扩 10%，会把 `::after` 的环带裁掉）
虽已消失，但搬进去之后本来就更准（影子跟着撕毛轮廓走），不回退。

## 二、连带消掉的一个边界瑕疵

`RT_EDGE = 6 < BAND = 10`：牌贴窗口左缘时，碎点环带左侧被窗口裁掉 4px。
这条一直登记在案（改它要动题名遮挡判据），碎点层删掉后**自动消失**。

## 三、探针改造：从"碎点参数对不对"改成"碎点不该存在"

| 探针 | 改动 |
|---|---|
| `probe_seal_ink.py` + `seal_ink_probe.js` | 删 `prims`（碎点图元计数）与两条数量断言；新增 `gen.hasSpecks === false`、`gen.hasApply === true`。**28/0** |
| `probe_role_title_stress.py` + `role_title_stress.js` | 删 `BAND`、`spkVB`、`afterInset`、`speckClipLeft`；新增 `speckType === 'none'`（`::after` 没有背景图） |
| `probe_renderer.py` + `renderer_probe.js` | `_seal_pair()` 去掉碎点参数，改为 **`spk` 非空即失败**；三处（预览 / 双方位提醒窗 / 外卖窗）各补一条 `afterContent === 'none'`。**489/0** |
| `scripts/oneoff/inspect_seal.js` | 观察器不再铺碎点层，只放大墨块本体 |

`_seal_pair` 里那条 `if spk: return False, "牌外仍有碎点层"` 是**永久性的负向断言**：
以后谁把 `--rt-specks` 加回来（CSS 已经没地方铺它了，加回来就是死代码），
渲染探针会当场红。

## 四、验证

- `probe_renderer` **489/0**（新增/改写的四条断言均 ok：`content=none` ×3 + `specks` 恒空）
- `probe_role_title` **408/0**
- `probe_seal_ink` **28/0**（120 块墨块：中央 `alphaMin=1.000`、`gapMax=0`、`cover`/`edgeCover` 不变）
- `probe_role_title_stress` / `probe_settings_ui`：见当日日志
- `go test -count=1 -tags server ./...`：19 包全 ok
- `build/bin/rest.exe` 重建（`appVersion=dev`）；发布仍走 `scripts/build/make_portable.py`

目检：`scripts/oneoff/inspect_seal.js` 输出 6 倍放大图（50×88 / 44×132 / 37×300 /
37×418 × 3 种子），牌外干净、无任何红点或擦痕。
