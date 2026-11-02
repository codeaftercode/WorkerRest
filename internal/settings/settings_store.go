package settings

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

// NormalizeRoleOrder 归一化候选专员顺序（跨包导出，供 main 的会话构建复用：
// 表单传来的 order 也走同一套「只保留非空字符串项并去重」的口径）。
func NormalizeRoleOrder(v any) []string { return normalizeRoleOrder(v) }

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

// NormalizeSupervisorMessages 督休专员配置归一化（跨包导出，供测试复用）。
func NormalizeSupervisorMessages(raw map[string]any) { normalizeSupervisorMessages(raw) }

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

// normalizeLoad 加载路径的归一化，**顺序逐条对应 config.ts 的 loadSettingsFrom**。
//
// 返回值只有四件事需要立刻落盘：「种入了预置条目」「提醒内容条目 id 被收敛成序号」
// 「摘掉了早期误种的应用暂停条目」「清掉了已删除功能的残留键（含老版本写过的机器标识）」。
// 其余归一化结果（越界夹紧）**不主动回写** —— 每次启动都改写用户配置会把键序和格式一起
// 动掉，而且在「验证脚本误指真实配置」时会留下副作用；这些夹紧结果会在用户下一次保存时
// 一并落盘。
func normalizeLoad(raw map[string]any) (presetSeeded, idsRenumbered, appsPruned, legacyDropped bool) {
	// ① 旧键清理与迁移（必须在归一化之前：迁移写出来的值才是最终值）
	legacyDropped = cleanLegacyKeys(raw)
	// ② 休息触发方式与固定时刻表
	raw["short"] = normalizeBreakConfigOf(mapOf(raw["short"]))
	raw["long"] = normalizeBreakConfigOf(mapOf(raw["long"]))
	// ③ 声音
	normalizeSound(raw)
	// ④ 自定义提醒
	idsRenumbered = normalizeCustomBreaks(raw)
	// ⑤ 外卖提醒
	raw["takeout"] = normalizeTakeout(raw)
	// ⑥ 指定应用运行时暂停：归一化 → 摘掉早期误种的综合办公套件 → 首次种入内置会议软件
	raw["appPause"], appsPruned = pruneSuitePresets(normalizeAppPause(raw))
	if raw["appPause"].(map[string]any)["seeded"] != true {
		raw["appPause"] = seedAppPause(raw["appPause"].(map[string]any))
	}
	// ⑦ 开关类字段（含旧的「休息窗口显示位置」下拉框迁移）
	normalizeToggles(raw)
	// ⑦·5 设置窗口尺寸（window 块，老配置没有就按出厂默认补一块）
	normalizeWindow(raw)
	// ⑧ 提醒内容（含旧 messages 键迁移）
	idsRenumbered = normalizeReminderMessages(raw) || idsRenumbered
	// ⑨ 督休专员
	normalizeSupervisorMessages(raw)
	// ⑩ 预置提醒内容条目（幂等）
	presetSeeded = seedPresetEntries(raw)
	return presetSeeded, idsRenumbered, appsPruned, legacyDropped
}

// NormalizeLoad 加载路径的归一化（跨包导出，供 main 的恢复默认复用：
// 恢复默认后要立刻种入预置条目、与启动管线完全一致）。
func NormalizeLoad(raw map[string]any) {
	normalizeLoad(raw)
}

// normalizeForSave 设置页保存 / 导入外来配置后要跑的归一化。
// 与加载路径的差别：不做旧键迁移与首次种子（那些是一次性动作，加载时已经做过），
// 唯一的例外是机器标识键的兜底删除（见函数体）；顺序对应 main.ts 的 settings:save /
// settings:import。
func normalizeForSave(raw map[string]any) {
	// 机器标识机制已删除（二维码不再带 id）：保存路径也兜底摘掉，保证任何一次落盘
	// 都不会把它写回 settings.json —— 加载路径的同类清理在 cleanLegacyKeys 里
	delete(raw, "installId")
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

// NormalizeForSave 保存 / 导入后要跑的归一化（跨包导出，供 main 的提示音导入复用）。
func NormalizeForSave(raw map[string]any) { normalizeForSave(raw) }

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

// Candidates 候选配置文件路径（跨包导出，供 main 的提示音目录推导等使用）。
func Candidates(resourcesRoot string) []string { return settingsCandidates(resourcesRoot) }

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
		presetSeeded, idsRenumbered, appsPruned, legacyDropped := normalizeLoad(merged)
		s := &Settings{raw: merged, path: p}
		// 四种情况回写：种入预置条目、提醒内容条目 id 被收敛成序号（一次性格式统一，
		// 收敛完即为定值，不会每次启动都写盘）、摘除早期误种的套件预设、清掉已删除功能的
		// 残留键（不落盘的话残留键会一直躺在 settings.json 里，导入导出也会带走）
		if presetSeeded || idsRenumbered || appsPruned || legacyDropped {
			if err := s.Save(); err != nil {
				log.Printf("[settings] 初始化结果落盘失败: %v", err)
			} else {
				log.Printf("[settings] 已回写初始化结果 %s（种入预置=%v 条目重编号=%v 摘除套件预设=%v 清理残留键=%v）",
					p, presetSeeded, idsRenumbered, appsPruned, legacyDropped)
			}
		}
		return s
	}
	// 一份都读不到 = 首次运行：走与「读到文件」完全相同的归一化管线（种入预置条目），
	// 并落盘 —— 与 上一版的 firstRun 分支一致
	fresh := defaultSettingsMap()
	presetSeeded, idsRenumbered, appsPruned, legacyDropped := normalizeLoad(fresh)
	s := &Settings{raw: fresh, path: ""}
	if presetSeeded || idsRenumbered || appsPruned || legacyDropped {
		if err := s.Save(); err != nil {
			log.Printf("[settings] 首次运行落盘失败: %v", err)
		}
	}
	return s
}

// Load 读配置（跨包导出，供 main 启动时调用）。
func Load(resourcesRoot string) *Settings { return loadSettings(resourcesRoot) }

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

// SetResourcesRootHint 注入资源根目录推导函数（跨包导出，供 main 启动时注入）。
func SetResourcesRootHint(fn func() string) { resourcesRootHintFn = fn }

func (s *Settings) resourcesRootHint() string {
	if resourcesRootHintFn != nil {
		return resourcesRootHintFn()
	}
	return "."
}
