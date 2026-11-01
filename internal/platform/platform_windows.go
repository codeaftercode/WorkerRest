//go:build windows

package platform

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// WebView2 运行时预检（Windows）。
//
// 为什么项目自己要探一次：Wails v3.0.0 没有 v2 那套 install mode / bootstrapper
// （`WebviewInstallMode` 在 v3 里不存在），缺运行时时唯一的表现是
// GetAvailableCoreWebView2BrowserVersionString 失败 → handleError → 写 Logger 后整个
// 进程退出。而发布版是 GUI 子系统（-H windowsgui，无控制台、无弹窗），用户看到的
// 就是「双击没反应」—— 这是最典型的「看起来装坏了」的发布事故。
//
// 判据取微软官方推荐的注册表口径（EdgeUpdate\Clients\{GUID} 的 pv），外加
// msedgewebview2.exe 的目录存在性做二次确认。**只在「确认缺失」时拦人**：
// 读不到（权限、非标准部署）一律放行 —— 探测本身的误判比不提示更糟。

const (
	// webView2ClientGUID 是 WebView2 Runtime 在 EdgeUpdate 里的固定产品 GUID
	// （微软文档「Detect if the WebView2 Runtime is already installed」给的同一个值）。
	webView2ClientGUID = `{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}`
	// webView2DownloadURL 微软官方 Evergreen Bootstrapper 的短链（fwlink 会跳到当前语言页）。
	webView2DownloadURL = "https://go.microsoft.com/fwlink/p/?LinkId=2124703"
)

// WebView2DownloadURL 返回 WebView2 运行时官方下载页短链（给 main 包提示用户用）。
func WebView2DownloadURL() string { return webView2DownloadURL }

// HiddenSysProcAttr 返回「隐藏子进程窗口」的 SysProcAttr（Windows）。
//
// 自更新/重启拉起子进程时，默认会闪一个控制台黑框；HideWindow + CREATE_NO_WINDOW
// （0x00000200）把它藏起来。其它平台无此概念，返回 nil（即不设置，走默认行为）。
func HiddenSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x00000200}
}

// lastInputInfo 对应 LASTINPUTINFO { UINT cbSize; DWORD dwTime; }
// x64 下 sizeof = 8，dwTime 在偏移 4（Go 的结构体布局与之一致，无需手算偏移）。
type lastInputInfo struct {
	cbSize uint32
	dwTime uint32
}

var (
	modUser32            = syscall.NewLazyDLL("user32.dll")
	procGetLastInputInfo = modUser32.NewProc("GetLastInputInfo")
)

// LastInputTick 读系统「最后一次键鼠输入」的毫秒 tick；第二个返回值表示是否读到。
// 只用它做「两次采样是否相同」的比较，**绝不与墙钟相减**（零点在开机时刻）。
func LastInputTick() (int64, bool) {
	var li lastInputInfo
	li.cbSize = uint32(unsafe.Sizeof(li))
	r, _, _ := procGetLastInputInfo.Call(uintptr(unsafe.Pointer(&li)))
	if r == 0 {
		return 0, false
	}
	return int64(li.dwTime), true
}

type registryQuery struct {
	root   registry.Key
	path   string
	access uint32
}

// webView2RegistryQueries 运行时可能被登记的三处：
//   - HKLM 64 位视图（常规的机器级安装）
//   - HKLM 32 位视图（在 WOW6432Node 下；64 位进程必须显式带上 WOW64_32KEY 才看得到
//     —— 2026-10 实测本机就只登记在这一处：pv 在 WOW6432Node，64 位视图是空的）
//   - HKCU（免管理员的「仅为我安装」）
func webView2RegistryQueries() []registryQuery {
	client := `\Clients\` + webView2ClientGUID
	access := uint32(registry.QUERY_VALUE)
	return []registryQuery{
		{registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\EdgeUpdate` + client, access | registry.WOW64_64KEY},
		{registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\EdgeUpdate` + client, access | registry.WOW64_32KEY},
		{registry.CURRENT_USER, `Software\Microsoft\EdgeUpdate` + client, access | registry.WOW64_64KEY},
	}
}

// ProbeWebView2 探测本机 WebView2 运行时。
func ProbeWebView2() WebView2Probe {
	unknown := false
	for _, q := range webView2RegistryQueries() {
		k, err := registry.OpenKey(q.root, q.path, q.access)
		if err != nil {
			// 键不存在才是「这一处没有」；别的错误（权限等）不构成缺失证据。
			if !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
				unknown = true
			}
			continue
		}
		pv, _, err := k.GetStringValue("pv")
		_ = k.Close()
		if err != nil || !webView2VersionUsable(pv) {
			// 键在但 pv 为空/0.0.0.0：微软文档明确这种情况等于「运行时已不在了」。
			continue
		}
		return WebView2Probe{Version: pv, Found: true}
	}
	// 注册表没读到，再用安装目录兜一层：某些离线部署/企业分发只落文件不写 EdgeUpdate。
	if v := webView2VersionFromInstallDir(); v != "" {
		return WebView2Probe{Version: v, Found: true}
	}
	return WebView2Probe{Certain: !unknown}
}

// webView2VersionUsable pv 为 "0.0.0.0" 或空串时，微软的判定是「未安装」。
func webView2VersionUsable(pv string) bool {
	pv = strings.TrimSpace(pv)
	return pv != "" && pv != "0.0.0.0"
}

// webView2InstallRoots 返回 EdgeWebView\Application 的候选根目录。
//
// 不只依赖 %ProgramFiles(x86)% 这类环境变量：某些启动方式下它们根本不存在
// （实测由 bash 拉起的子进程里 ProgramFiles / ProgramFiles(x86) / LOCALAPPDATA
// 全是空串，路径会退化成 "\Microsoft\EdgeWebView\Application" 这种无效值）。
// 所以再用 %SystemDrive% 拼出两条标准路径，最后才用 LOCALAPPDATA（免管理员安装）。
func webView2InstallRoots() []string {
	var roots []string
	add := func(bases ...string) {
		for _, b := range bases {
			if b == "" {
				continue
			}
			roots = append(roots, filepath.Join(b, "Microsoft", "EdgeWebView", "Application"))
		}
	}
	add(os.Getenv("ProgramFiles(x86)"), os.Getenv("ProgramFiles"))
	if drive := os.Getenv("SystemDrive"); drive != "" {
		add(drive+`\Program Files (x86)`, drive+`\Program Files`)
	}
	add(os.Getenv("LOCALAPPDATA"))
	return roots
}

// webView2VersionFromInstallDir 在常见的安装根目录里找 msedgewebview2.exe，
// 返回其所在的版本目录名（形如 120.0.2210.144）。
func webView2VersionFromInstallDir() string {
	for _, root := range webView2InstallRoots() {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			exe := filepath.Join(root, e.Name(), "msedgewebview2.exe")
			if st, err := os.Stat(exe); err == nil && !st.IsDir() {
				return e.Name()
			}
		}
	}
	return ""
}
