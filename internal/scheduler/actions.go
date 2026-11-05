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
		s.Resume(PauseReasonManual)
	} else {
		s.Pause(PauseReasonManual)
	}
}

// PauseUntil 定时暂停：at 是到期时刻（毫秒），0 表示取消定时、退回无限暂停。
//
// 原因仍然记 manual —— 定时暂停是「我接下来一段时间不工作」，不是软件判定你在开会，
// 所以清场、跳过、托盘文案全走手动那一套；新增的只有「到点自动结束」和「恢复重锚」。
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
		// 不能顺手 Pause(manual) —— 那会把「纯自动暂停」偷偷改成手动暂停（托盘上
		// 从此多一条 manual，条件消失也不会自己恢复）。
		s.mu.Unlock()
		return
	}
	s.state.PauseDeadline = at
	s.pauseTimed = at > 0
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
// 两种暂停的语义刻意不同（判据只有一条：reason 是不是 manual）：
//
//   - 手动暂停：用户自己按的。只收提前提示窗，休息窗继续挂着、剩余时间冻结，
//     恢复后整体平移 —— 短暂暂停不吞他正在走的休息。
//   - 自动暂停：软件判定他此刻在忙或不在（勿扰 / 投屏 / 全屏 / 应用占用 / 空闲）。
//     进入的那一刻就把**所有**提醒窗口收掉（休息窗、提前提示、外卖窗与外卖预通知），
//     暂停期间到点的提醒一律跳过，恢复后按「离开」口径重锚内置倒计时。
//     用户没要求暂停，就不该让任何一扇窗继续留在屏幕上，也不该攒着等恢复再弹。
//
// 已经在暂停态时叠加一条自动原因，同样要清场：那说明此前是手动暂停、休息窗还冻结
// 挂着，而现在系统判定用户在忙，窗口该收。
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
		// 暂停提醒时关闭休息前提示弹窗（托盘暂停与弹窗内暂停共用此路径）
		s.host.OnPaused()
	}
	s.emitState()
}

// clearForAutoPause 自动原因进入暂停时的清场：所有提醒窗口立即收掉，被打断的那次休息
// 记「跳过」（不是「提前结束」—— 用户没走完，也不是他自己点的结束）。
//
// 不在这里重排计划：暂停期间 NextAt 保持暂停前那一刻，RemainingMs 用 PausedAt 折算，
// 状态卡显示的就是「暂停时还剩多久」。真正的重排交给恢复（内置倒计时重锚 / 固定线跳过
// 已过时刻），否则会拿着真实 now 算出「还剩 间隔+暂停时长」的假数字。
func (s *Scheduler) clearForAutoPause() {
	cfg := s.host.CfgSnapshot() // 锁外取，遵守「不持 s.mu 碰 App.mu」的锁序
	s.mu.Lock()
	st := &s.state
	inBreak := st.Phase == "break"
	kind := st.NextType
	skippedMs := int64(0)
	if inBreak {
		if c, ok := cfg.BreakByID(kind); ok {
			skippedMs = c.DurationMs
		}
		// 这次休息名额已用掉：短休息计数照走，与「跳过」同口径（长休息清零）
		s.advanceCounter(kind)
		st.BreakStartedAt = 0
		st.BreakEndsAt = 0
		st.BreakWhilePaused = false
	}
	// pre 与 break 都归 idle：phase=pre 的提示窗已被收掉，状态不能继续停在 pre
	st.Phase = "idle"
	s.mu.Unlock()

	// 宿主握有真实会话（哪一扇窗、预览与否），关窗由它统一做；统计口径留在调度器，
	// 免得同一笔「跳过」在两层各记一次
	s.host.OnAutoPause()
	if inBreak {
		s.host.OnBreakSkipped(kind, skippedMs)
		log.Printf("[scheduler] 自动暂停打断休息 %s（%d ms），按跳过处理", kind, skippedMs)
	}
	s.syncPreBreakWindow()
}

// AutoPauseActive 当前这段暂停里是否有自动原因。外卖模块据此决定「一律跳过」——
// 自动暂停不看用户给外卖配的「暂停休息时」处置。
func (s *Scheduler) AutoPauseActive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.Paused && s.pauseHadAuto
}

// Resume 去掉一条暂停原因；全部清掉时恢复，并把各线的时间计划整体平移/重算。
// 恢复的那一刻若仍落在提前提示区间内，会重新弹出提示窗（暂停时它被收掉了）。
func (s *Scheduler) Resume(reason string) { s.resume(reason, true) }

// ResumeQuiet 与 Resume 同义，只是**不重弹提前提示窗**。
//
// 只给「紧接着就会重锚倒计时」的调用方用（目前是空闲链路：人回来后内置倒计时
// 从头计时，见 ReanchorCountdown）。那种场景下提前提示窗注定要被收掉，先弹一个
// 再关掉，用户看到的是右下角闪一下窗口，日志里还留一条「即将休息（剩余 0 ms）」。
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
	// 摘掉 manual 就等于「这段（定时）手动暂停到此结束」，到期时刻是它的附属物，必须
	// 一起清掉：留着的话托盘会继续显示「还有 25 分钟（到 22:30）」、菜单里还挂着
	// 「取消定时」——而那段暂停已经被用户按掉了，剩下的只是别的自动原因（勿扰 / 投屏
	// 还挂着）。全部恢复时下面主体也会清零，这里只是提前一步覆盖「没恢复彻底」那一支。
	// pauseTimed 不动：真正恢复那一跳是否重锚，仍按「这段暂停里有没有过定时」判。
	if reason == PauseReasonManual {
		st.PauseDeadline = 0
	}
	if len(st.PauseReasons) != 0 || !st.Paused {
		s.mu.Unlock()
		return
	}
	delta := nowMs() - st.PausedAt
	// 这一段暂停里有没有出现过自动原因：有就按「离开」口径重锚内置倒计时。
	// 在真正恢复的这一刻取一次并清零 —— 中途只摘掉一条原因（还有别的挂着）时不算恢复，
	// pauseHadAuto 必须留到真正的恢复那一跳。
	//
	// 定时暂停同理（pauseTimed）：它表达的是「接下来一段时间我不工作」，回来重新计
	// 一个完整周期才合理。跨夜那条尤其要紧 —— 零点前几分钟合盖睡觉时，空闲检测
	// 还没挂上就到了期，不重锚的话第二天开机会接着昨夜冻结的剩余时间走。
	reanchor := s.pauseHadAuto || s.pauseTimed
	s.pauseHadAuto = false
	s.pauseTimed = false
	st.Paused = false
	// 段起点随这一段暂停一起结束。不清的话它会一直留着上一段的时刻：任何「pausedAt != 0
	// 就认为暂停过」的判据都会误读，状态快照里也会带着一个没有意义的旧值。
	// 「已暂停多久」的读者（托盘 / 小窗 / 到期日志）本来就都以 Paused 为前提，这里只是
	// 让字段本身也诚实。
	st.PausedAt = 0
	// 到期时刻随暂停一起结束：留着会让托盘继续显示「还有 0 分钟」，也可能被下一段
	// 暂停误当成自己的截止时间
	st.PauseDeadline = 0
	// 这一段暂停结束了：清掉「已按跳过记账」「已照常弹过」两张 occurrence 表，
	// 下一段暂停重新计
	s.pauseSkipped = map[string]bool{}
	s.pauseFired = map[string]bool{}
	preType := ""
	switch {
	case st.Phase == "break" && st.BreakWhilePaused:
		// 暂停开始**之后**用户主动「立即休息」发起的那次：它一直按真实时钟在走
		//（见 pauseFreezesClockLocked），恢复时不该再按暂停时长平移，否则白送一段休息。
		st.BreakWhilePaused = false
		s.mu.Unlock()
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
		// 倒计时线的恢复有两种口径：
		//   - 手动暂停：冻结整体平移，用户的「还剩 X 分钟」不会因为按了一下暂停而白跑；
		//   - 自动暂停（reanchor）：与空闲离开完全一致 —— 内置倒计时从头计时。
		//     开会 30 分钟回来不该看到「还剩 2 分钟」，那和离开 30 分钟回来从头计时
		//     是同一件事：这段时间用户不在，倒计时不该被他没参与的等待消耗掉。
		//     自定义提醒（cb-）仍按剩余平移：它是用户按自己节奏配的绝对安排。
		oldAt := st.NextAt
		nextType := st.NextType
		newAt := oldAt + delta
		reanchored := false
		if reanchor {
			if kind, at, iv, ok := s.reanchorTargetLocked(cfg, nowMs()); ok && at > oldAt {
				newAt = at
				// 重锚用的就是当前配置间隔：同步锚点间隔表，后续 replan 的沿用判据才成立
				s.anchorIv[kind] = iv
				reanchored = true
				log.Printf("[scheduler] 自动暂停恢复：内置倒计时重新计时 %s → %s",
					plan.FmtClock(oldAt), plan.FmtClock(at))
			}
		}
		st.NextAt = newAt
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
		// 分组倒计时同步：重锚的那条直接取新时刻（与 ReanchorCountdown 同口径），
		// 平移时才是「旧值 + 时长」。重锚却按 +delta 算的话，状态卡会显示一个
		// 「满间隔 + 暂停时长」的假倒计时（组内混着固定线的近似误差由下一次 replan 纠正）。
		if reanchored {
			if st.Rest != nil && st.Rest.At == oldAt {
				st.Rest = &model.PlanRef{Name: st.Rest.Name, At: newAt}
			}
			if st.Other != nil && st.Other.At == oldAt {
				st.Other = &model.PlanRef{Name: st.Other.Name, At: newAt}
			}
		} else {
			if st.Rest != nil {
				st.Rest.At += delta
			}
			if st.Other != nil {
				st.Other.At += delta
			}
		}
		// 重锚把计划推到「现在 + 间隔」：phase 必须拉回等待态。停在 pre 的话，
		// 下一跳不会重新判「即将休息」，窗口判据却已不成立 —— 状态与窗口各说各话。
		if reanchored {
			st.Phase = "idle"
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
	if preType != "" && allowPre {
		nb := int64(0)
		if c, ok := cfg.BreakByID(preType); ok {
			nb = c.NotifyBeforeMs
		}
		s.host.OnPreNotify(preType, nb)
	}
	s.syncPreBreakWindow()
	// 自动暂停（含空闲）的内置倒计时已在上面重锚；手动暂停仍按平移，调用方若走的是
	// 离开链路（idleWatchTick）才需要再调一次 ReanchorCountdown（幂等）。
}
