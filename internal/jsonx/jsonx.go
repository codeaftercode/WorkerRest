// Package jsonx 是配置归一化用到的纯 JSON / 字符串 / 数字工具。
//
// 这些函数最初散在 main 包的 settings_time.go / settings_msg.go / role.go 里，
// 被 settings / breakplan / role / sound / takeout 等多个域共享 —— 任何域要下沉，
// 都绕不开这套「把 JS 语义的 JSON 值读成 Go 类型」的基础设施，于是先把它单独抽出。
//
// 口径约定：这些函数逐字复刻前端 types.ts / config.ts / supervisor.ts 里对应的
// JS 语义（String(v)、Number(v)、Array.isArray 等），因为配置字段可能被用户手改成
// 品类不符的值，Go 侧必须与前端同口径兜住，否则会出现「老配置里 name 是数字」这类
// 边角差异。
package jsonx

import (
	"crypto/rand"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// StrOf 取字符串值；不是字符串返回空串。
func StrOf(v any) string {
	s, _ := v.(string)
	return s
}

// JsStr 复刻 JS 的 String(v)：null/undefined → ""，数字不带多余小数，布尔转字面量。
func JsStr(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	case float64:
		if math.IsNaN(t) {
			return "NaN"
		}
		if math.IsInf(t, 1) {
			return "Infinity"
		}
		if math.IsInf(t, -1) {
			return "-Infinity"
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return ""
	}
}

// ToNumber 复刻 JS 的 Number(v)：null/""/false → 0，字符串按数字解析，
// 解析不出来返回 ok=false（对应 NaN / Infinity，调用方按 fallback 处理）。
func ToNumber(v any) (float64, bool) {
	switch t := v.(type) {
	case nil:
		return 0, true
	case float64:
		if math.IsNaN(t) || math.IsInf(t, 0) {
			return 0, false
		}
		return t, true
	case bool:
		if t {
			return 1, true
		}
		return 0, true
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return 0, true
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return 0, false
		}
		return f, true
	default:
		return 0, false
	}
}

// AnySlice 取数组；不是数组时返回 nil（等价于 JS 的 Array.isArray ? : []）。
func AnySlice(v any) []any {
	if arr, ok := v.([]any); ok {
		return arr
	}
	return nil
}

// NewRandomID 生成一枚 v4 形态的随机唯一串（与前端自带的同类生成器同形）。
func NewRandomID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("fallback-%d", os.Getpid())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// ItemID 取出条目的 id：字符串或数字都认（手改配置时可能写成 1 而不是 "1"）。
func ItemID(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case float64:
		if x == math.Trunc(x) && x >= 0 {
			return strconv.FormatInt(int64(x), 10)
		}
	case int:
		if x >= 0 {
			return strconv.Itoa(x)
		}
	}
	return ""
}

// NextSeqID 列表内可用的最短 id：现有 id 里最大的十进制整数 +1（没有数字 id 就从 "1" 起）。
//
// 条目 id 只需要「本列表内唯一且稳定」——固定模式靠它选中一条，删除 / 拖动靠它定位，
// 跨列表、跨文件都不要求唯一。取最大值而不是最小空位：删掉中间某条后新增不会复用刚被
// 删掉的号，fixedId 更不容易指错。
func NextSeqID(ids []string) string {
	max := 0
	for _, s := range ids {
		if n, err := strconv.Atoi(s); err == nil && n > max {
			max = n
		}
	}
	return strconv.Itoa(max + 1)
}

// IsSeqID 判断 id 是否已经是「列表内序号」形态（十进制正整数）。
func IsSeqID(s string) bool {
	n, err := strconv.Atoi(s)
	return err == nil && n > 0
}

// SeqIDFor 把一个已有 id 收敛成序号：已经是正整数就原样保留（可能不连续，但稳定），
// 其余情况（随机串 / builtin-xxx-N / 手写乱码 / 与本列表里别的条目重号）一律重新编号。
func SeqIDFor(old string, ids []string, seen map[string]bool) string {
	if IsSeqID(old) && !seen[old] {
		return old
	}
	return NextSeqID(ids)
}

// RemapMsgID 按「旧 id → 新 id」把 fixedId 搬到重编号后的那一条上；
// 映射不到就原样返回，交给调用方按「指向真实条目」的规则再校验一次。
func RemapMsgID(remap map[string]string, id string) string {
	if id == "" {
		return ""
	}
	if n, ok := remap[id]; ok {
		return n
	}
	return id
}

// AsMap 取子对象；缺失 / 类型不符给空 map。
func AsMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

// WriteFileAtomic 先写 `path.tmp` 再改名：写到一半崩溃不会留下半截 JSON。
// settings.json / stats.json / meta.json 这些配置文件都走它落盘。
func WriteFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		// 改名失败（例如目标位置被同名目录占着）→ 别把临时文件留在目录里
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
