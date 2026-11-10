// Package settings 是配置域：整份 map[string]any 的读写、deepMerge、类型归一、落盘。
//
// 设计要点：配置永远当作一整份 JSON 对象处理（不做逐字段强类型 struct 映射），读取时
// 通过一组带兜底的方法取值 —— 键缺失、类型被手改坏、老版本残留键都不会让程序崩，只会
// 回落到出厂默认。出厂默认内嵌在本包的 default_settings.json（全仓唯一源）。
//
// 写入策略：先写 .tmp 再 Rename，避免写一半断电留下半个 JSON。本包不持有 App 状态，
// 也不决定“改动什么时候生效”——那是 cmd 层的事。
package settings

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"workerrest/internal/jsonx"
	"workerrest/internal/model"
	"workerrest/internal/urltls"
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

// DefaultSettingsJSON 内嵌出厂默认（跨包导出，供 main 的 --export-default-settings 落盘）。
func DefaultSettingsJSON() []byte { return defaultSettingsJSON }

// ---------- 从配置里读出来的类型化结构（字段名与磁盘 JSON 逐字一致） ----------

// 暂停休息期间到点的处置（配置键 pauseAction，缺省 PauseActionPostpone）。
// 自定义提醒存在 config.pauseAction 里，外卖提醒存在 takeout.pauseAction 里；
// 短 / 长休息不看这个键 —— 暂停期间一律不提醒、也不记账。
const (
	// PauseActionRemind 照常提醒：提前提示小窗与提醒窗口照旧弹
	PauseActionRemind = "remind"
	// PauseActionPostpone 推迟提醒：按该提醒的“可推迟”时长往后推，推完仍暂停就再推（默认）
	PauseActionPostpone = "postpone"
	// PauseActionSkip 跳过提醒：不弹任何窗，直接按“跳过”记账
	PauseActionSkip = "skip"
)

// normPauseAction 只认三个合法值，其余（缺失 / 类型不符 / 手改坏）一律按默认“推迟提醒”。
func normPauseAction(v any) string {
	if s, ok := v.(string); ok {
		switch s {
		case PauseActionRemind, PauseActionPostpone, PauseActionSkip:
			return s
		}
	}
	return PauseActionPostpone
}

// Settings 一份配置：raw 是完整的配置对象，path 是它实际来自的文件（空 = 全用出厂默认）
type Settings struct {
	raw  map[string]any
	path string
}

// New 构造一份配置（raw 是完整配置对象，path 是它实际来自的文件，空 = 全用出厂默认）。
// 供跨包调用方（main 的绑定层）在已有 raw/path 时直接装配，避免暴露私有字段。
func New(raw map[string]any, path string) *Settings {
	return &Settings{raw: raw, path: path}
}

// SetPath 覆盖配置来源路径（供测试与装配场景：把一份内存配置指到指定落点后再 Save）。
// 生产路径走 New / Load 即可，一般无需调用。
func (s *Settings) SetPath(p string) { s.path = p }

// defaultSettingsMap 出厂默认（解析内嵌的生成产物；解析不了说明生成物坏了，直接崩）
func defaultSettingsMap() map[string]any {
	var m map[string]any
	if err := json.Unmarshal(defaultSettingsJSON, &m); err != nil {
		panic(fmt.Sprintf("default_settings.json 无法解析（内嵌的出厂默认坏了）: %v", err))
	}
	return m
}

// DefaultSettingsMap 出厂默认（跨包导出，供 main 的恢复默认等复用）。
func DefaultSettingsMap() map[string]any { return defaultSettingsMap() }

// isPlainObject 与 config.ts 的 isPlainObject 同义：数组与 nil 都不算“普通对象”
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
// 不能悄悄换成默认值。要区分“键缺失”与“键是 null”的调用点自己先做存在性判断。
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

// DeepMerge 深合并（跨包导出，供 main 的托盘开关 / 窗口尺寸落盘复用）。
func DeepMerge(base, over any) any { return deepMerge(base, over) }

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

// StrSlice 取字符串数组（只保留字符串项，空串与重复项剔除）。
// 键不存在 / 类型不是数组时返回 nil —— 调用方据此回落到默认顺序。
func (s *Settings) StrSlice(keys ...string) []string {
	return normalizeRoleOrder(s.lookup(keys...))
}

// Raw 整份配置（给 settings:get 下发，也用于落盘）
func (s *Settings) Raw() map[string]any { return s.raw }

// Path 配置来自哪个文件（空 = 全用出厂默认，尚未落盘）
func (s *Settings) Path() string { return s.path }

// LastUpdateCheck 最近一次自动/手动检查更新落到的日期（YYYY-MM-DD），空 = 从未检查。
// 用于“自动检查更新”的当日去重：同一天已查过就不再查。
func (s *Settings) LastUpdateCheck() string {
	return s.Str("", "lastUpdateCheck")
}

// SetLastUpdateCheck 记录一次检查发生的日期（仅当非空时写入 raw，由调用方 Save 落盘）。
func (s *Settings) SetLastUpdateCheck(date string) {
	if date == "" {
		return
	}
	s.raw["lastUpdateCheck"] = date
}

// IgnoredVersion 被用户“忽略本版本”的版本号；空 = 没有忽略任何版本。
// 自动弹窗时若 latest 落在 ignoredVersion 上则抑制弹窗，但“立即更新”按钮仍可显示。
func (s *Settings) IgnoredVersion() string {
	return s.Str("", "ignoredVersion")
}

// SetIgnoredVersion 设置/清除忽略版本（"" 表示清除忽略，之后任何新版本都会再次弹窗）。
func (s *Settings) SetIgnoredVersion(v string) {
	if v == "" {
		delete(s.raw, "ignoredVersion")
		return
	}
	s.raw["ignoredVersion"] = v
}

// LastUpdateResult 持久化的“最近一次检查结果”（形状与 updateCheckResult.toMap 一致）。
// 用于让“发现新版本但未更新”的状态在重启后仍然显示，直到更新真正成功才清除。
// 空/nil = 没有待展示的更新结果。
func (s *Settings) LastUpdateResult() map[string]any {
	return s.Map("lastUpdateResult")
}

// SetLastUpdateResult 写入/清除最近一次检查结果（nil 表示清除）。
func (s *Settings) SetLastUpdateResult(m map[string]any) {
	if m == nil {
		delete(s.raw, "lastUpdateResult")
		return
	}
	s.raw["lastUpdateResult"] = m
}

// ---------- 类型化视图（只覆盖 Go 逻辑真正要用的字段） ----------

// Break 某种休息的配置（kind = "short" | "long"）
func (s *Settings) Break(kind string) model.BreakConfig {
	return breakConfigOf(s.Map(kind))
}

// breakConfigOf 从一份休息配置对象读出类型化配置（缺字段用与 types.ts 同口径的默认值）。
// 内置短/长与自定义提醒共用它 —— 自定义提醒的卡片是 {id,name,config,messages}，
// 配置本体在 config 子对象里，取值口径与短休息完全一致。
func breakConfigOf(m map[string]any) model.BreakConfig {
	cfg := model.BreakConfig{
		Enabled:        true,
		Mode:           "countdown",
		DurationMs:     20_000,
		IntervalMs:     600_000,
		NotifyBeforeMs: 10_000,
		PostponeMs:     120_000,
		// 缺省：暂停休息期间到点按“推迟提醒”处理（推“可推迟”时长，仍暂停就继续推）
		PauseAction: PauseActionPostpone,
		Slots:       []model.TimeSlot{},
	}
	if m == nil {
		return cfg
	}
	cfg.Enabled = mapFlag(m, "enabled", true)
	cfg.Mode = normBreakMode(mapStr(m, "mode", "countdown"))
	cfg.DurationMs = mapInt(m, "durationMs", 20_000)
	cfg.IntervalMs = mapInt(m, "intervalMs", 600_000)
	cfg.NotifyBeforeMs = mapInt(m, "notifyBeforeMs", 10_000)
	// “推迟时长”最小 1 分钟（0 分钟等于原地重弹，2026-11-10 起不再支持）、最多 120 分钟
	cfg.PostponeMs = clampInt64(mapInt(m, "postponeMs", 120_000), 60_000, 120*60_000)
	cfg.PauseAction = normPauseAction(m["pauseAction"])
	// 空闲门：默认不勾、默认 30 秒。时长上限 30 分钟 —— 再长就没有“离开工位顺手休息”
	// 的意思了，那种需求该用「暂停休息」表达。
	cfg.IdleGate = mapFlag(m, "idleGate", false)
	cfg.IdleGateMs = clampInt64(mapInt(m, "idleGateMs", idleGateDefaultMs), 0, idleGateMaxMs)
	cfg.Slots = normalizeTimeSlots(m["slots"])
	return cfg
}

// 空闲门时长（毫秒）：出厂 30 秒，上限 30 分钟。
const (
	idleGateDefaultMs int64 = 30_000
	idleGateMaxMs     int64 = 30 * 60_000
)

// IdleGateDefaultMs 空闲门出厂时长（设置页新增卡片 / 控件默认值共用）
func IdleGateDefaultMs() int64 { return idleGateDefaultMs }

// IdleGateMaxMs 空闲门时长上限（设置页输入框 max 与之同源）
func IdleGateMaxMs() int64 { return idleGateMaxMs }

// BreakConfigOf 从一份休息配置对象读出类型化配置（跨包导出，供 main 的时间线计算复用）。
func BreakConfigOf(m map[string]any) model.BreakConfig { return breakConfigOf(m) }

// normBreakMode 计时方法只认两个合法值，其余一律按倒计时（旧配置 mode 非法时如此）
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

// clampInt64 把毫秒级数值钳进区间（int64 版的 clampInt，后者只接 int）
func clampInt64(v, min, max int64) int64 {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

// SupervisorCfg 督休专员显示开关（督休专员本体在 supervisors/<名称>/meta.json，名字就是身份）
func (s *Settings) SupervisorCfg() model.SupervisorConfig {
	return model.SupervisorConfig{
		Enabled:   s.Flag(true, "supervisor", "enabled"),
		Gaze:      s.Flag(true, "supervisor", "gaze"),
		Selection: s.normSelection(s.Str("random", "supervisor", "selection")),
		FixedRole: s.Str("", "supervisor", "fixedRole"),
		Order:     s.StrSlice("supervisor", "order"),
	}
}

func (s *Settings) normSelection(v string) string {
	switch v {
	case "fixed", "sequence", "random":
		return v
	default:
		return "random"
	}
}

// AppearanceCfg 提醒窗口外观。这里的兜底值与 default_settings.json 的 appearance 块
// **必须同值**（用户 2026-10-29 定的出厂默认：透明 0.05、宽高 95%、全屏关）——
// 两处漂移时，老配置（缺键走兜底）与恢复默认后的表现就不一样，界面上看不出来。
func (s *Settings) AppearanceCfg() model.AppearanceConfig {
	return model.AppearanceConfig{
		Color:        s.Str("#4f7cff", "appearance", "color"),
		Transparency: math.Min(0.5, math.Max(0, s.Num(0.05, "appearance", "transparency"))),
		Blur:         s.Flag(true, "appearance", "blur"),
		Fullscreen:   s.Flag(false, "appearance", "fullscreen"),
		AlwaysOnTop:  s.Flag(true, "appearance", "alwaysOnTop"),
		WidthPct:     s.Num(95, "appearance", "widthPct"),
		HeightPct:    s.Num(95, "appearance", "heightPct"),
		HideOnShare:  s.Flag(true, "appearance", "hideOnShare"),
	}
}

// AppearanceView 下发给渲染层的外观结构
func (s *Settings) AppearanceView() model.Appearance {
	cfg := s.AppearanceCfg()
	return model.Appearance{
		Color:        cfg.Color,
		Transparency: cfg.Transparency,
		Blur:         cfg.Blur,
		Fullscreen:   cfg.Fullscreen,
	}
}

// Reminders 某类提醒的提醒内容池（kind = "short" | "long" | "takeout"）
func (s *Settings) Reminders(kind string) model.ReminderMessages {
	return remindersOf(s.Map("reminderMessages", kind))
}

// RemindersByID 按休息身份取提醒内容池：自定义提醒取它自己的那一套，短 / 长 / 外卖
// 取共用的 reminderMessages 表。与 types.ts 的 breakMessagesById 同口径。
func (s *Settings) RemindersByID(id string) model.ReminderMessages {
	if cb := s.CustomBreakByID(id); cb != nil {
		return remindersOf(jsonx.AsMap(cb["messages"]))
	}
	kind := "short"
	if id == "long" || id == "takeout" {
		kind = id
	}
	return s.Reminders(kind)
}

// remindersOf 从一份提醒内容对象读出类型化结构（items 里非对象项直接丢弃）
func remindersOf(m map[string]any) model.ReminderMessages {
	out := model.ReminderMessages{
		Order:   "fixed",
		FixedID: "",
		Items:   []model.ReminderMessageItem{},
	}
	if m == nil {
		return out
	}
	out.Order = mapStr(m, "order", "fixed")
	out.FixedID = mapStr(m, "fixedId", "")
	for _, it := range jsonx.AnySlice(m["items"]) {
		im, ok := it.(map[string]any)
		if !ok {
			continue
		}
		out.Items = append(out.Items, model.ReminderMessageItem{
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
func (s *Settings) TakeoutSlots() []model.TimeSlot {
	return normalizeTimeSlots(s.lookup("takeout", "slots"))
}

// DisplayIDs 提醒窗口要显示在哪几块屏上（休息窗口与外卖窗口共用同一策略）。
//
// 语义见 internal/display 的 Resolve：**空数组 = 还没挑过屏**，运行时兜底主屏
// （绝不是“所有屏”）；非空时就是 display.Info.ID 的具体字符串。
// 出厂配置就是空数组，首次启动时由 fillPrimaryDisplayID 枚举一遍、把主屏的稳定 ID
// 写进配置，用户打开设置页看到的就是“主屏已勾”。这里只做“取出来 + 去空”，
// 真正的解析（含拔屏兜底）在 display 包。
func (s *Settings) DisplayIDs() []string {
	arr, ok := s.lookup("displayIds").([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, it := range arr {
		if str, ok := it.(string); ok && str != "" {
			out = append(out, str)
		}
	}
	return out
}

// StartMinimized 启动时最小化到托盘（不显示设置窗口，托盘图标在，点一下就出来）
func (s *Settings) StartMinimized() bool { return s.Flag(false, "startMinimized") }

// AppName 软件名（空串合法 = 不显示名字）。
// 注意：这里做折叠空白 / 限长是**给 Go 逻辑与窗口标题用的**；下发给设置页的 raw
// 保持磁盘原样（上一版的 loadSettingsFrom 不归一化 appName，值为准）。
func (s *Settings) AppName() string { return normalizeAppName(s.Str("", "appName")) }

// Autostart 开机自启动开关
func (s *Settings) Autostart() bool { return s.Flag(false, "autostart") }

// UpdateSites 更新源地址列表：每一项都指向一份远程 version.json。
// **只保留 https**（http 明文源已禁用，回环地址例外，仅为单测的 httptest 保留，
// 判定见 update_trust.go 的 checkUpdateURL）；其余（空串 / 非法 scheme / 非数组）
// 一律丢弃，返回 nil 表示“未配置任何更新源”。供 update.go 依次容错拉取版本信息用。
func (s *Settings) UpdateSites() []string {
	arr, ok := s.lookup("sites").([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, it := range arr {
		str, ok := it.(string)
		if !ok {
			continue
		}
		str = strings.TrimSpace(str)
		if str == "" {
			continue
		}
		// 用与下载/清单同一套判定，避免“配置层放行、下载层拒绝”的两套口径。
		if err := urltls.CheckUpdateURL(str); err != nil {
			continue
		}
		out = append(out, str)
	}
	return out
}

// SetSites 直接覆盖本地更新源列表（远程 version.json 下发的 sites 会走这里替换本地）。
// 调用方应只在远程确实返回了非空 sites 时才调用，避免把本地更新源误清空。
// 写入前再过滤一次协议：远程下发值等同于不可信输入，这层是“万一上游漏了”的兜底。
func (s *Settings) SetSites(sites []string) {
	raw := make([]any, 0, len(sites))
	for _, it := range sites {
		it = strings.TrimSpace(it)
		if it == "" || urltls.CheckUpdateURL(it) != nil {
			continue
		}
		raw = append(raw, it)
	}
	s.raw["sites"] = raw
}
