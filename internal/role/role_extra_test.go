package role

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 这一组补的是「督休专员这份软件资产」的读写口径：图能不能找到、能不能转 data URL、
// 按名字写配置会不会建出空壳。都是真实文件系统操作，全部落在 t.TempDir() 里。

// 造一个最小督休专员目录：<root>/<dir>/meta.json + 立绘图
func makeRole(t *testing.T, root, dir, name, figureName string, figure []byte) string {
	t.Helper()
	d := filepath.Join(root, dir)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := `{"name":"` + name + `","image":"` + figureName + `"}`
	if err := os.WriteFile(filepath.Join(d, RoleMetaFile), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}
	if figureName != "" {
		if err := os.WriteFile(filepath.Join(d, figureName), figure, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

func TestIsImageName(t *testing.T) {
	for _, n := range []string{"figure.webp", "a.png", "b.JPG", "c.jpeg", "d.gif", "带 空格.png"} {
		if !IsImageName(n) {
			t.Fatalf("%q 应是合法图片名", n)
		}
	}
	for _, n := range []string{"", "figure.txt", "a/b.png", `a\b.png`, ".hidden.png", "noext"} {
		if IsImageName(n) {
			t.Fatalf("%q 不该算合法图片名", n)
		}
	}
}

func TestRoleAssetFallsBackToOtherExtensions(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "figure.png"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// meta.json 写的是 webp、磁盘上只有 png：必须靠扩展名兜底找到，否则「图在却不显示」
	if got := RoleAsset(dir, "figure.webp", RoleFigureBase); got != "figure.png" {
		t.Fatalf("应回落到 png，得到 %q", got)
	}
	// 写的就是磁盘上的名字 → 原样返回
	if got := RoleAsset(dir, "figure.png", RoleFigureBase); got != "figure.png" {
		t.Fatalf("得到 %q", got)
	}
	// 主名不带扩展名也照样找
	if got := RoleAsset(dir, "", RoleFigureBase); got != "figure.png" {
		t.Fatalf("按主名兜底应找到，得到 %q", got)
	}
	// 目录里什么都没有
	if got := RoleAsset(t.TempDir(), "figure.webp", RoleFigureBase); got != "" {
		t.Fatalf("找不到应给空串，得到 %q", got)
	}
}

func TestValidRoleDirName(t *testing.T) {
	for _, n := range []string{"秦始皇", "alice", "a b", "角色-1"} {
		if !ValidRoleDirName(n) {
			t.Fatalf("%q 应是合法目录名", n)
		}
	}
	bad := []string{"", ".hidden", "..", "a/b", `a\b`, "a:b", "a*b", "a?b", `a"b`, "a<b", "a>b", "a|b",
		strings.Repeat("x", 129)}
	for _, n := range bad {
		if ValidRoleDirName(n) {
			t.Fatalf("%q 不该算合法目录名", n)
		}
	}
}

func TestSanitizeRoleDirName(t *testing.T) {
	cases := map[string]string{
		"  Alice  ": "Alice",
		"A/B":       "AB", // 路径分隔符不能留（名字可以带 /）
		`A\B`:       "AB",
		"a:b*c?":    "abc",
		"名字.":       "名字",       // 尾随点会被系统吞掉
		"":          "督休专员",     // 抹平后为空 → 给一个可用名
		"CON":       "_CON",     // Windows 保留名加前缀，否则 mkdir 直接失败
		"con.txt":   "_con.txt", // 保留名的判断只看第一个点之前
		"正常名字":      "正常名字",
	}
	for in, want := range cases {
		if got := SanitizeRoleDirName(in); got != want {
			t.Fatalf("SanitizeRoleDirName(%q) = %q，期望 %q", in, got, want)
		}
	}
	// 超长名字要截断（目录名上限 255 字节，中文按 3 字节算）
	long := strings.Repeat("字", 100)
	if n := len([]rune(SanitizeRoleDirName(long))); n > 60 {
		t.Fatalf("应截断到 60 个字符以内，得到 %d", n)
	}
}

func TestRoleIdentityIgnoresCaseAndSeparators(t *testing.T) {
	// 查重判据是 RoleIdentity 相等，不是字符串相等：Windows 目录名不分大小写
	if RoleIdentity("Alice") != RoleIdentity("alice") {
		t.Fatal("大小写不同的名字会撞同一个目录，必须算重名")
	}
	if RoleIdentity("A/B") != RoleIdentity("AB") {
		t.Fatal("sanitize 后撞同一个目录，必须算重名")
	}
	if RoleIdentity("张三") == RoleIdentity("李四") {
		t.Fatal("不同名字不该算重名")
	}
}

func TestScanRolesAndFigureDataURL(t *testing.T) {
	root := t.TempDir()
	makeRole(t, root, "秦始皇", "秦始皇", "figure.webp", []byte("fake-webp"))

	roles := ScanRoles(root)
	if len(roles) != 1 {
		t.Fatalf("应扫到 1 个督休专员，得到 %d", len(roles))
	}
	r := roles[0]
	if r.Name != "秦始皇" || !r.Enabled {
		t.Fatalf("身份/启用状态不符：%+v", r)
	}
	if r.FigurePath == "" || !strings.HasSuffix(r.FigurePath, "figure.webp") {
		t.Fatalf("立绘路径应指向真实文件，得到 %q", r.FigurePath)
	}
	if !strings.HasPrefix(r.Thumb, "data:image/webp;base64,") {
		t.Fatalf("没有预览图时应回退到立绘原图的 data URL，得到 %q", r.Thumb[:min(32, len(r.Thumb))])
	}

	// FigureDataURL 是同一件事的对外入口
	if got := FigureDataURL(root, "秦始皇"); !strings.HasPrefix(got, "data:image/webp;base64,") {
		t.Fatalf("得到 %q", got)
	}
	// 空名 / 不存在的名字 / 空根目录都给空串（调用方据此显示占位）
	for _, n := range []string{"", "  ", "不存在"} {
		if got := FigureDataURL(root, n); got != "" {
			t.Fatalf("FigureDataURL(%q) 应为空串，得到 %q", n, got)
		}
	}
	if got := FigureDataURL("", "秦始皇"); got != "" {
		t.Fatalf("空根目录应为空串，得到 %q", got)
	}
}

func TestScanRolesSkipsBrokenDirectories(t *testing.T) {
	root := t.TempDir()
	makeRole(t, root, "好的", "好的", "figure.webp", []byte("x"))
	// 没有 meta.json 的目录（可能是正在写入）跳过，不能让整个扫描失败
	if err := os.MkdirAll(filepath.Join(root, "空的"), 0o755); err != nil {
		t.Fatal(err)
	}
	// meta.json 是坏 JSON 的目录同样跳过
	bad := filepath.Join(root, "坏的")
	if err := os.MkdirAll(bad, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, RoleMetaFile), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 隐藏目录不参与（ValidRoleDirName 拒绝）
	if err := os.MkdirAll(filepath.Join(root, ".hidden"), 0o755); err != nil {
		t.Fatal(err)
	}
	roles := ScanRoles(root)
	if len(roles) != 1 || roles[0].Name != "好的" {
		t.Fatalf("只该扫到「好的」，得到 %+v", roles)
	}
	if ScanRoles(filepath.Join(root, "不存在")) != nil {
		t.Fatal("根目录不存在时应给 nil")
	}
}

// 立绘缺失的督休专员仍要列出（FigurePath 为空）：要被设置页看到才能改名 / 换图。
func TestScanRolesKeepsRoleWithoutFigure(t *testing.T) {
	root := t.TempDir()
	makeRole(t, root, "缺图", "缺图", "", nil)
	roles := ScanRoles(root)
	if len(roles) != 1 {
		t.Fatalf("缺立绘的专员不该消失，得到 %d", len(roles))
	}
	if roles[0].FigurePath != "" || roles[0].Thumb != "" {
		t.Fatalf("缺图时不该编出一个路径，得到 %+v", roles[0])
	}
}

func TestWriteRoleFileCreatesThenOverwrites(t *testing.T) {
	root := t.TempDir()
	if err := WriteRoleFile(root, "新专员", RoleFile{Name: "新专员", Enabled: true, Image: "figure.webp"}); err != nil {
		t.Fatalf("首次写入失败: %v", err)
	}
	// 目录名是名字的投影
	dir, ok := ResolveRoleDir(root, "新专员")
	if !ok {
		t.Fatal("写完应能按名字反查到目录")
	}
	if filepath.Base(dir) != "新专员" {
		t.Fatalf("目录名应是名字的投影，得到 %q", filepath.Base(dir))
	}
	got, ok := ReadRoleFile(root, "新专员")
	if !ok || got.Name != "新专员" || !got.Enabled {
		t.Fatalf("读回不符：%+v ok=%v", got, ok)
	}
	// 再写一次：覆盖同一份，不建出第二个目录
	if err := WriteRoleFile(root, "新专员", RoleFile{Name: "新专员", Enabled: false}); err != nil {
		t.Fatalf("覆盖写失败: %v", err)
	}
	if roles := ScanRoles(root); len(roles) != 1 {
		t.Fatalf("覆盖写不该新建目录，得到 %d 个", len(roles))
	}
	if got, _ := ReadRoleFile(root, "新专员"); got.Enabled {
		t.Fatal("覆盖写没生效（enabled 还是 true）")
	}
	// 名字全空 → 明确报错，不建「督休专员」这种默认目录
	if err := WriteRoleFile(root, "  ", RoleFile{}); err == nil {
		t.Fatal("名字全空时应报错")
	}
}

func TestWriteRoleFileAtRejectsBadDir(t *testing.T) {
	// 目标被同名文件占着 → MkdirAll 失败，必须报错而不是静默丢弃
	root := t.TempDir()
	blocked := filepath.Join(root, "占位")
	if err := os.WriteFile(blocked, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteRoleFileAt(blocked, RoleFile{Name: "x"}); err == nil {
		t.Fatal("目录位置被文件占着时应报错")
	}
}
