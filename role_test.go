package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"workerrest/internal/role"
)

// 立绘格式换成 webp 后的读取回归（2026-10-23）：meta.json 里写的扩展名与磁盘上的
// 实际文件不一致时，role.ScanRoles 必须按**实际文件**定位，否则立绘「明明在、却显示不出来」。
// 三类情形：完全对齐（webp）、meta.json 落后一拍（写 png、磁盘只有 webp）、
// meta.json 缺 image 字段（走新默认 figure.webp，但磁盘是老 png）。

// writeRoleDir 铺一个督休专员子目录：meta.json + 指定的图片文件（名字 → 内容）
func writeRoleDir(t *testing.T, root, dirName string, cfg map[string]any, files map[string]string) {
	t.Helper()
	dir := filepath.Join(root, dirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// findRole 在扫描结果里按名字找（找不到 Fatal）
func findRole(t *testing.T, roles []role.Role, name string) role.Role {
	t.Helper()
	for _, r := range roles {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("扫描结果里没有 %s（roles=%d）", name, len(roles))
	return role.Role{}
}

// TestScanRolesWebpAligned meta.json 与磁盘都是 webp → 直接读 webp
func TestScanRolesWebpAligned(t *testing.T) {
	root := t.TempDir()
	writeRoleDir(t, root, "眼镜主管", map[string]any{"name": "眼镜主管", "enabled": true,
		"image": "figure.webp", "preview": "preview.webp",
	}, map[string]string{"figure.webp": "webp-figure", "preview.webp": "webp-preview"})

	r := findRole(t, role.ScanRoles(root), "眼镜主管")
	if r.Image != "figure.webp" {
		t.Fatalf("Image 应为 figure.webp: %q", r.Image)
	}
	if filepath.Base(r.FigurePath) != "figure.webp" {
		t.Fatalf("FigurePath 应指向 figure.webp: %q", r.FigurePath)
	}
	if r.Thumb == "" {
		t.Fatal("缩略图应为非空 data URL")
	}
	if r.Preview != "preview.webp" {
		t.Fatalf("Preview 应为 preview.webp: %q", r.Preview)
	}
}

// TestScanRolesImageExtFallback meta.json 还写着 png、磁盘上只有 webp → 按实际文件纠正
func TestScanRolesImageExtFallback(t *testing.T) {
	root := t.TempDir()
	writeRoleDir(t, root, "体能教练", map[string]any{"name": "体能教练", "enabled": true,
		"image": "figure.png", "preview": "preview.png",
	}, map[string]string{"figure.webp": "webp-figure", "preview.webp": "webp-preview"})

	r := findRole(t, role.ScanRoles(root), "体能教练")
	if r.FigurePath == "" {
		t.Fatal("磁盘上有 figure.webp，立绘不应判为缺失（meta.json 的 png 名要能兜底）")
	}
	if r.Image != "figure.webp" || filepath.Base(r.FigurePath) != "figure.webp" {
		t.Fatalf("应纠正为实际文件名 figure.webp: Image=%q FigurePath=%q", r.Image, r.FigurePath)
	}
	if r.Thumb == "" {
		t.Fatal("缩略图应能按 preview.webp 兜底读到")
	}
}

// TestScanRolesDefaultFigureName meta.json 缺 image 字段 → 默认 figure.webp；磁盘是老 png 也能读
func TestScanRolesDefaultFigureName(t *testing.T) {
	root := t.TempDir()
	writeRoleDir(t, root, "老自定义", map[string]any{"name": "老自定义", "enabled": true}, map[string]string{"figure.png": "png-figure", "preview.png": "png-preview"})

	r := findRole(t, role.ScanRoles(root), "老自定义")
	if r.FigurePath == "" {
		t.Fatal("只有 figure.png 的老目录也必须能读到立绘")
	}
	if r.Image != "figure.png" {
		t.Fatalf("应纠正为实际文件名 figure.png: %q", r.Image)
	}
	if r.Thumb == "" {
		t.Fatal("缩略图应能按 preview.png 兜底读到")
	}
}

// TestScanRolesMissingImage 一个图都没有 → 仍然列出（设置页要能改名 / 换图 / 删除），
// 但 FigurePath / Thumb 为空
func TestScanRolesMissingImage(t *testing.T) {
	root := t.TempDir()
	writeRoleDir(t, root, "空目录", map[string]any{"name": "空目录", "enabled": true,
		"image": "figure.webp", "preview": "preview.webp",
	}, map[string]string{})

	r := findRole(t, role.ScanRoles(root), "空目录")
	if r.FigurePath != "" || r.Thumb != "" {
		t.Fatalf("没有图片文件时 FigurePath/Thumb 都应为空: %q %q", r.FigurePath, r.Thumb)
	}
}
