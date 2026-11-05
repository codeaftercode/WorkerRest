package main

import (
	"log"
	"net/url"
	"time"
	"workerrest/internal/model"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// 自动暂停期间的「已暂停多久」提示。
//
// 从任何渠道进入自动暂停后都不再自动恢复（见 sys_watch.go 的 autoResumeOnClear），
// 于是用户可能一整天都停在「已暂停」里，自己却忘了这回事 —— 所以每满一小时在右下角
// 提醒一次。三条约束都是按真实办公场景定的：
//
// 一、**文案不许出现「休息」**。这条提示有可能被旁人看到（工位侧后方的人、临时过来
// 说话的人）。「已暂停 1 小时 · 恢复」谁看都只是一句软件状态；写成「该休息了」就成了
// 当众被人读到「他在摸鱼」。按钮同理，叫「恢复」，不叫「恢复休息」。
//
// 二、**投屏 / 全屏 / 勿扰生效时一次都不弹**。右下角的小窗在投屏时会被整屏分享出去，
// 那正是最不该出现在别人屏幕上的东西；全屏（演示 / 放片）与勿扰同理会压住画面。
// 这几条原因里任意一条的条件仍成立，就整段静默 —— 宁可不提示，也不冒这个险。
//
// 三、**压制一解除就先弹一次**。投屏刚断 / 退出全屏 / 勿扰关掉的那一刻，屏幕重新变成
// 用户自己的，正好把「已经暂停多久」告诉他，之后回到每小时一次的兜底节奏。否则一场
// 两小时的会议结束，用户还要再等一小时才见到提示。
//
// 计时锚点是调度器的 PausedAt（本段暂停开始的时刻），节流用「上一次弹出时刻」：
// 压制期间流逝的时间不会被补账成连弹，恢复后也不会立刻从头再来一遍。

const (
	// pauseNoticeWatchMs 判一拍的间隔。提示的最小粒度就是小时级，5 秒足够跟上
	// 「投屏刚断」这种状态翻转，也不必为它开一条更密的定时器。
	pauseNoticeWatchMs = 5000
	// pauseNoticeIntervalMs 兜底节奏：每满一小时提示一次
	pauseNoticeIntervalMs = 3600_000
	// pauseNoticeMinGapMs 两次提示之间的最短间隔。只用于「压制解除」那条早弹：
	// 刚弹过 30 秒又开了一次全屏再关掉，不该再弹一次。
	pauseNoticeMinGapMs = 60_000
	// 小窗尺寸与贴边留白：比提前提示窗（400×240）小得多 —— 它只是一句话加一个按钮
	pauseNoticeWidth  = 300
	pauseNoticeHeight = 76
	pauseNoticeMargin = 24
	// pauseNoticeShowMs 小窗停留多久自己退场。页面里的淡出在 6.6 秒开始（见 pause.css），
	// 这里留出余量：页面脚本没加载起来时由主进程兜底关窗，不让它赖在屏幕上。
	pauseNoticeShowMs = 7500
	// pauseNoticeWindowName 窗口名：Wails 按名字交付窗口，清理也按它查。
	// 与 URL 的 ?win= 同一个值（其余窗口都是这个约定），通用关闭分支也能按它找到。
	pauseNoticeWindowName = "pause"
)

// pauseNoticeRiskReasons 一旦条件成立就不能弹出任何新窗口的原因。
//
// 只收「屏幕可能被旁人看到 / 画面不能被压住」这四条，空闲与应用占用不在其中：
// 人不在座位上时弹窗没人看（也不尴尬），应用占用只说明某个软件在跑，屏幕还是私人的。
var pauseNoticeRiskReasons = []string{
	pauseReasonDnd,
	pauseReasonFullScr,
	pauseReasonCastMirror,
	pauseReasonCastWireless,
}

// pauseNoticeState 节流状态（只有 pauseNoticeMu 保护的那几个字段有语义）。
type pauseNoticeState struct {
	// PausedAt 这一段自动暂停的开始时刻 —— 段标识。换了段（恢复后又被挂起）
	// 一切从零开始，绝不继承上一段的节流与压制标记。
	PausedAt int64
	// ShownAt 本段自动暂停里上一次弹出的时刻（0 = 还没弹过）
	ShownAt int64
	// Suppressed 上一拍是否被「投屏 / 全屏 / 勿扰」压制 —— 用来识别压制解除的那一刻
	Suppressed bool
}

// pauseNoticeSuppressed 当前生效的自动原因里，有没有「不能弹窗」的那几条。
// active 是「原因名 → 条件此刻是否成立」（见 autoReasonActiveSnapshot）。
// 纯函数，单测直接喂表。
func pauseNoticeSuppressed(active map[string]bool) bool {
	for _, r := range pauseNoticeRiskReasons {
		if active[r] {
			return true
		}
	}
	return false
}

// pauseNoticeDue 距上次弹出（本段还没弹过则距暂停开始）是否已满一个间隔。
// 时钟被往回拨时 now-base 为负，判不成到期 —— 宁可晚一点提示，也不要在时间乱跳时弹。
func pauseNoticeDue(pausedAt, lastShownAt, now, intervalMs int64) bool {
	base := pausedAt
	if lastShownAt > base {
		base = lastShownAt
	}
	if base <= 0 || intervalMs <= 0 {
		return false
	}
	return now-base >= intervalMs
}

// pauseNoticeGapOK 距上次提示是否已经过了最短间隔（压制解除那条早弹的护栏）。
func pauseNoticeGapOK(lastShownAt, now int64) bool {
	if lastShownAt <= 0 {
		return true
	}
	return now-lastShownAt >= pauseNoticeMinGapMs
}

// pauseNoticeStep 一拍决策：这一拍要不要弹，以及推进后的节流状态。
//
// 抽成纯函数是因为「一小时 / 压制解除 / 刚弹过」这三条时序在真实时间下没法测，
// 而它们恰恰是这段逻辑里最容易写错的部分（测试见 pause_notice_test.go）。
func pauseNoticeStep(prev pauseNoticeState, pausedAt, now int64, suppressed bool) (bool, pauseNoticeState) {
	// 段标识：换了暂停段就从零开始，不继承上一段的节流与压制标记。
	// 少了这一句，「投屏中暂停 → 用户点恢复 → 一秒后勿扰又挂起」会在新一段的第一拍
	// 把上一段残留的 Suppressed 当成「压制刚解除」，立刻补弹一次（用户刚点过恢复）。
	if prev.PausedAt != pausedAt {
		prev = pauseNoticeState{PausedAt: pausedAt}
	}
	if suppressed {
		// 压制期间什么都不弹；记下「这段被压过」，解除那一刻才有得补
		return false, pauseNoticeState{PausedAt: pausedAt, ShownAt: prev.ShownAt, Suppressed: true}
	}
	// 压制刚解除 = 投屏 / 全屏 / 勿扰都结束了：这一刻屏幕重新属于用户，先补一次。
	// 但刚弹过（不到最短间隔）就不重复弹 —— 开关一次全屏不该换来两条提示。
	edge := prev.Suppressed && pauseNoticeGapOK(prev.ShownAt, now)
	if !edge && !pauseNoticeDue(pausedAt, prev.ShownAt, now, pauseNoticeIntervalMs) {
		return false, pauseNoticeState{PausedAt: pausedAt, ShownAt: prev.ShownAt}
	}
	return true, pauseNoticeState{PausedAt: pausedAt, ShownAt: now}
}

// pauseNoticeText 提示文案。只有「已暂停 + 时长」，刻意不带任何与休息有关的字眼
// （见文件头第一条）。时长沿用托盘的 fmtTrayPaused：两边说的必须是同一件事。
func pauseNoticeText(elapsedMs int64) string {
	// 不足一分钟在托盘里说「不到 1 分钟」，拼进这句话读着别扭；实际也不会走到
	//（最早也要满一小时），这里按一分钟兜底，保证任何入参出来的都是一句通顺的话。
	if elapsedMs < 60_000 {
		elapsedMs = 60_000
	}
	return "已暂停 " + fmtTrayPaused(elapsedMs)
}

// pauseNoticeBounds 小窗的位置与大小：主屏工作区右下角（与提前提示窗同一处留白）。
// 纯函数，单测直接验。
func pauseNoticeBounds(wa application.Rect) (x, y, w, h int) {
	w, h = pauseNoticeWidth, pauseNoticeHeight
	x = wa.X + wa.Width - w - pauseNoticeMargin
	y = wa.Y + wa.Height - h - pauseNoticeMargin
	return
}

// pauseNoticeURL 页面地址。文案经 query 下发，渲染层不自己拼（与提前提示窗同口径：
// 名称只有 Go 侧这一份）。中文与空格必须逐段编码，否则 WebView2 当非法字符丢掉。
func pauseNoticeURL(text string) string {
	return "/pause.html?win=pause&text=" + url.QueryEscape(text)
}

// ---------- 节流状态的读写 ----------

func (a *App) pauseNoticeSnapshot() pauseNoticeState {
	a.pauseNoticeMu.Lock()
	defer a.pauseNoticeMu.Unlock()
	return pauseNoticeState{
		PausedAt:   a.pauseNoticePausedAt,
		ShownAt:    a.pauseNoticeShownAt,
		Suppressed: a.pauseNoticeSuppressed,
	}
}

func (a *App) pauseNoticeStore(st pauseNoticeState) {
	a.pauseNoticeMu.Lock()
	// 本段暂停结束（或还没开始）时清零：下一段自动暂停从「没弹过」算起
	a.pauseNoticePausedAt = st.PausedAt
	a.pauseNoticeShownAt = st.ShownAt
	a.pauseNoticeSuppressed = st.Suppressed
	a.pauseNoticeMu.Unlock()
}

// ---------- 一拍 ----------

// pauseNoticeTick 自动暂停期间的提示判定（每 pauseNoticeWatchMs 一跳）。
//
// 只对自动暂停生效：手动暂停是用户自己按下的，他知道自己在暂停，托盘上也一直写着
// 已暂停多久，再弹窗就是替他操心。不在自动暂停里时把节流状态清零，下一段重新计时。
func (a *App) pauseNoticeTick() {
	if a.sched == nil {
		return
	}
	a.pauseNoticeAdvance(a.sched.StateCopy(), a.sched.AutoPauseActive(), nowMs())
}

// pauseNoticeAdvance 一拍的实际逻辑（调度器状态与时刻都由调用方给）。
//
// 单独拆出来是为了能测：真实时间下「满一小时」这条根本跑不到，
// 而它恰恰是这段逻辑里最容易写错的一处（见 pause_notice_test.go）。
func (a *App) pauseNoticeAdvance(st model.SchedulerState, auto bool, now int64) {
	if !st.Paused || !auto {
		a.pauseNoticeStore(pauseNoticeState{})
		return
	}
	show, next := pauseNoticeStep(a.pauseNoticeSnapshot(), st.PausedAt, now,
		pauseNoticeSuppressed(a.autoReasonActiveSnapshot()))
	a.pauseNoticeStore(next)
	if show {
		a.showPauseNotice(max0(now - st.PausedAt))
	}
}

// ---------- 小窗 ----------

// showPauseNotice 弹出（或重弹）小窗。每次先关旧的：一小时的间隔远大于窗口寿命，
// 但「压制解除补弹」可能紧跟着上一拍的时刻，不能叠出两扇窗。
func (a *App) showPauseNotice(elapsedMs int64) {
	text := pauseNoticeText(elapsedMs)
	a.pauseNoticeWinMu.Lock()
	defer a.pauseNoticeWinMu.Unlock()
	a.closePauseNoticeWindowLocked()
	if serverMode {
		log.Printf("[pauseNotice] server 模式无原生窗口，跳过创建（%q）", text)
		return
	}
	x, y, w, h := pauseNoticeBounds(application.Rect{})
	// 跟「提醒窗口显示在哪几块屏」走，取第一块（与提前提示窗同规则：固定窗口名，
	// 全局只允许一扇，多屏同时弹只会互相顶替）
	if sc := a.preBreakScreen(); sc != nil {
		x, y, w, h = pauseNoticeBounds(sc.WorkArea)
	}
	// 共享屏幕时隐藏：这句「已暂停」被同事看到不算大事，但用户既然勾了
	// 「共享屏幕时隐藏提醒窗口」，就该按同一个口径处理 —— 那扇窗出现在共享画面里
	// 同样突兀。生效前提（--disable-direct-composition）见 main.go 的注释。
	hideGuard := a.CfgSnapshot().AppearanceCfg().HideOnShare
	win := a.app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:          pauseNoticeWindowName,
		Title:         "暂停提示",
		X:             x,
		Y:             y,
		Width:         w,
		Height:        h,
		Frameless:     true,
		AlwaysOnTop:   true,
		DisableResize: true,
		// 不设它 X/Y 会被无视：Wails 的 InitialPosition 零值 = 整窗居中
		InitialPosition:          application.WindowXY,
		ContentProtectionEnabled: hideGuard,
		URL:                      pauseNoticeURL(text),
		BackgroundType:           application.BackgroundTypeTranslucent,
		BackgroundColour:         application.NewRGBA(0, 0, 0, 0),
		// 窗口身份经 URL 的 ?win=pause 下发；JS 字段对 URL 页面不生效（仅占位）
		JS: `window.__WR_WIN = "pause";`,
		Windows: application.WindowsWindow{
			// 无边框窗口在 Windows 上会留一圈原生装饰（阴影 / 描边），与其余小窗一致关掉
			DisableFramelessWindowDecorations: true,
			HiddenOnTaskbar:                   true,
		},
	})
	// 被系统 / 用户直接关掉（Alt+F4）时清掉引用，免得后续拿着已销毁的窗口去 Close
	win.OnWindowEvent(events.Common.WindowClosing, func(*application.WindowEvent) {
		a.mu.Lock()
		dropWindowRef(a.wins, pauseNoticeWindowName, win)
		a.mu.Unlock()
	})
	a.mu.Lock()
	a.wins[pauseNoticeWindowName] = win
	a.mu.Unlock()
	log.Printf("[pauseNotice] 自动暂停 %d 分钟，右下角提示一次：%q", elapsedMs/60_000, text)
	// 到点自己退场：页面里的淡出只是视觉效果，真正的关闭由主进程兜底 ——
	// 页面脚本没加载起来（WebView2 异常）时窗口也不会赖在屏幕上
	time.AfterFunc(pauseNoticeShowMs*time.Millisecond, func() { a.closePauseNoticeWin(win) })
}

// closePauseNoticeWin 关掉「确实是这一扇」的小窗。
//
// 定时器是异步的：7.5 秒后可能已经弹过下一扇（极端情况下压制反复翻转），
// 按指针比对再关，否则会关掉刚建好的新窗 —— 与其余固定名窗口同一个坑。
func (a *App) closePauseNoticeWin(win *application.WebviewWindow) {
	a.pauseNoticeWinMu.Lock()
	defer a.pauseNoticeWinMu.Unlock()
	a.mu.Lock()
	cur := a.wins[pauseNoticeWindowName]
	a.mu.Unlock()
	if cur != win {
		return
	}
	a.closePauseNoticeWindowLocked()
}

// closePauseNoticeWindow 关掉小窗（没开着时是空操作）
func (a *App) closePauseNoticeWindow() {
	a.pauseNoticeWinMu.Lock()
	defer a.pauseNoticeWinMu.Unlock()
	a.closePauseNoticeWindowLocked()
}

// closePauseNoticeWindowLocked 已持有 pauseNoticeWinMu 时用。
// 一律先摘引用再关：关窗会触发 WindowClosing，回调里再查一次 map 也无妨。
func (a *App) closePauseNoticeWindowLocked() {
	a.mu.Lock()
	w := a.wins[pauseNoticeWindowName]
	delete(a.wins, pauseNoticeWindowName)
	a.mu.Unlock()
	if w != nil {
		w.Close()
	}
}

// ---------- 绑定给渲染层的接口 ----------

// PauseNoticeResume 小窗上点「恢复」：与托盘 / 快捷键的「恢复休息」完全同一条入口。
// 它会把当时生效的自动原因逐条记为「已手动恢复」再摘掉（见 schedPauseToggle），
// 所以恢复之后这些原因的条件没消失前不会又挂回来。
//
// 收窗在下面再补一次：schedPauseToggle 只在「确实处在暂停」时才走恢复分支并顺手收窗，
// 万一状态早已翻篇（别处先恢复了），这扇窗不能就这么留在屏幕上。
func (a *App) PauseNoticeResume() {
	a.schedPauseToggle()
	a.closePauseNoticeWindow()
}

// PauseNoticeDismiss 只关提示、不恢复（用户还想继续保持暂停）。
//
// 关掉之后**不重置节流**：下一次还是按「距上次弹出满一小时」来算，否则连点几下关闭
// 就能把整条提示关掉一整天。
func (a *App) PauseNoticeDismiss() {
	a.closePauseNoticeWindow()
}
