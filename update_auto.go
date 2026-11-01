package main

import (
	"log"
	"math/rand"
	"time"
	"workerrest/internal/logx"
	"workerrest/internal/update"
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

// startAutoUpdateCheck 实现「自动检查更新」：勾选了才跑；当天已查过则跳过（不重复联网）。
// 真正的触发时机由 autoUpdateLoop + update.Tick 决定（见那里的规则说明），
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
	if a.cfg.LastUpdateCheck() == update.TodayStr() {
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
	var st update.State
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
		action := update.Tick(&st, today, now,
			a.idle != nil && a.idle.IsActive(), a.schedPaused(), done, autoUpdateRand)
		if action == update.ActCheck {
			// 网络请求 + JSON 解析 + 落盘，是这条循环里唯一有实质 panic 风险的部分。
			// 被 panic 打断时不能立刻重试（一秒内连炸三次就会触发熔断），退到下一轮排期。
			if !logx.GuardRunOK("update.check", func() { a.runAutoUpdateCheck(today) }) {
				// 轮询粒度只有 2 秒，直接重试等于连炸；退一分钟再说
				log.Printf("[update] 本次自动检查异常中断，1 分钟后重新排期")
				autoUpdateSleep(time.Minute)
			}
			continue // 立刻下一拍：成功则 done 生效，失败则重新按当天规则排期
		}
		autoUpdateSleep(update.AutoCheckPoll)
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
