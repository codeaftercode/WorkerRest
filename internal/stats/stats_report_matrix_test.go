package stats

// 统计报表的**口径组合**矩阵（2026-11-13）。
//
// 报表页的三张图都建立在同一份日记录上：完成 / 提前结束 / 跳过 / 推迟 / 自定义 五项各自
// 独立累加，周期汇总只是按日期区间把它们加起来。单点用例已经钉过“各个入口记什么”，
// 这里补的是**叠加与边界**：
//
//	· 同一天里同时发生推迟 + 跳过 + 提前结束 + 自定义，五项互不串味；
//	· 长休息接管短休息：短休息记一次“完成”但不记时长（时长已算在长休息里）；
//	· 月末 / 季末 / 年末的最后一班算在哪一侧；
//	· 一次休息跨零点：时长按零点切开、次数只记在结束那天；
//	· 最长连续工作按“取最大”聚合，不是累加；
//	· 周区间以周一为首日（周日属于上一周）；
//	· Periods() 与 DaysInRange() 区间一致（报表页与历史明细同源）。

import (
	"path/filepath"
	"testing"
	"time"
)

// matrixStore 建一个空统计库（临时目录，不碰真实 stats.json）
func matrixStore(t *testing.T, now time.Time) *StatsStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stats.json")
	st := NewStatsStore(func() string { return path })
	st.Load()
	t.Cleanup(st.Stop)
	st.nowFn = func() int64 { return now.UnixMilli() }
	return st
}

// at 把“现在”挪到指定时刻执行 fn（记账入口全靠 nowFn 判断属于哪一天）
func at(st *StatsStore, y int, mo time.Month, d, hh, mm int, fn func()) {
	prev := st.nowFn
	at := time.Date(y, mo, d, hh, mm, 0, 0, time.Local)
	st.nowFn = func() int64 { return at.UnixMilli() }
	fn()
	st.nowFn = prev
}

// dayOf 取某天的记录（不存在就是零值副本）
func dayOf(st *StatsStore, key string) DayStats {
	st.mu.Lock()
	defer st.mu.Unlock()
	if d := st.days[key]; d != nil {
		return d.clone()
	}
	return EmptyDay(key)
}

// ---------- 同一天里的五项叠加 ----------

// 推迟 / 跳过 / 提前结束 / 完成 / 自定义：五项各自独立，互不串味。
func TestReportMatrixFiveCountersIndependent(t *testing.T) {
	now := time.Date(2026, 11, 13, 10, 0, 0, 0, time.Local)
	st := matrixStore(t, now)
	day := "2026-11-13"

	// 完成一次短休息（20 秒）
	st.OnBreakStart("short")
	st.OnBreakTick()
	at(st, 2026, 11, 13, 10, 1, func() { st.OnBreakEnd("", false) })
	// 三次推迟（1 / 2 / 3 分钟）+ 两次跳过（20 秒 / 5 分钟）
	st.RecordBreakPostponed("short", 60_000)
	st.RecordBreakPostponed("short", 120_000)
	st.RecordBreakPostponed("long", 180_000)
	st.RecordBreakSkipped("short", 20_000)
	st.RecordBreakSkipped("long", 300_000)
	// 一次提前结束（长休息）
	at(st, 2026, 11, 13, 10, 5, func() {
		st.OnBreakStart("long")
		st.OnBreakTick()
	})
	at(st, 2026, 11, 13, 10, 7, func() { st.OnBreakEnd("", true) })
	// 一次自定义提醒
	at(st, 2026, 11, 13, 10, 10, func() {
		st.OnBreakStart("cb-water")
		st.OnBreakTick()
	})
	at(st, 2026, 11, 13, 10, 11, func() { st.OnBreakEnd("喝水提醒", false) })

	d := dayOf(st, day)
	if d.ShortCount != 1 {
		t.Fatalf("完成短休息应为 1，实际 %d", d.ShortCount)
	}
	if d.LongCount != 0 {
		t.Fatalf("长休息是提前结束，不该进“完成”，实际 %d", d.LongCount)
	}
	if d.EndEarlyCount != 1 {
		t.Fatalf("提前结束应为 1，实际 %d", d.EndEarlyCount)
	}
	if d.CustomCount != 1 || d.CustomCounts["cb-water"] != 1 {
		t.Fatalf("自定义提醒应单独记一次，实际 count=%d map=%v", d.CustomCount, d.CustomCounts)
	}
	if d.PostponeCount != 3 || d.PostponeMs != 360_000 {
		t.Fatalf("推迟应为 3 次 / 6 分钟，实际 %d 次 / %d ms", d.PostponeCount, d.PostponeMs)
	}
	if d.SkipCount != 2 || d.SkipMs != 320_000 {
		t.Fatalf("跳过应为 2 次 / 5 分 20 秒，实际 %d 次 / %d ms", d.SkipCount, d.SkipMs)
	}
	// 报表“休息次数” = 完成 + 提前结束 + 自定义（前端按这四项相加）
	if got := d.ShortCount + d.LongCount + d.EndEarlyCount + d.CustomCount; got != 3 {
		t.Fatalf("休息次数应为 3（1 完成 + 1 提前结束 + 1 自定义），实际 %d", got)
	}
	// 名字快照要留下来（提醒被改名 / 删除后历史报表还能对上号）
	if names := st.CustomNames(); names["cb-water"] != "喝水提醒" {
		t.Fatalf("自定义提醒名字快照丢了：%v", names)
	}
}

// 长休息接管短休息：短休息记一次“完成”，但**不记时长**（那段时间已经算在长休息里）。
func TestReportMatrixCoveredShortCountsWithoutDuration(t *testing.T) {
	now := time.Date(2026, 11, 13, 11, 0, 0, 0, time.Local)
	st := matrixStore(t, now)

	// 长休息跑了 10 秒，期间短休息“到点但被覆盖”
	st.OnBreakStart("long")
	at(st, 2026, 11, 13, 11, 0, func() {})
	st.nowFn = func() int64 { return now.Add(10 * time.Second).UnixMilli() }
	st.OnBreakTick()
	st.OnBreakCounted("short")
	st.nowFn = func() int64 { return now.Add(60 * time.Second).UnixMilli() }
	st.OnBreakEnd("", false)

	d := dayOf(st, "2026-11-13")
	if d.LongCount != 1 {
		t.Fatalf("长休息应记一次完成，实际 %d", d.LongCount)
	}
	if d.ShortCount != 1 {
		t.Fatalf("被覆盖的短休息也应记一次完成，实际 %d", d.ShortCount)
	}
	if d.ShortMs != 0 {
		t.Fatalf("被覆盖的短休息不该再记时长（会与长休息重复），实际 %d ms", d.ShortMs)
	}
	if d.LongMs < 59_000 || d.LongMs > 61_000 {
		t.Fatalf("长休息时长应≈60 秒，实际 %d ms", d.LongMs)
	}
	if d.BreakMs != d.LongMs {
		t.Fatalf("休息总时长应等于长休息那段（%d），实际 %d", d.LongMs, d.BreakMs)
	}
}

// ---------- 边界：月末 / 季末 / 年末 ----------

// periodsOf 取某一周期（week/month/quarter/year, offset）的汇总
func periodsOf(st *StatsStore, unit string, offset int) PeriodStats {
	now := time.UnixMilli(st.nowFn()).Local()
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.toPeriodLocked(PeriodRangeOf(unit, offset, now))
}

// rangeOf 取某一周期的区间（Start/End 只在 PeriodRange 上）
func rangeOf(st *StatsStore, unit string, offset int) PeriodRange {
	return PeriodRangeOf(unit, offset, time.UnixMilli(st.nowFn()).Local())
}

// 月末最后一班算在当月，次月 1 日算在下月 —— 周期汇总按日期字符串切片。
func TestReportMatrixMonthBoundary(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.Local)
	st := matrixStore(t, now)
	at(st, 2026, 10, 31, 23, 0, func() { st.RecordBreakSkipped("short", 20_000) })
	at(st, 2026, 11, 1, 0, 30, func() { st.RecordBreakSkipped("short", 20_000) })

	cur := periodsOf(st, "month", 0)
	prev := periodsOf(st, "month", -1)
	if cur.SkipCount != 1 || cur.SkipMs != 20_000 {
		t.Fatalf("本月应只含 11-01 那一笔，实际 %d 次 / %d ms", cur.SkipCount, cur.SkipMs)
	}
	if prev.SkipCount != 1 || prev.SkipMs != 20_000 {
		t.Fatalf("上月应只含 10-31 那一笔，实际 %d 次 / %d ms", prev.SkipCount, prev.SkipMs)
	}
	if cur.Key != "2026-11" || prev.Key != "2026-10" {
		t.Fatalf("周期键不对：cur=%q prev=%q", cur.Key, prev.Key)
	}
}

// 季末：9-30 属 Q3，10-01 属 Q4。
func TestReportMatrixQuarterBoundary(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.Local)
	st := matrixStore(t, now)
	at(st, 2026, 9, 30, 22, 0, func() { st.RecordBreakPostponed("short", 60_000) })
	at(st, 2026, 10, 1, 9, 0, func() { st.RecordBreakPostponed("short", 120_000) })

	q4 := periodsOf(st, "quarter", 0)
	q3 := periodsOf(st, "quarter", -1)
	if q4.Key != "2026-Q4" || q3.Key != "2026-Q3" {
		t.Fatalf("季度键不对：q4=%q q3=%q", q4.Key, q3.Key)
	}
	if q4.PostponeCount != 1 || q4.PostponeMs != 120_000 {
		t.Fatalf("Q4 应只含 10-01 那一笔，实际 %d 次 / %d ms", q4.PostponeCount, q4.PostponeMs)
	}
	if q3.PostponeCount != 1 || q3.PostponeMs != 60_000 {
		t.Fatalf("Q3 应只含 09-30 那一笔，实际 %d 次 / %d ms", q3.PostponeCount, q3.PostponeMs)
	}
}

// 年末：12-31 属上一年，01-01 属新的一年（报表跨年不许串）。
func TestReportMatrixYearBoundary(t *testing.T) {
	now := time.Date(2026, 1, 5, 12, 0, 0, 0, time.Local)
	st := matrixStore(t, now)
	at(st, 2025, 12, 31, 23, 50, func() { st.RecordTakeoutAction("ordered") })
	at(st, 2026, 1, 1, 12, 0, func() { st.RecordTakeoutAction("skip") })

	cur := periodsOf(st, "year", 0)
	prev := periodsOf(st, "year", -1)
	if cur.TakeoutSkipped != 1 || cur.TakeoutOrdered != 0 {
		t.Fatalf("今年应只含 01-01 的“不点了”，实际 ordered=%d skipped=%d",
			cur.TakeoutOrdered, cur.TakeoutSkipped)
	}
	if prev.TakeoutOrdered != 1 || prev.TakeoutSkipped != 0 {
		t.Fatalf("去年应只含 12-31 的“已点好”，实际 ordered=%d skipped=%d",
			prev.TakeoutOrdered, prev.TakeoutSkipped)
	}
}

// 一次休息跨零点：时长按本地零点切两半，次数只记在**结束**那天（一次休息是一个事件）。
func TestReportMatrixBreakAcrossMidnight(t *testing.T) {
	st := matrixStore(t, time.Date(2026, 11, 13, 23, 58, 0, 0, time.Local))
	st.OnBreakStart("short") // 23:58 开始
	st.OnBreakTick()
	// 00:03 结束（跨了 5 分钟）
	st.nowFn = func() int64 {
		return time.Date(2026, 11, 14, 0, 3, 0, 0, time.Local).UnixMilli()
	}
	st.OnBreakEnd("", false)

	d1 := dayOf(st, "2026-11-13")
	d2 := dayOf(st, "2026-11-14")
	if d1.ShortMs < 119_000 || d1.ShortMs > 121_000 {
		t.Fatalf("前一天应记 2 分钟，实际 %d ms", d1.ShortMs)
	}
	if d2.ShortMs < 179_000 || d2.ShortMs > 181_000 {
		t.Fatalf("当天应记 3 分钟，实际 %d ms", d2.ShortMs)
	}
	if d1.ShortCount != 0 || d2.ShortCount != 1 {
		t.Fatalf("次数只记在结束那天：前一天 %d / 当天 %d", d1.ShortCount, d2.ShortCount)
	}
	if d1.ShortMs+d2.ShortMs < 299_000 || d1.ShortMs+d2.ShortMs > 301_000 {
		t.Fatalf("两段合计应≈5 分钟，实际 %d ms", d1.ShortMs+d2.ShortMs)
	}
}

// ---------- 聚合口径 ----------

// 最长连续工作按“取最大”聚合：两天 30 / 45 分钟，汇总必须是 45 而不是 75。
func TestReportMatrixMaxStreakTakesMax(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.Local)
	st := matrixStore(t, now)
	st.mu.Lock()
	d1 := st.dayOfLocked("2026-11-11")
	d1.MaxStreakMs = 30 * 60_000
	d2 := st.dayOfLocked("2026-11-12")
	d2.MaxStreakMs = 45 * 60_000
	st.mu.Unlock()

	week := periodsOf(st, "week", 0) // 2026-11-13 是周五 → 本周 11-09 ~ 11-15
	if week.Key != "2026-W46" {
		t.Fatalf("周键不对：%q", week.Key)
	}
	agg := func() DayStats {
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.aggregateRangeLocked("2026-11-09", "2026-11-15")
	}()
	if agg.MaxStreakMs != 45*60_000 {
		t.Fatalf("最长连续工作应取最大值 45 分钟，实际 %d ms", agg.MaxStreakMs)
	}
}

// 周区间以周一为首日：周日属于**上一周**，不是新一周的开头。
func TestReportMatrixWeekStartsMonday(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.Local) // 周五
	st := matrixStore(t, now)
	// 2026-11-08 是周日（属 11-02 ~ 11-08 那一周），2026-11-09 是周一
	at(st, 2026, 11, 8, 20, 0, func() { st.RecordBreakSkipped("short", 20_000) })
	at(st, 2026, 11, 9, 9, 0, func() { st.RecordBreakSkipped("short", 20_000) })

	cur := periodsOf(st, "week", 0)
	prev := periodsOf(st, "week", -1)
	if cur.SkipCount != 1 {
		t.Fatalf("本周应只含 11-09 那一笔，实际 %d", cur.SkipCount)
	}
	if prev.SkipCount != 1 {
		t.Fatalf("上一周应只含 11-08（周日）那一笔，实际 %d", prev.SkipCount)
	}
	if r := rangeOf(st, "week", -1); r.Start != "2026-11-02" || r.End != "2026-11-08" {
		t.Fatalf("上一周区间应是 11-02 ~ 11-08，实际 %s ~ %s", r.Start, r.End)
	}
}

// Periods() 的月度汇总必须等于同区间的逐日明细之和（报表页与历史明细同源）。
func TestReportMatrixPeriodsMatchDaysInRange(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.Local)
	st := matrixStore(t, now)
	for _, d := range []int{2, 7, 13} {
		at(st, 2026, 11, d, 10, 0, func() {
			st.RecordBreakPostponed("short", 60_000)
			st.RecordBreakSkipped("long", 300_000)
		})
	}
	month := periodsOf(st, "month", 0)
	r := rangeOf(st, "month", 0)
	days := st.DaysInRange(r.Start, r.End)
	var postpones, skips int64
	var postponeMs, skipMs int64
	for _, d := range days {
		postpones += d.PostponeCount
		postponeMs += d.PostponeMs
		skips += d.SkipCount
		skipMs += d.SkipMs
	}
	if int64(postpones) != month.PostponeCount || postponeMs != month.PostponeMs {
		t.Fatalf("推迟对不上：逐日 %d 次 / %d ms，汇总 %d 次 / %d ms",
			postpones, postponeMs, month.PostponeCount, month.PostponeMs)
	}
	if int64(skips) != month.SkipCount || skipMs != month.SkipMs {
		t.Fatalf("跳过对不上：逐日 %d 次 / %d ms，汇总 %d 次 / %d ms",
			skips, skipMs, month.SkipCount, month.SkipMs)
	}
}
