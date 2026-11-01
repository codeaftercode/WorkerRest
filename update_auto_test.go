package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"workerrest/internal/idle"
	"workerrest/internal/settings"
)

// ---------- 自动检查更新的调度（autoUpdateTick）----------

// fixedRand 固定随机时长，便于断言排期时刻。
func fixedRand(d time.Duration) func(time.Duration) time.Duration {
	return func(time.Duration) time.Duration { return d }
}

// TestAutoUpdateRandWithinWindow 随机时长必须落在 [0, max) 内。
func TestAutoUpdateRandWithinWindow(t *testing.T) {
	for _, max := range []time.Duration{autoCheckWindow, autoCheckRetry} {
		for i := 0; i < 200; i++ {
			if d := autoUpdateRand(max); d < 0 || d >= max {
				t.Fatalf("随机时长 %v 超出 [0,%v)", d, max)
			}
		}
	}
	if d := autoUpdateRand(0); d != 0 {
		t.Fatalf("上界为 0 时应返回 0，实得 %v", d)
	}
	if d := autoUpdateRand(-time.Minute); d != 0 {
		t.Fatalf("上界为负时应返回 0，实得 %v", d)
	}
}

// TestAutoUpdateTickDailyTiming 当天首次键鼠活动后，3 小时内随机一刻才真正检查。
func TestAutoUpdateTickDailyTiming(t *testing.T) {
	const day = "2026-10-23"
	t0 := time.Date(2026, 10, 23, 9, 0, 0, 0, time.Local)
	var st autoUpdateState

	// 1) 还没有键鼠活动：不排期（一直不用电脑就永远不检查）
	if act := autoUpdateTick(&st, day, t0, false, false, false, fixedRand(time.Hour)); act != autoActNone {
		t.Fatalf("无活动时不该有动作，实得 %q", act)
	}
	if !st.armedAt.IsZero() {
		t.Fatalf("无活动时不该排期，实得 %v", st.armedAt)
	}

	// 2) 首次活动：只在 3 小时窗内排期，不当场检查
	if act := autoUpdateTick(&st, day, t0, true, false, false, fixedRand(time.Hour)); act != autoActNone {
		t.Fatalf("首次活动后不该立刻检查，实得 %q", act)
	}
	if want := t0.Add(time.Hour); !st.armedAt.Equal(want) {
		t.Fatalf("排期时刻 = %v，期望 %v", st.armedAt, want)
	}

	// 3) 未到点：什么也不做
	if act := autoUpdateTick(&st, day, t0.Add(59*time.Minute), true, false, false, fixedRand(time.Hour)); act != autoActNone {
		t.Fatalf("未到点不该检查，实得 %q", act)
	}

	// 4) 到点且不在暂停休息：去检查，并清掉本次排期
	if act := autoUpdateTick(&st, day, t0.Add(time.Hour), true, false, false, fixedRand(time.Hour)); act != autoActCheck {
		t.Fatalf("到点应检查更新，实得 %q", act)
	}
	if !st.armedAt.IsZero() {
		t.Fatalf("检查后应清空排期，实得 %v", st.armedAt)
	}
}

// TestAutoUpdateTickRetriesWhilePaused 到点若处于暂停休息，30 分钟内再随机，反复。
func TestAutoUpdateTickRetriesWhilePaused(t *testing.T) {
	const day = "2026-10-23"
	t0 := time.Date(2026, 10, 23, 9, 0, 0, 0, time.Local)
	st := autoUpdateState{day: day}
	autoUpdateTick(&st, day, t0, true, false, false, fixedRand(time.Hour))
	at := t0.Add(time.Hour)

	// 第 1 次到点：暂停 → 推 10 分钟
	if act := autoUpdateTick(&st, day, at, true, true, false, fixedRand(10*time.Minute)); act != autoActNone {
		t.Fatalf("暂停时不该检查，实得 %q", act)
	}
	if want := at.Add(10 * time.Minute); !st.armedAt.Equal(want) {
		t.Fatalf("重试时刻 = %v，期望 %v", st.armedAt, want)
	}

	// 第 2 次到点：还暂停 → 再推 5 分钟
	at = at.Add(10 * time.Minute)
	if act := autoUpdateTick(&st, day, at, true, true, false, fixedRand(5*time.Minute)); act != autoActNone {
		t.Fatalf("仍暂停时不该检查，实得 %q", act)
	}
	if want := at.Add(5 * time.Minute); !st.armedAt.Equal(want) {
		t.Fatalf("重试时刻 = %v，期望 %v", st.armedAt, want)
	}

	// 第 3 次到点：已恢复 → 检查
	if act := autoUpdateTick(&st, day, at.Add(5*time.Minute), true, false, false, fixedRand(time.Minute)); act != autoActCheck {
		t.Fatalf("恢复后到点应检查，实得 %q", act)
	}
}

// TestAutoUpdateTickResetsAtMidnight 跨天整体重置：昨晚没查成不影响今天。
func TestAutoUpdateTickResetsAtMidnight(t *testing.T) {
	const d1, d2 = "2026-10-23", "2026-10-24"
	st := autoUpdateState{day: d1}
	late := time.Date(2026, 10, 23, 23, 50, 0, 0, time.Local)
	autoUpdateTick(&st, d1, late, true, false, false, fixedRand(2*time.Hour)) // 排到次日 01:50
	if st.armedAt.IsZero() {
		t.Fatalf("应已排期")
	}

	// 次日 00:30：跨天 → 排期作废，回到「等今天首次活动」（此刻无活动）
	act := autoUpdateTick(&st, d2, time.Date(2026, 10, 24, 0, 30, 0, 0, time.Local),
		false, false, false, fixedRand(time.Hour))
	if act != autoActNone {
		t.Fatalf("跨天不该检查，实得 %q", act)
	}
	if st.day != d2 || !st.armedAt.IsZero() {
		t.Fatalf("跨天应整体重置，实得 day=%q armed=%v", st.day, st.armedAt)
	}

	// 新的一天首次活动 → 重新按 3 小时窗排期（前一天一直暂停没查成也不影响）
	next := time.Date(2026, 10, 24, 8, 0, 0, 0, time.Local)
	autoUpdateTick(&st, d2, next, true, true, false, fixedRand(30*time.Minute))
	if want := next.Add(30 * time.Minute); !st.armedAt.Equal(want) {
		t.Fatalf("新一天的排期 = %v，期望 %v（暂停不影响「首次活动后 3 小时窗」的起算）", st.armedAt, want)
	}
}

// TestAutoUpdateTickSkipsWhenDone 当天已查过 → 不再排期、不再检查。
func TestAutoUpdateTickSkipsWhenDone(t *testing.T) {
	const day = "2026-10-23"
	now := time.Date(2026, 10, 23, 10, 0, 0, 0, time.Local)
	st := autoUpdateState{day: day, armedAt: now}
	if act := autoUpdateTick(&st, day, now, true, false, true, fixedRand(time.Hour)); act != autoActNone {
		t.Fatalf("当天已查过不该再检查，实得 %q", act)
	}
	if !st.armedAt.IsZero() {
		t.Fatalf("当天已查过应清空排期，实得 %v", st.armedAt)
	}
}

// TestRunAutoUpdateCheckRecordsDay 真正检查出一份结论才记「今天查过」；
// 网络失败不记，当天后面还会按规则再排一次。
func TestRunAutoUpdateCheckRecordsDay(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/version.json" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"version":"99.0.0","date":"2026.10.23","url":["https://example.invalid/pkg/"],"sha256":"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"}`)
	}))
	defer srv.Close()

	newApp := func(t *testing.T, site string) *App {
		t.Setenv("WR_SETTINGS_PATH", filepath.Join(t.TempDir(), "settings.json"))
		cfg := settings.New(settings.DefaultSettingsMap(), "")
		cfg.SetSites([]string{site})
		return &App{cfg: cfg, mu: sync.Mutex{}}
	}

	t.Run("有结论才记当天", func(t *testing.T) {
		a := newApp(t, srv.URL+"/version.json")
		a.runAutoUpdateCheck("2026-10-23")
		if got := a.cfg.LastUpdateCheck(); got != "2026-10-23" {
			t.Fatalf("检查成功应记今天，实得 %q", got)
		}
		if st := a.snapshotUpdateStatus(); st["status"] != "update-available" {
			t.Fatalf("状态应为 update-available，实得 %v", st)
		}
	})

	t.Run("失败不记当天", func(t *testing.T) {
		a := newApp(t, srv.URL+"/bad.json")
		a.runAutoUpdateCheck("2026-10-23")
		if got := a.cfg.LastUpdateCheck(); got != "" {
			t.Fatalf("检查失败不该记今天，实得 %q", got)
		}
		if st := a.snapshotUpdateStatus(); st["status"] != "error" {
			t.Fatalf("状态应为 error，实得 %v", st)
		}
	})
}

// TestAutoUpdateLoopDispatch 把「常驻循环 → 状态机 → 真实检查 → 记录」串起来跑一遍：
// 假时钟加速（每次睡眠把虚拟时间往前推），验证首次活动后确实自动联网检查了一次，
// 并把「今天查过」写在虚拟时钟那天的名下。
func TestAutoUpdateLoopDispatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"version":"99.0.0","date":"2026.10.23","url":["https://example.invalid/pkg/"],"sha256":"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"}`)
	}))
	defer srv.Close()

	t.Setenv("WR_SETTINGS_PATH", filepath.Join(t.TempDir(), "settings.json"))
	cfg := settings.New(settings.DefaultSettingsMap(), "")
	cfg.SetSites([]string{srv.URL + "/version.json"})
	if !cfg.Flag(true, "checkUpdateOnStart") {
		t.Fatalf("出厂默认应当勾选「自动检查更新」")
	}
	idleState := idle.NewIdleState()
	idleState.ForceIdle(0) // 0ms 空闲 = 用户正在操作
	a := &App{cfg: cfg, mu: sync.Mutex{}, idle: idleState}

	var clockMu sync.Mutex
	clock := time.Date(2026, 10, 23, 9, 0, 0, 0, time.Local)
	oldNow, oldSleep := autoUpdateNow, autoUpdateSleep
	autoUpdateNow = func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return clock
	}
	var stopped atomic.Bool
	block := make(chan struct{})
	// parked：循环已经彻底挂住（不再碰配置目录）的信号。
	// 没有它就只能靠「LastUpdateCheck 非空」判断，而那一步是**先写内存再落盘**——
	// 轮询看到值时 Save 可能还在写 settings.json.tmp，接着 t.TempDir 的清理会撞上
	// 「目录非空」而偶发失败（全量跑时更容易复现）。
	parked := make(chan struct{}, 1)
	autoUpdateSleep = func(d time.Duration) {
		// 一旦真正查过一次（或测试收尾），就把循环挂住：虚拟时间不再往前走，
		// 免得它在毫秒内又跑到第二天、再触发一次检查。
		if stopped.Load() || a.lastUpdateCheckLocked() != "" {
			select {
			case parked <- struct{}{}:
			default:
			}
			<-block
		}
		clockMu.Lock()
		clock = clock.Add(d)
		clockMu.Unlock()
	}
	defer func() {
		stopped.Store(true)
		autoUpdateNow, autoUpdateSleep = oldNow, oldSleep
	}()

	go a.autoUpdateLoop()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && a.lastUpdateCheckLocked() == "" {
		time.Sleep(5 * time.Millisecond)
	}
	stopped.Store(true)

	// 等循环真的挂住：它挂住的时刻一定在 Save 之后，这时目录才算静止。
	select {
	case <-parked:
	case <-time.After(5 * time.Second):
		t.Fatalf("自动更新循环没有按预期挂起（可能仍在反复检查）")
	}

	if got := a.lastUpdateCheckLocked(); got != "2026-10-23" {
		t.Fatalf("应在假时钟当天（2026-10-23）完成检查，实得 %q", got)
	}
	if st := a.snapshotUpdateStatus(); st["status"] != "update-available" {
		t.Fatalf("状态应为 update-available，实得 %v", st)
	}
}

// driveAutoUpdate 按固定步长把调度状态机跑一段时间，返回每次真正去检查的时刻。
// 这里刻意用真实的 autoUpdateRand，用「区间断言」验证随机窗口没越界。
func driveAutoUpdate(from, to time.Time, step time.Duration,
	active, paused func(time.Time) bool) []time.Time {
	var st autoUpdateState
	var checks []time.Time
	doneDay := ""
	for now := from; now.Before(to); now = now.Add(step) {
		day := now.Format("2006-01-02")
		if autoUpdateTick(&st, day, now, active(now), paused(now), doneDay == day, autoUpdateRand) == autoActCheck {
			checks = append(checks, now)
			doneDay = day
		}
	}
	return checks
}

// TestAutoUpdateTickSimulatedDay 用 1 分钟步长模拟一整天，验证：
// 非暂停时检查点落在「首次活动后 3 小时内」；暂停时按 30 分钟窗反复重试；
// 一整天都没等到非暂停 → 当天不查，第二天照常按自己的首次活动起算。
//
// 注意 tol：状态机是「到点后的第一拍才动手」，而这里步长是 1 分钟，所以真正落点最多
// 比随机窗末晚一个步长。断言必须带这个容差 —— 否则随机数落在窗末那一分钟内就会偶发
// 挂（窗口 3h = 180 拍，命中 1/180，跑几十次必现）。
func TestAutoUpdateTickSimulatedDay(t *testing.T) {
	base := time.Date(2026, 10, 23, 0, 0, 0, 0, time.Local)
	hm := func(h, m int) time.Time { return base.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute) }
	const step = time.Minute
	tol := step

	// 1) 09:00 起一直有操作、不暂停 → 当天恰好查一次，时刻早于 12:00（+1 拍）
	checks := driveAutoUpdate(hm(9, 0), hm(24, 0), step,
		func(time.Time) bool { return true }, func(time.Time) bool { return false })
	if len(checks) != 1 {
		t.Fatalf("当天应恰好检查一次，实得 %d 次：%v", len(checks), checks)
	}
	if checks[0].Before(hm(9, 0)) || checks[0].After(hm(12, 0).Add(tol)) {
		t.Fatalf("检查时刻 %v 未落在 09:00 之后的 3 小时窗内", checks[0])
	}

	// 2) 09:00 起有操作，但一直暂停到 13:00 → 恢复后的 30 分钟内才查
	checks = driveAutoUpdate(hm(9, 0), hm(24, 0), step,
		func(time.Time) bool { return true },
		func(now time.Time) bool { return now.Before(hm(13, 0)) })
	if len(checks) != 1 {
		t.Fatalf("应恰好检查一次，实得 %d 次：%v", len(checks), checks)
	}
	if checks[0].Before(hm(13, 0)) || checks[0].After(hm(13, 30).Add(tol)) {
		t.Fatalf("恢复后的检查时刻 %v 未落在 13:00~13:30 内", checks[0])
	}

	// 3) 第一天全程暂停（当天查不成），第二天不暂停 → 第二天照样查一次
	day2 := base.AddDate(0, 0, 1)
	checks = driveAutoUpdate(hm(8, 0), day2.Add(24*time.Hour), step,
		func(now time.Time) bool { return now.Hour() >= 8 },
		func(now time.Time) bool { return now.Before(day2) })
	if len(checks) != 1 {
		t.Fatalf("第二天应检查一次（前一天没查成不影响），实得 %d 次：%v", len(checks), checks)
	}
	if !checks[0].After(day2) && !checks[0].Equal(day2) {
		t.Fatalf("检查应落在第二天，实得 %v", checks[0])
	}
	if h := checks[0].Sub(day2.Add(8 * time.Hour)); h < 0 || h > 3*time.Hour+tol {
		t.Fatalf("第二天的检查时刻 %v 未落在 08:00 之后的 3 小时窗内", checks[0])
	}
}

// TestMain 在整包单测里默认关掉出厂兜底更新源：否则任何「期望失败」的用例都会真的去
// 访问 goodgoodlive.com（15 秒超时，结果还依赖网络）。要验兜底行为的用例自己临时打开。
func TestMain(m *testing.M) {
	defaultUpdateSite, defaultPackageURL = "", ""
	os.Exit(m.Run())
}

// TestWithFallbackSite 兜底更新源 / 兜底包地址的合并规则：去空白、去重、追加在最后，
// 兜底置空时只留配置里的项。
func TestWithFallbackSite(t *testing.T) {
	oldSite, oldPkg := defaultUpdateSite, defaultPackageURL
	defaultUpdateSite = "https://fb/version.json"
	defaultPackageURL = "https://fb/rest.zip"
	defer func() { defaultUpdateSite, defaultPackageURL = oldSite, oldPkg }()

	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"配置为空 → 只剩兜底", nil, []string{"https://fb/version.json"}},
		{"兜底排在最后", []string{"https://a/version.json", "", "  https://b/version.json  "},
			[]string{"https://a/version.json", "https://b/version.json", "https://fb/version.json"}},
		{"兜底已在链中 → 不重复追加", []string{"https://fb/version.json", "https://a/version.json"},
			[]string{"https://fb/version.json", "https://a/version.json"}},
		{"重复项去重", []string{"https://a/version.json", "https://a/version.json"},
			[]string{"https://a/version.json", "https://fb/version.json"}},
	}
	for _, c := range cases {
		if got := withFallbackSite(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s：得到 %v，期望 %v", c.name, got, c.want)
		}
	}

	// 兜底置空 → 不追加任何东西（全空则返回空链，调用方走「未配置更新源」分支）
	defaultUpdateSite = ""
	if got := withFallbackSite([]string{"https://a/version.json"}); !reflect.DeepEqual(got, []string{"https://a/version.json"}) {
		t.Errorf("兜底置空后不该追加：%v", got)
	}
	if got := withFallbackSite(nil); len(got) != 0 {
		t.Errorf("兜底与配置都空时应返回空链：%v", got)
	}

	if got := withFallbackPackage([]string{"https://a/", " "}); !reflect.DeepEqual(got, []string{"https://a/", "https://fb/rest.zip"}) {
		t.Errorf("包地址兜底应追加在末尾：%v", got)
	}
}

// TestCheckForUpdateFallsBackToDefaultSite 配置里一条更新源都没有时，仍然能靠出厂兜底源
// 完成检查 —— 「装完没配 sites 也能更新」是兜底地址存在的意义。
func TestCheckForUpdateFallsBackToDefaultSite(t *testing.T) {
	body := `{"version":"v99.0.0","date":"2026.10.23",` +
		`"url":["https://x/pkg.zip"],"changes":["x"],` +
		`"sha256":"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	oldSite, oldPkg := defaultUpdateSite, defaultPackageURL
	defaultUpdateSite = srv.URL + "/version.json"
	defaultPackageURL = ""
	defer func() { defaultUpdateSite, defaultPackageURL = oldSite, oldPkg }()

	// 完全空白的配置：连 sites 键都没有
	t.Setenv("WR_SETTINGS_PATH", filepath.Join(t.TempDir(), "settings.json"))
	a := &App{cfg: settings.New(map[string]any{}, ""), mu: sync.Mutex{}}
	st := a.checkForUpdate()
	if st.Status != "update-available" {
		t.Fatalf("应靠兜底源查到新版本，实得 %+v", st)
	}
	if st.Latest != "v99.0.0" {
		t.Fatalf("版本号不符：%q", st.Latest)
	}
}
