package role

import (
	"encoding/base64"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
)

var windowsReservedNames = map[string]bool{
	"con": true, "prn": true, "aux": true, "nul": true,
	"com1": true, "com2": true, "com3": true, "com4": true, "com5": true,
	"com6": true, "com7": true, "com8": true, "com9": true,
	"lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true, "lpt5": true,
	"lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
}

// SanitizeRoleDirName 把专员名收敛成合法目录名。
//
// 目录名是身份的文件系统投影，所以这里多做几件“不处理就会静默撞车”的事：
//   - 剔除 Windows 非法字符；
//   - 去掉尾随的点与空格（系统会吞掉它们，"abc." 与 "abc" 会指向同一个目录）；
//   - 保留名（CON / PRN / COM1 …）加下划线前缀，否则 mkdir 直接失败；
//   - 限制长度：目录名上限 255 字节，中文按 3 字节算，60 个字符留足余量。
func SanitizeRoleDirName(name string) string {
	s := strings.TrimSpace(name)
	s = strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			return -1
		default:
			return r
		}
	}, s)
	s = strings.TrimRight(strings.TrimSpace(s), " .")
	if runes := []rune(s); len(runes) > 60 {
		s = strings.TrimSpace(string(runes[:60]))
	}
	s = strings.TrimRight(s, " .")
	if s == "" {
		return "督休专员"
	}
	base := strings.ToLower(s)
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	if windowsReservedNames[base] {
		s = "_" + s
	}
	return s
}

// IsRegularFile 路径存在且是普通文件
func IsRegularFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// ToDataURL 读图并转 data URL（渲染层 <img src> 直接用，省一次 HTTP 往返）
func ToDataURL(file string) string {
	b, err := os.ReadFile(file)
	if err != nil {
		return ""
	}
	mime, ok := ImageExtMIME[strings.ToLower(filepath.Ext(file))]
	if !ok {
		mime = "image/png"
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(b)
}

// PickRoleLine 从督休专员的台词池里选 1 条（与 上一版 pickRoleLine 同规则）：
// fixed 取指定条目（与启停无关）；sequence 在启用条目间轮换（游标按**专员名**记忆，
// 改名会让游标归零 —— 这是改名换身份的附带代价，不影响正确性）；
// random 等概率抽启用条目。取不到返回空串——由渲染层沿用第一段台词。
func PickRoleLine(cfg RoleMessages, name string, seq map[string]int, rng *rand.Rand) string {
	if cfg.Order == "fixed" {
		for _, it := range cfg.Items {
			if it.ID == cfg.FixedID {
				return it.Text
			}
		}
		return ""
	}
	pool := make([]RoleMessageItem, 0, len(cfg.Items))
	for _, it := range cfg.Items {
		if it.Enabled {
			pool = append(pool, it)
		}
	}
	if len(pool) == 0 {
		return ""
	}
	if cfg.Order == "sequence" {
		cur := seq[name] % len(pool)
		seq[name] = (cur + 1) % len(pool)
		return pool[cur].Text
	}
	return pool[rng.Intn(len(pool))].Text
}

// PickOne 等概率取一条（掷骰子交给调用方持有的 rng，便于测试时固定种子）
func PickOne(lines []string, rng *rand.Rand) string {
	if len(lines) == 0 {
		return ""
	}
	return lines[rng.Intn(len(lines))]
}
