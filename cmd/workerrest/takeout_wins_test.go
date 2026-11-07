package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"workerrest/internal/model"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// 外卖窗口集合的“替换”语义。
//
// 野窗事故：`showTakeoutWindowsAt` 原来直接 `a.takeoutWins = wins`，连点两下“预览”
// 时后一批把前一批的引用整批顶掉 —— 屏幕上那扇旧的再没有任何代码持有它，用户点
// “知道了”关的是新窗，旧窗谁也关不下。现在替换只走 `replaceTakeoutWins`，
// 由它把旧集合交还给调用方去 Close。这两条测试钉住“交还”与“唯一写入点”。

func TestReplaceTakeoutWinsHandsBackStale(t *testing.T) {
	a, _ := newPreBreakApp(t, customWaterCfg)

	// 零值窗口对象只用来做身份比较，全程不调用它的方法
	w1 := new(application.WebviewWindow)
	w2 := new(application.WebviewWindow)
	w3 := new(application.WebviewWindow)
	first := []*application.WebviewWindow{w1}
	second := []*application.WebviewWindow{w2, w3}
	third := []*application.WebviewWindow{w1}

	if old := a.replaceTakeoutWins(first, &model.TakeoutInfo{ID: "t1"}); len(old) != 0 {
		t.Fatalf("首次登记不该有旧窗口，实际交还 %d 个", len(old))
	}
	old := a.replaceTakeoutWins(second, &model.TakeoutInfo{ID: "t2"})
	if len(old) != 1 || old[0] != w1 {
		t.Fatalf("第二次登记必须把第一批交还（供调用方关闭），实际 %d 个", len(old))
	}
	old = a.replaceTakeoutWins(third, &model.TakeoutInfo{ID: "t3"})
	if len(old) != 2 || old[0] != w2 || old[1] != w3 {
		t.Fatalf("第三次登记必须交还第二批的 2 个窗口，实际 %d 个", len(old))
	}

	a.mu.Lock()
	cur := a.takeoutCurrent
	live := len(a.takeoutWins)
	a.mu.Unlock()
	if cur == nil || cur.ID != "t3" {
		t.Fatalf("会话应换成最后一次登记的 info，实际 %+v", cur)
	}
	if live != 1 {
		t.Fatalf("当前集合应是第三次那批（1 个），实际 %d 个", live)
	}

	// info 为 nil 时不动会话（建完新窗后登记那一步）
	a.replaceTakeoutWins(nil, nil)
	a.mu.Lock()
	cur = a.takeoutCurrent
	a.mu.Unlock()
	if cur == nil || cur.ID != "t3" {
		t.Fatalf("info 为 nil 时不该清会话，实际 %+v", cur)
	}
}

// 源码级钉子：外卖窗口集合的写入只允许三种形态，
// 直接赋整批（`= wins`）会重现野窗，必须走 replaceTakeoutWins。
func TestTakeoutWinsAssignedOnlyThroughReplace(t *testing.T) {
	files, err := filepath.Glob("takeout*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("找不到 takeout*.go：%v", err)
	}
	allowed := map[string]bool{
		"a.takeoutWins = next":                        true, // replaceTakeoutWins
		"a.takeoutWins = append(ws[:i], ws[i+1:]...)": true, // removeTakeoutWindow
		"a.takeoutWins = nil":                         true, // closeTakeoutWindows
	}
	seen := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("读 %s 失败：%v", name, err)
		}
		for _, line := range strings.Split(string(b), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") || !strings.Contains(trimmed, "a.takeoutWins = ") {
				continue
			}
			seen++
			if !allowed[trimmed] {
				t.Errorf("%s 出现了计划外的 a.takeoutWins 赋值：%q（整批替换请走 replaceTakeoutWins，"+
					"否则旧窗引用会丢、变成关不掉的野窗）", name, trimmed)
			}
		}
	}
	if seen != len(allowed) {
		t.Errorf("a.takeoutWins 赋值点数量变了（期望 %d 处，实际 %d 处）—— 新增写入点务必确认旧窗口仍会被关闭",
			len(allowed), seen)
	}
}

// 用户 Alt+F4 关掉最后一扇外卖窗：会话必须跟着结束。
// 不清的话 realTakeoutActive 永远为真（它按会话判定），设置页“预览”会被永久拒绝。
func TestRemoveTakeoutWindowClearsCurrentSession(t *testing.T) {
	a, _ := newPreBreakApp(t, customWaterCfg)

	w1 := new(application.WebviewWindow)
	info := &model.TakeoutInfo{ID: "real-1", Preview: false}
	a.replaceTakeoutWins([]*application.WebviewWindow{w1}, info)

	a.removeTakeoutWindow(w1)

	a.mu.Lock()
	cur := a.takeoutCurrent
	live := len(a.takeoutWins)
	a.mu.Unlock()
	if cur != nil {
		t.Fatalf("最后一扇窗关掉后会话应清空，实际 %+v", cur)
	}
	if live != 0 {
		t.Fatalf("窗口集合应清空，实际 %d 个", live)
	}
	if a.realTakeoutActive() {
		t.Fatal("会话清空后 realTakeoutActive 应为 false（否则预览被永久拒）")
	}
}

// 迟到的旧窗口回调不许清掉新会话：关掉的窗口属于上一次提醒时只摘它自己。
func TestRemoveTakeoutWindowIgnoresStaleSession(t *testing.T) {
	a, _ := newPreBreakApp(t, customWaterCfg)

	oldWin := new(application.WebviewWindow)
	newWin := new(application.WebviewWindow)
	a.replaceTakeoutWins([]*application.WebviewWindow{oldWin}, &model.TakeoutInfo{ID: "old", Preview: false})
	// 顶替：新会话 + 新窗口，旧窗口在屏幕上的引用已交给调用方关闭
	a.replaceTakeoutWins(nil, &model.TakeoutInfo{ID: "new", Preview: false})
	a.replaceTakeoutWins([]*application.WebviewWindow{newWin}, nil)

	a.removeTakeoutWindow(oldWin) // 旧窗的关闭回调姗姗来迟

	a.mu.Lock()
	cur := a.takeoutCurrent
	live := len(a.takeoutWins)
	a.mu.Unlock()
	if cur == nil || cur.ID != "new" {
		t.Fatalf("旧窗口的回调不该清掉新会话，实际会话 %+v", cur)
	}
	if live != 1 {
		t.Fatalf("新窗口应还在集合里，实际 %d 个", live)
	}
}
