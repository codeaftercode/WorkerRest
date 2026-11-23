package main

import (
	"log"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"workerrest/internal/hotkey"
	"workerrest/internal/logx"
)

// 全局快捷键（对应 上一版 globalShortcut）：推迟 / 提前结束 / 暂停恢复 / 跳到下次休息。
//
// Wails v3 没有全局快捷键能力，直接用 Win32（原语已收进 internal/hotkey）：
//   - user32!RegisterHotKey(NULL, id, mods, vk) 注册**线程级**热键（hwnd=NULL 时
//     WM_HOTKEY 投递到注册线程的消息队列，不需要窗口）；
//   - 专用 goroutine（LockOSThread）跑 GetMessageW 循环，收到 WM_HOTKEY 按 id 分发到
//     调度器动作（与休息窗口 / 托盘 / 设置页同一个汇聚点，相位保护在调度器里）；
//   - 配置变更（SaveSettings / Import / Reset）后 PostThreadMessage 触发线程内
//     注销旧键 + 重注册 —— RegisterHotKey/UnregisterHotKey 必须与注册线程同线程。
//
// 无头 / server 模式不启动：冒烟进程不该真的抢用户的系统热键。
// 非 Windows 平台：internal/hotkey 的 RegisterHotKey 恒返回 false，startHotkeys 里
// 探测失败后不启动（走与“读不到热键能力”相同的降级路径）。

const (
	hkIdPostpone  = 1
	hkIdEndEarly  = 2
	hkIdPause     = 3
	hkIdNextBreak = 4
)

// hotkeyDef 一个全局快捷键：配置键名 + 热键 id + 触发后走哪个调度器动作。
// 键名与设置页 shortcuts.* 一致（settings.json 里透传，Go 配置模型未类型化）。
type hotkeyDef struct {
	id     int
	key    string
	action func(*App)
}

var hotkeyDefs = []hotkeyDef{
	// “推迟当前休息”与侧栏「推迟休息」同一口径：只认下一条短 / 长休息，
	// 外卖与自定义提醒不参与（2026-11-14 用户口径）。
	{hkIdPostpone, "postpone", (*App).schedPostponeRest},
	{hkIdEndEarly, "endEarly", (*App).schedEndBreak},
	{hkIdPause, "pause", (*App).schedPauseToggle},
	{hkIdNextBreak, "nextBreak", (*App).schedBreakNow},
}

// HotkeyStatus 一个全局快捷键的最近一次注册结果。
//
// 为什么要回传前端：RegisterHotKey 失败的唯一原因是“这个组合键已经被别的程序占了”，
// 而它只会在日志里留一行 —— 用户在设置页看到框里写着 Ctrl+Alt+B、按下去毫无反应，
// 无从判断是没生效还是软件坏了（docs/QA-REPORT-2026-10-25.md:77 记过这个坑）。
// 现在每次注册都把逐项结果记下来，设置页在对应行下面直接标红。
//
// State 取值（前端按它挑文案，不解析 Accel）：
//   - ok          注册成功
//   - disabled    accel 留空 = 用户禁用了这一项
//   - invalid     组合键无法识别（需要至少一个修饰键 + 可识别主键）
//   - conflict    RegisterHotKey 失败：已被其他程序占用
//   - unsupported 本机拿不到全局热键能力（非 Windows / server 模式 / 消息循环未就绪）
type HotkeyStatus struct {
	Key   string `json:"key"`   // 配置键名：postpone / endEarly / pause / nextBreak
	Accel string `json:"accel"` // 该项当前配置的组合键字符串；disabled 时为空
	State string `json:"state"`
}

const (
	hotkeyStateOK          = "ok"
	hotkeyStateDisabled    = "disabled"
	hotkeyStateInvalid     = "invalid"
	hotkeyStateConflict    = "conflict"
	hotkeyStateUnsupported = "unsupported"
)

// hotkeyStatusEvent 注册结果广播（配置保存 / 恢复默认后会重新注册，前端据此刷新行下提示）。
const hotkeyStatusEvent = "hotkeys:status"

// hotkeyCheckTimeout 检测按钮的最长等待：热键线程要跑完一整轮注销 + 重注册，
// 正常是毫秒级。超时就用最近一次快照兜底，绝不让设置页一直转圈。
const hotkeyCheckTimeout = 3 * time.Second

// hotkeyApplyTimeout 改单项快捷键的最长等待。线程内要做的事和检测一样多
// （注销四项 + 按候选配置重注册），正常也是亚毫秒级。
const hotkeyApplyTimeout = 3 * time.Second

// ShortcutApplyResult “改一项快捷键”的结果。
//
// 为什么要一个专门的结果类型：改键必须是**一次原子提交** —— 先按候选配置真的注册，
// 注册上了才写配置文件；注册不上就原样退回，配置文件一个字节都不动。
// 旧实现是“先存配置 → 重注册 → 失败再回滚”，用户看到的就是“先存再报错”，
// 中间那一小段时间里配置里躺着一个注定失效的键。
type ShortcutApplyResult struct {
	OK    bool   `json:"ok"`    // 已注册成功且已写入配置
	State string `json:"state"` // 候选键的最终状态：ok / conflict / invalid / disabled / unsupported / ""（未测）
	Key   string `json:"key"`   // 配置键名
	Accel string `json:"accel"` // 用户想设的组合键
	Kept  string `json:"kept"`  // 未生效时配置里保留的原值（空 = 此项本来就没设）
	// List 收尾时四项的真实状态：成功 = 候选配置的结果，失败 = 已还原成原配置的结果。
	// 前端不必再自己推断“现在到底哪些生效”。
	List []HotkeyStatus `json:"list"`
}

// HotkeyManager 全局快捷键。App 持有（a.hotkeys），server 模式为 nil。
type HotkeyManager struct {
	app      *App
	threadID atomic.Uint32 // 消息循环线程；0 = 尚未就绪（reload 会静默跳过）

	// suspended = 设置页正在录入快捷键，四项暂时注销（见 SetCapture）。
	// 为什么必须注销而不是“只忽略按键”：RegisterHotKey 是系统级独占的，
	// 注册期间那次按键会被系统直接投给我们的消息队列，**前台窗口收不到** ——
	// 用户想录 Ctrl+Alt+P，结果是动作真的执行了、输入框里还什么都没有。
	suspended atomic.Bool

	mu      sync.Mutex
	status  []HotkeyStatus        // 最近一次 apply 的逐项结果
	waiters []chan []HotkeyStatus // 正在等“检测”结果的调用方（见 check）
	applied bool                  // 至少完整跑过一次 apply（区分“还没注册”和“全部禁用”）

	// 改单项快捷键的请求（见 SetShortcut）。参数与回传 channel 一起挂在结构体上，
	// 消息只负责叫醒线程 —— 与 SetCapture 同一套路，避免 busy 时消息乱序配错参数。
	candMu  sync.Mutex
	candSc  map[string]any
	candKey string
	candCh  chan candidateOutcome
}

// candidateOutcome 线程内按候选配置试注册的结果。
type candidateOutcome struct {
	list   []HotkeyStatus // 收尾后四项的真实状态（失败时已是还原后的旧配置结果）
	target HotkeyStatus   // 用户改的那一项在候选配置下的结果
	blame  string         // 不为空 = 不可用的那一项（目标项自己、或被挤掉的另一项）
}

// startHotkeys 启动消息循环线程并按当前配置注册（server / 无头跳过）。
func (a *App) startHotkeys() {
	if serverMode || a.app == nil {
		return
	}
	// 非 Windows / 热键能力不可用时，RegisterHotKey 恒失败，探测后不启动。
	h := &HotkeyManager{app: a}
	a.hotkeys = h
	go h.run()
}

// reloadHotkeys 配置变更后重注册（未启动时是空操作）。
func (a *App) reloadHotkeys() {
	if a.hotkeys != nil {
		a.hotkeys.reload()
	}
}

// run 消息循环线程：注册 → 循环取消息 → WM_HOTKEY 分发 / reload 重注册 / WM_QUIT 收尾。
func (h *HotkeyManager) run() {
	runtime.LockOSThread()
	// GetMessage 前先空 Peek 一次：确保线程真的有消息队列，
	// 否则并发的 reload 里 PostThreadMessage 可能因队列未建而丢消息。
	var m hotkey.WinMsg
	hotkey.PeekMessage(&m)
	h.threadID.Store(hotkey.CurrentThreadID())
	logx.GuardRun("hotkeys.apply", h.apply)
	for {
		if !hotkey.GetMessage(&m) { // WM_QUIT / 错误
			// 必须在这条线程上注销（键位跟着注册线程走），然后才把 tid 清 0 ——
			// tid==0 在调用方眼里就是“线程已退出、键位已归还”，测试靠它等收尾。
			h.unregisterAll()
			h.threadID.Store(0)
			log.Printf("[hotkey] 消息循环退出，全局热键已注销（此后按快捷键不会有反应）")
			return
		}
		switch m.Message {
		case hotkey.WmHotkey:
			logx.GuardRun("hotkeys.dispatch", func() { h.dispatch(int(m.WParam)) })
		case hotkey.HkReloadMsg:
			logx.GuardRun("hotkeys.apply", h.apply)
		case hotkey.HkCaptureMsg:
			logx.GuardRun("hotkeys.capture", h.applyCapture)
		case hotkey.HkCandidateMsg:
			logx.GuardRun("hotkeys.candidate", h.applyCandidate)
		}
	}
}

// reload 通知消息循环线程重读配置（线程未就绪时跳过：run() 里本来就会注册一次）。
func (h *HotkeyManager) reload() {
	if id := h.threadID.Load(); id != 0 {
		hotkey.PostThreadMessage(id, hotkey.HkReloadMsg)
	}
}

// shutdown 让消息循环线程收尾退出（幂等，可重复调用）。
//
// 必须走 WM_QUIT，不能从调用方直接 unregisterAll()：全局热键挂在**注册它的那条线程**上，
// 跨线程注销不会报错，只是静默什么都不做 —— 于是那个组合键被那条线程占到进程结束。
// 这不是理论问题：测试里每起一个消息循环就占掉一组，跑到后面 F13~F24 就分不出来了。
// GetMessage 收到 WM_QUIT 返回 false，run() 在自己的线程上 unregisterAll 后 return。
//
// 生产侧进程退出时系统会回收键位，所以这个方法当前的唯一调用方是测试；放在这里是因为
// “只有消息循环线程能注销”这条约束属于 HotkeyManager 自己，不该让每个测试各自猜着写。
func (h *HotkeyManager) shutdown() {
	if id := h.threadID.Load(); id != 0 {
		hotkey.PostThreadMessage(id, hotkey.WmQuit)
	}
}

// SetCapture 设置页进入 / 离开快捷键录入态：进入即注销四项，离开即按当前配置注册回来。
//
// 为什么注销：全局热键是系统级独占的。录入时用户按下的那个组合键（往往就是四项之一）
// 会被系统直接投给热键线程并执行动作 —— 想录 Ctrl+Alt+P，结果真的推迟了一次休息，
// 输入框还收不到按键。只有让这几十毫秒里“系统里没有这个热键”，按键才会正常落到
// 前台窗口的输入框。
//
// 只存状态 + 叫醒线程：真正的 Register/Unregister 必须发生在消息循环线程上，
// 所以这里只投一条消息，由线程读 suspended 的最新值决定挂起还是恢复。
func (h *HotkeyManager) SetCapture(on bool) {
	h.suspended.Store(on)
	if id := h.threadID.Load(); id != 0 {
		hotkey.PostThreadMessage(id, hotkey.HkCaptureMsg)
	}
}

// applyCapture 线程内执行挂起 / 恢复。恢复复用 apply：配置可能在这期间被保存过，
// 重读一遍比记住“挂起前注册了什么”更不容易错。
func (h *HotkeyManager) applyCapture() {
	if h.suspended.Load() {
		h.unregisterAll()
		log.Printf("[hotkey] 录入中，四项已挂起（避免按下的组合键触发动作）")
		return
	}
	h.apply()
}

// apply 注销全部旧键后按当前配置重注册，并把逐项结果存快照 + 广播给前端。
// 注册失败（组合键被其他程序占用、两个动作配了同一个键）记日志但不影响其他键。
func (h *HotkeyManager) apply() {
	h.unregisterAll()
	// 录入中：保存设置 / 恢复默认触发的重注册必须等挂起结束再做，
	// 否则用户在输入框里按着键、后台一次保存就把四项注册回去，挂起形同虚设。
	if h.suspended.Load() {
		log.Printf("[hotkey] 录入中，本次重注册已推迟到挂起结束")
		return
	}
	a := h.app
	a.mu.Lock()
	sc, _ := a.cfg.Raw()["shortcuts"].(map[string]any)
	a.mu.Unlock()
	list, _ := h.register(sc, "")
	// 一行收尾日志：全成功时上面没有任何 [hotkey] 行，“什么都没打”既可能是成功
	// 也可能是消息循环压根没跑，真机排查时分不出来（probe_windows_e2e 的 C 段就看这行）。
	ok, conflict, disabled := 0, 0, 0
	for _, st := range list {
		switch st.State {
		case hotkeyStateOK:
			ok++
		case hotkeyStateConflict, hotkeyStateInvalid:
			conflict++
		case hotkeyStateDisabled:
			disabled++
		}
	}
	log.Printf("[hotkey] 注册完成：%d/%d 项可用（冲突 %d、禁用 %d）", ok, len(list), conflict, disabled)
	h.publish(list)
}

// planHotkey 判定一项配置在**注册之前**就能确定的部分，并给出解析好的修饰键 / 主键。
//
// 返回值 State 为空 = “这项得真去 RegisterHotKey 才能知道结果”（ok / conflict 由调用方填）。
// 单独抽出来是为了能单测：disabled / invalid 两个分支不该依赖真机注册热键
// （跑测试时抢用户的全局键是不可接受的副作用）。
func planHotkey(key, accel string) (st HotkeyStatus, mods, vk uintptr) {
	s := strings.TrimSpace(accel)
	st.Key = key
	st.Accel = s
	if s == "" {
		st.State = hotkeyStateDisabled // 留空 = 用户主动禁用该项
		return
	}
	mods, vk, ok := hotkey.ParseAccelerator(s)
	if !ok {
		log.Printf("[hotkey] %s=%q 无法识别，已忽略（需要至少一个修饰键 + 可识别的主键）", key, s)
		st.State = hotkeyStateInvalid
		return
	}
	return st, mods, vk
}

// publish 存下快照、广播给所有窗口、再叫醒正在等待的“检测”调用方。
//
// 顺序有讲究：先 Emit 再送 waiter —— 点击检测的那一路会**整份**写结果（成功也写绿字），
// 让它在广播之后落地，就不会被广播（只标问题、成功行清空）抢先擦掉绿字。
// 两者拿到的是同一份 list，前端也另有一层静默期兜底（见 settings.js 的 hotkeyAutoMuteUntil）。
func (h *HotkeyManager) publish(list []HotkeyStatus) {
	h.record(list)
	h.mu.Lock()
	waiters := h.waiters
	h.waiters = nil
	h.mu.Unlock()

	if h.app != nil && h.app.app != nil {
		// 广播包一层 GuardRun：它是“通知”，不该有能力打断后面的 waiter 唤醒 ——
		// 这里 panic 一次，点检测的人就要白等 3 秒然后拿到“未取到结果”。
		logx.GuardRun("hotkeys.emit", func() { h.app.app.Event.Emit(hotkeyStatusEvent, list) })
	}
	for _, ch := range waiters {
		select {
		case ch <- list:
		default: // 缓冲 1 已满（同一调用方重复登记）：丢掉旧值，结果都一样
		}
	}
}

// record 只更新快照，不广播。回滚路径用它：那是一次内部还原，
// 结论由 SetShortcut 的返回值单独告诉前端，再广播一份会两边抢着解释同一件事。
func (h *HotkeyManager) record(list []HotkeyStatus) {
	h.mu.Lock()
	h.status = list
	h.applied = true
	h.mu.Unlock()
}

// snapshot 最近一次注册结果。未跑过 apply 时返回空数组（前端据此什么都不画）。
func (h *HotkeyManager) snapshot() []HotkeyStatus {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.applied {
		return nil
	}
	out := make([]HotkeyStatus, len(h.status))
	copy(out, h.status)
	return out
}

// check 请求消息循环线程按当前配置重新注册一遍，并等它把逐项结果送回来。
//
// 为什么“检测冲突”就是重新注册：RegisterHotKey 对已被**其他进程**占用的组合键返回 0，
// 这是 Windows 上唯一可靠的判据（系统没有提供“查询某组合键归谁”的 API）。
// 借一次注销 + 重注册就能同时得到一个如实的结果，副作用是这几十毫秒内热键短暂失效。
func (h *HotkeyManager) check() []HotkeyStatus {
	// 检测 = “按当前配置注册一遍”，前提是没在挂起。点检测按钮时输入框已经失焦，
	// 正常也轮不到这里兜底；留着是为了不依赖前端 IPC 的先后顺序 ——
	// 挂起状态下 apply 会跳过注册，check 就会白等到 3 秒超时。
	if h.suspended.Swap(false) {
		log.Printf("[hotkey] 检测前解除录入挂起")
	}
	id := h.threadID.Load()
	if id == 0 {
		// 消息循环还没就绪（启动瞬间点检测）：先给现有快照，不编造结果
		return h.snapshot()
	}
	ch := make(chan []HotkeyStatus, 1)
	h.mu.Lock()
	h.waiters = append(h.waiters, ch)
	h.mu.Unlock()

	hotkey.PostThreadMessage(id, hotkey.HkReloadMsg)
	select {
	case list := <-ch:
		return list
	case <-time.After(hotkeyCheckTimeout):
		// 返回 nil（而不是最近一次快照）：超时意味着这一轮**没有**结论，
		// 拿旧结果顶上去等于把“没测成”显示成“测得的结果”，正好是要避免的那种谎报。
		// 前端拿到 null 会写“未取到结果，请重试”。
		log.Printf("[hotkey] 检测超时（%s），本轮无结论", hotkeyCheckTimeout)
		return nil
	}
}

// requestCandidate 让消息循环线程按候选配置真注册一遍，并等它把逐项结果送回来。
//
// 参数与 channel 挂在结构体上、消息只负责叫醒线程：与 SetCapture 同一套路，
// 避免线程 busy 时多条消息乱序，把参数配给了另一次的 channel。
//
// 第二个返回 false = 线程未就绪 / 超时，本轮没有结论。调用方按“测不了”处理
// （直接落盘），不能把它当成冲突 —— 否则消息循环还没起来时一个键都改不了。
func (h *HotkeyManager) requestCandidate(key string, cand map[string]any) (candidateOutcome, bool) {
	id := h.threadID.Load()
	if id == 0 {
		return candidateOutcome{}, false
	}
	ch := make(chan candidateOutcome, 1)
	h.candMu.Lock()
	h.candSc = cand
	h.candKey = key
	h.candCh = ch
	h.candMu.Unlock()

	hotkey.PostThreadMessage(id, hotkey.HkCandidateMsg)
	select {
	case out := <-ch:
		return out, true
	case <-time.After(hotkeyApplyTimeout):
		h.candMu.Lock()
		if h.candCh == ch { // 还没被线程取走：撤回，别让它回传到没人听的 channel
			h.candSc = nil
			h.candKey = ""
			h.candCh = nil
		}
		h.candMu.Unlock()
		log.Printf("[hotkey] 改键等待超时（%s），本轮无结论", hotkeyApplyTimeout)
		return candidateOutcome{}, false
	}
}

// applyCandidate 线程内按候选配置注册四项。不可用时**立刻还原**成当前配置。
//
// 关键点：不可用时不能只“回传失败”就完事 —— 那一刻系统里的热键已经被换成候选
// 配置了，必须当场按磁盘上仍然有效的旧配置注册回去，否则用户会看到“提示说没改成，
// 但按旧键没反应”。还原走静默 record（不广播）：结论由 SetShortcut 的返回值带给前端，
// 再广播一份会和它抢着解释同一件事。
func (h *HotkeyManager) applyCandidate() {
	h.candMu.Lock()
	cand := h.candSc
	key := h.candKey
	ch := h.candCh
	h.candSc = nil
	h.candKey = ""
	h.candCh = nil
	h.candMu.Unlock()
	if ch == nil {
		return
	}
	// 不碰 suspended：录入期间四项本来就是挂起的，而这里本来就要“真的注册”。
	// 若强行把挂起清掉，用户手快接着点下一个输入框的那几十毫秒里，按键又会被
	// 系统投给热键线程执行动作。挂起状态完全交给前端的 focus / blur 控制。
	prevList := h.snapshot()
	h.unregisterAll()
	list, target := h.register(cand, key)

	blame := "" // 被这次改动挤掉的另一项（正常情况下前端本地校验就挡住了）
	if target.State != hotkeyStateOK && target.State != hotkeyStateDisabled {
		blame = target.Key
	} else if k := newlyBroken(prevList, list, target.Key); k != "" {
		blame = k
	}
	if blame != "" {
		log.Printf("[hotkey] 拒绝改键 %s=%q（%s 不可用），已还原配置", target.Key, target.Accel, blame)
		a := h.app
		a.mu.Lock()
		cur, _ := a.cfg.Raw()["shortcuts"].(map[string]any)
		a.mu.Unlock()
		h.unregisterAll()
		restored, _ := h.register(cur, "")
		h.record(restored)
		list = restored
	}
	// 录入期间仍处于挂起态：把刚刚真注册上去的键再收走。
	// 挂起的全部意义就是“系统里没有我们的热键”，否则用户在输入框里继续按键时
	// 会被系统投给热键线程执行动作（正是这一轮要修的问题）。前端 blur 时会 apply 回来。
	if h.suspended.Load() {
		h.unregisterAll()
	}
	ch <- candidateOutcome{list: list, target: target, blame: blame}
}

// register 按给定 shortcuts 子表注册四项（**不**先注销，调用方负责）。
// 返回逐项结果与 key 对应的那一项。
func (h *HotkeyManager) register(sc map[string]any, key string) ([]HotkeyStatus, HotkeyStatus) {
	list := make([]HotkeyStatus, 0, len(hotkeyDefs))
	var target HotkeyStatus
	for _, d := range hotkeyDefs {
		s, _ := sc[d.key].(string)
		st, mods, vk := planHotkey(d.key, s)
		if st.State == "" { // 能走到注册这一步
			if !hotkey.RegisterHotKey(d.id, mods, vk) {
				log.Printf("[hotkey] %s=%q 注册失败（组合键可能已被其他程序占用）", d.key, strings.TrimSpace(s))
				st.State = hotkeyStateConflict
			} else {
				st.State = hotkeyStateOK
			}
		}
		if d.key == key {
			target = st
		}
		list = append(list, st)
	}
	return list, target
}

// newlyBroken 候选配置把哪一项从“本来能用”挤成了不可用。
//
// 只看目标项不够：用户把 A 改成 B 已经在用的键时，先注册的 A 会成功、B 会失败，
// 而“有冲突就不改”的本意是**整体放弃**，不能让原有的 B 悄悄失效。
func newlyBroken(prev, next []HotkeyStatus, key string) string {
	was := make(map[string]string, len(prev))
	for _, st := range prev {
		was[st.Key] = st.State
	}
	for _, st := range next {
		if st.Key == key {
			continue
		}
		if st.State != hotkeyStateConflict && st.State != hotkeyStateInvalid {
			continue
		}
		if was[st.Key] == hotkeyStateOK || was[st.Key] == hotkeyStateDisabled {
			return st.Key
		}
	}
	return ""
}

// HotkeyStatus 最近一次注册的逐项结果（设置页打开时拉一次：启动时的注册早于页面加载，
// 事件已经错过了）。server / 无头模式没有热键管理器，回一份 unsupported 让前端说实话。
func (a *App) HotkeyStatus() []HotkeyStatus {
	if a.hotkeys == nil {
		return unsupportedHotkeyStatuses()
	}
	return a.hotkeys.snapshot()
}

// CheckHotkeyConflicts 强制重新注册一遍并返回逐项结果（设置页「检测冲突」按钮）。
func (a *App) CheckHotkeyConflicts() []HotkeyStatus {
	if a.hotkeys == nil {
		return unsupportedHotkeyStatuses()
	}
	return a.hotkeys.check()
}

// SetHotkeyCapture 设置页进入 / 退出快捷键录入态（true = 录入中，四项暂时注销）。
//
// 前端在输入框 focus / blur 时调用。没有热键管理器（server、非 Windows、启动失败）时
// 静默返回：那些环境下本来就没有热键会抢按键。
func (a *App) SetHotkeyCapture(on bool) {
	if a.hotkeys == nil {
		return
	}
	a.hotkeys.SetCapture(on)
}

// SetShortcut 改一项全局快捷键：**先真的按候选配置注册，注册上了才写配置**。
//
// 这是“有冲突就不要修改原来的快捷键”的实现方式。旧做法是先写配置、再重注册、
// 失败后回滚 —— 那在视觉上就是“先存再报错”，而且中间那一小段时间里配置文件里
// 确实躺着一个注定失效的键（用户若此刻退出，坏值就留下了）。
//
// 现在的顺序：
//  1. 消息循环线程按候选配置注册四项（RegisterHotKey 是唯一可靠判据 ——
//     Windows 没有“查询某组合键归谁”的接口）；
//  2. 目标项注册成功、且没有把另一项挤掉 → 只写 shortcuts 子树；
//  3. 任一不成立 → 线程内当场按旧配置注册回去，配置文件一个字节都不动。
//
// 返回的 List 是**收尾之后**的真实状态，前端不必猜“现在到底哪几项生效”。
func (a *App) SetShortcut(key, accel string) ShortcutApplyResult {
	key = strings.TrimSpace(key)
	accel = strings.TrimSpace(accel)
	res := ShortcutApplyResult{Key: key, Accel: accel}

	// 键名白名单：前端传错也不该让任意键名进配置文件
	known := false
	for _, d := range hotkeyDefs {
		if d.key == key {
			known = true
			break
		}
	}
	if !known {
		res.State = hotkeyStateInvalid
		res.List = unsupportedHotkeyStatuses()
		return res
	}

	a.mu.Lock()
	cur, _ := a.cfg.Raw()["shortcuts"].(map[string]any)
	cand := make(map[string]any, len(cur)+1)
	for k, v := range cur {
		cand[k] = v
	}
	prev, _ := cand[key].(string)
	cand[key] = accel
	a.mu.Unlock()
	res.Kept = strings.TrimSpace(prev)

	// 格式问题不必上线程，解析就能定；而且这类键**绝不落盘**
	if accel != "" {
		if _, _, ok := hotkey.ParseAccelerator(accel); !ok {
			res.State = hotkeyStateInvalid
			if a.hotkeys != nil {
				res.List = a.hotkeys.snapshot()
			} else {
				res.List = unsupportedHotkeyStatuses()
			}
			return res
		}
	}

	// 没有热键能力（server / 非 Windows / 启动失败）：本机不存在“被占用”这回事，
	// 直接落盘，行为与加这个接口之前一致。
	if a.hotkeys == nil {
		if accel == "" {
			res.State = hotkeyStateDisabled // 清空不需要热键能力
		} else {
			res.State = hotkeyStateUnsupported
		}
		res.OK = a.saveShortcutsOnly(cand)
		res.List = unsupportedHotkeyStatuses()
		return res
	}

	out, done := a.hotkeys.requestCandidate(key, cand)
	if !done {
		// 线程未就绪 / 超时：测不了。按老行为直接保存 —— 测不了不等于有冲突，
		// 否则消息循环没起来时用户一个键都改不了。
		res.OK = a.saveShortcutsOnly(cand)
		if a.hotkeys != nil {
			a.hotkeys.reload()
		}
		return res
	}

	res.List = out.list
	res.State = out.target.State
	if out.blame != "" {
		// 目标项不可用，或把另一项挤成了冲突：线程内已按旧配置还原，配置不动
		res.State = out.target.State
		if res.State == "" || res.State == hotkeyStateOK || res.State == hotkeyStateDisabled {
			// 目标项自己没问题、是挤掉了别人：对用户而言仍然是“没改成”
			res.State = hotkeyStateConflict
		}
		return res
	}

	if !a.saveShortcutsOnly(cand) {
		// 注册成功但落盘失败（磁盘满 / 只读）：把系统里的键退回旧配置，
		// 别让“配置是旧的、系统里却是新的”这种状态留下来。
		log.Printf("[hotkey] 改键落盘失败，已还原注册")
		a.hotkeys.reload()
		res.State = ""
		return res
	}

	res.OK = true
	// 广播一次：其它窗口 / 页面加载用的状态源要跟着更新。成功时没有冲突项，
	// 前端不会因此多弹一条汇总（见 settings.js 的 onHotkeyStatus）。
	a.hotkeys.publish(out.list)
	return res
}

// unsupportedHotkeyStatuses 没有热键能力时，仍按四个定义回形状完整的结果，
// 前端不必为“空数组”和“不可用”写两套分支。
func unsupportedHotkeyStatuses() []HotkeyStatus {
	out := make([]HotkeyStatus, 0, len(hotkeyDefs))
	for _, d := range hotkeyDefs {
		out = append(out, HotkeyStatus{Key: d.key, State: hotkeyStateUnsupported})
	}
	return out
}

func (h *HotkeyManager) unregisterAll() {
	for _, d := range hotkeyDefs {
		hotkey.UnregisterHotKey(d.id) // 未注册的 id 返回 0，无害
	}
}

func (h *HotkeyManager) dispatch(id int) {
	for _, d := range hotkeyDefs {
		if d.id == id {
			d.action(h.app)
			return
		}
	}
}
