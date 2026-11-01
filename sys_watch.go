package main

import (
	"log"
	"strings"
	"time"

	"workerrest/internal/autostart"
	"workerrest/internal/windnd"
	"workerrest/internal/winproc"
)

// 系统能力监视器：空闲 / 勿扰 / 应用占用 —— 三条链路都汇聚到调度器的
// 「暂停提醒 / 恢复提醒」，与 历史源码/main.ts 的 startIdleWatcher / startDndWatcher /
// startAppPauseWatcher 逐条对齐。
//
// 判定逻辑本身抽在别处（decideIdleStep / GetDndStatus / matchAppPause），这里只负责
// 「多久看一次」与「看到之后对调度器做什么」。三个 tick 函数都是 App 的方法而不是内联
// 在 goroutine 里，就是为了让单测能直接推进一步，不必等真实的 1/3/4 秒。

const (
	// dndWatchMs 勿扰轮询间隔（与 main.ts 的 3_000 一致）
	dndWatchMs = 3000
	// appPauseWatchMs 指定应用轮询间隔（与 main.ts 的 4_000 一致）
	appPauseWatchMs = 4000
)

// 暂停原因键。idle 用英文键（上一版侧就是 'idle'，types.ts 的 pauseReasonText 认它）；
// 勿扰与应用占用在 上一版侧本来就是中文键，中文键在原样返回的映射里天然可读，保持一致。
const (
	pauseReasonIdle     = "idle"
	pauseReasonDnd      = "勿扰"
	pauseReasonFullScr  = "全屏独占"
	pauseReasonAppPause = "应用占用"
)

// startSystemWatchers 启动系统能力监视。
//
// **无头（server）模式不启动会改调度器状态的三条链路**：冒烟机上系统始终空闲（无人操作），
// 空闲监视每秒就会把调度器重新挂起，后面所有「到点触发 / 推迟重弹」的自检都会随机踩空。
// 与 上一版 `if (!SMOKE) { startIdleWatcher() }` 是同一个理由、同一个口径。
//
// 空闲**采样**本身照旧启动（只读、不改任何状态），这样无头模式下也能断言探测能力可用。
func (a *App) startSystemWatchers() {
	if a.idle == nil {
		a.idle = NewIdleState()
	}
	a.idle.Start()
	dnd := windnd.GetDndStatus()
	procs := winproc.ListProcessNames()
	dndNote := dnd.Detail
	if !dnd.Supported {
		dndNote += "（不支持，勿扰暂停不生效）"
	}
	log.Printf("[sys] 空闲=%s / 勿扰=%s / 进程=%s", a.idle.detail(), dndNote, procs.Detail)
	if serverMode {
		log.Printf("[sys] 无头模式：不启动空闲 / 勿扰 / 应用占用监视（冒烟需要确定性时间轴）")
		return
	}
	a.sysStop = make(chan struct{})
	// 把通道作为参数传下去，而不是在循环里每跳读 a.sysStop：stopSystemWatchers 会
	// close 之后把字段置 nil，若某一跳恰好读到 nil，`<-nil` 会永久阻塞，goroutine
	// 既停不下来也不再轮询（应用退出时留下一个卡住的监视器）。
	stop := a.sysStop
	// 空闲：不立刻判一次（先攒够采样），其余两条与上一版 一样启动即判一次
	go a.watchLoop(stop, "空闲", idleSampleMs, false, func() { a.idleAway = a.idleWatchTick(a.idleAway) })
	go a.watchLoop(stop, "勿扰", dndWatchMs, true, a.dndWatchTick)
	go a.watchLoop(stop, "应用占用", appPauseWatchMs, true, a.appPauseWatchTick)
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
		guardRun("sys."+name, fn)
	}
	t := time.NewTicker(time.Duration(intervalMs) * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-stop:
			log.Printf("[sys] %s监视已停止", name)
			return
		case <-t.C:
			guardRun("sys."+name, fn)
		}
	}
}

// idleWatchTick 推进一次空闲判定；入参 / 返回都是「上一次有没有判成离开」。
func (a *App) idleWatchTick(away bool) bool {
	if a.sched == nil || a.idle == nil {
		return away
	}
	cfg := a.cfgSnapshot()
	threshold := int64(cfg.IdlePauseMin()) * 60_000
	idleMs := a.idle.IdleSinceMs()
	switch decideIdleStep(a.sched.currentPhase(), away, idleMs, threshold,
		a.sched.pausedForBusyReason(), a.idle.IsActive()) {
	case idleStepAway:
		a.sched.pause(pauseReasonIdle)
		// 统计：把「从最后一次输入到现在」这段从工作时长**搬进**离开时长
		//（三重封顶在 BeginAway 里：不越过工作段起点、按当天 workMs 封顶、不跨天）
		moved := a.statsStore().BeginAway(a.idle.LastInputAtMs())
		log.Printf("[idle] 已连续无输入 %ds ≥ %ds：暂停提醒（%d ms 从工作搬进离开）",
			idleMs/1000, threshold/1000, moved)
		return true
	case idleStepBack:
		a.sched.resume(pauseReasonIdle)
		// 离开结束后内置倒计时重新开始计时（自定义提醒与固定时刻线不动，见 reanchorCountdown）
		a.sched.reanchorCountdown()
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
	cfg := a.cfgSnapshot()
	dnd, fs := windnd.Query()

	a.applyAutoPause(cfg.DndPause() && dnd.Active, pauseReasonDnd, &a.dndActiveLogged,
		"[dnd] 系统勿扰 / 专注 / 演示模式已开启："+dnd.Detail+"，暂停提醒",
		"[dnd] 系统勿扰 / 专注 / 演示模式已关闭：恢复提醒")

	a.applyAutoPause(cfg.FullScreenPause() && fs.Active, pauseReasonFullScr, &a.fsActiveLogged,
		"[fullscreen] 检测到全屏独占应用运行（"+fs.Detail+"），暂停提醒",
		"[fullscreen] 全屏独占应用已退出：恢复提醒")
}

// applyAutoPause 把一条「系统状态 → 暂停原因」的边沿变化落到调度器上。
// 只在进入 / 退出暂停的那一次打日志：状态没变就什么都不做（3 秒一跳，不能刷屏）。
// logged 指向 App 上该支路自己的标记字段，两条支路互不影响。
func (a *App) applyAutoPause(active bool, reason string, logged *bool, enterMsg, exitMsg string) {
	if active {
		a.sched.pause(reason)
		if !*logged {
			*logged = true
			log.Print(enterMsg)
		}
		return
	}
	if *logged {
		*logged = false
		log.Print(exitMsg)
	}
	a.sched.resume(reason)
}

// appPauseWatchTick 推进一次「指定应用占用」判定。
// 每跳现读内存里的配置，所以设置页改了就即时生效，保存时不需要重建定时器。
//
// 忽略会话（appPauseIgnored）：自动暂停后用户手动点「恢复休息」置位，此后目标应用
// 还在跑也不再暂停；目标应用全部退出（本轮没命中）时解除，下次再运行照常自动暂停。
func (a *App) appPauseWatchTick() {
	if a.sched == nil {
		return
	}
	cfg := a.cfgSnapshot()
	r := winproc.ListProcessNames()
	hit, matched := false, []string(nil)
	if r.Supported {
		hit, matched = matchAppPause(cfg.AppPauseCfg(), r.Names)
	}
	if hit && a.isAppPauseIgnored() {
		// 忽略会话中：应用还在跑就按兵不动（原因早已摘掉，也不该再加回去）
		return
	}
	if hit {
		a.sched.pause(pauseReasonAppPause)
		if !a.appPauseHitLogged {
			a.appPauseHitLogged = true
			log.Printf("[appPause] 命中 %s，暂停提醒", strings.Join(matched, "、"))
		}
		return
	}
	// 没命中 = 目标应用都已退出：忽略会话随之自然结束
	if a.isAppPauseIgnored() {
		a.setAppPauseIgnored(false)
		log.Printf("[appPause] 被忽略的目标应用已退出，忽略解除（下次运行照常自动暂停）")
	}
	if a.appPauseHitLogged {
		a.appPauseHitLogged = false
		log.Printf("[appPause] 目标应用已退出，恢复提醒")
	}
	a.sched.resume(pauseReasonAppPause)
}

// SysProbe 系统能力自检：空闲 / 勿扰 / 进程 / 自启动注册表。
// 诊断页与无头冒烟都靠它断言「这几条系统链路真的通了」，而不是只看日志。
func (a *App) SysProbe() map[string]any {
	idle := IdleInfo{Detail: "未初始化"}
	if a.idle != nil {
		idle = a.idle.Info()
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
		autostartInfo["wanted"] = a.cfgSnapshot().Autostart()
	}
	return map[string]any{
		"idle":       idle,
		"dnd":        windnd.GetDndStatus(),
		"fullscreen": windnd.GetFullScreenStatus(),
		"processes":  procs,
		"autostart":  autostartInfo,
	}
}
