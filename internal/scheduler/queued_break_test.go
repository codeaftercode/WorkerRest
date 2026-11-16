package scheduler

// 提前结束的排队接力在调度器这一侧的契约：StartQueuedBreak 把“已经消费过、
// 现在轮到显示”的那一班直接弹成窗口，但**不能再写一次 consumed** ——
// 它带进来的 at 是别的提醒的计划时刻，拿去记 OccurrenceKey(kind, at) 有可能
// 把这个身份真正的下一班误标成已处理，那一班就凭空消失了。

import "testing"

func TestStartQueuedBreakOpensWindowWithoutConsuming(t *testing.T) {
	s, _ := newTestSched(t, earlyEndCfg)
	s.ResetCycle()

	s.mu.Lock()
	before := len(s.consumed)
	s.mu.Unlock()

	id := s.StartQueuedBreak("cb-a")

	s.mu.Lock()
	after := len(s.consumed)
	phase, bt := s.state.Phase, s.state.BreakType
	ends := s.state.BreakEndsAt
	s.mu.Unlock()

	if id == "" {
		t.Fatal("接力应当真的弹出一扇窗（返回会话 id）")
	}
	if after != before {
		t.Fatalf("接力弹窗不该新增 consumed（这一班在并入时就消费过了），实际 %d -> %d", before, after)
	}
	if phase != "break" || bt != "cb-a" {
		t.Fatalf("应停在 cb-a 的休息相位，实际 phase=%q breakType=%q", phase, bt)
	}
	if ends <= 0 {
		t.Fatalf("新窗应当有自己的结束时刻，实际 %d", ends)
	}
}

// 已经在休息时不该再“接力”——调用方据此走“接不上窗口”的兜底（补记账），
// 否则会把正开着的这扇窗顶掉。
func TestStartQueuedBreakRejectedWhileBreaking(t *testing.T) {
	s, _ := newTestSched(t, earlyEndCfg)
	s.ResetCycle()
	s.BreakNowOf("short")

	if id := s.StartQueuedBreak("cb-a"); id != "" {
		t.Fatalf("休息进行中不该再开一扇窗，实际返回 %q", id)
	}
	s.mu.Lock()
	bt := s.state.BreakType
	s.mu.Unlock()
	if bt != "short" {
		t.Fatalf("原来那次休息的身份不该被改掉，实际 %q", bt)
	}
}
