package main

import (
	"fmt"
	"log"
	"net/url"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// 休息前提示弹窗（右下角那个带倒计时与动作按钮的小窗）：
// electron/src/windows/preBreakWindow.ts 的对应物。
//
// 为什么不用系统通知：Windows 的通知带不了按钮，而这里需要「立即休息 / 推迟休息 /
// 跳过休息 / 暂停计时」四个动作，所以是自绘窗口。
//
// 与 Electron 版逐条对齐的三处，都是踩过才知道要这么写的：
//
// 一、版式随 URL query 带过去（custom / tail），渲染层第一帧就按它铺。否则会先画一遍
// 内置版式（四个按钮 + 两行说明），等 500ms 轮询回来才切成自定义版式，而**窗口高度是
// 按版式给的** —— 肉眼能看到一次高度跳动。
//
// 二、高度按内容定：240 = 有说明（内置短 / 长），172 = 无说明（自定义提醒）。
// 窗口高度等于内容高时按钮才不贴住文字。
//
// 三、位置固定在主显示器工作区右下角，避免遮住屏幕中央正在工作的内容。
//
// 内容（标题 / 版式 / 倒计时后缀）由主进程**在弹窗打开时算一次**并记住：窗口里每 500ms
// 轮询 PreBreakState 拿的是同一份，中途计划漂移不会让标题跟着变（与 Electron 的
// preBreakInfo() 同口径）。名称只有这一份，渲染层不自己拼。

// PreBreakInfo 与「哪一种提醒」绑定的弹窗内容
type PreBreakInfo struct {
	// Custom 自定义提醒：另一套版式（倒计时带名称、不显示两行说明、按钮改「…提醒」
	// 且没有「暂停计时」）
	Custom bool `json:"custom"`
	// Title 标题：自定义提醒是「XX时间到」，内置短 / 长是劝说文案
	Title string `json:"title"`
	// CountdownTail 倒计时数字后面那段（内置「 秒进入休息」/ 自定义「 秒显示喝水提醒」）
	CountdownTail string `json:"countdownTail"`
}

// builtinPreBreakTitle 内置短 / 长的标题。它们的名字（短休息 / 长休息）不是用户起的，
// 说「短休息时间到」很怪 —— 保持原来的劝说文案与原来的四按钮版式（含两行说明）。
const builtinPreBreakTitle = "您已连续工作较长时间，建议您短暂休息"

// builtinPreBreakTail 内置版的倒计时后缀；自定义提醒那套见 preBreakCountdownTail
const builtinPreBreakTail = " 秒进入休息"

// 标题与倒计时后缀的纯函数 preBreakTitle / preBreakCountdownTail 在计划层迁移时就搬进了
// breakplan.go（对应 electron/src/types.ts 的同名函数），这里直接复用，不再重复定义。

// preBreakInfoOfType 按休息身份算弹窗内容。kind 是内置身份（short / long）时走内置版式，
// 其余按自定义提醒的名字生成（自定义提醒的 id 前缀是 cb-，见 normalizeCustomBreaks）。
func (a *App) preBreakInfoOfType(kind string) PreBreakInfo {
	if kind == "short" || kind == "long" {
		return PreBreakInfo{Custom: false, Title: builtinPreBreakTitle, CountdownTail: builtinPreBreakTail}
	}
	name := a.cfgSnapshot().BreakNameByID(kind)
	return PreBreakInfo{Custom: true, Title: preBreakTitle(name), CountdownTail: preBreakCountdownTail(name)}
}

// preBreakInfoOf 当前计划里那一条的内容（调度器没起来时按短休息算）
func (a *App) preBreakInfoOf() PreBreakInfo {
	kind := "short"
	if a.sched != nil {
		kind = a.sched.stateCopy().NextType
	}
	return a.preBreakInfoOfType(kind)
}

// currentPreBreakInfo 取「当前窗口该显示的内容」。从没弹过时是一个安全的内置版空壳：
// 渲染层拿到空 tail 会把倒计时后缀清空，所以这里兜底成内置后缀。
func (a *App) currentPreBreakInfo() PreBreakInfo {
	a.mu.Lock()
	defer a.mu.Unlock()
	info := a.preInfo
	if info.CountdownTail == "" {
		info.CountdownTail = builtinPreBreakTail
	}
	if !info.Custom && info.Title == "" {
		info.Title = builtinPreBreakTitle
	}
	return info
}

// rememberPreBreakInfo 算一次弹窗内容并记下 —— 窗口内每 500ms 轮询读的就是这份，
// 所以中途计划漂移不会让标题 / 倒计时后缀跟着变（与 Electron 的模块级 info 同口径）。
func (a *App) rememberPreBreakInfo(kind string) PreBreakInfo {
	info := a.preBreakInfoOfType(kind)
	a.mu.Lock()
	a.preInfo = info
	a.mu.Unlock()
	return info
}

// 窗口尺寸：宽固定 400（与 Electron 同值）；高按版式两档，见文件头说明。
const (
	preBreakWidth     = 400
	preBreakHeightDef = 240 // 内置版式：标题 + 倒计时 + 两行说明 + 四个按钮
	preBreakHeightCus = 172 // 自定义版式：没有两行说明，也没有「暂停计时」
	preBreakMargin    = 24  // 距工作区右下角的留白
)

// preBreakBounds 提前提示窗的位置与大小（纯函数，单测直接验）。
// wa 是目标显示器的工作区（application.Rect，字段 X/Y/Width/Height 全为 int）。
func preBreakBounds(wa application.Rect, custom bool) (x, y, w, h int) {
	w = preBreakWidth
	h = preBreakHeightDef
	if custom {
		h = preBreakHeightCus
	}
	x = wa.X + wa.Width - w - preBreakMargin
	y = wa.Y + wa.Height - h - preBreakMargin
	return
}

// preBreakURL 弹窗页面地址。版式经 query 下发（渲染层读 location.search，见文件头第一条）。
// tail 里带中文与空格，必须逐段编码 —— 直接把原文拼进 query 会被 WebView2 当非法字符丢掉。
func preBreakURL(info PreBreakInfo) string {
	flag := "0"
	if info.Custom {
		flag = "1"
	}
	return "/prebreak.html?custom=" + flag + "&tail=" + url.QueryEscape(info.CountdownTail) + "&win=prebreak"
}

// preBreakWindowName 窗口名：Wails 按名字交付窗口，清理时也按它查
const preBreakWindowName = "prebreak"

// ShowPreBreakWindow 弹出（或重弹）提前提示窗。
//
// 每次都先关掉旧的：暂停后恢复会再走一次 onPreNotify，不能叠出两个窗口。
// 无头（-tags server）构建下没有原生窗口，只记内容不建窗 —— 状态与动作照常可用，
// 所以无头冒烟仍能完整验证 PreBreakState / PreBreakAction 这条链路。
func (a *App) ShowPreBreakWindow(info PreBreakInfo) {
	a.closePreBreakWindow()
	if serverMode {
		log.Printf("[prebreak] server 模式无原生窗口，跳过创建（内容已就绪 custom=%v title=%q）",
			info.Custom, info.Title)
		return
	}
	x, y, w, h := preBreakBounds(application.Rect{}, info.Custom)
	if sc := a.app.Screen.GetPrimary(); sc != nil {
		x, y, w, h = preBreakBounds(sc.WorkArea, info.Custom)
	}
	win := a.app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             preBreakWindowName,
		Title:            "即将休息",
		X:                x,
		Y:                y,
		Width:            w,
		Height:           h,
		Frameless:        true,
		AlwaysOnTop:      true,
		DisableResize:    true,
		URL:              preBreakURL(info),
		BackgroundType:   application.BackgroundTypeTranslucent,
		BackgroundColour: application.NewRGBA(0, 0, 0, 0),
		// 窗口身份经 preBreakURL 的 ?win=prebreak 下发；JS 字段对 URL 页面不生效（仅占位）
		JS: `window.__WR_WIN = "prebreak";`,
		Windows: application.WindowsWindow{
			// 无边框窗口在 Windows 上会留一圈原生装饰（阴影 / 描边），与 Electron 版一致地关掉
			DisableFramelessWindowDecorations: true,
			// 与 Electron 的 skipTaskbar 同义：小窗不进任务栏
			HiddenOnTaskbar: true,
		},
	})
	// 窗口被系统 / 用户直接关掉（Alt+F4）时清掉引用，免得后续 closePreBreakWindow 拿着
	// 一个已销毁的窗口去 Close —— 跨 tick 访问销毁过的窗口会抛错。
	win.OnWindowEvent(events.Common.WindowClosing, func(*application.WindowEvent) {
		a.mu.Lock()
		delete(a.wins, preBreakWindowName)
		a.mu.Unlock()
	})
	a.mu.Lock()
	a.wins[preBreakWindowName] = win
	a.mu.Unlock()
	log.Printf("[prebreak] 提前提示窗已弹出 custom=%v %dx%d @(%d,%d) title=%q",
		info.Custom, w, h, x, y, info.Title)
}

// closePreBreakWindow 关掉提前提示窗（没开着时是空操作）。
// 一律先摘引用再关：关窗会触发 WindowClosing，回调里再查一次 map 也无妨。
func (a *App) closePreBreakWindow() {
	a.mu.Lock()
	w := a.wins[preBreakWindowName]
	delete(a.wins, preBreakWindowName)
	a.mu.Unlock()
	if w != nil {
		w.Close()
	}
}

// ---------- 绑定给渲染层的接口 ----------

// PreBreakState 提前提示窗的状态（渲染层每 500ms 轮询一次刷新倒计时）。
// 字段集与 electron/main.ts 的 'prebreak:state' 处理器逐字对齐：
// 渲染层直接读 st.paused / st.title / st.custom / st.countdownTail / st.remainMs，
// 少一个字段不会报错，只会静默显示错内容 —— 所以这里不裁字段。
func (a *App) PreBreakState() map[string]any {
	phase, paused, remain := "idle", false, int64(0)
	if a.sched != nil {
		st := a.sched.stateCopy()
		phase, paused = st.Phase, st.Paused
		remain = a.sched.remainingMs()
	}
	info := a.currentPreBreakInfo()
	return map[string]any{
		"phase":         phase,
		"paused":        paused,
		"remainMs":      remain,
		"title":         info.Title,
		"custom":        info.Custom,
		"countdownTail": info.CountdownTail,
	}
}

// PreBreakAction 提前提示窗上的四个动作，与 Electron 的 'prebreak:action' 同义：
// breakNow / postpone / skip 汇聚到调度器（与托盘、快捷键同一条入口），
// pause 与托盘「暂停休息」完全相同（暂停调度并立即关窗）。
//
// 未知动作返回错误而不是静默无事 —— 静默降级是这类「按名调用」链路最容易漏的坑。
func (a *App) PreBreakAction(action string) error {
	if err := a.preBreakAct(action); err != nil {
		return err
	}
	// 立即休息会触发 break-start（那里也会关）；推迟 / 跳过 / 暂停都在此直接关
	a.closePreBreakWindow()
	return nil
}

// preBreakAct 动作分发（不碰窗口，单测可直接驱动）
func (a *App) preBreakAct(action string) error {
	switch action {
	case "breakNow":
		a.schedBreakNow()
	case "postpone":
		a.schedPostpone()
	case "skip":
		a.schedSkip()
	case "pause":
		a.schedPauseToggle()
	default:
		return fmt.Errorf("未知的提前提示动作 %q（可用：breakNow / postpone / skip / pause）", action)
	}
	return nil
}
