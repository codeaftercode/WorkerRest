package update

import (
	"testing"
	"time"
)

// fixedRand 固定随机时长，便于断言排期时刻。
func fixedRand(d time.Duration) func(time.Duration) time.Duration {
	return func(time.Duration) time.Duration { return d }
}

// TestTickDailyTiming 当天首次键鼠活动后，3 小时内随机一刻才真正检查。
func TestTickDailyTiming(t *testing.T) {
	const day = "2026-10-23"
	t0 := time.Date(2026, 10, 23, 9, 0, 0, 0, time.Local)
	var st State

	// 1) 还没有键鼠活动：不排期（一直不用电脑就永远不检查）
	if act := Tick(&st, day, t0, false, false, false, fixedRand(time.Hour)); act != ActNone {
		t.Fatalf("无活动时不该有动作，实得 %q", act)
	}
	if !st.ArmedAt.IsZero() {
		t.Fatalf("无活动时不该排期，实得 %v", st.ArmedAt)
	}

	// 2) 首次活动：只在 3 小时窗内排期，不当场检查
	if act := Tick(&st, day, t0, true, false, false, fixedRand(time.Hour)); act != ActNone {
		t.Fatalf("首次活动后不该立刻检查，实得 %q", act)
	}
	if want := t0.Add(time.Hour); !st.ArmedAt.Equal(want) {
		t.Fatalf("排期时刻 = %v，期望 %v", st.ArmedAt, want)
	}

	// 3) 未到点：什么也不做
	if act := Tick(&st, day, t0.Add(59*time.Minute), true, false, false, fixedRand(time.Hour)); act != ActNone {
		t.Fatalf("未到点不该检查，实得 %q", act)
	}

	// 4) 到点且不在暂停休息：去检查，并清掉本次排期
	if act := Tick(&st, day, t0.Add(time.Hour), true, false, false, fixedRand(time.Hour)); act != ActCheck {
		t.Fatalf("到点应检查更新，实得 %q", act)
	}
	if !st.ArmedAt.IsZero() {
		t.Fatalf("检查后应清空排期，实得 %v", st.ArmedAt)
	}
}

// TestTickRetriesWhilePaused 到点若处于暂停休息，30 分钟内再随机，反复。
func TestTickRetriesWhilePaused(t *testing.T) {
	const day = "2026-10-23"
	t0 := time.Date(2026, 10, 23, 9, 0, 0, 0, time.Local)
	st := State{Day: day}
	Tick(&st, day, t0, true, false, false, fixedRand(time.Hour))
	at := t0.Add(time.Hour)

	// 第 1 次到点：暂停 → 推 10 分钟
	if act := Tick(&st, day, at, true, true, false, fixedRand(10*time.Minute)); act != ActNone {
		t.Fatalf("暂停时不该检查，实得 %q", act)
	}
	if want := at.Add(10 * time.Minute); !st.ArmedAt.Equal(want) {
		t.Fatalf("重试时刻 = %v，期望 %v", st.ArmedAt, want)
	}

	// 第 2 次到点：还暂停 → 再推 5 分钟
	at = at.Add(10 * time.Minute)
	if act := Tick(&st, day, at, true, true, false, fixedRand(5*time.Minute)); act != ActNone {
		t.Fatalf("仍暂停时不该检查，实得 %q", act)
	}
	if want := at.Add(5 * time.Minute); !st.ArmedAt.Equal(want) {
		t.Fatalf("重试时刻 = %v，期望 %v", st.ArmedAt, want)
	}

	// 第 3 次到点：已恢复 → 检查
	if act := Tick(&st, day, at.Add(5*time.Minute), true, false, false, fixedRand(time.Minute)); act != ActCheck {
		t.Fatalf("恢复后到点应检查，实得 %q", act)
	}
}

// TestTickResetsAtMidnight 跨天整体重置：昨晚没查成不影响今天。
func TestTickResetsAtMidnight(t *testing.T) {
	const d1, d2 = "2026-10-23", "2026-10-24"
	st := State{Day: d1}
	late := time.Date(2026, 10, 23, 23, 50, 0, 0, time.Local)
	Tick(&st, d1, late, true, false, false, fixedRand(2*time.Hour)) // 排到次日 01:50
	if st.ArmedAt.IsZero() {
		t.Fatalf("应已排期")
	}

	// 次日 00:30：跨天 → 排期作废，回到「等今天首次活动」（此刻无活动）
	act := Tick(&st, d2, time.Date(2026, 10, 24, 0, 30, 0, 0, time.Local),
		false, false, false, fixedRand(time.Hour))
	if act != ActNone {
		t.Fatalf("跨天不该检查，实得 %q", act)
	}
	if st.Day != d2 || !st.ArmedAt.IsZero() {
		t.Fatalf("跨天应整体重置，实得 day=%q armed=%v", st.Day, st.ArmedAt)
	}

	// 新的一天首次活动 → 重新按 3 小时窗排期（前一天一直暂停没查成也不影响）
	next := time.Date(2026, 10, 24, 8, 0, 0, 0, time.Local)
	Tick(&st, d2, next, true, true, false, fixedRand(30*time.Minute))
	if want := next.Add(30 * time.Minute); !st.ArmedAt.Equal(want) {
		t.Fatalf("新一天的排期 = %v，期望 %v（暂停不影响「首次活动后 3 小时窗」的起算）", st.ArmedAt, want)
	}
}

// TestTickSkipsWhenDone 当天已查过 → 不再排期、不再检查。
func TestTickSkipsWhenDone(t *testing.T) {
	const day = "2026-10-23"
	now := time.Date(2026, 10, 23, 10, 0, 0, 0, time.Local)
	st := State{Day: day, ArmedAt: now}
	if act := Tick(&st, day, now, true, false, true, fixedRand(time.Hour)); act != ActNone {
		t.Fatalf("当天已查过不该再检查，实得 %q", act)
	}
	if !st.ArmedAt.IsZero() {
		t.Fatalf("当天已查过应清空排期，实得 %v", st.ArmedAt)
	}
}
