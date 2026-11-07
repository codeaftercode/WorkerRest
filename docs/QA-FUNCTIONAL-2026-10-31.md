# 功能性测试方案与执行报告 — Go + Wails v3.0.0 (Windows)

> 测试日期：2026-10-31　测试对象：WorkerRest（吃饼人也敢休息？）HEAD `ea5a557`，tag `v0.9.6`
> 测试负责人：Horizon（软件测试专家）
> 纪律：功能规格先于用例；每项结论附可追溯证据；无法自动化的项标注“需人工执行”并给出精确步骤，不伪造执行结果。

---

## 0. 执行能力与范围声明

### 可自动执行的能力（本机实测）
| 能力 | 证据 |
|---|---|
| 无头端到端功能测试（`-tags server` 构建的真实二进制，经 Wails HTTP 端点调用真实绑定） | `python scripts/smoke_server.py` → **181 项断言，失败 0** |
| 补充功能测试（时间基准 / 配置损坏注入 / 持久化 / 恢复默认） | `python scripts/qa_func_supplement.py` → **29 项断言，失败 0**（本报告新增） |
| 绑定方法输入边界 / 路径遍历 / 非法 scheme 注入 | 内联脚本 → 10/10 通过（本报告新增） |
| 静态可访问性 / 语义审计（纯 Node，不依赖 GUI） | `node scripts/a11y_audit.js` → 全部页面无名控件 0、设置页 10 对话框、1 活区 |
| 读日志 / 读进程 / 读文件 / 读配置 | 全程使用 |

### 不能执行、需人工的项（如实声明）
| 项 | 原因 |
|---|---|
| **`go test` 单元测试** | 本机 **Go 工具链缺失**（不在 PATH、不在常见安装位置、无 scoop/winget 安装；`find /c /d -name go.exe` 零命中）。已有 `*_test.go` 单元测试无法本机执行。 |
| **构建 production exe / `wails3 build`** | 同上，`go build` 不可用。本报告在已构建的 `build/bin/rest_server.exe`（2026-10-31 20:46）上执行无头端到端测试。 |
| **真实 GUI 交互**（窗口拖拽 / 最小化恢复 / 关闭路径 / 卡死检测 / 单实例窗口前置） | 需要真实显示器 + WebView2 渲染进程 + 带控制台的 GUI 探针产物（`rest_probe.exe`，需 Go 构建）。本机无法构建该产物。 |
| **多显示器不同 DPI** | 本机单显示器。 |
| **修改系统时间 / 触发真实睡眠 / 休眠** | 沙箱环境无权限、无真实电源状态。 |
| **稳定性长时运行（2 小时句柄采样）** | 需真实 GUI 进程长跑（无头 server 不建窗口，句柄口径无意义）。 |
| **更新链真实升级（v0.9.5→v0.9.6）** | 无上一版安装包、无干净 VM。 |

### 测试所用构建与环境
| 项 | 值 |
|---|---|
| 测试二进制 | `build/bin/rest_server.exe`（`-tags server` 无头构建，20,700,672 字节，2026-10-31 20:46） |
| production 构建路径 | `build/bin/rest.exe`（14,458,880 字节，2026-10-31 11:26；**本机无法重新构建**，见上） |
| 操作系统 | Windows（win32），Administrator 用户 |
| 配置隔离 | `WR_SETTINGS_PATH` 独占指向 `%TEMP%` 隔离副本，测试收尾清理 |
| Go 版本（编译期） | go1.27.3（`go.mod` 语言版本 1.26.3 + toolchain） |
| Wails 版本 | v3.0.0 正式版（`go.mod:7`） |

---

## 1. 功能规格逆向梳理

### 1.1 功能清单

| 类别 | 功能 | 来源 |
|---|---|---|
| **核心业务** | 短休息提醒（倒计时/固定时刻） | `breakplan.go:562` `builtinCountdownLine`；`scheduler.go` |
| 核心业务 | 长休息提醒（倒计时/固定时刻，每 N 次短休息接一次长休息） | `breakplan.go:562-590`；`settings.go:171` `LongEveryNShort` |
| 核心业务 | 自定义提醒（第 3 种及以后，独立触发线） | `breakplan.go:304` `CustomBreakList`；`settings.html:183` 时间规划页 |
| 核心业务 | 休息窗口（无边框透明，每屏一个，含督休专员立绘/台词/倒计时） | `app.go:807` `ShowBreakWindow`；`frontend/dist/break.html` |
| 核心业务 | 提前提示窗（右下角小窗，倒计时+动作按钮） | `prebreak.go`；`frontend/dist/prebreak.html`；`scheduler.go:840` `preBreakWanted` |
| 核心业务 | 外卖提醒（独立于调度器，秒级 tick，含优惠券二维码） | `takeout.go`；`frontend/dist/takeout.html` |
| 核心业务 | 督休专员系统（内置 17 个 + 自定义，立绘/台词/顺序/随机/固定） | `role.go`；`app.go:370` `buildSupervisorSessionWith`；`frontend/dist/settings.html:329` |
| **时间相关** | 倒计时（countdown 模式，`intervalMs` 间隔） | `breakplan.go:588` `At: now + cfg.IntervalMs` |
| 时间相关 | 固定时刻（fixed 模式，`slots` 时刻表，daily/weekly） | `breakplan.go:592` `fixedLines`；`nextSlotAt` |
| 时间相关 | 倒计时链展开（今日时间线） | `breakplan.go:828` `expandBuiltinCountdown` |
| 时间相关 | 时长统计（工作/休息/离开/连续工作，跨零点切分） | `stats.go`；`frontend/dist/settings.html:429` 统计报表页 |
| 时间相关 | 暂停/恢复（手动/空闲/勿扰/全屏/指定应用，多原因并存） | `scheduler.go:690` `pause`/`resume`；`sys_idle.go`/`sys_dnd.go` |
| 时间相关 | 推迟/跳过/提前结束 | `scheduler.go:569/629/666` |
| **配置/设置** | 通用配置、时间规划、督休专员、统计报表、快捷键、关于 6 页 | `settings.html:42-47` |
| 配置 | 保存设置（自动保存，改一下调一次） | `bindings_settings.go:38` `SaveSettings` |
| 配置 | 恢复默认设置（备份+回退+保留 installId） | `bindings_settings.go:93` `ResetSettings` |
| 配置 | 快捷键（推迟/提前结束/暂停/立即休息） | `hotkeys.go`；`settings.html:416` |
| **文件/数据** | 导入自定义专员立绘（选图） | `bindings_settings.go:294` `PickSupervisorImage` |
| 文件/数据 | 导入音乐（白名单+20MB 上限+副本） | `bindings_settings.go:417` `AddMusicSound`；`sound.go` |
| 文件/数据 | 导出报表（CSV/PNG） | `bindings_settings.go:372` `ExportReport` |
| 文件/数据 | 统计文件（损坏备份重建+弹窗告知） | `stats.go:396` `Load`；`GetStatsFileNotice` |
| **系统集成** | 系统托盘（状态/动作/暂停/自启动菜单） | `tray.go`；`main.go:152` 单实例 |
| 系统集成 | 开机自启动（注册表 HKCU\Run） | `sys_autostart.go`；`settings.go:513` `Autostart` |
| 系统集成 | 单实例运行（第二个实例退出+首实例窗口前置） | `main.go:152-158` |
| 系统集成 | 共享屏幕时隐藏提醒窗口（SetWindowDisplayAffinity） | `main.go:135-141`；`capture_guard.go` |
| 系统集成 | WebView2 运行时预检 | `main.go:99-112`；`webview2_check.go` |
| 系统集成 | 自动更新（检查/下载/SHA-256 校验/替换 exe） | `update.go`；`update_trust.go` |
| **通知提示** | 提示音（内置/自定义，单曲/顺序/随机） | `sound.go`；`settings.go:1047` `normalizeSound` |
| 通知提示 | 系统通知（Windows 上改自绘弹窗） | `scheduler.go:247`；`app.go:710` `onPreNotify` |

**时间相关功能专项标注**：存在倒计时（countdown）、正计时（统计工作时长累加）、定时提醒（fixed 时刻 + 外卖）、周期性任务（daily/weekly）、时长统计。**时间基准实现（读代码确认）**：
- 调度器：`nowMs() = time.Now().UnixMilli()`（**墙钟绝对时间戳**，非 tick 累加，非单调时钟），tick 500ms（`scheduler.go:86/141`）
- 判定：全部基于 `now >= nextAt` 绝对时间戳比较（`scheduler.go:243`），**不会漂移**
- 统计：`time.Now()`（墙钟）+ `GetTickCount64`（单调，不含睡眠）双时钟（`stats.go:1246-1258`、`sys_power.go:28`）

### 1.2 配置项矩阵

| 键名 | 类型 | 默认值 | 合法范围/枚举 | 持久化 | 重启生效 | 失败行为 |
|---|---|---|---|---|---|---|
| `appName` | string | “吃饼人也敢休息？” | 任意（空=不显示名），折叠空白+限长（读取侧） | ✓ | 即时（窗口标题） | 折叠 |
| `short.enabled` | bool | true | bool | ✓ | 即时 | 非 true 视为 false |
| `short.mode` | enum | "countdown" | `countdown`/`fixed`（其余→countdown） | ✓ | 即时 | 归一化 |
| `short.intervalMs` | int | 600000 | 任意（读取侧 mapInt 兜底，**不 clamp**） | ✓ | 即时 | 类型不符→默认 |
| `short.durationMs` | int | 20000 | 任意（同上） | ✓ | 即时 | 同上 |
| `short.notifyBeforeMs` | int | 10000 | 任意 | ✓ | 即时 | 同上 |
| `short.postponeMs` | int | 120000 | 任意 | ✓ | 即时 | 同上 |
| `short.slots[]` | array | [] | hour 0-23 / minute 0-59（`slotInt` clamp） | ✓ | 即时 | clamp |
| `long.*` | 同 short | intervalMs=1200000 | 同上 | ✓ | 即时 | 同上 |
| `customBreaks[]` | array | [] | 对象含 id/name/config/messages | ✓ | 即时 | 丢弃非法项 |
| `longEveryNShort` | int | 2 | 任意（读取侧边界） | ✓ | 即时 | 越界→2 |
| `idlePauseMin` | int | 5 | ≥1（读取侧 clamp 到 [1, 2^30]） | ✓ | 即时 | clamp |
| `dndPause` | bool | true | bool | ✓ | 即时 | 非 true 视为 false |
| `fullscreenPause` | bool | true | bool | ✓ | 即时 | 同上 |
| `appPause.enabled` | bool | true | bool | ✓ | 即时 | 同上 |
| `appPause.items[]` | array | [] | 对象含 exe/name/enabled | ✓ | 即时 | 非对象/空 exe 丢弃 |
| `theme` | enum | "light" | 任意（前端消费） | ✓ | 即时 | — |
| `appearance.color` | string | "#4f7cff" | 任意 | ✓ | 即时 | — |
| `appearance.transparency` | float | 0.05 | 读取侧 clamp [0, 0.5] | ✓ | 即时 | clamp |
| `appearance.blur` | bool | true | bool | ✓ | 即时 | — |
| `appearance.fullscreen` | bool | false | bool | ✓ | 即时 | — |
| `appearance.alwaysOnTop` | bool | true | bool | ✓ | 即时 | — |
| `appearance.widthPct/heightPct` | float | 95 | 任意（读取侧不 clamp，前端消费） | ✓ | 即时 | — |
| `appearance.hideOnShare` | bool | true | bool | ✓ | 即时 | — |
| `allScreens` | bool | true | bool | ✓ | 即时 | — |
| `window.width/height` | int | 874/857 | 任意（Go 独占写 last*） | ✓ | 即时 | — |
| `window.rememberSize` | bool | true | bool（前端唯一可改） | ✓ | 即时 | — |
| `window.lastWidth/lastHeight` | int | 0 | **Go 独占写**（前端无权） | ✓ | 即时 | `keepWindowLastSize` 保护 |
| `sound.enabled` | bool | true | bool | ✓ | 即时 | — |
| `sound.volume` | float | 0.6 | 任意（读取侧） | ✓ | 即时 | — |
| `sound.sound/custom/mode/singleId` | mixed | — | 见 `normalizeSound` | ✓ | 即时 | 归一化 |
| `sound.music[]` | array | [] | 导入副本 | ✓ | 即时 | — |
| `shortcuts.*` | string | 4 个默认键 | 任意（热键注册失败兜底） | ✓ | 即时（重注册） | 注销旧键+重注册 |
| `reminderMessages.*` | object | 空 items | order: fixed/random/sequence；items 含 id/text/enabled | ✓ | 即时 | 丢无文本项，id 收敛序号 |
| `supervisor.enabled/gaze` | bool | true | bool | ✓ | 即时 | — |
| `supervisor.selection` | enum | "random" | fixed/sequence/random（其余→random） | ✓ | 即时 | 归一化 |
| `supervisor.fixedRole` | string | “秦始皇” | 专员名 | ✓ | 即时 | — |
| `supervisor.order[]` | array | [] | 专员名列表 | ✓ | 即时 | `applyRoleOrder` |
| `takeout.enabled` | bool | true | bool | ✓ | 即时 | — |
| `takeout.leadSec` | int | 10 | clamp [0, 300] | ✓ | 即时 | clamp |
| `takeout.postponeMin` | int | 2 | clamp [0, 120] | ✓ | 即时 | clamp |
| `takeout.pauseAction` | enum | "postpone" | remind/postpone/skip（其余→postpone） | ✓ | 即时 | 归一化 |
| `takeout.slots[]` | array | 1 个午餐 11:30 | 同 short.slots | ✓ | 即时 | clamp |
| `autostart` | bool | false | bool | ✓ | 即时（写注册表） | — |
| `startMinimized` | bool | false | bool | ✓ | 重启 | — |
| `installId` | string | 随机生成 | 首次生成后永不改变 | ✓ | 重启 | 缺失→补生成 |
| `checkUpdateOnStart` | bool | true | bool | ✓ | 重启 | — |
| `sites[]` | array | 1 个 https 源 | **只保留 https**（http 明文拒，回环例外） | ✓ | 即时 | 非法丢弃 |

### 1.3 状态与界面清单

| 界面/状态 | 触发条件 | 来源 |
|---|---|---|
| 设置窗口（主窗口，无边框，874×857） | 启动默认 | `main.go:185-208` |
| 通用配置页 | `data-tab=general` | `settings.html:64` |
| 时间规划页（含今日时间线折叠列表） | `data-tab=timing` | `settings.html:183` |
| 督休专员页（磁贴库+顺序/随机/固定） | `data-tab=supervisor` | `settings.html:329` |
| 统计报表页（4 组统计表） | `data-tab=stats` | `settings.html:429` |
| 快捷键页 | `data-tab=shortcuts` | `settings.html:416` |
| 关于页（版本/开源鸣谢/更新） | `data-tab=about` | `settings.html:503` |
| 休息窗口（每屏一个） | 休息到点/立即休息/预览 | `app.go:807`；`break.html` |
| 提前提示窗（右下角） | phase=pre（进入提前区间） | `prebreak.html`；`scheduler.go:840` |
| 外卖窗口 | 外卖到点 | `takeout.html` |
| 诊断页（手点提醒+环境信息） | 路由 `/index.html`（正常路径不可达） | `main.go:184` 注释 |
| 10 个对话框（弹窗） | 设置页各操作 | `a11y_audit.js` 输出 `dialogs:10` |
| 暂停态 | 手动/空闲/勿扰/全屏/指定应用 | `scheduler.go:690` |
| 空态（全部提醒关闭） | `nextAt=0` | `scheduler.go:1075` |
| 错误态（配置损坏） | 非法 JSON/越界值 | `settings.go:1718-1726` |
| 统计文件损坏弹窗 | stats.json 非法 | `stats.go:391-394` |
| 托盘图标+菜单 | GUI 模式启动 | `tray.go`；`app.go:74-83` |

### 1.4 不适用声明

| 提示词提到 | 本项目情况 | 判断依据 |
|---|---|---|
| 多语言/本地化 | **不适用** | 全仓仅 `lang="zh-CN"`，无 i18n 目录/`i18n`/`locale` 关键词 |
| 安装器（NSIS/MSIX） | **不适用** | 无 `Taskfile.yml`/`wails.json`/`*.nsi`/`makensis`，分发为便携 zip（`make_portable.py`） |
| 文件关联/URI 协议双击 | **不适用** | 无注册表关联写入（仅 HKCU\Run 自启动一项），`sys_autostart.go` |
| 账号/登录/注册 | **不适用** | 无账号体系，`installId` 是本地随机串非身份凭证 |
| 夏令时切换 | **不适用（中国无夏令时）** | 但跨天逻辑用 `AddDate` 而非固定毫秒，已为无夏令时地区之外的时区留有余量（`breakplan.go:724-726`） |
| 遥测/崩溃上报 | **不适用** | 无 SDK/上报端点 |
| 网络数据同步/云存储 | **不适用** | 数据全本机，仅更新检查联网（GET 只取不送） |

---

## 2. 测试方案

### 配置组合策略
- **配置项总数**：约 60+ 个用户可改键（见 1.2 矩阵）。
- **全组合**：爆炸（2^N × 枚举），不可行。
- **采用策略**：**成对组合（pairwise）+ 代码交互定向三元组**。由于本项目配置项之间存在大量“读取侧 clamp/归一化”的独立解耦（每项配置由 `Settings` 类型化视图独立读出，`breakConfigOf` 等函数不跨项耦合），真正有交互的配置对集中在：
  - `short.mode` × `short.slots`（fixed 模式才读 slots）
  - `short.enabled` × `long.enabled` × `longEveryNShort`（长短交替）
  - `supervisor.selection` × `supervisor.fixedRole` × `supervisor.order`（专员选择策略）
  - `takeout.enabled` × `takeout.slots` × `takeout.pauseAction`
  - `appearance.transparency` × `appearance.blur`（外观渲染）
- **执行覆盖**：通过无头端到端测试覆盖了“正常值 → 修改 → 生效 → 持久化 → 恢复”主链路；通过损坏注入覆盖“非法值 → 降级”。**未逐对执行 pairwise 全矩阵**（原因：无头 server 模式下大部分 UI 渲染配置项无可见副作用，纯数值 clamp 已在单元测试层覆盖但本机无法跑 go test），见 §5 未覆盖声明。

### 用例总数 / 分布
| 维度 | 数量 |
|---|---|
| 无头端到端（smoke_server.py） | 181 断言 |
| 补充功能测试（qa_func_supplement.py，本报告） | 29 断言 |
| 输入边界/注入（内联脚本，本报告） | 10 断言 |
| 静态可访问性（a11y_audit.js） | 4 页面 × 语义项 |
| **自动化等级 A 合计** | **约 220 项** |
| 需人工执行（等级 C） | 14 项（见 §4） |

### 判定标准摘要
- **文字溢出**：`scrollWidth > clientWidth + 1`（前端注入检测，需 GUI，标手动）
- **卡死**：`SendMessageTimeout(WM_NULL)` 5s 超时（需 GUI，标手动）
- **无法关闭**：点击关闭后 10s 进程仍在（需 GUI，标手动）
- **倒计时错误**：显示值与期望值偏差 >1s（无头已验：`nextAt` 绝对锚点恒定，无漂移）
- **配置损坏**：进程崩溃与否、是否回退默认（无头已验，全部降级不崩溃）

---

## 3. 用例执行结果

### 3.1 失败用例

**无。** 本报告执行范围内（无头端到端 + 补充 + 注入 + 静态）未发现失败项。

> 说明：`go test` 单元测试（约 30 个 `*_test.go` 文件、数千断言）因本机 Go 工具链缺失**未能执行**，这些不属于“通过”，而属于“阻塞”，见 §3.3。历史审计（`QA-AUDIT-2026-10-31.md`）记录的单测结论为 `go test -count=1 -tags server ./...` → `ok workerrest 13.431s`，但那是早前有 Go 工具链时的结果，本报告不将其计入本次“通过”。

### 3.2 通过用例（汇总）

| 域 | 用例组 | 断言数 | 关键证据 |
|---|---|---|---|
| 域1 核心 | 绑定契约全量解析（55 个方法按名可调） | 14 | `smoke_server.py` §2“按名解析” |
| 域1 核心 | 休息会话构建/字段完整性/预览互斥 | 20+ | smoke §3“数据对齐” |
| 域1 核心 | 动作记账（推迟/跳过/提前结束） | 15+ | smoke §4-8 |
| 域1 核心 | 恢复默认设置（备份+回退+installId 保留） | 5 | qa_func §reset |
| 域1 核心 | 输入边界（空名/路径遍历/非法 scheme） | 10 | 内联注入脚本 |
| 域2 配置 | 持久化往返（改→退出→重启→保留） | 3 | qa_func §persist |
| 域2 配置 | 越界值 clamp（hour/minute） | 2 | qa_func §corrupt |
| 域3 时间 | 时间基准（nextAt=now+interval，绝对锚点无漂移） | 13 | qa_func §time_baseline |
| 域7 错误注入 | 非法 JSON/空配置/越界值降级不崩溃 | 6 | qa_func §corrupt |
| 域7 错误注入 | 统计文件损坏备份重建（代码审查） | — | `stats.go:391-394` |
| 域4 文案 | 静态可访问性/无名控件/活区 | 4 页面 | `a11y_audit.js` |
| 域1/3 | 外卖/统计/声音/更新全链路 | 100+ | smoke §9-10 |

**证据索引**：
- 无头冒烟报告：`%TEMP%/wr_go_smoke_report.txt`
- 补充测试脚本：`scripts/qa_func_supplement.py`（本报告新增）
- 可访问性审计：`build/a11y_audit.json`

### 3.3 阻塞与不适用用例

| 用例 | 原因 |
|---|---|
| `go test -count=1 -tags server ./...`（约 30 个测试文件的单元测试） | **阻塞**：本机 Go 工具链缺失。需在带 Go 1.27.3 的环境执行。 |
| `go test -race`（竞态检测） | **阻塞**：需 Go 工具链 + CGO（`CGO_ENABLED=1`），本机均无。 |
| Fuzz target 运行 | **阻塞**：无 fuzz target（历史已显式接受覆盖缺口，见 `REVIEW-ROUND2` §3 D.2）。 |
| GUI 渲染/窗口生命周期全部 | **不适用（本机）→ 需人工**：无真实 GUI 环境。见 §4。 |

---

## 4. 需人工执行的用例清单

> 以下 14 项均需真实 GUI 环境（显示器 + WebView2 + 带控制台的 GUI 产物）。每一项给出精确步骤与通过标准。

- **[手动] GUI-01 | 关闭路径全覆盖**
  - 环境：真实 Win10/11，GUI 版 `rest.exe`（或 `workerrest.exe`）
  - 步骤：1) 双击启动，确认设置窗口出现；2) 点击标题栏“×”关闭；3) 任务管理器确认进程 10 秒内退出；4) 重新启动，按 Alt+F4；5) 重新启动，托盘图标右键→退出；6) 每次记录窗口消失时刻与进程退出时刻。
  - 通过标准：5 条路径每条进程均在 10 秒内退出，无残留子进程（`tasklist | findstr workerrest` 为空）。
  - 失败采集：进程列表、应用日志（`<exe>/logs/app.log`）、窗口标题。

- **[手动] GUI-02 | 单实例与多开**
  - 步骤：1) 双击启动第一个实例；2) 连续双击 exe 两次；3) 观察第二/三个进程是否立即退出、首实例窗口是否前置。
  - 通过标准：`tasklist` 只保留一个 `workerrest` 进程，首实例窗口获得焦点。

- **[手动] GUI-03 | 倒计时最小化/遮挡/托盘后计时正确性**
  - 步骤：1) 设短休息 interval=1 分钟；2) 倒计时运行中最小化设置窗口；3) 等待 2 分钟；4) 恢复窗口，检查休息是否到点触发、剩余时间是否正确。
  - 通过标准：最小化/失焦/托盘隐藏均不影响触发（WebView2 后台节流不影响 Go 侧调度器，因为计时在 Go 侧）。
  - 依据：计时在 Go 侧 `scheduler.go` 的 `nowMs()`，非前端 `setInterval`，理论不受前端节流影响，需真机复核。

- **[手动] GUI-04 | 系统时间调整对倒计时的影响**
  - 步骤：1) 设 10 分钟倒计时；2) 运行 2 分钟；3) 系统时间向前调 +5 分钟；4) 观察倒计时显示与触发。
  - 通过标准：基于 `time.Now()` 的实现会跳变（剩余时间骤减），但 60 秒 `tickJumpThresholdMs` 阈值会触发重排（`scheduler.go:174-178`），不应在“醒来第一刻”误弹过期提醒。记录实际行为是否与产品预期一致。
  - 失败采集：日志中 `两次 tick 间隔 ... 重排` 行。

- **[手动] GUI-05 | 睡眠/休眠唤醒后计时**
  - 步骤：1) 设 3 分钟倒计时；2) 睡眠 2 分钟；3) 唤醒；4) 记录显示值与触发时刻。
  - 通过标准：醒来后调度器通过 60s 阈值重排（不补弹）；统计通过 `detectSleep`（墙钟−唤醒tick 差值）把睡眠段记为“离开”，不记为“工作”。
  - 依据：`scheduler.go:174`、`stats.go:1247-1257`、`sys_power.go:37-43`。

- **[手动] GUI-06 | 跨天倒计时与统计翻页**
  - 步骤：1) 设固定时刻 23:59；2) 通宵挂机跨零点；3) 观察统计报表是否翻到新一天。
  - 通过标准：日范围右端自动推进、新一天数据出现在表格、用户手动选过的范围不被重置。
  - 依据：`probe_crossday.py`（需 Playwright，本机未跑）。

- **[手动] GUI-07 | 最小窗口尺寸布局检查**
  - 步骤：1) 拖窗口到最小尺寸（760 宽，见 `window_size.go`）；2) 逐 6 页检查关键按钮可见可点、文本无截断、无意外水平滚动条。
  - 通过标准：无控件重叠/截断，关键按钮可点击。
  - 依据：`probe_window_size.py`（需 GUI）。

- **[手动] GUI-08 | DPI 缩放（100%/150%/200%）**
  - 步骤：1) 分别设置 100%/150%/200% 缩放；2) 逐界面截图对比。
  - 通过标准：无截断/重叠/模糊/错位。Wails 已程序化设 PER_MONITOR_V2（`main.go` 注释）。
  - 失败采集：各 DPI 下截图。

- **[手动] GUI-09 | 多显示器不同 DPI 拖拽**
  - 环境：两台不同 DPI 显示器。
  - 通过标准：拖拽窗口跨屏无模糊/错位/裁剪。

- **[手动] GUI-10 | 长操作卡死检测**
  - 步骤：1) 触发文件导入（大批量音乐/立绘）；2) 用 `SendMessageTimeout(WM_NULL)` 或 UI Automation 检测窗口响应。
  - 通过标准：连续 5 秒无响应记为卡死；连续点击同一按钮 20 次不进入不可恢复忙碌态。

- **[手动] GUI-11 | 稳定性长时运行（2 小时句柄采样）**
  - 步骤：1) 空闲运行 2 小时；2) 每 15 分钟用 `Get-Process workerrest | Select Handles,Threads,WorkingSet` 采样 GDI/USER 句柄、线程、内存。
  - 通过标准：任一指标不单调递增（首末差值在噪声内）。
  - 依据：历史审计 §6 第 7 项（未执行）。

- **[手动] GUI-12 | 共享屏幕隐藏提醒窗口**
  - 步骤：1) 开屏幕共享（如腾讯会议）；2) 触发一次休息提醒；3) 用截图工具验证窗口是否隐藏。
  - 通过标准：`--disable-direct-composition` + `SetWindowDisplayAffinity` 生效，捕获结果 0% 可见。
  - 依据：`main.go:135-141`、`probe_capture_guard.py`。

- **[手动] GUI-13 | 干净 Win10（无 WebView2）首次启动**
  - 步骤：1) 未装 WebView2 的干净 VM 双击；2) 观察是否出现安装引导提示。
  - 通过标准：出现可操作提示 + 打开下载页（`main.go:99-112`）。

- **[手动] GUI-14 | 从 v0.9.5 升级到 v0.9.6**
  - 步骤：1) 装 v0.9.5；2) 触发自动更新到 v0.9.6。
  - 通过标准：config/stats/supervisors 保留、版本号更新、首启正常。

---

## 5. 配置组合覆盖报告

| 项 | 数量 |
|---|---|
| 配置项总数（用户可改） | 60+ |
| 本机已自动化覆盖的组合 | 主链路（正常→修改→生效→持久化→恢复）+ 非法值降级 |
| **未执行**的 pairwise 全矩阵 | 全部（原因见下） |
| 失败组合 | 无 |

**未执行原因（如实声明）**：
1. 本机 **Go 工具链缺失**，无法跑 `go test`（配置 clamp/归一化的表驱动单元测试在 `settings_test.go` 等文件中，但无法执行）。
2. 无头 server 模式下，大量 UI 渲染类配置项（`theme`/`appearance.color`/`appearance.blur` 等）无可见副作用，pairwise 组合的“执行受影响功能”这一步需要 GUI 才能验证。
3. 因此，配置组合的**成对覆盖未能本机完成**，仅覆盖了“单一配置项变更的主链路”与“非法值降级”两条正交维度。这是本报告最大的覆盖缺口，需在带 Go 工具链 + GUI 的环境补做。

---

## 6. 时间相关功能专项结论

### 时间基准实现方式
- **调度器（休息倒计时）**：`nowMs() = time.Now().UnixMilli()`（`scheduler.go:86`）——**墙钟绝对时间戳**，tick 用 `time.NewTicker(500ms)`（`scheduler.go:141`），但所有“到点”判定基于 `now >= nextAt` 绝对时间戳比较（`scheduler.go:243`），**不是 tick 累加**。结论：**无漂移**。
- **统计（工作时长累加）**：`time.Now()` 墙钟 + `GetTickCount64`（单调，不含睡眠）双时钟，`detectSleep(wallDelta - tickDelta)` 检测睡眠（`sys_power.go:37-43`）。
- **外卖提醒**：每秒 `time.NewTicker(time.Second)`，按分钟匹配固定时刻（`takeout.go:468`）。
- **前端**：无 `setInterval` 计时（倒计时显示由 Go 侧 `remainingMs` 推送，非前端自计）。

### 漂移实测数据（本机可自动化部分）
| 档位 | 期望 nextAt−now | 实测 delta | 结论 |
|---|---|---|---|
| 短休息 600s | 600000ms | 599873ms（偏差 127ms，属启动采样误差） | ✅ 无漂移 |
| 长休息 2h（7200000ms） | 7200000ms | 7199388ms（偏差 612ms） | ✅ 无漂移 |
| 改 interval 后重锚 30s | 30000ms | 29374ms（偏差 626ms，重锚后采样） | ✅ 正确重锚 |
| 连续采样 nextAt 恒定性 | 不变 | `1793453198740` == `1793453198740`（3 次采样全等） | ✅ 绝对锚点，不随采样漂移 |

> 关键证据：`nextAt` 是**绝对时间戳**（固定值），采样次数再多也不变，这是“无 tick 累加漂移”的直接证明。2 小时档终点偏差 612ms 远小于 1s 阈值（且为启动采样误差，非累积漂移）。

### 窗口状态/系统时间/睡眠各场景结论
| 场景 | 结论 | 依据 |
|---|---|---|
| 最小化/失焦/遮挡/托盘 | **理论不受影响**（计时在 Go 侧非前端） | `scheduler.go`；需真机复核（GUI-03） |
| 系统时间向前/向后调 | 基于墙钟会跳变；60s 阈值触发重排，不补弹过期提醒 | `scheduler.go:163-178`；需真机（GUI-04） |
| 睡眠/休眠唤醒 | 调度器 60s 阈值重排；统计 `detectSleep` 记“离开”不记“工作” | `sys_power.go`、`stats.go`；需真机（GUI-05） |
| 跨天 | 用 `AddDate` 推进（非固定毫秒），本地零点切分 | `breakplan.go:724-726`、`stats.go:247`；需真机（GUI-06） |

### 到点行为结论
- 归零显示：`max0()` 保证不为负（`scheduler.go:88`）
- 到点触发：`now >= nextAt` + 自然到点后多停留 1s（`breakCloseDwellMs`，`scheduler.go:158`）
- 停止/重置/重新开始状态机无残留：`consumed` map 去重 + `reanchorCountdown` 重锚（`scheduler.go:47-51`、`922-986`）

---

## 7. 缺陷汇总与放行建议

### BLOCKER 列表
**无。** 本次功能测试执行范围内（无头端到端 + 补充 + 注入 + 静态审计）未发现 BLOCKER 级功能缺陷。

### WARNING 列表
**无新发现。** 历史遗留 WARNING（未签名、无 fuzz、多显示器 DPI 未实测、`-race` 未跑）已在 `REVIEW-ROUND2-2026-10-31.md` 记录，本轮未消除、未新增。

### NOTE（观察项）
1. **覆盖缺口**：配置组合 pairwise 全矩阵未本机执行（Go 工具链缺失 + 无 GUI），见 §5。
2. **单元测试未执行**：约 30 个 `*_test.go` 文件的表驱动单元测试（配置 clamp、调度器状态机、统计、声音、更新信任链等）因 Go 工具链缺失未本机运行，属**阻塞**而非“通过”。
3. **测试脚本留档**：本报告新增 `scripts/qa_func_supplement.py`（29 项补充断言），可随 Go 工具链恢复后一起纳入 CI。

### 放行建议
**有条件放行。**

在“代码与发布配置层无 BLOCKER”（见 `REVIEW-ROUND2` §8 结论）的基础上，本次功能测试补充验证了：
- 核心业务功能链路（休息/外卖/统计/声音/更新）181 项端到端断言全通过；
- 时间基准无漂移（绝对锚点实现，代码 + 实测双重确认）；
- 配置损坏/非法值/路径遍历注入全部安全降级、不崩溃；
- 配置持久化与恢复默认行为正确。

**放行附加条件**（本次新识别）：
1. 在带 Go 1.27.3 工具链的环境补跑 `go test -count=1 -tags server ./...`，确认约 30 个单元测试文件全绿（当前本机阻塞）。
2. 补跑 §4 的 GUI-01/02/03（关闭路径、单实例、倒计时最小化）三条最高优先级的真机验证——这三条涉及“进程残留”“双开”“后台节流”等无法用无头方式验证的运行时行为。
3. §5 的配置组合 pairwise 覆盖，建议在 GUI 环境补做（尤其 `short.mode`×`slots`、`supervisor.selection`×`fixedRole`、`takeout.pauseAction` 三组交互对）。

---

*可复现证据：`scripts/smoke_server.py`（181 项）、`scripts/qa_func_supplement.py`（29 项，本报告新增）、内联注入脚本（10 项）、`node scripts/a11y_audit.js`、`build/a11y_audit.json`。*
