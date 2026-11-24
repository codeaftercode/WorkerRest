package main

import (
	"reflect"
	"sort"
)

// 前端调用契约：web/dist/shim.js 会按名字调用的绑定方法。
//
// 为什么单独列一份：Wails v3 的前端调用是“按全限定名调用”
// （`Call.ByName("main.App.<方法名>", ...)`，见 shim.js），方法一旦改名，
// 前端只会在运行时静默失败——JS 里拼错的名字没有任何编译期检查。
// 这里把契约显式化，并由 contract_test.go 做三重校验：
//   1. 前缀常量与 *App 的类型名一致；
//   2. wiredCalls 里的方法在 *App 上都存在；
//   3. wiredCalls 与 shim.js 里真正 `call("<名>")` 的集合完全一致（双向）。
//
// 新增前端调用的绑定方法时，务必同步这个列表（以及 web/dist/shim.js）。

// wiredCalls 已接通的绑定：前端真的会调、Go 侧有实现。
// “已接通”= 名字与签名对得上、调用不会断；所有方法都已是真实现（含文件选择框 /
// 导出报表 / 检查更新，见 bindings_settings.go / filedialog.go / update.go）。
var wiredCalls = []string{
	// ---- 休息窗口 ----
	"BreakCurrent",
	"Action",
	"PreviewBreak",
	"StartBreak",

	// ---- 全屏询问窗（fsask.html） ----
	"FullScreenAskAnswer",

	// ---- 通用 / 窗口 ----
	"CloseWindow",
	"SupActivity",
	"GetState",
	"WindowMaximized",
	"QuitApp",
	"ListDisplays",
	"KnownDisplays",

	// ---- 设置读写 ----
	"GetSettings",
	"SaveSettings",
	"ResetSettings",
	"BuiltinMsgDefaults",

	// ---- 督休专员目录 ----
	"ListRoles",
	"SaveRole",
	"RemoveRole",
	"PickSupervisorImage",
	"ReplaceRoleImage",
	"RoleFigureDataURL",
	"SaveRolePreview",
	"SupervisorPreview",

	// ---- 关于页 / 更新 / 外链 ----
	"AppInfo",
	"UpdateStatus",
	"CheckUpdate",
	"StartUpdate",
	"PendingUpdate",
	"IgnoreVersion",
	"OpenExternal",
	"ExportReport",
	// 开源鸣谢：关于页入口读清单、另存为导出一份（见 notices.go）
	"GetThirdPartyNotices",
	"ExportThirdPartyNotices",
	// 项目许可条款：同一行的另一个入口（见 license.go）——便携目录不再散放 LICENSE，
	// 这两个绑定就是条款的唯一出口
	"GetProjectLicense",
	"ExportProjectLicense",

	// ---- 窗口 / 自更新重启恢复 ----
	"ShowSettingsWindow",
	"SetActiveTab",

	// ---- 全局快捷键（见 hotkeys.go）----
	// HotkeyStatus：设置页打开时拉一次最近一次注册结果（启动注册早于页面加载，事件已错过）
	// CheckHotkeyConflicts：快捷键页「检测冲突」按钮，重新注册一遍并回传逐项结果
	// SetHotkeyCapture：录入时挂起四项 —— 否则按下的组合键会被系统投给热键线程执行动作
	// SetShortcut：改一项快捷键 —— 先按候选配置真注册，成功才落盘（原子提交）
	// ResetShortcuts：四项一起恢复出厂组合键 —— 同一套原子提交，任一默认键被占用就整体放弃
	"HotkeyStatus",
	"CheckHotkeyConflicts",
	"SetHotkeyCapture",
	"SetShortcut",
	"ResetShortcuts",

	// ---- 统计 ----
	"GetStats",
	"GetHistory",
	"ClearStats",
	// 统计文件损坏已备份重建的告知（见 stats.go 的 StatsFileNotice）
	"GetStatsFileNotice",
	"AckStatsFileNotice",

	// ---- 声音 ----
	"SoundFileURL",
	"AddMusicSound",
	"DeleteMusicSound",
	// 随包 sounds/ 的启动体检：新音频待导入 / 条目文件缺失（见 sound_scan.go）
	"GetSoundScanNotice",
	"ImportScannedSounds",
	"DropMissingSounds",
	"DismissSoundScan",

	// ---- 指定应用运行时暂停 ----
	"ListRunningApps",
	"PickAppExe",

	// ---- 提前提示窗口（右下角小窗，见 prebreak.go） ----
	"PreBreakState",
	"PreBreakAction",

	// ---- 自动暂停期间的“已暂停多久”提示（见 pause_notice.go） ----
	"PauseNoticeResume",
	"PauseNoticeDismiss",

	// ---- 外卖窗口（见 takeout.go） ----
	"TakeoutCurrent",
	"PreviewTakeout",
	"TakeoutAction",
}

// pendingCalls 尚未接通的绑定：Go 侧已有同名占位方法（前端调用不会报错），
// 但 shim.js 里仍是 stub。迁移完成时把它挪进 wiredCalls，contract_test 会顺手校验 shim 是否真的接上了。
var pendingCalls = []string{}

// wiredArity 每个已接通绑定的**形参个数**（不含接收者）。
//
// 为什么需要它：契约原先只校验“方法名存在”，而 shim.js 是逐个手写实参数组的。
// Go 侧给某个方法加一个参数 → 名字没变 → go test 全绿 → shim.js 仍传旧参数 →
// Wails 返 422 → shim.js 的 catch 把错误吞成 null → 界面静默降级（最坏的失败形态）。
// 把个数也钉住之后，arity 漂移在 `go test` 阶段就炸，而不是等用户发现功能没了。
//
// 三处必须同时一致（contract_test.go 的 TestShimArityMatchesCallSites 做三方比对）：
//  1. 本表的条目；2. *App 上方法的 reflect 形参个数；3. shim.js 里 ARITY 表与真实实参数组。
//
// 新增/修改绑定签名时，本表、shim.js 的 ARITY、以及 shim.js 的调用处要一起改。
//
// 条数必须与 wiredCalls 完全相同（TestContractArityMatchesApp 双向校验，漏登记即 fail）——
// 这里不采用“没登记就不校验”的宽松口径：Go 侧是权威且可反射得到的，没有理由留空。
var wiredArity = map[string]int{
	// ---- 休息窗口 ----
	"BreakCurrent": 1, // (win)
	"Action":       2, // (win, name)
	"PreviewBreak": 2, // (type, forceKey)
	"StartBreak":   1, // (kind)

	// ---- 全屏询问窗（fsask.html） ----
	"FullScreenAskAnswer": 1, // (answer)

	// ---- 通用 / 窗口 ----
	"CloseWindow":     1, // (win)
	"SupActivity":     1, // (win)
	"GetState":        0,
	"WindowMaximized": 0,
	"QuitApp":         0,
	"ListDisplays":    0,
	"KnownDisplays":   0,

	// ---- 设置读写 ----
	"GetSettings":        0,
	"SaveSettings":       1, // (cfg)
	"ResetSettings":      0,
	"BuiltinMsgDefaults": 0,

	// ---- 督休专员目录 ----
	"ListRoles":           0,
	"SaveRole":            2, // (key, cfg)
	"RemoveRole":          1, // (key)
	"PickSupervisorImage": 0,
	"ReplaceRoleImage":    1, // (key)
	"RoleFigureDataURL":   1, // (key)
	"SaveRolePreview":     2, // (key, dataURL)
	"SupervisorPreview":   2, // (sup, key)

	// ---- 关于页 / 更新 / 外链 ----
	"AppInfo":                 0,
	"UpdateStatus":            0,
	"CheckUpdate":             0,
	"StartUpdate":             0,
	"PendingUpdate":           0,
	"IgnoreVersion":           1, // (version)
	"OpenExternal":            1, // (url)
	"ExportReport":            2, // (name, b64)
	"GetThirdPartyNotices":    0,
	"ExportThirdPartyNotices": 0,
	"GetProjectLicense":       0,
	"ExportProjectLicense":    0,

	// ---- 窗口 / 自更新重启恢复 ----
	"ShowSettingsWindow": 0,
	"SetActiveTab":       1, // (tab)

	// ---- 全局快捷键 ----
	"HotkeyStatus":         0,
	"CheckHotkeyConflicts": 0,
	"SetHotkeyCapture":     1, // (on bool) 录入中挂起 / 恢复
	"SetShortcut":          2, // (key, accel) → ShortcutApplyResult{ok,state,kept,list}
	"ResetShortcuts":       0, // → ShortcutResetResult{ok,state,key,list}（四项一起恢复默认）

	// ---- 统计 ----
	"GetStats":           0,
	"GetHistory":         2, // (start, end)
	"ClearStats":         0,
	"GetStatsFileNotice": 0,
	"AckStatsFileNotice": 0,

	// ---- 声音 ----
	"SoundFileURL":        1, // (file)
	"AddMusicSound":       0,
	"DeleteMusicSound":    1, // (file)
	"GetSoundScanNotice":  0,
	"ImportScannedSounds": 1, // (files []string)
	"DropMissingSounds":   1, // (ids []string)
	"DismissSoundScan":    0,

	// ---- 指定应用运行时暂停 ----
	"ListRunningApps": 0,
	"PickAppExe":      0,

	// ---- 提前提示窗口 ----
	"PreBreakState":  0,
	"PreBreakAction": 1, // (action)

	// ---- 自动暂停期间的“已暂停多久”提示 ----
	"PauseNoticeResume":  0,
	"PauseNoticeDismiss": 0,

	// ---- 外卖窗口 ----
	"TakeoutCurrent": 1, // (win)
	"PreviewTakeout": 1, // (win)
	"TakeoutAction":  2, // (win, action)
}

// diagnosticCalls 前端契约之外、但被 Wails 注册成绑定的导出方法（诊断 / 内部通道）。
//
// 为什么单列：Wails 的 NewService 会把结构体上**全部**导出方法反射成 main.App.<名>，
// 所以 `-tags server` 构建（无头 HTTP 服务）下这些方法同样对任何能访问本机端口的
// 进程开放。它们不在前端契约里（shim.js 不调），但删不得（探针、托盘、内部事件用），
// 于是显式登记为“诊断用”，让合同测试能对“新增导出方法”强制走一次登记：
// 不登记 → contract_test 直接 fail，逼你回答“这个方法该不该被注册成绑定”。
//
// 若将来要收窄注册面（例如用白名单服务包装层只暴露 wiredCalls），这一份就是
// “不在白名单里”的清单本身。本轮只做断言，不改注册面。
var diagnosticCalls = []string{
	// 内部事件钩子：Go 侧向窗口广播状态时调用的方法，不是前端入口
	"OnAutoPause",
	"OnBreakCounted",
	"OnBreakExtra",
	"OnBreakPostponed",
	"OnBreakSkipped",
	"OnPaused",
	"OnPreNotify",
	"OnSchedulerState",
	// 窗口 / 会话的内部开关（由 Go 侧或托盘触发）
	"CloseBreakWindows",
	"ClosePreBreak",
	"CompleteBreakWindows",
	"ShowBreakSession",
	"ShowBreakWindow",
	"ShowPreBreakWindow",
	// 自启动与配置快照（托盘 / 设置页内部的读接口）
	"ApplyAutostart",
	"CfgSnapshot",
	"IdleMs",
	// 调度器 Host 接口要求的方法：只读一个内存字段（询问窗是否在等答复），
	// 供 onTick 在到点那一拍决定要不要先按住休息窗。前端永远不该调它。
	"BreakHoldRequested",
	// 诊断探针：scripts/probe/*.py 与本机排查用
	"SysProbe",
	"StatsProbe",
	"TakeoutFiredSnapshot",
	"TakeoutIdleWaitSnapshot",
	"TakeoutPostponedSnapshot",
}

// serviceLifecycleCalls Wails 服务生命周期接口的方法：**不**注册成绑定，
// 因此既不在 wiredCalls/diagnosticCalls 里，也不该出现在绑定的注册日志里。
var serviceLifecycleCalls = []string{"ServiceStartup", "ServiceShutdown"}

// allCalls 契约全集（自检与文档用）
func allCalls() []string {
	out := make([]string, 0, len(wiredCalls)+len(pendingCalls))
	out = append(out, wiredCalls...)
	out = append(out, pendingCalls...)
	sort.Strings(out)
	return out
}

// bindingFQN 拼出 Wails 注册表里的全限定名：PkgPath.Type.Method。
//
// 值是 "main.App."，原因是**构建产物的 PkgPath 就是 "main"**：
// wails 用 reflect 的 PkgPath() 拼名字，而 main 包在 `go build` 出的二进制里
// 其 PkgPath 被编译器置为 "main"（不是模块名、也不是 package 子句里的名字）。
//
// 反直觉的坑（本项目踩过）：`go test` 里同一份代码的 PkgPath() 返回的是**导入路径**
// "workerrest"（模块名）——因为测试二进制把被测包当普通库链接。
// 所以单元测试里反射拿到的值不能当构建产物的真值，前缀只能写死、
// 并由 scripts/smoke_server.py 在真实二进制上端到端校验（按名调用，找不到即 422）。
const bindingFQN = "main.App."

// missingFrontendBindings 返回契约里有、但 *App 上不存在的方法名（启动自检用）
func missingFrontendBindings() []string {
	t := reflect.TypeOf(&App{})
	var missing []string
	for _, name := range allCalls() {
		if _, ok := t.MethodByName(name); !ok {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	return missing
}
