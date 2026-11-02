// Package sound 是提示音的纯逻辑：从配置里挑本次该放哪一首（单曲循环 / 随机 / 顺序），
// 以及用户音频文件与内置音的文件命名规则。
//
// 不含播放 —— 真正解码与输出在 cmd 层的 sound_host.go（依赖 Wails 的音频通道）。
package sound

import (
	"os"
	"path/filepath"
	"strings"

	"workerrest/internal/model"
)

// 提示音选曲与文件名规则（纯逻辑，零 App 依赖）。
//
// 音频分两类：内置提示音（相对文件名，随包 sounds/ 目录）+ 用户导入的音乐（绝对路径，
// 副本放在配置目录旁）。选曲口径与 sound.ts 逐字对齐：
//   · single 模式播 singleId 指向的一条（条目停用也照播，被删/文件不可用 = 静音）；
//   · random / sequence 只在「启用且文件存在」的条目里挑；全不可用不回退（刻意静音）。

const (
	// MaxMusicBytes 导入上限 20 MB：每次播放都要读盘，过大的文件会拖慢提醒弹出。
	MaxMusicBytes = 20 * 1024 * 1024

	// builtinSoundRelDir 内置提示音的随包目录名（未注入时的兜底相对路径）。
	builtinSoundRelDir = "sounds"
)

// Exts 允许的音频扩展名（与 sound.ts 的 CUSTOM_SOUND_EXTS 逐字一致：
// Chromium 能解码的常见格式）。
var Exts = []string{"wav", "mp3", "ogg", "oga", "m4a", "aac", "flac", "opus", "webm"}

// builtinDirFn 内置提示音随包目录的注入点：App 启动时注入 <resourcesRoot>/sounds
// （打包态 exe 旁、开发态模块根）；单测可指到临时目录。
// 与 settings 包的 resourcesRootHint 同一套路 —— 避免包级函数反向依赖 App。
var builtinDirFn func() string

// SetBuiltinDirFn 注入内置提示音随包目录（打包态 exe 旁、开发态模块根）。
func SetBuiltinDirFn(fn func() string) { builtinDirFn = fn }

// builtinDir 内置提示音目录；未注入时兜底相对路径（单测的 cwd 就是模块根）。
func builtinDir() string {
	if builtinDirFn != nil {
		return builtinDirFn()
	}
	return builtinSoundRelDir
}

// BuiltinDir 内置提示音目录（打包态 exe 旁的 sounds/，开发态模块根）。
// 供 main 包在需要直接定位随包目录时使用（如扫描、交付内置音）。
func BuiltinDir() string { return builtinDir() }

// Supported 扩展名是否在白名单里（大小写不敏感；无扩展名 = 不支持）。
func Supported(name string) bool {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))
	if ext == "" {
		return false
	}
	for _, e := range Exts {
		if e == ext {
			return true
		}
	}
	return false
}

// BuiltinExists 内置提示音在不在随包 sounds/ 目录里（磁盘判定）。
// 文件名只取最后一段再拼进目录，杜绝 ../ 这类路径穿越；空文件视为不存在
// （与 FileUsable 同口径 —— 播一个 0 字节的 wav 只会静默失败）。
func BuiltinExists(name string) bool {
	name = filepath.Base(name)
	if !Supported(name) {
		return false
	}
	st, err := os.Stat(filepath.Join(builtinDir(), name))
	return err == nil && st.Mode().IsRegular() && st.Size() > 0
}

// FileUsable 这个文件现在能不能播：内置看随包资源，自定义看磁盘上是不是常规文件
// 且在大小上限内。**空文件也算不可用** —— 播一个 0 字节的 wav 只会静默失败。
func FileUsable(file string) bool {
	if !filepath.IsAbs(file) {
		return BuiltinExists(file)
	}
	st, err := os.Stat(file)
	if err != nil || !st.Mode().IsRegular() {
		return false
	}
	return st.Size() > 0 && st.Size() <= MaxMusicBytes
}

// ResolveSoundFile 单曲循环要播的文件：singleId 指向的那一条。条目不存在 / 没有 file /
// 文件不可用都返回空串 = 静音（与 sound.ts 的 resolveSoundFile 同口径）。
// 注意「条目是否被停用」与这里无关 —— 停用只影响 random / sequence 的选池。
// singleId 为空 = 用户没选过单曲，按契约静音（默认值由 normalizeSound 在首次种入时补上）。
func ResolveSoundFile(cfg model.SoundCfg) string {
	for _, m := range cfg.Music {
		if m.ID != cfg.SingleID || cfg.SingleID == "" {
			continue
		}
		if strings.TrimSpace(m.File) == "" {
			return ""
		}
		if !FileUsable(m.File) {
			return ""
		}
		return m.File
	}
	return ""
}

// EnabledMusicFiles 音乐列表里「启用 + 文件仍可用」的路径，按列表顺序。
// random / sequence 只从这里挑；顺序游标也按这个顺序走。
func EnabledMusicFiles(cfg model.SoundCfg) []string {
	out := make([]string, 0, len(cfg.Music))
	for _, m := range cfg.Music {
		if !m.Enabled || strings.TrimSpace(m.File) == "" {
			continue
		}
		if !FileUsable(m.File) {
			continue
		}
		out = append(out, m.File)
	}
	return out
}

// SameDir 两个目录是不是同一个（Windows 不区分大小写）。
func SameDir(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}
