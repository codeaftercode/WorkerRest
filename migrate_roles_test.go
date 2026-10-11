package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// 督休专员目录迁移的纯函数回归（毫秒级）。与 上一版的 migrateLegacyRoles 逐条对应，
// 分工同 settings_test.go：这里拦「规则写错」，与 上一版的逐字段对拍交给
// verify_settings_parity.py（或将来新增的 verify_roles_parity）。

// seedBuiltinRoles 在 rolesRoot 下铺一份最小模板：只放 role_coach 与 role_ai 两个
// 内置督休专员（各带一份可解析的 role.json），模拟「模板目录已就位」。
func seedBuiltinRoles(t *testing.T, root string) {
	t.Helper()
	for _, key := range []string{"role_coach", "role_ai"} {
		dir := filepath.Join(root, key)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		cfg := RoleFile{
			Name:     roleFallbackName(key),
			Enabled:  true,
			Image:    "figure.png",
			Geom:     supGeomDefault,
			Messages: RoleMessages{Order: "fixed", Items: []RoleMessageItem{}},
		}
		if err := writeRoleFile(root, key, cfg); err != nil {
			t.Fatal(err)
		}
	}
}

// readRoleJSON 读一份 role.json 回 map，方便断言个别字段
func readRoleJSON(t *testing.T, root, key string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, key, "role.json"))
	if err != nil {
		t.Fatalf("读 %s/role.json 失败: %v", key, err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("解析 %s/role.json 失败: %v", key, err)
	}
	return m
}

// TestMigrateLegacyRolesFreshInstall 全新安装：没有任何旧键 → 短路置标记，不删内置目录
func TestMigrateLegacyRolesFreshInstall(t *testing.T) {
	root := t.TempDir()
	seedBuiltinRoles(t, root)
	raw := map[string]any{
		"supervisor": map[string]any{
			"selection": "fixed",
			"enabled":   true,
		},
	}
	if !migrateLegacyRoles(raw, root) {
		t.Fatal("全新安装应返回 true（置了 rolesMigrated）")
	}
	sup := raw["supervisor"].(map[string]any)
	if sup["rolesMigrated"] != true {
		t.Fatal("应置 rolesMigrated=true")
	}
	// 内置目录没被删（短路保护：不能把「旧配置没这督休专员」误判成「用户删过」）
	for _, key := range []string{"role_coach", "role_ai"} {
		if !isRegularFile(filepath.Join(root, key, "role.json")) {
			t.Fatalf("全新安装不该删内置目录 %s", key)
		}
	}
}

// TestMigrateLegacyRolesAlreadyMigrated 已迁移过 → 直接 false，不动任何东西
func TestMigrateLegacyRolesAlreadyMigrated(t *testing.T) {
	root := t.TempDir()
	seedBuiltinRoles(t, root)
	raw := map[string]any{
		"supervisor": map[string]any{
			"rolesMigrated": true,
			"roles":         []any{map[string]any{"key": "role_coach.png"}}, // 残留旧键
		},
	}
	if migrateLegacyRoles(raw, root) {
		t.Fatal("已迁移过应返回 false")
	}
	// 旧键原样保留（由 normalizeSupervisorMessages 的清理逻辑去删，不在迁移里做）
	sup := raw["supervisor"].(map[string]any)
	if _, ok := sup["roles"]; !ok {
		t.Fatal("已迁移过不应动旧键")
	}
}

// TestMigrateLegacyRolesBuiltinOverride 内置督休专员：旧数据覆盖模板 role.json
func TestMigrateLegacyRolesBuiltinOverride(t *testing.T) {
	root := t.TempDir()
	seedBuiltinRoles(t, root)
	raw := map[string]any{
		"supervisor": map[string]any{
			"roles": []any{
				map[string]any{"key": "role_coach.png", "name": "  魔鬼教练改名 ", "enabled": false},
				map[string]any{"key": "role_ai", "name": "赛博改", "enabled": true},
			},
			"geom": map[string]any{
				"role_coach": map[string]any{"sizePct": 55.0, "posXPct": 10.0, "posYPct": 5.0},
			},
			"messagesByRole": map[string]any{
				"role_coach": map[string]any{
					"order": "random",
					"items": []any{map[string]any{"text": "站起来！", "enabled": true}},
				},
			},
			"fixedKey": "role_coach.png",
		},
	}
	if !migrateLegacyRoles(raw, root) {
		t.Fatal("有旧键应发生迁移")
	}
	coach := readRoleJSON(t, root, "role_coach")
	if coach["name"] != "  魔鬼教练改名 " {
		t.Fatalf("名字应原样覆盖（trim 交给 writeRoleFile 的归一化）: %q", coach["name"])
	}
	if coach["enabled"] != false {
		t.Fatalf("enabled 应为 false: %v", coach["enabled"])
	}
	gm := coach["geom"].(map[string]any)
	if gm["sizePct"] != 55.0 {
		t.Fatalf("geom.sizePct 应为 55: %v", gm["sizePct"])
	}
	msgs := coach["messages"].(map[string]any)
	if msgs["order"] != "random" {
		t.Fatalf("messages.order 应为 random: %v", msgs["order"])
	}
	items := msgs["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["text"] != "站起来！" {
		t.Fatalf("台词应覆盖: %v", items)
	}
	// role_ai 的名字也覆盖了
	ai := readRoleJSON(t, root, "role_ai")
	if ai["name"] != "赛博改" {
		t.Fatalf("role_ai 名字应覆盖: %q", ai["name"])
	}
}

// TestMigrateLegacyRolesBuiltinDeleted 旧配置里没有的内置督休专员 → 连目录一起删
func TestMigrateLegacyRolesBuiltinDeleted(t *testing.T) {
	root := t.TempDir()
	seedBuiltinRoles(t, root)
	raw := map[string]any{
		"supervisor": map[string]any{
			// 只有 role_coach，没有 role_ai → role_ai 目录应被删
			"roles": []any{
				map[string]any{"key": "role_coach.png", "name": "体能教练", "enabled": true},
			},
		},
	}
	if !migrateLegacyRoles(raw, root) {
		t.Fatal("应发生迁移")
	}
	if isRegularFile(filepath.Join(root, "role_ai", "role.json")) {
		t.Fatal("role_ai 在旧配置中不存在，目录应被删除")
	}
	if !isRegularFile(filepath.Join(root, "role_coach", "role.json")) {
		t.Fatal("role_coach 目录应保留")
	}
}

// TestMigrateLegacyRolesCustomRole 自定义督休专员：按旧 key 建目录搬图
func TestMigrateLegacyRolesCustomRole(t *testing.T) {
	root := t.TempDir()
	seedBuiltinRoles(t, root)
	// 造一张「图片」文件（内容随意，createRoleDir 只读字节算摘要）
	imgPath := filepath.Join(t.TempDir(), "avatar.png")
	if err := os.WriteFile(imgPath, []byte("fake-png-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	raw := map[string]any{
		"supervisor": map[string]any{
			"roles": []any{
				map[string]any{"key": "custom-abcdef1234", "name": "我的督休专员", "enabled": true, "path": imgPath},
			},
			"fixedKey": "custom-abcdef1234",
		},
	}
	if !migrateLegacyRoles(raw, root) {
		t.Fatal("应发生迁移")
	}
	// createRoleDir 按 sha1 摘要算 key（custom-6820e4f877 = sha1("fake-png-bytes") 前 10 位），
	// 与旧 key 不同（旧 key 只是「是否已存在」的参考，落点以摘要目录为准，对齐 上一版）。
	dir := filepath.Join(root, "custom-6820e4f877")
	if !isRegularFile(filepath.Join(dir, "role.json")) {
		t.Fatal("自定义督休专员目录应建立在摘要 key 下")
	}
	cfg := readRoleJSON(t, root, "custom-6820e4f877")
	if cfg["name"] != "我的督休专员" {
		t.Fatalf("自定义督休专员名字应保留: %q", cfg["name"])
	}
}

// TestMigrateLegacyRolesFixedKeyRemap fixedKey 去 .png + 失效兜底
func TestMigrateLegacyRolesFixedKeyRemap(t *testing.T) {
	root := t.TempDir()
	seedBuiltinRoles(t, root)
	raw := map[string]any{
		"supervisor": map[string]any{
			"roles": []any{
				map[string]any{"key": "role_coach.png", "name": "体能教练", "enabled": true},
			},
			// fixedKey 带 .png 后缀 → 去掉；且指向存在的 role_coach
			"fixedKey": "role_coach.png",
		},
	}
	if !migrateLegacyRoles(raw, root) {
		t.Fatal("应发生迁移")
	}
	sup := raw["supervisor"].(map[string]any)
	if sup["fixedKey"] != "role_coach" {
		t.Fatalf("fixedKey 应去掉 .png: %q", sup["fixedKey"])
	}
	// 旧键全部清除
	for _, k := range []string{"roles", "geom", "messagesByRole", "rolesSeeded", "image", "builtin", "customs"} {
		if _, ok := sup[k]; ok {
			t.Fatalf("迁移后应删除旧键 %s", k)
		}
	}
	if sup["rolesMigrated"] != true {
		t.Fatal("应置 rolesMigrated=true")
	}
}

// TestMigrateLegacyRolesFixedKeyInvalid fixedKey 指向不存在的督休专员 → 兜底到第一个启用的
func TestMigrateLegacyRolesFixedKeyInvalid(t *testing.T) {
	root := t.TempDir()
	seedBuiltinRoles(t, root)
	raw := map[string]any{
		"supervisor": map[string]any{
			"roles": []any{
				map[string]any{"key": "role_coach.png", "name": "体能教练", "enabled": true},
				map[string]any{"key": "role_ai", "name": "赛博监考官", "enabled": true},
			},
			// 指向一个迁移后不存在的督休专员
			"fixedKey": "role_mentor",
		},
	}
	if !migrateLegacyRoles(raw, root) {
		t.Fatal("应发生迁移")
	}
	sup := raw["supervisor"].(map[string]any)
	fixed := sup["fixedKey"].(string)
	// role_mentor 不存在（也没在旧 roles 里），兜底到第一个启用的内置督休专员（role_coach，清单序）
	if fixed != "role_coach" {
		t.Fatalf("fixedKey 失效应兜底到第一个启用督休专员 role_coach: %q", fixed)
	}
}

// TestMigrateLegacyRolesNoSupervisor supervisor 缺失 → 直接 false
func TestMigrateLegacyRolesNoSupervisor(t *testing.T) {
	root := t.TempDir()
	if migrateLegacyRoles(map[string]any{}, root) {
		t.Fatal("无 supervisor 应返回 false")
	}
}
