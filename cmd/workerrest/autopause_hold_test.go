package main

import (
	"bytes"
	"log"
	"strings"
	"testing"

	"workerrest/internal/model"
	"workerrest/internal/scheduler"
)

// 条件消失时是否自动恢复，按原因分两套（autoResumeOnClear）。
// 这张表就是产品口径：改错任何一格，直接后果是会议中途弹窗，或者每天要手动点恢复。
func TestAutoResumeOnClearMatrix(t *testing.T) {
	cases := []struct {
		reason string
		want   bool
		why    string
	}{
		{scheduler.PauseReasonIdle, true, "人回来了 = 场景结束；空闲时人不在场，没有尴尬可言"},
		{pauseReasonAppPause, true, "目标应用退出 = 忙完了，是明确的结束信号"},
		{pauseReasonDnd, false, "勿扰可能被系统或别人随手关掉，会议还在"},
		{pauseReasonFullScr, false, "退出全屏或演示还可以继续开会"},
		{pauseReasonCastMirror, false, "投屏断开不等于散会"},
		{pauseReasonCastWireless, false, "无线投放断开不等于散会"},
	}
	for _, c := range cases {
		if got := autoResumeOnClear(c.reason); got != c.want {
			t.Errorf("autoResumeOnClear(%q) = %v，应为 %v（%s）", c.reason, got, c.want, c.why)
		}
	}

	// 两个判据必须互相独立：manual 不是自动原因；白名单里每一条都是自动原因
	if scheduler.IsAutoPauseReason(scheduler.PauseReasonManual) {
		t.Error("manual 不该被当成自动原因")
	}
	for _, r := range autoPauseReasons {
		if !scheduler.IsAutoPauseReason(r) {
			t.Errorf("%q 列在 autoPauseReasons 里，却不是自动原因", r)
		}
	}
}

// 点「恢复休息」时记不记忽略，取决于该原因的条件**此刻**是否仍成立。
// 两边都要钉：记漏了按钮点了没反应，记多了下一场会议彻底静音。
func TestIgnoreOnlyForStillActiveReasons(t *testing.T) {
	// 进入侧去抖：一律连推两拍让条件成立
	enterDnd := func(a *App) {
		a.applyAutoPause(true, pauseReasonDnd, &a.dndActiveLogged, "", "")
		a.applyAutoPause(true, pauseReasonDnd, &a.dndActiveLogged, "", "")
	}

	t.Run("条件仍在：记忽略，不再挂回来", func(t *testing.T) {
		a, s := newWatchApp(t, shortCountdown)
		enterDnd(a)
		a.schedPauseToggle()
		if !a.isAutoPauseIgnored(pauseReasonDnd) {
			t.Fatal("条件仍成立时点恢复应记忽略")
		}
		// 忽略期间连推两拍也不重新暂停
		a.applyAutoPause(true, pauseReasonDnd, &a.dndActiveLogged, "", "")
		a.applyAutoPause(true, pauseReasonDnd, &a.dndActiveLogged, "", "")
		if s.IsPaused() {
			t.Fatalf("忽略期间不该重新暂停，得到 %v", s.StateCopy().PauseReasons)
		}
	})

	t.Run("条件已消失：不记忽略，下一场会议照常暂停", func(t *testing.T) {
		a, s := newWatchApp(t, shortCountdown)
		enterDnd(a)
		a.applyAutoPause(false, pauseReasonDnd, &a.dndActiveLogged, "", "")
		if !s.IsPaused() {
			t.Fatal("条件消失后应保持暂停（等用户手动恢复）")
		}

		a.schedPauseToggle()
		if s.IsPaused() {
			t.Fatalf("点恢复后应结束暂停，得到 %v", s.StateCopy().PauseReasons)
		}
		if a.isAutoPauseIgnored(pauseReasonDnd) {
			t.Fatal("条件已消解的原因不该记忽略，否则下一场会议不再自动暂停")
		}

		enterDnd(a)
		if !s.IsPaused() {
			t.Fatal("下一场会议应照常自动暂停（说明忽略没被误记）")
		}
	})
}

// 保持暂停期间原因留在 PauseReasons 里，条件再次成立时不该叠出第二条。
// 这条钉的是「不自动恢复」带来的副作用：以前条件消失会摘掉原因，现在不摘了。
func TestHoldPausedReasonNotDuplicated(t *testing.T) {
	a, s := newWatchApp(t, shortCountdown)
	a.applyAutoPause(true, pauseReasonFullScr, &a.fsActiveLogged, "", "")
	a.applyAutoPause(true, pauseReasonFullScr, &a.fsActiveLogged, "", "") // 第二拍确认
	a.applyAutoPause(false, pauseReasonFullScr, &a.fsActiveLogged, "", "")
	// 条件消失后去抖计数清零：再次成立要重新攒满两拍
	a.applyAutoPause(true, pauseReasonFullScr, &a.fsActiveLogged, "", "")
	a.applyAutoPause(true, pauseReasonFullScr, &a.fsActiveLogged, "", "")
	st := s.StateCopy()
	if len(st.PauseReasons) != 1 || st.PauseReasons[0] != pauseReasonFullScr {
		t.Fatalf("同一原因不该叠加，得到 %v", st.PauseReasons)
	}
}

// 条件消失后「保持暂停」，退出日志只能打一次。
//
// 回归的是 2026-11-05 真机现象：一次只持续 3 秒的系统「勿扰(busy)」把软件挂进暂停，
// 之后每 3 秒重打一条「…已关闭：恢复提醒；按策略保持暂停，等用户手动恢复」，十几分钟
// 刷出近 300 行 —— 47 KB 的 app.log 里九成是它，真实现场全被挤进滚动淘汰。
func TestHoldPausedExitLogsOnce(t *testing.T) {
	a, s := newWatchApp(t, shortCountdown)

	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(old)

	const enter, exit = "进入：勿扰", "退出：勿扰"
	a.applyAutoPause(true, pauseReasonDnd, &a.dndActiveLogged, enter, exit)
	a.applyAutoPause(true, pauseReasonDnd, &a.dndActiveLogged, enter, exit) // 第二拍确认
	for i := 0; i < 10; i++ {
		a.applyAutoPause(false, pauseReasonDnd, &a.dndActiveLogged, enter, exit)
	}
	if n := strings.Count(buf.String(), "按策略保持暂停"); n != 1 {
		t.Fatalf("退出日志应只打一次，实际 %d 次：\n%s", n, buf.String())
	}
	if !s.IsPaused() {
		t.Fatal("保持暂停期间不该恢复提醒")
	}

	// 条件再次成立：重新报一次「暂停提醒」（用户可能真的又开了勿扰），且不叠原因
	a.applyAutoPause(true, pauseReasonDnd, &a.dndActiveLogged, enter, exit)
	a.applyAutoPause(true, pauseReasonDnd, &a.dndActiveLogged, enter, exit)
	if n := strings.Count(buf.String(), enter); n != 2 {
		t.Fatalf("条件二次成立应重新报一次进入日志，实际 %d 次：\n%s", n, buf.String())
	}
	if st := s.StateCopy(); len(st.PauseReasons) != 1 {
		t.Fatalf("同一原因不该叠加，得到 %v", st.PauseReasons)
	}
}

// 进入侧去抖：单次采样尖峰（一次 3 秒的勿扰 / 全屏 / 投屏闪现）不把提醒挂进暂停。
//
// 回归 2026-11-05 真机根因的上游：那次是 20:02:19 一条 QUNS_BUSY 就暂停、20:02:22
// 条件就没了。加去抖后，这种「成立一拍就消失」的尖峰连暂停都不会触发，后面的
// 「按策略保持暂停」刷屏更无从发生。
func TestAutoPauseEnterDebounce(t *testing.T) {
	// 四条「保持暂停」原因都走 applyAutoPause，共用一个去抖逻辑；抽一条（勿扰）钉死行为，
	// 其余三条由 TestAutoPauseManualResumeIgnoresUntilConditionClears 的表格顺带覆盖。
	a, s := newWatchApp(t, shortCountdown)

	// ① 第一拍成立：不暂停、不打进入日志
	a.applyAutoPause(true, pauseReasonDnd, &a.dndActiveLogged, "in", "out")
	if s.IsPaused() {
		t.Fatalf("单拍成立不该暂停（去抖），得到 %v", s.StateCopy().PauseReasons)
	}
	if a.dndActiveLogged {
		t.Fatal("单拍成立不该置「已记录」标记")
	}

	// ② 第二拍条件消失：尖峰到此为止，整段作废，仍不暂停
	a.applyAutoPause(false, pauseReasonDnd, &a.dndActiveLogged, "in", "out")
	if s.IsPaused() {
		t.Fatalf("尖峰消失后不该暂停，得到 %v", s.StateCopy().PauseReasons)
	}

	// ③ 连续两拍成立：确认暂停
	a.applyAutoPause(true, pauseReasonDnd, &a.dndActiveLogged, "in", "out")
	a.applyAutoPause(true, pauseReasonDnd, &a.dndActiveLogged, "in", "out")
	if !s.IsPaused() || len(s.StateCopy().PauseReasons) != 1 {
		t.Fatalf("连续两拍应暂停，得到 %v", s.StateCopy().PauseReasons)
	}
}

// 托盘提示：带「暂停了多久」，条件全消失时补一句「可以恢复」。
// 自动暂停不再自己恢复，用户想知道什么时候该去点，只有托盘这一处能告诉他。
func TestTrayTipShowsPauseDurationAndReadyHint(t *testing.T) {
	now := nowMs()
	st := model.SchedulerState{
		Paused:       true,
		PauseReasons: []string{pauseReasonDnd},
		PausedAt:     now - 72*60_000,
	}

	tip, status, _, pause := trayTexts(st, false)
	if want := "已暂停 1 小时 12 分钟：勿扰"; tip != want {
		t.Errorf("tooltip = %q，期望 %q", tip, want)
	}
	if status != "状态：已暂停" {
		t.Errorf("状态行 = %q", status)
	}
	if pause != "恢复休息" {
		t.Errorf("暂停项 = %q，应为「恢复休息」", pause)
	}

	tipReady, statusReady, _, _ := trayTexts(st, true)
	if !strings.Contains(tipReady, "可以恢复") {
		t.Errorf("条件已结束时 tooltip 应提示可以恢复，得到 %q", tipReady)
	}
	if statusReady != "状态：已暂停（条件已结束）" {
		t.Errorf("状态行 = %q", statusReady)
	}
}

func TestFmtTrayPaused(t *testing.T) {
	cases := []struct {
		ms   int64
		want string
	}{
		{0, "不到 1 分钟"},
		{59_999, "不到 1 分钟"},
		{60_000, "1 分钟"},
		{42*60_000 + 59_000, "42 分钟"}, // 向下取整：既成事实不能四舍五入成 43
		{59 * 60_000, "59 分钟"},
		{60 * 60_000, "1 小时"},
		{72 * 60_000, "1 小时 12 分钟"},
		{25 * 60 * 60_000, "1 天 1 小时"},
	}
	for _, c := range cases {
		if got := fmtTrayPaused(c.ms); got != c.want {
			t.Errorf("fmtTrayPaused(%d) = %q，期望 %q", c.ms, got, c.want)
		}
	}
}
