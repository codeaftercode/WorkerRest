package main

import (
	"log"
	"time"
	"workerrest/internal/logx"
	"workerrest/internal/model"

	"workerrest/internal/platform"
)

// ---------- 系统级键鼠活动轮询（对应 reminderSync.ts） ----------

const (
	// supPollMs 采样周期：100ms 足够跟上人手操作，开销可忽略
	supPollMs = 100
	// supBroadcastGapMs 两次广播之间的最小间隔
	supBroadcastGapMs = 250
)

// supWatchInfo 轮询状态（冒烟自检读它做断言，不去数日志行数）
type supWatchInfo struct {
	Running bool   `json:"running"`
	Mode    string `json:"mode"` // tick | idle | off
	Polls   int    `json:"polls"`
	Pulses  int    `json:"pulses"`
}

// ensureSupActivityWatch 启动系统级键鼠活动轮询（已在跑则忽略）。有提醒在**场**时调用：
// 休息会话一建立（app.showBreakSession，含预览）与外卖窗口一创建（ShowTakeoutWindow）。
// 用户在**任意应用**里敲了键或动了鼠标 → 广播 sup:active（与窗口焦点无关）。
//
// 为什么休息会话也要开：休息窗口里的二阶段（督休专员改成命令语气）原本只监听窗口内的
// mousemove / keydown，用户切到别的应用继续干活时完全检测不到 —— 用户报的就是这条
// 「不能只在提醒窗口上检测，要在整个操作系统层面做全局检测」。
//
// 与 上一版的差异：只有 GetLastInputInfo 一个来源，读不到 tick 时整段不启动。
func (a *App) ensureSupActivityWatch() {
	a.mu.Lock()
	if a.supWatchStop != nil {
		a.mu.Unlock()
		return
	}
	if !serverMode {
		if _, ok := platform.LastInputTick(); !ok {
			a.mu.Unlock()
			log.Printf("[sup-watch] 读不到系统输入 tick，键鼠活动轮询不启动")
			return
		}
	}
	a.supWatch = supWatchInfo{Running: true, Mode: "tick"}
	ch := make(chan struct{})
	a.supWatchStop = ch
	a.mu.Unlock()

	go func() {
		t := time.NewTicker(supPollMs * time.Millisecond)
		defer t.Stop()
		var lastTick int64
		hasTick := false
		lastSentAt := int64(0)
		for {
			select {
			case <-ch:
				return
			case <-t.C:
				// 整拍包进闭包：里面的 continue 一律改写成 return —— 闭包返回就等于
				// 「这一拍结束」，与 continue 同义，换来一次 panic 只丢一拍。
				logx.GuardRun("sup.watch", func() {
					a.mu.Lock()
					w := &a.supWatch
					w.Polls++
					a.mu.Unlock()
					tk, ok := platform.LastInputTick()
					if !ok {
						// 读取中途失效：整段降级（但 Go 侧无秒级来源，直接停止比较）
						return
					}
					active := hasTick && tk != lastTick
					lastTick = tk
					hasTick = true
					if !active {
						return
					}
					now := nowMs()
					if now-lastSentAt < supBroadcastGapMs {
						return
					}
					lastSentAt = now
					a.mu.Lock()
					a.supWatch.Pulses++
					n := a.broadcastSupActive()
					first := a.supWatch.Pulses == 1
					a.mu.Unlock()
					if first {
						log.Printf("[sup-watch] global input pulse: mode=tick to=%d", n)
					}
				})
			}
		}
	}()
}

// broadcastSupActive 把「用户仍在操作键鼠」广播给所有提醒窗口，返回实际广播的次数。
// Go 侧用 Event.Emit("sup:active") 全局广播（不按窗口定向，见文件头注释）。
// 单测里没有 Wails 应用对象（a.app == nil）：计数照走、广播跳过，不让后台 goroutine 炸测试进程。
func (a *App) broadcastSupActive() int {
	if a.app == nil {
		return 0
	}
	a.app.Event.Emit("sup:active")
	return 1
}

// releaseSupActivityWatch 提醒彻底收场了才停止轮询
func (a *App) releaseSupActivityWatch() {
	a.mu.Lock()
	ch := a.supWatchStop
	if ch == nil {
		a.mu.Unlock()
		return
	}
	if a.reminderPresentLocked() {
		a.mu.Unlock()
		return
	}
	a.supWatchStop = nil
	a.supWatch.Mode = "off"
	a.supWatch.Running = false
	a.mu.Unlock()
	close(ch)
}

// reminderPresentLocked 还有没有「需要盯全局输入」的提醒在场。
// 窗口之外还要看**会话**：无头 server 模式没有原生窗口，只有会话；
// 有会话在跑就说明休息还没结束，轮询不能停。
func (a *App) reminderPresentLocked() bool {
	return a.hasBreakWindowLocked() || a.hasTakeoutWindowsLocked() || a.sess != nil
}

func (a *App) hasTakeoutWindowsLocked() bool {
	return len(a.takeoutWins) > 0
}

func (a *App) hasBreakWindowLocked() bool {
	return len(a.breakWins) > 0
}

// supActivityWatchInfo 当前轮询状态（冒烟自检用）
func (a *App) supActivityWatchInfo() supWatchInfo {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.supWatch
}

// ---------- 绑定（对应 main.ts 的 takeout:current / takeout:preview / takeout:action） ----------

// TakeoutCurrent 当前外卖会话（渲染层首帧拉取；无会话返回 nil）
func (a *App) TakeoutCurrent(win string) *model.TakeoutInfo {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.takeoutCurrent
}

// PreviewTakeout 预览外卖提醒窗口：有配置的时间就预览「下一条」，否则用当前时刻
//
// 真实外卖提醒窗口在场时拒弹（返回 previewBusyReply）：showTakeoutWindowsAt 会把
// a.takeoutWins 整批换成新建的窗口，屏幕上那扇真实的提醒窗引用就此丢掉，再也关不掉，
// 会话也被预览顶掉 —— 与休息窗口同一个毛病，口径统一在这里拦。
func (a *App) PreviewTakeout(win string) string {
	if a.realTakeoutActive() {
		log.Printf("[takeout] 真实外卖提醒窗口在场，忽略预览请求")
		return previewBusyReply
	}
	cfg := a.CfgSnapshot()
	slot := model.TimeSlot{ID: "preview", Hour: time.Now().Hour(), Minute: time.Now().Minute(), Enabled: true}
	if hit, ok := nextSlotAt(cfg.TakeoutSlots(), nowMs()); ok {
		slot = hit.Slot
	}
	a.showTakeoutWindows(slot, true, 0)
	return "ok"
}

// realTakeoutActive 真实外卖提醒（非预览）正在进行
func (a *App) realTakeoutActive() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.takeoutCurrent != nil && !a.takeoutCurrent.Preview
}

// TakeoutAction 外卖窗口上的三个按钮：已点好 / 不点了 / 推迟。
// 「推迟」不在窗口里排定时器，而是记一笔交给每秒的 tick（见 checkTakeoutReminders）；
// 预览态只关窗不记账，免得「看一眼预览」把真实提醒推后。
func (a *App) TakeoutAction(win, action string) error {
	a.mu.Lock()
	cur := a.takeoutCurrent
	a.mu.Unlock()
	if cur == nil {
		a.closeTakeoutWindows()
		return nil
	}
	if !cur.Preview {
		a.statsStore().RecordTakeoutAction(action)
	}
	if action == "postpone" && cur != nil && !cur.Preview {
		mins := cur.PostponeMin
		count := cur.PostponedCount + 1
		a.mu.Lock()
		a.takeoutPostponed = &model.TakeoutPostponed{
			Slot:  cur.Slot,
			DueAt: nowMs() + int64(mins)*60_000,
			Count: count,
		}
		a.mu.Unlock()
		log.Printf("[takeout] 用户选择 推迟 %d 分钟（第 %d 次）", mins, count)
	} else {
		a.mu.Lock()
		a.takeoutPostponed = nil
		a.mu.Unlock()
		label := map[string]string{"ordered": "已点好", "skip": "不点了", "postpone": "推迟（预览，忽略）"}[action]
		if label == "" {
			label = action
		}
		log.Printf("[takeout] 用户选择 %s", label)
	}
	a.closeTakeoutWindows()
	return nil
}
