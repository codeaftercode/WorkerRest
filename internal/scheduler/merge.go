package scheduler

// 撞车处理（2026-11-13 用户口径）：
//
//	① 长休息碰上正在进行的别的休息 → 立刻停掉那一次、换成这次长休息。
//	   被停掉的若是短休息，统计上记「完成 1 次」，**不是**提前结束。
//	② 其他提醒（自定义提醒 / 外卖）在休息期间到点 → 并入当前这扇休息窗（单窗多条），
//	   不另开窗口、也不等休息结束补弹。
//
// 两种情形都在 onTick 的“休息进行中”分支里判：原先那里直接 return，结果另一条提醒
// 到点后既没人弹也没人记账 —— 睡过去一整轮，用户既看不到窗、也查不到任何痕迹。

import (
	"log"
	"time"

	"workerrest/internal/model"
	"workerrest/internal/plan"
)

// preemptShortWithLong 长休息到点、而当前正在进行的是另一次休息：立刻换成这次长休息。
// 返回是否发生了抢占（true 时调用方不要再走收窗逻辑）。
//
// 为什么长休息优先：两者都是“离开屏幕歇一会儿”，长休息是更完整的那一次 —— 让短休息
// 先走完再排长休息，用户会在十分钟里被连着拦两次；而只保留长休息，短休息想达到的效果
// 已经包含在里面了。这条规则对“同一刻都到点”同样成立（那种情况下 pool 的头一条就是长休息，
// 短休息会在下一拍的 mergeDueExtras 里按“已被覆盖”记完成）。
//
// 抢占用 **CompleteBreakWindows** 收尾：短休息确实是被顶替的，不是用户提前溜号，
// 所以统计记「完成」而不是「提前结束」；同时不出结束提示音 —— 紧接着就开始长休息，
// 两声叠在一起只是噪音。
func (s *Scheduler) preemptShortWithLong() bool {
	now := nowMs()
	s.mu.Lock()
	// 正在弹的是谁，以 BreakType 为准（见 curBreakLocked）：休息期间 replan 会把 NextType
	// 改写成下一条，拿它判“是不是已经在长休息”会让一次真的长休息抢占被静默放弃。
	cur := ""
	if s.state.Phase == "break" {
		cur = s.curBreakLocked()
	}
	if cur == "" || cur == "long" {
		s.mu.Unlock()
		return false
	}
	at := int64(0)
	for _, l := range s.state.Lines {
		if l.Type != "long" || l.At > now {
			continue
		}
		if at == 0 || l.At < at {
			at = l.At
		}
	}
	if at == 0 {
		s.mu.Unlock()
		return false
	}
	// 短休息计数照“完成一次”推进（被覆盖也是歇了一次）；其余身份不参与长短交替
	if cur == "short" {
		s.advanceCounter(cur)
	}
	s.state.Phase = "idle"
	s.state.BreakWhilePaused = false
	s.state.BreakStartedAt = 0
	s.state.BreakEndsAt = 0
	s.mu.Unlock()

	log.Printf("[scheduler] 长休息到点（%s）：立刻结束正在进行的 %s，换成这次长休息（记完成，不算提前结束）",
		time.UnixMilli(at).Format("15:04:05"), cur)
	s.host.CompleteBreakWindows()
	s.startBreakAt("long", at)
	return true
}

// mergeDueExtras 休息进行中，把其他已经到点的提醒并入当前会话（单窗多条）。
//
// 同一身份在一次休息里只并入一次：长休息 20 分钟、喝水提醒 5 分钟一次时，用户该看到的
// 是「这次休息同时覆盖了喝水提醒」，而不是四张一模一样的卡。
//
// 并入之后这一班必须**往前推**，否则休息一结束 replan 就会看见一个停在过去的锚点，
// 又变成“紧接着补弹一次”：
//   - 倒计时线：锚点推到“原定 + n×间隔”，“n 取最小的、落点在休息结束之后”的值 ——
//     节奏照旧，原定 09:50 的下一次仍是 10:20（而不是休息结束那一刻重新起算）；
//   - 固定时刻线：把区间内**每一班**都记成已消费。只记当前这一班的话，休息跨过两班时
//     剩下那班会在 replan 时被 `nextSlotAt(from=now)` 直接跳过，凭空消失。
//
// 顺序是“先问宿主收不收，再消费”：宿主那边没有可挂的休息会话（窗口被 Alt+F4 关掉、
// 或建窗失败）时这一班原样留着，等休息结束由正常路径提醒 —— 先消费再发现没地方显示，
// 就是又一次静默丢失，正是这个函数要消灭的东西。
func (s *Scheduler) mergeDueExtras() {
	cfg := s.host.CfgSnapshot()
	now := nowMs()
	type cand struct {
		kind    string
		at      int64
		builtin bool
		fixed   bool
		iv      int64
		slots   []model.TimeSlot
	}
	s.mu.Lock()
	if s.state.Phase != "break" {
		s.mu.Unlock()
		return
	}
	cur := s.curBreakLocked()
	ends := s.state.BreakEndsAt
	if ends <= 0 {
		ends = now
	}
	if s.extraMerged == nil {
		s.extraMerged = map[string]bool{}
	}
	var cands []cand
	for i := range s.state.Lines {
		l := &s.state.Lines[i]
		if l.Type == cur || l.At > now || s.extraMerged[l.Type] {
			continue
		}
		c, ok := cfg.BreakByID(l.Type)
		if !ok || !c.Enabled {
			continue
		}
		cands = append(cands, cand{
			kind: l.Type, at: l.At, builtin: isBuiltinBreakID(l.Type),
			fixed: c.Mode == "fixed", iv: c.IntervalMs, slots: c.Slots,
		})
	}
	s.mu.Unlock()
	if len(cands) == 0 {
		return
	}
	// 宿主确认（内置休息不需要窗口，直接记）
	taken := make([]cand, 0, len(cands))
	for _, cd := range cands {
		if cd.builtin || s.host.OnBreakExtra(cd.kind, cd.at) {
			taken = append(taken, cd)
		} else {
			log.Printf("[scheduler] 休息进行中 %s 到点，但当前没有可挂的休息窗口：保留这一班，之后再提醒",
				cd.kind)
		}
	}
	if len(taken) == 0 {
		return
	}
	s.mu.Lock()
	for _, cd := range taken {
		s.extraMerged[cd.kind] = true
		s.consumed[plan.OccurrenceKey(cd.kind, cd.at)] = true
		if cd.fixed {
			at := cd.at
			for at <= ends {
				s.consumed[plan.OccurrenceKey(cd.kind, at)] = true
				next, ok := plan.NextSlotAt(cd.slots, at)
				if !ok || next.At > ends {
					break
				}
				at = next.At
			}
		} else {
			iv := cd.iv
			if iv <= 0 {
				iv = 60_000
			}
			at := cd.at + iv
			for at <= ends {
				at += iv
			}
			// 按“身份 + 原时刻”重找：中间可能发生过 replan，索引不能跨锁用
			for i := range s.state.Lines {
				if s.state.Lines[i].Type == cd.kind && s.state.Lines[i].At == cd.at {
					s.state.Lines[i].At = at
					break
				}
			}
		}
		if cd.builtin {
			// 内置休息被另一次休息覆盖：算“完成一次”，不弹卡（用户口径）
			s.advanceCounter(cd.kind)
			s.mu.Unlock()
			log.Printf("[scheduler] 休息进行中 %s 到点：已被当前这次休息覆盖，记一次完成", cd.kind)
			s.host.OnBreakCounted(cd.kind)
			s.mu.Lock()
		}
	}
	s.mu.Unlock()
}
