//go:build windows

package windnd

import (
	"syscall"
	"unsafe"
)

var (
	modShell32                       = syscall.NewLazyDLL("shell32.dll")
	procSHQueryUserNotificationState = modShell32.NewProc("SHQueryUserNotificationState")
)

// queryQunsState 查一次系统通知状态。ok=假表示 API 不可用（调用失败）。
// 勿扰与全屏独占共用这一次查询：两者是同一个返回值的不同取值，分开调会多打一次 DLL。
func queryQunsState() (state uint32, ok bool) {
	hr, _, _ := procSHQueryUserNotificationState.Call(uintptr(unsafe.Pointer(&state)))
	// HRESULT：S_OK = 0；失败值当 uintptr 看是大正数，判 != 0 即可
	if hr != 0 {
		return 0, false
	}
	return state, true
}

// GetDndStatus 查询当前系统勿扰状态。永远返回结果，不抛错。
func GetDndStatus() DndStatus {
	state, ok := queryQunsState()
	return dndStatusOf(state, ok)
}

// GetFullScreenStatus 查询当前是否处于全屏独占。永远返回结果，不抛错。
func GetFullScreenStatus() FullScreenStatus {
	state, ok := queryQunsState()
	return fullScreenStatusOf(state, ok)
}

// Query 一次查询同时返回勿扰与全屏独占两个结论。
//
// 两条支路共用同一次 SHQueryUserNotificationState 调用（同一个返回值的不同取值），
// 分开调会多打一次 DLL —— 3 秒一跳的监视循环里，这是无谓的开销。
func Query() (dnd DndStatus, full FullScreenStatus) {
	state, ok := queryQunsState()
	return dndStatusOf(state, ok), fullScreenStatusOf(state, ok)
}
