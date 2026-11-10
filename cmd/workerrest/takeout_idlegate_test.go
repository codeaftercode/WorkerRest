package main

import (
	"strconv"
	"testing"
	"time"
	"workerrest/internal/model"
)

// 外卖提醒的空闲门（“鼠标键盘无活动 x 时间”才弹）回归。
//
// 外卖不走调度器，自己每秒 tick，所以这里的口径是：
//
//	到点 ──空闲未达──> takeoutGated（状态卡写“等待空闲”），这一跳已消费
//	                     │
//	                     └─空闲达标─> armed 推迟线（DueAt = now + 提前通知秒数）
//	                                    └─> 提前通知窗 ─> 正式提醒窗（提前 0 秒则直接真窗）
//
// 用户点的“推迟”倒计时结束后要**重新**过一遍空闲门；armed 那条刚达标，不再复判。

// takeoutGateCfg 造一份“此刻到点”的外卖配置（空闲门时长以毫秒给，0 = 不设门）
func takeoutGateCfg(leadSec int, gateMs int64) string {
	now := time.Now()
	return `{"takeout":{"enabled":true,"leadSec":` + itoa(leadSec) +
		`,"postponeMin":15,"idleGate":true,"idleGateMs":` + strconv.FormatInt(gateMs, 10) +
		`,"slots":[{"id":"s1","hour":` + itoa(now.Hour()) + `,"minute":` + itoa(now.Minute()) +
		`,"repeat":"daily","days":[],"enabled":true}]}}`
}

// 到点了但键鼠还在动：挂起等空闲，不弹任何窗，也不重复弹。
func TestTakeoutIdleGateWaitsForIdle(t *testing.T) {
	a := newTakeoutApp(t, takeoutGateCfg(60, 30_000))
	a.sched = nil
	a.idle.ForceIdle(5_000) // 门槛 30 秒，才安静了 5 秒

	a.checkTakeoutReminders()

	if a.takeoutGated == nil {
		t.Fatal("空闲不足时应挂起等空闲")
	}
	if a.takeoutGated.Slot.ID != "s1" {
		t.Fatalf("挂起的应是到点那条，得到 %+v", a.takeoutGated)
	}
	if a.takeoutCurrent != nil {
		t.Fatal("等待空闲期间不该弹提醒窗")
	}
	if a.preInfo.Owner == "takeout" {
		t.Fatal("等待空闲期间不该弹提前通知窗")
	}
	if a.takeoutPostponed != nil {
		t.Fatal("等待中的那一笔不是“推迟”（用户没点），不该进推迟线")
	}
	if got := a.TakeoutIdleWaitSnapshot(); got == nil || got.Slot.ID != "s1" {
		t.Fatalf("状态快照应报出“等待空闲”，得到 %+v", got)
	}

	// 人还在动：再跑几拍仍然是等待，不重复消费、不弹窗
	for i := 0; i < 3; i++ {
		a.checkTakeoutReminders()
	}
	if a.takeoutGated == nil || a.takeoutCurrent != nil {
		t.Fatal("空闲未达期间应一直等着，且不弹窗")
	}
}

// 空闲达标：落成 armed 推迟线（DueAt = 现在 + 提前通知秒数），并立刻弹提前通知窗。
func TestTakeoutIdleGateArmsPreNotify(t *testing.T) {
	a := newTakeoutApp(t, takeoutGateCfg(60, 30_000))
	a.sched = nil
	a.idle.ForceIdle(5_000)
	a.checkTakeoutReminders() // 先进等待

	a.idle.ForceIdle(30_000)
	before := nowMs()
	a.checkTakeoutReminders() // 达标

	if a.takeoutGated != nil {
		t.Fatal("达标后不该还在等空闲")
	}
	pp := a.takeoutPostponed
	if pp == nil {
		t.Fatal("达标后应落成一条计划（复用推迟线走提前通知 → 真窗）")
	}
	if !pp.Armed {
		t.Fatal("这条计划应标记 Armed（刚达标，不再复判空闲）")
	}
	want := before + 60_000
	if diff := pp.DueAt - want; diff < -2_000 || diff > 2_000 {
		t.Fatalf("到点应≈现在+提前通知 60 秒，want=%d got=%d（差 %d）", want, pp.DueAt, diff)
	}
	if a.takeoutCurrent != nil {
		t.Fatal("提前通知阶段不该弹正式窗")
	}
	if a.preInfo.Owner != "takeout" {
		t.Fatalf("达标后应弹外卖提前通知窗，得到 owner=%q", a.preInfo.Owner)
	}
	if a.preTargetAt != pp.DueAt {
		t.Fatalf("提前通知倒计时应指向到点时刻 %d，得到 %d", pp.DueAt, a.preTargetAt)
	}
}

// 提前通知为 0 秒：达标即弹正式提醒窗（不留提示窗一环）。
func TestTakeoutIdleGateZeroLeadFiresDirectly(t *testing.T) {
	a := newTakeoutApp(t, takeoutGateCfg(0, 30_000))
	a.sched = nil
	a.idle.ForceIdle(40_000) // 到点当下就已经安静够久

	a.checkTakeoutReminders() // 挂起并立刻落成 “DueAt = now” 的计划
	if a.takeoutCurrent != nil {
		t.Fatal("落位那一拍不该同时弹窗（先挂起再走推迟线）")
	}
	if a.takeoutPostponed == nil {
		t.Fatal("达标后应有计划在路上")
	}

	a.checkTakeoutReminders() // 下一拍：推迟线到点
	if a.takeoutCurrent == nil {
		t.Fatal("提前通知 0 秒时，达标后应直接弹正式提醒窗")
	}
	if a.preInfo.Owner == "takeout" {
		t.Fatal("提前通知 0 秒时不该弹提前通知窗")
	}
	if a.takeoutPostponed != nil {
		t.Fatal("弹过之后应清掉计划，避免重复弹")
	}
}

// 用户点的“推迟”倒计时结束后，重新按空闲门判断：人还在动 → 回到等待空闲。
func TestTakeoutIdleGateRechecksAfterPostpone(t *testing.T) {
	a := newTakeoutApp(t, takeoutGateCfg(60, 30_000))
	a.sched = nil
	slot := model.TimeSlot{ID: "s1", Hour: 12, Minute: 30, Repeat: "daily", Enabled: true}
	due := nowMs() - 1
	a.takeoutPostponed = &model.TakeoutPostponed{Slot: slot, DueAt: due, Count: 1}
	a.idle.ForceIdle(1_000) // 推迟倒计时结束时人刚在打字

	a.checkTakeoutReminders()

	if a.takeoutCurrent != nil {
		t.Fatal("空闲不足时不该弹正式窗")
	}
	if a.takeoutPostponed != nil {
		t.Fatal("这条推迟线应转回等待空闲")
	}
	if a.takeoutGated == nil {
		t.Fatal("推迟到点后应重新等空闲")
	}
	if a.takeoutGated.At != due {
		t.Fatalf("重新等待的应是推迟后的那一跳（%d），得到 %d", due, a.takeoutGated.At)
	}
	if a.preInfo.Owner == "takeout" {
		t.Fatal("回到等待空闲时应收掉原来的提前通知窗")
	}
}

// armed 那条是刚达标的计划：不倒回去复判（否则“达标 → 等空闲”会无限打转）。
func TestTakeoutIdleGateArmedSkipsRecheck(t *testing.T) {
	a := newTakeoutApp(t, takeoutGateCfg(0, 30_000))
	a.sched = nil
	slot := model.TimeSlot{ID: "s1", Hour: 12, Minute: 30, Repeat: "daily", Enabled: true}
	a.takeoutPostponed = &model.TakeoutPostponed{Slot: slot, DueAt: nowMs() - 1, Count: 1, Armed: true}
	a.idle.ForceIdle(0) // 人已经回来了

	a.checkTakeoutReminders()

	if a.takeoutCurrent == nil {
		t.Fatal("armed 计划到点应直接弹正式窗")
	}
	if a.takeoutCurrent.PostponedCount != 1 {
		t.Fatalf("推迟次数应原样带出，得到 %d", a.takeoutCurrent.PostponedCount)
	}
	if a.takeoutGated != nil {
		t.Fatal("不该倒回等待空闲")
	}
}

// 开了空闲门就不弹“提前通知”小窗（那条线在到点之前，人可能还在忙）。
func TestTakeoutIdleGateSuppressesPreNotifyLine(t *testing.T) {
	now := time.Now()
	ft := now.Add(60 * time.Second)
	if ft.Hour() == now.Hour() && ft.Minute() == now.Minute() {
		t.Skip("整分钟边界，构造不出“下一分钟”的提前区间")
	}
	if ft.YearDay() != now.YearDay() {
		t.Skip("跨天边界，TimeSlot 只有时分，构造不出今天未来的槽位")
	}
	cfg := `{"takeout":{"enabled":true,"leadSec":60,"postponeMin":15,"idleGate":true,"idleGateMs":30000,` +
		`"slots":[{"id":"s1","hour":` + itoa(ft.Hour()) + `,"minute":` + itoa(ft.Minute()) +
		`,"repeat":"daily","days":[],"enabled":true}]}}`
	a := newTakeoutApp(t, cfg)
	a.sched = nil
	a.idle.ForceIdle(0)

	a.checkTakeoutReminders()

	if a.preInfo.Owner == "takeout" {
		t.Fatal("开了空闲门时，进入提前区间也不该弹提前通知窗（人可能还在忙）")
	}
	if len(a.takeoutPreFired) != 0 {
		t.Fatalf("不该记提前通知去重键，得到 %v", a.takeoutPreFired)
	}
	if a.takeoutGated != nil || a.takeoutCurrent != nil {
		t.Fatal("离原定时刻还早，此刻不该挂起也不该弹窗")
	}
}

// 空闲门被取消（用户取消勾选）：挂着的那一笔失去意义，丢掉；按原来的三线照常走。
func TestTakeoutIdleGateClearedWhenGateDisabled(t *testing.T) {
	a := newTakeoutApp(t, takeoutGateCfg(0, 30_000))
	a.sched = nil
	a.idle.ForceIdle(0)
	a.checkTakeoutReminders() // 挂起
	if a.takeoutGated == nil {
		t.Fatal("前置：应已挂起")
	}

	// 用户取消勾选：换一份没有空闲门的配置，再跑一拍
	a.cfg = testSettings(t, takeoutGateCfg(0, 0))
	a.checkTakeoutReminders()

	if a.takeoutGated != nil {
		t.Fatal("空闲门取消后，挂起的那一笔应被丢掉")
	}
	if a.takeoutPostponed != nil {
		t.Fatal("不该凭空冒出一条计划")
	}
}
