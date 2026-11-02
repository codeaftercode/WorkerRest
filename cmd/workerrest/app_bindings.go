package main

import (
	"encoding/json"
	"github.com/wailsapp/wails/v3/pkg/application"
	"log"
	"runtime"
	"runtime/debug"
	"strings"
	"workerrest/internal/model"
	"workerrest/internal/plan"
	"workerrest/internal/role"
)

// settingsWindow 取设置窗口（按名字查，避免跨 tick 持有已销毁的窗口引用 ——
// 跨 tick 拿着旧窗口再访问会抛 "object has been destroyed"）。
// 返回接口类型 application.Window：Wails 的窗口管理器按名字交付的就是接口。
func (a *App) settingsWindow() application.Window {
	w, ok := a.app.Window.GetByName("settings")
	if !ok {
		return nil
	}
	return w
}

// ShowSettingsWindow 打开（或唤起）设置窗口。窗口在 main.go 里随应用创建，
// 这里负责「按配置决定这次用多大」+ 显示与聚焦——托盘、单实例二次启动、
// 自更新重启恢复都走它。
//
// 每次都过一遍 applySettingsWindowSize：勾了「自动记录窗口尺寸」就用上次的尺寸，
// 没勾就回到默认尺寸（用户中途拖大过窗口，关掉再开也应恢复默认，见 window_size.go）。
func (a *App) ShowSettingsWindow() {
	w := a.settingsWindow()
	if w == nil {
		return
	}
	a.applySettingsWindowSize(w)
	w.Show()
	w.Focus()
}

// SetActiveTab 前端每次切页时上报当前 tab 名（general/timing/supervisor/stats/
// shortcuts/about），用于自更新重启后恢复「更新前所在的页面」。
func (a *App) SetActiveTab(tab string) {
	if tab == "" {
		return
	}
	a.mu.Lock()
	a.activeTab = tab
	a.mu.Unlock()
}

// CloseWindow 渲染层「关窗」入口（休息窗口的预览点任意处 / 设置窗口的关闭按钮）。
// 休息窗口是多窗口（每块目标屏一个，存 a.breakWins），点任意一个预览窗整组关掉；
// 外卖窗口同理走 closeTakeoutWindows。
func (a *App) CloseWindow(win string) {
	if win == "takeout" {
		a.closeTakeoutWindows()
		return
	}
	// 提前提示窗走专用入口：它有自己的建窗锁，通用分支直接 delete(a.wins, win) 会
	// 与「关旧窗 → 建新窗」交错（删掉刚建好的新窗引用，那扇窗就没人关了）。
	if win == preBreakWindowName {
		a.closePreBreakWindow()
		return
	}
	if win == "break" {
		// 预览点任意处关闭（真实休息的结束由调度器 finishBreak 走 closeBreakWindows）：
		// 收尾走同一条路径，只是不出声。预览会话的 info.Preview 会让统计那一段自动跳过。
		a.closeBreakWindowsOpt(true)
		return
	}
	a.mu.Lock()
	w := a.wins[win]
	delete(a.wins, win)
	a.mu.Unlock()
	if w != nil {
		w.Close()
	}
}

// ---------- 绑定给前端的接口（对应 历史源码/preload.ts 的 window.api） ----------

// BreakCurrent 当前休息会话（渲染层首帧拉取；无会话返回 nil）
func (a *App) BreakCurrent(win string) *model.BreakInfo {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sess
}

// Action 窗口上的动作。win 决定动作归属，name 决定做什么：
//
//	调度器类（设置页侧栏按钮 / 托盘 / 快捷键共用同一批名字）：
//	  breakNow / postpone / skip / pauseToggle / resetCycle / endBreak
//	设置窗口自绘标题栏：winMin / winMaxToggle / winClose
//	休息窗口：endBreak（提前结束）、close（预览点任意处）
//
// 与 上一版一致：这些动作全部汇聚到**调度器**上，而不是各自直接开关窗口——
// 否则 phase / 已消费记账 / 派生视图会对不上。
func (a *App) Action(win, name string) error {
	switch name {
	case "breakNow":
		a.schedBreakNow()
		return nil
	case "postpone":
		a.schedPostpone()
		return nil
	case "skip":
		a.schedSkip()
		return nil
	case "pauseToggle":
		a.schedPauseToggle()
		return nil
	case "resetCycle":
		a.schedResetCycle()
		return nil
	case "endBreak":
		log.Printf("[action] endBreak <- %s", win)
		a.schedEndBreak()
		return nil
	}
	if win != "settings" {
		switch name {
		case "close":
			a.CloseWindow(win)
		default:
			log.Printf("[action] 未实现的动作 %q（<- %s）", name, win)
		}
		return nil
	}
	switch name {
	case "winMin":
		if w := a.settingsWindow(); w != nil {
			w.Minimise()
		}
	case "winMaxToggle":
		if w := a.settingsWindow(); w != nil {
			if w.IsMaximised() {
				w.UnMaximise()
			} else {
				w.Maximise()
			}
		}
	case "winClose":
		// 关闭 = 隐藏到托盘（不是销毁）：托盘「设置…」/ 双击要能重新唤起。
		// Wails 的 Window.Close() 会销毁窗口并从注册表移除，之后 GetByName 拿不到、
		// ShowSettingsWindow 就无法再显示 —— 所以这里用 Hide 保留窗口对象。
		// 隐藏前把当前尺寸记下来：防抖 1 秒内关窗时定时器还没到，不主动 flush 就丢了。
		a.rememberSettingsWindowSize()
		a.flushSettingsWindowSize()
		if w := a.settingsWindow(); w != nil {
			w.Hide()
			if a.app != nil && a.app.Event != nil {
				a.app.Event.Emit("settings:hidden", nil)
			}
		}
	default:
		log.Printf("[action] 设置窗口未实现的动作 %q", name)
	}
	return nil
}

// 调度器动作的转发（a.sched 在启动后一定非 nil，这里仍判空以防启动期的调用）
func (a *App) schedBreakNow() {
	if a.sched != nil {
		a.sched.BreakNow()
	}
}
func (a *App) schedPostpone() {
	if a.sched != nil {
		a.sched.Postpone()
	}
}
func (a *App) schedSkip() {
	if a.sched != nil {
		a.sched.Skip()
	}
}

// schedPauseToggle 托盘 / 提示窗 / 快捷键共用的手动「暂停休息 ↔ 恢复休息」。
//
// 自动暂停（应用占用）生效时点「恢复休息」有个特殊语义：不是去摘一个不存在的 manual
// 原因（那会原地 no-op，按钮像坏了），而是**临时忽略目标应用**——立即恢复，且监视器
// 不再因这些应用重新暂停，直到应用全部退出（忽略随退出解除）或用户再次手动暂停
// （忽略作废，回到常规判定）。下次该应用再运行时照常自动暂停。
func (a *App) schedPauseToggle() {
	if a.sched == nil {
		return
	}
	if a.sched.HasPauseReason(pauseReasonAppPause) {
		a.setAppPauseIgnored(true)
		a.sched.Resume(pauseReasonAppPause)
		// 若还叠着手动暂停，一并摘掉：用户点的是「恢复休息」，就该恢复到底
		a.sched.Resume("manual")
		log.Printf("[appPause] 用户手动恢复：临时忽略目标应用，直到退出或手动暂停")
		return
	}
	if !a.sched.IsPaused() {
		// 手动暂停：作废可能残留的忽略会话，回到常规自动暂停判定
		a.setAppPauseIgnored(false)
	}
	a.sched.PauseToggle()
}

// setAppPauseIgnored / isAppPauseIgnored 忽略会话标志（监视 goroutine 与 UI 线程共用）
func (a *App) setAppPauseIgnored(v bool) {
	a.appPauseMu.Lock()
	a.appPauseIgnored = v
	a.appPauseMu.Unlock()
}

func (a *App) isAppPauseIgnored() bool {
	a.appPauseMu.Lock()
	defer a.appPauseMu.Unlock()
	return a.appPauseIgnored
}
func (a *App) schedResetCycle() {
	if a.sched != nil {
		a.sched.ResetCycle()
	}
}
func (a *App) schedEndBreak() {
	if a.sched == nil {
		return
	}
	a.sched.EndBreak()
	// 兜底：调度器已经不在休息阶段时 endBreak 直接返回（自然到点 / 状态已翻篇），
	// 屏幕上的窗口就成了「点了没反应」的窗 —— 用户只能 Alt+F4。这里直接收掉，
	// 绝不把无主窗口留给用户。正常路径上 finishBreak 已经关过窗，这步是空操作。
	if a.sched.StateCopy().Phase != "break" && a.hasBreakWindows() {
		log.Printf("[action] endBreak 无效（调度器不在休息阶段），直接收掉残留的休息窗口")
		a.closeBreakWindowsOpt(true)
	}
}

// SupActivity 页内捕获到键鼠操作 → 广播给其余窗口（多屏同步）。
// 与 上一版同一条铁律：收到广播的窗口不再上报，否则窗口之间回环。
func (a *App) SupActivity(win string) {
	a.app.Event.Emit("sup:active")
}

// PreviewBreak 弹一次预览：顶层「督休专员-启用」开关仍生效（未启用时预览窗直接不显示立绘，
// 与设置页勾选状态一致）；forceKey 指定督休专员（空 = 按配置选），可绕过单个督休专员的启用
// 以预览已停用的督休专员。预览窗口带「预览模式」角标、点任意处关闭，
// 且不影响真实倒计时与统计——与 上一版 break:preview 同语义。
//
// 预览**完全不进调度器**：不消费 occurrence、不改 phase、不占会话（与上一版 一致，
// 那边 break:preview 直接调 showBreakWindows）。
//
// 真实休息窗口在场时**拒弹**（返回 previewBusyReply）：预览会建一个新会话并顶掉当前
// 会话，ShowBreakWindow 随后把旧窗口当 stale 关掉 —— 用户看到的就是「点了预览，正在
// 进行的休息窗被关掉」，而且那次休息的统计 / 结束判定也跟着乱。让真实提醒照常走完。
func (a *App) PreviewBreak(kind, forceKey string) string {
	if kind == "" {
		kind = "short"
	}
	if a.realBreakActive() {
		log.Printf("[break] 真实休息窗口在场，忽略预览请求（kind=%s）", kind)
		return previewBusyReply
	}
	return a.ShowBreakSession(kind, true, forceKey)
}

// previewBusyReply 预览被拒时给渲染层的返回值：真实提醒窗口正在显示。
// 空串另有用处（休息身份不存在等），所以这里用独立取值，前端据此给出提示而不是干等。
const previewBusyReply = "busy"

// realBreakActive 真实休息（非预览）正在进行：会话在场即算。
// 窗口被 Alt+F4 直接关掉时 removeBreakWindow 会清会话，所以这条不会把人永久挡在门外。
func (a *App) realBreakActive() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sess != nil && !a.sess.Preview
}

// StartBreak 立即开始一次指定类型的休息（托盘 / 诊断页）。
// kind 留空 = 用当前计划的那一条（与托盘「立即休息一下」同义）。
//
// 走调度器而不是直接建会话：phase / 已消费记账 / 派生视图都要跟着动，
// 否则到点前又会被 replan 排回来弹第二次。
func (a *App) StartBreak(kind string) string {
	if a.sched == nil {
		return a.ShowBreakSession(kind, false, "")
	}
	return a.sched.BreakNowOf(kind)
}

// GetState 给设置页的状态快照。
//
// 字段集**必须完整**：设置页的 renderStatus() 一上来就读 st.paused /
// st.pauseReasons.length / st.phase，缺一个就在 init 里抛错、整页停摆
// （见 历史源码/types.ts 的 SchedulerState 与 main.ts 的 stateForRenderer）。
func (a *App) GetState() map[string]any {
	st := model.SchedulerState{
		Phase: "idle", NextType: "short", NextName: plan.ShortBreakName,
		Lines: []model.PlannedBreak{}, Timeline: []model.TimelineItem{},
		FixedNext: map[string]int64{}, PauseReasons: []string{},
	}
	if a.sched != nil {
		st = a.sched.StateForRenderer()
	}
	out := structToMap(st)
	// Go 侧扩展字段（上一版没有；诊断与控制台用）
	out["engine"] = "go+wails3"
	out["rolesRoot"] = a.rolesRoot
	out["settings"] = orNone(a.CfgSnapshot().Path())
	out["serverMode"] = serverMode
	return out
}

// structToMap 把结构体转成 map（走一次 JSON 往返，字段名与 json tag 完全一致）
func structToMap(v any) map[string]any {
	b, err := json.Marshal(v)
	if err != nil {
		return map[string]any{}
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return map[string]any{}
	}
	return m
}

// AppInfo 关于页的环境信息。
//
// 如实回报引擎为 Wails v3 (Go)，不伪装成 上一版。设置页「软件信息」卡片现在只渲染
// version / buildTime（发布时间）两行；goVersion / wailsVersion 仍然回传，供探针与
// 排障判断跑的是哪个引擎版本（尤其是 beta 与正式版之分），只是不再上界面。
func (a *App) AppInfo() map[string]any {
	goVer := strings.TrimPrefix(runtime.Version(), "go")
	return map[string]any{
		"name":         a.CfgSnapshot().AppName(),
		"version":      appVersion,
		"electron":     "Wails v3 (Go)",
		"node":         "-",
		"engine":       "go+wails3",
		"platform":     "windows",
		"roles":        len(role.ScanRoles(a.rolesRoot)),
		"goVersion":    goVer,
		"wailsVersion": wailsVersion(),
		"buildTime":    buildTime,
	}
}

// wailsVersion 取 Wails 的真实版本号，读不到时退回 "v3"。
//
// 以前这里写死 "v3"：beta 与正式版在关于页长得一模一样，用户报 bug 时说不清自己装的
// 是哪一版。改成读构建信息里的模块版本（-s -w 只剥符号与调试信息，modinfo 仍在）。
// 模块版本缺失（源码占位构建、依赖信息被裁剪）或仍是 (devel) 时，退回宽口径 "v3" ——
// 宁可少显示一点，也不要在关于页摆一个空字符串。
func wailsVersion() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, d := range bi.Deps {
			if d.Path == "github.com/wailsapp/wails/v3" {
				if v := strings.TrimSpace(d.Version); v != "" && v != "(devel)" {
					return v
				}
			}
		}
	}
	return "v3"
}

// ListRoles 督休专员目录扫描结果（预览图为 data URL，设置页督休专员列表用）
// ListRoles 候选督休专员列表（设置页画廊 / 主窗口共用）。
// 顺序按用户拖动保存的 supervisor.order 排 —— 画廊显示顺序与「顺序轮换」的出场次序
// 共用这一份顺序，用户拖一次两边同时生效。
func (a *App) ListRoles() []role.Role {
	roles := role.ScanRoles(a.rolesRoot)
	if cfg := a.CfgSnapshot(); cfg != nil {
		roles = role.ApplyRoleOrder(roles, cfg.SupervisorCfg().Order)
	}
	// 空目录（便携包被删了 assets/supervisors、或首次运行素材还没落地）时 ScanRoles
	// 返回 nil，JSON 序列化成 null；前端是 `for (const r of ROLES)`，拿到 null 会直接
	// 抛 TypeError 让整个立绘库白屏。这里统一给空数组，让「没有专员」是一种正常状态。
	if roles == nil {
		return []role.Role{}
	}
	return roles
}

// QuitApp 退出应用（托盘 / 主窗口按钮）
func (a *App) QuitApp() {
	log.Println("[app] 退出请求")
	a.app.Quit()
}

// WindowMaximized 设置窗口当前是否最大化（自绘标题栏首帧拉一次，之后靠事件推）
func (a *App) WindowMaximized() bool {
	if w := a.settingsWindow(); w != nil {
		return w.IsMaximised()
	}
	return false
}

// appVersion 的唯一来源是 git tag（格式 v主.次.修订）：打包时由
// scripts/make_portable.py 通过 -ldflags "-X main.appVersion=<tag 去掉 v>" 注入。
// 用 var 而非 const：源码里只留 "dev" 占位符（开发 / 无头验证跑出来就是 dev），
// 不在源码里写死真实版本号，避免与 tag 分裂。
var appVersion = "dev"

// buildTime 打包时通过 -ldflags "-X main.buildTime=<RFC3339>" 注入，关于页以「发布时间」
// 展示；开发 / 无头验证不注入则留空，前端回退显示「本地构建」。
var buildTime = ""

// pauseReasonText 暂停原因键 → 界面文案：内部用稳定的英文键，展示层不该原样抛给用户；
// 中文原因（'勿扰' / '应用占用'）原样返回，未知键也不会消失（与 types.ts 同口径）。
func pauseReasonText(reason string) string {
	switch reason {
	case "idle":
		return "系统空闲"
	case "manual":
		return "手动暂停"
	default:
		return reason
	}
}

func pauseReasonsText(reasons []string) string {
	if len(reasons) == 0 {
		return "已暂停"
	}
	out := make([]string, 0, len(reasons))
	for _, r := range reasons {
		out = append(out, pauseReasonText(r))
	}
	return strings.Join(out, "、")
}
