//go:build windows

package autostart

import (
	"golang.org/x/sys/windows/registry"
)

// runValueName HKCU Run 键里的值名，同时也是本进程的 AppUserModelId。
// 与单例 UniqueID、站点域名统一在同一个反向域名下。
const runValueName = "com.goodgoodlive.workerrest"

// 键路径写成变量（不是常量）：单测要把它们换成临时键做真实读写往返，
// **绝不能拿用户真实的 Run 键当试验田**。
var (
	runKeyPath      = `Software\Microsoft\Windows\CurrentVersion\Run`
	approvedKeyPath = `Software\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\Run`
)

// ReadRunEntry 读回 Run 键里的原始命令行；不存在返回 nil。
// 只用于日志与核验，不做内容比对 —— 比对要先过 NormalizeCommandLine。
func ReadRunEntry() *string {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return nil
	}
	defer func() { _ = k.Close() }()
	v, _, err := k.GetStringValue(runValueName)
	if err != nil {
		return nil
	}
	return &v
}

// RunEntryExists Run 键里是否已有本应用的启动项。
func RunEntryExists() bool { return ReadRunEntry() != nil }

// WriteRunEntry 写入启动项（勾选开机自启动）。可重复调用 —— 已有值会被覆盖成最新命令行。
//
// 顺带清掉「启动已批准」里的禁用标记：项不存在 = 默认启用。不清的话，用户曾在任务管理器里
// 禁用过就永远是禁用的 —— Run 键写得再对也不启动。
func WriteRunEntry(cmdline string) RegResult {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err != nil {
		return RegResult{OK: false, Detail: "打开 Run 键失败：" + err.Error()}
	}
	err = k.SetStringValue(runValueName, cmdline)
	_ = k.Close()
	cleared := ClearApproval()
	if err != nil {
		return RegResult{OK: false, Detail: err.Error()}
	}
	if cleared.OK {
		return RegResult{OK: true, Detail: "written"}
	}
	return RegResult{OK: true, Detail: "written(approval not cleared: " + cleared.Detail + ")"}
}

// RemoveRunEntry 删除启动项（取消勾选开机自启动）。本来就没有也算成功 —— 目标状态已达成。
func RemoveRunEntry() RegResult {
	err := error(nil)
	if k, e := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE); e == nil {
		err = k.DeleteValue(runValueName)
		_ = k.Close()
	}
	approval := ClearApproval()
	// 不存在算成功（DeleteValue 对不存在的值返回 ERROR_FILE_NOT_FOUND）
	if err != nil && RunEntryExists() {
		return RegResult{OK: false, Detail: err.Error()}
	}
	if approval.OK {
		return RegResult{OK: true, Detail: "removed"}
	}
	return RegResult{OK: true, Detail: "removed(approval not cleared: " + approval.Detail + ")"}
}

// ClearApproval 清掉「启动已批准」里本应用的条目（启用标记与禁用标记一并清掉）。
func ClearApproval() RegResult {
	k, err := registry.OpenKey(registry.CURRENT_USER, approvedKeyPath, registry.SET_VALUE)
	if err != nil {
		// 连键都没有 → 本来就没有这一项 → 目标状态已达成
		return RegResult{OK: true, Detail: "absent"}
	}
	defer func() { _ = k.Close() }()
	if _, _, e := k.GetBinaryValue(runValueName); e != nil {
		return RegResult{OK: true, Detail: "absent"}
	}
	if e := k.DeleteValue(runValueName); e != nil {
		return RegResult{OK: false, Detail: e.Error()}
	}
	return RegResult{OK: true, Detail: "cleared"}
}

// ApprovalDisabled 「启动已批准」里是否标记为禁用（任务管理器 → 启动 页里的开关）。
// 返回 nil 表示读不出来（无法判断），调用方按「需要处理」对待。与 autostart.ts 同口径。
func ApprovalDisabled() *bool {
	ptr := func(b bool) *bool { return &b }
	k, err := registry.OpenKey(registry.CURRENT_USER, approvedKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return ptr(false) // 本来就没有这一项 = 默认启用
	}
	defer func() { _ = k.Close() }()
	b, _, err := k.GetBinaryValue(runValueName)
	if err != nil {
		return ptr(false)
	}
	if len(b) == 0 {
		return nil
	}
	// 12 字节，首字节 02 = 启用、03 = 禁用
	return ptr(b[0] != 0x02)
}
