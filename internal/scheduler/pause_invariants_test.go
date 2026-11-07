package scheduler

import (
	"fmt"
	"math/rand"
	"testing"
)

// 暂停状态机的不变量（随机操作序列）。
//
// 为什么需要这一组：单条用例只能钉住“我想到的那条路径”。暂停的入口有五条自动原因 +
// 手动 + 定时，出口有 Resume / ResumeQuiet / PauseToggle / 到期判定，两两叠加就是几十种
// 组合。真正会出错的往往是“A 摘掉之后 B 还挂着”这种中间态 —— 状态字段之间对不上，
// 界面就开始说胡话（托盘写着“还有 25 分钟”，实际那段暂停早被用户结束了）。
//
// 这里用固定种子的随机序列反复穿越这些入口，每一步都检查同一组不变量。种子固定，
// 失败可复现（报错里带上第几步、做的什么操作、当时的状态）。

// pauseInvariants 每一步之后都必须成立的事实。
func pauseInvariants(t *testing.T, s *Scheduler, step int, op string) {
	t.Helper()
	st := s.StateCopy()
	where := fmt.Sprintf("第 %d 步 %s 之后：paused=%v reasons=%v pausedAt=%d deadline=%d",
		step, op, st.Paused, st.PauseReasons, st.PausedAt, st.PauseDeadline)

	// 1. Paused 与 PauseReasons 必须互为表里：任何一侧单独成立都会让“恢复”按钮
	//    点了没反应（原因不在）或暂停永远解不开（Paused 卡住）。
	if st.Paused != (len(st.PauseReasons) > 0) {
		t.Fatalf("%s：Paused 与 PauseReasons 不一致", where)
	}
	// 2. 暂停中必须有段起点（“已暂停多久”与小窗节流都以它为锚），恢复后必须清零。
	if st.Paused && st.PausedAt <= 0 {
		t.Fatalf("%s：暂停中却没有 PausedAt", where)
	}
	if !st.Paused && st.PausedAt != 0 {
		t.Fatalf("%s：已恢复却留着 PausedAt", where)
	}
	// 3. 恢复后不该残留到期时刻：下一次暂停会把它当成自己的截止时间。
	if !st.Paused && st.PauseDeadline != 0 {
		t.Fatalf("%s：已恢复却留着 PauseDeadline", where)
	}
	// 4. 到期时刻只属于“手动（定时）暂停”：manual 都不在了，界面不该再说
	//    “还有 25 分钟”——那段暂停已经被用户结束，剩下的只是别的自动原因。
	if st.PauseDeadline != 0 && !containsStr(st.PauseReasons, PauseReasonManual) {
		t.Fatalf("%s：没有 manual 却留着到期时刻（托盘会继续显示“还有 …”）", where)
	}
	// 5. 同一原因不许重复登记：重复会让“摘掉一条”之后暂停还挂着，
	//    用户点多少次恢复都没用。
	seen := make(map[string]bool, len(st.PauseReasons))
	for _, r := range st.PauseReasons {
		if seen[r] {
			t.Fatalf("%s：原因 %q 重复登记", where, r)
		}
		seen[r] = true
	}
}

// TestPauseResumeInvariants 用固定种子的随机序列穿越全部暂停入口，每步验不变量。
func TestPauseResumeInvariants(t *testing.T) {
	s, _ := newTestSched(t, shortCountdown)
	s.replan()

	reasons := []string{
		PauseReasonManual,
		PauseReasonIdle,
		"勿扰", "全屏或演示", "复制模式", "无线投放", "应用占用",
	}
	rng := rand.New(rand.NewSource(20261105))

	for step := 1; step <= 400; step++ {
		var op string
		switch rng.Intn(5) {
		case 0:
			r := reasons[rng.Intn(len(reasons))]
			op = "Pause(" + r + ")"
			s.Pause(r)
		case 1:
			r := reasons[rng.Intn(len(reasons))]
			op = "Resume(" + r + ")"
			s.Resume(r)
		case 2:
			r := reasons[rng.Intn(len(reasons))]
			op = "ResumeQuiet(" + r + ")"
			s.ResumeQuiet(r)
		case 3:
			// 定时暂停：有时给未来时刻，有时给“取消定时”
			if rng.Intn(3) == 0 {
				op = "PauseUntil(0)"
				s.PauseUntil(0)
			} else {
				op = "PauseUntil(+30min)"
				s.PauseUntil(nowMs() + 30*60_000)
			}
		default:
			op = "PauseToggle"
			s.PauseToggle()
		}
		pauseInvariants(t, s, step, op)
	}
}

// TestPauseReasonIndependentResume 多条原因并存时，摘掉中间那一条既不能提前恢复，
// 也不能把别的原因一起摘掉（为“勿扰恢复”把投屏一起静音就是这么来的）。
func TestPauseReasonIndependentResume(t *testing.T) {
	s, _ := newTestSched(t, shortCountdown)

	s.Pause("勿扰")
	s.Pause("复制模式")
	s.Pause("应用占用")
	if got := s.StateCopy().PauseReasons; len(got) != 3 {
		t.Fatalf("三条原因应各自登记，得到 %v", got)
	}

	s.Resume("复制模式")
	st := s.StateCopy()
	if !st.Paused {
		t.Fatal("还剩两条原因，不该恢复")
	}
	if got := st.PauseReasons; len(got) != 2 || !containsStr(got, "勿扰") || !containsStr(got, "应用占用") {
		t.Fatalf("只该摘掉复制模式，得到 %v", got)
	}
}

// TestPauseSameReasonIsIdempotent 同一条原因连着来（监视器每 3 秒一跳都会重新判定）：
// 不能重复登记，更不能重置段起点 —— 否则“已暂停多久”永远停在 0 分钟。
func TestPauseSameReasonIsIdempotent(t *testing.T) {
	s, _ := newTestSched(t, shortCountdown)

	s.Pause("勿扰")
	first := s.StateCopy().PausedAt
	s.Pause("勿扰")
	s.Pause("勿扰")

	st := s.StateCopy()
	if len(st.PauseReasons) != 1 {
		t.Fatalf("重复暂停应只留一条原因，得到 %v", st.PauseReasons)
	}
	if st.PausedAt != first {
		t.Fatalf("段起点不该被重复暂停重置：%d → %d", first, st.PausedAt)
	}
}

// TestResumeUnknownReasonIsNoop 摘一条根本不存在的原因此前是“点了没反应的恢复”：
// 必须原样不动（不能把 Paused 清掉，也不能动 PausedAt）。
func TestResumeUnknownReasonIsNoop(t *testing.T) {
	s, _ := newTestSched(t, shortCountdown)

	s.Pause("勿扰")
	before := s.StateCopy()

	s.Resume("应用占用")
	s.ResumeQuiet("全屏或演示")

	st := s.StateCopy()
	if !st.Paused || len(st.PauseReasons) != 1 || st.PausedAt != before.PausedAt {
		t.Fatalf("摘不存在的原因应完全无副作用，得到 %v", st.PauseReasons)
	}
}
