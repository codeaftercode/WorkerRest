package scheduler

// 侧栏那组按钮的**目标口径**（2026-11-14 用户口径）：
//
//	立即休息 / 暂停休息 / 推迟休息 / 跳过本次 / 重置周期
//
// 全部只针对「下一次短休息 / 长休息」（两者取最近的那一个），**外卖提醒与自定义提醒
// 不参与**。调度器的全局 next（NextType / NextAt）可能是外卖或自定义提醒，侧栏按钮
// 如果拿它当目标，点一次「推迟休息」就会把本该准点响的喝水提醒推走 —— 而短休息一动
// 没动。这份文件钉的就是“推的是谁、没推谁”。
//
// 暂停 / 恢复是全局开关（暂停的是所有提醒，不针对某一条），这里不需要用例。

import (
	"testing"
	"time"
	"workerrest/internal/plan"
)

// waterAndShort 短休息 10 分钟后到（可推 2 分钟）+ 喝水提醒 3 分钟后到（可推 5 分钟）。
// 全局 next 是喝水提醒，短休息排在后面 —— 正好是“侧栏按钮推错目标”的高危组合。
const waterAndShort = `{` + `"short":{"enabled":true,"mode":"countdown","intervalMs":600000,` +
	`"durationMs":20000,"notifyBeforeMs":0,"postponeMs":120000,"pauseAction":"postpone","slots":[]},` +
	`"customBreaks":[{"id":"cb-water","name":"喝水提醒","config":{"enabled":true,"mode":"countdown",` +
	`"intervalMs":180000,"durationMs":20000,"notifyBeforeMs":0,"postponeMs":300000,` +
	`"pauseAction":"postpone","slots":[]}}]}`

// lineAt 取某身份挂起线的时刻（没有就失败）
func lineAt(t *testing.T, s *Scheduler, kind string) int64 {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, l := range s.state.Lines {
		if l.Type == kind {
			return l.At
		}
	}
	t.Fatalf("挂起线里没有 %s：%+v", kind, s.state.Lines)
	return 0
}

// 「推迟休息」推的是短休息，喝水提醒原样不动。
func TestSidebarPostponeTargetsBuiltinRest(t *testing.T) {
	s, host := newTestSched(t, waterAndShort)
	s.replan()
	waterBefore := lineAt(t, s, "cb-water")
	shortBefore := lineAt(t, s, "short")
	if shortBefore <= waterBefore {
		t.Fatalf("前置：喝水提醒应排在短休息之前（%d vs %d）", waterBefore, shortBefore)
	}

	s.PostponeRest()

	if len(host.postpones) != 1 || host.postpones[0] != "short" {
		t.Fatalf("「推迟休息」只该推短 / 长休息，实际记了 %v", host.postpones)
	}
	if got := lineAt(t, s, "cb-water"); got != waterBefore {
		t.Fatalf("喝水提醒不该被侧栏按钮推走：%s → %s",
			time.UnixMilli(waterBefore).Format("15:04:05"), time.UnixMilli(got).Format("15:04:05"))
	}
	near(t, lineAt(t, s, "short"), shortBefore+120_000, 2000, "短休息应往后推 2 分钟")

	st := rx(s)
	if st.NextType != "cb-water" {
		t.Fatalf("全局 next 仍是喝水提醒（它更早），实际 %q", st.NextType)
	}
	if st.RestKind != "short" {
		t.Fatalf("侧栏「下次休息」那一格应是短休息，实际 %q", st.RestKind)
	}
	if !st.PostponeAllowed {
		t.Fatal("短休息勾了「允许推迟提醒」，按钮该是可用的")
	}
}

// 「跳过本次」跳的是短休息，喝水提醒照常留着。
func TestSidebarSkipTargetsBuiltinRest(t *testing.T) {
	s, host := newTestSched(t, waterAndShort)
	s.replan()
	waterBefore := lineAt(t, s, "cb-water")
	shortBefore := lineAt(t, s, "short")

	// 跳过会把这一跳的 occurrence 记进 consumed，重排后新的一跳是 now+间隔；同一毫秒内
	// 两者会算出同一个键而被 filterConsumed 剔掉（500ms 后的下一拍自然补回）。
	// 挪开一毫秒，让“新起算的那一跳”与“被跳过的那一跳”不是同一刻。
	time.Sleep(3 * time.Millisecond)
	s.SkipRest()

	if len(host.skips) != 1 || host.skips[0] != "short" {
		t.Fatalf("「跳过本次」只该跳短 / 长休息，实际记了 %v", host.skips)
	}
	if got := lineAt(t, s, "cb-water"); got != waterBefore {
		t.Fatalf("喝水提醒不该被跳过：%s → %s",
			time.UnixMilli(waterBefore).Format("15:04:05"), time.UnixMilli(got).Format("15:04:05"))
	}
	// 被跳过的那一跳已消费，短休息按新间隔重新起算
	if got := lineAt(t, s, "short"); got <= shortBefore {
		t.Fatalf("短休息跳过之后应重新起算（>%s），实际 %s",
			time.UnixMilli(shortBefore).Format("15:04:05"), time.UnixMilli(got).Format("15:04:05"))
	}
	if got := s.StateCopy().ShortsSinceLong; got != 1 {
		t.Fatalf("跳过一次短休息后计数应 +1，实际 %d", got)
	}
	if st := rx(s); st.NextType != "cb-water" {
		t.Fatalf("全局 next 仍是喝水提醒，实际 %q", st.NextType)
	}
}

// 只开了自定义提醒（没有短 / 长休息）时，侧栏那组按钮没有目标：不动任何东西。
func TestSidebarActionsNoBuiltinAreNoOps(t *testing.T) {
	// 短 / 长两条要显式关掉：不写就取出厂默认（都启用），也就谈不上“没有目标”
	js := `{"short":{"enabled":false},"long":{"enabled":false},` +
		`"customBreaks":[{"id":"cb-water","name":"喝水提醒","config":{"enabled":true,` +
		`"mode":"countdown","intervalMs":180000,"durationMs":20000,"notifyBeforeMs":0,` +
		`"postponeMs":300000,"pauseAction":"postpone","slots":[]}}]}`
	s, host := newTestSched(t, js)
	s.replan()
	waterBefore := lineAt(t, s, "cb-water")
	if _, _, ok := s.RestTarget(); ok {
		t.Fatal("只开了自定义提醒时不该有短 / 长休息目标")
	}

	s.PostponeRest()
	s.SkipRest()

	if len(host.postpones) != 0 || len(host.skips) != 0 {
		t.Fatalf("没有短 / 长休息时不该记账：postpones=%v skips=%v", host.postpones, host.skips)
	}
	if got := lineAt(t, s, "cb-water"); got != waterBefore {
		t.Fatal("没有短 / 长休息时，自定义提醒的线不该被改动")
	}
	if st := rx(s); st.PostponeAllowed || st.RestKind != "" {
		t.Fatalf("没有短 / 长休息时侧栏该按“不适用”处理：allowed=%v kind=%q",
			st.PostponeAllowed, st.RestKind)
	}
}

// 「重置周期」只把短 / 长那一轮从头算：自定义提醒的倒计时进度、推迟待办原样保留。
func TestResetCycleKeepsCustomReminders(t *testing.T) {
	s, _ := newTestSched(t, waterAndShort)
	s.replan()
	// 造“都走过一段”的现场：短休息还剩 1 分钟、喝水提醒还剩 30 秒
	now := nowMs()
	s.mu.Lock()
	for i := range s.state.Lines {
		switch s.state.Lines[i].Type {
		case "short":
			s.state.Lines[i].At = now + 60_000
		case "cb-water":
			s.state.Lines[i].At = now + 30_000
		}
	}
	// 喝水提醒挂一条“改到 5 分钟后再弹”的推迟待办，短休息也挂一条（重置时只该清后者）
	s.postponed[plan.OccurrenceKey("cb-water", now+30_000)] =
		&postponedBreak{kind: "cb-water", at: now + 30_000, dueAt: now + 300_000}
	s.postponed[plan.OccurrenceKey("short", now+60_000)] =
		&postponedBreak{kind: "short", at: now + 60_000, dueAt: now + 240_000}
	s.mu.Unlock()
	s.replan()

	s.ResetCycle()

	near(t, lineAt(t, s, "cb-water"), now+30_000, 2000, "自定义提醒的倒计时进度应原样保留")
	near(t, lineAt(t, s, "short"), now+600_000, 2000, "短休息应从头计时（当前间隔）")
	if got := s.StateCopy().ShortsSinceLong; got != 0 {
		t.Fatalf("重置周期后短休息计数应归零，实际 %d", got)
	}
	s.mu.Lock()
	_, waterPostponed := s.postponed[plan.OccurrenceKey("cb-water", now+30_000)]
	_, shortPostponed := s.postponed[plan.OccurrenceKey("short", now+60_000)]
	s.mu.Unlock()
	if !waterPostponed {
		t.Fatal("自定义提醒的推迟待办不该被「重置周期」撤掉")
	}
	if shortPostponed {
		t.Fatal("短 / 长休息的推迟待办该随重置一起撤掉")
	}
}

// 「推迟休息」推的是短休息时，喝水提醒那扇**已经在场的提前提示窗**不能被顺手收掉
// （它没被推迟，收掉等于替用户取消了一次提醒，500ms 后又被 onTick 弹回来 = 闪一下）。
func TestSidebarPostponeKeepsOtherPreWindow(t *testing.T) {
	js := `{` + `"short":{"enabled":true,"mode":"countdown","intervalMs":600000,` +
		`"durationMs":20000,"notifyBeforeMs":0,"postponeMs":120000,"pauseAction":"postpone","slots":[]},` +
		`"customBreaks":[{"id":"cb-water","name":"喝水提醒","config":{"enabled":true,"mode":"countdown",` +
		`"intervalMs":120000,"durationMs":20000,"notifyBeforeMs":60000,` +
		`"postponeMs":300000,"pauseAction":"postpone","slots":[]}}]}`
	s, host := newTestSched(t, js)
	s.replan()
	// 把喝水提醒挪进提前提示区间，相位按 onTick 的结果摆好（phase=pre）
	s.mu.Lock()
	for i := range s.state.Lines {
		if s.state.Lines[i].Type == "cb-water" {
			s.state.Lines[i].At = nowMs() + 30_000
		}
	}
	s.state.NextType, s.state.NextAt = "cb-water", nowMs()+30_000
	s.state.Phase = "pre"
	s.mu.Unlock()

	s.PostponeRest()

	if len(host.postpones) != 1 || host.postpones[0] != "short" {
		t.Fatalf("推迟的应是短休息，实际 %v", host.postpones)
	}
	if got := s.CurrentPhase(); got != "pre" {
		t.Fatalf("喝水提醒的提前提示窗不该被收掉（phase=%q）", got)
	}
	near(t, lineAt(t, s, "cb-water"), nowMs()+30_000, 3000, "喝水提醒的时刻不该被动")
}

// 「推迟休息」的显隐判据是短 / 长休息那一条，不是全局 next：
// 喝水提醒不允许推迟，也不能让下一条短休息的推迟按钮消失。
func TestSidebarPostponeAllowedFollowsBuiltinRest(t *testing.T) {
	js := `{` + `"short":{"enabled":true,"mode":"countdown","intervalMs":600000,` +
		`"durationMs":20000,"notifyBeforeMs":0,"postponeMs":120000,"pauseAction":"postpone","slots":[]},` +
		`"customBreaks":[{"id":"cb-water","name":"喝水提醒","config":{"enabled":true,"mode":"countdown",` +
		`"intervalMs":180000,"durationMs":20000,"notifyBeforeMs":0,"postponeMs":0,` +
		`"pauseAction":"skip","slots":[]}}]}`
	s, _ := newTestSched(t, js)
	s.replan()
	st := rx(s)
	if st.NextType != "cb-water" {
		t.Fatalf("前置：全局 next 应是喝水提醒，实际 %q", st.NextType)
	}
	if !st.PostponeAllowed || st.RestKind != "short" {
		t.Fatalf("判据应看短休息（允许推迟）：allowed=%v kind=%q", st.PostponeAllowed, st.RestKind)
	}

	// 反向：短休息自己不允许推迟 → 按钮该藏
	js2 := `{"short":{"enabled":true,"mode":"countdown","intervalMs":600000,` +
		`"durationMs":20000,"notifyBeforeMs":0,"postponeMs":0,"pauseAction":"skip","slots":[]}}`
	s2, _ := newTestSched(t, js2)
	s2.replan()
	if st2 := rx(s2); st2.PostponeAllowed {
		t.Fatal("短休息没勾「允许推迟提醒」时按钮该藏起来")
	}
}
