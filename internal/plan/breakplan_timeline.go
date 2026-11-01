package plan

import (
	"sort"
	"strconv"
	"time"
	"workerrest/internal/jsonx"
	"workerrest/internal/model"
	"workerrest/internal/settings"
)

// ---------- 今日时间线 ----------

// cdExpandMax 倒计时链展开的安全上限：intervalMs 极小时防止同一天撑出天文数字条目
const cdExpandMax = 64

// expandBuiltinCountdown 内置短/长倒计时链：从挂起那一跳出发，按调度器的真实节奏推算
// 今天剩余的每一次，供今日时间线展示（固定线/外卖早已展开全天，倒计时此前只列一跳）。
// 推演口径与调度器逐条对齐：
//   - 计数推进 = advanceCounter（短 +1、长清零；只有双倒计时才参与长短交替）；
//   - 锚点 = 上一跳**开始**时刻 + 上一跳的提醒时长（调度器是「休息结束 → replan」，
//     replan 时刻 ≈ 开始 + 时长；用落点起算会与真实节奏漂移）；
//   - 落点 = 锚点 + 下一跳的 intervalMs；
//   - 交替 = 双倒计时且 ssL >= longEveryNShort 时选长（与 builtinCountdownLine 同口径）。
func expandBuiltinCountdown(s *settings.Settings, now int64, shortsSinceLong int, skip map[string]bool, first *model.PlannedBreak) []model.PlannedBreak {
	if first == nil {
		return []model.PlannedBreak{}
	}
	cfgOf := func(t string) model.BreakConfig { return s.Break(t) }
	out := []model.PlannedBreak{*first}
	curType, curAt, ssL := first.Type, first.At, shortsSinceLong
	for i := 0; i < cdExpandMax; i++ {
		// ① 模拟 advanceCounter：上一跳被消费后计数如何推进（挂起跳本身尚未计数）
		if curType == "short" {
			ssL++
		} else if curType == "long" {
			ssL = 0
		}
		// ② 下一跳类型（单倒计时恒为那一类型，短/长交替只在双倒计时时发生）
		var cds []string
		for _, t := range []string{"short", "long"} {
			c := cfgOf(t)
			if c.Enabled && c.Mode == "countdown" {
				cds = append(cds, t)
			}
		}
		if len(cds) == 0 {
			break
		}
		nextType := cds[0]
		if len(cds) == 2 && ssL >= s.LongEveryNShort() {
			nextType = "long"
		}
		// ③ 锚点 = 上一跳开始 + 上一跳时长；退化配置直接收链防死循环
		curCfg, nextCfg := cfgOf(curType), cfgOf(nextType)
		if curCfg.DurationMs <= 0 || nextCfg.IntervalMs <= 0 {
			break
		}
		at := curAt + curCfg.DurationMs + nextCfg.IntervalMs
		// ⑤ 不前进（异常）或已跨天 → 今天到此为止
		if at <= curAt || !sameLocalDay(at, now) {
			break
		}
		curType, curAt = nextType, at
		// ⑥ 已消费的这一跳不展示，但链条照常推进（与固定线的处理一致）
		if skip[occurrenceKey(nextType, at)] {
			continue
		}
		name := shortBreakName
		if nextType == "long" {
			name = longBreakName
		}
		out = append(out, model.PlannedBreak{Type: nextType, Name: name, At: at, Source: "countdown"})
	}
	return out
}

// expandCustomCountdown 自定义提醒倒计时链：anchor = 上一跳 at + 上一跳时长 + 自己的
// intervalMs，同样剔已消费、今天截断、上限保护。
//
// 为什么加时长：调度器是「休息结束 → replan → now + interval」，replan 发生在窗口关闭
// 那一刻（≈ 开始 + 时长）。只加 interval 会把整条链算早一个「提醒时长」，用户按今日
// 时间线核对时会发现「写着 10:00，实际 10:05 才提醒」。
func expandCustomCountdown(now int64, skip map[string]bool, first model.PlannedBreak, intervalMs, durationMs int64) []model.PlannedBreak {
	out := []model.PlannedBreak{first}
	curAt := first.At
	step := durationMs + intervalMs
	for i := 0; i < cdExpandMax && step > 0; i++ {
		at := curAt + step
		if !sameLocalDay(at, now) {
			break
		}
		curAt = at
		if skip[occurrenceKey(first.Type, at)] {
			continue
		}
		out = append(out, model.PlannedBreak{Type: first.Type, Name: first.Name, At: at, Source: "countdown"})
	}
	return out
}

// TimelineOptions todayTimeline 的入参（对应 breakPlan.ts 的 opts）
type TimelineOptions struct {
	Skip             map[string]bool
	Shifts           map[string]int64
	CountdownLines   []model.PlannedBreak
	Paused           bool
	TakeoutFired     map[string]bool
	TakeoutPostponed *model.TakeoutPostponed
	ShortsSinceLong  int
}

// todayTimeline 今日提醒时间线（时间规划页顶部的折叠列表）：综合当前全部提醒设置，
// 推算**当天**尚未触发的提醒时刻，按时间排序。
func todayTimeline(s *settings.Settings, now int64, opts TimelineOptions) []model.TimelineItem {
	out := []model.TimelineItem{}
	// ① 固定时刻线：展开当天全部剩余 occurrence（不只下一跳）
	for _, e := range s.BreakEntries() {
		c := e.Config
		if !c.Enabled || c.Mode != "fixed" {
			continue
		}
		from := now
		for i := 0; i < len(c.Slots)+2; i++ {
			hit, ok := nextSlotAt(c.Slots, from)
			if !ok || !sameLocalDay(hit.At, now) {
				break
			}
			if opts.Skip[occurrenceKey(e.ID, hit.At)] {
				from = hit.At + 1
				continue
			}
			// 被推迟的那一跳：原时刻不再展示（推迟后的时刻由 shifts 里覆盖过的值体现）
			if shift, ok := opts.Shifts[e.ID]; ok && hit.At < shift {
				from = hit.At + 1
				continue
			}
			out = append(out, model.TimelineItem{
				Key: occurrenceKey(e.ID, hit.At), Type: e.ID, Name: e.Name, At: hit.At, Source: "fixed",
			})
			from = hit.At + 1
		}
		// 被推迟的固定时刻线：上面的循环把「原时刻那一跳」跳过了，但没把「推迟后的时刻」
		// 补回来——否则推迟后该条会从今日时间线消失。这里把 shifts 里覆盖过的时刻补成
		// 这一身份下一次实际触发（若该时刻已作为后续 slot 出现则不再重复）。
		if shift, ok := opts.Shifts[e.ID]; ok && shift > now {
			dup := false
			for _, it := range out {
				if it.Type == e.ID && it.At == shift {
					dup = true
					break
				}
			}
			if !dup {
				out = append(out, model.TimelineItem{
					Key: occurrenceKey(e.ID, shift), Type: e.ID, Name: e.Name, At: shift, Source: "fixed",
				})
			}
		}
	}
	// ② 外卖：展开当天剩余条目（原定时刻；提前通知只弹小窗，真实窗口仍按原定时刻弹出）
	if s.TakeoutEnabled() {
		day := time.UnixMilli(now)
		for _, sl := range s.TakeoutSlots() {
			if !sl.Enabled {
				continue
			}
			if sl.Repeat == "weekly" && !intIn(sl.Days, isoWeekday(day)) {
				continue
			}
			at := slotAlertAt(sl, 0, now)
			if at <= now {
				continue
			}
			if opts.TakeoutFired[occurrenceKey("takeout", at)] {
				continue
			}
			out = append(out, model.TimelineItem{
				Key:  "takeout#" + strconv.FormatInt(at, 10) + "#" + sl.ID,
				Type: "takeout", Name: TAKEOUT_NAME, At: at, Source: "fixed",
			})
		}
		if pp := opts.TakeoutPostponed; pp != nil && pp.DueAt > now {
			out = append(out, model.TimelineItem{
				Key:  "takeout#pp#" + strconv.FormatInt(pp.DueAt, 10),
				Type: "takeout", Name: TAKEOUT_NAME, At: pp.DueAt, Source: "fixed",
			})
		}
	}
	// ③ 倒计时线展开成链：链首 = 挂起那一跳（保留调度器平移/冻结结果），
	// 后续按调度器真实节奏（开始+时长 → 下一跳间隔）推算今天剩余的每一次；暂停时全链冻结。
	paused := opts.Paused
	pushCd := func(p model.PlannedBreak) {
		flag := paused
		out = append(out, model.TimelineItem{
			Key:  "cd#" + p.Type + "#" + strconv.FormatInt(p.At, 10),
			Type: p.Type, Name: p.Name, At: p.At, Source: "countdown", Paused: &flag,
		})
	}
	var cds []model.PlannedBreak
	for _, l := range opts.CountdownLines {
		if l.Source == "countdown" {
			cds = append(cds, l)
		}
	}
	// 内置短/长：最多一条挂起线，展开成链
	var builtinFirst *model.PlannedBreak
	for i := range cds {
		if cds[i].Type == "short" || cds[i].Type == "long" {
			builtinFirst = &cds[i]
			break
		}
	}
	for _, p := range expandBuiltinCountdown(s, now, opts.ShortsSinceLong, opts.Skip, builtinFirst) {
		pushCd(p)
	}
	// 自定义提醒：每条各一条链；配置里已删/已改模式的残留线退化为只显示挂起一跳（旧行为）
	for _, l := range cds {
		if l.Type == "short" || l.Type == "long" {
			continue
		}
		c := s.CustomBreakByID(l.Type)
		cfg := model.BreakConfig{}
		if c != nil {
			cfg = settings.BreakConfigOf(jsonx.AsMap(c["config"]))
		}
		if c == nil || cfg.Mode != "countdown" {
			pushCd(l)
			continue
		}
		for _, p := range expandCustomCountdown(now, opts.Skip, l, cfg.IntervalMs, cfg.DurationMs) {
			pushCd(p)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At < out[j].At })
	return out
}

// intIn 整数是否在切片里（小切片线性查找就够）
func intIn(a []int, v int) bool {
	for _, x := range a {
		if x == v {
			return true
		}
	}
	return false
}

// fmtClock 日志用的 HH:MM
func fmtClock(ms int64) string {
	if ms == 0 {
		return "0"
	}
	return time.UnixMilli(ms).Format("15:04")
}
