package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// 统计层的回归（对应 历史源码/stats.ts）。
//
// 这一层最容易错的三处，都在这里钉死：
//   - **跨零点切段**（splitByDay）：工作 / 休息 / 离开的每一段都按本地零点切成多段，
//     各归各天。写错了不会报错，只会让账目悄悄记到隔壁那天；
//   - **离开的三重封顶**（BeginAway）：不越过工作段起点、按当天 workMs 封顶、不跨天；
//   - **ISO 周号与季度起始月**（periodRangeOf）：日期算术的经典雷区。
//
// 真实性的兜底是 scripts/verify_stats_parity.py —— 拿 上一版的 stats.ts 当黄金参考
// 对同一组输入逐字段对拍。这里只负责把规则本身钉死。

// statsClock 可推进的假时钟（毫秒墙钟）
type statsClock struct{ ms int64 }

func (c *statsClock) now() int64          { return c.ms }
func (c *statsClock) set(t time.Time)     { c.ms = t.UnixMilli() }
func (c *statsClock) add(d time.Duration) { c.ms += int64(d / time.Millisecond) }
func localTime(y int, mo time.Month, d, h, mi int) time.Time {
	return time.Date(y, mo, d, h, mi, 0, 0, time.Local)
}

// newTestStats 建一个落点在临时目录、时钟可推进的统计存储（绝不碰用户真实文件）
func newTestStats(t *testing.T, c *statsClock) (*StatsStore, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stats.json")
	st := NewStatsStore(func() string { return path })
	st.nowFn = c.now
	// 与 StartWorkAccumulator 首行同口径：工作段起点以「注入的时钟」为准。
	// NewStatsStore 里用的是真实系统时钟，不覆盖的话 workSegStartAt 会落在未来，
	// BeginAway 结算连续工作段时会一路算到零点（测试里是真的踩过）。
	st.workSegStartAt = c.now()
	return st, path
}

// dayForTest 读某一天的记录（不存在时给全 0 条目）
func (st *StatsStore) dayForTest(date string) DayStats {
	st.mu.Lock()
	defer st.mu.Unlock()
	if d := st.days[date]; d != nil {
		return d.clone()
	}
	return emptyDay(date)
}

// ---------- 跨零点切段 ----------

func TestSplitByDay(t *testing.T) {
	base := localTime(2026, 10, 8, 10, 0)
	// 同一天内的一段：只有一段
	segs := splitByDay(base.UnixMilli(), base.Add(90*time.Second).UnixMilli())
	if len(segs) != 1 || segs[0].date != "2026-10-08" || segs[0].ms != 90_000 {
		t.Fatalf("同一天内应切出 1 段 90s，得到 %+v", segs)
	}
	// 跨零点：23:58 → 次日 00:03 切成「前一天 2 分钟 + 当天 3 分钟」
	from := localTime(2026, 10, 8, 23, 58)
	to := localTime(2026, 10, 9, 0, 3)
	segs = splitByDay(from.UnixMilli(), to.UnixMilli())
	if len(segs) != 2 {
		t.Fatalf("跨零点应切出 2 段，得到 %+v", segs)
	}
	if segs[0].date != "2026-10-08" || segs[0].ms != 2*60_000 {
		t.Fatalf("前一段应是 10-08 的 2 分钟，得到 %+v", segs[0])
	}
	if segs[1].date != "2026-10-09" || segs[1].ms != 3*60_000 {
		t.Fatalf("后一段应是 10-09 的 3 分钟，得到 %+v", segs[1])
	}
	// 防御：逆序 / 空区间都不产出
	if n := len(splitByDay(to.UnixMilli(), from.UnixMilli())); n != 0 {
		t.Fatalf("逆序区间不该产出，得到 %d 段", n)
	}
	if n := len(splitByDay(from.UnixMilli(), from.UnixMilli())); n != 0 {
		t.Fatalf("空区间不该产出，得到 %d 段", n)
	}
}

func TestDayKeyAndDayStart(t *testing.T) {
	ts := localTime(2026, 10, 8, 23, 59).UnixMilli()
	if got := dayKeyOf(ts); got != "2026-10-08" {
		t.Fatalf("dayKeyOf = %q，应为 2026-10-08", got)
	}
	want := localTime(2026, 10, 8, 0, 0).UnixMilli()
	if got := dayStartTs(ts); got != want {
		t.Fatalf("dayStartTs = %d，应为当日本地零点 %d", got, want)
	}
}

// ---------- ISO 周号 / 周期区间 ----------

func TestISOWeekNumber(t *testing.T) {
	cases := []struct {
		day  time.Time
		want int
	}{
		{localTime(2026, 1, 1, 12, 0), 1},    // 周四，属第 1 周
		{localTime(2026, 1, 5, 12, 0), 2},    // 下一个周一
		{localTime(2025, 12, 29, 12, 0), 1},  // 周一，其周四落在 2026-01-01 → 2026 年第 1 周
		{localTime(2026, 12, 31, 12, 0), 53}, // 2026 是 53 周年
	}
	for _, c := range cases {
		if got := isoWeekNumber(c.day); got != c.want {
			t.Errorf("%s 的 ISO 周号 = %d，应为 %d", c.day.Format("2006-01-02"), got, c.want)
		}
	}
}

func TestPeriodRangeDay(t *testing.T) {
	now := localTime(2026, 10, 8, 15, 0)
	r := periodRangeOf("day", 0, now)
	if r.Key != "2026-10-08" || r.Start != "2026-10-08" || r.End != "2026-10-08" {
		t.Fatalf("当天区间不对：%+v", r)
	}
	if prev := periodRangeOf("day", -1, now); prev.Key != "2026-10-07" {
		t.Fatalf("前一天 = %q，应为 2026-10-07", prev.Key)
	}
}

// TestPeriodRangeWeekKeyUsesMondayYear 周键的年份用的是**周一那天**的年份 ——
// 跨年那周（周一在去年）键就是去年。这是 stats.ts 的实现细节，必须照抄。
func TestPeriodRangeWeekKeyUsesMondayYear(t *testing.T) {
	now := localTime(2026, 1, 1, 9, 0) // 周四；本周周一 = 2025-12-29
	r := periodRangeOf("week", 0, now)
	if r.Start != "2025-12-29" || r.End != "2026-01-04" {
		t.Fatalf("本周区间应为 2025-12-29 ~ 2026-01-04，得到 %+v", r)
	}
	if r.Key != "2025-W01" {
		t.Fatalf("周键应为 2025-W01（年份取周一那年），得到 %q", r.Key)
	}
	if r.Label != "第 1 周" {
		t.Fatalf("周标签应为「第 1 周」，得到 %q", r.Label)
	}
}

func TestPeriodRangeWeekOffset(t *testing.T) {
	now := localTime(2026, 10, 8, 15, 0) // 周四
	r := periodRangeOf("week", 0, now)
	if r.Start != "2026-10-05" || r.End != "2026-10-11" {
		t.Fatalf("本周应为 10-05 ~ 10-11，得到 %+v", r)
	}
	prev := periodRangeOf("week", -1, now)
	if prev.Start != "2026-09-28" || prev.End != "2026-10-04" {
		t.Fatalf("上周应为 09-28 ~ 10-04，得到 %+v", prev)
	}
}

func TestPeriodRangeMonthQuarterYear(t *testing.T) {
	now := localTime(2026, 10, 8, 15, 0)
	m := periodRangeOf("month", 0, now)
	if m.Key != "2026-10" || m.Label != "2026 年 10 月" || m.Start != "2026-10-01" || m.End != "2026-10-31" {
		t.Fatalf("当月不对：%+v", m)
	}
	// 负偏移跨年：2026-01 的上一个月是 2025-12
	if jan := periodRangeOf("month", -9, now); jan.Key != "2026-01" || jan.End != "2026-01-31" {
		t.Fatalf("2026-01 不对：%+v", jan)
	}
	if dec := periodRangeOf("month", -10, now); dec.Key != "2025-12" || dec.Start != "2025-12-01" || dec.End != "2025-12-31" {
		t.Fatalf("2025-12 不对：%+v", dec)
	}

	q := periodRangeOf("quarter", 0, now)
	if q.Key != "2026-Q4" || q.Start != "2026-10-01" || q.End != "2026-12-31" {
		t.Fatalf("当季不对：%+v", q)
	}
	if q3 := periodRangeOf("quarter", -1, now); q3.Key != "2026-Q3" || q3.Start != "2026-07-01" || q3.End != "2026-09-30" {
		t.Fatalf("上一季不对：%+v", q3)
	}
	// 负偏移跨年 + 季度进位
	if q := periodRangeOf("quarter", -4, now); q.Key != "2025-Q4" || q.Start != "2025-10-01" {
		t.Fatalf("2025-Q4 不对：%+v", q)
	}

	y := periodRangeOf("year", 0, now)
	if y.Key != "2026" || y.Start != "2026-01-01" || y.End != "2026-12-31" {
		t.Fatalf("当年不对：%+v", y)
	}
	if p := periodRangeOf("year", -1, now); p.Key != "2025" {
		t.Fatalf("去年 = %q，应为 2025", p.Key)
	}
}

// ---------- 夹紧与读写 ----------

func TestNormalizeDay(t *testing.T) {
	raw := map[string]any{
		"date":         "2026-10-08",
		"workMs":       float64(1234.6), // 四舍五入
		"shortCount":   float64(-3),     // 负数丢弃
		"longMs":       "abc",           // 非数字丢弃
		"customCount":  float64(2),
		"postponeMs":   float64(0), // 0 不算改动
		"skipMs":       float64(500),
		"unknownField": float64(999), // 未知键忽略
		"customCounts": map[string]any{"cb-water": float64(3), "other": float64(4), "cb-bad": float64(-1)},
	}
	d := normalizeDay(raw, "2026-10-08")
	if d.Date != "2026-10-08" {
		t.Fatalf("date 应保留，得到 %q", d.Date)
	}
	if d.WorkMs != 1235 {
		t.Fatalf("workMs 应四舍五入成 1235，得到 %d", d.WorkMs)
	}
	if d.ShortCount != 0 || d.LongMs != 0 || d.PostponeMs != 0 {
		t.Fatalf("非法 / 非正数应丢弃：%+v", d)
	}
	if d.SkipMs != 500 || d.CustomCount != 2 {
		t.Fatalf("合法值应保留：%+v", d)
	}
	// 只认 cb- 前缀键 + 正数
	if len(d.CustomCounts) != 1 || d.CustomCounts["cb-water"] != 3 {
		t.Fatalf("customCounts 只应留下 cb-water=3，得到 %+v", d.CustomCounts)
	}
	// 空条目：CustomCounts 必须是空 map（序列化成 {} 而不是 null）
	if e := emptyDay("2026-01-01"); e.CustomCounts == nil || len(e.CustomCounts) != 0 {
		t.Fatal("空条目的 CustomCounts 应是非 nil 空 map")
	}
}

func TestStatsLoadFlushRoundTrip(t *testing.T) {
	c := &statsClock{}
	c.set(localTime(2026, 10, 8, 10, 0))
	st, path := newTestStats(t, c)

	st.OnBreakStart("short")
	c.add(20 * time.Second)
	st.OnBreakEnd("", false)
	st.OnBreakStart("cb-water")
	c.add(15 * time.Second)
	st.OnBreakEnd("喝水提醒", false)
	st.RecordTakeoutAction("ordered")
	st.RecordBreakPostponed("short", 120_000)
	st.RecordBreakSkipped("long", 30_000)
	st.Flush()

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("落盘后应存在统计文件：%v", err)
	}

	back := NewStatsStore(func() string { return path })
	back.nowFn = c.now // 用同一个测试时钟，Today() 才会定位到 10-08 而非真实今天
	back.Load()
	d := back.Today()
	if d.ShortCount != 1 || d.ShortMs != 20_000 {
		t.Fatalf("短休息往返不对：%+v", d)
	}
	if d.CustomCount != 1 || d.CustomCounts["cb-water"] != 1 {
		t.Fatalf("自定义提醒往返不对：%+v", d)
	}
	if d.TakeoutOrdered != 1 || d.PostponeCount != 1 || d.PostponeMs != 120_000 || d.SkipMs != 30_000 {
		t.Fatalf("外卖 / 推迟 / 跳过往返不对：%+v", d)
	}
	// 名字快照是保留键，不进天记录
	if got := back.CustomNames()["cb-water"]; got != "喝水提醒" {
		t.Fatalf("名字快照应为「喝水提醒」，得到 %q", got)
	}
	if _, ok := back.Today().CustomCounts["cb-water"]; !ok {
		t.Fatal("按 id 的计数明细应随天记录一起落盘")
	}
}

func TestStatsLoadToleratesGarbage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stats.json")
	bad := []byte("{ this is not json")
	if err := os.WriteFile(path, bad, 0o644); err != nil {
		t.Fatal(err)
	}
	st := NewStatsStore(func() string { return path })
	st.Load() // 不 panic 即为通过
	if n := len(st.DaysInRange("", "")); n != 0 {
		t.Fatalf("损坏文件应从空表开始，得到 %d 天", n)
	}
	if d := st.Today(); d.WorkMs != 0 {
		t.Fatalf("损坏文件后 today 应全 0，得到 %+v", d)
	}
}

// ---------- 统计文件的创建 / 备份重建（打包不再预置 data/stats.json，见 make_portable.py）----------

func TestStatsLoadCreatesEmptyFileWhenMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data", "stats.json") // data 目录也不存在：首跑要连目录一起建
	st := NewStatsStore(func() string { return path })
	st.Load()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("缺文件时应自动创建空表：%v", err)
	}
	var top map[string]any
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatalf("自建的空表必须是合法 JSON：%v（内容 %q）", err, raw)
	}
	if _, ok := top["_customNames"]; !ok {
		t.Fatalf("空表应带 _customNames 保留键（与 marshalLocked 同格式），得到 %q", raw)
	}
	if n := st.FileNotice(); n != nil {
		t.Fatalf("首跑自建不是「文件损坏」，不该产生弹窗说明：%+v", n)
	}
	if n := len(st.DaysInRange("", "")); n != 0 {
		t.Fatalf("新建的空表应为 0 天，得到 %d 天", n)
	}
}

func TestStatsLoadBacksUpBrokenFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stats.json")
	bad := []byte("{ this is not json")
	if err := os.WriteFile(path, bad, 0o644); err != nil {
		t.Fatal(err)
	}
	st := NewStatsStore(func() string { return path })
	st.Load()

	n := st.FileNotice()
	if n == nil {
		t.Fatal("损坏文件必须留下处置说明（界面要弹窗告知用户）")
	}
	if n.File != path {
		t.Fatalf("说明里的文件路径应为 %q，得到 %q", path, n.File)
	}
	if n.Reason == "" || n.At == "" {
		t.Fatalf("说明应带判定原因与处置时刻：%+v", n)
	}
	// 备份与原文件同目录、命名为 stats_<日期_时间>.json，内容与损坏前逐字节一致
	if filepath.Dir(n.Backup) != dir {
		t.Fatalf("备份应留在同目录 %q，得到 %q", dir, n.Backup)
	}
	base := filepath.Base(n.Backup)
	if !strings.HasPrefix(base, "stats_") || !strings.HasSuffix(base, ".json") {
		t.Fatalf("备份名应为 stats_<日期_时间>.json，得到 %q", base)
	}
	if got, err := os.ReadFile(n.Backup); err != nil || !bytes.Equal(got, bad) {
		t.Fatalf("备份内容应保留原始数据（err=%v）", err)
	}
	// 原路径上换成了空表
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("重建后原路径应存在空表：%v", err)
	}
	var top map[string]any
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatalf("重建的空表必须可解析：%v（内容 %q）", err, raw)
	}
	if len(top) != 1 {
		t.Fatalf("重建的空表只该有 _customNames，得到 %q", raw)
	}

	// 同一秒内再次损坏：备份名必须退让，不能覆盖上一份备份
	if err := os.WriteFile(path, []byte("[1,2,3]"), 0o644); err != nil { // 数组 = 顶层结构不对
		t.Fatal(err)
	}
	st.Load()
	n2 := st.FileNotice()
	if n2 == nil || n2.Backup == n.Backup {
		t.Fatalf("第二次损坏应另存一份备份，得到 %+v", n2)
	}
	backups, _ := filepath.Glob(filepath.Join(dir, "stats_*.json"))
	if len(backups) != 2 {
		t.Fatalf("两次损坏应留下两份备份，得到 %v", backups)
	}

	// 用户点掉弹窗后不再有说明
	st.AckFileNotice()
	if n := st.FileNotice(); n != nil {
		t.Fatalf("Ack 之后不该再有说明：%+v", n)
	}
}

func TestStatsLoadKeepsFileWhenUnreadable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stats.json")
	// 用同名**目录**制造读取失败：读不出来 ≠ 文件坏了，绝不能改名或删除用户的文件
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Skipf("无法在同名目录上制造读取失败：%v", err)
	}
	st := NewStatsStore(func() string { return path })
	st.Load()
	if fi, err := os.Stat(path); err != nil || !fi.IsDir() {
		t.Fatalf("读取失败时不该动用户的路径（err=%v）", err)
	}
	if n := st.FileNotice(); n != nil {
		t.Fatalf("读取失败不是「格式错误」，不该弹窗说明：%+v", n)
	}
	if len(st.DaysInRange("", "")) != 0 {
		t.Fatal("读取失败时应从空表开始用")
	}
}

func TestStatsClear(t *testing.T) {
	c := &statsClock{}
	c.set(localTime(2026, 10, 8, 10, 0))
	st, path := newTestStats(t, c)
	st.OnBreakStart("short")
	c.add(20 * time.Second)
	st.OnBreakEnd("", false)
	st.RecordTakeoutAction("ordered")

	st.Clear()
	if d := st.Today(); d.WorkMs != 0 || d.ShortCount != 0 || d.TakeoutOrdered != 0 {
		t.Fatalf("清空后 today 应全 0，得到 %+v", d)
	}
	if n := len(st.DaysInRange("", "")); n != 1 {
		// Clear 会立即写空表；随后 Today() 又建出当天的全 0 条目 —— 这是刻意的
		t.Fatalf("清空并 touch 之后应只剩当天一条，得到 %d 条", n)
	}
	back := NewStatsStore(func() string { return path })
	back.Load()
	if n := len(back.DaysInRange("", "")); n != 0 {
		t.Fatalf("落盘的应是空表，得到 %d 条", n)
	}
	if got := back.CustomNames(); len(got) != 0 {
		t.Fatalf("清空后名字快照也应为空，得到 %+v", got)
	}
}

// ---------- 休息记账 ----------

func TestBreakAccounting(t *testing.T) {
	c := &statsClock{}
	c.set(localTime(2026, 10, 8, 10, 0))
	st, _ := newTestStats(t, c)

	st.OnBreakStart("short")
	if k, _ := st.BreakLogInfo(); k != "short" {
		t.Fatalf("进行中休息类型应为 short，得到 %q", k)
	}
	c.add(30 * time.Second)
	if n := st.OnBreakTick(); n != 30_000 {
		t.Fatalf("首跳应入账 30s，得到 %d", n)
	}
	if n := st.OnBreakTick(); n != 0 {
		t.Fatalf("重复推进应幂等（0），得到 %d", n)
	}
	c.add(10 * time.Second)
	kind, ms := st.OnBreakEnd("", false)
	if kind != "short" || ms != 40_000 {
		t.Fatalf("结束应报 short/40000ms，得到 %q/%d", kind, ms)
	}
	d := st.Today()
	if d.ShortMs != 40_000 || d.BreakMs != 40_000 || d.LongMs != 0 || d.ShortCount != 1 {
		t.Fatalf("短休息记账不对：%+v", d)
	}
	if k, _ := st.BreakLogInfo(); k != "" {
		t.Fatal("结束后不该还有进行中的休息")
	}
	// 没有休息在计时时结束：类型为空、不记任何东西
	if k, n := st.OnBreakEnd("", false); k != "" || n != 0 {
		t.Fatalf("空结束应返回空串/0，得到 %q/%d", k, n)
	}

	// 时长为 0（点开就结束）也要记这笔「完成了一次」
	st.OnBreakStart("long")
	if k, n := st.OnBreakEnd("", false); k != "long" || n != 0 {
		t.Fatalf("0 时长结束应报 long/0，得到 %q/%d", k, n)
	}
	if d := st.Today(); d.LongCount != 1 || d.LongMs != 0 {
		t.Fatalf("0 时长也应记次数：%+v", d)
	}

	// 自定义提醒：不并进短/长，单独计数 + 明细 + 名字快照
	st.OnBreakStart("cb-water")
	c.add(5 * time.Second)
	st.OnBreakEnd("喝水提醒", false)
	d = st.Today()
	if d.CustomCount != 1 || d.CustomCounts["cb-water"] != 1 {
		t.Fatalf("自定义提醒记账不对：%+v", d)
	}
	if d.ShortCount != 1 || d.LongCount != 1 {
		t.Fatalf("自定义提醒不该并进短/长：%+v", d)
	}
	if got := st.CustomNames()["cb-water"]; got != "喝水提醒" {
		t.Fatalf("名字快照应为「喝水提醒」，得到 %q", got)
	}
}

// TestBreakTickSplitsAcrossMidnight 休息跨零点：时长按本地零点切开，各归各天；
// 次数只记在**结束那天**（一次休息就是一个事件，切不成两半）。
func TestBreakTickSplitsAcrossMidnight(t *testing.T) {
	c := &statsClock{}
	c.set(localTime(2026, 10, 8, 23, 40))
	st, _ := newTestStats(t, c)
	st.OnBreakStart("short")
	c.set(localTime(2026, 10, 9, 0, 10))
	st.OnBreakEnd("", false)

	if d := st.dayForTest("2026-10-08"); d.ShortMs != 20*60_000 || d.ShortCount != 0 {
		t.Fatalf("前一天应记 20 分钟、不记次数：%+v", d)
	}
	if d := st.dayForTest("2026-10-09"); d.ShortMs != 10*60_000 || d.ShortCount != 1 {
		t.Fatalf("当天应记 10 分钟与 1 次：%+v", d)
	}
}

// ---------- 离开 ----------

func TestBeginAwayCapsByWorkMs(t *testing.T) {
	c := &statsClock{}
	c.set(localTime(2026, 10, 8, 10, 0))
	st, _ := newTestStats(t, c)

	st.mu.Lock()
	st.touchTodayLocked().WorkMs = 5 * 60_000
	// 工作段起点挪到 10 分钟前，才可能把「已计入的那段」搬走
	st.workSegStartAt = c.now() - 10*60_000
	st.mu.Unlock()

	moved := st.BeginAway(c.now() - 10*60_000)
	if moved != 5*60_000 {
		t.Fatalf("应按当天 workMs 封顶搬走 5 分钟，得到 %d", moved)
	}
	d := st.Today()
	if d.WorkMs != 0 || d.AwayMs != 5*60_000 {
		t.Fatalf("搬完应工作 0 / 离开 5 分钟：%+v", d)
	}
	// 已在离开中：再搬一次不重开一段
	if again := st.BeginAway(c.now() - 10*60_000); again != 0 {
		t.Fatalf("已在离开中应搬 0，得到 %d", again)
	}
}

// TestBeginAwayNotCrossDay 离开不跨天：只搬「离开起点所在那天」的部分
func TestBeginAwayNotCrossDay(t *testing.T) {
	c := &statsClock{}
	c.set(localTime(2026, 10, 9, 0, 3))
	st, _ := newTestStats(t, c)

	st.mu.Lock()
	st.dayOfLocked("2026-10-08").WorkMs = 4 * 60_000
	st.dayOfLocked("2026-10-09").WorkMs = 10 * 60_000
	st.workSegStartAt = localTime(2026, 10, 8, 23, 0).UnixMilli()
	st.mu.Unlock()

	// 离开起点 = 前一天 23:58 → 只搬 10-08 的 2 分钟（受该天 workMs 封顶，4 分钟够）
	moved := st.BeginAway(localTime(2026, 10, 8, 23, 58).UnixMilli())
	if moved != 2*60_000 {
		t.Fatalf("只应搬走前一天的 2 分钟，得到 %d", moved)
	}
	if d := st.dayForTest("2026-10-08"); d.AwayMs != 2*60_000 || d.WorkMs != 2*60_000 {
		t.Fatalf("前一天的账不对：%+v", d)
	}
	if d := st.dayForTest("2026-10-09"); d.AwayMs != 0 || d.WorkMs != 10*60_000 {
		t.Fatalf("当天不该被搬：%+v", d)
	}
}

func TestOnAwayTickNotCrossDay(t *testing.T) {
	c := &statsClock{}
	c.set(localTime(2026, 10, 8, 23, 0))
	st, _ := newTestStats(t, c)
	st.mu.Lock()
	st.workSegStartAt = c.now()
	st.mu.Unlock()
	st.BeginAway(c.now())

	// 推进到次日 00:05：只累计 10-08 的那 1 小时
	c.set(localTime(2026, 10, 9, 0, 5))
	added := st.OnAwayTick()
	if added != 60*60_000 {
		t.Fatalf("只应累计前一天的 1 小时，得到 %d", added)
	}
	if d := st.dayForTest("2026-10-09"); d.AwayMs != 0 {
		t.Fatalf("当天不该累计离开：%+v", d)
	}
	if d := st.dayForTest("2026-10-08"); d.AwayMs != 60*60_000 {
		t.Fatalf("前一天应累计 1 小时：%+v", d)
	}
	// 幂等
	if n := st.OnAwayTick(); n != 0 {
		t.Fatalf("重复推进应为 0，得到 %d", n)
	}
	st.EndAway()
	if info := st.AwayInfo(); info.Open {
		t.Fatalf("结束后不该仍在离开中：%+v", info)
	}
}

func TestAwayPartOf(t *testing.T) {
	from := localTime(2026, 10, 8, 23, 58)
	to := localTime(2026, 10, 9, 0, 3)
	if got := AwayPartOf(from.UnixMilli(), to.UnixMilli(), "2026-10-08"); got != 2*60_000 {
		t.Fatalf("10-08 的部分应为 2 分钟，得到 %d", got)
	}
	if got := AwayPartOf(from.UnixMilli(), to.UnixMilli(), "2026-10-09"); got != 3*60_000 {
		t.Fatalf("10-09 的部分应为 3 分钟，得到 %d", got)
	}
}

func TestCountedSpanSleep(t *testing.T) {
	last, now := int64(1000), int64(2000)
	// 不睡眠：整跳都算
	if a, b, ok := countedSpan(last, now, 0, 0); !ok || a != last || b != now {
		t.Fatalf("普通跳应整跳计入，得到 %d/%d/%v", a, b, ok)
	}
	// 睡眠中：上限钉在入睡那一刻
	if a, b, ok := countedSpan(last, now, 1500, 0); !ok || a != 1000 || b != 1500 {
		t.Fatalf("睡眠中应剪到入睡时刻，得到 %d/%d/%v", a, b, ok)
	}
	// 整跳都在入睡之后：不计
	if _, _, ok := countedSpan(1600, 2000, 1500, 0); ok {
		t.Fatal("入睡之后的跳不该计入工作")
	}
	// 这一跳跨过醒来时刻：从醒来那一刻算起
	if a, b, ok := countedSpan(1000, 2000, 0, 1500); !ok || a != 1500 || b != 2000 {
		t.Fatalf("跨醒来应从头算，得到 %d/%d/%v", a, b, ok)
	}
	// 醒来时刻在区间之外：不受影响
	if a, _, _ := countedSpan(1000, 2000, 0, 900); a != 1000 {
		t.Fatalf("醒来早于起点时不该剪，得到 %d", a)
	}
}

// ---------- 其它记录点 ----------

func TestRecordTakeoutPostponeSkip(t *testing.T) {
	c := &statsClock{}
	c.set(localTime(2026, 10, 8, 10, 0))
	st, _ := newTestStats(t, c)

	st.RecordTakeoutAction("ordered")
	st.RecordTakeoutAction("skip")
	st.RecordTakeoutAction("postpone")
	st.RecordBreakPostponed("short", 120_000)
	st.RecordBreakPostponed("short", 60_000)
	st.RecordBreakSkipped("long", 30_000)
	st.RecordBreakSkipped("long", -5) // 负数时长夹成 0，但次数照记

	d := st.Today()
	if d.TakeoutOrdered != 1 || d.TakeoutSkipped != 1 || d.TakeoutPostponed != 1 {
		t.Fatalf("外卖三动作不对：%+v", d)
	}
	if d.PostponeCount != 2 || d.PostponeMs != 180_000 {
		t.Fatalf("推迟记账不对：%+v", d)
	}
	if d.SkipCount != 2 || d.SkipMs != 30_000 {
		t.Fatalf("跳过记账不对：%+v", d)
	}
}

func TestDaysInRange(t *testing.T) {
	c := &statsClock{}
	c.set(localTime(2026, 10, 8, 10, 0))
	st, _ := newTestStats(t, c)
	st.mu.Lock()
	st.dayOfLocked("2026-10-05").WorkMs = 1
	st.dayOfLocked("2026-10-07").WorkMs = 2
	st.dayOfLocked("2026-10-08").WorkMs = 3
	st.mu.Unlock()

	all := st.DaysInRange("", "")
	if len(all) != 3 {
		t.Fatalf("不限范围应返回 3 天，得到 %d", len(all))
	}
	// 升序
	if all[0].Date != "2026-10-05" || all[2].Date != "2026-10-08" {
		t.Fatalf("应按日期升序，得到 %s..%s", all[0].Date, all[2].Date)
	}
	// 闭区间
	if got := st.DaysInRange("2026-10-06", "2026-10-08"); len(got) != 2 || got[0].Date != "2026-10-07" {
		t.Fatalf("范围过滤不对：%+v", got)
	}
	// 非法入参 = 该侧不限
	if got := st.DaysInRange("abc", "xyz"); len(got) != 3 {
		t.Fatalf("非法入参应视为不限，得到 %d 天", len(got))
	}
	// 起点晚于终点：空
	if got := st.DaysInRange("2026-10-09", "2026-10-01"); len(got) != 0 {
		t.Fatalf("逆序范围应为空，得到 %d 天", len(got))
	}
}

func TestPeriodsAggregate(t *testing.T) {
	c := &statsClock{}
	c.set(localTime(2026, 10, 8, 12, 0))
	st, _ := newTestStats(t, c)
	st.mu.Lock()
	a := st.dayOfLocked("2026-10-08")
	a.WorkMs, a.ShortCount, a.CustomCount, a.EndEarlyCount = 1_000, 2, 5, 4
	b := st.dayOfLocked("2026-10-07")
	b.WorkMs, b.ShortCount, b.CustomCount, b.EndEarlyCount = 2_000, 3, 7, 1
	st.mu.Unlock()

	ps := st.Periods()
	if len(ps) != 4 {
		t.Fatalf("周期汇总应为 4 项（周月季年），得到 %d", len(ps))
	}
	if ps[0].Key[:6] != "2026-W" || ps[1].Key != "2026-10" || ps[2].Key != "2026-Q4" || ps[3].Key != "2026" {
		t.Fatalf("周期顺序应为 周/月/季/年：%+v", []string{ps[0].Key, ps[1].Key, ps[2].Key, ps[3].Key})
	}
	// 10-07 与 10-08 同属 10-05~10-11 那周
	for i, p := range ps {
		if p.WorkMs != 3_000 || p.ShortCount != 5 || p.CustomCount != 12 {
			t.Errorf("第 %d 个周期的聚合不对：%+v", i, p)
		}
		// 提前结束次数也是累加项：休息报表要按周期画它（4 + 1）
		if p.EndEarlyCount != 5 {
			t.Errorf("第 %d 个周期的提前结束次数应为 5，实得 %d", i, p.EndEarlyCount)
		}
	}
	// 区间之外的数据不计入
	st.mu.Lock()
	st.dayOfLocked("2025-10-08").WorkMs = 9_999
	st.mu.Unlock()
	if ps := st.Periods(); ps[3].WorkMs != 3_000 {
		t.Fatalf("去年的数据不该落进今年：%+v", ps[3])
	}
}

// ---------- 睡眠补记 ----------

// TestNotifySlept 醒来补记一段睡眠：睡眠整段算离开、工作段从醒来重起、不跨天。
func TestNotifySlept(t *testing.T) {
	c := &statsClock{}
	c.set(localTime(2026, 10, 8, 10, 0))
	st, _ := newTestStats(t, c)

	// 从 10:00 睡到 10:30（30 分钟）
	st.NotifySlept(c.now(), c.now()+30*60_000)
	c.add(30 * time.Minute) // 醒来时刻推进 30 分钟

	d := st.Today()
	if d.AwayMs != 30*60_000 || d.WorkMs != 0 {
		t.Fatalf("睡眠 30 分钟应全记离开、不记工作：%+v", d)
	}
	st.mu.Lock()
	seg := st.workSegStartAt
	st.mu.Unlock()
	if seg != c.now() {
		t.Fatalf("工作段应从醒来时刻重起：%d vs %d", seg, c.now())
	}
}

// TestNotifySleptNotCrossDay 睡眠跨零点只记起点那天（与 OnAwayTick 同一口径）。
func TestNotifySleptNotCrossDay(t *testing.T) {
	c := &statsClock{}
	c.set(localTime(2026, 10, 9, 0, 3))
	st, _ := newTestStats(t, c)

	// 从 10-08 23:58 睡到 10-09 00:03（跨零点 5 分钟）
	from := localTime(2026, 10, 8, 23, 58).UnixMilli()
	to := c.now()
	st.NotifySlept(from, to)

	if d := st.dayForTest("2026-10-08"); d.AwayMs != 2*60_000 {
		t.Fatalf("只应记睡眠起点那天（10-08）的 2 分钟，得到 %+v", d)
	}
	if d := st.dayForTest("2026-10-09"); d.AwayMs != 0 {
		t.Fatalf("10-09 不该记离开（不跨天），得到 %+v", d)
	}
}

// TestNotifySleptDuringAway 睡眠撞上空闲离开：推进尾账锚点，不重复记。
func TestNotifySleptDuringAway(t *testing.T) {
	c := &statsClock{}
	c.set(localTime(2026, 10, 8, 10, 0))
	st, _ := newTestStats(t, c)

	// 先进入空闲离开（把 10 分钟前到现在搬进离开）
	st.mu.Lock()
	st.touchTodayLocked().WorkMs = 10 * 60_000
	st.workSegStartAt = c.now() - 10*60_000
	st.mu.Unlock()
	st.BeginAway(c.now() - 10*60_000)

	// 离开期间睡了 5 分钟
	sleepFrom := c.now()
	c.add(5 * time.Minute)
	st.NotifySlept(sleepFrom, c.now())

	// 醒来后又推进一次离开尾账（模拟 onAwayTick），不该重复记睡眠那段
	c.add(3 * time.Minute)
	st.OnAwayTick()

	// 总离开 = 10 分钟(初始搬) + 5 分钟(睡眠) + 3 分钟(醒来后) = 18 分钟
	if d := st.Today(); d.AwayMs != 18*60_000 {
		t.Fatalf("离开应累计 18 分钟（初始 10 + 睡眠 5 + 醒来后 3），得到 %+v", d)
	}
}

// ---------- 落点推导 ----------

func TestStatsFileForConfig(t *testing.T) {
	if got := statsFileForConfig(""); got != "stats.json" {
		t.Fatalf("空路径应退回 stats.json，得到 %q", got)
	}
	// 便携形态：<运行目录>/config/settings.json → <运行目录>/data/stats.json
	portable := filepath.Join("C:", "app", "config", "settings.json")
	if got, want := statsFileForConfig(portable), filepath.Join("C:", "app", "data", "stats.json"); got != want {
		t.Fatalf("便携形态落点 = %q，应为 %q", got, want)
	}
	// 非 config 目录形态：与配置文件同目录
	loose := filepath.Join("C:", "somewhere", "else", "settings.json")
	if got, want := statsFileForConfig(loose), filepath.Join("C:", "somewhere", "else", "stats.json"); got != want {
		t.Fatalf("非 config 形态落点 = %q，应为 %q", got, want)
	}
}

// TestStatsChangeNotify 「数据变了」要真的通知出去（统计报表「今天」卡靠它实时刷新）。
//
// 三件事一起钉：记一笔就通知；连着记多笔不会被合并丢掉至少一次；Stop 之后不再通知。
func TestStatsChangeNotify(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stats.json")
	st := NewStatsStore(func() string { return path })
	st.Load()
	defer st.Stop()

	var mu sync.Mutex
	hits := 0
	st.SetOnChange(func() {
		mu.Lock()
		hits++
		mu.Unlock()
	})
	st.StartNotify()

	waitHits := func(min int, d time.Duration) int {
		deadline := time.Now().Add(d)
		for time.Now().Before(deadline) {
			mu.Lock()
			n := hits
			mu.Unlock()
			if n >= min {
				return n
			}
			time.Sleep(10 * time.Millisecond)
		}
		mu.Lock()
		defer mu.Unlock()
		return hits
	}

	st.RecordTakeoutAction("ordered")
	if n := waitHits(1, 2*time.Second); n < 1 {
		t.Fatalf("记一笔外卖动作后没有收到通知（hits=%d）", n)
	}

	// 连着记 5 笔：容量 1 的通道会合并，但至少要再收到一次
	before := waitHits(1, time.Second)
	for i := 0; i < 5; i++ {
		st.RecordBreakPostponed("short", 300_000)
	}
	if n := waitHits(before+1, 2*time.Second); n <= before {
		t.Fatalf("连续记账没有通知（hits 停在 %d）", n)
	}

	// 清空也是「变了」
	before = waitHits(1, time.Second)
	time.Sleep(50 * time.Millisecond)
	st.Clear()
	if n := waitHits(before+1, 2*time.Second); n <= before {
		t.Fatalf("Clear 之后没有通知（hits 停在 %d）", n)
	}

	// Stop 之后不再通知（notifier goroutine 退出）
	st.Stop()
	time.Sleep(50 * time.Millisecond)
	afterStop := waitHits(1, 100*time.Millisecond)
	st.RecordTakeoutAction("ordered")
	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	final := hits
	mu.Unlock()
	if final != afterStop {
		t.Fatalf("Stop 之后仍在通知（%d → %d）", afterStop, final)
	}
}

// ---------- 最长连续工作（今天卡「久坐」面板） ----------

// workTicks 连续记 d 长的工作（每 1 秒一拍，与真实计时器同粒度）
func workTicks(st *StatsStore, c *statsClock, d time.Duration) {
	for i := int64(0); i < int64(d/time.Second); i++ {
		st.accountWorkTick(c.ms, c.ms+1000)
		c.add(time.Second)
	}
}

func TestMaxWorkStreak(t *testing.T) {
	c := &statsClock{}
	c.set(localTime(2026, 10, 9, 9, 0))
	st, path := newTestStats(t, c)
	defer st.Stop()

	// 连续工作 12 分钟 → 最长连续 = 12 分钟
	workTicks(st, c, 12*time.Minute)
	d := st.dayForTest("2026-10-09")
	if d.WorkMs != 720_000 {
		t.Fatalf("工作时长应为 12 分钟，实得 %d", d.WorkMs)
	}
	if d.MaxStreakMs != 720_000 {
		t.Fatalf("最长连续工作应为 12 分钟，实得 %d", d.MaxStreakMs)
	}
	// 段数组里此时只有一项，就是这段进行中的 12 分钟（久坐面板的「平均 / 中位数」靠它）
	if len(d.WorkSegments) != 1 || d.WorkSegments[0] != 720_000 {
		t.Fatalf("段数组应只有一段 12 分钟，实得 %v", d.WorkSegments)
	}

	// 被休息打断后再工作 5 分钟 → 最长仍取 12 分钟（不是累加，也不被更短的段盖掉）
	st.endStreakAt(c.ms)
	c.add(time.Minute) // 休息一分钟
	workTicks(st, c, 5*time.Minute)
	d = st.dayForTest("2026-10-09")
	if d.WorkMs != 1_020_000 {
		t.Fatalf("两段工作时间合计应为 17 分钟，实得 %d", d.WorkMs)
	}
	if d.MaxStreakMs != 720_000 {
		t.Fatalf("最长连续工作应保持 12 分钟，实得 %d", d.MaxStreakMs)
	}
	if len(d.WorkSegments) != 2 || d.WorkSegments[0] != 720_000 || d.WorkSegments[1] != 300_000 {
		t.Fatalf("段数组应为 [12 分钟, 5 分钟]，实得 %v", d.WorkSegments)
	}

	// 记盘 → 读回：maxStreakMs 不能被 normalizeDay 丢掉
	st.Flush()
	st2 := NewStatsStore(func() string { return path })
	st2.nowFn = c.now
	st2.Load()
	if got := st2.dayForTest("2026-10-09").MaxStreakMs; got != 720_000 {
		t.Fatalf("重新读盘后最长连续工作应为 12 分钟，实得 %d", got)
	}
	// 段数组也要能读回（平均 / 中位数全靠它，丢了面板就退化成 0）
	if got := st2.dayForTest("2026-10-09").WorkSegments; len(got) != 2 || got[0] != 720_000 || got[1] != 300_000 {
		t.Fatalf("重新读盘后段数组应为 [12 分钟, 5 分钟]，实得 %v", got)
	}
	st2.Stop()

	// 清空统计 → 归零，且新的连续段从头起算
	st.Clear()
	if got := st.dayForTest("2026-10-09").MaxStreakMs; got != 0 {
		t.Fatalf("清空后最长连续工作应归零，实得 %d", got)
	}
	workTicks(st, c, 3*time.Minute)
	d = st.dayForTest("2026-10-09")
	if d.MaxStreakMs != 180_000 {
		t.Fatalf("清空后重新累计应为 3 分钟，实得 %d", d.MaxStreakMs)
	}
	if len(d.WorkSegments) != 1 || d.WorkSegments[0] != 180_000 {
		t.Fatalf("清空后段数组应只有一段 3 分钟，实得 %v", d.WorkSegments)
	}
}

func TestMaxWorkStreakSplitsAcrossMidnight(t *testing.T) {
	c := &statsClock{}
	c.set(localTime(2026, 10, 9, 23, 50))
	st, _ := newTestStats(t, c)
	defer st.Stop()

	// 23:50 一路工作到 00:10：一天各记自己那 10 分钟，不合成 20 分钟
	workTicks(st, c, 20*time.Minute)
	if got := st.dayForTest("2026-10-09").MaxStreakMs; got != 600_000 {
		t.Fatalf("前一天最长连续工作应为 10 分钟，实得 %d", got)
	}
	if got := st.dayForTest("2026-10-10").MaxStreakMs; got != 600_000 {
		t.Fatalf("次天最长连续工作应为 10 分钟，实得 %d", got)
	}
	// 段数组同样按天切：前一天一段定稿的 10 分钟，次天一段 10 分钟
	if got := st.dayForTest("2026-10-09").WorkSegments; len(got) != 1 || got[0] != 600_000 {
		t.Fatalf("前一天段数组应为 [10 分钟]，实得 %v", got)
	}
	if got := st.dayForTest("2026-10-10").WorkSegments; len(got) != 1 || got[0] != 600_000 {
		t.Fatalf("次天段数组应为 [10 分钟]，实得 %v", got)
	}
}

func TestMaxWorkStreakEndsOnAway(t *testing.T) {
	c := &statsClock{}
	c.set(localTime(2026, 10, 9, 14, 0))
	st, _ := newTestStats(t, c)
	defer st.Stop()

	workTicks(st, c, 8*time.Minute) // 14:00–14:08 一直工作
	// 判为离开（最后一次输入就在此刻）→ 连续段断掉
	st.BeginAway(c.ms)
	workTicks(st, c, 3*time.Minute)
	if got := st.dayForTest("2026-10-09").MaxStreakMs; got != 480_000 {
		t.Fatalf("离开后最长连续工作应停在 8 分钟，实得 %d", got)
	}
}

// ---------- 提前结束休息 ----------

// 提前结束 = 只记 EndEarlyCount，**不计入完成**（ShortCount / LongCount 不动），
// 时长照实记进休息时长（那确实是坐过的时间）。
func TestBreakEndEarlyNotCountedAsDone(t *testing.T) {
	c := &statsClock{}
	c.set(localTime(2026, 10, 9, 10, 0))
	st, path := newTestStats(t, c)
	defer st.Stop()

	// 短休息坐满 2 分钟后提前结束
	st.OnBreakStart("short")
	c.add(2 * time.Minute)
	st.OnBreakEnd("", true)
	d := st.dayForTest("2026-10-09")
	if d.EndEarlyCount != 1 {
		t.Fatalf("提前结束应记 1 次，实得 %d", d.EndEarlyCount)
	}
	if d.ShortCount != 0 {
		t.Fatalf("提前结束不该计入完成（shortCount 应为 0），实得 %d", d.ShortCount)
	}
	if d.ShortMs != 120_000 || d.BreakMs != 120_000 {
		t.Fatalf("提前结束的时长仍要照实记，实得 %+v", d)
	}

	// 正常坐满的长休息：计入完成，提前结束不变
	st.OnBreakStart("long")
	c.add(5 * time.Minute)
	st.OnBreakEnd("", false)
	d = st.dayForTest("2026-10-09")
	if d.LongCount != 1 || d.EndEarlyCount != 1 {
		t.Fatalf("正常结束应记完成、不记提前结束：%+v", d)
	}

	// early 对自定义提醒无效：它们本来就没有「提前结束」这个口径
	st.OnBreakStart("cb-water")
	c.add(time.Minute)
	st.OnBreakEnd("喝水提醒", true)
	d = st.dayForTest("2026-10-09")
	if d.EndEarlyCount != 1 {
		t.Fatalf("自定义提醒不该记提前结束，实得 %d", d.EndEarlyCount)
	}
	if d.CustomCount != 1 || d.CustomCounts["cb-water"] != 1 {
		t.Fatalf("自定义提醒应照常记完成明细：%+v", d)
	}

	// 记盘 → 读回（normalizeDay 要认得这两个键）
	st.Flush()
	st2 := NewStatsStore(func() string { return path })
	st2.nowFn = c.now
	st2.Load()
	if got := st2.dayForTest("2026-10-09").EndEarlyCount; got != 1 {
		t.Fatalf("重新读盘后提前结束应为 1 次，实得 %d", got)
	}
	st2.Stop()

	// 清空统计 → 归零
	st.Clear()
	if got := st.dayForTest("2026-10-09").EndEarlyCount; got != 0 {
		t.Fatalf("清空后提前结束应归零，实得 %d", got)
	}
}

// 四项（完成 / 推迟 / 跳过 / 提前结束）只统计短休息、长休息：自定义提醒的推迟与跳过
// 绝不能混进来（外卖走独立的 RecordTakeoutAction，不在同一组口径里）。
func TestBreakStatsIgnoreCustomReminders(t *testing.T) {
	c := &statsClock{}
	c.set(localTime(2026, 10, 9, 10, 0))
	st, _ := newTestStats(t, c)
	defer st.Stop()

	a := &App{stats: st}
	// 自定义提醒：推迟 / 跳过都调了宿主回调，但一条都不该记
	a.onBreakPostponed("cb-water", 5*60_000)
	a.onBreakSkipped("cb-water", 5*60_000)
	if d := st.dayForTest("2026-10-09"); d.PostponeCount != 0 || d.SkipCount != 0 {
		t.Fatalf("自定义提醒不该计入推迟 / 跳过：%+v", d)
	}

	// 内置短 / 长休息：照常记
	a.onBreakPostponed("short", 5*60_000)
	a.onBreakSkipped("long", 2*60_000)
	d := st.dayForTest("2026-10-09")
	if d.PostponeCount != 1 || d.PostponeMs != 5*60_000 {
		t.Fatalf("短休息推迟应记 1 次 / 5 分钟：%+v", d)
	}
	if d.SkipCount != 1 || d.SkipMs != 2*60_000 {
		t.Fatalf("长休息跳过应记 1 次 / 2 分钟：%+v", d)
	}
	if d.CustomCount != 0 {
		t.Fatalf("推迟 / 跳过不该改自定义完成次数：%+v", d)
	}
}

// ---------- 连续运行跨天：Today 快照要跟着翻到新的一天 ----------

// 软件不重启、连续跑过零点时，渲染层每秒拿到的 Today() 必须换人：
// 日期换成新的一天、各项数值是新的零起点，昨天的账一个字都不许动。
// （前端「今天」卡就是靠这个快照刷的；报表范围由前端现算，另有跨天重画。）
func TestTodayRollsOverAtMidnight(t *testing.T) {
	c := &statsClock{}
	c.set(localTime(2026, 10, 9, 23, 58))
	st, path := newTestStats(t, c)
	defer st.Stop()

	// 23:58 → 23:59 一直工作
	st.accountWorkTick(c.ms, c.ms+60_000)
	c.add(time.Minute)
	today := st.Today()
	if today.Date != "2026-10-09" || today.WorkMs != 60_000 {
		t.Fatalf("跨天前 Today 应为 10-09 / 1 分钟：%+v", today)
	}

	// 跳到 00:01：Today 直接翻到 10-10，工作归零（这是新一天的空账）
	c.set(localTime(2026, 10, 10, 0, 1))
	today = st.Today()
	if today.Date != "2026-10-10" {
		t.Fatalf("跨天后 Today 的日期应变成 10-10，实得 %q", today.Date)
	}
	if today.WorkMs != 0 || today.BreakMs != 0 || today.AwayMs != 0 ||
		today.ShortCount != 0 || today.PostponeCount != 0 || today.SkipCount != 0 {
		t.Fatalf("跨天后新一天的账应该全是 0：%+v", today)
	}

	// 前一天一个字节都不许变
	if d := st.dayForTest("2026-10-09"); d.WorkMs != 60_000 {
		t.Fatalf("跨天后昨天的工时不该被改：%+v", d)
	}

	// 新一天继续记：只进新的一天
	st.accountWorkTick(c.ms, c.ms+30_000)
	if d := st.dayForTest("2026-10-10"); d.WorkMs != 30_000 {
		t.Fatalf("跨天后新记的工时应进新的一天：%+v", d)
	}
	if d := st.dayForTest("2026-10-09"); d.WorkMs != 60_000 {
		t.Fatalf("新一天的记账不该漏回昨天：%+v", d)
	}

	// 落盘读回：两天的记录都在
	st.Flush()
	st2 := NewStatsStore(func() string { return path })
	st2.nowFn = c.now
	st2.Load()
	if d := st2.dayForTest("2026-10-09"); d.WorkMs != 60_000 {
		t.Fatalf("读盘后昨天的工时应保持 1 分钟：%+v", d)
	}
	if d := st2.dayForTest("2026-10-10"); d.WorkMs != 30_000 {
		t.Fatalf("读盘后新一天的工时应为 30 秒：%+v", d)
	}
	st2.Stop()
}
