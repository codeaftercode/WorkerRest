//go:build !windows

package windnd

// GetCastStatus 非 Windows 平台没有统一的“桌面是否被镜像到别的屏”查询接口：
// macOS 有 CGDisplayMirrorsDisplay，Linux 要看 XRandR / Mutter 的显示配置，
// 两边都得单独实现。在实现之前一律返回“不支持”，让投屏自动暂停在该平台上
// 保持关闭，而不是猜一个可能误判的结论。
func GetCastStatus() CastStatus {
	return CastStatus{Supported: false, Detail: "当前平台未实现投屏检测"}
}
