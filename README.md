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
- **Go 1.26+**（`go.mod` 的语言版本仍是 1.26.3；本仓库用 **1.27.3** 构建与验证，
  `appInfo()` 里仍回传编译时的真实 Go 版本，供探针与排障使用）
- **Wails v3**：`github.com/wailsapp/wails/v3 v3.0.0`（正式版；v3 自身也要求 Go 1.26+，
  与上一条本来就是同一个约束）。`appInfo().wailsVersion` 读的是构建信息里的真实依赖版本，
  升级后在探针输出里就能看出跑的是 beta 还是正式版（关于页不再展示这一行）
- （可选）**Node.js + npm** —— 仅渲染层探针 `probe_renderer.py` 需要（Playwright 驱动 Edge）

## 运行

```powershell
# 开发运行（GUI，直接启动设置窗口）
go run .

# 构建 GUI 产物（无控制台窗口，去符号，不带构建机路径）
go build -trimpath -o build/bin/rest.exe -ldflags "-H windowsgui -s -w" .

# 无头构建（验证专用：没有原生窗口，但照样服务资源与绑定端点）
go build -trimpath -tags server -o build/bin/chibingren_server.exe -ldflags "-s -w" .
```

`-trimpath` 不是可选项：不加它，二进制里每个源文件都以 `D:/…/xxx.go` 的绝对路径存在
（panic 栈、运行时符号），等于把构建机的目录结构和用户名一起发出去。加了之后路径统一
变成 `workerrest/xxx.go`。`scripts/verify_portable.py` 有一条断言专门扫这个泄漏。

产物 `rest.exe` 约 13.9 MB（Go 1.27.3 + Wails v3.0.0 下实测 13,949,440 字节），已含
embed 的前端资源与图标；四个内置提示音不再 embed 进二进制，打包时随包放在 `sounds/`
目录，开发态是模块根的 `sounds/`。

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
| `version.json` | 自动更新清单（version / date / url / sites / changes / sha256 / size） |
| `THIRD-PARTY-NOTICES.txt` | 开源鸣谢清单（同一份也内嵌在 exe 里，见「开源鸣谢」一节） |
| `清除开机自启动项.bat` | 删目录前清掉注册表里的开机自启动项（便携版没有卸载器） |

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
- **出厂默认 = 作者当时在用的那套配置**（2026-10-29 定）：`default_settings.json` 的每一项
  都按当天的 `config/settings.json` 取值，所以点「恢复默认设置」得到的就是他日常在用的状态。
  三类东西不在此列：**声音**（`sound.music` 与内置音由 `normalizeSound` 种入，文件在 `sounds/`）、
  **督休专员**（`supervisors/` 目录 + 各 `meta.json`，`supervisor` 段仍按出厂 random 口径）、
  **统计**（另存 `stats.json`）。改默认值时容易漏的三处必须同值：出厂 JSON、Go 侧兜底
  （`AppearanceCfg` 的透明度 / 宽高百分比 / 全屏、`FullScreenPause`）、操作手册里的默认值文案。
- **`ResetSettings` 会跑一遍 `normalizeLoad`**：只把出厂 JSON 写下去的话，恢复默认后提醒内容
  面板、指定应用列表、提示音列表都是空的，要重启一次才种入；跑完之后立刻就是出厂那套值
  （配置里那枚固定键仍保留，`settings.backup.json` 仍先备份）。
- **exe 文件图标来自仓库根的 `rsrc_windows_amd64.syso`**（随源码入库）：`go build` 会自动链接
  同包目录下的 `*_windows_amd64.syso`，所以 `make_portable.py` 与 ldflags 都不用改。图标源就是
  全项目唯一那份 `frontend/dist/icon.png`，重做图标后重新生成一次即可：
  ```powershell
  # 1) 用 Pillow 从 icon.png 生成多尺寸 ico（16/24/32/48/64/128/256，资源管理器大图标要 256）
  # 2) go run github.com/akavel/rsrc -arch amd64 -ico app.ico -o rsrc_windows_amd64.syso
  ```
  校验方式：资源管理器里直接看 exe 图标，或解析 PE 资源确认 `RT_GROUP_ICON(14)` 存在
  （`VersionInfo` 只看版本字段，与图标无关）。**托盘与标题栏不需要另做图**：托盘直接吃这份
  PNG（Wails v3 的 `w32.CreateSmallHIconFromImage` PNG/ICO 都认，系统再按 `SM_CXSMICON` 缩放），
  标题栏是同一张图按 20px 显示。
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
- **更新包的完整性只认 `sha256`**（`version.json` 的 `sha256` / `size` 由
  `make_portable.py` 从刚生成的 `rest.zip` 现算，不是手填；改过 `rest.zip` 就必须重算）。
  客户端的规矩（实现见 `update_trust.go`）：
  - 只接受 **https** 源，`http://` 明文源一律丢弃（唯一例外是回环地址，仅为单测的
    httptest 保留）；**重定向的每一跳也重新校验**，所以 https 302 到 http 会被拦下；
  - 清单缺 `sha256`、格式不对、或 `url` 全是 http → `checkForUpdate` 直接返回 error
    并说明原因，**不会**降级成「只靠 TLS」；
  - 下载后逐字节比对哈希，不符就删掉换下一个镜像；全部失败则拒绝更新；
  - 体积上限：清单 1 MiB、更新包 64 MiB、解压总量 192 MiB、条目数 2048
    （防 zip bomb），超限即中断。
  打包脚本在 `rest.zip` 缺失或未生成时会直接失败——没有哈希的清单等于没有更新通道。
- 解压即用，无需安装。**不做 NSIS 安装器**——单二进制天然绿色，便携目录 zip 即交付形态。
- 便携运行的数据（配置 / 统计 / 用户音频）落在 exe 旁的 `config/` 与 `data/`，跟着程序走；
  直接删掉整个目录就是完全卸载。唯一的目录外残留是注册表里的开机自启动项（只有勾过才有），
  删目录前先跑包里的 `清除开机自启动项.bat`：它只删 `HKCU\…\Run` 与
  `HKCU\…\Explorer\StartupApproved\Run` 里本软件那一项，不删文件、不碰别的软件。
  这个脚本**刻意不叫「卸载.bat」**——它不删任何文件，叫卸载会让用户以为删完目录就干净了。
  脚本里的值名与 `sys_autostart.go` 的 `runValueName` 是两份真源，`make_portable.py`
  打包前会解析源码逐字比对（连键路径一起），不一致直接停；`verify_zip` 断言包里有它。

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
go test -tags server ./...         # 单元测试（295 用例）：契约 / shim 一致性 / 归一化 / 状态机 /
                                   # 系统能力 / 提示音 / 统计 / 督休专员迁移与命名唯一 / 外卖 / 更新 / 睡眠检测
python scripts/smoke_server.py     # 绑定层无头冒烟：179 项断言
python scripts/probe_renderer.py   # 渲染层探针：297 项断言 + 截图（需先 npm install）
node   scripts/a11y_audit.js         # 可访问性静态审计：无名控件 / 活区 / 对话框语义，写 build/a11y_audit.json
node   scripts/probe_a11y_focus.js # 对话框焦点管理行为探针：101 项（进得去 / Tab 跑不出 / 关后回得来）
python scripts/probe_gui_boot.py   # GUI 启动探针：16 项断言（系统能力 / 提前提示窗 / 提示音宿主）
python scripts/probe_crossday.py   # 跨天探针：11 项（报表默认范围必须现算，不能沿用昨天的）
python scripts/probe_stats_file.py # 统计文件自愈探针：19 项（首跑自建 data/ / 损坏改名备份 / 弹窗说明 / Ack 清空）
python scripts/probe_sound_scan.py # 提示音目录体检探针：27 项（新音频原地登记 / 死条目删除 / 重启不重复提示）
python scripts/verify_about.py     # 关于页字段与配色核对（浅色 / 深色各一张截图）
python scripts/verify_window_size.py  # 设置窗口尺寸：14 项（勾选框 / 默认尺寸下恰好不滚 / 宽度边界 / 落盘）
python scripts/probe_window_size.py   # 尺寸**测量**（不是断言）：逐 10px 扫宽度、逐 2px 扫高度，改版式后重测用
python scripts/probe_window_gui.py    # 真窗口尺寸：9 项（首次按出厂尺寸 / 拖拽记录 / 重启沿用 / 关掉开关回默认）
```

- **单测必须带 `-tags server`**：不带时「提前提示窗」用例会因 `a.app == nil` panic（无头形态
  没有原生窗口），这是既有约束，不是回归。
- `tray_icon_test.go` 钉住图片资产这一层：托盘图标（`frontend/dist/icon.png`，唯一那份图标）
  必须能从 embed 读到、是合法 PNG，并且**真的走一遍 Wails 的转换链** `w32.CreateSmallHIconFromImage`
  拿到非零 HICON —— 格式不被接受时托盘只是静默变空白图标，不跑测试发现不了；另外检查 dist 下
  每张位图非空（图片个数不写死，增删图片不该让测试变红）。
- 上述脚本全部无头运行，报告写在 `%TEMP%`（`wr_go_smoke_report.txt` 等），退出码 0 = 全通过。
- 冒烟 / 探针会**隔离配置**（拷真实配置进 `%TEMP%` + `WR_SETTINGS_PATH` 独占），绝不改动
  运行目录里的真实配置与统计。
- **可访问性两条线**：`scripts/a11y_audit.js` 看静态语义（新增输入框必须配 `label[for]` 或
  `aria-label`，新增对话框必须 `role` + `aria-modal` + `aria-labelledby`）；`scripts/probe_a11y_focus.js`
  看行为（打开聚焦、Tab 循环、关闭归还）。对话框开关一律只改 `class="hidden"`，焦点由
  `settings.js` 顶部的 MutationObserver 统一接管 —— **不要**在各调用点再手写 `focus()`。
- `make_portable.py` 组装前会核对 **exe 比源码新**：`--no-build` 复用上一轮产物时，只要改过
  任何非测试 `*.go` 或任一 `//go:embed` 资源（`frontend/dist/**` 等）就会被拒（rc=1），
  信息里直接列出比 exe 新的文件。这是 `build/bin` 里放旧 exe 也不至于发错包的兜底。

## 目录结构

```
  *.go                    后端源码（App 窗口/会话 + 调度器/计划 + 统计 + 系统能力 + 各窗口）
  main.go                 入口：注册服务 + 主窗口（= 设置窗口）
  contract.go             前端绑定「契约表」：wiredCalls 列表 + 启动自检
  supervisors/            内置督休专员模板（17 个专员，立绘均为 webp，约 4.5 MB，随包分发；子目录用专员名称命名，meta.json 里只有名字（名字即身份，没有内部 id）；无 meta.json 的目录只作素材、不进包）
  sounds/                 内置提示音源文件（四个 wav，打包时预置进便携包）
  manual/操作手册.html     随包操作手册（自包含 HTML）
  frontend/dist/          渲染层产物（已固化；shim.js 把 window.api 适配到 Go 绑定）+ 全部图片资产：
                          icon.png **全项目唯一图标**（标题栏 20px / 托盘 / exe 图标源三处共用）、
                          sitting_risk.webp 关于页横幅 —— 整个目录 go:embed 进 exe，运行时只认这里
  rsrc_windows_amd64.syso exe 文件图标资源（RT_GROUP_ICON，16~256 七档，由 frontend/dist/icon.png 生成）
  default_settings.json   出厂默认配置（go:embed）
  scripts/                验证与打包脚本
```

核心模块一览：`breakplan.go`（计划层）、`scheduler.go`（调度器状态机）、`stats.go`（使用统计）、
`sound.go`（提示音）、`prebreak.go`（提前提示窗）、`takeout.go`（外卖提醒）、
`sys_*.go`（空闲 / 勿扰 / 进程 / 自启动 / 睡眠检测）、`filedialog.go`（原生文件对话框）、
`window_size.go`（设置窗口的默认 / 最小 / 记住上次尺寸）、`update.go`（检查更新）、
`migrate_roles.go`（旧配置督休专员迁移）。

## 数据与配置落点

- 一律「数据与程序同目录」：配置、统计、用户音频统一落在 exe 旁的 `config/` 与 `data/` 下，
  整目录拷贝 / 删除就是完整的迁移 / 卸载。不再检查 `%APPDATA%`（老版本的历史目录已放弃兼容）。
- **`sounds/` 在启动时体检一次**（见 `sound_scan.go`）：用户直接把音频丢进 exe 旁的 `sounds/`，
  或手动删掉某个已登记条目的文件时，设置页会弹窗逐项询问 ——「发现 N 个新提示音」可一键
  **原地登记**（不复制、不改名，否则原文件下次启动又会被当成新的，变成每次开机都问一遍）；
  「N 个提示音的文件已不在」可一键**从配置删除**（只删条目，不动任何文件）。启动时只记录、
  不擅自改配置；点「暂不处理」本次运行不再问，下次启动重新扫。
- 打包交付的便携目录**自带**两件套：`sounds/`（四个内置提示音）、`config/settings.json`
  （出厂默认，首跑补上固定键）——首跑直接在预置文件上继续写。
- **`data/` 与 `stats.json` 不进安装包**（统计是用户数据，不属于发行物）：
  - 首跑：`data/` 目录与空的 `data/stats.json` 由程序自动创建（见 `stats.go` 的 `Load`）；
  - 损坏：文件**读不出来**时不动它、只从空表开始用；**格式错误（解析失败 / 不是 JSON 对象）**
    时先把原文件改名成 `data/stats_<日期_时间>.json` 留作备份，再重建空表，并在设置页弹窗
    说明「备份在哪、为什么判定损坏」——历史统计不会无声消失；
  - 更新：只替换 `rest.exe`（旧的改名成 `.bak`），解压与合并配置都在 `update/` 与 `config/`
    里进行，**不碰** `data/`。
- **督休专员的出场顺序可拖动调整**：设置页「督休专员」→ 候选专员列表，按住立绘磁贴拖动即可
  改顺序，松手即生效并自动保存。顺序存于 `config/settings.json` 的 `supervisor.order`
  （专员名数组），重启后仍在；画廊显示顺序与「顺序轮换」模式的出场次序**共用这一份**，
  两边永远一致。出厂默认 `order` 为空数组 = 按内置清单顺序，补装/新增的专员自动排到末尾。
- **督休专员的显示名唯一**：导入立绘时会弹命名框；提交的名字若与现有专员重名，
  **拒绝保存**并把错误留在框里、要求重新输入（对话框不关、已输入的字保留）。
  Go 侧 `SaveRole` 也会再兜一层底（重名直接返回 false、磁盘零副作用），
  软件自己起名的场景（导入时用文件名）则自动退让成「名字 (2)」。
  判据是**目录名口径**而不是字符串相等：`A/B` 与 `AB`、`Alice` 与 `alice`、
  `abc.` 与 `abc` 都算重名（它们会落到同一个目录上）。**同一张图导入两次**也走这条 ——
  第二个自动叫「我的图 (2)」，不会出现两个同名专员抢一个目录。

## 开源鸣谢（第三方许可）

随包声明的第三方组件、版本与许可全文在仓库根 `THIRD-PARTY-NOTICES.txt`。文件内部只有三段，
**不含任何说明性文字、也不写文件级标题**（弹窗标题已经叫「开源鸣谢」，正文再顶一遍是重复）：
致谢 → 组件清单（名称 / 版本 / 协议，列对齐）→ 逐段许可全文（78 字符横线分隔）。
它由生成器从**构建事实**推导（不是手抄清单）：

```powershell
python scripts/gen_notices.py           # 重新生成（依赖或 Go 版本变了才需要）
python scripts/gen_notices.py --check   # 只校验：与当前构建不一致则退出码 1
```

- 收录口径 = **实际参与构建**的组件：Go 模块取自 `go list -deps`（与 `go version -m`
  读出的内嵌依赖一致）、Go 标准库与运行时、以及内联进 `frontend/dist` 产物的库
  （`qrcode` / `dijkstrajs`）。未被链接的 `go.sum` 条目不在其列。
- 同一份文件两个去处：`//go:embed` 进 exe（`notices.go`），并由打包脚本放到便携包根目录。
  只把声明编译进机器码、却没有读取入口，等于没有复现 —— 所以设置页「关于 → 软件信息 →
  开源鸣谢」可读全文，并可「另存为…」导出一份（内容与内嵌文本相同，导出时转 CRLF）。
- **依赖变了必须重新生成**：`make_portable.py` 在 `go build` **之前**跑 `--check`，过期直接
  停止 —— 清单是编进 exe 的，编进去之后所有校验都看不出「一致地过期」。单测
  `TestNoticesCoversLinkedModules` 用 `go list -deps` 两个方向核对（不缺、也不多声明）。
- 升级前端内联库时，把 npm 包里的许可原文放回 `scripts/notices/`，并同步 `gen_notices.py`
  的 `FRONTEND`（含产物指纹串）：指纹在 bundle 里找不到会直接报错，防止清单悄悄过期。
- 这份文件只声明**第三方**组件；本仓库自己的代码没有单独的 `LICENSE` 文件。

## 后台 goroutine 的 panic 兜底

Wails v3 只保护**绑定方法**（`pkg/application/bindings.go` 把 panic 转成 `CallError`，
前端 promise reject、进程存活）。但调度 tick、空闲/勿扰/应用占用监视、托盘刷新、外卖检查、
统计落盘、自动更新排期这些循环都跑在本项目自建的 goroutine 里，不在框架保护范围内 ——
一处 nil deref 就会让进程直接终止，而发布版是 GUI 子系统，用户看到的是「窗口和托盘图标
一起消失，日志里什么都没有」。

`recoverguard.go` 给这些循环加了统一兜底：

| 层次 | 行为 |
|---|---|
| `guardRun(name, fn)` | 包住**每一轮**（不是循环外层）。panic 只丢这一拍，记日志后继续 |
| 日志限流 | 每个位置首次打全栈，之后 5 分钟内不再重复（循环里每 500 ms 炸一次时，全栈会把 2 MiB×3 的滚动日志瞬间填满，把第一次的现场冲掉） |
| 熔断 | 同一位置 2 秒内连炸 3 次 → 判定不可恢复（典型是 panic 落在 Lock 与 Unlock 之间、互斥量没释放，后续 tick 全卡死），落盘 + 弹 `MessageBoxW` 指出日志路径 + 以退出码 1 退出 |
| `main()` 顶层 recover | 兜住主 goroutine，同样落盘 + 提示 |

- **约定：临界区里不放会 panic 的业务调用。** `recover` 无法代你解锁（`sync.Mutex` 没有
  「外部释放」），panic 落在锁段中间时这一层救不回来，只能靠熔断收场。锁段保持「只读字段 /
  只赋值」的短小形状，是这套机制成立的前提。
- `guardRunOK` 额外返回「这一轮是否跑完」：自动更新检查 panic 后不能立刻重试
  （轮询粒度 2 秒，一秒内连炸三次正好撞熔断阈值），拿到 false 改为退避一分钟。
- `TestBackgroundGoroutinesAreGuarded` 扫全仓：任何一个起了后台 goroutine 的文件里没有
  `guardRun` 就红 —— 新增循环忘了包兜底，测试直接拦住。

## 设置窗口尺寸

出厂尺寸写死在 `default_settings.json` 的 `window` 块里（`window_size.go` 顶部有完整推导）：

```json
"window": { "width": 874, "height": 857, "rememberSize": true, "lastWidth": 0, "lastHeight": 0 }
```

- **874 是用户选定的宽度**（2026-10-29 他把窗口拖到这个尺寸后要求「记住现在的尺寸，设为默认」）。
- **高度不是随手填的**：`scripts/probe_window_size.py` 起无头服务后逐 2px 扫高度，取宽度 ≥820 时
  「关于页恰好不出垂直滚动条」的那一档。2026-10-31 关于页先因「第三方许可」一行抬到 893，
  同一天又把「发布时间」并进「版本」行、删掉独立的一行（−36px 行高），边界回落到 **857**
  （`857 − 44（38 标题栏 + 6 底部透明边框）= 813 = 内容区高度`；855 就滚 2px）。
  **关于页内容或底部 padding 一变，就必须重跑该脚本重取高度**，否则 `verify_window_size.py` 的
  「出厂尺寸下关于页恰好不滚」会红。宽度一旦回落到 760，关于页需要更高的窗口 —— 那属于
  「窗口被用户缩窄，允许出滚动条」，不影响出厂值。
- **最小宽度 760 也是实测的**：同一脚本逐 10px 扫宽度，750 起才没有横向裁切（740 时 `.row.field`
  溢出 2px），取 760 留 10px 余量。两个边界与滚动条是否出现无关（620 / 940 两个高度下结论一致）。
- `width` / `height` 是**默认尺寸**，`lastWidth` / `lastHeight` 是**上次用过的尺寸**，两对数各管
  一头 —— 共用一对的话「记住尺寸」一开，默认值就被顶掉，用户取消勾选时没有「默认」可回。
- 通用配置 → 设置窗口 → **自动记录窗口尺寸**（`window.rememberSize`，出厂 true）：勾选则每次
  打开都沿用上次尺寸，不勾则始终用默认尺寸（用户中途拖大过窗口，关掉再开也会回到默认并居中）。
- **`lastWidth` / `lastHeight` 只由 Go 写**：窗口 resize 后防抖 1 秒落盘，隐藏 / 退出时立即 flush。
  设置页拿到的整份配置是打开页面那一刻的快照，`SaveSettings` 会用 `keepWindowLastSize` 把这两个
  键还原成当前值 —— 否则用户拖大窗口后再改别的设置，前端会把旧快照送回来把尺寸顶掉。
- 最小尺寸（760×600）是布局常量、不进配置：宽度小了字段行会横向裁切，高度没有下限（各页都能滚）。
  运行时还会按主屏工作区夹一次（物理像素 ÷ ScaleFactor），小屏笔记本上宁可矮一点让内容滚动，
  也不要开出一扇一开机就超出屏幕的窗口。

## 已知差异（与历史 上一版）

- **z-order 抬升降级**：上一版 用 `moveTop()` 让外卖压到休息窗口之上；Wails v3 没有 z-order
  抬升 API，改用 `SetAlwaysOnTop(true) + Focus()` 近似（同层精确层级无法逐像素复刻）。
- **系统睡眠事件**：上一版 靠 `powerMonitor` 实时通知；Go 侧没有电源事件源，改为**醒来后
  补记**——检测「墙钟跳变 - `GetTickCount64` 跳变」超过 60 秒即判定睡过，把睡眠整段记进离开。
- **便携数据落点**：Go 版统一「配置文件所在目录旁」，与 上一版的 `userDataPath` 约定略有
  不同，但功能不受影响（落点由配置文件位置推导，两边读写各自的份）。
