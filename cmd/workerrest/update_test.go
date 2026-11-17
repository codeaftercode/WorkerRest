package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"workerrest/internal/jsonx"
	"workerrest/internal/settings"
	"workerrest/internal/update"
)

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

// TestVersionInfoParsesPackagedManifest 钉住“打包脚本产出的清单”与 Go 侧结构的
// 字段口径一致（键名见 scripts/make_portable.py 的 write_version_json）。
// 任何一边改名都会让线上更新瞬间失效，所以这层契约必须有测试，而不是靠“记得同步”。
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
	if _, err := update.ParseSHA256(info.SHA256); err != nil {
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

	cfg := settings.New(settings.DefaultSettingsMap(), "")
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
// 必须报 error 并说明原因，而不是对外宣称“有更新”再让人点进去失败。
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

			cfg := settings.New(settings.DefaultSettingsMap(), "")
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
	if err := settings.MergeConfigFromDir(dir, raw); err != nil {
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

// TestUpdateResultPersistence 验证“发现新版本但未更新”的状态能持久化、能在重启后
// 恢复、并在更新成功后清除；同时验证版本已失效（不比当前新）时恢复会被清理。
func TestUpdateResultPersistence(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "settings.json")
	t.Setenv("WR_SETTINGS_PATH", tmp)

	cfg := settings.New(settings.DefaultSettingsMap(), "")
	a := &App{cfg: cfg, mu: sync.Mutex{}}

	// 1) 发布/持久化一次“有更新”结果
	st := updateCheckResult{Status: "update-available", Current: appVersion, Latest: "9999.0.0", URLs: []string{"http://x/pkg.zip"}, Changes: []string{"新功能"}}
	a.publishUpdateStatus(st)

	// 2) 内存态与落盘都应反映该结果
	if a.updateStatus["status"] != "update-available" {
		t.Fatalf("内存态未更新：%v", a.updateStatus)
	}
	// 读 a.cfg（而不是构造时的局部 cfg）：persistUpdateResult 走 mutateConfigCopy
	// 「深拷 + 换指针」，旧对象从此冻结 —— 局部引用看到的是改写前的快照。
	if got := a.cfg.LastUpdateResult(); got == nil || got["latest"] != "9999.0.0" {
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

	// 3) 模拟“重启”：从同一份配置重新加载并恢复
	var reloaded map[string]any
	if err := json.Unmarshal(data, &reloaded); err != nil {
		t.Fatal(err)
	}
	cfg2 := settings.New(reloaded, "")
	a2 := &App{cfg: cfg2, mu: sync.Mutex{}}
	a2.restoreUpdateStatus()
	if a2.updateStatus == nil || a2.updateStatus["status"] != "update-available" {
		t.Fatalf("重启后未恢复“有更新”：%v", a2.updateStatus)
	}

	// 4) 更新成功后清除（同样读 a2.cfg：clearUpdateResult 是换指针，cfg2 是冻结的旧对象）
	a2.clearUpdateResult()
	if got := a2.cfg.LastUpdateResult(); got != nil {
		t.Fatalf("清除后配置仍残留更新结果：%v", got)
	}
	if a2.updateStatus != nil {
		t.Fatalf("清除后内存态未清空：%v", a2.updateStatus)
	}

	// 5) 失效版本（不比当前新）在恢复时应被清理，避免长期残留“有更新”
	cfg3 := settings.New(settings.DefaultSettingsMap(), "")
	a3 := &App{cfg: cfg3, mu: sync.Mutex{}}
	a3.persistUpdateResult(map[string]any{"status": "update-available", "current": appVersion, "latest": appVersion})
	a3.restoreUpdateStatus()
	if a3.updateStatus != nil {
		t.Fatalf("失效版本应被清理，却恢复了：%v", a3.updateStatus)
	}
	if got := a3.cfg.LastUpdateResult(); got != nil {
		t.Fatalf("失效版本应在恢复时被清除：%v", got)
	}
}

// TestPendingUpdateReturnsSettingsState 验证自更新重启后，PendingUpdate 会把
// “更新前设置窗口可见 / 所在 tab”回传给前端，供其恢复页面。
func TestPendingUpdateReturnsSettingsState(t *testing.T) {
	root, exePath, updateDir, _, pendingPath := writePendingLayout(t, updatePending{
		Version:         "2.0.0",
		Date:            "2026-10-19",
		Changes:         []string{"新功能"},
		SettingsVisible: true,
		ActiveTab:       "about",
	})
	withCurrentExe(t, exePath)
	// pendingUpdatePath 是包级变量；还原以免影响其它测试
	old := pendingUpdatePath
	defer func() { pendingUpdatePath = old }()
	pendingUpdatePath = pendingPath

	cfg := settings.New(map[string]any{}, filepath.Join(root, "settings.json"))
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
	// 摘要展示后 update 临时目录被清理（合法路径才清）
	if _, err := os.Stat(updateDir); !os.IsNotExist(err) {
		t.Fatalf("展示摘要后应清理 update 临时目录（err=%v）", err)
	}
}

// writePendingLayout 造一套「安装目录 + update 暂存目录」的真实形状：
// <root>/rest.exe（当前程序）、<root>/update/rest.exe（新程序）、<root>/update/pending.json。
// 返回 root、当前 exe、update 目录、新 exe、pending 路径。
func writePendingLayout(t *testing.T, p updatePending) (root, exePath, updateDir, newExe, pendingPath string) {
	t.Helper()
	root = t.TempDir()
	exePath = filepath.Join(root, "rest.exe")
	if err := os.WriteFile(exePath, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	updateDir = filepath.Join(root, updateDirName)
	if err := os.MkdirAll(updateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	newExe = filepath.Join(updateDir, newExeName)
	if err := os.WriteFile(newExe, []byte("NEW"), 0o755); err != nil {
		t.Fatal(err)
	}
	if p.OldExe == "" {
		p.OldExe = exePath
	}
	if p.NewExe == "" {
		p.NewExe = newExe
	}
	pendingPath = filepath.Join(updateDir, "pending.json")
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pendingPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return root, exePath, updateDir, newExe, pendingPath
}

// withCurrentExe 把「当前程序路径」指到临时目录里的假 exe（校验只认它推导出的安装目录）。
func withCurrentExe(t *testing.T, exePath string) {
	t.Helper()
	old := currentExePath
	currentExePath = func() (string, error) { return exePath, nil }
	t.Cleanup(func() { currentExePath = old })
}

// TestValidatePendingUpdate 钉住自更新的输入校验口径：合法形状只有 StartUpdate 写出来的
// 那一种；任何一项越界都必须报错（调用方据此不删目录、不覆盖文件）。
func TestValidatePendingUpdate(t *testing.T) {
	// 正常 --updated：本进程就是被替换的那个 exe
	root, exePath, updateDir, newExe, pendingPath := writePendingLayout(t, updatePending{Version: "1.0.1"})
	if _, err := validatePendingUpdate(pendingPath, exePath); err != nil {
		t.Fatalf("正常形状应通过校验: %v", err)
	}
	// 正常 --self-update：本进程是 update 目录里的 stage 副本，OldExe 是它反推出的那个 exe
	stageExe := filepath.Join(updateDir, stageNameForExe(exePath))
	if err := os.WriteFile(stageExe, []byte("STAGE"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := validatePendingUpdate(pendingPath, stageExe); err != nil {
		t.Fatalf("stage 副本形态应通过校验（否则真机自更新会被自己拦下）: %v", err)
	}

	// ① pending 不在 <安装目录>/update 内：平级目录 / ../ 越界都算
	sibling := filepath.Join(root, "sibling")
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatal(err)
	}
	siblingPending := filepath.Join(sibling, "pending.json")
	if err := os.WriteFile(siblingPending, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := validatePendingUpdate(siblingPending, exePath); err == nil {
		t.Fatalf("update 目录之外的 pending 应被拒绝")
	}
	if _, err := validatePendingUpdate(filepath.Join(updateDir, "..", "sibling", "pending.json"), exePath); err == nil {
		t.Fatalf("用 .. 绕出去的待办路径应被拒绝")
	}

	// ② OldExe 不是当前程序（指向同目录另一个 exe 也算）
	victim := filepath.Join(root, "victim.exe")
	if err := os.WriteFile(victim, []byte("VICTIM"), 0o755); err != nil {
		t.Fatal(err)
	}
	badOld := `{"version":"1.0.1","oldExe":"` + jsonEscape(victim) + `","newExe":"` + jsonEscape(newExe) + `"}`
	if err := os.WriteFile(pendingPath, []byte(badOld), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := validatePendingUpdate(pendingPath, exePath); err == nil {
		t.Fatalf("OldExe 不是当前程序时应被拒绝")
	}
	// ②b 同一份 pending 在 self-update 分支里也必须被拒：安装目录内**任意** .exe
	// 都不能被指定成覆盖目标（否则「rest.stage.exe + oldExe=victim.exe」就能覆盖并执行它）
	if _, err := validatePendingUpdate(pendingPath, stageExe); err == nil {
		t.Fatalf("self-update 分支只认由 stage 副本名反推出的那个 exe，victim.exe 应被拒绝")
	}
}

// TestValidatePendingUpdateRenamedInstall 用户把便携包里的 rest.exe 改成别的名字后，
// 自更新仍要能认回「被替换的就是那个 exe」：stage 副本名由安装名派生，不是写死 rest。
func TestValidatePendingUpdateRenamedInstall(t *testing.T) {
	root := t.TempDir()
	exePath := filepath.Join(root, "休息提醒.exe")
	if err := os.WriteFile(exePath, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	updateDir := filepath.Join(root, updateDirName)
	if err := os.MkdirAll(updateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	newExe := filepath.Join(updateDir, newExeName)
	if err := os.WriteFile(newExe, []byte("NEW"), 0o755); err != nil {
		t.Fatal(err)
	}
	stageExe := filepath.Join(updateDir, stageNameForExe(exePath))
	if err := os.WriteFile(stageExe, []byte("STAGE"), 0o755); err != nil {
		t.Fatal(err)
	}
	pendingPath := filepath.Join(updateDir, "pending.json")
	body := `{"version":"1.0.1","oldExe":"` + jsonEscape(exePath) + `","newExe":"` + jsonEscape(newExe) + `"}`
	if err := os.WriteFile(pendingPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := validatePendingUpdate(pendingPath, stageExe); err != nil {
		t.Fatalf("改过名的安装目录也要能自更新（否则相当于堵死一条正常流程）: %v", err)
	}
	// 反过来：拿安装目录里的另一个 exe 去冒充 stage 副本，仍要被拒
	if _, err := validatePendingUpdate(pendingPath, exePath); err != nil {
		// --updated 形态（本进程就是目标）本来就该通过
		t.Fatalf("--updated 形态应通过: %v", err)
	}
}

// TestValidatePendingUpdateInstallDirNamedUpdate 安装目录本身就叫 update（D:\update\）时，
// --updated 不能被误判成自更新分支（否则 .bak 的删除范围会跟着抬一级）。
func TestValidatePendingUpdateInstallDirNamedUpdate(t *testing.T) {
	root := t.TempDir()
	installDir := filepath.Join(root, updateDirName) // 安装目录 = .../update
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		t.Fatal(err)
	}
	exePath := filepath.Join(installDir, "rest.exe")
	if err := os.WriteFile(exePath, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	updateDir := filepath.Join(installDir, updateDirName)
	if err := os.MkdirAll(updateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	newExe := filepath.Join(updateDir, newExeName)
	if err := os.WriteFile(newExe, []byte("NEW"), 0o755); err != nil {
		t.Fatal(err)
	}
	pendingPath := filepath.Join(updateDir, "pending.json")
	body := `{"version":"1.0.1","oldExe":"` + jsonEscape(exePath) + `","newExe":"` + jsonEscape(newExe) + `"}`
	if err := os.WriteFile(pendingPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := validatePendingUpdate(pendingPath, exePath); err != nil {
		t.Fatalf("安装目录叫 update 时 --updated 也应通过: %v", err)
	}
}

// TestCheckPendingNewExe 覆盖前的专项检查：新程序必须就在 update 目录内、且是常规文件。
func TestCheckPendingNewExe(t *testing.T) {
	root := t.TempDir()
	updateDir := filepath.Join(root, updateDirName)
	if err := os.MkdirAll(updateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	good := filepath.Join(updateDir, newExeName)
	if err := os.WriteFile(good, []byte("NEW"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := checkPendingNewExe(updateDir, good); err != nil {
		t.Fatalf("正常新程序应通过: %v", err)
	}
	outside := filepath.Join(root, "outside.exe")
	if err := os.WriteFile(outside, []byte("X"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := checkPendingNewExe(updateDir, outside); err == nil {
		t.Fatalf("update 目录之外的新程序应被拒绝")
	}
	if err := checkPendingNewExe(updateDir, filepath.Join(updateDir, "missing.exe")); err == nil {
		t.Fatalf("不存在的新程序应被拒绝")
	}
	if err := checkPendingNewExe(updateDir, updateDir); err == nil {
		t.Fatalf("目录不是常规文件，应被拒绝")
	}
}

// TestPendingUpdateRejectsPendingOutsideUpdateDir ①：`--updated` 指向 update 目录之外的
// 目录时，一次都不能删 —— 否则一个被改过的快捷方式参数就是「任意目录递归删除」。
func TestPendingUpdateRejectsPendingOutsideUpdateDir(t *testing.T) {
	root := t.TempDir()
	exePath := filepath.Join(root, "rest.exe")
	if err := os.WriteFile(exePath, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	withCurrentExe(t, exePath)
	// 与安装目录平级的「受害目录」：里面放 pending.json，删它就会丢用户数据
	victim := filepath.Join(root, "victim")
	if err := os.MkdirAll(victim, 0o755); err != nil {
		t.Fatal(err)
	}
	victimNew := filepath.Join(victim, "rest.exe")
	if err := os.WriteFile(victimNew, []byte("NEW"), 0o755); err != nil {
		t.Fatal(err)
	}
	pendingPath := filepath.Join(victim, "pending.json")
	body := `{"version":"9.9.9","oldExe":"` + jsonEscape(exePath) + `","newExe":"` + jsonEscape(victimNew) + `"}`
	if err := os.WriteFile(pendingPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	old := pendingUpdatePath
	defer func() { pendingUpdatePath = old }()
	pendingUpdatePath = pendingPath

	a := &App{cfg: settings.New(map[string]any{}, filepath.Join(root, "settings.json")), mu: sync.Mutex{}}
	if info := a.PendingUpdate(); info != nil {
		t.Fatalf("越界的 pending 不该展示摘要：%v", info)
	}
	if pendingUpdatePath != "" {
		t.Fatalf("越界的 pending 应被清空，实得 %q", pendingUpdatePath)
	}
	// 删除数为 0：目录和里面两个文件都还在
	entries, err := os.ReadDir(victim)
	if err != nil {
		t.Fatalf("受害目录被删了：%v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("受害目录里应仍有 2 个文件，实得 %d", len(entries))
	}
	if b, err := os.ReadFile(victimNew); err != nil || string(b) != "NEW" {
		t.Fatalf("受害目录里的文件被改动：%q %v", b, err)
	}
}

// TestRunSelfUpdateRejectsForeignOldExe ②：pending 里的 OldExe 不是当前 exe 时，
// runSelfUpdate 必须什么都不做 —— 既不改名备份，也不把 NewExe 覆盖上去。
// 同时覆盖「安装目录里任意 .exe 都能被指定覆盖」这条窄口（self-update 分支）：
// 复现路径是造 <root>/update/{pending.json, rest.stage.exe} 再让 OldExe 指向 <root>/victim.exe。
func TestRunSelfUpdateRejectsForeignOldExe(t *testing.T) {
	root, exePath, updateDir, newExe, _ := writePendingLayout(t, updatePending{Version: "1.0.1"})
	if err := os.WriteFile(exePath, []byte("CURRENT"), 0o755); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(root, "victim.exe")
	if err := os.WriteFile(victim, []byte("VICTIM"), 0o755); err != nil {
		t.Fatal(err)
	}
	stageExe := filepath.Join(updateDir, stageNameForExe(exePath))
	if err := os.WriteFile(stageExe, []byte("STAGE"), 0o755); err != nil {
		t.Fatal(err)
	}
	// pending 指向同目录里的另一个 exe：两种入口都必须拒绝
	pendingPath := filepath.Join(updateDir, "pending.json")
	body := `{"version":"1.0.1","oldExe":"` + jsonEscape(victim) + `","newExe":"` + jsonEscape(newExe) + `"}`
	if err := os.WriteFile(pendingPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	// ① --self-update 形态（本进程是 stage 副本）
	withCurrentExe(t, stageExe)
	runSelfUpdate(pendingPath)
	// ② --updated 形态（本进程就是安装目录里的 exe）
	withCurrentExe(t, exePath)
	runSelfUpdate(pendingPath)

	if b, err := os.ReadFile(victim); err != nil || string(b) != "VICTIM" {
		t.Fatalf("外来的 OldExe 被覆盖了：%q %v", b, err)
	}
	if b, err := os.ReadFile(exePath); err != nil || string(b) != "CURRENT" {
		t.Fatalf("当前 exe 不该被动：%q %v", b, err)
	}
	if _, err := os.Stat(victim + ".bak"); !os.IsNotExist(err) {
		t.Fatalf("被拒绝的路径不该留下 .bak（err=%v）", err)
	}
	if b, err := os.ReadFile(newExe); err != nil || string(b) != "NEW" {
		t.Fatalf("新 exe 不该被动：%q %v", b, err)
	}
}

// jsonEscape 把路径转成 JSON 字符串字面量（Windows 路径带反斜杠，手拼会坏）。
func jsonEscape(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return ""
	}
	return string(b[1 : len(b)-1])
}

// TestCheckForUpdateWritesSitesUnderLock 配置写入路径：并发跑 checkForUpdate 的同时
// 反复「拿快照读配置」（设置页 / 调度器 / 外卖窗口都是这种读法），必须不 panic。
// 修复前这里是「无锁原地改 raw」，配合下面的读法就是 Go 运行时的
// "concurrent map read and map write" —— fatal，recover 兜不住（用 -race 跑更直接）。
func TestCheckForUpdateWritesSitesUnderLock(t *testing.T) {
	t.Setenv("WR_SETTINGS_PATH", filepath.Join(t.TempDir(), "settings.json"))
	const sum = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	wantSites := []string{"https://a.example/version.json", "https://b.example/version.json"}
	body := `{"version":"v999.0.0","url":["https://a.example/rest.zip"],` +
		`"sites":["https://a.example/version.json","https://b.example/version.json"],` +
		`"sha256":"` + sum + `"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	cfg := settings.New(settings.DefaultSettingsMap(), "")
	cfg.SetSites([]string{srv.URL + "/version.json"}) // 回环 http 源，单测专用
	a := &App{cfg: cfg, mu: sync.Mutex{}}
	// 修复前的原地改会把这份「旧对象」也写脏，这里留个句柄好断言
	before := a.CfgSnapshot()

	// 并发读：模拟设置页 / 外卖窗口「拿快照后不再持锁」的读法
	stop := make(chan struct{})
	var readers sync.WaitGroup
	for i := 0; i < 4; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				s := a.CfgSnapshot()
				_ = s.UpdateSites()
				_ = s.AppearanceCfg()
				_ = s.Flag(true, "checkUpdateOnStart")
			}
		}()
	}

	var checkers sync.WaitGroup
	for i := 0; i < 8; i++ {
		checkers.Add(1)
		go func() {
			defer checkers.Done()
			if st := a.checkForUpdate(); st.Status == "" {
				t.Errorf("checkForUpdate 返回了空状态")
			}
		}()
	}
	checkers.Wait()
	close(stop)
	readers.Wait()

	// 远程下发的 sites 落到了当前配置（并已落盘）
	if got := a.CfgSnapshot().UpdateSites(); !reflect.DeepEqual(got, wantSites) {
		t.Fatalf("远程下发的 sites 未写入：%v", got)
	}
	// 但**不能原地改**：替换前的对象必须保持原样，否则快照读仍会与写并发
	if got := before.UpdateSites(); !reflect.DeepEqual(got, []string{srv.URL + "/version.json"}) {
		t.Fatalf("旧配置对象被原地改写了：%v", got)
	}
	data, err := os.ReadFile(os.Getenv("WR_SETTINGS_PATH"))
	if err != nil {
		t.Fatalf("配置未落盘: %v", err)
	}
	var onDisk map[string]any
	if err := json.Unmarshal(data, &onDisk); err != nil {
		t.Fatalf("落盘 JSON 解析失败: %v", err)
	}
	if got := jsonx.AnySliceToStrings(onDisk["sites"]); !reflect.DeepEqual(got, wantSites) {
		t.Fatalf("落盘里的 sites 不符：%v", got)
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

	// 哈希不符 → 必须报错，且不留下“看起来能用”的包
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
