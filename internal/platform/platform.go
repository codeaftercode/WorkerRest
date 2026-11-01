// Package platform 提供平台能力的统一抽象，是跨平台重构的核心。
//
// 原则：main 包（以及任何业务包）**不直接 import Windows-only 库**（syscall /
// unsafe / golang.org/x/sys/windows / registry），一律通过本包的平台中立函数名调用；
// 各平台实现由 build tag 选择（platform_windows.go / platform_other.go）。
//
// 本包目前承载：
//   - WebView2 运行时预检（Windows-only；其它平台无需预检，恒返回「已就绪」）
//
// 未来真机移植时，Linux（WebKitGTK）、macOS（WKWebView）、银河麒麟（ARM64）的
// 差异也统一收敛到本包，业务层零改动。
package platform

// WebView2Probe WebView 运行时预检结果（沿用 Windows 上的命名，跨平台同义）。
//
// Found=false 表示未找到运行时；Certain 才说明「这不是权限问题，是真的没装」——
// 只有这种情况才该拦人。其它平台（Linux/macOS）用系统自带 WebKit/WKWebView，
// 无需预检，恒返回 Found=true。
type WebView2Probe struct {
	Version string
	Found   bool
	Certain bool
}
