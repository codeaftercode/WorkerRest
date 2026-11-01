package windnd

import "testing"

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
