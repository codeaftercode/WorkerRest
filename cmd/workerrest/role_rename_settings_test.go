package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"workerrest/internal/settings"
)

// 改名 = 换身份：配置里指向旧名的两处（fixedRole / order）必须跟着换。
//
// 不换的后果不是“配置难看”，而是**选错了人**：固定模式按 fixedRole 反查不到，
// 只能兜底取列表第一个 —— 用户改完名会发现“我固定显示的那个人被换掉了”；
// order 里留下旧名则是一个永远匹配不上的僵尸键，配置随改名次数越积越大。
// 设置页 renameRole 在前端做同一件事（S 里那两处），两边一致才不会互相覆盖。
func TestSaveRoleRenameUpdatesSettingsRefs(t *testing.T) {
	root := t.TempDir()
	writeRoleDir(t, root, "医生", map[string]any{
		"name": "医生", "enabled": true, "image": "figure.webp",
	}, map[string]string{"figure.webp": "w"})
	writeRoleDir(t, root, "学姐", map[string]any{
		"name": "学姐", "enabled": true, "image": "figure.webp",
	}, map[string]string{"figure.webp": "w"})

	cfgPath := filepath.Join(t.TempDir(), "settings.json")
	cfg := settings.New(map[string]any{
		"supervisor": map[string]any{
			"enabled":   true,
			"selection": "fixed",
			"fixedRole": "医生",
			"order":     []any{"医生", "学姐"},
		},
	}, cfgPath)
	a := &App{rolesRoot: root, cfg: cfg}

	if !a.SaveRole("医生", map[string]any{"name": "值班医生"}) {
		t.Fatal("改名应成功")
	}

	sup, _ := a.cfg.Raw()["supervisor"].(map[string]any)
	if got, _ := sup["fixedRole"].(string); got != "值班医生" {
		t.Fatalf("fixedRole 应跟着改名：%q", got)
	}
	order, _ := sup["order"].([]any)
	if len(order) != 2 || order[0] != "值班医生" || order[1] != "学姐" {
		t.Fatalf("order 里的旧名应换成新名、且位置不变：%v", order)
	}

	// 真落盘了，不只是改了内存
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("配置文件应已写出：%v", err)
	}
	var disk map[string]any
	if err := json.Unmarshal(b, &disk); err != nil {
		t.Fatalf("配置文件不是合法 JSON：%v", err)
	}
	dsup, _ := disk["supervisor"].(map[string]any)
	if got, _ := dsup["fixedRole"].(string); got != "值班医生" {
		t.Fatalf("磁盘上的 fixedRole 应跟着改名：%q", got)
	}

	// 名字没变时不碰配置（零副作用），也别把 fixedRole 改坏
	before := string(b)
	if !a.SaveRole("值班医生", map[string]any{"name": "值班医生"}) {
		t.Fatal("提交同名应放行")
	}
	after, _ := os.ReadFile(cfgPath)
	if string(after) != before {
		t.Fatalf("名字没变时不该改写配置文件\n前：%s\n后：%s", before, string(after))
	}
}
