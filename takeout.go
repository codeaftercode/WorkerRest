package main

import (
	"strconv"
	"sync"
	"time"
	"workerrest/internal/jsonx"
	"workerrest/internal/model"
	"workerrest/internal/plan"
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
	// takeoutPreNotifyGraceMs 预通知窗过了到点后的收窗宽限：正常路径由真窗弹出时关闭，
	// 这里只兜「真窗没能弹出」（暂停 / 跳过 / 总开关被关）时的残留
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
func (a *App) buildTakeoutInfo(slot model.TimeSlot, preview bool, postponedCount int, atOverride int64) *model.TakeoutInfo {
	cfg := a.cfgSnapshot()
	now := time.Now()
	// atOverride = 这次提醒真正归属的 occurrence（提前通知跨午夜时是次日那一格、
	// 推迟线是推迟后的到点时刻）；不传则按「当天该时刻」—— 与历史口径一致。
	atMs := atOverride
	if atMs <= 0 {
		atMs = slotAlertAt(slot, 0, now.UnixMilli())
	}
	// 红包二维码指向本机专属领取地址：id 取配置里那枚固定键（每台机器一枚，首次运行生成），
	// 服务端（www.goodgoodlive.com）按 id 区分来源。只要显示外卖提醒窗口就必须展示优惠券，
	// 因此这里无条件带上地址（不再有开关）。
	// t = 毫秒时间戳：每次弹窗都重新生成地址，供服务端做时效判断 / 缓存刷新，
	// 与 id 一起追加在查询串上（id 保留）。
	redData := &model.TakeoutRedPacket{URL: "https://www.goodgoodlive.com/cps/index.html?id=" +
		cfg.Str("", "installId") + "&t=" + strconv.FormatInt(now.UnixMilli(), 10)}
	info := &model.TakeoutInfo{
		ID:             takeoutID(),
		At:             atMs,
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
