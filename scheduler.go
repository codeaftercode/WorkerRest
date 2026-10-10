package main

import (
	"log"
	"sync"
	"time"
)

// 休息调度器：electron/src/scheduler.ts 的 BreakScheduler 逐条移植。
//
// 时间基准全部使用**绝对毫秒时间戳**（UnixMilli），避免长时间运行漂移。
// 「下一次什么时候休息」不在本文件里推算，而是交给纯函数 planNextBreak
// （见 breakplan.go）：倒计时模式按间隔，固定时间模式按时刻表，谁更早就听谁的。
// 这里只负责到点切换状态机。
//
// 锁的纪律（两把锁，顺序固定，绝不反向）：
//   - App.mu 保护 a.cfg / a.sess / a.wins；Scheduler.mu 保护 state / consumed；
//   - 允许「持有 Scheduler.mu 时去拿 App.mu」（cfgSnapshot），反向绝不允许；
//   - 更省事的做法是**不嵌套**：所有需要配置的分支都先 cfgSnapshot() 再拿 Scheduler.mu，
//     所有事件/窗口/推送都在**放开 Scheduler.mu 之后**才做。
//
// schedHost 调度器需要宿主提供的全部能力。真实实现是 *App；单测用一个假宿主
// （不需要 application.App，也不碰窗口/托盘），这样状态机可以脱离 Wails 跑单测 ——
// 「推迟」「暂停恢复」「消费记账」这些分支全靠它才验得动。
type schedHost interface {
	cfgSnapshot() *Settings
	onSchedulerState()
	onPreNotify(kind string, notifyBeforeMs int64)
	onPaused()
	showBreakSession(kind string, preview bool, forceKey string) string
	closeBreakWindows()
	takeoutPostponedSnapshot() *TakeoutPostponed
	takeoutFiredSnapshot() map[string]bool
	// 推迟 / 跳过休息时各记一笔统计（对应 Electron 的 scheduler.on('postpone' / 'skip')）
	onBreakPostponed(kind string, postponeMs int64)
	onBreakSkipped(kind string, skippedMs int64)
}

type Scheduler struct {
	host schedHost

	mu    sync.Mutex
	state SchedulerState
	// consumed 已消费的触发线 occurrence（键 = occurrenceKey）：开始一次休息
	// （无论到点触发还是「立即休息」）就把它记下，replan 时不再返回同一次安排。
	// 否则固定时刻 12:00 的休息在提前窗点「立即休息」、又马上「提前结束」后，
	// replan 会把还没到点的 12:00 原样排回来——提前窗与提醒窗各再弹一次。
	// 「跳过」同样消费当前 occurrence（固定线不消费的话，跳过等于没跳）。
	consumed map[string]bool

	stopOnce sync.Once
	stopCh   chan struct{}
}

// nowMs 当前时刻（毫秒时间戳，与 JS 的 Date.now() 同基准）
func nowMs() int64 { return time.Now().UnixMilli() }

func max0(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}

func newScheduler(a *App) *Scheduler {
	return newSchedulerWithHost(a)
}

// newSchedulerWithHost 建一个调度器（单测传假宿主，生产传 *App）
func newSchedulerWithHost(host schedHost) *Scheduler {
	s := &Scheduler{host: host, consumed: map[string]bool{}, stopCh: make(chan struct{})}
	s.state = SchedulerState{
		Phase:        "idle",
		NextType:     "short",
		NextName:     shortBreakName,
		Lines:        []PlannedBreak{},
		Timeline:     []TimelineItem{},
		FixedNext:    map[string]int64{},
		PauseReasons: []string{},
	}
	return s
}

// startScheduler 启动调度器（App 启动时调一次）
func (a *App) startScheduler() {
	s := newScheduler(a)
	a.sched = s
	s.resetCycle()
	go s.loop()
}

// Stop 停止调度循环（退出 / 测试收尾）
func (s *Scheduler) Stop() {
	s.stopOnce.Do(func() { close(s.stopCh) })
}

// loop 半秒一跳。Electron 里是两个各 500ms 的定时器（主 tick + 休息窗口到点收窗），
// 这里合成一跳：休息进行中只看向「该收窗了没」，其余情况走主 tick。
func (s *Scheduler) loop() {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.onTick()
		}
	}
}

func (s *Scheduler) onTick() {
	s.mu.Lock()
	if s.state.Paused {
		s.mu.Unlock()
		return
	}
	if s.state.Phase == "break" {
		// 休息进行中不叠加：固定时刻撞上正在进行的休息时跳过
		//（不像外卖提醒那样压在休息之上）；到点则收窗。
		due := s.state.BreakEndsAt > 0 && nowMs() >= s.state.BreakEndsAt
		s.mu.Unlock()
		if due {
			s.finishBreak()
		}
		return
	}
	if s.state.NextAt == 0 {
		s.mu.Unlock()
		return
	}
	nextType := s.state.NextType
	nextAt := s.state.NextAt
	now := nowMs()
	preNotifyMs := int64(0)
	if s.state.Phase == "idle" {
		cfg, ok := s.host.cfgSnapshot().BreakByID(nextType)
		if ok && cfg.NotifyBeforeMs > 0 && now >= nextAt-cfg.NotifyBeforeMs {
			s.state.Phase = "pre"
			preNotifyMs = cfg.NotifyBeforeMs
		}
	}
	due := now >= nextAt
	s.mu.Unlock()

	if preNotifyMs > 0 {
		// 系统通知在 Windows 上无法带按钮，改用自绘提示弹窗（含倒计时与动作按钮）
		s.host.onPreNotify(nextType, preNotifyMs)
	}
	if due {
		s.startBreak()
	}
}

// ---------- 对外的动作（托盘 / 设置页 / 快捷键 / 提前提示窗都汇聚到这里） ----------

// resetCycle 重置周期：休息进行中先正常收尾（关窗口、落账），再重置——
// 否则状态归零了窗口还挂着，直到下一次休息开始才被顺带关掉。
func (s *Scheduler) resetCycle() {
	s.mu.Lock()
	inBreak := s.state.Phase == "break"
	s.mu.Unlock()
	if inBreak {
		s.endBreak()
	}
	s.mu.Lock()
	s.state.ShortsSinceLong = 0
	// 显式重置周期：挂起线清空，本次重排全部按当前配置重新起算（不保留旧倒计时进度）
	s.state.Lines = []PlannedBreak{}
	s.mu.Unlock()
	s.scheduleNext()
}

// breakNow 立即开始一次休息（跳过等待）
func (s *Scheduler) breakNow() {
	s.breakNowOf("")
}

// breakNowOf 立即开始一次休息，kind 留空 = 用当前计划的那一条。
// 指定 kind 时先把 nextType 顶上去（诊断 / 托盘「来一次长休息」用），
// 这样 phase、已消费记账与派生视图都按同一条身份走。
// 返回值是会话 id（正在休息或该身份不存在时为空串）。
func (s *Scheduler) breakNowOf(kind string) string {
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
func (s *Scheduler) postpone() {
	cfg := s.host.cfgSnapshot()
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
	st.NextAt = nowMs() + ms
	st.Phase = "idle"
	// 统计：记一次推迟 + 推后时长（与 Electron 的 scheduler.on('postpone') 同一口径）
	s.host.onBreakPostponed(nextType, ms)
	// 被推迟的那条线在挂起线表里同步平移（今日时间线读的是这份表）
	for i := range st.Lines {
		if st.Lines[i].Type == nextType && st.Lines[i].At == oldAt {
			st.Lines[i].At = st.NextAt
		}
	}
	sortPlans(st.Lines)
	// 被推迟的这条必是其分组里最早的一条（它是全局最早）：状态卡上的分组倒计时跟着走
	if nextType == "short" || nextType == "long" {
		if st.Rest != nil {
			st.Rest = &PlanRef{Name: st.Rest.Name, At: st.NextAt}
		}
	} else if st.Other != nil {
		st.Other = &PlanRef{Name: st.Other.Name, At: st.NextAt}
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
}

// skip 跳过当前休息，直接进入下一周期
func (s *Scheduler) skip() {
	cfg := s.host.cfgSnapshot()
	s.mu.Lock()
	st := &s.state
	if st.Phase == "break" || st.Paused || st.NextAt == 0 {
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
	s.consumed[occurrenceKey(nextType, st.NextAt)] = true
	s.mu.Unlock()
	// 统计：记一次跳过 + 被跳过那次的时长（对应 Electron 的 scheduler.on('skip')）
	s.host.onBreakSkipped(nextType, skippedMs)
	log.Printf("[scheduler] 跳过 %s（时长 %d ms）", nextType, skippedMs)
	s.scheduleNext()
}

// endBreak 提前结束进行中的休息
func (s *Scheduler) endBreak() {
	s.mu.Lock()
	inBreak := s.state.Phase == "break"
	s.mu.Unlock()
	if !inBreak {
		return
	}
	s.finishBreak()
}

// pauseToggle 手动暂停 / 恢复切换
func (s *Scheduler) pauseToggle() {
	s.mu.Lock()
	paused := s.state.Paused
	s.mu.Unlock()
	if paused {
		s.resume("manual")
	} else {
		s.pause("manual")
	}
}

// pause 加一条暂停原因。多条原因并存，全部清掉才真的恢复。
func (s *Scheduler) pause(reason string) {
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
	s.host.onPaused()
}

// resume 去掉一条暂停原因；全部清掉时恢复，并把各线的时间计划整体平移/重算
func (s *Scheduler) resume(reason string) {
	cfg := s.host.cfgSnapshot()
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
		sortPlans(st.Lines)
		// 分组倒计时同步平移（组内若混着固定时刻线，近似误差由下一次 replan 纠正——仅显示用）
		if st.Rest != nil {
			st.Rest.At += delta
		}
		if st.Other != nil {
			st.Other.At += delta
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
	if preType != "" {
		nb := int64(0)
		if c, ok := cfg.BreakByID(preType); ok {
			nb = c.NotifyBeforeMs
		}
		s.host.onPreNotify(preType, nb)
	}
	// 离开结束后内置倒计时重新从头计时由调用方接着调 reanchorCountdown()
}

// remainingMs 距下一次事件（休息开始 / 结束）的剩余毫秒；没有安排休息时为 0。
// 暂停期间剩余时间冻结（锚定到 pausedAt），不再随时间往下掉。
func (s *Scheduler) remainingMs() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := nowMs()
	if s.state.Paused {
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
func (s *Scheduler) refreshPlan() {
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
func (s *Scheduler) reanchorCountdown() {
	cfg := s.host.cfgSnapshot()
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
	// 回来时若还停在「休息前提示窗」阶段，拉回等待态：离开一趟回来不该立刻被催
	st.Phase = "idle"
	for i := range st.Lines {
		l := &st.Lines[i]
		if l.Source == "countdown" && l.Type == nextType && l.At == oldAt {
			l.At = at
		}
	}
	sortPlans(st.Lines)
	if st.Rest != nil && st.Rest.At == oldAt {
		st.Rest = &PlanRef{Name: st.Rest.Name, At: at}
	}
	if st.Other != nil && st.Other.At == oldAt {
		st.Other = &PlanRef{Name: st.Other.Name, At: at}
	}
	s.mu.Unlock()
	log.Printf("[scheduler] 内置倒计时重新计时：%s → %s", fmtClock(oldAt), fmtClock(at))
	s.emitState()
}

// ---------- 内部：计划与状态机 ----------

// cfgByID 取某条休息的配置（先拿配置快照，避免持锁期间碰 App.mu）
func (s *Scheduler) cfgByID(id string) (BreakConfig, bool) {
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

// prevCountdownAnchors 收集挂起线里仍未到期的倒计时锚点（键 = 休息身份，同身份取最早）。
// replan 重算时把它传回 schedulerLines / planNextBreakSplit，让还没到期的倒计时原样
// 保留 —— 设置页任意保存触发的 replan 不再清零用户的倒计时进度（见 breakplan.go 的
// countdownAnchor）。
func prevCountdownAnchors(lines []PlannedBreak, now int64) map[string]int64 {
	prev := map[string]int64{}
	for _, l := range lines {
		if l.Source != "countdown" || l.At <= now {
			continue
		}
		if old, ok := prev[l.Type]; !ok || l.At < old {
			prev[l.Type] = l.At
		}
	}
	return prev
}

// replan 重算下一次休息（只改 state 里的字段，不发事件、不动 phase）。
// 仍未到期的倒计时锚点原样保留：设置保存、导入等触发的重算只增删身份，
// 不把已流逝的倒计时进度清零重计。
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
	// 上一轮仍未到期的倒计时锚点先收好（必须在覆盖 st.Lines 之前）：本轮原样沿用
	prev := prevCountdownAnchors(st.Lines, now)
	// 整份挂起线留给今日时间线用（state.lines）；planNextBreak 的口径就是它的首条
	pool := schedulerLines(cfg, now, st.ShortsSinceLong, s.consumed, prev)
	st.Lines = pool
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
func (s *Scheduler) refreshDerivedLocked(cfg *Settings, now int64) {
	prev := prevCountdownAnchors(s.state.Lines, now)
	split := planNextBreakSplit(cfg, now, s.state.ShortsSinceLong, s.consumed, prev)
	s.state.Rest = planRefOf(split.Builtin)
	s.state.Other = planRefOf(split.Other)
	s.state.FixedNext = fixedNextMap(cfg, now, s.consumed)
}

func planRefOf(p *PlannedBreak) *PlanRef {
	if p == nil {
		return nil
	}
	return &PlanRef{Name: p.Name, At: p.At}
}

// scheduleNext 回到等待态并重排下一次休息
func (s *Scheduler) scheduleNext() {
	s.mu.Lock()
	s.state.Phase = "idle"
	s.mu.Unlock()
	s.replan()
	s.emitState()
}

// startBreak 到点（或立即休息）弹一次真实休息，返回会话 id（弹不出来时为空串）
func (s *Scheduler) startBreak() string {
	cfg := s.host.cfgSnapshot()
	s.mu.Lock()
	st := &s.state
	kind := st.NextType
	bcfg, ok := cfg.BreakByID(kind)
	if !ok {
		// 计划里的这条休息已被删除：不弹窗，直接重排下一条
		s.mu.Unlock()
		s.scheduleNext()
		return ""
	}
	now := nowMs()
	st.Phase = "break"
	st.BreakStartedAt = now
	st.BreakEndsAt = now + bcfg.DurationMs
	// 这次安排已消费：无论到点触发还是「立即休息」，replan 都不再排它
	s.consumed[occurrenceKey(kind, st.NextAt)] = true
	// 派生视图立即跟上：刚消费掉的那条不再出现在分组倒计时与卡片摘要的「下次」里
	s.refreshDerivedLocked(cfg, now)
	s.mu.Unlock()

	log.Printf("[scheduler] 开始休息 %s（%d ms，%s）", kind, bcfg.DurationMs, bcfg.Mode)
	// 休息窗口与设置页立即反映「休息中」
	id := s.host.showBreakSession(kind, false, "")
	s.emitState()
	return id
}

// finishBreak 结束进行中的休息（自然到点与提前结束共用）
func (s *Scheduler) finishBreak() {
	s.mu.Lock()
	kind := s.state.NextType
	s.advanceCounter(kind)
	s.mu.Unlock()

	// break-end 的副作用：关休息窗口、空闲基准清零（空闲计时属下一步的「系统能力」）
	log.Printf("[scheduler] 结束休息 %s", kind)
	s.host.closeBreakWindows()
	s.scheduleNext()
}

// emitState 把状态推给设置页与托盘（对应 Electron 的 emit('state')）
func (s *Scheduler) emitState() {
	s.host.onSchedulerState()
}

// ---------- 对外快照 ----------

// stateCopy 深拷贝一份状态（切片与 map 都要拷：调用方会在锁外读它）
func (s *Scheduler) stateCopy() SchedulerState {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.state
	out.Lines = append([]PlannedBreak{}, s.state.Lines...)
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
func (s *Scheduler) stateForRenderer() SchedulerState {
	cfg := s.host.cfgSnapshot()
	st := s.stateCopy()
	pp := s.host.takeoutPostponedSnapshot()
	if pp != nil {
		if st.FixedNext == nil {
			st.FixedNext = map[string]int64{}
		}
		st.FixedNext["takeout"] = pp.DueAt
	}
	st.Timeline = todayTimeline(cfg, nowMs(), TimelineOptions{
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
	prev := prevCountdownAnchors(s.state.Lines, now)
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
