package main

import (
	"testing"
	"time"
	"workerrest/internal/model"
)

// ---------- 外卖 ----------

// takeoutSkipCfg 与 takeoutCfg 等价，但「暂停休息时」显式写 skip（默认已改成推迟；
// 时刻取 12:30，正常跑测试时不会是「此刻」）
const takeoutSkipCfg = `{"takeout":{"enabled":true,"leadSec":0,"postponeMin":15,"pauseAction":"skip","slots":[{"id":"s1","hour":12,"minute":30,"repeat":"daily","days":[],"enabled":true}]},"reminderMessages":{"takeout":{"order":"random","fixedId":"","items":[{"id":"m1","text":"该点外卖啦","enabled":true}]}}}`

// takeoutDueNowCfg 一条「此刻到点」的外卖配置；extra 追加到 takeout 对象里
func takeoutDueNowCfg(extra string) string {
	now := time.Now()
	return `{"takeout":{"enabled":true,"leadSec":0,"postponeMin":15` + extra + `,"slots":[` +
		`{"id":"s1","hour":` + itoa(now.Hour()) + `,"minute":` + itoa(now.Minute()) +
		`,"repeat":"daily","days":[],"enabled":true}]}}`
}

// 跳过提醒：暂停期间外卖到点不弹窗、不弹提前提示，记一次「取消 / 跳过」
func TestTakeoutPausedSkipRecords(t *testing.T) {
	cfg := takeoutDueNowCfg(`,"pauseAction":"skip"`)
	a := newTakeoutApp(t, cfg)
	s, _ := newTestSched(t, cfg)
	s.Pause("manual")
	a.sched = s

	a.checkTakeoutReminders()
	if a.takeoutCurrent != nil {
		t.Fatal("暂停期间不应弹出外卖提醒窗口")
	}
	if a.preInfo.Owner == "takeout" {
		t.Fatal("暂停期间不应弹出外卖提前提示小窗")
	}
	if got := a.statsStore().Today().TakeoutSkipped; got != 1 {
		t.Fatalf("暂停期间到点应记 1 次跳过，得到 %d", got)
	}
	// 同一分钟内再跑一跳：不重复记账
	a.checkTakeoutReminders()
	if got := a.statsStore().Today().TakeoutSkipped; got != 1 {
		t.Fatalf("同一 occurrence 不应重复记账，得到 %d", got)
	}
}

// 正常提醒：暂停期间照常弹外卖提醒窗口，不记账
func TestTakeoutPausedRemindShowsWindow(t *testing.T) {
	cfg := takeoutDueNowCfg(`,"pauseAction":"remind"`)
	a := newTakeoutApp(t, cfg)
	s, _ := newTestSched(t, cfg)
	s.Pause("manual")
	a.sched = s

	a.checkTakeoutReminders()
	if a.takeoutCurrent == nil {
		t.Fatal("「正常提醒」时暂停期间到点应照常弹外卖提醒窗口")
	}
	if got := a.statsStore().Today().TakeoutSkipped; got != 0 {
		t.Fatalf("「正常提醒」不应记账，得到 %d", got)
	}
	// 同一分钟内再跑一跳：靠 takeoutFired 去重，不重复弹
	a.checkTakeoutReminders()
	if a.takeoutCurrent == nil {
		t.Fatal("重复 tick 不应关掉已弹出的提醒窗口")
	}
}

// 推迟提醒：到点不弹窗、不记账，排一条「推迟到 now+15 分钟」；仍暂停时再到期就继续推
func TestTakeoutPausedPostponeSetsPlan(t *testing.T) {
	cfg := takeoutDueNowCfg(`,"pauseAction":"postpone"`)
	a := newTakeoutApp(t, cfg)
	s, _ := newTestSched(t, cfg)
	s.Pause("manual")
	a.sched = s

	a.checkTakeoutReminders()
	if a.takeoutCurrent != nil {
		t.Fatal("「推迟提醒」不应弹外卖提醒窗口")
	}
	if a.preInfo.Owner == "takeout" {
		t.Fatal("「推迟提醒」不应弹提前提示小窗")
	}
	if got := a.statsStore().Today().TakeoutSkipped; got != 0 {
		t.Fatalf("「推迟提醒」不算跳过，得到 %d", got)
	}
	if a.takeoutPostponed == nil {
		t.Fatal("应排下一条推迟计划")
	}
	first := a.takeoutPostponed.DueAt - nowMs()
	if first < 14*60_000 || first > 16*60_000 {
		t.Fatalf("推迟后的时刻应约为 now+15 分钟，得到 %d ms", first)
	}
	// 同一分钟再跑：计划不变（不重复排、也不弹）
	a.checkTakeoutReminders()
	if a.takeoutPostponed == nil || a.takeoutPostponed.DueAt != nowMs()+first {
		// 允许毫秒级抖动，只校验「仍然在未来且没有被清掉」
		if a.takeoutPostponed == nil {
			t.Fatal("重复 tick 不应清掉推迟计划")
		}
	}
	// 推后的时刻又到了、仍然暂停：继续往后推，不弹窗
	a.takeoutPostponed.DueAt = nowMs() - 1000
	a.checkTakeoutReminders()
	if a.takeoutPostponed == nil || a.takeoutPostponed.DueAt <= nowMs() {
		t.Fatal("仍暂停时应继续往后推")
	}
	if a.takeoutCurrent != nil {
		t.Fatal("仍暂停时不应弹窗")
	}
}

// 推迟提醒：恢复后按推迟后的时刻弹窗
func TestTakeoutPausedPostponeFiresAfterResume(t *testing.T) {
	cfg := takeoutDueNowCfg(`,"pauseAction":"postpone"`)
	a := newTakeoutApp(t, cfg)
	s, _ := newTestSched(t, cfg)
	s.Pause("manual")
	a.sched = s
	a.checkTakeoutReminders()
	if a.takeoutPostponed == nil {
		t.Fatal("应排下一条推迟计划")
	}
	s.Resume("manual")
	a.takeoutPostponed.DueAt = nowMs() - 1000
	a.checkTakeoutReminders()
	if a.takeoutCurrent == nil {
		t.Fatal("恢复后推迟到点应弹出外卖提醒窗口")
	}
	if got := a.statsStore().Today().TakeoutSkipped; got != 0 {
		t.Fatalf("推迟到点弹出不算跳过，得到 %d", got)
	}
}

// 推迟提醒但「可推迟」填了 0：退化成跳过
func TestTakeoutPausedPostponeZeroFallsBackToSkip(t *testing.T) {
	now := time.Now()
	cfg := `{"takeout":{"enabled":true,"leadSec":0,"pauseAction":"postpone","postponeMin":0,"slots":[` +
		`{"id":"s1","hour":` + itoa(now.Hour()) + `,"minute":` + itoa(now.Minute()) +
		`,"repeat":"daily","days":[],"enabled":true}]}}`
	a := newTakeoutApp(t, cfg)
	s, _ := newTestSched(t, cfg)
	s.Pause("manual")
	a.sched = s
	a.checkTakeoutReminders()
	if a.takeoutPostponed != nil {
		t.Fatal("推迟时长为 0 时不应留下推迟计划")
	}
	if got := a.statsStore().Today().TakeoutSkipped; got != 1 {
		t.Fatalf("推迟时长为 0 时应记 1 次跳过，得到 %d", got)
	}
}

// 跳过的推迟线：暂停期间推迟线到点同样按跳过处理并清掉计划（恢复后不补弹）
func TestTakeoutPausedSkipPostponedLine(t *testing.T) {
	cfg := takeoutSkipCfg
	a := newTakeoutApp(t, cfg)
	s, _ := newTestSched(t, cfg)
	s.Pause("manual")
	a.sched = s
	a.takeoutPostponed = &model.TakeoutPostponed{
		Slot:  model.TimeSlot{ID: "s1", Hour: 12, Minute: 30, Repeat: "daily", Enabled: true},
		DueAt: nowMs() - 1000,
		Count: 1,
	}
	a.checkTakeoutReminders()
	if a.takeoutPostponed != nil {
		t.Fatal("暂停期间到点的推迟线应清空")
	}
	if got := a.statsStore().Today().TakeoutSkipped; got != 1 {
		t.Fatalf("推迟线到点应记 1 次跳过，得到 %d", got)
	}
	if a.takeoutCurrent != nil {
		t.Fatal("暂停期间不应弹窗外卖")
	}
}

// 默认（未写 pauseAction）= 推迟提醒：外卖暂停期间到点同样只排推迟计划
func TestTakeoutPausedDefaultPostpone(t *testing.T) {
	cfg := takeoutDueNowCfg("")
	a := newTakeoutApp(t, cfg)
	s, _ := newTestSched(t, cfg)
	s.Pause("manual")
	a.sched = s

	a.checkTakeoutReminders()
	if a.takeoutCurrent != nil {
		t.Fatal("默认「推迟提醒」不应弹外卖提醒窗口")
	}
	if a.preInfo.Owner == "takeout" {
		t.Fatal("默认「推迟提醒」不应弹提前提示小窗")
	}
	if got := a.statsStore().Today().TakeoutSkipped; got != 0 {
		t.Fatalf("默认「推迟提醒」不算跳过，得到 %d", got)
	}
	if a.takeoutPostponed == nil {
		t.Fatal("默认「推迟提醒」应排下一条推迟计划")
	}
	if d := a.takeoutPostponed.DueAt - nowMs(); d < 14*60_000 || d > 16*60_000 {
		t.Fatalf("推迟后的时刻应约为 now+15 分钟，得到 %d ms", d)
	}
}
