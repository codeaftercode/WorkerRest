package main

import (
	"testing"
	"time"
	"workerrest/internal/model"
	"workerrest/internal/scheduler"
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

// postponeMin=0 = 没勾「允许推迟提醒」（2026-11-13）：暂停期间到点没有时长可推，
// 处置降级成跳过（与调度器 pauseDisposition 同口径），不留推迟计划
func TestTakeoutPausedPostponeZeroSkips(t *testing.T) {
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
		t.Fatal("postponeMin=0 时不该留下推迟计划")
	}
	if got := a.statsStore().Today().TakeoutSkipped; got != 1 {
		t.Fatalf("postponeMin=0 暂停期间到点应记 1 次跳过，得到 %d", got)
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

// 自动暂停不再有例外：外卖配了“正常提醒”，勿扰里到点照样弹窗、不记账
// （2026-11-12 用户口径：“包括短休息、长休息、外卖提醒、自定义提醒在内的所有提醒，
// 都受“暂停休息时”的控制，不存在例外”）。
func TestTakeoutAutoPauseHonorsRemind(t *testing.T) {
	cfg := takeoutDueNowCfg(`,"pauseAction":"remind"`)
	a := newTakeoutApp(t, cfg)
	s, _ := newTestSched(t, cfg)
	s.Pause(pauseReasonDnd) // 勿扰 = 自动原因
	a.sched = s

	a.checkTakeoutReminders()
	if a.takeoutCurrent == nil {
		t.Fatal("自动暂停下“正常提醒”应照常弹外卖提醒窗口")
	}
	if got := a.statsStore().Today().TakeoutSkipped; got != 0 {
		t.Fatalf("“正常提醒”不应记账，得到 %d", got)
	}
	// 同一分钟内再跑一跳：靠 takeoutFired 去重，不重复弹也不重复记账
	a.checkTakeoutReminders()
	if got := a.statsStore().Today().TakeoutSkipped; got != 0 {
		t.Fatalf("同一 occurrence 不应重复记账，得到 %d", got)
	}
}

// 自动暂停 + 跳过提醒：与手动暂停同一套动作（记一笔取消、不弹窗）
func TestTakeoutAutoPauseHonorsSkip(t *testing.T) {
	cfg := takeoutDueNowCfg(`,"pauseAction":"skip"`)
	a := newTakeoutApp(t, cfg)
	s, _ := newTestSched(t, cfg)
	s.Pause(pauseReasonDnd)
	a.sched = s

	a.checkTakeoutReminders()
	if a.takeoutCurrent != nil {
		t.Fatal("“跳过提醒”不该弹外卖提醒窗口")
	}
	if got := a.statsStore().Today().TakeoutSkipped; got != 1 {
		t.Fatalf("自动暂停下“跳过提醒”到点应记 1 次取消，得到 %d", got)
	}
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

	// 两条配置都是默认的“推迟提醒”，所以留窗清单全空
	a.OnAutoPause(scheduler.AutoPauseKeep{})

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

// “正常提醒”时进入自动暂停：外卖窗与推迟线都留着、不记取消，外卖预通知也留着
// （用户口径：这条提醒选了“正常提醒”，它的窗口就不该被收）。
func TestOnAutoPauseKeepsRemindTakeout(t *testing.T) {
	cfg := takeoutDueNowCfg(`,"pauseAction":"remind"`)
	a := newTakeoutApp(t, cfg)
	a.preInfo = PreBreakInfo{Owner: "takeout", Title: "该点外卖啦"}
	a.preTargetAt = nowMs() + 60_000
	a.takeoutPostponed = &model.TakeoutPostponed{DueAt: nowMs() + 60_000, Count: 1}

	a.OnAutoPause(scheduler.AutoPauseKeep{Takeout: true})

	if a.takeoutPostponed == nil {
		t.Fatal("“正常提醒”时推迟线应保留（恢复后照常补弹）")
	}
	if got := a.statsStore().Today().TakeoutSkipped; got != 0 {
		t.Fatalf("“正常提醒”不该记取消，得到 %d", got)
	}
	if a.preInfo.Owner != "takeout" || a.preTargetAt == 0 {
		t.Fatal("外卖预通知属于“正常提醒”，窗口与目标时刻都该保留")
	}
}

// 休息 / 自定义提醒的提前提示窗同理：判据是**这扇窗自己**提示的那条提醒的“暂停休息时”。
func TestOnAutoPauseKeepsBreakPreWindow(t *testing.T) {
	a := newTakeoutApp(t, `{"short":{"enabled":true,"pauseAction":"remind"}}`)
	a.preInfo = PreBreakInfo{Kind: "short", Title: "建议您短休息"}
	a.preTargetAt = nowMs() + 30_000

	a.OnAutoPause(scheduler.AutoPauseKeep{PreKind: "short"})

	if a.preInfo.Title == "" {
		t.Fatal("“正常提醒”的提前提示窗应保留（info 不该被清）")
	}
	if a.preTargetAt == 0 {
		t.Fatal("保留的提前提示窗不该清掉目标时刻（窗口倒计时还要用）")
	}
}

// 对照：同一条短休息配的是“推迟提醒”，同一扇提示窗就该收掉；窗上没带身份时，
// 调度器给的那条身份也能兜住（老会话 / 单测构造的窗就是这种）。
func TestOnAutoPauseClosesPostponeBreakPreWindow(t *testing.T) {
	a := newTakeoutApp(t, `{"short":{"enabled":true,"pauseAction":"postpone"}}`)
	a.preInfo = PreBreakInfo{Kind: "short", Title: "建议您短休息"}
	a.preTargetAt = nowMs() + 30_000

	a.OnAutoPause(scheduler.AutoPauseKeep{PreKind: "short"})

	if a.preInfo.Title != "" || a.preTargetAt != 0 {
		t.Fatal("“推迟提醒”的提示窗应被收掉，状态也要一起清")
	}
}

// 归属按窗自己判：窗上提示的是“喝水提醒”（配了正常提醒），而调度器当下在等的那条是
// 短休息（推迟提醒）—— 窗该留下，不能被“当前那条”的口径带走。
// 暂停中照常弹出的自定义提醒就是这个场景（pausedFire 不经过 phase=pre）。
func TestOnAutoPausePrefersWindowOwnKind(t *testing.T) {
	const cfg = `{"short":{"enabled":true,"pauseAction":"postpone"},` +
		`"customBreaks":[{"id":"cb-water","name":"喝水提醒","config":{"enabled":true,"pauseAction":"remind"}}]}`
	a := newTakeoutApp(t, cfg)
	a.preInfo = PreBreakInfo{Kind: "cb-water", Custom: true, Title: "喝水提醒时间到"}
	a.preTargetAt = nowMs() + 30_000

	a.OnAutoPause(scheduler.AutoPauseKeep{PreKind: "short"})

	if a.preInfo.Title == "" || a.preTargetAt == 0 {
		t.Fatal("应看窗自己的归属（cb-water 配了正常提醒），不能按调度器当前那条（short）收窗")
	}
}

// 反向：窗是短休息（推迟提醒），调度器那边即便说“正常提醒”也不该替它做主。
func TestOnAutoPauseDoesNotTrustSchedulerKindOverWindow(t *testing.T) {
	const cfg = `{"short":{"enabled":true,"pauseAction":"postpone"},` +
		`"customBreaks":[{"id":"cb-water","name":"喝水提醒","config":{"enabled":true,"pauseAction":"remind"}}]}`
	a := newTakeoutApp(t, cfg)
	a.preInfo = PreBreakInfo{Kind: "short", Title: "建议您短休息"}
	a.preTargetAt = nowMs() + 30_000

	a.OnAutoPause(scheduler.AutoPauseKeep{PreKind: "cb-water"})

	if a.preInfo.Title != "" || a.preTargetAt != 0 {
		t.Fatal("窗自己配的是“推迟提醒”就该收，调度器给的身份只当窗没带身份时兜底")
	}
}

// 窗上没带身份（老会话 / 单测构造）时，用调度器交代的那条兜底；配了“正常提醒”就留着。
func TestOnAutoPauseFallsBackToSchedulerKind(t *testing.T) {
	a := newTakeoutApp(t, `{"short":{"enabled":true,"pauseAction":"remind"}}`)
	a.preInfo = PreBreakInfo{Title: "建议您短休息"}
	a.preTargetAt = nowMs() + 30_000

	a.OnAutoPause(scheduler.AutoPauseKeep{PreKind: "short"})

	if a.preInfo.Title == "" || a.preTargetAt == 0 {
		t.Fatal("窗没带身份时应按调度器给的归属判，配了“正常提醒”就留下")
	}
}

// 手动暂停（托盘 / 弹窗内“暂停计时”）同样按窗自己的归属判：只剩提示窗要收，
// 配了“正常提醒”的那条留着，恢复后继续倒数。外卖预通知不在这条路径上。
func TestOnPausedKeepsWindowByOwnKind(t *testing.T) {
	const cfg = `{"short":{"enabled":true,"pauseAction":"postpone"},` +
		`"customBreaks":[{"id":"cb-water","name":"喝水提醒","config":{"enabled":true,"pauseAction":"remind"}}]}`
	a := newTakeoutApp(t, cfg)
	a.preInfo = PreBreakInfo{Kind: "cb-water", Custom: true, Title: "喝水提醒时间到"}
	a.preTargetAt = nowMs() + 30_000

	// 调度器那边在等的可能是别的条（这里给 short），不该替这扇窗做主
	a.OnPaused("short")
	if a.preInfo.Title == "" || a.preTargetAt == 0 {
		t.Fatal("窗自己配了“正常提醒”，手动暂停也不该收它")
	}

	a.preInfo = PreBreakInfo{Kind: "short", Title: "建议您短休息"}
	a.preTargetAt = nowMs() + 30_000
	a.OnPaused("short")
	if a.preInfo.Title != "" || a.preTargetAt != 0 {
		t.Fatal("窗自己配的是“推迟提醒”，手动暂停该把它收掉")
	}

	// 外卖预通知归外卖卡，手动暂停这条路径一概不碰
	a.preInfo = PreBreakInfo{Owner: "takeout", Title: "该点外卖啦"}
	a.preTargetAt = nowMs() + 30_000
	a.OnPaused("")
	if a.preInfo.Owner != "takeout" {
		t.Fatal("外卖预通知不该被“暂停休息”收掉（它按外卖自己的 pauseAction 走）")
	}
}
