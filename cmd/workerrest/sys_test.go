package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	a, s, _ := newWatchAppWithHost(t, cfgJSON)
	return a, s
}

// newWatchAppWithHost 同上，另把记录型宿主一并交出来（要看 OnPreNotify 之类的回调时用）
func newWatchAppWithHost(t *testing.T, cfgJSON string) (*App, *scheduler.Scheduler, *testHost) {
	t.Helper()
	// 统计落点钉进临时目录：idleWatchTick 会走 BeginAway / EndAway，后者会标脏并在 2 秒后
	// 落盘 —— 不隔离的话测试会去建目录、写用户真实的 stats.json
	t.Setenv("WR_STATS_PATH", filepath.Join(t.TempDir(), "stats.json"))
	s, host := newTestSched(t, cfgJSON)
	s.ResetCycle()
	return &App{cfg: host.cfg, sched: s, idle: idle.NewIdleState()}, s, host
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

	// 进程退出（条目关掉 = hit=false）：第一拍不认账（应用可能正在重启，见
	// appPauseExitConfirmMisses），第二拍仍没命中才自动恢复。整段不需要任何手动操作。
	a.mu.Lock()
	a.cfg = testSettings(t, appPauseCfg(t, false))
	a.mu.Unlock()
	a.appPauseWatchTick()
	if st := s.StateCopy(); !st.Paused {
		t.Fatalf("退出要连续两拍才认：第一拍应保持暂停，得到 paused=%v reasons=%v", st.Paused, st.PauseReasons)
	}
	a.appPauseWatchTick()
	if st := s.StateCopy(); st.Paused || len(st.PauseReasons) != 0 {
		t.Fatalf("连续两拍都没命中应自动恢复，得到 paused=%v reasons=%v", st.Paused, st.PauseReasons)
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
	if !a.isAutoPauseIgnored(pauseReasonAppPause) {
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
	if a.isAutoPauseIgnored(pauseReasonAppPause) {
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

	// ⑥ 应用退出 → 忽略解除（与自动恢复同一条迟滞：连续两拍没命中才算退出）；
	// 应用再次运行 → 照常自动暂停（下个会话）
	a.mu.Lock()
	a.cfg = testSettings(t, appPauseCfg(t, false))
	a.mu.Unlock()
	a.appPauseWatchTick()
	if !a.isAutoPauseIgnored(pauseReasonAppPause) {
		t.Fatal("退出要连续两拍才认：第一拍不该解除忽略（应用可能正在重启）")
	}
	a.appPauseWatchTick()
	if a.isAutoPauseIgnored(pauseReasonAppPause) {
		t.Fatal("连续两拍都没命中，忽略应解除")
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

	// ② 只活跃一拍：进入侧去抖，攒满两拍前不暂停、不打日志（单次采样尖峰到此为止）
	a.applyAutoPause(true, pauseReasonFullScr, &a.fsActiveLogged, "in", "out")
	if st := s.StateCopy(); st.Paused || len(st.PauseReasons) != 0 {
		t.Fatalf("单拍活跃不该暂停（去抖），得到 paused=%v %v", st.Paused, st.PauseReasons)
	}
	if a.fsActiveLogged {
		t.Fatal("单拍活跃不该留下「已记录」标记")
	}

	// ③ 第二拍仍活跃：确认，暂停原因恰好是「全屏独占」，且标记置位
	a.applyAutoPause(true, pauseReasonFullScr, &a.fsActiveLogged, "in", "out")
	st := s.StateCopy()
	if !st.Paused || len(st.PauseReasons) != 1 || st.PauseReasons[0] != pauseReasonFullScr {
		t.Fatalf("两拍活跃应停在「全屏独占」上，得到 paused=%v %v", st.Paused, st.PauseReasons)
	}
	if !a.fsActiveLogged {
		t.Fatal("确认暂停时该置位「已记录」标记（否则每 3 秒刷一行日志）")
	}

	// ④ 仍然活跃：幂等，不能叠出第二条原因
	a.applyAutoPause(true, pauseReasonFullScr, &a.fsActiveLogged, "in", "out")
	if st := s.StateCopy(); len(st.PauseReasons) != 1 {
		t.Fatalf("重复判活跃不该叠加暂停原因，得到 %v", st.PauseReasons)
	}

	// ⑤ 退出全屏：按策略**保持暂停** —— 退出全屏不等于会议结束，恢复交给用户手动点
	a.applyAutoPause(false, pauseReasonFullScr, &a.fsActiveLogged, "in", "out")
	st = s.StateCopy()
	if !st.Paused || len(st.PauseReasons) != 1 || st.PauseReasons[0] != pauseReasonFullScr {
		t.Fatalf("退出全屏应保持暂停等用户恢复，得到 paused=%v %v", st.Paused, st.PauseReasons)
	}
	if a.isAutoReasonActive(pauseReasonFullScr) {
		t.Fatal("条件已消失，autoReasonActive 应登记为 false")
	}
	if a.fsActiveLogged {
		t.Fatal("标记应清零：它是「上一跳条件是否成立」，留着会让退出日志每 3 秒重打一遍")
	}
	if !a.allAutoReasonsCleared() {
		t.Fatal("挂着的自动原因条件都消失了，托盘该提示「可以恢复了」")
	}

	// ⑥ 用户点「恢复休息」：这时才真的结束，且**不为已消解的这条记忽略** ——
	// 记了下一场会议就不会再自动暂停，那比误弹一次糟得多
	a.schedPauseToggle()
	if st := s.StateCopy(); st.Paused || len(st.PauseReasons) != 0 {
		t.Fatalf("手动恢复后应结束暂停，得到 %v", st.PauseReasons)
	}
	if a.isAutoPauseIgnored(pauseReasonFullScr) {
		t.Fatal("条件已消解的原因不该记忽略，否则下次进全屏不再自动暂停")
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

// 自动暂停开关的默认值：勿扰、全屏独占、无线投放、复制模式都默认开。
// 全屏独占原本默认关（新增能力不替用户决定），2026-10-29 用户要求「以当前配置为出厂默认」
// 后改为开 —— 出厂默认与代码兜底必须同值，否则老配置（缺键走兜底）与新装表现不一致。
// 投屏（2026-11-03 新增，同日拆成无线 / 复制两个）沿用同一个口径：出厂即勾选，代码兜底同为 true。
func TestAutoPauseFlagDefaults(t *testing.T) {
	s := settings.New(map[string]any{}, "")
	if !s.DndPause() {
		t.Fatal("dndPause 缺省应为 true")
	}
	if !s.FullScreenPause() {
		t.Fatal("fullscreenPause 缺省应为 true（与 default_settings.json 同值）")
	}
	if !s.CastWirelessPause() {
		t.Fatal("castWirelessPause 缺省应为 true（与 default_settings.json 同值）")
	}
	if !s.CastMirrorPause() {
		t.Fatal("castMirrorPause 缺省应为 true（与 default_settings.json 同值）")
	}
	s = settings.New(map[string]any{
		"dndPause": false, "fullscreenPause": false,
		"castWirelessPause": false, "castMirrorPause": false,
	}, "")
	if s.DndPause() || s.FullScreenPause() || s.CastWirelessPause() || s.CastMirrorPause() {
		t.Fatalf("配置值未生效：dnd=%v fullscreen=%v wireless=%v mirror=%v",
			s.DndPause(), s.FullScreenPause(), s.CastWirelessPause(), s.CastMirrorPause())
	}
}

// 「信不信某条判据」本身是纯逻辑，用构造的探测结果钉住四种命中形态 × 四种开关组合。
// 真机测试只能验证「不该暂停时确实没暂停」，验证不到「该暂停时真暂停了」——
// 那取决于跑测试的机器当下接了几块屏，在单屏机器上永远走不到命中分支。
//
// 拆成两条原因之后，矩阵要断言的是「各自是否命中」，不再是合成的一个布尔：
// 合成布尔时代「两条都命中、只开复制」与「只有复制」返回的是同一个 true，看不出区别。
func TestCastPauseWantedMatrix(t *testing.T) {
	states := []struct {
		name string
		st   windnd.CastStatus
	}{
		{"都没命中", windnd.CastStatus{Supported: true}},
		{"只有镜像", windnd.CastStatus{Supported: true, Mirror: true, Active: true}},
		{"只有无线", windnd.CastStatus{Supported: true, Wireless: true, Active: true}},
		{"两条都命中", windnd.CastStatus{Supported: true, Mirror: true, Wireless: true, Active: true}},
	}
	// 每行的四列对应开关组合 {都关} {只开镜像} {只开无线} {都开}，值是 (镜像, 无线)
	want := map[string][4][2]bool{
		"都没命中":  {{false, false}, {false, false}, {false, false}, {false, false}},
		"只有镜像":  {{false, false}, {true, false}, {false, false}, {true, false}},
		"只有无线":  {{false, false}, {false, false}, {false, true}, {false, true}},
		"两条都命中": {{false, false}, {true, false}, {false, true}, {true, true}},
	}
	combos := [4][2]bool{{false, false}, {true, false}, {false, true}, {true, true}}
	for _, s := range states {
		for i, c := range combos {
			gotMirror, gotWireless := castWant(s.st, c[0], c[1])
			if w := want[s.name][i]; gotMirror != w[0] || gotWireless != w[1] {
				t.Fatalf("%s / 复制=%v 无线=%v：得到 (%v,%v)，应为 %v",
					s.name, c[0], c[1], gotMirror, gotWireless, w)
			}
		}
	}
}

// 投屏支路：两条判据各受自己的开关管，开哪条信哪条，且各自成为一条独立原因。
//
// 不写死「本机一定在投屏」：以 GetCastStatus() 当预期，任何机器上都成立。
// 四种开关组合逐个过，才真正证明「拆开了」—— 只测全开和全关的话，
// 两个开关在代码里串成一个 AND、或者共用一条原因名，都照样是绿的。
func TestCastWatchRespectsEachSwitch(t *testing.T) {
	live := windnd.GetCastStatus()
	cases := []struct {
		name         string
		cfg          string
		wantMirror   bool
		wantWireless bool
	}{
		{"两个开关都关", `{"castWirelessPause":false,"castMirrorPause":false}`, false, false},
		{"只开复制模式", `{"castWirelessPause":false,"castMirrorPause":true}`, live.Mirror, false},
		{"只开无线投放", `{"castWirelessPause":true,"castMirrorPause":false}`, false, live.Wireless},
		{"两个都开", `{"castWirelessPause":true,"castMirrorPause":true}`, live.Mirror, live.Wireless},
	}
	for _, c := range cases {
		a, s := newWatchApp(t, shortCountdown)
		a.mu.Lock()
		a.cfg = testSettings(t, c.cfg)
		a.mu.Unlock()
		// 连打两跳：不该因为每跳都 Pause 一次而叠出重复原因
		a.castWatchTick()
		a.castWatchTick()
		st := s.StateCopy()
		gotMirror, gotWireless := false, false
		for _, r := range st.PauseReasons {
			switch r {
			case pauseReasonCastMirror:
				gotMirror = true
			case pauseReasonCastWireless:
				gotWireless = true
			}
		}
		if gotMirror != c.wantMirror || gotWireless != c.wantWireless {
			t.Fatalf("%s：应为 复制=%v 无线=%v，得到 %v（本机探测 mirror=%v wireless=%v %s）",
				c.name, c.wantMirror, c.wantWireless, st.PauseReasons, live.Mirror, live.Wireless, live.Detail)
		}
		wantLen := 0
		for _, v := range []bool{c.wantMirror, c.wantWireless} {
			if v {
				wantLen++
			}
		}
		if len(st.PauseReasons) != wantLen {
			t.Fatalf("%s：暂停原因数应为 %d，得到 %v", c.name, wantLen, st.PauseReasons)
		}
		if gotMirror != a.castMirrorLogged || gotWireless != a.castWirelessLogged {
			t.Fatalf("%s：「已记录」标记应与暂停状态一致（mirror=%v/%v wireless=%v/%v）",
				c.name, a.castMirrorLogged, gotMirror, a.castWirelessLogged, gotWireless)
		}
	}
	t.Logf("本机投屏探测：active=%v mirror=%v wireless=%v（%s）",
		live.Active, live.Mirror, live.Wireless, live.Detail)
}

// 六条自动原因共用的「恢复休息」语义（本表覆盖其中五条；「应用占用」走自己的
// appPauseWatchTick，由 TestAppPauseWatch* 一组单独覆盖）：
//
//	因某原因暂停 → 点恢复休息 → 条件还在也不再因它暂停 → 条件消失后解除 → 再次达成照常暂停。
//
// 另外两条不变式：被忽略的原因不影响其他原因暂停；用户主动暂停会作废所有忽略。
func TestAutoPauseManualResumeIgnoresUntilConditionClears(t *testing.T) {
	const idleThresholdMs = 6 * 60_000 // 默认 idlePauseMin=5

	cases := []struct {
		name   string
		reason string
		other  string                // 用来验证「其他原因照常生效」的另一条原因
		on     func(a *App, on bool) // 条件成立 / 消失各推一跳
	}{
		{
			name: "勿扰", reason: pauseReasonDnd, other: pauseReasonFullScr,
			on: func(a *App, on bool) {
				a.applyAutoPause(on, pauseReasonDnd, &a.dndActiveLogged, "", "")
				if on {
					// 进入侧去抖：第二拍才确认
					a.applyAutoPause(on, pauseReasonDnd, &a.dndActiveLogged, "", "")
				}
			},
		},
		{
			name: "全屏独占", reason: pauseReasonFullScr, other: pauseReasonDnd,
			on: func(a *App, on bool) {
				a.applyAutoPause(on, pauseReasonFullScr, &a.fsActiveLogged, "", "")
				if on {
					a.applyAutoPause(on, pauseReasonFullScr, &a.fsActiveLogged, "", "")
				}
			},
		},
		{
			name: "复制模式", reason: pauseReasonCastMirror, other: pauseReasonCastWireless,
			on: func(a *App, on bool) {
				a.applyAutoPause(on, pauseReasonCastMirror, &a.castMirrorLogged, "", "")
				if on {
					a.applyAutoPause(on, pauseReasonCastMirror, &a.castMirrorLogged, "", "")
				}
			},
		},
		{
			name: "无线投放", reason: pauseReasonCastWireless, other: pauseReasonCastMirror,
			on: func(a *App, on bool) {
				a.applyAutoPause(on, pauseReasonCastWireless, &a.castWirelessLogged, "", "")
				if on {
					a.applyAutoPause(on, pauseReasonCastWireless, &a.castWirelessLogged, "", "")
				}
			},
		},
		{
			name: "系统空闲", reason: scheduler.PauseReasonIdle, other: pauseReasonDnd,
			on: func(a *App, on bool) {
				if on {
					a.idle.ForceIdle(idleThresholdMs)
				} else {
					a.idle.ForceIdle(0) // 有输入 = 人回来了
				}
				a.idleAway = a.idleWatchTick(a.idleAway)
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, s := newWatchApp(t, shortCountdown)
			hasReason := func(r string) bool {
				for _, x := range s.StateCopy().PauseReasons {
					if x == r {
						return true
					}
				}
				return false
			}

			// ① 条件成立 → 因该原因暂停
			c.on(a, true)
			if !s.IsPaused() || !hasReason(c.reason) {
				t.Fatalf("前置：应因「%s」暂停，得到 %v", c.reason, s.StateCopy().PauseReasons)
			}

			// ② 用户点「恢复休息」→ 立即恢复
			a.schedPauseToggle()
			if s.IsPaused() || len(s.StateCopy().PauseReasons) != 0 {
				t.Fatalf("点恢复休息应立即生效，得到 %v", s.StateCopy().PauseReasons)
			}
			if !a.isAutoPauseIgnored(c.reason) {
				t.Fatalf("恢复后应把「%s」记为已手动恢复", c.reason)
			}

			// ③ 条件仍在：连推两跳也不再挂回来
			c.on(a, true)
			c.on(a, true)
			if s.IsPaused() || hasReason(c.reason) {
				t.Fatalf("忽略期间条件仍成立，不该重新暂停，得到 %v", s.StateCopy().PauseReasons)
			}

			// ④ 别的原因照常生效（恢复的是「当时那几条」，不是一刀切静音）
			a.applyAutoPause(true, c.other, new(bool), "", "")
			a.applyAutoPause(true, c.other, new(bool), "", "") // 进入侧去抖：第二拍才确认
			if !hasReason(c.other) {
				t.Fatalf("「%s」被忽略时，「%s」仍应照常暂停，得到 %v",
					c.reason, c.other, s.StateCopy().PauseReasons)
			}
			a.applyAutoPause(false, c.other, new(bool), "", "")
			// 条件消失后怎么收场，取决于该原因的策略（autoResumeOnClear）：
			// 空闲与应用占用自动恢复；勿扰 / 全屏 / 两条投屏保留暂停，等用户手动点。
			if autoResumeOnClear(c.other) {
				if s.IsPaused() {
					t.Fatalf("「%s」条件消失后应自动恢复，得到 %v", c.other, s.StateCopy().PauseReasons)
				}
			} else {
				if !hasReason(c.other) {
					t.Fatalf("「%s」条件消失后应保持暂停（等用户手动恢复），得到 %v",
						c.other, s.StateCopy().PauseReasons)
				}
				a.schedPauseToggle()
				if s.IsPaused() {
					t.Fatalf("手动恢复「%s」后应结束暂停，得到 %v", c.other, s.StateCopy().PauseReasons)
				}
			}

			// ⑤ 条件消失 → 忽略解除（下次达成照常触发）
			c.on(a, false)
			if a.isAutoPauseIgnored(c.reason) {
				t.Fatalf("「%s」的条件已消失，忽略应解除", c.reason)
			}

			// ⑥ 再次达成 → 重新暂停
			c.on(a, true)
			if !hasReason(c.reason) {
				t.Fatalf("条件再次达成应重新暂停，得到 %v", s.StateCopy().PauseReasons)
			}
		})
	}
}

// 用户主动「暂停休息」清空所有忽略：他明确要求回到常规判定，之后各原因该暂停就暂停。
func TestManualPauseClearsAutoPauseIgnores(t *testing.T) {
	a, s := newWatchApp(t, shortCountdown)

	a.applyAutoPause(true, pauseReasonDnd, &a.dndActiveLogged, "", "")
	a.applyAutoPause(true, pauseReasonDnd, &a.dndActiveLogged, "", "") // 进入侧去抖：第二拍
	a.schedPauseToggle() // 恢复 + 忽略勿扰
	if !a.isAutoPauseIgnored(pauseReasonDnd) {
		t.Fatal("前置：恢复后应处于忽略状态")
	}

	// 此刻既没暂停也没自动原因 → 这里是「手动暂停」
	a.schedPauseToggle()
	if !s.IsPaused() {
		t.Fatal("应进入手动暂停")
	}
	if a.isAutoPauseIgnored(pauseReasonDnd) {
		t.Fatal("手动暂停应作废忽略，回到常规自动判定")
	}

	// 勿扰条件仍成立：下一跳照常叠加（去抖计数在手动恢复时被忽略表分支清零，重新攒两拍）
	a.applyAutoPause(true, pauseReasonDnd, &a.dndActiveLogged, "", "")
	a.applyAutoPause(true, pauseReasonDnd, &a.dndActiveLogged, "", "")
	found := false
	for _, r := range s.StateCopy().PauseReasons {
		if r == pauseReasonDnd {
			found = true
		}
	}
	if !found {
		t.Fatalf("忽略作废后勿扰应照常生效，得到 %v", s.StateCopy().PauseReasons)
	}
}

// 「离开恰好从提前提示区间里开始」时，回来不该先弹一个提示窗再被重锚收掉。
//
// 时序：倒计时剩 2.9 秒时弹出提前提示窗 → 用户此刻离开 → 5 分钟后回来。
// 恢复会把 NextAt 按暂停时长整体平移（仍在原来的提前区间内），于是旧实现会调一次
// OnPreNotify 弹出提示窗，紧接着 ReanchorCountdown 把 NextAt 推到「现在 + 整个间隔」
// 又把窗收掉 —— 用户看到右下角闪一下。空闲链路改用 ResumeQuiet 后不再弹。
//
// 对照组故意直接调公开的 Resume（= 旧实现那一条），证明差别来自这一次调用，
// 而不是「这个场景本身弹不出来」。
func TestIdleReturnDoesNotFlashPreWindow(t *testing.T) {
	// 4 秒一次短休息、提前 3 秒提示：提前区间 = [t0+1s, t0+4s]
	const tight = `{"short":{"enabled":true,"mode":"countdown","intervalMs":4000,"durationMs":2000,` +
		`"notifyBeforeMs":3000,"postponeMs":60000,"slots":[]}}`

	a, s, host := newWatchAppWithHost(t, tight)
	// 对照组与实验组共用一个时间轴，只睡一次（对照组的 App 本身用不上，只要调度器与宿主）
	_, sOld, hostOld := newWatchAppWithHost(t, tight)

	// 让「现在」落进提前区间
	time.Sleep(1100 * time.Millisecond)

	// 实验组：走真实空闲链路
	a.idle.ForceIdle(6 * 60_000)
	a.idleAway = a.idleWatchTick(false)
	a.idle.ForceIdle(0)
	a.idleAway = a.idleWatchTick(a.idleAway)

	// 对照组：手动暂停（恢复时按剩余时间平移、仍落在提前区间 → 弹一次提示窗），
	// 紧接着重锚把它收掉 —— 这正是自动暂停统一重锚之前的行为，用来证明「闪一下」
	// 这个场景确实构造得出来，实验组的 0 次不是场景没搭成。
	// 2026-11-05 起自动原因（含空闲）在 resume 内部就会重锚，无法再用 idle 复刻旧路径。
	sOld.Pause(scheduler.PauseReasonManual)
	sOld.Resume(scheduler.PauseReasonManual)
	sOld.ReanchorCountdown()

	if len(hostOld.pre) != 1 {
		t.Fatalf("对照组不成立：手动暂停恢复后应弹一次提示窗，得到 %v（场景没构造出来）", hostOld.pre)
	}
	if sOld.PreBreakWanted() {
		t.Fatal("对照组不成立：重锚后提示窗应已不该在场")
	}
	if len(host.pre) != 0 {
		t.Fatalf("空闲回来不该弹提前提示窗（会被重锚立刻收掉），得到 %v", host.pre)
	}
	if s.PreBreakWanted() {
		t.Fatal("重锚后提示窗不该在场")
	}
	if s.IsPaused() {
		t.Fatalf("回来应已恢复，得到 reasons=%v", s.StateCopy().PauseReasons)
	}
	// 重锚仍要发生：内置倒计时从「现在」重新计时（4 秒），不是残留的 2.9 秒
	if remain := s.RemainingMs(); remain < 3500 || remain > 4200 {
		t.Fatalf("回来应把内置倒计时重锚到满间隔，得到剩余 %d ms", remain)
	}
}

// 固定时刻线不会重锚，所以空闲回来时必须照常把提前提示窗弹回来 ——
// 静默恢复会把「这次提醒的提前提示」永久吞掉（休息仍准点弹，但少了提前那一下）。
// 与上一条配对：同一处代码，倒计时线要静默、固定线不能静默。
func TestIdleReturnKeepsPreWindowForFixedLine(t *testing.T) {
	// 下一个整分钟触发、提前 3 分钟提示 → 现在必然已在提前区间内
	next := time.Now().Add(time.Minute)
	cfg := `{"short":{"enabled":true,"mode":"fixed","durationMs":20000,"notifyBeforeMs":180000,` +
		`"postponeMs":60000,"slots":[{"hour":` + itoa(next.Hour()) + `,"minute":` + itoa(next.Minute()) +
		`,"enabled":true}]}}`

	a, s, host := newWatchAppWithHost(t, cfg)
	if s.CountdownReanchorable() {
		t.Fatalf("前置：固定时刻线不该被判成可重锚（nextSource=%v）", s.StateCopy().NextSource)
	}

	a.idle.ForceIdle(6 * 60_000)
	a.idleAway = a.idleWatchTick(false)
	if !s.IsPaused() {
		t.Fatal("前置：应已因空闲暂停")
	}

	a.idle.ForceIdle(0)
	a.idleAway = a.idleWatchTick(a.idleAway)
	if s.IsPaused() {
		t.Fatalf("回来应已恢复，得到 %v", s.StateCopy().PauseReasons)
	}
	if len(host.pre) != 1 {
		t.Fatalf("固定时刻线不会重锚，提前提示窗必须弹回来，得到 %v", host.pre)
	}
	if !s.PreBreakWanted() {
		t.Fatal("提前提示窗应在场（还没重锚，仍在提前区间内）")
	}
}

// C：忙碌型自动暂停期间统计仍记「工作」。
//
// 统计的三态只看两件事：休息窗在不在场（statsIsBreak）、连续无输入有没有超阈值
// （statsIsIdle）。自动暂停不改这两条 —— 开会 / 投屏 / 全屏时人还在电脑前，这段
// 该记工作；只有真正没人碰键鼠超过阈值才记离开，与暂停原因无关。
func TestBusyAutoPauseCountsAsWork(t *testing.T) {
	a, s := newWatchApp(t, shortCountdown)
	s.Pause(pauseReasonDnd)

	if a.statsIsBreak() {
		t.Fatal("自动暂停已收掉休息窗，不该记休息")
	}
	if a.statsIsIdle() {
		t.Fatal("人还在用电脑（勿扰 / 投屏 / 全屏），不该记离开")
	}

	// 真正离开（无输入超过阈值）才翻成离开 —— 哪怕暂停原因还挂着
	a.idle.ForceIdle(10 * 60_000)
	if !a.statsIsIdle() {
		t.Fatal("连续无输入超过阈值应记离开")
	}
}
