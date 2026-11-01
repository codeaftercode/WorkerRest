package main

import (
	"path/filepath"
	"sync"
	"testing"
	"time"
)

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
