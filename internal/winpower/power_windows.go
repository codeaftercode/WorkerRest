//go:build windows

package winpower

import "syscall"

var (
	modKernel32        = syscall.NewLazyDLL("kernel32.dll")
	procGetTickCount64 = modKernel32.NewProc("GetTickCount64")
)

// SysWakeTick 读系统自开机以来的毫秒数（GetTickCount64，**不含睡眠 / 休眠时间**）。
// 第二个返回值表示是否读到。kernel32 的 GetTickCount64 永不失败，这里恒为 true；
// 保留布尔返回是为了与 idleState 的 tickFn 保持同一形状，方便单测注入假源。
func SysWakeTick() (int64, bool) {
	r, _, _ := procGetTickCount64.Call()
	return int64(r), true
}
