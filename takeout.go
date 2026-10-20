package main

import (
	"log"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// 外卖提醒：按固定时刻弹出（不做倒计时），显示逻辑与休息窗口一致（同一套外观配置、
// 多屏策略、透明与圆角），区别只在内容。对应 历史源码/main.ts 的「外卖提醒」段 +
// 历史源码/windows/takeoutWindow.ts + raise.ts + displays.ts + reminderSync.ts。
//
// 与 上一版的两处**有意差异**（见 README「已知差异」）：
//   ① z-order：上一版 用 moveTop() 在同层（screen-saver）内「谁后抬谁在上」把外卖压到
//      休息窗口之上；Wails v3 没有暴露 z-order 抬升 API，这里降级成 SetAlwaysOnTop(true) +
//      Focus() 近似（抬升簿记 raiseState 仍保留，供诊断 / 冒烟断言）。
//   ② 系统级键鼠轮询：上一版有 powerMonitor.getSystemIdleTime() 可降级秒级；Go 侧只有
//      user32!GetLastInputInfo 一个来源，读不到 tick 时整段不启动（不假装有活动）。

const (
	// takeoutWindowBaseName 外卖窗口在 wins 里不按名存（多窗口），这里只作日志 / 诊断名
	takeoutWindowBaseName = "takeout"
	// raiseIntervalMs 抬升循环间隔：足够密集以压过异步显示的休息窗口，又不至于视觉抖动
	raiseIntervalMs = 700
	// takeoutPostponeGraceMs 推迟计划有效期：到点后若一直暂停 / 睡眠，超过此宽限即作废
	takeoutPostponeGraceMs = 5 * 60_000
)

// ---------- 抬升簿记（对应 windows/raise.ts） ----------

// raiseKind 抬升方：休息窗口或外卖窗口
type raiseKind string

const (
	raiseKindBreak   raiseKind = "break"
	raiseKindTakeout raiseKind = "takeout"
)

// raiseState 窗口层级簿记。休息窗口与外卖窗口都要求置顶（Windows 上是 topmost 层级）。
// 记录抬升顺序与「休息窗口是否在场」，给诊断与冒烟自检一个可断言的状态。
type raiseState struct {
	mu      sync.Mutex
	last    raiseKind
	seq     []raiseKind
	breakOn bool
}

func (r *raiseState) noteRaised(k raiseKind) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.last = k
	r.seq = append(r.seq, k)
	if len(r.seq) > 40 {
		r.seq = r.seq[len(r.seq)-40:]
	}
}

func (r *raiseState) lastRaised() raiseKind {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.last
}

func (r *raiseState) raiseSequence() []raiseKind {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]raiseKind, len(r.seq))
	copy(out, r.seq)
	return out
}

func (r *raiseState) setBreaksActive(v bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.breakOn = v
}

func (r *raiseState) anyBreakActive() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.breakOn
}

// ---------- 会话构建 ----------

// buildTakeoutInfo 组装一次外卖会话（对应 takeoutWindow.ts 的 showTakeoutWindows 里
// 那个 TakeoutSessionInfo）。会话与「弹窗」分开：渲染层随后会自己再拉一次 TakeoutCurrent。
func (a *App) buildTakeoutInfo(slot TimeSlot, preview bool, postponedCount int) *TakeoutInfo {
	cfg := a.cfgSnapshot()
	now := time.Now()
	at := time.Date(now.Year(), now.Month(), now.Day(), slot.Hour, slot.Minute, 0, 0, now.Location())
	// 红包二维码指向本机专属领取地址：installId 是每台设备一枚的 UUID，
	// 服务端（nav.hcctc.cn）按 id 区分来源。只要显示外卖提醒窗口就必须展示优惠券，
	// 因此这里无条件带上地址（不再有开关）。
	redData := &TakeoutRedPacket{URL: "https://nav.hcctc.cn/api/cps/id=" + cfg.Str("", "installId")}
	info := &TakeoutInfo{
		ID:             takeoutID(),
		At:             at.UnixMilli(),
		Hour:           slot.Hour,
		Minute:         slot.Minute,
		Slot:           slot,
		FiredAt:        now.UnixMilli(),
		Preview:        preview,
		Meal:           takeoutMealLabel(slot.Hour),
		LeadSec:        cfg.TakeoutLeadSec(),
		PostponeMin:    cfg.TakeoutPostponeMin(),
		PostponedCount: postponedCount,
		Appearance:     cfg.AppearanceView(),
		RedPacketData:  redData,
	}
	// 外卖提醒自己的提醒内容：每次弹窗选 1 条；一条启用的都没有时用提醒名「外卖提醒」
	// 兜底（窗口里「本该显示提醒内容的位置」不空着）。pickMessage 返回的切片第 0 条即内容。
	if msgs := a.pickMessage("takeout", TAKEOUT_NAME); len(msgs) > 0 {
		info.Message = msgs[0]
	}
	// 督休专员：与休息窗口同一条构建路径；外卖窗口的督休专员第一段台词 = 本提醒的提醒内容。
	// 第二阶段**不换台词**：命令台词清空，渲染层 applyStage 的 `command || stage1` 沿用第一段。
	if sup := a.buildSupervisorSession(preview, ""); sup != nil {
		sup.Lines.Command = ""
		info.Supervisor = sup
	}
	return info
}

// takeoutID 生成本次外卖会话 id（对应 上一版的 `${Date.now()}-${random36}`；Go 侧用 UUID 同样唯一）
func takeoutID() string {
	return newUUID()
}

// ---------- 触发与推迟（对应 main.ts 的 checkTakeoutReminders） ----------

// fireTakeout 弹出一次外卖提醒（固定时刻线、推迟线两条触发路径共用）
func (a *App) fireTakeout(slot TimeSlot, postponedCount int) {
	log.Printf("[takeout] 触发提醒 %02d:%02d lead=%d秒 postponed=%d次",
		slot.Hour, slot.Minute, a.cfgSnapshot().TakeoutLeadSec(), postponedCount)
	a.showTakeoutWindows(slot, false, postponedCount)
	a.playConfiguredSound()
}

// checkTakeoutReminders 外卖提醒检查：与休息调度不同，这里不累加时间，而是「到某一时刻
// 就触发」。每秒跑一次（按分钟匹配），因此就算某次 tick 被阻塞也不会漏掉整分钟的窗口。
// 暂停提醒期间不弹，恢复后也不补弹——时刻已经过去，补弹只是打扰。
//
// 三条触发线，谁先到谁弹：
//
//	① 推迟线：用户刚点了「推迟」，到点就重新弹一次
//	② 固定时刻线（真窗）：到「条目原定时刻」就弹（提前通知不改真实弹出时刻，
//	   只是先在 原定时刻−提前量 弹一个预通知小窗，见 showTakeoutPreNotify）
//	③ 提前通知线：在原定时刻−提前量 与 原定时刻 之间弹一次预通知小窗（提前量=0 不弹）
//
// 去重键按 occurrence（原定时刻）计，不含提前量——所以预通知与真窗不会互相串。
func (a *App) checkTakeoutReminders() {
	cfg := a.cfgSnapshot()
	if !cfg.TakeoutEnabled() {
		a.mu.Lock()
		a.takeoutPostponed = nil
		a.takeoutPreFired = nil
		a.mu.Unlock()
		return
	}
	// 推迟计划过期即作废（只在暂停 / 休眠期间才可能发生）
	a.mu.Lock()
	pp := a.takeoutPostponed
	if pp != nil && nowMs() > pp.DueAt+takeoutPostponeGraceMs {
		log.Printf("[takeout] 推迟计划已过期，作废")
		a.takeoutPostponed = nil
		pp = nil
	}
	a.mu.Unlock()
	if a.sched != nil && a.sched.isPaused() {
		return
	}
	now := nowMs()
	leadSec := cfg.TakeoutLeadSec()
	leadMs := int64(leadSec) * 1000
	// ① 推迟线：到点直接弹真窗
	if pp != nil && now >= pp.DueAt {
		a.mu.Lock()
		a.takeoutPostponed = nil
		a.mu.Unlock()
		a.closeTakeoutPreNotifyIfOpen()
		a.fireTakeout(pp.Slot, pp.Count)
		return
	}
	// ①b 推迟后的提前通知：leadSec>0 时，在推迟后的真实时刻前 leadMs 再弹一次预通知小窗
	// （倒计时指向推迟后的到点时刻）。与原始提前通知线同口径：推迟后仍需提前提醒用户。
	// 去重用 pp.PreFired——每秒 tick 都会走到这里，只弹一次。
	if pp != nil && leadSec > 0 && now >= pp.DueAt-leadMs && now < pp.DueAt && !pp.PreFired {
		pp.PreFired = true
		a.showTakeoutPreNotify(pp.Slot, pp.DueAt)
		return
	}
	// ② 固定时刻线（真窗）：原定时刻已到就弹；③ 提前通知线：在原定时刻−提前量 之后弹一次
	var toFire []TimeSlot
	var toPreNotify []TimeSlot
	a.mu.Lock()
	for _, slot := range cfg.TakeoutSlots() {
		if !slot.Enabled {
			continue
		}
		at := slotAlertAt(slot, 0, now) // 原定时刻（当天，用于 occurrence 去重键）
		// ② 真窗：now 命中条目「原定分钟」（leadMs=0）才弹——保持原来的「按分钟匹配」语义，
		//    不补弹过去错过的分钟；按 occurrence 去重，避免同一分钟重复弹。
		if isTimeSlotDue(slot, now, 0) {
			key := occurrenceKey("takeout", at)
			if a.takeoutFired[key] {
				continue
			}
			a.takeoutFired[key] = true
			toFire = append(toFire, slot)
			continue
		}
		// ③ 提前通知窗：now+提前量 命中原定分钟那一刻弹一次（提前量=0 不进此支）；
		//    窗口随后保持显示，直到原定时刻真窗弹出或用户动作。
		if leadSec > 0 && isTimeSlotDue(slot, now, leadMs) {
			key := occurrenceKey("takeout", at)
			if a.takeoutPreFired[key] {
				continue
			}
			a.takeoutPreFired[key] = true
			toPreNotify = append(toPreNotify, slot)
		}
	}
	a.mu.Unlock()
	for _, slot := range toFire {
		a.closeTakeoutPreNotifyIfOpen()
		a.fireTakeout(slot, 0)
	}
	for _, slot := range toPreNotify {
		a.showTakeoutPreNotify(slot, 0)
	}
}

// showTakeoutPreNotify 在外卖「原定时刻−提前量」弹出提前通知小窗（复用休息的提前提示窗，
// 但走外卖 owner：标题=提醒名、倒计时指向 targetAt（原定时刻或推迟后的到点时刻）、
// 三按钮 立即提醒/推迟提醒/跳过提醒）。targetAt=0 时按原定时刻（slotAlertAt）推算。
// 提前量=0 时不该被调用（checkTakeoutReminders 已把这条支滤掉）。
func (a *App) showTakeoutPreNotify(slot TimeSlot, targetAt int64) {
	cfg := a.cfgSnapshot()
	if targetAt == 0 {
		targetAt = slotAlertAt(slot, 0, nowMs()) // 原定时刻（当天）
	}
	info := PreBreakInfo{
		Owner:         "takeout",
		Custom:        true,
		Title:         TAKEOUT_NAME,
		CountdownTail: " 秒",
		PostponeMin:   cfg.TakeoutPostponeMin(),
	}
	a.mu.Lock()
	a.preInfo = info
	a.preTargetAt = targetAt
	a.preTakeoutSlot = slot
	a.mu.Unlock()
	a.ShowPreBreakWindow(info)
}

// closeTakeoutPreNotifyIfOpen 关掉「外卖预通知」小窗（不打扰正在显示的休息/自定义预通知）
func (a *App) closeTakeoutPreNotifyIfOpen() {
	a.mu.Lock()
	isTakeout := a.preInfo.Owner == "takeout"
	a.mu.Unlock()
	if isTakeout {
		a.closePreBreakWindow()
	}
}

// startTakeoutWatcher 启动外卖每秒检查（对应 上一版的 startTakeoutWatcher）
func (a *App) startTakeoutWatcher() {
	a.takeoutStop = make(chan struct{})
	ch := a.takeoutStop
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-ch:
				return
			case <-t.C:
				a.checkTakeoutReminders()
			}
		}
	}()
}

// stopTakeoutWatcher 停止外卖检查（退出清理）
func (a *App) stopTakeoutWatcher() {
	a.mu.Lock()
	ch := a.takeoutStop
	a.takeoutStop = nil
	a.mu.Unlock()
	if ch != nil {
		close(ch)
	}
}

// ---------- 窗口（对应 takeoutWindow.ts + displays.ts） ----------

// targetDisplays 按「在所有屏幕显示」开关选出要显示的显示器（休息窗口与外卖
// 窗口共用同一策略）。取消勾选 ⇒ 只在主屏幕显示（Wails v3 无获取光标所在屏的 API，以主屏兜底）。
func (a *App) targetDisplays() []*application.Screen {
	cfg := a.cfgSnapshot()
	all := a.app.Screen.GetAll()
	if len(all) == 0 {
		return nil
	}
	if cfg.AllScreens() {
		return all
	}
	// 光标所在屏：Wails v3 没有 getCursorScreenPoint 的等价物，用主屏兜底
	if sc := a.app.Screen.GetPrimary(); sc != nil {
		return []*application.Screen{sc}
	}
	return all[:1]
}

// showTakeoutWindows 弹出外卖提醒窗口（每个目标屏各一个）。无头（server 模式）下不建窗，
// 但会话照常构建，于是无头冒烟能验证「触发 → 会话 JSON → 动作记账」整条链路。
func (a *App) showTakeoutWindows(slot TimeSlot, preview bool, postponedCount int) {
	info := a.buildTakeoutInfo(slot, preview, postponedCount)
	a.mu.Lock()
	a.takeoutCurrent = info
	a.mu.Unlock()

	if serverMode || a.app == nil {
		if serverMode {
			log.Printf("[takeout] server 模式无原生窗口，跳过创建（会话已就绪 %s）", info.Meal)
		}
		return
	}

	cfg := a.cfgSnapshot()
	ap := cfg.AppearanceCfg()
	displays := a.targetDisplays()
	var wins []*application.WebviewWindow
	for _, display := range displays {
		wa := display.WorkArea
		w := int(float64(wa.Width) * ap.WidthPct / 100)
		h := int(float64(wa.Height) * ap.HeightPct / 100)
		if w < 320 {
			w = 320
		}
		if h < 240 {
			h = 240
		}
		x, y := wa.X, wa.Y
		if !ap.Fullscreen {
			x = wa.X + (wa.Width-w)/2
			y = wa.Y + (wa.Height-h)/2
		}
		win := a.app.Window.NewWithOptions(application.WebviewWindowOptions{
			Title:         "外卖提醒",
			X:             x,
			Y:             y,
			Width:         w,
			Height:        h,
			Frameless:     true,
			AlwaysOnTop:   ap.AlwaysOnTop,
			DisableResize: true,
			// 不设它 X/Y 会被无视：Wails 的 InitialPosition 零值 = 整窗居中
			InitialPosition:  application.WindowXY,
			URL:              "/takeout.html?win=takeout",
			BackgroundType:   application.BackgroundTypeTranslucent,
			BackgroundColour: application.NewRGBA(0, 0, 0, 0),
			// 窗口身份经 URL 的 ?win=takeout 下发；JS 字段对 URL 页面不生效（仅占位）
			JS: `window.__WR_WIN = "takeout";`,
			Windows: application.WindowsWindow{
				DisableFramelessWindowDecorations: true,
				HiddenOnTaskbar:                   true,
			},
		})
		// 窗口被系统 / 用户直接关掉（Alt+F4）时清掉引用
		win.OnWindowEvent(events.Common.WindowClosing, func(*application.WindowEvent) {
			a.removeTakeoutWindow(win)
		})
		wins = append(wins, win)
	}
	a.mu.Lock()
	a.takeoutWins = wins
	a.mu.Unlock()

	// 抬升：降级成置顶 + 聚焦（见文件头注释）。用户点「知道了」类路径不经过这里。
	if ap.AlwaysOnTop {
		for _, w := range wins {
			w.SetAlwaysOnTop(true)
		}
	}
	if len(wins) > 0 {
		wins[0].Focus()
	}
	a.raise.noteRaised(raiseKindTakeout)
	// 抬升循环：在场期间每 raiseIntervalMs 抬一次（只在有休息窗口时），
	// 把随后显示的休息窗口压回下面。
	a.startRaiseLoop()
	// 系统级键鼠活动轮询：广播给所有提醒窗口（见 reminderSync 段）
	a.ensureSupActivityWatch()
}

func (a *App) removeTakeoutWindow(w *application.WebviewWindow) {
	a.mu.Lock()
	ws := a.takeoutWins
	for i, x := range ws {
		if x == w {
			a.takeoutWins = append(ws[:i], ws[i+1:]...)
			break
		}
	}
	live := len(a.takeoutWins) > 0
	a.mu.Unlock()
	if !live {
		a.resetTakeoutState()
	}
}

// closeTakeoutWindows 关掉全部外卖窗口并清会话
func (a *App) closeTakeoutWindows() {
	a.mu.Lock()
	ws := a.takeoutWins
	a.takeoutWins = nil
	a.takeoutCurrent = nil
	a.mu.Unlock()
	// 外卖提醒结束提示音：只在「确有窗口被关掉」时播（启动清理 / 空关窗不响）。
	// 与休息结束（closeBreakWindows）同口径：声音卡片勾选启用才会在结束响一声。
	if len(ws) > 0 {
		a.playConfiguredSound()
	}
	for _, w := range ws {
		w.Close()
	}
	a.resetTakeoutState()
}

// resetTakeoutState 会话 / 定时器状态复位（收窗与「最后一个窗口被关闭」两条路径共用）
func (a *App) resetTakeoutState() {
	a.stopRaiseLoop()
	a.releaseSupActivityWatch()
}

// hasTakeoutWindows 是否有外卖窗口在场（含「刚建还没 show」的）
func (a *App) hasTakeoutWindows() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.takeoutWins) > 0
}

// raiseTakeoutWindows 把外卖窗口抬到休息窗口之上：降级为置顶 + 聚焦（见文件头注释）
func (a *App) raiseTakeoutWindows() {
	a.mu.Lock()
	ws := append([]*application.WebviewWindow(nil), a.takeoutWins...)
	cur := a.takeoutCurrent
	a.mu.Unlock()
	raised := false
	for _, w := range ws {
		if cur != nil && cur.Appearance.Fullscreen {
			continue
		}
		w.SetAlwaysOnTop(true)
		w.Focus()
		raised = true
	}
	if raised {
		a.raise.noteRaised(raiseKindTakeout)
	}
}

func (a *App) startRaiseLoop() {
	a.mu.Lock()
	if a.takeoutRaiseStop != nil {
		a.mu.Unlock()
		return
	}
	ch := make(chan struct{})
	a.takeoutRaiseStop = ch
	a.mu.Unlock()
	go func() {
		t := time.NewTicker(raiseIntervalMs * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-ch:
				return
			case <-t.C:
				if !a.hasTakeoutWindows() {
					a.stopRaiseLoop()
					return
				}
				if a.raise.anyBreakActive() {
					a.raiseTakeoutWindows()
				}
			}
		}
	}()
}

func (a *App) stopRaiseLoop() {
	a.mu.Lock()
	ch := a.takeoutRaiseStop
	a.takeoutRaiseStop = nil
	a.mu.Unlock()
	if ch != nil {
		close(ch)
	}
}

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

// ensureSupActivityWatch 启动系统级键鼠活动轮询（已在跑则忽略）。有提醒窗口在场时调用。
// 用户在**任意应用**里敲了键或动了鼠标 → 广播 sup:active（与窗口焦点无关）。
//
// 与 上一版的差异：只有 GetLastInputInfo 一个来源，读不到 tick 时整段不启动。
func (a *App) ensureSupActivityWatch() {
	a.mu.Lock()
	if a.supWatchStop != nil {
		a.mu.Unlock()
		return
	}
	if !serverMode {
		if _, ok := lastInputTick(); !ok {
			a.mu.Unlock()
			log.Printf("[sup-watch] 读不到系统输入 tick，键鼠活动轮询不启动")
			return
		}
	}
	a.supWatch = supWatchInfo{Mode: "tick"}
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
				a.mu.Lock()
				w := &a.supWatch
				w.Polls++
				a.mu.Unlock()
				tk, ok := lastInputTick()
				if !ok {
					// 读取中途失效：整段降级（但 Go 侧无秒级来源，直接停止比较）
					continue
				}
				active := hasTick && tk != lastTick
				lastTick = tk
				hasTick = true
				if !active {
					continue
				}
				now := nowMs()
				if now-lastSentAt < supBroadcastGapMs {
					continue
				}
				lastSentAt = now
				a.mu.Lock()
				a.supWatch.Pulses++
				n := a.broadcastSupActive()
				first := a.supWatch.Pulses == 1
				a.mu.Unlock()
				if first {
					log.Printf("[smoke] global input pulse: mode=tick to=%d", n)
				}
			}
		}
	}()
}

// broadcastSupActive 把「用户仍在操作键鼠」广播给所有提醒窗口，返回实际广播的次数。
// Go 侧用 Event.Emit("sup:active") 全局广播（不按窗口定向，见文件头注释）。
func (a *App) broadcastSupActive() int {
	a.app.Event.Emit("sup:active")
	return 1
}

// releaseSupActivityWatch 全部提醒窗口都收掉了才停止轮询
func (a *App) releaseSupActivityWatch() {
	a.mu.Lock()
	ch := a.supWatchStop
	if ch == nil {
		a.mu.Unlock()
		return
	}
	if a.hasTakeoutWindowsLocked() || a.hasBreakWindowLocked() {
		a.mu.Unlock()
		return
	}
	a.supWatchStop = nil
	a.supWatch.Mode = "off"
	a.mu.Unlock()
	close(ch)
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
func (a *App) TakeoutCurrent(win string) *TakeoutInfo {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.takeoutCurrent
}

// PreviewTakeout 预览外卖提醒窗口：有配置的时间就预览「下一条」，否则用当前时刻
func (a *App) PreviewTakeout(win string) string {
	cfg := a.cfgSnapshot()
	slot := TimeSlot{ID: "preview", Hour: time.Now().Hour(), Minute: time.Now().Minute(), Enabled: true}
	if hit, ok := nextSlotAt(cfg.TakeoutSlots(), nowMs()); ok {
		slot = hit.Slot
	}
	a.showTakeoutWindows(slot, true, 0)
	return "ok"
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
		a.takeoutPostponed = &TakeoutPostponed{
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
