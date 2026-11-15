package role

import (
	"encoding/base64"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

var windowsReservedNames = map[string]bool{
	"con": true, "prn": true, "aux": true, "nul": true,
	"com1": true, "com2": true, "com3": true, "com4": true, "com5": true,
	"com6": true, "com7": true, "com8": true, "com9": true,
	"lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true, "lpt5": true,
	"lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
}

// maxRoleDirNameBytes 目录名的字节上限。ValidRoleDirName 允许 128 字节，
// 这里留出 " (2)" 之类去重后缀的余量。
const maxRoleDirNameBytes = 120

// SanitizeRoleDirName 把专员名收敛成合法目录名。
//
// 目录名是身份的文件系统投影，所以这里多做几件“不处理就会静默撞车”的事：
//   - 剔除 Windows 非法字符（含控制字符）；
//   - 去掉尾随的点与空格（系统会吞掉它们，"abc." 与 "abc" 会指向同一个目录）；
//   - 保留名（CON / PRN / COM1 …）加下划线前缀，否则 mkdir 直接失败；
//   - 以 "." 开头的名字加下划线前缀（否则扫描时被当成隐藏目录跳过，见下）；
//   - 限制长度：ValidRoleDirName 按**字节**限 128，所以 rune 与 byte 两道都要守。
//
// 这里必须保证产出**一定能通过 ValidRoleDirName** —— 否则改名会“成功”但专员从列表里
// 消失（文件还在，却再也改名不了、删不掉、预览不到）。TestSanitizeRoleDirNameIsAlwaysScannable
// 把这条不变量钉在一整个脏名字语料上。
//
// 分工：**前端命名框才是策略闸**（settings.js 的 roleNameError 直接拒收点开头与
// \ / : * ? " < > | 这 9 个字符，并去掉结尾的点），这里只做**兜底**——手改过的
// 配置、历史数据、跨平台搬过来的目录仍然要能读、能改、能删，所以不做拒绝，只做收敛。
func SanitizeRoleDirName(name string) string {
	s := strings.TrimSpace(name)
	// 控制字符（0x00-0x1F、0x7F）不是“不好看”而已：Win32 直接判文件名语法错误，
	// 于是 os.Stat 返回一个既不是“存在”也不是“不存在”的错误，而 UniqueRoleDir
	// 只在“不存在”时收敛 —— 名字里混进一个换行符就足以把改名卡死。
	s = strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			return -1
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	s = strings.TrimRight(strings.TrimSpace(s), " .")
	// 截断要同时守住 rune 与 byte 两道：60 个汉字是 180 字节，只按 rune 截会让
	// 目录名超过 ValidRoleDirName 的 128 字节上限，扫描时被跳过 —— 与上面同一类消失。
	if runes := []rune(s); len(runes) > 60 {
		s = strings.TrimSpace(string(runes[:60]))
	}
	for len(s) > maxRoleDirNameBytes {
		_, size := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-size]
	}
	s = strings.TrimRight(s, " .")
	if s == "" {
		return "督休专员"
	}
	// "." 开头 = "." / ".." / 隐藏目录的地盘，ValidRoleDirName 一律判非法。
	// 名字叫 ".hidden" 本来完全合法，加个前缀把它救回来（与保留名的处理同一路）。
	if strings.HasPrefix(s, ".") {
		s = "_" + s
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
