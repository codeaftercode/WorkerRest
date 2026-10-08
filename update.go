package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// 检查更新：electron/main.ts 的 checkForUpdate 对应物。
//
// 更新源是 GitHub Releases（与 Electron 版同一个仓库 `lwb/worker-rest`），走公开的
// `api.github.com/repos/.../releases/latest`，用 net/http 实现，**零额外依赖**。
// 结果统一写进 a.updateStatus 并推给打开着的设置页（update:status-changed 事件），
// 失败如实回报、不伪装成「已是最新」。

const (
	updateRepoAPI  = "https://api.github.com/repos/lwb/worker-rest/releases/latest"
	updateRepoPage = "https://github.com/lwb/worker-rest/releases/latest"
	// updateTimeout 单次检查的超时（与 Electron 版 6 秒 AbortController 同义）
	updateTimeout = 6 * time.Second
)

// updateCheckResult 一次检查的落库形状（字段名与渲染层读的 st.status / st.current /
// st.latest / st.url / st.message 对齐）
type updateCheckResult struct {
	Status  string `json:"status"`
	Current string `json:"current"`
	Latest  string `json:"latest,omitempty"`
	URL     string `json:"url,omitempty"`
	Message string `json:"message,omitempty"`
}

// toMap 转成 map[string]any（推给前端 / 存 updateStatus 用）
func (r updateCheckResult) toMap() map[string]any {
	m := map[string]any{"status": r.Status, "current": r.Current}
	if r.Latest != "" {
		m["latest"] = r.Latest
	}
	if r.URL != "" {
		m["url"] = r.URL
	}
	if r.Message != "" {
		m["message"] = r.Message
	}
	return m
}

// CheckUpdate 手动检查更新（设置页「检查更新」按钮）。失败也如实返回 error 状态，
// 页面会显示「检查更新失败：…」。
//
// 无头（server）模式不发网络请求：冒烟要确定性，不把结果押在 GitHub 是否可达上。
// 这里返回一个固定的「未接入」error 形状（status + current + message 齐全），
// 版本比较逻辑由 update_test.go 的纯函数用例覆盖。
func (a *App) CheckUpdate() map[string]any {
	if serverMode {
		st := updateCheckResult{
			Status:  "error",
			Current: appVersion,
			Message: "无头模式不检查更新",
		}
		a.mu.Lock()
		a.updateStatus = st.toMap()
		a.mu.Unlock()
		a.app.Event.Emit("update:status-changed", st.toMap())
		return st.toMap()
	}
	st := a.checkForUpdate()
	a.mu.Lock()
	a.updateStatus = st.toMap()
	a.mu.Unlock()
	a.app.Event.Emit("update:status-changed", st.toMap())
	return st.toMap()
}

// checkForUpdate 请求更新源比对版本。latest 严格大于 current 才算有新版。
// 与 Electron 版同口径：v 前缀 / 缺段都容忍，三段式比较。
func (a *App) checkForUpdate() updateCheckResult {
	current := appVersion
	client := &http.Client{Timeout: updateTimeout}
	req, err := http.NewRequest(http.MethodGet, updateRepoAPI, nil)
	if err != nil {
		return updateCheckResult{Status: "error", Current: current, Message: err.Error()}
	}
	req.Header.Set("User-Agent", "worker-rest-update-check")
	req.Header.Set("Accept", "application/vnd.github+json")
	res, err := client.Do(req)
	if err != nil {
		return updateCheckResult{Status: "error", Current: current, Message: err.Error()}
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return updateCheckResult{Status: "error", Current: current, Message: "HTTP " + strconv.Itoa(res.StatusCode)}
	}
	var data struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.NewDecoder(res.Body).Decode(&data); err != nil {
		return updateCheckResult{Status: "error", Current: current, Message: "响应解析失败"}
	}
	latest := strings.TrimPrefix(strings.TrimSpace(data.TagName), "v")
	if latest == "" {
		return updateCheckResult{Status: "error", Current: current, Message: "响应里没有版本号"}
	}
	if isNewerVersion(current, latest) {
		url := data.HTMLURL
		if !strings.HasPrefix(url, "https://") {
			url = updateRepoPage
		}
		return updateCheckResult{Status: "update-available", Current: current, Latest: latest, URL: url}
	}
	return updateCheckResult{Status: "up-to-date", Current: current}
}

// isNewerVersion 简单三段版本号比较：latest 严格大于 current 才算新版
// （v 前缀 / 缺段都容忍）。逐字对应 main.ts 的 isNewerVersion。
func isNewerVersion(current, latest string) bool {
	a := parseVersion(current)
	b := parseVersion(latest)
	for i := 0; i < 3; i++ {
		ai, bi := 0, 0
		if i < len(a) {
			ai = a[i]
		}
		if i < len(b) {
			bi = b[i]
		}
		if bi != ai {
			return bi > ai
		}
	}
	return false
}

// parseVersion 把版本号拆成整数段（v 前缀剥掉，非数字段按 0 处理）
func parseVersion(v string) []int {
	s := strings.TrimPrefix(strings.TrimSpace(v), "v")
	parts := strings.Split(s, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, _ := strconv.Atoi(strings.TrimSpace(p))
		out = append(out, n)
	}
	return out
}

// startAutoUpdateCheck 启动时按「启动时检查更新」配置决定要不要静默检查一次。
// 失败不打扰用户（结果仍会推给设置页，由它显示）；与 Electron 的
// `if (settings.checkUpdateOnStart) setTimeout(checkForUpdate(false), 15s)` 同义，
// 这里延时 15 秒，避免和启动阶段的其它初始化抢网络。
func (a *App) startAutoUpdateCheck() {
	if serverMode {
		log.Printf("[update] 无头模式不检查更新")
		return
	}
	if !a.cfgSnapshot().Flag(true, "checkUpdateOnStart") {
		log.Printf("[update] 启动时检查更新已关闭，跳过")
		return
	}
	go func() {
		time.Sleep(15 * time.Second)
		st := a.checkForUpdate()
		a.mu.Lock()
		a.updateStatus = st.toMap()
		a.mu.Unlock()
		a.app.Event.Emit("update:status-changed", st.toMap())
		log.Printf("[update] 启动自动检查: %s", st.Status)
	}()
}
