package scheduler

import (
	"testing"
	"time"
	"workerrest/internal/model"
	"workerrest/internal/plan"
)

// 时间轴审计（全量组合）的回归用例。
//
// 这里只钉「时间状态机在各种组合下必须成立的不变量」，不重复 scheduler_test.go
// 已覆盖的单条规则。每条用例都对应一条曾经的（或极易写错的）交叉路径：
//   - 暂停 × replan：暂停期间的时间是冻结的，任何重排都不得改写剩余；
//   - 提前量 × 跨午夜：occurrence 归属「提醒真正发生的那一天」，不是「现在这一天」。
// 这两条都不是「算错一个数」，而是两套时间基准（真实墙钟 / 冻结时钟、now / now+提前量）
// 在同一处混用 —— 只靠读代码很难发现，所以用测试钉住。

// ---------- 暂停 × replan ----------

// TestPausedReplanKeepsFrozenRemain 暂停期间的一次 replan 不得把冻结的剩余时间推远。
//
// 触发路径（真实）：用户手动暂停 → 在设置页改任意选项（每次改动都会 SaveSettings →
// refreshPlan → scheduleNext → replan）→ 若 replan 用真实墙钟判断「倒计时锚点是否过期」，
// 冻结中的旧锚点会被判过期并按 now+interval 重新起算；恢复时 resume 又整体平移 delta，
// 等于把暂停时长算了两遍，用户看到「还剩 50 分钟」（配置是 20 分钟）。
func TestPausedReplanKeepsFrozenRemain(t *testing.T) {
	s, _ := newTestSched(t, shortCountdown) // 每 10 分钟一次、每次 20 秒
	s.replan()

	const pausedFor = 30 * 60_000
	const remainAtPause = 5 * 60_000
	s.mu.Lock()
	s.state.Paused = true
	s.state.PauseReasons = []string{"manual"}
	// 暂停发生在 30 分钟前；暂停那一刻距离下次休息还有 5 分钟
	s.state.PausedAt = nowMs() - pausedFor
	s.state.NextAt = s.state.PausedAt + remainAtPause
	for i := range s.state.Lines {
		s.state.Lines[i].At = s.state.NextAt
	}
	s.mu.Unlock()

	if got := s.RemainingMs(); got != remainAtPause {
		t.Fatalf("暂停中剩余应冻结在 %d ms，实际 %d ms", remainAtPause, got)
	}

	// 暂停期间改设置 → replan（不再经过 scheduleNext 也能覆盖同一条路径）
	s.replan()
	if got := s.RemainingMs(); got != remainAtPause {
		t.Fatalf("暂停期间 replan 不得改写冻结的剩余：want %d ms，got %d ms", remainAtPause, got)
	}

	// 恢复：只平移一次，剩余仍是 5 分钟
	s.Resume("manual")
	got := s.RemainingMs()
	if got < remainAtPause-2000 || got > remainAtPause+2000 {
		t.Fatalf("恢复后剩余应≈%d ms（只平移一次），实际 %d ms", remainAtPause, got)
	}
}

// TestPausedScheduleNextKeepsFrozenRemain 走完整链路（scheduleNext，设置保存用的就是它）
// 也必须保住冻结的剩余；同时 phase 要回到 idle（暂停时不该停在 pre）。
func TestPausedScheduleNextKeepsFrozenRemain(t *testing.T) {
	s, _ := newTestSched(t, shortCountdown)
	s.replan()

	const pausedFor = 40 * 60_000
	s.mu.Lock()
	s.state.Paused = true
	s.state.PauseReasons = []string{PauseReasonIdle} // 用真实键，确保 resume 走得到
	s.state.PausedAt = nowMs() - pausedFor
	s.state.NextAt = s.state.PausedAt + 3*60_000
	s.state.Phase = "pre"
	for i := range s.state.Lines {
		s.state.Lines[i].At = s.state.NextAt
	}
	s.mu.Unlock()

	s.scheduleNext()

	if st := s.StateCopy(); st.Phase != "idle" {
		t.Fatalf("重排后应回到等待态，实际 phase=%q", st.Phase)
	}
	if got := s.RemainingMs(); got != 3*60_000 {
		t.Fatalf("重排后冻结剩余应保持 3 分钟，实际 %d ms", got)
	}

	s.Resume(PauseReasonIdle)
	got := s.RemainingMs()
	if got < 3*60_000-2000 || got > 3*60_000+2000 {
		t.Fatalf("恢复后剩余应≈3 分钟，实际 %d ms", got)
	}
}

// ---------- 提前量 × 跨午夜 ----------

// TestSlotOccurrenceAcrossMidnight 提前量把提醒推到「次日零点后」时，occurrence 必须
// 归属次日 —— 否则去重键会与当天同一条目的旧键相撞（提前通知被吞），预通知窗的
// 倒计时也会指着已经过去的时刻（进窗即 0 秒）。
func TestSlotOccurrenceAcrossMidnight(t *testing.T) {
	slot := model.TimeSlot{ID: "s1", Hour: 0, Minute: 10, Enabled: true}
	lead := int64(20 * 60_000) // 提前 20 分钟 → 23:50 进入提前区间

	now := time.Date(2026, 10, 27, 23, 50, 0, 0, time.Local).UnixMilli()
	want := time.Date(2026, 10, 28, 0, 10, 0, 0, time.Local).UnixMilli()
	if got := plan.SlotOccurrenceAt(slot, lead, now); got != want {
		t.Fatalf("跨午夜 occurrence 应归属次日 %d，实际 %d（差 %d ms）", want, got, got-want)
	}

	// 不跨午夜的普通情形：仍归属当天
	now2 := time.Date(2026, 10, 27, 11, 50, 0, 0, time.Local).UnixMilli()
	slot2 := model.TimeSlot{ID: "s2", Hour: 12, Minute: 10, Enabled: true}
	want2 := time.Date(2026, 10, 27, 12, 10, 0, 0, time.Local).UnixMilli()
	if got := plan.SlotOccurrenceAt(slot2, lead, now2); got != want2 {
		t.Fatalf("同日 occurrence 应为 %d，实际 %d", want2, got)
	}

	// 提前通知与真窗必须落在同一个 occurrence 键上（否则去重表各记一份、互相吞或重复）
	if plan.OccurrenceKey("takeout", plan.SlotOccurrenceAt(slot, lead, now)) !=
		plan.OccurrenceKey("takeout", plan.SlotAlertAt(slot, 0, want)) {
		t.Fatalf("提前线与会话线应指向同一 occurrence")
	}
}

// TestSlotOccurrenceSameDayNoShift 提前量不跨天时，slotOccurrenceAt 与「原定时刻」一致，
// 避免修复跨午夜时把普通情形改错。
func TestSlotOccurrenceSameDayNoShift(t *testing.T) {
	now := time.Date(2026, 10, 27, 9, 0, 0, 0, time.Local).UnixMilli()
	slot := model.TimeSlot{ID: "s3", Hour: 9, Minute: 30, Enabled: true}
	got := plan.SlotOccurrenceAt(slot, 10*60_000, now)
	want := time.Date(2026, 10, 27, 9, 30, 0, 0, time.Local).UnixMilli()
	if got != want {
		t.Fatalf("普通情形 occurrence 应为 %d，实际 %d", want, got)
	}
}

// TestTakeoutPreNotifyTargetNotInPast 预通知窗的倒计时目标必须是「即将到来的那一次」。
// 23:50 弹次日 00:10 的预通知，若 targetAt 按"今天 00:10"算，进窗就是 0 秒。
func TestTakeoutPreNotifyTargetNotInPast(t *testing.T) {
	slot := model.TimeSlot{ID: "s4", Hour: 0, Minute: 5, Enabled: true}
	now := time.Date(2026, 10, 27, 23, 58, 0, 0, time.Local).UnixMilli()
	target := plan.SlotOccurrenceAt(slot, 7*60_000, now)
	if target <= now {
		t.Fatalf("预通知目标必须晚于现在：now=%d target=%d", now, target)
	}
	if remain := target - now; remain != 7*60_000 {
		t.Fatalf("倒计时应为 7 分钟，实际 %d ms", remain)
	}
}

// ---------- 暂停 × 立即休息 ----------

// TestBreakStartedWhilePausedStillCloses 暂停态点「立即休息」之后，内置休息必须照常
// 到点收窗。
//
// 触发路径（真实）：托盘右键 →「暂停休息」→「立即休息」。休息在暂停开始之后发起，
// 若到点判定仍按「暂停则冻结」，onTick 的暂停分支永远收不了窗 —— 长休息窗口会一直挂在
// 屏幕上，直到用户想起来去恢复暂停。
func TestBreakStartedWhilePausedStillCloses(t *testing.T) {
	s, _ := newTestSched(t, shortCountdown)
	s.replan()
	s.Pause("manual")
	if id := s.BreakNowOf("short"); id == "" {
		t.Fatal("暂停中「立即休息」应当能开始一次休息")
	}
	if got := s.StateCopy().Phase; got != "break" {
		t.Fatalf("应进入休息态，实际 %q", got)
	}
	// 把这次休息的结束时刻挪到 2 秒前（越过 1 秒停留）
	s.mu.Lock()
	s.state.BreakEndsAt = nowMs() - 2000
	s.mu.Unlock()
	s.onTick()
	if got := s.StateCopy().Phase; got == "break" {
		t.Fatalf("暂停中主动开始的休息到点必须自动收窗，实际仍在 %q", got)
	}
}

// TestBreakBeforePauseStaysFrozen 暂停之前就在进行的休息仍要冻结：到点也不收窗，
// 恢复时整体平移（短暂暂停不吞用户的休息时间）。
func TestBreakBeforePauseStaysFrozen(t *testing.T) {
	s, _ := newTestSched(t, shortCountdown)
	s.replan()
	if id := s.BreakNowOf("short"); id == "" {
		t.Fatal("应能开始一次休息")
	}
	s.Pause("manual")
	s.mu.Lock()
	s.state.BreakEndsAt = nowMs() - 2000
	s.mu.Unlock()
	s.onTick()
	if got := s.StateCopy().Phase; got != "break" {
		t.Fatalf("暂停前开始的休息应保持冻结（不收窗），实际 %q", got)
	}
}

// TestRefreshPlanKeepsPrePhase 保存设置触发的重排不得把「即将休息」打回等待态。
// 否则每改一个输入框，右下角窗口都会关掉再弹一遍（500ms 后），肉眼可见一次闪烁。
func TestRefreshPlanKeepsPrePhase(t *testing.T) {
	s, _ := newTestSched(t, shortCountdown) // 提前量 10 秒
	s.replan()
	// 造出「已经进入提前区间」：距下次 5 秒
	s.mu.Lock()
	s.state.NextAt = nowMs() + 5000
	for i := range s.state.Lines {
		s.state.Lines[i].At = s.state.NextAt
		s.anchorIv[s.state.Lines[i].Type] = 600_000
	}
	s.state.Phase = "pre"
	s.mu.Unlock()

	s.RefreshPlan()
	if got := s.StateCopy().Phase; got != "pre" {
		t.Fatalf("同一提醒仍在提前区间，phase 应保持 pre，实际 %q", got)
	}
	if !s.PreBreakWanted() {
		t.Fatal("窗口应仍在场（判据成立）")
	}
}

// TestRefreshPlanClosesWhenReminderSwitched 重排后换成了另一条提醒（例如跳过当前这条），
// 不能保留旧的 pre 阶段：窗口内容会与新提醒对不上，必须收窗由下一跳重弹。
func TestRefreshPlanClosesWhenReminderSwitched(t *testing.T) {
	s, _ := newTestSched(t, `{"short":{"enabled":true,"mode":"countdown","intervalMs":600000,`+
		`"durationMs":20000,"notifyBeforeMs":10000,"postponeMs":120000,"slots":[]},`+
		`"long":{"enabled":true,"mode":"countdown","intervalMs":600000,`+
		`"durationMs":60000,"notifyBeforeMs":10000,"postponeMs":120000,"slots":[]},`+
		`"longEveryNShort":1}`)
	s.replan()
	// 当前是短休息、已在提前区间
	s.mu.Lock()
	s.state.NextAt = nowMs() + 5000
	for i := range s.state.Lines {
		s.state.Lines[i].At = s.state.NextAt
	}
	s.state.Phase = "pre"
	// 计数已够 → replan 会排长休息（换了一条）
	s.state.ShortsSinceLong = 1
	s.mu.Unlock()

	s.RefreshPlan()
	st := s.StateCopy()
	if st.Phase == "pre" && st.NextType != "short" {
		t.Fatalf("换成了另一条提醒后不应保留旧 pre（窗口内容会错配），实际 next=%q phase=%q", st.NextType, st.Phase)
	}
}

// TestTickJumpResetsInsteadOfFiring 睡眠 / 休眠醒来后，不要在醒来第一刻补弹一个
// 「已过期」的休息窗口：应当重排，让倒计时从头计时。
//
// 真实场景：合盖前倒计时还剩 3 分钟，合盖 8 小时。醒来第一跳若照常判 due，
// 用户刚打开电脑就被一个 5 分钟长休息窗口糊脸。
func TestTickJumpResetsInsteadOfFiring(t *testing.T) {
	s, _ := newTestSched(t, shortCountdown) // 每 10 分钟
	s.replan()
	// 伪造「上一次 tick 在 20 分钟前」：倒计时早已过期
	s.mu.Lock()
	s.lastTickAt = nowMs() - 20*60_000
	s.state.NextAt = nowMs() - 60_000
	for i := range s.state.Lines {
		s.state.Lines[i].At = s.state.NextAt
	}
	s.mu.Unlock()

	s.onTick()
	st := s.StateCopy()
	if st.Phase == "break" {
		t.Fatal("时间大跳后不应立刻弹休息窗口")
	}
	if want := nowMs() + 600_000; st.NextAt < want-5000 || st.NextAt > want+5000 {
		t.Fatalf("倒计时应重新从头计时（≈%d），实际 %d", want, st.NextAt)
	}
}

// TestTickNormalGapStillFires 正常节奏（500ms 一跳）不受大跳处理影响：到点照常开始休息。
func TestTickNormalGapStillFires(t *testing.T) {
	s, _ := newTestSched(t, shortCountdown)
	s.replan()
	s.onTick() // 记下 lastTickAt
	s.mu.Lock()
	s.state.NextAt = nowMs() - 10
	for i := range s.state.Lines {
		s.state.Lines[i].At = s.state.NextAt
	}
	s.mu.Unlock()
	s.onTick()
	if got := s.StateCopy().Phase; got != "break" {
		t.Fatalf("正常间隔下的到点应开始休息，实际 %q", got)
	}
}
