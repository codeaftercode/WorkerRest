# 验收测试报告 — 大规模重构后的全量回归

> 测试日期：2026-11-01　对象：WorkerRest（吃饼人也敢休息？）重构后工作区（含未提交改动）
> 结论：**1313 项自动断言全部通过，发现并修复 3 个真实缺陷 + 7 处重构遗留的探针失效**。
> 口径：不接受“以前跑过”作为证据 —— 所有数字都是本轮重新执行得到的；跑不起来的项如实标为“无法测量”，不写通过。

---

## 0. 一句话结论

重构没有把功能改坏：六个页面、六个设置页签、十个对话框、52 个前端绑定、全部多值配置项、异常与边界路径均已逐项执行并通过。测试过程中发现的 3 个缺陷（空专员目录白屏、五个对话框无 Esc、关闭对话框后焦点丢失）**都是重构前就存在的老问题**，本轮一并修掉；另有 7 个探针脚本因目录重构静默失效 —— 也就是说“重构后的测试”在修它们之前根本没跑起来过。

---

## 1. 环境与构建（修正上一份报告的结论）

上一份报告（`QA-FUNCTIONAL-2026-10-31.md`）写“本机 Go 工具链缺失”，那个结论是错的，本轮已实跑：

| 项 | 值 |
|---|---|
| Go | go1.27.3，`D:/Programs/Go/bin/go.exe`（**不在 PATH**，须用绝对路径） |
| 构建缓存 | `GOPATH=C:/Users/lwb/go`、`GOCACHE=C:/Users/lwb/AppData/Local/go-build`（本机 LOCALAPPDATA 可用，与旧记录里的 Administrator 路径不同） |
| 无头产物 | `build/bin/rest_server.exe`（本轮重建，`-tags server`） |
| GUI 产物 | `build/bin/rest.exe`（本轮重建，`-trimpath -ldflags "-H windowsgui -s -w"`） |
| 浏览器自动化 | Playwright 驱动系统 Edge（`channel: "msedge"`），Node 22 |
| 配置隔离 | 全部探针用 `WR_SETTINGS_PATH` 指向临时副本，收尾断言仓库内 `config/settings.json` 的 SHA-256 未变 |
| 桌面状态 | **本会话桌面 BitBlt 抓图整块黑**（非黑像素 0.0%）→ 捕获保护一项无法测量，见第 6 节 |

---

## 2. 覆盖清单与结果

### 2.1 无头端到端（真实二进制 + Wails HTTP 端点 + Playwright）

| 脚本 | 覆盖内容 | 断言 | 结果 |
|---|---|---|---|
| `scripts/smoke_server.py` | 前端 → Go 绑定全链路、52 个绑定的调用形状、日志与状态一致性 | 181 | 0 失败 |
| `scripts/qa_func_supplement.py` | 时间基准、非法值注入、配置损坏自愈、恢复默认 | 29 | 0 失败 |
| `scripts/probe/probe_renderer.py` | 渲染层深断言：index / prebreak / break / takeout / settings 五页；立绘几何、倒计时字号、消息池、滚动链、暂停态文案、角色拖拽排序、重名拒绝、时间基准 | 479 | 0 失败 |
| `scripts/probe/probe_settings_ui.py` | 设置页三层：① 六页签全控件扫描（数量基线 / 可访问名 / 无横向溢出 / 值与配置一致）+ 十对话框静态语义契约（91）；② 对话框交互：真点击打开、焦点进框、Esc、焦点归位、Tab 陷阱、遮罩关闭（53）；③ 写入链路：快捷键录入（合法/裸键/重复键双向）、音量、总开关、播放方式三值、主题切换、外卖数值归一、恢复默认取消不改配置（27） | **171** | 0 失败 |
| `scripts/probe/probe_bindings_full.py` | `contract.go` 里 52 个绑定逐个真实调用：只读结构、保存/忽略版本/重置往返、窗口 no-op、会话起停、专员写操作拒绝路径、无头文件对话框降级、外链拒绝分支；末尾元测试（伪造绑定名必须返回 unknown bound method） | 134 | 0 失败 |
| `scripts/probe/probe_config_matrix.py` | 每个多值配置项逐个取值：short/long 的 mode 与 slots、sound.mode 三值/音量边界、supervisor.selection、reminderMessages.order、takeout.pauseAction/leadSec/slots、theme/shortcuts 透传、sites 过滤；再加 7 组**启动态**矩阵（nextAt / nextSource / nextType / timeline 类型序列、longEveryNShort=1 vs 3） | 69 | 0 失败 |
| `scripts/probe/probe_edge_cases.py` | 缺资源目录、配置只读、20 路并发保存、emoji + 300 字标题、100 时刻 + 30 自定义提醒、配置落点被占成目录 | 33 | 0 失败 |
| `scripts/probe/probe_sound_scan.py` | 提示音目录体检：新音频询问导入、死条目询问删除 | 27 | 0 失败 |
| `scripts/probe/probe_stats_file.py` | 统计文件自愈：首跑自建、损坏改名备份后重建 | 19 | 0 失败 |
| `scripts/probe/probe_crossday.py` | 跨零点：报表跟着翻页 | 11 | 0 失败 |

无头小计：**1153 项断言，0 失败**。

### 2.2 真机（真实窗口 / 真实进程）

| 脚本 | 覆盖内容 | 断言 | 结果 |
|---|---|---|---|
| `scripts/probe/probe_gui_boot.py` | GUI 产物冷启动 12 秒：`[boot]` 完成、无 panic、系统能力探测、空闲采样、三条监视、调度器、四个快捷键无注册失败、提前提示窗与播放宿主窗真的建出来、`sound.html` 由资源服务交付、休息开始/结束播放链路留痕、启动路径不写注册表 | 17 | 0 失败 |
| `scripts/probe/probe_window_gui.py` | 首次启动 874×857、拖到 754×757 后落盘 `lastWidth/lastHeight`、重启沿用、关掉“自动记录”回默认 | 9 | 0 失败 |
| `scripts/verify/verify_window_size.py` | 窗口尺寸端到端断言（含落盘值） | 14 | 0 失败 |
| `scripts/verify/verify_about.py` | 关于页/鸣谢弹窗截图与字段测量（非断言脚本） | — | 测量值符合规格¹ |

真机小计：**40 项断言，0 失败**。
¹ 实测：版本行 `dev | 本地构建`，数字 14px/650、时间 12.5px/520、同一行且有分隔线；鸣谢弹窗 12px 等宽、行高 20.4、CRLF 未混入、首行“本程序使用了下列开源项目”；关于页口径文案“不收集个人信息；仅检查更新时联网，且只下载、不上传”（无“不联网”字样）。

### 2.3 可访问性

| 脚本 | 覆盖内容 | 断言/结果 |
|---|---|---|
| `node scripts/probe_a11y_focus.js` | 十个对话框焦点行为：进得去 / Tab 跑不出 / Shift+Tab 回绕 / 关后回得来 | 101 项，0 失败 |
| `node scripts/probe_cdrop_a11y.js` | 自绘下拉 `.cdrop-host` 键盘可达性（方向键 / Home / End / Esc / 焦点回归） | 19 项，0 失败 |
| `node scripts/a11y_audit.js` | 四页静态审计：`settings.html` 135 控件、`break` 1、`takeout` 3、`prebreak` 4 | 全部页 `unnamedCount=0`、`imgNoAlt=0`、`lang=zh-CN`；设置页 1 个活区 |

可访问性小计：**120 项断言 + 4 页静态审计，0 失败**。

### 2.4 Go 单测与覆盖率（`go test -count=1 -tags server ./...`）

全部包 `ok`。覆盖率：

| 包 | 覆盖率 | 包 | 覆盖率 |
|---|---|---|---|
| `internal/urltls` | 100.0% | `internal/idle` | 96.2% |
| `internal/update` | 92.5% | `internal/winproc` | 90.5% |
| `internal/autostart` | 84.6% | `internal/scheduler` | 82.4% |
| `internal/stats` | 80.8% | `internal/windnd` | 78.3% |
| `internal/winpower` | 66.7% | `internal/platform` | 66.0% |
| `internal/logx` | 64.8% | `internal/settings` | 61.8% |
| `internal/sound` | 51.2% | `cmd/workerrest` | 48.1% |

`internal/model` / `plan` / `role` / `hotkey` / `notifier` / `jsonx` **无测试文件**（纯类型或薄封装；行为由上面 1153 项集成断言覆盖）。

---

## 3. 本轮发现并修复的真实缺陷

### 3.1 空专员目录 → 立绘库白屏（P1，重构前就存在）

- 现象：便携包被删掉 `assets/supervisors/`（或首次运行素材还没落地）时，`ListRoles()` 的 `ScanRoles` 返回 `nil`，JSON 序列化成 `null`；前端是 `for (const r of ROLES)`，拿到 `null` 直接抛 `TypeError`，整个立绘库白屏。
- 修复：`cmd/workerrest/app_bindings.go` 里空目录统一返回 `[]role.Role{}`，让“没有专员”成为正常状态。
- 回归：新增 `cmd/workerrest/roles_bindings_test.go`；`probe_edge_cases.py` 的“缺资源目录”用例同时钉住（`ListRoles` 返回 `[]`、`AppInfo.roles == 0`、`SoundFileURL` 空串、服务不崩）。

### 3.2 五个对话框没有 Esc（P2）

- 现象：十个 `.modal` 里只有 confirm / prompt / notices / color / app-pick 自己监听了 Esc；批量新增、统计损坏提示、提示音体检、更新提示、更新完成这五个只能点按钮关，键盘用户按 Esc 没反应。
- 修复：`settings.js` 加 `ESC_DISMISS` 兜底表，Esc 时**点它自己的关闭按钮**（复用原关闭逻辑，不抄第二份语义）。
- 验证：`settings_modals_probe.js` 十个对话框逐个断言“Esc 能关”，并单独验证“没有对话框时按 Esc 是空操作”。

### 3.3 关闭对话框后焦点掉到页面顶部（P2）

- 现象：调色弹窗、鸣谢弹窗在**显示自己的同一段同步代码里**就把焦点移进框内，于是 `MutationObserver` 回调跑起来时活动元素已在框内，`modalOpener` 记成 null；关闭后焦点退到“页面第一个可聚焦控件”——实测落到“开机自启动”勾选框，键盘用户的上下文当场丢失。
- 修复：加 `focusin` 持续记录“最后一个在框外拿到焦点的元素”，打开时若活动元素已在框内就退到它，与打开顺序无关。
- 验证：真点击打开 → Esc → 断言 `document.activeElement === 触发控件`（十个对话框全覆盖）。

---

## 4. 重构遗留：7 个探针脚本静默失效（已修）

目录重构把 `node_modules` 移到了 `scripts/`、把 main 包移到了 `cmd/workerrest/`，但脚本里还写死老路径。这些脚本**要么直接报错退出、要么找不到依赖**，等价于“重构后的 Playwright 测试类整体不存在”：

| 脚本 | 失效原因 |
|---|---|
| `manual_shots.py`、`probe_crossday.py`、`probe_window_size.py`、`verify_about.py`、`verify_window_size.py` | playwright 只在根目录找（已改为 `scripts/node_modules` → 根 双位置查找） |
| `probe_renderer.py` | 同上（经 `playwright_mod` 双位置查找） |
| `probe_window_gui.py`、`probe_window_size.py`、`verify_about.py`、`verify_window_size.py` | `go build ... "."` 指向根目录（已改为 `./cmd/workerrest`） |
| `renderer_probe.js`、`about_shot.js` | `probe_preload.js` 相对路径少了一层（已改为 `../probe_preload.js`） |

---

## 5. 探针自身的问题（不是产品缺陷，但会误报）

1. **假失败 6 项**（`probe_bindings_full`）：`PendingUpdate`、`GetStatsFileNotice`、`GetSoundScanNotice`、`PickSupervisorImage`、`PickAppExe`、`TakeoutAction` 返回 `null` 是设计语义（前端 `if (!picked) return`），原断言写错了判据。
2. **假失败 13 项**（`probe_config_matrix`）：`GetSettings` 返回的是**合并后的原始 map**，不是生效值 —— `durationMs`/`intervalMs`/`volume` 不在这一层夹取。已改为在正确层断言（极端间隔看实际连发条数、音量看日志里的播放音量、sites 看 `CheckUpdate` 行为）。另修正：`sound.mode` 合法集是 `single|random|sequence`（没有 `music`）。
3. **环境噪声**：Windows 突发请求下偶发 `WSAEADDRINUSE(10048)` / `WSAENOBUFS(10055)` / Wails 反代 `upstream connect failed`。四支 Python 探针统一加退避重试（只在“请求还没进方法体”的错误上重试，不影响幂等性）。
4. **`probe_capture_guard.py` 增设桌面预检**：桌面截屏整块黑时，原逻辑会把“我看不见屏幕”判成“捕获保护没生效”，结论正好反了。现在预检不过直接返回**退出码 3 = 无法测量**。

---

## 6. 无法测量 / 未覆盖（如实声明）

| 项 | 原因 | 影响 |
|---|---|---|
| **共享屏幕时隐藏（捕获保护）** | 本会话桌面 BitBlt 抓图整块黑（非黑像素 0.0%），对照组拿不到品红，实验组“0% 品红”与“屏幕全黑”不可区分 | 既不算通过也不算失败；需在有人值守的交互式桌面重跑 `python scripts/probe/probe_capture_guard.py` |
| **真实发声** | 无头构建不建窗口，GUI 构建下探针只能读到“本应播放哪个文件”的日志 | 播放链路已验证（gui_boot 17 项），“音箱是否出声”属人工确认 |
| **更新链真实下载安装** | 需要真实更新服务器 + 上一版安装包 + 干净 VM | `internal/update` 单测 92.5%，sha256/大小/上限的 fail-closed 分支均有用例 |
| **注册表自启动写入** | `reg.exe` 在沙箱黑名单 | `internal/autostart` 单测 84.6% 覆盖解析与命令拼装；GUI 启动路径已断言“不写注册表” |
| **多显示器不同 DPI** | 本机单显示器（探针在 192 DPI 下跑过） | — |

---

## 7. 复现

```bash
# 单测 + 静态检查
GOPATH=C:/Users/lwb/go GOCACHE=C:/Users/lwb/AppData/Local/go-build \
  /d/Programs/Go/bin/go.exe test -count=1 -tags server ./...
/d/Programs/Go/bin/go.exe vet -tags server ./...

# 无头产物（所有无头探针的前置）
GOPATH=C:/Users/lwb/go GOCACHE=C:/Users/lwb/AppData/Local/go-build \
  /d/Programs/Go/bin/go.exe build -tags server -o build/bin/rest_server.exe ./cmd/workerrest

python scripts/smoke_server.py
python scripts/qa_func_supplement.py
python scripts/probe/probe_renderer.py
python scripts/probe/probe_settings_ui.py      # 三支 JS 探针，共 171 项
python scripts/probe/probe_bindings_full.py
python scripts/probe/probe_config_matrix.py
python scripts/probe/probe_edge_cases.py
python scripts/probe/probe_sound_scan.py
python scripts/probe/probe_stats_file.py
python scripts/probe/probe_crossday.py

# 真机
python scripts/probe/probe_gui_boot.py
python scripts/probe/probe_window_gui.py
python scripts/verify/verify_window_size.py

# 可访问性
node scripts/a11y_audit.js
node scripts/probe_a11y_focus.js
node scripts/probe_cdrop_a11y.js

# 或走 Taskfile：task test / task check / task probe:ui / task probe:bindings /
#              task probe:matrix / task probe:edge / task a11y
```
