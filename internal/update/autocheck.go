package update

import "time"

// 自动检查更新的调度状态机（纯逻辑，零 App 依赖）。

const (
	// AutoCheckWindow 当天第一次检测到键鼠活动后，在这么长的时间窗内随机取一刻检查
	AutoCheckWindow = 3 * time.Hour
	// AutoCheckPoll 调度轮询粒度（判断是否跨天 / 是否已有键鼠活动 / 是否到点）
	AutoCheckPoll = 2 * time.Second
)

// 调度动作
const (
	ActNone  = ""      // 继续等
	ActCheck = "check" // 现在去检查更新
)

// State 自动检查更新的调度状态（只保存“当天”这一份，跨天整体重置）。
type State struct {
	Day     string    // 本次调度所属的本地日期
	ArmedAt time.Time // 该日期内的目标时刻；零值＝还没排期（在等当天第一次键鼠活动）
	// Skipped 今天已经因为“暂停休息”放弃检查，当天不再排期。
	// 与 ArmedAt 为零分开记：零值是“还没排期”，这个是“今天不排了”。
	Skipped bool
}

// SkipDay 记下“今天不查了”：调度循环在真正联网前才发现自己处于暂停状态时用它兜底
// （Tick 到点与真正发起请求之间隔着最多一个轮询间隔）。幂等。
func (s *State) SkipDay() {
	s.ArmedAt = time.Time{}
	s.Skipped = true
}

// Tick 推进一次调度状态机，返回这一拍该做什么。规则：
//  1. 当天首次检测到键鼠活动 → 在 0~3 小时内随机取一个时刻（ArmedAt）；
//  2. 到点时不在“暂停休息”状态 → 去检查更新；
//  3. 到点时正处于“暂停休息”→ **当天就不查了**（Skipped），不再改时刻重试；
//  4. 跨天（含排期期间跨过零点）一律整体重置，当天有没有查成都不影响第二天；
//  5. 当天已经查过（done）→ 不再排期。
//
// 第 3 条是 2026-11-05 用户拍板的口径：暂停意味着“别来烦我”，检查更新也算打扰 ——
// 那就干脆等明天那份排期，错过一天不影响什么。原先是 30 分钟一次的重试链，会在
// 整个暂停期间每半小时探一次，与这个承诺相矛盾。
//
// 纯逻辑，不碰时钟、不碰 IO，时间由参数给、随机由 randDur 给，方便单测。
func Tick(st *State, today string, now time.Time,
	active, paused, done bool, randDur func(time.Duration) time.Duration) string {
	if st.Day != today {
		*st = State{Day: today}
	}
	if done {
		st.ArmedAt = time.Time{}
		return ActNone
	}
	if st.Skipped {
		return ActNone
	}
	if st.ArmedAt.IsZero() {
		// 还在等这一天的第一次键鼠活动；没活动就不排期
		if !active {
			return ActNone
		}
		st.ArmedAt = now.Add(randDur(AutoCheckWindow))
		return ActNone
	}
	// 系统时钟被往回调（手动改时间 / 校时）会把已排的 ArmedAt 推到很远的未来，
	// 那一整天的自动检查就永远等不到。超过本日窗口 + 5 分钟就当作失效、重新排期。
	if st.ArmedAt.After(now.Add(AutoCheckWindow + 5*time.Minute)) {
		st.ArmedAt = now.Add(randDur(AutoCheckWindow))
	}
	if now.Before(st.ArmedAt) {
		return ActNone
	}
	if paused {
		st.SkipDay()
		return ActNone
	}
	st.ArmedAt = time.Time{}
	return ActCheck
}
