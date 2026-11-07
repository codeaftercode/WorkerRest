//go:build windows

package windnd

import (
	"syscall"
	"unsafe"
)

var (
	modShell32                       = syscall.NewLazyDLL("shell32.dll")
	procSHQueryUserNotificationState = modShell32.NewProc("SHQueryUserNotificationState")
	modNtdll                         = syscall.NewLazyDLL("ntdll.dll")
	procNtQueryWnfStateData          = modNtdll.NewProc("NtQueryWnfStateData")
)

// queryQunsState 查一次系统通知状态。ok=假表示 API 不可用（调用失败）。
// QUNS 的取值互斥，全屏 / 演示这一个结论共用这一次查询。
func queryQunsState() (state uint32, ok bool) {
	hr, _, _ := procSHQueryUserNotificationState.Call(uintptr(unsafe.Pointer(&state)))
	// HRESULT：S_OK = 0；失败值当 uintptr 看是大正数，判 != 0 即可
	if hr != 0 {
		return 0, false
	}
	return state, true
}

// queryQuietHoursProfile 读 WNF 勿扰档位。
//
// **为什么不能只用 SHQueryUserNotificationState**（2026-11-06 真机结论）：Win11 通知中心
// 手动开“请勿打扰”、以及“开始专注”启动专注会话，都不改变 QUNS（连采 12 秒恒为 5）。
// QUNS 只描述“系统此刻愿不愿意弹通知横幅”，不描述“用户按没按勿扰”。真正的勿扰档位
// 放在 WNF_SHEL_QUIETHOURS_ACTIVE_PROFILE_CHANGED 里；这也是 YASB 等桌面工具在用的判据。
//
// 未文档化接口，所以任何异常（导出函数不存在、状态不存在、权限不足、数据长度不足）
// 都只返回“不支持”，由调用方整拍跳过勿扰这一条，不影响 QUNS 那一条。
func queryQuietHoursProfile() (uint32, bool) {
	if err := procNtQueryWnfStateData.Find(); err != nil {
		return 0, false
	}
	var (
		stamp uint32
		buf   uint32
		size  = uint32(unsafe.Sizeof(buf))
		name  = wnfQuietHoursActiveName
	)
	// NTSTATUS NtQueryWnfStateData(
	//   PCWNF_STATE_NAME StateName, PCWNF_TYPE_ID TypeId, const VOID* ExplicitScope,
	//   PWNF_CHANGE_STAMP ChangeStamp, PVOID Buffer, PULONG BufferSize)
	r, _, _ := procNtQueryWnfStateData.Call(
		uintptr(unsafe.Pointer(&name)), 0, 0,
		uintptr(unsafe.Pointer(&stamp)),
		uintptr(unsafe.Pointer(&buf)),
		uintptr(unsafe.Pointer(&size)))
	if int32(r) < 0 || size < uint32(unsafe.Sizeof(buf)) {
		return 0, false
	}
	return buf, true
}

// Query 一次采样：两条信号各读一次，两个结论从原始值算出来。
func Query() BusyStatus {
	quns, qunsOK := queryQunsState()
	profile, wnfOK := queryQuietHoursProfile()
	return BusyStatus{
		QunsSupported: qunsOK,
		Quns:          quns,
		WnfSupported:  wnfOK,
		QuietProfile:  profile,
	}
}

// GetDndStatus 系统勿扰状态（WNF 档位）。永远返回结果，不抛错。
func GetDndStatus() DndStatus { return Query().Dnd() }

// GetFullScreenStatus 全屏 / 演示状态（QUNS 2 / 3 / 4）。永远返回结果，不抛错。
func GetFullScreenStatus() FullScreenStatus { return Query().FullScreen() }
