package stats

import (
	"encoding/json"
	"log"
	"time"
	"workerrest/internal/jsonx"
	"workerrest/internal/logx"
)

// Flush 落盘：`open → write → fsync → close → rename`。与 settings 的写盘同套路
// （先写临时文件再改名：写到一半崩溃不会留下半截 JSON；名字快照随文件一起走）。
//
// 为什么要 fsync：只靠 rename 的原子性，保证的是“不会读到写了一半的 JSON”，但数据可能还
// 躺在操作系统的写缓冲里 —— 突然断电就会丢掉最后一次写。本软件的落盘本来就是为“直接关
// 电脑 / 突然断电”准备的，所以数据先刷到盘片、再改名。
func (st *StatsStore) Flush() {
	st.mu.Lock()
	st.dirty = false
	path := st.File()
	text, err := st.marshalLocked()
	st.mu.Unlock()
	if err != nil {
		log.Printf("[stats] 序列化失败: %v", err)
		return
	}
	if path == "" {
		return
	}
	if err := jsonx.WriteFileAtomic(path, text); err != nil {
		log.Printf("[stats] 保存失败: %v", err)
	}
}

// marshalLocked 组装落盘 JSON：`{_customNames: {...}, "YYYY-MM-DD": {...}}`（缩进 2 空格）
func (st *StatsStore) marshalLocked() ([]byte, error) {
	top := map[string]any{"_customNames": st.customNames}
	for date, d := range st.days {
		top[date] = d
	}
	return json.MarshalIndent(top, "", "  ")
}

// Clear 清空全部统计数据：内存表清零、作废未落盘的脏标记并立即写空表
// （关于页“清空统计数据”用）。清空后工作累加器的下一个 tick 会照常建出今天的全 0 条目。
func (st *StatsStore) Clear() {
	st.mu.Lock()
	st.days = map[string]*DayStats{}
	st.customNames = map[string]string{}
	st.dirty = false
	// 清空后计时从零重新开始：工作段的起点一并挪到现在，免得下一次判离开时把清空前的账
	// 一起搬走（搬运受当天 workMs 封顶所以不会为负，但语义上仍不干净）。
	st.workSegStartAt = st.nowFn()
	// 进行中的这次休息也一并作废：刚清空完不该下一秒又被实时入账补回一段休息时长。
	// 随后 break-end 时 OnBreakEnd 见类型为空会直接返回，这次休息也不会记次数。
	st.breakLogType = ""
	st.breakLogStartedAt = 0
	st.breakLogSettledAt = 0
	// 离开段同理作废：清空后不该下一秒又被补回一段离开时长
	st.awayOpen = false
	st.awayStartedAt = 0
	st.awaySettledAt = 0
	st.awayDayKey = ""
	st.awayCountedMs = 0
	// 连续工作段也从头起算：清空后“最长连续工作”必须归零
	st.streakStartMs = 0
	st.streakDayKey = ""
	if st.saveTimer != nil {
		st.saveTimer.Stop()
		st.saveTimer = nil
	}
	// 清空也是“数据变了”：渲染层的“今天”卡要立刻归零
	st.notifyLocked()
	st.mu.Unlock()
	st.Flush()
}

// markDirtyLocked 标脏 + 2 秒防抖落盘（调用方需持锁）。
// 频繁的秒级累加不必每次落盘：合并 2 秒写一次。
// 顺手投一个“已变”通知（非阻塞、容量 1，连着的改动自动合并成一次）。
func (st *StatsStore) markDirtyLocked() {
	st.dirty = true
	st.notifyLocked()
	if st.saveTimer == nil {
		st.saveTimer = time.AfterFunc(2*time.Second, func() {
			st.mu.Lock()
			st.saveTimer = nil
			dirty := st.dirty
			st.mu.Unlock()
			if dirty {
				st.Flush()
			}
		})
	}
}

// notifyLocked 投一个“统计数据已变”通知（调用方需持锁）。
// 通道容量 1 + 非阻塞投递 = 天然合并：notifier 还在处理上一次时，后来的改动只会把
// 同一格再标一次，不会积压出每秒一长串事件。
func (st *StatsStore) notifyLocked() {
	if st.notifyCh == nil {
		return
	}
	select {
	case st.notifyCh <- struct{}{}:
	default:
	}
}

// SetOnChange 注册“统计数据已变”的回调（App 侧注入发事件的实现）。
// 回调在 notifier goroutine 里执行，**不持锁**，可以安全地再读 Today()。
func (st *StatsStore) SetOnChange(fn func()) {
	st.mu.Lock()
	st.onChange = fn
	st.mu.Unlock()
}

// StartNotify 启动通知 goroutine：把合并后的“已变”通知转给 onChange（幂等无保护，
// 只在 startStats 里调一次）。数据没变就不发通知 —— 空闲 / 暂停时不会每秒刷。
func (st *StatsStore) StartNotify() {
	go func() {
		for {
			select {
			case <-st.stopCh:
				return
			case <-st.notifyCh:
				st.mu.Lock()
				fn := st.onChange
				st.mu.Unlock()
				if fn != nil {
					// onChange 是 App 侧的推送函数（读配置 + 发事件），可能 nil deref
					logx.GuardRun("stats.notify", fn)
				}
			}
		}
	}()
}

// StartAutoFlush 周期性落盘（默认 1 分钟一次）：防止直接关电脑 / 突然断电把工作时长、
// 休息时长丢掉。落盘有两层，一起保证“最多丢多少”：
//
//	① 有改动时 2 秒防抖写一次 —— 正常情况下最多丢 2 秒；
//	② 这一层是**上界保证**：即使防抖定时器因为任何原因没跑，最多也只丢 1 分钟。
//	   没有改动时不写盘，不做无谓的磁盘写入。
func (st *StatsStore) StartAutoFlush(intervalMs int) {
	st.mu.Lock()
	st.autoFlushIntervalMs = intervalMs
	done := make(chan struct{})
	st.autoFlushDone = done
	st.mu.Unlock()
	go func() {
		defer close(done)
		t := time.NewTicker(time.Duration(intervalMs) * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-st.stopCh:
				return
			case <-t.C:
				logx.GuardRun("stats.autoflush", func() {
					st.mu.Lock()
					st.autoFlushTicks++
					dirty := st.dirty
					st.mu.Unlock()
					if dirty {
						st.Flush()
					}
				})
			}
		}
	}()
}

// StatsAutoFlushInfo 周期落盘自检信息（冒烟断言用：证明 1 分钟兜底真的在跑）
type StatsAutoFlushInfo struct {
	Ticks      int  `json:"ticks"`
	IntervalMs int  `json:"intervalMs"`
	Dirty      bool `json:"dirty"`
}

// AutoFlushInfo 读周期落盘自检信息
func (st *StatsStore) AutoFlushInfo() StatsAutoFlushInfo {
	st.mu.Lock()
	defer st.mu.Unlock()
	return StatsAutoFlushInfo{Ticks: st.autoFlushTicks, IntervalMs: st.autoFlushIntervalMs, Dirty: st.dirty}
}

// Stop 停止后台 goroutine（退出清理）。
//
// 除了发停止信号，还要**等自动落盘真的退出**：Flush 是“先清脏标记、再写文件”，
// 只发信号就返回的话，调用方（App 退出、单测的 TempDir 清理）可能在一次写盘的中途
// 就把目录删掉，文件随即被重建 —— 表现成“目录删不干净”。
// 顺手作废 2 秒防抖定时器：它同样会写盘，停了就不该再触发。
func (st *StatsStore) Stop() {
	st.stopOnce.Do(func() { close(st.stopCh) })
	st.mu.Lock()
	if st.saveTimer != nil {
		st.saveTimer.Stop()
		st.saveTimer = nil
	}
	done := st.autoFlushDone
	st.mu.Unlock()
	if done != nil {
		<-done
	}
}

// file 当前统计文件路径（每次现算：便携判据可能随配置来源变化）
func (st *StatsStore) File() string {
	if st.fileFn == nil {
		return ""
	}
	return st.fileFn()
}

// ---------- 内存表 ----------

// dayOfLocked 指定本地日期的条目（没有就建一条）
func (st *StatsStore) dayOfLocked(key string) *DayStats {
	d := st.days[key]
	if d == nil {
		e := EmptyDay(key)
		d = &e
		st.days[key] = d
	}
	return d
}

// touchTodayLocked 今天的条目（没有就建一条；跨零点后第一次调用自然翻到新的一天）
func (st *StatsStore) touchTodayLocked() *DayStats {
	return st.dayOfLocked(DayKeyOf(st.nowFn()))
}

// Today 今天的统计（深拷贝）
func (st *StatsStore) Today() DayStats {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.touchTodayLocked().clone()
}

// CustomNames 提醒 id → 名字快照（只读副本）：历史里出现过、后来删掉 / 改名的提醒也查得到
func (st *StatsStore) CustomNames() map[string]string {
	st.mu.Lock()
	defer st.mu.Unlock()
	out := make(map[string]string, len(st.customNames))
	for k, v := range st.customNames {
		out[k] = v
	}
	return out
}

// ---------- 休息时长：进行中逐秒实时入账 ----------

// OnBreakStart 休息开始（main.ts 的 break-start）。
//
// 从这里起休息时长就“边休息边入账”，而不是等 break-end 一次性写。区别在直接关机 /
// 突然断电：后者会把整次休息的时长丢掉，前者最多丢最后一次落盘到关机之间的那 1 分钟。
func (st *StatsStore) OnBreakStart(kind string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	// 上一段还没收尾（异常路径：break-start 撞上 break-start）：先把它的尾账结掉再开新的
	if st.breakLogType != "" {
		st.onBreakTickLocked()
	}
	st.breakLogType = kind
	st.breakLogStartedAt = st.nowFn()
	st.breakLogSettledAt = st.breakLogStartedAt
}

// OnBreakTick 推进休息时长：把自上次入账以来新过去的墙钟时间按本地零点切开、各记各天。
//
//   - **幂等且只增不减**：重复调用只入账“还没入过的那一段”，不会重复计；
//   - **不受睡眠 / 长阻塞守卫影响**：休息本来就按墙上时间算，睡眠跨在休息中时那段照算休息，
//     所以这里的跨度再大也照入账；
//   - 返回本次新增的毫秒数（没有休息在计时时返回 0）。
func (st *StatsStore) OnBreakTick() int64 {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.onBreakTickLocked()
}

func (st *StatsStore) onBreakTickLocked() int64 {
	if st.breakLogType == "" {
		return 0
	}
	now := st.nowFn()
	from := st.breakLogSettledAt
	if now <= from {
		return 0
	}
	st.breakLogSettledAt = now
	for _, seg := range SplitByDay(from, now) {
		d := st.dayOfLocked(seg.date)
		d.BreakMs += seg.ms
		if st.breakLogType == "short" {
			d.ShortMs += seg.ms
		} else if st.breakLogType == "long" {
			d.LongMs += seg.ms
		}
	}
	st.markDirtyLocked()
	return now - from
}

// OnBreakEnd 休息结束（main.ts 的 break-end）：补最后一截，再记次数与名字快照。
//
// **跨零点按本地零点切开**由 OnBreakTick 完成：一次休息从 23:40 到 0:10，前一天记 20 分钟、
// 当天记 10 分钟。次数与名字快照只记在**结束那天** —— 一次休息就是一个事件，切不成两半。
// 时长为 0（点开就结束）时也要记这笔“完成了一次”。
//
// name 是自定义提醒的名字快照（提醒删掉 / 改名后报表还能对上号）。
// early 表示这次是**提前结束**（只对短 / 长休息有意义，见 app.closeBreakWindows 的判定）：
// 提前结束的不计入“完成”，只记 EndEarlyCount —— 时长照实记进 BreakMs / ShortMs / LongMs，
// 因为那确实是坐过的休息时间。
// 返回本次休息的类型与总墙钟时长；没有休息在计时时类型为空串。
func (st *StatsStore) OnBreakEnd(name string, early bool) (string, int64) {
	st.mu.Lock()
	defer st.mu.Unlock()
	kind := st.breakLogType
	if kind == "" {
		return "", 0
	}
	st.onBreakTickLocked()
	ms := st.nowFn() - st.breakLogStartedAt
	last := st.touchTodayLocked()
	// 提前结束只可能是内置短 / 长休息（自定义提醒没有这个按钮，外卖也不走这里）
	if early && kind != "short" && kind != "long" {
		early = false
	}
	switch kind {
	case "short":
		if early {
			last.EndEarlyCount++
		} else {
			last.ShortCount++
		}
	case "long":
		if early {
			last.EndEarlyCount++
		} else {
			last.LongCount++
		}
	default:
		// 自定义提醒（id 为 cb-…）：不并进短/长，单独记一笔，统计页才能如实反映
		last.CustomCount++
		last.CustomCounts[kind]++
		if name != "" {
			st.customNames[kind] = name
		}
	}
	st.breakLogType = ""
	st.breakLogStartedAt = 0
	st.breakLogSettledAt = 0
	// 休息结束 = 新的工作段起点（与“软件启动 / 上一次离开结束”并列）。少了这一句，
	// 休息之后再判离开时，BeginAway 的检索区间会一路回溯到休息**之前**，把上一段的时长
	// 也搬走 —— 搬走的量受当天 workMs 封顶所以不会为负，但会从错的地方扣、账目对不上。
	st.workSegStartAt = st.nowFn()
	st.markDirtyLocked()
	return kind, ms
}

// BreakLogInfo 进行中休息的自检信息（冒烟断言用：证明休息期间真的在实时入账）
func (st *StatsStore) BreakLogInfo() (string, int64) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.breakLogType == "" {
		return "", 0
	}
	return st.breakLogType, st.nowFn() - st.breakLogStartedAt
}

// RecordBreakPostponed 推迟一次休息：记次数 + 推后时长。
// **只由短 / 长休息调用**（调用方 App.onBreakPostponed 已过滤自定义提醒）；
// 自定义提醒与外卖都不进这个口径。
func (st *StatsStore) RecordBreakPostponed(_ string, postponeMs int64) {
	st.mu.Lock()
	defer st.mu.Unlock()
	d := st.touchTodayLocked()
	d.PostponeCount++
	d.PostponeMs += max(0, postponeMs)
	st.markDirtyLocked()
}

// RecordBreakSkipped 跳过一次休息：记次数 + 被跳过那次的休息时长。
// **只由短 / 长休息调用**（调用方 App.onBreakSkipped 已过滤自定义提醒）。
func (st *StatsStore) RecordBreakSkipped(_ string, skippedMs int64) {
	st.mu.Lock()
	defer st.mu.Unlock()
	d := st.touchTodayLocked()
	d.SkipCount++
	d.SkipMs += max(0, skippedMs)
	st.markDirtyLocked()
}

// RecordTakeoutAction 外卖窗口上的按钮：已点好 / 不点了（取消）/ 推迟
func (st *StatsStore) RecordTakeoutAction(action string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	d := st.touchTodayLocked()
	switch action {
	case "ordered":
		d.TakeoutOrdered++
	case "skip":
		d.TakeoutSkipped++
	default:
		d.TakeoutPostponed++
	}
	st.markDirtyLocked()
}

// ---------- 离开时长 ----------

// NotifySuspend 系统进入睡眠 / 休眠：睡眠期间的工作时长不计，整段算“离开”
func (st *StatsStore) NotifySuspend() {
	st.mu.Lock()
	now := st.nowFn()
	st.sleepStartedAt = now
	st.mu.Unlock()
	// 睡眠 = 人不在电脑前 → 整段归离开。幂等：若已因空闲判过离开，起点保持原来的
	// “最后一次输入时刻”（那段空闲本来就是离开，不该被切成两段）
	st.BeginAway(now)
}

// NotifyResume 睡眠结束：这段离开结束，工作段起点挪到现在，醒来后从零重新累计
func (st *StatsStore) NotifyResume() {
	st.mu.Lock()
	st.sleepStartedAt = 0
	st.sleepEndedAt = st.nowFn()
	st.mu.Unlock()
	st.EndAway()
}

// NotifySlept 醒来后补记一段睡眠 [fromMs, toMs)。
//
// 为什么需要“补记”：进程在睡眠期间被挂起，无法在入睡那一刻实时调用 NotifySuspend /
// NotifyResume（这两个方法保留，供未来真有电源事件源时用）。工作累加器在醒来后第一跳
// 检测到“墙钟跳变远超唤醒 tick 跳变”（见 detectSleep），就用这里一次性补记。
//
// 语义：睡眠 = 人不在电脑前，整段算离开（**不跨天**，只记睡眠起点所在那天 —— 周末
// 合盖两天不会变成几十小时的离开）；工作段从醒来时刻重新起算。不是“从工作搬”——
// 工作累加器在睡眠期间停摆，这段从没记进过工作。
func (st *StatsStore) NotifySlept(fromMs, toMs int64) {
	st.mu.Lock()
	defer st.mu.Unlock()
	day := DayKeyOf(fromMs)
	var added int64
	for _, seg := range SplitByDay(fromMs, toMs) {
		if seg.date == day {
			st.dayOfLocked(seg.date).AwayMs += seg.ms
			added += seg.ms
		}
	}
	// 若正处在空闲离开中，把尾账锚点推进到醒来时刻，避免 onAwayTick 重复记这段睡眠
	if st.awayOpen && st.awaySettledAt < toMs {
		st.awaySettledAt = toMs
	}
	// 睡了一觉 → 连续工作段断在入睡那一刻
	st.endStreakLocked(fromMs)
	st.workSegStartAt = toMs
	if added > 0 {
		st.markDirtyLocked()
	}
}

// CountedSpan 算出这一跳真正该计入**工作**的区间 `[a, b)`，跨睡眠的部分剪掉；
// 整跳都不该计入时 ok=false。
//
//   - sleepStartedAt != 0（睡眠中）：这一跳最多只算到入睡那一刻 —— 之后每一跳都落在入睡
//     之后，b 被钉在 sleepStartedAt 上，`b <= a` 直接返回 false，睡眠期间一秒都不计；
//   - sleepEndedAt ∈ (last, now]：这一跳跨过了醒来时刻 → 从醒来那一刻开始算。
//
// 睡眠那一段本身由离开段负责补记（见 BeginAway / OnAwayTick），不在这里补。
//
// 为什么睡眠中要显式钉住 b（而不是只靠“睡眠期间定时器不跑”）：真机上进程被挂起、1 秒
// 节拍确实停摆，但进程若仍能跑（合盖前的短暂 suspend），只有第一跳会被 sleepStartedAt
// 剪到，从第二跳起它已在 last 之后、不再起剪裁作用。钉住 b 让“睡着就不算工作”在任何
// 情况下成立。
func CountedSpan(last, now, sleepStartedAt, sleepEndedAt int64) (int64, int64, bool) {
	a, b := last, now
	if sleepStartedAt != 0 {
		b = min(b, sleepStartedAt)
	} else if sleepEndedAt > a && sleepEndedAt <= b {
		a = sleepEndedAt
	}
	return a, b, b > a
}

// BeginAway 判定为“离开”：把“从最后一次键鼠输入到现在”这段**从 WorkMs 搬到 AwayMs**。
//
// 为什么是“搬”而不是“扣掉”：空闲判定是轮询的，在判定为空闲之前那 N 分钟已经被按秒
// 计入了工作时长，而人其实早已离开工位。原先的实现是把这段扣掉、且扣掉之后它不进入任何
// 字段 —— 账因此不闭合（工作少了、休息没涨，凭空消失）。搬进 AwayMs 之后三个桶互补：
// `工作 + 休息 + 离开 ≈ 软件运行时长`。
//
// 三重封顶，保证只搬“确实被计入过、且属于本段”的部分：
//
//	① 区间左端不越过本工作段的起点 workSegStartAt —— 不会搬到上一段去；
//	② 每一段各自用**那一天**已计入的 WorkMs 封顶 —— 绝不为负；
//	③ **离开不跨天**：只搬“离开起点所在那天”的那部分，零点之后不再计入。
//
// sinceTs 是离开起点（最后一次键鼠输入的时刻）。返回实际搬走的毫秒数。
