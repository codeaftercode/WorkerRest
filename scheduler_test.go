package main

import (
	"encoding/json"
	"testing"
	"time"
)

// 调度器与计划层的纯函数回归。
//
// 分工（与外部对拍互补，别搞混）：
//   - 这里拦「规则写错」：消费记账、暂停冻结平移、休息总时长封顶、跨天截断……
//     毫秒级跑完，改完代码先跑它。
//   - scripts/verify_plan_parity.py 拦「与 Electron 不一致」：拿 Electron 自己的
//     breakPlan.ts 当黄金参考逐字段对拍 27 个场景。
// 两者都过才算这一层真的对。

// ---------- 脚手架 ----------

// testSettings 用一段 JSON 造一份配置（只填本次用到的字段，其余按 breakConfigOf 的默认）
func testSettings(t *testing.T, js string) *Settings {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(js), &m); err != nil {
		t.Fatalf("测试配置解析失败: %v", err)
	}
	return &Settings{raw: m}
}

// fakeHost 调度器宿主：只实现调度器真正会调的那几个口子，不碰窗口 / 托盘 / Wails，
// 这样状态机可以脱离 application.App 跑单测。
type fakeHost struct {
	cfg       *Settings
	states    int
	pre       []string
	pauses    int
	breaks    []string
	closed    int
	closeWin  string
	postpones []string
	skips     []string
}

func (h *fakeHost) cfgSnapshot() *Settings                      { return h.cfg }
func (h *fakeHost) onSchedulerState()                           { h.states++ }
func (h *fakeHost) onPreNotify(kind string, _ int64)            { h.pre = append(h.pre, kind) }
func (h *fakeHost) onPaused()                                   { h.pauses++ }
func (h *fakeHost) closeBreakWindows()                          { h.closed++ }
func (h *fakeHost) takeoutPostponedSnapshot() *TakeoutPostponed { return nil }
func (h *fakeHost) takeoutFiredSnapshot() map[string]bool       { return nil }
func (h *fakeHost) onBreakPostponed(kind string, _ int64)       { h.postpones = append(h.postpones, kind) }
func (h *fakeHost) onBreakSkipped(kind string, _ int64)         { h.skips = append(h.skips, kind) }
func (h *fakeHost) showBreakSession(kind string, _ bool, _ string) string {
	h.breaks = append(h.breaks, kind)
	return "id-" + kind
}

func newTestSched(t *testing.T, cfgJSON string) (*Scheduler, *fakeHost) {
	t.Helper()
	host := &fakeHost{cfg: testSettings(t, cfgJSON)}
	return newSchedulerWithHost(host), host
}

// shortCountdown 常用的短休息倒计时配置（10 分钟一次 20 秒）
const shortCountdown = `{"short":{"enabled":true,"mode":"countdown","intervalMs":600000,"durationMs":20000,"notifyBeforeMs":10000,"postponeMs":120000,"slots":[]}}`

// ---------- 状态机 ----------

func TestSchedulerReplanCountdown(t *testing.T) {
	s, _ := newTestSched(t, shortCountdown)
	before := nowMs()
	s.replan()
	got := s.stateCopy()
	if got.NextType != "short" || got.NextName != "短休息" {
		t.Fatalf("下一次应是短休息，得到 %q/%q", got.NextType, got.NextName)
	}
	if got.NextSource == nil || *got.NextSource != "countdown" {
		t.Fatalf("来源应是 countdown，得到 %v", got.NextSource)
	}
	want := before + 600_000
	if diff := got.NextAt - want; diff < -2000 || diff > 2000 {
		t.Fatalf("下一次时刻应≈现在+10 分钟，want=%d got=%d（差 %d ms）", want, got.NextAt, diff)
	}
	if got.Rest == nil || got.Rest.At != got.NextAt {
		t.Fatalf("分组视图 rest 应对齐全局 next，得到 %+v", got.Rest)
	}
	if len(got.Lines) != 1 || got.Lines[0].Source != "countdown" {
		t.Fatalf("挂起线应只有 1 条倒计时线，得到 %+v", got.Lines)
	}
}

func TestSchedulerReplanKeepsCountdownProgress(t *testing.T) {
	// 设置页任意保存都会触发 refreshPlan → replan：未到期的倒计时不得被
	// 重置回满间隔（用户已流逝的进度要保留），跳过/到期后才重新起算。
	s, _ := newTestSched(t, shortCountdown)
	s.replan()
	first := s.stateCopy().NextAt

	// 模拟时间流逝：用户已等了 4 分钟（像暂停恢复那样整体平移）
	s.mu.Lock()
	s.state.NextAt -= 4 * 60_000
	for i := range s.state.Lines {
		s.state.Lines[i].At = s.state.NextAt
	}
	s.mu.Unlock()

	s.replan()
	got := s.stateCopy()
	want := first - 4*60_000
	if got.NextAt != want {
		t.Fatalf("replan 不该重置未到期的倒计时（应保留已流逝的 4 分钟），want=%d got=%d", want, got.NextAt)
	}
	if len(got.Lines) != 1 || got.Lines[0].At != got.NextAt {
		t.Fatalf("挂起线应同步，得到 %+v", got.Lines)
	}

	// 跳过：该 occurrence 已消费，倒计时必须从头计时（不能沿用旧锚点）
	s.skip()
	got2 := s.stateCopy()
	want2 := nowMs() + 600_000
	if diff := got2.NextAt - want2; diff < -2000 || diff > 2000 {
		t.Fatalf("跳过后倒计时应重新起算，want≈%d got=%d（差 %d）", want2, got2.NextAt, diff)
	}
}

func TestSchedulerReplanReanchorsOnIntervalChange(t *testing.T) {
	// 改间隔要立刻按新间隔重算：未到期锚点只在间隔未变时沿用
	s, host := newTestSched(t, shortCountdown)
	s.replan()
	// 等了 4 分钟后把间隔 10 分钟改成 45 分钟（模拟设置页保存 → replan）
	s.mu.Lock()
	s.state.NextAt -= 4 * 60_000
	for i := range s.state.Lines {
		s.state.Lines[i].At = s.state.NextAt
	}
	host.cfg = testSettings(t, `{"short":{"enabled":true,"mode":"countdown","intervalMs":2700000,"durationMs":20000,"notifyBeforeMs":10000,"postponeMs":120000,"slots":[]}}`)
	s.mu.Unlock()
	s.replan()
	got := s.stateCopy()
	want := nowMs() + 2_700_000
	if diff := got.NextAt - want; diff < -2000 || diff > 2000 {
		t.Fatalf("改间隔后应立刻按新间隔重算，want≈%d got=%d（差 %d）", want, got.NextAt, diff)
	}

	// 对照组：间隔没变的无关保存不得动倒计时
	s2, _ := newTestSched(t, shortCountdown)
	s2.replan()
	keep := s2.stateCopy().NextAt
	s2.mu.Lock()
	s2.state.NextAt -= 4 * 60_000
	for i := range s2.state.Lines {
		s2.state.Lines[i].At = s2.state.NextAt
	}
	s2.mu.Unlock()
	s2.replan()
	if got2 := s2.stateCopy().NextAt; got2 != keep-4*60_000 {
		t.Fatalf("间隔未变时 replan 不该重置倒计时，want=%d got=%d", keep-4*60_000, got2)
	}
}

func TestSchedulerAllDisabledNoPlan(t *testing.T) {
	s, _ := newTestSched(t, `{"short":{"enabled":false},"long":{"enabled":false}}`)
	s.replan()
	got := s.stateCopy()
	if got.NextAt != 0 || got.NextSource != nil {
		t.Fatalf("全部关闭时不该有安排，得到 nextAt=%d source=%v", got.NextAt, got.NextSource)
	}
	if got.Rest != nil || got.Other != nil {
		t.Fatalf("分组视图都该是 nil，得到 rest=%+v other=%+v", got.Rest, got.Other)
	}
	if len(got.Lines) != 0 {
		t.Fatalf("挂起线该为空，得到 %+v", got.Lines)
	}
}

func TestSchedulerPostponeShiftsPlan(t *testing.T) {
	s, _ := newTestSched(t, shortCountdown)
	s.replan()
	origAt := s.stateCopy().NextAt

	s.postpone()
	got := s.stateCopy()
	want := nowMs() + 120_000
	if diff := got.NextAt - want; diff < -2000 || diff > 2000 {
		t.Fatalf("推迟后应是现在+2 分钟，want=%d got=%d", want, got.NextAt)
	}
	if got.NextAt == origAt {
		t.Fatal("推迟后时刻不该还是原值")
	}
	// 被推迟的那条线要跟着平移（今日时间线读的是 lines）
	if len(got.Lines) != 1 || got.Lines[0].At != got.NextAt {
		t.Fatalf("挂起线应同步平移，得到 %+v", got.Lines)
	}
	// 分组倒计时也要跟着走
	if got.Rest == nil || got.Rest.At != got.NextAt {
		t.Fatalf("分组倒计时应同步，得到 %+v", got.Rest)
	}
	if got.Phase != "idle" {
		t.Fatalf("推迟后应回到 idle，得到 %q", got.Phase)
	}
}

func TestSchedulerSkipConsumesOccurrence(t *testing.T) {
	// 固定时刻：今天稍后的一条（用相对时刻构造，避开具体日期）
	target := nowMs() + 3_600_000
	at := time.UnixMilli(target)
	cfg := `{"short":{"enabled":true,"mode":"fixed","durationMs":20000,"slots":[
		{"id":"s-a","hour":` + itoa(at.Hour()) + `,"minute":` + itoa(at.Minute()) + `,"repeat":"daily","days":[],"enabled":true}
	]},"long":{"enabled":false}}`
	// 固定时刻是按「时:分」对齐的，规划出来的时刻 = 那一刻的整分
	aligned := time.Date(at.Year(), at.Month(), at.Day(), at.Hour(), at.Minute(), 0, 0, time.Local).UnixMilli()
	s, _ := newTestSched(t, cfg)
	s.replan()
	planned := s.stateCopy().NextAt
	if planned != aligned {
		t.Fatalf("规划时刻应为 %d，得到 %d", aligned, planned)
	}

	s.skip()
	if !s.consumed[occurrenceKey("short", aligned)] {
		t.Fatal("跳过必须消费掉本次 occurrence（否则固定线会原样排回来）")
	}
	after := s.stateCopy().NextAt
	if after == aligned {
		t.Fatal("跳过之后不该还排在同一个时刻")
	}
	if sameLocalDay(after, aligned) {
		t.Fatalf("今天这条已跳过的线不该再出现，得到 %d", after)
	}
}

func TestSchedulerBreakLifecycleAndCounter(t *testing.T) {
	s, host := newTestSched(t, `{"short":{"enabled":true,"mode":"countdown","intervalMs":600000,"durationMs":20000},
		"long":{"enabled":true,"mode":"countdown","intervalMs":1800000,"durationMs":60000},"longEveryNShort":2}`)
	s.replan()

	id := s.breakNowOf("")
	if id == "" || len(host.breaks) != 1 || host.breaks[0] != "short" {
		t.Fatalf("应立即弹一次短休息，id=%q breaks=%v", id, host.breaks)
	}
	st := s.stateCopy()
	if st.Phase != "break" {
		t.Fatalf("phase 应为 break，得到 %q", st.Phase)
	}
	if st.BreakEndsAt-st.BreakStartedAt != 20_000 {
		t.Fatalf("本次休息时长应为 20s，得到 %d", st.BreakEndsAt-st.BreakStartedAt)
	}
	if st.ShortsSinceLong != 0 {
		t.Fatal("休息中不该提前计数")
	}

	s.endBreak()
	st = s.stateCopy()
	if st.Phase != "idle" {
		t.Fatalf("结束后应回到 idle，得到 %q", st.Phase)
	}
	if host.closed != 1 {
		t.Fatalf("应关一次休息窗口，得到 %d", host.closed)
	}
	if st.ShortsSinceLong != 1 {
		t.Fatalf("短休息结束应计数 +1，得到 %d", st.ShortsSinceLong)
	}
	// 双倒计时 + ssL=1 < longEveryNShort=2 → 仍排短休息
	if st.NextType != "short" {
		t.Fatalf("还没轮到长休息，得到 %q", st.NextType)
	}
}

func TestSchedulerLongBreakResetsCounter(t *testing.T) {
	s, _ := newTestSched(t, `{"short":{"enabled":true,"mode":"countdown","intervalMs":600000},
		"long":{"enabled":true,"mode":"countdown","intervalMs":1800000},"longEveryNShort":1}`)
	s.replan()
	// ssL=0 >= longEveryNShort? 0>=1 不成立 → 短休息；先手动把计数顶到 1
	s.mu.Lock()
	s.state.ShortsSinceLong = 1
	s.mu.Unlock()
	s.replan()
	if got := s.stateCopy().NextType; got != "long" {
		t.Fatalf("满 N 次应轮到长休息，得到 %q", got)
	}

	s.breakNowOf("")
	s.endBreak()
	if got := s.stateCopy().ShortsSinceLong; got != 0 {
		t.Fatalf("长休息结束应把计数清零，得到 %d", got)
	}
}

func TestSchedulerPauseReasonsGate(t *testing.T) {
	s, host := newTestSched(t, shortCountdown)
	s.pause("idle")
	if !s.stateCopy().Paused {
		t.Fatal("pause 后应为暂停态")
	}
	if host.pauses != 1 {
		t.Fatalf("暂停应通知宿主关提前提示窗，得到 %d", host.pauses)
	}
	s.pause("idle") // 重复原因不该重复入列
	if n := len(s.stateCopy().PauseReasons); n != 1 {
		t.Fatalf("重复原因应被忽略，得到 %d 条", n)
	}
	s.pause("manual")
	if n := len(s.stateCopy().PauseReasons); n != 2 {
		t.Fatalf("应有 2 条暂停原因，得到 %d", n)
	}
	// 只清一条：仍然暂停
	s.resume("idle")
	st := s.stateCopy()
	if !st.Paused || len(st.PauseReasons) != 1 {
		t.Fatalf("还剩一条原因时应保持暂停，得到 paused=%v reasons=%v", st.Paused, st.PauseReasons)
	}
	// 最后一条清掉：恢复
	s.resume("manual")
	if s.stateCopy().Paused {
		t.Fatal("全部原因清掉后应恢复")
	}
}

func TestSchedulerResumeShiftsCountdown(t *testing.T) {
	s, _ := newTestSched(t, shortCountdown)
	s.replan()
	origAt := s.stateCopy().NextAt

	s.pause("manual")
	// 假装已经暂停了 50 秒（不真的等）
	s.mu.Lock()
	s.state.PausedAt = nowMs() - 50_000
	s.mu.Unlock()
	s.resume("manual")

	got := s.stateCopy().NextAt
	want := origAt + 50_000
	if diff := got - want; diff < -1500 || diff > 1500 {
		t.Fatalf("倒计时线应整体平移 50s，want=%d got=%d（差 %d）", want, got, diff)
	}
	if !sameLocalDay(got, origAt) {
		t.Fatal("短暂暂停不该把倒计时推到别的日子")
	}
}

func TestSchedulerResumeBreakCappedToDuration(t *testing.T) {
	// 休息期间被长时间暂停（如开着勿扰一下午）：恢复时总时长封顶为配置时长，
	// 超过就直接收尾，不能拖成遥遥无期的「休息中」
	s, host := newTestSched(t, shortCountdown)
	now := nowMs()
	s.mu.Lock()
	s.state.Phase = "break"
	s.state.NextType = "short"
	s.state.BreakStartedAt = now - 100_000
	s.state.BreakEndsAt = now + 10_000
	s.state.Paused = true
	s.state.PausedAt = now - 60_000
	s.state.PauseReasons = []string{"manual"}
	s.mu.Unlock()

	s.resume("manual")
	st := s.stateCopy()
	if st.Phase != "idle" {
		t.Fatalf("超出总时长上限应直接收尾，得到 phase=%q", st.Phase)
	}
	if host.closed != 1 {
		t.Fatalf("收尾应关一次窗口，得到 %d", host.closed)
	}
	if st.ShortsSinceLong != 1 {
		t.Fatalf("收尾也要计数，得到 %d", st.ShortsSinceLong)
	}
}

func TestSchedulerReanchorCountdownOnlyBuiltin(t *testing.T) {
	s, _ := newTestSched(t, shortCountdown)
	s.replan()
	// 把挂起时刻改成「5 秒后」，模拟离开前剩下的旧账
	s.mu.Lock()
	s.state.NextAt = nowMs() + 5_000
	s.state.Lines[0].At = s.state.NextAt
	s.mu.Unlock()

	s.reanchorCountdown()
	got := s.stateCopy()
	want := nowMs() + 600_000
	if diff := got.NextAt - want; diff < -2000 || diff > 2000 {
		t.Fatalf("离开结束后内置倒计时应重头计时，want≈%d got=%d", want, got.NextAt)
	}
	if got.Lines[0].At != got.NextAt {
		t.Fatal("挂起线要同步")
	}

	// 自定义提醒不该被重锚（用户按自己节奏配的绝对安排）
	s2, _ := newTestSched(t, `{"short":{"enabled":false},"long":{"enabled":false},
		"customBreaks":[{"id":"cb-1","name":"喝水提醒","config":{"enabled":true,"mode":"countdown","intervalMs":600000,"durationMs":15000}}]}`)
	s2.replan()
	// 故意改成一个「哨兵」值：重锚若生效就会把它换成 now+intervalMs
	sentinel := nowMs() + 5_000
	s2.mu.Lock()
	s2.state.NextAt = sentinel
	s2.mu.Unlock()
	s2.reanchorCountdown()
	if got := s2.stateCopy().NextAt; got != sentinel {
		t.Fatalf("自定义提醒的倒计时不该被一次离开推走（%d → %d）", sentinel, got)
	}
}

func TestSchedulerBreakNowIgnoredWhileBreaking(t *testing.T) {
	s, host := newTestSched(t, shortCountdown)
	s.replan()
	s.breakNowOf("")
	s.breakNowOf("")
	if len(host.breaks) != 1 {
		t.Fatalf("休息进行中不该再弹一次，得到 %d 次", len(host.breaks))
	}
}

func TestSchedulerStartBreakSkipsDeletedCustom(t *testing.T) {
	// 计划里的自定义提醒被删掉了：不该弹窗，直接重排
	s, host := newTestSched(t, `{"short":{"enabled":true,"mode":"countdown","intervalMs":600000},"long":{"enabled":false}}`)
	s.replan()
	s.mu.Lock()
	s.state.NextType = "cb-gone"
	s.mu.Unlock()
	s.breakNowOf("") // nextType 已指向不存在的身份
	if len(host.breaks) != 0 {
		t.Fatalf("已删除的休息不该弹窗，得到 %v", host.breaks)
	}
	if s.stateCopy().Phase != "idle" {
		t.Fatal("应回到等待态并重排")
	}
}

func TestSchedulerStateForRendererIncludesTimeline(t *testing.T) {
	s, _ := newTestSched(t, shortCountdown)
	s.replan()
	st := s.stateForRenderer()
	if len(st.Timeline) == 0 {
		t.Fatal("状态快照必须带上今日时间线（设置页时间规划页要渲染它）")
	}
	if st.FixedNext == nil || st.PauseReasons == nil || st.Lines == nil {
		t.Fatal("快照里的 map / 切片都不能是 nil（渲染层直接读长度）")
	}
	for _, it := range st.Timeline {
		if it.Source == "countdown" && it.Paused == nil {
			t.Fatal("倒计时线的项必须带 paused 键（渲染层据它显示「已暂停」标记）")
		}
	}
}

// ---------- 纯函数 ----------

func TestNextSlotAtIsStrictlyAfter(t *testing.T) {
	slot := TimeSlot{ID: "s", Hour: 12, Minute: 0, Repeat: "daily", Enabled: true}
	noon := time.Date(2026, 10, 8, 12, 0, 0, 0, time.Local).UnixMilli()
	// 恰好等于该时刻：不算（严格晚于）
	hit, ok := nextSlotAt([]TimeSlot{slot}, noon)
	if !ok {
		t.Fatal("应该找得到下一条")
	}
	if hit.At == noon {
		t.Fatal("等于 from 的时刻不该被选中")
	}
	if !sameLocalDay(hit.At, noon+86_400_000) {
		t.Fatalf("应落到第二天，得到 %d", hit.At)
	}
	// 早一秒：就是今天这条
	hit, _ = nextSlotAt([]TimeSlot{slot}, noon-1000)
	if hit.At != noon {
		t.Fatalf("早一秒应命中今天 12:00，得到 %d", hit.At)
	}
}

func TestFindDueTimeSlotsDedupAndPrune(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 30, 0, time.Local).UnixMilli()
	slots := []TimeSlot{
		{ID: "a", Hour: 12, Minute: 0, Repeat: "daily", Enabled: true},
		{ID: "b", Hour: 12, Minute: 0, Repeat: "daily", Enabled: true},
		{ID: "c", Hour: 13, Minute: 0, Repeat: "daily", Enabled: true},
		{ID: "d", Hour: 12, Minute: 0, Repeat: "daily", Enabled: false},
	}
	fired := map[string]bool{}
	got := findDueTimeSlots(slots, now, fired, 0)
	if len(got) != 2 {
		t.Fatalf("应命中 a/b 两条（c 未到点、d 已停用），得到 %d", len(got))
	}
	// 同一分钟再跑一次：不重复返回
	if again := findDueTimeSlots(slots, now+5_000, fired, 0); len(again) != 0 {
		t.Fatalf("同一分钟不该重复触发，得到 %d", len(again))
	}
	if len(fired) != 2 {
		t.Fatalf("已触发集合应有 2 个键，得到 %d", len(fired))
	}
	// 跨天：旧键该被清掉
	nextDay := now + 86_400_000
	findDueTimeSlots(nil, nextDay, fired, 0)
	if len(fired) != 0 {
		t.Fatalf("跨天后旧键应清空，得到 %v", fired)
	}
}

func TestSlotAlertAtCrossesDayWithLead(t *testing.T) {
	slot := TimeSlot{ID: "s", Hour: 0, Minute: 10, Repeat: "daily", Enabled: true}
	day := time.Date(2026, 10, 8, 12, 0, 0, 0, time.Local).UnixMilli()
	got := slotAlertAt(slot, 30*60_000, day)
	want := time.Date(2026, 10, 7, 23, 40, 0, 0, time.Local).UnixMilli()
	if got != want {
		t.Fatalf("提前 30 分钟应落到前一天 23:40，want=%d got=%d", want, got)
	}
}

func TestExpandBuiltinCountdownStopsAtMidnight(t *testing.T) {
	s := testSettings(t, `{"short":{"enabled":true,"mode":"countdown","intervalMs":600000,"durationMs":20000},
		"long":{"enabled":false},"longEveryNShort":2}`)
	now := time.Date(2026, 10, 8, 23, 30, 0, 0, time.Local).UnixMilli()
	first := PlannedBreak{Type: "short", Name: "短休息", At: now + 600_000, Source: "countdown"}
	chain := expandBuiltinCountdown(s, now, 0, nil, &first)
	for _, p := range chain {
		if !sameLocalDay(p.At, now) {
			t.Fatalf("倒计时链不该跨天，得到 %d", p.At)
		}
	}
	// 23:30 起算：第一跳 23:40，第二跳 = 23:40 + 20 秒时长 + 10 分钟间隔 = 23:50:20，
	// 第三跳就跨天了 —— 所以今天正好两跳。
	if len(chain) != 2 {
		t.Fatalf("23:30 起算今天应有两跳，得到 %d 跳", len(chain))
	}
	wantSecond := time.Date(2026, 10, 8, 23, 50, 20, 0, time.Local).UnixMilli()
	if chain[1].At != wantSecond {
		t.Fatalf("第二跳应为 23:50:20，得到 %d", chain[1].At)
	}
}

func TestFixedNextMapSkipsConsumedToNextDay(t *testing.T) {
	s := testSettings(t, `{"short":{"enabled":true,"mode":"fixed","slots":[
		{"id":"s-a","hour":12,"minute":0,"repeat":"daily","days":[],"enabled":true}]},
		"long":{"enabled":false},"takeout":{"enabled":false}}`)
	now := time.Date(2026, 10, 8, 11, 0, 0, 0, time.Local).UnixMilli()
	noon := time.Date(2026, 10, 8, 12, 0, 0, 0, time.Local).UnixMilli()

	m := fixedNextMap(s, now, nil)
	if m["short"] != noon {
		t.Fatalf("未消费时应给今天 12:00，得到 %v", m)
	}
	m2 := fixedNextMap(s, now, map[string]bool{occurrenceKey("short", noon): true})
	if m2["short"] == noon {
		t.Fatal("已消费的今天这条不该再返回")
	}
	if !sameLocalDay(m2["short"], noon+86_400_000) {
		t.Fatalf("应顺延到明天 12:00，得到 %d", m2["short"])
	}
}

func TestTodayTimelineExcludesOtherDays(t *testing.T) {
	s := testSettings(t, `{"short":{"enabled":true,"mode":"fixed","slots":[
		{"id":"s-a","hour":23,"minute":0,"repeat":"daily","days":[],"enabled":true}]},
		"long":{"enabled":false},"takeout":{"enabled":false}}`)
	now := time.Date(2026, 10, 8, 22, 0, 0, 0, time.Local).UnixMilli()
	items := todayTimeline(s, now, TimelineOptions{})
	if len(items) != 1 {
		t.Fatalf("今天只剩 23:00 一条，得到 %d 条", len(items))
	}
	if !sameLocalDay(items[0].At, now) {
		t.Fatal("时间线只报当天")
	}
}

func TestOccurrenceKeyRoundTrip(t *testing.T) {
	k := occurrenceKey("cb-1", 1_791_432_000_000)
	if got := occurrenceKeyAt(k); got != 1_791_432_000_000 {
		t.Fatalf("解析回不去：%q -> %d", k, got)
	}
	// JS 的 Number('') === 0，所以「没有 #」或后缀为空一律当 0，与 replan 的过期清理口径一致
	if got := occurrenceKeyAt("short"); got != 0 {
		t.Fatalf("没有 # 时应为 0，得到 %d", got)
	}
}

func TestRemindersByIDForCustomBreak(t *testing.T) {
	s := testSettings(t, `{"reminderMessages":{"short":{"order":"fixed","fixedId":"x","items":[{"id":"x","text":"短的"}]}},
		"customBreaks":[{"id":"cb-1","name":"喝水","config":{"intervalMs":60000},
			"messages":{"order":"fixed","fixedId":"m1","items":[{"id":"m1","text":"该喝水了","enabled":true}]}}]}`)
	rm := s.RemindersByID("cb-1")
	if len(rm.Items) != 1 || rm.Items[0].Text != "该喝水了" {
		t.Fatalf("自定义提醒该取自己的提醒内容，得到 %+v", rm.Items)
	}
	if got := s.RemindersByID("short"); len(got.Items) != 1 || got.Items[0].Text != "短的" {
		t.Fatalf("短休息该取共用的那份，得到 %+v", got.Items)
	}
	// 认不出的身份回落到 short（与 types.ts 的 breakMessagesById 一致）
	if got := s.RemindersByID("cb-不存在"); len(got.Items) != 1 || got.Items[0].Text != "短的" {
		t.Fatalf("未知身份应回落 short，得到 %+v", got.Items)
	}
}

func TestDescribePlanText(t *testing.T) {
	at := time.Date(2026, 10, 8, 12, 0, 0, 0, time.Local).UnixMilli()
	got := describePlan(&PlannedBreak{Type: "long", Name: "长休息", At: at, Source: "fixed"})
	if got != "长休息（固定时刻）12:00" {
		t.Fatalf("摘要文案不对：%q", got)
	}
	if got := describePlan(nil); got != "未安排休息" {
		t.Fatalf("无计划时的文案不对：%q", got)
	}
}

// itoa 测试里拼 JSON 用（避免引入 strconv 只为一处）
func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [8]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
