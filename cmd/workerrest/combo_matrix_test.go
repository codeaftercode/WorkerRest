package main

import (
	"fmt"
	"strings"
	"testing"

	"workerrest/internal/model"
	"workerrest/internal/scheduler"
)

// 组合条件专项（2026-11-23 全量审计）。
//
// 单点行为各自都有测试，这里专门盯**同时成立**的情况：七条自动暂停原因任意组合、
// 忽略表与别的原因并存、定时暂停到期被多条原因同时挡住、询问窗与手动暂停撞在一起。
// 判据一律是状态机不变量（Paused ⇔ reasons 非空、PausedAt / PauseDeadline 只在暂停态
// 有意义、原因不重复），不是某一处日志文案 —— 这样新增一条原因时会自动被覆盖到。

// TestPauseReasonSubsetMatrix 七条自动原因的**全部 128 种组合**：任意子集挂上去，
// 再按逆序逐条摘掉，每一步都校验状态机不变量。
//
// 为什么值得枚举：这些原因来自五条互不通信的监视链路（勿扰 / 投屏两条 / 全屏两条 /
// 应用占用 / 空闲），真实场景里两三条同时成立是常态（开会时既勿扰又投屏、全屏看视频
// 又接着投影）。只要某一条的 resume 写了“直接恢复”而不是“摘掉自己那条”，就会出现
// “别人还挂着它却恢复提醒”的漏窗 —— 而这种漏窗只在组合下暴露。
func TestPauseReasonSubsetMatrix(t *testing.T) {
	reasons := append([]string{}, autoPauseReasons...)
	if len(reasons) != 7 {
		t.Fatalf("自动原因应为 7 条，实际 %d 条（新增原因请同步本矩阵的语义）", len(reasons))
	}
	for mask := 0; mask < 1<<len(reasons); mask++ {
		var want []string
		for i, r := range reasons {
			if mask&(1<<i) != 0 {
				want = append(want, r)
			}
		}
		name := "空集"
		if len(want) > 0 {
			name = strings.Join(want, "+")
		}
		t.Run(name, func(t *testing.T) {
			a, s := newWatchApp(t, shortCountdown)
			_ = a
			for _, r := range want {
				s.Pause(r)
			}
			assertPauseInvariants(t, s.StateCopy(), want)

			// 逆序逐条摘：每摘一条，剩下的必须仍然 suspend 住
			rest := append([]string{}, want...)
			for i := len(want) - 1; i >= 0; i-- {
				s.Resume(want[i])
				rest = rest[:len(rest)-1]
				assertPauseInvariants(t, s.StateCopy(), rest)
			}
			// 全摘干净：暂停起点与到期时刻都必须归零，否则下一段会带着上一段的旧值
			st := s.StateCopy()
			if st.Paused || st.PausedAt != 0 || st.PauseDeadline != 0 {
				t.Fatalf("全部原因摘除后应完全干净，得到 paused=%v pausedAt=%d deadline=%d",
					st.Paused, st.PausedAt, st.PauseDeadline)
			}
		})
	}
}

// assertPauseInvariants 校验一组“应当生效”的原因对应的状态机形状。
func assertPauseInvariants(t *testing.T, st model.SchedulerState, want []string) {
	t.Helper()
	if st.Paused != (len(want) > 0) {
		t.Fatalf("Paused=%v，但应有 %d 条原因（%v）", st.Paused, len(want), st.PauseReasons)
	}
	if len(st.PauseReasons) != len(want) {
		t.Fatalf("原因数 %d ≠ 期望 %d：%v", len(st.PauseReasons), len(want), st.PauseReasons)
	}
	seen := map[string]bool{}
	for _, r := range st.PauseReasons {
		if seen[r] {
			t.Fatalf("原因重复：%v", st.PauseReasons)
		}
		seen[r] = true
	}
	for _, r := range want {
		if !seen[r] {
			t.Fatalf("缺少原因 %q：%v", r, st.PauseReasons)
		}
	}
	if st.Paused && st.PausedAt == 0 {
		t.Fatal("暂停态必须有暂停起点（“已暂停多久”与统计都靠它）")
	}
	if !st.Paused && st.PausedAt != 0 {
		t.Fatalf("未暂停却留着暂停起点 %d", st.PausedAt)
	}
	// 到期时刻是 manual 的附属物：没有 manual 就不该有 deadline
	if st.PauseDeadline != 0 && !seen[scheduler.PauseReasonManual] {
		t.Fatalf("PauseDeadline=%d 却不含 manual：%v", st.PauseDeadline, st.PauseReasons)
	}
}

// TestTimedPauseExpiryHoldMatrix 定时暂停到期 × 七条自动原因**各自单独**挡着：
// 每一种组合都必须“保持暂停 + 记下阻挡原因”，摘掉那条原因后立刻恢复。
//
// 这是“到点了但此刻恢复会打扰”的核心组合。少一条进 pauseHoldReasons，到点就会在
// 投影仪上弹提醒；多一条（例如把 non-existent 的键也算进去），到期就永远不恢复。
func TestTimedPauseExpiryHoldMatrix(t *testing.T) {
	for _, reason := range autoPauseReasons {
		t.Run(reason, func(t *testing.T) {
			a, s := newWatchApp(t, shortCountdown)
			// 一段已到期的定时手动暂停
			s.PauseUntil(nowMs() - 1)
			if !s.HasPauseReason(scheduler.PauseReasonManual) {
				t.Fatal("PauseUntil 未挂上 manual")
			}
			// 再叠上这条自动原因（空闲不走 applyAutoPause，直接落原因）
			if reason == scheduler.PauseReasonIdle {
				s.Pause(scheduler.PauseReasonIdle)
			} else {
				a.setAutoReasonActive(reason, true)
				s.Pause(reason)
			}

			a.pauseExpiryTick()
			if !s.IsPaused() {
				t.Fatalf("到期时 %q 仍成立，不该恢复", reason)
			}
			if hold := a.pauseHoldSnapshot(); len(hold) != 1 {
				t.Fatalf("应记下 1 条阻挡原因，得到 %v", hold)
			}

			// 条件结束：下一拍就该放行
			if reason == scheduler.PauseReasonIdle {
				s.Resume(scheduler.PauseReasonIdle)
			} else {
				a.setAutoReasonActive(reason, false)
				s.Resume(reason)
			}
			a.pauseExpiryTick()
			if s.IsPaused() {
				t.Fatalf("%q 已结束，到期暂停该恢复，仍挂着 %v", reason, s.StateCopy().PauseReasons)
			}
			if hold := a.pauseHoldSnapshot(); len(hold) != 0 {
				t.Fatalf("恢复后阻挡原因应清空，得到 %v", hold)
			}
		})
	}
}

// TestIgnoredReasonIsolatedFromOthers 忽略表只对**那一条**原因生效：
// 用户为勿扰点过恢复之后，别的自动原因（这里用复制投影）该暂停还是暂停；
// 而勿扰的条件消失时忽略自动解除，下次勿扰再开照常暂停。
//
// 反过来的错误写法是把“用户手动恢复过”当成全局静音 —— 会静音一整场会议里所有后续判定，
// 用户只会觉得“自动暂停时好时坏”。
func TestIgnoredReasonIsolatedFromOthers(t *testing.T) {
	a, s := newWatchApp(t, shortCountdown)
	enter := func(reason string) {
		a.applyAutoPause(true, reason, loggedOf(a, reason), "", "")
		a.applyAutoPause(true, reason, loggedOf(a, reason), "", "")
	}

	// ① 勿扰成立 → 暂停 → 用户点恢复：记忽略并恢复
	enter(pauseReasonDnd)
	if !s.IsPaused() {
		t.Fatal("勿扰成立应暂停")
	}
	a.schedPauseToggle()
	if s.IsPaused() || !a.isAutoPauseIgnored(pauseReasonDnd) {
		t.Fatalf("手动恢复后应不暂停且记下忽略，得到 paused=%v ignored=%v",
			s.IsPaused(), a.isAutoPauseIgnored(pauseReasonDnd))
	}

	// ② 另一条原因成立：照常暂停（忽略表不牵连）
	enter(pauseReasonCastMirror)
	st := s.StateCopy()
	if !st.Paused || len(st.PauseReasons) != 1 || st.PauseReasons[0] != pauseReasonCastMirror {
		t.Fatalf("复制投影应独立生效，得到 %+v", st.PauseReasons)
	}

	// ③ 勿扰条件消失：忽略解除（下次勿扰照常暂停），但复制投影还挂着，仍暂停
	a.applyAutoPause(false, pauseReasonDnd, loggedOf(a, pauseReasonDnd), "", "")
	if a.isAutoPauseIgnored(pauseReasonDnd) {
		t.Fatal("条件消失后忽略应解除")
	}
	if !s.IsPaused() {
		t.Fatal("复制投影仍成立，不该恢复")
	}

	// ④ 勿扰再次成立：与复制投影并存（两条原因，互不顶替）
	enter(pauseReasonDnd)
	st = s.StateCopy()
	if len(st.PauseReasons) != 2 {
		t.Fatalf("两条原因应并存，得到 %+v", st.PauseReasons)
	}
}

// loggedOf 取某个原因对应的“上一拍是否成立”标记字段。
// 只在测试里用：生产代码里每个支路各持一个字段（互不影响），这里按名字映射一下。
func loggedOf(a *App, reason string) *bool {
	switch reason {
	case pauseReasonDnd:
		return &a.dndActiveLogged
	case pauseReasonFullScr:
		return &a.fsActiveLogged
	case pauseReasonFullScrBusy:
		return &a.fsBusyLogged
	case pauseReasonCastMirror:
		return &a.castMirrorLogged
	case pauseReasonCastWireless:
		return &a.castWirelessLogged
	}
	panic(fmt.Sprintf("原因 %q 没有独立的 logged 字段（新增原因时补一条）", reason))
}

// TestManualPauseDismissesAskWindow 询问窗开着时用户手动暂停：问题已经没有意义
// （都已经暂停了），窗口必须立刻收掉，而不是把 12 秒倒计时走完再叠一条“非独占全屏”。
//
// 这是 2026-11-23 全量审计发现的一处组合不合理：以前只有“立即休息”会收它。
func TestManualPauseDismissesAskWindow(t *testing.T) {
	a, s := newWatchApp(t, shortCountdown)
	// 造一扇“已在等答复”的询问（没有原生窗口，pending 就是全部状态）
	a.fsAskMu.Lock()
	a.fsAsk = fsAskState{pending: true, seq: 1, exe: "game.exe", label: "游戏"}
	a.fsAskMu.Unlock()

	a.schedPauseToggle()

	if a.BreakHoldRequested() {
		t.Fatal("手动暂停后询问窗应被收掉（问题已无意义）")
	}
	if !s.HasPauseReason(scheduler.PauseReasonManual) {
		t.Fatalf("应进入手动暂停，得到 %+v", s.StateCopy().PauseReasons)
	}
	if s.HasPauseReason(pauseReasonFullScrBusy) {
		t.Fatal("不该因为询问窗而多挂一条“非独占全屏”")
	}
}

// shortCountdownRemind 与 shortCountdown 同，但这次休息配的是“正常提醒”（暂停时照常弹）。
const shortCountdownRemind = `{"short":{"enabled":true,"mode":"countdown",` +
	`"intervalMs":600000,"durationMs":20000,"notifyBeforeMs":10000,"postponeMs":120000,` +
	`"pauseAction":"remind","slots":[]}}`

// TestTimedPauseExpiryKeepsRemindBreakRunning 三维交叉：**休息中（配“正常提醒”）× 自动暂停 ×
// 定时暂停到期**。
//
// 三方各有一条规则，叠起来最容易互相打脸：
//   - 自动暂停闯进来时配“正常提醒”的休息不收窗，标 BreakWhilePaused 让它按真实时钟走完；
//   - 定时暂停到期时若自动原因还成立，要保持暂停（不能到点就在屏幕上弹）；
//   - 恢复时 BreakWhilePaused 的那次休息**不平移**剩余时间（它一直是真实时钟在走）。
//
// 错一处就会露馅：忘了保留 → 用户的休息被吞；到期没挡住 → 提醒弹在会里；恢复时又平移
// 一次 → 白送一段休息（20 秒的休息变成“暂停多久就多长”）。
func TestTimedPauseExpiryKeepsRemindBreakRunning(t *testing.T) {
	a, s := newWatchApp(t, shortCountdownRemind)
	s.BreakNow()
	before := s.StateCopy()
	if before.Phase != "break" {
		t.Fatalf("前置：应已进入休息，得到 %q", before.Phase)
	}
	breakEndsAt := before.BreakEndsAt

	// ① 自动暂停（勿扰）闯进来：配“正常提醒”的休息窗留着、时钟不冻结
	a.setAutoReasonActive(pauseReasonDnd, true)
	s.Pause(pauseReasonDnd)
	mid := s.StateCopy()
	if mid.Phase != "break" || !mid.BreakWhilePaused {
		t.Fatalf("“正常提醒”的休息不该被收掉，应标 BreakWhilePaused：phase=%q bp=%v",
			mid.Phase, mid.BreakWhilePaused)
	}
	if mid.BreakEndsAt != breakEndsAt {
		t.Fatalf("按真实时钟走完：结束时刻不该被改动，was=%d now=%d", breakEndsAt, mid.BreakEndsAt)
	}

	// ② 再叠一段已到期的定时暂停：到期这一拍被勿扰挡住，不能恢复
	a.schedPauseFor(30)
	s.PauseUntil(nowMs() - 1)
	a.pauseExpiryTick()
	if !s.IsPaused() {
		t.Fatal("勿扰仍成立，到期不该恢复（否则提醒会弹在会里）")
	}
	if hold := a.pauseHoldSnapshot(); len(hold) != 1 || hold[0] != "勿扰" {
		t.Fatalf("应点名“勿扰”，得到 %v", hold)
	}
	if st := s.StateCopy(); st.BreakEndsAt != breakEndsAt {
		t.Fatalf("暂停期间这次休息按真实时钟走，不该冻结或平移：was=%d now=%d",
			breakEndsAt, st.BreakEndsAt)
	}

	// ③ 勿扰结束 → 下一拍放行，摘掉 manual 真正的恢复
	a.setAutoReasonActive(pauseReasonDnd, false)
	s.Resume(pauseReasonDnd)
	a.pauseExpiryTick()
	st := s.StateCopy()
	if st.Paused {
		t.Fatalf("条件都结束了该恢复，仍挂着 %v", st.PauseReasons)
	}
	if st.Phase != "break" {
		t.Fatalf("这次休息还没走完，恢复不该顺手结束它，得到 %q", st.Phase)
	}
	if st.BreakEndsAt != breakEndsAt {
		t.Fatalf("BreakWhilePaused 的那次休息恢复时不平移（它是真实时钟走完的），was=%d now=%d",
			breakEndsAt, st.BreakEndsAt)
	}
	if st.BreakWhilePaused {
		t.Fatal("恢复后必须回收 BreakWhilePaused，否则下一次暂停会误判它是暂停中发起的")
	}
	if st.PauseDeadline != 0 {
		t.Fatalf("恢复后截止时刻应清零，得到 %d", st.PauseDeadline)
	}
}

// TestAutoResumeOnClearDoesNotEndTimedPause 空闲“条件消失即自动恢复”不能把**用户设的**
// 那段定时暂停一起结束掉。
//
// 场景：开会前点了“暂停 30 分钟”，人离开 → 空闲那条也挂上来（两条原因并存）；人回来 →
// 空闲按策略自动摘掉自己那一条。此时如果哪一方写成“恢复了”，用户回到座位就会立刻被
// 提醒 —— 而他明明说好了 30 分钟内别打扰。
func TestAutoResumeOnClearDoesNotEndTimedPause(t *testing.T) {
	a, s := newWatchApp(t, shortCountdown)

	a.schedPauseFor(30)
	first := s.StateCopy()
	if !first.Paused || first.PauseDeadline == 0 {
		t.Fatalf("前置：应进入定时暂停，得到 paused=%v deadline=%d",
			first.Paused, first.PauseDeadline)
	}

	// 人走开：空闲支路直接落原因（不走 applyAutoPause）
	s.Pause(scheduler.PauseReasonIdle)
	if n := len(s.StateCopy().PauseReasons); n != 2 {
		t.Fatalf("两条原因应并存，得到 %d 条：%v", n, s.StateCopy().PauseReasons)
	}

	// 人回来：空闲自动恢复（autoResumeOnClear=true）
	s.ResumeQuiet(scheduler.PauseReasonIdle)

	st := s.StateCopy()
	if !st.Paused || !s.HasPauseReason(scheduler.PauseReasonManual) {
		t.Fatalf("定时暂停不该被空闲的自动恢复带走，得到 %+v", st.PauseReasons)
	}
	if st.PauseDeadline != first.PauseDeadline {
		t.Fatalf("截止时刻不该被改写（还是原来那段），was=%d now=%d",
			first.PauseDeadline, st.PauseDeadline)
	}

	// 到点仍然能正常到期恢复（说明这段定时暂停没被“半摘”成残废）
	s.PauseUntil(nowMs() - 1)
	a.pauseExpiryTick()
	if s.IsPaused() {
		t.Fatalf("到点且无条件应恢复，仍挂着 %v", s.StateCopy().PauseReasons)
	}
}

// TestExpiryGateTreatsIgnoredConditionAsBlocking 忽略表与到期闸门问的是**两个不同的问题**：
// 前者答“别因为它开始暂停”，后者答“此刻恢复会不会打扰”。它们不共享答案，也不能互相
// 覆盖 —— 这条用例把这个分工钉住，并验“条件消失后自己会好”。
//
// 走一遍用户实际会走的路：勿扰开着 → 自动暂停 → 用户点“恢复休息”（记忽略，提醒照常）
// → 用户接着点“暂停 30 分钟”（他确实要静一会儿）→ 到点这一刻勿扰还开着。
// 结果是保持暂停、托盘点名“等勿扰结束”；关掉勿扰之后下一拍自动恢复，不需要再点一次。
func TestExpiryGateTreatsIgnoredConditionAsBlocking(t *testing.T) {
	a, s := newWatchApp(t, shortCountdown)

	a.setAutoReasonActive(pauseReasonDnd, true)
	s.Pause(pauseReasonDnd)
	if !s.IsPaused() {
		t.Fatal("前置：勿扰成立应暂停")
	}

	// 用户点“恢复休息”：记忽略并摘原因（提醒恢复运行）
	a.schedPauseToggle()
	if s.IsPaused() || !a.isAutoPauseIgnored(pauseReasonDnd) {
		t.Fatalf("前置：应已忽略勿扰并恢复，得到 paused=%v ignored=%v",
			s.IsPaused(), a.isAutoPauseIgnored(pauseReasonDnd))
	}

	// 接着点名要静 30 分钟，并把到期时刻拨到过去（等价于 30 分钟过去了）
	a.schedPauseFor(30)
	s.PauseUntil(nowMs() - 1)

	a.pauseExpiryTick()
	if !s.IsPaused() {
		t.Fatal("勿扰条件此刻仍成立，到期闸门该挡住（忽略表管的是“别开始暂停”）")
	}
	if hold := a.pauseHoldSnapshot(); len(hold) != 1 || hold[0] != "勿扰" {
		t.Fatalf("应点名“勿扰”，得到 %v", hold)
	}

	// 条件消失：下一拍自己好，不需要用户再点一次
	a.setAutoReasonActive(pauseReasonDnd, false)
	a.pauseExpiryTick()
	if s.IsPaused() {
		t.Fatalf("勿扰结束后应自动恢复，仍挂着 %v", s.StateCopy().PauseReasons)
	}
	if hold := a.pauseHoldSnapshot(); len(hold) != 0 {
		t.Fatalf("恢复后阻挡标记应清空，得到 %v", hold)
	}
}

// TestPreBreakPauseActionIdempotentWhilePaused 提前提示窗上的「暂停计时」在暂停中必须
// 无事发生：它走的是 pauseToggle，已经在暂停里再按一下就是**恢复**，还会把勿扰 / 投屏
// 那些自动原因记成“用户已忽略”。
//
// 两层防线各有分工：页面在暂停态把这个按钮藏起来（prebreak_page_probe.js 钉着），
// 这里钉后端 —— 旧窗口、自动化调用、键盘都绕得过页面那一层。
func TestPreBreakPauseActionIdempotentWhilePaused(t *testing.T) {
	a, s := newWatchApp(t, shortCountdown)
	a.setAutoReasonActive(pauseReasonDnd, true)
	s.Pause(pauseReasonDnd)

	if err := a.preBreakAct("pause"); err != nil {
		t.Fatalf("暂停中的“暂停计时”应是空操作而不是报错：%v", err)
	}

	st := s.StateCopy()
	if !st.Paused || len(st.PauseReasons) != 1 || st.PauseReasons[0] != pauseReasonDnd {
		t.Fatalf("不该因为按了“暂停计时”而恢复：%+v", st.PauseReasons)
	}
	if a.isAutoPauseIgnored(pauseReasonDnd) {
		t.Fatal("更不该顺手把勿扰记成“已忽略” —— 那会让整场会议的自动暂停失效")
	}

	// 未暂停时它照常生效（幂等不等于失效）
	a2, s2 := newWatchApp(t, shortCountdown)
	if err := a2.preBreakAct("pause"); err != nil {
		t.Fatalf("未暂停时“暂停计时”应正常生效：%v", err)
	}
	if !s2.HasPauseReason(scheduler.PauseReasonManual) {
		t.Fatalf("未暂停时应进入手动暂停，得到 %+v", s2.StateCopy().PauseReasons)
	}
}
