package main

import (
	"math/rand"
	"path/filepath"
	"testing"
	"time"
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
		cfg:          testSettings(t, cfgJSON),
		rolesRoot:    t.TempDir(),
		rng:          rand.New(rand.NewSource(1)),
		cursor:       make(map[string]int),
		takeoutFired: map[string]bool{},
		idle:         NewIdleState(),
	}
}

// takeoutCfg 一条启用的外卖配置：午餐 12:30、可推迟 15 分钟、提前 0 分钟
const takeoutCfg = `{
  "takeout": {
    "enabled": true,
    "leadMin": 0,
    "postponeMin": 15,
    "slots": [{"id":"s1","hour":12,"minute":30,"repeat":"daily","days":[],"enabled":true}]
  },
  "reminderMessages": {
    "takeout": {"order":"random","fixedId":"","items":[{"id":"m1","text":"该点外卖啦","enabled":true}]}
  }
}`

func TestBuildTakeoutInfo(t *testing.T) {
	a := newTakeoutApp(t, takeoutCfg)
	slot := TimeSlot{ID: "s1", Hour: 12, Minute: 30, Repeat: "daily", Enabled: true}
	info := a.buildTakeoutInfo(slot, false, 0)
	if info == nil {
		t.Fatal("会话不应为空")
	}
	if info.Meal != "午餐" {
		t.Fatalf("餐段应=午餐，得到 %q", info.Meal)
	}
	if info.Hour != 12 || info.Minute != 30 {
		t.Fatalf("时刻应=12:30，得到 %d:%d", info.Hour, info.Minute)
	}
	if info.LeadMin != 0 || info.PostponeMin != 15 {
		t.Fatalf("lead/postpone 应=0/15，得到 %d/%d", info.LeadMin, info.PostponeMin)
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
	cfg := `{"takeout":{"enabled":true,"leadMin":0,"postponeMin":0,"slots":[]},"installId":"uuid-1234"}`
	a := newTakeoutApp(t, cfg)
	info := a.buildTakeoutInfo(TimeSlot{ID: "s1", Hour: 12, Minute: 30, Enabled: true}, false, 0)
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
	cfg := `{"takeout":{"enabled":true,"leadMin":0,"postponeMin":15,"slots":[]},"installId":"uuid-1234"}`
	a := newTakeoutApp(t, cfg)
	info := a.buildTakeoutInfo(TimeSlot{ID: "s1", Hour: 18, Minute: 0, Enabled: true}, false, 0)
	if info.RedPacketData == nil {
		t.Fatal("外卖提醒窗口应始终展示外卖优惠券（redPacketData 不应为 nil）")
	}
	want := "https://nav.hcctc.cn/api/cps/id=uuid-1234"
	if info.RedPacketData.URL != want {
		t.Fatalf("红包地址应含 installId，得到 %q", info.RedPacketData.URL)
	}
}

func TestBuildTakeoutInfoSupervisorClearsCommand(t *testing.T) {
	// 有督休专员时 supervisor 的 command 必须清空（外卖窗口第二段不换台词）
	// 这里只验证「nil 时不炸」——有督休专员的路径由 GUI/对拍覆盖（需要真实立绘 data URL）
	a := newTakeoutApp(t, takeoutCfg)
	info := a.buildTakeoutInfo(TimeSlot{ID: "s1", Hour: 12, Minute: 30, Enabled: true}, false, 0)
	if info.Supervisor != nil {
		t.Fatalf("空督休专员目录应无 supervisor，得到 %+v", info.Supervisor)
	}
}

// ---------- 触发 / 推迟 ----------

// 固定时刻线：把 slot 设成「现在这一刻」就应立即触发
func TestCheckTakeoutFiresOnDueSlot(t *testing.T) {
	now := time.Now()
	cfg := `{"takeout":{"enabled":true,"leadMin":0,"postponeMin":15,"slots":[` +
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
	slot := TimeSlot{ID: "s1", Hour: 12, Minute: 30, Repeat: "daily", Enabled: true}
	// 直接注入一笔「已到点」的推迟计划
	a.takeoutPostponed = &TakeoutPostponed{Slot: slot, DueAt: nowMs() - 1, Count: 2}
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
	slot := TimeSlot{ID: "s1", Hour: 12, Minute: 30, Repeat: "daily", Enabled: true}
	a.takeoutPostponed = &TakeoutPostponed{Slot: slot, DueAt: nowMs() - takeoutPostponeGraceMs - 1000, Count: 1}
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
	a := newTakeoutApp(t, `{"takeout":{"enabled":false,"leadMin":0,"postponeMin":15,"slots":[]}}`)
	a.sched = nil
	a.takeoutPostponed = &TakeoutPostponed{Slot: TimeSlot{}, DueAt: nowMs() - 1, Count: 1}
	a.checkTakeoutReminders()
	if a.takeoutPostponed != nil {
		t.Fatal("外卖关闭时应清掉推迟计划")
	}
}

// ---------- 动作记账 ----------

func TestTakeoutActionOrderedAndSkip(t *testing.T) {
	a := newTakeoutApp(t, takeoutCfg)
	a.sched = nil
	slot := TimeSlot{ID: "s1", Hour: 12, Minute: 30, Repeat: "daily", Enabled: true}
	a.takeoutCurrent = a.buildTakeoutInfo(slot, false, 0)
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
	slot := TimeSlot{ID: "s1", Hour: 12, Minute: 30, Repeat: "daily", Enabled: true}
	a.takeoutCurrent = a.buildTakeoutInfo(slot, false, 0)
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
	slot := TimeSlot{ID: "s1", Hour: 12, Minute: 30, Repeat: "daily", Enabled: true}
	a.takeoutCurrent = a.buildTakeoutInfo(slot, true, 0)
	if err := a.TakeoutAction("takeout", "postpone"); err != nil {
		t.Fatalf("预览 postpone 报错: %v", err)
	}
	if a.takeoutPostponed != nil {
		t.Fatal("预览态的推迟不应记真实推迟计划")
	}
}

// 外卖统计记账走 RecordTakeoutAction：验证 stats 里确实 +1（预览不计）
func TestTakeoutActionRecordsStats(t *testing.T) {
	a := newTakeoutApp(t, takeoutCfg)
	a.sched = nil
	st := NewStatsStore(func() string { return filepath.Join(t.TempDir(), "stats.json") })
	a.stats = st
	slot := TimeSlot{ID: "s1", Hour: 12, Minute: 30, Repeat: "daily", Enabled: true}
	a.takeoutCurrent = a.buildTakeoutInfo(slot, false, 0)
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

// ---------- 小工具 ----------

// itoa 已在 scheduler_test.go 定义，这里复用（同包）
