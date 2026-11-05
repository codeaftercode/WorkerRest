package main

import (
	"testing"

	"workerrest/internal/settings"
)

// 缩放 / 小屏下窗口尺寸夹紧的回归（2026-11-05）。
//
// 起因：probe_dpi_layout.py 按「物理分辨率 × 缩放」跑了 25 种组合，发现 8 种组合下
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
// 钉住这个行为，避免以后有人「顺手对称一下」把宽度也改成让位。
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
// 它只依赖屏信息，取不到屏时返回设计下限；这里只能验「无 app / 无屏」这条分支，
// 真实屏的分支由 probe_dpi_layout.py 的矩阵覆盖（需要真窗口）。
func TestSettingsWindowMinHeightWithoutScreen(t *testing.T) {
	a := &App{} // a.app == nil
	if got := a.settingsWindowMinHeight(); got != settings.SettingsWindowMinHeight {
		t.Fatalf("取不到屏信息时应回落到设计下限 %d，实际 %d",
			settings.SettingsWindowMinHeight, got)
	}
}
