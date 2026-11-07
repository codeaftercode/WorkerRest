// Package role 管理督休专员这份“软件资产”：目录扫描、名字查重、立绘与磁贴预览图、
// 导入导出、顺序维护。专员的身份是目录名 + meta.json 里的 name，没有内部 id。
//
// 本包只处理磁盘上的文件与元信息，不涉及配置读写（那是 internal/settings）与界面渲染
// （缩略图字节由 cmd 层的 role_thumb.js 生成）。
package role

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"workerrest/internal/jsonx"
	"workerrest/internal/model"
)

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
	Name     string        `json:"name"`
	Enabled  bool          `json:"enabled"`
	Image    string        `json:"image"`
	Preview  string        `json:"preview,omitempty"`
	Geom     model.SupGeom `json:"geom"`
	Messages RoleMessages  `json:"messages"`
}

// DefaultRoleOrder 软件自带督休专员的默认出场次序（按**名称**列，不是身份）。
//
// 它只决定“用户还没拖过磁贴时”立绘库与顺序轮换的先后；身份、定位、配置一律按
// meta.json 的 name。所以把“医生”改成“值班医生”，它就不再匹配这条默认次序、
// 自动排到末尾 —— 这是去掉内部 id 的必然代价，也是用户认可的行为（2026-10-28：
// 自带的与导入的没有区别，也不需要区分）。
//
// 2026-10-29 按用户在设置页拖出的实际排布定稿，作为打包发布时的出厂顺序；改这里的
// 顺序要同步 scripts/probe_renderer.py 与 scripts/smoke_server.py 的 BUILTIN_ROLE_ORDER
// （那两个是它的镜像，只用于“出厂顺序 vs 磁盘扫描”的断言）。
var DefaultRoleOrder = []string{
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
	"画家",
	"魔法师",
}

// IsImageName 图片文件名合法性（与 supervisor.ts 的 isImageName 同口径）：
// 只要名字合法就先用着，文件在不在由调用方另外判断——“名字非法”与“文件缺失”
// 是两件事，上一版 用前者决定要不要回落到 figure.*（见 roleAsset）。
func IsImageName(name string) bool {
	if name == "" || strings.ContainsAny(name, `/\`) || strings.HasPrefix(name, ".") {
		return false
	}
	_, ok := ImageExtMIME[strings.ToLower(filepath.Ext(name))]
	return ok
}

// NormalizeRoleMessages 归一化 meta.json 的台词池（对应 supervisor.ts 的
// normalizeRoleFile 里的 messages 部分）：order 只认三个合法值（缺省 fixed）、
// 逐条补 id、enabled 只认非 false。
func NormalizeRoleMessages(m map[string]any) RoleMessages {
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
	items := jsonx.AnySlice(m["items"])
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
		old := jsonx.ItemID(mm["id"])
		id := jsonx.SeqIDFor(old, ids, seen)
		if old != "" && old != id && !seen[old] {
			remap[old] = id
		}
		seen[id] = true
		ids = append(ids, id)
		out.Items = append(out.Items, RoleMessageItem{ID: id, Text: text, Enabled: mm["enabled"] != false})
	}
	out.FixedID = jsonx.RemapMsgID(remap, out.FixedID)
	return out
}

// NormalizeRoleFile 把 meta.json 的原始 map 归一化（对应 supervisor.ts 的
// normalizeRoleFile）。**必须从原始 map 取 geom**，不能从反序列化后的结构体取：
// 结构体里“字段缺失”与“显式 0”长得一样，而 0 对 sizePct 是非法值（会被夹成 20%）、
// 对 posXPct/posYPct 却合法 —— 混了就会把“没写过 geom”的督休专员算成缩到 20%。
func NormalizeRoleFile(raw map[string]any) RoleFile {
	out := RoleFile{
		Name:     strings.TrimSpace(jsonx.JsStr(raw["name"])),
		Enabled:  raw["enabled"] != false,
		Image:    DefaultFigureName,
		Geom:     NormalizeSupGeom(jsonx.AsMap(raw["geom"])),
		Messages: NormalizeRoleMessages(jsonx.AsMap(raw["messages"])),
	}
	if s, ok := raw["image"].(string); ok && IsImageName(s) {
		out.Image = s
	}
	if s, ok := raw["preview"].(string); ok && IsImageName(s) {
		out.Preview = s
	}
	return out
}

// Role 扫描结果 = 历史源码/supervisor.ts 里 listRoleViews() 的输出
// （{thumb, ...cfg}），字段名对齐 —— 设置页直接读这些字段：
//   - name     → 身份，同时是磁贴标题、台词气泡与提醒面板里的称呼；
//   - thumb    → 立绘库磁贴的缩略图（data URL），少了它就是一片碎图；
//   - geom     → 尺寸/位置滑条的当前值；
//   - messages → “提醒内容”面板的条目（见 settings.js 的 msgCfgOf），
//     少了下发面板会永远显示“暂无提醒内容”。
type Role struct {
	Thumb    string        `json:"thumb"`
	Name     string        `json:"name"`
	Enabled  bool          `json:"enabled"`
	Image    string        `json:"image"`
	Preview  string        `json:"preview,omitempty"`
	Geom     model.SupGeom `json:"geom"`
	Messages RoleMessages  `json:"messages"`
	// FigurePath 立绘绝对路径，只给 Go 侧用（组装会话时读图）
	FigurePath string `json:"-"`
}

// UrgeLines 内置劝说台词库：第一段台词的兜底（正常情况下第一段说的就是提醒配置的提醒内容，
// 只有提醒内容为空时才会用到它）。与 历史源码/supervisor.ts 的 URGE_LINES 同源。
var UrgeLines = []string{
	"休息时间到了，先把视线从屏幕上移开。",
	"我在看着你呢——去休息吧。",
	"这几分钟不属于屏幕，站起来活动一下。",
	"眼睛累了不会喊疼，但我知道它在抗议。",
	"肩膀有点僵了吧？离开椅子两分钟就好。",
	"我不催你，只是提醒你：该歇会儿了。",
}

var ImageExtMIME = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".webp": "image/webp",
	".gif":  "image/gif",
}

// 立绘 / 预览图的**主名**（扩展名换过多少次都按它兜底找）与默认文件名：meta.json 里
// 没写 image 时用 DefaultFigureName。2026-10-23 起随包立绘统一为 webp，所以默认值也
// 换成 webp；自定义督休专员目录里遗留的 png 由 RoleAsset 的扩展名兜底读到，
// 不会因为换了默认值就显示不出来。
const (
	RoleFigureBase    = "figure"
	RolePreviewBase   = "preview"
	DefaultFigureName = RoleFigureBase + ".webp"
)

// RoleMetaFile 督休专员配置文件名：2026-10-28 由 role.json 改为 meta.json
// （配置与立绘 / 预览图这两种素材分开命名，看目录就知道哪个是数据）。
// 只认这一个名字：软件尚未发布，不存在需要兼容的历史部署。
const RoleMetaFile = "meta.json"

// roleAssetExts 立绘可接受的扩展名，按尝试优先级排列（RoleAsset 兜底时依次找）
var roleAssetExts = []string{".webp", ".png", ".jpg", ".jpeg", ".gif"}

// RoleAsset 在督休专员目录里定位一张图：先按 meta.json 里写的名字找，找不到再拿同一
// 主名换其他图片扩展名找。返回**实际文件名**（相对目录名），找不到返回空串。
//
// 需要扩展名兜底的原因：立绘格式换过（png → webp），而 meta.json 是数据、可能比磁盘
// 落一拍（用户自己转换了格式、或配置里写死 figure.png）。只认单一扩展名会让
// “图明明在、却显示不出来”变成最难排查的那类问题。base 是 name 不可用时的主名。
func RoleAsset(dir, name, base string) string {
	if name != "" && IsImageName(name) {
		if IsRegularFile(filepath.Join(dir, name)) {
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
		if IsRegularFile(filepath.Join(dir, base+ext)) {
			return base + ext
		}
	}
	return ""
}

// ValidRoleDirName 目录名合法性：拒绝空、隐藏目录、路径分隔符与 .. —— 目录名会拼进文件路径。
// 判的是**磁盘目录名**，不是专员名（专员名可以带 `/` 之类，由 SanitizeRoleDirName 抹平）。
func ValidRoleDirName(dirName string) bool {
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

// ScanRoles 扫描督休专员目录。读不到 meta.json 的子目录跳过（可能正在写入）；
// 立绘缺失的督休专员**仍然列出**（FigurePath 为空）——与 上一版一致：要被设置页看到
// 才能改名 / 删除 / 换图，不能凭空消失。
//
// 身份 = meta.json 的 name；name 缺失时用目录名（目录名本来就是 name 的投影）。
// 顺序：DefaultRoleOrder 里的名称按清单顺序在前，其余（用户导入 / 手放的）按目录名
// 跟在后面——这个顺序就是立绘库磁贴与“顺序轮换”的出场次序。
func ScanRoles(root string) []Role {
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
		if !e.IsDir() || !ValidRoleDirName(e.Name()) {
			continue
		}
		dir := filepath.Join(root, e.Name())
		raw, err := os.ReadFile(filepath.Join(dir, RoleMetaFile))
		if err != nil {
			continue
		}
		var rawMap map[string]any
		if err := json.Unmarshal(raw, &rawMap); err != nil {
			continue
		}
		cfg := NormalizeRoleFile(rawMap)
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
		if asset := RoleAsset(dir, cfg.Image, RoleFigureBase); asset != "" {
			r.Image = asset
			r.FigurePath = filepath.Join(dir, asset)
		}
		// 磁贴缩略图：优先用 preview.*（渲染层生成的 184 高 WebP），**没有就回退到立绘原图**。
		// 预览图是可选优化而不是必需品（见 role_preview.go）：用户删了预览图、或专员是在
		// 别处手工放进来的，磁贴也得有画面 —— 回退的代价只是多解码几 MB。
		//
		// 找不到实际文件时把 Preview 清空（而不是留着 meta.json 里那个名字）：这个字段
		// 的含义是“真实存在的预览图”，报一个已经不存在的文件名只会误导调用方。
		if asset := RoleAsset(dir, cfg.Preview, RolePreviewBase); asset != "" {
			r.Preview = asset
			r.Thumb = ToDataURL(filepath.Join(dir, asset))
		} else {
			r.Preview = ""
		}
		if r.Thumb == "" && r.FigurePath != "" {
			r.Thumb = ToDataURL(r.FigurePath)
		}
		recs = append(recs, rec{role: r, dirName: e.Name()})
	}
	// 自带督休专员按默认清单顺序在前
	out := make([]Role, 0, len(recs))
	used := make(map[string]bool, len(recs))
	for _, want := range DefaultRoleOrder {
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

// RoleIdentity 把专员名折算成“目录名口径”用于查重。
//
// 为什么不能直接比 name 字符串：
//   - SanitizeRoleDirName 会剔除 `/\:*?"<>|` 并 trim —— "A/B" 与 "AB" 是不同的名字、
//     却是同一个目录；
//   - Windows 目录名不区分大小写 —— "Alice" 与 "alice" 也撞；
//   - 尾随点 / 空格被系统吞掉 —— "abc." 与 "abc" 撞；
//   - CON / PRN / COM1 这类保留名靠 sanitize 加前缀避开，折算后仍要一致。
//
// 所以“没有重名”的判据是 RoleIdentity 相等，而不是字符串相等。
func RoleIdentity(name string) string {
	return strings.ToLower(SanitizeRoleDirName(name))
}
