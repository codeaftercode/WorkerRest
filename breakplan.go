package main

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

// 休息计划层：electron/src/breakPlan.ts + fixedTime.ts + types.ts 的纯函数逐条移植。
//
// 这一层刻意**不持有任何状态、不碰 App**：喂进「一份配置 + 一个基准时刻 + 已消费集合」
// 就吐出「下一次该什么时候休息」。好处有两个：
//   - 它是整个调度器的口径来源，纯粹才能被 scripts/verify_plan_parity.py 拿去
//     与 Electron 自己的 breakPlan.ts 逐字段对拍（移植漏规则的错不报错，只能对拍出来）；
//   - 时间基准一律用**绝对毫秒时间戳**（JS 用 Date.now()，Go 用 UnixMilli），
//     两边都靠本地时区解释「几点几分」，不跨时区搬运。
//
// 模型（与 breakPlan.ts 的注释一致）：每种休息是一条「触发线」——
//   - 倒计时线：距上次休息 intervalMs 后触发（内置短/长**共用**一条，按
//     「每 N 次短休息接一次长休息」挑一条；自定义提醒各有一条自己的线）；
//   - 固定时刻线：每种休息各一条，按自己的时刻表找下一个时刻。
// 谁的更早就听谁的（同一时刻长休息优先）。全部关闭时返回 null。

// ---------- 类型 ----------

// BreakEntry 一条休息的身份 + 配置（内置短/长 + 全部自定义提醒）：计划与调度按它遍历
type BreakEntry struct {
	ID     string      `json:"id"`
	Name   string      `json:"name"`
	Config BreakConfig `json:"config"`
}

// PlannedBreak 计划出来的一次休息（托盘/状态卡直接用 Name）
type PlannedBreak struct {
	Type   string    `json:"type"`
	Name   string    `json:"name"`
	At     int64     `json:"at"`
	Source string    `json:"source"` // countdown | fixed
	Slot   *TimeSlot `json:"slot,omitempty"`
}

// PlanRef 状态卡上的「下一次 XX」分组（只有名字与时刻）
type PlanRef struct {
	Name string `json:"name"`
	At   int64  `json:"at"`
}

// PlanSplit 按设置页状态卡的口径把下一跳分成两组
type PlanSplit struct {
	Builtin *PlannedBreak `json:"builtin"`
	Other   *PlannedBreak `json:"other"`
}

// TimelineItem 今日提醒时间线的一项
//
// Paused 是**指针**：Electron 只给倒计时线的项写 paused 键（固定线没有这个键），
// 用 *bool + omitempty 才能连「键在不在」都对上。
type TimelineItem struct {
	Key    string `json:"key"`
	Type   string `json:"type"`
	Name   string `json:"name"`
	At     int64  `json:"at"`
	Source string `json:"source"`
	Paused *bool  `json:"paused,omitempty"`
}

// TakeoutPostponed 外卖的待推迟提醒（窗口上点「推迟」时由外卖模块记账）
type TakeoutPostponed struct {
	Slot  TimeSlot `json:"slot"`
	DueAt int64    `json:"dueAt"`
	Count int      `json:"count"`
}

// SchedulerState 调度器状态快照。
// 字段集**必须完整**：设置页的 renderStatus() 一上来就读 paused / pauseReasons.length /
// phase，缺一个就在 init 里抛错、整页停摆（见 electron/src/types.ts 的 SchedulerState）。
type SchedulerState struct {
	Phase string `json:"phase"` // idle | pre | break
	// NextType 下一次休息的身份（'short' | 'long' | 自定义 id）
	NextType string `json:"nextType"`
	// NextName 下一次休息的展示名（托盘提示与状态卡直接用，免得再回查配置）
	NextName string `json:"nextName"`
	NextAt   int64  `json:"nextAt"`
	// NextSource 没有安排（全部关闭）时为 null
	NextSource *string `json:"nextSource"`
	// Rest 短/长休息线里最近的一条（状态卡「下次休息提醒」格）；null = 两者都没安排
	Rest *PlanRef `json:"rest"`
	// Other 外卖提醒与所有自定义提醒里最近的一条（状态卡「下次XX提醒」格）；null = 都没启用
	Other *PlanRef `json:"other"`
	// Lines 调度器当前挂起的全部触发线（replan 时算好；推迟/恢复时跟着平移）
	Lines []PlannedBreak `json:"lines"`
	// Timeline 今日提醒时间线（主进程在状态快照时按当前设置与调度状态现算）
	Timeline []TimelineItem `json:"timeline"`
	// FixedNext 各「固定时刻线」身份下一次实际触发的时刻（键 = BreakId 或 'takeout'）
	FixedNext      map[string]int64 `json:"fixedNext"`
	BreakEndsAt    int64            `json:"breakEndsAt"`
	BreakStartedAt int64            `json:"breakStartedAt"`
	Paused         bool             `json:"paused"`
	PauseReasons   []string         `json:"pauseReasons"`
	// PausedAt 进入暂停的时刻（冻结倒计时用：暂停期间剩余时间按此锚定，不再往下掉）
	PausedAt        int64 `json:"pausedAt"`
	ShortsSinceLong int   `json:"shortsSinceLong"`
}

// ---------- 常量 ----------

// TAKEOUT_NAME 外卖提醒的展示名。它不在 BreakEntries 里（不是一种「休息」），
// 但窗口与提前提示都要用它，所以单独给一个常量。
const TAKEOUT_NAME = "外卖提醒"

// 内置短/长的展示名（不是用户起的，不进配置）
const (
	shortBreakName = "短休息"
	longBreakName  = "长休息"
)

// weekdays 星期取值与 short 文案（与 types.ts 的 WEEKDAYS 一致，1=周一 … 7=周日）
var weekdays = []struct {
	Value int
	Short string
}{
	{1, "一"}, {2, "二"}, {3, "三"}, {4, "四"}, {5, "五"}, {6, "六"}, {7, "日"},
}

// takeoutMealLabel 按时刻给出餐段文案（外卖窗口的大标题）
func takeoutMealLabel(hour int) string {
	switch {
	case hour >= 5 && hour < 10:
		return "早餐"
	case hour >= 10 && hour < 14:
		return "午餐"
	case hour >= 14 && hour < 17:
		return "下午茶"
	case hour >= 17 && hour < 21:
		return "晚餐"
	default:
		return "夜宵"
	}
}

// preBreakTitle 提前提示弹窗的标题：自定义提醒用「XX时间到」。
// 名称本身就以「时间」结尾时把这俩字去掉，避免「喝水时间时间到」这种叠字。
func preBreakTitle(name string) string {
	base := name
	if strings.HasSuffix(base, "时间") {
		base = base[:len(base)-len("时间")]
	}
	return base + "时间到"
}

// preBreakCountdownTail 自定义提醒提前弹窗里倒计时的后半句：`还有 10 秒显示 XX 提醒`。
// 名称后缀已经是「提醒」时不再补一次。返回值带前导空格 —— 它接在数字后面。
func preBreakCountdownTail(name string) string {
	base := name
	if strings.HasSuffix(base, "提醒") {
		base = base[:len(base)-len("提醒")]
	}
	return " 秒显示" + base + "提醒"
}

// ---------- 配置读出（计划层要用的那几个视图） ----------

// LongEveryNShort 每经过 N 次短休息后触发一次长休息
func (s *Settings) LongEveryNShort() int {
	n := s.Int(2, "longEveryNShort")
	if n > 1<<30 || n < -1<<30 {
		return 2
	}
	return int(n)
}

// IdlePauseMin 系统空闲多少分钟后自动暂停（始终启用、不可关闭；最小 1 分钟）
func (s *Settings) IdlePauseMin() int {
	n := s.Int(5, "idlePauseMin")
	if n < 1 {
		return 1
	}
	if n > 1<<30 {
		return 1 << 30
	}
	return int(n)
}

// DndPause 系统勿扰时暂停
func (s *Settings) DndPause() bool { return s.Flag(true, "dndPause") }

// TakeoutLeadMin 外卖「提前通知」分钟数（0=到点提醒）
func (s *Settings) TakeoutLeadMin() int { return clampInt(s.Int(0, "takeout", "leadMin"), 0, 120) }

// TakeoutPostponeMin 外卖「可推迟」分钟数
func (s *Settings) TakeoutPostponeMin() int {
	return clampInt(s.Int(10, "takeout", "postponeMin"), 1, 120)
}

// TakeoutRedPacket 是否显示外卖优惠券
func (s *Settings) TakeoutRedPacket() bool { return s.Flag(false, "takeout", "redPacket") }

// AppPauseItem 指定应用运行时暂停的一条规则
type AppPauseItem struct {
	ID      string
	Exe     string // 可执行文件名（只存 basename，匹配时忽略路径与大小写）
	Name    string // 软件名：只用于显示，可留空（界面回退到可执行文件名）
	Enabled bool
}

// AppPauseConfig 指定应用运行时自动暂停（总开关默认关，与升级前行为一致）
type AppPauseConfig struct {
	Enabled bool
	Items   []AppPauseItem
}

// AppPauseCfg 指定应用运行时自动暂停（enabled 只认真正的 true）
func (s *Settings) AppPauseCfg() AppPauseConfig {
	return AppPauseConfig{
		Enabled: s.Flag(false, "appPause", "enabled"),
		Items:   s.AppPauseItems(),
	}
}

// AppPauseItems 应用列表（已归一化：非对象项与空 exe 丢弃、按小写去重）
func (s *Settings) AppPauseItems() []AppPauseItem {
	seen := map[string]bool{}
	out := []AppPauseItem{}
	for _, it := range anySlice(s.lookup("appPause", "items")) {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		exe := exeBasename(mapStr(m, "exe", ""))
		if exe == "" {
			continue
		}
		key := strings.ToLower(exe)
		if seen[key] {
			continue
		}
		seen[key] = true
		name := appPauseLabel(exe)
		if v, ok := m["name"].(string); ok {
			name = normalizeAppName(v)
		}
		out = append(out, AppPauseItem{
			ID:      mapStr(m, "id", ""),
			Exe:     exe,
			Name:    name,
			Enabled: mapFlag(m, "enabled", true),
		})
	}
	return out
}

// matchAppPause 纯函数：拿当前运行的进程名（小写）判断是否命中暂停规则。
// 判定逻辑抽出来是为了让 watcher 与测试共用同一份实现，不碰任何状态。
func matchAppPause(cfg AppPauseConfig, runningNames []string) (hit bool, matched []string) {
	if !cfg.Enabled || len(cfg.Items) == 0 {
		return false, nil
	}
	running := make(map[string]bool, len(runningNames))
	for _, n := range runningNames {
		running[strings.ToLower(n)] = true
	}
	for _, it := range cfg.Items {
		if it.Enabled && running[strings.ToLower(it.Exe)] {
			matched = append(matched, it.Exe)
		}
	}
	return len(matched) > 0, matched
}

func clampInt(v int64, min, max int) int {
	if v < int64(min) {
		return min
	}
	if v > int64(max) {
		return max
	}
	return int(v)
}

// CustomBreak 一条自定义提醒（设置页「时间规划」页底部新建的第 3 种及以后提醒）。
// 选项、默认值与含义与「短休息」完全一致（同一套 BreakConfig），提醒内容也自成一套。
type CustomBreak struct {
	ID     string
	Name   string
	Config BreakConfig
}

// CustomBreakList 全部自定义提醒（配置里 customBreaks 数组的顺序即设置页卡片顺序）
func (s *Settings) CustomBreakList() []CustomBreak {
	raw := anySlice(s.lookup("customBreaks"))
	out := make([]CustomBreak, 0, len(raw))
	for _, it := range raw {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, CustomBreak{
			ID:     mapStr(m, "id", ""),
			Name:   mapStr(m, "name", ""),
			Config: breakConfigOf(asMap(m["config"])),
		})
	}
	return out
}

// CustomBreakByID 按 id 取一条自定义提醒的原始对象（取不到返回 nil）
func (s *Settings) CustomBreakByID(id string) map[string]any {
	if id == "" {
		return nil
	}
	for _, it := range anySlice(s.lookup("customBreaks")) {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		if mapStr(m, "id", "") == id {
			return m
		}
	}
	return nil
}

// BreakEntries 全部休息的清单（内置短/长 + 自定义各条，顺序固定）：计划与调度按它遍历。
// 内置两条永远在列，自定义提醒被删掉后自然消失。
func (s *Settings) BreakEntries() []BreakEntry {
	out := []BreakEntry{
		{ID: "short", Name: shortBreakName, Config: s.Break("short")},
		{ID: "long", Name: longBreakName, Config: s.Break("long")},
	}
	for _, c := range s.CustomBreakList() {
		out = append(out, BreakEntry{ID: c.ID, Name: c.Name, Config: c.Config})
	}
	return out
}

// BreakByID 按身份取配置；这条休息已不存在（设置页删掉了自定义提醒）时返回 false，
// 调用方各自兜底（与 types.ts 的 breakConfigById 返回 null 同义）
func (s *Settings) BreakByID(id string) (BreakConfig, bool) {
	for _, e := range s.BreakEntries() {
		if e.ID == id {
			return e.Config, true
		}
	}
	return BreakConfig{}, false
}

// BreakNameByID 按身份取展示名；不认识的身份回落成「休息」
func (s *Settings) BreakNameByID(id string) string {
	for _, e := range s.BreakEntries() {
		if e.ID == id {
			return e.Name
		}
	}
	return "休息"
}

// ---------- 固定时刻（fixedTime.ts） ----------

// pad2 两位补零（与 fixedTime.ts 的 pad2 同义）
func pad2(n int) string {
	if n < 10 {
		if n < 0 {
			return strconv.Itoa(n)
		}
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}

// slotTimeText 时刻文本 HH:MM（界面与日志共用）
func slotTimeText(s TimeSlot) string {
	return pad2(s.Hour) + ":" + pad2(s.Minute)
}

// isoWeekday 与 types.ts 的 isoWeekday 同义：1=周一 … 7=周日
// （Go 的 time.Weekday 与 JS 的 getDay() 同为 0=周日 … 6=周六，换算一致）
func isoWeekday(t time.Time) int {
	w := int(t.Weekday())
	if w == 0 {
		return 7
	}
	return w
}

// localDateKey YYYY-MM-DD（本地时区，与 JS 的 `${y}-${pad2(m)}-${pad2(d)}` 同义）
func localDateKey(t time.Time) string {
	return strconv.Itoa(t.Year()) + "-" + pad2(int(t.Month())) + "-" + pad2(t.Day())
}

// startOfDay 本地零点
func startOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// sameLocalDay 两个时间戳是否落在本地的同一天
func sameLocalDay(a, b int64) bool {
	ta, tb := time.UnixMilli(a), time.UnixMilli(b)
	return ta.Year() == tb.Year() && ta.Month() == tb.Month() && ta.Day() == tb.Day()
}

// slotAlertAt 提前量下「实际弹出提醒的时刻」：条目时刻减去提前量（跨天时自动落到前一天）
func slotAlertAt(slot TimeSlot, leadMs, dayMs int64) int64 {
	d := time.UnixMilli(dayMs)
	at := time.Date(d.Year(), d.Month(), d.Day(), slot.Hour, slot.Minute, 0, 0, d.Location())
	return at.UnixMilli() - leadMs
}

// slotFireKey 触发去重键：同一条目在同一天同一时刻（精确到分钟）只会触发一次
func slotFireKey(slot TimeSlot, nowMs, leadMs int64) string {
	t := time.UnixMilli(nowMs + leadMs)
	return slot.ID + "#" + localDateKey(t) + "#" + slotTimeText(slot)
}

// isTimeSlotDue 这一刻该条目是否到期：启用中、时刻吻合（含提前量）、周期吻合
func isTimeSlotDue(slot TimeSlot, nowMs, leadMs int64) bool {
	if !slot.Enabled {
		return false
	}
	t := time.UnixMilli(nowMs + leadMs)
	if slot.Hour != t.Hour() || slot.Minute != t.Minute() {
		return false
	}
	if slot.Repeat == "weekly" && !intIn(slot.Days, isoWeekday(t)) {
		return false
	}
	return true
}

// findDueTimeSlots 找出给定的时刻表中此刻所有到期的条目，并把它们记入已触发集合
// （重复调用不会重复返回同一个时刻）。fired 跨 tick 复用同一个 map。
func findDueTimeSlots(slots []TimeSlot, nowMs int64, fired map[string]bool, leadMs int64) []TimeSlot {
	var out []TimeSlot
	for _, slot := range slots {
		if !isTimeSlotDue(slot, nowMs, leadMs) {
			continue
		}
		key := slotFireKey(slot, nowMs, leadMs)
		if fired[key] {
			continue
		}
		fired[key] = true
		out = append(out, slot)
	}
	pruneFiredSlotKeys(fired, nowMs+leadMs)
	return out
}

// pruneFiredSlotKeys 只保留「当前这一天」的键：跨天后旧键没有意义，长期运行也不会把集合撑大
func pruneFiredSlotKeys(fired map[string]bool, nowMs int64) {
	today := "#" + localDateKey(time.UnixMilli(nowMs)) + "#"
	for k := range fired {
		if !strings.Contains(k, today) {
			delete(fired, k)
		}
	}
}

// describeTimeSlot 条目描述：界面与日志共用的中文摘要（如「每天 12:00」「每周 一、三 09:30」）
func describeTimeSlot(slot TimeSlot) string {
	txt := slotTimeText(slot)
	if slot.Repeat != "weekly" {
		return "每天 " + txt
	}
	var names []string
	for _, d := range weekdays {
		if intIn(slot.Days, d.Value) {
			names = append(names, d.Short)
		}
	}
	joined := strings.Join(names, "、")
	if joined == "" {
		joined = "（未选）"
	}
	return "每周 " + joined + " " + txt
}

// ---------- 下一次休息（breakPlan.ts） ----------

// occurrenceKey 已消费 occurrence 的去重键：`${type}#${计划时刻毫秒}`
func occurrenceKey(typ string, at int64) string {
	return typ + "#" + strconv.FormatInt(at, 10)
}

// occurrenceKeyAt 拆出 occurrenceKey 里的时刻（拆不出返回 0，与 JS 的 Number(”)===0 同义）
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
	Slot TimeSlot
}

// nextSlotAt 下一次触发的时刻：返回最近一次触发的时刻与对应条目，没有启用的条目返回 false。
// 逐字对应 types.ts 的 nextSlotAt —— 每个条目各往后找 8 天，找到第一个**严格晚于** from 的
// 时刻就停（这一条不再往后看）；周重复还要落脚那天在勾选范围内。
func nextSlotAt(slots []TimeSlot, from int64) (SlotHit, bool) {
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
func builtinCountdownLine(s *Settings, now int64, shortsSinceLong int) (PlannedBreak, bool) {
	var cds []string
	short, long := s.Break("short"), s.Break("long")
	if short.Enabled && short.Mode == "countdown" {
		cds = append(cds, "short")
	}
	if long.Enabled && long.Mode == "countdown" {
		cds = append(cds, "long")
	}
	if len(cds) == 0 {
		return PlannedBreak{}, false
	}
	// 短/长都是倒计时：沿用「每 N 次短休息后换长休息」的节奏；只有一方是倒计时时就用那一方
	typ := cds[0]
	if len(cds) == 2 && shortsSinceLong >= s.LongEveryNShort() {
		typ = "long"
	}
	cfg := short
	name := shortBreakName
	if typ == "long" {
		cfg, name = long, longBreakName
	}
	return PlannedBreak{
		Type: typ, Name: name,
		// 倒计时线不受任何时段限制：就是「从当前这一刻起再过 intervalMs」。
		// 已消费的 occurrence 由 schedulerLines 的 skip 过滤统一剔除。
		At: now + cfg.IntervalMs, Source: "countdown",
	}, true
}

// fixedLines 固定时刻线：每种启用且为固定时间模式的休息各查一次时刻表
func fixedLines(s *Settings, now int64) []PlannedBreak {
	out := []PlannedBreak{}
	for _, e := range s.BreakEntries() {
		if !e.Config.Enabled || e.Config.Mode != "fixed" {
			continue
		}
		if hit, ok := nextSlotAt(e.Config.Slots, now); ok {
			slot := hit.Slot
			out = append(out, PlannedBreak{Type: e.ID, Name: e.Name, At: hit.At, Source: "fixed", Slot: &slot})
		}
	}
	return out
}

// sortPlans 按时刻升序；时刻相同（多条撞在一起）时让内置长休息优先——
// 休息时间更长的那次覆盖短的那次。Array.sort 是稳定排序，这里用 SliceStable 对齐。
func sortPlans(pool []PlannedBreak) {
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

// filterConsumed 剔除已消费的 occurrence：提前结束 / 立即休息 / 跳过过的安排不得再弹第二次
func filterConsumed(pool []PlannedBreak, skip map[string]bool) []PlannedBreak {
	if len(skip) == 0 {
		return pool
	}
	out := make([]PlannedBreak, 0, len(pool))
	for _, p := range pool {
		if !skip[occurrenceKey(p.Type, p.At)] {
			out = append(out, p)
		}
	}
	return out
}

// schedulerLines 调度器的全部挂起线（不含外卖——外卖窗口独立于休息调度）：
// 固定时刻线各取下一跳、内置短/长共用倒计时线、每条自定义提醒各一条倒计时线，
// 已消费的 occurrence 已剔除，按时刻升序。
//
// prev 是上一轮挂起的倒计时锚点（键 = 休息身份，见 Scheduler 的 prevCountdownAnchors）：
// 仍未到期且未被消费的锚点原样沿用——设置页任意保存都会触发 replan，此前每一次
// replan 都把倒计时按 now+interval 重新起算，用户已流逝的进度被无声清零。
func schedulerLines(s *Settings, now int64, shortsSinceLong int, skip map[string]bool, prev map[string]int64) []PlannedBreak {
	lines := fixedLines(s, now)
	if cd, ok := builtinCountdownLine(s, now, shortsSinceLong); ok {
		cd.At = countdownAnchor(prev, cd.Type, now, skip, cd.At)
		lines = append(lines, cd)
	}
	// 自定义提醒：每条各一条倒计时线（内置的短/长已由上面那条线统一代表）
	for _, c := range s.CustomBreakList() {
		if !c.Config.Enabled || c.Config.Mode != "countdown" {
			continue
		}
		l := PlannedBreak{
			Type: c.ID, Name: c.Name, At: now + c.Config.IntervalMs, Source: "countdown",
		}
		l.At = countdownAnchor(prev, l.Type, now, skip, l.At)
		lines = append(lines, l)
	}
	pool := filterConsumed(lines, skip)
	sortPlans(pool)
	return pool
}

// countdownAnchor 倒计时线的锚点：上一轮同身份的锚点仍未到期且未被消费时原样沿用
// （倒计时进度跨 replan 保留，改间隔也从下一周期生效），否则按当前配置从 now 重新起算。
// 到期、被跳过/立即休息（已消费）或该身份被删除后，自然回落到 fresh。
func countdownAnchor(prev map[string]int64, typ string, now int64, skip map[string]bool, fresh int64) int64 {
	if old, ok := prev[typ]; ok && old > now && !skip[occurrenceKey(typ, old)] {
		return old
	}
	return fresh
}

// planNextBreak 算出下一次该休息的时刻；全部关闭 / 固定模式下没有启用条目时返回 false
func planNextBreak(s *Settings, now int64, shortsSinceLong int, skip map[string]bool, prev map[string]int64) (PlannedBreak, bool) {
	pool := schedulerLines(s, now, shortsSinceLong, skip, prev)
	if len(pool) == 0 {
		return PlannedBreak{}, false
	}
	return pool[0], true
}

// describePlan 计划的中文摘要：日志与托盘提示共用（如「长休息（固定时刻）12:00」）
func describePlan(p *PlannedBreak) string {
	if p == nil {
		return "未安排休息"
	}
	src := "倒计时"
	if p.Source == "fixed" {
		src = "固定时刻"
	}
	return p.Name + "（" + src + "）" + time.UnixMilli(p.At).Format("15:04")
}

// takeoutLine 外卖提醒的下一次触发时刻（含「提前通知」——提醒时刻 = 条目时刻 - 提前量）。
// 外卖不进休息调度（窗口独立、永不自动收窗），这里只为设置页状态卡提供倒计时。
// 固定时刻每天循环，查今天与明天两天足够覆盖「下一个还没到的时刻」。
func takeoutLine(s *Settings, now int64) (PlannedBreak, bool) {
	if !s.TakeoutEnabled() {
		return PlannedBreak{}, false
	}
	slots := s.TakeoutSlots()
	if len(slots) == 0 {
		return PlannedBreak{}, false
	}
	leadMs := int64(s.TakeoutLeadMin()) * 60_000
	best := int64(0)
	found := false
	for d := 0; d < 2; d++ {
		day := now + int64(d)*86_400_000
		for _, sl := range slots {
			at := slotAlertAt(sl, leadMs, day)
			if at > now && (!found || at < best) {
				best, found = at, true
			}
		}
	}
	if !found {
		return PlannedBreak{}, false
	}
	return PlannedBreak{Type: "takeout", Name: TAKEOUT_NAME, At: best, Source: "fixed"}, true
}

// planNextBreakSplit 按设置页状态卡的口径把下一跳分成两组：短/长一组（「下次休息提醒」），
// 外卖 + 自定义提醒一组（「下次XX提醒」），两边互不混用。
// 全局 next（planNextBreak）不含外卖 —— 外卖窗口独立于休息调度；分组视图把它算进
// other 只是为了让状态卡能显示「下次外卖提醒还有多久」。
func planNextBreakSplit(s *Settings, now int64, shortsSinceLong int, skip map[string]bool, prev map[string]int64) PlanSplit {
	lines := fixedLines(s, now)
	if cd, ok := builtinCountdownLine(s, now, shortsSinceLong); ok {
		cd.At = countdownAnchor(prev, cd.Type, now, skip, cd.At)
		lines = append(lines, cd)
	}
	for _, c := range s.CustomBreakList() {
		if !c.Config.Enabled || c.Config.Mode != "countdown" {
			continue
		}
		l := PlannedBreak{
			Type: c.ID, Name: c.Name, At: now + c.Config.IntervalMs, Source: "countdown",
		}
		l.At = countdownAnchor(prev, l.Type, now, skip, l.At)
		lines = append(lines, l)
	}
	if tk, ok := takeoutLine(s, now); ok {
		lines = append(lines, tk)
	}
	pool := filterConsumed(lines, skip)
	pick := func(isBuiltin bool) *PlannedBreak {
		xs := make([]PlannedBreak, 0, len(pool))
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
	return PlanSplit{Builtin: pick(true), Other: pick(false)}
}

// fixedNextMap 各「固定时刻线」身份下一次实际触发的时刻表（键 = BreakId 或 'takeout'）。
// 与 planNextBreak 同一口径：已消费的 occurrence 不返回，并顺着时刻表找**再下一次**
// （今天 12:00 被跳过后，摘要应显示明天 12:00，而不是把这条身份整个抹掉）。
// 倒计时模式的身份不出现在表里（摘要里倒计时本就不展示具体时刻）。
func fixedNextMap(s *Settings, now int64, skip map[string]bool) map[string]int64 {
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
func expandBuiltinCountdown(s *Settings, now int64, shortsSinceLong int, skip map[string]bool, first *PlannedBreak) []PlannedBreak {
	if first == nil {
		return []PlannedBreak{}
	}
	cfgOf := func(t string) BreakConfig { return s.Break(t) }
	out := []PlannedBreak{*first}
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
		out = append(out, PlannedBreak{Type: nextType, Name: name, At: at, Source: "countdown"})
	}
	return out
}

// expandCustomCountdown 自定义提醒倒计时链：anchor = 上一跳 at + 自己的 intervalMs，
// 同样剔已消费、今天截断、上限保护。
func expandCustomCountdown(now int64, skip map[string]bool, first PlannedBreak, intervalMs int64) []PlannedBreak {
	out := []PlannedBreak{first}
	curAt := first.At
	for i := 0; i < cdExpandMax && intervalMs > 0; i++ {
		at := curAt + intervalMs
		if !sameLocalDay(at, now) {
			break
		}
		curAt = at
		if skip[occurrenceKey(first.Type, at)] {
			continue
		}
		out = append(out, PlannedBreak{Type: first.Type, Name: first.Name, At: at, Source: "countdown"})
	}
	return out
}

// TimelineOptions todayTimeline 的入参（对应 breakPlan.ts 的 opts）
type TimelineOptions struct {
	Skip             map[string]bool
	Shifts           map[string]int64
	CountdownLines   []PlannedBreak
	Paused           bool
	TakeoutFired     map[string]bool
	TakeoutPostponed *TakeoutPostponed
	ShortsSinceLong  int
}

// todayTimeline 今日提醒时间线（时间规划页顶部的折叠列表）：综合当前全部提醒设置，
// 推算**当天**尚未触发的提醒时刻，按时间排序。
func todayTimeline(s *Settings, now int64, opts TimelineOptions) []TimelineItem {
	out := []TimelineItem{}
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
			out = append(out, TimelineItem{
				Key: occurrenceKey(e.ID, hit.At), Type: e.ID, Name: e.Name, At: hit.At, Source: "fixed",
			})
			from = hit.At + 1
		}
	}
	// ② 外卖：展开当天剩余条目（弹出时刻 = 条目时刻 - 提前量）
	if s.TakeoutEnabled() {
		leadMs := int64(s.TakeoutLeadMin()) * 60_000
		day := time.UnixMilli(now)
		for _, sl := range s.TakeoutSlots() {
			if !sl.Enabled {
				continue
			}
			if sl.Repeat == "weekly" && !intIn(sl.Days, isoWeekday(day)) {
				continue
			}
			at := slotAlertAt(sl, leadMs, now)
			if at <= now {
				continue
			}
			if opts.TakeoutFired[slotFireKey(sl, now, leadMs)] {
				continue
			}
			out = append(out, TimelineItem{
				Key:  "takeout#" + strconv.FormatInt(at, 10) + "#" + sl.ID,
				Type: "takeout", Name: TAKEOUT_NAME, At: at, Source: "fixed",
			})
		}
		if pp := opts.TakeoutPostponed; pp != nil && pp.DueAt > now {
			out = append(out, TimelineItem{
				Key:  "takeout#pp#" + strconv.FormatInt(pp.DueAt, 10),
				Type: "takeout", Name: TAKEOUT_NAME, At: pp.DueAt, Source: "fixed",
			})
		}
	}
	// ③ 倒计时线展开成链：链首 = 挂起那一跳（保留调度器平移/冻结结果），
	// 后续按调度器真实节奏（开始+时长 → 下一跳间隔）推算今天剩余的每一次；暂停时全链冻结。
	paused := opts.Paused
	pushCd := func(p PlannedBreak) {
		flag := paused
		out = append(out, TimelineItem{
			Key:  "cd#" + p.Type + "#" + strconv.FormatInt(p.At, 10),
			Type: p.Type, Name: p.Name, At: p.At, Source: "countdown", Paused: &flag,
		})
	}
	var cds []PlannedBreak
	for _, l := range opts.CountdownLines {
		if l.Source == "countdown" {
			cds = append(cds, l)
		}
	}
	// 内置短/长：最多一条挂起线，展开成链
	var builtinFirst *PlannedBreak
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
		cfg := BreakConfig{}
		if c != nil {
			cfg = breakConfigOf(asMap(c["config"]))
		}
		if c == nil || cfg.Mode != "countdown" {
			pushCd(l)
			continue
		}
		for _, p := range expandCustomCountdown(now, opts.Skip, l, cfg.IntervalMs) {
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
