package sound

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// 音乐副本的文件名规则（纯逻辑）。

// IsLegacyMusicName 旧版导入留下的副本名：music-<8-4-4-4-12 位十六进制>.<ext>。
// 新版统一成 music-<序号>.<ext>，用户打开目录能看懂哪个是哪个。
func IsLegacyMusicName(name string) bool {
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

// NextMusicIndex 目录里 music-<N>.<ext> 的最大序号 +1（一条都没有就从 1 开始）。
func NextMusicIndex(dir string) int {
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

// FreeMusicPath 在 dir 里找一个还没被占用的 music-<序号>.<ext>。
// 循环是为了防同名（用户手动放了一个同名文件、或两个导入同时进来）。
func FreeMusicPath(dir, ext string) string {
	for i := 0; i < 1000; i++ {
		p := filepath.Join(dir, fmt.Sprintf("music-%d.%s", NextMusicIndex(dir), ext))
		if _, err := os.Stat(p); os.IsNotExist(err) {
			return p
		}
	}
	return ""
}

// DisplayName 从文件名推显示名（去目录、去扩展名；去空了就退回原名）。
func DisplayName(file string) string {
	base := filepath.Base(file)
	if i := strings.LastIndex(base, "."); i > 0 {
		base = base[:i]
	}
	if strings.TrimSpace(base) == "" {
		return filepath.Base(file)
	}
	return base
}
