package winpower

import "syscall"

// 系统睡眠检测：用 GetTickCount64 的跳变在**醒来后**检测「刚才睡过」。
//
// 为什么用 tick 跳变而不是事件回调：Wails v3 没有暴露 WM_POWERBROADCAST 之类的
// 电源事件源（上一版有 powerMonitor.getSystemIdleTime / suspend-resume 事件）。
// 而睡眠期间进程被挂起、定时器停摆，无法在入睡那一刻执行任何代码 —— 唯一可靠的
// 「事后检测」依据是：墙钟（time.Now）跳了很远，但 GetTickCount64（**不含睡眠时间**）
// 只跳了一点点，两者差值就是睡眠时长。
//
// 检测放在工作累加器的每秒 tick 里（见 stats.go 的 StartWorkAccumulator），醒来后
// 第一跳就会触发，天然对齐「工作段从醒来重新起算」的口径。

var (
	modKernel32        = syscall.NewLazyDLL("kernel32.dll")
	procGetTickCount64 = modKernel32.NewProc("GetTickCount64")
)

// sleepThresholdMs 判定「发生过睡眠」的「墙钟跳变 - tick 跳变」差值下限。
// 设 60 秒：把 NTP 校时、手动改时间这类秒级跳变挡在门外，只认真正的睡眠 / 休眠。
const sleepThresholdMs = 60 * 1000

// SysWakeTick 读系统自开机以来的毫秒数（GetTickCount64，**不含睡眠 / 休眠时间**）。
// 第二个返回值表示是否读到。kernel32 的 GetTickCount64 永不失败，这里恒为 true；
// 保留布尔返回是为了与 idleState 的 tickFn 保持同一形状，方便单测注入假源。
func SysWakeTick() (int64, bool) {
	r, _, _ := procGetTickCount64.Call()
	return int64(r), true
}

// DetectSleep 判定一次采样是否跨过了睡眠：返回睡眠时长（>0 表示睡过）。
// wallDelta 是墙钟跳变（毫秒），tickDelta 是唤醒 tick 跳变（毫秒）。正常时两者相等；
// 睡眠时墙钟跳了很远、tick 只跳一点，差值就是睡眠时长。
// 抽成纯函数是为了能脱离时钟与 Win32 做表驱动单测。
func DetectSleep(wallDelta, tickDelta int64) int64 {
	d := wallDelta - tickDelta
	if d > sleepThresholdMs {
		return d
	}
	return 0
}
