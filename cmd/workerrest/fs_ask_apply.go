package main

import (
	"log"
	"time"

	"workerrest/internal/settings"
	"workerrest/internal/windnd"
	"workerrest/internal/winproc"
)

// 非独占全屏（QUNS=2）的接线层：把一次探测结果按配置的三档处置落到调度器与询问窗上。
//
// 与其余几条监视链路的差别只在“ask 这一档”：那三档（勿扰 / 投屏 / 独占全屏）都是
// “条件成立就暂停”，而 ask 要经过一轮问答。为了不让问答把整条链路搅乱，ask 档：
//   - **不经过 applyAutoPause**（它内部有 2 拍进入去抖，叠加会把 12 秒变成 24 秒）；
//   - 询问期间只登记条件、不暂停（计时继续走，用户还得能看见倒计时在动）；
//   - 结案那一刻用 applyAutoPauseNow 立刻落暂停，段结束时仍走 applyAutoPause(false) 的标准收尾。
//
// 锁序（务必保持）：fsAskMu 之下**绝不调用调度器**。onTick 持 s.mu 调 BreakHoldRequested
// 会来拿 fsAskMu，反过来在 fsAskMu 里调 sched.Pause 就是 ABBA 死锁。

// fsBusyApply 一拍的全部处置，按 fullscreenBusyMode 三档分派。
func (a *App) fsBusyApply(busy bool, mode, detail string) {
	if a.sched == nil {
		return
	}
	switch mode {
	case settings.FullScreenBusyModePause:
		a.fsAskDismissSoft("切换到“直接暂停”")
		a.applyAutoPause(busy, pauseReasonFullScrBusy, &a.fsBusyLogged,
			"[screen] 检测到非独占全屏（"+detail+"），暂停提醒",
			"[screen] 非独占全屏已结束：恢复提醒")
	case settings.FullScreenBusyModeAsk:
		a.fsAskTick(busy)
	default:
		// off（出厂默认）：什么都不做。但用户可能刚从 pause/ask 切过来，
		// 那一刻还挂着这条暂停或一扇悬空的询问窗，得收干净。
		a.fsAskDismissSoft("非独占全屏处置已关闭")
		if a.fsBusyLogged || a.sched.HasPauseReason(pauseReasonFullScrBusy) ||
			a.isAutoPauseIgnored(pauseReasonFullScrBusy) {
			a.applyAutoPause(false, pauseReasonFullScrBusy, &a.fsBusyLogged,
				"[screen] 检测到非独占全屏，暂停提醒",
				"[screen] 非独占全屏已结束：恢复提醒")
		}
		a.setAutoReasonActive(pauseReasonFullScrBusy, false)
	}
}

// fsAskRiskyActive 此刻能不能在屏幕上多出一扇窗。
//
// 只看“勿扰 / 独占全屏或演示 / 复制模式 / 无线投放”四条，**不能**直接复用
// pauseNoticeRiskReasons：那张表里还包含“非独占全屏”自己（它挂在暂停上时同样要压制
// 每小时提示），拿它判断会把询问永远压掉。
func fsAskRiskyActive(active map[string]bool) bool {
	return active[pauseReasonDnd] || active[pauseReasonFullScr] ||
		active[pauseReasonCastMirror] || active[pauseReasonCastWireless]
}

// fsAskTick 推进一次询问状态机（每 3 秒一拍，与勿扰同一条循环）。
//
// 自家窗要当成“没发生”：询问窗偶尔会被 Wails 顶成前台（见 fs_ask_guard.go），此时几何
// 判定看到的是一扇 420×200 的窗 —— 若照实采信，这一段全屏就在软件眼里结束，段被 reset、
// 窗被关、前台回到全屏、下一拍又弹，变成每 6 秒一轮的死循环。所以前台是自己时沿用上一拍
// 真正的前台应用。守卫会把前台还回去，这里是兜底：两道防线互不依赖。
func (a *App) fsAskTick(busy bool) {
	raw := windnd.ForegroundSnapshot()
	a.fsAskMu.Lock()
	geo, update := fsAskResolveGeo(raw, a.fsLastGeo, a.fsHasGeo)
	if update {
		a.fsLastGeo, a.fsHasGeo = geo, true
	}
	a.fsAskMu.Unlock()
	a.fsAskAdvance(busy, geo, time.Now().UnixMilli())
}

// fsAskResolveGeo 把“这一拍的原始快照”折算成“判据该采信的前台”。
//
// 第二个返回值是“要不要把这个结果记成上一拍的真实前台”。两条规则：
//   - 看不到前台（Supported=false）：原样交给状态机（它按“几何不可用”处理），不污染缓存；
//   - 看到的是自家询问窗：当成没发生，沿用上一拍的真实记录 —— 否则这一段全屏就被判结束。
//
// 抽成纯函数是因为这是“每 6 秒死循环”的兜底判据，而真机上没法让前台窗口听指挥。
func fsAskResolveGeo(raw, cached windnd.ForegroundInfo, hasCached bool) (windnd.ForegroundInfo, bool) {
	if !raw.Supported {
		return raw, false
	}
	if raw.IsSelf {
		if hasCached {
			return cached, false
		}
		return raw, false
	}
	return raw, true
}

// fsAskAdvance 一拍的实际逻辑（快照与时刻由调用方给）。
//
// 拆出来是为了能测：真机上没法让前台窗口变成“一块覆盖整屏的无边框窗”，
// 而“QUNS=2 但几何不是全屏时什么都不做”恰恰是这套判据里最要紧的一条。
func (a *App) fsAskAdvance(busy bool, geo windnd.ForegroundInfo, now int64) {
	if a.sched == nil {
		return
	}
	// 名单按当前时间过滤（“今天”的豁免跨过本地 24:00 自动失效，不需要定时器）
	skip := a.CfgSnapshot().FullScreenSkipMode(geo.Exe, now)

	in := fsAskInput{
		Supported: true,
		Busy:      busy,
		Geo:       geo,
		Risky:     fsAskRiskyActive(a.autoReasonActiveSnapshot()),
		Ignored:   a.isAutoPauseIgnored(pauseReasonFullScrBusy),
		Skip:      skip,
		Now:       now,
	}

	a.fsAskMu.Lock()
	action := a.fsAsk.step(in)
	needLog := false
	if action == fsAskStepPauseNow {
		needLog = !a.fsAsk.logged
		a.fsAsk.logged = true
	}
	seq := a.fsAsk.seq
	exe, label := a.fsAsk.exe, a.fsAsk.label
	a.fsAskMu.Unlock()

	// 条件登记先于动作：pause_notice 的压制表与定时暂停的到期判定都靠它。
	segActive := busy && (geo.IsFullscreen() || !geo.Supported)
	a.setAutoReasonActive(pauseReasonFullScrBusy, segActive)

	switch action {
	case fsAskStepAsk:
		canMark := exe != "" && !winproc.IsHostProcess(exe)
		// keepFg 用判定层认定的全屏窗句柄（geo.HWND），不是此刻的 GetForegroundWindow
		if a.showFSAskWindow(exe, label, canMark, geo.HWND) {
			a.fsAskArmTimeout(seq)
		}
	case fsAskStepPauseNow:
		a.pauseFullScrBusyNow(needLog, "全屏询问结案")
		a.closeFSAskWindow()
	case fsAskStepCloseAsk:
		a.closeFSAskWindow()
		if !segActive {
			a.applyAutoPause(false, pauseReasonFullScrBusy, &a.fsBusyLogged,
				"[screen] 检测到非独占全屏，暂停提醒",
				"[screen] 非独占全屏已结束：恢复提醒")
		}
	case fsAskStepClear:
		a.applyAutoPause(false, pauseReasonFullScrBusy, &a.fsBusyLogged,
			"[screen] 检测到非独占全屏，暂停提醒",
			"[screen] 非独占全屏已结束：恢复提醒")
	}
}

// pauseFullScrBusyNow 立刻落一条“非独占全屏”暂停（跳过拍数去抖）。
//
// 调用方必须先放开 fsAskMu：这里会拿调度器的锁（见文件头的锁序说明）。
func (a *App) pauseFullScrBusyNow(needLog bool, why string) {
	if a.sched == nil {
		return
	}
	a.setAutoReasonActive(pauseReasonFullScrBusy, true)
	a.resetAutoPauseStreak(pauseReasonFullScrBusy)
	a.sched.Pause(pauseReasonFullScrBusy)
	if needLog {
		log.Printf("[screen] 检测到非独占全屏：暂停提醒（%s）", why)
	}
}
