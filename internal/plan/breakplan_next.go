package plan

import (
	"sort"
	"strconv"
	"strings"
	"time"
	"workerrest/internal/model"
	"workerrest/internal/settings"
)

// ---------- 下一次休息（breakPlan.ts） ----------

// occurrenceKey 已消费 occurrence 的去重键：`${type}#${计划时刻毫秒}`
func occurrenceKey(typ string, at int64) string {
	return typ + "#" + strconv.FormatInt(at, 10)
}

// occurrenceKeyAt 拆出 occurrenceKey 里的时刻（拆不出返回 0，与 JS 的 Number("")===0 同义）
func occurrenceKeyAt(key string) int64 {
	i := strings.LastIndex(key, "#")
	if i < 0 {
		return 0
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(key[i+1:]), 64)
	if err != nil || f != f { // NaN
		return 0
	}
	return int64(f)
}

// SlotHit 时刻表的命中结果
type SlotHit struct {
	At   int64
	Slot model.TimeSlot
}

// nextSlotAt 下一次触发的时刻：返回最近一次触发的时刻与对应条目，没有启用的条目返回 false。
// 逐字对应 types.ts 的 nextSlotAt —— 每个条目各往后找 8 天，找到第一个**严格晚于** from 的
// 时刻就停（这一条不再往后看）；周重复还要落脚那天在勾选范围内。
func nextSlotAt(slots []model.TimeSlot, from int64) (SlotHit, bool) {
	var best SlotHit
	found := false
	base := startOfDay(time.UnixMilli(from))
	for _, slot := range slots {
		if !slot.Enabled {
			continue
		}
		for d := 0; d < 8; d++ {
			day := base.AddDate(0, 0, d)
			if slot.Repeat == "weekly" && !intIn(slot.Days, isoWeekday(day)) {
				continue
			}
			at := time.Date(day.Year(), day.Month(), day.Day(), slot.Hour, slot.Minute, 0, 0, day.Location())
			ms := at.UnixMilli()
			if ms <= from {
				continue
			}
			if !found || ms < best.At {
				best = SlotHit{At: ms, Slot: slot}
				found = true
			}
			break
		}
	}
	return best, found
}

// builtinCountdownLine 内置短/长的倒计时线：没有启用中的倒计时类型时返回 false。
// 自定义提醒不参与这条线（各自独立成线），所以这里只看 short / long。
func builtinCountdownLine(s *settings.Settings, now int64, shortsSinceLong int) (model.PlannedBreak, bool) {
	var cds []string
	short, long := s.Break("short"), s.Break("long")
	if short.Enabled && short.Mode == "countdown" {
		cds = append(cds, "short")
	}
	if long.Enabled && long.Mode == "countdown" {
		cds = append(cds, "long")
	}
	if len(cds) == 0 {
		return model.PlannedBreak{}, false
	}
	// 短/长都是倒计时：沿用“每 N 次短休息后换长休息”的节奏；只有一方是倒计时时就用那一方
	typ := cds[0]
	if len(cds) == 2 && shortsSinceLong >= s.LongEveryNShort() {
		typ = "long"
	}
	cfg := short
	name := shortBreakName
	if typ == "long" {
		cfg, name = long, longBreakName
	}
	return model.PlannedBreak{
		Type: typ, Name: name,
		// 倒计时线不受任何时段限制：就是“从当前这一刻起再过 intervalMs”。
		// 已消费的 occurrence 由 schedulerLines 的 skip 过滤统一剔除。
		At: now + cfg.IntervalMs, Source: "countdown",
	}, true
}

// fixedLines 固定时刻线：每种启用且为固定时间模式的休息各查一次时刻表。
//
// skip 里的 occurrence 表示“这一班已经处理过了”（跳过 / 已弹出 / 推迟后又被判定跳过）：
// 顺着时刻表继续找**再下一次**，而不是把这条身份整个丢掉。曾经这里只取最近一班，
// 于是“在提前提示窗里跳过 12:00”会让 12:00 之后那班也不排了 —— 这条提醒在当天剩下的
// 时间里彻底消失，直到下一次 replan 才回来。口径与 fixedNextMap 一致。
func fixedLines(s *settings.Settings, now int64, skip map[string]bool) []model.PlannedBreak {
	out := []model.PlannedBreak{}
	for _, e := range s.BreakEntries() {
		if !e.Config.Enabled || e.Config.Mode != "fixed" {
			continue
		}
		from := now
		for i := 0; i < 8; i++ {
			hit, ok := nextSlotAt(e.Config.Slots, from)
			if !ok {
				break
			}
			if skip[occurrenceKey(e.ID, hit.At)] {
				from = hit.At + 1 // 这一班已处理：看再下一次
				continue
			}
			slot := hit.Slot
			out = append(out, model.PlannedBreak{Type: e.ID, Name: e.Name, At: hit.At, Source: "fixed", Slot: &slot})
			break
		}
	}
	return out
}

// sortPlans 按时刻升序；时刻相同（多条撞在一起）时让内置长休息优先——
// 休息时间更长的那次覆盖短的那次。Array.sort 是稳定排序，这里用 SliceStable 对齐。
func sortPlans(pool []model.PlannedBreak) {
	sort.SliceStable(pool, func(i, j int) bool {
		a, b := pool[i], pool[j]
		if a.At != b.At {
			return a.At < b.At
		}
		if a.Type == "long" {
			return b.Type != "long"
		}
		return false
	})
}

// filterConsumed 剔除已消费的 occurrence：提前结束 / 立即休息 / 跳过过的安排不得再弹第二次。
//
// **倒计时线不在这里剔**：它的「消费」已经体现在 countdownAnchor 的锚点沿用判定里
// （跳过 / 弹过会把旧锚点记进 skip，逼下一轮换用新锚点 now+interval）。这里再按
// occurrence 剔一次，会在一个窄窗口把新线误删：跳过落在锚点刚生成的同一毫秒时，
// fresh = now+interval 恰好等于刚被消费的旧锚点，整条线消失、NextAt 归 0 ——
// 调度循环跑着时下一拍会自愈，但那 500ms 的空档本身就是错的；循环还没起
// （启动最初一拍 / 单测）就停在 0 上。固定时刻线的 At 是离散班次，与 consumed
// 键天然一一对应，不受影响。
func filterConsumed(pool []model.PlannedBreak, skip map[string]bool) []model.PlannedBreak {
	if len(skip) == 0 {
		return pool
	}
	out := make([]model.PlannedBreak, 0, len(pool))
	for _, p := range pool {
		if p.Source != "countdown" && skip[occurrenceKey(p.Type, p.At)] {
			continue
		}
		out = append(out, p)
	}
	return out
}

// cdAnchor 上一轮挂起的倒计时锚点：At = 触发时刻，Iv = 产生该锚点的间隔（ms）。
//
// 沿用条件只有“未到期 / 未消费 / 间隔未变”——暂停不再冻结时钟（2026-11-12 用户口径：
// 暂停期间倒计时不要停），所以暂停期间的锚点照样会过期；过期就按 now+interval 重算，
// 否则侧栏会一直停在 0:00（曾经的 Frozen 标记随旧口径一起删除）。
type cdAnchor struct {
	At int64
	Iv int64
}

// schedulerLines 调度器的全部挂起线（不含外卖——外卖窗口独立于休息调度）：
// 固定时刻线各取下一跳、内置短/长共用倒计时线、每条自定义提醒各一条倒计时线，
// 已消费的 occurrence 已剔除，按时刻升序。
//
// prev 是上一轮挂起的倒计时锚点（键 = 休息身份，见 Scheduler 的
// prevCountdownAnchorsLocked）：未到期、未被消费且间隔未变的锚点原样沿用——
// 设置页任意保存都会触发 replan，不能把用户已流逝的倒计时进度清零；
// 间隔改了的立刻按新间隔重新起算。
func schedulerLines(s *settings.Settings, now int64, shortsSinceLong int, skip map[string]bool, prev map[string]cdAnchor) []model.PlannedBreak {
	lines := fixedLines(s, now, skip)
	if cd, ok := builtinCountdownLine(s, now, shortsSinceLong); ok {
		cur, _ := s.BreakByID(cd.Type)
		cd.At = countdownAnchor(prev, cd.Type, now, skip, cd.At, cur.IntervalMs)
		lines = append(lines, cd)
	}
	// 自定义提醒：每条各一条倒计时线（内置的短/长已由上面那条线统一代表）
	for _, c := range s.CustomBreakList() {
		if !c.Config.Enabled || c.Config.Mode != "countdown" {
			continue
		}
		l := model.PlannedBreak{
			Type: c.ID, Name: c.Name, At: now + c.Config.IntervalMs, Source: "countdown",
		}
		l.At = countdownAnchor(prev, l.Type, now, skip, l.At, c.Config.IntervalMs)
		lines = append(lines, l)
	}
	pool := filterConsumed(lines, skip)
	sortPlans(pool)
	return pool
}

// countdownAnchor 倒计时线的锚点：上一轮同身份的锚点未被消费、且产生间隔与当前配置
// 一致时原样沿用（倒计时进度跨 replan 保留）；间隔已改的立刻按新间隔重新起算。
//
// **已到期（At 早于墙钟）的锚点同样沿用**：它是“这一跳已经到点、但还没有任何一拍把它
// 消费掉”的现场。旧实现在这里一律重算成 now+interval，于是两种很常见的时序会把这一次
// 提醒凭空抹掉（用户看不到窗、也没有任何记录）：
//   - 到点那一瞬（相隔一拍 500ms 内）用户恰好在设置页改了某项 → autoSave → replan；
//   - 另一条提醒正占着休息窗口，这条到点后要等那个窗口收掉（onTick 休息中直接 return），
//     期间任何一次 replan 都会把它推后一整个间隔。
//
// 沿用之后 NextAt 就停在过去，下一拍按“到点”弹窗；弹完 startBreakAt 会把它记进
// consumed，下一次 replan 走 skip 分支回到 fresh。真正该“从头计时”的场景（睡眠 / 改系统
// 时间）由 onTick 的时间大跳分支显式清空挂起线，不依赖这里。
func countdownAnchor(prev map[string]cdAnchor, typ string, now int64, skip map[string]bool, fresh, curIv int64) int64 {
	if a, ok := prev[typ]; ok && !skip[occurrenceKey(typ, a.At)] && a.Iv == curIv {
		return a.At
	}
	return fresh
}

// planNextBreak 算出下一次该休息的时刻；全部关闭 / 固定模式下没有启用条目时返回 false
func planNextBreak(s *settings.Settings, now int64, shortsSinceLong int, skip map[string]bool, prev map[string]cdAnchor) (model.PlannedBreak, bool) {
	pool := schedulerLines(s, now, shortsSinceLong, skip, prev)
	if len(pool) == 0 {
		return model.PlannedBreak{}, false
	}
	return pool[0], true
}

// describePlan 计划的中文摘要：日志与托盘提示共用（如“长休息（固定时刻）12:00”）
func describePlan(p *model.PlannedBreak) string {
	if p == nil {
		return "未安排休息"
	}
	src := "倒计时"
	if p.Source == "fixed" {
		src = "固定时刻"
	}
	return p.Name + "（" + src + "）" + time.UnixMilli(p.At).Format("15:04")
}

// takeoutLine 外卖提醒的“原定时刻”（下一次条目的时刻，不含提前通知——提前通知只是
// 先弹个小窗，真实提醒窗口仍在原定时刻弹出）。外卖不进休息调度（窗口独立、永不自动收窗），
// 这里只为设置页状态卡提供倒计时。固定时刻每天循环，查今天与明天两天足够覆盖“下一个还没到的时刻”。
func takeoutLine(s *settings.Settings, now int64) (model.PlannedBreak, bool) {
	if !s.TakeoutEnabled() {
		return model.PlannedBreak{}, false
	}
	slots := s.TakeoutSlots()
	if len(slots) == 0 {
		return model.PlannedBreak{}, false
	}
	best := int64(0)
	found := false
	// 用日历日推进（AddDate）而不是 now+86400000：夏令时切换日一天可能是 23/25 小时，
	// 加固定毫秒会在那两天落回同一天或跳过一天。中国无夏令时，但这段逻辑不该因此写死。
	base := time.UnixMilli(now)
	for d := 0; d < 2; d++ {
		day := base.AddDate(0, 0, d).UnixMilli()
		for _, sl := range slots {
			at := slotAlertAt(sl, 0, day)
			if at > now && (!found || at < best) {
				best, found = at, true
			}
		}
	}
	if !found {
		return model.PlannedBreak{}, false
	}
	return model.PlannedBreak{Type: "takeout", Name: TAKEOUT_NAME, At: best, Source: "fixed"}, true
}

// planNextBreakSplit 按设置页状态卡的口径把下一跳分成两组：短/长一组（“下次休息提醒”），
// 外卖 + 自定义提醒一组（“下次XX提醒”），两边互不混用。
// 全局 next（planNextBreak）不含外卖 —— 外卖窗口独立于休息调度；分组视图把它算进
// other 只是为了让状态卡能显示“下次外卖提醒还有多久”。
func planNextBreakSplit(s *settings.Settings, now int64, shortsSinceLong int, skip map[string]bool, prev map[string]cdAnchor) model.PlanSplit {
	lines := fixedLines(s, now, skip)
	if cd, ok := builtinCountdownLine(s, now, shortsSinceLong); ok {
		cur, _ := s.BreakByID(cd.Type)
		cd.At = countdownAnchor(prev, cd.Type, now, skip, cd.At, cur.IntervalMs)
		lines = append(lines, cd)
	}
	for _, c := range s.CustomBreakList() {
		if !c.Config.Enabled || c.Config.Mode != "countdown" {
			continue
		}
		l := model.PlannedBreak{
			Type: c.ID, Name: c.Name, At: now + c.Config.IntervalMs, Source: "countdown",
		}
		l.At = countdownAnchor(prev, l.Type, now, skip, l.At, c.Config.IntervalMs)
		lines = append(lines, l)
	}
	if tk, ok := takeoutLine(s, now); ok {
		lines = append(lines, tk)
	}
	pool := filterConsumed(lines, skip)
	pick := func(isBuiltin bool) *model.PlannedBreak {
		xs := make([]model.PlannedBreak, 0, len(pool))
		for _, p := range pool {
			builtin := p.Type == "short" || p.Type == "long"
			if builtin == isBuiltin {
				xs = append(xs, p)
			}
		}
		if len(xs) == 0 {
			return nil
		}
		sortPlans(xs)
		first := xs[0]
		return &first
	}
	return model.PlanSplit{Builtin: pick(true), Other: pick(false)}
}

// fixedNextMap 各“固定时刻线”身份下一次实际触发的时刻表（键 = BreakId 或 'takeout'）。
// 与 planNextBreak 同一口径：已消费的 occurrence 不返回，并顺着时刻表找**再下一次**
// （今天 12:00 被跳过后，摘要应显示明天 12:00，而不是把这条身份整个抹掉）。
// 倒计时模式的身份不出现在表里（摘要里倒计时本就不展示具体时刻）。
func fixedNextMap(s *settings.Settings, now int64, skip map[string]bool) map[string]int64 {
	out := map[string]int64{}
	for _, e := range s.BreakEntries() {
		c := e.Config
		if !c.Enabled || c.Mode != "fixed" {
			continue
		}
		from := now
		for i := 0; i < 8; i++ {
			hit, ok := nextSlotAt(c.Slots, from)
			if !ok {
				break
			}
			if !skip[occurrenceKey(e.ID, hit.At)] {
				out[e.ID] = hit.At
				break
			}
			from = hit.At + 1 // 这一次已被消费：找再下一次
		}
	}
	if tk, ok := takeoutLine(s, now); ok {
		out["takeout"] = tk.At
	}
	return out
}
