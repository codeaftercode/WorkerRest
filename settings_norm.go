package main

import (
	"math"
	"regexp"
	"strings"
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
	// 预置提示音只种一次：seeded 标记缺失（首次运行 / 老配置升级）时补进列表，
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
	// singleId 兜底：上面校验后若仍为空（全新安装 / 老配置升级 / 被用户清空），且列表里
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
//
// 提前通知单位由「分钟」改为「秒」：旧配置里若为分钟（leadMin）则 ×60 换算成秒，保留用户意图。
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
	// 提前通知：新字段 leadSec（秒）；旧配置 leadMin（分）按比例换算成秒，保留用户意图
	leadVal := 0.0
	if v, ok := jsonx.ToNumber(o["leadSec"]); ok {
		leadVal = v
	} else if v, ok := jsonx.ToNumber(o["leadMin"]); ok {
		leadVal = v * 60
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

// appPausePreset 内置的常用视频会议软件（与 frontend/dist/settings.js 的 APP_PAUSE_PRESETS 逐字一致）。
//
// **只收「打开它基本就是在开会」的客户端**。飞书 / 钉钉 / 企业微信 / WeLink 刻意不入列：
// 它们当然也能开会，但日常更多是看消息、批流程、写文档，拿「进程在不在」判断「正在开会」
// 会大面积误伤 —— 一整天挂着企业微信的人会被判成一直在开会，休息提醒永远不响。
// 2026-10-29 按这个口径把四个综合办公套件从清单里摘掉（老配置里的残留见 pruneSuitePresets）。
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

// suitePresetExes 早期版本误种进「应用暂停」清单的综合办公套件（小写可执行文件名）。
//
// 它们能开会，但日常是消息 / 审批 / 文档入口，用它判断「正在开会」会把一整天挂着
// 企业微信的人判成全程在会。现在清单只留以会议为主的客户端，这几个老条目由
// pruneSuitePresets 从存量配置里摘掉。
var suitePresetExes = map[string]bool{
	"feishu.exe":   true,
	"dingtalk.exe": true,
	"wxwork.exe":   true,
	"welink.exe":   true,
}

// pruneSuitePresets 把早期版本自动种入的综合办公套件从 items 里摘掉（幂等、一次性纠正）。
//
// 只动「软件自己种的」那几条：id 必须恰好是 preset-<小写 exe>，且 exe 没被改过。
// 用户手工添加的条目 id 是随机串，改过进程名的 id 也对不上 —— 那两种情况是用户的决定，
// 一律保留。第二个返回值表示"真的摘掉了"，调用方据此触发一次落盘，免得磁盘上一直留着
// 用户手翻 json 就能看见的残留。
func pruneSuitePresets(cfg map[string]any) (map[string]any, bool) {
	items := jsonx.AnySlice(cfg["items"])
	if len(items) == 0 {
		return cfg, false
	}
	out := make([]any, 0, len(items))
	removed := 0
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			out = append(out, it)
			continue
		}
		exe := strings.ToLower(strings.TrimSpace(jsonx.StrOf(m["exe"])))
		if suitePresetExes[exe] && strings.EqualFold(strings.TrimSpace(jsonx.StrOf(m["id"])), "preset-"+exe) {
			removed++
			continue
		}
		out = append(out, it)
	}
	if removed == 0 {
		return cfg, false
	}
	next := make(map[string]any, len(cfg))
	for k, v := range cfg {
		next[k] = v
	}
	next["items"] = out
	return next, true
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
	if n, ok := jsonx.ToNumber(raw["idlePauseMin"]); ok && n >= 1 {
		raw["idlePauseMin"] = math.Round(n)
	} else {
		raw["idlePauseMin"] = float64(5)
	}
	if _, ok := raw["startMinimized"].(bool); !ok {
		raw["startMinimized"] = false
	}
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
	cfg["width"] = float64(clampInt(mapInt(cfg, "width", settingsWindowDefaultWidth), settingsWindowMinWidth, settingsWindowMaxWidth))
	cfg["height"] = float64(clampInt(mapInt(cfg, "height", settingsWindowDefaultHeight), settingsWindowMinHeight, settingsWindowMaxHeight))
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
				ap["transparency"] = math.Min(0.5, math.Max(0, 1-old))
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

// normalizeRoleOrder 归一化候选专员顺序：只保留非空字符串项并去重。
// 类型不是数组（老配置手改坏了 / 导入配置带了别的结构）时返回 nil ——
// 顺序是**派生**数据，坏了就回落到内置清单顺序，绝不让程序起不来。
