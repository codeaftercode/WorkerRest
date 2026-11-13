package main

// 单窗多条（2026-11-13 用户口径）。
//
// 一次只开一扇提醒窗。休息正在进行时，另一条提醒（自定义提醒 / 外卖）到点**不另开窗**，
// 而是作为“同时提醒”挂到当前这次休息上 —— 用户本来就已经离开屏幕，这条提醒想达成的
// 效果已经由这次休息达成；把它悄悄吞掉不对，等休息结束再补弹一次也不对（十分钟内被连着
// 拦两回）。内置短 / 长休息之间的撞车不走这里，它们按“长休息优先”处理（见 scheduler.merge.go）。
//
// 外卖是唯一带按钮的副条：卡片上的“推迟 / 知道了”直接转发给外卖模块的 TakeoutAction，
// 记账与独立窗口完全同一套，不在这里另算。

import (
	"log"
	"strings"

	"workerrest/internal/model"
)

// OnBreakExtra 调度器回调：休息期间到点的自定义提醒并入当前休息窗。
//
// 返回 false = 当前没有可挂的休息会话（窗口被 Alt+F4 关掉 / 建窗失败 / 在预览里）：
// 调度器据此**不消费**这一班，让它之后按正常路径再提醒一次。
func (a *App) OnBreakExtra(kind string, at int64) bool {
	a.mu.Lock()
	sess := a.sess
	if sess == nil || sess.Preview {
		a.mu.Unlock()
		log.Printf("[break] 休息期间 %s 到点，但当前没有休息会话可并入：这一班保留，之后照常提醒", kind)
		return false
	}
	name := a.cfg.BreakNameByID(kind)
	// pickMessage 会推进顺序游标 / 用 rng，必须在锁内调（与 ShowBreakSession 同口径）
	text := strings.Join(a.pickMessage(kind, name), " ")
	a.mu.Unlock()
	a.addBreakExtra(model.BreakExtra{Kind: kind, Name: name, Text: text, At: at})
	return true
}

// OnBreakCounted 调度器回调：一次内置休息被另一次休息覆盖，按“完成”记一次。
// 时长不记 —— 那段时间已经由覆盖它的那次休息记过了，再记一遍会把“休息时长”算重。
func (a *App) OnBreakCounted(kind string) {
	a.statsStore().OnBreakCounted(kind)
}

// addBreakExtra 往当前休息会话追加一条“同时提醒”，并把整份清单推给窗口。
func (a *App) addBreakExtra(ex model.BreakExtra) {
	a.mu.Lock()
	sess := a.sess
	if sess == nil || sess.Preview {
		a.mu.Unlock()
		return
	}
	// 同一条提醒在一次休息里只挂一次：长休息 20 分钟碰上 5 分钟一次的提醒时，
	// 不给用户堆四张一模一样的卡（调度器那边也做了去重，这里兜一道）
	for _, e := range sess.Extras {
		if e.Kind == ex.Kind {
			a.mu.Unlock()
			return
		}
	}
	sess.Extras = append(sess.Extras, ex)
	list := append([]model.BreakExtra(nil), sess.Extras...)
	a.mu.Unlock()
	name := ex.Name
	if name == "" {
		name = ex.Kind
	}
	log.Printf("[break] 并入同时提醒：%s（%s）", name, ex.Kind)
	a.emitBreakExtras(list)
}

// dropBreakExtra 把一条副条从当前会话里摘掉（外卖被处理掉 / 休息结束交接）
func (a *App) dropBreakExtra(kind string) {
	a.mu.Lock()
	sess := a.sess
	if sess == nil || len(sess.Extras) == 0 {
		a.mu.Unlock()
		return
	}
	kept := make([]model.BreakExtra, 0, len(sess.Extras))
	for _, e := range sess.Extras {
		if e.Kind != kind {
			kept = append(kept, e)
		}
	}
	if len(kept) == len(sess.Extras) {
		a.mu.Unlock()
		return
	}
	sess.Extras = kept
	list := append([]model.BreakExtra(nil), kept...)
	a.mu.Unlock()
	a.emitBreakExtras(list)
}

// emitBreakExtras 把整份副条清单推给休息窗口（增量协议两边各存一份状态最容易漏掉移除，
// 所以每次推全量，前端整块重绘）。
func (a *App) emitBreakExtras(list []model.BreakExtra) {
	if a.app == nil || a.app.Event == nil {
		return
	}
	if list == nil {
		list = []model.BreakExtra{}
	}
	a.app.Event.Emit("break:extras", list)
}

// attachTakeoutToBreak 休息窗正在场时，把这次外卖提醒并入休息窗，返回是否并入。
//
// 返回 true 时调用方不要再建外卖窗口：休息窗是 95%×95% 的置顶窗，两扇同样大小的窗
// 叠在一起，用户只会看到最后弹出的那一扇 —— 休息倒计时被整个盖住。
func (a *App) attachTakeoutToBreak(info *model.TakeoutInfo) bool {
	a.mu.Lock()
	sess := a.sess
	if sess == nil || sess.Preview {
		a.mu.Unlock()
		return false
	}
	// 会话先立起来：TakeoutAction 认的是 a.takeoutCurrent（推迟 / 已点好都靠它记账）
	a.takeoutCurrent = info
	a.mu.Unlock()

	text := strings.TrimSpace(info.Meal)
	if msg := strings.TrimSpace(info.Message); msg != "" {
		if text != "" {
			text += " · " + msg
		} else {
			text = msg
		}
	}
	a.addBreakExtra(model.BreakExtra{
		Kind:    "takeout",
		Name:    "外卖提醒",
		Text:    text,
		At:      info.At,
		Takeout: info,
	})
	return true
}

// finishBreakExtras 休息收尾时处理副条。
//
// carry=true 表示马上要开始另一次休息（长休息抢占短休息）：副条跟着搬过去，
// 不能随旧窗口一起消失（那些提醒已经被消费掉了，丢了就真的没了）。
// 否则：自定义提醒副条到此为止（它要传达的信息已经显示过），外卖若还没被处理则交回
// 独立窗口 —— 外卖是“要你去门口取餐”的实际动作，不能随休息窗一起关掉。
func (a *App) finishBreakExtras(info *model.BreakInfo, carry bool) {
	if info == nil || info.Preview || len(info.Extras) == 0 {
		return
	}
	hasTakeout := false
	for _, e := range info.Extras {
		if e.Kind == "takeout" {
			hasTakeout = true
		}
	}
	if carry {
		a.mu.Lock()
		a.carryExtras = append([]model.BreakExtra(nil), info.Extras...)
		a.mu.Unlock()
		return
	}
	if hasTakeout {
		a.handoffTakeoutExtra()
	}
}

// handoffTakeoutExtra 休息结束时，把“并进来、还没被处理”的外卖重新弹成独立窗口。
//
// 判定条件是“有外卖会话、但一扇外卖窗都没有”——有窗口说明它是独立弹的，本来就在
// 自己身上；没有窗口只可能是它被并进了刚结束的这次休息。
func (a *App) handoffTakeoutExtra() {
	a.mu.Lock()
	cur := a.takeoutCurrent
	winless := cur != nil && !cur.Preview && len(a.takeoutWins) == 0
	slot, count, at := model.TimeSlot{}, 0, int64(0)
	if winless {
		slot, count, at = cur.Slot, cur.PostponedCount, cur.At
	}
	a.mu.Unlock()
	if !winless {
		return
	}
	log.Printf("[break] 休息结束，把并进来的外卖提醒交回独立窗口")
	a.showTakeoutWindowsAt(slot, false, count, at)
}
