package main

import (
	"log"
	"os"
	"path/filepath"
	"strings"

	"workerrest/internal/settings"
	"workerrest/internal/stats"
)

// appStatsFile 统计文件落点。优先级：
//  1. `WR_STATS_PATH`：显式指定（冒烟 / 单测钉死落点）；
//  2. `WR_SETTINGS_PATH`：测试用它把配置隔离到临时目录，统计跟着走同一目录 ——
//     这一步保证「冒烟永远碰不到用户的真实统计文件」，不必依赖调用方记得设置。
//     刻意**优先于**便携推导：测试目录名恰好叫 config 时也不会跑到别处去；
//  3. 按当前配置文件的路径推导（见 stats.StatsFileForConfig）；
//  4. 还没找到配置文件时，按候选路径（= 唯一的 <运行目录>/config/settings.json）推导，
//     与 sound.go 同口径。
func (a *App) appStatsFile() string {
	if p := strings.TrimSpace(os.Getenv("WR_STATS_PATH")); p != "" {
		return p
	}
	if sp := strings.TrimSpace(os.Getenv("WR_SETTINGS_PATH")); sp != "" {
		return filepath.Join(filepath.Dir(sp), "stats.json")
	}
	cfgPath := ""
	if s := a.CfgSnapshot(); s != nil {
		cfgPath = s.Path()
	}
	if cfgPath == "" {
		cands := settings.Candidates(a.resourcesRoot())
		if len(cands) > 0 {
			cfgPath = cands[len(cands)-1]
		}
	}
	return stats.StatsFileForConfig(cfgPath)
}

// statsStore 取统计存储（首次访问时建好并读盘）。
//
// **读盘必须在 App 的锁之外**：Load 里要回读配置路径（appStatsFile → cfgSnapshot → a.mu），
// 持锁调用会自死锁 —— 现实路径是 idleWatchTick → statsStore → Load → appStatsFile。
func (a *App) statsStore() *stats.StatsStore {
	a.mu.Lock()
	st := a.stats
	created := false
	if st == nil {
		st = stats.NewStatsStore(a.appStatsFile)
		a.stats = st
		created = true
	}
	a.mu.Unlock()
	// onChange 在 App 锁之外注入：SetOnChange 内部要拿 st.mu，而 st.mu 侧有一条
	// 「Flush → appStatsFile → a.mu」的反向路径，两个方向锁序相反会留下死锁窗口。
	if created {
		// 数据一变就把今天的快照推给渲染层（统计报表「今天」卡实时刷新）。
		// 回调在 notifier goroutine 里跑、**不持任何锁**，只读一份快照再广播。
		st.SetOnChange(func() {
			app := a.app
			if app == nil {
				return
			}
			app.Event.Emit("stats:changed", st.Today())
		})
	}
	st.LoadOnce()
	return st
}

// GetStats 使用统计（今天 + 周期汇总）。
// periods 是**数组**、顺序固定 week → month → quarter → year（见 stats.StatsStore.Periods）。
func (a *App) GetStats() map[string]any {
	st := a.statsStore()
	return map[string]any{
		"today":   st.Today(),
		"periods": st.Periods(),
	}
}

// GetHistory 历史明细（逐日记录）。days 按日期升序，customNames 是 id → 名字快照。
func (a *App) GetHistory(start, end string) map[string]any {
	st := a.statsStore()
	return map[string]any{
		"days":        st.DaysInRange(start, end),
		"customNames": st.CustomNames(),
	}
}

// ClearStats 清空全部统计数据（关于页 → 数据与隐私，渲染层已做二次确认）
func (a *App) ClearStats() map[string]any {
	a.statsStore().Clear()
	log.Printf("[stats] 已清空全部统计数据")
	return map[string]any{"ok": true}
}

// GetStatsFileNotice 统计文件损坏并被重建时的说明（渲染层打开设置页时来取）。
// 没有发生过（绝大多数情况）返回 nil —— 前端据此不弹窗。
func (a *App) GetStatsFileNotice() map[string]any {
	n := a.statsStore().FileNotice()
	if n == nil {
		return nil
	}
	return map[string]any{
		"file":   n.File,
		"backup": n.Backup,
		"reason": n.Reason,
		"at":     n.At,
	}
}

// AckStatsFileNotice 用户已看过「统计文件已重建」弹窗：清掉说明，本次运行不再弹。
func (a *App) AckStatsFileNotice() map[string]any {
	a.statsStore().AckFileNotice()
	return map[string]any{"ok": true}
}

// StatsProbe 统计自检（诊断页与无头冒烟用）：证明记账链路真的在跑，而不是只返回空表。
func (a *App) StatsProbe() map[string]any {
	st := a.statsStore()
	kind, elapsed := st.BreakLogInfo()
	return map[string]any{
		"file":        st.File(),
		"today":       st.Today(),
		"days":        len(st.DaysInRange("", "")),
		"autoFlush":   st.AutoFlushInfo(),
		"breakLog":    map[string]any{"type": kind, "elapsedMs": elapsed},
		"away":        st.AwayInfo(),
		"customNames": st.CustomNames(),
	}
}

// startStats 启动统计后台链路：周期落盘（1 分钟兜底）+ 每秒工作累加。
// 两条判定注入（依赖调度器与空闲状态机，stats.go 不反向依赖它们）。
func (a *App) startStats() {
	st := a.statsStore()
	st.StartAutoFlush(60_000)
	st.StartWorkAccumulator(a.statsIsBreak, a.statsIsIdle)
	st.StartNotify()
	log.Printf("[stats] 已启动（文件 %s）", st.File())
}

// stopStats 退出收尾：无条件写一次盘再停后台 goroutine
func (a *App) stopStats() {
	a.mu.Lock()
	st := a.stats
	a.mu.Unlock()
	if st == nil {
		return
	}
	st.Flush()
	st.Stop()
	log.Printf("[stats] 已落盘并停止")
}

// statsIsBreak 工作累加器的「这一跳归休息」判定：休息窗口正显示
func (a *App) statsIsBreak() bool {
	if a.sched == nil {
		return false
	}
	return a.sched.CurrentPhase() == "break"
}

// statsIsIdle 工作累加器的「这一跳归离开」判定：连续无输入已达阈值
// （与 idleWatchTick 同一口径；手动暂停 / 勿扰 / 应用占用都不算 —— 那时人还在用电脑）
func (a *App) statsIsIdle() bool {
	if a.sched == nil || a.idle == nil {
		return false
	}
	return a.idle.IdleSinceMs() >= int64(a.CfgSnapshot().IdlePauseMin())*60_000
}

// OnBreakPostponed / OnBreakSkipped 调度器宿主接口的实现（见 internal/scheduler 的 Host）：
// 推迟 / 跳过各记一笔统计。对应上一版的 scheduler.on('postpone' / 'skip')。
//
// **只认短休息 / 长休息**：调度器的 NextType 也可能是自定义提醒（id 形如 cb-…），
// 手动推迟、手动跳过、暂停休息期间按「跳过」处理的自定义提醒都会走到这两个回调，
// 但「今天卡 - 休息」这一组四项（完成 / 推迟 / 跳过 / 提前结束）只统计内置短、长休息。
// 外卖走的是独立的 RecordTakeoutAction，不经过这里。
func (a *App) OnBreakPostponed(kind string, postponeMs int64) {
	if kind != "short" && kind != "long" {
		return
	}
	a.statsStore().RecordBreakPostponed(kind, postponeMs)
}

func (a *App) OnBreakSkipped(kind string, skippedMs int64) {
	if kind != "short" && kind != "long" {
		return
	}
	a.statsStore().RecordBreakSkipped(kind, skippedMs)
}
