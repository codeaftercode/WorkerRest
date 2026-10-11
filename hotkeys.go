package main

import (
	"log"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"unsafe"
)

// 全局快捷键（对应 上一版 globalShortcut）：推迟 / 提前结束 / 暂停恢复 / 跳到下次休息。
//
// Wails v3 没有全局快捷键能力，直接用 Win32：
//   - user32!RegisterHotKey(NULL, id, mods, vk) 注册**线程级**热键（hwnd=NULL 时
//     WM_HOTKEY 投递到注册线程的消息队列，不需要窗口）；
//   - 专用 goroutine（LockOSThread）跑 GetMessageW 循环，收到 WM_HOTKEY 按 id 分发到
//     调度器动作（与休息窗口 / 托盘 / 设置页同一个汇聚点，相位保护在调度器里）；
//   - 配置变更（SaveSettings / Import / Reset）后 PostThreadMessage 触发线程内
//     注销旧键 + 重注册 —— RegisterHotKey/UnregisterHotKey 必须与注册线程同线程。
//
// 无头 / server 模式不启动：冒烟进程不该真的抢用户的系统热键。
const (
	hkIdPostpone  = 1
	hkIdEndEarly  = 2
	hkIdPause     = 3
	hkIdNextBreak = 4

	wmHotkey    = 0x0312 // WM_HOTKEY：wParam = 注册时的 id
	hkReloadMsg = 0x8001 // WM_APP+1：配置变了，线程内重注册

	modAlt     = 0x0001
	modControl = 0x0002
	modShift   = 0x0004
	modWin     = 0x0008
)

var (
	procRegisterHotKey     = modUser32.NewProc("RegisterHotKey")
	procUnregisterHotKey   = modUser32.NewProc("UnregisterHotKey")
	procGetMessageW        = modUser32.NewProc("GetMessageW")
	procPeekMessageW       = modUser32.NewProc("PeekMessageW")
	procPostThreadMessageW = modUser32.NewProc("PostThreadMessageW")
	procGetCurrentThreadId = modKernel32.NewProc("GetCurrentThreadId")
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

// winMsg 对应 Win32 MSG 结构（x64 布局与系统一致）。
type winMsg struct {
	hwnd    uintptr
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      struct{ x, y int32 }
}

// run 消息循环线程：注册 → 循环取消息 → WM_HOTKEY 分发 / reload 重注册 / WM_QUIT 收尾。
func (h *HotkeyManager) run() {
	runtime.LockOSThread()
	// GetMessage 前先空 Peek 一次：确保线程真的有消息队列，
	// 否则并发的 reload 里 PostThreadMessage 可能因队列未建而丢消息。
	var m winMsg
	procPeekMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0, 0) // PM_NOREMOVE
	tid, _, _ := procGetCurrentThreadId.Call()
	h.threadID.Store(uint32(tid))
	h.apply()
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if r == 0 || r == ^uintptr(0) { // WM_QUIT / 错误
			h.unregisterAll()
			return
		}
		switch m.message {
		case wmHotkey:
			h.dispatch(int(m.wParam))
		case hkReloadMsg:
			h.apply()
		}
	}
}

// reload 通知消息循环线程重读配置（线程未就绪时跳过：run() 里本来就会注册一次）。
func (h *HotkeyManager) reload() {
	if id := h.threadID.Load(); id != 0 {
		procPostThreadMessageW.Call(uintptr(id), hkReloadMsg, 0, 0)
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
		mods, vk, ok := parseAccelerator(s)
		if !ok {
			log.Printf("[hotkey] %s=%q 无法识别，已忽略（需要至少一个修饰键 + 可识别的主键）", d.key, s)
			continue
		}
		r, _, _ := procRegisterHotKey.Call(0, uintptr(d.id), mods, vk)
		if r == 0 {
			log.Printf("[hotkey] %s=%q 注册失败（组合键可能已被其他程序占用）", d.key, s)
		}
	}
}

func (h *HotkeyManager) unregisterAll() {
	for _, d := range hotkeyDefs {
		procUnregisterHotKey.Call(0, uintptr(d.id)) // 未注册的 id 返回 0，无害
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

// ---------- 加速键字符串解析（"Ctrl+Alt+R" → MOD_CONTROL|MOD_ALT + VK_R） ----------

// modNames 修饰键段 → Win32 MOD_*。录入端（settings.js acceleratorFromEvent）只产出
// Ctrl / Alt / Shift / Super，这里放宽接收几个常见别名。
var modNames = map[string]uintptr{
	"CTRL":  modControl,
	"ALT":   modAlt,
	"SHIFT": modShift,
	"SUPER": modWin,
	"META":  modWin,
	"WIN":   modWin,
}

// vkNames 非字符主键（e.key 名称大写）→ 虚拟键码。F1-F24 单独处理。
var vkNames = map[string]uintptr{
	"SPACE": 0x20, "TAB": 0x09, "ENTER": 0x0D, "RETURN": 0x0D, "BACKSPACE": 0x08,
	"DELETE": 0x2E, "DEL": 0x2E, "INSERT": 0x2D, "INS": 0x2D,
	"HOME": 0x24, "END": 0x23, "PAGEUP": 0x21, "PAGEDOWN": 0x22,
	"ARROWUP": 0x26, "ARROWDOWN": 0x28, "ARROWLEFT": 0x25, "ARROWRIGHT": 0x27,
	"UP": 0x26, "DOWN": 0x28, "LEFT": 0x25, "RIGHT": 0x27,
	"PRINTSCREEN": 0x2C, "SCROLLLOCK": 0x91, "PAUSE": 0x13,
}

// parseAccelerator 解析设置页录入的组合键字符串。
// 格式与 acceleratorFromEvent 产出一致：修饰键（Ctrl/Alt/Shift/Super）+ 主键，
// 主键支持 A-Z / 0-9 / F1-F24 / 常见命名键 / 空格。必须至少有一个修饰键
// （裸字母做全局热键会把正常打字全吃掉，直接拒绝）。
func parseAccelerator(s string) (mods, vk uintptr, ok bool) {
	// 入口不 TrimSpace：空格键录入出来就是 "Ctrl+ "（尾部空格是主键），Trim 会把它吃掉。
	parts := strings.Split(s, "+")
	if len(parts) < 2 {
		return 0, 0, false
	}
	for _, p := range parts[:len(parts)-1] {
		m, found := modNames[strings.ToUpper(strings.TrimSpace(p))]
		if !found {
			return 0, 0, false
		}
		mods |= m
	}
	if mods == 0 {
		return 0, 0, false
	}
	key := parts[len(parts)-1] // 不 Trim：配置里的空格键就是 "Ctrl+ "
	if key == "" {
		return 0, 0, false
	}
	if key == " " {
		return mods, 0x20, true
	}
	if len(key) == 1 {
		c := key[0]
		if (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			return mods, uintptr(c), true // 字母数字的 VK 码 == ASCII
		}
		return 0, 0, false
	}
	key = strings.ToUpper(key)
	if len(key) > 1 && key[0] == 'F' {
		if n, err := strconv.Atoi(key[1:]); err == nil && n >= 1 && n <= 24 {
			return mods, uintptr(0x70 + n - 1), true // VK_F1 = 0x70
		}
	}
	if v, found := vkNames[key]; found {
		return mods, v, true
	}
	return 0, 0, false
}
