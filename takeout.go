package main

import (
	"log"
	"strconv"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// 外卖提醒：按固定时刻弹出（不做倒计时），显示逻辑与休息窗口一致（同一套外观配置、
// 多屏策略、透明与圆角），区别只在内容。对应 历史源码/main.ts 的「外卖提醒」段 +
// 历史源码/windows/takeoutWindow.ts + raise.ts + displays.ts + reminderSync.ts。
//
// 与 上一版的两处**有意差异**（见 README「已知差异」）：
//   ① z-order：上一版 用 moveTop() 在同层（screen-saver）内「谁后抬谁在上」把外卖压到
//      休息窗口之上；Wails v3 没有暴露 z-order 抬升 API，这里降级成 SetAlwaysOnTop(true) +
//      Focus() 近似（抬升簿记 raiseState 仍保留，供诊断 / 冒烟断言）。
//   ② 系统级键鼠轮询：上一版有 powerMonitor.getSystemIdleTime() 可降级秒级；Go 侧只有
//      user32!GetLastInputInfo 一个来源，读不到 tick 时整段不启动（不假装有活动）。

const (
	// takeoutWindowBaseName 外卖窗口在 wins 里不按名存（多窗口），这里只作日志 / 诊断名
	takeoutWindowBaseName = "takeout"
	// raiseIntervalMs 抬升循环间隔：足够密集以压过异步显示的休息窗口，又不至于视觉抖动
	raiseIntervalMs = 700
	// takeoutPostponeGraceMs 推迟计划有效期：到点后若一直暂停 / 睡眠，超过此宽限即作废
	takeoutPostponeGraceMs = 5 * 60_000
	// takeoutPreNotifyGraceMs 预通知窗过了到点后的收窗宽限：正常路径由真窗弹出时关闭，
	// 这里只兜「真窗没能弹出」（暂停 / 跳过 / 总开关被关）时的残留
	takeoutPreNotifyGraceMs = 60_000
)

// ---------- 抬升簿记（对应 windows/raise.ts） ----------

// raiseKind 抬升方：休息窗口或外卖窗口
type raiseKind string

const (
	raiseKindBreak   raiseKind = "break"
	raiseKindTakeout raiseKind = "takeout"
)

// raiseState 窗口层级簿记。休息窗口与外卖窗口都要求置顶（Windows 上是 topmost 层级）。
// 记录抬升顺序与「休息窗口是否在场」，给诊断与冒烟自检一个可断言的状态。
type raiseState struct {
	mu      sync.Mutex
	last    raiseKind
	seq     []raiseKind
	breakOn bool
}

func (r *raiseState) noteRaised(k raiseKind) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.last = k
	r.seq = append(r.seq, k)
	if len(r.seq) > 40 {
		r.seq = r.seq[len(r.seq)-40:]
	}
}

func (r *raiseState) lastRaised() raiseKind {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.last
}

func (r *raiseState) raiseSequence() []raiseKind {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]raiseKind, len(r.seq))
	copy(out, r.seq)
	return out
}

func (r *raiseState) setBreaksActive(v bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.breakOn = v
}

func (r *raiseState) anyBreakActive() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.breakOn
}

// ---------- 会话构建 ----------

// buildTakeoutInfo 组装一次外卖会话（对应 takeoutWindow.ts 的 showTakeoutWindows 里
// 那个 TakeoutSessionInfo）。会话与「弹窗」分开：渲染层随后会自己再拉一次 TakeoutCurrent。
func (a *App) buildTakeoutInfo(slot TimeSlot, preview bool, postponedCount int, atOverride int64) *TakeoutInfo {
	cfg := a.cfgSnapshot()
	now := time.Now()
	// atOverride = 这次提醒真正归属的 occurrence（提前通知跨午夜时是次日那一格、
	// 推迟线是推迟后的到点时刻）；不传则按「当天该时刻」—— 与历史口径一致。
	atMs := atOverride
	if atMs <= 0 {
		atMs = slotAlertAt(slot, 0, now.UnixMilli())
	}
	// 红包二维码指向本机专属领取地址：id 取配置里那枚固定键（每台机器一枚，首次运行生成），
	// 服务端（www.goodgoodlive.com）按 id 区分来源。只要显示外卖提醒窗口就必须展示优惠券，
	// 因此这里无条件带上地址（不再有开关）。
	// t = 毫秒时间戳：每次弹窗都重新生成地址，供服务端做时效判断 / 缓存刷新，
	// 与 id 一起追加在查询串上（id 保留）。
	redData := &TakeoutRedPacket{URL: "https://www.goodgoodlive.com/cps/index.html?id=" +
		cfg.Str("", "installId") + "&t=" + strconv.FormatInt(now.UnixMilli(), 10)}
	info := &TakeoutInfo{
		ID:             takeoutID(),
		At:             atMs,
		Hour:           slot.Hour,
		Minute:         slot.Minute,
		Slot:           slot,
		FiredAt:        now.UnixMilli(),
		Preview:        preview,
		Meal:           takeoutMealLabel(slot.Hour),
		LeadSec:        cfg.TakeoutLeadSec(),
		PostponeMin:    cfg.TakeoutPostponeMin(),
		PostponedCount: postponedCount,
		Appearance:     cfg.AppearanceView(),
		RedPacketData:  redData,
	}
	// 外卖提醒自己的提醒内容：每次弹窗选 1 条；一条启用的都没有时用提醒名「外卖提醒」
	// 兜底（窗口里「本该显示提醒内容的位置」不空着）。pickMessage 返回的切片第 0 条即内容。
	if msgs := a.pickMessage("takeout", TAKEOUT_NAME); len(msgs) > 0 {
		info.Message = msgs[0]
	}
	// 督休专员：与休息窗口同一条构建路径；外卖窗口的督休专员第一段台词 = 本提醒的提醒内容。
	// 第二阶段**不换台词**：命令台词清空，渲染层 applyStage 的 `command || stage1` 沿用第一段。
	if sup := a.buildSupervisorSession(preview, ""); sup != nil {
		sup.Lines.Command = ""
		info.Supervisor = sup
	}
	return info
}

// takeoutID 生成本次外卖会话 id（对应 上一版的 `${Date.now()}-${random36}`；Go 侧用随机唯一串同样唯一）
func takeoutID() string {
	return newRandomID()
}

// ---------- 触发与推迟（对应 main.ts 的 checkTakeoutReminders） ----------

// fireTakeout 弹出一次外卖提醒（固定时刻线、推迟线两条触发路径共用）
func (a *App) fireTakeout(slot TimeSlot, postponedCount int) {
	a.fireTakeoutAt(slot, postponedCount, 0)
}

// fireTakeoutAt 同上，但显式指定这次提醒归属的 occurrence 时刻（会话里「原定时刻」用它）。
// 提前通知跨午夜（23:50 提次日 00:10）时「立即提醒」要落在次日那一格，不能显示今天。
func (a *App) fireTakeoutAt(slot TimeSlot, postponedCount int, atMs int64) {
	log.Printf("[takeout] 触发提醒 %02d:%02d lead=%d秒 postponed=%d次",
		slot.Hour, slot.Minute, a.cfgSnapshot().TakeoutLeadSec(), postponedCount)
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
	cfg := a.cfgSnapshot()
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
	var pp TakeoutPostponed
	if hasPP {
		pp = *a.takeoutPostponed
	}
	// 顺手修剪去重表：只留最近 3 天的 occurrence 键。键里带绝对时刻，长期运行
	//（一年几千条）不会影响正确性，但也没必要一直留着；3 天足够覆盖跨午夜的提前线。
	if len(a.takeoutFired) > 32 {
		cut := now - 3*86_400_000
		for k := range a.takeoutFired {
			if occurrenceKeyAt(k) < cut {
				delete(a.takeoutFired, k)
			}
		}
	}
	a.mu.Unlock()
	// 暂停休息期间：按「暂停休息时」的处置走 —— 正常提醒就落下去走正常的触发线；
	// 推迟提醒就把弹出时间往后推（推完仍暂停就再推）；跳过提醒则不弹提前提示小窗、
	// 也不弹提醒窗口，直接按「跳过」记账（外卖报表的取消次数）。
	// 暂停只管短 / 长休息，外卖自己按这份配置决定弹不弹。
	if a.sched != nil && a.sched.isPaused() {
		switch cfg.TakeoutPauseAction() {
		case PauseActionRemind:
			// 照常提醒：继续往下走下面的四条触发线
		case PauseActionPostpone:
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
	var toFire []TimeSlot
	type preHit struct {
		slot TimeSlot
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
		if isTimeSlotDue(slot, now, 0) {
			at := slotAlertAt(slot, 0, now) // 原定时刻（当天，用于 occurrence 去重键）
			key := occurrenceKey("takeout", at)
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
		if leadSec > 0 && isTimeSlotDue(slot, now, leadMs) {
			at := slotOccurrenceAt(slot, leadMs, now)
			key := occurrenceKey("takeout", at)
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
func (a *App) postponeTakeoutWhilePaused(cfg *Settings, hasPP bool) bool {
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
			if isTimeSlotDue(slot, now, 0) {
				a.takeoutFired[occurrenceKey("takeout", slotAlertAt(slot, 0, now))] = true
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
		if !isTimeSlotDue(slot, now, 0) {
			continue
		}
		key := occurrenceKey("takeout", slotAlertAt(slot, 0, now))
		if a.takeoutFired[key] {
			continue
		}
		a.takeoutFired[key] = true
		a.takeoutPostponed = &TakeoutPostponed{Slot: slot, DueAt: now + ms, Count: 1}
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
func (a *App) skipTakeoutWhilePaused(cfg *Settings) {
	now := nowMs()
	ppFired := false
	var hits []TimeSlot
	// 推迟线到点与「本分钟条目到点」必须在同一把锁里判定并消费：
	// UI 线程的 TakeoutAction 同时会写这两张表，锁外读会读到回退前的值。
	a.mu.Lock()
	if p := a.takeoutPostponed; p != nil && now >= p.DueAt {
		a.takeoutPostponed = nil
		ppFired = true
	}
	for _, slot := range cfg.TakeoutSlots() {
		if !isTimeSlotDue(slot, now, 0) {
			continue
		}
		key := occurrenceKey("takeout", slotAlertAt(slot, 0, now))
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
// 三按钮 立即提醒/推迟提醒/跳过提醒）。targetAt=0 时按原定时刻（slotAlertAt）推算。
// 提前量=0 时不该被调用（checkTakeoutReminders 已把这条支滤掉）。
func (a *App) showTakeoutPreNotify(slot TimeSlot, targetAt int64) {
	cfg := a.cfgSnapshot()
	if targetAt == 0 {
		targetAt = slotAlertAt(slot, 0, nowMs()) // 原定时刻（当天）
	}
	info := PreBreakInfo{
		Owner:         "takeout",
		Custom:        true,
		Title:         TAKEOUT_NAME,
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
				guardRun("takeout.tick", func() {
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

// ---------- 窗口（对应 takeoutWindow.ts + displays.ts） ----------

// targetDisplays 按「在所有屏幕显示」开关选出要显示的显示器（休息窗口与外卖
// 窗口共用同一策略）。取消勾选 ⇒ 只在主屏幕显示（Wails v3 无获取光标所在屏的 API，以主屏兜底）。
func (a *App) targetDisplays() []*application.Screen {
	cfg := a.cfgSnapshot()
	all := a.app.Screen.GetAll()
	if len(all) == 0 {
		return nil
	}
	if cfg.AllScreens() {
		return all
	}
	// 光标所在屏：Wails v3 没有 getCursorScreenPoint 的等价物，用主屏兜底
	if sc := a.app.Screen.GetPrimary(); sc != nil {
		return []*application.Screen{sc}
	}
	return all[:1]
}

// showTakeoutWindows 弹出外卖提醒窗口（每个目标屏各一个）。无头（server 模式）下不建窗，
// 但会话照常构建，于是无头冒烟能验证「触发 → 会话 JSON → 动作记账」整条链路。
func (a *App) showTakeoutWindows(slot TimeSlot, preview bool, postponedCount int) {
	a.showTakeoutWindowsAt(slot, preview, postponedCount, 0)
}

// showTakeoutWindowsAt 同上，附带 occurrence 时刻（0 = 按当天该时刻）。
func (a *App) showTakeoutWindowsAt(slot TimeSlot, preview bool, postponedCount int, atMs int64) {
	info := a.buildTakeoutInfo(slot, preview, postponedCount, atMs)

	if serverMode || a.app == nil {
		a.mu.Lock()
		a.takeoutCurrent = info
		a.mu.Unlock()
		if serverMode {
			log.Printf("[takeout] server 模式无原生窗口，跳过创建（会话已就绪 %s）", info.Meal)
		}
		return
	}

	// 建窗要几百毫秒：串行化，避免连点「预览」时两批窗口互相覆盖（野窗）
	a.takeoutWinMu.Lock()
	defer a.takeoutWinMu.Unlock()

	// 旧窗口（上一次预览，或上一轮真实提醒留下的多屏窗）必须先关：下面要用新列表
	// 整批替换 a.takeoutWins，不关的话旧窗引用就此丢掉，屏幕上那扇窗再也没有代码能
	// 关它。不播结束提示音 —— 这是被同一次操作顶替，不是提醒正常结束。
	stale := a.replaceTakeoutWins(nil, info)
	for _, w := range stale {
		w.Close()
	}
	if len(stale) > 0 {
		log.Printf("[takeout] 关掉被顶替的旧窗口 %d 个（否则引用丢失 → 关不掉的野窗）", len(stale))
	}

	cfg := a.cfgSnapshot()
	ap := cfg.AppearanceCfg()
	displays := a.targetDisplays()
	var wins []*application.WebviewWindow
	for _, display := range displays {
		wa := display.WorkArea
		w := int(float64(wa.Width) * ap.WidthPct / 100)
		h := int(float64(wa.Height) * ap.HeightPct / 100)
		if w < 320 {
			w = 320
		}
		if h < 240 {
			h = 240
		}
		x, y := wa.X, wa.Y
		if !ap.Fullscreen {
			x = wa.X + (wa.Width-w)/2
			y = wa.Y + (wa.Height-h)/2
		}
		win := a.app.Window.NewWithOptions(application.WebviewWindowOptions{
			Title:         "外卖提醒",
			X:             x,
			Y:             y,
			Width:         w,
			Height:        h,
			Frameless:     true,
			AlwaysOnTop:   ap.AlwaysOnTop,
			DisableResize: true,
			// 不设它 X/Y 会被无视：Wails 的 InitialPosition 零值 = 整窗居中
			InitialPosition: application.WindowXY,
			// 共享屏幕时隐藏（同「提醒窗口 → 共享屏幕时隐藏提醒窗口」）：
			// 外卖提醒里也有「该点外卖啦」这类文案，被同事看见一样尴尬
			ContentProtectionEnabled: ap.HideOnShare,
			URL:                      "/takeout.html?win=takeout",
			BackgroundType:           application.BackgroundTypeTranslucent,
			BackgroundColour:         application.NewRGBA(0, 0, 0, 0),
			// 窗口身份经 URL 的 ?win=takeout 下发；JS 字段对 URL 页面不生效（仅占位）
			JS: `window.__WR_WIN = "takeout";`,
			Windows: application.WindowsWindow{
				DisableFramelessWindowDecorations: true,
				HiddenOnTaskbar:                   true,
			},
		})
		// 窗口被系统 / 用户直接关掉（Alt+F4）时清掉引用
		win.OnWindowEvent(events.Common.WindowClosing, func(*application.WindowEvent) {
			a.removeTakeoutWindow(win)
		})
		wins = append(wins, win)
	}
	// 建窗这几百毫秒里提醒可能已经被结束（用户点「知道了」/ 退出 / 新一轮顶替）：
	// 会话没了就别挂窗口，否则屏幕上留一扇「没有会话」的空窗 —— 点按钮会立刻被关掉，
	// 但用户看到的是一个莫名其妙的空白框，且它不在任何集合里，只能靠点击自己退场。
	a.mu.Lock()
	stillWanted := a.takeoutCurrent != nil && a.takeoutCurrent.ID == info.ID
	a.mu.Unlock()
	if !stillWanted {
		for _, w := range wins {
			w.Close()
		}
		log.Printf("[takeout] 建窗期间提醒已结束，本轮建出的 %d 个窗口直接收掉", len(wins))
		return
	}
	// 登记新一批窗口。上面已经把旧集合摘空，这里拿到的 old 必然为空；
	// 仍走同一个函数，是为了让「替换必须吐出旧窗口」这条只有一处的实现。
	a.replaceTakeoutWins(wins, nil)

	// 抬升：降级成置顶 + 聚焦（见文件头注释）。用户点「知道了」类路径不经过这里。
	if ap.AlwaysOnTop {
		for _, w := range wins {
			w.SetAlwaysOnTop(true)
		}
	}
	if len(wins) > 0 {
		wins[0].Focus()
	}
	a.raise.noteRaised(raiseKindTakeout)
	// 抬升循环：在场期间每 raiseIntervalMs 抬一次（只在有休息窗口时），
	// 把随后显示的休息窗口压回下面。
	a.startRaiseLoop()
	// 系统级键鼠活动轮询：广播给所有提醒窗口（见 reminderSync 段）
	a.ensureSupActivityWatch()
}

// replaceTakeoutWins 用新一批窗口替换当前集合（info 非空时顺带把会话换成它），
// 返回**被替换掉的旧窗口** —— 调用方必须把它们关掉。
//
// 直接 `a.takeoutWins = wins` 是「关不掉的野窗」的根因：旧窗引用一旦被顶掉，就再没有
// 任何代码持有它 —— 用户点「知道了」关的是新窗，屏幕上那扇旧的谁也关不下（Alt+F4
// 只会触发它自己的 WindowClosing，而那时它已不在集合里）。所以替换只走这一条路。
func (a *App) replaceTakeoutWins(next []*application.WebviewWindow, info *TakeoutInfo) []*application.WebviewWindow {
	a.mu.Lock()
	defer a.mu.Unlock()
	old := a.takeoutWins
	a.takeoutWins = next
	if info != nil {
		a.takeoutCurrent = info
	}
	// 每个窗口记住它属于哪一次提醒：关窗回调据此判断「关掉的是不是当前这次」，
	// 迟到的旧窗口回调不许把刚建好的新会话清掉（与休息窗口的 breakWinSess 同理）。
	a.takeoutWinSess = make(map[*application.WebviewWindow]string, len(next))
	if a.takeoutCurrent != nil {
		for _, w := range next {
			a.takeoutWinSess[w] = a.takeoutCurrent.ID
		}
	}
	return old
}

func (a *App) removeTakeoutWindow(w *application.WebviewWindow) {
	a.mu.Lock()
	ws := a.takeoutWins
	sessID := a.takeoutWinSess[w]
	for i, x := range ws {
		if x == w {
			a.takeoutWins = append(ws[:i], ws[i+1:]...)
			break
		}
	}
	delete(a.takeoutWinSess, w)
	live := len(a.takeoutWins) > 0
	// 最后一个窗口被用户直接关掉（Alt+F4）：会话跟着结束。不清的话 realTakeoutActive
	// 永远为真（它按会话判定），之后点「预览」会被永久拒绝 —— 但只清**确实属于当前
	// 这次提醒**的窗口，否则「顶替旧窗」时迟到的回调会把新会话一起清掉。
	if !live && sessID != "" && a.takeoutCurrent != nil && sessID == a.takeoutCurrent.ID {
		a.takeoutCurrent = nil
	}
	a.mu.Unlock()
	if !live {
		a.resetTakeoutState()
	}
}

// closeTakeoutWindows 关掉全部外卖窗口并清会话（用户动作路径，结束时有提示音）
func (a *App) closeTakeoutWindows() {
	a.closeTakeoutWindowsOpt(false)
}

// closeTakeoutWindowsOpt silent=true 时不播「外卖提醒结束」提示音（退出清理用）。
func (a *App) closeTakeoutWindowsOpt(silent bool) {
	a.mu.Lock()
	ws := a.takeoutWins
	a.takeoutWins = nil
	a.takeoutCurrent = nil
	a.takeoutWinSess = nil
	a.mu.Unlock()
	// 外卖提醒结束提示音：只在「确有窗口被关掉」时播（启动清理 / 空关窗不响）。
	// 与休息结束（closeBreakWindows）同口径：声音卡片勾选启用才会在结束响一声。
	if len(ws) > 0 && !silent {
		a.playConfiguredSound()
	}
	for _, w := range ws {
		w.Close()
	}
	a.resetTakeoutState()
}

// resetTakeoutState 会话 / 定时器状态复位（收窗与「最后一个窗口被关闭」两条路径共用）
func (a *App) resetTakeoutState() {
	a.stopRaiseLoop()
	a.releaseSupActivityWatch()
}

// hasTakeoutWindows 是否有外卖窗口在场（含「刚建还没 show」的）
func (a *App) hasTakeoutWindows() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.takeoutWins) > 0
}

// raiseTakeoutWindows 把外卖窗口抬到休息窗口之上：降级为置顶 + 聚焦（见文件头注释）
func (a *App) raiseTakeoutWindows() {
	a.mu.Lock()
	ws := append([]*application.WebviewWindow(nil), a.takeoutWins...)
	cur := a.takeoutCurrent
	a.mu.Unlock()
	raised := false
	for _, w := range ws {
		if cur != nil && cur.Appearance.Fullscreen {
			continue
		}
		// 已销毁的窗口跳过：Wails 的 SetAlwaysOnTop 没有存活检查（Show / Focus / Close
		// 都有），对着一个已销毁的 HWND 反复 SetWindowPos 只会白刷一遍 API。
		if !a.windowLive(w) {
			continue
		}
		w.SetAlwaysOnTop(true)
		w.Focus()
		raised = true
	}
	if raised {
		a.raise.noteRaised(raiseKindTakeout)
	}
}

func (a *App) startRaiseLoop() {
	a.mu.Lock()
	if a.takeoutRaiseStop != nil {
		a.mu.Unlock()
		return
	}
	ch := make(chan struct{})
	a.takeoutRaiseStop = ch
	a.mu.Unlock()
	go func() {
		t := time.NewTicker(raiseIntervalMs * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-ch:
				return
			case <-t.C:
				// return 写在闭包里只退出一轮，所以「该收摊了」用变量带出来，
				// 由外层 return 真正结束这条 goroutine。
				stop := false
				guardRun("takeout.raise", func() {
					if !a.hasTakeoutWindows() {
						a.stopRaiseLoop()
						stop = true
						return
					}
					if a.raise.anyBreakActive() {
						a.raiseTakeoutWindows()
					}
				})
				if stop {
					return
				}
			}
		}
	}()
}

func (a *App) stopRaiseLoop() {
	a.mu.Lock()
	ch := a.takeoutRaiseStop
	a.takeoutRaiseStop = nil
	a.mu.Unlock()
	if ch != nil {
		close(ch)
	}
}

// ---------- 系统级键鼠活动轮询（对应 reminderSync.ts） ----------

const (
	// supPollMs 采样周期：100ms 足够跟上人手操作，开销可忽略
	supPollMs = 100
	// supBroadcastGapMs 两次广播之间的最小间隔
	supBroadcastGapMs = 250
)

// supWatchInfo 轮询状态（冒烟自检读它做断言，不去数日志行数）
type supWatchInfo struct {
	Running bool   `json:"running"`
	Mode    string `json:"mode"` // tick | idle | off
	Polls   int    `json:"polls"`
	Pulses  int    `json:"pulses"`
}

// ensureSupActivityWatch 启动系统级键鼠活动轮询（已在跑则忽略）。有提醒在**场**时调用：
// 休息会话一建立（app.showBreakSession，含预览）与外卖窗口一创建（ShowTakeoutWindow）。
// 用户在**任意应用**里敲了键或动了鼠标 → 广播 sup:active（与窗口焦点无关）。
//
// 为什么休息会话也要开：休息窗口里的二阶段（督休专员改成命令语气）原本只监听窗口内的
// mousemove / keydown，用户切到别的应用继续干活时完全检测不到 —— 用户报的就是这条
// 「不能只在提醒窗口上检测，要在整个操作系统层面做全局检测」。
//
// 与 上一版的差异：只有 GetLastInputInfo 一个来源，读不到 tick 时整段不启动。
func (a *App) ensureSupActivityWatch() {
	a.mu.Lock()
	if a.supWatchStop != nil {
		a.mu.Unlock()
		return
	}
	if !serverMode {
		if _, ok := lastInputTick(); !ok {
			a.mu.Unlock()
			log.Printf("[sup-watch] 读不到系统输入 tick，键鼠活动轮询不启动")
			return
		}
	}
	a.supWatch = supWatchInfo{Running: true, Mode: "tick"}
	ch := make(chan struct{})
	a.supWatchStop = ch
	a.mu.Unlock()

	go func() {
		t := time.NewTicker(supPollMs * time.Millisecond)
		defer t.Stop()
		var lastTick int64
		hasTick := false
		lastSentAt := int64(0)
		for {
			select {
			case <-ch:
				return
			case <-t.C:
				// 整拍包进闭包：里面的 continue 一律改写成 return —— 闭包返回就等于
				// 「这一拍结束」，与 continue 同义，换来一次 panic 只丢一拍。
				guardRun("sup.watch", func() {
					a.mu.Lock()
					w := &a.supWatch
					w.Polls++
					a.mu.Unlock()
					tk, ok := lastInputTick()
					if !ok {
						// 读取中途失效：整段降级（但 Go 侧无秒级来源，直接停止比较）
						return
					}
					active := hasTick && tk != lastTick
					lastTick = tk
					hasTick = true
					if !active {
						return
					}
					now := nowMs()
					if now-lastSentAt < supBroadcastGapMs {
						return
					}
					lastSentAt = now
					a.mu.Lock()
					a.supWatch.Pulses++
					n := a.broadcastSupActive()
					first := a.supWatch.Pulses == 1
					a.mu.Unlock()
					if first {
						log.Printf("[sup-watch] global input pulse: mode=tick to=%d", n)
					}
				})
			}
		}
	}()
}

// broadcastSupActive 把「用户仍在操作键鼠」广播给所有提醒窗口，返回实际广播的次数。
// Go 侧用 Event.Emit("sup:active") 全局广播（不按窗口定向，见文件头注释）。
// 单测里没有 Wails 应用对象（a.app == nil）：计数照走、广播跳过，不让后台 goroutine 炸测试进程。
func (a *App) broadcastSupActive() int {
	if a.app == nil {
		return 0
	}
	a.app.Event.Emit("sup:active")
	return 1
}

// releaseSupActivityWatch 提醒彻底收场了才停止轮询
func (a *App) releaseSupActivityWatch() {
	a.mu.Lock()
	ch := a.supWatchStop
	if ch == nil {
		a.mu.Unlock()
		return
	}
	if a.reminderPresentLocked() {
		a.mu.Unlock()
		return
	}
	a.supWatchStop = nil
	a.supWatch.Mode = "off"
	a.supWatch.Running = false
	a.mu.Unlock()
	close(ch)
}

// reminderPresentLocked 还有没有「需要盯全局输入」的提醒在场。
// 窗口之外还要看**会话**：无头 server 模式没有原生窗口，只有会话；
// 有会话在跑就说明休息还没结束，轮询不能停。
func (a *App) reminderPresentLocked() bool {
	return a.hasBreakWindowLocked() || a.hasTakeoutWindowsLocked() || a.sess != nil
}

func (a *App) hasTakeoutWindowsLocked() bool {
	return len(a.takeoutWins) > 0
}

func (a *App) hasBreakWindowLocked() bool {
	return len(a.breakWins) > 0
}

// supActivityWatchInfo 当前轮询状态（冒烟自检用）
func (a *App) supActivityWatchInfo() supWatchInfo {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.supWatch
}

// ---------- 绑定（对应 main.ts 的 takeout:current / takeout:preview / takeout:action） ----------

// TakeoutCurrent 当前外卖会话（渲染层首帧拉取；无会话返回 nil）
func (a *App) TakeoutCurrent(win string) *TakeoutInfo {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.takeoutCurrent
}

// PreviewTakeout 预览外卖提醒窗口：有配置的时间就预览「下一条」，否则用当前时刻
//
// 真实外卖提醒窗口在场时拒弹（返回 previewBusyReply）：showTakeoutWindowsAt 会把
// a.takeoutWins 整批换成新建的窗口，屏幕上那扇真实的提醒窗引用就此丢掉，再也关不掉，
// 会话也被预览顶掉 —— 与休息窗口同一个毛病，口径统一在这里拦。
func (a *App) PreviewTakeout(win string) string {
	if a.realTakeoutActive() {
		log.Printf("[takeout] 真实外卖提醒窗口在场，忽略预览请求")
		return previewBusyReply
	}
	cfg := a.cfgSnapshot()
	slot := TimeSlot{ID: "preview", Hour: time.Now().Hour(), Minute: time.Now().Minute(), Enabled: true}
	if hit, ok := nextSlotAt(cfg.TakeoutSlots(), nowMs()); ok {
		slot = hit.Slot
	}
	a.showTakeoutWindows(slot, true, 0)
	return "ok"
}

// realTakeoutActive 真实外卖提醒（非预览）正在进行
func (a *App) realTakeoutActive() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.takeoutCurrent != nil && !a.takeoutCurrent.Preview
}

// TakeoutAction 外卖窗口上的三个按钮：已点好 / 不点了 / 推迟。
// 「推迟」不在窗口里排定时器，而是记一笔交给每秒的 tick（见 checkTakeoutReminders）；
// 预览态只关窗不记账，免得「看一眼预览」把真实提醒推后。
func (a *App) TakeoutAction(win, action string) error {
	a.mu.Lock()
	cur := a.takeoutCurrent
	a.mu.Unlock()
	if cur == nil {
		a.closeTakeoutWindows()
		return nil
	}
	if !cur.Preview {
		a.statsStore().RecordTakeoutAction(action)
	}
	if action == "postpone" && cur != nil && !cur.Preview {
		mins := cur.PostponeMin
		count := cur.PostponedCount + 1
		a.mu.Lock()
		a.takeoutPostponed = &TakeoutPostponed{
			Slot:  cur.Slot,
			DueAt: nowMs() + int64(mins)*60_000,
			Count: count,
		}
		a.mu.Unlock()
		log.Printf("[takeout] 用户选择 推迟 %d 分钟（第 %d 次）", mins, count)
	} else {
		a.mu.Lock()
		a.takeoutPostponed = nil
		a.mu.Unlock()
		label := map[string]string{"ordered": "已点好", "skip": "不点了", "postpone": "推迟（预览，忽略）"}[action]
		if label == "" {
			label = action
		}
		log.Printf("[takeout] 用户选择 %s", label)
	}
	a.closeTakeoutWindows()
	return nil
}
