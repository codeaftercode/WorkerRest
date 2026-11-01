package main

import (
	"fmt"
	"math/rand"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"workerrest/internal/idle"
	"workerrest/internal/model"
	"workerrest/internal/stats"
)

// 外卖提醒的纯逻辑回归（脱离窗口与 Wails）。
//
// 覆盖：会话字段组装、固定时刻触发、推迟记账、推迟过期作废、暂停跳过、动作记账。
// 窗口创建 / 多屏 / 键鼠轮询依赖原生窗口，由无头冒烟 + GUI 探针覆盖。

// newTakeoutApp 造一个能跑会话构建与触发逻辑的最小 App（不碰窗口与 Wails）。
// rolesRoot 指向空目录 → buildSupervisorSession 返回 nil（无督休专员），正好测「无督休专员」路径。
func newTakeoutApp(t *testing.T, cfgJSON string) *App {
	t.Helper()
	t.Setenv("WR_STATS_PATH", filepath.Join(t.TempDir(), "stats.json"))
	return &App{
		cfg:             testSettings(t, cfgJSON),
		rolesRoot:       t.TempDir(),
		rng:             rand.New(rand.NewSource(1)),
		cursor:          make(map[string]int),
		takeoutFired:    map[string]bool{},
		takeoutPreFired: map[string]bool{},
		idle:            idle.NewIdleState(),
	}
}

// takeoutCfg 一条启用的外卖配置：午餐 12:30、可推迟 15 分钟、提前 0 分钟
const takeoutCfg = `{
  "takeout": {
    "enabled": true,
    "leadSec": 0,
    "postponeMin": 15,
    "slots": [{"id":"s1","hour":12,"minute":30,"repeat":"daily","days":[],"enabled":true}]
  },
  "reminderMessages": {
    "takeout": {"order":"random","fixedId":"","items":[{"id":"m1","text":"该点外卖啦","enabled":true}]}
  }
}`

func TestBuildTakeoutInfo(t *testing.T) {
	a := newTakeoutApp(t, takeoutCfg)
	slot := model.TimeSlot{ID: "s1", Hour: 12, Minute: 30, Repeat: "daily", Enabled: true}
	info := a.buildTakeoutInfo(slot, false, 0, 0)
	if info == nil {
		t.Fatal("会话不应为空")
	}
	if info.Meal != "午餐" {
		t.Fatalf("餐段应=午餐，得到 %q", info.Meal)
	}
	if info.Hour != 12 || info.Minute != 30 {
		t.Fatalf("时刻应=12:30，得到 %d:%d", info.Hour, info.Minute)
	}
	if info.LeadSec != 0 || info.PostponeMin != 15 {
		t.Fatalf("lead/postpone 应=0/15，得到 %d/%d", info.LeadSec, info.PostponeMin)
	}
	if info.PostponedCount != 0 {
		t.Fatalf("首次弹 postponedCount 应=0，得到 %d", info.PostponedCount)
	}
	if info.Preview {
		t.Fatal("真实弹窗 preview 应为 false")
	}
	if info.Message == "" {
		t.Fatal("提醒内容不应为空（有一条启用项）")
	}
	// 外卖提醒窗口始终展示优惠券：redPacketData 不应为 nil
	if info.RedPacketData == nil {
		t.Fatal("外卖提醒窗口应始终展示外卖优惠券，redPacketData 不应为 nil")
	}
	if info.Supervisor != nil {
		t.Fatal("无督休专员目录时 supervisor 应为 nil")
	}
	// at 应是当天 12:30 的 Unix 毫秒
	now := time.Now()
	wantAt := time.Date(now.Year(), now.Month(), now.Day(), 12, 30, 0, 0, now.Location()).UnixMilli()
	if info.At != wantAt {
		t.Fatalf("at 应=当天 12:30（%d），得到 %d", wantAt, info.At)
	}
}

// 「可推迟」为 0：会话快照 PostponeMin=0（渲染层据此隐藏「推迟」按钮）
func TestBuildTakeoutInfoPostponeZero(t *testing.T) {
	cfg := `{"takeout":{"enabled":true,"leadSec":0,"postponeMin":0,"slots":[]},"installId":"dev-1234"}`
	a := newTakeoutApp(t, cfg)
	info := a.buildTakeoutInfo(model.TimeSlot{ID: "s1", Hour: 12, Minute: 30, Enabled: true}, false, 0, 0)
	if info == nil {
		t.Fatal("会话不应为空")
	}
	if info.PostponeMin != 0 {
		t.Fatalf("postponeMin=0 时应透传 0，得到 %d", info.PostponeMin)
	}
	// 归一化侧同样不许把 0 夹成 1（保存路径）
	norm := normalizeTakeout(map[string]any{"takeout": map[string]any{"postponeMin": float64(0)}})
	if norm["postponeMin"] != float64(0) {
		t.Fatalf("normalizeTakeout 应保留 postponeMin=0，得到 %v", norm["postponeMin"])
	}
}

// TakeoutPostponeMin 的下放开到 0：0 = 不显示「推迟」按钮；负值夹到 0、越界夹到 120
func TestTakeoutPostponeMinClamp(t *testing.T) {
	cases := []struct {
		js   string
		want int
	}{
		{`{"takeout":{"postponeMin":0}}`, 0},
		{`{"takeout":{"postponeMin":-5}}`, 0},
		{`{"takeout":{"postponeMin":15}}`, 15},
		{`{"takeout":{"postponeMin":999}}`, 120},
		{`{"takeout":{}}`, 10}, // 缺省兜底
	}
	for _, c := range cases {
		s := testSettings(t, c.js)
		if got := s.TakeoutPostponeMin(); got != c.want {
			t.Errorf("TakeoutPostponeMin(%s) = %d，期望 %d", c.js, got, c.want)
		}
	}
}

// 外卖提醒窗口始终展示优惠券：redPacketData 必有且地址指向本机专属领取地址
func TestBuildTakeoutInfoRedPacketAlwaysShown(t *testing.T) {
	cfg := `{"takeout":{"enabled":true,"leadSec":0,"postponeMin":15,"slots":[]},"installId":"dev-1234"}`
	a := newTakeoutApp(t, cfg)
	info := a.buildTakeoutInfo(model.TimeSlot{ID: "s1", Hour: 18, Minute: 0, Enabled: true}, false, 0, 0)
	if info.RedPacketData == nil {
		t.Fatal("外卖提醒窗口应始终展示外卖优惠券（redPacketData 不应为 nil）")
	}
	// 地址 = 固定 base + id=配置固定键 + t=毫秒时间戳（每次弹窗动态生成）
	wantPrefix := "https://www.goodgoodlive.com/cps/index.html?id=dev-1234&t="
	url := info.RedPacketData.URL
	if !strings.HasPrefix(url, wantPrefix) {
		t.Fatalf("红包地址应含固定键与时间戳参数，得到 %q", url)
	}
	tsStr := strings.TrimPrefix(url, wantPrefix)
	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil || ts <= 0 {
		t.Fatalf("t 应为毫秒时间戳，得到 %q", tsStr)
	}
	if ts < time.Now().Add(-time.Minute).UnixMilli() || ts > time.Now().Add(time.Minute).UnixMilli() {
		t.Fatalf("t 应接近当前时间，得到 %d", ts)
	}
}

func TestBuildTakeoutInfoSupervisorClearsCommand(t *testing.T) {
	// 有督休专员时 supervisor 的 command 必须清空（外卖窗口第二段不换台词）
	// 这里只验证「nil 时不炸」——有督休专员的路径由 GUI/对拍覆盖（需要真实立绘 data URL）
	a := newTakeoutApp(t, takeoutCfg)
	info := a.buildTakeoutInfo(model.TimeSlot{ID: "s1", Hour: 12, Minute: 30, Enabled: true}, false, 0, 0)
	if info.Supervisor != nil {
		t.Fatalf("空督休专员目录应无 supervisor，得到 %+v", info.Supervisor)
	}
}

// ---------- 触发 / 推迟 ----------

// 固定时刻线：把 slot 设成「现在这一刻」就应立即触发
func TestCheckTakeoutFiresOnDueSlot(t *testing.T) {
	now := time.Now()
	cfg := `{"takeout":{"enabled":true,"leadSec":0,"postponeMin":15,"slots":[` +
		`{"id":"s1","hour":` + itoa(now.Hour()) + `,"minute":` + itoa(now.Minute()) +
		`,"repeat":"daily","days":[],"enabled":true}]}}`
	a := newTakeoutApp(t, cfg)
	a.sched = nil // 未启动调度器：checkTakeout 里 sched.isPaused 跳过
	a.checkTakeoutReminders()
	if a.takeoutCurrent == nil {
		t.Fatal("到点的条目应触发一次外卖会话")
	}
	if a.takeoutCurrent.Meal == "" {
		t.Fatal("会话应有餐段文案")
	}
	// 同一分钟内再检查：不应重复弹（去重键生效）
	first := a.takeoutCurrent
	a.mu.Lock()
	a.takeoutCurrent = nil
	a.mu.Unlock()
	a.checkTakeoutReminders()
	if a.takeoutCurrent != nil {
		t.Fatalf("同一分钟内的第二次 tick 不应重复触发，得到 %+v", a.takeoutCurrent)
	}
	_ = first
}

// 推迟线：记一笔待推迟，到点重新弹
func TestCheckTakeoutPostponeLine(t *testing.T) {
	a := newTakeoutApp(t, takeoutCfg)
	a.sched = nil
	slot := model.TimeSlot{ID: "s1", Hour: 12, Minute: 30, Repeat: "daily", Enabled: true}
	// 直接注入一笔「已到点」的推迟计划
	a.takeoutPostponed = &model.TakeoutPostponed{Slot: slot, DueAt: nowMs() - 1, Count: 2}
	a.checkTakeoutReminders()
	if a.takeoutCurrent == nil {
		t.Fatal("到点的推迟计划应重新弹窗")
	}
	if a.takeoutCurrent.PostponedCount != 2 {
		t.Fatalf("推迟次数应=2，得到 %d", a.takeoutCurrent.PostponedCount)
	}
	if a.takeoutPostponed != nil {
		t.Fatal("推迟计划触发后应清空")
	}
}

// 推迟计划过期：超过宽限作废，不弹
func TestCheckTakeoutPostponeExpired(t *testing.T) {
	a := newTakeoutApp(t, takeoutCfg)
	a.sched = nil
	slot := model.TimeSlot{ID: "s1", Hour: 12, Minute: 30, Repeat: "daily", Enabled: true}
	a.takeoutPostponed = &model.TakeoutPostponed{Slot: slot, DueAt: nowMs() - takeoutPostponeGraceMs - 1000, Count: 1}
	a.checkTakeoutReminders()
	if a.takeoutCurrent != nil {
		t.Fatal("过期的推迟计划不应弹窗")
	}
	if a.takeoutPostponed != nil {
		t.Fatal("过期的推迟计划应被作废清空")
	}
}

// 外卖总开关关闭：清掉推迟计划，不触发
func TestCheckTakeoutDisabled(t *testing.T) {
	a := newTakeoutApp(t, `{"takeout":{"enabled":false,"leadSec":0,"postponeMin":15,"slots":[]}}`)
	a.sched = nil
	a.takeoutPostponed = &model.TakeoutPostponed{Slot: model.TimeSlot{}, DueAt: nowMs() - 1, Count: 1}
	a.checkTakeoutReminders()
	if a.takeoutPostponed != nil {
		t.Fatal("外卖关闭时应清掉推迟计划")
	}
}

// ---------- 动作记账 ----------

func TestTakeoutActionOrderedAndSkip(t *testing.T) {
	a := newTakeoutApp(t, takeoutCfg)
	a.sched = nil
	slot := model.TimeSlot{ID: "s1", Hour: 12, Minute: 30, Repeat: "daily", Enabled: true}
	a.takeoutCurrent = a.buildTakeoutInfo(slot, false, 0, 0)
	if err := a.TakeoutAction("takeout", "ordered"); err != nil {
		t.Fatalf("ordered 动作报错: %v", err)
	}
	if a.takeoutCurrent != nil {
		t.Fatal("ordered 后会话应清空")
	}
}

func TestTakeoutActionPostpone(t *testing.T) {
	a := newTakeoutApp(t, takeoutCfg)
	a.sched = nil
	slot := model.TimeSlot{ID: "s1", Hour: 12, Minute: 30, Repeat: "daily", Enabled: true}
	a.takeoutCurrent = a.buildTakeoutInfo(slot, false, 0, 0)
	if err := a.TakeoutAction("takeout", "postpone"); err != nil {
		t.Fatalf("postpone 动作报错: %v", err)
	}
	if a.takeoutPostponed == nil {
		t.Fatal("postpone 应记一笔待推迟计划")
	}
	if a.takeoutPostponed.Count != 1 {
		t.Fatalf("第一次推迟 count 应=1，得到 %d", a.takeoutPostponed.Count)
	}
	if a.takeoutPostponed.Slot.ID != "s1" {
		t.Fatalf("推迟计划应保留原条目，得到 %q", a.takeoutPostponed.Slot.ID)
	}
	// dueAt 应是 now + 15 分钟
	wantDue := nowMs() + 15*60_000
	if diff := a.takeoutPostponed.DueAt - wantDue; diff < -2000 || diff > 2000 {
		t.Fatalf("推迟到点应≈现在+15 分钟，得到 %d（差 %d）", a.takeoutPostponed.DueAt, diff)
	}
	if a.takeoutCurrent != nil {
		t.Fatal("postpone 后会话应清空（等 tick 重弹）")
	}
}

// 预览态动作：不记账、不推迟
func TestTakeoutActionPreview(t *testing.T) {
	a := newTakeoutApp(t, takeoutCfg)
	a.sched = nil
	slot := model.TimeSlot{ID: "s1", Hour: 12, Minute: 30, Repeat: "daily", Enabled: true}
	a.takeoutCurrent = a.buildTakeoutInfo(slot, true, 0, 0)
	if err := a.TakeoutAction("takeout", "postpone"); err != nil {
		t.Fatalf("预览 postpone 报错: %v", err)
	}
	if a.takeoutPostponed != nil {
		t.Fatal("预览态的推迟不应记真实推迟计划")
	}
}

// 外卖推迟后、提前通知（leadSec>0）仍应在推迟后的时刻前弹提前通知窗：
// checkTakeoutReminders 应在 DueAt 前 leadMs 处补弹一次预通知，倒计时指向推迟后的到点时刻。
func TestCheckTakeoutPostponePreNotify(t *testing.T) {
	const cfg = `{"takeout":{"enabled":true,"leadSec":60,"postponeMin":15,"slots":` +
		`[{"id":"s1","hour":12,"minute":30,"repeat":"daily","days":[],"enabled":true}]}}`
	a := newTakeoutApp(t, cfg)
	a.sched = nil
	slot := model.TimeSlot{ID: "s1", Hour: 12, Minute: 30, Repeat: "daily", Enabled: true}
	// 推迟到 30 秒后（仍在 60 秒提前窗内）：此刻应弹「推迟后的提前通知」
	due := nowMs() + 30*1000
	a.takeoutPostponed = &model.TakeoutPostponed{Slot: slot, DueAt: due, Count: 1}
	a.checkTakeoutReminders()
	if a.takeoutPostponed == nil || !a.takeoutPostponed.PreFired {
		t.Fatal("推迟后的提前通知应已标记 PreFired")
	}
	if a.preInfo.Owner != "takeout" {
		t.Fatalf("应弹外卖预通知，得到 owner=%q", a.preInfo.Owner)
	}
	if a.preTargetAt != due {
		t.Fatalf("预通知倒计时应指向推迟后的到点时刻 %d，得到 %d", due, a.preTargetAt)
	}
	if a.takeoutCurrent != nil {
		t.Fatal("推迟后的提前通知阶段不应同时弹真窗")
	}
	// 同一次推迟线：再跑一次 tick 不应重复弹（PreFired 去重）
	a.checkTakeoutReminders()
	if a.preTargetAt != due {
		t.Fatalf("PreFired 去重后倒计时目标不应变，得到 %d", a.preTargetAt)
	}
}

// 外卖推迟后的提前通知已弹过（PreFired），到点（DueAt）应弹真窗并清掉推迟线；
// 且用户若「立即提醒」/「跳过」也应清掉推迟线，避免到点重复弹。
func TestCheckTakeoutPostponePreNotifyThenFire(t *testing.T) {
	const cfg = `{"takeout":{"enabled":true,"leadSec":60,"postponeMin":15,"slots":` +
		`[{"id":"s1","hour":12,"minute":30,"repeat":"daily","days":[],"enabled":true}]}}`
	a := newTakeoutApp(t, cfg)
	a.sched = nil
	slot := model.TimeSlot{ID: "s1", Hour: 12, Minute: 30, Repeat: "daily", Enabled: true}
	// ①b 已弹过提前通知（PreFired=true），现在到点
	a.takeoutPostponed = &model.TakeoutPostponed{Slot: slot, DueAt: nowMs() - 1, Count: 1, PreFired: true}
	a.checkTakeoutReminders()
	if a.takeoutCurrent == nil {
		t.Fatal("推迟线到点应弹真窗")
	}
	if a.takeoutPostponed != nil {
		t.Fatal("推迟线触发后应清空，避免重复弹")
	}
}

// 外卖推迟后、提前通知为 0（leadSec=0）时不应弹提前通知窗，只到点弹真窗。
func TestCheckTakeoutPostponeNoLead(t *testing.T) {
	const cfg = `{"takeout":{"enabled":true,"leadSec":0,"postponeMin":15,"slots":` +
		`[{"id":"s1","hour":12,"minute":30,"repeat":"daily","days":[],"enabled":true}]}}`
	a := newTakeoutApp(t, cfg)
	a.sched = nil
	slot := model.TimeSlot{ID: "s1", Hour: 12, Minute: 30, Repeat: "daily", Enabled: true}
	a.takeoutPostponed = &model.TakeoutPostponed{Slot: slot, DueAt: nowMs() + 30*1000, Count: 1}
	a.checkTakeoutReminders()
	if a.takeoutPostponed != nil && a.takeoutPostponed.PreFired {
		t.Fatal("leadSec=0 时不应弹推迟后的提前通知")
	}
	if a.preInfo.Owner == "takeout" {
		t.Fatal("leadSec=0 时不应出现外卖预通知窗口")
	}
}

// 外卖统计记账走 RecordTakeoutAction：验证 stats 里确实 +1（预览不计）
func TestTakeoutActionRecordsStats(t *testing.T) {
	a := newTakeoutApp(t, takeoutCfg)
	a.sched = nil
	st := stats.NewStatsStore(func() string { return filepath.Join(t.TempDir(), "stats.json") })
	a.stats = st
	slot := model.TimeSlot{ID: "s1", Hour: 12, Minute: 30, Repeat: "daily", Enabled: true}
	a.takeoutCurrent = a.buildTakeoutInfo(slot, false, 0, 0)
	_ = a.TakeoutAction("takeout", "ordered")
	d := st.Today()
	if d.TakeoutOrdered != 1 {
		t.Fatalf("ordered 应使 takeoutOrdered=1，得到 %d", d.TakeoutOrdered)
	}
}

// ---------- 抬升簿记 ----------

func TestRaiseStateBookkeeping(t *testing.T) {
	var r raiseState
	if r.lastRaised() != "" {
		t.Fatalf("初始 last 应为空，得到 %q", r.lastRaised())
	}
	r.noteRaised(raiseKindBreak)
	r.noteRaised(raiseKindTakeout)
	if r.lastRaised() != raiseKindTakeout {
		t.Fatalf("最后一次抬升应是外卖，得到 %q", r.lastRaised())
	}
	seq := r.raiseSequence()
	if len(seq) != 2 || seq[0] != raiseKindBreak || seq[1] != raiseKindTakeout {
		t.Fatalf("抬升顺序应=[break takeout]，得到 %v", seq)
	}
	r.setBreaksActive(true)
	if !r.anyBreakActive() {
		t.Fatal("setBreaksActive(true) 后 anyBreakActive 应为 true")
	}
	r.setBreaksActive(false)
	if r.anyBreakActive() {
		t.Fatal("setBreaksActive(false) 后 anyBreakActive 应为 false")
	}
}

// ---------- 提前通知（复用休息提前提示窗）----------

// futureSlot 造一个「原定时刻在现在之后约 2 小时」的外卖条目，避免测试跑在槽位已过的时刻
// （真实预通知只会在原定时刻之前弹出，preTargetAt 一定是未来时刻，这里复刻该前提）。
//
// ⚠️ TimeSlot 只存 hour/minute，slotAlertAt 一律按**当天**解释：20:00 之后再 +2h 会跨到
// 明天，时分落回 0 点附近，按今天算就成了「已经过去 22 小时」的时刻 —— 于是剩余被钳成 0、
// 今日时间线也不展开，测试在晚上必红（2026-10-25 22:05 踩到）。跨天时改取「现在 → 今天
// 23:59」的中点，保证时分仍属今天且在未来。
func futureSlot() model.TimeSlot {
	now := time.Now()
	ft := now.Add(2 * time.Hour)
	if ft.Year() != now.Year() || ft.YearDay() != now.YearDay() {
		end := time.Date(now.Year(), now.Month(), now.Day(), 23, 59, 0, 0, now.Location())
		ft = now.Add(end.Sub(now) / 2)
	}
	return model.TimeSlot{ID: "s1", Hour: ft.Hour(), Minute: ft.Minute(), Repeat: "daily", Enabled: true}
}

// 外卖预通知：showTakeoutPreNotify 把内容标记为 owner=takeout，PreBreakState 据此返回
// 指向「原定时刻」的倒计时（不是提前量）；倒计时归零（原定时刻到）由 checkTakeoutReminders
// 的真窗分支接管，预通知窗也会被关掉。
func TestTakeoutPreNotifyState(t *testing.T) {
	cfg := `{"takeout":{"enabled":true,"leadSec":300,"postponeMin":15,"slots":[` +
		`{"id":"s1","hour":12,"minute":30,"repeat":"daily","days":[],"enabled":true}]}}`
	a := newTakeoutApp(t, cfg)
	a.sched = nil
	slot := futureSlot()
	a.showTakeoutPreNotify(slot, 0)
	st := a.PreBreakState()
	if st["title"] != TAKEOUT_NAME {
		t.Fatalf("预通知标题应为 %q，得到 %v", TAKEOUT_NAME, st["title"])
	}
	if st["custom"] != true {
		t.Fatalf("外卖预通知应走 custom 版式（无说明/无暂停），得到 %v", st["custom"])
	}
	if st["paused"] != false {
		t.Fatalf("外卖预通知不应暂停计时，得到 %v", st["paused"])
	}
	at := slotAlertAt(slot, 0, nowMs())
	remain, _ := st["remainMs"].(int64)
	want := at - nowMs()
	if want < 0 {
		// 23:59 之后无法构造「今天、且在未来」的时刻（TimeSlot 只有时分）：剩余按 0 说
		want = 0
	}
	if remain < want-2000 || remain > want+2000 {
		t.Fatalf("预通知倒计时应≈原定时刻剩余 %d，得到 %d", want, remain)
	}
}

// 外卖预通知「立即提醒」：提前弹真窗并消费该次 occurrence（原定时刻不再重复弹）
func TestTakeoutPreNotifyBreakNow(t *testing.T) {
	cfg := `{"takeout":{"enabled":true,"leadSec":300,"postponeMin":15,"slots":[` +
		`{"id":"s1","hour":12,"minute":30,"repeat":"daily","days":[],"enabled":true}]}}`
	a := newTakeoutApp(t, cfg)
	a.sched = nil
	slot := futureSlot()
	a.showTakeoutPreNotify(slot, 0)
	if err := a.preBreakAct("breakNow"); err != nil {
		t.Fatalf("立即提醒不应报错：%v", err)
	}
	if a.takeoutCurrent == nil {
		t.Fatal("立即提醒应弹真窗（takeoutCurrent 非空）")
	}
	at := slotAlertAt(slot, 0, nowMs())
	if !a.takeoutFired[occurrenceKey("takeout", at)] {
		t.Fatal("立即提醒应消费该次 occurrence，避免原定时刻重复弹")
	}
}

// 外卖预通知「跳过提醒」：消费该次 occurrence，真窗不再弹
func TestTakeoutPreNotifySkip(t *testing.T) {
	cfg := `{"takeout":{"enabled":true,"leadSec":300,"postponeMin":15,"slots":[` +
		`{"id":"s1","hour":12,"minute":30,"repeat":"daily","days":[],"enabled":true}]}}`
	a := newTakeoutApp(t, cfg)
	a.sched = nil
	slot := futureSlot()
	a.showTakeoutPreNotify(slot, 0)
	if err := a.preBreakAct("skip"); err != nil {
		t.Fatalf("跳过提醒不应报错：%v", err)
	}
	at := slotAlertAt(slot, 0, nowMs())
	if !a.takeoutFired[occurrenceKey("takeout", at)] {
		t.Fatal("跳过提醒应消费该次 occurrence")
	}
	if a.takeoutCurrent != nil {
		t.Fatal("跳过提醒不应弹真窗")
	}
}

// 外卖预通知「推迟提醒」：消费原 occurrence，改走推迟线（到点重新弹）
func TestTakeoutPreNotifyPostpone(t *testing.T) {
	cfg := `{"takeout":{"enabled":true,"leadSec":300,"postponeMin":15,"slots":[` +
		`{"id":"s1","hour":12,"minute":30,"repeat":"daily","days":[],"enabled":true}]}}`
	a := newTakeoutApp(t, cfg)
	a.sched = nil
	slot := futureSlot()
	a.showTakeoutPreNotify(slot, 0)
	if err := a.preBreakAct("postpone"); err != nil {
		t.Fatalf("推迟提醒不应报错：%v", err)
	}
	at := slotAlertAt(slot, 0, nowMs())
	if !a.takeoutFired[occurrenceKey("takeout", at)] {
		t.Fatal("推迟提醒应消费原 occurrence")
	}
	if a.takeoutPostponed == nil {
		t.Fatal("推迟提醒应设置推迟计划")
	}
}

// 时间线：外卖条目按「原定时刻」展示（不再减提前量），被消费（立即/跳过）的条目隐藏。
// 用 futureSlot（原定时刻在未来）确保今日时间线会展开它。
func TestTodayTimelineTakeoutOriginalTime(t *testing.T) {
	slot := futureSlot()
	cfg := `{"takeout":{"enabled":true,"leadSec":600,"postponeMin":15,"slots":[` +
		fmt.Sprintf(`{"id":%q,"hour":%d,"minute":%d,"repeat":"daily","days":[],"enabled":true}]}}`,
			slot.ID, slot.Hour, slot.Minute)
	a := newTakeoutApp(t, cfg)
	a.sched = nil
	s := a.cfgSnapshot()
	wantAt := slotAlertAt(slot, 0, nowMs())
	if wantAt <= nowMs() {
		// 23:59 之后构造不出「今天、且在未来」的槽位（TimeSlot 只有时分），今日时间线
		// 本来就不展开已过去的条目 —— 这是产品语义，不是缺陷，跳过而不是误报。
		t.Skip("当前时刻已到 23:59，无法构造今天的未来外卖槽位")
	}
	tl := todayTimeline(s, nowMs(), TimelineOptions{TakeoutFired: map[string]bool{}})
	found := false
	for _, it := range tl {
		if it.Type == "takeout" {
			found = true
			if it.At != wantAt {
				t.Fatalf("外卖时间线应显示原定时刻 %d，得到 %d", wantAt, it.At)
			}
		}
	}
	if !found {
		t.Fatal("时间线应包含外卖条目")
	}
	// 该 occurrence 被消费后，时间线应隐藏它
	tl2 := todayTimeline(s, nowMs(), TimelineOptions{
		TakeoutFired: map[string]bool{occurrenceKey("takeout", wantAt): true},
	})
	for _, it := range tl2 {
		if it.Type == "takeout" && it.At == wantAt {
			t.Fatal("被消费的外卖条目应从时间线隐藏")
		}
	}
}

// ---------- 小工具 ----------

// itoa 已在 scheduler_test.go 定义，这里复用（同包）
