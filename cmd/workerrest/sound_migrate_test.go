package main

import (
	"os"
	"path/filepath"
	"testing"
	"workerrest/internal/settings"
)

// TestIsLegacyMusicName / TestFreeMusicPathIncrements 已下沉到 internal/sound（白盒测试）。

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
