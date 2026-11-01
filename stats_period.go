package main

import (
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

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
	// EndEarlyCount 提前结束的休息次数（只统计短 / 长休息）。注意它与休息次数**互不排斥**：
	// 休息报表的「休息次数」= 短 + 长 + 提前结束 + 自定义（休息只要开始就算一次），
	// 与今天卡「完成」（坐满整次，不含提前结束）是两个口径。
	EndEarlyCount int64 `json:"endEarlyCount"`
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
		// 最长连续工作是「取最长」而不是累加（周期汇总不展示它，这里只保证聚合结果
		// 这个字段本身不是错的）
		if d.MaxStreakMs > out.MaxStreakMs {
			out.MaxStreakMs = d.MaxStreakMs
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
		EndEarlyCount:    agg.EndEarlyCount,
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

// GetStatsFileNotice 统计文件损坏并被重建时的说明（渲染层打开设置页时来取）。
// 没有发生过（绝大多数情况）返回 nil —— 前端据此不弹窗。
func (a *App) GetStatsFileNotice() map[string]any {
	n := a.statsStore().FileNotice()
	if n == nil {
		return nil
	}
	return map[string]any{
		"file":   n.File,
		"backup": n.Backup,
		"reason": n.Reason,
		"at":     n.At,
	}
}

// AckStatsFileNotice 用户已看过「统计文件已重建」弹窗：清掉说明，本次运行不再弹。
func (a *App) AckStatsFileNotice() map[string]any {
	a.statsStore().AckFileNotice()
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
// 推迟 / 跳过各记一笔统计。对应上一版的 scheduler.on('postpone' / 'skip')。
//
// **只认短休息 / 长休息**：调度器的 NextType 也可能是自定义提醒（id 形如 cb-…），
// 手动推迟、手动跳过、暂停休息期间按「跳过」处理的自定义提醒都会走到这两个回调，
// 但「今天卡 - 休息」这一组四项（完成 / 推迟 / 跳过 / 提前结束）只统计内置短、长休息。
// 外卖走的是独立的 RecordTakeoutAction，不经过这里。
func (a *App) onBreakPostponed(kind string, postponeMs int64) {
	if kind != "short" && kind != "long" {
		return
	}
	a.statsStore().RecordBreakPostponed(kind, postponeMs)
}

func (a *App) onBreakSkipped(kind string, skippedMs int64) {
	if kind != "short" && kind != "long" {
		return
	}
	a.statsStore().RecordBreakSkipped(kind, skippedMs)
}
