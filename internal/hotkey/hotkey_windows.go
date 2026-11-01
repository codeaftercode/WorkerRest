//go:build windows

package hotkey

import (
	"syscall"
	"unsafe"
)

// Win32 全局快捷键原语（user32 / kernel32）。main 包的 HotkeyManager 消息循环
// 通过这些函数调用，不直接碰 syscall / unsafe。

var (
	modKernel32            = syscall.NewLazyDLL("kernel32.dll")
	modUser32              = syscall.NewLazyDLL("user32.dll")
	procRegisterHotKey     = modUser32.NewProc("RegisterHotKey")
	procUnregisterHotKey   = modUser32.NewProc("UnregisterHotKey")
	procGetMessageW        = modUser32.NewProc("GetMessageW")
	procPeekMessageW       = modUser32.NewProc("PeekMessageW")
	procPostThreadMessageW = modUser32.NewProc("PostThreadMessageW")
	procGetCurrentThreadId = modKernel32.NewProc("GetCurrentThreadId")
)

// WinMsg 对应 Win32 MSG 结构（x64 布局与系统一致）。
type WinMsg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      struct{ X, Y int32 }
}

// RegisterHotKey 注册线程级热键（hwnd=NULL 时 WM_HOTKEY 投递到注册线程消息队列）。
// 返回 0 表示失败（组合键被占用等）。
func RegisterHotKey(id int, mods, vk uintptr) bool {
	r, _, _ := procRegisterHotKey.Call(0, uintptr(id), mods, vk)
	return r != 0
}

// UnregisterHotKey 注销热键（未注册的 id 返回 0，无害）。
func UnregisterHotKey(id int) {
	procUnregisterHotKey.Call(0, uintptr(id))
}

// PeekMessage 空 Peek 一次（PM_NOREMOVE）：确保线程真的有消息队列，
// 否则并发的 PostThreadMessage 可能因队列未建而丢消息。
func PeekMessage(m *WinMsg) {
	procPeekMessageW.Call(uintptr(unsafe.Pointer(m)), 0, 0, 0, 0)
}

// GetMessage 阻塞取一条消息。返回 false 表示 WM_QUIT / 错误，应退出循环。
func GetMessage(m *WinMsg) bool {
	r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(m)), 0, 0, 0)
	return r != 0 && r != ^uintptr(0)
}

// PostThreadMessage 向消息循环线程发一条消息（用于配置变更后触发重注册）。
func PostThreadMessage(threadID uint32, msg uint32) {
	procPostThreadMessageW.Call(uintptr(threadID), uintptr(msg), 0, 0)
}

// CurrentThreadID 返回当前线程 id。
func CurrentThreadID() uint32 {
	tid, _, _ := procGetCurrentThreadId.Call()
	return uint32(tid)
}
