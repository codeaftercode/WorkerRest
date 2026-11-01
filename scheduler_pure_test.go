package main

import (
	"testing"
	"time"
	"workerrest/internal/model"
	"workerrest/internal/plan"
)

// ---------- 纯函数 ----------

func TestNextSlotAtIsStrictlyAfter(t *testing.T) {
	slot := model.TimeSlot{ID: "s", Hour: 12, Minute: 0, Repeat: "daily", Enabled: true}
	noon := time.Date(2026, 10, 8, 12, 0, 0, 0, time.Local).UnixMilli()
	// 恰好等于该时刻：不算（严格晚于）
	hit, ok := nextSlotAt([]model.TimeSlot{slot}, noon)
	if !ok {
		t.Fatal("应该找得到下一条")
	}
	if hit.At == noon {
		t.Fatal("等于 from 的时刻不该被选中")
	}
	if !plan.SameLocalDay(hit.At, noon+86_400_000) {
		t.Fatalf("应落到第二天，得到 %d", hit.At)
	}
	// 早一秒：就是今天这条
	hit, _ = nextSlotAt([]model.TimeSlot{slot}, noon-1000)
	if hit.At != noon {
		t.Fatalf("早一秒应命中今天 12:00，得到 %d", hit.At)
	}
}

func TestFindDueTimeSlotsDedupAndPrune(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 30, 0, time.Local).UnixMilli()
	slots := []model.TimeSlot{
		{ID: "a", Hour: 12, Minute: 0, Repeat: "daily", Enabled: true},
		{ID: "b", Hour: 12, Minute: 0, Repeat: "daily", Enabled: true},
		{ID: "c", Hour: 13, Minute: 0, Repeat: "daily", Enabled: true},
		{ID: "d", Hour: 12, Minute: 0, Repeat: "daily", Enabled: false},
	}
	fired := map[string]bool{}
	got := plan.FindDueTimeSlots(slots, now, fired, 0)
	if len(got) != 2 {
		t.Fatalf("应命中 a/b 两条（c 未到点、d 已停用），得到 %d", len(got))
	}
	// 同一分钟再跑一次：不重复返回
	if again := plan.FindDueTimeSlots(slots, now+5_000, fired, 0); len(again) != 0 {
		t.Fatalf("同一分钟不该重复触发，得到 %d", len(again))
	}
	if len(fired) != 2 {
		t.Fatalf("已触发集合应有 2 个键，得到 %d", len(fired))
	}
	// 跨天：旧键该被清掉
	nextDay := now + 86_400_000
	plan.FindDueTimeSlots(nil, nextDay, fired, 0)
	if len(fired) != 0 {
		t.Fatalf("跨天后旧键应清空，得到 %v", fired)
	}
}

func TestSlotAlertAtCrossesDayWithLead(t *testing.T) {
	slot := model.TimeSlot{ID: "s", Hour: 0, Minute: 10, Repeat: "daily", Enabled: true}
	day := time.Date(2026, 10, 8, 12, 0, 0, 0, time.Local).UnixMilli()
	got := slotAlertAt(slot, 30*60_000, day)
	want := time.Date(2026, 10, 7, 23, 40, 0, 0, time.Local).UnixMilli()
	if got != want {
		t.Fatalf("提前 30 分钟应落到前一天 23:40，want=%d got=%d", want, got)
	}
}

func TestExpandBuiltinCountdownStopsAtMidnight(t *testing.T) {
	s := testSettings(t, `{"short":{"enabled":true,"mode":"countdown","intervalMs":600000,"durationMs":20000},
		"long":{"enabled":false},"longEveryNShort":2}`)
	now := time.Date(2026, 10, 8, 23, 30, 0, 0, time.Local).UnixMilli()
	first := model.PlannedBreak{Type: "short", Name: "短休息", At: now + 600_000, Source: "countdown"}
	chain := plan.ExpandBuiltinCountdown(s, now, 0, nil, &first)
	for _, p := range chain {
		if !plan.SameLocalDay(p.At, now) {
			t.Fatalf("倒计时链不该跨天，得到 %d", p.At)
		}
	}
	// 23:30 起算：第一跳 23:40，第二跳 = 23:40 + 20 秒时长 + 10 分钟间隔 = 23:50:20，
	// 第三跳就跨天了 —— 所以今天正好两跳。
	if len(chain) != 2 {
		t.Fatalf("23:30 起算今天应有两跳，得到 %d 跳", len(chain))
	}
	wantSecond := time.Date(2026, 10, 8, 23, 50, 20, 0, time.Local).UnixMilli()
	if chain[1].At != wantSecond {
		t.Fatalf("第二跳应为 23:50:20，得到 %d", chain[1].At)
	}
}

func TestFixedNextMapSkipsConsumedToNextDay(t *testing.T) {
	s := testSettings(t, `{"short":{"enabled":true,"mode":"fixed","slots":[
		{"id":"s-a","hour":12,"minute":0,"repeat":"daily","days":[],"enabled":true}]},
		"long":{"enabled":false},"takeout":{"enabled":false}}`)
	now := time.Date(2026, 10, 8, 11, 0, 0, 0, time.Local).UnixMilli()
	noon := time.Date(2026, 10, 8, 12, 0, 0, 0, time.Local).UnixMilli()

	m := fixedNextMap(s, now, nil)
	if m["short"] != noon {
		t.Fatalf("未消费时应给今天 12:00，得到 %v", m)
	}
	m2 := fixedNextMap(s, now, map[string]bool{occurrenceKey("short", noon): true})
	if m2["short"] == noon {
		t.Fatal("已消费的今天这条不该再返回")
	}
	if !plan.SameLocalDay(m2["short"], noon+86_400_000) {
		t.Fatalf("应顺延到明天 12:00，得到 %d", m2["short"])
	}
}

func TestTodayTimelineExcludesOtherDays(t *testing.T) {
	s := testSettings(t, `{"short":{"enabled":true,"mode":"fixed","slots":[
		{"id":"s-a","hour":23,"minute":0,"repeat":"daily","days":[],"enabled":true}]},
		"long":{"enabled":false},"takeout":{"enabled":false}}`)
	now := time.Date(2026, 10, 8, 22, 0, 0, 0, time.Local).UnixMilli()
	items := todayTimeline(s, now, plan.TimelineOptions{})
	if len(items) != 1 {
		t.Fatalf("今天只剩 23:00 一条，得到 %d 条", len(items))
	}
	if !plan.SameLocalDay(items[0].At, now) {
		t.Fatal("时间线只报当天")
	}
}

func TestOccurrenceKeyRoundTrip(t *testing.T) {
	k := occurrenceKey("cb-1", 1_791_432_000_000)
	if got := occurrenceKeyAt(k); got != 1_791_432_000_000 {
		t.Fatalf("解析回不去：%q -> %d", k, got)
	}
	// JS 的 Number('') === 0，所以「没有 #」或后缀为空一律当 0，与 replan 的过期清理口径一致
	if got := occurrenceKeyAt("short"); got != 0 {
		t.Fatalf("没有 # 时应为 0，得到 %d", got)
	}
}

// 固定时刻线被推迟后：原时刻那一跳要从时间线剔除，同时要补回「推迟后的时刻」，
// 否则推迟后该条会从今日时间线消失（与倒计时线 / 外卖的推迟展示口径一致）。
func TestTodayTimelineFixedPostpone(t *testing.T) {
	s := testSettings(t, `{"short":{"enabled":true,"mode":"fixed","slots":[
		{"id":"s-a","hour":12,"minute":0,"repeat":"daily","days":[],"enabled":true}]},
		"long":{"enabled":false},"takeout":{"enabled":false}}`)
	now := time.Date(2026, 10, 8, 11, 0, 0, 0, time.Local).UnixMilli()
	original := time.Date(2026, 10, 8, 12, 0, 0, 0, time.Local).UnixMilli()
	shift := time.Date(2026, 10, 8, 12, 30, 0, 0, time.Local).UnixMilli()
	items := todayTimeline(s, now, plan.TimelineOptions{Shifts: map[string]int64{"short": shift}})
	if len(items) != 1 {
		t.Fatalf("推迟后时间线应只剩推迟时刻那一条，得到 %d 条", len(items))
	}
	if items[0].Type != "short" || items[0].At != shift {
		t.Fatalf("应显示推迟后的时刻 %d，得到 type=%s at=%d", shift, items[0].Type, items[0].At)
	}
	if items[0].At == original {
		t.Fatal("原时刻 12:00 不应再出现")
	}
}

// 自定义提醒取消勾选（config.enabled=false）后不应再被排程 / 触发：调度器的所有触发线
// （倒计时 / 固定时刻 / 状态卡下次 / 今日时间线）都应按 config.enabled 过滤掉它。
// 对应「自定义提醒取消勾选没有效果，仍会提醒」的回归守卫。
func TestDisabledCustomBreakExcluded(t *testing.T) {
	// 仅一条被禁用的自定义倒计时提醒：不应有任何 next，挂起线也应空
	cfg := `{"short":{"enabled":false},"long":{"enabled":false},` +
		`"customBreaks":[{"id":"cb-x","name":"喝水提醒","config":{` +
		`"enabled":false,"mode":"countdown","intervalMs":600000,"durationMs":20000,` +
		`"notifyBeforeMs":10000,"postponeMs":120000,"slots":[]}}]}`
	s, _ := newTestSched(t, cfg)
	s.replan()
	if s.state.NextAt != 0 {
		t.Fatalf("禁用自定义提醒不应排程，NextAt 应为 0，得到 %d", s.state.NextAt)
	}
	if len(s.state.Lines) != 0 {
		t.Fatalf("禁用自定义提醒不应出现在挂起线，得到 %+v", s.state.Lines)
	}
	st := s.stateForRenderer()
	if st.Other != nil {
		t.Fatalf("禁用自定义提醒不应出现在状态卡合并组，得到 %+v", st.Other)
	}
	if len(st.Timeline) != 0 {
		t.Fatalf("禁用自定义提醒不应出现在今日时间线，得到 %+v", st.Timeline)
	}
	// 启用短休息后，NextType 应是 short 而非被禁用的自定义提醒
	s2, _ := newTestSched(t, `{"short":{"enabled":true,"mode":"countdown","intervalMs":600000,"durationMs":20000},`+
		`"long":{"enabled":false},`+
		`"customBreaks":[{"id":"cb-x","name":"喝水提醒","config":{`+
		`"enabled":false,"mode":"countdown","intervalMs":600000,"durationMs":20000,`+
		`"notifyBeforeMs":10000,"postponeMs":120000,"slots":[]}}]}`)
	s2.replan()
	if s2.state.NextType != "short" {
		t.Fatalf("禁用自定义提醒不应成为 NextType，得到 %q", s2.state.NextType)
	}
}

// 外卖推迟后：设置页状态卡的「下次外卖提醒」（合并组 st.other）与 fixedNext.takeout、
// 今日时间线都要反映推迟后的时刻，而不是原定时刻。外卖的推迟不经调度器，stateForRenderer
// 必须自己把推迟线覆盖进派生视图（与 FixedNext 同一口径）。
func TestStateForRendererTakeoutPostpone(t *testing.T) {
	const onlyTakeout = `{"short":{"enabled":false},"long":{"enabled":false},` +
		`"takeout":{"enabled":true,"leadSec":0,"postponeMin":15,"slots":[` +
		`{"id":"s1","hour":12,"minute":30,"repeat":"daily","days":[],"enabled":true}]},` +
		`"customBreaks":[]}`
	s, host := newTestSched(t, onlyTakeout)
	s.replan()
	due := nowMs() + 30*60_000
	host.pp = &model.TakeoutPostponed{
		Slot:  model.TimeSlot{ID: "s1", Hour: 12, Minute: 30, Repeat: "daily", Enabled: true},
		DueAt: due,
		Count: 0,
	}
	st := s.stateForRenderer()
	if st.Other == nil || st.Other.Name != plan.TAKEOUT_NAME {
		t.Fatalf("合并组下次应是外卖，得到 %+v", st.Other)
	}
	if st.Other.At != due {
		t.Fatalf("下次外卖提醒应=推迟后时刻 %d，得到 %d", due, st.Other.At)
	}
	if st.FixedNext["takeout"] != due {
		t.Fatalf("fixedNext.takeout 应=推迟后时刻 %d，得到 %d", due, st.FixedNext["takeout"])
	}
	found := false
	for _, it := range st.Timeline {
		if it.Type == "takeout" && it.At == due {
			found = true
		}
	}
	if !found {
		t.Fatal("今日时间线应包含推迟后的外卖条目")
	}
}

func TestRemindersByIDForCustomBreak(t *testing.T) {
	s := testSettings(t, `{"reminderMessages":{"short":{"order":"fixed","fixedId":"x","items":[{"id":"x","text":"短的"}]}},
		"customBreaks":[{"id":"cb-1","name":"喝水","config":{"intervalMs":60000},
			"messages":{"order":"fixed","fixedId":"m1","items":[{"id":"m1","text":"该喝水了","enabled":true}]}}]}`)
	rm := s.RemindersByID("cb-1")
	if len(rm.Items) != 1 || rm.Items[0].Text != "该喝水了" {
		t.Fatalf("自定义提醒该取自己的提醒内容，得到 %+v", rm.Items)
	}
	if got := s.RemindersByID("short"); len(got.Items) != 1 || got.Items[0].Text != "短的" {
		t.Fatalf("短休息该取共用的那份，得到 %+v", got.Items)
	}
	// 认不出的身份回落到 short（与 types.ts 的 breakMessagesById 一致）
	if got := s.RemindersByID("cb-不存在"); len(got.Items) != 1 || got.Items[0].Text != "短的" {
		t.Fatalf("未知身份应回落 short，得到 %+v", got.Items)
	}
}

func TestDescribePlanText(t *testing.T) {
	at := time.Date(2026, 10, 8, 12, 0, 0, 0, time.Local).UnixMilli()
	got := describePlan(&model.PlannedBreak{Type: "long", Name: "长休息", At: at, Source: "fixed"})
	if got != "长休息（固定时刻）12:00" {
		t.Fatalf("摘要文案不对：%q", got)
	}
	if got := describePlan(nil); got != "未安排休息" {
		t.Fatalf("无计划时的文案不对：%q", got)
	}
}

// itoa 测试里拼 JSON 用（避免引入 strconv 只为一处）
func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [8]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
