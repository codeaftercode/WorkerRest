package main

import (
	"fmt"
	"log"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// 系统托盘：替代 Electron 版的 tray.ts。v3 内置 SystemTray（v2 需第三方库）。
//
// 菜单结构与 tray.ts 的 buildMenu 逐项对齐：状态行（禁用）→ 立即休息 / 提前结束 →
// 重置休息周期 → 暂停 / 恢复 → 设置… → 开机自启动（勾选框）→ 退出。
// 文案随调度器状态变的三项各留一个引用，状态一变就 SetLabel——
// 比每次重建菜单省事，也不会把原生菜单整个换掉。
func (a *App) startTray() {
	if serverMode {
		// 无头服务模式没有原生托盘
		return
	}
	tray := a.app.SystemTray.New()
	tray.SetIcon(trayIcon)
	// 单击 / 双击都打开设置（与 tray.ts 的 tray.on('click', openSettings) 一致；
	// Windows 托盘习惯是双击，但单击也不拦着 —— 两种都接到显示设置窗口）
	tray.OnClick(func() {
		a.ShowSettingsWindow()
	})
	tray.OnDoubleClick(func() {
		a.ShowSettingsWindow()
	})
	tray.OnRightClick(func() {
		tray.OpenMenu()
	})

	menu := a.app.NewMenu()
	status := menu.Add("状态：努力工作中")
	status.SetEnabled(false)
	menu.AddSeparator()
	action := menu.Add("立即休息").OnClick(func(_ *application.Context) {
		if a.sched != nil && a.sched.currentPhase() == "break" {
			a.schedEndBreak()
		} else {
			a.schedBreakNow()
		}
	})
	menu.Add("重置休息周期").OnClick(func(_ *application.Context) {
		a.schedResetCycle()
	})
	pause := menu.Add("暂停休息").OnClick(func(_ *application.Context) {
		a.schedPauseToggle()
	})
	menu.AddSeparator()
	menu.Add("设置…").OnClick(func(_ *application.Context) {
		a.ShowSettingsWindow()
	})
	// 与设置页的「开机自启动」共用同一份 settings.autostart：勾哪儿都改配置 + 改注册表，
	// 并把结果广播回设置页（Electron 版 toggleAutostartNow 的三件事）。
	auto := menu.AddCheckbox("开机自启动", a.cfgSnapshot().Autostart())
	auto.OnClick(func(_ *application.Context) {
		a.toggleAutostart()
	})
	menu.AddSeparator()
	menu.Add("退出").OnClick(func(_ *application.Context) {
		a.QuitApp()
	})
	tray.SetMenu(menu)

	a.mu.Lock()
	a.tray = tray
	a.trayStatus = status
	a.trayAction = action
	a.trayPause = pause
	a.trayAutostart = auto
	a.mu.Unlock()
	log.Printf("[tray] 托盘就绪（单击打开设置）")
	a.refreshTray()
}

// toggleAutostart 托盘勾选框：翻转 settings.autostart → 落盘 → 同步注册表 → 通知设置页。
//
// 勾选框的视觉状态由原生菜单自己翻；这里落盘后 refreshTray 会用配置里的真值再 SetChecked 一次，
// 所以「写注册表失败」也不会让界面与配置脱节（配置才是唯一真源）。
func (a *App) toggleAutostart() {
	a.mu.Lock()
	next := !a.cfg.Autostart()
	if merged, ok := deepMerge(a.cfg.Raw(), map[string]any{"autostart": next}).(map[string]any); ok {
		s := &Settings{raw: merged, path: a.cfg.Path()}
		if err := s.Save(); err == nil {
			a.cfg = s
		}
	}
	now := a.cfg.Autostart()
	a.mu.Unlock()

	if now != next {
		log.Printf("[tray] 开机自启动写入失败，保持原值")
	}
	a.app.Event.Emit("autostart", now)
	a.ApplyAutostart()
	a.refreshTray()
	log.Printf("[tray] 开机自启动 → %v", now)
}

// refreshTray 按当前调度状态刷新托盘提示与菜单文案（对应 Electron 的 trayUI.refresh）
func (a *App) refreshTray() {
	a.mu.Lock()
	tray, status, action, pause := a.tray, a.trayStatus, a.trayAction, a.trayPause
	auto := a.trayAutostart
	a.mu.Unlock()
	if tray == nil || a.sched == nil {
		return
	}
	st := a.sched.stateForRenderer()

	// 提示语**不带标题前缀**：托盘图标本身就是身份，而标题由用户自定义、可能很长，
	// 不该把「还剩多久」这半句挤掉（Windows 工具提示的宽度有限）。
	var tip, statusText, actionText, pauseText string
	switch {
	case st.Paused:
		tip = "已暂停：" + pauseReasonsText(st.PauseReasons)
		statusText = "状态：已暂停"
		pauseText = "恢复休息"
	case st.Phase == "break":
		// 暂停期间剩余时间冻结在 pausedAt，但暂停已被上一个分支拦下，这里按当前时间算
		tip = "休息中，剩余 " + fmtTrayRemain(max0(st.BreakEndsAt-nowMs()))
		statusText = "状态：休息中"
		actionText = "提前结束休息"
		pauseText = "暂停休息"
	case st.NextAt == 0:
		// 短/长休息都关闭，或固定时间模式下一刻都没配：没有下一次
		tip = "未安排休息"
		statusText = "状态：未安排休息"
		actionText = "立即休息"
		pauseText = "暂停休息"
	default:
		left := max0(st.NextAt - nowMs())
		tip = fmtTrayRemain(left) + "后" + st.NextName
		statusText = "下次" + st.NextName + "：" + fmtTrayRemain(left)
		actionText = "立即休息"
		pauseText = "暂停休息"
	}
	tray.SetTooltip(tip)
	status.SetLabel(statusText)
	action.SetLabel(actionText)
	pause.SetLabel(pauseText)
	// 开机自启动的勾以**配置**为准（不是以「刚才点了什么」为准）：设置页里改、导入配置改、
	// 恢复默认改，都会走到这里把托盘那一项对齐。
	if auto != nil {
		auto.SetChecked(a.cfgSnapshot().Autostart())
	}
}

// fmtTrayRemain 托盘提示的倒计时文案（与 tray.ts 的 fmt 逐条对齐）：
// 「45 秒」「9 分 30 秒」「1 小时 30 分 30 秒」——
// 超过 60 分钟换算成小时，托盘里出现「90 分 30 秒」很难读。
func fmtTrayRemain(ms int64) string {
	s := (ms + 999) / 1000 // 与 JS 的 Math.ceil(ms / 1000) 同义
	if s < 60 {
		return fmt.Sprintf("%d 秒", s)
	}
	m := s / 60
	r := s % 60
	if m < 60 {
		return fmt.Sprintf("%d 分 %d 秒", m, r)
	}
	h := m / 60
	mm := m % 60
	out := fmt.Sprintf("%d 小时", h)
	if mm > 0 {
		out += fmt.Sprintf(" %d 分", mm)
	}
	if r > 0 {
		out += fmt.Sprintf(" %d 秒", r)
	}
	return out
}

// fmtTrayShort 紧凑倒计时（日志与诊断页用，与 tray.ts 的 fmtShort 同义）
func fmtTrayShort(ms int64) string {
	s := (ms + 999) / 1000
	if s < 60 {
		return fmt.Sprintf("%ds", s)
	}
	m := s / 60
	if m < 60 {
		if r := s % 60; r > 0 {
			return fmt.Sprintf("%dm%ds", m, r)
		}
		return fmt.Sprintf("%dm", m)
	}
	h := m / 60
	mm := m % 60
	parts := []string{fmt.Sprintf("%dh", h)}
	if mm > 0 {
		parts = append(parts, fmt.Sprintf("%dm", mm))
	}
	if r := s % 60; r > 0 {
		parts = append(parts, fmt.Sprintf("%ds", r))
	}
	return strings.Join(parts, "")
}
