package main

import (
	"reflect"
	"sort"
)

// 前端调用契约：frontend/dist/shim.js 会按名字调用的绑定方法。
//
// 为什么单独列一份：Wails v3 的前端调用是「按全限定名调用」
// （`Call.ByName("main.App.<方法名>", ...)`，见 shim.js），方法一旦改名，
// 前端只会在运行时静默失败——JS 里拼错的名字没有任何编译期检查。
// 这里把契约显式化，并由 contract_test.go 做三重校验：
//   1. 前缀常量与 *App 的类型名一致；
//   2. wiredCalls 里的方法在 *App 上都存在；
//   3. wiredCalls 与 shim.js 里真正 `call("<名>")` 的集合完全一致（双向）。
//
// 新增前端调用的绑定方法时，务必同步这个列表（以及 frontend/dist/shim.js）。

// wiredCalls 已接通的绑定：前端真的会调、Go 侧有实现。
// 「已接通」= 名字与签名对得上、调用不会断；所有方法都已是真实现（含文件选择框 /
// 导出报表 / 检查更新，见 bindings_settings.go / filedialog.go / update.go）。
var wiredCalls = []string{
	// ---- 休息窗口 ----
	"BreakCurrent",
	"Action",
	"PreviewBreak",
	"StartBreak",

	// ---- 通用 / 窗口 ----
	"CloseWindow",
	"SupActivity",
	"GetState",
	"WindowMaximized",
	"QuitApp",

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

	// ---- 关于页 / 更新 / 手册 / 外链 ----
	"AppInfo",
	"UpdateStatus",
	"CheckUpdate",
	"StartUpdate",
	"PendingUpdate",
	"IgnoreVersion",
	"OpenManual",
	"OpenExternal",
	"ExportReport",

	// ---- 窗口 / 自更新重启恢复 ----
	"ShowSettingsWindow",
	"SetActiveTab",

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

	// ---- 外卖窗口（见 takeout.go） ----
	"TakeoutCurrent",
	"PreviewTakeout",
	"TakeoutAction",
}

// pendingCalls 尚未接通的绑定：Go 侧已有同名占位方法（前端调用不会报错），
// 但 shim.js 里仍是 stub。迁移完成时把它挪进 wiredCalls，contract_test 会顺手校验 shim 是否真的接上了。
var pendingCalls = []string{}

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
