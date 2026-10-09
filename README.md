# 打工人休息提醒（WorkerRest）

本地优先、可深度自定义的休息提醒软件，用 **Go + Wails v3** 实现，渲染层
（HTML/CSS/JS）与历史 Electron 版一致。运行时不依赖 Electron（181MB exe + 50MB
Chromium），改用系统自带的 WebView2。

> 本项目原为 Electron 版「吃饼人也敢休息？」的 Go 重构（曾作为 `rewrite-go/` 子目录
> 存在）。迁移完成后 Electron 工程已删除，本目录即为独立仓库。

## 体积对比（实测，2026-10-08）

| 交付物    | Electron 版 | Go 版         |
| ------ | ---------- | ------------ |
| 未压缩产物  | 257 MB     | **12.34 MB** |
| 便携 zip | 124.4 MB   | **26 MB**（含 roles/ 22MB）  |

未压缩瘦身 **95.2%**。剩余体积 = Go 二进制本身（含 embed 的前端资源与图标，
`-H windowsgui -s -w`）加上随包分发的 `roles/` 角色目录（22MB 立绘）。Go 版**不做 NSIS
安装器**——单二进制天然绿色，便携目录 zip 即交付形态。

## 为什么是 Wails v3（而不是 v2）

**Wails v2 只有单窗口**——v3 才支持多窗口。本项目的核心交互恰恰是多窗口：每屏一个

无边框透明提醒窗口 + 外卖窗口 + 提前提示窗口 + 设置窗口。v3 目前是 `v3.0.0-beta.28`，

API 仍在演进；v2 的内置能力（无托盘、无多窗口）反而不满足需求，第三方 systray 也

只解决托盘。

## 目录结构

```
  main.go               应用入口：application.New + 服务注册 + 主窗口（= 设置窗口）
  app.go                App 后端：窗口管理 / 会话 / 路径 / 资源定位 / 督导会话
  types.go              与渲染层逐字对齐的数据结构（BreakInfo / SupStageInfo / ...）
  contract.go           绑定的「契约表」：已接通 / 待接通 + 启动自检
  settings.go           配置读取 + **三条归一化路径**（加载 / 保存 / 导入，逐条对应 config.ts）
  default_settings.json 出厂默认（由 types.ts 的 defaultSettings() 生成，见 scripts/）
  messages.go           内置提醒内容（与 electron/src/messages.ts 同源）
  role.go               休息督导读取：角色目录扫描 / 归一化 / 立绘 data URL / 台词挑选
  role_write.go         休息督导写入：role.json 增删改 / 立绘几何夹紧
  migrate_roles.go      旧版「角色数据存 settings.json」→ 角色目录的一次性迁移
                        （对应 supervisor.ts 的 migrateLegacyRoles）
  bindings_settings.go  设置页那批绑定（配置读写 / 角色增删改 / 文件选择 / 导入导出）
  filedialog.go         原生文件对话框封装（go-common-file-dialog，COM 无 CGO）：打开/多选/另存为
  update.go             检查更新（GitHub Releases + 三段版本比较，net/http 零依赖）
  takeout.go            外卖提醒窗口：会话构建 / 触发与推迟记账 / 每秒 watcher / 多屏建窗 /
                        抬升簿记 / 键鼠活动轮询 / TakeoutCurrent·PreviewTakeout·TakeoutAction
                        （对应 windows/takeoutWindow.ts + raise.ts + displays.ts + reminderSync.ts）
  breakplan.go          计划层纯函数：触发线 / 下一次休息 / 固定时刻表 / 今日时间线
                        （scheduler 与设置页状态卡的共同底稿，对应 breakPlan.ts + fixedTime.ts）
  scheduler.go          调度器状态机：倒计时与固定时刻共存 / 消费记账 / 推迟跳过 /
                        暂停恢复 / 离开后重锚（逐条对应 scheduler.ts 的 BreakScheduler）
  sys_idle.go           空闲状态机（连续无输入时长 L）+ user32!GetLastInputInfo
                        （对应 idleState.ts + systemInput.ts；纯函数判定 decideIdleStep）
  sys_dnd.go            系统勿扰探测（shell32!SHQueryUserNotificationState，对应 dnd.ts）
  sys_proc.go           进程枚举（Toolhelp32，对应 appProcess.ts）
  sys_autostart.go      开机自启动注册表读写（HKCU Run + StartupApproved，对应 autostart.ts）
  sys_watch.go          三条监视链路（空闲 / 勿扰 / 应用占用）+ SysProbe 自检绑定
  stats.go              使用统计：三桶记账（工作/休息/离开）+ 跨零点切段 + 周期聚合 + 防抖落盘
                        （对应 electron/src/stats.ts）
  prebreak.go           休息前提示窗：内容算一次就记住 / 版式随 URL 下发 / 四个动作汇聚调度器
  sound.go              提示音：内置音走资源服务 / 用户音乐走只读路由（白名单）/
                        选曲（单曲·随机·顺序）+ 隐藏宿主窗口播放（对应 sound.ts）
  scheduler_test.go     状态机单测（假宿主 schedHost，不依赖 Wails）
  sound_test.go         提示音单测：选曲规则 / 静音语义 / 路由白名单 / 导入副本的删除边界
  prebreak_test.go      提前提示窗单测：版式内容 / 窗口几何 / 状态与四动作分发
  sys_test.go           系统能力单测：空闲状态机 / 判定 / 命令行 / 注册表往返（临时键）/ 真机探测
  migrate_roles_test.go 角色目录迁移单测：全新安装短路 / 内置覆盖 / 删目录 / 自定义搬图 / fixedKey 重映射
  stats_test.go         使用统计单测：切段 / 周期区间 / ISO 周号 / 记账口径 / 三重封顶 / 睡眠剪裁
  stats_parity_test.go  统计对拍入口（读 WR_STATS_REF 复跑、写 WR_STATS_DUMP；时区不符则跳过）
  takeout_test.go       外卖单测：会话构建字段 / 触发与推迟 / 过期作废 / 暂停跳过 / 三动作记账
  plan_parity_test.go   计划层对拍入口（读 WR_PLAN_REF 复跑、写 WR_PLAN_DUMP；时区不符则跳过）
  tray.go               系统托盘（v3 内置 SystemTray；菜单与文案对齐 tray.ts）
  logging.go            可选调试日志（WR_LOG_LEVEL=debug，用来打印真实注册的绑定 FQN）
  mode_gui.go           serverMode = false（普通构建）
  mode_server.go        serverMode = true（-tags server 无头构建）
  settings_test.go      归一化的纯函数回归（毫秒级，改规则先跑它）
  frontend/dist/        ← 复用 Electron 版 renderer 产物 + shim.js（见下）
  assets/icon.ico       托盘图标（go:embed）
  scripts/              验证与生成脚本（见「验证」一节）
```

## 渲染层复用方式

`frontend/dist/` 里的 `settings / break / takeout / prebreak` 几套页面是 Electron 版

`dist/renderer` 的产物副本，**HTML/CSS/JS 一行未改**；只额外注入 `shim.js`，把渲染层

依赖的 `window.api`（Electron preload 的形状）映射到 Go 后端。

这套产物是**固化后的最终副本**（Electron 工程已删除，不再有同步脚本）。当初的同步契约
是「每个 html 在自己的脚本前注入 shim、文本转 CRLF、二进制原样」——现在产物已按此
落盘并随仓库分发，改渲染层就直接改 `frontend/dist/` 里的文件。

`frontend/dist/index.html` 是 Go 版自己写的**调试控制台**（环境信息 + 角色磁贴 + 一键弹

提醒）。

**绑定已接通**：shim 不再依赖 v2 的全局 `window.go.main.App`，而是动态

`import("/wails/runtime.js")`（v3 内置运行时，由 assetserver 在 `/wails/runtime.js`

提供），再用 `Call.ByName("main.App.<方法>", ...args)` 调绑定。因此\*\*不需要执行

`wails3 generate bindings`\*\*，渲染层产物零改动即可复用。同源

（Windows 下页面 origin 是 `http://wails.localhost`）让动态 import 不被 CSP 拦下。

### 关键坑：全限定名的包前缀是 `main`，不是模块名

```
对：  main.App.BreakCurrent
错：  workerrest.App.BreakCurrent   ← go test 里 reflect 出的 PkgPath 是这个，别照抄
```

`pkg/application/bindings.go` 用 `namedType.PkgPath()` 拼 FQN；main 包在 `go build`

产物里 PkgPath 就是 `"main"`，而在 `go test` 二进制里会返回模块导入路径——**两者不同**。

所以「在单测里用反射比对前缀」是假信号，真值只能靠无头服务端到端验证（见下）。

名字写错时后端返回 `422 unknown bound method name`，不会静默失败。

## 验证（三层，均可无头运行）

```powershell
go test ./...                                  # 契约 / shim 一致性 / 归一化 / 状态机 / 系统能力 / 提示音 / 统计 / 角色迁移 / 外卖 / 更新（含版本比较）
python scripts/smoke_server.py                 # 绑定层：166 项断言
python scripts/probe_renderer.py               # 渲染层：120 项断言 + 12 张截图
python scripts/probe_gui_boot.py               # GUI 启动层：16 项断言（系统能力 / 提前提示窗 / 提示音宿主）
python scripts/make_portable.py                # 便携打包：构建 + 组装 + 打 zip + 校验
python scripts/verify_portable.py              # 便携验证：隔离 APPDATA 解包首跑，验落点
```

- **smoke_server.py**：`go build -tags server` 产出无头服务（环境变量

  `WAILS_SERVER_HOST` / `WAILS_SERVER_PORT` 可覆盖监听地址），按 `/wails/runtime.js`

  里 `Call.ByName` 的**同一请求形状** `POST /wails/runtime` 逐个打绑定，并把返回数据与

  **隔离副本里的 settings.json / role.json** 逐项对齐。绑定是否按预期 FQN 注册，只有这条

  通道能可靠验证（GUI 下看不到注册表，生成的 bindings 又不反映运行时值）。
- **probe_renderer.py**：把 `frontend/dist` 的**真实页面**装进 Electron（只当浏览器用，

  `--disable-gpu --in-process-gpu --no-sandbox`）并指向无头服务，断言 shim 生效

  （`window.__WR_SHIM="go"`）、**设置页整页填表且无 JS 错误**、六个页签逐个切过去都能渲染、

  角色磁贴与预览图全部装载、提醒窗口里立绘**真的被浏览器解码**

  （`naturalWidth x naturalHeight`）、说话人 / 气泡文案 / 倒计时 / 主题色与配置一致，

  每个场景截图存档。
- **probe_gui_boot.py**：把**带控制台**的 GUI 产物（单独构建一份去掉 `-H windowsgui` 的  
  exe，否则没有 stdout）拉起来约 12 秒读日志。它补的是无头冒烟**结构性看不见**的那一块：  
  无头构建里三条系统监视是故意不启动的（会把调度器随机挂起、毁掉确定性断言），窗口也  
  一律不建。断言 `[boot] 启动完成` / `[sys] 空闲=… 勿扰=… 进程=…` /  
  `[prebreak] 提前提示窗已弹出` / `[sound] 播放宿主窗口已创建` / `[sound] 播放` /  
  `sound.html` 真的被资源服务交付 / 无 panic / 启动路径没写注册表。  
  **容器或 CI 里 WebView2 的渲染进程常常起不来**（日志刷 `webview2: process failed`），  
  但主进程的启动链路、系统调用探测与窗口创建都不受影响，断言只打在这一层。
- **verify_settings_parity.py / verify_plan_parity.py / verify_stats_parity.py 已移除**：这三个
  对拍脚本把 **Electron 自己的实现当黄金参考**（`gen_*.mjs` 用 `ts.transpileModule` + `node:vm`
  原地跑 Electron 源码）。Electron 工程已删除，黄金参考来源随之消失，故一并移除。
  迁移期间它们已把「Go 与 Electron 逐字段一致」验到 0 处差异（配置三层 / 计划 27 场景 /
  统计 279 场景），结论固化在下面的「已完成」清单里。
- **scheduler_test.go**：状态机（`BreakScheduler` 的移植）单测。关键设计是把宿主能力抽成
  `schedHost` 接口，单测传假宿主，于是「推迟平移」「跳过消费」「暂停恢复」「休息总时长封顶」
  「离开后只重锚内置倒计时」这些分支全都能脱离 Wails 跑（28 个用例，毫秒级）。

### 验证脚本的两条铁律

1. **必须隔离配置**。加载路径在「补生成 installId / 种入预置条目」时会**回写配置文件**；

   直接对着 `%APPDATA%` 里的真实配置跑，会改动用户的数据（第一次实测就把键序动掉了）。

   所以 `smoke_server.py` / `probe_renderer.py` 一律先把真实

   配置拷进 `%TEMP%`，再用 `WR_SETTINGS_PATH` **独占**指向副本；前两个还会在结束时断言

   真实 `settings.json` 的 SHA-256 没变。新增验证脚本请照抄这套。
2. **断言要按真实契约写**。设置页是「拿到什么就填什么」，所以字段名 / 单位都要照

   `settings.html` 里的标签核（例：`#short-interval` 的单位是**分钟**，

   `short.durationMs` 是毫秒；角色缩略图的字段名是 `thumb` 而不是 `previewUrl`）。

各脚本都会把报告写到 `%TEMP%`（`wr_go_smoke_report.txt` / `wr_go_probe_report.txt`），

退出码 0 = 全通过。

## 构建

```powershell
# 正常（GUI）
go build -o build/bin/chibingren.exe -ldflags "-H windowsgui -s -w" .
# 无头（验证用，没有原生窗口，但仍服务资源与绑定端点）
go build -tags server -o build/bin/chibingren_server.exe -ldflags "-s -w" .
# 便携目录打包（产出 release/WorkerRest-<版本>-portable.zip + 散装目录）
python scripts/make_portable.py            # 构建 + 组装 + 打 zip + 校验
python scripts/verify_portable.py          # 隔离 APPDATA 下解包首跑，验便携落点
```

体积：exe 12.34 MB；`tar -a -c -f out.zip chibingren.exe` → 4.82 MB；便携 zip（含 roles/）约 26 MB。

## 迁移记录（对照 Electron 版，全部完成）

**已完成**

- [x] Go 后端骨架 + Wails v3 集成（多窗口 / 托盘 / 单实例）
- [x] 主窗口用系统 WebView2 渲染（实测启动后 16 个 `msedgewebview2` 子进程）
- [x] **前端绑定接通**：`shim.js` → `/wails/runtime.js` → `Call.ByName`，多窗口闭环跑通
- [x] 会话数据结构与渲染层逐字对齐（`BreakInfo` / `SupStageInfo` / `SupGeom` / `SupLines`）
- [x] 角色目录：扫描 / 归一化 / 内置清单顺序 / 立绘 data URL / 台词挑选 / 增删改写回
- [x] **设置页迁移**：`settings.html/css/js` 零改动复用，主窗口即设置窗口；

  配置层（`GetSettings` / `SaveSettings` / `ResetSettings`）与 Electron 的

  `config.ts` **逐字段对拍一致**（真实配置 + 老配置 + 深度迁移三轮，均 0 处差异）
- [x] 配置归一化：`deepMerge`（数组整体替换）+ 加载 / 保存 / 导入三条路径，

  含旧键迁移、越界夹紧、预置条目种入、内置音乐种入、内置会议软件种入
- [x] **计划层**（`breakplan.go`）：两级触发线模型 —— 倒计时线（内置短/长**共用**一条、

  按「每 N 次短休息接一次长休息」挑；每条自定义提醒各一条）+ 固定时刻线（每种休息各一条），

  谁更早就听谁的、同一时刻长休息优先；`occurredKey` 去重键、`nextSlotAt` 逐条目各找 8 天、

  `fixedNextMap` 已消费顺延、`todayTimeline` 把倒计时线展开成当天整条链。

  与 Electron 的 `breakPlan.ts` / `fixedTime.ts` **27 个场景逐字段对拍 0 处差异**。

  （注：`workTime.ts` 的「工作时段」功能在 Electron 侧已整体删除，两边都只清理残留键）
- [x] **调度器状态机**（`scheduler.go`）：到点切换 phase / 消费记账 `consumed` /

  推迟平移 / 跳过即消费 / 暂停合并 `pauseReasons` / 恢复时倒计时平移 + 固定线重算下一个

  未到时刻 / 休息总时长封顶 / 离开后只重锚内置倒计时（自定义与固定线不动、不消费长短名额）。

  宿主能力抽成 `schedHost` 接口，状态机脱离 Wails 可单测（28 个用例）
- [x] **状态卡与托盘接线**：`GetState` 走 `stateForRenderer()`（真实 timeline / rest / other /

  fixedNext）；`Action` 的 `breakNow / postpone / skip / pauseToggle / resetCycle / endBreak`

  全部汇聚到调度器；托盘菜单与文案对齐 `tray.ts`（单击 = 打开设置，三个随状态变的菜单项

  原位 `SetLabel` 而不重建菜单）
- [x] **系统能力**（`sys_*.go`，纯 Go，不需要 cgo）：空闲状态机（`user32!GetLastInputInfo`，  
  软件侧维护连续无输入时长、休息窗口关闭后从 0 重算）、勿扰探测（2/4/6 三个勿扰态）、  
  进程枚举（Toolhelp32）、注册表自启动（`HKCU\...\Run` + `StartupApproved` 的禁用位）。  
  **「离开检测 → `pause('idle')` + `reanchorCountdown()`」这条链路真的通了**；  
  托盘与设置页补上「开机自启动」。无头构建只探测、不启动会改状态的监视
- [x] **休息前提示窗**（`prebreak.go`）：右下角那个带倒计时与四动作的小窗。内容在打开时算一次  
  并记住（500ms 轮询期间不随计划漂移）、版式随 URL query 下发（内置 240 / 自定义 172 两档  
  高度，第一帧就按它铺），四个动作（立即 / 推迟 / 跳过 / 暂停）全部汇聚到调度器
- [x] **提示音**（`sound.go`）：内置四个 wav 随包走资源服务（`/sounds/…`），用户导入的音乐  
  走本进程的只读路由 `/__sound/`（白名单：音频目录内、或配置点名的绝对路径，其余 403）；  
  选曲口径对齐 `sound.ts`（单曲与启停无关、随机 / 顺序只在「启用且文件存在」的条目里挑、  
  顺序游标跨播放递增、列表停用 / 删空即刻意静音）；播放由隐藏宿主窗口 `ExecJS` 承载  
  （Wails 的 ExecJS 自带排队，不需要自己等页面就绪）。休息开始 / 结束各播一次
- [x] **使用统计**（`stats.go`）：三桶记账（工作 `WorkMs` / 休息 `BreakMs` / 离开 `AwayMs`，  
  账闭合为「工作 + 休息 + 离开 ≈ 软件运行时长」）+ 跨零点切段（`splitByDay`）+ 三重封顶  
  搬运（离开区间左端不越过工作段起点、按当日已计工作封顶、只搬离开起点所在那天）+  
  睡眠剪裁（`countedSpan`）+ 周期聚合（日/周/月/季/年）+ 历史查询 + 清空；落盘两层  
  （改动 2 秒防抖 + 1 分钟兜底 + 退出再写，`writeFileAtomic` tmp→Sync→Rename）。  
  与 Electron 的 `stats.ts` **279 个场景逐字段对拍 0 处差异**
- [x] **角色目录迁移**（`migrate_roles.go`）：旧版「角色数据存 settings.json」一次性搬进  
  角色目录 role.json（对应 `supervisor.ts` 的 `migrateLegacyRoles`）。内置角色用旧数据覆盖  
  模板（旧列表里没有的连目录一起删、不回补）、自定义角色按 sha1 摘要建目录搬图、  
  `fixedKey` 去 `.png` 重映射 + 失效兜底到第一个启用角色、迁完删掉 supervisor 的旧键并置  
  `rolesMigrated=true`。挂在 `NewApp` 里 `loadSettings` 之后（迁移了就当场落盘）
- [x] **外卖提醒窗口**（`takeout.go`）：会话构建（餐段 `takeoutMealLabel` / 台词 / 红包地址 /  
  督导立绘）+ 触发（固定时刻线 + 推迟线到点重弹 / 过期作废）+ 每秒 watcher + 多屏建窗  
  （`allScreens` 开则每屏一个、否则光标所在屏）+ 抬升簿记（`raiseState`）+ 键鼠活动轮询  
  （复用 `sys_idle` 的 `GetLastInputInfo`，tick 变则 `sup:active` 广播）。三个动作  
  `ordered`/`skip`/`postpone` 汇聚统计记账与推迟记账。对应 `takeoutWindow.ts` + `raise.ts` +  
  `displays.ts` + `reminderSync.ts` 四件套
- [x] **休息督导版式**（`supervisorLayout.ts`）：这是**纯渲染层**逻辑（量立绘实际占位再摆内容列、  
  按容器宽高比切横竖两套版式、立绘让位只降不升、气泡尾巴锚点对准角色可见中心），三处共用  
  （休息窗口 / 外卖窗口 / 设置页预览）。它随前端产物一起编译进 `dist/break.js` / `takeout.js` /  
  `settings.js`，**Go 侧复用同一份产物、无需（也不该）再写一份**。本轮只补了渲染探针的版式断言：  
  同一会话只换窗口尺寸，横屏 `data-sup-layout=side`（立绘左右缘 44/396、内容列 466px）、  
  竖屏 `data-sup-layout=top`（立绘左右缘 30/495、内容列 704px、尾巴锚点 tailX 已算出）
- [x] 体积验证：12.34 MB / zip 4.82 MB（含 embed 的前端资源、图标与四个内置提示音）
- [x] **原生文件对话框**（`filedialog.go`）：用 `github.com/harry1453/go-common-file-dialog`  
  （MIT、纯 COM 接口、**无 CGO**）调 Windows 系统原生打开 / 另存为对话框，替代 Electron 的  
  `dialog.showOpenDialog / showSaveDialog`——Wails v3 没公开 dialog 包（它把同名库 vendored 进  
  internal，第三方 import 不到），所以直接依赖这个公开库。封装成 `showOpenDialog /  
  showOpenMultiDialog / showSaveDialog`，每次调用放进 `runtime.LockOSThread` 的独立 goroutine  
  （COM 要求 CoInitialize 与 Show 在同一 STA 线程）。三个文件选择绑定由此接通：  
  `AddMusicSound`（多选音频 + 导入副本）、`PickAppExe`（选 exe 返回 `{path,exe}`）、  
  `PickSupervisorImage`（选图建角色目录，preview 直接复制原图——不引图像库、保持依赖简洁）
- [x] **导入 / 导出配置**（`bindings_settings.go` 的 `ExportSettings / ImportSettings`）：导出走  
  另存为对话框写缩进 JSON；导入走打开对话框读 JSON → `deepMerge` 到当前配置之上 →  
  `normalizeForSave` 归一化 → 落盘 → 同步标题 / 开机自启动（只在真变了才写注册表）/ 状态 /  
  托盘 / 调度计划。语义逐条对齐 Electron 的 `settings:export / settings:import`
- [x] **检查更新**（`update.go`）：用 `net/http` 查 GitHub Releases 的 `latest`（`lwb/worker-rest`，  
  与 Electron 同源，**零额外依赖**），三段版本比较（v 前缀 / 缺段容忍，`isNewerVersion` 逐字对齐），  
  结果写 `updateStatus` 并推 `update:status-changed`。启动时按 `checkUpdateOnStart` 延时 15 秒静默  
  查一次（失败不打扰）；无头模式不发网络请求（冒烟要确定性），版本比较由单测覆盖
- [x] **便携目录打包**（`scripts/make_portable.py` + `scripts/verify_portable.py`）：构建发布版  
  exe（`-X main.appVersion` 注入版本，`appVersion` 从 const 改为 var 以支持 ldflags 注入）→  
  组装 `WorkerRest-<版本>-portable/`（exe + roles/）→ 清运行时残留 → 打 zip 并校验内容 →  
  验证脚本在**隔离 APPDATA** 下解包首跑，断言配置落在 exe 旁（便携优先）、不外溢到 APPDATA。  
  **不做 NSIS 安装器**——Go 单二进制 + embed 前端资源天然绿色，便携目录 zip 即交付形态  
  （与 Electron 的 dir 目标对齐，免去 asar / koffi / marker）。

**待迁移清单已清空** —— 对照 Electron 版的全部模块均已迁入 Go 侧（骨架 / 设置页 / 会话 /  
调度 / 统计 / 角色迁移 / 外卖 / 督导版式 / 文件对话框 / 导入导出 / 检查更新 / 便携打包）。

**已知差异（Go 版与 Electron 版有意不同）**

- **用户音频目录的落点**：Electron 是 `userDataPath('sounds', 'sounds', false)` ——  
  非便携 = `<userData>/sounds`、便携 = `<运行目录>/sounds/sounds`（category 与 rel 同名，  
  结果多一层）。Go 版统一成「**配置文件所在目录旁的 `sounds/`**」：非便携形态下与  
  Electron 完全一致，便携形态下是 `<运行目录>/config/sounds`。可见影响只有一个：  
  便携老用户的**旧副本删不掉**（播放不受影响 —— 配置里存的是绝对路径）。
- **统计文件的便携落点**：Electron 是 `userDataPath('stats.json','data',true)`，便携形态下  
  落在 `<运行目录>/data/stats.json`；Go 版 `WR_SETTINGS_PATH` 分支直接取「配置文件同目录」，  
  便携形态下落在 `<运行目录>/config/stats.json`（与 `sounds/` 同一套约定，配置同目录旁的  
  数据文件）。功能不受影响 —— 落点由配置文件位置推导，两边读写的都是自己的那份。
- **系统睡眠事件（醒来后补记）**：Electron 靠 `powerMonitor` 的 suspend/resume 通知  
  `NotifySuspend` / `NotifyResume`（把睡眠整段算进离开、醒来后重锚）。Go 侧没有 Wails  
  对应的电源事件源，改为**醒来后补记**（`stats.go` 的 `NotifySlept` + `sys_power.go`）：  
  工作累加器每秒 tick 里比对「墙钟跳变」与「`GetTickCount64` 跳变」（后者不含睡眠时间），  
  差值超过 60 秒即判定睡过一觉，把睡眠整段补记进离开（不跨天）、工作段从醒来重新起算。  
  入睡那一刻无法实时回调（进程被挂起），但账目结果与 Electron 一致。
- **外卖/休息窗口的 z-order 抬升降级**：Electron 用 `moveTop()` 做同层「谁后抬谁在上」，  
  让外卖压在休息之上。Wails v3 没有暴露 z-order 抬升 API（只有 `SetAlwaysOnTop` 用  
  `HWND_TOPMOST` 置顶），所以 Go 版用 **`SetAlwaysOnTop(true)` + `Focus()`** 近似替代：  
  两个窗口都置顶时，后抬的那个被聚焦、通常在上，但「同层精确 z-order」语义无法逐像素复刻。  
  抬升簿记（`raiseState`）与键鼠活动轮询（`sup:active` 广播）仍照常工作。
