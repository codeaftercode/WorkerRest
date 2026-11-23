//go:build !windows

package windnd

// 非 Windows 平台的前台窗口快照桩：恒返回“不支持 + 有说明”，
// 由调用方按“没有结论”处理（绝不据此暂停，也绝不据此放行）。

// ForegroundSnapshot 非 Windows 平台：不支持
func ForegroundSnapshot() ForegroundInfo {
	return ForegroundInfo{Supported: false, Detail: "前台窗口全屏判定仅支持 Windows"}
}
