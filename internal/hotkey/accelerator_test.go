package hotkey

import "testing"

// ParseAccelerator 是用户配置字符串进入 RegisterHotKey 的唯一关卡：解析错了要么热键
// 静默失效，要么注册出一个用户没想要的组合（裸字母做全局热键会把打字全吃掉，必须拒绝）。
func TestParseAcceleratorModifiers(t *testing.T) {
	cases := []struct {
		in   string
		mods uintptr
		vk   uintptr
	}{
		{"Ctrl+Alt+P", ModControl | ModAlt, 'P'},
		{"Alt+Shift+F5", ModAlt | ModShift, 0x74}, // VK_F5 = 0x70+4
		{"Ctrl+Shift+Space", ModControl | ModShift, 0x20},
		{"Super+1", ModWin, '1'},
		// 别名与大小写：录入端只产 Ctrl/Alt/Shift/Super，但手改配置常见这些写法
		{"ctrl+alt+x", ModControl | ModAlt, 'X'},
		{"META+K", ModWin, 'K'},
		{"Win+K", ModWin, 'K'},
		{"CTRL + ALT + Z", ModControl | ModAlt, 'Z'}, // 修饰键段允许空格
		// 三键以上与重复修饰
		{"Ctrl+Alt+Shift+F24", ModControl | ModAlt | ModShift, 0x87},
		{"Ctrl+Ctrl+K", ModControl, 'K'},
	}
	for _, c := range cases {
		mods, vk, ok := ParseAccelerator(c.in)
		if !ok {
			t.Fatalf("%q 应解析成功", c.in)
		}
		if mods != c.mods || vk != c.vk {
			t.Fatalf("%q 得到 mods=%#x vk=%#x，期望 mods=%#x vk=%#x", c.in, mods, vk, c.mods, c.vk)
		}
	}
}

func TestParseAcceleratorNamedKeys(t *testing.T) {
	cases := map[string]uintptr{
		"Ctrl+Enter":       0x0D,
		"Ctrl+Return":      0x0D,
		"Ctrl+Tab":         0x09,
		"Ctrl+Backspace":   0x08,
		"Ctrl+Delete":      0x2E,
		"Ctrl+Del":         0x2E,
		"Ctrl+Insert":      0x2D,
		"Ctrl+Home":        0x24,
		"Ctrl+End":         0x23,
		"Ctrl+PageUp":      0x21,
		"Ctrl+PageDown":    0x22,
		"Ctrl+Up":          0x26,
		"Ctrl+Down":        0x28,
		"Ctrl+Left":        0x25,
		"Ctrl+Right":       0x27,
		"Ctrl+ArrowLeft":   0x25,
		"Ctrl+PrintScreen": 0x2C,
		"Ctrl+Pause":       0x13,
		"Ctrl+ScrollLock":  0x91,
	}
	for in, want := range cases {
		_, vk, ok := ParseAccelerator(in)
		if !ok {
			t.Fatalf("%q 应解析成功", in)
		}
		if vk != want {
			t.Fatalf("%q 得到 vk=%#x，期望 %#x", in, vk, want)
		}
	}
}

func TestParseAcceleratorDigitsAndLetters(t *testing.T) {
	for c := 'A'; c <= 'Z'; c++ {
		_, vk, ok := ParseAccelerator("Ctrl+" + string(c))
		if !ok || vk != uintptr(c) {
			t.Fatalf("Ctrl+%c 得到 vk=%#x ok=%v", c, vk, ok)
		}
	}
	for c := '0'; c <= '9'; c++ {
		_, vk, ok := ParseAccelerator("Ctrl+" + string(c))
		if !ok || vk != uintptr(c) {
			t.Fatalf("Ctrl+%c 得到 vk=%#x ok=%v", c, vk, ok)
		}
	}
}

// 空格键是唯一“尾部空格有意义”的输入：录入端产出 "Ctrl+ "，入口绝不能 Trim。
func TestParseAcceleratorTrailingSpaceIsSpaceKey(t *testing.T) {
	mods, vk, ok := ParseAccelerator("Ctrl+ ")
	if !ok || vk != 0x20 || mods != ModControl {
		t.Fatalf("Ctrl+空格 得到 mods=%#x vk=%#x ok=%v", mods, vk, ok)
	}
	// 只有主键位置保留空格；修饰键段的空格照旧削掉
	if _, vk, ok := ParseAccelerator(" Ctrl +A"); !ok || vk != 'A' {
		t.Fatalf("修饰键段两侧空格应被削掉，得到 vk=%#x ok=%v", vk, ok)
	}
}

func TestParseAcceleratorRejects(t *testing.T) {
	bad := []string{
		"",           // 空
		"A",          // 裸字母：会把正常打字全吃掉
		"F5",         // 裸功能键同样不许
		"Ctrl",       // 只有修饰键
		"Ctrl+",      // 缺主键
		"Ctrl+Alt+",  // 缺主键
		"P+Ctrl",     // 顺序反了：修饰键必须在前面
		"Ctrl+Shift", // 两个修饰键没主键
		"Hyper+K",    // 不认识的修饰键
		"Ctrl+F0",    // F0 不存在
		"Ctrl+F25",   // 超出 F24
		"Ctrl+中",     // 非 ASCII 主键
		"Ctrl+中aa",   // 多字节
		"Ctrl+AB",    // 多字符非命名键
		"Ctrl+Plus",  // 不在白名单里的命名键（+ 号本身也不在白名单）
		"+K",         // 空修饰键段
	}
	for _, s := range bad {
		if mods, vk, ok := ParseAccelerator(s); ok {
			t.Fatalf("%q 应被拒绝，却得到 mods=%#x vk=%#x", s, mods, vk)
		}
	}
}

// F1..F24 边界单独钉：0x70 + n - 1，错一位就是“按 F12 触发 F11”。
func TestParseAcceleratorFunctionKeyRange(t *testing.T) {
	for n := 1; n <= 24; n++ {
		_, vk, ok := ParseAccelerator("Ctrl+F" + itoa(n))
		if !ok {
			t.Fatalf("Ctrl+F%d 应解析成功", n)
		}
		if want := uintptr(0x70 + n - 1); vk != want {
			t.Fatalf("Ctrl+F%d 得到 vk=%#x，期望 %#x", n, vk, want)
		}
	}
}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}
