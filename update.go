package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/bodgit/sevenzip"
)

// 检查更新 + 自动更新：更新源由配置里的 `sites` 数组给出，每一项都是 http/https
// 地址，指向一份远程 version.json（格式见用户需求）。依次容错拉取第一项、失败取
// 下一项、全失败提示失败。
//
// 自动更新流程（Windows 下运行中的 exe 被文件锁占用，不能原地覆盖）：
//  1. version.json 的 url 是数组：取第一项补全为完整包地址（不以 .7z 结尾的拼上
//     /rest.7z，以 / 结尾的只拼 rest.7z）下载 rest.7z；下载失败取下一项、同样
//     补全后重试，全部失败才报错；
//  2. 解压到 <运行目录>/update，读取解压目录及其子目录下所有 json 配置，按
//     「顶层键」增量合并进本地配置（本地已有键不覆盖、本地无则新增），随后落盘；
//  3. 把包内的新 exe 暂存为 update/rest.stage.exe，写 pending.json，
//     启动该 stage 子进程（带 --self-update <pending>），当前进程退出；
//  4. stage 进程等旧 exe 解锁后将其改名为 .bak、把新 exe 复制到位，再启动
//     rest.exe --updated <pending> 并显示更新摘要。
// 第 4 步之所以要从「副本」启动，是因为 self-update 进程若直接是旧 exe 镜像就会
// 锁住自己、无法 rename 旧 exe —— 必须用一个独立的副本文件来干替换的活。

const (
	updateTimeout = 15 * time.Second
	// updateDirName 解压 / 暂存目录名（运行目录下）
	updateDirName = "update"
	// packageName 更新包文件名：version.json 的 url 数组补全后指向的文件，
	// 下载到 update 目录下也用这个名字
	packageName = "rest.7z"
	// stageExeName 自更新子进程副本名（与旧 exe 是不同的文件，避免锁住自身）
	stageExeName = "rest.stage.exe"
	// newExeName 包内新 exe 的名字（解压出来后用于复制到位）
	newExeName = "rest.exe"
)

// versionInfo 远程 version.json 的结构
type versionInfo struct {
	Version string   `json:"version"`
	Date    string   `json:"date"`
	URL     []string `json:"url"`
	Sites   []string `json:"sites"`
	Changes []string `json:"changes"`
}

// updateCheckResult 一次检查的落库形状（字段名与渲染层读的 st.status / st.current /
// st.latest / st.url / st.date / st.changes / st.message / st.ignored 对齐）
type updateCheckResult struct {
	Status  string `json:"status"`
	Current string `json:"current"`
	Latest  string `json:"latest,omitempty"`
	// URLs 下载地址数组（version.json 的 url 字段）：StartUpdate 按顺序补全下载。
	URLs    []string `json:"url,omitempty"`
	Date    string   `json:"date,omitempty"`
	Changes []string `json:"changes,omitempty"`
	Message string   `json:"message,omitempty"`
	// Ignored 仅当发现新版本、且该版本正是被用户「忽略」的版本时为 true：
	// 前端据此仍显示「立即更新」按钮，但不再自动弹窗（直到发现更大的版本号）。
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

// CheckUpdate 手动检查更新（设置页「检查更新」按钮）。失败也如实返回 error 状态，
// 页面会显示「检查更新失败：…」。
//
// 无头（server）模式不发网络请求：冒烟要确定性，不把结果押在网络是否可达上，
// 返回固定的「未接入」error 形状（版本比较逻辑由 update_test.go 的纯函数用例覆盖）。
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

// PendingUpdate 前端启动后拉取「本次启动刚完成更新」的摘要（version/date/changes）。
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
	// 更新已生效（当前已在新版本上运行）：清除「有更新」的持久化状态，
	// 避免重启后旧版本号仍挂在关于页。
	a.cfg.SetLastUpdateResult(nil)
	if err := a.cfg.Save(); err != nil {
		log.Printf("[update] 清除更新状态失败: %v", err)
	}
	return map[string]any{
		"version":         p.Version,
		"date":            p.Date,
		"changes":         stringSliceToAny(p.Changes),
		"settingsVisible": p.SettingsVisible,
		"activeTab":       p.ActiveTab,
	}
}

// IgnoreVersion 用户点了「忽略本版本」：把该版本号记进配置。之后自动/手动检查到的
// 若是同一版本，前端不再自动弹窗（「立即更新」按钮仍显示），直到发现更大的版本号。
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

// publishUpdateStatus 统一「落内存 + 推前端 + 持久化」一次检查结果。
// 无论自动检查还是手动检查，只要发现新版本，重启后仍会显示「有更新」，
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
func (a *App) persistUpdateResult(m map[string]any) {
	a.cfg.SetLastUpdateResult(m)
	if err := a.cfg.Save(); err != nil {
		log.Printf("[update] 写入更新状态失败: %v", err)
	}
}

// restoreUpdateStatus 启动早期调用：从配置恢复上次「发现新版本」的结果，
// 使未更新的状态在重启后仍然展示。若该版本已不比当前版本新（如用户手动升级），
// 视为已失效并清理，避免长期残留「有更新」。
func (a *App) restoreUpdateStatus() {
	res := a.cfg.LastUpdateResult()
	if res == nil {
		return
	}
	if latest, ok := res["latest"].(string); ok && latest != "" {
		// update-available：校验版本仍有效
		if !isNewerVersion(appVersion, latest) {
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

// checkForUpdate 按配置里的 sites 依次容错拉取 version.json 并比对版本。
// latest 严格大于 current 才算有新版。
func (a *App) checkForUpdate() updateCheckResult {
	current := appVersion
	sites := a.cfg.UpdateSites()
	if len(sites) == 0 {
		return updateCheckResult{
			Status:  "error",
			Current: current,
			Message: "未配置更新源（请在配置 sites 中填写 version.json 地址）",
		}
	}
	info, err := fetchVersionJSON(sites)
	if err != nil {
		return updateCheckResult{Status: "error", Current: current, Message: err.Error()}
	}
	// 远程 version.json 可下发更新源 sites：非空则覆盖（替换）本地配置里的 sites，
	// 让更新源可由服务端集中管控；空则保留本地原有值（避免误清空唯一更新源）。
	if len(info.Sites) > 0 {
		a.cfg.SetSites(info.Sites)
		if err := a.cfg.Save(); err != nil {
			log.Printf("[update] 写入远程下发的更新源失败: %v", err)
		}
	}
	latest := strings.TrimSpace(info.Version)
	if latest == "" {
		return updateCheckResult{Status: "error", Current: current, Message: "响应里没有版本号"}
	}
	if isNewerVersion(current, latest) {
		// 有新版却拿不到下载地址：直接报错，别等用户点「立即更新」才失败
		if len(info.URL) == 0 {
			return updateCheckResult{Status: "error", Current: current, Message: "响应里没有下载地址"}
		}
		st := updateCheckResult{
			Status:  "update-available",
			Current: current,
			Latest:  latest,
			URLs:    info.URL,
			Date:    info.Date,
			Changes: info.Changes,
		}
		// 忽略版本处理：被忽略的版本只抑制弹窗、不抑制按钮；
		// 但一旦发现「比被忽略版本更大」的版本，自动解除忽略并恢复弹窗。
		if ig := a.cfg.IgnoredVersion(); ig != "" {
			if isNewerVersion(ig, latest) {
				a.cfg.SetIgnoredVersion("")
				if err := a.cfg.Save(); err != nil {
					log.Printf("[update] 解除忽略版本失败: %v", err)
				}
			} else if sameVersion(ig, latest) {
				st.Ignored = true
			}
		}
		return st
	}
	return updateCheckResult{Status: "up-to-date", Current: current}
}

// fetchVersionJSON 依次尝试 sites 中的地址，第一项成功即返回；全部失败返回 error。
func fetchVersionJSON(sites []string) (versionInfo, error) {
	client := &http.Client{Timeout: updateTimeout}
	var lastErr error
	for _, site := range sites {
		info, err := fetchOneVersionJSON(client, site)
		if err == nil {
			return info, nil
		}
		log.Printf("[update] 拉取 %s 失败: %v", site, err)
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("所有更新源均不可用")
	}
	return versionInfo{}, lastErr
}

func fetchOneVersionJSON(client *http.Client, url string) (versionInfo, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return versionInfo{}, err
	}
	req.Header.Set("User-Agent", "rest-update-check")
	res, err := client.Do(req)
	if err != nil {
		return versionInfo{}, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return versionInfo{}, fmt.Errorf("HTTP %d", res.StatusCode)
	}
	var info versionInfo
	if err := json.NewDecoder(res.Body).Decode(&info); err != nil {
		return versionInfo{}, fmt.Errorf("响应解析失败")
	}
	return info, nil
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
		n := strconvAtoiSafe(p)
		out = append(out, n)
	}
	return out
}

// strconvAtoiSafe 容忍空/非法段（对应 JS 的 parseInt 容错）
func strconvAtoiSafe(p string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(p))
	return n
}

// sameVersion 两段版本号（按前三段）是否完全相同（v 前缀 / 缺段都容忍）。
// 用于判断「本次远程版本是否就是被忽略的那个版本」。
func sameVersion(a, b string) bool {
	av := parseVersion(a)
	bv := parseVersion(b)
	for i := 0; i < 3; i++ {
		x, y := 0, 0
		if i < len(av) {
			x = av[i]
		}
		if i < len(bv) {
			y = bv[i]
		}
		if x != y {
			return false
		}
	}
	return true
}

// todayStr 本地日期（YYYY-MM-DD），用作「当天是否已检查更新」的去重键。
func todayStr() string {
	return time.Now().Format("2006-01-02")
}

// startAutoUpdateCheck 实现「自动检查更新」：勾选了才跑；当天已查过则跳过
// （不重复联网）；真正触发点是「启动后第一次检测到系统非空闲」——
// 即用户真的回来操作电脑（idle.IsActive 连续 3 秒有输入），而不是启动那一瞬间。
// 失败不打扰用户（结果仍会推给设置页，由它显示）。
func (a *App) startAutoUpdateCheck() {
	if serverMode {
		log.Printf("[update] 无头模式不检查更新")
		return
	}
	if !a.cfg.Flag(true, "checkUpdateOnStart") {
		log.Printf("[update] 自动检查更新已关闭，跳过")
		return
	}
	if a.cfg.LastUpdateCheck() == todayStr() {
		log.Printf("[update] 今天已检查过更新，跳过自动检查")
		return
	}
	go func() {
		// 等到「启动后第一次非空闲」；idle 未启动（读不到系统 tick）时不触发。
		const pollEvery = 2 * time.Second
		const maxWait = 6 * time.Hour // 长时间无操作则放弃本次自动检查，等下次启动
		deadline := time.Now().Add(maxWait)
		for {
			if time.Now().After(deadline) {
				log.Printf("[update] 启动后长时间无活动，本次自动检查延后")
				return
			}
			if a.idle != nil && a.idle.IsActive() {
				break
			}
			time.Sleep(pollEvery)
		}
		// 轮询期间可能别处已查过，再确认一次当日去重
		if a.cfg.LastUpdateCheck() == todayStr() {
			return
		}
		st := a.checkForUpdate()
		a.publishUpdateStatus(st)
		// 当天去重：只有真正判定出结果（已是最新 / 有更新）才记今天查过了；
		// error（如网络抖动）不记，让用户当天重启还能再试一次。
		if st.Status != "error" {
			a.cfg.SetLastUpdateCheck(todayStr())
			if err := a.cfg.Save(); err != nil {
				log.Printf("[update] 写入上次检查日期失败: %v", err)
			}
		}
		log.Printf("[update] 启动自动检查: %s", st.Status)
	}()
}

// updatePending 自更新阶段落盘的元数据（供 --updated 启动后展示摘要 / 清理）
type updatePending struct {
	Version string   `json:"version"`
	Date    string   `json:"date"`
	Changes []string `json:"changes"`
	OldExe  string   `json:"oldExe"`
	NewExe  string   `json:"newExe"`
	// 更新前设置窗口状态：用于在更新重启后恢复「更新前所在的页面 / 是否可见」。
	SettingsVisible bool   `json:"settingsVisible"`
	ActiveTab       string `json:"activeTab"`
}

// StartUpdate 执行自动更新：下载 7z → 解压 → 增量合并配置 → 暂存新 exe →
// 启动 self-update 子进程替换并重启。任何一步失败都在退出前返回 error 形状，
// 只有全部就绪、子进程拉起后才 os.Exit（前端在此之前已显示「正在更新」遮罩）。
func (a *App) StartUpdate() map[string]any {
	if serverMode {
		return map[string]any{"status": "error", "message": "无头模式不支持自动更新"}
	}
	// 取最近一次检查结果（CheckUpdate 必须已跑过，且确实发现新版本）
	a.mu.Lock()
	st := a.updateStatus
	a.mu.Unlock()
	if st == nil || st["status"] != "update-available" {
		return map[string]any{"status": "error", "message": "当前没有可执行的更新"}
	}
	urls := statusURLs(st)
	if len(urls) == 0 {
		return map[string]any{"status": "error", "message": "缺少下载地址"}
	}
	latest, _ := st["latest"].(string)
	date, _ := st["date"].(string)
	changes := anySliceToStrings(st["changes"])

	exePath, err := os.Executable()
	if err != nil {
		return map[string]any{"status": "error", "message": "无法确定程序位置"}
	}
	exeDir := filepath.Dir(exePath)
	updateDir := filepath.Join(exeDir, updateDirName)

	// 干净的 update 目录
	if err := os.RemoveAll(updateDir); err != nil {
		return map[string]any{"status": "error", "message": "清理更新目录失败"}
	}
	if err := os.MkdirAll(updateDir, 0o755); err != nil {
		return map[string]any{"status": "error", "message": "创建更新目录失败"}
	}

	archive := filepath.Join(updateDir, packageName)
	if err := downloadPackage(urls, archive); err != nil {
		return map[string]any{"status": "error", "message": "下载更新包失败：" + err.Error()}
	}
	if err := extract7z(archive, updateDir); err != nil {
		return map[string]any{"status": "error", "message": "解压更新包失败：" + err.Error()}
	}

	// 包内新 exe
	newExe := findNewExe(updateDir)
	if newExe == "" {
		return map[string]any{"status": "error", "message": "更新包内未找到可执行文件"}
	}

	// 增量合并配置（顶层键：本地已有不覆盖，本地无则新增），随后落盘
	if err := mergeConfigFromDir(updateDir, a.cfg.Raw()); err != nil {
		log.Printf("[update] 合并配置跳过（%v），仅替换程序", err)
	}
	if err := a.cfg.Save(); err != nil {
		log.Printf("[update] 更新前配置落盘失败（%v），继续替换程序", err)
	}

	// 写 pending + 暂存 stage 副本
	pendingPath := filepath.Join(updateDir, "pending.json")
	// 记录更新前设置窗口状态：若窗口可见（用户正在设置页点更新），重启后恢复之。
	settingsVisible := false
	if w := a.settingsWindow(); w != nil {
		settingsVisible = w.IsVisible()
	}
	a.mu.Lock()
	activeTab := a.activeTab
	a.mu.Unlock()
	pending := updatePending{
		Version:         latest,
		Date:            date,
		Changes:         changes,
		OldExe:          exePath,
		NewExe:          newExe,
		SettingsVisible: settingsVisible,
		ActiveTab:       activeTab,
	}
	if data, err := json.MarshalIndent(pending, "", "  "); err == nil {
		_ = os.WriteFile(pendingPath, append(data, '\n'), 0o644)
	}
	stagePath := filepath.Join(updateDir, stageExeName)
	if err := copyFile(newExe, stagePath); err != nil {
		return map[string]any{"status": "error", "message": "暂存更新程序失败：" + err.Error()}
	}

	// 启动 self-update 子进程（独立副本，避免锁住自身），随后退出
	cmd := exec.Command(stagePath, "--self-update", pendingPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x00000200}
	if err := cmd.Start(); err != nil {
		return map[string]any{"status": "error", "message": "启动更新程序失败：" + err.Error()}
	}
	log.Printf("[update] 已拉起自更新子进程，主进程即将退出")
	// 更新即将生效：清除「有更新」的持久化状态，避免重启后旧版本号仍挂在关于页。
	a.clearUpdateResult()
	// 给子进程一点时间接管后再退出
	time.Sleep(300 * time.Millisecond)
	os.Exit(0)
	return nil // 不会到达
}

// downloadFile 下载 url 到 dest（流式写盘，避免大文件占内存）
func downloadFile(url, dest string) error {
	client := &http.Client{Timeout: 10 * time.Minute}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "rest-update")
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", res.StatusCode)
	}
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, res.Body); err != nil {
		return err
	}
	return nil
}

// statusURLs 从更新状态 map 里取出下载地址数组：内存里是 []string，
// 从配置 JSON 恢复回来会变成 []any，两种形状都认。
func statusURLs(st map[string]any) []string {
	switch v := st["url"].(type) {
	case []string:
		return append([]string(nil), v...)
	case []any:
		return anySliceToStrings(v)
	}
	return nil
}

// normalizePackageURL 把 version.json url 数组里的一项补全为完整的 7z 包地址：
// 已是以 .7z（忽略大小写）结尾的直接使用；以 / 结尾的只拼 rest.7z；其余拼 /rest.7z。
func normalizePackageURL(raw string) string {
	u := strings.TrimSpace(raw)
	if u == "" {
		return ""
	}
	if strings.HasSuffix(strings.ToLower(u), ".7z") {
		return u
	}
	if strings.HasSuffix(u, "/") {
		return u + packageName
	}
	return u + "/" + packageName
}

// downloadPackage 按 urls 顺序依次补全地址并下载，任一地址成功即返回；
// 全部失败返回最后一次的错误（空数组 / 全空白项同样报错）。
func downloadPackage(urls []string, dest string) error {
	var lastErr error
	for _, raw := range urls {
		u := normalizePackageURL(raw)
		if u == "" {
			continue
		}
		log.Printf("[update] 尝试下载更新包: %s", u)
		if err := downloadFile(u, dest); err != nil {
			log.Printf("[update] 下载更新包失败: %v", err)
			lastErr = err
			continue
		}
		return nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("没有可用的下载地址")
	}
	return lastErr
}

// extract7z 把 7z 包解压到 dest（保留目录结构）
func extract7z(archive, dest string) error {
	r, err := sevenzip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, f := range r.File {
		target := filepath.Join(dest, f.Name)
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, f.Mode())
		if err != nil {
			rc.Close()
			return err
		}
		if _, err := io.Copy(out, rc); err != nil {
			out.Close()
			rc.Close()
			return err
		}
		out.Close()
		rc.Close()
	}
	return nil
}

// findNewExe 在解压目录里找新 exe：优先与 newExeName 同名（忽略大小写），
// 退而求其次取第一个 *.exe
func findNewExe(dir string) string {
	var fallback string
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := strings.ToLower(d.Name())
		if name == newExeName {
			fallback = path
			return filepath.SkipAll
		}
		if strings.HasSuffix(name, ".exe") && fallback == "" {
			fallback = path
		}
		return nil
	})
	return fallback
}

// mergeConfigFromDir 遍历目录及其子目录下所有 json 文件，把每个文件的顶层键按
// 「本地无则新增、本地有则不改」合并进 raw（与用户需求的配置项增量合并同义）。
func mergeConfigFromDir(dir string, raw map[string]any) error {
	return filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".json") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var patch map[string]any
		if err := json.Unmarshal(data, &patch); err != nil {
			log.Printf("[update] 跳过无法解析的配置文件 %s: %v", path, err)
			return nil
		}
		for k, v := range patch {
			if _, exists := raw[k]; !exists {
				raw[k] = v
			}
		}
		return nil
	})
}

// stringSliceToAny []string → []any
func stringSliceToAny(s []string) []any {
	out := make([]any, 0, len(s))
	for _, v := range s {
		out = append(out, v)
	}
	return out
}

// anySliceToStrings 把 any 数组里的字符串取出来（容错非字符串项）
func anySliceToStrings(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, it := range arr {
		if s, ok := it.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// runSelfUpdate 由 `--self-update <pending>` 触发：等旧 exe 解锁 → 改名备份 →
// 复制新 exe 到位 → 启动 rest.exe --updated <pending> → 退出。
// 不在本函数里调用 app.Run，因此不会触发 Wails 单例互斥，旧进程退出后即可接管。
func runSelfUpdate(pendingPath string) {
	data, err := os.ReadFile(pendingPath)
	if err != nil {
		log.Fatalf("[self-update] 读取 pending 失败: %v", err)
	}
	var p updatePending
	if err := json.Unmarshal(data, &p); err != nil {
		log.Fatalf("[self-update] 解析 pending 失败: %v", err)
	}
	if err := replaceExe(p.OldExe, p.NewExe); err != nil {
		log.Fatalf("[self-update] 替换程序失败: %v", err)
	}
	cmd := exec.Command(p.OldExe, "--updated", pendingPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x00000200}
	if err := cmd.Start(); err != nil {
		log.Fatalf("[self-update] 启动新程序失败: %v", err)
	}
	log.Printf("[self-update] 已替换并启动新程序，自更新进程退出")
	os.Exit(0)
}

// replaceExe 把 newExe 复制到位覆盖 oldExe：先 rename 旧 exe 为 .bak（旧进程退出后
// 文件锁释放才能成功，循环重试），再 copy 新 exe。任一环节失败则回滚。
func replaceExe(oldExe, newExe string) error {
	backup := oldExe + ".bak"
	_ = os.Remove(backup)
	renamed := false
	for i := 0; i < 120; i++ { // 最多等 60s
		if err := os.Rename(oldExe, backup); err == nil {
			renamed = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !renamed {
		return fmt.Errorf("无法移走旧程序（可能仍被占用）")
	}
	if err := copyFile(newExe, oldExe); err != nil {
		_ = os.Rename(backup, oldExe) // 回滚
		return fmt.Errorf("写入新程序失败: %w", err)
	}
	return nil
}
