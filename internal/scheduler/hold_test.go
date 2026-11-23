package scheduler

import "testing"

// 询问挂起（2026-11-20）：非独占全屏的询问窗还在等答复时，到点的那几拍先按住 ——
// 不弹窗、不消费、不重排，下一拍再看。它解决的是一幅具体的画面：询问窗与休息窗同屏。
//
// 关键约束与空闲门同构：**NextAt 必须留在过去**。一旦被重排成 now+interval，这一轮
// 提醒就被悄悄吞掉了（那正是历史上“静默丢一轮”的坑）。

// 到点先按住；解除后下一拍照常提醒。
func TestBreakHoldDefersDueBreak(t *testing.T) {
	s, host := newTestSched(t, shortCountdown)
	s.replan()
	at := forceDue(s)
	host.hold = true

	s.onTick()
	st := s.StateCopy()
	if len(host.breaks) != 0 || len(host.pre) != 0 {
		t.Fatalf("挂起期间不该弹任何窗，breaks=%v pre=%v", host.breaks, host.pre)
	}
	if st.NextAt != at {
		t.Fatalf("挂起期间 NextAt 必须留在过去（%d），得到 %d", at, st.NextAt)
	}
	if st.Phase != "idle" {
		t.Fatalf("挂起不该推进阶段，得到 %q", st.Phase)
	}

	host.hold = false
	s.onTick()
	if len(host.pre) == 0 && len(host.breaks) == 0 {
		t.Fatal("解除挂起后下一拍必须照常提醒")
	}
}

// 挂起只管“已到点”这一拍：还没到点时的提前提示不该被拦（那时也没人问）。
func TestBreakHoldDoesNotBlockPreNotify(t *testing.T) {
	s, host := newTestSched(t, shortCountdown)
	s.replan()
	s.mu.Lock()
	s.state.NextAt = nowMs() + 5000 // 落在提前提示窗口内（默认提前 10 秒）
	s.state.Phase = "idle"
	s.mu.Unlock()

	host.hold = true
	s.onTick()
	if len(host.pre) == 0 {
		t.Fatal("未到点时的提前提示不该被挂起拦住")
	}
}
