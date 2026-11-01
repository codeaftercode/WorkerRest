package logx

import (
	"testing"
	"time"
)

// GuardRun 必须把 panic 吃掉并让后面的代码照常跑 —— 这是「后台 goroutine 一条 nil deref
// 就让 GUI 静默消失」的直接对策。跑在循环体里时，它保证的是「丢一拍，不丢整条循环」。
func TestGuardRunRecoversAndContinues(t *testing.T) {
	after := false
	func() {
		defer func() {
			// GuardRun 内部已经 recover 过；若它漏了，这里会再接到 panic → 测试失败
			if r := recover(); r != nil {
				t.Fatalf("GuardRun 没兜住 panic: %v", r)
			}
		}()
		GuardRun("test.guardRun", func() {
			var p *int
			_ = *p // nil deref，等价于线上那种「一处空指针」
		})
		after = true
	}()
	if !after {
		t.Fatal("GuardRun 返回后，调用点后面的代码应该照常执行")
	}
}

// 需要知道这一轮是否被打断的调用方（自动更新排期）靠返回值决定要不要退避。
func TestGuardRunOKReportsInterruption(t *testing.T) {
	if ok := GuardRunOK("test.ok", func() {}); !ok {
		t.Fatal("正常跑完应返回 true")
	}
	if ok := GuardRunOK("test.ok", func() { panic("boom") }); ok {
		t.Fatal("被 panic 打断应返回 false")
	}
}

// 决策表逐格验证。熔断会 os.Exit，所以「该不该熔断」只能在纯函数层面测。
func TestDecidePanic(t *testing.T) {
	base := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	tal := &panicTally{}

	// 第 1 次：打全栈，不熔断
	d := decidePanic(tal, base)
	if !d.full || d.trip || d.total != 1 || d.burst != 1 {
		t.Fatalf("首次应 full=true trip=false，得到 %+v", d)
	}

	// 窗内第 2 次：不全栈（首次现场已经打过，重复打会把日志滚走），还没到熔断阈值
	d = decidePanic(tal, base.Add(500*time.Millisecond))
	if d.full || d.trip || d.burst != 2 {
		t.Fatalf("第 2 次应 full=false trip=false burst=2，得到 %+v", d)
	}

	// 窗内第 3 次：达到阈值 → 熔断，且这一跳要留全栈
	d = decidePanic(tal, base.Add(time.Second))
	if !d.trip || !d.full || d.burst != PanicBurstLimit {
		t.Fatalf("连续 %d 次应熔断且带全栈，得到 %+v", PanicBurstLimit, d)
	}

	// 隔了 10 秒再来：不算连炸（burst 归 1），但离上次全栈不到 5 分钟 → 不打全栈
	d = decidePanic(tal, base.Add(10*time.Second))
	if d.trip || d.full || d.burst != 1 {
		t.Fatalf("超出时间窗应重新计数且不熔断，得到 %+v", d)
	}

	// 超过 5 分钟：复发也值得再看一次栈（可能换了原因）
	d = decidePanic(tal, base.Add(panicFullStackGap+time.Minute))
	if !d.full || d.trip {
		t.Fatalf("隔了 %s 应重新打全栈，得到 %+v", panicFullStackGap, d)
	}
	if d.total != 5 {
		t.Fatalf("累计次数应连续累加，得到 %d", d.total)
	}
}

// 统计表按名字隔离：调度器连炸不该把托盘刷新的计数带起来。
func TestPanicTallyIsolatedByName(t *testing.T) {
	panicTalliesMu.Lock()
	panicTallies = map[string]*panicTally{}
	panicTalliesMu.Unlock()

	a, b := panicTallyFor("a"), panicTallyFor("b")
	if a == b {
		t.Fatal("不同名字必须拿到不同统计对象")
	}
	if panicTallyFor("a") != a {
		t.Fatal("同名必须复用同一个对象")
	}
}

// 提示语里的日志路径不能是空的：GUI 下用户唯一能提供的现场就是它。
func TestLogPathForUserMessage(t *testing.T) {
	t.Setenv("WR_LOG_FILE", "")
	if p := logPathForUserMessage(); p == "" {
		t.Fatal("应能推出默认日志路径（<exe>/logs/app.log）")
	}
	t.Setenv("WR_LOG_FILE", `C:\tmp\x.log`)
	if p := logPathForUserMessage(); p != `C:\tmp\x.log` {
		t.Fatalf("WR_LOG_FILE 应优先，得到 %q", p)
	}
}

// 单次 panic 不得触发熔断：偶发一条脏数据不该让用户的软件退出。
func TestSinglePanicDoesNotTrip(t *testing.T) {
	GuardRun("test.single", func() { panic("once") })
	// 走到这里就说明没有 os.Exit
	t.Log("单次 panic 已兜住，进程继续")
}
