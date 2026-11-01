package main

import (
	"log"
	"math/rand"
	"time"
	"workerrest/internal/logx"
)

const (
	// autoCheckWindow 当天第一次检测到键鼠活动后，在这么长的时间窗内随机取一刻检查
	autoCheckWindow = 3 * time.Hour
	// autoCheckRetry 到点却处于「暂停休息」时，在这么长的时间窗内再随机取一刻重试，
	// 仍暂停就再取一刻，如此反复
	autoCheckRetry = 30 * time.Minute
	// autoCheckPoll 调度轮询粒度（判断是否跨天 / 是否已有键鼠活动 / 是否到点）
	autoCheckPoll = 2 * time.Second
)

// 时间源与随机源注入点：生产直接用真实实现，单测换成假时钟/定值。
var (
	autoUpdateNow   = time.Now
	autoUpdateSleep = time.Sleep
	// autoUpdateRand 在 [0, max) 里取一个随机时长
	autoUpdateRand = func(max time.Duration) time.Duration {
		if max <= 0 {
			return 0
		}
		return time.Duration(rand.Int63n(int64(max)))
	}
)

// 调度动作
const (
	autoActNone  = ""      // 继续等
	autoActCheck = "check" // 现在去检查更新
)

// autoUpdateState 自动检查更新的调度状态（只保存「当天」这一份，跨天整体重置）。
type autoUpdateState struct {
	day     string    // 本次调度所属的本地日期
	armedAt time.Time // 该日期内的目标时刻；零值＝还没排期（在等当天第一次键鼠活动）
}

// autoUpdateTick 推进一次调度状态机，返回这一拍该做什么。规则：
//  1. 当天首次检测到键鼠活动 → 在 0~3 小时内随机取一个时刻（armedAt）；
//  2. 到点时若不在「暂停休息」状态 → 去检查更新；
//  3. 到点时正处于「暂停休息」→ 在 0~30 分钟内再随机取一个时刻，到点再判，反复；
//  4. 跨天（含排期或重试期间跨过零点）一律整体重置，当天有没有查成都不影响第二天；
//  5. 当天已经查过（done）→ 不再排期。
//
// 纯逻辑，不碰时钟、不碰 IO，时间由参数给、随机由 randDur 给，方便单测。
func autoUpdateTick(st *autoUpdateState, today string, now time.Time,
	active, paused, done bool, randDur func(time.Duration) time.Duration) string {
	if st.day != today {
		*st = autoUpdateState{day: today}
	}
	if done {
		st.armedAt = time.Time{}
		return autoActNone
	}
	if st.armedAt.IsZero() {
		// 还在等这一天的第一次键鼠活动；没活动就不排期
		if !active {
			return autoActNone
		}
		st.armedAt = now.Add(randDur(autoCheckWindow))
		return autoActNone
	}
	// 系统时钟被往回调（手动改时间 / 校时）会把已排的 armedAt 推到很远的未来，
	// 那一整天的自动检查就永远等不到。超过本日窗口 + 5 分钟就当作失效、重新排期。
	if st.armedAt.After(now.Add(autoCheckWindow + 5*time.Minute)) {
		st.armedAt = now.Add(randDur(autoCheckWindow))
	}
	if now.Before(st.armedAt) {
		return autoActNone
	}
	if paused {
		st.armedAt = now.Add(randDur(autoCheckRetry))
		return autoActNone
	}
	st.armedAt = time.Time{}
	return autoActCheck
}

// startAutoUpdateCheck 实现「自动检查更新」：勾选了才跑；当天已查过则跳过（不重复联网）。
// 真正的触发时机由 autoUpdateLoop + autoUpdateTick 决定（见那里的规则说明），
// 失败不打扰用户（结果仍会推给设置页，由它显示）。
func (a *App) startAutoUpdateCheck() {
	if serverMode {
		log.Printf("[update] 无头模式不检查更新")
		return
	}
	if !a.cfg.Flag(true, "checkUpdateOnStart") {
		log.Printf("[update] 自动检查更新已关闭，跳过")
		return
	}
	if a.idle == nil {
		log.Printf("[update] 读不到系统空闲状态，跳过自动检查")
		return
	}
	if a.cfg.LastUpdateCheck() == todayStr() {
		log.Printf("[update] 今天已检查过更新，等明天再自动检查")
	}
	go a.autoUpdateLoop()
}

// lastUpdateCheckLocked 持锁读取「上次检查日期」。cfg 的读写必须持 a.mu：
// autoUpdateLoop 是后台 goroutine，主线程（设置页、其它 goroutine）会同时访问 cfg，
// 直接摸 a.cfg.LastUpdateCheck() 会与 persistUpdateResult / runAutoUpdateCheck 的写
// 竞争（race 检测已实锤），统一走这里。
func (a *App) lastUpdateCheckLocked() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg.LastUpdateCheck()
}

// autoUpdateLoop 常驻调度循环：进程活多久就转多久，跨天自动进入下一天。
func (a *App) autoUpdateLoop() {
	var st autoUpdateState
	for {
		// 用户中途关掉「自动检查更新」就不再排期（重新打开需重启软件才生效，
		// 与「开关只在启动时读一次」的既有口径一致）
		// 读 cfg 必须持 a.mu：本循环是后台 goroutine，主线程同时会读写 cfg。
		a.mu.Lock()
		enabled := a.cfg.Flag(true, "checkUpdateOnStart")
		done := a.cfg.LastUpdateCheck() == autoUpdateNow().Format("2006-01-02")
		a.mu.Unlock()
		if !enabled {
			log.Printf("[update] 自动检查更新已关闭，停止调度")
			return
		}
		today := autoUpdateNow().Format("2006-01-02")
		now := autoUpdateNow()
		action := autoUpdateTick(&st, today, now,
			a.idle != nil && a.idle.IsActive(), a.schedPaused(), done, autoUpdateRand)
		if action == autoActCheck {
			// 网络请求 + JSON 解析 + 落盘，是这条循环里唯一有实质 panic 风险的部分。
			// 被 panic 打断时不能立刻重试（一秒内连炸三次就会触发熔断），退到下一轮排期。
			if !logx.GuardRunOK("update.check", func() { a.runAutoUpdateCheck(today) }) {
				// 轮询粒度只有 2 秒，直接重试等于连炸；退一分钟再说
				log.Printf("[update] 本次自动检查异常中断，1 分钟后重新排期")
				autoUpdateSleep(time.Minute)
			}
			continue // 立刻下一拍：成功则 done 生效，失败则重新按当天规则排期
		}
		autoUpdateSleep(autoCheckPoll)
	}
}

// schedPaused 当前是否处于「暂停休息」状态（手动暂停 / 勿扰 / 指定应用占用 / 空闲
// 自动暂停都算）。此时不去检查更新，等 30 分钟内的随机时刻再试。
func (a *App) schedPaused() bool {
	return a.sched != nil && a.sched.isPaused()
}

// runAutoUpdateCheck 真正联网检查一次，并把「今天查过」落库。
func (a *App) runAutoUpdateCheck(today string) {
	st := a.checkForUpdate()
	a.publishUpdateStatus(st)
	// 当天去重：只有真正判定出结果（已是最新 / 有更新）才记今天查过了；
	// error（如网络抖动）不记，当天后面还会按规则再排一次。
	// 写 cfg 必须持 a.mu（本函数跑在 autoUpdateLoop 后台 goroutine 里），
	// 网络请求 checkForUpdate 已在上方锁外完成。
	if st.Status != "error" {
		a.mu.Lock()
		a.cfg.SetLastUpdateCheck(today)
		err := a.cfg.Save()
		a.mu.Unlock()
		if err != nil {
			log.Printf("[update] 写入上次检查日期失败: %v", err)
		}
	}
	log.Printf("[update] 自动检查: %s", st.Status)
}
