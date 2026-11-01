package main

import (
	"testing"

	"workerrest/internal/hotkey"
)

// TestParseAccelerator 全局快捷键字符串解析（设置页录入格式 Ctrl+Alt+R）。
func TestParseAccelerator(t *testing.T) {
	cases := []struct {
		in   string
		mods uintptr
		vk   uintptr
		ok   bool
	}{
		{"Ctrl+Alt+R", hotkey.ModControl | hotkey.ModAlt, 'R', true},
		{"Ctrl+Shift+F5", hotkey.ModControl | hotkey.ModShift, 0x74, true},
		{"Alt+F4", hotkey.ModAlt, 0x73, true},
		{"Super+1", hotkey.ModWin, '1', true},
		{"Ctrl+X", hotkey.ModControl, 'X', true},
		{"Ctrl+ ", hotkey.ModControl, 0x20, true},       // 空格键（录入端存成 "Ctrl+ "）
		{"Ctrl+ArrowUp", hotkey.ModControl, 0x26, true}, // 方向键
		{"Ctrl+Delete", hotkey.ModControl, 0x2E, true},  // 命名键
		{"Shift+Space", hotkey.ModShift, 0x20, true},    // 命名键（大写形态）
		{"Ctrl+Alt+Shift+F24", hotkey.ModControl | hotkey.ModAlt | hotkey.ModShift, 0x87, true},
		{"X", 0, 0, false},        // 无修饰键：会吃掉正常打字，必须拒绝
		{"Ctrl", 0, 0, false},     // 只有修饰键
		{"Ctrl+", 0, 0, false},    // 主键缺失
		{"Ctrl+Foo", 0, 0, false}, // 未知主键
		{"Ctrl+Esc", 0, 0, false}, // Esc 不该录入（前端用 Esc 清空）；也不识别
		{"Ctrl+Ω", 0, 0, false},   // 非字母数字单字符（OEM 键不支持）
		{"Hyper+R", 0, 0, false},  // 未知修饰键
		{"Ctrl+F25", 0, 0, false}, // F 键越界
		{"Ctrl+F0", 0, 0, false},  // F 键越界
		{"", 0, 0, false},         // 空串（禁用态，apply 直接跳过）
	}
	for _, c := range cases {
		mods, vk, ok := hotkey.ParseAccelerator(c.in)
		if ok != c.ok || (ok && (mods != c.mods || vk != c.vk)) {
			t.Errorf("ParseAccelerator(%q) = (%#x, %#x, %v)，期望 (%#x, %#x, %v)",
				c.in, mods, vk, ok, c.mods, c.vk, c.ok)
		}
	}
}

// TestHotkeyDefsMatchFrontend 配置键名必须与设置页 shortcuts.* 一致
// （settings.js 读写 S.shortcuts.postpone / endEarly / pause / nextBreak）。
func TestHotkeyDefsMatchFrontend(t *testing.T) {
	want := map[string]bool{"postpone": false, "endEarly": false, "pause": false, "nextBreak": false}
	for _, d := range hotkeyDefs {
		if _, found := want[d.key]; !found {
			t.Errorf("hotkeyDefs 键 %q 不在前端 shortcuts 配置里（settings.json 不会带它）", d.key)
		}
		want[d.key] = true
		if d.id <= 0 {
			t.Errorf("hotkeyDefs[%q] id=%d 非正（id 是 RegisterHotKey 与 WM_HOTKEY 的配对键）", d.key, d.id)
		}
	}
	for k, seen := range want {
		if !seen {
			t.Errorf("前端 shortcuts.%s 没有对应的热键定义", k)
		}
	}
}

// TestHotkeyDefsActions 四个热键的动作用方法值 (*App).schedXxx 表达，编译期已保证存在；
// 这里再钉一遍 id 唯一性（id 冲突会导致后注册的失败、WM_HOTKEY 分发错乱）。
func TestHotkeyDefsActions(t *testing.T) {
	seen := map[int]bool{}
	for _, d := range hotkeyDefs {
		if seen[d.id] {
			t.Errorf("hotkeyDefs id=%d 重复", d.id)
		}
		seen[d.id] = true
	}
	if len(hotkeyDefs) != 4 {
		t.Errorf("应有 4 个全局快捷键定义，实际 %d", len(hotkeyDefs))
	}
}
