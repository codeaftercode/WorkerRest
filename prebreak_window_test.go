package main

import (
	"testing"
	"time"
)

// 提前提示窗「在场判据」的回归（对应真实反馈：还有 1200 秒就弹出提前通知窗）。
//
// 窗口是**状态的影子**：只有 phase=pre 且剩余 ≤ 提前量时才该在屏幕右下角。
// 计划被重锚 / 推迟 / 跳过 / 开始休息之后，窗口必须立刻退场 —— 否则它会带着
// 新一轮的整段倒计时留在屏幕上，用户看到的就是「倒计时刚起就弹提示窗」。

// recHost 记录型宿主：记录每次提前通知时的剩余毫秒，以及收窗次数。
type recHost struct {
	*fakeHost
	remainAt []int64
	sched    *Scheduler
	closes   int
}

func (h *recHost) onPreNotify(kind string, nb int64) {
	h.fakeHost.onPreNotify(kind, nb)
	if h.sched != nil {
		h.remainAt = append(h.remainAt, h.sched.remainingMs())
	}
}

func (h *recHost) closePreBreak() { h.closes++ }

// newRecSched 造一个带记录宿主的调度器（long 倒计时 20 分钟 / 5 分钟 / 提前 10 秒）
func newRecSched(t *testing.T) (*Scheduler, *recHost) {
	t.Helper()
	cfg := `{"short":{"enabled":false,"mode":"countdown","intervalMs":600000,` +
		`"durationMs":20000,"notifyBeforeMs":10000,"postponeMs":120000,"slots":[]},` +
		`"long":{"enabled":true,"mode":"countdown","intervalMs":1200000,` +
		`"durationMs":300000,"notifyBeforeMs":10000,"postponeMs":120000,"slots":[]}}`
	base := &fakeHost{cfg: testSettings(t, cfg)}
	s := newSchedulerWithHost(base)
	r := &recHost{fakeHost: base, sched: s}
	s.host = r
	s.replan()
	return s, r
}

// 空闲暂停 → 回来：resume 在提前区间内会弹窗（此时剩余是对的），紧接着
// reanchorCountdown 把计划推回「现在 + 整轮间隔」—— 窗口必须跟着收掉。
func TestPreWindowClosedWhenCountdownReanchored(t *testing.T) {
	s, r := newRecSched(t)
	// 「暂停在最后 5 秒」：冻结的剩余 = NextAt - PausedAt = 5s
	s.mu.Lock()
	s.state.Paused = true
	s.state.PausedAt = nowMs() - 60_000
	s.state.NextAt = s.state.PausedAt + 5_000
	for i := range s.state.Lines {
		s.state.Lines[i].At = s.state.NextAt
	}
	s.state.PauseReasons = []string{pauseReasonIdle}
	s.mu.Unlock()

	s.resume(pauseReasonIdle)
	if len(r.remainAt) == 0 {
		t.Fatal("前置：剩余 5 秒在提前区间内，resume 应弹提前提示窗")
	}
	if ms := r.remainAt[len(r.remainAt)-1]; ms > 10_000 {
		t.Fatalf("前置：resume 弹窗时剩余 %d ms，应 ≤ 提前量 10000 ms", ms)
	}
	closesBefore := r.closes
	s.reanchorCountdown()

	remain := s.stateCopy().NextAt - nowMs()
	if remain < 1_100_000 {
		t.Fatalf("前置：重锚应把计划推回整轮间隔（20 分钟），实际剩余 %d ms", remain)
	}
	if r.closes == closesBefore {
		t.Fatalf("重锚把计划推到 %d ms 之后，提前提示窗却还开着（窗口会显示一整轮倒计时）", remain)
	}
}

// 托盘 / 设置页侧栏 / 快捷键点「推迟休息」直接调调度器，不经过 PreBreakAction。
func TestPreWindowClosedWhenPostponedFromTray(t *testing.T) {
	s, r := newRecSched(t)
	s.mu.Lock()
	s.state.Phase = "pre"
	s.mu.Unlock()
	closesBefore := r.closes

	s.postpone()
	if got := s.stateCopy().Phase; got != "idle" {
		t.Fatalf("前置：推迟后应回到 idle，得到 %q", got)
	}
	if r.closes == closesBefore {
		t.Fatal("推迟后提前提示窗应关闭（否则窗口继续显示已失效的提醒倒计时）")
	}
}

// 判据本身：phase=pre 但计划已被推远（剩余远超提前量）时不该在场 —— 这是
// 「窗口带着满格倒计时」的直接成因。
func TestPreBreakWantedRequiresNotifyHorizon(t *testing.T) {
	s, _ := newRecSched(t)
	s.mu.Lock()
	s.state.Phase = "pre"
	s.state.NextAt = nowMs() + 1_200_000 // 整轮 20 分钟
	s.mu.Unlock()
	if s.preBreakWanted() {
		t.Fatal("剩余 1200 秒远超提前量 10 秒，窗口不该在场")
	}

	s.mu.Lock()
	s.state.NextAt = nowMs() + 5_000
	s.mu.Unlock()
	if !s.preBreakWanted() {
		t.Fatal("剩余 5 秒在提前区间内，窗口该在场")
	}

	// 到点（已过期）由休息窗口接管，提示窗退场
	s.mu.Lock()
	s.state.NextAt = nowMs() - 1_000
	s.mu.Unlock()
	if s.preBreakWanted() {
		t.Fatal("已过点应由休息窗口接管，提前提示窗不该在场")
	}

	// 暂停期间一律不在场
	s.mu.Lock()
	s.state.NextAt = nowMs() + 5_000
	s.state.Paused = true
	s.mu.Unlock()
	if s.preBreakWanted() {
		t.Fatal("暂停期间提前提示窗不该在场")
	}
}

// 真实场景（用户报的那种）：人走开 → 倒计时在离开期间走到点 → 5 分钟后空闲自动暂停
// （提示窗已在暂停时收掉）→ 回来恢复 + 重锚。此时**不该**再弹提示窗，
// 更不该让它挂着一整轮倒计时（1200 秒）。
func TestNoPreWindowWhenAwayThroughDueTime(t *testing.T) {
	s, r := newRecSched(t)
	// 暂停已 6 分钟，冻结那一刻的剩余已经到期（暂停判定滞后于倒计时到点）
	s.mu.Lock()
	s.state.Paused = true
	s.state.PausedAt = nowMs() - 360_000
	s.state.NextAt = s.state.PausedAt - 20_000
	for i := range s.state.Lines {
		s.state.Lines[i].At = s.state.NextAt
	}
	s.state.PauseReasons = []string{pauseReasonIdle}
	s.mu.Unlock()

	s.resume(pauseReasonIdle)
	s.reanchorCountdown()

	if len(r.remainAt) != 0 {
		t.Fatalf("离开期间已到点，恢复时不该弹提前提示窗（实际弹了 %d 次，剩余 %v ms）",
			len(r.remainAt), r.remainAt)
	}
	if s.preBreakWanted() {
		t.Fatal("重锚后计划在整轮间隔之后，提前提示窗不该在场")
	}
}

// 自然多轮循环（压缩时间）：提前通知只应在剩余 ≤ 提前量时弹出。
func TestPreNotifyFiresOnlyInsideNotifyWindow(t *testing.T) {
	cfg := `{"short":{"enabled":false,"mode":"countdown","intervalMs":600000,` +
		`"durationMs":20000,"notifyBeforeMs":10000,"postponeMs":120000,"slots":[]},` +
		`"long":{"enabled":true,"mode":"countdown","intervalMs":3000,` +
		`"durationMs":1000,"notifyBeforeMs":800,"postponeMs":120000,"slots":[]}}`
	base := &fakeHost{cfg: testSettings(t, cfg)}
	s := newSchedulerWithHost(base)
	r := &recHost{fakeHost: base, sched: s}
	s.host = r
	s.replan()

	deadline := time.Now().Add(11 * time.Second)
	for time.Now().Before(deadline) {
		s.onTick()
		time.Sleep(50 * time.Millisecond)
	}
	if len(r.remainAt) < 2 {
		t.Fatalf("压缩时间下 11 秒应至少弹过两次提前通知，实际 %d 次", len(r.remainAt))
	}
	for i, ms := range r.remainAt {
		if ms > 800 {
			t.Fatalf("第 %d 次提前通知时还剩 %d ms，超出提前量 800 ms —— 弹早了", i+1, ms)
		}
		t.Logf("第 %d 次提前通知：剩余 %d ms", i+1, ms)
	}
}
