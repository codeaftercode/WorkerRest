package scheduler

import (
	"log"
	"workerrest/internal/model"
	"workerrest/internal/plan"
)

// ---------- 提前提示窗的在场判据 ----------

// preBreakWanted 右下角「提前提示窗」此刻是否该在场 —— 唯一判据。
//
// 窗口是**状态的影子**，不是一次性的弹窗：计划一旦离开提前区间（被重锚、推迟、
// 跳过、暂停、到点开始休息、目标提醒被关闭），窗口就必须消失。此前关窗散落在
// onPaused / scheduleNext / PreBreakAction 各处，谁漏一处，窗口就带着**新一轮的整段
// 倒计时**留在屏幕上 —— 用户看到的就是「还有 1200 秒（= 整个间隔）就弹出提前通知」。
//
// phase == "pre" 之外还要卡「剩余 ≤ 提前量」：phase 卡在 pre 而计划已被推远时，
// 光看 phase 会误判成该在场。
func (s *Scheduler) PreBreakWanted() bool {
	s.mu.Lock()
	phase, paused, nextAt, nextType := s.state.Phase, s.state.Paused, s.state.NextAt, s.state.NextType
	s.mu.Unlock()
	if paused || phase != "pre" || nextAt == 0 {
		return false
	}
	// cfgSnapshot 要加 App.mu：必须在放开 Scheduler.mu 之后再取（锁序铁律）
	c, ok := s.host.CfgSnapshot().BreakByID(nextType)
	if !ok || !c.Enabled || c.NotifyBeforeMs <= 0 {
		return false
	}
	remain := nextAt - nowMs()
	return remain >= 0 && remain <= c.NotifyBeforeMs
}

// syncPreBreakWindow 把提前提示窗收成状态的影子：判据说不该在场就关掉。
// 幂等：窗口本来就不在时是空操作，可以在任意状态变更后无脑调。
func (s *Scheduler) syncPreBreakWindow() {
	if !s.PreBreakWanted() {
		s.host.ClosePreBreak()
	}
}

// pauseFreezesClockLocked 暂停此刻是否冻结「休息剩余时间」。
//
// 冻结（true）只适用于「暂停之前就已经开始的休息」：短暂暂停不吞用户的休息时间，
// 恢复时整体平移。另外两种情形必须照常走时钟，否则会互相踩：
//   - 暂停期间照常弹出的自定义提醒（pausedFire）—— 它本来就在正常计时；
//   - 暂停开始之后用户主动「立即休息」发起的内置休息 —— 若也冻结，onTick 的暂停分支
//     永远不会判到点，休息窗口挂到用户恢复暂停为止（短休息也会变成永久窗口）。
func (s *Scheduler) pauseFreezesClockLocked() bool {
	st := &s.state
	if !st.Paused {
		return false
	}
	if st.Phase == "break" && (isCustomBreakID(st.NextType) || st.BreakWhilePaused) {
		return false
	}
	return true
}

// remainingMs 距下一次事件（休息开始 / 结束）的剩余毫秒；没有安排休息时为 0。
// 暂停期间剩余时间冻结（锚定到 pausedAt），不再随时间往下掉。
func (s *Scheduler) RemainingMs() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := nowMs()
	// 暂停期间剩余时间冻结（锚定到 pausedAt）；但「本该正常走时」的两种情形不冻结：
	// 暂停期间照常弹出的自定义提醒、以及暂停开始之后主动「立即休息」的那次休息。
	if s.pauseFreezesClockLocked() {
		now = s.state.PausedAt
	}
	if s.state.Phase == "break" {
		return max0(s.state.BreakEndsAt - now)
	}
	if s.state.NextAt == 0 {
		return 0
	}
	return max0(s.state.NextAt - now)
}

// refreshPlan 设置变更后重算下一次休息（休息进行中不动，等它自然结束再说）
func (s *Scheduler) RefreshPlan() {
	s.mu.Lock()
	inBreak := s.state.Phase == "break"
	s.mu.Unlock()
	if inBreak {
		return
	}
	s.scheduleNext()
}

// reanchorCountdown 离开结束后：内置短/长**倒计时**线重新从头计时（锚到 now + intervalMs）。
//
// 为什么需要：离开期间倒计时只被冻结平移，回来时看到的「还剩 X 分钟」是离开前的旧账。
// 按需求，离开结束后内置倒计时应当从本轮开头重新计时。
//
// 三处刻意不动：
//   - 自定义提醒（id 为 cb-…）：它们是用户按自己的节奏配的绝对安排，不该被一次离开推走；
//   - 固定时刻线（source != countdown）：时刻是绝对的，重锚没有意义；
//   - 长短交替计数（shortsSinceLong）：离开不是软件安排的休息，不该消费一次短休息名额。
//
// 只推后不提前：若重锚后的时刻反而更早（理论上不该发生），保持原值。
func (s *Scheduler) ReanchorCountdown() {
	cfg := s.host.CfgSnapshot()
	s.mu.Lock()
	st := &s.state
	if st.Phase == "break" || st.NextAt == 0 {
		s.mu.Unlock()
		return
	}
	if st.NextSource == nil || *st.NextSource != "countdown" {
		s.mu.Unlock()
		return
	}
	if len(st.NextType) > 3 && st.NextType[:3] == "cb-" {
		s.mu.Unlock()
		return
	}
	bcfg, ok := cfg.BreakByID(st.NextType)
	if !ok {
		s.mu.Unlock()
		return
	}
	oldAt := st.NextAt
	nextType := st.NextType
	at := nowMs() + bcfg.IntervalMs
	if at <= oldAt {
		s.mu.Unlock()
		return
	}
	st.NextAt = at
	// 重锚用的就是当前配置间隔：同步锚点间隔表，后续 replan 的沿用判据才成立
	s.anchorIv[nextType] = bcfg.IntervalMs
	// 回来时若还停在「休息前提示窗」阶段，拉回等待态：离开一趟回来不该立刻被催
	st.Phase = "idle"
	for i := range st.Lines {
		l := &st.Lines[i]
		if l.Source == "countdown" && l.Type == nextType && l.At == oldAt {
			l.At = at
		}
	}
	plan.SortPlans(st.Lines)
	if st.Rest != nil && st.Rest.At == oldAt {
		st.Rest = &model.PlanRef{Name: st.Rest.Name, At: at}
	}
	if st.Other != nil && st.Other.At == oldAt {
		st.Other = &model.PlanRef{Name: st.Other.Name, At: at}
	}
	s.mu.Unlock()
	log.Printf("[scheduler] 内置倒计时重新计时：%s → %s", plan.FmtClock(oldAt), plan.FmtClock(at))
	s.emitState()
	// 重锚把计划推到「现在 + 间隔」＝彻底离开提前区间：刚从 resume 弹出的提示窗
	// 必须跟着收掉，否则它会带着一整轮倒计时挂在屏幕上（用户报的「还有 1200 秒就弹」
	// 就是这个：窗口是对的，倒数被重锚改成了满格）。
	s.syncPreBreakWindow()
}
