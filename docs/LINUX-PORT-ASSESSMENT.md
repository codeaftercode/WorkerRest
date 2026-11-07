# Linux 移植评估

> 状态：评估结论，**未改任何代码**。
> 前置：[ARCHITECTURE-REFACTOR-DESIGN.md](ARCHITECTURE-REFACTOR-DESIGN.md) 设计的平台抽象层已落地，
> 本文评估在这套骨架上做真机移植的实际代价。
> 日期：2026-11-03

---

## 1. 结论先说

三句话：

1. **架构不是障碍。** `internal/` 下 7 个平台包已经成对写好了，移植是“填桩”，不是“重构”。
2. **有一个功能在 Linux 上不存在**，而它恰好是软件存在的理由：共享屏幕时隐藏提醒窗口。
3. **Wayland 会让一半的“监督”能力降级**，而 2026 年主流发行版的默认会话已经是 Wayland。

所以这件事的判断标准不是“能不能移植”，是“移植过去还是个什么东西”。

---

## 2. 现有骨架的实际状态

### 2.1 已经成对的 7 个包

| 包 | Windows 实现 | 当前 `_other` 桩的行为 |
|---|---|---|
| `internal/platform` | WebView2 预检、`GetLastInputInfo`、`HiddenSysProcAttr` | 预检恒“已就绪”；`LastInputTick() → (0, false)` |
| `internal/hotkey` | `RegisterHotKey` + 消息循环 | `RegisterHotKey` 恒 false（热键不启动） |
| `internal/autostart` | HKCU `Run` 键、`StartupApproved` | 全部 no-op，读不到也写不进 |
| `internal/windnd` | `SHQueryUserNotificationState`（勿扰 + 全屏） | 恒“不支持”，绝不报 active |
| `internal/winproc` | Toolhelp 快照枚举进程 | 恒“不支持” |
| `internal/winpower` | `GetTickCount64` | `time.Now().UnixMilli()` 近似 |
| `internal/notifier` | `MessageBoxW` / `ShellExecuteW` | 只打日志 |

这个结构是重构那一轮的直接红利：业务层和前端一行都不用动。

### 2.2 三处抽象漏了窗

骨架不是完美的，有三处漏了，移植时会撞上：

**① `internal/hotkey` 把 Win32 的消息循环漏了出来。**

它对外暴露的是一整套 `WinMsg` / `PeekMessage` / `GetMessage` / `PostThreadMessage` / `CurrentThreadID`。这是 Win32 线程消息队列的模型，Linux 没有对应概念。填桩填不出这个模型，只能换实现，连带 `HotkeyManager` 一起重写。

好消息是 Wails v3 自己带了全局快捷键抽象，两条路都有实现：

```
pkg/application/global_shortcut_linux_x11.go     X11 XGrabKey
pkg/application/global_shortcut_linux_portal.go  xdg-desktop-portal GlobalShortcuts（Wayland）
```

正确做法是把手写的 manager 整个换成 Wails 的 API，`internal/hotkey` 只保留加速键字符串解析。

**② `internal/autostart` 的返回值带着 Windows 语义。**

`RegResult`、`ClearApproval()`、`ApprovalDisabled()` 都是 Windows 10“启动应用审批”机制的产物（用户可能在任务管理器里禁用了自启动项，程序得知道）。Linux 的 `.desktop` 文件没有这层，接口要重新切一刀。

**③ `internal/winpower` 的桩不是它自己注释说的那个东西。**

`power_other.go` 注释写“用单调时钟（time.Since 基于单调时钟）替代 `GetTickCount64`”，实现却是 `time.Now().UnixMilli()`，那是**墙钟**，改系统时间就会跳。真移植时要用 `CLOCK_BOOTTIME`（含睡眠）与 `CLOCK_MONOTONIC`（不含）的差值。顺带说明：包里的 `DetectSleep(wallDelta, tickDelta)` 是纯函数，跨平台直接可用。

---

## 3. 改造清单

### 3.1 填实现就能工作的

| 能力 | Linux 实现路径 | 备注 |
|---|---|---|
| 开机自启 | `~/.config/autostart/workerrest.desktop` | 比注册表简单，但要先按 §2.2② 改接口 |
| 进程枚举 | `/proc/*/comm` | 比 Toolhelp 快照容易，`appPausePresets` 逻辑原样可用 |
| 外链 / 错误框 | `xdg-open` / `notify-send`，或直接用 Wails 的 `Browser.OpenURL` / `Dialog` | |
| 勿扰检测 | GNOME：`gsettings get org.gnome.desktop.notifications show-banners`；KDE：`org.freedesktop.Notifications` 的 `Inhibited` 属性 | 要按桌面环境分支，没有统一接口 |
| 全屏检测 | X11：遍历 `_NET_CLIENT_LIST` 查 `_NET_WM_STATE_FULLSCREEN` | Wayland 见 §3.3 |
| 休眠唤醒计时 | `CLOCK_BOOTTIME − CLOCK_MONOTONIC` | 见 §2.2③ |
| 空闲 / 全局键鼠 | X11：XInput2 raw events（`XRecord` 已弃用） | Wayland 见 §3.3 |
| 多屏铺满 | Wails 的 `screen_linux.go` 已实现，`targetDisplays()` 直接可用 | |
| 窗口置顶 | Wails 的 `setAlwaysOnTop` 走 GTK `keep_above` | Wayland 下合成器可忽略 |
| 单实例 | Wails 的 `single_instance_linux.go` | 无需自己写 |
| 托盘 | Wails 的 `systemtray_linux.go`（StatusNotifierItem） | GNOME 默认不显示，见 §4 |

### 3.2 要改接口的

- `internal/hotkey` → 换成 Wails `GlobalShortcut`（见 §2.2①）
- `internal/autostart` → 去掉 `RegResult` / `ClearApproval` / `ApprovalDisabled`（见 §2.2②）

### 3.3 无法实现的

**① 共享屏幕时隐藏提醒窗口。**

Wails v3.0.0 在 Linux 上是空实现：

```go
// pkg/application/webview_window_linux.go:536
func (w *linuxWebviewWindow) setContentProtection(enabled bool) {}
```

这不是 Wails 偷懒。`WDA_EXCLUDEFROMCAPTURE` 是 DWM 提供的一条**系统级契约**：窗口声明“别画我”，所有走 DWM 的捕获方都必须尊重。Linux 没有这个契约方。

作为对照，三个平台的实际状况：

| 平台 | 机制 | 状态 |
|---|---|---|
| Windows | `SetWindowDisplayAffinity(WDA_EXCLUDEFROMCAPTURE)` | 有，本项目正在用 |
| macOS | `[NSWindow setSharingType:NSWindowSharingNone]` | 有，Wails v3 已实现（`webview_window_darwin.go:1137`） |
| Linux | —— | 无 |

所以这不是“跨平台都难”，是 **Linux 独有的缺口**。macOS 版理论上能保留这个卖点，Linux 版不能。

在 Linux 上为什么补不上：

- X11 下 OBS 是直接 `XShmGetImage` 读根窗口、或读合成器输出，应用没有任何 API 能说“别画我”。合成器级别的窗口排除从来没有标准化过。
- Wayland 下客户端连自己窗口在屏幕上的坐标都拿不到，更不可能影响 screencast 的输出流。

**这是本软件存在的理由本身**，所以 Linux 版必须换思路，见 §3.4。

**② 抢焦点。**

Wayland 协议明确禁止，需要 xdg-activation token，且由合成器决定是否授予。提醒窗口可能在后台，用户根本不知道它弹了。X11 下问题小得多。

**③ 全局键鼠的精细粒度。**

Wayland 下只剩两个选项：读 `/dev/input`（需要 input 组权限，且拿到的是原始设备事件，要自己管设备热插拔），或者用 logind 的 `IdleHint`（粒度是桌面环境设定的空闲阈值，通常 5 分钟）。后者对这个软件毫无用处——休息窗口只有 20 秒。

后果不只是二阶段失效，“长时间无操作自动暂停”和“离开统计”也一起失效。

### 3.4 唯一可行的替代方案，但可行性比看上去差

Linux 有一个 Windows 没有的信号：**麦克风占用**。PipeWire / PulseAudio 能确切拿到“哪个进程在用麦克风”，`pactl subscribe` 就能实时监听。这比按进程名猜“在不在开会”精确一个量级——能区分“飞书挂在后台”和“飞书正在开会”，看起来正好能解决 `appPausePresets` 只敢收四个“打开即开会”客户端的困境。

但它能不能用来判定“在不在开会”，取决于“静音”是哪一种。这一条当初被想当然了。

威斯康星大学麦迪逊分校有一篇论文专门测过（*A Privacy Analysis of Mute Buttons in Video Conferencing Apps*），分析了 10 个会议应用的静音行为，结果分成三类，而三类的检测结果是相反的：

| 静音行为 | 代表 | 麦克风指示灯 | PipeWire 里的表现 |
|---|---|---|---|
| 持续采样 | Webex（唯一一个） | 一直亮 | source-output 存在，数据照常流动 |
| 保留设备但不读数据 | Zoom 等大多数原生客户端 | 一直亮 | source-output 存在，数据为零或不流动 |
| 浏览器软件静音 | 所有 Web 应用 | **熄灭** | **source-output 消失** |

第二类之所以这么干，是因为要维持“你已静音，但检测到你在说话”这个提示，必须留着句柄读 silent 标志。

**所以判据不是“有没有音频数据”，是“有没有客户端流连着麦克风”。** 原生客户端静音时连接还在，照样测得到；而且这时信号更干净——不用管音量，只看流在不在。

**但浏览器开会 + 静音测不到。** 浏览器把 track 设成 disable 就释放设备。这一条对 Linux 尤其伤：Linux 上 Google Meet 就是纯浏览器，Teams 也只剩 Web 版——最常用的两个恰好落在第三类。

比静音更彻底的漏检是压根没开设备，这几种连“静音”这个动作都没有：

- **不连接音频入会**（Zoom 的 "Join without audio"、Teams 同款选项），完全没有 capture 流。
- **全程旁听，麦克风摄像头都不开。**
- **自己在共享屏幕给别人看**，通常不上麦。

这几类的信号都是零，而它们恰好是“共享出去会很尴尬”的高危场景。

#### 判据链

要做就得叠条件，不能只认一条：

1. **连接持续 > 10–15 秒。** 飞书发语音条、语音输入、搜索都会短暂占麦，靠时长过滤——会议是长时间的。
2. **`media.role = Communication`。** WebRTC 栈（Chrome / Firefox / Electron）自动给采集流打的标签，比进程名更能说明“这是一条通信流，不是录音”。
3. **`Corked` / `Mute` 字段。** `pactl list source-outputs` 能看到流的暂停与静音状态，“流存在 + corked”几乎等于“开了麦但没在用”，正好对应上表第二类。**这一条没有实测过**，实现前要在 Zoom / 飞书 / 腾讯会议三家的 Linux 客户端上各验一遍。
4. **摄像头做交叉验证。** 摄像头也被占用时几乎可以确定在开会，但很多人不开，所以只能加分，不能当必要条件。

#### 但不该拿它做自动判定

两种误判的代价不对称：

- **误判成开会 → 不弹提醒**：用户漏掉休息，而且永远不知道自己漏了。
- **误判成没开会 → 弹了**：最多尴尬一次，而这正是软件存在的理由。

而漏检的那几类（浏览器开会、不连音频、纯旁听）恰好是风险最高的人。**信号在最需要它的时候最不靠。**

所以它的位置不是“自动暂停”，是“问一句”：

> 托盘提示：“检测到麦克风被 XX 占用，可能在开会。要暂停提醒吗？”[暂停] [忽略]

点一下，准确率就是 100%；不点也没有副作用。代价是每次都要点一下，但比“会议中途突然冒出提醒”和“误判后整天不提醒”都便宜。

Windows 版其实已经有这个兜底动作了——应用暂停。Linux 的麦克风检测不是发明新机制，只是**把这一步自动化一格**：从“你自己想起来点”变成“我提醒你可以点了”。

代价是产品的承诺变了。Windows 版承诺“提醒照常弹，别人看不见”；Linux 版只能承诺“开会时我会提醒你暂停”。后者有用，但不是同一个卖点，发布时必须说清楚，不能含糊成一个“共享保护”。

---

## 4. 隐性风险

这些不是 API 缺失，是“写完了也不知道哪里不对劲”的那一类。

| 风险 | 具体表现 | 依据 |
|---|---|---|
| **提示音不响** | `--autoplay-policy=no-user-gesture-required` 是 Chromium 参数，WebKitGTK 完全不认。WebKitGTK 有 `webkit_settings_set_media_playback_requires_user_gesture`，但 Wails v3 的 Linux 后端没暴露（只有 iOS 后端有对应设置） | `main.go:143` 记的正是这个症状：“试听能响、真休息不响” |
| **探针测的不是真机** | 探针是 Playwright 驱动 Chromium（`renderer_probe.js`），和 Windows 的 WebView2 同内核。移植后真机是 WebKitGTK，字体度量不同 | `probe_role_title.py` 的列区间判据、`--cd-w` 反推、`fitDialogText()` 折行点全部要在 WebKit 下重测 |
| **中文字体** | WebKitGTK 走 fontconfig，宿主没装中文字体会直接出豆腐块 | 需在文档里给出依赖包名，或在打包时内嵌字体 |
| **NVIDIA 白屏** | WebKitGTK 的 DMA-BUF 渲染器在 NVIDIA 专有驱动上 `gbm_bo_map` 失败，窗口全白。Wails 会检测 `/sys/module/nvidia` 自动置 `WEBKIT_DISABLE_DMABUF_RENDERER=1`，容器里可能检测不到 | Wails 官方 Linux 打包文档 |
| **打包链整套重写** | `make_portable.py`、`gen_windows_res.py`（goversioninfo）、`rsrc_windows_amd64.syso`、`verify_portable.py`、`清除开机自启动项.bat` 全是 Windows 的 | |
| **不能交叉编译** | Wails Linux 必须 CGO + GTK/WebKit 头文件，构建只能在 Linux 机器上做（或走官方 Docker 交叉构建） | |

---

## 5. 发行版与构建目标

### 5.1 硬约束一：WebKitGTK 的 ABI 在编译期就固定了

Wails v3 有两条构建路径，`pkg-config` 要求不同：

```
默认：      gtk4      webkitgtk-6.0     ← 官方推荐
-tags gtk3：gtk+-3.0   webkit2gtk-4.1   ← 兼容路径，v3.1 移除
```

两组 ABI 不兼容。一份二进制不可能两者通吃，必须按 ABI 出两份包，或者放弃其中一类系统。

| 库 | 覆盖的发行版 |
|---|---|
| `webkitgtk-6.0`（GTK4） | Ubuntu 24.04+、Debian 13+、Fedora 40+、Arch、openSUSE Tumbleweed |
| `webkit2gtk-4.1`（GTK3） | Ubuntu 22.04、Debian 12、Fedora ≤39、RHEL / Rocky / Alma 9 |

**结论：只做 `webkitgtk-6.0`，不做 gtk3 那条路。** 两个理由：官方已把 gtk3 标为 v3.1 移除；Ubuntu 安全团队已停止为 22.04 的 webkit2gtk 提供更新（“在 jammy 及更早版本上已无法构建新版 webkit2gtk”，标记 Ignored）。为它出包等于让用户跑一个没有安全补丁的浏览器内核。

### 5.2 硬约束二：会话类型比发行版更决定成败

2026 年的主流默认会话都是 Wayland：Debian 13 是 GNOME 48 + Wayland，Fedora 默认 Wayland，KDE Plasma 6 默认 Wayland，Ubuntu 也默认 Wayland（NVIDIA 专有驱动下回退 X11）。

按 §3.3 的清单，Wayland 下残缺的是：

| 能力 | X11 | Wayland |
|---|---|---|
| 全局键鼠活动 | XInput2，可用 | 只能 `/dev/input`（需权限）或 logind `IdleHint`（粒度 5 分钟） |
| 全屏检测 | 可用 | `ext-foreign-toplevel-list-v1` 只有 KDE 实现，GNOME 不支持 |
| 抢焦点 / 强置顶 | 可用 | 合成器说了算 |
| 全局快捷键 | X11 `XGrabKey` | portal `GlobalShortcuts`，KDE 支持，GNOME 46+ 支持 |

也就是说：**X11 会话下功能完整，但 X11 正在退场；用户默认装的系统上，功能是残缺的。**

这件事只有一种诚实的处理方式：启动时读 `XDG_SESSION_TYPE`，Wayland 下主动告诉用户哪几项不可用，而不是让它静默失效。

### 5.3 分层清单

**Tier 1（首发必须站稳）**
- Ubuntu 24.04 LTS / 26.04 LTS（26.04 于 2026-04 发布，26.04.1 在 2026-09，WebKitGTK 2.52.6，同时提供 4.1 与 6.0）
- Debian 13 Trixie
- Fedora 40+

**Tier 2（成本接近零，顺手带上）**
- Arch / Manjaro：AUR 是 Wails 打包链自带格式
- openSUSE Tumbleweed

**明确不做**
- Ubuntu 22.04、Debian 12、RHEL 9 / Rocky 9 / Alma 9：缺 `webkitgtk-6.0`
- 国产发行版：见下

### 5.4 国产发行版的判断

UOS 20、银河麒麟 V10 这些主流版本都缺 `webkitgtk-6.0`（分别基于 Debian 10 与 CentOS 8 系），且以 ARM64 为主（飞腾 / 鲲鹏），要单独构建一套。更重要的是：个人 MIT 开源项目进信创需要软著、检测报告、适配认证，成本远大于收益。

**除非要走政企渠道，否则先不碰。** 如果需要留接口，就是 `platform` 包继续按 `linux && arm64` 走同一份实现，不额外写代码。

---

## 6. 打包

Wails v3 的打包链自带 AppImage / DEB / RPM / AUR 四种（nfpm + linuxdeploy），不需要自己写脚本。

**不做 Flatpak 和 Snap。** 这软件要枚举 `/proc` 进程、监听全局键鼠、Wayland 下还要读 `/dev/input`。沙箱要放开到 `--filesystem=host --device=all` 才有意义，而那等于不再有沙箱。

首发建议出 **deb + AppImage** 两个。注意 AppImage 是“便携”不是“自带依赖”：WebKitGTK 通常被排除、依赖宿主，所以它的实际可用性还是取决于宿主的 WebKit 版本。另外在 Ubuntu 24.04+ / Fedora 39+ / Arch 上，因为 `.relr.dyn` ELF 段导致 linuxdeploy 自带的 strip 处理不了，Wails 会自动关掉 strip，包体积大 20–40%，功能不受影响。

---

## 7. 建议路线

如果真要做，按这个顺序：

1. **只做 X11。** Wayland 下三个核心能力都残缺，做出来也是半残，先不碰。文档里明写“Wayland 会话功能受限”。
2. **填能干的桩**：autostart、winproc、notifier、winpower、windnd；`hotkey` 不是填桩，是按 §2.2① 换成 Wails 的 `GlobalShortcut`。
3. **先跑通探针体系。** server mode 是纯 HTTP，跨平台零改动，这是现有资产里最值钱的部分。但要清楚它测的是 Chromium 不是 WebKitGTK，像素判据要另建一套。
4. **实现麦克风检测，但只做到“问一句”。** 按 §3.4 的判据链来，先在 Zoom / 飞书 / 腾讯会议三家真机上把 `Corked` 的实际行为测清楚；不要做成静默自动暂停——漏检的恰好是最需要保护的那批人，而误判的代价全落在用户身上。
5. 最后才碰打包和发行版矩阵。

其中第 4 步决定 Linux 版是不是值得做。如果只做到第 3 步，得到的是一个“会在开会时弹窗、还会被同事看到”的 Stretchly——那正是这个项目当初想解决的问题。
