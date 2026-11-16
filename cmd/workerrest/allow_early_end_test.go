package main

// 「弹窗设置 → 允许提前结束」在宿主侧的回归。
//
// schedEndBreak 是所有提前结束入口的**唯一汇聚点**（休息窗口的按钮、全局快捷键、
// 托盘 / 侧栏的“提前结束休息”都走它）。按钮可以不画，但全局快捷键是启动时按配置
// 注册的，没法按会话动态注销 —— 所以拦截必须落在这里，否则会出现
// “窗口上没有提前结束、按下快捷键却真的结束了”的半开状态。

import (
	"testing"

	"workerrest/internal/model"
)

// 不勾「允许提前结束」的短休息
const noEarlyEndCfg = `{
  "short":{"enabled":true,"mode":"countdown","intervalMs":600000,"durationMs":20000,"notifyBeforeMs":0,"postponeMs":120000,"allowEarlyEnd":false,"slots":[]},
  "long":{"enabled":true,"mode":"countdown","intervalMs":3600000,"durationMs":60000,"notifyBeforeMs":0,"postponeMs":120000,"slots":[]},
  "customBreaks":[{"id":"cb-a","name":"喝水提醒","config":{"enabled":true,"mode":"countdown","intervalMs":300000,"durationMs":10000,"notifyBeforeMs":0,"postponeMs":120000,"allowEarlyEnd":false,"slots":[]}}]
}`

// 勾着「允许提前结束」的短休息（显式写 true，与出厂默认一致）
const earlyEndCfg = `{
  "short":{"enabled":true,"mode":"countdown","intervalMs":600000,"durationMs":20000,"notifyBeforeMs":0,"postponeMs":120000,"allowEarlyEnd":true,"slots":[]},
  "long":{"enabled":true,"mode":"countdown","intervalMs":3600000,"durationMs":60000,"notifyBeforeMs":0,"postponeMs":120000,"slots":[]},
  "customBreaks":[{"id":"cb-a","name":"喝水提醒","config":{"enabled":true,"mode":"countdown","intervalMs":300000,"durationMs":10000,"notifyBeforeMs":0,"postponeMs":120000,"allowEarlyEnd":true,"slots":[]}}]
}`

// breakScene 造一个“正在进行短休息”的场景：调度器在 break 阶段 + 会话在场，
// 与 ShowBreakSession 之后的真实状态同形（只是不弹真窗口）。
func breakScene(t *testing.T, cfg string) (*App, string) {
	t.Helper()
	a := newExtraApp(t, cfg)
	s, _ := newTestSched(t, cfg)
	// ResetCycle 先按配置重排一次内置倒计时：没有待触发的线时 BreakNow 会直接返回
	// （“当前没有可用的短 / 长休息”），phase 永远进不了 break。
	s.ResetCycle()
	a.sched = s
	s.BreakNow()
	if got := s.StateCopy().Phase; got != "break" {
		t.Fatalf("前置条件不成立：调度器不在休息阶段（%q）", got)
	}
	a.mu.Lock()
	a.sess = &model.BreakInfo{ID: "short-1", Type: "short", Name: "短休息", DurationMs: 20000}
	a.mu.Unlock()
	return a, s.StateCopy().Phase
}

// 不勾：提前结束（无论从按钮、快捷键还是托盘触发）一律不响应 —— 休息继续、窗口留着。
func TestEndBreakIgnoredWhenEarlyEndDisabled(t *testing.T) {
	a, _ := breakScene(t, noEarlyEndCfg)

	a.schedEndBreak()

	if got := a.sched.StateCopy().Phase; got != "break" {
		t.Fatalf("不允许提前结束时不该结束休息，实际相位 %q", got)
	}
	if a.BreakCurrent("") == nil {
		t.Fatal("不允许提前结束时不该清掉会话（窗口会变成空白页）")
	}
	if a.curBreakAllowsEndEarly() {
		t.Fatal("配置写了 allowEarlyEnd=false，判据应为“不允许”")
	}
}

// 勾着：行为与以前完全一致 —— 休息结束、会话清空。
func TestEndBreakWorksWhenEarlyEndAllowed(t *testing.T) {
	a, _ := breakScene(t, earlyEndCfg)

	a.schedEndBreak()

	if got := a.sched.StateCopy().Phase; got == "break" {
		t.Fatal("允许提前结束时应当真的结束这次休息")
	}
	// 会话本身由宿主回调（App.OnBreakEnd）清 —— 这里的宿主是记录型 testHost，
	// 不负责清 a.sess，所以只看相位：相位翻出 break 就说明 EndBreak 真的走了。
	if !a.curBreakAllowsEndEarly() {
		t.Fatal("配置写了 allowEarlyEnd=true，判据应为“允许”")
	}
}

// 判据按**当前会话自己的身份**查：短休息不勾、自定义提醒勾着时，
// 各自的结论必须分开（不能拿短休息的配置去判自定义提醒）。
func TestCurBreakAllowsEndEarlyFollowsSessionType(t *testing.T) {
	mixed := `{
  "short":{"enabled":true,"mode":"countdown","intervalMs":600000,"durationMs":20000,"notifyBeforeMs":0,"postponeMs":120000,"allowEarlyEnd":false,"slots":[]},
  "long":{"enabled":true,"mode":"countdown","intervalMs":3600000,"durationMs":60000,"notifyBeforeMs":0,"postponeMs":120000,"allowEarlyEnd":true,"slots":[]},
  "customBreaks":[{"id":"cb-a","name":"喝水提醒","config":{"enabled":true,"mode":"countdown","intervalMs":300000,"durationMs":10000,"notifyBeforeMs":0,"postponeMs":120000,"allowEarlyEnd":true,"slots":[]}}]
}`
	a := newExtraApp(t, mixed)
	cases := map[string]bool{"short": false, "long": true, "cb-a": true}
	for kind, want := range cases {
		a.mu.Lock()
		a.sess = &model.BreakInfo{ID: kind + "-1", Type: kind, Name: kind}
		a.mu.Unlock()
		if got := a.curBreakAllowsEndEarly(); got != want {
			t.Errorf("%s：期望 %v，实际 %v", kind, want, got)
		}
	}
	// 没有会话时按“允许”办（不在休息阶段，判据不该拦任何东西）
	a.mu.Lock()
	a.sess = nil
	a.mu.Unlock()
	if !a.curBreakAllowsEndEarly() {
		t.Error("没有会话时应返回“允许”")
	}
}
