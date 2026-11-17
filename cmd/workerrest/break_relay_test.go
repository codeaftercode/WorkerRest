package main

// 「提前结束只结束当前这扇窗」的宿主侧回归（2026-11-16 用户口径）：
//
//	① 右上角还有卡时，最早到点的那张接替成一扇新窗，其余继续挂在它的右上角；
//	② 没被接力成窗的自定义提醒，在休息收尾时按“显示过一次”记一笔统计；
//	③ 接力成窗的那种由 OnBreakStart / OnBreakEnd 记账，不能再被补记一次。

import (
	"log"
	"path/filepath"
	"strings"
	"testing"

	"workerrest/internal/model"
	"workerrest/internal/scheduler"
)

const relayCfg = `{
  "short":{"enabled":true,"mode":"countdown","intervalMs":600000,"durationMs":20000,"notifyBeforeMs":0,"postponeMs":120000,"allowEarlyEnd":true,"slots":[]},
  "long":{"enabled":true,"mode":"countdown","intervalMs":3600000,"durationMs":60000,"notifyBeforeMs":0,"postponeMs":120000,"allowEarlyEnd":true,"slots":[]},
  "sound":{"enabled":true,"volume":0.6,"mode":"single","singleId":"sm-builtin-short_start",
           "music":[{"id":"sm-builtin-short_start","file":"short_start.wav","name":"短休息开始","enabled":true}]},
  "customBreaks":[
    {"id":"cb-a","name":"喝水提醒","config":{"enabled":true,"mode":"countdown","intervalMs":300000,"durationMs":10000,"notifyBeforeMs":0,"postponeMs":120000,"allowEarlyEnd":true,"slots":[]}},
    {"id":"cb-b","name":"站起来走走","config":{"enabled":true,"mode":"countdown","intervalMs":300000,"durationMs":10000,"notifyBeforeMs":0,"postponeMs":120000,"allowEarlyEnd":true,"slots":[]}},
    {"id":"cb-c","name":"看看远处","config":{"enabled":true,"mode":"countdown","intervalMs":300000,"durationMs":10000,"notifyBeforeMs":0,"postponeMs":120000,"allowEarlyEnd":true,"slots":[]}}
  ]
}`

// appTestHost 把调度器的几个关键回调**真接到 App** 上（与 Wails 宿主同一形状）。
//
// 直接用 testHost 的话，EndBreak 里的 CloseBreakWindows 只会把一个计数器 +1，
// App 那边的会话清空、break-end 记账、副条收尾全都不发生 —— 测出来的就是假现场。
type appTestHost struct {
	*testHost
	a *App
}

func (h *appTestHost) ShowBreakSession(kind string, preview bool, forceKey string) string {
	return h.a.ShowBreakSession(kind, preview, forceKey)
}
func (h *appTestHost) CloseBreakWindows() {
	h.a.closeBreakWindowsOpt(false, true, false, false)
}
func (h *appTestHost) CompleteBreakWindows() {
	h.a.closeBreakWindowsOpt(true, true, true, true)
}

// OnBreakExtra 也必须真接到 App：只让 testHost 记一笔的话，调度器“并入成功”了，
// App 那边的会话里却没有副条 —— 测出来的就是假现场（撞车用例踩过这个坑）。
func (h *appTestHost) OnBreakExtra(kind string, at int64) bool {
	if !h.a.OnBreakExtra(kind, at) {
		return false
	}
	h.testHost.extras = append(h.testHost.extras, kind)
	return true
}

// breakQueueScene 造“短休息真的开着 + 右上角挂着若干张卡”的现场。
//
// kinds 的顺序故意与到点先后**相反**（第一个参数最晚到点）：队列必须按 At 排，
// 不能按并入顺序排 —— 否则这条用例在“已经有序”的情况下永远测不出排序。
func breakQueueScene(t *testing.T, kinds ...string) *App {
	t.Helper()
	a := newExtraApp(t, relayCfg)
	th := &testHost{cfg: a.cfg, breakOK: true}
	a.sched = scheduler.New(&appTestHost{testHost: th, a: a})
	a.sched.ResetCycle()
	a.sched.BreakNow()
	if got := a.sched.StateCopy().Phase; got != "break" {
		t.Fatalf("前置条件不成立：调度器没进休息（%q）", got)
	}

	base := int64(1_700_000_000_000)
	a.mu.Lock()
	a.sess.Extras = nil
	for i, k := range kinds {
		a.sess.Extras = append(a.sess.Extras, model.BreakExtra{
			Kind: k, Name: k, Text: k,
			At: base + int64(len(kinds)-i)*1000,
		})
	}
	a.mu.Unlock()
	return a
}

// 取队列：按到点时刻升序，取完会话里的副条要清空（否则收窗路径会按“丢弃”再处理一遍）
func TestTakeBreakQueueOrdersAndClears(t *testing.T) {
	a := breakQueueScene(t, "cb-b", "cb-a")

	q := a.takeBreakQueue()

	if len(q) != 2 || q[0].Kind != "cb-a" || q[1].Kind != "cb-b" {
		t.Fatalf("队列应按到点时刻升序（cb-a 早于 cb-b），实际 %v", q)
	}
	a.mu.Lock()
	left := len(a.sess.Extras)
	a.mu.Unlock()
	if left != 0 {
		t.Fatalf("取走之后会话里不该还留着副条，实际还剩 %d 条", left)
	}
}

// 只有外卖时不接管：外卖本来就该交回它自己的独立窗口，不能当成“卡”接力
func TestTakeBreakQueueIgnoresTakeoutOnly(t *testing.T) {
	a := breakQueueScene(t)
	a.mu.Lock()
	a.sess.Extras = []model.BreakExtra{{Kind: "takeout", Name: "外卖提醒", At: 1}}
	a.mu.Unlock()

	if q := a.takeBreakQueue(); q != nil {
		t.Fatalf("只有外卖时不该接管队列，实际 %v", q)
	}
	a.mu.Lock()
	left := len(a.sess.Extras)
	a.mu.Unlock()
	if left != 1 {
		t.Fatalf("会话里的外卖副条不该被摘走，实际 %d 条", left)
	}
}

// 提前结束：队头（最早到点的那张）接替成一扇新窗，其余挂在它右上角
func TestEndBreakRelaysOldestExtraAsWindow(t *testing.T) {
	a := breakQueueScene(t, "cb-b", "cb-c", "cb-a") // 故意乱序，cb-a 最早到点

	a.schedEndBreak()

	cur := a.BreakCurrent("")
	if cur == nil {
		t.Fatal("提前结束后应当有一扇新窗接替（右上角还有卡）")
	}
	if cur.Type != "cb-a" {
		t.Fatalf("接替的应当是最早到点的那张（cb-a），实际 %q", cur.Type)
	}
	if len(cur.Extras) != 2 {
		t.Fatalf("其余两张应当继续挂在右上角，实际 %d 条：%+v", len(cur.Extras), cur.Extras)
	}
	kinds := map[string]bool{}
	for _, e := range cur.Extras {
		kinds[e.Kind] = true
	}
	if !kinds["cb-b"] || !kinds["cb-c"] {
		t.Fatalf("右上角应是 cb-b / cb-c，实际 %+v", cur.Extras)
	}
	// 调度器也得真的进到 break 相位 —— 光有会话、相位还停在 idle 的话，
	// 侧栏会写“下次短休息”、托盘也会当成没在休息
	st := a.sched.StateCopy()
	if st.Phase != "break" || st.BreakType != "cb-a" {
		t.Fatalf("调度器应停在 cb-a 的休息相位，实际 phase=%q breakType=%q", st.Phase, st.BreakType)
	}
	if st.BreakEndsAt <= 0 {
		t.Fatalf("新窗应当有自己的结束时刻，实际 %d", st.BreakEndsAt)
	}
}

// 接力成窗的那条**不能**同时被补记：它走 OnBreakStart / OnBreakEnd 那一对
func TestRelayedExtraNotCountedTwice(t *testing.T) {
	a := breakQueueScene(t, "cb-a")
	a.schedEndBreak()

	if got := a.statsStore().Today().CustomCount; got != 0 {
		t.Fatalf("接力成窗的提醒此刻还没结束，不该已经记过（实际 customCount=%d）", got)
	}
	// 结束这次接力窗：这时才记一次（customCount 与按 id 的次数各 +1）
	a.schedEndBreak()
	today := a.statsStore().Today()
	if today.CustomCount != 1 {
		t.Fatalf("接力窗结束后应记 1 次自定义提醒，实际 %d（customCounts=%+v）",
			today.CustomCount, today.CustomCounts)
	}
	if today.CustomCounts["cb-a"] != 1 {
		t.Fatalf("按 id 的次数应为 cb-a=1，实际 %+v", today.CustomCounts)
	}
	// 第一次（短休息）记提前结束；自定义提醒不分完成 / 提前
	if today.EndEarlyCount != 1 {
		t.Fatalf("短休息应记 1 次提前结束，实际 %d", today.EndEarlyCount)
	}
}

// 自然结束（没有接力）时，挂在右上角的自定义提醒要按“显示过”补记一条
func TestMergedCustomCountedOnNaturalEnd(t *testing.T) {
	a := breakQueueScene(t, "cb-b", "cb-c")

	info := &model.BreakInfo{
		Type: "short", Name: "短休息",
		Extras: []model.BreakExtra{
			{Kind: "cb-b", Name: "站起来走走", At: 2},
			{Kind: "cb-c", Name: "看看远处", At: 3},
		},
	}
	a.finishBreakExtras(info, false)

	today := a.statsStore().Today()
	if today.CustomCount != 2 {
		t.Fatalf("并入的两条都该各记 1 次，实际 %d", today.CustomCount)
	}
	if today.CustomCounts["cb-b"] != 1 || today.CustomCounts["cb-c"] != 1 {
		t.Fatalf("按 id 的次数不对：%+v", today.CustomCounts)
	}
}

// 长休息抢占（carry=true）：副条只是搬过去，不能在这里记账（它们还没结束）
func TestCarriedExtrasNotCountedYet(t *testing.T) {
	a := breakQueueScene(t, "cb-b")
	info := &model.BreakInfo{
		Type: "short", Extras: []model.BreakExtra{{Kind: "cb-b", Name: "站起来走走", At: 2}},
	}
	a.finishBreakExtras(info, true)

	if got := a.statsStore().Today().CustomCount; got != 0 {
		t.Fatalf("搬去下一次休息的副条不该现在记账，实际 %d", got)
	}
	a.mu.Lock()
	n := len(a.carryExtras)
	a.mu.Unlock()
	if n != 1 {
		t.Fatalf("副条应被搬进 carryExtras，实际 %d 条", n)
	}
}

// 收尾清字段：phase 回 idle 之后，不能再留着“还剩 N 秒”这种旧值
func TestFinishBreakClearsTimeFields(t *testing.T) {
	a := breakQueueScene(t)

	a.sched.EndBreak()

	st := a.sched.StateCopy()
	if st.Phase != "idle" {
		t.Fatalf("结束之后应当回到 idle，实际 %q", st.Phase)
	}
	if st.BreakEndsAt != 0 || st.BreakStartedAt != 0 || st.BreakType != "" {
		t.Fatalf("休息的时间字段应当清空，实际 endedAt=%d startedAt=%d type=%q",
			st.BreakEndsAt, st.BreakStartedAt, st.BreakType)
	}
}

// 队头是外卖（只有它比所有自定义卡都早到点）：外卖交回自己的独立窗口，
// 剩下没窗口承载的卡按“显示过”补记 —— 不能因为接不上就丢一条。
func TestRelayTakeoutHeadHandsBackWindow(t *testing.T) {
	a := breakQueueScene(t)
	slot := model.TimeSlot{ID: "s1", Hour: 12, Minute: 30, Repeat: "daily", Enabled: true}
	a.showTakeoutWindowsAt(slot, false, 0, 0)

	a.mu.Lock()
	src := a.takeoutCurrent
	a.sess.Extras = nil
	a.mu.Unlock()
	if src == nil {
		t.Fatal("前置条件不成立：外卖会话没建起来")
	}
	info := *src

	a.resumeFromQueue([]model.BreakExtra{
		{Kind: "takeout", Name: "外卖提醒", Takeout: &info, At: 1},
		{Kind: "cb-b", Name: "站起来走走", At: 2},
	})

	a.mu.Lock()
	after := a.takeoutCurrent
	a.mu.Unlock()
	if after == nil || after.ID == info.ID {
		t.Fatalf("外卖应当被重新交回独立窗口（新会话），实际 %+v", after)
	}
	if got := a.statsStore().Today().CustomCount; got != 1 {
		t.Fatalf("没有窗口承载的 cb-b 应补记 1 次，实际 %d", got)
	}
}

// 外卖卡排在自定义卡之后：跟着 carry 挂到新窗右上角，等新窗结束再交回 ——
// 现在不能提前弹独立窗（两扇 95% 的窗会互相盖住）。
func TestRelayTakeoutRidesAlongToNextWindow(t *testing.T) {
	a := breakQueueScene(t)
	slot := model.TimeSlot{ID: "s1", Hour: 12, Minute: 30, Repeat: "daily", Enabled: true}
	a.showTakeoutWindowsAt(slot, false, 0, 0)

	a.mu.Lock()
	src := a.takeoutCurrent
	a.mu.Unlock()
	if src == nil {
		t.Fatal("前置条件不成立：外卖会话没建起来")
	}
	info := *src
	a.mu.Lock()
	a.sess.Extras = []model.BreakExtra{
		{Kind: "cb-a", Name: "喝水提醒", At: 1},
		{Kind: "takeout", Name: "外卖提醒", Takeout: &info, At: 2},
	}
	a.mu.Unlock()

	a.schedEndBreak()

	cur := a.BreakCurrent("")
	if cur == nil || cur.Type != "cb-a" {
		t.Fatalf("应当由 cb-a 接替成一扇窗，实际 %+v", cur)
	}
	if len(cur.Extras) != 1 || cur.Extras[0].Kind != "takeout" {
		t.Fatalf("外卖卡应挂在新窗右上角，实际 %+v", cur.Extras)
	}
}

// 接力的那条提醒刚被删 / 停用：开不出窗口时，卡要按“显示过”补记，不能静默消失
func TestRelayUnavailableKindStillCounted(t *testing.T) {
	a := breakQueueScene(t)
	a.mu.Lock()
	a.sess.Extras = []model.BreakExtra{{Kind: "cb-gone", Name: "已删提醒", At: 1}}
	a.mu.Unlock()

	a.schedEndBreak()

	if cur := a.BreakCurrent(""); cur != nil {
		t.Fatalf("开不出窗口时会话应当收尾，实际还挂着 %+v", cur)
	}
	if got := a.sched.StateCopy().Phase; got != "idle" {
		t.Fatalf("应当回到 idle，实际 %q", got)
	}
	if got := a.statsStore().Today().CustomCounts["cb-gone"]; got != 1 {
		t.Fatalf("接不上的卡应补记 1 次，实际 %d", got)
	}
}

// ---------- 换窗时的提示音（2026-11-16 用户口径：换窗完全不出声） ----------
//
// 换窗本来有两声：旧窗收尾的“结束音”+ 新窗开场那一瞬的“开始音”。两声响只隔几百毫秒，
// 单曲循环下就是同一条音频连响两遍，听感是“卡了一下”。用户最终口径是**整段静音**：
// 换窗是用户自己点出来的，不需要声音确认。接不上新窗（那条提醒刚被删 / 停用）时不是换窗、
// 休息确实结束了，结束音要补回来，不能一声不响。
//
// 无头构建里“发声”落在 playSoundFile 的日志上，这里就数日志里的“播放”条数。

// injectRelaySound 把内置音目录指到仓库里的 assets/sounds：relayCfg 的 singleId 指向
// short_start.wav，文件不在的话会走“静音”分支，这些用例就失去意义了。
func injectRelaySound(t *testing.T) {
	t.Helper()
	injectBuiltinSoundDir(t, filepath.Join(repoRoot(t), "assets", "sounds"))
}

// countSoundPlays 跑一次动作并数它放了几声（顺带把日志带回去断言走了哪条路径）
func countSoundPlays(t *testing.T, fn func()) (int, string) {
	t.Helper()
	buf := &lockedBuffer{}
	old := log.Writer()
	log.SetOutput(buf)
	defer log.SetOutput(old)
	fn()
	out := buf.String()
	if strings.Contains(out, "[sound] 静音") {
		t.Fatalf("测试配置没解析出可播的提示音（走到静音分支），这条用例失去意义：\n%s", out)
	}
	return strings.Count(out, "[sound] 播放"), out
}

// 换窗整段静音：旧窗的结束音、新窗的开始音都不响
func TestRelaySwitchIsSilent(t *testing.T) {
	injectRelaySound(t)
	a := breakQueueScene(t, "cb-a")

	n, out := countSoundPlays(t, a.schedEndBreak)

	cur := a.BreakCurrent("")
	if cur == nil || cur.Type != "cb-a" {
		t.Fatalf("前置条件不成立：应当由 cb-a 接替成一扇新窗，实际 %+v", cur)
	}
	if n != 0 {
		t.Fatalf("换窗应当完全不出声，实际响了 %d 声：\n%s", n, out)
	}
	if !strings.Contains(out, "[sound] 换窗") {
		t.Fatalf("应当走换窗静音那条路径，实际日志：\n%s", out)
	}
}

// 静音不外溢：接力窗自己结束（右上角已经没卡了）时，结束音照常响一声
func TestRelaySilenceDoesNotLeak(t *testing.T) {
	injectRelaySound(t)
	a := breakQueueScene(t, "cb-a")

	a.schedEndBreak() // 换窗：静音
	if cur := a.BreakCurrent(""); cur == nil || cur.Type != "cb-a" {
		t.Fatalf("前置条件不成立：接力窗没开出来，实际 %+v", cur)
	}

	n, out := countSoundPlays(t, a.schedEndBreak) // 接力窗正常结束

	if n != 1 {
		t.Fatalf("接力窗结束时该照常响一声结束音（静音标记必须只作用于换窗那一次），实际 %d 声：\n%s", n, out)
	}
	if strings.Contains(out, "[sound] 换窗") {
		t.Fatalf("这一步不是换窗，不该再走静音路径，实际日志：\n%s", out)
	}
}

// 没有卡可接（结束就是结束）：结束音照常响一声，不能让音
func TestEndBreakWithoutRelayKeepsEndSound(t *testing.T) {
	injectRelaySound(t)
	a := breakQueueScene(t)

	n, out := countSoundPlays(t, a.schedEndBreak)

	if n != 1 {
		t.Fatalf("结束一次休息应当响一声结束音，实际 %d 声：\n%s", n, out)
	}
	if strings.Contains(out, "[sound] 换窗") {
		t.Fatalf("没有新窗接上来时不该让出结束音，实际日志：\n%s", out)
	}
}

// 接力没接上（提醒刚被删 / 停用）：结束音要补回来，否则这次提前结束一声不响
func TestRelayFallbackRestoresEndSound(t *testing.T) {
	injectRelaySound(t)
	a := breakQueueScene(t)
	a.mu.Lock()
	a.sess.Extras = []model.BreakExtra{{Kind: "cb-gone", Name: "已删提醒", At: 1}}
	a.mu.Unlock()

	n, out := countSoundPlays(t, a.schedEndBreak)

	if n != 1 {
		t.Fatalf("接不上新窗时要把结束音补回来，实际 %d 声：\n%s", n, out)
	}
}

// 队头是外卖：外卖走它自己的独立窗口（不经过 ShowBreakSession，没有开始音），
// 结束音不能被白让 —— 否则换过去完全没有声音
func TestRelayTakeoutHeadKeepsEndSound(t *testing.T) {
	injectRelaySound(t)
	a := breakQueueScene(t)
	slot := model.TimeSlot{ID: "s1", Hour: 12, Minute: 30, Repeat: "daily", Enabled: true}
	a.showTakeoutWindowsAt(slot, false, 0, 0)
	a.mu.Lock()
	src := a.takeoutCurrent
	a.mu.Unlock()
	if src == nil {
		t.Fatal("前置条件不成立：外卖会话没建起来")
	}
	info := *src
	a.mu.Lock()
	a.sess.Extras = []model.BreakExtra{{Kind: "takeout", Name: "外卖提醒", Takeout: &info, At: 1}}
	a.mu.Unlock()

	n, out := countSoundPlays(t, a.schedEndBreak)

	if n != 1 {
		t.Fatalf("队头是外卖时仍要响一声结束音，实际 %d 声：\n%s", n, out)
	}
	if strings.Contains(out, "[sound] 换窗") {
		t.Fatalf("外卖接管不该走“让出结束音”，实际日志：\n%s", out)
	}
}
