package main

import (
	"net/url"
	"path/filepath"
	"workerrest/internal/model"
	"workerrest/internal/settings"
	"workerrest/internal/sound"
)

// 提示音：历史源码/sound.ts 的对应物。
//
// 为什么要有“播放宿主窗口”：主进程自己放不了声音。上一版开一个 1×1 的隐藏
// BrowserWindow 承载 <audio>；这里同理 —— Wails 的隐藏窗口加载 sound.html，
// 播放时向它 ExecJS 一句 `new Audio(url).play()`。
//
// 音频分两类，来源与交付方式都不同：
//
//	内置提示音（四个 wav，short/long × start/end）—— 相对文件名。
//	  不再 embed 进二进制：打包时随包放在 exe 旁的 sounds/（make_portable.py 预置），
//	  开发态是模块根的 sounds/。播放 URL 是 /sounds/xxx.wav，由 soundMiddleware 从
//	  随包目录磁盘交付（见 serveBuiltinSound）。
//	用户导入的音乐 —— 绝对路径，副本放在**配置目录旁的 sounds/**（见 customSoundDir）。
//	  这些文件与随包目录无关，走本文件自带的一条只读路由 /__sound/?p=…（见
//	  soundMiddleware）。
//
// 那条路由**只服务白名单内的文件**（音频目录下的副本，或当前配置音乐列表里点名的绝对
// 路径），其余一律 403。它是本机回环上的一个 HTTP 端点，绝不做任意文件读取服务。
//
// 与上一版 逐条对齐的三处：
//
// 一、选曲口径“单曲循环与启停无关”：single 模式播 singleId 指向的那一条（条目被停用
// 也照样播，被删 / 文件不可用 = 静音）；random / sequence 只在**启用且文件存在**的条目
// 里挑。全部不可用时不回退 —— 用户把列表停用 / 删空就是刻意静音。
//
// 二、顺序播放的游标跨播放持续递增（不是每次从头开始），所以连续两次播放取的是列表里
// 相邻的两条。
//
// 三、导入副本的删除只认“音频目录里**直接**躺着的那一层”，子目录与别处的文件一律不碰。

const (
	// soundURLPrefix 自定义音资源路由前缀（见 soundMiddleware）；带 query 参数 p
	soundURLPrefix = "/__sound/"

	// builtinSoundURLDir 内置提示音对外的 URL 前缀（soundMiddleware 按随包目录磁盘交付）
	builtinSoundURLDir = "/sounds/"

	// soundHostWindowName 播放宿主窗口名（Wails 按名字交付 / 查找窗口）
	soundHostWindowName = "sound"
)

// soundMimeByExt 资源路由响应的 Content-Type（Chromium 按扩展名基本也能猜对，
// 显式给出是为了不让某些格式落到 application/octet-stream 上）
var soundMimeByExt = map[string]string{
	"wav": "audio/wav", "mp3": "audio/mpeg", "ogg": "audio/ogg", "oga": "audio/ogg",
	"m4a": "audio/mp4", "aac": "audio/aac", "flac": "audio/flac",
	"opus": "audio/ogg", "webm": "audio/webm",
}

// soundPickNext 按配置挑下一首要播的音频，返回空串表示这次刻意静音。游标（顺序播放）
// 会随手推进 —— 它记在 App 上，跨播放持续递增（与 sound.ts 的模块级 seqCursor 同义）。
//
// rnd 是随机源注入点：生产用 math/rand 全局函数（自带锁、并发安全），单测传一个定值
// 函数就能把 random 分支变成可断言的。
func (a *App) soundPickNext(cfg model.SoundCfg, rnd func(n int) int) string {
	if !cfg.Enabled || cfg.Volume <= 0 {
		return ""
	}
	if cfg.Mode == "random" || cfg.Mode == "sequence" {
		files := sound.EnabledMusicFiles(cfg)
		if len(files) == 0 {
			return "" // 全部停用 / 删空：用户的选择，静音
		}
		if cfg.Mode == "random" {
			return files[rnd(len(files))]
		}
		a.mu.Lock()
		cur := a.soundCursor
		a.soundCursor++
		a.mu.Unlock()
		return files[cur%len(files)]
	}
	return sound.ResolveSoundFile(cfg)
}

// ---------- 路径 ----------

// customSoundDir 用户导入音频的副本目录。
//
// 上一版是 userDataPath('sounds', 'sounds', false)：非便携 = <userData>/sounds，
// 便携 = <运行目录>/sounds/sounds（category 与 rel 同名，结果多一层）。
// Go 版**统一成“配置文件所在目录旁的 sounds/”**：也就是 <运行目录>/config/sounds
// （配置在 config/settings.json），数据与程序同目录。
// 这处差异的可见影响只有“便携老用户的旧副本删不掉”（播放不受影响 —— 配置里存的是
// 绝对路径），已记进 README 的已知差异。
func (a *App) customSoundDir() string {
	s := a.CfgSnapshot()
	if s != nil {
		if p := s.Path(); p != "" {
			return filepath.Join(filepath.Dir(p), "sounds")
		}
	}
	// 还没有配置文件：按候选路径（= 唯一的 <运行目录>/config/settings.json）推导
	cands := settings.Candidates(a.resourcesRoot())
	if len(cands) == 0 {
		return ""
	}
	return filepath.Join(filepath.Dir(cands[len(cands)-1]), "sounds")
}

// builtinSoundURL 内置提示音的播放 URL（文件名只取最后一段，杜绝 ../ 这类拼接）
func builtinSoundURL(file string) string {
	return builtinSoundURLDir + url.PathEscape(filepath.Base(file))
}
