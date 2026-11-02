package role

import (
	"encoding/base64"
	"log"
	"os"
	"path/filepath"
	"strings"

	"workerrest/internal/jsonx"
)

// 立绘预览图（磁贴缩略图）的生成与落盘。
//
// 历史与取舍：
//   - 上一版（Electron）用 nativeImage 把立绘缩到 192 宽写 preview.png；
//   - 迁到 Go 侧时退化成「把原图复制一份当 preview」—— 等于白占一倍磁盘，还会和真正的
//     立绘脱钩（用户手工换图不改 meta，磁贴就还显示旧副本），而磁贴该省的从来不是
//     磁盘而是**解码内存**：17 张 1024×1536 原图解码要 114 MB，184 高的缩略图只要 4 MB；
//   - 现在由**渲染层**生成（canvas + WebP 编码器，见 web/dist/role_thumb.js）：
//     裁掉四边透明区、缩到磁贴高度（92 CSS px × 2 倍屏 = 184）、编码成 6~10 KB 的 WebP，
//     再把 data URL 送回来由这里落盘。
//
// 为什么不让 Go 自己缩：本工程 CGO_ENABLED=0（机器上没有 gcc），纯 Go 路线要引
// golang.org/x/image（+ nativewebp 才有 WebP 输出），实测 exe 涨 1.16~1.25 MB，
// 缩放还得自己挑算法；渲染层是零依赖、零体积，产出还更小。
//
// 预览图是**可选优化**：没有 preview.* 时 ScanRoles 会让 thumb 回退到立绘原图，
// 磁贴照样显示（只是多解码几 MB）。所以这里任何一步失败都只是「少了个优化」，
// 绝不能让导入 / 换图流程失败。

const (
	// 预览图体积上限：前端产出 6~10 KB，这里给足余量但挡住异常输入
	rolePreviewMaxBytes = 4 << 20
	// 预览图文件名主名（与 role.go 的 RolePreviewBase 同源，这里只用于删除旧文件）
	rolePreviewGlob = "preview.*"
)

// FigureDataURL 返回某个督休专员立绘原图的 data URL（渲染层据此生成缩略图）。
// 找不到立绘（或 meta.json 读不出来）返回空串。
func FigureDataURL(root, name string) string {
	name = strings.TrimSpace(name)
	if name == "" || root == "" {
		return ""
	}
	dir, ok := ResolveRoleDir(root, name)
	if !ok {
		return ""
	}
	cfg, ok := ReadRoleFile(root, name)
	if !ok {
		return ""
	}
	asset := RoleAsset(dir, cfg.Image, RoleFigureBase)
	if asset == "" {
		return ""
	}
	return ToDataURL(filepath.Join(dir, asset))
}

// SaveRolePreview 保存渲染层生成好的预览图（data URL）并回写 meta.json。
//
// 返回是否成功。数据不合法（不是图片 / 过大）或专员不存在都返回 false，
// 渲染层据此静默跳过 —— 预览图只是优化，没有它磁贴会回退到立绘原图。
func SaveRolePreview(root, name, dataURL string) bool {
	name = strings.TrimSpace(name)
	if name == "" || root == "" {
		return false
	}
	dir, ok := ResolveRoleDir(root, name)
	if !ok {
		log.Printf("[role] 保存预览图失败：找不到督休专员 %s", name)
		return false
	}
	cfg, ok := ReadRoleFile(root, name)
	if !ok {
		log.Printf("[role] 保存预览图失败：读不出 meta.json %s", name)
		return false
	}
	raw, ext, ok := DecodeImageDataURL(dataURL)
	if !ok {
		log.Printf("[role] 保存预览图失败：数据不是可识别的图片（%s）", name)
		return false
	}
	target := RolePreviewBase + ext
	if err := jsonx.WriteFileAtomic(filepath.Join(dir, target), raw); err != nil {
		log.Printf("[role] 写入预览图失败 %s: %v", name, err)
		return false
	}
	// 清掉扩展名不同的旧预览（preview.png / preview.jpg …），刚写的那份跳过。
	// 顺序与 ReplaceRoleImage 一致：**先写新的、再删旧的**，中途失败也不至于没有预览。
	if matches, _ := filepath.Glob(filepath.Join(dir, rolePreviewGlob)); matches != nil {
		for _, m := range matches {
			if filepath.Base(m) == target {
				continue
			}
			_ = os.Remove(m)
		}
	}
	// meta.json 里记下真实的预览文件名（没有这一步，RoleAsset 只能靠「同名换扩展名」兜底）
	cfg.Preview = target
	if err := WriteRoleFileAt(dir, cfg); err != nil {
		log.Printf("[role] 回写 meta.json 失败 %s: %v", name, err)
		return false
	}
	return true
}

// DecodeImageDataURL 解析 "data:image/webp;base64,..."（也接受裸 base64），
// 并**按魔数**判断真实格式 —— 扩展名由内容决定，不信任前端的 MIME 声明。
func DecodeImageDataURL(s string) ([]byte, string, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, "", false
	}
	if strings.HasPrefix(s, "data:") {
		i := strings.Index(s, ",")
		if i < 0 {
			return nil, "", false
		}
		// 只接受 base64 编码的 data URL；其它（如 data:image/svg+xml,<svg...) 一律拒绝
		if !strings.Contains(strings.ToLower(s[:i]), ";base64") {
			return nil, "", false
		}
		s = s[i+1:]
	}
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		// 容忍缺少 padding 的 base64（某些内核的 toDataURL 不带 '='）
		raw, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(s, "="))
		if err != nil {
			return nil, "", false
		}
	}
	if len(raw) < 32 || len(raw) > rolePreviewMaxBytes {
		return nil, "", false
	}
	if ext, ok := SniffImageExt(raw); ok {
		return raw, ext, true
	}
	return nil, "", false
}

// SniffImageExt 按魔数认格式，返回与 ImageExtMIME 对应的扩展名。
func SniffImageExt(b []byte) (string, bool) {
	switch {
	case len(b) >= 12 && string(b[0:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return ".webp", true
	case len(b) >= 8 && string(b[1:4]) == "PNG":
		return ".png", true
	case len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
		return ".jpg", true
	case len(b) >= 6 && (string(b[0:6]) == "GIF87a" || string(b[0:6]) == "GIF89a"):
		return ".gif", true
	}
	return "", false
}
