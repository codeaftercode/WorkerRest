package main

// 托盘文案的**组合场景**矩阵（2026-11-13）。
//
// trayTextsAt 是纯函数，但它的分支优先级此前只有单点用例（tray_test.go 五行）。
// 真正会咬人的是叠加态：暂停 + 一次冻结着的休息、定时暂停 + 到期被全屏挡住、
// 自动原因消失但还没点恢复、等待空闲撞上定时暂停……这份文件按「状态组合」铺开，
// 每个组合钉住 tooltip / 状态行 / 动作项 / 暂停项四段文案。
//
// 末尾还有一条**跨层**用例：真调度器（main 包的 newTestSched）排好计划 → 取
// StateForRenderer 快照 → 直接喂 trayTextsAt，验证侧栏与托盘吃的是同一份状态。

import (
	"strings"
	"testing"
	"time"

	"workerrest/internal/model"
	"workerrest/internal/plan"
)

// trayOf 便于一次拿四段文案
func trayOf(st model.SchedulerState, cleared bool, hold []string, now int64) (string, string, string, string) {
	return trayTextsAt(st, cleared, hold, now)
}

// 暂停中还有一次“冻结着的休息”（它自己选的是推迟）：除了暂停原因，还要说清这次
// 休息还剩多久 —— 恢复之后要接着走完。
func TestTrayScenarioPausedWithFrozenBreak(t *testing.T) {
	now := nowMs()
	st := model.SchedulerState{
		Paused: true, Phase: "break", NextType: "short", NextName: plan.ShortBreakName,
		BreakStartedAt: now - 20_000, BreakEndsAt: now + 150_000,
		PausedAt: now - 60_000, PauseReasons: []string{"manual"},
	}
	tip, status, action, pause := trayOf(st, false, nil, now)
	if !strings.Contains(tip, "已暂停 1 分钟：手动暂停") {
		t.Fatalf("tooltip 应写暂停原因，实际 %q", tip)
	}
	if !strings.Contains(tip, "这次休息还剩 3 分 30 秒") {
		t.Fatalf("冻结着的休息要写剩余（= BreakEndsAt − PausedAt，暂停那一刻的剩余），实际 %q", tip)
	}
	if status != "状态：已暂停" {
		t.Fatalf("状态行 = %q", status)
	}
	if action != "立即休息" || pause != "恢复休息" {
		t.Fatalf("动作项 = %q / 暂停项 = %q", action, pause)
	}
}

// 暂停中「立即休息」发起的那次（BreakWhilePaused）：按真实时钟走完，不写“还剩”。
func TestTrayScenarioPausedWhileBreakRuns(t *testing.T) {
	now := nowMs()
	st := model.SchedulerState{
		Paused: true, Phase: "break", BreakWhilePaused: true,
		BreakStartedAt: now, BreakEndsAt: now + 20_000,
		PausedAt: now - 30_000, PauseReasons: []string{"manual"},
	}
	tip, _, _, _ := trayOf(st, false, nil, now)
	if strings.Contains(tip, "这次休息还剩") {
		t.Fatalf("暂停中主动发起的休息按真实时钟走，不该说“还剩”，实际 %q", tip)
	}
	if !strings.Contains(tip, "已暂停") {
		t.Fatalf("tooltip 仍要说清暂停态，实际 %q", tip)
	}
}

// 定时暂停：说“还剩多久（到几点）”比“已暂停多久”有用。
func TestTrayScenarioTimedPauseRemaining(t *testing.T) {
	now := nowMs()
	st := model.SchedulerState{
		Paused: true, PauseDeadline: now + 29*60_000, PausedAt: now - 60_000,
		PauseReasons: []string{"manual"},
	}
	tip, status, _, pause := trayOf(st, false, nil, now)
	wantAt := time.UnixMilli(st.PauseDeadline).Format("15:04")
	if tip != "暂停中 · 还有 29 分钟（到 "+wantAt+"）" {
		t.Fatalf("tooltip = %q", tip)
	}
	if status != "状态：暂停中（还有 29 分钟）" {
		t.Fatalf("状态行 = %q", status)
	}
	if pause != "恢复休息" {
		t.Fatalf("定时暂停也能手动恢复，暂停项 = %q", pause)
	}
}

// 定时暂停到“明天 0:00”（托盘菜单里的那一档）：跨天要写“明天”。
func TestTrayScenarioTimedPauseTomorrow(t *testing.T) {
	base := time.Date(2026, 11, 13, 22, 30, 0, 0, time.Local)
	now := base.UnixMilli()
	tomorrow := time.Date(2026, 11, 14, 0, 0, 0, 0, time.Local).UnixMilli()
	st := model.SchedulerState{Paused: true, PauseDeadline: tomorrow, PausedAt: now}

	tip, status, _, _ := trayOf(st, false, nil, now)
	if tip != "暂停中 · 还有 1 小时 30 分钟（到 明天 00:00）" {
		t.Fatalf("tooltip = %q", tip)
	}
	if status != "状态：暂停中（还有 1 小时 30 分钟）" {
		t.Fatalf("状态行 = %q", status)
	}
}

// 定时暂停到点了却被全屏 / 投屏挡住：点名在等什么，否则用户会以为没生效。
func TestTrayScenarioTimedPauseExpiredHeld(t *testing.T) {
	now := nowMs()
	st := model.SchedulerState{
		Paused: true, PauseDeadline: now - 5_000, PausedAt: now - 3600_000,
		PauseReasons: []string{"全屏独占"},
	}
	tip, status, action, pause := trayOf(st, false, []string{"全屏独占"}, now)
	if tip != "暂停时长已到，等“全屏独占”结束" {
		t.Fatalf("tooltip = %q", tip)
	}
	if status != "状态：暂停（时长已到）" {
		t.Fatalf("状态行 = %q", status)
	}
	if action != "立即休息" || pause != "恢复休息" {
		t.Fatalf("动作项 = %q / 暂停项 = %q", action, pause)
	}
}

// 定时暂停到点、也没被挡住（watch 还没跑起来的那一拍）：兜底文案。
func TestTrayScenarioTimedPauseExpired(t *testing.T) {
	now := nowMs()
	st := model.SchedulerState{Paused: true, PauseDeadline: now - 1_000, PausedAt: now - 600_000}
	tip, status, _, _ := trayOf(st, false, nil, now)
	if tip != "暂停时长已到" || status != "状态：暂停（时长已到）" {
		t.Fatalf("tooltip = %q / 状态行 = %q", tip, status)
	}
}

// 定时暂停期间同时“到点了在等空闲”：暂停分支优先，不能写成“等待空闲后提醒”。
// 这条分支顺序是刻意的（人既然暂停了，先说暂停），把它钉住免得将来调换。
func TestTrayScenarioTimedPauseBeatsIdleWait(t *testing.T) {
	now := nowMs()
	st := model.SchedulerState{
		Paused: true, PauseDeadline: now + 10*60_000, PausedAt: now - 60_000,
		Phase: "idle", NextType: "short", NextName: plan.ShortBreakName,
		NextAt: now - 1_000, IdleWait: true, PauseReasons: []string{"manual"},
	}
	tip, _, _, _ := trayOf(st, false, nil, now)
	if strings.Contains(tip, "等待空闲") {
		t.Fatalf("暂停态应说暂停，不该说等待空闲，实际 %q", tip)
	}
	if !strings.HasPrefix(tip, "暂停中 · 还有 10 分钟") {
		t.Fatalf("tooltip = %q", tip)
	}
}

// 自动暂停的原因都消失了、但用户还没点恢复：补一句“可以恢复了”。
// 自动暂停不再自己恢复（退出全屏常常只是切出去看一眼），托盘是唯一告诉他这件事的地方。
func TestTrayScenarioAutoPauseClearedReminder(t *testing.T) {
	now := nowMs()
	st := model.SchedulerState{
		Paused: true, PausedAt: now - 90*60_000, PauseReasons: []string{"全屏独占"},
	}
	tip, status, pause, _ := trayOf(st, true, nil, now)
	_ = pause
	if !strings.HasSuffix(tip, "（已结束，可以恢复）") {
		t.Fatalf("条件消失后要提示可以恢复，实际 %q", tip)
	}
	if status != "状态：已暂停（条件已结束）" {
		t.Fatalf("状态行 = %q", status)
	}

	// 还在生效时不能催
	tip2, status2, _, _ := trayOf(st, false, nil, now)
	if strings.Contains(tip2, "可以恢复") || status2 != "状态：已暂停" {
		t.Fatalf("条件仍生效时不该催恢复：tip=%q status=%q", tip2, status2)
	}
}

// 多个自动原因并存：按登记顺序拼接，用户才看得懂自己在等几件事。
func TestTrayScenarioMultiplePauseReasons(t *testing.T) {
	now := nowMs()
	st := model.SchedulerState{
		Paused: true, PausedAt: now - 2*3600_000,
		PauseReasons: []string{"idle", "勿扰"},
	}
	tip, _, _, _ := trayOf(st, false, nil, now)
	if want := "已暂停 2 小时：系统空闲、勿扰"; tip != want {
		t.Fatalf("tooltip = %q，期望 %q", tip, want)
	}
}

// 休息中的剩余时长跨小时：托盘里出现“90 分 30 秒”很难读，要换算成小时。
// 同时验证 now 可注入（trayTextsAt 的 break 分支曾误用包级 nowMs()）。
func TestTrayScenarioBreakRemainUsesInjectedNow(t *testing.T) {
	base := time.Date(2026, 11, 13, 10, 0, 0, 0, time.Local)
	now := base.UnixMilli()
	st := model.SchedulerState{
		Phase: "break", BreakStartedAt: now, BreakEndsAt: now + 90*60_000 + 30_000,
	}
	tip, status, action, pause := trayOf(st, false, nil, now)
	if want := "休息中，剩余 1 小时 30 分 30 秒"; tip != want {
		t.Fatalf("tooltip = %q，期望 %q", tip, want)
	}
	if status != "状态：休息中" || action != "提前结束休息" || pause != "暂停休息" {
		t.Fatalf("状态行 = %q / 动作项 = %q / 暂停项 = %q", status, action, pause)
	}
}

// 到点那一瞬间还没被处理（既没暂停、也没进休息、也不在等空闲）：按分钟约数说话，
// 不会出现“约 0 分钟后”这种看着像坏了的文案。
func TestTrayScenarioDueMomentSaysAboutOneMinute(t *testing.T) {
	now := nowMs()
	st := model.SchedulerState{
		Phase: "idle", NextType: "short", NextName: plan.ShortBreakName, NextAt: now - 400,
	}
	tip, status, _, _ := trayOf(st, false, nil, now)
	if tip != "约 1 分钟后短休息" {
		t.Fatalf("tooltip = %q", tip)
	}
	if status != "下次短休息：约 1 分钟后" {
		t.Fatalf("状态行 = %q", status)
	}
}

// 跨层：真调度器排出的快照直接喂托盘，验证“侧栏与托盘吃同一份状态”。
func TestTrayScenarioFromRealScheduler(t *testing.T) {
	js := `{"short":{"enabled":true,"mode":"countdown","intervalMs":600000,"durationMs":20000,` +
		`"notifyBeforeMs":0,"postponeMs":120000,"slots":[]}}`
	s, _ := newTestSched(t, js)
	s.RefreshPlan() // 导出包装（main 包碰不到未导出的 replan）

	st := s.StateForRenderer()
	tip, status, _, _ := trayTextsAt(st, false, nil, nowMs())
	if want := "约 10 分钟后短休息"; tip != want {
		t.Fatalf("tooltip = %q，期望 %q（侧栏状态快照 NextAt=%d）", tip, want, st.NextAt)
	}
	if want := "下次短休息：约 10 分钟后"; status != want {
		t.Fatalf("状态行 = %q，期望 %q", status, want)
	}

	// 暂停后同一份快照要翻成暂停态（原因中文映射也一起验）
	s.Pause("manual")
	st = s.StateForRenderer()
	tip, status, _, pause := trayTextsAt(st, false, nil, nowMs())
	if !strings.HasPrefix(tip, "已暂停 不到 1 分钟：手动暂停") {
		t.Fatalf("暂停后 tooltip = %q", tip)
	}
	if status != "状态：已暂停" || pause != "恢复休息" {
		t.Fatalf("状态行 = %q / 暂停项 = %q", status, pause)
	}
	// 暂停不冻钟：目标时刻本身不变
	if st.NextAt == 0 {
		t.Fatal("暂停不该把下一次清空")
	}
}
