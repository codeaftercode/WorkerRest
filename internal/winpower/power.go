// Package winpower 检测“系统刚才睡过一觉”：醒来后靠 tick 跳变发现睡眠，据此把
// 休息计时重新对齐，避免一觉醒来发现休息早该结束了。
//
// 非 Windows 平台走 _other 桩，恒返回“没睡过”。
package winpower

// 系统睡眠检测：用“唤醒 tick”的跳变在**醒来后**检测“刚才睡过”。
//
// 为什么用 tick 跳变而不是事件回调：Wails v3 没有暴露 WM_POWERBROADCAST 之类的
// 电源事件源（上一版有 powerMonitor.getSystemIdleTime / suspend-resume 事件）。
// 而睡眠期间进程被挂起、定时器停摆，无法在入睡那一刻执行任何代码 —— 唯一可靠的
// “事后检测”依据是：墙钟（time.Now）跳了很远，但唤醒 tick（**不含睡眠时间**，
// Windows 上是 GetTickCount64，其它平台用单调时钟近似）只跳了一点点，两者差值就是睡眠时长。
//
// 检测放在工作累加器的每秒 tick 里（见 stats.go 的 StartWorkAccumulator），醒来后
// 第一跳就会触发，天然对齐“工作段从醒来重新起算”的口径。
//
// 本文件是平台中立部分：纯函数 DetectSleep + 阈值常量。
// 平台相关的“读唤醒 tick”在 power_windows.go / power_other.go。

// sleepThresholdMs 判定“发生过睡眠”的“墙钟跳变 - tick 跳变”差值下限。
// 设 60 秒：把 NTP 校时、手动改时间这类秒级跳变挡在门外，只认真正的睡眠 / 休眠。
const sleepThresholdMs = 60 * 1000

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
