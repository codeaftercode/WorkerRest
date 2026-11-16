package scheduler

import (
	"log"
	"workerrest/internal/model"
	"workerrest/internal/plan"
	"workerrest/internal/settings"
)

// ---------- 内部：计划与状态机 ----------

// cfgByID 取某条休息的配置（先拿配置快照，避免持锁期间碰 App.mu）
func (s *Scheduler) cfgByID(id string) (model.BreakConfig, bool) {
	return s.host.CfgSnapshot().BreakByID(id)
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

// prevCountdownAnchorsLocked 收集挂起线里的倒计时锚点（键 = 休息身份，同身份取最早），
// 并附上产生各锚点的间隔（s.anchorIv）。replan 重算时传回 schedulerLines /
// planNextBreakSplit：未消费且间隔未变的锚点原样保留（倒计时进度跨 replan 保留），
// 间隔已改的立刻按新间隔重新起算（见 breakplan.go 的 countdownAnchor）。
// 调用方必须已持有 s.mu。
//
// **已到点的锚点也收进来**（不再按 `At <= now` 滤掉）：那一跳是“已经到点、还没被任何
// 一拍消费”的现场，丢掉它等于让这一次提醒消失一轮 —— 到点那一瞬保存一次设置、或另一条
// 提醒正占着休息窗口时，用户就是看不到窗也查不到记录。沿用后 NextAt 停在过去，下一拍
// 直接弹；弹完 startBreakAt 记进 consumed，下一次 replan 自然回到 now+interval。
//
// 暂停期间不冻结（2026-11-12 用户口径：倒计时不要停）：暂停中的锚点照样会到期，处置由
// onTick 的暂停分支按各卡“暂停休息时”办（跳过 / 正常提醒 / 推迟）。
func (s *Scheduler) prevCountdownAnchorsLocked(now int64) map[string]plan.CdAnchor {
	prev := map[string]plan.CdAnchor{}
	for _, l := range s.state.Lines {
		if l.Source != "countdown" {
			continue
		}
		if old, ok := prev[l.Type]; !ok || l.At < old.At {
			prev[l.Type] = plan.CdAnchor{At: l.At, Iv: s.anchorIv[l.Type]}
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
	cfg := s.host.CfgSnapshot()
	now := nowMs()
	s.mu.Lock()
	defer s.mu.Unlock()
	st := &s.state
	// 过期键清掉：nextSlotAt 只返回严格晚于 now 的时刻，已过时刻的 occurrence
	// 不可能再被选中，留着只会把集合撑大。
	//
	// 留一分钟的余量：暂停期间到点的固定时刻线会消费“当前这一分钟”的 occurrence
	// （跳过 / 推迟越界，见 pausedPostpone），按 <= now 立刻清掉的话，同一分钟内
	// 每一拍都会重新“到期”再记一次跳过 —— 半秒一条，统计页直接爆。
	for k := range s.consumed {
		if plan.OccurrenceKeyAt(k) <= now-60_000 {
			delete(s.consumed, k)
		}
	}
	// 上一轮仍未到期的倒计时锚点先收好（必须在覆盖 st.Lines 之前）
	prev := s.prevCountdownAnchorsLocked(now)
	// 整份挂起线留给今日时间线用（state.lines）；planNextBreak 的口径就是它的首条
	pool := plan.SchedulerLines(cfg, now, st.ShortsSinceLong, s.consumed, prev)
	// 正在等空闲的那一跳必须补回来：它的时刻在过去，重算时天然被排除（见 idleGate.go）
	pool = s.mergeIdleWaitLocked(cfg, now, pool)
	st.Lines = pool
	s.syncAnchorIvLocked(cfg)
	// 分组视图只给设置页状态卡用：短/长一组，外卖 + 自定义一组，互不混用。
	// 全局 next（nextType/nextAt）语义不变，托盘不受影响。
	s.refreshDerivedLocked(cfg, now)
	if len(pool) == 0 {
		// 全部关闭、或固定模式下一刻都没配：不再安排任何休息。
		//
		// 类型/名字也要一起清空：留着上一轮的 NextType 会让“下次提醒是什么”与
		// “还有多久”自相矛盾（NextAt=0 却被问“下次是哪条”）。设置页的“推迟休息”
		// 按钮就靠 nextType 判断显示，残留值会让一个没有下次提醒的状态也长出这个按钮。
		st.NextType = ""
		st.NextName = ""
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
	// 固定时刻线上挂着“推迟待办”时，dueAt 才是这条提醒真正的弹出时刻（它比时刻表里的
	// 下一班更早）。不顶上来的话，托盘 / 提前提示窗 / 下次触发都还对着原定时刻算，
	// 与侧栏显示的 dueAt 各说一套。
	if q, ok := s.nextPostponedLocked(now); ok && q.dueAt < st.NextAt {
		for _, e := range cfg.BreakEntries() {
			if e.ID != q.kind || !e.Config.Enabled {
				continue
			}
			st.NextType, st.NextName, st.NextAt = q.kind, e.Name, q.dueAt
			src := "fixed"
			st.NextSource = &src
			ref := &model.PlanRef{Name: e.Name, At: q.dueAt}
			if isBuiltinBreakID(q.kind) {
				st.Rest = ref
			} else {
				st.Other = ref
			}
			break
		}
	}
}

// nextPostponedLocked 最近一条还没到点的推迟待办（固定时刻线的推迟）。
// 到点的那条不算：它由 flushPostponed 负责弹出，留在这里会让计划永远指着过去。
// 调用方必须已持有 s.mu。
func (s *Scheduler) nextPostponedLocked(now int64) (*postponedBreak, bool) {
	var pick *postponedBreak
	for _, p := range s.postponed {
		if p.dueAt <= now {
			continue
		}
		if pick == nil || p.dueAt < pick.dueAt {
			pick = p
		}
	}
	return pick, pick != nil
}

// refreshDerivedLocked 刷新状态卡用的派生视图（分组最近一条 + 各固定线“下次”时刻表）。
// 调用方必须已持有 s.mu。倒计时锚点从当前挂起线收集：replan 里是新池（锚点已保留），
// startBreak 消费后是旧线（刚消费那条已到期不会混入，其余身份的进度同样不被清掉）。
func (s *Scheduler) refreshDerivedLocked(cfg *settings.Settings, now int64) {
	prev := s.prevCountdownAnchorsLocked(now)
	split := plan.PlanNextBreakSplit(cfg, now, s.state.ShortsSinceLong, s.consumed, prev)
	s.state.Rest = planRefOf(split.Builtin)
	s.state.Other = planRefOf(split.Other)
	s.state.FixedNext = plan.FixedNextMap(cfg, now, s.consumed)
	s.applyPostponedRefsLocked(cfg, now)
}

// applyPostponedRefsLocked 让“暂停期间被推迟的固定时刻线”出现在状态卡的倒计时格上。
//
// 固定时刻是绝对的，推迟不改配置里的 hour/minute（那一格 12:00 就是 12:00），它只多出
// 一条“改到 dueAt 再弹”的待办。不覆盖的话，侧栏那格会一直显示已经过去的时刻（0:00），
// 用户看不出“它被推到了几点”。倒计时线不走这张表 —— 那条是把挂起线的 At 直接挪走。
// 调用方必须已持有 s.mu。
func (s *Scheduler) applyPostponedRefsLocked(cfg *settings.Settings, now int64) {
	if len(s.postponed) == 0 {
		return
	}
	names := map[string]string{}
	for _, e := range cfg.BreakEntries() {
		names[e.ID] = e.Name
	}
	for _, p := range s.postponed {
		if p.dueAt <= now {
			continue
		}
		name := names[p.kind]
		if name == "" {
			continue
		}
		ref := &s.state.Other
		if isBuiltinBreakID(p.kind) {
			ref = &s.state.Rest
		}
		if *ref == nil || (*ref).At > p.dueAt {
			*ref = &model.PlanRef{Name: name, At: p.dueAt}
		}
	}
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
	// 原来就在“即将休息”、新计划又仍落在提前区间（设置保存触发的重排最常见）：
	// 把 phase 拉回来。否则窗口会被 syncPreBreakWindow 关掉、下一个 500ms tick 再弹一遍，
	// 用户看到右下角窗口闪一下。
	// 新计划被推远 / 被禁用 / 换成了另一条提醒时判据不成立（或窗口内容会对不上），
	// 仍按原逻辑收窗，由下一个 tick 用新内容重弹。
	if wasPre {
		cfg := s.host.CfgSnapshot()
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
	// 刚从“即将休息”阶段被重排（例如用户在设置页取消勾选了正在倒计时提醒的那条），
	// 关闭残留的提前提示窗 —— 否则它会继续显示已失效提醒的倒计时，用户感知为“仍会提醒”。
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
	return s.startBreakAtWith(kind, at, true)
}

// startBreakAtWith 是 startBreakAt 的本体，多一个“要不要把这一班记成已消费”的开关。
//
// markConsumed=false 只给 startQueuedBreak 用：接力弹出的那一班在**并入时**就已经
// 消费过了，而它带进来的 at 是别的提醒的计划时刻 —— 拿它去记 OccurrenceKey(kind, at)，
// 万一撞上这个身份真正的下一班，就会把人家凭空标成已处理（这一班从此消失）。
func (s *Scheduler) startBreakAtWith(kind string, at int64, markConsumed bool) string {
	cfg := s.host.CfgSnapshot()
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
	// 开始休息了，就没人在等空闲了
	s.clearIdleWaitLocked()
	st.NextType = kind
	// 当前这次的“身份”单独记一份：NextType 会在休息中被 replan 改写成下一条，
	// 而这半分钟里“正在休息的是哪条提醒”必须钉住（侧栏“提前结束”文案、托盘那一项
	// 的可点性都认它）。暂停冻结着的那次休息同样保留，见 StateCopy 的出口兜底。
	st.BreakType = kind
	st.Phase = "break"
	st.BreakStartedAt = now
	st.BreakEndsAt = now + bcfg.DurationMs
	// 暂停态下主动发起的休息：不参与暂停的时间冻结（见 pauseFreezesClockLocked）
	st.BreakWhilePaused = st.Paused
	// 这次安排已消费：无论到点触发还是“立即休息”，replan 都不再排它
	if markConsumed && at > 0 {
		s.consumed[plan.OccurrenceKey(kind, at)] = true
	}
	// 新的一次休息：并入记录清零（同一条提醒在下一次休息里可以再并一次）
	s.extraMerged = map[string]bool{}
	// 派生视图立即跟上：刚消费掉的那条不再出现在分组倒计时与卡片摘要的“下次”里
	s.refreshDerivedLocked(cfg, now)
	s.mu.Unlock()

	log.Printf("[scheduler] 开始休息 %s（%d ms，%s）", kind, bcfg.DurationMs, bcfg.Mode)
	// 休息窗口与设置页立即反映“休息中”
	id := s.host.ShowBreakSession(kind, false, "")
	// phase 已翻到 break：提前提示窗在此刻起不该在场（App 侧建会话时已收，这里兜一道，
	// 保证“窗口只在 phase=pre 存在”这条不变量对所有宿主成立）
	s.syncPreBreakWindow()
	s.emitState()
	return id
}

// curBreakLocked 当前这次休息的身份 —— 一律以 BreakType 为准，只有它为空时才退回 NextType。
//
// 为什么不能拿 NextType 顶替：休息期间只要 replan 一次（保存设置、重置周期、时间跳变），
// NextType 就会被改写成“下一条要弹的”，而正在弹的还是原来那条。用它去推进计数，
// 本来是自定义提醒休息结束会把短休息计数 +1（下一班长休息被凭空推远）；长休息到点抢占时
// 又会因为 NextType 已经写着 "long" 而误判成“已经在长休息”，直接放弃抢占。
// 调用方必须已持有 s.mu。
func (s *Scheduler) curBreakLocked() string {
	if s.state.BreakType != "" {
		return s.state.BreakType
	}
	return s.state.NextType
}

// finishBreak 结束进行中的休息（自然到点与提前结束共用）
func (s *Scheduler) finishBreak() {
	s.mu.Lock()
	kind := s.curBreakLocked()
	s.state.BreakWhilePaused = false
	s.advanceCounter(kind)
	s.extraMerged = nil
	// 这次休息的时间字段在收尾这一处清干净。原先只清了 BreakWhilePaused，
	// BreakEndsAt / BreakStartedAt / BreakType 会留在快照里（实测：phase 已经回到 idle，
	// breakEndsAt 还写着“还剩 84 秒”）。眼下每个消费方都先判 phase 才读，所以没出过错，
	// 但那是靠约定撑着 —— 以后谁漏判一次，就会拿到一个已经过去的结束时刻。
	s.state.BreakStartedAt = 0
	s.state.BreakEndsAt = 0
	s.state.BreakType = ""
	s.mu.Unlock()

	// break-end 的副作用：关休息窗口、空闲基准清零（空闲计时属下一步的“系统能力”）
	log.Printf("[scheduler] 结束休息 %s", kind)
	s.host.CloseBreakWindows()
	s.scheduleNext()
}

// emitState 把状态推给设置页与托盘（对应 上一版的 emit('state')）
func (s *Scheduler) emitState() {
	s.host.OnSchedulerState()
}

// ---------- 对外快照 ----------

// stateCopy 深拷贝一份状态（切片与 map 都要拷：调用方会在锁外读它）
func (s *Scheduler) StateCopy() model.SchedulerState {
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
	// 等待空闲：整份状态一个标记，具体是哪一格由 NextType 决定 —— 内置短/长归 Rest 那一格，
	// 自定义提醒归 Other 那一格。前端据此逐格判断，不必自己比对名字（名字可被用户改）。
	out.IdleWait = s.idleWait != nil
	if out.IdleWait {
		if isCustomBreakID(out.NextType) {
			if out.Other != nil {
				out.Other.IdleWait = true
			}
		} else if out.Rest != nil {
			out.Rest.IdleWait = true
		}
	}
	// 休息身份只在 break 相位有效：出相位后一律清空，免得清相位的那七八处漏一个
	// 就留下“已经回到工作中、侧栏却还写着提前结束”的残影。
	if out.Phase != "break" {
		out.BreakType = ""
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
func (s *Scheduler) StateForRenderer() model.SchedulerState {
	cfg := s.host.CfgSnapshot()
	st := s.StateCopy()
	// 固定时刻线被推迟：摘要表那一格要说“改到几点弹”，与 NextAt / 侧栏同一口径。
	// 时间线据 shifts 跳过原时刻并补上这一条，卡片右上角“下次 …”也跟着走；
	// 不改的话摘要里只剩时刻表的下一班，这次推迟在界面上完全看不见。
	s.mu.Lock()
	for _, p := range s.postponed {
		if p.dueAt <= nowMs() {
			continue
		}
		if cur, ok := st.FixedNext[p.kind]; !ok || p.dueAt < cur {
			st.FixedNext[p.kind] = p.dueAt
		}
	}
	s.mu.Unlock()
	pp := s.host.TakeoutPostponedSnapshot()
	if pp != nil {
		if st.FixedNext == nil {
			st.FixedNext = map[string]int64{}
		}
		st.FixedNext["takeout"] = pp.DueAt
		// 合并组“下次外卖 / 自定义提醒”若当前就是外卖那条，推迟后的时刻也要跟着走
		// （与 FixedNext 同一口径；外卖的推迟 / 跳过不经调度器，不覆盖的话状态卡还显示旧时刻）
		if st.Other != nil && st.Other.Name == plan.TAKEOUT_NAME {
			st.Other = &model.PlanRef{Name: plan.TAKEOUT_NAME, At: pp.DueAt}
		}
	}
	// 外卖到点了、还在等空闲：状态卡上那一格写“等待空闲”（比“已到点 0 秒”有用）。
	// 与推迟线互斥 —— 空闲门达标后立刻落成推迟线（armed），挂起当场清掉。
	if g := s.host.TakeoutIdleWaitSnapshot(); g != nil {
		st.Other = &model.PlanRef{Name: plan.TAKEOUT_NAME, At: g.At, IdleWait: true}
	}
	// 这一跳允不允许推迟：设置页侧栏的「推迟休息」按钮据此显隐。判据只认配置里
	// 那条提醒自己的 PostponeMs（0 = 没勾「允许推迟提醒」）；身份查不到就按允许处理
	// —— 查不到说明这条提醒刚被删掉，重排马上会换下一条，藏按钮反而更奇怪。
	//
	// **目标是下一条短 / 长休息，不是全局 NextType**（2026-11-14 用户口径：侧栏那组按钮
	// 全部只针对短 / 长休息，外卖与自定义提醒不参与）。拿全局 NextType 判会错位：
	// 下一条休息明明能推，赶上下一条是“不允许推迟”的喝水提醒，按钮就平白消失了。
	restKind, _, hasRest := s.RestTarget()
	st.RestKind = restKind
	st.PostponeAllowed = false
	if hasRest {
		if c, ok := cfg.BreakByID(restKind); ok {
			st.PostponeAllowed = c.CanPostpone()
		} else {
			st.PostponeAllowed = true
		}
	}
	// 这次休息允不允许提前结束：侧栏「提前结束」按钮据此置灰（勾掉时灰着，而不是
	// 点了才被拒；休息中途把勾补上，下一秒的快照就恢复可点）。每次快照现算、不存进
	// 内部 state，所以配置一变就跟上。身份查不到按允许 —— 与 breakConfigOf 的缺键
	// 兜底、与宿主 schedEndBreak 的拦截同一口径。
	st.AllowEarlyEnd = true
	if st.Phase == "break" && st.BreakType != "" {
		if c, ok := cfg.BreakByID(st.BreakType); ok {
			st.AllowEarlyEnd = c.AllowEarlyEnd
		}
	}
	st.Timeline = plan.TodayTimeline(cfg, nowMs(), plan.TimelineOptions{
		Skip:             s.consumedCopy(),
		Shifts:           st.FixedNext,
		CountdownLines:   st.Lines,
		TakeoutFired:     s.host.TakeoutFiredSnapshot(),
		TakeoutPostponed: pp,
		ShortsSinceLong:  st.ShortsSinceLong,
	})
	return st
}

// planSummary 日志用的计划摘要
func (s *Scheduler) PlanSummary() string {
	cfg := s.host.CfgSnapshot()
	now := nowMs()
	s.mu.Lock()
	ssL := s.state.ShortsSinceLong
	prev := s.prevCountdownAnchorsLocked(now)
	s.mu.Unlock()
	p, ok := plan.PlanNextBreak(cfg, now, ssL, s.consumedCopy(), prev)
	if !ok {
		return plan.DescribePlan(nil)
	}
	return plan.DescribePlan(&p)
}

// currentPhase 当前阶段（诊断 / 托盘用）
func (s *Scheduler) CurrentPhase() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.Phase
}

// isPaused 是否处于暂停态（外卖 watcher 用它跳过“暂停期间不弹”）
func (s *Scheduler) IsPaused() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.Paused
}

// hasPauseReason 当前是否挂着某条暂停原因（手动恢复要区分“自动暂停”与“手动暂停”：
// 自动暂停的恢复走临时忽略，手动暂停的恢复走常规 toggle）
func (s *Scheduler) HasPauseReason(reason string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return containsStr(s.state.PauseReasons, reason)
}

// pausedForBusyReason 正因“用户仍在用电脑”而暂停（手动 / 勿扰 / 指定应用占用）。
//
// 这几种状态下不做空闲离开判定：开会、看视频、跑全屏应用时用户当然不动键鼠，
// 那段时间照算在工作。只有 idle 自己这一条会参与空闲判定（否则离开态永远解不开）。
func (s *Scheduler) PausedForBusyReason() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.state.PauseReasons {
		if r != PauseReasonIdle {
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
