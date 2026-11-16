package scheduler

// 撞车的**穷举矩阵**（2026-11-16）。
//
// 已有用例各自只钉一条规则（长休息优先、并入不另开窗、同一身份只并一次……）。
// 这份文件把“谁在场上 × 谁到点 × 以什么顺序到点”全部跑一遍：
//
//	① 在场身份：短休息 / 长休息 / 自定义提醒（三种都真起一次休息）；
//	② 之后到点的事件：{short, long, cb-a, cb-b} 的全部排列（长度 1..4）；
//	③ 同一拍同时到点：上述集合的全部非空子集（考验“长休息优先”与同拍并入的先后）；
//	④ 同一身份重复到点、固定时刻表在一次休息里跨好几班；
//	⑤ 休息中 replan 把 NextType 翻页之后（保存设置 / 重置周期）——当前这次休息的身份
//	   必须仍以 BreakType 为准。
//
// 每个场景后都断言：开了几扇窗、分别是谁、按“完成”收尾几次、记账给了谁、右上角并入了
// 谁（按顺序）、此刻休息还剩多久、侧栏/托盘读的字段（BreakType / AllowEarlyEnd）、
// 时间线是否升序且不含过去。**期望值由一个按文档口径独立写的模型算出**，不是抄实现。

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"workerrest/internal/model"
	"workerrest/internal/plan"
)

// ---------- 配置 ----------

// clBody 一条倒计时线（含 allowEarlyEnd，内置与自定义同形）
func clBody(ivMs, durMs int, allowEarlyEnd bool) string {
	ae := "false"
	if allowEarlyEnd {
		ae = "true"
	}
	return `"enabled":true,"mode":"countdown","intervalMs":` + itoa(ivMs) +
		`,"durationMs":` + itoa(durMs) + `,"notifyBeforeMs":0,"postponeMs":120000,` +
		`"allowEarlyEnd":` + ae + `,"slots":[]`
}

// clCfg 矩阵用的配置：短 20 秒 / 长 90 秒 / 两条自定义各 10 秒，全部允许提前结束。
// 间隔都取得足够大（10 分钟以上），保证重排不会自己冒出新的一班来干扰断言。
var clCfg = `{` +
	`"short":{` + `"enabled":true,"mode":"countdown","intervalMs":600000,"durationMs":20000,"notifyBeforeMs":0,"postponeMs":120000,"allowEarlyEnd":true,"slots":[]` + `},` +
	`"long":{` + `"enabled":true,"mode":"countdown","intervalMs":3600000,"durationMs":90000,"notifyBeforeMs":0,"postponeMs":120000,"allowEarlyEnd":true,"slots":[]` + `},` +
	`"customBreaks":[` +
	`{"id":"cb-a","name":"喝水提醒","config":{` + clBody(1800000, 10000, true) + `}},` +
	`{"id":"cb-b","name":"站起来走走","config":{` + clBody(2400000, 10000, true) + `}}` +
	`]}`

// clDurations 休息时长（秒），断言“倒计时还剩多久”用
var clDurations = map[string]int64{"short": 20000, "long": 90000, "cb-a": 10000, "cb-b": 10000}

// ---------- 场景驱动 ----------

// clScene 起一次休息作为“在场者”。empty 表示不在休息（只用于对照）。
//
// 真实计划里同一刻只挂一条内置线（短 / 长按计数交替），但撞车处理的前提正是“另一条
// 提醒也挂在线上并到点了”（见 merge.go 文件头）。所以这里把缺席的身份补一条未来的
// 挂起线，让四种身份都能被摆到“到点”—— 与既有 ui_matrix / regression 用例同一手法。
func clScene(t *testing.T, occ string) (*Scheduler, *fakeHost) {
	t.Helper()
	s, host := newTestSched(t, clCfg)
	s.ResetCycle()
	if occ != "" {
		if id := s.startBreakAt(occ, nowMs()); id == "" {
			t.Fatalf("前置条件不成立：%s 没能起一次休息", occ)
		}
	}
	// 补线：缺哪条补哪条（时刻都放在未来，不影响“此刻到点”的判定）
	future := map[string]int64{
		"short": 600000, "long": 3600000, "cb-a": 1800000, "cb-b": 2400000,
	}
	names := map[string]string{"short": "短休息", "long": "长休息", "cb-a": "喝水提醒", "cb-b": "站起来走走"}
	s.mu.Lock()
	have := map[string]bool{}
	for _, l := range s.state.Lines {
		have[l.Type] = true
	}
	for k, iv := range future {
		if have[k] {
			continue
		}
		s.state.Lines = append(s.state.Lines, model.PlannedBreak{
			Type: k, Name: names[k], At: nowMs() + iv, Source: "countdown",
		})
	}
	s.mu.Unlock()
	return s, host
}

// clArrive 让一批身份在同一拍到点（forceKindDue 把它们挪到 1 秒前），然后跑一拍 onTick。
func clArrive(t *testing.T, s *Scheduler, kinds []string) {
	t.Helper()
	for _, k := range kinds {
		forceKindDue(t, s, k)
	}
	s.onTick()
}

// ---------- 独立模型（按文档口径重写一遍，不抄实现） ----------
//
// 规则（见 merge.go 文件头）：
//   - 长休息到点、场上不是长休息 → 立刻顶替：被顶掉的记“完成”，新开一扇长休息窗；
//     这一拍到此为止（并入留到下一拍）；
//   - 其他提醒到点 → 并入当前窗；短休息被并 = 记“完成一次”（用户确实在休息）；
//   - 同一身份在一次休息里只处理一次；与场上同一身份到点则直接忽略（不能并进自己）。

type clModel struct {
	occ       string
	windows   []string
	completed int
	counted   []string
	extras    []string
	merged    map[string]bool
	// carry 与场上同一身份、当拍没法处理的到点：**不能被吞掉**，等这次休息换了身份
	// （长休息抢占 / 结束）后还要算数。下一拍继续参与处理。
	carry []string
}

func newClModel(occ string) *clModel {
	m := &clModel{occ: occ, merged: map[string]bool{}}
	if occ != "" {
		m.windows = []string{occ}
	}
	return m
}

// canonical 一拍之内的处理顺序 = 挂起线的顺序：内置在前、自定义按配置顺序
func clRank(k string) int {
	switch k {
	case "short":
		return 0
	case "long":
		return 1
	case "cb-a":
		return 2
	case "cb-b":
		return 3
	}
	return 9
}

func (m *clModel) tick(set []string) {
	pending := append(append([]string(nil), m.carry...), set...)
	m.carry = nil
	sort.SliceStable(pending, func(i, j int) bool { return clRank(pending[i]) < clRank(pending[j]) })
	// 长休息到点、场上不是长休息：这一拍就地换成它，其余到点留到下一拍（实现里
	// preemptShortWithLong 之后直接 return）
	if m.occ != "long" && clContains(pending, "long") {
		m.completed++  // 被顶掉的那次记“完成”，不是提前结束
		m.occ = "long" // 换成长休息
		m.windows = append(m.windows, "long")
		m.merged = map[string]bool{} // 新的一次休息，并入记录清零
		m.carry = append(m.carry, clRemove(pending, "long")...)
		return
	}
	for _, k := range pending {
		if k == m.occ {
			// 与场上同一身份：这一拍没法显示、也不能记；留到换了身份之后再算
			m.carry = append(m.carry, k)
			continue
		}
		if m.merged[k] {
			continue
		}
		m.merged[k] = true
		if k == "short" {
			// 内置短休息被覆盖（或长休息在场时到点）：记完成一次，不弹卡、不并入
			m.counted = append(m.counted, "short")
		} else {
			m.extras = append(m.extras, k)
		}
	}
}

func clContains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func clRemove(list []string, v string) []string {
	out := make([]string, 0, len(list))
	for _, x := range list {
		if x != v {
			out = append(out, x)
		}
	}
	return out
}

// ---------- 断言 ----------

// clAssert 把现场与模型逐项对账
func clAssert(t *testing.T, s *Scheduler, host *fakeHost, m *clModel, ctx string) {
	t.Helper()

	if strings.Join(host.breaks, ",") != strings.Join(m.windows, ",") {
		t.Fatalf("%s：开的窗对不上（期望 %v，实际 %v）", ctx, m.windows, host.breaks)
	}
	if host.completed != m.completed {
		t.Fatalf("%s：按“完成”收尾次数不对（期望 %d，实际 %d）", ctx, m.completed, host.completed)
	}
	if strings.Join(host.counted, ",") != strings.Join(m.counted, ",") {
		t.Fatalf("%s：被覆盖记账不对（期望 %v，实际 %v）", ctx, m.counted, host.counted)
	}
	if strings.Join(host.extras, ",") != strings.Join(m.extras, ",") {
		t.Fatalf("%s：并入清单不对（期望 %v，实际 %v）", ctx, m.extras, host.extras)
	}
	if len(host.pre) != 0 {
		t.Fatalf("%s：休息中不该再弹提前提示窗，实际 %v", ctx, host.pre)
	}

	st := s.StateForRenderer()
	if st.Phase != "break" {
		t.Fatalf("%s：应当一直在休息，实际 phase=%q", ctx, st.Phase)
	}
	if st.BreakType != m.occ {
		t.Fatalf("%s：正在弹的身份应为 %q，实际 %q", ctx, m.occ, st.BreakType)
	}
	// 倒计时：以这次休息自己的时长为准，且必须是“未来”
	now := nowMs()
	if d := st.BreakEndsAt - now; d <= 0 || d > clDurations[m.occ] {
		t.Fatalf("%s：休息倒计时应在 (0, %d]，实际还剩 %d ms", ctx, clDurations[m.occ], d)
	}
	// 侧栏 / 托盘读的两个字段
	if want := clAllowEarly(m.occ); st.AllowEarlyEnd != want {
		t.Fatalf("%s：allowEarlyEnd 应为 %v（按 %s 那张卡），实际 %v",
			ctx, want, m.occ, st.AllowEarlyEnd)
	}
	// 长休息在场时短休息计数已清零（长短交替的唯一推进点）
	// 注：长休息期间若又“覆盖”了一次短休息，计数会按“歇了一次”+1，收尾时才清零，
	// 所以这里不钉 0，只钉“长休息正常收尾后会归零”（见 TestCollisionLongResetsShortCount）。
	// 时间线：升序、不含过去
	assertTimelineSorted(t, st, now)

	// 并入的倒计时线必须被推到“休息结束之后”，否则休息一结束就会补弹一次
	if len(m.extras) > 0 {
		s.mu.Lock()
		ends := s.state.BreakEndsAt
		for _, k := range m.extras {
			found := false
			for _, l := range s.state.Lines {
				if l.Type != k {
					continue
				}
				found = true
				if l.At <= ends {
					s.mu.Unlock()
					t.Fatalf("%s：并入的 %s 下一班应排在休息结束之后（%s），实际 %s",
						ctx, k, time.UnixMilli(ends).Format("15:04:05"),
						time.UnixMilli(l.At).Format("15:04:05"))
				}
			}
			if !found {
				s.mu.Unlock()
				t.Fatalf("%s：并入的 %s 在挂起线里找不到了（锚点丢了）", ctx, k)
			}
		}
		s.mu.Unlock()
	}
}

func clAllowEarly(kind string) bool { return true } // clCfg 四张卡都允许

// ---------- ① 顺序排列：全部排列（长度 1..4） ----------

func TestCollisionMatrixSequentialPermutations(t *testing.T) {
	pool := []string{"short", "long", "cb-a", "cb-b"}
	occupants := []string{"short", "long", "cb-a"}

	for _, occ := range occupants {
		occ := occ
		for _, seq := range clPermutations(pool, 1, len(pool)) {
			seq := seq
			t.Run(occ+"/"+strings.Join(seq, "→"), func(t *testing.T) {
				s, host := clScene(t, occ)
				m := newClModel(occ)
				for i, k := range seq {
					clArrive(t, s, []string{k})
					m.tick([]string{k})
					clAssert(t, s, host, m, fmt.Sprintf("第 %d 步（%s 到点）", i+1, k))
				}
			})
		}
	}
}

// ---------- ② 同一拍多个到点：全部非空子集 ----------

func TestCollisionMatrixSimultaneous(t *testing.T) {
	pool := []string{"short", "long", "cb-a", "cb-b"}
	occupants := []string{"short", "long", "cb-a"}

	for _, occ := range occupants {
		occ := occ
		for _, set := range clSubsets(pool) {
			set := set
			t.Run(occ+"/{"+strings.Join(set, ",")+"}", func(t *testing.T) {
				s, host := clScene(t, occ)
				m := newClModel(occ)

				// 同一拍全部到点：长休息抢占会先发生，其余留到下一拍并入
				clArrive(t, s, set)
				m.tick(set)
				// 抢占那一拍之后，再补一拍把剩下的处理掉（实现里下一拍才并入）
				s.onTick()
				m.tick(nil)
				clAssert(t, s, host, m, "同一拍 "+strings.Join(set, "+"))
			})
		}
	}
}

// ---------- ③ 同一身份重复到点 ----------

func TestCollisionSameIdentityRepeats(t *testing.T) {
	cases := []struct {
		name  string
		occ   string
		steps [][]string
	}{
		{"短休息在场，短休息连着到点两次", "short", [][]string{{"short"}, {"short"}}},
		{"长休息在场，长休息连着到点两次", "long", [][]string{{"long"}, {"long"}}},
		{"自定义提醒在场，同一身份再到期", "cb-a", [][]string{{"cb-a"}, {"cb-a"}}},
		{"短休息在场：同拍忽略自己、并进自定义，下一拍自己再来仍被忽略", "short", [][]string{{"short", "cb-a"}, {"short"}}},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			s, host := clScene(t, c.occ)
			m := newClModel(c.occ)
			for i, step := range c.steps {
				clArrive(t, s, step)
				m.tick(step)
				clAssert(t, s, host, m, fmt.Sprintf("第 %d 步 %v", i+1, step))
			}
			// 同一条提醒在一次休息里只挂一张卡
			seen := map[string]int{}
			for _, e := range host.extras {
				seen[e]++
			}
			for k, n := range seen {
				if n > 1 {
					t.Fatalf("同一条提醒在一次休息里被并了 %d 次：%s", n, k)
				}
			}
		})
	}
}

// 同一条自定义提醒跨两次休息：每次休息各挂一张卡（并入记录按“这一次休息”清零）。
// 这也是“同身份多个提醒”真正的形态：不是一次休息里重复，而是下一次休息要能再提醒一次。
func TestCollisionSameIdentityAcrossTwoBreaks(t *testing.T) {
	s, host := clScene(t, "short")

	clArrive(t, s, []string{"cb-a"})
	if got := strings.Join(host.extras, ","); got != "cb-a" {
		t.Fatalf("第一次休息应并入 cb-a，实际 %v", host.extras)
	}

	// 第一次休息结束（自然到点），再起一次
	s.mu.Lock()
	s.state.BreakEndsAt = nowMs() - 1000
	s.mu.Unlock()
	s.onTick()
	if got := s.CurrentPhase(); got != "idle" {
		t.Fatalf("第一次休息应已结束，实际 %q", got)
	}
	if id := s.StartQueuedBreak("short"); id == "" {
		// 用“立即休息”那条路重开一次（内置身份取当前计划里的短 / 长）
		if id2 := s.BreakNowOf("short"); id2 == "" {
			t.Fatalf("第二次休息没能起来")
		}
	}
	// 把 cb-a 重新摆到“到点”（上一次并入时它已被推到休息结束之后）
	clArrive(t, s, []string{"cb-a"})

	if got := strings.Join(host.extras, ","); got != "cb-a,cb-a" {
		t.Fatalf("第二次休息应再并一次 cb-a（两次休息各一张），实际 %v", host.extras)
	}
}

// ---------- ④ 固定时刻表：一次休息横跨好几班 ----------

// 固定时刻的自定义提醒在一次长休息里跨过 3 个时刻：这三班必须全部记成已消费，
// 否则休息结束后 replan 会把它们一个个排回来（休息一结束连着弹三次）。
func TestCollisionFixedSlotsAcrossBreakAllConsumed(t *testing.T) {
	now := time.Now()
	s1 := now.Add(1 * time.Minute).Truncate(time.Minute)
	s2 := now.Add(2 * time.Minute).Truncate(time.Minute)
	s3 := now.Add(3 * time.Minute).Truncate(time.Minute)
	slots := slotLine("f1", s1) + `,` + slotLine("f2", s2) + `,` + slotLine("f3", s3)
	js := `{` +
		`"short":{` + clBody(600000, 20000, true) + `},` +
		`"long":{` + clBody(3600000, 600000, true) + `},` + // 10 分钟：跨过三班
		`"customBreaks":[{"id":"cb-f","name":"远眺提醒","config":{"enabled":true,"mode":"fixed",` +
		`"durationMs":10000,"notifyBeforeMs":0,"postponeMs":120000,"allowEarlyEnd":true,"slots":[` + slots + `]}}]}`

	s, host := newTestSched(t, js)
	s.ResetCycle()
	if id := s.startBreakAt("long", nowMs()); id == "" {
		t.Fatal("前置条件不成立：长休息没起来")
	}
	// 把这条固定线摆到“刚刚到点”（真实到点要等分钟边界，这里只验消费循环）
	s.mu.Lock()
	for i := range s.state.Lines {
		if s.state.Lines[i].Type == "cb-f" {
			s.state.Lines[i].At = nowMs() - 1000
		}
	}
	s.mu.Unlock()

	s.onTick()

	if len(host.extras) != 1 || host.extras[0] != "cb-f" {
		t.Fatalf("固定时刻的自定义提醒应并入一次，实际 %v", host.extras)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, at := range []time.Time{s1, s2, s3} {
		if !s.consumed[plan.OccurrenceKey("cb-f", at.UnixMilli())] {
			t.Fatalf("休息期间跨过的 %s 那一班没被消费（休息一结束会补弹）",
				at.Format("15:04"))
		}
	}
}

// 长休息正常收尾时把“短休息计数”清零：哪怕休息期间又覆盖过一次短休息。
func TestCollisionLongResetsShortCount(t *testing.T) {
	s, _ := clScene(t, "long")
	s.mu.Lock()
	s.state.ShortsSinceLong = 3
	s.state.BreakEndsAt = nowMs() - 1000 // 自然到点
	s.mu.Unlock()

	s.onTick()

	s.mu.Lock()
	got := s.state.ShortsSinceLong
	s.mu.Unlock()
	if got != 0 {
		t.Fatalf("长休息结束后短休息计数应清零，实际 %d", got)
	}
}

// ---------- ⑤ 倒计时节奏 ----------
// 并入之后下一班仍按原间隔往前走，而不是从休息结束那一刻重新起算
func TestCollisionMergedCountdownKeepsRhythm(t *testing.T) {
	s, host := clScene(t, "short")
	m := newClModel("short")

	// 先把 cb-a 摆到“到点”，记下这一刻作为基准（= 原定那一班）
	before := forceKindDue(t, s, "cb-a")
	if before == 0 {
		t.Fatal("前置条件不成立：没有 cb-a 的挂起线")
	}
	s.onTick()
	m.tick([]string{"cb-a"})
	clAssert(t, s, host, m, "cb-a 并入")

	s.mu.Lock()
	after, ends := int64(0), s.state.BreakEndsAt
	for _, l := range s.state.Lines {
		if l.Type == "cb-a" {
			after = l.At
		}
	}
	s.mu.Unlock()

	if after <= ends {
		t.Fatalf("下一班 %s 应排在休息结束 %s 之后",
			time.UnixMilli(after).Format("15:04:05"), time.UnixMilli(ends).Format("15:04:05"))
	}
	if d := after - before; d <= 0 || d%1800000 != 0 {
		t.Fatalf("下一班应当是原班次 + n×间隔（30 分钟），实际相差 %d ms", d)
	}
	if d := after - ends; d > 1800000 {
		t.Fatalf("下一班离休息结束不该超过一个间隔，实际 %d ms", d)
	}
}

// ---------- ⑥ 休息中 replan（保存设置 / 重置周期）之后，身份以 BreakType 为准 ----------

// 自定义提醒在弹 → 期间发生一次 replan，NextType 被翻成“下一条”（long / short）。
// 此后长休息到点必须顶替、短休息到点必须记完成 —— 都不能因为 NextType 被翻页而静默吞掉。
// 自然结束时要推进的是**这次休息自己的**身份，不是翻页后的 NextType。
func TestCollisionBreakTypeIsAuthoritative(t *testing.T) {
	cases := []struct {
		name    string
		flipTo  string
		arrive  string
		wantWin []string
		wantCnt []string
	}{
		{
			name: "自定义休息中 NextType 翻成长休息：长休息照常抢占", flipTo: "long", arrive: "long",
			wantWin: []string{"cb-a", "long"}, wantCnt: nil,
		},
		{
			name: "自定义休息中 NextType 翻成短休息：短休息到点要记完成", flipTo: "short", arrive: "short",
			wantWin: []string{"cb-a"}, wantCnt: []string{"short"},
		},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			s, host := clScene(t, "cb-a")
			s.mu.Lock()
			s.state.NextType = c.flipTo // 模拟 replan 把“下一条”翻页
			s.mu.Unlock()

			clArrive(t, s, []string{c.arrive})

			// 用 BreakType 读“正在弹的是谁”，不能拿 NextType
			st := s.StateForRenderer()
			if st.BreakType != "cb-a" && st.BreakType != "long" {
				t.Fatalf("BreakType 被 NextType 带跑了：%q", st.BreakType)
			}
			if len(host.breaks) != len(c.wantWin) {
				t.Fatalf("开窗序列应为 %v，实际 %v", c.wantWin, host.breaks)
			}
			for i := range c.wantWin {
				if host.breaks[i] != c.wantWin[i] {
					t.Fatalf("开窗序列应为 %v，实际 %v", c.wantWin, host.breaks)
				}
			}
			if strings.Join(host.counted, ",") != strings.Join(c.wantCnt, ",") {
				t.Fatalf("被覆盖记账应为 %v，实际 %v", c.wantCnt, host.counted)
			}
		})
	}
}

// 自定义休息自然到点时，推进的必须是这次休息自己的身份（cb-a 不参与长短交替）：
// NextType 被翻成 short 也不能把短休息计数凭空 +1。
func TestCollisionFinishAdvanceUsesBreakType(t *testing.T) {
	s, _ := clScene(t, "cb-a")
	s.mu.Lock()
	s.state.NextType = "short"
	before := s.state.ShortsSinceLong
	s.state.BreakEndsAt = nowMs() - 1000 // 自然到点
	s.mu.Unlock()

	s.onTick()

	s.mu.Lock()
	after := s.state.ShortsSinceLong
	s.mu.Unlock()
	if after != before {
		t.Fatalf("自定义休息结束不该推进短休息计数（%d → %d）", before, after)
	}
	if got := s.CurrentPhase(); got != "idle" {
		t.Fatalf("自然到点后应回到 idle，实际 %q", got)
	}
}

// ---------- 生成器 ----------
// clPermutations 全部排列（长度 min..max）
func clPermutations(items []string, min, max int) [][]string {
	var out [][]string
	var walk func(cur []string, used []bool)
	walk = func(cur []string, used []bool) {
		if len(cur) >= min {
			out = append(out, append([]string(nil), cur...))
		}
		if len(cur) == max {
			return
		}
		for i := range items {
			if used[i] {
				continue
			}
			used[i] = true
			walk(append(cur, items[i]), used)
			used[i] = false
		}
	}
	walk(nil, make([]bool, len(items)))
	return out
}

// clSubsets 全部非空子集
func clSubsets(items []string) [][]string {
	var out [][]string
	for mask := 1; mask < 1<<len(items); mask++ {
		var set []string
		for i := range items {
			if mask&(1<<i) != 0 {
				set = append(set, items[i])
			}
		}
		out = append(out, set)
	}
	return out
}
