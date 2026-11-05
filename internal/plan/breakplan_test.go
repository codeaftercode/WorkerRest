package plan

import (
	"encoding/json"
	"testing"
	"time"

	"workerrest/internal/model"
	"workerrest/internal/settings"
)

// plan 包是纯算术层：给定配置与「现在」，算出下一次休息在哪。判据全部是「毫秒时间戳
// 进、毫秒时间戳出」，所以这里不 sleep、不依赖真实时钟，跑起来是毫秒级的。
//
// 基准时刻统一取本地 2026-03-12（周四）10:00 —— 周四是为了让「周重复」的用例同时
// 能覆盖「本周还有」与「要等下周」两种情形。

const (
	dayMs = int64(24 * 60 * 60 * 1000)
)

func at(y int, mo time.Month, d, h, mi int) int64 {
	return time.Date(y, mo, d, h, mi, 0, 0, time.Local).UnixMilli()
}

// base 基准日 2026-03-12 10:00
func base() int64 { return at(2026, 3, 12, 10, 0) }

// cfgOf 用出厂默认 + 覆盖片段造一份配置（数组整体替换、对象递归合并）
func cfgOf(t *testing.T, over string) *settings.Settings {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(over), &m); err != nil {
		t.Fatalf("覆盖片段不是合法 JSON: %v", err)
	}
	merged, ok := settings.DeepMerge(settings.DefaultSettingsMap(), m).(map[string]any)
	if !ok {
		t.Fatal("DeepMerge 没回 map")
	}
	return settings.New(merged, "")
}

// ---------- 时刻与文本 ----------

func TestPad2AndSlotTimeText(t *testing.T) {
	cases := map[int]string{0: "00", 5: "05", 9: "09", 10: "10", 59: "59"}
	for in, want := range cases {
		if got := pad2(in); got != want {
			t.Fatalf("pad2(%d) = %q，期望 %q", in, got, want)
		}
	}
	if got := pad2(-1); got != "-1" {
		t.Fatalf("负数不该补零（异常值原样露出比补成 -01 好排查），得到 %q", got)
	}
	if got := slotTimeText(model.TimeSlot{Hour: 9, Minute: 5}); got != "09:05" {
		t.Fatalf("得到 %q", got)
	}
}

func TestIsoWeekdayMatchesJSGetDay(t *testing.T) {
	cases := []struct {
		d    time.Time
		want int
	}{
		{time.Date(2026, 3, 9, 0, 0, 0, 0, time.Local), 1},  // 周一
		{time.Date(2026, 3, 12, 0, 0, 0, 0, time.Local), 4}, // 周四
		{time.Date(2026, 3, 14, 0, 0, 0, 0, time.Local), 6}, // 周六
		{time.Date(2026, 3, 15, 0, 0, 0, 0, time.Local), 7}, // 周日（JS 的 0 要换成 7）
	}
	for _, c := range cases {
		if got := isoWeekday(c.d); got != c.want {
			t.Fatalf("%s 得到 %d，期望 %d", c.d.Format("2006-01-02"), got, c.want)
		}
	}
}

func TestLocalDateKeyAndSameLocalDay(t *testing.T) {
	if got := localDateKey(time.Date(2026, 3, 5, 23, 59, 0, 0, time.Local)); got != "2026-03-05" {
		t.Fatalf("得到 %q", got)
	}
	if !sameLocalDay(at(2026, 3, 12, 0, 0), at(2026, 3, 12, 23, 59)) {
		t.Fatal("同一天的首尾应算同一天")
	}
	if sameLocalDay(at(2026, 3, 12, 23, 59), at(2026, 3, 13, 0, 0)) {
		t.Fatal("跨零点的相邻两秒不该算同一天")
	}
}

func TestStartOfDay(t *testing.T) {
	d := time.UnixMilli(at(2026, 3, 12, 15, 42))
	sod := startOfDay(d)
	if sod.Hour() != 0 || sod.Minute() != 0 || sod.Second() != 0 || sod.Nanosecond() != 0 {
		t.Fatalf("应回到本地零点，得到 %s", sod)
	}
	if sod.Day() != 12 || sod.Location() != d.Location() {
		t.Fatalf("日期/时区不该变，得到 %s", sod)
	}
}

// 提前量跨天：00:10 的提醒提前 20 分钟 → 前一天 23:50 就要弹；但 occurrence 仍归次日。
func TestSlotAlertAtCrossesMidnight(t *testing.T) {
	slot := model.TimeSlot{ID: "s1", Hour: 0, Minute: 10, Enabled: true, Repeat: "daily"}
	day := at(2026, 3, 12, 12, 0)
	if got, want := slotAlertAt(slot, 20*60_000, day), at(2026, 3, 11, 23, 50); got != want {
		t.Fatalf("提前 20 分钟应落到前一天 23:50，得到 %s", time.UnixMilli(got))
	}
	// occurrence 仍归次日 00:10（不是前一天）
	now := at(2026, 3, 11, 23, 55)
	if got, want := slotOccurrenceAt(slot, 20*60_000, now), at(2026, 3, 12, 0, 10); got != want {
		t.Fatalf("occurrence 应归次日 00:10，得到 %s", time.UnixMilli(got))
	}
}

func TestSlotFireKeyUsesOccurrenceDate(t *testing.T) {
	slot := model.TimeSlot{ID: "s1", Hour: 0, Minute: 10, Enabled: true}
	key := slotFireKey(slot, at(2026, 3, 11, 23, 55), 20*60_000)
	if key != "s1#2026-03-12#00:10" {
		t.Fatalf("去重键应挂次日日期，得到 %q", key)
	}
}

func TestIsTimeSlotDue(t *testing.T) {
	slot := model.TimeSlot{ID: "s", Hour: 12, Minute: 0, Enabled: true, Repeat: "daily"}
	if !isTimeSlotDue(slot, at(2026, 3, 12, 12, 0), 0) {
		t.Fatal("时刻吻合应到期")
	}
	if isTimeSlotDue(slot, at(2026, 3, 12, 12, 1), 0) {
		t.Fatal("差一分钟不该到期")
	}
	// 提前量把「到期时刻」整体前移
	if !isTimeSlotDue(slot, at(2026, 3, 12, 11, 50), 10*60_000) {
		t.Fatal("提前 10 分钟应到期")
	}
	// 关闭的条目不参与
	off := slot
	off.Enabled = false
	if isTimeSlotDue(off, at(2026, 3, 12, 12, 0), 0) {
		t.Fatal("关闭的条目不该到期")
	}
	// 周重复：只认勾选的星期几
	weekly := model.TimeSlot{ID: "w", Hour: 12, Minute: 0, Enabled: true, Repeat: "weekly", Days: []int{4}}
	if !isTimeSlotDue(weekly, at(2026, 3, 12, 12, 0), 0) {
		t.Fatal("周四勾了周四，应到期")
	}
	if isTimeSlotDue(weekly, at(2026, 3, 13, 12, 0), 0) {
		t.Fatal("周五没勾，不该到期")
	}
}

func TestFindDueTimeSlotsDedupesAndPrunes(t *testing.T) {
	slots := []model.TimeSlot{
		{ID: "a", Hour: 12, Minute: 0, Enabled: true, Repeat: "daily"},
		{ID: "b", Hour: 12, Minute: 0, Enabled: false, Repeat: "daily"},
	}
	fired := map[string]bool{"a#2026-03-11#09:00": true} // 昨天的残留键
	now := at(2026, 3, 12, 12, 0)

	first := findDueTimeSlots(slots, now, fired, 0)
	if len(first) != 1 || first[0].ID != "a" {
		t.Fatalf("应只命中启用中的 a，得到 %v", first)
	}
	if fired["a#2026-03-11#09:00"] {
		t.Fatal("昨天的键应被清掉，否则长期运行集合越撑越大")
	}
	// 同一分钟内重复 tick 不该重复弹
	if again := findDueTimeSlots(slots, now+30_000, fired, 0); len(again) != 0 {
		t.Fatalf("同一分钟第二次 tick 不该再返回，得到 %v", again)
	}
	// 第二天同一时刻可以再弹一次
	if next := findDueTimeSlots(slots, now+dayMs, fired, 0); len(next) != 1 {
		t.Fatalf("第二天应重新命中，得到 %v", next)
	}
}

func TestDescribeTimeSlot(t *testing.T) {
	daily := model.TimeSlot{Hour: 9, Minute: 5, Repeat: "daily", Enabled: true}
	if got := describeTimeSlot(daily); got != "每天 09:05" {
		t.Fatalf("得到 %q", got)
	}
	weekly := model.TimeSlot{Hour: 9, Minute: 30, Repeat: "weekly", Days: []int{3, 1}, Enabled: true}
	if got := describeTimeSlot(weekly); got != "每周 一、三 09:30" {
		t.Fatalf("应按周一到周日的顺序列出（与勾选顺序无关），得到 %q", got)
	}
	none := model.TimeSlot{Hour: 9, Minute: 30, Repeat: "weekly", Days: nil, Enabled: true}
	if got := describeTimeSlot(none); got != "每周 （未选） 09:30" {
		t.Fatalf("一个都没勾时要说清楚，得到 %q", got)
	}
}

func TestTakeoutMealLabelBoundaries(t *testing.T) {
	cases := map[int]string{
		0: "夜宵", 4: "夜宵", 5: "早餐", 9: "早餐",
		10: "午餐", 13: "午餐", 14: "下午茶", 16: "下午茶",
		17: "晚餐", 20: "晚餐", 21: "夜宵", 23: "夜宵",
	}
	for h, want := range cases {
		if got := takeoutMealLabel(h); got != want {
			t.Fatalf("%d 点应为 %q，得到 %q", h, want, got)
		}
	}
}

func TestPreBreakTexts(t *testing.T) {
	if got := preBreakTitle("喝水"); got != "喝水时间到" {
		t.Fatalf("得到 %q", got)
	}
	// 名称本身以「时间」结尾时不能叠成「喝水时间时间到」
	if got := preBreakTitle("喝水时间"); got != "喝水时间到" {
		t.Fatalf("得到 %q", got)
	}
	if got := preBreakCountdownTail("喝水"); got != " 秒显示喝水提醒" {
		t.Fatalf("尾部要带前导空格（接在数字后面），得到 %q", got)
	}
	if got := preBreakCountdownTail("喝水提醒"); got != " 秒显示喝水提醒" {
		t.Fatalf("名称已带「提醒」时不该补第二次，得到 %q", got)
	}
}

func TestOccurrenceKeyRoundTrip(t *testing.T) {
	if got := occurrenceKey("short", 1234567); got != "short#1234567" {
		t.Fatalf("得到 %q", got)
	}
	if got := occurrenceKeyAt("short#1234567"); got != 1234567 {
		t.Fatalf("得到 %d", got)
	}
	// 拆不出来一律 0（与 JS 的 Number('')===0 同义），调用方按「不是有效键」处理
	for _, k := range []string{"", "short", "short#abc", "short#"} {
		if got := occurrenceKeyAt(k); got != 0 {
			t.Fatalf("occurrenceKeyAt(%q) 应为 0，得到 %d", k, got)
		}
	}
	// 只看最后一个 # 之后：类型里带 # 也不影响（类型是自由字符串）
	if got := occurrenceKeyAt("weird#type#99"); got != 99 {
		t.Fatalf("应按最后一个 # 切分，得到 %d", got)
	}
}

// ---------- 下一次休息 ----------

func TestNextSlotAt(t *testing.T) {
	daily := []model.TimeSlot{{ID: "d", Hour: 12, Minute: 0, Enabled: true, Repeat: "daily"}}
	hit, ok := nextSlotAt(daily, base())
	if !ok || hit.At != at(2026, 3, 12, 12, 0) {
		t.Fatalf("10:00 时下一次应是今天 12:00，得到 %s ok=%v", time.UnixMilli(hit.At), ok)
	}
	// 严格晚于：正好 12:00 时应该给明天
	hit, _ = nextSlotAt(daily, at(2026, 3, 12, 12, 0))
	if hit.At != at(2026, 3, 13, 12, 0) {
		t.Fatalf("正好到点时应给下一次（明天），得到 %s", time.UnixMilli(hit.At))
	}
	// 关掉的条目不参与
	off := []model.TimeSlot{{ID: "d", Hour: 12, Minute: 0, Enabled: false}}
	if _, ok := nextSlotAt(off, base()); ok {
		t.Fatal("关掉的条目不该给出下一次")
	}
	// 周重复：周四 10:00 时，勾周四 → 今天 12:00；勾周一 → 下周一
	thu := []model.TimeSlot{{ID: "w", Hour: 12, Minute: 0, Enabled: true, Repeat: "weekly", Days: []int{4}}}
	if hit, _ := nextSlotAt(thu, base()); hit.At != at(2026, 3, 12, 12, 0) {
		t.Fatalf("勾了周四应给今天 12:00，得到 %s", time.UnixMilli(hit.At))
	}
	mon := []model.TimeSlot{{ID: "w", Hour: 12, Minute: 0, Enabled: true, Repeat: "weekly", Days: []int{1}}}
	if hit, _ := nextSlotAt(mon, base()); hit.At != at(2026, 3, 16, 12, 0) {
		t.Fatalf("勾了周一应给下周一，得到 %s", time.UnixMilli(hit.At))
	}
	// 多条时取最近的
	multi := []model.TimeSlot{
		{ID: "late", Hour: 18, Minute: 0, Enabled: true, Repeat: "daily"},
		{ID: "soon", Hour: 11, Minute: 0, Enabled: true, Repeat: "daily"},
	}
	if hit, _ := nextSlotAt(multi, base()); hit.Slot.ID != "soon" {
		t.Fatalf("应取最近的一条，得到 %q", hit.Slot.ID)
	}
}

func TestBuiltinCountdownLine(t *testing.T) {
	// 出厂：短 10 分钟倒计时、长 20 分钟倒计时、每 2 次短休息换长
	s := cfgOf(t, `{}`)
	cd, ok := builtinCountdownLine(s, base(), 0)
	if !ok || cd.Type != "short" || cd.At != base()+600_000 {
		t.Fatalf("默认应给短休息 +10 分钟，得到 %+v ok=%v", cd, ok)
	}
	if cd.Source != "countdown" || cd.Name != ShortBreakName {
		t.Fatalf("来源/名称不符：%+v", cd)
	}
	// 攒够次数 → 长休息
	if cd, _ := builtinCountdownLine(s, base(), 2); cd.Type != "long" || cd.At != base()+1_200_000 {
		t.Fatalf("攒够 2 次短休息后应给长休息，得到 %+v", cd)
	}
	// 只有长休息是倒计时
	onlyLong := cfgOf(t, `{"short":{"enabled":false}}`)
	if cd, ok := builtinCountdownLine(onlyLong, base(), 0); !ok || cd.Type != "long" {
		t.Fatalf("短休息关闭时应给长休息，得到 %+v ok=%v", cd, ok)
	}
	// 只有短休息是倒计时（长改成固定时刻）
	fixedLong := cfgOf(t, `{"long":{"mode":"fixed"}}`)
	if cd, ok := builtinCountdownLine(fixedLong, base(), 99); !ok || cd.Type != "short" {
		t.Fatalf("长休息不是倒计时时，这条线只可能是短休息，得到 %+v ok=%v", cd, ok)
	}
	// 两条都关掉
	none := cfgOf(t, `{"short":{"enabled":false},"long":{"enabled":false}}`)
	if _, ok := builtinCountdownLine(none, base(), 0); ok {
		t.Fatal("两条都关掉时不该有倒计时线")
	}
}

func TestFixedLines(t *testing.T) {
	s := cfgOf(t, `{
		"short":{"mode":"fixed","slots":[{"id":"s1","hour":12,"minute":0,"repeat":"daily","days":[],"enabled":true}]}
	}`)
	lines := fixedLines(s, base())
	if len(lines) != 1 {
		t.Fatalf("应只有短休息一条固定线，得到 %+v", lines)
	}
	if lines[0].Type != "short" || lines[0].Source != "fixed" || lines[0].Slot == nil {
		t.Fatalf("形状不符：%+v", lines[0])
	}
	if lines[0].At != at(2026, 3, 12, 12, 0) {
		t.Fatalf("时刻不符：%s", time.UnixMilli(lines[0].At))
	}
	// 固定模式但没有条目 → 没有线
	empty := cfgOf(t, `{"short":{"mode":"fixed","slots":[]}}`)
	if got := fixedLines(empty, base()); len(got) != 0 {
		t.Fatalf("没有时刻条目时不该有线，得到 %+v", got)
	}
}

func TestSortPlansPutsLongFirstOnTie(t *testing.T) {
	tie := base()
	pool := []model.PlannedBreak{
		{Type: "custom", Name: "喝水", At: tie},
		{Type: "short", Name: ShortBreakName, At: tie},
		{Type: "long", Name: LongBreakName, At: tie},
		{Type: "later", Name: "晚", At: tie + 1000},
	}
	sortPlans(pool)
	if pool[0].Type != "long" {
		t.Fatalf("撞在同一时刻时长休息应优先（更长的那次覆盖短的），得到 %q", pool[0].Type)
	}
	if pool[len(pool)-1].At != tie+1000 {
		t.Fatalf("整体仍要按时刻升序，得到 %+v", pool)
	}
}

func TestFilterConsumed(t *testing.T) {
	pool := []model.PlannedBreak{
		{Type: "short", At: 100},
		{Type: "short", At: 200},
	}
	if got := filterConsumed(pool, nil); len(got) != 2 {
		t.Fatalf("空跳过表应原样返回，得到 %+v", got)
	}
	skip := map[string]bool{occurrenceKey("short", 200): true}
	got := filterConsumed(pool, skip)
	if len(got) != 1 || got[0].At != 100 {
		t.Fatalf("已消费的那跳应被剔除，得到 %+v", got)
	}
}

func TestCountdownAnchor(t *testing.T) {
	now := base()
	skip := map[string]bool{}
	prev := map[string]cdAnchor{"short": {At: now + 300_000, Iv: 600_000}}

	if got := countdownAnchor(prev, "short", now, skip, now+600_000, 600_000); got != now+300_000 {
		t.Fatalf("未到期且间隔未变应沿用旧锚点（保存设置不该清零已流逝的倒计时），得到 %s", time.UnixMilli(got))
	}
	// 间隔改了 → 立刻按新间隔重算
	if got := countdownAnchor(prev, "short", now, skip, now+900_000, 900_000); got != now+900_000 {
		t.Fatalf("间隔变了应重算，得到 %s", time.UnixMilli(got))
	}
	// 已到期 → 重算（这一跳已经过去，不能沿用）
	expired := map[string]cdAnchor{"short": {At: now - 1, Iv: 600_000}}
	if got := countdownAnchor(expired, "short", now, skip, now+600_000, 600_000); got != now+600_000 {
		t.Fatalf("已到期应重算，得到 %s", time.UnixMilli(got))
	}
	// 冻结锚点即使早于墙钟也照样沿用（暂停期间时间不流逝）
	frozen := map[string]cdAnchor{"short": {At: now - 1, Iv: 600_000, Frozen: true}}
	if got := countdownAnchor(frozen, "short", now, skip, now+600_000, 600_000); got != now-1 {
		t.Fatalf("冻结锚点应原样沿用，得到 %s", time.UnixMilli(got))
	}
	// 已被消费 → 重算
	consumed := map[string]cdAnchor{"short": {At: now + 300_000, Iv: 600_000}}
	if got := countdownAnchor(consumed, "short", now, map[string]bool{occurrenceKey("short", now+300_000): true}, now+600_000, 600_000); got != now+600_000 {
		t.Fatalf("已消费应重算，得到 %s", time.UnixMilli(got))
	}
	// 没有旧锚点 → 重算
	if got := countdownAnchor(nil, "short", now, skip, now+600_000, 600_000); got != now+600_000 {
		t.Fatalf("无旧锚点应重算，得到 %s", time.UnixMilli(got))
	}
}

func TestPlanNextBreakPicksEarliestAcrossSources(t *testing.T) {
	s := cfgOf(t, `{
		"short":{"mode":"fixed","slots":[{"id":"s1","hour":12,"minute":0,"repeat":"daily","days":[],"enabled":true}]},
		"long":{"enabled":false}
	}`)
	// 10:50：固定线 12:00 更近，还是倒计时…… 短休息已经是固定模式，所以只剩固定线
	if p, ok := planNextBreak(s, at(2026, 3, 12, 10, 50), 0, nil, nil); !ok || p.At != at(2026, 3, 12, 12, 0) {
		t.Fatalf("得到 %+v ok=%v", p, ok)
	}
	// 短休息改回倒计时（+10 分钟）后，倒计时比 12:00 更早
	s2 := cfgOf(t, `{
		"short":{"mode":"countdown","intervalMs":600000},
		"long":{"enabled":false}
	}`)
	if p, _ := planNextBreak(s2, at(2026, 3, 12, 10, 50), 0, nil, nil); p.Type != "short" || p.At != at(2026, 3, 12, 11, 0) {
		t.Fatalf("应取倒计时那一条，得到 %+v", p)
	}
}

func TestPlanNextBreakEmptyWhenAllDisabled(t *testing.T) {
	s := cfgOf(t, `{"short":{"enabled":false},"long":{"enabled":false}}`)
	if _, ok := planNextBreak(s, base(), 0, nil, nil); ok {
		t.Fatal("两条都关掉时不该有下一次")
	}
}

func TestDescribePlan(t *testing.T) {
	if got := describePlan(nil); got != "未安排休息" {
		t.Fatalf("得到 %q", got)
	}
	cd := model.PlannedBreak{Name: ShortBreakName, Source: "countdown", At: at(2026, 3, 12, 10, 10)}
	if got := describePlan(&cd); got != "短休息（倒计时）10:10" {
		t.Fatalf("得到 %q", got)
	}
	fx := model.PlannedBreak{Name: "喝水", Source: "fixed", At: at(2026, 3, 12, 12, 5)}
	if got := describePlan(&fx); got != "喝水（固定时刻）12:05" {
		t.Fatalf("得到 %q", got)
	}
}

func TestTakeoutLine(t *testing.T) {
	// 出厂：11:30 一条
	s := cfgOf(t, `{}`)
	p, ok := takeoutLine(s, base())
	if !ok || p.At != at(2026, 3, 12, 11, 30) || p.Type != "takeout" || p.Name != TAKEOUT_NAME {
		t.Fatalf("得到 %+v ok=%v", p, ok)
	}
	// 过了 11:30 → 明天的 11:30（固定时刻每天循环）
	if p, _ := takeoutLine(s, at(2026, 3, 12, 12, 0)); p.At != at(2026, 3, 13, 11, 30) {
		t.Fatalf("应给明天，得到 %s", time.UnixMilli(p.At))
	}
	// 关闭 / 没有条目
	if _, ok := takeoutLine(cfgOf(t, `{"takeout":{"enabled":false}}`), base()); ok {
		t.Fatal("外卖关闭时不该有下一次")
	}
	if _, ok := takeoutLine(cfgOf(t, `{"takeout":{"slots":[]}}`), base()); ok {
		t.Fatal("没有时刻条目时不该有下一次")
	}
}

func TestPlanNextBreakSplitSeparatesTakeoutFromBuiltin(t *testing.T) {
	// 短休息倒计时 +10 分钟（10:10），外卖 11:30 → builtin 是短休息，other 是外卖
	s := cfgOf(t, `{}`)
	split := planNextBreakSplit(s, base(), 0, nil, nil)
	if split.Builtin == nil || split.Builtin.Type != "short" {
		t.Fatalf("内置一组应是短休息，得到 %+v", split.Builtin)
	}
	if split.Other == nil || split.Other.Type != "takeout" {
		t.Fatalf("另一组应是外卖（外卖不进全局 next，但状态卡要显示它），得到 %+v", split.Other)
	}
	// 自定义提醒比外卖早 → 另一组变成自定义
	s2 := cfgOf(t, `{"customBreaks":[{"id":"c1","name":"喝水","config":{"enabled":true,"mode":"countdown","intervalMs":300000,"durationMs":20000}}]}`)
	split = planNextBreakSplit(s2, base(), 0, nil, nil)
	if split.Other == nil || split.Other.Type != "c1" {
		t.Fatalf("另一组应是更早的自定义提醒，得到 %+v", split.Other)
	}
}

func TestFixedNextMapSkipsConsumedOccurrence(t *testing.T) {
	s := cfgOf(t, `{
		"short":{"mode":"fixed","slots":[{"id":"s1","hour":12,"minute":0,"repeat":"daily","days":[],"enabled":true}]},
		"long":{"enabled":false}
	}`)
	now := base()
	got := fixedNextMap(s, now, nil)
	if got["short"] != at(2026, 3, 12, 12, 0) {
		t.Fatalf("得到 %s", time.UnixMilli(got["short"]))
	}
	// 今天这一跳被跳过 → 摘要应显示明天，而不是把这条身份整个抹掉
	skip := map[string]bool{occurrenceKey("short", at(2026, 3, 12, 12, 0)): true}
	got = fixedNextMap(s, now, skip)
	if got["short"] != at(2026, 3, 13, 12, 0) {
		t.Fatalf("被跳过后应给下一次，得到 %s", time.UnixMilli(got["short"]))
	}
	// 外卖也进这张表（键 takeout）
	if v, ok := got["takeout"]; !ok || v != at(2026, 3, 12, 11, 30) {
		t.Fatalf("外卖应进表，得到 %v ok=%v", v, ok)
	}
}

// ---------- 今日时间线 ----------

func TestExpandBuiltinCountdownChain(t *testing.T) {
	s := cfgOf(t, `{
		"short":{"enabled":true,"mode":"countdown","intervalMs":600000,"durationMs":60000},
		"long":{"enabled":true,"mode":"countdown","intervalMs":1200000,"durationMs":300000},
		"longEveryNShort":2
	}`)
	now := base()
	first := model.PlannedBreak{Type: "short", Name: ShortBreakName, At: now, Source: "countdown"}
	chain := expandBuiltinCountdown(s, now, 0, nil, &first)
	if len(chain) < 4 {
		t.Fatalf("今天应该还能排出好几跳，得到 %d 项", len(chain))
	}
	// 锚点 = 上一跳开始 + 上一跳时长 + 下一跳间隔
	if chain[1].At != now+60_000+600_000 {
		t.Fatalf("第二跳应 = 上一跳开始 + 时长 + 间隔，得到 %s", time.UnixMilli(chain[1].At))
	}
	for i := 2; i < len(chain); i++ {
		if chain[i].At <= chain[i-1].At {
			t.Fatalf("链必须严格递增，第 %d 项倒退", i)
		}
		if !sameLocalDay(chain[i].At, now) {
			t.Fatalf("今天的链不该跨到第二天，第 %d 项是 %s", i, time.UnixMilli(chain[i].At))
		}
	}
	// 每 2 次短休息之后应出现长休息
	sawLong := false
	for _, p := range chain {
		if p.Type == "long" {
			sawLong = true
			break
		}
	}
	if !sawLong {
		t.Fatalf("应出现长休息，得到 %+v", chain)
	}
	// nil 首项 → 空链
	if got := expandBuiltinCountdown(s, now, 0, nil, nil); len(got) != 0 {
		t.Fatalf("没有挂起线时应给空，得到 %+v", got)
	}
}

func TestExpandBuiltinCountdownSkipsButStillAdvances(t *testing.T) {
	s := cfgOf(t, `{
		"short":{"enabled":true,"mode":"countdown","intervalMs":600000,"durationMs":60000},
		"long":{"enabled":false}
	}`)
	now := base()
	first := model.PlannedBreak{Type: "short", Name: ShortBreakName, At: now, Source: "countdown"}
	second := now + 60_000 + 600_000
	skip := map[string]bool{occurrenceKey("short", second): true}
	chain := expandBuiltinCountdown(s, now, 0, skip, &first)
	for _, p := range chain {
		if p.At == second {
			t.Fatal("已消费的那一跳不该展示")
		}
	}
	if len(chain) < 2 {
		t.Fatalf("跳过一跳不该让整条链停住，得到 %d 项", len(chain))
	}
	if chain[1].At != second+60_000+600_000 {
		t.Fatalf("链应继续按节奏推进，得到 %s", time.UnixMilli(chain[1].At))
	}
}

func TestExpandBuiltinCountdownDegenerateStops(t *testing.T) {
	// 时长为 0 的退化配置：链条必须收住，不能死循环
	s := cfgOf(t, `{"short":{"enabled":true,"mode":"countdown","intervalMs":600000,"durationMs":0},"long":{"enabled":false}}`)
	now := base()
	first := model.PlannedBreak{Type: "short", At: now, Source: "countdown"}
	if got := expandBuiltinCountdown(s, now, 0, nil, &first); len(got) != 1 {
		t.Fatalf("退化配置应只留挂起那一跳，得到 %d 项", len(got))
	}
}

func TestExpandCustomCountdown(t *testing.T) {
	now := base()
	first := model.PlannedBreak{Type: "c1", Name: "喝水", At: now + 300_000, Source: "countdown"}
	chain := expandCustomCountdown(now, nil, first, 1_800_000, 20_000)
	if len(chain) < 2 {
		t.Fatalf("应展开成链，得到 %d 项", len(chain))
	}
	if chain[1].At != first.At+20_000+1_800_000 {
		t.Fatalf("步长应为「时长 + 间隔」，得到 %s", time.UnixMilli(chain[1].At))
	}
	for _, p := range chain {
		if !sameLocalDay(p.At, now) {
			t.Fatalf("不该跨天，得到 %s", time.UnixMilli(p.At))
		}
	}
	// 退化：间隔与时长都 0 → 只留首项
	if got := expandCustomCountdown(now, nil, first, 0, 0); len(got) != 1 {
		t.Fatalf("退化配置应只留首项，得到 %d 项", len(got))
	}
}

func TestTodayTimelineFixedAndSkip(t *testing.T) {
	s := cfgOf(t, `{
		"short":{"mode":"fixed","slots":[{"id":"s1","hour":12,"minute":0,"repeat":"daily","days":[],"enabled":true},{"id":"s2","hour":15,"minute":0,"repeat":"daily","days":[],"enabled":true}]},
		"long":{"enabled":false},
		"takeout":{"enabled":false}
	}`)
	now := base()
	items := todayTimeline(s, now, TimelineOptions{})
	if len(items) != 2 {
		t.Fatalf("今天还剩 12:00 与 15:00 两条，得到 %+v", items)
	}
	if items[0].At != at(2026, 3, 12, 12, 0) || items[0].Source != "fixed" || items[0].Type != "short" {
		t.Fatalf("第一项不符：%+v", items[0])
	}
	// 跳过 12:00 → 只剩 15:00（跳过的那条不再出现）
	skip := map[string]bool{occurrenceKey("short", at(2026, 3, 12, 12, 0)): true}
	items = todayTimeline(s, now, TimelineOptions{Skip: skip})
	if len(items) != 1 || items[0].At != at(2026, 3, 12, 15, 0) {
		t.Fatalf("跳过 12:00 后应只剩 15:00，得到 %+v", items)
	}
}

func TestTodayTimelineShiftReplacesOriginal(t *testing.T) {
	s := cfgOf(t, `{
		"short":{"mode":"fixed","slots":[{"id":"s1","hour":12,"minute":0,"repeat":"daily","days":[],"enabled":true}]},
		"long":{"enabled":false},
		"takeout":{"enabled":false}
	}`)
	now := base()
	shiftAt := at(2026, 3, 12, 12, 2) // 推迟 2 分钟
	items := todayTimeline(s, now, TimelineOptions{Shifts: map[string]int64{"short": shiftAt}})
	if len(items) != 1 {
		t.Fatalf("推迟后应仍是一条，得到 %+v", items)
	}
	if items[0].At != shiftAt {
		t.Fatalf("应显示推迟后的时刻，得到 %s", time.UnixMilli(items[0].At))
	}
}

func TestTodayTimelineTakeoutAndPostponed(t *testing.T) {
	s := cfgOf(t, `{"short":{"enabled":false},"long":{"enabled":false}}`)
	now := base()
	items := todayTimeline(s, now, TimelineOptions{})
	if len(items) != 1 || items[0].Type != "takeout" || items[0].At != at(2026, 3, 12, 11, 30) {
		t.Fatalf("应只有外卖一条，得到 %+v", items)
	}
	// 已触发的不再展示
	fired := map[string]bool{occurrenceKey("takeout", at(2026, 3, 12, 11, 30)): true}
	if got := todayTimeline(s, now, TimelineOptions{TakeoutFired: fired}); len(got) != 0 {
		t.Fatalf("已触发的外卖不该再列出，得到 %+v", got)
	}
	// 推迟线要单独补一条
	pp := &model.TakeoutPostponed{DueAt: at(2026, 3, 12, 13, 0), Count: 1}
	got := todayTimeline(s, now, TimelineOptions{TakeoutPostponed: pp})
	found := false
	for _, it := range got {
		if it.At == pp.DueAt {
			found = true
		}
	}
	if !found {
		t.Fatalf("推迟后的外卖时刻应出现在时间线里，得到 %+v", got)
	}
	// 已经过期的推迟线不展示
	past := &model.TakeoutPostponed{DueAt: now - 1, Count: 1}
	for _, it := range todayTimeline(s, now, TimelineOptions{TakeoutPostponed: past}) {
		if it.At == past.DueAt {
			t.Fatal("已过期的推迟线不该出现")
		}
	}
}

func TestTodayTimelineCountdownChainAndPausedFlag(t *testing.T) {
	s := cfgOf(t, `{
		"short":{"enabled":true,"mode":"countdown","intervalMs":600000,"durationMs":60000},
		"long":{"enabled":false},
		"takeout":{"enabled":false}
	}`)
	now := base()
	line := model.PlannedBreak{Type: "short", Name: ShortBreakName, At: now + 600_000, Source: "countdown"}
	items := todayTimeline(s, now, TimelineOptions{CountdownLines: []model.PlannedBreak{line}})
	if len(items) < 2 {
		t.Fatalf("倒计时应展开成链，得到 %+v", items)
	}
	for _, it := range items {
		if it.Source != "countdown" || it.Paused == nil {
			t.Fatalf("倒计时项应带 source 与 paused 标记，得到 %+v", it)
		}
		if *it.Paused {
			t.Fatal("没暂停时 paused 应为 false")
		}
	}
	// 暂停中：全链标记冻结
	pausedItems := todayTimeline(s, now, TimelineOptions{CountdownLines: []model.PlannedBreak{line}, Paused: true})
	for _, it := range pausedItems {
		if it.Paused == nil || !*it.Paused {
			t.Fatalf("暂停中应标记冻结，得到 %+v", it)
		}
	}
	// 链首保留调度器平移后的时刻（不是重新按 now + interval 算）
	if pausedItems[0].At != line.At {
		t.Fatalf("链首应保留挂起那一跳的时刻，得到 %s", time.UnixMilli(pausedItems[0].At))
	}
	// 整体按时刻升序
	for i := 1; i < len(items); i++ {
		if items[i].At < items[i-1].At {
			t.Fatalf("时间线必须升序，第 %d 项倒退", i)
		}
	}
}

func TestTodayTimelineCustomBreakCountdown(t *testing.T) {
	s := cfgOf(t, `{
		"short":{"enabled":false},"long":{"enabled":false},
		"takeout":{"enabled":false},
		"customBreaks":[{"id":"c1","name":"喝水","config":{"enabled":true,"mode":"countdown","intervalMs":1800000,"durationMs":20000}}]
	}`)
	now := base()
	line := model.PlannedBreak{Type: "c1", Name: "喝水", At: now + 1_800_000, Source: "countdown"}
	items := todayTimeline(s, now, TimelineOptions{CountdownLines: []model.PlannedBreak{line}})
	if len(items) < 2 {
		t.Fatalf("自定义提醒的倒计时也应展开，得到 %+v", items)
	}
	for _, it := range items {
		if it.Type != "c1" || it.Name != "喝水" {
			t.Fatalf("类型/名称不符：%+v", it)
		}
	}
	// 配置里已删掉的残留言：退化为只显示挂起一跳，不能崩
	gone := model.PlannedBreak{Type: "deleted", Name: "旧提醒", At: now + 60_000, Source: "countdown"}
	items = todayTimeline(s, now, TimelineOptions{CountdownLines: []model.PlannedBreak{gone}})
	if len(items) != 1 || items[0].Type != "deleted" {
		t.Fatalf("已删除的提醒应只显示挂起一跳，得到 %+v", items)
	}
}

func TestIntInAndFmtClock(t *testing.T) {
	if !intIn([]int{1, 3, 5}, 3) || intIn([]int{1, 3, 5}, 2) || intIn(nil, 1) {
		t.Fatal("intIn 判据不符")
	}
	if got := fmtClock(0); got != "0" {
		t.Fatalf("0 是「没有时刻」的哨兵，应原样显示，得到 %q", got)
	}
	if got := fmtClock(at(2026, 3, 12, 9, 5)); got != "09:05" {
		t.Fatalf("得到 %q", got)
	}
}

// ---------- 应用暂停匹配 ----------

func TestMatchAppPause(t *testing.T) {
	cfg := model.AppPauseConfig{
		Enabled: true,
		Items: []model.AppPauseItem{
			{Exe: "Teams.exe", Enabled: true},
			{Exe: "Zoom.exe", Enabled: false}, // 关掉的条目不该命中
		},
	}
	hit, matched := matchAppPause(cfg, []string{"teams.exe", "chrome.exe"})
	if !hit || len(matched) != 1 || matched[0] != "Teams.exe" {
		t.Fatalf("进程名应大小写不敏感，得到 hit=%v matched=%v", hit, matched)
	}
	if hit, _ := matchAppPause(cfg, []string{"chrome.exe"}); hit {
		t.Fatal("没命中不该报命中")
	}
	if hit, _ := matchAppPause(cfg, nil); hit {
		t.Fatal("没有进程时不该命中")
	}
	off := cfg
	off.Enabled = false
	if hit, _ := matchAppPause(off, []string{"teams.exe"}); hit {
		t.Fatal("总开关关掉后不该命中")
	}
	if hit, _ := matchAppPause(model.AppPauseConfig{Enabled: true}, []string{"teams.exe"}); hit {
		t.Fatal("没有条目时不该命中")
	}
	// 多个同时命中要全部报出来（日志与提示要知道是谁）
	cfg.Items = append(cfg.Items, model.AppPauseItem{Exe: "zoom.exe", Enabled: true})
	_, matched = matchAppPause(cfg, []string{"teams.exe", "zoom.exe"})
	if len(matched) != 2 {
		t.Fatalf("两个都该命中，得到 %v", matched)
	}
}
