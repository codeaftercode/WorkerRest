package model

// 计划层 + 配置层的纯数据类型。全部零 App 依赖、零函数依赖，
// 只依赖本包内的 TimeSlot 等最底层类型。

// BreakConfig 一次休息的配置（mode = countdown | fixed 决定用间隔还是时刻表）
type BreakConfig struct {
	Enabled        bool   `json:"enabled"`
	Mode           string `json:"mode"`
	DurationMs     int64  `json:"durationMs"`
	IntervalMs     int64  `json:"intervalMs"`
	NotifyBeforeMs int64  `json:"notifyBeforeMs"`
	PostponeMs     int64  `json:"postponeMs"`
	// PauseAction 暂停休息期间到点时怎么处置：remind / postpone / skip（缺省 skip，
	// 见 PauseAction* 常量）。只对自定义提醒有意义；短 / 长休息不看这个字段。
	PauseAction string     `json:"pauseAction"`
	Slots       []TimeSlot `json:"slots"`
}

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
}

// PlanSplit 按设置页状态卡的口径把下一跳分成两组
type PlanSplit struct {
	Builtin *PlannedBreak `json:"builtin"`
	Other   *PlannedBreak `json:"other"`
}

// TimelineItem 今日提醒时间线的一项
//
// Paused 是**指针**：上一版 只给倒计时线的项写 paused 键（固定线没有这个键），
// 用 *bool + omitempty 才能连“键在不在”都对上。
type TimelineItem struct {
	Key    string `json:"key"`
	Type   string `json:"type"`
	Name   string `json:"name"`
	At     int64  `json:"at"`
	Source string `json:"source"`
	Paused *bool  `json:"paused,omitempty"`
}

// TakeoutPostponed 外卖的待推迟提醒（窗口上点“推迟”时由外卖模块记账）
type TakeoutPostponed struct {
	Slot  TimeSlot `json:"slot"`
	DueAt int64    `json:"dueAt"`
	Count int      `json:"count"`
	// PreFired 推迟后的提前通知是否已弹过：leadSec>0 时，推迟线在 DueAt 前 leadMs
	// 要再弹一次预通知（与原始提前通知线同口径），用这个标记去重，避免每秒 tick 重复弹。
	PreFired bool `json:"preFired"`
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
	// Rest 短/长休息线里最近的一条（状态卡“下次休息提醒”格）；null = 两者都没安排
	Rest *PlanRef `json:"rest"`
	// Other 外卖提醒与所有自定义提醒里最近的一条（状态卡“下次XX提醒”格）；null = 都没启用
	Other *PlanRef `json:"other"`
	// Lines 调度器当前挂起的全部触发线（replan 时算好；推迟/恢复时跟着平移）
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
	// PausedAt 进入暂停的时刻（冻结倒计时用：暂停期间剩余时间按此锚定，不再往下掉）
	PausedAt int64 `json:"pausedAt"`
	// PauseDeadline 定时暂停的到期时刻（0 = 无限，直到手动恢复）。
	// 到点不一定立刻恢复：还有“投屏 / 全屏 / 勿扰 / 空闲 / 应用占用”在生效时保持暂停，
	// 等它们消失（判定见 cmd/workerrest/pause_expiry.go）。
	PauseDeadline   int64 `json:"pauseDeadline"`
	ShortsSinceLong int   `json:"shortsSinceLong"`
}

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

// CustomBreak 一条自定义提醒（设置页“时间规划”页底部新建的第 3 种及以后提醒）。
// 选项、默认值与含义与“短休息”完全一致（同一套 BreakConfig），提醒内容也自成一套。
type CustomBreak struct {
	ID     string
	Name   string
	Config BreakConfig
}
