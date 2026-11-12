package scheduler

import (
	"testing"
	"time"
)

// “暂停休息时”三种处置（正常提醒 / 推迟提醒 / 跳过提醒，默认推迟）的回归：
//   - 跳过：外卖提醒 / 自定义提醒 / 内置短长休息在暂停态下到点时不弹任何窗，直接按
//     “跳过”记一笔（内置短 / 长还会推进计数并消费这一跳）；
//   - 正常提醒：暂停期间照常弹（提前提示小窗 + 提醒窗口），不进跳过统计；
//   - 推迟提醒：暂停期间不弹、也不记账，按该提醒的“可推迟”时长往后推；推完仍暂停就
//     继续推，恢复后按推迟后的时刻弹（内置倒计时线例外，见下）；
//   - **暂停原因不参与处置**（2026-11-12 用户口径：“不存在例外”）：手动 / 空闲 / 勿扰 /
//     全屏 / 投屏 / 应用占用一律按上面三条走，曾经那条例外“自动暂停一律跳过”已删除。
//
// 一条铁律：暂停态每半秒一跳都会重算一次，任何分支重复记账 / 重复弹窗都算 bug ——
// 所以每个用例都跑多跳，断言只发生一次。
//
// 注：配置层归一化（pauseAction 默认/非法值/保存路径）已下沉到 internal/settings 的
// pause_action_test.go；这里只留调度器与外卖的处置行为。

// ---------- 调度器（自定义提醒） ----------

// 固定时刻模式 + 跳过提醒：暂停期间到点的那一跳记一次跳过，且只记一次
func TestPausedSkipCustomFixedSlot(t *testing.T) {
	now := time.Now()
	cfg := `{"customBreaks":[{"id":"cb-water","name":"喝水提醒","config":{` +
		`"enabled":true,"pauseAction":"skip","mode":"fixed","durationMs":20000,"slots":[` +
		`{"id":"s1","hour":` + itoa(now.Hour()) + `,"minute":` + itoa(now.Minute()) +
		`,"repeat":"daily","days":[],"enabled":true}]}}]}`
	s, host := newTestSched(t, cfg)
	s.replan()
	s.Pause("manual")

	s.onTick()
	if len(host.skips) != 1 || host.skips[0] != "cb-water" {
		t.Fatalf("暂停期间到点的自定义提醒应记 1 次跳过，得到 %v", host.skips)
	}
	if len(host.breaks) != 0 {
		t.Fatalf("暂停期间不应弹休息窗口，得到 %v", host.breaks)
	}
	if len(host.pre) != 0 {
		t.Fatalf("暂停期间不应弹提前提示，得到 %v", host.pre)
	}
	// 同一分钟内再跑一跳：不得重复记账
	s.onTick()
	if len(host.skips) != 1 {
		t.Fatalf("同一 occurrence 不应重复记账，得到 %v", host.skips)
	}
}

// 倒计时模式 + 跳过提醒：挂起那一跳在暂停期间过点 → 记一次跳过
func TestPausedSkipCustomCountdown(t *testing.T) {
	cfg := `{"customBreaks":[{"id":"cb-water","name":"喝水提醒","config":{` +
		`"enabled":true,"pauseAction":"skip","mode":"countdown","intervalMs":600000,"durationMs":20000,"slots":[]}}]}`
	s, host := newTestSched(t, cfg)
	s.replan()
	// 模拟“暂停期间时间走完了这一跳”
	s.mu.Lock()
	s.state.NextAt = nowMs() - 1000
	for i := range s.state.Lines {
		s.state.Lines[i].At = s.state.NextAt
	}
	s.mu.Unlock()
	s.Pause("manual")

	s.onTick()
	s.onTick()
	if len(host.skips) != 1 || host.skips[0] != "cb-water" {
		t.Fatalf("倒计时到点应记 1 次跳过，得到 %v", host.skips)
	}
	if len(host.breaks) != 0 {
		t.Fatalf("暂停期间不应弹休息窗口，得到 %v", host.breaks)
	}
}

// 正常提醒：暂停期间照常弹提醒窗口（不记账），且同一次 occurrence 只弹一次
func TestPausedRemindCustomFixedSlot(t *testing.T) {
	now := time.Now()
	cfg := `{"customBreaks":[{"id":"cb-water","name":"喝水提醒","config":{` +
		`"enabled":true,"pauseAction":"remind","mode":"fixed","durationMs":20000,"slots":[` +
		`{"id":"s1","hour":` + itoa(now.Hour()) + `,"minute":` + itoa(now.Minute()) +
		`,"repeat":"daily","days":[],"enabled":true}]}}]}`
	s, host := newTestSched(t, cfg)
	s.replan()
	s.Pause("manual")
	s.onTick()
	if len(host.skips) != 0 {
		t.Fatalf("“正常提醒”不应记账，得到 %v", host.skips)
	}
	if len(host.breaks) != 1 || host.breaks[0] != "cb-water" {
		t.Fatalf("暂停期间到点应照常弹提醒，得到 %v", host.breaks)
	}
	// 再跑两跳：同一次 occurrence 不得重复弹（第一跳已进休息窗口，第二跳是收窗判定）
	s.onTick()
	s.onTick()
	if len(host.breaks) != 1 {
		t.Fatalf("同一 occurrence 不应重复弹，得到 %v", host.breaks)
	}
}

// 正常提醒 + 倒计时线到点：同样照常弹（挂起那一跳过点即触发）
func TestPausedRemindCustomCountdown(t *testing.T) {
	cfg := `{"customBreaks":[{"id":"cb-water","name":"喝水提醒","config":{` +
		`"enabled":true,"pauseAction":"remind","mode":"countdown","intervalMs":600000,"durationMs":20000,"slots":[]}}]}`
	s, host := newTestSched(t, cfg)
	s.replan()
	s.mu.Lock()
	s.state.NextAt = nowMs() - 1000
	for i := range s.state.Lines {
		s.state.Lines[i].At = s.state.NextAt
	}
	s.mu.Unlock()
	s.Pause("manual")
	s.onTick()
	if len(host.breaks) != 1 || host.breaks[0] != "cb-water" {
		t.Fatalf("倒计时到点应照常弹提醒，得到 %v", host.breaks)
	}
	if len(host.skips) != 0 {
		t.Fatalf("“正常提醒”不应记账，得到 %v", host.skips)
	}
}

// 正常提醒 + 提前通知：落在“原定时刻 − 提前量”区间时照常弹提前提示小窗
func TestPausedRemindCustomPreNotify(t *testing.T) {
	at := time.Now().Add(5 * time.Minute) // 5 分钟后到点：此刻正落在“到点 − 提前量”那一分钟
	cfg := `{"customBreaks":[{"id":"cb-water","name":"喝水提醒","config":{` +
		`"enabled":true,"pauseAction":"remind","mode":"fixed","durationMs":20000,"notifyBeforeMs":300000,"slots":[` +
		`{"id":"s1","hour":` + itoa(at.Hour()) + `,"minute":` + itoa(at.Minute()) +
		`,"repeat":"daily","days":[],"enabled":true}]}}]}`
	s, host := newTestSched(t, cfg)
	s.replan()
	s.Pause("manual")
	s.onTick()
	s.onTick()
	if len(host.pre) != 1 || host.pre[0] != "cb-water" {
		t.Fatalf("提前区间应照常弹提前提示小窗，得到 %v", host.pre)
	}
	if len(host.breaks) != 0 {
		t.Fatalf("还没到点不应弹提醒窗口，得到 %v", host.breaks)
	}
	if len(host.skips) != 0 {
		t.Fatalf("“正常提醒”不应记账，得到 %v", host.skips)
	}
}

// 推迟提醒：到点不弹窗也不记账，只排一条“推迟到 X”的计划；再到期时仍暂停就继续推
func TestPausedPostponeCustomFixedSlot(t *testing.T) {
	now := time.Now()
	cfg := `{"customBreaks":[{"id":"cb-water","name":"喝水提醒","config":{` +
		`"enabled":true,"pauseAction":"postpone","postponeMs":120000,"mode":"fixed","durationMs":20000,"slots":[` +
		`{"id":"s1","hour":` + itoa(now.Hour()) + `,"minute":` + itoa(now.Minute()) +
		`,"repeat":"daily","days":[],"enabled":true}]}}]}`
	s, host := newTestSched(t, cfg)
	s.replan()
	s.Pause("manual")

	s.onTick()
	s.onTick()
	if len(host.breaks) != 0 || len(host.pre) != 0 || len(host.skips) != 0 {
		t.Fatalf("推迟提醒不应弹窗也不应记账，得到 breaks=%v pre=%v skips=%v",
			host.breaks, host.pre, host.skips)
	}
	s.mu.Lock()
	if len(s.postponed) != 1 {
		s.mu.Unlock()
		t.Fatalf("应排下 1 条推迟计划，得到 %d", len(s.postponed))
	}
	var due int64
	for _, p := range s.postponed {
		due = p.dueAt
	}
	s.mu.Unlock()
	if due < nowMs()+90_000 || due > nowMs()+150_000 {
		t.Fatalf("推迟后的时刻应约为 now+2 分钟，得到 %d", due-nowMs())
	}

	// 推后的时刻又到了、仍然暂停：继续往后推，且不弹窗
	s.mu.Lock()
	for _, p := range s.postponed {
		p.dueAt = nowMs() - 1000
	}
	s.mu.Unlock()
	s.onTick()
	s.mu.Lock()
	again := int64(0)
	for _, p := range s.postponed {
		again = p.dueAt
	}
	s.mu.Unlock()
	if len(host.breaks) != 0 {
		t.Fatalf("仍暂停时不应弹窗，得到 %v", host.breaks)
	}
	if again <= nowMs() {
		t.Fatalf("仍暂停时应继续往后推，得到 %d", again-nowMs())
	}
}

// 推迟提醒：恢复后按推迟后的时刻弹出，且只弹一次（去重表不随 resume 清空）
func TestPausedPostponeFiresAfterResume(t *testing.T) {
	now := time.Now()
	cfg := `{"customBreaks":[{"id":"cb-water","name":"喝水提醒","config":{` +
		`"enabled":true,"pauseAction":"postpone","postponeMs":120000,"mode":"fixed","durationMs":20000,"slots":[` +
		`{"id":"s1","hour":` + itoa(now.Hour()) + `,"minute":` + itoa(now.Minute()) +
		`,"repeat":"daily","days":[],"enabled":true}]}}]}`
	s, host := newTestSched(t, cfg)
	s.replan()
	s.Pause("manual")
	s.onTick()
	s.Resume("manual")
	s.mu.Lock()
	pending := len(s.postponed)
	s.mu.Unlock()
	if pending != 1 {
		t.Fatalf("恢复后推迟计划应保留，得到 %d", pending)
	}
	// 推到点的时刻已经到了：这一跳弹出来
	s.mu.Lock()
	for _, p := range s.postponed {
		p.dueAt = nowMs() - 1000
	}
	s.mu.Unlock()
	s.onTick()
	if len(host.breaks) != 1 || host.breaks[0] != "cb-water" {
		t.Fatalf("推迟后的时刻到点应弹出一次，得到 %v", host.breaks)
	}
	if len(host.skips) != 0 {
		t.Fatalf("推迟不算跳过，得到 %v", host.skips)
	}
	// 再跑几跳：同一次 occurrence 不得重复弹
	s.onTick()
	s.Resume("manual")
	s.onTick()
	if len(host.breaks) != 1 {
		t.Fatalf("同一 occurrence 不应重复弹，得到 %v", host.breaks)
	}
}

// 推迟时长下限 1 分钟（2026-11-10）：配置写 0 也被抬到 1 分钟，
// 不再“没有时长可推”而降级成跳过
func TestPausedPostponeZeroClimbsToOneMinute(t *testing.T) {
	now := time.Now()
	cfg := `{"customBreaks":[{"id":"cb-water","name":"喝水提醒","config":{` +
		`"enabled":true,"pauseAction":"postpone","postponeMs":0,"mode":"fixed","durationMs":20000,"slots":[` +
		`{"id":"s1","hour":` + itoa(now.Hour()) + `,"minute":` + itoa(now.Minute()) +
		`,"repeat":"daily","days":[],"enabled":true}]}}]}`
	s, host := newTestSched(t, cfg)
	s.replan()
	s.Pause("manual")
	s.onTick()
	if len(host.skips) != 0 {
		t.Fatalf("推迟时长下限 1 分钟：不该记跳过，得到 %v", host.skips)
	}
	s.mu.Lock()
	n := len(s.postponed)
	var due int64
	for _, p := range s.postponed {
		due = p.dueAt
	}
	s.mu.Unlock()
	if n != 1 {
		t.Fatalf("应留下 1 条推迟计划，得到 %d", n)
	}
	if d := due - nowMs(); d <= 0 || d > 61_000 {
		t.Fatalf("推迟时长应约 1 分钟，得到 %d ms", d)
	}
}

// ---------- 调度器（内置短 / 长休息） ----------
//
// 2026-11-12 起短 / 长休息也有“暂停休息时”（用户要求与外卖提醒、自定义提醒一致）：
//   - 跳过：不弹窗，推进短休息计数、消费这一跳、记一笔跳过，然后重排下一次；
//   - 正常提醒：照常弹休息窗（与暂停中“立即休息”发起的那次同路），不记账；
//   - 推迟（默认）：什么都不做 —— 倒计时在暂停期间本来就冻结，恢复时整体平移 / 重锚，
//     这一跳不会丢；固定时刻线例外，它要排进推迟表（时刻是绝对的，恢复时 replan 只会
//     顺到下一个还没到的时刻）。
// 自动暂停是例外：内置短 / 长在那条支路里一律不打扰、也不记账。

// 把内置倒计时那一跳挪到过去（模拟“暂停期间时间走完了这一跳”）
func forceBuiltinDueNow(s *Scheduler) {
	s.mu.Lock()
	s.state.NextAt = nowMs() - 1000
	for i := range s.state.Lines {
		s.state.Lines[i].At = s.state.NextAt
	}
	s.mu.Unlock()
}

// 倒计时 + 跳过：记一次跳过、推进计数、消费这一跳并重排（同一 occurrence 只记一次）
func TestPausedSkipBuiltinCountdown(t *testing.T) {
	s, host := newTestSched(t, `{"short":{"enabled":true,"mode":"countdown",`+
		`"intervalMs":600000,"durationMs":20000,"pauseAction":"skip","slots":[]}}`)
	s.replan()
	forceBuiltinDueNow(s)
	s.Pause("manual")

	s.onTick()
	s.onTick()
	if len(host.skips) != 1 || host.skips[0] != "short" {
		t.Fatalf("暂停期间到点的短休息应按跳过记 1 次，得到 %v", host.skips)
	}
	if len(host.breaks) != 0 || len(host.pre) != 0 {
		t.Fatalf("跳过不该弹窗，得到 breaks=%v pre=%v", host.breaks, host.pre)
	}
	s.mu.Lock()
	shorts := s.state.ShortsSinceLong
	nextAt := s.state.NextAt
	stillQueued := false
	for _, l := range s.state.Lines {
		if l.Type == "short" && l.At <= nowMs() {
			stillQueued = true
		}
	}
	s.mu.Unlock()
	if shorts != 1 {
		t.Fatalf("跳过一次短休息应把计数推进到 1，得到 %d", shorts)
	}
	if stillQueued {
		t.Fatal("跳过的那一跳必须从挂起线里摘掉，否则恢复后会被原样排回来再弹一次")
	}
	if nextAt <= nowMs() {
		t.Fatalf("跳过后应重排下一次（NextAt 落在未来），得到 %d", nextAt-nowMs())
	}
}

// 固定时刻 + 跳过：同一分钟只记一笔，且不弹窗
func TestPausedSkipBuiltinFixedSlot(t *testing.T) {
	now := time.Now()
	s, host := newTestSched(t, `{"long":{"enabled":true,"mode":"fixed","durationMs":300000,`+
		`"pauseAction":"skip","slots":[{"id":"s1","hour":`+itoa(now.Hour())+
		`,"minute":`+itoa(now.Minute())+`,"repeat":"daily","days":[],"enabled":true}]}}`)
	s.replan()
	s.Pause("manual")
	s.onTick()
	s.onTick()
	if len(host.skips) != 1 || host.skips[0] != "long" {
		t.Fatalf("固定时刻到点的长休息应按跳过记 1 次，得到 %v", host.skips)
	}
	if len(host.breaks) != 0 {
		t.Fatalf("跳过不该弹窗，得到 %v", host.breaks)
	}
}

// 倒计时 + 正常提醒：暂停期间照常弹休息窗（与“立即休息”同路，照常走完自己的时长）
func TestPausedRemindBuiltinCountdown(t *testing.T) {
	// 暂停原因不参与处置（2026-11-12 用户口径：“不存在例外”）：手动 / 勿扰 / 空闲三种
	// 都要照常弹，且弹出来的休息都要按真实时钟走完。
	for _, reason := range []string{PauseReasonManual, "勿扰", PauseReasonIdle} {
		s, host := newTestSched(t, `{"short":{"enabled":true,"mode":"countdown",`+
			`"intervalMs":600000,"durationMs":20000,"notifyBeforeMs":10000,`+
			`"pauseAction":"remind","slots":[]}}`)
		s.replan()
		forceBuiltinDueNow(s)
		s.Pause(reason)

		s.onTick()
		if len(host.breaks) != 1 || host.breaks[0] != "short" {
			t.Fatalf("%s：“正常提醒”应在暂停期间照常弹短休息，得到 %v", reason, host.breaks)
		}
		if len(host.skips) != 0 {
			t.Fatalf("%s：照常提醒不算跳过，得到 %v", reason, host.skips)
		}
		s.mu.Lock()
		phase, whilePaused := s.state.Phase, s.state.BreakWhilePaused
		frozen := s.pauseFreezesClockLocked()
		s.mu.Unlock()
		if phase != "break" || !whilePaused {
			t.Fatalf("%s：应进入休息且标记为“暂停中主动发起”，得到 phase=%q whilePaused=%v",
				reason, phase, whilePaused)
		}
		if frozen {
			t.Fatalf("%s：暂停中弹出来的休息必须按真实时钟走，否则窗口会挂到恢复为止", reason)
		}
		// 再跑几跳：同一次 occurrence 只弹一次
		s.onTick()
		s.onTick()
		if len(host.breaks) != 1 {
			t.Fatalf("%s：同一 occurrence 不应重复弹，得到 %v", reason, host.breaks)
		}
	}
}

// 倒计时 + 推迟（默认）：什么都不做，冻结的那一跳原样留着，恢复后整体平移
func TestPausedPostponeBuiltinCountdownKeepsFrozen(t *testing.T) {
	s, host := newTestSched(t, shortCountdown)
	s.replan()
	forceBuiltinDueNow(s)
	s.Pause("manual")
	s.mu.Lock()
	before := s.state.NextAt
	s.mu.Unlock()

	s.onTick()
	if len(host.skips) != 0 || len(host.breaks) != 0 || len(host.pre) != 0 {
		t.Fatalf("推迟不该弹窗也不该记账，得到 skips=%v breaks=%v pre=%v", host.skips, host.breaks, host.pre)
	}
	s.mu.Lock()
	after := s.state.NextAt
	pending := len(s.postponed)
	s.mu.Unlock()
	if after != before {
		t.Fatalf("推迟不清动冻结的那一跳（%d → %d）", before, after)
	}
	if pending != 0 {
		t.Fatalf("内置倒计时线不排推迟计划（会与冻结线撞车弹两次），得到 %d 条", pending)
	}
	// 恢复后这一跳仍在（推迟 = 冻结平移，下一次落在冻结时刻 + 暂停时长；
	// 测试里暂停只有几毫秒，所以平移量约为 0 —— 关键是不能把它丢掉）
	s.Resume("manual")
	s.mu.Lock()
	resumed := s.state.NextAt
	s.mu.Unlock()
	if resumed < before {
		t.Fatalf("恢复不该把这一跳往前挪（%d → %d）", before, resumed)
	}
}

// 固定时刻 + 推迟（默认）：排一条推迟计划，恢复后到点弹出
func TestPausedPostponeBuiltinFixedSlotFiresAfterResume(t *testing.T) {
	now := time.Now()
	s, host := newTestSched(t, `{"long":{"enabled":true,"mode":"fixed","durationMs":300000,`+
		`"postponeMs":120000,"slots":[{"id":"s1","hour":`+itoa(now.Hour())+
		`,"minute":`+itoa(now.Minute())+`,"repeat":"daily","days":[],"enabled":true}]}}`)
	s.replan()
	s.Pause("manual")
	s.onTick()
	if len(host.breaks) != 0 || len(host.skips) != 0 {
		t.Fatalf("暂停期间推迟不该弹窗 / 记账，得到 breaks=%v skips=%v", host.breaks, host.skips)
	}
	s.mu.Lock()
	n := len(s.postponed)
	s.mu.Unlock()
	if n != 1 {
		t.Fatalf("固定时刻被推迟应留 1 条推迟计划，得到 %d", n)
	}
	s.Resume("manual")
	s.mu.Lock()
	for _, p := range s.postponed {
		p.dueAt = nowMs() - 1000
	}
	s.mu.Unlock()
	s.onTick()
	if len(host.breaks) != 1 || host.breaks[0] != "long" {
		t.Fatalf("推迟后的时刻到点应弹出一次长休息，得到 %v", host.breaks)
	}
}

// 自动暂停不再有例外：内置短休息配了“跳过提醒”，勿扰里到点照样记一笔跳过并推进计数
// （2026-11-12 用户口径：“包括短休息、长休息、外卖提醒、自定义提醒在内的所有提醒，
// 都受“暂停休息时”的控制，不存在例外”）。
func TestAutoPauseHonorsSkipOnBuiltinBreak(t *testing.T) {
	s, host := newTestSched(t, `{"short":{"enabled":true,"mode":"countdown",`+
		`"intervalMs":600000,"durationMs":20000,"pauseAction":"skip","slots":[]}}`)
	s.replan()
	forceBuiltinDueNow(s)
	s.Pause("勿扰 / 专注")
	if !s.AutoPauseActive() {
		t.Fatal("测试前提：这段暂停应带自动原因")
	}
	s.onTick()
	if len(host.skips) != 1 || host.skips[0] != "short" {
		t.Fatalf("自动暂停期间到点的短休息应记一笔跳过，得到 %v", host.skips)
	}
	if len(host.breaks) != 0 {
		t.Fatalf("跳过提醒不该弹窗，得到 %v", host.breaks)
	}
	if n := s.StateCopy().ShortsSinceLong; n != 1 {
		t.Fatalf("跳过应推进短休息计数（与手动跳过同口径），得到 %d", n)
	}
	// 同一 occurrence 的下一拍不得重复记账
	s.onTick()
	if len(host.skips) != 1 {
		t.Fatalf("同一 occurrence 不应重复记账，得到 %v", host.skips)
	}
}

// 同一张卡在手动暂停下也是同一套动作 —— 暂停原因不参与处置。
func TestManualPauseHonorsSkipOnBuiltinBreak(t *testing.T) {
	s, host := newTestSched(t, `{"short":{"enabled":true,"mode":"countdown",`+
		`"intervalMs":600000,"durationMs":20000,"pauseAction":"skip","slots":[]}}`)
	s.replan()
	forceBuiltinDueNow(s)
	s.Pause(PauseReasonManual)
	s.onTick()
	if len(host.skips) != 1 || host.skips[0] != "short" {
		t.Fatalf("手动暂停期间到点的短休息应记一笔跳过，得到 %v", host.skips)
	}
}

// “推迟提醒”（默认）+ 自动暂停：不弹、不记账；恢复后第一拍就把这一跳补上
// （时钟冻结平移，不再按“离开”口径重锚 —— 重锚会把它悄悄抹掉，那等于又有了例外）。
func TestAutoPausePostponeBuiltinFiresAfterResume(t *testing.T) {
	s, host := newTestSched(t, `{"short":{"enabled":true,"mode":"countdown",`+
		`"intervalMs":600000,"durationMs":20000,"pauseAction":"postpone",`+
		`"postponeMs":120000,"slots":[]}}`)
	s.replan()
	forceBuiltinDueNow(s)
	s.Pause("勿扰")
	s.onTick()
	if len(host.breaks) != 0 || len(host.skips) != 0 {
		t.Fatalf("推迟提醒在暂停期间不该弹也不该记账，得到 breaks=%v skips=%v", host.breaks, host.skips)
	}
	if len(s.postponed) != 0 {
		t.Fatal("内置倒计时线不该另排推迟计划（冻结平移已实现推迟，再排会弹两次）")
	}
	s.Resume("勿扰")
	s.onTick()
	if len(host.breaks) != 1 || host.breaks[0] != "short" {
		t.Fatalf("恢复后第一拍应按“推迟提醒”补上这一跳，得到 %v", host.breaks)
	}
}

// 恢复后清空去重表：下一段暂停里新的 occurrence 照常记账
func TestPauseSkippedClearedOnResume(t *testing.T) {
	now := time.Now()
	cfg := `{"customBreaks":[{"id":"cb-water","name":"喝水提醒","config":{` +
		`"enabled":true,"pauseAction":"skip","mode":"fixed","durationMs":20000,"slots":[` +
		`{"id":"s1","hour":` + itoa(now.Hour()) + `,"minute":` + itoa(now.Minute()) +
		`,"repeat":"daily","days":[],"enabled":true}]}}]}`
	s, host := newTestSched(t, cfg)
	s.replan()
	s.Pause("manual")
	s.onTick()
	s.Resume("manual")
	s.mu.Lock()
	empty := len(s.pauseSkipped) == 0 && len(s.pauseFired) == 0
	s.mu.Unlock()
	if !empty {
		t.Fatal("完全恢复后去重表应清空")
	}
	if len(host.skips) != 1 {
		t.Fatalf("恢复不应影响已记的跳过，得到 %v", host.skips)
	}
}

// 默认（未写 pauseAction）= 推迟提醒：暂停期间到点不弹窗、不记账，排一条推迟计划
func TestPausedDefaultPostponeCustomFixedSlot(t *testing.T) {
	now := time.Now()
	cfg := `{"customBreaks":[{"id":"cb-water","name":"喝水提醒","config":{` +
		`"enabled":true,"postponeMs":120000,"mode":"fixed","durationMs":20000,"slots":[` +
		`{"id":"s1","hour":` + itoa(now.Hour()) + `,"minute":` + itoa(now.Minute()) +
		`,"repeat":"daily","days":[],"enabled":true}]}}]}`
	s, host := newTestSched(t, cfg)
	s.replan()
	s.Pause("manual")

	s.onTick()
	if len(host.skips) != 0 {
		t.Fatalf("默认“推迟提醒”不应记跳过，得到 %v", host.skips)
	}
	if len(host.breaks) != 0 || len(host.pre) != 0 {
		t.Fatalf("默认“推迟提醒”不应弹窗，得到 breaks=%v pre=%v", host.breaks, host.pre)
	}
	s.mu.Lock()
	n := len(s.postponed)
	var due int64
	for _, p := range s.postponed {
		due = p.dueAt
	}
	s.mu.Unlock()
	if n != 1 {
		t.Fatalf("应排 1 条推迟计划，得到 %d", n)
	}
	if d := due - nowMs(); d < 115_000 || d > 125_000 {
		t.Fatalf("推迟后的时刻应约为 now+2 分钟，得到 %d ms", d)
	}
}
