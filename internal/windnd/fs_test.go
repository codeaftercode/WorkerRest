package windnd

import (
	"testing"
)

// fullscreenOf 的判据矩阵。这套条件对齐 Chromium 的 ui/base/fullscreen_win.cc，
// 改动任何一条都要在这里补一条“为什么不算全屏”的反例。
func TestFullscreenOfMatrix(t *testing.T) {
	mon := FSRect{Left: 0, Top: 0, Right: 1920, Bottom: 1080}
	full := FSRect{Left: 0, Top: 0, Right: 1920, Bottom: 1080}

	cases := []struct {
		name string
		in   ForegroundInfo
		want bool
	}{
		{
			"无边框窗口铺满整屏 = 全屏",
			ForegroundInfo{Supported: true, Rect: full, Monitor: mon},
			true,
		},
		{
			// 任务栏占着底部 48px：最大化窗口只覆盖 rcWork，不是全屏
			"只覆盖工作区不算全屏",
			ForegroundInfo{Supported: true, Rect: FSRect{0, 0, 1920, 1032}, Monitor: mon},
			false,
		},
		{
			"容差内（差 2px）仍算全屏",
			ForegroundInfo{Supported: true, Rect: FSRect{0, 0, 1922, 1082}, Monitor: mon},
			true,
		},
		{
			"超出容差（差 3px）不算全屏",
			ForegroundInfo{Supported: true, Rect: FSRect{0, 0, 1923, 1083}, Monitor: mon},
			false,
		},
		{
			// 副屏：monitor 原点不是 0，验证判据不限定主屏（Chromium 只认主屏）
			"副屏铺满也算全屏",
			ForegroundInfo{
				Supported: true,
				Rect:      FSRect{1920, 0, 3840, 1080},
				Monitor:   FSRect{1920, 0, 3840, 1080},
			},
			true,
		},
		{
			"已最大化（IsZoomed）不算全屏",
			ForegroundInfo{Supported: true, Rect: full, Monitor: mon, Zoomed: true},
			false,
		},
		{
			"带 WS_THICKFRAME 不算全屏",
			ForegroundInfo{Supported: true, Rect: full, Monitor: mon, Style: fsStyleThickFrame},
			false,
		},
		{
			"带标题栏不算全屏",
			ForegroundInfo{Supported: true, Rect: full, Monitor: mon, Style: fsStyleCaption},
			false,
		},
		{
			"带 WS_EX_TOOLWINDOW 不算全屏",
			ForegroundInfo{Supported: true, Rect: full, Monitor: mon, ExStyle: fsExToolWindow},
			false,
		},
		{
			"带立体边框不算全屏",
			ForegroundInfo{Supported: true, Rect: full, Monitor: mon, ExStyle: fsExWindowEdge | fsExClientEdge},
			false,
		},
		{
			"自家窗口不算全屏",
			ForegroundInfo{Supported: true, Rect: full, Monitor: mon, IsSelf: true},
			false,
		},
		{
			// 实测：全屏应用关掉、桌面重新成为前台时，GetForegroundWindow 会短暂返回
			// dwm 的合成表面 —— 它铺满整屏、无边框，几何上完全像个全屏窗。若不排除，
			// 询问窗会弹出一句「dwm 正在全屏运行」。
			"dwm 的桌面合成表面不算全屏",
			ForegroundInfo{Supported: true, Rect: full, Monitor: mon, Exe: "dwm.exe"},
			false,
		},
		{
			"资源管理器铺满整屏也不算全屏（宿主进程）",
			ForegroundInfo{Supported: true, Rect: full, Monitor: mon, Exe: "explorer.exe"},
			false,
		},
		{
			"普通播放器铺满整屏仍是全屏（宿主排除不能误伤）",
			ForegroundInfo{Supported: true, Rect: full, Monitor: mon, Exe: "vlc.exe"},
			true,
		},
		{
			"桌面 / 任务栏不算全屏",
			ForegroundInfo{Supported: true, Rect: full, Monitor: mon, IsShell: true},
			false,
		},
		{
			"被 cloak 的窗口不算全屏",
			ForegroundInfo{Supported: true, Rect: full, Monitor: mon, Cloaked: true},
			false,
		},
		{
			"取数失败不算全屏（没有结论，不能当成命中）",
			ForegroundInfo{Supported: false, Rect: full, Monitor: mon},
			false,
		},
		{
			"空矩形不算全屏",
			ForegroundInfo{Supported: true, Monitor: mon},
			false,
		},
		{
			"没取到显示器不算全屏",
			ForegroundInfo{Supported: true, Rect: full},
			false,
		},
	}

	for _, c := range cases {
		if got := fullscreenOf(c.in); got != c.want {
			t.Fatalf("%s：期望 %v，得到 %v（%+v）", c.name, c.want, got, c.in)
		}
		if got := c.in.IsFullscreen(); got != c.want {
			t.Fatalf("%s：IsFullscreen 应与 fullscreenOf 一致", c.name)
		}
	}
}

// 宿主进程不可标记：把 applicationframehost / rundll32 写进名单等于放行一大片
// 不相干的功能（UWP 全家、所有用 rundll32 加载的旧式组件）。
func TestCanMarkRejectsHostProcesses(t *testing.T) {
	for _, exe := range []string{
		"applicationframehost.exe", "rundll32.exe", "dllhost.exe",
		"svchost.exe", "explorer.exe", "dwm.exe", "ApplicationFrameHost.EXE",
	} {
		f := ForegroundInfo{Supported: true, Exe: exe}
		if f.CanMark() {
			t.Fatalf("%s 是宿主进程，不该允许标记", exe)
		}
		if !IsHostProcess(exe) {
			t.Fatalf("%s 应被判为宿主进程", exe)
		}
	}
	if !IsHostProcess("  RUNDLL32.exe ") {
		t.Fatal("比较前必须先去掉空白并转小写")
	}
	if (ForegroundInfo{Exe: "microsoft.photos.exe"}).CanMark() != true {
		t.Fatal("普通应用应该可以标记")
	}
	if (ForegroundInfo{Supported: true}).CanMark() {
		t.Fatal("取不到 exe 时不可标记")
	}
}

// 询问窗要显示“哪个应用”，任何情况下都不能是空串
func TestDisplayNameNeverEmpty(t *testing.T) {
	if got := (ForegroundInfo{Exe: "vlc.exe"}).DisplayName(); got != "vlc" {
		t.Fatalf("优先用 exe 去掉后缀，得到 %q", got)
	}
	if got := (ForegroundInfo{Title: "  某视频  "}).DisplayName(); got != "某视频" {
		t.Fatalf("没有 exe 时用标题，得到 %q", got)
	}
	if got := (ForegroundInfo{}).DisplayName(); got != "未知应用" {
		t.Fatalf("都没有时给兜底文案，得到 %q", got)
	}
}
