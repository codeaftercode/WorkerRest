package main

import (
	"fmt"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"log"
	"math/rand"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"workerrest/internal/sound"
)

// ---------- 播放宿主 ----------

// initSoundHost 建播放宿主窗口（隐藏 1×1，加载 sound.html）。
// 无头构建下没有原生窗口 —— 播放会退化成「只记日志」（见 playSoundFile），
// 所以无头冒烟仍能断言「该播哪一首」这条链路。
func (a *App) initSoundHost() {
	if serverMode || a.app == nil {
		log.Printf("[sound] server 模式无原生窗口，跳过播放宿主创建")
		return
	}
	a.mu.Lock()
	exists := a.wins[soundHostWindowName] != nil
	a.mu.Unlock()
	if exists {
		return
	}
	win := a.app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             soundHostWindowName,
		Title:            "sound",
		Width:            1,
		Height:           1,
		Hidden:           true,
		Frameless:        true,
		DisableResize:    true,
		URL:              "/sound.html",
		BackgroundColour: application.NewRGBA(0, 0, 0, 0),
		Windows: application.WindowsWindow{
			DisableFramelessWindowDecorations: true,
			HiddenOnTaskbar:                   true,
		},
	})
	// 页面加载完（WebView 运行时就绪）只用来留一条日志 —— 播放**不**依赖它：
	// webbview 未就绪时 ExecJS 会把脚本排队、等加载完再执行（Wails 自带语义），
	// 所以自己加一道「没就绪就不发」反而会把播放整段丢掉。
	win.OnWindowEvent(events.Common.WindowRuntimeReady, func(*application.WindowEvent) {
		log.Printf("[sound] 播放宿主就绪（sound.html 已加载）")
	})
	win.OnWindowEvent(events.Common.WindowClosing, func(*application.WindowEvent) {
		a.mu.Lock()
		// 只删自己：destroySoundHost 之后可能重建宿主窗，迟到一步的旧回调按名字 delete
		// 会把新宿主的引用抹掉（与提前提示窗同一个坑，见 dropWindowRef）
		dropWindowRef(a.wins, soundHostWindowName, win)
		a.mu.Unlock()
	})
	a.mu.Lock()
	a.wins[soundHostWindowName] = win
	a.mu.Unlock()
	log.Printf("[sound] 播放宿主窗口已创建（隐藏 1×1，加载 sound.html）")
}

// destroySoundHost 收掉播放宿主（退出清理）
func (a *App) destroySoundHost() {
	a.mu.Lock()
	w := a.wins[soundHostWindowName]
	delete(a.wins, soundHostWindowName)
	a.mu.Unlock()
	if w != nil {
		w.Close()
	}
}

// playConfiguredSound 播放当前生效的提示音（休息开始 / 结束各一次）。
// 静音是「配置的选择」而不是错误，所以静音路径只记日志、不报错。
func (a *App) playConfiguredSound() {
	cfg := a.cfgSnapshot().SoundCfg()
	file := a.soundPickNext(cfg, rand.Intn)
	if file == "" {
		log.Printf("[sound] 静音（enabled=%v volume=%.2f mode=%s）", cfg.Enabled, cfg.Volume, cfg.Mode)
		return
	}
	log.Printf("[sound] 播放 %s（volume=%.2f mode=%s）", file, cfg.Volume, cfg.Mode)
	a.playSoundFile(file, cfg.Volume)
}

// playSoundFile 让宿主窗口播一个文件。
//
// 不需要等宿主「就绪」：WebView 还没加载完时 ExecJS 会把脚本排队、加载完自动执行
// （见 WebviewWindow.ExecJS 的 pendingJS 分支）。宿主窗口在启动时就建好了，
// 头几次播放通常远晚于页面加载，这条路径不会丢。
func (a *App) playSoundFile(file string, volume float64) {
	url := a.SoundFileURL(file)
	if url == "" {
		log.Printf("[sound] 无法播放 %s（文件不可用或格式不支持）", file)
		return
	}
	if serverMode || a.app == nil {
		log.Printf("[sound] server 模式不发声（本应播放 %s volume=%.2f）", url, volume)
		return
	}
	a.execPlaySound(url, volume)
}

// execPlaySound 把播放指令发给承载 <audio> 的窗口。每次新建一个 Audio 元素 —— 允许重叠
// 播放（休息结束音紧接下一段开始时不该互相打断），与 sound.ts 的 playSound 同口径。
func (a *App) execPlaySound(url string, volume float64) {
	w := a.audioHostWindow()
	if w == nil {
		return
	}
	// 纯 JS，不带注释；url 用 strconv.Quote 转义（与 JS 字符串字面量兼容）
	script := fmt.Sprintf(
		"(()=>{const a=new Audio(%s);a.volume=%s;a.play().catch(()=>{})})()",
		strconv.Quote(url), strconv.FormatFloat(volume, 'f', -1, 64))
	w.ExecJS(script)
}

// audioHostWindow 挑一个承载 <audio> 的窗口：优先主设置窗口（main.go 里随应用创建、
// 「关闭」只是 Hide 不销毁，全程存活、已加载且用户交互过），退回专用宿主窗。
// 历史上只用隐藏宿主窗，但那条链从未被验证能真发声（隐藏窗可能不加载 + WebView2 默认
// 拦无手势有声自动播放）；改走必然存活的设置窗更稳，autoplay 已在 main.go 全局放开。
// 二者皆无返回 nil（server 模式在 playSoundFile 已先行短路，不会走到这里）。
func (a *App) audioHostWindow() application.Window {
	if a.app != nil {
		if w := a.settingsWindow(); w != nil {
			return w
		}
	}
	a.mu.Lock()
	hw := a.wins[soundHostWindowName]
	a.mu.Unlock()
	if hw == nil {
		return nil // 别把「类型化 nil」当接口返回，否则上层 w==nil 判不出
	}
	return hw
}

// ---------- 绑定给渲染层的接口 ----------

// SoundFileURL 把音频文件换算成可播放 URL（设置页试听用 <audio>、播放宿主用 Audio）。
// 内置音（相对文件名）→ 资源服务的 /sounds/xxx.wav；自定义音（绝对路径）→ 本进程的
// /__sound/?p=…（白名单见 allowedSoundPath）。**文件不可用返回空串** ——
// 渲染层正是用 `if (!url)` 判「找不到该音频文件」的。
func (a *App) SoundFileURL(file string) string {
	file = strings.TrimSpace(file)
	if file == "" {
		return ""
	}
	if !filepath.IsAbs(file) {
		if !sound.BuiltinExists(file) {
			return ""
		}
		return builtinSoundURL(file)
	}
	abs := filepath.Clean(file)
	if !sound.FileUsable(abs) {
		return ""
	}
	return soundURLPrefix + "?p=" + url.QueryEscape(abs)
}

// DeleteMusicSound 删除音乐条目对应的副本文件（只删音频目录里那一层，见 deleteMusicSound）
func (a *App) DeleteMusicSound(file string) bool {
	return a.deleteMusicSound(file)
}
