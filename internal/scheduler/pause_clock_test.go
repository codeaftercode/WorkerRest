package scheduler

import (
	"testing"
	"time"
)

// “暂停不停表”（2026-11-12 用户口径）的回归：
//
//	无论手动暂停还是自动暂停，设置页侧栏显示的长/短休息、外卖提醒 / 自定义提醒倒计时
//	都继续往下掉；到 0 时按该卡片“暂停休息时”的选中项处置。
//
// 这里钉三件事：
//  1. 暂停中剩余时间照真实时钟往下掉（不再冻结在暂停那一刻）；
//  2. 暂停中“跳过自定义提醒”必须真的消费那一跳（旧实现只记一笔跳过，恢复后还会再弹）；
//  3. 进行中的休息碰上手动暂停，按它自己的“暂停休息时”办（走完 / 跳过 / 冻结）。

func TestPausedRemainingKeepsTicking(t *testing.T) {
	s, _ := newTestSched(t, shortCountdown)
	s.replan()
	s.Pause(PauseReasonManual)
	first := s.RemainingMs()
	time.Sleep(60 * time.Millisecond)
	second := s.RemainingMs()
	if second >= first {
		t.Fatalf("手动暂停中剩余应继续减少（%d → %d）", first, second)
	}

	// 自动原因同口径：暂停原因不参与时钟
	s2, _ := newTestSched(t, shortCountdown)
	s2.replan()
	s2.Pause("勿扰 / 专注")
	a := s2.RemainingMs()
	time.Sleep(60 * time.Millisecond)
	if b := s2.RemainingMs(); b >= a {
		t.Fatalf("自动暂停中剩余也应继续减少（%d → %d）", a, b)
	}
}

// 暂停中“跳过”自定义提醒：必须真的消费那一跳（摘线 + 重排），否则恢复后它还会照常弹一次。
func TestPausedSkipCustomConsumesOccurrence(t *testing.T) {
	cfg := `{"short":{"enabled":false},"long":{"enabled":false},` +
		`"customBreaks":[{"id":"cb-water","name":"喝水提醒","config":{` +
		`"enabled":true,"pauseAction":"skip","mode":"countdown",` +
		`"intervalMs":600000,"durationMs":15000,"slots":[]}}]}`
	s, host := newTestSched(t, cfg)
	s.replan()
	s.mu.Lock()
	s.state.NextAt = nowMs() - 1000
	for i := range s.state.Lines {
		s.state.Lines[i].At = s.state.NextAt
	}
	s.mu.Unlock()

	s.Pause(PauseReasonManual)
	s.onTick()
	if len(host.skips) != 1 || host.skips[0] != "cb-water" {
		t.Fatalf("暂停中到点的自定义提醒应记一笔跳过，得到 %v", host.skips)
	}
	if got := s.StateCopy().NextAt - nowMs(); got <= 0 {
		t.Fatalf("跳过之后应重排到未来（侧栏不能停在 0:00），得到 %d ms", got)
	}

	s.Resume(PauseReasonManual)
	for i := 0; i < 3; i++ {
		s.onTick()
	}
	if len(host.breaks) != 0 {
		t.Fatalf("恢复后不该补弹已经跳过的那一跳，得到 %v", host.breaks)
	}
}

// 正在进行中的休息碰上**手动**暂停：按它自己的“暂停休息时”办。
func TestManualPauseRunningBreakFollowsCard(t *testing.T) {
	cases := []struct {
		action     string
		wantPhase  string
		wantSkips  int
		wantFrozen bool
	}{
		{"remind", "break", 0, false},  // 照常走完
		{"skip", "idle", 1, false},     // 就此结束并记账
		{"postpone", "break", 0, true}, // 冻结剩余、恢复后接着走
	}
	for _, c := range cases {
		cfg := `{"short":{"enabled":true,"mode":"countdown","intervalMs":600000,` +
			`"durationMs":20000,"pauseAction":"` + c.action + `","slots":[]}}`
		s, host := newTestSched(t, cfg)
		s.replan()
		s.mu.Lock()
		s.state.Phase = "break"
		s.state.NextType = "short"
		s.state.BreakStartedAt = nowMs()
		s.state.BreakEndsAt = nowMs() + 20_000
		s.mu.Unlock()

		s.Pause(PauseReasonManual)
		st := s.StateCopy()
		if st.Phase != c.wantPhase {
			t.Fatalf("%s：phase 应为 %q，得到 %q", c.action, c.wantPhase, st.Phase)
		}
		if len(host.skips) != c.wantSkips {
			t.Fatalf("%s：跳过次数应为 %d，得到 %v", c.action, c.wantSkips, host.skips)
		}
		if frozen := s.pauseFreezesClockLocked(); frozen != c.wantFrozen {
			t.Fatalf("%s：冻结应为 %v，得到 %v", c.action, c.wantFrozen, frozen)
		}
		if c.wantFrozen {
			// 冻结的口径：剩余时间不再变化
			before := s.RemainingMs()
			time.Sleep(60 * time.Millisecond)
			if after := s.RemainingMs(); after != before {
				t.Fatalf("推迟：剩余应冻结在 %d ms，实际 %d ms", before, after)
			}
		}
	}
}

// 固定时刻线在暂停中被“推迟”：状态卡的倒计时格要显示推迟后的时刻（dueAt），
// 不能显示那格已经过去的绝对时刻（否则侧栏一直停在 0:00）。
func TestPostponedFixedSlotShowsDueAt(t *testing.T) {
	now := time.Now()
	cfg := `{"long":{"enabled":true,"mode":"fixed","durationMs":300000,"postponeMs":120000,` +
		`"slots":[{"id":"s1","hour":` + itoa(now.Hour()) + `,"minute":` + itoa(now.Minute()) +
		`,"repeat":"daily","days":[],"enabled":true}]}}`
	s, _ := newTestSched(t, cfg)
	s.replan()
	s.Pause(PauseReasonManual)
	s.onTick()

	st := s.StateCopy()
	if st.Rest == nil {
		t.Fatal("状态卡应显示被推迟的那条长休息")
	}
	if got := st.Rest.At - nowMs(); got < 117_000 || got > 123_000 {
		t.Fatalf("状态卡应显示推迟后的时刻（≈120000 ms 后），实际 %d ms", got)
	}
}
