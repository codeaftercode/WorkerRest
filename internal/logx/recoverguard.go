package logx

import (
	"log"
	"os"
	"path/filepath"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"workerrest/internal/notifier"
)

// headless 无头 / 探针模式：true 时熔断不弹窗（会卡住自动化），直接以非零码退出。
// 由 main 包在启动早期根据 serverMode 调用 SetHeadless 注入 —— serverMode 是
// main 包的 build-tag 常量，logx 不能反向 import main，故用注入式标志。
var headless atomic.Bool

// SetHeadless 设置无头模式（server 构建 / 探针调用）。熔断时据此决定是否弹错误框。
func SetHeadless(v bool) { headless.Store(v) }

// 后台 goroutine 的 panic 兜底。
//
// 为什么必须有这一层：Wails v3 只保护**绑定方法**（pkg/application/bindings.go 把
// panic 转成 CallError，前端 reject、进程存活 —— 这点与 v2 不同，不能套用 v2 的结论）。
// 但本项目有十几条自建 goroutine：调度 tick（500 ms）、空闲 / 勿扰 / 应用占用监视、
// 托盘刷新、外卖检查、统计落盘、自动更新排期…… 它们不在框架保护范围内。任何一个
// nil deref 都会让 Go 运行时直接终止进程，而发布版是 -H windowsgui 子系统：
// 用户看到的就是“窗口和托盘图标一起消失”，没有任何提示，日志里也什么都没有。
//
// 三层防护：
//  1. guardRun  一轮后台逻辑：panic 只丢这一轮，记日志后继续跑；
//  2. 熔断      同一处短时间内连炸 → 状态已不可信（典型：panic 落在 Lock 与 Unlock
//     之间，互斥量没释放，后续 tick 全卡在 Lock 上；这时进程活着但什么都不动，
//     比崩溃更难查）。主动落盘 + 弹一次错误框 + 退出，不留在假死态；
//  3. main 顶层 recover（见 main.go）兜住主 goroutine。
//
// ⚠️ 约定：临界区里不放会 panic 的业务调用。recover 无法代你解锁（sync.Mutex 没有
// “外部释放”），panic 落在锁段中间时这一层救不回来 —— 那种情况由熔断兜住。锁段保持
// “只读字段 / 只赋值”的短小形状，是这套兜底能成立的前提。

const (
	// panicBurstWindow 判定“连炸”的时间窗：两轮 tick 的正常间隔远小于它
	panicBurstWindow = 2 * time.Second
	// PanicBurstLimit 窗口内达到这个次数就熔断
	PanicBurstLimit = 3
	// panicFullStackGap 同一处再次打印完整栈的最小间隔 —— 循环里每 500 ms 炸一次时，
	// 全栈会把滚动日志的 6 MiB 瞬间填满，反而把第一次的现场冲掉
	panicFullStackGap = 5 * time.Minute
	// PanicExitCode 熔断退出码：不能是 0（0 = 正常结束）
	PanicExitCode = 1
)

// panicTally 单个位置的 panic 计数（名字 → 统计）。自带锁，不与全局表锁混用。
type panicTally struct {
	mu       sync.Mutex
	total    int
	burst    int
	lastAt   time.Time
	lastFull time.Time
}

var (
	panicTalliesMu sync.Mutex
	panicTallies   = map[string]*panicTally{}
)

// GuardRun 跑一轮可能 panic 的后台逻辑。
//
// 用法：循环体每轮包一次（不要只包循环外层 —— 那样一次 panic 就让整条循环永久停摆）：
//
//	case <-t.C:
//	    logx.GuardRun("scheduler.tick", s.onTick)
//
// 也用于一次性 goroutine 的整体（panic 后 goroutine 结束，没有进程级后果）。
func GuardRun(name string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			guardPanic(name, r)
		}
	}()
	fn()
}

// GuardRunOK 与 GuardRun 相同，但告诉调用方这一轮是否完整跑完（false = 被 panic 打断）。
//
// 需要它的是“失败后立刻重试会变成连炸”的场景：例如自动更新检查 panic 后，外层若
// 直接 continue 就会在一秒内重来三次，正好撞上熔断阈值 —— 而一次网络解析异常远不到
// “不可恢复”的程度。拿到 false 后改成退避，才是对的处置。
func GuardRunOK(name string, fn func()) (ok bool) {
	ok = true
	defer func() {
		if r := recover(); r != nil {
			ok = false
			guardPanic(name, r)
		}
	}()
	fn()
	return ok
}

// panicDecision 一次 panic 的处置决定。
//
// 抽成纯函数是因为“熔断”那条路会 os.Exit —— 不能靠跑测试去验证它，
// 只能把决策与实际动作分开，单测把决策表打满。
type panicDecision struct {
	total int  // 这个位置累计第几次
	burst int  // 时间窗内的连续次数
	full  bool // 是否打印完整栈
	trip  bool // 是否熔断退出
}

// decidePanic 推进统计并给出处置。调用方需持有 t 的锁。
func decidePanic(t *panicTally, now time.Time) panicDecision {
	t.total++
	if now.Sub(t.lastAt) <= panicBurstWindow {
		t.burst++
	} else {
		t.burst = 1
	}
	t.lastAt = now

	d := panicDecision{total: t.total, burst: t.burst}
	d.trip = t.burst >= PanicBurstLimit
	// 第一次、隔了很久的复发、以及要熔断的那次：打完整栈。
	// 其余只累加 —— 循环里连续 panic 时，日志该留给第一次的现场。
	d.full = d.total == 1 || now.Sub(t.lastFull) >= panicFullStackGap || d.trip
	if d.full {
		t.lastFull = now
	}
	return d
}

// guardPanic 记录一次后台 panic；连炸则熔断退出。
func guardPanic(name string, r any) {
	t := panicTallyFor(name)

	t.mu.Lock()
	d := decidePanic(t, time.Now())
	t.mu.Unlock()

	if !d.full {
		if d.total%100 == 0 {
			log.Printf("[panic] %s 已累计 %d 次（首次现场见前文，栈已省略）", name, d.total)
		}
		return
	}

	log.Printf("[panic] 后台 goroutine %q 第 %d 次 panic: %v\n%s", name, d.total, r, debug.Stack())
	if d.trip {
		fatalPanicExit(name, d.burst)
	}
}

// panicTallyFor 取（必要时建）某个位置的统计。
func panicTallyFor(name string) *panicTally {
	panicTalliesMu.Lock()
	defer panicTalliesMu.Unlock()
	t := panicTallies[name]
	if t == nil {
		t = &panicTally{}
		panicTallies[name] = t
	}
	return t
}

// fatalPanicExit 连续 panic 后收尾：落盘 → 提示用户（GUI）/ 退出（无头）。
//
// 为什么不静默退出：静默退出正是本文件要消灭的现象。用户至少要看到一句人话
// 和日志位置，否则他只记得“软件自己关了”。
func fatalPanicExit(name string, burst int) {
	log.Printf("[panic] %s 在 %s 内连续 %d 次 panic，判定为不可恢复，退出进程（日志：%s）",
		name, panicBurstWindow, burst, logPathForUserMessage())

	if headless.Load() {
		// 无头 / 探针：不弹窗（会卡住自动化），但退出码要反映“崩了”
		os.Exit(PanicExitCode)
	}
	PromptPanicExit(name)
	os.Exit(PanicExitCode)
}

// PromptPanicExit 用系统原生消息框告知用户并指出日志位置。
//
// 复用 internal/notifier 的消息框封装：此时窗口状态可能已经不可信，
// 能可靠显示的只有系统原生框。
func PromptPanicExit(name string) {
	const title = "吃饼人也敢休息？— 遇到内部错误"
	// 不放栈、不放技术细节：用户要看的是“发生了什么、日志在哪、重开会不会好”。
	text := "程序内部出错，已停止运行。\r\n\r\n" +
		"错误位置：" + name + "\r\n" +
		"日志文件：" + logPathForUserMessage() + "\r\n\r\n" +
		"重新打开通常可以继续使用；若反复出现，把日志文件反馈给作者即可定位。"

	notifier.Error(title, text)
}

// logPathForUserMessage 日志文件的绝对路径（给用户看的那个）。
//
// 与 logging.go 的选择顺序一致：WR_LOG_FILE 优先，否则 <exe>/logs/app.log；
// 取不到时给空串（提示语里那一行会显示成空的，好过显示一个错的路径）。
func logPathForUserMessage() string {
	if p := os.Getenv("WR_LOG_FILE"); p != "" {
		return p
	}
	if dirs := appLogDirs(); len(dirs) > 0 {
		return filepath.Join(dirs[0], logFileName)
	}
	return ""
}
