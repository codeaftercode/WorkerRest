package main

import (
	"log"
	"strconv"
	"sync"
	"time"
	"workerrest/internal/jsonx"
	"workerrest/internal/model"
	"workerrest/internal/plan"
	"workerrest/internal/settings"
)

// 外卖提醒：按固定时刻弹出（不做倒计时），显示逻辑与休息窗口一致（同一套外观配置、
// 多屏策略、透明与圆角），区别只在内容。对应 历史源码/main.ts 的“外卖提醒”段 +
// 历史源码/windows/takeoutWindow.ts + raise.ts + displays.ts + reminderSync.ts。
//
// 与 上一版的两处**有意差异**（见 README“已知差异”）：
//   ① z-order：上一版 用 moveTop() 在同层（screen-saver）内“谁后抬谁在上”把外卖压到
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
	// takeoutPreNotifyGraceMs 预通知窗过了到点后的收窗宽限：正常路径由真窗弹出时关闭，
	// 这里只兜“真窗没能弹出”（暂停 / 跳过 / 总开关被关）时的残留
	takeoutPreNotifyGraceMs = 60_000
)

// ---------- 抬升簿记（对应 windows/raise.ts） ----------

// raiseKind 抬升方：休息窗口或外卖窗口
type raiseKind string

const (
	raiseKindBreak   raiseKind = "break"
	raiseKindTakeout raiseKind = "takeout"
)

// raiseState 窗口层级簿记。休息窗口与外卖窗口都要求置顶（Windows 上是 topmost 层级）。
// 记录抬升顺序与“休息窗口是否在场”，给诊断与冒烟自检一个可断言的状态。
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
// 那个 TakeoutSessionInfo）。会话与“弹窗”分开：渲染层随后会自己再拉一次 TakeoutCurrent。
func (a *App) buildTakeoutInfo(slot model.TimeSlot, preview bool, postponedCount int, atOverride int64) *model.TakeoutInfo {
	cfg := a.CfgSnapshot()
	now := time.Now()
	// atOverride = 这次提醒真正归属的 occurrence（提前通知跨午夜时是次日那一格、
	// 推迟线是推迟后的到点时刻）；不传则按“当天该时刻”—— 与历史口径一致。
	atMs := atOverride
	if atMs <= 0 {
		atMs = plan.SlotAlertAt(slot, 0, now.UnixMilli())
	}
	// 外卖优惠券二维码：指向固定的领取页，不带任何本机标识 —— 每台机器生成的都是同一个
	// 地址，扫码领券与“这台电脑是谁”无关。只要显示外卖提醒窗口就必须展示优惠券，因此
	// 这里无条件带上地址（不再有开关）。
	// t = 毫秒时间戳：每次弹窗都重新生成地址，供服务端做时效判断 / 缓存刷新。
	redData := &model.TakeoutRedPacket{URL: "https://www.goodgoodlive.com/cps/index.html?t=" +
		strconv.FormatInt(now.UnixMilli(), 10)}
	info := &model.TakeoutInfo{
		ID:             takeoutID(),
		At:             atMs,
		Hour:           slot.Hour,
		Minute:         slot.Minute,
		Slot:           slot,
		FiredAt:        now.UnixMilli(),
		Preview:        preview,
		Meal:           plan.TakeoutMealLabel(slot.Hour),
		LeadSec:        cfg.TakeoutLeadSec(),
		PostponeMin:    cfg.TakeoutPostponeMin(),
		PostponedCount: postponedCount,
		Appearance:     cfg.AppearanceView(),
		RedPacketData:  redData,
	}
	// 外卖提醒自己的提醒内容：每次弹窗选 1 条；一条启用的都没有时用提醒名“外卖提醒”
	// 兜底（窗口里“本该显示提醒内容的位置”不空着）。pickMessage 返回的切片第 0 条即内容。
	if msgs := a.pickMessage("takeout", plan.TAKEOUT_NAME); len(msgs) > 0 {
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

// takeoutID 生成本次外卖会话 id（对应 上一版的 `${Date.now()}-${random36}`；Go 侧用随机唯一串同样唯一）
func takeoutID() string {
	return jsonx.NewRandomID()
}

// ---------- 空闲门（“鼠标键盘无活动 x 时间”才弹） ----------

// a.takeoutGated 存的是 model.TakeoutIdleWait：到点了、但还在等键鼠无活动达标的那一笔。
// 与 takeoutPostponed 分开存 —— 那一个是用户点的推迟（有人为决定在里面，报表要记一次），
// 这里只是配置要求“等用户离开工位再弹”。状态快照里两者写法也不同：后者写“等待空闲”。
// armGatedTakeout 空闲门达标后，把挂起的那一笔外卖落成“armed 推迟线”：
// DueAt = 现在 + 提前通知秒数，随后完全复用现有的两条触发线 ——
// ①b 在 DueAt-提前量 弹预通知小窗（提前量 0 就不弹），① 在 DueAt 弹真窗。
//
// 换句话说：“等空闲 → 提前提示 → 正式提醒”这三段里，前一段是本函数新加的，
// 后两段一行都没改。lead=0 时 DueAt=now，下一拍 ① 直接弹真窗。
//
// 幂等：每一次 tick 都调一遍，达标之前什么也不做。
func (a *App) armGatedTakeout(cfg *settings.Settings, now int64) {
	gateMs := cfg.TakeoutIdleGateMs()
	a.mu.Lock()
	g := a.takeoutGated
	if g == nil {
		a.mu.Unlock()
		return
	}
	// 空闲门被取消（或总开关关了）：这笔挂起没有意义了，丢掉
	if gateMs <= 0 {
		a.takeoutGated = nil
		a.mu.Unlock()
		return
	}
	// 已经有推迟线在路上（用户刚点了推迟 / 上一次已 armed）：让它自己走完，
	// 不要在它前面插一条新的 —— 外卖窗口只有一扇，两条线会互相顶掉。
	if a.takeoutPostponed != nil {
		a.mu.Unlock()
		return
	}
	a.mu.Unlock()
	// 空闲读数在锁外取（IdleMs 自己会加 a.mu）
	if a.IdleMs() < gateMs {
		return
	}
	lead := int64(cfg.TakeoutLeadSec()) * 1000
	a.mu.Lock()
	if a.takeoutGated == nil || a.takeoutPostponed != nil {
		a.mu.Unlock()
		return
	}
	g = a.takeoutGated
	a.takeoutGated = nil
	a.takeoutPostponed = &model.TakeoutPostponed{Slot: g.Slot, DueAt: now + lead, Count: 0, Armed: true}
	a.mu.Unlock()
	log.Printf("[takeout] %02d:%02d 空闲门达标：%d 秒后弹提前通知（提前通知 0 秒则直接弹提醒窗）",
		g.Slot.Hour, g.Slot.Minute, lead/1000)
}

// TakeoutIdleWaitSnapshot 当前有没有“到点了、还在等空闲”的外卖提醒（状态快照用）
func (a *App) TakeoutIdleWaitSnapshot() *model.TakeoutIdleWait {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.takeoutGated
}
