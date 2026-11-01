package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// TestReminderWindowsEnableCaptureGuard 「共享屏幕时隐藏提醒窗口」必须覆盖**每一个**
// 提醒窗口建窗点。
//
// 这条功能的特点是失效时完全没有症状：窗口照样弹、颜色照样对，只有真开会共享屏幕
// 那一刻才暴露。而新增一类提醒（或复制一份建窗代码）时漏写一个字段，编译、单测、
// 冒烟全都不会报。所以用源码扫描钉住：除白名单外，任何 WebviewWindowOptions
// 字面量都必须带 ContentProtectionEnabled。
//
// 白名单按**文件**粒度，只允许放「画面上不承载提醒内容」的窗口；
// 往白名单里加东西必须同时写清理由，否则这条断言会被人情一点点蛀空。
func TestReminderWindowsEnableCaptureGuard(t *testing.T) {
	skip := map[string]string{
		"main.go":       "设置窗口：用户主动打开，内容就是设置本身，没有提醒文案",
		"sound_host.go": "提示音播放宿主：1×1 且 Hidden，画面上根本不出现",
	}
	// 扫描所有源文件（而非固定清单）：源码拆分后建窗点会散到 app_windows.go、
	// takeout_windows.go 等主题文件，逐个点名反而会漏掉新增的建窗文件。
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("列出源文件失败：%v", err)
	}

	checked := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("读不到 %s: %v", name, err)
		}
		blocks := windowOptionBlocks(string(src))
		if len(blocks) == 0 {
			continue // 该文件没有建窗点，不是本次检查对象
		}
		if _, ok := skip[name]; ok {
			continue
		}
		for i, blk := range blocks {
			checked++
			if !strings.Contains(blk, "ContentProtectionEnabled") {
				t.Errorf("%s 第 %d 个建窗点缺 ContentProtectionEnabled —— 该窗口会被共享屏幕捕获；"+
					"若它不是提醒窗口，把该文件加进本测试的白名单并写明理由", name, i+1)
			}
		}
	}
	if checked == 0 {
		t.Fatal("一个提醒窗口建窗点都没扫到：扫描逻辑或文件名变了，这条断言已经失效")
	}
}

// TestSyncCaptureGuardToLiveWithoutWindows 没有提醒窗口在场时同步逻辑不能炸。
// SaveSettings 每次改到这个键都会走一遍，而窗口集合在启动 / server 模式下是空的。
func TestSyncCaptureGuardToLiveWithoutWindows(t *testing.T) {
	a := &App{
		cfg:  &Settings{raw: defaultSettingsMap()},
		wins: map[string]*application.WebviewWindow{},
	}
	a.syncCaptureGuardToLive() // 不 panic 即通过
}

// windowOptionBlocks 取出源码里每个 WebviewWindowOptions{...} 字面量的文本（含最外层
// 花括号），跳过字符串与行注释 —— 否则字面量里的括号会把配对带偏。
func windowOptionBlocks(src string) []string {
	const marker = "WebviewWindowOptions{"
	var out []string
	for i := 0; i < len(src); {
		j := strings.Index(src[i:], marker)
		if j < 0 {
			break
		}
		start := i + j + len(marker) - 1 // 指向 '{'
		closed := -1
		depth := 0
		for k := start; k < len(src) && closed < 0; k++ {
			switch src[k] {
			case '"':
				for k++; k < len(src) && src[k] != '"'; k++ {
					if src[k] == '\\' {
						k++
					}
				}
			case '`':
				for k++; k < len(src) && src[k] != '`'; k++ {
				}
			case '/':
				if k+1 < len(src) && src[k+1] == '/' {
					for k < len(src) && src[k] != '\n' {
						k++
					}
				}
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					closed = k + 1
				}
			}
		}
		if closed < 0 {
			break
		}
		out = append(out, src[start:closed])
		i = closed
	}
	return out
}
