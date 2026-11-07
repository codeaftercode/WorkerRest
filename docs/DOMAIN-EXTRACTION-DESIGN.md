# 领域下沉设计（internal 子包化）

> 目标：把根目录 59 个 `package main` 源文件里“零 App 依赖”的纯领域逻辑下沉到 `internal/` 子包，
> 让根目录只保留“App 绑定壳 + 平台/窗口相关”，真正减少根目录文件数。
>
> 上一轮“文件级瘦身”（路线 B）让根目录从 34 个源文件增至 59 个——文件更多了。
> 本文档是对该副作用的修正，走“领域下沉”（路线 A）的正确实现。

## 一、硬约束（不可违背）

1. **`bindingFQN = "main.App."`**（`contract.go`）：所有 `func (a *App)` 绑定方法必须留在 `package main`。
   前端 `Call.ByName("main.App.xxx")` 靠 `reflect.PkgPath()` 拼名字，main 包的 PkgPath 恒为 `"main"`。
2. **下沉对象只限“零 App 依赖”的纯数据/纯函数**。任何引用 `App` 类型、`a.mu`、或绑定方法的代码都不能下沉。
3. **依赖方向必须单向**：`internal/model`（最底层类型）← `internal/settings` / `internal/plan` ← `package main`。
   绝不允许出现 main → internal 又 internal → main 的循环。

## 二、依赖现状（已实测确认）

`App` 引用密度（grep `\*App|\ba\.mu\b|\bApp\b` 计数）把 59 个源文件分成两群：

- **零 App 依赖（19 个，可下沉候选）**：
  `types.go`、`settings.go`、`settings_time.go`、`settings_norm.go`、`settings_msg.go`、
  `breakplan.go`、`breakplan_slots.go`、`breakplan_next.go`、`breakplan_timeline.go`、
  `role.go`、`role_import.go`、`role_write.go`、`role_names.go`、`role_asset.go`、
  `recoverguard.go`、`mode_server.go`、`mode_gui.go`、`messages.go`、`update_trust.go`
- **有 App 依赖（40 个，必须留根目录）**：其余全部。

类型依赖链（关键）：

```
settings_time.go  →  TimeSlot                       （纯数据 + 归一化工具，最底层）
types.go          →  BreakInfo{Slot TimeSlot}, TakeoutInfo   （依赖 TimeSlot）
breakplan.go      →  PlanRef/PlannedBreak/SchedulerState/... （依赖 TimeSlot、Settings）
settings.go       →  Settings{Slots []TimeSlot}, BreakConfig, SupervisorConfig, ...
```

注意：`types.go` 的 `BreakInfo` 引用了 `TimeSlot`，`breakplan.go`/`settings.go` 都引用 `TimeSlot`。
所以 `TimeSlot` 必须放在**最底层**的 model 包，`types.go`、`breakplan.go`、`settings.go` 都 import 它。

## 三、子包划分方案

### 目标结构

```
internal/
├── model/          # 最底层共享类型：TimeSlot、BreakInfo、TakeoutInfo、PlanRef、SchedulerState...
├── settings/       # Settings 模型 + 归一化 + 访问器（不含 App）
└── plan/           # 计划层纯计算（breakplan 的纯函数部分）
```

### 关键判断：Settings 方法牵连的类型怎么处理

`Settings` 类型挂了一堆方法（`breakplan.go` 里的 `LongEveryNShort`、`BreakEntries`、`BreakByID`、
`AppPauseCfg` 等），这些方法的返回值类型（`BreakConfig`、`AppPauseConfig`、`CustomBreak`、`BreakEntry`）
也零 App 依赖。**这些类型必须和 Settings 一起下沉到 `internal/model` 或 `internal/settings`**，
否则 main 包 import settings 时，settings 又反向引用 main 的 `BreakConfig` → 循环依赖。

因此下沉粒度必须“以类型为最小单元，连带其方法”。

## 四、风险清单（务必正视）

1. **锁语义**：`stats.go` 曾出现 `a.mu` 自死锁坑。下沉“纯数据”不动锁，但下沉“统计存取”这类带状态的类型会牵涉锁。**本次下沉范围严格控制在不带锁的纯数据/纯函数。**
2. **本机无法 `-race`**：缺 gcc，无法验证并发回归。所以**任何涉及共享可变状态的下沉都不做**，只下沉不可变数据 + 纯函数。
3. **import 波及面**：`TimeSlot` 被 5+ 文件引用、`Settings` 被 10+ 文件引用。每下沉一个类型，main 包里所有引用点都要加 import，改动量大但机械（可用脚本辅助）。
4. **测试文件**：`_test.go` 大量直接引用这些类型。下沉后测试文件的 import 也要改，但测试逻辑本身不变。

## 五、执行策略（分阶段、每步可验证）

1. **阶段 1：下沉 `internal/model`（最底层类型）**
   - 搬 `settings_time.go` 的 `TimeSlot` + 归一化工具、`types.go` 的 `BreakInfo`/`TakeoutInfo`、`breakplan.go` 的纯数据类型（`PlanRef`/`PlannedBreak`/`SchedulerState`/`BreakEntry`/`AppPauseItem`/`AppPauseConfig`/`CustomBreak`/`TimelineItem`/`TakeoutPostponed`）。
   - main 包改成 `import "workerrest/internal/model"`，引用点加 `model.` 前缀。
   - 每搬一类跑 `go build ./...` + `go test`。
2. **阶段 2：下沉 `internal/settings`（Settings 模型）**
   - 搬 `settings.go`/`settings_norm.go`/`settings_msg.go` 的 `Settings` + 方法 + 归一化。
   - 这一步波及面最大，放最后做。
3. **阶段 3：下沉 `internal/plan`（计划层纯计算）**
   - 搬 `breakplan_slots.go`/`breakplan_next.go`/`breakplan_timeline.go` 里不依赖 App 的纯计算。
4. **阶段 4：下沉 role 领域（可选）**
   - `role.go`/`role_import.go`/`role_write.go`/`role_names.go`/`role_asset.go` 里零 App 依赖的部分。

## 六、验证门禁（每个阶段结束必过）

- `gofmt -l .` 干净
- `go build ./...` 通过
- `go vet ./...` 干净
- `go test -count=1 -tags server ./...` 全绿
- 跨平台编译：`GOOS=linux GOARCH=amd64` 与 `arm64` 下 `internal/*` 包可编译（main 包允许因 Wails CGO 失败）

## 七、待确认决策

- [ ] 子包根路径：`internal/model` 还是 `internal/shared`？（建议 `internal/model`）
- [ ] 下沉粒度：是否连 `Settings` 方法一并下沉（建议是，否则循环依赖）？
- [ ] role 领域（立绘/专员）是否纳入本轮下沉，还是暂缓？
