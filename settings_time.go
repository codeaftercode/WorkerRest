package main

import (
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"strings"

	"workerrest/internal/jsonx"
	"workerrest/internal/model"
)

// ---------- 时刻表 ----------

// defaultWeekdays 每周模式的默认勾选：工作日（与 types.ts 的 DEFAULT_WEEKDAYS 一致）
var defaultWeekdays = []int{1, 2, 3, 4, 5}

// reLeadingInt 复刻 JS 的 parseInt：跳过前导空白与正负号，取连续数字前缀。
// 不能直接用 Sscanf("%f")：对字符串 "12.7"，JS 的 parseInt 得 12 而 Sscanf 得 12.7。
var reLeadingInt = regexp.MustCompile(`^[ \t\r\n]*([+-]?[0-9]+)`)

// slotInt 把任意来源的数值夹紧到 [min, max]；非法时用 fallback。
// 与 types.ts 里 normalizeTimeSlot 的内部 int() 同义：数字直接取，其余按字符串 parseInt。
func slotInt(v any, min, max, fallback int) int {
	var n float64
	switch t := v.(type) {
	case float64:
		n = t
	case int:
		n = float64(t)
	case int32:
		n = float64(t)
	case int64:
		n = float64(t)
	case json.Number:
		if f, err := t.Float64(); err == nil {
			n = f
		} else {
			return fallback
		}
	case string:
		m := reLeadingInt.FindStringSubmatch(t)
		if m == nil {
			return fallback
		}
		f, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			return fallback
		}
		n = f
	default:
		return fallback
	}
	if math.IsNaN(n) || math.IsInf(n, 0) {
		return fallback
	}
	r := int(math.Round(n))
	if r < min {
		return min
	}
	if r > max {
		return max
	}
	return r
}

// normalizeTimeSlot 夹紧一条时间配置；结构不成立返回 nil（调用方过滤）。
// 与 types.ts 的 normalizeTimeSlot 同义：每周模式至少要有一天，缺了就补工作日，
// 避免「设置了却没反应」。
func normalizeTimeSlot(raw any) (model.TimeSlot, bool) {
	m, ok := raw.(map[string]any)
	if !ok {
		return model.TimeSlot{}, false
	}
	slot := model.TimeSlot{
		ID:      newIDIfEmpty(jsonx.StrOf(m["id"])),
		Hour:    slotInt(m["hour"], 0, 23, 12),
		Minute:  slotInt(m["minute"], 0, 59, 0),
		Repeat:  "daily",
		Enabled: m["enabled"] != false,
	}
	if s, _ := m["repeat"].(string); s == "weekly" {
		slot.Repeat = "weekly"
	}
	seen := map[int]bool{}
	if arr, ok := m["days"].([]any); ok {
		for _, d := range arr {
			n := slotInt(d, 1, 7, 0)
			if n >= 1 && n <= 7 && !seen[n] {
				seen[n] = true
				slot.Days = append(slot.Days, n)
			}
		}
	}
	sortInts(slot.Days)
	if slot.Repeat == "weekly" && len(slot.Days) == 0 {
		slot.Days = append([]int(nil), defaultWeekdays...)
	}
	if slot.Repeat != "weekly" {
		slot.Days = []int{}
	}
	return slot, true
}

func normalizeTimeSlots(raw any) []model.TimeSlot {
	arr, ok := raw.([]any)
	if !ok {
		return []model.TimeSlot{}
	}
	out := make([]model.TimeSlot, 0, len(arr))
	for _, it := range arr {
		if s, ok := normalizeTimeSlot(it); ok {
			out = append(out, s)
		}
	}
	return out
}

// slotsToAny 把归一化后的时刻表还原成 map 数组（落盘 / 下发用）
func slotsToAny(slots []model.TimeSlot) []any {
	arr := make([]any, 0, len(slots))
	for _, s := range slots {
		arr = append(arr, slotToMap(s))
	}
	return arr
}

func slotToMap(s model.TimeSlot) map[string]any {
	days := make([]any, 0, len(s.Days))
	for _, d := range s.Days {
		days = append(days, d)
	}
	return map[string]any{
		"id": s.ID, "hour": s.Hour, "minute": s.Minute,
		"repeat": s.Repeat, "days": days, "enabled": s.Enabled,
	}
}

func sortInts(a []int) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j] < a[j-1]; j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}

// clipRunes 按字符数截断（JS 的 slice(0, n) 按 UTF-16 码元；中文两者一致，
// 只有超出 BMP 的符号会有 1 个字符的差别，可接受）
func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

// newIDIfEmpty 没 id 就补一枚（时刻条目靠 id 做去重与增删）
func newIDIfEmpty(v string) string {
	if strings.TrimSpace(v) != "" {
		return v
	}
	return jsonx.NewRandomID()
}

// ============================================================================
// 归一化
// ============================================================================

// normalizeAppName 折叠空白、去首尾、限长 30（可以是空串 = 不显示名字）
var reSpaces = regexp.MustCompile(`\s+`)
