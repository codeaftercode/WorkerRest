package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"workerrest/internal/jsonx"
)

func normalizeRoleOrder(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
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

// normalizeSupervisorMessages 督休专员配置归一化（与 config.ts 同义）：
// selection 只认固定 / 顺序 / 随机；开关类字段只认布尔；视线跟随恒为 true。
func normalizeSupervisorMessages(raw map[string]any) {
	sup, ok := raw["supervisor"].(map[string]any)
	if !ok {
		return
	}
	if s, _ := sup["selection"].(string); s != "fixed" && s != "sequence" && s != "random" {
		sup["selection"] = "random"
	}
	if _, ok := sup["fixedRole"].(string); !ok {
		sup["fixedRole"] = ""
	}
	if _, ok := sup["enabled"].(bool); !ok {
		sup["enabled"] = true
	}
	// 视线跟随已改为始终启用、不再可配置
	sup["gaze"] = true
	// 候选专员展示顺序：拖动磁贴得到的专员名数组。类型不对就整键丢掉（回落到默认顺序）。
	if v, ok := sup["order"]; ok {
		if _, isArr := v.([]any); isArr {
			norm := make([]any, 0, len(v.([]any)))
			for _, s := range normalizeRoleOrder(v) {
				norm = append(norm, s)
			}
			sup["order"] = norm
		} else {
			delete(sup, "order")
		}
	}
}

// normalizeReminderMessages 提醒内容迁移 + 归一化（对应 config.ts 的同名函数）：
// 旧版是全局一套 messages，现在短 / 长 / 外卖各自独立一套。旧条目统一归短休息，
// 显示方式沿用旧 mode，随后删掉旧键。每套配置**重建**成 order / fixedId / seeded / items。
func normalizeReminderMessages(raw map[string]any) bool {
	renumbered := false
	if legacy, ok := raw["messages"].(map[string]any); ok {
		items := []any{}
		for _, it := range jsonx.AnySlice(legacy["items"]) {
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
				id = jsonx.NewRandomID()
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
		items, idRemap := normalizeMsgItems(cfg["items"])
		if len(idRemap) > 0 {
			renumbered = true
		}
		fixedID := jsonx.RemapMsgID(idRemap, jsonx.StrOf(cfg["fixedId"]))
		if order != "fixed" || !itemsHaveID(items, fixedID) {
			fixedID = ""
		}
		rm[kind] = map[string]any{
			"order": order, "fixedId": fixedID,
			"seeded": cfg["seeded"] == true, "items": items,
		}
	}
	return renumbered
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

// ensureConfigKey 配置固定键：首次运行生成一枚并持久化，之后读回原值、永不改变。
// 返回本次是否**新生成**了它（决定要不要落盘）。
func ensureConfigKey(raw map[string]any) bool {
	if s, ok := raw["installId"].(string); ok && strings.TrimSpace(s) != "" {
		return false
	}
	raw["installId"] = jsonx.NewRandomID()
	return true
}

// normalizeLoad 加载路径的归一化，**顺序逐条对应 config.ts 的 loadSettingsFrom**。
//
// 返回值刻意与 上一版的 LoadedSettings 对齐：只有「补生成了配置固定键」「种入了预置条目」
// 「提醒条目重编号」以及「摘掉了早期误种的应用暂停条目」这几件事需要立刻落盘。
// 其余归一化结果（旧键清理 / 越界夹紧）**不主动回写** —— 每次启动都改写用户配置会把键序
// 和格式一起动掉，而且在「验证脚本误指真实配置」时会留下副作用；这些清理结果会在用户
// 下一次保存时一并落盘。
func normalizeLoad(raw map[string]any) (configKeyCreated, presetSeeded, idsRenumbered, appsPruned bool) {
	// ① 配置固定键（首次运行 / 老配置升级补一枚）
	configKeyCreated = ensureConfigKey(raw)
	// ② 旧键清理与迁移（必须在归一化之前：迁移写出来的值才是最终值）
	cleanLegacyKeys(raw)
	// ③ 休息触发方式与固定时刻表
	raw["short"] = normalizeBreakConfigOf(mapOf(raw["short"]))
	raw["long"] = normalizeBreakConfigOf(mapOf(raw["long"]))
	// ④ 声音
	normalizeSound(raw)
	// ⑤ 自定义提醒
	idsRenumbered = normalizeCustomBreaks(raw)
	// ⑥ 外卖提醒
	raw["takeout"] = normalizeTakeout(raw)
	// ⑦ 指定应用运行时暂停：归一化 → 摘掉早期误种的综合办公套件 → 首次种入内置会议软件
	raw["appPause"], appsPruned = pruneSuitePresets(normalizeAppPause(raw))
	if raw["appPause"].(map[string]any)["seeded"] != true {
		raw["appPause"] = seedAppPause(raw["appPause"].(map[string]any))
	}
	// ⑧ 开关类字段（含旧的「休息窗口显示位置」下拉框迁移）
	normalizeToggles(raw)
	// ⑧·5 设置窗口尺寸（window 块，老配置没有就按出厂默认补一块）
	normalizeWindow(raw)
	// ⑨ 提醒内容（含旧 messages 键迁移）
	idsRenumbered = normalizeReminderMessages(raw) || idsRenumbered
	// ⑩ 督休专员
	normalizeSupervisorMessages(raw)
	// ⑪ 预置提醒内容条目（幂等）
	presetSeeded = seedPresetEntries(raw)
	return configKeyCreated, presetSeeded, idsRenumbered, appsPruned
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
	normalizeWindow(raw)
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
// 督休专员目录（supervisors/）不在本函数内扫描：它由 App 在启动时按需读，配置里只存
// 名字（fixedRole / order），见 README「督休专员」。
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
		configKeyCreated, presetSeeded, idsRenumbered, appsPruned := normalizeLoad(merged)
		s := &Settings{raw: merged, path: p}
		// 三种情况回写：补生成配置固定键、种入预置条目、提醒内容条目 id 被收敛成序号
		// （后者是一次性的格式统一，收敛完即为定值，不会每次启动都写盘）
		if configKeyCreated || presetSeeded || idsRenumbered || appsPruned {
			if err := s.Save(); err != nil {
				log.Printf("[settings] 初始化结果落盘失败: %v", err)
			} else {
				log.Printf("[settings] 已回写初始化结果 %s（补固定键=%v 种入预置=%v 条目重编号=%v 摘除套件预设=%v）",
					p, configKeyCreated, presetSeeded, idsRenumbered, appsPruned)
			}
		}
		return s
	}
	// 一份都读不到 = 首次运行：走与「读到文件」完全相同的归一化管线（补固定键、
	// 种入预置条目），并落盘 —— 与 上一版的 firstRun 分支一致
	fresh := defaultSettingsMap()
	configKeyCreated, presetSeeded, idsRenumbered, appsPruned := normalizeLoad(fresh)
	s := &Settings{raw: fresh, path: ""}
	if configKeyCreated || presetSeeded || idsRenumbered || appsPruned {
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
