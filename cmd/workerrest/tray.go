package main

import (
	"fmt"
	"log"
	"strings"
	"time"
	"workerrest/internal/logx"
	"workerrest/internal/model"
	"workerrest/internal/settings"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// 系统托盘：替代 上一版的 tray.ts。v3 内置 SystemTray（v2 需第三方库）。
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
		// 右键弹出前先重算一次：原生菜单打开后不会自己重绘，这里补一刀，
		// 保证菜单顶部的“下次…：约 X 分钟后”是刚算出来的值（不等下一秒的 ticker）。
		a.refreshTray()
		tray.OpenMenu()
	})

	menu := a.app.NewMenu()
	status := menu.Add("状态：努力工作中")
	status.SetEnabled(false)
	menu.AddSeparator()
	action := menu.Add("立即休息").OnClick(func(_ *application.Context) {
		if a.sched != nil && a.sched.CurrentPhase() == "break" {
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
	// “暂停一段时间”：会议室场景的入口。进会议室前点一下，到点自己结束。
	// 单独一项而不是把“暂停休息”改成子菜单 —— 最高频的一键暂停/恢复不该多一次点击。
	timed := menu.AddSubmenu("暂停一段时间")
	timed30 := timed.Add("暂停 30 分钟").OnClick(func(_ *application.Context) {
		a.schedPauseFor(pauseFor30Min)
	})
	timed60 := timed.Add("暂停 1 小时").OnClick(func(_ *application.Context) {
		a.schedPauseFor(pauseFor1Hour)
	})
	timed120 := timed.Add("暂停 2 小时").OnClick(func(_ *application.Context) {
		a.schedPauseFor(pauseFor2Hour)
	})
	timedTomorrow := timed.Add("暂停到明天").OnClick(func(_ *application.Context) {
		a.schedPauseUntilTomorrow()
	})
	timed.AddSeparator()
	// 只在“正在定时暂停”时出现：把 1 小时改回一直暂停
	timedCancel := timed.Add("取消定时（一直暂停）").OnClick(func(_ *application.Context) {
		a.schedPauseFor(0)
	})
	timedCancel.SetHidden(true)
	menu.AddSeparator()
	menu.Add("设置…").OnClick(func(_ *application.Context) {
		a.ShowSettingsWindow()
	})
	// 与设置页的“开机自启动”共用同一份 settings.autostart：勾哪儿都改配置 + 改注册表，
	// 并把结果广播回设置页（上一版 toggleAutostartNow 的三件事）。
	auto := menu.AddCheckbox("开机自启动", a.CfgSnapshot().Autostart())
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
	a.trayTimed = []*application.MenuItem{timed30, timed60, timed120, timedTomorrow}
	a.trayTimedCancel = timedCancel
	a.mu.Unlock()
	log.Printf("[tray] 托盘就绪（单击打开设置）")
	a.refreshTray()
	// 剩余时间在流逝、但调度器状态没变时也要推：tooltip / 菜单里的倒计时不能停在
	// 最后一次事件的值上（用户反馈“x分x秒后”不动的根因就在这）。
	a.startTrayRefresh()
}

// ---------- 定时刷新 ----------

// trayRefreshInterval 托盘文案的刷新间隔。用户要求“至少每分钟更新一次”，这里取 1 秒
// 叠加“文案不变不写”（见 applyTrayLabels）：分钟级文案一分钟才真正落一次原生调用，
// 休息中的秒级剩余也能秒级跟上，其余 tick 只做纯内存计算、不产生主线程往返。
const trayRefreshInterval = time.Second

// startTrayRefresh 起托盘定时刷新循环。server 模式没有托盘（startTray 直接返回），
// 不会走到这里；重复调用安全（已在跑就跳过）。
func (a *App) startTrayRefresh() {
	a.mu.Lock()
	if a.trayStop != nil {
		a.mu.Unlock()
		return
	}
	a.trayStop = make(chan struct{})
	ch := a.trayStop
	a.mu.Unlock()
	go func() {
		t := time.NewTicker(trayRefreshInterval)
		defer t.Stop()
		for {
			select {
			case <-ch:
				return
			case <-t.C:
				logx.GuardRun("tray.refresh", a.refreshTray)
			}
		}
	}()
}

// stopTrayRefresh 停止托盘定时刷新（退出清理）
func (a *App) stopTrayRefresh() {
	a.mu.Lock()
	ch := a.trayStop
	a.trayStop = nil
	a.mu.Unlock()
	if ch != nil {
		close(ch)
	}
}

// trayLabels 上一次写到托盘提示与菜单的文案快照（a.mu 保护）。秒级刷新每秒重算，
// 但只在文案真正变化时才调原生 Set* —— 见 applyTrayLabels。
type trayLabels struct {
	tip       string
	status    string
	action    string
	pause     string
	autostart bool
	valid     bool // false = 还没写过；首写一律全量落一次
}

// toggleAutostart 托盘勾选框：翻转 settings.autostart → 落盘 → 同步注册表 → 通知设置页。
//
// 勾选框的视觉状态由原生菜单自己翻；这里落盘后 refreshTray 会用配置里的真值再 SetChecked 一次，
// 所以“写注册表失败”也不会让界面与配置脱节（配置才是唯一真源）。
func (a *App) toggleAutostart() {
	a.mu.Lock()
	next := !a.cfg.Autostart()
	if merged, ok := settings.DeepMerge(a.cfg.Raw(), map[string]any{"autostart": next}).(map[string]any); ok {
		s := settings.New(merged, a.cfg.Path())
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

// refreshTray 按当前调度状态刷新托盘提示与菜单文案（对应 上一版的 trayUI.refresh）
func (a *App) refreshTray() {
	a.mu.Lock()
	tray, status, action, pause := a.tray, a.trayStatus, a.trayAction, a.trayPause
	auto := a.trayAutostart
	a.mu.Unlock()
	if tray == nil || a.sched == nil {
		return
	}
	st := a.sched.StateForRenderer()
	tip, statusText, actionText, pauseText := trayTextsAt(st, a.allAutoReasonsCleared(),
		a.pauseHoldSnapshot(), nowMs())
	// 开机自启动的勾以**配置**为准（不是以“刚才点了什么”为准）：设置页里改、导入配置改、
	// 恢复默认改，都会走到这里把托盘那一项对齐。
	next := trayLabels{
		tip:       tip,
		status:    statusText,
		action:    actionText,
		pause:     pauseText,
		autostart: auto != nil && a.CfgSnapshot().Autostart(),
		valid:     true,
	}
	a.applyTrayLabels(tray, status, action, pause, auto, next)
	a.syncTimedPauseMenu(st)
}

// timedPauseLabels 子菜单四项的标签，按暂停状态切换。
//
// 暂停中不用“暂停 30 分钟”这四个字：那会让人以为是要再暂停一次，而实际语义是
// “把已经开始的这段改成 30 分钟后结束”。也说清“…结束”，避免被读成“再延长 30 分钟”。
func timedPauseLabels(paused bool) []string {
	if paused {
		return []string{"改到 30 分钟后结束", "改到 1 小时后结束", "改到 2 小时后结束", "改到明天 0 点"}
	}
	return []string{"暂停 30 分钟", "暂停 1 小时", "暂停 2 小时", "暂停到明天"}
}

// syncTimedPauseMenu 按状态改写子菜单标签、显隐“取消定时”。
//
// 只在形态真正变化时写原生菜单（与 applyTrayLabels 同一套理由）：托盘每秒刷新，
// 每次都 SetLabel 会让菜单不断重绘。
func (a *App) syncTimedPauseMenu(st model.SchedulerState) {
	a.mu.Lock()
	items, cancel := a.trayTimed, a.trayTimedCancel
	prev := a.trayTimedKey
	key := "idle"
	if st.Paused {
		if st.PauseDeadline > 0 {
			key = "timed"
		} else {
			key = "paused"
		}
	}
	a.trayTimedKey = key
	a.mu.Unlock()
	if len(items) == 0 || prev == key {
		return
	}
	labels := timedPauseLabels(st.Paused)
	for i, it := range items {
		if i < len(labels) {
			it.SetLabel(labels[i])
		}
	}
	if cancel != nil {
		cancel.SetHidden(!(st.Paused && st.PauseDeadline > 0))
	}
}

// trayTexts 按调度状态算出托盘提示与菜单文案（纯函数：不碰原生对象，便于单测；
// refreshTray 只负责把它们落到托盘 / 菜单上）。
//
// 提示语**不带标题前缀**：托盘图标本身就是身份，而标题由用户自定义、可能很长，
// 不该把“还剩多久”这半句挤掉（Windows 工具提示的宽度有限）。
//
// pausedCleared：当前挂着的自动原因是否都已“条件消失”。是的话提示里补一句
// “可以恢复了”—— 自动暂停现在不自己恢复（见 autoResumeOnClear），用户想知道
// 什么时候该去点恢复，只有托盘这一处能告诉他。
func trayTexts(st model.SchedulerState, pausedCleared bool) (tip, statusText, actionText, pauseText string) {
	return trayTextsAt(st, pausedCleared, nil, nowMs())
}

// trayTextsAt 同 trayTexts，额外接“现在”与“到期被什么挡住”——两者都只在定时暂停
// 分支用到，拆出来是为了让“还有 42 分钟 / 时长已到，等全屏结束”这些时序可测。
func trayTextsAt(st model.SchedulerState, pausedCleared bool, hold []string, now int64) (tip, statusText, actionText, pauseText string) {
	switch {
	case st.Paused:
		if st.PauseDeadline > 0 {
			// 定时暂停：说“还剩多久”比说“已暂停多久”有用。一夜过去看到“已暂停 14 小时”
			// 像出了故障，而“还有 3 小时（到明天 0:00）”是在回答他真正关心的事。
			tip, statusText = timedPauseText(st.PauseDeadline, hold, now)
		} else {
			tip = "已暂停 " + fmtTrayPaused(max0(now-st.PausedAt)) + "：" + pauseReasonsText(st.PauseReasons)
			if pausedCleared {
				tip += "（已结束，可以恢复）"
				statusText = "状态：已暂停（条件已结束）"
			} else {
				statusText = "状态：已暂停"
			}
		}
		// “立即休息”是常驻项（见本文件顶部的菜单结构说明）：暂停时也要留着标签，
		// 不能像以前那样留个空串把菜单项清成空白。
		actionText = "立即休息"
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
		// 约数到分钟（用户不要求秒级精度）：tooltip“约 10 分钟后短休息”、
		// 菜单状态行“下次短休息：约 10 分钟后”。
		about := fmtTrayAboutMin(max0(st.NextAt - nowMs()))
		tip = "约 " + about + "后" + st.NextName
		statusText = "下次" + st.NextName + "：约 " + about + "后"
		actionText = "立即休息"
		pauseText = "暂停休息"
	}
	return tip, statusText, actionText, pauseText
}

// applyTrayLabels 只在文案变化时才写原生托盘/菜单。秒级刷新循环每秒都会走到这，
// 而“约 X 分钟后”一分钟才变一次、其余状态文案只在状态翻转时变 —— 变化检测让
// 绝大多数 tick 什么都不做（既省主线程往返，也避免无谓的菜单重绘）。
func (a *App) applyTrayLabels(tray *application.SystemTray, status, action, pause, auto *application.MenuItem, next trayLabels) {
	a.mu.Lock()
	prev := a.trayLast
	a.trayLast = next
	a.mu.Unlock()
	if !prev.valid {
		// 首写（托盘刚就绪）一律全量落一次
		tray.SetTooltip(next.tip)
		status.SetLabel(next.status)
		action.SetLabel(next.action)
		pause.SetLabel(next.pause)
		if auto != nil {
			auto.SetChecked(next.autostart)
		}
		return
	}
	if prev.tip != next.tip {
		tray.SetTooltip(next.tip)
	}
	if prev.status != next.status {
		status.SetLabel(next.status)
	}
	if prev.action != next.action {
		action.SetLabel(next.action)
	}
	if prev.pause != next.pause {
		pause.SetLabel(next.pause)
	}
	if auto != nil && prev.autostart != next.autostart {
		auto.SetChecked(next.autostart)
	}
}

// timedPauseText 定时暂停的提示语。
//
// 三种形态：还有多久（正常）、时长已到且被挡住（点名在等什么）、时长已到但没挡住
// （理论上一拍就会恢复，只在 watch 没跑起来时出现，兜底用）。
func timedPauseText(deadline int64, hold []string, now int64) (tip, status string) {
	if pauseExpired(deadline, now) {
		if len(hold) > 0 {
			return "暂停时长已到，等“" + pauseHoldText(hold) + "”结束",
				"状态：暂停（时长已到）"
		}
		return "暂停时长已到", "状态：暂停（时长已到）"
	}
	remain := fmtPauseRemain(deadline - now)
	return "暂停中 · 还有 " + remain + "（到 " + fmtPauseAt(deadline, now) + "）",
		"状态：暂停中（还有 " + remain + "）"
}

// fmtPauseRemain 定时暂停的剩余时长：约数（四舍五入到分钟），超过一小时换算成小时。
// 与 fmtTrayPaused 的向下取整刻意相反 —— 那个是既成事实，这个是约数。
func fmtPauseRemain(ms int64) string {
	if ms <= 0 {
		return "不到 1 分钟"
	}
	m := (ms + 30_000) / 60_000
	if m < 1 {
		return "不到 1 分钟"
	}
	if m < 60 {
		return fmt.Sprintf("%d 分钟", m)
	}
	h, mm := m/60, m%60
	if mm == 0 {
		return fmt.Sprintf("%d 小时", h)
	}
	return fmt.Sprintf("%d 小时 %d 分钟", h, mm)
}

// fmtPauseAt 到期时刻的展示：当天“15:04”、次日“明天 0:00”、更远“11月7日 0:00”。
// 只给托盘提示用，不参与判定。
func fmtPauseAt(deadline, now int64) string {
	t := time.UnixMilli(deadline)
	n := time.UnixMilli(now)
	if sameLocalDay(t, n) {
		return t.Format("15:04")
	}
	if sameLocalDay(t, n.AddDate(0, 0, 1)) {
		return "明天 " + t.Format("15:04")
	}
	return fmt.Sprintf("%d月%d日 %s", int(t.Month()), t.Day(), t.Format("15:04"))
}

// sameLocalDay 两个时刻是否落在本地同一天。
func sameLocalDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}

// fmtTrayRemain 托盘提示的倒计时文案（与 tray.ts 的 fmt 逐条对齐）：
// “45 秒”“9 分 30 秒”“1 小时 30 分 30 秒”——
// 超过 60 分钟换算成小时，托盘里出现“90 分 30 秒”很难读。
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

// fmtTrayAboutMin 分钟级约数（只返回“N 分钟”这段，“约 … 后”由调用方拼）：
// 托盘 tooltip 与菜单状态行共用。用户不要求秒级精度、刷新也是分钟级，故四舍五入到
// 分钟；不足 1 分钟按 1 分钟计（免得出“约 0 分钟后”这种怪话）。
func fmtTrayAboutMin(ms int64) string {
	s := ms / 1000
	if s < 0 {
		s = 0
	}
	m := (s + 30) / 60 // 四舍五入到分钟
	if m < 1 {
		m = 1
	}
	return fmt.Sprintf("%d 分钟", m)
}

// fmtTrayPaused 已经暂停了多久（托盘提示用）。
//
// 与 fmtTrayAboutMin 的取舍相反：这里**向下取整**，因为“暂停了多久”是既成事实，
// 四舍五入到“已暂停 2 分钟”而实际只有 1 分 10 秒会显得软件在说谎。
// 不足一分钟单独说，避免出现“已暂停 0 分钟”。
func fmtTrayPaused(ms int64) string {
	if ms < 60_000 {
		return "不到 1 分钟"
	}
	m := ms / 60_000
	if m < 60 {
		return fmt.Sprintf("%d 分钟", m)
	}
	h, mm := m/60, m%60
	if mm == 0 {
		if h < 24 {
			return fmt.Sprintf("%d 小时", h)
		}
		return fmt.Sprintf("%d 天 %d 小时", h/24, h%24)
	}
	return fmt.Sprintf("%d 小时 %d 分钟", h, mm)
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
