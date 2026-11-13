package main

import (
	"encoding/json"
	"testing"
	"workerrest/internal/model"
	"workerrest/internal/scheduler"
	"workerrest/internal/settings"
)

// main 包的调度器测试脚手架：调度器已下沉到 internal/scheduler，这里提供 main 包
// 测试用的“记录型宿主”与“配置构造”，让集成测试（系统监听 / 提前提示 / 外卖）能
// 以最小侵入驱动调度器。纯调度器状态机的白盒测试在 internal/scheduler 包内。

// testSettings 用一段 JSON 造一份配置（只填本次用到的字段，其余按默认）
func testSettings(t *testing.T, js string) *settings.Settings {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(js), &m); err != nil {
		t.Fatalf("测试配置解析失败: %v", err)
	}
	return settings.New(m, "")
}

// testHost 记录型宿主：只实现调度器真正会调的那几个口子，不碰窗口 / 托盘 / Wails。
type testHost struct {
	cfg        *settings.Settings
	states     int
	pre        []string
	pauses     int
	autoPauses int
	breaks     []string
	closed     int
	postpones  []string
	skips      []string
	pp         *model.TakeoutPostponed
	// autoKeep 最近一次 OnAutoPause 收到的在场窗口归属（断言“正常提醒时别收窗”用）
	autoKeep scheduler.AutoPauseKeep
	// pauseKeepPre 最近一次 OnPaused 收到的提示窗归属（手动暂停时提前提示窗留不留的兜底身份）
	pauseKeepPre string
	// idleMs 系统级“连续无输入”读数的假值（空闲门测试用）
	idleMs int64
	// extras 休息期间被并入当前窗口的提醒身份（单窗多条）
	extras []string
	// counted 被另一次休息覆盖、按“完成”记账的内置休息身份
	counted []string
	// completed 按“完成”收尾的窗口次数（长休息抢占短休息时 +1）
	completed int
	// breakOK OnBreakExtra 的返回值（是否“有可挂的休息会话”）；默认 true，
	// 需要模拟“窗口被 Alt+F4 关掉”的用例把它置 false
	breakOK bool
}

func (h *testHost) CfgSnapshot() *settings.Settings  { return h.cfg }
func (h *testHost) IdleMs() int64                    { return h.idleMs }
func (h *testHost) OnSchedulerState()                { h.states++ }
func (h *testHost) OnPreNotify(kind string, _ int64) { h.pre = append(h.pre, kind) }
func (h *testHost) OnPaused(preKind string)          { h.pauses++; h.pauseKeepPre = preKind }
func (h *testHost) OnAutoPause(keep scheduler.AutoPauseKeep) {
	h.autoPauses++
	h.autoKeep = keep
}
func (h *testHost) ClosePreBreak()        {}
func (h *testHost) CloseBreakWindows()    { h.closed++ }
func (h *testHost) CompleteBreakWindows() { h.closed++; h.completed++ }
func (h *testHost) OnBreakExtra(kind string, _ int64) bool {
	h.extras = append(h.extras, kind)
	return h.breakOK
}
func (h *testHost) OnBreakCounted(kind string)                        { h.counted = append(h.counted, kind) }
func (h *testHost) TakeoutPostponedSnapshot() *model.TakeoutPostponed { return h.pp }
func (h *testHost) TakeoutFiredSnapshot() map[string]bool             { return nil }
func (h *testHost) TakeoutIdleWaitSnapshot() *model.TakeoutIdleWait   { return nil }
func (h *testHost) OnBreakPostponed(kind string, _ int64)             { h.postpones = append(h.postpones, kind) }
func (h *testHost) OnBreakSkipped(kind string, _ int64)               { h.skips = append(h.skips, kind) }
func (h *testHost) ShowBreakSession(kind string, _ bool, _ string) string {
	h.breaks = append(h.breaks, kind)
	return "id-" + kind
}

// newTestSched 造一个带记录宿主的调度器（尚未 replan，调用方按需 ResetCycle）
func newTestSched(t *testing.T, cfgJSON string) (*scheduler.Scheduler, *testHost) {
	t.Helper()
	host := &testHost{cfg: testSettings(t, cfgJSON), breakOK: true}
	return scheduler.New(host), host
}

// shortCountdown 常用的短休息倒计时配置（10 分钟一次 20 秒）
const shortCountdown = `{"short":{"enabled":true,"mode":"countdown","intervalMs":600000,"durationMs":20000,"notifyBeforeMs":10000,"postponeMs":120000,"slots":[]}}`

// itoa 测试里拼 JSON 用（避免引入 strconv 只为一处）
func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [8]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
