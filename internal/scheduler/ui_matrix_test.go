package scheduler

// “设置页侧栏 / 今日时间线”的**组合场景矩阵**（2026-11-13）。
//
// 已有单测各自只钉一条规则（暂停不冻钟、推迟不叠加、长休息优先……），而用户真遇到的
// 都是叠加态：暂停里撞车、推迟把两个固定时刻挤到一起、长休息接管短休息之后侧栏该写谁、
// 多次推迟把时刻推到越过下一班。这份文件专门跑这些叠加态，每个场景都断言**同一份快照**
// 上的四层可观测面：
//
//	① 侧栏：phase / breakEndsAt / rest / other / idleWait / paused+pauseReasons+deadline
//	② 时间线：StateForRenderer().Timeline（设置页「待触发」清单）
//	③ 记账：宿主收到的 postpone / skip / counted / 弹窗（统计报表的输入端）
//	④ 托盘：状态快照本身（trayTextsAt 是纯函数，文案矩阵在 cmd 包
//	   tray_scenario_test.go 里跑；这里保证喂给它的字段是对的）
//
// 时间推进：包内没有可注入的时钟，倒计时线统一用 forceKindDue 把目标线挪到「1 秒前」
// 再 onTick；固定时刻线则把 slot 直接配在「当前这一分钟」，靠真实时钟自然到点
// （改固定线的 At 会破坏 occurrence 身份，不能用 forceKindDue 推它）。

import (
	"testing"
	"time"
	"workerrest/internal/model"
	"workerrest/internal/plan"
)

// ---------- 脚手架 ----------

// rx 取一份渲染层快照（侧栏 + 时间线都从它来）
func rx(s *Scheduler) model.SchedulerState { return s.StateForRenderer() }

// forceKindDue 把 kind 那条挂起线连同 NextAt 挪到“1 秒前”，返回挪到的那一刻。
// 只对倒计时线成立（固定线的身份是时刻表里的绝对时刻）。
func forceKindDue(t *testing.T, s *Scheduler, kind string) int64 {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	at := nowMs() - 1000
	found := false
	for i := range s.state.Lines {
		if s.state.Lines[i].Type == kind {
			s.state.Lines[i].At = at
			found = true
		}
	}
	if !found {
		t.Fatalf("挂起线里没有 %s，无法构造到点（lines=%+v）", kind, s.state.Lines)
	}
	if s.state.NextType == kind {
		s.state.NextAt = at
	}
	if kind == "short" || kind == "long" {
		if s.state.Rest != nil {
			s.state.Rest = &model.PlanRef{Name: s.state.Rest.Name, At: at}
		}
	} else if s.state.Other != nil && s.state.Other.Name == s.state.NextName {
		s.state.Other = &model.PlanRef{Name: s.state.Other.Name, At: at}
	}
	return at
}

// slotLine 造一条“每天 HH:MM”的固定时刻（用绝对时间算，跨午夜也成立）
func slotLine(id string, at time.Time) string {
	return `{"id":"` + id + `","hour":` + itoa(at.Hour()) + `,"minute":` + itoa(at.Minute()) +
		`,"repeat":"daily","days":[],"enabled":true}`
}

// cdBody 倒计时线的字段体（不含外层键名），自定义提醒里也复用同一套字段
func cdBody(ivMs, durMs, preMs, postponeMs int, pauseAction string) string {
	return `"enabled":true,"mode":"countdown","intervalMs":` + itoa(ivMs) +
		`,"durationMs":` + itoa(durMs) + `,"notifyBeforeMs":` + itoa(preMs) +
		`,"postponeMs":` + itoa(postponeMs) + `,"pauseAction":"` + pauseAction + `","slots":[]`
}

// countdownCfg 构造一条内置倒计时线（短 / 长休息）
func countdownCfg(key string, ivMs, durMs, preMs, postponeMs int, pauseAction string) string {
	return `"` + key + `":{` + cdBody(ivMs, durMs, preMs, postponeMs, pauseAction) + `}`
}

// customBreak 一条自定义提醒（字段体与内置倒计时同形）
func customBreak(id, name string, ivMs, durMs, postponeMs int, pauseAction string) string {
	return `{"id":"` + id + `","name":"` + name + `","config":{` +
		cdBody(ivMs, durMs, 0, postponeMs, pauseAction) + `}}`
}

// fixedCfg 构造一条固定时刻线（slots 由调用方拼好）
func fixedCfg(key string, durMs, postponeMs int, pauseAction, slots string) string {
	return `"` + key + `":{"enabled":true,"mode":"fixed","durationMs":` + itoa(durMs) +
		`,"notifyBeforeMs":0,"postponeMs":` + itoa(postponeMs) +
		`,"pauseAction":"` + pauseAction + `","slots":[` + slots + `]}`
}

// timelineOf 时间线里某一身份的条目时刻（升序，前面已经由 plan 排好）
func timelineOf(st model.SchedulerState, kind string) []int64 {
	var out []int64
	for _, it := range st.Timeline {
		if it.Type == kind {
			out = append(out, it.At)
		}
	}
	return out
}

// assertTimelineSorted 时间线必须升序、且不含已经过去的时刻（“待触发”清单）
func assertTimelineSorted(t *testing.T, st model.SchedulerState, now int64) {
	t.Helper()
	var prev int64
	for i, it := range st.Timeline {
		if it.At < now {
			t.Fatalf("时间线第 %d 条（%s @ %s）已经过去了，不该进“待触发”",
				i, it.Name, time.UnixMilli(it.At).Format("15:04:05"))
		}
		if i > 0 && it.At < prev {
			t.Fatalf("时间线没按时刻升序：第 %d 条 %d < 上一条 %d", i, it.At, prev)
		}
		prev = it.At
	}
}

// assertSidebarRest 侧栏「下次休息」那一格必须指向 rest 代表的这条线
func assertSidebarRest(t *testing.T, st model.SchedulerState, name string) {
	t.Helper()
	if st.Rest == nil {
		t.Fatalf("侧栏「下次休息」是空的，期望 %q", name)
	}
	if st.Rest.Name != name {
		t.Fatalf("侧栏「下次休息」写的是 %q，期望 %q", st.Rest.Name, name)
	}
	if st.Rest.At <= 0 {
		t.Fatalf("侧栏「下次休息」没有时刻：%+v", st.Rest)
	}
}

func near(t *testing.T, got, want int64, tol int64, what string) {
	t.Helper()
	if got < want-tol || got > want+tol {
		t.Fatalf("%s：期望 ≈ %s（±%dms），实际 %s",
			what, time.UnixMilli(want).Format("15:04:05.000"), tol,
			time.UnixMilli(got).Format("15:04:05.000"))
	}
}

// ---------- 1. 基线：三格与时间线自洽 ----------

// 短休息 10 分钟 + 自定义提醒 30 分钟，侧栏两格各指一条，时间线按升序展开、不含过去。
func TestUIMatrixBaselineSidebarAndTimeline(t *testing.T) {
	js := `{` + countdownCfg("short", 600000, 20000, 10000, 120000, "postpone") + `,` +
		`"customBreaks":[` + customBreak("cb-water", "喝水提醒", 1800000, 20000, 120000, "postpone") + `]}`
	s, _ := newTestSched(t, js)
	s.replan()
	now := nowMs()
	st := rx(s)

	if st.Phase != "idle" {
		t.Fatalf("刚排好应是 idle，实际 %q", st.Phase)
	}
	if st.NextType != "short" {
		t.Fatalf("最近一条应是短休息，实际 %q", st.NextType)
	}
	assertSidebarRest(t, st, "短休息")
	if st.Other == nil || st.Other.Name != "喝水提醒" {
		t.Fatalf("侧栏「下次提醒」应指向喝水提醒，实际 %+v", st.Other)
	}
	if st.Other.At < st.Rest.At {
		t.Fatalf("喝水提醒（30 分钟）不该早于短休息（10 分钟）：rest=%d other=%d",
			st.Rest.At, st.Other.At)
	}
	if len(st.Timeline) == 0 {
		t.Fatal("今日时间线为空")
	}
	assertTimelineSorted(t, st, now)
	// 倒计时线要展开成“今天剩余的每一次”：10 分钟一条，今天还剩多少条至少 1 条
	if got := len(timelineOf(st, "short")); got < 1 {
		t.Fatalf("时间线里应至少有一条短休息，实际 %d", got)
	}
	// 侧栏与时间线必须指向同一刻（两条通路各算各的，容易漂）
	if !containsInt(timelineOf(st, "short"), st.Rest.At) {
		t.Fatalf("时间线里没有侧栏显示的那一刻 %d：%v", st.Rest.At, timelineOf(st, "short"))
	}
}

func containsInt(list []int64, v int64) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// ---------- 2. 撞车：长休息接管短休息 ----------

// 长休息到点时短休息正在屏幕上 → 侧栏整格换成「长休息」，被覆盖的那次记完成、
// 时间线不再列出它。
func TestUIMatrixLongPreemptsShareSidebar(t *testing.T) {
	js := `{` + countdownCfg("short", 600000, 20000, 0, 120000, "postpone") + `,` +
		countdownCfg("long", 3600000, 300000, 0, 300000, "postpone") + `}`
	s, host := newTestSched(t, js)
	s.replan()
	s.mu.Lock()
	s.state.Phase = "break"
	s.state.NextType = "short"
	s.state.BreakStartedAt = nowMs() - 5000
	s.state.BreakEndsAt = nowMs() + 15000
	s.state.Lines = append(s.state.Lines, plannedAt("long", "长休息", nowMs()-200, "countdown"))
	s.mu.Unlock()

	s.onTick()
	now := nowMs()
	st := rx(s)

	if st.Phase != "break" || st.NextType != "long" {
		t.Fatalf("抢占后应处于长休息中，实际 phase=%q next=%q", st.Phase, st.NextType)
	}
	if host.completed != 1 {
		t.Fatalf("短休息应按“完成”收尾一次，实际 completed=%d", host.completed)
	}
	if len(host.counted) != 0 {
		t.Fatalf("被抢占用的是完成口径，不该再记“覆盖计数”，实际 %v", host.counted)
	}
	// 侧栏「下次休息」这一刻没有下一次 —— 正在休息的倒计时看 breakEndsAt
	if st.BreakEndsAt <= now || st.BreakEndsAt > now+301_000 {
		t.Fatalf("侧栏「正在休息」的终点应是长休息时长（≈5 分钟），实际 %s",
			time.UnixMilli(st.BreakEndsAt).Format("15:04:05"))
	}
	if containsInt(timelineOf(st, "short"), st.BreakStartedAt) {
		t.Fatal("被长休息覆盖的那次短休息不该留在时间线里")
	}
	assertTimelineSorted(t, st, now)
}

// 长短同一刻到点：只弹长休息；短休息记一次“完成”，侧栏全程只有长休息。
func TestUIMatrixSameMomentOnlyLong(t *testing.T) {
	js := `{` + countdownCfg("short", 600000, 20000, 0, 120000, "postpone") + `,` +
		countdownCfg("long", 600000, 60000, 0, 120000, "postpone") + `}`
	s, host := newTestSched(t, js)
	s.replan()
	due := nowMs() - 50
	s.mu.Lock()
	s.state.Lines = []model.PlannedBreak{
		plannedAt("short", "短休息", due, "countdown"),
		plannedAt("long", "长休息", due, "countdown"),
	}
	s.state.Phase = "idle"
	s.state.NextType = "long"
	s.state.NextAt = due
	src := "countdown"
	s.state.NextSource = &src
	s.mu.Unlock()

	s.onTick()
	if len(host.breaks) != 1 || host.breaks[0] != "long" {
		t.Fatalf("同一刻只该弹长休息，实际 %v", host.breaks)
	}
	st := rx(s)
	if st.NextType != "long" || st.Phase != "break" {
		t.Fatalf("侧栏应显示长休息中，实际 phase=%q next=%q", st.Phase, st.NextType)
	}
	s.onTick()
	if len(host.counted) != 1 || host.counted[0] != "short" {
		t.Fatalf("短休息应记一次完成，实际 %v", host.counted)
	}
	if len(host.breaks) != 1 {
		t.Fatalf("短休息不该补弹，实际 %v", host.breaks)
	}
}

// ---------- 3. 同一提醒的多个固定时刻 ----------

// 短休息配了两个固定时刻（下一分钟 / 再两分钟）：侧栏与时间线同时列出两班；
// 第一班被消费（弹过 / 手动跳过）之后，两处都只指向第二班。
//
// 这里不真的等到点：固定线的身份是时刻表里的绝对时刻，把它的 At 改到过去会破坏
// occurrence 身份（消费键对不上，replan 会把同一班排回来）。到点弹窗本身另有测试覆盖。
func TestUIMatrixOneReminderTwoFixedSlots(t *testing.T) {
	now := time.Now()
	s1 := now.Add(time.Minute).Truncate(time.Minute)
	s2 := now.Add(3 * time.Minute).Truncate(time.Minute)
	slots := slotLine("s1", s1) + `,` + slotLine("s2", s2)
	js := `{` + fixedCfg("short", 20000, 120000, "postpone", slots) + `}`
	s, _ := newTestSched(t, js)
	s.replan()

	st := rx(s)
	if st.NextType != "short" || st.Phase != "idle" {
		t.Fatalf("固定时刻下应是 idle/短休息，实际 phase=%q next=%q", st.Phase, st.NextType)
	}
	assertSidebarRest(t, st, "短休息")
	if st.Rest.At != s1.UnixMilli() {
		t.Fatalf("侧栏应指向第一班 %s，实际 %s",
			s1.Format("15:04"), time.UnixMilli(st.Rest.At).Format("15:04"))
	}
	if !containsInt(timelineOf(st, "short"), s1.UnixMilli()) ||
		!containsInt(timelineOf(st, "short"), s2.UnixMilli()) {
		t.Fatalf("时间线应同时列出两班：%v", timelineOf(st, "short"))
	}
	// 第一班“用掉了”（弹窗结束 / 手动跳过都会写消费键）
	s.mu.Lock()
	s.consumed[plan.OccurrenceKey("short", s1.UnixMilli())] = true
	s.mu.Unlock()
	s.replan()

	st = rx(s)
	if st.NextAt != s2.UnixMilli() {
		t.Fatalf("第一班消费后应指向第二班 %s，实际 %s",
			s2.Format("15:04"), time.UnixMilli(st.NextAt).Format("15:04"))
	}
	if containsInt(timelineOf(st, "short"), s1.UnixMilli()) {
		t.Fatal("已消费的第一班不该留在“待触发”里")
	}
	if !containsInt(timelineOf(st, "short"), s2.UnixMilli()) {
		t.Fatalf("第二班应仍在时间线里：%v", timelineOf(st, "short"))
	}
}

// ---------- 4. 推迟 ----------

// 连续三次推迟：每次基准是「现在与原定中较晚者」+ 推迟时长，绝对不叠加成一次跳到底，
// 侧栏与时间线每次都要跟着走。
func TestUIMatrixRepeatedPostponeAccumulates(t *testing.T) {
	s, host := newTestSched(t, shortCountdown) // 推迟 2 分钟
	s.replan()
	forceKindDue(t, s, "short") // 到点了但还没弹（侧栏「推迟休息」正是这个时机）
	want := int64(0)
	for i := 1; i <= 3; i++ {
		s.Postpone()
		st := rx(s)
		now := nowMs()
		if st.NextAt <= now {
			t.Fatalf("第 %d 次推迟后仍是过去时刻 %s", i, time.UnixMilli(st.NextAt).Format("15:04:05"))
		}
		want = st.NextAt
		if d := want - now; d < int64(i*120_000)-1000 || d > int64(i*120_000)+1000 {
			t.Fatalf("第 %d 次推迟后应≈%d 分钟（每次在“当前到点”基础上再推 2 分钟），实际 %d ms",
				i, i*2, d)
		}
		assertSidebarRest(t, st, "短休息")
		if st.Rest.At != st.NextAt {
			t.Fatalf("第 %d 次推迟后侧栏没跟着挪：%d vs %d", i, st.Rest.At, st.NextAt)
		}
		if !containsInt(timelineOf(st, "short"), st.NextAt) {
			t.Fatalf("第 %d 次推迟后时间线里没有新时刻 %s：%v", i,
				time.UnixMilli(st.NextAt).Format("15:04:05"), timelineOf(st, "short"))
		}
	}
	if len(host.postpones) != 3 {
		t.Fatalf("应记 3 次推迟，实际 %v", host.postpones)
	}
	// 三次各推 2 分钟、后一次以“上一次的到点”为基准 → 累计约 6 分钟（证明没有叠加成一次）
	if d := want - nowMs(); d < 355_000 || d > 365_000 {
		t.Fatalf("三次推迟后应累计≈6 分钟，实际 %d ms", d)
	}
}

// 固定时刻推迟越过下一班：不排待办，按“跳过本次”记账，侧栏直接指向下一班。
func TestUIMatrixPostponeBeyondNextSlotSkips(t *testing.T) {
	now := time.Now()
	s1 := now.Add(time.Minute).Truncate(time.Minute)
	s2 := now.Add(3 * time.Minute).Truncate(time.Minute)
	slots := slotLine("s1", s1) + `,` + slotLine("s2", s2)
	js := `{` + fixedCfg("short", 20000, 300000, "postpone", slots) + `}` // 推迟 5 分钟 > 下一班
	s, host := newTestSched(t, js)
	s.replan()
	firstAt := rx(s).NextAt
	if firstAt != s1.UnixMilli() {
		t.Fatalf("前置：侧栏应先指第一班 %s，实际 %s",
			s1.Format("15:04"), time.UnixMilli(firstAt).Format("15:04"))
	}

	s.Postpone() // 第一班 + 5 分钟 ≥ 第二班 → 跳过本次

	if len(host.skips) != 1 || host.skips[0] != "short" {
		t.Fatalf("越过下一班应记一次跳过，实际 %v", host.skips)
	}
	if len(host.postpones) != 0 {
		t.Fatalf("跳过时不该同时记推迟，实际 %v", host.postpones)
	}
	st := rx(s)
	if st.NextAt <= firstAt {
		t.Fatalf("跳过本次后侧栏应指向下一班（>%s），实际 %s",
			time.UnixMilli(firstAt).Format("15:04:05"), time.UnixMilli(st.NextAt).Format("15:04:05"))
	}
	if containsInt(timelineOf(st, "short"), firstAt) {
		t.Fatal("跳过的那一班不该留在“待触发”里")
	}
	if st.FixedNext["short"] > 0 && st.FixedNext["short"] == firstAt+300_000 {
		t.Fatal("跳过时不该留下“改到 X 再提醒”的待办")
	}
}

// ---------- 5. 暂停 / 恢复 ----------

// 手动暂停：侧栏进入暂停态、原因可查，倒计时照真实墙钟走（不冻钟），恢复不平移。
func TestUIMatrixManualPauseClockKeepsRunning(t *testing.T) {
	s, _ := newTestSched(t, shortCountdown)
	s.replan()
	before := rx(s)

	s.Pause(PauseReasonManual)
	st := rx(s)
	if !st.Paused || len(st.PauseReasons) != 1 || st.PauseReasons[0] != PauseReasonManual {
		t.Fatalf("暂停态应写清原因：paused=%v reasons=%v", st.Paused, st.PauseReasons)
	}
	if st.PausedAt <= 0 {
		t.Fatal("暂停应记下 PausedAt（托盘“已暂停 N 分钟”用它）")
	}
	if st.NextAt != before.NextAt {
		t.Fatalf("暂停不该改目标时刻：%d → %d", before.NextAt, st.NextAt)
	}
	// 等一小会儿：剩余必须真的在减少（暂停只改“到点之后怎么办”），且目标时刻不漂移
	remBefore := s.RemainingMs()
	time.Sleep(220 * time.Millisecond)
	after := rx(s)
	if after.NextAt != before.NextAt {
		t.Fatalf("暂停期间目标时刻不该漂移：%d → %d", before.NextAt, after.NextAt)
	}
	if d := remBefore - s.RemainingMs(); d < 150 || d > 1200 {
		t.Fatalf("暂停期间剩余应随墙钟减少（睡 220ms），实际减少 %d ms", d)
	}

	s.Resume(PauseReasonManual)
	st = rx(s)
	if st.Paused || len(st.PauseReasons) != 0 {
		t.Fatalf("恢复后不该还挂着原因：%v", st.PauseReasons)
	}
	if st.NextAt != before.NextAt {
		t.Fatalf("恢复不该补偿平移：%d → %d", before.NextAt, st.NextAt)
	}
}

// 暂停中到点 + 「推迟提醒」：倒计时线改线到 now+推迟时长，恢复后到点照常弹。
func TestUIMatrixPausedDuePostpone(t *testing.T) {
	s, host := newTestSched(t, shortCountdown)
	s.replan()
	forceKindDue(t, s, "short")
	s.Pause(PauseReasonManual)

	s.onTick()
	st := rx(s)
	now := nowMs()
	if len(host.breaks) != 0 {
		t.Fatalf("“推迟提醒”不该弹窗，实际 %v", host.breaks)
	}
	if d := st.NextAt - now; d < 119_000 || d > 121_000 {
		t.Fatalf("应推到≈2 分钟后，实际 %d ms", d)
	}
	assertSidebarRest(t, st, "短休息")
	if st.Rest.At != st.NextAt {
		t.Fatalf("侧栏没跟着推迟：%d vs %d", st.Rest.At, st.NextAt)
	}
	// 暂停中的推迟是系统按卡片配置自动做的（用户没点过“推迟”），所以**不记推迟统计** ——
	// 与手动点“推迟休息”（Postpone 会记一笔）刻意区分开。
	if len(host.postpones) != 0 {
		t.Fatalf("暂停中自动推迟不该记“推迟”统计，实际 %v", host.postpones)
	}

	s.Resume(PauseReasonManual)
	forceKindDue(t, s, "short")
	s.onTick()
	if len(host.breaks) != 1 {
		t.Fatalf("恢复后到点应照常弹，实际 %v", host.breaks)
	}
}

// 暂停中到点 + 「跳过提醒」：记一次跳过、时间线抹掉这一班、侧栏推进到下一班。
func TestUIMatrixPausedDueSkip(t *testing.T) {
	js := `{` + countdownCfg("short", 600000, 20000, 0, 120000, "skip") + `}`
	s, host := newTestSched(t, js)
	s.replan()
	at := forceKindDue(t, s, "short")
	s.Pause(PauseReasonManual)

	s.onTick()
	s.onTick() // 拍两下：不得重复记账
	st := rx(s)
	if len(host.skips) != 1 || host.skips[0] != "short" {
		t.Fatalf("应只记一次跳过，实际 %v", host.skips)
	}
	if containsInt(timelineOf(st, "short"), at) {
		t.Fatal("跳过的那一班不该留在“待触发”里")
	}
	if st.NextAt <= at {
		t.Fatalf("跳过之后应推进到下一班（>%d），实际 %d", at, st.NextAt)
	}
	assertTimelineSorted(t, st, nowMs())
}

// 暂停中到点 + 「正常提醒」：照常弹窗；侧栏进入「正在休息」并按真实时钟倒计时。
func TestUIMatrixPausedDueRemind(t *testing.T) {
	js := `{` + countdownCfg("short", 600000, 20000, 0, 120000, "remind") + `}`
	s, host := newTestSched(t, js)
	s.replan()
	forceKindDue(t, s, "short")
	s.Pause(PauseReasonManual)

	s.onTick()
	st := rx(s)
	if len(host.breaks) != 1 || host.breaks[0] != "short" {
		t.Fatalf("“正常提醒”应照常弹，实际 %v", host.breaks)
	}
	if st.Phase != "break" || st.BreakEndsAt <= nowMs() {
		t.Fatalf("侧栏应进入「正在休息」，实际 phase=%q endsAt=%d", st.Phase, st.BreakEndsAt)
	}
	if !st.BreakWhilePaused {
		t.Fatal("暂停中发起的这次休息应标 BreakWhilePaused（按真实时钟走完、自动收窗）")
	}
}

// 多个自动原因叠加：各自独立，只清其中一条仍在暂停。
func TestUIMatrixAutoPauseReasonsStack(t *testing.T) {
	s, _ := newTestSched(t, shortCountdown)
	s.replan()
	s.Pause("fullscreen")
	s.Pause("dnd")

	st := rx(s)
	if len(st.PauseReasons) != 2 {
		t.Fatalf("两个自动原因应并存，实际 %v", st.PauseReasons)
	}
	s.Resume("fullscreen")
	st = rx(s)
	if !st.Paused || len(st.PauseReasons) != 1 || st.PauseReasons[0] != "dnd" {
		t.Fatalf("只清一条后应仍暂停且只剩 dnd，实际 paused=%v %v", st.Paused, st.PauseReasons)
	}
	s.Resume("dnd")
	if st = rx(s); st.Paused {
		t.Fatalf("原因全清后应恢复，实际 %v", st.PauseReasons)
	}
}

// 定时暂停：侧栏带 deadline（“到 HH:MM 自动结束”），倒计时照走。
func TestUIMatrixTimedPauseDeadline(t *testing.T) {
	s, _ := newTestSched(t, shortCountdown)
	s.replan()
	deadline := nowMs() + 30*60_000
	s.PauseUntil(deadline)

	st := rx(s)
	if !st.Paused {
		t.Fatal("定时暂停也是暂停")
	}
	if st.PauseDeadline != deadline {
		t.Fatalf("deadline 应为 %d，实际 %d", deadline, st.PauseDeadline)
	}
	if st.PausedAt <= 0 || st.PausedAt > nowMs() {
		t.Fatalf("PausedAt 应在过去，实际 %d", st.PausedAt)
	}
	if st.NextAt <= nowMs() {
		t.Fatal("暂停不冻钟，但目标时刻本身不该是过去")
	}
}

// 暂停中「立即休息」：侧栏进入休息态且标 BreakWhilePaused，暂停原因还在（不是取消暂停）。
func TestUIMatrixBreakNowWhilePaused(t *testing.T) {
	s, host := newTestSched(t, shortCountdown)
	s.replan()
	s.Pause(PauseReasonManual)
	s.BreakNow()

	st := rx(s)
	if st.Phase != "break" || !st.BreakWhilePaused {
		t.Fatalf("应立即进入休息且标 BreakWhilePaused：phase=%q bp=%v", st.Phase, st.BreakWhilePaused)
	}
	if !st.Paused || len(st.PauseReasons) != 1 {
		t.Fatalf("“立即休息”不该顺手解除暂停，实际 paused=%v %v", st.Paused, st.PauseReasons)
	}
	if len(host.breaks) != 1 {
		t.Fatalf("应弹一次休息窗，实际 %v", host.breaks)
	}
}

// ---------- 6. 空闲门 ----------

// 到点了但键鼠还在动：停在“等待空闲”，这一跳不进“待触发”清单（它已经到点了）。
func TestUIMatrixIdleWaitNotInTimeline(t *testing.T) {
	s, host := newTestSched(t, shortGated)
	s.replan()
	at := forceKindDue(t, s, "short")
	host.idleMs = 5_000 // 门槛 30 秒

	s.onTick()
	st := rx(s)
	if !st.IdleWait {
		t.Fatal("状态应标 idleWait")
	}
	if st.Rest == nil || !st.Rest.IdleWait {
		t.Fatalf("侧栏内置休息那一格应标“等待空闲”，实际 %+v", st.Rest)
	}
	if st.NextAt != at {
		t.Fatalf("等待中目标时刻必须留在过去，实际 %d vs %d", st.NextAt, at)
	}
	if containsInt(timelineOf(st, "short"), at) {
		t.Fatal("卡在等待空闲的那一跳已经到点了，不该列进“待触发”")
	}
}

// ---------- 7. 空配置 / 外卖覆盖 ----------

// 三类都关：侧栏三格全空，时间线为空，托盘写“未安排休息”。
func TestUIMatrixAllDisabled(t *testing.T) {
	js := `{"short":{"enabled":false},"long":{"enabled":false},` +
		`"takeout":{"enabled":false},"customBreaks":[]}`
	s, _ := newTestSched(t, js)
	s.replan()
	st := rx(s)
	if st.NextAt != 0 || st.NextType != "" {
		t.Fatalf("全关时应无下一跳，实际 next=%q at=%d", st.NextType, st.NextAt)
	}
	if st.Rest != nil || st.Other != nil {
		t.Fatalf("全关时侧栏两格都该为空，实际 rest=%+v other=%+v", st.Rest, st.Other)
	}
	if len(st.Timeline) != 0 {
		t.Fatalf("全关时时间线应为空，实际 %v", st.Timeline)
	}
	if st.NextSource != nil {
		t.Fatalf("全关时 NextSource 应为 null，实际 %q", *st.NextSource)
	}
}

// 外卖推迟后：fixedNext 与侧栏那一格都要指向推迟后的时刻（卡片摘要 / 侧栏同一口径）。
func TestUIMatrixTakeoutPostponedShowsInSidebar(t *testing.T) {
	now := time.Now()
	tko := `{"takeout":{"enabled":true,"leadSec":0,"postponeMin":2,"pauseAction":"postpone",` +
		`"slots":[` + slotLine("t1", now.Add(3*time.Minute)) + `]}}`
	s, host := newTestSched(t, tko)
	host.pp = &model.TakeoutPostponed{DueAt: nowMs() + 15*60_000, Count: 1}
	s.replan()

	st := rx(s)
	due := host.pp.DueAt
	if got := st.FixedNext["takeout"]; got != due {
		t.Fatalf("fixedNext.takeout 应指向推迟后的时刻 %d，实际 %d", due, got)
	}
	if st.Other == nil || st.Other.Name != "外卖提醒" {
		t.Fatalf("侧栏应显示外卖那一格，实际 %+v", st.Other)
	}
	if st.Other.At != due {
		t.Fatalf("侧栏外卖格应写推迟后的时刻 %d，实际 %d", due, st.Other.At)
	}
	// 时间线要把原定时刻换掉，只留推迟后的那一班
	if containsInt(timelineOf(st, "takeout"), now.Add(3*time.Minute).UnixMilli()) {
		t.Fatal("时间线里不该同时留着外卖原定时刻")
	}
	if !containsInt(timelineOf(st, "takeout"), due) {
		t.Fatalf("时间线里应有一条推迟后的外卖 %s", time.UnixMilli(due).Format("15:04"))
	}
}

// 休息期间到了另一条提醒：它并入当前窗口，但侧栏「下次提醒」不能因此丢掉还挂在后面的自定义提醒。
func TestUIMatrixMergedExtraKeepsOtherSlot(t *testing.T) {
	js := `{` + countdownCfg("short", 300000, 60000, 0, 120000, "postpone") + `,` +
		`"customBreaks":[` + customBreak("cb-water", "喝水提醒", 600000, 20000, 120000, "postpone") + `]}`
	s, host := newTestSched(t, js)
	s.replan()
	// 起短休息
	forceKindDue(t, s, "short")
	s.onTick()
	if s.CurrentPhase() != "break" {
		t.Fatalf("应先进入短休息，实际 %q", s.CurrentPhase())
	}
	// 休息期间喝水提醒到点 → 并入
	forceKindDue(t, s, "cb-water")
	s.onTick()

	if len(host.extras) != 1 || host.extras[0] != "cb-water" {
		t.Fatalf("喝水提醒应并入当前休息窗，实际 %v", host.extras)
	}
	if len(host.breaks) != 1 {
		t.Fatalf("不该另开窗口，实际 %v", host.breaks)
	}
	st := rx(s)
	if st.Phase != "break" || st.NextType != "short" {
		t.Fatalf("并入不该打断这次休息：phase=%q next=%q", st.Phase, st.NextType)
	}
	// 被并入的那一班向前推，时间线里不该再出现“刚刚那一跳”
	if containsInt(timelineOf(st, "cb-water"), nowMs()-1000) {
		t.Fatal("已并入的那一班不该留在“待触发”里")
	}
}
