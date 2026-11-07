//go:build !windows

package hotkey

// 非 Windows 平台的全局快捷键桩：本项目尚未做 Linux/macOS 真机适配。
// 所有函数都是 no-op（返回“未注册”），让 main 包的 HotkeyManager 在非 Windows
// 上自然走“不启动”路径。未来真机移植时补 XGrabKey（Linux）/ RegisterEventHotKey
// （macOS）实现。

// WinMsg 非 Windows 平台的占位消息结构（不会用到）。
type WinMsg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      struct{ X, Y int32 }
}

// RegisterHotKey 非 Windows 平台恒返回 false（注册失败 → 不启动热键）。
func RegisterHotKey(id int, mods, vk uintptr) bool { return false }

// UnregisterHotKey 非 Windows 平台 no-op。
func UnregisterHotKey(id int) {}

// PeekMessage 非 Windows 平台 no-op。
func PeekMessage(m *WinMsg) {}

// GetMessage 非 Windows 平台恒返回 false（退出循环）。
func GetMessage(m *WinMsg) bool { return false }

// PostThreadMessage 非 Windows 平台 no-op。
func PostThreadMessage(threadID uint32, msg uint32) {}

// CurrentThreadID 非 Windows 平台返回 0。
func CurrentThreadID() uint32 { return 0 }
