package scheduler

import (
	"testing"
	"time"
)

// 定时手动暂停（PauseUntil）的三条不变量：
//   一、到期时刻随状态走，前端 / 托盘 / 诊断页都能读到；
//   二、改时长不重置 PausedAt —— 把 1 小时改成 2 小时，已经过去的那段不该从头算；
//   三、恢复时清掉到期时刻，且按「离开」口径重锚（与无限手动暂停的平移刻意不同）。

func TestPauseUntilSetsDeadline(t *testing.T) {
	s, _ := newTestSched(t, shortCountdown)
	before := nowMs()
	s.PauseUntil(before + 3_600_000)

	st := s.StateCopy()
	if !st.Paused {
		t.Fatal("PauseUntil 应进入暂停")
	}
	if d := st.PauseDeadline - (before + 3_600_000); d < -2000 || d > 2000 {
		t.Fatalf("到期时刻应≈现在 + 1 小时，得到 %d（差 %d ms）", st.PauseDeadline, d)
	}
	firstPausedAt := st.PausedAt

	// 改成 2 小时：只改到期时刻
	time.Sleep(20 * time.Millisecond)
	s.PauseUntil(nowMs() + 7_200_000)
	st = s.StateCopy()
	if st.PausedAt != firstPausedAt {
		t.Fatalf("改时长不该重置暂停起点：want=%d got=%d", firstPausedAt, st.PausedAt)
	}
	if st.PauseDeadline == 0 {
		t.Fatal("改时长后应仍有到期时刻")
	}

	// 取消定时 → 退回「一直暂停」
	s.PauseUntil(0)
	st = s.StateCopy()
	if !st.Paused {
		t.Fatal("取消定时不该恢复暂停")
	}
	if st.PauseDeadline != 0 {
		t.Fatalf("取消定时后到期时刻应为 0，得到 %d", st.PauseDeadline)
	}
}

// 恢复时清掉到期时刻：否则下一段暂停会带着上一段的截止时间，托盘一挂上就显示
// 「还有 0 分钟」。
func TestResumeClearsPauseDeadline(t *testing.T) {
	s, _ := newTestSched(t, shortCountdown)
	s.PauseUntil(nowMs() + 600_000)
	s.Resume(PauseReasonManual)

	st := s.StateCopy()
	if st.Paused {
		t.Fatal("恢复后不该仍在暂停")
	}
	if st.PauseDeadline != 0 {
		t.Fatalf("恢复后到期时刻应清零，得到 %d", st.PauseDeadline)
	}
}

// 定时暂停恢复时重锚内置倒计时。
//
// 跨夜场景是这条判据的由来：晚上设「到明天」、零点前几分钟合盖睡觉，空闲检测还
// 没挂上就到了期；不重锚的话第二天开机会接着昨夜冻结的剩余时间走，几分钟后就弹。
func TestTimedPauseResumeReanchorsCountdown(t *testing.T) {
	s, _ := newTestSched(t, shortCountdown)
	s.replan()
	// 模拟「这一轮只剩 2 分钟」
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

	s.PauseUntil(nowMs() + 600_000)
	time.Sleep(50 * time.Millisecond)
	s.Resume(PauseReasonManual)

	if remain := s.RemainingMs(); remain < 599_000 || remain > 600_100 {
		t.Fatalf("定时暂停恢复应重锚到满间隔 600000 ms，得到 %d（按剩余 120000 平移是无限手动暂停的口径）", remain)
	}
}

// 对照：无限手动暂停恢复仍按剩余平移（既有语义，别被定时暂停的改动带偏）。
func TestUntimedManualPauseStillKeepsProgress(t *testing.T) {
	s, _ := newTestSched(t, shortCountdown)
	s.replan()
	s.mu.Lock()
	s.state.NextAt = nowMs() + 120_000
	s.mu.Unlock()

	s.Pause(PauseReasonManual)
	time.Sleep(30 * time.Millisecond)
	s.Resume(PauseReasonManual)

	if remain := s.RemainingMs(); remain < 119_000 || remain > 121_000 {
		t.Fatalf("无限手动暂停恢复应保留剩余约 120000 ms，得到 %d", remain)
	}
}

// 「取消定时」不能把纯自动暂停偷偷变成手动暂停：PauseUntil(0) 在没有 manual 的时候
// 应该是空操作，否则托盘上会凭空多一条 manual，条件消失后也不自动恢复。
func TestPauseUntilZeroDoesNotCreateManualPause(t *testing.T) {
	s, _ := newTestSched(t, shortCountdown)
	s.Pause("勿扰")
	s.PauseUntil(0)
	s.mu.Lock()
	hasManual := containsStr(s.state.PauseReasons, PauseReasonManual)
	s.mu.Unlock()
	if hasManual {
		t.Fatalf("取消定时不该凭空加一条 manual，得到 %v", s.StateCopy().PauseReasons)
	}
}

// TestManualResumeEndsTimedSegment 定时暂停中用户点「恢复休息」，但别的自动原因还挂着
// （勿扰 / 投屏没解除）→ 暂停继续，可这段**手动定时**已经结束了：
// 到期时刻必须跟着清掉，否则托盘会继续显示「还有 25 分钟（到 22:30）」、菜单里还挂着
// 「取消定时（一直暂停）」，而用户刚刚亲手结束的就是这段定时暂停。
func TestManualResumeEndsTimedSegment(t *testing.T) {
	s, _ := newTestSched(t, shortCountdown)
	s.Pause("勿扰")
	s.PauseUntil(nowMs() + 25*60_000)

	if d := s.StateCopy().PauseDeadline; d == 0 {
		t.Fatal("前置：定时暂停应有到期时刻")
	}

	s.Resume(PauseReasonManual)

	st := s.StateCopy()
	if !st.Paused {
		t.Fatal("勿扰还挂着，不该恢复")
	}
	if st.PauseDeadline != 0 {
		t.Fatalf("手动这段结束后到期时刻应清零，得到 %d", st.PauseDeadline)
	}
	if !containsStr(st.PauseReasons, "勿扰") {
		t.Fatalf("勿扰原因不该被动到，得到 %v", st.PauseReasons)
	}
	// 再恢复勿扰：整段结束，一切归零
	s.Resume("勿扰")
	st = s.StateCopy()
	if st.Paused || st.PauseDeadline != 0 || st.PausedAt != 0 {
		t.Fatalf("全部恢复后应干净归零，得到 paused=%v deadline=%d pausedAt=%d",
			st.Paused, st.PauseDeadline, st.PausedAt)
	}
}

// TestResumeClearsPausedAt 恢复后段起点必须清零：留着上一段的时刻，任何
// 「pausedAt != 0 就认为暂停过」的判据都会误读（状态快照里也带着一个没有意义的旧值）。
func TestResumeClearsPausedAt(t *testing.T) {
	s, _ := newTestSched(t, shortCountdown)
	s.Pause("勿扰")
	if s.StateCopy().PausedAt == 0 {
		t.Fatal("前置：暂停中应有段起点")
	}
	s.Resume("勿扰")
	if got := s.StateCopy().PausedAt; got != 0 {
		t.Fatalf("恢复后段起点应清零，得到 %d", got)
	}
	// 下一段必须是新时刻（「已暂停多久」与小窗节流都以它为段标识）
	s.Pause("勿扰")
	if s.StateCopy().PausedAt == 0 {
		t.Fatal("新一段暂停应重新记段起点")
	}
}
