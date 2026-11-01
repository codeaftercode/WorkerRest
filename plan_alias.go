package main

import (
	"workerrest/internal/model"
	"workerrest/internal/plan"
	"workerrest/internal/settings"
)

// 本文件是 breakplan 域下沉到 internal/plan 后的 main 包过渡层。
//
// main 包（调度器 / 外卖 / 提醒窗 / 系统监听）与测试仍以旧名引用这些纯函数，
// 这里用函数转发保持零改动。类型（CdAnchor / TimelineOptions / SlotHit）与常量
// （TAKEOUT_NAME / ShortBreakName）已收敛为直接写 plan. 前缀。

// ---------- 纯函数转发（breakplan.go 域） ----------

func takeoutMealLabel(hour int) string { return plan.TakeoutMealLabel(hour) }

func preBreakTitle(name string) string { return plan.PreBreakTitle(name) }

func preBreakCountdownTail(name string) string { return plan.PreBreakCountdownTail(name) }

func matchAppPause(cfg model.AppPauseConfig, runningNames []string) (bool, []string) {
	return plan.MatchAppPause(cfg, runningNames)
}

// ---------- 下一次休息（breakplan_next.go 域） ----------

func occurrenceKey(typ string, at int64) string { return plan.OccurrenceKey(typ, at) }

func occurrenceKeyAt(key string) int64 { return plan.OccurrenceKeyAt(key) }

func nextSlotAt(slots []model.TimeSlot, from int64) (plan.SlotHit, bool) {
	return plan.NextSlotAt(slots, from)
}

func sortPlans(pool []model.PlannedBreak) { plan.SortPlans(pool) }

func schedulerLines(s *settings.Settings, now int64, shortsSinceLong int, skip map[string]bool, prev map[string]plan.CdAnchor) []model.PlannedBreak {
	return plan.SchedulerLines(s, now, shortsSinceLong, skip, prev)
}

func countdownAnchor(prev map[string]plan.CdAnchor, typ string, now int64, skip map[string]bool, fresh, curIv int64) int64 {
	return plan.CountdownAnchor(prev, typ, now, skip, fresh, curIv)
}

func planNextBreak(s *settings.Settings, now int64, shortsSinceLong int, skip map[string]bool, prev map[string]plan.CdAnchor) (model.PlannedBreak, bool) {
	return plan.PlanNextBreak(s, now, shortsSinceLong, skip, prev)
}

func describePlan(p *model.PlannedBreak) string { return plan.DescribePlan(p) }

func planNextBreakSplit(s *settings.Settings, now int64, shortsSinceLong int, skip map[string]bool, prev map[string]plan.CdAnchor) model.PlanSplit {
	return plan.PlanNextBreakSplit(s, now, shortsSinceLong, skip, prev)
}

func fixedNextMap(s *settings.Settings, now int64, skip map[string]bool) map[string]int64 {
	return plan.FixedNextMap(s, now, skip)
}

// ---------- 固定时刻（breakplan_slots.go 域） ----------

func slotAlertAt(slot model.TimeSlot, leadMs, dayMs int64) int64 {
	return plan.SlotAlertAt(slot, leadMs, dayMs)
}

func slotOccurrenceAt(slot model.TimeSlot, leadMs, nowMs int64) int64 {
	return plan.SlotOccurrenceAt(slot, leadMs, nowMs)
}

func isTimeSlotDue(slot model.TimeSlot, nowMs, leadMs int64) bool {
	return plan.IsTimeSlotDue(slot, nowMs, leadMs)
}

// ---------- 今日时间线（breakplan_timeline.go 域） ----------

func todayTimeline(s *settings.Settings, now int64, opts plan.TimelineOptions) []model.TimelineItem {
	return plan.TodayTimeline(s, now, opts)
}

func fmtClock(ms int64) string { return plan.FmtClock(ms) }
