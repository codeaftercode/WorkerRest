package main

import (
	"net/url"
	"strings"
	"testing"
	"workerrest/internal/model"
	"workerrest/internal/plan"
	"workerrest/internal/scheduler"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// 提前提示窗（历史源码/windows/preBreakWindow.ts 的移植）的回归。
//
// 分工：“窗口真的弹出来了”只有 probe_gui_boot.py（拉起 GUI 产物读日志）与
// scripts/smoke_server.py（无头下走绑定）能验；这里钉的是四件纯逻辑的事：
//
//  一、版式内容 —— 内置短 / 长用劝说文案，自定义提醒用“XX时间到”，
//     后缀去重规则（“喝水时间”不再补一次“时间”、“喝水提醒”不再补一次“提醒”）；
//  二、窗口几何 —— 右下角定位、两档高度、副屏 / 负坐标工作区；
//  三、状态快照的**字段集** —— 渲染层每 500ms 轮询 PreBreakState，少一个字段
//     不会报错，只会静默显示错内容（这个坑在“占位返回形状”上踩过一次）；
//  四、四个动作真的汇聚到调度器，未知动作要报错而不是静默无事。

// customWaterCfg 带一条自定义提醒（喝水提醒）的配置，够 preBreakInfoOfType 取名用
const customWaterCfg = `{"short":{"enabled":true,"mode":"countdown","intervalMs":600000,` +
	`"durationMs":20000,"notifyBeforeMs":10000,"postponeMs":120000,"slots":[]},` +
	`"customBreaks":[{"id":"cb-water","name":"喝水提醒","enabled":true,` +
	`"config":{"enabled":true,"mode":"countdown","intervalMs":600000,"durationMs":20000,` +
	`"notifyBeforeMs":60000,"postponeMs":120000,"slots":[]}}]}`

// newPreBreakApp 造一个“配置 + 调度器”的最小 App（不碰窗口 / 托盘 / Wails）
func newPreBreakApp(t *testing.T, cfgJSON string) (*App, *scheduler.Scheduler) {
	t.Helper()
	s, host := newTestSched(t, cfgJSON)
	s.ResetCycle()
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
		// 标题按长短休息区分：short→“建议您短休息”、long→“建议您长休息”
		wantTitle := builtinPreBreakTitleShort
		if kind == "long" {
			wantTitle = builtinPreBreakTitleLong
		}
		if got.Title != wantTitle {
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
		t.Fatal("自定义提醒应走自定义版式（无两行说明、按钮改“…提醒”）")
	}
	if got.Title != "喝水提醒时间到" {
		t.Fatalf("标题应是“喝水提醒时间到”，得到 %q", got.Title)
	}
	if got.CountdownTail != " 秒显示喝水提醒" {
		t.Fatalf("倒计时后缀应是“ 秒显示喝水提醒”，得到 %q", got.CountdownTail)
	}
}

// 后缀去重：名称已经以“时间 / 提醒”结尾时不再补一次（叠字很难看）
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
		if got := plan.PreBreakTitle(c.name); got != c.title {
			t.Errorf("plan.PreBreakTitle(%q) = %q，期望 %q", c.name, got, c.title)
		}
		if got := plan.PreBreakCountdownTail(c.name); got != c.tail {
			t.Errorf("plan.PreBreakCountdownTail(%q) = %q，期望 %q", c.name, got, c.tail)
		}
	}
}

// 不认识的身份（提醒刚被删掉）回落成“休息”，不能让标题空着
func TestPreBreakInfoOfTypeUnknownFallsBack(t *testing.T) {
	a, _ := newPreBreakApp(t, customWaterCfg)
	got := a.preBreakInfoOfType("cb-gone")
	if !got.Custom || got.Title != "休息时间到" {
		t.Fatalf("已删除的自定义提醒应回落成“休息时间到”，得到 %+v", got)
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

// 休息窗口按目标屏工作区计算：多屏下每块屏各算各的，不能用主屏尺寸（会跑到屏幕外）
func TestBreakBoundsFor(t *testing.T) {
	cfg := model.AppearanceConfig{WidthPct: 50, HeightPct: 50}

	// 主屏 1920x1040（任务栏已扣除）→ 半屏居中
	wa := application.Rect{X: 0, Y: 0, Width: 1920, Height: 1040}
	x, y, w, h := reminderBoundsFor(wa, cfg)
	if w != 960 || h != 520 {
		t.Fatalf("应为主屏工作区的 50%%，得到 %dx%d", w, h)
	}
	if x != 480 || y != 260 {
		t.Fatalf("应在该屏工作区内居中，得到 (%d,%d)", x, y)
	}

	// 副屏（非零原点）也要相对**那块屏**居中
	wa2 := application.Rect{X: 1920, Y: -200, Width: 2560, Height: 1400}
	x, y, w, h = reminderBoundsFor(wa2, cfg)
	if w != 1280 || h != 700 {
		t.Fatalf("副屏应为该屏工作区的 50%%，得到 %dx%d", w, h)
	}
	if x != 1920+640 || y != -200+350 {
		t.Fatalf("副屏应相对该屏工作区居中，得到 (%d,%d)", x, y)
	}

	// 全屏：铺满工作区；最小尺寸兜底
	if x, y, w, h = reminderBoundsFor(wa, model.AppearanceConfig{Fullscreen: true}); x != 0 || y != 0 || w != 1920 || h != 1040 {
		t.Fatalf("全屏应铺满工作区，得到 %dx%d @(%d,%d)", w, h, x, y)
	}
	small := application.Rect{X: 0, Y: 0, Width: 400, Height: 300}
	x, y, w, h = reminderBoundsFor(small, cfg)
	if w != 320 || h != 240 {
		t.Fatalf("过小工作区应触发最小尺寸兜底，得到 %dx%d", w, h)
	}
	// 兜底之后位置必须按**夹紧后的**尺寸重算，否则窗口从居中偏出去
	if x != 40 || y != 30 {
		t.Fatalf("兜底后仍应居中（(400-320)/2, (300-240)/2），得到 (%d,%d)", x, y)
	}

	// 1280 的屏、20% 宽 = 256 < 320：夹到 320 后仍要居中（曾右偏 32px）
	narrow := application.Rect{X: 0, Y: 0, Width: 1280, Height: 720}
	x, y, w, h = reminderBoundsFor(narrow, model.AppearanceConfig{WidthPct: 20, HeightPct: 20})
	if w != 320 || h != 240 {
		t.Fatalf("1280×720 的 20%% 应夹到 320×240，得到 %dx%d", w, h)
	}
	if x != (1280-320)/2 || y != (720-240)/2 {
		t.Fatalf("夹紧后应居中于该屏，得到 (%d,%d)", x, y)
	}
}

// 扩展模式下“主屏 + 投影仪”是两块屏、两扇窗：每扇必须完整落在自己那块屏里，
// 且两扇互不交叠。**不能把两块屏当成一个整体弹一扇横跨两屏的大窗** —— 那样窗口
// 中点会落在两屏缝合处，投影仪上只看到半张立绘，比不弹更糟。
//
// 防的是将来有人图省事，用 GetAll 的并集矩形去建窗（或把 targetDisplays 的结果
// 合并成一个"虚拟桌面"）。ShowBreakWindow 现在是“循环每块屏、各调一次本函数”，
// 这条断言把这个前提固化成可执行的约束。
func TestBreakWindowsStayInsideTheirOwnDisplay(t *testing.T) {
	// 主屏 2560×1440 @(0,0)，右边接一块 1920×1080 的投影屏。
	// 副屏给个负 Y 起点：非零/负原点最容易暴露“按并集算”的写法。
	mainScr := application.Rect{X: 0, Y: 0, Width: 2560, Height: 1440}
	projScr := application.Rect{X: 2560, Y: -209, Width: 1920, Height: 1080}

	for _, cfg := range []model.AppearanceConfig{
		{Fullscreen: true, WidthPct: 95, HeightPct: 95}, // 全屏铺满
		{WidthPct: 95, HeightPct: 95},                   // 出厂默认
		{WidthPct: 50, HeightPct: 50},                   // 半屏
	} {
		rects := [][4]int{boundsOf(mainScr, cfg), boundsOf(projScr, cfg)}
		for i, wa := range []application.Rect{mainScr, projScr} {
			x, y, w, h := rects[i][0], rects[i][1], rects[i][2], rects[i][3]
			if x < wa.X || y < wa.Y || x+w > wa.X+wa.Width || y+h > wa.Y+wa.Height {
				t.Fatalf("%+v：第 %d 块屏的窗口越界 (%d,%d) %dx%d，屏为 %+v",
					cfg, i+1, x, y, w, h, wa)
			}
		}
		if rectsOverlap(rects[0], rects[1]) {
			t.Fatalf("%+v：两扇窗交叠了 %v / %v —— 说明几何被按并集算了",
				cfg, rects[0], rects[1])
		}
	}
}

// boundsOf 把 reminderBoundsFor 的四个返回值收成 (x,y,w,h)，方便按矩形集合断言。
func boundsOf(wa application.Rect, cfg model.AppearanceConfig) [4]int {
	x, y, w, h := reminderBoundsFor(wa, cfg)
	return [4]int{x, y, w, h}
}

// rectsOverlap 两个 (x,y,w,h) 矩形是否相交（边贴边不算）。
func rectsOverlap(a, b [4]int) bool {
	return a[0] < b[0]+b[2] && b[0] < a[0]+a[2] &&
		a[1] < b[1]+b[3] && b[1] < a[1]+a[3]
}

// 地址里带版式：渲染层第一帧就按它铺，否则会先画内置版式再跳一次（窗口高度按版式给）
func TestPreBreakURL(t *testing.T) {
	builtin := preBreakURL(PreBreakInfo{Custom: false, CountdownTail: builtinPreBreakTail, PostponeMin: 2})
	parsed, err := url.Parse(builtin)
	if err != nil {
		t.Fatalf("地址解析失败: %v", err)
	}
	if parsed.Path != "/prebreak.html" {
		t.Fatalf("路径应是 /prebreak.html，得到 %q", parsed.Path)
	}
	q := parsed.Query()
	if q.Get("custom") != "0" || q.Get("tail") != builtinPreBreakTail ||
		q.Get("postpone") != "1" || q.Get("win") != "prebreak" {
		t.Fatalf("内置版式地址参数不对：%q", builtin)
	}

	// 不可推迟（PostponeMin=0）时带 postpone=0，渲染层首帧就藏掉“推迟”按钮
	custom := preBreakURL(PreBreakInfo{Custom: true, CountdownTail: " 秒显示喝水提醒", PostponeMin: 0})
	parsed2, err := url.Parse(custom)
	if err != nil {
		t.Fatalf("地址解析失败: %v", err)
	}
	q2 := parsed2.Query()
	if q2.Get("custom") != "1" || q2.Get("tail") != " 秒显示喝水提醒" ||
		q2.Get("postpone") != "0" || q2.Get("win") != "prebreak" {
		t.Fatalf("自定义版式（不可推迟）地址参数不对：%q", custom)
	}
}

// “可推迟”为 0：弹窗内容、地址、轮询状态三处都不给“推迟”按钮活路
// （提前提示窗 / 外卖窗口 / 设置页 quick-action 三个入口共用这一语义）
func TestPreBreakPostponeZero(t *testing.T) {
	zeroCfg := `{"short":{"enabled":true,"mode":"countdown","intervalMs":600000,` +
		`"durationMs":20000,"notifyBeforeMs":10000,"postponeMs":0,"slots":[]}}`
	a, _ := newPreBreakApp(t, zeroCfg)

	info := a.preBreakInfoOfType("short")
	if info.PostponeMin != 0 {
		t.Fatalf("postponeMs=0 时 PostponeMin 应是 0，得到 %d", info.PostponeMin)
	}
	u := preBreakURL(info)
	parsed, err := url.Parse(u)
	if err != nil {
		t.Fatalf("地址解析失败: %v", err)
	}
	if parsed.Query().Get("postpone") != "0" {
		t.Fatalf("不可推迟时地址应带 postpone=0，得到 %q", u)
	}
	a.rememberPreBreakInfo("short")
	if st := a.PreBreakState(); st["canPostpone"] != false {
		t.Fatalf("不可推迟时 canPostpone 应是 false，得到 %#v", st["canPostpone"])
	}

	// 对照：>0 时三处都放行（shortCountdown 的 postponeMs=120000 ⇒ 2 分钟）
	a2, _ := newPreBreakApp(t, shortCountdown)
	if got := a2.preBreakInfoOfType("short").PostponeMin; got != 2 {
		t.Fatalf("shortCountdown 的 PostponeMin 应是 2，得到 %d", got)
	}
	a2.rememberPreBreakInfo("short")
	if st := a2.PreBreakState(); st["canPostpone"] != true {
		t.Fatalf("可推迟时 canPostpone 应是 true，得到 %#v", st["canPostpone"])
	}

	// 自定义提醒同样按自己的配置走（cb-water 的 postponeMs=120000 ⇒ 2 分钟）
	a3, _ := newPreBreakApp(t, customWaterCfg)
	if got := a3.preBreakInfoOfType("cb-water").PostponeMin; got != 2 {
		t.Fatalf("cb-water 的 PostponeMin 应是 2，得到 %d", got)
	}
}

// ---------- 状态快照 ----------

func TestPreBreakStateShape(t *testing.T) {
	a, _ := newPreBreakApp(t, customWaterCfg)
	st := a.PreBreakState()
	for _, k := range []string{"phase", "paused", "remainMs", "title", "custom", "countdownTail", "canPostpone", "bgColor"} {
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
	if st["title"] != builtinPreBreakTitleShort || st["countdownTail"] != builtinPreBreakTail || st["custom"] != false {
		t.Fatalf("未弹过窗时应兜底成内置版式，得到 %v", st)
	}
}

// 内容“算一次就记住”：弹窗打开后读到的标题不随计划漂移（与 上一版的模块级 info 同口径）
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
	// 下一次弹出换回内置提醒：记忆要跟着换（不是“第一次是什么就永远是什么”）
	a.rememberPreBreakInfo("short")
	st = a.PreBreakState()
	if st["title"] != builtinPreBreakTitleShort || st["custom"] != false {
		t.Fatalf("再次记住后应变成内置版式，得到 %v", st)
	}
}

// ---------- 背景颜色（跟随“通用配置 → 提醒窗口 → 背景颜色”） ----------

// bgColorCfg 把提醒窗口背景色调成亮黄（浅底）：用来钉“小窗底色跟着配置走”这条链路
const bgColorCfg = `{"appearance":{"color":"#ffd400"},` +
	`"short":{"enabled":true,"mode":"countdown","intervalMs":600000,"durationMs":20000,` +
	`"notifyBeforeMs":10000,"postponeMs":120000,"slots":[]}}`

// 小窗底色 = appearance.color（不是写死的深色）：弹窗内容、URL（首帧）、轮询状态三处同源。
// 渲染层的“浅底配深字”由 prebreak.js 按这个颜色算，探针里断言，不在 Go 侧。
func TestPreBreakBgColorFollowsAppearance(t *testing.T) {
	a, _ := newPreBreakApp(t, bgColorCfg)
	info := a.preBreakInfoOfType("short")
	if info.BgColor != "#ffd400" {
		t.Fatalf("BgColor 应取 appearance.color，得到 %q", info.BgColor)
	}
	// URL 带 bg（# 必须编码成 %23，否则被 WebView2 当锚点吃掉）
	u := preBreakURL(info)
	parsed, err := url.Parse(u)
	if err != nil {
		t.Fatalf("地址解析失败: %v", err)
	}
	if got := parsed.Query().Get("bg"); got != "#ffd400" {
		t.Fatalf("地址应带 bg=#ffd400（编码后），得到 %q（完整 %q）", got, u)
	}
	if !strings.Contains(u, "bg=%23ffd400") {
		t.Fatalf("bg 的 # 必须编码成 %%23，得到 %q", u)
	}
	// 轮询状态同源
	a.rememberPreBreakInfo("short")
	if st := a.PreBreakState(); st["bgColor"] != "#ffd400" {
		t.Fatalf("PreBreakState 的 bgColor 应同源，得到 %#v", st["bgColor"])
	}
	// 没带 BgColor 的 info（旧窗口 / 手工构造）回退到当前配置，不留空
	a.mu.Lock()
	a.preInfo = PreBreakInfo{Custom: false, Title: builtinPreBreakTitleShort}
	a.mu.Unlock()
	if st := a.PreBreakState(); st["bgColor"] != "#ffd400" {
		t.Fatalf("info 没记颜色时应回退到当前配置，得到 %#v", st["bgColor"])
	}
	// 地址里没有颜色时干脆不带这个参数（渲染层用内置默认蓝），别传一个空的 bg=
	if strings.Contains(preBreakURL(PreBreakInfo{PostponeMin: 1}), "bg=") {
		t.Fatal("BgColor 为空时地址不该带 bg 参数")
	}
}

// ---------- 动作 ----------

func TestPreBreakActionPostponeAndSkip(t *testing.T) {
	a, s := newPreBreakApp(t, shortCountdown)
	before := s.StateCopy().NextAt

	if err := a.PreBreakAction("postpone"); err != nil {
		t.Fatalf("postpone 不该报错：%v", err)
	}
	after := s.StateCopy()
	if after.Phase != "idle" {
		t.Fatalf("推迟后应回到 idle，得到 %q", after.Phase)
	}
	// 推迟 = 在原定时刻上往后推（不是“从现在起算”）：提前提示窗弹出时原定时刻还在
	// 未来，若写 now+postponeMs 会把提醒提前到现在（P2-1，实测 1798s→299s）。
	want := before + 120_000 // shortCountdown 的 postponeMs
	if diff := after.NextAt - want; diff < -2000 || diff > 2000 {
		t.Fatalf("推迟应在原定时刻上+postponeMs，want=%d got=%d（原值 %d）", want, after.NextAt, before)
	}
	if after.NextAt <= before {
		t.Fatalf("推迟不该把提醒提前：before=%d after=%d", before, after.NextAt)
	}

	if err := a.PreBreakAction("skip"); err != nil {
		t.Fatalf("skip 不该报错：%v", err)
	}
	skipped := s.StateCopy()
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
	if got := s.StateCopy().Phase; got != "break" {
		t.Fatalf("立即休息后 phase 应是 break，得到 %q", got)
	}
}

func TestPreBreakActionPauseToggles(t *testing.T) {
	a, s := newPreBreakApp(t, shortCountdown)
	if err := a.PreBreakAction("pause"); err != nil {
		t.Fatalf("pause 不该报错：%v", err)
	}
	st := s.StateCopy()
	if !st.Paused || len(st.PauseReasons) != 1 || st.PauseReasons[0] != "manual" {
		t.Fatalf("pause 应等价于托盘暂停，得到 paused=%v reasons=%v", st.Paused, st.PauseReasons)
	}
	if err := a.PreBreakAction("pause"); err != nil {
		t.Fatalf("再次 pause（恢复）不该报错：%v", err)
	}
	if s.StateCopy().Paused {
		t.Fatal("再次 pause 应恢复（与托盘暂停同一入口）")
	}
}

// 未知动作必须报错：静默无事是这类“按名调用”链路最容易漏的坑
func TestPreBreakActionUnknownErrors(t *testing.T) {
	a, s := newPreBreakApp(t, shortCountdown)
	before := s.StateCopy()
	err := a.PreBreakAction("fly")
	if err == nil {
		t.Fatal("未知动作应报错，而不是静默无事")
	}
	after := s.StateCopy()
	if after.Phase != before.Phase || after.Paused != before.Paused || after.NextAt != before.NextAt {
		t.Fatalf("未知动作不该改动调度器状态：%+v -> %+v", before, after)
	}
	// 没窗时关窗是空操作（不能因为 wins 里没有这一项就崩）
	a.closePreBreakWindow()
}

// 注：ShowPreBreakWindow 本身不在这里测 —— 它会去碰 Wails 的窗口管理器，
// 而 go test 默认**不带** -tags server（serverMode=false），单测里没有 application.App。
// “真的弹出来了”由 probe_gui_boot.py（GUI 产物 + 日志）与 smoke_server.py
// （无头下走 PreBreakState / PreBreakAction）覆盖。
