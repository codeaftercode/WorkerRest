package main

import (
	"log"
	"time"
	"workerrest/internal/display"
	"workerrest/internal/logx"
	"workerrest/internal/model"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// ---------- 窗口（对应 takeoutWindow.ts + displays.ts） ----------

// targetDisplays 按“提醒窗口显示在哪几块屏”选出要显示的显示器（休息窗口与外卖窗口共用）。
//
// 配置里存的是 display.Info.ID（EDID 设备路径压出来的稳定标识），**不是 Wails 的 Screen.ID**
// —— 后者是 HMONITOR 句柄的十进制串，进程重启就变，写进配置等于没写。
// 两边用**显示器矩形**（Bounds，物理像素）对齐，对不上的跳过；一块都对不上时回退主屏，
// 不会出现“一个窗口都不弹”。
//
// ⚠️ 对齐必须用 Bounds，不能用 WorkArea：WorkArea 扣掉了任务栏（主屏高度 1440 → 1392），
// 而 display.Info 是整块屏的源模式尺寸，四边永远对不上。2026-11-04 双屏真机实测：
// 用 WorkArea 比的时候，选主屏是靠“全部失配 → 回退主屏”蒙对的，选副屏则静默落到主屏
// —— 用户勾了副屏却永远只看到主屏弹窗，日志里一句异常都没有。
//
// 空配置 = 只主屏（不是所有屏）：允许“空 = 全部”等于让新接入的屏自动进入显示范围，
// 接上投影仪的那一刻提醒就会弹到投影上。
func (a *App) targetDisplays() []*application.Screen {
	all := a.app.Screen.GetAll()
	if len(all) == 0 {
		return nil
	}
	ids := a.CfgSnapshot().DisplayIDs()
	infos, err := display.List()
	if err != nil || len(infos) == 0 {
		// 枚举不可用（远程会话 / 无桌面）：按主屏处理，与 Resolve 的空配置兜底同语义
		return a.primaryScreen(all)
	}
	picked := display.Resolve(ids, infos)
	out := make([]*application.Screen, 0, len(picked))
	seen := make(map[string]bool, len(picked)) // 按 Wails 的屏 ID 去重，见下
	var unmatched []string
	for _, want := range picked {
		sc := matchScreen(all, want)
		if sc == nil {
			// 选中的屏在 Wails 那边找不到对应窗口（DPI 虚拟化 / 驱动报的矩形有偏差）：
			// 记一笔，别让“勾了没生效”变成不可诊断的静默失败
			unmatched = append(unmatched, want.Label+"("+want.ID+")")
			continue
		}
		// 两块 Info 落到同一扇 Wails 屏时只建一个窗：复制模式下矩形会重合，
		// 不去重就会在同一位置叠两个全屏窗（一个盖一个，还会闪一下）。
		if !seen[sc.ID] {
			seen[sc.ID] = true
			out = append(out, sc)
		}
	}
	if len(unmatched) > 0 {
		log.Printf("[display] 选中的 %d 块屏在 Wails 侧找不到对应屏幕，已跳过：%v", len(unmatched), unmatched)
	}
	if len(out) == 0 {
		// 矩形口径对不上（DPI 虚拟化、驱动改了时序）→ 至少弹一个，绝不静默什么都不显示
		log.Printf("[display] 所有选中屏都未匹配，回退主屏（勾选=%v，枚举到 %d 块）", ids, len(infos))
		return a.primaryScreen(all)
	}
	return out
}

// matchScreen 把一块 display.Info 对到 Wails 的 Screen 上。
//
// 一档比矩形四边全等；二档退成“中心点落在谁的矩形里”，容得下驱动报的 1~2 px 偏差。
// 两块屏相邻时中心点离边界几百像素，二档不会认错人。
func matchScreen(all []*application.Screen, want display.Info) *application.Screen {
	for _, sc := range all {
		if rectEq(sc.Bounds, want) || rectEq(sc.PhysicalBounds, want) {
			return sc
		}
	}
	cx, cy := want.X+want.Width/2, want.Y+want.Height/2
	for _, sc := range all {
		if containsPoint(sc.Bounds, cx, cy) || containsPoint(sc.PhysicalBounds, cx, cy) {
			return sc
		}
	}
	return nil
}

func rectEq(r application.Rect, d display.Info) bool {
	return r.X == d.X && r.Y == d.Y && r.Width == d.Width && r.Height == d.Height
}

func containsPoint(r application.Rect, x, y int) bool {
	return x >= r.X && x < r.X+r.Width && y >= r.Y && y < r.Y+r.Height
}

// primaryScreen 主屏兜底；Wails 拿不到主屏时退回第一块。
func (a *App) primaryScreen(all []*application.Screen) []*application.Screen {
	if sc := a.app.Screen.GetPrimary(); sc != nil {
		return []*application.Screen{sc}
	}
	if len(all) > 0 {
		return all[:1]
	}
	return nil
}

// showTakeoutWindows 弹出外卖提醒窗口（每个目标屏各一个）。无头（server 模式）下不建窗，
// 但会话照常构建，于是无头冒烟能验证“触发 → 会话 JSON → 动作记账”整条链路。
func (a *App) showTakeoutWindows(slot model.TimeSlot, preview bool, postponedCount int) {
	a.showTakeoutWindowsAt(slot, preview, postponedCount, 0)
}

// showTakeoutWindowsAt 同上，附带 occurrence 时刻（0 = 按当天该时刻）。
func (a *App) showTakeoutWindowsAt(slot model.TimeSlot, preview bool, postponedCount int, atMs int64) {
	info := a.buildTakeoutInfo(slot, preview, postponedCount, atMs)

	if serverMode || a.app == nil {
		a.mu.Lock()
		a.takeoutCurrent = info
		a.mu.Unlock()
		if serverMode {
			log.Printf("[takeout] server 模式无原生窗口，跳过创建（会话已就绪 %s）", info.Meal)
		}
		return
	}

	// 建窗要几百毫秒：串行化，避免连点“预览”时两批窗口互相覆盖（野窗）
	a.takeoutWinMu.Lock()
	defer a.takeoutWinMu.Unlock()

	// 旧窗口（上一次预览，或上一轮真实提醒留下的多屏窗）必须先关：下面要用新列表
	// 整批替换 a.takeoutWins，不关的话旧窗引用就此丢掉，屏幕上那扇窗再也没有代码能
	// 关它。不播结束提示音 —— 这是被同一次操作顶替，不是提醒正常结束。
	stale := a.replaceTakeoutWins(nil, info)
	for _, w := range stale {
		w.Close()
	}
	if len(stale) > 0 {
		log.Printf("[takeout] 关掉被顶替的旧窗口 %d 个（否则引用丢失 → 关不掉的野窗）", len(stale))
	}

	cfg := a.CfgSnapshot()
	ap := cfg.AppearanceCfg()
	displays := a.targetDisplays()
	var wins []*application.WebviewWindow
	for _, display := range displays {
		// 与休息窗同一个函数：外观是同一份配置，只能有一种解读。
		// （以前这里自己算，`全屏` 只挪位置不改尺寸，会留出右下角的空隙。）
		x, y, w, h := reminderBoundsFor(display.WorkArea, ap)
		win := a.app.Window.NewWithOptions(application.WebviewWindowOptions{
			Title:         "外卖提醒",
			X:             x,
			Y:             y,
			Width:         w,
			Height:        h,
			Frameless:     true,
			AlwaysOnTop:   ap.AlwaysOnTop,
			DisableResize: true,
			// 不设它 X/Y 会被无视：Wails 的 InitialPosition 零值 = 整窗居中
			InitialPosition: application.WindowXY,
			// 共享屏幕时隐藏（同“提醒窗口 → 共享屏幕时隐藏提醒窗口”）：
			// 外卖提醒里也有“该点外卖啦”这类文案，被同事看见一样尴尬
			ContentProtectionEnabled: ap.HideOnShare,
			URL:                      "/takeout.html?win=takeout",
			BackgroundType:           application.BackgroundTypeTranslucent,
			BackgroundColour:         application.NewRGBA(0, 0, 0, 0),
			// 窗口身份经 URL 的 ?win=takeout 下发；JS 字段对 URL 页面不生效（仅占位）
			JS: `window.__WR_WIN = "takeout";`,
			Windows: application.WindowsWindow{
				DisableFramelessWindowDecorations: true,
				HiddenOnTaskbar:                   true,
			},
		})
		// 窗口被系统 / 用户直接关掉（Alt+F4）时清掉引用
		win.OnWindowEvent(events.Common.WindowClosing, func(*application.WindowEvent) {
			a.removeTakeoutWindow(win)
		})
		wins = append(wins, win)
	}
	// 建窗这几百毫秒里提醒可能已经被结束（用户点“知道了”/ 退出 / 新一轮顶替）：
	// 会话没了就别挂窗口，否则屏幕上留一扇“没有会话”的空窗 —— 点按钮会立刻被关掉，
	// 但用户看到的是一个莫名其妙的空白框，且它不在任何集合里，只能靠点击自己退场。
	a.mu.Lock()
	stillWanted := a.takeoutCurrent != nil && a.takeoutCurrent.ID == info.ID
	a.mu.Unlock()
	if !stillWanted {
		for _, w := range wins {
			w.Close()
		}
		log.Printf("[takeout] 建窗期间提醒已结束，本轮建出的 %d 个窗口直接收掉", len(wins))
		return
	}
	// 登记新一批窗口。上面已经把旧集合摘空，这里拿到的 old 必然为空；
	// 仍走同一个函数，是为了让“替换必须吐出旧窗口”这条只有一处的实现。
	a.replaceTakeoutWins(wins, nil)

	// 抬升：降级成置顶 + 聚焦（见文件头注释）。用户点“知道了”类路径不经过这里。
	if ap.AlwaysOnTop {
		for _, w := range wins {
			w.SetAlwaysOnTop(true)
		}
	}
	if len(wins) > 0 {
		wins[0].Focus()
	}
	a.raise.noteRaised(raiseKindTakeout)
	// 抬升循环：在场期间每 raiseIntervalMs 抬一次（只在有休息窗口时），
	// 把随后显示的休息窗口压回下面。
	a.startRaiseLoop()
	// 系统级键鼠活动轮询：广播给所有提醒窗口（见 reminderSync 段）
	a.ensureSupActivityWatch()
}

// replaceTakeoutWins 用新一批窗口替换当前集合（info 非空时顺带把会话换成它），
// 返回**被替换掉的旧窗口** —— 调用方必须把它们关掉。
//
// 直接 `a.takeoutWins = wins` 是“关不掉的野窗”的根因：旧窗引用一旦被顶掉，就再没有
// 任何代码持有它 —— 用户点“知道了”关的是新窗，屏幕上那扇旧的谁也关不下（Alt+F4
// 只会触发它自己的 WindowClosing，而那时它已不在集合里）。所以替换只走这一条路。
func (a *App) replaceTakeoutWins(next []*application.WebviewWindow, info *model.TakeoutInfo) []*application.WebviewWindow {
	a.mu.Lock()
	defer a.mu.Unlock()
	old := a.takeoutWins
	a.takeoutWins = next
	if info != nil {
		a.takeoutCurrent = info
	}
	// 每个窗口记住它属于哪一次提醒：关窗回调据此判断“关掉的是不是当前这次”，
	// 迟到的旧窗口回调不许把刚建好的新会话清掉（与休息窗口的 breakWinSess 同理）。
	a.takeoutWinSess = make(map[*application.WebviewWindow]string, len(next))
	if a.takeoutCurrent != nil {
		for _, w := range next {
			a.takeoutWinSess[w] = a.takeoutCurrent.ID
		}
	}
	return old
}

func (a *App) removeTakeoutWindow(w *application.WebviewWindow) {
	a.mu.Lock()
	ws := a.takeoutWins
	sessID := a.takeoutWinSess[w]
	for i, x := range ws {
		if x == w {
			a.takeoutWins = append(ws[:i], ws[i+1:]...)
			break
		}
	}
	delete(a.takeoutWinSess, w)
	live := len(a.takeoutWins) > 0
	// 最后一个窗口被用户直接关掉（Alt+F4）：会话跟着结束。不清的话 realTakeoutActive
	// 永远为真（它按会话判定），之后点“预览”会被永久拒绝 —— 但只清**确实属于当前
	// 这次提醒**的窗口，否则“顶替旧窗”时迟到的回调会把新会话一起清掉。
	if !live && sessID != "" && a.takeoutCurrent != nil && sessID == a.takeoutCurrent.ID {
		a.takeoutCurrent = nil
	}
	a.mu.Unlock()
	if !live {
		a.resetTakeoutState()
	}
}

// closeTakeoutWindows 关掉全部外卖窗口并清会话（用户动作路径，结束时有提示音）
func (a *App) closeTakeoutWindows() {
	a.closeTakeoutWindowsOpt(false)
}

// closeTakeoutWindowsOpt silent=true 时不播“外卖提醒结束”提示音（退出清理用）。
func (a *App) closeTakeoutWindowsOpt(silent bool) {
	a.mu.Lock()
	ws := a.takeoutWins
	a.takeoutWins = nil
	a.takeoutCurrent = nil
	a.takeoutWinSess = nil
	a.mu.Unlock()
	// 外卖提醒结束提示音：只在“确有窗口被关掉”时播（启动清理 / 空关窗不响）。
	// 与休息结束（closeBreakWindows）同口径：声音卡片勾选启用才会在结束响一声。
	if len(ws) > 0 && !silent {
		a.playConfiguredSound()
	}
	for _, w := range ws {
		w.Close()
	}
	a.resetTakeoutState()
}

// resetTakeoutState 会话 / 定时器状态复位（收窗与“最后一个窗口被关闭”两条路径共用）
func (a *App) resetTakeoutState() {
	a.stopRaiseLoop()
	a.releaseSupActivityWatch()
}

// hasTakeoutWindows 是否有外卖窗口在场（含“刚建还没 show”的）
func (a *App) hasTakeoutWindows() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.takeoutWins) > 0
}

// raiseTakeoutWindows 把外卖窗口抬到休息窗口之上：降级为置顶 + 聚焦（见文件头注释）
func (a *App) raiseTakeoutWindows() {
	a.mu.Lock()
	ws := append([]*application.WebviewWindow(nil), a.takeoutWins...)
	cur := a.takeoutCurrent
	a.mu.Unlock()
	raised := false
	for _, w := range ws {
		if cur != nil && cur.Appearance.Fullscreen {
			continue
		}
		// 已销毁的窗口跳过：Wails 的 SetAlwaysOnTop 没有存活检查（Show / Focus / Close
		// 都有），对着一个已销毁的 HWND 反复 SetWindowPos 只会白刷一遍 API。
		if !a.windowLive(w) {
			continue
		}
		w.SetAlwaysOnTop(true)
		w.Focus()
		raised = true
	}
	if raised {
		a.raise.noteRaised(raiseKindTakeout)
	}
}

func (a *App) startRaiseLoop() {
	a.mu.Lock()
	if a.takeoutRaiseStop != nil {
		a.mu.Unlock()
		return
	}
	ch := make(chan struct{})
	a.takeoutRaiseStop = ch
	a.mu.Unlock()
	go func() {
		t := time.NewTicker(raiseIntervalMs * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-ch:
				return
			case <-t.C:
				// return 写在闭包里只退出一轮，所以“该收摊了”用变量带出来，
				// 由外层 return 真正结束这条 goroutine。
				stop := false
				logx.GuardRun("takeout.raise", func() {
					if !a.hasTakeoutWindows() {
						a.stopRaiseLoop()
						stop = true
						return
					}
					if a.raise.anyBreakActive() {
						a.raiseTakeoutWindows()
					}
				})
				if stop {
					return
				}
			}
		}
	}()
}

func (a *App) stopRaiseLoop() {
	a.mu.Lock()
	ch := a.takeoutRaiseStop
	a.takeoutRaiseStop = nil
	a.mu.Unlock()
	if ch != nil {
		close(ch)
	}
}
