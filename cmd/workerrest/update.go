package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"workerrest/internal/jsonx"
	"workerrest/internal/update"
)

const (
	updateTimeout = 15 * time.Second
	// updateDirName 解压 / 暂存目录名（运行目录下）
	updateDirName = "update"
	// packageName 更新包文件名：version.json 的 url 数组补全后指向的文件，
	// 下载到 update 目录下也用这个名字
	packageName = "rest.zip"
	// stageExeName 自更新子进程副本名（与旧 exe 是不同的文件，避免锁住自身）
	stageExeName = "rest.stage.exe"
	// newExeName 包内新 exe 的名字（解压出来后用于复制到位）
	newExeName = "rest.exe"
)

// 出厂兜底更新源 / 兜底更新包地址：打包进程序里的最后一道保险。
//
// 配置里的 sites 会被远程 version.json 整体覆盖（服务端可随时换源），所以这里把官方地址
// 额外固定排在候选链**最后**：只要它还在，软件就永远留有一条官方更新通道。其他源（如
// nav.hcctc.cn）都是**可选项** —— version.json 给了就用、没给就不用，程序里不硬编码任何
// 第三方地址。声明成 var 而不是 const：单测要能把它们置空，避免真的去访问公网。
var (
	defaultUpdateSite = "https://www.goodgoodlive.com/worker-rest/version.json"
	defaultPackageURL = "https://www.goodgoodlive.com/worker-rest/rest.zip"
)

// withFallbackSite 把兜底更新源补到候选链末尾：去空白、去重，兜底已存在则不重复追加。
// 配置为空时返回的仍是“只有兜底”这一条，因此“没配更新源就完全查不了”不会发生。
func withFallbackSite(sites []string) []string {
	out := make([]string, 0, len(sites)+1)
	seen := make(map[string]struct{}, len(sites)+1)
	for _, s := range sites {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	if fb := strings.TrimSpace(defaultUpdateSite); fb != "" {
		if _, dup := seen[fb]; !dup {
			out = append(out, fb)
		}
	}
	return out
}

// withFallbackPackage 把兜底更新包地址补到下载候选末尾（downloadPackage 会再按规则补全）。
func withFallbackPackage(urls []string) []string {
	out := make([]string, 0, len(urls)+1)
	for _, u := range urls {
		if u = strings.TrimSpace(u); u != "" {
			out = append(out, u)
		}
	}
	if fb := strings.TrimSpace(defaultPackageURL); fb != "" {
		out = append(out, fb)
	}
	return out
}

// versionInfo 远程 version.json 的结构。
// SHA256 是 rest.zip 的哈希（必填，见 update_trust.go 的信任边界说明）；
// Size 是可选的大小提示，只用于提前报错，真正的判据仍是哈希。
type versionInfo struct {
	Version string   `json:"version"`
	Date    string   `json:"date"`
	URL     []string `json:"url"`
	Sites   []string `json:"sites"`
	Changes []string `json:"changes"`
	SHA256  string   `json:"sha256"`
	Size    int64    `json:"size"`
}

// updateCheckResult 一次检查的落库形状（字段名与渲染层读的 st.status / st.current /
// st.latest / st.url / st.date / st.changes / st.message / st.ignored 对齐）
type updateCheckResult struct {
	Status  string `json:"status"`
	Current string `json:"current"`
	Latest  string `json:"latest,omitempty"`
	// URLs 下载地址数组（version.json 的 url 字段）：StartUpdate 按顺序补全下载。
	URLs []string `json:"url,omitempty"`
	// SHA256 更新包的哈希：检查时从清单取、随状态一起持久化，StartUpdate 用它校验
	// 下载下来的包。缺这个值等同于“不允许更新”。
	SHA256  string   `json:"sha256,omitempty"`
	Date    string   `json:"date,omitempty"`
	Changes []string `json:"changes,omitempty"`
	Message string   `json:"message,omitempty"`
	// Ignored 仅当发现新版本、且该版本正是被用户“忽略”的版本时为 true：
	// 前端据此仍显示“立即更新”按钮，但不再自动弹窗（直到发现更大的版本号）。
	Ignored bool `json:"ignored,omitempty"`
}

// toMap 转成 map[string]any（推给前端 / 存 updateStatus 用）
func (r updateCheckResult) toMap() map[string]any {
	m := map[string]any{"status": r.Status, "current": r.Current}
	if r.Latest != "" {
		m["latest"] = r.Latest
	}
	if len(r.URLs) > 0 {
		m["url"] = r.URLs
	}
	if r.SHA256 != "" {
		m["sha256"] = r.SHA256
	}
	if r.Date != "" {
		m["date"] = r.Date
	}
	if len(r.Changes) > 0 {
		cs := make([]any, 0, len(r.Changes))
		for _, c := range r.Changes {
			cs = append(cs, c)
		}
		m["changes"] = cs
	}
	if r.Message != "" {
		m["message"] = r.Message
	}
	if r.Ignored {
		m["ignored"] = true
	}
	return m
}

// pendingUpdatePath 由 main 从命令行 `--updated <path>` 注入：指向一次成功替换后
// 留下的 pending.json，供前端拉取更新摘要并清理临时目录。
var pendingUpdatePath string

// CheckUpdate 手动检查更新（设置页“检查更新”按钮）。失败也如实返回 error 状态，
// 页面会显示“检查更新失败：…”。
//
// 无头（server）模式不发网络请求：冒烟要确定性，不把结果押在网络是否可达上，
// 返回固定的“未接入”error 形状（版本比较逻辑由 update_test.go 的纯函数用例覆盖）。
func (a *App) CheckUpdate() map[string]any {
	if serverMode {
		st := updateCheckResult{
			Status:  "error",
			Current: appVersion,
			Message: "无头模式不检查更新",
		}
		a.publishUpdateStatus(st)
		return a.snapshotUpdateStatus()
	}
	st := a.checkForUpdate()
	a.publishUpdateStatus(st)
	return a.snapshotUpdateStatus()
}

// PendingUpdate 前端启动后拉取“本次启动刚完成更新”的摘要（version/date/changes）。
// 返回 nil 表示没有待展示的更新；展示后即清理 update 临时目录与旧 exe 备份。
func (a *App) PendingUpdate() map[string]any {
	if pendingUpdatePath == "" {
		return nil
	}
	data, err := os.ReadFile(pendingUpdatePath)
	if err != nil {
		pendingUpdatePath = ""
		return nil
	}
	var p updatePending
	if err := json.Unmarshal(data, &p); err != nil {
		pendingUpdatePath = ""
		return nil
	}
	// 清理 update 临时目录（含 stage 副本 / 新 exe 副本 / pending 自身）
	if dir := filepath.Dir(pendingUpdatePath); dir != "" {
		_ = os.RemoveAll(dir)
	}
	// 清理旧程序备份
	if p.OldExe != "" {
		_ = os.Remove(p.OldExe + ".bak")
	}
	pendingUpdatePath = ""
	// 更新已生效（当前已在新版本上运行）：清除“有更新”的持久化状态，
	// 避免重启后旧版本号仍挂在关于页。
	a.cfg.SetLastUpdateResult(nil)
	if err := a.cfg.Save(); err != nil {
		log.Printf("[update] 清除更新状态失败: %v", err)
	}
	return map[string]any{
		"version":         p.Version,
		"date":            p.Date,
		"changes":         jsonx.StringSliceToAny(p.Changes),
		"settingsVisible": p.SettingsVisible,
		"activeTab":       p.ActiveTab,
	}
}

// IgnoreVersion 用户点了“忽略本版本”：把该版本号记进配置。之后自动/手动检查到的
// 若是同一版本，前端不再自动弹窗（“立即更新”按钮仍显示），直到发现更大的版本号。
func (a *App) IgnoreVersion(version string) map[string]any {
	if version == "" {
		return map[string]any{"status": "error", "message": "缺少版本号"}
	}
	a.cfg.SetIgnoredVersion(version)
	if err := a.cfg.Save(); err != nil {
		log.Printf("[update] 写入忽略版本失败: %v", err)
		return map[string]any{"status": "error", "message": "保存失败：" + err.Error()}
	}
	// 同步当前内存状态：标记 ignored，前端据此在本次会话里也不再自动弹窗（按钮仍显示）。
	a.mu.Lock()
	if a.updateStatus != nil {
		a.updateStatus["ignored"] = true
	}
	a.mu.Unlock()
	// 持久化 ignored 标记，使重启后同一版本仍不自动弹窗（按钮仍显示）。
	a.persistUpdateResult(a.snapshotUpdateStatus())
	a.app.Event.Emit("update:status-changed", a.snapshotUpdateStatus())
	return map[string]any{"status": "ok"}
}

// snapshotUpdateStatus 取当前 updateStatus 的副本（避免把内部 map 直接交给前端闭包修改）。
func (a *App) snapshotUpdateStatus() map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.updateStatus == nil {
		return map[string]any{"status": "idle", "current": appVersion}
	}
	out := make(map[string]any, len(a.updateStatus))
	for k, v := range a.updateStatus {
		out[k] = v
	}
	return out
}

// publishUpdateStatus 统一“落内存 + 推前端 + 持久化”一次检查结果。
// 无论自动检查还是手动检查，只要发现新版本，重启后仍会显示“有更新”，
// 直到更新真正成功（StartUpdate / PendingUpdate 会清除该持久化结果）。
func (a *App) publishUpdateStatus(st updateCheckResult) {
	m := st.toMap()
	a.mu.Lock()
	a.updateStatus = m
	a.mu.Unlock()
	if a.app != nil && a.app.Event != nil {
		a.app.Event.Emit("update:status-changed", m)
	}
	a.persistUpdateResult(m)
}

// persistUpdateResult 把当前检查结果写入配置并落盘（nil 表示清除）。
// cfg 的读写必须持 a.mu（autoUpdateLoop 是后台 goroutine，主线程也会同时访问 cfg，
// 见 race 检测），这里与其它 cfg 写入点一致：锁内做 SetLastUpdateResult + Save。
func (a *App) persistUpdateResult(m map[string]any) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cfg.SetLastUpdateResult(m)
	if err := a.cfg.Save(); err != nil {
		log.Printf("[update] 写入更新状态失败: %v", err)
	}
}

// restoreUpdateStatus 启动早期调用：从配置恢复上次“发现新版本”的结果，
// 使未更新的状态在重启后仍然展示。若该版本已不比当前版本新（如用户手动升级），
// 视为已失效并清理，避免长期残留“有更新”。
func (a *App) restoreUpdateStatus() {
	res := a.cfg.LastUpdateResult()
	if res == nil {
		return
	}
	if latest, ok := res["latest"].(string); ok && latest != "" {
		// update-available：校验版本仍有效
		if !update.IsNewerVersion(appVersion, latest) {
			a.clearUpdateResult()
			return
		}
	} else if res["status"] == "update-available" {
		// 缺 latest 的更新结果视为异常，清理
		a.clearUpdateResult()
		return
	}
	a.mu.Lock()
	a.updateStatus = copyAnyMap(res)
	a.mu.Unlock()
	log.Printf("[update] 已从配置恢复上次更新状态: %v", res["status"])
}

// clearUpdateResult 清除内存与持久化的更新结果（更新成功后调用）。
func (a *App) clearUpdateResult() {
	a.mu.Lock()
	a.updateStatus = nil
	a.mu.Unlock()
	a.cfg.SetLastUpdateResult(nil)
	if err := a.cfg.Save(); err != nil {
		log.Printf("[update] 清除更新状态失败: %v", err)
	}
}

// copyAnyMap 深拷贝一份 map[string]any（一层 + 数组元素复制，覆盖 toMap 形状），
// 避免把配置 raw 里的 map 直接交给渲染层闭包修改。
func copyAnyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		if arr, ok := v.([]any); ok {
			cp := make([]any, len(arr))
			copy(cp, arr)
			out[k] = cp
		} else {
			out[k] = v
		}
	}
	return out
}
