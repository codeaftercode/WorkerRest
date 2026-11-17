package stats

import (
	"os"
	"testing"
	"time"
)

// 这一组钉的是“后台循环真的在跑”—— 它们是断电 / 睡眠时的兜底，平时看不见，
// 坏了也只会表现为“数据莫名少一截”，所以必须有自动化证据。

// 周期落盘：脏了就在下一拍写盘；不脏就不写。
func TestStartAutoFlushTicksAndFlushes(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/stats.json"
	st := NewStatsStore(func() string { return path })
	st.Load()
	defer st.Stop()

	st.StartAutoFlush(10) // 10ms 一拍，测试里不用等一分钟
	if info := st.AutoFlushInfo(); info.IntervalMs != 10 {
		t.Fatalf("间隔应被记下（自检信息要能说明兜底节奏），得到 %+v", info)
	}

	// 造一次改动（脏标记）——用真实的记账入口，不直接改字段
	now := st.nowFn()
	st.accountWorkTick(now-1000, now)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		info := st.AutoFlushInfo()
		if info.Ticks >= 1 && !info.Dirty {
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("脏了却没落盘: %v", err)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("自动落盘没在 2 秒内发生，自检信息 %+v", st.AutoFlushInfo())
}

// 没有改动时不写盘：不做无谓的磁盘写入（机械盘上一天能省下几千次 fsync）。
func TestAutoFlushDoesNotWriteWhenClean(t *testing.T) {
	path := t.TempDir() + "/stats.json"
	st := NewStatsStore(func() string { return path })
	st.Load()
	defer st.Stop()
	os.Remove(path) // Load 会在首跑时建一份空表，这里删掉以便观察有没有新写入

	st.StartAutoFlush(10)
	time.Sleep(60 * time.Millisecond)
	if info := st.AutoFlushInfo(); info.Ticks < 1 {
		t.Fatalf("拍数应在上长，得到 %+v", info)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("没有改动时不该写盘")
	}
}

// TestStatsFlushIntervalFloor 稳态降频：每秒一跳的工作记账不得每 2~3 秒就全量重写
// 整份历史 + fsync 一次。
//
// 修复前的链路是“accountWorkTick → markDirty → 2 秒 AfterFunc → 回调先清 dirty 再
// Flush”，而每秒又有新的改动把 dirty 立起来 —— 稳态实测约每 3 秒一次全量写盘
// （8 小时工作日 ≈ 9600 次）。这里钉两件事：
//   - 距上一次**成功**落盘不足 10 秒时，防抖定时器被推到下限之后（不是 2 秒）；
//   - 10 秒窗口内实际 Flush 次数 ≤ 1（本用例里是 0：窗口起点刚落过一次盘）；
//   - 显式 Flush（退出收尾 / 清空统计走的路径）不受下限约束，立即生效。
func TestStatsFlushIntervalFloor(t *testing.T) {
	path := t.TempDir() + "/stats.json"
	st := NewStatsStore(func() string { return path })
	st.Load()
	defer st.Stop()

	st.Flush() // 基准：把“上次成功落盘”钉在现在
	base := st.AutoFlushInfo()
	if base.Flushes != 1 {
		t.Fatalf("基准落盘应计 1 次，实际 %d", base.Flushes)
	}
	if base.FloorMs != statsFlushFloorMs {
		t.Fatalf("自检信息应交代下限，实际 %d ms", base.FloorMs)
	}

	// 模拟稳态的 10 秒：每秒一跳工作记账（循环本身只花几毫秒，时间用手工区间表达）
	now := st.nowFn()
	for i := int64(1); i <= 10; i++ {
		st.accountWorkTick(now+(i-1)*1000, now+i*1000)
	}
	info := st.AutoFlushInfo()
	if n := info.Flushes - base.Flushes; n > 1 {
		t.Fatalf("10 秒窗口内落盘 %d 次（应 ≤1：稳态降频失效）", n)
	}
	if info.Flushes != base.Flushes {
		t.Fatalf("10 秒窗口内不该因每秒一跳而落盘，实际多了 %d 次", info.Flushes-base.Flushes)
	}
	// 定时器必须被推到下限之后 —— 这是“稳态从 3 秒拉到 ~10 秒”的直接证据
	if info.DueInMs < statsFlushFloorMs-1500 {
		t.Fatalf("下一次落盘应被推到 ≈%d ms 之后，实际 %d ms（仍按 2 秒防抖在跑）",
			statsFlushFloorMs, info.DueInMs)
	}

	// 真等 2.5 秒：旧实现的 2 秒防抖此时必然已经写盘
	time.Sleep(2500 * time.Millisecond)
	if got := st.AutoFlushInfo().Flushes; got != base.Flushes {
		t.Fatalf("距上次落盘不足 10 秒就写了盘（稳态仍是 2~3 秒一次），实际 %d 次", got-base.Flushes)
	}

	// 收尾路径不受下限约束：进程退出时必须能立即落盘
	st.Flush()
	if got := st.AutoFlushInfo().Flushes; got != base.Flushes+1 {
		t.Fatalf("显式 Flush 应立即生效（退出收尾依赖它），实际 %d 次", got-base.Flushes)
	}
}

// 睡眠 / 唤醒：睡眠整段算“离开”，醒来后工作段从零起算。
func TestNotifySuspendAndResumeCountAway(t *testing.T) {
	path := t.TempDir() + "/stats.json"
	st := NewStatsStore(func() string { return path })
	now := int64(1_760_000_000_000)
	st.nowFn = func() int64 { return now }
	st.workSegStartAt = now

	st.NotifySuspend()
	now += 30 * 60 * 1000 // 睡了 30 分钟
	st.NotifyResume()

	d := st.Today()
	if d.AwayMs < 30*60*1000 {
		t.Fatalf("睡眠整段应记成离开，得到 %+v", d)
	}
	if d.WorkMs != 0 {
		t.Fatalf("睡眠期间不该记工作，得到 %+v", d)
	}
}

// 工作累加器：1 秒一拍，把 [上一拍, 现在) 记成工作。
// 用真实时钟跑两拍 —— 这是唯一能证明“计时循环本身没坏”的办法。
func TestStartWorkAccumulatorCountsWork(t *testing.T) {
	path := t.TempDir() + "/stats.json"
	st := NewStatsStore(func() string { return path })
	st.Load()
	defer st.Stop()

	st.StartWorkAccumulator(func() bool { return false }, func() bool { return false })
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if d := st.Today(); d.WorkMs > 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("两拍之后工作时长应开始累计，得到 %+v", st.Today())
}
