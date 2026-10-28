package main

import (
	"path/filepath"
	"testing"
)

// 候选专员顺序的回归（2026-10-26）。
//
// 需求：设置页「督休专员 → 候选专员」列表支持**拖动立绘调整顺序**并持久化。
// 顺序落在 config/settings.json 的 supervisor.order（专员名数组），
// 画廊显示顺序与「顺序轮换」的出场次序**共用**这一份 —— 拖一次两边同时生效，
// 不会出现「画廊一个样、轮换另一个样」的劈叉。

func roleNames(roles []Role) []string {
	out := make([]string, 0, len(roles))
	for _, r := range roles {
		out = append(out, r.Name)
	}
	return out
}

func eqKeys(a, b []string) bool {
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

// applyRoleOrder：按 order 重排；order 里缺的追加在后，多余的忽略
func TestApplyRoleOrder(t *testing.T) {
	roles := []Role{{Name: "a"}, {Name: "b"}, {Name: "c"}}
	cases := []struct {
		name  string
		order []string
		want  []string
	}{
		{"空顺序 = 原序", nil, []string{"a", "b", "c"}},
		{"完全重排", []string{"c", "a", "b"}, []string{"c", "a", "b"}},
		{"部分覆盖：缺的追加在后", []string{"c"}, []string{"c", "a", "b"}},
		{"多余的 key 忽略", []string{"x", "b", "y"}, []string{"b", "a", "c"}},
		{"重复 key 只取一次", []string{"b", "b"}, []string{"b", "a", "c"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := roleNames(applyRoleOrder(roles, tc.order))
			if !eqKeys(got, tc.want) {
				t.Fatalf("顺序不符: got %v want %v", got, tc.want)
			}
		})
	}
}

// SupervisorCfg 读得出 supervisor.order；杂质（空串 / 重复 / 非字符串）剔除
func TestSupervisorCfgReadsOrder(t *testing.T) {
	s := testSettings(t, `{"supervisor":{"order":["b","a","b","","a"]}}`)
	got := s.SupervisorCfg().Order
	want := []string{"b", "a"}
	if !eqKeys(got, want) {
		t.Fatalf("order 归一不对: got %v want %v", got, want)
	}
	// 类型不是数组 → nil（回落到内置清单顺序）
	s2 := testSettings(t, `{"supervisor":{"order":"abc"}}`)
	if s2.SupervisorCfg().Order != nil {
		t.Fatalf("类型不对时应为 nil, got %v", s2.SupervisorCfg().Order)
	}
	// 键不存在 → nil（老配置没有这个键）
	s3 := testSettings(t, `{"supervisor":{"enabled":true}}`)
	if s3.SupervisorCfg().Order != nil {
		t.Fatalf("缺键时应为 nil, got %v", s3.SupervisorCfg().Order)
	}
}

// 归一化：order 类型不对 → 整键丢掉；数组里的杂质剔除
func TestNormalizeSupervisorMessagesOrder(t *testing.T) {
	raw := map[string]any{"supervisor": map[string]any{"order": "nope"}}
	normalizeSupervisorMessages(raw)
	if _, ok := raw["supervisor"].(map[string]any)["order"]; ok {
		t.Fatal("类型不对的 order 应被丢掉")
	}
	raw2 := map[string]any{"supervisor": map[string]any{"order": []any{"b", 1, "", "a", "b"}}}
	normalizeSupervisorMessages(raw2)
	got, _ := raw2["supervisor"].(map[string]any)["order"].([]any)
	if len(got) != 2 || got[0] != "b" || got[1] != "a" {
		t.Fatalf("order 归一不对: %v", got)
	}

}

// ListRoles 按保存的顺序返回（设置页画廊与主窗口共用这一份）
func TestListRolesFollowsSavedOrder(t *testing.T) {
	a := newSupTestApp(t, `{"supervisor":{"enabled":true,"selection":"sequence","order":["sup-b","sup-a"]}}`)
	writeRole(t, a.rolesRoot, "sup-a", "sup-a", true)
	writeRole(t, a.rolesRoot, "sup-b", "sup-b", true)
	writeRole(t, a.rolesRoot, "sup-c", "sup-c", true)
	got := roleNames(a.ListRoles())
	want := []string{"sup-b", "sup-a", "sup-c"} // order 里没有的 sup-c 追加在后
	if !eqKeys(got, want) {
		t.Fatalf("ListRoles 顺序不对: got %v want %v", got, want)
	}
}

// 顺序轮换的出场次序 = 用户拖出来的顺序（画廊顺序 == 轮换次序）
func TestSequenceRotationFollowsSavedOrder(t *testing.T) {
	a := newSupTestApp(t, `{"supervisor":{"enabled":true,"selection":"sequence","order":["sup-c","sup-b","sup-a"]}}`)
	writeRole(t, a.rolesRoot, "sup-a", "sup-a", true)
	writeRole(t, a.rolesRoot, "sup-b", "sup-b", true)
	writeRole(t, a.rolesRoot, "sup-c", "sup-c", true)
	cfg := a.cfg.SupervisorCfg()
	want := []string{"sup-c", "sup-b", "sup-a"}
	for i, w := range want {
		st := a.buildSupervisorSessionWith(cfg, false, "")
		if st == nil {
			t.Fatalf("第 %d 次会话为空", i+1)
		}
		if st.RoleName != w {
			t.Fatalf("第 %d 次轮换应为 %s，实际 %s", i+1, w, st.RoleName)
		}
	}
}

// 设置页预览用**表单里**的顺序（刚拖完还没落盘也立刻生效）
func TestSupervisorPreviewUsesFormOrder(t *testing.T) {
	a := newSupTestApp(t, `{"supervisor":{"enabled":true,"selection":"sequence","order":["sup-a"]}}`)
	writeRole(t, a.rolesRoot, "sup-a", "sup-a", true)
	writeRole(t, a.rolesRoot, "sup-b", "sup-b", true)
	st := a.SupervisorPreview(map[string]any{
		"enabled":   true,
		"selection": "sequence",
		"order":     []any{"sup-b"},
	}, "")
	if st == nil {
		t.Fatal("预览会话为空")
	}
	if st.RoleName != "sup-b" {
		t.Fatalf("预览应按表单顺序取到 sup-b，实际 %s", st.RoleName)
	}
}

// 持久化：顺序写进 settings.json 后重新读回来仍在（走完整的加载 + 归一化管线）
func TestSupervisorOrderPersistsToDisk(t *testing.T) {
	dir := t.TempDir()
	iso := filepath.Join(dir, "settings.json")
	t.Setenv("WR_SETTINGS_PATH", iso)
	s := &Settings{raw: map[string]any{"supervisor": map[string]any{"order": []any{"b", "a"}}}}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	got := loadSettings(dir).SupervisorCfg().Order
	if !eqKeys(got, []string{"b", "a"}) {
		t.Fatalf("落盘后顺序丢失: got %v want [b a]", got)
	}
}

// 出厂默认里的 order: [] 不能把用户存下的顺序冲掉（数组整体替换，不是合并）
func TestDefaultOrderDoesNotOverrideUserOrder(t *testing.T) {
	base := defaultSettingsMap()
	disk := map[string]any{"supervisor": map[string]any{"order": []any{"b", "a"}}}
	merged, _ := deepMerge(base, disk).(map[string]any)
	got := (&Settings{raw: merged}).SupervisorCfg().Order
	if !eqKeys(got, []string{"b", "a"}) {
		t.Fatalf("出厂默认覆盖了用户顺序: got %v want [b a]", got)
	}
}
