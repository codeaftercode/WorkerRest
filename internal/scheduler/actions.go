package scheduler

import (
	"log"
	"workerrest/internal/model"
	"workerrest/internal/plan"
	"workerrest/internal/settings"
)

// ---------- 对外的动作（托盘 / 设置页 / 快捷键 / 提前提示窗都汇聚到这里） ----------

// ResetCycle 重置周期：把「短休息 → 长休息」这一轮从头算。
//
// 目标**只是内置短 / 长休息**（与侧栏那组按钮同一口径，2026-11-14 用户口径）：
// 外卖提醒、自定义提醒的挂起线、倒计时进度、推迟待办、等待空闲一律不动 ——
// 点一次「重置周期」不该把下午三点的喝水提醒也按新配置推后。
//
// 休息进行中先正常收尾（关窗、落账），否则状态归零了窗口还挂着；但只在结束的
// 是短 / 长休息时这样做 —— 自定义提醒的窗不归这个按钮管，硬收掉等于替用户结束一次
// 他没让结束的提醒。
func (s *Scheduler) ResetCycle() {
	s.mu.Lock()
	inBreak := s.state.Phase == "break"
	builtinBreak := isBuiltinBreakID(s.state.NextType)
	s.resetBuiltinCycleLocked()
	s.mu.Unlock()
	if inBreak && !builtinBreak {
		// 正在提醒的是外卖 / 自定义提醒：只重排短 / 长这一轮，phase 不动。
		// 走 scheduleNext 会把 phase 拉回 idle，挂着的那扇提醒窗就没人收窗了。
		s.replan()
		s.emitState()
		return
	}
	if inBreak {
		s.EndBreak()
	}
	s.scheduleNext()
}

// resetBuiltinCycleLocked 清掉内置短 / 长这一轮的进度与挂起线（调用方必须已持有 s.mu）。
// 自定义提醒的线与锚点原样留着，replan 时它们的倒计时进度照样沿用。
func (s *Scheduler) resetBuiltinCycleLocked() {
	st := &s.state
	st.ShortsSinceLong = 0
	kept := make([]model.PlannedBreak, 0, len(st.Lines))
	for _, l := range st.Lines {
		if isBuiltinBreakID(l.Type) {
			continue
		}
		kept = append(kept, l)
	}
	st.Lines = kept
	// 锚点表只清内置两条：其余身份的锚点是它们各自的倒计时进度，不能连带清零
	delete(s.anchorIv, "short")
	delete(s.anchorIv, "long")
	// 内置短 / 长挂着的推迟待办一起撤：显式重置就是从当前配置重新起算
	for k, p := range s.postponed {
		if isBuiltinBreakID(p.kind) {
			delete(s.postponed, k)
		}
	}
	if s.idleWait != nil && isBuiltinBreakID(s.idleWait.kind) {
		s.clearIdleWaitLocked()
	}
}

// BreakNow 立即开始一次休息（跳过等待）。托盘 / 侧栏 / 快捷键的“立即休息”都走这里。
//
// **只认内置短休息、长休息**：外卖提醒与自定义提醒不参与。它们是“到点提醒”，
// 提前手动触发没有意义 —— 更重要的是会顺手把那一刻的 occurrence 标成已消费，
// 把本该准点响的那次提醒吃掉。
//
// 具体起哪一条：当前挂起线里最早的一条短 / 长休息（正在休息 → 空操作）。
// 挂起线为空或两条内置都没开 → 什么也不做，不能退回去拿“下一条任意提醒”顶上。
//
// 与 BreakNowOf 的分工：BreakNowOf(kind) 仍是“按身份起一次休息”（诊断、
// 托盘“来一次长休息”、冒烟脚本的 StartBreak("cb-xxx") 都靠它），语义不动。
func (s *Scheduler) BreakNow() {
	id, breaking := s.builtinBreakNowOf()
	if id != "" || breaking {
		// 已经弹出来了，或者本来就在休息中（沿用“正在休息就空操作”）
		return
	}
	log.Printf("[scheduler] 立即休息：当前没有可用的短 / 长休息（外卖与自定义提醒不参与）")
}

// builtinNextLocked 当前挂起线里最早的一条内置短 / 长休息（外卖与自定义提醒不参与）。
// 侧栏那组按钮的目标都从它来：全局 next（NextType / NextAt）可能是外卖或自定义提醒，
// 拿它当目标会推错东西。调用方必须已持有 s.mu。
func (s *Scheduler) builtinNextLocked() (string, int64, bool) {
	kind, at := "", int64(0)
	for _, l := range s.state.Lines {
		if !isBuiltinBreakID(l.Type) {
			continue
		}
		if kind == "" || l.At < at {
			kind, at = l.Type, l.At
		}
	}
	if kind == "" {
		return "", 0, false
	}
	return kind, at, true
}

// RestTarget 下一条短 / 长休息的身份与时刻（外卖与自定义提醒不参与）。
// 设置页拿它决定侧栏那组按钮显不显示、能不能点 —— 判据只有这一份。
func (s *Scheduler) RestTarget() (kind string, at int64, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.builtinNextLocked()
}

// builtinBreakNowOf 挑一条内置短 / 长休息立即开始。
// 返回会话 id（没有可用的内置休息时为空串）与“是否本来就在休息中”。
func (s *Scheduler) builtinBreakNowOf() (string, bool) {
	s.mu.Lock()
	if s.state.Phase == "break" {
		s.mu.Unlock()
		return "", true
	}
	kind, at, ok := s.builtinNextLocked()
	s.mu.Unlock()
	if !ok {
		return "", false
	}
	return s.startBreakAt(kind, at), false
}

// breakNowOf 立即开始一次休息，kind 留空 = 用当前计划的那一条。
// 指定 kind 时先把 nextType 顶上去（诊断 / 托盘“来一次长休息”用），
// 这样 phase、已消费记账与派生视图都按同一条身份走。
// 返回值是会话 id（正在休息或该身份不存在时为空串）。
func (s *Scheduler) BreakNowOf(kind string) string {
	s.mu.Lock()
	if s.state.Phase == "break" {
		s.mu.Unlock()
		return ""
	}
	if kind != "" {
		s.state.NextType = kind
	}
	s.mu.Unlock()
	return s.startBreak()
}

// Postpone 推迟「当前这一跳」= 计划里的下一次提醒（可能是自定义提醒）。
// 提前提示窗上的“推迟”走这条路（窗上提示哪条就推哪条，见 PostponeOf）。
//
// 侧栏与快捷键**不走这里**：它们只认短 / 长休息（见 PostponeRest）。
func (s *Scheduler) Postpone() { s.postpone("", false) }

// PostponeOf 推迟指定身份的提醒。提前提示窗用它：窗上那条才是目标，
// 别拿“调度器的全局 next”去猜 —— 暂停中照常弹出的自定义提醒就会让两者不是同一个。
func (s *Scheduler) PostponeOf(kind string) { s.postpone(kind, false) }

// PostponeRest 推迟「下一次短 / 长休息」（两者取最近的那一个）。
// 设置页侧栏的「推迟休息」与快捷键「推迟当前休息」走它：外卖提醒与自定义提醒
// 都不参与（2026-11-14 用户口径），它们自己的窗口里另有推迟入口。
func (s *Scheduler) PostponeRest() { s.postpone("", true) }

// postpone 推迟目标的实现（仅在未进入休息窗口时有效）
//
// 推迟后的时刻 = 基准 + 卡片里的“推迟时长”，基准取“现在”与原定时刻的较晚者
// （推迟只能是往后推）。两种计时方式的落点不同：
//
//   - 倒计时线：这一跳是相对的，直接把挂起线的 At 挪到新时刻，侧栏 / 托盘 / 下次触发
//     全都按它算；重排时未到期的锚点会被沿用，推迟不会丢。
//   - 固定时刻线：时刻是列表里的绝对时刻，改不了配置，所以只记一条“改到 dueAt 再弹”
//     的待办（postponed 表），由 replan 把它顶到最近一条、flushPostponed 到点弹出。
//     若推迟后的时刻已经碰上或越过列表里的**下一次**，这次就别再单独弹了 —— 跳过本次、
//     直接按下一个固定时刻算（2026-11-12 用户口径）。否则“12:00 推迟 3 小时”会在 15:00
//     弹一次，把 14:00 那班整个挤掉，用户看到的是配置里根本不存在的时刻。
func (s *Scheduler) postpone(kind string, restOnly bool) {
	cfg := s.host.CfgSnapshot()
	s.mu.Lock()
	st := &s.state
	// 暂停中照样可以推迟：倒计时不冻钟（2026-11-12 口径），侧栏那两个按钮在暂停时也
	// 照常可点，点了就该真的把这一跳往后挪。这里曾有一条 `|| st.Paused` 早退（冻结
	// 时代的遗留），结果暂停中「推迟休息」点了没反应，界面也不禁用 —— 静默空操作。
	if st.Phase == "break" {
		s.mu.Unlock()
		return
	}
	nextType, oldAt, ok := s.actionTargetLocked(kind, restOnly)
	if !ok {
		s.mu.Unlock()
		log.Printf("[scheduler] 推迟：当前没有可推迟的%s（kind=%q）",
			restLabel(restOnly), kind)
		return
	}
	bcfg, ok := cfg.BreakByID(nextType)
	if !ok {
		// 计划里的这条休息已被删除：没有“推迟多久”可言，直接重排下一条
		s.mu.Unlock()
		s.scheduleNext()
		return
	}
	ms := bcfg.PostponeMs
	// “允许推迟提醒”没勾（PostponeMs == 0）：这一次推迟请求整体作废。
	// 界面上的按钮已经藏了，但快捷键、托盘、提前提示窗可能仍有入口 —— 判据只在这里
	// 认一次，别让“按钮没了、快捷键还能推”这种半开半关的状态存在。
	// 顺手也别清“等待空闲”的挂起：什么都没变，状态就不该动。
	if ms <= 0 {
		s.mu.Unlock()
		log.Printf("[scheduler] %s 不允许推迟（未勾选“允许推迟提醒”），忽略本次推迟", nextType)
		return
	}
	// 推迟意味着“这一跳到点了但先不提醒”，等待空闲的挂起一并结束：新时刻由推迟算出来，
	// 到点后如果空闲门还开着，会重新进入等待（见 onTick 的空闲门分支）。
	// 只清目标这一条：目标是短 / 长休息时，别的提醒的“等待空闲”不该被顺手抹掉。
	if s.idleWait != nil && s.idleWait.kind == nextType {
		s.clearIdleWaitLocked()
	}
	// 基准取“现在”与“原定时刻”的较晚者：推迟只能是往后推。
	// 早先直接写 now+ms，工作态里点一次“推迟”会把 30 分钟后的提醒拉到 5 分钟后
	// （既违反“推迟”的语义，也把统计里的“推后时长”记成了负数般的假值）。
	base := nowMs()
	if oldAt > base {
		base = oldAt
	}
	postAt := base + ms
	if bcfg.Mode == "fixed" {
		s.postponeFixedLocked(nextType, bcfg, oldAt, postAt, ms)
		return
	}
	// 相位：只有“被推迟的就是屏幕上正在提示的那一条”才回 idle（那扇提前提示窗该收了）。
	// 推的是另一条时保留相位 —— 侧栏只推短 / 长休息，而提示窗里可能是喝水提醒，关掉它
	// 等于替用户取消了一次没被推迟的提醒，500ms 后 onTick 又会弹回来，用户看到闪一下。
	wasPre := st.Phase == "pre"
	st.Phase = "idle"
	if wasPre && (st.NextType != nextType || st.NextAt != oldAt) {
		st.Phase = "pre"
	}
	// 统计：记一次推迟 + 推后时长。nextType 也可能是自定义提醒（cb-…），
	// 但统计页“休息”四项只认短 / 长休息 —— 过滤在 App.onBreakPostponed 里一处做掉。
	s.host.OnBreakPostponed(nextType, ms)
	// 被推迟的那条线在挂起线表里同步平移（今日时间线读的是这份表）
	moved := false
	for i := range st.Lines {
		if st.Lines[i].Type == nextType && st.Lines[i].At == oldAt {
			st.Lines[i].At = postAt
			moved = true
		}
	}
	if !moved {
		// 目标时刻在挂起线里找不到（NextAt 与 line 失配，只可能来自构造出来的状态）：
		// 记一条日志，别让“点了推迟、界面无变化”变成无声的空操作。
		log.Printf("[scheduler] 推迟 %s：挂起线里没有 %d 这一刻，计划未改动", nextType, oldAt)
	}
	plan.SortPlans(st.Lines)
	s.mu.Unlock()
	// 统计用：不管推迟来自托盘、快捷键还是休息前提示窗，都从这里过一遍
	log.Printf("[scheduler] 推迟 %s %d ms", nextType, ms)
	// 统一重排而不是就地改 NextAt：目标可能不是全局那一条（侧栏只推短 / 长），
	// NextType / NextAt 必须按新的挂起线重算，否则托盘和侧栏会继续指着被推走的那一跳。
	// 重排也会收掉已失效的提前提示窗（推迟把原定时刻推远，已不在提前区间）。
	s.scheduleNext()
}

// restLabel 日志用：目标口径的中文说法
func restLabel(restOnly bool) string {
	if restOnly {
		return "短 / 长休息"
	}
	return "提醒"
}

// actionTargetLocked 解析「推迟 / 跳过」的目标（调用方必须已持有 s.mu）。
//
//	restOnly   → 只认下一次短 / 长休息（侧栏与快捷键）
//	kind != "" → 指定身份：取它挂起线里最早的一跳（提前提示窗）
//	其余       → 计划里的全局下一次（st.NextType / st.NextAt）
//
// 三种口径不能混：全局 next 可能是外卖或自定义提醒，拿它当“休息”的目标会推错东西。
func (s *Scheduler) actionTargetLocked(kind string, restOnly bool) (string, int64, bool) {
	st := &s.state
	if restOnly {
		return s.restTargetLocked()
	}
	if kind != "" {
		typ, at := "", int64(0)
		for _, l := range st.Lines {
			if l.Type != kind {
				continue
			}
			if typ == "" || l.At < at {
				typ, at = l.Type, l.At
			}
		}
		if typ == "" {
			// 这条提醒已经不在挂起线里（刚被关掉 / 删掉）：重排会换下一条，
			// 别退回去拿全局 next 顶替 —— 那会推错另一条提醒。
			return "", 0, false
		}
		if due, ok := s.pendingPostponedDueLocked(kind); ok && due < at {
			at = due
		}
		return typ, at, true
	}
	if st.NextType == "" || st.NextAt == 0 {
		return "", 0, false
	}
	return st.NextType, st.NextAt, true
}

// restTargetLocked 下一次短 / 长休息的目标（kind, 时刻）。
//
// 固定时刻线挂着“改到 dueAt 再弹”的推迟待办时，dueAt 才是这一跳现在的时刻 ——
// 点「推迟 / 跳过」针对的就是它（否则下一次点推迟会跳过那条待办，直接作用在
// 时刻表里的下一班上）。
func (s *Scheduler) restTargetLocked() (string, int64, bool) {
	kind, at, ok := s.builtinNextLocked()
	if !ok {
		return "", 0, false
	}
	if due, ok := s.pendingPostponedDueLocked(kind); ok && due < at {
		at = due
	}
	return kind, at, true
}

// pendingPostponedDueLocked 这条提醒挂着的最早一条“还没到点”的推迟待办时刻
// （没有则 false）。调用方必须已持有 s.mu。
func (s *Scheduler) pendingPostponedDueLocked(kind string) (int64, bool) {
	var due int64
	found := false
	for _, p := range s.postponed {
		if p.kind != kind || p.dueAt <= nowMs() {
			continue
		}
		if !found || p.dueAt < due {
			due, found = p.dueAt, true
		}
	}
	return due, found
}

// postponeFixedLocked 固定时刻线的推迟（调用方必须已持有 s.mu）。
//
// 本次这一班先记成“已处理”，再按推迟后的时刻排待办：消费掉原定 occurrence 后，
// 时刻表会顺延到下一班，dueAt 才排得进去（否则 replan 又会把原定时刻排回来）。
// 推迟后的时刻越过下一班时改为“跳过本次”—— 不排待办，下一班按它自己的时刻来。
func (s *Scheduler) postponeFixedLocked(nextType string, bcfg model.BreakConfig, oldAt, postAt, ms int64) {
	st := &s.state
	// 反复推迟：NextAt 就是上一次的 dueAt，必须沿用原定 occurrence，否则每次都会
	// 多记一条待办（同一班被弹两次）。
	occAt := oldAt
	key := plan.OccurrenceKey(nextType, oldAt)
	if p := s.pendingPostponedLocked(nextType, oldAt); p != nil {
		occAt, key = p.at, plan.OccurrenceKey(p.kind, p.at)
	}
	if next, ok := plan.NextSlotAt(bcfg.Slots, occAt); ok && postAt >= next.At {
		delete(s.postponed, key)
		s.consumed[plan.OccurrenceKey(nextType, occAt)] = true
		s.advanceCounter(nextType)
		st.Phase = "idle"
		s.mu.Unlock()
		// 行为上这次提醒就是被跳过了：记账口径与“跳过”一致（只认短 / 长，App 侧过滤）
		s.host.OnBreakSkipped(nextType, bcfg.DurationMs)
		log.Printf("[scheduler] 推迟 %s 后（%s）不早于下一次固定时刻 %s：跳过本次，按 %s 计",
			nextType, plan.FmtClock(postAt), plan.FmtClock(next.At), plan.FmtClock(next.At))
		s.scheduleNext()
		return
	}
	s.consumed[plan.OccurrenceKey(nextType, occAt)] = true
	s.postponed[key] = &postponedBreak{kind: nextType, at: occAt, dueAt: postAt}
	st.Phase = "idle"
	s.mu.Unlock()
	s.host.OnBreakPostponed(nextType, ms)
	log.Printf("[scheduler] 推迟 %s %d ms（固定时刻，改到 %s 再提醒）", nextType, ms, plan.FmtClock(postAt))
	// 排待办之后必须重排：NextAt 要按 dueAt 走，托盘 / 提前提示窗 / 侧栏才对得上
	s.scheduleNext()
}

// pendingPostponedLocked 这条提醒当前挂着的那条推迟待办（没有则 nil）。
// 判据是“同一个 kind 且 dueAt == NextAt”：replan 会把推迟后的时刻顶到 NextAt，
// 所以下一次点推迟时 NextAt 就是上次的 dueAt。调用方必须已持有 s.mu。
func (s *Scheduler) pendingPostponedLocked(kind string, dueAt int64) *postponedBreak {
	for _, p := range s.postponed {
		if p.kind == kind && p.dueAt == dueAt {
			return p
		}
	}
	return nil
}

// Skip 跳过「当前这一跳」= 计划里的下一次提醒（可能是自定义提醒）。
// 提前提示窗上的“跳过”走这条路（见 SkipOf）。
//
// 侧栏与快捷键**不走这里**：它们只认短 / 长休息（见 SkipRest）。
func (s *Scheduler) Skip() { s.skip("", false) }

// SkipOf 跳过指定身份的提醒（提前提示窗用：窗上那条才是目标）。
func (s *Scheduler) SkipOf(kind string) { s.skip(kind, false) }

// SkipRest 跳过「下一次短 / 长休息」（两者取最近的那一个）。设置页侧栏的「跳过本次」
// 与托盘 / 休息窗口的“提前结束”之外的那条入口走它：外卖提醒与自定义提醒不参与。
func (s *Scheduler) SkipRest() { s.skip("", true) }

// skip 跳过目标的实现：休息进行中就等于提前结束，否则消费这一跳并进入下一周期。
//
// 休息进行中点“跳过 / 提前结束”（设置页侧栏、托盘、快捷键都汇聚到这里）：
// 语义上就是立刻结束这次休息，落 endBreak 路径——早先直接 return，
// 界面上点了没反应，用户只能去休息窗口里另找入口。
func (s *Scheduler) skip(kind string, restOnly bool) {
	cfg := s.host.CfgSnapshot()
	s.mu.Lock()
	st := &s.state
	if st.Phase == "break" {
		s.mu.Unlock()
		log.Printf("[scheduler] 休息中“跳过”→ 提前结束本次休息")
		s.finishBreak()
		return
	}
	// 暂停中同样受理（与 Postpone 同口径）：点“跳过本次”就是这一次不提醒了，
	// 该记一笔跳过并把这一跳消费掉。旧实现这里的 `st.Paused` 早退让暂停中的按钮
	// 完全失效 —— 而暂停期间倒计时照常走，用户正需要它能用。
	nextType, at, ok := s.actionTargetLocked(kind, restOnly)
	if !ok {
		s.mu.Unlock()
		log.Printf("[scheduler] 跳过：当前没有可跳过的%s（kind=%q）", restLabel(restOnly), kind)
		return
	}
	bcfg, ok := cfg.BreakByID(nextType)
	if !ok {
		s.mu.Unlock()
		s.scheduleNext()
		return
	}
	skippedMs := bcfg.DurationMs
	s.advanceCounter(nextType)
	// 跳过消费了这次安排：目标这一条的“等待空闲”也随之结束（别的提醒的不动）
	if s.idleWait != nil && s.idleWait.kind == nextType {
		s.clearIdleWaitLocked()
	}
	// 固定时刻线可能正挂着一条“改到 dueAt 再弹”的待办（Postpone）：跳过这次就得把那条
	// 一起撤掉，否则 dueAt 一到 flushPostponed 又弹一次 —— 用户明明点了“跳过”。
	if p := s.pendingPostponedLocked(nextType, at); p != nil {
		delete(s.postponed, plan.OccurrenceKey(p.kind, p.at))
		s.consumed[plan.OccurrenceKey(p.kind, p.at)] = true
	}
	// 固定时刻线：不消费的话 replan 会把同一时刻原样排回来，“跳过”等于没跳
	s.consumed[plan.OccurrenceKey(nextType, at)] = true
	s.mu.Unlock()
	// 统计：记一次跳过 + 被跳过那次的时长。同样只认短 / 长休息，过滤在 App 侧。
	s.host.OnBreakSkipped(nextType, skippedMs)
	log.Printf("[scheduler] 跳过 %s（时长 %d ms）", nextType, skippedMs)
	s.scheduleNext()
}

// endBreak 提前结束进行中的休息
func (s *Scheduler) EndBreak() {
	s.mu.Lock()
	inBreak := s.state.Phase == "break"
	s.mu.Unlock()
	if !inBreak {
		return
	}
	s.finishBreak()
}

// pauseToggle 手动暂停 / 恢复切换
func (s *Scheduler) PauseToggle() {
	s.mu.Lock()
	paused := s.state.Paused
	s.mu.Unlock()
	if paused {
		s.Resume(PauseReasonManual)
	} else {
		s.Pause(PauseReasonManual)
	}
}

// PauseUntil 定时暂停：at 是到期时刻（毫秒），0 表示取消定时、退回无限暂停。
//
// 原因仍然记 manual —— 定时暂停是“我接下来一段时间不工作”，不是软件判定你在开会，
// 所以清场、跳过、托盘文案全走手动那一套；它比普通手动暂停只多一件事：到点自动结束
// （到点后与手动恢复走同一条 resume，不额外重锚任何倒计时 —— 2026-11-12 口径）。
//
// 已暂停时只改到期时刻，不重置 PausedAt：把 1 小时改成 2 小时，已经过去的那段不该
// 从头算。传进来的时刻若已过期也没关系，下一拍到期判定就会恢复。
func (s *Scheduler) PauseUntil(at int64) {
	if at < 0 {
		at = 0
	}
	s.mu.Lock()
	already := containsStr(s.state.PauseReasons, PauseReasonManual)
	if at == 0 && !already {
		// 没有定时、也没有手动暂停：没有可取消的东西。
		// 不能顺手 Pause(manual) —— 那会把“纯自动暂停”偷偷改成手动暂停（托盘上
		// 从此多一条 manual，条件消失也不会自己恢复）。
		s.mu.Unlock()
		return
	}
	s.state.PauseDeadline = at
	s.mu.Unlock()
	if already {
		// 已经在暂停里：只更新到期时刻，PausedAt 保持原值
		s.emitState()
		return
	}
	s.Pause(PauseReasonManual)
}

// pause 加一条暂停原因。多条原因并存，全部清掉才真的恢复。
//
// 进入暂停时按原因分两种动作：
//
//   - 手动暂停：只收提前提示窗；正在进行中的休息按它自己的“暂停休息时”处置
//     （见 pauseRunningBreak：正常提醒照常走完 / 跳过就此结束 / 推迟冻结剩余）。
//   - 自动暂停：软件判定他此刻在忙或不在（勿扰 / 投屏 / 全屏 / 应用占用 / 空闲），
//     进入的那一刻就把**在场**的提醒窗口收掉（提前提示、外卖窗与外卖预通知，以及休息窗）。
//     用户没要求暂停，就不该让任何一扇窗继续留在屏幕上。
//
// **两种暂停都让过“正常提醒”的那扇窗**（2026-11-12 用户口径：四类提醒一视同仁）：
// 那条提醒的“暂停休息时”选了“正常提醒”，它此刻在屏幕上的窗口就不收 —— 自动暂停见
// clearForAutoPause，手动暂停见这里的 OnPaused(preKind)。判据只看卡里的那一项，
// 暂停原因不参与，也不按“调度器此刻在等哪条”一刀切：宿主按每扇窗自己的归属去查，
// 见 AutoPauseKeep。
//
// **暂停期间到点的提醒不按原因区别对待**（2026-11-12 用户口径：“不存在例外”）：
// 四类提醒一律按各自卡里的“暂停休息时”处置走，见 onTick 的 paused 分支与 pausedSkip /
// pausedFire / pausedPostpone。
//
// **时钟口径**（2026-11-12 用户口径：暂停不影响倒计时）：暂停不再冻结“下一跳”的时钟，
// 侧栏照常往下掉，到 0 按上述处置办；因此恢复时既不整体平移、也不重锚（定时暂停同样不重锚）。
// 唯一被冻结的是“进行中且选推迟的那次休息”的剩余时间 —— 那正是“推迟”的含义。
//
// 已经在暂停态时叠加一条自动原因，同样要清场：那说明此前是手动暂停、休息窗还冻结
// 挂着，而现在系统判定用户在忙，窗口该收（同样让过“正常提醒”的那次休息）。
func (s *Scheduler) Pause(reason string) {
	auto := IsAutoPauseReason(reason)
	s.mu.Lock()
	if containsStr(s.state.PauseReasons, reason) {
		s.mu.Unlock()
		return
	}
	s.state.PauseReasons = append(s.state.PauseReasons, reason)
	if auto {
		s.pauseHadAuto = true
	}
	becamePaused := !s.state.Paused
	if becamePaused {
		s.state.Paused = true
		s.state.PausedAt = nowMs()
	}
	s.mu.Unlock()
	log.Printf("[scheduler] 暂停（%s）", reason)
	if auto {
		s.clearForAutoPause()
	} else if becamePaused {
		// 暂停提醒时关闭休息前提示弹窗（托盘暂停与弹窗内暂停共用此路径）；
		// 但那条提醒自己的“暂停休息时”是“正常提醒”时就留着 —— 留不留由宿主按窗自己的
		// 归属判，这里只交代“调度器这边在提示的是哪条”（见 AutoPauseKeep）
		s.host.OnPaused(s.pausePreKind())
		// 正在进行中的休息按它**自己的**“暂停休息时”处置（2026-11-12 用户口径）：
		// 正常提醒 → 照常走完；跳过 → 就此结束并记账；推迟（缺省）→ 剩余时间冻结、
		// 恢复后接着走。自动暂停不在这里走这套，原因见 pauseRunningBreak 的注释。
		s.pauseRunningBreak()
	}
	s.emitState()
}

// pausePreKind 调度器此刻那扇提前提示窗（Phase=pre）提示的提醒身份；不在提示阶段为空串。
// 只当窗上没带身份（老会话 / 单测构造）时由宿主兜底用。
func (s *Scheduler) pausePreKind() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.Phase == "pre" {
		return s.state.NextType
	}
	return ""
}

// pauseKeepLocked 进入暂停这一刻，调度器这边在场窗口的归属（已持 s.mu 时用）。
//
// 只交代身份、不下结论：休息窗与提前提示窗各自的“暂停休息时”由宿主拿着那扇窗自己的
// 归属去查（settings.BreakPauseRemind）—— 暂停中照常弹出的自定义提醒会让提示窗与
// “调度器此刻在等的那条”不是同一个身份，这里一刀切就会判错。外卖不走调度器，
// 窗与推迟线都在宿主管，全部归外卖卡，直接给结论。
func (s *Scheduler) pauseKeepLocked(cfg *settings.Settings) AutoPauseKeep {
	st := &s.state
	keep := AutoPauseKeep{
		Takeout: cfg.TakeoutPauseAction() == settings.PauseActionRemind,
	}
	switch st.Phase {
	case "break":
		keep.BreakKind = st.NextType
	case "pre":
		keep.PreKind = st.NextType
	}
	return keep
}

// clearForAutoPause 自动原因进入暂停时的清场：提醒窗口收掉，被打断的那次休息
// 记“跳过”（不是“提前结束”—— 用户没走完，也不是他自己点的结束）。
//
// **留窗规则只有一条：那条提醒自己配的是“正常提醒”**（`pauseAction=remind`）。
// “正常提醒”＝照常提醒，此刻在屏幕上的那扇窗就是“照常”的现场 —— 收起它等于把这条
// 配置偷偷降级。休息窗、提前提示窗、外卖窗与外卖预通知各自按**自己那条提醒**判
// （宿主按窗归属查配置，见 AutoPauseKeep）；暂停原因一概不参与这个判断，只有卡里的那一项说话。
//
// 留休息窗时：不记跳过、不推进计数、标 BreakWhilePaused 让它按真实时钟走完
// （与“暂停中主动立即休息”同一口径，到点由 onTick 的暂停分支收窗）。
// 留提前提示窗时：phase 也停在 pre —— PreBreakWanted 见到“暂停中 + 正常提醒”才判它该在场。
//
// 不在这里重排计划（这是“进入暂停那一刻”的一次性动作）：暂停不冻钟，之后每一跳都由
// onTick 的暂停分支按各卡的处置接着走 —— 到点该弹的弹、该推的推、该跳的跳。
// 这里若顺手 replan，等于用“重算出来的下一跳”顶掉“暂停时手上那一跳”，处置就落不了地。
func (s *Scheduler) clearForAutoPause() {
	cfg := s.host.CfgSnapshot() // 锁外取，遵守“不持 s.mu 碰 App.mu”的锁序
	s.mu.Lock()
	st := &s.state
	inBreak := st.Phase == "break"
	inPre := st.Phase == "pre"
	kind := st.NextType
	skippedMs := int64(0)
	keep := s.pauseKeepLocked(cfg)
	keepBreak := inBreak && cfg.BreakPauseRemind(kind)
	keepPre := inPre && cfg.BreakPauseRemind(kind)
	if inBreak {
		if c, ok := cfg.BreakByID(kind); ok {
			skippedMs = c.DurationMs
		}
	}
	switch {
	case inBreak && keepBreak:
		// 按真实时钟走完：BreakWhilePaused 让 pauseFreezesClockLocked 返回 false，
		// 到点后由 onTick 的暂停分支 finishBreak，与暂停中主动“立即休息”同路。
		st.BreakWhilePaused = true
	case inPre && keepPre:
		// 提示窗留着，阶段也留在 pre：判据见 PreBreakWanted 的“暂停中”那一条
	default:
		if inBreak {
			// 这次休息名额已用掉：短休息计数照走，与“跳过”同口径（长休息清零）
			s.advanceCounter(kind)
			st.BreakStartedAt = 0
			st.BreakEndsAt = 0
			st.BreakWhilePaused = false
		}
		// pre 与 break 都归 idle：提示窗被收掉后，状态不能继续停在 pre
		st.Phase = "idle"
	}
	s.mu.Unlock()

	// 宿主握有真实会话（哪一扇窗、预览与否），关窗由它统一做；统计口径留在调度器，
	// 免得同一笔“跳过”在两层各记一次
	s.host.OnAutoPause(keep)
	switch {
	case inBreak && !keepBreak:
		s.host.OnBreakSkipped(kind, skippedMs)
		log.Printf("[scheduler] 自动暂停打断休息 %s（%d ms），按跳过处理", kind, skippedMs)
	case keepBreak:
		log.Printf("[scheduler] 自动暂停：%s 配的是“正常提醒”，休息窗保留、按真实时钟走完", kind)
	}
	if inPre {
		if keepPre {
			log.Printf("[scheduler] 自动暂停：%s 配的是“正常提醒”，提前提示窗保留", kind)
		} else {
			log.Printf("[scheduler] 自动暂停：%s 没收提前提示窗，照常收掉", kind)
		}
	}
	s.syncPreBreakWindow()
}

// pauseRunningBreak 手动暂停那一刻，**正在进行中**的休息按它自己的“暂停休息时”处置。
//
// “倒计时不要停”这条口径只针对“还没到点的那一跳”：已经开始的休息按卡片处置更合理 ——
//
//   - 正常提醒（remind）：BreakWhilePaused=true，窗口留着、剩余时间照真实时钟走完；
//   - 跳过（skip）：立刻结束，短休息计数照走，记一笔跳过（与自动暂停同口径）；
//   - 推迟（postpone，缺省）：剩余时间冻结在暂停那一刻，恢复时整体后移（见 resume）。
//
// 自动暂停不走这里（clearForAutoPause 有自己的清场规则）：自动暂停表达的是“此刻别打扰我”，
// 进入那一刻屏幕上的窗必须收掉（勿扰 / 全屏 / 会议里压一扇休息窗是不可接受的），所以它
// 只分“正常提醒就留着照常走完、其余按跳过收场”。手动暂停是用户自己按的，窗留着不唐突，
// 才有“冻结剩下一半、回来接着休息”这个选项。
func (s *Scheduler) pauseRunningBreak() {
	cfg := s.host.CfgSnapshot()
	s.mu.Lock()
	st := &s.state
	if st.Phase != "break" || !st.Paused {
		s.mu.Unlock()
		return
	}
	kind := st.NextType
	act := settings.PauseActionPostpone
	skippedMs := int64(0)
	if c, ok := cfg.BreakByID(kind); ok {
		act = pauseDisposition(c)
		skippedMs = c.DurationMs
	}
	switch act {
	case settings.PauseActionRemind:
		st.BreakWhilePaused = true
		s.mu.Unlock()
		log.Printf("[scheduler] 暂停：休息 %s 配的是“正常提醒”，窗口保留、按真实时钟走完", kind)
		return
	case settings.PauseActionSkip:
		s.advanceCounter(kind)
		st.BreakStartedAt = 0
		st.BreakEndsAt = 0
		st.BreakWhilePaused = false
		st.Phase = "idle"
		s.mu.Unlock()
		log.Printf("[scheduler] 暂停：休息 %s 配的是“跳过”，就此结束（%d ms）", kind, skippedMs)
		s.host.CloseBreakWindows()
		s.host.OnBreakSkipped(kind, skippedMs)
		s.scheduleNext()
		return
	}
	s.mu.Unlock()
	log.Printf("[scheduler] 暂停：休息 %s 配的是“推迟”，剩余时间冻结、恢复后接着走", kind)
}

// AutoPauseActive 当前这段暂停里是否有自动原因。**只用于表达“这段暂停不是用户按的”**
// （暂停提示 pause_notice.go 据此决定要不要提醒他“已暂停多久”），不参与任何提醒的处置：
// 到点怎么办一律看各卡自己的“暂停休息时”，与暂停原因无关（2026-11-12 用户口径）。
func (s *Scheduler) AutoPauseActive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.Paused && s.pauseHadAuto
}

// Resume 去掉一条暂停原因；全部清掉时恢复，并按当前时刻重判各线（倒计时线不平移、
// 固定时刻线重算下一个还没到的时刻）。
// 恢复的那一刻若仍落在提前提示区间内，会重新弹出提示窗（暂停时它被收掉了）。
func (s *Scheduler) Resume(reason string) { s.resume(reason, true) }

// ResumeQuiet 与 Resume 同义，只是**不重弹提前提示窗**。
//
// 只给“紧接着就会重锚倒计时”的调用方用（目前是空闲链路：人回来后内置倒计时
// 从头计时，见 ReanchorCountdown）。那种场景下提前提示窗注定要被收掉，先弹一个
// 再关掉，用户看到的是右下角闪一下窗口，日志里还留一条“即将休息（剩余 0 ms）”。
func (s *Scheduler) ResumeQuiet(reason string) { s.resume(reason, false) }

func (s *Scheduler) resume(reason string, allowPre bool) {
	cfg := s.host.CfgSnapshot()
	s.mu.Lock()
	idx := indexOfStr(s.state.PauseReasons, reason)
	if idx < 0 {
		s.mu.Unlock()
		return
	}
	rest := make([]string, 0, len(s.state.PauseReasons)-1)
	rest = append(rest, s.state.PauseReasons[:idx]...)
	rest = append(rest, s.state.PauseReasons[idx+1:]...)
	s.state.PauseReasons = rest
	st := &s.state
	// 摘掉 manual 就等于“这段（定时）手动暂停到此结束”，到期时刻是它的附属物，必须
	// 一起清掉：留着的话托盘会继续显示“还有 25 分钟（到 22:30）”、菜单里还挂着
	// “取消定时”——而那段暂停已经被用户按掉了，剩下的只是别的自动原因（勿扰 / 投屏
	// 还挂着）。全部恢复时下面主体也会清零，这里只是提前一步覆盖“没恢复彻底”那一支。
	if reason == PauseReasonManual {
		st.PauseDeadline = 0
	}
	if len(st.PauseReasons) != 0 || !st.Paused {
		s.mu.Unlock()
		return
	}
	// 暂停起点先留住：被冻结的那次休息要用它算“暂停了多久”，恢复时把剩余时间接上
	pausedAt := st.PausedAt
	delta := nowMs() - pausedAt
	// pauseHadAuto 必须在真正的恢复这一跳清零：AutoPauseActive() 靠它回答
	// “这段暂停里有过自动原因”，暂停提示（pause_notice.go）只用那个口径。
	s.pauseHadAuto = false
	st.Paused = false
	// 段起点随这一段暂停一起结束。不清的话它会一直留着上一段的时刻：任何“pausedAt != 0
	// 就认为暂停过”的判据都会误读，状态快照里也会带着一个没有意义的旧值。
	// “已暂停多久”的读者（托盘 / 小窗 / 到期日志）本来就都以 Paused 为前提，这里只是
	// 让字段本身也诚实。
	st.PausedAt = 0
	// 到期时刻随暂停一起结束：留着会让托盘继续显示“还有 0 分钟”，也可能被下一段
	// 暂停误当成自己的截止时间
	st.PauseDeadline = 0
	// 这一段暂停结束了：清掉“已按跳过记账”“已照常弹过”两张 occurrence 表，
	// 下一段暂停重新计
	s.pauseSkipped = map[string]bool{}
	s.pauseFired = map[string]bool{}
	preType := ""
	switch {
	case st.Phase == "break" && st.BreakWhilePaused:
		// 暂停开始**之后**发起的、或配“正常提醒”的那次：它一直按真实时钟在走
		//（见 pauseFreezesClockLocked），恢复时不该再按暂停时长平移，否则白送一段休息。
		st.BreakWhilePaused = false
		s.mu.Unlock()
	case st.Phase == "break":
		// 剩余时间冻结整体平移（短暂暂停不吞用户的休息时间）；但总休息时长
		//（含暂停期间）封顶为本次配置时长——暂停期间本来就在休息。
		// 空闲暂停不会在休息窗口期间发生（onTick 见 phase === 'break' 直接返回），
		// 所以能落到这里的只有手动 / 勿扰 / 指定应用占用三种暂停；它们可能持续很久
		//（开着勿扰看了一下午视频）。这次休息自己选的是“推迟”，就不封顶：封顶会让一次
		// 长暂停直接把休息判成到点（等于强制结束），与“推迟”相反。
		st.BreakEndsAt += delta
		s.mu.Unlock()
	case st.NextSource != nil && *st.NextSource == "countdown":
		// 倒计时线**不再平移**（2026-11-12 用户口径：暂停不影响倒计时，恢复时也不需要
		// 补偿）。暂停期间时钟照常走，到点的那一跳已经在暂停中用该卡的“暂停休息时”
		// 处置过（跳过 / 提醒 / 推迟推到的新时刻），这里只剩一件正事：
		// 按当前时刻重判一次“即将休息”阶段，不依赖下一个 tick。
		//
		// 已过期（now >= NextAt）时不弹：那一跳该由 onTick 直接开始休息，弹一个
		//“还剩 0 秒”的提示窗只是闪一下。
		if c, ok := cfg.BreakByID(st.NextType); ok && st.NextAt > 0 && c.NotifyBeforeMs > 0 {
			now := nowMs()
			if now < st.NextAt && now >= st.NextAt-c.NotifyBeforeMs {
				st.Phase = "pre"
				preType = st.NextType
			}
		}
		s.mu.Unlock()
	default:
		// 固定时刻线：时刻是绝对的，平移没有意义 —— 直接重算“下一个还没到的时刻”，
		// 暂停期间已经过去的时刻不补弹（与外卖提醒同一口径）
		st.Phase = "idle"
		s.mu.Unlock()
		s.replan()
		s.mu.Lock()
		if c, ok := cfg.BreakByID(s.state.NextType); ok &&
			s.state.NextAt > 0 && c.NotifyBeforeMs > 0 &&
			nowMs() >= s.state.NextAt-c.NotifyBeforeMs {
			s.state.Phase = "pre"
		}
		if s.state.Phase == "pre" {
			preType = s.state.NextType
		}
		s.mu.Unlock()
	}
	log.Printf("[scheduler] 恢复（%s）", reason)
	s.emitState()
	// 暂停时正处于“即将休息”阶段的话，恢复后重新弹出提示窗（倒计时继续）
	// 判据在 syncPreBreakWindow：刚弹出的场景 phase=pre 且剩余在提前区间内，不会被收掉
	if preType != "" && allowPre {
		nb := int64(0)
		if c, ok := cfg.BreakByID(preType); ok {
			nb = c.NotifyBeforeMs
		}
		s.host.OnPreNotify(preType, nb)
	}
	s.syncPreBreakWindow()
	// 这里不重锚任何倒计时（暂停不影响倒计时，恢复也就没有补偿）。需要“从头计时”的
	// 只有离开链路（idleWatchTick）：它自己另调一次 ReanchorCountdown（幂等）。
}
