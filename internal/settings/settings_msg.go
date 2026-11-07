package settings

import (
	"strings"

	"workerrest/internal/jsonx"
)

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
	// 暂停休息时的处置：值非法就落回默认；旧键 skipWhenPaused（内测版）顺手清掉。
	// 只在原本就有这个键时补写 —— 短 / 长休息不写它（它们不受这个开关影响）。
	if _, has := out["pauseAction"]; has {
		out["pauseAction"] = normPauseAction(out["pauseAction"])
	}
	delete(out, "skipWhenPaused")
	out["slots"] = slotsToAny(normalizeTimeSlots(out["slots"]))
	return out
}

// NormalizeBreakConfigOf 复刻 types.ts 的 normalizeBreakConfig（跨包导出，供测试与
// main 包复用）。
func NormalizeBreakConfigOf(cfg map[string]any) map[string]any { return normalizeBreakConfigOf(cfg) }

// defaultCustomBreakConfig 自定义提醒的出厂配置（与 types.ts 的 defaultCustomBreakConfig 逐字一致）
func defaultCustomBreakConfig() map[string]any {
	return map[string]any{
		"enabled":        true,
		"mode":           "countdown",
		"intervalMs":     float64(10 * 60_000),
		"durationMs":     float64(20_000),
		"notifyBeforeMs": float64(10_000),
		"postponeMs":     float64(2 * 60_000),
		"pauseAction":    PauseActionPostpone,
		"slots":          []any{},
	}
}

// DefaultCustomBreakConfig 自定义提醒的出厂配置（跨包导出）。
func DefaultCustomBreakConfig() map[string]any { return defaultCustomBreakConfig() }

// customBreakPrefix 自定义提醒 id 的前缀（与 types.ts 的 CUSTOM_BREAK_PREFIX 一致）
const customBreakPrefix = "cb-"

// CustomBreakPrefix 自定义提醒 id 前缀（跨包导出，供 main 的调度器判断身份用）。
const CustomBreakPrefix = customBreakPrefix

// normalizeOrder 提醒内容显示方式：只认 fixed / random / sequence，其余回 fixed
func normalizeOrder(v any) string {
	if s, ok := v.(string); ok && (s == "fixed" || s == "random" || s == "sequence") {
		return s
	}
	return "fixed"
}

// normalizeMsgItems 归一化一组提醒内容条目：丢掉没有文本的、把 id 收敛成列表内序号、
// enabled 只认非 false。返回的 remap 是“旧 id → 新 id”（只含真被改过的），
// 调用方必须拿它把 fixedId 搬到新号上，否则固定模式会指空。
func normalizeMsgItems(v any) ([]any, map[string]string) {
	arr := jsonx.AnySlice(v)
	out := make([]any, 0, len(arr))
	ids := make([]string, 0, len(arr))
	seen := make(map[string]bool, len(arr))
	remap := make(map[string]string, len(arr))
	for _, it := range arr {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		text, ok := m["text"].(string)
		if !ok {
			continue
		}
		old := jsonx.ItemID(m["id"])
		id := jsonx.SeqIDFor(old, ids, seen)
		if old != "" && old != id && !seen[old] {
			remap[old] = id
		}
		seen[id] = true
		ids = append(ids, id)
		out = append(out, map[string]any{
			"id": id, "text": text, "enabled": m["enabled"] != false,
		})
	}
	return out, remap
}

// itemsHaveID 判断 id 是否指向列表里真实存在的一条（fixedId 的合法性只看这个）
func itemsHaveID(items []any, id string) bool {
	if id == "" {
		return false
	}
	for _, it := range items {
		if m, ok := it.(map[string]any); ok && jsonx.JsStr(m["id"]) == id {
			return true
		}
	}
	return false
}

// normalizeCustomBreaks 自定义提醒归一化（对应 config.ts 的 normalizeCustomBreaks）：
// 丢弃非法条目、配置按短休息的默认值补齐、缺 id / id 重复的补一枚新 id。**整体替换** ——
// 重建后多余的键会被丢掉。返回“有提醒内容条目被重编号”（调用方据此决定要不要回写）。
func normalizeCustomBreaks(raw map[string]any) bool {
	arr := jsonx.AnySlice(raw["customBreaks"])
	seen := map[string]bool{}
	renumbered := false
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
			id = customBreakPrefix + jsonx.NewRandomID()
		}
		seen[id] = true
		name := clipRunes(strings.TrimSpace(jsonx.JsStr(m["name"])), 24)
		if name == "" {
			name = "自定义提醒"
		}
		msg, _ := m["messages"].(map[string]any)
		msgOrder := normalizeOrder(msg["order"])
		msgItems, idRemap := normalizeMsgItems(msg["items"])
		if len(idRemap) > 0 {
			renumbered = true
		}
		msgFixedID := jsonx.RemapMsgID(idRemap, jsonx.ItemID(msg["fixedId"]))
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
	return renumbered
}
