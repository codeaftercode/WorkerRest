package main

// 托盘「立即休息 / 提前结束休息」那一项的**文案与可点性**（2026-11-15 用户口径）。
//
// 口径有两层：
//   1) 只有短 / 长休息在弹时，这一项才叫“提前结束休息”；自定义提醒、外卖提醒在弹时
//      仍叫“立即休息”（它们各有自己的窗口与入口）。
//   2) 不允许提前结束的短 / 长休息 → 灰；自定义 / 外卖提醒在弹 → 也灰
//      （此刻人已经在休息里，“立即休息”点了不会有下一次休息）；暂停、工作中不灰。
//
// 这里把两种组合铺开，末尾再钉一条互斥：判成灰且文案是“提前结束休息”时，
// 原因必然只能是“那张卡没勾允许提前结束”，不能是别的。

import (
	"testing"

	"workerrest/internal/model"
)

const trayTestNow = int64(1_700_000_000_000)

func TestTrayActionDisabled(t *testing.T) {
	cases := []struct {
		name  string
		st    model.SchedulerState
		allow bool
		want  bool
	}{
		{
			name:  "短休息在弹 + 不允许提前结束 → 灰",
			st:    model.SchedulerState{Phase: "break", BreakType: "short", BreakEndsAt: trayTestNow + 1},
			allow: false,
			want:  true,
		},
		{
			name:  "长休息在弹 + 不允许提前结束 → 灰",
			st:    model.SchedulerState{Phase: "break", BreakType: "long", BreakEndsAt: trayTestNow + 1},
			allow: false,
			want:  true,
		},
		{
			name:  "短休息在弹 + 允许提前结束 → 可点",
			st:    model.SchedulerState{Phase: "break", BreakType: "short", BreakEndsAt: trayTestNow + 1},
			allow: true,
			want:  false,
		},
		{
			// 暂停时冻结着的那次休息：phase 还是 break，但 actionText 是“立即休息”，
			// 那一项跟提前结束无关，灰了会让人以为连休息都发不起来了。
			name:  "暂停 + 冻结的短休息 + 不允许 → 不灰",
			st:    model.SchedulerState{Phase: "break", BreakType: "short", Paused: true, BreakEndsAt: trayTestNow + 1},
			allow: false,
			want:  false,
		},
		{
			// 自定义提醒在弹：托盘这一项写的是“立即休息”，可人已经在休息里 —— 灰。
			name:  "自定义提醒在弹 → 灰",
			st:    model.SchedulerState{Phase: "break", BreakType: "cb-a", BreakEndsAt: trayTestNow + 1},
			allow: true,
			want:  true,
		},
		{
			name:  "外卖提醒在弹 → 灰",
			st:    model.SchedulerState{Phase: "break", BreakType: "takeout", BreakEndsAt: trayTestNow + 1},
			allow: true,
			want:  true,
		},
		{
			name:  "工作中 → 不灰（文案是：立即休息）",
			st:    model.SchedulerState{Phase: "work", NextAt: trayTestNow + 1},
			allow: false,
			want:  false,
		},
		{
			name:  "未安排休息 → 不灰",
			st:    model.SchedulerState{},
			allow: false,
			want:  false,
		},
	}
	for _, c := range cases {
		if got := trayActionDisabled(c.st, c.allow); got != c.want {
			t.Errorf("%s：trayActionDisabled = %v，想要 %v", c.name, got, c.want)
		}
	}
}

// 文案与灰的互斥关系：写“提前结束休息”的只能是短 / 长休息；且这种时候判成灰，
// 唯一理由就是那张卡没勾允许提前结束。
func TestTrayActionLabelAndDisabledAgree(t *testing.T) {
	states := []model.SchedulerState{
		{Phase: "break", BreakType: "short", BreakEndsAt: trayTestNow + 1},
		{Phase: "break", BreakType: "long", BreakEndsAt: trayTestNow + 1},
		{Phase: "break", BreakType: "cb-a", BreakEndsAt: trayTestNow + 1},
		{Phase: "break", BreakType: "takeout", BreakEndsAt: trayTestNow + 1},
		{Phase: "break", BreakType: "short", Paused: true, BreakEndsAt: trayTestNow + 1},
		{Phase: "break", BreakType: "cb-a", Paused: true, BreakEndsAt: trayTestNow + 1},
		{Phase: "work", NextAt: trayTestNow + 1},
		{Phase: "work", IdleWait: true, NextAt: trayTestNow + 1},
		{},
	}
	for _, st := range states {
		_, _, actionText, _ := trayTextsAt(st, false, nil, trayTestNow)
		for _, allow := range []bool{true, false} {
			off := trayActionDisabled(st, allow)
			if actionText == "提前结束休息" {
				if !model.IsRestKind(st.BreakType) {
					t.Errorf("BreakType=%q：文案写成“提前结束休息”，但这不是短 / 长休息", st.BreakType)
				}
				if off && allow {
					t.Errorf("BreakType=%q：允许提前结束却判成灰", st.BreakType)
				}
			}
			// 正在弹非短 / 长休息时，即使配置允许也不该留着可点
			if st.Phase == "break" && !st.Paused && !model.IsRestKind(st.BreakType) && !off {
				t.Errorf("BreakType=%q：自定义 / 外卖提醒在弹时应置灰", st.BreakType)
			}
		}
	}
}

// App 层：refreshTray 算 actionOff 时取的是**当前会话自己那张卡**的配置，
// 以及**当前这次休息到底是不是短 / 长休息**。
func TestTrayActionOffFollowsSessionConfig(t *testing.T) {
	// 不勾允许提前结束的短休息正在休息 → 灰
	a, _ := breakScene(t, noEarlyEndCfg)
	if !a.trayActionOff(a.sched.StateForRenderer()) {
		t.Fatal("不勾「允许提前结束」的短休息正在休息：托盘“提前结束休息”应当置灰")
	}

	// 同一条休息、勾着 → 不灰
	b, _ := breakScene(t, earlyEndCfg)
	if b.trayActionOff(b.sched.StateForRenderer()) {
		t.Fatal("勾着「允许提前结束」：托盘那一项不该置灰")
	}

	// 暂停：冻结着的那次休息 phase 仍是 break，但菜单那一项写的是“立即休息”，不能灰
	c, _ := breakScene(t, noEarlyEndCfg)
	st := c.sched.StateForRenderer()
	st.Paused = true
	if c.trayActionOff(st) {
		t.Fatal("暂停时托盘那一项是“立即休息”，不该跟着提前结束置灰")
	}
}

// 自定义提醒在弹：BreakType 是它自己的 id（不是 short / long），托盘那一项
// 既不叫“提前结束休息”，也必须是灰的 —— 哪怕那张卡勾着允许提前结束。
func TestTrayActionOffForCustomBreak(t *testing.T) {
	a := newExtraApp(t, earlyEndCfg) // cb-a 允许提前结束，照样不认
	s, _ := newTestSched(t, earlyEndCfg)
	s.ResetCycle()
	a.sched = s
	s.BreakNowOf("cb-a")
	a.mu.Lock()
	a.sess = &model.BreakInfo{ID: "cb-a-1", Type: "cb-a", Name: "喝水提醒", DurationMs: 10000}
	a.mu.Unlock()

	st := s.StateForRenderer()
	if st.Phase != "break" || st.BreakType != "cb-a" {
		t.Fatalf("前置条件不成立：phase=%q breakType=%q", st.Phase, st.BreakType)
	}
	if !a.trayActionOff(st) {
		t.Fatal("自定义提醒在弹：托盘那一项应当置灰")
	}
	if _, _, actionText, _ := trayTextsAt(st, false, nil, nowMs()); actionText == "提前结束休息" {
		t.Fatalf("自定义提醒在弹：托盘那一项不该写“提前结束休息”（实际 %q）", actionText)
	}
}
