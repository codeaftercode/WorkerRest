//go:build !windows

package display

// List 非 Windows 平台没有可靠的多屏枚举能力（Linux 的 X11/Wayland 各走各的协议，
// Wails v3 也没有统一抽象）。返回 ErrUnsupported，调用方退回“只显示一块”。
func List() ([]Info, error) { return nil, ErrUnsupported }
