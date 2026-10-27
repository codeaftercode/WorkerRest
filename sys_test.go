package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows/registry"
)

// 系统能力（空闲 / 勿扰 / 进程 / 自启动）的回归。
//
// 这一层与 上一版侧没有对拍脚本 —— 那边是 koffi 调 DLL + reg.exe 子进程，两边
// 没有可比较的中间产物。所以：
//   - **纯逻辑**（空闲状态机、空闲判定、命令行拼装）在这里逐条钉死；
//   - **系统调用**（进程枚举、GetLastInputInfo、勿扰查询）在本机真跑，断言的是
//     「能读到且有合理结果」，不是某个固定值；
//   - **注册表**在**临时键**上做真实写入 / 读回 / 删除往返，绝不碰用户真实的 Run 键。

// ---------- 脚手架 ----------

// probeClock 假时钟 + 假 tick 源：GetLastInputInfo 返回的是「变化与否」，
// 这里用一个显式可改的整数完全等价地模拟它。
type probeClock struct {
	nowMs   int64
	tick    int64
	hasTick bool
}

func (p *probeClock) newIdle() *IdleState {
	return &IdleState{
		nowFn:       func() int64 { return p.nowMs },
		tickFn:      func() (int64, bool) { return p.tick, p.hasTick },
		lastInputAt: p.nowMs,
	}
}

// noBackLoop 把采样间隔拉到小时级：单测自己手动调 Sample 推进，后台 goroutine 别来抢
func noBackLoop(t *testing.T) {
	t.Helper()
	old := idleSampleInterval
	idleSampleInterval = time.Hour
	t.Cleanup(func() { idleSampleInterval = old })
}

// ---------- 空闲状态机 ----------

func TestIdleStateUnstartedIsZeroAndOnline(t *testing.T) {
	p := &probeClock{nowMs: 1_000_000, hasTick: true}
	s := p.newIdle()
	if got := s.IdleSinceMs(); got != 0 {
		t.Fatalf("未启动时 L 应为 0，得到 %d", got)
	}
	if !s.IsActive() {
		t.Fatal("未启动时应视为在线（否则冒烟机无人操作会被一路判成离开）")
	}
	if got := s.LastInputAtMs(); got != p.nowMs {
		t.Fatalf("未启动时起点应是现在，得到 %d", got)
	}
}

func TestIdleStateStartFailsWhenTickUnavailable(t *testing.T) {
	p := &probeClock{nowMs: 1_000_000, hasTick: false}
	s := p.newIdle()
	if s.Start() {
		t.Fatal("读不到系统 tick 时 Start 应返回 false（不能假装有活动）")
	}
	if got := s.IdleSinceMs(); got != 0 {
		t.Fatalf("启动失败后 L 仍应为 0，得到 %d", got)
	}
	if !s.IsActive() {
		t.Fatal("启动失败后应视为在线")
	}
	if s.Info().Supported {
		t.Fatal("启动失败后 Supported 应为 false")
	}
}

func TestIdleStateSamplingSemantics(t *testing.T) {
	noBackLoop(t)
	p := &probeClock{nowMs: 1_000_000, tick: 100, hasTick: true}
	s := p.newIdle()
	if !s.Start() {
		t.Fatal("假 tick 源可用时 Start 应成功")
	}
	defer s.Stop()

	// 5 分钟没有任何输入
	p.nowMs += 300_000
	if got := s.IdleSinceMs(); got != 300_000 {
		t.Fatalf("L 应等于墙钟推进量，得到 %d", got)
	}
	if got := s.LastInputAtMs(); got != 1_000_000 {
		t.Fatalf("起点应是最后一次输入时刻，得到 %d", got)
	}

	// 采样时 tick 没变 → 不算活动
	s.Sample()
	if s.IsActive() {
		t.Fatal("tick 没变不该算「有输入」")
	}
	if got := s.IdleSinceMs(); got != 300_000 {
		t.Fatalf("tick 没变时 L 应继续增长，得到 %d", got)
	}

	// tick 变了 → 记下墙钟，L 归零
	p.nowMs += 1_000
	p.tick = 200
	s.Sample()
	if got := s.IdleSinceMs(); got != 0 {
		t.Fatalf("检测到输入后 L 应归零，得到 %d", got)
	}
	if got := s.LastInputAtMs(); got != p.nowMs {
		t.Fatalf("起点应更新为本次采样时刻，得到 %d", got)
	}

	// 去抖：必须连续 3 次采样都有输入才算「人真的回来了」
	p.nowMs += 1_000
	p.tick = 300
	s.Sample()
	if s.IsActive() {
		t.Fatal("才 2 次连续输入不该算回来（去抖 3 秒）")
	}
	p.nowMs += 1_000
	p.tick = 400
	s.Sample()
	if !s.IsActive() {
		t.Fatal("连续 3 次输入应判为回来")
	}

	// 中途断一下 → 去抖计数清零
	p.nowMs += 1_000
	s.Sample() // tick 仍为 400，没变
	if s.IsActive() {
		t.Fatal("中途没输入应把去抖计数清零")
	}
}

func TestIdleStateResetBaselineAndForce(t *testing.T) {
	noBackLoop(t)
	p := &probeClock{nowMs: 1_000_000, tick: 100, hasTick: true}
	s := p.newIdle()
	if !s.Start() {
		t.Fatal("Start 应成功")
	}
	defer s.Stop()

	// 重置基准（休息窗口关闭时调用）：L 从 0 重新起算
	p.nowMs += 600_000
	if got := s.IdleSinceMs(); got != 600_000 {
		t.Fatalf("重置前 L 应为 600000，得到 %d", got)
	}
	s.ResetBaseline()
	if got := s.IdleSinceMs(); got != 0 {
		t.Fatalf("重置后 L 应为 0，得到 %d", got)
	}
	if got := s.LastInputAtMs(); got != p.nowMs {
		t.Fatalf("重置后起点应是现在，得到 %d", got)
	}
	if s.IsActive() {
		t.Fatal("重置会清掉去抖计数，此刻不该算「已恢复活动」")
	}

	// 强制值（冒烟 / 单测专用）
	s.ForceIdle(700_000)
	if got := s.IdleSinceMs(); got != 700_000 {
		t.Fatalf("强制后 L 应为 700000，得到 %d", got)
	}
	if s.IsActive() {
		t.Fatalf("强制 L=700000 时不该算活跃（阈值 %d）", idleActiveMs)
	}
	if got := s.LastInputAtMs(); got != p.nowMs-700_000 {
		t.Fatalf("强制态的起点应反推为 now-L，得到 %d", got)
	}
	s.ForceIdle(-1) // 解除
	if got := s.IdleSinceMs(); got != 0 {
		t.Fatalf("解除强制后应回到「刚重置过」的状态，得到 %d", got)
	}
}

// ---------- 空闲判定（纯函数） ----------

func TestDecideIdleStep(t *testing.T) {
	const thr = 300_000
	cases := []struct {
		name               string
		phase              string
		away               bool
		idleMs, threshold  int64
		busyPaused, active bool
		want               idleStep
	}{
		{"休息窗口显示期间一律不判", "break", false, 9_999_999, thr, false, true, idleStepNone},
		{"休息期间即使已在离开态也不判", "break", true, 9_999_999, thr, false, true, idleStepNone},
		{"手动/勿扰/应用占用时不判离开", "idle", false, 9_999_999, thr, true, false, idleStepNone},
		{"但忙时仍要继续检测「回来了」", "idle", true, 0, thr, true, true, idleStepBack},
		{"够阈值 → 判离开", "idle", false, thr, thr, false, false, idleStepAway},
		{"差一毫秒不判", "idle", false, thr - 1, thr, false, false, idleStepNone},
		{"已在离开态且仍空闲 → 不重复动作", "idle", true, 9_999_999, thr, false, false, idleStepNone},
		{"空闲回落但人还没「真的回来」→ 不判", "idle", true, 1_000, thr, false, false, idleStepNone},
		{"空闲回落且去抖达标 → 判回来", "idle", true, 1_000, thr, false, true, idleStepBack},
		{"有活动但没判过离开 → 不动作", "idle", false, 0, thr, false, true, idleStepNone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := decideIdleStep(c.phase, c.away, c.idleMs, c.threshold, c.busyPaused, c.active)
			if got != c.want {
				t.Fatalf("decideIdleStep = %v，期望 %v", got, c.want)
			}
		})
	}
}

// ---------- 监视链路（接到真实调度器上） ----------

// newWatchApp 造一个「配置 + 调度器 + 空闲状态机」的最小 App（不碰窗口与 Wails）
func newWatchApp(t *testing.T, cfgJSON string) (*App, *Scheduler) {
	t.Helper()
	// 统计落点钉进临时目录：idleWatchTick 会走 BeginAway / EndAway，后者会标脏并在 2 秒后
	// 落盘 —— 不隔离的话测试会去建目录、写用户真实的 stats.json
	t.Setenv("WR_STATS_PATH", filepath.Join(t.TempDir(), "stats.json"))
	s, host := newTestSched(t, cfgJSON)
	s.replan()
	return &App{cfg: host.cfg, sched: s, idle: NewIdleState()}, s
}

func TestIdleWatchPausesThenResumesAndReanchors(t *testing.T) {
	a, s := newWatchApp(t, shortCountdown) // 默认 idlePauseMin=5（最小 1 分钟）
	firstNext := s.stateCopy().NextAt

	// ① 未达阈值：什么都不做
	a.idle.ForceIdle(60_000)
	if away := a.idleWatchTick(false); away {
		t.Fatal("1 分钟 < 5 分钟阈值，不该判离开")
	}
	if st := s.stateCopy(); st.Paused || len(st.PauseReasons) != 0 {
		t.Fatalf("未达阈值不该暂停，得到 %+v", st.PauseReasons)
	}

	// ② 达阈值：暂停，且暂停原因恰好是一条 idle
	a.idle.ForceIdle(6 * 60_000)
	if away := a.idleWatchTick(false); !away {
		t.Fatal("达到 6 分钟应判离开")
	}
	st := s.stateCopy()
	if !st.Paused || len(st.PauseReasons) != 1 || st.PauseReasons[0] != pauseReasonIdle {
		t.Fatalf("应恰好因 idle 暂停，得到 paused=%v reasons=%v", st.Paused, st.PauseReasons)
	}

	// ③ 已在离开态：重复推进不该叠加暂停原因
	a.idleWatchTick(true)
	if n := len(s.stateCopy().PauseReasons); n != 1 {
		t.Fatalf("重复推进不该叠加暂停原因，得到 %d 条", n)
	}

	// ④ 键鼠活动回来：恢复 + 重锚内置倒计时
	a.idle.ForceIdle(0) // IsActive（0 < 3000 去抖阈值）
	if away := a.idleWatchTick(true); away {
		t.Fatal("有活动应判回来")
	}
	st = s.stateCopy()
	if st.Paused || len(st.PauseReasons) != 0 {
		t.Fatalf("回来应清空暂停，得到 paused=%v reasons=%v", st.Paused, st.PauseReasons)
	}
	if st.Phase != "idle" {
		t.Fatalf("回来应处于 idle，得到 %q", st.Phase)
	}
	// 重锚 = 内置倒计时从「现在」重新起算（阈值 10 分钟）
	want := nowMs() + 600_000
	if diff := st.NextAt - want; diff < -2000 || diff > 2000 {
		t.Fatalf("重锚后应≈现在+10 分钟，want=%d got=%d（原值 %d）", want, st.NextAt, firstNext)
	}
}

func TestIdleWatchSkippedWhileBreakShowing(t *testing.T) {
	a, s := newWatchApp(t, shortCountdown)
	s.breakNow() // phase=break
	if got := s.currentPhase(); got != "break" {
		t.Fatalf("前置条件不成立：phase=%q", got)
	}
	a.idle.ForceIdle(9_999_999)
	if away := a.idleWatchTick(false); away {
		t.Fatal("休息窗口显示期间不该判离开")
	}
	if st := s.stateCopy(); st.Paused {
		t.Fatalf("休息期间不该被空闲挂起（否则休息窗口永远不会自动关闭），reasons=%v", st.PauseReasons)
	}
}

// 休息窗口关闭（break-end 的唯一真实调用点 closeBreakWindows）必须清零空闲基准：
// 否则「刚休息完、还没动鼠标」进入 idle 后，第一次空闲判定会立刻把状态判成离开并暂停。
func TestIdleResetBaselineOnBreakEnd(t *testing.T) {
	a, _ := newWatchApp(t, shortCountdown)
	if a.idle == nil {
		t.Fatal("前置：newWatchApp 应构造出 idle")
	}
	// 休息期间用户一直没动鼠标 → 空闲时长被累计得很大
	a.idle.ForceIdle(9_999_999)
	if ms := a.idle.IdleSinceMs(); ms < 60_000 {
		t.Fatalf("前置：休息期间 L 应已累计，得到 %d", ms)
	}
	// closeBreakWindows 是 finishBreak（休息结束）唯一调用点，直接验收它
	a.closeBreakWindows()
	if ms := a.idle.IdleSinceMs(); ms >= 60_000 {
		t.Fatalf("休息结束后空闲基准没清零：L=%d ms（应回到 ~0，否则进入 idle 会立刻判离开）", ms)
	}
	// 进入 idle 后的第一次判定不应误判离开
	if away := a.idleWatchTick(false); away {
		t.Fatal("休息结束后第一次空闲判定不该判离开（基准应已清零）")
	}
}

func TestAppPauseWatchRealProcessHitAndRelease(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Skipf("取不到本进程路径：%v", err)
	}
	exe := filepath.Base(self)
	cfg := `{"appPause":{"enabled":true,"items":[{"id":"a1","exe":` + jsonStr(exe) +
		`,"name":"","enabled":true}]},` + strings.TrimPrefix(shortCountdown, "{")

	a, s := newWatchApp(t, cfg)

	// 本测试进程一定在运行 → 必然命中
	a.appPauseWatchTick()
	st := s.stateCopy()
	if len(st.PauseReasons) != 1 || st.PauseReasons[0] != pauseReasonAppPause {
		t.Fatalf("应因「%s」暂停，得到 %v", pauseReasonAppPause, st.PauseReasons)
	}

	// 命中期间视为「用户仍在用电脑」：即使空闲到天荒地老也不判离开
	a.idle.ForceIdle(24 * 3_600_000)
	if away := a.idleWatchTick(false); away {
		t.Fatal("忙原因暂停期间不该判空闲离开")
	}
	if n := len(s.stateCopy().PauseReasons); n != 1 {
		t.Fatalf("不该叠加 idle 暂停，得到 %v", s.stateCopy().PauseReasons)
	}

	// 关掉总开关 → 下一跳自动恢复（配置改动即时生效，不需要重建监视）
	a.mu.Lock()
	a.cfg = testSettings(t, shortCountdown)
	a.mu.Unlock()
	a.appPauseWatchTick()
	if st := s.stateCopy(); st.Paused || len(st.PauseReasons) != 0 {
		t.Fatalf("目标应用规则关掉后应恢复，得到 paused=%v reasons=%v", st.Paused, st.PauseReasons)
	}
}

// appPauseCfg 拼一份「命中本测试进程」的 appPause 配置（enabledItem 控制条目开关，
// 用来在单测里模拟目标应用退出 / 再次运行——条目关了 = 进程不在了，走的同一条
// hit=false 路径）
func appPauseCfg(t *testing.T, enabledItem bool) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Skipf("取不到本进程路径：%v", err)
	}
	return `{"appPause":{"enabled":true,"items":[{"id":"a1","exe":` + jsonStr(filepath.Base(self)) +
		`,"name":"","enabled":` + map[bool]string{true: "true", false: "false"}[enabledItem] + `}]},` +
		strings.TrimPrefix(shortCountdown, "{")
}

func TestAppPauseWatchAutoResumesWhenProcessExits(t *testing.T) {
	a, s := newWatchApp(t, appPauseCfg(t, true))

	a.appPauseWatchTick()
	if st := s.stateCopy(); !st.Paused || len(st.PauseReasons) != 1 || st.PauseReasons[0] != pauseReasonAppPause {
		t.Fatalf("目标应用运行时应自动暂停，得到 paused=%v reasons=%v", st.Paused, st.PauseReasons)
	}

	// 进程退出（条目关掉 = hit=false）→ 下一跳必须自动恢复，无需任何手动操作
	a.mu.Lock()
	a.cfg = testSettings(t, appPauseCfg(t, false))
	a.mu.Unlock()
	a.appPauseWatchTick()
	if st := s.stateCopy(); st.Paused || len(st.PauseReasons) != 0 {
		t.Fatalf("目标应用退出后应自动恢复，得到 paused=%v reasons=%v", st.Paused, st.PauseReasons)
	}
}

func TestAppPauseManualResumeIgnoresUntilExitOrManualPause(t *testing.T) {
	a, s := newWatchApp(t, appPauseCfg(t, true))

	// ① 自动暂停生效
	a.appPauseWatchTick()
	if st := s.stateCopy(); !st.Paused || st.PauseReasons[0] != pauseReasonAppPause {
		t.Fatalf("前置：应因应用占用暂停，得到 %v", st.PauseReasons)
	}

	// ② 用户点「恢复休息」→ 立即恢复 + 进入忽略会话
	a.schedPauseToggle()
	if st := s.stateCopy(); st.Paused || len(st.PauseReasons) != 0 {
		t.Fatalf("手动恢复应立即生效，得到 paused=%v reasons=%v", st.Paused, st.PauseReasons)
	}
	if !a.isAppPauseIgnored() {
		t.Fatal("手动恢复后应进入忽略会话")
	}

	// ③ 应用还在跑：不再被自动暂停（这正是临时忽略的意义）
	a.appPauseWatchTick()
	a.appPauseWatchTick()
	if st := s.stateCopy(); st.Paused || len(st.PauseReasons) != 0 {
		t.Fatalf("忽略会话期间不该重新暂停，得到 paused=%v reasons=%v", st.Paused, st.PauseReasons)
	}

	// ④ 用户手动暂停 → 忽略作废，回到常规判定（应用还在跑就照常叠加应用占用）
	a.schedPauseToggle()
	if st := s.stateCopy(); !st.Paused {
		t.Fatal("手动暂停应生效")
	}
	if a.isAppPauseIgnored() {
		t.Fatal("手动暂停应作废忽略会话")
	}
	a.appPauseWatchTick()
	st := s.stateCopy()
	if len(st.PauseReasons) != 2 {
		t.Fatalf("忽略作废后应用仍在跑，应叠加应用占用，得到 %v", st.PauseReasons)
	}

	// ⑤ 此时再点恢复：摘掉应用占用 + 手动暂停两条，立即恢复并再次进入忽略
	a.schedPauseToggle()
	if st := s.stateCopy(); st.Paused || len(st.PauseReasons) != 0 {
		t.Fatalf("应彻底恢复，得到 paused=%v reasons=%v", st.Paused, st.PauseReasons)
	}

	// ⑥ 应用退出 → 忽略解除；应用再次运行 → 照常自动暂停（下个会话）
	a.mu.Lock()
	a.cfg = testSettings(t, appPauseCfg(t, false))
	a.mu.Unlock()
	a.appPauseWatchTick()
	if a.isAppPauseIgnored() {
		t.Fatal("应用退出后忽略应解除")
	}
	a.mu.Lock()
	a.cfg = testSettings(t, appPauseCfg(t, true))
	a.mu.Unlock()
	a.appPauseWatchTick()
	if st := s.stateCopy(); !st.Paused || len(st.PauseReasons) != 1 || st.PauseReasons[0] != pauseReasonAppPause {
		t.Fatalf("下次应用再运行应重新自动暂停，得到 paused=%v reasons=%v", st.Paused, st.PauseReasons)
	}
}

// jsonStr 把字符串编成 JSON 字面量（测试里要拼很长的路径）
func jsonStr(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}

func TestDndWatchIsIdempotent(t *testing.T) {
	a, s := newWatchApp(t, shortCountdown)
	a.dndWatchTick()
	a.dndWatchTick()
	st := s.stateCopy()
	// 本机此刻到底开没开勿扰不确定，但必须满足：没开 → 一条暂停原因都不留
	live := GetDndStatus()
	if live.Supported && live.Active {
		if len(st.PauseReasons) != 1 || st.PauseReasons[0] != pauseReasonDnd {
			t.Fatalf("勿扰开启时应恰好停在该原因上，得到 %v", st.PauseReasons)
		}
	} else if len(st.PauseReasons) != 0 {
		t.Fatalf("勿扰未开启时不该留下暂停原因，得到 %v", st.PauseReasons)
	}
}

// 全屏独占支路：只读写它自己那条暂停原因，进入 / 退出各打一行日志（边沿触发），
// 中间反复「仍然活跃」不重复暂停、也不产生第二条原因。
func TestApplyAutoPauseFullScreenEdgeOnly(t *testing.T) {
	a, s := newWatchApp(t, shortCountdown)

	// ① 不活跃：一条原因都不该留（首次 tick 的常态）
	a.applyAutoPause(false, pauseReasonFullScr, &a.fsActiveLogged, "in", "out")
	if st := s.stateCopy(); st.Paused || len(st.PauseReasons) != 0 {
		t.Fatalf("不活跃时不该暂停，得到 %v", st.PauseReasons)
	}
	if a.fsActiveLogged {
		t.Fatal("不活跃时不该留下「已记录」标记")
	}

	// ② 活跃：暂停原因恰好是「全屏独占」，且标记置位
	a.applyAutoPause(true, pauseReasonFullScr, &a.fsActiveLogged, "in", "out")
	st := s.stateCopy()
	if !st.Paused || len(st.PauseReasons) != 1 || st.PauseReasons[0] != pauseReasonFullScr {
		t.Fatalf("活跃时应恰好停在「全屏独占」上，得到 paused=%v %v", st.Paused, st.PauseReasons)
	}
	if !a.fsActiveLogged {
		t.Fatal("活跃时该置位「已记录」标记（否则每 3 秒刷一行日志）")
	}

	// ③ 仍然活跃：幂等，不能叠出第二条原因
	a.applyAutoPause(true, pauseReasonFullScr, &a.fsActiveLogged, "in", "out")
	if st := s.stateCopy(); len(st.PauseReasons) != 1 {
		t.Fatalf("重复判活跃不该叠加暂停原因，得到 %v", st.PauseReasons)
	}

	// ④ 退出：原因摘掉、恢复，且只摘自己那条
	a.applyAutoPause(false, pauseReasonFullScr, &a.fsActiveLogged, "in", "out")
	st = s.stateCopy()
	if st.Paused || len(st.PauseReasons) != 0 {
		t.Fatalf("退出后应恢复提醒，得到 paused=%v %v", st.Paused, st.PauseReasons)
	}
	if a.fsActiveLogged {
		t.Fatal("退出后「已记录」标记应清零")
	}
}

// ---------- 命令行拼装 ----------

func TestBuildRunCommandLine(t *testing.T) {
	cases := []struct {
		name string
		exe  string
		args []string
		want string
	}{
		{"无空格不加引号", `C:\wr\chibingren.exe`, []string{autostartFlag}, `C:\wr\chibingren.exe --autostart`},
		{"含空格加引号", `C:\Program Files\吃饼人\chibingren.exe`, []string{autostartFlag}, `"C:\Program Files\吃饼人\chibingren.exe" --autostart`},
		{"没有参数也行", `C:\wr\chibingren.exe`, nil, `C:\wr\chibingren.exe`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := BuildRunCommandLine(c.exe, c.args); got != c.want {
				t.Fatalf("BuildRunCommandLine = %q，期望 %q", got, c.want)
			}
		})
	}
}

func TestNormalizeCommandLine(t *testing.T) {
	cases := []struct{ a, b string }{
		{`"C:\wr\a.exe" --autostart`, `C:\wr\a.exe  --autostart`},
		{`  C:\wr\a.exe   --autostart  `, `C:\wr\a.exe --autostart`},
		{`"C:\Program Files\a.exe"`, `C:\Program Files\a.exe`},
	}
	for _, c := range cases {
		if NormalizeCommandLine(c.a) != NormalizeCommandLine(c.b) {
			t.Fatalf("规范化后应相等：%q → %q，%q → %q",
				c.a, NormalizeCommandLine(c.a), c.b, NormalizeCommandLine(c.b))
		}
	}
	if NormalizeCommandLine(`a.exe --autostart`) == NormalizeCommandLine(`a.exe --other`) {
		t.Fatal("不同的参数不该被规范化成同一个值")
	}
}

// ---------- 注册表（临时键，绝不碰真实 Run 键） ----------

func TestAutostartRegistryRoundTrip(t *testing.T) {
	oldRun, oldApproved := runKeyPath, approvedKeyPath
	// 第一件事就是改路径 + 兜底清理：万一后面 panic，也不会落到真实 Run 键上
	runKeyPath = `Software\WorkerRestSelfTest\Run`
	approvedKeyPath = `Software\WorkerRestSelfTest\StartupApproved\Run`
	defer func() {
		runKeyPath, approvedKeyPath = oldRun, oldApproved
		cleanupSelfTestKeys()
	}()
	cleanupSelfTestKeys()

	if ReadRunEntry() != nil || RunEntryExists() {
		t.Fatal("临时键初始应是干净的")
	}

	cmd := BuildRunCommandLine(`C:\Program Files\吃饼人\chibingren.exe`, []string{autostartFlag})
	r := WriteRunEntry(cmd)
	if !r.OK {
		t.Fatalf("写入临时键失败（沙箱可能拦了注册表写）：%s", r.Detail)
	}
	if !RunEntryExists() {
		t.Fatal("写完读回应存在")
	}
	if got := ReadRunEntry(); got == nil || *got != cmd {
		t.Fatalf("读回的命令行应逐字一致，得到 %v", got)
	}
	if d := ApprovalDisabled(); d == nil || *d {
		t.Fatalf("刚写完、没有任何「启动已批准」记录时不该是禁用态，得到 %v", d)
	}

	// 重复写入 = 覆盖成最新命令行（换了目录 / 装了新版要能纠正）
	cmd2 := BuildRunCommandLine(`C:\wr\chibingren.exe`, []string{autostartFlag})
	if r := WriteRunEntry(cmd2); !r.OK {
		t.Fatalf("覆盖写入失败：%s", r.Detail)
	}
	if got := ReadRunEntry(); got == nil || *got != cmd2 {
		t.Fatalf("覆盖后应读到新命令行，得到 %v", got)
	}

	// 「启动已批准」里的禁用标记：首字节 03 = 用户在任务管理器里禁用过
	k, _, err := registry.CreateKey(registry.CURRENT_USER, approvedKeyPath, registry.SET_VALUE)
	if err != nil {
		t.Fatalf("建临时批准键失败：%v", err)
	}
	buf := make([]byte, 12)
	buf[0] = 0x03
	if err := k.SetBinaryValue(runValueName, buf); err != nil {
		_ = k.Close()
		t.Fatalf("写批准二进制值失败：%v", err)
	}
	_ = k.Close()
	if d := ApprovalDisabled(); d == nil || !*d {
		t.Fatalf("首字节 03 应读成「已禁用」，得到 %v", d)
	}
	// 02 = 启用
	buf[0] = 0x02
	if k2, err := registry.OpenKey(registry.CURRENT_USER, approvedKeyPath, registry.SET_VALUE); err == nil {
		_ = k2.SetBinaryValue(runValueName, buf)
		_ = k2.Close()
	}
	if d := ApprovalDisabled(); d == nil || *d {
		t.Fatalf("首字节 02 应读成「启用」，得到 %v", d)
	}
	if r := ClearApproval(); !r.OK {
		t.Fatalf("清「启动已批准」失败：%s", r.Detail)
	}
	if r := ClearApproval(); !r.OK || r.Detail != "absent" {
		t.Fatalf("再清一次应报 absent 且算成功，得到 %+v", r)
	}

	// 删除：本来就存在时必须真的删掉
	if r := RemoveRunEntry(); !r.OK {
		t.Fatalf("删除失败：%s", r.Detail)
	}
	if RunEntryExists() {
		t.Fatal("删完不该还在")
	}
	// 幂等：再删一次也算成功（目标状态已达成）
	if r := RemoveRunEntry(); !r.OK {
		t.Fatalf("重复删除应算成功：%+v", r)
	}
}

func TestAutostartValueNameMatchesPrevVersion(t *testing.T) {
	// 两个版本必须用同一个值名，否则注册表里会留下孤儿项、任务管理器显示两个条目
	if runValueName != "com.lwb.workerrest" {
		t.Fatalf("值名应为 com.lwb.workerrest，得到 %q", runValueName)
	}
	if autostartFlag != "--autostart" {
		t.Fatalf("标记参数应为 --autostart，得到 %q", autostartFlag)
	}
}

func TestAutostartTargetSkipsDevBuild(t *testing.T) {
	// 测试二进制必然落在 go-build 临时目录里 → 必须被判定为「不可注册」
	_, _, ok := AutostartTarget()
	if ok {
		t.Fatal("开发构建（落在临时 go-build 目录里的 exe）不该被注册成开机自启动，否则就是一条死路径")
	}
}

func cleanupSelfTestKeys() {
	for _, p := range []string{
		`Software\WorkerRestSelfTest\Run`,
		`Software\WorkerRestSelfTest\StartupApproved\Run`,
		`Software\WorkerRestSelfTest\StartupApproved`,
		`Software\WorkerRestSelfTest`,
	} {
		_ = registry.DeleteKey(registry.CURRENT_USER, p)
	}
}

// ---------- 真机系统调用 ----------

func TestListProcessNamesReal(t *testing.T) {
	r := ListProcessNames()
	if !r.Supported {
		t.Fatalf("本机进程枚举应可用：%s", r.Detail)
	}
	if len(r.Names) < 3 {
		t.Fatalf("进程数太少，不像真实枚举：%s %v", r.Detail, r.Names)
	}
	seen := make(map[string]bool, len(r.Names))
	for _, n := range r.Names {
		k := strings.ToLower(n)
		if seen[k] {
			t.Fatalf("结果应按大小写去重，出现重复项 %q", n)
		}
		seen[k] = true
	}
	selfPath, _ := os.Executable()
	self := strings.ToLower(filepath.Base(selfPath))
	if !seen[self] {
		t.Fatalf("列表里应有本测试进程 %q（否则枚举的是别的什么东西）", self)
	}
}

func TestIdleProbeReal(t *testing.T) {
	noBackLoop(t)
	s := NewIdleState()
	if !s.Start() {
		t.Fatalf("user32!GetLastInputInfo 应可用：%s", s.Info().Detail)
	}
	defer s.Stop()
	info := s.Info()
	if !info.Supported {
		t.Fatal("Supported 应为 true")
	}
	if info.IdleMs < 0 || info.IdleMs > 24*3_600_000 {
		t.Fatalf("空闲时长不合理：%d ms", info.IdleMs)
	}
}

func TestDndProbeShape(t *testing.T) {
	st := GetDndStatus()
	if st.Detail == "" {
		t.Fatal("detail 不该为空（无论支持与否都要有说明）")
	}
	if !st.Supported {
		t.Skipf("本机勿扰检测不可用：%s", st.Detail)
	}
	if st.Active {
		known := false
		for _, name := range winDndStates {
			if name == st.Detail {
				known = true
			}
		}
		if !known {
			t.Fatalf("判定为勿扰时 detail 应是已知勿扰状态之一，得到 %q", st.Detail)
		}
	}
}

// QUERY_USER_NOTIFICATION_STATE 取值 → 结论的映射表。这里钉死三条口径，改动前先看注释：
//   - 2 / 4 是勿扰（Win10 专注助手、Win11 勿扰、演示模式）
//   - 6（首次登录 / 大版本升级后的静默时间）**不算**勿扰（2026-10-27 用户明确要求）
//   - 3（全屏独占）走「全屏独占」开关，也不算勿扰
func TestQunsStateMapping(t *testing.T) {
	cases := []struct {
		name    string
		state   uint32
		dnd     bool
		fullScr bool
	}{
		{"锁屏/屏保", qunsNotPresent, false, false},
		{"勿扰/仅优先", qunsBusy, true, false},
		{"全屏独占", qunsRunningD3DFullScreen, false, true},
		{"演示模式", qunsPresentationMode, true, false},
		{"正常接收", qunsAcceptsNotifications, false, false},
		{"升级后静默时间", qunsQuietTime, false, false},
		{"商店应用在前台", qunsApp, false, false},
		{"未知取值", 99, false, false},
	}
	for _, c := range cases {
		d := dndStatusOf(c.state, true)
		if !d.Supported || d.Active != c.dnd {
			t.Fatalf("%s（state=%d）：勿扰结论应为 active=%v，得到 %+v", c.name, c.state, c.dnd, d)
		}
		if d.Detail == "" {
			t.Fatalf("%s（state=%d）：detail 不该为空", c.name, c.state)
		}
		fs := fullScreenStatusOf(c.state, true)
		if !fs.Supported || fs.Active != c.fullScr {
			t.Fatalf("%s（state=%d）：全屏独占结论应为 active=%v，得到 %+v", c.name, c.state, c.fullScr, fs)
		}
		if fs.Detail == "" {
			t.Fatalf("%s（state=%d）：全屏 detail 不该为空", c.name, c.state)
		}
	}
	// API 不可用：一律「不支持 + 有说明」，绝不报 active（否则在非 Windows / 调用失败时
	// 会凭空暂停休息）
	if d := dndStatusOf(0, false); d.Supported || d.Active || d.Detail == "" {
		t.Fatalf("API 不可用时勿扰结论应为 {supported:false, active:false, detail:非空}，得到 %+v", d)
	}
	if f := fullScreenStatusOf(0, false); f.Supported || f.Active || f.Detail == "" {
		t.Fatalf("API 不可用时全屏结论应为 {supported:false, active:false, detail:非空}，得到 %+v", f)
	}
}

// 两个开关的默认值：勿扰默认开（老行为不变）；全屏独占默认**关**（新增能力，
// 开启等于「游戏时不提醒、计时也停」，不能替用户决定）。
func TestAutoPauseFlagDefaults(t *testing.T) {
	s := &Settings{raw: map[string]any{}}
	if !s.DndPause() {
		t.Fatal("dndPause 缺省应为 true")
	}
	if s.FullScreenPause() {
		t.Fatal("fullscreenPause 缺省应为 false")
	}
	s = &Settings{raw: map[string]any{"dndPause": false, "fullscreenPause": true}}
	if s.DndPause() || !s.FullScreenPause() {
		t.Fatalf("配置值未生效：dnd=%v fullscreen=%v", s.DndPause(), s.FullScreenPause())
	}
}
