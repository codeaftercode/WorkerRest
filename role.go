package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// 督休专员目录读取：语义与 历史源码/supervisor.ts 对齐（那里配置文件叫 role.json，
// 这里 2026-10-28 起改名为 meta.json；不做旧名兼容，见 roleMetaFile 的说明）——
// 一个督休专员 = supervisors/<名称>/ 一个子目录，内含 meta.json + 立绘 + 预览图。
//
// **身份就是专员名**：meta.json 里没有内部 id（2026-10-28 用户要求去掉 key —— 软件自带
// 的专员与用户导入的没有区别，不需要区分）。目录名是 name 的文件系统投影（sanitize 后），
// 读写都按 name 反查目录、比对 meta.json 的 name 字段。
//
// 代价是「重命名 = 换身份」：改名必须连同目录一起改（renameRoleDir），并同步配置里指向
// 它的 fixedRole / order；唯一性由 roleNameTaken 在写入前强制（口径是**目录名**而不是
// 显示名，见 roleIdentity 的注释）。
//
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

// RoleFile meta.json 的磁盘结构。Name 就是身份，没有单独的 id 字段：
// 同一份配置里两个督休专员不许重名，写入前由 roleNameTaken 拦。
type RoleFile struct {
	Name     string       `json:"name"`
	Enabled  bool         `json:"enabled"`
	Image    string       `json:"image"`
	Preview  string       `json:"preview,omitempty"`
	Geom     SupGeom      `json:"geom"`
	Messages RoleMessages `json:"messages"`
}

// defaultRoleOrder 软件自带督休专员的默认出场次序（按**名称**列，不是身份）。
//
// 它只决定「用户还没拖过磁贴时」立绘库与顺序轮换的先后；身份、定位、配置一律按
// meta.json 的 name。所以把「医生」改成「值班医生」，它就不再匹配这条默认次序、
// 自动排到末尾 —— 这是去掉内部 id 的必然代价，也是用户认可的行为（2026-10-28：
// 自带的与导入的没有区别，也不需要区分）。
//
// 2026-10-29 按用户在设置页拖出的实际排布定稿，作为打包发布时的出厂顺序；改这里的
// 顺序要同步 scripts/probe_renderer.py 与 scripts/smoke_server.py 的 BUILTIN_ROLE_ORDER
// （那两个是它的镜像，只用于「出厂顺序 vs 磁盘扫描」的断言）。
var defaultRoleOrder = []string{
	"秦始皇",
	"神秘黑客",
	"中尉军官",
	"熊猫侠客",
	"医生",
	"经济学家",
	"法官",
	"体能教练",
	"毒舌损友",
	"野蛮女友",
	"霸道总裁",
	"温柔学姐",
	"眼镜主管",
	"飞行员",
	"画家",
	"魔法师",
	"战士",
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

// normalizeRoleMessages 归一化 meta.json 的台词池（对应 supervisor.ts 的
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
	items := anySlice(m["items"])
	ids := make([]string, 0, len(items))
	seen := make(map[string]bool, len(items))
	remap := make(map[string]string, len(items))
	for _, it := range items {
		mm, ok := it.(map[string]any)
		if !ok {
			continue
		}
		text, ok := mm["text"].(string)
		if !ok {
			continue
		}
		old := itemID(mm["id"])
		id := seqIDFor(old, ids, seen)
		if old != "" && old != id && !seen[old] {
			remap[old] = id
		}
		seen[id] = true
		ids = append(ids, id)
		out.Items = append(out.Items, RoleMessageItem{ID: id, Text: text, Enabled: mm["enabled"] != false})
	}
	out.FixedID = remapMsgID(remap, out.FixedID)
	return out
}

// normalizeRoleFile 把 meta.json 的原始 map 归一化（对应 supervisor.ts 的
// normalizeRoleFile）。**必须从原始 map 取 geom**，不能从反序列化后的结构体取：
// 结构体里「字段缺失」与「显式 0」长得一样，而 0 对 sizePct 是非法值（会被夹成 20%）、
// 对 posXPct/posYPct 却合法 —— 混了就会把「没写过 geom」的督休专员算成缩到 20%。
func normalizeRoleFile(raw map[string]any) RoleFile {
	out := RoleFile{
		Name:     strings.TrimSpace(jsStr(raw["name"])),
		Enabled:  raw["enabled"] != false,
		Image:    defaultFigureName,
		Geom:     normalizeSupGeom(asMap(raw["geom"])),
		Messages: normalizeRoleMessages(asMap(raw["messages"])),
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
// （{thumb, ...cfg}），字段名对齐 —— 设置页直接读这些字段：
//   - name     → 身份，同时是磁贴标题、台词气泡与提醒面板里的称呼；
//   - thumb    → 立绘库磁贴的缩略图（data URL），少了它就是一片碎图；
//   - geom     → 尺寸/位置滑条的当前值；
//   - messages → 「提醒内容」面板的条目（见 settings.js 的 msgCfgOf），
//     少了下发面板会永远显示「暂无提醒内容」。
type Role struct {
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

// 立绘 / 预览图的**主名**（扩展名换过多少次都按它兜底找）与默认文件名：meta.json 里
// 没写 image 时用 defaultFigureName。2026-10-23 起随包立绘统一为 webp，所以默认值也
// 换成 webp；自定义督休专员目录里遗留的 png 由 roleAsset 的扩展名兜底读到，
// 不会因为换了默认值就显示不出来。
const (
	roleFigureBase    = "figure"
	rolePreviewBase   = "preview"
	defaultFigureName = roleFigureBase + ".webp"
)

// roleMetaFile 督休专员配置文件名：2026-10-28 由 role.json 改为 meta.json
// （配置与立绘 / 预览图这两种素材分开命名，看目录就知道哪个是数据）。
// 只认这一个名字：软件尚未发布，不存在需要兼容的历史部署。
const roleMetaFile = "meta.json"

// roleAssetExts 立绘可接受的扩展名，按尝试优先级排列（roleAsset 兜底时依次找）
var roleAssetExts = []string{".webp", ".png", ".jpg", ".jpeg", ".gif"}

// roleAsset 在督休专员目录里定位一张图：先按 meta.json 里写的名字找，找不到再拿同一
// 主名换其他图片扩展名找。返回**实际文件名**（相对目录名），找不到返回空串。
//
// 需要扩展名兜底的原因：立绘格式换过（png → webp），而 meta.json 是数据、可能比磁盘
// 落一拍（用户自己转换了格式、或配置里写死 figure.png）。只认单一扩展名会让
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

// validRoleDirName 目录名合法性：拒绝空、隐藏目录、路径分隔符与 .. —— 目录名会拼进文件路径。
// 判的是**磁盘目录名**，不是专员名（专员名可以带 `/` 之类，由 sanitizeRoleDirName 抹平）。
func validRoleDirName(dirName string) bool {
	if dirName == "" || len(dirName) > 128 {
		return false
	}
	if strings.HasPrefix(dirName, ".") {
		return false
	}
	if strings.ContainsAny(dirName, `/\:*?"<>|`) || dirName == ".." {
		return false
	}
	return true
}

// scanRoles 扫描督休专员目录。读不到 meta.json 的子目录跳过（可能正在写入）；
// 立绘缺失的督休专员**仍然列出**（FigurePath 为空）——与 上一版一致：要被设置页看到
// 才能改名 / 删除 / 换图，不能凭空消失。
//
// 身份 = meta.json 的 name；name 缺失时用目录名（目录名本来就是 name 的投影）。
// 顺序：defaultRoleOrder 里的名称按清单顺序在前，其余（用户导入 / 手放的）按目录名
// 跟在后面——这个顺序就是立绘库磁贴与「顺序轮换」的出场次序。
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
		if !e.IsDir() || !validRoleDirName(e.Name()) {
			continue
		}
		dir := filepath.Join(root, e.Name())
		raw, err := os.ReadFile(filepath.Join(dir, roleMetaFile))
		if err != nil {
			continue
		}
		var rawMap map[string]any
		if err := json.Unmarshal(raw, &rawMap); err != nil {
			continue
		}
		cfg := normalizeRoleFile(rawMap)
		name := cfg.Name
		if name == "" {
			name = e.Name()
		}
		r := Role{
			Name:     name,
			Enabled:  cfg.Enabled,
			Image:    cfg.Image,
			Preview:  cfg.Preview,
			Geom:     cfg.Geom,
			Messages: cfg.Messages,
		}
		// 立绘 / 预览按**实际文件**定位：meta.json 写的扩展名可能和磁盘上不一致
		// （格式从 png 换成 webp 时最容易漏改的就是 meta.json）。
		if asset := roleAsset(dir, cfg.Image, roleFigureBase); asset != "" {
			r.Image = asset
			r.FigurePath = filepath.Join(dir, asset)
		}
		// 磁贴缩略图：优先用 preview.*（渲染层生成的 184 高 WebP），**没有就回退到立绘原图**。
		// 预览图是可选优化而不是必需品（见 role_preview.go）：用户删了预览图、或专员是在
		// 别处手工放进来的，磁贴也得有画面 —— 回退的代价只是多解码几 MB。
		//
		// 找不到实际文件时把 Preview 清空（而不是留着 meta.json 里那个名字）：这个字段
		// 的含义是「真实存在的预览图」，报一个已经不存在的文件名只会误导调用方。
		if asset := roleAsset(dir, cfg.Preview, rolePreviewBase); asset != "" {
			r.Preview = asset
			r.Thumb = toDataURL(filepath.Join(dir, asset))
		} else {
			r.Preview = ""
		}
		if r.Thumb == "" && r.FigurePath != "" {
			r.Thumb = toDataURL(r.FigurePath)
		}
		recs = append(recs, rec{role: r, dirName: e.Name()})
	}
	// 自带督休专员按默认清单顺序在前
	out := make([]Role, 0, len(recs))
	used := make(map[string]bool, len(recs))
	for _, want := range defaultRoleOrder {
		for _, rc := range recs {
			if rc.role.Name != "" && !used[rc.role.Name] && strings.EqualFold(rc.role.Name, want) {
				out = append(out, rc.role)
				used[rc.role.Name] = true
				break
			}
		}
	}
	// 其余按目录名排序（用户导入 / 手放）
	rest := make([]rec, 0)
	for _, rc := range recs {
		if !used[rc.role.Name] {
			rest = append(rest, rc)
		}
	}
	sort.Slice(rest, func(i, j int) bool { return rest[i].dirName < rest[j].dirName })
	for _, rc := range rest {
		out = append(out, rc.role)
	}
	return out
}

// roleIdentity 把专员名折算成「目录名口径」用于查重。
//
// 为什么不能直接比 name 字符串：
//   - sanitizeRoleDirName 会剔除 `/\:*?"<>|` 并 trim —— "A/B" 与 "AB" 是不同的名字、
//     却是同一个目录；
//   - Windows 目录名不区分大小写 —— "Alice" 与 "alice" 也撞；
//   - 尾随点 / 空格被系统吞掉 —— "abc." 与 "abc" 撞；
//   - CON / PRN / COM1 这类保留名靠 sanitize 加前缀避开，折算后仍要一致。
//
// 所以「没有重名」的判据是 roleIdentity 相等，而不是字符串相等。
func roleIdentity(name string) string {
	return strings.ToLower(sanitizeRoleDirName(name))
}

// roleNameTaken 判断显示名 name 是否已被**其它**督休专员占用。
//
// excludeName 是「自己」的当前名字（改名时要排除自己 —— 否则用户不改名直接点确定
// 会被自己的旧名字挡住）。
//
// 「重名」的危害不是显示层的小瑕疵，而是 name ↔ 目录 ↔ fixedRole/order 三者失配：
// 目录名靠 uniqueRoleDir 去重成「X (2)」，meta.json 里却还是「X」，于是同一个名字在
// 画廊 / 台词气泡 / 提醒面板标题里指向两个不同的人，删一个还会删错。
func roleNameTaken(root, name, excludeName string) bool {
	n := strings.TrimSpace(name)
	if n == "" {
		return false
	}
	id := roleIdentity(n)
	ex := roleIdentity(excludeName)
	for _, other := range scanRoleNames(root) {
		if excludeName != "" && roleIdentity(other) == ex {
			continue
		}
		if roleIdentity(other) == id {
			return true
		}
	}
	return false
}

// scanRoleNames 只读各 meta.json 的显示名，**不碰立绘 / 预览图**。
//
// 为什么不用 scanRoles：它会给每个督休专员生成缩略图的 data URL（17 个自带专员
// 约 3.5 MB 的图片要读一遍再 base64 编码），而查重名只需要名字。
// uniqueRoleName 还要循环调用查重（"X" → "X (2)" → "X (3)"），
// 走 scanRoles 就是把整套图片反复解码若干遍 —— 改个名字要等好几秒。
func scanRoleNames(root string) []string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() || !validRoleDirName(e.Name()) {
			continue
		}
		dir := filepath.Join(root, e.Name())
		raw, err := os.ReadFile(filepath.Join(dir, roleMetaFile))
		if err != nil {
			continue
		}
		var rawMap map[string]any
		if err := json.Unmarshal(raw, &rawMap); err != nil {
			continue
		}
		name := strings.TrimSpace(jsStr(rawMap["name"]))
		if name == "" {
			name = e.Name()
		}
		out = append(out, name)
	}
	return out
}

// uniqueRoleName 给显示名去重：name 已被占用时依次试「name (2)」「name (3)」。
//
// 用于**软件自己起名**的地方（导入时用文件名当默认名）——这类场景不能弹窗问用户，
// 就自动退让一位。用户**主动**改名（SaveRole）不走这里：那条路要拒绝保存、要求重新输入。
//
// excludeName 是「自己」的当前名字：改名时把自己算进去，会把「不改名直接确定」
// 误判成重名。
func uniqueRoleName(root, name, excludeName string) string {
	base := strings.TrimSpace(name)
	if base == "" {
		base = "自定义督休专员"
	}
	// 只扫一次：循环里反复查重会把目录读上好几遍。
	taken := make([]string, 0, 16)
	ex := roleIdentity(excludeName)
	for _, other := range scanRoleNames(root) {
		if excludeName != "" && roleIdentity(other) == ex {
			continue
		}
		taken = append(taken, roleIdentity(other))
	}
	free := func(cand string) bool {
		id := roleIdentity(cand)
		for _, t := range taken {
			if t == id {
				return false
			}
		}
		return true
	}
	if free(base) {
		return base
	}
	for i := 2; ; i++ {
		cand := fmt.Sprintf("%s (%d)", base, i)
		if free(cand) {
			return cand
		}
	}
}

// applyRoleOrder 按用户保存的顺序重排督休专员。
//
// order 是专员名数组（设置页拖动立绘磁贴得到的顺序，落在 config/settings.json 的
// supervisor.order 里）：
//   - order 里出现的名字，按 order 的先后排；
//   - order 里没有的（后来导入的 / 改过名的）保持 scanRoles 给的相对顺序，追加在后面 ——
//     不会因为没写进 order 就消失；
//   - order 里多余的名字（专员已删除或已改名）直接忽略。
//
// order 为空时原样返回，即默认清单顺序 + 其余按目录名排序。
func applyRoleOrder(roles []Role, order []string) []Role {
	if len(order) == 0 || len(roles) == 0 {
		return roles
	}
	byName := make(map[string]Role, len(roles))
	for _, r := range roles {
		byName[roleIdentity(r.Name)] = r
	}
	out := make([]Role, 0, len(roles))
	used := make(map[string]bool, len(roles))
	for _, want := range order {
		id := roleIdentity(want)
		r, ok := byName[id]
		if !ok || used[id] {
			continue
		}
		out = append(out, r)
		used[id] = true
	}
	for _, r := range roles {
		if !used[roleIdentity(r.Name)] {
			out = append(out, r)
		}
	}
	return out
}

// resolveRoleDir 按专员名反查子目录：目录名是 name 的投影（sanitize 后可能不同，
// 比如名字里带 `/`），所以必须读 meta.json 的 name 比对，不能直接拼路径。
// 找不到返回 ("", false)。
func resolveRoleDir(root, name string) (string, bool) {
	want := strings.TrimSpace(name)
	if want == "" {
		return "", false
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		if !e.IsDir() || !validRoleDirName(e.Name()) {
			continue
		}
		dir := filepath.Join(root, e.Name())
		raw, err := os.ReadFile(filepath.Join(dir, roleMetaFile))
		if err != nil {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			continue
		}
		got := strings.TrimSpace(jsStr(m["name"]))
		if got == "" {
			// name 缺失：目录名就是身份（与 scanRoles 的口径一致）
			if strings.EqualFold(e.Name(), want) {
				return dir, true
			}
			continue
		}
		if strings.EqualFold(got, want) {
			return dir, true
		}
	}
	return "", false
}

// windowsReservedNames Windows 不允许直接当目录名的设备名（不区分大小写，
// 带扩展名也一样：CON.txt 同样非法）。
var windowsReservedNames = map[string]bool{
	"con": true, "prn": true, "aux": true, "nul": true,
	"com1": true, "com2": true, "com3": true, "com4": true, "com5": true,
	"com6": true, "com7": true, "com8": true, "com9": true,
	"lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true, "lpt5": true,
	"lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
}

// sanitizeRoleDirName 把专员名收敛成合法目录名。
//
// 目录名是身份的文件系统投影，所以这里多做几件「不处理就会静默撞车」的事：
//   - 剔除 Windows 非法字符；
//   - 去掉尾随的点与空格（系统会吞掉它们，"abc." 与 "abc" 会指向同一个目录）；
//   - 保留名（CON / PRN / COM1 …）加下划线前缀，否则 mkdir 直接失败；
//   - 限制长度：目录名上限 255 字节，中文按 3 字节算，60 个字符留足余量。
func sanitizeRoleDirName(name string) string {
	s := strings.TrimSpace(name)
	s = strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			return -1
		default:
			return r
		}
	}, s)
	s = strings.TrimRight(strings.TrimSpace(s), " .")
	if runes := []rune(s); len(runes) > 60 {
		s = strings.TrimSpace(string(runes[:60]))
	}
	s = strings.TrimRight(s, " .")
	if s == "" {
		return "督休专员"
	}
	base := strings.ToLower(s)
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	if windowsReservedNames[base] {
		s = "_" + s
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
// fixed 取指定条目（与启停无关）；sequence 在启用条目间轮换（游标按**专员名**记忆，
// 改名会让游标归零 —— 这是改名换身份的附带代价，不影响正确性）；
// random 等概率抽启用条目。取不到返回空串——由渲染层沿用第一段台词。
func pickRoleLine(cfg RoleMessages, name string, seq map[string]int, rng *rand.Rand) string {
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
		cur := seq[name] % len(pool)
		seq[name] = (cur + 1) % len(pool)
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
