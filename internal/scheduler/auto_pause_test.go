package scheduler

import (
	"strings"
	"testing"
	"time"
)

// 自动暂停（勿扰 / 全屏 / 投屏 / 应用占用 / 空闲）的两件事：
//
//   A. 进入自动暂停时收掉**此刻在屏幕上**的提醒窗口（休息窗、提前提示窗、外卖窗），被打断的
//      休息记“跳过”。只管进入那一刻在场的窗口。
//      留窗规则（2026-11-12 用户口径，四类提醒一律适用）：那扇窗所属提醒自己的“暂停休息时”
//      是“正常提醒”就不收 —— 见 TestAutoPauseKeepsRemindBreakRunning / KeepsRemindPreWindow /
//      TestAutoPauseKeepTakeoutFollowsTakeoutConfig。暂停原因不参与判断。
//   B. 恢复时与手动暂停**同构**：不做任何补偿（既不平移也不重锚）—— 暂停期间时钟照常
//      走，到点的那一跳已经在暂停里按各卡处置过。曾经这里有“按离开口径重锚内置倒计时”
//      的分支，2026-11-12 用户口径“暂停就是暂停，不分原因”后删除。
//
// 暂停期间到点的提醒不按原因区别对待：一律看各卡自己的“暂停休息时”（见 pause_skip_test.go
// 的 TestAutoPauseHonorsSkipOnBuiltinBreak / TestAutoPausePostponeBuiltinFiresAfterResume）。
// 曾经那条例外“自动暂停一律跳过、不看配置”已删除。

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
	// 留不留由宿主按 short 自己的配置判：调度器这边只交代“提示窗属于 short”，
	// 并保证状态不再认可这扇窗在场（PreBreakWanted 已在上面断言）
	if host.autoKeep.PreKind != "short" {
		t.Fatalf("应把提示窗的归属（short）交给宿主，得到 %+v", host.autoKeep)
	}
}

// 提前提示窗在场时被自动原因打断：那条提醒配的是“正常提醒”→ 窗口与阶段都留着。
// 窗口会以“已暂停 / --”显示，恢复后继续照常倒数（见 prebreak.js 的 st.paused 分支）。
func TestAutoPauseKeepsRemindPreWindow(t *testing.T) {
	const tight = `{"short":{"enabled":true,"mode":"countdown","intervalMs":10000,"durationMs":2000,` +
		`"notifyBeforeMs":5000,"postponeMs":60000,"pauseAction":"remind","slots":[]}}`
	s, host := newTestSched(t, tight)
	s.replan()
	s.mu.Lock()
	s.state.NextAt = nowMs() + 3000
	s.mu.Unlock()
	s.onTick()
	if s.CurrentPhase() != "pre" {
		t.Fatalf("前置：应已在提前提示区间内，得到 %q", s.CurrentPhase())
	}

	s.Pause("勿扰")

	if p := s.CurrentPhase(); p != "pre" {
		t.Fatalf("“正常提醒”的提前提示窗不该被收，阶段应留在 pre，得到 %q", p)
	}
	if host.autoKeep.PreKind != "short" {
		t.Fatalf("应把提示窗的归属（short）告诉宿主，得到 %+v", host.autoKeep)
	}
	// 窗口每 500ms 自愈一次（PreBreakState → PreBreakWanted），判据也得认它在场，
	// 否则窗口会在半秒后自己退场 —— 等于没留
	if !s.PreBreakWanted() {
		t.Fatal("暂停中“正常提醒”的提前提示窗应判为在场")
	}
}

// 外卖的“暂停休息时”同样参与留窗判断（外卖不走调度器，判据仍在这里算好交给宿主）。
func TestAutoPauseKeepTakeoutFollowsTakeoutConfig(t *testing.T) {
	cases := []struct {
		action string
		want   bool
	}{
		{"remind", true},
		{"postpone", false},
		{"skip", false},
	}
	for _, c := range cases {
		cfg := `{"takeout":{"enabled":true,"pauseAction":"` + c.action + `"}}`
		s, host := newTestSched(t, cfg)
		s.Pause("勿扰")
		if got := host.autoKeep.Takeout; got != c.want {
			t.Fatalf("外卖配 %q 时应 keep.Takeout=%v，得到 %v", c.action, c.want, got)
		}
	}
}

// 手动暂停同理：提前提示窗的归属交给宿主，留不留由它按那条提醒的配置判
// （托盘 / 弹窗内“暂停计时”都走这条）。调度器不替任何一条提醒下结论。
func TestManualPauseHandsPreKindToHost(t *testing.T) {
	const tight = `{"short":{"enabled":true,"mode":"countdown","intervalMs":10000,"durationMs":2000,` +
		`"notifyBeforeMs":5000,"postponeMs":60000,"pauseAction":"%s","slots":[]}}`
	for _, action := range []string{"remind", "postpone", "skip"} {
		s, host := newTestSched(t, strings.Replace(tight, "%s", action, 1))
		s.replan()
		s.mu.Lock()
		s.state.NextAt = nowMs() + 3000
		s.mu.Unlock()
		s.onTick()
		if s.CurrentPhase() != "pre" {
			t.Fatalf("前置（%s）：应已在提前提示区间内，得到 %q", action, s.CurrentPhase())
		}
		s.Pause(PauseReasonManual)
		if got := host.pauseKeepPre; got != "short" {
			t.Fatalf("%s 配 %q 时手动暂停应把归属 \"short\" 交给宿主，得到 %q", "short", action, got)
		}
	}
}

// 自动暂停期间到点的自定义提醒按它自己的“暂停休息时”走：配了“正常提醒”就照常弹。
// 2026-11-12 起不再有“自动暂停一律跳过”的例外。
func TestAutoPauseHonorsRemindOnCustomReminder(t *testing.T) {
	s, host := newTestSched(t, customFixedNow(`,"pauseAction":"remind"`))
	s.replan()
	s.Pause("勿扰")

	s.onTick()
	if len(host.breaks) != 1 || host.breaks[0] != "cb-water" {
		t.Fatalf("自动暂停下“正常提醒”应照常弹出，得到 %v", host.breaks)
	}
	if len(host.skips) != 0 {
		t.Fatalf("照常提醒不该记跳过，得到 %v", host.skips)
	}
}

// 同一条配置在手动暂停下也是照常弹 —— 两种暂停原因不该有区别。
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

// 正在进行的休息配的是“正常提醒”：自动暂停闯进来时**不收窗**，让它照常走完
// （2026-11-12 用户口径：“正常休息时别收窗”）。不记跳过、不推进计数，时钟也不冻结。
func TestAutoPauseKeepsRemindBreakRunning(t *testing.T) {
	s, host := newTestSched(t, `{"short":{"enabled":true,"mode":"countdown",`+
		`"intervalMs":600000,"durationMs":20000,"pauseAction":"remind","slots":[]}}`)
	s.replan()
	if id := s.startBreak(); id == "" {
		t.Fatal("前置：应能弹出休息窗口")
	}

	s.Pause("勿扰")

	if p := s.CurrentPhase(); p != "break" {
		t.Fatalf("“正常提醒”的休息不该被自动暂停收掉，得到 phase=%q", p)
	}
	if host.autoPauses != 1 || host.autoKeep.BreakKind != "short" {
		t.Fatalf("应通知宿主清场并交代休息窗归属（short），得到 %d 次 keep=%+v",
			host.autoPauses, host.autoKeep)
	}
	if len(host.skips) != 0 {
		t.Fatalf("“正常提醒”不该记跳过，得到 %v", host.skips)
	}
	if n := s.StateCopy().ShortsSinceLong; n != 0 {
		t.Fatalf("这次休息名额不该被消费，得到 %d", n)
	}
	s.mu.Lock()
	keep := s.state.BreakWhilePaused
	frozen := s.pauseFreezesClockLocked()
	s.mu.Unlock()
	if !keep || frozen {
		t.Fatalf("应按真实时钟走完（BreakWhilePaused=%v 冻结=%v），否则窗口会挂到恢复为止", keep, frozen)
	}
}

// 上面那次休息到点后照常收窗（暂停还在，但这次休息是“照常提醒”，该正常结束）。
func TestAutoPauseRemindBreakClosesWhenDue(t *testing.T) {
	s, host := newTestSched(t, `{"short":{"enabled":true,"mode":"countdown",`+
		`"intervalMs":600000,"durationMs":20000,"pauseAction":"remind","slots":[]}}`)
	s.replan()
	s.startBreak()
	s.Pause("勿扰")

	// 把结束时刻拨到过去：模拟暂停里这 20 秒走完了
	s.mu.Lock()
	s.state.BreakEndsAt = nowMs() - breakCloseDwellMs - 1000
	s.mu.Unlock()
	s.onTick()

	if p := s.CurrentPhase(); p != "idle" {
		t.Fatalf("休息到点应正常收窗，得到 phase=%q", p)
	}
	if host.closed == 0 {
		t.Fatal("应关掉休息窗口")
	}
	if len(host.skips) != 0 {
		t.Fatalf("走完的休息不该记跳过，得到 %v", host.skips)
	}
}

// 暂停里走完的那次休息排出的下一跳**已经是按真实时间算的**，恢复时不能再 +暂停时长。
func TestResumeAfterKeptBreakDoesNotShiftNext(t *testing.T) {
	s, _ := newTestSched(t, `{"short":{"enabled":true,"mode":"countdown",`+
		`"intervalMs":600000,"durationMs":20000,"pauseAction":"remind","slots":[]}}`)
	s.replan()
	s.startBreak()
	s.Pause("勿扰")

	s.mu.Lock()
	s.state.BreakEndsAt = nowMs() - breakCloseDwellMs - 1000
	s.mu.Unlock()
	// 同一毫秒内 replan 两次会算出同一个“现在 + 间隔”，被 consumed 当成同一跳过滤掉
	//（真实场景不会一秒内开两次休息），这里错开 2 ms 让断言稳定
	time.Sleep(2 * time.Millisecond)
	s.onTick()
	planned := s.StateCopy().NextAt
	if planned == 0 {
		t.Fatal("前置：休息结束后应排出下一跳")
	}

	time.Sleep(30 * time.Millisecond)
	s.Resume("勿扰")

	if got := s.StateCopy().NextAt; got != planned {
		t.Fatalf("暂停期间重排过的下一跳不该再平移：want=%d got=%d（差 %d ms）",
			planned, got, got-planned)
	}
}

// 对照：配“跳过提醒”的休息遇到自动暂停照旧收窗、记一笔跳过 —— 只有“正常提醒”才放行。
func TestAutoPauseSkipBreakStillCleared(t *testing.T) {
	s, host := newTestSched(t, `{"short":{"enabled":true,"mode":"countdown",`+
		`"intervalMs":600000,"durationMs":20000,"pauseAction":"skip","slots":[]}}`)
	s.replan()
	s.startBreak()

	s.Pause("勿扰")

	if p := s.CurrentPhase(); p != "idle" {
		t.Fatalf("“跳过提醒”的休息应被收掉（phase=idle），得到 %q", p)
	}
	// 被收掉的那次休息仍要把归属交代出去（宿主按它查配置：skip → 收窗），
	// 调度器这边已经落了账、也退回等待态
	if host.autoKeep.BreakKind != "short" {
		t.Fatalf("应把休息窗的归属（short）交给宿主，得到 %+v", host.autoKeep)
	}
	if len(host.skips) != 1 || host.skips[0] != "short" {
		t.Fatalf("应记一次跳过，得到 %v", host.skips)
	}
	if n := s.StateCopy().ShortsSinceLong; n != 1 {
		t.Fatalf("这次休息名额应被消费，得到 %d", n)
	}
}

// ---------- B：恢复不做补偿（与暂停原因无关） ----------

// 无论暂停原因是手动还是自动，恢复后内置倒计时都接着原来的目标走：平移与重锚都没有。
// 曾经自动原因会重锚到满间隔（“离开回来从头计时”），2026-11-12 起删掉；手动暂停那侧的
// “冻结 + 恢复整体平移”也在同一天取消（暂停不再冻钟，到点由处置接手）。
func TestPauseResumeKeepsCountdownProgress(t *testing.T) {
	for _, reason := range []string{PauseReasonManual, "勿扰", PauseReasonIdle} {
		s, _ := newTestSched(t, shortCountdown)
		s.replan()
		// 模拟“这一轮已经走了 8 分钟，还剩 2 分钟”
		s.mu.Lock()
		s.state.NextAt = nowMs() + 120_000
		for i := range s.state.Lines {
			if s.state.Lines[i].Source == "countdown" {
				s.state.Lines[i].At = s.state.NextAt
			}
		}
		if s.state.Rest != nil {
			s.state.Rest.At = s.state.NextAt
		}
		s.mu.Unlock()

		s.Pause(reason)
		time.Sleep(20 * time.Millisecond)
		s.Resume(reason)

		remain := s.RemainingMs()
		if remain < 119_000 || remain > 121_000 {
			t.Fatalf("%s 恢复应保留剩余约 120000 ms（与手动暂停同构），得到 %d", reason, remain)
		}
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
