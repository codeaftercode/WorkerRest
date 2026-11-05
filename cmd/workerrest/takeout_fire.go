package main

import (
	"log"
	"time"
	"workerrest/internal/logx"
	"workerrest/internal/model"
	"workerrest/internal/plan"
	"workerrest/internal/settings"
)

// ---------- 触发与推迟（对应 main.ts 的 checkTakeoutReminders） ----------

// fireTakeout 弹出一次外卖提醒（固定时刻线、推迟线两条触发路径共用）
func (a *App) fireTakeout(slot model.TimeSlot, postponedCount int) {
	a.fireTakeoutAt(slot, postponedCount, 0)
}

// fireTakeoutAt 同上，但显式指定这次提醒归属的 occurrence 时刻（会话里「原定时刻」用它）。
// 提前通知跨午夜（23:50 提次日 00:10）时「立即提醒」要落在次日那一格，不能显示今天。
func (a *App) fireTakeoutAt(slot model.TimeSlot, postponedCount int, atMs int64) {
	log.Printf("[takeout] 触发提醒 %02d:%02d lead=%d秒 postponed=%d次",
		slot.Hour, slot.Minute, a.CfgSnapshot().TakeoutLeadSec(), postponedCount)
	a.showTakeoutWindowsAt(slot, false, postponedCount, atMs)
	a.playConfiguredSound()
}

// checkTakeoutReminders 外卖提醒检查：与休息调度不同，这里不累加时间，而是「到某一时刻
// 就触发」。每秒跑一次（按分钟匹配），因此就算某次 tick 被阻塞也不会漏掉整分钟的窗口。
// 暂停提醒期间不弹，恢复后也不补弹——时刻已经过去，补弹只是打扰。
//
// 三条触发线，谁先到谁弹：
//
//	① 推迟线：用户刚点了「推迟」，到点就重新弹一次
//	② 固定时刻线（真窗）：到「条目原定时刻」就弹（提前通知不改真实弹出时刻，
//	   只是先在 原定时刻−提前量 弹一个预通知小窗，见 showTakeoutPreNotify）
//	③ 提前通知线：在原定时刻−提前量 与 原定时刻 之间弹一次预通知小窗（提前量=0 不弹）
//
// 去重键按 occurrence（原定时刻）计，不含提前量——所以预通知与真窗不会互相串。
func (a *App) checkTakeoutReminders() {
	cfg := a.CfgSnapshot()
	if !cfg.TakeoutEnabled() {
		a.mu.Lock()
		a.takeoutPostponed = nil
		a.takeoutPreFired = nil
		a.mu.Unlock()
		// 总开关刚被关掉：已经弹出的「外卖预通知」小窗必须一起收掉。它不属于调度器
		//（owner=takeout 的 PreBreakState 不走向上的自愈判据），不在这里关就会永远挂着，
		// 而且倒计时停在 0 秒。
		a.closeTakeoutPreNotifyIfOpen()
		return
	}
	now := nowMs()
	// 推迟计划一律锁内取快照：TakeoutAction（UI 线程）会同时改写 DueAt / PreFired，
	// 锁外直接读这几个字段是数据竞争（真机上表现为「刚点的推迟被 tick 回退」）。
	// 判「过期作废」也放在同一把锁里，避免读到写了一半的状态。
	a.mu.Lock()
	if a.takeoutPostponed != nil &&
		now > a.takeoutPostponed.DueAt+takeoutPostponeGraceMs {
		log.Printf("[takeout] 推迟计划已过期，作废")
		a.takeoutPostponed = nil
	}
	hasPP := a.takeoutPostponed != nil
	var pp model.TakeoutPostponed
	if hasPP {
		pp = *a.takeoutPostponed
	}
	// 顺手修剪去重表：只留最近 3 天的 occurrence 键。键里带绝对时刻，长期运行
	//（一年几千条）不会影响正确性，但也没必要一直留着；3 天足够覆盖跨午夜的提前线。
	if len(a.takeoutFired) > 32 {
		cut := now - 3*86_400_000
		for k := range a.takeoutFired {
			if plan.OccurrenceKeyAt(k) < cut {
				delete(a.takeoutFired, k)
			}
		}
	}
	a.mu.Unlock()
	// 暂停休息期间：按「暂停休息时」的处置走 —— 正常提醒就落下去走正常的触发线；
	// 推迟提醒就把弹出时间往后推（推完仍暂停就再推）；跳过提醒则不弹提前提示小窗、
	// 也不弹提醒窗口，直接按「跳过」记账（外卖报表的取消次数）。
	// 暂停只管短 / 长休息，外卖自己按这份配置决定弹不弹。
	//
	// 例外：**自动暂停**（勿扰 / 投屏 / 全屏 / 应用占用 / 空闲）一律跳过，不看这份配置。
	// 那不是用户按的暂停，是软件判定他现在不该被打断；按「照常提醒」弹出来就是打扰，
	// 按「推迟提醒」攒到恢复后也会一次倒出来。窗口在进入自动暂停的那一刻已经收掉
	//（见 App.OnAutoPause），这里管的是暂停期间刚到点的那些。
	if a.sched != nil && a.sched.IsPaused() {
		if a.sched.AutoPauseActive() {
			a.skipTakeoutWhilePaused(cfg)
			return
		}
		switch cfg.TakeoutPauseAction() {
		case settings.PauseActionRemind:
			// 照常提醒：继续往下走下面的四条触发线
		case settings.PauseActionPostpone:
			if a.postponeTakeoutWhilePaused(cfg, hasPP) {
				return
			}
			// 「可推迟」填了 0：没有时长可推，退化成跳过（否则每跳原地打转、永远不弹）
			a.skipTakeoutWhilePaused(cfg)
			return
		default:
			a.skipTakeoutWhilePaused(cfg)
			return
		}
	}
	leadSec := cfg.TakeoutLeadSec()
	leadMs := int64(leadSec) * 1000
	// ① 推迟线：到点直接弹真窗。清空时核对 DueAt —— 期间用户可能又点了一次推迟，
	// 不能把那份新计划一起清掉。
	if hasPP && now >= pp.DueAt {
		a.mu.Lock()
		if p := a.takeoutPostponed; p != nil && p.DueAt == pp.DueAt {
			a.takeoutPostponed = nil
		}
		a.mu.Unlock()
		a.closeTakeoutPreNotifyIfOpen()
		// 推迟线：会话里的「原定时刻」= 推迟后的到点时刻（跨午夜时就是次日）
		a.fireTakeoutAt(pp.Slot, pp.Count, pp.DueAt)
		return
	}
	// ①b 推迟后的提前通知：leadSec>0 时，在推迟后的真实时刻前 leadMs 再弹一次预通知小窗
	// （倒计时指向推迟后的到点时刻）。与原始提前通知线同口径：推迟后仍需提前提醒用户。
	// 去重标记 PreFired 在锁内写（每秒 tick 都会走到这里，只弹一次）。
	if hasPP && leadSec > 0 && now >= pp.DueAt-leadMs && now < pp.DueAt {
		a.mu.Lock()
		mark := false
		if p := a.takeoutPostponed; p != nil && p.DueAt == pp.DueAt && !p.PreFired {
			p.PreFired = true
			mark = true
		}
		a.mu.Unlock()
		if mark {
			a.showTakeoutPreNotify(pp.Slot, pp.DueAt)
			return
		}
	}
	// ② 固定时刻线（真窗）：原定时刻已到就弹；③ 提前通知线：在原定时刻−提前量 之后弹一次
	var toFire []model.TimeSlot
	type preHit struct {
		slot model.TimeSlot
		at   int64 // 这次提醒真正发生的那一刻（可能已跨到次日）
	}
	var toPreNotify []preHit
	a.mu.Lock()
	for _, slot := range cfg.TakeoutSlots() {
		if !slot.Enabled {
			continue
		}
		// ② 真窗：now 命中条目「原定分钟」（leadMs=0）才弹——保持原来的「按分钟匹配」语义，
		//    不补弹过去错过的分钟；按 occurrence 去重，避免同一分钟重复弹。
		if plan.IsTimeSlotDue(slot, now, 0) {
			at := plan.SlotAlertAt(slot, 0, now) // 原定时刻（当天，用于 occurrence 去重键）
			key := plan.OccurrenceKey("takeout", at)
			if a.takeoutFired[key] {
				continue
			}
			a.takeoutFired[key] = true
			toFire = append(toFire, slot)
			continue
		}
		// ③ 提前通知窗：now+提前量 命中原定分钟那一刻弹一次（提前量=0 不进此支）；
		//    窗口随后保持显示，直到原定时刻真窗弹出或用户动作。
		//    at 必须按 now+提前量 折算：提前量把「进入提前区间」推到前一天时（23:50 提
		//    次日 00:10），occurrence 仍归次日 —— 用 now 当天算会与当天同条目的旧键相撞。
		if leadSec > 0 && plan.IsTimeSlotDue(slot, now, leadMs) {
			at := plan.SlotOccurrenceAt(slot, leadMs, now)
			key := plan.OccurrenceKey("takeout", at)
			if a.takeoutPreFired[key] {
				continue
			}
			a.takeoutPreFired[key] = true
			toPreNotify = append(toPreNotify, preHit{slot: slot, at: at})
		}
	}
	a.mu.Unlock()
	for _, slot := range toFire {
		a.closeTakeoutPreNotifyIfOpen()
		a.fireTakeout(slot, 0)
	}
	for _, h := range toPreNotify {
		a.showTakeoutPreNotify(h.slot, h.at)
	}
}

// postponeTakeoutWhilePaused 暂停休息期间的「推迟提醒」：到点不弹窗，把提醒窗口的弹出
// 时间往后推「可推迟（分钟）」；推完再到期时若仍在暂停休息状态，就继续往后推（如此往复）。
// 推迟不是吞掉：恢复后由正常的推迟线在推迟后的时刻弹出。
//
// 返回 false 表示这次没法推迟（「可推迟」填了 0，没有时长可推），由调用方退化成跳过。
// 已有一次推迟在路上时，本分钟到点的其它条目并进那一次（外卖提醒窗口只有一扇，
// 标题也不区分时刻），记进 takeoutFired 免得恢复后再补弹。
func (a *App) postponeTakeoutWhilePaused(cfg *settings.Settings, hasPP bool) bool {
	ms := int64(cfg.TakeoutPostponeMin()) * 60_000
	if ms <= 0 {
		return false
	}
	now := nowMs()
	if hasPP {
		// 锁内重新取（hasPP 只是「进来时有没有」，期间可能被 UI 的 TakeoutAction 换掉）
		a.mu.Lock()
		p := a.takeoutPostponed
		restarted := false
		if p != nil && now >= p.DueAt {
			p.DueAt = now + ms
			p.PreFired = false // 推后之后还要再走一遍提前通知线
			restarted = true
		}
		// 本分钟到点的其它条目并进这一次推迟（外卖窗口只有一扇，标题也不区分时刻）
		for _, slot := range cfg.TakeoutSlots() {
			if plan.IsTimeSlotDue(slot, now, 0) {
				a.takeoutFired[plan.OccurrenceKey("takeout", plan.SlotAlertAt(slot, 0, now))] = true
			}
		}
		a.mu.Unlock()
		if p == nil {
			// 计划已被清掉（用户点了别的动作）：本跳什么都不做，别退化成跳过记账
			return true
		}
		if restarted {
			log.Printf("[takeout] 暂停休息期间推迟后再次到点，继续推迟 %d 分钟", ms/60_000)
			// 旧的预通知窗指向已经作废的到点时刻，收掉（新的到点前还会再弹一次）
			a.closeTakeoutPreNotifyIfOpen()
		}
		return true
	}
	a.mu.Lock()
	for _, slot := range cfg.TakeoutSlots() {
		if a.takeoutPostponed != nil {
			// 期间 UI 刚点了一次推迟（hasPP 取快照时还没有）：别覆盖用户的选择，
			// 交给下一跳走「已有计划」分支
			break
		}
		if !plan.IsTimeSlotDue(slot, now, 0) {
			continue
		}
		key := plan.OccurrenceKey("takeout", plan.SlotAlertAt(slot, 0, now))
		if a.takeoutFired[key] {
			continue
		}
		a.takeoutFired[key] = true
		a.takeoutPostponed = &model.TakeoutPostponed{Slot: slot, DueAt: now + ms, Count: 1}
		a.mu.Unlock()
		log.Printf("[takeout] 暂停休息期间 %02d:%02d 到点，推迟 %d 分钟再提醒",
			slot.Hour, slot.Minute, ms/60_000)
		return true
	}
	a.mu.Unlock()
	// 这一刻没有到点的条目（推迟计划还在路上）：什么都不用做
	return true
}

// skipTakeoutWhilePaused 暂停休息期间外卖到点：**不弹**提前提示小窗、**不弹**提醒窗口，
// 直接把这一次当「不点了（跳过）」记一笔（外卖报表的「取消次数」），并消费该次 occurrence
// —— 恢复后同一分钟不再补弹。「推迟提醒」但「可推迟」填了 0 时也落到这里（没有时长可推）。
//
// 三条线口径：
//   - 推迟线：到点（DueAt）即记一次跳过并清掉推迟计划（暂停期间不补弹）；
//   - 固定时刻线：按分钟匹配（与真窗同口径），每个到点的条目各记一次；
//   - 提前通知线：暂停期间根本不弹小窗，因此不单独记账（到点那一跳已记）。
func (a *App) skipTakeoutWhilePaused(cfg *settings.Settings) {
	now := nowMs()
	ppFired := false
	var hits []model.TimeSlot
	// 推迟线到点与「本分钟条目到点」必须在同一把锁里判定并消费：
	// UI 线程的 TakeoutAction 同时会写这两张表，锁外读会读到回退前的值。
	a.mu.Lock()
	if p := a.takeoutPostponed; p != nil && now >= p.DueAt {
		a.takeoutPostponed = nil
		ppFired = true
	}
	for _, slot := range cfg.TakeoutSlots() {
		if !plan.IsTimeSlotDue(slot, now, 0) {
			continue
		}
		key := plan.OccurrenceKey("takeout", plan.SlotAlertAt(slot, 0, now))
		if a.takeoutFired[key] {
			continue
		}
		a.takeoutFired[key] = true
		hits = append(hits, slot)
	}
	a.mu.Unlock()
	skipped := len(hits)
	if ppFired {
		skipped++
		log.Printf("[takeout] 暂停休息期间推迟线到点，按跳过处理")
	}
	for _, slot := range hits {
		log.Printf("[takeout] 暂停休息期间 %02d:%02d 到点，按跳过处理", slot.Hour, slot.Minute)
	}
	for i := 0; i < skipped; i++ {
		a.statsStore().RecordTakeoutAction("skip")
	}
	if skipped > 0 {
		// 这一跳被跳过（或者推迟计划作废）：已经弹出的预通知窗不能继续挂着，
		// 否则它指向的那次提醒永远不会来，倒计时停在 0 秒。
		a.closeTakeoutPreNotifyIfOpen()
	}
}

// showTakeoutPreNotify 在外卖「原定时刻−提前量」弹出提前通知小窗（复用休息的提前提示窗，
// 但走外卖 owner：标题=提醒名、倒计时指向 targetAt（原定时刻或推迟后的到点时刻）、
// 三按钮 立即提醒/推迟提醒/跳过提醒）。targetAt=0 时按原定时刻（plan.SlotAlertAt）推算。
// 提前量=0 时不该被调用（checkTakeoutReminders 已把这条支滤掉）。
func (a *App) showTakeoutPreNotify(slot model.TimeSlot, targetAt int64) {
	cfg := a.CfgSnapshot()
	if targetAt == 0 {
		targetAt = plan.SlotAlertAt(slot, 0, nowMs()) // 原定时刻（当天）
	}
	info := PreBreakInfo{
		Owner:         "takeout",
		Custom:        true,
		Title:         plan.TAKEOUT_NAME,
		CountdownTail: " 秒",
		PostponeMin:   cfg.TakeoutPostponeMin(),
		// 背景色与休息预通知同源（通用配置 → 提醒窗口 → 背景颜色）
		BgColor: cfg.AppearanceCfg().Color,
	}
	a.mu.Lock()
	a.preInfo = info
	a.preTargetAt = targetAt
	a.preTakeoutSlot = slot
	a.mu.Unlock()
	a.ShowPreBreakWindow(info)
}

// closeTakeoutPreNotifyIfOpen 关掉「外卖预通知」小窗（不打扰正在显示的休息/自定义预通知）
func (a *App) closeTakeoutPreNotifyIfOpen() {
	a.mu.Lock()
	isTakeout := a.preInfo.Owner == "takeout"
	a.mu.Unlock()
	if isTakeout {
		a.closePreBreakWindow()
	}
}

// startTakeoutWatcher 启动外卖每秒检查（对应 上一版的 startTakeoutWatcher）
func (a *App) startTakeoutWatcher() {
	a.takeoutStop = make(chan struct{})
	ch := a.takeoutStop
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		n := 0
		for {
			select {
			case <-ch:
				return
			case <-t.C:
				// 每秒一跳：包 panic 兜底，丢一拍不丢整条循环（见 recoverguard.go）
				logx.GuardRun("takeout.tick", func() {
					a.checkTakeoutReminders()
					// 每 5 秒纠一次「窗口跑出显示器工作区」（拔掉外接屏后原窗口坐标作废）。
					// 放在 checkTakeoutReminders 之外：外卖关掉时那条会提前 return，而休息窗 /
					// 提前提示窗一样可能跑到不可见区域。
					n++
					if n%5 == 0 {
						a.ensureWindowsOnScreen()
					}
				})
			}
		}
	}()
}

// stopTakeoutWatcher 停止外卖检查（退出清理）
func (a *App) stopTakeoutWatcher() {
	a.mu.Lock()
	ch := a.takeoutStop
	a.takeoutStop = nil
	a.mu.Unlock()
	if ch != nil {
		close(ch)
	}
}
