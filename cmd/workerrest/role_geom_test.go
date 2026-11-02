package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"workerrest/internal/model"
	"workerrest/internal/role"
)

// 内置督休专员统一几何的回归（2026-10-28 用户要求）。
//
// 需求原话：所有督休专员的立绘高度为 80%、距窗口左侧 5%；打包发布、恢复默认配置时
// 都是这一组值；导入自定义立绘时默认也是这一组值。
//
// 所以这条测试同时钉三件事：
//  1. 默认值本身（Go 侧 role.SupGeomDefault —— 「恢复默认」与「导入自定义立绘」都走它）
//  2. 仓库里每个内置 meta.json 的 geom 与默认值一致（打包发布出去的就是这些文件）
//  3. 前端几处 fallback 常量不能跟 Go 侧劈叉（读不到 geom 时显示成另一套比例）
//
// 只比 sizePct / posXPct / posYPct：flip 是每个专员各自的美术设定（温柔学姐、魔法师
// 本来就翻转），不属于统一项。
func TestBuiltinRoleGeomDefaults(t *testing.T) {
	want := model.SupGeom{SizePct: 80, PosXPct: 5, PosYPct: 0, Flip: false}
	if role.SupGeomDefault != want {
		t.Fatalf("role.SupGeomDefault 应为 %+v，得到 %+v", want, role.SupGeomDefault)
	}

	root := repoRoot(t)
	entries, err := os.ReadDir(filepath.Join(root, "assets", "supervisors"))
	if err != nil {
		t.Fatalf("读不到 supervisors/：%v", err)
	}
	checked := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(root, "assets", "supervisors", e.Name(), "meta.json")
		raw, err := os.ReadFile(path)
		if err != nil {
			continue // 素材目录（没有 meta.json）本来就该跳过
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("%s 解析失败：%v", path, err)
		}
		geom, _ := m["geom"].(map[string]any)
		got := role.NormalizeSupGeom(geom)
		if got.SizePct != want.SizePct || got.PosXPct != want.PosXPct || got.PosYPct != want.PosYPct {
			t.Errorf("%s 的 geom 应为 高度 %v%% / 距左 %v%% / 距底 %v%%，得到 高度 %v%% / 距左 %v%% / 距底 %v%%",
				path, want.SizePct, want.PosXPct, want.PosYPct, got.SizePct, got.PosXPct, got.PosYPct)
		}
		checked++
	}
	// 防「目录改名 / 结构变动后这条测试空跑一遍就算过」
	if checked < 10 {
		t.Fatalf("只检查到 %d 个内置专员，明显偏少（应该十几款）", checked)
	}

	// 前端三处 fallback 常量：读不到 geom 时按它显示，跟 Go 侧劈叉就会「换台机器
	// 立绘突然小一圈」。这四处是同一组数字的手工副本，只能靠测试钉住。
	dist := filepath.Join(root, "cmd", "workerrest", "web", "dist")
	for _, c := range []struct{ file, decl string }{
		{"settings.js", "SUP_GEOM_DEFAULT"},
		{"break.js", "GEOM_FALLBACK"},
		{"takeout.js", "GEOM_FALLBACK"},
	} {
		body, err := os.ReadFile(filepath.Join(dist, c.file))
		if err != nil {
			t.Errorf("读不到 %s：%v", c.file, err)
			continue
		}
		line := ""
		for _, l := range strings.Split(string(body), "\n") {
			if strings.Contains(l, "var "+c.decl+" = {") {
				line = l
				break
			}
		}
		if line == "" {
			t.Errorf("%s 里找不到 var %s = {…} 声明", c.file, c.decl)
			continue
		}
		if !strings.Contains(line, "sizePct: 80") || !strings.Contains(line, "posXPct: 5") {
			t.Errorf("%s 的 %s 应与 Go 侧同值（sizePct: 80 / posXPct: 5），实际是：%s",
				c.file, c.decl, strings.TrimSpace(line))
		}
	}
}

// 导入自定义立绘：新建的 meta.json 直接用默认几何（80 / 5 / 0），不是别的数。
// 走真实的 role.CreateRoleDir（导入按钮背后那条路），不是只读 role.SupGeomDefault 变量。
func TestImportedRoleUsesDefaultGeom(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "cat.png")
	if err := os.WriteFile(src, []byte("fake-png-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	rolesRoot := filepath.Join(root, "assets", "supervisors")
	if err := os.MkdirAll(rolesRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	name := role.CreateRoleDir(rolesRoot, "小花猫", src)
	if name == "" {
		t.Fatal("role.CreateRoleDir 失败，导入路径本身就有问题")
	}
	cfg, ok := role.ReadRoleFile(rolesRoot, name)
	if !ok {
		t.Fatal("读不回刚导入的督休专员")
	}
	if cfg.Geom.SizePct != 80 || cfg.Geom.PosXPct != 5 || cfg.Geom.PosYPct != 0 {
		t.Fatalf("导入自定义立绘的默认几何应为 高度 80%% / 距左 5%% / 距底 0%%，得到 高度 %v%% / 距左 %v%% / 距底 %v%%",
			cfg.Geom.SizePct, cfg.Geom.PosXPct, cfg.Geom.PosYPct)
	}
}
