# 打工人休息提醒（WorkerRest）

本地优先、可深度自定义的休息提醒软件。常驻系统托盘，定时提醒你进行短休息与长休息，
缓解眼疲劳与久坐压力；支持督休专员立绘、外卖提醒、提前提示窗、使用统计等。

用 **Go + Wails v3** 实现，渲染层（HTML/CSS/JS）与历史 Electron 版一致，但运行时不依赖
Electron（181MB exe + 50MB Chromium），改用系统自带的 **WebView2**。单二进制 + embed 前端
资源，天然绿色便携。

> 本项目原为 Electron 版「吃饼人也敢休息？」的 Go 重构。迁移完成后 Electron 工程已删除，
> 本目录即为独立仓库。

## 环境要求

- **Windows 10 / 11**（依赖系统 WebView2 运行时：Win11 必带，Win10 通常已通过
  Windows Update 自动安装；缺失时首次启动会提示安装 WebView2 Runtime）
- **Go 1.26+**
- （可选）**Node.js + npm** —— 仅渲染层探针 `probe_renderer.py` 需要（Playwright 驱动 Edge）

## 运行

```powershell
# 开发运行（GUI，直接启动设置窗口）
go run .

# 构建 GUI 产物（无控制台窗口，去符号）
go build -o build/bin/chibingren.exe -ldflags "-H windowsgui -s -w" .

# 无头构建（验证专用：没有原生窗口，但照样服务资源与绑定端点）
go build -tags server -o build/bin/chibingren_server.exe -ldflags "-s -w" .
```

产物 `chibingren.exe` 约 12.3 MB（已含 embed 的前端资源与图标；四个内置提示音不再
embed 进二进制，打包时随包放在 `sounds/` 目录，开发态是模块根的 `sounds/`）。

## 打包

```powershell
# 便携目录打包：构建发布版 exe → 组装 exe + roles/ → 打 zip → 校验内容
python scripts/make_portable.py

# （可选）隔离环境解包首跑，验证预置三件套与首跑落点（不外溢到 APPDATA）
python scripts/verify_portable.py
```

产出 `release/WorkerRest-<版本>-portable.zip`（约 26 MB，含 22 MB 角色立绘）以及同名散装目录。

- 版本号默认读 `app.go` 的 `appVersion`（当前 1.0.0）；打包时通过
  `-ldflags "-X main.appVersion=<版本>"` 注入。也可 `--version <版本>` 覆盖。
- 解压即用，无需安装。**不做 NSIS 安装器**——单二进制天然绿色，便携目录 zip 即交付形态。
- 便携运行的数据（配置 / 统计 / 用户音频）落在 exe 旁的 `config/` 与 `data/`，跟着程序走；
  直接删掉整个目录就是完全卸载。

## 测试与验证

```powershell
go test ./...                  # 单元测试：契约 / shim 一致性 / 归一化 / 状态机 / 系统能力 / 提示音 / 统计 / 角色迁移 / 外卖 / 更新 / 睡眠检测
python scripts/smoke_server.py   # 绑定层无头冒烟：166 项断言
python scripts/probe_renderer.py # 渲染层探针：120 项断言 + 12 张截图（需先 npm install）
python scripts/probe_gui_boot.py # GUI 启动探针：16 项断言（系统能力 / 提前提示窗 / 提示音宿主）
```

- 三个脚本都能无头运行，报告写在 `%TEMP%`（`wr_go_smoke_report.txt` 等），退出码 0 = 全通过。
- 冒烟 / 探针会**隔离配置**（拷真实配置进 `%TEMP%` + `WR_SETTINGS_PATH` 独占），绝不改动
  运行目录里的真实配置与统计。

## 目录结构

```
  *.go                    后端源码（App 窗口/会话 + 调度器/计划 + 统计 + 系统能力 + 各窗口）
  main.go                 入口：注册服务 + 主窗口（= 设置窗口）
  contract.go             前端绑定「契约表」：wiredCalls 列表 + 启动自检
  roles/                  内置角色模板（22MB 立绘，随包分发）
  sounds/                 内置提示音源文件（四个 wav，打包时预置进便携包）
  manual/操作手册.html     随包操作手册（自包含 HTML）
  frontend/dist/          渲染层产物（已固化；shim.js 把 window.api 适配到 Go 绑定）
  assets/icon.ico         托盘图标（go:embed）
  default_settings.json   出厂默认配置（go:embed）
  scripts/                验证与打包脚本
```

核心模块一览：`breakplan.go`（计划层）、`scheduler.go`（调度器状态机）、`stats.go`（使用统计）、
`sound.go`（提示音）、`prebreak.go`（提前提示窗）、`takeout.go`（外卖提醒）、
`sys_*.go`（空闲 / 勿扰 / 进程 / 自启动 / 睡眠检测）、`filedialog.go`（原生文件对话框）、
`update.go`（检查更新）、`migrate_roles.go`（旧配置角色迁移）。

## 数据与配置落点

- 一律「数据与程序同目录」：配置、统计、用户音频统一落在 exe 旁的 `config/` 与 `data/` 下，
  整目录拷贝 / 删除就是完整的迁移 / 卸载。不再检查 `%APPDATA%`（老版本的历史目录已放弃兼容）。
- 打包交付的便携目录**自带**三件套：`sounds/`（四个内置提示音）、`config/settings.json`
  （出厂默认，首跑补 installId）、`data/stats.json`（空统计表）——首跑直接在预置文件上继续写。

## 已知差异（与历史 Electron 版）

- **z-order 抬升降级**：Electron 用 `moveTop()` 让外卖压到休息窗口之上；Wails v3 没有 z-order
  抬升 API，改用 `SetAlwaysOnTop(true) + Focus()` 近似（同层精确层级无法逐像素复刻）。
- **系统睡眠事件**：Electron 靠 `powerMonitor` 实时通知；Go 侧没有电源事件源，改为**醒来后
  补记**——检测「墙钟跳变 - `GetTickCount64` 跳变」超过 60 秒即判定睡过，把睡眠整段记进离开。
- **便携数据落点**：Go 版统一「配置文件所在目录旁」，与 Electron 的 `userDataPath` 约定略有
  不同，但功能不受影响（落点由配置文件位置推导，两边读写各自的份）。
