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
	"sort"
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
	// pickMessage 会推进顺序游标 / 用 rng，必须在锁内调（与 ShowBreakSession 同口径）；
	// 顺手把锁内这份配置当快照传进去，提醒内容就只从这一份读。
	text := strings.Join(a.pickMessage(a.cfg, kind, name), " ")
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
	// 没被接力成一扇窗的自定义提醒：它在这扇休息窗的右上角**显示过**，统计里就该有
	// 这一次（2026-11-16 用户口径：与正常显示时一样）。必须在丢弃之前记 —— 这些
	// occurrence 在并入时就已经标成 consumed 了，之后不会再提醒第二次。
	//
	// 被接力成新窗的那种不在这里记：它走 OnBreakStart / OnBreakEnd，与独立弹窗同一套
	// （见 takeBreakQueue —— 它把这些卡从会话里摘走，这里就看不到它们了）。
	a.countExtras(info.Extras)
	if hasTakeout {
		a.handoffTakeoutExtra()
	}
}

// countExtras 把一串副条里的自定义提醒按“显示过一次”补记账（外卖不算 —— 它有自己
// 的次数口径，走 TakeoutAction 那条路）。
func (a *App) countExtras(list []model.BreakExtra) {
	for _, e := range list {
		if e.Kind == "takeout" || e.Kind == "" {
			continue
		}
		a.statsStore().OnCustomMerged(e.Kind, e.Name)
	}
}

// takeBreakQueue 提前结束时，把当前会话右上角的卡取下来（按到点时刻升序）。
//
// **提前结束只结束当前这一扇窗**（2026-11-16 用户口径）：右上角的卡不该跟着消失。
// 取下来而不是留在会话里，是为了让收窗路径（finishBreakExtras）看到空清单，从而不按
// “丢弃 / 补记”处理它们 —— 这些卡由 schedEndBreak 统一分配：队头接替成一扇新窗，
// 其余继续挂在那扇窗的右上角。
//
// 全是外卖（或没有卡）时返回 nil，保持原路径：外卖本来就该交回它自己的独立窗口，
// 那是它的窗口形态，不是“卡消失”。
func (a *App) takeBreakQueue() []model.BreakExtra {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sess == nil || a.sess.Preview || len(a.sess.Extras) == 0 {
		return nil
	}
	hasCustom := false
	for _, e := range a.sess.Extras {
		if e.Kind != "takeout" && e.Kind != "" {
			hasCustom = true
			break
		}
	}
	if !hasCustom {
		return nil
	}
	q := append([]model.BreakExtra(nil), a.sess.Extras...)
	// 稳定排序：到点时刻相同时保持并入顺序，别让同一刻的两条每次换个先后
	sort.SliceStable(q, func(i, j int) bool { return q[i].At < q[j].At })
	a.sess.Extras = nil
	return q
}

// queueLeadsNewWindow 这串队列会不会被队头接替成一扇**休息窗**。
//
// false = 队列为空，或队头是外卖（外卖走它自己的独立窗口，不经过 ShowBreakSession）。
// 判定与 resumeFromQueue 里的分支一一对应 —— 提示音要不要让出去（见 schedEndBreak）
// 就认这一条，两处口径不能各写一份。
func queueLeadsNewWindow(queue []model.BreakExtra) bool {
	return len(queue) > 0 && queue[0].Takeout == nil
}

// resumeFromQueue 接着显示提前结束时取下来的那串卡：队头开成一扇新窗，其余挂它右上角。
//
// 返回是否真的接上了**一扇新的休息窗**（队头被接替）。false 表示没有新窗在场：
// 队头是外卖（交回它自己的独立窗口），或那条提醒已经开不出窗（刚被删 / 停用）。
// 调用方据此把让出去的结束音补回来 —— 这一次提前结束总得有一声。
func (a *App) resumeFromQueue(queue []model.BreakExtra) bool {
	if len(queue) == 0 {
		return false
	}
	next, rest := queue[0], queue[1:]
	// 队头是外卖（只有当它比所有自定义卡都更早到点）：外卖有自己的窗口与记账，
	// 交给既有交接路径；剩下没有窗口承载的卡按“显示过”补记，别静默消失。
	if next.Takeout != nil {
		a.countExtras(rest)
		a.handoffTakeoutExtra()
		return false
	}
	// 其余卡先挂好，再开窗：ShowBreakSession 在**建会话的那一刻**读 carryExtras，
	// 晚一步设就接不上（新窗首帧会是空的）。
	if len(rest) > 0 {
		a.mu.Lock()
		a.carryExtras = append([]model.BreakExtra(nil), rest...)
		a.mu.Unlock()
	}
	if id := a.sched.StartQueuedBreak(next.Kind); id != "" {
		log.Printf("[break] 提前结束当前窗：右上角最早的一条接替成一扇新窗 —— %s（%s，其余 %d 条继续挂在右上角）",
			next.Name, next.Kind, len(rest))
		return true
	}
	// 开不出来（提醒刚被删 / 停用，或调度器不在可开窗状态）：把刚挂上的 carry 撤回，
	// 否则它们会漂到下一次休息去；这些卡一律按“显示过”记账。
	a.mu.Lock()
	a.carryExtras = nil
	a.mu.Unlock()
	log.Printf("[break] 提前结束：%s（%s）接不上窗口（配置已删 / 停用？），按已显示记账", next.Name, next.Kind)
	a.countExtras(queue)
	if hasTakeoutExtra(queue) {
		a.handoffTakeoutExtra()
	}
	return false
}

// hasTakeoutExtra 这串副条里有没有外卖（决定接不上窗口时要不要兜底交回独立窗）
func hasTakeoutExtra(list []model.BreakExtra) bool {
	for _, e := range list {
		if e.Kind == "takeout" {
			return true
		}
	}
	return false
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
