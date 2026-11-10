package scheduler

import (
	"testing"
	"workerrest/internal/model"
	"workerrest/internal/plan"
)

// 空闲门（“鼠标键盘连续无活动 x 时间”才提醒）的规则回归。逐条对应需求原话：
//
//	勾上之后到点不马上弹 → 侧栏写“等待空闲” → 键鼠连续无活动达到 x 才继续：
//	      先弹提前提示窗，提示窗结束后弹正式提醒窗；
//	      提前通知为 0 秒 → 达标即弹正式提醒窗；
//	      在提示窗里点了“推迟” → 按“可推迟”倒计时，倒计时结束后**重新**按空闲门判断。
//
// 这里只跑状态机（假宿主），窗口与前端由 GUI 探针覆盖。

// shortGated 短休息：10 分钟一次、20 秒、提前 10 秒提示，空闲门 30 秒。
const shortGated = `{"short":{"enabled":true,"mode":"countdown","intervalMs":600000,` +
	`"durationMs":20000,"notifyBeforeMs":10000,"postponeMs":120000,` +
	`"idleGate":true,"idleGateMs":30000,"slots":[]}}`

// forceDue 把当前计划挪到“已到点”（单测里不真的等 10 分钟）。返回挪到的那一刻。
func forceDue(s *Scheduler) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := &s.state
	at := nowMs() - 1_000
	old := st.NextAt
	kind := st.NextType
	st.NextAt = at
	st.Phase = "idle"
	for i := range st.Lines {
		if st.Lines[i].Type == kind && st.Lines[i].At == old {
			st.Lines[i].At = at
		}
	}
	if st.Rest != nil && st.Rest.At == old {
		st.Rest = &model.PlanRef{Name: st.Rest.Name, At: at}
	}
	if st.Other != nil && st.Other.At == old {
		st.Other = &model.PlanRef{Name: st.Other.Name, At: at}
	}
	return at
}

// 到点了但键鼠无活动还不够：停在“等待空闲”，不弹任何窗，也不消费这一跳。
func TestIdleGateWaitsWhenIdleTooShort(t *testing.T) {
	s, host := newTestSched(t, shortGated)
	s.replan()
	at := forceDue(s)
	host.idleMs = 10_000 // 门槛 30 秒，才走了 10 秒

	s.onTick()

	st := s.StateCopy()
	if !st.IdleWait {
		t.Fatalf("空闲不足时应停在等待态，得到 %+v", st)
	}
	if st.Rest == nil || !st.Rest.IdleWait {
		t.Fatalf("内置休息那一格应标成“等待空闲”，得到 %+v", st.Rest)
	}
	if st.NextAt != at {
		t.Fatalf("等待中 NextAt 必须留在过去（%d），得到 %d", at, st.NextAt)
	}
	if len(host.breaks) != 0 || len(host.pre) != 0 {
		t.Fatalf("等待中不该弹任何窗，breaks=%v pre=%v", host.breaks, host.pre)
	}
	if !s.IdleWaitPending() {
		t.Fatal("IdleWaitPending 应报 true")
	}
	// 等待是同一个条件，逐拍重复推送会把设置页刷成动画：只推一次
	pushed := host.states
	s.onTick()
	if host.states != pushed {
		t.Fatalf("同一段等待里不该重复推状态，多推了 %d 次", host.states-pushed)
	}
}

// 空闲达标：把这一跳重排成“从现在起算提前提示”，再走原来的提示窗 → 正式窗链路。
func TestIdleGateReachedArmsPreNotify(t *testing.T) {
	s, host := newTestSched(t, shortGated)
	s.replan()
	forceDue(s)
	host.idleMs = 10_000
	s.onTick() // 先进等待

	host.idleMs = 30_000
	before := nowMs()
	s.onTick() // 达标

	st := s.StateCopy()
	want := before + 10_000 // 提前量 10 秒
	if diff := st.NextAt - want; diff < -2_000 || diff > 2_000 {
		t.Fatalf("达标后应重排到≈现在+提前量，want=%d got=%d（差 %d ms）", want, st.NextAt, diff)
	}
	if st.IdleWait || s.IdleWaitPending() {
		t.Fatalf("达标后不该再标“等待空闲”，得到 %+v", st)
	}
	if len(host.pre) != 0 {
		t.Fatalf("重排那一拍不该直接弹提示窗（下一拍才进提前区间），pre=%v", host.pre)
	}

	// 下一拍：落在提前区间内 → 弹提前提示窗
	s.onTick()
	if len(host.pre) != 1 || host.pre[0] != "short" {
		t.Fatalf("应弹出 1 次提前提示，得到 %v", host.pre)
	}
	if !s.PreBreakWanted() {
		t.Fatal("提前提示窗此刻应在场")
	}
	if len(host.breaks) != 0 {
		t.Fatalf("提示阶段不该弹正式窗，得到 %v", host.breaks)
	}
}

// 提前通知为 0 秒：跳过提示窗，空闲达标即弹正式提醒窗。
func TestIdleGateZeroNotifyFiresBreakDirectly(t *testing.T) {
	cfg := `{"short":{"enabled":true,"mode":"countdown","intervalMs":600000,` +
		`"durationMs":20000,"notifyBeforeMs":0,"postponeMs":120000,` +
		`"idleGate":true,"idleGateMs":30000,"slots":[]}}`
	s, host := newTestSched(t, cfg)
	s.replan()
	forceDue(s)
	host.idleMs = 10_000
	s.onTick()
	if len(host.breaks) != 0 {
		t.Fatalf("空闲不足时不该弹窗，得到 %v", host.breaks)
	}

	host.idleMs = 45_000
	s.onTick()
	if len(host.breaks) != 1 || host.breaks[0] != "short" {
		t.Fatalf("提前通知为 0 时应达标即弹正式窗，得到 %v", host.breaks)
	}
	if len(host.pre) != 0 {
		t.Fatalf("提前通知为 0 时不该有提示窗，得到 %v", host.pre)
	}
}

// 提示窗里点了“推迟”：按可推迟倒计时，倒计时结束后重新按空闲门判断。
func TestIdleGateRechecksAfterPostpone(t *testing.T) {
	s, host := newTestSched(t, shortGated)
	s.replan()
	forceDue(s)
	host.idleMs = 10_000
	s.onTick() // 等待
	host.idleMs = 30_000
	s.onTick() // 达标 → 重排到 now+10s
	s.onTick() // 进提前区间 → 弹提示窗
	if len(host.pre) != 1 {
		t.Fatalf("前置：应已弹出提前提示，得到 %v", host.pre)
	}

	s.Postpone() // 用户在提示窗里点“推迟”（可推迟 2 分钟）
	if s.IdleWaitPending() {
		t.Fatal("推迟之后不该还在等空闲")
	}
	at := forceDue(s) // 模拟 2 分钟过去
	host.idleMs = 5_000
	s.onTick()

	st := s.StateCopy()
	if !st.IdleWait {
		t.Fatalf("推迟到点后应重新等空闲，得到 %+v", st)
	}
	if st.NextAt != at {
		t.Fatalf("重新等待的应是推迟后的那一跳（%d），得到 %d", at, st.NextAt)
	}
	if len(host.breaks) != 0 {
		t.Fatalf("重新等待期间不该弹窗，得到 %v", host.breaks)
	}
	if len(host.postpones) != 1 {
		t.Fatalf("应记 1 次推迟，得到 %v", host.postpones)
	}
}

// 等待中的那一跳时刻在过去：任何一次 replan（每次保存设置都会跑）都得把它补回来，
// 否则用户随手改个别的设置，“等待空闲”就凭空消失。
func TestIdleGateSurvivesReplan(t *testing.T) {
	s, host := newTestSched(t, shortGated)
	s.replan()
	at := forceDue(s)
	host.idleMs = 10_000
	s.onTick()

	s.replan()

	st := s.StateCopy()
	if !st.IdleWait || !s.IdleWaitPending() {
		t.Fatalf("replan 之后应仍在等空闲，得到 %+v", st)
	}
	if st.NextAt != at {
		t.Fatalf("replan 之后 NextAt 应仍是原时刻 %d，得到 %d", at, st.NextAt)
	}
	if len(st.Lines) == 0 || st.Lines[0].Type != "short" || st.Lines[0].At != at {
		t.Fatalf("挂起线首条应是等待中的那一跳，得到 %+v", st.Lines)
	}
	if st.Rest == nil || !st.Rest.IdleWait {
		t.Fatalf("分组视图仍应标“等待空闲”，得到 %+v", st.Rest)
	}
}

// 跳过：等待作废，按新周期从头排。
func TestIdleGateClearedBySkip(t *testing.T) {
	s, host := newTestSched(t, shortGated)
	s.replan()
	forceDue(s)
	host.idleMs = 10_000
	s.onTick()

	s.Skip()

	st := s.StateCopy()
	if st.IdleWait || s.IdleWaitPending() {
		t.Fatalf("跳过之后不该还在等空闲，得到 %+v", st)
	}
	if len(host.skips) != 1 {
		t.Fatalf("应记 1 次跳过，得到 %v", host.skips)
	}
	if st.NextAt <= nowMs() {
		t.Fatalf("跳过之后应重排出未来的一跳，得到 %d", st.NextAt)
	}
}

// 系统空闲自动暂停撞上空闲门：人已经离开工位达到设定时长，正是这次提醒要等的那一刻，
// 暂停不该拦它 —— 直接弹正式提醒窗（提示窗刚被清场收掉，弹出来也会被自愈判据收走）。
func TestIdleGateFiresDuringIdleAutoPause(t *testing.T) {
	s, host := newTestSched(t, shortGated)
	s.replan()
	forceDue(s)
	host.idleMs = 10_000
	s.onTick()

	s.Pause(PauseReasonIdle)
	host.idleMs = 30_000
	s.onTick()

	if len(host.breaks) != 1 || host.breaks[0] != "short" {
		t.Fatalf("空闲自动暂停中达标应直接弹正式窗，得到 %v", host.breaks)
	}
	if s.IdleWaitPending() {
		t.Fatal("弹过之后不该还在等空闲")
	}
}

// 手动暂停仍然拦着：那不是“用户离开了”，是他明确说了现在别打扰。
func TestIdleGateHeldByManualPause(t *testing.T) {
	s, host := newTestSched(t, shortGated)
	s.replan()
	forceDue(s)
	host.idleMs = 10_000
	s.onTick()

	s.Pause(PauseReasonManual)
	host.idleMs = 600_000 // 门前门后都够长也不放行
	s.onTick()

	if len(host.breaks) != 0 {
		t.Fatalf("手动暂停期间不该弹窗，得到 %v", host.breaks)
	}
	if !s.IdleWaitPending() {
		t.Fatal("手动暂停不该把等待清掉（恢复后还要接着等）")
	}
}

// 手动暂停 → 恢复：等待中的那一跳**已经到点**，恢复不能按暂停时长把它往后平移。
// 平移会把它推进提前提示区间，人还没离开工位就先弹一个提示窗。
func TestIdleGateSurvivesManualPauseResume(t *testing.T) {
	s, host := newTestSched(t, shortGated)
	s.replan()
	at := forceDue(s)
	host.idleMs = 10_000
	s.onTick()

	s.Pause(PauseReasonManual)
	// 假装暂停了 30 秒（真睡 30 秒没必要）：没有“等待中不平移”这条，恢复会把这一跳
	// 往后推 30 秒，NextAt 立刻不再是 at
	s.mu.Lock()
	s.state.PausedAt -= 30_000
	s.mu.Unlock()
	host.idleMs = 0
	s.Resume(PauseReasonManual)

	st := s.StateCopy()
	if !st.IdleWait || !s.IdleWaitPending() {
		t.Fatalf("恢复后应继续等空闲，得到 %+v", st)
	}
	if st.NextAt != at {
		t.Fatalf("恢复不该平移等待中的那一跳：want=%d got=%d", at, st.NextAt)
	}
	if st.Phase == "pre" || len(host.pre) != 0 {
		t.Fatalf("恢复不该让提示窗冒出来，phase=%q pre=%v", st.Phase, host.pre)
	}
}

// 0 分 0 秒 = 没启用（勾着也一样）：到点就照老规矩走，不等人离开。
func TestIdleGateZeroDurationIsInert(t *testing.T) {
	cfg := `{"short":{"enabled":true,"mode":"countdown","intervalMs":600000,` +
		`"durationMs":20000,"notifyBeforeMs":0,"postponeMs":120000,` +
		`"idleGate":true,"idleGateMs":0,"slots":[]}}`
	s, host := newTestSched(t, cfg)
	s.replan()
	forceDue(s)
	host.idleMs = 0
	s.onTick()

	if len(host.breaks) != 1 {
		t.Fatalf("空闲门时长 0 应等同未启用，到点直接弹窗，得到 %v", host.breaks)
	}
	if s.IdleWaitPending() {
		t.Fatal("不该进入等待态")
	}
}

// 自定义提醒归另一格：状态卡上“下次外卖 / 自定义提醒”那一格才写“等待空闲”。
func TestIdleGateMarksCustomBreakCell(t *testing.T) {
	cfg := `{"short":{"enabled":false},"long":{"enabled":false},` +
		`"customBreaks":[{"id":"cb-water","name":"喝水提醒","config":{` +
		`"enabled":true,"mode":"countdown","intervalMs":1800000,"durationMs":20000,` +
		`"notifyBeforeMs":10000,"postponeMs":60000,"idleGate":true,"idleGateMs":30000,` +
		`"slots":[]}}]}`
	s, host := newTestSched(t, cfg)
	s.replan()
	if got := s.StateCopy().NextType; got != "cb-water" {
		t.Fatalf("前置：下一个应是自定义提醒，得到 %q", got)
	}
	forceDue(s)
	host.idleMs = 0
	s.onTick()

	st := s.StateCopy()
	if !st.IdleWait {
		t.Fatalf("自定义提醒到点也应等空闲，得到 %+v", st)
	}
	if st.Other == nil || !st.Other.IdleWait || st.Other.Name != "喝水提醒" {
		t.Fatalf("“下次外卖 / 自定义提醒”那一格应标等待空闲，得到 %+v", st.Other)
	}
	if st.Rest != nil {
		t.Fatalf("内置休息都关了，不该有 rest 那一格，得到 %+v", st.Rest)
	}
	if len(host.breaks) != 0 {
		t.Fatalf("等待期间不该弹窗，得到 %v", host.breaks)
	}
}

// 外卖不走调度器，但它和自定义提醒共用一个格子：等空闲由宿主报给快照，
// 状态卡上那一格写“等待空闲”（而不是“已到点 0 秒”）。
func TestStateForRendererMarksTakeoutIdleWait(t *testing.T) {
	cfg := `{"short":{"enabled":false},"long":{"enabled":false},` +
		`"takeout":{"enabled":true,"leadSec":60,"slots":[` +
		`{"id":"s1","hour":12,"minute":30,"repeat":"daily","days":[],"enabled":true}]}}`
	s, host := newTestSched(t, cfg)
	s.replan()
	at := nowMs() - 1_000
	host.gated = &model.TakeoutIdleWait{
		Slot: model.TimeSlot{ID: "s1", Hour: 12, Minute: 30, Repeat: "daily", Enabled: true},
		At:   at,
	}

	st := s.StateForRenderer()
	if st.Other == nil || !st.Other.IdleWait {
		t.Fatalf("外卖等空闲时那一格应标 IdleWait，得到 %+v", st.Other)
	}
	if st.Other.Name != plan.TAKEOUT_NAME || st.Other.At != at {
		t.Fatalf("那一格应是外卖提醒、时刻为原定时刻 %d，得到 %+v", at, st.Other)
	}
}
