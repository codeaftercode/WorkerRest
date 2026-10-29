package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// 设置窗口尺寸策略的回归。
//
// 这层值得钉死的原因：尺寸配错**不会报错也不会崩** —— 窗口照开，只是每次都比上次小一点、
// 或者「自动记录」明明勾着却没生效。没有断言的话只能靠人一次次关窗开窗去看。

// TestDefaultWindowSizeIsMeasured 出厂默认尺寸必须等于实测值，且默认值与最小尺寸自洽。
//
// 913 / 857 的来源见 window_size.go 顶部：913 是用户选定的默认宽度，857 是在该宽度下
// 「关于页恰好不出垂直滚动条」的实测高度（857 = 813 内容 + 38 标题栏 + 6 透明边框；847 就滚）。
// 最小宽度 760 也是实测的：740 宽时 .row.field 横向溢出 2px、750 起归零。
func TestDefaultWindowSizeIsMeasured(t *testing.T) {
	raw, err := os.ReadFile("default_settings.json")
	if err != nil {
		t.Fatalf("读出厂默认失败: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("解析出厂默认失败: %v", err)
	}
	win, ok := m["window"].(map[string]any)
	if !ok {
		t.Fatalf("出厂默认缺少 window 块（设置窗口尺寸没写进默认配置）")
	}
	if got := win["width"]; got != float64(settingsWindowDefaultWidth) {
		t.Fatalf("window.width = %v，期望实测默认宽度 %d", got, settingsWindowDefaultWidth)
	}
	if got := win["height"]; got != float64(settingsWindowDefaultHeight) {
		t.Fatalf("window.height = %v，期望实测默认高度 %d", got, settingsWindowDefaultHeight)
	}
	// 出厂默认「自动记录」= 开：首次启动用默认尺寸，之后沿用上次的
	if got, ok := win["rememberSize"].(bool); !ok || !got {
		t.Fatalf("window.rememberSize = %v，期望 true", win["rememberSize"])
	}
	// 还没记录过上次尺寸 —— 有值就说明默认配置被运行污染了
	if win["lastWidth"] != float64(0) || win["lastHeight"] != float64(0) {
		t.Fatalf("出厂默认的 lastWidth/lastHeight 应为 0，实际 %v/%v", win["lastWidth"], win["lastHeight"])
	}
	// 默认尺寸不得小于最小尺寸（否则首启就是残窗）
	if settingsWindowDefaultWidth < settingsWindowMinWidth || settingsWindowDefaultHeight < settingsWindowMinHeight {
		t.Fatalf("默认尺寸 %dx%d 小于最小尺寸 %dx%d",
			settingsWindowDefaultWidth, settingsWindowDefaultHeight,
			settingsWindowMinWidth, settingsWindowMinHeight)
	}
}

// TestWindowSizePolicy 四种组合：记没记过 × 勾没勾「自动记录」。
func TestWindowSizePolicy(t *testing.T) {
	cases := []struct {
		name     string
		cfg      WindowSizeConfig
		wantW    int
		wantH    int
		scenario string
	}{
		{"未勾选-有上次", WindowSizeConfig{Width: 913, Height: 857, Remember: false, LastWidth: 1024, LastHeight: 800},
			913, 857, "不自动记录：永远用默认尺寸，上次记录的值不参与"},
		{"未勾选-无上次", WindowSizeConfig{Width: 913, Height: 857, Remember: false},
			913, 857, "首次启动：默认尺寸"},
		{"已勾选-有上次", WindowSizeConfig{Width: 913, Height: 857, Remember: true, LastWidth: 1024, LastHeight: 800},
			1024, 800, "自动记录：沿用上次尺寸"},
		{"已勾选-无上次", WindowSizeConfig{Width: 913, Height: 857, Remember: true},
			913, 857, "刚勾上还没拖过窗口：回落默认尺寸"},
		{"已勾选-上次不完整", WindowSizeConfig{Width: 913, Height: 857, Remember: true, LastWidth: 1024},
			913, 857, "只有宽度合法（手改坏 / 旧残留）：整对作废，不要高度 0 的窗"},
		{"已勾选-上次过小", WindowSizeConfig{Width: 913, Height: 857, Remember: true, LastWidth: 300, LastHeight: 200},
			913, 857, "小于最小尺寸的记录值不采信"},
		{"默认值过小", WindowSizeConfig{Width: 300, Height: 200, Remember: false},
			settingsWindowMinWidth, settingsWindowMinHeight, "配置被改成荒唐值：夹进最小尺寸"},
	}
	for _, c := range cases {
		w, h := c.cfg.Size()
		if w != c.wantW || h != c.wantH {
			t.Errorf("%s: Size() = %dx%d，期望 %dx%d（%s）", c.name, w, h, c.wantW, c.wantH, c.scenario)
		}
	}
}

// TestWindowSizeCfgReadsConfig 配置读取：缺 window 块 / 字段类型不对都不能炸，
// 一律回落到实测默认值（设置页首帧直接填表，这里出错就是整页停摆）。
func TestWindowSizeCfgReadsConfig(t *testing.T) {
	// 完全没有 window 块
	cfg := testSettings(t, `{}`).WindowSizeCfg()
	if cfg.Width != settingsWindowDefaultWidth || cfg.Height != settingsWindowDefaultHeight || !cfg.Remember {
		t.Fatalf("缺 window 块时 = %+v，期望默认值且 Remember=true", cfg)
	}

	// 手改坏：字符串宽度、负数尺寸、rememberSize 是字符串
	cfg = testSettings(t, `{"window":{"width":"宽","height":-5,"rememberSize":"yes","lastWidth":0,"lastHeight":0}}`).WindowSizeCfg()
	if cfg.Width != settingsWindowDefaultWidth {
		t.Errorf("width 非数字时应回落默认值，实际 %d", cfg.Width)
	}
	if cfg.Height != settingsWindowMinHeight {
		t.Errorf("height 为负应夹到最小值 %d，实际 %d", settingsWindowMinHeight, cfg.Height)
	}
	if !cfg.Remember {
		t.Errorf("rememberSize 不是布尔时应回落 true")
	}
}

// TestNormalizeWindowClampsAndClears 归一化：写盘前的兜底（normalizeLoad / normalizeForSave 共用）。
func TestNormalizeWindowClampsAndClears(t *testing.T) {
	raw := map[string]any{}
	normalizeWindow(raw)
	win := raw["window"].(map[string]any)
	if win["width"] != float64(settingsWindowDefaultWidth) || win["height"] != float64(settingsWindowDefaultHeight) {
		t.Fatalf("空配置补出来的 window = %v", win)
	}

	raw = map[string]any{"window": map[string]any{
		"width": float64(100), "height": float64(999999),
		"lastWidth": float64(800), "lastHeight": float64(0),
	}}
	normalizeWindow(raw)
	win = raw["window"].(map[string]any)
	if win["width"] != float64(settingsWindowMinWidth) {
		t.Errorf("width=100 应夹到 %d，实际 %v", settingsWindowMinWidth, win["width"])
	}
	if win["height"] != float64(settingsWindowMaxHeight) {
		t.Errorf("height 超上界应夹到 %d，实际 %v", settingsWindowMaxHeight, win["height"])
	}
	if win["lastWidth"] != float64(0) || win["lastHeight"] != float64(0) {
		t.Errorf("只有一边合法的 last 应整对归零，实际 %v/%v", win["lastWidth"], win["lastHeight"])
	}

	// 合法的一对要原样保留（别把用户的记录清掉）
	raw = map[string]any{"window": map[string]any{"lastWidth": float64(900), "lastHeight": float64(700)}}
	normalizeWindow(raw)
	win = raw["window"].(map[string]any)
	if win["lastWidth"] != float64(900) || win["lastHeight"] != float64(700) {
		t.Errorf("合法的 last 被清掉了：%v/%v", win["lastWidth"], win["lastHeight"])
	}
}

// TestKeepWindowLastSizeIgnoresStalePayload 设置页送回来的旧快照不许覆盖刚记录的尺寸。
// 场景：用户拖大窗口（Go 记下 1024×800）→ 又改了别的设置 → 前端送回的仍是 913×857 时的快照。
func TestKeepWindowLastSizeIgnoresStalePayload(t *testing.T) {
	cur := map[string]any{"window": map[string]any{
		"width": float64(913), "height": float64(857), "rememberSize": true,
		"lastWidth": float64(1024), "lastHeight": float64(800),
	}}
	next := map[string]any{"window": map[string]any{
		"width": float64(913), "height": float64(857), "rememberSize": false,
		"lastWidth": float64(0), "lastHeight": float64(0),
	}}
	keepWindowLastSize(cur, next)
	win := next["window"].(map[string]any)
	if win["lastWidth"] != float64(1024) || win["lastHeight"] != float64(800) {
		t.Fatalf("旧快照覆盖了已记录的尺寸: %v/%v", win["lastWidth"], win["lastHeight"])
	}
	// 用户在界面上改的开关必须生效（只保护尺寸，不保护勾选框）
	if win["rememberSize"] != false {
		t.Fatalf("rememberSize 应取前端送来的 false，实际 %v", win["rememberSize"])
	}
}

// TestSaveWindowLastSizePersists 记录尺寸要真的落到 settings.json，并且值没变时不重复写。
func TestSaveWindowLastSizePersists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	s := testSettings(t, `{"window":{"width":913,"height":857,"rememberSize":true,"lastWidth":0,"lastHeight":0}}`)
	s.path = path
	a := &App{cfg: s}

	a.saveWindowLastSize(1024, 800)

	var disk map[string]any
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("settings.json 没写出来: %v", err)
	}
	if err := json.Unmarshal(raw, &disk); err != nil {
		t.Fatalf("写出来的配置解析失败: %v", err)
	}
	win := disk["window"].(map[string]any)
	if win["lastWidth"] != float64(1024) || win["lastHeight"] != float64(800) {
		t.Fatalf("落盘的 lastWidth/lastHeight = %v/%v，期望 1024/800", win["lastWidth"], win["lastHeight"])
	}
	// 默认尺寸不能被「上次尺寸」顶掉（两对数各管一头）
	if win["width"] != float64(913) || win["height"] != float64(857) {
		t.Fatalf("默认尺寸被改动了: %v/%v", win["width"], win["height"])
	}
	// 内存里的配置也要同步：下次 Show 读的就是它
	if w, h := a.cfg.WindowSizeCfg().Size(); w != 1024 || h != 800 {
		t.Fatalf("内存配置未同步，Size() = %dx%d", w, h)
	}

	// 同一个尺寸再记一次：不该再写盘（拖拽停手后会有多次重复触发）
	st1, _ := os.Stat(path)
	a.saveWindowLastSize(1024, 800)
	st2, _ := os.Stat(path)
	if !st1.ModTime().Equal(st2.ModTime()) {
		t.Errorf("尺寸没变却又写了一次盘（拖拽期间会频繁写配置）")
	}
}

// TestFitSizeToScreenNoApp 无窗口环境（单测 / server 模式）下不夹紧，原样返回。
func TestFitSizeToScreenNoApp(t *testing.T) {
	a := &App{}
	w, h := a.fitSizeToScreen(913, 857)
	if w != 913 || h != 857 {
		t.Fatalf("没有屏幕信息时应原样返回，实际 %dx%d", w, h)
	}
}
