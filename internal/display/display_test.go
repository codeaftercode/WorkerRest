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
		t.Fatal("排序后应补齐 Label（型号名取不到时补“显示器 N”占位）")
	}
}

// 占位名要认得出，否则历史目录里记过的真名会被“显示器 1”盖掉。
func TestIsPlaceholderLabel(t *testing.T) {
	placeholder := []string{"显示器 1", "显示器 2", " 显示器 12 ", "显示器 100"}
	for _, s := range placeholder {
		if !IsPlaceholderLabel(s) {
			t.Fatalf("%q 应判为占位名", s)
		}
	}
	real := []string{"", "显示器", "显示器 A", "DELL U2720Q", "LG 27UL850", "3120×2080", "显示器 0", "显示器 -1"}
	for _, s := range real {
		if IsPlaceholderLabel(s) {
			t.Fatalf("%q 不是占位名，不该被当成占位名", s)
		}
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
		// 换会话 / 拔线换口后 ID 全变：按“勾了几块”沿用，不再只回退主屏
		{"全部失效 → 按勾选数沿用（2 块）", []string{"gone-1", "gone-2"}, []string{"p", "l"}},
	}
	for _, c := range cases {
		got := ids(Resolve(c.ids, all, nil))
		if !same(got, c.want) {
			t.Errorf("%s：得到 %v，期望 %v", c.name, got, c.want)
		}
	}

	if got := Resolve([]string{"l"}, nil, nil); got != nil {
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
	got := Resolve(nil, all, nil)
	if len(got) != 1 || got[0].ID != "p" {
		t.Fatalf("空配置只应回退主屏，得到 %v", got)
	}
}

// TestIsSessionScopedID 钉住“哪些标识不能进历史目录”。
//
// 场景：今天远程连一次（只会枚举到 DEFAULT_MONITOR#…），明天在真机上用
// （只会枚举到 DEL41A9#…），两者互斥。把远程那块记进历史，真机上就会多出一条
// 永远“未连接”的“显示器 1”，删了下次远程还回来。
func TestIsSessionScopedID(t *testing.T) {
	session := []string{
		"DEFAULT_MONITOR#1&19638b53&1&UID256",
		"Default_Monitor#1&19638b53&1&UID256", // 大小写两种写法系统都给过
		"pos:0,0",
		"pos:-1920,0",
		"",
		"   ",
	}
	for _, id := range session {
		if !IsSessionScopedID(id) {
			t.Errorf("%q 应当算会话级（不进历史目录）", id)
		}
	}
	real := []string{
		"DEL41A9#4&1ff378d5&0&UID4162",
		"DELA0FD#5&1876e159&0&UID4352",
		"GSM5B7F#4&1ff378d5&0&UID4162",
	}
	for _, id := range real {
		if IsSessionScopedID(id) {
			t.Errorf("%q 是真实显示器，不该被当成会话级", id)
		}
	}
}

// TestResolveFallbackByCount 钉住“换会话后勾选全部失效”时的兜底。
//
// 场景：今天远程（两块 DEFAULT_MONITOR 虚拟屏）勾了两块，明天真机（两块 DEL…）
// 一个 ID 都对不上。改之前只回退主屏 → 用户看到的是“勾了两块、只弹一块”，
// 必须重进设置页重勾。现在按“勾了几块”沿用，两块还是两块。
//
// 但不能因此把新接的东西算进来：所以优先取历史目录里见过的屏（真屏），
// 兜底数量不够时才按位置补。3 块屏（两块常用 + 一块刚插的投影）时不会挑到投影。
func TestResolveFallbackByCount(t *testing.T) {
	ids := func(list []Info) []string {
		out := make([]string, len(list))
		for i, d := range list {
			out[i] = d.ID
		}
		return out
	}

	// ① 远程勾 2 块 → 真机 2 块：两块都弹
	local2 := []Info{
		{ID: "DEL41A9#1", IsPrimary: true, X: 0, Y: 0},
		{ID: "DELA0FD#2", X: 2560, Y: 0},
	}
	if got := ids(Resolve([]string{"DEFAULT_MONITOR#1", "DEFAULT_MONITOR#2"}, local2, nil)); len(got) != 2 {
		t.Fatalf("两块对两块应当沿用两块，得到 %v", got)
	}

	// ② 勾 2 块，当前 3 块（左边多插了一块投影，且投影不在历史目录里）：只取见过的两块
	three := []Info{
		{ID: "DEL41A9#1", IsPrimary: true, X: 0, Y: 0},
		{ID: "PROJ#9", X: -1920, Y: 0}, // 排序后夹在主屏与右屏之间，最容易误取
		{ID: "DELA0FD#2", X: 2560, Y: 0},
	}
	got := ids(Resolve([]string{"DEFAULT_MONITOR#1", "DEFAULT_MONITOR#2"}, three,
		[]string{"DEL41A9#1", "DELA0FD#2"}))
	if len(got) != 2 || got[0] != "DEL41A9#1" || got[1] != "DELA0FD#2" {
		t.Fatalf("应当优先取历史里见过的两块（跳过投影），得到 %v", got)
	}

	// ③ 勾 2 块但当前只剩 1 块：取 1 块，别造出不存在的屏
	if got := ids(Resolve([]string{"x1", "x2"}, local2[:1], nil)); len(got) != 1 {
		t.Fatalf("当前屏少于勾选数时只能取现有数量，得到 %v", got)
	}

	// ④ 只勾 1 块：沿用 1 块（主屏优先），不要因为兜底就弹成两块
	one := ids(Resolve([]string{"DEFAULT_MONITOR#1"}, local2, nil))
	if len(one) != 1 || one[0] != "DEL41A9#1" {
		t.Fatalf("勾一块只该沿用一块（主屏），得到 %v", one)
	}
}
