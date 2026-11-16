package main

import (
	"fmt"
	"log"
	"math/rand"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	"workerrest/internal/logx"
	"workerrest/internal/sound"
)

// ---------- 播放宿主 ----------

// initSoundHost 建播放宿主窗口（隐藏 1×1，加载 sound.html）。
// 无头构建下没有原生窗口 —— 播放会退化成“只记日志”（见 playSoundFile），
// 所以无头冒烟仍能断言“该播哪一首”这条链路。
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
	// 页面加载完（WebView 运行时就绪）只用来留一条 debug 日志 —— 播放**不**依赖它：
	// webbview 未就绪时 ExecJS 会把脚本排队、等加载完再执行（Wails 自带语义），
	// 所以自己加一道“没就绪就不发”反而会把播放整段丢掉。
	win.OnWindowEvent(events.Common.WindowRuntimeReady, func(*application.WindowEvent) {
		logx.Debugf("[sound] 播放宿主就绪（sound.html 已加载）")
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
// 静音是“配置的选择”而不是错误，所以静音路径只记日志、不报错。
func (a *App) playConfiguredSound() {
	cfg := a.CfgSnapshot().SoundCfg()
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
// 不需要等宿主“就绪”：WebView 还没加载完时 ExecJS 会把脚本排队、加载完自动执行
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

// playSoundScript 播一段音频的 JS 模板（%s = url 字面量，%s = 音量）。
//
// 每次新建一个 Audio 元素 —— 允许重叠播放（休息结束音紧接下一段开始时不该互相打断），
// 与 sound.ts 的 playSound 同口径。放出去的实例顺手记进 window.__wrSounds（放完即摘），
// 只为了 stopSounds 能把“还在响的那几首”找出来停掉：Audio 不在 DOM 里，
// querySelectorAll 是找不到的。
const playSoundScript = "(()=>{const L=(window.__wrSounds=window.__wrSounds||[]);" +
	"const a=new Audio(%s);a.volume=%s;" +
	"a.onended=()=>{const i=L.indexOf(a);if(i>=0)L.splice(i,1)};" +
	"L.push(a);a.play().catch(()=>{})})()"

// stopSoundsScript 停掉本程序放出去、还在响的所有实例。设置页里的“试听”是页面自己的
// <audio>，不在这份清单里 —— 试听不会被一次换窗掐断。
//
// 不直接 pause：从 0.6 的音量上硬切会“咔”一声（波形在半途被截断）。用 120 ms 线性
// 淡出（6 × 20 ms）再停，听感就是自然地收掉。清单在同一个 tick 里就清空（淡出独立在
// 跑）—— 紧接着新放的“开始音”不该被这次停音带走。
const stopSoundsScript = "(()=>{const L=window.__wrSounds||[];" +
	"for(const a of L){try{const v=a.volume;let k=0;" +
	"const t=setInterval(()=>{k++;a.volume=Math.max(0,v*(1-k/6));" +
	"if(k>=6){clearInterval(t);try{a.pause();a.currentTime=0}catch(e){}}},20)}" +
	"catch(e){try{a.pause()}catch(e2){}}}" +
	"L.length=0})()"

// execPlaySound 把播放指令发给承载 <audio> 的窗口。
func (a *App) execPlaySound(url string, volume float64) {
	w := a.audioHostWindow()
	if w == nil {
		return
	}
	// 纯 JS，不带注释；url 用 strconv.Quote 转义（与 JS 字符串字面量兼容）
	w.ExecJS(fmt.Sprintf(playSoundScript,
		strconv.Quote(url), strconv.FormatFloat(volume, 'f', -1, 64)))
}

// stopSounds 掐掉还在响的提示音：接力换窗 / 长休息抢占，紧接着就要放新的一声，
// 上一首（尤其是导入的几分钟音乐）不掐掉只会和新的那首叠在一起。
//
// 无头构建与单测里没有播放宿主，直接返回（与 playSoundFile 同一口径）。
func (a *App) stopSounds() {
	if serverMode || a.app == nil {
		return
	}
	w := a.audioHostWindow()
	if w == nil {
		return
	}
	w.ExecJS(stopSoundsScript)
}

// setRelayQuiet 置位 / 撤销“换窗整段静音”：这一次收窗不播结束音（relayQuietEnd），
// 紧接着那一次建会话也不播开始音（relayQuietStart）。只在 schedEndBreak 里一置一撤；
// 两个标记各由消费方读走即清（closeBreakWindowsOpt / ShowBreakSession），所以即使
// EndBreak 是空操作（相位已经翻篇），也不会把静音漏给下一次正常到点。
func (a *App) setRelayQuiet(v bool) {
	a.mu.Lock()
	a.relayQuietEnd = v
	a.relayQuietStart = v
	a.mu.Unlock()
}

// takeRelayQuietStart 读走“这次建会话不出声”的标记（读一次就清）
func (a *App) takeRelayQuietStart() bool {
	a.mu.Lock()
	v := a.relayQuietStart
	a.relayQuietStart = false
	a.mu.Unlock()
	return v
}

// audioHostWindow 挑一个承载 <audio> 的窗口：优先主设置窗口（main.go 里随应用创建、
// “关闭”只是 Hide 不销毁，全程存活、已加载且用户交互过），退回专用宿主窗。
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
		return nil // 别把“类型化 nil”当接口返回，否则上层 w==nil 判不出
	}
	return hw
}

// ---------- 绑定给渲染层的接口 ----------

// SoundFileURL 把音频文件换算成可播放 URL（设置页试听用 <audio>、播放宿主用 Audio）。
// 内置音（相对文件名）→ 资源服务的 /sounds/xxx.wav；自定义音（绝对路径）→ 本进程的
// /__sound/?p=…（白名单见 allowedSoundPath）。**文件不可用返回空串** ——
// 渲染层正是用 `if (!url)` 判“找不到该音频文件”的。
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
