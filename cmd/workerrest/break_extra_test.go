package main

// 撞车处理的宿主侧回归（脱离窗口与 Wails）：
//   - 短休息被长休息顶替时，统计记「完成」而不是「提前结束」；
//   - 休息期间到点的自定义提醒 / 外卖并入当前休息会话（单窗多条）；
//   - 休息结束时，还没被处理的外卖交回独立窗口（不能随休息窗一起消失）。

import (
	"math/rand"
	"path/filepath"
	"testing"

	"workerrest/internal/idle"
	"workerrest/internal/model"
)

func newExtraApp(t *testing.T, cfgJSON string) *App {
	t.Helper()
	t.Setenv("WR_STATS_PATH", filepath.Join(t.TempDir(), "stats.json"))
	return &App{
		cfg:             testSettings(t, cfgJSON),
		rolesRoot:       t.TempDir(),
		rng:             rand.New(rand.NewSource(1)),
		cursor:          make(map[string]int),
		takeoutFired:    map[string]bool{},
		takeoutPreFired: map[string]bool{},
		idle:            idle.NewIdleState(),
	}
}

const extraCfg = `{
  "short":{"enabled":true,"mode":"countdown","intervalMs":600000,"durationMs":20000,"notifyBeforeMs":0,"postponeMs":120000,"slots":[]},
  "long":{"enabled":true,"mode":"countdown","intervalMs":3600000,"durationMs":60000,"notifyBeforeMs":0,"postponeMs":120000,"slots":[]},
  "customBreaks":[{"id":"cb-a","name":"喝水提醒","config":{"enabled":true,"mode":"countdown","intervalMs":300000,"durationMs":10000,"notifyBeforeMs":0,"postponeMs":120000,"slots":[]},"messages":{"order":"fixed","fixedId":"m1","items":[{"id":"m1","text":"起来喝口水","enabled":true}]}}],
  "takeout":{"enabled":true,"leadSec":0,"postponeMin":15,"slots":[{"id":"s1","hour":12,"minute":30,"repeat":"daily","days":[],"enabled":true}]}
}`

// 短休息被长休息顶替：统计记「完成 1 次」，提前结束那格必须是 0
// （用户口径：这不是提前结束，是被更长的休息接走了）。
func TestPreemptedShortCountsAsCompleted(t *testing.T) {
	a := newExtraApp(t, extraCfg)
	now := nowMs()
	a.mu.Lock()
	a.sess = &model.BreakInfo{ID: "short-1", Type: "short", Name: "短休息", EndsAt: now + 17000, DurationMs: 20000}
	a.mu.Unlock()
	a.statsStore().OnBreakStart("short")

	a.CompleteBreakWindows()

	today := a.statsStore().Today()
	if today.ShortCount != 1 {
		t.Fatalf("被顶替的短休息应记完成 1 次，实际 ShortCount=%d", today.ShortCount)
	}
	if today.EndEarlyCount != 0 {
		t.Fatalf("被顶替的短休息不该记提前结束，实际 EndEarlyCount=%d", today.EndEarlyCount)
	}
	if a.sess != nil {
		t.Fatalf("收尾后会话应清空，实际仍有 %+v", a.sess)
	}
}

// 没有单独跑过的短休息（长休息进行中到点）按完成记一次，且不记时长
// （那段时间已经由长休息那一段记进去了，再记一遍会把休息时长算重）。
func TestBreakCountedAddsCountWithoutDuration(t *testing.T) {
	a := newExtraApp(t, extraCfg)
	a.OnBreakCounted("short")
	today := a.statsStore().Today()
	if today.ShortCount != 1 {
		t.Fatalf("应记完成 1 次，实际 %d", today.ShortCount)
	}
	if today.ShortMs != 0 || today.BreakMs != 0 {
		t.Fatalf("覆盖走的这一班不该记时长，实际 ShortMs=%d BreakMs=%d", today.ShortMs, today.BreakMs)
	}
}

// 休息期间到点的自定义提醒：挂进当前会话，窗口上能看到，且不再另开窗口。
func TestOnBreakExtraMergesIntoSession(t *testing.T) {
	a := newExtraApp(t, extraCfg)
	a.mu.Lock()
	a.sess = &model.BreakInfo{ID: "short-1", Type: "short", Name: "短休息", DurationMs: 20000}
	a.mu.Unlock()

	if ok := a.OnBreakExtra("cb-a", nowMs()); !ok {
		t.Fatal("有休息会话时应接受并入")
	}
	a.mu.Lock()
	extras := append([]model.BreakExtra(nil), a.sess.Extras...)
	a.mu.Unlock()
	if len(extras) != 1 || extras[0].Kind != "cb-a" {
		t.Fatalf("自定义提醒应并入会话，实际 %+v", extras)
	}
	if extras[0].Text != "起来喝口水" {
		t.Fatalf("副条应带上这条提醒本次抽中的内容，实际 %q", extras[0].Text)
	}
	// 同一条提醒重复并入只保留一张卡
	a.OnBreakExtra("cb-a", nowMs())
	a.mu.Lock()
	n := len(a.sess.Extras)
	a.mu.Unlock()
	if n != 1 {
		t.Fatalf("同一条提醒在一次休息里只该挂一次，实际 %d 张", n)
	}
}

// 没有休息会话（窗口被 Alt+F4 关掉）时必须回 false：调度器据此不消费这一班，
// 让它之后照常提醒 —— 否则就是“消费掉了但哪儿都没显示”的静默丢失。
func TestOnBreakExtraRejectedWithoutSession(t *testing.T) {
	a := newExtraApp(t, extraCfg)
	if ok := a.OnBreakExtra("cb-a", nowMs()); ok {
		t.Fatal("没有休息会话时不该接受并入")
	}
}

// 休息窗在场时外卖并入休息窗，不另开自己的窗口。
func TestTakeoutMergedIntoRunningBreak(t *testing.T) {
	a := newExtraApp(t, extraCfg)
	a.mu.Lock()
	a.sess = &model.BreakInfo{ID: "short-1", Type: "short", Name: "短休息", DurationMs: 20000}
	a.mu.Unlock()

	slot := model.TimeSlot{ID: "s1", Hour: 12, Minute: 30, Repeat: "daily", Enabled: true}
	a.showTakeoutWindowsAt(slot, false, 0, 0)

	a.mu.Lock()
	cur := a.takeoutCurrent
	extras := append([]model.BreakExtra(nil), a.sess.Extras...)
	a.mu.Unlock()
	if cur == nil {
		t.Fatal("外卖会话应被建起来（推迟 / 已点好要按它记账）")
	}
	if len(extras) != 1 || extras[0].Kind != "takeout" || extras[0].Takeout == nil {
		t.Fatalf("外卖应并入休息窗并带上自己的会话，实际 %+v", extras)
	}
}

// 休息结束时，还没被处理的外卖要交回独立窗口 —— 随休息窗一起关掉等于用户没看见。
func TestTakeoutHandedBackWhenBreakEnds(t *testing.T) {
	a := newExtraApp(t, extraCfg)
	a.mu.Lock()
	a.sess = &model.BreakInfo{ID: "short-1", Type: "short", Name: "短休息", DurationMs: 20000}
	a.mu.Unlock()
	slot := model.TimeSlot{ID: "s1", Hour: 12, Minute: 30, Repeat: "daily", Enabled: true}
	a.showTakeoutWindowsAt(slot, false, 0, 0)
	a.mu.Lock()
	before := a.takeoutCurrent.ID
	a.mu.Unlock()

	a.closeBreakWindowsOpt(true, true, false, false)

	a.mu.Lock()
	after := a.takeoutCurrent
	a.mu.Unlock()
	if after == nil {
		t.Fatal("休息结束后外卖会话不该消失")
	}
	if after.ID == before {
		t.Fatalf("休息结束后应把外卖重新交回独立窗口（重新建会话），实际还是并进去的那份 %q", before)
	}
}

// 客户端点了副条上的“知道了”：外卖会话被处理掉，副条也要从休息窗上摘掉。
func TestTakeoutExtraDroppedAfterAction(t *testing.T) {
	a := newExtraApp(t, extraCfg)
	a.mu.Lock()
	a.sess = &model.BreakInfo{ID: "short-1", Type: "short", Name: "短休息", DurationMs: 20000}
	a.mu.Unlock()
	slot := model.TimeSlot{ID: "s1", Hour: 12, Minute: 30, Repeat: "daily", Enabled: true}
	a.showTakeoutWindowsAt(slot, false, 0, 0)

	if err := a.TakeoutAction("break", "ordered"); err != nil {
		t.Fatalf("TakeoutAction 出错: %v", err)
	}
	a.mu.Lock()
	extras := append([]model.BreakExtra(nil), a.sess.Extras...)
	cur := a.takeoutCurrent
	a.mu.Unlock()
	if cur != nil {
		t.Fatal("处理掉之后外卖会话应清空")
	}
	if len(extras) != 0 {
		t.Fatalf("处理掉之后副条应摘掉，实际还剩 %+v", extras)
	}
}
