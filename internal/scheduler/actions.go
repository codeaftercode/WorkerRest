package scheduler

import (
	"log"
	"workerrest/internal/model"
	"workerrest/internal/plan"
	"workerrest/internal/settings"
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
	// 推迟表与“等待空闲”的挂起也一起清：显式重置就是要按新配置从头来
	s.postponed = map[string]*postponedBreak{}
	s.clearIdleWaitLocked()
	s.mu.Unlock()
	s.scheduleNext()
}

// breakNow 立即开始一次休息（跳过等待）
func (s *Scheduler) BreakNow() {
	s.BreakNowOf("")
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
		// 计划里的这条休息已被删除：没有“推迟多久”可言，直接重排下一条
		s.mu.Unlock()
		s.scheduleNext()
		return
	}
	ms := bcfg.PostponeMs
	oldAt := st.NextAt
	// 推迟意味着“这一跳到点了但先不提醒”，等待空闲的挂起一并结束：新时刻由推迟算出来，
	// 到点后如果空闲门还开着，会重新进入等待（见 onTick 的空闲门分支）
	s.clearIdleWaitLocked()
	// 基准取“现在”与“原定时刻”的较晚者：推迟只能是往后推。
	// 早先直接写 now+ms，工作态里点一次“推迟”会把 30 分钟后的提醒拉到 5 分钟后
	// （既违反“推迟”的语义，也把统计里的“推后时长”记成了负数般的假值）。
	base := nowMs()
	if oldAt > base {
		base = oldAt
	}
	st.NextAt = base + ms
	st.Phase = "idle"
	// 统计：记一次推迟 + 推后时长。nextType 也可能是自定义提醒（cb-…），
	// 但统计页“休息”四项只认短 / 长休息 —— 过滤在 App.onBreakPostponed 里一处做掉。
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
	// 固定时刻线的推迟也要落到摘要表：卡片右上角的“下次 …”跟着推迟后的时刻走
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
// 休息进行中点“跳过 / 提前结束”（设置页侧栏、托盘、快捷键都汇聚到这里）：
// 语义上就是立刻结束这次休息，落 endBreak 路径——早先直接 return，
// 界面上点了没反应，用户只能去休息窗口里另找入口。
func (s *Scheduler) Skip() {
	cfg := s.host.CfgSnapshot()
	s.mu.Lock()
	st := &s.state
	if st.Phase == "break" {
		s.mu.Unlock()
		log.Printf("[scheduler] 休息中“跳过”→ 提前结束本次休息")
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
	// 跳过消费了这次安排：等待空闲的挂起也该随之结束
	s.clearIdleWaitLocked()
	// 固定时刻线：不消费的话 replan 会把同一时刻原样排回来，“跳过”等于没跳
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
// 原因仍然记 manual —— 定时暂停是“我接下来一段时间不工作”，不是软件判定你在开会，
// 所以清场、跳过、托盘文案全走手动那一套；新增的只有“到点自动结束”和“恢复重锚”。
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
// 进入暂停时按原因分两种动作：
//
//   - 手动暂停：只收提前提示窗，休息窗继续挂着、剩余时间冻结，恢复后整体平移 ——
//     短暂暂停不吞他正在走的休息。
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
// pausedFire / pausedPostpone。时钟口径也同构：恢复时按剩余平移，不再按“离开”重锚
// （那条只留给定时暂停与纯空闲离开，见 resume 里的 reanchor）。
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
// 不在这里重排计划：暂停期间 NextAt 保持暂停前那一刻，RemainingMs 用 PausedAt 折算，
// 状态卡显示的就是“暂停时还剩多久”。真正的重排交给恢复（内置倒计时平移 / 固定线跳过
// 已过时刻），否则会拿着真实 now 算出“还剩 间隔+暂停时长”的假数字。
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

// AutoPauseActive 当前这段暂停里是否有自动原因。**只用于表达“这段暂停不是用户按的”**
// （暂停提示 pause_notice.go 据此决定要不要提醒他“已暂停多久”），不参与任何提醒的处置：
// 到点怎么办一律看各卡自己的“暂停休息时”，与暂停原因无关（2026-11-12 用户口径）。
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
	// pauseTimed 不动：真正恢复那一跳是否重锚，仍按“这段暂停里有没有过定时”判。
	if reason == PauseReasonManual {
		st.PauseDeadline = 0
	}
	if len(st.PauseReasons) != 0 || !st.Paused {
		s.mu.Unlock()
		return
	}
	// 暂停起点：下面清零之前先留住，delta 用它算
	pausedAt := st.PausedAt
	delta := nowMs() - pausedAt
	// 这段暂停里有没有计划被按真实时间重排过（见 Scheduler.pauseReplanned）：
	// 有就不在下面 +delta，那条时刻本来就成立。
	replanned := s.pauseReplanned
	s.pauseReplanned = false
	// 正在等空闲的那一跳**已经到点**了（剩余恒为 0，见 idleGate.go 的“为什么 NextAt 留在
	// 过去”）。恢复时不能按暂停时长平移它：平移会把它推回未来，下一拍就落进
	// “now ∈ [NextAt-提前量, NextAt]”的提前提示区间 —— 人还没离开工位，门都没过，
	// 右下角却先弹出提示窗。保持原时刻，接着等。
	if s.idleWait != nil {
		delta = 0
	}
	// 这一段暂停里有没有出现过定时暂停：有就按“离开”口径重锚内置倒计时。
	// 在真正恢复的这一刻取一次并清零 —— 中途只摘掉一条原因（还有别的挂着）时不算恢复，
	// pauseTimed 必须留到真正的恢复那一跳。
	//
	// 定时暂停（“接下来 2 小时我不工作”、睡前暂停到明早）表达的是“接下来一段时间我不工作”，
	// 回来重新计一个完整周期才合理。跨夜那条尤其要紧 —— 零点前几分钟合盖睡觉时，空闲检测
	// 还没挂上就到了期，不重锚的话第二天开机会接着昨夜冻结的剩余时间走。
	//
	// **自动原因不再重锚**（2026-11-12 用户口径：暂停就是暂停，不分原因）：勿扰 / 全屏 /
	// 投屏 / 应用占用 / 空闲与手动暂停同构 —— 时钟冻结、恢复后按剩余平移，暂停期间到点的
	// 那一跳由恢复后的第一拍按“暂停休息时”的处置补上（推迟 = 恢复即弹）。此前这里是
	// `pauseHadAuto || pauseTimed`，自动原因会把倒计时推回一整个间隔，等于把“推迟提醒”
	// 悄悄降级成“到点作废”。纯空闲离开仍会从头计时，那条走 sys_watch.go 的
	// ResumeQuiet + ReanchorCountdown，与本函数的判据无关。
	reanchor := s.pauseTimed
	s.pauseTimed = false
	// pauseHadAuto 不再参与重锚，但必须在真正的恢复这一跳清零：AutoPauseActive() 靠它
	// 回答“这段暂停里有过自动原因”，暂停提示（pause_notice.go）只用那个口径。
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
		// 暂停开始**之后**用户主动“立即休息”发起的那次：它一直按真实时钟在走
		//（见 pauseFreezesClockLocked），恢复时不该再按暂停时长平移，否则白送一段休息。
		st.BreakWhilePaused = false
		s.mu.Unlock()
	case st.Phase == "break":
		// 剩余时间冻结整体平移（短暂暂停不吞用户的休息时间）；但总休息时长
		//（含暂停期间）封顶为本次配置时长——暂停期间本来就在休息。
		// 空闲暂停不会在休息窗口期间发生（onTick 见 phase === 'break' 直接返回），
		// 所以能落到这里的只有手动 / 勿扰 / 指定应用占用三种暂停；它们可能持续很久
		//（开着勿扰看了一下午视频），封顶照旧需要：否则休息会被拖成遥遥无期的
		//“休息中”，窗口不置顶时用户只能干等。
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
		//   - 绝大多数情况（手动暂停、勿扰 / 全屏 / 投屏 / 应用占用等自动暂停）：冻结整体平移，
		//     用户的“还剩 X 分钟”不会因为按了一下暂停而白跑；暂停期间到点的那一跳留在过去，
		//     恢复后的第一拍就按“暂停休息时”的处置补上（推迟 = 立刻弹出来）。
		//   - 定时暂停（reanchor）：按“离开”口径从头计时 —— “接下来两小时我不工作”“睡前暂停
		//     到明早”之后回来该是一个完整周期，而不是昨夜剩下的几分钟。
		//     自定义提醒（cb-）一律按剩余平移：它是用户按自己节奏配的绝对安排。
		oldAt := st.NextAt
		nextType := st.NextType
		newAt := oldAt + delta
		reanchored := false
		// 暂停期间已经重排过（见 pauseReplanned）：那条计划本来就是按真实时间算出来的，
		// 再 +delta 会把下一次休息白推一个“暂停时长”。典型来源就是本次新口径 ——
		// “正常提醒”的休息在暂停里按真实时钟走完，finishBreak 立刻排了下一跳。
		// 暂停期间照常弹出的自定义提醒走完也一样。
		if replanned {
			newAt = oldAt
		}
		if reanchor {
			if kind, at, iv, ok := s.reanchorTargetLocked(cfg, nowMs()); ok && at > oldAt {
				newAt = at
				// 重锚用的就是当前配置间隔：同步锚点间隔表，后续 replan 的沿用判据才成立
				s.anchorIv[kind] = iv
				reanchored = true
				log.Printf("[scheduler] 定时暂停恢复：内置倒计时重新计时 %s → %s",
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
		// 暂停期间被“推迟提醒”推后的那一条跟线一起平移：occurrence 去重键保持一致，
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
		// 平移时才是“旧值 + 时长”。重锚却按 +delta 算的话，状态卡会显示一个
		// “满间隔 + 暂停时长”的假倒计时（组内混着固定线的近似误差由下一次 replan 纠正）。
		if reanchored {
			if st.Rest != nil && st.Rest.At == oldAt {
				st.Rest = &model.PlanRef{Name: st.Rest.Name, At: newAt}
			}
			if st.Other != nil && st.Other.At == oldAt {
				st.Other = &model.PlanRef{Name: st.Other.Name, At: newAt}
			}
		} else if delta != 0 && newAt != oldAt {
			// 只有真的按剩余平移了才跟着挪。newAt == oldAt 有两种来路：暂停时长约等于 0，
			// 或这条计划是暂停期间重排的（已经在未来，上面已跳过 +delta）—— 后者跟着
			// +delta 会把状态卡上的“下次休息”推到一个谁都没安排过的时刻。
			if st.Rest != nil {
				st.Rest.At += delta
			}
			if st.Other != nil {
				st.Other.At += delta
			}
		}
		// 重锚把计划推到“现在 + 间隔”：phase 必须拉回等待态。停在 pre 的话，
		// 下一跳不会重新判“即将休息”，窗口判据却已不成立 —— 状态与窗口各说各话。
		if reanchored {
			st.Phase = "idle"
			// 这一跳已经被整段推走（离开 = 重新计一个完整周期），原来在等的空闲也就作废了：
			// 留着它会让 mergeIdleWait 把旧时刻钉回来，把重锚的结果又抹掉。
			s.clearIdleWaitLocked()
		}
		// 与固定线分支一致：恢复后立即重判“即将休息”阶段，不依赖下一个 tick。
		// 暂停期间若 NextAt 被平移进提前区间内，或暂停前本就在 pre，都应立刻回到提示窗。
		//
		// 已过期（now >= NextAt）时不弹：那一跳该由 onTick 直接开始休息，弹一个
		//“还剩 0 秒”的提示窗只是闪一下；更重要的是“离开期间刚好到点”那种 case ——
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
	// 自动暂停（含空闲）的内置倒计时已在上面重锚；手动暂停仍按平移，调用方若走的是
	// 离开链路（idleWatchTick）才需要再调一次 ReanchorCountdown（幂等）。
}
