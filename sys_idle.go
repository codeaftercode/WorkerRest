package main

import (
	"log"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// 空闲状态机（软件侧维护的「连续无输入时长 L」）—— 历史源码/idleState.ts 的移植。
//
// 为什么不直接问系统「空闲多少秒」：产品的口径是「休息窗口关闭后空闲从 0 重新计算」，
// 而系统的输入计时器不会因为软件关掉一个窗口就归零。所以这里自己维护：
//
//		L = 现在 − max(最后一次输入时刻, 重置点)
//
//	  - **最后一次输入时刻**：读 `user32!GetLastInputInfo`。它返回的是 GetTickCount 域的
//	    tick，与墙钟不同源、不能相减 —— 所以靠「两次采样 tick 变了」推定「这段采样窗口里
//	    有过键鼠输入」，再把**墙钟**记下来（与 systemInput.ts 的口径逐字一致）。
//	  - **重置点**：休息窗口关闭时由 endBreak 流程调 ResetBaseline() 置为当时。
//	  - **存活去抖**：连续 idleActiveMs 都采到输入才算「人真的回来了」。猫踩一下鼠标
//	    不会把一段离开切成 离开 / 工作 1 秒 / 离开。
//
// 未调 Start()（或系统读不到 tick）时 L 恒为 0 —— 与 idleState.ts 的 `started` 同义，
// 冒烟下不会因为「无人操作」被一路判成离开。
const (
	// idleSampleMs 采样间隔：L 的精度就是这个值（1 秒够用）
	idleSampleMs = 1000
	// idleActiveMs 连续输入多久才算「人真的回来了」（去抖）
	idleActiveMs = 3000
)

// idleSampleInterval 采样间隔。写成变量（不是常量）是为了让单测把它拉成小时级 ——
// 否则后台 goroutine 会和单测里手动推进的假时钟互相干扰，断言变成掷骰子。
var idleSampleInterval = idleSampleMs * time.Millisecond

// ---------- Win32：最后一次键鼠输入时刻 ----------

// lastInputInfo 对应 LASTINPUTINFO { UINT cbSize; DWORD dwTime; }
// x64 下 sizeof = 8，dwTime 在偏移 4（Go 的结构体布局与之一致，无需手算偏移）。
type lastInputInfo struct {
	cbSize uint32
	dwTime uint32
}

var (
	modUser32            = syscall.NewLazyDLL("user32.dll")
	procGetLastInputInfo = modUser32.NewProc("GetLastInputInfo")
)

// lastInputTick 读系统「最后一次键鼠输入」的毫秒 tick；第二个返回值表示是否读到。
// 只用它做「两次采样是否相同」的比较，**绝不与墙钟相减**（零点在开机时刻）。
func lastInputTick() (int64, bool) {
	var li lastInputInfo
	li.cbSize = uint32(unsafe.Sizeof(li))
	r, _, _ := procGetLastInputInfo.Call(uintptr(unsafe.Pointer(&li)))
	if r == 0 {
		return 0, false
	}
	return int64(li.dwTime), true
}

// ---------- 状态机 ----------

// IdleState 空闲状态机。所有导出方法都可并发调用（自带锁）。
type IdleState struct {
	mu sync.Mutex

	started bool
	// hasTick / lastTick 上一次采到的输入 tick（hasTick=false 表示还没采过）
	hasTick  bool
	lastTick int64

	lastInputAt    int64 // 最后一次检测到键鼠输入的**墙钟**毫秒
	resetAt        int64 // 重置点（0 = 还没重置过）
	activeStreakMs int64 // 已连续检测到输入的时长，用于 idleActiveMs 去抖

	forced   bool // 冒烟专用：把 L 钉死在 forcedMs
	forcedMs int64

	// 注入点（单测替换成假时钟 / 假 tick 源）
	nowFn  func() int64
	tickFn func() (int64, bool)

	stopCh   chan struct{}
	stopOnce sync.Once
}

// NewIdleState 建一个空闲状态机（**未启动**：L 恒为 0，要调 Start 才开始采样）
func NewIdleState() *IdleState {
	return &IdleState{
		nowFn:       nowMs,
		tickFn:      lastInputTick,
		lastInputAt: nowMs(),
	}
}

// Start 启动采样。与 idleState.ts 的 startIdleState 同义，但**多一步探测**：
// 读不到 tick（user32 调用失败）时保持未启动状态并返回 false —— 上一版 那边还有
// `powerMonitor.getSystemIdleTime()` 可以退化成秒级，Go 侧没有第二来源，
// 假装有活动会让 L 一路变大、把用户误判成离开，所以这里选择明确地「不启用」。
func (s *IdleState) Start() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return true
	}
	if _, ok := s.tickFn(); !ok {
		log.Printf("[idle] 读不到系统输入 tick（user32!GetLastInputInfo 不可用），空闲自动暂停不启用")
		return false
	}
	s.lastInputAt = s.nowFn()
	s.resetAt = 0
	s.activeStreakMs = 0
	s.started = true
	ch := make(chan struct{})
	s.stopCh = ch
	go s.loop(ch)
	log.Printf("[idle] 采样已启动（%s，间隔 %dms，去抖 %dms）", s.detail(), idleSampleMs, idleActiveMs)
	return true
}

// Stop 停止采样（退出清理）
func (s *IdleState) Stop() {
	s.stopOnce.Do(func() {
		s.mu.Lock()
		ch := s.stopCh
		s.stopCh = nil
		s.started = false
		s.mu.Unlock()
		if ch != nil {
			close(ch)
		}
	})
}

func (s *IdleState) loop(ch chan struct{}) {
	t := time.NewTicker(idleSampleInterval)
	defer t.Stop()
	for {
		select {
		case <-ch:
			return
		case <-t.C:
			s.Sample()
		}
	}
}

// Sample 推进一次采样。导出是为了让单测能注入假时钟与假 tick 源后手动推进，
// 不必等真实的一秒。
func (s *IdleState) Sample() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.started {
		return
	}
	now := s.nowFn()
	tick, ok := s.tickFn()
	if !ok {
		return
	}
	if s.hasTick && tick != s.lastTick {
		// 采样窗口里 tick 变过 = 这段时间有过键鼠输入
		s.lastInputAt = now
		s.activeStreakMs += idleSampleMs
	} else {
		s.activeStreakMs = 0
	}
	s.lastTick = tick
	s.hasTick = true
}

// IdleSinceMs L：连续无输入时长（毫秒）。未启动 / 被强制时按各自口径返回。
func (s *IdleState) IdleSinceMs() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.forced {
		return s.forcedMs
	}
	if !s.started {
		return 0
	}
	return max(0, s.nowFn()-max(s.lastInputAt, s.resetAt))
}

// LastInputAtMs 这段空闲的起点（= 最后一次输入的时刻）
func (s *IdleState) LastInputAtMs() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.forced {
		return s.nowFn() - s.forcedMs
	}
	if !s.started {
		return s.nowFn()
	}
	return max(s.lastInputAt, s.resetAt)
}

// IsActive 人是不是「真的回来了」：连续 idleActiveMs 都有输入（去抖）
func (s *IdleState) IsActive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.forced {
		return s.forcedMs < idleActiveMs
	}
	if !s.started {
		return true
	}
	return s.activeStreakMs >= idleActiveMs
}

// ResetBaseline 把空闲基准清零 —— 休息窗口关闭时调用（「窗口关闭后空闲从 0 重新计算」）。
// 置成「此刻刚有输入」，于是要再等满 N 分钟才会判下一次离开。
func (s *IdleState) ResetBaseline() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.nowFn()
	s.lastInputAt = now
	s.resetAt = now
	s.activeStreakMs = 0
}

// ForceIdle 冒烟 / 单测专用：把 L 钉在 ms（传负数解除）。
func (s *IdleState) ForceIdle(ms int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ms < 0 {
		s.forced = false
		s.forcedMs = 0
		return
	}
	s.forced = true
	s.forcedMs = ms
}

// Supported 本机能否读系统输入 tick
func (s *IdleState) Supported() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.tickFn()
	return ok
}

// detail 读取能力详情（日志用）
func (s *IdleState) detail() string { return "user32!GetLastInputInfo" }

// IdleInfo 空闲状态自检信息（对应 idleState.ts 的 idleStateInfo）
type IdleInfo struct {
	Supported bool   `json:"supported"`
	Detail    string `json:"detail"`
	IdleMs    int64  `json:"idleMs"`
	Active    bool   `json:"active"`
}

// Info 汇总空闲状态（诊断页与无头冒烟用）
func (s *IdleState) Info() IdleInfo {
	supported := s.Supported()
	d := s.detail()
	if !supported {
		d = "user32!GetLastInputInfo 不可用"
	}
	return IdleInfo{
		Supported: supported,
		Detail:    d,
		IdleMs:    s.IdleSinceMs(),
		Active:    s.IsActive(),
	}
}

// ---------- 判定（纯函数） ----------

// idleStep 一次空闲判定的结论
type idleStep int

const (
	idleStepNone idleStep = iota
	idleStepAway          // 刚判定为「离开」→ 暂停提醒
	idleStepBack          // 刚判定为「回来了」→ 恢复提醒 + 重锚内置倒计时
)

// decideIdleStep 把空闲读数翻成「该做什么」。抽成纯函数是为了能脱离窗口与时钟做表驱动单测
// —— 这段顺序写反了不会报错，只会让休息窗口永远不关（见下面第一条）。
//
//	phase      调度器当前阶段（"break" 时一律不判）
//	away       上一次判定结果
//	busyPaused 因手动 / 勿扰 / 应用占用而暂停（用户仍在用电脑）
func decideIdleStep(phase string, away bool, idleMs, thresholdMs int64, busyPaused, active bool) idleStep {
	// 休息窗口显示期间一律不判：这段时间算「休息」，窗口关闭时会把空闲基准清零。
	// 而且必须**在暂停之前**就拦住 —— 休息期间若被判成离开而 pause('idle')，
	// 调度器的休息计时在暂停态直接 return，窗口就永远不会自动关闭。
	if phase == "break" {
		return idleStepNone
	}
	// 手动暂停 / 勿扰 / 应用占用：用户仍在用电脑（开会、看视频、跑全屏应用），
	// 这段时间不做离开判定。已经判过离开的话仍要继续检测「回来了」。
	if !away && busyPaused {
		return idleStepNone
	}
	if idleMs >= thresholdMs {
		if away {
			return idleStepNone // 已在离开态：只续计时
		}
		return idleStepAway
	}
	if away && active {
		// 连续 3 秒都有输入才算真的回来了（猫踩一下鼠标不能把一段离开切成两半）
		return idleStepBack
	}
	return idleStepNone
}
