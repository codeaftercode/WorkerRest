package main

import (
	"strings"
	"testing"

	"workerrest/internal/scheduler"
)

// 外卖「空闲门 × 暂停」的组合矩阵（2026-11-23 全量审计）。
//
// 两条判据互相咬合，谁先谁后决定了这一笔外卖到底弹不弹：
//
//	到点 + 空闲门未达  → takeoutGated（状态卡写“等待空闲”，这一跳已消费）
//	       │
//	       ├─ 暂停只有 idle 一条原因 → **不算拦阻**：人已经离开工位够久了，正是这次
//	       │   提醒要等的那一刻。gateHold 放行，照常 arm → 预通知 → 真窗。
//	       │   （否则「空闲门 15 分钟」永远等不到：空闲满 5 分钟就先被自动暂停了。）
//	       │
//	       └─ 暂停含忙的原因（手动 / 勿扰 / 投屏 / 应用占用）→ 按外卖卡的
//	           “暂停休息时”处置（推迟 / 跳过 / 正常提醒）。
//
// 后半条有个必须一起做的收尾：处置一旦接管，那份“等空闲”的挂起就得撤掉 —— 见
// TestTakeoutGatedClearedByPauseDisposition 里被钉住的两条后果。

// TestTakeoutIdleGateHeldThroughIdleOnlyPause 只挂着 idle 的暂停不拦阻空闲门：
// 到点后安静够久就该照常 arm（预通知 → 真窗），既不能按“跳过”吞掉，也不能按
// “推迟 15 分钟”往后推 —— 那样「空闲门」这个功能在人离开的场景里永远触发不了。
func TestTakeoutIdleGateHeldThroughIdleOnlyPause(t *testing.T) {
	cfg := takeoutGateCfg(60, 30_000) // 提前 60 秒，空闲门 30 秒
	a := newTakeoutApp(t, cfg)
	s, _ := newTestSched(t, cfg)
	a.sched = s
	a.idle.ForceIdle(5_000) // 才安静 5 秒，未达门槛

	a.checkTakeoutReminders()
	if a.takeoutGated == nil {
		t.Fatal("前置：空闲未达应先挂起")
	}
	if a.takeoutPostponed != nil {
		t.Fatal("前置：未达标时不该有推迟线")
	}

	// 人离开达到阈值：只挂 idle 这一条原因
	s.Pause(scheduler.PauseReasonIdle)
	a.idle.ForceIdle(40_000) // 40 秒 ≥ 门槛 30 秒

	a.checkTakeoutReminders()

	if a.takeoutGated != nil {
		t.Fatal("达标后应落成计划，不该继续挂在“等待空闲”上")
	}
	pp := a.takeoutPostponed
	if pp == nil {
		t.Fatal("达标后应落成 armed 计划（预通知 60 秒后弹真窗）")
	}
	if !pp.Armed || pp.Count != 0 {
		t.Fatalf("这必须是空闲门 arm 出来的那一份（Armed=true, Count=0），不是暂停处置推出来的：%+v", pp)
	}
	if left := pp.DueAt - nowMs(); left < 55_000 || left > 65_000 {
		t.Fatalf("arm 的到点时刻应是“现在 + 提前量 60 秒”，得到还剩 %d ms", left)
	}
	if a.preInfo.Owner != "takeout" {
		t.Fatalf("达标后应弹出外卖预通知小窗，得到 owner=%q", a.preInfo.Owner)
	}
}

// TestTakeoutGatedClearedByPauseDisposition 忙原因接管时，“等空闲”的挂起必须撤掉。
//
// 不撤的两条后果都是真的（这条用例的前半段就是它们的复现）：
//  1. 状态卡一直写“等待空闲”，而外卖其实已经排进推迟线了 —— 用户看到“在等空闲”，
//     实际上是“推迟到 15 分钟后”，两个说法对不上；
//  2. 推迟线走完之后那份挂起还在，下一次空闲达标会被 armGatedTakeout 捡起来，把
//     **已经提醒过的**那一笔再弹一遍（它的 At 还是当初那个时刻）。
func TestTakeoutGatedClearedByPauseDisposition(t *testing.T) {
	cfg := takeoutGateCfg(60, 30_000)
	a := newTakeoutApp(t, cfg)
	s, _ := newTestSched(t, cfg)
	a.sched = s
	a.idle.ForceIdle(5_000)

	a.checkTakeoutReminders()
	if a.takeoutGated == nil {
		t.Fatal("前置：空闲未达应先挂起")
	}

	// 勿扰：忙原因，空闲门不再是理由
	s.Pause(pauseReasonDnd)
	a.idle.ForceIdle(40_000) // 就算安静够了，也不该被 idle 那条放行

	a.checkTakeoutReminders()

	if a.takeoutGated != nil {
		t.Fatalf("忙原因接管后不该留着“等空闲”的挂起：%+v", a.takeoutGated)
	}
	if a.TakeoutIdleWaitSnapshot() != nil {
		t.Fatal("状态快照也不该再报“等待空闲”（否则状态卡与实际处置对不上）")
	}
	pp := a.takeoutPostponed
	if pp == nil {
		t.Fatal("没勾“正常提醒”时应按“暂停休息时”处置：推迟提醒")
	}
	if pp.Count != 1 || pp.Armed {
		t.Fatalf("这应是暂停处置推出来的那份（Count=1, Armed=false），不是空闲门 arm 的：%+v", pp)
	}

	// 推迟线到点、提醒发出之后，那份挂起不能复活把同一笔再弹一遍
	s.Resume(pauseReasonDnd)
	a.mu.Lock()
	a.takeoutPostponed.DueAt = nowMs() - 1
	a.mu.Unlock()
	a.checkTakeoutReminders()
	a.checkTakeoutReminders()
	if a.takeoutGated != nil {
		t.Fatal("推迟线走完后挂起仍活着 —— 下一次空闲达标会把它 arm 起来重复提醒")
	}
}

// TestTakeoutGatedClearedBySkipWhilePaused 同上，处置是“跳过”时也要清掉。
// 跳过意味着这一笔不弹了，留一份“等空闲”在手里等于给它留了条后路。
func TestTakeoutGatedClearedBySkipWhilePaused(t *testing.T) {
	cfg := takeoutGateCfg(60, 30_000)
	// 外卖卡选“跳过提醒”
	cfg = strings.Replace(cfg, `"idleGate":true`, `"idleGate":true,"pauseAction":"skip"`, 1)
	a := newTakeoutApp(t, cfg)
	s, _ := newTestSched(t, cfg)
	a.sched = s
	a.idle.ForceIdle(5_000)

	a.checkTakeoutReminders()
	if a.takeoutGated == nil {
		t.Fatal("前置：空闲未达应先挂起")
	}

	s.Pause(pauseReasonDnd)
	a.idle.ForceIdle(40_000)
	a.checkTakeoutReminders()

	if a.takeoutGated != nil {
		t.Fatalf("按“跳过”处置之后不该留着挂起：%+v", a.takeoutGated)
	}
	if a.takeoutPostponed != nil {
		t.Fatal("“跳过”不该留推迟线")
	}
	if a.takeoutCurrent != nil {
		t.Fatal("“跳过”不该弹提醒窗")
	}
}
