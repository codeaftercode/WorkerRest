package main

import (
	"os"
	"path/filepath"
	"testing"
)

// 督休专员**显示名唯一性**的回归（2026-10-26）。
//
// 背景：一个督休专员 = supervisors/<目录>/{role.json, 立绘, 预览图}。
// 目录名 = 显示名，key 是稳定身份。显示名重名会让「key ↔ 目录 ↔ 显示名」
// 三者失配：目录名靠 uniqueRoleDir 去重成「X (2)」，而 role.json 里的显示名
// 还是「X」—— 同一个名字在画廊磁贴 / 台词气泡 / 提醒面板标题里指向两个不同的人。
//
// 三层防线，逐层钉住：
//   ① 前端 promptDialog 的 validate：重名 → 拒绝保存、留在框里要求重新输入；
//   ② Go 侧 SaveRole：重名 → 返回 false，零副作用（不改目录、不写 role.json）；
//   ③ Go 侧 createRoleDir / 迁移：软件自己起名 → 自动退让成「X (2)」。
//
// ⚠️ 注意：显示名唯一性**只管「同名」**，管不到「同图」。
// 同一张图片导入两次（key 撞号）由 uniqueRoleKey 负责，两道防线互补、缺一不可。

// roleNameTaken 基本行为：命中同名 / 大小写不同也算命中 / 排除自己不误报。
func TestRoleNameTakenBasics(t *testing.T) {
	root := t.TempDir()
	writeRoleDir(t, root, "医生", map[string]any{
		"key": "role_doctor", "name": "医生", "enabled": true, "image": "figure.webp",
	}, map[string]string{"figure.webp": "w"})
	writeRoleDir(t, root, "学姐", map[string]any{
		"key": "role_mentor", "name": "学姐", "enabled": true, "image": "figure.webp",
	}, map[string]string{"figure.webp": "w"})
	writeRoleDir(t, root, "Alice", map[string]any{
		"key": "role_alice", "name": "Alice", "enabled": true, "image": "figure.webp",
	}, map[string]string{"figure.webp": "w"})

	if !roleNameTaken(root, "医生", "") {
		t.Fatal("同名应判定为已占用")
	}
	// 大小写不敏感（与 Windows 目录名口径一致）：
	// "Alice" 与 "alice" 在目录层只能存在一个，显示名若区分就会指向两个人。
	if !roleNameTaken(root, "alice", "") {
		t.Fatal("大小写不同的同名应判定为已占用")
	}
	if !roleNameTaken(root, "  医生  ", "") {
		t.Fatal("首尾空格差异应仍算占用")
	}
	// 排除自己：改名时不能被自己的旧名字挡住
	if roleNameTaken(root, "医生", "role_doctor") {
		t.Fatal("excludeKey 命中自己时不应算占用")
	}
	if roleNameTaken(root, "不存在的名字", "") {
		t.Fatal("没人用的名字不应算占用")
	}
	if roleNameTaken(root, "   ", "") {
		t.Fatal("空名不应算占用")
	}
}

// scanRoleNames：只读 key 与显示名，且**不碰立绘 / 预览图**。
//
// 为什么这条要单测：查重名原本走 scanRoles，而 scanRoles 会给每个专员生成
// 缩略图的 data URL（17 个内置专员约 3.5 MB 图片要读一遍再 base64 编码）。
// 改个名字要等好几秒 —— 轻量扫描是性能修复的落点，不能被改回去。
func TestScanRoleNamesLightweight(t *testing.T) {
	root := t.TempDir()
	// 目录里放一个**坏掉的**立绘文件：真去读图就会出错，
	// 只读 role.json 的路径不该被它影响。
	writeRoleDir(t, root, "甲", map[string]any{
		"key": "role_a", "name": "甲", "enabled": true, "image": "figure.webp",
	}, map[string]string{"figure.webp": "not-an-image"})
	writeRoleDir(t, root, "乙", map[string]any{
		"key": "role_b", "name": "乙", "enabled": true, "image": "figure.webp",
	}, map[string]string{"figure.webp": "not-an-image"})

	entries := scanRoleNames(root)
	if len(entries) != 2 {
		t.Fatalf("应扫到 2 条，实际 %d 条", len(entries))
	}
	seen := map[string]string{}
	for _, e := range entries {
		seen[e.Key] = e.Name
	}
	if seen["role_a"] != "甲" || seen["role_b"] != "乙" {
		t.Fatalf("key → 显示名 映射不对：%v", seen)
	}
}

// uniqueRoleName：被占用时依次退让「X (2)」「X (3)」；排除自己不误判。
func TestUniqueRoleNameFallsBack(t *testing.T) {
	root := t.TempDir()
	writeRoleDir(t, root, "医生", map[string]any{
		"key": "role_doctor", "name": "医生", "enabled": true, "image": "figure.webp",
	}, map[string]string{"figure.webp": "w"})

	if got := uniqueRoleName(root, "教练", ""); got != "教练" {
		t.Fatalf("没人用的名字不该被改：%q", got)
	}
	if got := uniqueRoleName(root, "医生", ""); got != "医生 (2)" {
		t.Fatalf("重名应退让成「医生 (2)」：%q", got)
	}
	// 自己就叫「医生」时（迁移路径：角色已在磁盘上）不能把自己算进去
	if got := uniqueRoleName(root, "医生", "role_doctor"); got != "医生" {
		t.Fatalf("排除自己时不该退让：%q", got)
	}
	// 连着占两位 → 退让到 (3)
	writeRoleDir(t, root, "医生 (2)", map[string]any{
		"key": "role_d2", "name": "医生 (2)", "enabled": true, "image": "figure.webp",
	}, map[string]string{"figure.webp": "w"})
	if got := uniqueRoleName(root, "医生", ""); got != "医生 (3)" {
		t.Fatalf("应退让到「医生 (3)」：%q", got)
	}
}

// SaveRole 遇到重名 → 返回 false，且**零副作用**：目录没改名、role.json 没被改写。
//
// 零副作用很重要：拒绝发生在目录改名之前，所以磁盘状态完全不变 ——
// 用户换个名字重试即可，不会留下「名字改了但目录没改」的半成品。
func TestSaveRoleRejectsDuplicateName(t *testing.T) {
	root := t.TempDir()
	writeRoleDir(t, root, "医生", map[string]any{
		"key": "role_doctor", "name": "医生", "enabled": true, "image": "figure.webp",
	}, map[string]string{"figure.webp": "w"})
	writeRoleDir(t, root, "学姐", map[string]any{
		"key": "role_mentor", "name": "学姐", "enabled": true, "image": "figure.webp",
	}, map[string]string{"figure.webp": "w"})

	a := &App{rolesRoot: root}

	// 学姐改成「医生」→ 拒绝
	if a.SaveRole("role_mentor", map[string]any{"name": "医生"}) {
		t.Fatal("重名必须被拒绝（SaveRole 应返回 false）")
	}
	// 零副作用：目录名没变
	if _, err := os.Stat(filepath.Join(root, "学姐")); err != nil {
		t.Fatalf("被拒后原目录应还在：%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "医生 (2)")); err == nil {
		t.Fatal("被拒后不该建出新目录")
	}
	// role.json 也没被改写
	cfg, ok := readRoleFile(root, "role_mentor")
	if !ok || cfg.Name != "学姐" {
		t.Fatalf("被拒后显示名不该变：%q ok=%v", cfg.Name, ok)
	}

	// 改成没被占用的名字 → 放行，且目录跟着改名
	if !a.SaveRole("role_mentor", map[string]any{"name": "学姐2"}) {
		t.Fatal("不重名应放行")
	}
	if _, err := os.Stat(filepath.Join(root, "学姐2")); err != nil {
		t.Fatalf("改名后目录应跟着走：%v", err)
	}
	cfg, _ = readRoleFile(root, "role_mentor")
	if cfg.Name != "学姐2" {
		t.Fatalf("改名后显示名应更新：%q", cfg.Name)
	}
}

// 改名时排除自己：名字不变（提交原名）不该被自己的旧名挡住。
func TestSaveRoleAllowsUnchangedName(t *testing.T) {
	root := t.TempDir()
	writeRoleDir(t, root, "医生", map[string]any{
		"key": "role_doctor", "name": "医生", "enabled": true, "image": "figure.webp",
	}, map[string]string{"figure.webp": "w"})

	a := &App{rolesRoot: root}
	if !a.SaveRole("role_doctor", map[string]any{"name": "医生"}) {
		t.Fatal("提交原名应放行（不能被自己的旧名挡住）")
	}
	// 大小写不同也算同一个名字 → 允许（自己）
	if !a.SaveRole("role_doctor", map[string]any{"name": "医生"}) {
		t.Fatal("再次提交同名应放行")
	}
}

// createRoleDir 落盘时显示名也要唯一：同名导入两次 → 第二个是「X (2)」，
// 而不是两个都叫「X」（那会让画廊 / 台词气泡分不清谁是谁）。
func TestCreateRoleDirDisplayNameUnique(t *testing.T) {
	root := t.TempDir()
	src := writeImgSrc(t, t.TempDir(), "我的图.png", "bytes-a")
	src2 := writeImgSrc(t, t.TempDir(), "另一张.png", "bytes-b")

	k1 := createRoleDir(root, "我的图", src)
	k2 := createRoleDir(root, "我的图", src2)
	if k1 == "" || k2 == "" {
		t.Fatalf("导入应成功：k1=%q k2=%q", k1, k2)
	}
	c1, _ := readRoleFile(root, k1)
	c2, _ := readRoleFile(root, k2)
	if c1.Name != "我的图" {
		t.Fatalf("第一个应叫「我的图」：%q", c1.Name)
	}
	if c2.Name != "我的图 (2)" {
		t.Fatalf("第二个应自动退让成「我的图 (2)」：%q", c2.Name)
	}
	// 显示名与目录名一致（目录名 = 显示名的新布局）
	d2, _ := resolveRoleDir(root, k2)
	if filepath.Base(d2) != "我的图 (2)" {
		t.Fatalf("目录名应与显示名一致：%q", filepath.Base(d2))
	}
}

// 「同名」与「同图」是两个正交维度：
// 同一张图导入两次 → key 不同（uniqueRoleKey 兜底）、显示名也不同（自动退让）；
// 不同图同名导入 → 显示名不同、key 本来就不同（摘要不同）。
//
// 这条用例把「两道防线互补」钉死：名称唯一性检查**不能**替代 key 唯一化。
func TestSameImageAndSameNameAreIndependent(t *testing.T) {
	root := t.TempDir()
	src := writeImgSrc(t, t.TempDir(), "我的图.png", "same-bytes")

	// 同图导入两次
	k1 := createRoleDir(root, "我的图", src)
	k2 := createRoleDir(root, "我的图", src)
	if k1 == k2 {
		t.Fatalf("同图两次必须给不同 key：%q", k1)
	}
	c1, _ := readRoleFile(root, k1)
	c2, _ := readRoleFile(root, k2)
	if c1.Name == c2.Name {
		t.Fatalf("同图两次的显示名也应唯一：%q == %q", c1.Name, c2.Name)
	}
	// 显示名唯一 → roleNameTaken 必须能查到对方
	if !roleNameTaken(root, c2.Name, "") {
		t.Fatalf("「%s」应被判定为已占用", c2.Name)
	}
	// 排除自己时不误报
	if roleNameTaken(root, c2.Name, k2) {
		t.Fatalf("排除自己时「%s」不该算占用", c2.Name)
	}
}
