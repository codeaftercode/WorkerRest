package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 版本比较纯函数回归：isNewerVersion / parseVersion 逐条对应 main.ts 的
// isNewerVersion（v 前缀、缺段都容忍，三段式比较，latest 严格大于 current 才算新版）。

func TestParseVersion(t *testing.T) {
	cases := []struct {
		in   string
		want []int
	}{
		{"1.0.0", []int{1, 0, 0}},
		{"v1.2.3", []int{1, 2, 3}},
		{"  v2.0.0  ", []int{2, 0, 0}},
		{"1.2", []int{1, 2}},
		{"1", []int{1}},
		{"", []int{0}}, // 空串拆出一段空 → 0
		{"abc", []int{0}},
		{"1.x.3", []int{1, 0, 3}}, // 非数字段按 0
	}
	for _, c := range cases {
		if got := parseVersion(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("parseVersion(%q) = %v，期望 %v", c.in, got, c.want)
		}
	}
}

func TestIsNewerVersion(t *testing.T) {
	cases := []struct {
		current, latest string
		want            bool
	}{
		{"1.0.0", "1.0.1", true},
		{"1.0.0", "1.1.0", true},
		{"1.0.0", "2.0.0", true},
		{"1.0.0", "1.0.0", false},
		{"1.0.1", "1.0.0", false},
		{"2.0.0", "1.9.9", false},
		// v 前缀
		{"1.0.0", "v1.1.0", true},
		{"v1.0.0", "1.1.0", true},
		// 缺段容忍：latest 补 0 后仍大
		{"1.0.0", "1.1", true},
		{"1.1", "1.1.0", false},
		{"1.0.0", "1.0", false},
		// 空 / 非法
		{"1.0.0", "", false},
		{"", "1.0.0", true},
	}
	for _, c := range cases {
		if got := isNewerVersion(c.current, c.latest); got != c.want {
			t.Errorf("isNewerVersion(%q, %q) = %v，期望 %v", c.current, c.latest, got, c.want)
		}
	}
}

func TestUpdateCheckResultToMap(t *testing.T) {
	r := updateCheckResult{Status: "update-available", Current: "1.0.0", Latest: "1.1.0", URLs: []string{"https://x", "https://y"}}
	m := r.toMap()
	if m["status"] != "update-available" || m["current"] != "1.0.0" ||
		m["latest"] != "1.1.0" {
		t.Fatalf("toMap 字段不符：%v", m)
	}
	urls, ok := m["url"].([]string)
	if !ok || len(urls) != 2 || urls[0] != "https://x" || urls[1] != "https://y" {
		t.Fatalf("toMap 的 url 应为字符串数组：%v", m["url"])
	}
	// error 状态不带 latest/url，只带 message
	e := updateCheckResult{Status: "error", Current: "1.0.0", Message: "boom"}
	em := e.toMap()
	if _, ok := em["latest"]; ok {
		t.Fatalf("error 状态不该带 latest：%v", em)
	}
	if em["message"] != "boom" {
		t.Fatalf("message 不符：%v", em)
	}
}

// TestFetchVersionJSONFallback 验证 sites 容错：第一项失败取第二项，全失败报错。
func TestFetchVersionJSONFallback(t *testing.T) {
	okBody := `{"version":"v2.0.0","date":"2026.10.18","url":["http://x/pkg.zip"],"changes":["a","b"]}`
	srvOK := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(okBody))
	}))
	defer srvOK.Close()
	srvFail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srvFail.Close()

	// 第一项失败、第二项成功 → 应拿到第二项的内容
	info, err := fetchVersionJSON([]string{srvFail.URL, srvOK.URL})
	if err != nil {
		t.Fatalf("应容错成功，却报错: %v", err)
	}
	if info.Version != "v2.0.0" {
		t.Fatalf("version 不符：%v", info.Version)
	}
	if len(info.URL) != 1 || info.URL[0] != "http://x/pkg.zip" {
		t.Fatalf("url 数组不符：%v", []string(info.URL))
	}
	if len(info.Changes) != 2 {
		t.Fatalf("changes 长度应为 2，实得 %v", info.Changes)
	}

	// 全部失败 → 报错
	if _, err := fetchVersionJSON([]string{srvFail.URL}); err == nil {
		t.Fatalf("全失败时应当返回 error")
	}
}

// TestVersionInfoParsesPackagedManifest 钉住「打包脚本产出的清单」与 Go 侧结构的
// 字段口径一致（键名见 scripts/make_portable.py 的 write_version_json）。
// 任何一边改名都会让线上更新瞬间失效，所以这层契约必须有测试，而不是靠「记得同步」。
func TestVersionInfoParsesPackagedManifest(t *testing.T) {
	doc := `{
	  "version": "v0.9.4",
	  "date": "2026.10.31",
	  "url": ["https://www.goodgoodlive.com/worker-rest/"],
	  "sites": ["https://www.goodgoodlive.com/worker-rest/version.json"],
	  "changes": ["1. 示例"],
	  "sha256": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
	  "size": 12345
	}`
	var info versionInfo
	if err := json.Unmarshal([]byte(doc), &info); err != nil {
		t.Fatalf("解析打包清单失败: %v", err)
	}
	if info.Version != "v0.9.4" || info.Date != "2026.10.31" {
		t.Fatalf("version/date 未对上: %+v", info)
	}
	if len(info.URL) != 1 || len(info.Sites) != 1 || len(info.Changes) != 1 {
		t.Fatalf("数组字段未对上: %+v", info)
	}
	if _, err := parseSHA256(info.SHA256); err != nil {
		t.Fatalf("清单里的 sha256 应能被接受: %v", err)
	}
	if info.Size != 12345 {
		t.Fatalf("size 未对上: %d", info.Size)
	}
}

// TestCheckForUpdateURLArray 端到端验证：真实 version.json 形状（url 为数组 + sha256）
// 经 checkForUpdate 落成状态 map 的 url 数组与 sha256，StartUpdate 侧能原样取出并按规则补全。
func TestCheckForUpdateURLArray(t *testing.T) {
	t.Setenv("WR_SETTINGS_PATH", filepath.Join(t.TempDir(), "settings.json"))
	const sum = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	body := `{"version":"v1.1.0","date":"2026.10.23",` +
		`"url":["https://nav.hcctc.cn/temp/","https://www.goodgoodlive.com/worker-rest/"],` +
		`"sites":["https://a/version.json","https://b/version.json"],` +
		`"sha256":"` + sum + `",` +
		`"changes":["修复了一些已知问题"]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	cfg := &Settings{raw: defaultSettingsMap()}
	cfg.SetSites([]string{srv.URL + "/version.json"})
	a := &App{cfg: cfg, mu: sync.Mutex{}}

	st := a.checkForUpdate()
	if st.Status != "update-available" {
		t.Fatalf("应为有新版，实得 %+v", st)
	}
	want := []string{"https://nav.hcctc.cn/temp/", "https://www.goodgoodlive.com/worker-rest/"}
	if !reflect.DeepEqual(st.URLs, want) {
		t.Fatalf("url 数组未透传：%v", st.URLs)
	}
	if st.SHA256 != sum {
		t.Fatalf("sha256 未透传：%q", st.SHA256)
	}
	// 状态 map 往返后 StartUpdate 仍能取出同一份数组与哈希
	m := st.toMap()
	if got := statusURLs(m); !reflect.DeepEqual(got, want) {
		t.Fatalf("statusURLs 回读不符：%v", got)
	}
	if got := statusString(m, "sha256"); got != sum {
		t.Fatalf("状态 map 里 sha256 丢失：%q", got)
	}
	// 补全规则：目录项 → <目录>/rest.zip
	if got := normalizePackageURL(want[1]); got != "https://www.goodgoodlive.com/worker-rest/rest.zip" {
		t.Fatalf("补全结果不符：%s", got)
	}
}

// TestCheckForUpdateRejectsUntrustedManifest 清单缺 sha256 / 只给 http 源时，
// 必须报 error 并说明原因，而不是对外宣称「有更新」再让人点进去失败。
func TestCheckForUpdateRejectsUntrustedManifest(t *testing.T) {
	const sum = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	cases := []struct {
		name string
		body string
		want string // message 里应出现的片段
	}{
		{
			name: "缺 sha256",
			body: `{"version":"v1.1.0","url":["https://a.example/rest.zip"]}`,
			want: "sha256",
		},
		{
			name: "sha256 格式不对",
			body: `{"version":"v1.1.0","url":["https://a.example/rest.zip"],"sha256":"deadbeef"}`,
			want: "sha256",
		},
		{
			name: "下载地址全是 http",
			body: `{"version":"v1.1.0","url":["http://a.example/rest.zip","ftp://b/x"],"sha256":"` + sum + `"}`,
			want: "https",
		},
		{
			name: "清单声明体积超上限",
			body: `{"version":"v1.1.0","url":["https://a.example/rest.zip"],` +
				`"sha256":"` + sum + `","size":99999999999}`,
			want: "超过上限",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("WR_SETTINGS_PATH", filepath.Join(t.TempDir(), "settings.json"))
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(c.body))
			}))
			defer srv.Close()

			cfg := &Settings{raw: defaultSettingsMap()}
			cfg.SetSites([]string{srv.URL + "/version.json"})
			a := &App{cfg: cfg, mu: sync.Mutex{}}

			st := a.checkForUpdate()
			if st.Status != "error" {
				t.Fatalf("应拒绝更新（error），实得 %+v", st)
			}
			if !strings.Contains(st.Message, c.want) {
				t.Fatalf("message 应含 %q，实得 %q", c.want, st.Message)
			}
		})
	}
}

// TestMergeConfigAdditive 验证解压目录配置合并语义：本地已有键不覆盖，本地无则新增。
func TestMergeConfigAdditive(t *testing.T) {
	dir := t.TempDir()
	// 第一个 json：新增键 newKey + 与本地同名的 oldKey（不应覆盖）
	if err := os.WriteFile(filepath.Join(dir, "patch1.json"), []byte(`{"newKey":"v1","oldKey":"fromPatch"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// 第二个 json：子目录里的文件也应被当作顶层键处理（整段新增）
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "patch2.json"), []byte(`{"another":"v2"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// 非 json 文件应被忽略
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("ignore me"), 0o644); err != nil {
		t.Fatal(err)
	}

	raw := map[string]any{"oldKey": "local", "keep": 1}
	if err := mergeConfigFromDir(dir, raw); err != nil {
		t.Fatalf("mergeConfigFromDir 报错: %v", err)
	}
	if raw["oldKey"] != "local" {
		t.Fatalf("本地已有键被覆盖：%v", raw["oldKey"])
	}
	if raw["newKey"] != "v1" {
		t.Fatalf("本地缺失键未新增：%v", raw["newKey"])
	}
	if raw["another"] != "v2" {
		t.Fatalf("子目录 json 顶层键未新增：%v", raw["another"])
	}
	if _, ok := raw["note.txt"]; ok {
		t.Fatalf("非 json 文件不应被合并")
	}
}

// TestUpdateResultPersistence 验证「发现新版本但未更新」的状态能持久化、能在重启后
// 恢复、并在更新成功后清除；同时验证版本已失效（不比当前新）时恢复会被清理。
func TestUpdateResultPersistence(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "settings.json")
	t.Setenv("WR_SETTINGS_PATH", tmp)

	cfg := &Settings{raw: defaultSettingsMap()}
	a := &App{cfg: cfg, mu: sync.Mutex{}}

	// 1) 发布/持久化一次「有更新」结果
	st := updateCheckResult{Status: "update-available", Current: appVersion, Latest: "9999.0.0", URLs: []string{"http://x/pkg.zip"}, Changes: []string{"新功能"}}
	a.publishUpdateStatus(st)

	// 2) 内存态与落盘都应反映该结果
	if a.updateStatus["status"] != "update-available" {
		t.Fatalf("内存态未更新：%v", a.updateStatus)
	}
	if got := cfg.LastUpdateResult(); got == nil || got["latest"] != "9999.0.0" {
		t.Fatalf("配置未持久化更新结果：%v", got)
	}
	// 落盘文件里应当含 lastUpdateResult
	data, err := os.ReadFile(tmp)
	if err != nil {
		t.Fatalf("配置未落盘: %v", err)
	}
	var onDisk map[string]any
	if err := json.Unmarshal(data, &onDisk); err != nil {
		t.Fatalf("落盘 JSON 解析失败: %v", err)
	}
	if _, ok := onDisk["lastUpdateResult"]; !ok {
		t.Fatalf("落盘缺少 lastUpdateResult：%v", onDisk)
	}

	// 3) 模拟「重启」：从同一份配置重新加载并恢复
	var reloaded map[string]any
	if err := json.Unmarshal(data, &reloaded); err != nil {
		t.Fatal(err)
	}
	cfg2 := &Settings{raw: reloaded}
	a2 := &App{cfg: cfg2, mu: sync.Mutex{}}
	a2.restoreUpdateStatus()
	if a2.updateStatus == nil || a2.updateStatus["status"] != "update-available" {
		t.Fatalf("重启后未恢复「有更新」：%v", a2.updateStatus)
	}

	// 4) 更新成功后清除
	a2.clearUpdateResult()
	if cfg2.LastUpdateResult() != nil {
		t.Fatalf("清除后配置仍残留更新结果：%v", cfg2.LastUpdateResult())
	}
	if a2.updateStatus != nil {
		t.Fatalf("清除后内存态未清空：%v", a2.updateStatus)
	}

	// 5) 失效版本（不比当前新）在恢复时应被清理，避免长期残留「有更新」
	cfg3 := &Settings{raw: defaultSettingsMap()}
	a3 := &App{cfg: cfg3, mu: sync.Mutex{}}
	a3.persistUpdateResult(map[string]any{"status": "update-available", "current": appVersion, "latest": appVersion})
	a3.restoreUpdateStatus()
	if a3.updateStatus != nil {
		t.Fatalf("失效版本应被清理，却恢复了：%v", a3.updateStatus)
	}
	if cfg3.LastUpdateResult() != nil {
		t.Fatalf("失效版本应在恢复时被清除：%v", cfg3.LastUpdateResult())
	}
}

// TestPendingUpdateReturnsSettingsState 验证自更新重启后，PendingUpdate 会把
// 「更新前设置窗口可见 / 所在 tab」回传给前端，供其恢复页面。
func TestPendingUpdateReturnsSettingsState(t *testing.T) {
	dir := t.TempDir()
	pendingPath := filepath.Join(dir, "pending.json")
	pending := updatePending{
		Version:         "2.0.0",
		Date:            "2026-10-19",
		Changes:         []string{"新功能"},
		SettingsVisible: true,
		ActiveTab:       "about",
	}
	data, _ := json.MarshalIndent(pending, "", "  ")
	if err := os.WriteFile(pendingPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	// pendingUpdatePath 是包级变量；还原以免影响其它测试
	old := pendingUpdatePath
	defer func() { pendingUpdatePath = old }()
	pendingUpdatePath = pendingPath

	cfg := &Settings{raw: map[string]any{}, path: filepath.Join(dir, "settings.json")}
	a := &App{cfg: cfg, mu: sync.Mutex{}}

	info := a.PendingUpdate()
	if info == nil {
		t.Fatal("PendingUpdate 返回 nil")
	}
	if info["settingsVisible"] != true {
		t.Fatalf("settingsVisible 应为 true，实得 %v", info["settingsVisible"])
	}
	if info["activeTab"] != "about" {
		t.Fatalf("activeTab 应为 about，实得 %v", info["activeTab"])
	}
	if info["version"] != "2.0.0" {
		t.Fatalf("version 应为 2.0.0，实得 %v", info["version"])
	}
}

// TestVersionInfoURLIsArray 验证 url 只接受数组写法（软件尚未发布，不做旧单字符串兼容）。
func TestVersionInfoURLIsArray(t *testing.T) {
	var info versionInfo
	if err := json.Unmarshal([]byte(`{"version":"1.0.1","url":["http://a/dir","http://b/pkg.zip"]}`), &info); err != nil {
		t.Fatalf("数组写法应能解析: %v", err)
	}
	if !reflect.DeepEqual(info.URL, []string{"http://a/dir", "http://b/pkg.zip"}) {
		t.Fatalf("url 数组不符：%v", info.URL)
	}
	// 旧的单字符串写法不再兼容：解析直接失败，而不是把整串当目录再拼出畸形地址
	if err := json.Unmarshal([]byte(`{"version":"1.0.1","url":"http://a/pkg.zip"}`), &info); err == nil {
		t.Fatalf("单字符串 url 应当解析失败")
	}
}

// TestNormalizePackageURL 验证 url 数组项的补全规则：.zip 结尾直接用、
// / 结尾只拼 rest.zip、其余拼 /rest.zip。
func TestNormalizePackageURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"http://x/dir", "http://x/dir/rest.zip"},
		{"http://x/dir/", "http://x/dir/rest.zip"},
		{"http://x/pkg.zip", "http://x/pkg.zip"},
		{"http://x/PKG.ZIP", "http://x/PKG.ZIP"},      // 后缀大小写不敏感
		{"  http://x/dir  ", "http://x/dir/rest.zip"}, // 首尾空白剥掉
		{"", ""},
		{"   ", ""},
	}
	for _, c := range cases {
		if got := normalizePackageURL(c.in); got != c.want {
			t.Fatalf("normalizePackageURL(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

// TestStatusURLsShapes 验证从更新状态 map 取地址数组的两种形状：
// 内存里新写入的 []string、配置 JSON 往返后的 []any。
func TestStatusURLsShapes(t *testing.T) {
	cases := []struct {
		name string
		v    any
		want []string
	}{
		{"内存 []string", []string{"http://a", "http://b"}, []string{"http://a", "http://b"}},
		{"配置往返 []any", []any{"http://a", "http://b"}, []string{"http://a", "http://b"}},
		{"缺省", nil, nil},
	}
	for _, c := range cases {
		st := map[string]any{}
		if c.v != nil {
			st["url"] = c.v
		}
		if got := statusURLs(st); !reflect.DeepEqual(got, c.want) {
			t.Fatalf("%s：statusURLs = %v，期望 %v", c.name, got, c.want)
		}
	}
}

// TestDownloadPackageFallback 验证 url 数组的下载容错：第一项失败自动取下一项
// （目录项会先补全为 /rest.zip），全部失败才报错，空数组直接报错。
func TestDownloadPackageFallback(t *testing.T) {
	body := []byte("fake-zip-bytes")
	goodSum := fmt.Sprintf("%x", sha256.Sum256(body))
	srvOK := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest.zip" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(body)
	}))
	defer srvOK.Close()
	srvFail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srvFail.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "rest.zip")

	// 第一项 500、第二项是目录（需拼 /rest.zip）→ 应容错成功
	if err := downloadPackage([]string{srvFail.URL, srvOK.URL}, dest, goodSum); err != nil {
		t.Fatalf("应容错下载成功，却报错: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("下载内容不符: %v %q", err, got)
	}

	// 全部失败 → 报错
	if err := downloadPackage([]string{srvFail.URL}, dest, goodSum); err == nil {
		t.Fatalf("全部失败时应报错")
	}
	// 空数组 → 报错
	if err := downloadPackage(nil, dest, goodSum); err == nil {
		t.Fatalf("空地址数组应报错")
	}
	// 第一项直接是完整 .zip 地址 → 不拼后缀直接下
	if err := downloadPackage([]string{srvOK.URL + "/rest.zip"}, dest, goodSum); err != nil {
		t.Fatalf("完整 .zip 地址应直接下载: %v", err)
	}

	// 哈希不符 → 必须报错，且不留下「看起来能用」的包
	badSum := fmt.Sprintf("%x", sha256.Sum256([]byte("another-bytes")))
	if err := downloadPackage([]string{srvOK.URL}, dest, badSum); err == nil {
		t.Fatalf("哈希不符时应报错")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("校验失败后应删除下载的文件（err=%v）", err)
	}
	// 没有哈希 → 直接拒绝，连下载都不做
	if err := downloadPackage([]string{srvOK.URL}, dest, ""); err == nil {
		t.Fatalf("缺少 sha256 时应拒绝下载")
	}
	// 全 http 明文地址 → 拒绝
	if err := downloadPackage([]string{"http://example.invalid/pkg/"}, dest, goodSum); err == nil {
		t.Fatalf("http 明文下载地址应被拒绝")
	}
}

// TestExtractZip 验证更新包解压：普通文件与子目录都还原、findNewExe 能定位，
// 且含 ../ 的恶意条目被拒绝（不能写到解压目录之外）。
func TestExtractZip(t *testing.T) {
	dir := t.TempDir()
	pkg := filepath.Join(dir, packageName)
	f, err := os.Create(pkg)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	add := func(name, body string) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("建条目 %s 失败: %v", name, err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	add("rest.exe", "EXE")
	add("config/settings.json", `{"a":1}`)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(dir, "out")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := extractZip(pkg, out); err != nil {
		t.Fatalf("解压失败: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(out, "rest.exe")); err != nil || string(b) != "EXE" {
		t.Fatalf("rest.exe 内容不符: %v %q", err, b)
	}
	if b, err := os.ReadFile(filepath.Join(out, "config", "settings.json")); err != nil || string(b) != `{"a":1}` {
		t.Fatalf("子目录文件内容不符: %v %q", err, b)
	}
	if got := findNewExe(out); got != filepath.Join(out, "rest.exe") {
		t.Fatalf("findNewExe = %q，期望包内 rest.exe", got)
	}

	// 目录穿越条目必须被拒
	bad := filepath.Join(dir, "bad.zip")
	bf, err := os.Create(bad)
	if err != nil {
		t.Fatal(err)
	}
	bw := zip.NewWriter(bf)
	if w, err := bw.Create("../evil.txt"); err == nil {
		_, _ = w.Write([]byte("x"))
	}
	if err := bw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := bf.Close(); err != nil {
		t.Fatal(err)
	}
	if err := extractZip(bad, out); err == nil {
		t.Fatalf("含 ../ 的包应被拒绝")
	}
	if _, err := os.Stat(filepath.Join(dir, "evil.txt")); err == nil {
		t.Fatalf("恶意条目被写到解压目录之外")
	}
}

// TestSetActiveTab 验证前端每次切页上报的 tab 被记录，用于重启后恢复页面。
func TestSetActiveTab(t *testing.T) {
	a := &App{mu: sync.Mutex{}, activeTab: "general"}
	a.SetActiveTab("about")
	if a.activeTab != "about" {
		t.Fatalf("SetActiveTab 未生效，实得 %q", a.activeTab)
	}
	a.SetActiveTab("")
	if a.activeTab != "about" {
		t.Fatalf("空 tab 不应覆盖已有值，实得 %q", a.activeTab)
	}
}

// ---------- 自动检查更新的调度（autoUpdateTick）----------

// fixedRand 固定随机时长，便于断言排期时刻。
func fixedRand(d time.Duration) func(time.Duration) time.Duration {
	return func(time.Duration) time.Duration { return d }
}

// TestAutoUpdateRandWithinWindow 随机时长必须落在 [0, max) 内。
func TestAutoUpdateRandWithinWindow(t *testing.T) {
	for _, max := range []time.Duration{autoCheckWindow, autoCheckRetry} {
		for i := 0; i < 200; i++ {
			if d := autoUpdateRand(max); d < 0 || d >= max {
				t.Fatalf("随机时长 %v 超出 [0,%v)", d, max)
			}
		}
	}
	if d := autoUpdateRand(0); d != 0 {
		t.Fatalf("上界为 0 时应返回 0，实得 %v", d)
	}
	if d := autoUpdateRand(-time.Minute); d != 0 {
		t.Fatalf("上界为负时应返回 0，实得 %v", d)
	}
}

// TestAutoUpdateTickDailyTiming 当天首次键鼠活动后，3 小时内随机一刻才真正检查。
func TestAutoUpdateTickDailyTiming(t *testing.T) {
	const day = "2026-10-23"
	t0 := time.Date(2026, 10, 23, 9, 0, 0, 0, time.Local)
	var st autoUpdateState

	// 1) 还没有键鼠活动：不排期（一直不用电脑就永远不检查）
	if act := autoUpdateTick(&st, day, t0, false, false, false, fixedRand(time.Hour)); act != autoActNone {
		t.Fatalf("无活动时不该有动作，实得 %q", act)
	}
	if !st.armedAt.IsZero() {
		t.Fatalf("无活动时不该排期，实得 %v", st.armedAt)
	}

	// 2) 首次活动：只在 3 小时窗内排期，不当场检查
	if act := autoUpdateTick(&st, day, t0, true, false, false, fixedRand(time.Hour)); act != autoActNone {
		t.Fatalf("首次活动后不该立刻检查，实得 %q", act)
	}
	if want := t0.Add(time.Hour); !st.armedAt.Equal(want) {
		t.Fatalf("排期时刻 = %v，期望 %v", st.armedAt, want)
	}

	// 3) 未到点：什么也不做
	if act := autoUpdateTick(&st, day, t0.Add(59*time.Minute), true, false, false, fixedRand(time.Hour)); act != autoActNone {
		t.Fatalf("未到点不该检查，实得 %q", act)
	}

	// 4) 到点且不在暂停休息：去检查，并清掉本次排期
	if act := autoUpdateTick(&st, day, t0.Add(time.Hour), true, false, false, fixedRand(time.Hour)); act != autoActCheck {
		t.Fatalf("到点应检查更新，实得 %q", act)
	}
	if !st.armedAt.IsZero() {
		t.Fatalf("检查后应清空排期，实得 %v", st.armedAt)
	}
}

// TestAutoUpdateTickRetriesWhilePaused 到点若处于暂停休息，30 分钟内再随机，反复。
func TestAutoUpdateTickRetriesWhilePaused(t *testing.T) {
	const day = "2026-10-23"
	t0 := time.Date(2026, 10, 23, 9, 0, 0, 0, time.Local)
	st := autoUpdateState{day: day}
	autoUpdateTick(&st, day, t0, true, false, false, fixedRand(time.Hour))
	at := t0.Add(time.Hour)

	// 第 1 次到点：暂停 → 推 10 分钟
	if act := autoUpdateTick(&st, day, at, true, true, false, fixedRand(10*time.Minute)); act != autoActNone {
		t.Fatalf("暂停时不该检查，实得 %q", act)
	}
	if want := at.Add(10 * time.Minute); !st.armedAt.Equal(want) {
		t.Fatalf("重试时刻 = %v，期望 %v", st.armedAt, want)
	}

	// 第 2 次到点：还暂停 → 再推 5 分钟
	at = at.Add(10 * time.Minute)
	if act := autoUpdateTick(&st, day, at, true, true, false, fixedRand(5*time.Minute)); act != autoActNone {
		t.Fatalf("仍暂停时不该检查，实得 %q", act)
	}
	if want := at.Add(5 * time.Minute); !st.armedAt.Equal(want) {
		t.Fatalf("重试时刻 = %v，期望 %v", st.armedAt, want)
	}

	// 第 3 次到点：已恢复 → 检查
	if act := autoUpdateTick(&st, day, at.Add(5*time.Minute), true, false, false, fixedRand(time.Minute)); act != autoActCheck {
		t.Fatalf("恢复后到点应检查，实得 %q", act)
	}
}

// TestAutoUpdateTickResetsAtMidnight 跨天整体重置：昨晚没查成不影响今天。
func TestAutoUpdateTickResetsAtMidnight(t *testing.T) {
	const d1, d2 = "2026-10-23", "2026-10-24"
	st := autoUpdateState{day: d1}
	late := time.Date(2026, 10, 23, 23, 50, 0, 0, time.Local)
	autoUpdateTick(&st, d1, late, true, false, false, fixedRand(2*time.Hour)) // 排到次日 01:50
	if st.armedAt.IsZero() {
		t.Fatalf("应已排期")
	}

	// 次日 00:30：跨天 → 排期作废，回到「等今天首次活动」（此刻无活动）
	act := autoUpdateTick(&st, d2, time.Date(2026, 10, 24, 0, 30, 0, 0, time.Local),
		false, false, false, fixedRand(time.Hour))
	if act != autoActNone {
		t.Fatalf("跨天不该检查，实得 %q", act)
	}
	if st.day != d2 || !st.armedAt.IsZero() {
		t.Fatalf("跨天应整体重置，实得 day=%q armed=%v", st.day, st.armedAt)
	}

	// 新的一天首次活动 → 重新按 3 小时窗排期（前一天一直暂停没查成也不影响）
	next := time.Date(2026, 10, 24, 8, 0, 0, 0, time.Local)
	autoUpdateTick(&st, d2, next, true, true, false, fixedRand(30*time.Minute))
	if want := next.Add(30 * time.Minute); !st.armedAt.Equal(want) {
		t.Fatalf("新一天的排期 = %v，期望 %v（暂停不影响「首次活动后 3 小时窗」的起算）", st.armedAt, want)
	}
}

// TestAutoUpdateTickSkipsWhenDone 当天已查过 → 不再排期、不再检查。
func TestAutoUpdateTickSkipsWhenDone(t *testing.T) {
	const day = "2026-10-23"
	now := time.Date(2026, 10, 23, 10, 0, 0, 0, time.Local)
	st := autoUpdateState{day: day, armedAt: now}
	if act := autoUpdateTick(&st, day, now, true, false, true, fixedRand(time.Hour)); act != autoActNone {
		t.Fatalf("当天已查过不该再检查，实得 %q", act)
	}
	if !st.armedAt.IsZero() {
		t.Fatalf("当天已查过应清空排期，实得 %v", st.armedAt)
	}
}

// TestRunAutoUpdateCheckRecordsDay 真正检查出一份结论才记「今天查过」；
// 网络失败不记，当天后面还会按规则再排一次。
func TestRunAutoUpdateCheckRecordsDay(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/version.json" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"version":"99.0.0","date":"2026.10.23","url":["https://example.invalid/pkg/"],"sha256":"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"}`)
	}))
	defer srv.Close()

	newApp := func(t *testing.T, site string) *App {
		t.Setenv("WR_SETTINGS_PATH", filepath.Join(t.TempDir(), "settings.json"))
		cfg := &Settings{raw: defaultSettingsMap()}
		cfg.SetSites([]string{site})
		return &App{cfg: cfg, mu: sync.Mutex{}}
	}

	t.Run("有结论才记当天", func(t *testing.T) {
		a := newApp(t, srv.URL+"/version.json")
		a.runAutoUpdateCheck("2026-10-23")
		if got := a.cfg.LastUpdateCheck(); got != "2026-10-23" {
			t.Fatalf("检查成功应记今天，实得 %q", got)
		}
		if st := a.snapshotUpdateStatus(); st["status"] != "update-available" {
			t.Fatalf("状态应为 update-available，实得 %v", st)
		}
	})

	t.Run("失败不记当天", func(t *testing.T) {
		a := newApp(t, srv.URL+"/bad.json")
		a.runAutoUpdateCheck("2026-10-23")
		if got := a.cfg.LastUpdateCheck(); got != "" {
			t.Fatalf("检查失败不该记今天，实得 %q", got)
		}
		if st := a.snapshotUpdateStatus(); st["status"] != "error" {
			t.Fatalf("状态应为 error，实得 %v", st)
		}
	})
}

// TestAutoUpdateLoopDispatch 把「常驻循环 → 状态机 → 真实检查 → 记录」串起来跑一遍：
// 假时钟加速（每次睡眠把虚拟时间往前推），验证首次活动后确实自动联网检查了一次，
// 并把「今天查过」写在虚拟时钟那天的名下。
func TestAutoUpdateLoopDispatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"version":"99.0.0","date":"2026.10.23","url":["https://example.invalid/pkg/"],"sha256":"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"}`)
	}))
	defer srv.Close()

	t.Setenv("WR_SETTINGS_PATH", filepath.Join(t.TempDir(), "settings.json"))
	cfg := &Settings{raw: defaultSettingsMap()}
	cfg.SetSites([]string{srv.URL + "/version.json"})
	if !cfg.Flag(true, "checkUpdateOnStart") {
		t.Fatalf("出厂默认应当勾选「自动检查更新」")
	}
	idle := NewIdleState()
	idle.ForceIdle(0) // 0ms 空闲 = 用户正在操作
	a := &App{cfg: cfg, mu: sync.Mutex{}, idle: idle}

	var clockMu sync.Mutex
	clock := time.Date(2026, 10, 23, 9, 0, 0, 0, time.Local)
	oldNow, oldSleep := autoUpdateNow, autoUpdateSleep
	autoUpdateNow = func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return clock
	}
	var stopped atomic.Bool
	block := make(chan struct{})
	// parked：循环已经彻底挂住（不再碰配置目录）的信号。
	// 没有它就只能靠「LastUpdateCheck 非空」判断，而那一步是**先写内存再落盘**——
	// 轮询看到值时 Save 可能还在写 settings.json.tmp，接着 t.TempDir 的清理会撞上
	// 「目录非空」而偶发失败（全量跑时更容易复现）。
	parked := make(chan struct{}, 1)
	autoUpdateSleep = func(d time.Duration) {
		// 一旦真正查过一次（或测试收尾），就把循环挂住：虚拟时间不再往前走，
		// 免得它在毫秒内又跑到第二天、再触发一次检查。
		if stopped.Load() || a.lastUpdateCheckLocked() != "" {
			select {
			case parked <- struct{}{}:
			default:
			}
			<-block
		}
		clockMu.Lock()
		clock = clock.Add(d)
		clockMu.Unlock()
	}
	defer func() {
		stopped.Store(true)
		autoUpdateNow, autoUpdateSleep = oldNow, oldSleep
	}()

	go a.autoUpdateLoop()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && a.lastUpdateCheckLocked() == "" {
		time.Sleep(5 * time.Millisecond)
	}
	stopped.Store(true)

	// 等循环真的挂住：它挂住的时刻一定在 Save 之后，这时目录才算静止。
	select {
	case <-parked:
	case <-time.After(5 * time.Second):
		t.Fatalf("自动更新循环没有按预期挂起（可能仍在反复检查）")
	}

	if got := a.lastUpdateCheckLocked(); got != "2026-10-23" {
		t.Fatalf("应在假时钟当天（2026-10-23）完成检查，实得 %q", got)
	}
	if st := a.snapshotUpdateStatus(); st["status"] != "update-available" {
		t.Fatalf("状态应为 update-available，实得 %v", st)
	}
}

// driveAutoUpdate 按固定步长把调度状态机跑一段时间，返回每次真正去检查的时刻。
// 这里刻意用真实的 autoUpdateRand，用「区间断言」验证随机窗口没越界。
func driveAutoUpdate(from, to time.Time, step time.Duration,
	active, paused func(time.Time) bool) []time.Time {
	var st autoUpdateState
	var checks []time.Time
	doneDay := ""
	for now := from; now.Before(to); now = now.Add(step) {
		day := now.Format("2006-01-02")
		if autoUpdateTick(&st, day, now, active(now), paused(now), doneDay == day, autoUpdateRand) == autoActCheck {
			checks = append(checks, now)
			doneDay = day
		}
	}
	return checks
}

// TestAutoUpdateTickSimulatedDay 用 1 分钟步长模拟一整天，验证：
// 非暂停时检查点落在「首次活动后 3 小时内」；暂停时按 30 分钟窗反复重试；
// 一整天都没等到非暂停 → 当天不查，第二天照常按自己的首次活动起算。
//
// 注意 tol：状态机是「到点后的第一拍才动手」，而这里步长是 1 分钟，所以真正落点最多
// 比随机窗末晚一个步长。断言必须带这个容差 —— 否则随机数落在窗末那一分钟内就会偶发
// 挂（窗口 3h = 180 拍，命中 1/180，跑几十次必现）。
func TestAutoUpdateTickSimulatedDay(t *testing.T) {
	base := time.Date(2026, 10, 23, 0, 0, 0, 0, time.Local)
	hm := func(h, m int) time.Time { return base.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute) }
	const step = time.Minute
	tol := step

	// 1) 09:00 起一直有操作、不暂停 → 当天恰好查一次，时刻早于 12:00（+1 拍）
	checks := driveAutoUpdate(hm(9, 0), hm(24, 0), step,
		func(time.Time) bool { return true }, func(time.Time) bool { return false })
	if len(checks) != 1 {
		t.Fatalf("当天应恰好检查一次，实得 %d 次：%v", len(checks), checks)
	}
	if checks[0].Before(hm(9, 0)) || checks[0].After(hm(12, 0).Add(tol)) {
		t.Fatalf("检查时刻 %v 未落在 09:00 之后的 3 小时窗内", checks[0])
	}

	// 2) 09:00 起有操作，但一直暂停到 13:00 → 恢复后的 30 分钟内才查
	checks = driveAutoUpdate(hm(9, 0), hm(24, 0), step,
		func(time.Time) bool { return true },
		func(now time.Time) bool { return now.Before(hm(13, 0)) })
	if len(checks) != 1 {
		t.Fatalf("应恰好检查一次，实得 %d 次：%v", len(checks), checks)
	}
	if checks[0].Before(hm(13, 0)) || checks[0].After(hm(13, 30).Add(tol)) {
		t.Fatalf("恢复后的检查时刻 %v 未落在 13:00~13:30 内", checks[0])
	}

	// 3) 第一天全程暂停（当天查不成），第二天不暂停 → 第二天照样查一次
	day2 := base.AddDate(0, 0, 1)
	checks = driveAutoUpdate(hm(8, 0), day2.Add(24*time.Hour), step,
		func(now time.Time) bool { return now.Hour() >= 8 },
		func(now time.Time) bool { return now.Before(day2) })
	if len(checks) != 1 {
		t.Fatalf("第二天应检查一次（前一天没查成不影响），实得 %d 次：%v", len(checks), checks)
	}
	if !checks[0].After(day2) && !checks[0].Equal(day2) {
		t.Fatalf("检查应落在第二天，实得 %v", checks[0])
	}
	if h := checks[0].Sub(day2.Add(8 * time.Hour)); h < 0 || h > 3*time.Hour+tol {
		t.Fatalf("第二天的检查时刻 %v 未落在 08:00 之后的 3 小时窗内", checks[0])
	}
}

// TestMain 在整包单测里默认关掉出厂兜底更新源：否则任何「期望失败」的用例都会真的去
// 访问 goodgoodlive.com（15 秒超时，结果还依赖网络）。要验兜底行为的用例自己临时打开。
func TestMain(m *testing.M) {
	defaultUpdateSite, defaultPackageURL = "", ""
	os.Exit(m.Run())
}

// TestWithFallbackSite 兜底更新源 / 兜底包地址的合并规则：去空白、去重、追加在最后，
// 兜底置空时只留配置里的项。
func TestWithFallbackSite(t *testing.T) {
	oldSite, oldPkg := defaultUpdateSite, defaultPackageURL
	defaultUpdateSite = "https://fb/version.json"
	defaultPackageURL = "https://fb/rest.zip"
	defer func() { defaultUpdateSite, defaultPackageURL = oldSite, oldPkg }()

	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"配置为空 → 只剩兜底", nil, []string{"https://fb/version.json"}},
		{"兜底排在最后", []string{"https://a/version.json", "", "  https://b/version.json  "},
			[]string{"https://a/version.json", "https://b/version.json", "https://fb/version.json"}},
		{"兜底已在链中 → 不重复追加", []string{"https://fb/version.json", "https://a/version.json"},
			[]string{"https://fb/version.json", "https://a/version.json"}},
		{"重复项去重", []string{"https://a/version.json", "https://a/version.json"},
			[]string{"https://a/version.json", "https://fb/version.json"}},
	}
	for _, c := range cases {
		if got := withFallbackSite(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s：得到 %v，期望 %v", c.name, got, c.want)
		}
	}

	// 兜底置空 → 不追加任何东西（全空则返回空链，调用方走「未配置更新源」分支）
	defaultUpdateSite = ""
	if got := withFallbackSite([]string{"https://a/version.json"}); !reflect.DeepEqual(got, []string{"https://a/version.json"}) {
		t.Errorf("兜底置空后不该追加：%v", got)
	}
	if got := withFallbackSite(nil); len(got) != 0 {
		t.Errorf("兜底与配置都空时应返回空链：%v", got)
	}

	if got := withFallbackPackage([]string{"https://a/", " "}); !reflect.DeepEqual(got, []string{"https://a/", "https://fb/rest.zip"}) {
		t.Errorf("包地址兜底应追加在末尾：%v", got)
	}
}

// TestCheckForUpdateFallsBackToDefaultSite 配置里一条更新源都没有时，仍然能靠出厂兜底源
// 完成检查 —— 「装完没配 sites 也能更新」是兜底地址存在的意义。
func TestCheckForUpdateFallsBackToDefaultSite(t *testing.T) {
	body := `{"version":"v99.0.0","date":"2026.10.23",` +
		`"url":["https://x/pkg.zip"],"changes":["x"],` +
		`"sha256":"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	oldSite, oldPkg := defaultUpdateSite, defaultPackageURL
	defaultUpdateSite = srv.URL + "/version.json"
	defaultPackageURL = ""
	defer func() { defaultUpdateSite, defaultPackageURL = oldSite, oldPkg }()

	// 完全空白的配置：连 sites 键都没有
	t.Setenv("WR_SETTINGS_PATH", filepath.Join(t.TempDir(), "settings.json"))
	a := &App{cfg: &Settings{raw: map[string]any{}}, mu: sync.Mutex{}}
	st := a.checkForUpdate()
	if st.Status != "update-available" {
		t.Fatalf("应靠兜底源查到新版本，实得 %+v", st)
	}
	if st.Latest != "v99.0.0" {
		t.Fatalf("版本号不符：%q", st.Latest)
	}
}
