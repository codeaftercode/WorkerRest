package main

import (
	"log"
	"net/url"
	"strconv"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// 非独占全屏的询问小窗（2026-11-20，实验性）。
//
// 它是本项目里第一扇“会问问题”的窗口，因此有三条额外的约束：
//
// 一、**必须置顶但不抢焦点**。用户此刻正在看全屏内容（浏览器 F11、视频、PPT 放映）；
// 抢焦点会把无边框游戏踢出全屏，而 QUNS 正是靠“前台窗口铺满整屏”判定的 —— 一抢焦点，
// 这一段全屏就结束了，询问窗自己也失去了意义。见下面的 WS_EX_NOACTIVATE。
//
// 二、**尺寸克制**。420×200、右下角，只是“某个应用全屏了，要不要暂停”一句话加四个按钮。
// 它不能像休息窗那样占满屏 —— 它出现在用户正在看的东西上面。
//
// 三、**投屏 / 勿扰 / 独占全屏时根本不建它**（判据在 fs_ask.go 的状态机里，返回 PauseNow
// 而不是 Ask）。这条与 pause_notice.go 的铁律同源：屏幕上不能多出一扇窗。

const (
	// fsAskWindowName 窗口名：Wails 按名字交付窗口，清理也按它查。
	// 与 URL 的 ?win= 同一个值（其余窗口都是这个约定）。
	fsAskWindowName = "fsask"
	// fsAskWidth / fsAskHeight 询问窗尺寸。四个按钮 + 一行标题 + 一行倒计时，
	// 420×200 在 125% 缩放下也放得下；再大就会盖住太多画面。
	fsAskWidth  = 420
	fsAskHeight = 200
	fsAskMargin = 24
)

// 扩展窗口样式位（winuser.h）。ExStyle 是**整份替换**（Wails 只在 options.Windows.ExStyle
// 非零时覆盖默认值），所以必须把 Wails 默认要加的那几位一起写上，漏一位就少一个行为。
//
// Wails 构造默认值的顺序（webview_window_windows.go 的 run()）：
//
//	exStyle := WS_EX_CONTROLPARENT
//	if BackgroundType != BackgroundTypeSolid {
//	    if IgnoreMouseEvents { TRANSPARENT|LAYERED } else { NOREDIRECTIONBITMAP }
//	}
//	if AlwaysOnTop     { TOPMOST }
//	if HiddenOnTaskbar { TOOLWINDOW } else { APPWINDOW }
//	if options.Windows.ExStyle != 0 { exStyle = options.Windows.ExStyle }   // 整份替换
//
// 也就是说这四个常量必须与上面那份默认位**逐位对齐**，少一位就等于把那个行为关掉。
// 2026-11-23 审这一层时发现漏了 NOREDIRECTIONBITMAP —— 它是 Translucent 背景做
// DirectComposition 合成的必备位，漏了会被 DWM 分配重定向位图，圆角卡片外面一圈
// 不是透出全屏画面而是一块实底。缺这一位的后果只在真机、只在圆角外那圈像素上看得见，
// 所以判据放在 fsAskWindowOptions 上（TestFSAskExStyleCoversWailsDefaults）。
const (
	wsExTopmost          = 0x00000008 // WS_EX_TOPMOST：压在其它窗口之上
	wsExToolWindow       = 0x00000080 // WS_EX_TOOLWINDOW：不占任务栏 / Alt-Tab
	wsExNoRedirectionBmp = 0x00200000 // WS_EX_NOREDIRECTIONBITMAP：透明合成（Translucent 必需）
	wsExControlParent    = 0x00010000 // WS_EX_CONTROLPARENT：让 Tab 键在窗内控件间走
	wsExNoActivate       = 0x08000000 // WS_EX_NOACTIVATE：点击不抢前台焦点（见文件头）
)

// fsAskBounds 右下角定位（与暂停提示小窗同一套算法，只是尺寸不同）。
// area 为显示器工作区（DIP）。
func fsAskBounds(area application.Rect) (x, y, w, h int) {
	w, h = fsAskWidth, fsAskHeight
	x = area.X + area.Width - w - fsAskMargin
	y = area.Y + area.Height - h - fsAskMargin
	if x < area.X {
		x = area.X
	}
	if y < area.Y {
		y = area.Y
	}
	return
}

// fsAskURL 页面地址。所有文案都由 Go 侧下发（应用名只有这一份），中文必须逐段编码。
func fsAskURL(exe, label string, canMark bool, secs int) string {
	mark := "0"
	if canMark {
		mark = "1"
	}
	return "/fsask.html?win=" + fsAskWindowName +
		"&exe=" + url.QueryEscape(exe) +
		"&name=" + url.QueryEscape(label) +
		"&canMark=" + mark +
		"&secs=" + strconv.Itoa(secs)
}

// fsAskWindowOptions 询问窗的完整声明。
//
// 抽成纯函数不是为了覆盖率，而是因为这里的每一项都在真机上极难复现：要在无边框游戏 /
// 视频全屏的那一刻正好触发询问，才看得到“抢了焦点”“跑到任务栏”“背后一块黑底”。
// 而且 Wails 升级会悄悄改默认值 —— 声明放在这里，TestFSAskWindowOptions 就能逐项钉住。
//
// hideOnShare 来自「共享屏幕时隐藏」配置：询问窗里带着应用名，共享出去同样突兀。
func fsAskWindowOptions(x, y, w, h int, exe, label string, canMark, hideOnShare bool) application.WebviewWindowOptions {
	return application.WebviewWindowOptions{
		Name:          fsAskWindowName,
		Title:         "全屏询问",
		X:             x,
		Y:             y,
		Width:         w,
		Height:        h,
		Frameless:     true,
		AlwaysOnTop:   true,
		DisableResize: true,
		// 不设它 X/Y 会被无视：Wails 的 InitialPosition 零值 = 整窗居中
		InitialPosition:          application.WindowXY,
		ContentProtectionEnabled: hideOnShare,
		URL:                      fsAskURL(exe, label, canMark, fsAskTimeoutMs/1000),
		BackgroundType:           application.BackgroundTypeTranslucent,
		BackgroundColour:         application.NewRGBA(0, 0, 0, 0),
		JS:                       `window.__WR_WIN = "fsask";`,
		Windows: application.WindowsWindow{
			DisableFramelessWindowDecorations: true,
			HiddenOnTaskbar:                   true,
			// 整份替换：必须含 Wails 默认要加的全部位（见四个常量上方的对照表）
			ExStyle: wsExTopmost | wsExToolWindow | wsExControlParent | wsExNoActivate | wsExNoRedirectionBmp,
		},
	}
}

// showFSAskWindow 建（或重建）询问窗。每段全屏只会走到这里一次。
//
// keepFg 是“这一段全屏应用”的窗口句柄（判定层给的 geo.HWND）：前台万一被询问窗自己
// 顶走，守卫要还的就是它 —— 而不是建窗那一刻的 GetForegroundWindow()。后者在监视循环
// 与前台切换的交界上并不可靠（实测会拿到终端窗口，把前台还给终端，这一段全屏因此在
// 判据眼里结束）。
//
// 返回是否真的建出了窗口：没建出来（无头模式 / 单测里没有 application.App）就不该再挂
// 超时定时器 —— 否则定时器会在测试结束后对着已停止的调度器动手。
func (a *App) showFSAskWindow(exe, label string, canMark bool, keepFg uintptr) bool {
	a.fsAskWinMu.Lock()
	defer a.fsAskWinMu.Unlock()
	a.closeFSAskWindowLocked()
	if serverMode || a.app == nil {
		log.Printf("[fsAsk] 无原生窗口（server / 测试环境），跳过询问（%q）", label)
		return false
	}
	x, y, w, h := fsAskBounds(application.Rect{})
	if sc := a.preBreakScreen(); sc != nil {
		x, y, w, h = fsAskBounds(sc.WorkArea)
	}
	// 共享屏幕时隐藏：询问窗里带着“某某应用”的名字，共享出去同样突兀。
	hideGuard := a.CfgSnapshot().AppearanceCfg().HideOnShare
	win := a.app.Window.NewWithOptions(fsAskWindowOptions(x, y, w, h, exe, label, canMark, hideGuard))
	win.OnWindowEvent(events.Common.WindowClosing, func(*application.WindowEvent) {
		// 窗都没了，再去动前台只会把用户刚点开的窗口抢走
		a.stopFSAskForegroundGuard()
		uninstallFSAskFocusFilter(uintptr(win.NativeWindow()))
		a.mu.Lock()
		dropWindowRef(a.wins, fsAskWindowName, win)
		a.mu.Unlock()
	})
	a.mu.Lock()
	a.wins[fsAskWindowName] = win
	a.mu.Unlock()
	// WS_EX_NOACTIVATE 挡不住 Wails 在 WM_SETFOCUS 里显式 SetForegroundWindow，
	// 所以再上两层：① 吞掉 WM_SETFOCUS（根治，见 fs_ask_focus_windows.go）；
	// ② 前台守卫（兜底：万一还有别的路径把窗顶成前台，120ms 内还回去）。
	hwnd := uintptr(win.NativeWindow())
	installFSAskFocusFilter(hwnd)
	a.startFSAskForegroundGuard(hwnd, keepFg)
	log.Printf("[fsAsk] 非独占全屏（%s）：询问是否暂停（%d 秒内不答按暂停处理）",
		label, fsAskTimeoutMs/1000)
	return true
}

// closeFSAskWindow 关掉询问窗（幂等）
func (a *App) closeFSAskWindow() {
	a.fsAskWinMu.Lock()
	defer a.fsAskWinMu.Unlock()
	a.closeFSAskWindowLocked()
}

// closeFSAskWindowLocked 调用方持有 fsAskWinMu
func (a *App) closeFSAskWindowLocked() {
	a.stopFSAskForegroundGuard()
	a.mu.Lock()
	w := a.wins[fsAskWindowName]
	delete(a.wins, fsAskWindowName)
	a.mu.Unlock()
	if w != nil {
		uninstallFSAskFocusFilter(uintptr(w.NativeWindow()))
		w.Close()
	}
}

// fsAskDismissSoft 段外原因导致的软关闭（例如用户点了“立即休息”）。
// 抢不到建窗锁就跳过：**绝不阻塞建窗路径**，否则一次并发就把休息窗卡住。
func (a *App) fsAskDismissSoft(why string) {
	a.fsAskMu.Lock()
	wasPending := a.fsAsk.pending
	a.fsAsk.pending = false
	a.fsAskMu.Unlock()
	if !wasPending {
		return
	}
	if !a.fsAskWinMu.TryLock() {
		return
	}
	a.closeFSAskWindowLocked()
	a.fsAskWinMu.Unlock()
	log.Printf("[fsAsk] %s：关闭全屏询问（未答复，按“不暂停”处理）", why)
}

// fsAskArmTimeout 12 秒后按“暂停”结案。
//
// 用 seq 校验“还是同一段询问”：段在超时前就结束、或用户已经答过，回调必须什么都不做。
// 页面里也有倒计时，但那是视觉效果 —— 真正的结案由这里兜底（WebView2 异常时页面可能
// 根本没加载起来）。
func (a *App) fsAskArmTimeout(seq int64) {
	a.fsAskArmTimeoutAfter(seq, fsAskTimeoutMs*time.Millisecond)
}

// fsAskArmTimeoutAfter 同上，超时时长可注入（真机固定 12 秒，测试用毫秒级）。
//
// 拆出来是因为这条回调里有三条分支（已经不是同一段 / 已经答过 / 真的到点）全是“什么都不
// 发生”或“落一条暂停”，只能靠真等 —— 12 秒一次没法进常规回归，而它恰恰是“不答就静默
// 放过”与“答过了又被结案”两种事故的唯一防线。
func (a *App) fsAskArmTimeoutAfter(seq int64, d time.Duration) {
	time.AfterFunc(d, func() {
		a.fsAskMu.Lock()
		st := &a.fsAsk
		if !st.pending || st.seq != seq {
			a.fsAskMu.Unlock()
			return
		}
		st.pending = false
		needLog := !st.logged
		st.logged = true
		a.fsAskMu.Unlock()

		log.Printf("[fsAsk] 询问超时，按暂停处理")
		a.pauseFullScrBusyNow(needLog, "询问超时")
		a.closeFSAskWindow()
	})
}
