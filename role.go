package main

import (
	"encoding/base64"
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// 督休专员目录读取：语义与 历史源码/supervisor.ts 的 role.json 完全对齐——
// 一个督休专员 = supervisors/<名称>/ 一个子目录，内含 role.json + 立绘 + 预览图。
// 目录名现在用专员名称（中文），身份 key 存进 role.json 的 key 字段，读写一律按 key 反查。
// 用户在该目录里增删子目录即增删督休专员，重启即生效（本阶段先做到「扫描 + 读取」，
// 增删改留到设置页迁移那一阶段）。

// RoleMessageItem 一条台词（命令阶段），id 沿用内置前缀规则
type RoleMessageItem struct {
	ID      string `json:"id"`
	Text    string `json:"text"`
	Enabled bool   `json:"enabled"`
}

// RoleMessages 督休专员的命令台词池（order: random | sequence | fixed）
type RoleMessages struct {
	Order   string            `json:"order"`
	FixedID string            `json:"fixedId,omitempty"`
	Items   []RoleMessageItem `json:"items"`
}

// RoleFile role.json 的磁盘结构（字段名与 上一版逐字一致，两侧可互相识别）
type RoleFile struct {
	// Key 督休专员的稳定身份标识（旧版等于目录名，现目录名改用专员名称后，
	// key 仍保持旧值：内置沿用 role_* / supervisor_default，自定义沿用 custom-<sha1>）。
	// 它才是 fixedKey / 迁移映射 / 读写定位的权威键，目录名只是给人看的名字。
	Key      string       `json:"key,omitempty"`
	Name     string       `json:"name"`
	Enabled  bool         `json:"enabled"`
	Image    string       `json:"image"`
	Preview  string       `json:"preview,omitempty"`
	Geom     SupGeom      `json:"geom"`
	Messages RoleMessages `json:"messages"`
}

// builtinRoleMeta 内置督休专员的固定顺序与兜底名字（与 历史源码/supervisor.ts 的
// BUILTIN_ROLE_META 逐字一致）。顺序**不是**按目录名排的 —— 它就是「立绘库磁贴」与
// 「顺序轮换」的出场次序，改成字典序会让内置督休专员的出场顺序与 上一版不同。
// 2026-10-23 起先是 5 个督休专员（魔鬼教官 / 宿管大爷 / 赛博监考官 / 冷面上司已下架，
// 目录一并删除）；2026-10-25 又追加了 12 个（始皇帝 … 毒舌损友），新的一律排在原有 5 个
// 之后 —— 新增专员不该打乱老用户习惯的出场次序。清单里没有的 key 不会按内置顺序出现，
// 而是排在清单末尾按目录名排序（自定义 / 用户手放）。
var builtinRoleMeta = []struct{ key, name string }{
	{"supervisor_default", "眼镜主管"},
	{"role_mentor", "温柔学姐"},
	{"role_doctor", "医生"},
	{"role_coach", "体能教练"},
	{"role_shopkeeper", "古风掌柜"},
	{"role_emperor", "始皇帝"},
	{"role_hacker", "神秘黑客"},
	{"role_pilot", "飞行员"},
	{"role_judge", "法官"},
	{"role_economist", "经济学家"},
	{"role_magician", "魔法师"},
	{"role_painter", "画家"},
	{"role_warrior", "战士"},
	{"role_panda", "熊猫侠客"},
	{"role_lieutenant", "中尉军官"},
	{"role_girlfriend", "野蛮女友"},
	{"role_frenemy", "毒舌损友"},
}

// roleFallbackName role.json 里没写 name 时的兜底名
func roleFallbackName(key string) string {
	for _, m := range builtinRoleMeta {
		if m.key == key {
			return m.name
		}
	}
	return "自定义督休专员"
}

// isImageName 图片文件名合法性（与 supervisor.ts 的 isImageName 同口径）：
// 只要名字合法就先用着，文件在不在由调用方另外判断——「名字非法」与「文件缺失」
// 是两件事，上一版 用前者决定要不要回落到 figure.*（见 roleAsset）。
func isImageName(name string) bool {
	if name == "" || strings.ContainsAny(name, `/\`) || strings.HasPrefix(name, ".") {
		return false
	}
	_, ok := imageExtMIME[strings.ToLower(filepath.Ext(name))]
	return ok
}

// normalizeRoleMessages 归一化 role.json 的台词池（对应 supervisor.ts 的
// normalizeRoleFile 里的 messages 部分）：order 只认三个合法值（缺省 fixed）、
// 逐条补 id、enabled 只认非 false。
func normalizeRoleMessages(m map[string]any) RoleMessages {
	out := RoleMessages{Order: "fixed", Items: []RoleMessageItem{}}
	if s, ok := m["order"].(string); ok {
		switch s {
		case "fixed", "random", "sequence":
			out.Order = s
		}
	}
	if s, ok := m["fixedId"].(string); ok {
		out.FixedID = s
	}
	for _, it := range anySlice(m["items"]) {
		mm, ok := it.(map[string]any)
		if !ok {
			continue
		}
		text, ok := mm["text"].(string)
		if !ok {
			continue
		}
		id := ""
		if s, ok := mm["id"].(string); ok && s != "" {
			id = s
		}
		if id == "" {
			id = newUUID()
		}
		out.Items = append(out.Items, RoleMessageItem{ID: id, Text: text, Enabled: mm["enabled"] != false})
	}
	return out
}

// normalizeRoleFile 把 role.json 的原始 map 归一化（对应 supervisor.ts 的
// normalizeRoleFile）。**必须从原始 map 取 geom**，不能从反序列化后的结构体取：
// 结构体里「字段缺失」与「显式 0」长得一样，而 0 对 sizePct 是非法值（会被夹成 20%）、
// 对 posXPct/posYPct 却合法 —— 混了就会把「没写过 geom」的督休专员算成缩到 20%。
func normalizeRoleFile(raw map[string]any) RoleFile {
	name := strings.TrimSpace(jsStr(raw["name"]))
	out := RoleFile{
		Name:     name,
		Enabled:  raw["enabled"] != false,
		Image:    defaultFigureName,
		Geom:     normalizeSupGeom(asMap(raw["geom"])),
		Messages: normalizeRoleMessages(asMap(raw["messages"])),
	}
	// key 优先取 role.json 显式字段；缺失（旧部署）时留给调用方按目录名兜底
	if s, ok := raw["key"].(string); ok && s != "" {
		out.Key = s
	}
	if s, ok := raw["image"].(string); ok && isImageName(s) {
		out.Image = s
	}
	if s, ok := raw["preview"].(string); ok && isImageName(s) {
		out.Preview = s
	}
	return out
}

// asMap 取子对象；缺失 / 类型不符给空 map
func asMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

// Role 扫描结果 = 历史源码/supervisor.ts 里 listRoleViews() 的输出
// （{key, thumb, ...cfg}），字段名逐字对齐 —— 设置页直接读这些字段：
//   - thumb    → 立绘库磁贴的缩略图（data URL），少了它就是一片碎图；
//   - geom     → 尺寸/位置滑条的当前值；
//   - messages → 「提醒内容」面板的条目（见 settings.js 的 msgCfgOf），
//     少了下发面板会永远显示「暂无提醒内容」。
type Role struct {
	Key      string       `json:"key"`
	Thumb    string       `json:"thumb"`
	Name     string       `json:"name"`
	Enabled  bool         `json:"enabled"`
	Image    string       `json:"image"`
	Preview  string       `json:"preview,omitempty"`
	Geom     SupGeom      `json:"geom"`
	Messages RoleMessages `json:"messages"`
	// FigurePath 立绘绝对路径，只给 Go 侧用（组装会话时读图）
	FigurePath string `json:"-"`
}

// 内置劝说台词库：第一段台词的兜底（正常情况下第一段说的就是提醒配置的提醒内容，
// 只有提醒内容为空时才会用到它）。与 历史源码/supervisor.ts 的 URGE_LINES 同源。
var urgeLines = []string{
	"休息时间到了，先把视线从屏幕上移开。",
	"我在看着你呢——去休息吧。",
	"这几分钟不属于屏幕，站起来活动一下。",
	"眼睛累了不会喊疼，但我知道它在抗议。",
	"肩膀有点僵了吧？离开椅子两分钟就好。",
	"我不催你，只是提醒你：该歇会儿了。",
}

var imageExtMIME = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".webp": "image/webp",
	".gif":  "image/gif",
}

// 立绘 / 预览图的**主名**（扩展名换过多少次都按它兜底找）与默认文件名：role.json 里
// 没写 image 时用 defaultFigureName。2026-10-23 起随包立绘统一为 webp，所以默认值也
// 换成 webp；自定义督休专员目录里遗留的 png 由 roleAsset 的扩展名兜底读到，
// 不会因为换了默认值就显示不出来。
const (
	roleFigureBase    = "figure"
	rolePreviewBase   = "preview"
	defaultFigureName = roleFigureBase + ".webp"
)

// roleAssetExts 立绘可接受的扩展名，按尝试优先级排列（roleAsset 兜底时依次找）
var roleAssetExts = []string{".webp", ".png", ".jpg", ".jpeg", ".gif"}

// roleAsset 在督休专员目录里定位一张图：先按 role.json 里写的名字找，找不到再拿同一
// 主名换其他图片扩展名找。返回**实际文件名**（相对目录名），找不到返回空串。
//
// 需要扩展名兜底的原因：立绘格式换过（png → webp），而 role.json 是数据、可能比磁盘
// 落一拍（用户自己转换了格式、或老配置里写死 figure.png）。只认单一扩展名会让
// 「图明明在、却显示不出来」变成最难排查的那类问题。base 是 name 不可用时的主名。
func roleAsset(dir, name, base string) string {
	if name != "" && isImageName(name) {
		if isRegularFile(filepath.Join(dir, name)) {
			return name
		}
		if b := strings.TrimSuffix(name, filepath.Ext(name)); b != "" {
			base = b
		}
	}
	if base == "" {
		return ""
	}
	for _, ext := range roleAssetExts {
		if isRegularFile(filepath.Join(dir, base+ext)) {
			return base + ext
		}
	}
	return ""
}

// validRoleKey 目录名合法性：与 上一版 isValidRoleKey 同口径
// （拒绝空、隐藏目录、路径分隔符与 .. —— 督休专员 key 会拼进文件路径）
func validRoleKey(key string) bool {
	if key == "" || len(key) > 128 {
		return false
	}
	if strings.HasPrefix(key, ".") {
		return false
	}
	if strings.ContainsAny(key, `/\:*?"<>|`) || key == ".." {
		return false
	}
	return true
}

// scanRoles 扫描督休专员目录。读不到 role.json 的子目录跳过（可能正在写入）；
// 立绘缺失的督休专员**仍然列出**（figurePath 为空）——与 上一版一致：要被设置页看到
// 才能改名 / 删除 / 换图，不能凭空消失。
//
// 目录名现在是专员名称（中文），与身份 key 解耦：身份以 role.json 的 key 字段为准，
// 找不到 key 时回退到目录名（兼容旧部署）。顺序：内置督休专员按 BUILTIN_ROLE_META
// 的清单顺序在前，其余（自定义 / 用户手放）按目录名跟在后面——这个顺序就是立绘库磁贴
// 与「顺序轮换」的出场次序。
func scanRoles(root string) []Role {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	type rec struct {
		role    Role
		dirName string
	}
	recs := make([]rec, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() || !validRoleKey(e.Name()) {
			continue
		}
		dir := filepath.Join(root, e.Name())
		raw, err := os.ReadFile(filepath.Join(dir, "role.json"))
		if err != nil {
			continue
		}
		var rawMap map[string]any
		if err := json.Unmarshal(raw, &rawMap); err != nil {
			continue
		}
		cfg := normalizeRoleFile(rawMap)
		roleKey := cfg.Key
		if roleKey == "" {
			roleKey = e.Name()
		}
		if cfg.Name == "" {
			cfg.Name = roleFallbackName(roleKey)
		}
		r := Role{
			Key:      roleKey,
			Name:     cfg.Name,
			Enabled:  cfg.Enabled,
			Image:    cfg.Image,
			Preview:  cfg.Preview,
			Geom:     cfg.Geom,
			Messages: cfg.Messages,
		}
		// 立绘 / 预览按**实际文件**定位：role.json 写的扩展名可能和磁盘上不一致
		// （格式从 png 换成 webp 时最容易漏改的就是 role.json）。
		if name := roleAsset(dir, cfg.Image, roleFigureBase); name != "" {
			r.Image = name
			r.FigurePath = filepath.Join(dir, name)
		}
		if name := roleAsset(dir, cfg.Preview, rolePreviewBase); name != "" {
			r.Preview = name
			r.Thumb = toDataURL(filepath.Join(dir, name))
		}
		recs = append(recs, rec{role: r, dirName: e.Name()})
	}
	// 内置督休专员按清单顺序在前
	out := make([]Role, 0, len(recs))
	used := make(map[string]bool, len(recs))
	for _, m := range builtinRoleMeta {
		for _, rc := range recs {
			if rc.role.Key == m.key && !used[m.key] {
				out = append(out, rc.role)
				used[m.key] = true
				break
			}
		}
	}
	// 其余按目录名排序（自定义 / 用户手放）
	rest := make([]rec, 0)
	for _, rc := range recs {
		if !used[rc.role.Key] {
			rest = append(rest, rc)
		}
	}
	sort.Slice(rest, func(i, j int) bool { return rest[i].dirName < rest[j].dirName })
	for _, rc := range rest {
		out = append(out, rc.role)
	}
	return out
}

// resolveRoleDir 按 key 反查督休专员子目录：目录名改用专员名称后，key 不再等于目录名，
// 所有读写都必须「按 key 反查」——扫描各子目录的 role.json，返回 key 字段（或目录名兜底）
// 与 key 匹配的目录绝对路径。找不到返回 ("", false)。
func resolveRoleDir(root, key string) (string, bool) {
	if key == "" {
		return "", false
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		if !e.IsDir() || !validRoleKey(e.Name()) {
			continue
		}
		dir := filepath.Join(root, e.Name())
		raw, err := os.ReadFile(filepath.Join(dir, "role.json"))
		if err != nil {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			continue
		}
		if k, ok := m["key"].(string); ok && k == key {
			return dir, true
		}
		// 旧部署：role.json 没有 key 字段，目录名即 key
		if e.Name() == key {
			return dir, true
		}
	}
	return "", false
}

// sanitizeRoleDirName 把专员名称收敛成合法目录名：剔除 Windows 非法字符并裁掉首尾空白。
// 空名给一个兜底，避免建出坏目录。
func sanitizeRoleDirName(name string) string {
	s := strings.TrimSpace(name)
	if s == "" {
		return "督休专员"
	}
	s = strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			return -1
		default:
			return r
		}
	}, s)
	s = strings.TrimSpace(s)
	if s == "" {
		return "督休专员"
	}
	return s
}

// isRegularFile 路径存在且是普通文件
func isRegularFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// toDataURL 读图并转 data URL（渲染层 <img src> 直接用，省一次 HTTP 往返）
func toDataURL(file string) string {
	b, err := os.ReadFile(file)
	if err != nil {
		return ""
	}
	mime, ok := imageExtMIME[strings.ToLower(filepath.Ext(file))]
	if !ok {
		mime = "image/png"
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(b)
}

// pickRoleLine 从督休专员的台词池里选 1 条（与 上一版 pickRoleLine 同规则）：
// fixed 取指定条目（与启停无关）；sequence 在启用条目间轮换（游标按督休专员记忆）；
// random 等概率抽启用条目。取不到返回空串——由渲染层沿用第一段台词。
func pickRoleLine(cfg RoleMessages, key string, seq map[string]int, rng *rand.Rand) string {
	if cfg.Order == "fixed" {
		for _, it := range cfg.Items {
			if it.ID == cfg.FixedID {
				return it.Text
			}
		}
		return ""
	}
	pool := make([]RoleMessageItem, 0, len(cfg.Items))
	for _, it := range cfg.Items {
		if it.Enabled {
			pool = append(pool, it)
		}
	}
	if len(pool) == 0 {
		return ""
	}
	if cfg.Order == "sequence" {
		cur := seq[key] % len(pool)
		seq[key] = (cur + 1) % len(pool)
		return pool[cur].Text
	}
	return pool[rng.Intn(len(pool))].Text
}

// pickOne 等概率取一条（掷骰子交给调用方持有的 rng，便于测试时固定种子）
func pickOne(lines []string, rng *rand.Rand) string {
	if len(lines) == 0 {
		return ""
	}
	return lines[rng.Intn(len(lines))]
}
