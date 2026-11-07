//go:build !windows

package platform

import "syscall"

// 非 Windows 平台（Linux / macOS / 银河麒麟 ARM64）的 WebView 预检。
//
// 这些平台用系统自带的 WebKitGTK（Linux）或 WKWebView（macOS），由 Wails 运行时
// 直接使用，不需要像 Windows 的 WebView2 那样单独预检一个“运行时是否已安装”。
// 所以恒返回 Found=true（已就绪），让 main 包的启动预检直接放行。

// WebView2DownloadURL 非 Windows 平台无 WebView2，返回空串（不会用到）。
func WebView2DownloadURL() string { return "" }

// ProbeWebView2 非 Windows 平台恒返回“已就绪”。
func ProbeWebView2() WebView2Probe {
	return WebView2Probe{Found: true, Certain: true}
}

// HiddenSysProcAttr 非 Windows 平台无“隐藏子进程窗口”概念，返回 nil（走默认行为）。
func HiddenSysProcAttr() *syscall.SysProcAttr { return nil }

// LastInputTick 非 Windows 平台的桩：本项目尚未做 Linux/macOS 真机适配，返回
// (0, false) 表示“读不到”。调用方据此跳过“空闲自动暂停”这条链路（与 Windows 上
// GetLastInputInfo 不可用时的降级口径一致）。未来真机移植时，Linux 用 XScreenSaver
// 扩展 / xprintidle，macOS 用 IOKit HID 事件，补到对应平台文件即可。
func LastInputTick() (int64, bool) { return 0, false }
