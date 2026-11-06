//go:build !windows

package windnd

// 非 Windows 平台的系统状态检测桩：本项目尚未做 Linux/macOS 真机适配，
// 恒返回「不支持 + 有说明」，绝不报 active（否则会凭空暂停提醒）。
// 未来真机移植时，Linux 可用 gsettings 查勿扰，macOS 无公开 API 可保持不支持。

// Query 非 Windows 平台：两条信号都不支持。
func Query() BusyStatus {
	return BusyStatus{QunsSupported: false, WnfSupported: false}
}

// GetDndStatus 非 Windows 平台返回「不支持」。
func GetDndStatus() DndStatus {
	return DndStatus{Supported: false, Detail: "勿扰检测仅支持 Windows"}
}

// GetFullScreenStatus 非 Windows 平台返回「不支持」。
func GetFullScreenStatus() FullScreenStatus {
	return FullScreenStatus{Supported: false, Detail: "全屏检测仅支持 Windows"}
}
