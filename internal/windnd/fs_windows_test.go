//go:build windows

package windnd

import (
	"testing"
	"unsafe"
)

// MONITORINFO 必须严格 40 字节：布局错了 GetMonitorInfoW 只会返回 FALSE，
// 表现为“全屏永远判不出来”，而不会报错。RECT 同理必须是 16 字节。
func TestMonitorInfoLayout(t *testing.T) {
	if got := unsafe.Sizeof(fsMonitorInfo{}); got != 40 {
		t.Fatalf("MONITORINFO 应为 40 字节，实际 %d", got)
	}
	if got := unsafe.Sizeof(fsWinRect{}); got != 16 {
		t.Fatalf("RECT 应为 16 字节，实际 %d", got)
	}
}

// 真机冒烟：这一步只验证“取数不崩 + 结论自洽”，不断言具体是不是全屏
// （跑测试时前台可能是终端、IDE 或没有窗口）。
func TestForegroundSnapshotSmoke(t *testing.T) {
	f := ForegroundSnapshot()
	if !f.Supported {
		t.Fatalf("Windows 上快照必须成功，得到 %+v", f)
	}
	if f.HWND != 0 && f.Detail == "" {
		t.Fatal("取到窗口时必须有可读的 Detail，否则日志无法定位误判")
	}
	if f.IsSelf && f.IsFullscreen() {
		t.Fatal("自家窗口绝不能判成全屏")
	}
	if f.IsShell && f.IsFullscreen() {
		t.Fatal("桌面 / 任务栏不能判成全屏")
	}
}
