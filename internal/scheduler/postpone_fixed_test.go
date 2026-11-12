package scheduler

import (
	"testing"
	"time"
	"workerrest/internal/plan"
)

// 固定时刻模式的“推迟”口径（2026-11-12 用户口径）：
//
//   - 倒计时显示 = 原定时刻 + 卡片里的“推迟时长”（基准取较晚者：现在或原定时刻），
//     也就是“这一次改到几点弹”，不是重新按间隔起算；
//   - 推迟后的时刻若等于 / 晚于时刻表里的**下一次**，这次就不再单独弹了：跳过本次，
//     直接按下一班算（否则 12:00 推迟 3 小时会在 15:00 弹一次，把 14:00 那班挤掉）。
//
// 时刻条目只精确到分钟，测试用“下一个整分 + N 分钟”构造 T1，T2 = T1 + 5 分钟。

func fixedPairCfg(t *testing.T, postponeMs int64) (cfg string, t1, t2 time.Time) {
	t.Helper()
	t1 = time.Now().Truncate(time.Minute).Add(4 * time.Minute)
	t2 = t1.Add(5 * time.Minute)
	slot := func(id string, at time.Time) string {
		return `{"id":"` + id + `","hour":` + itoa(at.Hour()) + `,"minute":` + itoa(at.Minute()) +
			`,"repeat":"daily","days":[],"enabled":true}`
	}
	cfg = `{"short":{"enabled":true,"mode":"fixed","durationMs":60000,"postponeMs":` +
		itoa(int(postponeMs)) + `,"slots":[` + slot("s1", t1) + `,` + slot("s2", t2) + `]}}`
	return
}

// 没越过下一班：改到“原定 + 推迟时长”，侧栏显示那一刻，重排也不会把它抹掉。
func TestPostponeFixedKeepsDueAt(t *testing.T) {
	cfg, t1, _ := fixedPairCfg(t, 120_000) // 推迟 2 分钟（与下一班差 5 分钟）
	s, host := newTestSched(t, cfg)
	s.replan()
	if got := s.StateCopy().NextAt; got != t1.UnixMilli() {
		t.Fatalf("前置：NextAt 应是 T1=%s，得到 %s", t1, time.UnixMilli(got))
	}

	s.Postpone()
	want := t1.Add(2 * time.Minute).UnixMilli()
	st := s.StateCopy()
	if st.NextAt != want {
		t.Fatalf("推迟后倒计时应指向“原定 + 推迟时长”=%s，得到 %s",
			time.UnixMilli(want), time.UnixMilli(st.NextAt))
	}
	if st.Rest == nil || st.Rest.At != want {
		t.Fatalf("侧栏“下次休息”应显示推迟后的时刻，得到 %+v", st.Rest)
	}
	if len(host.postpones) != 1 || len(host.skips) != 0 {
		t.Fatalf("应记一次推迟、不记跳过，得到 postpones=%v skips=%v", host.postpones, host.skips)
	}
	// 卡片右上角“下次 …”与今日时间线读的是 fixedNext：一起指向推迟后的时刻
	sr := s.StateForRenderer()
	if got := sr.FixedNext["short"]; got != want {
		t.Fatalf("摘要表应显示推迟后的时刻 %s，得到 %s", time.UnixMilli(want), time.UnixMilli(got))
	}
	hasDue := false
	for _, it := range sr.Timeline {
		if it.Type == "short" && it.At == want {
			hasDue = true
		}
	}
	if !hasDue {
		t.Fatalf("今日时间线应出现推迟后的时刻 %s，得到 %+v", time.UnixMilli(want), sr.Timeline)
	}

	// 保存设置（RefreshPlan）这类重排不能把推迟抹掉：旧实现按时刻表重算，倒计时会跳回原定时刻
	s.RefreshPlan()
	if got := s.StateCopy().NextAt; got != want {
		t.Fatalf("重排后推迟时刻应保持 %s，得到 %s", time.UnixMilli(want), time.UnixMilli(got))
	}

	// 到点弹出；弹出后下一班仍是 T2（原定那班已被这一跳消费掉）
	s.mu.Lock()
	for _, p := range s.postponed {
		p.dueAt = nowMs() - 1000
	}
	s.mu.Unlock()
	s.onTick()
	if len(host.breaks) != 1 || host.breaks[0] != "short" {
		t.Fatalf("推迟后的时刻到点应弹出一次短休息，得到 %v", host.breaks)
	}
}

// 暂停期间到点的固定时刻线同样按这条规则：推迟会越过下一班时，这一班跳过（不动待办表）。
func TestPausedPostponeFixedCrossingSkipsSlot(t *testing.T) {
	t1 := time.Now().Truncate(time.Minute)
	t2 := t1.Add(3 * time.Minute)
	slot := func(id string, at time.Time) string {
		return `{"id":"` + id + `","hour":` + itoa(at.Hour()) + `,"minute":` + itoa(at.Minute()) +
			`,"repeat":"daily","days":[],"enabled":true}`
	}
	cfg := `{"long":{"enabled":true,"mode":"fixed","durationMs":60000,"postponeMs":300000,` +
		`"slots":[` + slot("s1", t1) + `,` + slot("s2", t2) + `]}}`
	s, host := newTestSched(t, cfg)
	s.replan()
	s.Pause(PauseReasonManual)
	s.onTick()

	if len(host.skips) != 1 || host.skips[0] != "long" {
		t.Fatalf("降级成跳过的那一班应记一笔跳过，得到 %v", host.skips)
	}
	s.mu.Lock()
	pending := len(s.postponed)
	consumed := s.consumed[plan.OccurrenceKey("long", t1.UnixMilli())]
	s.mu.Unlock()
	if pending != 0 {
		t.Fatalf("越界的那班不该留待办，得到 %d 条", pending)
	}
	if !consumed {
		t.Fatal("这一班应记为已处理，否则同一分钟内每一拍都会重新“到点”")
	}
	if st := s.StateCopy(); st.NextAt != t2.UnixMilli() {
		t.Fatalf("下一班应是 %s，得到 %s", t2, time.UnixMilli(st.NextAt))
	}
	// 再走一拍：同一班不得重复记账
	s.onTick()
	if len(host.skips) != 1 {
		t.Fatalf("同一 occurrence 不该重复记账，得到 %v", host.skips)
	}
}

// 推迟之后点“跳过本次”：那条待办要一起撤掉，否则 dueAt 一到又弹一次。
func TestSkipCancelsPostponedFixed(t *testing.T) {
	cfg, _, t2 := fixedPairCfg(t, 120_000)
	s, host := newTestSched(t, cfg)
	s.replan()
	s.Postpone()
	s.mu.Lock()
	pending := len(s.postponed)
	s.mu.Unlock()
	if pending != 1 {
		t.Fatalf("前置：推迟后应有一条待办，得到 %d", pending)
	}

	s.Skip()
	s.mu.Lock()
	pending = len(s.postponed)
	s.mu.Unlock()
	if pending != 0 {
		t.Fatalf("跳过之后不该还留着待办，得到 %d 条", pending)
	}
	if got := s.StateCopy().NextAt; got != t2.UnixMilli() {
		t.Fatalf("跳过之后应排到下一班 %s，得到 %s", t2, time.UnixMilli(got))
	}
	if len(host.skips) != 1 || len(host.postpones) != 1 {
		t.Fatalf("统计应是一次推迟 + 一次跳过，得到 postpones=%v skips=%v", host.postpones, host.skips)
	}
	if s.flushPostponed() {
		t.Fatal("跳过之后不该还有待办可弹")
	}
	if len(host.breaks) != 0 {
		t.Fatalf("跳过之后不该弹窗，得到 %v", host.breaks)
	}
}

// 暂停中“推迟”的待办到点、人还在暂停：按卡片继续往后推（不是等恢复后才弹）。
func TestPausedPostponedDueKeepsPushing(t *testing.T) {
	t1 := time.Now().Truncate(time.Minute).Add(-time.Minute) // 上一分钟（不参与“当前分钟到期”判定）
	t2 := t1.Add(10 * time.Minute)
	slot := func(id string, at time.Time) string {
		return `{"id":"` + id + `","hour":` + itoa(at.Hour()) + `,"minute":` + itoa(at.Minute()) +
			`,"repeat":"daily","days":[],"enabled":true}`
	}
	cfg := `{"long":{"enabled":true,"mode":"fixed","durationMs":60000,"postponeMs":120000,` +
		`"slots":[` + slot("s1", t1) + `,` + slot("s2", t2) + `]}}`
	s, host := newTestSched(t, cfg)
	s.replan()
	s.Pause(PauseReasonManual)

	key := plan.OccurrenceKey("long", t1.UnixMilli())
	s.mu.Lock()
	s.postponed[key] = &postponedBreak{kind: "long", at: t1.UnixMilli(), dueAt: nowMs() - 1000}
	s.mu.Unlock()

	s.onTick()
	s.mu.Lock()
	p := s.postponed[key]
	s.mu.Unlock()
	if p == nil {
		t.Fatal("到点的待办应继续往后推，不该被丢掉")
	}
	if left := p.dueAt - nowMs(); left < 110_000 || left > 130_000 {
		t.Fatalf("应再推一个推迟时长（≈120000 ms），实际 %d ms", left)
	}
	if len(host.breaks) != 0 || len(host.skips) != 0 {
		t.Fatalf("暂停中推迟不该弹窗也不该记账，得到 breaks=%v skips=%v", host.breaks, host.skips)
	}
}

// 越过下一班：跳过本次，按下一班算（不排推迟待办、不弹窗、记一笔跳过）。
func TestPostponeFixedSkipsWhenCrossingNextSlot(t *testing.T) {
	cases := []struct {
		name string
		ms   int64
	}{
		{"晚于下一班", 6 * 60_000},
		{"正好等于下一班", 5 * 60_000},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg, t1, t2 := fixedPairCfg(t, c.ms)
			s, host := newTestSched(t, cfg)
			s.replan()
			s.Postpone()

			st := s.StateCopy()
			if st.NextAt != t2.UnixMilli() {
				t.Fatalf("应直接按下一班 %s 算，得到 %s", t2, time.UnixMilli(st.NextAt))
			}
			if st.NextName != plan.ShortBreakName {
				t.Fatalf("下一班仍是同一条提醒（短休息），得到 %q", st.NextName)
			}
			if st.Rest == nil || st.Rest.At != t2.UnixMilli() {
				t.Fatalf("侧栏应显示下一班，得到 %+v", st.Rest)
			}
			if len(host.skips) != 1 || host.skips[0] != "short" {
				t.Fatalf("这一班应记成跳过，得到 %v", host.skips)
			}
			if len(host.postpones) != 0 {
				t.Fatalf("越界的那班不该记推迟，得到 %v", host.postpones)
			}
			s.mu.Lock()
			pending := len(s.postponed)
			consumed := s.consumed[plan.OccurrenceKey("short", t1.UnixMilli())]
			s.mu.Unlock()
			if pending != 0 {
				t.Fatalf("越界的那班不该留推迟待办，得到 %d 条", pending)
			}
			if !consumed {
				t.Fatal("原定那一班应被标记为已处理，否则重排又会把它排回来")
			}
			if n := st.ShortsSinceLong; n != 1 {
				t.Fatalf("降级成跳过也要推进短休息计数（与手动跳过同口径），得到 %d", n)
			}
			// 原定那一刻已经过去 / 被跳过：到点前不该再弹
			s.onTick()
			if len(host.breaks) != 0 {
				t.Fatalf("跳过之后不该弹窗，得到 %v", host.breaks)
			}
		})
	}
}
