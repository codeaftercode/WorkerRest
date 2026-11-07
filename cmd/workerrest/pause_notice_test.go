package main

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"workerrest/internal/model"
	"workerrest/internal/scheduler"
)

// 自动暂停期间的“已暂停多久”提示（见 pause_notice.go）。
//
// 三组用例分别钉住三件事：哪些原因不许弹（投屏 / 全屏或演示 / 请勿打扰）、什么时候弹
// （满一小时 / 压制解除补一次 / 刚弹过不重复），以及**文案里不许出现“休息”**
// —— 最后这条是产品要求，不是口味问题：这扇窗可能被旁人看到。

func TestPauseNoticeSuppressedMatrix(t *testing.T) {
	cases := []struct {
		name   string
		active map[string]bool
		want   bool
	}{
		{"什么都没生效", map[string]bool{}, false},
		{"勿扰", map[string]bool{pauseReasonDnd: true}, true},
		{"全屏或演示", map[string]bool{pauseReasonFullScr: true}, true},
		{"复制模式", map[string]bool{pauseReasonCastMirror: true}, true},
		{"无线投放", map[string]bool{pauseReasonCastWireless: true}, true},
		// 这两条不压制：人不在座位上时弹窗没人看；应用占用只说明某个软件在跑，
		// 屏幕还是私人的，右下角出现一句“已暂停”不会让谁尴尬。
		{"空闲", map[string]bool{scheduler.PauseReasonIdle: true}, false},
		{"应用占用", map[string]bool{pauseReasonAppPause: true}, false},
		// 条件已消失（登记为 false）不等于生效
		{"换屏但条件已消失", map[string]bool{pauseReasonCastMirror: false}, false},
		{"勿扰已关但投屏还在", map[string]bool{pauseReasonDnd: false, pauseReasonCastWireless: true}, true},
	}
	for _, c := range cases {
		if got := pauseNoticeSuppressed(c.active); got != c.want {
			t.Errorf("%s：pauseNoticeSuppressed=%v，期望 %v", c.name, got, c.want)
		}
	}
}

func TestPauseNoticeDue(t *testing.T) {
	const hour = int64(3600_000)
	const pausedAt = int64(1_000_000)
	cases := []struct {
		name   string
		paused int64
		last   int64
		now    int64
		want   bool
	}{
		{"刚暂停", pausedAt, 0, pausedAt + 1_000, false},
		{"差 1 秒满一小时", pausedAt, 0, pausedAt + hour - 1_000, false},
		{"整满一小时", pausedAt, 0, pausedAt + hour, true},
		{"超出一小时", pausedAt, 0, pausedAt + hour + 5_000, true},
		// 弹过之后从“上次弹出”重新算，不是从暂停开始算 —— 否则第二小时会立刻再弹一次
		{"弹过 10 分钟", pausedAt, pausedAt + hour, pausedAt + hour + 600_000, false},
		{"距上次弹出满一小时", pausedAt, pausedAt + hour, pausedAt + 2*hour, true},
		// 时钟被往回拨：宁可不弹，也不要在时间乱跳时连弹
		{"时钟回拨", pausedAt, 0, pausedAt - 60_000, false},
		{"没有暂停时刻", 0, 0, 9_999_999, false},
		{"间隔非法", pausedAt, 0, pausedAt + hour, false},
	}
	for _, c := range cases {
		interval := hour
		if c.name == "间隔非法" {
			interval = 0
		}
		if got := pauseNoticeDue(c.paused, c.last, c.now, interval); got != c.want {
			t.Errorf("%s：pauseNoticeDue=%v，期望 %v", c.name, got, c.want)
		}
	}
}

// 一拍一拍地推，覆盖“压制 → 解除 → 再满一小时”的完整时序。
func TestPauseNoticeStepTimeline(t *testing.T) {
	const hour = int64(3600_000)
	start := int64(10_000_000)
	// 开会两小时，全程投屏；散会后再挂一小时才该有第二条
	var st pauseNoticeState

	// 00:00 进入自动暂停（投屏中）→ 不弹
	show, st := pauseNoticeStep(st, start, start, true)
	if show {
		t.Fatal("投屏过程中一进来就弹了")
	}
	if !st.Suppressed {
		t.Fatal("压制标记没记下：解除那一刻就补不出提示")
	}
	// 01:00 还在投屏 → 即使满一小时也不弹
	show, st = pauseNoticeStep(st, start, start+hour, true)
	if show {
		t.Fatal("投屏期间满一小时也不许弹（窗口会被分享出去）")
	}
	// 01:59 还在投屏
	show, st = pauseNoticeStep(st, start, start+hour+59*60_000, true)
	if show {
		t.Fatal("投屏期间不许弹")
	}
	// 02:00 投屏断了：压制解除的那一刻先补一次（此时已远超一小时）
	show, st = pauseNoticeStep(st, start, start+2*hour, false)
	if !show {
		t.Fatal("压制解除那一刻应补弹一次")
	}
	if st.ShownAt != start+2*hour {
		t.Fatalf("弹出时刻没记下：%d", st.ShownAt)
	}
	// 02:00:05 下一拍：刚弹过，不许连弹
	show, st = pauseNoticeStep(st, start, start+2*hour+5_000, false)
	if show {
		t.Fatal("同一段暂停里连弹了两次")
	}
	// 02:30 未满一小时 → 不弹
	show, st = pauseNoticeStep(st, start, start+2*hour+1_800_000, false)
	if show {
		t.Fatal("距上次弹出未满一小时就弹了")
	}
	// 03:00 满一小时 → 弹（兜底节奏回到正常）
	show, _ = pauseNoticeStep(st, start, start+3*hour, false)
	if !show {
		t.Fatal("距上次弹出满一小时应该再弹一次")
	}
}

// 压制解除但刚弹过（不到最短间隔）：开关一次全屏不该换来两条提示。
func TestPauseNoticeStepEdgeRespectsMinGap(t *testing.T) {
	start := int64(20_000_000)
	pausedAt := start - 3_600_000
	st := pauseNoticeState{PausedAt: pausedAt, ShownAt: start}
	// 30 秒前刚弹过，随后进全屏又退出
	_, st = pauseNoticeStep(st, pausedAt, start+30_000, true)
	show, _ := pauseNoticeStep(st, pausedAt, start+31_000, false)
	if show {
		t.Fatal("刚弹过 30 秒，压制解除不该再弹一次")
	}
}

// 从未压制过的自动暂停：纯粹按一小时一弹。
func TestPauseNoticeStepPlainHourly(t *testing.T) {
	start := int64(30_000_000)
	var st pauseNoticeState
	for i := 0; i < 60; i++ { // 每分钟一拍，推到 59 分
		show, next := pauseNoticeStep(st, start, start+int64(i)*60_000, false)
		if show {
			t.Fatalf("第 %d 分钟就弹了（未满一小时）", i)
		}
		st = next
	}
	show, st := pauseNoticeStep(st, start, start+3_600_000, false)
	if !show {
		t.Fatal("满一小时没弹")
	}
	// 再推一小时
	show, _ = pauseNoticeStep(st, start, start+3_600_000+30_000, false)
	if show {
		t.Fatal("刚弹过就再弹")
	}
}

// 换了暂停段：不继承上一段的节流与压制标记。
//
// “投屏中暂停 → 用户点恢复 → 一秒后勿扰又挂起”是最容易踩到的形态：上一段残留的
// Suppressed 会被当成“压制刚解除”，新一段的第一拍就补弹 —— 而用户刚点过恢复。
func TestPauseNoticeNewSegmentStartsFresh(t *testing.T) {
	first := int64(80_000_000)
	show, st := pauseNoticeStep(pauseNoticeState{}, first, first, true)
	if show || !st.Suppressed || st.PausedAt != first {
		t.Fatalf("第一段首拍状态不对：show=%v st=%+v", show, st)
	}
	second := first + 1_000
	show, st = pauseNoticeStep(st, second, second, false)
	if show {
		t.Fatal("新一段暂停的第一拍就弹了（继承了上一段的压制标记）")
	}
	if st.ShownAt != 0 || st.PausedAt != second {
		t.Fatalf("新一段应从零开始，实际 %+v", st)
	}
}

// 文案：必须说清“已暂停多久”，且绝不出现与休息有关的字眼。
func TestPauseNoticeTextWording(t *testing.T) {
	cases := []struct {
		ms   int64
		want string
	}{
		{42 * 60_000, "已暂停 42 分钟"},
		{60 * 60_000, "已暂停 1 小时"},
		{95 * 60_000, "已暂停 1 小时 35 分钟"},
		// 不足一分钟（实际不会走到，最早也要满一小时）也要是一句通顺的话
		{5_000, "已暂停 1 分钟"},
	}
	for _, c := range cases {
		got := pauseNoticeText(c.ms)
		if got != c.want {
			t.Errorf("pauseNoticeText(%d)=%q，期望 %q", c.ms, got, c.want)
		}
		for _, bad := range []string{"休息", "提醒", "健康", "站起来", "活动"} {
			if strings.Contains(got, bad) {
				t.Errorf("文案 %q 出现了 %q —— 这扇小窗可能被旁人看到，不能带这类字眼", got, bad)
			}
		}
	}
}

// 页面源码也钉住：文案由主进程下发，但页面里的静态兜底文字同样可能被改出“休息”。
// 这扇窗的对外可见性让它比别的文案更敏感，所以连 html / js / css / 页面标题一起扫。
func TestPauseNoticePageHasNoRestWording(t *testing.T) {
	for _, name := range []string{"pause.html", "pause.js", "pause.css"} {
		p := filepath.Join("web", "dist", name)
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("读 %s 失败：%v", p, err)
		}
		src := string(b)
		for _, bad := range []string{"休息", "休息一下", "该休息"} {
			if strings.Contains(src, bad) {
				t.Errorf("%s 出现了 %q —— 这扇小窗可能被旁人看到，不能带这类字眼", p, bad)
			}
		}
		if name == "pause.html" && !strings.Contains(src, "shim.js") {
			t.Errorf("%s 少了 shim.js：按钮点了不会有任何反应", p)
		}
	}
}

// 窗口名与 URL 的 ?win= 必须一致：CloseWindow / ensureWindowsOnScreen / 退出清理
// 都按名字找窗口，两处漂移就会出现“关不掉的小窗”。
func TestPauseNoticeWindowNameMatchesURL(t *testing.T) {
	u, err := url.Parse(pauseNoticeURL("已暂停 1 小时"))
	if err != nil {
		t.Fatalf("URL 解析失败：%v", err)
	}
	if got := u.Query().Get("win"); got != pauseNoticeWindowName {
		t.Fatalf("URL 的 win=%q 与窗口名 %q 不一致", got, pauseNoticeWindowName)
	}
	if got := u.Query().Get("text"); got != "已暂停 1 小时" {
		t.Fatalf("文案没经 query 正确下发：%q", got)
	}
	if !strings.HasPrefix(u.Path, "/pause.html") {
		t.Fatalf("指向的页面不对：%q", u.Path)
	}
}

// 手动暂停不弹：用户自己按的暂停，托盘上一直写着已暂停多久，不该再被弹窗提醒。
func TestPauseNoticeIgnoresManualPause(t *testing.T) {
	a, sched := newPreBreakApp(t, customWaterCfg)
	sched.Pause(scheduler.PauseReasonManual)
	a.pauseNoticeTick()

	a.pauseNoticeMu.Lock()
	shown := a.pauseNoticeShownAt
	a.pauseNoticeMu.Unlock()
	if shown != 0 {
		t.Fatalf("手动暂停不该弹提示，实际记下弹出时刻 %d", shown)
	}
}

// 自动暂停刚开始不弹（还没满一小时），但节流状态要跟着在走。
func TestPauseNoticeAutoPauseStartsQuiet(t *testing.T) {
	a, sched := newPreBreakApp(t, customWaterCfg)
	sched.Pause(pauseReasonAppPause)
	a.pauseNoticeTick()

	a.pauseNoticeMu.Lock()
	shown := a.pauseNoticeShownAt
	a.pauseNoticeMu.Unlock()
	if shown != 0 {
		t.Fatalf("自动暂停刚开始就弹了，实际记下弹出时刻 %d", shown)
	}
}

// “满一小时”这条在真实时间下跑不到，用 pauseNoticeAdvance 直接喂构造好的状态：
// 无头模式下 showPauseNotice 只记日志（不建窗），但弹出时刻必须记账。
func TestPauseNoticeFiresAfterOneHour(t *testing.T) {
	a, _ := newPreBreakApp(t, customWaterCfg)
	now := int64(50_000_000)
	st := model.SchedulerState{Paused: true, PausedAt: now - 3_600_000, PauseReasons: []string{pauseReasonAppPause}}

	a.pauseNoticeAdvance(st, true, now)

	a.pauseNoticeMu.Lock()
	shown := a.pauseNoticeShownAt
	a.pauseNoticeMu.Unlock()
	if shown != now {
		t.Fatalf("满一小时应弹出并记下时刻 %d，实际 %d", now, shown)
	}

	// 恢复（不再处于暂停）后节流清零，下一段暂停重新计时
	a.pauseNoticeAdvance(model.SchedulerState{}, false, now+1_000)
	a.pauseNoticeMu.Lock()
	shown = a.pauseNoticeShownAt
	suppressed := a.pauseNoticeSuppressed
	a.pauseNoticeMu.Unlock()
	if shown != 0 || suppressed {
		t.Fatalf("恢复后节流状态应清零，实际 shown=%d suppressed=%v", shown, suppressed)
	}
}

// 压制解除那一刻补弹：投屏条件由监视链路登记在 autoReasonActive 里。
func TestPauseNoticeFiresWhenCastEnds(t *testing.T) {
	a, _ := newPreBreakApp(t, customWaterCfg)
	now := int64(60_000_000)
	st := model.SchedulerState{Paused: true, PausedAt: now - 7_200_000, PauseReasons: []string{pauseReasonCastMirror}}

	// 投屏中：满两小时也不弹
	a.setAutoReasonActive(pauseReasonCastMirror, true)
	a.pauseNoticeAdvance(st, true, now)
	a.pauseNoticeMu.Lock()
	shown := a.pauseNoticeShownAt
	a.pauseNoticeMu.Unlock()
	if shown != 0 {
		t.Fatalf("投屏期间不许弹，实际记下 %d", shown)
	}

	// 投屏断了（条件消失，但按策略暂停仍保留）：这一刻补弹一次
	a.setAutoReasonActive(pauseReasonCastMirror, false)
	a.pauseNoticeAdvance(st, true, now+3_000)
	a.pauseNoticeMu.Lock()
	shown = a.pauseNoticeShownAt
	a.pauseNoticeMu.Unlock()
	if shown != now+3_000 {
		t.Fatalf("投屏结束后应补弹一次（记下 %d），实际 %d", now+3_000, shown)
	}

	// 下一拍不重复
	a.pauseNoticeAdvance(st, true, now+8_000)
	a.pauseNoticeMu.Lock()
	again := a.pauseNoticeShownAt
	a.pauseNoticeMu.Unlock()
	if again != shown {
		t.Fatalf("压制解除后连弹了两次：%d → %d", shown, again)
	}
}

// “只关提示、不恢复”不能顺手把节流清掉 —— 否则连点几下关闭就能把整条提示关掉一天。
// 这里用无头 App 直接驱动绑定方法，验证它只关窗、不改节流。
func TestPauseNoticeDismissKeepsThrottle(t *testing.T) {
	a, _ := newPreBreakApp(t, customWaterCfg)
	now := int64(70_000_000)
	a.pauseNoticeStore(pauseNoticeState{ShownAt: now})
	a.PauseNoticeDismiss()
	a.pauseNoticeMu.Lock()
	defer a.pauseNoticeMu.Unlock()
	if a.pauseNoticeShownAt != now {
		t.Fatalf("关闭提示不该重置节流，实际 %d", a.pauseNoticeShownAt)
	}
}
