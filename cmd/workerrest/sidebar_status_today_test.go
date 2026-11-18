package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 侧栏状态格只报**当天**的下一次（2026-11-18 用户第三次定稿）。
//
// 这条口径被改回去过两次 —— 一次报“明天 00:10”，一次报“周四 09:00”，每次都在真机上
// 被投诉“侧栏显示了第二天的提醒”。根因不是算法，是有人觉得“跨天的那次也应该看得见”，
// 于是把跨天时刻又写回了 remainCell。所以这里除了钉行为，还钉住 statusCells 的源码形态：
//
//	· 短 / 长休息、外卖 / 自定义两张卡的下一次都必须过 isToday 守卫；
//	· 一旦有人重新引入“明天 HH:MM / 周X HH:MM”的写法（nextSlotDayLabel 那一类），直接报错。
//
// 行为层的等价断言在真机探针里（scripts/probe/settings_ui_probe.js 扫状态格），这里是
// 一层不依赖界面就能跑的红线 —— 改了文案忘了守卫，`go test` 就立刻红。
func TestSidebarStatusCellsTodayOnly(t *testing.T) {
	p := filepath.Join("web", "dist", "settings.js")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("读 %s 失败：%v", p, err)
	}
	src := string(b)

	// 定位 statusCells 函数体：从函数头到下一个四空格缩进的收尾括号。
	start := strings.Index(src, "function statusCells(st, now) {")
	if start < 0 {
		t.Fatalf("%s 里找不到 statusCells —— 侧栏状态格的整体刷新入口没了", p)
	}
	end := strings.Index(src[start:], "\n  }\n")
	if end < 0 {
		t.Fatalf("statusCells 函数体没能闭合，没法做源码契约断言")
	}
	body := src[start : start+end]

	for _, want := range []string{
		"const isToday = (at) => dayDiffOf(at) === 0;", // 当天判据只有一处定义
		"st.other && isToday(st.other.at)",             // 外卖 / 自定义那格
		": isToday(st.rest.at)",                        // 短 / 长休息那格
	} {
		if !strings.Contains(body, want) {
			t.Errorf("statusCells 里少了 %q —— 跨天的“下一次”就会漏到侧栏里（明天 00:10 / 周四 09:00）", want)
		}
	}
	if strings.Contains(src, "nextSlotDayLabel") {
		t.Errorf("%s 又出现了 nextSlotDayLabel：那套写法会把“明天 / 周X + 时刻”印到侧栏状态格；"+
			"跨天的下一次只留给卡片摘要与今日时间线", p)
	}
	// 休息格不能退回“只判有没有排上”的老写法（st.rest ? [...]）：那样跨天的那次
	// 会无条件渲染出来，等于又把明天的时刻放回侧栏。
	if strings.Contains(body, "st.rest ? [") {
		t.Errorf("statusCells 里下次休息那格又只判 st.rest 就渲染 —— 得先过 isToday(st.rest.at)，明天的那次整格不出现")
	}
}
