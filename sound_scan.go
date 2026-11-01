package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"workerrest/internal/jsonx"
)

// 随包提示音目录（sounds/）的启动体检。
//
// 为什么需要它：sounds/ 是 exe 旁的随包目录，用户想加自己的提示音，最自然的动作就是
// 把文件丢进去 —— 但程序只播「配置里登记过的条目」，丢进去等于没用。反过来的情形更
// 常见：用户清理磁盘时删掉了某个已登记的文件，配置里就留下一条永远播不出来的死条目
// （试听点了没反应，界面上还没有任何解释）。
//
// 两种都在启动时扫一次，然后**只记录、绝不擅自改配置** —— 由设置页弹窗逐项问用户
// （与统计文件自愈同一套「先告知、再让用户决定」的口径；开机自启时完全安静）。

// SoundScanFile 扫到的新音频：随包 sounds/ 里有、音乐列表里没登记
type SoundScanFile struct {
	// File 相对文件名。用户同意后 music[].file 就写这个值 —— 播放走 /sounds/ 资源
	// 路由（serveBuiltinSound），删除条目时也不会去删这个随包文件（见 deleteMusicSound
	// 只删音频目录内的绝对路径）。
	File string `json:"file"`
	// Name 去掉扩展名的显示名（与 normalizeSound 给缺名条目补名的口径一致）
	Name string `json:"name"`
	// Size 字节数：弹窗里显示，用户凭大小能认出是哪一个文件
	Size int64 `json:"size"`
}

// SoundScanMissing 配置里登记了、文件却已经不在的条目
type SoundScanMissing struct {
	ID   string `json:"id"`
	File string `json:"file"`
	Name string `json:"name"`
}

// SoundScanNotice 启动扫描结果。两项都空时整份 notice 不成立（不打扰用户）。
type SoundScanNotice struct {
	// Dir 被扫描的随包提示音目录（弹窗里显示，用户知道该往哪看）
	Dir     string             `json:"dir"`
	New     []SoundScanFile    `json:"new"`
	Missing []SoundScanMissing `json:"missing"`
}

// empty 没有任何需要用户决定的事
func (n *SoundScanNotice) empty() bool {
	return n == nil || (len(n.New) == 0 && len(n.Missing) == 0)
}

// ---------- 扫描 ----------

// scanSoundDir 扫随包 sounds/，算出「新发现的音频」与「已失效的配置条目」。
//
// 「已登记」= 内置 4 条的固定文件名（无论有没有种进 music 列表，它们都由内置音逻辑
// 直接使用）∪ 配置里 file 为相对名的那些条目。路径比较一律小写（Windows 不区分）。
func (a *App) scanSoundDir() SoundScanNotice {
	out := SoundScanNotice{Dir: builtinSoundDir()}

	registered := make(map[string]bool, len(builtinMusic)+4)
	for _, b := range builtinMusic {
		registered[strings.ToLower(b.file)] = true
	}
	var cfg SoundCfg
	if s := a.cfgSnapshot(); s != nil {
		cfg = s.SoundCfg()
	}
	for _, m := range cfg.Music {
		if m.File == "" || filepath.IsAbs(m.File) {
			continue
		}
		registered[strings.ToLower(filepath.Base(m.File))] = true
	}

	// 目录里的新文件。不可播放的一律不收：扩展名不在白名单、0 字节、超过导入上限
	// —— 与 soundFileUsable 同口径，否则「导入了却播不出」比不提示更糟。
	if entries, err := os.ReadDir(out.Dir); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			// 跳过隐藏文件与 Office/资源管理器的临时文件（~$xxx）
			if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "~$") {
				continue
			}
			if !soundExtSupported(name) {
				continue
			}
			if registered[strings.ToLower(name)] {
				continue
			}
			info, err := e.Info()
			if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxMusicBytes {
				continue
			}
			out.New = append(out.New, SoundScanFile{
				File: name,
				Name: soundDisplayName(name),
				Size: info.Size(),
			})
		}
	} else if !os.IsNotExist(err) {
		log.Printf("[sound] 扫描提示音目录失败: %v", err)
	}
	sort.Slice(out.New, func(i, j int) bool {
		return strings.ToLower(out.New[i].File) < strings.ToLower(out.New[j].File)
	})

	// 配置里点名、但文件已经不在了的条目（相对名查随包目录，绝对路径查磁盘）
	for _, m := range cfg.Music {
		if soundFileUsable(m.File) {
			continue
		}
		name := m.Name
		if strings.TrimSpace(name) == "" {
			name = soundDisplayName(m.File)
		}
		out.Missing = append(out.Missing, SoundScanMissing{ID: m.ID, File: m.File, Name: name})
	}
	return out
}

// soundDisplayName 从文件名推显示名（去目录、去扩展名；去空了就退回原名）
func soundDisplayName(file string) string {
	base := filepath.Base(file)
	if i := strings.LastIndex(base, "."); i > 0 {
		base = base[:i]
	}
	if strings.TrimSpace(base) == "" {
		return filepath.Base(file)
	}
	return base
}

// startSoundScan 启动时扫一次（只记录，等设置页来取）
func (a *App) startSoundScan() {
	n := a.scanSoundDir()
	if n.empty() {
		log.Printf("[sound] 随包提示音目录 %s：无变化", n.Dir)
		return
	}
	a.soundScanMu.Lock()
	a.soundScan = &n
	a.soundScanMu.Unlock()
	if len(n.New) > 0 {
		names := make([]string, 0, len(n.New))
		for _, f := range n.New {
			names = append(names, f.File)
		}
		log.Printf("[sound] 随包提示音目录 %s 发现 %d 个未登记音频（等设置页询问）：%v",
			n.Dir, len(n.New), names)
	}
	if len(n.Missing) > 0 {
		names := make([]string, 0, len(n.Missing))
		for _, m := range n.Missing {
			names = append(names, m.Name)
		}
		log.Printf("[sound] 有 %d 条提示音的文件已不存在（等设置页询问）：%v", len(n.Missing), names)
	}
}

// ---------- 绑定（设置页弹窗） ----------

// GetSoundScanNotice 启动扫描结果。没有变化返回 null（渲染层用 `if (!n) return` 判空）。
func (a *App) GetSoundScanNotice() map[string]any {
	a.soundScanMu.Lock()
	defer a.soundScanMu.Unlock()
	if a.soundScan.empty() {
		return nil
	}
	return map[string]any{
		"dir":     a.soundScan.Dir,
		"new":     a.soundScan.New,
		"missing": a.soundScan.Missing,
	}
}

// ImportScannedSounds 把扫到的新音频登记进音乐列表。
//
// 本质是「原地登记」：文件本来就在随包目录里，所以不复制、不改名，music[].file 直接
// 写相对文件名。若改成复制一份副本，原文件会留在目录里 —— 下次启动又被当成「新音频」，
// 变成每次开机都要问一遍的循环。
//
// 入参只认「这次扫出来过」的文件名：前端传别的值一律丢弃，避免任意字符串被写进配置。
func (a *App) ImportScannedSounds(files []string) map[string]any {
	a.soundScanMu.Lock()
	candidates := make(map[string]SoundScanFile)
	if a.soundScan != nil {
		for _, f := range a.soundScan.New {
			candidates[strings.ToLower(f.File)] = f
		}
	}
	a.soundScanMu.Unlock()
	if len(candidates) == 0 {
		return map[string]any{"ok": true, "imported": 0}
	}

	want := make([]SoundScanFile, 0, len(files))
	seen := make(map[string]bool, len(files))
	for _, f := range files {
		key := strings.ToLower(filepath.Base(strings.TrimSpace(f)))
		if key == "" || seen[key] {
			continue
		}
		if hit, ok := candidates[key]; ok {
			seen[key] = true
			want = append(want, hit)
		}
	}
	if len(want) == 0 {
		return map[string]any{"ok": true, "imported": 0}
	}

	added := make([]string, 0, len(want))
	ok := a.mutateSoundMusic(func(music []any) ([]any, bool) {
		have := make(map[string]bool, len(music))
		for _, it := range music {
			if m, ok := it.(map[string]any); ok {
				have[strings.ToLower(jsonx.JsStr(m["file"]))] = true
			}
		}
		for _, f := range want {
			key := strings.ToLower(f.File)
			if have[key] {
				continue // 已经登记过（连点两次导入）
			}
			have[key] = true
			music = append(music, map[string]any{
				"id":      "sm-" + jsonx.NewRandomID(),
				"file":    f.File,
				"name":    f.Name,
				"enabled": true,
			})
			added = append(added, f.File)
		}
		return music, len(added) > 0
	})
	if ok {
		// 写盘成功才划掉待办：失败时留着，用户重开设置页还能再试一次
		a.dropScanEntries(added, nil)
		log.Printf("[sound] 已导入 %d 个随包提示音：%v", len(added), added)
	}
	return map[string]any{"ok": ok, "imported": len(added)}
}

// DropMissingSounds 把「文件已不在」的条目从配置里删掉。
//
// 只删配置条目 —— 文件本来就不在了，没有可删的文件。singleId 若指向被删条目，
// 由 normalizeForSave 里的 normalizeSound 兜底（迁移到老字段 / 指向第一首启用项）。
func (a *App) DropMissingSounds(ids []string) map[string]any {
	a.soundScanMu.Lock()
	candidates := make(map[string]bool)
	if a.soundScan != nil {
		for _, m := range a.soundScan.Missing {
			candidates[m.ID] = true
		}
	}
	a.soundScanMu.Unlock()
	if len(candidates) == 0 {
		return map[string]any{"ok": true, "removed": 0}
	}

	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if candidates[id] {
			want[id] = true
		}
	}
	if len(want) == 0 {
		return map[string]any{"ok": true, "removed": 0}
	}

	removed := make([]string, 0, len(want))
	ok := a.mutateSoundMusic(func(music []any) ([]any, bool) {
		keep := make([]any, 0, len(music))
		for _, it := range music {
			if m, isMap := it.(map[string]any); isMap && want[jsonx.JsStr(m["id"])] {
				removed = append(removed, jsonx.JsStr(m["id"]))
				continue
			}
			keep = append(keep, it)
		}
		return keep, len(removed) > 0
	})
	if ok {
		a.dropScanEntries(nil, removed)
		log.Printf("[sound] 已从配置删除 %d 条文件缺失的提示音条目：%v", len(removed), removed)
	}
	return map[string]any{"ok": ok, "removed": len(removed)}
}

// DismissSoundScan 用户选了「暂不导入 / 保留」：本次运行不再询问。
//
// 刻意不持久化：下次启动会重新扫，文件还在就再问一次 —— 用户可能只是这次不想动，
// 而「永远不再问」反过来会让死条目一直留着、新文件一直用不上。
func (a *App) DismissSoundScan() map[string]any {
	a.soundScanMu.Lock()
	a.soundScan = nil
	a.soundScanMu.Unlock()
	log.Printf("[sound] 用户选择暂不处理提示音目录变化，本次运行不再询问")
	return map[string]any{"ok": true}
}

// ---------- 内部 ----------

// dropScanEntries 从待办里划掉已处理的项；两项都空就整份收掉（本次运行不再问）
func (a *App) dropScanEntries(files, ids []string) {
	fset := make(map[string]bool, len(files))
	for _, f := range files {
		fset[strings.ToLower(f)] = true
	}
	iset := make(map[string]bool, len(ids))
	for _, id := range ids {
		iset[id] = true
	}

	a.soundScanMu.Lock()
	defer a.soundScanMu.Unlock()
	if a.soundScan == nil {
		return
	}
	if len(fset) > 0 {
		keep := a.soundScan.New[:0]
		for _, f := range a.soundScan.New {
			if !fset[strings.ToLower(f.File)] {
				keep = append(keep, f)
			}
		}
		a.soundScan.New = keep
	}
	if len(iset) > 0 {
		keep := a.soundScan.Missing[:0]
		for _, m := range a.soundScan.Missing {
			if !iset[m.ID] {
				keep = append(keep, m)
			}
		}
		a.soundScan.Missing = keep
	}
	if a.soundScan.empty() {
		a.soundScan = nil
	}
}

// mutateSoundMusic 在 a.mu 内改 sound.music 并落盘（与 SaveSettings 同口径：写盘成功才
// 整体替换 a.cfg）。fn 收到当前列表的深拷贝，返回新列表与是否有改动。
//
// 为什么要深拷贝：配置是 map[string]any 的树，deepMerge 只递归对象、数组整体替换，
// 数组里的元素仍是同一份引用 —— 直接改元素会污染「已经发出去的那份配置」。
func (a *App) mutateSoundMusic(fn func(music []any) ([]any, bool)) bool {
	a.mu.Lock()
	cur := a.cfg
	if cur == nil {
		a.mu.Unlock()
		return false
	}
	raw := jsonCloneMap(cur.Raw())
	if raw == nil {
		a.mu.Unlock()
		log.Printf("[sound] 配置深拷贝失败，放弃修改音乐列表")
		return false
	}
	snd, _ := raw["sound"].(map[string]any)
	if snd == nil {
		snd = map[string]any{}
		raw["sound"] = snd
	}
	next, changed := fn(jsonx.AnySlice(snd["music"]))
	if !changed {
		a.mu.Unlock()
		return false
	}
	snd["music"] = next
	normalizeForSave(raw)
	s := &Settings{raw: raw, path: cur.Path()}
	if err := s.Save(); err != nil {
		a.mu.Unlock()
		log.Printf("[sound] 保存音乐列表失败: %v", err)
		return false
	}
	a.cfg = s
	a.mu.Unlock()
	// 状态推送必须在解锁后：GetState → cfgSnapshot 要拿同一把锁。
	// 单测里只搭了配置（a.app 为空），跳过推送。
	if a.app != nil {
		a.pushState()
	}
	return true
}

// jsonCloneMap 深拷贝一份配置（JSON 往返）。配置不大，且只在用户点按钮时才走这条路。
func jsonCloneMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil
	}
	return out
}
