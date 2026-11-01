//go:build windows

package notifier

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// MessageBoxW 的 uType 位（与 main 包迁移前保持一致）。
const (
	mbOK            = 0x00000000
	mbIconError     = 0x00000010
	mbSetForeground = 0x00010000
	mbTopmost       = 0x00040000
)

var (
	user32  = windows.NewLazySystemDLL("user32.dll")
	msgBoxW = user32.NewProc("MessageBoxW")
)

// Error 用 MessageBoxW 弹原生错误框。
func Error(title, text string) {
	textPtr, err := windows.UTF16PtrFromString(text)
	if err != nil {
		return
	}
	titlePtr, err := windows.UTF16PtrFromString(title)
	if err != nil {
		return
	}
	msgBoxW.Call(0,
		uintptr(unsafe.Pointer(textPtr)),
		uintptr(unsafe.Pointer(titlePtr)),
		uintptr(mbOK|mbIconError|mbSetForeground|mbTopmost))
}

// OpenURL 用 ShellExecuteW("open") 交给默认浏览器。
//
// 不走 exec.Command("rundll32"/"cmd /c start")：那两条会拉起子进程，在限制子进程的
// 安全策略下直接失败，且容易闪出黑框；ShellExecute 是 shell 的既有入口，行为与用户
// 双击链接一致。
func OpenURL(url string) error {
	verb, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	target, err := windows.UTF16PtrFromString(url)
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, verb, target, nil, nil, windows.SW_SHOWNORMAL)
}
