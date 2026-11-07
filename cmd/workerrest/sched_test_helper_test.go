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
}

func (h *testHost) CfgSnapshot() *settings.Settings                   { return h.cfg }
func (h *testHost) OnSchedulerState()                                 { h.states++ }
func (h *testHost) OnPreNotify(kind string, _ int64)                  { h.pre = append(h.pre, kind) }
func (h *testHost) OnPaused()                                         { h.pauses++ }
func (h *testHost) OnAutoPause()                                      { h.autoPauses++ }
func (h *testHost) ClosePreBreak()                                    {}
func (h *testHost) CloseBreakWindows()                                { h.closed++ }
func (h *testHost) TakeoutPostponedSnapshot() *model.TakeoutPostponed { return h.pp }
func (h *testHost) TakeoutFiredSnapshot() map[string]bool             { return nil }
func (h *testHost) OnBreakPostponed(kind string, _ int64)             { h.postpones = append(h.postpones, kind) }
func (h *testHost) OnBreakSkipped(kind string, _ int64)               { h.skips = append(h.skips, kind) }
func (h *testHost) ShowBreakSession(kind string, _ bool, _ string) string {
	h.breaks = append(h.breaks, kind)
	return "id-" + kind
}

// newTestSched 造一个带记录宿主的调度器（尚未 replan，调用方按需 ResetCycle）
func newTestSched(t *testing.T, cfgJSON string) (*scheduler.Scheduler, *testHost) {
	t.Helper()
	host := &testHost{cfg: testSettings(t, cfgJSON)}
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
