package main

import (
	"log"
	"workerrest/internal/model"
	"workerrest/internal/scheduler"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// endEarlyToleranceMs “提前结束”判定的容差：结束时刻必须早于计划结束这么多才算提前。
// 自然到点由调度器的定时器驱动，正常不会早于 EndsAt，这个容差只是不被毫秒级抖动误伤。
const endEarlyToleranceMs = 1500

// closeBreakWindows 关掉休息窗口并清掉会话（调度器收尾路径用，不经过渲染层）。
// 目前唯一的调用点是调度器的 finishBreak，所以这里顺带承担“休息结束”的副作用：
// 播一次提示音（对应 上一版的 break-end → playConfiguredSound）。
func (a *App) CloseBreakWindows() {
	a.closeBreakWindowsOpt(false, true, false, false)
}

// CompleteBreakWindows 按“完成”收尾当前休息（调度器在“长休息抢占短休息”时调它）。
//
// 与 CloseBreakWindows 的区别有三处：不判“提前结束”（这次休息确实是被更长的那次
// 顶替的，不是用户提前溜号）、不出声（紧接着就开始长休息，两声叠一起只是噪音），
// 以及**把已并入的副条带给下一次休息**（carry=true）。
func (a *App) CompleteBreakWindows() {
	a.closeBreakWindowsOpt(true, true, true, true)
}

// closeBreakWindowsOpt silent=true 时不播“休息结束”提示音 —— 预览关窗（看一眼就走）
// 与退出清理都不该出声；countEnd=false 时不记“休息结束”这笔统计 —— 被自动暂停
// 打断的那种收窗由调度器记成“跳过”，这里再记一次“完成 / 提前结束”就重复了。
// asComplete=true 时按**完成**记账，绕过“提前结束”判定（长休息抢占短休息的路径）。
// carry=true 时把已并入的“同时提醒”留给下一次休息（见 finishBreakExtras）。
// 其余副作用（关窗、清会话、清空闲基准）完全一致。
func (a *App) closeBreakWindowsOpt(silent, countEnd, asComplete, carry bool) {
	a.mu.Lock()
	ws := a.breakWins
	a.breakWins = nil
	a.breakSessID = ""
	for _, w := range ws {
		delete(a.breakWinSess, w)
	}
	info := a.sess
	a.sess = nil
	// 接力换窗的“让音”标记只管这一次收窗：读走就清，绝不漏到下一次正常到点
	quiet := a.relayQuietEnd
	a.relayQuietEnd = false
	a.mu.Unlock()
	for _, w := range ws {
		w.Close()
	}
	// 窗口和会话都没了：全局键鼠轮询该收了（若还有外卖窗口在场则继续跑，
	// 判断在 releaseSupActivityWatch 里）
	a.releaseSupActivityWatch()
	// 休息窗全没了：外卖抬升循环不该再抢窗口层级与焦点（每次抬升都会 Focus 外卖窗，
	// 没有休息窗在场时反复抢焦点等于打断用户）
	a.raise.setBreaksActive(false)
	switch {
	case quiet, asComplete:
		// 马上有一扇新窗顶上来（接力换窗 / 长休息抢占短休息）：结束音不响。
		// 接力换窗是**整段静音**（新窗的开始音由 ShowBreakSession 按 relayQuietStart 同样跳过，
		// 见 schedEndBreak）；抢占那次新休息的开始音照响 —— 那是自动发生的“新一次休息开始”。
		// 两种情况都要把上一段还没放完的那首停掉，否则两首会叠在一起（导入的音乐动辄几分钟，
		// 盖住新窗的声音还分不清是哪一条在响）。
		log.Printf("[sound] 换窗：结束音不响（整段静音=%v 抢占=%v）", quiet, asComplete)
		a.stopSounds()
	case !silent:
		a.playConfiguredSound()
	}
	// 统计：休息结束（唯一调用点是调度器的 finishBreak，所以这里就是 break-end）。
	// 类型取自会话本身 —— 休息结束时调度器状态可能已经翻篇，而记录要用**刚刚结束的那次**；
	// 自定义提醒把名字快照一起记下来，提醒删了 / 改名了报表也还能对上号。
	if countEnd && info != nil && !info.Preview {
		name := ""
		if cfg := a.CfgSnapshot(); cfg != nil {
			name = cfg.BreakNameByID(info.Type)
		}
		// 提前结束：结束时刻明显早于计划结束。留 1.5 秒容差 —— 自然到点由定时器驱动，
		// 正常不会早于 EndsAt，但别让抖动被记成“提前结束”。
		// 口径只含短休息 / 长休息：自定义提醒的窗口上虽然也挂着“提前结束”，但它只记
		// 一条“自定义提醒”（不区分坐没坐满），外卖不走这里。
		// asComplete（长休息抢占短休息）明确要求按完成记，不看时刻。
		early := !asComplete && (info.Type == "short" || info.Type == "long") &&
			nowMs()+endEarlyToleranceMs < info.EndsAt
		// 提前结束的那次不计入“完成”，OnBreakEnd 内部按 early 分开记
		a.statsStore().OnBreakEnd(name, early)
	}
	// 休息窗口关闭：把空闲基准清零（无论是否有统计会话 / 是否预览），否则
	// “刚休息完、还没动鼠标”进入 idle 后会被立刻误判成离开而暂停
	if a.idle != nil {
		a.idle.ResetBaseline()
	}
	// 已并入的“同时提醒”：要么带进下一次休息（长休息抢占），要么把还没被处理的外卖
	// 交回独立窗口 —— 否则那笔提醒随休息窗一起消失，用户压根没看到。
	a.finishBreakExtras(info, carry)
}

// ---------- 调度器事件（上一版 scheduler.on(...) 的对应物） ----------

// onSchedulerState 状态一变：推给设置页 + 刷新托盘
func (a *App) OnSchedulerState() {
	a.pushState()
	a.refreshTray()
}

// onPreNotify 进入“距下次休息不足 notifyBeforeMs”的区间了：弹出右下角的提前提示小窗
// （系统通知在 Windows 上带不了按钮，所以是自绘窗口，见 prebreak.go）。
//
// 内容在这里**算一次**并记进 a.preInfo：窗口内轮询 PreBreakState 拿到的是同一份，
// 中途计划漂移（比如固定时刻线重排）不会让标题 / 后缀跟着变。
func (a *App) OnPreNotify(kind string, notifyBeforeMs int64) {
	info := a.rememberPreBreakInfo(kind)
	// 剩余时间一起打出来：这张日志是排查“弹早了 / 弹晚了”的唯一现场
	//（窗口上显示的数字与它同源，见 PreBreakState）
	remain := int64(-1)
	if a.sched != nil {
		remain = a.sched.RemainingMs()
	}
	log.Printf("[prebreak] 即将休息：%s（提前 %d ms，剩余 %d ms）custom=%v title=%q",
		kind, notifyBeforeMs, remain, info.Custom, info.Title)
	a.ShowPreBreakWindow(info)
}

// onPaused 暂停提醒：关掉提前提示窗。托盘暂停与弹窗内“暂停计时”共用这条路径；
// 恢复时若仍在提前提示区间内，调度器会再走一次 onPreNotify 把窗口弹回来（倒计时继续）。
//
// 只收调度器自己那扇窗（closePreBreak）：外卖预通知共用同一窗口但归外卖模块管 ——
// “暂停休息”不等于“暂停外卖”，外卖按自己的 pauseAction 决定照常/推迟/跳过，
// 无条件关窗会让“照常提醒”那条配置在暂停时静默失效。
//
// keepPre 为真时连自己那扇也不收：那条提醒的“暂停休息时”是“正常提醒”，照常提醒的窗
// 不该被一次暂停收掉（用户口径，见 scheduler.AutoPauseKeep）。窗口会以“已暂停”显示，
// 恢复后继续倒数。
//
// preKind 只是兜底：判据看的是**这扇窗自己**提示的那条提醒（a.preInfo.Kind）——
// 暂停中照常弹出的自定义提醒，其提示窗与“调度器此刻在等的那条”未必是同一个身份。
func (a *App) OnPaused(preKind string) {
	a.mu.Lock()
	kind, takeoutOwned := a.preInfo.Kind, a.preInfo.Owner == "takeout"
	a.mu.Unlock()
	// 外卖预通知共用同一扇窗但归外卖模块管，这条路径不收（“暂停休息”不等于“暂停外卖”）
	if takeoutOwned {
		return
	}
	if kind == "" {
		kind = preKind
	}
	if a.pauseKeepsWindow(kind) {
		return
	}
	a.closePreBreakWindow()
	// 窗收了，记住的内容也一并作废：留着一份“上次提示的是谁”只会让后续读取
	// （PreBreakState / 动作分发）拿到一扇已经不在场的窗的状态。
	a.mu.Lock()
	a.preInfo = PreBreakInfo{}
	a.preTargetAt = 0
	a.mu.Unlock()
}

// pauseKeepsWindow 这条提醒自己的“暂停休息时”是不是“正常提醒”（它此刻的窗口留不留）。
// 判据只有这一条，且必须按**那扇窗自己的归属**去查 —— 不按“调度器此刻在等哪条”一刀切。
func (a *App) pauseKeepsWindow(kind string) bool {
	return a.CfgSnapshot().BreakPauseRemind(kind)
}

// OnAutoPause 自动原因（空闲 / 勿扰 / 全屏 / 投屏 / 应用占用）进入暂停：把**此刻正显示在
// 屏幕上**的提醒窗口收掉 —— 提前提示窗、外卖提醒窗、外卖预通知，以及休息窗。
//
// 留不留只看那扇窗所属提醒自己的“暂停休息时”：选了“正常提醒”就不收 —— 照常提醒的窗
// 不该被一次暂停收掉，收掉等于把配置偷偷降级。**每扇窗按它自己的归属查**（见
// pauseKeepsWindow）：提示窗用它自己提示的那条（a.preInfo.Kind，外卖预通知归外卖卡），
// 休息窗用它所属会话的那次休息（a.sess.Type）；keep 里的身份只在这扇窗没带身份时兜底。
// 与 OnPaused 的区别只在“进入的那一刻”：手动暂停只收提前提示窗，休息窗按它自己卡里的
// “暂停休息时”处置（推迟 → 剩余时间冻结挂着；正常提醒 → 照常走完；跳过 → 就此结束）；
// 自动暂停默认把在场窗口全收掉（暂停原因不参与判断）。
// **只作用于进入瞬间已经在场的窗口**：暂停期间到点的新提醒一律按各自“暂停休息时”的处置走
// （照常提醒就会照常弹出来，与暂停原因无关，见 scheduler 的 onTick paused 分支）。
//
// 记账分工：被打断的那次休息由调度器记“跳过”（它知道休息身份与时长）；这里只负责关窗，
// 且**不记** break-end、不播结束提示音 —— 休息不是走完的，也不是用户点的结束。
func (a *App) OnAutoPause(keep scheduler.AutoPauseKeep) {
	// 提前提示窗：外卖预通知与调度器的提示窗是同一个窗口，所以留不留要按它自己的归属判 ——
	// 外卖预通知看外卖那份配置，休息 / 自定义提醒看那扇窗自己提示的那条（preInfo.Kind；
	// 老会话 / 单测构造没带身份时才退回 keep.PreKind）。
	// 收窗后连 owner 一起清掉 —— 留着一个 "takeout" 会让后续 ClosePreBreak 误判成
	// “这扇窗归外卖管”而跳过关闭，残留状态还会让 PreBreakState 读到已失效的倒计时。
	a.mu.Lock()
	preOwner, preKind := a.preInfo.Owner, a.preInfo.Kind
	a.mu.Unlock()
	if preKind == "" {
		preKind = keep.PreKind
	}
	keepPre := keep.Takeout
	if preOwner != "takeout" {
		keepPre = a.pauseKeepsWindow(preKind)
	}
	if !keepPre {
		a.closePreBreakWindow()
		a.mu.Lock()
		a.preInfo = PreBreakInfo{}
		a.preTargetAt = 0
		a.mu.Unlock()
	}

	// 外卖：窗口在场就关掉并记一次“取消”；推迟线一并作废（恢复后不补弹）。
	// 这是“进入暂停那一刻的在场窗口”，与暂停期间到点的那批互不重叠 —— 后者由
	// checkTakeoutReminders 按外卖自己的“暂停休息时”处置。
	// 预览窗不记账：用户只是点了一眼预览，不该算进外卖报表的取消次数。
	// “正常提醒”时窗与推迟线都留着（keep.Takeout）—— 照常提醒就是照常。
	a.mu.Lock()
	hadTakeoutWin := len(a.takeoutWins) > 0
	takeoutPreview := a.takeoutCurrent != nil && a.takeoutCurrent.Preview
	hadTakeoutPostponed := a.takeoutPostponed != nil
	if !keep.Takeout {
		a.takeoutPostponed = nil
	}
	a.mu.Unlock()
	if hadTakeoutWin && !keep.Takeout {
		a.closeTakeoutWindowsOpt(true)
		if !takeoutPreview {
			a.statsStore().RecordTakeoutAction("skip")
		}
	}
	if hadTakeoutPostponed && !keep.Takeout {
		a.statsStore().RecordTakeoutAction("skip")
	}

	// 休息窗：静默关、不记 break-end（统计由调度器按“跳过”记）。
	// 留不留看**这次休息自己**的配置（a.sess.Type 就是窗所属会话的休息身份）——
	// 配了“正常提醒”就留着，按真实时钟走完。
	a.mu.Lock()
	hadBreak := len(a.breakWins) > 0 || a.sess != nil
	breakKind := keep.BreakKind
	if a.sess != nil && a.sess.Type != "" {
		breakKind = a.sess.Type
	}
	a.mu.Unlock()
	keepBreak := a.pauseKeepsWindow(breakKind)
	if hadBreak && !keepBreak {
		a.closeBreakWindowsOpt(true, false, false, false)
	}
	log.Printf("[autoPause] 自动暂停：在场提醒窗口处理（休息=%v 休息归属=%q 保留休息=%v 外卖=%v 外卖推迟=%v 保留外卖=%v 提前提示归属=%q 保留提前提示=%v）",
		hadBreak, breakKind, keepBreak, hadTakeoutWin, hadTakeoutPostponed, keep.Takeout, preKind, keepPre)
}

// ClosePreBreak 调度器重排时关掉残留的提前提示窗（对应 scheduler.Host.ClosePreBreak）。
// 与 onPaused 的区别：这里只关窗、不改 pause 状态，专供“被禁用 / 重排的提醒”清场。
//
// 只收**自己那扇窗**：外卖预通知（owner=takeout）复用同一个窗口，归外卖模块自己管
// （closeTakeoutPreNotifyIfOpen）—— 调度器任何一次状态变更都顺手把它抹掉的话，
// 外卖的提前通知会被“保存设置”这类无关动作带走。
func (a *App) ClosePreBreak() {
	a.mu.Lock()
	takeoutOwned := a.preInfo.Owner == "takeout"
	a.mu.Unlock()
	if takeoutOwned {
		return
	}
	a.closePreBreakWindow()
}

// ---------- 外卖状态的快照（供状态快照读取） ----------

func (a *App) TakeoutPostponedSnapshot() *model.TakeoutPostponed {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.takeoutPostponed == nil {
		return nil
	}
	cp := *a.takeoutPostponed
	return &cp
}

func (a *App) TakeoutFiredSnapshot() map[string]bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.takeoutFired) == 0 {
		return nil
	}
	out := make(map[string]bool, len(a.takeoutFired))
	for k := range a.takeoutFired {
		out[k] = true
	}
	return out
}

// ---------- 窗口 ----------

// reminderBoundsFor 提醒窗口在**指定显示器**工作区上的位置与大小：与 上一版
// breakWindow.ts 同规则——该屏工作区的 widthPct × heightPct，居中；全屏时铺满工作区。
// 纯函数，多屏下对每块目标屏各算一次（单测直接验）。
//
// 名字从 breakBoundsFor 改成 reminderBoundsFor（2026-11-05）：休息窗与外卖窗共用这一份。
// 改成共用之前，外卖窗是自己手算的——尺寸只认 widthPct / heightPct，`全屏` 只挪了位置，
// 于是“全屏 + 95%”会变成铺在左上角的 95% 方块，右下留一条 5% 的空隙（GUI 探针实测
// 窗口 2432×1322 / 工作区 2560×1392）。同一份外观配置必须只有一处解读。
func reminderBoundsFor(wa application.Rect, cfg model.AppearanceConfig) (x, y, w, h int) {
	if cfg.Fullscreen {
		return wa.X, wa.Y, wa.Width, wa.Height
	}
	w = int(float64(wa.Width) * cfg.WidthPct / 100)
	h = int(float64(wa.Height) * cfg.HeightPct / 100)
	// 先夹尺寸、再算位置。反过来的话，兜底一旦触发，位置还按夹紧**前**的尺寸算，
	// 窗口就从居中偏出去（1280 的屏、20% 宽 = 256 → 夹到 320，右移 32px）。
	if w < 320 {
		w = 320
	}
	if h < 240 {
		h = 240
	}
	// 工作区比下限还小（极端 DPI / 小屏）：宁可窗口小一点，也要完整留在屏内
	if w > wa.Width {
		w = wa.Width
	}
	if h > wa.Height {
		h = wa.Height
	}
	x = wa.X + (wa.Width-w)/2
	y = wa.Y + (wa.Height-h)/2
	return
}

// ShowBreakWindow 弹出休息提醒窗口（托盘 / 设置页按钮 / 调度器都走这里）。
// 无头（-tags server）构建下没有原生窗口，跳过创建——会话数据仍然构建，
// 因此无头构建可以完整验证“绑定 → 会话 JSON”这条链路。
//
// 多屏：按“在所有屏幕显示”开关对每块目标屏各建一个窗（与外卖窗口同策略，
// 见 targetDisplays）。
//
// 复用规则（2026-10-25 修）：**只有窗口就是为当前会话建的**才复用（用户按托盘
// “立即休息”重复点同一会话时不该叠窗）；会话换了就整组关掉重建。曾经的
// “有窗口就复用”会让“切换督休专员 → 点预览”读到上一个会话的页面，
// 表现为预览窗里没有（或还是上一个）督休专员。
func (a *App) ShowBreakWindow(kind string) {
	if serverMode {
		log.Printf("[win] server 模式无原生窗口，跳过创建 break 窗口（会话已就绪）")
		return
	}
	// 建窗要几百毫秒：串行化，避免连点“预览”时两批窗口互相覆盖（野窗）
	a.breakWinMu.Lock()
	defer a.breakWinMu.Unlock()
	a.mu.Lock()
	curID := ""
	if a.sess != nil {
		curID = a.sess.ID
	}
	winSess, winsNow := a.breakSessID, a.breakWins
	a.mu.Unlock()
	// 复用还要求“窗口确实还活着”：已销毁的窗口不在窗口管理器里，对它 Show()
	// 会被 Wails 当成重新 Run —— 原地复活一扇僵尸窗（页面重载、旧会话内容闪一下）。
	var stale []*application.WebviewWindow
	if breakWinReusable(winSess, curID, len(winsNow)) && a.windowsLive(winsNow) {
		for _, w := range winsNow {
			w.Show()
			w.Focus()
		}
		return
	}
	a.mu.Lock()
	if len(a.breakWins) > 0 {
		stale = a.breakWins
		a.breakWins = nil
		a.breakSessID = ""
		// 指针条目跟着摘掉：留着只会让 map 无界增长，且容易让后来者误判归属
		for _, w := range stale {
			delete(a.breakWinSess, w)
		}
	}
	// 先认领会话：建窗要几百毫秒，这期间旧窗口的 WindowClosing 可能迟到 ——
	// 认领之后 removeBreakWindow 能认出“那是上一个会话的窗”，不会把会话清掉。
	a.breakSessID = curID
	a.mu.Unlock()
	// 旧会话的窗口先关（它们的页面内容已经过期，留着只会让人看到旧立绘/旧倒计时）
	for _, w := range stale {
		w.Close()
	}

	// 把“督休专员-启用”状态经 URL query 下发，让提醒窗口首帧就定好布局，
	// 避免先显示无立绘窗口、立绘加载完再切换的闪烁（frontend 据此决定整窗先
	// 透明、等立绘就绪再淡入；无立绘时立即显示正确态）。
	supFlag := "0"
	if a.CfgSnapshot().SupervisorCfg().Enabled {
		supFlag = "1"
	}

	ap := a.CfgSnapshot().AppearanceCfg()
	var wins []*application.WebviewWindow
	for _, display := range a.targetDisplays() {
		x, y, w, h := reminderBoundsFor(display.WorkArea, ap)
		win := a.app.Window.NewWithOptions(application.WebviewWindowOptions{
			Title:         "休息一下",
			X:             x,
			Y:             y,
			Width:         w,
			Height:        h,
			Frameless:     true,
			AlwaysOnTop:   ap.AlwaysOnTop,
			DisableResize: true,
			// 不设它 X/Y 会被无视：Wails 的 InitialPosition 零值 = 整窗居中
			InitialPosition:  application.WindowXY,
			URL:              "/break.html?win=break&sup=" + supFlag,
			BackgroundType:   application.BackgroundTypeTranslucent,
			BackgroundColour: application.NewRGBA(0, 0, 0, 0),
			// 共享屏幕时隐藏：Wails 在窗口显示后调 SetWindowDisplayAffinity。
			// 生效前提是 main.go 的 --disable-direct-composition —— 少了它，像素不在
			// 窗口重绘表面里，标记照样回读成功而捕获照旧可见（那里有实测记录）。
			ContentProtectionEnabled: ap.HideOnShare,
			// 窗口身份经 URL 的 ?win=break 下发；JS 字段对 URL 页面不生效（仅占位）
			JS: `window.__WR_WIN = "break";`,
			// 无边框窗口在 Windows 上会留一圈原生装饰（阴影/描边），与 上一版一致地关掉
			Windows: application.WindowsWindow{
				DisableFramelessWindowDecorations: true,
				// 与 上一版的 skipTaskbar 同义：提醒 / 预览窗口不进任务栏
				HiddenOnTaskbar: true,
			},
		})
		// 窗口被系统 / 用户直接关掉（Alt+F4）时清掉引用：跨 tick 拿着已销毁的窗口
		// 访问会抛错，且会让“已有窗口在场”的判断永久为真
		win.OnWindowEvent(events.Common.WindowClosing, func(*application.WindowEvent) {
			a.removeBreakWindow(win)
		})
		wins = append(wins, win)
	}

	a.mu.Lock()
	// 建窗这几百毫秒里会话可能已经被收掉（提前结束 / 预览点任意处 / 调度器到点）：
	// 那就不能把刚建好的窗口挂上去 —— 否则屏幕上一扇“没有会话”的空窗，而程序认为
	// 它属于一个已经结束的会话，除了等下一次建窗或退出，谁也关不掉它。
	stillWanted := curID != "" && a.sess != nil && a.sess.ID == curID
	if !stillWanted {
		a.breakSessID = ""
		a.mu.Unlock()
		for _, w := range wins {
			w.Close()
		}
		log.Printf("[win] 建窗期间会话已结束，本轮建出的 %d 个窗口直接收掉", len(wins))
		return
	}
	if a.breakWinSess == nil {
		a.breakWinSess = make(map[*application.WebviewWindow]string)
	}
	for _, w := range wins {
		a.breakWinSess[w] = curID
	}
	a.breakWins = wins
	a.breakSessID = curID
	a.mu.Unlock()
	// 告诉外卖抬升循环“休息窗口在场了”：两者都是置顶窗，后显示的会压住先显示的，
	// 只有靠这个标记，外卖窗才会被周期性地重新抬到休息窗之上（时间敏感的一方在上面）。
	// 此前它只在单测里被设过、生产代码从不接线，抬升循环因此从未真正生效。
	a.raise.setBreaksActive(len(wins) > 0)
}

// removeBreakWindow 单个休息窗口被直接关掉（Alt+F4）时从切片摘除；
// 最后一个属于**当前会话**的窗口被关掉时清掉会话（与“最后一个窗口被关闭”语义对齐）。
func (a *App) removeBreakWindow(w *application.WebviewWindow) {
	a.mu.Lock()
	sid := a.breakWinSess[w]
	delete(a.breakWinSess, w)
	if ws := a.breakWins; len(ws) > 0 {
		for i, x := range ws {
			if x == w {
				a.breakWins = append(ws[:i], ws[i+1:]...)
				break
			}
		}
	}
	cur := ""
	if a.sess != nil {
		cur = a.sess.ID
	}
	if breakSessEndsOn(len(a.breakWins), sid, cur) {
		a.sess = nil
		a.breakSessID = ""
	}
	a.mu.Unlock()
	// 最后一个休息窗口被直接关掉（Alt+F4）：全局键鼠轮询按“还有没有提醒窗口”决定收不收
	a.releaseSupActivityWatch()
	// 多屏时可能只关掉其中一扇：按**剩余**窗口更新外卖抬升标记
	a.raise.setBreaksActive(a.hasBreakWindows())
}

// hasBreakWindows 是否有休息窗口在场
func (a *App) hasBreakWindows() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.breakWins) > 0
}

// breakWinReusable 现有休息窗口能不能直接复用：必须“确实有窗口”且
// “这些窗口就是为当前会话建的”。curSess 为空（没有会话）时一律不复用。
func breakWinReusable(winSess, curSess string, n int) bool {
	return n > 0 && curSess != "" && winSess == curSess
}

// ensureWindowsOnScreen 把落在所有显示器工作区之外的提醒窗口移回主屏中央。
//
// 触发场景：拔掉外接显示器（或改分辨率 / 缩放）后，原本建在那块屏上的窗口坐标落在
// 已经不存在的位置。它仍然置顶、仍然不接受操作，用户除了 Alt+F4 没有任何入口 ——
// 这是“关不掉的窗”最容易被忽略的一种。全部窗口都在工作区内时一次 API 都不调。
func (a *App) ensureWindowsOnScreen() {
	if serverMode || a.app == nil {
		return
	}
	a.mu.Lock()
	ws := append([]*application.WebviewWindow(nil), a.breakWins...)
	ws = append(ws, a.takeoutWins...)
	if w := a.wins[preBreakWindowName]; w != nil {
		ws = append(ws, w)
	}
	// 暂停提示小窗也纳入：拔掉外接屏时它同样可能落在不存在的坐标上
	if w := a.wins[pauseNoticeWindowName]; w != nil {
		ws = append(ws, w)
	}
	a.mu.Unlock()
	if len(ws) == 0 {
		return
	}
	screens := a.app.Screen.GetAll()
	if len(screens) == 0 {
		return
	}
	for _, w := range ws {
		if !a.windowLive(w) {
			continue
		}
		if rectVisibleOnAny(w.Bounds(), screens) {
			continue
		}
		log.Printf("[win] 提醒窗口跑到所有显示器工作区之外（窗口 #%d），移回主屏中央", w.ID())
		w.Center()
	}
}

// rectVisibleOnAny 矩形与任一显示器工作区是否有可见交集
func rectVisibleOnAny(r application.Rect, screens []*application.Screen) bool {
	for _, s := range screens {
		wa := s.WorkArea
		if r.X < wa.X+wa.Width && r.X+r.Width > wa.X &&
			r.Y < wa.Y+wa.Height && r.Y+r.Height > wa.Y {
			return true
		}
	}
	return false
}

// windowLive 窗口对象是否还挂在 Wails 的窗口管理器里（已销毁的窗口会被摘掉）。
//
// 为什么要问这一句：Wails 的 Show() 在窗口已销毁时会走 `w.Run()` **原地重建**同一扇窗，
// 所以“复用”之前不确认存活，可能把一扇本该消失的提醒窗复活出来（页面重载、
// 倒计时从头开始），而程序还当它是上次那个会话的窗。
func (a *App) windowLive(w *application.WebviewWindow) bool {
	if w == nil || a.app == nil {
		return false
	}
	_, ok := a.app.Window.GetByID(w.ID())
	return ok
}

// windowsLive 一批窗口是否全部还活着（Create 之后立即注册进管理器，所以新建的也算活）
func (a *App) windowsLive(ws []*application.WebviewWindow) bool {
	for _, w := range ws {
		if !a.windowLive(w) {
			return false
		}
	}
	return true
}

// dropWindowRef 只在 m[name] 确实指向 w 时才删除这一项。
//
// 关窗事件在 Wails 里是**异步分发**的（每个监听器各起一个 goroutine）。“关旧窗 →
// 立刻建同名新窗”是这些窗口的常规操作，迟到的旧窗回调若按名字无脑 delete，抹掉的
// 是刚建好的新窗引用 —— 那扇新窗从此没有任何代码持有它，只能靠它自己的页面按钮
// 关闭，主进程彻底失去控制权。所有“按名字登记窗口”的回调都必须走这里。
func dropWindowRef(m map[string]*application.WebviewWindow, name string, w *application.WebviewWindow) {
	if m[name] == w {
		delete(m, name)
	}
}

// breakSessEndsOn 关掉一个窗口后要不要结束会话：
//   - 还剩窗口（多屏）→ 不结束；
//   - 关掉的是**上一次会话**的窗（closedSess 与当前会话不符）→ 也不结束。
//     这是“预览窗时不时空壳”的根因之一：旧窗口的 WindowClosing 迟到一步，
//     把刚建好的新会话清掉了，新窗口首帧 breakCurrent() 拿到 nil。
func breakSessEndsOn(remaining int, closedSess, curSess string) bool {
	if remaining > 0 {
		return false
	}
	if curSess != "" && closedSess != curSess {
		return false
	}
	return true
}
