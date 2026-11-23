package main

import (
	"log"
	"time"

	"workerrest/internal/logx"
)

// 询问窗的“前台守卫”（2026-11-21）。
//
// 为什么不靠 WS_EX_NOACTIVATE 就够：那一位只保证**用户点击 / ShowWindow** 不会激活窗口，
// 挡不住显式的 SetForegroundWindow。而 Wails 在 wndproc 的 WM_SETFOCUS 分支里会调
// `focus()`，其第一行就是 `SetForegroundWindow(w.hwnd)`（webview_window_windows.go 的
// `func (a *windowsWebviewWindow) focus()`）。WebView2 初始化完成时若给自己的渲染子窗
// SetFocus，父窗就会收到 WM_SETFOCUS —— 于是询问窗把前台抢走了。
//
// 后果不是“难看”而是功能坏掉：QUNS 按“前台窗口铺满整屏”判定，抢走前台后几何层看到的是
// 一扇 420×200 的窗 → 这一段全屏在软件眼里结束 → 段被 reset → 关窗 → 前台回到全屏 →
// 又弹。实测就是每 6 秒弹一次、永不结案的循环（probe_fs_ask.py 的 B 段 3/3 复现）。
//
// 守卫做两件事，每 120ms 采样一次再决定：前台若是我们自己，立刻还回去；Z 序若被压在下面，
// 不激活地重新抬到 TOPMOST 组顶部。两件事都**只在需要时才做**——无条件抬会每秒白发两次
// SetWindowPos（每次都广播 WM_WINDOWPOSCHANGED），而真正被压住时要等更久才恢复。
//
// 第二件事不是“顺手加保险”，是实测出来的另一个真问题：全屏应用本身常常也是
// WS_EX_TOPMOST（探针的靶子窗、无边框游戏、播放器全屏都是），而**同为 TOPMOST 时后抬者在上**。
// 只要那扇窗把前台拿回去（或自己再 SetWindowPos 一次），询问窗就被整个盖住 —— 用户看不见
// 也点不到，点击直接穿透到下面的全屏内容上。实测表现是“点按钮时灵时不灵”（探针 C/D 段
// 命中 WindowFromPoint 返回的是靶子窗而不是 WebView2 渲染窗）。抬高也不激活，所以不会
// 把焦点从全屏应用手里抢回来。

const (
	fsFgGuardInterval = 120 * time.Millisecond
)

// fsGuardOp 守卫每一拍要做的动作。
type fsGuardOp int

// wrRect 对应 Win32 RECT。单位与 WindowFromPoint 的坐标系同源（同一个进程里两者一致，
// 不受 DPI 虚拟化影响 —— 这一点很关键，别一个用物理一个用逻辑）。
type wrRect struct {
	Left, Top, Right, Bottom int32
}

const (
	fsGuardIdle    fsGuardOp = iota // 一切正常：不动窗（不发 WM_WINDOWPOSCHANGED）
	fsGuardRestore                  // 前台被我们抢走了：还回去 + 顺手抬 Z 序
	fsGuardRaise                    // 被别的 TOPMOST 窗盖住了：只抬 Z 序，不激活
)

// fsGuardPlan 每拍的决策（抽成纯函数才能测：真机上没法让 Win32 在“前台是我 / 我被盖住”
// 四种组合里听指挥）。
//
// 优先级：抢前台比被盖住更严重（抢前台会让这一段全屏在判据眼里结束，引发每 6 秒一轮的
// 死循环），所以 isSelf 一成立就只做 Restore —— Restore 之后本来就要抬一次，两个动作
// 合成的。
func fsGuardPlan(isSelf, occluded bool) fsGuardOp {
	switch {
	case isSelf:
		return fsGuardRestore
	case occluded:
		return fsGuardRaise
	default:
		return fsGuardIdle
	}
}

// startFSAskForegroundGuard 启动守卫。self = 询问窗 HWND，prev = 这一段全屏应用的窗口
// 句柄（判定层的 geo.HWND，**不是**建窗那一刻的 GetForegroundWindow —— 后者不可靠）。
// 调用方持有 fsAskWinMu —— 这里只再拿 fsFgMu，锁序是 fsAskWinMu → fsFgMu。
func (a *App) startFSAskForegroundGuard(self, prev uintptr) {
	a.stopFSAskForegroundGuard()
	if self == 0 || prev == 0 {
		// 拿不到句柄/原前台（无头环境、前台恰好看不到）：不启动，判据层那道兜底仍在
		if self != 0 {
			log.Printf("[fsAsk] 前台守卫未启动：原前台未知（self=0x%X）", self)
		}
		return
	}
	stop := make(chan struct{})
	a.fsFgMu.Lock()
	a.fsFgStop = stop
	a.fsFgMu.Unlock()
	go func() {
		t := time.NewTicker(fsFgGuardInterval)
		defer t.Stop()
		announced := false
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				// 每轮一次 panic 兜底（guard_guard_test.go 的防回归线）：
				// 这条循环里只有几个 Win32 调用，但它是**常驻**的 —— 一旦 panic
				// 就是整个进程消失，而用户什么提示都看不到。
				logx.GuardRun("fsask.foreground", func() {
					// 每拍先采样再决定：只被盖住时才抬（判定见 fsAskOccluded）。
					switch fsGuardPlan(foregroundWindow() == self, fsAskOccluded(self)) {
					case fsGuardRestore:
						ok := restoreForeground(prev)
						// 还原前台会把全屏应用顶到 Z 序最上，询问窗就被盖住了 —— 必须补一下
						// 「提回 TOPMOST 顶但不激活」。两步都要，缺一个就是“看不见”或“点不到”。
						raiseNoActivate(self)
						if ok && !announced {
							// 只报一次：反复报会把日志刷满，而这件事本身应该极少发生
							announced = true
							log.Printf("[fsAsk] 询问窗被顶成前台，已还给全屏应用（0x%X）", prev)
						}
					case fsGuardRaise:
						raiseNoActivate(self)
					}
				})
			}
		}
	}()
}

// stopFSAskForegroundGuard 停掉守卫（幂等）。窗关了就不该再动前台。
func (a *App) stopFSAskForegroundGuard() {
	a.fsFgMu.Lock()
	if a.fsFgStop != nil {
		close(a.fsFgStop)
		a.fsFgStop = nil
	}
	a.fsFgMu.Unlock()
}
