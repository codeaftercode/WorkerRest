package main

import (
	"log"
	"strings"
	"time"

	"workerrest/internal/model"
	"workerrest/internal/settings"
	"workerrest/internal/winproc"
)

// 询问窗的答复与“记住这个应用”的落盘（对应 fsask.js 的 window.api.fullScreenAskAnswer）。

// 四个答复的键名（与 fsask.js 的按钮 data-answer 一一对应）。
const (
	fsAnswerPause = "pause" // 暂停（本次全屏期间）
	fsAnswerSkip  = "skip"  // 本次不暂停（本段不再问）
	fsAnswerToday = "today" // 今天本应用不暂停（本地 24:00 失效）
	fsAnswerNever = "never" // 本应用永不暂停（进名单）
)

// FullScreenAskAnswer 答复询问窗。未知 answer / 没有待答复的询问时静默返回 nil ——
// 迟到的点击（窗口已因超时或段结束关掉）不该在界面上报错。
func (a *App) FullScreenAskAnswer(answer string) error {
	switch answer {
	case fsAnswerPause, fsAnswerSkip, fsAnswerToday, fsAnswerNever:
	default:
		log.Printf("[fsAsk] 忽略未知答复 %q", answer)
		return nil
	}

	a.fsAskMu.Lock()
	if !a.fsAsk.pending {
		a.fsAskMu.Unlock()
		return nil
	}
	a.fsAsk.pending = false
	needLog := !a.fsAsk.logged
	a.fsAsk.logged = true
	exe, label := a.fsAsk.exe, a.fsAsk.label
	a.fsAskMu.Unlock()

	switch answer {
	case fsAnswerPause:
		a.pauseFullScrBusyNow(needLog, "用户选择暂停")
	case fsAnswerToday:
		a.rememberFullScreenSkip(exe, label, model.FullScreenSkipToday, nextLocalMidnightMs())
	case fsAnswerNever:
		a.rememberFullScreenSkip(exe, label, model.FullScreenSkipNever, 0)
	case fsAnswerSkip:
		log.Printf("[fsAsk] %s：本次不暂停（本段不再询问）", label)
	}
	a.closeFSAskWindow()
	return nil
}

// nextLocalMidnightMs 下一个本地零点（毫秒）。"今天不再问"的失效时刻 ——
// 用本地时区的 24:00，而不是“now + 24 小时”：后者会让用户在凌晨 1 点选的“今天”
// 一直有效到第二天凌晨 1 点。
func nextLocalMidnightMs() int64 {
	now := time.Now()
	y, m, d := now.Date()
	return time.Date(y, m, d+1, 0, 0, 0, 0, now.Location()).UnixMilli()
}

// rememberFullScreenSkip 把某个 exe 写进“全屏时不暂停”名单（整体替换后保存）。
//
// 走 SaveSettings 而不是只改内存：设置页与诊断页读的是同一份配置，只改内存会让
// “名单里有、设置页看不到”。代价是这一次保存会带上一串副作用（推送状态、重算计划、
// 重注册快捷键）—— 但一次询问最多发生一次，完全可接受。
func (a *App) rememberFullScreenSkip(exe, label, mode string, until int64) {
	exe = strings.TrimSpace(exe)
	if exe == "" || winproc.IsHostProcess(exe) {
		// 宿主进程（ApplicationFrameHost / rundll32 …）不可标记：写进去等于放行一大片
		// 不相干的应用。前端已禁用这两个按钮，这里是兜底。
		log.Printf("[fsAsk] 拒绝把系统宿主进程写进名单：%q", exe)
		return
	}
	items := a.CfgSnapshot().FullScreenSkipItems()
	raw := make([]any, 0, len(items)+1)
	replaced := false
	for _, it := range items {
		if strings.EqualFold(it.Exe, exe) {
			it.Mode, it.Until = mode, until
			if label != "" {
				it.Label = label
			}
			replaced = true
		}
		raw = append(raw, map[string]any{
			"exe": it.Exe, "label": it.Label, "mode": it.Mode, "until": it.Until,
		})
	}
	if !replaced {
		raw = append(raw, map[string]any{
			"exe": exe, "label": label, "mode": mode, "until": until,
		})
	}
	if !a.saveFullScreenSkipOnly(raw) {
		log.Printf("[fsAsk] 记住 %s（%s）失败：配置没写进去", exe, mode)
		return
	}
	// 名单变了：调度计划与设置页都要立刻看到（下一条到点判定依赖它）。
	// 不走 SaveSettings 的原因见 saveFullScreenSkipOnly 的注释。
	if a.sched != nil {
		a.sched.RefreshPlan()
	}
	if a.app != nil {
		a.pushState()
		a.app.Event.Emit("fsask:list-changed")
	}
	if mode == model.FullScreenSkipToday {
		log.Printf("[fsAsk] %s：今天不再询问（到 %s 失效）", label,
			time.UnixMilli(until).Format("2006-01-02 15:04"))
	} else {
		log.Printf("[fsAsk] %s：已加入“全屏时不暂停”名单（永不暂停）", label)
	}
}

// saveFullScreenSkipOnly 只把 fullscreenSkipApps 子树写进配置文件。
//
// 为什么不走 SaveSettings：那条路会顺带重注册全局快捷键、广播 autostart、推整份状态，
// 而这里只是用户点了一下“以后别问”—— 为一次名单增项去重注册热键既浪费也会打断用户
// 正在按的组合键。做法与 saveShortcutsOnly 完全一致（合并 → 深拷 → 归一化 → 落盘）。
func (a *App) saveFullScreenSkipOnly(items []any) bool {
	a.mu.Lock()
	merged, _ := settings.DeepMerge(a.cfg.Raw(), map[string]any{"fullscreenSkipApps": items}).(map[string]any)
	if merged == nil {
		a.mu.Unlock()
		log.Printf("[fsAsk] 名单保存被忽略：合并结果为空")
		return false
	}
	// DeepMerge 对未被覆盖的嵌套 map 是直接搬引用，归一化会原地写 —— 必须先深拷
	if cloned := jsonCloneMap(merged); cloned != nil {
		merged = cloned
	}
	keepWindowLastSize(a.cfg.Raw(), merged)
	settings.NormalizeForSave(merged)
	s := settings.New(merged, a.cfg.Path())
	if err := s.Save(); err != nil {
		a.mu.Unlock()
		log.Printf("[fsAsk] 名单保存失败: %v", err)
		return false
	}
	a.cfg = s
	a.mu.Unlock()
	return true
}
