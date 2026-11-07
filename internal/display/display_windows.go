//go:build windows

package display

import (
	"strconv"
	"syscall"
	"unsafe"
)

var (
	modUser32 = syscall.NewLazyDLL("user32.dll")

	procGetDisplayConfigBufferSizes = modUser32.NewProc("GetDisplayConfigBufferSizes")
	procQueryDisplayConfig          = modUser32.NewProc("QueryDisplayConfig")
	procDisplayConfigGetDeviceInfo  = modUser32.NewProc("DisplayConfigGetDeviceInfo")
)

const (
	// QDC_ONLY_ACTIVE_PATHS 只取正在驱动屏幕的路径，不用 QDC_ALL_PATHS
	// （后者会把闲置接口算成一块屏）。
	qdcOnlyActivePaths = 0x00000002
	// QDC_VIRTUAL_MODE_AWARE Win10 起支持；失败时回退到不带该标志再试一次。
	qdcVirtualModeAware = 0x00000010

	displayConfigPathActive             = 0x00000001
	displayConfigPathSupportVirtualMode = 0x00000008 // 置位时 modeInfoIdx 是位域

	// displayConfigModeInfoTypeSource 1 = DISPLAYCONFIG_MODE_INFO_TYPE_SOURCE
	displayConfigModeInfoTypeSource = 1
	// displayConfigGetTargetName 2 = DISPLAYCONFIG_DEVICE_INFO_GET_TARGET_NAME
	displayConfigGetTargetName = 2

	// DISPLAYCONFIG_MODE_INFO 固定 64 字节：infoType(4) + id(4) + adapterId(8) + union(48)
	modeInfoSize = 64

	// DISPLAYCONFIG_ROTATION：旋转是**输出端**的属性，源模式本身永远是横的
	rotIdentity = 1
	rot90       = 2
	rot180      = 3
	rot270      = 4
)

// ---- 以下结构体必须与 wingdi.h 逐字节一致，错了 API 只会返回 ERROR_INVALID_PARAMETER ----

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
	outputTechnology uint32
	rotation         uint32
	scaling          uint32
	refreshRate      displayRational
	scanLineOrdering uint32
	targetAvailable  uint32
	statusFlags      uint32
}

type displayPathInfo struct {
	sourceInfo displayPathSourceInfo
	targetInfo displayPathTargetInfo
	flags      uint32
}

type displayModeInfo struct {
	infoType  uint32
	id        uint32
	adapterID displayLUID
	union     [48]byte
}

// 源模式在 union 里：width(4) height(4) pixelFormat(4) position.x(4) position.y(4)
func (m *displayModeInfo) width() int  { return int(*(*uint32)(unsafe.Pointer(&m.union[0]))) }
func (m *displayModeInfo) height() int { return int(*(*uint32)(unsafe.Pointer(&m.union[4]))) }
func (m *displayModeInfo) posX() int   { return int(*(*int32)(unsafe.Pointer(&m.union[12]))) }
func (m *displayModeInfo) posY() int   { return int(*(*int32)(unsafe.Pointer(&m.union[16]))) }

type displayDeviceInfoHeader struct {
	typ       uint32
	size      uint32
	adapterID displayLUID
	id        uint32
}

type displayTargetDeviceName struct {
	header            displayDeviceInfoHeader
	flags             uint32
	outputTechnology  uint32
	edidManufactureID uint16
	edidProductCodeID uint16
	connectorInstance uint32
	friendlyName      [64]uint16
	devicePath        [128]uint16
}

func utf16z(b []uint16) string {
	n := 0
	for n < len(b) && b[n] != 0 {
		n++
	}
	if n == 0 {
		return ""
	}
	return syscall.UTF16ToString(b[:n])
}

// queryRaw 取所有活动显示路径及其模式表。ok=false 表示两种 flags 组合都失败。
func queryRaw() ([]displayPathInfo, []displayModeInfo, bool) {
	for _, flags := range []uint32{qdcOnlyActivePaths | qdcVirtualModeAware, qdcOnlyActivePaths} {
		if paths, modes, ok := queryRawWith(flags); ok {
			return paths, modes, true
		}
	}
	return nil, nil, false
}

func queryRawWith(flags uint32) ([]displayPathInfo, []displayModeInfo, bool) {
	var nPath, nMode uint32
	if r, _, _ := procGetDisplayConfigBufferSizes.Call(
		uintptr(flags),
		uintptr(unsafe.Pointer(&nPath)),
		uintptr(unsafe.Pointer(&nMode)),
	); r != 0 {
		return nil, nil, false
	}
	if nPath == 0 {
		return nil, nil, true
	}
	if nMode == 0 {
		nMode = 1
	}
	paths := make([]displayPathInfo, nPath)
	modes := make([]displayModeInfo, nMode)
	if r, _, _ := procQueryDisplayConfig.Call(
		uintptr(flags),
		uintptr(unsafe.Pointer(&nPath)),
		uintptr(unsafe.Pointer(&paths[0])),
		uintptr(unsafe.Pointer(&nMode)),
		uintptr(unsafe.Pointer(&modes[0])),
		0,
	); r != 0 {
		return nil, nil, false
	}
	return paths[:nPath], modes[:nMode], true
}

// targetNameOf 取一个输出口的 EDID 设备名与接口路径。
func targetNameOf(t displayPathTargetInfo) (name, path string, ok bool) {
	var dn displayTargetDeviceName
	dn.header.typ = displayConfigGetTargetName
	dn.header.size = uint32(unsafe.Sizeof(dn))
	dn.header.adapterID = t.adapterID
	dn.header.id = t.id
	if r, _, _ := procDisplayConfigGetDeviceInfo.Call(uintptr(unsafe.Pointer(&dn))); r != 0 {
		return "", "", false
	}
	return utf16z(dn.friendlyName[:]), utf16z(dn.devicePath[:]), true
}

// List 枚举当前正在使用的显示器，主屏在前。
//
// 复制模式下多个输出口共用同一个 source mode，这里按“桌面矩形”去重 —— 镜像的两块屏
// 内容完全一样，本来也没法只在一块上弹窗，算成一块才是对的。
func List() ([]Info, error) {
	paths, modes, ok := queryRaw()
	if !ok {
		return nil, ErrUnsupported
	}
	out := make([]Info, 0, len(paths))
	seenRect := make(map[[4]int]bool, len(paths))
	for _, p := range paths {
		if p.flags&displayConfigPathActive == 0 {
			continue
		}
		idx := p.sourceInfo.modeInfoIdx
		if p.flags&displayConfigPathSupportVirtualMode != 0 {
			idx >>= 16 // 低 16 位是克隆组，高 16 位才是模式下标
		}
		if idx >= uint32(len(modes)) || modes[idx].infoType != displayConfigModeInfoTypeSource {
			continue
		}
		m := &modes[idx]
		// 桌面矩形的宽高要按输出端的旋转折算：竖屏（旋转 90/270）的源模式仍是 1920×1080，
		// 但它在桌面坐标系里占的是 1080×1920。不折算的话，这块屏的矩形永远和 Wails 的
		// Bounds 对不上（2026-11-04 双屏真机就踩到了：副屏竖屏，四边比对全失配、
		// 只能靠中心点兜底蒙对，而 UI 上还会把分辨率显示成横的）。
		w, h := desktopSize(m.width(), m.height(), p.targetInfo.rotation)
		rect := [4]int{m.posX(), m.posY(), w, h}
		if seenRect[rect] {
			continue
		}
		name, path, ok := targetNameOf(p.targetInfo)
		id := devicePathToID(path)
		if !ok || id == "" {
			// 取不到 EDID 路径（个别虚拟显示驱动）→ 用桌面矩形兜底。
			// 这种标识换布局就会变，但总比“没有 ID 可以存”好。
			id = "pos:" + itoa(rect[0]) + "," + itoa(rect[1])
			name = ""
		}
		seenRect[rect] = true
		out = append(out, Info{
			ID:        id,
			Label:     name,
			X:         rect[0],
			Y:         rect[1],
			Width:     rect[2],
			Height:    rect[3],
			IsPrimary: rect[0] == 0 && rect[1] == 0,
		})
	}
	if len(out) == 0 {
		return nil, ErrUnsupported
	}
	sortInfos(out)
	return out, nil
}

func itoa(n int) string { return strconv.Itoa(n) }

// desktopSize 把源模式尺寸折算成桌面占用尺寸：旋转 90/270 时宽高互换。
//
// 源模式（DISPLAYCONFIG_MODE_INFO_TYPE_SOURCE）描述的是**未旋转**的画面尺寸，
// 旋转由输出端施加，所以竖屏显示器在这里仍是 1920×1080，而它在虚拟桌面里占的是
// 1080×1920。位置 posX/posY 已经是旋转后的桌面坐标，不需要再折算。
func desktopSize(w, h int, rotation uint32) (int, int) {
	if rotation == rot90 || rotation == rot270 {
		return h, w
	}
	return w, h
}
