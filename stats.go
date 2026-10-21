package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// 使用统计：按**本地日期**记账，落盘在统计文件里（落点见 App.appStatsFile）。
// 移植自 历史源码/stats.ts，口径逐条对齐。
//
// 三个桶（设置页「统计」与这里一一对应），同一时刻只会落进其中一个：
//   - **工作 WorkMs**：人还在用电脑。起点是软件启动 / 上一次休息结束 / 上一次离开结束，
//     此后按秒累加。手动暂停提醒、勿扰暂停、指定应用占用暂停**都照常计入** ——
//     那几种状态下用户仍在用电脑（开会、看视频、跑全屏应用），不是离开；
//   - **休息 BreakMs**：软件安排的休息窗口内的实际时长（提前结束只算实际部分）。
//     **休息期间逐秒实时入账**（见 OnBreakTick），不是等休息结束才一次性写：正在进行中的
//     休息因此也被每分钟的兜底落盘带走，中途关机 / 断电最多丢 1 分钟，而不是把整次休息
//     的时长全丢掉（次数仍只在结束时记，见 OnBreakEnd）；
//   - **离开 AwayMs**：连续无键鼠输入超过「系统空闲 N 分钟」的那段，以及系统睡眠 / 休眠的
//     整段。判定发生的瞬间，把「从最后一次输入到现在」这段**从 WorkMs 搬到 AwayMs**
//     （见 BeginAway）—— 扣掉之后它没有凭空消失，账因此闭合：
//     `工作 + 休息 + 离开 ≈ 软件运行时长`（仅限软件运行期间；退出 / 关机不属于任何桶）。
//   - **离开不跨天**：一段离开最多记到「它起点那天的本地零点」，跨天之后不再累加
//     （除非又检测到活动、离开结束）。这样周末不关机也不会凭空多出几十小时的离开。
//   - **跨零点按本地时间切开**：工作与休息的每一段都按本地零点切成多段、各归各天
//     （见 splitByDay）—— 23:58 离开、0:03 才判定空闲，两天各自的那部分各归各天。
//   - **系统睡眠 / 休眠**：工作时长只算到入睡那一刻、醒来后重新开始；睡眠整段归「离开」。
//     睡眠时如果正处于休息窗口，休息算到入睡那一刻就结算关窗，剩下的归「离开」。
//   - 推迟休息 / 跳过休息 / 外卖三动作各记一笔（见 Record* 三个方法）。
//   - 落盘：有改动时 2 秒防抖写一次，另有每 1 分钟的兜底写盘（见 StartAutoFlush），
//     退出时再无条件写一次（ServiceShutdown → Flush）。

// DayStats 一天的统计（字段与 stats.ts 的 DayStats 一一对应）
type DayStats struct {
	// Date 本地日期 YYYY-MM-DD
	Date string `json:"date"`
	// WorkMs 工作时长（毫秒）
	WorkMs int64 `json:"workMs"`
	// BreakMs 实际休息时长（毫秒）：只含软件安排的休息窗口
	BreakMs int64 `json:"breakMs"`
	// AwayMs 离开时长（毫秒）：连续无键鼠输入超过阈值的那段 + 系统睡眠整段
	// （**不跨天** —— 一段离开最多记到它起点那天的本地零点）
	AwayMs int64 `json:"awayMs"`
	// ShortMs 实际短休息时长（毫秒）：休息报表堆叠柱的「短休息」层
	ShortMs int64 `json:"shortMs"`
	// LongMs 实际长休息时长（毫秒）：休息报表堆叠柱的「长休息」层
	LongMs int64 `json:"longMs"`
	// ShortCount 完成的短休息次数
	ShortCount int64 `json:"shortCount"`
	// LongCount 完成的长休息次数
	LongCount int64 `json:"longCount"`
	// CustomCount 完成的自定义提醒次数（与短/长分开记，免得混进「短休息 N 次」里）
	CustomCount int64 `json:"customCount"`
	// CustomCounts 按提醒 id 分的触发次数（自定义提醒报表按提醒拆系列用）
	CustomCounts map[string]int64 `json:"customCounts"`
	// PostponeCount 推迟休息次数
	PostponeCount int64 `json:"postponeCount"`
	// PostponeMs 推迟累计时长（毫秒）
	PostponeMs int64 `json:"postponeMs"`
	// SkipCount 跳过休息次数
	SkipCount int64 `json:"skipCount"`
	// SkipMs 跳过累计时长（毫秒）：被跳过那几次休息的时长合计
	SkipMs int64 `json:"skipMs"`
	// TakeoutOrdered 点外卖次数
	TakeoutOrdered int64 `json:"takeoutOrdered"`
	// TakeoutSkipped 取消点外卖次数（窗口上点「不点了」）
	TakeoutSkipped int64 `json:"takeoutSkipped"`
	// TakeoutPostponed 推迟点外卖次数
	TakeoutPostponed int64 `json:"takeoutPostponed"`
}

// statNumKeys 与 stats.ts 的 NUM_KEYS 同序：聚合、归一化、周期汇总都按这份清单走。
// 注意 shortMs / longMs 也在里面 —— 周期汇总的输出里确实带这两个字段。
var statNumKeys = []string{
	"workMs", "breakMs", "awayMs", "shortMs", "longMs", "shortCount", "longCount", "customCount",
	"postponeCount", "postponeMs", "skipCount", "skipMs",
	"takeoutOrdered", "takeoutSkipped", "takeoutPostponed",
}

// numPtr 取某个数值字段的地址（nil = 未知键）
func numPtr(d *DayStats, key string) *int64 {
	switch key {
	case "workMs":
		return &d.WorkMs
	case "breakMs":
		return &d.BreakMs
	case "awayMs":
		return &d.AwayMs
	case "shortMs":
		return &d.ShortMs
	case "longMs":
		return &d.LongMs
	case "shortCount":
		return &d.ShortCount
	case "longCount":
		return &d.LongCount
	case "customCount":
		return &d.CustomCount
	case "postponeCount":
		return &d.PostponeCount
	case "postponeMs":
		return &d.PostponeMs
	case "skipCount":
		return &d.SkipCount
	case "skipMs":
		return &d.SkipMs
	case "takeoutOrdered":
		return &d.TakeoutOrdered
	case "takeoutSkipped":
		return &d.TakeoutSkipped
	case "takeoutPostponed":
		return &d.TakeoutPostponed
	}
	return nil
}

// statDateRe 合法日期键（与 stats.ts 的 /^\d{4}-\d{2}-\d{2}$/ 同口径）
var statDateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

func emptyDay(date string) DayStats {
	return DayStats{Date: date, CustomCounts: map[string]int64{}}
}

// clone 深拷贝（CustomCounts 是 map，调用方会在锁外读它）
func (d DayStats) clone() DayStats {
	out := d
	out.CustomCounts = make(map[string]int64, len(d.CustomCounts))
	for k, v := range d.CustomCounts {
		out.CustomCounts[k] = v
	}
	return out
}

// normalizeDay 把磁盘上任何形态的条目夹紧成合法值（旧版本数据 / 手改都不会炸）
func normalizeDay(raw any, date string) DayStats {
	o, _ := raw.(map[string]any)
	out := emptyDay(date)
	for _, k := range statNumKeys {
		v := numFromAny(o[k])
		if v > 0 {
			*numPtr(&out, k) = int64(math.Round(v))
		}
	}
	// 按 id 分的提醒计数：只认 cb- 前缀键与正整数，其它形态一概丢弃
	if cc, ok := o["customCounts"].(map[string]any); ok {
		for id, rv := range cc {
			if !strings.HasPrefix(id, "cb-") {
				continue
			}
			if v := numFromAny(rv); v > 0 {
				out.CustomCounts[id] = int64(math.Round(v))
			}
		}
	}
	return out
}

// numFromAny 宽容地取数字：非数字 / 非有限值一律当 0
func numFromAny(v any) float64 {
	switch n := v.(type) {
	case float64:
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return 0
		}
		return n
	case json.Number:
		f, err := n.Float64()
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return 0
		}
		return f
	case int64:
		return float64(n)
	case int:
		return float64(n)
	}
	return 0
}

// ---------- 本地日期工具（与 stats.ts 的 dayKey / dayStartTs / splitByDay 同口径） ----------

// dayKeyOf 某个时刻所在**本地**日期的 YYYY-MM-DD
func dayKeyOf(ts int64) string {
	return time.UnixMilli(ts).Local().Format("2006-01-02")
}

// dayStartTs 某个时刻所在本地日期的零点（毫秒时间戳）
func dayStartTs(ts int64) int64 {
	d := time.UnixMilli(ts).Local()
	return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.Local).UnixMilli()
}

// daySeg 一段属于某个本地日期的时长
type daySeg struct {
	date string
	ms   int64
}

// splitByDay 把一段时间 `[from, to)` 按本地零点切成若干段，每段带自己所属的本地日期。
//
// 跨零点时返回多段：23:58 → 次日 00:03 切成「前一天 2 分钟 + 当天 3 分钟」，各记各天的账。
// 正常 1 秒一跳的累加只会得到单段，只有跨零点那一跳才真正切开；休息跨零点（含睡眠跨零点）
// 则可能一次切出多段。
//
// `+26 小时` 再取零点 = 次日的零点：夏令时下当天可能是 23 或 25 小时，26 小时一定越过次日
// 零点而不会跳到后天（中国无夏令时，这里只是不把逻辑写死）。
func splitByDay(from, to int64) []daySeg {
	out := []daySeg{}
	cur := from
	// 防御：时间戳逆序或异常大跨度时不要转不出去（正常最多切 2~3 段）
	for guard := 0; cur < to && guard < 1000; guard++ {
		end := to
		if next := dayStartTs(dayStartTs(cur) + 26*3600_000); next < end {
			end = next
		}
		if ms := end - cur; ms > 0 {
			out = append(out, daySeg{date: dayKeyOf(cur), ms: ms})
		}
		cur = end
	}
	return out
}

// ---------- 统计存储 ----------

// StatsStore 统计存储与记账。所有导出方法都可并发调用（自带锁）。
type StatsStore struct {
	mu sync.Mutex

	days map[string]*DayStats
	// customNames 提醒 id → 名字快照：提醒删掉 / 改名后，自定义提醒报表还能显示历史数据
	// 是哪条提醒。与天记录同文件持久化（保留键 _customNames，不进 days 表）
	customNames map[string]string

	dirty               bool
	saveTimer           *time.Timer
	autoFlushTicks      int
	autoFlushIntervalMs int

	// 进行中休息的记账上下文：
	//   breakLogType 为空 = 当前没有休息在计时；
	//   breakLogStartedAt 本次休息的开始时刻，结束时用来报总时长；
	//   breakLogSettledAt 上一次入账的时刻 —— 每跳只入账 `[上次入账, 现在)` 这一段，
	//   所以 tick 被调用多少次都不会重复计。
	breakLogType      string
	breakLogStartedAt int64
	breakLogSettledAt int64

	// 本工作段的起点：软件启动 / 上一次休息结束 / 上一次离开结束时置为当时。用途只有一个
	// —— 把工作搬去离开（BeginAway）时，检索区间不越过它，免得把上一段的账一起搬走。
	workSegStartAt int64
	// sleepStartedAt 系统睡眠开始的时刻（0 = 当前没在睡眠）
	sleepStartedAt int64
	// sleepEndedAt 最近一次从睡眠中醒来的时刻（0 = 本进程还没睡过）
	sleepEndedAt int64

	// 进行中离开段的记账上下文：
	//   awayOpen 当前是否正处在离开中；
	//   awayStartedAt 这段离开的起点（墙钟），日志与自检用；
	//   awaySettledAt 上一次入账的时刻（每跳只入账 `[上次入账, 现在)`，天然幂等）；
	//   awayDayKey **离开起点所在那天** —— 离开不跨天，零点之后不再累计；
	//   awayCountedMs 本段已累计进 awayMs 的量（自检用）。
	awayOpen      bool
	awayStartedAt int64
	awaySettledAt int64
	awayDayKey    string
	awayCountedMs int64

	// 注入点（单测替换成假时钟 / 假落点 / 假唤醒 tick 源）
	nowFn      func() int64
	fileFn     func() string
	wakeTickFn func() (int64, bool)

	stopCh   chan struct{}
	stopOnce sync.Once

	// notifyCh 「统计数据已变」的合并通知通道（容量 1）：工作时长每秒都在涨，
	// 连续的多次改动合并成一次通知 —— 渲染层只需要知道「后来变了」，中间态没意义。
	notifyCh chan struct{}
	// onChange 由 App 注入：把「已变」转成发给渲染层的事件（见 StartNotify）。
	// 在独立的 notifier goroutine 里调用，调用时不持 mu，可以放心再读 Today()。
	onChange func()

	// loadOnce 让「首次读盘」只发生一次：App.statsStore 会在**不持自己的锁**的情况下调
	// LoadOnce —— Load 里要回读配置路径，而那条路径需要 App 的锁，持锁调用会自死锁
	//（本项目踩过一次：idleWatchTick → statsStore → Load → appStatsFile → cfgSnapshot）。
	loadOnce sync.Once
}

// LoadOnce 首次读盘（幂等、并发安全）。App 侧一律用它，不要直接调 Load。
func (st *StatsStore) LoadOnce() {
	st.loadOnce.Do(func() { st.Load() })
}

// NewStatsStore 建一个统计存储（**不读盘**：要调 Load）
func NewStatsStore(fileFn func() string) *StatsStore {
	now := nowMs()
	return &StatsStore{
		days:           map[string]*DayStats{},
		customNames:    map[string]string{},
		workSegStartAt: now,
		nowFn:          nowMs,
		fileFn:         fileFn,
		wakeTickFn:     sysWakeTick,
		stopCh:         make(chan struct{}),
		notifyCh:       make(chan struct{}, 1),
	}
}

// Load 读统计文件。没有文件或内容损坏 = 从空表开始，不是错误。
// 统计数据全部永久保留，这里不裁剪历史条目。
func (st *StatsStore) Load() {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.days = map[string]*DayStats{}
	st.customNames = map[string]string{}
	path := st.file()
	if path == "" {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var top map[string]any
	if err := json.Unmarshal(raw, &top); err != nil {
		log.Printf("[stats] 统计文件解析失败（从空表开始）：%v", err)
		return
	}
	// 名字快照（保留键，不是日期条目）：只认 cb- 前缀与非空字符串
	if names, ok := top["_customNames"].(map[string]any); ok {
		for id, v := range names {
			if strings.HasPrefix(id, "cb-") {
				if s, ok := v.(string); ok && s != "" {
					st.customNames[id] = s
				}
			}
		}
	}
	for date, v := range top {
		if !statDateRe.MatchString(date) {
			continue
		}
		d := normalizeDay(v, date)
		st.days[date] = &d
	}
}

// Flush 落盘：`open → write → fsync → close → rename`。与 settings 的写盘同套路
// （先写临时文件再改名：写到一半崩溃不会留下半截 JSON；名字快照随文件一起走）。
//
// 为什么要 fsync：只靠 rename 的原子性，保证的是「不会读到写了一半的 JSON」，但数据可能还
// 躺在操作系统的写缓冲里 —— 突然断电就会丢掉最后一次写。本软件的落盘本来就是为「直接关
// 电脑 / 突然断电」准备的，所以数据先刷到盘片、再改名。
func (st *StatsStore) Flush() {
	st.mu.Lock()
	st.dirty = false
	path := st.file()
	text, err := st.marshalLocked()
	st.mu.Unlock()
	if err != nil {
		log.Printf("[stats] 序列化失败: %v", err)
		return
	}
	if path == "" {
		return
	}
	if err := writeFileAtomic(path, text); err != nil {
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
// （关于页「清空统计数据」用）。清空后工作累加器的下一个 tick 会照常建出今天的全 0 条目。
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
	if st.saveTimer != nil {
		st.saveTimer.Stop()
		st.saveTimer = nil
	}
	// 清空也是「数据变了」：渲染层的「今天」卡要立刻归零
	st.notifyLocked()
	st.mu.Unlock()
	st.Flush()
}

// markDirtyLocked 标脏 + 2 秒防抖落盘（调用方需持锁）。
// 频繁的秒级累加不必每次落盘：合并 2 秒写一次。
// 顺手投一个「已变」通知（非阻塞、容量 1，连着的改动自动合并成一次）。
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

// notifyLocked 投一个「统计数据已变」通知（调用方需持锁）。
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

// SetOnChange 注册「统计数据已变」的回调（App 侧注入发事件的实现）。
// 回调在 notifier goroutine 里执行，**不持锁**，可以安全地再读 Today()。
func (st *StatsStore) SetOnChange(fn func()) {
	st.mu.Lock()
	st.onChange = fn
	st.mu.Unlock()
}

// StartNotify 启动通知 goroutine：把合并后的「已变」通知转给 onChange（幂等无保护，
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
					fn()
				}
			}
		}
	}()
}

// StartAutoFlush 周期性落盘（默认 1 分钟一次）：防止直接关电脑 / 突然断电把工作时长、
// 休息时长丢掉。落盘有两层，一起保证「最多丢多少」：
//
//	① 有改动时 2 秒防抖写一次 —— 正常情况下最多丢 2 秒；
//	② 这一层是**上界保证**：即使防抖定时器因为任何原因没跑，最多也只丢 1 分钟。
//	   没有改动时不写盘，不做无谓的磁盘写入。
func (st *StatsStore) StartAutoFlush(intervalMs int) {
	st.mu.Lock()
	st.autoFlushIntervalMs = intervalMs
	st.mu.Unlock()
	go func() {
		t := time.NewTicker(time.Duration(intervalMs) * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-st.stopCh:
				return
			case <-t.C:
				st.mu.Lock()
				st.autoFlushTicks++
				dirty := st.dirty
				st.mu.Unlock()
				if dirty {
					st.Flush()
				}
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

// Stop 停止后台 goroutine（退出清理）
func (st *StatsStore) Stop() {
	st.stopOnce.Do(func() { close(st.stopCh) })
}

// file 当前统计文件路径（每次现算：便携判据可能随配置来源变化）
func (st *StatsStore) file() string {
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
		e := emptyDay(key)
		d = &e
		st.days[key] = d
	}
	return d
}

// touchTodayLocked 今天的条目（没有就建一条；跨零点后第一次调用自然翻到新的一天）
func (st *StatsStore) touchTodayLocked() *DayStats {
	return st.dayOfLocked(dayKeyOf(st.nowFn()))
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
// 从这里起休息时长就「边休息边入账」，而不是等 break-end 一次性写。区别在直接关机 /
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
//   - **幂等且只增不减**：重复调用只入账「还没入过的那一段」，不会重复计；
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
	for _, seg := range splitByDay(from, now) {
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
// 时长为 0（点开就结束）时也要记这笔「完成了一次」。
//
// name 是自定义提醒的名字快照（提醒删掉 / 改名后报表还能对上号）。
// 返回本次休息的类型与总墙钟时长；没有休息在计时时类型为空串。
func (st *StatsStore) OnBreakEnd(name string) (string, int64) {
	st.mu.Lock()
	defer st.mu.Unlock()
	kind := st.breakLogType
	if kind == "" {
		return "", 0
	}
	st.onBreakTickLocked()
	ms := st.nowFn() - st.breakLogStartedAt
	last := st.touchTodayLocked()
	switch kind {
	case "short":
		last.ShortCount++
	case "long":
		last.LongCount++
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
	// 休息结束 = 新的工作段起点（与「软件启动 / 上一次离开结束」并列）。少了这一句，
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

// RecordBreakPostponed 推迟一次休息：记次数 + 推后时长
func (st *StatsStore) RecordBreakPostponed(_ string, postponeMs int64) {
	st.mu.Lock()
	defer st.mu.Unlock()
	d := st.touchTodayLocked()
	d.PostponeCount++
	d.PostponeMs += max(0, postponeMs)
	st.markDirtyLocked()
}

// RecordBreakSkipped 跳过一次休息：记次数 + 被跳过那次的休息时长
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

// NotifySuspend 系统进入睡眠 / 休眠：睡眠期间的工作时长不计，整段算「离开」
func (st *StatsStore) NotifySuspend() {
	st.mu.Lock()
	now := st.nowFn()
	st.sleepStartedAt = now
	st.mu.Unlock()
	// 睡眠 = 人不在电脑前 → 整段归离开。幂等：若已因空闲判过离开，起点保持原来的
	// 「最后一次输入时刻」（那段空闲本来就是离开，不该被切成两段）
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
// 为什么需要「补记」：进程在睡眠期间被挂起，无法在入睡那一刻实时调用 NotifySuspend /
// NotifyResume（这两个方法保留，供未来真有电源事件源时用）。工作累加器在醒来后第一跳
// 检测到「墙钟跳变远超唤醒 tick 跳变」（见 detectSleep），就用这里一次性补记。
//
// 语义：睡眠 = 人不在电脑前，整段算离开（**不跨天**，只记睡眠起点所在那天 —— 周末
// 合盖两天不会变成几十小时的离开）；工作段从醒来时刻重新起算。不是「从工作搬」——
// 工作累加器在睡眠期间停摆，这段从没记进过工作。
func (st *StatsStore) NotifySlept(fromMs, toMs int64) {
	st.mu.Lock()
	defer st.mu.Unlock()
	day := dayKeyOf(fromMs)
	var added int64
	for _, seg := range splitByDay(fromMs, toMs) {
		if seg.date == day {
			st.dayOfLocked(seg.date).AwayMs += seg.ms
			added += seg.ms
		}
	}
	// 若正处在空闲离开中，把尾账锚点推进到醒来时刻，避免 onAwayTick 重复记这段睡眠
	if st.awayOpen && st.awaySettledAt < toMs {
		st.awaySettledAt = toMs
	}
	st.workSegStartAt = toMs
	if added > 0 {
		st.markDirtyLocked()
	}
}

// countedSpan 算出这一跳真正该计入**工作**的区间 `[a, b)`，跨睡眠的部分剪掉；
// 整跳都不该计入时 ok=false。
//
//   - sleepStartedAt != 0（睡眠中）：这一跳最多只算到入睡那一刻 —— 之后每一跳都落在入睡
//     之后，b 被钉在 sleepStartedAt 上，`b <= a` 直接返回 false，睡眠期间一秒都不计；
//   - sleepEndedAt ∈ (last, now]：这一跳跨过了醒来时刻 → 从醒来那一刻开始算。
//
// 睡眠那一段本身由离开段负责补记（见 BeginAway / OnAwayTick），不在这里补。
//
// 为什么睡眠中要显式钉住 b（而不是只靠「睡眠期间定时器不跑」）：真机上进程被挂起、1 秒
// 节拍确实停摆，但进程若仍能跑（合盖前的短暂 suspend），只有第一跳会被 sleepStartedAt
// 剪到，从第二跳起它已在 last 之后、不再起剪裁作用。钉住 b 让「睡着就不算工作」在任何
// 情况下成立。
func countedSpan(last, now, sleepStartedAt, sleepEndedAt int64) (int64, int64, bool) {
	a, b := last, now
	if sleepStartedAt != 0 {
		b = min(b, sleepStartedAt)
	} else if sleepEndedAt > a && sleepEndedAt <= b {
		a = sleepEndedAt
	}
	return a, b, b > a
}

// BeginAway 判定为「离开」：把「从最后一次键鼠输入到现在」这段**从 WorkMs 搬到 AwayMs**。
//
// 为什么是「搬」而不是「扣掉」：空闲判定是轮询的，在判定为空闲之前那 N 分钟已经被按秒
// 计入了工作时长，而人其实早已离开工位。原先的实现是把这段扣掉、且扣掉之后它不进入任何
// 字段 —— 账因此不闭合（工作少了、休息没涨，凭空消失）。搬进 AwayMs 之后三个桶互补：
// `工作 + 休息 + 离开 ≈ 软件运行时长`。
//
// 三重封顶，保证只搬「确实被计入过、且属于本段」的部分：
//
//	① 区间左端不越过本工作段的起点 workSegStartAt —— 不会搬到上一段去；
//	② 每一段各自用**那一天**已计入的 WorkMs 封顶 —— 绝不为负；
//	③ **离开不跨天**：只搬「离开起点所在那天」的那部分，零点之后不再计入。
//
// sinceTs 是离开起点（最后一次键鼠输入的时刻）。返回实际搬走的毫秒数。
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
	st.awayOpen = true
	st.awayStartedAt = sinceTs
	st.awaySettledAt = now
	st.awayDayKey = dayKeyOf(from)
	st.awayCountedMs = 0
	if now <= from {
		return 0
	}
	for _, seg := range splitByDay(from, now) {
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
//   - **幂等**：重复调用只入账「还没入过的那一段」；
//   - **不跨天**：只累计「离开起点所在那天」的部分，跨过本地零点之后的时长一律不再计入
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
	for _, seg := range splitByDay(from, now) {
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
// 真实的「离开不跨天」过滤就发生在 BeginAway / OnAwayTick 里的 `seg.date != awayDayKey`
// 那一行，这里只是把同一个过滤单独暴露出来，好让断言能在不伪造系统时间的前提下验证它。
func AwayPartOf(from, to int64, day string) int64 {
	var ms int64
	for _, seg := range splitByDay(from, to) {
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
//   - **系统睡眠 / 休眠**：只算到入睡那一刻，醒来后重新开始（见 countedSpan）；
//   - 每次计入都**按本地零点切开、各记各天**（见 splitByDay）。
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
				now := st.nowFn()
				// 睡眠检测：墙钟跳变 - 唤醒 tick 跳变 > 阈值 → 这中间睡过一觉。
				// 醒来后第一跳就触发，把睡眠整段补记成离开并跳过工作记账（见 NotifySlept）。
				if haveTick && st.wakeTickFn != nil {
					if tick, ok := st.wakeTickFn(); ok {
						if detectSleep(now-last, tick-lastTick) > 0 {
							st.NotifySlept(last, now)
							last = now
							lastTick = tick
							continue
						}
						lastTick = tick
					}
				}
				from := last
				last = now
				// 休息 = 这一跳归休息：顺手推进休息计时（休息时长同样是实时入账的，
				// 否则进行中的休息只能等 break-end 才写盘，断电就整次丢掉）
				if isBreak() {
					st.OnBreakTick()
					continue
				}
				st.mu.Lock()
				a, b, ok := countedSpan(from, now, st.sleepStartedAt, st.sleepEndedAt)
				st.mu.Unlock()
				if !ok {
					continue
				}
				// 空闲（含「大跳时按当时的空闲值归属」这一条）：这一跳不计工作 ——
				// 那段由离开段负责记进 AwayMs（App 侧的监视会调 BeginAway 搬过去）
				if isIdle() {
					continue
				}
				st.mu.Lock()
				for _, seg := range splitByDay(a, b) {
					st.dayOfLocked(seg.date).WorkMs += seg.ms
				}
				st.markDirtyLocked()
				st.mu.Unlock()
			}
		}
	}()
}

// ---------- 周期区间与聚合：日 / 周 / 月 / 季 / 年 ----------

// PeriodRange 一个周期的标识与覆盖区间
type PeriodRange struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Range string `json:"range"`
	Start string `json:"start"`
	End   string `json:"end"`
}

// PeriodStats 周期汇总视图：由天记录聚合而来，字段口径与 DayStats 一致
// （去掉 date，也没有 customCounts 明细 —— 与 stats.ts 的 NUM_KEYS 一致）
type PeriodStats struct {
	Key              string `json:"key"`
	Label            string `json:"label"`
	Range            string `json:"range"`
	WorkMs           int64  `json:"workMs"`
	BreakMs          int64  `json:"breakMs"`
	AwayMs           int64  `json:"awayMs"`
	ShortMs          int64  `json:"shortMs"`
	LongMs           int64  `json:"longMs"`
	ShortCount       int64  `json:"shortCount"`
	LongCount        int64  `json:"longCount"`
	CustomCount      int64  `json:"customCount"`
	PostponeCount    int64  `json:"postponeCount"`
	PostponeMs       int64  `json:"postponeMs"`
	SkipCount        int64  `json:"skipCount"`
	SkipMs           int64  `json:"skipMs"`
	TakeoutOrdered   int64  `json:"takeoutOrdered"`
	TakeoutSkipped   int64  `json:"takeoutSkipped"`
	TakeoutPostponed int64  `json:"takeoutPostponed"`
}

// mondayOf 所在周的周一（ISO 周，周一起始，0 点）
func mondayOf(d time.Time) time.Time {
	x := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, d.Location())
	wd := int(x.Weekday()) // Sunday = 0
	if wd == 0 {
		wd = 7
	}
	return x.AddDate(0, 0, -(wd - 1))
}

// isoWeekNumber ISO-8601 周号（含 1 月 1 日的那周多为第 1 周）—— 与 stats.ts 的周四法一致
func isoWeekNumber(d time.Time) int {
	t := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, d.Location())
	// 移到那一周的周四：`(getDay()+6)%7` 把周日归零，再取到本周四
	t = t.AddDate(0, 0, -((int(t.Weekday())+6)%7)+3)
	firstThu := time.Date(t.Year(), 1, 4, 0, 0, 0, 0, d.Location())
	firstThu = firstThu.AddDate(0, 0, -((int(firstThu.Weekday())+6)%7)+3)
	weeks := math.Round(float64(t.Sub(firstThu)) / float64(7*24*time.Hour))
	return 1 + int(weeks)
}

// periodRangeOf 以 now 所在周期为基准、偏移 offset 个周期（负数=过去）的区间。
// 这是周期查询的唯一区间来源：「周期报表」（offset=0）与「历史查询」（任意 offset）共用。
//
// 注意 JS 的月份是 0-based、Go 是 1-based，偏移一律在 0-based 上算再转回来 ——
// time.Date 与 new Date 一样会正规化越界的月 / 日，所以负偏移不用特判。
func periodRangeOf(unit string, offset int, now time.Time) PeriodRange {
	y := now.Year()
	m0 := int(now.Month()) - 1
	day := now.Day()
	key := func(t time.Time) string { return t.Format("2006-01-02") }
	switch unit {
	case "day":
		// 注意 offset 加在**日**上（不是月份）—— 与 stats.ts 的 `now.getDate() + offset` 一致。
		// time.Date 与 new Date 一样会正规化越界的日（day+(-1)=0 → 上个月最后一天）
		d := time.Date(y, now.Month(), day+offset, 0, 0, 0, 0, now.Location())
		k := key(d)
		return PeriodRange{Key: k, Label: k, Range: k, Start: k, End: k}
	case "week":
		mon := mondayOf(now).AddDate(0, 0, offset*7)
		sun := mon.AddDate(0, 0, 6)
		wk := isoWeekNumber(mon)
		return PeriodRange{
			Key:   fmt.Sprintf("%d-W%s", mon.Year(), pad2(wk)),
			Label: fmt.Sprintf("第 %d 周", wk),
			Range: key(mon) + " ~ " + key(sun),
			Start: key(mon),
			End:   key(sun),
		}
	case "month":
		d := time.Date(y, time.Month(m0+offset+1), 1, 0, 0, 0, 0, now.Location())
		end := time.Date(d.Year(), d.Month()+1, 0, 0, 0, 0, 0, now.Location())
		return PeriodRange{
			Key:   fmt.Sprintf("%d-%s", d.Year(), pad2(int(d.Month()))),
			Label: fmt.Sprintf("%d 年 %d 月", d.Year(), int(d.Month())),
			Range: key(d) + " ~ " + key(end),
			Start: key(d),
			End:   key(end),
		}
	case "quarter":
		// 基准季度起始月再按 offset 平移（offset 单位是季度 = 3 个月）
		d := time.Date(y, time.Month((m0/3+offset)*3+1), 1, 0, 0, 0, 0, now.Location())
		end := time.Date(d.Year(), d.Month()+3, 0, 0, 0, 0, 0, now.Location())
		q := (int(d.Month())-1)/3 + 1
		return PeriodRange{
			Key:   fmt.Sprintf("%d-Q%d", d.Year(), q),
			Label: fmt.Sprintf("%d 年第 %d 季度", d.Year(), q),
			Range: key(d) + " ~ " + key(end),
			Start: key(d),
			End:   key(end),
		}
	}
	yy := y + offset
	return PeriodRange{
		Key:   fmt.Sprintf("%d", yy),
		Label: fmt.Sprintf("%d 年", yy),
		Range: fmt.Sprintf("%d-01-01 ~ %d-12-31", yy, yy),
		Start: fmt.Sprintf("%d-01-01", yy),
		End:   fmt.Sprintf("%d-12-31", yy),
	}
}

// aggregateRangeLocked 把 [startKey, endKey] 区间内的天记录按 numKeys 累加（调用方需持锁）
func (st *StatsStore) aggregateRangeLocked(startKey, endKey string) DayStats {
	var out DayStats
	for date, d := range st.days {
		if date < startKey || date > endKey {
			continue
		}
		for _, k := range statNumKeys {
			*numPtr(&out, k) += *numPtr(d, k)
		}
	}
	return out
}

// toPeriodLocked 区间 → 周期汇总
func (st *StatsStore) toPeriodLocked(r PeriodRange) PeriodStats {
	agg := st.aggregateRangeLocked(r.Start, r.End)
	return PeriodStats{
		Key:              r.Key,
		Label:            r.Label,
		Range:            r.Range,
		WorkMs:           agg.WorkMs,
		BreakMs:          agg.BreakMs,
		AwayMs:           agg.AwayMs,
		ShortMs:          agg.ShortMs,
		LongMs:           agg.LongMs,
		ShortCount:       agg.ShortCount,
		LongCount:        agg.LongCount,
		CustomCount:      agg.CustomCount,
		PostponeCount:    agg.PostponeCount,
		PostponeMs:       agg.PostponeMs,
		SkipCount:        agg.SkipCount,
		SkipMs:           agg.SkipMs,
		TakeoutOrdered:   agg.TakeoutOrdered,
		TakeoutSkipped:   agg.TakeoutSkipped,
		TakeoutPostponed: agg.TakeoutPostponed,
	}
}

// Periods 当前所处周期的汇总，顺序固定：周 → 月 → 季 → 年（月 ⊆ 季 ⊆ 年）
func (st *StatsStore) Periods() []PeriodStats {
	now := time.UnixMilli(st.nowFn()).Local()
	st.mu.Lock()
	defer st.mu.Unlock()
	out := make([]PeriodStats, 0, 4)
	for _, u := range []string{"week", "month", "quarter", "year"} {
		out = append(out, st.toPeriodLocked(periodRangeOf(u, 0, now)))
	}
	return out
}

// DaysInRange 历史明细：时间范围内的所有天记录（按日期升序）。start/end 缺省或非法 = 该侧不限
func (st *StatsStore) DaysInRange(start, end string) []DayStats {
	s, e := "0000-00-00", "9999-99-99"
	if start != "" && statDateRe.MatchString(start) {
		s = start
	}
	if end != "" && statDateRe.MatchString(end) {
		e = end
	}
	out := []DayStats{}
	if s > e {
		return out
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	keys := make([]string, 0, len(st.days))
	for k := range st.days {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if k >= s && k <= e {
			out = append(out, st.days[k].clone())
		}
	}
	return out
}

// ---------- 落盘 ----------

// writeFileAtomic 先写 `path.tmp` 再改名：写到一半崩溃不会留下半截 JSON
func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ---------- App 侧：落点推导与统计绑定 ----------

// statsFileForConfig 由配置文件路径推出统计文件路径（纯函数，可单测）：
//   - 便携形态（配置文件在 `<运行目录>/config/settings.json`）→ `<运行目录>/data/stats.json`；
//   - 其余形态 → 配置文件所在目录（= userData 根）下的 `stats.json`。
//
// 与 上一版的 `userDataPath('stats.json', 'data', true)` 对齐。
func statsFileForConfig(cfgPath string) string {
	if cfgPath == "" {
		return "stats.json"
	}
	dir := filepath.Dir(cfgPath)
	if strings.EqualFold(filepath.Base(dir), "config") {
		return filepath.Join(filepath.Dir(dir), "data", "stats.json")
	}
	return filepath.Join(dir, "stats.json")
}

// appStatsFile 统计文件落点。优先级：
//  1. `WR_STATS_PATH`：显式指定（冒烟 / 单测钉死落点）；
//  2. `WR_SETTINGS_PATH`：测试用它把配置隔离到临时目录，统计跟着走同一目录 ——
//     这一步保证「冒烟永远碰不到用户的真实统计文件」，不必依赖调用方记得设置。
//     刻意**优先于**便携推导：测试目录名恰好叫 config 时也不会跑到别处去；
//  3. 按当前配置文件的路径推导（见 statsFileForConfig）；
//  4. 还没找到配置文件时，按候选路径（= 唯一的 <运行目录>/config/settings.json）推导，
//     与 sound.go 同口径。
func (a *App) appStatsFile() string {
	if p := strings.TrimSpace(os.Getenv("WR_STATS_PATH")); p != "" {
		return p
	}
	if sp := strings.TrimSpace(os.Getenv("WR_SETTINGS_PATH")); sp != "" {
		return filepath.Join(filepath.Dir(sp), "stats.json")
	}
	cfgPath := ""
	if s := a.cfgSnapshot(); s != nil {
		cfgPath = s.Path()
	}
	if cfgPath == "" {
		cands := settingsCandidates(a.resourcesRoot())
		if len(cands) > 0 {
			cfgPath = cands[len(cands)-1]
		}
	}
	return statsFileForConfig(cfgPath)
}

// statsStore 取统计存储（首次访问时建好并读盘）。
//
// **读盘必须在 App 的锁之外**：Load 里要回读配置路径（appStatsFile → cfgSnapshot → a.mu），
// 持锁调用会自死锁 —— 现实路径是 idleWatchTick → statsStore → Load → appStatsFile。
func (a *App) statsStore() *StatsStore {
	a.mu.Lock()
	st := a.stats
	created := false
	if st == nil {
		st = NewStatsStore(a.appStatsFile)
		a.stats = st
		created = true
	}
	a.mu.Unlock()
	// onChange 在 App 锁之外注入：SetOnChange 内部要拿 st.mu，而 st.mu 侧有一条
	// 「Flush → appStatsFile → a.mu」的反向路径，两个方向锁序相反会留下死锁窗口。
	if created {
		// 数据一变就把今天的快照推给渲染层（统计报表「今天」卡实时刷新）。
		// 回调在 notifier goroutine 里跑、**不持任何锁**，只读一份快照再广播。
		st.SetOnChange(func() {
			app := a.app
			if app == nil {
				return
			}
			app.Event.Emit("stats:changed", st.Today())
		})
	}
	st.LoadOnce()
	return st
}

// GetStats 使用统计（今天 + 周期汇总）。
// periods 是**数组**、顺序固定 week → month → quarter → year（见 StatsStore.Periods）。
func (a *App) GetStats() map[string]any {
	st := a.statsStore()
	return map[string]any{
		"today":   st.Today(),
		"periods": st.Periods(),
	}
}

// GetHistory 历史明细（逐日记录）。days 按日期升序，customNames 是 id → 名字快照。
func (a *App) GetHistory(start, end string) map[string]any {
	st := a.statsStore()
	return map[string]any{
		"days":        st.DaysInRange(start, end),
		"customNames": st.CustomNames(),
	}
}

// ClearStats 清空全部统计数据（关于页 → 数据与隐私，渲染层已做二次确认）
func (a *App) ClearStats() map[string]any {
	a.statsStore().Clear()
	log.Printf("[stats] 已清空全部统计数据")
	return map[string]any{"ok": true}
}

// StatsProbe 统计自检（诊断页与无头冒烟用）：证明记账链路真的在跑，而不是只返回空表。
func (a *App) StatsProbe() map[string]any {
	st := a.statsStore()
	kind, elapsed := st.BreakLogInfo()
	return map[string]any{
		"file":        st.file(),
		"today":       st.Today(),
		"days":        len(st.DaysInRange("", "")),
		"autoFlush":   st.AutoFlushInfo(),
		"breakLog":    map[string]any{"type": kind, "elapsedMs": elapsed},
		"away":        st.AwayInfo(),
		"customNames": st.CustomNames(),
	}
}

// startStats 启动统计后台链路：周期落盘（1 分钟兜底）+ 每秒工作累加。
// 两条判定注入（依赖调度器与空闲状态机，stats.go 不反向依赖它们）。
func (a *App) startStats() {
	st := a.statsStore()
	st.StartAutoFlush(60_000)
	st.StartWorkAccumulator(a.statsIsBreak, a.statsIsIdle)
	st.StartNotify()
	log.Printf("[stats] 已启动（文件 %s）", st.file())
}

// stopStats 退出收尾：无条件写一次盘再停后台 goroutine
func (a *App) stopStats() {
	a.mu.Lock()
	st := a.stats
	a.mu.Unlock()
	if st == nil {
		return
	}
	st.Flush()
	st.Stop()
	log.Printf("[stats] 已落盘并停止")
}

// statsIsBreak 工作累加器的「这一跳归休息」判定：休息窗口正显示
func (a *App) statsIsBreak() bool {
	if a.sched == nil {
		return false
	}
	return a.sched.currentPhase() == "break"
}

// statsIsIdle 工作累加器的「这一跳归离开」判定：连续无输入已达阈值
// （与 idleWatchTick 同一口径；手动暂停 / 勿扰 / 应用占用都不算 —— 那时人还在用电脑）
func (a *App) statsIsIdle() bool {
	if a.sched == nil || a.idle == nil {
		return false
	}
	return a.idle.IdleSinceMs() >= int64(a.cfgSnapshot().IdlePauseMin())*60_000
}

// onBreakPostponed / onBreakSkipped 调度器宿主接口的实现（见 scheduler.go 的 schedHost）：
// 推迟 / 跳过各记一笔统计。对应 上一版的 scheduler.on('postpone' / 'skip')。
func (a *App) onBreakPostponed(kind string, postponeMs int64) {
	a.statsStore().RecordBreakPostponed(kind, postponeMs)
}

func (a *App) onBreakSkipped(kind string, skippedMs int64) {
	a.statsStore().RecordBreakSkipped(kind, skippedMs)
}
