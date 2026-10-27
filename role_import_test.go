package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 导入 / 删除自设立绘的回归（2026-10-26）。
//
// 背景：立绘 = supervisors/<专员名称>/ 一个目录，身份 key 存 role.json。key 的基底是
// **图片内容摘要**（同一张图 → 同一个 key），而目录名是按名称去重的（同名 → "名字 (2)"）。
// 两者放在一起会打破「key ↔ 目录」的一一对应，而所有读写都按 key 反查**第一个**命中，
// 于是会冒出一串「僵尸目录」式的错位。这里逐条钉住。

// writeImgSrc 造一张源图片文件，返回路径
func writeImgSrc(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// 同一张图导入两次 → 必须拿到**两个不同的 key**、两个各自独立的目录。
//
// 反例（修复前）：key 都是 custom-<摘要>，两个目录共用一个 key → resolveRoleDir 只命中
// 第一个 → 改名 / 换图打到第一个、删除只删一个、另一个残留到重启又冒出来。
func TestCreateRoleDirSameImageGetsDistinctKeys(t *testing.T) {
	root := t.TempDir()
	src := writeImgSrc(t, t.TempDir(), "我的图.png", "same-bytes")

	k1 := createRoleDir(root, "我的图", src)
	k2 := createRoleDir(root, "我的图", src)
	if k1 == "" || k2 == "" {
		t.Fatalf("导入应成功：k1=%q k2=%q", k1, k2)
	}
	if k1 == k2 {
		t.Fatalf("同一张图导入两次必须给不同 key（否则两个目录共用一个身份）：k1=k2=%q", k1)
	}
	// 两个 key 都要能各自反查到自己的目录
	d1, ok1 := resolveRoleDir(root, k1)
	d2, ok2 := resolveRoleDir(root, k2)
	if !ok1 || !ok2 {
		t.Fatalf("两个 key 都应能反查到目录：ok1=%v ok2=%v", ok1, ok2)
	}
	if d1 == d2 {
		t.Fatalf("两个 key 不该指向同一个目录：%q", d1)
	}
	// 扫描结果里两个都在，且各带各的立绘路径
	roles := scanRoles(root)
	if len(roles) != 2 {
		t.Fatalf("扫描应得到 2 个督休专员，实际 %d 个", len(roles))
	}
	for _, r := range roles {
		if r.FigurePath == "" {
			t.Fatalf("督休专员 %s 没有立绘路径（FigurePath 为空 → 永远出不了场）", r.Key)
		}
	}
}

// 删掉其中一个，另一个必须完好 —— 修复前会连带删错 / 留下残留。
func TestRemoveOneDuplicateKeepsTheOther(t *testing.T) {
	root := t.TempDir()
	src := writeImgSrc(t, t.TempDir(), "我的图.png", "same-bytes")
	k1 := createRoleDir(root, "我的图", src)
	k2 := createRoleDir(root, "我的图", src)

	if !removeRoleDir(root, k2) {
		t.Fatal("删除第二个应成功")
	}
	roles := scanRoles(root)
	if len(roles) != 1 || roles[0].Key != k1 {
		t.Fatalf("删掉第二个后应只剩 %s，实际 %v", k1, roleKeys(roles))
	}
	// 反过来删第一个，第二个也不能被误伤
	root2 := t.TempDir()
	a1 := createRoleDir(root2, "我的图", src)
	a2 := createRoleDir(root2, "我的图", src)
	if !removeRoleDir(root2, a1) {
		t.Fatal("删除第一个应成功")
	}
	roles = scanRoles(root2)
	if len(roles) != 1 || roles[0].Key != a2 {
		t.Fatalf("删掉第一个后应只剩 %s，实际 %v", a2, roleKeys(roles))
	}
}

// 命名对话框点「取消」时的清理只能删掉**本次新建**的那个，不能误删之前导入成功的。
func TestCancelNamingKeepsPreviouslyImported(t *testing.T) {
	root := t.TempDir()
	src := writeImgSrc(t, t.TempDir(), "我的图.png", "same-bytes")
	k1 := createRoleDir(root, "我的图", src)
	k2 := createRoleDir(root, "我的图", src)

	// 前端取消分支：removeSupervisorRole(picked.key)
	if !removeRoleDir(root, k2) {
		t.Fatal("取消时的清理应成功")
	}
	roles := scanRoles(root)
	if len(roles) != 1 || roles[0].Key != k1 {
		t.Fatalf("取消导入不应误删之前那个：期望只剩 %s，实际 %v", k1, roleKeys(roles))
	}
}

// 不支持的图片格式必须直接拒绝（不建目录）——
// 修复前会建出 figure.bmp，而读取侧的 isImageName 不认 .bmp →
// scanRoles 里 image 回落成默认 figure.webp、FigurePath 为空 →
// 该督休专员**永远出不了场**（buildSupervisorSessionWith 过滤 FigurePath == ""）。
// 迁移路径的 src 来自旧配置里的任意路径，不受选图对话框的过滤器约束。
func TestCreateRoleDirRejectsUnsupportedExt(t *testing.T) {
	root := t.TempDir()
	src := writeImgSrc(t, t.TempDir(), "图.bmp", "bmp-bytes")

	if key := createRoleDir(root, "图", src); key != "" {
		t.Fatalf("不支持的扩展名应被拒绝，实际返回 key=%q", key)
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
	if key := createRoleDir(root, "图", ok); key == "" {
		t.Fatal("png 应能正常导入")
	}
}

// resolveRoleDir 的「目录名即 key」兜底**只**适用于 role.json 没有 key 字段的旧部署。
//
// 反例（修复前）：用户把某个督休专员命名成 "supervisor_default"（目录名 = 内置专员的 key），
// 按 key 反查会命中这个目录 → 内置专员的改名 / 换图 / 删除会打到**别人的目录**。
func TestResolveRoleDirDoesNotHijackByDirName(t *testing.T) {
	root := t.TempDir()
	// 目录名 = "supervisor_default"，但它自己的 key 是 custom-abc
	writeRoleDir(t, root, "supervisor_default", map[string]any{
		"key": "custom-abc", "name": "冒充内置", "enabled": true,
		"image": "figure.webp",
	}, map[string]string{"figure.webp": "w"})
	// 真正的内置督休专员
	writeRoleDir(t, root, "眼镜主管", map[string]any{
		"key": "supervisor_default", "name": "眼镜主管", "enabled": true,
		"image": "figure.webp",
	}, map[string]string{"figure.webp": "w"})

	dir, ok := resolveRoleDir(root, "supervisor_default")
	if !ok {
		t.Fatal("内置督休专员应能按 key 反查到")
	}
	if filepath.Base(dir) != "眼镜主管" {
		t.Fatalf("按 key 反查必须命中真正拥有该 key 的目录，实际命中 %q", filepath.Base(dir))
	}
	// 反过来：查 custom-abc 只能命中那个目录
	dir2, ok2 := resolveRoleDir(root, "custom-abc")
	if !ok2 || filepath.Base(dir2) != "supervisor_default" {
		t.Fatalf("custom-abc 应命中「supervisor_default」目录，实际 %q ok=%v", filepath.Base(dir2), ok2)
	}
}

// 旧部署（role.json 没有 key 字段）仍按目录名反查 —— 不能把兼容性改坏。
func TestResolveRoleDirLegacyDirNameFallback(t *testing.T) {
	root := t.TempDir()
	writeRoleDir(t, root, "老自定义", map[string]any{
		"name": "老自定义", "enabled": true, "image": "figure.webp",
	}, map[string]string{"figure.webp": "w"})

	dir, ok := resolveRoleDir(root, "老自定义")
	if !ok || filepath.Base(dir) != "老自定义" {
		t.Fatalf("旧部署应仍能按目录名反查，实际 %q ok=%v", filepath.Base(dir), ok)
	}
	// scanRoles 给它的 key 也是目录名
	roles := scanRoles(root)
	if len(roles) != 1 || roles[0].Key != "老自定义" {
		t.Fatalf("旧部署的 key 应回落成目录名，实际 %v", roleKeys(roles))
	}
}

// 删除督休专员后，配置里指向它的 fixedKey 与 order 都要清干净。
//
// fixedKey 指向不存在的 key → 固定模式下休息窗口取不到人；
// order 里的陈旧 key → 配置随删随涨，且用户重新导入同一张图时它会「回到原来的位置」。
func TestRemoveRoleClearsFixedKeyAndOrder(t *testing.T) {
	rolesRoot := t.TempDir()
	cfgPath := filepath.Join(t.TempDir(), "settings.json")
	t.Setenv("WR_SETTINGS_PATH", cfgPath)

	// 铺两个督休专员
	writeRoleDir(t, rolesRoot, "甲", map[string]any{
		"key": "role_a", "name": "甲", "enabled": true, "image": "figure.webp",
	}, map[string]string{"figure.webp": "w"})
	writeRoleDir(t, rolesRoot, "乙", map[string]any{
		"key": "role_b", "name": "乙", "enabled": true, "image": "figure.webp",
	}, map[string]string{"figure.webp": "w"})

	a := &App{
		cfg: &Settings{raw: map[string]any{
			"supervisor": map[string]any{
				"enabled":  true,
				"fixedKey": "role_b",
				"order":    []any{"role_b", "role_a"},
			},
		}},
		rolesRoot: rolesRoot,
	}

	if !a.RemoveRole("role_b") {
		t.Fatal("RemoveRole 应成功")
	}
	// 目录真删了
	if _, ok := resolveRoleDir(rolesRoot, "role_b"); ok {
		t.Fatal("role_b 的目录应已被删除")
	}
	// 配置里的 fixedKey / order 都清了
	sup := a.cfg.Raw()["supervisor"].(map[string]any)
	if s, _ := sup["fixedKey"].(string); s == "role_b" {
		t.Fatalf("fixedKey 仍指向已删除的 role_b：%q", s)
	}
	order, _ := sup["order"].([]any)
	for _, v := range order {
		if s, _ := v.(string); s == "role_b" {
			t.Fatalf("order 里仍留着已删除的 role_b：%v", order)
		}
	}
	if len(order) != 1 {
		t.Fatalf("order 应只剩 role_a，实际 %v", order)
	}
	// 落盘的那份也一致
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("配置应已落盘：%v", err)
	}
	if strings.Contains(string(b), "role_b") {
		t.Fatalf("落盘配置里仍出现 role_b：%s", b)
	}
}

// 换图写失败时，**旧立绘必须还在**。
//
// 反例（修复前）：replaceRoleImage 先把 figure.* / preview.* 全删了再写新的，
// 写失败就落进「既没有立绘也没有预览图」的状态 —— FigurePath 为空 →
// 这个督休专员永远出不了场，而 role.json 还指着已经不存在的旧文件名。
// 模拟写失败的办法：在目标位置放一个**同名目录**，让改名落不下去。
func TestReplaceRoleImageKeepsOldImageOnWriteFailure(t *testing.T) {
	root := t.TempDir()
	writeRoleDir(t, root, "角色", map[string]any{
		"key": "role_x", "name": "角色", "enabled": true,
		"image": "figure.png", "preview": "preview.png",
	}, map[string]string{"figure.png": "old-figure", "preview.png": "old-preview"})

	dir := filepath.Join(root, "角色")
	// 让新图（figure.webp）的落盘失败：在目标位置放一个**非空目录**占位。
	// 必须非空 —— 空目录会被 os.Remove 顺手删掉（旧代码「先删后写」那一步），
	// 注入就失效了；非空目录删不掉，写入必然失败。
	if err := os.MkdirAll(filepath.Join(dir, "figure.webp", "占位"), 0o755); err != nil {
		t.Fatal(err)
	}
	src := writeImgSrc(t, t.TempDir(), "新图.webp", "new-bytes")

	if replaceRoleImage(root, "role_x", src) {
		t.Fatal("写入失败时 replaceRoleImage 应返回 false")
	}
	// 旧立绘与旧预览都还在
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

// 导入失败不能留下半成品目录：没有 role.json 的目录会被 scanRoles 跳过 ——
// 用户在界面上看不见它，它却占着「名字」，下一次导入就变成 "名字 (2)" 了。
// 这里覆盖两条真正会走到的失败路径：不支持的格式、超大的文件。
func TestCreateRoleDirLeavesNoLitterOnFailure(t *testing.T) {
	// 不支持的格式
	root1 := t.TempDir()
	bad := writeImgSrc(t, t.TempDir(), "图.bmp", "bmp-bytes")
	if key := createRoleDir(root1, "图", bad); key != "" {
		t.Fatalf("不支持的格式应被拒绝，实际 key=%q", key)
	}
	if entries, _ := os.ReadDir(root1); len(entries) != 0 {
		t.Fatalf("失败后不该留下目录，实际 %d 个", len(entries))
	}

	// 超大的文件（> 20MB）
	root2 := t.TempDir()
	big := filepath.Join(t.TempDir(), "大图.png")
	if err := os.WriteFile(big, make([]byte, 21*1024*1024), 0o644); err != nil {
		t.Fatal(err)
	}
	if key := createRoleDir(root2, "大图", big); key != "" {
		t.Fatalf("超大的文件应被拒绝，实际 key=%q", key)
	}
	if entries, _ := os.ReadDir(root2); len(entries) != 0 {
		t.Fatalf("失败后不该留下目录，实际 %d 个", len(entries))
	}

	// 正常路径：三样文件齐备，且只扫得到 1 个督休专员
	root3 := t.TempDir()
	ok := writeImgSrc(t, t.TempDir(), "图.png", "png-bytes")
	key := createRoleDir(root3, "图", ok)
	if key == "" {
		t.Fatal("正常导入应成功")
	}
	dir, _ := resolveRoleDir(root3, key)
	if dir == "" {
		t.Fatal("应能按 key 反查到目录")
	}
	for _, name := range []string{"figure.png", "preview.png", "role.json"} {
		if !isRegularFile(filepath.Join(dir, name)) {
			t.Fatalf("目录里应该有 %s", name)
		}
	}
	if n := len(scanRoles(root3)); n != 1 {
		t.Fatalf("应只扫到 1 个督休专员，实际 %d 个", n)
	}
}
