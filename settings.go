package main

import (
	"crypto/rand"
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// 配置层：整份配置以 map 持有，而不是在 Go 里逐个字段声明结构体。
//
// 原因：设置页**没有自己的默认值** —— 它拿到 `getSettings()` 的结果就直接用来填表
// （见 renderer/settings.ts 的 `S = await api.getSettings()`），少一个字段就会在
// 渲染时抛错。Go 逻辑真正读的只有十来个字段（时长、间隔、外观、督导选择、提醒内容），
// 其余几十个字段 Go 一个都不碰，但必须**原样保留并完整下发**。所以这里：
//   raw（整份 map） = deepMerge(出厂默认, 磁盘内容)
// 用访问器读出 Go 需要的那几个，其余照原样活着。
//
// 出厂默认不手写，而是从 历史源码/types.ts 的 defaultSettings() 生成
// （scripts/gen_default_settings.mjs，可 --check 检测漂移）—— 手抄过一次就抄错了
// appName 与 short.postponeMs 两个值，这类失配必须靠生成来杜绝。
//
// 归一化（本文件下半部分）逐条对应 历史源码/config.ts 的
// loadSettingsFrom / settings:save / settings:import 三条路径。**顺序不能动**：
// 迁移必须在归一化之前（否则旧键会被当成非法值处理），预置条目种入必须在
// reminderMessages 重建之后。对拍脚本 scripts/verify_settings_parity.py 是最快的回归手段。

//go:embed default_settings.json
var defaultSettingsJSON []byte

// ---------- 从配置里读出来的类型化结构（字段名与磁盘 JSON 逐字一致） ----------

// BreakConfig 一次休息的配置（mode = countdown | fixed 决定用间隔还是时刻表）
type BreakConfig struct {
	Enabled        bool       `json:"enabled"`
	Mode           string     `json:"mode"`
	DurationMs     int64      `json:"durationMs"`
	IntervalMs     int64      `json:"intervalMs"`
	NotifyBeforeMs int64      `json:"notifyBeforeMs"`
	PostponeMs     int64      `json:"postponeMs"`
	Slots          []TimeSlot `json:"slots"`
}

// AppearanceConfig 外观配置（磁盘结构；比下发给渲染层的 Appearance 多几个字段）
type AppearanceConfig struct {
	Color        string  `json:"color"`
	Transparency float64 `json:"transparency"`
	Blur         bool    `json:"blur"`
	Fullscreen   bool    `json:"fullscreen"`
	AlwaysOnTop  bool    `json:"alwaysOnTop"`
	WidthPct     float64 `json:"widthPct"`
	HeightPct    float64 `json:"heightPct"`
}

// SupervisorConfig 督休专员显示开关（督休专员本体在 roles/<key>/role.json）
type SupervisorConfig struct {
	Enabled   bool   `json:"enabled"`
	Gaze      bool   `json:"gaze"`
	Selection string `json:"selection"` // fixed | sequence | random
	FixedKey  string `json:"fixedKey"`
}

// ReminderMessageItem 一条提醒内容
type ReminderMessageItem struct {
	ID      string `json:"id"`
	Text    string `json:"text"`
	Enabled bool   `json:"enabled"`
}

// ReminderMessages 某类提醒的提醒内容池
type ReminderMessages struct {
	Order   string                `json:"order"`
	FixedID string                `json:"fixedId"`
	Items   []ReminderMessageItem `json:"items"`
}

// Settings 一份配置：raw 是完整的配置对象，path 是它实际来自的文件（空 = 全用出厂默认）
type Settings struct {
	raw  map[string]any
	path string
}

// defaultSettingsMap 出厂默认（解析内嵌的生成产物；解析不了说明生成物坏了，直接崩）
func defaultSettingsMap() map[string]any {
	var m map[string]any
	if err := json.Unmarshal(defaultSettingsJSON, &m); err != nil {
		panic(fmt.Sprintf("default_settings.json 无法解析（内嵌的出厂默认坏了）: %v", err))
	}
	return m
}

// isPlainObject 与 config.ts 的 isPlainObject 同义：数组与 nil 都不算「普通对象」
func isPlainObject(v any) bool {
	if v == nil {
		return false
	}
	_, ok := v.(map[string]any)
	return ok
}

// deepMerge 深合并：以 base 为底叠加 over。
//
// 逐字对应 config.ts 的 deepMerge —— 只递归普通对象，数组整体替换而不是逐项合并
// （提醒内容列表 / 时刻表 / 音乐列表都靠这条：用户删掉一条就是删掉，不能被默认值补回来）。
// over 不是普通对象时**原样返回 over**（含 nil）—— 这是 JS 的语义：显式写了 null 就该是 null，
// 不能悄悄换成默认值。要区分「键缺失」与「键是 null」的调用点自己先做存在性判断。
func deepMerge(base, over any) any {
	if !isPlainObject(base) || !isPlainObject(over) {
		return over
	}
	bm := base.(map[string]any)
	om := over.(map[string]any)
	out := make(map[string]any, len(bm)+len(om))
	for k, v := range bm {
		out[k] = v
	}
	for k, v := range om {
		if bv, ok := bm[k]; ok {
			out[k] = deepMerge(bv, v)
		} else {
			out[k] = v
		}
	}
	return out
}

// deepCopyValue 深拷贝一个 JSON 值（内嵌默认配置是共享的，改写前必须先拷一份）
func deepCopyValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, vv := range t {
			out[k] = deepCopyValue(vv)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, vv := range t {
			out[i] = deepCopyValue(vv)
		}
		return out
	default:
		return v
	}
}

// ---------- 读取访问器 ----------

// lookup 按路径取值；任一层缺失或类型不符返回 nil
func (s *Settings) lookup(keys ...string) any {
	var cur any = s.raw
	for _, k := range keys {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		v, exists := m[k]
		if !exists {
			return nil
		}
		cur = v
	}
	return cur
}

// Str 取字符串；缺失 / 类型不符时用 def
func (s *Settings) Str(def string, keys ...string) string {
	if v, ok := s.lookup(keys...).(string); ok {
		return v
	}
	return def
}

// Num 取浮点数；JSON 里的数字都是 float64
func (s *Settings) Num(def float64, keys ...string) float64 {
	switch v := s.lookup(keys...).(type) {
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return def
		}
		return v
	case json.Number:
		if f, err := v.Float64(); err == nil {
			return f
		}
	}
	return def
}

// Int 取整数（四舍五入）
func (s *Settings) Int(def int64, keys ...string) int64 {
	switch t := s.lookup(keys...).(type) {
	case float64:
		if !math.IsNaN(t) && !math.IsInf(t, 0) {
			return int64(math.Round(t))
		}
	case json.Number:
		if f, err := t.Int64(); err == nil {
			return f
		}
	}
	return def
}

// Flag 取布尔；只认真正的 bool（与 normalizeToggles 的口径一致）
func (s *Settings) Flag(def bool, keys ...string) bool {
	if v, ok := s.lookup(keys...).(bool); ok {
		return v
	}
	return def
}

// Map 取子对象
func (s *Settings) Map(keys ...string) map[string]any {
	if v, ok := s.lookup(keys...).(map[string]any); ok {
		return v
	}
	return nil
}

// Slice 取数组
func (s *Settings) Slice(keys ...string) []any {
	if v, ok := s.lookup(keys...).([]any); ok {
		return v
	}
	return nil
}

// Raw 整份配置（给 settings:get 下发，也用于落盘）
func (s *Settings) Raw() map[string]any { return s.raw }

// Path 配置来自哪个文件（空 = 全用出厂默认，尚未落盘）
func (s *Settings) Path() string { return s.path }

// ---------- 类型化视图（只覆盖 Go 逻辑真正要用的字段） ----------

// Break 某种休息的配置（kind = "short" | "long"）
func (s *Settings) Break(kind string) BreakConfig {
	return breakConfigOf(s.Map(kind))
}

// breakConfigOf 从一份休息配置对象读出类型化配置（缺字段用与 types.ts 同口径的默认值）。
// 内置短/长与自定义提醒共用它 —— 自定义提醒的卡片是 {id,name,config,messages}，
// 配置本体在 config 子对象里，取值口径与短休息完全一致。
func breakConfigOf(m map[string]any) BreakConfig {
	cfg := BreakConfig{
		Enabled:        true,
		Mode:           "countdown",
		DurationMs:     20_000,
		IntervalMs:     600_000,
		NotifyBeforeMs: 10_000,
		PostponeMs:     120_000,
		Slots:          []TimeSlot{},
	}
	if m == nil {
		return cfg
	}
	cfg.Enabled = mapFlag(m, "enabled", true)
	cfg.Mode = normBreakMode(mapStr(m, "mode", "countdown"))
	cfg.DurationMs = mapInt(m, "durationMs", 20_000)
	cfg.IntervalMs = mapInt(m, "intervalMs", 600_000)
	cfg.NotifyBeforeMs = mapInt(m, "notifyBeforeMs", 10_000)
	cfg.PostponeMs = mapInt(m, "postponeMs", 120_000)
	cfg.Slots = normalizeTimeSlots(m["slots"])
	return cfg
}

// normBreakMode 触发方式只认两个合法值，其余一律按倒计时（旧配置 mode 非法时如此）
func normBreakMode(v string) string {
	if v == "fixed" {
		return "fixed"
	}
	return "countdown"
}

// mapFlag / mapStr / mapInt 从一份 JSON 对象里取字段，语义与 Settings.Flag/Str/Int 一致：
// 只认类型正确的值，其余（含缺失、null、类型不符）一律用默认值。
func mapFlag(m map[string]any, key string, def bool) bool {
	if v, ok := m[key].(bool); ok {
		return v
	}
	return def
}

func mapStr(m map[string]any, key, def string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return def
}

func mapInt(m map[string]any, key string, def int64) int64 {
	switch t := m[key].(type) {
	case float64:
		if !math.IsNaN(t) && !math.IsInf(t, 0) {
			return int64(math.Round(t))
		}
	case json.Number:
		if f, err := t.Int64(); err == nil {
			return f
		}
	}
	return def
}

// SupervisorCfg 督休专员显示开关（督休专员本体在 roles/<key>/role.json）
func (s *Settings) SupervisorCfg() SupervisorConfig {
	return SupervisorConfig{
		Enabled:   s.Flag(true, "supervisor", "enabled"),
		Gaze:      s.Flag(true, "supervisor", "gaze"),
		Selection: s.normSelection(s.Str("fixed", "supervisor", "selection")),
		FixedKey:  s.Str("", "supervisor", "fixedKey"),
	}
}

func (s *Settings) normSelection(v string) string {
	switch v {
	case "sequence", "random":
		return v
	default:
		return "fixed"
	}
}

// AppearanceCfg 外观（磁盘结构）
func (s *Settings) AppearanceCfg() AppearanceConfig {
	return AppearanceConfig{
		Color:        s.Str("#4f7cff", "appearance", "color"),
		Transparency: s.Num(0.15, "appearance", "transparency"),
		Blur:         s.Flag(true, "appearance", "blur"),
		Fullscreen:   s.Flag(false, "appearance", "fullscreen"),
		AlwaysOnTop:  s.Flag(true, "appearance", "alwaysOnTop"),
		WidthPct:     s.Num(80, "appearance", "widthPct"),
		HeightPct:    s.Num(80, "appearance", "heightPct"),
	}
}

// AppearanceView 下发给渲染层的外观结构
func (s *Settings) AppearanceView() Appearance {
	cfg := s.AppearanceCfg()
	return Appearance{
		Color:        cfg.Color,
		Transparency: cfg.Transparency,
		Blur:         cfg.Blur,
		Fullscreen:   cfg.Fullscreen,
	}
}

// Reminders 某类提醒的提醒内容池（kind = "short" | "long" | "takeout"）
func (s *Settings) Reminders(kind string) ReminderMessages {
	return remindersOf(s.Map("reminderMessages", kind))
}

// RemindersByID 按休息身份取提醒内容池：自定义提醒取它自己的那一套，短 / 长 / 外卖
// 取共用的 reminderMessages 表。与 types.ts 的 breakMessagesById 同口径。
func (s *Settings) RemindersByID(id string) ReminderMessages {
	if cb := s.CustomBreakByID(id); cb != nil {
		return remindersOf(asMap(cb["messages"]))
	}
	kind := "short"
	if id == "long" || id == "takeout" {
		kind = id
	}
	return s.Reminders(kind)
}

// remindersOf 从一份提醒内容对象读出类型化结构（items 里非对象项直接丢弃）
func remindersOf(m map[string]any) ReminderMessages {
	out := ReminderMessages{
		Order:   "fixed",
		FixedID: "",
		Items:   []ReminderMessageItem{},
	}
	if m == nil {
		return out
	}
	out.Order = mapStr(m, "order", "fixed")
	out.FixedID = mapStr(m, "fixedId", "")
	for _, it := range anySlice(m["items"]) {
		im, ok := it.(map[string]any)
		if !ok {
			continue
		}
		out.Items = append(out.Items, ReminderMessageItem{
			ID:      mapStr(im, "id", ""),
			Text:    mapStr(im, "text", ""),
			Enabled: mapFlag(im, "enabled", false),
		})
	}
	return out
}

// TakeoutEnabled 外卖提醒总开关
func (s *Settings) TakeoutEnabled() bool { return s.Flag(true, "takeout", "enabled") }

// TakeoutSlots 外卖提醒的时刻表（已归一化）
func (s *Settings) TakeoutSlots() []TimeSlot { return normalizeTimeSlots(s.lookup("takeout", "slots")) }

// AllScreens 「在所有屏幕显示提醒窗口」开关（休息窗口与外卖窗口共用同一多屏策略）
func (s *Settings) AllScreens() bool { return s.Flag(true, "allScreens") }

// StartMinimized 启动时最小化到托盘（不显示设置窗口，托盘图标在，点一下就出来）
func (s *Settings) StartMinimized() bool { return s.Flag(false, "startMinimized") }

// AppName 软件名（空串合法 = 不显示名字）。
// 注意：这里做折叠空白 / 限长是**给 Go 逻辑与窗口标题用的**；下发给设置页的 raw
// 保持磁盘原样（上一版的 loadSettingsFrom 不归一化 appName，值为准）。
func (s *Settings) AppName() string { return normalizeAppName(s.Str("", "appName")) }

// Autostart 开机自启动开关
func (s *Settings) Autostart() bool { return s.Flag(false, "autostart") }

// ---------- 时刻表 ----------

// TimeSlot 一条固定时刻：时刻 + 重复周期（1=周一 … 7=周日）
type TimeSlot struct {
	ID      string `json:"id"`
	Hour    int    `json:"hour"`
	Minute  int    `json:"minute"`
	Repeat  string `json:"repeat"`
	Days    []int  `json:"days"`
	Enabled bool   `json:"enabled"`
}

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
func normalizeTimeSlot(raw any) (TimeSlot, bool) {
	m, ok := raw.(map[string]any)
	if !ok {
		return TimeSlot{}, false
	}
	slot := TimeSlot{
		ID:      newIDIfEmpty(strOf(m["id"])),
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

func normalizeTimeSlots(raw any) []TimeSlot {
	arr, ok := raw.([]any)
	if !ok {
		return []TimeSlot{}
	}
	out := make([]TimeSlot, 0, len(arr))
	for _, it := range arr {
		if s, ok := normalizeTimeSlot(it); ok {
			out = append(out, s)
		}
	}
	return out
}

// slotsToAny 把归一化后的时刻表还原成 map 数组（落盘 / 下发用）
func slotsToAny(slots []TimeSlot) []any {
	arr := make([]any, 0, len(slots))
	for _, s := range slots {
		arr = append(arr, slotToMap(s))
	}
	return arr
}

func slotToMap(s TimeSlot) map[string]any {
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

func strOf(v any) string {
	s, _ := v.(string)
	return s
}

// jsStr 复刻 JS 的 String(v)：null/undefined → ""，数字不带多余小数，布尔转字面量。
// 配置里的字段可能被手改成品类不符的值，上一版侧大量用 String(x ?? ”) 兜住，
// 这里必须同口径，否则会出现「老配置里 name 是数字」这类边角差异。
func jsStr(v any) string {
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

// toNumber 复刻 JS 的 Number(v)：null/""/false → 0，字符串按数字解析，
// 解析不出来返回 ok=false（对应 NaN / Infinity，调用方按 fallback 处理）。
func toNumber(v any) (float64, bool) {
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

// anySlice 取数组；不是数组时返回 nil（等价于 JS 的 Array.isArray ? : []）
func anySlice(v any) []any {
	if arr, ok := v.([]any); ok {
		return arr
	}
	return nil
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
	return newUUID()
}

// newUUID 生成一枚 v4 UUID（与 crypto.randomUUID() 同形）
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("fallback-%d", os.Getpid())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// ============================================================================
// 归一化
// ============================================================================

// normalizeAppName 折叠空白、去首尾、限长 30（可以是空串 = 不显示名字）
var reSpaces = regexp.MustCompile(`\s+`)

func normalizeAppName(raw string) string {
	s := strings.TrimSpace(reSpaces.ReplaceAllString(raw, " "))
	return clipRunes(s, 30)
}

// normalizeBreakConfigOf 复刻 types.ts 的 normalizeBreakConfig：
// 返回 {...cfg, mode, slots} —— 模式只认两个合法值（旧配置一律按倒计时），时刻表统一夹紧。
// cfg 为 nil（JSON null）时等价于 {...null} = {}，只留 mode + slots。
func normalizeBreakConfigOf(cfg map[string]any) map[string]any {
	out := make(map[string]any, len(cfg)+2)
	for k, v := range cfg {
		out[k] = v
	}
	if s, _ := out["mode"].(string); s != "fixed" {
		out["mode"] = "countdown"
	}
	out["slots"] = slotsToAny(normalizeTimeSlots(out["slots"]))
	return out
}

// defaultCustomBreakConfig 自定义提醒的出厂配置（与 types.ts 的 defaultCustomBreakConfig 逐字一致）
func defaultCustomBreakConfig() map[string]any {
	return map[string]any{
		"enabled":        true,
		"mode":           "countdown",
		"intervalMs":     float64(10 * 60_000),
		"durationMs":     float64(20_000),
		"notifyBeforeMs": float64(10_000),
		"postponeMs":     float64(2 * 60_000),
		"slots":          []any{},
	}
}

// customBreakPrefix 自定义提醒 id 的前缀（与 types.ts 的 CUSTOM_BREAK_PREFIX 一致）
const customBreakPrefix = "cb-"

// builtinPrefix 预置条目的 id 前缀（与 config.ts 的 BUILTIN_PREFIX 一致）
const builtinPrefix = "builtin-"

// normalizeOrder 提醒内容显示方式：只认 fixed / random / sequence，其余回 fixed
func normalizeOrder(v any) string {
	if s, ok := v.(string); ok && (s == "fixed" || s == "random" || s == "sequence") {
		return s
	}
	return "fixed"
}

// normalizeMsgItems 归一化一组提醒内容条目：丢掉没有文本的、补 id、enabled 只认非 false。
// dropBuiltin 为真时顺带剔除 `builtin-` 前缀的历史种子条目（自定义提醒的列表默认空）。
func normalizeMsgItems(v any, dropBuiltin bool) []any {
	arr := anySlice(v)
	out := make([]any, 0, len(arr))
	for _, it := range arr {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		text, ok := m["text"].(string)
		if !ok {
			continue
		}
		id := ""
		if s, ok := m["id"].(string); ok {
			id = s
		}
		if id == "" {
			id = newUUID()
		}
		if dropBuiltin && strings.HasPrefix(id, builtinPrefix) {
			continue
		}
		out = append(out, map[string]any{
			"id": id, "text": text, "enabled": m["enabled"] != false,
		})
	}
	return out
}

// itemsHaveID 判断 id 是否指向列表里真实存在的一条（fixedId 的合法性只看这个）
func itemsHaveID(items []any, id string) bool {
	if id == "" {
		return false
	}
	for _, it := range items {
		if m, ok := it.(map[string]any); ok && jsStr(m["id"]) == id {
			return true
		}
	}
	return false
}

// normalizeCustomBreaks 自定义提醒归一化（对应 config.ts 的 normalizeCustomBreaks）：
// 丢弃非法条目、配置按短休息的默认值补齐、缺 id / id 重复的补一枚新 id、
// 剔除历史上种进去的 `builtin-` 条目。**整体替换** —— 重建后多余的键会被丢掉。
func normalizeCustomBreaks(raw map[string]any) {
	arr := anySlice(raw["customBreaks"])
	seen := map[string]bool{}
	out := make([]any, 0, len(arr))
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id := ""
		if s, ok := m["id"].(string); ok {
			id = strings.TrimSpace(s)
		}
		// 内置的两个身份不许被占用
		if id == "short" || id == "long" {
			id = ""
		}
		// 统一带上 cb- 前缀：渲染层直接拿 id 当 DOM 前缀用，缺了前缀就会和内置卡片的 id 撞车
		if id != "" && !strings.HasPrefix(id, customBreakPrefix) {
			id = customBreakPrefix + id
		}
		if id == "" || seen[id] {
			id = customBreakPrefix + newUUID()
		}
		seen[id] = true
		name := clipRunes(strings.TrimSpace(jsStr(m["name"])), 24)
		if name == "" {
			name = "自定义提醒"
		}
		msg, _ := m["messages"].(map[string]any)
		msgOrder := normalizeOrder(msg["order"])
		msgItems := normalizeMsgItems(msg["items"], true)
		msgFixedID := ""
		if s, ok := msg["fixedId"].(string); ok {
			msgFixedID = s
		}
		if msgOrder != "fixed" || !itemsHaveID(msgItems, msgFixedID) {
			msgFixedID = ""
		}
		// config：键缺失（undefined）用出厂配置；显式 null / 标量按 {...null} = {} 处理
		var cfg map[string]any
		if cv, has := m["config"]; !has {
			cfg = defaultCustomBreakConfig()
		} else if cm, ok := cv.(map[string]any); ok {
			cfg, _ = deepMerge(defaultCustomBreakConfig(), cm).(map[string]any)
		} else {
			cfg = map[string]any{}
		}
		out = append(out, map[string]any{
			"id":       id,
			"name":     name,
			"config":   normalizeBreakConfigOf(cfg),
			"messages": map[string]any{"order": msgOrder, "fixedId": msgFixedID, "items": msgItems},
		})
	}
	raw["customBreaks"] = out
}

// soundMusic 内置提示音（与 config.ts 的 BUILTIN_MUSIC 逐字一致）
type soundMusic struct{ id, file, name string }

var builtinMusic = []soundMusic{
	{"sm-builtin-short_start", "short_start.wav", "双音上行"},
	{"sm-builtin-short_end", "short_end.wav", "双音下行"},
	{"sm-builtin-long_start", "long_start.wav", "三音上行"},
	{"sm-builtin-long_end", "long_end.wav", "三音下行"},
}

var reWavExt = regexp.MustCompile(`(?i)\.wav$`)

// normalizeSound 声音配置归一化（对应 config.ts 的 normalizeSound）：播放方式只认
// single / random / sequence；音乐列表逐条夹紧、补 id、按文件名补显示名；预置 4 种
// 提示音只在 seeded 未置位时种入一次；singleId 必须指向列表里存在的条目，
// 失效时从废弃的 sound 字段迁移一次，再不行置空。
func normalizeSound(raw map[string]any) {
	snd, ok := raw["sound"].(map[string]any)
	if !ok {
		return
	}
	if s, _ := snd["mode"].(string); s != "random" && s != "sequence" {
		snd["mode"] = "single"
	}
	seen := map[string]bool{}
	music := []any{}
	for _, it := range anySlice(snd["music"]) {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		file, ok := m["file"].(string)
		if !ok || strings.TrimSpace(file) == "" {
			continue
		}
		id := ""
		if s, ok := m["id"].(string); ok {
			id = strings.TrimSpace(s)
		}
		if id == "" || seen[id] {
			id = "sm-" + newUUID()
		}
		seen[id] = true
		name := clipRunes(strings.TrimSpace(jsStr(m["name"])), 60)
		if name == "" {
			base := file
			if i := strings.LastIndexAny(base, "\\/"); i >= 0 {
				base = base[i+1:]
			}
			base = reWavExt.ReplaceAllString(base, "")
			if i := strings.LastIndex(base, "."); i > 0 {
				base = base[:i]
			}
			name = base
		}
		music = append(music, map[string]any{
			"id": id, "file": file, "name": name, "enabled": m["enabled"] != false,
		})
	}
	// 预置提示音只种一次：seeded 标记缺失（首次运行 / 老配置升级）时补进列表，
	// 之后的删除与停用都原样保留、不再回补
	if snd["seeded"] != true {
		have := map[string]bool{}
		for _, it := range music {
			have[jsStr(it.(map[string]any)["file"])] = true
		}
		missing := []any{}
		for _, b := range builtinMusic {
			if have[b.file] {
				continue
			}
			missing = append(missing, map[string]any{
				"id": b.id, "file": b.file, "name": b.name, "enabled": true,
			})
		}
		if len(missing) > 0 {
			music = append(missing, music...)
		}
		snd["music"] = music
		snd["seeded"] = true
	} else {
		snd["music"] = music
	}
	// 单曲循环播哪首：只要求条目存在（与启停无关）
	singleID := ""
	if s, ok := snd["singleId"].(string); ok {
		singleID = s
	}
	if !itemsHaveID(snd["music"].([]any), singleID) {
		legacy := ""
		if s, ok := snd["sound"].(string); ok {
			legacy = "sm-builtin-" + reWavExt.ReplaceAllString(s, "")
		}
		if legacy != "" && itemsHaveID(snd["music"].([]any), legacy) {
			snd["singleId"] = legacy
		} else {
			snd["singleId"] = ""
		}
	}
}

// normalizeTakeout 归一化整份外卖配置（对应 types.ts 的 normalizeTakeout）：
// **重建**对象，只留这 5 个键；leadMin 夹到 0–120、postponeMin 夹到 0–120
// （0 = 不显示「推迟」按钮，见 takeout.js 的显隐）。
func normalizeTakeout(raw map[string]any) map[string]any {
	var o map[string]any
	if m, ok := raw["takeout"].(map[string]any); ok {
		o = m
	}
	pick := func(v any, min, max, fallback float64) float64 {
		n, ok := toNumber(v)
		if !ok {
			return fallback
		}
		r := math.Round(n)
		if r < min {
			r = min
		}
		if r > max {
			r = max
		}
		return r
	}
	return map[string]any{
		"enabled":     o["enabled"] != false,
		"leadMin":     pick(o["leadMin"], 0, 120, 0),
		"postponeMin": pick(o["postponeMin"], 0, 120, 10),
		"redPacket":   o["redPacket"] == true,
		"slots":       slotsToAny(normalizeTimeSlots(o["slots"])),
	}
}

// appPausePreset 内置的常用远程会议软件（与 types.ts 的 APP_PAUSE_PRESETS 逐字一致）
type appPausePreset struct{ exe, label string }

var appPausePresets = []appPausePreset{
	{"WeMeetApp.exe", "腾讯会议"},
	{"Feishu.exe", "飞书"},
	{"DingTalk.exe", "钉钉"},
	{"WXWork.exe", "企业微信"},
	{"ms-teams.exe", "Microsoft Teams"},
	{"Zoom.exe", "Zoom"},
	{"CiscoCollabHost.exe", "Webex"},
	{"WeLink.exe", "华为云 WeLink"},
}

// appPauseLabel 内置清单里某个可执行文件名对应的软件名；不在清单里返回空串
func appPauseLabel(exe string) string {
	k := strings.ToLower(strings.TrimSpace(exe))
	for _, p := range appPausePresets {
		if strings.ToLower(p.exe) == k {
			return p.label
		}
	}
	return ""
}

// exeBasename 从用户输入或文件路径里取可执行文件名：剥掉目录、去空白、限长 120
func exeBasename(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == '\\' || r == '/' })
	if len(parts) == 0 {
		return ""
	}
	return clipRunes(strings.TrimSpace(parts[len(parts)-1]), 120)
}

// normalizeAppPause 归一化应用列表（对应 types.ts 的 normalizeAppPause）：
// 丢弃空项、按小写去重、补 id；**重建**对象，只留 enabled / seeded / items。
func normalizeAppPause(raw map[string]any) map[string]any {
	var o map[string]any
	if m, ok := raw["appPause"].(map[string]any); ok {
		o = m
	}
	seen := map[string]bool{}
	items := []any{}
	for _, it := range anySlice(o["items"]) {
		r, ok := it.(map[string]any)
		if !ok {
			continue
		}
		exe := exeBasename(strOf(r["exe"]))
		if exe == "" {
			continue
		}
		key := strings.ToLower(exe)
		if seen[key] {
			continue
		}
		seen[key] = true
		id := ""
		if s, ok := r["id"].(string); ok {
			id = s
		}
		if id == "" {
			id = newUUID()
		}
		// 老配置（本字段是后加的）里没有 name：内置清单命中的程序补上中文名，其余留空
		name := ""
		if s, ok := r["name"].(string); ok {
			name = normalizeAppName(s)
		} else {
			name = appPauseLabel(exe)
		}
		items = append(items, map[string]any{
			"id": id, "exe": exe, "name": name, "enabled": r["enabled"] != false,
		})
	}
	return map[string]any{
		"enabled": o["enabled"] == true,
		"seeded":  o["seeded"] == true,
		"items":   items,
	}
}

// seedAppPause 把内置的常用会议软件并入列表（只补缺、按进程名去重），并打上「已种过」标记
func seedAppPause(cfg map[string]any) map[string]any {
	have := map[string]bool{}
	items := anySlice(cfg["items"])
	for _, it := range items {
		if m, ok := it.(map[string]any); ok {
			have[strings.ToLower(strOf(m["exe"]))] = true
		}
	}
	out := make([]any, 0, len(items)+len(appPausePresets))
	out = append(out, items...)
	for _, p := range appPausePresets {
		key := strings.ToLower(p.exe)
		if have[key] {
			continue
		}
		have[key] = true
		out = append(out, map[string]any{
			"id": "preset-" + key, "exe": p.exe, "name": p.label, "enabled": true,
		})
	}
	return map[string]any{"enabled": cfg["enabled"] == true, "seeded": true, "items": out}
}

// normalizeToggles 开关类字段的归一化 + 迁移（与 config.ts 的 normalizeToggles 同义）
//
//   - 旧键 screens（休息窗口显示位置下拉框）→ allScreens 布尔值，然后删掉旧键；
//   - idlePause 开关已整体删除（空闲自动暂停现在始终启用），旧键当场删掉；
//   - allScreens 只认布尔值；
//   - idlePauseMin 只认 ≥1 的整数，非法回退 5；
//   - startMinimized 只认布尔值，非法回退 false。
func normalizeToggles(raw map[string]any) {
	if _, ok := raw["screens"]; ok {
		if _, isBool := raw["allScreens"].(bool); !isBool {
			raw["allScreens"] = raw["screens"] == "all"
		}
		delete(raw, "screens")
	}
	if _, ok := raw["allScreens"].(bool); !ok {
		raw["allScreens"] = true
	}
	if _, ok := raw["idlePause"]; ok {
		delete(raw, "idlePause")
	}
	if n, ok := toNumber(raw["idlePauseMin"]); ok && n >= 1 {
		raw["idlePauseMin"] = math.Round(n)
	} else {
		raw["idlePauseMin"] = float64(5)
	}
	if _, ok := raw["startMinimized"].(bool); !ok {
		raw["startMinimized"] = false
	}
}

// cleanLegacyKeys 清理已删除功能残留在配置里的旧键。
//
// deepMerge 只补默认值、不删旧键，用户升级上来的配置里还留着这些块 —— 不清理就会
// 一直躺在 settings.json 里（也会被设置页的「导入导出」带走）。
func cleanLegacyKeys(raw map[string]any) {
	// appearance.opacity 是「不透明度」语义（0.92=接近实底），与 UI 的「透明度」相反：
	// 换算 transparency = 1 - opacity 保持视觉不变，之后删掉旧键
	if ap, ok := raw["appearance"].(map[string]any); ok {
		if _, hasNew := ap["transparency"].(float64); !hasNew {
			if old, ok := ap["opacity"].(float64); ok {
				ap["transparency"] = math.Min(0.9, math.Max(0, 1-old))
			}
		}
		// 清理可能残留的旧键（如迁移中途混写的配置）
		if _, ok := ap["transparency"].(float64); ok {
			delete(ap, "opacity")
		}
	}
	// 「禁止跳过」功能已删除：清掉 short/long 里残留的 strict
	for _, kind := range []string{"short", "long"} {
		if m, ok := raw[kind].(map[string]any); ok {
			if _, ok := m["strict"]; ok {
				delete(m, "strict")
			}
		}
	}
	// 督休专员旧的「压迫强度」level 已取消
	if sup, ok := raw["supervisor"].(map[string]any); ok {
		if _, ok := sup["level"]; ok {
			delete(sup, "level")
		}
	}
	// 提示音统一：旧的四种音效字段（shortStart/shortEnd/longStart/longEnd）合并成单一 sound
	if snd, ok := raw["sound"].(map[string]any); ok {
		anyLegacy := false
		for _, k := range []string{"shortStart", "shortEnd", "longStart", "longEnd"} {
			if _, ok := snd[k].(string); ok {
				anyLegacy = true
			}
		}
		if anyLegacy {
			if v, ok := snd["shortStart"].(string); ok {
				snd["sound"] = v
			}
			for _, k := range []string{"shortStart", "shortEnd", "longStart", "longEnd"} {
				delete(snd, k)
			}
		}
	}
	// 「工作时段」功能已整体删除
	if _, ok := raw["workTime"]; ok {
		delete(raw, "workTime")
	}
}

// normalizeSupervisorMessages 督休专员配置归一化（与 config.ts 同义）：
// selection 只认固定 / 顺序 / 随机；开关类字段只认布尔；视线跟随恒为 true；
// rolesMigrated 置位后，把可能从导入配置带进来的旧督休专员字段直接清掉。
func normalizeSupervisorMessages(raw map[string]any) {
	sup, ok := raw["supervisor"].(map[string]any)
	if !ok {
		return
	}
	if s, _ := sup["selection"].(string); s != "sequence" && s != "random" {
		sup["selection"] = "fixed"
	}
	if _, ok := sup["fixedKey"].(string); !ok {
		sup["fixedKey"] = ""
	}
	if _, ok := sup["enabled"].(bool); !ok {
		sup["enabled"] = true
	}
	// 视线跟随已改为始终启用、不再可配置
	sup["gaze"] = true
	migrated := sup["rolesMigrated"] == true
	sup["rolesMigrated"] = migrated
	if migrated {
		for _, k := range []string{"roles", "geom", "messagesByRole", "rolesSeeded", "image", "builtin", "customs"} {
			if _, ok := sup[k]; ok {
				delete(sup, k)
			}
		}
	}
}

// normalizeReminderMessages 提醒内容迁移 + 归一化（对应 config.ts 的同名函数）：
// 旧版是全局一套 messages，现在短 / 长 / 外卖各自独立一套。旧条目统一归短休息，
// 显示方式沿用旧 mode，随后删掉旧键。每套配置**重建**成 order / fixedId / seeded / items。
func normalizeReminderMessages(raw map[string]any) {
	if legacy, ok := raw["messages"].(map[string]any); ok {
		items := []any{}
		for _, it := range anySlice(legacy["items"]) {
			m, ok := it.(map[string]any)
			if !ok {
				continue
			}
			text, ok := m["text"].(string)
			if !ok || strings.TrimSpace(text) == "" {
				continue
			}
			id := ""
			if s, ok := m["id"].(string); ok && s != "" {
				id = s
			}
			if id == "" {
				id = newUUID()
			}
			items = append(items, map[string]any{
				"id": id, "text": text, "enabled": m["enabled"] != false,
			})
		}
		order := "random"
		if s, _ := legacy["mode"].(string); s == "sequence" {
			order = "sequence"
		}
		raw["reminderMessages"] = map[string]any{
			"short":   map[string]any{"order": order, "items": items},
			"long":    map[string]any{"order": order, "items": []any{}},
			"takeout": map[string]any{"order": order, "items": []any{}},
		}
		delete(raw, "messages")
	}
	rm, ok := raw["reminderMessages"].(map[string]any)
	if !ok {
		def, _ := deepCopyValue(defaultSettingsMap()["reminderMessages"]).(map[string]any)
		if def == nil {
			def = map[string]any{}
		}
		rm = def
		raw["reminderMessages"] = rm
	}
	for _, kind := range []string{"short", "long", "takeout"} {
		cfg, _ := rm[kind].(map[string]any)
		order := normalizeOrder(cfg["order"])
		items := normalizeMsgItems(cfg["items"], false)
		fixedID := strOf(cfg["fixedId"])
		if order != "fixed" || !itemsHaveID(items, fixedID) {
			fixedID = ""
		}
		rm[kind] = map[string]any{
			"order": order, "fixedId": fixedID,
			"seeded": cfg["seeded"] == true, "items": items,
		}
	}
}

// seedPresetEntries 把预置提醒内容种进配置（幂等，返回是否有改动）。
//
// 只在 seeded 未置位时置位；此时 items 仍为空才种入预置条目（老配置已有升级带来的
// 条目 → 只置位、不动条目，避免同 id 重复行）。种入后就是普通条目，删空不再回补。
// 必须在 normalizeReminderMessages 之后调用（它重建了三个分支）。
func seedPresetEntries(raw map[string]any) bool {
	rm, ok := raw["reminderMessages"].(map[string]any)
	if !ok {
		return false
	}
	changed := false
	for _, kind := range []string{"short", "long", "takeout"} {
		cfg, ok := rm[kind].(map[string]any)
		if !ok {
			continue
		}
		if cfg["seeded"] == true {
			continue
		}
		cfg["seeded"] = true
		if items, ok := cfg["items"].([]any); !ok || len(items) == 0 {
			seeds := builtinKindItems(kind)
			arr := make([]any, 0, len(seeds))
			for _, it := range seeds {
				arr = append(arr, map[string]any{"id": it.ID, "text": it.Text, "enabled": it.Enabled})
			}
			cfg["items"] = arr
		}
		changed = true
	}
	return changed
}

// ensureInstallID 安装标识：首次运行生成一枚并持久化，之后读回原值、永不改变。
// 返回本次是否**新生成**了它（与 上一版的 installIdCreated 同义，决定要不要落盘）。
func ensureInstallID(raw map[string]any) bool {
	if s, ok := raw["installId"].(string); ok && strings.TrimSpace(s) != "" {
		return false
	}
	raw["installId"] = newUUID()
	return true
}

// normalizeLoad 加载路径的归一化，**顺序逐条对应 config.ts 的 loadSettingsFrom**。
//
// 返回值刻意与 上一版的 LoadedSettings 对齐：只有「补生成了 installId」或
// 「种入了预置条目」这两件事需要立刻落盘。其余归一化结果（旧键清理 / 越界夹紧）
// **不主动回写** —— 每次启动都改写用户配置会把键序和格式一起动掉，而且在
// 「验证脚本误指真实配置」时会留下副作用；这些清理结果会在用户下一次保存时一并落盘。
func normalizeLoad(raw map[string]any) (installIDCreated, presetSeeded bool) {
	// ① 安装标识（首次运行 / 老配置升级补一枚）
	installIDCreated = ensureInstallID(raw)
	// ② 旧键清理与迁移（必须在归一化之前：迁移写出来的值才是最终值）
	cleanLegacyKeys(raw)
	// ③ 休息触发方式与固定时刻表
	raw["short"] = normalizeBreakConfigOf(mapOf(raw["short"]))
	raw["long"] = normalizeBreakConfigOf(mapOf(raw["long"]))
	// ④ 声音
	normalizeSound(raw)
	// ⑤ 自定义提醒
	normalizeCustomBreaks(raw)
	// ⑥ 外卖提醒
	raw["takeout"] = normalizeTakeout(raw)
	// ⑦ 指定应用运行时暂停 + 首次种入内置会议软件
	raw["appPause"] = normalizeAppPause(raw)
	if raw["appPause"].(map[string]any)["seeded"] != true {
		raw["appPause"] = seedAppPause(raw["appPause"].(map[string]any))
	}
	// ⑧ 开关类字段（含旧的「休息窗口显示位置」下拉框迁移）
	normalizeToggles(raw)
	// ⑨ 提醒内容（含旧 messages 键迁移）
	normalizeReminderMessages(raw)
	// ⑩ 督休专员
	normalizeSupervisorMessages(raw)
	// ⑪ 预置提醒内容条目（幂等）
	presetSeeded = seedPresetEntries(raw)
	return installIDCreated, presetSeeded
}

// normalizeForSave 设置页保存 / 导入外来配置后要跑的归一化。
// 与加载路径的差别：**不做**旧键清理与首次种子（那些是一次性迁移，加载时已经做过），
// 顺序对应 main.ts 的 settings:save / settings:import。
func normalizeForSave(raw map[string]any) {
	normalizeReminderMessages(raw)
	normalizeSupervisorMessages(raw)
	raw["short"] = normalizeBreakConfigOf(mapOf(raw["short"]))
	raw["long"] = normalizeBreakConfigOf(mapOf(raw["long"]))
	normalizeCustomBreaks(raw)
	raw["takeout"] = normalizeTakeout(raw)
	normalizeSound(raw)
	raw["appPause"] = normalizeAppPause(raw)
	normalizeToggles(raw)
}

// mapOf 取子对象；缺失 / 类型不符时给一个空 map（对应 JS 里对 undefined 做 {...x}）
func mapOf(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

// ---------- 读写 ----------

// settingsCandidates 候选配置文件路径（按优先级）。
// 只有一个真实落点：exe 旁的 config/settings.json —— 数据与程序同目录，整目录
// 分发（打包 / 拷贝 / 删除）时配置跟着走。不再检查 %APPDATA%（老版本的历史目录
// 已放弃兼容，见 README「数据落点」）。
//
// 环境变量 WR_SETTINGS_PATH 可**独占**指定一份配置：验证脚本用它在隔离副本上跑
// 「保存 / 恢复默认 / 迁移」这些写路径，绝不碰用户的真实配置。
func settingsCandidates(resourcesRoot string) []string {
	if p := strings.TrimSpace(os.Getenv("WR_SETTINGS_PATH")); p != "" {
		return []string{p}
	}
	return []string{filepath.Join(resourcesRoot, "config", "settings.json")}
}

// loadSettings 读配置：找到第一份可解析的 settings.json 就用它，在出厂默认之上深合并
// （缺失字段不会整体退回默认，数组整体替换），再过一遍归一化与迁移。
//
// 与 上一版 loadSettingsFrom 的差异：督休专员目录迁移不在本函数内做（上一版 把迁移
// 塞进了 loadSettingsFrom），而是由 migrate_roles.go 在 App 启动时统一处理（一次性的 sha1
// 摘要目录迁移 + 旧 key 清理），见该文件与 README「督休专员」。
func loadSettings(resourcesRoot string) *Settings {
	base := defaultSettingsMap()
	for _, p := range settingsCandidates(resourcesRoot) {
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var disk any
		if err := json.Unmarshal(raw, &disk); err != nil {
			log.Printf("[settings] 解析失败，跳过 %s: %v", p, err)
			continue
		}
		merged, ok := deepMerge(base, disk).(map[string]any)
		if !ok || merged == nil {
			continue
		}
		installIDCreated, presetSeeded := normalizeLoad(merged)
		s := &Settings{raw: merged, path: p}
		// 只有「补生成 installId」或「种入预置条目」才回写（与 上一版的
		// firstRun||installIdCreated||presetSeeded 同口径）
		if installIDCreated || presetSeeded {
			if err := s.Save(); err != nil {
				log.Printf("[settings] 初始化结果落盘失败: %v", err)
			} else {
				log.Printf("[settings] 已回写初始化结果 %s（补 installId=%v 种入预置=%v）", p, installIDCreated, presetSeeded)
			}
		}
		return s
	}
	// 一份都读不到 = 首次运行：走与「读到文件」完全相同的归一化管线（补 installId、
	// 种入预置条目），并落盘 —— 与 上一版的 firstRun 分支一致
	fresh := defaultSettingsMap()
	installIDCreated, presetSeeded := normalizeLoad(fresh)
	s := &Settings{raw: fresh, path: ""}
	if installIDCreated || presetSeeded {
		if err := s.Save(); err != nil {
			log.Printf("[settings] 首次运行落盘失败: %v", err)
		}
	}
	return s
}

// Save 落盘：写到配置实际来源的文件；来源为空时按便携/安装形态推导一个目标路径。
// 用「先写临时文件再改名」保证不会写出半截 JSON（写一半被杀 = 配置全丢）。
func (s *Settings) Save() error {
	target := s.path
	if target == "" {
		// 隔离测试：指定了 WR_SETTINGS_PATH 就写到它那里（哪怕文件还不存在）
		if p := strings.TrimSpace(os.Getenv("WR_SETTINGS_PATH")); p != "" {
			target = p
		} else {
			// 首次运行无来源：与 loadSettings 的读取优先级一致，取唯一的候选
			// （= <运行目录>/config/settings.json），数据与程序同目录。
			cands := settingsCandidates(s.resourcesRootHint())
			target = cands[0]
		}
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.raw, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	s.path = target
	return nil
}

// resourcesRootHint 落盘目标在「还没有配置文件」时的推导依据。
// 由 App 在启动时注入（避免 Settings 反向依赖 App）。
var resourcesRootHintFn func() string

func (s *Settings) resourcesRootHint() string {
	if resourcesRootHintFn != nil {
		return resourcesRootHintFn()
	}
	return "."
}
