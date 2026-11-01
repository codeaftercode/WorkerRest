package main

import (
	"os"
	"path/filepath"
	"testing"
	"workerrest/internal/role"
)

// 督休专员**名字唯一性**的回归（2026-10-26 起；2026-10-28 改成「名字即身份」后重写）。
//
// 背景：一个督休专员 = supervisors/<目录>/{meta.json, 立绘, 预览图}，没有内部 id。
// 名字重复会让「名字 ↔ 目录 ↔ fixedRole / order」四者失配：目录名靠 role.UniqueRoleDir
// 去重成「X (2)」，meta.json 里却还是「X」—— 同一个名字在画廊磁贴 / 台词气泡 /
// 提醒面板标题里指向两个不同的人，删一个还会删错。
//
// 三层防线，逐层钉住：
//   ① 前端 promptDialog 的 validate：重名 → 拒绝保存、留在框里要求重新输入；
//   ② Go 侧 SaveRole：重名 → 返回 false，零副作用（不改目录、不写 meta.json）；
//   ③ Go 侧 role.CreateRoleDir：软件自己起名 → 自动退让成「X (2)」。
//
// ⚠️ 判据是 role.RoleIdentity（**目录名口径**），不是字符串相等：名字里带 `/`、大小写不同、
// 尾随点空格，都会落到同一个目录上，必须算重名。见 TestRoleNameTakenDirNameIdentity。

// role.RoleNameTaken 基本行为：命中同名 / 大小写不同也算命中 / 排除自己不误报。
func TestRoleNameTakenBasics(t *testing.T) {
	root := t.TempDir()
	writeRoleDir(t, root, "医生", map[string]any{
		"name": "医生", "enabled": true, "image": "figure.webp",
	}, map[string]string{"figure.webp": "w"})
	writeRoleDir(t, root, "学姐", map[string]any{
		"name": "学姐", "enabled": true, "image": "figure.webp",
	}, map[string]string{"figure.webp": "w"})
	writeRoleDir(t, root, "Alice", map[string]any{
		"name": "Alice", "enabled": true, "image": "figure.webp",
	}, map[string]string{"figure.webp": "w"})

	if !role.RoleNameTaken(root, "医生", "") {
		t.Fatal("同名应判定为已占用")
	}
	// 大小写不敏感（与 Windows 目录名口径一致）：
	// "Alice" 与 "alice" 在目录层只能存在一个，显示名若区分就会指向两个人。
	if !role.RoleNameTaken(root, "alice", "") {
		t.Fatal("大小写不同的同名应判定为已占用")
	}
	if !role.RoleNameTaken(root, "  医生  ", "") {
		t.Fatal("首尾空格差异应仍算占用")
	}
	// 排除自己：改名时不能被自己的旧名字挡住
	if role.RoleNameTaken(root, "医生", "医生") {
		t.Fatal("excludeName 命中自己时不应算占用")
	}
	if role.RoleNameTaken(root, "不存在的名字", "") {
		t.Fatal("没人用的名字不应算占用")
	}
	if role.RoleNameTaken(root, "   ", "") {
		t.Fatal("空名不应算占用")
	}
}

// 判据是「目录名口径」：会被 sanitize 抹平的差异必须算重名。
//
// 反例：只比字符串的话，"A/B" 与 "AB" 判为不重名 → 两个专员争同一个目录，
// 后写的那份 meta.json 覆盖前一份，界面上只剩一个人（文件还在，最难查的那种）。
func TestRoleNameTakenDirNameIdentity(t *testing.T) {
	root := t.TempDir()
	writeRoleDir(t, root, "AB", map[string]any{
		"name": "AB", "enabled": true, "image": "figure.webp",
	}, map[string]string{"figure.webp": "w"})
	writeRoleDir(t, root, "abc", map[string]any{
		"name": "abc", "enabled": true, "image": "figure.webp",
	}, map[string]string{"figure.webp": "w"})

	cases := []struct {
		name string
		want bool
	}{
		{"A/B", true},     // 斜杠被剔除 → AB
		{"A\\B", true},    // 反斜杠同理
		{"AB.", true},     // 尾随点被系统吞掉
		{"AB ", true},     // 尾随空格同理
		{"ABC", true},     // 大小写不同
		{"A:B", true},     // 冒号被剔除
		{"AC", false},     // 真的不同
		{"", false},       // 空名
		{"AB (2)", false}, // 去重后缀是独立的名字
	}
	for _, c := range cases {
		if got := role.RoleNameTaken(root, c.name, ""); got != c.want {
			t.Fatalf("role.RoleNameTaken(%q)=%v，期望 %v", c.name, got, c.want)
		}
	}
}

// role.SanitizeRoleDirName 的几处「不处理就会静默撞车」的边界。
func TestSanitizeRoleDirName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"医生", "医生"},
		{" 医生 ", "医生"},
		{"A/B", "AB"},
		{"医生.", "医生"},
		{"医生 ", "医生"},
		{"CON", "_CON"}, // Windows 保留名
		{"con.txt", "_con.txt"},
		{"COM1", "_COM1"},
		{"", "督休专员"},
		{"...", "督休专员"},
	}
	for _, c := range cases {
		if got := role.SanitizeRoleDirName(c.in); got != c.want {
			t.Fatalf("role.SanitizeRoleDirName(%q)=%q，期望 %q", c.in, got, c.want)
		}
	}
	// 超长名字截到 60 个字符（目录名上限 255 字节，中文按 3 字节算）
	long := ""
	for i := 0; i < 80; i++ {
		long += "长"
	}
	if n := len([]rune(role.SanitizeRoleDirName(long))); n != 60 {
		t.Fatalf("超长名字应截到 60 字符，实际 %d", n)
	}
}

// role.ScanRoleNames：只读显示名，且**不碰立绘 / 预览图**。
//
// 为什么这条要单测：查重名原本走 role.ScanRoles，而 role.ScanRoles 会给每个专员生成
// 缩略图的 data URL（17 个内置专员约 3.5 MB 图片要读一遍再 base64 编码）。
// 改个名字要等好几秒 —— 轻量扫描是性能修复的落点，不能被改回去。
func TestScanRoleNamesLightweight(t *testing.T) {
	root := t.TempDir()
	// 目录里放一个**坏掉的**立绘文件：真去读图就会出错，
	// 只读 meta.json 的路径不该被它影响。
	writeRoleDir(t, root, "甲", map[string]any{
		"name": "甲", "enabled": true, "image": "figure.webp",
	}, map[string]string{"figure.webp": "not-an-image"})
	writeRoleDir(t, root, "乙", map[string]any{
		"name": "乙", "enabled": true, "image": "figure.webp",
	}, map[string]string{"figure.webp": "not-an-image"})

	names := role.ScanRoleNames(root)
	if len(names) != 2 {
		t.Fatalf("应扫到 2 条，实际 %d 条", len(names))
	}
	seen := map[string]bool{}
	for _, n := range names {
		seen[n] = true
	}
	if !seen["甲"] || !seen["乙"] {
		t.Fatalf("显示名不对：%v", names)
	}
}

// role.UniqueRoleName：被占用时依次退让「X (2)」「X (3)」；排除自己不误判。
func TestUniqueRoleNameFallsBack(t *testing.T) {
	root := t.TempDir()
	writeRoleDir(t, root, "医生", map[string]any{
		"name": "医生", "enabled": true, "image": "figure.webp",
	}, map[string]string{"figure.webp": "w"})

	if got := role.UniqueRoleName(root, "教练", ""); got != "教练" {
		t.Fatalf("没人用的名字不该被改：%q", got)
	}
	if got := role.UniqueRoleName(root, "医生", ""); got != "医生 (2)" {
		t.Fatalf("重名应退让成「医生 (2)」：%q", got)
	}
	// 自己就叫「医生」时不能把自己算进去
	if got := role.UniqueRoleName(root, "医生", "医生"); got != "医生" {
		t.Fatalf("排除自己时不该退让：%q", got)
	}
	// 连着占两位 → 退让到 (3)
	writeRoleDir(t, root, "医生 (2)", map[string]any{
		"name": "医生 (2)", "enabled": true, "image": "figure.webp",
	}, map[string]string{"figure.webp": "w"})
	if got := role.UniqueRoleName(root, "医生", ""); got != "医生 (3)" {
		t.Fatalf("应退让到「医生 (3)」：%q", got)
	}
}

// SaveRole 遇到重名 → 返回 false，且**零副作用**：目录没改名、meta.json 没被改写。
//
// 零副作用很重要：拒绝发生在写盘之前，所以磁盘状态完全不变 ——
// 用户换个名字重试即可，不会留下「名字改了但目录没改」的半成品。
func TestSaveRoleRejectsDuplicateName(t *testing.T) {
	root := t.TempDir()
	writeRoleDir(t, root, "医生", map[string]any{
		"name": "医生", "enabled": true, "image": "figure.webp",
	}, map[string]string{"figure.webp": "w"})
	writeRoleDir(t, root, "学姐", map[string]any{
		"name": "学姐", "enabled": true, "image": "figure.webp",
	}, map[string]string{"figure.webp": "w"})

	a := &App{rolesRoot: root}

	// 学姐改成「医生」→ 拒绝
	if a.SaveRole("学姐", map[string]any{"name": "医生"}) {
		t.Fatal("重名必须被拒绝（SaveRole 应返回 false）")
	}
	// 零副作用：目录名没变
	if _, err := os.Stat(filepath.Join(root, "学姐")); err != nil {
		t.Fatalf("被拒后原目录应还在：%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "医生 (2)")); err == nil {
		t.Fatal("被拒后不该建出新目录")
	}
	cfg, ok := role.ReadRoleFile(root, "学姐")
	if !ok || cfg.Name != "学姐" {
		t.Fatalf("被拒后显示名不该变：%q ok=%v", cfg.Name, ok)
	}

	// 改成没被占用的名字 → 放行，且目录跟着改名
	if !a.SaveRole("学姐", map[string]any{"name": "学姐2"}) {
		t.Fatal("不重名应放行")
	}
	if _, err := os.Stat(filepath.Join(root, "学姐2")); err != nil {
		t.Fatalf("改名后目录应跟着走：%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "学姐")); err == nil {
		t.Fatal("改名后旧目录不该还在")
	}
	cfg, _ = role.ReadRoleFile(root, "学姐2")
	if cfg.Name != "学姐2" {
		t.Fatalf("改名后显示名应更新：%q", cfg.Name)
	}
}

// 改名后立绘不能丢：这是「改名 = 换身份」最容易踩的坑 ——
// 按新名反查不到旧目录会新建空壳，原来的立绘就被丢在旧目录里。
func TestSaveRoleRenameKeepsFigure(t *testing.T) {
	root := t.TempDir()
	writeRoleDir(t, root, "医生", map[string]any{
		"name": "医生", "enabled": true, "image": "figure.png",
	}, map[string]string{"figure.png": "doctor-bytes"})

	a := &App{rolesRoot: root}
	if !a.SaveRole("医生", map[string]any{"name": "值班医生"}) {
		t.Fatal("改名应成功")
	}
	dir, ok := role.ResolveRoleDir(root, "值班医生")
	if !ok {
		t.Fatal("改名后应能按新名反查")
	}
	if filepath.Base(dir) != "值班医生" {
		t.Fatalf("目录名应跟着改：%q", filepath.Base(dir))
	}
	b, err := os.ReadFile(filepath.Join(dir, "figure.png"))
	if err != nil {
		t.Fatalf("改名后立绘不该丢：%v", err)
	}
	if string(b) != "doctor-bytes" {
		t.Fatalf("立绘内容被改了：%q", b)
	}
	// 旧目录不能留
	if _, err := os.Stat(filepath.Join(root, "医生")); err == nil {
		t.Fatal("改名后旧目录不该还在")
	}
	// 扫描结果里也只有一个人
	if roles := role.ScanRoles(root); len(roles) != 1 || roles[0].Name != "值班医生" {
		t.Fatalf("扫描结果应只有「值班医生」，实际 %v", roleNames(roles))
	}
}

// 改名时排除自己：名字不变（提交原名）不该被自己的旧名挡住。
func TestSaveRoleAllowsUnchangedName(t *testing.T) {
	root := t.TempDir()
	writeRoleDir(t, root, "医生", map[string]any{
		"name": "医生", "enabled": true, "image": "figure.webp",
	}, map[string]string{"figure.webp": "w"})

	a := &App{rolesRoot: root}
	if !a.SaveRole("医生", map[string]any{"name": "医生"}) {
		t.Fatal("提交原名应放行（不能被自己的旧名挡住）")
	}
	if !a.SaveRole("医生", map[string]any{"name": "医生"}) {
		t.Fatal("再次提交同名应放行")
	}
}

// role.CreateRoleDir 落盘时显示名也要唯一：同名导入两次 → 第二个是「X (2)」，
// 而不是两个都叫「X」（那会让画廊 / 台词气泡分不清谁是谁）。
func TestCreateRoleDirDisplayNameUnique(t *testing.T) {
	root := t.TempDir()
	src := writeImgSrc(t, t.TempDir(), "我的图.png", "bytes-a")
	src2 := writeImgSrc(t, t.TempDir(), "另一张.png", "bytes-b")

	n1 := role.CreateRoleDir(root, "我的图", src)
	n2 := role.CreateRoleDir(root, "我的图", src2)
	if n1 == "" || n2 == "" {
		t.Fatalf("导入应成功：n1=%q n2=%q", n1, n2)
	}
	c1, _ := role.ReadRoleFile(root, n1)
	c2, _ := role.ReadRoleFile(root, n2)
	if c1.Name != "我的图" {
		t.Fatalf("第一个应叫「我的图」：%q", c1.Name)
	}
	if c2.Name != "我的图 (2)" {
		t.Fatalf("第二个应自动退让成「我的图 (2)」：%q", c2.Name)
	}
	// 目录名与名字一致
	d2, _ := role.ResolveRoleDir(root, n2)
	if filepath.Base(d2) != "我的图 (2)" {
		t.Fatalf("目录名应与名字一致：%q", filepath.Base(d2))
	}
}
