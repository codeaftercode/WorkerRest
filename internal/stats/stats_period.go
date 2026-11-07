package stats

import (
	"fmt"
	"math"
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
	// 休息报表的“休息次数”= 短 + 长 + 提前结束 + 自定义（休息只要开始就算一次），
	// 与今天卡“完成”（坐满整次，不含提前结束）是两个口径。
	EndEarlyCount int64 `json:"endEarlyCount"`
}

// MondayOf 所在周的周一（ISO 周，周一起始，0 点）
func MondayOf(d time.Time) time.Time {
	x := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, d.Location())
	wd := int(x.Weekday()) // Sunday = 0
	if wd == 0 {
		wd = 7
	}
	return x.AddDate(0, 0, -(wd - 1))
}

// ISOWeekNumber ISO-8601 周号（含 1 月 1 日的那周多为第 1 周）—— 与 stats.ts 的周四法一致
func ISOWeekNumber(d time.Time) int {
	t := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, d.Location())
	// 移到那一周的周四：`(getDay()+6)%7` 把周日归零，再取到本周四
	t = t.AddDate(0, 0, -((int(t.Weekday())+6)%7)+3)
	firstThu := time.Date(t.Year(), 1, 4, 0, 0, 0, 0, d.Location())
	firstThu = firstThu.AddDate(0, 0, -((int(firstThu.Weekday())+6)%7)+3)
	weeks := math.Round(float64(t.Sub(firstThu)) / float64(7*24*time.Hour))
	return 1 + int(weeks)
}

// PeriodRangeOf 以 now 所在周期为基准、偏移 offset 个周期（负数=过去）的区间。
// 这是周期查询的唯一区间来源：“周期报表”（offset=0）与“历史查询”（任意 offset）共用。
//
// 注意 JS 的月份是 0-based、Go 是 1-based，偏移一律在 0-based 上算再转回来 ——
// time.Date 与 new Date 一样会正规化越界的月 / 日，所以负偏移不用特判。
func PeriodRangeOf(unit string, offset int, now time.Time) PeriodRange {
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
		mon := MondayOf(now).AddDate(0, 0, offset*7)
		sun := mon.AddDate(0, 0, 6)
		wk := ISOWeekNumber(mon)
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
		for _, k := range StatNumKeys {
			*NumPtr(&out, k) += *NumPtr(d, k)
		}
		// 最长连续工作是“取最长”而不是累加（周期汇总不展示它，这里只保证聚合结果
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
		out = append(out, st.toPeriodLocked(PeriodRangeOf(u, 0, now)))
	}
	return out
}

// DaysInRange 历史明细：时间范围内的所有天记录（按日期升序）。start/end 缺省或非法 = 该侧不限
func (st *StatsStore) DaysInRange(start, end string) []DayStats {
	s, e := "0000-00-00", "9999-99-99"
	if start != "" && StatDateRe.MatchString(start) {
		s = start
	}
	if end != "" && StatDateRe.MatchString(end) {
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

// StatsFileForConfig 由配置文件路径推出统计文件路径（纯函数，可单测）：
//   - 便携形态（配置文件在 `<运行目录>/config/settings.json`）→ `<运行目录>/data/stats.json`；
//   - 其余形态 → 配置文件所在目录（= userData 根）下的 `stats.json`。
//
// 与 上一版的 `userDataPath('stats.json', 'data', true)` 对齐。
func StatsFileForConfig(cfgPath string) string {
	if cfgPath == "" {
		return "stats.json"
	}
	dir := filepath.Dir(cfgPath)
	if strings.EqualFold(filepath.Base(dir), "config") {
		return filepath.Join(filepath.Dir(dir), "data", "stats.json")
	}
	return filepath.Join(dir, "stats.json")
}
