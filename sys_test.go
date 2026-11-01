package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"workerrest/internal/idle"
	"workerrest/internal/scheduler"
	"workerrest/internal/settings"
	"workerrest/internal/windnd"
)

// 系统能力（空闲 / 勿扰 / 进程 / 自启动）的回归。
//
// 这一层与 上一版侧没有对拍脚本 —— 那边是 koffi 调 DLL + reg.exe 子进程，两边
// 没有可比较的中间产物。所以：
//   - **纯逻辑**（空闲状态机、空闲判定、命令行拼装）在这里逐条钉死（IdleState 本体
//     已下沉 internal/idle，纯逻辑测试随包下沉到 internal/idle/idle_test.go）；
//   - **系统调用**（进程枚举、GetLastInputInfo、勿扰查询）在本机真跑，断言的是
//     「能读到且有合理结果」，不是某个固定值；
//   - **注册表**在**临时键**上做真实写入 / 读回 / 删除往返，绝不碰用户真实的 Run 键。

// ---------- 监视链路（接到真实调度器上） ----------

// newWatchApp 造一个「配置 + 调度器 + 空闲状态机」的最小 App（不碰窗口与 Wails）
func newWatchApp(t *testing.T, cfgJSON string) (*App, *scheduler.Scheduler) {
	t.Helper()
	// 统计落点钉进临时目录：idleWatchTick 会走 BeginAway / EndAway，后者会标脏并在 2 秒后
	// 落盘 —— 不隔离的话测试会去建目录、写用户真实的 stats.json
	t.Setenv("WR_STATS_PATH", filepath.Join(t.TempDir(), "stats.json"))
	s, host := newTestSched(t, cfgJSON)
	s.ResetCycle()
	return &App{cfg: host.cfg, sched: s, idle: idle.NewIdleState()}, s
}

func TestIdleWatchPausesThenResumesAndReanchors(t *testing.T) {
	a, s := newWatchApp(t, shortCountdown) // 默认 idlePauseMin=5（最小 1 分钟）
	firstNext := s.StateCopy().NextAt

	// ① 未达阈值：什么都不做
	a.idle.ForceIdle(60_000)
	if away := a.idleWatchTick(false); away {
		t.Fatal("1 分钟 < 5 分钟阈值，不该判离开")
	}
	if st := s.StateCopy(); st.Paused || len(st.PauseReasons) != 0 {
		t.Fatalf("未达阈值不该暂停，得到 %+v", st.PauseReasons)
	}

	// ② 达阈值：暂停，且暂停原因恰好是一条 idle
	a.idle.ForceIdle(6 * 60_000)
	if away := a.idleWatchTick(false); !away {
		t.Fatal("达到 6 分钟应判离开")
	}
	st := s.StateCopy()
	if !st.Paused || len(st.PauseReasons) != 1 || st.PauseReasons[0] != scheduler.PauseReasonIdle {
		t.Fatalf("应恰好因 idle 暂停，得到 paused=%v reasons=%v", st.Paused, st.PauseReasons)
	}

	// ③ 已在离开态：重复推进不该叠加暂停原因
	a.idleWatchTick(true)
	if n := len(s.StateCopy().PauseReasons); n != 1 {
		t.Fatalf("重复推进不该叠加暂停原因，得到 %d 条", n)
	}

	// ④ 键鼠活动回来：恢复 + 重锚内置倒计时
	a.idle.ForceIdle(0) // IsActive（0 < 3000 去抖阈值）
	if away := a.idleWatchTick(true); away {
		t.Fatal("有活动应判回来")
	}
	st = s.StateCopy()
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
	s.BreakNow() // phase=break
	if got := s.CurrentPhase(); got != "break" {
		t.Fatalf("前置条件不成立：phase=%q", got)
	}
	a.idle.ForceIdle(9_999_999)
	if away := a.idleWatchTick(false); away {
		t.Fatal("休息窗口显示期间不该判离开")
	}
	if st := s.StateCopy(); st.Paused {
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
	a.CloseBreakWindows()
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
	st := s.StateCopy()
	if len(st.PauseReasons) != 1 || st.PauseReasons[0] != pauseReasonAppPause {
		t.Fatalf("应因「%s」暂停，得到 %v", pauseReasonAppPause, st.PauseReasons)
	}

	// 命中期间视为「用户仍在用电脑」：即使空闲到天荒地老也不判离开
	a.idle.ForceIdle(24 * 3_600_000)
	if away := a.idleWatchTick(false); away {
		t.Fatal("忙原因暂停期间不该判空闲离开")
	}
	if n := len(s.StateCopy().PauseReasons); n != 1 {
		t.Fatalf("不该叠加 idle 暂停，得到 %v", s.StateCopy().PauseReasons)
	}

	// 关掉总开关 → 下一跳自动恢复（配置改动即时生效，不需要重建监视）
	a.mu.Lock()
	a.cfg = testSettings(t, shortCountdown)
	a.mu.Unlock()
	a.appPauseWatchTick()
	if st := s.StateCopy(); st.Paused || len(st.PauseReasons) != 0 {
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
	if st := s.StateCopy(); !st.Paused || len(st.PauseReasons) != 1 || st.PauseReasons[0] != pauseReasonAppPause {
		t.Fatalf("目标应用运行时应自动暂停，得到 paused=%v reasons=%v", st.Paused, st.PauseReasons)
	}

	// 进程退出（条目关掉 = hit=false）→ 下一跳必须自动恢复，无需任何手动操作
	a.mu.Lock()
	a.cfg = testSettings(t, appPauseCfg(t, false))
	a.mu.Unlock()
	a.appPauseWatchTick()
	if st := s.StateCopy(); st.Paused || len(st.PauseReasons) != 0 {
		t.Fatalf("目标应用退出后应自动恢复，得到 paused=%v reasons=%v", st.Paused, st.PauseReasons)
	}
}

func TestAppPauseManualResumeIgnoresUntilExitOrManualPause(t *testing.T) {
	a, s := newWatchApp(t, appPauseCfg(t, true))

	// ① 自动暂停生效
	a.appPauseWatchTick()
	if st := s.StateCopy(); !st.Paused || st.PauseReasons[0] != pauseReasonAppPause {
		t.Fatalf("前置：应因应用占用暂停，得到 %v", st.PauseReasons)
	}

	// ② 用户点「恢复休息」→ 立即恢复 + 进入忽略会话
	a.schedPauseToggle()
	if st := s.StateCopy(); st.Paused || len(st.PauseReasons) != 0 {
		t.Fatalf("手动恢复应立即生效，得到 paused=%v reasons=%v", st.Paused, st.PauseReasons)
	}
	if !a.isAppPauseIgnored() {
		t.Fatal("手动恢复后应进入忽略会话")
	}

	// ③ 应用还在跑：不再被自动暂停（这正是临时忽略的意义）
	a.appPauseWatchTick()
	a.appPauseWatchTick()
	if st := s.StateCopy(); st.Paused || len(st.PauseReasons) != 0 {
		t.Fatalf("忽略会话期间不该重新暂停，得到 paused=%v reasons=%v", st.Paused, st.PauseReasons)
	}

	// ④ 用户手动暂停 → 忽略作废，回到常规判定（应用还在跑就照常叠加应用占用）
	a.schedPauseToggle()
	if st := s.StateCopy(); !st.Paused {
		t.Fatal("手动暂停应生效")
	}
	if a.isAppPauseIgnored() {
		t.Fatal("手动暂停应作废忽略会话")
	}
	a.appPauseWatchTick()
	st := s.StateCopy()
	if len(st.PauseReasons) != 2 {
		t.Fatalf("忽略作废后应用仍在跑，应叠加应用占用，得到 %v", st.PauseReasons)
	}

	// ⑤ 此时再点恢复：摘掉应用占用 + 手动暂停两条，立即恢复并再次进入忽略
	a.schedPauseToggle()
	if st := s.StateCopy(); st.Paused || len(st.PauseReasons) != 0 {
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
	if st := s.StateCopy(); !st.Paused || len(st.PauseReasons) != 1 || st.PauseReasons[0] != pauseReasonAppPause {
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
	st := s.StateCopy()
	// 本机此刻到底开没开勿扰不确定，但必须满足：没开 → 一条暂停原因都不留
	live := windnd.GetDndStatus()
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
	if st := s.StateCopy(); st.Paused || len(st.PauseReasons) != 0 {
		t.Fatalf("不活跃时不该暂停，得到 %v", st.PauseReasons)
	}
	if a.fsActiveLogged {
		t.Fatal("不活跃时不该留下「已记录」标记")
	}

	// ② 活跃：暂停原因恰好是「全屏独占」，且标记置位
	a.applyAutoPause(true, pauseReasonFullScr, &a.fsActiveLogged, "in", "out")
	st := s.StateCopy()
	if !st.Paused || len(st.PauseReasons) != 1 || st.PauseReasons[0] != pauseReasonFullScr {
		t.Fatalf("活跃时应恰好停在「全屏独占」上，得到 paused=%v %v", st.Paused, st.PauseReasons)
	}
	if !a.fsActiveLogged {
		t.Fatal("活跃时该置位「已记录」标记（否则每 3 秒刷一行日志）")
	}

	// ③ 仍然活跃：幂等，不能叠出第二条原因
	a.applyAutoPause(true, pauseReasonFullScr, &a.fsActiveLogged, "in", "out")
	if st := s.StateCopy(); len(st.PauseReasons) != 1 {
		t.Fatalf("重复判活跃不该叠加暂停原因，得到 %v", st.PauseReasons)
	}

	// ④ 退出：原因摘掉、恢复，且只摘自己那条
	a.applyAutoPause(false, pauseReasonFullScr, &a.fsActiveLogged, "in", "out")
	st = s.StateCopy()
	if st.Paused || len(st.PauseReasons) != 0 {
		t.Fatalf("退出后应恢复提醒，得到 paused=%v %v", st.Paused, st.PauseReasons)
	}
	if a.fsActiveLogged {
		t.Fatal("退出后「已记录」标记应清零")
	}
}

// ---------- 开机自启动（目标判定，注册表读写本体已迁 internal/autostart） ----------

func TestAutostartTargetSkipsDevBuild(t *testing.T) {
	// 测试二进制必然落在 go-build 临时目录里 → 必须被判定为「不可注册」
	_, _, ok := AutostartTarget()
	if ok {
		t.Fatal("开发构建（落在临时 go-build 目录里的 exe）不该被注册成开机自启动，否则就是一条死路径")
	}
}

// ---------- 真机系统调用 ----------

// 两个开关的默认值：勿扰、全屏独占都默认开。
// 全屏独占原本默认关（新增能力不替用户决定），2026-10-29 用户要求「以当前配置为出厂默认」
// 后改为开 —— 出厂默认与代码兜底必须同值，否则老配置（缺键走兜底）与新装表现不一致。
func TestAutoPauseFlagDefaults(t *testing.T) {
	s := settings.New(map[string]any{}, "")
	if !s.DndPause() {
		t.Fatal("dndPause 缺省应为 true")
	}
	if !s.FullScreenPause() {
		t.Fatal("fullscreenPause 缺省应为 true（与 default_settings.json 同值）")
	}
	s = settings.New(map[string]any{"dndPause": false, "fullscreenPause": false}, "")
	if s.DndPause() || s.FullScreenPause() {
		t.Fatalf("配置值未生效：dnd=%v fullscreen=%v", s.DndPause(), s.FullScreenPause())
	}
}
