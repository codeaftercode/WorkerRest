package main

import (
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// WebView2 运行时预检。
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

	// MessageBoxW 的 uType 位
	mbOK            = 0x00000000
	mbIconError     = 0x00000010
	mbSetForeground = 0x00010000
	mbTopmost       = 0x00040000
)

// webview2Probe 探测结果。
//
// Found=false 表示三个注册表位置与两个安装目录都没找到运行时；此时
// Certain 才说明「这不是权限问题，是真的没装」—— 只有这种情况才该拦人。
type webview2Probe struct {
	Version string
	Found   bool
	Certain bool
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

// probeWebView2 探测本机 WebView2 运行时。
func probeWebView2() webview2Probe {
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
		return webview2Probe{Version: pv, Found: true}
	}
	// 注册表没读到，再用安装目录兜一层：某些离线部署/企业分发只落文件不写 EdgeUpdate。
	if v := webView2VersionFromInstallDir(); v != "" {
		return webview2Probe{Version: v, Found: true}
	}
	return webview2Probe{Certain: !unknown}
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

// promptWebView2Missing 提示用户并打开官方下载页。
//
// 用 MessageBoxW 而不是 Wails 的对话框：这时应用还没建起来，WebView2 也渲染不了 HTML，
// 能用的只有系统原生消息框。MessageBoxW 是阻塞调用，用户点掉才继续 —— 正好当「等用户
// 看完」的同步点。
func promptWebView2Missing() {
	const text = "本程序需要 Microsoft Edge WebView2 运行时才能显示界面，" +
		"当前系统未检测到它。\r\n\r\n" +
		"点「确定」后会打开微软官方下载页，安装完成后重新打开本程序即可。\r\n" +
		"（体积很小，普通权限安装，不需要管理员。）"
	const title = "吃饼人也敢休息？— 缺少 WebView2 运行时"

	textPtr, err := windows.UTF16PtrFromString(text)
	if err != nil {
		return
	}
	titlePtr, err := windows.UTF16PtrFromString(title)
	if err != nil {
		return
	}
	msgBoxW.Call(0,
		uintptr(unsafe.Pointer(textPtr)),
		uintptr(unsafe.Pointer(titlePtr)),
		uintptr(mbOK|mbIconError|mbSetForeground|mbTopmost))

	if err := openExternalURL(webView2DownloadURL); err != nil {
		log.Printf("[boot] 打开 WebView2 下载页失败: %v", err)
	}
}

var (
	user32  = windows.NewLazySystemDLL("user32.dll")
	msgBoxW = user32.NewProc("MessageBoxW")
)

// openExternalURL 用 ShellExecuteW("open") 交给默认浏览器。
//
// 不走 exec.Command("rundll32"/"cmd /c start")：那两条会拉起子进程，在限制子进程的
// 安全策略下直接失败，且容易闪出黑框；ShellExecute 是 shell 的既有入口，行为与用户
// 双击链接一致。
func openExternalURL(url string) error {
	verb, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	target, err := windows.UTF16PtrFromString(url)
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, verb, target, nil, nil, windows.SW_SHOWNORMAL)
}
