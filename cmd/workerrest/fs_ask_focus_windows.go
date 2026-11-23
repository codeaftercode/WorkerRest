//go:build windows

package main

import (
	"log"
	"sync/atomic"
	"syscall"
)

var (
	procSetWindowLongPtrW = fgUser32.NewProc("SetWindowLongPtrW")
	procCallWindowProcW   = fgUser32.NewProc("CallWindowProcW")
	procDefWindowProcW    = fgUser32.NewProc("DefWindowProcW")
)

const (
	// GWLP_WNDPROC = -4（winuser.h），按补码写成 uintptr
	gwlpWndProc = ^uintptr(3)
	wmSetFocus  = 0x0007
)

var (
	fsAskOrigWndProc atomic.Uintptr
	fsAskWndProcCb   = syscall.NewCallback(fsAskWndProcHook)
)

// fsAskWndProcHook 询问窗的窗口过程过滤层：**只拦 WM_SETFOCUS**，其余全部转发给 Wails。
//
// 为什么拦：Wails 的窗口过程在 `case WM_SETFOCUS` 里调 `focus()`，而 focus() 的第一行
// 就是 `SetForegroundWindow(w.hwnd)`（webview_window_windows.go）。WebView2 初始化完成时
// 会给自己渲染的子窗 SetFocus，父窗因此收到 WM_SETFOCUS —— 询问窗就这样把前台抢走了。
//
// WS_EX_NOACTIVATE 挡不住它：那一位只保证“用户点击 / ShowWindow 不激活”，挡不住显式的
// SetForegroundWindow。实测不拦时 8 拍里有 1~8 拍前台是询问窗；拦掉后为 0。
//
// 抢前台的代价不是“难看”：无边框游戏一失焦就不再是全屏，QUNS 从 2 掉回 5，这一段全屏
// 在判据里当场结束 —— 询问窗自己把前提毁掉。
//
// **为什么是转发给 DefWindowProc 而不是直接 return 0**：直接吞掉会让这扇窗永远得不到
// 焦点，而 Wails 那条 focus() 里还负责 `chromium.Focus()` —— 少了它，WebView2 对鼠标
// 点击的处理会变得时灵时不灵（实测点击成功率从 100% 掉到约 50%，用户就是“点了没反应”）。
// 让系统照常走默认的 WM_SETFOCUS 处理、只跳过 Wails 那一段「顺带把顶层窗顶成前台」，
// 两个目标才能同时成立。
func fsAskWndProcHook(hwnd, msg, wparam, lparam uintptr) uintptr {
	if msg == wmSetFocus {
		r, _, _ := procDefWindowProcW.Call(hwnd, msg, wparam, lparam)
		return r
	}
	orig := fsAskOrigWndProc.Load()
	if orig == 0 {
		return 0
	}
	r, _, _ := procCallWindowProcW.Call(orig, hwnd, msg, wparam, lparam)
	return r
}

// installFSAskFocusFilter 给已建好的询问窗装上过滤层。
func installFSAskFocusFilter(hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	old, _, err := procSetWindowLongPtrW.Call(hwnd, gwlpWndProc, fsAskWndProcCb)
	if old == 0 && err != nil {
		// 装不上就退回前台守卫那条路（它也能兜住，只是慢 120ms 一轮）
		log.Printf("[fsAsk] 焦点过滤层装不上（SetWindowLongPtr: %v），只靠前台守卫", err)
		return
	}
	fsAskOrigWndProc.Store(old)
	log.Printf("[fsAsk] 焦点过滤层已装（原 wndproc=0x%X）：WM_SETFOCUS 不再 SetForegroundWindow", old)
}

// uninstallFSAskFocusFilter 还原原窗口过程（窗销毁前调用；失败也无害）。
func uninstallFSAskFocusFilter(hwnd uintptr) {
	orig := fsAskOrigWndProc.Swap(0)
	if hwnd == 0 || orig == 0 {
		return
	}
	procSetWindowLongPtrW.Call(hwnd, gwlpWndProc, orig)
}
