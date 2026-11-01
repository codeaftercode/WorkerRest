package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"workerrest/internal/model"
	"workerrest/internal/settings"
	"workerrest/internal/sound"
)

// 随包提示音目录体检（sound_scan.go）的回归。
//
// 这里钉三件在真实使用里最容易被误判的事：
//   - 内置 4 条不能因为「配置里没登记」就被当成用户的新文件（它们由内置音逻辑直接使用）；
//   - 不可播放的文件（0 字节 / 非音频扩展名 / 子目录 / 隐藏文件）不能进候选，
//     否则用户点了导入还是播不出来，比不提示更糟；
//   - 写配置走的是「深拷贝 → 改 → 落盘 → 整体替换」，不能顺手把别的条目改了。

// withBuiltinSoundDir 把随包提示音目录指到临时目录（生产是 <resourcesRoot>/sounds）
func withBuiltinSoundDir(t *testing.T, dir string) {
	t.Helper()
	old := sound.BuiltinDir()
	sound.SetBuiltinDirFn(func() string { return dir })
	t.Cleanup(func() { sound.SetBuiltinDirFn(func() string { return old }) })
}

// musicFiles 配置音乐列表里的 id → file 快照，方便断言
func musicFiles(s *settings.Settings) map[string]string {
	out := map[string]string{}
	for _, m := range s.SoundCfg().Music {
		out[m.ID] = m.File
	}
	return out
}

func TestScanSoundDirReportsNewAndMissing(t *testing.T) {
	dir := t.TempDir()
	withBuiltinSoundDir(t, dir)
	// 随包的 4 个内置音（名字与 settings.BuiltinSounds() 一致）
	for _, n := range []string{"short_start.wav", "short_end.wav", "long_start.wav", "long_end.wav"} {
		writeSoundFile(t, filepath.Join(dir, n), 32)
	}
	// 用户丢进来的一个（该被发现）
	writeSoundFile(t, filepath.Join(dir, "my-alarm.mp3"), 128)
	// 以下都不该进候选
	writeSoundFile(t, filepath.Join(dir, "空文件.wav"), 0)
	writeSoundFile(t, filepath.Join(dir, "readme.txt"), 10)
	writeSoundFile(t, filepath.Join(dir, ".hidden.wav"), 8)
	writeSoundFile(t, filepath.Join(dir, "~$临时.wav"), 8)
	writeSoundFile(t, filepath.Join(dir, "子目录", "inner.wav"), 16)

	a, _ := newSoundApp(t, soundCfgJSON("single", "sm-keep",
		musicItem("sm-keep", "short_start.wav", "双音上行", true),
		musicItem("sm-gone", "missing.mp3", "没了的曲子", true)))

	n := a.scanSoundDir()
	if len(n.New) != 1 || n.New[0].File != "my-alarm.mp3" {
		t.Fatalf("新音频应只有 my-alarm.mp3，实际 %+v", n.New)
	}
	if n.New[0].Name != "my-alarm" {
		t.Fatalf("显示名应去掉扩展名，得到 %q", n.New[0].Name)
	}
	if len(n.Missing) != 1 || n.Missing[0].ID != "sm-gone" {
		t.Fatalf("缺失条目应只有 sm-gone，实际 %+v", n.Missing)
	}
}

func TestScanSoundDirDoesNotTreatBuiltinAsNew(t *testing.T) {
	dir := t.TempDir()
	withBuiltinSoundDir(t, dir)
	for _, b := range settings.BuiltinSounds() {
		writeSoundFile(t, filepath.Join(dir, b.File), 32)
	}
	// 配置里一条都没登记：4 个内置音仍不算「新发现」，也不该报任何待办
	a, _ := newSoundApp(t, soundCfgJSON("single", ""))
	n := a.scanSoundDir()
	if len(n.New) != 0 {
		t.Fatalf("内置提示音不该被当成新文件，实际 %+v", n.New)
	}
	if !n.empty() {
		t.Fatalf("只剩内置音时不该有需要用户决定的事，实际 %+v", n)
	}
}

func TestScanSoundDirMissingReportsRegisteredEntry(t *testing.T) {
	// 目录压根不存在：登记过的相对名条目必然找不到文件
	withBuiltinSoundDir(t, filepath.Join(t.TempDir(), "不存在"))
	a, _ := newSoundApp(t, soundCfgJSON("single", "sm-a",
		musicItem("sm-a", "short_start.wav", "双音上行", true)))

	n := a.scanSoundDir()
	if len(n.New) != 0 {
		t.Fatalf("目录不存在不该有新文件，实际 %+v", n.New)
	}
	if len(n.Missing) != 1 || n.Missing[0].ID != "sm-a" {
		t.Fatalf("登记条目应报缺失，实际 %+v", n.Missing)
	}
}

func TestImportScannedSoundsRegistersRelativeFile(t *testing.T) {
	dir := t.TempDir()
	withBuiltinSoundDir(t, dir)
	writeSoundFile(t, filepath.Join(dir, "my-alarm.mp3"), 128)

	a, cfgDir := newSoundApp(t, soundCfgJSON("single", ""))
	a.soundScan = &SoundScanNotice{
		Dir: dir,
		New: []SoundScanFile{{File: "my-alarm.mp3", Name: "my-alarm", Size: 128}},
	}

	r := a.ImportScannedSounds([]string{"my-alarm.mp3"})
	if r["ok"] != true || r["imported"] != 1 {
		t.Fatalf("导入应成功，得到 %+v", r)
	}

	// 内存里的配置换了新对象（与 SaveSettings 同口径）
	music := a.cfgSnapshot().SoundCfg().Music
	var found *model.SoundMusicItem
	for i := range music {
		if music[i].File == "my-alarm.mp3" {
			found = &music[i]
		}
	}
	if found == nil {
		t.Fatalf("音乐列表里应出现 my-alarm.mp3，实际 %+v", musicFiles(a.cfgSnapshot()))
	}
	if found.Name != "my-alarm" || !found.Enabled || found.ID == "" {
		t.Fatalf("新条目字段不对: %+v", *found)
	}

	// 真的落盘了（不是只在内存）
	raw, err := os.ReadFile(filepath.Join(cfgDir, "settings.json"))
	if err != nil {
		t.Fatalf("配置应已写盘: %v", err)
	}
	if !strings.Contains(string(raw), "my-alarm.mp3") {
		t.Fatalf("落盘的配置里没有新条目:\n%s", raw)
	}

	// 幂等：再导一次不应产生第二条
	if r2 := a.ImportScannedSounds([]string{"my-alarm.mp3"}); r2["imported"] != 0 {
		t.Fatalf("重复导入应为 0，得到 %+v", r2)
	}
	n := 0
	for _, m := range a.cfgSnapshot().SoundCfg().Music {
		if m.File == "my-alarm.mp3" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("my-alarm.mp3 应只有一条，实际 %d 条", n)
	}
}

func TestImportScannedSoundsRejectsUnknownNames(t *testing.T) {
	dir := t.TempDir()
	withBuiltinSoundDir(t, dir)
	a, _ := newSoundApp(t, soundCfgJSON("single", ""))
	a.soundScan = &SoundScanNotice{Dir: dir, New: []SoundScanFile{{File: "ok.wav", Name: "ok"}}}

	// 没扫到过的名字（含路径穿越）一律不写进配置
	for _, bad := range []string{"../../evil.mp3", "other.wav", "", "ok.wav/../x.wav"} {
		if r := a.ImportScannedSounds([]string{bad}); r["imported"] != 0 {
			t.Fatalf("应拒绝 %q，得到 %+v", bad, r)
		}
	}
	for _, f := range musicFiles(a.cfgSnapshot()) {
		if f == "../../evil.mp3" || f == "other.wav" || f == "x.wav" {
			t.Fatalf("不该写进配置: %q", f)
		}
	}
}

func TestDropMissingSoundsRemovesListedOnly(t *testing.T) {
	a, cfgDir := newSoundApp(t, soundCfgJSON("single", "sm-a",
		musicItem("sm-a", "a.wav", "A", true),
		musicItem("sm-b", "b.wav", "B", true)))
	// 只有 sm-a 被判为缺失；sm-b 不在待办里，即使前端传上来也不许删
	a.soundScan = &SoundScanNotice{Missing: []SoundScanMissing{{ID: "sm-a", File: "a.wav", Name: "A"}}}

	r := a.DropMissingSounds([]string{"sm-a", "sm-b"})
	if r["removed"] != 1 {
		t.Fatalf("只该删 1 条，得到 %+v", r)
	}
	got := musicFiles(a.cfgSnapshot())
	if _, ok := got["sm-a"]; ok {
		t.Fatalf("sm-a 应已删除: %+v", got)
	}
	if _, ok := got["sm-b"]; !ok {
		t.Fatalf("sm-b 不该被删: %+v", got)
	}
	// singleId 指向被删的 sm-a：normalizeSound 应把它挪到第一首启用项，不能留一个死 id
	if id := a.cfgSnapshot().SoundCfg().SingleID; id == "sm-a" || id == "" {
		t.Fatalf("singleId 应改指现存条目，得到 %q", id)
	}
	if raw, err := os.ReadFile(filepath.Join(cfgDir, "settings.json")); err != nil ||
		strings.Contains(string(raw), `"sm-a"`) {
		t.Fatalf("落盘配置里不该再有 sm-a（err=%v）:\n%s", err, raw)
	}
}

func TestDismissSoundScanStopsAsking(t *testing.T) {
	a, _ := newSoundApp(t, soundCfgJSON("single", ""))
	a.soundScan = &SoundScanNotice{
		New:     []SoundScanFile{{File: "x.wav", Name: "x"}},
		Missing: []SoundScanMissing{{ID: "sm-z", File: "z.wav", Name: "Z"}},
	}
	if a.GetSoundScanNotice() == nil {
		t.Fatal("有变化时应返回 notice")
	}
	a.DismissSoundScan()
	if a.GetSoundScanNotice() != nil {
		t.Fatal("用户选了暂不之后本次运行不该再返回 notice")
	}
}

func TestImportDropsHandledScanEntries(t *testing.T) {
	dir := t.TempDir()
	withBuiltinSoundDir(t, dir)
	writeSoundFile(t, filepath.Join(dir, "one.wav"), 16)
	writeSoundFile(t, filepath.Join(dir, "two.wav"), 16)

	a, _ := newSoundApp(t, soundCfgJSON("single", ""))
	a.soundScan = &SoundScanNotice{Dir: dir, New: []SoundScanFile{
		{File: "one.wav", Name: "one"},
		{File: "two.wav", Name: "two"},
	}}
	a.ImportScannedSounds([]string{"one.wav"})

	n := a.GetSoundScanNotice()
	if n == nil {
		t.Fatal("还剩 two.wav 没处理，notice 不该消失")
	}
	left, _ := n["new"].([]SoundScanFile)
	if len(left) != 1 || left[0].File != "two.wav" {
		t.Fatalf("待办里应只剩 two.wav，实际 %+v", n["new"])
	}
}
