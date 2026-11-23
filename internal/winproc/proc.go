// Package winproc 枚举当前运行的进程名（Toolhelp32），供“打开即暂停”的客户端识别
// （腾讯会议 / Teams / Zoom / Webex）。
//
// 非 Windows 平台走 _other 桩，返回空列表 —— 调用方必须能接受“认不出任何进程”。
package winproc

import "strings"

// 系统进程枚举 —— 历史源码/appProcess.ts 的 Go 版。
//
// Windows 用 kernel32 的 Toolhelp32 API：CreateToolhelp32Snapshot + Process32FirstW/NextW。
// 相比起子进程跑 `tasklist` / `wmic`，这里没有 GBK 编码问题（宽字符直接读），
// 也不用担心 Windows 11 24H2 起 `wmic` 已被移除；而且**不启动任何子进程**，
// 在限制子进程的安全策略下同样能跑（上一版走 koffi 也是同一个理由）。
//
// 调用失败一律降级为 supported=false，绝不抛错 —— 应用自动暂停只是锦上添花的功能。
//
// 本文件是平台中立部分：结果类型定义。
// 平台相关的枚举实现在 proc_windows.go（//go:build windows）；
// 其它平台返回“不支持”的桩在 proc_other.go（//go:build !windows）。

// AppProcessList 进程枚举结果（字段名与 appProcess.ts 的 AppProcessList 一致）
type AppProcessList struct {
	Supported bool     `json:"supported"`
	Names     []string `json:"names"`
	Detail    string   `json:"detail"`
}

// hostProcessNames 系统宿主进程：它们承载的是**别的程序**的窗口或功能，
// 把这类名字当成“某个应用”写进按进程名生效的名单，等于一口气放行一大片不相干的东西：
//   - ApplicationFrameHost：UWP 应用的窗口壳（照片、电影和电视、便笺……）；
//   - rundll32：旧版 Windows 照片查看器（PhotoViewer.dll）等一大票内置 DLL 的宿主；
//   - dllhost：Shell 扩展与 COM 代理；
//   - svchost / explorer / shellhost：系统本身；
//   - dwm：桌面窗口管理器。全屏应用被关掉、桌面重新成为前台时，GetForegroundWindow
//     会短暂返回 dwm 拥有的合成表面 —— 它铺满整屏且无边框，几何判定会误认成“全屏应用”
//     （实测会弹出「dwm 正在全屏运行」的询问）。
//
// 因此“全屏时不暂停”名单**拒收**这些名字（见 settings 的归一化与 windnd 的 CanMark），
// 几何全屏判定也**不认**它们（见 windnd 的 fullscreenOf）。
var hostProcessNames = map[string]bool{
	"applicationframehost.exe": true,
	"rundll32.exe":             true,
	"dllhost.exe":              true,
	"svchost.exe":              true,
	"explorer.exe":             true,
	"shellhost.exe":            true,
	"dwm.exe":                  true,
}

// IsHostProcess 这个名字是否属于“不该被当成某个应用”的系统宿主进程。
// 大小写不敏感、容忍前后空白（用户可能从任务管理器复制粘贴）。
func IsHostProcess(exe string) bool {
	return hostProcessNames[strings.ToLower(strings.TrimSpace(exe))]
}
