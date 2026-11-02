// Package scheduler 是休息调度状态机：每秒一拍地推进「还剩多久休息、要不要弹窗、
// 用户暂停/跳过/推迟之后时钟怎么走」，并维护提前提示与外卖提醒的一次性触发点。
//
// 它持有状态（倒计时锚点、跳过集合、暂停时刻）并加锁，但**不直接操作窗口**：所有
// 需要界面动作的时刻都以回调/返回值交给 cmd 层执行，这样状态机可以在无 GUI 的测试里
// 把跨天、暂停冻结、提前线这些场景跑完。
package scheduler

import (
	"log"
	"strings"
	"sync"
	"time"
	"workerrest/internal/logx"
	"workerrest/internal/model"
	"workerrest/internal/plan"
	"workerrest/internal/settings"
)

// Host 调度器对宿主的依赖（生产是 *App，单测传假实现）。方法必须大写（导出）：
// 跨包实现接口时，未导出的方法名只在本包可见，main 包的 *App 无法满足小写方法。
type Host interface {
	CfgSnapshot() *settings.Settings
	OnSchedulerState()
	OnPreNotify(kind string, notifyBeforeMs int64)
	OnPaused()
	ClosePreBreak()
	ShowBreakSession(kind string, preview bool, forceKey string) string
	CloseBreakWindows()
	TakeoutPostponedSnapshot() *model.TakeoutPostponed
	TakeoutFiredSnapshot() map[string]bool
	// 推迟 / 跳过休息时各记一笔统计（对应 上一版的 scheduler.on('postpone' / 'skip')）
	OnBreakPostponed(kind string, postponeMs int64)
	OnBreakSkipped(kind string, skippedMs int64)
}

type Scheduler struct {
	host Host

	mu    sync.Mutex
	state model.SchedulerState
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

// PauseReasonIdle 空闲暂停的原因键。它唯一不参与「用户仍在用电脑」的 busy 判定
// （见 pausedForBusyReason）：手动 / 勿扰 / 指定应用占用都会让空闲判定失效，
// 只有 idle 自己能解开离开态。触发方（main 包的系统监视）用这个常量传 pause/resume。
const PauseReasonIdle = "idle"

func max0(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}

// isCustomBreakID 是否是自定义提醒的身份（内置短 / 长休息不算）
func isCustomBreakID(id string) bool { return strings.HasPrefix(id, settings.CustomBreakPrefix) }

// New 建一个调度器（生产传 *App，单测传假宿主）
func New(host Host) *Scheduler {
	s := &Scheduler{
		host:         host,
		consumed:     map[string]bool{},
		anchorIv:     map[string]int64{},
		pauseSkipped: map[string]bool{},
		pauseFired:   map[string]bool{},
		postponed:    map[string]*postponedBreak{},
		stopCh:       make(chan struct{}),
	}
	s.state = model.SchedulerState{
		Phase:        "idle",
		NextType:     "short",
		NextName:     plan.ShortBreakName,
		Lines:        []model.PlannedBreak{},
		Timeline:     []model.TimelineItem{},
		FixedNext:    map[string]int64{},
		PauseReasons: []string{},
	}
	return s
}

// Stop 停止调度循环（退出 / 测试收尾）
func (s *Scheduler) Stop() {
	s.stopOnce.Do(func() { close(s.stopCh) })
}

// Start 启动调度循环：重置周期后跑后台循环（生产启动时调一次）。循环体在 Loop 内，
// 每一跳都由 logx.GuardRun 兜底，这里只负责「重置 + 起 goroutine」。
func (s *Scheduler) Start() {
	s.ResetCycle()
	go s.Loop()
}

// Loop 半秒一跳。上一版 里是两个各 500ms 的定时器（主 tick + 休息窗口到点收窗），
// 这里合成一跳：休息进行中只看向「该收窗了没」，其余情况走主 tick。
func (s *Scheduler) Loop() {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			// 包一层 panic 兜底：调度器是整程序的心跳，一跳里的 nil deref 不该让
			// GUI 直接消失（见 recoverguard.go）。丢一拍，下半秒继续。
			logx.GuardRun("scheduler.tick", s.onTick)
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
	cfg, cfgOK := s.host.CfgSnapshot().BreakByID(nextType)
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
		s.host.OnPreNotify(nextType, preNotifyMs)
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
	cfg := s.host.CfgSnapshot()
	now := nowMs()
	type hit struct {
		id     string
		name   string
		skipMs int64
	}
	var hits []hit
	s.mu.Lock()
	for _, c := range cfg.CustomBreakList() {
		if !c.Config.Enabled || pauseDisposition(c.Config) != settings.PauseActionSkip {
			continue
		}
		if c.Config.Mode == "fixed" {
			for _, slot := range c.Config.Slots {
				if !plan.IsTimeSlotDue(slot, now, 0) {
					continue
				}
				key := plan.OccurrenceKey(c.ID, plan.SlotAlertAt(slot, 0, now))
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
			key := plan.OccurrenceKey(l.Type, l.At)
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
		s.host.OnBreakSkipped(h.id, h.skipMs)
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
	cfg := s.host.CfgSnapshot()
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
		if !cc.Enabled || pauseDisposition(cc) != settings.PauseActionRemind {
			continue
		}
		if cc.Mode == "fixed" {
			for _, slot := range cc.Slots {
				if !slot.Enabled {
					continue
				}
				at := plan.SlotAlertAt(slot, 0, now)
				if at <= 0 {
					continue
				}
				if plan.IsTimeSlotDue(slot, now, 0) {
					if key := plan.OccurrenceKey(c.ID, at); !s.pauseFired[key] {
						cands = append(cands, cand{id: c.ID, at: at, key: key})
					}
					continue
				}
				// 提前提示线：occurrence 按 now+提前量 折算（提前量跨午夜时归次日那一格），
				// 与真窗用同一把键，才能正确区分「同一天的两个不同 occurrence」。
				if nb := cc.NotifyBeforeMs; nb > 0 && plan.IsTimeSlotDue(slot, now, nb) {
					preAt := plan.SlotOccurrenceAt(slot, nb, now)
					key := plan.OccurrenceKey(c.ID, preAt) + pauseFirePreSuffix
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
			key := plan.OccurrenceKey(l.Type, l.At)
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
		s.host.OnPreNotify(pick.id, pick.nb)
		return
	}
	log.Printf("[scheduler] 暂停休息期间 %s 到点，照常提醒", pick.id)
	s.startBreakAt(pick.id, pick.at)
}

// pauseDisposition 这条自定义提醒在暂停休息期间的实际处置。把「推迟但没有推迟时长」
// 降级成跳过：没有时长可推，硬推会每跳原地打转、永远不弹。未知取值按默认的推迟。
func pauseDisposition(c model.BreakConfig) string {
	switch c.PauseAction {
	case settings.PauseActionRemind:
		return settings.PauseActionRemind
	case settings.PauseActionPostpone:
		if c.PostponeMs <= 0 {
			return settings.PauseActionSkip
		}
		return settings.PauseActionPostpone
	case settings.PauseActionSkip:
		return settings.PauseActionSkip
	}
	return settings.PauseActionPostpone
}

// pausedPostpone 暂停期间的「推迟提醒」：到点不弹窗，按该提醒的「可推迟（分钟）」
// 把弹出时间往后推；推完再到期时若仍在暂停休息状态，就继续往后推，如此往复。
// 推迟不是吞掉：恢复后由 flushPostponed 在推迟后的时刻弹出。
//
// 「可推迟」填 0 的配置已经被 pauseDisposition 降级成跳过，这里不会看到。
func (s *Scheduler) pausedPostpone() {
	cfg := s.host.CfgSnapshot()
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
		if !cc.Enabled || pauseDisposition(cc) != settings.PauseActionPostpone {
			continue
		}
		push := func(at int64) {
			key := plan.OccurrenceKey(c.ID, at)
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
				if !plan.IsTimeSlotDue(slot, now, 0) {
					continue
				}
				push(plan.SlotAlertAt(slot, 0, now))
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
	if c, ok := s.host.CfgSnapshot().BreakByID(pick.kind); !ok || !c.Enabled {
		log.Printf("[scheduler] 推迟的 %s 已被关闭或删除，丢弃", pick.kind)
		return false
	}
	log.Printf("[scheduler] 推迟的 %s 到点，弹出提醒（原定 %s）", pick.kind,
		time.UnixMilli(pick.at).Format("15:04:05"))
	s.startBreakAt(pick.kind, pick.at)
	return true
}
