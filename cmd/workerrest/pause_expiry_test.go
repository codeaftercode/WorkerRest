package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"workerrest/internal/model"
	"workerrest/internal/scheduler"
)

// 定时手动暂停的判据与文案。全部写成具名纯函数，时刻靠参数喂 —— 跨夜、到期被挡、
// 改时长这些时序在真实时间下没法测，恰好也是最容易写错的部分。

// “到明天”= 下一个本地零点。零点是日期边界，“下一个零点”永远唯一，
// 不需要“不足一小时算次日”那种给“早上 8 点”打的补丁。
func TestNextMidnight(t *testing.T) {
	loc := time.Local
	cases := []struct {
		name string
		now  time.Time
		want time.Time
	}{
		{"晚上九点半", time.Date(2026, 11, 5, 21, 30, 0, 0, loc), time.Date(2026, 11, 6, 0, 0, 0, 0, loc)},
		{"差一秒跨天", time.Date(2026, 11, 5, 23, 59, 59, 0, loc), time.Date(2026, 11, 6, 0, 0, 0, 0, loc)},
		{"零点整", time.Date(2026, 11, 6, 0, 0, 0, 0, loc), time.Date(2026, 11, 7, 0, 0, 0, 0, loc)},
		{"凌晨", time.Date(2026, 11, 6, 0, 30, 0, 0, loc), time.Date(2026, 11, 7, 0, 0, 0, 0, loc)},
		{"年末", time.Date(2026, 12, 31, 22, 0, 0, 0, loc), time.Date(2027, 1, 1, 0, 0, 0, 0, loc)},
		{"月末", time.Date(2026, 2, 28, 10, 0, 0, 0, loc), time.Date(2026, 3, 1, 0, 0, 0, 0, loc)},
	}
	for _, c := range cases {
		if got := nextMidnight(c.now); !got.Equal(c.want) {
			t.Fatalf("%s：want %s got %s", c.name, c.want, got)
		}
	}
}

func TestPauseExpired(t *testing.T) {
	const now = int64(1_700_000_000_000)
	cases := []struct {
		deadline int64
		want     bool
		why      string
	}{
		{0, false, "无限暂停永不到期"},
		{now - 1, true, "已过期"},
		{now, true, "整点到期"},
		{now + 1, false, "还差 1 毫秒"},
	}
	for _, c := range cases {
		if got := pauseExpired(c.deadline, now); got != c.want {
			t.Fatalf("%s：want %v got %v", c.why, c.want, got)
		}
	}
}

// 到期时“此刻恢复会不会打扰”：六条自动原因全算，顺序固定（文案稳定）。
func TestPauseHoldReasons(t *testing.T) {
	if got := pauseHoldReasons(nil); len(got) != 0 {
		t.Fatalf("没有任何条件生效时应可恢复，得到 %v", got)
	}
	if got := pauseHoldReasons(map[string]bool{"idle": false, "勿扰": false}); len(got) != 0 {
		t.Fatalf("登记为 false 不等于生效，得到 %v", got)
	}
	got := pauseHoldReasons(map[string]bool{
		"全屏或演示": true,
		"勿扰":    true,
	})
	txt := pauseHoldText(got)
	if !strings.Contains(txt, "勿扰") || !strings.Contains(txt, "全屏") {
		t.Fatalf("应点名挡住的两条原因，得到 %q", txt)
	}
	// 顺序跟着 autoPauseReasons 走，不跟 map 走
	if got[0] != "勿扰" {
		t.Fatalf("顺序应固定（勿扰在全屏之前），得到 %v", got)
	}

	// 每一条都能单独挡住
	for _, r := range autoPauseReasons {
		if len(pauseHoldReasons(map[string]bool{r: true})) != 1 {
			t.Fatalf("%s 生效时应挡住到期恢复", r)
		}
	}
	// manual 不是自动原因，不该出现在白名单里
	for _, r := range autoPauseReasons {
		if r == "manual" {
			t.Fatal("manual 不该在阻挡清单里")
		}
	}
}

func TestTimedPauseText(t *testing.T) {
	now := time.Date(2026, 11, 5, 21, 0, 0, 0, time.Local).UnixMilli()

	// 未到期：说还剩多久、到几点
	tip, status := timedPauseText(now+30*60_000, nil, now)
	if !strings.Contains(tip, "还有 30 分钟") || !strings.Contains(tip, "21:30") {
		t.Fatalf("未到期应写剩余与时刻，得到 %q", tip)
	}
	if !strings.Contains(status, "暂停中") {
		t.Fatalf("状态行应写暂停中，得到 %q", status)
	}

	// 到期且被挡住：点名在等什么
	tip, status = timedPauseText(now-1, []string{"全屏或演示"}, now)
	if !strings.Contains(tip, "时长已到") || !strings.Contains(tip, "全屏或演示") {
		t.Fatalf("到期被挡应点名原因，得到 %q", tip)
	}
	if !strings.Contains(status, "时长已到") {
		t.Fatalf("状态行应标出时长已到，得到 %q", status)
	}

	// 到期但没挡住：一拍就会恢复，兜底文案也不能崩
	tip, _ = timedPauseText(now-1, nil, now)
	if !strings.Contains(tip, "时长已到") {
		t.Fatalf("兜底文案应说明已到，得到 %q", tip)
	}
}

func TestFormatPauseRemain(t *testing.T) {
	cases := []struct {
		ms   int64
		want string
	}{
		{0, "不到 1 分钟"},
		{-5000, "不到 1 分钟"},
		{10_000, "不到 1 分钟"},
		{30_000, "1 分钟"},
		{42 * 60_000, "42 分钟"},
		{60 * 60_000, "1 小时"},
		{90 * 60_000, "1 小时 30 分钟"},
		{14 * 3600_000, "14 小时"},
	}
	for _, c := range cases {
		if got := fmtPauseRemain(c.ms); got != c.want {
			t.Fatalf("%d ms：want %q got %q", c.ms, c.want, got)
		}
	}
}

func TestFormatPauseAt(t *testing.T) {
	now := time.Date(2026, 11, 5, 21, 0, 0, 0, time.Local)
	at := func(y int, m time.Month, d, hh, mm int) int64 {
		return time.Date(y, m, d, hh, mm, 0, 0, time.Local).UnixMilli()
	}
	if got := fmtPauseAt(at(2026, 11, 5, 23, 30), now.UnixMilli()); got != "23:30" {
		t.Fatalf("当天应写 HH:MM，得到 %q", got)
	}
	if got := fmtPauseAt(at(2026, 11, 6, 0, 0), now.UnixMilli()); got != "明天 00:00" {
		t.Fatalf("次日应写“明天 00:00”，得到 %q", got)
	}
	if got := fmtPauseAt(at(2026, 11, 9, 0, 0), now.UnixMilli()); got != "11月9日 00:00" {
		t.Fatalf("更远应写日期，得到 %q", got)
	}
}

func TestTimedPauseLabels(t *testing.T) {
	idle := timedPauseLabels(false)
	paused := timedPauseLabels(true)
	if len(idle) != 4 || len(paused) != 4 {
		t.Fatalf("四档：idle=%v paused=%v", idle, paused)
	}
	if idle[0] != "暂停 30 分钟" || idle[3] != "暂停到明天" {
		t.Fatalf("未暂停时是“暂停 …”，得到 %v", idle)
	}
	for _, s := range paused {
		if !strings.HasPrefix(s, "改到") {
			t.Fatalf("暂停中应说“改到 …”，得到 %q", s)
		}
	}
	// “暂停”与“改到”不能混用：前者会被读成“再暂停一次”
	if strings.Contains(paused[0], "暂停 30 分钟") {
		t.Fatalf("暂停中不该再用“暂停 30 分钟”，得到 %q", paused[0])
	}
}

// 托盘文案：定时暂停走“还有多久”，不走“已暂停多久”。
func TestTrayTextsTimedPause(t *testing.T) {
	now := nowMs()
	st := model.SchedulerState{
		Paused:        true,
		PausedAt:      now - 12*60_000,
		PauseReasons:  []string{"manual"},
		PauseDeadline: now + 48*60_000,
	}
	tip, status, action, pause := trayTextsAt(st, false, nil, now)
	if !strings.Contains(tip, "还有") {
		t.Fatalf("定时暂停应说还剩多久，得到 %q", tip)
	}
	if strings.Contains(tip, "已暂停") {
		t.Fatalf("定时暂停不该强调已暂停多久（一夜过去会像故障），得到 %q", tip)
	}
	if action != "立即休息" || pause != "恢复休息" {
		t.Fatalf("菜单项标签不该变，得到 %q / %q", action, pause)
	}
	if !strings.Contains(status, "暂停中") {
		t.Fatalf("状态行应写暂停中，得到 %q", status)
	}

	// 无限暂停仍是原来的口径
	st.PauseDeadline = 0
	tip, _, _, _ = trayTextsAt(st, false, nil, now)
	if !strings.Contains(tip, "已暂停") || !strings.Contains(tip, "手动暂停") {
		t.Fatalf("无限暂停应保留原口径，得到 %q", tip)
	}
}

// ---------- 到期判定（真调度器 + 无头 App） ----------

func TestSchedPauseForSetsDeadline(t *testing.T) {
	a, _ := newPreBreakApp(t, customWaterCfg)
	before := nowMs()
	a.schedPauseFor(30)

	st := a.sched.StateCopy()
	if !st.Paused {
		t.Fatal("应进入暂停")
	}
	if d := st.PauseDeadline - (before + 30*60_000); d < -3000 || d > 3000 {
		t.Fatalf("到期应≈现在 + 30 分钟，得到 %d（差 %d ms）", st.PauseDeadline, d)
	}
	if !strings.Contains(pauseReasonsText(st.PauseReasons), "手动暂停") {
		t.Fatalf("定时暂停的原因仍是手动，得到 %v", st.PauseReasons)
	}

	// 取消定时 → 仍暂停，只是没有截止时刻
	a.schedPauseFor(0)
	st = a.sched.StateCopy()
	if !st.Paused || st.PauseDeadline != 0 {
		t.Fatalf("取消定时后应仍暂停且无截止时刻，得到 paused=%v deadline=%d", st.Paused, st.PauseDeadline)
	}
}

func TestSchedPauseUntilTomorrow(t *testing.T) {
	a, _ := newPreBreakApp(t, customWaterCfg)
	a.schedPauseUntilTomorrow()
	st := a.sched.StateCopy()
	want := nextMidnight(time.Now()).UnixMilli()
	if d := st.PauseDeadline - want; d < -3000 || d > 3000 {
		t.Fatalf("到期应落在下一个本地零点 %d，得到 %d", want, st.PauseDeadline)
	}
	if !st.Paused {
		t.Fatal("应进入暂停")
	}
}

// 到期且没有任何阻挡：摘掉 manual 恢复，截止时刻清零。
func TestPauseExpiryResumesWhenClear(t *testing.T) {
	a, _ := newPreBreakApp(t, customWaterCfg)
	a.sched.PauseUntil(nowMs() - 1000)

	a.pauseExpiryTick()

	if a.sched.IsPaused() {
		t.Fatal("没有阻挡时应恢复")
	}
	if got := a.sched.StateCopy().PauseDeadline; got != 0 {
		t.Fatalf("恢复后截止时刻应清零，得到 %d", got)
	}
	if hold := a.pauseHoldSnapshot(); len(hold) != 0 {
		t.Fatalf("恢复后不该留着阻挡标记，得到 %v", hold)
	}
}

// 到期时勿扰还在：保持暂停并点名，条件消失那一拍才恢复。
func TestPauseExpiryHoldsWhenBlocked(t *testing.T) {
	a, _ := newPreBreakApp(t, customWaterCfg)
	a.sched.PauseUntil(nowMs() - 1000)
	a.setAutoReasonActive(pauseReasonDnd, true)

	a.pauseExpiryTick()
	if !a.sched.IsPaused() {
		t.Fatal("勿扰生效时不该恢复")
	}
	if hold := a.pauseHoldSnapshot(); len(hold) != 1 || hold[0] != "勿扰" {
		t.Fatalf("应记下阻挡原因“勿扰”，得到 %v", hold)
	}

	a.setAutoReasonActive(pauseReasonDnd, false)
	a.pauseExpiryTick()
	if a.sched.IsPaused() {
		t.Fatal("条件消失后应恢复")
	}
	if hold := a.pauseHoldSnapshot(); len(hold) != 0 {
		t.Fatalf("恢复后阻挡标记应清空，得到 %v", hold)
	}
}

// 人不在（idle 挂在 PauseReasons 里）也算阻挡 —— 空闲不走登记表，靠这条补。
// 场景：23:55 设“到明天”、合盖睡觉，0 点到期时人不在，等第二天回来再恢复。
func TestPauseExpiryHoldsWhenIdle(t *testing.T) {
	a, _ := newPreBreakApp(t, customWaterCfg)
	a.sched.PauseUntil(nowMs() - 1000)
	a.sched.Pause(scheduler.PauseReasonIdle)

	a.pauseExpiryTick()

	if !a.sched.IsPaused() {
		t.Fatal("人不在时不该恢复")
	}
	hold := a.pauseHoldSnapshot()
	if len(hold) != 1 || hold[0] != "系统空闲" {
		t.Fatalf("应点名“系统空闲”，得到 %v", hold)
	}

	// 人回来：idle 被摘掉，下一拍恢复
	a.sched.Resume(scheduler.PauseReasonIdle)
	a.pauseExpiryTick()
	if a.sched.IsPaused() {
		t.Fatal("人回来后应恢复")
	}
}

// 还没到期：什么都不做，也不该清掉阻挡标记之外的东西。
func TestPauseExpiryDoesNothingBeforeDeadline(t *testing.T) {
	a, _ := newPreBreakApp(t, customWaterCfg)
	a.sched.PauseUntil(nowMs() + 3_600_000)

	a.pauseExpiryTick()

	if !a.sched.IsPaused() {
		t.Fatal("未到期不该恢复")
	}
	if got := a.sched.StateCopy().PauseDeadline; got == 0 {
		t.Fatal("未到期不该清掉截止时刻")
	}
}

// 定时暂停中点了“恢复休息”，但勿扰还挂着：这段定时暂停已经结束，不该再报
// “时长已到”——那是用户刚做过的操作，不该被说成一件待办。
func TestPauseExpiryIgnoresAfterManualResume(t *testing.T) {
	a, _ := newPreBreakApp(t, customWaterCfg)
	a.sched.PauseUntil(nowMs() - 1000)
	a.sched.Pause(pauseReasonDnd)

	// 用户点恢复：摘掉 manual，勿扰仍在（PauseReasons 非空 → 截止时刻暂不清）
	a.sched.Resume(scheduler.PauseReasonManual)
	if !a.sched.IsPaused() {
		t.Fatal("前置：勿扰还挂着，应仍在暂停")
	}

	a.pauseExpiryTick()

	if hold := a.pauseHoldSnapshot(); len(hold) != 0 {
		t.Fatalf("手动恢复后不该再报阻挡，得到 %v", hold)
	}
	if !a.sched.IsPaused() {
		t.Fatal("勿扰仍在，不该被到期判定恢复")
	}
}

// 前端读的是 JSON 的 pauseDeadline：字段名写错会静默失效（状态格不显示结束时刻），
// 所以在这里钉住序列化结果，而不是只测 Go 结构体。
func TestSchedulerStateJSONHasPauseDeadline(t *testing.T) {
	b, err := json.Marshal(model.SchedulerState{PauseDeadline: 123})
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	if !strings.Contains(string(b), `"pauseDeadline":123`) {
		t.Fatalf("状态 JSON 应带 pauseDeadline 字段，得到 %s", b)
	}
}

// 托盘层面复现“定时那段已结束，却还在说还有多久”：
// 定时暂停中点了“恢复休息”，勿扰还挂着（暂停继续）→ 托盘必须回到
// “已暂停 X：勿扰”，不能再说“还有 25 分钟（到 …）”，也不该再给“取消定时”。
func TestTrayDropsTimedTextAfterManualResumeKeepsAuto(t *testing.T) {
	a, _ := newPreBreakApp(t, customWaterCfg)
	a.sched.Pause(pauseReasonDnd)
	a.sched.PauseUntil(nowMs() + 25*60_000)
	a.sched.Resume(scheduler.PauseReasonManual)

	st := a.sched.StateCopy()
	if !st.Paused {
		t.Fatal("前置：勿扰还挂着，应仍在暂停")
	}
	if st.PauseDeadline != 0 {
		t.Fatalf("前置：手动那段结束后不该再有到期时刻，得到 %d", st.PauseDeadline)
	}
	tip, _, _, _ := trayTextsAt(st, false, nil, nowMs())
	if strings.Contains(tip, "还有") {
		t.Fatalf("托盘不该再说“还有 …（到 …）”，得到 %q", tip)
	}
	if !strings.Contains(tip, "已暂停") || !strings.Contains(tip, "勿扰") {
		t.Fatalf("托盘应回到“已暂停 … ：勿扰”，得到 %q", tip)
	}
}

// 到期时被勿扰挡住、之后勿扰结束：现在停在“勿扰这条原因还在（按策略保留）”上。
//
// 这条用例刻意钉的是**当前语义**而不是理想语义：到期只摘 manual，其余的自动原因按
// 各自策略走（勿扰 / 全屏或演示 / 两条投屏，条件消失也不自动恢复）。结果是“用户设了『到明天』，
// 零点正开着勿扰 → 第二天早上关掉勿扰，提醒仍然停着，得点一下恢复”。
// 留着它有两个用处：一是把这条行为写进验收，二是将来若改成“到期授权恢复到底”，
// 这条会立刻变红，提醒按新口径重写。
func TestTimedPauseExpiryAfterHoldClearsLeavesAutoPause(t *testing.T) {
	a, _ := newPreBreakApp(t, customWaterCfg)
	a.sched.Pause(pauseReasonDnd)
	a.setAutoReasonActive(pauseReasonDnd, true)
	a.sched.PauseUntil(nowMs() - 1000)

	// 到期这一拍：勿扰条件成立 → 挡住，保持暂停
	a.pauseExpiryTick()
	if !a.sched.IsPaused() {
		t.Fatal("勿扰生效时不该恢复")
	}
	if hold := a.pauseHoldSnapshot(); len(hold) != 1 {
		t.Fatalf("应记下阻挡原因，得到 %v", hold)
	}

	// 勿扰结束（条件消失）：按策略保留暂停，等用户点恢复 —— 到期判定也不该再报阻挡
	a.setAutoReasonActive(pauseReasonDnd, false)
	a.pauseExpiryTick()

	st := a.sched.StateCopy()
	if !st.Paused {
		t.Fatal("当前语义：勿扰这条原因仍在，暂停保持")
	}
	if hold := a.pauseHoldSnapshot(); len(hold) != 0 {
		t.Fatalf("条件已消失，不该再报阻挡，得到 %v", hold)
	}
	if st.PauseDeadline != 0 {
		t.Fatalf("到期且已摘掉 manual 后，截止时刻应清零，得到 %d", st.PauseDeadline)
	}
	if !a.sched.HasPauseReason(pauseReasonDnd) || a.sched.HasPauseReason(scheduler.PauseReasonManual) {
		t.Fatalf("应只剩勿扰这条原因，得到 %v", st.PauseReasons)
	}
}
