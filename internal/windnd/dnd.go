// Package windnd 判断「现在该不该让提醒窗弹出来」：系统勿扰模式、全屏独占应用
// （游戏、放映）都算不应打扰。
//
// 判据来自 Windows 的 SHQueryUserNotificationState；非 Windows 平台走 _other 桩，
// 恒返回「可以打扰」，保证跨平台编译与测试。
package windnd

import "fmt"

// 系统「不希望被打扰」状态检测（勿扰 / 专注助手 / 演示模式 / 全屏独占）——
// 历史源码/dnd.ts 的 Go 版。
//
// Windows 的文档化 API 是 shell32!SHQueryUserNotificationState，返回 QUERY_USER_NOTIFICATION_STATE。
// 上一版用 koffi 调用；Go 不需要任何外部依赖，直接用 syscall 调 DLL，
// 也就没有「打包遗漏原生模块 → 整体降级」这条失败路径了。
//
// 一次 API 查询要同时服务两件事，所以**取值只查一次、结论分开算**：
//   - 勿扰 / 专注 / 演示模式（2 / 4）→ 自动暂停提醒（开关 dndPause）
//   - 全屏独占（3）→ 自动暂停提醒（开关 fullscreenPause，默认关）
//
// 调用失败一律降级为「不支持」，绝不抛错 —— 自动暂停只是锦上添花的功能。
//
// 本文件是平台中立部分：类型、取值常量、以及「取值 → 结论」的纯函数。
// 平台相关的「查一次 DLL」在 dnd_windows.go（//go:build windows）；
// 其它平台返回「不支持」的桩在 dnd_other.go（//go:build !windows）。

// QUERY_USER_NOTIFICATION_STATE 的取值（winuser.h / shellapi.h 里的 QUNS_*）；
// 用常量而不是魔法数字，注释里写清每个取值在用户眼里是什么。
const (
	qunsNotPresent           = 1 // 锁屏 / 屏保 / 非活动快速用户切换会话
	qunsBusy                 = 2 // 全屏应用或演示设置生效（Win10 专注助手、Win11 勿扰也落在这里）
	qunsRunningD3DFullScreen = 3 // 全屏独占应用（真全屏游戏、播放器全屏）
	qunsPresentationMode     = 4 // Win+P → 演示模式
	qunsAcceptsNotifications = 5 // 正常接收通知
	qunsQuietTime            = 6 // 新用户首次登录后一小时 / 大版本升级后的头一小时
	qunsApp                  = 7 // Windows 商店应用在前台
)

// DndStatus 勿扰检测结果（字段名与 dnd.ts 的 DndStatus 一致）
type DndStatus struct {
	Supported bool   `json:"supported"`
	Active    bool   `json:"active"`
	Detail    string `json:"detail"`
}

// FullScreenStatus 全屏独占检测结果（与 DndStatus 同形状，便于日志 / 诊断页复用）
type FullScreenStatus struct {
	Supported bool   `json:"supported"`
	Active    bool   `json:"active"`
	Detail    string `json:"detail"`
}

// winDndStates QUERY_USER_NOTIFICATION_STATE 里的「勿扰态」取值 —— 与 dnd.ts 的
// WIN_DND_STATES 对齐（取值相同、文案逐字相同），只保留 2 / 4。
//
// **6（QUNS_QUIET_TIME）2026-10-27 起不再算勿扰**：微软文档写的是「新用户首次登录后的
// 一小时、大版本升级或全新安装后的头一小时」，它是静默时间，不是用户主动开的勿扰 ——
// 刚做完系统更新的人会遇到提醒莫名停一小时。
var winDndStates = map[uint32]string{
	qunsBusy:             "busy（勿扰/仅优先打扰）",
	qunsPresentationMode: "presentation（演示模式）",
}

// winDndStateText 其余取值的说明。只用于「没命中勿扰态」时的 Detail —— 以前这里一律写
// 「正常接收通知」，把 QUNS_RUNNING_D3D_FULL_SCREEN(3) 也盖成了“正常”，日志/诊断页
// 上看着像没问题、实际是「全屏独占，走另一条暂停开关」这条关键差异被抹掉了。
var winDndStateText = map[uint32]string{
	qunsNotPresent:           "not-present（锁屏 / 屏保 / 非活动快速用户切换会话）",
	qunsRunningD3DFullScreen: "d3d-fullscreen（全屏独占应用运行中，走「全屏独占」开关）",
	qunsAcceptsNotifications: "accepts-notifications（正常接收通知）",
	qunsQuietTime:            "quiet-time（首次登录 / 大版本升级后的静默时间，不视为勿扰）",
	qunsApp:                  "app（Windows 商店应用在前台，不视为勿扰）",
}

// qunsStateText 取值 → 人话。未知取值也给出可读说明，绝不返回空串。
func qunsStateText(state uint32) string {
	if text, ok := winDndStateText[state]; ok {
		return text
	}
	return fmt.Sprintf("state=%d（未知取值）", state)
}

// dndStatusOf 取值 → 勿扰结论（纯函数，单测直接喂取值，不必真开勿扰）。
func dndStatusOf(state uint32, ok bool) DndStatus {
	if !ok {
		return DndStatus{Supported: false, Detail: "SHQueryUserNotificationState 调用失败"}
	}
	if name, hit := winDndStates[state]; hit {
		return DndStatus{Supported: true, Active: true, Detail: name}
	}
	return DndStatus{Supported: true, Active: false, Detail: qunsStateText(state)}
}

// fullScreenStatusOf 取值 → 全屏独占结论（纯函数，同上）。
// 只认 3：无边框全屏 / 浏览器 F11 系统多半返回 5，本来就不认为你在“独占全屏”。
func fullScreenStatusOf(state uint32, ok bool) FullScreenStatus {
	if !ok {
		return FullScreenStatus{Supported: false, Detail: "SHQueryUserNotificationState 调用失败"}
	}
	if state == qunsRunningD3DFullScreen {
		return FullScreenStatus{Supported: true, Active: true, Detail: qunsStateText(state)}
	}
	return FullScreenStatus{Supported: true, Active: false, Detail: qunsStateText(state)}
}
