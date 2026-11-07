package main

import (
	"math/rand"
	"path/filepath"
	"testing"
)

// 全局键鼠活动轮询的生命周期（takeout.go 的 ensureSupActivityWatch / releaseSupActivityWatch）。
//
// 这一组钉的是：**休息会话一开局就要盯系统级输入**。曾经的实现只在 ShowTakeoutWindow
// 里启动轮询，于是短 / 长休息窗口在场时，督休专员的二阶段只能靠窗口内的本地
// mousemove / keydown 触发 —— 用户切到别的应用继续敲键、动鼠标，窗口毫无反应，
// 这就是“不能只在提醒窗口上检测，要在整个操作系统层面做全局检测”那条报障。
//
// 窗口与广播本身依赖原生窗口（由无头冒烟 + GUI 探针覆盖），这里只验启停时机：
// 会话在 → 轮询在；会话与窗口都没了 → 轮询收掉；重复调用不会留下两个 goroutine。

const supWatchCfg = `{
  "short": {"enabled": true, "durationMs": 20000},
  "supervisor": {"enabled": false}
}`

// newSupWatchApp 造一个只跑会话构建的最小 App（不碰窗口与 Wails）。
// 必须用 -tags server 构建：否则 showBreakSession 会真的去建原生窗口。
func newSupWatchApp(t *testing.T) *App {
	t.Helper()
	if !serverMode {
		t.Skip("需要在 -tags server 下运行：非 server 模式会真的建原生窗口")
	}
	t.Setenv("WR_STATS_PATH", filepath.Join(t.TempDir(), "stats.json"))
	a := &App{
		cfg:       testSettings(t, supWatchCfg),
		rolesRoot: t.TempDir(),
		rng:       rand.New(rand.NewSource(1)),
		cursor:    make(map[string]int),
	}
	t.Cleanup(func() { a.releaseSupActivityWatch() })
	return a
}

func (a *App) watchStopSnapshot() chan struct{} {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.supWatchStop
}

// TestBreakSessionStartsGlobalActivityWatch 休息会话开局即启轮询，关窗后收掉。
func TestBreakSessionStartsGlobalActivityWatch(t *testing.T) {
	a := newSupWatchApp(t)

	if info := a.supActivityWatchInfo(); info.Running || info.Mode != "" {
		t.Fatalf("初始不该有轮询，得到 %+v", info)
	}

	id := a.ShowBreakSession("short", true, "")
	if id == "" {
		t.Fatal("短休息会话应当建得起来")
	}
	if info := a.supActivityWatchInfo(); !info.Running || info.Mode != "tick" {
		t.Fatalf("休息会话开局就该有全局键鼠轮询，得到 %+v", info)
	}

	// 预览窗口“点任意处关闭”走的就是这条：会话与窗口一起清掉
	a.CloseWindow("break")
	if info := a.supActivityWatchInfo(); info.Running || info.Mode != "off" {
		t.Fatalf("关掉休息窗口后轮询应收掉，得到 %+v", info)
	}
	if ch := a.watchStopSnapshot(); ch != nil {
		t.Fatal("停止信号应当清空")
	}
}

// TestEnsureSupActivityWatchIdempotent 重复 ensure 不该起第二个 goroutine。
func TestEnsureSupActivityWatchIdempotent(t *testing.T) {
	a := newSupWatchApp(t)
	a.ensureSupActivityWatch()
	first := a.watchStopSnapshot()
	if first == nil {
		t.Fatal("ensure 之后应当有停止信号")
	}
	a.ensureSupActivityWatch()
	if second := a.watchStopSnapshot(); second != first {
		t.Fatal("重复 ensure 不该换掉停止信号（否则前一个 goroutine 永远收不掉）")
	}
}

// TestReleaseSupActivityWatchKeepsRunningWhileSessionAlive 有会话在跑就不能停。
// 场景：外卖窗口先关（会顺带调 release），但休息还没结束。
func TestReleaseSupActivityWatchKeepsRunningWhileSessionAlive(t *testing.T) {
	a := newSupWatchApp(t)
	a.ShowBreakSession("short", true, "")

	a.releaseSupActivityWatch()
	if !a.supActivityWatchInfo().Running {
		t.Fatal("休息会话还在跑，轮询不该停")
	}

	a.mu.Lock()
	a.sess = nil
	a.mu.Unlock()
	a.releaseSupActivityWatch()
	if a.supActivityWatchInfo().Running {
		t.Fatal("会话结束且没有窗口，轮询该停")
	}
}
