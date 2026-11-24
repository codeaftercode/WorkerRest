package main

import (
	"testing"

	"github.com/wailsapp/wails/v3/pkg/application"

	"workerrest/internal/settings"
)

// 缩放 / 小屏下窗口尺寸夹紧的回归（2026-11-05）。
//
// 起因：probe_dpi_layout.py 按“物理分辨率 × 缩放”跑了 25 种组合，发现 8 种组合下
// 窗口高度 600（最小高度兜底）**超过工作区 DIP 高度** —— 窗口底部落到任务栏后面，
// 用户怎么拖都看不到最后一块内容，连 #content 的滚动条都拖不到底。
// 最典型的是 1366×768 @125%：工作区只有 574 DIP，窗口却开 600。

// TestFitSizeInWorkAreaNeverTallerThanScreen 任何工作区下，窗口高度都不超过工作区。
func TestFitSizeInWorkAreaNeverTallerThanScreen(t *testing.T) {
	// 物理分辨率 × 缩放 → 工作区（任务栏按 40 物理像素 × 缩放折）
	screens := []struct {
		name     string
		physW    int
		physH    int
		taskbarH int
	}{
		{"1366×768", 1366, 768, 40},
		{"1920×1080", 1920, 1080, 40},
		{"2560×1440", 2560, 1440, 40},
		{"2880×1800", 2880, 1800, 40},
		{"3840×2160", 3840, 2160, 40},
	}
	scales := []float64{1, 1.25, 1.5, 2, 3}

	for _, sc := range screens {
		for _, scale := range scales {
			waW := int(float64(sc.physW) / scale)
			waH := int(float64(sc.physH-sc.taskbarH) / scale)
			w, h := fitSizeInWorkArea(
				settings.SettingsWindowDefaultWidth, settings.SettingsWindowDefaultHeight,
				waW, waH, settings.SettingsWindowMinWidth)
			if h > waH {
				t.Errorf("%s @%g%%：窗口高 %d 超过工作区高 %d（底部会跑到屏幕外）",
					sc.name, scale*100, h, waH)
			}
			if w > waW && waW >= settings.SettingsWindowMinWidth {
				t.Errorf("%s @%g%%：窗口宽 %d 超过工作区宽 %d", sc.name, scale*100, w, waW)
			}
		}
	}
}

// TestFitSizeInWorkAreaKeepsDefaultOnBigScreen 大屏上默认尺寸原样保留（不被顺手改动）。
func TestFitSizeInWorkAreaKeepsDefaultOnBigScreen(t *testing.T) {
	w, h := fitSizeInWorkArea(874, 857, 1920, 1040, 760)
	if w != 874 || h != 857 {
		t.Fatalf("1920×1040 的工作区放得下 874×857，实际被改成 %dx%d", w, h)
	}
	// 2560×1440 @150% 的工作区是 1706×920，也放得下
	if w, h := fitSizeInWorkArea(874, 857, 1706, 920, 760); w != 874 || h != 857 {
		t.Fatalf("1706×920 的工作区放得下 874×857，实际被改成 %dx%d", w, h)
	}
}

// TestFitSizeInWorkAreaClampsHeightButNotBelowUsable 矮屏上夹高度，且留 40 DIP 余量。
func TestFitSizeInWorkAreaClampsHeightButNotBelowUsable(t *testing.T) {
	// 1366×768 @125%：工作区 1092×574
	w, h := fitSizeInWorkArea(874, 857, 1092, 574, 760)
	if h != 534 {
		t.Fatalf("工作区 574 高时应夹到 534（留 40 余量），实际 %d", h)
	}
	if w != 874 {
		t.Fatalf("宽度 874 放得下，不该被动，实际 %d", w)
	}
}

// TestFitSizeInWorkAreaWidthFloorIsDeliberate 宽度下限不让位：再窄字段行就横向裁切。
//
// 这是**刻意的取舍**（见 fitSizeInWorkArea 的注释）：工作区窄到 760 以下只出现在
// 200%+ 缩放的极端配置里，那时窗口宽出屏幕一部分，好过把输入框挤到不可用。
// 钉住这个行为，避免以后有人“顺手对称一下”把宽度也改成让位。
func TestFitSizeInWorkAreaWidthFloorIsDeliberate(t *testing.T) {
	w, _ := fitSizeInWorkArea(874, 857, 683, 344, 760)
	if w != 760 {
		t.Fatalf("工作区 683 宽时应保留下限 760（宽度是布局硬下限），实际 %d", w)
	}
}

// TestFitSizeInWorkAreaTinyWorkArea 工作区矮到留不出 40 DIP 余量时不再留边，但仍不超屏。
func TestFitSizeInWorkAreaTinyWorkArea(t *testing.T) {
	for _, waH := range []int{40, 30, 10, 1} {
		_, h := fitSizeInWorkArea(874, 857, 1280, waH, 760)
		if h > waH {
			t.Errorf("工作区高 %d 时窗口高 %d 超过工作区", waH, h)
		}
		if h < 1 {
			t.Errorf("工作区高 %d 时窗口高算成了 %d（不可见）", waH, h)
		}
	}
}

// TestSettingsWindowMinHeightFallsBackToScreen settingsWindowMinHeight 在矮屏上必须让位。
//
// 它只依赖屏信息，取不到屏时返回设计下限；这里只能验“无 app / 无屏”这条分支，
// 真实屏的分支由 probe_dpi_layout.py 的矩阵覆盖（需要真窗口）。
func TestSettingsWindowMinHeightWithoutScreen(t *testing.T) {
	a := &App{} // a.app == nil
	if got := a.settingsWindowMinHeight(); got != settings.SettingsWindowMinHeight {
		t.Fatalf("取不到屏信息时应回落到设计下限 %d，实际 %d",
			settings.SettingsWindowMinHeight, got)
	}
}

// TestFitSizeInWorkAreaExactKeepsRecordedSize 记录过的尺寸在工作区内必须**原样**还原。
//
// 起因（2026-11-24 用户反馈）：远程桌面里勾“自动记录窗口尺寸”，关掉设置窗口再打开，
// 尺寸比关闭时小一截。根因是恢复时走的是默认尺寸那一套 `-40 DIP` 余量，而远程会话
// 的工作区本来就不高（688 DIP），用户拖到 688 就顶到上限，重开被夹成 648 —— 关一次
// 小一截，看起来就是“记不住”。
func TestFitSizeInWorkAreaExactKeepsRecordedSize(t *testing.T) {
	// 记录值完全放得下：一个像素都不许动
	if w, h := fitSizeInWorkAreaExact(1119, 600, 1280, 688, 760); w != 1119 || h != 600 {
		t.Fatalf("工作区 1280x688 放得下 1119x600，被改成 %dx%d", w, h)
	}
	// 记录值正好等于工作区高度：不许再拿 40 DIP 余量去削（旧行为会削成 648）
	if w, h := fitSizeInWorkAreaExact(1119, 688, 1280, 688, 760); w != 1119 || h != 688 {
		t.Fatalf("记录高度正好等于工作区 688 时应原样保留，实际 %dx%d", w, h)
	}
	// 真的超出工作区才夹，且夹到工作区本身，不是工作区 - 40
	if w, h := fitSizeInWorkAreaExact(1119, 900, 1280, 688, 760); w != 1119 || h != 688 {
		t.Fatalf("超出工作区应夹到 688，实际 %dx%d", w, h)
	}
	if w, h := fitSizeInWorkAreaExact(1400, 600, 1280, 688, 760); w != 1280 || h != 600 {
		t.Fatalf("宽度超出工作区应夹到 1280，实际 %dx%d", w, h)
	}
	// 宽度下限照旧不让位
	if w, _ := fitSizeInWorkAreaExact(1000, 600, 683, 688, 760); w != 760 {
		t.Fatalf("工作区窄于下限时保留 760，实际 %d", w)
	}
	// 工作区高度算成 0（屏信息脏数据）时不能给出 0 高的窗口
	if _, h := fitSizeInWorkAreaExact(874, 857, 1280, 0, 760); h != 1 {
		t.Fatalf("工作区高 0 时应兜底为 1，实际 %d", h)
	}
}

// TestScreenWorkAreaDipDividesOnce 工作区只能折算一次。
//
// 起因（2026-11-24 真实屏复现）：Wails v3 的 ScreenManager.applyDPIScaling 已经把
// WorkArea 从 PhysicalWorkArea 换算成 DIP，这里又除了一遍 ScaleFactor —— 200% 屏上
// 1560×992 的工作区被算成 780×496。用户拖到 1100×700 关了重开，就被这个假工作区
// 夹成 780×496，看到的是“每次关掉再打开都小一圈”。
//
// 真实进程里打印出来的三元组（本机 RDP 3120×1984 @200%）：
//
//	WorkArea=1560x992  PhysicalWorkArea=3120x1984  ScaleFactor=2
//
// 拿 WorkArea 再除 2 就是 780×496 —— 与修复前日志里的“屏工作区 780x496 DIP”、以及
// 实测被夹成的 764×456（780−16 × 496−40）完全吻合。这条测试锁的就是“不能再除第二次”。
func TestScreenWorkAreaDipDividesOnce(t *testing.T) {
	// 200%：物理 3120×1984 → DIP 1560×992。WorkArea 已被 Wails 换算过，不能再用它除。
	s := &application.Screen{
		WorkArea:         application.Rect{Width: 1560, Height: 992},
		PhysicalWorkArea: application.Rect{Width: 3120, Height: 1984},
		ScaleFactor:      2,
	}
	if w, h, ok := screenWorkAreaDip(s); !ok || w != 1560 || h != 992 {
		t.Fatalf("200%% 屏工作区应为 1560x992，实际 %dx%d (ok=%v)", w, h, ok)
	}

	// 125%：物理 1366×728 → DIP 1093×582
	s = &application.Screen{
		WorkArea:         application.Rect{Width: 1093, Height: 582},
		PhysicalWorkArea: application.Rect{Width: 1366, Height: 728},
		ScaleFactor:      1.25,
	}
	if w, h, _ := screenWorkAreaDip(s); w != 1092 || h != 582 {
		t.Fatalf("125%% 屏工作区应为 1092x582（截断），实际 %dx%d", w, h)
	}

	// 铁证用例：即便 WorkArea 看起来像“被除过一遍”的 780×496，只要物理工作区与缩放
	// 能算出 1560×992，就必须以 PhysicalWorkArea 为准。谁把实现改回 `WorkArea / scale`
	// 这里会变成 390×248，测试立刻红。
	s = &application.Screen{
		WorkArea:         application.Rect{Width: 780, Height: 496},
		PhysicalWorkArea: application.Rect{Width: 3120, Height: 1984},
		ScaleFactor:      2,
	}
	if w, h, _ := screenWorkAreaDip(s); w != 1560 || h != 992 {
		t.Fatalf("应以 PhysicalWorkArea 为准得到 1560x992，实际 %dx%d", w, h)
	}

	// 物理工作区缺失时回退 WorkArea（v3 里它已经是 DIP，别再除）
	s = &application.Screen{
		WorkArea:    application.Rect{Width: 1280, Height: 688},
		ScaleFactor: 2,
	}
	if w, h, ok := screenWorkAreaDip(s); !ok || w != 1280 || h != 688 {
		t.Fatalf("缺 PhysicalWorkArea 时应直接用 WorkArea 1280x688，实际 %dx%d (ok=%v)", w, h, ok)
	}

	// 全空：ok=false，调用方保持原尺寸
	if _, _, ok := screenWorkAreaDip(&application.Screen{}); ok {
		t.Fatal("没有任何工作区信息时不该返回 ok")
	}
	if _, _, ok := screenWorkAreaDip(nil); ok {
		t.Fatal("nil 屏不该返回 ok")
	}
}

// TestSettingsWindowMinHeightWith 最小高度在矮屏上要让位，但不能压到没法用。
//
// 建窗时 Wails 还没枚举屏，靠原生探测拿工作区（settingsWindowMinHeightForCreate），
// 所以这条纯函数是两个入口的共同判据，值得单独锁。
func TestSettingsWindowMinHeightWith(t *testing.T) {
	cases := []struct {
		name string
		waH  int
		ok   bool
		want int
	}{
		{"没屏信息 → 设计下限", 0, false, 600},
		{"大屏（200% 的 1560x992）→ 设计下限", 992, true, 600},
		{"正好等于下限", 600, true, 600},
		{"1366x768@125%（574）→ 让位到 534", 574, true, 574 - 40},
		{"1366x768@150%（472）→ 让位到 432", 472, true, 472 - 40},
		{"矮到留不出余量（200）→ 压到工作区本身", 200, true, 200},
	}
	for _, c := range cases {
		if got := settingsWindowMinHeightWith(c.waH, c.ok); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}
