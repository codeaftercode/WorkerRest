package main

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// 督休专员目录迁移：把旧版「督休专员数据存 settings.json」一次性搬进督休专员目录 role.json。
// 逐字对应 历史源码/supervisor.ts 的 migrateLegacyRoles（见那处的完整注释）。
//
// 与 上一版的一个有意差异：Go 侧的督休专员目录是**单一落点**（rolesRoot 就是
// 数据目录，没有「随包模板 → %APPDATA%/supervisors」的 ensureRolesDir 分离），所以迁移
// 直接写进 rolesRoot 本身。模板目录里的 role.json 自带默认配置，迁移用旧数据覆盖它。
// 目录名用专员名称、身份 key 存于 role.json（见 role.go 的 resolveRoleDir）。

// migrateLegacyRoles 执行一次性迁移，返回是否发生了迁移（调用方据此落盘）。
//
// raw = 整份配置的 map（含 supervisor 子对象）；rolesRoot = 督休专员目录落点（supervisors/）。
// 迁移内容：内置督休专员的名称/启用/几何/台词覆盖模板 role.json（旧列表里没有的内置督休专员
// 连目录一起删，不回补）、自定义督休专员按旧 key 建目录并搬图、fixedKey 键名重映射、
// 迁完删掉 supervisor 里的全部旧键并置 rolesMigrated=true。
func migrateLegacyRoles(raw map[string]any, rolesRoot string) bool {
	sup, ok := raw["supervisor"].(map[string]any)
	if !ok {
		return false
	}
	if sup["rolesMigrated"] == true {
		return false
	}
	// 全新安装（或已是督休专员目录版配置）：settings 里没有任何旧版遗留键。
	// 必须在这里短路 —— 否则下面会把「旧配置里没有这个内置督休专员」误判成「用户删过」，
	// 首次启动就把模板里刚就位的内置督休专员目录全部删光。
	hasLegacy := false
	for _, k := range []string{"roles", "geom", "messagesByRole", "image", "builtin", "customs"} {
		if _, exists := sup[k]; exists {
			hasLegacy = true
			break
		}
	}
	if !hasLegacy {
		sup["rolesMigrated"] = true
		log.Printf("[supervisor] 无旧版督休专员配置（全新安装），跳过迁移")
		return true
	}

	entries := scanRoles(rolesRoot)
	var oldRoles []map[string]any
	for _, r := range anySlice(sup["roles"]) {
		if m, ok := r.(map[string]any); ok {
			oldRoles = append(oldRoles, m)
		}
	}
	oldGeom := asMap(sup["geom"])
	oldMsgs := asMap(sup["messagesByRole"])
	// pickOld：旧配置里 geom/messagesByRole 的键可能带 .png 后缀（内置键），两种都认
	pickOld := func(m map[string]any, key string) any {
		if v, ok := m[key]; ok {
			return v
		}
		if v, ok := m[key+".png"]; ok {
			return v
		}
		return nil
	}
	// 旧 roles[] 里的 key 集合（内置键带 .png，自定义键为内容摘要）
	legacyPresent := make(map[string]bool)
	for _, r := range oldRoles {
		if k, ok := r["key"].(string); ok && k != "" {
			legacyPresent[k] = true
		}
	}
	migratedNames := []string{}

	// ① 内置督休专员：模板 role.json 就位 → 覆盖用户数据；被用户删过 → 目录一并删除
	for _, e := range entries {
		if !isBuiltinRole(e.Key) {
			continue
		}
		legacyKey := e.Key
		if !legacyPresent[e.Key] {
			legacyKey = e.Key + ".png"
		}
		if !legacyPresent[legacyKey] {
			if removeRoleDir(rolesRoot, e.Key) {
				log.Printf("[supervisor] 迁移：内置督休专员 %s 在旧配置中已被删除，目录一并移除", e.Key)
			}
			continue
		}
		old := findOldRole(oldRoles, legacyKey)
		geom := pickOld(oldGeom, e.Key)
		msgs := pickOld(oldMsgs, e.Key)
		cfg, ok := readRoleFile(rolesRoot, e.Key)
		if !ok {
			cfg = RoleFile{
				Name:     roleFallbackName(e.Key),
				Enabled:  true,
				Image:    defaultFigureName,
				Geom:     supGeomDefault,
				Messages: RoleMessages{Order: "fixed", Items: []RoleMessageItem{}},
			}
		}
		if name, _ := old["name"].(string); strings.TrimSpace(name) != "" {
			cfg.Name = name
		}
		cfg.Enabled = old["enabled"] != false
		if gm, ok := geom.(map[string]any); ok {
			cfg.Geom = normalizeSupGeom(gm)
		}
		if mm, ok := msgs.(map[string]any); ok {
			cfg.Messages = normalizeRoleMessages(mm)
		}
		if err := writeRoleFile(rolesRoot, e.Key, cfg); err == nil {
			migratedNames = append(migratedNames, cfg.Name)
		}
	}

	// ② 自定义督休专员：按旧 key 建目录，把配置目录里的图片副本搬进来
	for _, r := range oldRoles {
		key, _ := r["key"].(string)
		path, _ := r["path"].(string)
		if key == "" || path == "" {
			continue
		}
		if isBuiltinRole(key) || !strings.HasPrefix(key, "custom-") {
			continue
		}
		if !isRegularFile(path) {
			continue
		}
		name, _ := r["name"].(string)
		if name == "" {
			name = key
		}
		// createRoleDir 内部按 sha1 摘要算目录 key（= custom-<摘要>），与旧 key 的语义一致：
		// 旧版自定义督休专员的 key 就是图片内容摘要，所以正常情况下两者相同；图片若被改过，
		// 摘要会变、目录 key 跟着变，这时以**摘要目录**为准（与上一版 一致 —— 只 warn，
		// 不把目录改回旧 key）。
		created := createRoleDir(rolesRoot, name, path)
		if created == "" {
			continue // 图片读不进 / 超 20MB / 建目录失败：跳过这个督休专员
		}
		targetKey := created
		if created != key {
			log.Printf("[supervisor] 迁移：自定义督休专员摘要键不一致 old=%s new=%s", key, created)
		}
		if cfg, ok := readRoleFile(rolesRoot, targetKey); ok {
			geom := asMap(oldGeom[key])
			msgs := asMap(oldMsgs[key])
			cfg.Name = strings.TrimSpace(name)
			if cfg.Name == "" {
				cfg.Name = "自定义督休专员"
			}
			cfg.Enabled = r["enabled"] != false
			if len(geom) > 0 {
				cfg.Geom = normalizeSupGeom(geom)
			}
			if len(msgs) > 0 {
				cfg.Messages = normalizeRoleMessages(msgs)
			}
			_ = writeRoleFile(rolesRoot, targetKey, cfg)
		}
		migratedNames = append(migratedNames, name)
	}

	// ③ fixedKey 键名重映射 + 失效兜底；删旧键、置迁移标记
	fixedKey, _ := sup["fixedKey"].(string)
	if strings.HasSuffix(fixedKey, ".png") {
		fixedKey = strings.TrimSuffix(fixedKey, ".png")
	}
	after := scanRoles(rolesRoot)
	if !hasRoleKey(after, fixedKey) {
		fixedKey = firstEnabledRole(after)
	}
	sup["fixedKey"] = fixedKey
	sup["rolesMigrated"] = true
	for _, k := range []string{"roles", "geom", "messagesByRole", "rolesSeeded", "image", "builtin", "customs"} {
		delete(sup, k)
	}
	log.Printf("[supervisor] 督休专员数据迁移完成：%d 个督休专员写入 role.json（fixedKey=%s，settings.json 旧键已清除）",
		len(migratedNames), fixedKey)
	return true
}

// isBuiltinRole 是否内置督休专员（按清单 key）
func isBuiltinRole(key string) bool {
	for _, m := range builtinRoleMeta {
		if m.key == key {
			return true
		}
	}
	return false
}

// findOldRole 在旧 roles[] 里找 key 匹配的那条（找不到给空 map）
func findOldRole(oldRoles []map[string]any, key string) map[string]any {
	for _, r := range oldRoles {
		if k, _ := r["key"].(string); k == key {
			return r
		}
	}
	return map[string]any{}
}

// hasRoleKey 扫描结果里是否有指定 key
func hasRoleKey(roles []Role, key string) bool {
	for _, r := range roles {
		if r.Key == key {
			return true
		}
	}
	return false
}

// firstEnabledRole 第一个启用的督休专员 key，全禁用时取第一个；空返回空串
func firstEnabledRole(roles []Role) string {
	for _, r := range roles {
		if r.Enabled {
			return r.Key
		}
	}
	if len(roles) > 0 {
		return roles[0].Key
	}
	return ""
}

// createRoleDir 新建自定义督休专员目录：把本地图片复制成 figure + preview，算 sha1 摘要做 key。
// 对应 supervisor.ts 的 createRoleDir。与 上一版的差异：preview 直接复制原图（Go 侧
// 没有 nativeImage 缩图，原生选图 + 生成缩略图是占位项），所以 preview 用与 figure 相同的扩展名。
// 目录名改用专员名称（sanitize + 去重），与「督休专员 = 目录、目录名 = 名称」的新布局一致；
// key 仍由摘要决定并写进 role.json，作为稳定身份。返回新督休专员目录的 key。
func createRoleDir(rolesRoot, name, src string) string {
	st, err := os.Stat(src)
	if err != nil || !st.Mode().IsRegular() || st.Size() > 20*1024*1024 {
		return ""
	}
	b, err := os.ReadFile(src)
	if err != nil {
		return ""
	}
	sum := sha1.Sum(b)
	key := "custom-" + hex.EncodeToString(sum[:])[:10]
	// 目录按专员名称命名（去重），与「目录名 = 名称」的新布局一致
	base := sanitizeRoleDirName(name)
	if base == "" {
		base = key
	}
	dir := uniqueRoleDir(rolesRoot, base, "")
	if isRegularFile(filepath.Join(dir, "role.json")) {
		return key // 同名目录里已有 role.json：沿用既有目录
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ""
	}
	ext := strings.ToLower(filepath.Ext(src))
	if ext == "" {
		ext = ".png"
	}
	if err := os.WriteFile(filepath.Join(dir, "figure"+ext), b, 0o644); err != nil {
		return ""
	}
	if err := os.WriteFile(filepath.Join(dir, "preview"+ext), b, 0o644); err != nil {
		return ""
	}
	cfg := RoleFile{
		Key:      key,
		Name:     name,
		Enabled:  true,
		Image:    "figure" + ext,
		Preview:  "preview" + ext,
		Geom:     supGeomDefault,
		Messages: RoleMessages{Order: "random", Items: genericCommandItems()},
	}
	if cfg.Name == "" {
		cfg.Name = "自定义督休专员"
	}
	// 目录已按名称建好：直接落盘 role.json（绕过「按 key 反查目录」）
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return ""
	}
	data = append(data, '\n')
	target := filepath.Join(dir, "role.json")
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		_ = os.Remove(tmp)
		return ""
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return ""
	}
	return key
}

// genericCommandItems 新建自定义督休专员时的命令台词种子（通用命令库）。
// 对应 supervisor.ts 的 genericCommandItems（Go 侧没有内置专属台词库，统一用通用命令）。
func genericCommandItems() []RoleMessageItem {
	lines := []string{
		"我说了，离开屏幕。现在就离开。",
		"再碰一次键鼠，我就一直这么盯着你。",
		"你是想让我一直数下去吗？站起来。",
		"最后一遍：站起来，离开屏幕。",
		"眼睛需要休息，这没什么可商量的。",
		"不离开屏幕，我就一直盯着你，直到你走。",
	}
	items := make([]RoleMessageItem, 0, len(lines))
	for i, text := range lines {
		items = append(items, RoleMessageItem{
			ID:      "builtin-cmd-generic-" + string(rune('0'+i)),
			Text:    text,
			Enabled: true,
		})
	}
	return items
}
