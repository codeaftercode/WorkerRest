# 全量功能审计与组合条件验收 — 2026-11-23

> 对象：吃饼人也敢休息？v0.9.9（HEAD = `v2c8f6e7` 之后的未提交改动）
> 范围：**全部 7 个窗口 / 6 个设置页 tab / 63 个 Go 绑定 / 7 条自动暂停原因 + manual**；
> 重点是**组合条件**：多条原因同时成立、暂停 × 休息相位 × 定时暂停、忽略表 × 到期闸门、
> 空闲门 × 暂停处置。
> 结论：**21 个包 906 个测试函数全绿、覆盖率 75.5%（语句），本轮复核的相关探针 171 项断言全通过**；
> 本轮发现并修复 **4 处产品缺陷**（3 处是“组合下才暴露”，单点测试都测不到），
> 另有 4 处经判定**符合预期**的刻意行为，已写进本文以防被后来的改动“优化”掉。
> 口径：数字全部为本轮实跑；跑不起来的写「无法测量」。
> **补充**：§9 是同日 20:40–21:15 的“全测”追加 —— 补跑 16 个漏列探针（642 项断言）、
> 补齐 10 个零覆盖纯逻辑函数、把托盘菜单从“只验菜单弹出”扩到**逐项点真动作**（6 → 33 项），
> 并把一条失效的判据（`probe_capture_guard` 的 BitBlt 抓图）如实降级为无法测量。

---

## 1. 方法：在原有四层上加一层「组合矩阵」

| 层 | 验什么 | 为什么这一层不能替代别的层 |
|---|---|---|
| 单元测试 | 纯逻辑与状态机不变量 | 单点行为，各功能各测一条 |
| **组合矩阵（本轮新增）** | **同时成立的多条原因 / 多层状态交叉** | 单点全绿而组合漏窗，是这个软件最容易出的错：五条监视链路互不通信 |
| 页面层探针（file:// + 假 api） | 页面自己该负责的事：按钮形状、文案、溢出、暂停态形态 | 真窗只看得出“点了有反应”，看不出“按钮该不该在” |
| 无头探针（`-tags server`） | 绑定链路、页面渲染、配置矩阵、边界异常 | 起得快、可重复；但故意不启动会改调度器的监视器 |
| 真 GUI 探针 | 真窗口、托盘、全局热键、询问窗 Z 序、自动暂停 | 「窗真的弹出来了、真的没被压住」只有真机看得见 |

组合矩阵这一层的判据**只写状态机不变量**，不写某一处日志文案：

```
Paused ⇔ PauseReasons 非空          原因不重复
暂停态必有 PausedAt，非暂停态 PausedAt == 0
PauseDeadline ≠ 0  ⇒  PauseReasons 含 manual
```

这样以后新增第八条自动原因时，矩阵会自动把它算进去；写死文案的判据则会悄悄失效。

---

## 2. 环境

| 项 | 值 |
|---|---|
| 系统 | Windows 11，缩放 200%（dpi=192） |
| Go | go1.27.3（`D:/Programs/Go/bin`，不在 PATH；显式给 `GOPATH`/`GOCACHE`） |
| WebView2 | 本轮 `probe_gui_boot` 实测 `Environment created successfully`，版本由运行时常量记录 |
| 浏览器探针 | 系统 Edge（`channel: "msedge"`），无头 |
| 隔离 | `WR_SETTINGS_PATH` / `WR_STATS_PATH` / `WR_LOG_FILE` 指向 `%TEMP%`；每轮收尾断言真实 `config/settings.json` 未被改写 |

---

## 3. 验证矩阵

### 3.1 静态与单测

| 项 | 结果 |
|---|---|
| `gofmt -l cmd internal` | 干净（顺带把 `internal/plan/breakplan_next.go` 两行注释缩进按 gofmt 归位，纯空白） |
| `go vet -tags server ./...` | 无输出 |
| `go test -count=1 -tags server ./...` | **21 个包全绿**，906 个测试函数 |
| `go test -tags server -coverpkg=./...` | **75.5%**（语句口径；按块算会得到荒谬值，见 §7.3） |

改动文件的函数覆盖率（`go tool cover -func`）：

| 文件 | 关键函数 | 覆盖 |
|---|---|---|
| `takeout_fire.go` | `takeoutPauseTakeover`（新增）/ `checkTakeoutReminders` / `skipTakeoutWhilePaused` | 100% / 89.6% / 100% |
| `prebreak.go` | `preBreakAct` / `PreBreakAction` | 92.6% / 71.4% |
| `pause_expiry.go` | `pauseExpiryTick` / `pauseHoldReasons` / `pauseActiveSnapshot` | 84.2% / 100% / 100% |

### 3.2 组合矩阵（本轮核心产出）

| # | 组合维度 | 规模 | 结果 |
|---|---|---|---|
| 1 | 七条自动原因**任意子集**：挂上去 → 逆序逐条摘除 | **128 种组合**，每步校不变量 | 全绿 |
| 2 | 定时暂停到期 × 每条原因**单独**挡着 | 7 | 全绿 |
| 3 | 休息中（配“正常提醒”）× 自动暂停 × 定时到期，恢复时不平移 | 三维交叉 | 全绿 |
| 4 | 自动原因「条件消失即恢复」× 定时暂停 | idle / 应用占用两条 | 全绿 |
| 5 | 忽略表 × 别的自动原因 × 到期闸门 | 勿扰点恢复 → 投影照常 → 到期仍挡 → 条件消失自愈 | 全绿 |
| 6 | 询问窗 × 手动暂停 | 1 | 全绿 |
| 7 | 外卖空闲门 × 暂停（只 idle / 忙原因 × 推迟 / 跳过） | 3 | 全绿 |

各组的**判据与理由**：

1. **原因子集矩阵**（`TestPauseReasonSubsetMatrix`）
   五条监视链路（勿扰 / 投屏两条 / 全屏两条 / 应用占用 / 空闲）互不通信，真实场景里两三
   条同时成立是常态（开会时既勿扰又投屏）。只要某一条的恢复写成“直接恢复”而不是“摘掉
   自己那条”，就会出现“别人还挂着它却恢复提醒”的漏窗 —— 这种漏窗只在组合下暴露。

2. **到期闸门矩阵**（`TestTimedPauseExpiryHoldMatrix`）
   少一条进 `pauseHoldReasons`，到点就会在投影仪上弹提醒；多算一条（例如把不存在的键算
   进去），到期就永远不恢复。

3. **三维交叉**（`TestTimedPauseExpiryKeepsRemindBreakRunning`）
   三方各有一条规则，叠起来互相打脸：自动暂停闯进来时“正常提醒”的休息不收窗且不冻钟；
   到期时自动原因仍成立要保持暂停；恢复时 `BreakWhilePaused` 那次休息**不平移**。
   错一处分别是：休息被吞 / 提醒弹在会里 / 白送一段休息。

4. **自动恢复不越权**（`TestAutoResumeOnClearDoesNotEndTimedPause`）
   人离开 → idle 挂上；人回来 → idle 摘自己那条。若写成“恢复了”，用户回到座位就被提醒，
   而他明明设了 30 分钟。

5. **忽略表与到期闸门是两个问题**（`TestExpiryGateTreatsIgnoredConditionAsBlocking`）
   忽略表答“别因为它**开始**暂停”；闸门答“此刻**恢复**会不会打扰”。二者不共享答案，也不能
   互相覆盖。行为：到期被勿扰挡住 → 关掉勿扰后下一拍自动恢复，不需要再点一次。

6. **询问窗 × 手动暂停**（`TestManualPauseDismissesAskWindow`）
   都已经暂停了，还问“要不要暂停”？窗口必须立刻收，而不是走完 12 秒再叠一条原因。

7. **外卖空闲门 × 暂停**（`takeout_pause_matrix_test.go` 三例）
   只挂 idle 的暂停**不算拦阻**（人离开够久正是空闲门要等的那一刻，否则「空闲门 15 分钟 +
   系统空闲 5 分钟自动暂停」这个组合永远触发不了）；忙原因（勿扰 / 手动 / 投屏 / 应用占用）
   则由外卖卡的“暂停休息时”接管。

### 3.3 页面层与端到端探针（本轮复跑）

| 探针 | 断言 | 结果 |
|---|---|---|
| `prebreak_page_probe.js`（**本轮新增**） | 51 | 全绿 |
| `probe_fs_ask.py --only B`（**本轮补计时口径 + 窗口属性**） | 21 | 全绿 |
| `probe_fs_ask.py` 全套（**追加 Z 序打压与窗口属性**，见 4.D6、4.D7） | **83** | 全绿 |
| `probe_pages_sweep.py`（七页面全谱） | 80 | 全绿 |
| `probe_gui_boot.py`（真 GUI 冷启动） | 17 | 全绿 |
| `probe_pause_autopause.py`（自动暂停链路） | 15 | 全绿 |

`probe_fs_ask.py` 的 B 组原来只断言“日志里出现了结案”，定时器即使跑成 25 秒也照样绿。
本轮补两条：

- **实测墙钟**：从看到询问窗到日志出现「询问超时，按暂停处理」= **11.94 秒**（2026-11-23
  19:44 真机，200% 缩放）。断言区间 `[11.0, 15.5]`，下限是发现延迟（≤0.3s）的余量，
  上限挡住“结案了但晚得多”（比如要等下一拍才结算）。
- **同一段不再弹**：结案后靶子仍立在屏上（全屏没结束），观察 10 秒不许出现第二扇询问窗。
  这条钉的是“每 6 秒弹一次死循环”那个入口 —— 段键或守卫任一失灵就会复现。

等待期间由探针自己保持前台（不调 `wait_any_log`）：前台一旦被终端拿走，QUNS 会从 2 掉回 5，
这一段全屏在软件眼里就结束了，结案文案会变成“条件消失”，计时断言随之失去意义。

`prebreak_page_probe.js` 覆盖七组形态：内置版式 / 自定义版式（172 高）/ **暂停态** /
暂停后恢复 / 未勾“允许推迟” / 名字取到上限 30 字 / 底色深浅两档。判据里包括
「暂停态没有『暂停计时』」「暂停态整行倒计时收起」「30 字名字不把按钮挤出 400×172」
以及四条按钮的 `preBreakAction` 回传值。

---

## 4. 本轮发现并修复的缺陷

### D1 提前提示窗上「暂停计时」在暂停态按下去是**恢复**（组合缺陷）

- **现象**：那条提醒配“正常提醒”时，暂停中提前提示窗会被留着（`clearForAutoPause` 的
  `keepPre` 分支），窗上四个按钮照旧，其中「暂停计时」走 `schedPauseToggle` —— 已在暂停里
  再按一下就是**恢复**，而且会把勿扰 / 投屏那些自动原因记成“用户已忽略”。
- **为什么单点测不到**：按钮文案、动作回传、暂停态文案各自都有测试且全绿；错的是“这个按钮
  在**这个状态**下该不该存在”。
- **修复（两道防线）**
  1. `prebreak.js`：暂停态 `pauseBtn.classList.add("hidden")`，恢复那一支由 `applyVariant`
     重新决定（不会留成永久隐藏）。
  2. `prebreak.go` 的 `preBreakAct("pause")`：已在暂停中直接空操作并记一行日志 ——
     旧窗口、自动化调用、键盘都绕得过页面那一层。
- **判据**：`TestPreBreakActionPauseIsOneWay`（原 `…PauseToggles` 按新口径重写）、
  `TestPreBreakPauseActionIdempotentWhilePaused`、`prebreak_page_probe.js` 的 C/D 两组。

### D2 暂停态窗口上留着「还有 --」

- **现象**：暂停时把秒数改成 `--`、后缀清空，但 `还有` 是 HTML 里的静态文字，屏幕上留下
  “还有 --”这么一句半截话，看着像坏了。
- **修复**：暂停态整行 `#pb-countdown` 收起，恢复时再显示。
- **判据**：`prebreak_page_probe.js` C 组「暂停时整行倒计时收起」+ D 组「恢复后倒计时行回来」。

### D3 外卖「等空闲」的挂起被暂停处置漏清（连锁缺陷）

- **现象**（两条后果都真实）：
  1. 到点后键鼠还在动 → 挂起「等待空闲」；此时手动 / 勿扰暂停接管（推迟或跳过）。
     挂起不会被清 —— 状态卡一直写“等待空闲”，而外卖其实已经排进推迟线了；
  2. 推迟线走完之后那份挂起还在，下一次空闲达标被 `armGatedTakeout` 捡起来，把
     **已经提醒过**的那一笔按当初的时刻再弹一遍。
- **更深的根因**：`armGatedTakeout` 排在暂停判定**之前**。忙原因暂停时它照样把挂起 arm 成
  `{Armed:true, Count:0}`，而随后的处置见“已有推迟线”就什么都不做 —— 连“跳过”也清不掉它，
  恢复后照样弹。即“跳过没跳过、推迟也没按推迟时长算”。
- **修复**
  1. 新增 `takeoutPauseTakeover(cfg, pp)`：在 **arm 之前**判断这一拍会不会被暂停处置接管
     （`remind` 不接管；只有 idle 一条原因且已在等 / 已 armed 也不接管）。
  2. 接管时：不 arm → 取下挂起 → **把处置落到挂起这一笔上**（它已被空闲门记账消费，
     扫“本分钟到点的条目”那张表里已经找不到它）→ 推迟则新建 `Count=1` 的推迟线、
     跳过则记一次 `skip` 记账并收掉预通知窗。
- **判据**：`TestTakeoutIdleGateHeldThroughIdleOnlyPause`、
  `TestTakeoutGatedClearedByPauseDisposition`、`TestTakeoutGatedClearedBySkipWhilePaused`
  （三条都先红后绿，红的就是上面两条后果）。

### D4 询问窗开着时手动暂停不收窗（上一轮已修，本轮补组合用例）

- 已在 `schedPauseToggle` 的“手动暂停”分支加 `fsAskDismissSoft`，本轮补
  `TestManualPauseDismissesAskWindow` 钉住“不该叠出一条『非独占全屏』原因”。

### D5 `probe_name_extremes` 的「牌底不压对话框」判据混坐标系（探针缺陷，非产品缺陷）

长期偶发红：`极端全景 [takeout@880x560] 1字` 报 `bottom=397 dialog=406.23`，间隙 9.23px
卡在 9.5px 阈值上，余量只有 0.27px —— 一次渲染抖动就红。

根因不是抖动，是**两个坐标系相减**：

| 量 | 来源 | 坐标系 |
|---|---|---|
| 牌底 `lBottom` | `offsetParent.top + offsetTop + offsetHeight` | 布局（不含 transform / scale） |
| 对话框顶 `dialogTop` | `getBoundingClientRect().top` | 渲染（含 scale / translate） |

同一对元素，布局口径量出 9.23px，渲染口径量出 **14.56px** —— 差 5.33px 全是坐标系差值，
被 9.5 这个魔数盖住了。

修法：`name_extremes.js` 新增 `rBottom`（`getBoundingClientRect().bottom`，与 `dialogTop` 同源），
断言改成 `rBottom <= dialogTop - 11.5`（RT_GAP 12 口径，留 0.5 浮点余量）；
`clamped_bottom`（放宽牌心断言的开关）同步换渲染坐标，阈值放到 16.5。
**缺 `rBottom` 时直接报错、不再退回旧判据** —— 一条已知错误的断言不许继续充数。

验证：全量 **12741 项 / 0 失败**，最小渲染间隙 **14.56px**（余量 0.27 → 3.06px），
两次连跑数值一致。产品侧不需要改：实际视觉间隙 14.56px 比设计口径 12 更松，没有压到对话框。

### D6 询问窗 Z 序守卫：从「每 480ms 无条件抬」改成「采样到被盖住才抬」

原来的做法是每 4 拍（约 480ms）无激活重抬一次，两个方向都不对：

- **正常情况下白发**：每秒 2 次 `SetWindowPos`，每次都广播 `WM_WINDOWPOSCHANGED`；
- **真被压住时要等**：最坏 480ms 里用户看不见这扇窗，点击直接穿透到底下的全屏内容。

改成每拍（120ms）先采样、只有被盖住才抬。判据与探针完全一致：取窗自己矩形**中心**那一点
问系统“归谁”（`WindowFromPoint` + `GetAncestor(GA_ROOT)`），顶层祖先不是自己就是被盖住。
用中心点而非四角，是因为询问窗是圆角卡片，四角落在透明区域，`WindowFromPoint` 会穿透。

三处易错点都用判据钉住了：

| 易错点 | 后果 | 判据 |
|---|---|---|
| `POINT` 按值传参写成传指针 | 静默读到别的内存，判据恒错 | `TestFSAskOccludedLogic` 的注入点验证 + 探针实测 |
| 忘记取顶层祖先 | WebView2 渲染子是子窗，永远判“被盖住” → 每拍抬窗 | 同上「命中自己的子窗不算被盖住」 |
| 把“问不到归属”当成“被盖住” | 每拍白发 `SetWindowPos` | 同上「这点上没有窗口：保守当成没被盖住」 |

真机验证（`probe_fs_ask.py` C 组新增）：询问窗弹出后主动把全屏靶子重新顶到 TOPMOST 组顶
（同为 TOPMOST 时后抬者在上），测量抬回延迟 ——

```
ok   C · 被全屏应用压住后能自己抬回来  | 恢复 103ms
ok   C · 抬回延迟 ≤700ms（守卫 120ms 一拍）  | 103ms
```

103ms ≈ 一个 tick。判据上限压到 700ms 是有意的：放宽到 480ms 就分不出新旧实现，等于没测
这段改动。

### D7 询问窗的 ExStyle 少了一位 Wails 默认位（审计如实结论：**补了，但不是用户可见缺陷**）

`showFSAskWindow` 里 `Windows.ExStyle` 是**整份替换**（Wails 只在它非零时覆盖自己算好的
默认值），所以清单必须与上游逐位对齐。对照 `webview_window_windows.go` 的 `run()`：

```go
exStyle := WS_EX_CONTROLPARENT
if BackgroundType != BackgroundTypeSolid {
    if IgnoreMouseEvents { TRANSPARENT|LAYERED } else { NOREDIRECTIONBITMAP }
}
if AlwaysOnTop     { TOPMOST }
if HiddenOnTaskbar { TOOLWINDOW } else { APPWINDOW }
if options.Windows.ExStyle != 0 { exStyle = options.Windows.ExStyle }
```

我们那份清单补齐了 TOPMOST / TOOLWINDOW / CONTROLPARENT，**漏了 `WS_EX_NOREDIRECTIONBITMAP`**
（`0x00200000`）—— 它是 `BackgroundTypeTranslucent` 那条分支加的位。

**如实说明：这一位缺失没有造成可见后果。** 我原本推断“没有它就没有 DirectComposition 合成，
圆角外会变成一块实底”，并照这个推断做了对照组实验：临时去掉该位构建，跑真机 B 组 ——

| 断言 | 去掉该位 | 补上该位 |
|---|---|---|
| `ExStyle` 含 `NOREDIRECTIONBITMAP` | **FAIL**（`0x08010188`） | ok（`0x08210188`） |
| 圆角外像素 = 靶子底色 | **ok**（读到 `(0,120,215)`，正是靶子色） | ok（同上） |

也就是说 Win11 的 DWM 在没有这一位时仍然正确合成了透明区域。**推断错了，测量纠正了它。**

处置：**保留补齐**（与上游默认位一致，少一层重定向表面的开销，无副作用），但按“与上游默认
对齐”记，不夸大成缺陷。这个遗漏真正有价值的是**方法上的**：它暴露了“整份替换”这类清单
没有任何判据盯着 —— 于是补了两层：

1. **单测盯声明**：`fsAskWindowOptions` 从 `showFSAskWindow` 里抽成纯函数，`TestFSAskWindowOptions`
   复刻一份 Wails 的默认位构造（`wailsDefaultExStyle`，刻意照抄数值而非引用上游常量），断言
   “我们的 ExStyle 是它的超集”，并断言多出来的位只能是 `NOACTIVATE`。上游哪天加新默认位，
   这条会红，逼人回来核对。**对照组已验证它能抓住**：去掉那一位 → `ExStyle 漏掉 Wails 默认位 0x200000`。
2. **探针盯系统实际收到的位**：B 组新增 6 条 —— 五个必须有的 `WS_EX_*`、不得含 `APPWINDOW`
   （否则出现在任务栏）、几何是 420×200 DIP（按该窗 DPI 换算）、圆角外透出全屏画面。

抽出纯函数后 `fsAskWindowOptions` 与 `fsAskArmTimeout` 都是 100%。`showFSAskWindow` 内部仍是
24%：建窗那段需要 `application.App`，而 `serverMode` 是**编译期常量**（`mode_server.go`），
带 `-tags server` 跑测试时它恒为 true、函数在第一道闸门就返回；不带 tag 整包其他用例会因缺
`application.App` 而 panic。试过在测试里 `application.New()` —— 能拿到窗口对象，但没有消息
循环时 `NativeWindow()` 返回 0，验不到真实句柄。**这条路径只能是探针的活**，不硬开后门
（后门一开，测的就不是生产配置了）。

---

## 5. 判定为「符合预期」的刻意行为（别当成 bug 改掉）

| 行为 | 为什么合理 |
|---|---|
| 定时暂停到期只摘 `manual`，其余原因按各自策略（勿扰 / 全屏 / 两条投屏条件消失也不自动恢复） | 到期是“用户事先授权的结束”，不是“替用户判断条件”。条件还在时恢复等于把提醒弹到会上；托盘会点名在等什么，且随时可点恢复 |
| 到期闸门**不理会**忽略表 | 忽略表答“别开始暂停”，闸门答“此刻恢复会不会打扰”。二者是两个问题；关掉勿扰后下一拍自动恢复，不需要再点一次（`TestExpiryGateTreatsIgnoredConditionAsBlocking`） |
| 侧栏状态格跨天的那次**整格不渲染** | 2026-11-18 第三次定稿：跨天只留给卡片摘要与今日时间线，侧栏只报当天 |
| 暂停不冻钟（唯一例外：正在进行且自己选“推迟”的那次休息） | 2026-11-12 用户口径；暂停期间到点的那一跳由各卡“暂停休息时”处置 |
| 720/730 宽时 general/shortcuts/timing 有 3–13px `clipOver` | 产品最小窗宽 760，740 起为 0，不构成缺陷（`probe_window_size` 的 WIDTH_SWEEP 记录） |

---

## 6. 剩余风险与待观察

### 6.1 本轮已收口的三项（原来列在这里）

| 项 | 原状态 | 处置 | 判据 |
|---|---|---|---|
| 询问窗 Z 序 | 每 480ms 无条件重抬：正常时白发 `SetWindowPos`，真被压住时最坏 480ms 看不见 | 改成每拍采样、**只有被盖住才抬**（见 4.D6） | 真机打压 **103ms** 抬回；`TestFSAskOccludedLogic` 七条子用例钉中心点/顶层祖先/问不到三种情形 |
| 原生窗口函数覆盖率低（`fs_ask_window.go` 按函数平均 55.7% / `fs_ask_apply.go` 68.3%） | 低覆盖全压在“必须真窗口”的函数上，等于这段没有自动回归 | 把可判定的部分抽出来：`fsAskBounds`（纯几何）、`fsAskWindowOptions`（窗口声明）、`fsAskArmTimeout` / `fsAskArmTimeoutAfter`（超时可注入）、`fsAskResolveGeo`（自家窗折算）、`fsGuardPlan`（决策表）、`fsAskOccluded`（注入 Win32） | 这七项**全部 100%**（本轮再加 `fsAskWindowOptions` 与 `fsAskArmTimeout`），两个文件的按函数平均升到 **87.8% / 90.3%**；总覆盖 74.6% → **75.0%**；`fsAskArmTimeoutAfter` 三条分支（到点/迟到 seq/已答复）各有用例 |
| 热键测试键位预算耗尽 | 每起一个消息循环就永久占一组键 —— 线程不退出，`defer h.unregisterAll()` 是**跨线程注销、静默失败** | `HotkeyManager.shutdown()` 投 `WM_QUIT`；`run()` 在自己的线程上 `unregisterAll` 后才清 `threadID`；四个用例统一 `defer stopHotkeys` | `-count=3` 连跑全绿；**停用清理的对照组**立刻复现：首轮报「键位没有归还」，次轮报「挂起后仍被占用」 |

热键那项的根因值得记一笔：`defer h.unregisterAll()` 看起来在清理，实际上因为
`RegisterHotKey/UnregisterHotKey` 与注册线程绑定，从测试 goroutine 调用**什么也没做**。
所以“键位不够用”不是预算问题，是**从来没归还过**。现在这条用例末尾还会断言
「退出后同一个键能重新注册」，把“清理了”变成可观测事实。

### 6.2 仍然存在的（结构性，不该靠改代码消掉）

| 项 | 说明 | 处置 |
|---|---|---|
| `startFSAskForegroundGuard` / `showFSAskWindow` 内部仍是 0–24% | 需要真实 HWND 与 WebView2；`serverMode` 是编译期常量，测试配置里恒 true | 由 `probe_fs_ask.py` **83** 项覆盖（含 ExStyle、几何、圆角外的透明像素）；GUI 探针不进 CI 是选择，不是遗漏 |
| `pauseNoticeBounds` / `preBreakScreen` 覆盖 0% | 依赖原生显示器与窗口句柄 | 同上 |
| 定时暂停的「到 X 自动结束」只在 `title` 上 | 状态格的可见结论统一为“暂停休息”，时长与原因挂 title | 源码契约 `TestStatusCellPausedWording` 钉住，改文案会红 |
| 720/730 宽 `clipOver` | 产品最小窗宽 760，740 起为 0 | 不构成缺陷（见第 5 节） |

`probe_name_extremes` 的 0.27px 余量已在本轮修掉（见 4.D5），不再列为待观察。

---

## 7. 复现命令

```bash
# 单测与静态检查（注意必须带 -tags server，否则 nil panic）
export GOPATH=C:/Users/lwb/go GOCACHE=C:/Users/lwb/AppData/Local/go-build
/D/Programs/Go/bin/go vet -tags server ./...
/D/Programs/Go/bin/go test -count=1 -tags server ./...

# 覆盖率（语句口径；-func 每函数一行，别按块平均）
/D/Programs/Go/bin/go test -count=1 -tags server -coverpkg=./... -coverprofile=build/cover.out ./...
/D/Programs/Go/bin/go tool cover -func=build/cover.out | tail -1

# 组合矩阵单独跑
/D/Programs/Go/bin/go test -count=1 -tags server -run 'Matrix|Combo|Takeover|OneWay|Idempotent' ./cmd/workerrest/

# 页面层
node scripts/probe/prebreak_page_probe.js
python scripts/probe/probe_pages_sweep.py

# 真 GUI（先重建 exe！web/dist 是固化产物，改前端必须重新 go build）
/D/Programs/Go/bin/go build -trimpath -ldflags "-X main.appVersion=<ver> -H windowsgui -s -w" -o build/bin/rest.exe ./cmd/workerrest
python scripts/probe/probe_gui_boot.py
python scripts/probe/probe_pause_autopause.py
```

> 构建注意：`go build` 若报 `can't find export data (bufio: buffer full)`，是上一次构建被
> 打断写坏了缓存，`go clean -cache` 后重建即可（约 15 秒）。

---

## 8. 结论

- **组合条件下的行为**：七条原因的全部 128 种并存形态、暂停 × 休息相位 × 定时暂停的三维
  交叉、忽略表 × 到期闸门、空闲门 × 暂停处置 —— 全部符合约定语义，无漏窗、无早恢复、无
  重复提醒。
- **本轮修复的 6 处缺陷**（4 处产品 + 2 处探针/判据）里有 3 处是“组合下才暴露、单点测试全绿”
  的类型，说明组合矩阵这一层值得长期保留（已固化为 `combo_matrix_test.go` 与
  `takeout_pause_matrix_test.go`）。
- **三条“稳定性隐患”全部收口**：询问窗 Z 序从“每 480ms 无条件抬”改成“采样到被盖住才抬”
  （真机实测 103ms 恢复）；原生窗口函数的可判定部分全部抽成纯函数（`fsAskBounds` /
  `fsAskWindowOptions` / `fsAskArmTimeout` / `fsAskArmTimeoutAfter` / `fsAskResolveGeo` /
  `fsGuardPlan` / `fsAskOccluded` 均 100%）；热键键位被占用的根因是**从来没归还过**，
  现已由 `shutdown()` 走线程内注销真正归还。
- **另外补了一位 Wails 默认样式位**（`WS_EX_NOREDIRECTIONBITMAP`，见 4.D7），但如实说：对照
  实验证明漏掉它时透明合成照样正确，属“与上游默认对齐”而非修用户可见缺陷。这一轮真正的
  产出是把“整份替换的清单”做成可判据的（单测盯声明是上游默认位的超集，探针盯系统实际收到
  的位），而不是那个位本身。
- **发布前仍差**：提交与打包（`scripts/build/make_portable.py` 的两道闸门：tag 与干净树）。

---

## 9. 全测补充（同日 20:40–21:15）

第一版验收只跑了 6 个探针。清点后发现仓库里另有 **16 个现成探针**没跑、**10 个纯逻辑函数**
覆盖率 0、托盘菜单**只能验到“菜单弹出来了”**。补测结果如下 ——
其中两处是“探针自己把自己测红”（`probe_windows_e2e` 的时序、`probe_capture_guard` 的判据），
一处是**判据在当前架构下失效**，必须如实降级而不是报绿。

### 9.1 补跑 16 个漏列探针：≥669 项断言，0 失败

| 探针 | 断言 | 结果 | 验什么 |
|---|---|---|---|
| `probe_bindings_full.py` | 139 | 全绿 | 63 个绑定的返回结构、落盘、无头降级、元测试 |
| `probe_pages_sweep.py` | 80 | 全绿 | 7 页面资产网络层 + 全局不变量 |
| `probe_config_matrix.py` | 72 | 全绿 | 每个多值配置项真存真读 + 启动态矩阵 |
| `probe_static_audit.py` | 59 | 全绿 | 脚本集合/顺序、无内联脚本、无孤儿脚本 |
| `probe_settings_ui.py` | 48+ | 全绿 | 设置页广度 + 对话框交互 + 写入链路 + 快捷键（六脚本合跑，数字只统计到最后一段） |
| `ui_matrix_probe.js` | 42 | 全绿 | 侧栏三格 / 时间线 / 看板的组合态 |
| `probe_edge_cases.py` | 39 | 全绿 | 缺资源、不可写、并发写、超长名、100 条时刻表、落点被占 |
| `probe_idle_ui.py` | 39 | 全绿 | 空闲行双向一致 + 排版 |
| `probe_pause_cycle.py` | 37 | 全绿 | 暂停不冻钟、到点处置、恢复不平移 |
| `probe_sound_scan.py` | 27 | 全绿 | 音频扫描 / 导入 / 删除 / 不重复提示 |
| `probe_dpi_layout.py` | 24 | 全绿 | DPR 1~2.5 布局指纹一致 + 760 宽零裁切 |
| `probe_stats_file.py` | 19 | 全绿 | 统计损坏 → 备份 → 重建 → 告知 |
| `probe_crossday.py` | 11 | 全绿 | 跨零点范围自推、新数据出现、手选范围不被重置 |
| `probe_tray_e2e.py` | 33 | 全绿 | 托盘单击唤出 + 右键菜单 + **六类菜单项动作**（见 9.3） |
| `a11y_audit.js` | 判定项全过 | 全绿 | 可访问名 / label 关联 / img alt / tab 序 / 焦点线索 |
| `probe_a11y_focus.js` | 0 失败 | 全绿 | 对话框焦点进得去、跑不出、回得来 |

真机三支：

| 探针 | 断言 | 结果 |
|---|---|---|
| `probe_windows_e2e.py` | 16 | 全绿（A 短休息全流程 / B 外卖预通知+主窗 / C 全局热键） |
| `probe_window_gui.py` | 11 | 全绿（出厂尺寸 / 拖拽记录 / 重启沿用 / 关掉开关回默认） |
| `probe_capture_guard.py` | — | **无法测量**（见 9.5） |

### 9.2 补齐 10 个零覆盖纯逻辑函数

这 10 个都是 `go tool cover` 里 0% **且测试文件零引用**，不是“测不到”，是一直没人写：

| 函数 | 钉住的语义 | 补后覆盖 |
|---|---|---|
| `nextLocalMidnightMs` | 「到明天」暂停档的失效时刻：本地零点、严格晚于此刻、≤25h、日期=次日（`time.Date` 规范化顺带覆盖跨月/跨年） | 100% |
| `validWindowSize` | 一半合法一半 0 的旧残留必须整对作废 | 100% |
| `audioFilterPattern` | 过滤器项与 `sound.Exts` 同序同长（少一个 = 文件选不中） | 100% |
| `fmtTrayShort` | ceil 到秒；59.999s→1m、3599s→59m59s、3601s→1h1s 等 16 个边界 | 100% |
| `updateDirFor` | stage 副本在 update 目录内不再套一层；大小写目录名；update 里的正式 exe 要再下一层 | 100% |
| `mergeUpdateConfig` | 只补缺、不覆盖本地已有顶层键；坏 JSON 跳过不阻断；**落盘**而不只改内存；配置未就绪不 panic | 66.7% |
| `structToMap` | json tag 生效、数字走 float64、不可序列化退化成空 map | 85.7% |
| `preBreakInfoOf` | 调度器没起来时按短休息兜底（空标题会显示一扇没文字的窗） | 100% |
| `hasAutostartFlag` | 精确匹配：`--autostart=1` / `--AUTOSTART` / `--autostart-now` 都不算 | 100% |
| `settings.FullScreenSkipItems` | “此刻”包装与显式时间入口结果一致；无名单时返回空切片而非 nil | 100% |

新增 `cmd/workerrest/zero_cover_pure_test.go`（9 条）与 `internal/settings` 的 1 条。
**总覆盖率 75.0% → 75.5%**，测试函数 896 → **906**。

`mergeUpdateConfig` 停在 66.7% 的原因：剩下那条是 `MergeConfigFromDir` 返回 error 的分支，
只有“文件存在但读失败”才走到（坏 JSON 会被它自己跳过），Windows 上造这个条件得改 ACL，
不值得为 1 条语句引入权限操作。`structToMap` 的 85.7% 同理 —— 剩下的是 `json.Unmarshal`
失败分支，marshal 成功的前提下构造不出来。

### 9.3 托盘菜单：从“菜单弹出来了”改成“每一项点下去世界真的变了”

原探针只发 `WM_RBUTTONUP` 再断言 `#32768` 窗口存在 —— 菜单里 6 类动作一个都没验。
扩展做法（不猜坐标、不猜 ID）：

1. 向 `#32768` 菜单窗口发 `MN_GETHMENU(0x01E1)` 拿真实 `HMENU`（owner-draw 菜单的标准问法）；
2. 按位置 `GetMenuStringW` + `GetMenuItemID` 遍历，**按文案**定位项（含 `GetSubMenu` 展开子菜单）；
3. 点动作 = 向托盘宿主投 `WM_COMMAND`（`wParam` = 命令 ID），Wails 的 `wndProc` 就是拿
   `wParam & 0xffff` 去 `menuMapping` 查 —— 与真点一下同一条分支。

新增 27 项断言，覆盖：六个顶层项存在、子菜单四项存在、初始状态行、**立即休息 → 状态行变「休息中」
→ 同一项翻成「提前结束休息」→ 点它离开休息**、**暂停 30 分钟 → 状态行「还有 30 分钟」→
子菜单翻成「改到 30 分钟后结束」→ 恢复**、**暂停休息 → 「已暂停」→ 翻成「恢复休息」→ 恢复**、
**设置… 把隐藏的设置窗唤出**、**退出 8 秒内自行结束**。

两处坑都写进注释了：菜单 ID 是运行时分配的（实测五位数、跨重建累积），不能猜；
状态行文案**前缀不统一**（默认分支“下次短休息：约 30 分钟后”，只有休息/暂停/未安排才写“状态：”），
所以按**位置**取第一项，不按前缀匹配。

### 9.4 修掉两处“探针自己把自己测红”

**D8 `probe_windows_e2e` 外卖预通知的时序算错了对象。**
原写法 `cand = now + lead` 后用 `cand - now < 25` 判断余量 —— 这个差值恒为 60，
而真正的“槽时刻”是 `cand` 所在分钟的 **00 秒**。`now` 落在 `xx:58` 时槽只差 2 秒，
预通知还没画出来主窗就到点了。首次实跑就是这个结果：`主窗 0.0s 弹出 / 预通知 80s 内没出现`
（不是产品缺陷）。改成先算 `slot_epoch = (int(cand)//60)*60`，要求 `slot_epoch - now ≥ 25`。
复跑：预通知 **0.0s**、主窗 **34.9s**，两个时刻都确定。

**D9 `probe_tray_e2e` 两处读菜单的时机问题。** 刚关掉的菜单窗口还在销毁队列里时，
`menu_windows` 可能先枚举到它，对失效 hwnd 发 `MN_GETHMENU` 得到 0 → 读出一张空表。
修法：读到空表就重开（最多 4 次）。另外“暂停 30 分钟”只在**未暂停**时叫这个名字，
暂停后整组标签翻成“改到 X 后结束”—— 顺序改成先测定时暂停再测一键暂停。

### 9.5 `probe_capture_guard`：判据在当前架构下失效，如实降级为**无法测量**

对照组（开关=关，窗口本该整片品红）实测 **0.00%**，抓到的画面是**底下的 WorkBuddy 界面**。
两种可能必须分开，于是做了两组对照：

| 检查 | 结果 |
|---|---|
| 窗口 `IsWindowVisible` / 亲和性 | 可见=1、亲和性=0（对照组） |
| `BitBlt(GetDC(NULL))` 窗口矩形 | 品红 **0.00%**，画面 = 桌面其它内容 |
| `PrintWindow(PW_RENDERFULLCONTENT)` | 整块**黑图** |
| WebView2 命令行 | `--disable-direct-composition` **确实传进去了**（158.0.4370.33） |

根因：Wails 把提醒窗建成 `BackgroundTypeTranslucent` → 窗口带 `WS_EX_NOREDIRECTIONBITMAP`
→ **没有 DWM 重定向表面**，GDI 侧（BitBlt / PrintWindow）拿不到它的像素。与
`SetWindowDisplayAffinity` 有没有生效**无关**。也就是说这条判据在**任何** WebView2 半透明窗口上
恒红，实验组的“品红 0.00%”是假绿。

处置：探针在对照组失败时不再记 FAIL，而是打印口径失效原因 + **人工验证三步**，退出码 **3（无法测量）**。
人工步骤：勾上「共享屏幕时隐藏」→ 让一类提醒弹出 → 在 Teams/腾讯会议/OBS 的**预演画面**里看自己
→ 看不到即生效。这一步必须人做，因为“共享屏幕时对方看到什么”只能由真正的捕获管线回答。

> 已知风险（未证实）：`PrintWindow` 全黑说明像素已不在 GDI 表面。若 Chromium 158 下
> `--disable-direct-composition` 不再让帧回到窗口表面，WDA 可能只作用于一个“没有内容”的表面，
> 即**共享时隐藏实际失效**。这条只能由上面的人工步骤确认，代码侧不做猜测性改动。

### 9.6 仍未覆盖的（与本轮结论一致）

| 项 | 为什么还不测 |
|---|---|
| `probe_display_target.py` | 需要**双屏**（本机单屏）：`displayIds` 落点只能靠单测 + 一台双屏机 |
| `probe_idle_switch.py` | 需静置 15 分钟无输入才切阈值，代价与收益不匹配 |
| 声音真的播放 | `sound_host.go` 仍 0%；只能靠人耳或录回环，`sound_scan` 只验扫描/导入 |
| 更新「下载→校验→替换 exe→重启」 | 需要 https 源 + 真替换进程；现有 `replaceExe` 单测只覆盖纯逻辑 |
| 多显示器 + 混合 DPI 下询问窗定位 | 同第一行；本机单屏 200% |
