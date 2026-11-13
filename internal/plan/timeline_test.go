package plan

// 今日时间线（时间规划页顶部列表）的回归用例：这几条都是 2026-11-13 审计时发现
// 与用户常识不符、随后修掉的场景，留在这里防止再犯。

import (
	"testing"
	"time"

	"workerrest/internal/model"
)

// 外卖被推迟后，今日时间线只列“推迟后的时刻”。曾经原定时刻与推迟时刻同时出现，
// 用户看到同一条提醒在同一天列了两遍。
func TestTakeoutTimelineShowsBothOriginalAndPostponed(t *testing.T) {
	s := cfgOf(t, `{"short":{"enabled":false},"long":{"enabled":false},
		"takeout":{"enabled":true,"leadSec":10,"slots":[{"id":"tko-lunch","hour":11,"minute":30,"repeat":"daily","days":[],"enabled":true}]}}`)
	now := at(2026, 3, 12, 11, 20) // 提前 10 分钟：预通知小窗里点了“推迟”
	items := todayTimeline(s, now, TimelineOptions{
		TakeoutPostponed: &model.TakeoutPostponed{Slot: s.TakeoutSlots()[0], DueAt: at(2026, 3, 12, 11, 40)},
	})
	var times []string
	for _, it := range items {
		if it.Type == "takeout" {
			times = append(times, time.UnixMilli(it.At).Format("15:04"))
		}
	}
	if len(times) > 1 {
		t.Fatalf("外卖推迟后时间线把同一条提醒列了两遍：%v（应只留推迟后的 11:40）", times)
	}
}

// 每周重复但一个星期都没勾：计划层如实认为“没有下一次”（不会退化成每天、也不报错），
// 所以这种条目永远不会触发。界面侧负责不让它出现：设置页不许取消最后一天，旧配置的
// 描述文案也会写成“每周（未选，不会提醒）”（见 settings.js 的 tk-day change 与
// describeTimeSlot）。这条用例钉的是计划层的口径：别哪天改成“没勾就当每天”。
func TestWeeklySlotWithoutDaysNeverFires(t *testing.T) {
	slot := model.TimeSlot{ID: "x", Hour: 12, Minute: 0, Repeat: "weekly", Days: nil, Enabled: true}
	if _, ok := nextSlotAt([]model.TimeSlot{slot}, base()); ok {
		t.Fatal("未勾任何星期的每周条目不该有下一次")
	}
}

// 跨午夜的提前通知：23:50 该为“明天 00:10”的那次提醒弹预通知。
func TestCrossMidnightPreNotify(t *testing.T) {
	slot := model.TimeSlot{ID: "mid", Hour: 0, Minute: 10, Repeat: "daily", Enabled: true}
	now := at(2026, 3, 12, 23, 50)
	if !isTimeSlotDue(slot, now, 20*60_000) {
		t.Fatal("23:50 应为明天 00:10 的那次提醒进入提前区间")
	}
	if occur := slotOccurrenceAt(slot, 20*60_000, now); !sameLocalDay(occur, at(2026, 3, 13, 0, 10)) {
		t.Fatalf("提前线归属的 occurrence 应是明天 00:10，得到 %s",
			time.UnixMilli(occur).Format("01-02 15:04"))
	}
}

// 今天的固定时刻已经全部过去时，摘要表 fixedNext 指向**明天 09:00**：今日时间线
// 不能把这班明天的时间当成一条“待触发”补进来（10:00 打开时间规划，列表里出现一条
// 09:00 会让人以为今天还漏了一次）。
func TestTimelineShowsTomorrowAsToday(t *testing.T) {
	s := cfgOf(t, `{"short":{"enabled":true,"mode":"fixed",
		"slots":[{"id":"s9","hour":9,"minute":0,"repeat":"daily","days":[],"enabled":true}]},
		"long":{"enabled":false},"takeout":{"enabled":false}}`)
	now := at(2026, 3, 12, 10, 0)
	shifts := fixedNextMap(s, now, nil)
	items := todayTimeline(s, now, TimelineOptions{Shifts: shifts})
	for _, it := range items {
		if !sameLocalDay(it.At, now) {
			t.Fatalf("今日时间线里混进了明天的条目：%s %s（source=%s）",
				time.UnixMilli(it.At).Format("01-02 15:04"), it.Name, it.Source)
		}
	}
}
