package role

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 督休专员**改名**的目录名侧契约（2026-11-14）：
// 名字是身份，目录名只是它的文件系统投影。投影可以“不好看”，但**不能反过来毁掉身份**：
// 只要 SanitizeRoleDirName 产出一个 ValidRoleDirName 不认的名字，扫描就会跳过整个目录 ——
// meta.json 还在、立绘还在，可这位专员从列表里蒸发了：改不了名、删不掉、预览不到。
//
// 这组用例把三件事钉死：
//  1. 任何脏名字净化后都必须“可被扫描”（TestSanitizeRoleDirNameAlwaysScannable）；
//  2. 去重循环必须有出口，不能因为一个非法/无权限的路径就无限自增（TestUniqueRoleDirTerminates*）；
//  3. 只差大小写的改名不许退让成 "Doctor (2)"（TestRenameRoleDirCaseOnly）。

// dirtyRoleNames 是一份“用户真能打出来 / 粘进来 / 从文件名继承”的脏名字语料。
var dirtyRoleNames = []string{
	"",
	" ",
	"   ",
	".",
	"..",
	"...",
	"  .  ",
	"/",
	"///",
	"\\",
	"a/b",
	"a\\b",
	"a:b",
	`a*b?c"d<e>f|g`,
	"../../etc/passwd",
	".hidden",
	"..hidden",
	".git",
	"CON",
	"con",
	"con.txt",
	"AUX",
	"COM1.txt",
	"LPT9",
	"aux ",
	"医生.",
	"医生 ",
	"医生 . . ",
	" 医生 ",
	"中文 空格",
	"Victoria Nightingale",
	"emoji😀🎉",
	"医\u00e9",  // NFC：é 单码位
	"医e\u0301", // NFD：e + 组合重音
	"医\n生",     // 控制字符（换行）
	"医\t生",     // 控制字符（制表）
	"a\x00b",   // NUL
	"a\x01b",   // SOH
	"a\x7fb",   // DEL
	"100%",
	"%USERPROFILE%",
	"$HOME",
	strings.Repeat("长", 80),  // 240 字节：只按 rune 截会更长
	strings.Repeat("a", 200), // 200 字节：超出 ValidRoleDirName 的 128
	"." + strings.Repeat("长", 60),
	"名字里带 (2)",
}

// 净化后的名字必须能被扫描器认成“合法专员目录”，而且是**幂等**的：
// 同一个名字反复保存不能每次都换一个目录名（那会让目录名一路漂移）。
func TestSanitizeRoleDirNameAlwaysScannable(t *testing.T) {
	for _, name := range dirtyRoleNames {
		got := SanitizeRoleDirName(name)
		if !ValidRoleDirName(got) {
			t.Errorf("净化结果不是合法目录名：%q → %q（len=%d）", name, got, len(got))
			continue
		}
		if len(got) > maxRoleDirNameBytes+1 {
			t.Errorf("净化结果过长：%q → %q（%d 字节）", name, got, len(got))
		}
		if strings.HasPrefix(got, ".") {
			t.Errorf("净化结果以点开头（扫描会跳过）：%q → %q", name, got)
		}
		if strings.HasSuffix(got, ".") || strings.HasSuffix(got, " ") {
			t.Errorf("净化结果尾部有点/空格（系统会吞掉）：%q → %q", name, got)
		}
		for _, r := range got {
			if r < 0x20 || r == 0x7f {
				t.Errorf("净化结果仍含控制字符：%q → %q", name, got)
				break
			}
		}
		if again := SanitizeRoleDirName(got); again != got {
			t.Errorf("净化不幂等：%q → %q → %q", name, got, again)
		}
	}
}

// 几条关键口径的具体形态（不只是“合法”，而是**符合约定**）。
func TestSanitizeRoleDirNameCases(t *testing.T) {
	cases := map[string]string{
		"医生":      "医生",
		"  医生  ":  "医生",
		"医生.":     "医生", // 尾随点会被系统吞掉，必须先去掉
		"医生 . . ": "医生",
		"a/b:c":   "abc",      // 九个保留字符直接剔除
		".hidden": "_.hidden", // 点开头会被扫描跳过，加前缀救回来
		"..":      "督休专员",     // 只剩点时回落默认
		"///":     "督休专员",
		"   ":     "督休专员",
		"CON":     "_CON", // 设备名，mkdir 会失败
		"con.txt": "_con.txt",
		"医\n生":    "医生", // 控制字符剔除（留着会让 Stat 报“语法不正确”）
		"a\x01b":  "ab",
	}
	for in, want := range cases {
		if got := SanitizeRoleDirName(in); got != want {
			t.Errorf("SanitizeRoleDirName(%q) = %q，期望 %q", in, got, want)
		}
	}
	// 长名字：按 **字节** 守住 128 的上限，不能只按 rune 截
	long := SanitizeRoleDirName(strings.Repeat("长", 80))
	if len(long) > maxRoleDirNameBytes {
		t.Fatalf("长名字没按字节截断：%d 字节", len(long))
	}
	if !ValidRoleDirName(long) {
		t.Fatalf("长名字截断后仍不合法：%q", long)
	}
}

// UniqueRoleDir 遇到“既不是存在、也不是不存在”的 Stat 错误时必须有出口。
// 之前这里是 `for { ... i++ }`，Stat 一直报第三种错误 → 自增到天荒地老：
// 用户点“确定”改名，设置窗口整窗卡死，只能杀进程。
func TestUniqueRoleDirTerminatesOnStatError(t *testing.T) {
	// 构造一个 Stat 会返回 ENOTDIR 的 root：把普通文件当成父目录用。
	base := t.TempDir()
	file := filepath.Join(base, "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(file, "supervisors")

	done := make(chan string, 1)
	go func() { done <- UniqueRoleDir(root, "医生", "") }()
	select {
	case got := <-done:
		if got == "" {
			t.Fatal("应返回一个候选路径（让写入层去报错），实际为空")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("UniqueRoleDir 没能在 3 秒内返回：Stat 报的不是 IsNotExist 时不能无限自增")
	}
}

// 去重：已占用 → (2)/(3)；判“自己”要大小写不敏感（Windows 下 Doctor == doctor）。
func TestUniqueRoleDirDedup(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"医生", "医生 (2)"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if got := UniqueRoleDir(root, "医生", ""); filepath.Base(got) != "医生 (3)" {
		t.Fatalf("应退让到 “医生 (3)”，实际 %q", filepath.Base(got))
	}
	self := filepath.Join(root, "医生")
	if got := UniqueRoleDir(root, "医生", self); got != self {
		t.Fatalf("应认出这是自己（直接返回），实际 %q", got)
	}
	// 大小写不同也仍然是自己：否则只改大小写会退让成 "医生 (2)"
	if got := UniqueRoleDir(root, "医生", filepath.Join(root, "医生")); got != self {
		t.Fatalf("大小写不敏感地认自己失败：%q", got)
	}
}

// 只差大小写的改名：目录应当就地改大小写，而不是退让成 "Doctor (2)"。
func TestRenameRoleDirCaseOnly(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "doctor")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	got := RenameRoleDir(root, dir, "Doctor")
	if filepath.Base(got) != "Doctor" {
		t.Fatalf("应就地改成 Doctor，实际 %q", filepath.Base(got))
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 1 {
		t.Fatalf("不该多出一个目录：%v", namesOf(entries))
	}
}

// 目录改名失败（被句柄占住 / 源目录已不在）不能 panic，也不能把路径改成不存在的目标：
// 返回原目录，让调用方继续按 meta.json 的 name 反查。
func TestRenameRoleDirKeepsOldDirOnFailure(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "不存在")
	if got := RenameRoleDir(root, missing, "新名字"); got != missing {
		t.Fatalf("失败时应返回原目录，实际 %q", got)
	}
	// 目标位被一个**同名的文件**占住：UniqueRoleDir 会退让成 “新名字 (2)”，仍然成功
	dir := filepath.Join(root, "老名字")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "新名字"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := RenameRoleDir(root, dir, "新名字")
	if filepath.Base(got) != "新名字 (2)" {
		t.Fatalf("同名文件占位时应退让成 “新名字 (2)”，实际 %q", filepath.Base(got))
	}
}

func namesOf(entries []os.DirEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}
