package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 状态格在暂停态下的四条口径（2026-11-23 全量审计补齐）。
//
// 这些是纯渲染层的事：Go 侧的状态机不变量（Paused ⇔ 原因非空、到期时刻归属 manual）
// 已经在 combo_matrix_test.go 里钉住了，剩下最容易漏的一层是“状态怎么被说给用户听”：
//
//   - 七条自动原因 + manual 在格子里只给一个结论「暂停休息」，具体原因挂 title；
//   - 定时暂停多一句「到 HH:MM 自动结束」—— 不给的话用户过一会儿就忘了自己设过时长；
//   - 暂停中**不冻钟**：唯一保留冻结的是“正在进行、且它自己选的是推迟”的那次休息
//     （`!st.breakWhilePaused`）；漏了这个否定号，配“正常提醒”的休息会在计时上被冻住，
//     BreakWhilePaused 那套“按真实时钟走完”就白做了；
//   - 侧栏那颗按钮的文案跟着状态走（暂停休息 / 恢复休息），否则点下去与预期相反。
//
// 界面层的等价断言在 scripts/probe/probe_renderer.py（真页面 + 真状态），这里是一层
// 不依赖界面就能跑的红线 —— 改文案或改判据忘了同步，`go test` 立刻红。
func TestStatusCellPausedWording(t *testing.T) {
	p := filepath.Join("web", "dist", "settings.js")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("读 %s 失败：%v", p, err)
	}
	src := string(b)

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
		// 暂停统一一个结论，原因不上格子
		`st.paused ? "暂停休息" : phaseText`,
		// 定时暂停的“到点自动结束”，只在真定时（deadline > 0）时才加
		`st.paused && st.pauseDeadline > 0`,
		"自动结束",
		// 原因与到期时刻都只挂 title
		"暂停原因：",
		// 暂停不冻钟：只有“正在进行 + 不是暂停中发起的 + 自己选了推迟”那次才冻
		`st.paused && st.phase === "break" && !st.breakWhilePaused`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("statusCells 里少了 %q —— 暂停态的口径被改动了（见本用例注释里的四条）", want)
		}
	}

	// 侧栏那颗按钮必须自带两种文案：写死「暂停休息」的话，暂停中再点一次会变得无法预期
	if !strings.Contains(src, `st.paused ? "恢复休息" : "暂停休息"`) {
		t.Errorf("%s 里侧栏暂停按钮的文案不再跟着状态走（应为 st.paused ? \"恢复休息\" : \"暂停休息\"）", p)
	}
}
