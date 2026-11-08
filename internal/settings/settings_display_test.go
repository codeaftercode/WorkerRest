package settings

import (
	"testing"
	"time"
	"workerrest/internal/display"
)

// 历史显示器目录：只在“真的变了”时回报 changed（每打开一次设置页就重写配置是不可接受的），
// 且永远不会因为“这次没枚举到”而丢条目 —— 删除只能由用户在面板上点“删除”。
func TestRememberDisplaysAddsAndKeepsLastSeen(t *testing.T) {
	s := New(rawOf(t, `{"displayKnown":[]}`), "")
	now := time.Date(2026, 11, 8, 14, 0, 0, 0, time.Local)
	list := []display.Info{
		{ID: "AAA#1", Label: "DELL U2412M", Width: 1920, Height: 1200},
	}
	if !s.RememberDisplays(list, now) {
		t.Fatal("第一次记录应当回报 changed")
	}
	got := s.DisplayKnown()
	if len(got) != 1 || got[0].ID != "AAA#1" || got[0].Label != "DELL U2412M" ||
		got[0].Width != 1920 || got[0].Height != 1200 {
		t.Fatalf("目录内容不对: %#v", got)
	}
	if got[0].LastSeen != now.Format(time.RFC3339) {
		t.Fatalf("lastSeen = %q，期望 %q", got[0].LastSeen, now.Format(time.RFC3339))
	}

	// 一模一样再来一次：lastSeen 会刷新，但不算“值得落盘的变化”
	later := now.Add(time.Hour)
	if s.RememberDisplays(list, later) {
		t.Fatal("内容没变不该回报 changed（否则每次开设置页都重写配置文件）")
	}

	// 名字 / 分辨率变了要回报 changed
	renamed := []display.Info{{ID: "AAA#1", Label: "DELL U2412M", Width: 2560, Height: 1440}}
	if !s.RememberDisplays(renamed, later) {
		t.Fatal("分辨率变了应当回报 changed")
	}

	// 枚举结果为空（拔光了 / 枚举失败）：一条都不许删
	if s.RememberDisplays(nil, later) {
		t.Fatal("空列表不该改动目录")
	}
	if len(s.DisplayKnown()) != 1 {
		t.Fatalf("空列表把目录清空了: %#v", s.DisplayKnown())
	}
}

// 占位名（“显示器 N”）不许覆盖记过的真名：某次枚举只给出占位名时，历史记录里
// 那块“LG 27UL850”得留着 —— 面板左列显示的就是真名，两处对不上会让人以为换了屏。
func TestRememberDisplaysKeepsRealNameOverPlaceholder(t *testing.T) {
	s := New(rawOf(t, `{"displayKnown":[]}`), "")
	now := time.Date(2026, 11, 8, 14, 0, 0, 0, time.Local)
	real := []display.Info{{ID: "AAA#1", Label: "LG 27UL850", Width: 3840, Height: 2160}}
	s.RememberDisplays(real, now)

	// 分辨率没变，只有名字退化成占位 → 不该回报 changed，也不该改名字
	placeholder := []display.Info{{ID: "AAA#1", Label: "显示器 1", Width: 3840, Height: 2160}}
	if s.RememberDisplays(placeholder, now.Add(time.Hour)) {
		t.Fatal("占位名不该算“名字变了”（会把历史名字覆盖掉）")
	}
	if got := s.DisplayKnown(); len(got) != 1 || got[0].Label != "LG 27UL850" {
		t.Fatalf("真名被占位名盖掉了: %#v", got)
	}

	// 反过来：先只有占位名，后来拿到真名 → 要更新
	s2 := New(rawOf(t, `{"displayKnown":[]}`), "")
	s2.RememberDisplays(placeholder, now)
	if !s2.RememberDisplays(real, now) {
		t.Fatal("拿到真名应当回报 changed")
	}
	if got := s2.DisplayKnown(); got[0].Label != "LG 27UL850" {
		t.Fatalf("真名没写进去: %#v", got)
	}
}

// 目录按最后可见时间倒序：面板从旧到新列一遍时，最近用过的排在最上面。
func TestDisplayKnownSortedByLastSeen(t *testing.T) {
	s := New(rawOf(t, `{"displayKnown":[
		{"id":"OLD#1","label":"旧屏","width":1024,"height":768,"lastSeen":"2026-10-01T10:00:00+08:00"},
		{"id":"NEW#1","label":"新屏","width":2560,"height":1440,"lastSeen":"2026-11-07T10:00:00+08:00"}
	]}`), "")
	got := s.DisplayKnown()
	if len(got) != 2 || got[0].ID != "NEW#1" || got[1].ID != "OLD#1" {
		t.Fatalf("排序不对: %#v", got)
	}
}

// 目录归一化：坏值（缺 id / 空 id / 非对象）一律丢掉，同 ID 后写的覆盖先写的，
// 宽高只认正数（0 在面板上退化成只显示名字，不显示“0×0”）。
func TestNormalizeDisplayKnownDropsJunk(t *testing.T) {
	raw := rawOf(t, `{"displayKnown":[
		{"id":"","label":"空 ID"},
		{"label":"没有 ID"},
		"不是对象",
		{"id":"AAA#1","label":"先写的","width":-5,"height":0,"lastSeen":"2026-11-01T00:00:00+08:00"},
		{"id":"AAA#1","label":"后写的","width":1920,"height":1080,"lastSeen":"2026-11-02T00:00:00+08:00"}
	]}`)
	raw["displayKnown"] = normalizeDisplayKnown(raw["displayKnown"])
	s := New(raw, "")
	got := s.DisplayKnown()
	if len(got) != 1 {
		t.Fatalf("应当只剩 1 条（同 ID 合并），得到 %#v", got)
	}
	if got[0].Label != "后写的" || got[0].Width != 1920 || got[0].Height != 1080 {
		t.Fatalf("同 ID 应当由后写的覆盖: %#v", got[0])
	}

	// 坏值整个清成空数组（= 没记过），不留怪形状让前端去猜
	raw2 := rawOf(t, `{"displayKnown":"x"}`)
	raw2["displayKnown"] = normalizeDisplayKnown(raw2["displayKnown"])
	if arr, ok := raw2["displayKnown"].([]any); !ok || len(arr) != 0 {
		t.Fatalf("非数组应当归一化成空数组，得到 %#v", raw2["displayKnown"])
	}
}

// 出厂配置里必须有 displayKnown 这个键：设置页读它来决定要不要画“历史”那一栏，
// 缺键时前端只能按“没记过”处理，等于功能静默失效。
func TestDefaultHasDisplayKnown(t *testing.T) {
	s := New(DefaultSettingsMap(), "")
	if s.DisplayKnown() != nil {
		t.Fatalf("出厂目录应当为空，得到 %#v", s.DisplayKnown())
	}
	if _, ok := s.Raw()["displayKnown"]; !ok {
		t.Fatal("出厂配置缺 displayKnown 键")
	}
}

// 会话级虚拟屏（远程桌面的 DEFAULT_MONITOR / pos:x,y 兜底）不进历史目录，
// 而且**老配置里已经写进去的这类记录会被清掉** —— 否则今天远程、明天本地，
// 历史里就多出一条永远“未连接”的“显示器 1”。
func TestDisplayKnownSkipsSessionScopedDisplays(t *testing.T) {
	now := time.Date(2026, 11, 8, 19, 0, 0, 0, time.Local)

	// 写入口：远程会话枚举结果里只有那块虚拟屏 → 目录保持空，也不回报 changed
	s := New(rawOf(t, `{"displayKnown":[]}`), "")
	remote := []display.Info{
		{ID: "DEFAULT_MONITOR#1&19638b53&1&UID256", Label: "显示器 1", Width: 3120, Height: 2080},
		{ID: "pos:0,0", Label: "显示器 2", Width: 1920, Height: 1080},
	}
	if s.RememberDisplays(remote, now) {
		t.Fatal("会话级虚拟屏不该写进历史目录")
	}
	if got := s.DisplayKnown(); len(got) != 0 {
		t.Fatalf("历史目录应当为空，得到 %#v", got)
	}

	// 本地会话：同一台机器换成两块真屏 → 只记这两块
	local := []display.Info{
		{ID: "DEL41A9#4&1ff378d5&0&UID4162", Label: "DELL U2719DS", Width: 2560, Height: 1440},
		{ID: "DELA0FD#5&1876e159&0&UID4352", Label: "DELL SP2318H", Width: 1920, Height: 1080},
	}
	if !s.RememberDisplays(local, now) {
		t.Fatal("真屏应当写进历史目录")
	}
	if got := s.DisplayKnown(); len(got) != 2 {
		t.Fatalf("应当只记两块真屏，得到 %#v", got)
	}

	// 读入口 + 归一化：老配置里残留的虚拟屏记录读不出来，且归一化时被删掉
	raw := rawOf(t, `{"displayKnown":[
		{"id":"DEFAULT_MONITOR#1&19638b53&1&UID256","label":"显示器 1","width":3120,"height":2080,"lastSeen":"2026-11-08T18:00:00+08:00"},
		{"id":"pos:0,0","label":"显示器 2","width":1920,"height":1080,"lastSeen":"2026-11-07T18:00:00+08:00"},
		{"id":"DEL41A9#4&1ff378d5&0&UID4162","label":"DELL U2719DS","width":2560,"height":1440,"lastSeen":"2026-11-06T18:00:00+08:00"}
	]}`)
	old := New(raw, "")
	if got := old.DisplayKnown(); len(got) != 1 || got[0].ID != "DEL41A9#4&1ff378d5&0&UID4162" {
		t.Fatalf("读入口应当滤掉会话级记录，得到 %#v", got)
	}
	raw["displayKnown"] = normalizeDisplayKnown(raw["displayKnown"])
	arr, _ := raw["displayKnown"].([]any)
	if len(arr) != 1 {
		t.Fatalf("归一化应当把会话级记录从配置里清掉，得到 %#v", raw["displayKnown"])
	}
}
