package main

import (
	"log"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"workerrest/internal/settings"
)

// 设置窗口尺寸的应用层策略。出厂数字与归一化（window 块 → WindowSizeConfig）已随
// settings 域下沉到 internal/settings（settings_window.go），这里只保留「按屏幕工作区
// 夹紧 / 应用到窗口 / 记录上次尺寸」这些需要碰 Wails 窗口与 App 状态的部分。

// winSizeSaveDelay 拖拽改尺寸的落盘防抖。拖一次会连发几十个 resize 事件，
// 每个都写一遍 settings.json 既慢又费磁盘；停手 1 秒再写一次就够。
const winSizeSaveDelay = time.Second

// ---------- App 侧：解析 / 应用 / 记录 ----------

// settingsWindowSize 这次该用的窗口尺寸（已按屏幕工作区夹紧）。
func (a *App) settingsWindowSize() (int, int) {
	a.mu.Lock()
	w, h := a.cfg.WindowSizeCfg().Size()
	a.mu.Unlock()
	return a.fitSizeToScreen(w, h)
}

// fitSizeToScreen 把尺寸夹进主屏工作区。
//
// 高分屏上 WorkArea 是**物理**像素，而窗口的 Width / Height 是 DIP，所以先按
// ScaleFactor 折回 DIP 再比 —— 不折的话 1.5 倍屏上永远夹不住，默认 857 的高度会顶到
// 任务栏外面去。夹不住主屏的情况（小屏笔记本）宁可矮一点让内容滚动，也不要开出一扇
// 一开机就超出屏幕的窗口。
func (a *App) fitSizeToScreen(width, height int) (int, int) {
	waW, waH, ok := a.primaryWorkAreaDip()
	if !ok {
		return width, height
	}
	return fitSizeInWorkArea(width, height, waW, waH, settings.SettingsWindowMinWidth)
}

// primaryWorkAreaDip 主屏工作区折算成 DIP（取不到屏信息时 ok=false）。
//
// WorkArea 是物理像素、窗口尺寸是 DIP，两者不换算就没法比较：1.5 倍屏上 1040 物理
// 像素的工作区只有 693 DIP，而 857 的默认高度在 DIP 里看是「放得下」的假象。
func (a *App) primaryWorkAreaDip() (w, h int, ok bool) {
	if a.app == nil || a.app.Screen == nil {
		return 0, 0, false
	}
	s := a.app.Screen.GetPrimary()
	if s == nil || s.WorkArea.Width <= 0 || s.WorkArea.Height <= 0 {
		return 0, 0, false
	}
	scale := float64(s.ScaleFactor)
	if scale <= 0 {
		scale = 1
	}
	return int(float64(s.WorkArea.Width) / scale), int(float64(s.WorkArea.Height) / scale), true
}

// settingsWindowMinHeight 创建 / 调整窗口时该用的最小高度。
//
// 常规就是设计下限 600。但主屏工作区**比它还矮**的时候（1366×768 @125% 只剩 574 DIP，
// @150% 只剩 472），600 这个下限反而会把窗口顶到屏幕底下：底部那 26～128px 永远在任务栏
// 后面，连滚动条都拖不到，用户没法把内容看全。这时让下限让位于屏幕 —— 窗口矮一点能滚，
// 窗口比屏幕高是没救的。
func (a *App) settingsWindowMinHeight() int {
	_, waH, ok := a.primaryWorkAreaDip()
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

// settingsWindowTinyHeight 极端小屏（<400 DIP 高）下的兜底：再小窗口就没法用了。
const settingsWindowTinyHeight = 240

// fitSizeInWorkArea 把窗口尺寸夹进工作区（全部是 DIP）。纯函数，单测直接验。
//
// 两条下限的处理**不对称**，这是刻意的：
//   - 宽度不低于 minW：再窄字段行就横向裁切，「内容被切掉」比「窗口宽出屏幕」更难用，
//     而且窄到 760 以下的工作区只有 200%+ 缩放的极端配置；
//   - 高度让位给工作区：内容本来就能滚动，窗口比屏幕高才是真的看不全。
//
// 注意 maxH 的兜底不再是 minH —— 那正是「窗口比屏幕还高」的来源。
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

// applySettingsWindowSize 按配置给设置窗口换尺寸（每次 Show 之前调用）。
//
// 只有**尺寸真的不同**才动窗口：勾了「自动记录」且这次记录的尺寸就是当前尺寸时，
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
	// 最小尺寸跟着当前屏走：换到更矮的屏（或用户改了缩放）之后，旧的 MinHeight
	// 会把窗口底部顶到屏幕外，连 SetSize 都会被系统按 min 顶回去。
	w.SetMinSize(settings.SettingsWindowMinWidth, a.settingsWindowMinHeight())
	w.SetSize(width, height)
	// 换了尺寸就回到屏幕中央：否则窗口以左上角为锚点单向长出去，缩小再放大时
	// 很容易一半跑到屏幕外，用户只能先最大化再还原把它捞回来。
	w.Center()
	log.Printf("[window] 设置窗口 %dx%d → %dx%d", curW, curH, width, height)
}

// keepWindowLastSize 把 prev 里的「上次尺寸」覆盖到 next 上（SaveSettings 用）。
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
// 没勾「自动记录窗口尺寸」时直接返回：那种情况下尺寸永远用默认值，记了也不会被读，
// 白写磁盘。最大化 / 最小化 / 全屏态不记 —— 记下来的是屏幕尺寸而不是用户的窗口尺寸，
// 下次打开会开出一扇「假最大化」的窗。
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

// validWindowSize 一对尺寸是否可用作「上次尺寸」：两边都得 ≥ 最小值。
// 只有一边合法（手改坏 / 旧版本残留）时整对作废 —— 拿一个合法 + 一个 0 去开窗
// 会得到一扇高度 0 的窗口。
func validWindowSize(w, h int) bool {
	return w >= settings.SettingsWindowMinWidth && h >= settings.SettingsWindowMinHeight
}

// saveWindowLastSize 落盘「上次尺寸」；值没变就不写（拖拽期间的重复调用靠它挡住）。
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
