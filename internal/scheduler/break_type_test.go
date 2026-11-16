package scheduler

// 休息身份（state.BreakType）与「允许提前结束」（渲染快照 st.AllowEarlyEnd）。
//
// 这两项只为侧栏与托盘服务：BreakType 说“正在弹的是哪条提醒”，AllowEarlyEnd 说
// “这次允不允许被提前结束”。口径（2026-11-15 用户）：只有短 / 长休息配“提前结束”，
// 自定义提醒 / 外卖提醒在弹时侧栏与托盘那两处既不许写“提前结束”，也不许留着可点。
//
// 为什么不能拿 NextType 顶替 BreakType：休息期间只要 replan 一次（保存设置、时间跳变），
// NextType 就被改写成“下一条”。这里最后一条用例专门钉这件事。

import "testing"

const earlyEndCfg = `{
  "short":{"enabled":true,"mode":"countdown","intervalMs":600000,"durationMs":20000,"notifyBeforeMs":0,"postponeMs":120000,"allowEarlyEnd":false,"slots":[]},
  "long":{"enabled":true,"mode":"countdown","intervalMs":3600000,"durationMs":60000,"notifyBeforeMs":0,"postponeMs":120000,"allowEarlyEnd":true,"slots":[]},
  "customBreaks":[{"id":"cb-a","name":"喝水提醒","config":{"enabled":true,"mode":"countdown","intervalMs":300000,"durationMs":10000,"notifyBeforeMs":0,"postponeMs":120000,"allowEarlyEnd":true,"slots":[]}}]
}`

func TestBreakTypeForShortRest(t *testing.T) {
	s, _ := newTestSched(t, earlyEndCfg)
	s.ResetCycle()
	s.BreakNowOf("short")

	st := s.StateForRenderer()
	if st.Phase != "break" || st.BreakType != "short" {
		t.Fatalf("短休息中：phase=%q breakType=%q，期望 break/short", st.Phase, st.BreakType)
	}
	// 短休息卡写的 allowEarlyEnd=false：这一次不许提前结束
	if st.AllowEarlyEnd {
		t.Fatal("短休息卡没勾允许提前结束，快照却写着允许")
	}

	// 结束之后身份必须清掉：否则侧栏会留着“提前结束”的残影
	s.EndBreak()
	after := s.StateForRenderer()
	if after.Phase == "break" {
		t.Fatalf("结束休息后仍在 break 相位")
	}
	if after.BreakType != "" {
		t.Fatalf("结束休息后 breakType 应清空，实际 %q", after.BreakType)
	}
	// 不在休息时 AllowEarlyEnd 恒为 true（缺省允许，别让前端误判成“不让结束”）
	if !after.AllowEarlyEnd {
		t.Fatal("不在休息时 allowEarlyEnd 应为 true（缺省允许）")
	}
}

func TestBreakTypeForCustomBreak(t *testing.T) {
	s, _ := newTestSched(t, earlyEndCfg)
	s.ResetCycle()
	s.BreakNowOf("cb-a")

	st := s.StateForRenderer()
	if st.Phase != "break" || st.BreakType != "cb-a" {
		t.Fatalf("自定义提醒中：phase=%q breakType=%q，期望 break/cb-a", st.Phase, st.BreakType)
	}
	// 这张卡自己写着 allowEarlyEnd=true，快照照实下发 —— 但侧栏 / 托盘对非短 / 长
	// 休息一律不认这个值（见 cmd 包的 trayActionDisabled）。这里只钉“快照没撒谎”。
	if !st.AllowEarlyEnd {
		t.Fatal("cb-a 卡片勾着允许提前结束，快照却写着不允许")
	}
}

// 休息中发生一次 replan：BreakType 必须还钉着当前这次。
//
// 为什么这条重要：replan（保存设置、时间跳变都会触发）会把 NextType 重算成“下一条”，
// 而下一条是谁跟当前这次无关。侧栏与托盘判“是不是短 / 长休息”只读 BreakType，
// 拿 NextType 判就会出现“自定义提醒在弹、界面却写着提前结束”的错位。
//
// replan 会不会真的翻页取决于下一条是谁（本例里恰好还是 cb-a），所以这里不指望它
// 帮忙，直接把 NextType 摆成“已翻页”的后态 —— 断言的是两个字段的独立性。
func TestBreakTypeSurvivesReplanDuringBreak(t *testing.T) {
	s, _ := newTestSched(t, earlyEndCfg)
	s.ResetCycle()
	s.BreakNowOf("cb-a")

	s.mu.Lock()
	cur := s.state.BreakType
	s.mu.Unlock()
	if cur != "cb-a" {
		t.Fatalf("前置条件不成立：BreakType=%q", cur)
	}

	s.replan() // 休息中保存一次设置

	// 模拟 replan 把“下一条”翻成了别条身份
	s.mu.Lock()
	s.state.NextType = "long"
	s.mu.Unlock()

	st := s.StateForRenderer()
	if st.BreakType != "cb-a" {
		t.Fatalf("BreakType 被 NextType 带跑了：%q，应当还是 cb-a", st.BreakType)
	}
	if st.NextType != "long" {
		t.Fatalf("前置条件不成立：NextType=%q", st.NextType)
	}
	// 这一对才是侧栏 / 托盘真正读的判据：正在弹的是自定义提醒 → 不配“提前结束”
	if st.BreakType == "short" || st.BreakType == "long" {
		t.Fatal("cb-a 不该被当成短 / 长休息")
	}
}
