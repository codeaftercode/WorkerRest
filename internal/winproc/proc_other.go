//go:build !windows

package winproc

// ListProcessNames 非 Windows 平台的桩：本项目尚未做 Linux/macOS 真机适配，
// 恒返回「不支持」。未来真机移植时，Linux 读 /proc 枚举、macOS 用 sysctl 或
// NSWorkspace，补到对应 _linux.go/_darwin.go 即可。
func ListProcessNames() AppProcessList {
	return AppProcessList{Supported: false, Names: []string{}, Detail: "进程枚举仅支持 Windows"}
}
