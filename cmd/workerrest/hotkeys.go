package main

import (
	"log"
	"runtime"
	"strings"
	"sync/atomic"
	"workerrest/internal/logx"

	"workerrest/internal/hotkey"
)

// 全局快捷键（对应 上一版 globalShortcut）：推迟 / 提前结束 / 暂停恢复 / 跳到下次休息。
//
// Wails v3 没有全局快捷键能力，直接用 Win32（原语已收进 internal/hotkey）：
//   - user32!RegisterHotKey(NULL, id, mods, vk) 注册**线程级**热键（hwnd=NULL 时
//     WM_HOTKEY 投递到注册线程的消息队列，不需要窗口）；
//   - 专用 goroutine（LockOSThread）跑 GetMessageW 循环，收到 WM_HOTKEY 按 id 分发到
//     调度器动作（与休息窗口 / 托盘 / 设置页同一个汇聚点，相位保护在调度器里）；
//   - 配置变更（SaveSettings / Import / Reset）后 PostThreadMessage 触发线程内
//     注销旧键 + 重注册 —— RegisterHotKey/UnregisterHotKey 必须与注册线程同线程。
//
// 无头 / server 模式不启动：冒烟进程不该真的抢用户的系统热键。
// 非 Windows 平台：internal/hotkey 的 RegisterHotKey 恒返回 false，startHotkeys 里
// 探测失败后不启动（走与“读不到热键能力”相同的降级路径）。

const (
	hkIdPostpone  = 1
	hkIdEndEarly  = 2
	hkIdPause     = 3
	hkIdNextBreak = 4
)

// hotkeyDef 一个全局快捷键：配置键名 + 热键 id + 触发后走哪个调度器动作。
// 键名与设置页 shortcuts.* 一致（settings.json 里透传，Go 配置模型未类型化）。
type hotkeyDef struct {
	id     int
	key    string
	action func(*App)
}

var hotkeyDefs = []hotkeyDef{
	{hkIdPostpone, "postpone", (*App).schedPostpone},
	{hkIdEndEarly, "endEarly", (*App).schedEndBreak},
	{hkIdPause, "pause", (*App).schedPauseToggle},
	{hkIdNextBreak, "nextBreak", (*App).schedBreakNow},
}

// HotkeyManager 全局快捷键。App 持有（a.hotkeys），server 模式为 nil。
type HotkeyManager struct {
	app      *App
	threadID atomic.Uint32 // 消息循环线程；0 = 尚未就绪（reload 会静默跳过）
}

// startHotkeys 启动消息循环线程并按当前配置注册（server / 无头跳过）。
func (a *App) startHotkeys() {
	if serverMode || a.app == nil {
		return
	}
	// 非 Windows / 热键能力不可用时，RegisterHotKey 恒失败，探测后不启动。
	h := &HotkeyManager{app: a}
	a.hotkeys = h
	go h.run()
}

// reloadHotkeys 配置变更后重注册（未启动时是空操作）。
func (a *App) reloadHotkeys() {
	if a.hotkeys != nil {
		a.hotkeys.reload()
	}
}

// run 消息循环线程：注册 → 循环取消息 → WM_HOTKEY 分发 / reload 重注册 / WM_QUIT 收尾。
func (h *HotkeyManager) run() {
	runtime.LockOSThread()
	// GetMessage 前先空 Peek 一次：确保线程真的有消息队列，
	// 否则并发的 reload 里 PostThreadMessage 可能因队列未建而丢消息。
	var m hotkey.WinMsg
	hotkey.PeekMessage(&m)
	h.threadID.Store(hotkey.CurrentThreadID())
	logx.GuardRun("hotkeys.apply", h.apply)
	for {
		if !hotkey.GetMessage(&m) { // WM_QUIT / 错误
			h.unregisterAll()
			return
		}
		switch m.Message {
		case hotkey.WmHotkey:
			logx.GuardRun("hotkeys.dispatch", func() { h.dispatch(int(m.WParam)) })
		case hotkey.HkReloadMsg:
			logx.GuardRun("hotkeys.apply", h.apply)
		}
	}
}

// reload 通知消息循环线程重读配置（线程未就绪时跳过：run() 里本来就会注册一次）。
func (h *HotkeyManager) reload() {
	if id := h.threadID.Load(); id != 0 {
		hotkey.PostThreadMessage(id, hotkey.HkReloadMsg)
	}
}

// apply 注销全部旧键后按当前配置重注册。注册失败（组合键被其他程序占用、
// 两个动作配了同一个键）记日志但不影响其他键。
func (h *HotkeyManager) apply() {
	h.unregisterAll()
	a := h.app
	a.mu.Lock()
	sc, _ := a.cfg.Raw()["shortcuts"].(map[string]any)
	a.mu.Unlock()
	for _, d := range hotkeyDefs {
		s, _ := sc[d.key].(string)
		s = strings.TrimSpace(s)
		if s == "" {
			continue // 留空 = 禁用该项
		}
		mods, vk, ok := hotkey.ParseAccelerator(s)
		if !ok {
			log.Printf("[hotkey] %s=%q 无法识别，已忽略（需要至少一个修饰键 + 可识别的主键）", d.key, s)
			continue
		}
		if !hotkey.RegisterHotKey(d.id, mods, vk) {
			log.Printf("[hotkey] %s=%q 注册失败（组合键可能已被其他程序占用）", d.key, s)
		}
	}
}

func (h *HotkeyManager) unregisterAll() {
	for _, d := range hotkeyDefs {
		hotkey.UnregisterHotKey(d.id) // 未注册的 id 返回 0，无害
	}
}

func (h *HotkeyManager) dispatch(id int) {
	for _, d := range hotkeyDefs {
		if d.id == id {
			d.action(h.app)
			return
		}
	}
}
