package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 导入 / 删除自设立绘的回归（2026-10-26 起；2026-10-28 改成「名字即身份」后重写）。
//
// 背景：立绘 = supervisors/<专员名>/(meta.json + 立绘 + 预览图)，**没有内部 id**。
// 所以「同一张图导入两次」必须得到两个不同的名字（我的图 / 我的图 (2)）与两个独立目录 ——
// 名字重复意味着 fixedRole / order / 台词游标会指向两个人，删一个还会删错。

// writeImgSrc 造一张源图片文件，返回路径
func writeImgSrc(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// 同一张图导入两次 → 两个不同的名字、两个各自独立的目录，各自都能查到自己的立绘。
func TestCreateRoleDirSameImageGetsDistinctNames(t *testing.T) {
	root := t.TempDir()
	src := writeImgSrc(t, t.TempDir(), "我的图.png", "same-bytes")

	n1 := createRoleDir(root, "我的图", src)
	n2 := createRoleDir(root, "我的图", src)
	if n1 == "" || n2 == "" {
		t.Fatalf("导入应成功：n1=%q n2=%q", n1, n2)
	}
	if n1 == n2 {
		t.Fatalf("同一张图导入两次必须给不同名字（否则就是同一个人）：%q", n1)
	}
	d1, ok1 := resolveRoleDir(root, n1)
	d2, ok2 := resolveRoleDir(root, n2)
	if !ok1 || !ok2 {
		t.Fatalf("两个名字都应能反查到目录：ok1=%v ok2=%v", ok1, ok2)
	}
	if d1 == d2 {
		t.Fatalf("两个名字不该指向同一个目录：%q", d1)
	}
	roles := scanRoles(root)
	if len(roles) != 2 {
		t.Fatalf("扫描应得到 2 个督休专员，实际 %d 个", len(roles))
	}
	for _, r := range roles {
		if r.FigurePath == "" {
			t.Fatalf("督休专员 %s 没有立绘路径（FigurePath 为空 → 永远出不了场）", r.Name)
		}
	}
}

// 删掉其中一个，另一个必须完好。
func TestRemoveOneDuplicateKeepsTheOther(t *testing.T) {
	root := t.TempDir()
	src := writeImgSrc(t, t.TempDir(), "我的图.png", "same-bytes")
	n1 := createRoleDir(root, "我的图", src)
	n2 := createRoleDir(root, "我的图", src)

	if !removeRoleDir(root, n2) {
		t.Fatal("删除第二个应成功")
	}
	roles := scanRoles(root)
	if len(roles) != 1 || roles[0].Name != n1 {
		t.Fatalf("删掉第二个后应只剩 %s，实际 %v", n1, roleNames(roles))
	}
	root2 := t.TempDir()
	a1 := createRoleDir(root2, "我的图", src)
	a2 := createRoleDir(root2, "我的图", src)
	if !removeRoleDir(root2, a1) {
		t.Fatal("删除第一个应成功")
	}
	roles = scanRoles(root2)
	if len(roles) != 1 || roles[0].Name != a2 {
		t.Fatalf("删掉第一个后应只剩 %s，实际 %v", a2, roleNames(roles))
	}
}

// 命名对话框点「取消」时的清理只能删掉**本次新建**的那个，不能误删之前导入成功的。
func TestCancelNamingKeepsPreviouslyImported(t *testing.T) {
	root := t.TempDir()
	src := writeImgSrc(t, t.TempDir(), "我的图.png", "same-bytes")
	n1 := createRoleDir(root, "我的图", src)
	n2 := createRoleDir(root, "我的图", src)

	// 前端取消分支：removeSupervisorRole(picked.saved)
	if !removeRoleDir(root, n2) {
		t.Fatal("取消时的清理应成功")
	}
	roles := scanRoles(root)
	if len(roles) != 1 || roles[0].Name != n1 {
		t.Fatalf("取消导入不应误删之前那个：期望只剩 %s，实际 %v", n1, roleNames(roles))
	}
}

// 不支持的图片格式必须直接拒绝（不建目录）——
// 建出 figure.bmp 的话读取侧 isImageName 不认 .bmp →
// scanRoles 里 image 回落成默认 figure.webp、FigurePath 为空 →
// 该督休专员**永远出不了场**（buildSupervisorSessionWith 过滤 FigurePath == ""）。
func TestCreateRoleDirRejectsUnsupportedExt(t *testing.T) {
	root := t.TempDir()
	src := writeImgSrc(t, t.TempDir(), "图.bmp", "bmp-bytes")

	if name := createRoleDir(root, "图", src); name != "" {
		t.Fatalf("不支持的扩展名应被拒绝，实际返回 name=%q", name)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("拒绝后不该留下任何目录，实际 %d 个", len(entries))
	}
	// 支持的格式照常能建
	ok := writeImgSrc(t, t.TempDir(), "图.png", "png-bytes")
	if name := createRoleDir(root, "图", ok); name == "" {
		t.Fatal("png 应能正常导入")
	}
}

// resolveRoleDir 按 meta.json 的 name 反查，**不看目录名**：
// 用户手改了 name（或目录名被系统清洗过）也必须能找到。
func TestResolveRoleDirFollowsMetaName(t *testing.T) {
	root := t.TempDir()
	writeRoleDir(t, root, "旧目录名", map[string]any{
		"name": "新名字", "enabled": true, "image": "figure.webp",
	}, map[string]string{"figure.webp": "w"})
	writeRoleDir(t, root, "另一个", map[string]any{
		"name": "另一个", "enabled": true, "image": "figure.webp",
	}, map[string]string{"figure.webp": "w"})

	dir, ok := resolveRoleDir(root, "新名字")
	if !ok {
		t.Fatal("应按 meta.json 的 name 反查到目录")
	}
	if filepath.Base(dir) != "旧目录名" {
		t.Fatalf("命中的目录不对：%q", filepath.Base(dir))
	}
	if _, ok := resolveRoleDir(root, "旧目录名"); ok {
		t.Fatal("目录名不是身份，不该按它反查到")
	}
}

// meta.json 缺 name（用户手放的空壳）→ 用目录名当名字，与 scanRoles 口径一致。
func TestResolveRoleDirFallsBackToDirName(t *testing.T) {
	root := t.TempDir()
	writeRoleDir(t, root, "手放专员", map[string]any{
		"enabled": true, "image": "figure.webp",
	}, map[string]string{"figure.webp": "w"})

	dir, ok := resolveRoleDir(root, "手放专员")
	if !ok || filepath.Base(dir) != "手放专员" {
		t.Fatalf("缺 name 时应按目录名反查，实际 %q ok=%v", filepath.Base(dir), ok)
	}
	roles := scanRoles(root)
	if len(roles) != 1 || roles[0].Name != "手放专员" {
		t.Fatalf("缺 name 时应回落成目录名，实际 %v", roleNames(roles))
	}
}

// 删除督休专员后，配置里指向它的 fixedRole 与 order 都要清干净。
//
// fixedRole 指向不存在的名字 → 固定模式下休息窗口取不到人；
// order 里的陈旧名字 → 配置随删随涨。
func TestRemoveRoleClearsFixedRoleAndOrder(t *testing.T) {
	rolesRoot := t.TempDir()
	cfgPath := filepath.Join(t.TempDir(), "settings.json")
	t.Setenv("WR_SETTINGS_PATH", cfgPath)

	writeRoleDir(t, rolesRoot, "甲", map[string]any{
		"name": "甲", "enabled": true, "image": "figure.webp",
	}, map[string]string{"figure.webp": "w"})
	writeRoleDir(t, rolesRoot, "乙", map[string]any{
		"name": "乙", "enabled": true, "image": "figure.webp",
	}, map[string]string{"figure.webp": "w"})

	a := &App{
		cfg: &Settings{raw: map[string]any{
			"supervisor": map[string]any{
				"enabled":   true,
				"fixedRole": "乙",
				"order":     []any{"乙", "甲"},
			},
		}},
		rolesRoot: rolesRoot,
	}

	if !a.RemoveRole("乙") {
		t.Fatal("RemoveRole 应成功")
	}
	if _, ok := resolveRoleDir(rolesRoot, "乙"); ok {
		t.Fatal("乙 的目录应已被删除")
	}
	sup := a.cfg.Raw()["supervisor"].(map[string]any)
	if s, _ := sup["fixedRole"].(string); s == "乙" {
		t.Fatalf("fixedRole 仍指向已删除的 乙：%q", s)
	}
	order, _ := sup["order"].([]any)
	for _, v := range order {
		if s, _ := v.(string); s == "乙" {
			t.Fatalf("order 里仍留着已删除的 乙：%v", order)
		}
	}
	if len(order) != 1 {
		t.Fatalf("order 应只剩 甲，实际 %v", order)
	}
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("配置应已落盘：%v", err)
	}
	if strings.Contains(string(b), "乙") {
		t.Fatalf("落盘配置里仍出现 乙：%s", b)
	}
}

// 换图写失败时，**旧立绘必须还在**。
//
// 反例（修复前）：replaceRoleImage 先把 figure.* / preview.* 全删了再写新的，
// 写失败就落进「既没有立绘也没有预览图」的状态 —— FigurePath 为空 →
// 这个督休专员永远出不了场，而 meta.json 还指着已经不存在的旧文件名。
// 模拟写失败的办法：在目标位置放一个**同名目录**，让改名落不下去。
func TestReplaceRoleImageKeepsOldImageOnWriteFailure(t *testing.T) {
	root := t.TempDir()
	writeRoleDir(t, root, "督休专员", map[string]any{
		"name": "督休专员", "enabled": true,
		"image": "figure.png", "preview": "preview.png",
	}, map[string]string{"figure.png": "old-figure", "preview.png": "old-preview"})

	dir := filepath.Join(root, "督休专员")
	// 让新图（figure.webp）的落盘失败：在目标位置放一个**非空目录**占位。
	// 必须非空 —— 空目录会被 os.Remove 顺手删掉（旧代码「先删后写」那一步），
	// 注入就失效了；非空目录删不掉，写入必然失败。
	if err := os.MkdirAll(filepath.Join(dir, "figure.webp", "占位"), 0o755); err != nil {
		t.Fatal(err)
	}
	src := writeImgSrc(t, t.TempDir(), "新图.webp", "new-bytes")

	if replaceRoleImage(root, "督休专员", src) {
		t.Fatal("写入失败时 replaceRoleImage 应返回 false")
	}
	for _, name := range []string{"figure.png", "preview.png"} {
		p := filepath.Join(dir, name)
		if !isRegularFile(p) {
			t.Fatalf("旧文件 %s 不该被毁掉（写失败应保持原样）", name)
		}
		b, _ := os.ReadFile(p)
		if string(b) != "old-"+strings.TrimSuffix(name, ".png") {
			t.Fatalf("%s 内容被改了：%q", name, b)
		}
	}
}

// 导入失败不能留下半成品目录：没有 meta.json 的目录会被 scanRoles 跳过 ——
// 用户在界面上看不见它，它却占着「名字」，下一次导入就变成 "名字 (2)" 了。
func TestCreateRoleDirLeavesNoLitterOnFailure(t *testing.T) {
	root1 := t.TempDir()
	bad := writeImgSrc(t, t.TempDir(), "图.bmp", "bmp-bytes")
	if name := createRoleDir(root1, "图", bad); name != "" {
		t.Fatalf("不支持的格式应被拒绝，实际 name=%q", name)
	}
	if entries, _ := os.ReadDir(root1); len(entries) != 0 {
		t.Fatalf("失败后不该留下目录，实际 %d 个", len(entries))
	}

	root2 := t.TempDir()
	big := filepath.Join(t.TempDir(), "大图.png")
	if err := os.WriteFile(big, make([]byte, 21*1024*1024), 0o644); err != nil {
		t.Fatal(err)
	}
	if name := createRoleDir(root2, "大图", big); name != "" {
		t.Fatalf("超大的文件应被拒绝，实际 name=%q", name)
	}
	if entries, _ := os.ReadDir(root2); len(entries) != 0 {
		t.Fatalf("失败后不该留下目录，实际 %d 个", len(entries))
	}

	// 正常路径：立绘 + meta 齐备，且只扫得到 1 个督休专员
	root3 := t.TempDir()
	ok := writeImgSrc(t, t.TempDir(), "图.png", "png-bytes")
	name := createRoleDir(root3, "图", ok)
	if name == "" {
		t.Fatal("正常导入应成功")
	}
	dir, ok2 := resolveRoleDir(root3, name)
	if !ok2 {
		t.Fatal("应能按名字反查到目录")
	}
	for _, f := range []string{"figure.png", "meta.json"} {
		if !isRegularFile(filepath.Join(dir, f)) {
			t.Fatalf("目录里应该有 %s", f)
		}
	}
	// 不再复制一份 preview 当缩略图（那是白占磁盘、还会和立绘脱钩）：缩略图由渲染层生成
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), rolePreviewBase) {
				t.Fatalf("导入后不该出现预览图副本，实际有 %s", e.Name())
			}
		}
	}
	if n := len(scanRoles(root3)); n != 1 {
		t.Fatalf("应只扫到 1 个督休专员，实际 %d 个", n)
	}
}
