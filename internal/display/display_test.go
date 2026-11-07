package display

import "testing"

func TestDevicePathToID(t *testing.T) {
	cases := []struct{ in, want string }{
		// 真机实测形态（DEL = DELL，后段是适配器实例 + 端口 UID）
		{`\\?\DISPLAY#DEL41A9#4&1ff378d5&0&UID4162#{e6f07b5f-ee97-4a90-b076-33f57bf4eaa7}`, `DEL41A9#4&1ff378d5&0&UID4162`},
		{`\\?\DISPLAY#DELA0FD#5&1876e159&0&UID4352#{e6f07b5f-ee97-4a90-b076-33f57bf4eaa7}`, `DELA0FD#5&1876e159&0&UID4352`},
		{``, ``},
		// 没有 \\?\DISPLAY# 前缀也要能压（个别驱动的路径不带前缀）
		{`MONITOR#ACR0123#1&2&3#{guid}`, `MONITOR#ACR0123#1&2&3`},
		// 没有 GUID 段（驱动没给）→ 原样保留，不能截断成空串
		{`\\?\DISPLAY#SAM0F99#4&abc&0&UID1`, `SAM0F99#4&abc&0&UID1`},
	}
	for _, c := range cases {
		if got := devicePathToID(c.in); got != c.want {
			t.Errorf("devicePathToID(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

func TestSortInfos(t *testing.T) {
	list := []Info{
		{ID: "b", X: 1920, Y: 0},
		{ID: "a", X: 0, Y: 0, IsPrimary: true},
		{ID: "c", X: -1920, Y: 0},
	}
	sortInfos(list)
	if list[0].ID != "a" {
		t.Fatalf("主屏应排在最前，得到 %v", list)
	}
	// 其余按 X 升序：-1920 在 1920 前
	if list[1].ID != "c" || list[2].ID != "b" {
		t.Fatalf("非主屏应按 X 升序，得到 %v", list)
	}
	if list[0].Label == "" {
		t.Fatal("排序后应补齐 Label（EDID 名取不到时退化成“显示器 N”）")
	}
}

func TestResolve(t *testing.T) {
	all := []Info{
		{ID: "p", Label: "主屏", IsPrimary: true, X: 0, Y: 0},
		{ID: "l", Label: "左屏", X: -1920, Y: 0},
		{ID: "r", Label: "右屏", X: 1920, Y: 0},
	}
	ids := func(list []Info) []string {
		out := make([]string, len(list))
		for i, d := range list {
			out[i] = d.ID
		}
		return out
	}
	same := func(a, b []string) bool {
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return true
	}

	cases := []struct {
		name string
		ids  []string
		want []string
	}{
		{"空列表 = 只主屏（不是所有屏）", nil, []string{"p"}},
		{"空切片 = 只主屏", []string{}, []string{"p"}},
		{"单个具体屏", []string{"l"}, []string{"l"}},
		{"多选保持 all 的顺序", []string{"r", "l"}, []string{"l", "r"}},
		{"全部失效（拔线换口）→ 回退主屏", []string{"gone-1", "gone-2"}, []string{"p"}},
	}
	for _, c := range cases {
		got := ids(Resolve(c.ids, all))
		if !same(got, c.want) {
			t.Errorf("%s：得到 %v，期望 %v", c.name, got, c.want)
		}
	}

	if got := Resolve([]string{"l"}, nil); got != nil {
		t.Fatalf("一块屏都没有时应返回 nil，得到 %v", got)
	}
}

// TestResolveEmptyNeverMeansAll 空配置不能等于“所有屏”。
//
// 这条钉的是产品诉求：接上投影仪的那一刻，提醒不该因为“投影仪新插进来”就弹上去。
// 空配置只兜底主屏，多屏必须由用户亲手勾。改成“空 = all”会让这条立刻红。
func TestResolveEmptyNeverMeansAll(t *testing.T) {
	all := []Info{
		{ID: "p", IsPrimary: true, X: 0, Y: 0},
		{ID: "proj", X: 1920, Y: 0},
	}
	got := Resolve(nil, all)
	if len(got) != 1 || got[0].ID != "p" {
		t.Fatalf("空配置只应回退主屏，得到 %v", got)
	}
}
