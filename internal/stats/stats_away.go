package stats

import (
	"time"
	"workerrest/internal/logx"
	"workerrest/internal/winpower"
)

func (st *StatsStore) BeginAway(sinceTs int64) int64 {
	st.mu.Lock()
	defer st.mu.Unlock()
	now := st.nowFn()
	if st.awayOpen {
		// 已经在离开中（睡眠撞上已判定的空闲离开）：把尾账推到现在即可，不重开一段
		st.onAwayTickLocked()
		return 0
	}
	from := max(sinceTs, st.workSegStartAt)
	// 判为离开 = 人离开工位 → 连续工作段到此为止（以最后一次键鼠输入为终点）
	st.endStreakLocked(from)
	st.awayOpen = true
	st.awayStartedAt = sinceTs
	st.awaySettledAt = now
	st.awayDayKey = DayKeyOf(from)
	st.awayCountedMs = 0
	if now <= from {
		return 0
	}
	for _, seg := range SplitByDay(from, now) {
		if seg.date != st.awayDayKey {
			continue
		}
		d := st.dayOfLocked(seg.date)
		take := min(seg.ms, d.WorkMs)
		if take <= 0 {
			continue
		}
		d.WorkMs -= take
		d.AwayMs += take
		st.awayCountedMs += take
	}
	if st.awayCountedMs > 0 {
		st.markDirtyLocked()
	}
	return st.awayCountedMs
}

// OnAwayTick 推进离开时长：把自上次入账以来新过去的时间记进 AwayMs。
//
//   - **幂等**：重复调用只入账“还没入过的那一段”；
//   - **不跨天**：只累计“离开起点所在那天”的部分，跨过本地零点之后的时长一律不再计入
//     （直到检测到活动、这段离开结束）。这样周末不关机的离开不会变成几十小时；
//   - 睡眠期间定时器停摆，所以睡眠那一段是在醒来时（NotifyResume → EndAway）被一次性补记的。
func (st *StatsStore) OnAwayTick() int64 {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.onAwayTickLocked()
}

func (st *StatsStore) onAwayTickLocked() int64 {
	if !st.awayOpen {
		return 0
	}
	now := st.nowFn()
	from := st.awaySettledAt
	if now <= from {
		return 0
	}
	st.awaySettledAt = now
	var added int64
	for _, seg := range SplitByDay(from, now) {
		if seg.date != st.awayDayKey {
			continue
		}
		st.dayOfLocked(seg.date).AwayMs += seg.ms
		added += seg.ms
	}
	if added > 0 {
		st.awayCountedMs += added
		st.markDirtyLocked()
	}
	return added
}

// EndAway 结束这段离开（检测到活动 / 睡眠结束）：补最后一截，工作段从此刻重新起算
func (st *StatsStore) EndAway() {
	st.mu.Lock()
	defer st.mu.Unlock()
	if !st.awayOpen {
		return
	}
	st.onAwayTickLocked()
	st.awayOpen = false
	st.awayStartedAt = 0
	st.awaySettledAt = 0
	st.awayDayKey = ""
	st.awayCountedMs = 0
	st.workSegStartAt = st.nowFn()
	st.markDirtyLocked()
}

// AwayInfo 进行中离开段的自检信息（冒烟断言用）
type StatsAwayInfo struct {
	Open      bool   `json:"open"`
	StartedAt int64  `json:"startedAt"`
	CountedMs int64  `json:"countedMs"`
	DayKey    string `json:"dayKey"`
}

// AwayInfo 读进行中离开段的自检信息
func (st *StatsStore) AwayInfo() StatsAwayInfo {
	st.mu.Lock()
	defer st.mu.Unlock()
	return StatsAwayInfo{Open: st.awayOpen, StartedAt: st.awayStartedAt, CountedMs: st.awayCountedMs, DayKey: st.awayDayKey}
}

// AwayPartOf 一段区间里属于 `day` 那天的毫秒数（冒烟断言用）。
// 真实的“离开不跨天”过滤就发生在 BeginAway / OnAwayTick 里的 `seg.date != awayDayKey`
// 那一行，这里只是把同一个过滤单独暴露出来，好让断言能在不伪造系统时间的前提下验证它。
func AwayPartOf(from, to int64, day string) int64 {
	var ms int64
	for _, seg := range SplitByDay(from, to) {
		if seg.date == day {
			ms += seg.ms
		}
	}
	return ms
}

// ---------- 工作时长累加器 ----------

// StartWorkAccumulator 工作时长累加器：每秒一跳。
//
// 口径：
//   - **工作段起点 = 软件启动 / 上一次休息结束 / 上一次离开结束**（workSegStartAt）；
//   - 两个判定由调用方注入（依赖调度器与空闲状态机，本模块不反向依赖）：
//     isBreak() = 休息窗口正显示 —— 这一跳归休息（同一个节拍推进 OnBreakTick）；
//     isIdle() = 连续无输入已达阈值 —— 这一跳归离开（App 侧会调 BeginAway 搬运）。
//     手动暂停 / 勿扰暂停 / 指定应用占用**都不在这两个判定里** —— 那几种状态下用户仍在
//     用电脑（开会、看视频、跑全屏应用），照样计入；
//   - 空闲那一跳**只跳过、不改任何记账状态**：搬运区间由 workSegStartAt 与当天 WorkMs
//     双重封顶，不需要额外的余额变量；
//   - **系统睡眠 / 休眠**：只算到入睡那一刻，醒来后重新开始（见 CountedSpan）；
//   - 每次计入都**按本地零点切开、各记各天**（见 SplitByDay）。
// ---------- 连续工作段（久坐指标）----------
//
// “最长连续工作”= 今天最长的一段不间断工作时长。口径：
//   - 计时 goroutine 判为“这一跳算工作”就延续当前段（起算点是这一跳的 a）；
//   - 休息开始 / 空闲 / 睡眠 / 判为离开 / 清空统计 → 段结束；
//   - 暂停休息（pause）不算打断 —— 人还坐在电脑前，久坐风险照样存在；
//   - 跨零点按天切开，各天记自己那一段。
//
// 与 WorkMs 的区别只有一点：WorkMs 是累加，MaxStreakMs 取最长。

// streakBeginLocked 不在连续工作段里就在这一跳的起点起算（调用方需持锁）
func (st *StatsStore) streakBeginLocked(from int64) {
	if st.streakStartMs == 0 {
		st.streakStartMs = from
		// 段数组的占位推迟到第一次 settle（那时才知道这一跳算到哪一天）
		st.streakDayKey = ""
	}
}

// settleStreakLocked 用 [起点, to) 刷新各天的最长连续工作时长与段数组（不动起点，供每跳实时更新）
func (st *StatsStore) settleStreakLocked(to int64) {
	if st.streakStartMs == 0 || to <= st.streakStartMs {
		return
	}
	changed := false
	segs := SplitByDay(st.streakStartMs, to)
	for i, seg := range segs {
		d := st.dayOfLocked(seg.date)
		if seg.ms > d.MaxStreakMs {
			d.MaxStreakMs = seg.ms
			changed = true
		}
		// 最后一段是“到 to 为止的那段”= 进行中：同一天就改写末项（每跳实时涨），
		// 跨到新的一天就在新的一天追加；更早的段是跨零点切出来的定稿段，只在还没记过
		// 那一天时才追加（否则每跳都会把同一段重复追加一遍）。
		last := i == len(segs)-1
		if last && st.streakDayKey == seg.date && len(d.WorkSegments) > 0 {
			if d.WorkSegments[len(d.WorkSegments)-1] != seg.ms {
				d.WorkSegments[len(d.WorkSegments)-1] = seg.ms
				changed = true
			}
			continue
		}
		if !last && seg.date <= st.streakDayKey {
			continue
		}
		if len(d.WorkSegments) >= maxSegsPerDay {
			continue
		}
		d.WorkSegments = append(d.WorkSegments, seg.ms)
		st.streakDayKey = seg.date
		changed = true
	}
	if changed {
		st.markDirtyLocked()
	}
}

// endStreakLocked 结束当前连续工作段：先把 [起点, at) 结算进当天，再清掉起点（调用方需持锁）。
// 段数组末项已经在 settle 里定稿，这里不追加。
func (st *StatsStore) endStreakLocked(at int64) {
	if st.streakStartMs == 0 {
		return
	}
	st.settleStreakLocked(at)
	st.streakStartMs = 0
	st.streakDayKey = ""
}

// endStreakAt 带锁版本的段结束（计时 goroutine / 各通知入口用）
func (st *StatsStore) endStreakAt(at int64) {
	st.mu.Lock()
	st.endStreakLocked(at)
	st.mu.Unlock()
}

// accountWorkTick 把 [a, b) 这一跳记成工作，并延续/刷新连续工作段（计时 goroutine 用）。
// 抽成方法是为了能被单测直接驱动 —— 1 秒一拍的循环体在测试里跑不起来。
func (st *StatsStore) accountWorkTick(a, b int64) {
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, seg := range SplitByDay(a, b) {
		st.dayOfLocked(seg.date).WorkMs += seg.ms
	}
	st.streakBeginLocked(a)
	st.settleStreakLocked(b)
	st.markDirtyLocked()
}

func (st *StatsStore) StartWorkAccumulator(isBreak, isIdle func() bool) {
	st.mu.Lock()
	st.workSegStartAt = st.nowFn()
	st.mu.Unlock()
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		last := st.nowFn()
		lastTick, haveTick := int64(0), false
		if st.wakeTickFn != nil {
			lastTick, haveTick = st.wakeTickFn()
		}
		for {
			select {
			case <-st.stopCh:
				return
			case <-t.C:
				// 整拍包进闭包：里面的 continue 一律改写成 return（闭包返回 = 本拍结束，
				// continue 的语义不变），换来一次 panic 只丢一拍。last / lastTick / haveTick
				// 在闭包外声明，闭包直接改它们 —— 跨拍状态照常延续。
				logx.GuardRun("stats.workacc", func() {
					now := st.nowFn()
					// 睡眠检测：墙钟跳变 - 唤醒 tick 跳变 > 阈值 → 这中间睡过一觉。
					// 醒来后第一跳就触发，把睡眠整段补记成离开并跳过工作记账（见 NotifySlept）。
					if haveTick && st.wakeTickFn != nil {
						if tick, ok := st.wakeTickFn(); ok {
							if winpower.DetectSleep(now-last, tick-lastTick) > 0 {
								st.NotifySlept(last, now)
								last = now
								lastTick = tick
								return
							}
							lastTick = tick
						}
					}
					from := last
					last = now
					// 休息 = 这一跳归休息：顺手推进休息计时（休息时长同样是实时入账的，
					// 否则进行中的休息只能等 break-end 才写盘，断电就整次丢掉）
					if isBreak() {
						st.endStreakAt(now)
						st.OnBreakTick()
						return
					}
					st.mu.Lock()
					a, b, ok := CountedSpan(from, now, st.sleepStartedAt, st.sleepEndedAt)
					st.mu.Unlock()
					if !ok {
						// 整跳都在睡眠里 → 连续工作段断在这里
						st.endStreakAt(now)
						return
					}
					// 空闲（含“大跳时按当时的空闲值归属”这一条）：这一跳不计工作 ——
					// 那段由离开段负责记进 AwayMs（App 侧的监视会调 BeginAway 搬过去）
					if isIdle() {
						st.endStreakAt(now)
						return
					}
					st.accountWorkTick(a, b)
				})
			}
		}
	}()
}
