package main

import (
	"testing"

	"workerrest/internal/windnd"
)

// 守卫每拍的决策表。
//
// 真机上没法让 Win32 在“前台是我 / 我被盖住”四种组合里听指挥，所以把判断抽成纯函数钉在
// 这里；真机行为由 probe_fs_ask.py 的「按钮坐标上确实是询问窗」那条硬判据兜（它不看日志，
// 直接问系统“这一点上是谁”）。
func TestFSGuardPlan(t *testing.T) {
	cases := []struct {
		name     string
		isSelf   bool
		occluded bool
		want     fsGuardOp
	}{
		{"一切正常：不动窗", false, false, fsGuardIdle},
		{"被别的 TOPMOST 窗盖住：只抬 Z 序", false, true, fsGuardRaise},
		{"前台被我们抢了：还回去", true, false, fsGuardRestore},
		// 抢前台比被盖住更严重：它会让这一段全屏在判据眼里结束，进而每 6 秒弹一次死循环。
		// Restore 分支里本来就会补一次 raiseNoActivate，所以这里不该额外再报 Raise。
		{"既抢了前台又被盖住：按抢前台处理", true, true, fsGuardRestore},
	}
	for _, c := range cases {
		if got := fsGuardPlan(c.isSelf, c.occluded); got != c.want {
			t.Errorf("%s：fsGuardPlan(isSelf=%v, occluded=%v)=%v，期望 %v",
				c.name, c.isSelf, c.occluded, got, c.want)
		}
	}
}

// 自家询问窗出现在“前台”里要当成没发生，沿用上一拍的真实记录。
//
// 这条是“每 6 秒弹一次”死循环的兜底：询问窗一旦被顶成前台，几何判定看到的是一扇 420×200
// 的窗 —— 照实采信就等于“这一段全屏结束了”，于是关窗、前台回到全屏、下一拍再弹。
func TestFSAskResolveGeoIgnoresSelf(t *testing.T) {
	real := windnd.ForegroundInfo{Supported: true, HWND: 0x1234, Exe: "game.exe"}
	self := windnd.ForegroundInfo{Supported: true, HWND: 0x5678, IsSelf: true}

	// 常见路径：看到别人 → 采信它，并记成“上一拍的真实前台”
	got, update := fsAskResolveGeo(real, windnd.ForegroundInfo{}, false)
	if got.HWND != 0x1234 || !update {
		t.Fatalf("普通快照应被采信并写入缓存，得到 hwnd=%#x update=%v", got.HWND, update)
	}

	// 看到自己 → 用缓存顶替，且**不**把自家窗写进缓存
	got, update = fsAskResolveGeo(self, real, true)
	if got.HWND != 0x1234 {
		t.Fatalf("自家窗应沿用上一拍的真实前台（game.exe），得到 hwnd=%#x exe=%q", got.HWND, got.Exe)
	}
	if update {
		t.Fatal("自家窗不该被写进缓存：写进去之后下一拍连真实前台都没了")
	}

	// 冷启动第一拍就撞上自家窗（没有缓存可退）：只能原样交出去，同样不污染缓存
	got, update = fsAskResolveGeo(self, windnd.ForegroundInfo{}, false)
	if !got.IsSelf || update {
		t.Fatalf("无缓存时自家窗应原样交出且不写缓存，得到 isSelf=%v update=%v", got.IsSelf, update)
	}

	// 读不到前台：原样交出，不写缓存（Supported=false 有自己的语义，不能被当成“真实前台”）
	unsupported := windnd.ForegroundInfo{Supported: false}
	got, update = fsAskResolveGeo(unsupported, real, true)
	if got.Supported || update {
		t.Fatalf("不可用的快照应原样交出且不写缓存，得到 supported=%v update=%v", got.Supported, update)
	}
}

// 监视循环每 3 秒调一次的入口，在没有原生窗口的环境里必须安全返回。
//
// 它跑在常驻 goroutine 里（sys_watch 的监视链），panic 就是整个进程消失、用户连提示
// 都看不到。这条用例不看具体行为，只看“跑得通且不乱改状态”。
func TestFSAskTickSafeWithoutWindow(t *testing.T) {
	a, s := newWatchApp(t, shortCountdown)
	// busy=false：状态机走“条件不成立”，不会尝试建窗（测试环境本来也建不出来）
	a.fsAskTick(false)
	a.fsAskTick(false)
	if st := s.StateCopy(); st.Paused {
		t.Fatalf("busy=false 两拍就暂停了：%+v", st.PauseReasons)
	}
	a.fsAskMu.Lock()
	pending := a.fsAsk.pending
	a.fsAskMu.Unlock()
	if pending {
		t.Fatal("busy=false 不该留下一个 pending 的询问")
	}
}

// 拿不到句柄 / 拿不到原前台时不许启动守卫。
//
// 无头环境（server）、或前台恰好看不到时会出现；真启动了就会对着无效句柄每 120ms 调一次
// SetWindowPos（还把 prev=0 交给 SetForegroundWindow）。
func TestGuardNotStartedWithoutHandles(t *testing.T) {
	a := &App{}
	a.startFSAskForegroundGuard(0, 0)
	if a.fsFgStop != nil {
		t.Fatal("self=0 时不该启动守卫")
	}
	a.startFSAskForegroundGuard(0x1234, 0)
	if a.fsFgStop != nil {
		t.Fatal("prev=0 时不该启动守卫：还原前台会把窗口交给 0 号句柄")
	}
	// 停两次不能 panic（窗关闭与退出清理都会调）
	a.stopFSAskForegroundGuard()
	a.stopFSAskForegroundGuard()
}
