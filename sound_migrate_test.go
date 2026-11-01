package main

import (
	"os"
	"path/filepath"
	"testing"
	"workerrest/internal/settings"
)

// 旧命名识别：只认 music- 前缀 + 8-4-4-4-12 的十六进制，别把用户自己的文件误伤。
func TestIsLegacyMusicName(t *testing.T) {
	yes := []string{
		"music-3435a519-c1d7-4d26-a82d-8267119e0001.mp3",
		"music-3435A519-C1D7-4D26-A82D-8267119E0001.wav",
		"music-00000000-0000-4000-8000-000000000000.flac",
	}
	no := []string{
		"music-1.mp3",  // 新命名
		"music-12.wav", // 新命名（两位数）
		"music-1.MP3",  // 新命名（大写扩展名）
		"music-3435a519c1d74d26a82d8267119e0001.mp3",      // 没有连字符
		"music-3435a519-c1d7-4d26-a82d-8267119e000.mp3",   // 35 位
		"music-3435a519-c1d7-4d26-a82d-8267119e00010.mp3", // 37 位
		"music-3435a519-c1d7-4d26-a82d-8267119e000g.mp3",  // 非十六进制
		"mymusic-3435a519-c1d7-4d26-a82d-8267119e0001.mp3",
		"music-3435a519-c1d7-4d26-a82d-8267119e0001", // 没有扩展名
	}
	for _, n := range yes {
		if !isLegacyMusicName(n) {
			t.Errorf("应识别为旧命名: %s", n)
		}
	}
	for _, n := range no {
		if isLegacyMusicName(n) {
			t.Errorf("不该识别为旧命名: %s", n)
		}
	}
}

// 序号取「现有最大 +1」，删掉中间一条也不会把已有序号复用给新导入。
func TestFreeMusicPathIncrements(t *testing.T) {
	dir := t.TempDir()
	if got := filepath.Base(freeMusicPath(dir, "mp3")); got != "music-1.mp3" {
		t.Fatalf("空目录应从 1 开始，得到 %s", got)
	}
	write := func(name string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("music-1.mp3")
	write("music-7.mp3")
	if got := filepath.Base(freeMusicPath(dir, "mp3")); got != "music-8.mp3" {
		t.Fatalf("应接着最大序号往后排（music-8.mp3），得到 %s", got)
	}
	// 扩展名不同也共用同一套序号：目录里不会出现两个 music-8
	write("music-8.wav")
	if got := filepath.Base(freeMusicPath(dir, "mp3")); got != "music-9.mp3" {
		t.Fatalf("跨扩展名也应避开已占用序号，得到 %s", got)
	}
}

// 迁移：旧名文件改名 + 配置里的绝对路径同步 + 幂等。
func TestMigrateMusicFileNames(t *testing.T) {
	root := t.TempDir()
	sndDir := filepath.Join(root, "sounds")
	if err := os.MkdirAll(sndDir, 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := "music-3435a519-c1d7-4d26-a82d-8267119e0001.mp3"
	if err := os.WriteFile(filepath.Join(sndDir, legacy), []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 已存在的可读名：迁移要接着它往后排，不能覆盖
	if err := os.WriteFile(filepath.Join(sndDir, "music-3.flac"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join(sndDir, legacy)
	cfg := map[string]any{
		"sound": map[string]any{
			"music": []any{
				map[string]any{"id": "sm-1", "file": oldPath, "name": "旧条目", "enabled": true},
				map[string]any{"id": "sm-2", "file": filepath.Join(sndDir, "music-3.flac"), "name": "已有", "enabled": true},
			},
			"singleId": "sm-1",
		},
	}
	a := &App{cfg: settings.New(cfg, filepath.Join(root, "settings.json"))}

	if n := a.migrateMusicFileNames(); n != 1 {
		t.Fatalf("应迁移 1 条，得到 %d", n)
	}
	newPath := filepath.Join(sndDir, "music-4.mp3")
	if _, err := os.Stat(newPath); err != nil {
		t.Fatalf("新文件应存在：%v", err)
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatal("旧文件应已消失")
	}
	music := cfg["sound"].(map[string]any)["music"].([]any)
	got := music[0].(map[string]any)["file"].(string)
	if got != newPath {
		t.Fatalf("配置应改指新路径，得到 %q", got)
	}
	if music[1].(map[string]any)["file"].(string) == "" {
		t.Fatal("不相关的条目不该被清空")
	}
	// singleId 指向条目 id，不随文件名变
	if cfg["sound"].(map[string]any)["singleId"].(string) != "sm-1" {
		t.Fatal("singleId 不该被改动")
	}
	// 幂等
	if n := a.migrateMusicFileNames(); n != 0 {
		t.Fatalf("第二次启动不该再改，得到 %d", n)
	}
}
