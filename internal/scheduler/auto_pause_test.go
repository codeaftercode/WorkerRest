package scheduler

import (
	"testing"
	"time"
)

// 自动暂停（勿扰 / 全屏 / 投屏 / 应用占用 / 空闲）与手动暂停的语义分叉：
//
//   A. 进入自动暂停时立刻收掉**所有**提醒窗口（休息窗、提前提示窗），被打断的休息
//      记“跳过”；暂停期间到点的自定义提醒一律跳过，不看它自己的“暂停休息时”配置。
//   B. 恢复时按“离开”口径重锚内置倒计时（从头计时），而不是按剩余时间平移。
//
// 手动暂停一条都不适用：那是用户自己按的暂停，休息窗继续挂着、进度平移、自定义提醒
// 按配置处置。所以下面每个 A/B 用例都配一条手动暂停的对照，防止把两种暂停写成一种。

// ---------- A：进入自动暂停时清场 ----------

// 休息进行中被自动原因打断：休息立刻结束，这次记“跳过”而不是“完成 / 提前结束”。
func TestAutoPauseInterruptsOngoingBreak(t *testing.T) {
	s, host := newTestSched(t, shortCountdown)
	s.replan()
	if id := s.startBreak(); id == "" {
		t.Fatal("前置：应能弹出休息窗口")
	}
	if s.CurrentPhase() != "break" {
		t.Fatalf("前置：应处于休息中，得到 %q", s.CurrentPhase())
	}

	s.Pause("勿扰")

	if p := s.CurrentPhase(); p != "idle" {
		t.Fatalf("自动暂停后应立刻结束休息（phase=idle），得到 %q", p)
	}
	if host.autoPauses != 1 {
		t.Fatalf("应通知宿主清场（关全部窗口），得到 %d 次", host.autoPauses)
	}
	if len(host.skips) != 1 || host.skips[0] != "short" {
		t.Fatalf("被打断的休息应记一次跳过，得到 %v", host.skips)
	}
	if n := s.StateCopy().ShortsSinceLong; n != 1 {
		t.Fatalf("这次休息名额应被消费（与手动跳过同口径），得到 %d", n)
	}
	if s.PreBreakWanted() {
		t.Fatal("自动暂停期间不该有提前提示窗")
	}
}

// 提前提示窗在场（phase=pre）时被自动原因打断：窗口收掉、状态回 idle。
func TestAutoPauseClearsPreWindow(t *testing.T) {
	// 间隔 10 秒、提前 5 秒：replan 之后第一跳必然已经进入提前区间
	const tight = `{"short":{"enabled":true,"mode":"countdown","intervalMs":10000,"durationMs":2000,` +
		`"notifyBeforeMs":5000,"postponeMs":60000,"slots":[]}}`
	s, host := newTestSched(t, tight)
	s.replan()
	// 把计划拨到“还剩 3 秒”（< 提前量 5 秒）：不这么拨就得真等 5 秒才进提前区间
	s.mu.Lock()
	s.state.NextAt = nowMs() + 3000
	s.mu.Unlock()
	s.onTick()
	if s.CurrentPhase() != "pre" {
		t.Fatalf("前置：应已在提前提示区间内，得到 %q", s.CurrentPhase())
	}

	s.Pause("勿扰")

	if p := s.CurrentPhase(); p != "idle" {
		t.Fatalf("自动暂停应把 pre 拉回 idle，得到 %q", p)
	}
	if host.autoPauses != 1 {
		t.Fatalf("应通知宿主清场，得到 %d 次", host.autoPauses)
	}
	if s.PreBreakWanted() {
		t.Fatal("自动暂停期间提前提示窗不该在场")
	}
}

// 自动暂停期间到点的自定义提醒一律按跳过，哪怕它配的是“正常提醒”。
func TestAutoPauseForcesSkipOnCustomReminder(t *testing.T) {
	s, host := newTestSched(t, customFixedNow(`,"pauseAction":"remind"`))
	s.replan()
	s.Pause("勿扰")

	s.onTick()
	if len(host.breaks) != 0 {
		t.Fatalf("自动暂停期间不应弹出提醒窗口，得到 %v", host.breaks)
	}
	if len(host.pre) != 0 {
		t.Fatalf("自动暂停期间不应弹出提前提示，得到 %v", host.pre)
	}
	if len(host.skips) != 1 || host.skips[0] != "cb-water" {
		t.Fatalf("自动暂停期间到点应记一次跳过，得到 %v", host.skips)
	}
	// 同一 occurrence 的下一跳不得重复记账
	s.onTick()
	if len(host.skips) != 1 {
		t.Fatalf("同一 occurrence 不应重复记账，得到 %v", host.skips)
	}
}

// 对照：同一条“正常提醒”配置在手动暂停下照常弹 —— 证明配置本身有效，
// 上面的 0 次是“自动”这个前提带来的，不是配置没生效。
func TestManualPauseStillHonorsRemindOnCustomReminder(t *testing.T) {
	s, host := newTestSched(t, customFixedNow(`,"pauseAction":"remind"`))
	s.replan()
	s.Pause("manual")

	s.onTick()
	if len(host.breaks) != 1 || host.breaks[0] != "cb-water" {
		t.Fatalf("手动暂停下“正常提醒”应照常弹出，得到 %v", host.breaks)
	}
	if len(host.skips) != 0 {
		t.Fatalf("照常提醒不该记跳过，得到 %v", host.skips)
	}
}

// ---------- B：恢复时重锚内置倒计时 ----------

// 自动暂停恢复后，内置倒计时从头计时（与离开回来的口径一致）。
func TestAutoPauseResumeReanchorsCountdown(t *testing.T) {
	s, _ := newTestSched(t, shortCountdown)
	s.replan()
	// 模拟“这一轮已经走了 8 分钟，还剩 2 分钟”：连挂起线与分组视图一起拨，
	// 保持状态自洽（只改 NextAt 的话 Rest.At 对不上，恢复时不会跟着走）
	s.mu.Lock()
	at := nowMs() + 120_000
	s.state.NextAt = at
	for i := range s.state.Lines {
		if s.state.Lines[i].Source == "countdown" {
			s.state.Lines[i].At = at
		}
	}
	if s.state.Rest != nil {
		s.state.Rest.At = at
	}
	s.mu.Unlock()

	s.Pause("勿扰")
	time.Sleep(50 * time.Millisecond)
	s.Resume("勿扰")

	remain := s.RemainingMs()
	if remain < 599_000 || remain > 600_100 {
		t.Fatalf("自动暂停恢复应重锚到满间隔 600000 ms，得到 %d（按剩余 120000 平移就是旧口径）", remain)
	}
	// 状态卡的分组倒计时也要跟着到满间隔，不能停在“旧的剩余 + 暂停时长”
	if st := s.StateCopy(); st.Rest != nil && st.Rest.At != st.NextAt {
		t.Fatalf("分组倒计时应同步到重锚后的时刻：Rest.At=%d NextAt=%d", st.Rest.At, st.NextAt)
	}
}

// 对照：手动暂停恢复仍按剩余平移 —— 用户主动按的暂停不吞他的倒计时进度。
func TestManualPauseResumeKeepsCountdownProgress(t *testing.T) {
	s, _ := newTestSched(t, shortCountdown)
	s.replan()
	s.mu.Lock()
	s.state.NextAt = nowMs() + 120_000
	s.mu.Unlock()

	s.Pause("manual")
	time.Sleep(50 * time.Millisecond)
	s.Resume("manual")

	remain := s.RemainingMs()
	if remain < 119_000 || remain > 121_000 {
		t.Fatalf("手动暂停恢复应保留剩余约 120000 ms，得到 %d", remain)
	}
}

// 自定义倒计时提醒不重锚：它是用户按自己节奏配的安排，一次暂停不该把它推走一整轮。
func TestAutoPauseKeepsCustomCountdownProgress(t *testing.T) {
	// 内置短 / 长休息都关掉：否则“下一个”永远是内置那条，量不到自定义提醒
	cfg := `{"short":{"enabled":false},"long":{"enabled":false},` +
		`"customBreaks":[{"id":"cb-water","name":"喝水提醒","config":{` +
		`"enabled":true,"postponeMs":60000,"mode":"countdown","intervalMs":1800000,` +
		`"durationMs":20000,"slots":[]}}]}`
	s, _ := newTestSched(t, cfg)
	s.replan()
	st := s.StateCopy()
	if st.NextType != "cb-water" {
		t.Fatalf("前置：下一个应是自定义提醒，得到 %q", st.NextType)
	}
	s.mu.Lock()
	s.state.NextAt = nowMs() + 90_000 // 还剩 1.5 分钟（间隔 30 分钟）
	s.mu.Unlock()

	s.Pause("勿扰")
	time.Sleep(30 * time.Millisecond)
	s.Resume("勿扰")

	remain := s.RemainingMs()
	if remain < 89_000 || remain > 91_000 {
		t.Fatalf("自定义倒计时提醒应保留剩余约 90000 ms，得到 %d", remain)
	}
}

// customFixedNow 造一条“此刻到点”的自定义提醒；extra 追加到 config 对象里
// （例如 ,"pauseAction":"remind"）
func customFixedNow(extra string) string {
	now := time.Now()
	return `{"customBreaks":[{"id":"cb-water","name":"喝水提醒","config":{"enabled":true` + extra +
		`,"mode":"fixed","durationMs":20000,"slots":[{"id":"s1","hour":` + itoa(now.Hour()) +
		`,"minute":` + itoa(now.Minute()) + `,"repeat":"daily","days":[],"enabled":true}]}}]}`
}
