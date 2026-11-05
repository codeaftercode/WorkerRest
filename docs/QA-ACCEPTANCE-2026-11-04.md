# 双屏真机验证报告 — 提醒窗口落点

> 测试日期：2026-11-04　对象：`displayIds`（提醒窗口显示在哪几块屏）+ 投屏判定
> 结论：**发现并修复 2 个真机才现形的缺陷**（都用 WorkArea 对齐、竖屏尺寸未折算旋转），
> 新增真机探针 `probe_display_target.py`，18 项断言全部通过。
> 口径：数字均为本轮实跑；探针自己写错的地方也如实记下来（不把「探针假设过期」当成产品缺陷）。

---

## 1. 环境

| 项 | 值 |
|---|---|
| 显示拓扑 | 扩展模式，2 块活动屏（此前一直是单屏，多屏路径从未在真机上跑过） |
| 主屏 | DELL U2719DS　2560×1440 @ (0,0)，任务栏 48px |
| 副屏 | DELL SP2318H　**竖屏旋转**，桌面占位 1080×1920 @ (2560,−209) |
| 投屏判定 | `active=false mirror=false wireless=false`（2 条活动输出、非镜像）— 正确 |
| Go | go1.27.3（`D:/Programs/Go/bin`，不在 PATH） |
| 隔离 | `WR_SETTINGS_PATH` / `WR_LOG_FILE` 指向 `%TEMP%`，真实 `config/settings.json` 未动 |

副屏是竖屏这点很关键：它是第二个缺陷的触发条件，横屏机器上永远看不到。

---

## 2. 新增真机探针

`scripts/probe/probe_display_target.py`（+ 辅助 `scripts/probe/display_ids.go`）：

- 一组一条进程，隔离配置，让短休息 6 秒后自己弹出来；
- 用 Win32 枚举**本进程**的顶层窗口（DWM 可见边界），按窗口中心点判定落在哪块屏；
- 同时断言**数量**、**落屏**、**尺寸（按目标屏 × widthPct）**；
- 少于两块活动屏时返回退出码 3（= 无法测量），与「失败」区分；
- 显示器清单借 Go 的 `display.List()` 输出 JSON，不在 Python 里重抄一遍 DISPLAYCONFIG
  结构体 —— 那套结构体错一个字段就会静默取到错值（`internal/windnd` 的镜像判据踩过）。

| 场景 | 断言 |
|---|---|
| 只勾主屏（配置里是主屏的稳定 ID） | 1 扇，主屏，尺寸 = 主屏 95% |
| 空配置（`[]`） | 1 扇，主屏（不是「所有屏」） |
| 勾满两块 | **2 扇，各落一块屏**，尺寸各按本屏算 |
| 只勾副屏 | 1 扇，只在副屏，主屏一扇都没有 |
| 勾了已拔掉的 ID | 1 扇，回退主屏（绝不「一扇都不弹」） |
| 只勾副屏 + 提前提示 | 「即将休息」小窗也在副屏 |

---

## 3. 缺陷 1：用 WorkArea 对整屏矩形比四边，选副屏永远落到主屏

`targetDisplays()` 原本拿 `application.Screen.WorkArea`（**扣掉任务栏**）去和
`display.Info`（**整块屏的源模式尺寸**）比四边全等。主屏高度 1440 → 工作区 1392，
永远差 48px，于是四边比对必然失败。

失败后的兜底是「一块都没匹配上 → 回退主屏」，所以：

- 选主屏：靠兜底蒙对，看起来正常；
- **选副屏：同样兜底回主屏 —— 用户勾了副屏，提醒永远只弹主屏，日志一句异常都没有**；
- 勾满两块：仍然只弹 1 扇（主屏）。

真机第一轮就是这么暴露的（`落屏 = ['DELL SP2719DS'] 实际 = ['DELL U2719DS']`）。

修法：

- 对齐改用 `Screen.Bounds`（整块屏矩形），`PhysicalBounds` 兜一遍；
- 四边不全等时退一档「中心点落在谁的矩形里」，容得下驱动报的 1~2 px 偏差；
- 匹配不上的屏**记日志**（型号 + ID），全部失配回退主屏也记日志 —— 不让它再是静默失败。

回归测试：`cmd/workerrest/display_target_test.go` 直接构造「工作区矮 48px」的屏幕，
钉死不能再用 WorkArea 对齐；另一条覆盖 2px 漂移与「屏外矩形必须返回 nil」。

---

## 4. 缺陷 2：竖屏的桌面占用尺寸没折算旋转

`internal/display.List()` 把 `DISPLAYCONFIG_MODE_INFO`（源模式）的宽高直接当桌面尺寸，
但源模式永远是**未旋转**的画面：竖屏显示器报 1920×1080，实际在虚拟桌面里占 1080×1920。

后果：

- 与 Wails `Bounds`（1080×1920）四边全失配，只靠第 3 节的「中心点」兜底才蒙对；
- 设置页显示器下拉的分辨率提示把竖屏显示成横的；
- 一旦两块屏的中心点判定出现歧义，落点就会错。

修法：`desktopSize()` 按 `targetInfo.rotation`（90/270）互换宽高；位置坐标本就是旋转后的，
不折算。单位测试 `TestDesktopSizeAppliesRotation` 覆盖 identity / 90 / 180 / 270。

修好后的真机输出：

```
DEL41A9#4&1ff378d5&0&UID4162 | DELL U2719DS | 2560x1440 @ (0,0) · 主屏
DELA0FD#5&1876e159&0&UID4352 | DELL SP2318H | 1080x1920 @ (2560,-209)
```

---

## 5. 本轮结果

| 项 | 断言 | 结果 |
|---|---|---|
| `probe/probe_display_target.py`（新） | 18 | 0 失败 |
| `go test -count=1 -tags server ./...` | 全部包 | 全绿 |
| `go vet -tags server ./...` | — | 干净 |
| `probe/probe_settings_ui.py`（三层） | 112 + 57 + 42 = 211 | 0 失败 |
| `smoke_server.py` | 189 | 0 失败 |

真机落点实测（2560×1440 主屏 + 1080×1920 竖屏副屏）：

```
只勾主屏      → (64,35) 2432×1322      主屏
勾满两块      → (64,35) 2432×1322 + (2587,-162) 1026×1778
只勾副屏      → (2587,-162) 1026×1778
失配 ID       → (64,35) 2432×1322      主屏
提前提示      → (3216,1399) 400×240    副屏右下角
```

---

## 6. 顺带修掉的探针自身问题（不是产品缺陷）

`settings_flow_probe.js` 的多选断言是单屏时代写的：它直接点「第一个选项」，
多屏时第一个恰好是唯一勾着的主屏，点下去等于「取消最后一块」—— UI 拒绝是**正确行为**，
断言却写成「应当变成未勾选」，于是报红两条。

改成多屏口径：点未勾选的第二块 → 断言落盘成两块的具体 ID 快照 →
取消主屏（允许）→ 勾回来 → 再验「最后一块不允许取消」。单屏机器保留原分支。

---

## 7. 同批改动：出厂 `displayIds` 改为空数组 + 启动自动补主屏

需求：出厂配置里「显示的屏幕」是空的；软件启动时若为空，立即检测所有显示器，自动勾选主屏
并写进配置文件。

实现（`internal/settings`）：

- `default_settings.json`：`"displayIds": []`；
- `fillPrimaryDisplayID()`：加载管线第 ⑦·1 步，空数组时枚举一次 `display.List()`，
  取主屏的稳定 ID 写进配置，并作为 `normalizeLoad` 的返回值 `displayFilled`
  触发磁盘回写（走的是既有的「加载即回写」通道，不额外引入写路径）；
- 写**具体 ID**：界面上那一格就是「勾了哪几块屏」的快照，写完之后配置与勾选一一对应；
  软件尚未发布，因此不保留任何「跟随主屏」的哨兵值或迁移分支；
- 枚举不可用（远程会话 / 无桌面）时保持空数组，运行时 `Resolve` 仍兜底主屏，
  下次启动再补 —— 不写一个「看不见屏」时猜出来的值；
- 幂等：已有值时既不修改也不回报 changed，避免每次启动都写盘。

真机实测（隔离配置，两次冷启动）：

```
出厂 settings.json      displayIds = []
第一次启动后            displayIds = ["DEL41A9#4&1ff378d5&0&UID4162"]（主屏 DELL U2719DS）
日志                    [settings] 已回写初始化结果 …（补主屏 ID=true）
第二次启动              sha256 不变、mtime 不变（没有重复写盘）
```

| 项 | 断言 | 结果 |
|---|---|---|
| `TestPrimaryIDOf` / `TestFillPrimaryDisplayID` / `TestDefaultDisplayIDsEmptyThenFilled` | 3 | 通过（含真机枚举；不可用时自动跳过） |
| `go test -count=1 -tags server ./...` | 全部包 | 全绿 |
| `probe_settings_ui.py`（三层） | 112 + 57 + 42 | 0 失败 |
| `probe_display_target.py` | 18 | 0 失败 |

---

## 8. 移除全部旧配置迁移代码（软件未发布，不存在老版本）

用户口径：**不要考虑老配置迁移**。于是把「为了兼容不存在的旧版本」而写的整条链路拆掉，
而不是留着当死代码。

删掉的东西：

| 位置 | 内容 |
|---|---|
| `internal/display` | `PrimaryID = "@primary"` 哨兵值，以及 `Resolve` 里的哨兵分支 |
| `internal/settings/settings_norm.go` | `legacyScreensToIDs()`、`cleanLegacyKeys()` 整段（`screens`/`allScreens`/`castPause`/`installId`/`opacity`/`strict`/`supervisor.level`/`sound.shortStart…`/`workTime` 九类兼容分支）、`suitePresetExes` + `pruneSuitePresets()`、`normalizeSound` 里从 `sound` 字符串反推 `singleId`、`normalizeTakeout` 的 `leadMin × 60` |
| `internal/settings/settings_store.go` | `normalizeLoad` 返回值 5 → 3（`presetSeeded` / `idsRenumbered` / `displayFilled`）、`normalizeForSave` 里兜底 `delete(raw,"installId")`、`reminderMessages` 的 `messages` 旧键整段迁移 |
| `cmd/workerrest/web/dist/settings.js` | `DISPLAY_PRIMARY` 常量与 `"@primary"` 勾选态解析 |
| 探针 | 播种一律 `displayIds: []`（= 出厂值，启动自补主屏）；落点探针「只勾主屏」改用真实 `Info.ID` |
| 测试 | 删 `TestLegacyAllScreensBecomesSnapshot` / `TestLegacyCastPauseSplitsIntoBoth` / `TestPruneSuitePresets`；`TestNormalizeLoadLegacyMigration` → `TestNormalizeLoadCoercesBadValues`（只留「脏值收口」，不再断言任何旧键） |

保留的（不是版本兼容，而是**坏值兜底**）：

- `normalizeDisplayIDs` 丢弃非字符串 / 空串 / 重复项 → 空数组；
- 空 `displayIds` 的运行时兜底主屏（`display.Resolve` + `fillPrimaryDisplayID`）；
- 各类越界夹紧、非法枚举回落缺省值；
- 枚举不可用时保持空数组、下次启动再补。

回归（本轮全部重跑）：

| 项 | 断言 | 结果 |
|---|---|---|
| `go test -count=1 -tags server ./...` | 全部包 | 全绿 |
| `go vet -tags server ./...` / `gofmt -l` | — | 干净（含 CRLF 保持） |
| `probe_settings_ui.py`（三层） | 112 + 57 + 42 | 0 失败 |
| `probe_display_target.py`（真机双屏） | 18 | 0 失败 |
| `probe_capture_guard.py` | 17 | 0 失败 |
| `probe_config_matrix.py` | 69 | 0 失败 |
| `smoke_server.py` | 189 | 0 失败 |
| 真机冷启动 ×2（隔离配置） | 补齐 + 幂等 | `[]` → `["DEL41A9#…"]`，第二次 sha256 与 mtime 均不变 |

顺带修掉：`gofmt -l` 查出 `cmd/workerrest/app_bindings.go`（`ShowSettingsWindow` 花括号后混进了
制表符）与 `sys_test.go`（map 字面量对齐）两处格式漂移，均为前一阶段改动留下的。

---

## 9. 显示器列表的检测时机 + 面板内「刷新」（用户提问驱动的补充）

### 9.1 现状（改动前）

`displayIds` 下拉的选项来自 `window.api.listDisplays()` → `display.List()`（`QueryDisplayConfig`，
每次调用都现场枚举，没有缓存）。但**调用时机只有一处**：`fillForm()` —— 也就是设置窗口的
webview 首次加载时调一次；`恢复默认设置` 之后会再走一遍。设置窗口是启动时创建、
关闭只 `Hide()` 不销毁、页面不重载，所以实际行为是：

> **一次启动只枚举一次**。中途接上 / 拔掉显示器、换接口，选项一直是旧的，除非重启软件。

只关显示器电源不属于「拔线」：`QDC_ONLY_ACTIVE_PATHS` 仍会返回那条活动路径，所以那一块
**继续留在列表里**，提醒窗口照旧画上去（只是用户看不见）。Windows 没有公开 API 报告
「这块屏已断电」，应用层无法检测。

### 9.2 改了什么

| 位置 | 改动 |
|---|---|
| `web/dist/settings.js` | 抽出 `fetchDisplays()`（枚举 + 组装 items）；`mountMultiDrop` 增加 `reload` 参数与底部 `.cdrop-foot` 行（左侧「检测到 N 块 / 勾选的 N 块未接入」、右侧 `↻ 刷新`）；`renderOptions()` 支持就地重建选项 |
| `web/dist/settings.css` | `.cdrop-foot` / `.cdrop-foot-note` / `.cdrop-refresh` / `.cdrop-empty` / 转圈动画（`prefers-reduced-motion` 下关闭） |
| `web/dist/settings.html` | 「显示的屏幕」的 `?` 提示补「列表只在打开这一页时检测一次，之后点面板里的刷新（只关电源不算拔掉）」 |
| 文档 | README「勾选是快照」补检测时机与刷新语义；`scripts/build_manual.py` 同步后重建手册（16 张图，两次生成 sha256 一致） |

刷新语义（刻意这样定）：

1. **就地替换**选项，菜单不关、键盘焦点不丢；
2. 新接入的屏**默认不勾**（快照语义不变，插投影仪不会被自动带上）；
3. 已勾选但当前没接的屏**保留在配置里**（`effective()` = 勾选项 + 原配置里已不在列表的 ID），
   面板上提示「勾选的 N 块未接入」，接回去还在 —— 面板上看不见的东西不该被一次顺手保存抹掉；
4. **刷新本身不写配置**（勾选归用户），下一次 `autoSave` 才落盘；
5. 枚举结果为 0 时**不把触发器做成 disabled**：面板里只剩「刷新」，接好屏当场能重试。

### 9.3 探针当场抓到的两个 bug（都已修）

- `allIds` 在挂载时忘了从 `items` 初始化 → 第一次渲染时摘要被当成「未检测到显示器」，
  勾选兜底到第一块；改为 `var allIds = items.map(...)`。
- 刷新时给按钮设 `disabled` → 焦点被踢到 `body`，`focusout` 判定「焦点离开面板」把菜单关了，
  用户点刷新反而看到面板消失；改用 `.busy` 类 + `aria-busy`（点击由 busy 标记拦）。
- 附带修：`original`（「已配置」基准）在每次 `toggle` 后同步成 `value()`，否则基准停在
  进页面那一刻，之后新勾的屏被拔掉永远算不进「未接入」。

### 9.4 验证

| 项 | 断言 | 结果 |
|---|---|---|
| `go test -count=1 -tags server ./...` | 全部包 | 全绿 |
| `probe_settings_ui.py`（三层） | 122 + 57 + 58 = 237 | 0 失败（flow 层 +12 条：结构 5 条、刷新行为 5 条、热插拔桩 12 条中的其余） |
| 热插拔桩（`[F0·2b]`，替换 `listDisplays` 而不真拔线） | 12 | 0 失败：多一块假屏 → 选项 +1 且默认未勾；拔掉已勾选的屏 → 选项 -1、提示未接入、配置仍保留；换回真实枚举 → 复原且配置一字未改 |
| `scripts/a11y_audit.js` | 四个页面 | 未命名控件 0 |
| `scripts/probe_cdrop_a11y.js` | 19 | 0 失败（自绘下拉键盘可达未回退） |
| `smoke_server.py` | 189 | 0 失败 |
| 截图核对 | `display_drop_open.png` | 两个选项 + 分隔线 + 「检测到 2 块 / ↻ 刷新」，180px 触发器宽度未变、无溢出无重叠 |

一次性截图脚本：`scripts/oneoff/shot_display_drop.py`（起无头服务 + 隔离配置 + Edge 截图，
产物在 `%TEMP%/wr_shot/`）。

---

## 10. 自动暂停的「恢复休息」语义统一 + 投屏原因拆分

### 10.1 改动前：只有一条原因真的实现了

用户提问「是不是有『因某原因暂停，恢复休息后该原因不再生效，直到它消失再达成』这个设定」。
核查结论：**只有「指定应用运行时暂停」有**，另外四条（系统空闲 / 勿扰 / 全屏独占 / 投屏）
点「恢复休息」是空操作。

实测（临时用例，勿扰触发一次自动暂停后调 `schedPauseToggle()`）：

```
[scheduler] 暂停（勿扰）
点恢复休息后：paused=true  reasons=[勿扰]
下一跳后：    paused=true  reasons=[勿扰]
```

根因两层：

1. `schedPauseToggle` 只对 `应用占用` 做特判，其余原因落到 `PauseToggle()` →
   `Resume("manual")`；而自动暂停的 `PauseReasons` 里根本没有 `manual`，
   `Resume` 找不到这条就 return —— 整条路径等于没执行。
2. 即便把原因摘掉也没用：`applyAutoPause` 是电平触发，每 3 秒一跳，条件还在就再
   `Pause` 一次，三秒后原样挂回去。所以必须有「已手动恢复」的忽略状态。

### 10.2 改了什么

| 位置 | 改动 |
|---|---|
| `app.go` | `appPauseIgnored bool` → `autoPauseIgnored map[string]bool`（键 = 原因名）；`castActiveLogged` 拆成 `castMirrorLogged` / `castWirelessLogged` |
| `sys_watch.go` | 原因键拆分：`投屏` → `复制模式` / `无线投放`；`applyAutoPause` 加忽略判定与解除；新增忽略表四个方法 + `activeAutoPauseReasons()`；`appPauseWatchTick` 改用新方法；空闲回来自动清除 idle 的忽略 |
| `app_bindings.go` | `schedPauseToggle` 统一：把当前正在生效的自动原因全部记为已手动恢复并逐个摘掉，再摘 `manual`；用户主动暂停时清空整张忽略表 |

语义（六条自动原因一致）：

1. 因某原因自动暂停 → 点「恢复休息」→ **立即恢复**；
2. 该原因条件仍在 → 不再因它暂停（忽略表命中）；
3. 条件消失（勿扰关闭 / 投屏结束 / 应用退出 / 人回来）→ 忽略自动解除；
4. 再次达成 → 照常自动暂停；
5. 恢复之后才成立的**其他**原因不受影响，该暂停还是暂停；
6. 用户主动点「暂停休息」→ 清空所有忽略，回到常规判定。

### 10.3 为什么把「复制模式」和「无线投放」拆成两条原因

两条判据的来源（拓扑镜像 vs 输出端是不是 Miracast）、开关、用户心智都独立。合成
一条原因时，用户为复制模式点一次「恢复休息」，正在用的无线投放会被一起静音，
而且托盘只显示「投屏」，看不出是哪一条还生效。拆开后托盘显示
「已暂停：复制模式」/「已暂停：无线投放」，可分别忽略、分别解除。

前端不需要改：`pauseReasonText` 的默认分支原样返回，两个新键本身就是可读中文。

### 10.4 验证

| 项 | 断言 | 结果 |
|---|---|---|
| `go test -count=1 -tags server ./...` | 全部包 | 全绿 |
| `TestAutoPauseManualResumeIgnoresUntilConditionClears` | 5 条原因 × 6 步（暂停 / 恢复 / 忽略期间不再挂回 / 其他原因照常 / 条件消失解除 / 再次达成重暂停） | 全绿 |
| `TestManualPauseClearsAutoPauseIgnores` | 手动暂停清空忽略、之后照常叠加 | 全绿 |
| `TestCastPauseWantedMatrix` | 4 种命中形态 × 4 种开关组合，断言 (镜像, 无线) 两个分量 | 全绿 |
| `TestCastWatchRespectsEachSwitch` | 四种开关组合下两条原因各自出现、无重复 | 全绿 |
| `go vet -tags server ./...` / `gofmt -l` | — | 干净 |
| `smoke_server.py` | 189 | 0 失败 |
| `probe_config_matrix.py` | 69 | 0 失败 |

未覆盖：真机勿扰 / 投屏 / 全屏独占的端到端（触发条件依赖系统状态），本轮用
`applyAutoPause` 直接喂状态验证判定层，系统探测层由 `TestCastWatchRespectsEachSwitch`
与 `SysProbe` 冒烟另行覆盖。

### 10.5 顺带修掉：探针在单屏机器上的必失败断言

复跑设置页三层时 `settings_flow_probe.js` 报 1 项失败：「底部提示『勾选的 N 块未接入』」。
根因不是本次改动：`display.List()` 当时只枚举到一块 `Default_Monitor`（3120×2080，
真实双屏没上报），热插拔桩拔掉**唯一一块**屏后选项数归 0，底部理应改成可重试的
「点「刷新」重新检测」，而断言写死了「未接入」。

改法是按剩余选项数分支：0 个选项断「刷新」，≥1 个断「未接入」，两种机器都成立。
复跑 122 + 57 + 53 全绿（58 → 53 是空列表分支跳过的 5 条）。

---

## 11. 自动暂停全链路审计（2026-11-04 晚）

### 11.1 范围与手段

范围：六条自动原因（系统空闲 / 勿扰 / 全屏独占 / 复制模式 / 无线投放 / 指定应用占用）
的探测 → 判定 → 暂停记账 → 忽略表 → 恢复 → 时钟与统计的连带影响，以及托盘 / 设置页 / 前端。

手段：逐行走读 + **可执行夹具**。夹具不是读代码猜结论，而是把每条疑点做成能跑的用例，
打印实际状态；成立才写进本表，不成立的在 11.4 列出「已排除」。

### 11.2 已修复

| # | 缺陷 | 证据 | 修法 |
|---|---|---|---|
| 1 | 离开恰好从「提前提示区间」里开始时，回来会先弹一次提前提示窗、再被重锚收掉（右下角闪一下，日志留一条「即将休息（剩余 0 ms）」） | 夹具实测：`OnPreNotify 0 → 1`，随后 `PreBreakWanted()=false`；同样的场景在提前区间之前离开再回来是 0 次 | `Resume` 拆出 `ResumeQuiet`（恢复但不重弹提示窗），空闲链路按 `CountdownReanchorable()` 决定走哪条 |
| 2 | 「指定应用运行时暂停」关着时，每 4 秒仍枚举一次全部进程 | `appPauseWatchTick` 单次 17.82 ms（本机 152 进程） | 功能关闭且无残留状态时直接返回：17.82 ms → **25 µs** |

修 1 时补了一道防回归：静默恢复的前提是「紧接着真的会重锚」。固定时刻线与自定义提醒
不会重锚，无条件静默会把这次提醒的提前提示**永久吞掉**（休息仍准点弹，少了提前那一下）。
判据抽成 `reanchorTargetLocked`，`ReanchorCountdown` 与 `CountdownReanchorable` 共用，
不各写一份。

配对回归：

| 用例 | 断言 |
|---|---|
| `TestIdleReturnDoesNotFlashPreWindow` | 倒计时线：实验组 `OnPreNotify=0`；同一时刻的对照组（直接调 `Resume`）必须 `=1`，证明场景构造成立、差别只来自这一次调用 |
| `TestIdleReturnKeepsPreWindowForFixedLine` | 固定时刻线：回来必须弹回提前提示窗（`OnPreNotify=1` 且 `PreBreakWanted()=true`） |

### 11.3 待决策（未改）

| # | 现象 | 实测 / 依据 | 影响 |
|---|---|---|---|
| A | 休息窗口在场时因自动原因暂停，**休息剩余时间被冻结**（窗挂到条件消失） | 实测：休息中开勿扰，剩余 20000 ms → 1200 ms 后仍 20000 ms；勿扰关掉才继续走 | 全屏看视频 / 开勿扰时，一个 20 秒的休息窗可以挂几十分钟；恢复时按封顶直接收尾，统计仍记一次「完成」 |
| B | 自动暂停恢复后**不重锚**倒计时，与空闲离开的口径不一致 | `ReanchorCountdown` 全仓只有空闲链路一个调用点 | 开会 30 分钟（勿扰）回来，倒计时接着走，可能只剩 2 分钟；而离开 30 分钟回来会从头计时 |
| C | 忙碌型自动暂停期间，统计照常累加「工作」 | `statsIsIdle` 只判空闲时长（stats_bindings.go:167） | 开会 / 投屏 30 分钟被记成工作时长，日报偏高 |
| D | 「已忽略」对用户不可见 | 托盘只显示「已暂停：…」或「约 X 分钟后」，忽略表无任何出口 | 误点一次「恢复休息」后，该原因在条件消失前不再提醒，用户无从得知 |

A 的修法要点（改动会碰统计口径，故未擅自改）：`pauseFreezesClockLocked` 增加一条
「本次暂停的原因全是自动原因 → 不冻结」，`Pause` 进入暂停时把结论存进
`pauseFrozeBreak`，`Resume` 的 break 分支据此决定要不要按 delta 平移 —— 最后一个
原因被摘掉时判据已经变了，不能现算。前端 `renderStatus` 里 `phase==="break" && !breakWhilePaused`
时用 `pausedAt` 当 now 的那一段要同步改。

### 11.4 已排除（查过，不是缺陷）

| 疑点 | 为什么不是 |
|---|---|
| 已暂停时叠 / 摘一条原因不推状态，界面显示旧原因 | `Pause`/`Resume` 的中途分支确实不 emitState（实测 states 差值为 0），但托盘每秒重算、设置页每秒轮询 `getState`，界面 1 秒内自愈 |
| 忽略表与监视 goroutine 的竞态（先读后写） | 两个方向的交错结果都正确：忽略已写入则监视器跳过；忽略未写入而原因已在列表里，`Pause` 本就早退 |
| 锁序死锁 | `autoPauseMu` 从不与 `sched.mu` 嵌套持有；`Resume` 在拿 `sched.mu` 之前取 `cfg` 快照，与 `StateForRenderer` 同序 |
| 空闲原因也进忽略表是否有用 | 无实际作用：`Pause(idle)` 只由 `IdleStepAway` 触发，而它要求 `away==false`；用户恢复后 `away` 仍是 true，不会再触发。无害但属冗余，注释已写明 |
| 投屏两条判据共用一次 `QueryDisplayConfig` 会不会互相牵连 | 各自独立原因、独立开关、独立「已记录」标记，夹具实测：为复制模式恢复后，无线投放仍处于忽略、条件消失后照常重新暂停 |

### 11.5 真机探测（本机，非构造）

```
dnd: supported=true  active=false detail=accepts-notifications（正常接收通知）
fullscreen: supported=true active=false detail=accepts-notifications
cast: supported=true active=false mirror=false wireless=false detail=1 条活动输出，非镜像
procs: supported=true count=152
idle: supported=true detail=user32!GetLastInputInfo
```

五条探测链路在本机全部可用、当前均未命中。**勿扰 / 投屏 / 全屏独占的端到端触发未验**：
需要真的打开勿扰或接一台投影，沙箱里改不了系统状态。

### 11.6 回归

| 项 | 结果 |
|---|---|
| `go test -count=1 -tags server ./...` | 全绿（含新增 2 条） |
| `go vet -tags server ./...` / `gofmt -l` | 干净 |
| `smoke_server.py` | 189 项，失败 0 |
| `probe_config_matrix.py` | 69 项，失败 0 |

---

## 12. 自动暂停的窗口 / 倒计时 / 统计口径（用户定的 A / B / C）

第 11 节把四条待决策摆出来，用户逐条拍板：

- **A** 触发自动暂停时，立即推迟或跳过所有提醒窗口和提前提示窗口 —— 长休息、短休息、
  外卖提醒、自定义提醒，一个不留。
- **B** 自动暂停恢复后重锚倒计时，与空闲离开同一口径。
- **C** 忙碌型暂停期间记入工作（现状即如此，确认口径并钉回归）。

### 12.1 关键分歧：两种暂停不是一种

判据只有一条：原因键是不是 `manual`（`scheduler.PauseReasonManual`）。不等于它就是自动原因。

| | 手动暂停（托盘 / 设置页 / 快捷键） | 自动暂停（空闲 / 勿扰 / 全屏 / 复制模式 / 无线投放 / 应用占用） |
|---|---|---|
| 进入时 | 只关提前提示窗 | **收掉全部窗口**：休息窗、提前提示窗、外卖提醒窗、外卖预通知；外卖推迟线作废 |
| 被打断的休息 | 继续挂着，剩余时间冻结 | 立即结束，记一次「跳过」（不是「完成 / 提前结束」），短休息计数照走 |
| 暂停期间到点的自定义提醒 | 按各自的「暂停休息时」处置（照常提醒 / 推迟 / 跳过） | **一律跳过**，不看那份配置 |
| 暂停期间到点的外卖 | 同上，按外卖自己的 pauseAction | **一律跳过** |
| 恢复 | 休息剩余平移，倒计时按剩余平移 | 内置倒计时**重锚到满间隔**（与离开回来一致）；固定时刻线只排未来时刻；自定义倒计时保留剩余 |

### 12.2 改了什么

| 位置 | 改动 |
|---|---|
| `internal/scheduler/scheduler.go` | `Host` 加 `OnAutoPause()`；`Scheduler.pauseHadAuto` 记录「这段暂停含自动原因」；`Pause` 按 manual / 自动分叉（自动走 `clearForAutoPause`）；`onTick` 暂停分支对自动原因改调 `pausedSkip(true)`；新增 `AutoPauseActive()` |
| `internal/scheduler/actions.go` | `clearForAutoPause`：打断休息（`advanceCounter` + 记跳过）、phase 归 idle、通知宿主收窗；`resume` 读取 `pauseHadAuto`，自动原因走 `reanchorTargetLocked` 重锚，手动原因保持平移；新增「暂停后主动立即休息」不平移的分支 |
| `internal/scheduler/plan.go` | 无（`reanchorTargetLocked` 已存在，恢复路径复用它） |
| `cmd/workerrest/app_windows.go` | 新增 `OnAutoPause`（关提前提示含外卖 owner、清 `preInfo/preTargetAt`、作废外卖推迟线并记跳过、静默关休息窗不计 break-end）；`closeBreakWindowsOpt` 加 `countEnd` 参数 |
| `cmd/workerrest/takeout_fire.go` | `AutoPauseActive()` 时一律 `skipTakeoutWhilePaused` |
| `cmd/workerrest/app_bindings.go` | `"manual"` 字面量改用 `scheduler.PauseReasonManual` |

`countEnd=false` 的三处既有调用（预览关窗、endBreak 兜底、退出清理）语义不变：它们本来就不该记
break-end，只是原先靠 `info.Preview` 与「状态已翻篇」间接实现，现在显式化。

### 12.3 为什么不给「自动暂停」也按用户配置推迟

推迟的语义是「稍后再提醒」，它默认用户会回来处理。自动暂停期间可能攒下几次外卖、几条自定义
提醒，恢复那一刻一起倒出来，比不提醒更烦。跳过则是「这一次算了」——与「这段暂停不是用户要求
的」一致。手动暂停不同：用户亲手按的，他配了「照常提醒」就是想让它照常弹。

### 12.4 验证

新增 10 条回归（每条都配了手动暂停的对照组，防止两种暂停被写成一种）：

| 用例 | 断言 |
|---|---|
| `TestAutoPauseInterruptsOngoingBreak` | 休息中触发勿扰 → phase=idle、`OnAutoPause` 一次、记 1 次「short 跳过」、短休息计数 +1 |
| `TestAutoPauseClearsPreWindow` | phase=pre 时触发 → 回 idle、清场一次、`PreBreakWanted()=false` |
| `TestAutoPauseForcesSkipOnCustomReminder` | 配「正常提醒」的自定义提醒在自动暂停期间到点 → 不弹窗不弹提示、记 1 次跳过、下一跳不重复 |
| `TestManualPauseStillHonorsRemindOnCustomReminder` | 对照：同一配置手动暂停 → 照常弹 |
| `TestAutoPauseResumeReanchorsCountdown` | 剩 2 分钟时暂停 → 恢复后剩约 600000 ms（满间隔） |
| `TestManualPauseResumeKeepsCountdownProgress` | 对照：手动暂停恢复仍剩约 120000 ms |
| `TestAutoPauseKeepsCustomCountdownProgress` | 自定义倒计时提醒不重锚，保留剩余 |
| `TestTakeoutAutoPauseSkipsEvenWithRemind` | 外卖配「正常提醒」+ 勿扰 → 不弹窗、记 1 次跳过 |
| `TestOnAutoPauseDiscardsTakeoutPostponed` | 推迟线作废并记跳过、外卖预通知归属被清 |
| `TestBusyAutoPauseCountsAsWork` | 勿扰暂停期间 `statsIsBreak=false`、`statsIsIdle=false`（记工作）；真的无输入超阈值才记离开 |

回归结果：

| 项 | 结果 |
|---|---|
| `go test -count=1 -tags server ./...` | 全绿 |
| `go vet -tags server ./...` / `gofmt -l` | 干净 |
| `smoke_server.py` | 189 项，失败 0 |
| `probe_config_matrix.py` | 69 项，失败 0 |
| `probe_settings_ui.py` 三层 | 122 / 57 / 58，失败 0，真实 settings.json 未动 |
| 手册重建 | 16 张图，无未解析占位符 |

### 12.5 未覆盖

- 真机勿扰 / 投屏 / 全屏的端到端触发：沙箱改不了系统状态，本轮用 `Pause(原因)` 直接喂状态
  验判定层。
- 窗口「真的消失」只验到 App 侧的会话与状态（`sess`、`takeoutPostponed`、`preInfo`），
  没抓屏核像素；真窗口的建 / 关路径由既有 `probe_capture_guard.py` 另行覆盖。

---

## 13. 自动暂停不再自动恢复 + 托盘显示暂停时长（2026-11-05）

### 13.1 改了判据的粒度：从「一个标志」到「两张表」

用户要求：无论从哪个渠道触发自动暂停，都不要自动恢复，必须等用户手动点恢复。

直接照做会连带两个问题，都没照抄：

**其一，空闲与应用占用必须继续自动恢复。** 这两条的条件消失就是场景结束
（人回来 / 目标应用退出），而空闲暂停时人根本不在场，没有尴尬可言。把它们也变成
「等手动恢复」，后果是每次离开几分钟回来都要去托盘点一下，核心提醒当天停摆。

所以判据不是「是不是自动原因」，而是「条件消失是否等于场景结束」，拆成两张表：

| 判据 | 位置 | 回答的问题 |
|---|---|---|
| `scheduler.IsAutoPauseReason(r)` | `internal/scheduler/scheduler.go` | 这条原因是不是自动判定挂上的（决定清场 / 跳过 / 恢复重锚那套自动语义） |
| `autoResumeOnClear(r)` | `cmd/workerrest/sys_watch.go` | 条件消失时要不要自己恢复 |

| 原因 | 条件消失时 | 为什么 |
|---|---|---|
| 系统空闲 | 自动恢复 | 人回来了；空闲时人不在场，无尴尬可能 |
| 指定应用占用 | 自动恢复 | 应用退出是明确的结束信号 |
| 勿扰 / 专注 / 演示 | **保持暂停** | 勿扰可能被系统或别人随手关掉，会议还在 |
| 全屏独占 | **保持暂停** | 退出全屏常常只是切出去看一眼 |
| 复制模式 | **保持暂停** | 投屏断一下不等于散会 |
| 无线投放 | **保持暂停** | 同上 |

**其二，「条件消失」与「原因被摘掉」不再是一回事。** 以前条件消失 = Resume =
原因从 `PauseReasons` 里消失，一个事件。现在条件消失后原因仍留着维持暂停，于是
用户此时点「恢复休息」会被当成「用户为一条仍生效的原因点了恢复」，记进忽略表 ——
下一场会议就彻底静音了。

补一张表解决：`autoReasonActive map[string]bool`（App 字段，与忽略表共用
`autoPauseMu`），由监视链路每跳登记「条件此刻是否成立」。`ignoreAutoPause`
只对条件仍成立的那些记忽略。

### 13.2 改动清单

| 文件 | 改动 |
|---|---|
| `internal/scheduler/scheduler.go` | 新增 `IsAutoPauseReason(reason string) bool`（不等于 manual 即自动，新增原因默认落在自动一侧） |
| `internal/scheduler/actions.go` | `Pause` 里的 `reason != PauseReasonManual` 改用上面那个函数 |
| `cmd/workerrest/app.go` | 新增 `autoReasonActive map[string]bool` 字段与注释 |
| `cmd/workerrest/sys_watch.go` | 新增 `autoResumeOnClear`；`applyAutoPause` 条件消失分支按策略决定 Resume 与否；`appPauseWatchTick` 每跳登记条件状态；`ignoreAutoPause` 跳过已消解的原因；新增 `setAutoReasonActive` / `isAutoReasonActive` / `autoReasonActiveLocked` / `allAutoReasonsCleared`；`activeAutoPauseReasons` 同时过白名单与 `IsAutoPauseReason` |
| `cmd/workerrest/tray.go` | `trayTexts` 加 `pausedCleared` 参数；暂停态 tooltip 变「已暂停 42 分钟：勿扰」，条件全消解时追加「（已结束，可以恢复）」、状态行变「状态：已暂停（条件已结束）」；新增 `fmtTrayPaused`（向下取整到分钟，超 24 小时按天） |
| `cmd/workerrest/web/dist/settings.html` | 「自动暂停」卡片标题加 `?`：触发时收窗 + 结束后不自动恢复 |
| `README.md` / `scripts/build_manual.py` | 同步「哪些原因会自动恢复」 |

`fmtTrayPaused` 特意**向下取整**：暂停时长是既成事实，把 1 分 10 秒说成「2 分钟」
会让用户觉得软件在说谎；这与 `fmtTrayAboutMin`（下次提醒的约数，四舍五入）取相反的
取舍，两处注释都写明了原因。

### 13.3 托盘为什么必须带时长

自动暂停不再自己恢复之后，用户唯一的状态来源就是托盘。只说「已暂停：勿扰」不够 ——
他需要知道「停了多久」才能判断是不是卡住了、该不该点恢复，而不会为了看一眼去打开设置页。
条件全结束时的「可以恢复」是同一件事的另一半：软件把「什么时候该动手」明确告诉用户，
而不是让他自己去猜全屏已经退出了没有。

### 13.4 验证

| 项 | 结果 |
|---|---|
| `go test -count=1 -tags server ./...` | 全绿（含新增 4 条） |
| `go vet -tags server ./...` / `gofmt -l` | 干净 |
| GUI（不带 tag）与 server 两种构建 | 均通过 |
| `smoke_server.py` | 189 项，失败 0 |
| `probe_config_matrix.py` | 69 项，失败 0 |
| `probe_settings_ui.py` 三层 | 122 / 57 / 58，失败 0，真实 settings.json 未动 |
| 手册重建 | 16 张图，无未解析占位符 |

新增回归：

| 用例 | 断言 |
|---|---|
| `TestAutoResumeOnClearMatrix` | 六条原因各归哪一侧；`manual` 不是自动原因；白名单每条都是自动原因 |
| `TestIgnoreOnlyForStillActiveReasons` | 条件仍在 → 点恢复记忽略、不再挂回；条件已消失 → 不记忽略、**下一场会议照常暂停** |
| `TestHoldPausedReasonNotDuplicated` | 保持暂停期间条件再次成立，原因不叠加 |
| `TestTrayTipShowsPauseDurationAndReadyHint` | tooltip「已暂停 1 小时 12 分钟：勿扰」；条件结束追加「可以恢复」并换状态行 |
| `TestFmtTrayPaused` | 0 / 59 秒 / 1 分钟 / 42 分 59 秒（向下取整）/ 1 小时 / 1 小时 12 分 / 1 天 1 小时 |

改了两条旧用例的断言（`TestApplyAutoPauseFullScreenEdgeOnly` 第 ④ 步、
`TestAutoPauseManualResumeIgnoresUntilConditionClears` 第 ④ 步）：它们原来断言
「退出全屏 → 恢复」，正是本次要改掉的行为。前者改成「保持暂停 + 登记 false +
托盘可恢复 + 手动恢复后不记忽略」四段断言。

### 13.5 还没做

- **暂停每小时提示**（用户提的第 2 条）：本轮只做了托盘侧。弹窗本身有个必须解决的
  冲突 —— 投屏时右下角弹窗会被投到会议室大屏上，触发条件得加「无投屏 / 无全屏」，
  且应在「条件消失那一刻」先弹一次，而不是从暂停开始计时。
  → 2026-11-05 已实现，见第 14 节。
- 手动暂停加时长（30 分钟 / 1 小时），补两类只剩手动可用的场景：有线**扩展**接投影仪
  （判据故意不认，见 README「无线的认得出，插线的认不出」）、会议室里笔记本屏直接被旁人
  看到（不投屏也不共享，没有任何可检测的信号）。~~「共享单个窗口检测不到」~~ 这句是错的，
  共享走采集通路，亲和性管得住，见第 14.6 节。

## 14. 自动暂停期间的每小时提示（2026-11-05）

### 14.1 需求

用户在「自动暂停不再自动恢复」之后提的第 2 条：暂停每过 1 小时，在右下角弹窗提示一次；
**提醒文字不要提任何「休息」相关的字眼**，不要让别人看到后引起尴尬。

### 14.2 三条硬约束

一、**文案不带「休息」**。「已暂停 1 小时 · 恢复」谁看都只是一句软件状态；写成
「该休息了」就成了当众被人读到「他在摸鱼」。按钮同理叫「恢复」，不叫「恢复休息」。

二、**投屏 / 全屏 / 勿扰生效时一次都不弹**。右下角的小窗在投屏时会被整屏分享出去，
那正是最不该出现在别人屏幕上的东西；全屏（演示 / 放片）与勿扰同理会压住画面。
这四条原因（复制模式、无线投放分开算）任意一条条件仍成立就整段静默 —— 宁可不提示，
也不冒这个险。

三、**压制一解除就先补一次**。投屏刚断 / 退出全屏 / 勿扰关掉的那一刻，屏幕重新属于
用户，正好把「已经暂停多久」告诉他；否则一场两小时的会开完，还要再等一小时才见到提示。

### 14.3 实现（`cmd/workerrest/pause_notice.go`）

判据全部写成具名纯函数，时序可测：

| 函数 | 回答的问题 |
|---|---|
| `pauseNoticeSuppressed(active)` | 当前有没有「不能弹窗」的原因在生效（只看四条危险原因） |
| `pauseNoticeDue(pausedAt, lastShownAt, now, 1h)` | 距上次弹出（本段没弹过则距暂停开始）是否满一小时 |
| `pauseNoticeGapOK(lastShownAt, now)` | 距上次提示是否过了一分钟（压制解除那条早弹的护栏） |
| `pauseNoticeStep(prev, pausedAt, now, suppressed)` | 这一拍弹不弹 + 推进后的节流状态 |
| `pauseNoticeText(elapsedMs)` | 「已暂停 42 分钟」（沿用托盘的 `fmtTrayPaused`，两边说同一件事） |

驱动：`startSystemWatchers` 里新增一条 5 秒一跳的 `watchLoop`，只调 `pauseNoticeTick`；
它只认 `sched.AutoPauseActive()`（这一段暂停里出现过自动原因），手动暂停一律不弹 ——
用户自己按的暂停，托盘上一直写着多久，再弹就是替他操心。不在自动暂停里时节流状态清零，
下一段重新计时。

窗口：`pause.html` + `pause.css` + `pause.js`，300×76 无边框、置顶、不进任务栏，
落在「提醒窗口显示在哪几块屏」的第一块右下角（与提前提示窗同规则）。文案经 URL 的
`text` 下发 —— 措辞的真源只有 Go 侧一处，页面不自己拼。`ContentProtectionEnabled`
跟「共享屏幕时隐藏提醒窗口」走；页面 CSS 在 6.6 秒开始淡出，主进程 7.5 秒兜底关窗
（页面脚本没起来时也不会赖在屏幕上）。

两个动作：`PauseNoticeResume` 走 `schedPauseToggle()`，与托盘「恢复休息」完全同一条
入口（会把生效的自动原因逐条记为「已手动恢复」再摘掉）；`PauseNoticeDismiss` 只关提示。
恢复路径（含托盘 / 快捷键 / 提前提示窗里的暂停按钮）都会顺手收掉这扇小窗。

### 14.4 容易写错的几处

- **节流锚点必须是「上次弹出时刻」而不是暂停开始时刻**：否则第二小时一到就立刻再弹一次
  （`pauseNoticeDue` 取 `max(pausedAt, lastShownAt)`）。
- **压制解除的早弹要有最短间隔**：刚弹过 30 秒又开一次全屏再关掉，不该换来第二条提示。
- **「只关提示」不重置节流**：否则连点几下 × 就能把整条提示关掉一整天。
- **节流状态必须带段标识（`PausedAt`）**：换了暂停段一律从零开始。少了这一句，
  「投屏中暂停 → 用户点恢复 → 一秒后勿扰又挂起」会在新一段的第一拍把上一段残留的
  `Suppressed` 当成「压制刚解除」立刻补弹 —— 而用户刚点过恢复（`TestPauseNoticeNewSegmentStartsFresh`）。
- **空闲 / 应用占用不算压制原因**：人不在座位上弹窗没人看（也不尴尬）；应用占用只说明
  某个软件在跑，屏幕还是私人的——真正决定「能不能弹」的是屏幕会不会被别人看到。
- **7.5 秒兜底关窗要按窗口指针比对**：定时器是异步的，极端情况下已经弹过下一扇，
  无脑按名字关会关掉新窗（与其余固定名窗口同一个坑）。

### 14.5 验证

| 项 | 结果 |
|---|---|
| `go test -count=1 -tags server ./...` | 全绿（含新增 14 条） |
| `go vet -tags server ./...` / `gofmt -l` | 干净 |
| GUI（不带 tag）与 server 两种构建 | 均通过；`grep -a pause.html` 两个二进制都命中 |
| `smoke_server.py` | 189 项，失败 0 |
| `probe_bindings_full.py` | 140 项，失败 0（含新增 2 条） |
| 页面探针（Playwright + 系统 Edge，300×76 视口） | 文案 `已暂停 1 小时`、三个元素无溢出、无 JS 错误、点击与 Esc 都调到绑定；长文案 `已暂停 23 小时 59 分钟` 也不挤掉按钮 |

新增回归：

| 用例 | 断言 |
|---|---|
| `TestPauseNoticeSuppressedMatrix` | 四条危险原因各自压制；空闲 / 应用占用不压制；条件登记为 false 不等于生效 |
| `TestPauseNoticeDue` | 差 1 秒 / 整满 / 超出一小时；从上次弹出重算；时钟回拨不弹；无暂停时刻不弹 |
| `TestPauseNoticeStepTimeline` | 开会两小时全程投屏一次不弹 → 投屏断了补弹一次 → 下一拍不连弹 → 再满一小时才弹 |
| `TestPauseNoticeStepEdgeRespectsMinGap` | 刚弹过 30 秒，压制解除不重复弹 |
| `TestPauseNoticeStepPlainHourly` | 每分钟推进 59 拍都不弹，第 60 分钟弹 |
| `TestPauseNoticeNewSegmentStartsFresh` | 投屏中暂停 → 恢复 → 一秒后勿扰又挂起：新一段第一拍不弹（不继承上一段的压制标记） |
| `TestPauseNoticeTextWording` | 42 分钟 / 1 小时 / 1 小时 35 分钟 / 不足一分钟四种输出；断言不含「休息」「提醒」等字眼 |
| `TestPauseNoticePageHasNoRestWording` | 三个页面源文件（含注释与标题）都不出现该词；html 必须引入 shim.js |
| `TestPauseNoticeWindowNameMatchesURL` | 窗口名与 URL 的 `?win=` 一致，文案经 query 正确编码 |
| `TestPauseNoticeIgnoresManualPause` | 手动暂停 tick 一次，节流不记账 |
| `TestPauseNoticeAutoPauseStartsQuiet` | 自动暂停刚开始（未满一小时）不弹，节流状态跟着在走 |
| `TestPauseNoticeFiresAfterOneHour` | 构造「暂停已一小时」→ 记账弹出；恢复后节流清零 |
| `TestPauseNoticeFiresWhenCastEnds` | 投屏中满两小时不弹；条件消失那一拍补弹；下一拍不重复 |
| `TestPauseNoticeDismissKeepsThrottle` | 关闭提示不重置节流 |

### 14.6 还没做

- **真机端到端未验**：真实暂停满一小时、真实投屏断开后的补弹 —— 沙箱改不了系统状态，
  也等不了一小时。窗口建窗路径与提前提示窗逐字同构（同一个 `NewWithOptions` 用法），
  但没有真跑过。
- 手动暂停加时长（30 分钟 / 1 小时 / 2 小时），补两类只剩手动可用的场景：有线**扩展**接
  投影仪（判据故意不认）、会议室里笔记本屏直接被旁人看到（没有可检测的信号）。

  **更正（2026-11-05，用户指出）**：这里原先写的是「补腾讯会议共享单个窗口检测不到的盲区」，
  是错的。共享屏幕 / 共享单个窗口走的是**软件采集**通路，`SetWindowDisplayAffinity` 已经覆盖：
  共享单个窗口时提醒窗根本不在采集范围内，共享整个屏幕时亲和性让它从捕获画面里消失。
  亲和性不是「检测」，是「排除」—— 它不需要知道你在共享。需要检测的只有**投屏**（物理显示
  输出），那条通路亲和性够不着。所以「共享」不构成盲区，真盲区只有上面那两类。

## 15. 定时手动暂停（2026-11-05）

### 15.1 需求

「手动暂停加时长」：30 分钟 / 1 小时 / 2 小时 / 到明天。最初提的是「到明早 8 点」，
评审后改为**到明天 0 点**：零点是日期边界，「下一个零点」永远唯一，既不用为「8 点是几点」
加一个配置项，也不会在 7:00–8:00 之间产生「这算今天还是明天」的歧义。

### 15.2 为什么

第 14 节把「共享屏幕」从盲区里划掉之后，真正只剩两类：

| 盲区 | 为什么没有信号 |
|---|---|
| 有线**扩展**接投影仪 | 判据故意不认（认了会让双屏办公的人永久不弹），README 有完整对照表 |
| 会议室里笔记本屏被旁人直接看到 | 不投屏、不共享，摄像头 / 麦克风又已拍板不查 |

共同点：人**进会议室前就知道**接下来一小时不该弹。让他那一刻点一下，比让软件去猜准。

### 15.3 语义

定时暂停仍然是**手动暂停**（原因 `manual`），窗口照常走完、不跳过提醒；新增的只有
「到期时刻」和「到期后按离开口径重锚」。到点不是无条件恢复，要过一道闸：

| 到点时的状态 | 行为 |
|---|---|
| 没有任何条件生效 | 立即恢复，与手点「恢复休息」同一条路径 |
| 有六条之一生效（空闲 / 勿扰 / 全屏 / 复制 / 无线 / 指定应用） | **保持暂停**，托盘写「暂停时长已到，等「全屏独占」结束」，条件消失那一拍恢复 |
| 手动 + 自动原因叠加 | 只摘 manual，自动原因继续挂着（既有语义） |

与「每小时提示」的压制表**刻意不同**：那张表问「屏幕会不会被别人看到」（只有四条），
这张问「现在恢复会不会打扰」（六条全算）。两张表在代码里是 `pauseNoticeSuppressed`
与 `pauseHoldReasons` 两个函数，注释互相点名，不许合并。

### 15.4 四个坑（都已在实现里修掉）

一、**空闲不在登记表里**。`autoReasonActive` 由 `applyAutoPause` 写，而空闲走
`idleWatchTick` 直接 `Pause` / `Resume`，从来没人登记。于是「23:55 设到明天、合盖睡觉、
7:30 唤醒」会在第一拍读到一个空的登记表，当场恢复，接着昨夜冻结的剩余时间走 —— 开机
几分钟后弹「该休息了」。修法：`pauseActiveSnapshot` 单独补一条，看 `PauseReasons` 里
是否还挂着 idle（人回来时 `Resume(idle)` 会摘掉它）。

二、**手动恢复后旧截止时刻还在**。定时暂停中点了「恢复休息」，勿扰还挂着 →
`Resume(manual)` 因为 `PauseReasons` 非空提前 return，截止时刻没清。下一拍到期判定
又会把它当成「时长已到」，把用户刚做过的操作说成一件待办。修法：到期判定先看 manual
在不在，不在就直接退出。

三、**恢复要重锚**。无限手动暂停恢复是「冻结平移」，定时暂停必须按「离开」口径重锚：
它表达的是「接下来一段时间我不工作」，回来重新计一个完整周期才合理。跨夜那条更要紧 ——
零点前几分钟合盖睡觉时，空闲检测还没挂上就到了期，不重锚第二天开机就接着昨夜的剩余
时间走。实现用 `pauseTimed` 标志，与 `pauseHadAuto` 并列参与 `reanchor`。

四、**托盘菜单不能把「暂停休息」改成子菜单**。那是最高频的一键操作，变两步不值当；
「恢复休息」也不该藏进子菜单。改成：`暂停休息` 保持单项 + 新增 `暂停一段时间 ▸`
（四档），暂停中把四项标签改成「改到 30 分钟后结束 …」，并显出一项
「取消定时（一直暂停）」。标签变化只在形态翻转时写原生菜单。

### 15.5 文案

| 状态 | 托盘提示 |
|---|---|
| 定时暂停中 | `暂停中 · 还有 48 分钟（到 22:00）` |
| 到期被挡 | `暂停时长已到，等「全屏独占」结束` |
| 无限暂停 | `已暂停 42 分钟：手动暂停`（原口径不变） |

定时暂停刻意**不说**「已暂停多久」：一夜过去看到「已暂停 14 小时」像出了故障，而
「还有 3 小时」回答的是他真正关心的事。设置页侧栏的状态格仍写「暂停休息」，悬停补一句
「到 22:00 自动结束」。

### 15.6 实现

| 文件 | 内容 |
|---|---|
| `internal/model/plan.go` | `SchedulerState.PauseDeadline`（0 = 无限） |
| `internal/scheduler/actions.go` | `PauseUntil(at)`；`resume` 清截止时刻、按 `pauseTimed` 重锚 |
| `internal/scheduler/scheduler.go` | `pauseTimed` 标志 |
| `cmd/workerrest/pause_expiry.go`（新） | `nextMidnight` / `pauseExpired` / `pauseHoldReasons` / `pauseActiveSnapshot` / `pauseExpiryTick` / `schedPauseFor` / `schedPauseUntilTomorrow` |
| `cmd/workerrest/tray.go` | 子菜单、动态标签、`timedPauseText` / `fmtPauseRemain` / `fmtPauseAt` |
| `cmd/workerrest/sys_watch.go` | 新增 5 秒一跳的「暂停到期」循环 |
| `cmd/workerrest/web/dist/settings.js` | 状态格悬停补「到 HH:MM 自动结束」 |

到期判定放在 App 层的 watchLoop 而不是 `scheduler.onTick`：`autoReasonActive` 本来就在
App 层，搬进调度器会变成两处真源；也不动 `Host` 接口，不牵动所有测试桩。5 秒粒度对
小时级到期完全够，且跨休眠天然正确（比绝对时刻，不看 tick 连续性）。

### 15.7 验证

| 项 | 结果 |
|---|---|
| `go test -count=1 -tags server ./...` | 全绿（新增 15 条） |
| `go vet -tags server ./...` / `gofmt -l` | 干净 |
| GUI 与 server 两种构建 | 通过 |
| `node --check settings.js` | 通过 |
| `smoke_server.py` | 189 项，失败 0 |
| `probe_bindings_full.py` | 140 项，失败 0（未加绑定，契约不变） |

新增回归：

| 用例 | 断言 |
|---|---|
| `TestPauseUntilSetsDeadline` | 到期时刻落进状态；改时长不重置 `PausedAt`；`PauseUntil(0)` 退回无限 |
| `TestPauseUntilZeroDoesNotCreateManualPause` | 纯自动暂停时「取消定时」不会凭空加 manual |
| `TestResumeClearsPauseDeadline` | 恢复清截止时刻 |
| `TestTimedPauseResumeReanchorsCountdown` | 定时暂停恢复重锚到满间隔 |
| `TestUntimedManualPauseStillKeepsProgress` | 对照组：无限手动暂停仍平移 |
| `TestNextMidnight` | 21:30 / 23:59:59 / 零点整 / 凌晨 / 年末 / 月末六种时刻 |
| `TestPauseExpired` | 差 1 ms / 整点 / 过期 / 无限 |
| `TestPauseHoldReasons` | 六条各自挡住；顺序固定；manual 不在清单里 |
| `TestTimedPauseText` | 未到期 / 到期被挡点名原因 / 兜底 |
| `TestFormatPauseRemain` | 8 档（含「不到 1 分钟」「14 小时」） |
| `TestFormatPauseAt` | 当天 / 明天 00:00 / 跨日日期 |
| `TestTimedPauseLabels` | 四档；暂停中说「改到…」而不是「暂停…」 |
| `TestTrayTextsTimedPause` | 定时暂停说「还有」，无限暂停保留原口径 |
| `TestSchedPauseForSetsDeadline` / `TestSchedPauseUntilTomorrow` | 入口落到正确时刻 |
| `TestPauseExpiryResumesWhenClear` / `HoldsWhenBlocked` / `HoldsWhenIdle` / `DoesNothingBeforeDeadline` / `IgnoresAfterManualResume` | 到期五态 |
| `TestSchedulerStateJSONHasPauseDeadline` | JSON 字段名钉住（前端读它） |

### 15.8 没做的

- **真机端到端未验**：真实「到明天」跨夜、真实会议中到期被挡 —— 沙箱改不了系统状态，
  也跨不了夜。判定与文案全部是纯函数 + 无头 App 覆盖，窗口与菜单路径没有真跑过。
- 档位固定四档，不做自定义分钟数（真要 45 分钟再加）。
- 不加「暂停 1 小时」快捷键（`pause` 保持切换，多一个键位会挤占用户的其他键）。
- 到期恢复不弹任何提示：用户自己设的时长，屏幕刚恢复就该安静。

## 16. 「探测读不到」与「条件不成立」不是一回事（2026-11-05）

### 16.1 由来

用户问「恢复迟滞是怎么回事」。查代码时发现，比迟滞更本质的问题是三条监视链路都把
**探测失败**当成了「条件不成立」：`Supported=false` 时 `Active` / `Names` 是空的，
代码继续往下走，等于宣称「勿扰关了 / 投屏断了 / 目标应用都退出了」。

| 链路 | 失败条件 | 旧行为 | 后果 |
|---|---|---|---|
| 勿扰 / 全屏 | `SHQueryUserNotificationState` 调用失败 | 两条都按「已关闭」走「条件消失」分支 | 误恢复提醒；清掉用户点过恢复的忽略表 |
| 复制 / 无线 | `QueryDisplayConfig` 失败（UAC 桌面、远程会话、拓扑重排） | 两条都按「已结束」走 | 同上，拔插线那几百毫秒就会走到 |
| 应用占用 | Toolhelp 快照拿不到句柄 | `hit=false` → 自动恢复 | 提醒恢复 → 下一拍又暂停 |

忽略表被清掉的后果不是「多暂停一次」而是「用户点过恢复的原因又挂回来」——用户会认为
按钮没生效。这类 bug 不报错、日志里还写着「已关闭」，只能靠代码审查发现。

### 16.2 修法：没有结论就什么都别做

三条 tick 各自抽出 `dndApply` / `castApply` / `appPauseApply`，第一句都是
`if !x.Supported { return }`。跳过的语义是「上一拍的结论继续生效」，不是「不暂停」：

- 条件登记（`autoReasonActive`）保持原值 → 「已暂停」提示的压制判定、定时暂停的挡路判定
  都还认为它在生效（保守方向）；
- 忽略表、`logged` 标志一律不动；
- 下一拍探测成功再按真实结果走。

副作用是「应用真的退出了、恰好这一拍失败」会让暂停多挂 4 秒，下一拍即恢复。相比误恢复
（用户正看着屏幕、提前提示窗可能闪一下）这点延迟无害。

抽成独立函数是为了可测：真机上没法让系统 API 按需失败。

### 16.3 应用占用再加一道拍数迟滞

探测失败之外还有真实的「短暂消失」：Teams / 微信 / 企业微信更新时会退出再起，正好落在
4 秒采样点上就是一次无意义的「恢复 → 再暂停」。新增 `appPauseExitConfirmMisses = 2`
（8 秒）：连续两拍都没命中才认退出。

四条约束，少一条都会出错：

| 约束 | 为什么 |
|---|---|
| 探测失败**不计入**计数 | 读不到不等于不在跑，否则「失败 + 失败」会被当成两拍确认 |
| 命中即清零 | 否则「抖一下 → 命中 → 又抖一下」会跨间隙累积成「连续两拍」，迟滞形同虚设 |
| 迟滞期间**冻结全部状态** | 尤其不能把 `autoReasonActive` 写成 false —— 那会让定时暂停的到期判定以为不再被挡而提前恢复 |
| 功能开关关掉**不走迟滞** | 用户关了「应用运行时暂停」却还暂停 4 秒，像是没生效 |

只有「此刻还挂着这条暂停」时才需要等第二拍：本来就没暂停，等一拍不改变任何结果，只会让
忽略表的解除晚 4 秒。

其余四条自动原因**不做迟滞**：它们「条件消失不自动恢复」，抖动只会多一条日志，窗口不会弹。
投屏的进入 / 退出本来就是插线、Win+P 这种一次性动作。空闲那条早就有回程去抖
（`idle.ActiveMs = 3000`，猫踩一下鼠标不算人回来了）。

### 16.4 验证

| 项 | 结果 |
|---|---|
| `go test -count=1 -tags server ./...` | 15 包全绿（新增 9 条 + 改 2 条既有语义） |
| `go vet -tags server ./...` / `gofmt -l` | 干净 |
| GUI 与 server 两种构建 | 通过 |
| `smoke_server.py` | 189 项，失败 0 |
| `probe_bindings_full.py` | 140 项，失败 0（绑定与契约均未变） |

新增回归：

| 用例 | 断言 |
|---|---|
| `TestDndProbeFailureChangesNothing` | 失败时不解除暂停、不清忽略表、不改条件登记 |
| `TestDndRealClearDoesClearIgnore` | 对照组：真查到关闭才清忽略、登记 false，且保持暂停 |
| `TestCastProbeFailureChangesNothing` | 两条投屏原因都被跳过 |
| `TestCastRealClearDoesClearIgnore` | 对照组：真断开才走「条件消失」 |
| `TestAppPauseProbeFailureKeepsPause` | 枚举失败不恢复、不清 `logged`、不改登记 |
| `TestAppPauseRealClearResumes` | 两拍都没命中才恢复 |
| `TestAppPauseMissStreakResetsOnHit` | 计数命中即清零，不跨间隙累积 |
| `TestAppPauseDisabledTakesEffectImmediately` | 关开关立刻恢复，不走两拍 |
| `TestAppPauseWatchAutoResumesWhenProcessExits`（改） | 第一拍保持、第二拍恢复 |
| `TestAppPauseManualResumeIgnoresUntilExitOrManualPause`（改） | 忽略表解除同样要两拍 |

**红绿验证**：把三处 `if !x.Supported { return }` 临时删掉，三条失败判据同时变红；
把 `appPauseExitConfirmMisses` 改成 1，两条迟滞用例变红。确认测试不是空测。

### 16.5 没做的

- 勿扰 / 全屏 / 投屏三条的**进入侧**没有去抖：一次误判就是一次暂停，但它们是「条件消失
  不自动恢复」那侧，误进入只会让提醒停一次，用户点一下即可。相比加拍数确认引入的复杂度，
  不值当。
- 真机上无法复现「系统 API 偶发失败」，这一条只有代码级与单测级证据。

## 17. 暂停期间不自动检查更新，手动入口照常（2026-11-05）

### 17.1 需求与由来

用户先问「暂停状态只存在于内存，关机重启就结束」，确认现状后决定不改（丢了就丢了），
随后提出：**暂停期间无论手动还是自动，都不要执行更新，也不要检查更新**——「错过就错过了，
大不了明天再检查，不差这一天两天的」。

第一版按这句话把「执行」也拦了（`StartUpdate` 暂停即拒绝）。用户随后收窄了口径：
**手动点「立即更新」可以正常执行，只是不执行「自动检查更新」**。最终落地的范围因此是：

| 动作 | 谁发起 | 暂停期间 |
|---|---|---|
| 自动检查更新（`autoUpdateLoop` → `update.Tick`） | 软件 | 跳过（当天作废） |
| 手动「检查更新」 | 用户 | 照常 |
| 手动「立即更新」（下载 → 替换 → 重启） | 用户 | 照常 |

判据是「**谁在做决定**」：自动检查是软件替用户做主，暂停期间不该越过用户；而按下按钮的
这一刻用户就坐在机器前，重启是他自己的选择，软件没有理由替他拒绝。

这条要求的实质是给「暂停」这个词划边界，而边界只画在**软件自己发起的动作**上：到点要不要
联网这件事是软件在做决定，暂停时就该放弃；用户亲手按的那个按钮不是软件的决定，不归暂停管。

### 17.2 检查侧：到点遇到暂停就当天作废

原先是「到点若暂停，30 分钟后再随机取一刻重试，反复」——整个暂停期间会被每半小时探一次。
改成：`update.Tick` 到点发现暂停 → `state.SkipDay()`，当天不再排期，等第二天的排期。

| 时刻 | 旧行为 | 新行为 |
|---|---|---|
| 10:23 到点，正在开会 | 推到 10:23+rand(30min) 再试 | 当天不查了 |
| 11:00 会议结束 | 11:00 后的某个重试点去查 | 仍不查（当天已作废） |
| 次日首次键鼠活动 | 重新排期 | 重新排期（不变） |

`State` 新增 `Skipped bool`：不能靠 `ArmedAt` 归零表示，因为零值的含义是「还没排期」，
会被下一拍当成「等首次活动」而重新排上。

调度循环里另有一道兜底：`Tick` 返回「去检查」之后、真正联网之前再判一次（两者之间隔着
一个 2 秒轮询间隔，暂停可能恰好在这期间开始），命中就 `SkipDay()` 并跳过这一轮。

### 17.3 执行侧：曾经加过闸，按用户要求撤掉

第一版在 `StartUpdate` 开头加了暂停闸（判据 `updatePausedRefusal()`，前端两个「立即更新」
入口也先看一眼 `LAST_STATE.paused`），理由是「更新会替换程序并重启自己」。

用户随即指出这个方向错了：**手动点「立即更新」应当正常执行，只是不执行「自动检查更新」**。
于是整体撤除：

- 删掉 `updatePausedRefusal()` 与 `StartUpdate` 里那次调用 —— 函数体里不再出现任何暂停判据；
- 删掉 `settings.js` 里的 `refuseUpdateWhilePaused()` 及两处调用（关于页按钮、更新弹窗按钮）；
- 自动侧原样保留：`Tick` 到点遇暂停 `SkipDay()`、`autoUpdateLoop` 联网前二次判定、`State.Skipped`。

`StartUpdate` 里留了一段注释说明「这里为什么不判暂停」，避免下一个人以「和自动检查保持
一致」为由把它加回来。

### 17.4 判据：谁在做决定

| 动作 | 谁发起 | 暂停期间 | 理由 |
|---|---|---|---|
| 自动检查更新 | 软件 | 跳过 | 软件替用户做主，暂停期间不该越过用户 |
| 手动「检查更新」 | 用户 | 照常 | 用户亲手点的；不发包、不重启、不弹窗 |
| 手动「立即更新」 | 用户 | 照常 | 用户亲手点的，且他就在机器前；重启是他自己的选择 |

一句话：暂停约束的是**软件自己发起的联网与替换**，不是用户当下的决定。用户点下去之后
发生的事（替换程序、重启自己）由他自己承担，软件不替他拦。

### 17.5 验证

| 项 | 结果 |
|---|---|
| `go test -count=1 -tags server ./...` | 15 包全绿（**撤闸后重跑**） |
| `go vet -tags server ./...` / `gofmt -l` | 干净 |
| `node --check settings.js` | 通过 |
| GUI 与 server 两种构建 | 通过 |
| `probe_bindings_full.py` | 140 项，失败 0（本轮重跑；绑定与契约未变） |

新增 / 改写的回归：

| 用例 | 断言 |
|---|---|
| `TestTickSkipsDayWhilePaused`（改） | 到点暂停 → 当天作废；恢复后（随机窗取 0）也不再补查 |
| `TestSkipDayMethod` | 兜底方法幂等；跨天 `Skipped` 清零 |
| `TestAutoUpdateTickSimulatedDay`（改） | 第 2 段：暂停到 13:00 → 当天 0 次检查；第 3 段：第二天照常 |
| `TestAutoUpdateSkipsDayWhenPausedAtDueTime` | 完整时间线：排期 → 暂停到点 → 恢复后整天不查 → 次日恢复 |
| `TestSkipDayStopsRestOfDay` | 作废后即使随机窗为 0 也不许再排期 |
| `TestStartUpdateNotGatedByPause` | 读源码断言 `StartUpdate` 在 `downloadPackage` 之前**没有**任何暂停判据（`schedPaused` / `IsPaused` / `updatePausedRefusal`） |

**红绿验证**：把 `Tick` 的暂停分支改回「30 分钟后再试」→ `TestTickSkipsDayWhilePaused`
与 `TestAutoUpdateSkipsDayWhenPausedAtDueTime` 变红（报「到点正在暂停应整天放弃，实得 1 次」）；
在 `StartUpdate` 里重新加一句暂停判断 → `TestStartUpdateNotGatedByPause` 变红（报出现了
`schedPaused`）。

### 17.6 没做的

- 真机端到端没验：真实暂停跨过排期时刻、真实在暂停中手点「立即更新」走完下载到重启 ——
  沙箱改不了系统状态，也不容易卡准随机排期时刻。调度与判据全部是纯函数 + 无头 App 覆盖。
- 暂停期间「关于」页仍会显示上次检查留下的「发现新版本」，按钮照常可点（按本条口径，
  这正是期望行为）。
- 暂停期间打开设置页，仍可能把**上次检查残留**的「发现新版本」弹窗弹出来：那条状态是本地
  持久化的，不发任何网络请求，与暂停无关（暂停挡的是「再去查一次」）。

## 18. 暂停 / 恢复专项深度检测（2026-11-05）

用户要求「对暂停、恢复进行深入细致全面检测和测试」。本节记录这轮的**方法、发现的缺陷、
修了什么、以及哪些只能靠真人配合验**，不再重复前几节已经写过的设计说明。

### 18.1 方法与覆盖面

四条腿并行，互相补位（任何一条单独都不足以说「暂停没问题」）：

| 手段 | 覆盖什么 | 结果 |
|---|---|---|
| 既有单测全谱（`internal/scheduler` + `cmd/workerrest` 里 19 个 test 文件） | 判据、状态机、统计口径、跳过/推迟语义 | 全绿 |
| **新增不变量随机序列**（`pause_invariants_test.go`） | 400 步随机 `Pause / Resume / ResumeQuiet / PauseUntil / PauseToggle`，每步验 5 条不变量 | 全绿（本轮揪出 2 处缺陷，见 18.2） |
| **新增端到端探针**（`probe_pause_cycle.py` + `pause_cycle_probe.js`） | 真页面点按钮 → 真绑定 → 真调度器：文案翻转、nextAt 冻结与平移、暂停中到点不弹窗、恢复后照常弹 | 25 项断言，连跑 3 轮 0 失败 |
| **新增自动暂停探针**（`probe_pause_autopause.py`） | 真 GUI 产物 + 真实进程占用（`ping.exe`）：命中 → 暂停 → 收全部提醒窗 → 暂停期不弹 → 进程退出后自动恢复 + 重锚 → 恢复后照常弹 | 13 项断言，0 失败 |

覆盖矩阵（✔ = 有回归/探针钉住，— = 只能真人验，见 18.5）：

| 暂停来源 | 进入 | 暂停中行为 | 恢复 |
|---|---|---|---|
| 手动「暂停休息」 | ✔ 单测 + E2E | ✔ 休息窗冻结、配置处置、自定义提醒按配置 | ✔ 冻结平移（E2E 实测 2.66 s） |
| 手动 + 定时（30 分 / 1 时 / 2 时 / 到明天） | ✔ 单测 | ✔ 到点过六条阻挡闸、被挡点名原因 | ✔ 到期重锚；撤销定时退回无限 |
| 空闲 | ✔ 单测（真实 `DecideIdleStep` + 真实统计搬账） | ✔ 强制跳过、不打扰 | ✔ 回来重锚（`ResumeQuiet` 不闪窗） |
| 勿扰 / 全屏独占 | ✔ 单测 + 探测失败对照 | ✔ 保留暂停等用户点 | ✔ 只手动恢复（条件消失也不自动恢复） |
| 复制模式 / 无线投放 | ✔ 单测（两条原因独立） | ✔ 保留暂停等用户点 | ✔ 只手动恢复 |
| 应用占用 | ✔ 单测 + **真 GUI + 真进程 E2E** | ✔ 收窗 + 强制跳过 | ✔ 自动恢复 + 重锚（含 2 拍退出迟滞） |

### 18.2 本轮发现并修掉的两处缺陷

都是「停止位」不干净：暂停结束后有两个字段没跟着归零，平时看不出来，叠在别的状态上就
开始说胡话。

| # | 现象（可复现路径） | 根因 | 修法 |
|---|---|---|---|
| 1 | 定时暂停中用户点「恢复休息」，但勿扰还挂着（暂停继续）→ 托盘继续显示「还有 25 分钟（到 22:30）」，菜单里还挂着「取消定时」 | `Resume(manual)` 只摘原因，`PauseDeadline` 要等 `PauseReasons` 清空才清；而勿扰那条按策略留着，永远等不到清空 | `resume()` 摘 `manual` 时同步清 `PauseDeadline`（`pauseTimed` 保留，恢复是否重锚的判据不变） |
| 2 | 恢复后 `PausedAt` 一直留着上一段的时刻；任何「pausedAt != 0 就认为暂停过」的判据都会误读，状态快照也带着无意义的旧值 | `resume()` 清 `Paused` / `PauseDeadline` / 两张去重表，唯独漏了段起点 | `resume()` 里 `PausedAt = 0`；下一段暂停必然重记（`Pause` 只在 `becamePaused` 时写） |

**红绿验证**（先让测试红、再修）：

- 撤掉 `St.PauseDeadline = 0` → `TestManualResumeEndsTimedSegment`、`TestTrayDropsTimedTextAfterManualResumeKeepsAuto` 红（报「手动那段结束后不该再有到期时刻，得到 1793861348143」）；
- 撤掉 `PausedAt = 0` → `TestResumeClearsPausedAt` 与不变量序列第 10 步红（报「已恢复却留着 PausedAt」）；
- 恢复两处 → 四条全绿，不变量 400 步全绿。

### 18.3 新增回归

| 用例 | 断言 |
|---|---|
| `TestPauseResumeInvariants` | 400 步随机操作，每步验：`Paused` ↔ `PauseReasons` 互为表里 / 暂停中有段起点、恢复后为零 / 恢复后无截止时刻 / 截止时刻必伴 `manual` / 同一原因不重复登记 |
| `TestPauseReasonIndependentResume` | 三条原因并存，摘中间一条不提前恢复、不误摘别的 |
| `TestPauseSameReasonIsIdempotent` | 监视器每跳重复 `Pause` 不重复登记、不重置段起点 |
| `TestResumeUnknownReasonIsNoop` | 摘不存在的原因完全无副作用 |
| `TestManualResumeEndsTimedSegment` | 定时 + 勿扰并存时点恢复：只清定时，勿扰仍挡住 |
| `TestResumeClearsPausedAt` | 恢复后段起点归零、下一段重记 |
| `TestTrayDropsTimedTextAfterManualResumeKeepsAuto` | 同场景下托盘文案回到「已暂停 X：勿扰」，不再说「还有 …」 |
| `TestTimedPauseExpiryAfterHoldClearsLeavesAutoPause` | **钉住当前语义**：到期被勿扰挡住、勿扰之后结束，暂停仍保持（等用户点）。将来若改成「到期授权恢复到底」，这条会立刻变红提醒按新口径重写 |

### 18.4 观察到的环境级偶发（与暂停逻辑无关）

`go test -count=5 -shuffle=on ./cmd/workerrest` 偶发 2 条用例在**清理阶段**失败：

```
TempDir RemoveAll cleanup: unlinkat C:/.../TestRemoveRoleClearsFixedRoleAndOrder20594196/002: The directory is not empty
TempDir RemoveAll cleanup: ... \TestRunAutoUpdateCheckRecordsDay有结论才记当天1428726700\001: ...
```

- 单独 `-count=20` 跑这两条：20 轮全绿；全包连跑 15 轮里出现 2 次。
- 是 Windows 临时目录清理的时序问题（文件句柄/索引器滞后），不是断言失败，也不在暂停链路上。
- 记在这里是为了「不把偶发当没事」，未做改动：要根治得给这两条用例换掉 `t.TempDir()` 的清理口径，
  收益与风险都不成比例。

### 18.5 只能真人配合的部分（未验）

| 项 | 为什么验不了 | 需要你做什么 |
|---|---|---|
| 托盘「暂停一段时间 ▸」四档 | 托盘是原生 Win32 菜单，探针点不到；`schedPauseFor` 也不是绑定 | 右键托盘 → 暂停一段时间 → 30 分钟，看提示是否变「还有 30 分钟（到 HH:MM）」 |
| 勿扰 / 全屏独占真实触发 | 要改系统状态（专注助手 / 全屏独占程序） | 开专注助手 → 应暂停；关掉后应**保持暂停**，点「恢复休息」才回来 |
| 复制模式 / 无线投放真实触发 | 要有第二块屏或无线显示适配器 | 按 Win+P 选「复制」→ 应暂停；改回「扩展」后仍保持暂停 |
| 空闲自动暂停与「人回来」 | 需真实 60 秒无输入，再用键鼠唤醒 | 静置 1 分钟 → 应暂停；动一下鼠标 → 应自动恢复且倒计时重头开始 |
| 跨夜「到明天」 | 需要真的过零点（进程不重启） | 睡前点「到明天」，第二天开机看提醒是否已恢复（关机即清空属预期） |
| 暂停热键 | 需要真实按键注入 | 按暂停热键，托盘图标/菜单文案应立刻翻转 |

### 18.6 验证数字

- `go test -count=1 -tags server ./...`：15 包全绿（新增 7 条暂停回归）
- `go vet -tags server ./...` / `gofmt -l`：干净
- `probe_pause_cycle.py`：25 项 × 3 轮，0 失败；真实 `config/settings.json` SHA-256 未变
- `probe_pause_autopause.py`：13 项，0 失败；真实配置未变
- **无法测量**：`-race`（本机没有 C 编译器，`go test -race` 直接报 `requires cgo`）。
  代偿手段：`-count=5 -shuffle=on` 重复 + 全包多轮，以及 18.4 记录的环境级偶发。

## 19. 全量覆盖率专项：全部页面 / 全部控件 / 全部窗口（2026-11-05）

用户要求「对整个软件，所有窗口、所有页面、所有选项，做全面细致深度检查与测试，要达到极高的
覆盖率」。这一节只记**方法、新发现、修了什么、哪些还得真人来**，设计语义不复述。

### 19.1 四条腿与各自补的盲区

| 层 | 手段 | 补的是哪个盲区 | 结果 |
|---|---|---|---|
| 静态 | **新增** `probe_static_audit.py`（52 项） | 资源在不在、脚本集合对不对、有没有内联脚本被 CSP 静默拦掉；**不需要浏览器，秒级** | 52 / 52 |
| 单测 | 覆盖率测绘 + 逐包补强（9 个包） | 「谁都被别处调用」的包在逐包统计里假装 0% | 见 19.3 |
| 页面运行时 | **新增** `probe_pages_sweep.py`（80 项）+ `probe_pause_page.py`（25 项） | 7 个页面从没在同一张表里比过；子资源 404 被 console 吞掉；暂停小窗此前从未被任何探针渲染 | 80 / 80、25 / 25 |
| 控件接线 | **新增** `settings_options_probe.js`（86 项，挂在 `probe_settings_ui.py` 第四支） | 逐个控件「操作 → 配置变 → 操作回去 → 逐字还原」；能抓死控件与接错键 | 86 / 86 |
| 真窗口 | **新增** `probe_windows_e2e.py`（16 项）+ `probe_tray_e2e.py`（6 项） | 无头构建不建窗口，尺寸/位置/生命周期/托盘点击全在窗口层 | 16 / 16、6 / 6 |

四层互相补位：静态审计看不见「运行到某个时机才去取 `seal-ink-7.svg`」这类动态引用；
内容探针用 mock shim，网络层错误会被它自己的断言淹掉；无头服务不建原生窗口，
`WebviewWindowOptions` 有没有接对只有真跑一次才知道。

### 19.2 静态层：`probe_static_audit.py` 抓到的两处真问题

| # | 问题 | 为什么要紧 | 修法 |
|---|---|---|---|
| 1 | `sound.html` 缺 `<html lang>` | 七个页面里唯一漏的；WebView2 靠它选断词与朗读语言 | 补 `lang="zh-CN"` |
| 2 | `settings.js` 里 `fillSupervisorImageName()` 引用 `#sup-geom-role`，HTML 里根本没有这个 id | 每次切专员都在查一个不存在的节点（`if` 兜住了，所以不报错，但这是纯死引用；改结构的人会被它误导） | 删掉那三行 |

预期脚本表也顺手对齐了：`role_thumb.js` 是被 `settings.html` **静态引用**的，
早先的清单漏了它，脚本一多就会「多出来的那个被当成孤儿」误报。

### 19.3 单测层：先看清覆盖率，再补

**遍历统计的陷阱**：逐包 `go test -cover` 会让只被别处调用的包显示 `0.0%`
（`plan` 913 行、`role` 1191 行、`hotkey` 197 行、`jsonx` 227 行全是假 0）。
必须 `-coverpkg=./...` 跨包统计才看得见真实覆盖：**全局 69.5%，802 个函数里 155 个零覆盖**。

补强后（同一口径实测）：

| 包 | 前 | 后 | 补的是什么 |
|---|---|---|---|
| `internal/plan` | 0.0%（假） / 59.5%（真） | **92.3%** | 跨午夜提前量、去重键、`describeTimeSlot` 排序、倒计时链展开、今日时间线六种分支 |
| `internal/role` | 0.0%（假） / 64.0%（真） | **38.6%**（真口径 89%） | 目录名净化（CON/尾随点/超长）、坏目录跳过、立绘 dataURL |
| `internal/jsonx` | 0.0% | **89.6%** | JS 语义的 `JsStr` / `ToNumber`、`NextSeqID` 取最大+1、原子写不留 `.tmp` |
| `internal/hotkey` | 0.0%（假） | **88.1%** | 16 种拒绝分支、修饰键别名、`"Ctrl+ "` 尾部空格是空格键、真注册 F24 后注销 |
| `internal/settings` | 59.1% | 64.1% | 品类兜底、`DisplayIDs` 去空、`WR_SETTINGS_PATH` 独占、归一化幂等 |
| `internal/stats` | 80.8% | 90.5% | 脏了才写、`NotifySuspend/Resume` 整段记离开 |
| `internal/logx` | 64.8% | 78.7% | `teeWriter` 文件失败要冒错、`AppLogger` 永不 nil |
| `internal/scheduler` | 84.6% | 86.3% | `PlanSummary` 文案与来源 |
| `internal/update` / `platform` | 92.8 / 66.0 | 93.8 / 70.0 | 本地日期、`WebView2` 下载地址必须 https + 微软域、隐藏窗两个标志 |

**仍然零覆盖、且不该硬测的**：`hotkey.GetMessage`（阻塞调用，只能在真消息循环里覆盖，
已由 `probe_windows_e2e.py` 的热键段间接证明）、`notifier` / `windnd` 的系统 API 包装层、
`cmd/workerrest` 里 120 个依赖 Wails 运行时窗口的绑定（由绑定级探针 140 项覆盖）。

### 19.4 页面运行时层

`probe_pages_sweep.py`：7 个页面各跑一遍同一组不变量 —— 页面 200 / 零 JS 报错 /
**零失败请求**（含子资源 404 与请求被中止）/ `html[lang]` / `<title>` / 无横向溢出 /
所有 `<img>` 真的解码成功 / 样式表真的生效 / 壳元素在且该露的露出来 / 页面专属契约。

页面专属契约里有两条是**真数据**，不是存在性判断：`index` 要求环境信息 ≥1 项、专员磁贴 ≥1 个
（实测 6 项 / 15 个），`prebreak` 要求倒计时秒数 > 0。

「零失败请求」这一条遇到过一个假阳性，处理方式记在这里：Wails 运行时会先 `fetch` 一下
`/wails/custom.js` 判断在不在，那次 fetch 被它自己 abort（`net::ERR_ABORTED`），紧接着用
`<script>` 正式加载，200 通过。判据用**「这个 URL 有没有成功过」**，不是按路径白名单放行 ——
`custom.js` 真丢了的时候 `okAssets` 里不会有它，照样会红。

### 19.5 设置页全控件接线矩阵

`settings_options_probe.js` 对六个 tab 里每个可见且可逆的控件做三步：操作它 → 配置必须有变化
（死控件会报「没有变化」）→ 记录落在哪些叶子 → 操作回去 → 配置必须逐字还原。
**general 20 / timing 14 / supervisor 1 / about 1**，共 36 个控件全绿，接线表逐条打印在探针输出里
（`general/castMirrorPause → castMirrorPause: true → false` 这种，便于人工核对「这个开关到底改了哪个键」）。

三个口径上的坑（都是探针自己的错，写在这里免得下次又踩）：

| 现象 | 真因 |
|---|---|
| 主题 `theme=light` 报「配置无变化」 | 单选框再点一次已选中的那枚**不发 change**。已选中时先点同组另一枚，再由「操作回去」点回本枚 |
| 主题 `theme=dark` 报「不可逆」 | 还原记的是「我这枚该不该选中」，而单选组的原状态在被选中的那枚上。改成先记「组里当下选中谁」 |
| `sup-size` / `sup-posx` / `sup-posy` 报「死控件」 | 只发了 `input`。这三个滑条是 `input` 改内存 + 刷预览、`change` 才走 `autoSave → persistRole` 写 `meta.json`，少发一个就验不到整条链 |

督休专员的尺寸/位置/翻转不写 `settings.json` 而写 `supervisor/<专员>/meta.json`，
所以这四枚控件单列一段，用 `listSupervisorRoles()` 读回来验（`{"sizePct":88,...}` 逐字比对）。

统计页的图表是 **DOM/SVG 折线**（`.rc-plot` 里由 `setupRestChart` 画 `<svg>`），**没有 canvas**；
早先按 canvas 数量断言会得到 0，属于量错了对象，改成量三个图表容器各自画出了东西。

### 19.6 真窗口端到端：发现的缺陷与修法

`probe_windows_e2e.py` 三段（各自起一次进程，避免上一轮的调度线干扰）：

| 段 | 怎么触发 | 断言 |
|---|---|---|
| A 短休息全流程 | 倒计时 45 s + 提前 20 s | 约 23 s 弹「即将休息」、约 43 s 弹「休息一下」、实测提前 **20.1 s**、存活 20.1 s 后自己关掉；日志里 `preBreak` 算的 `400x240 @(2136,1128)` 与实际窗口**逐像素一致** |
| B 外卖提醒 | 槽设在 now+60 s 那一分钟、提前量 60 s（提前线落在当前分钟） | 预通知秒级弹出、主窗在原定时刻弹出、主窗弹出时预通知已收掉、日志有「触发提醒 15:23 lead=60秒」 |
| C 全局热键 | `SendInput` 发 `Ctrl+Alt+Shift+F24`（对应 `shortcuts.nextBreak`） | **0.4 s** 后弹出「休息一下」——注册、分发、建窗一条链打通 |

**A 段布局分支用 95%（`widthPct/heightPct`），B 段用 `fullscreen=true`**，两条分支各覆盖一次。

**本轮发现并修掉的缺陷：`全屏` 对外卖窗只生效一半。**

| 项 | 内容 |
|---|---|
| 现象 | `appearance.fullscreen = true` 时，休息窗铺满工作区，外卖窗却是 **2432×1322**（工作区 2560×1392）—— 95% 的方块贴在左上角，右下留一条 5% 的空隙 |
| 根因 | 休息窗走纯函数 `breakBoundsFor`（`fullscreen` 直接返回 `wa.X/Y/W/H`），外卖窗在 `takeout_windows.go` 里**自己手算**：尺寸只认 `widthPct/heightPct`，`fullscreen` 只用来决定「要不要居中」 |
| 修法 | 把 `breakBoundsFor` 改名 `reminderBoundsFor` 并让外卖窗也调它 —— **同一份外观配置只能有一处解读** |
| 红绿 | 改前三段 A/B 全绿、B 的尺寸断言红（2432×1322 ≠ 2560×1392）；改后 **2560×1392 = 工作区**，`probe_display_target.py`（18 项）与 `probe_capture_guard.py`（17 项）复跑全绿，说明多屏与捕获保护没被牵动 |

### 19.7 托盘端到端

托盘是原生 Win32 图标，点不到，但 Wails 给它建了一个**消息专属窗口**（`HWND_MESSAGE` 下的
`WailsWebviewWindow`，`EnumWindows` 看不到它）。`probe_tray_e2e.py` 直接找到它、投递它自己注册的
回调（`WM_USER+1`，`lParam` 带鼠标事件），看真实反应：

- 对照组（不最小化）→ 设置窗口一开始就可见、托盘宿主已建、单实例哨兵 `wails-app-com.goodgoodlive.workerrest-sic` 在
- `startMinimized=true` → 设置窗口**对象存在但不可见** → 投 `WM_LBUTTONUP` → **窗口被唤出**
- 投 `WM_RBUTTONUP` → 出现 `#32768` 菜单窗口（右键菜单真的弹了）

`WM_USER+1` / `WM_LBUTTONUP` 这两个值是 Wails v3.0.0 的框架契约
（`pkg/application/systemtray_windows.go` 的 `wmUserSystray` / `wndProc`）。升级 Wails 后这里若失败，
先去看那个文件的常量，别急着改断言。

### 19.8 只能真人配合的部分

| 项 | 为什么验不了 | 需要你做什么 |
|---|---|---|
| 托盘图标真的出现在通知区 | `Shell_NotifyIcon` 只有「加/改/删」，没有查询接口 | 看任务栏右下角有没有图标；被折叠进溢出区也算正常 |
| 托盘 tooltip 与菜单文案 | 原生 tooltip 不可读；文案逻辑本身有 `trayTexts` 单测 | 悬停图标看「约 N 分钟后短休息」，右键看「下次…：约 N 分钟后」 |
| 暂停每小时提示小窗（`pause.html`） | 只在**自动**暂停满 1 小时后才弹（手动暂停按设计不弹），探针时间窗内到不了 | 开专注助手挂一小时（或用空闲暂停挂一小时），看是否弹一次、点「恢复」是否回到工作 |
| 立绘/台词在 4K 与 100%/125%/150% 缩放下的观感 | 需要真实缩放比与真眼 | 三档缩放下各看一眼立绘有没有糊、名字牌有没有压脸 |
| 双击托盘、托盘菜单各项的实际效果 | TrackPopupMenu 是模态原生菜单，点不到菜单项 | 挨个点一遍：立即休息 / 暂停 / 推迟 / 跳过 / 重置周期 / 开机自启 / 退出 |
| 声音真的响 | 探针把 `sound.enabled` 关了（免得在用户机器上放声音） | 开着声音跑一次短休息，听开场与结束提示音 |

### 19.9 本轮数字

| 项 | 数字 |
|---|---|
| `probe_static_audit.py` | 52 项，0 失败 |
| `probe_pages_sweep.py` | 80 项（7 页 × 11 项 + 专属契约），0 失败 |
| `probe_pause_page.py` | 25 项，0 失败 |
| `probe_settings_ui.py` 四支合计 | 122 + 57 + 58 + 86 = **323 项**，0 失败 |
| `probe_windows_e2e.py` | 16 项三段，0 失败 |
| `probe_tray_e2e.py` | 6 项，0 失败 |
| `probe_display_target.py` | 18 项，0 失败 |
| `probe_capture_guard.py` | 17 项，0 失败 |
| `probe_window_gui.py` | 9 项，0 失败 |
| `go test -count=1 -tags server ./...` | 18 个包全绿（`model` / `notifier` 无测试文件） |
| Go 覆盖率（`-coverpkg=./...`） | 全局 69.5%；本轮把 9 个包抬到 64%–94% |
| **无法测量** | `-race`（本机无 C 编译器，`requires cgo`）；代偿：`-count=5 -shuffle=on` + 不变量随机序列 |

三处改动后的产物重建与真实配置校验：`config/settings.json` 的 SHA-256 在所有隔离探针跑完后未变。

### 19.10 一处环境级偶发与处置（不藏）

`probe_renderer.py` 首轮报 1 失败：「外卖窗口：页面无控制台错误」，内容是

```
[shim] 绑定调用失败 main.App.SupActivity: TypeError: Failed to fetch
```

- 复跑同一份代码：479 / 479 全绿。3 次里红 1 次。
- `SupActivity` 是「用户在任意窗口动了键鼠」的心跳上报，**fire-and-forget**，调用方不读结果，
  页面对它也没有失败分支；这次 `fetch` 被浏览器中止（页面正在关闭，或 server 模式下 HTTP
  连接被并发挤掉）后界面不会少任何东西。
- 处置：在渲染探针的 console 收集处只放过这一条，判据钉死到
  `[shim] 绑定调用失败 main.App.SupActivity: TypeError: Failed to fetch` 整串。
  绑定真丢了（方法不存在）、或它抛的是别的错，照旧上报；「SupActivity 在不在」另有契约自检
  （57 个方法全在）与绑定级探针兜底。
- 为什么不是纯放宽：这条断言原本要守的是「页面渲染过程中 JS 没炸」。一个心跳请求在关闭瞬间
  被中止不属于这个范畴，把它算进去只会训练人「红了就重跑」。

## 20. 系统缩放（DPI）对设置窗口的影响（2026-11-05）

用户问：「设置窗口设置了默认尺寸，如果系统设置了缩放，会不会导致样式有问题？特别是关于页面，
整个页面内容能否在默认尺寸下显示全？」

这一节按三层回答：**缩放本身改不改布局** / **缩放间接改了什么** / **默认尺寸下到底显不显示得全**。

### 20.1 结论先行

| 问题 | 答案 |
|---|---|
| 缩放会不会打乱布局（字体、间距、换行）？ | **不会。** 5 档设备像素比（1 / 1.25 / 1.5 / 2 / 2.5）下，六个页面「所有可见元素高度之和」逐页差 **0px**，CSS 视口恒为 874×857 |
| 默认尺寸下关于页能不能显示全？ | **能。** 内容区可视 813px、内容需要 813px，溢出 0 —— 恰好不滚，且这是**实测复算**，不是引用旧数字 |
| 那缩放到底影响了什么？ | **只影响窗口能被开多大。** `fitSizeToScreen` 用 `WorkArea / ScaleFactor` 夹紧窗口，缩放越大、屏幕越小，窗口被夹得越矮，关于页就必须滚动（25 种组合里 14 种） |
| 被夹矮了会不会「看不全」？ | **不会看不全，但要滚。** 25 种组合下横向裁切 0 处、滚到底都能看到最后一块 |

### 20.2 为什么缩放不改布局：先把前提证死

Wails v3 的 `WebviewWindowOptions.Width/Height` 是 **DIP**（Windows 侧 `bounds` 走
`absoluteToRelativeDipPoint`），Wails 又把进程设成 **PerMonitorV2** DPI 感知
（`SetProcessDpiAwarenessContext(PER_MONITOR_AWARE_V2)`），所以 1 CSS px ≈ 1 DIP：
125% 下 874 DIP 的窗口是 1092 物理像素，但网页拿到的仍是 874 个 CSS px。

这个前提不能只写在注释里 —— 它是整节结论的地基，所以有两道实证：

1. **无头侧**（`probe_dpi_layout.py` 第 1 段）：同一视口 874×857，换 5 档
   `deviceScaleFactor`，逐页抓「所有可见元素矩形高度之和」。任何字体度量、行高、
   换行差异都会让它漂移 —— 实测六页最大差 **0px**，且每档 `innerWidth/innerHeight`
   都还是 874×857。
2. **真窗口侧**（`probe_window_gui.py` 新增两条）：用 `GetClientRect` 拿客户区再折
   DIP。实测 **874×858**（858 是 1px 取整）。无边框窗口靠 `WM_NCCALCSIZE` 把客户区
   撑满整个窗口，**不可见的拖动边框没有吃掉网页视口** —— 否则「857 = 关于页恰好不滚」
   这个实测值会整体失效。

### 20.3 默认尺寸下关于页的实测

```
默认 874×857：内容区可视 813px / 内容需要 813px / 溢出 0px
```

813 = 857 − 自绘标题栏 48 − `#content` 上下 padding 26/20 − 底部透明边框 6。
`SettingsWindowDefaultHeight = 857` 这个「恰好不出滚动条」的实测值，**在本轮仍然成立**。

顺带量到一条与宽度有关的：窗口宽 760（最小宽度）时，关于页需要 **893px** ——
窄了之后「数据与隐私」的说明文字换行多一行。所以 857 只对 874 及更宽的窗口成立，
这是 `--label-w` 与文本换行共同决定的，改宽度就要重测。

### 20.4 缩放间接改的：窗口被夹矮

`fitSizeToScreen` 的夹紧是「工作区 DIP − 16 / − 40」。25 种「分辨率 × 缩放」组合的实测：

| 屏幕 | 缩放 | 工作区 DIP | 窗口 DIP | 关于页溢出 |
|---|---|---|---|---|
| 1920×1080 | 100% | 1920×1040 | 874×857 | 0 |
| 1920×1080 | 125% | 1536×824 | 874×784 | 73px |
| 1920×1080 | 150% | 1280×680 | 874×640 | 217px |
| 2560×1440 | 150% | 1706×920 | 874×857 | 0 |
| 3840×2160 | 200% | 1920×1040 | 874×857 | 0 |
| 1366×768 | 125% | 1092×574 | 874×534 | 323px |
| 1366×768 | 150% | 910×472 | 874×432 | 425px |

**结论：这是设计上的取舍，不是缺陷。** 内容本来就能滚动，窗口比屏幕矮可以滚，
窗口比屏幕高是没救的。所以 100% / 150%-大屏 / 200%-4K 这些常见组合都不滚，只有
「小屏 + 大缩放」才需要滚一滚。

### 20.5 本轮抓到的缺陷：最小高度 600 会把窗口顶出屏幕

| 项 | 内容 |
|---|---|
| 现象 | 8 / 25 种组合下窗口高 **600**（最小高度兜底），而工作区 DIP 高只有 500 / 574 / 472 / 344 …… 窗口底部 **26～128px 永久落在任务栏后面**，`#content` 的滚动条也拖不到底 |
| 最现实的触发 | **1366×768 @125%**（大量办公笔记本的出厂配置）：工作区只有 574 DIP，窗口却开 600 |
| 根因 | `fitSizeToScreen` 里 `if maxH < SettingsWindowMinHeight { maxH = SettingsWindowMinHeight }` —— 兜底把「工作区高度」抬成了「设计下限」，于是夹紧反而制造了超屏窗口。代码注释写的意图是「宁可矮一点让内容滚动，也不要开出一扇超出屏幕的窗口」，实现与意图相反 |
| 修法 | ① 抽出纯函数 `fitSizeInWorkArea`，`maxH` 不再被 `minH` 抬起，只保 `waH - 40`；② 新增 `settingsWindowMinHeight()`，主屏工作区比 600 还矮时把最小高度降到 `waH - 40`（下限 240），并在 `applySettingsWindowSize` 里 `SetMinSize` 跟着当前屏走 |
| 红绿 | 修前 `probe_dpi_layout.py` 的「窗口高度不会超过工作区」红，列出 8 组；修后 **0 处**，24 项全绿 |
| 附带 | 宽度下限 760 **刻意不让位**（见 20.6） |

最小高度让位后的窗口：1366×768@125% → 534；@150% → 432；1080p@150% → 640。

### 20.6 刻意的取舍：宽度不让位

工作区窄到 760 以下（= 200%+ 缩放的小屏，如 1366×768@200% 只剩 683 DIP）时，
窗口宽仍保 760，右侧会超出屏幕。理由：760 是**实测的布局硬下限**（740 时 `.row.field`
横向溢出 2px），把窗口压到 683 会让输入框直接裁掉，比「宽出去 77px」更难用。
`TestFitSizeInWorkAreaWidthFloorIsDeliberate` 钉住这个行为，免得以后有人「顺手对称一下」。

### 20.7 残存边界（已知，未改）

| 项 | 说明 |
|---|---|
| 跨屏拖动不重新夹紧 | `applySettingsWindowSize` 只在**每次 Show 之前**调用。把窗口从 100% 屏拖到 150% 屏时不会立刻重算，底部会暂时超屏，下次打开窗口时自动纠正 |
| 窗口被夹矮后需要滚动 | 见 20.4，属于设计取舍。滚到底能看全（25 种组合全部通过尾部可达性断言） |
| 打包时 WebView2 的独立缩放 | 若用户在别处给 WebView2 设过页面缩放（`--force-device-scale-factor` 之类），CSS 视口会跟着变。产品没有设置这一项，默认 1.0 |

### 20.8 本轮数字

| 项 | 数字 |
|---|---|
| `probe_dpi_layout.py` | 24 项，0 失败（5 档 DPR × 6 页 + 25 种分辨率×缩放 + 4 档宽度） |
| `probe_window_gui.py` | 11 项，0 失败（新增「客户区 = 网页视口」2 项） |
| `window_size_fit_test.go`（新增） | 6 个用例，全绿（25 种组合不超屏、大屏保持默认、矮屏夹到 534、宽度下限刻意不让位、极矮工作区、取不到屏时回落） |
| `go test -count=1 -tags server ./...` | 18 个包全绿 |
| `go build ./cmd/workerrest` | 通过 |
