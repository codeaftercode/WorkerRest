# WorkerRest 跨平台架构重构设计

> 状态：设计草案，待评审。**未改任何代码。**
> 目标：为跨平台（Windows / Linux / macOS / 非 x86 的银河麒麟）发布与未来功能扩展，重新设计源码分层与平台隔离。
> 日期：2026-11-01
>
> **后续（2026-11-01 晚）**：本文第 3 节的目标结构是“main.go 平铺在根”的形态，实际落地时
> 改为标准 Go 布局 `cmd/workerrest/` + `internal/`（main 包下沉，`frontend/dist` 随包改为
> `cmd/workerrest/web/dist`，随包资源收进 `assets/`）。最终结构以 README 的“目录结构”为准，
> 本文保留为重构决策的历史记录。

---

## 1. 现状诊断

### 1.1 问题清单

| 问题 | 事实依据 | 影响 |
|------|---------|------|
| **单包巨型文件** | `settings.go` 1798 行、`stats.go` 1726 行、`app.go` 1473 行、`scheduler.go` 1343 行、`update.go` 1159 行、`breakplan.go` 1058 行、`takeout.go` 1016 行 —— 7 个文件超过 1000 行，占源码总量 70% | 单文件职责过重，难以维护与分工 |
| **核心结构体超载** | `App` 结构体 50+ 字段：窗口管理 + 调度器 + 托盘 + 声音 + 外卖 + 系统能力 + 统计 + 尺寸记忆全塞一个 struct，靠一把 `a.mu` 串起 | 平台无关逻辑与平台绑定逻辑深度交织 |
| **平台代码无隔离** | `hotkeys.go`/`sys_idle.go`/`sys_autostart.go`/`webview2_check.go`/`recoverguard.go` 直接 `syscall.NewLazyDLL`、`unsafe`、`golang.org/x/sys/windows`、`registry` | 无法跨平台编译，Windows API 泄漏进业务逻辑 |
| **已有 build tag 但极薄** | 仅 `mode_gui.go`/`mode_server.go` 用 `//go:build`，其余 Windows 代码全裸奔 | 平台差异没有被系统化处理 |
| **绑定契约锁定 main 包** | `contract.go:125` `bindingFQN = "main.App."`，Wails 用 `reflect.PkgPath()` 拼名，main 包 PkgPath 恒为 `"main"` | `App` 及其绑定方法**必须**留在 `package main` |

### 1.2 关键约束（决定方案走向的硬事实）

1. **Wails 绑定锁死 main 包**：所有 `func (a *App)` 方法（约 200+ 个）必须在 `package main`，否则前端 `Call.ByName("main.App.xxx")` 全断。这是 Wails v3 的机制，不是可选风格。
2. **Go 静态链接**：第三方库机器码编进 exe，无 DLL 可拆（此前已论证，DIFF-UPDATE-DESIGN.md）。
3. **`go:embed` 路径**：`frontend/dist`、`THIRD-PARTY-NOTICES.txt`、`default_settings.json` 位置被 embed 指令钉死（本次不改资源位置）。
4. **`syscall.NewLazyDLL`/`unsafe`/`registry`/`windows.*` 是 Windows-only**，任何 import 它们的文件都编译不过 Linux/macOS。

### 1.3 已有成果（上一轮 internal/ 拆分）

`internal/winproc`（进程枚举）、`internal/windnd`（勿扰/全屏）、`internal/winpower`（睡眠检测）已拆出，验证了“纯工具抽到 internal 子包”的路径可行。本轮是这条路径的系统化扩展。

---

## 2. 设计目标

1. **平台隔离**：所有平台专属能力（快捷键、托盘、自启动、空闲检测、进程枚举、文件对话框、WebView2 预检、弹窗）收敛到明确的平台抽象层，跨平台编译时不触碰 Windows API。
2. **解耦巨型文件**：把 1000+ 行的单文件按“领域 + 纯函数 vs 绑定方法”切分，让每块职责单一、可独立测试。
3. **不破坏绑定契约**：`App` 留在 main 包，200+ 绑定方法不动名字（契约测试 `contract_test.go` 钉住）。
4. **渐进可落地**：分阶段执行，每阶段结束都能 `go build` + `go test` 全绿，不出现“大爆炸式”重构。

---

## 3. 目标目录结构

```
workerrest/
├── main.go                      # 入口（保留，但平台分支抽走）
├── contract.go                  # 绑定契约表（保留）
├── app.go                       # App 结构体 + 生命周期（瘦身，拆出各领域方法）
├── settings.go                  # 设置模型（纯结构体 + 深合并，不依赖 App）
├── ...                          # 其余业务文件（见下）
├── internal/
│   ├── winproc/                 # ✅ 已有：进程枚举
│   ├── windnd/                  # ✅ 已有：勿扰/全屏
│   ├── winpower/                # ✅ 已有：睡眠检测
│   ├── platform/                # 🆕 平台抽象层（核心新增）
│   │   ├── platform.go          #   接口定义 + 注册表
│   │   ├── platform_windows.go  #   Windows 实现（build tag: windows）
│   │   ├── platform_linux.go    #   Linux 实现（build tag: linux）
│   │   └── platform_darwin.go   #   macOS 实现（build tag: darwin）
│   ├── sysinfo/                 # 🆕 系统能力探测（空闲/勿扰/进程/自启动 → 接口化）
│   ├── hotkey/                  # 🆕 全局快捷键（Windows 实现 + 其它平台 no-op）
│   ├── notifier/                # 🆕 系统弹窗/消息框（Windows MessageBoxW + 其它）
│   ├── autostart/               # 🆕 开机自启动（注册表 + autostart/.desktop + launchd）
│   └── dialog/                  # 🆕 文件对话框（封装 wails Dialog，平台中立）
├── domain/                      # 🆕 纯业务领域逻辑（零平台依赖、零 App 依赖）
│   ├── plan/                    #   计划层（breakplan.go 的纯计算部分）
│   ├── schedule/                #   调度状态机（scheduler.go 的纯状态转换）
│   ├── stats/                   #   统计模型（stats.go 的纯数据部分）
│   └── ...
└── ...
```

---

## 4. 核心设计：平台抽象层（platform）

这是跨平台的关键。原则：**main 包不再直接 import 任何 Windows-only 库，统一通过 `internal/platform` 的接口调用。**

### 4.1 接口定义（`internal/platform/platform.go`，全平台编译）

```go
package platform

// Capability 描述一个平台能力的探测/操作结果。
type Capability interface{}

// Provider 平台能力提供者，由各平台实现文件在 init/变量中注册。
type Provider interface {
    // SystemTray 返回是否支持系统托盘
    SystemTraySupported() bool
    // Hotkeys 返回是否支持全局快捷键
    HotkeysSupported() bool
    // Autostart 返回是否支持开机自启动
    AutostartSupported() bool
    // ...
}

// 各平台实现通过 build tag 提供同一个 `Get()` 入口
var current Provider
```

### 4.2 Windows 实现（`platform_windows.go`，`//go:build windows`）

```go
//go:build windows

package platform

import (
    "golang.org/x/sys/windows"
    "golang.org/x/sys/windows/registry"
    // ...
)

type windowsProvider struct{}

func init() { current = windowsProvider{} }

func (windowsProvider) SystemTraySupported() bool { return true }
// ... 注册表自启动、MessageBoxW、GetLastInputInfo 等全部收在这里
```

### 4.3 Linux / macOS 实现（`//go:build linux` / `darwin`）

```go
//go:build linux

package platform

type linuxProvider struct{}

func init() { current = linuxProvider{} }

func (linuxProvider) SystemTraySupported() bool { return true } // 或按发行版判断
func (linuxProvider) HotkeysSupported() bool    { return false } // 银河麒麟可能需 X11 支持
// ...
```

### 4.4 关键点

- **`unsafe` / `syscall` / `windows.*` / `registry` 只出现在 `_windows.go` 文件**，且这些文件带 `//go:build windows`。
- Linux/macOS 文件用纯标准库 + 各平台对应库（如 `github.com/godbus/dbus` for autostart，按需引入）。
- 银河麒麟（非 x86，如 ARM64/龙芯）走 `//go:build linux && (arm64 || loong64)` 或直接 `linux`（Go 的 build tag 能表达架构），确保 `unsafe` 指针操作按架构正确。

---

## 5. 平台专属功能清单与归属

| 现有文件 | Windows 专属点 | 跨平台处置 | 目标位置 |
|---------|--------------|-----------|---------|
| `hotkeys.go` | `RegisterHotKey`/`GetMessageW`/`GetCurrentThreadId` | 抽成接口 `internal/hotkey`，Windows 实现 + 其它平台 no-op | `internal/hotkey/` |
| `sys_idle.go` | `GetLastInputInfo`（user32） | 抽成接口，Linux 用 `xprintidle`/`XScreenSaver`，macOS 用 IOKit | `internal/sysinfo/idle` |
| `sys_autostart.go` | `registry` 读写 | 抽成接口，Linux 写 `~/.config/autostart/*.desktop`，macOS 用 launchd | `internal/autostart/` |
| `sys_dnd.go`（已拆） | `Shell_NotifyIcon`/QUERY_USER_NOTIFICATION_STATE | 已有 `internal/windnd`，保留 Windows 实现，其它平台返回“不支持” | `internal/windnd/` |
| `webview2_check.go` | `MessageBoxW`/`ShellExecuteW`/registry | WebView2 是 Windows-only；抽 `internal/notifier`（MessageBox），Linux/macOS 走 WebKitGTK/WKWebView（Wails 处理） | `internal/notifier/` |
| `recoverguard.go` | `MessageBoxW` | 复用 `internal/notifier` | `internal/notifier/` |
| `filedialog.go` | COM / `syscall.SyscallN` | 抽 `internal/dialog`，封装 wails Dialog（本身已跨平台） | `internal/dialog/` |
| `sys_watch.go` | 组合调用 dnd/进程 | 通过接口调用，本身平台中立 | 留 main（App 方法） |
| `capture_guard.go` | `SetWindowDisplayAffinity` | Windows-only 能力，抽接口，其它平台 no-op | `internal/platform/` |
| `mode_gui.go`/`mode_server.go` | `//go:build` | 已是 build tag，保留 | 根目录 |

---

## 6. 领域逻辑拆分（解耦巨型文件）

平台隔离之外，第二目标是**解耦巨型文件**。原则：**纯函数/纯数据抽到 `domain/` 或 `internal/` 子包，App 绑定方法留 main 但变薄**。

### 6.1 `settings.go`（1798 行）→ 拆

| 部分 | 性质 | 去向 |
|------|------|------|
| `Settings` 结构体 + `deepMerge` + `jsonCloneMap` + 校验 | 纯数据/纯函数 | `internal/settings/`（`package settings`） |
| 出厂默认值 `defaultSettingsJSON` + `builtinMusic` + `appPausePresets` | 纯数据 | 同上 |
| `func (a *App) GetSettings/SaveSettings/ResetSettings` | App 绑定方法 | 留 main（`bindings_settings.go`） |

> 关键：`Settings` 结构体被 `App.cfg` 引用、被几乎所有文件用。拆到 `internal/settings` 后，main 包 `import` 它，**不会循环依赖**（settings 包不 import main）。这是干净的拆分。

### 6.2 `scheduler.go`（1343 行）→ 拆

调度器的**状态机纯逻辑**（状态转换、下次时间计算、replan 判定）与 **App 绑定方法**（`StartBreak`/`Action` 等）分离：

| 部分 | 去向 |
|------|------|
| `Scheduler` 结构体 + `tick()` + `scheduleNext()` + 状态转换 | `internal/schedule/`（纯状态机，注入时间源） |
| `func (a *App) StartBreak/Action/...` | 留 main |

> 关键难点：`Scheduler` 依赖 `BreakInfo`/`Settings`/`TimeSlot` 等类型。这些类型需先抽到共享的 `internal/model/`（或 `domain/model`），供 schedule/settings/stats 共同引用，避免循环依赖。

### 6.3 `stats.go`（1726 行）→ 拆

| 部分 | 去向 |
|------|------|
| `StatsStore` + 天记录 + 落盘/读盘 + 防抖 | `internal/stats/`（纯数据 + 文件 IO） |
| `func (a *App) GetStats/GetHistory/...` | 留 main |

> 关键：`StatsStore.Load` 的重入死锁（`a.mu` 自锁）是既有坑，拆分后锁语义要重新审视（见 §9 风险）。

### 6.4 `takeout.go`（1016 行）→ 拆

外卖提醒的**纯计算**（下一时刻、去重键、推迟状态机）与窗口管理分离。窗口部分留 main（依赖 `application.WebviewWindow`），计算部分抽 `internal/takeout/`。

### 6.5 `breakplan.go`（1058 行）→ 拆

计划层的**纯计算**（工作日展开、时间段判定、下次休息时刻）抽 `domain/plan/`，这是最纯的部分，零 App 依赖、零平台依赖。

---

## 7. 共享类型层（`internal/model` 或 `domain/model`）

跨包拆分的前提是有一个**无循环依赖的共享类型层**：

```
internal/model/
├── break.go       # BreakInfo, TimeSlot, BreakEntry, BreakKind
├── schedule.go    # SchedulerState, PauseAction, ...
├── stats.go       # StatsDay, StatsRecord, ...
├── role.go        # SupervisorConfig, RoleMeta, SupGeom, ...
└── settings.go    # Settings（或单独 internal/settings）
```

**依赖方向必须单向**：`model` 不依赖任何包 → `domain/*`、`internal/*` 依赖 `model` → `main` 依赖所有。这是打破当前“全在 main 一个包互相引用”的关键。

---

## 8. 分阶段执行计划

> 每阶段结束 `go build ./...` + `go test -count=1 -tags server ./...` 全绿，才进入下一阶段。阶段间独立可回滚。

### 阶段 0：冻结基线（本次不动）
- 记录当前 357+ 测试全绿基线、`go vet` 干净、gofmt 干净。
- 提交当前目录整理变更（如果尚未提交）。

### 阶段 1：平台抽象层骨架（最低风险，先立接口）
1. 建 `internal/platform/`，定义 `Provider` 接口 + `Get()` 入口 + 三个平台文件的空实现（build tag）。
2. 先把**最容易剥离**的 `webview2_check.go` 里的 `MessageBoxW`/`ShellExecuteW` 抽成 `internal/notifier`，`recoverguard.go` 同步改用。
3. `filedialog.go` 抽 `internal/dialog`。
4. 验证：`go build -tags windows`（当前平台）+ 测试全绿。**此时还不能跨平台编译**（还有 hotkeys/sys_idle 等裸 Windows 代码），但接口已立。

### 阶段 2：系统能力接口化
1. `hotkeys.go` → `internal/hotkey`（Windows 实现 + linux/darwin no-op）。
2. `sys_idle.go` → `internal/sysinfo/idle`（Windows `GetLastInputInfo` + linux/macOS 桩）。
3. `sys_autostart.go` → `internal/autostart`（registry + desktop/launchd 桩）。
4. `capture_guard.go` 的 Windows-only 部分 → `internal/platform`。
5. **此时** `main` 包不再有裸 Windows API，可尝试 `GOOS=linux go build ./...`（可能仍有 wails 平台差异需处理）。

### 阶段 3：共享类型层 + settings 拆分
1. 建 `internal/model`，迁移 `BreakInfo`/`TimeSlot`/`SchedulerState` 等共享类型（这是最细的活，需逐个改引用）。
2. `settings.go` 的纯部分 → `internal/settings`。
3. 验证编译 + 测试。

### 阶段 4：调度器/统计/外卖/计划拆分
1. `breakplan.go` 纯计算 → `domain/plan`。
2. `scheduler.go` 状态机 → `internal/schedule`。
3. `stats.go` 纯数据 → `internal/stats`。
4. `takeout.go` 纯计算 → `internal/takeout`。
5. 每步验证。

### 阶段 5：跨平台编译验证 + 银河麒麟
1. `GOOS=linux GOARCH=amd64 go build` + `GOOS=linux GOARCH=arm64 go build`（银河麒麟常见架构）。
2. `GOOS=darwin go build`（macOS）。
3. 处理 wails v3 各平台差异（WebView2 vs WebKitGTK vs WKWebView 的 `application.Options` 差异）。
4. 建立 CI 矩阵（可选）。

---

## 9. 风险与难点（诚实评估）

1. **Wails 绑定契约不可动**：`App` 留 main 是硬约束，重构只能“变薄”不能“搬走”。契约测试 `contract_test.go` 是最后防线。
2. **锁语义是重灾区**：当前 `a.mu` 保护几乎所有共享状态，拆分时每个字段搬走都要重新确认锁的归属（`stats.go` 曾有 `a.mu` 自死锁坑）。**拆分过程最容易引入并发回归**，需 `-race`（本机当前缺 gcc，需先补 C 工具链）。
3. **共享类型层是最大工程量**：`BreakInfo`/`TimeSlot` 等类型被几十个文件引用，迁移到 `internal/model` 需要逐个改 import，是全项目最大的一次改动，极易出错。
4. **跨平台“真能跑”≠“能编译”**：`GOOS=linux go build` 通过只证明“没 Windows API 泄漏”，不代表 Linux 下托盘/自启动/空闲检测**行为正确**。银河麒麟的非 x86 架构还涉及 `unsafe` 指针、字节序等。**编译通过只是第一步，真机验证是另一座山**（当前无 Linux/macOS 测试环境）。
5. **wails v3 平台差异**：`application.Options`、`WindowsWindow`、`SystemTray`、`Dialog` 在不同平台的字段/行为不同（如 Linux 需 WebKitGTK 依赖、macOS 需 WKWebView），`main.go` 的窗口/托盘创建代码可能也要按平台拆分。
6. **`-race` 工具链缺失**：本机无 gcc/clang，`CGO_ENABLED=0`，race 检测跑不了。跨平台重构涉及共享状态，强烈建议先补 mingw gcc（之前 summary 提到装过 msys2，需确认）。

---

## 10. 需要你拍板的决策点

1. **平台支持范围**：先支持到“能编译 + Windows 行为不变”，还是要求 Linux/macOS/银河麒麟“真机可用”？前者是重构，后者是完整移植（工作量和测试环境要求完全不同）。
2. **共享类型层命名**：`internal/model` vs `domain/model`（Go 社区惯例：`internal/` 表示私有，`domain/` 表示领域层，两者都行，看你的偏好）。
3. **是否引入 Linux/macOS 第三方库**：如 `godbus`（Linux autostart/DBus）、`go-ole` 替代等。现阶段建议先用“接口 + no-op 桩”保证跨平台编译，真机适配时再逐个填实现。
4. **银河麒麟架构**：具体是 ARM64 还是龙芯 LoongArch？这决定 build tag 的架构表达方式。
5. **CI 矩阵**：要不要现在就搭 GitHub Actions 的多平台编译矩阵（linux/amd64、linux/arm64、darwin）？这能在无真机的情况下至少保证“编译不断”。

---

## 11. 建议的推进方式

**我建议先做“阶段 1 + 阶段 2”（平台抽象层 + 系统能力接口化）**，这两步：
- 收益最大（真正解决“跨平台编译”这个核心诉求）；
- 风险最低（只搬平台代码，不碰 `App` 绑定契约和共享锁语义）；
- 不依赖“共享类型层”这个最大工程量的前置。

**阶段 3-5（共享类型层 + 领域拆分）** 是“解耦巨型文件”的部分，工程量大、风险高，建议在阶段 1-2 稳定后，**单独评估**是否值得做（可能“解耦”的收益不如“平台隔离”大）。

---

等你确认决策点后，我再出**具体到文件、带 diff 概览和回归步骤的执行清单**，逐阶段落地。
