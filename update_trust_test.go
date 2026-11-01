package main

import (
	"archive/zip"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"workerrest/internal/urltls"
)

// 更新链信任边界的回归（审计 BLOCKER-2）。
//
// 这组用例守的是「远程内容不能绕开校验落到本地并被执行」：
// 协议降级、重定向到明文、缺哈希、哈希不符、超体积、zip bomb —— 任何一条漏掉，
// 前面所有校验都没有意义。断言都打在**行为**上（是否拒绝 / 文件是否留下），
// 不依赖具体错误措辞之外的实现细节。

func TestCheckUpdateURL(t *testing.T) {
	cases := []struct {
		url  string
		ok   bool
		note string
	}{
		{"https://example.com/version.json", true, "https 正常"},
		{"HTTPS://Example.COM/x", true, "scheme 大小写不敏感"},
		{"http://example.com/version.json", false, "明文 http 必须拒绝"},
		{"http://127.0.0.1:8080/x", true, "回环 http（单测/本机调试）"},
		{"http://localhost:9000/x", true, "localhost 回环"},
		{"http://[::1]:9000/x", true, "IPv6 回环"},
		{"http://10.0.0.5/x", false, "内网地址不算回环"},
		{"ftp://example.com/x", false, "非 http(s) 协议"},
		{"file:///C:/x.zip", false, "本地文件协议"},
		{"javascript:alert(1)", false, "伪协议"},
		{"", false, "空串"},
		{"   ", false, "全空白"},
		{"https://", false, "缺主机名"},
	}
	for _, c := range cases {
		err := urltls.CheckUpdateURL(c.url)
		if c.ok && err != nil {
			t.Errorf("%s：%q 应放行，却报错 %v", c.note, c.url, err)
		}
		if !c.ok && err == nil {
			t.Errorf("%s：%q 应拒绝，却放行了", c.note, c.url)
		}
	}
}

// TestSanitizeUpdateURLs 保持顺序、丢不合规项。
func TestSanitizeUpdateURLs(t *testing.T) {
	in := []string{
		"https://a/version.json",
		"http://evil/version.json",
		"  https://b/version.json  ",
		"",
		"ftp://c/x",
	}
	got := sanitizeUpdateURLs(in)
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
// 否则「源是 https」只是假象，实际内容仍可能走明文（或被中间人改写）。
func TestUpdateClientRejectsRedirectDowngrade(t *testing.T) {
	var target string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target, http.StatusFound)
	}))
	defer srv.Close()
	target = "http://evil.example/package.zip" // 非回环 http

	// srv 自己是回环 http（允许），但重定向目标不是 → 必须在跟随重定向时报错
	client := newUpdateClient(updateTimeout)
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
	got, err := parseSHA256(valid)
	if err != nil || got != valid {
		t.Fatalf("合法值应原样通过：%q %v", got, err)
	}
	// 大小写与 sha256: 前缀都该被规范化
	if got, err := parseSHA256("SHA256:" + strings.ToUpper(valid)); err != nil || got != valid {
		t.Fatalf("大写/带前缀应规范化，实得 %q %v", got, err)
	}
	for _, bad := range []string{"", "   ", "deadbeef", valid + "0", strings.Repeat("z", 64)} {
		if _, err := parseSHA256(bad); err == nil {
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
	if err := verifySHA256(p, good); err != nil {
		t.Fatalf("哈希一致应通过：%v", err)
	}
	if err := verifySHA256(p, strings.Repeat("0", 64)); err == nil {
		t.Fatal("哈希不符应报错")
	}
}

// TestDownloadFileSizeCap Content-Length 超上限时直接拒绝（不落盘）。
func TestDownloadFileSizeCap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1048576")
		_, _ = w.Write([]byte("small"))
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "x.zip")
	if err := downloadFileLimited(srv.URL, dest, 1024); err == nil {
		t.Fatal("超过上限应报错")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("拒绝后不该留下文件（err=%v）", err)
	}
}

// TestDownloadFileStreamCap 服务端不发 Content-Length（chunked）时，
// 仍要在实际写入超过上限处中断并删掉半截文件。
func TestDownloadFileStreamCap(t *testing.T) {
	body := strings.Repeat("A", 4096)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 不设 Content-Length：httptest 会退化成 chunked
		w.(http.Flusher).Flush()
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "x.zip")
	if err := downloadFileLimited(srv.URL, dest, 1024); err == nil {
		t.Fatal("超过上限应报错")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("中断后不该留下半截文件（err=%v）", err)
	}
}

// TestExtractZipLimits 条目数与解压总量两个上限。
func TestExtractZipLimits(t *testing.T) {
	mk := func(t *testing.T, files map[string]string) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "pkg.zip")
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		zw := zip.NewWriter(f)
		for name, body := range files {
			w, err := zw.Create(name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write([]byte(body)); err != nil {
				t.Fatal(err)
			}
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		return p
	}

	t.Run("条目数超限", func(t *testing.T) {
		pkg := mk(t, map[string]string{"a": "1", "b": "2", "c": "3"})
		out := t.TempDir()
		if err := extractZipLimited(pkg, out, maxExtractTotalBytes, 2); err == nil {
			t.Fatal("条目数超限应报错")
		}
	})

	t.Run("解压总量超限", func(t *testing.T) {
		pkg := mk(t, map[string]string{"a": strings.Repeat("x", 2048)})
		out := t.TempDir()
		if err := extractZipLimited(pkg, out, 1024, maxExtractEntries); err == nil {
			t.Fatal("解压总量超限应报错")
		}
	})

	t.Run("正常包通过", func(t *testing.T) {
		pkg := mk(t, map[string]string{"rest.exe": "EXE", "config/settings.json": "{}"})
		out := t.TempDir()
		if err := extractZipLimited(pkg, out, maxExtractTotalBytes, maxExtractEntries); err != nil {
			t.Fatalf("正常包应通过：%v", err)
		}
		if b, err := os.ReadFile(filepath.Join(out, "rest.exe")); err != nil || string(b) != "EXE" {
			t.Fatalf("内容不符：%v %q", err, b)
		}
	})
}

// TestUpdateLimitsAreSane 上限本身要合理：比实际产物（约 15 MiB）宽，
// 但不能宽到「等于没有上限」。改动这些常量时先看这里。
func TestUpdateLimitsAreSane(t *testing.T) {
	if maxPackageBytes < 32<<20 || maxPackageBytes > 256<<20 {
		t.Fatalf("更新包上限 %d 不在合理区间（32~256 MiB）", maxPackageBytes)
	}
	if maxExtractTotalBytes < maxPackageBytes {
		t.Fatalf("解压上限（%d）不该小于包体积上限（%d）", maxExtractTotalBytes, maxPackageBytes)
	}
	if maxVersionJSONBytes > 4<<20 {
		t.Fatalf("清单上限 %d 过大", maxVersionJSONBytes)
	}
	if maxExtractEntries < 64 {
		t.Fatalf("条目上限 %d 过小，正常更新包可能装不下", maxExtractEntries)
	}
	// 生产路径用的就是常量本身（而不是测试里的小上限）：钉一下包装函数真的没漏传。
	if err := downloadFile("http://evil.example/x", filepath.Join(t.TempDir(), "x")); err == nil {
		t.Fatal("downloadFile 生产路径也应在协议层拒绝明文 http")
	}
	_ = fmt.Sprintf("%d", maxExtractTotalBytes)
}
