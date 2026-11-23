package windnd

import "testing"

// 两条信号 → 两个结论的映射矩阵。改动前先看 dnd.go 顶部注释：两条系统状态选项
// 各有自己的判据，彼此不共用“是不是勿扰”这一个结论。
func TestBusyStateMapping(t *testing.T) {
	cases := []struct {
		name       string
		state      uint32
		screenBusy bool
		tier       FullScreenTier
	}{
		{"锁屏/屏保", qunsNotPresent, false, FSNone},
		{"全屏应用（非独占）/ 忙碌", qunsBusy, true, FSBusy},
		{"全屏独占", qunsRunningD3DFullScreen, true, FSExclusive},
		{"演示设置", qunsPresentationMode, true, FSExclusive},
		{"正常接收", qunsAcceptsNotifications, false, FSNone},
		{"升级后静默时间", qunsQuietTime, false, FSNone},
		{"商店应用在前台", qunsApp, false, FSNone},
		{"未知取值", 99, false, FSNone},
	}
	for _, c := range cases {
		fs := fullScreenStatusOf(c.state, true)
		if !fs.Supported || fs.Active != c.screenBusy || fs.Tier != c.tier || fs.Detail == "" {
			t.Fatalf("%s（state=%d）：应为 active=%v tier=%v，得到 %+v",
				c.name, c.state, c.screenBusy, c.tier, fs)
		}
	}
}

// 分档是 2026-11-20 的核心决定：QUNS=2 单独降档（默认不处理），3/4 保持“直接暂停”。
// 三个值仍然都是 Active —— Active 只表示“屏幕上有全屏内容”，怎么处置由 App 层按档决定。
func TestScreenTiersSplitBusyFromExclusive(t *testing.T) {
	if fs := fullScreenStatusOf(qunsBusy, true); fs.Tier != FSBusy || !fs.Active {
		t.Fatalf("QUNS=2 必须是 FSBusy 且 Active，得到 %+v", fs)
	}
	for _, state := range []uint32{qunsRunningD3DFullScreen, qunsPresentationMode} {
		fs := fullScreenStatusOf(state, true)
		if fs.Tier != FSExclusive || !fs.Active {
			t.Fatalf("QUNS=%d 必须是 FSExclusive 且 Active，得到 %+v", state, fs)
		}
	}
	for _, state := range []uint32{qunsNotPresent, qunsAcceptsNotifications, qunsQuietTime, qunsApp, 99} {
		if fs := fullScreenStatusOf(state, true); fs.Active || fs.Tier != FSNone {
			t.Fatalf("QUNS=%d 不该命中任何档，得到 %+v", state, fs)
		}
	}
	if s := FSBusy.String(); s != "busy" {
		t.Fatalf("档位文案要能进日志，得到 %q", s)
	}
}

// 勿扰判据来自 WNF 档位，与 QUNS 无关：这是 Win11“通知中心手动开勿扰”能被测到的关键。
func TestDndComesFromWnfProfileOnly(t *testing.T) {
	cases := []struct {
		profile uint32
		active  bool
	}{
		{quietHoursOff, false},
		{quietHoursPriorityOnly, true},
		{quietHoursAlarmsOnly, true},
		{42, true}, // 未知非零档位：保守按勿扰处理
	}
	for _, c := range cases {
		d := dndStatusOfProfile(c.profile, true)
		if !d.Supported || d.Active != c.active || d.Detail == "" {
			t.Fatalf("档位 %d：勿扰结论应为 active=%v，得到 %+v", c.profile, c.active, d)
		}
	}
	// QUNS=5（正常接收通知）+ WNF=1（用户按了勿扰）：勿扰必须仍为生效
	st := BusyStatus{QunsSupported: true, Quns: qunsAcceptsNotifications, WnfSupported: true, QuietProfile: 1}
	if !st.Dnd().Active {
		t.Fatal("QUNS 正常但 WNF 报勿扰时，勿扰必须生效 —— 这正是旧代码的漏检现场")
	}
	if st.FullScreen().Active {
		t.Fatal("QUNS=5 时全屏 / 演示那条不该生效")
	}
}

// 探测失败：一律“不支持 + 有说明”，绝不报 active（否则会凭空暂停休息）。
func TestProbeFailureNeverReportsActive(t *testing.T) {
	for _, d := range []DndStatus{dndStatusOfProfile(1, false)} {
		if d.Supported || d.Active || d.Detail == "" {
			t.Fatalf("WNF 失败结论错误：%+v", d)
		}
	}
	fs := fullScreenStatusOf(qunsPresentationMode, false)
	if fs.Supported || fs.Active || fs.Tier != FSNone || fs.Detail == "" {
		t.Fatalf("QUNS 失败结论错误：%+v", fs)
	}
}

// 两条探测链路互不牵连：WNF 读不到只让勿扰那条没有结论，QUNS 那条照常可判。
func TestWnfFailureDoesNotDisableQunsBranch(t *testing.T) {
	st := BusyStatus{QunsSupported: true, Quns: qunsPresentationMode, WnfSupported: false}
	if st.Dnd().Supported {
		t.Fatal("WNF 不支持时勿扰应是“没有结论”")
	}
	if !st.FullScreen().Supported || !st.FullScreen().Active {
		t.Fatal("WNF 不支持不该把 QUNS 那条作废：QUNS=4 应命中“全屏或演示”")
	}

	st2 := BusyStatus{QunsSupported: false, WnfSupported: true, QuietProfile: quietHoursPriorityOnly}
	if !st2.Dnd().Active {
		t.Fatal("QUNS 不支持不该影响勿扰")
	}
	if st2.FullScreen().Supported {
		t.Fatal("QUNS 不支持时全屏 / 演示那条应是“没有结论”")
	}
}
