# 开发与实现说明（WorkerRest）

> 这份文档面向开发者：构建链、实现约定、探针与验证矩阵、各处“为什么这么写”的原因。
> 想先了解这个软件是什么、怎么用，请回到根目录的 [README](../README.md)；
> 用户手册（面向使用者、含界面说明）在 [manual/操作手册.html](manual/操作手册.html)。

本地优先、可深度自定义的休息提醒软件。常驻系统托盘，定时提醒你进行短休息与长休息，
缓解眼疲劳与久坐压力；支持督休专员立绘、外卖提醒、提前提示窗、使用统计等。

用 **Go + Wails v3** 实现，渲染层（HTML/CSS/JS）与历史 上一版一致，但运行时不依赖
上一版（181MB exe + 50MB Chromium），改用系统自带的 **WebView2**。单二进制 + embed 前端
资源，天然绿色便携。

> 本项目原为 上一版“吃饼人也敢休息？”的 Go 重构。迁移完成后 上一版工程已删除，
> 本目录即为独立仓库。

## 环境要求

- **Windows 10 / 11**（依赖系统 WebView2 运行时：Win11 必带，Win10 通常已通过
  Windows Update 自动安装；缺失时首次启动会提示安装 WebView2 Runtime）
- **Go 1.26+**（`go.mod` 的语言版本仍是 1.26.3；本仓库用 **1.27.3** 构建与验证，
  `appInfo()` 里仍回传编译时的真实 Go 版本，供探针与排障使用）
- **Wails v3**：`github.com/wailsapp/wails/v3 v3.0.0`（正式版；v3 自身也要求 Go 1.26+，
  与上一条本来就是同一个约束）。`appInfo().wailsVersion` 读的是构建信息里的真实依赖版本，
  升级后在关于页那一行的行尾就能看出跑的是 beta 还是正式版（探针输出里也有）
- （可选）**Node.js + npm** —— 仅渲染层探针 `probe_renderer.py` 需要（Playwright 驱动 Edge）

## 运行

> 常用命令已收进 `Taskfile.yml`：`task --list` 看全部，`task dev` / `task build` /
> `task test` / `task package` / `task verify` 覆盖日常几件事（需要
> `go install github.com/go-task/task/v3/cmd/task@latest`）。下面保留等价的手敲写法。

```powershell
# 开发运行（GUI，直接启动设置窗口）
go run ./cmd/workerrest

# 构建 GUI 产物（无控制台窗口，去符号，不带构建机路径）
go build -trimpath -o build/bin/rest.exe -ldflags "-H windowsgui -s -w" ./cmd/workerrest

# 无头构建（验证专用：没有原生窗口，但照样服务资源与绑定端点）
go build -trimpath -tags server -o build/bin/rest_server.exe -ldflags "-s -w" ./cmd/workerrest
```

`-trimpath` 不是可选项：不加它，二进制里每个源文件都以 `D:/…/xxx.go` 的绝对路径存在
（panic 栈、运行时符号），等于把构建机的目录结构和用户名一起发出去。加了之后路径统一
变成 `workerrest/cmd/workerrest/xxx.go`。`scripts/build/verify_portable.py` 有一条断言专门扫这个泄漏。

产物 `rest.exe` 约 14 MB（Go 1.27.3 + Wails v3.0.0 下实测 14,670,848 字节），已含
embed 的前端资源与图标；四个内置提示音不再 embed 进二进制，打包时随包放在 exe 旁的
`sounds/` 目录，开发态读仓库里的 `assets/sounds/`。

## 打包

```powershell
# 便携目录打包：构建发布版 exe → 组装 exe + supervisors/ → 打 zip → 校验内容
python scripts/build/make_portable.py

# （可选）隔离环境解包首跑，验证预置三件套与首跑落点（不外溢到 APPDATA）
python scripts/build/verify_portable.py
```

> **Python 必须是 3.9+**。脚本用了类型注解与 f-string，Python 2.7 会在第一个 `def ... -> str`
> 处报 `SyntaxError`（看起来像脚本写坏了，其实是解释器选错）。本机 PATH 上的 `python`
> 可能是别的开发工具自带的 2.7 —— 那种情况下直接双击 `scripts/build/打包.bat`：它按
> “固定 Python 3.11 → `py -3` → `python`”的顺序挑解释器，选完还会校验版本，不对就报错
> 退出，不会跑到一半才莫名其妙失败。命令行里也可以显式指定：
>
> ```powershell
> D:\MyPrograms\Python\Python311\python.exe scripts/build/make_portable.py
> ```

产出（`release/WorkerRest-<tag>/`，tag 形如 `v0.9.1`）：

| 文件 | 用途 |
|---|---|
| `WorkerRest-portable.zip` | 便携目录 zip（含约 2.4 MB 督休专员立绘），解压即用 |
| `WorkerRest-portable/` | 同名散装目录 |
| `rest.zip` | 只含 `rest.exe` 的自动更新包（最大压缩，软件下载的就是它） |
| `version.json` | 自动更新清单（version / date / url / sites / changes / sha256 / size） |
| `清除开机自启动项.bat` | 删目录前清掉注册表里的开机自启动项（便携版没有卸载器） |

同一版本的四种产物都落在 `release/WorkerRest-<tag>/` 里；目录名与压缩包名**不带版本号**，
版本只体现在父目录名与 `version.json` 里。

- **版本号只认 git tag**（`v主.次.修订`，如 `v0.9.0`）：打包要求**当前提交上有 tag**，
  没 tag 直接停止并提示先打 tag（同时显示最近一次 tag）。`app.go` 的 `appVersion`
  只是 `dev` 占位符，真实版本由 `-ldflags "-X main.appVersion=<tag 去掉 v>"` 注入。
- **工作树必须干净**：`git status --porcelain` 有输出就停止（列出最多 12 项）。tag 指的是
  提交，工作树里的未提交改动不在那个 tag 里 —— 那种包能打出来、测试也全绿，但“发布包 =
  源码”的审计链断了。只是排查构建问题时才用 `--allow-dirty`，且该产物不可交付。
- `python scripts/build/version.py` 显示最近一次 tag 的版本号（`--bare` 去掉 `v`）。
- **出厂默认全仓只有一份** `internal/settings/default_settings.json`：它既是 `//go:embed`
  进 exe 的那份（“恢复默认设置”用的就是它），也是便携包 `config/settings.json` 的来源
  （新装首跑读这份）。仓库根不再放副本——以前那两份靠单测和打包脚本对齐，改了 json 忘了
  同步另一份是最容易踩的坑。打包时 `make_portable.py` 先让 exe 用
  `--dump-default-settings <文件>` 把内嵌那份吐出来、与这份源文件逐项比对，不一致直接停止
  （`--no-build` 用上一轮 exe 时最容易踩），再用导出的内容写进便携包；发布后
  `verify_portable.py` 用包里的 exe 再复核一次。单测 `TestEmbeddedDefaultMatchesFile`
  另钉住“编译时快照 == 磁盘文件”。
- **出厂默认 = 作者当时在用的那套配置**（2026-10-29 定）：`internal/settings/default_settings.json` 的每一项
  都按当天的 `config/settings.json` 取值，所以点“恢复默认设置”得到的就是他日常在用的状态。
  三类东西不在此列：**声音**（`sound.music` 与内置音由 `normalizeSound` 种入，文件在 `sounds/`）、
  **督休专员**（`supervisors/` 目录 + 各 `meta.json`，`supervisor` 段仍按出厂 random 口径）、
  **统计**（另存 `stats.json`）。改默认值时容易漏的三处必须同值：出厂 JSON、Go 侧兜底
  （`AppearanceCfg` 的透明度 / 宽高百分比 / 全屏、`FullScreenPause`）、操作手册里的默认值文案。
- **`ResetSettings` 会跑一遍 `normalizeLoad`**：只把出厂 JSON 写下去的话，恢复默认后提醒内容
  面板、指定应用列表、提示音列表都是空的，要重启一次才种入；跑完之后立刻就是出厂那套值
  （`settings.backup.json` 仍先备份）。
- **exe 的 Windows 资源（图标 + VERSIONINFO + MANIFEST）来自仓库根的 `rsrc_windows_amd64.syso`**
  （随源码入库）：`go build` 会自动链接同包目录下的 `*_windows_amd64.syso`，所以
  `make_portable.py` 与 ldflags 都不用改。三类资源由 `goversioninfo` 一个工具生成：
  - **RT_GROUP_ICON / RT_ICON** —— exe 文件图标（16~256 七档，源是全项目唯一那份
    `web/dist/icon.png`）
  - **RT_VERSION(16)** —— 文件/产品版本、公司、描述（资源管理器“属性 → 详细信息”可见）；
    版本号取“当前提交的 tag，无 tag 则最近 tag”（与 ldflags 注入的 `appVersion` 同源）
  - **RT_MANIFEST(24)** —— 应用清单，声明 `longPathAware`（Win10 1607+ 长路径）与
    `supportedOS`（Win7~11）。**不声明 `dpiAware`**：Wails 运行时已
    `SetProcessDpiAwarenessContext(PER_MONITOR_V2)`，清单里再写会冲突
  重新生成（**改了图标 / 改了清单**之后；打新 tag **不必**重新生成，见下）：
  ```bash
  python scripts/build/gen_windows_res.py          # 重新生成 syso（同时把 versioninfo.json / app.manifest 落到 build/windows/）
  python scripts/build/gen_windows_res.py --check  # 核对当前 syso 的图标/清单内容是否与源码一致（打包门禁里会跑）
  ```
  校验方式：`python scripts/build/verify_windows_res.py <exe>` 解析 PE 断言三类资源齐全；
  `make_portable.py` 打包前会自动跑 `check_winres`（资源齐全 + exe 的 VERSIONINFO 版本号
  == 本次 tag + syso 图标/清单内容未漂移三重门禁）。

  **版本号怎么跟着 tag 走而不产生死锁**：仓库里的 syso 版本号是“最近一次生成时的 tag”，
  打新 tag 后它自然是旧的。`make_portable.py` 在 `go build` 前用本次 tag 版本在**临时目录**
  现算一份正确版本的 syso（`gen_windows_res.py --version <tag> --out <临时路径>`）临时替换
  仓库根供链接，构建完恢复 —— 所以不需要“重生成 syso → commit → 挪 tag”的手工四步，
  也不污染工作树。仓库里的 syso 只在“图标或清单内容变了”时才需要真正重新生成并提交。
  **托盘与标题栏不需要另做图**：托盘直接吃这份 PNG（Wails v3 的
  `w32.CreateSmallHIconFromImage` PNG/ICO 都认，系统再按 `SM_CXSMICON` 缩放），标题栏是
  同一张图按 20px 显示。
- **默认检查更新地址 = `https://www.goodgoodlive.com/worker-rest/version.json`**，同时是
  **出厂兜底源**：写在 `default_settings.json` 的 `sites` 里（便携包的 `config/settings.json`
  是它的副本），`update.go` 里还有一份 `defaultUpdateSite` 常量，永远排在候选链**最后**。
  因此配置被改坏、或被远程换成别的源，软件都还留有一条官方更新通道；配置里一条源都没有时
  也照样能查。
- `version.json` 的两个地址字段口径不同：`url` 是**下载**地址数组（按顺序容错，第一项失败
  取下一项），`sites` 是**检查更新**地址数组（远程下发会整体覆盖客户端本地 `sites`）。
  **其他地址（含 `nav.hcctc.cn`）都是可选项**：发布清单里给了就用、没给就不用，程序不针对
  任何具体域名做特殊处理。`scripts/build/make_portable.py` 里的 `UPDATE_SITES` 必须与
  `default_settings.json` 的 `sites` 逐字一致——打包前会校验，不一致直接停止；发布后
  `verify_portable.py` 还会核对便携包的 `sites` 与同版本 `version.json` 的 `sites` 相同。
- `version.json` 的 `changes` 取“上一个 tag → 当前 tag”的提交标题，按时间顺序自动编号；
  首个 tag 没有上一个 tag，`changes` 为空。这是机械摘要，脚本跑完最后会提示**手动改写
  `release/WorkerRest-<tag>/version.json` 的 `changes`**，改成面向用户的发布说明。
- **更新包的完整性只认 `sha256`**（`version.json` 的 `sha256` / `size` 由
  `make_portable.py` 从刚生成的 `rest.zip` 现算，不是手填；改过 `rest.zip` 就必须重算）。
  客户端的规矩（实现见 `internal/update/trust.go`）：
  - 只接受 **https** 源，`http://` 明文源一律丢弃（唯一例外是回环地址，仅为单测的
    httptest 保留）；**重定向的每一跳也重新校验**，所以 https 302 到 http 会被拦下；
  - 清单缺 `sha256`、格式不对、或 `url` 全是 http → `checkForUpdate` 直接返回 error
    并说明原因，**不会**降级成“只靠 TLS”；
  - 下载后逐字节比对哈希，不符就删掉换下一个镜像；全部失败则拒绝更新；
  - 体积上限：清单 1 MiB、更新包 64 MiB、解压总量 192 MiB、条目数 2048
    （防 zip bomb），超限即中断。
  打包脚本在 `rest.zip` 缺失或未生成时会直接失败——没有哈希的清单等于没有更新通道。
- 解压即用，无需安装。**不做 NSIS 安装器**——单二进制天然绿色，便携目录 zip 即交付形态。
- 便携运行的数据（配置 / 统计 / 用户音频）落在 exe 旁的 `config/` 与 `data/`，跟着程序走；
  直接删掉整个目录就是完全卸载。唯一的目录外残留是注册表里的开机自启动项（只有勾过才有），
  删目录前先跑包里的 `清除开机自启动项.bat`：它只删 `HKCU\…\Run` 与
  `HKCU\…\Explorer\StartupApproved\Run` 里本软件那一项，不删文件、不碰别的软件。
  这个脚本**刻意不叫“卸载.bat”**——它不删任何文件，叫卸载会让用户以为删完目录就干净了。
  脚本里的值名与 `internal/autostart/autostart_windows.go` 的 `runValueName` 是两份真源，`make_portable.py`
  打包前会解析源码逐字比对（连键路径一起），不一致直接停；`verify_zip` 断言包里有它。

## 检查更新：自动 vs 手动

两条路径共用同一套联网逻辑（`checkForUpdate`）与同一套落库（`publishUpdateStatus`：
写内存 → 推 `update:status-changed` → 持久化 `lastUpdateResult`）。差别只在**什么时候查**
和**查到之后怎么告诉用户**。

| 维度 | 自动检查 | 手动检查 |
|---|---|---|
| 入口 | 启动时 `startAutoUpdateCheck` → 常驻 `autoUpdateLoop`（仅在勾选“自动检查更新”时运行） | 关于页“检查更新”按钮 → 绑定 `CheckUpdate()` |
| 时机 | 当天**首次键鼠活动**后 0~3 小时内随机一刻；到点若处于“暂停休息”，再在 0~30 分钟内随机一刻，反复直到不再暂停 | 点击立即联网，最长等 `updateTimeout` × 更新源个数 |
| 频率 | 一天一次：`lastUpdateCheck == 今天` 就不再查；`error` 不记当天，当天稍后还会再排；跨天整体重置，当天没查成不影响次日 | 不限次；**不写** `lastUpdateCheck`，不占自动检查当天的名额 |
| 界面反馈 | 静默，只改关于页状态条与“立即更新”按钮 | 立刻进加载态（按钮“检查中…”置灰 + 状态条“正在检查更新…”），`finally` 复原 |
| 发现新版本 | **有条件弹窗**：① 勾选了“自动检查更新”（初始渲染传 `autoModal = S.checkUpdateOnStart`）；② 该版本没被忽略（`st.ignored` 为假）；③ 本次会话没为同一版本弹过（`updateModalShownFor`） | **一律弹窗**（`renderUpdate(st, true, true)` 的 `force`）：忽略过、已弹过都照弹 |
| 已是最新 | 无提示，状态条显示“已是最新版本” | 状态条 + toast“已是最新版本” |
| 检查失败 | 无提示（状态条“更新检查失败”），当天稍后按规则重试 | 状态条 + toast“检查更新失败（原因）” |

其他约定：

- “忽略本版本”（`ignoreVersion`）**只压制自动弹窗**：按钮仍显示“立即更新”；当发现比被忽略
  版本更大的版本号时会自动解除忽略、恢复自动弹窗；用户手动点检查则始终能看到弹窗。
- 检查结果会持久化（`lastUpdateResult`），重启后打开关于页仍显示“发现新版本 x.y.z”；
  但只有勾了自动检查时才会在打开设置页那一刻自动弹窗，否则只显示状态条。
- 手动检查期间前端会丢弃 `update:status-changed` 推送（`if (checking) return`），以免后台
  推来的旧结果把加载态盖掉；结果以 `CheckUpdate()` 的返回值为准。
- “自动检查更新”开关只在启动时读一次：中途关掉会停掉排期，重新打开需重启软件。
- 无头模式（`-tags server`）不联网：`CheckUpdate` 返回确定性的 `error`，自动检查直接跳过。

回归覆盖见“测试与验证”里的渲染层探针：`metrics.updateLoading`（点击即进入加载态）、
`metrics.updateModalForce`（同版本 + `ignored: true` 连点两次都必须弹窗）。

## 测试与验证

```powershell
go test -tags server ./...         # 单元测试（365 个测试函数）：契约 / shim 一致性 / 归一化 / 状态机 /
                                   # 系统能力 / 提示音 / 统计 / 督休专员迁移与命名唯一 / 外卖 / 更新 / 睡眠检测
python scripts/smoke_server.py     # 绑定层无头冒烟：181 项断言
python scripts/probe/probe_renderer.py   # 渲染层探针：479 项断言 + 截图（需先在 scripts/ 下 npm install）
python scripts/probe/probe_settings_ui.py # 设置页三层探针：171 项（六 tab 全控件 / 十个对话框交互 / 写入链路）
python scripts/probe/probe_bindings_full.py  # 绑定全集：134 项（契约里每个绑定都真调一次）
python scripts/probe/probe_config_matrix.py  # 配置矩阵：69 项（多值配置 + 启动态矩阵）
python scripts/probe/probe_edge_cases.py     # 异常边界：33 项（缺资源 / 只读 / 并发 / 超长 / 落点异常）
node   scripts/a11y_audit.js         # 可访问性静态审计：无名控件 / 活区 / 对话框语义，写 build/a11y_audit.json
node   scripts/probe_a11y_focus.js # 对话框焦点管理行为探针：101 项（进得去 / Tab 跑不出 / 关后回得来）
python scripts/probe/probe_gui_boot.py   # GUI 启动探针：17 项断言（系统能力 / 提前提示窗 / 提示音宿主）
python scripts/probe/probe_crossday.py   # 跨天探针：11 项（报表默认范围必须现算，不能沿用昨天的）
python scripts/probe/probe_stats_file.py # 统计文件自愈探针：19 项（首跑自建 data/ / 损坏改名备份 / 弹窗说明 / Ack 清空）
python scripts/probe/probe_sound_scan.py # 提示音目录体检探针：27 项（新音频原地登记 / 死条目删除 / 重启不重复提示）
python scripts/verify/verify_about.py     # 关于页字段与配色核对（浅色 / 深色各一张截图）
python scripts/verify/verify_window_size.py  # 设置窗口尺寸：14 项（勾选框 / 默认尺寸下恰好不滚 / 宽度边界 / 落盘）
python scripts/probe/probe_window_size.py   # 尺寸**测量**（不是断言）：逐 10px 扫宽度、逐 2px 扫高度，改版式后重测用
python scripts/probe/probe_window_gui.py    # 真窗口尺寸：9 项（首次按出厂尺寸 / 拖拽记录 / 重启沿用 / 关掉开关回默认）
# —— 以下要真实桌面，会短暂弹出真窗口（休息窗 / 外卖窗 / 暂停小窗），跑之前先存好手头工作 ——
python scripts/probe/probe_windows_e2e.py    # 休息 / 外卖 / 热键三段真窗口；自动构建 rest_winprobe.exe
python scripts/probe/probe_tray_e2e.py       # 托盘菜单与单击行为；同上
python scripts/probe/probe_pause_autopause.py # 应用占用自动暂停；自动构建 rest_probe.exe
python scripts/probe/probe_capture_guard.py  # 共享屏幕时隐藏（逐类窗口抓屏）；自动构建 rest_capguard.exe
python scripts/probe/probe_display_target.py # 多屏落点与 Wails 对齐；自动构建 rest_disp.exe
```

> 上面这些脚本不要求按顺序跑，但改过 `web/dist` 后必须重建 `build/bin/rest_server.exe`
> 再跑：前端是 `//go:embed` 编进 exe 的，用旧 exe 跑等于在测旧前端。

- **单测必须带 `-tags server`**：不带时“提前提示窗”用例会因 `a.app == nil` panic（无头形态
  没有原生窗口），这是既有约束，不是回归。
- `tray_icon_test.go` 钉住图片资产这一层：托盘图标（`web/dist/icon.png`，唯一那份图标）
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
  任何非测试 `*.go` 或任一 `//go:embed` 资源（`web/dist/**` 等）就会被拒（rc=1），
  信息里直接列出比 exe 新的文件。这是 `build/bin` 里放旧 exe 也不至于发错包的兜底。

## 目录结构

```
  cmd/workerrest/          main 包（后端源码 + 前端资源 + Windows 资源）：
    *.go                  后端源码（App 窗口/会话 + 调度器/计划 + 统计 + 系统能力 + 各窗口）
    main.go               入口：注册服务 + 主窗口（= 设置窗口）
    contract.go           前端绑定“契约表”：wiredCalls 列表 + 启动自检
    web/dist/             渲染层产物（已固化；shim.js 把 window.api 适配到 Go 绑定）+ 全部图片资产：
                          icon.png **全项目唯一图标**（标题栏 20px / 托盘 / exe 图标源三处共用）、
                          sitting_risk.webp 关于页横幅 —— 整个目录 go:embed 进 exe，运行时只认这里
    rsrc_windows_amd64.syso exe 的 Windows 资源（RT_GROUP_ICON/RT_ICON + RT_VERSION + RT_MANIFEST，
                          由 scripts/build/gen_windows_res.py 用 goversioninfo 生成，随源码入库）
    THIRD-PARTY-NOTICES.txt 开源鸣谢清单（gen_notices.py 生成，go:embed 进 exe，不随包散放）
    LICENSE              内嵌用的许可正文副本（仓库根 LICENSE 的逐字节副本，go:embed 进 exe；
                         必须与仓库根一致，license_test.go 与 make_portable.check_license 双向钉住）
  build/                  构建资源与产物：
    windows/              Wails 约定的平台资源目录：VERSIONINFO 源配置 + 应用清单
                          （gen_windows_res.py 生成留档，与 cmd/workerrest/*.syso 同源）
    bin/                  构建产物（全部不入库，都是 `go build` 直接输出）：
      rest.exe            正式产物，发布包与自动更新替换的就是它（task build）
      rest_server.exe     `-tags server` 无头产物，探针与冒烟测试用，没有原生窗口
      rest_winprobe.exe   真窗口探针宿主（probe_windows_e2e / probe_tray_e2e / probe_window_gui）
      rest_probe.exe      GUI 启动与自动暂停探针（probe_gui_boot / probe_pause_autopause）
      rest_capguard.exe   共享屏幕隐藏探针（probe_capture_guard）
      rest_disp.exe       多屏落点探针（probe_display_target）
                          后面四个是同一份 GUI 代码的副本，只靠文件名区分进程与日志，
                          由各自探针临时构建，**不是发布物**，打包脚本也不会碰它们。
  internal/               领域逻辑 + 平台能力（全部零 App 依赖，可独立测试）：
    model/                共享数据结构（零依赖，全仓最底层）
    settings/             配置域：整份 map + 深合并 + 归一 + 落盘
      default_settings.json  出厂默认配置（**全仓唯一源**：既是 //go:embed 的数据源，
                          也是便携包 config/settings.json 的来源，没有仓库根副本）
    plan/                 休息计划纯函数（下一次休息 / 固定时刻 / 今日时间线）
    scheduler/            调度状态机（tick / 暂停 / 跳过 / 推迟 / 提前提示）
    stats/                使用统计与报表（连续记录、离开判定）
    role/                 督休专员资产（扫描 / 命名 / 导入 / 预览图）
    sound/                提示音选曲与文件命名（不含播放）
    update/               版本比较 + 清单信任校验 + 自动检查节奏
    urltls/               更新链路 HTTPS 校验（重定向逐跳）
    jsonx/ logx/          JSON 小工具；日志落点与后台 goroutine panic 兜底
    platform/ autostart/ hotkey/ idle/ notifier/ windnd/ winpower/ winproc/
                          Windows 平台能力，每个包都是 _windows.go + _other.go 桩
  assets/                 随包静态资源（不进 exe；打包时复制到 exe 旁，开发态直接从这儿读）：
    supervisors/          内置督休专员模板（15 个专员，立绘与缩略图均为 webp，约 2.4 MB，随包分发；子目录用专员名称命名，meta.json 里只有名字（名字即身份，没有内部 id）；无 meta.json 的目录只作素材、不进包）
    sounds/               内置提示音源文件（四个 wav，打包时预置进便携包）
  scripts/                构建 / 探针 / 验证脚本（逐项索引见 scripts/README.md）
  LICENSE                 项目 MIT 许可正文（go:embed 的数据源；关于页“许可条款”可读可导出）
  Taskfile.yml            常用命令入口（task build / test / package / verify / a11y）
  docs/                   设计 / 审计 / 测试报告；docs/promo/ 是运营素材（不入库、不随包），
                          docs/images/ 是 README 用的产品截图
  config/ data/           开发态运行时落点（首次运行自动生成，已 gitignore）：
                          配置文件与统计。便携包里它们就是 exe 旁的用户数据目录 ——
                          “数据与程序同目录”是便携语义的一部分，所以不进源码库也不进安装包
```

`cmd/workerrest/` 只剩“必须跟 main 包绑在一起”的东西（Wails 绑定方法要能被反射成
`main.App.xxx`）：`app*.go`（生命周期与会话）、`bindings_*.go`（设置页 / 专员 / 提示音 /
统计的绑定）、`prebreak.go` / `pause_notice.go`、`takeout*.go`（自绘提醒窗）、`window_size.go`（尺寸记忆）、
`update*.go`（下载与自替换）、`sys_*.go`（空闲 / 勿扰 / 自启动的 tick 与注册表写入）。
能脱离界面推理的逻辑都在 `internal/`：计划 → `plan`、状态机 → `scheduler`、配置 →
`settings`、统计 → `stats`、专员资产 → `role`，改这些不必懂 Wails。

**为什么前端产物是 `cmd/workerrest/web/dist` 而不是根目录的 `frontend/`**：Wails v3 模板把
`frontend/` 放在工程根，是因为它的 `main.go` 也在根、`//go:embed frontend/dist` 够得着。
本项目按 Go 的标准布局把 main 包放进了 `cmd/workerrest/`，而 **`//go:embed` 只能嵌包目录
以内的文件**，够不到根目录的 `frontend/`。所以产物跟着 main 包走，命名用 `web/`（这里没有
前端源码工程，只有固化的 dist，叫 frontend 反而误导）。`build/` 的用法则与 Wails 一致：
`build/<os>/` 放平台资源，产物在 `build/bin/`。

## 源码约定：换行、编码、缩进

三平台（Windows / Linux / macOS）clone 下来必须是**同一串字节**，由两处配置共同钉住：

| 文件 | 作用 |
|---|---|
| `.gitattributes` | `* text=auto eol=lf` —— 不管本机 `core.autocrlf` 是什么，文本文件 checkout 出来一律 LF |
| `.editorconfig` | 编辑器新建 / 保存时默认 LF + 末尾换行 + UTF-8，新加入的协作者不用配任何东西 |

**三条硬规则：**

1. **文本一律 LF**（`.go` / `.py` / `.js` / `.css` / `.html` / `.md` / `.json` / `.yml`）。`eol=lf` 覆盖 `core.autocrlf`，所以 Windows 用户即便 `core.autocrlf=true` 也不会拿到 CRLF。
2. **批处理是唯一例外**：`scripts/build/*.bat` 是 **GBK + CRLF**（cmd.exe 按 OEM 代码页 936 读文件，换行或编码一变就满屏乱码），`.gitattributes` 用 `-text` 把它钉成逐字节复制，三平台拿到的完全一样。
3. **运行时导出的文件仍然用 CRLF**：许可条款（`licensePayload`）、开源鸣谢（`toCRLF`）、统计 CSV（`lines.join("\r\n")` + BOM）—— 这些是**给 Windows 用户和 Excel 看的产物**，与仓库源码的行尾是两件事。把这里的 `\r\n` 改成 `\n` 会真的坏功能：老记事本会把 LF 显示成一行，Excel 也会把整张表并成一行。

> 自查：`git ls-files --eol`。健康的仓库只应出现 `i/lf w/lf`（文本）、`i/-text w/-text`（二进制）、`i/crlf w/crlf`（仅 `.bat`）。出现 `w/crlf` 的文本文件说明某台机器 checkout 后又被工具改回去了，重新 checkout 一次即可。

## 数据与配置落点

- 一律“数据与程序同目录”：配置、统计、用户音频统一落在 exe 旁的 `config/` 与 `data/` 下，
  整目录拷贝 / 删除就是完整的迁移 / 卸载。不再检查 `%APPDATA%`（老版本的历史目录已放弃兼容）。
- **`sounds/` 在启动时体检一次**（见 `sound_scan.go`）：用户直接把音频丢进 exe 旁的 `sounds/`，
  或手动删掉某个已登记条目的文件时，设置页会弹窗逐项询问 ——“发现 N 个新提示音”可一键
  **原地登记**（不复制、不改名，否则原文件下次启动又会被当成新的，变成每次开机都问一遍）；
  “N 个提示音的文件已不在”可一键**从配置删除**（只删条目，不动任何文件）。启动时只记录、
  不擅自改配置；点“暂不处理”本次运行不再问，下次启动重新扫。
- 打包交付的便携目录**自带**两件套：`sounds/`（四个内置提示音）、`config/settings.json`
  （出厂默认，首跑补种预置条目）——首跑直接在预置文件上继续写。
- **`data/` 与 `stats.json` 不进安装包**（统计是用户数据，不属于发行物）：
  - 首跑：`data/` 目录与空的 `data/stats.json` 由程序自动创建（见 `stats.go` 的 `Load`）；
  - 损坏：文件**读不出来**时不动它、只从空表开始用；**格式错误（解析失败 / 不是 JSON 对象）**
    时先把原文件改名成 `data/stats_<日期_时间>.json` 留作备份，再重建空表，并在设置页弹窗
    说明“备份在哪、为什么判定损坏”——历史统计不会无声消失；
  - 更新：只替换 `rest.exe`（旧的改名成 `.bak`），解压与合并配置都在 `update/` 与 `config/`
    里进行，**不碰** `data/`。
- **督休专员的出场顺序可拖动调整**：设置页“督休专员”→ 候选专员列表，按住立绘磁贴拖动即可
  改顺序，松手即生效并自动保存。顺序存于 `config/settings.json` 的 `supervisor.order`
  （专员名数组），重启后仍在；画廊显示顺序与“顺序轮换”模式的出场次序**共用这一份**，
  两边永远一致。出厂默认 `order` 为空数组 = 按内置清单顺序，补装/新增的专员自动排到末尾。
- **督休专员的显示名唯一**：导入立绘时会弹命名框；提交的名字若与现有专员重名，
  **拒绝保存**并把错误留在框里、要求重新输入（对话框不关、已输入的字保留）。
  Go 侧 `SaveRole` 也会再兜一层底（重名直接返回 false、磁盘零副作用），
  软件自己起名的场景（导入时用文件名）则自动退让成“名字 (2)”。
  判据是**目录名口径**而不是字符串相等：`A/B` 与 `AB`、`Alice` 与 `alice`、
  `abc.` 与 `abc` 都算重名（它们会落到同一个目录上）。**同一张图导入两次**也走这条 ——
  第二个自动叫“我的图 (2)”，不会出现两个同名专员抢一个目录。

## 开源鸣谢（第三方许可）

随包声明的第三方组件、版本与许可全文在仓库根 `THIRD-PARTY-NOTICES.txt`。文件内部只有三段，
**不含任何说明性文字、也不写文件级标题**（弹窗标题已经叫“开源鸣谢”，正文再顶一遍是重复）：
致谢 → 组件清单（名称 / 版本 / 协议，列对齐）→ 逐段许可全文（78 字符横线分隔）。
它由生成器从**构建事实**推导（不是手抄清单）：

```powershell
python scripts/build/gen_notices.py           # 重新生成（依赖或 Go 版本变了才需要）
python scripts/build/gen_notices.py --check   # 只校验：与当前构建不一致则退出码 1
```

- 收录口径 = **实际参与构建**的组件：Go 模块取自 `go list -deps`（与 `go version -m`
  读出的内嵌依赖一致）、Go 标准库与运行时、以及内联进 `web/dist` 产物的库
  （`qrcode` / `dijkstrajs`）。未被链接的 `go.sum` 条目不在其列。
- 只内嵌、不散放：`//go:embed` 进 exe（`notices.go`），便携目录里**没有**这份 txt。
  理由是分发形态 —— 更新包 `rest.zip` 只换 exe，目录里那份从首装那天起就不再更新，
  留着等于发一份“一致地过期”的快照。只把声明编译进机器码、却没有读取入口，等于没有复现，
  所以设置页“关于 → 软件信息 → 开源鸣谢”可读全文，并可“另存为…”导出一份
  （内容与内嵌文本相同，导出时转 CRLF）—— 那是用户取到文件的唯一途径。
- **依赖变了必须重新生成**：`make_portable.py` 在 `go build` **之前**跑 `--check`，过期直接
  停止 —— 清单是编进 exe 的，编进去之后所有校验都看不出“一致地过期”。单测
  `TestNoticesCoversLinkedModules` 用 `go list -deps` 两个方向核对（不缺、也不多声明）。
- 升级前端内联库时，把 npm 包里的许可原文放回 `scripts/build/notices/`，并同步 `gen_notices.py`
  的 `FRONTEND`（含产物指纹串）：指纹在 bundle 里找不到会直接报错，防止清单悄悄过期。
- 这份文件只声明**第三方**组件；本项目自己的许可见下一节。

## 许可证

本项目以 **MIT** 发布，正文见仓库根 [`LICENSE`](LICENSE)。它可以自由使用、修改、再分发
（含商业用途），需要保留版权声明与许可全文；软件按“现状”提供，不含任何担保。

- **正文内嵌，随二进制走**：`LICENSE` 被 `//go:embed` 编进 exe，设置页“关于 → 软件信息 →
  许可条款”可读全文，并可“另存为…”。便携目录里不再散放这份文件 —— 更新包 `rest.zip`
  只替换 `rest.exe`，散放的那份会停在首装当天的版本上；内嵌的这份跟着版本走。
  一致性由三处钉住：`license_test.go` 断言内嵌字节 == 仓库根 `LICENSE`，
  `make_portable.check_license()` 在构建前比对同一对文件，`verify_zip()` 反向断言
  包里**没有**这两个文本（谁把拷贝逻辑加回来都会立刻红）。
- **第三方声明另有一份**：`THIRD-PARTY-NOTICES.txt`（见上一节）声明的是别人给我的授权，
  `LICENSE` 声明的是我给别人的授权，两者不是可以互相替代的东西。
- **随包素材**：`assets/` 下的督休专员立绘、提示音、图标与手册同属本项目产出，随 MIT 一并授权。
  如果以后引入外部素材，务必在那份素材旁留下来源与授权记录 —— 第三方素材不会因为放在这个
  仓库里就自动变成 MIT。
- **WebView2 不随包**：软件运行时用的是系统里的 WebView2 运行时（微软的组件，受微软自己的
  许可约束），本项目只检测不打包，因此不影响本项目的 MIT 授权。
- **自动更新包同样不含正文**：`rest.zip` 只替换 `rest.exe` —— 而正文本来就在 exe 里，
  所以“更新之后条款还在”这件事不需要额外的拷贝逻辑来保证。

## 后台 goroutine 的 panic 兜底

Wails v3 只保护**绑定方法**（`pkg/application/bindings.go` 把 panic 转成 `CallError`，
前端 promise reject、进程存活）。但调度 tick、空闲/勿扰/应用占用监视、托盘刷新、外卖检查、
统计落盘、自动更新排期这些循环都跑在本项目自建的 goroutine 里，不在框架保护范围内 ——
一处 nil deref 就会让进程直接终止，而发布版是 GUI 子系统，用户看到的是“窗口和托盘图标
一起消失，日志里什么都没有”。

`recoverguard.go` 给这些循环加了统一兜底：

| 层次 | 行为 |
|---|---|
| `guardRun(name, fn)` | 包住**每一轮**（不是循环外层）。panic 只丢这一拍，记日志后继续 |
| 日志限流 | 每个位置首次打全栈，之后 5 分钟内不再重复（循环里每 500 ms 炸一次时，全栈会把 2 MiB×3 的滚动日志瞬间填满，把第一次的现场冲掉） |
| 熔断 | 同一位置 2 秒内连炸 3 次 → 判定不可恢复（典型是 panic 落在 Lock 与 Unlock 之间、互斥量没释放，后续 tick 全卡死），落盘 + 弹 `MessageBoxW` 指出日志路径 + 以退出码 1 退出 |
| `main()` 顶层 recover | 兜住主 goroutine，同样落盘 + 提示 |

- **约定：临界区里不放会 panic 的业务调用。** `recover` 无法代你解锁（`sync.Mutex` 没有
  “外部释放”），panic 落在锁段中间时这一层救不回来，只能靠熔断收场。锁段保持“只读字段 /
  只赋值”的短小形状，是这套机制成立的前提。
- `guardRunOK` 额外返回“这一轮是否跑完”：自动更新检查 panic 后不能立刻重试
  （轮询粒度 2 秒，一秒内连炸三次正好撞熔断阈值），拿到 false 改为退避一分钟。
- `TestBackgroundGoroutinesAreGuarded` 扫全仓：任何一个起了后台 goroutine 的文件里没有
  `guardRun` 就红 —— 新增循环忘了包兜底，测试直接拦住。

## 设置窗口尺寸

出厂尺寸写死在 `default_settings.json` 的 `window` 块里（`window_size.go` 顶部有完整推导）：

```json
"window": { "width": 874, "height": 857, "rememberSize": true, "lastWidth": 0, "lastHeight": 0 }
```

- **874 是用户选定的宽度**（2026-10-29 他把窗口拖到这个尺寸后要求“记住现在的尺寸，设为默认”）。
- **高度不是随手填的**：`scripts/probe/probe_window_size.py` 起无头服务后逐 2px 扫高度，取宽度 ≥820 时
  “关于页恰好不出垂直滚动条”的那一档。2026-10-31 关于页先因“第三方许可”一行抬到 893，
  同一天又把“发布时间”并进“版本”行、删掉独立的“运行环境”行（−36px 行高），边界回落到 **857**
  （`857 − 44（38 标题栏 + 6 底部透明边框）= 813 = 内容区高度`；855 就滚 2px）。
  2026-11-02 又把“运行环境”以弱字压回“许可条款 / 开源鸣谢”那一行的行尾 —— 不再单独占行高，
  行高仍是 36px、边界仍是 857（实测 820 / 874 两档 needContentH 都是 819，与压回之前逐字相同）。
  **关于页内容或底部 padding 一变，就必须重跑该脚本重取高度**，否则 `verify_window_size.py` 的
  “出厂尺寸下关于页恰好不滚”会红。宽度一旦回落到 760，关于页需要更高的窗口 —— 那属于
  “窗口被用户缩窄，允许出滚动条”，不影响出厂值。
- **最小宽度 760 也是实测的**：同一脚本逐 10px 扫宽度，750 起才没有横向裁切（740 时 `.row.field`
  溢出 2px），取 760 留 10px 余量。两个边界与滚动条是否出现无关（620 / 940 两个高度下结论一致）。
- `width` / `height` 是**默认尺寸**，`lastWidth` / `lastHeight` 是**上次用过的尺寸**，两对数各管
  一头 —— 共用一对的话“记住尺寸”一开，默认值就被顶掉，用户取消勾选时没有“默认”可回。
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
  补记**——检测“墙钟跳变 - `GetTickCount64` 跳变”超过 60 秒即判定睡过，把睡眠整段记进离开。
- **便携数据落点**：Go 版统一“配置文件所在目录旁”，与 上一版的 `userDataPath` 约定略有
  不同，但功能不受影响（落点由配置文件位置推导，两边读写各自的份）。
