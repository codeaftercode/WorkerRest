package main

import (
	"testing"

	"workerrest/internal/display"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// 双屏真机上的一副典型形状：主屏 2560×1440（任务栏占 48），副屏 1920×1080 摆在右边。
// 注意 WorkArea 比 Bounds 矮 —— 这正是 2026-11-04 那个 bug 的成因。
func twoScreens() (primary, secondary *application.Screen) {
	primary = &application.Screen{
		ID:        "111",
		Bounds:    application.Rect{X: 0, Y: 0, Width: 2560, Height: 1440},
		WorkArea:  application.Rect{X: 0, Y: 0, Width: 2560, Height: 1392},
		IsPrimary: true,
	}
	secondary = &application.Screen{
		ID:       "222",
		Bounds:   application.Rect{X: 2560, Y: -209, Width: 1920, Height: 1080},
		WorkArea: application.Rect{X: 2560, Y: -209, Width: 1920, Height: 1040},
	}
	return primary, secondary
}

// TestMatchScreenUsesBoundsNotWorkArea 钉住“按整块屏矩形对齐”这条。
//
// 曾经的实现拿 WorkArea 比 display.Info 的四边，而 display.Info 是整块屏的源模式尺寸
// （不含任务栏扣减），于是：
//   - 选主屏：全部失配 → 走“回退主屏”兜底，看起来是对的（蒙对）；
//   - 选副屏：同样全部失配 → 还是回退主屏 → **勾了副屏永远只有主屏弹窗**，且日志无异常。
//
// 这条测试直接构造“工作区矮 48px”的屏幕，钉死不能再用 WorkArea 对齐。
func TestMatchScreenUsesBoundsNotWorkArea(t *testing.T) {
	primary, secondary := twoScreens()
	all := []*application.Screen{primary, secondary}

	gotP := matchScreen(all, display.Info{ID: "A", Label: "主屏", X: 0, Y: 0, Width: 2560, Height: 1440})
	if gotP != primary {
		t.Fatalf("主屏应匹配到 primary，得到 %+v", gotP)
	}

	gotS := matchScreen(all, display.Info{ID: "B", Label: "副屏", X: 2560, Y: -209, Width: 1920, Height: 1080})
	if gotS != secondary {
		t.Fatalf("副屏应匹配到 secondary（不能因为 WorkArea 矮 40px 就失配），得到 %+v", gotS)
	}
}

// TestMatchScreenToleratesSmallDrift 驱动报的矩形偶尔差 1~2 px，中心点兜底要接住。
func TestMatchScreenToleratesSmallDrift(t *testing.T) {
	primary, secondary := twoScreens()
	all := []*application.Screen{primary, secondary}

	// 高度差 2px：四边不全等，但中心点仍在 secondary 里
	got := matchScreen(all, display.Info{ID: "B", Label: "副屏", X: 2560, Y: -209, Width: 1920, Height: 1078})
	if got != secondary {
		t.Fatalf("差 2px 时应靠中心点匹配到 secondary，得到 %+v", got)
	}

	// 完全不在任何一块屏上：必须返回 nil（让上层跳过并记日志），不许瞎猜一块
	if got := matchScreen(all, display.Info{ID: "C", X: 9000, Y: 0, Width: 1920, Height: 1080}); got != nil {
		t.Fatalf("屏外矩形不该匹配到任何屏，得到 %+v", got)
	}
}
