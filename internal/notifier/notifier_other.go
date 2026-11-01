//go:build !windows

package notifier

import "log"

// Error 非 Windows 平台的 no-op 桩：本项目尚未做 Linux/macOS 真机适配。
// 现阶段只记日志，未来真机移植时用平台原生弹窗（如 Linux 的 zenity/notify-send、
// macOS 的 NSAlert）替换。
func Error(title, text string) {
	log.Printf("[notifier] 原生错误框（未实现）: %s — %s", title, text)
}

// OpenURL 非 Windows 平台的 no-op 桩。
func OpenURL(url string) error {
	log.Printf("[notifier] 打开外链（未实现）: %s", url)
	return nil
}
