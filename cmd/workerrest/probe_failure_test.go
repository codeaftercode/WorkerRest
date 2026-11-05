package main

import (
	"testing"

	"workerrest/internal/windnd"
	"workerrest/internal/winproc"
)

// 「探测失败 ≠ 条件不成立」的回归。
//
// 三条监视链路各自会碰到「这一次读不到」：SHQueryUserNotificationState 调用失败、
// QueryDisplayConfig 失败（UAC 桌面 / 远程会话 / 拓扑正在重排）、Toolhelp 快照拿不到句柄。
// 这三种情况以前都把探测结果当成「条件不成立」往下喂，于是：
//
//   - 应用占用：误判「目标应用都退出了」→ 自动恢复提醒 → 下一拍又暂停；
//   - 勿扰 / 全屏 / 投屏：走「条件消失」分支，把用户点过恢复的忽略表清掉 →
//     下一次查询成功、条件仍成立，暂停又挂回来（「我明明点过恢复了」）。
//
// 正确的语义是「没有结论就什么都别做」：上一拍的状态继续生效，等下一次查询成功再判。
// 真机上没法让系统 API 按需失败，所以判据都抽在 dndApply / castApply / appPauseApply
// 这三个函数里，测试直接喂一个失败结果。

// probeFailed / probeOK 造两种「探测结果」，写法与真实返回同形。
func probeFailed() winproc.AppProcessList {
	return winproc.AppProcessList{Supported: false, Names: []string{}, Detail: "CreateToolhelp32Snapshot 返回空句柄"}
}

func probeOK(names ...string) winproc.AppProcessList {
	return winproc.AppProcessList{Supported: true, Names: names, Detail: "测试造的结果"}
}

// 勿扰：探测失败时既不该解除暂停，也不该动忽略表与「条件是否成立」的登记。
func TestDndProbeFailureChangesNothing(t *testing.T) {
	a, s := newWatchApp(t, shortCountdown)
	a.sched.Pause(pauseReasonDnd)
	a.setAutoReasonActive(pauseReasonDnd, true)
	a.ignoreAutoPause([]string{pauseReasonDnd})

	failed := windnd.DndStatus{Supported: false, Detail: "SHQueryUserNotificationState 调用失败"}
	a.dndApply(failed, windnd.FullScreenStatus{Supported: false, Detail: failed.Detail}, true, true)

	if !s.IsPaused() {
		t.Fatal("探测失败不该解除暂停")
	}
	if !a.isAutoPauseIgnored(pauseReasonDnd) {
		t.Fatal("探测失败不该清掉忽略表：下一次查询成功、条件仍成立时会把暂停偷偷挂回来")
	}
	if !a.autoReasonActiveSnapshot()[pauseReasonDnd] {
		t.Fatal("探测失败不该把「条件仍成立」改写成 false")
	}
}

// 对照：确实查到了「勿扰已关闭」——这时才该清忽略、登记 false，且仍保持暂停
// （勿扰属于「条件消失不自动恢复」那条策略）。
func TestDndRealClearDoesClearIgnore(t *testing.T) {
	a, s := newWatchApp(t, shortCountdown)
	a.sched.Pause(pauseReasonDnd)
	a.setAutoReasonActive(pauseReasonDnd, true)
	a.ignoreAutoPause([]string{pauseReasonDnd})

	a.dndApply(windnd.DndStatus{Supported: true, Active: false},
		windnd.FullScreenStatus{Supported: true, Active: false}, true, true)

	if a.isAutoPauseIgnored(pauseReasonDnd) {
		t.Fatal("条件真的消失了，忽略应该解除（下次再开勿扰照常暂停）")
	}
	if a.autoReasonActiveSnapshot()[pauseReasonDnd] {
		t.Fatal("条件真的消失后，登记应为 false")
	}
	if !s.IsPaused() {
		t.Fatal("勿扰属于「条件消失不自动恢复」那一侧，应保持暂停")
	}
}

// 投屏：探测失败时两条原因都不该被当成「已结束」。
func TestCastProbeFailureChangesNothing(t *testing.T) {
	a, s := newWatchApp(t, shortCountdown)
	a.sched.Pause(pauseReasonCastMirror)
	a.sched.Pause(pauseReasonCastWireless)
	a.setAutoReasonActive(pauseReasonCastMirror, true)
	a.setAutoReasonActive(pauseReasonCastWireless, true)
	a.ignoreAutoPause([]string{pauseReasonCastMirror, pauseReasonCastWireless})

	a.castApply(windnd.CastStatus{Supported: false, Detail: "QueryDisplayConfig 调用失败"}, true, true)

	if !s.IsPaused() {
		t.Fatal("探测失败不该解除暂停")
	}
	for _, r := range []string{pauseReasonCastMirror, pauseReasonCastWireless} {
		if !a.isAutoPauseIgnored(r) {
			t.Fatalf("探测失败不该清掉 %s 的忽略表", r)
		}
		if !a.autoReasonActiveSnapshot()[r] {
			t.Fatalf("探测失败不该把 %s 的条件登记改成 false", r)
		}
	}
}

// 对照：查到「确实没有投屏了」→ 清忽略、登记 false，仍保持暂停（投屏不自动恢复）。
func TestCastRealClearDoesClearIgnore(t *testing.T) {
	a, s := newWatchApp(t, shortCountdown)
	a.sched.Pause(pauseReasonCastMirror)
	a.setAutoReasonActive(pauseReasonCastMirror, true)
	a.ignoreAutoPause([]string{pauseReasonCastMirror})

	a.castApply(windnd.CastStatus{Supported: true, Mirror: false, Wireless: false}, true, true)

	if a.isAutoPauseIgnored(pauseReasonCastMirror) {
		t.Fatal("投屏真的断了，忽略应解除")
	}
	if a.autoReasonActiveSnapshot()[pauseReasonCastMirror] {
		t.Fatal("投屏真的断了，登记应为 false")
	}
	if !s.IsPaused() {
		t.Fatal("投屏属于「条件消失不自动恢复」那一侧，应保持暂停")
	}
}

// 应用占用：枚举失败时不能自动恢复，也不能清 logged（否则下一拍命中会再打一条「命中」日志，
// 看上去像刚切进会议）。
func TestAppPauseProbeFailureKeepsPause(t *testing.T) {
	a, s := newWatchApp(t, appPauseCfg(t, true))
	a.appPauseWatchTick()
	if !a.appPauseHitLogged {
		t.Fatal("前置：应已命中")
	}
	if !s.IsPaused() {
		t.Fatal("前置：应已暂停")
	}

	a.appPauseApply(a.CfgSnapshot().AppPauseCfg(), probeFailed())

	if !s.IsPaused() {
		t.Fatal("枚举失败不该被当成「目标应用都退出了」而自动恢复")
	}
	if !a.appPauseHitLogged {
		t.Fatal("枚举失败不该清掉命中标记")
	}
	if !a.autoReasonActiveSnapshot()[pauseReasonAppPause] {
		t.Fatal("枚举失败不该把条件登记改成 false")
	}
}

// 对照：枚举成功但确实没命中 → 连续两拍后自动恢复（应用占用是「条件消失就恢复」那一侧，
// 但要先跨过 appPauseExitConfirmMisses 那道防抖）。
func TestAppPauseRealClearResumes(t *testing.T) {
	a, s := newWatchApp(t, appPauseCfg(t, true))
	a.appPauseWatchTick()
	if !s.IsPaused() {
		t.Fatal("前置：应已暂停")
	}

	acfg := a.CfgSnapshot().AppPauseCfg()
	a.appPauseApply(acfg, probeOK("nope-not-a-real-app.exe"))
	if !s.IsPaused() {
		t.Fatal("第一拍没命中不该立刻恢复：应用可能正在重启")
	}
	a.appPauseApply(acfg, probeOK("nope-not-a-real-app.exe"))

	if s.IsPaused() {
		t.Fatal("连续两拍都没命中，应自动恢复")
	}
	if a.appPauseHitLogged {
		t.Fatal("恢复后命中标记应清零")
	}
}

// 计数必须「命中即清零」，不能跨间隙累积：否则「抖动一下 → 命中 → 又抖一下」会被
// 当成连续两拍退出，迟滞形同虚设。
func TestAppPauseMissStreakResetsOnHit(t *testing.T) {
	a, s := newWatchApp(t, appPauseCfg(t, true))
	a.appPauseWatchTick() // 命中，计数从 0 起
	acfg := a.CfgSnapshot().AppPauseCfg()

	miss := probeOK("nope-not-a-real-app.exe")
	a.appPauseApply(acfg, miss)
	hit := func() {
		a.appPauseWatchTick() // 真实枚举，目标进程就在本机跑着
	}

	hit()
	a.appPauseApply(acfg, miss)
	if !s.IsPaused() {
		t.Fatal("两次未命中之间夹着一次命中，不该算连续退出")
	}
	a.appPauseApply(acfg, miss)
	if s.IsPaused() {
		t.Fatal("这次才是连续第二拍，应恢复")
	}
}

// 功能关掉 = 明确的「不再管」，不必等第二拍：用户关了开关却还暂停 4 秒像是没生效。
func TestAppPauseDisabledTakesEffectImmediately(t *testing.T) {
	a, s := newWatchApp(t, appPauseCfg(t, true))
	a.appPauseWatchTick()
	if !s.IsPaused() {
		t.Fatal("前置：应已暂停")
	}

	a.mu.Lock()
	a.cfg = testSettings(t, shortCountdown)
	a.mu.Unlock()
	a.appPauseWatchTick()

	if s.IsPaused() {
		t.Fatal("关掉「应用运行时暂停」应立即恢复，不该走两拍确认")
	}
}
