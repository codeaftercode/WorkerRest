// Package autostart 提供“开机自启动”的平台抽象。
//
// Windows 上靠 HKCU 注册表的 Run 键 + Explorer\StartupApproved 禁用标记实现
// （见 autostart_windows.go）；其它平台当前是 no-op 桩（见 autostart_other.go），
// 未来真机移植时用 ~/.config/autostart/*.desktop（Linux）、launchd（macOS）补实现。
//
// 本包只放“跨平台接口 + 各平台实现”，不依赖 main 包的任何符号；
// main 包里的 ApplyAutostart（App 方法）负责拼命令行、判断目标状态并调用本包。
package autostart

import (
	"strings"
	"unicode"
)

// RegResult 注册表操作结果（字段名与历史 autostart.ts 的 RegResult 一致）。
type RegResult struct {
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// ValueName 返回启动项的值名（也是 AppUserModelId）。各平台实现文件里定义 runValueName。
func ValueName() string { return runValueName }

// NormalizeCommandLine 规范化命令行再比较：注册表里存的值可能带/不带引号、空白也可能不同，
// 直接字符串相等会把“其实已经是目标值”误判成“需要重写”。
func NormalizeCommandLine(s string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(s, `"`, "")), " ")
}

// BuildRunCommandLine 把可执行文件与参数拼成 Run 键里存的命令行。
// 路径含空格时**必须加引号**（Windows 启动项的规范写法），否则会退化成
// “按空格截断后猜完整文件名”的解析，同目录一旦出现截断同名文件就启动错东西。
func BuildRunCommandLine(exePath string, args []string) string {
	quote := func(s string) string {
		if strings.ContainsFunc(s, unicode.IsSpace) {
			return `"` + s + `"`
		}
		return s
	}
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, quote(exePath))
	for _, a := range args {
		parts = append(parts, quote(a))
	}
	return strings.Join(parts, " ")
}
