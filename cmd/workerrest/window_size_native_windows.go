//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

// 建窗前的原生探测。
//
// Wails 要到 app.Run() 里才枚举屏幕（newPlatformApp → processAndCacheScreens），
// 而设置窗口在 Run 之前就要创建：那一刻 Screen.GetPrimary() 还是空的，夹紧整段被
// 跳过 —— 超屏的记录值（手改配置、或从大屏换到小屏）会先被 Windows 按“最大追踪
// 尺寸”限一次（实测 2500×1500 → 1575×1055），等 Run 起来再补夹，用户看到窗口缩一下。
//
// 这里直接用 Win32 拿主屏工作区，**只为建窗那一刻**给一个合理初值；窗口显示之后的
// 一切尺寸判断仍走 Wails 的 Screen（多屏 / 跨屏 / 缩放变化都以它为准）。

const (
	spiGetWorkArea = 0x0030
	// DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 = (HANDLE)-4
	dpiAwarenessContextPerMonitorV2 = ^uintptr(3)
)

var (
	modUser32                     = syscall.NewLazyDLL("user32.dll")
	procSystemParametersInfoW     = modUser32.NewProc("SystemParametersInfoW")
	procGetDpiForSystem           = modUser32.NewProc("GetDpiForSystem")
	procSetProcessDpiAwarenessCtx = modUser32.NewProc("SetProcessDpiAwarenessContext")
)

type winRect struct {
	Left, Top, Right, Bottom int32
}

// ensurePerMonitorDpiAware 在建窗前把进程 DPI 感知设成 per-monitor v2。
//
// Wails 的 setupDPIAwareness 会先检测“是否已设置”，已设置就直接跳过（不报错），
// 所以这里提前设不会和它打架（manifest 场景同理）。不设的话进程是 unaware：
// GetMonitorInfo 给的是系统 DPI 缩放后的逻辑坐标、GetDpiForSystem 又返回 96，
// 乘除之后工作区会被算错（多屏不同 DPI 时更明显）。
func ensurePerMonitorDpiAware() {
	if procSetProcessDpiAwarenessCtx.Find() != nil {
		return // Win10 1703 之前没有这个 API：保持现状，靠 unaware 坐标兜底
	}
	// 失败（已被 manifest 固定）也无所谓：反正已经是某个感知级别了
	_, _, _ = procSetProcessDpiAwarenessCtx.Call(dpiAwarenessContextPerMonitorV2)
}

// primaryWorkAreaDipWin32 主屏工作区（DIP）。ok=false 表示系统调用失败。
func primaryWorkAreaDipWin32() (w, h int, ok bool) {
	if procSystemParametersInfoW.Find() != nil {
		return 0, 0, false
	}
	var r winRect
	ret, _, _ := procSystemParametersInfoW.Call(spiGetWorkArea, 0, uintptr(unsafe.Pointer(&r)), 0)
	if ret == 0 {
		return 0, 0, false
	}
	pw, ph := int(r.Right-r.Left), int(r.Bottom-r.Top)
	if pw <= 0 || ph <= 0 {
		return 0, 0, false
	}
	dpi := uintptr(96)
	if procGetDpiForSystem.Find() == nil {
		if v, _, _ := procGetDpiForSystem.Call(); v > 0 {
			dpi = v
		}
	}
	scale := float64(dpi) / 96
	return int(float64(pw) / scale), int(float64(ph) / scale), true
}
