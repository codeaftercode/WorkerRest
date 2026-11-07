package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"workerrest/internal/settings"
	"workerrest/internal/sound"
)

// ---------- 音乐副本的文件名 ----------
// isLegacyMusicName / nextMusicIndex / freeMusicPath 已下沉到 internal/sound（纯文件名规则）。

// rewriteMusicPath 把配置里所有指向 oldPath 的音乐条目改到 newPath
// （大小写不敏感：Windows 路径本就不区分，配置里存的是用户选文件时的原样大小写）。
func rewriteMusicPath(cfg map[string]any, oldPath, newPath string) int {
	snd, ok := cfg["sound"].(map[string]any)
	if !ok {
		return 0
	}
	list, ok := snd["music"].([]any)
	if !ok {
		return 0
	}
	n := 0
	for _, it := range list {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		if s, ok := m["file"].(string); ok && s != "" &&
			strings.EqualFold(filepath.Clean(s), filepath.Clean(oldPath)) {
			m["file"] = newPath
			n++
		}
	}
	return n
}

// migrateMusicFileNames 把旧版的随机长名副本改成 music-<序号>.<ext>，并同步配置里的绝对路径。
// 返回改了几条（>0 表示调用方需要落盘）。幂等：改完就不再匹配旧命名，下次启动不会重做。
// 只在启动阶段调用：那时调度器与监视器都还没起，不存在并发读配置。
func (a *App) migrateMusicFileNames() int {
	dir := a.customSoundDir()
	if dir == "" {
		return 0
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	cfg := a.cfg.Raw()
	changed := 0
	for _, e := range ents {
		if e.IsDir() || !sound.IsLegacyMusicName(e.Name()) {
			continue
		}
		oldPath := filepath.Join(dir, e.Name())
		ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(e.Name()), "."))
		dest := sound.FreeMusicPath(dir, ext)
		if dest == "" {
			continue
		}
		if err := os.Rename(oldPath, dest); err != nil {
			log.Printf("[sound] 音乐副本改名失败 %s: %v", oldPath, err)
			continue
		}
		rewriteMusicPath(cfg, oldPath, dest)
		changed++
		log.Printf("[sound] 音乐副本改为可读名：%s → %s", e.Name(), filepath.Base(dest))
	}
	return changed
}

// ---------- 导入 / 删除（供设置页的音乐列表用） ----------

// importMusicSound 把用户选中的音频复制进音频目录作为一条音乐（每条一个独立副本，
// 与单曲循环的自定义提示音互不影响：music-<序号>.<ext>）。
// 返回条目与空错误串；格式不支持 / 文件不可读 / 超限 / 复制失败返回 nil 与原因。
func (a *App) importMusicSound(src string) (map[string]any, string) {
	src = strings.TrimSpace(src)
	if !sound.Supported(src) {
		return nil, fmt.Sprintf("不支持的音频格式（可用：%s）", strings.Join(sound.Exts, " / "))
	}
	st, err := os.Stat(src)
	if err != nil || !st.Mode().IsRegular() {
		return nil, "文件不可读"
	}
	if st.Size() > sound.MaxMusicBytes {
		return nil, "文件超过 20 MB"
	}
	dir := a.customSoundDir()
	if dir == "" {
		return nil, "无法确定音频目录"
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, "创建音频目录失败"
	}
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(src), "."))
	// 文件名用递增序号（music-1.mp3、music-2.mp3）：用户在目录里能对上号，
	// 也不用担心随机长串被误认成别的东西。
	dest := sound.FreeMusicPath(dir, ext)
	if dest == "" {
		return nil, "音频目录里没有可用的文件序号"
	}
	if err := copyFile(src, dest); err != nil {
		log.Printf("[sound] 复制音乐失败: %v", err)
		return nil, "复制失败"
	}
	name := strings.TrimSuffix(filepath.Base(src), filepath.Ext(src))
	return map[string]any{"file": dest, "name": name}, ""
}

// deleteMusicSound 删除音乐条目对应的副本文件：**只删音频目录里直接躺着的那一层**，
// 子目录、别处的文件、内置条目（相对名）一律不碰。返回是否真的删掉了。
func (a *App) deleteMusicSound(file string) bool {
	file = strings.TrimSpace(file)
	if file == "" || !filepath.IsAbs(file) {
		return false
	}
	dir := a.customSoundDir()
	if dir == "" {
		return false
	}
	abs := filepath.Clean(file)
	if !sound.SameDir(filepath.Dir(abs), dir) {
		return false
	}
	if err := os.Remove(abs); err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[sound] 删除音乐失败: %v", err)
		}
		return false
	}
	return true
}

// copyFile 复用 bindings_settings.go 的同名实现（它已经会按需创建父目录）

// ---------- 本地音频资源路由 ----------

// soundMiddleware 拦两类音频请求，其余交给默认的资源服务：
//   - /sounds/xxx.wav：内置提示音（随包 sounds/ 目录，磁盘交付，见 serveBuiltinSound）；
//   - /__sound/?p=…：用户导入的音乐副本（白名单校验，见 allowedSoundPath）。
//
// 挂法见 main.go 的 Assets.Middleware —— 它注入在 Wails 内部中间件之前，所以这里
// 一定能先看到请求。
func (a *App) soundMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, builtinSoundURLDir):
			a.serveBuiltinSound(w, r)
		case strings.HasPrefix(r.URL.Path, soundURLPrefix):
			a.serveSoundFile(w, r)
		default:
			next.ServeHTTP(w, r)
		}
	})
}

// serveBuiltinSound 交付一个内置提示音（随包 sounds/ 目录）。
// 白名单：文件名只认路径最后一段 + 音频扩展名 + 文件确实在随包目录里，其余 404 ——
// 它和 /__sound/ 一样是本机回环上的 HTTP 端点，不做任意文件读取服务。
func (a *App) serveBuiltinSound(w http.ResponseWriter, r *http.Request) {
	// net/http 的 URL.Path 已是解码后的路径（%2F 会还原成 /），再取一次 Base 兜住穿越
	name := filepath.Base(strings.TrimPrefix(r.URL.Path, builtinSoundURLDir))
	if !sound.Supported(name) {
		http.NotFound(w, r)
		return
	}
	abs := filepath.Join(sound.BuiltinDir(), name)
	f, err := os.Open(abs)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() == 0 {
		http.NotFound(w, r)
		return
	}
	if ct := soundMimeByExt[strings.ToLower(strings.TrimPrefix(filepath.Ext(abs), "."))]; ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, name, st.ModTime(), f)
}

// allowedSoundPath 白名单校验：只放行“音频目录下的文件”与“当前配置音乐列表里点名的
// 绝对路径”，其余一律拒绝。返回规范化后的绝对路径。
func (a *App) allowedSoundPath(p string) (string, bool) {
	p = strings.TrimSpace(p)
	if p == "" || !filepath.IsAbs(p) || !sound.Supported(p) {
		return "", false
	}
	abs := filepath.Clean(p)
	if dir := a.customSoundDir(); dir != "" {
		// 目录内任意层级都算（用户可能自己把副本整理进子目录）
		if strings.HasPrefix(strings.ToLower(abs), strings.ToLower(dir+string(os.PathSeparator))) {
			return abs, true
		}
	}
	// 老配置里的音乐条目可能指向用户原始位置：只认配置点名的那几个
	if cfgHasMusicFile(a.CfgSnapshot(), abs) {
		return abs, true
	}
	return "", false
}

// cfgHasMusicFile 配置的音乐列表里是否点名叫了这个绝对路径
func cfgHasMusicFile(s *settings.Settings, abs string) bool {
	if s == nil {
		return false
	}
	for _, m := range s.SoundCfg().Music {
		if !filepath.IsAbs(m.File) {
			continue
		}
		if strings.EqualFold(filepath.Clean(m.File), abs) {
			return true
		}
	}
	return false
}

// serveSoundFile 交付一个本地音频文件（见 allowedSoundPath 的白名单）
func (a *App) serveSoundFile(w http.ResponseWriter, r *http.Request) {
	abs, ok := a.allowedSoundPath(r.URL.Query().Get("p"))
	if !ok {
		log.Printf("[sound] 拒绝资源请求 path=%q（不在白名单内）", r.URL.Query().Get("p"))
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	f, err := os.Open(abs)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() > sound.MaxMusicBytes {
		http.NotFound(w, r)
		return
	}
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(abs), "."))
	if ct := soundMimeByExt[ext]; ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	// 试听时用户可能刚替换过同名副本，不做协商缓存
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, filepath.Base(abs), st.ModTime(), f)
}
