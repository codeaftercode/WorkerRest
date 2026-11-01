package main

import (
	"log"
	"workerrest/internal/model"
	"workerrest/internal/plan"
	"workerrest/internal/settings"
)

// ---------- 内部：计划与状态机 ----------

// cfgByID 取某条休息的配置（先拿配置快照，避免持锁期间碰 App.mu）
func (s *Scheduler) cfgByID(id string) (model.BreakConfig, bool) {
	return s.host.cfgSnapshot().BreakByID(id)
}

// advanceCounter 短休息 → 计数 +1；长休息 → 清零；自定义提醒不参与长短交替。
// 调用方必须已持有 s.mu。
func (s *Scheduler) advanceCounter(id string) {
	if id == "short" {
		s.state.ShortsSinceLong++
	} else if id == "long" {
		s.state.ShortsSinceLong = 0
	}
}

// prevCountdownAnchorsLocked 收集挂起线里仍未到期的倒计时锚点（键 = 休息身份，
// 同身份取最早），并附上产生各锚点的间隔（s.anchorIv）。replan 重算时传回
// schedulerLines / planNextBreakSplit：未到期且间隔未变的锚点原样保留（倒计时
// 进度跨 replan 保留），间隔已改的立刻按新间隔重新起算（见 breakplan.go 的
// countdownAnchor）。调用方必须已持有 s.mu。
//
// 暂停期间「时间冻结」：判锚点过期必须用 PausedAt 而不是真实墙钟。否则暂停中任何一次
// replan（保存设置 / 暂停期间结束一次自定义提醒）都会把冻结锚点当成过期、按
// now+interval 重新起算；恢复时 resume 又整体平移 delta，等于把整段暂停算了两遍
// （用户看到「还剩 50 分钟」，而配置是 20 分钟）。
func (s *Scheduler) prevCountdownAnchorsLocked(now int64) map[string]plan.CdAnchor {
	ref := now
	frozen := false
	if s.state.Paused && s.state.PausedAt > 0 && s.state.PausedAt < ref {
		ref = s.state.PausedAt
		frozen = true
	}
	prev := map[string]plan.CdAnchor{}
	for _, l := range s.state.Lines {
		if l.Source != "countdown" || l.At <= ref {
			continue
		}
		if old, ok := prev[l.Type]; !ok || l.At < old.At {
			prev[l.Type] = plan.CdAnchor{At: l.At, Iv: s.anchorIv[l.Type], Frozen: frozen}
		}
	}
	return prev
}

// syncAnchorIvLocked 按重算后的挂起线同步锚点间隔表：池里每条倒计时线的锚点，
// 其产生间隔就是当前配置间隔（沿用者只在间隔未变时才会沿用，重新起算者天然是新间隔）。
// 调用方必须已持有 s.mu。
func (s *Scheduler) syncAnchorIvLocked(cfg *settings.Settings) {
	s.anchorIv = map[string]int64{}
	for _, l := range s.state.Lines {
		if l.Source != "countdown" {
			continue
		}
		if c, ok := cfg.BreakByID(l.Type); ok {
			s.anchorIv[l.Type] = c.IntervalMs
		}
	}
}

// replan 重算下一次休息（只改 state 里的字段，不发事件、不动 phase）。
// 未到期且间隔未变的倒计时锚点原样保留：设置保存、导入等触发的重算只增删身份，
// 不把已流逝的倒计时进度清零重计；间隔改了的立刻按新间隔重新起算。
func (s *Scheduler) replan() {
	cfg := s.host.cfgSnapshot()
	now := nowMs()
	s.mu.Lock()
	defer s.mu.Unlock()
	st := &s.state
	// 过期键清掉：nextSlotAt 只返回严格晚于 now 的时刻，已过时刻的 occurrence
	// 不可能再被选中，留着只会把集合撑大
	for k := range s.consumed {
		if occurrenceKeyAt(k) <= now {
			delete(s.consumed, k)
		}
	}
	// 上一轮仍未到期的倒计时锚点先收好（必须在覆盖 st.Lines 之前）
	prev := s.prevCountdownAnchorsLocked(now)
	// 整份挂起线留给今日时间线用（state.lines）；planNextBreak 的口径就是它的首条
	pool := schedulerLines(cfg, now, st.ShortsSinceLong, s.consumed, prev)
	st.Lines = pool
	s.syncAnchorIvLocked(cfg)
	// 分组视图只给设置页状态卡用：短/长一组，外卖 + 自定义一组，互不混用。
	// 全局 next（nextType/nextAt）语义不变，托盘不受影响。
	s.refreshDerivedLocked(cfg, now)
	if len(pool) == 0 {
		// 全部关闭、或固定模式下一刻都没配：不再安排任何休息
		st.NextAt = 0
		st.NextSource = nil
		return
	}
	p := pool[0]
	st.NextType = p.Type
	st.NextName = p.Name
	st.NextAt = p.At
	src := p.Source
	st.NextSource = &src
}

// refreshDerivedLocked 刷新状态卡用的派生视图（分组最近一条 + 各固定线「下次」时刻表）。
// 调用方必须已持有 s.mu。倒计时锚点从当前挂起线收集：replan 里是新池（锚点已保留），
// startBreak 消费后是旧线（刚消费那条已到期不会混入，其余身份的进度同样不被清掉）。
func (s *Scheduler) refreshDerivedLocked(cfg *settings.Settings, now int64) {
	prev := s.prevCountdownAnchorsLocked(now)
	split := planNextBreakSplit(cfg, now, s.state.ShortsSinceLong, s.consumed, prev)
	s.state.Rest = planRefOf(split.Builtin)
	s.state.Other = planRefOf(split.Other)
	s.state.FixedNext = fixedNextMap(cfg, now, s.consumed)
}

func planRefOf(p *model.PlannedBreak) *model.PlanRef {
	if p == nil {
		return nil
	}
	return &model.PlanRef{Name: p.Name, At: p.At}
}

// scheduleNext 回到等待态并重排下一次休息
func (s *Scheduler) scheduleNext() {
	s.mu.Lock()
	wasPre := s.state.Phase == "pre"
	preType := s.state.NextType
	s.state.Phase = "idle"
	s.mu.Unlock()
	s.replan()
	// 原来就在「即将休息」、新计划又仍落在提前区间（设置保存触发的重排最常见）：
	// 把 phase 拉回来。否则窗口会被 syncPreBreakWindow 关掉、下一个 500ms tick 再弹一遍，
	// 用户看到右下角窗口闪一下。
	// 新计划被推远 / 被禁用 / 换成了另一条提醒时判据不成立（或窗口内容会对不上），
	// 仍按原逻辑收窗，由下一个 tick 用新内容重弹。
	if wasPre {
		cfg := s.host.cfgSnapshot()
		s.mu.Lock()
		if s.state.NextType == preType {
			if c, ok := cfg.BreakByID(preType); ok &&
				c.Enabled && c.NotifyBeforeMs > 0 && s.state.NextAt > 0 {
				if now := nowMs(); s.state.NextAt > now && now >= s.state.NextAt-c.NotifyBeforeMs {
					s.state.Phase = "pre"
				}
			}
		}
		s.mu.Unlock()
	}
	// 刚从「即将休息」阶段被重排（例如用户在设置页取消勾选了正在倒计时提醒的那条），
	// 关闭残留的提前提示窗 —— 否则它会继续显示已失效提醒的倒计时，用户感知为「仍会提醒」。
	// 新计划若仍在提前区间内，判据成立，窗口不会被收掉。
	s.syncPreBreakWindow()
	s.emitState()
}

// startBreak 到点（或立即休息）弹一次真实休息，返回会话 id（弹不出来时为空串）
func (s *Scheduler) startBreak() string {
	s.mu.Lock()
	kind := s.state.NextType
	at := s.state.NextAt
	s.mu.Unlock()
	return s.startBreakAt(kind, at)
}

// startBreakAt 按指定身份与 occurrence 弹一次休息。at 是这次安排的时刻（去重键用），
// 传 0 表示沿用当前计划的 NextAt；暂停期间到点的自定义提醒也走这里（见 pausedFire）。
func (s *Scheduler) startBreakAt(kind string, at int64) string {
	cfg := s.host.cfgSnapshot()
	s.mu.Lock()
	st := &s.state
	bcfg, ok := cfg.BreakByID(kind)
	if !ok {
		// 计划里的这条休息已被删除：不弹窗，直接重排下一条
		s.mu.Unlock()
		s.scheduleNext()
		return ""
	}
	now := nowMs()
	if at == 0 {
		at = st.NextAt
	}
	st.NextType = kind
	st.Phase = "break"
	st.BreakStartedAt = now
	st.BreakEndsAt = now + bcfg.DurationMs
	// 暂停态下主动发起的休息：不参与暂停的时间冻结（见 pauseFreezesClockLocked）
	st.BreakWhilePaused = st.Paused
	// 这次安排已消费：无论到点触发还是「立即休息」，replan 都不再排它
	if at > 0 {
		s.consumed[occurrenceKey(kind, at)] = true
	}
	// 派生视图立即跟上：刚消费掉的那条不再出现在分组倒计时与卡片摘要的「下次」里
	s.refreshDerivedLocked(cfg, now)
	s.mu.Unlock()

	log.Printf("[scheduler] 开始休息 %s（%d ms，%s）", kind, bcfg.DurationMs, bcfg.Mode)
	// 休息窗口与设置页立即反映「休息中」
	id := s.host.showBreakSession(kind, false, "")
	// phase 已翻到 break：提前提示窗在此刻起不该在场（App 侧建会话时已收，这里兜一道，
	// 保证「窗口只在 phase=pre 存在」这条不变量对所有宿主成立）
	s.syncPreBreakWindow()
	s.emitState()
	return id
}

// finishBreak 结束进行中的休息（自然到点与提前结束共用）
func (s *Scheduler) finishBreak() {
	s.mu.Lock()
	kind := s.state.NextType
	s.state.BreakWhilePaused = false
	s.advanceCounter(kind)
	s.mu.Unlock()

	// break-end 的副作用：关休息窗口、空闲基准清零（空闲计时属下一步的「系统能力」）
	log.Printf("[scheduler] 结束休息 %s", kind)
	s.host.closeBreakWindows()
	s.scheduleNext()
}

// emitState 把状态推给设置页与托盘（对应 上一版的 emit('state')）
func (s *Scheduler) emitState() {
	s.host.onSchedulerState()
}

// ---------- 对外快照 ----------

// stateCopy 深拷贝一份状态（切片与 map 都要拷：调用方会在锁外读它）
func (s *Scheduler) stateCopy() model.SchedulerState {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.state
	out.Lines = append([]model.PlannedBreak{}, s.state.Lines...)
	out.PauseReasons = append([]string{}, s.state.PauseReasons...)
	out.FixedNext = make(map[string]int64, len(s.state.FixedNext))
	for k, v := range s.state.FixedNext {
		out.FixedNext[k] = v
	}
	if out.Rest != nil {
		cp := *out.Rest
		out.Rest = &cp
	}
	if out.Other != nil {
		cp := *out.Other
		out.Other = &cp
	}
	if out.NextSource != nil {
		cp := *out.NextSource
		out.NextSource = &cp
	}
	return out
}

// consumedCopy 已消费键的副本（今日时间线要用；拷贝避免外部拿着活 map 读）
func (s *Scheduler) consumedCopy() map[string]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]bool, len(s.consumed))
	for k := range s.consumed {
		out[k] = true
	}
	return out
}

// stateForRenderer 给设置页的状态快照（对应 main.ts 的 stateForRenderer）。
//
// 外卖推迟中时把 fixedNext.takeout 覆盖成推迟线的到点时刻：外卖的推迟 / 跳过不走
// 调度器（自己记账），不覆盖的话卡片摘要还显示推迟前的旧时刻。
// 今日提醒时间线**每次快照现算**——固定线展开当天剩余（剔除已消费、顺延被推迟那一跳）、
// 倒计时线从挂起那一跳展开成今天剩余的每一次、外卖剔除已触发并附上推迟线。
func (s *Scheduler) stateForRenderer() model.SchedulerState {
	cfg := s.host.cfgSnapshot()
	st := s.stateCopy()
	pp := s.host.takeoutPostponedSnapshot()
	if pp != nil {
		if st.FixedNext == nil {
			st.FixedNext = map[string]int64{}
		}
		st.FixedNext["takeout"] = pp.DueAt
		// 合并组「下次外卖 / 自定义提醒」若当前就是外卖那条，推迟后的时刻也要跟着走
		// （与 FixedNext 同一口径；外卖的推迟 / 跳过不经调度器，不覆盖的话状态卡还显示旧时刻）
		if st.Other != nil && st.Other.Name == plan.TAKEOUT_NAME {
			st.Other = &model.PlanRef{Name: plan.TAKEOUT_NAME, At: pp.DueAt}
		}
	}
	st.Timeline = todayTimeline(cfg, nowMs(), plan.TimelineOptions{
		Skip:             s.consumedCopy(),
		Shifts:           st.FixedNext,
		CountdownLines:   st.Lines,
		Paused:           st.Paused,
		TakeoutFired:     s.host.takeoutFiredSnapshot(),
		TakeoutPostponed: pp,
		ShortsSinceLong:  st.ShortsSinceLong,
	})
	return st
}

// planSummary 日志用的计划摘要
func (s *Scheduler) planSummary() string {
	cfg := s.host.cfgSnapshot()
	now := nowMs()
	s.mu.Lock()
	ssL := s.state.ShortsSinceLong
	prev := s.prevCountdownAnchorsLocked(now)
	s.mu.Unlock()
	p, ok := planNextBreak(cfg, now, ssL, s.consumedCopy(), prev)
	if !ok {
		return describePlan(nil)
	}
	return describePlan(&p)
}

// currentPhase 当前阶段（诊断 / 托盘用）
func (s *Scheduler) currentPhase() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.Phase
}

// isPaused 是否处于暂停态（外卖 watcher 用它跳过「暂停期间不弹」）
func (s *Scheduler) isPaused() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.Paused
}

// hasPauseReason 当前是否挂着某条暂停原因（手动恢复要区分「自动暂停」与「手动暂停」：
// 自动暂停的恢复走临时忽略，手动暂停的恢复走常规 toggle）
func (s *Scheduler) hasPauseReason(reason string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return containsStr(s.state.PauseReasons, reason)
}

// pausedForBusyReason 正因「用户仍在用电脑」而暂停（手动 / 勿扰 / 指定应用占用）。
//
// 这几种状态下不做空闲离开判定：开会、看视频、跑全屏应用时用户当然不动键鼠，
// 那段时间照算在工作。只有 idle 自己这一条会参与空闲判定（否则离开态永远解不开）。
func (s *Scheduler) pausedForBusyReason() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.state.PauseReasons {
		if r != pauseReasonIdle {
			return true
		}
	}
	return false
}

// ---------- 小工具 ----------

func containsStr(a []string, v string) bool { return indexOfStr(a, v) >= 0 }

func indexOfStr(a []string, v string) int {
	for i, x := range a {
		if x == v {
			return i
		}
	}
	return -1
}
