// Package scheduler 是休息调度状态机：每秒一拍地推进“还剩多久休息、要不要弹窗、
// 用户暂停/跳过/推迟之后时钟怎么走”，并维护提前提示与外卖提醒的一次性触发点。
//
// 它持有状态（倒计时锚点、跳过集合、暂停时刻）并加锁，但**不直接操作窗口**：所有
// 需要界面动作的时刻都以回调/返回值交给 cmd 层执行，这样状态机可以在无 GUI 的测试里
// 把跨天、暂停处置、提前线这些场景跑完。
package scheduler

import (
	"log"
	"strings"
	"sync"
	"time"
	"workerrest/internal/logx"
	"workerrest/internal/model"
	"workerrest/internal/plan"
	"workerrest/internal/settings"
)

// Host 调度器对宿主的依赖（生产是 *App，单测传假实现）。方法必须大写（导出）：
// 跨包实现接口时，未导出的方法名只在本包可见，main 包的 *App 无法满足小写方法。
type Host interface {
	CfgSnapshot() *settings.Settings
	OnSchedulerState()
	OnPreNotify(kind string, notifyBeforeMs int64)
	// OnPaused 手动暂停（用户自己按的）进入暂停：收掉提前提示窗；正在进行的休息按它自己
	// 卡里的“暂停休息时”处置（推迟 → 剩余时间冻结挂着；正常提醒 → 照常走完；跳过 → 就此
	// 结束，见 pauseRunningBreak）。preKind 是调度器这边在提示的那条提醒的身份，只在该窗
	// 自己没带身份时兜底 —— 留不留由宿主按窗自己的归属判，见 AutoPauseKeep 的说明。
	OnPaused(preKind string)
	// IdleMs 系统级“鼠标键盘连续无活动”时长（毫秒）：空闲门（等用户离开工位才提醒）
	// 的唯一读数。读不到系统输入 tick 的机器上恒为 0 —— 那种机器上空闲门永远不达标，
	// 表现是“一直等待空闲”，所以设置页对空闲门给的默认是不勾。
	IdleMs() int64
	// OnAutoPause 自动原因（空闲 / 勿扰 / 全屏 / 投屏 / 应用占用）进入暂停：收掉此刻
	// 已经在屏幕上的提醒窗口 —— 休息窗、提前提示窗、外卖提醒窗与外卖预通知窗。
	// 与 OnPaused 的区别：那个只收提前提示窗（手动暂停时休息窗要继续挂着走完）。
	//
	// 哪些窗要留由**每扇窗自己的归属**决定（判据只有一条：那条提醒自己的“暂停休息时”
	// 是不是“正常提醒”），keep 只交代身份，见 AutoPauseKeep。
	OnAutoPause(keep AutoPauseKeep)
	ClosePreBreak()
	ShowBreakSession(kind string, preview bool, forceKey string) string
	CloseBreakWindows()
	// CompleteBreakWindows 与 CloseBreakWindows 同义，只是**按“完成”记账**：
	// 收窗、清会话、照常记 break-end，但不判“提前结束”、也不播结束提示音。
	//
	// 唯一用途是“短休息正进行、长休息到点”的抢占（2026-11-13 用户口径）：
	// 短休息立刻停下换成这次长休息 —— 它确实被长休息顶替了，不是用户提前溜号，
	// 统计上应当记“完成 1 次”；而且紧接着就开始长休息，再响一遍结束音只是噪音。
	CompleteBreakWindows()
	// OnBreakExtra 休息进行中，另一条提醒到点了：把它挂到**当前这次休息**上
	//（单窗多条，见 model.BreakExtra），不要另开窗口、也不要等休息结束补弹。
	// kind 是那条提醒的身份（自定义提醒 / takeout），at 是这一班原本的时刻。
	// 返回 false 表示宿主当前没有可挂的休息会话（窗口被 Alt+F4 掉 / 建窗失败）：
	// 这种情况下调度器**不动**这一班，让它之后按正常路径再提醒一次 —— 先消费再发现
	// 没地方显示，就是又一次静默丢失。
	OnBreakExtra(kind string, at int64) bool
	// OnBreakCounted 记一次“没有单独跑过、但算完成”的内置休息（只有 short）。
	// 用在“短休息被长休息覆盖”的场景：时长不记（本来就没坐），次数 +1。
	OnBreakCounted(kind string)
	TakeoutPostponedSnapshot() *model.TakeoutPostponed
	TakeoutFiredSnapshot() map[string]bool
	// TakeoutIdleWaitSnapshot 外卖“到点了、还在等空闲”的那一笔（nil = 没有）。
	// 外卖不走调度器，但状态卡上它和自定义提醒共用一个格子，所以由宿主报给快照。
	TakeoutIdleWaitSnapshot() *model.TakeoutIdleWait
	// 推迟 / 跳过休息时各记一笔统计（对应 上一版的 scheduler.on('postpone' / 'skip')）
	OnBreakPostponed(kind string, postponeMs int64)
	OnBreakSkipped(kind string, skippedMs int64)
}

// AutoPauseKeep 进入暂停的那一刻，调度器这边**在场窗口的归属**（不是结论）。
//
// 判据只有一条：那扇窗所属提醒自己的“暂停休息时”是不是“正常提醒”（`pauseAction=remind`）。
// 暂停原因一概不参与 —— 手动暂停、空闲、勿扰 / 专注、全屏 / 演示、无线投放、复制投影、
// 指定应用占用，全都一样。用户口径（2026-11-12）：「所有提醒，如果该提醒的“暂停休息时”
// 选的是“正常提醒”，就不要收该提醒窗。不存在例外。」理由很直：选了“正常提醒”就是要求
// 照常提醒，正在屏幕上的那扇窗正是“照常”的现场，收掉等于把这条配置偷偷降级。
//
// **结论由宿主按每扇窗自己的归属算**（settings.BreakPauseRemind）：窗是宿主建的，只有它
// 知道屏幕上那扇提示窗此刻属于哪条提醒 —— 暂停中照常弹出来的自定义提醒就会让提示窗与
// “调度器此刻在等的那条”不是同一个身份。这里只交代“我知道的那条是谁”，供宿主在窗上没带
// 身份时（老会话 / 单测构造）兜底；外卖全归外卖卡那张配置，直接给结论。
type AutoPauseKeep struct {
	// BreakKind 正在进行的那次休息的身份（Phase=break 时非空）
	BreakKind string
	// PreKind 提前提示窗提示的那条提醒的身份（Phase=pre 时非空）
	PreKind string
	// Takeout 外卖提醒窗与外卖预通知窗留不留（全部归外卖卡自己的“暂停休息时”）
	Takeout bool
}

type Scheduler struct {
	host Host

	mu    sync.Mutex
	state model.SchedulerState
	// consumed 已消费的触发线 occurrence（键 = occurrenceKey）：开始一次休息
	// （无论到点触发还是“立即休息”）就把它记下，replan 时不再返回同一次安排。
	// 否则固定时刻 12:00 的休息在提前窗点“立即休息”、又马上“提前结束”后，
	// replan 会把还没到点的 12:00 原样排回来——提前窗与提醒窗各再弹一次。
	// “跳过”同样消费当前 occurrence（固定线不消费的话，跳过等于没跳）。
	consumed map[string]bool
	// anchorIv 各倒计时身份当前锚点的产生间隔（ms，键 = 休息身份）：replan 沿用旧锚点
	// 的判据之一 —— 间隔没变才沿用，改了间隔立刻按新间隔重新起算（见 countdownAnchor）。
	// 每次 replan 按新池同步（syncAnchorIvLocked）；reanchorCountdown / resetCycle 各自维护。
	anchorIv map[string]int64
	// pauseSkipped 暂停期间已按“跳过”记过账的 occurrence（键 = occurrenceKey）。
	// 暂停态每半秒一跳都会重算“有没有到点的自定义提醒”，没有这份去重会重复记账；
	// 完全恢复后清空（下一段暂停重新计），键里带时刻本身也不会串到别的 occurrence。
	pauseSkipped map[string]bool
	// pauseFired 暂停期间“正常提醒”的自定义提醒已照常弹过的 occurrence
	//（键 = occurrenceKey；提前提示小窗另加 "|pre" 后缀，与真窗分开去重）。
	// 与 pauseSkipped 同生命周期：完全恢复时一起清空。
	pauseFired map[string]bool
	// postponed 暂停期间被“推迟提醒”推后的 occurrence（键 = occurrenceKey，值 = 推到
	// 哪一刻）。这份表**不随 resume 清空**：推迟是延后，恢复后还要按推迟后的时刻弹。
	// 暂停原因不影响它：自动暂停与手动暂停一样，配了推迟就往后推（2026-11-12 起没有
	// “自动暂停只跳过”的例外）。
	postponed map[string]*postponedBreak
	// pauseHadAuto 这一段暂停里出现过至少一条自动原因（非 manual）。
	// **只用于表达“这段暂停不是用户按的”**（暂停提示 pause_notice.go 据此决定要不要提醒
	// “已暂停多久”）。它不参与任何处置，也不影响恢复时的时钟：暂停原因与处置无关，
	// 到点怎么办只看各卡的“暂停休息时”（2026-11-12 口径）。
	pauseHadAuto bool
	// idleWait 正在等空闲的那一跳（见 idleGate.go）。nil = 没有在等。
	idleWait *idleWait
	// extraMerged 本次休息里已经“并入”过的提醒身份（键 = 休息身份）：一次休息只并入
	// 同一条提醒一次，长休息 20 分钟碰上 5 分钟一次的喝水提醒时，不给用户堆四张一样的卡。
	// 每次开始 / 结束休息都重置（见 startBreakAt / finishBreak）。
	extraMerged map[string]bool

	stopOnce sync.Once
	stopCh   chan struct{}

	// lastTickAt 上一次 onTick 的墙钟时刻：用来发现“两次 tick 之间过了很久”——
	// 睡眠 / 休眠 / 系统时间被大幅调整时，倒计时会在醒来第一刻全部到点，
	// 不处理就会立刻弹一个休息窗口（用户刚开机就被要求休息）。
	lastTickAt int64
}

// postponedBreak 暂停期间被“推迟提醒”推后的一次自定义提醒。
// at = 原始 occurrence 时刻（startBreakAt 的 consumed 去重键用），dueAt = 推迟后的弹出时刻。
type postponedBreak struct {
	kind  string
	at    int64
	dueAt int64
}

// nowMs 当前时刻（毫秒时间戳，与 JS 的 Date.now() 同基准）
func nowMs() int64 { return time.Now().UnixMilli() }

// PauseReasonIdle 空闲暂停的原因键。它唯一不参与“用户仍在用电脑”的 busy 判定
// （见 pausedForBusyReason）：手动 / 勿扰 / 指定应用占用都会让空闲判定失效，
// 只有 idle 自己能解开离开态。触发方（main 包的系统监视）用这个常量传 pause/resume。
const PauseReasonIdle = "idle"

// PauseReasonManual 手动暂停的原因键（托盘 / 设置页 / 快捷键点的“暂停休息”）。
// 它与上面那条 idle 是仅有的两个由调度器自己命名的键；勿扰 / 全屏 / 投屏 / 应用占用
// 的原因名定义在 main 包的 sys_watch.go（那边负责探测，名字就跟着探测走）。
//
// “是不是自动原因”的判据只有一个：**不等于 manual 就是自动**。它决定两件事 ——
// 进入暂停那一刻要不要清场（收掉屏幕上的窗口，见 Pause）、以及暂停提示要不要催用户
// （见 pause_notice.go）。**不决定暂停期间到点的提醒怎么处置**：那看各卡自己的
// “暂停休息时”，与原因无关（2026-11-12 用户口径：“不存在例外”）。
const PauseReasonManual = "manual"

// IsAutoPauseReason 这条原因是否由自动判定挂上（即不是用户手动按下暂停）。
//
// 判据集中在函数里而不是散着写 `reason != "manual"`：新增原因时默认落在自动一侧
// （进暂停就清场），要归入手动必须显式把它加进这里的例外。
//
// 注意它与“条件消失是否自动恢复”**不是同一件事**：那是 App 层的
// autoResumeOnClear（见 cmd/workerrest/sys_watch.go），空闲与应用占用即使自动恢复，
// 也仍然是自动原因。
func IsAutoPauseReason(reason string) bool {
	return reason != PauseReasonManual
}

func max0(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}

// isCustomBreakID 是否是自定义提醒的身份（内置短 / 长休息不算）
func isCustomBreakID(id string) bool { return strings.HasPrefix(id, settings.CustomBreakPrefix) }

// isBuiltinBreakID 是否是内置短 / 长休息的身份
func isBuiltinBreakID(id string) bool { return id == "short" || id == "long" }

// New 建一个调度器（生产传 *App，单测传假宿主）
func New(host Host) *Scheduler {
	s := &Scheduler{
		host:         host,
		consumed:     map[string]bool{},
		anchorIv:     map[string]int64{},
		pauseSkipped: map[string]bool{},
		pauseFired:   map[string]bool{},
		postponed:    map[string]*postponedBreak{},
		stopCh:       make(chan struct{}),
	}
	s.state = model.SchedulerState{
		Phase:        "idle",
		NextType:     "short",
		NextName:     plan.ShortBreakName,
		Lines:        []model.PlannedBreak{},
		Timeline:     []model.TimelineItem{},
		FixedNext:    map[string]int64{},
		PauseReasons: []string{},
	}
	return s
}

// Stop 停止调度循环（退出 / 测试收尾）
func (s *Scheduler) Stop() {
	s.stopOnce.Do(func() { close(s.stopCh) })
}

// Start 启动调度循环：重置周期后跑后台循环（生产启动时调一次）。循环体在 Loop 内，
// 每一跳都由 logx.GuardRun 兜底，这里只负责“重置 + 起 goroutine”。
func (s *Scheduler) Start() {
	s.ResetCycle()
	go s.Loop()
}

// Loop 半秒一跳。上一版 里是两个各 500ms 的定时器（主 tick + 休息窗口到点收窗），
// 这里合成一跳：休息进行中只看向“该收窗了没”，其余情况走主 tick。
func (s *Scheduler) Loop() {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			// 包一层 panic 兜底：调度器是整程序的心跳，一跳里的 nil deref 不该让
			// GUI 直接消失（见 recoverguard.go）。丢一拍，下半秒继续。
			logx.GuardRun("scheduler.tick", s.onTick)
		}
	}
}

// breakCloseDwellMs：能量蓄满（休息自然到点）后让提醒窗口多停留的毫秒数，
// 让用户看清“满罐”状态再关窗。只作用于自然到点（onTick 的 due 判定），
// 手动“提前结束”走 endBreak → finishBreak 不经过这里，立即关窗不受影响。
const breakCloseDwellMs int64 = 1000

// tickJumpThresholdMs 两次 tick 的墙钟间隔超过它，就认为中间“时间大跳”过
// （睡眠 / 休眠 / 系统时间被调整）。与 stats 的睡眠判定同阈值：60 秒足够挡掉
// GC、磁盘、系统卡顿这类正常抖动。
const tickJumpThresholdMs = 60_000

func (s *Scheduler) onTick() {
	// 时间大跳：这一跳不判任何“到点”——睡眠期间过期的倒计时不该在醒来第一刻补弹，
	// 统一重排（内置 / 自定义倒计时从头计时，固定线顺延到下一个时刻）。
	// 暂停中也重排：暂停不冻钟，重排按真实墙钟重新起算，正好是“醒来不再欠着几次休息”。
	tickNow := nowMs()
	s.mu.Lock()
	last := s.lastTickAt
	s.lastTickAt = tickNow
	s.mu.Unlock()
	if last > 0 && tickNow-last > tickJumpThresholdMs {
		log.Printf("[scheduler] 两次 tick 间隔 %d ms（睡眠 / 休眠 / 系统时间调整）：重排，不补弹过期提醒",
			tickNow-last)
		// 挂起线要一起清掉：倒计时锚点的沿用规则会把“已经到点的那一跳”原样留着
		//（见 prevCountdownAnchorsLocked），不清的话醒来第一拍就会拿一条几小时前
		// 到点的线去弹窗 —— 正是这个分支要避免的事。
		s.mu.Lock()
		s.state.Lines = []model.PlannedBreak{}
		s.anchorIv = map[string]int64{}
		s.mu.Unlock()
		s.scheduleNext()
		return
	}
	s.mu.Lock()
	paused := s.state.Paused
	inBreak := s.state.Phase == "break"
	clockFrozen := s.pauseFreezesClockLocked()
	// 蓄能容器：到点（能量蓄满）后多停留 breakCloseDwellMs 再关窗
	breakDue := inBreak && s.state.BreakEndsAt > 0 && nowMs() >= s.state.BreakEndsAt+breakCloseDwellMs
	// 暂停期间：四类提醒都按“暂停休息时”的处置分派 —— 跳过（pausedSkip 记一笔跳过）、
	// 正常提醒（pausedFire 照常弹窗）、推迟提醒（pausedPostpone 往后推，推完仍暂停就再推）。
	// 短 / 长休息 2026-11-12 起也参与（用户要求与外卖提醒、自定义提醒一致）。
	//
	// **暂停原因不影响处置**（2026-11-12 用户口径：“不存在例外”）：手动暂停、系统空闲、
	// 勿扰/专注、全屏/演示、投屏、指定应用占用，统统按这张卡里的配置走。这里曾有一条例外
	//（自动暂停一律按跳过、不看配置），已删除。
	if paused {
		s.mu.Unlock()
		if inBreak {
			// 暂停期间弹出来的自定义提醒仍要按点收窗，否则窗口会一直挂着；
			// 暂停中“立即休息”主动开始的内置休息同理（clockFrozen=false）——
			// 少这一条，内置休息窗口会一直挂到用户恢复暂停为止。
			// 配“正常提醒”的休息碰上进来的自动暂停也一样：clearForAutoPause 把
			// BreakWhilePaused 立起来，它继续按真实时钟走，到点在这里收窗。
			// 其余“暂停前就在进行的短 / 长休息”按它自己的“暂停休息时”＝推迟办了：
			// 剩余时间冻结（pauseFreezesClockLocked），恢复后接着走完 —— 所以这里不收窗。
			if breakDue && !clockFrozen {
				s.finishBreak()
			}
			return
		}
		// 等待空闲的那一跳碰上“系统空闲自动暂停”不算拦阻：人已经离开工位达到设定时长，
		// 正是这次提醒要等的那一刻（详见 idleGate.go 的 fireIdleGateWhileIdlePaused）。
		if s.fireIdleGateWhileIdlePaused() {
			return
		}
		s.pausedSkip()
		s.pausedFire()
		s.pausedPostpone()
		return
	}
	// 休息进行中：先处理“撞车”，再看该不该收窗。
	//
	// 撞车分两种（2026-11-13 用户口径）：
	//   - 长休息到点、而正在休息的是别的（短休息 / 自定义）：立刻换成这次长休息，
	//     被停掉的那次记「完成」而不是「提前结束」（见 preemptShortWithLong）；
	//   - 其他提醒在休息期间到点：并入当前休息窗（单窗多条），不另开窗、也不补弹
	//     （见 mergeDueExtras）。
	//
	// 这里是原来最容易“静默丢一轮”的地方：旧实现直接 return，另一条提醒到点后既没人
	// 弹窗、也没人记账，倒计时锚点还会被重排成 now+interval，用户什么都察觉不到。
	if inBreak {
		s.mu.Unlock()
		if s.preemptShortWithLong() {
			return
		}
		s.mergeDueExtras()
		if breakDue {
			s.finishBreak()
		}
		return
	}
	// 暂停期间被“推迟提醒”推到现在的提醒：到点就弹（推迟是延后，不是吞掉）。
	// flushPostponed 自己加锁，这里先把进入本函数时拿的那把锁放开，之后再拿回来
	// 继续走下面的正常触发线。
	s.mu.Unlock()
	if s.flushPostponed() {
		return
	}
	// 空闲读数在 s.mu 之外取：Host 走 App，避免调度器锁与 App 锁交叉
	idleMs := s.host.IdleMs()
	s.mu.Lock()
	if s.state.NextAt == 0 {
		s.mu.Unlock()
		return
	}
	nextType := s.state.NextType
	nextAt := s.state.NextAt
	now := nowMs()
	// 提前把当前 NextType 的配置取出来：既用于“提前提示”阶段判定，也用于下面的
	// 禁用项竞态兜底（这条休息可能刚被用户在设置页关闭，replan 还没跑）。
	cfg, cfgOK := s.host.CfgSnapshot().BreakByID(nextType)
	preNotifyMs := int64(0)
	if s.state.Phase == "idle" {
		// 该提醒已被用户在设置页关闭（禁用项正常路径上由 replan 从挂起线剔除，
		// 这里再卡一道：既不进“提前提示”阶段、也绝不会到点触发，杜绝竞态下误提醒）。
		// now < nextAt：已到点就直接走下面的 due（开始休息），不再先弹一个 0 秒的提示窗。
		if cfgOK && cfg.Enabled && cfg.NotifyBeforeMs > 0 && now < nextAt && now >= nextAt-cfg.NotifyBeforeMs {
			s.state.Phase = "pre"
			preNotifyMs = cfg.NotifyBeforeMs
		}
	}
	due := now >= nextAt
	// 空闲门：到点先看“鼠标键盘连续无活动”够不够长（详见 idleGate.go）。
	// 不够 → 停在等待态；够了 → 把这一跳就地重排成 now+提前量，之后走原来的老链路。
	if due && cfgOK && cfg.Enabled {
		if gateMs := cfg.IdleGateWaitMs(); gateMs > 0 {
			if idleMs < gateMs {
				changed := s.idleWait == nil
				if changed {
					src := "countdown"
					if s.state.NextSource != nil {
						src = *s.state.NextSource
					}
					s.idleWait = &idleWait{kind: nextType, at: nextAt, source: src, name: s.state.NextName}
				}
				s.mu.Unlock()
				// 只在刚进入等待态时推一次状态：这之后的每一拍都在等同一个条件，
				// 半秒推一次会把设置页与托盘刷成动画。
				if changed {
					log.Printf("[scheduler] %s 已到点，等键鼠连续无活动 %d 秒后再提醒",
						nextType, gateMs/1000)
					s.emitState()
				}
				return
			}
			if s.armGatedBreakLocked(cfg, nextType, nextAt, now) {
				s.mu.Unlock()
				s.emitState()
				return
			}
			// 提前通知为 0：达标即弹正式提醒窗，落到下面的 startBreak
		}
	}
	s.mu.Unlock()

	if preNotifyMs > 0 {
		// 系统通知在 Windows 上无法带按钮，改用自绘提示弹窗（含倒计时与动作按钮）
		s.host.OnPreNotify(nextType, preNotifyMs)
	}
	if due {
		if cfgOK && !cfg.Enabled {
			// 配置刚改、replan 还没跑的竞态下，当前 NextType 已被禁用：重排，
			// 让它退出“下一个”位置，绝不误弹一个已关闭的提醒。
			s.scheduleNext()
		} else {
			s.startBreak()
		}
	}
}

// pausedSkip 暂停期间的“跳过提醒”：到点时不弹任何窗，直接按“跳过”处理
// （调一次 host.onBreakSkipped）。
//
// 暂停原因不参与判断（2026-11-12 用户口径：“不存在例外”，手动 / 空闲 / 勿扰 / 全屏 /
// 投屏 / 应用占用一律按各卡配置走）。这条路径里内置短 / 长休息与自定义提醒口径相同：
// 自定义提醒**不进统计**（统计页“休息”那一组四项只统计内置短休息与长休息，过滤集中在
// App.onBreakSkipped 一处，这里照旧发回调，免得调度器与宿主各判一次、口径漂移）；
// 内置短 / 长休息真的按“跳过”办 —— 推进短休息计数、把这一跳记进 consumed、记一笔跳过
// 统计，与用户手点“跳过”同一套状态变更（见 Skip），之后重排下一次。
//
// “推迟提醒”但“可推迟”填了 0 时也落到这里（没有时长可推，见 pauseDisposition）。
// 外卖不走调度器，它自己的每秒 tick 里做同样的事（见 takeout_fire.go）。
//
// 两条触发线的“到点”判定：
//   - 固定时刻线：按分钟匹配（isTimeSlotDue），暂停期间每个到点的时刻各记一次；
//   - 倒计时线：挂起线代表那一跳，At 已过就是到点了。暂停不冻钟，所以重排会把过期的
//     那一跳换成 now+interval（而不是原来那一跳）—— 这一跳因此只能被处置一次。
//
// 去重键是 occurrenceKey：暂停态每半秒一跳都会重算一次，没有去重会重复记账。
func (s *Scheduler) pausedSkip() {
	cfg := s.host.CfgSnapshot()
	now := nowMs()
	type hit struct {
		id     string
		name   string
		skipMs int64
	}
	var hits []hit
	// 一种身份在这一段暂停里只记一笔（固定时刻表里同一分钟有两个条目时
	// 不能让短休息计数跳两格），并且要重排一次计划
	handled := map[string]bool{}
	skipped := false
	s.mu.Lock()
	for _, e := range cfg.BreakEntries() {
		if !e.Config.Enabled {
			continue
		}
		builtin := isBuiltinBreakID(e.ID)
		if builtin && handled[e.ID] {
			continue
		}
		if pauseDisposition(e.Config) != settings.PauseActionSkip {
			continue
		}
		// 记账（消费这一跳、推进计数）；调用方已持锁
		note := func(key string) {
			s.pauseSkipped[key] = true
			// 这一跳真的用掉了：内置与自定义同口径，都要消费 + 摘掉挂起线。
			// 只记 pauseSkipped 不消费的话，replan 会沿着旧锚点把这一跳排回来
			//（跳过等于没跳），侧栏也会一直停在 0:00。
			s.consumed[key] = true
			keep := make([]model.PlannedBreak, 0, len(s.state.Lines))
			for _, l := range s.state.Lines {
				if l.Type == e.ID && l.At <= now {
					continue
				}
				keep = append(keep, l)
			}
			s.state.Lines = keep
			if builtin {
				s.advanceCounter(e.ID)
			}
			handled[e.ID] = true
			skipped = true
			hits = append(hits, hit{id: e.ID, name: e.Name, skipMs: e.Config.DurationMs})
		}
		if e.Config.Mode == "fixed" {
			for _, slot := range e.Config.Slots {
				if handled[e.ID] {
					break
				}
				if !plan.IsTimeSlotDue(slot, now, 0) {
					continue
				}
				key := plan.OccurrenceKey(e.ID, plan.SlotAlertAt(slot, 0, now))
				if s.pauseSkipped[key] {
					continue
				}
				note(key)
			}
			continue
		}
		for _, l := range s.state.Lines {
			if handled[e.ID] {
				break
			}
			if l.Type != e.ID || l.Source != "countdown" || l.At > now {
				continue
			}
			key := plan.OccurrenceKey(l.Type, l.At)
			if s.pauseSkipped[key] {
				continue
			}
			note(key)
		}
	}
	s.mu.Unlock()
	for _, h := range hits {
		log.Printf("[scheduler] 暂停休息期间 %s 到点，按跳过处理（%d ms）", h.name, h.skipMs)
		s.host.OnBreakSkipped(h.id, h.skipMs)
	}
	if skipped {
		// 这一跳已经用掉：重排。倒计时线的锚点见了 consumed 会按新间隔重新起算，
		// 固定时刻线则顺到下一个还没到的时刻（都在 scheduleNext → replan 里完成）。
		s.scheduleNext()
	}
}

// pauseFirePreSuffix 暂停期间“提前提示小窗”去重键的后缀：同一次 occurrence 的
// 真窗与提前提示各记一次，互不吞掉。
const pauseFirePreSuffix = "|pre"

// pausedFire 暂停期间“正常提醒”的提醒照常弹：固定时刻线到点弹提醒窗口、
// 落在提前区间弹提前提示小窗，倒计时线到点弹提醒窗口；不进跳过统计（与手动跳过区分开）。
//
// 2026-11-12 起短 / 长休息也走这里（“暂停休息时”= 正常提醒）：弹出来的休息窗与暂停中
// “立即休息”发起的那次同路（startBreakAt 里 BreakWhilePaused=true），照常走完自己的时长，
// onTick 的暂停分支会按点收窗。提前提示线不给内置（见下面的说明）。
//
// 每跳最多弹一条（与正常路径一致：一次只开一个提醒窗口），按到点时刻最早的优先，
// 其余的等这个窗口收掉后的下一跳再弹。去重表 pauseFired 与 pauseSkipped 同生命周期
// （完全恢复时一起清空），所以一段暂停里同一个 occurrence 只会弹一次。
func (s *Scheduler) pausedFire() {
	cfg := s.host.CfgSnapshot()
	now := nowMs()
	type cand struct {
		id  string
		at  int64
		key string
		pre bool
		nb  int64
	}
	var cands []cand
	s.mu.Lock()
	for _, e := range cfg.BreakEntries() {
		cc := e.Config
		if !cc.Enabled || pauseDisposition(cc) != settings.PauseActionRemind {
			continue
		}
		builtin := isBuiltinBreakID(e.ID)
		if cc.Mode == "fixed" {
			for _, slot := range cc.Slots {
				if !slot.Enabled {
					continue
				}
				at := plan.SlotAlertAt(slot, 0, now)
				if at <= 0 {
					continue
				}
				if plan.IsTimeSlotDue(slot, now, 0) {
					if key := plan.OccurrenceKey(e.ID, at); !s.pauseFired[key] {
						cands = append(cands, cand{id: e.ID, at: at, key: key})
					}
					continue
				}
				// 提前提示线只给自定义提醒。内置短 / 长休息在暂停里到点会按卡处置弹真窗
				// （见下面的倒计时分支），再补一个“还剩 N 秒”的小窗等于同一件事说两遍；
				// 而且小窗上的“现在 / 推迟 / 跳过”与暂停处置是两套口径，摆在一起容易点错。
				// 直接等真窗落下来更干净。
				if builtin {
					continue
				}
				// 提前提示线：occurrence 按 now+提前量 折算（提前量跨午夜时归次日那一格），
				// 与真窗用同一把键，才能正确区分“同一天的两个不同 occurrence”。
				if nb := cc.NotifyBeforeMs; nb > 0 && plan.IsTimeSlotDue(slot, now, nb) {
					preAt := plan.SlotOccurrenceAt(slot, nb, now)
					key := plan.OccurrenceKey(e.ID, preAt) + pauseFirePreSuffix
					if !s.pauseFired[key] {
						cands = append(cands, cand{id: e.ID, at: preAt, key: key, pre: true, nb: nb})
					}
				}
			}
			continue
		}
		// 倒计时线：挂起线里这条身份最早的一跳已过点即触发（暂停不冻钟，At 就是它
		// 本该提醒的那一刻）。
		for _, l := range s.state.Lines {
			if l.Type != e.ID || l.Source != "countdown" || l.At > now {
				continue
			}
			key := plan.OccurrenceKey(l.Type, l.At)
			if s.pauseFired[key] {
				continue
			}
			cands = append(cands, cand{id: e.ID, at: l.At, key: key})
		}
	}
	if len(cands) == 0 {
		s.mu.Unlock()
		return
	}
	// 取最早的一条（同一时刻时真窗优先于提前提示），只消费这一条的去重键
	pick := cands[0]
	for _, c := range cands[1:] {
		if c.at < pick.at || (c.at == pick.at && pick.pre && !c.pre) {
			pick = c
		}
	}
	s.pauseFired[pick.key] = true
	s.mu.Unlock()
	if pick.pre {
		log.Printf("[scheduler] 暂停休息期间 %s 进入提前提示区间，照常提醒", pick.id)
		s.host.OnPreNotify(pick.id, pick.nb)
		return
	}
	log.Printf("[scheduler] 暂停休息期间 %s 到点，照常提醒", pick.id)
	s.startBreakAt(pick.id, pick.at)
}

// pauseDisposition 这条提醒在暂停休息期间的实际处置。
// 未知取值按默认的推迟。
//
// **“推迟提醒”但没勾「允许推迟提醒」（PostponeMs == 0）时降级成“跳过”**：
// 没有时长可推，硬按推迟办会算出 now+0 的到期时刻 —— 每一拍都“又到点了”，
// 暂停期间会无限循环、还每轮记一笔。跳过是唯一有意义的替代（这个提醒这次不打扰，
// 与“推迟到恢复后再说”相反的那一侧）；要照常提醒就去卡里选“正常提醒”。
func pauseDisposition(c model.BreakConfig) string {
	switch c.PauseAction {
	case settings.PauseActionRemind:
		return settings.PauseActionRemind
	case settings.PauseActionSkip:
		return settings.PauseActionSkip
	}
	if !c.CanPostpone() {
		return settings.PauseActionSkip
	}
	return settings.PauseActionPostpone
}

// pausedPostpone 暂停期间的“推迟提醒”：到点不弹窗，把弹出时间往后推一个“推迟时长”；
// 推完再到期时若仍在暂停休息状态，就继续往后推，如此往复。推迟不是吞掉：推后的时刻
// 到了照样按处置走（仍暂停 → 再推；恢复后 → 弹窗）。
//
// 暂停不再冻结时钟（2026-11-12 用户口径：倒计时不要停），所以“推迟”必须真的改计划，
// 否则侧栏会停在 0:00：
//   - 倒计时线（内置与自定义同口径）：把挂起线的 At 挪到 now + 推迟时长，再重排一次，
//     NextAt 与状态卡跟着走。曾经内置倒计时线**不排**推迟计划 —— 那时时钟冻结、恢复时
//     整体平移就等于推迟；现在时钟不停，不挪就是“到点作废”。
//   - 固定时刻线：时刻是绝对的（12:00 就是 12:00），挪不了配置。记一笔 postponed（dueAt），
//     状态卡的倒计时格由 applyPostponedRefsLocked 显示 dueAt；到点仍暂停就再往后延。
func (s *Scheduler) pausedPostpone() {
	cfg := s.host.CfgSnapshot()
	now := nowMs()
	type note struct {
		name    string
		due     int64
		again   bool
		skipped bool
	}
	var notes []note
	type skippedBreak struct {
		id string
		ms int64
	}
	var skipped []skippedBreak
	moved := false
	nameOf := func(id string) string {
		for _, e := range cfg.BreakEntries() {
			if e.ID == id {
				return e.Name
			}
		}
		return id
	}
	s.mu.Lock()
	for _, e := range cfg.BreakEntries() {
		cc := e.Config
		if !cc.Enabled || pauseDisposition(cc) != settings.PauseActionPostpone {
			continue
		}
		push := func(at int64) {
			key := plan.OccurrenceKey(e.ID, at)
			if p, ok := s.postponed[key]; ok {
				// 推后的时刻又到了：还在暂停，就再往后推一段
				if now >= p.dueAt {
					p.dueAt = now + cc.PostponeMs
					notes = append(notes, note{name: e.Name, due: p.dueAt, again: true})
				}
				return
			}
			p := &postponedBreak{kind: e.ID, at: at, dueAt: now + cc.PostponeMs}
			s.postponed[key] = p
			notes = append(notes, note{name: e.Name, due: p.dueAt})
		}
		if cc.Mode == "fixed" {
			for _, slot := range cc.Slots {
				if !plan.IsTimeSlotDue(slot, now, 0) {
					continue
				}
				at := plan.SlotAlertAt(slot, 0, now)
				key := plan.OccurrenceKey(e.ID, at)
				// 推迟后的时刻碰上 / 越过列表里的下一次：这一班不再单独弹，跳过它、
				// 让下一班按自己的时刻来（与“推迟”按钮同一口径，见 Postpone）。
				// 消费掉这一班，时刻表才会顺延到下一班，否则它下个 tick 又“到期”。
				if next, ok := plan.NextSlotAt(cc.Slots, at); ok && at+cc.PostponeMs >= next.At {
					if !s.consumed[key] {
						s.consumed[key] = true
						s.advanceCounter(e.ID)
						skipped = append(skipped, skippedBreak{id: e.ID, ms: cc.DurationMs})
						notes = append(notes, note{name: e.Name, due: next.At, skipped: true})
					}
					delete(s.postponed, key)
					continue
				}
				push(at)
			}
			continue
		}
		// 倒计时线：挂起线里这一跳已过点即算到点，把它的 At 挪到推迟之后
		for i := range s.state.Lines {
			l := &s.state.Lines[i]
			if l.Type != e.ID || l.Source != "countdown" || l.At > now {
				continue
			}
			l.At = now + cc.PostponeMs
			moved = true
			notes = append(notes, note{name: e.Name, due: l.At})
		}
	}
	// 已经挂着的待办到点了、仍在暂停：按卡片继续往后推（“推完还暂停就继续推”）。
	// 没有这一段，待办只在恢复后才被 flushPostponed 弹出 —— 暂停期间到点的那次推迟
	// 等于“到点不办、等恢复”，与“到点按处置办”不一致。
	for key, p := range s.postponed {
		if p.dueAt > now {
			continue
		}
		cc, ok := cfg.BreakByID(p.kind)
		if !ok || !cc.Enabled {
			delete(s.postponed, key)
			continue
		}
		if pauseDisposition(cc) != settings.PauseActionPostpone {
			// 卡片在推迟期间改成了别的处置：留给 flushPostponed 按新设置办
			continue
		}
		if cc.PostponeMs <= 0 {
			delete(s.postponed, key)
			continue
		}
		due := p.dueAt + cc.PostponeMs
		if next, ok := plan.NextSlotAt(cc.Slots, p.at); ok && due >= next.At {
			// 再推就越过下一班：跳过本次（与“推迟”按钮同一口径）
			delete(s.postponed, key)
			s.consumed[plan.OccurrenceKey(p.kind, p.at)] = true
			s.advanceCounter(p.kind)
			skipped = append(skipped, skippedBreak{id: p.kind, ms: cc.DurationMs})
			notes = append(notes, note{name: nameOf(p.kind), due: next.At, skipped: true})
			continue
		}
		p.dueAt = due
		notes = append(notes, note{name: nameOf(p.kind), due: due, again: true})
	}
	s.mu.Unlock()
	if len(notes) == 0 && !moved {
		return
	}
	for _, n := range notes {
		at := time.UnixMilli(n.due).Format("15:04:05")
		if n.skipped {
			log.Printf("[scheduler] 暂停休息期间 %s 到点，推迟后不早于下一次固定时刻：跳过本次，按 %s 计", n.name, at)
			continue
		}
		if n.again {
			log.Printf("[scheduler] 暂停休息期间 %s 推迟后再次到点，继续推迟到 %s", n.name, at)
			continue
		}
		log.Printf("[scheduler] 暂停休息期间 %s 到点，推迟到 %s 再提醒", n.name, at)
	}
	// 降级成“跳过”的那几班照跳过的口径记账（只认短 / 长，App 侧过滤）
	for _, sk := range skipped {
		s.host.OnBreakSkipped(sk.id, sk.ms)
	}
	// 计划变了（线挪了 / 多了一条待办）：重排让 NextAt 与状态卡跟上，并推给设置页
	s.replan()
	s.emitState()
}

// flushPostponed 弹出“暂停期间被推迟、现在到点”的提醒，返回是否弹了一次。
// 每跳最多弹一条（与正常路径一致：一次只开一个提醒窗口），其余下一跳继续。
func (s *Scheduler) flushPostponed() bool {
	now := nowMs()
	s.mu.Lock()
	var pick *postponedBreak
	pickKey := ""
	for k, p := range s.postponed {
		if p.dueAt > now {
			continue
		}
		if pick == nil || p.dueAt < pick.dueAt {
			pick, pickKey = p, k
		}
	}
	if pick == nil {
		s.mu.Unlock()
		return false
	}
	delete(s.postponed, pickKey)
	s.mu.Unlock()

	// 这条提醒可能已经被删 / 关闭：没有窗口可弹，丢掉这一次
	if c, ok := s.host.CfgSnapshot().BreakByID(pick.kind); !ok || !c.Enabled {
		log.Printf("[scheduler] 推迟的 %s 已被关闭或删除，丢弃", pick.kind)
		return false
	} else if pauseDisposition(c) == settings.PauseActionSkip {
		// 推迟期间卡片被改成“跳过提醒”：按新设置办，不补弹（用户已经选了不提醒）
		s.mu.Lock()
		s.consumed[plan.OccurrenceKey(pick.kind, pick.at)] = true
		s.mu.Unlock()
		log.Printf("[scheduler] 推迟的 %s 到点，但卡片已改成“跳过提醒”：丢弃", pick.kind)
		s.host.OnBreakSkipped(pick.kind, c.DurationMs)
		return false
	}
	log.Printf("[scheduler] 推迟的 %s 到点，弹出提醒（原定 %s）", pick.kind,
		time.UnixMilli(pick.at).Format("15:04:05"))
	s.startBreakAt(pick.kind, pick.at)
	return true
}
