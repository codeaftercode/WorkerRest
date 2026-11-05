//go:build windows

package windnd

import (
	"testing"
	"unsafe"
)

// TestDisplayPathLayout 钉住三个 Win32 结构体的内存布局。
//
// 这是整条投屏检测里唯一「错了也不报错」的地方：字段顺序或大小写错，QueryDisplayConfig
// 只会返回 ERROR_INVALID_PARAMETER，代码按「API 不可用」降级，功能静默失效而日志里看着
// 一切正常。所以大小必须逐字节对上 wingdi.h：
//
//	DISPLAYCONFIG_PATH_SOURCE_INFO = LUID(8) + id(4) + modeInfoIdx(4) + statusFlags(4) = 20
//	DISPLAYCONFIG_PATH_TARGET_INFO = 8 + 4*5 + 8 + 4*3 = 48
//	DISPLAYCONFIG_PATH_INFO        = 20 + 48 + flags(4) = 72
func TestDisplayPathLayout(t *testing.T) {
	if got := unsafe.Sizeof(displayPathSourceInfo{}); got != 20 {
		t.Fatalf("DISPLAYCONFIG_PATH_SOURCE_INFO 应为 20 字节，得到 %d", got)
	}
	if got := unsafe.Sizeof(displayPathTargetInfo{}); got != 48 {
		t.Fatalf("DISPLAYCONFIG_PATH_TARGET_INFO 应为 48 字节，得到 %d", got)
	}
	if got := unsafe.Sizeof(displayPathInfo{}); got != 72 {
		t.Fatalf("DISPLAYCONFIG_PATH_INFO 应为 72 字节，得到 %d", got)
	}
	// flags 必须紧跟在 targetInfo 之后，中间不能有填充；
	// 偏移 68 而不是 72 说明 targetInfo 少算了一个字段。
	if got := unsafe.Offsetof(displayPathInfo{}.flags); got != 68 {
		t.Fatalf("DISPLAYCONFIG_PATH_INFO.flags 偏移应为 68，得到 %d", got)
	}
}

// displaySourceMode DISPLAYCONFIG_SOURCE_MODE 的头部。只取桌面坐标，
// 用来看「这几条路径画的是同一个画面还是各自独立的桌面」。
type displaySourceMode struct {
	width       uint32
	height      uint32
	pixelFormat uint32
	positionX   int32
	positionY   int32
}

// displayModeInfo DISPLAYCONFIG_MODE_INFO：infoType(4) + id(4) + adapterId(8) + union(48)。
type displayModeInfo struct {
	infoType  uint32
	id        uint32
	adapterID displayLUID
	union     [48]byte
}

// activeSourceOrigins 取每条活动路径所用 source mode 的桌面坐标。
func activeSourceOrigins(flags uint32) ([][2]int32, bool) {
	var nPath, nMode uint32
	if r, _, _ := procGetDisplayConfigBufferSizes.Call(
		uintptr(flags), uintptr(unsafe.Pointer(&nPath)), uintptr(unsafe.Pointer(&nMode)),
	); r != 0 {
		return nil, false
	}
	if nPath == 0 {
		return nil, true
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
		return nil, false
	}
	var out [][2]int32
	for i := uint32(0); i < nPath; i++ {
		p := paths[i]
		if p.flags&displayConfigPathActive == 0 {
			continue
		}
		idx := p.sourceInfo.modeInfoIdx
		if p.flags&displayConfigPathSupportVirtualMode != 0 {
			idx >>= 16
		}
		if idx >= nMode || modes[idx].infoType != 1 {
			continue // 1 = DISPLAYCONFIG_MODE_INFO_TYPE_SOURCE
		}
		sm := (*displaySourceMode)(unsafe.Pointer(&modes[idx].union[0]))
		out = append(out, [2]int32{sm.positionX, sm.positionY})
	}
	return out, true
}

// TestCastMirrorAgreesWithDesktopOrigins 用桌面坐标反证镜像判定。
//
// 这是唯一能发现「结构体字段理解错了」的自动化手段：镜像（复制模式）下所有路径画的是
// 同一个画面，桌面坐标必然全部重合；扩展模式下每块屏的坐标各不相同。两者不可能同时成立，
// 所以坐标与 Mirror 不一致就说明判据（或位域解析）有问题。
//
// 2026-11-03 这条测试抓到过真 bug：旧判据比 sourceInfo.id，双显卡机器上两个适配器的
// source 0 撞车，扩展桌面被判成镜像。当时坐标已经是 (0,0) / (2560,-209) 了。
func TestCastMirrorAgreesWithDesktopOrigins(t *testing.T) {
	origins, ok := activeSourceOrigins(qdcOnlyActivePaths | qdcVirtualModeAware)
	if !ok {
		t.Skip("QueryDisplayConfig 不可用，跳过交叉验证")
	}
	if len(origins) < 2 {
		t.Skipf("本机只有 %d 块活动屏（%v），无法交叉验证镜像判据", len(origins), origins)
	}
	same := true
	for _, o := range origins[1:] {
		if o != origins[0] {
			same = false
		}
	}
	st := GetCastStatus()
	if st.Mirror != same {
		t.Fatalf("镜像判定与桌面坐标矛盾：坐标 %v → %s，但 Mirror=%v（%s）",
			origins, map[bool]string{true: "全部重合=复制模式", false: "各不相同=扩展模式"}[same],
			st.Mirror, st.Detail)
	}
	t.Logf("交叉验证通过：桌面坐标=%v 与 Mirror=%v 一致", origins, st.Mirror)
}

// TestCastProbeShape 真机调一次，只断言「不 panic + 有说明」。
// 具体返回什么取决于这台机器现在有没有接第二块屏，不能写死。
func TestCastProbeShape(t *testing.T) {
	st := GetCastStatus()
	if st.Detail == "" {
		t.Fatal("detail 不该为空（无论支持与否都要有说明）")
	}
	if !st.Supported {
		t.Skipf("本机投屏检测不可用：%s", st.Detail)
	}
	if st.Active && !st.Mirror && !st.Wireless {
		t.Fatalf("判定为投屏时必须至少有一条判据成立（mirror / wireless），得到 %+v", st)
	}
	if !st.Active && (st.Mirror || st.Wireless) {
		t.Fatalf("任一条判据成立时应判定为投屏，得到 %+v", st)
	}
	t.Logf("投屏检测：active=%v mirror=%v wireless=%v（%s）", st.Active, st.Mirror, st.Wireless, st.Detail)
}
