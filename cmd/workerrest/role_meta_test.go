package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"workerrest/internal/role"
)

// 配置文件命名为 meta.json（2026-10-28 由 role.json 改名）的回归。
//
// 软件尚未发布、没有老用户，所以**不做旧名兼容**：扫不到 role.json、也不会就地改名。
// 下面两条钉子守住这个决定 —— 谁哪天顺手把兼容加回来，测试会先拦住。

// 仓库自带的 supervisors/ 里不许出现旧名：改了名还有人按老习惯放 role.json，
// 会变成「文件在那儿、软件永远不读」的隐形坑。
func TestRepoSupervisorsUseMetaName(t *testing.T) {
	root := repoRoot(t)
	sup := filepath.Join(root, "assets", "supervisors")
	entries, err := os.ReadDir(sup)
	if err != nil {
		t.Fatalf("读不到 supervisors/：%v", err)
	}
	dirs, stale := 0, []string{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(sup, e.Name())
		if role.IsRegularFile(filepath.Join(dir, "meta.json")) {
			dirs++
		}
		if role.IsRegularFile(filepath.Join(dir, "role.json")) {
			stale = append(stale, e.Name())
		}
	}
	if len(stale) > 0 {
		t.Fatalf("这些目录里还有 role.json（应改名为 meta.json）：%v", stale)
	}
	if dirs < 10 {
		t.Fatalf("只有 %d 个目录带 meta.json，明显偏少（应该十几款）", dirs)
	}
}

// 只有 role.json、没有 meta.json 的目录**不算督休专员**：既不列出、也不改名。
func TestLegacyRoleJSONIsIgnored(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "老专员")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"key": "custom-old", "name": "老专员", "enabled": true})
	if err := os.WriteFile(filepath.Join(dir, "role.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	if roles := role.ScanRoles(root); len(roles) != 0 {
		t.Fatalf("不该扫到只有 role.json 的目录，实际 %v", roleNames(roles))
	}
	if _, ok := role.ResolveRoleDir(root, "custom-old"); ok {
		t.Fatal("不该按 key 反查到只有 role.json 的目录")
	}
	if role.IsRegularFile(filepath.Join(dir, "meta.json")) {
		t.Fatal("不该替用户改名：软件不做旧名迁移")
	}
	if !role.IsRegularFile(filepath.Join(dir, "role.json")) {
		t.Fatal("不该动用户的 role.json")
	}
}

// 仓库自带的 meta.json 里若写了 fixedId，必须指向真实存在的台词 id。
//
// 为什么值得钉死：id 会被 role.NormalizeRoleMessages 重编号，fixedId 靠 remapMsgID 跟着搬。
// 一旦漏搬（或手改时写错号），「固定显示」模式会指向一条不存在的台词 —— 提醒窗口里
// 没有台词，日志也不报错，只能靠肉眼发现。
func TestRepoSupFixedIDResolves(t *testing.T) {
	root := repoRoot(t)
	sup := filepath.Join(root, "assets", "supervisors")
	entries, err := os.ReadDir(sup)
	if err != nil {
		t.Fatalf("读不到 supervisors/：%v", err)
	}
	checked := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(sup, e.Name(), "meta.json")
		if !role.IsRegularFile(path) {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读 %s 失败：%v", path, err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("解析 %s 失败：%v", path, err)
		}
		cfg := role.NormalizeRoleFile(m)
		if cfg.Messages.FixedID == "" {
			continue
		}
		checked++
		ok := false
		for _, it := range cfg.Messages.Items {
			if it.ID == cfg.Messages.FixedID {
				ok = true
				break
			}
		}
		if !ok {
			t.Errorf("%s 的 fixedId=%q 对不上任何台词 id（有 %d 条）",
				path, cfg.Messages.FixedID, len(cfg.Messages.Items))
		}
	}
	if checked == 0 {
		t.Fatal("一份 meta.json 都没检查到 fixedId，测试没意义（路径或字段名变了？）")
	}
}

// 导入立绘（role.CreateRoleDir → role.WriteRoleFile）只生成 meta.json。
func TestCreateRoleDirWritesMetaName(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(t.TempDir(), "猫.png")
	if err := os.WriteFile(src, []byte("fake-png"), 0o644); err != nil {
		t.Fatal(err)
	}
	rolesRoot := filepath.Join(root, "assets", "supervisors")
	if err := os.MkdirAll(rolesRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	name := role.CreateRoleDir(rolesRoot, "小花猫", src)
	if name == "" {
		t.Fatal("role.CreateRoleDir 失败")
	}
	dir, ok := role.ResolveRoleDir(rolesRoot, name)
	if !ok {
		t.Fatal("应能按 key 反查到目录")
	}
	if !role.IsRegularFile(filepath.Join(dir, "meta.json")) {
		t.Fatal("导入应生成 meta.json")
	}
	if _, err := os.Stat(filepath.Join(dir, "role.json")); err == nil {
		t.Fatal("导入不该再生成 role.json")
	}
	if got, ok := role.ReadRoleFile(rolesRoot, name); !ok || got.Name != "小花猫" {
		t.Fatalf("读回应为 小花猫，实际 %+v ok=%v", got, ok)
	}
}
