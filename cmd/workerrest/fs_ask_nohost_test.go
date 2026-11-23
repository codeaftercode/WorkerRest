package main

import "testing"

// 没有原生宿主时必须干净地返回 false。
//
// 这条判据不是形式主义：调用方按返回值决定要不要挂超时定时器 —— 谎报成功会让那一段
// 询问永远没有结论（用户不答，也等不到“按暂停处理”）。
//
// 为什么建窗成功的那条路径不在这里测：serverMode 是**编译期常量**（mode_server.go），
// 带 -tags server 跑测试时它恒为 true，showFSAskWindow 在第一道闸门就返回了；不带 tag
// 整个包的其他用例会因为缺 application.App 而 panic。真实建窗由 probe_fs_ask.py 在
// 真 GUI 产物上验（ExStyle 五位、420×200 几何、圆角外的透明像素、换段换窗、结案收窗）。
func TestShowFSAskWindowSkipsWithoutNativeHost(t *testing.T) {
	a, _ := newWatchApp(t, shortCountdown)
	if a.app != nil {
		t.Fatal("测试脚手架的 a.app 应为 nil")
	}
	if a.showFSAskWindow("vlc.exe", "影片播放器", true, 0) {
		t.Fatal("没有 application.App 时不该谎报成功")
	}
	a.mu.Lock()
	w := a.wins[fsAskWindowName]
	a.mu.Unlock()
	if w != nil {
		t.Fatal("建不出窗时 a.wins 不该被写脏")
	}
	// 关掉一次也不能变卦（结案回调与退出清理都会无条件调它）
	a.closeFSAskWindow()
	if a.showFSAskWindow("vlc.exe", "影片播放器", true, 0) {
		t.Fatal("close 之后仍不该谎报成功")
	}
}
