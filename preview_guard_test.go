package main

import (
	"path/filepath"
	"testing"
)

// 预览不许打扰真实提醒。
//
// 改前的毛病：真实休息窗口在场时点「预览」→ PreviewBreak 建一个 preview 会话覆盖
// a.sess，ShowBreakWindow 发现会话 id 变了、把旧窗口当 stale 关掉 —— 用户看到的是
// 「点了预览，正在进行的休息窗被关掉」，且那次休息的统计、结束判定全乱。
// 外卖更糟：showTakeoutWindowsAt 直接换掉 a.takeoutWins，真实窗口的引用就此丢掉、
// 再也关不掉。这里把两条都钉住。

// TestPreviewBreakRejectedWhileRealBreakRunning 真实休息在场 → 拒弹，且会话原样保留
func TestPreviewBreakRejectedWhileRealBreakRunning(t *testing.T) {
	// 真实休息会走 statsStore().OnBreakStart，落点钉进临时目录
	t.Setenv("WR_STATS_PATH", filepath.Join(t.TempDir(), "stats.json"))
	a, _ := newPreBreakApp(t, customWaterCfg)

	realID := a.showBreakSession("short", false, "")
	if realID == "" {
		t.Fatal("真实休息会话没建起来")
	}
	if !a.realBreakActive() {
		t.Fatal("真实休息刚开始，realBreakActive 应为 true")
	}

	if got := a.PreviewBreak("short", ""); got != previewBusyReply {
		t.Fatalf("真实休息在场时预览应被拒（返回 %q），实际返回 %q", previewBusyReply, got)
	}

	a.mu.Lock()
	sess := a.sess
	a.mu.Unlock()
	if sess == nil || sess.ID != realID {
		t.Fatalf("拒弹后会话应原样保留（%q），实际是 %+v", realID, sess)
	}
	if sess.Preview {
		t.Fatal("真实会话被预览顶掉了（preview=true）")
	}

	// 真实休息收掉之后必须恢复可预览 —— 拦的是「进行中」，不是永久禁用
	a.closeBreakWindows()
	if a.realBreakActive() {
		t.Fatal("休息结束后 realBreakActive 应为 false")
	}
	if got := a.PreviewBreak("short", ""); got == "" || got == previewBusyReply {
		t.Fatalf("真实休息结束后应能正常预览，实际返回 %q", got)
	}
}

// TestPreviewTakeoutRejectedWhileRealTakeoutRunning 真实外卖窗口在场 → 拒弹；预览态不拦
func TestPreviewTakeoutRejectedWhileRealTakeoutRunning(t *testing.T) {
	a, _ := newPreBreakApp(t, customWaterCfg)

	a.mu.Lock()
	a.takeoutCurrent = &TakeoutInfo{Preview: false}
	a.mu.Unlock()
	if !a.realTakeoutActive() {
		t.Fatal("构造了真实外卖会话，realTakeoutActive 应为 true")
	}
	if got := a.PreviewTakeout("takeout"); got != previewBusyReply {
		t.Fatalf("真实外卖窗口在场时预览应被拒（返回 %q），实际返回 %q", previewBusyReply, got)
	}

	// 预览态不算「真实在场」：预览覆盖预览仍是预览（不受这条拦截影响）
	a.mu.Lock()
	a.takeoutCurrent = &TakeoutInfo{Preview: true}
	a.mu.Unlock()
	if a.realTakeoutActive() {
		t.Fatal("预览会话不该被当成真实外卖提醒")
	}
	if got := a.PreviewTakeout("takeout"); got != "ok" {
		t.Fatalf("预览态不应被拦，实际返回 %q", got)
	}
	a.closeTakeoutWindows()
}
