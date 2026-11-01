package main

import (
	"fmt"
	"log"
	"net/url"
	"workerrest/internal/model"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// 休息前提示弹窗（右下角那个带倒计时与动作按钮的小窗）：
// 历史源码/windows/preBreakWindow.ts 的对应物。
//
// 为什么不用系统通知：Windows 的通知带不了按钮，而这里需要「立即休息 / 推迟休息 /
// 跳过休息 / 暂停计时」四个动作，所以是自绘窗口。
//
// 与 上一版逐条对齐的三处，都是踩过才知道要这么写的：
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
// 轮询 PreBreakState 拿的是同一份，中途计划漂移不会让标题跟着变（与 上一版的
// preBreakInfo() 同口径）。名称只有这一份，渲染层不自己拼。

// PreBreakInfo 与「哪一种提醒」绑定的弹窗内容
type PreBreakInfo struct {
	// Owner 弹窗归属："" / "break" = 休息 / 自定义提醒（走调度器倒计时）；"takeout" =
	// 外卖预通知（倒计时指向「原定时刻」，动作走外卖记账，不经过调度器）。
	// 渲染层不区分 owner（takeout 复用 custom 版式），仅主进程动作分发据此分流。
	Owner string `json:"owner"`
	// Custom 自定义提醒：另一套版式（倒计时带名称、不显示两行说明、按钮改「…提醒」
	// 且没有「暂停计时」）
	Custom bool `json:"custom"`
	// Title 标题：自定义提醒是「XX时间到」，内置短 / 长是劝说文案；外卖预通知是提醒名
	Title string `json:"title"`
	// CountdownTail 倒计时数字后面那段（内置「 秒进入休息」/ 自定义 / 外卖「 秒」）
	CountdownTail string `json:"countdownTail"`
	// PostponeMin 该提醒的「可推迟」分钟数：0 = 渲染层不显示「推迟」按钮。
	// 弹窗打开时算一次（与标题同口径），中途改配置不跟随。
	PostponeMin int `json:"postponeMin"`
	// BgColor 背景颜色：取「通用配置 → 提醒窗口 → 背景颜色」（appearance.color），
	// 与休息窗口同一个键。同样是弹窗打开时算一次。空串 = 渲染层用内置默认蓝。
	// 文字色不在这里下发 —— 由渲染层按这个底色算对比度（浅底配深字 / 深底配浅字），
	// 保证用户把颜色调到多浅多深，标题、倒计时、两行说明都还看得清。
	BgColor string `json:"bgColor"`
}

// builtinPreBreakTitleShort / builtinPreBreakTitleLong 内置短 / 长的标题。它们的名字
// （短休息 / 长休息）不是用户起的，说「短休息时间到」很怪 —— 保持原来的劝说文案与
// 原来的四按钮版式（含两行说明）。标题末尾按休息身份区分「短休息 / 长休息」，以便提前
// 提示窗能分清是哪一种（长 / 短休息提前提示窗口分开提示）。
const builtinPreBreakTitleShort = "您已连续工作较长时间，建议您短休息"
const builtinPreBreakTitleLong = "您已连续工作较长时间，建议您长休息"

// builtinPreBreakTail 内置版的倒计时后缀；自定义提醒那套见 preBreakCountdownTail
const builtinPreBreakTail = " 秒进入休息"

// 标题与倒计时后缀的纯函数 preBreakTitle / preBreakCountdownTail 在计划层迁移时就搬进了
// breakplan.go（对应 历史源码/types.ts 的同名函数），这里直接复用，不再重复定义。

// preBreakInfoOfType 按休息身份算弹窗内容。kind 是内置身份（short / long）时走内置版式，
// 其余按自定义提醒的名字生成（自定义提醒的 id 前缀是 cb-，见 normalizeCustomBreaks）。
func (a *App) preBreakInfoOfType(kind string) PreBreakInfo {
	// 推迟分钟数从该提醒自己的配置取；取整到分钟只用于「>0 判定」（按钮显隐），
	// 实际推迟时长以毫秒为准
	pm := 0
	if cfg, ok := a.cfgSnapshot().BreakByID(kind); ok {
		pm = int(cfg.PostponeMs / 60_000)
	}
	// 背景色与休息窗口同源（通用配置 → 提醒窗口 → 背景颜色），弹窗打开时算一次
	bg := a.cfgSnapshot().AppearanceCfg().Color
	if kind == "short" || kind == "long" {
		title := builtinPreBreakTitleShort
		if kind == "long" {
			title = builtinPreBreakTitleLong
		}
		return PreBreakInfo{Custom: false, Title: title, CountdownTail: builtinPreBreakTail, PostponeMin: pm, BgColor: bg}
	}
	name := a.cfgSnapshot().BreakNameByID(kind)
	return PreBreakInfo{Custom: true, Title: preBreakTitle(name), CountdownTail: preBreakCountdownTail(name), PostponeMin: pm, BgColor: bg}
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
// 渲染层拿到空 tail 会把倒计时后缀清空，所以这里兜底成内置后缀；PostponeMin 留 0
// （从没弹过窗时窗口本就不在，canPostpone=false 无负面影响）。
func (a *App) currentPreBreakInfo() PreBreakInfo {
	a.mu.Lock()
	defer a.mu.Unlock()
	info := a.preInfo
	if info.CountdownTail == "" {
		info.CountdownTail = builtinPreBreakTail
	}
	if !info.Custom && info.Title == "" {
		info.Title = builtinPreBreakTitleShort
	}
	return info
}

// rememberPreBreakInfo 算一次弹窗内容并记下 —— 窗口内每 500ms 轮询读的就是这份，
// 所以中途计划漂移不会让标题 / 倒计时后缀跟着变（与 上一版的模块级 info 同口径）。
func (a *App) rememberPreBreakInfo(kind string) PreBreakInfo {
	info := a.preBreakInfoOfType(kind)
	a.mu.Lock()
	a.preInfo = info
	a.mu.Unlock()
	return info
}

// 窗口尺寸：宽固定 400（与上一版 同值）；高按版式两档，见文件头说明。
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
// postpone=0 时渲染层首帧就藏掉「推迟」按钮（与 PreBreakState.canPostpone 同源）。
// bg 是背景颜色（# 也要编码成 %23）：带上它，首帧就是用户配的颜色，
// 不会先闪一下内置默认蓝再等 500ms 轮询改过来。
func preBreakURL(info PreBreakInfo) string {
	flag := "0"
	if info.Custom {
		flag = "1"
	}
	postpone := "1"
	if info.PostponeMin <= 0 {
		postpone = "0"
	}
	u := "/prebreak.html?custom=" + flag + "&tail=" + url.QueryEscape(info.CountdownTail) +
		"&postpone=" + postpone + "&win=prebreak"
	if info.BgColor != "" {
		u += "&bg=" + url.QueryEscape(info.BgColor)
	}
	return u
}

// preBreakWindowName 窗口名：Wails 按名字交付窗口，清理时也按它查
const preBreakWindowName = "prebreak"

// ShowPreBreakWindow 弹出（或重弹）提前提示窗。
//
// 每次都先关掉旧的：暂停后恢复会再走一次 onPreNotify，不能叠出两个窗口。
// 无头（-tags server）构建下没有原生窗口，只记内容不建窗 —— 状态与动作照常可用，
// 所以无头冒烟仍能完整验证 PreBreakState / PreBreakAction 这条链路。
func (a *App) ShowPreBreakWindow(info PreBreakInfo) {
	// 两路来源可能同时到：调度器的 onPreNotify 与外卖预通知各自在自己的 goroutine 里。
	// 不串行的话两边都「先关旧的、再建新的」，后写入 map 的那个赢，先建的成为没人持有的
	// 野窗 —— 固定名窗口尤其危险，它连按名字都查不到（GetByName 拿到的是另一扇）。
	a.preBreakMu.Lock()
	defer a.preBreakMu.Unlock()
	a.closePreBreakWindowLocked()
	if serverMode {
		log.Printf("[prebreak] server 模式无原生窗口，跳过创建（内容已就绪 custom=%v title=%q）",
			info.Custom, info.Title)
		return
	}
	x, y, w, h := preBreakBounds(application.Rect{}, info.Custom)
	if sc := a.app.Screen.GetPrimary(); sc != nil {
		x, y, w, h = preBreakBounds(sc.WorkArea, info.Custom)
	}
	// 共享屏幕时隐藏：提前提示小窗里也有提醒文案，和休息窗口同一个开关
	hideGuard := a.cfgSnapshot().AppearanceCfg().HideOnShare
	win := a.app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:          preBreakWindowName,
		Title:         "即将休息",
		X:             x,
		Y:             y,
		Width:         w,
		Height:        h,
		Frameless:     true,
		AlwaysOnTop:   true,
		DisableResize: true,
		// 不设它 X/Y 会被无视：Wails 的 InitialPosition 零值 = 整窗居中，
		// 这就是此前提前提示窗跑到屏幕正中的根因
		InitialPosition: application.WindowXY,
		// 共享屏幕时隐藏（同「提醒窗口 → 共享屏幕时隐藏提醒窗口」）：
		// 提前提示窗里有休息名称与倒计时，被同事看见一样露馅；
		// 生效前提（--disable-direct-composition）见 main.go 的注释
		ContentProtectionEnabled: hideGuard,
		URL:                      preBreakURL(info),
		BackgroundType:           application.BackgroundTypeTranslucent,
		BackgroundColour:         application.NewRGBA(0, 0, 0, 0),
		// 窗口身份经 preBreakURL 的 ?win=prebreak 下发；JS 字段对 URL 页面不生效（仅占位）
		JS: `window.__WR_WIN = "prebreak";`,
		Windows: application.WindowsWindow{
			// 无边框窗口在 Windows 上会留一圈原生装饰（阴影 / 描边），与 上一版一致地关掉
			DisableFramelessWindowDecorations: true,
			// 与 上一版的 skipTaskbar 同义：小窗不进任务栏
			HiddenOnTaskbar: true,
		},
	})
	// 窗口被系统 / 用户直接关掉（Alt+F4）时清掉引用，免得后续 closePreBreakWindow 拿着
	// 一个已销毁的窗口去 Close —— 跨 tick 访问销毁过的窗口会抛错。
	// 只删「自己这一扇」：closePreBreakWindow 摘掉旧引用后紧接着就会建新窗，而 Wails 的
	// 关闭事件是异步分发的（每个监听器各起一个 goroutine），迟到一步的旧窗回调按名字
	// delete 会把**新窗**的引用一起删掉 —— 新窗从此无人持有，程序再也关不掉它。
	win.OnWindowEvent(events.Common.WindowClosing, func(*application.WindowEvent) {
		a.mu.Lock()
		dropWindowRef(a.wins, preBreakWindowName, win)
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
//
// 走 preBreakMu：与 ShowPreBreakWindow 互斥。否则「窗口自己轮询触发的自愈关窗」可能
// 插进「关旧窗 → 建新窗」中间，把刚建好的新窗当成旧窗关掉。
func (a *App) closePreBreakWindow() {
	a.preBreakMu.Lock()
	defer a.preBreakMu.Unlock()
	a.closePreBreakWindowLocked()
}

// closePreBreakWindowLocked 已持有 preBreakMu 时用（ShowPreBreakWindow 内部）。
func (a *App) closePreBreakWindowLocked() {
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
// 字段集与 历史源码/main.ts 的 'prebreak:state' 处理器逐字对齐：
// 渲染层直接读 st.paused / st.title / st.custom / st.countdownTail / st.remainMs，
// 少一个字段不会报错，只会静默显示错内容 —— 所以这里不裁字段。
func (a *App) PreBreakState() map[string]any {
	info := a.currentPreBreakInfo()
	// 背景色每次都按当前配置现取：用户在设置里改了颜色、窗口还挂着时也能立刻跟上。
	// 优先用弹窗内容里记住的那份（与标题同口径），空则现取（旧窗口 / 单测构造的 info）。
	bgColor := info.BgColor
	if bgColor == "" {
		bgColor = a.cfgSnapshot().AppearanceCfg().Color
	}
	// 外卖预通知：倒计时指向「原定时刻」（不读调度器剩余），且永不暂停计时
	if info.Owner == "takeout" {
		a.mu.Lock()
		target := a.preTargetAt
		pp := a.takeoutPostponed
		a.mu.Unlock()
		var ppDue int64
		if pp != nil {
			ppDue = pp.DueAt
		}
		// 自愈：外卖预通知窗没有调度器那条「该不该在场」的判据，由窗口自己的轮询兜住
		// 三种失效：外卖总开关被关、推迟计划被改到别的时刻（这个 target 已作废）、
		// 过了到点 + 宽限仍未收（真窗本该弹出并关掉它）。不兜的话窗口会一直挂着，
		// 倒计时停在 0 秒，用户以为提醒坏了。
		cfg := a.cfgSnapshot()
		stale := !cfg.TakeoutEnabled() ||
			(pp != nil && ppDue > 0 && ppDue != target) ||
			(nowMs() > target+takeoutPreNotifyGraceMs)
		if stale {
			a.closePreBreakWindow()
		}
		remain := target - nowMs()
		if remain < 0 {
			remain = 0
		}
		return map[string]any{
			"phase":         "idle",
			"paused":        false,
			"remainMs":      remain,
			"title":         info.Title,
			"custom":        info.Custom,
			"countdownTail": info.CountdownTail,
			"canPostpone":   info.PostponeMin > 0,
			"bgColor":       bgColor,
		}
	}
	phase, paused, remain := "idle", false, int64(0)
	if a.sched != nil {
		st := a.sched.stateCopy()
		phase, paused = st.Phase, st.Paused
		remain = a.sched.remainingMs()
		// 自愈：窗口每 500ms 就自己来问一次状态 —— 顺手校验「我还该不该在场」。
		// 这层兜底与调度器各状态变更处的 syncPreBreakWindow 互补：任何漏收窗的路径
		//（含未来新加的）都会在 500ms 内被窗口自己发现并退场，绝不会带着一整轮
		// 满格倒计时留在屏幕上。窗口被关后本条轮询的返回无人接收，无需善后。
		//
		// 暂停期间不判：暂停态下 phase 不是 pre，但「暂停休息期间自定义提醒照常提醒」
		//（scheduler.pausedFire）正是要在暂停中弹这个窗，靠判据会把它自己误收掉。
		if !paused && !a.sched.preBreakWanted() {
			a.closePreBreakWindow()
		}
	}
	return map[string]any{
		"phase":         phase,
		"paused":        paused,
		"remainMs":      remain,
		"title":         info.Title,
		"custom":        info.Custom,
		"countdownTail": info.CountdownTail,
		// 推迟分钟数为 0 时渲染层藏掉「推迟」按钮（URL query 的 postpone 与之同源）
		"canPostpone": info.PostponeMin > 0,
		// 背景颜色（通用配置 → 提醒窗口 → 背景颜色）：渲染层据此算底色 + 自适应文字色
		"bgColor": bgColor,
	}
}

// PreBreakAction 提前提示窗上的四个动作，与 上一版的 'prebreak:action' 同义：
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
	a.mu.Lock()
	owner := a.preInfo.Owner
	slot := a.preTakeoutSlot
	at := a.preTargetAt
	a.mu.Unlock()
	// 外卖预通知：动作走外卖记账，不经过调度器（立即提醒=提前弹真窗并消费该次 occurrence；
	// 推迟=改走推迟线；跳过=消费该次 occurrence，原定时刻不再弹）
	if owner == "takeout" {
		switch action {
		case "breakNow":
			a.mu.Lock()
			a.takeoutPostponed = nil
			a.takeoutFired[occurrenceKey("takeout", at)] = true
			a.mu.Unlock()
			a.closePreBreakWindow()
			// occurrence 用预通知的目标时刻：提前通知跨午夜时是次日那一格
			a.fireTakeoutAt(slot, 0, at)
			a.refreshStateAfterTakeout()
			return nil
		case "postpone":
			// 先取配置（cfgSnapshot 要加 a.mu，不能在持锁时调用）；再一次性持锁写簿记
			mins := a.cfgSnapshot().TakeoutPostponeMin()
			a.mu.Lock()
			prevCount := 0
			if a.takeoutPostponed != nil {
				prevCount = a.takeoutPostponed.Count
			}
			a.takeoutFired[occurrenceKey("takeout", at)] = true
			a.takeoutPostponed = &model.TakeoutPostponed{Slot: slot, DueAt: nowMs() + int64(mins)*60000, Count: prevCount + 1}
			a.mu.Unlock()
			a.closePreBreakWindow()
			a.refreshStateAfterTakeout()
			return nil
		case "skip":
			a.mu.Lock()
			a.takeoutPostponed = nil
			a.takeoutFired[occurrenceKey("takeout", at)] = true
			a.mu.Unlock()
			a.closePreBreakWindow()
			a.refreshStateAfterTakeout()
			return nil
		default:
			return fmt.Errorf("未知的外卖预通知动作 %q（可用：breakNow / postpone / skip）", action)
		}
	}
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

// refreshStateAfterTakeout 外卖预通知动作（立即 / 推迟 / 跳过）改了 takeoutFired /
// takeoutPostponed 后，主动把状态推给设置页并刷新托盘：这些簿记不经过调度器，调度器的
// emitState 不会触发，不主动推的话设置页「今日提醒时间线」与「下次外卖提醒」在动作后
// 仍是旧值（与休息 / 自定义提醒走 schedPostpone→emitState 不同，外卖要自己推）。
//
// a.app == nil（无头单测）时跳过——pushState 要经 Wails 事件总线，没有 app 会崩。
func (a *App) refreshStateAfterTakeout() {
	if a.app == nil {
		return
	}
	a.pushState()
	a.refreshTray()
}
