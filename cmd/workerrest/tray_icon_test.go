//go:build windows

package main

import (
	"bytes"
	"io/fs"
	"path"
	"strings"
	"testing"

	"github.com/wailsapp/wails/v3/pkg/w32"
)

// 图片资产统一放在 web/dist 下（由 main.go 的 //go:embed all:web/dist 打进 exe），
// 仓库根不再有 assets/ 目录。全项目只有一个图标文件 icon.png，三处共用：
// 标题栏 <img class="tb-icon">、系统托盘（trayIcon）、exe 文件图标（构建期生成 syso）。
//
// 本文件钉住“坏了也看不出来”的两件事：
//
//  1. 托盘图标能过 Wails 的同一条转换链（CreateSmallHIconFromImage → CreateIconFromResourceEx）
//     并拿到非零 HICON。格式不被接受时托盘只是静默变空白图标，程序照常启动、日志也不报，
//     没有测试就只能靠肉眼。
//  2. dist 下的位图资产都非空。图片被误删、被构建流程覆盖成 0 字节时，页面会静默空一块
//     （关于页横幅、标题栏图标）。
//
// 为什么图标个数/图片个数都不写死：增删图片是正常的内容变更，写死个数会让测试在
// 无关改动上变红（本项目在“内置专员个数”上踩过这个坑）。

// TestTrayIconIsEmbeddedPNG 托盘图标必须是能从 embed 读到的 PNG。
func TestTrayIconIsEmbeddedPNG(t *testing.T) {
	if len(trayIcon) == 0 {
		t.Fatal("trayIcon 为空：web/dist/icon.png 没被 //go:embed all:web/dist 打进来")
	}
	if !bytes.HasPrefix(trayIcon, []byte("\x89PNG\r\n\x1a\n")) {
		t.Fatalf("trayIcon 不是 PNG（头 % x）：Wails 只认 PNG 与 ICO，别塞别的格式", trayIcon[:8])
	}
}

// TestTrayIconCreatesHIcon 拿真实字节走一遍 Wails 的转换链，必须得到非零 HICON。
func TestTrayIconCreatesHIcon(t *testing.T) {
	h, err := w32.CreateSmallHIconFromImage(trayIcon)
	if err != nil {
		t.Fatalf("CreateSmallHIconFromImage 失败（托盘图标会变空白）: %v", err)
	}
	if h == 0 {
		t.Fatal("CreateSmallHIconFromImage 返回空 HICON（托盘图标会变空白）")
	}
	w32.DestroyIcon(h)
}

// TestFrontendDistImagesNonEmpty dist 下每张位图都必须非空，且图标在册。
func TestFrontendDistImagesNonEmpty(t *testing.T) {
	imgExt := map[string]bool{".png": true, ".ico": true, ".webp": true, ".jpg": true, ".jpeg": true, ".gif": true}
	foundIcon := false
	count := 0
	err := fs.WalkDir(assets, "web/dist", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !imgExt[strings.ToLower(path.Ext(p))] {
			return nil
		}
		count++
		if path.Base(p) == "icon.png" {
			foundIcon = true
		}
		info, err := d.Info()
		if err != nil {
			t.Errorf("%s: 读文件信息失败: %v", p, err)
			return nil
		}
		if info.Size() == 0 {
			t.Errorf("%s: 是 0 字节（图片被误删或构建覆盖了？）", p)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("遍历 embed 的前端资源失败: %v", err)
	}
	if count == 0 {
		t.Fatal("web/dist 下没有任何位图资产，图片很可能被整体删掉了")
	}
	if !foundIcon {
		t.Fatal("web/dist/icon.png 不存在：标题栏、托盘、exe 图标都依赖它")
	}
}
