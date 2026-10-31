package main

import (
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// 提示音：历史源码/sound.ts 的对应物。
//
// 为什么要有「播放宿主窗口」：主进程自己放不了声音。上一版开一个 1×1 的隐藏
// BrowserWindow 承载 <audio>；这里同理 —— Wails 的隐藏窗口加载 sound.html，
// 播放时向它 ExecJS 一句 `new Audio(url).play()`。
//
// 音频分两类，来源与交付方式都不同：
//
//	内置提示音（四个 wav，short/long × start/end）—— 相对文件名。
//	  不再 embed 进二进制：打包时随包放在 exe 旁的 sounds/（make_portable.py 预置），
//	  开发态是模块根的 sounds/。播放 URL 是 /sounds/xxx.wav，由 soundMiddleware 从
//	  随包目录磁盘交付（见 serveBuiltinSound）。
//	用户导入的音乐 —— 绝对路径，副本放在**配置目录旁的 sounds/**（见 customSoundDir）。
//	  这些文件与随包目录无关，走本文件自带的一条只读路由 /__sound/?p=…（见
//	  soundMiddleware）。
//
// 那条路由**只服务白名单内的文件**（音频目录下的副本，或当前配置音乐列表里点名的绝对
// 路径），其余一律 403。它是本机回环上的一个 HTTP 端点，绝不做任意文件读取服务。
//
// 与上一版 逐条对齐的三处：
//
// 一、选曲口径「单曲循环与启停无关」：single 模式播 singleId 指向的那一条（条目被停用
// 也照样播，被删 / 文件不可用 = 静音）；random / sequence 只在**启用且文件存在**的条目
// 里挑。全部不可用时不回退 —— 用户把列表停用 / 删空就是刻意静音。
//
// 二、顺序播放的游标跨播放持续递增（不是每次从头开始），所以连续两次播放取的是列表里
// 相邻的两条。
//
// 三、导入副本的删除只认「音频目录里**直接**躺着的那一层」，子目录与别处的文件一律不碰。

const (
	// maxMusicBytes 导入上限 20 MB：每次播放都要读盘，过大的文件会拖慢提醒弹出
	maxMusicBytes = 20 * 1024 * 1024

	// soundURLPrefix 自定义音资源路由前缀（见 soundMiddleware）；带 query 参数 p
	soundURLPrefix = "/__sound/"

	// builtinSoundRelDir 内置提示音的随包目录（相对 resourcesRoot：打包态 exe 旁、
	// 开发态模块根）。文件本身**不再 embed**，由打包脚本预置进便携包。
	builtinSoundRelDir = "sounds"
	// builtinSoundURLDir 内置提示音对外的 URL 前缀（soundMiddleware 按随包目录磁盘交付）
	builtinSoundURLDir = "/sounds/"

	// soundHostWindowName 播放宿主窗口名（Wails 按名字交付 / 查找窗口）
	soundHostWindowName = "sound"
)

// customSoundExts 允许的音频扩展名（与 sound.ts 的 CUSTOM_SOUND_EXTS 逐字一致：
// Chromium 能解码的常见格式）
var customSoundExts = []string{"wav", "mp3", "ogg", "oga", "m4a", "aac", "flac", "opus", "webm"}

// soundMimeByExt 资源路由响应的 Content-Type（Chromium 按扩展名基本也能猜对，
// 显式给出是为了不让某些格式落到 application/octet-stream 上）
var soundMimeByExt = map[string]string{
	"wav": "audio/wav", "mp3": "audio/mpeg", "ogg": "audio/ogg", "oga": "audio/ogg",
	"m4a": "audio/mp4", "aac": "audio/aac", "flac": "audio/flac",
	"opus": "audio/ogg", "webm": "audio/webm",
}

// ---------- 内置音目录 ----------

// builtinSoundDirFn 内置提示音随包目录的注入点：App 启动时注入
// <resourcesRoot>/sounds（打包态 exe 旁、开发态模块根）；单测可指到临时目录。
// 与 settings.go 的 resourcesRootHintFn 同一套路 —— 避免包级函数反向依赖 App。
var builtinSoundDirFn func() string

// builtinSoundDir 内置提示音目录；未注入时兜底相对路径（单测的 cwd 就是模块根）
func builtinSoundDir() string {
	if builtinSoundDirFn != nil {
		return builtinSoundDirFn()
	}
	return builtinSoundRelDir
}

// ---------- 配置视图 ----------

// SoundMusicItem 音乐列表的一条（内置提示音与用户导入的曲子同构：内置的 file 是
// 相对文件名，导入的是绝对路径）
type SoundMusicItem struct {
	ID      string
	File    string
	Name    string
	Enabled bool
}

// SoundCfg 声音配置的类型化视图（字段名与 types.ts 的 SoundConfig 对齐）
type SoundCfg struct {
	Enabled  bool
	Volume   float64
	Mode     string // single | random | sequence
	SingleID string
	Music    []SoundMusicItem
}

// SoundCfg 取出声音配置。缺字段按「与默认配置同口径」兜底：启用、音量 0.6、单曲循环。
func (s *Settings) SoundCfg() SoundCfg {
	return soundCfgOf(s.Map("sound"))
}

func soundCfgOf(m map[string]any) SoundCfg {
	cfg := SoundCfg{Enabled: true, Volume: 0.6, Mode: "single", Music: []SoundMusicItem{}}
	if m == nil {
		return cfg
	}
	cfg.Enabled = mapFlag(m, "enabled", true)
	if v, ok := toNumber(m["volume"]); ok {
		cfg.Volume = clamp01(v)
	}
	if mode := mapStr(m, "mode", "single"); mode == "random" || mode == "sequence" {
		cfg.Mode = mode
	}
	cfg.SingleID = mapStr(m, "singleId", "")
	for _, it := range anySlice(m["music"]) {
		mi, ok := it.(map[string]any)
		if !ok {
			continue
		}
		file := mapStr(mi, "file", "")
		if strings.TrimSpace(file) == "" {
			continue
		}
		cfg.Music = append(cfg.Music, SoundMusicItem{
			ID:      mapStr(mi, "id", ""),
			File:    file,
			Name:    mapStr(mi, "name", ""),
			Enabled: mi["enabled"] != false,
		})
	}
	return cfg
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// ---------- 选曲（纯逻辑） ----------

// soundExtSupported 扩展名是否在白名单里（大小写不敏感；无扩展名 = 不支持）
func soundExtSupported(name string) bool {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))
	if ext == "" {
		return false
	}
	for _, e := range customSoundExts {
		if e == ext {
			return true
		}
	}
	return false
}

// builtinSoundExists 内置提示音在不在随包 sounds/ 目录里（磁盘判定）。
// 文件名只取最后一段再拼进目录，杜绝 ../ 这类路径穿越；空文件视为不存在
// （与 soundFileUsable 同口径 —— 播一个 0 字节的 wav 只会静默失败）。
func builtinSoundExists(name string) bool {
	name = filepath.Base(name)
	if !soundExtSupported(name) {
		return false
	}
	st, err := os.Stat(filepath.Join(builtinSoundDir(), name))
	return err == nil && st.Mode().IsRegular() && st.Size() > 0
}

// soundFileUsable 这个文件现在能不能播：内置看随包资源，自定义看磁盘上是不是常规文件
// 且在大小上限内。**空文件也算不可用** —— 播一个 0 字节的 wav 只会静默失败。
func soundFileUsable(file string) bool {
	if !filepath.IsAbs(file) {
		return builtinSoundExists(file)
	}
	st, err := os.Stat(file)
	if err != nil || !st.Mode().IsRegular() {
		return false
	}
	return st.Size() > 0 && st.Size() <= maxMusicBytes
}

// resolveSoundFile 单曲循环要播的文件：singleId 指向的那一条。条目不存在 / 没有 file /
// 文件不可用都返回空串 = 静音（与 sound.ts 的 resolveSoundFile 同口径）。
// 注意「条目是否被停用」与这里无关 —— 停用只影响 random / sequence 的选池。
// singleId 为空 = 用户没选过单曲，按契约静音（默认值由 normalizeSound 在首次种入时补上）。
func resolveSoundFile(cfg SoundCfg) string {
	for _, m := range cfg.Music {
		if m.ID != cfg.SingleID || cfg.SingleID == "" {
			continue
		}
		if strings.TrimSpace(m.File) == "" {
			return ""
		}
		if !soundFileUsable(m.File) {
			return ""
		}
		return m.File
	}
	return ""
}

// enabledMusicFiles 音乐列表里「启用 + 文件仍可用」的路径，按列表顺序。
// random / sequence 只从这里挑；顺序游标也按这个顺序走。
func enabledMusicFiles(cfg SoundCfg) []string {
	out := make([]string, 0, len(cfg.Music))
	for _, m := range cfg.Music {
		if !m.Enabled || strings.TrimSpace(m.File) == "" {
			continue
		}
		if !soundFileUsable(m.File) {
			continue
		}
		out = append(out, m.File)
	}
	return out
}

// soundPickNext 按配置挑下一首要播的音频，返回空串表示这次刻意静音。游标（顺序播放）
// 会随手推进 —— 它记在 App 上，跨播放持续递增（与 sound.ts 的模块级 seqCursor 同义）。
//
// rnd 是随机源注入点：生产用 math/rand 全局函数（自带锁、并发安全），单测传一个定值
// 函数就能把 random 分支变成可断言的。
func (a *App) soundPickNext(cfg SoundCfg, rnd func(n int) int) string {
	if !cfg.Enabled || cfg.Volume <= 0 {
		return ""
	}
	if cfg.Mode == "random" || cfg.Mode == "sequence" {
		files := enabledMusicFiles(cfg)
		if len(files) == 0 {
			return "" // 全部停用 / 删空：用户的选择，静音
		}
		if cfg.Mode == "random" {
			return files[rnd(len(files))]
		}
		a.mu.Lock()
		cur := a.soundCursor
		a.soundCursor++
		a.mu.Unlock()
		return files[cur%len(files)]
	}
	return resolveSoundFile(cfg)
}

// ---------- 路径 ----------

// customSoundDir 用户导入音频的副本目录。
//
// 上一版是 userDataPath('sounds', 'sounds', false)：非便携 = <userData>/sounds，
// 便携 = <运行目录>/sounds/sounds（category 与 rel 同名，结果多一层）。
// Go 版**统一成「配置文件所在目录旁的 sounds/」**：也就是 <运行目录>/config/sounds
// （配置在 config/settings.json），数据与程序同目录。
// 这处差异的可见影响只有「便携老用户的旧副本删不掉」（播放不受影响 —— 配置里存的是
// 绝对路径），已记进 README 的已知差异。
func (a *App) customSoundDir() string {
	s := a.cfgSnapshot()
	if s != nil {
		if p := s.Path(); p != "" {
			return filepath.Join(filepath.Dir(p), "sounds")
		}
	}
	// 还没有配置文件：按候选路径（= 唯一的 <运行目录>/config/settings.json）推导
	cands := settingsCandidates(a.resourcesRoot())
	if len(cands) == 0 {
		return ""
	}
	return filepath.Join(filepath.Dir(cands[len(cands)-1]), "sounds")
}

// builtinSoundURL 内置提示音的播放 URL（文件名只取最后一段，杜绝 ../ 这类拼接）
func builtinSoundURL(file string) string {
	return builtinSoundURLDir + url.PathEscape(filepath.Base(file))
}

// sameDir 两个目录是不是同一个（Windows 不区分大小写）
func sameDir(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

// ---------- 音乐副本的文件名 ----------

// isLegacyMusicName 旧版导入留下的副本名：music-<8-4-4-4-12 位十六进制>.<ext>。
// 新版统一成 music-<序号>.<ext>，用户打开目录能看懂哪个是哪个。
func isLegacyMusicName(name string) bool {
	ext := filepath.Ext(name)
	if ext == "" {
		return false
	}
	base := strings.TrimSuffix(name, ext)
	if !strings.HasPrefix(base, "music-") {
		return false
	}
	key := base[len("music-"):]
	if len(key) != 36 {
		return false
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
				return false
			}
		}
	}
	return true
}

// nextMusicIndex 目录里 music-<N>.<ext> 的最大序号 +1（一条都没有就从 1 开始）
func nextMusicIndex(dir string) int {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return 1
	}
	max := 0
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		base := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		if !strings.HasPrefix(base, "music-") {
			continue
		}
		if n, err := strconv.Atoi(strings.TrimPrefix(base, "music-")); err == nil && n > max {
			max = n
		}
	}
	return max + 1
}

// freeMusicPath 在 dir 里找一个还没被占用的 music-<序号>.<ext>。
// 循环是为了防同名（用户手动放了一个同名文件、或两个导入同时进来）。
func freeMusicPath(dir, ext string) string {
	for i := 0; i < 1000; i++ {
		p := filepath.Join(dir, fmt.Sprintf("music-%d.%s", nextMusicIndex(dir), ext))
		if _, err := os.Stat(p); os.IsNotExist(err) {
			return p
		}
	}
	return ""
}

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
		if e.IsDir() || !isLegacyMusicName(e.Name()) {
			continue
		}
		oldPath := filepath.Join(dir, e.Name())
		ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(e.Name()), "."))
		dest := freeMusicPath(dir, ext)
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
	if !soundExtSupported(src) {
		return nil, fmt.Sprintf("不支持的音频格式（可用：%s）", strings.Join(customSoundExts, " / "))
	}
	st, err := os.Stat(src)
	if err != nil || !st.Mode().IsRegular() {
		return nil, "文件不可读"
	}
	if st.Size() > maxMusicBytes {
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
	dest := freeMusicPath(dir, ext)
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
	if !sameDir(filepath.Dir(abs), dir) {
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
	if !soundExtSupported(name) {
		http.NotFound(w, r)
		return
	}
	abs := filepath.Join(builtinSoundDir(), name)
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

// allowedSoundPath 白名单校验：只放行「音频目录下的文件」与「当前配置音乐列表里点名的
// 绝对路径」，其余一律拒绝。返回规范化后的绝对路径。
func (a *App) allowedSoundPath(p string) (string, bool) {
	p = strings.TrimSpace(p)
	if p == "" || !filepath.IsAbs(p) || !soundExtSupported(p) {
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
	if cfgHasMusicFile(a.cfgSnapshot(), abs) {
		return abs, true
	}
	return "", false
}

// cfgHasMusicFile 配置的音乐列表里是否点名叫了这个绝对路径
func cfgHasMusicFile(s *Settings, abs string) bool {
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
	if err != nil || !st.Mode().IsRegular() || st.Size() > maxMusicBytes {
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

// ---------- 播放宿主 ----------

// initSoundHost 建播放宿主窗口（隐藏 1×1，加载 sound.html）。
// 无头构建下没有原生窗口 —— 播放会退化成「只记日志」（见 playSoundFile），
// 所以无头冒烟仍能断言「该播哪一首」这条链路。
func (a *App) initSoundHost() {
	if serverMode || a.app == nil {
		log.Printf("[sound] server 模式无原生窗口，跳过播放宿主创建")
		return
	}
	a.mu.Lock()
	exists := a.wins[soundHostWindowName] != nil
	a.mu.Unlock()
	if exists {
		return
	}
	win := a.app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             soundHostWindowName,
		Title:            "sound",
		Width:            1,
		Height:           1,
		Hidden:           true,
		Frameless:        true,
		DisableResize:    true,
		URL:              "/sound.html",
		BackgroundColour: application.NewRGBA(0, 0, 0, 0),
		Windows: application.WindowsWindow{
			DisableFramelessWindowDecorations: true,
			HiddenOnTaskbar:                   true,
		},
	})
	// 页面加载完（WebView 运行时就绪）只用来留一条日志 —— 播放**不**依赖它：
	// webbview 未就绪时 ExecJS 会把脚本排队、等加载完再执行（Wails 自带语义），
	// 所以自己加一道「没就绪就不发」反而会把播放整段丢掉。
	win.OnWindowEvent(events.Common.WindowRuntimeReady, func(*application.WindowEvent) {
		log.Printf("[sound] 播放宿主就绪（sound.html 已加载）")
	})
	win.OnWindowEvent(events.Common.WindowClosing, func(*application.WindowEvent) {
		a.mu.Lock()
		// 只删自己：destroySoundHost 之后可能重建宿主窗，迟到一步的旧回调按名字 delete
		// 会把新宿主的引用抹掉（与提前提示窗同一个坑，见 dropWindowRef）
		dropWindowRef(a.wins, soundHostWindowName, win)
		a.mu.Unlock()
	})
	a.mu.Lock()
	a.wins[soundHostWindowName] = win
	a.mu.Unlock()
	log.Printf("[sound] 播放宿主窗口已创建（隐藏 1×1，加载 sound.html）")
}

// destroySoundHost 收掉播放宿主（退出清理）
func (a *App) destroySoundHost() {
	a.mu.Lock()
	w := a.wins[soundHostWindowName]
	delete(a.wins, soundHostWindowName)
	a.mu.Unlock()
	if w != nil {
		w.Close()
	}
}

// playConfiguredSound 播放当前生效的提示音（休息开始 / 结束各一次）。
// 静音是「配置的选择」而不是错误，所以静音路径只记日志、不报错。
func (a *App) playConfiguredSound() {
	cfg := a.cfgSnapshot().SoundCfg()
	file := a.soundPickNext(cfg, rand.Intn)
	if file == "" {
		log.Printf("[sound] 静音（enabled=%v volume=%.2f mode=%s）", cfg.Enabled, cfg.Volume, cfg.Mode)
		return
	}
	log.Printf("[sound] 播放 %s（volume=%.2f mode=%s）", file, cfg.Volume, cfg.Mode)
	a.playSoundFile(file, cfg.Volume)
}

// playSoundFile 让宿主窗口播一个文件。
//
// 不需要等宿主「就绪」：WebView 还没加载完时 ExecJS 会把脚本排队、加载完自动执行
// （见 WebviewWindow.ExecJS 的 pendingJS 分支）。宿主窗口在启动时就建好了，
// 头几次播放通常远晚于页面加载，这条路径不会丢。
func (a *App) playSoundFile(file string, volume float64) {
	url := a.SoundFileURL(file)
	if url == "" {
		log.Printf("[sound] 无法播放 %s（文件不可用或格式不支持）", file)
		return
	}
	if serverMode || a.app == nil {
		log.Printf("[sound] server 模式不发声（本应播放 %s volume=%.2f）", url, volume)
		return
	}
	a.execPlaySound(url, volume)
}

// execPlaySound 把播放指令发给承载 <audio> 的窗口。每次新建一个 Audio 元素 —— 允许重叠
// 播放（休息结束音紧接下一段开始时不该互相打断），与 sound.ts 的 playSound 同口径。
func (a *App) execPlaySound(url string, volume float64) {
	w := a.audioHostWindow()
	if w == nil {
		return
	}
	// 纯 JS，不带注释；url 用 strconv.Quote 转义（与 JS 字符串字面量兼容）
	script := fmt.Sprintf(
		"(()=>{const a=new Audio(%s);a.volume=%s;a.play().catch(()=>{})})()",
		strconv.Quote(url), strconv.FormatFloat(volume, 'f', -1, 64))
	w.ExecJS(script)
}

// audioHostWindow 挑一个承载 <audio> 的窗口：优先主设置窗口（main.go 里随应用创建、
// 「关闭」只是 Hide 不销毁，全程存活、已加载且用户交互过），退回专用宿主窗。
// 历史上只用隐藏宿主窗，但那条链从未被验证能真发声（隐藏窗可能不加载 + WebView2 默认
// 拦无手势有声自动播放）；改走必然存活的设置窗更稳，autoplay 已在 main.go 全局放开。
// 二者皆无返回 nil（server 模式在 playSoundFile 已先行短路，不会走到这里）。
func (a *App) audioHostWindow() application.Window {
	if a.app != nil {
		if w := a.settingsWindow(); w != nil {
			return w
		}
	}
	a.mu.Lock()
	hw := a.wins[soundHostWindowName]
	a.mu.Unlock()
	if hw == nil {
		return nil // 别把「类型化 nil」当接口返回，否则上层 w==nil 判不出
	}
	return hw
}

// ---------- 绑定给渲染层的接口 ----------

// SoundFileURL 把音频文件换算成可播放 URL（设置页试听用 <audio>、播放宿主用 Audio）。
// 内置音（相对文件名）→ 资源服务的 /sounds/xxx.wav；自定义音（绝对路径）→ 本进程的
// /__sound/?p=…（白名单见 allowedSoundPath）。**文件不可用返回空串** ——
// 渲染层正是用 `if (!url)` 判「找不到该音频文件」的。
func (a *App) SoundFileURL(file string) string {
	file = strings.TrimSpace(file)
	if file == "" {
		return ""
	}
	if !filepath.IsAbs(file) {
		if !builtinSoundExists(file) {
			return ""
		}
		return builtinSoundURL(file)
	}
	abs := filepath.Clean(file)
	if !soundFileUsable(abs) {
		return ""
	}
	return soundURLPrefix + "?p=" + url.QueryEscape(abs)
}

// DeleteMusicSound 删除音乐条目对应的副本文件（只删音频目录里那一层，见 deleteMusicSound）
func (a *App) DeleteMusicSound(file string) bool {
	return a.deleteMusicSound(file)
}
