package settings

import (
	"math"
	"regexp"
	"strings"
	"workerrest/internal/display"
	"workerrest/internal/jsonx"
)

// soundMusic 内置提示音（与 config.ts 的 BUILTIN_MUSIC 逐字一致）
type soundMusic struct{ id, file, name string }

var builtinMusic = []soundMusic{
	{"sm-builtin-short_start", "short_start.wav", "双音上行"},
	{"sm-builtin-short_end", "short_end.wav", "双音下行"},
	{"sm-builtin-long_start", "long_start.wav", "三音上行"},
	{"sm-builtin-long_end", "long_end.wav", "三音下行"},
}

// BuiltinSound 内置提示音的一条（id/file/name 与内置清单逐字一致）。
type BuiltinSound struct {
	ID   string
	File string
	Name string
}

// BuiltinSounds 内置提示音的完整清单（跨包导出，供 main 的随包目录扫描与测试复用）。
func BuiltinSounds() []BuiltinSound {
	out := make([]BuiltinSound, 0, len(builtinMusic))
	for _, b := range builtinMusic {
		out = append(out, BuiltinSound{ID: b.id, File: b.file, Name: b.name})
	}
	return out
}

// BuiltinSoundFiles 内置提示音的文件名（相对名，跨包导出，供 main 的随包目录扫描
// 判断「哪些文件名已登记」用）。
func BuiltinSoundFiles() []string {
	out := make([]string, 0, len(builtinMusic))
	for _, b := range builtinMusic {
		out = append(out, b.file)
	}
	return out
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
	for _, it := range jsonx.AnySlice(snd["music"]) {
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
			id = "sm-" + jsonx.NewRandomID()
		}
		seen[id] = true
		name := clipRunes(strings.TrimSpace(jsonx.JsStr(m["name"])), 60)
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
	// 预置提示音只种一次：seeded 标记缺失（首次运行）时补进列表，
	// 之后的删除与停用都原样保留、不再回补
	if snd["seeded"] != true {
		have := map[string]bool{}
		for _, it := range music {
			have[jsonx.JsStr(it.(map[string]any)["file"])] = true
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
		snd["singleId"] = ""
	}
	// singleId 兜底：上面校验后若仍为空（全新的音乐列表 / 被用户清空），且列表里
	// 至少有「启用」的条目，默认指向第一首启用的提示音——否则默认 single 模式下会「列表
	// 里有音、真休息却静音」（只有试听能响），与用户预期相悖。全部停用时尊重用户选择，静音。
	if jsonx.JsStr(snd["singleId"]) == "" {
		for _, it := range snd["music"].([]any) {
			if m, ok := it.(map[string]any); ok && m["enabled"] != false {
				if id := jsonx.JsStr(m["id"]); id != "" {
					snd["singleId"] = id
					break
				}
			}
		}
	}
}

// normalizeTakeout 归一化整份外卖配置（对应 types.ts 的 normalizeTakeout）：
// **重建**对象，只留这 5 个键；leadSec 夹到 0–300、postponeMin 夹到 0–120
// （0 = 不显示「推迟」按钮，见 takeout.js 的显隐）。
func normalizeTakeout(raw map[string]any) map[string]any {
	var o map[string]any
	if m, ok := raw["takeout"].(map[string]any); ok {
		o = m
	}
	pick := func(v any, min, max, fallback float64) float64 {
		n, ok := jsonx.ToNumber(v)
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
	// 提前通知时长（秒）
	leadVal := 0.0
	if v, ok := jsonx.ToNumber(o["leadSec"]); ok {
		leadVal = v
	}
	return map[string]any{
		"enabled":     o["enabled"] != false,
		"leadSec":     pick(leadVal, 0, 300, 0),
		"postponeMin": pick(o["postponeMin"], 0, 120, 10),
		// 暂停休息时的处置：三个合法值之外一律落回「推迟提醒」（默认）
		"pauseAction": normPauseAction(o["pauseAction"]),
		"slots":       slotsToAny(normalizeTimeSlots(o["slots"])),
	}
}

// NormalizeTakeout 归一化整份外卖配置（跨包导出，供测试与 main 包复用）。
func NormalizeTakeout(raw map[string]any) map[string]any { return normalizeTakeout(raw) }

// appPausePreset 内置的常用视频会议软件（与 web/dist/settings.js 的 APP_PAUSE_PRESETS 逐字一致）。
//
// **只收「打开它基本就是在开会」的客户端**。飞书 / 钉钉 / 企业微信 / WeLink 刻意不入列：
// 它们当然也能开会，但日常更多是看消息、批流程、写文档，拿「进程在不在」判断「正在开会」
// 会大面积误伤 —— 一整天挂着企业微信的人会被判成一直在开会，休息提醒永远不响。
// 清单之外的程序（含飞书 / 钉钉 / 企业微信 / WeLink）只能由用户手工添加。
type appPausePreset struct{ exe, label string }

var appPausePresets = []appPausePreset{
	{"WeMeetApp.exe", "腾讯会议"},
	{"ms-teams.exe", "Microsoft Teams"},
	{"Zoom.exe", "Zoom"},
	{"CiscoCollabHost.exe", "Webex"},
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
	for _, it := range jsonx.AnySlice(o["items"]) {
		r, ok := it.(map[string]any)
		if !ok {
			continue
		}
		exe := exeBasename(jsonx.StrOf(r["exe"]))
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
			id = jsonx.NewRandomID()
		}
		// 没填显示名时：内置清单命中的程序补上中文名，其余留空（界面上只显示进程名）
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
	items := jsonx.AnySlice(cfg["items"])
	for _, it := range items {
		if m, ok := it.(map[string]any); ok {
			have[strings.ToLower(jsonx.StrOf(m["exe"]))] = true
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

// normalizeToggles 开关类字段的归一化
//
//   - displayIds 只认非空字符串组成的数组；空数组**不等于「所有屏」**，
//     它表示「还没挑过」→ 加载时补成主屏 ID，运行时 Resolve 也兜底主屏；
//   - idlePauseMin 只认 ≥1 的整数，非法回退 5；
//   - startMinimized 只认布尔值，非法回退 false。
func normalizeToggles(raw map[string]any) {
	raw["displayIds"] = normalizeDisplayIDs(raw["displayIds"])
	if n, ok := jsonx.ToNumber(raw["idlePauseMin"]); ok && n >= 1 {
		raw["idlePauseMin"] = math.Round(n)
	} else {
		raw["idlePauseMin"] = float64(5)
	}
	if _, ok := raw["startMinimized"].(bool); !ok {
		raw["startMinimized"] = false
	}
}

// normalizeDisplayIDs 显示器列表只认非空字符串，去重、保序。
// 畸形值一律清成空数组（= 没配过，Resolve 兜底主屏），不留一个怪值让 Resolve 走别的分支。
func normalizeDisplayIDs(v any) []any {
	arr, ok := v.([]any)
	if !ok {
		return []any{}
	}
	out := make([]any, 0, len(arr))
	seen := make(map[string]bool, len(arr))
	for _, it := range arr {
		str, ok := it.(string)
		if !ok {
			continue
		}
		str = strings.TrimSpace(str)
		if str == "" || seen[str] {
			continue
		}
		seen[str] = true
		out = append(out, str)
	}
	return out
}

// fillPrimaryDisplayID 出厂 displayIds 是空数组（= 还没挑过屏）。启动加载时补一次：
// 枚举当前所有显示器，把主屏的稳定 ID 写进配置，并回报 true 让磁盘一起落盘。
//
// 为什么写「具体 ID」而不是某个「跟随主屏」的哨兵值：这一格在界面上就是「勾了哪几块屏」
// 的快照，写完配置与勾选一一对应，换接法后也不会自己漂到别的屏上。
//
// 枚举不可用（远程会话 / 无桌面）时保持空数组 —— 运行时 display.Resolve 照样兜底主屏，
// 行为不变，下次启动再补一次；不在这里写一个「看不见屏」时猜出来的怪值。
func fillPrimaryDisplayID(raw map[string]any) bool {
	if ids, ok := raw["displayIds"].([]any); ok && len(ids) > 0 {
		return false
	}
	list, err := display.List()
	if err != nil {
		return false
	}
	id := primaryIDOf(list)
	if id == "" {
		return false
	}
	raw["displayIds"] = []any{id}
	return true
}

// primaryIDOf 主屏的稳定 ID；一块都没标主屏（个别虚拟显示驱动）时退回第一块
// —— 与 display.Resolve 的 primaryOf 同口径，绝不因为「没标主屏」就返回空。
func primaryIDOf(list []display.Info) string {
	for _, d := range list {
		if d.IsPrimary && d.ID != "" {
			return d.ID
		}
	}
	for _, d := range list {
		if d.ID != "" {
			return d.ID
		}
	}
	return ""
}

// normalizeWindow 归一化「设置窗口尺寸」块（window）。
//
// 逐条对应用户可能手改出来的坏值：
//   - width / height 夹进 [最小尺寸, 上限]，非法（缺失 / 非数字 / 越界）回落实测默认值。
//     比最小尺寸还小的窗口会把卡片里的字段行横向裁掉，这种值不能交给用户去排查；
//   - lastWidth / lastHeight 只认「两个都 ≥ 最小尺寸」的一对，否则整对归零
//     （0 = 还没记录过，打开时回落到默认尺寸）。一边合法一边 0 是最坑的：
//     拿它去开窗会得到一扇高度 0 的窗；
//   - rememberSize 只认布尔，缺省 true（出厂就是「自动记录」）。
func normalizeWindow(raw map[string]any) {
	cfg, ok := raw["window"].(map[string]any)
	if !ok {
		cfg = map[string]any{}
		raw["window"] = cfg
	}
	cfg["width"] = float64(clampInt(mapInt(cfg, "width", SettingsWindowDefaultWidth), SettingsWindowMinWidth, SettingsWindowMaxWidth))
	cfg["height"] = float64(clampInt(mapInt(cfg, "height", SettingsWindowDefaultHeight), SettingsWindowMinHeight, SettingsWindowMaxHeight))
	lw, lh := int(mapInt(cfg, "lastWidth", 0)), int(mapInt(cfg, "lastHeight", 0))
	if validWindowSize(lw, lh) {
		cfg["lastWidth"] = float64(lw)
		cfg["lastHeight"] = float64(lh)
	} else {
		cfg["lastWidth"] = float64(0)
		cfg["lastHeight"] = float64(0)
	}
	if _, ok := cfg["rememberSize"].(bool); !ok {
		cfg["rememberSize"] = true
	}
}

// NormalizeWindow 归一化「设置窗口尺寸」块（跨包导出，供测试复用）。
func NormalizeWindow(raw map[string]any) { normalizeWindow(raw) }

// normalizeRoleOrder 归一化候选专员顺序：只保留非空字符串项并去重。
// 类型不是数组（手改坏了 / 导入配置带了别的结构）时返回 nil ——
// 顺序是**派生**数据，坏了就回落到内置清单顺序，绝不让程序起不来。
