//go:build windows

package windnd

import (
	"syscall"
	"unsafe"
)

var (
	modUser32Cast                   = syscall.NewLazyDLL("user32.dll")
	procGetDisplayConfigBufferSizes = modUser32Cast.NewProc("GetDisplayConfigBufferSizes")
	procQueryDisplayConfig          = modUser32Cast.NewProc("QueryDisplayConfig")
)

const (
	// QDC_ONLY_ACTIVE_PATHS 只取“正在驱动一块屏”的路径。不用 QDC_DATABASE_CURRENT：
	// 那条路虽然能直接拿 DISPLAYCONFIG_TOPOLOGY_CLONE，但它与其他 flag 互斥（要单独调一次），
	// 而且走的是注册表里的持久化拓扑，用户用 SetDisplayConfig 临时改动后可能与现实不同步。
	qdcOnlyActivePaths = 0x00000002
	// QDC_VIRTUAL_MODE_AWARE Windows 10 起支持；不支持时下面会回退到不带这个 flag 再试一次。
	qdcVirtualModeAware = 0x00000010

	// DISPLAYCONFIG_PATH_ACTIVE 该路径参与当前桌面
	displayConfigPathActive = 0x00000001
	// DISPLAYCONFIG_PATH_SUPPORT_VIRTUAL_MODE 该路径由虚拟模式驱动（Win10 2004 起常见）。
	// 置位时 sourceInfo.modeInfoIdx 不再是数组下标，而是 cloneGroupId:16 | sourceModeInfoIdx:16
	// 的位域 —— 不拆开会把下标读成 0x1FFFF 这种越界值，镜像判定直接失效。
	displayConfigPathSupportVirtualMode = 0x00000008

	// modeInfoSize DISPLAYCONFIG_MODE_INFO 的大小。这一块内容我们一个字都不读，
	// 只是 QueryDisplayConfig 要求传一个放得下 numModeInfoArrayElements 项的缓冲区。
	modeInfoSize = 64
)

// ---- 下面几个结构体必须与 wingdi.h 的内存布局逐字节一致 ----
//
// 布局错了 QueryDisplayConfig 会直接返回 ERROR_INVALID_PARAMETER，不会崩，
// 但功能会静默失效。cast_test.go 里的 TestDisplayPathLayout 用 unsafe.Sizeof 钉住大小，
// 改动任何一个字段都会被测试拦下来。

type displayLUID struct {
	LowPart  uint32
	HighPart int32
}

type displayPathSourceInfo struct {
	adapterID   displayLUID
	id          uint32
	modeInfoIdx uint32
	statusFlags uint32
}

type displayRational struct {
	numerator   uint32
	denominator uint32
}

type displayPathTargetInfo struct {
	adapterID        displayLUID
	id               uint32
	modeInfoIdx      uint32
	outputTechnology uint32 // DISPLAYCONFIG_VIDEO_OUTPUT_TECHNOLOGY
	rotation         uint32
	scaling          uint32
	refreshRate      displayRational
	scanLineOrdering uint32
	targetAvailable  uint32 // BOOL
	statusFlags      uint32
}

type displayPathInfo struct {
	sourceInfo displayPathSourceInfo
	targetInfo displayPathTargetInfo
	flags      uint32
}

// queryActivePaths 取当前所有活动显示路径的精简视图。ok=假表示 API 不可用。
//
// 两种 flags 组合各试一次：带 QDC_VIRTUAL_MODE_AWARE 才能正确读到虚拟模式下的路径，
// 但它是 Windows 10 才支持的，老系统（或某些远程会话）会拒绝，这时退回不带 flag 的调用。
// 这一层降级有意义：投屏检测是“有就说、没有就算了”的功能，不能因为一个可选 flag
// 就让整个开关在所有机器上失效。
func queryActivePaths() ([]pathSummary, bool) {
	for _, flags := range []uint32{qdcOnlyActivePaths | qdcVirtualModeAware, qdcOnlyActivePaths} {
		if paths, ok := queryActivePathsWith(flags); ok {
			return paths, true
		}
	}
	return nil, false
}

func queryActivePathsWith(flags uint32) ([]pathSummary, bool) {
	var nPath, nMode uint32
	if r, _, _ := procGetDisplayConfigBufferSizes.Call(
		uintptr(flags),
		uintptr(unsafe.Pointer(&nPath)),
		uintptr(unsafe.Pointer(&nMode)),
	); r != 0 {
		return nil, false
	}
	if nPath == 0 {
		// 一条活动路径都没有：无头会话 / 远程桌面断开。当成“查到了，但没有输出”，
		// 而不是失败——失败会让日志天天报“不支持”，掩盖真正的问题。
		return nil, true
	}
	if nMode == 0 {
		nMode = 1
	}
	paths := make([]displayPathInfo, nPath)
	modes := make([]byte, modeInfoSize*uintptr(nMode))
	if r, _, _ := procQueryDisplayConfig.Call(
		uintptr(flags),
		uintptr(unsafe.Pointer(&nPath)),
		uintptr(unsafe.Pointer(&paths[0])),
		uintptr(unsafe.Pointer(&nMode)),
		uintptr(unsafe.Pointer(&modes[0])),
		0,
	); r != 0 {
		return nil, false
	}
	out := make([]pathSummary, 0, nPath)
	for i := uint32(0); i < nPath; i++ {
		p := paths[i]
		// 只看活动路径：未激活的 path 是“这块屏可以接”的候选拓扑，
		// 把它们算进来会把闲置的 HDMI 口当成投屏。
		if p.flags&displayConfigPathActive == 0 {
			continue
		}
		modeIdx := p.sourceInfo.modeInfoIdx
		cloneGroup := uint32(invalidCloneGroup)
		if p.flags&displayConfigPathSupportVirtualMode != 0 {
			// 虚拟模式下是位域：低 16 位克隆组，高 16 位 source mode 下标。
			modeIdx = p.sourceInfo.modeInfoIdx >> 16
			cloneGroup = p.sourceInfo.modeInfoIdx & 0xFFFF
		}
		if p.sourceInfo.modeInfoIdx == 0xFFFFFFFF {
			modeIdx = invalidModeIdx
		}
		out = append(out, pathSummary{
			modeIdx:    modeIdx,
			cloneGroup: cloneGroup,
			tech:       p.targetInfo.outputTechnology,
		})
	}
	return out, true
}

// GetCastStatus 查询当前是否正在投屏。永远返回结果，不抛错。
func GetCastStatus() CastStatus {
	paths, ok := queryActivePaths()
	return castStatusOf(paths, ok)
}
