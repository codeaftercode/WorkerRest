package plan

import (
	"testing"

	"workerrest/internal/model"
	"workerrest/internal/settings"
)

// export.go 是给调度器用的薄封装：同一个实现两处入口，容易在重构时“只改一边”。
// 这里只钉“封装确实转发到实现”，不重复实现里的判据（那些在 breakplan_test.go）。

func TestExportedWrappersForwardToImplementation(t *testing.T) {
	s := settings.New(settings.DefaultSettingsMap(), "")
	now := base()

	// PlanNextBreak == planNextBreak
	want, wantOK := planNextBreak(s, now, 0, nil, nil)
	got, gotOK := PlanNextBreak(s, now, 0, nil, nil)
	if gotOK != wantOK || got.At != want.At || got.Type != want.Type {
		t.Fatalf("PlanNextBreak 与 planNextBreak 不一致：%+v/%v vs %+v/%v", got, gotOK, want, wantOK)
	}

	// CountdownAnchor == countdownAnchor（含 Frozen 分支）
	prev := map[string]CdAnchor{"short": {At: now - 1, Iv: 600_000, Frozen: true}}
	if a, b := CountdownAnchor(prev, "short", now, nil, now+600_000, 600_000), countdownAnchor(prev, "short", now, nil, now+600_000, 600_000); a != b {
		t.Fatalf("CountdownAnchor 与实现不一致：%d vs %d", a, b)
	}

	// DescribePlan / TakeoutMealLabel / PreBreakTitle 这类纯转发再抽查一次
	if DescribePlan(nil) != describePlan(nil) {
		t.Fatal("DescribePlan 与实现不一致")
	}
	if TakeoutMealLabel(12) != takeoutMealLabel(12) {
		t.Fatal("TakeoutMealLabel 与实现不一致")
	}
	if PreBreakTitle("喝水") != preBreakTitle("喝水") {
		t.Fatal("PreBreakTitle 与实现不一致")
	}
	if PreBreakCountdownTail("喝水") != preBreakCountdownTail("喝水") {
		t.Fatal("PreBreakCountdownTail 与实现不一致")
	}
	if OccurrenceKeyAt(OccurrenceKey("short", 42)) != 42 {
		t.Fatal("OccurrenceKey/At 往返不一致")
	}
	if SlotAlertAt(mustSlot(), 60_000, now) != slotAlertAt(mustSlot(), 60_000, now) {
		t.Fatal("SlotAlertAt 与实现不一致")
	}
	if !SameLocalDay(now, now) {
		t.Fatal("SameLocalDay 应判同一天")
	}
}

func mustSlot() model.TimeSlot {
	return model.TimeSlot{ID: "s", Hour: 12, Minute: 0, Enabled: true, Repeat: "daily"}
}
