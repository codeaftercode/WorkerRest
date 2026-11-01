package winpower

import "testing"

// 睡眠检测纯函数（DetectSleep）的回归。
//
// 核心口径：墙钟跳变 - 唤醒 tick 跳变 = 睡眠时长。正常时两者相等（差 0）；
// 睡眠时墙钟跳了很远、tick 只跳一点；差值超过 60 秒才算「睡过」，否则把 NTP 校时、
// 手动改时间这类秒级跳变挡在门外。

func TestDetectSleep(t *testing.T) {
	cases := []struct {
		name       string
		wall, tick int64
		want       int64
	}{
		{"正常一秒", 1000, 1000, 0},
		{"墙钟略快于 tick（系统负载抖动）", 1300, 1000, 0},
		{"刚过阈值", sleepThresholdMs + 1, 0, sleepThresholdMs + 1},
		{"恰好阈值（不算睡过）", sleepThresholdMs, 0, 0},
		{"合盖一小时", 3600_000, 500, 3599_500},
		{"手动把时钟往回拨（负差不算）", -5000, 1000, 0},
	}
	for _, c := range cases {
		if got := DetectSleep(c.wall, c.tick); got != c.want {
			t.Errorf("%s: DetectSleep(%d, %d) = %d，应为 %d", c.name, c.wall, c.tick, got, c.want)
		}
	}
}
