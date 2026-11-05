package scheduler

import (
	"encoding/json"
	"testing"
	"time"
	"workerrest/internal/model"
	"workerrest/internal/plan"
	"workerrest/internal/settings"
)

// 调度器与计划层的纯函数回归。
//
// 分工（与外部对拍互补，别搞混）：
//   - 这里拦「规则写错」：消费记账、暂停冻结平移、休息总时长封顶、跨天截断……
//     毫秒级跑完，改完代码先跑它。
//   - scripts/verify_plan_parity.py 拦「与上一版 不一致」：拿 上一版自己的
//     breakPlan.ts 当黄金参考逐字段对拍 27 个场景。
// 两者都过才算这一层真的对。

// ---------- 脚手架 ----------

// testSettings 用一段 JSON 造一份配置（只填本次用到的字段，其余按 breakConfigOf 的默认）
func testSettings(t *testing.T, js string) *settings.Settings {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(js), &m); err != nil {
		t.Fatalf("测试配置解析失败: %v", err)
	}
	return settings.New(m, "")
}

// fakeHost 调度器宿主：只实现调度器真正会调的那几个口子，不碰窗口 / 托盘 / Wails，
// 这样状态机可以脱离 application.App 跑单测。
type fakeHost struct {
	cfg        *settings.Settings
	states     int
	pre        []string
	pauses     int
	autoPauses int
	breaks     []string
	closed     int
	closeWin   string
	postpones  []string
	skips      []string
	pp         *model.TakeoutPostponed
}

func (h *fakeHost) CfgSnapshot() *settings.Settings                   { return h.cfg }
func (h *fakeHost) OnSchedulerState()                                 { h.states++ }
func (h *fakeHost) OnPreNotify(kind string, _ int64)                  { h.pre = append(h.pre, kind) }
func (h *fakeHost) OnPaused()                                         { h.pauses++ }
func (h *fakeHost) OnAutoPause()                                      { h.autoPauses++ }
func (h *fakeHost) ClosePreBreak()                                    {}
func (h *fakeHost) CloseBreakWindows()                                { h.closed++ }
func (h *fakeHost) TakeoutPostponedSnapshot() *model.TakeoutPostponed { return h.pp }
func (h *fakeHost) TakeoutFiredSnapshot() map[string]bool             { return nil }
func (h *fakeHost) OnBreakPostponed(kind string, _ int64)             { h.postpones = append(h.postpones, kind) }
func (h *fakeHost) OnBreakSkipped(kind string, _ int64)               { h.skips = append(h.skips, kind) }
func (h *fakeHost) ShowBreakSession(kind string, _ bool, _ string) string {
	h.breaks = append(h.breaks, kind)
	return "id-" + kind
}

func newTestSched(t *testing.T, cfgJSON string) (*Scheduler, *fakeHost) {
	t.Helper()
	host := &fakeHost{cfg: testSettings(t, cfgJSON)}
	return New(host), host
}

// shortCountdown 常用的短休息倒计时配置（10 分钟一次 20 秒）
const shortCountdown = `{"short":{"enabled":true,"mode":"countdown","intervalMs":600000,"durationMs":20000,"notifyBeforeMs":10000,"postponeMs":120000,"slots":[]}}`

// ---------- 状态机 ----------

func TestSchedulerReplanCountdown(t *testing.T) {
	s, _ := newTestSched(t, shortCountdown)
	before := nowMs()
	s.replan()
	got := s.StateCopy()
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
	first := s.StateCopy().NextAt

	// 模拟时间流逝：用户已等了 4 分钟（像暂停恢复那样整体平移）
	s.mu.Lock()
	s.state.NextAt -= 4 * 60_000
	for i := range s.state.Lines {
		s.state.Lines[i].At = s.state.NextAt
	}
	s.mu.Unlock()

	s.replan()
	got := s.StateCopy()
	want := first - 4*60_000
	if got.NextAt != want {
		t.Fatalf("replan 不该重置未到期的倒计时（应保留已流逝的 4 分钟），want=%d got=%d", want, got.NextAt)
	}
	if len(got.Lines) != 1 || got.Lines[0].At != got.NextAt {
		t.Fatalf("挂起线应同步，得到 %+v", got.Lines)
	}

	// 跳过：该 occurrence 已消费，倒计时必须从头计时（不能沿用旧锚点）
	s.Skip()
	got2 := s.StateCopy()
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
	got := s.StateCopy()
	want := nowMs() + 2_700_000
	if diff := got.NextAt - want; diff < -2000 || diff > 2000 {
		t.Fatalf("改间隔后应立刻按新间隔重算，want≈%d got=%d（差 %d）", want, got.NextAt, diff)
	}

	// 对照组：间隔没变的无关保存不得动倒计时
	s2, _ := newTestSched(t, shortCountdown)
	s2.replan()
	keep := s2.StateCopy().NextAt
	s2.mu.Lock()
	s2.state.NextAt -= 4 * 60_000
	for i := range s2.state.Lines {
		s2.state.Lines[i].At = s2.state.NextAt
	}
	s2.mu.Unlock()
	s2.replan()
	if got2 := s2.StateCopy().NextAt; got2 != keep-4*60_000 {
		t.Fatalf("间隔未变时 replan 不该重置倒计时，want=%d got=%d", keep-4*60_000, got2)
	}
}

func TestSchedulerModeSwitchToFixedWithoutSlotsDropsReminder(t *testing.T) {
	// 倒计时切固定时间且没有任何时刻：应立刻按「没有这条提醒」重算——
	// 挂起线 / 下次休息 / 分组视图 / 今日时间线都不再出现按倒计时算出来的旧时刻
	s, host := newTestSched(t, shortCountdown)
	s.replan()
	if s.StateCopy().NextAt == 0 {
		t.Fatal("前置：应已安排短休息")
	}
	host.cfg = testSettings(t, `{"short":{"enabled":true,"mode":"fixed","durationMs":20000,"slots":[]},"long":{"enabled":false}}`)
	s.RefreshPlan()

	st := s.StateCopy()
	if st.NextAt != 0 || st.NextSource != nil {
		t.Fatalf("切到无时刻的固定模式后不该再有安排，nextAt=%d source=%v", st.NextAt, st.NextSource)
	}
	if len(st.Lines) != 0 {
		t.Fatalf("挂起线应为空，得到 %+v", st.Lines)
	}
	if st.Rest != nil {
		t.Fatalf("分组视图 rest 应为 nil，得到 %+v", st.Rest)
	}
	sr := s.StateForRenderer()
	if len(sr.Timeline) != 0 {
		t.Fatalf("今日时间线应为空，得到 %+v", sr.Timeline)
	}

	// 自定义提醒同理
	s3, host3 := newTestSched(t, `{"short":{"enabled":false},"long":{"enabled":false},
		"customBreaks":[{"id":"cb-1","name":"喝水提醒","config":{"enabled":true,"mode":"countdown","intervalMs":600000,"durationMs":15000}}]}`)
	s3.replan()
	if s3.StateCopy().NextAt == 0 {
		t.Fatal("前置：自定义提醒应已安排")
	}
	host3.cfg = testSettings(t, `{"short":{"enabled":false},"long":{"enabled":false},
		"customBreaks":[{"id":"cb-1","name":"喝水提醒","config":{"enabled":true,"mode":"fixed","durationMs":15000,"slots":[]}}]}`)
	s3.RefreshPlan()
	st3 := s3.StateCopy()
	if st3.NextAt != 0 || len(st3.Lines) != 0 || st3.Other != nil {
		t.Fatalf("自定义提醒切固定无时刻后应全部消失，nextAt=%d lines=%+v other=%+v", st3.NextAt, st3.Lines, st3.Other)
	}
	if sr3 := s3.StateForRenderer(); len(sr3.Timeline) != 0 {
		t.Fatalf("今日时间线应为空，得到 %+v", sr3.Timeline)
	}
}

func TestSchedulerAllDisabledNoPlan(t *testing.T) {
	s, _ := newTestSched(t, `{"short":{"enabled":false},"long":{"enabled":false}}`)
	s.replan()
	got := s.StateCopy()
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

// 「提前通知」为 0 秒：不进 pre 阶段、不触发 onPreNotify —— 到点直接开始休息，
// 不弹右下角提前提示窗（ notifyBeforeMs>0 的守卫在 onTick 与恢复暂停两条路径上都有）
func TestSchedulerNoPreNotifyWhenZero(t *testing.T) {
	cfg := `{"short":{"enabled":true,"mode":"countdown","intervalMs":600000,` +
		`"durationMs":20000,"notifyBeforeMs":0,"postponeMs":120000,"slots":[]}}`
	s, host := newTestSched(t, cfg)
	s.replan()

	// 挪到「已到点」：notifyBeforeMs=0 时 pre 与 due 同边界，只有守卫能拦住 pre
	s.mu.Lock()
	s.state.NextAt = nowMs() - 1
	for i := range s.state.Lines {
		s.state.Lines[i].At = s.state.NextAt
	}
	s.mu.Unlock()

	s.onTick()
	if len(host.pre) != 0 {
		t.Fatalf("notifyBeforeMs=0 不该触发提前通知，得到 %v", host.pre)
	}
	if got := s.StateCopy().Phase; got != "break" {
		t.Fatalf("到点应直接进入休息，得到 %q", got)
	}
	if len(host.breaks) != 1 || host.breaks[0] != "short" {
		t.Fatalf("到点应正常开始短休息，得到 %v", host.breaks)
	}
}

func TestSchedulerPostponeShiftsPlan(t *testing.T) {
	s, _ := newTestSched(t, shortCountdown)
	s.replan()
	origAt := s.StateCopy().NextAt

	s.Postpone()
	got := s.StateCopy()
	// 推迟是「在原定时刻基础上往后推」：原定 10 分钟后 + 可推迟 2 分钟。
	// （旧实现写 now+ms，会把还剩 10 分钟的提醒直接拉到 2 分钟后 —— P2-1）
	want := origAt + 120_000
	if diff := got.NextAt - want; diff < -2000 || diff > 2000 {
		t.Fatalf("推迟后应是原定时刻+2 分钟，want=%d got=%d", want, got.NextAt)
	}
	if got.NextAt < origAt {
		t.Fatalf("推迟不该把提醒提前：orig=%d got=%d", origAt, got.NextAt)
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

// 原定时刻已经过去（例如系统刚从睡眠里醒、tick 还没跟上）时，推迟仍要保证往后走：
// 基准退化成「现在」，不能算出比现在更早的时刻。
func TestSchedulerPostponeFromOverdue(t *testing.T) {
	s, _ := newTestSched(t, shortCountdown)
	s.replan()
	s.mu.Lock()
	s.state.NextAt = nowMs() - 60_000 // 已经过期 1 分钟
	s.mu.Unlock()

	s.Postpone()
	got := s.StateCopy()
	want := nowMs() + 120_000
	if diff := got.NextAt - want; diff < -2000 || diff > 2000 {
		t.Fatalf("过期后推迟应是现在+2 分钟，want=%d got=%d", want, got.NextAt)
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
	planned := s.StateCopy().NextAt
	if planned != aligned {
		t.Fatalf("规划时刻应为 %d，得到 %d", aligned, planned)
	}

	s.Skip()
	if !s.consumed[plan.OccurrenceKey("short", aligned)] {
		t.Fatal("跳过必须消费掉本次 occurrence（否则固定线会原样排回来）")
	}
	after := s.StateCopy().NextAt
	if after == aligned {
		t.Fatal("跳过之后不该还排在同一个时刻")
	}
	if plan.SameLocalDay(after, aligned) {
		t.Fatalf("今天这条已跳过的线不该再出现，得到 %d", after)
	}
}

func TestSchedulerBreakLifecycleAndCounter(t *testing.T) {
	s, host := newTestSched(t, `{"short":{"enabled":true,"mode":"countdown","intervalMs":600000,"durationMs":20000},
		"long":{"enabled":true,"mode":"countdown","intervalMs":1800000,"durationMs":60000},"longEveryNShort":2}`)
	s.replan()

	id := s.BreakNowOf("")
	if id == "" || len(host.breaks) != 1 || host.breaks[0] != "short" {
		t.Fatalf("应立即弹一次短休息，id=%q breaks=%v", id, host.breaks)
	}
	st := s.StateCopy()
	if st.Phase != "break" {
		t.Fatalf("phase 应为 break，得到 %q", st.Phase)
	}
	if st.BreakEndsAt-st.BreakStartedAt != 20_000 {
		t.Fatalf("本次休息时长应为 20s，得到 %d", st.BreakEndsAt-st.BreakStartedAt)
	}
	if st.ShortsSinceLong != 0 {
		t.Fatal("休息中不该提前计数")
	}

	// 隔开两毫秒再结束：同一毫秒内「开始 → 结束」时，重排算出的新倒计时时刻会与被消费的
	// 那次撞在同一个毫秒上、被「已消费」过滤掉（下一个 tick 才恢复）。那是瞬态，不属于
	// 本用例要验的「计数推进 → 下一跳类型」。
	time.Sleep(2 * time.Millisecond)
	s.EndBreak()
	st = s.StateCopy()
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
	if got := s.StateCopy().NextType; got != "long" {
		t.Fatalf("满 N 次应轮到长休息，得到 %q", got)
	}

	s.BreakNowOf("")
	s.EndBreak()
	if got := s.StateCopy().ShortsSinceLong; got != 0 {
		t.Fatalf("长休息结束应把计数清零，得到 %d", got)
	}
}

// 休息进行中点「跳过 / 提前结束」：必须真的结束这次休息，而不是静默 return。
// 设置页侧栏、托盘、快捷键都走 skip 这一个入口（P3）。
func TestSchedulerSkipWhileBreakingEndsBreak(t *testing.T) {
	s, host := newTestSched(t, shortCountdown)
	s.replan()
	if id := s.BreakNowOf(""); id == "" {
		t.Fatal("应能开始一次休息")
	}
	if got := s.StateCopy().Phase; got != "break" {
		t.Fatalf("phase 应为 break，得到 %q", got)
	}

	s.Skip()
	st := s.StateCopy()
	if st.Phase == "break" {
		t.Fatal("休息中点跳过应结束本次休息")
	}
	if host.closed != 1 {
		t.Fatalf("应关一次休息窗口，得到 %d", host.closed)
	}
	if st.Phase != "idle" {
		t.Fatalf("结束后应回到 idle，得到 %q", st.Phase)
	}
	// 与 endBreak 同一条路径：本次照常消费掉（短休息计数 +1），不会排回同一条
	if st.ShortsSinceLong != 1 {
		t.Fatalf("跳过结束也应消费本次，计数 +1，得到 %d", st.ShortsSinceLong)
	}
}

func TestSchedulerPauseReasonsGate(t *testing.T) {
	s, host := newTestSched(t, shortCountdown)
	s.Pause("idle")
	if !s.StateCopy().Paused {
		t.Fatal("pause 后应为暂停态")
	}
	// 自动原因走清场路径（收全部窗口），不是只关提前提示窗的那条
	if host.autoPauses != 1 || host.pauses != 0 {
		t.Fatalf("自动暂停应通知宿主清场，得到 autoPauses=%d pauses=%d", host.autoPauses, host.pauses)
	}
	s.Pause("idle") // 重复原因不该重复入列
	if n := len(s.StateCopy().PauseReasons); n != 1 {
		t.Fatalf("重复原因应被忽略，得到 %d 条", n)
	}
	s.Pause("manual")
	if n := len(s.StateCopy().PauseReasons); n != 2 {
		t.Fatalf("应有 2 条暂停原因，得到 %d", n)
	}
	// 只清一条：仍然暂停
	s.Resume("idle")
	st := s.StateCopy()
	if !st.Paused || len(st.PauseReasons) != 1 {
		t.Fatalf("还剩一条原因时应保持暂停，得到 paused=%v reasons=%v", st.Paused, st.PauseReasons)
	}
	// 最后一条清掉：恢复
	s.Resume("manual")
	if s.StateCopy().Paused {
		t.Fatal("全部原因清掉后应恢复")
	}
}

func TestSchedulerResumeShiftsCountdown(t *testing.T) {
	s, _ := newTestSched(t, shortCountdown)
	s.replan()
	origAt := s.StateCopy().NextAt

	s.Pause("manual")
	// 假装已经暂停了 50 秒（不真的等）
	s.mu.Lock()
	s.state.PausedAt = nowMs() - 50_000
	s.mu.Unlock()
	s.Resume("manual")

	got := s.StateCopy().NextAt
	want := origAt + 50_000
	if diff := got - want; diff < -1500 || diff > 1500 {
		t.Fatalf("倒计时线应整体平移 50s，want=%d got=%d（差 %d）", want, got, diff)
	}
	if !plan.SameLocalDay(got, origAt) {
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

	s.Resume("manual")
	st := s.StateCopy()
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

	s.ReanchorCountdown()
	got := s.StateCopy()
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
	s2.ReanchorCountdown()
	if got := s2.StateCopy().NextAt; got != sentinel {
		t.Fatalf("自定义提醒的倒计时不该被一次离开推走（%d → %d）", sentinel, got)
	}
}

func TestSchedulerBreakNowIgnoredWhileBreaking(t *testing.T) {
	s, host := newTestSched(t, shortCountdown)
	s.replan()
	s.BreakNowOf("")
	s.BreakNowOf("")
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
	s.BreakNowOf("") // nextType 已指向不存在的身份
	if len(host.breaks) != 0 {
		t.Fatalf("已删除的休息不该弹窗，得到 %v", host.breaks)
	}
	if s.StateCopy().Phase != "idle" {
		t.Fatal("应回到等待态并重排")
	}
}

func TestSchedulerStateForRendererIncludesTimeline(t *testing.T) {
	s, _ := newTestSched(t, shortCountdown)
	s.replan()
	st := s.StateForRenderer()
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
