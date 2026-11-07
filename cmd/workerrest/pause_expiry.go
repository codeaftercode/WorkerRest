package main

import (
	"log"
	"strings"
	"time"

	"workerrest/internal/scheduler"
)

// 定时手动暂停：从“一直暂停到手动恢复”扩出四档（30 分钟 / 1 小时 / 2 小时 / 到明天 0 点）。
//
// 这一档解决的盲区是“软件检测不到、只能靠人自觉”的两类场景：有线**扩展**接投影仪
//（判据故意不认，见 README“无线的认得出，插线的认不出”）、会议室里笔记本屏被旁人
// 直接看到（不投屏也不共享，没有任何信号）。共同点是人**进会议室前就知道**接下来
// 一小时不该弹，所以让他在那一刻点一下，比让软件去猜准得多。
//
// 与“自动暂停”的关系必须分清：
//   - 自动暂停是软件判定“你现在不该被打断”，条件是外部状态，条件消失后可能自动恢复；
//   - 定时暂停是用户自己说了“到某个时刻为止”，到点就该结束 —— 这不是“自动恢复”，
//     是用户事先授权的到期。但到期仍要过一道闸：此刻若正在投屏 / 全屏 / 勿扰 / 人不在 /
//     会议软件还开着，就不恢复，等它结束（否则到点那一刻提醒会弹到投影仪上，
//     正是这个功能要防的事）。

// 定时暂停的档位（分钟）。“到明天”走绝对时刻，不在这张表里。
const (
	pauseFor30Min = 30
	pauseFor1Hour = 60
	pauseFor2Hour = 120
)

// nextMidnight 下一个本地零点（本日 24:00 = 明日 00:00）。
//
// 用 time.Date 而不是“+24h”：跨月、跨年、夏令时全交给它归一化。也没有“不足 1 小时
// 算次日”那类补丁 —— 零点是日期边界，“下一个零点”永远唯一：23:59 点得到 1 分钟后，
// 00:30 点得到 23.5 小时后，不会像“到明早 8 点”那样在 7:00–8:00 之间产生歧义。
func nextMidnight(now time.Time) time.Time {
	return time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, now.Location())
}

// pauseExpired 定时暂停是否已到期（deadline = 0 表示无限暂停，永不到期）。
func pauseExpired(deadline, now int64) bool {
	return deadline > 0 && now >= deadline
}

// pauseHoldReasons 到期时“此刻恢复会不会打扰”的原因（空 = 可以恢复）。
//
// 判据读的是**条件此刻是否成立**，不是 PauseReasons 里挂着什么：勿扰关掉之后原因还
// 留在 PauseReasons 里等用户点恢复，那种情况下“条件”其实已经没了，不该再挡住到期恢复。
// 唯一例外是空闲 —— 它没有“条件”可查，只能看原因还在不在（见 pauseActiveSnapshot）。
//
// 六条全算 —— 与“每小时提示”的四条压制不同：那张表问的是“屏幕会不会被别人看到”，
// 这张问“现在恢复会不会打扰”。人不在（空闲）恢复也没意义，会议软件还开着多半仍在会里。
func pauseHoldReasons(active map[string]bool) []string {
	var out []string
	for _, r := range autoPauseReasons {
		if active[r] {
			out = append(out, pauseReasonText(r))
		}
	}
	return out
}

// pauseHoldText 把阻挡原因拼成一处文案（空集返回空串）。
func pauseHoldText(reasons []string) string {
	return strings.Join(reasons, "、")
}

// schedPauseFor 托盘子菜单“暂停一段时间”：minutes > 0 定时暂停；<= 0 表示取消定时，
// 退回“一直暂停”。
//
// 未暂停时先走 Pause(manual)（与托盘“暂停休息”同一条路径，清提前提示窗等行为一致），
// 已暂停时只改到期时刻 —— 把 1 小时改成 2 小时不该让已过去的那段从头算。
func (a *App) schedPauseFor(minutes int) {
	if a.sched == nil {
		return
	}
	if minutes <= 0 {
		if a.sched.IsPaused() {
			a.sched.PauseUntil(0)
			log.Printf("[pause] 取消定时，改为一直暂停")
		}
		return
	}
	a.sched.PauseUntil(nowMs() + int64(minutes)*60_000)
	// 新一段（或改时长）开始：上一段残留的阻挡标记不能带过来，否则托盘会显示
	// “时长已到，等 XX 结束”——而那是上一段的事
	a.setPauseHold(nil)
	log.Printf("[pause] 定时暂停 %d 分钟", minutes)
}

// schedPauseUntilTomorrow 暂停到明天。
//
// 注意这是**内存态**：关机重启后暂停就没了（与“一直暂停”一致，不落盘）。对关机的人
// 来说结果一样 —— 第二天开机本来就没有暂停。真正吃到这一档的是“晚上不关机、睡眠或
// 休眠”的机器：进程还在，零点判定在唤醒后第一拍执行。
func (a *App) schedPauseUntilTomorrow() {
	if a.sched == nil {
		return
	}
	at := nextMidnight(time.Now()).UnixMilli()
	a.sched.PauseUntil(at)
	a.setPauseHold(nil)
	log.Printf("[pause] 定时暂停到 %s", time.UnixMilli(at).Format("2006-01-02 15:04"))
}

// pauseExpiryTick 定时暂停的到期判定（每 pauseNoticeWatchMs 一跳，与暂停提示共用循环）。
//
// 分三步：没定时 / 没到期 → 什么都不做；到期且有阻挡 → 记下阻挡原因并**保持暂停**；
// 到期且无阻挡 → 摘掉 manual 恢复（rest 里的自动原因若还挂着，按既有语义继续暂停，
// 托盘会补一句“条件已结束，可以恢复”）。
func (a *App) pauseExpiryTick() {
	if a.sched == nil {
		return
	}
	st := a.sched.StateCopy()
	if !st.Paused || st.PauseDeadline == 0 {
		a.setPauseHold(nil)
		return
	}
	// 手动原因已经被摘掉（用户点了“恢复休息”，只是别的自动原因还挂着）：这段定时
	// 暂停已经结束，别再拿旧截止时刻去报“时长已到”—— 那会把用户刚做过的操作又
	// 说成一件待办。截止时刻要等真正恢复（PauseReasons 清空）时才由 Resume 清掉。
	if !a.sched.HasPauseReason(scheduler.PauseReasonManual) {
		a.setPauseHold(nil)
		return
	}
	if !pauseExpired(st.PauseDeadline, nowMs()) {
		a.setPauseHold(nil)
		return
	}
	hold := pauseHoldReasons(a.pauseActiveSnapshot())
	a.setPauseHold(hold)
	if len(hold) > 0 {
		// 到点了但此刻恢复会打扰（多半还在会里 / 还接着投影）：保持暂停，
		// 等条件消失那一拍再恢复。托盘文案会点名在等什么。
		return
	}
	log.Printf("[pause] 定时暂停到期，条件都结束了，恢复（已暂停 %d 分钟）",
		max0(nowMs()-st.PausedAt)/60_000)
	a.sched.Resume(scheduler.PauseReasonManual)
	a.setPauseHold(nil)
}

// pauseActiveSnapshot 到期判定要看的“条件此刻是否成立”。
//
// 其余自动原因查登记表（applyAutoPause 每跳写）；**空闲单独处理**：它不走那个函数，
// 由 idleWatchTick 直接 Pause / Resume，所以登记表里没有它。人不在的证据在调度器那边
// —— `PauseReasons` 里还挂着 idle 就说明离开还没结束（人回来时 Resume(idle) 会摘掉它）。
// 不这么补的话，“23:55 设到明天、合盖睡觉、7:30 唤醒”会在第一拍判定时读到一个空的
// 登记表（idle 还没轮到采样），当场恢复，接着昨夜冻结的剩余时间走。
func (a *App) pauseActiveSnapshot() map[string]bool {
	active := a.autoReasonActiveSnapshot()
	if a.sched.HasPauseReason(scheduler.PauseReasonIdle) {
		active[scheduler.PauseReasonIdle] = true
	}
	return active
}

// ---------- 阻挡原因的跨 goroutine 存取 ----------

// setPauseHold 记下“到期了但被什么挡住”（空 = 没挡）。只给托盘文案读，
// 不参与任何判定 —— 判据每次现算，不缓存。
func (a *App) setPauseHold(reasons []string) {
	a.pauseHoldMu.Lock()
	if len(reasons) == 0 {
		a.pauseHold = nil
	} else {
		a.pauseHold = append([]string{}, reasons...)
	}
	a.pauseHoldMu.Unlock()
}

// pauseHoldSnapshot 上一拍算出的阻挡原因（副本）。
func (a *App) pauseHoldSnapshot() []string {
	a.pauseHoldMu.Lock()
	defer a.pauseHoldMu.Unlock()
	if len(a.pauseHold) == 0 {
		return nil
	}
	return append([]string{}, a.pauseHold...)
}
