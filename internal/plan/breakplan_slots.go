package plan

import (
	"strconv"
	"strings"
	"time"
	"workerrest/internal/model"
)

// ---------- 固定时刻（fixedTime.ts） ----------

// pad2 两位补零（与 fixedTime.ts 的 pad2 同义）
func pad2(n int) string {
	if n < 10 {
		if n < 0 {
			return strconv.Itoa(n)
		}
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}

// slotTimeText 时刻文本 HH:MM（界面与日志共用）
func slotTimeText(s model.TimeSlot) string {
	return pad2(s.Hour) + ":" + pad2(s.Minute)
}

// isoWeekday 与 types.ts 的 isoWeekday 同义：1=周一 … 7=周日
// （Go 的 time.Weekday 与 JS 的 getDay() 同为 0=周日 … 6=周六，换算一致）
func isoWeekday(t time.Time) int {
	w := int(t.Weekday())
	if w == 0 {
		return 7
	}
	return w
}

// localDateKey YYYY-MM-DD（本地时区，与 JS 的 `${y}-${pad2(m)}-${pad2(d)}` 同义）
func localDateKey(t time.Time) string {
	return strconv.Itoa(t.Year()) + "-" + pad2(int(t.Month())) + "-" + pad2(t.Day())
}

// startOfDay 本地零点
func startOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// sameLocalDay 两个时间戳是否落在本地的同一天
func sameLocalDay(a, b int64) bool {
	ta, tb := time.UnixMilli(a), time.UnixMilli(b)
	return ta.Year() == tb.Year() && ta.Month() == tb.Month() && ta.Day() == tb.Day()
}

// slotAlertAt 提前量下“实际弹出提醒的时刻”：条目时刻减去提前量（跨天时自动落到前一天）
func slotAlertAt(slot model.TimeSlot, leadMs, dayMs int64) int64 {
	d := time.UnixMilli(dayMs)
	at := time.Date(d.Year(), d.Month(), d.Day(), slot.Hour, slot.Minute, 0, 0, d.Location())
	return at.UnixMilli() - leadMs
}

// slotOccurrenceAt 提前线所属的 occurrence 时刻（提醒**真正发生**的那一次）。
//
// 提前量会把“进入提前区间”的那一刻推到前一天（00:10 的提醒提前 20 分钟 → 23:50 就要
// 弹提前通知），但 occurrence 仍归属 00:10 那一次。基准取 now+leadMs 再构造当天时刻，
// 跨午夜时自然落到次日；直接用 slotAlertAt(slot, lead, now) 会得到“前一天 00:10”，
// 与次日同一条目的去重键相撞（提前通知被吞），预通知窗的倒计时也会指向过去（进窗即 0 秒）。
func slotOccurrenceAt(slot model.TimeSlot, leadMs, nowMs int64) int64 {
	return slotAlertAt(slot, 0, nowMs+leadMs)
}

// slotFireKey 触发去重键：同一条目在同一天同一时刻（精确到分钟）只会触发一次
func slotFireKey(slot model.TimeSlot, nowMs, leadMs int64) string {
	t := time.UnixMilli(nowMs + leadMs)
	return slot.ID + "#" + localDateKey(t) + "#" + slotTimeText(slot)
}

// isTimeSlotDue 这一刻该条目是否到期：启用中、时刻吻合（含提前量）、周期吻合
func isTimeSlotDue(slot model.TimeSlot, nowMs, leadMs int64) bool {
	if !slot.Enabled {
		return false
	}
	t := time.UnixMilli(nowMs + leadMs)
	if slot.Hour != t.Hour() || slot.Minute != t.Minute() {
		return false
	}
	if slot.Repeat == "weekly" && !intIn(slot.Days, isoWeekday(t)) {
		return false
	}
	return true
}

// findDueTimeSlots 找出给定的时刻表中此刻所有到期的条目，并把它们记入已触发集合
// （重复调用不会重复返回同一个时刻）。fired 跨 tick 复用同一个 map。
func findDueTimeSlots(slots []model.TimeSlot, nowMs int64, fired map[string]bool, leadMs int64) []model.TimeSlot {
	var out []model.TimeSlot
	for _, slot := range slots {
		if !isTimeSlotDue(slot, nowMs, leadMs) {
			continue
		}
		key := slotFireKey(slot, nowMs, leadMs)
		if fired[key] {
			continue
		}
		fired[key] = true
		out = append(out, slot)
	}
	pruneFiredSlotKeys(fired, nowMs+leadMs)
	return out
}

// pruneFiredSlotKeys 只保留“当前这一天”的键：跨天后旧键没有意义，长期运行也不会把集合撑大
func pruneFiredSlotKeys(fired map[string]bool, nowMs int64) {
	today := "#" + localDateKey(time.UnixMilli(nowMs)) + "#"
	for k := range fired {
		if !strings.Contains(k, today) {
			delete(fired, k)
		}
	}
}

// describeTimeSlot 条目描述：界面与日志共用的中文摘要（如“每天 12:00”“每周 一、三 09:30”）
func describeTimeSlot(slot model.TimeSlot) string {
	txt := slotTimeText(slot)
	if slot.Repeat != "weekly" {
		return "每天 " + txt
	}
	var names []string
	for _, d := range weekdays {
		if intIn(slot.Days, d.Value) {
			names = append(names, d.Short)
		}
	}
	joined := strings.Join(names, "、")
	if joined == "" {
		joined = "（未选）"
	}
	return "每周 " + joined + " " + txt
}
