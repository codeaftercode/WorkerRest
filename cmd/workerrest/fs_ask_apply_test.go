package main

import (
	"testing"

	"workerrest/internal/settings"
	"workerrest/internal/windnd"
)

// 非独占全屏（QUNS=2）三档处置的接线回归：判据与状态机各自有自己的用例，
// 这里钉的是“三档真的接到了调度器上”。
//
// 时间与前台窗口都由调用方给（fsAskAdvance 的入参），所以不需要真窗口、也不等 3 秒。

// qunsBusySample QUNS=2 + 勿扰关闭
func qunsBusySample() windnd.BusyStatus {
	return windnd.BusyStatus{QunsSupported: true, Quns: 2, WnfSupported: true, QuietProfile: 0}
}

// 出厂默认（off）：QUNS=2 完全不处置 —— 不暂停、不问、也不留原因。
func TestBusyModeOffDoesNothing(t *testing.T) {
	a, s := newWatchApp(t, shortCountdown)
	for i := 0; i < 3; i++ {
		a.dndApply(qunsBusySample(), true, true, settings.FullScreenBusyModeOff)
	}
	if st := s.StateCopy(); st.Paused || len(st.PauseReasons) != 0 {
		t.Fatalf("off 档不该暂停，得到 %v", st.PauseReasons)
	}
	a.fsAskMu.Lock()
	pending := a.fsAsk.pending
	a.fsAskMu.Unlock()
	if pending {
		t.Fatal("off 档不该发起询问")
	}
}

// pause 档：旧行为，两拍后因“非独占全屏”暂停；条件消失按策略保持暂停。
func TestBusyModePauseBehavesLikeBefore(t *testing.T) {
	a, s := newWatchApp(t, shortCountdown)
	a.dndApply(qunsBusySample(), true, true, settings.FullScreenBusyModePause)
	if s.IsPaused() {
		t.Fatal("单拍不该暂停（进入侧去抖）")
	}
	a.dndApply(qunsBusySample(), true, true, settings.FullScreenBusyModePause)
	rs := s.StateCopy().PauseReasons
	if !s.IsPaused() || len(rs) != 1 || rs[0] != pauseReasonFullScrBusy {
		t.Fatalf("两拍后应停在“非独占全屏”，得到 %v", rs)
	}
	if !a.fsBusyLogged {
		t.Fatal("暂停时应记住已打过进入日志")
	}
	// 条件消失：这条属于“保持暂停”，原因留在调度器里等用户手动恢复
	a.dndApply(qunsBusySample(), true, true, settings.FullScreenBusyModeOff)
	if !s.IsPaused() || !containsReason(s.StateCopy().PauseReasons, pauseReasonFullScrBusy) {
		t.Fatal("关掉该档不该自动恢复：非独占全屏属于“条件消失保持暂停”那一侧")
	}
}

// ask 档：询问期间不暂停（计时照走），用户答“暂停”才落暂停。
func TestBusyModeAskPausesOnlyAfterAnswer(t *testing.T) {
	a, s := newWatchApp(t, shortCountdown)
	geo := fsGeoFull("vlc.exe", 0x10)
	now := int64(1_700_000_000_000)

	a.fsAskAdvance(true, geo, now)
	a.fsAskAdvance(true, geo, now+3000) // 第二拍：发起询问
	if s.IsPaused() {
		t.Fatal("询问期间必须不暂停（用户还在看视频，倒计时该继续走）")
	}
	a.fsAskMu.Lock()
	pending := a.fsAsk.pending
	a.fsAskMu.Unlock()
	if !pending {
		t.Fatal("第二拍应发起询问")
	}

	if err := a.FullScreenAskAnswer(fsAnswerPause); err != nil {
		t.Fatalf("答复失败：%v", err)
	}
	if !s.IsPaused() || !containsReason(s.StateCopy().PauseReasons, pauseReasonFullScrBusy) {
		t.Fatalf("答“暂停”后应停在该原因上，得到 %v", s.StateCopy().PauseReasons)
	}
}

// ask 档：答“本次不暂停”既不暂停，也不在本段重复问。
func TestBusyModeAskSkipStaysRunning(t *testing.T) {
	a, s := newWatchApp(t, shortCountdown)
	geo := fsGeoFull("vlc.exe", 0x20)
	now := int64(1_700_000_000_000)
	a.fsAskAdvance(true, geo, now)
	a.fsAskAdvance(true, geo, now+3000)

	if err := a.FullScreenAskAnswer(fsAnswerSkip); err != nil {
		t.Fatalf("答复失败：%v", err)
	}
	if s.IsPaused() {
		t.Fatal("“本次不暂停”不该暂停")
	}
	a.fsAskAdvance(true, geo, now+6000)
	a.fsAskAdvance(true, geo, now+9000)
	a.fsAskMu.Lock()
	pending := a.fsAsk.pending
	a.fsAskMu.Unlock()
	if pending {
		t.Fatal("同一段不该重复询问")
	}
	if s.IsPaused() {
		t.Fatal("同一段后续拍也不该暂停")
	}
}

// QUNS=2 但前台不是全屏（最大化窗口 / 系统把演示设置报成 2）：不算全屏段，
// 既不暂停也不问 —— 这是“不再单信 QUNS”的核心收益。
func TestBusyModeAskIgnoresMaximizedForeground(t *testing.T) {
	a, s := newWatchApp(t, shortCountdown)
	geo := fsGeoWorkArea("chrome.exe", 0x30)
	now := int64(1_700_000_000_000)
	for i := 0; i < 4; i++ {
		a.fsAskAdvance(true, geo, now+int64(i)*3000)
	}
	if s.IsPaused() {
		t.Fatal("最大化窗口不该触发暂停")
	}
	a.fsAskMu.Lock()
	pending := a.fsAsk.pending
	a.fsAskMu.Unlock()
	if pending {
		t.Fatal("不该发起询问")
	}
}

// 投屏 / 勿扰生效时 ask 档也只能直接暂停（屏幕上不能多出窗）。
func TestBusyModeAskPausesWhenRisky(t *testing.T) {
	a, s := newWatchApp(t, shortCountdown)
	// 先把勿扰条件登记成“成立”：模拟用户开了勿扰
	a.setAutoReasonActive(pauseReasonDnd, true)
	geo := fsGeoFull("zoom.exe", 0x40)
	now := int64(1_700_000_000_000)
	a.fsAskAdvance(true, geo, now)
	a.fsAskAdvance(true, geo, now+3000)

	if !s.IsPaused() || !containsReason(s.StateCopy().PauseReasons, pauseReasonFullScrBusy) {
		t.Fatalf("压制中应直接暂停，得到 %v", s.StateCopy().PauseReasons)
	}
	a.fsAskMu.Lock()
	pending := a.fsAsk.pending
	a.fsAskMu.Unlock()
	if pending {
		t.Fatal("压制中绝不能发起询问")
	}
}

// 名单命中（永不暂停）：ask 档静默放行，既不暂停也不问。
func TestBusyModeAskRespectsSkipList(t *testing.T) {
	a, s := newWatchApp(t, shortCountdown+``)
	// 把名单写进配置：exe 小写匹配
	a.cfg = settings.New(map[string]any{
		"fullscreenBusyMode": settings.FullScreenBusyModeAsk,
		"fullscreenSkipApps": []any{
			map[string]any{"exe": "vlc.exe", "label": "VLC", "mode": "never"},
		},
	}, "")
	geo := fsGeoFull("vlc.exe", 0x50)
	now := int64(1_700_000_000_000)
	for i := 0; i < 3; i++ {
		a.fsAskAdvance(true, geo, now+int64(i)*3000)
	}
	if s.IsPaused() {
		t.Fatal("名单命中的应用不该暂停")
	}
	a.fsAskMu.Lock()
	pending := a.fsAsk.pending
	a.fsAskMu.Unlock()
	if pending {
		t.Fatal("名单命中的应用不该询问")
	}
}

// 3/4 档仍走老路：开关开着就暂停、关掉就只登记不暂停（原因按策略保留）。
func TestExclusiveTierStillPauses(t *testing.T) {
	a, s := newWatchApp(t, shortCountdown)
	st := windnd.BusyStatus{QunsSupported: true, Quns: 3, WnfSupported: true, QuietProfile: 0}
	a.dndApply(st, true, true, settings.FullScreenBusyModeOff)
	a.dndApply(st, true, true, settings.FullScreenBusyModeOff)
	if !containsReason(s.StateCopy().PauseReasons, pauseReasonFullScr) {
		t.Fatalf("QUNS=3 应直接暂停，得到 %v", s.StateCopy().PauseReasons)
	}

	b, s2 := newWatchApp(t, shortCountdown)
	st2 := windnd.BusyStatus{QunsSupported: true, Quns: 4, WnfSupported: true, QuietProfile: 0}
	b.dndApply(st2, false, true, settings.FullScreenBusyModeOff)
	b.dndApply(st2, false, true, settings.FullScreenBusyModeOff)
	rs := s2.StateCopy().PauseReasons
	if len(rs) != 1 || rs[0] != pauseReasonFullScr {
		t.Fatalf("QUNS=4 应命中“全屏或演示”，得到 %v", rs)
	}
}
