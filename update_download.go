package main

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"workerrest/internal/platform"
	"workerrest/internal/urltls"
)

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

// StartUpdate 执行自动更新：下载 zip → 解压 → 增量合并配置 → 暂存新 exe →
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
	urls := withFallbackPackage(statusURLs(st))
	if len(urls) == 0 {
		return map[string]any{"status": "error", "message": "缺少下载地址"}
	}
	// 校验值先取出并规范化：状态可能是上个版本持久化下来的（那时还没有 sha256），
	// 这种「没有哈希的待更新状态」一律拒绝，绝不降级成「只靠 TLS」。
	wantSHA, err := parseSHA256(statusString(st, "sha256"))
	if err != nil {
		return map[string]any{"status": "error", "message": "更新清单缺少有效的 sha256 校验值，已拒绝更新"}
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
	if err := downloadPackage(urls, archive, wantSHA); err != nil {
		return map[string]any{"status": "error", "message": "下载更新包失败：" + err.Error()}
	}
	if err := extractZip(archive, updateDir); err != nil {
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
	cmd.SysProcAttr = platform.HiddenSysProcAttr()
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

// downloadFile 下载 url 到 dest（流式写盘，避免大文件占内存）。
//
// 三道闸：协议白名单（含重定向）、Content-Length 预检、实际写入字节上限 ——
// 最后一道必须有，服务端可以不发 Content-Length 或用 chunked 慢慢灌。
func downloadFile(url, dest string) error {
	return downloadFileLimited(url, dest, maxPackageBytes)
}

// downloadFileLimited 是 downloadFile 的可注入上限版本（单测用小上限验证截断逻辑，
// 不必真造 64 MiB 的响应）。
func downloadFileLimited(url, dest string, limit int64) error {
	if err := urltls.CheckUpdateURL(url); err != nil {
		return err
	}
	client := newUpdateClient(10 * time.Minute)
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
	if res.ContentLength > limit {
		return fmt.Errorf("更新包体积 %d 字节，超过上限 %d", res.ContentLength, limit)
	}
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	n, err := io.Copy(out, io.LimitReader(res.Body, limit+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(dest)
		return err
	}
	if n > limit {
		_ = os.Remove(dest)
		return fmt.Errorf("更新包超过 %d 字节上限，已中断下载", limit)
	}
	return nil
}

// statusString 从更新状态 map 里取一个字符串字段（缺失/非字符串都返回空串）。
func statusString(st map[string]any, key string) string {
	if s, ok := st[key].(string); ok {
		return s
	}
	return ""
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

// normalizePackageURL 把 version.json url 数组里的一项补全为完整的 zip 包地址：
// 已是以 .zip（忽略大小写）结尾的直接使用；以 / 结尾的只拼 rest.zip；其余拼 /rest.zip。
func normalizePackageURL(raw string) string {
	u := strings.TrimSpace(raw)
	if u == "" {
		return ""
	}
	if strings.HasSuffix(strings.ToLower(u), ".zip") {
		return u
	}
	if strings.HasSuffix(u, "/") {
		return u + packageName
	}
	return u + "/" + packageName
}

// downloadPackage 按 urls 顺序依次补全地址、下载并**校验 sha256**，任一地址通过即返回；
// 全部失败返回最后一次的错误（空数组 / 全空白项同样报错）。
//
// 哈希不符时删除已下载的文件再换下一个镜像 —— 换镜像本身是安全的，因为每个镜像都要过
// 同一道哈希；而「哈希不符但继续装」绝对不允许。
func downloadPackage(urls []string, dest string, wantSHA string) error {
	want, err := parseSHA256(wantSHA)
	if err != nil {
		return fmt.Errorf("拒绝下载：%w", err)
	}
	var lastErr error
	for _, raw := range urls {
		u := normalizePackageURL(raw)
		if u == "" {
			continue
		}
		if err := urltls.CheckUpdateURL(u); err != nil {
			log.Printf("[update] 跳过不合规下载地址 %s: %v", u, err)
			lastErr = err
			continue
		}
		log.Printf("[update] 尝试下载更新包: %s", u)
		if err := downloadFile(u, dest); err != nil {
			log.Printf("[update] 下载更新包失败: %v", err)
			lastErr = err
			continue
		}
		if err := verifySHA256(dest, want); err != nil {
			log.Printf("[update] 更新包校验失败，丢弃后换下一镜像: %v", err)
			_ = os.Remove(dest)
			lastErr = err
			continue
		}
		log.Printf("[update] 更新包校验通过: sha256=%s…", head8(want))
		return nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("没有可用的下载地址")
	}
	return lastErr
}

// extractZip 把 zip 包解压到 dest（保留目录结构）。
// 用标准库 archive/zip：更新包改 zip 后不再依赖第三方 7z reader。
//
// 除目录穿越外还挡 zip bomb：条目数与解压总量都有上限，且落盘时再用 LimitReader
// 兜一层 —— 条目声明的 UncompressedSize 可以造假，实际写出来的字节数才是真的。
func extractZip(archive, dest string) error {
	return extractZipLimited(archive, dest, maxExtractTotalBytes, maxExtractEntries)
}

// extractZipLimited 是 extractZip 的可注入上限版本（单测用小上限验证截断逻辑）。
func extractZipLimited(archive, dest string, maxTotal int64, maxEntries int) error {
	r, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer r.Close()
	if len(r.File) > maxEntries {
		return fmt.Errorf("更新包条目过多（%d > %d）", len(r.File), maxEntries)
	}
	var written int64
	for _, f := range r.File {
		// 包内路径统一按 / 归一（Windows 压出来的 zip 可能带反斜杠）
		name := strings.ReplaceAll(f.Name, "\\", "/")
		target := filepath.Join(dest, filepath.FromSlash(name))
		// 防目录穿越：解压出来的路径必须仍在 dest 内
		if rel, err := filepath.Rel(dest, target); err != nil || rel == ".." ||
			strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return fmt.Errorf("更新包含非法路径：%s", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if f.UncompressedSize64 > uint64(maxTotal) {
			return fmt.Errorf("更新包含异常大的条目：%s（%d 字节）", f.Name, f.UncompressedSize64)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		mode := f.Mode().Perm()
		if mode == 0 {
			mode = 0o644
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
		if err != nil {
			rc.Close()
			return err
		}
		// +1 是为了「多读一个字节」就能判定越界，而不是静默截断。
		n, err := io.Copy(out, io.LimitReader(rc, maxTotal-written+1))
		out.Close()
		rc.Close()
		if err != nil {
			return err
		}
		if written+n > maxTotal {
			return fmt.Errorf("更新包解压后超过 %d 字节上限", maxTotal)
		}
		written += n
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
// 单文件大小也设上限：配置补丁正常只有几 KB，超过 1 MiB 的「配置」只会是畸形包。
func mergeConfigFromDir(dir string, raw map[string]any) error {
	return filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".json") {
			return nil
		}
		if info, err := d.Info(); err == nil && info.Size() > 1<<20 {
			log.Printf("[update] 跳过过大的配置文件 %s（%d 字节）", path, info.Size())
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
	cmd.SysProcAttr = platform.HiddenSysProcAttr()
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
