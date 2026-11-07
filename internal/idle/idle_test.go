package idle

import (
	"testing"
	"time"
)

// 空闲状态机与空闲判定的回归。这组用例原本在 main 包的 sys_test.go 里，
// 随 IdleState 一起下沉 —— 它们测的是 IdleState 的内部语义（私有字段 / 假时钟注入），
// 只有放在包内才能直接构造。监视链路（App 如何用 IdleState）仍留在 main 包。

// probeClock 假时钟 + 假 tick 源：GetLastInputInfo 返回的是“变化与否”，
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
	old := SampleInterval.Load()
	SampleInterval.Store(int64(time.Hour))
	t.Cleanup(func() { SampleInterval.Store(old) })
}

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
		t.Fatal("tick 没变不该算“有输入”")
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

	// 去抖：必须连续 3 次采样都有输入才算“人真的回来了”
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
		t.Fatal("重置会清掉去抖计数，此刻不该算“已恢复活动”")
	}

	// 强制值（冒烟 / 单测专用）
	s.ForceIdle(700_000)
	if got := s.IdleSinceMs(); got != 700_000 {
		t.Fatalf("强制后 L 应为 700000，得到 %d", got)
	}
	if s.IsActive() {
		t.Fatalf("强制 L=700000 时不该算活跃（阈值 %d）", ActiveMs)
	}
	if got := s.LastInputAtMs(); got != p.nowMs-700_000 {
		t.Fatalf("强制态的起点应反推为 now-L，得到 %d", got)
	}
	s.ForceIdle(-1) // 解除
	if got := s.IdleSinceMs(); got != 0 {
		t.Fatalf("解除强制后应回到“刚重置过”的状态，得到 %d", got)
	}
}

// 空闲判定（纯函数）的表驱动验证。
func TestDecideIdleStep(t *testing.T) {
	const thr = 300_000
	cases := []struct {
		name               string
		phase              string
		away               bool
		idleMs, threshold  int64
		busyPaused, active bool
		want               IdleStep
	}{
		{"休息窗口显示期间一律不判", "break", false, 9_999_999, thr, false, true, IdleStepNone},
		{"休息期间即使已在离开态也不判", "break", true, 9_999_999, thr, false, true, IdleStepNone},
		{"手动/勿扰/应用占用时不判离开", "idle", false, 9_999_999, thr, true, false, IdleStepNone},
		{"但忙时仍要继续检测“回来了”", "idle", true, 0, thr, true, true, IdleStepBack},
		{"够阈值 → 判离开", "idle", false, thr, thr, false, false, IdleStepAway},
		{"差一毫秒不判", "idle", false, thr - 1, thr, false, false, IdleStepNone},
		{"已在离开态且仍空闲 → 不重复动作", "idle", true, 9_999_999, thr, false, false, IdleStepNone},
		{"空闲回落但人还没“真的回来”→ 不判", "idle", true, 1_000, thr, false, false, IdleStepNone},
		{"空闲回落且去抖达标 → 判回来", "idle", true, 1_000, thr, false, true, IdleStepBack},
		{"有活动但没判过离开 → 不动作", "idle", false, 0, thr, false, true, IdleStepNone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := DecideIdleStep(c.phase, c.away, c.idleMs, c.threshold, c.busyPaused, c.active)
			if got != c.want {
				t.Fatalf("DecideIdleStep = %v，期望 %v", got, c.want)
			}
		})
	}
}

// 真机系统调用：user32!GetLastInputInfo 应可用。
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
