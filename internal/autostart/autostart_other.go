//go:build !windows

package autostart

// runValueName 非 Windows 平台的占位值名（本项目尚未做 Linux/macOS 真机适配）。
const runValueName = "com.goodgoodlive.workerrest"

// 非 Windows 平台没有注册表，这些路径仅供签名对齐，不会被用到。
var (
	runKeyPath      = ""
	approvedKeyPath = ""
)

// ReadRunEntry 非 Windows 平台 no-op：返回 nil（没有启动项）。
func ReadRunEntry() *string { return nil }

// RunEntryExists 非 Windows 平台 no-op。
func RunEntryExists() bool { return false }

// WriteRunEntry 非 Windows 平台 no-op：未来真机移植时写 autostart/.desktop 或 launchd。
func WriteRunEntry(cmdline string) RegResult {
	return RegResult{OK: false, Detail: "not implemented on this platform"}
}

// RemoveRunEntry 非 Windows 平台 no-op。
func RemoveRunEntry() RegResult {
	return RegResult{OK: true, Detail: "removed"}
}

// ClearApproval 非 Windows 平台 no-op。
func ClearApproval() RegResult { return RegResult{OK: true, Detail: "absent"} }

// ApprovalDisabled 非 Windows 平台 no-op：返回 false（默认启用）。
func ApprovalDisabled() *bool {
	f := false
	return &f
}
