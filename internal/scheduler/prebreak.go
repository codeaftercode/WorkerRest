package scheduler

import (
	"log"
	"workerrest/internal/model"
	"workerrest/internal/plan"
	"workerrest/internal/settings"
)

// ---------- 提前提示窗的在场判据 ----------

// preBreakWanted 右下角“提前提示窗”此刻是否该在场 —— 唯一判据。
//
// 窗口是**状态的影子**，不是一次性的弹窗：计划一旦离开提前区间（被重锚、推迟、
// 跳过、到点开始休息、目标提醒被关闭），窗口就必须消失。唯一不消失的原因是它那条提醒
// 自己的“暂停休息时”是“正常提醒” —— 照常提醒的窗不该被一次暂停收掉（见 AutoPauseKeep）。此前关窗散落在
// onPaused / scheduleNext / PreBreakAction 各处，谁漏一处，窗口就带着**新一轮的整段
// 倒计时**留在屏幕上 —— 用户看到的就是“还有 1200 秒（= 整个间隔）就弹出提前通知”。
//
// phase == "pre" 之外还要卡“剩余 ≤ 提前量”：phase 卡在 pre 而计划已被推远时，
// 光看 phase 会误判成该在场。
func (s *Scheduler) PreBreakWanted() bool {
	s.mu.Lock()
	phase, paused, nextAt, nextType := s.state.Phase, s.state.Paused, s.state.NextAt, s.state.NextType
	s.mu.Unlock()
	if phase != "pre" || nextAt == 0 {
		return false
	}
	// cfgSnapshot 要加 App.mu：必须在放开 Scheduler.mu 之后再取（锁序铁律）
	c, ok := s.host.CfgSnapshot().BreakByID(nextType)
	if !ok || !c.Enabled || c.NotifyBeforeMs <= 0 {
		return false
	}
	// 暂停期间：只有“暂停休息时 = 正常提醒”的那条才留在屏幕上 —— 进入暂停那一刻按这条
	// 规则决定收不收（clearForAutoPause / Pause），此后窗口每 500ms 的自愈轮询
	//（PreBreakState → PreBreakWanted）也得照同一条规则判，否则它 500ms 后自己把窗收掉。
	// 暂停中窗口显示“已暂停 / --”（见 prebreak.js），恢复后继续照常倒数。
	if paused && pauseDisposition(c) != settings.PauseActionRemind {
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

// pauseFreezesClockLocked 暂停此刻是否冻结“休息剩余时间”。
//
// **只有一种情况冻结**：暂停那一刻正在进行、且它自己选的是“推迟”的休息（缺省值）。
// 其余一律照真实时钟走：
//   - “下一次提醒”的倒计时（短 / 长休息的倒计时线、固定时刻线、自定义提醒、外卖）——
//     暂停不再停表，侧栏照常往下掉，到 0 由 onTick 的暂停分支按各卡“暂停休息时”处置
//     （2026-11-12 用户口径：无论手动还是自动暂停，倒计时都不要停）；
//   - 配“正常提醒”的休息：照常走完自己的时长（进入暂停时置 BreakWhilePaused）；
//   - 暂停期间由 pausedFire 弹出的、或用户主动“立即休息”发起的休息：
//     startBreakAt 里 BreakWhilePaused = 暂停中，天然不冻结。
func (s *Scheduler) pauseFreezesClockLocked() bool {
	st := &s.state
	return st.Paused && st.Phase == "break" && !st.BreakWhilePaused
}

// remainingMs 距下一次事件（休息开始 / 结束）的剩余毫秒；没有安排休息时为 0。
// 只有“进行中且选推迟的休息”冻结在暂停那一刻（见 pauseFreezesClockLocked），
// 倒计时线一律按真实时钟走。
func (s *Scheduler) RemainingMs() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := nowMs()
	// 暂停期间剩余时间冻结（锚定到 pausedAt）；但“本该正常走时”的两种情形不冻结：
	// 暂停期间照常弹出的自定义提醒、以及暂停开始之后主动“立即休息”的那次休息。
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

// reanchorTargetLocked 内置倒计时此刻该重锚到哪一刻（ok=false 表示这次恢复不该重锚）。
//
// 不重锚的四种情形即 ReanchorCountdown 的三条“刻意不动”：休息进行中、没有安排、
// 不是倒计时线（固定时刻线 / 自定义提醒）、这条提醒已被删掉。
// 抽出来是为了让“恢复时要不要静默”与“重锚会不会真的发生”共用同一条判据 ——
// 两处各写一份的话，改了一处就静默漂移。
func (s *Scheduler) reanchorTargetLocked(cfg *settings.Settings, now int64) (kind string, at, iv int64, ok bool) {
	st := &s.state
	if st.Phase == "break" || st.NextAt == 0 {
		return "", 0, 0, false
	}
	if st.NextSource == nil || *st.NextSource != "countdown" {
		return "", 0, 0, false
	}
	if isCustomBreakID(st.NextType) {
		return "", 0, 0, false
	}
	bcfg, exists := cfg.BreakByID(st.NextType)
	if !exists {
		return "", 0, 0, false
	}
	return st.NextType, now + bcfg.IntervalMs, bcfg.IntervalMs, true
}

// CountdownReanchorable 现在调 ReanchorCountdown 会不会真的重锚。
// 空闲链路的“恢复”据此决定要不要静默——静默的前提是紧接着真的会把计划推远。
func (s *Scheduler) CountdownReanchorable() bool {
	cfg := s.host.CfgSnapshot()
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _, _, ok := s.reanchorTargetLocked(cfg, nowMs())
	return ok
}

// ReanchorCountdown 离开结束后：内置短/长**倒计时**线重新从头计时（锚到 now + intervalMs）。
//
// 为什么需要：离开期间倒计时照真实墙钟往下掉，回来时看到的“还剩 X 分钟”是离开前的
// 旧账（甚至已经到点过一次，那次按处置被跳过 / 推迟了）。按需求，离开结束后内置倒计时
// 应当从本轮开头重新计时。
//
// 三处刻意不动（见 reanchorTargetLocked）：
//   - 自定义提醒（id 为 cb-…）：它们是用户按自己的节奏配的绝对安排，不该被一次离开推走；
//   - 固定时刻线（source != countdown）：时刻是绝对的，重锚没有意义；
//   - 长短交替计数（shortsSinceLong）：离开不是软件安排的休息，不该消费一次短休息名额。
//
// 只推后不提前：若重锚后的时刻反而更早（理论上不该发生），保持原值。
func (s *Scheduler) ReanchorCountdown() {
	cfg := s.host.CfgSnapshot()
	s.mu.Lock()
	st := &s.state
	nextType, at, iv, ok := s.reanchorTargetLocked(cfg, nowMs())
	if !ok {
		s.mu.Unlock()
		return
	}
	oldAt := st.NextAt
	if at <= oldAt {
		s.mu.Unlock()
		return
	}
	st.NextAt = at
	// 内置倒计时整个推走了：正在等空闲的挂起跟着作废（否则 mergeIdleWait 会把旧时刻钉回来）
	s.clearIdleWaitLocked()
	// 重锚用的就是当前配置间隔：同步锚点间隔表，后续 replan 的沿用判据才成立
	s.anchorIv[nextType] = iv
	// 回来时若还停在“休息前提示窗”阶段，拉回等待态：离开一趟回来不该立刻被催
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
	// 重锚把计划推到“现在 + 间隔”＝彻底离开提前区间：刚从 resume 弹出的提示窗
	// 必须跟着收掉，否则它会带着一整轮倒计时挂在屏幕上（用户报的“还有 1200 秒就弹”
	// 就是这个：窗口是对的，倒数被重锚改成了满格）。
	s.syncPreBreakWindow()
}
