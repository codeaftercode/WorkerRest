package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
