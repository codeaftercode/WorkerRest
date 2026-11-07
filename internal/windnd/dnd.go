// Package windnd 判断“现在该不该让提醒窗弹出来”：系统请勿打扰、全屏应用、
// Windows 演示设置、正在投屏，都算不应打扰。
//
// 判据来自 Windows 的两条**互相独立**的系统信号：
//   - shell32!SHQueryUserNotificationState → QUERY_USER_NOTIFICATION_STATE（QUNS）
//   - ntdll!NtQueryWnfStateData → WNF_SHEL_QUIETHOURS_ACTIVE_PROFILE_CHANGED（勿扰档位）
//
// 非 Windows 平台走 _other 桩，恒返回“不支持”，保证跨平台编译与测试。
package windnd

import "fmt"

// 两条系统状态自动暂停选项各自的判据（2026-11-06 三次收敛定稿）。
//
// 最初“勿扰 / 系统忙碌 / 演示设置”挤在一条 dndPause 开关里共用 QUNS 的 2 / 4，
// 结果 Win11 通知中心手动开勿扰根本不改 QUNS，那条开关名不副实；中途拆成三条独立开关，
// 又撞上 QUNS 自身的缺口：它是**互斥单值**，微软枚举页把“演示设置”同时写进了
// QUNS_BUSY(2) 和 QUNS_PRESENTATION_MODE(4)，函数页却只给一条二分契约——
// “只有 QNS_ACCEPTS_NOTIFICATIONS(5) 才允许弹通知”，从不承诺让调用者按数值反推成因，
// 也没公开 2 与 4 的判定顺序。既然分不出来，就别假装能分：全屏与演示合成一条选项。
//
//   - 请勿打扰 → WNF 勿扰档位 ≠ 0（Win11 通知中心“请勿打扰”、Win10 专注助手手动档位）
//   - 全屏 / 演示 → QUNS 2（非独占全屏或演示设置已应用）、3（D3D 独占全屏）、
//     4（Windows 演示设置主动屏蔽通知），三者任一即成立
//
// 判据没删、只并了开关：2 是浏览器 F11 / 播放器全屏这类“非独占全屏”的唯一兜底，
// 应用占用按进程名匹配、原来只认 3，删掉会让全屏看视频时弹窗无人拦。
//
// 两条各一个开关、一条暂停原因、一份忽略表；两条探测链路失败时也只跳过自己那条，
// 绝不因为 WNF 读不到就把 QUNS 的结论一起作废。
//
// 一次采样把两条信号各读一次，两个结论由纯函数从这两个原始值算出来 —— 真机上没法让
// 系统 API 按需要返回指定状态，单测直接喂原始值即可覆盖全部矩阵。

// QUERY_USER_NOTIFICATION_STATE 的取值（winuser.h / shellapi.h 里的 QUNS_*）
const (
	qunsNotPresent           = 1 // 锁屏 / 屏保 / 非活动快速用户切换会话
	qunsBusy                 = 2 // 全屏应用运行或应用了演示设置（微软官方 QUNS_BUSY 定义）
	qunsRunningD3DFullScreen = 3 // 全屏独占应用（真全屏游戏、播放器全屏）
	qunsPresentationMode     = 4 // Windows 演示设置已开启（不是 Win+P 投影）
	qunsAcceptsNotifications = 5 // 正常接收通知
	qunsQuietTime            = 6 // 新用户首次登录后一小时 / 大版本升级后的头一小时
	qunsApp                  = 7 // Windows 商店应用在前台
)

// WNF_SHEL_QUIETHOURS_ACTIVE_PROFILE_CHANGED 的数据是“当前生效的勿扰档位”。
// 0 = 没开；1 / 2 = 开了（Win11 的“请勿打扰”与 Win10 专注助手的仅优先 / 仅闹钟）。
// WNF 是未文档化接口，所以查询失败一律降级成“不支持”，绝不凭空暂停。
const (
	quietHoursOff           = 0
	quietHoursPriorityOnly  = 1
	quietHoursAlarmsOnly    = 2
	wnfQuietHoursActiveName = uint64(0x0D83063EA3BF1C75)
)

// DndStatus 系统勿扰检测结果（字段名与诊断页 / 冒烟断言一致）
type DndStatus struct {
	Supported bool   `json:"supported"`
	Active    bool   `json:"active"`
	Detail    string `json:"detail"`
}

// FullScreenStatus 全屏应用 / 演示设置检测结果（QUNS 2 / 3 / 4）。
// 类型名沿用旧的全屏判据，内部标识符不改以缩小改动面；对外文案一律写“全屏或演示”。
type FullScreenStatus struct {
	Supported bool   `json:"supported"`
	Active    bool   `json:"active"`
	Detail    string `json:"detail"`
}

// BusyStatus 一次系统采样的原始值。两条信号各带自己的 Supported —— 这是“两条选项
// 互不相关”在探测层的体现：WNF 读不到只让“勿扰”那一条整拍跳过，QUNS 那条照常判。
type BusyStatus struct {
	QunsSupported bool
	Quns          uint32
	WnfSupported  bool
	QuietProfile  uint32
}

func (s BusyStatus) Dnd() DndStatus { return dndStatusOfProfile(s.QuietProfile, s.WnfSupported) }
func (s BusyStatus) FullScreen() FullScreenStatus {
	return fullScreenStatusOf(s.Quns, s.QunsSupported)
}

var quietHoursProfileText = map[uint32]string{
	quietHoursOff:          "off（正常接收通知）",
	quietHoursPriorityOnly: "priority-only（勿扰 / 仅优先通知）",
	quietHoursAlarmsOnly:   "alarms-only（勿扰 / 仅闹钟）",
}

// qunsStateText 取值 → 人话。未知取值也给出可读说明，绝不返回空串。
// 6（QUNS_QUIET_TIME）2026-10-27 起明确不算勿扰：那是首次登录 / 大版本升级后的
// 静默时间，不是用户主动开的勿扰。
var qunsStateText = map[uint32]string{
	qunsNotPresent:           "not-present（锁屏 / 屏保 / 非活动快速用户切换会话）",
	qunsBusy:                 "busy（全屏应用运行或应用了演示设置）",
	qunsRunningD3DFullScreen: "d3d-fullscreen（全屏独占应用运行中）",
	qunsPresentationMode:     "presentation（演示设置）",
	qunsAcceptsNotifications: "accepts-notifications（正常接收通知）",
	qunsQuietTime:            "quiet-time（首次登录 / 大版本升级后的静默时间，不视为勿扰）",
	qunsApp:                  "app（Windows 商店应用在前台，不视为勿扰）",
}

func qunsText(state uint32) string {
	if text, ok := qunsStateText[state]; ok {
		return text
	}
	return fmt.Sprintf("state=%d（未知取值）", state)
}

// dndStatusOfProfile WNF 勿扰档位 → 结论。
// 未知的非零档位按“勿扰生效”处理：拿不准时宁可不弹窗，也不在用户按了勿扰之后弹。
func dndStatusOfProfile(profile uint32, ok bool) DndStatus {
	if !ok {
		return DndStatus{Supported: false, Detail: "WNF 勿扰档位读取失败"}
	}
	if profile == quietHoursOff {
		return DndStatus{Supported: true, Active: false, Detail: quietHoursProfileText[quietHoursOff]}
	}
	text, known := quietHoursProfileText[profile]
	if !known {
		text = fmt.Sprintf("profile=%d（未知档位，按勿扰处理）", profile)
	}
	return DndStatus{Supported: true, Active: true, Detail: text}
}

// fullScreenStatusOf 认 2 / 3 / 4，三者合起来才是“屏幕上正在放内容”的全部场景：
//   - QUNS_BUSY(2)：官方定义“全屏应用运行或应用了演示设置”，覆盖浏览器 F11、
//     播放器全屏、无边框覆盖整屏的窗口（非独占全屏），也覆盖演示设置。
//   - QUNS_RUNNING_D3D_FULL_SCREEN(3)：D3D 独占全屏（真全屏游戏）。
//   - QUNS_PRESENTATION_MODE(4)：Windows 演示设置被用户主动激活以屏蔽通知。
//
// 三者互斥（同一次查询只返回一个值），微软没公开 2 与 4 谁对应演示设置的判定顺序，
// 函数页的契约也只规定“只有 5 允许弹通知”。既然 API 不给成因，就统一归一条开关。
// 2026-11-06 起 QUNS=2 不再单列“系统忙碌”、QUNS=4 不再单列“演示设置”。
func fullScreenStatusOf(state uint32, ok bool) FullScreenStatus {
	if !ok {
		return FullScreenStatus{Supported: false, Detail: "SHQueryUserNotificationState 调用失败"}
	}
	return FullScreenStatus{
		Supported: true,
		Active:    state == qunsBusy || state == qunsRunningD3DFullScreen || state == qunsPresentationMode,
		Detail:    qunsText(state),
	}
}
