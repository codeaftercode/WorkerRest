package main

import (
	"testing"
	"time"
	"workerrest/internal/model"
	"workerrest/internal/settings"
)

// 「暂停休息时」三种处置（正常提醒 / 推迟提醒 / 跳过提醒，默认推迟）的回归：
//   - 跳过：外卖提醒 / 自定义提醒在暂停态下到点时不弹任何窗，直接按「跳过」记一笔统计；
//   - 正常提醒：暂停期间照常弹（提前提示小窗 + 提醒窗口），不进跳过统计；
//   - 推迟提醒：暂停期间不弹、也不记账，按该提醒的「可推迟」时长往后推；推完仍暂停就
//     继续推，恢复后按推迟后的时刻弹；
//   - 短 / 长休息三种情形都保持原状（暂停期间不弹、不记账）。
//
// 一条铁律：暂停态每半秒一跳都会重算一次，任何分支重复记账 / 重复弹窗都算 bug ——
// 所以每个用例都跑多跳，断言只发生一次。

// ---------- 配置层 ----------

func TestPauseActionDefaultsToPostpone(t *testing.T) {
	// 旧配置没有这个键：外卖与自定义提醒都应视为「推迟提醒」
	s := testSettings(t, `{"customBreaks":[{"id":"cb-a","name":"喝水","config":{"enabled":true}}]}`)
	if got := s.TakeoutPauseAction(); got != settings.PauseActionPostpone {
		t.Errorf("取不到 pauseAction 时应默认推迟（外卖），得到 %q", got)
	}
	if got := s.Break("short").PauseAction; got != settings.PauseActionPostpone {
		t.Errorf("取不到 pauseAction 时应默认推迟（配置层），得到 %q", got)
	}
	if got := s.CustomBreakList(); len(got) != 1 || got[0].Config.PauseAction != settings.PauseActionPostpone {
		t.Errorf("自定义提醒缺省应为推迟，得到 %+v", got)
	}
	// 新建自定义提醒的出厂配置也必须是推迟
	if got := settings.DefaultCustomBreakConfig()["pauseAction"]; got != settings.PauseActionPostpone {
		t.Errorf("新建自定义提醒的默认处置应为推迟，得到 %#v", got)
	}
}

func TestPauseActionExplicitValues(t *testing.T) {
	s := testSettings(t, `{"takeout":{"pauseAction":"postpone"},`+
		`"customBreaks":[{"id":"cb-a","name":"喝水","config":{"pauseAction":"remind"}}]}`)
	if got := s.TakeoutPauseAction(); got != settings.PauseActionPostpone {
		t.Errorf("外卖应读成推迟，得到 %q", got)
	}
	if got := s.CustomBreakList()[0].Config.PauseAction; got != settings.PauseActionRemind {
		t.Errorf("自定义提醒应读成正常提醒，得到 %q", got)
	}
	// 非法值一律落回默认的推迟（手改配置 / 旧版本写入的脏值）
	bad := testSettings(t, `{"takeout":{"pauseAction":"whatever"},"customBreaks":[]}`)
	if got := bad.TakeoutPauseAction(); got != settings.PauseActionPostpone {
		t.Errorf("非法值应落回推迟，得到 %q", got)
	}
}

// 保存路径：settings.NormalizeTakeout 重建 takeout 对象时必须带上这个键（否则存一次就丢）
func TestNormalizeTakeoutKeepsPauseAction(t *testing.T) {
	out := settings.NormalizeTakeout(map[string]any{
		"takeout": map[string]any{"enabled": true, "pauseAction": "remind", "slots": []any{}},
	})
	if got, _ := out["pauseAction"].(string); got != settings.PauseActionRemind {
		t.Fatalf("settings.NormalizeTakeout 应保留 pauseAction=remind，得到 %#v", out["pauseAction"])
	}
	// 缺省（旧配置）应补成 postpone：前端下拉默认就是「推迟提醒」
	out = settings.NormalizeTakeout(map[string]any{"takeout": map[string]any{"enabled": true}})
	if got, _ := out["pauseAction"].(string); got != settings.PauseActionPostpone {
		t.Fatalf("旧配置归一化后应补成 postpone，得到 %#v", out["pauseAction"])
	}
}

// 自定义提醒的配置归一化：已有键要校验，旧键 skipWhenPaused 要清掉，
// 原本没有这个键就不补写（短 / 长休息不写它）
func TestNormalizeBreakConfigPauseAction(t *testing.T) {
	if got := settings.NormalizeBreakConfigOf(map[string]any{"pauseAction": "bogus"})["pauseAction"]; got != settings.PauseActionPostpone {
		t.Fatalf("非法值应落回 postpone，得到 %#v", got)
	}
	out := settings.NormalizeBreakConfigOf(map[string]any{"mode": "countdown", "skipWhenPaused": true})
	if _, has := out["skipWhenPaused"]; has {
		t.Fatal("旧键 skipWhenPaused 应在归一化时被清掉")
	}
	if _, has := out["pauseAction"]; has {
		t.Fatal("原本没有 pauseAction 时不应补写")
	}
}

// ---------- 调度器（自定义提醒） ----------

// 固定时刻模式 + 跳过提醒：暂停期间到点的那一跳记一次跳过，且只记一次
func TestPausedSkipCustomFixedSlot(t *testing.T) {
	now := time.Now()
	cfg := `{"customBreaks":[{"id":"cb-water","name":"喝水提醒","config":{` +
		`"enabled":true,"pauseAction":"skip","mode":"fixed","durationMs":20000,"slots":[` +
		`{"id":"s1","hour":` + itoa(now.Hour()) + `,"minute":` + itoa(now.Minute()) +
		`,"repeat":"daily","days":[],"enabled":true}]}}]}`
	s, host := newTestSched(t, cfg)
	s.replan()
	s.pause("manual")

	s.onTick()
	if len(host.skips) != 1 || host.skips[0] != "cb-water" {
		t.Fatalf("暂停期间到点的自定义提醒应记 1 次跳过，得到 %v", host.skips)
	}
	if len(host.breaks) != 0 {
		t.Fatalf("暂停期间不应弹休息窗口，得到 %v", host.breaks)
	}
	if len(host.pre) != 0 {
		t.Fatalf("暂停期间不应弹提前提示，得到 %v", host.pre)
	}
	// 同一分钟内再跑一跳：不得重复记账
	s.onTick()
	if len(host.skips) != 1 {
		t.Fatalf("同一 occurrence 不应重复记账，得到 %v", host.skips)
	}
}

// 倒计时模式 + 跳过提醒：挂起那一跳在暂停期间过点 → 记一次跳过
func TestPausedSkipCustomCountdown(t *testing.T) {
	cfg := `{"customBreaks":[{"id":"cb-water","name":"喝水提醒","config":{` +
		`"enabled":true,"pauseAction":"skip","mode":"countdown","intervalMs":600000,"durationMs":20000,"slots":[]}}]}`
	s, host := newTestSched(t, cfg)
	s.replan()
	// 模拟「暂停期间时间走完了这一跳」
	s.mu.Lock()
	s.state.NextAt = nowMs() - 1000
	for i := range s.state.Lines {
		s.state.Lines[i].At = s.state.NextAt
	}
	s.mu.Unlock()
	s.pause("manual")

	s.onTick()
	s.onTick()
	if len(host.skips) != 1 || host.skips[0] != "cb-water" {
		t.Fatalf("倒计时到点应记 1 次跳过，得到 %v", host.skips)
	}
	if len(host.breaks) != 0 {
		t.Fatalf("暂停期间不应弹休息窗口，得到 %v", host.breaks)
	}
}

// 正常提醒：暂停期间照常弹提醒窗口（不记账），且同一次 occurrence 只弹一次
func TestPausedRemindCustomFixedSlot(t *testing.T) {
	now := time.Now()
	cfg := `{"customBreaks":[{"id":"cb-water","name":"喝水提醒","config":{` +
		`"enabled":true,"pauseAction":"remind","mode":"fixed","durationMs":20000,"slots":[` +
		`{"id":"s1","hour":` + itoa(now.Hour()) + `,"minute":` + itoa(now.Minute()) +
		`,"repeat":"daily","days":[],"enabled":true}]}}]}`
	s, host := newTestSched(t, cfg)
	s.replan()
	s.pause("manual")
	s.onTick()
	if len(host.skips) != 0 {
		t.Fatalf("「正常提醒」不应记账，得到 %v", host.skips)
	}
	if len(host.breaks) != 1 || host.breaks[0] != "cb-water" {
		t.Fatalf("暂停期间到点应照常弹提醒，得到 %v", host.breaks)
	}
	// 再跑两跳：同一次 occurrence 不得重复弹（第一跳已进休息窗口，第二跳是收窗判定）
	s.onTick()
	s.onTick()
	if len(host.breaks) != 1 {
		t.Fatalf("同一 occurrence 不应重复弹，得到 %v", host.breaks)
	}
}

// 正常提醒 + 倒计时线到点：同样照常弹（挂起那一跳过点即触发）
func TestPausedRemindCustomCountdown(t *testing.T) {
	cfg := `{"customBreaks":[{"id":"cb-water","name":"喝水提醒","config":{` +
		`"enabled":true,"pauseAction":"remind","mode":"countdown","intervalMs":600000,"durationMs":20000,"slots":[]}}]}`
	s, host := newTestSched(t, cfg)
	s.replan()
	s.mu.Lock()
	s.state.NextAt = nowMs() - 1000
	for i := range s.state.Lines {
		s.state.Lines[i].At = s.state.NextAt
	}
	s.mu.Unlock()
	s.pause("manual")
	s.onTick()
	if len(host.breaks) != 1 || host.breaks[0] != "cb-water" {
		t.Fatalf("倒计时到点应照常弹提醒，得到 %v", host.breaks)
	}
	if len(host.skips) != 0 {
		t.Fatalf("「正常提醒」不应记账，得到 %v", host.skips)
	}
}

// 正常提醒 + 提前通知：落在「原定时刻 − 提前量」区间时照常弹提前提示小窗
func TestPausedRemindCustomPreNotify(t *testing.T) {
	at := time.Now().Add(5 * time.Minute) // 5 分钟后到点：此刻正落在「到点 − 提前量」那一分钟
	cfg := `{"customBreaks":[{"id":"cb-water","name":"喝水提醒","config":{` +
		`"enabled":true,"pauseAction":"remind","mode":"fixed","durationMs":20000,"notifyBeforeMs":300000,"slots":[` +
		`{"id":"s1","hour":` + itoa(at.Hour()) + `,"minute":` + itoa(at.Minute()) +
		`,"repeat":"daily","days":[],"enabled":true}]}}]}`
	s, host := newTestSched(t, cfg)
	s.replan()
	s.pause("manual")
	s.onTick()
	s.onTick()
	if len(host.pre) != 1 || host.pre[0] != "cb-water" {
		t.Fatalf("提前区间应照常弹提前提示小窗，得到 %v", host.pre)
	}
	if len(host.breaks) != 0 {
		t.Fatalf("还没到点不应弹提醒窗口，得到 %v", host.breaks)
	}
	if len(host.skips) != 0 {
		t.Fatalf("「正常提醒」不应记账，得到 %v", host.skips)
	}
}

// 推迟提醒：到点不弹窗也不记账，只排一条「推迟到 X」的计划；再到期时仍暂停就继续推
func TestPausedPostponeCustomFixedSlot(t *testing.T) {
	now := time.Now()
	cfg := `{"customBreaks":[{"id":"cb-water","name":"喝水提醒","config":{` +
		`"enabled":true,"pauseAction":"postpone","postponeMs":120000,"mode":"fixed","durationMs":20000,"slots":[` +
		`{"id":"s1","hour":` + itoa(now.Hour()) + `,"minute":` + itoa(now.Minute()) +
		`,"repeat":"daily","days":[],"enabled":true}]}}]}`
	s, host := newTestSched(t, cfg)
	s.replan()
	s.pause("manual")

	s.onTick()
	s.onTick()
	if len(host.breaks) != 0 || len(host.pre) != 0 || len(host.skips) != 0 {
		t.Fatalf("推迟提醒不应弹窗也不应记账，得到 breaks=%v pre=%v skips=%v",
			host.breaks, host.pre, host.skips)
	}
	s.mu.Lock()
	if len(s.postponed) != 1 {
		s.mu.Unlock()
		t.Fatalf("应排下 1 条推迟计划，得到 %d", len(s.postponed))
	}
	var due int64
	for _, p := range s.postponed {
		due = p.dueAt
	}
	s.mu.Unlock()
	if due < nowMs()+90_000 || due > nowMs()+150_000 {
		t.Fatalf("推迟后的时刻应约为 now+2 分钟，得到 %d", due-nowMs())
	}

	// 推后的时刻又到了、仍然暂停：继续往后推，且不弹窗
	s.mu.Lock()
	for _, p := range s.postponed {
		p.dueAt = nowMs() - 1000
	}
	s.mu.Unlock()
	s.onTick()
	s.mu.Lock()
	again := int64(0)
	for _, p := range s.postponed {
		again = p.dueAt
	}
	s.mu.Unlock()
	if len(host.breaks) != 0 {
		t.Fatalf("仍暂停时不应弹窗，得到 %v", host.breaks)
	}
	if again <= nowMs() {
		t.Fatalf("仍暂停时应继续往后推，得到 %d", again-nowMs())
	}
}

// 推迟提醒：恢复后按推迟后的时刻弹出，且只弹一次（去重表不随 resume 清空）
func TestPausedPostponeFiresAfterResume(t *testing.T) {
	now := time.Now()
	cfg := `{"customBreaks":[{"id":"cb-water","name":"喝水提醒","config":{` +
		`"enabled":true,"pauseAction":"postpone","postponeMs":120000,"mode":"fixed","durationMs":20000,"slots":[` +
		`{"id":"s1","hour":` + itoa(now.Hour()) + `,"minute":` + itoa(now.Minute()) +
		`,"repeat":"daily","days":[],"enabled":true}]}}]}`
	s, host := newTestSched(t, cfg)
	s.replan()
	s.pause("manual")
	s.onTick()
	s.resume("manual")
	s.mu.Lock()
	pending := len(s.postponed)
	s.mu.Unlock()
	if pending != 1 {
		t.Fatalf("恢复后推迟计划应保留，得到 %d", pending)
	}
	// 推到点的时刻已经到了：这一跳弹出来
	s.mu.Lock()
	for _, p := range s.postponed {
		p.dueAt = nowMs() - 1000
	}
	s.mu.Unlock()
	s.onTick()
	if len(host.breaks) != 1 || host.breaks[0] != "cb-water" {
		t.Fatalf("推迟后的时刻到点应弹出一次，得到 %v", host.breaks)
	}
	if len(host.skips) != 0 {
		t.Fatalf("推迟不算跳过，得到 %v", host.skips)
	}
	// 再跑几跳：同一次 occurrence 不得重复弹
	s.onTick()
	s.resume("manual")
	s.onTick()
	if len(host.breaks) != 1 {
		t.Fatalf("同一 occurrence 不应重复弹，得到 %v", host.breaks)
	}
}

// 推迟提醒但「可推迟」填了 0：没有时长可推，退化成跳过（否则每跳原地打转、永远不弹）
func TestPausedPostponeZeroFallsBackToSkip(t *testing.T) {
	now := time.Now()
	cfg := `{"customBreaks":[{"id":"cb-water","name":"喝水提醒","config":{` +
		`"enabled":true,"pauseAction":"postpone","postponeMs":0,"mode":"fixed","durationMs":20000,"slots":[` +
		`{"id":"s1","hour":` + itoa(now.Hour()) + `,"minute":` + itoa(now.Minute()) +
		`,"repeat":"daily","days":[],"enabled":true}]}}]}`
	s, host := newTestSched(t, cfg)
	s.replan()
	s.pause("manual")
	s.onTick()
	s.onTick()
	if len(host.skips) != 1 {
		t.Fatalf("推迟时长为 0 时应退化成 1 次跳过，得到 %v", host.skips)
	}
	s.mu.Lock()
	empty := len(s.postponed) == 0
	s.mu.Unlock()
	if !empty {
		t.Fatal("退化成跳过时不应留下推迟计划")
	}
}

// 短 / 长休息保持原状：暂停期间到点既不弹也不记账
func TestPausedSkipIgnoresBuiltinBreaks(t *testing.T) {
	s, host := newTestSched(t, shortCountdown)
	s.replan()
	s.mu.Lock()
	s.state.NextAt = nowMs() - 1000
	for i := range s.state.Lines {
		s.state.Lines[i].At = s.state.NextAt
	}
	s.mu.Unlock()
	s.pause("manual")
	s.onTick()
	if len(host.skips) != 0 || len(host.breaks) != 0 || len(host.pre) != 0 {
		t.Fatalf("短休息暂停期间应保持原状，得到 skips=%v breaks=%v pre=%v", host.skips, host.breaks, host.pre)
	}
	// 恢复后照常从冻结点继续（这里只验不炸）
	s.resume("manual")
	s.onTick()
}

// 恢复后清空去重表：下一段暂停里新的 occurrence 照常记账
func TestPauseSkippedClearedOnResume(t *testing.T) {
	now := time.Now()
	cfg := `{"customBreaks":[{"id":"cb-water","name":"喝水提醒","config":{` +
		`"enabled":true,"pauseAction":"skip","mode":"fixed","durationMs":20000,"slots":[` +
		`{"id":"s1","hour":` + itoa(now.Hour()) + `,"minute":` + itoa(now.Minute()) +
		`,"repeat":"daily","days":[],"enabled":true}]}}]}`
	s, host := newTestSched(t, cfg)
	s.replan()
	s.pause("manual")
	s.onTick()
	s.resume("manual")
	s.mu.Lock()
	empty := len(s.pauseSkipped) == 0 && len(s.pauseFired) == 0
	s.mu.Unlock()
	if !empty {
		t.Fatal("完全恢复后去重表应清空")
	}
	if len(host.skips) != 1 {
		t.Fatalf("恢复不应影响已记的跳过，得到 %v", host.skips)
	}
}

// ---------- 外卖 ----------

// takeoutSkipCfg 与 takeoutCfg 等价，但「暂停休息时」显式写 skip（默认已改成推迟；
// 时刻取 12:30，正常跑测试时不会是「此刻」）
const takeoutSkipCfg = `{"takeout":{"enabled":true,"leadSec":0,"postponeMin":15,"pauseAction":"skip","slots":[{"id":"s1","hour":12,"minute":30,"repeat":"daily","days":[],"enabled":true}]},"reminderMessages":{"takeout":{"order":"random","fixedId":"","items":[{"id":"m1","text":"该点外卖啦","enabled":true}]}}}`

// takeoutDueNowCfg 一条「此刻到点」的外卖配置；extra 追加到 takeout 对象里
func takeoutDueNowCfg(extra string) string {
	now := time.Now()
	return `{"takeout":{"enabled":true,"leadSec":0,"postponeMin":15` + extra + `,"slots":[` +
		`{"id":"s1","hour":` + itoa(now.Hour()) + `,"minute":` + itoa(now.Minute()) +
		`,"repeat":"daily","days":[],"enabled":true}]}}`
}

// 跳过提醒：暂停期间外卖到点不弹窗、不弹提前提示，记一次「取消 / 跳过」
func TestTakeoutPausedSkipRecords(t *testing.T) {
	cfg := takeoutDueNowCfg(`,"pauseAction":"skip"`)
	a := newTakeoutApp(t, cfg)
	s, _ := newTestSched(t, cfg)
	s.pause("manual")
	a.sched = s

	a.checkTakeoutReminders()
	if a.takeoutCurrent != nil {
		t.Fatal("暂停期间不应弹出外卖提醒窗口")
	}
	if a.preInfo.Owner == "takeout" {
		t.Fatal("暂停期间不应弹出外卖提前提示小窗")
	}
	if got := a.statsStore().Today().TakeoutSkipped; got != 1 {
		t.Fatalf("暂停期间到点应记 1 次跳过，得到 %d", got)
	}
	// 同一分钟内再跑一跳：不重复记账
	a.checkTakeoutReminders()
	if got := a.statsStore().Today().TakeoutSkipped; got != 1 {
		t.Fatalf("同一 occurrence 不应重复记账，得到 %d", got)
	}
}

// 正常提醒：暂停期间照常弹外卖提醒窗口，不记账
func TestTakeoutPausedRemindShowsWindow(t *testing.T) {
	cfg := takeoutDueNowCfg(`,"pauseAction":"remind"`)
	a := newTakeoutApp(t, cfg)
	s, _ := newTestSched(t, cfg)
	s.pause("manual")
	a.sched = s

	a.checkTakeoutReminders()
	if a.takeoutCurrent == nil {
		t.Fatal("「正常提醒」时暂停期间到点应照常弹外卖提醒窗口")
	}
	if got := a.statsStore().Today().TakeoutSkipped; got != 0 {
		t.Fatalf("「正常提醒」不应记账，得到 %d", got)
	}
	// 同一分钟内再跑一跳：靠 takeoutFired 去重，不重复弹
	a.checkTakeoutReminders()
	if a.takeoutCurrent == nil {
		t.Fatal("重复 tick 不应关掉已弹出的提醒窗口")
	}
}

// 推迟提醒：到点不弹窗、不记账，排一条「推迟到 now+15 分钟」；仍暂停时再到期就继续推
func TestTakeoutPausedPostponeSetsPlan(t *testing.T) {
	cfg := takeoutDueNowCfg(`,"pauseAction":"postpone"`)
	a := newTakeoutApp(t, cfg)
	s, _ := newTestSched(t, cfg)
	s.pause("manual")
	a.sched = s

	a.checkTakeoutReminders()
	if a.takeoutCurrent != nil {
		t.Fatal("「推迟提醒」不应弹外卖提醒窗口")
	}
	if a.preInfo.Owner == "takeout" {
		t.Fatal("「推迟提醒」不应弹提前提示小窗")
	}
	if got := a.statsStore().Today().TakeoutSkipped; got != 0 {
		t.Fatalf("「推迟提醒」不算跳过，得到 %d", got)
	}
	if a.takeoutPostponed == nil {
		t.Fatal("应排下一条推迟计划")
	}
	first := a.takeoutPostponed.DueAt - nowMs()
	if first < 14*60_000 || first > 16*60_000 {
		t.Fatalf("推迟后的时刻应约为 now+15 分钟，得到 %d ms", first)
	}
	// 同一分钟再跑：计划不变（不重复排、也不弹）
	a.checkTakeoutReminders()
	if a.takeoutPostponed == nil || a.takeoutPostponed.DueAt != nowMs()+first {
		// 允许毫秒级抖动，只校验「仍然在未来且没有被清掉」
		if a.takeoutPostponed == nil {
			t.Fatal("重复 tick 不应清掉推迟计划")
		}
	}
	// 推后的时刻又到了、仍然暂停：继续往后推，不弹窗
	a.takeoutPostponed.DueAt = nowMs() - 1000
	a.checkTakeoutReminders()
	if a.takeoutPostponed == nil || a.takeoutPostponed.DueAt <= nowMs() {
		t.Fatal("仍暂停时应继续往后推")
	}
	if a.takeoutCurrent != nil {
		t.Fatal("仍暂停时不应弹窗")
	}
}

// 推迟提醒：恢复后按推迟后的时刻弹窗
func TestTakeoutPausedPostponeFiresAfterResume(t *testing.T) {
	cfg := takeoutDueNowCfg(`,"pauseAction":"postpone"`)
	a := newTakeoutApp(t, cfg)
	s, _ := newTestSched(t, cfg)
	s.pause("manual")
	a.sched = s
	a.checkTakeoutReminders()
	if a.takeoutPostponed == nil {
		t.Fatal("应排下一条推迟计划")
	}
	s.resume("manual")
	a.takeoutPostponed.DueAt = nowMs() - 1000
	a.checkTakeoutReminders()
	if a.takeoutCurrent == nil {
		t.Fatal("恢复后推迟到点应弹出外卖提醒窗口")
	}
	if got := a.statsStore().Today().TakeoutSkipped; got != 0 {
		t.Fatalf("推迟到点弹出不算跳过，得到 %d", got)
	}
}

// 推迟提醒但「可推迟」填了 0：退化成跳过
func TestTakeoutPausedPostponeZeroFallsBackToSkip(t *testing.T) {
	now := time.Now()
	cfg := `{"takeout":{"enabled":true,"leadSec":0,"pauseAction":"postpone","postponeMin":0,"slots":[` +
		`{"id":"s1","hour":` + itoa(now.Hour()) + `,"minute":` + itoa(now.Minute()) +
		`,"repeat":"daily","days":[],"enabled":true}]}}`
	a := newTakeoutApp(t, cfg)
	s, _ := newTestSched(t, cfg)
	s.pause("manual")
	a.sched = s
	a.checkTakeoutReminders()
	if a.takeoutPostponed != nil {
		t.Fatal("推迟时长为 0 时不应留下推迟计划")
	}
	if got := a.statsStore().Today().TakeoutSkipped; got != 1 {
		t.Fatalf("推迟时长为 0 时应记 1 次跳过，得到 %d", got)
	}
}

// 跳过的推迟线：暂停期间推迟线到点同样按跳过处理并清掉计划（恢复后不补弹）
func TestTakeoutPausedSkipPostponedLine(t *testing.T) {
	cfg := takeoutSkipCfg
	a := newTakeoutApp(t, cfg)
	s, _ := newTestSched(t, cfg)
	s.pause("manual")
	a.sched = s
	a.takeoutPostponed = &model.TakeoutPostponed{
		Slot:  model.TimeSlot{ID: "s1", Hour: 12, Minute: 30, Repeat: "daily", Enabled: true},
		DueAt: nowMs() - 1000,
		Count: 1,
	}
	a.checkTakeoutReminders()
	if a.takeoutPostponed != nil {
		t.Fatal("暂停期间到点的推迟线应清空")
	}
	if got := a.statsStore().Today().TakeoutSkipped; got != 1 {
		t.Fatalf("推迟线到点应记 1 次跳过，得到 %d", got)
	}
	if a.takeoutCurrent != nil {
		t.Fatal("暂停期间不应弹窗外卖")
	}
}

// 默认（未写 pauseAction）= 推迟提醒：暂停期间到点不弹窗、不记账，排一条推迟计划
func TestPausedDefaultPostponeCustomFixedSlot(t *testing.T) {
	now := time.Now()
	cfg := `{"customBreaks":[{"id":"cb-water","name":"喝水提醒","config":{` +
		`"enabled":true,"postponeMs":120000,"mode":"fixed","durationMs":20000,"slots":[` +
		`{"id":"s1","hour":` + itoa(now.Hour()) + `,"minute":` + itoa(now.Minute()) +
		`,"repeat":"daily","days":[],"enabled":true}]}}]}`
	s, host := newTestSched(t, cfg)
	s.replan()
	s.pause("manual")

	s.onTick()
	if len(host.skips) != 0 {
		t.Fatalf("默认「推迟提醒」不应记跳过，得到 %v", host.skips)
	}
	if len(host.breaks) != 0 || len(host.pre) != 0 {
		t.Fatalf("默认「推迟提醒」不应弹窗，得到 breaks=%v pre=%v", host.breaks, host.pre)
	}
	s.mu.Lock()
	n := len(s.postponed)
	var due int64
	for _, p := range s.postponed {
		due = p.dueAt
	}
	s.mu.Unlock()
	if n != 1 {
		t.Fatalf("应排 1 条推迟计划，得到 %d", n)
	}
	if d := due - nowMs(); d < 115_000 || d > 125_000 {
		t.Fatalf("推迟后的时刻应约为 now+2 分钟，得到 %d ms", d)
	}
}

// 默认（未写 pauseAction）= 推迟提醒：外卖暂停期间到点同样只排推迟计划
func TestTakeoutPausedDefaultPostpone(t *testing.T) {
	cfg := takeoutDueNowCfg("")
	a := newTakeoutApp(t, cfg)
	s, _ := newTestSched(t, cfg)
	s.pause("manual")
	a.sched = s

	a.checkTakeoutReminders()
	if a.takeoutCurrent != nil {
		t.Fatal("默认「推迟提醒」不应弹外卖提醒窗口")
	}
	if a.preInfo.Owner == "takeout" {
		t.Fatal("默认「推迟提醒」不应弹提前提示小窗")
	}
	if got := a.statsStore().Today().TakeoutSkipped; got != 0 {
		t.Fatalf("默认「推迟提醒」不算跳过，得到 %d", got)
	}
	if a.takeoutPostponed == nil {
		t.Fatal("默认「推迟提醒」应排下一条推迟计划")
	}
	if d := a.takeoutPostponed.DueAt - nowMs(); d < 14*60_000 || d > 16*60_000 {
		t.Fatalf("推迟后的时刻应约为 now+15 分钟，得到 %d ms", d)
	}
}
