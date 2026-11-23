package main

import (
	"fmt"

	"workerrest/internal/windnd"
)

// 非独占全屏（QUNS=2）的“询问式暂停”（2026-11-20，实验性，出厂关闭）。
//
// 背景：QUNS=2 是官方定义的垃圾桶（“全屏应用在跑**或**演示设置已应用”），浏览器 F11、
// 看图、视频全屏、PPT 放映、远程桌面、无边框游戏全在里面。默认直接暂停会误伤一大片，
// 完全不理会又会漏掉“全屏看剧两小时”。这一档因此做成：**先问一次，按答复记住这个应用**。
//
// 本文件只放**纯状态机**（可单测，不建窗、不碰调度器）。窗口与调度器接线见
// fs_ask_window.go 与 sys_watch.go 的 fsBusyApply。

const (
	// fsAskTimeoutMs 询问窗停留多久。超时按“暂停”结案 —— 用户没看见窗（可能正盯着
	// 全屏内容），此时“暂停”是更保守的方向：错过一次休息不会出事，误判“不暂停”会。
	fsAskTimeoutMs = 12_000
	// fsAskEnterConfirmTicks 进入侧去抖：连续两拍（6 秒）确认才算真的进了全屏段。
	// 与 applyAutoPause 的 autoPauseEnterConfirmTicks 同值 —— 但这里不能复用那个函数
	// （它内部还有一份拍数），否则一叠加就是 12 秒，用户会觉得“点了全屏半天没反应”。
	fsAskEnterConfirmTicks = 2
	// fsAskSegmentMissTicks 段结束去抖：连续两拍都不成立才算离开全屏。
	// 换视频、Alt-Tab 出来点一下再回去都不该重新问一遍。
	fsAskSegmentMissTicks = 2
)

// pauseReasonFullScrBusy 非独占全屏单独一条暂停原因。
//
// 与 pauseReasonFullScr（独占全屏与演示设置）分开是因为两者的信任度完全不同：
// 那一条从不需要用户同意，这一条只有用户答过“暂停”才会挂上，文案与忽略表也应该各走各的。
const pauseReasonFullScrBusy = "非独占全屏"

// fsAskInput 一拍的全部输入。时间由调用方给，状态机本身不读时钟（可测）。
type fsAskInput struct {
	// Supported QUNS 探测是否成功。失败 = 没有结论，整拍跳过。
	Supported bool
	// Busy 当前是否 QUNS=2（非独占全屏这一档）
	Busy bool
	// Geo 前台窗口快照（见 windnd.ForegroundSnapshot）
	Geo windnd.ForegroundInfo
	// Risky 投屏 / 勿扰 / 独占全屏正在生效 —— 此刻不能弹新窗（见 pause_notice.go 的铁律）
	Risky bool
	// Ignored 用户为这条原因点过“恢复休息”：条件还成立也不再暂停、不再询问
	Ignored bool
	// Skip 名单命中："never" / "today" / ""（settings.FullScreenSkipMode）
	Skip string
	// Now 当前时刻（毫秒）
	Now int64
}

// fsAskStep 状态机给出的动作
type fsAskStep int

const (
	// fsAskStepNone 什么都不做
	fsAskStepNone fsAskStep = iota
	// fsAskStepAsk 建询问窗（本段只问一次）
	fsAskStepAsk
	// fsAskStepPauseNow 立刻暂停（用户答“暂停”/ 超时 / 压制中 / 几何不可用）
	fsAskStepPauseNow
	// fsAskStepCloseAsk 段结束：关掉可能还开着的询问窗
	fsAskStepCloseAsk
	// fsAskStepClear 段结束：走标准收尾（按策略保持暂停，清忽略表）
	fsAskStepClear
)

// fsAskState “这一段全屏”的询问状态。
//
// 段键 = exe + HWND + 显示器原点：换视频/换游戏 = 新段（会再问一次），
// 同一个窗口 Alt-Tab 出去再回来 = 仍是同一段（不重复打扰）。
type fsAskState struct {
	segKey  string
	seen    int
	miss    int
	asked   bool
	pending bool
	// logged 这一段是否已经打过“已暂停”的进入日志。问完/超时/用户答“暂停”都会置位，
	// 段结束时统一清零 —— 与其余几条原因的 logged 同语义（只在跳变沿打日志）。
	logged   bool
	exe      string
	label    string
	degraded bool
	// seq 询问窗的序号：超时定时器与用户点击都靠它判断“自己是不是还在处理同一段询问”，
	// 迟到的回调不会把已经结案的下一段结掉。
	seq      int64
	deadline int64
}

// reset 清空一段的状态（seq 单调递增，不复位 —— 否则迟到的定时器可能撞上同号的新询问）
func (st *fsAskState) reset() {
	st.segKey = ""
	st.seen = 0
	st.miss = 0
	st.asked = false
	st.pending = false
	st.logged = false
	st.exe = ""
	st.label = ""
	st.degraded = false
	st.deadline = 0
}

// fsSegmentKey 段键 + 是否走了“几何不可用”的降级路径
func fsSegmentKey(in fsAskInput) (key string, degraded bool) {
	if !in.Geo.Supported {
		return "degraded", true
	}
	return fmt.Sprintf("%s|0x%X|%d,%d", in.Geo.Exe, in.Geo.HWND,
		in.Geo.Monitor.Left, in.Geo.Monitor.Top), false
}

// step 推进一拍，返回该执行的动作。纯函数：同样的输入序列一定得到同样的输出。
//
// 判定顺序即产品语义，每条都能单独解释：
//  1. QUNS 没有结论 → 什么都不做（不是“没有全屏”）；
//  2. 不在全屏段（QUNS≠2，或前台窗口几何上不是全屏）→ 去抖两拍后收尾。
//     这一条是“几何判定比 QUNS 更可信”的落点：QUNS=2 但前台是微信 → 不算全屏段，
//     既不暂停也不询问（也就不会把微信标进名单）；
//  3. 名单命中 → 静默（不暂停、不问）。这是用户自己给的答案，优先级高于一切；
//  4. 用户点过“恢复休息” → 本段静默；
//  5. 投屏 / 勿扰生效 → 直接暂停、不弹窗（全屏或投屏时屏幕上不能多出窗）；
//  6. 几何不可用（探测失败）→ 直接暂停，等于退回旧行为，绝不“因为读不到就放行”；
//  7. 其余 → 询问一次，12 秒超时按暂停结案。
func (st *fsAskState) step(in fsAskInput) fsAskStep {
	if !in.Supported {
		return fsAskStepNone
	}
	key, degraded := fsSegmentKey(in)
	active := in.Busy && (in.Geo.IsFullscreen() || degraded)

	if !active {
		st.miss++
		if st.miss < fsAskSegmentMissTicks {
			return fsAskStepNone
		}
		hadSegment := st.segKey != "" || st.asked || st.pending
		wasPending := st.pending
		st.reset()
		if !hadSegment {
			return fsAskStepNone
		}
		if wasPending {
			return fsAskStepCloseAsk
		}
		return fsAskStepClear
	}
	st.miss = 0

	if st.segKey != "" && key != st.segKey {
		// 换了全屏窗口：旧段作废（旧询问窗要关掉），这一拍起按新段重新判
		wasPending := st.pending
		st.reset()
		if wasPending {
			// 关窗与“重新问”同一拍：先关旧窗，下一拍再问新的
			st.segKey = key
			st.exe = in.Geo.Exe
			st.label = in.Geo.DisplayName()
			st.degraded = degraded
			return fsAskStepCloseAsk
		}
	}
	if st.segKey == "" {
		st.segKey = key
		st.exe = in.Geo.Exe
		st.label = in.Geo.DisplayName()
		st.degraded = degraded
	}

	st.seen++
	if st.seen < fsAskEnterConfirmTicks {
		return fsAskStepNone
	}

	if st.asked {
		if st.pending && in.Now >= st.deadline {
			st.pending = false
			return fsAskStepPauseNow
		}
		return fsAskStepNone
	}

	st.asked = true
	switch {
	case in.Skip != "":
		return fsAskStepNone
	case in.Ignored:
		return fsAskStepNone
	case in.Risky, degraded, !in.Geo.Supported:
		return fsAskStepPauseNow
	}

	st.pending = true
	st.seq++
	st.deadline = in.Now + fsAskTimeoutMs
	return fsAskStepAsk
}

// BreakHoldRequested 调度器 Host 的实现：询问窗正在等答复时，到点那一拍先按住休息窗。
//
// 只读一个内存字段，绝不回调调度器 —— onTick 是持 s.mu 调用它的。
// 锁序：scheduler.mu → fsAskMu，而 fsAskTick 一律先放开 fsAskMu 再碰调度器，不成环。
func (a *App) BreakHoldRequested() bool {
	a.fsAskMu.Lock()
	defer a.fsAskMu.Unlock()
	return a.fsAsk.pending
}
