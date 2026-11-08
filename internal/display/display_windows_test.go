//go:build windows

package display

import (
	"errors"
	"testing"
	"unsafe"
)

// TestDisplayLayout 钉住 Win32 结构体布局。
//
// 与 windnd 的投屏检测同理：布局错了 QueryDisplayConfig 只返回 ERROR_INVALID_PARAMETER，
// 代码按“不支持”降级，功能静默失效而日志看着一切正常。
func TestDisplayLayout(t *testing.T) {
	if got := unsafe.Sizeof(displayPathSourceInfo{}); got != 20 {
		t.Fatalf("DISPLAYCONFIG_PATH_SOURCE_INFO 应为 20 字节，得到 %d", got)
	}
	if got := unsafe.Sizeof(displayPathTargetInfo{}); got != 48 {
		t.Fatalf("DISPLAYCONFIG_PATH_TARGET_INFO 应为 48 字节，得到 %d", got)
	}
	if got := unsafe.Sizeof(displayPathInfo{}); got != 72 {
		t.Fatalf("DISPLAYCONFIG_PATH_INFO 应为 72 字节，得到 %d", got)
	}
	if got := unsafe.Offsetof(displayPathInfo{}.flags); got != 68 {
		t.Fatalf("DISPLAYCONFIG_PATH_INFO.flags 偏移应为 68，得到 %d", got)
	}
	if got := unsafe.Sizeof(displayModeInfo{}); got != 64 {
		t.Fatalf("DISPLAYCONFIG_MODE_INFO 应为 64 字节，得到 %d", got)
	}
	if got := unsafe.Sizeof(displayTargetDeviceName{}); got != 420 {
		t.Fatalf("DISPLAYCONFIG_TARGET_DEVICE_NAME 应为 420 字节，得到 %d", got)
	}
	if got := unsafe.Sizeof(displayDeviceInfoHeader{}); got != 20 {
		t.Fatalf("DISPLAYCONFIG_DEVICE_INFO_HEADER 应为 20 字节，得到 %d", got)
	}
}

// TestDesktopSizeAppliesRotation 竖屏（旋转 90/270）的源模式是横的，桌面占用是竖的。
//
// 不折算的后果实测过：副屏竖屏时报 1920×1080，与 Wails 的 Bounds（1080×1920）
// 四边全失配，窗口落点只能靠“中心点落在谁的矩形里”兜底，UI 上的分辨率也是错的。
func TestDesktopSizeAppliesRotation(t *testing.T) {
	cases := []struct {
		w, h         int
		rotation     uint32
		wantW, wantH int
	}{
		{1920, 1080, rotIdentity, 1920, 1080},
		{1920, 1080, rot90, 1080, 1920},
		{1920, 1080, rot180, 1920, 1080},
		{1920, 1080, rot270, 1080, 1920},
		{2560, 1440, rot90, 1440, 2560},
	}
	for _, c := range cases {
		gotW, gotH := desktopSize(c.w, c.h, c.rotation)
		if gotW != c.wantW || gotH != c.wantH {
			t.Fatalf("%dx%d 旋转 %d → 期望 %dx%d，得到 %dx%d",
				c.w, c.h, c.rotation, c.wantW, c.wantH, gotW, gotH)
		}
	}
}

// TestListRealDisplays 真机枚举一次，只断言“数据结构自洽”，不写死屏数
// —— 开发机是双屏，CI 或远程会话可能是零屏，写死就会红。
func TestListRealDisplays(t *testing.T) {
	list, err := List()
	if err != nil {
		if !errors.Is(err, ErrUnsupported) {
			t.Fatalf("List 只该返回 ErrUnsupported，得到 %v", err)
		}
		t.Skipf("本机枚举不可用：%v", err)
	}
	if len(list) == 0 {
		t.Fatal("返回 nil 错误时不该是空列表（空列表应伴随 ErrUnsupported）")
	}
	primary := 0
	seenID := make(map[string]bool, len(list))
	for _, d := range list {
		if d.ID == "" {
			t.Fatalf("每块屏都必须有 ID（要存进配置），得到 %+v", d)
		}
		if seenID[d.ID] {
			t.Fatalf("ID 撞车：%q（同型号双屏必须靠端口实例区分），列表 %+v", d.ID, list)
		}
		seenID[d.ID] = true
		if d.Width <= 0 || d.Height <= 0 {
			t.Fatalf("分辨率必须为正，得到 %+v", d)
		}
		if d.Label == "" {
			t.Fatalf("Label 必须补齐，得到 %+v", d)
		}
		if d.IsPrimary {
			primary++
			if d.X != 0 || d.Y != 0 {
				t.Fatalf("主屏必须在桌面原点，得到 %+v", d)
			}
		}
	}
	if primary != 1 {
		t.Fatalf("有且只有一块主屏（原点），实际 %d 块：%+v", primary, list)
	}
	// 主屏排在最前
	if !list[0].IsPrimary {
		t.Fatalf("主屏应排在列表首位，得到 %+v", list)
	}
	for _, d := range list {
		t.Logf("%s | %s | %dx%d @ (%d,%d)%s", d.ID, d.Label, d.Width, d.Height, d.X, d.Y,
			map[bool]string{true: " · 主屏"}[d.IsPrimary])
	}
}

// DISPLAY_DEVICEW 也必须是 840 字节，否则 EnumDisplayDevices 拿回来的字段全是错位的。
func TestDisplayDeviceLayout(t *testing.T) {
	if got := unsafe.Sizeof(displayDevice{}); got != 840 {
		t.Fatalf("DISPLAY_DEVICEW 应为 840 字节，得到 %d", got)
	}
}

// 通用占位名不能当显示器名字用：远程会话 / 虚拟显示驱动下所有屏都会叫这个名字。
func TestGenericMonitorName(t *testing.T) {
	generic := []string{
		"", "  ", "Default Monitor", "Generic PnP Monitor",
		"Generic Non-PnP Monitor", "默认监视器", "通用即插即用监视器", "Default_Monitor",
	}
	for _, s := range generic {
		if !genericMonitorName(s) {
			t.Fatalf("%q 应判为通用占位名", s)
		}
	}
	real := []string{"DELL U2720Q", "LG 27UL850", "SAMSUNG S27R650", "AOC Q27G2S/D"}
	for _, s := range real {
		if genericMonitorName(s) {
			t.Fatalf("%q 是真实型号名，不该被过滤", s)
		}
	}
}

// 型号名拿不到时补的是“显示器 N”占位名，**不能**是分辨率 —— 分辨率在面板右列单独显示。
func TestListNeverUsesResolutionAsName(t *testing.T) {
	list, err := List()
	if err != nil {
		t.Skipf("本机枚举不可用：%v", err)
	}
	for _, d := range list {
		if want := itoa(d.Width) + "×" + itoa(d.Height); d.Label == want {
			t.Fatalf("名字不能用分辨率冒充（左列名字 / 右列分辨率），得到 %+v", d)
		}
	}
}
