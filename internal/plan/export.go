package plan

import (
	"workerrest/internal/model"
	"workerrest/internal/settings"
)

// 本文件集中导出跨包符号：main 包（调度器 / 外卖 / 提醒窗）与测试通过
// plan.Xxx 引用。内部小写函数保持原样，这里只做导出包装。

// CdAnchor 倒计时锚点（main 的 Scheduler.prevCountdownAnchorsLocked 用它做 map 值类型）。
type CdAnchor = cdAnchor

// MatchAppPause 判断进程名是否命中暂停规则。
func MatchAppPause(cfg model.AppPauseConfig, runningNames []string) (bool, []string) {
	return matchAppPause(cfg, runningNames)
}

// TakeoutMealLabel 按时刻给出餐段文案。
func TakeoutMealLabel(hour int) string { return takeoutMealLabel(hour) }

// PreBreakTitle 提前提示弹窗的标题。
func PreBreakTitle(name string) string { return preBreakTitle(name) }

// PreBreakCountdownTail 提前弹窗倒计时的后半句。
func PreBreakCountdownTail(name string) string { return preBreakCountdownTail(name) }

// OccurrenceKey 已消费 occurrence 的去重键。
func OccurrenceKey(typ string, at int64) string { return occurrenceKey(typ, at) }

// OccurrenceKeyAt 拆出 occurrenceKey 里的时刻。
func OccurrenceKeyAt(key string) int64 { return occurrenceKeyAt(key) }

// NextSlotAt 下一次触发的时刻。
func NextSlotAt(slots []model.TimeSlot, from int64) (SlotHit, bool) {
	return nextSlotAt(slots, from)
}

// SortPlans 按时刻升序排列。
func SortPlans(pool []model.PlannedBreak) { sortPlans(pool) }

// SchedulerLines 调度器的全部挂起线。
func SchedulerLines(s *settings.Settings, now int64, shortsSinceLong int, skip map[string]bool, prev map[string]CdAnchor) []model.PlannedBreak {
	return schedulerLines(s, now, shortsSinceLong, skip, prev)
}

// CountdownAnchor 倒计时线的锚点。
func CountdownAnchor(prev map[string]CdAnchor, typ string, now int64, skip map[string]bool, fresh, curIv int64) int64 {
	return countdownAnchor(prev, typ, now, skip, fresh, curIv)
}

// PlanNextBreak 算出下一次该休息的时刻。
func PlanNextBreak(s *settings.Settings, now int64, shortsSinceLong int, skip map[string]bool, prev map[string]CdAnchor) (model.PlannedBreak, bool) {
	return planNextBreak(s, now, shortsSinceLong, skip, prev)
}

// DescribePlan 计划的中文摘要。
func DescribePlan(p *model.PlannedBreak) string { return describePlan(p) }

// PlanNextBreakSplit 按设置页状态卡的口径把下一跳分成两组。
func PlanNextBreakSplit(s *settings.Settings, now int64, shortsSinceLong int, skip map[string]bool, prev map[string]CdAnchor) model.PlanSplit {
	return planNextBreakSplit(s, now, shortsSinceLong, skip, prev)
}

// FixedNextMap 各固定时刻线身份下一次实际触发的时刻表。
func FixedNextMap(s *settings.Settings, now int64, skip map[string]bool) map[string]int64 {
	return fixedNextMap(s, now, skip)
}

// SameLocalDay 两个时间戳是否落在本地的同一天。
func SameLocalDay(a, b int64) bool { return sameLocalDay(a, b) }

// SlotAlertAt 提前量下“实际弹出提醒的时刻”。
func SlotAlertAt(slot model.TimeSlot, leadMs, dayMs int64) int64 {
	return slotAlertAt(slot, leadMs, dayMs)
}

// SlotOccurrenceAt 提前线所属的 occurrence 时刻。
func SlotOccurrenceAt(slot model.TimeSlot, leadMs, nowMs int64) int64 {
	return slotOccurrenceAt(slot, leadMs, nowMs)
}

// IsTimeSlotDue 这一刻该条目是否到期。
func IsTimeSlotDue(slot model.TimeSlot, nowMs, leadMs int64) bool {
	return isTimeSlotDue(slot, nowMs, leadMs)
}

// FindDueTimeSlots 找出此刻所有到期的条目。
func FindDueTimeSlots(slots []model.TimeSlot, nowMs int64, fired map[string]bool, leadMs int64) []model.TimeSlot {
	return findDueTimeSlots(slots, nowMs, fired, leadMs)
}

// ExpandBuiltinCountdown 内置短/长倒计时链。
func ExpandBuiltinCountdown(s *settings.Settings, now int64, shortsSinceLong int, skip map[string]bool, first *model.PlannedBreak) []model.PlannedBreak {
	return expandBuiltinCountdown(s, now, shortsSinceLong, skip, first)
}

// TodayTimeline 今日提醒时间线。
func TodayTimeline(s *settings.Settings, now int64, opts TimelineOptions) []model.TimelineItem {
	return todayTimeline(s, now, opts)
}

// FmtClock 日志用的 HH:MM。
func FmtClock(ms int64) string { return fmtClock(ms) }
