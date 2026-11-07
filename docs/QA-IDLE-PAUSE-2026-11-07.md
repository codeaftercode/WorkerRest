# 「系统空闲」开关专项测试报告 — 2026-11-07

> 对象：吃饼人也敢休息？v0.9.9 工作树（新增 `idlePause` 开关后的未提交改动）
> 范围：用户点名的四类——**复选框勾选状态、设置的分钟数、统计数据、其他相关项**
> 结论：**本轮 715 项探针断言 + 21 包 Go 单测全部通过，未发现产品缺陷**。修正了 2 类探针自身的假阳性（都不是产品问题）；另有 2 项待产品口径决定、1 项发布前待办，见第 6 节。
> 口径：所有数字均为本轮实跑；跑不起来的写「无法测量」，不拿历史结果顶替。真机 ON 用例依赖本机静置 60 秒，前两轮因有人操作测不到，已在探针里改成如实记「无法测量」而不是硬跑后续断言。

---

## 1. 用户改动已接住

用户在本轮开始前自行改了设置页文字，探针逐条钉死，均无回归：

| 改动 | 断言 |
|---|---|
| 「运行应用」→「运行应用：」（作为下方列表的引子） | `appText.endsWith("：")` |
| 「?」说明改成「无鼠标动作、无键盘输入持续时间超过设置值时自动暂停」 | 正则匹配判据，且挂在句尾 `.tail` 内、名称后无多余「?」 |
| 卡片标题「自动暂停」加「?」 | 页面扫描与静态审计覆盖 |

---

## 2. 方法：四条腿互相补位

| 层 | 验什么 | 为什么不能省 |
|---|---|---|
| Go 单测 | 阈值跟分钟数变、开关两态判定、离开尾账结算、配置解析 | 可注入假时钟与 `ForceIdle`，把真机测不到的边界钉死 |
| 无头探针（`-tags server`） | 勾选态 ↔ 配置双向、分钟数夹紧与回填、重载、排版、a11y | 起得快、可重复；但**故意不启动**会改调度器的监视器 |
| 真 GUI 探针 | 真实键鼠空闲 → 暂停 → 统计落盘，以及关掉开关后的对照 | 「系统是否真判了空闲」只有真机 `GetLastInputInfo` 能证 |
| 静态清点 | 开关读取点、关联链路、文档口径一致性 | 确认没有漏改的第二处判据 |

隔离：`WR_SETTINGS_PATH` / `WR_STATS_PATH` 指向 `%TEMP%`，每轮收尾断言真实 `config/settings.json` 未被改写（SHA-256 `d3aa704e…` 前后一致）。

---

## 3. 验证矩阵（全部本轮实跑）

### 3.1 静态与 Go 单测

| 项 | 结果 |
|---|---|
| `gofmt -l cmd internal` | 干净 |
| `go vet -tags server ./...` | 通过 |
| `go test -count=1 -tags server ./...` | **21 包全绿**（19 有测试 + 2 无测试文件） |
| `git diff --check` | rc=0；本轮改动全为 `i/lf w/lf`，新增探针脚本纯 LF 无 BOM |

本轮新增单测（4 条）：

| 用例 | 钉住什么 |
|---|---|
| `TestIdleWatchThresholdFollowsMinutes` | 同一 App 上把阈值 5→2→10 分钟，4 分钟空闲依次「不判 / 判 / 不判」——**改分钟数立刻改变判定**，不只是存进配置 |
| `TestIdleWatchSwitchOffIgnoresAnyMinutes` | 关掉开关后即便阈值 1 分钟、空闲远超阈值也整条不判；重新勾上立刻按当前阈值恢复 |
| `TestEndAwaySettlesTailAndRestartsWorkSegment` | 关开关走 `EndAway` 时把 20 分钟尾账一次性补齐（5→25 分钟）、工作段重新起算、再来一段不重算旧账 |
| `TestIdlePauseMinParsing` / `TestIdlePauseSwitch*` | 缺键/非布尔/NaN/字符串/小数/上下限共 12 组解析口径（前一轮已补） |

### 3.2 复选框勾选状态

| 项 | 结果 |
|---|---|
| 出厂默认、`flag(true)` 缺键兜底、显式 false、非布尔回落 | 单测全过 |
| 勾选态 == 配置、取消写 `false`、重载后仍是未勾选（不回默认开） | UI 探针全过 |
| 取消勾选 → 分钟框立即禁用；重新勾选 → 恢复可编辑 | UI 探针全过 |
| 两个键独立：取消勾选不清分钟数（5 → 5） | UI 探针全过 |
| 收尾还原为「开 + 5 分钟」 | UI 探针全过 |
| 关再开立刻恢复检测、不残留「上一拍还是离开」 | `TestIdleWatchSwitchOffThenOnAgain` |
| 关闭瞬间正停在离开态：同一拍恢复、只摘 idle 不动别的自动原因 | `TestIdleWatchSwitchOffResumesInFlightPause` / `...KeepsOtherAutoReasons` |
| 用户已手动恢复过再关开关：只收尾记账、不重锚倒计时 | `TestIdleWatchSwitchOffAfterManualResumeClosesAwayOnly`（变异验证过） |

### 3.3 设置的分钟数

| 项 | 结果 |
|---|---|
| 前端填 0→1、121→120、45 原样、清空→5；重载后显示夹紧值 | UI 探针全过 |
| 「暂停休息时」提示语随分钟数同步（45 / 回落 5） | UI 探针全过（本轮新增 2 条） |
| 后端解析：缺键/类型错/NaN→5、0 与负数→1、小数四舍五入、120 原样 | `TestIdlePauseMinParsing` |
| **真机**：阈值设 1 分钟，真实空闲 60 秒触发暂停 | 端到端 ON 用例，日志 `已连续无输入 60s ≥ 60s` |
| **阈值改动即时生效** | `TestIdleWatchThresholdFollowsMinutes` |

### 3.4 统计数据（真 GUI + 真落盘）

最终复跑（`probe_idle_switch.py`，16:11–16:14）：

| 用例 | 关键数据 | 结论 |
|---|---|---|
| **ON**（勾选，1 分钟） | 16:12:55 判离开，`59007 ms 从工作搬进离开`；`awayMs 59007 → 59007`、`workMs 0 → 0` | 首段从工作搬入离开；离开期间不算工作、不重复记账 |
| **OFF**（取消，同为 1 分钟） | `awayMs=0`；`workMs 61000 → 72000` 持续增长 | 完全不做空闲判定，这段时间照常算工作 |

15:47 那一轮额外真机命中了**恢复链路**（16:05 之后的复跑未再复现，故单列）：日志成对出现 `恢复（idle）` + `检测到键鼠活动：恢复提醒，内置倒计时重新开始`，统计 `awayMs 60032 → 70029`（尾账补齐）、`workMs 69970 → 77970`（工作段重新起算）。这与新增的 `TestEndAwaySettlesTailAndRestartsWorkSegment` 口径一致。

### 3.5 前端与可访问性矩阵

| 探针 | 断言数 | 失败 |
|---|---|---|
| `probe_idle_ui.py`（本支专项 UI） | **39** | 0 |
| `probe_settings_ui.py` 四组（123 / 57 / 61 / 88） | **329** | 0 |
| `probe_static_audit.py` | **52** | 0 |
| `probe_pages_sweep.py` | **80** | 0 |
| `smoke_server.py` | **192** | 0 |
| `probe_idle_switch.py`（端到端双用例） | **23** | 0 |
| `a11y_audit.js` | 无名控件 **0** | 0 |
| `probe_a11y_focus.js` | 失败 **0** | 0 |

合计 **715 项探针断言，0 失败**。UI 探针另覆盖：文字列同列（x=317）、四条系统状态选项仍并排一行、874 与最小宽 760 均不折行无溢出、行高 40px、暗色一致、勾选框与数字框都有可读名、全程无 JS 报错。

---

## 4. 修正的 2 类探针假阳性（都不是产品缺陷）

1. **「离开时长每秒持续增长」**：生产链路里 `OnAwayTick()` **没有周期调用点**，`AwayMs` 只在 `BeginAway` 搬入首段、在 `EndAway` 一次性结清尾账，离开期间落盘值本来就是冻结的。探针原断言写错了。已改为「搬入量 ≈ 阈值」「不倒退」「离开期间 `workMs` 不增长」，并补 `TestEndAwaySettlesTailAndRestartsWorkSegment` 从单测侧钉住尾账结算。
2. **「观察窗内始终停在离开态」**：真机跑探针时用户随时可能碰一下键鼠，一碰就走 `IdleStepBack → EndAway`，`awayMs` 补尾账、`workMs` 重新起算。探针按日志里有没有「恢复（idle）」分两种口径断言；另外「没测到阈值」时直接记一条「无法测量」并跳过依赖前提的统计断言，不再产出误导性 FAIL。

> 顺带修掉一个测试自锁：`accountWorkTick` 自身持锁，新单测外面曾多包一层 `st.mu.Lock()`，导致 SIGTERM 无输出；已改掉。

---

## 5. 关联链路静态清点（无需改动）

- 生产侧读 `IdlePause()` / `IdlePauseMin()` 只有 3 处：`idleWatchTick`（判定）、`statsIsIdle`（统计口径）、`settings` 访问器。两处判定同口径，关掉后既不暂停也不记「离开」。
- 定时手动暂停到期闸 `pauseHoldReasons`、暂停每小时提示、自动更新链、托盘暂停原因文案：都基于 `PauseReasons` / `IsActive()` 等既有判据，关掉开关后 idle 原因牌不会出现，无需跟着改。
- 文档：`README.md`、`scripts/build_manual.py`、`docs/manual/操作手册.html` 三处口径一致（生成器 110 条表格行与产物逐字对齐、缺 0 条），无「始终开启 / 不可关闭」残留；手册表格 `rowspan=7` 与 README「六种自动暂停」条数不变（加的是开关，不是新原因）。

---

## 6. 待定与待办

| # | 项 | 说明 | 需要谁 |
|---|---|---|---|
| 1 | **「暂停休息时」提示语口径** | 取消勾选「系统空闲」后，这条 `?` 仍写「系统空闲 5 分钟自动暂停」。原设计列的是**所有可能**导致暂停的情况（勿扰、应用占用那条也照列，不管开关勾没勾），所以严格说不算错；但新开关出现后读起来可能像在描述一个已关闭的功能。本轮只钉住现状、未擅自改文案 | 你定口径 |
| 2 | **手册设置页截图仍是旧图** | 图里没有新增的勾选框。要重拍得跑 `manual_shots.py` + `build_manual.py`（会重生成 4MB 内嵌图 HTML） | 发布前补 |
| 3 | **提交与发布** | 本轮 15 个改动文件（12 改 + 3 新增探针）尚未提交；`v0.9.9` tag 仍指向 `050ede7` | 你发话 |

---

## 7. 复跑命令

```bash
export PATH="/d/Programs/Go/bin:$PATH" GOPATH=C:/Users/lwb/go GOCACHE=C:/Users/lwb/AppData/Local/go-build
go test -count=1 -tags server ./...
python scripts/probe/probe_idle_ui.py
python scripts/probe/probe_settings_ui.py
python scripts/probe/probe_static_audit.py
python scripts/probe/probe_pages_sweep.py
python scripts/probe/probe_idle_switch.py --only on   # 需本机静置 60 秒
python scripts/probe/probe_idle_switch.py --only off
python scripts/smoke_server.py
node scripts/a11y_audit.js && node scripts/probe_a11y_focus.js
```
