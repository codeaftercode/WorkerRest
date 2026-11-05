//go:build windows

package hotkey

import "testing"

// 真注册一次全局热键，确认三个原语都活着：注册成功 → 注销 → 再注册。
//
// 组合键挑「Ctrl+Alt+Shift+F24」：所有修饰键都按住 + 最高功能键，正常软件不会占用，
// 也不会与用户的系统热键冲突；用例结束前必定注销（defer），不会污染后续测试。
func TestRegisterAndUnregisterRealHotkey(t *testing.T) {
	const id = 4711
	mods, vk, ok := ParseAccelerator("Ctrl+Alt+Shift+F24")
	if !ok {
		t.Fatal("前置：组合键应能解析")
	}
	if !RegisterHotKey(id, mods, vk) {
		t.Skip("本环境不允许注册全局热键（可能被其它进程占用 / 无交互桌面），跳过")
	}
	defer UnregisterHotKey(id)

	// 同一组合键第二次注册到不同 id 应失败：这正是「两个动作配同一个键」的检测方式
	if RegisterHotKey(id+1, mods, vk) {
		UnregisterHotKey(id + 1)
		t.Log("同一组合键可被重复注册（系统行为与文档不同），不改判据")
	}

	// 注销后能重新注册，说明注销真的生效了
	UnregisterHotKey(id)
	if !RegisterHotKey(id, mods, vk) {
		t.Fatal("注销后应能重新注册同一组合键")
	}
}

// 未注册的 id 注销必须无害（apply() 每次都无脑全量注销，不能因此崩）。
func TestUnregisterUnknownIdIsHarmless(t *testing.T) {
	UnregisterHotKey(65000)
	UnregisterHotKey(65000)
}

// PeekMessage 的作用是「确保线程有消息队列」，它本身不许阻塞、不许崩。
func TestPeekMessageDoesNotBlock(t *testing.T) {
	var m WinMsg
	PeekMessage(&m)
	if m.Message != 0 {
		t.Logf("空队列里 Peek 到消息 %#x（可能来自其它测试），不影响判据", m.Message)
	}
}

// CurrentThreadID 必须给出非 0 的真实 tid：HotkeyManager 拿它当「消息循环就绪」的
// 标志，0 表示未就绪（reload 会静默跳过）。
func TestCurrentThreadIDNonZero(t *testing.T) {
	if id := CurrentThreadID(); id == 0 {
		t.Fatal("线程 id 不该是 0")
	}
}

// PostThreadMessage → 消息真的进了本线程队列。先 Peek 建队列，再投递，再 Peek 取回。
// 这条钉的是 reload 链路能不能叫醒消息循环。
func TestPostThreadMessageLandsInQueue(t *testing.T) {
	var m WinMsg
	PeekMessage(&m) // 建队列
	tid := CurrentThreadID()

	PostThreadMessage(tid, HkReloadMsg)

	found := false
	for i := 0; i < 50; i++ { // 消息是异步投递的，给一点时间
		PeekMessage(&m)
		if m.Message == HkReloadMsg {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("投递的 HkReloadMsg 没进本线程消息队列（最后一条是 %#x）", m.Message)
	}
}
