# 吃饼人也敢休息？（WorkerRest）

本地优先、可深度自定义的休息提醒软件。常驻系统托盘，定时提醒你进行短休息与长休息，
缓解眼疲劳与久坐压力；支持督休专员立绘、外卖提醒、提前提示窗、使用统计等。

用 **Go + Wails v3** 实现，渲染层（HTML/CSS/JS）与历史 上一版一致，但运行时不依赖
上一版（181MB exe + 50MB Chromium），改用系统自带的 **WebView2**。单二进制 + embed 前端
资源，天然绿色便携。

> 本项目原为 上一版「吃饼人也敢休息？」的 Go 重构。迁移完成后 上一版工程已删除，
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
go build -o build/bin/rest.exe -ldflags "-H windowsgui -s -w" .

# 无头构建（验证专用：没有原生窗口，但照样服务资源与绑定端点）
go build -tags server -o build/bin/chibingren_server.exe -ldflags "-s -w" .
```

产物 `rest.exe` 约 12.3 MB（已含 embed 的前端资源与图标；四个内置提示音不再
embed 进二进制，打包时随包放在 `sounds/` 目录，开发态是模块根的 `sounds/`）。

## 打包

```powershell
# 便携目录打包：构建发布版 exe → 组装 exe + supervisors/ → 打 zip → 校验内容
python scripts/make_portable.py

# （可选）隔离环境解包首跑，验证预置三件套与首跑落点（不外溢到 APPDATA）
python scripts/verify_portable.py
```

产出（`release/WorkerRest-<tag>/`，tag 形如 `v0.9.1`）：

| 文件 | 用途 |
|---|---|
| `WorkerRest-portable.zip` | 便携目录 zip（含约 4.5 MB 督休专员立绘），解压即用 |
| `WorkerRest-portable/` | 同名散装目录 |
| `rest.zip` | 只含 `rest.exe` 的自动更新包（最大压缩，软件下载的就是它） |
| `version.json` | 自动更新清单（version / date / url / sites / changes） |

同一版本的四种产物都落在 `release/WorkerRest-<tag>/` 里；目录名与压缩包名**不带版本号**，
版本只体现在父目录名与 `version.json` 里。

- **版本号只认 git tag**（`v主.次.修订`，如 `v0.9.0`）：打包要求**当前提交上有 tag**，
  没 tag 直接停止并提示先打 tag（同时显示最近一次 tag）。`app.go` 的 `appVersion`
  只是 `dev` 占位符，真实版本由 `-ldflags "-X main.appVersion=<tag 去掉 v>"` 注入。
- `python scripts/version.py` 显示最近一次 tag 的版本号（`--bare` 去掉 `v`）。
- **出厂默认只有一份源文件** `default_settings.json`：`//go:embed` 进 exe 的是它（「恢复默认
  设置」用的就是这份），便携包的 `config/settings.json` 也是它（新装首跑读这份）。两份必须
  一致，否则「首装看到的值」与「点恢复默认后的值」会不同。打包时 `make_portable.py` 先让
  exe 用 `--dump-default-settings <文件>` 把内嵌那份吐出来、与仓库文件逐项比对，不一致直接
  停止（`--no-build` 用上一轮 exe 时最容易踩），再用导出的内容写进便携包；发布后
  `verify_portable.py` 用包里的 exe 再复核一次。单测 `TestEmbeddedDefaultMatchesFile`
  另钉住「编译时快照 == 工作区文件」。
- **默认检查更新地址 = `https://www.goodgoodlive.com/worker-rest/version.json`**，同时是
  **出厂兜底源**：写在 `default_settings.json` 的 `sites` 里（便携包的 `config/settings.json`
  是它的副本），`update.go` 里还有一份 `defaultUpdateSite` 常量，永远排在候选链**最后**。
  因此配置被改坏、或被远程换成别的源，软件都还留有一条官方更新通道；配置里一条源都没有时
  也照样能查。
- `version.json` 的两个地址字段口径不同：`url` 是**下载**地址数组（按顺序容错，第一项失败
  取下一项），`sites` 是**检查更新**地址数组（远程下发会整体覆盖客户端本地 `sites`）。
  **其他地址（含 `nav.hcctc.cn`）都是可选项**：发布清单里给了就用、没给就不用，程序不针对
  任何具体域名做特殊处理。`scripts/make_portable.py` 里的 `UPDATE_SITES` 必须与
  `default_settings.json` 的 `sites` 逐字一致——打包前会校验，不一致直接停止；发布后
  `verify_portable.py` 还会核对便携包的 `sites` 与同版本 `version.json` 的 `sites` 相同。
- `version.json` 的 `changes` 取「上一个 tag → 当前 tag」的提交标题，按时间顺序自动编号；
  首个 tag 没有上一个 tag，`changes` 为空。这是机械摘要，脚本跑完最后会提示**手动改写
  `release/WorkerRest-<tag>/version.json` 的 `changes`**，改成面向用户的发布说明。
- 解压即用，无需安装。**不做 NSIS 安装器**——单二进制天然绿色，便携目录 zip 即交付形态。
- 便携运行的数据（配置 / 统计 / 用户音频）落在 exe 旁的 `config/` 与 `data/`，跟着程序走；
  直接删掉整个目录就是完全卸载。

## 检查更新：自动 vs 手动

两条路径共用同一套联网逻辑（`checkForUpdate`）与同一套落库（`publishUpdateStatus`：
写内存 → 推 `update:status-changed` → 持久化 `lastUpdateResult`）。差别只在**什么时候查**
和**查到之后怎么告诉用户**。

| 维度 | 自动检查 | 手动检查 |
|---|---|---|
| 入口 | 启动时 `startAutoUpdateCheck` → 常驻 `autoUpdateLoop`（仅在勾选「自动检查更新」时运行） | 关于页「检查更新」按钮 → 绑定 `CheckUpdate()` |
| 时机 | 当天**首次键鼠活动**后 0~3 小时内随机一刻；到点若处于「暂停休息」，再在 0~30 分钟内随机一刻，反复直到不再暂停 | 点击立即联网，最长等 `updateTimeout` × 更新源个数 |
| 频率 | 一天一次：`lastUpdateCheck == 今天` 就不再查；`error` 不记当天，当天稍后还会再排；跨天整体重置，当天没查成不影响次日 | 不限次；**不写** `lastUpdateCheck`，不占自动检查当天的名额 |
| 界面反馈 | 静默，只改关于页状态条与「立即更新」按钮 | 立刻进加载态（按钮「检查中…」置灰 + 状态条「正在检查更新…」），`finally` 复原 |
| 发现新版本 | **有条件弹窗**：① 勾选了「自动检查更新」（初始渲染传 `autoModal = S.checkUpdateOnStart`）；② 该版本没被忽略（`st.ignored` 为假）；③ 本次会话没为同一版本弹过（`updateModalShownFor`） | **一律弹窗**（`renderUpdate(st, true, true)` 的 `force`）：忽略过、已弹过都照弹 |
| 已是最新 | 无提示，状态条显示「已是最新版本」 | 状态条 + toast「已是最新版本」 |
| 检查失败 | 无提示（状态条「更新检查失败」），当天稍后按规则重试 | 状态条 + toast「检查更新失败（原因）」 |

其他约定：

- 「忽略本版本」（`ignoreVersion`）**只压制自动弹窗**：按钮仍显示「立即更新」；当发现比被忽略
  版本更大的版本号时会自动解除忽略、恢复自动弹窗；用户手动点检查则始终能看到弹窗。
- 检查结果会持久化（`lastUpdateResult`），重启后打开关于页仍显示「发现新版本 x.y.z」；
  但只有勾了自动检查时才会在打开设置页那一刻自动弹窗，否则只显示状态条。
- 手动检查期间前端会丢弃 `update:status-changed` 推送（`if (checking) return`），以免后台
  推来的旧结果把加载态盖掉；结果以 `CheckUpdate()` 的返回值为准。
- 「自动检查更新」开关只在启动时读一次：中途关掉会停掉排期，重新打开需重启软件。
- 无头模式（`-tags server`）不联网：`CheckUpdate` 返回确定性的 `error`，自动检查直接跳过。

回归覆盖见「测试与验证」里的渲染层探针：`metrics.updateLoading`（点击即进入加载态）、
`metrics.updateModalForce`（同版本 + `ignored: true` 连点两次都必须弹窗）。

## 测试与验证

```powershell
go test -tags server ./...         # 单元测试（246 用例）：契约 / shim 一致性 / 归一化 / 状态机 /
                                   # 系统能力 / 提示音 / 统计 / 督休专员迁移与命名唯一 / 外卖 / 更新 / 睡眠检测
python scripts/smoke_server.py     # 绑定层无头冒烟：176 项断言
python scripts/probe_renderer.py   # 渲染层探针：270 项断言 + 截图（需先 npm install）
python scripts/probe_gui_boot.py   # GUI 启动探针：16 项断言（系统能力 / 提前提示窗 / 提示音宿主）
python scripts/probe_crossday.py   # 跨天探针：11 项（报表默认范围必须现算，不能沿用昨天的）
python scripts/verify_about.py     # 关于页字段与配色核对（浅色 / 深色各一张截图）
```

- **单测必须带 `-tags server`**：不带时「提前提示窗」用例会因 `a.app == nil` panic（无头形态
  没有原生窗口），这是既有约束，不是回归。
- 上述脚本全部无头运行，报告写在 `%TEMP%`（`wr_go_smoke_report.txt` 等），退出码 0 = 全通过。
- 冒烟 / 探针会**隔离配置**（拷真实配置进 `%TEMP%` + `WR_SETTINGS_PATH` 独占），绝不改动
  运行目录里的真实配置与统计。

## 目录结构

```
  *.go                    后端源码（App 窗口/会话 + 调度器/计划 + 统计 + 系统能力 + 各窗口）
  main.go                 入口：注册服务 + 主窗口（= 设置窗口）
  contract.go             前端绑定「契约表」：wiredCalls 列表 + 启动自检
  supervisors/            内置督休专员模板（17 个专员，立绘均为 webp，约 4.5 MB，随包分发；子目录用专员名称命名，key 存于 role.json；无 role.json 的目录只作素材、不进包）
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
`update.go`（检查更新）、`migrate_roles.go`（旧配置督休专员迁移）。

## 数据与配置落点

- 一律「数据与程序同目录」：配置、统计、用户音频统一落在 exe 旁的 `config/` 与 `data/` 下，
  整目录拷贝 / 删除就是完整的迁移 / 卸载。不再检查 `%APPDATA%`（老版本的历史目录已放弃兼容）。
- 打包交付的便携目录**自带**三件套：`sounds/`（四个内置提示音）、`config/settings.json`
  （出厂默认，首跑补 installId）、`data/stats.json`（空统计表）——首跑直接在预置文件上继续写。
- **督休专员的出场顺序可拖动调整**：设置页「督休专员」→ 候选专员列表，按住立绘磁贴拖动即可
  改顺序，松手即生效并自动保存。顺序存于 `config/settings.json` 的 `supervisor.order`
  （role key 数组），重启后仍在；画廊显示顺序与「顺序轮换」模式的出场次序**共用这一份**，
  两边永远一致。出厂默认 `order` 为空数组 = 按内置清单顺序，补装/新增的专员自动排到末尾。
- **督休专员的显示名唯一**：导入立绘时会弹命名框；提交的名字若与现有专员重名，
  **拒绝保存**并把错误留在框里、要求重新输入（对话框不关、已输入的字保留）。
  Go 侧 `SaveRole` 也会再兜一层底（重名直接返回 false、磁盘零副作用），
  软件自己起名的场景（导入用文件名、迁移旧配置）则自动退让成「名字 (2)」。
  ⚠️ 显示名唯一只管「同名」：**同一张图导入两次**（key 撞号）由另一道防线兜住
  （`uniqueRoleKey` 给 key 加 `-2` 后缀）—— 两道防线互补，缺一不可。

## 已知差异（与历史 上一版）

- **z-order 抬升降级**：上一版 用 `moveTop()` 让外卖压到休息窗口之上；Wails v3 没有 z-order
  抬升 API，改用 `SetAlwaysOnTop(true) + Focus()` 近似（同层精确层级无法逐像素复刻）。
- **系统睡眠事件**：上一版 靠 `powerMonitor` 实时通知；Go 侧没有电源事件源，改为**醒来后
  补记**——检测「墙钟跳变 - `GetTickCount64` 跳变」超过 60 秒即判定睡过，把睡眠整段记进离开。
- **便携数据落点**：Go 版统一「配置文件所在目录旁」，与 上一版的 `userDataPath` 约定略有
  不同，但功能不受影响（落点由配置文件位置推导，两边读写各自的份）。
