package main

import (
	"strings"
	"testing"

	"workerrest/internal/scheduler"
)

// 调度器停摆时托盘必须“看得见”：文案换成异常提示，恢复后回到正常文案。
//
// 这五行的价值在于它把一类**没有任何报错**的失效（ticker 还在转、onTick 卡死）
// 变成用户能察觉的现象，所以单独钉一次。
func TestTrayHeartbeatTexts(t *testing.T) {
	const tip, status = "约 10 分钟后短休息", "下次短休息：约 10 分钟后"

	// 阈值内：原样返回，不置 stale
	if gotTip, gotStatus, stale := trayHeartbeatTexts(tip, status, scheduler.HeartbeatStaleMs); gotTip != tip || gotStatus != status || stale {
		t.Fatalf("阈值内不该改文案，得到 %q / %q / stale=%v", gotTip, gotStatus, stale)
	}

	// 超过阈值：文案换成带秒数的异常提示
	gotTip, gotStatus, stale := trayHeartbeatTexts(tip, status, scheduler.HeartbeatStaleMs+3_400)
	if !stale {
		t.Fatal("超过阈值应判为停摆")
	}
	if !strings.Contains(gotTip, "停摆 8 秒") || !strings.Contains(gotTip, "可能失效") {
		t.Fatalf("tooltip 应说明停摆秒数与后果，得到 %q", gotTip)
	}
	if !strings.Contains(gotStatus, "停摆 8 秒") {
		t.Fatalf("状态行应显示停摆秒数，得到 %q", gotStatus)
	}
}
