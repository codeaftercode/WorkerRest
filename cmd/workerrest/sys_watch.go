package main

import (
	"log"
	"strings"
	"time"
	"workerrest/internal/logx"

	"workerrest/internal/autostart"
	"workerrest/internal/idle"
	"workerrest/internal/model"
	"workerrest/internal/plan"
	"workerrest/internal/scheduler"
	"workerrest/internal/windnd"
	"workerrest/internal/winproc"
)

// 系统能力监视器：空闲 / 勿扰 / 应用占用 —— 三条链路都汇聚到调度器的
// 「暂停提醒 / 恢复提醒」，与 历史源码/main.ts 的 startIdleWatcher / startDndWatcher /
// startAppPauseWatcher 逐条对齐。
//
// 判定逻辑本身抽在别处（decideIdleStep / GetDndStatus / plan.MatchAppPause），这里只负责
// 「多久看一次」与「看到之后对调度器做什么」。三个 tick 函数都是 App 的方法而不是内联
// 在 goroutine 里，就是为了让单测能直接推进一步，不必等真实的 1/3/4 秒。

const (
	// dndWatchMs 勿扰轮询间隔（与 main.ts 的 3_000 一致）
	dndWatchMs = 3000
	// castWatchMs 投屏轮询间隔。显示拓扑变化（插线、按 Win+P、开无线投屏）是低频动作，
	// 放宽到 5 秒也够；跟勿扰同为 3 秒，是为了让「拔线 → 恢复提醒」与勿扰一样快，
	// 用户不会觉得提醒「迟钝了一下才回来」。
	castWatchMs = 3000
	// appPauseWatchMs 指定应用轮询间隔（与 main.ts 的 4_000 一致）
	appPauseWatchMs = 4000
	// appPauseExitConfirmMisses 「目标应用退出」要连续几拍都没命中才算数。
	//
	// 应用占用是唯一「条件消失就自动恢复」的易抖判据：Teams / 微信 / 企业微信更新时会
	// 短暂退出再起，正好落在 4 秒采样点上就是一次无意义的恢复 —— 提前提示窗可能跟着闪
	// 一下，而下一拍又暂停回去。两次（8 秒）足以盖住这类重启间隙，又短到不会让人觉得
	// 「应用关了半天提醒还不回来」。
	//
	// 探测失败（Supported=false）不参与计数：读不到不等于不在跑。
	appPauseExitConfirmMisses = 2
)

// 暂停原因键。idle 用英文键（上一版侧就是 'idle'，types.ts 的 pauseReasonText 认它）；
// 其余是中文键，在 pauseReasonText 的原样返回分支里天然可读，托盘提示直接可用。
// idle 键的真源在 scheduler.PauseReasonIdle（调度器用它判定「是否 busy」），这里直接引用。
//
// 「复制模式」与「无线投放」刻意分成两条原因：判据来源、开关、以及用户对它们的心智都是
// 独立的——复制模式看的是拓扑被镜像，无线投放看的是输出端是不是 Miracast。合成一个
// 「投屏」时，用户为复制模式点一次「恢复休息」，正在用的无线投放会被一起静音。
const (
	pauseReasonDnd          = "勿扰"
	pauseReasonFullScr      = "全屏独占"
	pauseReasonCastMirror   = "复制模式"
	pauseReasonCastWireless = "无线投放"
	pauseReasonAppPause     = "应用占用"
)

// autoPauseReasons 全部「自动」暂停原因，供「点恢复休息时该忽略哪些」遍历用。
// manual（手动暂停）不在其中：它不是自动判定出来的，没有「条件仍成立」一说。
//
// 判据本身在 scheduler.IsAutoPauseReason（不等于 manual 即自动）。这份白名单保留
// 在 App 侧，是因为「该忽略谁」只认已知的六条：将来万一有别的原因名混进 PauseReasons，
// 也不该被当成可忽略的自动原因。
var autoPauseReasons = []string{
	scheduler.PauseReasonIdle,
	pauseReasonDnd,
	pauseReasonFullScr,
	pauseReasonCastMirror,
	pauseReasonCastWireless,
	pauseReasonAppPause,
}

// autoResumeOnClear 条件消失时**是否自动恢复**。
//
// 与「是不是自动原因」不是同一件事，两者千万别合并成一个判据：
//
//   - 空闲：人回来了 = 条件消失就是场景结束，必须自动恢复。否则每次离开几分钟回来
//     都要去托盘点一下，提醒会停摆一整天 —— 而空闲暂停时人不在场，本来就没有尴尬可言。
//   - 应用占用：目标应用退出 = 忙完了，同样是明确的结束信号，自动恢复。
//   - 勿扰 / 全屏独占 / 复制模式 / 无线投放：条件消失 **不等于** 场景结束 —— 退出全屏
//     还可以继续开会、投屏线松一下还会接回去、勿扰可能被系统或别人随手关掉。这几条
//     一律保留暂停，等用户自己点「恢复休息」，否则提醒会在会议中途弹出来，正是自动
//     暂停要拦的那一幕。
//
// 新增自动原因时先想清楚落在哪一侧：拿不准就选「不自动恢复」（用户多点一次，
// 好过在客户面前弹一次窗）。
func autoResumeOnClear(reason string) bool {
	switch reason {
	case scheduler.PauseReasonIdle, pauseReasonAppPause:
		return true
	}
	return false
}

// startSystemWatchers 启动系统能力监视。
//
// **无头（server）模式不启动会改调度器状态的三条链路**：冒烟机上系统始终空闲（无人操作），
// 空闲监视每秒就会把调度器重新挂起，后面所有「到点触发 / 推迟重弹」的自检都会随机踩空。
// 与 上一版 `if (!SMOKE) { startIdleWatcher() }` 是同一个理由、同一个口径。
//
// 空闲**采样**本身照旧启动（只读、不改任何状态），这样无头模式下也能断言探测能力可用。
func (a *App) startSystemWatchers() {
	if a.idle == nil {
		a.idle = idle.NewIdleState()
	}
	a.idle.Start()
	dnd := windnd.GetDndStatus()
	cast := windnd.GetCastStatus()
	procs := winproc.ListProcessNames()
	dndNote := dnd.Detail
	if !dnd.Supported {
		dndNote += "（不支持，勿扰暂停不生效）"
	}
	castNote := cast.Detail
	if !cast.Supported {
		castNote += "（不支持，投屏暂停不生效）"
	}
	log.Printf("[sys] 空闲=%s / 勿扰=%s / 投屏=%s / 进程=%s",
		a.idle.Detail(), dndNote, castNote, procs.Detail)
	if serverMode {
		log.Printf("[sys] 无头模式：不启动空闲 / 勿扰 / 投屏 / 应用占用监视（冒烟需要确定性时间轴）")
		return
	}
	a.sysStop = make(chan struct{})
	// 把通道作为参数传下去，而不是在循环里每跳读 a.sysStop：stopSystemWatchers 会
	// close 之后把字段置 nil，若某一跳恰好读到 nil，`<-nil` 会永久阻塞，goroutine
	// 既停不下来也不再轮询（应用退出时留下一个卡住的监视器）。
	stop := a.sysStop
	// 空闲：不立刻判一次（先攒够采样），其余两条与上一版 一样启动即判一次
	go a.watchLoop(stop, "空闲", idle.SampleMs, false, func() { a.idleAway = a.idleWatchTick(a.idleAway) })
	go a.watchLoop(stop, "勿扰", dndWatchMs, true, a.dndWatchTick)
	go a.watchLoop(stop, "投屏", castWatchMs, true, a.castWatchTick)
	go a.watchLoop(stop, "应用占用", appPauseWatchMs, true, a.appPauseWatchTick)
	// 「已暂停多久」提示（见 pause_notice.go）：整条链路只读上面这些监视器登记的条件，
	// 不改调度器状态，所以放在同一组停止信号里即可，退出时一起收。
	go a.watchLoop(stop, "暂停提示", pauseNoticeWatchMs, true, a.pauseNoticeTick)
	// 定时暂停的到期判定（见 pause_expiry.go）：与上面共用间隔与停止信号。
	// 它是独立 goroutine，不保证排在四条监视之后跑；判定读的是各监视器最近一次登记的
	// 条件，最多滞后一个监视周期（秒级），对小时级的到期没有影响。空闲那条不在登记表里，
	// 由 pauseActiveSnapshot 单独补（看 PauseReasons 里是否还挂着 idle）。
	go a.watchLoop(stop, "暂停到期", pauseNoticeWatchMs, true, a.pauseExpiryTick)
}

// stopSystemWatchers 停止全部监视（退出清理）
func (a *App) stopSystemWatchers() {
	if a.sysStop != nil {
		close(a.sysStop)
		a.sysStop = nil
	}
	if a.idle != nil {
		a.idle.Stop()
	}
}

// watchLoop 通用轮询骨架：`immediate` 为真时先立刻判一次再进入定时（上一版的
// `void tick()` + `setInterval(tick, …)` 就是这个形状）。
func (a *App) watchLoop(stop <-chan struct{}, name string, intervalMs int, immediate bool, fn func()) {
	if immediate {
		logx.GuardRun("sys."+name, fn)
	}
	t := time.NewTicker(time.Duration(intervalMs) * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-stop:
			log.Printf("[sys] %s监视已停止", name)
			return
		case <-t.C:
			logx.GuardRun("sys."+name, fn)
		}
	}
}

// idleWatchTick 推进一次空闲判定；入参 / 返回都是「上一次有没有判成离开」。
func (a *App) idleWatchTick(away bool) bool {
	if a.sched == nil || a.idle == nil {
		return away
	}
	cfg := a.CfgSnapshot()
	threshold := int64(cfg.IdlePauseMin()) * 60_000
	idleMs := a.idle.IdleSinceMs()
	switch idle.DecideIdleStep(a.sched.CurrentPhase(), away, idleMs, threshold,
		a.sched.PausedForBusyReason(), a.idle.IsActive()) {
	case idle.IdleStepAway:
		a.sched.Pause(scheduler.PauseReasonIdle)
		// 统计：把「从最后一次输入到现在」这段从工作时长**搬进**离开时长
		//（三重封顶在 BeginAway 里：不越过工作段起点、按当天 workMs 封顶、不跨天）
		moved := a.statsStore().BeginAway(a.idle.LastInputAtMs())
		log.Printf("[idle] 已连续无输入 %ds ≥ %ds：暂停提醒（%d ms 从工作搬进离开）",
			idleMs/1000, threshold/1000, moved)
		return true
	case idle.IdleStepBack:
		// 恢复的调用分两种，取决于「紧接着的重锚会不会真的把计划推远」：
		//   - 内置倒计时（绝大多数配置）会重锚 → 用 ResumeQuiet：恢复时先弹提示窗、
		//     再被重锚收掉的话，右下角会闪一下，日志里还留一条「即将休息（剩余 0 ms）」；
		//   - 固定时刻线 / 自定义提醒不会重锚 → 用 Resume：提前提示窗该照常弹回来，
		//     静默的话这次提醒的「提前提示」就被一次离开永久吞掉了。
		if a.sched.CountdownReanchorable() {
			a.sched.ResumeQuiet(scheduler.PauseReasonIdle)
		} else {
			a.sched.Resume(scheduler.PauseReasonIdle)
		}
		// 人回来了 = 空闲这条的条件消失：解除「已手动恢复」，下次再离开照常暂停
		a.clearAutoPauseIgnored(scheduler.PauseReasonIdle)
		// 离开结束后内置倒计时重新开始计时（自定义提醒与固定时刻线不动，见 reanchorCountdown）
		a.sched.ReanchorCountdown()
		// 统计：这段离开到此为止，工作段从此刻重新起算
		a.statsStore().EndAway()
		log.Printf("[idle] 检测到键鼠活动：恢复提醒，内置倒计时重新开始")
		return false
	}
	return away
}

// dndWatchTick 推进一次系统「勿扰 / 全屏独占」判定。
//
// 两条支路共用同一次 SHQueryUserNotificationState 查询（同一个返回值的不同取值）：
//   - 2 / 4：勿扰、专注助手、演示模式 —— 开关 dndPause
//   - 3：全屏独占（真全屏游戏、播放器全屏）—— 开关 fullscreenPause
//
// 与 上一版有一处**刻意不同**：那边每一跳（3 秒）都打一条 `[dnd] active: …`，
// 勿扰开着的时候日志会被刷满；这里只在「进入 / 退出暂停」的那一次打日志。
func (a *App) dndWatchTick() {
	if a.sched == nil {
		return
	}
	cfg := a.CfgSnapshot()
	dnd, fs := windnd.Query()
	a.dndApply(dnd, fs, cfg.DndPause(), cfg.FullScreenPause())
}

// dndApply 把一次「勿扰 / 全屏独占」查询结果落到调度器上。
//
// **探测失败（Supported=false）时整拍跳过**，不是当成「条件不成立」。两者差别很大：
// SHQueryUserNotificationState 偶发调用失败（会话切换、系统忙、UAC 桌面）时，
// 若按「假」处理就会走「条件消失」分支 —— 误恢复提醒、把用户点过恢复的忽略表清掉
// （下次条件成立又挂回来，用户会疑惑「我明明点过恢复了」）、还会在日志里留下一句
// 假的「已关闭」。没有结论就什么都别做，等下一次查询成功再判。
//
// 抽出独立函数是为了让这条判据可测：真机上没法让系统 API 按需失败（测试见
// TestProbeFailureKeepsPause）。
//
// 勿扰与全屏独占共用同一次查询（同一个返回值），所以只判一次 Supported：
// 一个失败就是两个都失败。
func (a *App) dndApply(dnd windnd.DndStatus, fs windnd.FullScreenStatus, dndOn, fsOn bool) {
	if !dnd.Supported {
		return
	}
	a.applyAutoPause(dndOn && dnd.Active, pauseReasonDnd, &a.dndActiveLogged,
		"[dnd] 系统勿扰 / 专注 / 演示模式已开启："+dnd.Detail+"，暂停提醒",
		"[dnd] 系统勿扰 / 专注 / 演示模式已关闭：恢复提醒")

	a.applyAutoPause(fsOn && fs.Active, pauseReasonFullScr, &a.fsActiveLogged,
		"[fullscreen] 检测到全屏独占应用运行（"+fs.Detail+"），暂停提醒",
		"[fullscreen] 全屏独占应用已退出：恢复提醒")
}

// castWatchTick 推进一次「是否正在投屏」判定。
//
// 单独成一条链路、不并进 dndWatchTick，是因为判据来源完全不同：勿扰与全屏独占读的是
// SHQueryUserNotificationState 的同一个返回值，投屏读的是显示拓扑（QueryDisplayConfig）。
// 合在一起那个函数名就彻底名不副实了。
//
// 三种「不暂停」分在三处，别混：探测失败 → castApply 整拍跳过（不是当成没投屏）；
// 开关关着 → castWant 把该条过滤掉（另一条照常）；真没投屏 → 正常的条件不成立。
//
// 2026-11-03 起两条判据各受自己的开关管：只开「复制模式」时，无线那条即使命中也不暂停。
// 探测本身仍是一次 QueryDisplayConfig，拆的只是「信不信这一条」。
func (a *App) castWatchTick() {
	if a.sched == nil {
		return
	}
	cfg := a.CfgSnapshot()
	a.castApply(windnd.GetCastStatus(), cfg.CastMirrorPause(), cfg.CastWirelessPause())
}

// castApply 把一次投屏探测结果落到调度器上。
//
// **探测失败时整拍跳过**：QueryDisplayConfig 失败（远程会话、UAC 桌面、拓扑正在重排）
// 时 CastStatus 是 Supported=false / Active=false，按「假」处理会走两条「已结束」分支，
// 把用户点过恢复的忽略表清掉；下一次查询成功、条件仍成立，暂停又挂回来。拔插线那几百
// 毫秒正好会走到这条路。
//
// 注意这里不做「拍数去抖」：投屏的进入 / 退出都是一次性动作，真要抖也只多两条日志。
// 真正需要迟滞的是「应用占用」，见 appPauseApply。
func (a *App) castApply(cast windnd.CastStatus, mirrorOn, wirelessOn bool) {
	if !cast.Supported {
		return
	}
	// 两条判据各挂一条独立原因：用户为复制模式点一次「恢复休息」，不该把正在用的
	// 无线投放一起静音。它们本来就是两个开关、两种判据、两种心智。
	mirrorWant, wirelessWant := castWant(cast, mirrorOn, wirelessOn)
	a.applyAutoPause(mirrorWant, pauseReasonCastMirror, &a.castMirrorLogged,
		"[cast] 检测到复制模式（"+cast.Detail+"），暂停提醒",
		"[cast] 复制模式已结束：恢复提醒")
	a.applyAutoPause(wirelessWant, pauseReasonCastWireless, &a.castWirelessLogged,
		"[cast] 检测到无线投放（"+cast.Detail+"），暂停提醒",
		"[cast] 无线投放已结束：恢复提醒")
}

// castWant 两条判据各自被自己的开关放行后的结果。
// 抽成纯函数是为了在单屏机器上也能测「只开一条」——真机探测取决于当场接了什么。
func castWant(cast windnd.CastStatus, mirrorOn, wirelessOn bool) (mirror, wireless bool) {
	return cast.Mirror && mirrorOn, cast.Wireless && wirelessOn
}

// applyAutoPause 把一条「系统状态 → 暂停原因」落到调度器上，并维护忽略表与「条件是否仍成立」。
// 只在进入 / 退出暂停的那一次打日志：状态没变就什么都不做（3 秒一跳，不能刷屏）。
// logged 指向 App 上该支路自己的标记字段，各支路互不影响。
//
// 条件消失后走哪条路，由 autoResumeOnClear 决定：
//   - 自动恢复（空闲 / 应用占用）：Resume，与以前一致；
//   - 保留暂停（勿扰 / 全屏 / 投屏两条）：只清忽略、打日志，暂停原因留在调度器里，
//     等用户手动点「恢复休息」。此时 logged 保持 true —— 这条原因带来的暂停仍然在场，
//     下一跳不必再报一次「暂停提醒」。
func (a *App) applyAutoPause(active bool, reason string, logged *bool, enterMsg, exitMsg string) {
	// 条件成立与否都登记：用户点「恢复休息」时据此判断该不该记忽略
	a.setAutoReasonActive(reason, active)
	if active {
		if a.isAutoPauseIgnored(reason) {
			// 用户已经为这个原因点过「恢复休息」：条件还成立也不再挂回去
			return
		}
		a.sched.Pause(reason)
		if !*logged {
			*logged = true
			log.Print(enterMsg)
		}
		return
	}
	wasIgnored := a.clearAutoPauseIgnored(reason)
	keepPaused := !autoResumeOnClear(reason)
	if *logged {
		if wasIgnored {
			*logged = false
			log.Printf("[autoPause] %s 条件已消失，忽略解除（期间未再暂停）", reason)
		} else if keepPaused {
			// 退出全屏 / 投屏断开 / 勿扰被关掉，会议往往还在继续：不恢复，交给用户
			log.Printf("%s；按策略保持暂停，等用户手动恢复", exitMsg)
		} else {
			*logged = false
			log.Print(exitMsg)
		}
	}
	if keepPaused {
		return
	}
	a.sched.Resume(reason)
}

// appPauseWatchTick 推进一次「指定应用占用」判定。
// 每跳现读内存里的配置，所以设置页改了就即时生效，保存时不需要重建定时器。
//
// 忽略语义与其他自动原因一致（见 applyAutoPause）：用户点「恢复休息」后目标应用还在跑
// 也不再暂停；应用全部退出 = 条件消失，忽略随解除，下次运行照常自动暂停。
func (a *App) appPauseWatchTick() {
	if a.sched == nil {
		return
	}
	cfg := a.CfgSnapshot()
	acfg := cfg.AppPauseCfg()
	// 功能关着时不枚举进程：Toolhelp 快照本机约 18 ms，而这条链路 4 秒一跳，
	// 用户没开这个功能就让它在后台白跑一整天。只有「上一跳刚把它关掉、此刻还挂着
	// 原因或忽略」时才需要走完下面的清理（否则暂停会解不开）。
	if !acfg.Enabled && !a.appPauseHitLogged &&
		!a.isAutoPauseIgnored(pauseReasonAppPause) &&
		!a.sched.HasPauseReason(pauseReasonAppPause) {
		a.appPauseMissStreak = 0
		a.setAutoReasonActive(pauseReasonAppPause, false)
		return
	}
	r := winproc.ListProcessNames()
	a.appPauseApply(acfg, r)
}

// appPauseApply 把一次进程枚举结果落到调度器上。
//
// **枚举失败（Supported=false）时整拍跳过**：CreateToolhelp32Snapshot / Process32First
// 失败时 Names 是空的，按「没命中」处理等于宣称「目标应用都退出了」—— 自动恢复提醒、
// 清掉忽略表，下一拍枚举成功又暂停回去。而这条原因恰是唯一「条件消失就自动恢复」的
// 易抖判据（Teams / 微信更新时会短暂退出再起），一次失败正好落在 4 秒采样点上就是一
// 次无意义的恢复。没有结论就什么都不做。
//
// 这条跳过的副作用是「上一拍的结论继续生效」：应用其实已经退出、恰好这一拍枚举失败，
// 暂停会多挂 4 秒，下一拍成功即恢复。相比误恢复（用户正看着屏幕）这点延迟无害。
//
// 第二道防抖是「退出要连续两拍都没命中」（appPauseExitConfirmMisses）：应用更新时
// 短暂退出再起，落在采样点上就是一次无意义的恢复 + 再暂停。迟滞期间**冻结全部状态**
// —— 不改条件登记（否则定时暂停的到期判定会以为不再被挡）、不清忽略表、不动 logged，
// 表现与「还在跑」完全一致；中途又命中就把计数清零，用户感知不到这次波动。
func (a *App) appPauseApply(acfg model.AppPauseConfig, r winproc.AppProcessList) {
	if !r.Supported {
		return
	}
	hit, matched := plan.MatchAppPause(acfg, r.Names)
	if !hit {
		a.appPauseMissStreak++
		// 迟滞只对「此刻还挂着这条暂停」有意义：本来就没暂停，等第二拍不会改变任何东西，
		// 反而让忽略表的解除晚 4 秒。
		pending := a.appPauseHitLogged || a.sched.HasPauseReason(pauseReasonAppPause)
		if acfg.Enabled && pending && a.appPauseMissStreak < appPauseExitConfirmMisses {
			// 连续没命中还不够：**这一拍不改任何状态**（登记、忽略表、logged 都留原样），
			// 下一拍仍没命中才认退出。中途若又命中，streak 归零，用户看不到任何变化。
			return
		}
	}
	a.appPauseMissStreak = 0
	a.setAutoReasonActive(pauseReasonAppPause, hit)
	if hit && a.isAutoPauseIgnored(pauseReasonAppPause) {
		// 已手动恢复过：应用还在跑就按兵不动（原因早已摘掉，也不该再加回去）
		return
	}
	if hit {
		a.sched.Pause(pauseReasonAppPause)
		if !a.appPauseHitLogged {
			a.appPauseHitLogged = true
			log.Printf("[appPause] 命中 %s，暂停提醒", strings.Join(matched, "、"))
		}
		return
	}
	// 没命中 = 目标应用都已退出：忽略随条件消失解除
	if a.clearAutoPauseIgnored(pauseReasonAppPause) {
		log.Printf("[appPause] 目标应用已退出，忽略解除（下次运行照常自动暂停）")
	}
	if a.appPauseHitLogged {
		a.appPauseHitLogged = false
		log.Printf("[appPause] 目标应用已退出，恢复提醒")
	}
	a.sched.Resume(pauseReasonAppPause)
}

// ---------- 「用户已手动恢复过」的忽略表 ----------
//
// 语义（六条自动原因共用）：因某原因自动暂停 → 用户点「恢复休息」→ 该原因的条件还在，
// 也不再因它暂停；条件消失后解除，下次达成照常触发。其他原因不受影响——它们各自有
// 自己的表项，用户恢复的是「当时正在生效的那几条」。
//
// 为什么不用全局一个布尔：那样「为勿扰恢复」会把随后成立的投屏一起静音，
// 用户就再也不知道哪些还在生效。

// activeAutoPauseReasons 当前挂着的原因里属于「自动判定」的那几条（不含 manual）。
// 「用户点恢复休息时该忽略谁」就取这一份：恢复后才成立的原因不在其中，因此照常生效。
func (a *App) activeAutoPauseReasons() []string {
	if a.sched == nil {
		return nil
	}
	known := make(map[string]bool, len(autoPauseReasons))
	for _, r := range autoPauseReasons {
		known[r] = true
	}
	reasons := a.sched.StateCopy().PauseReasons
	out := make([]string, 0, len(reasons))
	for _, r := range reasons {
		// 两个判据都用上：IsAutoPauseReason 定「手动还是自动」的大类，
		// 白名单保证只认这六条已知的，别把将来混进来的怪名字当成可忽略原因。
		if known[r] && scheduler.IsAutoPauseReason(r) {
			out = append(out, r)
		}
	}
	return out
}

// allAutoReasonsCleared 当前挂着的自动原因是否**全部**「条件已消失」。
// 托盘据此在提示里补一句「可以恢复了」——这时暂停是刻意留着的，不是还在开会。
// 纯手动暂停（列表为空）返回 false：用户自己按的，不需要软件提醒他恢复。
func (a *App) allAutoReasonsCleared() bool {
	reasons := a.activeAutoPauseReasons()
	if len(reasons) == 0 {
		return false
	}
	for _, r := range reasons {
		if a.isAutoReasonActive(r) {
			return false
		}
	}
	return true
}

// setAutoReasonActive 登记「这条原因的条件此刻是否成立」（监视链路每跳更新）。
func (a *App) setAutoReasonActive(reason string, active bool) {
	a.autoPauseMu.Lock()
	defer a.autoPauseMu.Unlock()
	if a.autoReasonActive == nil {
		a.autoReasonActive = make(map[string]bool, len(autoPauseReasons))
	}
	a.autoReasonActive[reason] = active
}

// autoReasonActiveLocked 条件是否仍成立；未登记按 true。
// 默认 true 是保守方向：宁可多记一次忽略（用户点过恢复就别再挂回来），
// 也别因为漏登记而让恢复按钮点了没反应。
func (a *App) autoReasonActiveLocked(reason string) bool {
	v, ok := a.autoReasonActive[reason]
	if !ok {
		return true
	}
	return v
}

// autoReasonActiveSnapshot 「原因 → 条件此刻是否成立」的拷贝。
//
// 给 pause_notice.go 的「能不能弹窗」判定用：那里要一次看全几条危险原因，
// 逐条调 isAutoReasonActive 会反复加解锁，且在两次查询之间状态可能已经变了 ——
// 快照保证判断看的是同一时刻的同一份事实。
func (a *App) autoReasonActiveSnapshot() map[string]bool {
	a.autoPauseMu.Lock()
	defer a.autoPauseMu.Unlock()
	out := make(map[string]bool, len(pauseNoticeRiskReasons))
	for k, v := range a.autoReasonActive {
		out[k] = v
	}
	return out
}

// isAutoReasonActive 条件是否仍成立（调用方未持有 autoPauseMu）。
func (a *App) isAutoReasonActive(reason string) bool {
	a.autoPauseMu.Lock()
	defer a.autoPauseMu.Unlock()
	return a.autoReasonActiveLocked(reason)
}

// isAutoPauseIgnored 该原因是否处于「用户已手动恢复」状态。
func (a *App) isAutoPauseIgnored(reason string) bool {
	a.autoPauseMu.Lock()
	defer a.autoPauseMu.Unlock()
	return a.autoPauseIgnored[reason]
}

// ignoreAutoPause 把若干原因标记为「已手动恢复」。
//
// 跳过「条件已经消失」的那些：它们还留在 PauseReasons 里，只是因为按策略保留了暂停
// （见 autoResumeOnClear）。为它们记忽略会让下一场会议彻底静音 —— 用户点了恢复，
// 结果下一个会开场软件一声不响，那比误弹一次糟得多。
func (a *App) ignoreAutoPause(reasons []string) {
	if len(reasons) == 0 {
		return
	}
	a.autoPauseMu.Lock()
	defer a.autoPauseMu.Unlock()
	if a.autoPauseIgnored == nil {
		a.autoPauseIgnored = make(map[string]bool, len(reasons))
	}
	for _, r := range reasons {
		if !a.autoReasonActiveLocked(r) {
			continue
		}
		a.autoPauseIgnored[r] = true
	}
}

// clearAutoPauseIgnored 原因条件消失：解除忽略，回报是否真的解除过（日志与诊断用）。
func (a *App) clearAutoPauseIgnored(reason string) bool {
	a.autoPauseMu.Lock()
	defer a.autoPauseMu.Unlock()
	if !a.autoPauseIgnored[reason] {
		return false
	}
	delete(a.autoPauseIgnored, reason)
	return true
}

// resetAutoPauseIgnored 用户主动「暂停休息」：作废所有忽略，回到常规自动判定。
func (a *App) resetAutoPauseIgnored() {
	a.autoPauseMu.Lock()
	a.autoPauseIgnored = nil
	a.autoPauseMu.Unlock()
}

// SysProbe 系统能力自检：空闲 / 勿扰 / 进程 / 自启动注册表。
// 诊断页与无头冒烟都靠它断言「这几条系统链路真的通了」，而不是只看日志。
func (a *App) SysProbe() map[string]any {
	idleInfo := idle.IdleInfo{Detail: "未初始化"}
	if a.idle != nil {
		idleInfo = a.idle.Info()
	}
	procs := winproc.ListProcessNames()
	cur := autostart.ReadRunEntry()
	dis := autostart.ApprovalDisabled()
	autostartInfo := map[string]any{
		"valueName": autostart.ValueName(),
		"command":   nil,
		"exists":    cur != nil,
		"disabled":  nil,
		"wanted":    false,
	}
	if cur != nil {
		autostartInfo["command"] = *cur
	}
	if dis != nil {
		autostartInfo["disabled"] = *dis
	}
	if a.cfg != nil {
		autostartInfo["wanted"] = a.CfgSnapshot().Autostart()
	}
	return map[string]any{
		"idle":       idleInfo,
		"dnd":        windnd.GetDndStatus(),
		"fullscreen": windnd.GetFullScreenStatus(),
		"cast":       windnd.GetCastStatus(),
		"processes":  procs,
		"autostart":  autostartInfo,
	}
}
