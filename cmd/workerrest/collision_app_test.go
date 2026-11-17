package main

// 撞车的**宿主侧穷举**（2026-11-16）。
//
// internal/scheduler/collision_matrix_test.go 用独立模型证明了“哪一种撞车该触发哪个
// 宿主回调”（开窗序列 / 完成次数 / 并入清单 / BreakType / 倒计时）。这份文件接着验
// 那些回调在 App 里各自落下什么副作用，并把它们串成一场完整的撞车：
//
//	① 提示音：哪种撞车出声、出几声（无头构建里“发声”落在 [sound] 日志上）；
//	② 统计：完成 / 提前结束 / 自定义各记几次，时长只记一遍；
//	③ 副条：并入清单、搬去下一次休息（长休息抢占）、外卖交回独立窗；
//	④ 倒计时：并入副条 / 记完成都不许改动主休息的结束时刻；
//	⑤ 托盘：BreakType 换人之后文案与可点性跟着换（含“不允许提前结束”的那张卡）；
//	⑥ 提前提示窗：它在场不算休息会话；窗上按钮打的是“窗自己提示的那条提醒”。

import (
	"log"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"workerrest/internal/model"
	"workerrest/internal/scheduler"
)

// collCfg 撞车矩阵用的配置：短 20 秒 / 长 60 秒 / 三条自定义各 10 秒，其中 cb-b 故意
// 不勾「允许提前结束」（托盘判据要认“当前这次休息自己那张卡”）。
const collCfg = `{
  "short":{"enabled":true,"mode":"countdown","intervalMs":600000,"durationMs":20000,"notifyBeforeMs":0,"postponeMs":120000,"allowEarlyEnd":true,"slots":[]},
  "long":{"enabled":true,"mode":"countdown","intervalMs":3600000,"durationMs":60000,"notifyBeforeMs":0,"postponeMs":120000,"allowEarlyEnd":true,"slots":[]},
  "sound":{"enabled":true,"volume":0.6,"mode":"single","singleId":"sm-builtin-short_start",
           "music":[{"id":"sm-builtin-short_start","file":"short_start.wav","name":"短休息开始","enabled":true}]},
  "takeout":{"enabled":true,"leadSec":0,"postponeMin":15,"slots":[{"id":"s1","hour":12,"minute":30,"repeat":"daily","days":[],"enabled":true}]},
  "customBreaks":[
    {"id":"cb-a","name":"喝水提醒","config":{"enabled":true,"mode":"countdown","intervalMs":300000,"durationMs":10000,"notifyBeforeMs":0,"postponeMs":120000,"allowEarlyEnd":true,"slots":[]}},
    {"id":"cb-b","name":"站起来走走","config":{"enabled":true,"mode":"countdown","intervalMs":300000,"durationMs":10000,"notifyBeforeMs":0,"postponeMs":120000,"allowEarlyEnd":false,"slots":[]}},
    {"id":"cb-c","name":"看看远处","config":{"enabled":true,"mode":"countdown","intervalMs":300000,"durationMs":10000,"notifyBeforeMs":0,"postponeMs":120000,"allowEarlyEnd":true,"slots":[]}}
  ]
}`

// newCollApp 撞车用的 App：调度器真接到 App（与 Wails 宿主同一形状），提示音指向仓库里
// 的内置音（不指的话 playSoundFile 走“静音”分支，声数断言就全是 0，用例失去意义）。
func newCollApp(t *testing.T) *App {
	t.Helper()
	injectBuiltinSoundDir(t, filepath.Join(repoRoot(t), "assets", "sounds"))
	a := newExtraApp(t, collCfg)
	a.sched = scheduler.New(&appTestHost{testHost: &testHost{cfg: a.cfg, breakOK: true}, a: a})
	a.sched.ResetCycle()
	t.Cleanup(a.sched.Stop)
	return a
}

// collOpen 起一次休息作为“在场者”，返回 App。开会话那一声提示音在调用方捕获日志之前
// 就已经放完了，所以后续 countSoundPlays 只数撞车这一下。
func collOpen(t *testing.T, kind string) *App {
	t.Helper()
	a := newCollApp(t)
	if id := a.sched.BreakNowOf(kind); id == "" {
		t.Fatalf("前置条件不成立：%s 没能起一次休息", kind)
	}
	a.mu.Lock()
	ok := a.sess != nil && a.sess.Type == kind && !a.sess.Preview
	a.mu.Unlock()
	if !ok {
		t.Fatalf("前置条件不成立：%s 的休息会话没建起来", kind)
	}
	return a
}

// collExtras 当前会话右上角的副条（拷贝一份，避免测试持锁）
func collExtras(a *App) []model.BreakExtra {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sess == nil {
		return nil
	}
	return append([]model.BreakExtra(nil), a.sess.Extras...)
}

// collExtraKinds 副条身份清单（"cb-a,cb-c,takeout"）
func collExtraKinds(a *App) string {
	var ks []string
	for _, e := range collExtras(a) {
		ks = append(ks, e.Kind)
	}
	return strings.Join(ks, ",")
}

// collLineAt 某个身份最早的那条挂起线（没有则 0）
func collLineAt(t *testing.T, a *App, kind string) int64 {
	t.Helper()
	at := int64(0)
	for _, l := range a.sched.StateCopy().Lines {
		if l.Type != kind {
			continue
		}
		if at == 0 || l.At < at {
			at = l.At
		}
	}
	return at
}

// ---------- ① 提示音：每种撞车各响几声 ----------

// 撞车时“该不该出声”是用户唯一能立刻感知的指标，也最容易两声叠一起。
// 口径（2026-11-16 用户口径）：
//   - 长休息抢占 = 新一次休息开始了 → 只响新休息的开始音（旧窗的结束音不响）；
//   - 并入副条 / 记完成 = 不是新的一次休息，也不该打断用户 → 一声都不响；
//   - 正常收窗 = 一声结束音；
//   - 提前结束且有卡接力 = 换窗整段静音；队头是外卖时不能静音（外卖窗不经过开始音，
//     让掉就一声都没有了）。
func TestCollisionSoundMatrix(t *testing.T) {
	cases := []struct {
		name  string
		occ   string
		setup func(*App)
		act   func(*App)
		want  int
	}{
		{
			name: "长休息抢占短休息：只响新休息的开始音",
			occ:  "short",
			act: func(a *App) {
				a.closeBreakWindowsOpt(true, true, true, true) // 抢占收窗（静音）
				a.ShowBreakSession("long", false, "")
			},
			want: 1,
		},
		{
			name: "长休息抢占自定义提醒：只响新休息的开始音",
			occ:  "cb-a",
			act: func(a *App) {
				a.closeBreakWindowsOpt(true, true, true, true)
				a.ShowBreakSession("long", false, "")
			},
			want: 1,
		},
		{
			name: "休息中并入自定义提醒：不出声",
			occ:  "short",
			act:  func(a *App) { a.OnBreakExtra("cb-a", nowMs()) },
			want: 0,
		},
		{
			name:  "休息中并入第二条自定义提醒：仍不出声",
			occ:   "long",
			setup: func(a *App) { a.OnBreakExtra("cb-a", nowMs()) },
			act:   func(a *App) { a.OnBreakExtra("cb-c", nowMs()) },
			want:  0,
		},
		{
			name: "休息中并入外卖：不出声",
			occ:  "short",
			act: func(a *App) {
				a.attachTakeoutToBreak(&model.TakeoutInfo{ID: "tk-1", At: nowMs(), Meal: "午餐"})
			},
			want: 0,
		},
		{
			name: "短休息被覆盖记完成：不出声",
			occ:  "long",
			act:  func(a *App) { a.OnBreakCounted("short") },
			want: 0,
		},
		{
			name: "自然到点收窗：一声结束音",
			occ:  "short",
			act:  func(a *App) { a.closeBreakWindowsOpt(false, true, false, false) },
			want: 1,
		},
		{
			name: "自定义提醒自然到点：也是一声",
			occ:  "cb-a",
			act:  func(a *App) { a.closeBreakWindowsOpt(false, true, false, false) },
			want: 1,
		},
		{
			name: "提前结束、没有副条：一声结束音",
			occ:  "short",
			act:  func(a *App) { a.schedEndBreak() },
			want: 1,
		},
		{
			name:  "提前结束、右上角有卡接力：整段静音",
			occ:   "short",
			setup: func(a *App) { a.OnBreakExtra("cb-a", nowMs()) },
			act:   func(a *App) { a.schedEndBreak() },
			want:  0,
		},
		{
			// 队头是外卖时不能让音：外卖窗不经过 ShowBreakSession（没有开始音），
			// 让掉就彻底没声了。server 模式下外卖窗不建、放音也跳过，所以这里只见
			// 结束音那一声；GUI 下外卖窗自己还会放一声，那是对的（那是“外卖到了”）。
			name: "提前结束、只有外卖在挂：仍响结束音",
			occ:  "short",
			setup: func(a *App) {
				a.attachTakeoutToBreak(&model.TakeoutInfo{ID: "tk-1", At: nowMs(), Meal: "午餐"})
			},
			act:  func(a *App) { a.schedEndBreak() },
			want: 1,
		},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			a := collOpen(t, c.occ)
			if c.setup != nil {
				c.setup(a)
			}
			n, out := countSoundPlays(t, func() { c.act(a) })
			if n != c.want {
				t.Fatalf("提示音条数应为 %d，实际 %d：\n%s", c.want, n, out)
			}
		})
	}
}

// 抢占时必须真的把上一首掐掉（长休息抢短休息时，短休息那首可能还在放）。
// server 模式没有播放宿主，stopSounds 直接返回 —— 这里只钉“走了停音那条分支”，
// 真正的淡出由 node 桩验（见 scripts 里的 sound 脚本用例）。
func TestCollisionPreemptStopsPreviousSound(t *testing.T) {
	a := collOpen(t, "short")
	_, out := countSoundPlays(t, func() {
		a.closeBreakWindowsOpt(true, true, true, true)
	})
	if !strings.Contains(out, "[sound] 换窗") {
		t.Fatalf("抢占收窗应当走“结束音不响 + 停掉上一首”那条分支，实际日志：\n%s", out)
	}
}

// ---------- ② 统计：谁记几次、时长只记一遍 ----------

// 长休息抢占短休息：短休息记「完成 1 次」（不是提前结束），长休息照常记一次。
func TestCollisionStatsPreemptShortByLong(t *testing.T) {
	a := collOpen(t, "short")

	// 抢占：收窗（按完成记账、副条搬走）→ 起长休息
	time.Sleep(3 * time.Millisecond) // 让这一段休息真的记到一点时长（否则全是 0ms）
	a.closeBreakWindowsOpt(true, true, true, true)
	a.ShowBreakSession("long", false, "")
	// 长休息自然到点
	a.mu.Lock()
	a.sess.EndsAt = nowMs() - 10
	a.mu.Unlock()
	time.Sleep(3 * time.Millisecond)
	a.closeBreakWindowsOpt(false, true, false, false)

	d := a.statsStore().Today()
	if d.ShortCount != 1 {
		t.Fatalf("被顶替的短休息应记完成 1 次，实际 %d", d.ShortCount)
	}
	if d.LongCount != 1 {
		t.Fatalf("长休息应记完成 1 次，实际 %d", d.LongCount)
	}
	if d.EndEarlyCount != 0 {
		t.Fatalf("抢占不是提前结束，EndEarlyCount 应为 0，实际 %d", d.EndEarlyCount)
	}
	if d.BreakMs <= 0 || d.ShortMs <= 0 || d.LongMs <= 0 {
		t.Fatalf("两段休息的时长都该入账，实际 break=%d short=%d long=%d",
			d.BreakMs, d.ShortMs, d.LongMs)
	}
	if d.BreakMs != d.ShortMs+d.LongMs {
		t.Fatalf("休息总时长应等于短+长（不能重复计），实际 %d != %d+%d",
			d.BreakMs, d.ShortMs, d.LongMs)
	}
}

// 长休息抢占“自定义提醒”：那次自定义提醒确实显示过 → 记一笔；随后长休息单独记一笔。
func TestCollisionStatsPreemptCustomByLong(t *testing.T) {
	a := collOpen(t, "cb-a")

	a.closeBreakWindowsOpt(true, true, true, true)
	a.ShowBreakSession("long", false, "")
	a.mu.Lock()
	a.sess.EndsAt = nowMs() - 10
	a.mu.Unlock()
	a.closeBreakWindowsOpt(false, true, false, false)

	d := a.statsStore().Today()
	if d.CustomCounts["cb-a"] != 1 {
		t.Fatalf("被顶替的自定义提醒应记 1 次，实际 %v", d.CustomCounts)
	}
	if d.CustomCount != 1 {
		t.Fatalf("自定义提醒总数应为 1，实际 %d", d.CustomCount)
	}
	if d.LongCount != 1 {
		t.Fatalf("长休息应记完成 1 次，实际 %d", d.LongCount)
	}
	if d.EndEarlyCount != 0 {
		t.Fatalf("抢占不该记提前结束，实际 %d", d.EndEarlyCount)
	}
}

// 一次休息里并入两条不同的自定义提醒：各记一次；同一条重复并入只记一次。
// 时长不因并入而增加（那段时间已经由主休息记了）。
func TestCollisionStatsMergedExtras(t *testing.T) {
	a := collOpen(t, "short")
	a.mu.Lock()
	beforeEnds := a.sess.EndsAt
	a.mu.Unlock()

	a.OnBreakExtra("cb-a", nowMs())
	a.OnBreakExtra("cb-c", nowMs())
	a.OnBreakExtra("cb-a", nowMs()) // 同一条再来一次：只挂一张卡

	if got := collExtraKinds(a); got != "cb-a,cb-c" {
		t.Fatalf("同一条提醒在一次休息里只该挂一张卡，实际 %q", got)
	}
	a.mu.Lock()
	afterEnds := a.sess.EndsAt
	a.mu.Unlock()
	if afterEnds != beforeEnds {
		t.Fatalf("并入副条不该改动主休息的结束时刻（%d → %d）", beforeEnds, afterEnds)
	}

	a.mu.Lock()
	a.sess.EndsAt = nowMs() - 10
	a.mu.Unlock()
	a.closeBreakWindowsOpt(false, true, false, false)

	d := a.statsStore().Today()
	if d.CustomCounts["cb-a"] != 1 || d.CustomCounts["cb-c"] != 1 {
		t.Fatalf("两条并入的提醒应各记 1 次，实际 %v", d.CustomCounts)
	}
	if d.CustomCount != 2 {
		t.Fatalf("自定义提醒总数应为 2，实际 %d", d.CustomCount)
	}
	if d.ShortCount != 1 {
		t.Fatalf("主休息应记完成 1 次，实际 %d", d.ShortCount)
	}
	if d.BreakMs != d.ShortMs || d.LongMs != 0 {
		t.Fatalf("并入的提醒不该另记时长（break=%d short=%d long=%d）",
			d.BreakMs, d.ShortMs, d.LongMs)
	}
}

// 并入的外卖不计进休息那一组统计（它有自己的三次数：点了 / 取消了 / 推迟了）。
func TestCollisionStatsMergedTakeoutIsNotBreakStats(t *testing.T) {
	a := collOpen(t, "short")
	if !a.attachTakeoutToBreak(&model.TakeoutInfo{ID: "tk-1", At: nowMs(), Meal: "午餐"}) {
		t.Fatal("休息窗在场时外卖应并入当前休息窗")
	}
	a.mu.Lock()
	a.sess.EndsAt = nowMs() - 10
	a.mu.Unlock()
	a.closeBreakWindowsOpt(false, true, false, false)

	d := a.statsStore().Today()
	if d.CustomCount != 0 || len(d.CustomCounts) != 0 {
		t.Fatalf("外卖不该记进自定义提醒统计，实际 %d / %v", d.CustomCount, d.CustomCounts)
	}
	if d.TakeoutOrdered+d.TakeoutSkipped+d.TakeoutPostponed != 0 {
		t.Fatalf("并入外卖本身不算一次外卖动作，实际 %d/%d/%d",
			d.TakeoutOrdered, d.TakeoutSkipped, d.TakeoutPostponed)
	}
	if d.ShortCount != 1 {
		t.Fatalf("主休息仍应记完成 1 次，实际 %d", d.ShortCount)
	}
}

// ---------- ③ 副条：长休息抢占时整串搬过去 ----------

// 抢占换窗时，右上角已经并入的副条必须跟着搬到新一次休息上 —— 那些提醒在并入时就已经
// 消费掉了，随旧窗消失就真的没了。而且搬过去的那一刻还**不该**记账（本次休息还没结束）。
func TestCollisionExtrasCarriedToPreemptingBreak(t *testing.T) {
	a := collOpen(t, "short")
	a.OnBreakExtra("cb-a", nowMs())
	a.attachTakeoutToBreak(&model.TakeoutInfo{ID: "tk-1", At: nowMs(), Meal: "午餐"})
	if got := collExtraKinds(a); got != "cb-a,takeout" {
		t.Fatalf("前置条件不成立：副条应为 cb-a,takeout，实际 %q", got)
	}

	a.closeBreakWindowsOpt(true, true, true, true) // 抢占收窗（carry=true）
	a.ShowBreakSession("long", false, "")

	if got := collExtraKinds(a); got != "cb-a,takeout" {
		t.Fatalf("副条应整串搬到新一次休息上，实际 %q", got)
	}
	if n := a.statsStore().Today().CustomCounts["cb-a"]; n != 0 {
		t.Fatalf("搬过去时不该提前记账（这次休息还没结束），实际 %d", n)
	}

	// 长休息结束：cb-a 按“显示过一次”记一笔，外卖交回独立窗口
	a.mu.Lock()
	a.sess.EndsAt = nowMs() - 10
	a.mu.Unlock()
	buf := &lockedBuffer{}
	old := log.Writer()
	log.SetOutput(buf)
	a.closeBreakWindowsOpt(false, true, false, false)
	log.SetOutput(old)
	out := buf.String()

	if n := a.statsStore().Today().CustomCounts["cb-a"]; n != 1 {
		t.Fatalf("休息结束时并入的提醒应记 1 次，实际 %d", n)
	}
	if !strings.Contains(out, "把并进来的外卖提醒交回独立窗口") {
		t.Fatalf("并进来的外卖没被交回独立窗口：\n%s", out)
	}
	a.mu.Lock()
	cur := a.takeoutCurrent
	a.mu.Unlock()
	if cur == nil || cur.Preview {
		t.Fatalf("交回之后应有一份外卖会话，实际 %+v", cur)
	}
}

// ---------- ④ 倒计时：并入 / 记完成都不动主休息 ----------
// 三件事都不能改动主休息的剩余时间：并入自定义、并入外卖、另一条内置休息被覆盖。
// （倒计时是用户盯着看的那个数，被副条挤短或拉长都会让人以为休息时长变了。）
func TestCollisionCountdownUntouchedByMerges(t *testing.T) {
	a := collOpen(t, "long")
	a.mu.Lock()
	ends := a.sess.EndsAt
	dur := a.sess.DurationMs
	a.mu.Unlock()

	a.OnBreakExtra("cb-a", nowMs())
	a.attachTakeoutToBreak(&model.TakeoutInfo{ID: "tk-1", At: nowMs(), Meal: "午餐"})
	a.OnBreakCounted("short") // 短休息在长休息期间到点：只记完成

	a.mu.Lock()
	got := a.sess.EndsAt
	gotDur := a.sess.DurationMs
	a.mu.Unlock()
	if got != ends || gotDur != dur {
		t.Fatalf("并入 / 记完成不该改动主休息（endsAt %d→%d，duration %d→%d）",
			ends, got, dur, gotDur)
	}
	if st := a.sched.StateForRenderer(); st.BreakType != "long" {
		t.Fatalf("正在弹的应当还是长休息，实际 BreakType=%q", st.BreakType)
	}
}

// ---------- ⑤ 托盘：撞车换人之后文案与可点性跟着换 ----------

// 托盘第一项的名字与可点性只认**当前这次休息**（BreakType + 那张卡的「允许提前结束」）。
// 撞车的几种结果各走一遍：并入副条不该动它，抢占换成长休息之后仍按长休息那张卡。
func TestCollisionTrayFollowsCurrentBreak(t *testing.T) {
	cases := []struct {
		name     string
		occ      string
		wantText string
		wantOff  bool
	}{
		{"短休息在弹：提前结束休息、可点", "short", "提前结束休息", false},
		{"长休息在弹：提前结束休息、可点", "long", "提前结束休息", false},
		{"自定义提醒在弹：立即休息、灰", "cb-a", "立即休息", true},
		{"不允许提前结束的自定义提醒在弹：同样灰", "cb-b", "立即休息", true},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			a := collOpen(t, c.occ)
			// 先并一条副条进来：撞车不该把托盘那一项带跑
			a.OnBreakExtra("cb-c", nowMs())

			st := a.sched.StateForRenderer()
			if st.BreakType != c.occ {
				t.Fatalf("BreakType 应为 %q，实际 %q", c.occ, st.BreakType)
			}
			_, _, action, _ := trayTextsAt(st, false, nil, nowMs())
			if action != c.wantText {
				t.Fatalf("托盘第一项文案应为 %q，实际 %q", c.wantText, action)
			}
			if got := a.trayActionOff(st); got != c.wantOff {
				t.Fatalf("托盘第一项置灰应为 %v，实际 %v", c.wantOff, got)
			}
		})
	}

	// 抢占之后身份换成 long：文案仍是“提前结束休息”，可点性按长休息那张卡（允许）
	t.Run("抢占换成长休息后仍按长休息那张卡", func(t *testing.T) {
		a := collOpen(t, "short")
		a.sched.EndBreak()
		a.sched.BreakNowOf("long")
		st := a.sched.StateForRenderer()
		if st.BreakType != "long" {
			t.Fatalf("换人之后 BreakType 应为 long，实际 %q", st.BreakType)
		}
		_, _, action, _ := trayTextsAt(st, false, nil, nowMs())
		if action != "提前结束休息" || a.trayActionOff(st) {
			t.Fatalf("长休息在弹时托不应改口/变灰（action=%q off=%v）", action, a.trayActionOff(st))
		}
	})
}

// ---------- ⑥ 提前提示窗与撞车 ----------

// 提前提示窗在场时**没有**休息会话：这时另一条提醒到点不能“并进小窗”，
// 否则那一班既没显示也没记账（调度器据 OnBreakExtra 的返回值决定消费还是不消费）。
func TestCollisionPreWindowIsNotABreakSession(t *testing.T) {
	a := newCollApp(t)
	a.rememberPreBreakInfo("short") // 小窗已记住“短休息即将开始”
	if a.sess != nil {
		t.Fatal("前置条件不成立：还没起休息，不该有会话")
	}
	if ok := a.OnBreakExtra("cb-a", nowMs()); ok {
		t.Fatal("提前提示窗不算休息会话，另一条提醒不该被并入")
	}
	if got := collExtraKinds(a); got != "" {
		t.Fatalf("没有会话就不该有副条，实际 %q", got)
	}

	// 预览会话同样不算：预览是“看一眼窗口长什么样”，并进去会污染真实状态
	a2 := collOpen(t, "short")
	a2.mu.Lock()
	a2.sess.Preview = true
	a2.mu.Unlock()
	if ok := a2.OnBreakExtra("cb-a", nowMs()); ok {
		t.Fatal("预览会话不该接受并入")
	}
}

// 提前提示窗上的按钮打的是**它自己提示的那条提醒**，不是调度器此刻的“下一条”。
// 撞车场景里这两者经常不是同一个（窗上是自定义提醒，下一条是短休息）。
func TestCollisionPreWindowActionsTargetOwnReminder(t *testing.T) {
	t.Run("立即休息：起的是窗上那条（自定义提醒），不是下一条短休息", func(t *testing.T) {
		a := newCollApp(t)
		next, _ := a.sched.StateCopy().NextType, 0
		a.rememberPreBreakInfo("cb-a")
		if err := a.PreBreakAction("breakNow"); err != nil {
			t.Fatalf("breakNow 不该报错：%v", err)
		}
		st := a.sched.StateForRenderer()
		if st.Phase != "break" || st.BreakType != "cb-a" {
			t.Fatalf("应当起窗上那条自定义提醒，实际 phase=%q breakType=%q（下一条本来是 %q）",
				st.Phase, st.BreakType, next)
		}
		a.mu.Lock()
		sessType := a.sess.Type
		a.mu.Unlock()
		if sessType != "cb-a" {
			t.Fatalf("会话身份应为 cb-a，实际 %q", sessType)
		}
	})

	t.Run("跳过：跳过的是窗上那条，另一条不跟着被消费", func(t *testing.T) {
		a := newCollApp(t)
		a.rememberPreBreakInfo("cb-a")
		if err := a.PreBreakAction("skip"); err != nil {
			t.Fatalf("skip 不该报错：%v", err)
		}
		st := a.sched.StateCopy()
		if st.Phase != "idle" {
			t.Fatalf("跳过后应回到 idle，实际 %q", st.Phase)
		}
		if st.NextType == "" || st.NextAt == 0 {
			t.Fatalf("跳过后仍应安排下一次提醒，实际 nextType=%q nextAt=%d", st.NextType, st.NextAt)
		}
		// 跳过只记内置短 / 长（自定义提醒不进“休息”那一组统计）
		if d := a.statsStore().Today(); d.SkipCount != 0 {
			t.Fatalf("跳过自定义提醒不该记休息跳过，实际 %d", d.SkipCount)
		}
	})

	t.Run("推迟：推迟的是窗上那条，推迟后仍在等下一次", func(t *testing.T) {
		a := newCollApp(t)
		a.rememberPreBreakInfo("short")
		before := collLineAt(t, a, "short")
		if before == 0 {
			t.Fatal("前置条件不成立：短休息没有挂起的下一班")
		}
		if err := a.PreBreakAction("postpone"); err != nil {
			t.Fatalf("postpone 不该报错：%v", err)
		}
		// 推的是**这条提醒自己的那一跳**：全局 NextAt 可能还是别的提醒（配置里还有
		// 更早到点的自定义提醒 / 外卖），所以断言落在挂起线上，不是 NextAt。
		after := collLineAt(t, a, "short")
		if d := after - before; d < 119_000 || d > 121_000 {
			t.Fatalf("推迟应把短休息往后推 120000 ms（before=%d after=%d，差 %d）",
				before, after, d)
		}
		if st := a.sched.StateCopy(); st.Phase != "idle" {
			t.Fatalf("推迟后应回到 idle，实际 %q", st.Phase)
		}
	})
}

// ---------- ⑦ 并入顺序：自定义提醒与外卖的每一种先后 ----------

// appPermutations 全部排列（长度 min..max），撞车的“先后顺序”靠它铺开
func appPermutations(items []string, min, max int) [][]string {
	var out [][]string
	var walk func(cur []string, used []bool)
	walk = func(cur []string, used []bool) {
		if len(cur) >= min {
			out = append(out, append([]string(nil), cur...))
		}
		if len(cur) == max {
			return
		}
		for i := range items {
			if used[i] {
				continue
			}
			used[i] = true
			walk(append(cur, items[i]), used)
			used[i] = false
		}
	}
	walk(nil, make([]bool, len(items)))
	return out
}

// collMerge 按真实路径把一条副条并进当前休息（外卖走 attachTakeoutToBreak）
func collMerge(a *App, kind string) bool {
	if kind == "takeout" {
		return a.attachTakeoutToBreak(&model.TakeoutInfo{ID: "tk-1", At: nowMs(), Meal: "午餐"})
	}
	return a.OnBreakExtra(kind, nowMs())
}

// 在场者 × {自定义 / 外卖} 的每一种并入先后：
//   - 右上角的顺序 = 并入顺序（窗口整块重绘，先后必须与到点先后一致）；
//   - 无论谁先谁后，主休息的结束时刻都不动；
//   - 收尾时每条自定义各记一次、外卖不进自定义统计；
//   - 外卖并进来之后，休息结束要交回独立窗口。
func TestCollisionMergeOrderPermutations(t *testing.T) {
	wantBuiltin := map[string]int64{"short": 1, "long": 1}
	for _, occ := range []string{"short", "long", "cb-a"} {
		occ := occ
		pool := []string{"cb-a", "cb-b", "cb-c", "takeout"}
		var items []string
		for _, k := range pool {
			if k != occ { // 正在弹的那条不会并进自己
				items = append(items, k)
			}
		}
		for _, seq := range appPermutations(items, 1, 3) {
			seq := seq
			t.Run(occ+"/"+strings.Join(seq, "→"), func(t *testing.T) {
				a := collOpen(t, occ)
				a.mu.Lock()
				ends := a.sess.EndsAt
				a.mu.Unlock()

				for _, k := range seq {
					if !collMerge(a, k) {
						t.Fatalf("前置条件不成立：%s 没并进当前休息", k)
					}
				}
				if got := collExtraKinds(a); got != strings.Join(seq, ",") {
					t.Fatalf("副条顺序应与并入顺序一致（期望 %q，实际 %q）", strings.Join(seq, ","), got)
				}
				a.mu.Lock()
				ends2 := a.sess.EndsAt
				a.mu.Unlock()
				if ends2 != ends {
					t.Fatalf("并入副条不该改动主休息的结束时刻（%d → %d）", ends, ends2)
				}

				// 自然收尾
				a.mu.Lock()
				a.sess.EndsAt = nowMs() - 10
				a.mu.Unlock()
				buf := &lockedBuffer{}
				old := log.Writer()
				log.SetOutput(buf)
				a.closeBreakWindowsOpt(false, true, false, false)
				log.SetOutput(old)

				want := map[string]int64{}
				customs := 0
				for _, k := range seq {
					if k == "takeout" {
						continue
					}
					want[k]++
					customs++
				}
				if occ == "cb-a" {
					want["cb-a"]++
					customs++
				}
				d := a.statsStore().Today()
				for k, n := range want {
					if d.CustomCounts[k] != n {
						t.Fatalf("%s 应记 %d 次，实际 %d（全表 %v）", k, n, d.CustomCounts[k], d.CustomCounts)
					}
				}
				if d.CustomCount != int64(customs) {
					t.Fatalf("自定义提醒总数应为 %d，实际 %d", customs, d.CustomCount)
				}
				if n, ok := wantBuiltin[occ]; ok {
					got := d.LongCount
					if occ == "short" {
						got = d.ShortCount
					}
					if got != n {
						t.Fatalf("%s 应记完成 %d 次，实际 %d", occ, n, got)
					}
				}
				for _, k := range seq {
					if k != "takeout" {
						continue
					}
					if !strings.Contains(buf.String(), "把并进来的外卖提醒交回独立窗口") {
						t.Fatalf("外卖并入后应在休息结束时交回独立窗口：\n%s", buf.String())
					}
				}
			})
		}
	}
}

// ---------- ⑧ 端到端：真实 tick 跑一场完整撞车 ----------

// 真实调度循环（500ms 一跳）下的一场完整撞车：短休息先到点，两条自定义提示随后到点
// 一并进来；然后连点三次“提前结束”，两条提示依次接力成一扇扇新窗，最后收尾。
// 一次把「并入 / 不另开窗 / 倒计时不动 / 提示音 / 统计 / 托盘」全串起来验。
const collE2ECfg = `{
  "short":{"enabled":true,"mode":"countdown","intervalMs":2000,"durationMs":20000,"notifyBeforeMs":0,"postponeMs":120000,"allowEarlyEnd":true,"slots":[]},
  "long":{"enabled":false,"mode":"countdown","intervalMs":3600000,"durationMs":60000,"notifyBeforeMs":0,"postponeMs":120000,"allowEarlyEnd":true,"slots":[]},
  "sound":{"enabled":true,"volume":0.6,"mode":"single","singleId":"sm-builtin-short_start",
           "music":[{"id":"sm-builtin-short_start","file":"short_start.wav","name":"短休息开始","enabled":true}]},
  "customBreaks":[
    {"id":"cb-a","name":"喝水提醒","config":{"enabled":true,"mode":"countdown","intervalMs":3000,"durationMs":5000,"notifyBeforeMs":0,"postponeMs":120000,"allowEarlyEnd":true,"slots":[]}},
    {"id":"cb-b","name":"站起来走走","config":{"enabled":true,"mode":"countdown","intervalMs":5000,"durationMs":5000,"notifyBeforeMs":0,"postponeMs":120000,"allowEarlyEnd":true,"slots":[]}}
  ]
}`

// waitFor 轮询等待（真实时间用例专用：500ms 一跳的调度循环没法同步驱动）
func waitFor(t *testing.T, what string, limit time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("等待超时：%s", what)
}

func TestCollisionEndToEndRealTicks(t *testing.T) {
	injectBuiltinSoundDir(t, filepath.Join(repoRoot(t), "assets", "sounds"))
	a := newExtraApp(t, collE2ECfg)
	a.sched = scheduler.New(&appTestHost{testHost: &testHost{cfg: a.cfg, breakOK: true}, a: a})
	t.Cleanup(a.sched.Stop)
	// 统计从零起算：同进程里别的用例（以及上一轮被中断的跑动）可能在临时目录里
	// 留下同一份 stats.json，绝对值断言必须先有一份干净的基线。
	a.statsStore().Clear()

	buf := &lockedBuffer{}
	old := log.Writer()
	log.SetOutput(buf)
	defer log.SetOutput(old)
	defer func() {
		if t.Failed() {
			str := a.statsStore()
			t.Logf("现场：lines=%+v extras=%q statsFile=%s counts=%v\n日志：\n%s", a.sched.StateCopy().Lines, collExtraKinds(a), str.File(), str.Today().CustomCounts, buf.String())
		}
	}()

	a.sched.ResetCycle()
	go a.sched.Loop()

	// ① 短休息先到点
	waitFor(t, "短休息到点开窗", 8*time.Second, func() bool {
		a.mu.Lock()
		hasSess := a.sess != nil
		a.mu.Unlock()
		return hasSess && a.sched.CurrentPhase() == "break"
	})
	a.mu.Lock()
	typ, ends := a.sess.Type, a.sess.EndsAt
	a.mu.Unlock()
	if typ != "short" {
		t.Fatalf("先到点的应是短休息，实际 %q", typ)
	}

	// ② 两条自定义提醒依次并入
	waitFor(t, "cb-a 并入", 8*time.Second, func() bool {
		return strings.Contains(collExtraKinds(a), "cb-a")
	})
	waitFor(t, "cb-b 并入", 8*time.Second, func() bool {
		return strings.Contains(collExtraKinds(a), "cb-b")
	})

	// 并入不另开窗：日志里只该有“开始休息”一次
	if n := strings.Count(buf.String(), "开始休息"); n != 1 {
		t.Fatalf("并入不该另开窗口（开始休息出现 %d 次）：\n%s", n, buf.String())
	}
	// 主休息的倒计时与身份都不被副条带跑
	a.mu.Lock()
	ends2 := a.sess.EndsAt
	a.mu.Unlock()
	if ends2 != ends {
		t.Fatalf("并入副条不该改动主休息的结束时刻（%d → %d）", ends, ends2)
	}
	st := a.sched.StateForRenderer()
	if st.BreakType != "short" {
		t.Fatalf("正在弹的应仍是短休息，实际 %q", st.BreakType)
	}
	if _, _, action, _ := trayTextsAt(st, false, nil, nowMs()); action != "提前结束休息" {
		t.Fatalf("托盘第一项应为“提前结束休息”，实际 %q", action)
	}
	if a.trayActionOff(st) {
		t.Fatal("短休息允许提前结束，托盘那一项不该灰")
	}

	// ③ 连点三次提前结束：cb-a → cb-b → 收尾
	nextKind := func() string {
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.sess == nil {
			return ""
		}
		return a.sess.Type
	}
	a.schedEndBreak()
	waitFor(t, "cb-a 接替成一扇新窗", 3*time.Second, func() bool { return nextKind() == "cb-a" })
	a.schedEndBreak()
	waitFor(t, "cb-b 接替成一扇新窗", 3*time.Second, func() bool { return nextKind() == "cb-b" })
	a.schedEndBreak()
	waitFor(t, "最后一次提前结束把窗收掉", 3*time.Second, func() bool {
		return a.sched.CurrentPhase() != "break"
	})
	a.sched.Stop()

	out := buf.String()
	// 统计：短休息记“提前结束”（不是完成），两条自定义各记 1 次 —— 接力成窗的那种
	// 由 OnBreakStart/OnBreakEnd 记账，不能再被“并入”那条路重复记一次。
	d := a.statsStore().Today()
	if d.EndEarlyCount != 1 || d.ShortCount != 0 {
		t.Fatalf("短休息应记提前结束 1 次、完成 0 次，实际 endEarly=%d short=%d",
			d.EndEarlyCount, d.ShortCount)
	}
	if d.CustomCounts["cb-a"] != 1 || d.CustomCounts["cb-b"] != 1 {
		t.Fatalf("两条自定义提醒应各记 1 次（不能因为先并入过就多记），实际 %v", d.CustomCounts)
	}
	if d.CustomCount != 2 {
		t.Fatalf("自定义提醒总数应为 2，实际 %d", d.CustomCount)
	}
	// 提示音：短休息开始一声 + 最后收尾一声；两次换窗整段静音
	if n := strings.Count(out, "[sound] 播放"); n != 2 {
		t.Fatalf("整场撞车应只响 2 声（开始 + 收尾），实际 %d：\n%s", n, out)
	}
}
