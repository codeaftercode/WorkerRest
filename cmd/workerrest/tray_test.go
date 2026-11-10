package main

import (
	"testing"

	"workerrest/internal/model"
	"workerrest/internal/plan"
)

func TestFmtTrayAboutMin(t *testing.T) {
	cases := []struct {
		ms   int64
		want string
	}{
		{0, "1 分钟"},                   // 到点未刷新的瞬间也不出“0 分钟”
		{-5_000, "1 分钟"},              // 已过期（负数）兜底
		{20_000, "1 分钟"},              // 不足 1 分钟按 1 分钟
		{44_000, "1 分钟"},              // 44 秒四舍五入 → 1
		{60_000, "1 分钟"},              // 整 1 分钟
		{75_000, "1 分钟"},              // 1.25 分 → 1
		{90_000, "2 分钟"},              // 1.5 分 → 2（四舍五入）
		{9*60_000 + 40_000, "10 分钟"},  // 9 分 40 秒 → 10
		{10*60_000 + 40_000, "11 分钟"}, // 10 分 40 秒 → 11
		{90 * 60_000, "90 分钟"},        // 长间隔也保持分钟（不换算小时）
	}
	for _, c := range cases {
		if got := fmtTrayAboutMin(c.ms); got != c.want {
			t.Errorf("fmtTrayAboutMin(%d) = %q，期望 %q", c.ms, got, c.want)
		}
	}
}

func TestTrayTexts(t *testing.T) {
	now := nowMs()

	t.Run("倒计时中：约数到分钟", func(t *testing.T) {
		tip, status, action, pause := trayTexts(model.SchedulerState{
			Phase: "idle", NextType: "short", NextName: plan.ShortBreakName,
			NextAt: now + 10*60_000,
		}, false)
		if want := "约 10 分钟后短休息"; tip != want {
			t.Errorf("tooltip = %q，期望 %q", tip, want)
		}
		if want := "下次短休息：约 10 分钟后"; status != want {
			t.Errorf("状态行 = %q，期望 %q", status, want)
		}
		if action != "立即休息" || pause != "暂停休息" {
			t.Errorf("动作项 = %q / 暂停项 = %q", action, pause)
		}
	})

	t.Run("没有下一次", func(t *testing.T) {
		tip, status, action, pause := trayTexts(model.SchedulerState{Phase: "idle", NextAt: 0}, false)
		if tip != "未安排休息" || status != "状态：未安排休息" {
			t.Errorf("tooltip = %q / 状态行 = %q", tip, status)
		}
		if action != "立即休息" || pause != "暂停休息" {
			t.Errorf("动作项 = %q / 暂停项 = %q", action, pause)
		}
	})

	t.Run("休息中：秒级剩余", func(t *testing.T) {
		tip, status, action, pause := trayTexts(model.SchedulerState{
			Phase: "break", NextName: plan.ShortBreakName, BreakEndsAt: now + 30_000,
		}, false)
		if want := "休息中，剩余 30 秒"; tip != want {
			t.Errorf("tooltip = %q，期望 %q", tip, want)
		}
		if status != "状态：休息中" || action != "提前结束休息" || pause != "暂停休息" {
			t.Errorf("状态行 = %q / 动作项 = %q / 暂停项 = %q", status, action, pause)
		}
	})

	t.Run("已暂停：立即休息项不留空白", func(t *testing.T) {
		tip, status, action, pause := trayTexts(model.SchedulerState{
			Paused: true, Phase: "idle", NextName: plan.ShortBreakName,
			NextAt: now + 60_000, PauseReasons: []string{"manual"},
			PausedAt: now - 42*60_000,
		}, false)
		if want := "已暂停 42 分钟：手动暂停"; tip != want {
			t.Errorf("tooltip = %q，期望 %q", tip, want)
		}
		if status != "状态：已暂停" {
			t.Errorf("状态行 = %q", status)
		}
		// 暂停时“立即休息”必须仍有标签（曾经的 bug：actionText 留空串把菜单项清空白）
		if action != "立即休息" || pause != "恢复休息" {
			t.Errorf("动作项 = %q / 暂停项 = %q", action, pause)
		}
	})

	t.Run("等待空闲：单独一句，不说“约 0 分钟后”", func(t *testing.T) {
		// 空闲门勾上后到点不马上弹：这一刻 NextAt 已经过去，按分钟取整会说“约 1 分钟后”，
		// 用户会以为坏了 —— 明确写“等待空闲”。
		tip, status, action, pause := trayTexts(model.SchedulerState{
			Phase: "idle", NextType: "short", NextName: plan.ShortBreakName,
			NextAt: now - 1_000, IdleWait: true,
		}, false)
		if want := "等待空闲后提醒短休息"; tip != want {
			t.Errorf("tooltip = %q，期望 %q", tip, want)
		}
		if want := "下次短休息：等待空闲"; status != want {
			t.Errorf("状态行 = %q，期望 %q", status, want)
		}
		if action != "立即休息" || pause != "暂停休息" {
			t.Errorf("动作项 = %q / 暂停项 = %q", action, pause)
		}
	})

	t.Run("等待空闲但已暂停：暂停优先", func(t *testing.T) {
		// 手动 / 勿扰等暂停态下“人在忙”这件事本来就成立，托盘要先说暂停
		_, _, _, pause := trayTexts(model.SchedulerState{
			Paused: true, Phase: "idle", NextType: "short", NextName: plan.ShortBreakName,
			NextAt: now - 1_000, IdleWait: true, PauseReasons: []string{"manual"},
			PausedAt: now - 60_000,
		}, false)
		if pause != "恢复休息" {
			t.Errorf("暂停态下菜单项应为“恢复休息”，得到 %q", pause)
		}
	})
}
