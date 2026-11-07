package sound

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSupported(t *testing.T) {
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
		if got := Supported(c.name); got != c.want {
			t.Errorf("Supported(%q) = %v，期望 %v", c.name, got, c.want)
		}
	}
}

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
		if !IsLegacyMusicName(n) {
			t.Errorf("应识别为旧命名: %s", n)
		}
	}
	for _, n := range no {
		if IsLegacyMusicName(n) {
			t.Errorf("不该识别为旧命名: %s", n)
		}
	}
}

// 序号取“现有最大 +1”，删掉中间一条也不会把已有序号复用给新导入。
func TestFreeMusicPathIncrements(t *testing.T) {
	dir := t.TempDir()
	if got := filepath.Base(FreeMusicPath(dir, "mp3")); got != "music-1.mp3" {
		t.Fatalf("空目录应从 1 开始，得到 %s", got)
	}
	write := func(name string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("music-1.mp3")
	write("music-7.mp3")
	if got := filepath.Base(FreeMusicPath(dir, "mp3")); got != "music-8.mp3" {
		t.Fatalf("应接着最大序号往后排（music-8.mp3），得到 %s", got)
	}
	// 扩展名不同也共用同一套序号：目录里不会出现两个 music-8
	write("music-8.wav")
	if got := filepath.Base(FreeMusicPath(dir, "mp3")); got != "music-9.mp3" {
		t.Fatalf("跨扩展名也应避开已占用序号，得到 %s", got)
	}
}

func TestDisplayName(t *testing.T) {
	cases := []struct{ in, want string }{
		{`C:\music\song.mp3`, "song"},
		{"/a/b/铃声.flac", "铃声"},
		{"noext", "noext"},
		{".hidden", ".hidden"}, // 点号在开头（LastIndex=0）不算扩展名，原样返回
	}
	for _, c := range cases {
		if got := DisplayName(c.in); got != c.want {
			t.Errorf("DisplayName(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}
