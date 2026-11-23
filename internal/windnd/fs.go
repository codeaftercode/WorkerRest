package windnd

import (
	"strings"

	"workerrest/internal/winproc"
)

// 前台窗口几何判定（2026-11-20 新增）。
//
// 为什么不再只信 QUNS：它只给一个全局枚举，既不知道“是哪个应用”，也分不清
// “真全屏”和“最大化”。而产品需要的正是“哪个应用在全屏”——只有拿到 exe 名，
// 才能维护“全屏时不暂停”的名单、才能让用户回答“这个应用要不要暂停”。
//
// 判据照抄 Chromium 的 ui/base/fullscreen_win.cc（Windows 上不存在“窗口是否全屏”的
// 官方查询 API，Chromium 这个量级的项目也是自己算的），并按本产品补四条：
//   - 排除自身进程（我们的提醒窗/询问窗都是置顶窗，会被 GetForegroundWindow 返回）；
//   - 不限定主屏（Chromium 只认 MONITORINFOF_PRIMARY，副屏放视频也该认）；
//   - 排除 cloaked 窗口（UWP 切虚拟桌面/后台时“存在但不可见”）；
//   - 矩形用 ±2px 容差，并要求覆盖 rcMonitor 而不是 rcWork。
//
// 本文件是平台中立部分：类型与纯判据。真机取数在 fs_windows.go，桩在 fs_other.go。

// FSRect 物理像素矩形（GetWindowRect / MONITORINFO.rcMonitor 口径）
type FSRect struct {
	Left   int32 `json:"left"`
	Top    int32 `json:"top"`
	Right  int32 `json:"right"`
	Bottom int32 `json:"bottom"`
}

// Width 宽（可能为负，表示无效矩形）
func (r FSRect) Width() int32 { return r.Right - r.Left }

// Height 高（可能为负，表示无效矩形）
func (r FSRect) Height() int32 { return r.Bottom - r.Top }

// Empty 空矩形（未取到）
func (r FSRect) Empty() bool { return r.Right <= r.Left || r.Bottom <= r.Top }

// ForegroundInfo 一次前台窗口快照。Supported=false 表示这次取数失败（没有结论），
// 调用方必须按“不知道”处理，绝不能当成“没有全屏”。
type ForegroundInfo struct {
	Supported bool    `json:"supported"`
	HWND      uintptr `json:"hwnd"`
	PID       uint32  `json:"pid"`
	// Exe 小写 basename（如 microsoft.photos.exe）。解析失败为空 —— 此时**不可标记**
	Exe string `json:"exe"`
	// ExeFrom 名字来源：toolhelp / uwp-child / aumid / ""
	ExeFrom string `json:"exeFrom"`
	// AUMID 商店应用标识（仅展示用，形如 Microsoft.Windows.Photos_8wekyb3d8bbwe!App）
	AUMID   string `json:"aumid"`
	Class   string `json:"class"`
	Title   string `json:"title"`
	Rect    FSRect `json:"rect"`    // 窗口可见边界（优先 DWMWA_EXTENDED_FRAME_BOUNDS）
	Monitor FSRect `json:"monitor"` // 窗口所在显示器 rcMonitor（不是 rcWork）
	Zoomed  bool   `json:"zoomed"`
	Cloaked bool   `json:"cloaked"`
	IsSelf  bool   `json:"isSelf"`
	IsShell bool   `json:"isShell"`
	Style   uint32 `json:"style"`
	ExStyle uint32 `json:"exstyle"`
	Detail  string `json:"detail"`
}

// 窗口样式位（winuser.h）
const (
	fsStyleCaption    = 0x00C00000 // WS_CAPTION = WS_BORDER | WS_DLGFRAME
	fsStyleDlgFrame   = 0x00400000
	fsStyleThickFrame = 0x00040000

	fsExDlgModalFrame = 0x00000001
	fsExToolWindow    = 0x00000080
	fsExWindowEdge    = 0x00000100
	fsExClientEdge    = 0x00000200
	fsExStaticEdge    = 0x00020000
)

// fsTolerancePx 四边容差。GetWindowRect 与 rcMonitor 在 100%~175% 缩放下可能差 1px，
// 用 <= 而不是 ==，否则高 DPI 机器上真全屏会漏判。
const fsTolerancePx = 2

// coversMonitor rect 是否在容差内完整覆盖 monitor。
//
// 必须比 rcMonitor 而不是 rcWork：任务栏占着的那几十像素正是“最大化窗口”和“全屏窗口”
// 的天然分界，比 rcWork 会把所有最大化窗口算成全屏。
func coversMonitor(rect, monitor FSRect, tol int32) bool {
	if monitor.Empty() || rect.Empty() {
		return false
	}
	return absI32(rect.Left-monitor.Left) <= tol &&
		absI32(rect.Top-monitor.Top) <= tol &&
		absI32(rect.Right-monitor.Right) <= tol &&
		absI32(rect.Bottom-monitor.Bottom) <= tol
}

func absI32(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}

// fullscreenOf 纯判据：这个前台窗口算不算“全屏”。
//
// 顺序即语义，每条都能单独解释：
//  1. 没取到 / 是自家窗 / 是桌面外壳 / 已最大化 / 被 cloak → 不算。最大化≠全屏，
//     任务栏自动隐藏时两者几何上几乎一样，只能靠 IsZoomed 断。
//  2. 是系统宿主进程（dwm / explorer / ApplicationFrameHost …）→ 不算。它们不可能
//     “为用户播放全屏内容”，而几何上又常常很像个全屏窗：全屏应用被关掉、桌面重新成为
//     前台的那几秒，GetForegroundWindow 返回的是 dwm 的合成表面（铺满整屏、无边框），
//     实测会弹出「dwm 正在全屏运行」—— 问一个用户根本不认识、也无法记忆的名字。
//  3. 必须在容差内覆盖整块屏。
//  4. 有标题栏/可调边框的一律不算 —— Chromium 用这一条代替“尺寸容差”来区分
//     “最大化”与“真全屏”（带 WS_THICKFRAME 的窗口 GetWindowRect 会含 7~8px 不可见边框）。
//  5. 有立体边框/工具窗样式的也不算（浮动面板、工具栏）。
func fullscreenOf(f ForegroundInfo) bool {
	switch {
	case !f.Supported, f.IsSelf, f.IsShell, f.Zoomed, f.Cloaked:
		return false
	}
	if IsHostProcess(f.Exe) {
		return false
	}
	if !coversMonitor(f.Rect, f.Monitor, fsTolerancePx) {
		return false
	}
	if f.Style&(fsStyleCaption|fsStyleDlgFrame|fsStyleThickFrame) != 0 {
		return false
	}
	if f.ExStyle&(fsExDlgModalFrame|fsExToolWindow|fsExWindowEdge|fsExClientEdge|fsExStaticEdge) != 0 {
		return false
	}
	return true
}

// IsFullscreen 便捷读法（App 层只关心结论）
func (f ForegroundInfo) IsFullscreen() bool { return fullscreenOf(f) }

// DisplayName 给询问窗显示的“应用名”：优先 exe（去掉 .exe），其次窗口标题，
// 最后退回“未知应用”。绝不返回空串，否则询问窗会出现空标题。
func (f ForegroundInfo) DisplayName() string {
	if name := strings.TrimSuffix(f.Exe, ".exe"); name != "" {
		return name
	}
	if t := strings.TrimSpace(f.Title); t != "" {
		return t
	}
	return "未知应用"
}

// IsHostProcess 这个名字是否属于“不该进名单”的系统宿主进程。
// 名单本身放在 winproc（配置层归一化也要用它拦，见 settings 的 normalizeFullScreenSkip），
// 这里只做转发，避免同一份名单两处维护、迟早不一致。
func IsHostProcess(exe string) bool { return winproc.IsHostProcess(exe) }

// CanMark 这个前台窗口的应用能否写进“全屏时不暂停”名单。
// 取不到 exe（管理员进程、UWP 回退失败）或属于宿主进程时都不能标记 ——
// 让用户标记一个他不认识的名字，比不问更糟。
func (f ForegroundInfo) CanMark() bool {
	return f.Exe != "" && !IsHostProcess(f.Exe)
}
