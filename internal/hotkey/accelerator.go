// Package hotkey 提供全局快捷键的平台抽象。
//
// Windows 上用 user32!RegisterHotKey + GetMessageW 消息循环实现（见 hotkey_windows.go）；
// 其它平台当前是 no-op 桩（见 hotkey_other.go），未来真机移植时 Linux 用 X11 的
// XGrabKey、macOS 用 Carbon 的 RegisterEventHotKey 补实现。
//
// 本包只放「平台相关的最小原语 + 加速键字符串解析」，不依赖 main 包的 App；
// main 包里的 HotkeyManager（消息循环骨架、与调度器动作的映射）调用本包的原语。
package hotkey

import (
	"strconv"
	"strings"
)

// 热键 id（与 WM_HOTKEY 的 wParam 配对）与修饰键位。
const (
	ModAlt     = 0x0001
	ModControl = 0x0002
	ModShift   = 0x0004
	ModWin     = 0x0008

	// WmHotkey WM_HOTKEY：wParam = 注册时的 id
	WmHotkey = 0x0312
	// HkReloadMsg WM_APP+1：配置变了，线程内重注册
	HkReloadMsg = 0x8001
)

// modNames 修饰键段 → Win32 MOD_*。录入端（settings.js acceleratorFromEvent）只产出
// Ctrl / Alt / Shift / Super，这里放宽接收几个常见别名。
var modNames = map[string]uintptr{
	"CTRL":  ModControl,
	"ALT":   ModAlt,
	"SHIFT": ModShift,
	"SUPER": ModWin,
	"META":  ModWin,
	"WIN":   ModWin,
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

// ParseAccelerator 解析设置页录入的组合键字符串。
// 格式与 acceleratorFromEvent 产出一致：修饰键（Ctrl/Alt/Shift/Super）+ 主键，
// 主键支持 A-Z / 0-9 / F1-F24 / 常见命名键 / 空格。必须至少有一个修饰键
// （裸字母做全局热键会把正常打字全吃掉，直接拒绝）。
func ParseAccelerator(s string) (mods, vk uintptr, ok bool) {
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
	// 多字符主键两侧的空格是排版噪声（"Ctrl + Alt + Z" 的最后一段是 " Z"）：削掉。
	// 单字符不削 —— 空格键本身就是一个空格，已在上一步单独处理。
	if len(key) > 1 {
		key = strings.TrimSpace(key)
		if key == "" {
			return 0, 0, false
		}
	}
	if len(key) == 1 {
		c := key[0]
		// 字母统一折成大写再判：修饰键段本来就大小写不敏感（见上面的 ToUpper），
		// 主键不折的话「手改成 ctrl+alt+x」会静默失效 —— 录入端产出大写，
		// 但手改配置的人没有理由知道这条约定。
		if c >= 'a' && c <= 'z' {
			c -= 'a' - 'A'
		}
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
