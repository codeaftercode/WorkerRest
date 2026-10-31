package main

import (
	"log"
	"strings"
	"sync"
	"time"
)

// 休息调度器：历史源码/scheduler.ts 的 BreakScheduler 逐条移植。
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
	closePreBreak()
	showBreakSession(kind string, preview bool, forceKey string) string
	closeBreakWindows()
	takeoutPostponedSnapshot() *TakeoutPostponed
	takeoutFiredSnapshot() map[string]bool
	// 推迟 / 跳过休息时各记一笔统计（对应 上一版的 scheduler.on('postpone' / 'skip')）
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
	// anchorIv 各倒计时身份当前锚点的产生间隔（ms，键 = 休息身份）：replan 沿用旧锚点
	// 的判据之一 —— 间隔没变才沿用，改了间隔立刻按新间隔重新起算（见 countdownAnchor）。
	// 每次 replan 按新池同步（syncAnchorIvLocked）；reanchorCountdown / resetCycle 各自维护。
	anchorIv map[string]int64
	// pauseSkipped 暂停期间已按「跳过」记过账的 occurrence（键 = occurrenceKey）。
	// 暂停态每半秒一跳都会重算「有没有到点的自定义提醒」，没有这份去重会重复记账；
	// 完全恢复后清空（下一段暂停重新计），键里带时刻本身也不会串到别的 occurrence。
	pauseSkipped map[string]bool
	// pauseFired 暂停期间「正常提醒」的自定义提醒已照常弹过的 occurrence
	//（键 = occurrenceKey；提前提示小窗另加 "|pre" 后缀，与真窗分开去重）。
	// 与 pauseSkipped 同生命周期：完全恢复时一起清空。
	pauseFired map[string]bool
	// postponed 暂停期间被「推迟提醒」推后的 occurrence（键 = occurrenceKey，值 = 推到
	// 哪一刻）。这份表**不随 resume 清空**：推迟是延后，恢复后还要按推迟后的时刻弹。
	postponed map[string]*postponedBreak

	stopOnce sync.Once
	stopCh   chan struct{}

	// lastTickAt 上一次 onTick 的墙钟时刻：用来发现「两次 tick 之间过了很久」——
	// 睡眠 / 休眠 / 系统时间被大幅调整时，倒计时会在醒来第一刻全部到点，
	// 不处理就会立刻弹一个休息窗口（用户刚开机就被要求休息）。
	lastTickAt int64
}

// postponedBreak 暂停期间被「推迟提醒」推后的一次自定义提醒。
// at = 原始 occurrence 时刻（startBreakAt 的 consumed 去重键用），dueAt = 推迟后的弹出时刻。
type postponedBreak struct {
	kind  string
	at    int64
	dueAt int64
}

// nowMs 当前时刻（毫秒时间戳，与 JS 的 Date.now() 同基准）
func nowMs() int64 { return time.Now().UnixMilli() }

func max0(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}

// isCustomBreakID 是否是自定义提醒的身份（内置短 / 长休息不算）
func isCustomBreakID(id string) bool { return strings.HasPrefix(id, customBreakPrefix) }

func newScheduler(a *App) *Scheduler {
	return newSchedulerWithHost(a)
}

// newSchedulerWithHost 建一个调度器（单测传假宿主，生产传 *App）
func newSchedulerWithHost(host schedHost) *Scheduler {
	s := &Scheduler{
		host:         host,
		consumed:     map[string]bool{},
		anchorIv:     map[string]int64{},
		pauseSkipped: map[string]bool{},
		pauseFired:   map[string]bool{},
		postponed:    map[string]*postponedBreak{},
		stopCh:       make(chan struct{}),
	}
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

// loop 半秒一跳。上一版 里是两个各 500ms 的定时器（主 tick + 休息窗口到点收窗），
// 这里合成一跳：休息进行中只看向「该收窗了没」，其余情况走主 tick。
func (s *Scheduler) loop() {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			// 包一层 panic 兜底：调度器是整程序的心跳，一跳里的 nil deref 不该让
			// GUI 直接消失（见 recoverguard.go）。丢一拍，下半秒继续。
			guardRun("scheduler.tick", s.onTick)
		}
	}
}

// breakCloseDwellMs：能量蓄满（休息自然到点）后让提醒窗口多停留的毫秒数，
// 让用户看清「满罐」状态再关窗。只作用于自然到点（onTick 的 due 判定），
// 手动「提前结束」走 endBreak → finishBreak 不经过这里，立即关窗不受影响。
const breakCloseDwellMs int64 = 1000

// tickJumpThresholdMs 两次 tick 的墙钟间隔超过它，就认为中间「时间大跳」过
// （睡眠 / 休眠 / 系统时间被调整）。与 stats 的睡眠判定同阈值：60 秒足够挡掉
// GC、磁盘、系统卡顿这类正常抖动。
const tickJumpThresholdMs = 60_000

func (s *Scheduler) onTick() {
	// 时间大跳：这一跳不判任何「到点」——睡眠期间过期的倒计时不该在醒来第一刻补弹，
	// 统一重排（内置 / 自定义倒计时从头计时，固定线顺延到下一个时刻）。
	// 暂停中也重排：冻结锚点由 prevCountdownAnchorsLocked 保住，不会被清掉。
	tickNow := nowMs()
	s.mu.Lock()
	last := s.lastTickAt
	s.lastTickAt = tickNow
	s.mu.Unlock()
	if last > 0 && tickNow-last > tickJumpThresholdMs {
		log.Printf("[scheduler] 两次 tick 间隔 %d ms（睡眠 / 休眠 / 系统时间调整）：重排，不补弹过期提醒",
			tickNow-last)
		s.scheduleNext()
		return
	}
	s.mu.Lock()
	paused := s.state.Paused
	inBreak := s.state.Phase == "break"
	clockFrozen := s.pauseFreezesClockLocked()
	// 蓄能容器：到点（能量蓄满）后多停留 breakCloseDwellMs 再关窗
	breakDue := inBreak && s.state.BreakEndsAt > 0 && nowMs() >= s.state.BreakEndsAt+breakCloseDwellMs
	// 暂停期间：短 / 长休息保持原状（不提醒、不记账）；自定义提醒按「暂停休息时」的
	// 处置分派 —— 跳过（pausedSkip 记一笔跳过）、正常提醒（pausedFire 照常弹窗）、
	// 推迟提醒（pausedPostpone 往后推，推完仍暂停就再推）。
	if paused {
		s.mu.Unlock()
		if inBreak {
			// 暂停期间弹出来的自定义提醒仍要按点收窗，否则窗口会一直挂着；
			// 暂停中「立即休息」主动开始的内置休息同理（clockFrozen=false）——
			// 少这一条，内置休息窗口会一直挂到用户恢复暂停为止。
			// 暂停前就在进行的短 / 长休息保持冻结（恢复时整体平移）。
			if breakDue && !clockFrozen {
				s.finishBreak()
			}
			return
		}
		s.pausedSkip()
		s.pausedFire()
		s.pausedPostpone()
		return
	}
	// 休息进行中不叠加：固定时刻撞上正在进行中的休息时跳过
	//（不像外卖提醒那样压在休息之上）；到点则收窗。
	if inBreak {
		s.mu.Unlock()
		if breakDue {
			s.finishBreak()
		}
		return
	}
	// 暂停期间被「推迟提醒」推到现在的提醒：到点就弹（推迟是延后，不是吞掉）。
	// flushPostponed 自己加锁，这里先把进入本函数时拿的那把锁放开，之后再拿回来
	// 继续走下面的正常触发线。
	s.mu.Unlock()
	if s.flushPostponed() {
		return
	}
	s.mu.Lock()
	if s.state.NextAt == 0 {
		s.mu.Unlock()
		return
	}
	nextType := s.state.NextType
	nextAt := s.state.NextAt
	now := nowMs()
	// 提前把当前 NextType 的配置取出来：既用于「提前提示」阶段判定，也用于下面的
	// 禁用项竞态兜底（这条休息可能刚被用户在设置页关闭，replan 还没跑）。
	cfg, cfgOK := s.host.cfgSnapshot().BreakByID(nextType)
	preNotifyMs := int64(0)
	if s.state.Phase == "idle" {
		// 该提醒已被用户在设置页关闭（禁用项正常路径上由 replan 从挂起线剔除，
		// 这里再卡一道：既不进「提前提示」阶段、也绝不会到点触发，杜绝竞态下误提醒）。
		// now < nextAt：已到点就直接走下面的 due（开始休息），不再先弹一个 0 秒的提示窗。
		if cfgOK && cfg.Enabled && cfg.NotifyBeforeMs > 0 && now < nextAt && now >= nextAt-cfg.NotifyBeforeMs {
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
		if cfgOK && !cfg.Enabled {
			// 配置刚改、replan 还没跑的竞态下，当前 NextType 已被禁用：重排，
			// 让它退出「下一个」位置，绝不误弹一个已关闭的提醒。
			s.scheduleNext()
		} else {
			s.startBreak()
		}
	}
}

// pausedSkip 暂停期间的「跳过提醒」：自定义提醒在暂停态下到点时不弹任何窗，
// 直接按「跳过」处理（调一次 host.onBreakSkipped）。
// **但自定义提醒不进统计**：统计页「休息」这一组四项（完成 / 推迟 / 跳过 / 提前结束）
// 只统计内置短休息与长休息，过滤集中在 App.onBreakSkipped 一处 —— 这里照旧发回调，
// 免得调度器与宿主各判一次、口径漂移。所以本函数对自定义提醒实际只留下日志与去重。
// 「推迟提醒」但「可推迟」填了 0 时也落到这里（没有时长可推，见 pauseDisposition）。
//
// 为什么只处理自定义提醒：短 / 长休息按需求保持原状（暂停期间不提醒也不记账）；
// 外卖不走调度器，它自己的每秒 tick 里做同样的事（见 takeout.go 的 skipTakeoutWhilePaused）。
//
// 两条触发线的「到点」判定：
//   - 固定时刻线：按分钟匹配（isTimeSlotDue），暂停期间每个到点的时刻各记一次；
//   - 倒计时线：挂起线代表那一跳，At 已过就是到点了。暂停期间计划被冻结（不再往后排），
//     所以同一段暂停最多记这一跳。
//
// 去重键是 occurrenceKey：暂停态每半秒一跳都会重算一次，没有去重会重复记账。
func (s *Scheduler) pausedSkip() {
	cfg := s.host.cfgSnapshot()
	now := nowMs()
	type hit struct {
		id     string
		name   string
		skipMs int64
	}
	var hits []hit
	s.mu.Lock()
	for _, c := range cfg.CustomBreakList() {
		if !c.Config.Enabled || pauseDisposition(c.Config) != PauseActionSkip {
			continue
		}
		if c.Config.Mode == "fixed" {
			for _, slot := range c.Config.Slots {
				if !isTimeSlotDue(slot, now, 0) {
					continue
				}
				key := occurrenceKey(c.ID, slotAlertAt(slot, 0, now))
				if s.pauseSkipped[key] {
					continue
				}
				s.pauseSkipped[key] = true
				hits = append(hits, hit{id: c.ID, name: c.Name, skipMs: c.Config.DurationMs})
			}
			continue
		}
		for _, l := range s.state.Lines {
			if l.Type != c.ID || l.Source != "countdown" || l.At > now {
				continue
			}
			key := occurrenceKey(l.Type, l.At)
			if s.pauseSkipped[key] {
				continue
			}
			s.pauseSkipped[key] = true
			hits = append(hits, hit{id: c.ID, name: c.Name, skipMs: c.Config.DurationMs})
		}
	}
	s.mu.Unlock()
	for _, h := range hits {
		log.Printf("[scheduler] 暂停休息期间 %s 到点，按跳过处理（%d ms）", h.name, h.skipMs)
		s.host.onBreakSkipped(h.id, h.skipMs)
	}
}

// pauseFirePreSuffix 暂停期间「提前提示小窗」去重键的后缀：同一次 occurrence 的
// 真窗与提前提示各记一次，互不吞掉。
const pauseFirePreSuffix = "|pre"

// pausedFire 暂停期间「正常提醒」的自定义提醒照常提醒：固定时刻线到点弹提醒窗口、
// 落在提前区间弹提前提示小窗，倒计时线到点弹提醒窗口；不进跳过统计（与手动跳过区分开）。
//
// 每跳最多弹一条（与正常路径一致：一次只开一个提醒窗口），按到点时刻最早的优先，
// 其余的等这个窗口收掉后的下一跳再弹。去重表 pauseFired 与 pauseSkipped 同生命周期
// （完全恢复时一起清空），所以一段暂停里同一个 occurrence 只会弹一次。
func (s *Scheduler) pausedFire() {
	cfg := s.host.cfgSnapshot()
	now := nowMs()
	type cand struct {
		id  string
		at  int64
		key string
		pre bool
		nb  int64
	}
	var cands []cand
	s.mu.Lock()
	for _, c := range cfg.CustomBreakList() {
		cc := c.Config
		if !cc.Enabled || pauseDisposition(cc) != PauseActionRemind {
			continue
		}
		if cc.Mode == "fixed" {
			for _, slot := range cc.Slots {
				if !slot.Enabled {
					continue
				}
				at := slotAlertAt(slot, 0, now)
				if at <= 0 {
					continue
				}
				if isTimeSlotDue(slot, now, 0) {
					if key := occurrenceKey(c.ID, at); !s.pauseFired[key] {
						cands = append(cands, cand{id: c.ID, at: at, key: key})
					}
					continue
				}
				// 提前提示线：occurrence 按 now+提前量 折算（提前量跨午夜时归次日那一格），
				// 与真窗用同一把键，才能正确区分「同一天的两个不同 occurrence」。
				if nb := cc.NotifyBeforeMs; nb > 0 && isTimeSlotDue(slot, now, nb) {
					preAt := slotOccurrenceAt(slot, nb, now)
					key := occurrenceKey(c.ID, preAt) + pauseFirePreSuffix
					if !s.pauseFired[key] {
						cands = append(cands, cand{id: c.ID, at: preAt, key: key, pre: true, nb: nb})
					}
				}
			}
			continue
		}
		// 倒计时线：挂起线里这条身份最早的一跳已过点即触发（暂停期间计划被冻结、
		// 恢复时才整体平移，所以这里认的就是「本来该提醒的那一刻」）。
		for _, l := range s.state.Lines {
			if l.Type != c.ID || l.Source != "countdown" || l.At > now {
				continue
			}
			key := occurrenceKey(l.Type, l.At)
			if s.pauseFired[key] {
				continue
			}
			cands = append(cands, cand{id: c.ID, at: l.At, key: key})
		}
	}
	if len(cands) == 0 {
		s.mu.Unlock()
		return
	}
	// 取最早的一条（同一时刻时真窗优先于提前提示），只消费这一条的去重键
	pick := cands[0]
	for _, c := range cands[1:] {
		if c.at < pick.at || (c.at == pick.at && pick.pre && !c.pre) {
			pick = c
		}
	}
	s.pauseFired[pick.key] = true
	s.mu.Unlock()
	if pick.pre {
		log.Printf("[scheduler] 暂停休息期间 %s 进入提前提示区间，照常提醒", pick.id)
		s.host.onPreNotify(pick.id, pick.nb)
		return
	}
	log.Printf("[scheduler] 暂停休息期间 %s 到点，照常提醒", pick.id)
	s.startBreakAt(pick.id, pick.at)
}

// pauseDisposition 这条自定义提醒在暂停休息期间的实际处置。把「推迟但没有推迟时长」
// 降级成跳过：没有时长可推，硬推会每跳原地打转、永远不弹。未知取值按默认的推迟。
func pauseDisposition(c BreakConfig) string {
	switch c.PauseAction {
	case PauseActionRemind:
		return PauseActionRemind
	case PauseActionPostpone:
		if c.PostponeMs <= 0 {
			return PauseActionSkip
		}
		return PauseActionPostpone
	case PauseActionSkip:
		return PauseActionSkip
	}
	return PauseActionPostpone
}

// pausedPostpone 暂停期间的「推迟提醒」：到点不弹窗，按该提醒的「可推迟（分钟）」
// 把弹出时间往后推；推完再到期时若仍在暂停休息状态，就继续往后推，如此往复。
// 推迟不是吞掉：恢复后由 flushPostponed 在推迟后的时刻弹出。
//
// 「可推迟」填 0 的配置已经被 pauseDisposition 降级成跳过，这里不会看到。
func (s *Scheduler) pausedPostpone() {
	cfg := s.host.cfgSnapshot()
	now := nowMs()
	type note struct {
		name  string
		due   int64
		again bool
	}
	var notes []note
	s.mu.Lock()
	for _, c := range cfg.CustomBreakList() {
		cc := c.Config
		if !cc.Enabled || pauseDisposition(cc) != PauseActionPostpone {
			continue
		}
		push := func(at int64) {
			key := occurrenceKey(c.ID, at)
			if p, ok := s.postponed[key]; ok {
				// 推后的时刻又到了：还在暂停，就再往后推一段
				if now >= p.dueAt {
					p.dueAt = now + cc.PostponeMs
					notes = append(notes, note{name: c.Name, due: p.dueAt, again: true})
				}
				return
			}
			p := &postponedBreak{kind: c.ID, at: at, dueAt: now + cc.PostponeMs}
			s.postponed[key] = p
			notes = append(notes, note{name: c.Name, due: p.dueAt})
		}
		if cc.Mode == "fixed" {
			for _, slot := range cc.Slots {
				if !isTimeSlotDue(slot, now, 0) {
					continue
				}
				push(slotAlertAt(slot, 0, now))
			}
			continue
		}
		// 倒计时线：挂起线里这一跳已过点即算到点（暂停期间计划冻结，就是那一跳）
		for _, l := range s.state.Lines {
			if l.Type != c.ID || l.Source != "countdown" || l.At > now {
				continue
			}
			push(l.At)
		}
	}
	s.mu.Unlock()
	for _, n := range notes {
		at := time.UnixMilli(n.due).Format("15:04:05")
		if n.again {
			log.Printf("[scheduler] 暂停休息期间 %s 推迟后再次到点，继续推迟到 %s", n.name, at)
			continue
		}
		log.Printf("[scheduler] 暂停休息期间 %s 到点，推迟到 %s 再提醒", n.name, at)
	}
}

// flushPostponed 弹出「暂停期间被推迟、现在到点」的提醒，返回是否弹了一次。
// 每跳最多弹一条（与正常路径一致：一次只开一个提醒窗口），其余下一跳继续。
func (s *Scheduler) flushPostponed() bool {
	now := nowMs()
	s.mu.Lock()
	var pick *postponedBreak
	pickKey := ""
	for k, p := range s.postponed {
		if p.dueAt > now {
			continue
		}
		if pick == nil || p.dueAt < pick.dueAt {
			pick, pickKey = p, k
		}
	}
	if pick == nil {
		s.mu.Unlock()
		return false
	}
	delete(s.postponed, pickKey)
	s.mu.Unlock()

	// 这条提醒可能已经被删 / 关闭：没有窗口可弹，丢掉这一次
	if c, ok := s.host.cfgSnapshot().BreakByID(pick.kind); !ok || !c.Enabled {
		log.Printf("[scheduler] 推迟的 %s 已被关闭或删除，丢弃", pick.kind)
		return false
	}
	log.Printf("[scheduler] 推迟的 %s 到点，弹出提醒（原定 %s）", pick.kind,
		time.UnixMilli(pick.at).Format("15:04:05"))
	s.startBreakAt(pick.kind, pick.at)
	return true
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
	s.anchorIv = map[string]int64{}
	// 推迟表也一起清：显式重置就是要按新配置从头来
	s.postponed = map[string]*postponedBreak{}
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
	// 推迟把原定时刻推远 → 已不在提前区间：托盘 / 侧栏 / 快捷键这几条路径不经过
	// PreBreakAction，必须在这里收窗（否则窗口继续显示已失效的提醒倒计时）
	s.syncPreBreakWindow()
}

// skip 跳过当前休息，直接进入下一周期。
//
// 休息进行中点「跳过 / 提前结束」（设置页侧栏、托盘、快捷键都汇聚到这里）：
// 语义上就是立刻结束这次休息，落 endBreak 路径——早先直接 return，
// 界面上点了没反应，用户只能去休息窗口里另找入口。
func (s *Scheduler) skip() {
	cfg := s.host.cfgSnapshot()
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
	s.consumed[occurrenceKey(nextType, st.NextAt)] = true
	s.mu.Unlock()
	// 统计：记一次跳过 + 被跳过那次的时长。同样只认短 / 长休息，过滤在 App 侧。
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
					s.postponed[occurrenceKey(p.kind, p.at)] = p
				}
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
		s.host.onPreNotify(preType, nb)
	}
	s.syncPreBreakWindow()
	// 离开结束后内置倒计时重新从头计时由调用方接着调 reanchorCountdown()
}

// ---------- 提前提示窗的在场判据 ----------

// preBreakWanted 右下角「提前提示窗」此刻是否该在场 —— 唯一判据。
//
// 窗口是**状态的影子**，不是一次性的弹窗：计划一旦离开提前区间（被重锚、推迟、
// 跳过、暂停、到点开始休息、目标提醒被关闭），窗口就必须消失。此前关窗散落在
// onPaused / scheduleNext / PreBreakAction 各处，谁漏一处，窗口就带着**新一轮的整段
// 倒计时**留在屏幕上 —— 用户看到的就是「还有 1200 秒（= 整个间隔）就弹出提前通知」。
//
// phase == "pre" 之外还要卡「剩余 ≤ 提前量」：phase 卡在 pre 而计划已被推远时，
// 光看 phase 会误判成该在场。
func (s *Scheduler) preBreakWanted() bool {
	s.mu.Lock()
	phase, paused, nextAt, nextType := s.state.Phase, s.state.Paused, s.state.NextAt, s.state.NextType
	s.mu.Unlock()
	if paused || phase != "pre" || nextAt == 0 {
		return false
	}
	// cfgSnapshot 要加 App.mu：必须在放开 Scheduler.mu 之后再取（锁序铁律）
	c, ok := s.host.cfgSnapshot().BreakByID(nextType)
	if !ok || !c.Enabled || c.NotifyBeforeMs <= 0 {
		return false
	}
	remain := nextAt - nowMs()
	return remain >= 0 && remain <= c.NotifyBeforeMs
}

// syncPreBreakWindow 把提前提示窗收成状态的影子：判据说不该在场就关掉。
// 幂等：窗口本来就不在时是空操作，可以在任意状态变更后无脑调。
func (s *Scheduler) syncPreBreakWindow() {
	if !s.preBreakWanted() {
		s.host.closePreBreak()
	}
}

// pauseFreezesClockLocked 暂停此刻是否冻结「休息剩余时间」。
//
// 冻结（true）只适用于「暂停之前就已经开始的休息」：短暂暂停不吞用户的休息时间，
// 恢复时整体平移。另外两种情形必须照常走时钟，否则会互相踩：
//   - 暂停期间照常弹出的自定义提醒（pausedFire）—— 它本来就在正常计时；
//   - 暂停开始之后用户主动「立即休息」发起的内置休息 —— 若也冻结，onTick 的暂停分支
//     永远不会判到点，休息窗口挂到用户恢复暂停为止（短休息也会变成永久窗口）。
func (s *Scheduler) pauseFreezesClockLocked() bool {
	st := &s.state
	if !st.Paused {
		return false
	}
	if st.Phase == "break" && (isCustomBreakID(st.NextType) || st.BreakWhilePaused) {
		return false
	}
	return true
}

// remainingMs 距下一次事件（休息开始 / 结束）的剩余毫秒；没有安排休息时为 0。
// 暂停期间剩余时间冻结（锚定到 pausedAt），不再随时间往下掉。
func (s *Scheduler) remainingMs() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := nowMs()
	// 暂停期间剩余时间冻结（锚定到 pausedAt）；但「本该正常走时」的两种情形不冻结：
	// 暂停期间照常弹出的自定义提醒、以及暂停开始之后主动「立即休息」的那次休息。
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
	// 重锚用的就是当前配置间隔：同步锚点间隔表，后续 replan 的沿用判据才成立
	s.anchorIv[nextType] = bcfg.IntervalMs
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
	// 重锚把计划推到「现在 + 间隔」＝彻底离开提前区间：刚从 resume 弹出的提示窗
	// 必须跟着收掉，否则它会带着一整轮倒计时挂在屏幕上（用户报的「还有 1200 秒就弹」
	// 就是这个：窗口是对的，倒数被重锚改成了满格）。
	s.syncPreBreakWindow()
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
func (s *Scheduler) prevCountdownAnchorsLocked(now int64) map[string]cdAnchor {
	ref := now
	frozen := false
	if s.state.Paused && s.state.PausedAt > 0 && s.state.PausedAt < ref {
		ref = s.state.PausedAt
		frozen = true
	}
	prev := map[string]cdAnchor{}
	for _, l := range s.state.Lines {
		if l.Source != "countdown" || l.At <= ref {
			continue
		}
		if old, ok := prev[l.Type]; !ok || l.At < old.At {
			prev[l.Type] = cdAnchor{At: l.At, Iv: s.anchorIv[l.Type], Frozen: frozen}
		}
	}
	return prev
}

// syncAnchorIvLocked 按重算后的挂起线同步锚点间隔表：池里每条倒计时线的锚点，
// 其产生间隔就是当前配置间隔（沿用者只在间隔未变时才会沿用，重新起算者天然是新间隔）。
// 调用方必须已持有 s.mu。
func (s *Scheduler) syncAnchorIvLocked(cfg *Settings) {
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
func (s *Scheduler) refreshDerivedLocked(cfg *Settings, now int64) {
	prev := s.prevCountdownAnchorsLocked(now)
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
		// 合并组「下次外卖 / 自定义提醒」若当前就是外卖那条，推迟后的时刻也要跟着走
		// （与 FixedNext 同一口径；外卖的推迟 / 跳过不经调度器，不覆盖的话状态卡还显示旧时刻）
		if st.Other != nil && st.Other.Name == TAKEOUT_NAME {
			st.Other = &PlanRef{Name: TAKEOUT_NAME, At: pp.DueAt}
		}
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
