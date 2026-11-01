package update

import "time"

// 自动检查更新的调度状态机（纯逻辑，零 App 依赖）。

const (
	// AutoCheckWindow 当天第一次检测到键鼠活动后，在这么长的时间窗内随机取一刻检查
	AutoCheckWindow = 3 * time.Hour
	// AutoCheckRetry 到点却处于「暂停休息」时，在这么长的时间窗内再随机取一刻重试，
	// 仍暂停就再取一刻，如此反复
	AutoCheckRetry = 30 * time.Minute
	// AutoCheckPoll 调度轮询粒度（判断是否跨天 / 是否已有键鼠活动 / 是否到点）
	AutoCheckPoll = 2 * time.Second
)

// 调度动作
const (
	ActNone  = ""      // 继续等
	ActCheck = "check" // 现在去检查更新
)

// State 自动检查更新的调度状态（只保存「当天」这一份，跨天整体重置）。
type State struct {
	Day     string    // 本次调度所属的本地日期
	ArmedAt time.Time // 该日期内的目标时刻；零值＝还没排期（在等当天第一次键鼠活动）
}

// Tick 推进一次调度状态机，返回这一拍该做什么。规则：
//  1. 当天首次检测到键鼠活动 → 在 0~3 小时内随机取一个时刻（ArmedAt）；
//  2. 到点时若不在「暂停休息」状态 → 去检查更新；
//  3. 到点时正处于「暂停休息」→ 在 0~30 分钟内再随机取一个时刻，到点再判，反复；
//  4. 跨天（含排期或重试期间跨过零点）一律整体重置，当天有没有查成都不影响第二天；
//  5. 当天已经查过（done）→ 不再排期。
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
		st.ArmedAt = now.Add(randDur(AutoCheckRetry))
		return ActNone
	}
	st.ArmedAt = time.Time{}
	return ActCheck
}
