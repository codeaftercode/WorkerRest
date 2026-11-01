// Package notifier 提供系统原生消息框与「用默认浏览器打开外链」的平台抽象。
//
// 为什么单独抽一层：这两件事在 Windows 上分别靠 user32!MessageBoxW 与
// shell32!ShellExecuteW 完成（见 notifier_windows.go），都是 Windows-only 的
// 原生调用，直接写在 main 包里会让整个包无法跨平台编译。抽成接口后，
// main 包只依赖本包的平台中立函数名，各平台实现由 build tag 选择。
//
// 语义约定（跨平台一致）：
//   - Error 是**阻塞**的原生错误框（用户点掉才返回），用在「窗口/WebView 已不可信、
//     只剩系统原生 UI 可用」的场景（如缺 WebView2、主 goroutine panic 兜底）。
//   - OpenURL 用系统默认浏览器打开链接；失败返回 error，由调用方决定是否记日志。
//
// 函数实现分别落在 notifier_windows.go（//go:build windows）与
// notifier_other.go（//go:build !windows）。
package notifier
