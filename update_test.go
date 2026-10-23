package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
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

// TestCheckForUpdateURLArray 端到端验证：真实 version.json 形状（url 为数组）经
// checkForUpdate 落成状态 map 的 url 数组，StartUpdate 侧能原样取出并按规则补全。
func TestCheckForUpdateURLArray(t *testing.T) {
	t.Setenv("WR_SETTINGS_PATH", filepath.Join(t.TempDir(), "settings.json"))
	body := `{"version":"v1.1.0","date":"2026.10.23",` +
		`"url":["https://nav.hcctc.cn/temp/","https://www.goodgoodlive.com/worker-rest/"],` +
		`"sites":["https://a/version.json","https://b/version.json"],` +
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
	// 状态 map 往返后 StartUpdate 仍能取出同一份数组
	if got := statusURLs(st.toMap()); !reflect.DeepEqual(got, want) {
		t.Fatalf("statusURLs 回读不符：%v", got)
	}
	// 补全规则：目录项 → <目录>/rest.zip
	if got := normalizePackageURL(want[1]); got != "https://www.goodgoodlive.com/worker-rest/rest.zip" {
		t.Fatalf("补全结果不符：%s", got)
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
	if err := downloadPackage([]string{srvFail.URL, srvOK.URL}, dest); err != nil {
		t.Fatalf("应容错下载成功，却报错: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("下载内容不符: %v %q", err, got)
	}

	// 全部失败 → 报错
	if err := downloadPackage([]string{srvFail.URL}, dest); err == nil {
		t.Fatalf("全部失败时应报错")
	}
	// 空数组 → 报错
	if err := downloadPackage(nil, dest); err == nil {
		t.Fatalf("空地址数组应报错")
	}
	// 第一项直接是完整 .zip 地址 → 不拼后缀直接下
	if err := downloadPackage([]string{srvOK.URL + "/rest.zip"}, dest); err != nil {
		t.Fatalf("完整 .zip 地址应直接下载: %v", err)
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
