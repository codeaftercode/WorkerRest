package main

import (
	"fmt"
	"syscall"
	"unsafe"
)

// 系统勿扰（Do Not Disturb / 专注助手）检测 —— electron/src/dnd.ts 的 Go 版。
//
// Windows 的文档化 API 是 shell32!SHQueryUserNotificationState，覆盖 Win10 专注助手
// （仅优先 / 仅闹钟）、Win11 勿扰、演示模式等系统级「不希望被打扰」状态。
// Electron 版用 koffi 调用；Go 不需要任何外部依赖，直接用 syscall 调 DLL，
// 也就没有「打包遗漏原生模块 → 整体降级」这条失败路径了。
//
// 本项目只做 Windows（Wails v3 的目标平台），所以 dnd.ts 里的 Linux（gsettings）与
// macOS（无公开 API）两条分支不移植。
//
// 调用失败一律降级为「不支持」，绝不抛错 —— 勿扰暂停只是锦上添花的功能。

// DndStatus 勿扰检测结果（字段名与 dnd.ts 的 DndStatus 一致）
type DndStatus struct {
	Supported bool   `json:"supported"`
	Active    bool   `json:"active"`
	Detail    string `json:"detail"`
}

// winDndStates QUERY_USER_NOTIFICATION_STATE 里的「勿扰态」取值 —— 与 dnd.ts 的
// WIN_DND_STATES 逐条对齐（取值相同、文案逐字相同）。
var winDndStates = map[uint32]string{
	2: "busy（勿扰/仅优先打扰）",
	4: "presentation（演示模式）",
	6: "quiet（勿扰/专注助手）",
}

var (
	modShell32                       = syscall.NewLazyDLL("shell32.dll")
	procSHQueryUserNotificationState = modShell32.NewProc("SHQueryUserNotificationState")
)

// GetDndStatus 查询当前系统勿扰状态。永远返回结果，不抛错。
func GetDndStatus() DndStatus {
	var state uint32
	hr, _, _ := procSHQueryUserNotificationState.Call(uintptr(unsafe.Pointer(&state)))
	// HRESULT：S_OK = 0；失败值当 uintptr 看是大正数，判 != 0 即可
	if hr != 0 {
		return DndStatus{Supported: false, Detail: "SHQueryUserNotificationState 调用失败"}
	}
	if name, ok := winDndStates[state]; ok {
		return DndStatus{Supported: true, Active: true, Detail: name}
	}
	return DndStatus{
		Supported: true,
		Active:    false,
		Detail:    fmt.Sprintf("state=%d（正常接收通知）", state),
	}
}
