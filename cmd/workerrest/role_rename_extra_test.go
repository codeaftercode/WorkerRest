package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"workerrest/internal/role"
	"workerrest/internal/settings"
)

// 督休专员改名的**端到端**契约（2026-11-14 全面测试）。
//
// 改名是这条链路上最容易出事的一步：名字是身份，目录名只是投影，两者由 sanitize 桥接。
// 一次改名要动四样东西 —— meta.json / 目录名 / 配置里的 fixedRole 与 order / 内存里的
// supEditName，任何一样没跟上或者“跟得太早”，用户看到的就是“改名失败但没说是为什么”。
//
// 这组用例覆盖的异常面：
//   - 特殊字符（非法字符 / 控制字符 / 前导点 / 保留设备名 / 超长 / emoji / 全角空格）
//     —— 包括它们曾经**直接导致**的两类灾难：改名挂死、专员从列表里蒸发；
//   - 目录改名失败（被别的进程用无 FILE_SHARE_DELETE 的句柄占住）：保存必须成功、
//     目录名可以陈旧、后续还能再改；
//   - meta.json 写不进去（被句柄占住）：必须整体失败、零副作用、不留半截文件；
//   - 重名（含规范化后重名）、不存在的专员、坏 meta.json、空名字、改回原名、反复改名。

// roleDirs 列出 root 下的子目录名（排序）。
func roleDirs(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out
}

// countRolesNamed 扫描出“meta.json 的 name 等于 name”的目录个数（= 同名身份个数）。
func countRolesNamed(t *testing.T, root, name string) int {
	t.Helper()
	n := 0
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(root, e.Name(), "meta.json"))
		if err != nil {
			continue
		}
		var m map[string]any
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		if s, _ := m["name"].(string); strings.TrimSpace(s) == name {
			n++
		}
	}
	return n
}

// assertNoTempFiles 目录里不能留下 meta.json.tmp 之类半成品（写失败要自己收拾干净）。
func assertNoTempFiles(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		inner, err := os.ReadDir(filepath.Join(root, e.Name()))
		if err != nil {
			continue
		}
		for _, f := range inner {
			if strings.HasSuffix(f.Name(), ".tmp") {
				t.Fatalf("留下了半截文件：%s/%s", e.Name(), f.Name())
			}
		}
	}
}

func newRenameFixture(t *testing.T) (root string, cfgPath string, a *App) {
	t.Helper()
	root = t.TempDir()
	writeRoleDir(t, root, "医生", map[string]any{
		"name": "医生", "enabled": true, "image": "figure.webp", "preview": "preview.webp",
		"geom": map[string]any{"sizePct": 66, "posXPct": 12, "posYPct": 8, "flip": true},
		"messages": map[string]any{
			"order": "sequence",
			"items": []any{map[string]any{"id": "a", "text": "该休息了", "enabled": true}},
		},
	}, map[string]string{"figure.webp": "FIGURE", "preview.webp": "PREVIEW"})
	// 一位“目录名 (2) 但名字叫 路人”的邻居：用来验证去重后缀不是按目录名硬拼
	writeRoleDir(t, root, "x (2)", map[string]any{
		"name": "路人", "enabled": true, "image": "figure.webp",
	}, map[string]string{"figure.webp": "W"})

	cfgPath = filepath.Join(t.TempDir(), "settings.json")
	cfg := settings.New(map[string]any{
		"supervisor": map[string]any{
			"enabled":   true,
			"selection": "fixed",
			"fixedRole": "医生",
			"order":     []any{"医生", "路人"},
		},
	}, cfgPath)
	return root, cfgPath, &App{rolesRoot: root, cfg: cfg}
}

// 脏名字改名：必须成功、必须仍然可被扫描到、必须保留立绘与几何台词，且能改回原名。
//
// 这条用例的每一个名字都曾经或差点造成“改名成功但人没了”：
//   - ".hidden" / "..hidden" → 目录以点开头，ValidRoleDirName 判非法，扫描整目录跳过；
//   - 80 个汉字 → sanitize 按 rune 截 60（180 字节），超过 128 字节上限，同样被跳过；
//   - 换行 / 控制字符 → Win32 判“文件名语法不正确”，Stat 报第三种错误，改名为此挂死；
//   - "CON" / "con.txt" → Windows 设备名，mkdir 直接失败。
func TestSaveRoleRenameDirtyNames(t *testing.T) {
	names := []string{
		"值班医生",
		"Victoria Nightingale",
		"a/b",
		`a*b?c"d<e>f|g`,
		"a\x01b",
		"医\n生",
		"CON",
		"con.txt",
		".hidden",
		"..hidden",
		"..",
		"///",
		"医生.",
		"emoji😀🎉",
		"中文 空格",
		"%USERPROFILE%",
		"100%",
		strings.Repeat("长", 80),
		"护士 (2)",
		"x (2)",
	}
	for _, want := range names {
		t.Run(want, func(t *testing.T) {
			root, _, a := newRenameFixture(t)

			if !a.SaveRole("医生", map[string]any{"name": want}) {
				t.Fatalf("改名 %q 应成功", want)
			}
			// ① 按新名字反查得到，且只剩一位叫这个名字的人（没有分裂成两个）
			dir, ok := role.ResolveRoleDir(root, want)
			if !ok {
				t.Fatalf("改名后按 %q 反查不到目录（旧目录：%v）", want, roleDirs(t, root))
			}
			if n := countRolesNamed(t, root, want); n != 1 {
				t.Fatalf("应有且仅有 1 位叫 %q 的专员，实际 %d 位", want, n)
			}
			// ② 扫描能看到它 —— 这正是“前导点 / 超长目录名”当年丢掉的地方
			seen := false
			for _, n := range role.ScanRoleNames(root) {
				if n == want {
					seen = true
				}
			}
			if !seen {
				t.Fatalf("改名后扫描不到 %q：%v", want, roleDirs(t, root))
			}
			// ③ 旧名字已经没有人用了
			for _, n := range role.ScanRoleNames(root) {
				if n == "医生" {
					t.Fatalf("旧名字 医生 仍然存在：%v", role.ScanRoleNames(root))
				}
			}
			// ④ 立绘 / 预览 / 几何 / 台词一个都不能丢（改名只换身份）
			cfg, ok := role.ReadRoleFile(root, want)
			if !ok {
				t.Fatalf("meta.json 读不出来：%s", dir)
			}
			if cfg.Image != "figure.webp" || cfg.Preview != "preview.webp" {
				t.Fatalf("立绘/预览字段被改名带跑了：%+v", cfg)
			}
			for _, f := range []string{"figure.webp", "preview.webp"} {
				if !role.IsRegularFile(filepath.Join(dir, f)) {
					t.Fatalf("文件被改名弄丢了：%s", f)
				}
			}
			if cfg.Geom.SizePct != 66 || cfg.Geom.PosXPct != 12 || cfg.Geom.PosYPct != 8 || !cfg.Geom.Flip {
				t.Fatalf("几何被改名重置了：%+v", cfg.Geom)
			}
			if len(cfg.Messages.Items) != 1 || cfg.Messages.Order != "sequence" ||
				cfg.Messages.Items[0].Text != "该休息了" {
				t.Fatalf("台词被改名重置了：%+v", cfg.Messages)
			}
			// ⑤ 目录总数不变（旧的被改名，不是复制出一个新的）
			if dirs := roleDirs(t, root); len(dirs) != 2 {
				t.Fatalf("目录数应保持 2 个，实际 %v", dirs)
			}
			assertNoTempFiles(t, root)

			// ⑥ 还能改回原名（脏名字不会把后路堵死）
			if !a.SaveRole(want, map[string]any{"name": "医生"}) {
				t.Fatalf("从 %q 改回原名应成功", want)
			}
			if _, ok := role.ResolveRoleDir(root, "医生"); !ok {
				t.Fatalf("改回原名后反查不到，目录：%v", roleDirs(t, root))
			}
			if !role.IsRegularFile(filepath.Join(dirAfter(t, root, "医生"), "figure.webp")) {
				t.Fatal("改回原名后立绘丢了")
			}
		})
	}
}

func dirAfter(t *testing.T, root, name string) string {
	t.Helper()
	dir, ok := role.ResolveRoleDir(root, name)
	if !ok {
		t.Fatalf("反查不到 %q：%v", name, roleDirs(t, root))
	}
	return dir
}

// 目录名撞车（邻居的目录叫 "x (2)"，名字却叫 路人）→ 新目录退让成 "x (2) (2)"，
// 两个人都在、都能按名字反查回来。
func TestSaveRoleRenameIntoOccupiedDirName(t *testing.T) {
	root, _, a := newRenameFixture(t)
	if !a.SaveRole("医生", map[string]any{"name": "x (2)"}) {
		t.Fatal("改名应成功（名字不重，只是目录名撞了）")
	}
	dir, ok := role.ResolveRoleDir(root, "x (2)")
	if !ok {
		t.Fatalf("反查不到 x (2)：%v", roleDirs(t, root))
	}
	if base := filepath.Base(dir); base != "x (2) (2)" {
		t.Fatalf("目录应退让成 “x (2) (2)”，实际 %q", base)
	}
	if _, ok := role.ResolveRoleDir(root, "路人"); !ok {
		t.Fatal("邻居被改名波及了")
	}
}

// 只差大小写的改名：目录就地改大小写，不能退让成 "Doctor (2)"（那会让目录名彻底失去意义）。
func TestSaveRoleRenameCaseOnly(t *testing.T) {
	root := t.TempDir()
	writeRoleDir(t, root, "doctor", map[string]any{
		"name": "doctor", "enabled": true, "image": "figure.webp",
	}, map[string]string{"figure.webp": "w"})
	cfgPath := filepath.Join(t.TempDir(), "settings.json")
	cfg := settings.New(map[string]any{
		"supervisor": map[string]any{"enabled": true, "fixedRole": "doctor", "order": []any{"doctor"}},
	}, cfgPath)
	a := &App{rolesRoot: root, cfg: cfg}

	if !a.SaveRole("doctor", map[string]any{"name": "Doctor"}) {
		t.Fatal("只改大小写应成功")
	}
	dir, ok := role.ResolveRoleDir(root, "Doctor")
	if !ok {
		t.Fatalf("反查不到 Doctor：%v", roleDirs(t, root))
	}
	if base := filepath.Base(dir); base != "Doctor" {
		t.Fatalf("目录应就地改成 Doctor，实际 %q", base)
	}
	sup, _ := a.cfg.Raw()["supervisor"].(map[string]any)
	if got, _ := sup["fixedRole"].(string); got != "Doctor" {
		t.Fatalf("fixedRole 应跟着改成新大小写，实际 %q", got)
	}
}

// 目录被别的进程占住（无 FILE_SHARE_DELETE 的句柄）：目录改名必然失败，但**保存必须成功**。
//
// 这是设计里明确取舍过的一条：目录名只是投影，名字（meta.json）才是权威。
// 若这里回滚或者返回 false，用户会看到“改名失败”，而实际上名字已经改好了 —— 更糟的是
// 配置里的 fixedRole 没跟上，固定显示的人会被悄悄换成列表第一个。
func TestSaveRoleRenameWhenDirLocked(t *testing.T) {
	root, _, a := newRenameFixture(t)
	unlock := lockDir(t, filepath.Join(root, "医生"))
	defer unlock()

	if !a.SaveRole("医生", map[string]any{"name": "值班医生"}) {
		t.Fatal("目录改不了名时，保存本身仍应成功（名字是权威，目录名只是投影）")
	}
	if _, ok := role.ResolveRoleDir(root, "值班医生"); !ok {
		t.Fatalf("按新名字应能反查（靠 meta.json，不靠目录名）：%v", roleDirs(t, root))
	}
	if dirs := roleDirs(t, root); len(dirs) != 2 || dirs[0] != "x (2)" || dirs[1] != "医生" {
		t.Fatalf("目录名应停在旧名字上（改不动），实际 %v", dirs)
	}
	sup, _ := a.cfg.Raw()["supervisor"].(map[string]any)
	if got, _ := sup["fixedRole"].(string); got != "值班医生" {
		t.Fatalf("目录没改名也必须同步 fixedRole，实际 %q", got)
	}

	// 解开占用后再改一次名：陈旧的目录名不能挡住后续改名
	unlock()
	if !a.SaveRole("值班医生", map[string]any{"name": "夜班医生"}) {
		t.Fatal("解锁后再次改名应成功")
	}
	dir, ok := role.ResolveRoleDir(root, "夜班医生")
	if !ok {
		t.Fatalf("反查不到夜班医生：%v", roleDirs(t, root))
	}
	if base := filepath.Base(dir); base != "夜班医生" {
		t.Fatalf("第二次改名应把目录一并改过来，实际 %q", base)
	}
}

// meta.json 被占住（写不进去）：必须**整体失败**，不能“名字改了但没存住”。
// 前端据此把名字退回、把错误留在对话框里；磁盘上一个字节都不该变。
//
// 注意：句柄是“不共享”打开的，连读都读不了 —— 所以反查要等解锁后再验
// （这也正是真实场景：杀掉占用的进程，数据必须完好如初）。
func TestSaveRoleRenameWhenMetaWriteFails(t *testing.T) {
	root, cfgPath, a := newRenameFixture(t)
	metaPath := filepath.Join(root, "医生", "meta.json")
	metaBefore, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	cfgBefore, _ := os.ReadFile(cfgPath)

	unlock := lockFile(t, metaPath)
	failed := a.SaveRole("医生", map[string]any{"name": "值班医生"})
	unlock()

	if failed {
		t.Fatal("meta.json 写不进去时必须返回 false，否则界面会显示一个没存住的名字")
	}
	if _, ok := role.ResolveRoleDir(root, "值班医生"); ok {
		t.Fatal("失败后不该出现新名字")
	}
	if _, ok := role.ResolveRoleDir(root, "医生"); !ok {
		t.Fatalf("失败后旧名字必须完好：%v", roleDirs(t, root))
	}
	if dirs := roleDirs(t, root); len(dirs) != 2 || dirs[1] != "医生" {
		t.Fatalf("失败后目录不该被改名：%v", dirs)
	}
	metaAfter, err := os.ReadFile(metaPath)
	if err != nil || string(metaAfter) != string(metaBefore) {
		t.Fatalf("失败后 meta.json 被改动了：%v\n前 %s\n后 %s", err, metaBefore, metaAfter)
	}
	assertNoTempFiles(t, root)
	if cfgAfter, _ := os.ReadFile(cfgPath); string(cfgAfter) != string(cfgBefore) {
		t.Fatal("失败后配置文件不该被改写")
	}
	sup, _ := a.cfg.Raw()["supervisor"].(map[string]any)
	if got, _ := sup["fixedRole"].(string); got != "医生" {
		t.Fatalf("失败后 fixedRole 不该变，实际 %q", got)
	}
}

// 重名：**规范化之后**撞车的名字必须被拒 —— 判据是目录名口径（RoleIdentity），
// 不是字符串相等。尾点、尾空格、斜杠、保留名加前缀，都会把两个人落到同一个身份上。
//
// 注意区分“重名”与“改成自己的另一种写法”：医生 → 医生. 虽然字符串不同，但身份没变
// （还是同一个目录），属于自己，不该被自己的旧身份挡住 —— 那条单独验。
func TestSaveRoleRenameRejectsNormalizedDuplicates(t *testing.T) {
	root := t.TempDir()
	writeRoleDir(t, root, "医生", map[string]any{
		"name": "医生", "enabled": true, "image": "figure.webp",
	}, map[string]string{"figure.webp": "w"})
	writeRoleDir(t, root, "学姐", map[string]any{
		"name": "学姐", "enabled": true, "image": "figure.webp",
	}, map[string]string{"figure.webp": "w"})
	writeRoleDir(t, root, "_con", map[string]any{
		"name": "_con", "enabled": true, "image": "figure.webp",
	}, map[string]string{"figure.webp": "w"})
	a := &App{rolesRoot: root}

	dups := []string{
		"学姐.",    // 尾点被系统吞掉 → 落到 学姐 的目录
		"  学姐  ", // 前后空格（trim 后同名）
		"学/姐",    // 斜杠被剔除 → 学姐
		"CON",    // 保留名加前缀 "_CON" → 与已有 "_con" 同一身份
	}
	for _, name := range dups {
		if a.SaveRole("医生", map[string]any{"name": name}) {
			t.Fatalf("%q 规范化后与现有专员重名，应被拒绝", name)
		}
		if na := role.ScanRoleNames(root); len(na) != 3 {
			t.Fatalf("%q 被拒后专员数应保持 3，实际 %v", name, na)
		}
		if _, ok := role.ResolveRoleDir(root, "医生"); !ok {
			t.Fatalf("%q 被拒后原专员应完好", name)
		}
	}
}

// 改成“自己的另一种写法”：身份不变，所以允许；目录不动，也不会分裂出第二位。
// 这些名字分别是尾点、尾空格、保留字符、大小写 —— 用户完全可能这么改。
func TestSaveRoleRenameSelfVariants(t *testing.T) {
	for _, variant := range []string{"医生.", "医/生", "  医生  "} {
		root := t.TempDir()
		writeRoleDir(t, root, "医生", map[string]any{
			"name": "医生", "enabled": true, "image": "figure.webp",
		}, map[string]string{"figure.webp": "w"})
		a := &App{rolesRoot: root}
		if !a.SaveRole("医生", map[string]any{"name": variant}) {
			t.Fatalf("改成自己的另一种写法 %q 不该被自己的旧身份挡住", variant)
		}
		dirs := roleDirs(t, root)
		if len(dirs) != 1 || dirs[0] != "医生" {
			t.Fatalf("%q：身份没变，目录不该动，实际 %v", variant, dirs)
		}
		if n := countRolesNamed(t, root, "医生"); n > 1 {
			t.Fatalf("%q：不该分裂出第二位（同名 %d 位）", variant, n)
		}
		if !role.IsRegularFile(filepath.Join(root, "医生", "figure.webp")) {
			t.Fatalf("%q：立绘丢了", variant)
		}
	}
}

// 各种“改不了”的入口：不存在的专员 / 空名字 / 坏 meta.json。
func TestSaveRoleRenameRejectsBadInput(t *testing.T) {
	root := t.TempDir()
	writeRoleDir(t, root, "医生", map[string]any{
		"name": "医生", "enabled": true, "image": "figure.webp",
	}, map[string]string{"figure.webp": "w"})
	// 坏 meta.json 的目录：扫描跳过，但目录确实在
	bad := filepath.Join(root, "坏专员")
	if err := os.MkdirAll(bad, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, "meta.json"), []byte("{不是 JSON"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := &App{rolesRoot: root}

	cases := []struct {
		label string
		name  string
		patch map[string]any
	}{
		{"空名字", "", map[string]any{"name": "新名"}},
		{"只有空格的名字", "   ", map[string]any{"name": "新名"}},
		{"不存在的专员", "查无此人", map[string]any{"name": "新名"}},
		{"坏 meta.json", "坏专员", map[string]any{"name": "新名"}},
	}
	for _, c := range cases {
		if a.SaveRole(c.name, c.patch) {
			t.Fatalf("%s 应被拒绝", c.label)
		}
	}
	// 全部被拒之后现场不变
	if na := role.ScanRoleNames(root); len(na) != 1 || na[0] != "医生" {
		t.Fatalf("被拒后现场应保持原样，实际 %v", na)
	}
	if _, ok := role.ResolveRoleDir(root, "医生"); !ok {
		t.Fatal("医生应仍然在")
	}
}

// 不改名（同名 / 只多空格 / patch 里没有 name）必须是**零副作用**的成功：
// 目录不动、内容不变、配置不动。前端点“确定”时不改内容不该被当成失败。
//
// 比的是**解析后的配置**而不是字节：SaveRole 一律用 MarshalIndent 整份重写，
// 手写的紧凑 JSON 会被格式化，但那不是“改了内容”。
func TestSaveRoleRenameNoop(t *testing.T) {
	root, cfgPath, a := newRenameFixture(t)
	before, ok := role.ReadRoleFile(root, "医生")
	if !ok {
		t.Fatal("前置：应读得到 医生")
	}
	cfgBefore, _ := os.ReadFile(cfgPath)

	for _, patch := range []map[string]any{
		{"name": "医生"},
		{"name": "  医生  "},
		{"name": "医生 "},
		{"enabled": true},
	} {
		if !a.SaveRole("医生", patch) {
			t.Fatalf("不改名的保存应成功：%v", patch)
		}
	}
	if dirs := roleDirs(t, root); len(dirs) != 2 || dirs[1] != "医生" {
		t.Fatalf("目录不该动：%v", dirs)
	}
	after, ok := role.ReadRoleFile(root, "医生")
	if !ok {
		t.Fatal("改后再读不到 医生")
	}
	if before.Name != after.Name || before.Enabled != after.Enabled || before.Image != after.Image ||
		before.Preview != after.Preview || before.Geom != after.Geom ||
		len(after.Messages.Items) != len(before.Messages.Items) {
		t.Fatalf("内容被无谓改写：\n前 %+v\n后 %+v", before, after)
	}
	if cfgAfter, _ := os.ReadFile(cfgPath); string(cfgAfter) != string(cfgBefore) {
		t.Fatal("配置被无谓改写")
	}
}

// 遗留形态：meta.json 里**没有 name 字段**（早期版本 / 手放的目录）时，身份回落成目录名
// （ResolveRoleDir / ScanRoles 同一口径）。这种专员也必须能改名，且改完 meta.json 里
// 要真的写上 name —— 否则下次扫描又回落成目录名，等于改名没生效。
func TestSaveRoleRenameWithoutMetaName(t *testing.T) {
	root := t.TempDir()
	writeRoleDir(t, root, "老专员", map[string]any{
		"enabled": true, "image": "figure.webp",
	}, map[string]string{"figure.webp": "w"})
	a := &App{rolesRoot: root}

	if !a.SaveRole("老专员", map[string]any{"name": "新专员"}) {
		t.Fatal("没有 name 字段的专员也应能改名（身份回落成目录名）")
	}
	dir, ok := role.ResolveRoleDir(root, "新专员")
	if !ok {
		t.Fatalf("反查不到新名字：%v", roleDirs(t, root))
	}
	cfg, ok := role.ReadRoleFile(root, "新专员")
	if !ok || cfg.Name != "新专员" {
		t.Fatalf("meta.json 里应写上 name：%+v（%v）", cfg, ok)
	}
	if base := filepath.Base(dir); base != "新专员" {
		t.Fatalf("目录名应跟着改：%q", base)
	}
	if !role.IsRegularFile(filepath.Join(dir, "figure.webp")) {
		t.Fatal("立绘丢了")
	}
}

// 反复改名：A → B → C → A，每一步都只剩一个目录、立绘都在。
func TestSaveRoleRenameRepeated(t *testing.T) {
	root, _, a := newRenameFixture(t)
	seq := []string{"乙", "丙", "丁"}
	cur := "医生"
	for _, next := range seq {
		if !a.SaveRole(cur, map[string]any{"name": next}) {
			t.Fatalf("%s → %s 应成功", cur, next)
		}
		cur = next
		if dirs := roleDirs(t, root); len(dirs) != 2 {
			t.Fatalf("第 %s 步目录数应为 2，实际 %v", next, dirs)
		}
		if !role.IsRegularFile(filepath.Join(dirAfter(t, root, cur), "figure.webp")) {
			t.Fatalf("第 %s 步立绘丢了", next)
		}
	}
	if !a.SaveRole(cur, map[string]any{"name": "医生"}) {
		t.Fatal("改回原名应成功")
	}
	if _, ok := role.ResolveRoleDir(root, "医生"); !ok {
		t.Fatalf("最终反查不到原名：%v", roleDirs(t, root))
	}
	if n := countRolesNamed(t, root, "医生"); n != 1 {
		t.Fatalf("最终应只有 1 位医生，实际 %d", n)
	}
}
