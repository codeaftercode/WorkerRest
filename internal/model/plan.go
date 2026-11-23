package model

// 计划层 + 配置层的纯数据类型。全部零 App 依赖、零函数依赖，
// 只依赖本包内的 TimeSlot 等最底层类型。

// BreakConfig 一次休息的配置（mode = countdown | fixed 决定用间隔还是时刻表）
type BreakConfig struct {
	Enabled    bool   `json:"enabled"`
	Mode       string `json:"mode"`
	DurationMs int64  `json:"durationMs"`
	IntervalMs int64  `json:"intervalMs"`
	// NotifyBeforeMs 提前通知的提前量（毫秒）。**0 = 不提前通知**（设置页「提醒前弹出
	// 通知」没勾），到点直接弹正式提醒窗口。2026-11-13 起设置页用勾选框表达这件事，
	// 但存的仍是这个数 —— 0 就是“关”，没有第二个开关键。
	NotifyBeforeMs int64 `json:"notifyBeforeMs"`
	// PostponeMs 每次推迟的时长（毫秒）。**0 = 不允许推迟**（设置页「允许推迟提醒」
	// 没勾）：界面不显示推迟按钮，推迟快捷键也不响应。同样只有这一个键表达开关态。
	PostponeMs int64 `json:"postponeMs"`
	// PauseAction 暂停休息期间到点时怎么处置：remind / postpone / skip（缺省 postpone，
	// 见 PauseAction* 常量）。2026-11-12 起短休息 / 长休息也有这一项（用户要求与外卖提醒、
	// 自定义提醒一致）：remind 照常弹、postpone 顺延到恢复后、skip 跳过并记一笔统计；
	// 自动暂停（空闲 / 勿扰 / 全屏 / 投屏 / 应用占用）不看它，一律不打扰。
	PauseAction string `json:"pauseAction"`
	// IdleGate 到点后不马上提醒，先等“鼠标键盘连续无活动”达到 IdleGateMs（空闲门）。
	// 语义：用户只想在自己已经离开工位的那一刻被叫去休息，而不是干到一半被打断。
	IdleGate bool `json:"idleGate"`
	// IdleGateMs 空闲门时长（毫秒，默认 30 秒）。勾了但填 0 分 0 秒 = 没启用，见 IdleGateWaitMs。
	IdleGateMs int64 `json:"idleGateMs"`
	// AllowEarlyEnd 提醒窗口给不给「提前结束」：true = 窗口顶部显示「提前结束（快捷键）」，
	// 全局快捷键 / 侧栏 / 托盘的提前结束也照常响应；false = 都不给（见 schedEndBreak）。
	// 语义上默认**允许** —— 老配置没有这个键，缺省若按 false 走，升级后所有提醒会突然
	// 变得「关不掉」，只能干坐到时间走完。归一化口径见 breakConfigOf 的 mapFlag(..., true)。
	AllowEarlyEnd bool       `json:"allowEarlyEnd"`
	Slots         []TimeSlot `json:"slots"`
}

// IdleGateWaitMs 生效的空闲门时长：没勾、或时长 <= 0 一律返回 0（= 不设门）。
// 把“勾了但填成 0 分 0 秒”在这里就折成 0，调用方不需要各写一次判据
// （2026-11-08 用户口径：“如果分钟数和秒数都是 0 秒则相当于没有启用”）。
func (c BreakConfig) IdleGateWaitMs() int64 {
	if !c.IdleGate || c.IdleGateMs <= 0 {
		return 0
	}
	return c.IdleGateMs
}

// CanPostpone 这条提醒允不允许推迟（PostponeMs > 0）。
//
// 唯一判据：设置页「允许推迟提醒」勾选框把 PostponeMs 写成 0 或正数。所有入口
// （侧栏按钮、快捷键、提前提示窗、托盘）都问它一次，别各自比 ms <= 0 —— 一处漏判
// 就会出现“按钮藏了但快捷键还生效”这种半开半关的状态。
func (c BreakConfig) CanPostpone() bool { return c.PostponeMs > 0 }

// AppearanceConfig 外观配置（磁盘结构；比下发给渲染层的 Appearance 多几个字段）
type AppearanceConfig struct {
	Color        string  `json:"color"`
	Transparency float64 `json:"transparency"`
	Blur         bool    `json:"blur"`
	Fullscreen   bool    `json:"fullscreen"`
	AlwaysOnTop  bool    `json:"alwaysOnTop"`
	WidthPct     float64 `json:"widthPct"`
	HeightPct    float64 `json:"heightPct"`
	// HideOnShare 共享屏幕时隐藏提醒窗口：给提醒窗口打上“排除出屏幕捕获”
	// （Windows 的 WDA_EXCLUDEFROMCAPTURE），会议软件整屏 / 窗口共享时看不到它。
	// 默认开（出厂 json 同为 true）——这是“别被同事看见”的兜底，默认关等于没装。
	// 只影响提醒窗口（休息 / 提前提示 / 外卖），不影响设置窗口与督休画廊。
	HideOnShare bool `json:"hideOnShare"`
}

// SupervisorConfig 督休专员显示开关（督休专员本体在 supervisors/<名称>/meta.json，
// 名字就是身份 —— 没有内部 id）
type SupervisorConfig struct {
	Enabled   bool   `json:"enabled"`
	Gaze      bool   `json:"gaze"`
	Selection string `json:"selection"` // fixed | sequence | random
	// FixedRole 固定显示时指定的督休专员名。查不到（改名 / 删除）就兜底到第一个可用的。
	FixedRole string `json:"fixedRole"`
	// Order 候选专员的展示顺序（专员名数组）。用户在设置页拖动立绘磁贴调整顺序后落盘，
	// 这里读回来即为“顺序轮换”的出场次序 —— 画廊显示顺序与轮换次序共用这一份顺序，
	// 用户拖一次两边同时生效，不会出现“画廊一个样、轮换另一个样”的劈叉。
	Order []string `json:"order"`
}

// ReminderMessageItem 一条提醒内容
type ReminderMessageItem struct {
	ID      string `json:"id"`
	Text    string `json:"text"`
	Enabled bool   `json:"enabled"`
}

// ReminderMessages 某类提醒的提醒内容池
type ReminderMessages struct {
	Order   string                `json:"order"`
	FixedID string                `json:"fixedId"`
	Items   []ReminderMessageItem `json:"items"`
}

// BreakEntry 一条休息清单项（内置短/长 + 自定义各条）
type BreakEntry struct {
	ID     string      `json:"id"`
	Name   string      `json:"name"`
	Config BreakConfig `json:"config"`
}

// PlannedBreak 计划出来的一次休息（托盘/状态卡直接用 Name）
type PlannedBreak struct {
	Type   string    `json:"type"`
	Name   string    `json:"name"`
	At     int64     `json:"at"`
	Source string    `json:"source"` // countdown | fixed
	Slot   *TimeSlot `json:"slot,omitempty"`
}

// PlanRef 状态卡上的“下一次 XX”分组（只有名字与时刻）
type PlanRef struct {
	Name string `json:"name"`
	At   int64  `json:"at"`
	// IdleWait 这一跳已经到点、正在等“鼠标键盘无活动”达标（空闲门）：状态卡的时间格
	// 改成写“等待空闲”，不再画一个恒为 0 的数码管。
	IdleWait bool `json:"idleWait,omitempty"`
}

// PlanSplit 按设置页状态卡的口径把下一跳分成两组
type PlanSplit struct {
	Builtin *PlannedBreak `json:"builtin"`
	Other   *PlannedBreak `json:"other"`
}

// TimelineItem 今日提醒时间线的一项
//
// 曾经带一个 *bool 的 paused 标记（上一版只给倒计时线写 paused 键），
// 2026-11-12 起暂停不再冻钟、倒计时照真实墙钟走，时间线里已经没有“暂停中”这个概念，
// 字段随之删除。
type TimelineItem struct {
	Key    string `json:"key"`
	Type   string `json:"type"`
	Name   string `json:"name"`
	At     int64  `json:"at"`
	Source string `json:"source"`
}

// TakeoutIdleWait 到点了、正在等“鼠标键盘无活动”达标的那一笔外卖提醒
// （开着空闲门，人还在忙）。与 TakeoutPostponed 分开：那是用户点的推迟。
type TakeoutIdleWait struct {
	Slot TimeSlot `json:"slot"`
	// At 原定时刻（occurrence 身份）：面板与日志要能说清“等的是哪一次”
	At int64 `json:"at"`
}

// TakeoutPostponed 外卖的待推迟提醒（窗口上点“推迟”时由外卖模块记账）
type TakeoutPostponed struct {
	Slot  TimeSlot `json:"slot"`
	DueAt int64    `json:"dueAt"`
	Count int      `json:"count"`
	// PreFired 推迟后的提前通知是否已弹过：leadSec>0 时，推迟线在 DueAt 前 leadMs
	// 要再弹一次预通知（与原始提前通知线同口径），用这个标记去重，避免每秒 tick 重复弹。
	PreFired bool `json:"preFired"`
	// Armed 这条“推迟线”其实是空闲门达标后自动落位的（不是用户点的那次推迟）。
	// 区别只有一处：到点时不再复判空闲 —— 它是刚刚达标才排上的；
	// 用户点的推迟到点后要重新看一次空闲（见 checkTakeoutReminders）。
	Armed bool `json:"armed,omitempty"`
}

// SchedulerState 调度器状态快照。
// 字段集**必须完整**：设置页的 renderStatus() 一上来就读 paused / pauseReasons.length /
// phase，缺一个就在 init 里抛错、整页停摆（见 历史源码/types.ts 的 SchedulerState）。
type SchedulerState struct {
	Phase string `json:"phase"` // idle | pre | break
	// NextType 下一次休息的身份（'short' | 'long' | 自定义 id）
	NextType string `json:"nextType"`
	// NextName 下一次休息的展示名（托盘提示与状态卡直接用，免得再回查配置）
	NextName string `json:"nextName"`
	NextAt   int64  `json:"nextAt"`
	// NextSource 没有安排（全部关闭）时为 null
	NextSource *string `json:"nextSource"`
	// PostponeAllowed 下一条短 / 长休息（RestKind）允不允许推迟 —— 那条提醒卡勾了
	// 「允许推迟提醒」。侧栏「推迟休息」按钮据此显隐；快捷键 / 托盘走同一个调度器入口
	// （Scheduler.PostponeRest），由它再挡一道（前端藏起来只是别让人点到一个必然无反应的
	// 按钮，判据只有一份）。
	//
	// 注意口径：它说的是 **短 / 长休息那一条**，不是全局 NextType。全局 next 可能是外卖或
	// 自定义提醒（它们各自有自己的推迟入口），拿它们的设置来决定「推迟休息」显不显示，
	// 就会出现“下一条休息明明能推、按钮却没了”这种错位（2026-11-14 修正）。
	PostponeAllowed bool `json:"postponeAllowed"`
	// RestKind 下一条短 / 长休息的身份（short | long；两者都没排上时为空串）。
	// 侧栏那组按钮（立即休息 / 推迟休息 / 跳过本次 / 重置周期）的目标就是它。
	RestKind string `json:"restKind"`
	// BreakType 正在进行的这次休息的身份（short | long | 自定义 id | 外卖）；
	// phase 不是 break 时为空串。
	//
	// **不能拿 NextType 代替**：休息期间只要发生一次 replan（保存设置、时间跳变），
	// NextType 就被改写成“下一条”，用它判“当前这次是不是短 / 长休息”会错位。
	// 侧栏的“跳过本次 / 提前结束”文案与托盘第一项的可点性都认这个字段
	// （2026-11-15 用户口径：只有长 / 短休息才叫“提前结束”，自定义提醒、外卖提醒在弹时
	// 那两处既不许写“提前结束”，也不许留着可点）。
	BreakType string `json:"breakType"`
	// AllowEarlyEnd 本次休息（BreakType）允不允许提前结束 —— 那张卡勾没勾
	// 「弹窗设置 → 允许提前结束」。只在 phase == break 时有意义，其余时候恒为 true
	// （缺省允许，免得前端把“不在休息”误判成“不让结束”）。侧栏「提前结束」据此置灰，
	// 与宿主 schedEndBreak 的拦截同一处口径。
	AllowEarlyEnd bool `json:"allowEarlyEnd"`
	// Rest 短/长休息线里最近的一条（状态卡“下次休息提醒”格）；null = 两者都没安排
	Rest *PlanRef `json:"rest"`
	// Other 外卖提醒与所有自定义提醒里最近的一条（状态卡“下次XX提醒”格）；null = 都没启用
	Other *PlanRef `json:"other"`
	// Lines 调度器当前挂起的全部触发线（replan 时算好；推迟时跟着挪到 dueAt）
	Lines []PlannedBreak `json:"lines"`
	// Timeline 今日提醒时间线（主进程在状态快照时按当前设置与调度状态现算）
	Timeline []TimelineItem `json:"timeline"`
	// FixedNext 各“固定时刻线”身份下一次实际触发的时刻（键 = BreakId 或 'takeout'）
	FixedNext      map[string]int64 `json:"fixedNext"`
	BreakEndsAt    int64            `json:"breakEndsAt"`
	BreakStartedAt int64            `json:"breakStartedAt"`
	// BreakWhilePaused 这次休息是在“暂停态”下主动发起的（托盘 / 侧栏“立即休息”）。
	// 这种休息不参与暂停的时间冻结：用户既然点了开始，就要按真实时钟走完并自动收窗。
	BreakWhilePaused bool     `json:"breakWhilePaused"`
	Paused           bool     `json:"paused"`
	PauseReasons     []string `json:"pauseReasons"`
	// PausedAt 进入暂停的时刻。唯一读者是“进行中、且自己选的是推迟”的那次休息：
	// 它的剩余时间按此锚定（推迟 = 冻结剩余，恢复后接着走）。
	// 下一跳的倒计时**不**按此锚定 —— 暂停不冻钟，照真实墙钟往下掉（2026-11-12 口径）。
	PausedAt int64 `json:"pausedAt"`
	// PauseDeadline 定时暂停的到期时刻（0 = 无限，直到手动恢复）。
	// 到点不一定立刻恢复：还有“投屏 / 全屏 / 勿扰 / 空闲 / 应用占用”在生效时保持暂停，
	// 等它们消失（判定见 cmd/workerrest/pause_expiry.go）。
	PauseDeadline   int64 `json:"pauseDeadline"`
	ShortsSinceLong int   `json:"shortsSinceLong"`
	// IdleWait 下一次提醒已到点、正在等“鼠标键盘无活动”达标（空闲门）。状态卡据此把
	// 时间格写成“等待空闲”；具体是哪一条由 NextType 决定（Rest / Other 上另有一份标记，
	// 前端据此逐格判断，不必自己比对名字）。
	IdleWait bool `json:"idleWait"`
}

// IsRestKind 这个身份是不是内置的短 / 长休息（short | long）。
//
// 只有它们才配“提前结束”：自定义提醒与外卖提醒各有自己的窗口和入口，
// 侧栏与托盘那两处对它们一律不认（见 SchedulerState.BreakType 的注释）。
// 调度器包内的 isBuiltinBreakID 与它同一口径。
func IsRestKind(id string) bool { return id == "short" || id == "long" }

// AppPauseItem 指定应用运行时暂停的一条规则
type AppPauseItem struct {
	ID      string
	Exe     string // 可执行文件名（只存 basename，匹配时忽略路径与大小写）
	Name    string // 软件名：只用于显示，可留空（界面回退到可执行文件名）
	Enabled bool
}

// AppPauseConfig 指定应用运行时自动暂停（总开关默认关，与升级前行为一致）
type AppPauseConfig struct {
	Enabled bool
	Items   []AppPauseItem
}

// 全屏时不暂停的两种豁免方式（2026-11-20）
//
// 只对“非独占全屏（QUNS=2）”这一档有意义：独占全屏与演示设置永远直接暂停、从不询问，
// 也就没有“免问”这回事。
const (
	// FullScreenSkipNever 永不暂停该应用的全屏（长期，直到用户在设置页删掉）
	FullScreenSkipNever = "never"
	// FullScreenSkipToday 今天（本地日界 24:00）不再询问；跨天后自动失效
	FullScreenSkipToday = "today"
)

// FullScreenSkipItem “非独占全屏时不暂停”名单的一条。
//
//   - Exe 小写 basename，匹配用（与 winproc.ListProcessNames 的口径一致）；
//   - Label 只用于展示：商店应用更新后 exe 可能改名，用户认的是“照片”而不是文件名；
//   - Mode 见上面两个常量；
//   - Until 仅 Mode=today 有意义（本地当日 24:00 的毫秒时间戳），0 视为已过期。
type FullScreenSkipItem struct {
	Exe   string
	Label string
	Mode  string
	Until int64
}

// CustomBreak 一条自定义提醒（设置页“时间规划”页底部新建的第 3 种及以后提醒）。
// 选项、默认值与含义与“短休息”完全一致（同一套 BreakConfig），提醒内容也自成一套。
type CustomBreak struct {
	ID     string
	Name   string
	Config BreakConfig
}
