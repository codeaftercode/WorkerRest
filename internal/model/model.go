// Package model 是全仓共享的数据结构层：会话/状态、计划项、时间槽、配置树里的
// 结构体都是这里的类型。字段名与 JSON 形状必须与渲染层（web/dist 的 JS）一致，
// 改一个键名等于改前端契约。
//
// 本包零依赖（不 import 任何 internal 包），是依赖图的最底层；任何需要“一个类型”
// 的地方都可以引它，它不引任何人。
package model

// 会话与状态的数据结构：字段名/JSON 形状必须与现有渲染层（renderer/break.ts、
// takeout.ts、supervisorStage.ts）期望的结构**逐字对齐**——渲染层代码原样复用，
// 只把 window.api 的实现从 上一版 IPC 换成 Wails 绑定（见 web/dist/shim.js）。

// TimeSlot 一条固定时刻：时刻 + 重复周期（1=周一 … 7=周日）
type TimeSlot struct {
	ID      string `json:"id"`
	Hour    int    `json:"hour"`
	Minute  int    `json:"minute"`
	Repeat  string `json:"repeat"`
	Days    []int  `json:"days"`
	Enabled bool   `json:"enabled"`
}

// Appearance 提醒窗口外观（颜色 / 透明度 / 模糊 / 全屏）
type Appearance struct {
	Color        string  `json:"color"`
	Transparency float64 `json:"transparency"`
	Blur         bool    `json:"blur"`
	Fullscreen   bool    `json:"fullscreen"`
}

// SupGeom 立绘尺寸与位置（占窗口百分比）
type SupGeom struct {
	SizePct float64 `json:"sizePct"`
	PosXPct float64 `json:"posXPct"`
	PosYPct float64 `json:"posYPct"`
	Flip    bool    `json:"flip"`
}

// SupLines 督休专员两段台词
type SupLines struct {
	Urge    string `json:"urge"`
	Command string `json:"command"`
}

// SupStageInfo 督休专员在提醒窗口里的出场信息。
// 字段与 历史源码/supervisor.ts 的 SupervisorSessionInfo 逐字对齐 ——
// 设置页的预览面板直接读它（supPreview.geom / supPreview.lines.urge / .command），
// 少一个字段就是“Cannot read properties of undefined”把预览打断。
type SupStageInfo struct {
	Image    string   `json:"image"` // data URL
	RoleName string   `json:"roleName,omitempty"`
	Name     string   `json:"name,omitempty"`
	Gaze     bool     `json:"gaze"`
	Geom     *SupGeom `json:"geom,omitempty"`
	Lines    SupLines `json:"lines"`
}

// BreakInfo 一次休息会话（渲染层 Info 的 Go 对应物）
type BreakInfo struct {
	ID         string        `json:"id"`
	Type       string        `json:"type"` // short | long | 自定义 id
	Name       string        `json:"name"`
	EndsAt     int64         `json:"endsAt"`     // Unix 毫秒
	DurationMs int64         `json:"durationMs"` // 本次休息总时长
	Preview    bool          `json:"preview"`
	Appearance Appearance    `json:"appearance"`
	Messages   []string      `json:"messages"`
	Supervisor *SupStageInfo `json:"supervisor"`
	// EndEarlyShortcut 提前结束休息的全局快捷键（与“设置 → 快捷键”的 endEarly 一致）。
	// 窗口据此显示“提前结束（Ctrl+Alt+X）”提示；空串 = 未配置（只显示“提前结束”）。
	EndEarlyShortcut string `json:"endEarlyShortcut"`
}

// TakeoutInfo 外卖提醒会话（与 历史源码/types.ts 的 TakeoutSessionInfo 逐字对齐）。
// 渲染层 takeout.ts 读 id / at / hour / minute / firedAt / preview / meal / leadSec /
// postponeMin / postponedCount / message / appearance / supervisor / redPacketData；
// slot 额外保留给主进程“推迟”记账（takeout:action 按 cur.slot 重弹），渲染层不读也无妨。
type TakeoutInfo struct {
	ID     string `json:"id"`
	At     int64  `json:"at"` // 预定时刻（当天的 hour:minute，Unix 毫秒）
	Hour   int    `json:"hour"`
	Minute int    `json:"minute"`
	// Slot 触发本次提醒的时间条目（点“推迟”后按同一条目重新弹出）
	Slot TimeSlot `json:"slot"`
	// FiredAt 本次弹出时间（Unix 毫秒）
	FiredAt int64  `json:"firedAt"`
	Preview bool   `json:"preview"`
	Meal    string `json:"meal"` // 餐段文案：早餐 / 午餐 / 下午茶 / 晚餐 / 夜宵
	// LeadSec 提前通知秒数（生成会话时的配置快照）：只用于诊断，真实窗口始终按原定时刻弹出
	LeadSec int `json:"leadSec"`
	// PostponeMin 推迟时长的分钟数（最小 1；“推迟”按钮文案与主进程推迟时长共用）
	PostponeMin int `json:"postponeMin"`
	// PostponedCount 本次是第几次推迟：0=到点正常弹出，>0 显示“已推迟 N 次”
	PostponedCount int `json:"postponedCount"`
	// Message 本次展示的提醒内容（外卖提醒自己的那一套配置；空串窗口不显示）
	Message    string        `json:"message"`
	Appearance Appearance    `json:"appearance"`
	Supervisor *SupStageInfo `json:"supervisor"`
	// RedPacketData 红包二维码指向的地址；外卖提醒窗口始终展示优惠券，渲染层据此显示二维码
	RedPacketData *TakeoutRedPacket `json:"redPacketData"`
}

// TakeoutRedPacket 红包二维码地址（外卖提醒窗口始终展示，指向固定领取页，不含本机标识）
type TakeoutRedPacket struct {
	URL string `json:"url"`
}
