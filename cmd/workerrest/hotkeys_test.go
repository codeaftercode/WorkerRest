package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"workerrest/internal/hotkey"
	"workerrest/internal/settings"
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

// TestPlanHotkeyStates 注册之前就能判定的两种状态：留空 = 用户禁用、无法识别 = invalid。
// 合法组合键必须返回**空 State**（由调用方真去 RegisterHotKey 分 ok / conflict），
// 并给出解析好的键码 —— 这条判据让“检测按钮”不必在单测里真抢用户的全局热键。
func TestPlanHotkeyStates(t *testing.T) {
	both := uintptr(hotkey.ModControl | hotkey.ModAlt)
	cases := []struct {
		key, accel string
		wantState  string
		wantAccel  string
		wantMods   uintptr
		wantVK     uintptr
	}{
		{"postpone", "", hotkeyStateDisabled, "", 0, 0},
		{"postpone", "   ", hotkeyStateDisabled, "", 0, 0},
		{"pause", "P", hotkeyStateInvalid, "P", 0, 0},       // 裸键
		{"pause", "Ctrl", hotkeyStateInvalid, "Ctrl", 0, 0}, // 只有修饰键
		{"pause", "Ctrl+Alt+Foo", hotkeyStateInvalid, "Ctrl+Alt+Foo", 0, 0},
		{"nextBreak", " Ctrl+Alt+B ", "", "Ctrl+Alt+B", both, 'B'}, // 两侧空格只是排版噪声
		{"nextBreak", "ctrl+alt+b", "", "ctrl+alt+b", both, 'B'},   // 手改配置的小写也要认
	}
	for _, c := range cases {
		st, mods, vk := planHotkey(c.key, c.accel)
		if st.Key != c.key {
			t.Errorf("planHotkey(%q,%q) Key=%q", c.key, c.accel, st.Key)
		}
		if st.State != c.wantState {
			t.Errorf("planHotkey(%q,%q) State=%q，期望 %q", c.key, c.accel, st.State, c.wantState)
		}
		if st.Accel != c.wantAccel {
			t.Errorf("planHotkey(%q,%q) Accel=%q，期望 %q", c.key, c.accel, st.Accel, c.wantAccel)
		}
		if st.State == "" && (mods != c.wantMods || vk != c.wantVK) {
			t.Errorf("planHotkey(%q,%q) 键码=(%#x,%#x)，期望 (%#x,%#x)",
				c.key, c.accel, mods, vk, c.wantMods, c.wantVK)
		}
	}
}

// TestHotkeyCheckRoundTrip 跨线程“检测”往返：check() 把请求投给消息循环线程，
// apply 在那边跑完后叫醒它。这条链路涉及两个线程 + 一个 channel，
// 死锁或永远等不到结果（3 秒超时兜底会把它变成一份看不出原因的空结果）只能在这里抓。
//
// 配置全留空：一项都不会真去 RegisterHotKey —— 跑测试抢用户的全局热键是不可接受的副作用。
// 消息循环 goroutine 会一直阻塞到测试进程退出（没有退出通道，产品里也不需要）。
func TestHotkeyCheckRoundTrip(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("全局热键只有 Windows 实现")
	}
	a := &App{}
	a.cfg = settings.New(map[string]any{
		"shortcuts": map[string]any{"postpone": "", "endEarly": "", "pause": "", "nextBreak": ""},
	}, "")
	h := &HotkeyManager{app: a}
	go h.run()
	defer stopHotkeys(t, h)

	deadline := time.Now().Add(2 * time.Second)
	for h.threadID.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("消息循环线程没在 2s 内就绪")
		}
		time.Sleep(5 * time.Millisecond)
	}

	start := time.Now()
	list := h.check()
	if d := time.Since(start); d > hotkeyCheckTimeout {
		t.Fatalf("check() 用了 %s，超过 %s 的超时上限", d, hotkeyCheckTimeout)
	}
	if len(list) != len(hotkeyDefs) {
		t.Fatalf("check() 返回 %d 项，期望 %d 项", len(list), len(hotkeyDefs))
	}
	for _, st := range list {
		if st.State != hotkeyStateDisabled {
			t.Errorf("%q State=%q，期望 disabled（本测试配置全留空）", st.Key, st.State)
		}
	}
	// 快照与 check 必须同一份口径：设置页打开时走 snapshot，点按钮走 check，
	// 两条路渲染同一块 UI，项数或顺序不一致就会出现“点了检测反而少一行”。
	snap := h.snapshot()
	if len(snap) != len(list) {
		t.Fatalf("snapshot %d 项，check %d 项", len(snap), len(list))
	}
	for i := range list {
		if snap[i].Key != list[i].Key || snap[i].State != list[i].State {
			t.Errorf("第 %d 项 snapshot=%+v、check=%+v", i, snap[i], list[i])
		}
	}
}

// TestHotkeyCaptureRoundTrip 录入挂起 / 恢复的线程往返。
//
// 要防的回归是“录入时按下的组合键触发了动作”：那要求挂起期间四项真的被注销，
// 而注销只能在消息循环线程上做（RegisterHotKey/UnregisterHotKey 与注册线程绑定）。
// 这里验三件事：
//  1. HkCaptureMsg 不会被消息循环吞掉或卡住（挂起之后 reload / check 仍能跑完）；
//  2. 挂起期间保存设置触发的重注册不会把四项偷偷注册回去（apply 在挂起时跳过）；
//  3. check() 自己会解除挂起 —— 否则检测会因为 apply 跳过注册而白等 3 秒。
//
// 配置同样全留空：一项都不会真去 RegisterHotKey。
func TestHotkeyCaptureRoundTrip(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("全局热键只有 Windows 实现")
	}
	a := &App{}
	a.cfg = settings.New(map[string]any{
		"shortcuts": map[string]any{"postpone": "", "endEarly": "", "pause": "", "nextBreak": ""},
	}, "")
	h := &HotkeyManager{app: a}
	go h.run()
	defer stopHotkeys(t, h)

	deadline := time.Now().Add(2 * time.Second)
	for h.threadID.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("消息循环线程没在 2s 内就绪")
		}
		time.Sleep(5 * time.Millisecond)
	}

	h.SetCapture(true)
	time.Sleep(30 * time.Millisecond)
	if !h.suspended.Load() {
		t.Fatal("SetCapture(true) 后 suspended 应为 true")
	}

	// 挂起期间“保存设置”会来一条 reload：不能把四项注册回去。
	// 这里能观测到的是线程没卡住 —— 下一步的 check() 必须在超时前返回。
	h.reload()
	time.Sleep(30 * time.Millisecond)

	start := time.Now()
	list := h.check()
	if d := time.Since(start); d > hotkeyCheckTimeout {
		t.Fatalf("挂起后 check() 用了 %s，超过 %s（消息循环可能卡住）", d, hotkeyCheckTimeout)
	}
	if h.suspended.Load() {
		t.Error("check() 应解除录入挂起（否则 apply 跳过注册，检测永远没有结论）")
	}
	if len(list) != len(hotkeyDefs) {
		t.Fatalf("check() 返回 %d 项，期望 %d 项", len(list), len(hotkeyDefs))
	}
	for _, st := range list {
		if st.State != hotkeyStateDisabled {
			t.Errorf("%q State=%q，期望 disabled（本测试配置全留空）", st.Key, st.State)
		}
	}
}

// tryHotkeyFree 试注册一个组合键、成功就立刻注销，返回“当时它是不是空着的”。
//
// 两步必须在**同一条 OS 线程**上：RegisterHotKey / UnregisterHotKey 是线程绑定的
// （见 internal/hotkey/hotkey_windows.go 头注释）。测试 goroutine 会在 OS 线程间漂移，
// 中间漂一次，注销就静默失败 —— 那个组合键会被占到测试进程退出，后面所有用例都跟着红。
func tryHotkeyFree(id int, mods, vk uintptr) bool {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if !hotkey.RegisterHotKey(id, mods, vk) {
		return false
	}
	hotkey.UnregisterHotKey(id)
	return true
}

// stopHotkeys 停掉消息循环，并等它**真的**退出（= 那几组键位已还给系统）。
//
// 为什么不只 shutdown()：WM_QUIT 只是投一条消息，线程还得在自己的栈上跑完 unregisterAll
// 才 return。不等就进下一个用例，键位仍被那条线程占着 —— 这正是“键位预算”被吃光的原因：
// 每个起过消息循环的用例都留下一组永不归还的组合键。
//
// 收尾信号用 threadID：run() 在注销之后才把它清 0（顺序反了就说明“tid 已清但键位没还”，
// 那等于把隐患藏起来）。
func stopHotkeys(t *testing.T, h *HotkeyManager) {
	t.Helper()
	h.shutdown()
	deadline := time.Now().Add(2 * time.Second)
	for h.threadID.Load() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("消息循环线程没在 2s 内退出：全局热键仍占着系统键位")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// TestHotkeyCaptureReleasesRegistration 验证挂起真的把四项从**系统里**注销了，
// 而不是只让 dispatch 忽略按键。
//
// 判据用的是 RegisterHotKey 的全局独占性：同一个组合键被热键线程注册着时，
// 任何其它线程再注册必然失败；挂起后（已注销）再注册就会成功。
// 这比“模拟按一下看有没有触发”可靠得多 —— 不需要合成输入，也不依赖窗口焦点。
//
// 用 Ctrl+Alt+Shift+F24（与真机探针 C 段同一个罕见组合），先拿 F23 探一次环境：
// 无桌面会话 / 被策略拦下时直接 skip，而不是把“测不了”报成“挂起失效”。
func TestHotkeyCaptureReleasesRegistration(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("全局热键只有 Windows 实现")
	}
	const combo = "Ctrl+Alt+Shift+F24"
	mods, vk, ok := hotkey.ParseAccelerator(combo)
	if !ok {
		t.Fatalf("ParseAccelerator(%q) 失败", combo)
	}
	// 探针 id 避开 hotkeyDefs 的 1..4
	const probeID = 99
	envMods, envVK, ok := hotkey.ParseAccelerator("Ctrl+Alt+Shift+F23")
	if !ok {
		t.Fatal(`ParseAccelerator("Ctrl+Alt+Shift+F23") 失败`)
	}
	if !tryHotkeyFree(probeID, envMods, envVK) {
		t.Skip("本环境不允许注册全局热键（无桌面会话 / 被策略拦下），无法用再注册当判据")
	}

	a := &App{}
	a.cfg = settings.New(map[string]any{
		"shortcuts": map[string]any{"postpone": "", "endEarly": "", "pause": "", "nextBreak": combo},
	}, "")
	h := &HotkeyManager{app: a}
	go h.run()
	defer stopHotkeys(t, h)

	// 等第一轮 apply 跑完（applied 由 publish 置位），不能只看 threadID：
	// 线程 id 是 Store 在 apply 之前，那一刻四项还没注册。
	deadline := time.Now().Add(2 * time.Second)
	for h.snapshot() == nil {
		if time.Now().After(deadline) {
			t.Fatal("首轮 apply 没在 2s 内完成")
		}
		time.Sleep(5 * time.Millisecond)
	}

	tryRegister := func() bool {
		return tryHotkeyFree(probeID, mods, vk)
	}

	if tryRegister() {
		t.Fatalf("%s 本应已被热键线程注册占用，却能再注册成功（首轮 apply 没注册上？）", combo)
	}

	h.SetCapture(true)
	time.Sleep(80 * time.Millisecond)
	if !tryRegister() {
		t.Fatalf("挂起后 %s 仍被占用：输入框里按它还是会触发动作", combo)
	}

	h.SetCapture(false)
	time.Sleep(80 * time.Millisecond)
	if tryRegister() {
		t.Fatalf("取消挂起后 %s 没有被重新注册（四项快捷键恢复不了）", combo)
	}

	// 收尾并**证明**键位真的还给了系统。
	// 这条用例自己就是“键位预算被吃光”的元凶之一：线程注册的 F24 曾一直占到进程结束，
	// 后面的用例只能往更低的功能键退，退到没得退就开始互相干扰。
	// defer 已兜底一次，这里再显式跑一遍（幂等）并断言 —— 不然“清理了”这件事没任何判据。
	stopHotkeys(t, h)
	if !tryHotkeyFree(probeID, mods, vk) {
		t.Fatalf("消息循环退出后 %s 仍注册不上：键位没有归还（stopHotkeys 没等到线程真退出？）", combo)
	}
}

// 仍要回形状完整的结果：每项一个、键名与 hotkeyDefs 对齐 —— 前端只写一套渲染，
// 空数组和“四项都不支持”必须是同一种形状，否则设置页会留四个空位。
func TestUnsupportedHotkeyStatuses(t *testing.T) {
	list := unsupportedHotkeyStatuses()
	if len(list) != len(hotkeyDefs) {
		t.Fatalf("unsupported 结果 %d 项，hotkeyDefs %d 项", len(list), len(hotkeyDefs))
	}
	for i, st := range list {
		if st.Key != hotkeyDefs[i].key {
			t.Errorf("第 %d 项 key=%q，期望 %q（顺序应与 hotkeyDefs 一致）", i, st.Key, hotkeyDefs[i].key)
		}
		if st.State != hotkeyStateUnsupported {
			t.Errorf("%q State=%q，期望 %q", st.Key, st.State, hotkeyStateUnsupported)
		}
	}
}

// TestHotkeyFrontendRows 设置页的“行 id → 配置键”映射必须覆盖全部 hotkeyDefs。
//
// 少了哪一项，那个键的注册结果就没有地方显示 —— 用户按了没反应，而页面一言不发，
// 正是这轮要消灭的失败形态。前端表的变量名或形状被改掉时这里会直接失败（block 为空），
// 提醒同步更新。
func TestHotkeyFrontendRows(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("web", "dist", "settings.js"))
	if err != nil {
		t.Fatalf("读 settings.js: %v", err)
	}
	block := regexp.MustCompile(`(?s)var SHORTCUT_KEYS = \{.*?\};`).FindString(string(raw))
	if block == "" {
		t.Fatal("settings.js 里找不到 SHORTCUT_KEYS 表（变量名被改了？注册结果的显示依赖它）")
	}
	for _, d := range hotkeyDefs {
		if !strings.Contains(block, `"`+d.key+`"`) {
			t.Errorf("SHORTCUT_KEYS 缺 %q：该快捷键的注册结果没有对应的行", d.key)
		}
	}
	// 反向：前端表里的键名必须都是 Go 侧认识的（拼错的键名永远匹配不到结果）
	for _, m := range regexp.MustCompile(`"(sc-[A-Za-z]+)"\s*:\s*"([A-Za-z]+)"`).FindAllStringSubmatch(block, -1) {
		row, key := m[1], m[2]
		found := false
		for _, d := range hotkeyDefs {
			if d.key == key {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("SHORTCUT_KEYS 里 %q 映射到未知配置键 %q", row, key)
		}
	}
}

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

// TestSetShortcutNoManager 没有热键能力时的改键语义（server / 非 Windows / 启动失败）。
//
// 没有热键就不存在“被别的程序占用”，所以这里直接落盘；但**格式非法与键名非法
// 一律不落盘** —— 那是唯一还能在这条路径上守住的东西。
func TestSetShortcutNoManager(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	base := settings.DefaultSettingsMap()
	base["shortcuts"] = map[string]any{
		"postpone": "Ctrl+Alt+K", "endEarly": "Ctrl+Alt+X",
		"pause": "Ctrl+Alt+S", "nextBreak": "Ctrl+Alt+B",
	}
	a := &App{} // hotkeys == nil
	a.cfg = settings.New(base, path)

	// 1. 裸键：不写文件
	if res := a.SetShortcut("postpone", "X"); res.OK || res.State != hotkeyStateInvalid {
		t.Errorf("裸键改键 OK=%v State=%q，应为 false/%q", res.OK, res.State, hotkeyStateInvalid)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("非法组合键不该写配置文件（stat err=%v）", err)
	}

	// 2. 不认识的键名：拒绝，且不得把任意键写进配置
	if res := a.SetShortcut("nope", "Ctrl+Alt+J"); res.OK {
		t.Errorf("未知键名被接受了（State=%q）", res.State)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("未知键名不该写配置文件（stat err=%v）", err)
	}

	// 3. 合法组合键：落盘，并回报保留了哪个旧值
	res := a.SetShortcut("postpone", "Ctrl+Alt+J")
	if !res.OK {
		t.Fatalf("合法组合键改键失败：%+v", res)
	}
	if res.Kept != "Ctrl+Alt+K" {
		t.Errorf("Kept=%q，应为改之前的 Ctrl+Alt+K", res.Kept)
	}
	var raw map[string]any
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("配置文件没写出来：%v", err)
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("写出的配置不是合法 JSON：%v", err)
	}
	sc, _ := raw["shortcuts"].(map[string]any)
	if got, _ := sc["postpone"].(string); got != "Ctrl+Alt+J" {
		t.Errorf("落盘后的 postpone=%q，应为 Ctrl+Alt+J", got)
	}

	// 4. 清空 = disabled，也要落盘
	if res := a.SetShortcut("pause", ""); !res.OK || res.State != hotkeyStateDisabled {
		t.Errorf("清空结果 OK=%v State=%q，应为 true/%q", res.OK, res.State, hotkeyStateDisabled)
	}
}

// TestSetShortcutAtomicOnConflict 改键是**原子提交**：注册不上就一个字节都不落盘。
//
// 判据构造方式与挂起测试同一思路：让测试线程先占住一个组合键（其它线程再注册必然失败），
// 于是 SetShortcut 必须被系统判成 conflict。这条测试盯的正是用户抱怨的那件事 ——
// 旧实现会先把坏值写进配置文件、再回头报错并回滚，中间那一刻配置里就是坏的。
func TestSetShortcutAtomicOnConflict(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("全局热键只有 Windows 实现")
	}
	// RegisterHotKey / UnregisterHotKey 是**线程绑定**的：注册在哪条 OS 线程上，就只能由
	// 那条线程注销。测试 goroutine 默认可以在 OS 线程间漂移，一旦 Register 落在 A 线程、
	// Unregister 落在 B 线程，注销会静默失败，组合键被 A 线程占到测试进程退出 ——
	// 后面重试改键就一直报 conflict。这不是假想：实测本测试在这个位置有约 1/5 的偶发
	// 失败（`释放后改键仍失败 … State:conflict`）。所以这一段必须锁线程。
	// 生产侧不受影响：HotkeyManager.run() 第一行就是 runtime.LockOSThread()。
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	envMods, envVK, ok := hotkey.ParseAccelerator("Ctrl+Alt+Shift+F14")
	if !ok {
		t.Fatal(`ParseAccelerator("Ctrl+Alt+Shift+F14") 失败`)
	}
	const envID = 92
	if !hotkey.RegisterHotKey(envID, envMods, envVK) {
		t.Skip("本环境不允许注册全局热键（无桌面会话 / 被策略拦下）")
	}
	hotkey.UnregisterHotKey(envID)

	path := filepath.Join(t.TempDir(), "settings.json")
	base := settings.DefaultSettingsMap()
	base["shortcuts"] = map[string]any{"postpone": "Ctrl+Alt+K", "endEarly": "", "pause": "", "nextBreak": ""}
	a := &App{}
	a.cfg = settings.New(base, path)
	h := &HotkeyManager{app: a}
	a.hotkeys = h
	go h.run()
	// 曾经这里写的是 defer h.unregisterAll() —— 跨线程调用，静默失败，等于没清理：
	// 那条线程注册的 Ctrl+Alt+K 一直占到进程结束。必须让线程自己注销（见 stopHotkeys）。
	defer stopHotkeys(t, h)
	deadline := time.Now().Add(2 * time.Second)
	for h.snapshot() == nil {
		if time.Now().After(deadline) {
			t.Fatal("首轮 apply 没在 2s 内完成")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// 测试线程占住 F16：模拟“别的程序已经用了这个组合键”
	const taken = "Ctrl+Alt+Shift+F16"
	tMods, tVK, ok := hotkey.ParseAccelerator(taken)
	if !ok {
		t.Fatalf("ParseAccelerator(%q) 失败", taken)
	}
	const holdID = 91
	if !hotkey.RegisterHotKey(holdID, tMods, tVK) {
		t.Skipf("%s 在本环境注册不上，无法构造“被占用”场景", taken)
	}

	res := a.SetShortcut("postpone", taken)
	hotkey.UnregisterHotKey(holdID)

	if res.OK {
		t.Fatalf("被占用的组合键竟然改成功了：%+v", res)
	}
	if res.State != hotkeyStateConflict {
		t.Errorf("State=%q，应为 %q", res.State, hotkeyStateConflict)
	}
	if res.Kept != "Ctrl+Alt+K" {
		t.Errorf("Kept=%q，应保留原值 Ctrl+Alt+K", res.Kept)
	}
	// 要害：配置文件根本没被写过 —— 不是“写了又回滚”，是压根没写
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		raw, _ := os.ReadFile(path)
		t.Fatalf("冲突时不该产生配置文件，实际内容：%s", string(raw))
	}

	// 释放后同一个键必须能改成功，并真的落盘
	res = a.SetShortcut("postpone", taken)
	if !res.OK {
		t.Fatalf("释放后改键仍失败：%+v", res)
	}
	var raw map[string]any
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("成功改键后配置文件没写出来：%v", err)
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("写出的配置不是合法 JSON：%v", err)
	}
	sc, _ := raw["shortcuts"].(map[string]any)
	if got, _ := sc["postpone"].(string); got != taken {
		t.Errorf("落盘后的 postpone=%q，应为 %q", got, taken)
	}
}
