//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

var (
	fgUser32                = syscall.NewLazyDLL("user32.dll")
	procGetForegroundWindow = fgUser32.NewProc("GetForegroundWindow")
	procSetForegroundWindow = fgUser32.NewProc("SetForegroundWindow")
	procSetWindowPos        = fgUser32.NewProc("SetWindowPos")
	procGetWindowRect       = fgUser32.NewProc("GetWindowRect")
	procWindowFromPoint     = fgUser32.NewProc("WindowFromPoint")
	procGetAncestor         = fgUser32.NewProc("GetAncestor")
)

// SetWindowPos 的位（winuser.h）
const (
	swpNoSize     = 0x0001
	swpNoMove     = 0x0002
	swpNoActivate = 0x0010
	// hwndTopmost HWND_TOPMOST = (HWND)-1
	hwndTopmost = ^uintptr(0)
	// gaRoot GA_ROOT：取顶层祖先（WebView2 渲染窗是询问窗的子窗）
	gaRoot = 2
)

// foregroundWindow 当前前台窗口句柄（0 = 读不到）。
func foregroundWindow() uintptr {
	h, _, _ := procGetForegroundWindow.Call()
	return h
}

// restoreForeground 把前台还给 win。
//
// 这里不需要 AttachThreadInput 那套绕前台锁的技巧：调用发生在「我们的询问窗正占着前台」
// 这一刻，而 SetForegroundWindow 对**当前前台进程自己**是放行的（探针里从外部进程调用
// 才需要 attach）。
func restoreForeground(win uintptr) bool {
	if win == 0 {
		return false
	}
	ok, _, _ := procSetForegroundWindow.Call(win)
	return ok != 0
}

// raiseNoActivate 把 win 重新提到 TOPMOST 组的最上层，且不激活它。
//
// 还原前台之后必须补这一下：全屏应用（尤其是 TOPMOST 的无边框窗口）拿回前台时会被顶到
// Z 序最上，把询问窗整个盖住 —— 用户看不见也点不到。SWP_NOACTIVATE 保证只是挪 Z 序，
// 不把焦点从全屏应用手里抢回来。
func raiseNoActivate(win uintptr) bool {
	if win == 0 {
		return false
	}
	ok, _, _ := procSetWindowPos.Call(win, hwndTopmost, 0, 0, 0, 0,
		uintptr(swpNoSize|swpNoMove|swpNoActivate))
	return ok != 0
}

// 这两个调用点做成变量，只为了能测 fsAskOccluded 的**逻辑**（取中心点、取顶层祖先、
// 与自己比对）。真实 Win32 行为没法在单测里摆布：开发机上永远有别人的 TOPMOST 窗口
// 压在上面，造两扇测试窗根本抢不到最上层 —— 试过，稳定地红。真机部分由
// probe_fs_ask.py 的「按钮坐标上确实是询问窗」兜（它问系统同一个问题）。
var (
	// occlPointHit 屏幕上这一点归谁（0 = 没有任何窗口）。POINT 是 8 字节结构，Win64
	// 调用约定下按值放进寄存器：两个 int32 拼成一个 uintptr。传指针是 GetCursorPos
	// 那种 POINT* 接收方的写法，在这里会读到别的内存而且**不会报错**。
	occlPointHit = func(x, y int) uintptr {
		packed := uintptr(uint64(uint32(x)) | uint64(uint32(y))<<32)
		h, _, _ := procWindowFromPoint.Call(packed)
		return h
	}
	// occlRootOf 顶层祖先（WebView2 渲染窗是询问窗的子窗，只比自身句柄会永远不等）
	occlRootOf = func(h uintptr) uintptr {
		root, _, _ := procGetAncestor.Call(h, gaRoot)
		if root == 0 {
			return h
		}
		return root
	}
	// occlRectOf 窗口矩形（失败 = 句柄无效，当成“没被盖住”）
	occlRectOf = func(h uintptr) (wrRect, bool) {
		var r wrRect
		ok, _, _ := procGetWindowRect.Call(h, uintptr(unsafe.Pointer(&r)))
		return r, ok != 0
	}
)

// fsAskOccluded 询问窗此刻是否被别的顶层窗盖住（用户看不见 / 点不到）。
//
// 判据与探针完全一致：取窗自己矩形中心那一点，问系统“这一点上是谁”，再取它的顶层祖先
// —— 若顶层祖先不是询问窗，就是被盖住了。用中心点而不是四角，是因为询问窗是圆角卡片，
// 四角落在透明区域里，WindowFromPoint 会穿透到下面的全屏内容。
//
// 为什么需要这个判据（2026-11-23）：全屏应用自己往往也是 WS_EX_TOPMOST（无边框游戏、
// 播放器全屏、探针的靶子窗），而**同为 TOPMOST 时后抬者在上**。之前是“每 4 拍无条件抬
// 一次”，两个方向都不对：正常情况下每秒白发 2 次 SetWindowPos（每次都广播
// WM_WINDOWPOSCHANGED），真被压住时又要等最多 480ms 才恢复。改成先采样、只有被盖住才抬：
// 正常时零噪音，被盖住时下一个 120ms tick 就恢复。
func fsAskOccluded(self uintptr) bool {
	if self == 0 {
		return false
	}
	r, ok := occlRectOf(self)
	if !ok {
		return false
	}
	if r.Right <= r.Left || r.Bottom <= r.Top {
		return false
	}
	h := occlPointHit(int((r.Left+r.Right)/2), int((r.Top+r.Bottom)/2))
	if h == 0 {
		// 这点上没有窗口（截屏保护 / 桌面临时不可见）：保守当成没被盖住，
		// 免得每拍都无谓地抬一次。
		return false
	}
	return occlRootOf(h) != self
}
