package scheduler

// 时间规划 / 倒计时的回归用例：全是 2026-11-13 审计时按“用户常识期望的结果”写出来、
// 当场跑不通、随后修掉的场景。修完后留在这里防回归（不再是临时证据）。

import (
	"bytes"
	"log"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"workerrest/internal/model"
)

// lockedLogBuf 日志断言用：写日志的可能不止当前测试的 goroutine（别的用例留下的
// Loop / 定时器还活着），裸 bytes.Buffer 会构成数据竞争 —— 与 cmd 包的
// logsink_test.go 同一个教训，这里放一份包内的最小实现。
type lockedLogBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedLogBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedLogBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// plannedAt 造一条挂起线（测试里手工摆“某一跳已经到点”的现场用）
func plannedAt(typ, name string, at int64, src string) model.PlannedBreak {
	return model.PlannedBreak{Type: typ, Name: name, At: at, Source: src}
}

// 到点那一跳碰上“设置页保存触发的重排”不能被整段丢掉：倒计时从头再计一个满间隔的话，
// 用户那一刻本该看到休息窗，结果什么都没发生、还要再等 10 分钟。
// 修法：已到点但未消费的锚点照样沿用（见 plan.countdownAnchor），下一拍直接弹。
func TestDueCountdownLostOnSettingsSave(t *testing.T) {
	s, host := newTestSched(t, shortCountdown)
	s.replan()
	due := nowMs() - 800
	s.mu.Lock()
	s.state.NextAt = due
	for i := range s.state.Lines {
		s.state.Lines[i].At = due
	}
	s.mu.Unlock()

	s.RefreshPlan() // 设置页随手改一项 → autoSave → 重排
	s.onTick()

	if len(host.breaks) == 0 {
		st := s.StateCopy()
		t.Fatalf("已到点的倒计时被重排吃掉：没有弹窗，下一次被推到 %s（距今 %d 秒）",
			time.UnixMilli(st.NextAt).Format("15:04:05"), (st.NextAt-nowMs())/1000)
	}
}

// 两条提醒撞车：一条正在休息时另一条到点，后者不能被静默丢掉一整轮，也不该在休息
// 结束后紧接着再弹一扇窗（十分钟里被连着拦两回）。
// 口径（2026-11-13）：并入当前休息窗口（单窗多条），并把这一班往前推过休息结束时刻
// —— 既不会再补弹，也不会把下一次的节奏提前。
func TestSecondReminderMergedIntoRunningBreak(t *testing.T) {
	const js = `{
		"short":{"enabled":true,"mode":"countdown","intervalMs":600000,"durationMs":20000,"notifyBeforeMs":0,"postponeMs":120000,"slots":[]},
		"long":{"enabled":false},
		"customBreaks":[{"id":"cb-a","name":"喝水提醒","config":{"enabled":true,"mode":"countdown","intervalMs":300000,"durationMs":10000,"notifyBeforeMs":0,"postponeMs":120000,"slots":[]}}]
	}`
	s, host := newTestSched(t, js)
	s.replan()
	now := nowMs()
	s.mu.Lock()
	s.state.Phase = "break"
	s.state.NextType = "short"
	s.state.BreakStartedAt = now - 5000
	s.state.BreakEndsAt = now + 5000 // 这次休息还有 5 秒
	for i := range s.state.Lines {
		if s.state.Lines[i].Type == "cb-a" {
			s.state.Lines[i].At = now - 500 // 喝水提醒在休息期间到点
		}
	}
	s.mu.Unlock()

	s.onTick() // 休息中：并入，不弹窗

	if len(host.breaks) != 0 {
		t.Fatalf("休息期间到点的提醒不该另开窗口，实际弹了 %v", host.breaks)
	}
	if len(host.extras) != 1 || host.extras[0] != "cb-a" {
		t.Fatalf("喝水提醒应并入当前休息窗口，实际并入 %v", host.extras)
	}
	// 并入后锚点必须落在休息结束之后：否则休息一结束 replan 就看见一个过去的锚点，
	// 又变成“紧接着补弹一次”
	s.mu.Lock()
	at, ends := int64(0), s.state.BreakEndsAt
	for _, l := range s.state.Lines {
		if l.Type == "cb-a" {
			at = l.At
		}
	}
	s.mu.Unlock()
	if at <= ends {
		t.Fatalf("并入后这一班应推到休息结束之后（ends=%s），实际 %s",
			time.UnixMilli(ends).Format("15:04:05"), time.UnixMilli(at).Format("15:04:05"))
	}
	// 同一次休息里只并入一次：下一拍不该再冒一张一样的卡
	s.onTick()
	if len(host.extras) != 1 {
		t.Fatalf("同一条提醒在一次休息里只该并入一次，实际 %v", host.extras)
	}
}

// 长休息到点、而正在休息的是短休息：立刻停掉短休息换成这次长休息。
// 统计口径（用户明确）：短休息记「完成 1 次」，不是提前结束 —— 所以收尾必须走
// CompleteBreakWindows（host.completed），而不是普通的关窗。
func TestLongPreemptsRunningShort(t *testing.T) {
	const js = `{
		"short":{"enabled":true,"mode":"countdown","intervalMs":600000,"durationMs":20000,"notifyBeforeMs":0,"postponeMs":120000,"slots":[]},
		"long":{"enabled":true,"mode":"countdown","intervalMs":3600000,"durationMs":60000,"notifyBeforeMs":0,"postponeMs":120000,"slots":[]},
		"longEveryNShort":4
	}`
	s, host := newTestSched(t, js)
	s.replan()
	now := nowMs()
	s.mu.Lock()
	s.state.Phase = "break"
	s.state.NextType = "short"
	s.state.BreakStartedAt = now - 3000
	s.state.BreakEndsAt = now + 17000 // 短休息还剩 17 秒
	// 长休息在休息期间到点（这一拍之前就到了）
	s.mu.Unlock()
	s.mu.Lock()
	s.state.Lines = append(s.state.Lines, plannedAt("long", "长休息", now-200, "countdown"))
	s.mu.Unlock()

	s.onTick()

	if host.completed != 1 {
		t.Fatalf("短休息应被长休息顶替并按“完成”收尾（CompleteBreakWindows 一次），实际 %d", host.completed)
	}
	if len(host.breaks) != 1 || host.breaks[0] != "long" {
		t.Fatalf("应紧接着开始这次长休息，实际弹了 %v", host.breaks)
	}
	if st := s.StateCopy(); st.NextType != "long" || st.Phase != "break" {
		t.Fatalf("抢占后应处于长休息中，实际 phase=%q next=%q", st.Phase, st.NextType)
	}
}

// 长休息进行中短休息到点：短休息这一班按“已被覆盖”记一次完成，不弹卡、也不打断长休息。
func TestShortCountedWhileLongRuns(t *testing.T) {
	const js = `{
		"short":{"enabled":true,"mode":"countdown","intervalMs":600000,"durationMs":20000,"notifyBeforeMs":0,"postponeMs":120000,"slots":[]},
		"long":{"enabled":true,"mode":"countdown","intervalMs":3600000,"durationMs":60000,"notifyBeforeMs":0,"postponeMs":120000,"slots":[]}
	}`
	s, host := newTestSched(t, js)
	s.replan()
	now := nowMs()
	s.mu.Lock()
	s.state.Phase = "break"
	s.state.NextType = "long"
	s.state.BreakStartedAt = now - 60000
	s.state.BreakEndsAt = now + 60000
	s.state.Lines = append(s.state.Lines, plannedAt("short", "短休息", now-100, "countdown"))
	s.mu.Unlock()

	s.onTick()

	if len(host.breaks) != 0 {
		t.Fatalf("长休息期间短休息到点不该另开窗口，实际弹了 %v", host.breaks)
	}
	if len(host.counted) != 1 || host.counted[0] != "short" {
		t.Fatalf("短休息应记一次完成（不是跳过、也不是补弹），实际 %v", host.counted)
	}
	if len(host.extras) != 0 {
		t.Fatalf("内置休息之间不该走“单窗多条”，实际并入 %v", host.extras)
	}
}

// 长短两个倒计时同一刻到点：只弹长休息，短休息按完成记一次。
// 计划层 sortPlans 已经把长休息排在同一时刻的第一条，这里钉住调度器真的照它走。
func TestSameMomentLongBeatsShort(t *testing.T) {
	const js = `{
		"short":{"enabled":true,"mode":"countdown","intervalMs":600000,"durationMs":20000,"notifyBeforeMs":0,"postponeMs":120000,"slots":[]},
		"long":{"enabled":true,"mode":"countdown","intervalMs":600000,"durationMs":60000,"notifyBeforeMs":0,"postponeMs":120000,"slots":[]}
	}`
	s, host := newTestSched(t, js)
	s.replan()
	due := nowMs() - 50
	s.mu.Lock()
	s.state.Lines = nil
	s.state.Lines = append(s.state.Lines, plannedAt("short", "短休息", due, "countdown"))
	s.state.Lines = append(s.state.Lines, plannedAt("long", "长休息", due, "countdown"))
	s.state.Phase = "idle"
	s.state.NextType = "long"
	s.state.NextAt = due
	src := "countdown"
	s.state.NextSource = &src
	s.mu.Unlock()

	s.onTick() // 起长休息
	if len(host.breaks) != 1 || host.breaks[0] != "long" {
		t.Fatalf("同一刻到点应只弹长休息，实际 %v", host.breaks)
	}
	s.onTick() // 下一拍：短休息被覆盖，记完成
	if len(host.breaks) != 1 {
		t.Fatalf("短休息不该再弹窗，实际 %v", host.breaks)
	}
	if len(host.counted) != 1 || host.counted[0] != "short" {
		t.Fatalf("短休息应记一次完成，实际 %v", host.counted)
	}
}

// 暂停中侧栏的「跳过本次」必须真的跳过（曾经 Skip 在暂停态直接 return，UI 也不禁用，
// 点下去既没反应也没提示）。暂停不冻钟，暂停期间这个按钮正是有用的。
func TestSkipDeadWhilePaused(t *testing.T) {
	s, host := newTestSched(t, shortCountdown)
	s.replan()
	at := s.StateCopy().NextAt
	s.Pause(PauseReasonManual)
	before := nowMs()
	s.Skip()
	if len(host.skips) == 0 {
		st := s.StateCopy()
		t.Fatalf("暂停中「跳过本次」是空操作：没有记跳过，下一次仍是 %s（暂停前那一刻）",
			time.UnixMilli(st.NextAt).Format("15:04:05"))
	}
	// 跳过 = 消费这一跳并重排：下一次要从「现在」重新起算一个满间隔，
	// 而不是停在原定时刻 —— 只记统计不挪线，提醒照样来，跳过就成了空话。
	// （这条断言还钉着一个曾经的窄窗缺陷：跳过落在锚点刚生成的同一毫秒时，
	// filterConsumed 会把新锚点误当已消费、整条线删空 → NextAt 归 0，见
	// plan.filterConsumed 的注释。单测里 replan→pause→skip 常在同一毫秒内
	// 完成，正好稳定踩中它。）
	got := s.StateCopy().NextAt
	want := before + 600_000
	if diff := got - want; diff < -2_000 || diff > 2_000 {
		t.Fatalf("跳过后应重排成 现在+间隔：want≈%d got=%d（差 %d ms，原定 %d）", want, got, diff, at)
	}
}

// 暂停中侧栏的「推迟休息」同样要真的把这一跳往后挪（曾经与 Skip 一样在暂停态直接 return）。
// 挪多少也要钉：推迟口径 = max(now, 原定) + postponeMs（原定在 10 分钟外 → 取原定），
// 新时刻必须正好是 at+120000 —— 今日时间线 / 提前提示区间全跟着这个数走，
// 「往后挪了但挪错了量」与没挪一样是缺陷。
func TestPostponeDeadWhilePaused(t *testing.T) {
	s, host := newTestSched(t, shortCountdown)
	s.replan()
	at := s.StateCopy().NextAt
	s.Pause(PauseReasonManual)
	s.Postpone()
	if len(host.postpones) == 0 {
		t.Fatalf("暂停中「推迟休息」是空操作：OnBreakPostponed 未被调用")
	}
	got := s.StateCopy().NextAt
	if got != at+120_000 {
		t.Fatalf("推迟后应为 原定+postponeMs：want=%d got=%d（原定 %d，差 %d ms）",
			at+120_000, got, at, got-(at+120_000))
	}
}

// postpone 的挂起线失配分支（actions.go 的「挂起线里没有 %d 这一刻」）：
// NextAt 与 Lines 失配只可能来自异常状态，该分支的全部产出就是那条日志 ——
// 没有它，点了推迟、界面无声无变化。这里构造失配状态，钉住日志真的打出来。
func TestPostponeLineMismatchIsAudible(t *testing.T) {
	buf := &lockedLogBuf{}
	log.SetOutput(buf)
	defer log.SetOutput(os.Stderr)

	s, host := newTestSched(t, shortCountdown)
	s.replan()
	s.mu.Lock()
	s.state.NextAt += 7_777 // 与挂起线失配（线里记的还是旧时刻）
	s.mu.Unlock()
	s.Postpone()

	if !strings.Contains(buf.String(), "挂起线里没有") {
		t.Fatalf("失配分支应打「挂起线里没有…」日志（否则推迟变成无声空操作），实际日志尾部：%q",
			tailOf(buf.String(), 300))
	}
	// 回调照常发生：统计口径的过滤在 App 侧（只认短 / 长），调度层不该擅自吞掉。
	if len(host.postpones) == 0 {
		t.Fatalf("线失配时 OnBreakPostponed 仍应被调用")
	}
}

// tailOf 取日志尾部 n 个字符（失败信息里塞全文会把别的用例的日志也带进来）
func tailOf(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// 长短交替的既定口径（设置页“?”提示与手册都这么写）：计数到 N 之后换成**长休息**，
// 它的等待时长取长休息自己的“间隔”，从第 N 次短休息结束那一刻起算。
// 这条用例钉的是“换成了哪一条”与“间隔按谁算”，不是“第 N 次之后立刻就能歇”：
// 短休息间隔 20 分钟 + 长休息间隔 60 分钟，第 4 次短休息结束后要等 60 分钟才轮到长休息，
// 这是配置本身决定的（把长休息间隔改小即可），不是漏排。
func TestLongBreakGapAfterNthShort(t *testing.T) {
	const js = `{
		"short":{"enabled":true,"mode":"countdown","intervalMs":1200000,"durationMs":20000,"notifyBeforeMs":0,"postponeMs":120000,"slots":[]},
		"long":{"enabled":true,"mode":"countdown","intervalMs":3600000,"durationMs":300000,"notifyBeforeMs":0,"postponeMs":120000,"slots":[]},
		"longEveryNShort":4
	}`
	s, _ := newTestSched(t, js)
	s.mu.Lock()
	s.state.ShortsSinceLong = 4 // 第 4 次短休息刚结束（计数已推进）
	s.mu.Unlock()
	s.replan()
	st := s.StateCopy()
	if st.NextType != "long" {
		t.Fatalf("第 4 次之后应轮到长休息，得到 %q", st.NextType)
	}
	gap := (st.NextAt - nowMs()) / 60_000
	if gap < 59 || gap > 61 {
		t.Fatalf("长休息的等待时长应取它自己的间隔（60 分钟），实际 %d 分钟", gap)
	}
}

// 「提前通知」不能超过间隔的一半：填成 >= 间隔时刚排好就落进提前区间，状态永远停在
// “即将休息”、右下角小窗常驻。归一化里钳住（settings.breakConfigOf），设置页保存时
// 做同一件事并把输入框改回真实生效值。
func TestNotifyBeforeLongerThanInterval(t *testing.T) {
	const js = `{"short":{"enabled":true,"mode":"countdown","intervalMs":60000,"durationMs":20000,"notifyBeforeMs":300000,"postponeMs":120000,"slots":[]}}`
	s, host := newTestSched(t, js)
	s.replan()
	s.onTick()
	if s.CurrentPhase() == "pre" || len(host.pre) > 0 {
		st := s.StateCopy()
		t.Fatalf("间隔 60 秒、提前通知 300 秒：刚排好就进“即将休息”（剩余 %d 秒），状态将永远停在这里",
			(st.NextAt-nowMs())/1000)
	}
}

// 「等待空闲」的那一跳时刻在过去，今日时间线不该把它当成一条“待触发”列出来
// （列表标题就是“待触发 N 条”，列一条已经过去的时刻会让用户以为漏了）。
// 口径：倒计时项的 At < now 一律不列（到点那一拍还没弹完的那一瞬间也不列）。
func TestTimelineShowsPastIdleWait(t *testing.T) {
	const js = `{"short":{"enabled":true,"mode":"countdown","intervalMs":600000,"durationMs":20000,"notifyBeforeMs":10000,"postponeMs":120000,"idleGate":true,"idleGateMs":30000,"slots":[]}}`
	s, _ := newTestSched(t, js)
	s.replan()
	now := nowMs()
	s.mu.Lock()
	s.idleWait = &idleWait{kind: "short", name: "短休息", at: now - 90_000, source: "countdown"}
	s.mu.Unlock()
	s.scheduleNext()

	st := s.StateForRenderer()
	for _, it := range st.Timeline {
		if it.At < now-60_000 {
			t.Fatalf("时间线把“等待空闲”的那一跳（%s）当成待触发项列了出来",
				time.UnixMilli(it.At).Format("15:04:05"))
		}
	}
}
