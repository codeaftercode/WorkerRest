package stats

import (
	"testing"
	"time"
)

// ---------- 最长连续工作（今天卡“久坐”面板） ----------

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
	// 段数组里此时只有一项，就是这段进行中的 12 分钟（久坐面板的“平均 / 中位数”靠它）
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

	// 记盘 → 读回：maxStreakMs 不能被 NormalizeDay 丢掉
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

	// early 对自定义提醒无效：它们本来就没有“提前结束”这个口径
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

	// 记盘 → 读回（NormalizeDay 要认得这两个键）
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

// ---------- 连续运行跨天：Today 快照要跟着翻到新的一天 ----------

// 软件不重启、连续跑过零点时，渲染层每秒拿到的 Today() 必须换人：
// 日期换成新的一天、各项数值是新的零起点，昨天的账一个字都不许动。
// （前端“今天”卡就是靠这个快照刷的；报表范围由前端现算，另有跨天重画。）
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
