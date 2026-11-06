package main

import (
	"testing"

	"workerrest/internal/windnd"
	"workerrest/internal/winproc"
)

// 「探测失败 ≠ 条件不成立」的回归。
//
// 几条监视链路各自会碰到「这一次读不到」：SHQueryUserNotificationState 调用失败、
// WNF 勿扰档位读不到（未文档化接口，某些系统 / 权限下不可用）、QueryDisplayConfig 失败
// （UAC 桌面 / 远程会话 / 拓扑正在重排）、Toolhelp 快照拿不到句柄。
// 这三种情况以前都把探测结果当成「条件不成立」往下喂，于是：
//
//   - 应用占用：误判「目标应用都退出了」→ 自动恢复提醒 → 下一拍又暂停；
//   - 请勿打扰 / 全屏或演示 / 投屏：走「条件消失」分支，把用户点过恢复的忽略表清掉 →
//     下一次查询成功、条件仍成立，暂停又挂回来（「我明明点过恢复了」）。
//
// 正确的语义是「没有结论就什么都别做」：上一拍的状态继续生效，等下一次查询成功再判。
// 真机上没法让系统 API 按需失败，所以判据都抽在 dndApply / castApply / appPauseApply
// 这三个函数里，测试直接喂一个失败结果。两条信号各自独立失败时也不能互相牵连，
// 见 TestDndProbeLinksAreIndependent。

// probeFailed / probeOK 造两种「探测结果」，写法与真实返回同形。
func probeFailed() winproc.AppProcessList {
	return winproc.AppProcessList{Supported: false, Names: []string{}, Detail: "CreateToolhelp32Snapshot 返回空句柄"}
}

func probeOK(names ...string) winproc.AppProcessList {
	return winproc.AppProcessList{Supported: true, Names: names, Detail: "测试造的结果"}
}

// 勿扰：两条信号都读不到时既不该解除暂停，也不该动忽略表与「条件是否成立」的登记。
func TestDndProbeFailureChangesNothing(t *testing.T) {
	a, s := newWatchApp(t, shortCountdown)
	a.sched.Pause(pauseReasonDnd)
	a.setAutoReasonActive(pauseReasonDnd, true)
	a.ignoreAutoPause([]string{pauseReasonDnd})

	a.dndApply(windnd.BusyStatus{}, true, true) // QUNS / WNF 都失败

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

	// QUNS=5 正常、WNF 档位 0（用户没按勿扰）：两条条件都不成立
	a.dndApply(windnd.BusyStatus{QunsSupported: true, Quns: 5, WnfSupported: true, QuietProfile: 0},
		true, true)

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

// 两条探测链路互不牵连（2026-11-06 拆分的核心回归）：
// WNF 读不到只让「勿扰」没有结论，QUNS 的全屏或演示照常判；反之亦然。
func TestDndProbeLinksAreIndependent(t *testing.T) {
	t.Run("WNF 失败，QUNS=2 仍命中全屏或演示", func(t *testing.T) {
		a, s := newWatchApp(t, shortCountdown)
		st := windnd.BusyStatus{QunsSupported: true, Quns: 2, WnfSupported: false}
		a.dndApply(st, true, true)
		a.dndApply(st, true, true) // 进入侧去抖第二拍
		rs := s.StateCopy().PauseReasons
		if len(rs) != 1 || rs[0] != pauseReasonFullScr {
			t.Fatalf("WNF 失败时仍应命中「全屏或演示」，得到 %v", rs)
		}
		if a.autoReasonActiveSnapshot()[pauseReasonDnd] {
			t.Fatal("WNF 失败不该把「勿扰」登记成条件成立")
		}
	})

	t.Run("QUNS 失败，WNF 仍命中勿扰", func(t *testing.T) {
		a, s := newWatchApp(t, shortCountdown)
		st := windnd.BusyStatus{QunsSupported: false, WnfSupported: true, QuietProfile: 1}
		a.dndApply(st, true, true)
		a.dndApply(st, true, true)
		rs := s.StateCopy().PauseReasons
		if len(rs) != 1 || rs[0] != pauseReasonDnd {
			t.Fatalf("QUNS 失败时仍应命中「勿扰」，得到 %v", rs)
		}
	})
}

// 两条系统状态开关互不相关：关掉「全屏或演示」不影响「请勿打扰」。
// QUNS=4 现在也归这条开关，正好钉住「演示设置合并后仍然能被拦住」。
func TestSplitPauseSwitchesAreIndependent(t *testing.T) {
	a, s := newWatchApp(t, shortCountdown)
	// QUNS=4（演示设置）+ WNF=1（勿扰）；两拍确认
	st := windnd.BusyStatus{QunsSupported: true, Quns: 4, WnfSupported: true, QuietProfile: 1}
	a.dndApply(st, true, true)
	a.dndApply(st, true, true)

	rs := s.StateCopy().PauseReasons
	if !containsReason(rs, pauseReasonDnd) || !containsReason(rs, pauseReasonFullScr) {
		t.Fatalf("勿扰与全屏或演示都该命中，得到 %v", rs)
	}

	// 单独关掉「全屏或演示」：它的原因留在 PauseReasons 里（按策略保持暂停），
	// 但条件登记要变 false，勿扰那条不受影响。
	a.dndApply(st, true, false)
	if a.autoReasonActiveSnapshot()[pauseReasonFullScr] {
		t.Fatal("关掉全屏或演示开关后，它的条件不该再登记为成立")
	}
	if !a.autoReasonActiveSnapshot()[pauseReasonDnd] {
		t.Fatal("关掉全屏或演示不该影响勿扰这条的条件登记")
	}
	if !containsReason(s.StateCopy().PauseReasons, pauseReasonDnd) {
		t.Fatal("关掉全屏或演示不该把勿扰的暂停一起摘掉")
	}
}

func containsReason(rs []string, want string) bool {
	for _, r := range rs {
		if r == want {
			return true
		}
	}
	return false
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
