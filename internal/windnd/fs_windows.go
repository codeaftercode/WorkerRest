//go:build windows

package windnd

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"unsafe"

	"workerrest/internal/winproc"
)

// 前台窗口几何判定的真机取数（判据本身在 fs.go，可单测）。
//
// 只有“非独占全屏（QUNS=2）这一档选了询问”时才会走到这里，每 3 秒一拍，
// 一次快照约 0.1~0.5ms（Toolhelp 遍历进程表），对续航无可测量影响。

var (
	modUser32Fs   = syscall.NewLazyDLL("user32.dll")
	modDwmapiFs   = syscall.NewLazyDLL("dwmapi.dll")
	modKernel32Fs = syscall.NewLazyDLL("kernel32.dll")

	procGetForegroundWindow          = modUser32Fs.NewProc("GetForegroundWindow")
	procGetWindowThreadProcessId     = modUser32Fs.NewProc("GetWindowThreadProcessId")
	procGetWindowRect                = modUser32Fs.NewProc("GetWindowRect")
	procGetWindowLongW               = modUser32Fs.NewProc("GetWindowLongW")
	procIsZoomed                     = modUser32Fs.NewProc("IsZoomed")
	procGetClassNameW                = modUser32Fs.NewProc("GetClassNameW")
	procGetWindowTextW               = modUser32Fs.NewProc("GetWindowTextW")
	procMonitorFromRect              = modUser32Fs.NewProc("MonitorFromRect")
	procGetMonitorInfoW              = modUser32Fs.NewProc("GetMonitorInfoW")
	procFindWindowExW                = modUser32Fs.NewProc("FindWindowExW")
	procGetShellWindow               = modUser32Fs.NewProc("GetShellWindow")
	procGetDesktopWindow             = modUser32Fs.NewProc("GetDesktopWindow")
	procSetThreadDpiAwarenessContext = modUser32Fs.NewProc("SetThreadDpiAwarenessContext")

	procDwmGetWindowAttribute = modDwmapiFs.NewProc("DwmGetWindowAttribute")

	procOpenProcess               = modKernel32Fs.NewProc("OpenProcess")
	procCloseHandle               = modKernel32Fs.NewProc("CloseHandle")
	procGetApplicationUserModelId = modKernel32Fs.NewProc("GetApplicationUserModelId")
)

const (
	// GWL_STYLE(-16) / GWL_EXSTYLE(-20)，按 uintptr 传参时取反写法避免符号问题
	gwlStyle   uintptr = ^uintptr(15)
	gwlExStyle uintptr = ^uintptr(19)

	monitorDefaultToNull     = 0x00000000 // 矩形不与任何显示器相交时返回 NULL
	dwmwaExtendedFrameBounds = 9          // 不含不可见的 resize 边框，比 GetWindowRect 干净
	dwmwaCloaked             = 14         // 非 0 表示窗口被 cloaked（虚拟桌面切换 / 后台挂起）

	processQueryLimitedInformation = 0x1000

	// coreWindowClass UWP 应用的真实窗口类名；前台窗口常是 ApplicationFrameHost 的壳
	coreWindowClass = "Windows.UI.Core.CoreWindow"
)

// fsWinRect win32 RECT（与 FSRect 字段一一对应）
type fsWinRect struct {
	Left   int32
	Top    int32
	Right  int32
	Bottom int32
}

// fsMonitorInfo MONITORINFO（40 字节，布局错 API 只会返回 FALSE）
type fsMonitorInfo struct {
	CbSize    uint32
	RcMonitor fsWinRect
	RcWork    fsWinRect
	DwFlags   uint32
}

// enterPerMonitorDpi 把当前线程切到 PER_MONITOR_AWARE_V2，返回还原函数。
//
// 不做这一步会踩最常见的坑：GetWindowRect 返回按当前 DPI 虚拟化后的坐标，而
// GetMonitorInfo 返回物理像素，两者相减在 125%/150% 缩放下永远对不上 —— 表现为
// “在我机器上好好的”，到别人机器上全屏判不出来。
func enterPerMonitorDpi() func() {
	if err := procSetThreadDpiAwarenessContext.Find(); err != nil {
		return func() {} // Win10 1607 之前没有这个导出，退回进程默认值
	}
	prev, _, _ := procSetThreadDpiAwarenessContext.Call(^uintptr(3)) // -4 = PER_MONITOR_AWARE_V2
	if prev == 0 {
		return func() {}
	}
	return func() { procSetThreadDpiAwarenessContext.Call(prev) }
}

// ForegroundSnapshot 取一次前台窗口快照。
//
// Supported 的含义要分清：**调用成功**就算 true，哪怕没有前台窗口（此时 HWND=0、
// 矩形为空，判据自然得出“不是全屏”）。只有取数过程本身出错才返回 false，
// 让调用方按“没有结论”处理。
func ForegroundSnapshot() ForegroundInfo {
	restore := enterPerMonitorDpi()
	defer restore()

	hwnd, _, _ := procGetForegroundWindow.Call()
	if hwnd == 0 {
		return ForegroundInfo{Supported: true, Detail: "没有前台窗口"}
	}

	var pid uint32
	procGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	info := ForegroundInfo{Supported: true, HWND: hwnd, PID: pid}

	var rect fsWinRect
	if hr, _, _ := procDwmGetWindowAttribute.Call(hwnd, dwmwaExtendedFrameBounds,
		uintptr(unsafe.Pointer(&rect)), unsafe.Sizeof(rect)); hr != 0 {
		// 非 DWM 合成的窗口（旧式全屏、某些独占场景）只能退回 GetWindowRect
		procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&rect)))
	}
	info.Rect = FSRect{Left: rect.Left, Top: rect.Top, Right: rect.Right, Bottom: rect.Bottom}

	if hmon, _, _ := procMonitorFromRect.Call(uintptr(unsafe.Pointer(&rect)), monitorDefaultToNull); hmon != 0 {
		mi := fsMonitorInfo{CbSize: uint32(unsafe.Sizeof(fsMonitorInfo{}))}
		if r, _, _ := procGetMonitorInfoW.Call(hmon, uintptr(unsafe.Pointer(&mi))); r != 0 {
			info.Monitor = FSRect{
				Left: mi.RcMonitor.Left, Top: mi.RcMonitor.Top,
				Right: mi.RcMonitor.Right, Bottom: mi.RcMonitor.Bottom,
			}
		}
	}

	style, _, _ := procGetWindowLongW.Call(hwnd, gwlStyle)
	info.Style = uint32(int32(style))
	exStyle, _, _ := procGetWindowLongW.Call(hwnd, gwlExStyle)
	info.ExStyle = uint32(int32(exStyle))
	if z, _, _ := procIsZoomed.Call(hwnd); z != 0 {
		info.Zoomed = true
	}

	var cloaked uint32
	if hr, _, _ := procDwmGetWindowAttribute.Call(hwnd, dwmwaCloaked,
		uintptr(unsafe.Pointer(&cloaked)), unsafe.Sizeof(cloaked)); hr == 0 && cloaked != 0 {
		info.Cloaked = true
	}

	var buf [256]uint16
	if n, _, _ := procGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf))); n > 0 {
		info.Class = syscall.UTF16ToString(buf[:int(n)])
	}
	if n, _, _ := procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf))); n > 0 {
		info.Title = syscall.UTF16ToString(buf[:int(n)])
	}

	info.IsSelf = pid != 0 && pid == uint32(os.Getpid())
	if shell, _, _ := procGetShellWindow.Call(); shell != 0 && hwnd == shell {
		info.IsShell = true
	}
	if desk, _, _ := procGetDesktopWindow.Call(); desk != 0 && hwnd == desk {
		info.IsShell = true
	}
	switch strings.ToLower(info.Class) {
	case "progman", "workerw", "shell_traywnd", "shell_secondarytraywnd":
		info.IsShell = true
	}

	resolveForegroundExe(&info)
	info.Detail = fmt.Sprintf("hwnd=0x%X class=%q exe=%q from=%s rect=%d,%d,%d,%d monitor=%d,%d,%d,%d zoomed=%v cloaked=%v self=%v",
		hwnd, info.Class, info.Exe, info.ExeFrom,
		info.Rect.Left, info.Rect.Top, info.Rect.Right, info.Rect.Bottom,
		info.Monitor.Left, info.Monitor.Top, info.Monitor.Right, info.Monitor.Bottom,
		info.Zoomed, info.Cloaked, info.IsSelf)
	return info
}

// resolveForegroundExe 填 info.Exe / ExeFrom / AUMID。
//
// UWP 的三层包装（这也是“照片、电影和电视能不能认出来”的答案）：
//   - 前台窗口属于 ApplicationFrameHost.exe → 真身是它的子窗 Windows.UI.Core.CoreWindow，
//     取子窗的 PID 再查名字；
//   - 旧版照片查看器由 rundll32 承载内置的 PhotoViewer.dll，进程名分辨不出来 →
//     名字留在 rundll32 上，交给 IsHostProcess 拒绝标记；
//   - 两者都失败时取 AUMID（形如 Microsoft.Windows.Photos_8wekyb3d8bbwe!App）仅用于展示，
//     Exe 留空 = 不可标记，避免用户把一个陌生名字写进名单。
func resolveForegroundExe(info *ForegroundInfo) {
	if info.PID == 0 {
		return
	}
	byPID := winproc.ListProcessNamesByPID()
	name := strings.ToLower(strings.TrimSpace(byPID[info.PID]))
	if name == "" || name == "applicationframehost.exe" {
		if childPID := coreWindowChildPID(info.HWND); childPID != 0 {
			if childName := strings.ToLower(strings.TrimSpace(byPID[childPID])); childName != "" {
				name = childName
				info.ExeFrom = "uwp-child"
			}
		}
	}
	if name != "" && name != "applicationframehost.exe" {
		info.Exe = name
		if info.ExeFrom == "" {
			info.ExeFrom = "toolhelp"
		}
		return
	}
	if aumid := appUserModelID(info.PID); aumid != "" {
		info.AUMID = aumid
		info.ExeFrom = "aumid"
	}
}

// coreWindowChildPID 在壳窗口下找 UWP 真实窗口的 PID；找不到返回 0。
//
// 用 FindWindowExW 而不是 EnumChildWindows + NewCallback：Go 的 syscall.NewCallback
// 分配的回调永不被回收，每 3 秒建一个会在长时间运行后累积。
func coreWindowChildPID(parent uintptr) uint32 {
	cls, err := syscall.UTF16PtrFromString(coreWindowClass)
	if err != nil {
		return 0
	}
	child, _, _ := procFindWindowExW.Call(parent, 0, uintptr(unsafe.Pointer(cls)), 0)
	if child == 0 {
		return 0
	}
	var pid uint32
	procGetWindowThreadProcessId.Call(child, uintptr(unsafe.Pointer(&pid)))
	return pid
}

// appUserModelID 取商店应用的 AUMID；无权限或不是商店应用时返回空串。
func appUserModelID(pid uint32) string {
	h, _, _ := procOpenProcess.Call(processQueryLimitedInformation, 0, uintptr(pid))
	if h == 0 {
		return ""
	}
	defer procCloseHandle.Call(h)
	var buf [512]uint16
	size := uint32(len(buf))
	hr, _, _ := procGetApplicationUserModelId.Call(h,
		uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&buf[0])))
	if hr != 0 {
		return ""
	}
	return syscall.UTF16ToString(buf[:])
}
