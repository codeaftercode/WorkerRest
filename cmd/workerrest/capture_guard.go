package main

import "github.com/wailsapp/wails/v3/pkg/application"

// syncCaptureGuardToLive 把“共享屏幕时隐藏提醒窗口”的当前值同步到**已经在显示**的提醒窗口。
//
// 正常路径用不上它：提醒窗口每次弹出都是新建的，建窗那一刻就把开关读进
// ContentProtectionEnabled 了。这条只补一个边角 —— 提醒窗正显示时用户跑到设置页
// 把开关改掉，当前这扇窗会一直保持旧状态直到底层窗口销毁，与复选框“改一下立刻
// 生效”的语义不符。
//
// 三类窗口都要覆盖，含提前提示（#prebreak）与外卖（takeoutWins）：它们与休息窗口
// 是各自独立的建窗路径，漏一个就会出现“休息窗隐藏了、提前提示窗还露着”。
//
// 调用方：SaveSettings 里确认值真的变了之后（解锁之后再调，见那里的注释）。
func (a *App) syncCaptureGuardToLive() {
	hide := a.CfgSnapshot().AppearanceCfg().HideOnShare
	a.mu.Lock()
	wins := make([]*application.WebviewWindow, 0, len(a.breakWins)+len(a.takeoutWins)+1)
	wins = append(wins, a.breakWins...)
	wins = append(wins, a.takeoutWins...)
	if w := a.wins[preBreakWindowName]; w != nil {
		wins = append(wins, w)
	}
	a.mu.Unlock()

	for _, w := range wins {
		w.SetContentProtection(hide)
	}
}
