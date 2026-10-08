package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

// 提示音这一层的回归。
//
// 分工：
//   - 这里拦「选曲规则写错」：单曲与启停无关、随机 / 顺序的选池、游标推进、
//     静音语义、资源路由的白名单、导入副本的删除边界。
//   - scripts/smoke_server.py 拦「接进绑定后还对不对」：SoundFileURL 的实际返回值、
//     休息开始 / 结束真的播了、无头模式下退化成日志。
//   - 真发声只有 GUI 下才验得到（宿主窗口 + WebView2 解码），那一段在
//     scripts/probe_gui_boot.py 的日志断言里。

// ---------- 脚手架 ----------

// newSoundApp 造一个「配置落在临时目录」的最小 App：音频目录因此是 <临时目录>/sounds
func newSoundApp(t *testing.T, cfgJSON string) (*App, string) {
	t.Helper()
	dir := t.TempDir()
	s := testSettings(t, cfgJSON)
	s.path = filepath.Join(dir, "settings.json")
	return &App{cfg: s}, dir
}

// soundCfgFromJSON 从「含外层 sound 键」的整份配置里取出声音配置视图
// （soundCfgOf 收的是 sound 对象本身，别直接喂整份配置）
func soundCfgFromJSON(t *testing.T, js string) SoundCfg {
	t.Helper()
	s := testSettings(t, js)
	if s.Map("sound") == nil {
		t.Fatalf("测试配置里没有 sound 段: %s", js)
	}
	return s.SoundCfg()
}

// writeSoundFile 造一个「看起来可播」的文件（大小与扩展名过关即可，内容不解析）
func writeSoundFile(t *testing.T, path string, size int) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}
	buf := make([]byte, size)
	for i := range buf {
		buf[i] = byte('a' + i%26)
	}
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatalf("写文件失败: %v", err)
	}
	return path
}

// soundCfgJSON 造一段「完整配置」JSON（外层带 sound 键，与真实配置同构）
func soundCfgJSON(mode, singleID string, items ...string) string {
	music := "["
	for i, it := range items {
		if i > 0 {
			music += ","
		}
		music += it
	}
	music += "]"
	return `{"sound":{"enabled":true,"volume":0.6,"mode":"` + mode + `","singleId":"` +
		singleID + `","music":` + music + `}}`
}

func musicItem(id, file, name string, enabled bool) string {
	b, _ := json.Marshal(map[string]any{"id": id, "file": file, "name": name, "enabled": enabled})
	return string(b)
}

// ---------- 扩展名与内置音 ----------

func TestSoundExtSupported(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"short_start.wav", true},
		{"a.MP3", true},
		{"x.ogg", true},
		{"x.oga", true},
		{"x.m4a", true},
		{"x.aac", true},
		{"x.flac", true},
		{"x.opus", true},
		{"x.webm", true},
		{"noext", false},
		{"readme.txt", false},
		{"hello.mp3.exe", false},
		{"", false},
	}
	for _, c := range cases {
		if got := soundExtSupported(c.name); got != c.want {
			t.Errorf("soundExtSupported(%q) = %v，期望 %v", c.name, got, c.want)
		}
	}
}

// 四个内置提示音必须真的在随包资源里 —— 它们是「默认配置的 singleId 指向的那一条」，
// 少一个就会出现「用户什么都没动，休息时却没声音」这种查不出来的问题。
func TestBuiltinSoundsPresent(t *testing.T) {
	if len(builtinMusic) != 4 {
		t.Fatalf("内置提示音应有 4 条，得到 %d", len(builtinMusic))
	}
	for _, b := range builtinMusic {
		if !builtinSoundExists(b.file) {
			t.Errorf("内置提示音 %s 不在 embed 资源里（%s/%s）", b.file, builtinSoundFSDir, b.file)
		}
		url := builtinSoundURL(b.file)
		if url != builtinSoundURLDir+b.file {
			t.Errorf("%s 的播放 URL 应是 %s，得到 %s", b.file, builtinSoundURLDir+b.file, url)
		}
	}
	if builtinSoundExists("nope.wav") {
		t.Error("不存在的内置音不该被判为可用")
	}
	// 路径穿越与非法扩展名都必须在扩展名那一关就被挡掉
	if builtinSoundExists("../settings.json") {
		t.Error("非音频扩展名不该通过")
	}
	if builtinSoundExists("../../../etc/passwd") {
		t.Error("非音频扩展名不该通过")
	}
}

// ---------- 配置视图 ----------

func TestSoundCfgOf(t *testing.T) {
	// 缺字段：按默认（启用 / 0.6 / single）
	empty := soundCfgOf(nil)
	if !empty.Enabled || empty.Volume != 0.6 || empty.Mode != "single" {
		t.Fatalf("空配置应回默认，得到 %+v", empty)
	}
	// 非法值：音量夹到 0..1，mode 只认 random / sequence
	raw := map[string]any{
		"enabled": false, "volume": 3.5, "mode": "weird", "singleId": "sm-1",
		"music": []any{
			map[string]any{"id": "sm-1", "file": "short_start.wav", "name": "双音上行", "enabled": false},
			map[string]any{"id": "", "file": ""}, // 没有 file 的条目整条丢弃
			"不是对象",
		},
	}
	cfg := soundCfgOf(raw)
	if cfg.Enabled || cfg.Volume != 1 || cfg.Mode != "single" {
		t.Fatalf("异常值应被夹紧 / 兜底，得到 %+v", cfg)
	}
	if len(cfg.Music) != 1 {
		t.Fatalf("应只留下 1 条合法音乐，得到 %d 条", len(cfg.Music))
	}
	if cfg.Music[0].ID != "sm-1" || cfg.Music[0].Enabled {
		t.Fatalf("条目字段读错：%+v", cfg.Music[0])
	}
}

// ---------- 选曲 ----------

func TestResolveSoundFileSingle(t *testing.T) {
	dir := t.TempDir()
	real := writeSoundFile(t, filepath.Join(dir, "sounds", "music-1.mp3"), 128)

	// 命中且文件可用（条目被停用也照样播 —— 单曲循环与启停无关）
	cfg := soundCfgFromJSON(t, soundCfgJSON("single", "sm-1",
		musicItem("sm-1", real, "我的歌", false)))
	if got := resolveSoundFile(cfg); got != real {
		t.Errorf("单曲循环应返回选中的那条（停用也照样播），得到 %q", got)
	}

	// 未指定 singleId
	cfg = soundCfgFromJSON(t, soundCfgJSON("single", "",
		musicItem("sm-1", real, "我的歌", true)))
	if got := resolveSoundFile(cfg); got != "" {
		t.Errorf("未指定单曲应静音，得到 %q", got)
	}

	// 条目已删（singleId 指向一个不存在的 id）
	cfg = soundCfgFromJSON(t, soundCfgJSON("single", "sm-gone",
		musicItem("sm-1", real, "我的歌", true)))
	if got := resolveSoundFile(cfg); got != "" {
		t.Errorf("条目已删应静音，得到 %q", got)
	}

	// 文件不可用（磁盘上不存在）
	cfg = soundCfgFromJSON(t, soundCfgJSON("single", "sm-1",
		musicItem("sm-1", filepath.Join(dir, "sounds", "missing.mp3"), "丢了", true)))
	if got := resolveSoundFile(cfg); got != "" {
		t.Errorf("文件不存在应静音，得到 %q", got)
	}

	// 内置音（相对文件名）也要能解析出来
	cfg = soundCfgFromJSON(t, `{"sound":{"mode":"single","singleId":"sm-b",
		"music":[{"id":"sm-b","file":"short_start.wav","name":"双音上行","enabled":true}]}}`)
	if got := resolveSoundFile(cfg); got != "short_start.wav" {
		t.Errorf("内置音应原样返回文件名，得到 %q", got)
	}
}

func TestEnabledMusicFilesSkipsDisabledAndMissing(t *testing.T) {
	dir := t.TempDir()
	ok1 := writeSoundFile(t, filepath.Join(dir, "sounds", "a.mp3"), 16)
	ok2 := writeSoundFile(t, filepath.Join(dir, "sounds", "b.mp3"), 16)
	cfg := soundCfgFromJSON(t, soundCfgJSON("sequence", "",
		musicItem("sm-1", ok1, "启用", true),
		musicItem("sm-2", ok2, "停用", false),
		musicItem("sm-3", filepath.Join(dir, "sounds", "gone.mp3"), "文件丢了", true),
		musicItem("sm-4", "", "没文件名", true),
	))
	got := enabledMusicFiles(cfg)
	if len(got) != 1 || got[0] != ok1 {
		t.Fatalf("只应留下「启用且文件存在」的那一条，得到 %v", got)
	}
}

func TestSoundPickNext(t *testing.T) {
	dir := t.TempDir()
	f1 := writeSoundFile(t, filepath.Join(dir, "sounds", "1.mp3"), 16)
	f2 := writeSoundFile(t, filepath.Join(dir, "sounds", "2.mp3"), 16)
	f3 := writeSoundFile(t, filepath.Join(dir, "sounds", "3.mp3"), 16)
	items := []string{
		musicItem("sm-1", f1, "一", true),
		musicItem("sm-2", f2, "二", true),
		musicItem("sm-3", f3, "三", true),
	}

	// 顺序：游标跨播放持续递增，连播四次按列表顺序轮一遍
	a, _ := newSoundApp(t, soundCfgJSON("sequence", "", items...))
	cfg := a.cfgSnapshot().SoundCfg()
	for i, want := range []string{f1, f2, f3, f1} {
		if got := a.soundPickNext(cfg, func(int) int { return 0 }); got != want {
			t.Fatalf("第 %d 次顺序播放应为 %s，得到 %s", i+1, want, got)
		}
	}

	// 随机：交给注入的随机源决定下标（游标不动）
	a2, _ := newSoundApp(t, soundCfgJSON("random", "", items...))
	cfg2 := a2.cfgSnapshot().SoundCfg()
	if got := a2.soundPickNext(cfg2, func(n int) int { return n - 1 }); got != f3 {
		t.Fatalf("随机分支应听注入的随机源（取最后一条），得到 %s", got)
	}
	if a2.soundCursor != 0 {
		t.Fatalf("随机分支不该推进顺序游标，得到 %d", a2.soundCursor)
	}

	// 静音语义
	silent := []struct {
		name string
		js   string
	}{
		{"总开关关掉", `{"sound":{"enabled":false,"volume":0.6,"mode":"single","singleId":"sm-b","music":[]}}`},
		{"音量为 0", `{"sound":{"enabled":true,"volume":0,"mode":"single","singleId":"sm-b","music":[]}}`},
		{"顺序模式但列表为空", `{"sound":{"enabled":true,"volume":0.6,"mode":"sequence","music":[]}}`},
		{"单曲未指定", `{"sound":{"enabled":true,"volume":0.6,"mode":"single","singleId":"","music":[]}}`},
	}
	for _, c := range silent {
		sa, _ := newSoundApp(t, c.js)
		if got := sa.soundPickNext(sa.cfgSnapshot().SoundCfg(), func(int) int { return 0 }); got != "" {
			t.Errorf("%s：应静音，得到 %q", c.name, got)
		}
	}
}

// ---------- 播放 URL ----------

func TestSoundFileURL(t *testing.T) {
	a, dir := newSoundApp(t, `{}`)

	// 内置音 → 资源服务的 /sounds/xxx.wav
	if got := a.SoundFileURL("short_start.wav"); got != "/sounds/short_start.wav" {
		t.Errorf("内置音 URL 错：%q", got)
	}
	if got := a.SoundFileURL("nope.wav"); got != "" {
		t.Errorf("不存在的内置音应返回空串，得到 %q", got)
	}
	if got := a.SoundFileURL("   "); got != "" {
		t.Errorf("空文件名应返回空串，得到 %q", got)
	}

	// 自定义音 → 本进程的 /__sound/?p=…
	f := writeSoundFile(t, filepath.Join(dir, "sounds", "我的 歌.mp3"), 64)
	got := a.SoundFileURL(f)
	want := soundURLPrefix + "?p=" + url.QueryEscape(f)
	if got != want {
		t.Errorf("自定义音 URL 错：\n want %q\n got  %q", want, got)
	}
	if got := a.SoundFileURL(filepath.Join(dir, "sounds", "missing.mp3")); got != "" {
		t.Errorf("文件不存在应返回空串，得到 %q", got)
	}
	if got := a.SoundFileURL(filepath.Join(dir, "notaudio.txt")); got != "" {
		t.Errorf("非音频扩展名应返回空串，得到 %q", got)
	}
}

// ---------- 资源路由白名单 ----------

func TestAllowedSoundPath(t *testing.T) {
	a, dir := newSoundApp(t, `{}`)
	inside := writeSoundFile(t, filepath.Join(dir, "sounds", "a.mp3"), 16)
	sub := writeSoundFile(t, filepath.Join(dir, "sounds", "sub", "b.mp3"), 16)
	outside := writeSoundFile(t, filepath.Join(dir, "elsewhere.mp3"), 16)
	cfgFile := a.cfgSnapshot().Path()

	cases := []struct {
		name string
		p    string
		want bool
	}{
		{"音频目录内", inside, true},
		{"音频目录的子目录", sub, true},
		{"目录外", outside, false},
		{"配置文件本身", cfgFile, false},
		{"相对路径", "a.mp3", false},
		{"空", "", false},
		{"非音频扩展名", writeSoundFile(t, filepath.Join(dir, "sounds", "c.txt"), 8), false},
	}
	for _, c := range cases {
		if _, ok := a.allowedSoundPath(c.p); ok != c.want {
			t.Errorf("%s：allowedSoundPath(%q) = %v，期望 %v", c.name, c.p, ok, c.want)
		}
	}
}

// 老配置里点名的绝对路径（在音频目录之外）也要放行 —— 否则升级上来的用户一按试听就 403
func TestAllowedSoundPathConfigNamed(t *testing.T) {
	dir := t.TempDir()
	out := writeSoundFile(t, filepath.Join(dir, "用户自己放的位置", "old.mp3"), 16)
	a, _ := newSoundApp(t, soundCfgJSON("single", "sm-old", musicItem("sm-old", out, "老条目", true)))
	if _, ok := a.allowedSoundPath(out); !ok {
		t.Error("配置点名过的绝对路径应放行")
	}
	// 没被点名的同目录文件仍然不放行
	stranger := writeSoundFile(t, filepath.Join(dir, "用户自己放的位置", "stranger.mp3"), 16)
	if _, ok := a.allowedSoundPath(stranger); ok {
		t.Error("同一目录下没被点名的文件不该放行")
	}
}

func TestSoundMiddleware(t *testing.T) {
	a, dir := newSoundApp(t, `{}`)
	f := writeSoundFile(t, filepath.Join(dir, "sounds", "play.mp3"), 32)
	body := readFileString(t, f)

	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
		_, _ = w.Write([]byte("next"))
	})
	h := a.soundMiddleware(next)

	// 白名单内：200 + Content-Type + 原文内容
	req := httptest.NewRequest(http.MethodGet, soundURLPrefix+"?p="+url.QueryEscape(f), nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("合法请求应 200，得到 %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "audio/mpeg" {
		t.Errorf("Content-Type 应为 audio/mpeg，得到 %q", ct)
	}
	if rec.Body.String() != body {
		t.Errorf("响应体应与文件内容一致（%d vs %d 字节）", rec.Body.Len(), len(body))
	}

	// 白名单外：403
	bad := httptest.NewRequest(http.MethodGet, soundURLPrefix+"?p="+
		url.QueryEscape(filepath.Join(dir, "elsewhere.mp3")), nil)
	recBad := httptest.NewRecorder()
	h.ServeHTTP(recBad, bad)
	if recBad.Code != http.StatusForbidden {
		t.Errorf("白名单外应 403，得到 %d", recBad.Code)
	}
	if recBad.Body.String() == body {
		t.Error("403 不该把文件内容吐出来")
	}

	// 别的路径：原样交给下一个 handler
	other := httptest.NewRequest(http.MethodGet, "/settings.html", nil)
	recOther := httptest.NewRecorder()
	h.ServeHTTP(recOther, other)
	if recOther.Code != http.StatusNoContent || recOther.Body.String() != "next" {
		t.Errorf("非声音路径应转交下一个 handler，得到 %d / %q", recOther.Code, recOther.Body.String())
	}
}

// ---------- 导入 / 删除 ----------

func TestImportAndDeleteMusicSound(t *testing.T) {
	a, dir := newSoundApp(t, `{}`)
	sounds := filepath.Join(dir, "sounds")
	src := writeSoundFile(t, filepath.Join(t.TempDir(), "我的歌.mp3"), 64)

	item, errText := a.importMusicSound(src)
	if item == nil {
		t.Fatalf("导入应成功，得到错误 %q", errText)
	}
	got, _ := item["file"].(string)
	if !sameDir(filepath.Dir(got), sounds) {
		t.Fatalf("副本应落在音频目录里，得到 %q（期望目录 %q）", got, sounds)
	}
	if filepath.Ext(got) != ".mp3" {
		t.Errorf("副本应保留扩展名，得到 %q", got)
	}
	if item["name"] != "我的歌" {
		t.Errorf("显示名应是去扩展名的文件名，得到 %v", item["name"])
	}
	if readFileString(t, src) != readFileString(t, got) {
		t.Error("副本内容应与源一致")
	}

	// 删除：只删音频目录里那一层
	if !a.deleteMusicSound(got) {
		t.Fatal("删除音频目录里的副本应成功")
	}
	if fileExists(got) {
		t.Error("副本应已被删除")
	}
	if a.deleteMusicSound(got) {
		t.Error("重复删除应返回 false（文件已不在）")
	}
	if a.deleteMusicSound("short_start.wav") {
		t.Error("内置条目（相对名）不该被当成副本删除")
	}

	// 目录外与子目录都不碰
	outside := writeSoundFile(t, filepath.Join(dir, "outside.mp3"), 16)
	if a.deleteMusicSound(outside) || !fileExists(outside) {
		t.Error("音频目录外的文件不该被删除")
	}
	sub := writeSoundFile(t, filepath.Join(sounds, "sub", "inner.mp3"), 16)
	if a.deleteMusicSound(sub) || !fileExists(sub) {
		t.Error("子目录里的文件不该被删除")
	}

	// 非法扩展名 / 不存在的文件
	bad := writeSoundFile(t, filepath.Join(t.TempDir(), "doc.txt"), 16)
	if item, _ := a.importMusicSound(bad); item != nil {
		t.Error("非音频扩展名不该被导入")
	}
	if item, _ := a.importMusicSound(filepath.Join(t.TempDir(), "missing.mp3")); item != nil {
		t.Error("不存在的文件不该被导入")
	}
}

// ---------- 静音路径不建窗 / 不崩 ----------

// 无头构建与单测里都没有 Wails 应用：播放必须安静地退化成日志，不能碰窗口管理器
func TestPlayConfiguredSoundWithoutHost(t *testing.T) {
	a, _ := newSoundApp(t, soundCfgJSON("single", "sm-b",
		musicItem("sm-b", "short_start.wav", "双音上行", true)))
	a.playConfiguredSound() // 不 panic 即通过（真发声由 GUI 探针验）

	// 未启用时同样安静
	silent, _ := newSoundApp(t, `{"sound":{"enabled":false,"volume":0.6,"mode":"single","singleId":"sm-b","music":[]}}`)
	silent.playConfiguredSound()
}

// ---------- 小工具 ----------

func readFileString(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("打开文件失败 %s: %v", path, err)
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("读文件失败 %s: %v", path, err)
	}
	return string(b)
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode().IsRegular()
}
