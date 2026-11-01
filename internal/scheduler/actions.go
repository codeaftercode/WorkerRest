package scheduler

import (
	"log"
	"workerrest/internal/model"
	"workerrest/internal/plan"
)

// ---------- 对外的动作（托盘 / 设置页 / 快捷键 / 提前提示窗都汇聚到这里） ----------

// resetCycle 重置周期：休息进行中先正常收尾（关窗口、落账），再重置——
// 否则状态归零了窗口还挂着，直到下一次休息开始才被顺带关掉。
func (s *Scheduler) ResetCycle() {
	s.mu.Lock()
	inBreak := s.state.Phase == "break"
	s.mu.Unlock()
	if inBreak {
		s.EndBreak()
	}
	s.mu.Lock()
	s.state.ShortsSinceLong = 0
	// 显式重置周期：挂起线清空，本次重排全部按当前配置重新起算（不保留旧倒计时进度）
	s.state.Lines = []model.PlannedBreak{}
	s.anchorIv = map[string]int64{}
	// 推迟表也一起清：显式重置就是要按新配置从头来
	s.postponed = map[string]*postponedBreak{}
	s.mu.Unlock()
	s.scheduleNext()
}

// breakNow 立即开始一次休息（跳过等待）
func (s *Scheduler) BreakNow() {
	s.BreakNowOf("")
}

// breakNowOf 立即开始一次休息，kind 留空 = 用当前计划的那一条。
// 指定 kind 时先把 nextType 顶上去（诊断 / 托盘「来一次长休息」用），
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

// postpone 推迟当前休息（仅在未进入休息窗口时有效）
func (s *Scheduler) Postpone() {
	cfg := s.host.CfgSnapshot()
	s.mu.Lock()
	st := &s.state
	if st.Phase == "break" || st.Paused || st.NextAt == 0 {
		s.mu.Unlock()
		return
	}
	nextType := st.NextType
	bcfg, ok := cfg.BreakByID(nextType)
	if !ok {
		// 计划里的这条休息已被删除：没有「推迟多久」可言，直接重排下一条
		s.mu.Unlock()
		s.scheduleNext()
		return
	}
	ms := bcfg.PostponeMs
	oldAt := st.NextAt
	// 基准取「现在」与「原定时刻」的较晚者：推迟只能是往后推。
	// 早先直接写 now+ms，工作态里点一次「推迟」会把 30 分钟后的提醒拉到 5 分钟后
	// （既违反「推迟」的语义，也把统计里的「推后时长」记成了负数般的假值）。
	base := nowMs()
	if oldAt > base {
		base = oldAt
	}
	st.NextAt = base + ms
	st.Phase = "idle"
	// 统计：记一次推迟 + 推后时长。nextType 也可能是自定义提醒（cb-…），
	// 但统计页「休息」四项只认短 / 长休息 —— 过滤在 App.onBreakPostponed 里一处做掉。
	s.host.OnBreakPostponed(nextType, ms)
	// 被推迟的那条线在挂起线表里同步平移（今日时间线读的是这份表）
	for i := range st.Lines {
		if st.Lines[i].Type == nextType && st.Lines[i].At == oldAt {
			st.Lines[i].At = st.NextAt
		}
	}
	plan.SortPlans(st.Lines)
	// 被推迟的这条必是其分组里最早的一条（它是全局最早）：状态卡上的分组倒计时跟着走
	if nextType == "short" || nextType == "long" {
		if st.Rest != nil {
			st.Rest = &model.PlanRef{Name: st.Rest.Name, At: st.NextAt}
		}
	} else if st.Other != nil {
		st.Other = &model.PlanRef{Name: st.Other.Name, At: st.NextAt}
	}
	// 固定时刻线的推迟也要落到摘要表：卡片右上角的「下次 …」跟着推迟后的时刻走
	//（倒计时模式不在表里，无此键时跳过）
	if _, has := st.FixedNext[nextType]; has {
		st.FixedNext[nextType] = st.NextAt
	}
	s.mu.Unlock()
	// 统计用：不管推迟来自托盘、快捷键还是休息前提示窗，都从这里过一遍
	log.Printf("[scheduler] 推迟 %s %d ms", nextType, ms)
	s.emitState()
	// 推迟把原定时刻推远 → 已不在提前区间：托盘 / 侧栏 / 快捷键这几条路径不经过
	// PreBreakAction，必须在这里收窗（否则窗口继续显示已失效的提醒倒计时）
	s.syncPreBreakWindow()
}

// skip 跳过当前休息，直接进入下一周期。
//
// 休息进行中点「跳过 / 提前结束」（设置页侧栏、托盘、快捷键都汇聚到这里）：
// 语义上就是立刻结束这次休息，落 endBreak 路径——早先直接 return，
// 界面上点了没反应，用户只能去休息窗口里另找入口。
func (s *Scheduler) Skip() {
	cfg := s.host.CfgSnapshot()
	s.mu.Lock()
	st := &s.state
	if st.Phase == "break" {
		s.mu.Unlock()
		log.Printf("[scheduler] 休息中「跳过」→ 提前结束本次休息")
		s.finishBreak()
		return
	}
	if st.Paused || st.NextAt == 0 {
		s.mu.Unlock()
		return
	}
	nextType := st.NextType
	bcfg, ok := cfg.BreakByID(nextType)
	if !ok {
		s.mu.Unlock()
		s.scheduleNext()
		return
	}
	skippedMs := bcfg.DurationMs
	s.advanceCounter(nextType)
	// 固定时刻线：不消费的话 replan 会把同一时刻原样排回来，「跳过」等于没跳
	s.consumed[plan.OccurrenceKey(nextType, st.NextAt)] = true
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
		s.Resume("manual")
	} else {
		s.Pause("manual")
	}
}

// pause 加一条暂停原因。多条原因并存，全部清掉才真的恢复。
func (s *Scheduler) Pause(reason string) {
	s.mu.Lock()
	if containsStr(s.state.PauseReasons, reason) {
		s.mu.Unlock()
		return
	}
	s.state.PauseReasons = append(s.state.PauseReasons, reason)
	if s.state.Paused {
		s.mu.Unlock()
		return
	}
	s.state.Paused = true
	s.state.PausedAt = nowMs()
	s.mu.Unlock()
	log.Printf("[scheduler] 暂停（%s）", reason)
	s.emitState()
	// 暂停提醒时关闭休息前提示弹窗（托盘暂停与弹窗内暂停共用此路径）
	s.host.OnPaused()
}

// resume 去掉一条暂停原因；全部清掉时恢复，并把各线的时间计划整体平移/重算
func (s *Scheduler) Resume(reason string) {
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
	if len(st.PauseReasons) != 0 || !st.Paused {
		s.mu.Unlock()
		return
	}
	delta := nowMs() - st.PausedAt
	st.Paused = false
	// 这一段暂停结束了：清掉「已按跳过记账」「已照常弹过」两张 occurrence 表，
	// 下一段暂停重新计
	s.pauseSkipped = map[string]bool{}
	s.pauseFired = map[string]bool{}
	preType := ""
	switch {
	case st.Phase == "break":
		// 剩余时间冻结整体平移（短暂暂停不吞用户的休息时间）；但总休息时长
		//（含暂停期间）封顶为本次配置时长——暂停期间本来就在休息。
		// 空闲暂停不会在休息窗口期间发生（onTick 见 phase === 'break' 直接返回），
		// 所以能落到这里的只有手动 / 勿扰 / 指定应用占用三种暂停；它们可能持续很久
		//（开着勿扰看了一下午视频），封顶照旧需要：否则休息会被拖成遥遥无期的
		//「休息中」，窗口不置顶时用户只能干等。
		dur := int64(0)
		if c, ok := cfg.BreakByID(st.NextType); ok {
			dur = c.DurationMs
		}
		limit := st.BreakEndsAt + delta
		if capAt := st.BreakStartedAt + dur; limit > capAt {
			limit = capAt
		}
		if nowMs() >= limit {
			st.BreakEndsAt = nowMs()
			s.mu.Unlock()
			log.Printf("[scheduler] 恢复（%s）：休息已到总时长上限，直接收尾", reason)
			s.finishBreak()
			s.emitState()
			return
		}
		st.BreakEndsAt = limit
		s.mu.Unlock()
	case st.NextSource != nil && *st.NextSource == "countdown":
		// 倒计时线冻结：平移即可，用户的「还剩 X 分钟」不会因为离开而白跑
		oldAt := st.NextAt
		nextType := st.NextType
		st.NextAt += delta
		newAt := st.NextAt
		for i := range st.Lines {
			l := &st.Lines[i]
			if l.Source == "countdown" && l.Type == nextType && l.At == oldAt {
				l.At = newAt
			}
		}
		// 暂停期间被「推迟提醒」推后的那一条跟线一起平移：occurrence 去重键保持一致，
		// 推迟后的窗口弹过之后不会再被重排回来弹第二次
		if newAt != oldAt {
			for k, p := range s.postponed {
				if p.kind == nextType && p.at == oldAt {
					delete(s.postponed, k)
					p.at = newAt
					s.postponed[plan.OccurrenceKey(p.kind, p.at)] = p
				}
			}
		}
		plan.SortPlans(st.Lines)
		// 分组倒计时同步平移（组内若混着固定时刻线，近似误差由下一次 replan 纠正——仅显示用）
		if st.Rest != nil {
			st.Rest.At += delta
		}
		if st.Other != nil {
			st.Other.At += delta
		}
		// 与固定线分支一致：恢复后立即重判「即将休息」阶段，不依赖下一个 tick。
		// 暂停期间若 NextAt 被平移进提前区间内，或暂停前本就在 pre，都应立刻回到提示窗。
		//
		// 已过期（now >= NextAt）时不弹：那一跳该由 onTick 直接开始休息，弹一个
		//「还剩 0 秒」的提示窗只是闪一下；更重要的是「离开期间刚好到点」那种 case ——
		// 弹窗紧接着就被 reanchorCountdown 推回整轮间隔，窗口会挂着一整段倒计时。
		if c, ok := cfg.BreakByID(nextType); ok && st.NextAt > 0 && c.NotifyBeforeMs > 0 {
			now := nowMs()
			if now < st.NextAt && now >= st.NextAt-c.NotifyBeforeMs {
				st.Phase = "pre"
				preType = nextType
			}
		}
		s.mu.Unlock()
	default:
		// 固定时刻线：时刻是绝对的，平移没有意义 —— 直接重算「下一个还没到的时刻」，
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
	// 暂停时正处于「即将休息」阶段的话，恢复后重新弹出提示窗（倒计时继续）
	// 判据在 syncPreBreakWindow：刚弹出的场景 phase=pre 且剩余在提前区间内，不会被收掉
	if preType != "" {
		nb := int64(0)
		if c, ok := cfg.BreakByID(preType); ok {
			nb = c.NotifyBeforeMs
		}
		s.host.OnPreNotify(preType, nb)
	}
	s.syncPreBreakWindow()
	// 离开结束后内置倒计时重新从头计时由调用方接着调 reanchorCountdown()
}
