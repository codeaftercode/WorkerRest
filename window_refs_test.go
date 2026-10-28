package main

import (
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// 窗口引用的几条不变量。这里全是「无窗口也能验」的那部分：
// Wails 的窗口事件在无头构建里不存在，但**引用记账**的逻辑是纯 Go，可以直接测。

// dropWindowRef 只删自己那一扇。
//
// 这是「野窗」最隐蔽的成因：关窗事件在 Wails 里异步分发（每个监听器一个 goroutine），
// 「关旧窗 → 立刻建同名新窗」时迟到的旧回调若按名字 delete，抹掉的是新窗的引用 ——
// 那扇新窗再也关不掉。
func TestDropWindowRefOnlyRemovesOwnEntry(t *testing.T) {
	oldWin := new(application.WebviewWindow)
	newWin := new(application.WebviewWindow)

	// 情形一：登记的就是它 → 删除
	m := map[string]*application.WebviewWindow{"prebreak": oldWin}
	dropWindowRef(m, "prebreak", oldWin)
	if _, ok := m["prebreak"]; ok {
		t.Fatal("自己的引用应被删除")
	}

	// 情形二：同名但已换成新窗 → 迟到的旧回调不许动
	m = map[string]*application.WebviewWindow{"prebreak": newWin}
	dropWindowRef(m, "prebreak", oldWin)
	if got := m["prebreak"]; got != newWin {
		t.Fatalf("迟到的旧窗回调把新窗引用删了（这是关不掉的野窗的成因）")
	}

	// 情形三：map 里没有这一项 / map 为 nil → 静默无操作
	m = map[string]*application.WebviewWindow{}
	dropWindowRef(m, "prebreak", oldWin)
	var nilMap map[string]*application.WebviewWindow
	dropWindowRef(nilMap, "prebreak", oldWin)
}

// windowsLive 在无头（a.app == nil）下一律 false：宁可重建，也不要「复活」一个
// 已销毁的窗口（Wails 的 Show() 会对已销毁窗口重新 Run，等于原地造出一扇僵尸窗）。
func TestWindowsLiveHeadlessIsFalse(t *testing.T) {
	a, _ := newPreBreakApp(t, customWaterCfg)
	if a.app != nil {
		t.Skip("这条只在无头构造下有意义")
	}
	if a.windowLive(new(application.WebviewWindow)) {
		t.Fatal("没有 application 时 windowLive 必须是 false")
	}
	if a.windowsLive([]*application.WebviewWindow{new(application.WebviewWindow)}) {
		t.Fatal("没有 application 时 windowsLive 必须是 false")
	}
}

// 预览关窗走统一收尾：会话清空、窗口集合清空、不 panic（静默，不播音）。
func TestCloseWindowBreakPathUnified(t *testing.T) {
	a, _ := newPreBreakApp(t, customWaterCfg)
	id := a.showBreakSession("short", true, "")
	if id == "" {
		t.Fatal("预览会话没建起来")
	}

	a.CloseWindow("break")

	a.mu.Lock()
	sess := a.sess
	live := len(a.breakWins)
	n := len(a.breakWinSess)
	a.mu.Unlock()
	if sess != nil {
		t.Fatalf("预览关窗后会话应清空，实际 %+v", sess)
	}
	if live != 0 || n != 0 {
		t.Fatalf("窗口与归属表都应清空，实际 wins=%d sess=%d", live, n)
	}
	if a.supActivityWatchInfo().Running {
		t.Fatal("没有提醒在场时全局键鼠轮询不该还在跑")
	}
}

// 并发弹提前提示窗不许交叉建窗：两路来源（调度器 / 外卖预通知）本来就是两个 goroutine。
// 无头下不建窗，但「加锁 → 关旧 → 建新」这条临界区必须能并发跑而不打架。
func TestShowPreBreakWindowConcurrent(t *testing.T) {
	a, _ := newPreBreakApp(t, customWaterCfg)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				a.ShowPreBreakWindow(a.preBreakInfoOfType("short"))
			} else {
				a.showTakeoutPreNotify(TimeSlot{ID: "t", Hour: 12, Minute: 0, Enabled: true}, 0)
			}
		}(i)
	}
	wg.Wait()
	a.closePreBreakWindow()
}

// 源码钉子：休息窗口复用前必须确认窗口还活着（否则 Show() 会把已销毁窗口重新 Run）。
func TestBreakWindowReuseChecksLiveness(t *testing.T) {
	b, err := os.ReadFile("app.go")
	if err != nil {
		t.Fatalf("读 app.go 失败：%v", err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.Contains(line, "breakWinReusable(") || strings.HasPrefix(strings.TrimSpace(line), "func ") {
			continue
		}
		if !strings.Contains(line, "windowsLive(") {
			t.Errorf("复用判断缺少存活检查：%q（已销毁窗口调 Show() 会被 Wails 当成重新 Run）", strings.TrimSpace(line))
		}
		return
	}
	t.Fatal("没找到 breakWinReusable 的调用点 —— 测试失效，请更新")
}

// rectVisibleOnAny 决定「这扇窗还在不在能用眼睛看、用手点的世界里」。
// 拔掉外接屏后，原本建在那块屏上的提醒窗坐标落在不存在的区域，必须能被识别出来。
func TestRectVisibleOnAny(t *testing.T) {
	primary := &application.Screen{IsPrimary: true, WorkArea: application.Rect{X: 0, Y: 0, Width: 1920, Height: 1040}}
	left := &application.Screen{WorkArea: application.Rect{X: -1920, Y: 0, Width: 1920, Height: 1040}}
	screens := []*application.Screen{primary, left}

	cases := []struct {
		name string
		r    application.Rect
		want bool
	}{
		{"主屏正中", application.Rect{X: 760, Y: 420, Width: 400, Height: 240}, true},
		{"主屏右下角贴边", application.Rect{X: 1520, Y: 800, Width: 400, Height: 240}, true},
		{"副屏（负坐标）", application.Rect{X: -1920, Y: 0, Width: 800, Height: 600}, true},
		{"跨两块屏的接缝", application.Rect{X: -200, Y: 100, Width: 400, Height: 240}, true},
		{"跑到右侧屏外", application.Rect{X: 1920, Y: 0, Width: 400, Height: 240}, false},
		{"跑到上方屏外", application.Rect{X: 100, Y: -400, Width: 400, Height: 240}, false},
		{"副屏靠右下", application.Rect{X: -1000, Y: 500, Width: 400, Height: 240}, true},
	}
	for _, c := range cases {
		if got := rectVisibleOnAny(c.r, screens); got != c.want {
			t.Errorf("%s：rectVisibleOnAny=%v，期望 %v（rect=%+v）", c.name, got, c.want, c.r)
		}
	}

	// 只剩主屏时，原本在副屏的窗口必须被判成「看不见」
	if rectVisibleOnAny(application.Rect{X: -1920, Y: 0, Width: 800, Height: 600}, screens[:1]) {
		t.Error("副屏拔掉后，留在负坐标的窗口应判为不可见（这正是要纠偏的那类）")
	}
	// 没有显示器时不要纠偏（调用方已提前 return，这里钉住纯函数语义）
	if rectVisibleOnAny(application.Rect{X: 0, Y: 0, Width: 10, Height: 10}, nil) {
		t.Error("没有屏幕信息时不该断言可见")
	}
}

// 「提前结束」在调度器已经翻篇（自然到点 / 状态已翻篇）时必须还能兜住残留窗口：
// 无头下建不了窗口，至少钉住它不会崩、也不会顺手把外卖会话吃掉。
func TestSchedEndBreakOutsideBreakDoesNotTouchTakeout(t *testing.T) {
	a, sched := newPreBreakApp(t, customWaterCfg)
	if sched.stateCopy().Phase == "break" {
		t.Skip("构造出来的调度器正好在休息阶段，换个用例")
	}
	a.mu.Lock()
	a.takeoutCurrent = &TakeoutInfo{ID: "keep", Preview: false}
	a.mu.Unlock()

	a.schedEndBreak()

	a.mu.Lock()
	cur := a.takeoutCurrent
	a.mu.Unlock()
	if cur == nil || cur.ID != "keep" {
		t.Fatalf("非休息阶段的 endBreak 不该动外卖会话，实际 %+v", cur)
	}
}
