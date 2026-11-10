package main

import (
	"testing"
	"time"
	"workerrest/internal/model"
)

// ---------- 外卖 ----------

// takeoutSkipCfg 与 takeoutCfg 等价，但“暂停休息时”显式写 skip（默认已改成推迟；
// 时刻取 12:30，正常跑测试时不会是“此刻”）
const takeoutSkipCfg = `{"takeout":{"enabled":true,"leadSec":0,"postponeMin":15,"pauseAction":"skip","slots":[{"id":"s1","hour":12,"minute":30,"repeat":"daily","days":[],"enabled":true}]},"reminderMessages":{"takeout":{"order":"random","fixedId":"","items":[{"id":"m1","text":"该点外卖啦","enabled":true}]}}}`

// takeoutDueNowCfg 一条“此刻到点”的外卖配置；extra 追加到 takeout 对象里
func takeoutDueNowCfg(extra string) string {
	now := time.Now()
	return `{"takeout":{"enabled":true,"leadSec":0,"postponeMin":15` + extra + `,"slots":[` +
		`{"id":"s1","hour":` + itoa(now.Hour()) + `,"minute":` + itoa(now.Minute()) +
		`,"repeat":"daily","days":[],"enabled":true}]}}`
}

// 跳过提醒：暂停期间外卖到点不弹窗、不弹提前提示，记一次“取消 / 跳过”
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
		t.Fatal("“正常提醒”时暂停期间到点应照常弹外卖提醒窗口")
	}
	if got := a.statsStore().Today().TakeoutSkipped; got != 0 {
		t.Fatalf("“正常提醒”不应记账，得到 %d", got)
	}
	// 同一分钟内再跑一跳：靠 takeoutFired 去重，不重复弹
	a.checkTakeoutReminders()
	if a.takeoutCurrent == nil {
		t.Fatal("重复 tick 不应关掉已弹出的提醒窗口")
	}
}

// 推迟提醒：到点不弹窗、不记账，排一条“推迟到 now+15 分钟”；仍暂停时再到期就继续推
func TestTakeoutPausedPostponeSetsPlan(t *testing.T) {
	cfg := takeoutDueNowCfg(`,"pauseAction":"postpone"`)
	a := newTakeoutApp(t, cfg)
	s, _ := newTestSched(t, cfg)
	s.Pause("manual")
	a.sched = s

	a.checkTakeoutReminders()
	if a.takeoutCurrent != nil {
		t.Fatal("“推迟提醒”不应弹外卖提醒窗口")
	}
	if a.preInfo.Owner == "takeout" {
		t.Fatal("“推迟提醒”不应弹提前提示小窗")
	}
	if got := a.statsStore().Today().TakeoutSkipped; got != 0 {
		t.Fatalf("“推迟提醒”不算跳过，得到 %d", got)
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
		// 允许毫秒级抖动，只校验“仍然在未来且没有被清掉”
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

// 推迟时长下限 1 分钟：旧配置写 0 也不再退化成跳过，而是按 1 分钟推迟
func TestTakeoutPausedPostponeZeroClimbsToOneMinute(t *testing.T) {
	now := time.Now()
	cfg := `{"takeout":{"enabled":true,"leadSec":0,"pauseAction":"postpone","postponeMin":0,"slots":[` +
		`{"id":"s1","hour":` + itoa(now.Hour()) + `,"minute":` + itoa(now.Minute()) +
		`,"repeat":"daily","days":[],"enabled":true}]}}`
	a := newTakeoutApp(t, cfg)
	s, _ := newTestSched(t, cfg)
	s.Pause("manual")
	a.sched = s
	a.checkTakeoutReminders()
	if a.takeoutPostponed == nil {
		t.Fatal("推迟时长下限 1 分钟：0 的配置应照常推迟，不该退化成跳过")
	}
	if d := a.takeoutPostponed.DueAt - nowMs(); d <= 0 || d > 61_000 {
		t.Fatalf("推迟时长应约 1 分钟，得到 %d ms", d)
	}
	if got := a.statsStore().Today().TakeoutSkipped; got != 0 {
		t.Fatalf("推迟不该记跳过，得到 %d", got)
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
		t.Fatal("默认“推迟提醒”不应弹外卖提醒窗口")
	}
	if a.preInfo.Owner == "takeout" {
		t.Fatal("默认“推迟提醒”不应弹提前提示小窗")
	}
	if got := a.statsStore().Today().TakeoutSkipped; got != 0 {
		t.Fatalf("默认“推迟提醒”不算跳过，得到 %d", got)
	}
	if a.takeoutPostponed == nil {
		t.Fatal("默认“推迟提醒”应排下一条推迟计划")
	}
	if d := a.takeoutPostponed.DueAt - nowMs(); d < 14*60_000 || d > 16*60_000 {
		t.Fatalf("推迟后的时刻应约为 now+15 分钟，得到 %d ms", d)
	}
}

// 自动暂停：即使外卖配了“正常提醒”，到点也不弹窗、按跳过记账 ——
// 自动暂停不看用户给外卖配的“暂停休息时”，那是给手动暂停用的。
func TestTakeoutAutoPauseSkipsEvenWithRemind(t *testing.T) {
	cfg := takeoutDueNowCfg(`,"pauseAction":"remind"`)
	a := newTakeoutApp(t, cfg)
	s, _ := newTestSched(t, cfg)
	s.Pause(pauseReasonDnd) // 勿扰 = 自动原因
	a.sched = s

	a.checkTakeoutReminders()
	if a.takeoutCurrent != nil {
		t.Fatal("自动暂停期间不应弹外卖提醒窗口（配置是“正常提醒”也不行）")
	}
	if a.preInfo.Owner == "takeout" {
		t.Fatal("自动暂停期间不应弹外卖提前提示小窗")
	}
	if got := a.statsStore().Today().TakeoutSkipped; got != 1 {
		t.Fatalf("自动暂停期间到点应记 1 次跳过，得到 %d", got)
	}
	// 同一分钟内再跑一跳：不重复记账
	a.checkTakeoutReminders()
	if got := a.statsStore().Today().TakeoutSkipped; got != 1 {
		t.Fatalf("同一 occurrence 不应重复记账，得到 %d", got)
	}
}

// 进入自动暂停时 App 侧清场：待机的“推迟线”直接作废并记一次跳过
// （恢复后不补弹），外卖预通知窗也一并收掉。
func TestOnAutoPauseDiscardsTakeoutPostponed(t *testing.T) {
	a := newTakeoutApp(t, takeoutCfg)
	a.preInfo = PreBreakInfo{Owner: "takeout", Title: "该点外卖啦"}
	a.takeoutPostponed = &model.TakeoutPostponed{DueAt: nowMs() + 60_000, Count: 1}

	a.OnAutoPause()

	if a.takeoutPostponed != nil {
		t.Fatal("自动暂停后推迟线应作废（恢复后不补弹）")
	}
	if got := a.statsStore().Today().TakeoutSkipped; got != 1 {
		t.Fatalf("作废的推迟线应记 1 次跳过，得到 %d", got)
	}
	if a.preInfo.Owner == "takeout" {
		t.Fatal("外卖预通知的 owner 应被清掉（窗口由 closePreBreakWindow 关）")
	}
}
