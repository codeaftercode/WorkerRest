package main

import (
	"log"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"workerrest/internal/settings"
)

// 设置窗口尺寸的应用层策略。出厂数字与归一化（window 块 → WindowSizeConfig）已随
// settings 域下沉到 internal/settings（settings_window.go），这里只保留“按屏幕工作区
// 夹紧 / 应用到窗口 / 记录上次尺寸”这些需要碰 Wails 窗口与 App 状态的部分。

// winSizeSaveDelay 拖拽改尺寸的落盘防抖。拖一次会连发几十个 resize 事件，
// 每个都写一遍 settings.json 既慢又费磁盘；停手 1 秒再写一次就够。
const winSizeSaveDelay = time.Second

// ---------- App 侧：解析 / 应用 / 记录 ----------

// settingsWindowSize 这次该用的窗口尺寸（已按**窗口所在屏**的工作区夹紧）。
//
// 只用 Wails 的屏幕信息：取不到就不夹。**建窗那一刻不能调它**（那时 Wails 还没枚举
// 屏），要用 settingsWindowSizeForCreate。
func (a *App) settingsWindowSize() (int, int) {
	waW, waH, ok := a.workAreaDipForWindow(a.settingsWindowOrNil())
	return a.settingsWindowSizeWith(waW, waH, ok)
}

// settingsWindowSizeForCreate 建窗那一刻用的尺寸。
//
// 设置窗口在 app.Run() 之前创建，而 Wails 要到 Run 里才枚举屏幕
// （newPlatformApp → processAndCacheScreens）—— 此时 Screen.GetPrimary() 是空的。
// 不兜底的话，超屏的记录值（手改配置 / 从大屏换到小屏）会先被 Windows 按“最大追踪
// 尺寸”限一次（实测 2500×1500 → 1575×1055），等 Run 起来再补夹，用户看到窗口缩一下。
// 所以这里补一次原生主屏工作区探测（见 window_size_native_windows.go）。
func (a *App) settingsWindowSizeForCreate() (int, int) {
	waW, waH, ok := a.workAreaDipForWindow(a.settingsWindowOrNil())
	if !ok {
		waW, waH, ok = primaryWorkAreaDipWin32()
	}
	return a.settingsWindowSizeWith(waW, waH, ok)
}

// settingsWindowSizeWith 已知工作区（DIP）时该用的尺寸；ok=false 表示没有屏幕信息。
func (a *App) settingsWindowSizeWith(waW, waH int, ok bool) (int, int) {
	a.mu.Lock()
	cfg := a.cfg.WindowSizeCfg()
	a.mu.Unlock()
	w, h := cfg.Size()
	if !ok {
		return w, h
	}
	if cfg.Remember && validWindowSize(cfg.LastWidth, cfg.LastHeight) {
		// 记录过的尺寸是用户亲手拖出来的窗口大小，上限就是**工作区本身**：任务栏
		// 已经在 WorkArea 里扣掉了，再按默认尺寸那样留 40 DIP 余量，就等于每次重开
		// 都把用户拖出来的窗口缩小一截 —— 远程桌面里会话高度小，这截还挺显眼
		//（工作区 688 时记录 688，重开变 648，用户看到的就是“尺寸记不住”）。
		return fitSizeInWorkAreaExact(w, h, waW, waH, settings.SettingsWindowMinWidth)
	}
	return fitSizeInWorkArea(w, h, waW, waH, settings.SettingsWindowMinWidth)
}

// settingsWindowOrNil 取设置窗口，App 还没建起来（单测 / 极早期）时返回 nil。
// 直接用 settingsWindow() 会在 a.app 为 nil 时解引用崩溃，而尺寸计算恰恰是
// 启动最早期就会走到的一段代码。
func (a *App) settingsWindowOrNil() application.Window {
	if a.app == nil {
		return nil
	}
	return a.settingsWindow()
}

// workAreaDipForWindow 目标屏工作区（DIP）：优先窗口当前所在屏，取不到回退主屏。
//
// 窗口隐藏（收进托盘）时 GetWindowRect 依然带着上次的位置，所以“上次停在哪块屏上”
// 这条信息在隐藏态也是可用的；窗口上次停在已拔掉的副屏上时，最近屏算法会落到主屏，
// 与用户此刻能看到的那块屏一致。
func (a *App) workAreaDipForWindow(w application.Window) (int, int, bool) {
	if a.app == nil || a.app.Screen == nil {
		return 0, 0, false
	}
	if w != nil {
		x, y := w.Position()
		if s := a.app.Screen.ScreenNearestDipPoint(application.Point{X: x, Y: y}); s != nil {
			if waW, waH, ok := screenWorkAreaDip(s); ok {
				return waW, waH, true
			}
		}
	}
	return a.primaryWorkAreaDip()
}

// primaryWorkAreaDip 主屏工作区折算成 DIP（取不到屏信息时 ok=false）。
func (a *App) primaryWorkAreaDip() (int, int, bool) {
	if a.app == nil || a.app.Screen == nil {
		return 0, 0, false
	}
	return screenWorkAreaDip(a.app.Screen.GetPrimary())
}

// screenWorkAreaDip 单块屏的工作区（DIP）。
//
// **别拿 Screen.WorkArea 再除一次 ScaleFactor**：Wails v3 在
// ScreenManager.applyDPIScaling 里已经把 WorkArea 从 PhysicalWorkArea 换算成 DIP 了，
// 窗口的 Size() / Position() 也是 DIP（PhysicalToDipRect）。早先这里按“WorkArea 是
// 物理像素”又除了一遍缩放，200% 屏上 1560×992 的工作区被算成 780×496 —— 用户拖到
// 1100×700 关了重开，被这个假工作区夹成 780×496，看到的就是“每次关掉再打开都小一圈”。
// 用 PhysicalWorkArea 自己折算，口径不依赖 WorkArea 到底是哪一种。
func screenWorkAreaDip(s *application.Screen) (w, h int, ok bool) {
	if s == nil {
		return 0, 0, false
	}
	scale := float64(s.ScaleFactor)
	if scale <= 0 {
		scale = 1
	}
	if pw, ph := s.PhysicalWorkArea.Width, s.PhysicalWorkArea.Height; pw > 0 && ph > 0 {
		return int(float64(pw) / scale), int(float64(ph) / scale), true
	}
	// 物理工作区缺失（极端兜底）：WorkArea 在 Wails v3 里已经是 DIP，直接用。
	if s.WorkArea.Width > 0 && s.WorkArea.Height > 0 {
		return s.WorkArea.Width, s.WorkArea.Height, true
	}
	return 0, 0, false
}

// settingsWindowMinHeight 创建 / 调整窗口时该用的最小高度（只用 Wails 的屏信息）。
//
// 常规就是设计下限 600。但窗口所在屏的工作区**比它还矮**的时候（1366×768 @125% 只剩 574 DIP，
// @150% 只剩 472），600 这个下限反而会把窗口顶到屏幕底下：底部那 26～128px 永远在任务栏
// 后面，连滚动条都拖不到，用户没法把内容看全。这时让下限让位于屏幕 —— 窗口矮一点能滚，
// 窗口比屏幕高是没救的。
func (a *App) settingsWindowMinHeight() int {
	_, waH, ok := a.workAreaDipForWindow(a.settingsWindowOrNil())
	return settingsWindowMinHeightWith(waH, ok)
}

// settingsWindowMinHeightForCreate 建窗那一刻用的最小高度。
//
// 与 settingsWindowSizeForCreate 同理：建窗时 Wails 还没枚举屏，不补原生探测的话，
// 小屏（1366×768 @150%，工作区 472 DIP）会把 MinHeight 定成 600 —— 窗口一比屏幕还高，
// 连 SetSize 都会被系统按 min 顶回去。
func (a *App) settingsWindowMinHeightForCreate() int {
	_, waH, ok := a.workAreaDipForWindow(a.settingsWindowOrNil())
	if !ok {
		_, waH, ok = primaryWorkAreaDipWin32()
	}
	return settingsWindowMinHeightWith(waH, ok)
}

func settingsWindowMinHeightWith(waH int, ok bool) int {
	if !ok || waH >= settings.SettingsWindowMinHeight {
		return settings.SettingsWindowMinHeight
	}
	// 和工作区留 40 DIP 的余量（和夹紧时同一个数），但别压到没法用的高度。
	m := waH - 40
	if m < settingsWindowTinyHeight {
		m = settingsWindowTinyHeight
	}
	if m > waH {
		m = waH
	}
	return m
}

// settingsWindowTinyHeight 极端小屏下的兜底：再小窗口就没法用了。
// 与“上次尺寸”的有效下限是同一个数，改一处即可（见 settings.SettingsWindowMinUsableHeight）。
const settingsWindowTinyHeight = settings.SettingsWindowMinUsableHeight

// fitSizeInWorkArea 把窗口尺寸夹进工作区（全部是 DIP）。纯函数，单测直接验。
//
// 两条下限的处理**不对称**，这是刻意的：
//   - 宽度不低于 minW：再窄字段行就横向裁切，“内容被切掉”比“窗口宽出屏幕”更难用，
//     而且窄到 760 以下的工作区只有 200%+ 缩放的极端配置；
//   - 高度让位给工作区：内容本来就能滚动，窗口比屏幕高才是真的看不全。
//
// 注意 maxH 的兜底不再是 minH —— 那正是“窗口比屏幕还高”的来源。
func fitSizeInWorkArea(width, height, waW, waH, minW int) (int, int) {
	maxW := waW - 16
	maxH := waH - 40
	if maxW < minW {
		maxW = minW
	}
	if maxH < 1 {
		maxH = waH // 工作区矮到连 40 DIP 余量都留不出：不再留边
	}
	if maxH < 1 {
		maxH = 1
	}
	if width > maxW {
		width = maxW
	}
	if height > maxH {
		height = maxH
	}
	return width, height
}

// fitSizeInWorkAreaExact 与 fitSizeInWorkArea 同义，但**不额外留边**：上限就是工作区。
//
// 给“记录过的尺寸”用：那是用户亲手拖出来的窗口大小，任务栏已经算在 WorkArea 之外，
// 再留 40 DIP 余量会让每次重开都比上次小一截。只有真的超出工作区才夹。
func fitSizeInWorkAreaExact(width, height, waW, waH, minW int) (int, int) {
	maxW := waW
	if maxW < minW {
		maxW = minW
	}
	maxH := waH
	if maxH < 1 {
		maxH = 1
	}
	if width > maxW {
		width = maxW
	}
	if height > maxH {
		height = maxH
	}
	return width, height
}

// applySettingsWindowSize 按配置给设置窗口换尺寸（每次 Show 之前调用）。
//
// 只有**尺寸真的不同**才动窗口：勾了“自动记录”且这次记录的尺寸就是当前尺寸时，
// 不该顺手把用户挪过的窗口位置也拽回屏幕中央。
func (a *App) applySettingsWindowSize(w application.Window) {
	if w == nil || w.IsMaximised() || w.IsFullscreen() {
		return
	}
	width, height := a.settingsWindowSize()
	curW, curH := w.Size()
	if curW == width && curH == height {
		return
	}
	// 日志里带上目标屏工作区：远程桌面 / 多屏下“记录 1119 却开成 1024”这类反馈，
	// 只有把工作区一起打出来才能一眼分清是配置没记上还是被屏夹了。
	waW, waH, haveWA := a.workAreaDipForWindow(w)
	// 窗口还没真正创建（启动期 ServiceStartup 就会走到这里，此时 Size() = 0x0）：
	//   · 不能调 SetMinSize —— Wails 的实现是“当前尺寸小于 min 就把 options 尺寸抬到
	//     min”，0x0 必然触发，会把刚算好的宽度改回最小宽度 760；
	//   · SetSize 只写回 options，等窗口创建时按它落地，正是我们想要的。
	created := curW > 0 && curH > 0
	if created {
		// 最小尺寸跟着当前屏走：换到更矮的屏（或用户改了缩放）之后，旧的 MinHeight
		// 会把窗口底部顶到屏幕外，连 SetSize 都会被系统按 min 顶回去。
		w.SetMinSize(settings.SettingsWindowMinWidth, a.settingsWindowMinHeight())
	}
	w.SetSize(width, height)
	// 换了尺寸就回到屏幕中央：否则窗口以左上角为锚点单向长出去，缩小再放大时
	// 很容易一半跑到屏幕外，用户只能先最大化再还原把它捞回来。
	w.Center()
	switch {
	case !created:
		log.Printf("[window] 设置窗口创建尺寸 %dx%d（屏工作区 %dx%d DIP，窗口尚未创建）",
			width, height, waW, waH)
	case haveWA:
		log.Printf("[window] 设置窗口 %dx%d → %dx%d（屏工作区 %dx%d DIP）", curW, curH, width, height, waW, waH)
	default:
		log.Printf("[window] 设置窗口 %dx%d → %dx%d（屏信息不可用）", curW, curH, width, height)
	}
	// 系统有时会按 MinSize / MaxSize 把尺寸顶回去（或 DPI 换算取整），读回来对不上
	// 就记一行：用户反馈“记不住尺寸”时，这条能直接区分“配置没记上”与“系统没接受”。
	if created {
		if aw, ah := w.Size(); aw != width || ah != height {
			log.Printf("[window] 设置窗口尺寸未生效：期望 %dx%d，实际 %dx%d", width, height, aw, ah)
		}
	}
}

// keepWindowLastSize 把 prev 里的“上次尺寸”覆盖到 next 上（SaveSettings 用）。
// lastWidth / lastHeight 只由 Go 写（窗口 resize 时记录），设置页送回来的是打开页面
// 那一刻的旧快照，不许覆盖；prev 没有这两个键时连键一起删，避免残留半对数。
func keepWindowLastSize(prev, next map[string]any) {
	pm, ok := prev["window"].(map[string]any)
	if !ok {
		return
	}
	nm, ok := next["window"].(map[string]any)
	if !ok {
		nm = map[string]any{}
		next["window"] = nm
	}
	for _, k := range []string{"lastWidth", "lastHeight"} {
		if v, has := pm[k]; has {
			nm[k] = v
		} else {
			delete(nm, k)
		}
	}
}

// rememberSettingsWindowSize 记录当前窗口尺寸（防抖后落盘）。
//
// 没勾“自动记录窗口尺寸”时直接返回：那种情况下尺寸永远用默认值，记了也不会被读，
// 白写磁盘。最大化 / 最小化 / 全屏态不记 —— 记下来的是屏幕尺寸而不是用户的窗口尺寸，
// 下次打开会开出一扇“假最大化”的窗。
func (a *App) rememberSettingsWindowSize() {
	a.mu.Lock()
	remember := a.cfg.WindowSizeCfg().Remember
	a.mu.Unlock()
	if !remember {
		return
	}
	w := a.settingsWindow()
	if w == nil || w.IsMaximised() || w.IsMinimised() || w.IsFullscreen() {
		return
	}
	width, height := w.Size()
	if !validWindowSize(width, height) {
		return
	}
	a.winSizeMu.Lock()
	a.winSizePending = [2]int{width, height}
	if a.winSizeTimer == nil {
		a.winSizeTimer = time.AfterFunc(winSizeSaveDelay, a.flushSettingsWindowSize)
	} else {
		a.winSizeTimer.Reset(winSizeSaveDelay)
	}
	a.winSizeMu.Unlock()
}

// flushSettingsWindowSize 把待落盘的尺寸写进配置。防抖到期（独立 goroutine）、
// 隐藏窗口、退出程序三处调用 —— 只认 winSizePending，不再读窗口，
// 因此窗口已经销毁的退出路径也能安全调用。
func (a *App) flushSettingsWindowSize() {
	a.winSizeMu.Lock()
	size := a.winSizePending
	a.winSizePending = [2]int{}
	a.winSizeMu.Unlock()
	if !validWindowSize(size[0], size[1]) {
		return
	}
	a.saveWindowLastSize(size[0], size[1])
}

// validWindowSize 一对尺寸是否可用作“上次尺寸”：宽度 ≥ 最小宽度，高度 ≥ 可用下限。
//
// 高度判据与 internal/settings 的 validWindowSize 保持一致，**不是**设计下限 600：
// 远程桌面 / 小屏上工作区只有 500 出头，用户拖出来的就是那个高度，拿 600 判会把
// 记录当脏数据丢掉（这正是“勾了自动记录却记不住尺寸”的根因之一）。
func validWindowSize(w, h int) bool {
	return w >= settings.SettingsWindowMinWidth && h >= settings.SettingsWindowMinUsableHeight
}

// saveWindowLastSize 落盘“上次尺寸”；值没变就不写（拖拽期间的重复调用靠它挡住）。
//
// 与其它配置写入同一条路：深拷贝一份 → 打补丁 → Save 成功才整体替换 a.cfg，
// 绝不原地改 a.cfg.Raw()（那边可能正被设置页的 GetSettings 读着）。
func (a *App) saveWindowLastSize(width, height int) {
	a.mu.Lock()
	cur := a.cfg.WindowSizeCfg()
	if cur.LastWidth == width && cur.LastHeight == height {
		a.mu.Unlock()
		return
	}
	patch := map[string]any{"window": map[string]any{
		"lastWidth":  float64(width),
		"lastHeight": float64(height),
	}}
	merged, _ := settings.DeepMerge(a.cfg.Raw(), patch).(map[string]any)
	if merged == nil {
		a.mu.Unlock()
		log.Printf("[window] 记录设置窗口尺寸被忽略：配置不是对象")
		return
	}
	s := settings.New(merged, a.cfg.Path())
	if err := s.Save(); err != nil {
		a.mu.Unlock()
		log.Printf("[window] 记录设置窗口尺寸失败: %v", err)
		return
	}
	a.cfg = s
	a.mu.Unlock()
}
