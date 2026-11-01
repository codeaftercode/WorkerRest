package main

import (
	"path/filepath"
	"testing"
	"time"

	"workerrest/internal/stats"
)

// 四项（完成 / 推迟 / 跳过 / 提前结束）只统计短休息、长休息：自定义提醒的推迟与跳过
// 绝不能混进来（外卖走独立的 RecordTakeoutAction，不在同一组口径里）。
//
// 这里只钉 App 宿主回调的**过滤**：onBreakPostponed / onBreakSkipped 对 cb- 自定义提醒
// 直接 return，不落统计。记账本身（RecordBreakPostponed / RecordBreakSkipped 的数值）
// 由 internal/stats 包内的白盒测试覆盖。
func TestBreakStatsIgnoreCustomReminders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stats.json")
	st := stats.NewStatsStore(func() string { return path })
	defer st.Stop()

	a := &App{stats: st}
	// 自定义提醒：推迟 / 跳过都调了宿主回调，但一条都不该记
	a.onBreakPostponed("cb-water", 5*60_000)
	a.onBreakSkipped("cb-water", 5*60_000)

	today := time.Now().Format("2006-01-02")
	days := st.DaysInRange(today, today)
	if len(days) == 0 {
		// 自定义提醒没记账 → 今天还没有任何条目，正确
		return
	}
	if d := days[0]; d.PostponeCount != 0 || d.SkipCount != 0 {
		t.Fatalf("自定义提醒不该计入推迟 / 跳过：%+v", d)
	}

	// 内置短 / 长休息：照常记
	a.onBreakPostponed("short", 5*60_000)
	a.onBreakSkipped("long", 2*60_000)
	days = st.DaysInRange(today, today)
	if len(days) == 0 {
		t.Fatal("内置短/长休息应记入今天的统计")
	}
	d := days[0]
	if d.PostponeCount != 1 || d.PostponeMs != 5*60_000 {
		t.Fatalf("短休息推迟应记 1 次 / 5 分钟：%+v", d)
	}
	if d.SkipCount != 1 || d.SkipMs != 2*60_000 {
		t.Fatalf("长休息跳过应记 1 次 / 2 分钟：%+v", d)
	}
	if d.CustomCount != 0 {
		t.Fatalf("推迟 / 跳过不该改自定义完成次数：%+v", d)
	}
}
