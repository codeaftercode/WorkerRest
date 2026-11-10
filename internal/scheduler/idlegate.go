package scheduler

import (
	"log"
	"workerrest/internal/model"
	"workerrest/internal/plan"
	"workerrest/internal/settings"
)

// ---------- 空闲门（“鼠标键盘无活动 x 时间”才提醒） ----------
//
// 需求（2026-11-08）：每条提醒可以勾一个“鼠标键盘无活动 x 时间”。勾上之后，到了触发
// 时刻不马上弹，而是把这次安排挂起，等键鼠连续无活动达到 x 才继续：
//
//	到点 ──空闲未达──> 等待空闲（侧栏写“等待空闲”，不消费这一跳）
//	                     │
//	                     └─空闲达标─> 提前提示窗（提前量 x 秒）─> 正式提醒窗
//
// 提前量为 0 时跳过提示窗，达标即弹正式窗。
//
// 为什么“等待中”要把 NextAt 留在过去：那一跳确实已经到点了，只是人还在忙。把 NextAt
// 往前推（比如每秒重排一次）会让“已到点”这件事被时钟抹掉——人一离开工位又要重新等
// 一个间隔；而且每次重排都会把倒计时锚点刷新一遍。留在过去则语义诚实：状态卡只能写
// “等待空闲”（剩余时间恒为 0），达标那一刻立刻接着走。
//
// 等待中的那一跳在 replan 时会被重算掉（replan 只排“还没到的时刻”），所以每次重算都
// 得由 mergeIdleWaitLocked 显式补回——否则用户随手改个设置（每次 autoSave 都会 replan）
// 就会把刚出现的“等待空闲”抹掉。

// idleWait 正在等空闲的那一跳。nil = 没有在等。
type idleWait struct {
	kind   string // 休息身份（short / long / cb-…）
	name   string // 展示名（replan 补回时不用再查配置）
	at     int64  // 原定时刻（occurrence 身份，也是“已到点”的那个时刻）
	source string // countdown | fixed
}

// clearIdleWaitLocked 结束等待（跳过 / 推迟 / 开始休息 / 重锚之后都调用）。
// 调用方必须已持有 s.mu。
func (s *Scheduler) clearIdleWaitLocked() { s.idleWait = nil }

// IdleWaitPending 现在是否有提醒在等空闲（宿主的空闲链路据此判断“这一拍该不该判离开”）：
// 等待中的提醒本来就要等人离开工位，这段时间的自动暂停只会把它卡死。
func (s *Scheduler) IdleWaitPending() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.idleWait != nil
}

// mergeIdleWaitLocked 把“正在等空闲”的那一跳补回重算后的挂起线。
//
// 失效判据（任一成立就不再等）：这条提醒被关掉 / 删掉、空闲门被取消、这一跳已经被消费
// （跳过、立即休息过）。补回时按原时刻（过去）插入，因此它必然是 pool 的第一条，
// st.NextAt 也就跟着回到“已到点”的状态。
func (s *Scheduler) mergeIdleWaitLocked(cfg *settings.Settings, now int64, pool []model.PlannedBreak) []model.PlannedBreak {
	w := s.idleWait
	if w == nil {
		return pool
	}
	c, ok := cfg.BreakByID(w.kind)
	if !ok || !c.Enabled || c.IdleGateWaitMs() <= 0 || s.consumed[plan.OccurrenceKey(w.kind, w.at)] {
		s.idleWait = nil
		return pool
	}
	for _, p := range pool {
		if p.Type == w.kind && p.At == w.at {
			return pool
		}
	}
	pool = append(pool, model.PlannedBreak{Type: w.kind, Name: w.name, At: w.at, Source: w.source})
	plan.SortPlans(pool)
	return pool
}

// armGatedBreakLocked 空闲门刚刚达标：把这一跳就地重排成“从现在起算提前通知”。
//
// 返回 true 表示已经重排（下一拍起走原来的“提前提示窗 → 到点开休息”链路）；
// 返回 false 表示提前通知为 0，调用方直接开正式提醒窗即可。
//
// 为什么要改 NextAt 而不是另立一套“待弹”状态：提前提示窗的在场判据、倒计时、暂停恢复
// 重弹、窗口自愈，全都挂在“phase=pre 且 now 落在 NextAt-提前量 之内”这一条上。把 NextAt
// 挪到 now+提前量，整条链路原样复用，一行新判断都不用加。
func (s *Scheduler) armGatedBreakLocked(cfg model.BreakConfig, kind string, at, now int64) bool {
	if cfg.NotifyBeforeMs <= 0 {
		return false
	}
	newAt := now + cfg.NotifyBeforeMs
	s.idleWait = nil
	st := &s.state
	st.NextAt = newAt
	// phase 必须回等待态：停在 pre 的话下一拍不会再判“即将休息”，窗口判据也还不成立
	st.Phase = "idle"
	for i := range st.Lines {
		if st.Lines[i].Type == kind && st.Lines[i].At == at {
			st.Lines[i].At = newAt
		}
	}
	plan.SortPlans(st.Lines)
	// 分组倒计时跟着走（与 Postpone 同口径）：状态卡上的“下次短休息”要指向重排后的时刻
	if kind == "short" || kind == "long" {
		if st.Rest != nil && st.Rest.At == at {
			st.Rest = &model.PlanRef{Name: st.Rest.Name, At: newAt}
		}
	} else if st.Other != nil && st.Other.At == at {
		st.Other = &model.PlanRef{Name: st.Other.Name, At: newAt}
	}
	if _, has := st.FixedNext[kind]; has {
		st.FixedNext[kind] = newAt
	}
	// 这次安排已消费：即使这条线是固定时刻（下一跳会算成明天），也不该把今天这一跳
	// 再排回来弹第二次
	s.consumed[plan.OccurrenceKey(kind, at)] = true
	log.Printf("[scheduler] %s 空闲门达标：%d 秒后进入提前提示（原定 %s）", kind,
		cfg.NotifyBeforeMs/1000, plan.FmtClock(at))
	return true
}

// fireIdleGateWhileIdlePaused 等待空闲的提醒，碰上“系统空闲自动暂停”时该怎么办。
//
// 空闲自动暂停表达的正是“人离开工位了”，而空闲门要等的就是这一刻 —— 两者撞在一起时
// 暂停不该拦它。不特判的话，空闲门时长一旦大于「系统空闲 N 分钟自动暂停」（出厂 5 分钟），
// 这一跳会永远等不到：人离开时被暂停拦下，人回来时空闲又清零。用户会以为功能坏了。
//
// 只在“暂停原因只有空闲这一条”时放行：手动暂停、勿扰、投屏、应用占用都仍然拦着 ——
// 那些是用户明确说了“现在别打扰我”的场景。
//
// 这条路径直接开正式提醒窗，不走提前提示：自动暂停刚把所有窗口收掉，提前提示窗即使
// 弹出来也会在 500ms 内被自愈判据收走（PreBreakWanted 在暂停态恒为 false）。
func (s *Scheduler) fireIdleGateWhileIdlePaused() bool {
	s.mu.Lock()
	w := s.idleWait
	onlyIdle := len(s.state.PauseReasons) == 1 && s.state.PauseReasons[0] == PauseReasonIdle
	s.mu.Unlock()
	if w == nil || !onlyIdle {
		return false
	}
	cfg, ok := s.host.CfgSnapshot().BreakByID(w.kind)
	if !ok || !cfg.Enabled || cfg.IdleGateWaitMs() <= 0 {
		s.mu.Lock()
		s.clearIdleWaitLocked()
		s.mu.Unlock()
		return false
	}
	if s.host.IdleMs() < cfg.IdleGateWaitMs() {
		return false
	}
	s.mu.Lock()
	s.clearIdleWaitLocked()
	s.mu.Unlock()
	log.Printf("[scheduler] %s 空闲门达标（空闲自动暂停中，直接弹正式提醒）", w.kind)
	s.startBreakAt(w.kind, w.at)
	return true
}
