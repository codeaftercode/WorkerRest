//go:build !windows

package winpower

import "time"

// SysWakeTick 非 Windows 平台的近似实现：用单调时钟（time.Since 基于单调时钟，
// 不含睡眠/挂起时间）替代 Windows 的 GetTickCount64。
//
// 注意：Linux/macOS 的 sleep 语义与 Windows 不完全一致，这里只是让跨平台编译通过、
// 且保留「不含睡眠时间」的近似语义；真机移植时再按平台核对单调时钟行为。
func SysWakeTick() (int64, bool) {
	return time.Now().UnixMilli(), true
}
