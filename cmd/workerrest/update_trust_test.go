package main

import (
	"archive/zip"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"workerrest/internal/update"
	"workerrest/internal/urltls"
)

// 更新链信任边界的回归（审计 BLOCKER-2）。
//
// 纯信任函数（URL 白名单 / sha256 / 重定向降级 / 上限常量）已下沉到 internal/update，
// 对应测试见 internal/update/trust_test.go。这里保留依赖 main 包下载/解压实现的用例。

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
		if err := extractZipLimited(pkg, out, update.MaxExtractTotalBytes, 2); err == nil {
			t.Fatal("条目数超限应报错")
		}
	})

	t.Run("解压总量超限", func(t *testing.T) {
		pkg := mk(t, map[string]string{"a": strings.Repeat("x", 2048)})
		out := t.TempDir()
		if err := extractZipLimited(pkg, out, 1024, update.MaxExtractEntries); err == nil {
			t.Fatal("解压总量超限应报错")
		}
	})

	t.Run("正常包通过", func(t *testing.T) {
		pkg := mk(t, map[string]string{"rest.exe": "EXE", "config/settings.json": "{}"})
		out := t.TempDir()
		if err := extractZipLimited(pkg, out, update.MaxExtractTotalBytes, update.MaxExtractEntries); err != nil {
			t.Fatalf("正常包应通过：%v", err)
		}
		if b, err := os.ReadFile(filepath.Join(out, "rest.exe")); err != nil || string(b) != "EXE" {
			t.Fatalf("内容不符：%v %q", err, b)
		}
	})
}
