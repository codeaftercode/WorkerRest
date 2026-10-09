package main

import (
	"net/url"
	"testing"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// 提前提示窗（electron/src/windows/preBreakWindow.ts 的移植）的回归。
//
// 分工：「窗口真的弹出来了」只有 probe_gui_boot.py（拉起 GUI 产物读日志）与
// scripts/smoke_server.py（无头下走绑定）能验；这里钉的是四件纯逻辑的事：
//
//  一、版式内容 —— 内置短 / 长用劝说文案，自定义提醒用「XX时间到」，
//     后缀去重规则（「喝水时间」不再补一次「时间」、「喝水提醒」不再补一次「提醒」）；
//  二、窗口几何 —— 右下角定位、两档高度、副屏 / 负坐标工作区；
//  三、状态快照的**字段集** —— 渲染层每 500ms 轮询 PreBreakState，少一个字段
//     不会报错，只会静默显示错内容（这个坑在「占位返回形状」上踩过一次）；
//  四、四个动作真的汇聚到调度器，未知动作要报错而不是静默无事。

// customWaterCfg 带一条自定义提醒（喝水提醒）的配置，够 preBreakInfoOfType 取名用
const customWaterCfg = `{"short":{"enabled":true,"mode":"countdown","intervalMs":600000,` +
	`"durationMs":20000,"notifyBeforeMs":10000,"postponeMs":120000,"slots":[]},` +
	`"customBreaks":[{"id":"cb-water","name":"喝水提醒","enabled":true,` +
	`"config":{"enabled":true,"mode":"countdown","intervalMs":600000,"durationMs":20000,` +
	`"notifyBeforeMs":60000,"postponeMs":120000,"slots":[]}}]}`

// newPreBreakApp 造一个「配置 + 调度器」的最小 App（不碰窗口 / 托盘 / Wails）
func newPreBreakApp(t *testing.T, cfgJSON string) (*App, *Scheduler) {
	t.Helper()
	s, host := newTestSched(t, cfgJSON)
	s.replan()
	return &App{cfg: host.cfg, sched: s}, s
}

// ---------- 版式内容 ----------

func TestPreBreakInfoOfTypeBuiltin(t *testing.T) {
	a, _ := newPreBreakApp(t, customWaterCfg)
	for _, kind := range []string{"short", "long"} {
		got := a.preBreakInfoOfType(kind)
		if got.Custom {
			t.Fatalf("%s 应走内置版式（四按钮 + 两行说明），得到 custom=true", kind)
		}
		if got.Title != builtinPreBreakTitle {
			t.Fatalf("%s 的标题应是劝说文案，得到 %q", kind, got.Title)
		}
		if got.CountdownTail != builtinPreBreakTail {
			t.Fatalf("%s 的倒计时后缀应是 %q，得到 %q", kind, builtinPreBreakTail, got.CountdownTail)
		}
	}
}

func TestPreBreakInfoOfTypeCustom(t *testing.T) {
	a, _ := newPreBreakApp(t, customWaterCfg)
	got := a.preBreakInfoOfType("cb-water")
	if !got.Custom {
		t.Fatal("自定义提醒应走自定义版式（无两行说明、按钮改「…提醒」）")
	}
	if got.Title != "喝水提醒时间到" {
		t.Fatalf("标题应是「喝水提醒时间到」，得到 %q", got.Title)
	}
	if got.CountdownTail != " 秒显示喝水提醒" {
		t.Fatalf("倒计时后缀应是「 秒显示喝水提醒」，得到 %q", got.CountdownTail)
	}
}

// 后缀去重：名称已经以「时间 / 提醒」结尾时不再补一次（叠字很难看）
func TestPreBreakSuffixDedup(t *testing.T) {
	cases := []struct {
		name  string
		title string
		tail  string
	}{
		{"喝水", "喝水时间到", " 秒显示喝水提醒"},
		{"喝水时间", "喝水时间到", " 秒显示喝水时间提醒"},
		{"喝水提醒", "喝水提醒时间到", " 秒显示喝水提醒"},
		{"时间", "时间到", " 秒显示时间提醒"},
		{"提醒", "提醒时间到", " 秒显示提醒"},
	}
	for _, c := range cases {
		if got := preBreakTitle(c.name); got != c.title {
			t.Errorf("preBreakTitle(%q) = %q，期望 %q", c.name, got, c.title)
		}
		if got := preBreakCountdownTail(c.name); got != c.tail {
			t.Errorf("preBreakCountdownTail(%q) = %q，期望 %q", c.name, got, c.tail)
		}
	}
}

// 不认识的身份（提醒刚被删掉）回落成「休息」，不能让标题空着
func TestPreBreakInfoOfTypeUnknownFallsBack(t *testing.T) {
	a, _ := newPreBreakApp(t, customWaterCfg)
	got := a.preBreakInfoOfType("cb-gone")
	if !got.Custom || got.Title != "休息时间到" {
		t.Fatalf("已删除的自定义提醒应回落成「休息时间到」，得到 %+v", got)
	}
}

// ---------- 窗口几何 ----------

func TestPreBreakBounds(t *testing.T) {
	// 1920x1080 屏、任务栏 40 → 工作区 1920x1040
	wa := application.Rect{X: 0, Y: 0, Width: 1920, Height: 1040}

	x, y, w, h := preBreakBounds(wa, false)
	if w != 400 || h != 240 {
		t.Fatalf("内置版式应是 400x240，得到 %dx%d", w, h)
	}
	if x != 1920-400-24 || y != 1040-240-24 {
		t.Fatalf("内置版式应贴工作区右下角留 24px，得到 (%d,%d)", x, y)
	}

	x, y, w, h = preBreakBounds(wa, true)
	if w != 400 || h != 172 {
		t.Fatalf("自定义版式应是 400x172（少了那两行说明），得到 %dx%d", w, h)
	}
	if x != 1920-400-24 || y != 1040-172-24 {
		t.Fatalf("自定义版式也应贴右下角，得到 (%d,%d)", x, y)
	}
}

// 副屏（非零原点、负 Y）也要落在**那块屏**的右下角：多屏下用主屏尺寸算会跑到屏幕外
func TestPreBreakBoundsSecondScreen(t *testing.T) {
	wa := application.Rect{X: 1920, Y: -200, Width: 2560, Height: 1400}
	x, y, w, h := preBreakBounds(wa, false)
	if want := 1920 + 2560 - w - 24; x != want {
		t.Fatalf("X 应相对该屏工作区右缘，得到 %d 期望 %d", x, want)
	}
	if want := -200 + 1400 - h - 24; y != want {
		t.Fatalf("Y 应相对该屏工作区下缘（含负起点），得到 %d 期望 %d", y, want)
	}
}

// 地址里带版式：渲染层第一帧就按它铺，否则会先画内置版式再跳一次（窗口高度按版式给）
func TestPreBreakURL(t *testing.T) {
	const winSuffix = "&win=prebreak"
	builtin := preBreakURL(PreBreakInfo{Custom: false, CountdownTail: builtinPreBreakTail})
	if builtin != "/prebreak.html?custom=0&tail="+url.QueryEscape(builtinPreBreakTail)+winSuffix {
		t.Fatalf("内置版式地址不对：%q", builtin)
	}
	const prefix = "/prebreak.html?custom=1&tail="
	custom := preBreakURL(PreBreakInfo{Custom: true, CountdownTail: " 秒显示喝水提醒"})
	if len(custom) < len(prefix)+len(winSuffix) ||
		custom[:len(prefix)] != prefix ||
		custom[len(custom)-len(winSuffix):] != winSuffix {
		t.Fatalf("自定义版式地址不对：%q", custom)
	}
	// tail 里有中文与空格：必须能原样解回来（渲染层用 URLSearchParams 读它）
	back, err := url.QueryUnescape(custom[len(prefix) : len(custom)-len(winSuffix)])
	if err != nil {
		t.Fatalf("地址里的 tail 解不开：%v", err)
	}
	if back != " 秒显示喝水提醒" {
		t.Fatalf("tail 往返后变了：%q", back)
	}
}

// ---------- 状态快照 ----------

func TestPreBreakStateShape(t *testing.T) {
	a, _ := newPreBreakApp(t, customWaterCfg)
	st := a.PreBreakState()
	for _, k := range []string{"phase", "paused", "remainMs", "title", "custom", "countdownTail"} {
		if _, ok := st[k]; !ok {
			t.Fatalf("PreBreakState 少了字段 %q（渲染层直接读它，缺字段只会静默显示错内容）：%v", k, st)
		}
	}
	if st["phase"] != "idle" {
		t.Fatalf("刚 replan 完应是 idle，得到 %v", st["phase"])
	}
	if st["paused"] != false {
		t.Fatalf("paused 应是布尔 false，得到 %#v", st["paused"])
	}
	if ms, ok := st["remainMs"].(int64); !ok || ms <= 0 {
		t.Fatalf("remainMs 应是正的毫秒整数，得到 %#v", st["remainMs"])
	}
	// 还没弹过窗：兜底成内置版式（不能让标题 / 后缀空着）
	if st["title"] != builtinPreBreakTitle || st["countdownTail"] != builtinPreBreakTail || st["custom"] != false {
		t.Fatalf("未弹过窗时应兜底成内置版式，得到 %v", st)
	}
}

// 内容「算一次就记住」：弹窗打开后读到的标题不随计划漂移（与 Electron 的模块级 info 同口径）
func TestPreBreakInfoRemembered(t *testing.T) {
	a, _ := newPreBreakApp(t, customWaterCfg)
	got := a.rememberPreBreakInfo("cb-water")
	if !got.Custom || got.Title != "喝水提醒时间到" {
		t.Fatalf("记住的内容不对：%+v", got)
	}
	st := a.PreBreakState()
	if st["title"] != "喝水提醒时间到" || st["custom"] != true ||
		st["countdownTail"] != " 秒显示喝水提醒" {
		t.Fatalf("PreBreakState 应回放记住的内容，得到 %v", st)
	}
	// 下一次弹出换回内置提醒：记忆要跟着换（不是「第一次是什么就永远是什么」）
	a.rememberPreBreakInfo("short")
	st = a.PreBreakState()
	if st["title"] != builtinPreBreakTitle || st["custom"] != false {
		t.Fatalf("再次记住后应变成内置版式，得到 %v", st)
	}
}

// ---------- 动作 ----------

func TestPreBreakActionPostponeAndSkip(t *testing.T) {
	a, s := newPreBreakApp(t, shortCountdown)
	before := s.stateCopy().NextAt

	if err := a.PreBreakAction("postpone"); err != nil {
		t.Fatalf("postpone 不该报错：%v", err)
	}
	after := s.stateCopy()
	if after.Phase != "idle" {
		t.Fatalf("推迟后应回到 idle，得到 %q", after.Phase)
	}
	want := nowMs() + 120_000 // shortCountdown 的 postponeMs
	if diff := after.NextAt - want; diff < -2000 || diff > 2000 {
		t.Fatalf("推迟应重锚到「现在 + postponeMs」，want=%d got=%d（原值 %d）", want, after.NextAt, before)
	}

	if err := a.PreBreakAction("skip"); err != nil {
		t.Fatalf("skip 不该报错：%v", err)
	}
	skipped := s.stateCopy()
	if skipped.Phase != "idle" {
		t.Fatalf("跳过后应回到 idle，得到 %q", skipped.Phase)
	}
	if skipped.NextAt == after.NextAt {
		t.Fatalf("跳过应重排下一条，nextAt 不该原地不动（%d）", skipped.NextAt)
	}
}

func TestPreBreakActionBreakNow(t *testing.T) {
	a, s := newPreBreakApp(t, shortCountdown)
	if err := a.PreBreakAction("breakNow"); err != nil {
		t.Fatalf("breakNow 不该报错：%v", err)
	}
	if got := s.stateCopy().Phase; got != "break" {
		t.Fatalf("立即休息后 phase 应是 break，得到 %q", got)
	}
}

func TestPreBreakActionPauseToggles(t *testing.T) {
	a, s := newPreBreakApp(t, shortCountdown)
	if err := a.PreBreakAction("pause"); err != nil {
		t.Fatalf("pause 不该报错：%v", err)
	}
	st := s.stateCopy()
	if !st.Paused || len(st.PauseReasons) != 1 || st.PauseReasons[0] != "manual" {
		t.Fatalf("pause 应等价于托盘暂停，得到 paused=%v reasons=%v", st.Paused, st.PauseReasons)
	}
	if err := a.PreBreakAction("pause"); err != nil {
		t.Fatalf("再次 pause（恢复）不该报错：%v", err)
	}
	if s.stateCopy().Paused {
		t.Fatal("再次 pause 应恢复（与托盘暂停同一入口）")
	}
}

// 未知动作必须报错：静默无事是这类「按名调用」链路最容易漏的坑
func TestPreBreakActionUnknownErrors(t *testing.T) {
	a, s := newPreBreakApp(t, shortCountdown)
	before := s.stateCopy()
	err := a.PreBreakAction("fly")
	if err == nil {
		t.Fatal("未知动作应报错，而不是静默无事")
	}
	after := s.stateCopy()
	if after.Phase != before.Phase || after.Paused != before.Paused || after.NextAt != before.NextAt {
		t.Fatalf("未知动作不该改动调度器状态：%+v -> %+v", before, after)
	}
	// 没窗时关窗是空操作（不能因为 wins 里没有这一项就崩）
	a.closePreBreakWindow()
}

// 注：ShowPreBreakWindow 本身不在这里测 —— 它会去碰 Wails 的窗口管理器，
// 而 go test 默认**不带** -tags server（serverMode=false），单测里没有 application.App。
// 「真的弹出来了」由 probe_gui_boot.py（GUI 产物 + 日志）与 smoke_server.py
// （无头下走 PreBreakState / PreBreakAction）覆盖。
