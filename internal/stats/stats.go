// Package stats 是使用统计：把“休息/跳过/推迟/离开”这些事件记进 data/stats.json，
// 再按今日与区间算出报表（完成率、次数、连续记录）。
//
// 三条口径容易踩错，都写在这里：只统计内置的短休/长休（自定义与外卖不计入完成），
// 提前结束不算完成，报表里的次数不等于“今天完成”。文件损坏时不动原文件、改名备份后
// 从空表开始。
package stats

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"workerrest/internal/jsonx"
	"workerrest/internal/winpower"
)

// nowMs 当前毫秒时间戳（包内自足：不依赖 main 包）
func nowMs() int64 { return time.Now().UnixMilli() }

// pad2 两位补零（与 breakplan 域的 pad2 同义；stats 下沉后独立自足，不复用 main 包）
func pad2(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}

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
	// ShortMs 实际短休息时长（毫秒）：休息报表堆叠柱的“短休息”层
	ShortMs int64 `json:"shortMs"`
	// LongMs 实际长休息时长（毫秒）：休息报表堆叠柱的“长休息”层
	LongMs int64 `json:"longMs"`
	// ShortCount 完成的短休息次数
	ShortCount int64 `json:"shortCount"`
	// LongCount 完成的长休息次数
	LongCount int64 `json:"longCount"`
	// CustomCount 完成的自定义提醒次数（与短/长分开记，免得混进“短休息 N 次”里）
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
	// TakeoutSkipped 取消点外卖次数（窗口上点“不点了”）
	TakeoutSkipped int64 `json:"takeoutSkipped"`
	// TakeoutPostponed 推迟点外卖次数
	TakeoutPostponed int64 `json:"takeoutPostponed"`
	// MaxStreakMs 今日最长的一段“连续工作时长”（毫秒）：“今天卡”久坐面板用。
	// 与 WorkMs 的关系是“max 不是 sum”—— 它不进 StatNumKeys，周期汇总不做累加
	//（跨天聚合取 max 没有业务意义，所以 PeriodStats 里也不带这个字段）。
	MaxStreakMs int64 `json:"maxStreakMs"`
	// WorkSegments 今日各段连续工作时长（毫秒）。**最后一项是正在进行中的那段**，每跳实时
	// 更新；被休息 / 离开 / 睡眠打断后定稿，下一次工作再追加新的一项。
	// 久坐面板的“平均 / 中位数”靠它算 —— 中位数必须有分布，只有 max 是算不出来的。
	WorkSegments []int64 `json:"workSegments,omitempty"`
	// EndEarlyCount 提前结束休息的次数：**只统计短休息 / 长休息**，不含自定义提醒与外卖。
	// 与两个口径同时相关，别混：
	//   - 今天卡“完成”= 坐满整次的，**不含**提前结束（见 OnBreakEnd 的 early 分支）；
	//   - 休息报表“休息次数”= 休息只要开始过就算一次，**包含**提前结束（前端再相加）。
	EndEarlyCount int64 `json:"endEarlyCount"`
}

// StatNumKeys 与 stats.ts 的 NUM_KEYS 同序：聚合、归一化、周期汇总都按这份清单走。
// 注意 shortMs / longMs 也在里面 —— 周期汇总的输出里确实带这两个字段。
var StatNumKeys = []string{
	"workMs", "breakMs", "awayMs", "shortMs", "longMs", "shortCount", "longCount", "customCount",
	"postponeCount", "postponeMs", "skipCount", "skipMs", "endEarlyCount",
	"takeoutOrdered", "takeoutSkipped", "takeoutPostponed",
}

// maxSegsPerDay 一天最多记多少段连续工作：正常一天几十段，这个上限只是防御 ——
// 病态数据 / 手改文件也不至于把 stats.json 撑成无限大。
const maxSegsPerDay = 500

// NumPtr 取某个数值字段的地址（nil = 未知键）
func NumPtr(d *DayStats, key string) *int64 {
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
	case "endEarlyCount":
		return &d.EndEarlyCount
	case "takeoutOrdered":
		return &d.TakeoutOrdered
	case "takeoutSkipped":
		return &d.TakeoutSkipped
	case "takeoutPostponed":
		return &d.TakeoutPostponed
	}
	return nil
}

// StatDateRe 合法日期键（与 stats.ts 的 /^\d{4}-\d{2}-\d{2}$/ 同口径）
var StatDateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

func EmptyDay(date string) DayStats {
	return DayStats{Date: date, CustomCounts: map[string]int64{}}
}

// clone 深拷贝（CustomCounts 是 map、WorkSegments 是切片，调用方会在锁外读它）
func (d DayStats) clone() DayStats {
	out := d
	out.CustomCounts = make(map[string]int64, len(d.CustomCounts))
	for k, v := range d.CustomCounts {
		out.CustomCounts[k] = v
	}
	if d.WorkSegments != nil {
		out.WorkSegments = append([]int64{}, d.WorkSegments...)
	}
	return out
}

// NormalizeDay 把磁盘上任何形态的条目夹紧成合法值（旧版本数据 / 手改都不会炸）
func NormalizeDay(raw any, date string) DayStats {
	o, _ := raw.(map[string]any)
	out := EmptyDay(date)
	for _, k := range StatNumKeys {
		v := NumFromAny(o[k])
		if v > 0 {
			*NumPtr(&out, k) = int64(math.Round(v))
		}
	}
	// 按 id 分的提醒计数：只认 cb- 前缀键与正整数，其它形态一概丢弃
	if cc, ok := o["customCounts"].(map[string]any); ok {
		for id, rv := range cc {
			if !strings.HasPrefix(id, "cb-") {
				continue
			}
			if v := NumFromAny(rv); v > 0 {
				out.CustomCounts[id] = int64(math.Round(v))
			}
		}
	}
	// maxStreakMs / workSegments 不在 StatNumKeys 里（它们不参与累加聚合：
	// 最长段是取 max、段数组是分布本身），逐个单独读回
	if v := NumFromAny(o["maxStreakMs"]); v > 0 {
		out.MaxStreakMs = int64(math.Round(v))
	}
	if arr, ok := o["workSegments"].([]any); ok {
		// 只认正数且限量读入（手改文件塞几十万项进来也不会把内存撑爆）
		for _, it := range arr {
			if len(out.WorkSegments) >= maxSegsPerDay {
				break
			}
			if v := NumFromAny(it); v > 0 {
				out.WorkSegments = append(out.WorkSegments, int64(math.Round(v)))
			}
		}
	}
	return out
}

// NumFromAny 宽容地取数字：非数字 / 非有限值一律当 0
func NumFromAny(v any) float64 {
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

// ---------- 本地日期工具（与 stats.ts 的 dayKey / DayStartTs / SplitByDay 同口径） ----------

// DayKeyOf 某个时刻所在**本地**日期的 YYYY-MM-DD
func DayKeyOf(ts int64) string {
	return time.UnixMilli(ts).Local().Format("2006-01-02")
}

// DayStartTs 某个时刻所在本地日期的零点（毫秒时间戳）
func DayStartTs(ts int64) int64 {
	d := time.UnixMilli(ts).Local()
	return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.Local).UnixMilli()
}

// daySeg 一段属于某个本地日期的时长
type daySeg struct {
	date string
	ms   int64
}

// SplitByDay 把一段时间 `[from, to)` 按本地零点切成若干段，每段带自己所属的本地日期。
//
// 跨零点时返回多段：23:58 → 次日 00:03 切成“前一天 2 分钟 + 当天 3 分钟”，各记各天的账。
// 正常 1 秒一跳的累加只会得到单段，只有跨零点那一跳才真正切开；休息跨零点（含睡眠跨零点）
// 则可能一次切出多段。
//
// `+26 小时` 再取零点 = 次日的零点：夏令时下当天可能是 23 或 25 小时，26 小时一定越过次日
// 零点而不会跳到后天（中国无夏令时，这里只是不把逻辑写死）。
func SplitByDay(from, to int64) []daySeg {
	out := []daySeg{}
	cur := from
	// 防御：时间戳逆序或异常大跨度时不要转不出去（正常最多切 2~3 段）
	for guard := 0; cur < to && guard < 1000; guard++ {
		end := to
		if next := DayStartTs(DayStartTs(cur) + 26*3600_000); next < end {
			end = next
		}
		if ms := end - cur; ms > 0 {
			out = append(out, daySeg{date: DayKeyOf(cur), ms: ms})
		}
		cur = end
	}
	return out
}

// ---------- 统计存储 ----------

// StatsFileNotice “统计文件损坏，已备份并重建空表”的说明（给渲染层弹窗用）。
//
// 只在**这次启动**里存在一份：Load 判定文件读取 / 解析失败时产生，用户点掉弹窗后由
// AckStatsFileNotice 清掉。它是“已经发生的处置”的告知，不是错误状态，不影响记账。
type StatsFileNotice struct {
	// File 统计文件路径（重建后的空表位置）
	File string `json:"file"`
	// Backup 损坏文件被改名到的位置（原始数据还在这里，可人工抢救）
	Backup string `json:"backup"`
	// Reason 判定为损坏的直接原因（解析报错原文 / 结构不对）
	Reason string `json:"reason"`
	// At 处置时刻（本地时间 YYYY-MM-DD HH:MM:SS）
	At string `json:"at"`
}

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
	// streakStartMs 当前“连续工作段”的起点（0 = 当前没有连续工作在进行）。
	// 只在计时 goroutine 判为“这一跳算工作”时起算，被休息 / 空闲 / 睡眠 / 离开打断时清零。
	// 每跳把它到现在的长度与当天 MaxStreakMs 取最长（不累加），所以跨天也各归各天。
	streakStartMs int64
	// streakDayKey 进行中那段在 WorkSegments 里已经占位的那一天（空 = 还没占位）。
	// 跨零点时会从“更新末项”切换成“在新的一天追加一项”，靠它判断。
	streakDayKey string
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
	// autoFlushDone 自动落盘 goroutine 的退出信号：Stop 要等它真的退出。
	// 不等的话，Stop 返回时可能正有一次 Flush 在写盘 —— Flush 是先清脏标记再写文件，
	// 于是“已经停了却还在写”。退出清理（以及测试的 TempDir 清理）会撞上这一笔写：
	// 目录刚删掉，文件又被建回来（2026-11-08 本机连跑三次都红：TempDir RemoveAll:
	// directory is not empty）。
	autoFlushDone chan struct{}

	// notifyCh “统计数据已变”的合并通知通道（容量 1）：工作时长每秒都在涨，
	// 连续的多次改动合并成一次通知 —— 渲染层只需要知道“后来变了”，中间态没意义。
	notifyCh chan struct{}
	// onChange 由 App 注入：把“已变”转成发给渲染层的事件（见 StartNotify）。
	// 在独立的 notifier goroutine 里调用，调用时不持 mu，可以放心再读 Today()。
	onChange func()

	// loadOnce 让“首次读盘”只发生一次：App.statsStore 会在**不持自己的锁**的情况下调
	// LoadOnce —— Load 里要回读配置路径，而那条路径需要 App 的锁，持锁调用会自死锁
	//（本项目踩过一次：idleWatchTick → statsStore → Load → appStatsFile → cfgSnapshot）。
	loadOnce sync.Once

	// loadNotice 本次启动里“文件损坏 → 已备份 → 重建空表”的说明（nil = 没发生过）。
	// 用户点掉弹窗后由 AckFileNotice 清掉；只在 Load 里写、在锁内读写。
	loadNotice *StatsFileNotice
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
		wakeTickFn:     winpower.SysWakeTick,
		stopCh:         make(chan struct{}),
		notifyCh:       make(chan struct{}, 1),
	}
}

// Load 读统计文件，并把落点修成“一定有一份可用的空表”。四情形：
//
//	① 文件存在且能解析 → 正常读入（走 NormalizeDay，旧数据 / 手改都不会炸）；
//	② 文件不存在（新装 / 用户删了 data 目录）→ **建目录 + 写一份空表**，
//	   所以打包时不再预置 data/stats.json，首跑自己长出来；
//	③ 文件存在但**读不出来**（权限 / 被占用 / 路径是个目录）：不冒险动用户的文件，
//	   只从空表开始用，下次能读时数据还在；
//	④ 文件存在但**不是合法 JSON 对象**（截断、乱码、手改坏）：把原文件改名成
//	   `stats_<日期_时间>.json` 留在同目录（原始数据不销毁），再重建空表，并记一条
//	   StatsFileNotice 让界面弹窗说明——静默重置等于用户的历史凭空消失。
//
// 统计数据全部永久保留，这里不裁剪历史条目。
func (st *StatsStore) Load() {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.days = map[string]*DayStats{}
	st.customNames = map[string]string{}
	st.loadNotice = nil
	path := st.File()
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		log.Printf("[stats] 数据目录创建失败（%v），本次统计只在内存里", err)
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			st.createEmptyLocked(path)
			return
		}
		log.Printf("[stats] 统计文件读取失败（%v），本次从空表开始（原文件保持不动）", err)
		return
	}
	var top map[string]any
	perr := json.Unmarshal(raw, &top)
	reason := ""
	if perr != nil {
		reason = perr.Error()
	} else if top == nil {
		// 合法 JSON 但不是对象（`null` / 空文件解析成 nil）：结构上不可能装下天记录
		reason = "文件内容不是一个 JSON 对象"
	}
	if reason != "" {
		backup, berr := RenameBrokenStatsFile(path)
		if berr != nil {
			log.Printf("[stats] 统计文件损坏（%s），且改名保留失败（%v）：直接重建空表", reason, berr)
			backup = ""
		} else {
			log.Printf("[stats] 统计文件损坏（%s），已备份为 %s 并重建空表", reason, backup)
		}
		st.createEmptyLocked(path)
		st.loadNotice = &StatsFileNotice{
			File:   path,
			Backup: backup,
			Reason: reason,
			At:     time.Now().Format("2006-01-02 15:04:05"),
		}
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
		if !StatDateRe.MatchString(date) {
			continue
		}
		d := NormalizeDay(v, date)
		st.days[date] = &d
	}
}

// createEmptyLocked 把内存里的空表写到 path（调用方需持锁）。
// 内容与 marshalLocked 同源，所以“打包预置的空表”与“首跑自建的空表”字节等价。
func (st *StatsStore) createEmptyLocked(path string) {
	text, err := st.marshalLocked()
	if err != nil {
		log.Printf("[stats] 空统计表序列化失败: %v", err)
		return
	}
	if err := jsonx.WriteFileAtomic(path, text); err != nil {
		log.Printf("[stats] 空统计表写入失败（%v），本次统计只在内存里", err)
		return
	}
	log.Printf("[stats] 空统计表已就位：%s", path)
}

// BrokenStatsBackupName 损坏文件的备份路径：同目录 `stats_20261028_164510.json`。
// 同一秒里反复触发（手动换文件连点两次）时依次退让成 `-2` / `-3`，绝不覆盖已有备份。
func BrokenStatsBackupName(dir, base, ext, stamp string, n int) string {
	name := base + "_" + stamp
	if n > 1 {
		name += "-" + strconv.Itoa(n)
	}
	return filepath.Join(dir, name+ext)
}

// RenameBrokenStatsFile 把损坏的统计文件改名保留（不删除：里面可能有用户手改过的数据），
// 返回备份后的完整路径。
func RenameBrokenStatsFile(path string) (string, error) {
	dir, name := filepath.Split(path)
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	stamp := time.Now().Format("20060102_150405")
	for n := 1; n <= 100; n++ {
		cand := BrokenStatsBackupName(dir, base, ext, stamp, n)
		if _, err := os.Stat(cand); err == nil {
			continue // 已存在（同秒重名），换下一个后缀
		}
		if err := os.Rename(path, cand); err != nil {
			return "", err
		}
		return cand, nil
	}
	return "", fmt.Errorf("同一秒内备份文件过多，放弃改名")
}

// FileNotice 本次启动的“文件损坏已备份重建”说明（没有则 nil）
func (st *StatsStore) FileNotice() *StatsFileNotice {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.loadNotice == nil {
		return nil
	}
	n := *st.loadNotice
	return &n
}

// AckFileNotice 用户已看过弹窗：清掉说明（同一进程里不再弹）
func (st *StatsStore) AckFileNotice() {
	st.mu.Lock()
	st.loadNotice = nil
	st.mu.Unlock()
}
