//go:build windows

package platform

import (
	"strings"
	"testing"

	"golang.org/x/sys/windows/registry"
)

// WebView2 预检的约束。
//
// 这里不测「本机装了没有」（换台干净的机器结论就变），只测**判断口径**：
// 什么算已安装、哪些位置要查、链接必须是 https、以及「文件在就必须认」这条
// 防误判的不变量 —— 误判会让能用的机器被挡在门外，比不提示更糟。

func TestWebView2VersionUsable(t *testing.T) {
	cases := []struct {
		pv   string
		want bool
	}{
		{"", false},
		{"   ", false},
		{"0.0.0.0", false}, // 微软文档：键在但 pv=0.0.0.0 等于未安装
		{"120.0.2210.144", true},
		{" 120.0.2210.144 ", true},
	}
	for _, c := range cases {
		if got := webView2VersionUsable(c.pv); got != c.want {
			t.Errorf("webView2VersionUsable(%q) = %v, want %v", c.pv, got, c.want)
		}
	}
}

// TestWebView2RegistryQueriesCoverAllLocations 三个登记位置一个都不能少：
// 漏掉 32 位视图时，64 位进程在「装在 WOW6432Node」的机器上会误判缺失。
func TestWebView2RegistryQueriesCoverAllLocations(t *testing.T) {
	qs := webView2RegistryQueries()
	if len(qs) != 3 {
		t.Fatalf("应查 3 处，实得 %d", len(qs))
	}
	var hasHKLM64, hasHKLM32, hasHKCU bool
	for _, q := range qs {
		if !strings.Contains(q.path, webView2ClientGUID) {
			t.Errorf("查询路径缺 GUID: %q", q.path)
		}
		if q.access&registry.QUERY_VALUE == 0 {
			t.Errorf("查询缺 QUERY_VALUE 权限: %+v", q)
		}
		switch {
		case q.root == registry.LOCAL_MACHINE && q.access&registry.WOW64_64KEY != 0:
			hasHKLM64 = true
		case q.root == registry.LOCAL_MACHINE && q.access&registry.WOW64_32KEY != 0:
			hasHKLM32 = true
		case q.root == registry.CURRENT_USER:
			hasHKCU = true
		}
	}
	if !hasHKLM64 || !hasHKLM32 || !hasHKCU {
		t.Fatalf("覆盖不全: HKLM64=%v HKLM32=%v HKCU=%v", hasHKLM64, hasHKLM32, hasHKCU)
	}
}

// TestWebView2DownloadURL 下载页必须是 https（不能是 http，也不许空）。
func TestWebView2DownloadURL(t *testing.T) {
	if !strings.HasPrefix(webView2DownloadURL, "https://") {
		t.Fatalf("下载地址必须是 https: %q", webView2DownloadURL)
	}
	if !strings.Contains(webView2DownloadURL, "LinkId=2124703") {
		t.Fatalf("下载地址应为微软 WebView2 bootstrapper 短链: %q", webView2DownloadURL)
	}
}

// TestProbeWebView2NeverContradictsInstallDir 安装目录里有 msedgewebview2.exe 时，
// 探测结果不许说「没装」—— 这条在没装运行时的机器上自动跳过（文件不存在就不成立）。
func TestProbeWebView2NeverContradictsInstallDir(t *testing.T) {
	dirVer := webView2VersionFromInstallDir()
	if dirVer == "" {
		t.Skip("本机没有 EdgeWebView 安装目录，跳过（该不变量在此环境不成立）")
	}
	p := ProbeWebView2()
	if !p.Found {
		t.Fatalf("安装目录存在版本 %q，但 ProbeWebView2 报缺失（certain=%v）", dirVer, p.Certain)
	}
	if p.Version == "" {
		t.Fatal("Found=true 时必须带版本号")
	}
}
