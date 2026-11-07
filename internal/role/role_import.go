package role

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"workerrest/internal/jsonx"
)

// 导入本地图片为新的督休专员（设置页“导入立绘”背后那条路）。
//
// 身份就是专员名，所以这里只做三件事：定一个不重名的名字、按名字建目录、
// 写 figure 与 meta.json。没有内部 id，也没有 sha1 摘要键。
//
// 注意这里**不写预览图**：缩略图由渲染层生成（canvas 裁透明边 + 缩到磁贴高度 +
// 编码 WebP），建目录后由 settings.js 调 SaveRolePreview 补上，见 role_preview.go。

// CreateRoleDir 新建督休专员目录：把本地图片复制成 figure，写 meta.json。
// 对应 supervisor.ts 的 createRoleDir。与 上一版的差异：上一版用 nativeImage
// 顺手缩一张 preview.png；Go 侧不引图像库，缩略图改由渲染层事后生成
// （没有预览图时 ScanRoles 会让磁贴回退到原图，界面不会开天窗）。
//
// name 是默认名（通常是文件名）：重名时自动退让成“X (2)”，因为这条路不能弹窗问用户。
// 返回**最终落盘的专员名**（= meta.json 的 name，也是后续所有操作的标识）；失败返回空串。
func CreateRoleDir(rolesRoot, name, src string) string {
	st, err := os.Stat(src)
	if err != nil || !st.Mode().IsRegular() || st.Size() > 20*1024*1024 {
		return ""
	}
	// 扩展名必须先过白名单（与 ReplaceRoleImage 同口径）：ImageExtMIME 不认的格式
	// （.bmp / .svg …）写下去，读取侧的 IsImageName 也不认 → 磁贴空白、FigurePath 为空，
	// 该督休专员**永远出不了场**（buildSupervisorSessionWith 过滤 FigurePath == ""）。
	ext := strings.ToLower(filepath.Ext(src))
	if ext == "" {
		ext = ".png"
	}
	if _, ok := ImageExtMIME[ext]; !ok {
		log.Printf("[role] 不支持的图片格式：%s", ext)
		return ""
	}
	b, err := os.ReadFile(src)
	if err != nil {
		return ""
	}
	display := UniqueRoleName(rolesRoot, name, "")
	if strings.TrimSpace(display) == "" {
		display = "自定义督休专员"
	}
	dir := UniqueRoleDir(rolesRoot, SanitizeRoleDirName(display), "")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ""
	}
	// 任何一步失败都把刚建的目录整个清掉：没有 meta.json 的目录会被 ScanRoles 跳过
	// —— 用户在界面上看不见它，它却占着“名字”，下一次导入就变成 "名字 (2)" 了。
	abort := func() { _ = os.RemoveAll(dir) }
	if err := jsonx.WriteFileAtomic(filepath.Join(dir, "figure"+ext), b); err != nil {
		abort()
		return ""
	}
	cfg := RoleFile{
		Name:     display,
		Enabled:  true,
		Image:    "figure" + ext,
		Geom:     SupGeomDefault,
		Messages: RoleMessages{Order: "random", Items: GenericCommandItems()},
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		abort()
		return ""
	}
	data = append(data, '\n')
	if err := jsonx.WriteFileAtomic(filepath.Join(dir, RoleMetaFile), data); err != nil {
		abort()
		return ""
	}
	return cfg.Name
}

// GenericCommandItems 新建自定义督休专员时的命令台词种子（通用命令库）。
// 对应 supervisor.ts 的 genericCommandItems（Go 侧没有内置专属台词库，统一用通用命令）。
func GenericCommandItems() []RoleMessageItem {
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
			ID:      strconv.Itoa(i + 1),
			Text:    text,
			Enabled: true,
		})
	}
	return items
}
