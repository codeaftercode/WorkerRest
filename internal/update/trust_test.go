package update

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSanitizeUpdateURLs 保持顺序、丢不合规项。
func TestSanitizeUpdateURLs(t *testing.T) {
	in := []string{
		"https://a/version.json",
		"http://evil/version.json",
		"  https://b/version.json  ",
		"",
		"ftp://c/x",
	}
	got := SanitizeUpdateURLs(in)
	want := []string{"https://a/version.json", "https://b/version.json"}
	if len(got) != len(want) {
		t.Fatalf("过滤结果 %v，期望 %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("过滤结果[%d]=%q，期望 %q", i, got[i], want[i])
		}
	}
}

// TestUpdateClientRejectsRedirectDowngrade https 地址 302 到 http 必须被拦。
// 否则“源是 https”只是假象，实际内容仍可能走明文（或被中间人改写）。
func TestUpdateClientRejectsRedirectDowngrade(t *testing.T) {
	var target string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target, http.StatusFound)
	}))
	defer srv.Close()
	target = "http://evil.example/package.zip" // 非回环 http

	// srv 自己是回环 http（允许），但重定向目标不是 → 必须在跟随重定向时报错
	client := NewUpdateClient(15_000_000_000) // 15s
	res, err := client.Get(srv.URL)
	if err == nil {
		_ = res.Body.Close()
		t.Fatalf("重定向到明文 http 应报错，却成功了（%s）", res.Status)
	}
	if !strings.Contains(err.Error(), "https") {
		t.Fatalf("错误信息应说明协议要求，实得 %v", err)
	}
}

func TestParseSHA256(t *testing.T) {
	valid := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	got, err := ParseSHA256(valid)
	if err != nil || got != valid {
		t.Fatalf("合法值应原样通过：%q %v", got, err)
	}
	// 大小写与 sha256: 前缀都该被规范化
	if got, err := ParseSHA256("SHA256:" + strings.ToUpper(valid)); err != nil || got != valid {
		t.Fatalf("大写/带前缀应规范化，实得 %q %v", got, err)
	}
	for _, bad := range []string{"", "   ", "deadbeef", valid + "0", strings.Repeat("z", 64)} {
		if _, err := ParseSHA256(bad); err == nil {
			t.Errorf("%q 应被判非法", bad)
		}
	}
}

func TestVerifySHA256(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "pkg.zip")
	if err := os.WriteFile(p, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	good, err := sha256File(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifySHA256(p, good); err != nil {
		t.Fatalf("哈希一致应通过：%v", err)
	}
	if err := VerifySHA256(p, strings.Repeat("0", 64)); err == nil {
		t.Fatal("哈希不符应报错")
	}
}

// TestUpdateLimitsAreSane 上限本身要合理：比实际产物（约 15 MiB）宽，
// 但不能宽到“等于没有上限”。改动这些常量时先看这里。
func TestUpdateLimitsAreSane(t *testing.T) {
	if MaxPackageBytes < 32<<20 || MaxPackageBytes > 256<<20 {
		t.Fatalf("更新包上限 %d 不在合理区间（32~256 MiB）", MaxPackageBytes)
	}
	if MaxExtractTotalBytes < MaxPackageBytes {
		t.Fatalf("解压上限（%d）不该小于包体积上限（%d）", MaxExtractTotalBytes, MaxPackageBytes)
	}
	if MaxVersionJSONBytes > 4<<20 {
		t.Fatalf("清单上限 %d 过大", MaxVersionJSONBytes)
	}
	if MaxExtractEntries < 64 {
		t.Fatalf("条目上限 %d 过小，正常更新包可能装不下", MaxExtractEntries)
	}
}
