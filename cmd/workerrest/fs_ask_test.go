package main

import (
	"testing"

	"workerrest/internal/windnd"
)

// 盖满整块屏的无边框窗口（= 真全屏）
func fsGeoFull(exe string, hwnd uintptr) windnd.ForegroundInfo {
	mon := windnd.FSRect{Left: 0, Top: 0, Right: 1920, Bottom: 1080}
	return windnd.ForegroundInfo{
		Supported: true, HWND: hwnd, PID: 100, Exe: exe,
		Rect: mon, Monitor: mon, ExeFrom: "toolhelp",
	}
}

// 只覆盖工作区的最大化窗口（任务栏 48px）——不算全屏
func fsGeoWorkArea(exe string, hwnd uintptr) windnd.ForegroundInfo {
	return windnd.ForegroundInfo{
		Supported: true, HWND: hwnd, PID: 100, Exe: exe,
		Rect:    windnd.FSRect{Left: 0, Top: 0, Right: 1920, Bottom: 1032},
		Monitor: windnd.FSRect{Left: 0, Top: 0, Right: 1920, Bottom: 1080},
		Style:   0x00C00000, // WS_CAPTION
	}
}

// 进入要两拍、问只问一次、超时按暂停结案且只结案一次。
func TestFSAskAsksOnceThenTimesOutToPause(t *testing.T) {
	st := &fsAskState{}
	in := fsAskInput{Supported: true, Busy: true, Geo: fsGeoFull("vlc.exe", 0x10), Now: 1000}

	if got := st.step(in); got != fsAskStepNone {
		t.Fatalf("进入第一拍不该有动作（去抖），得到 %v", got)
	}
	if got := st.step(in); got != fsAskStepAsk {
		t.Fatalf("第二拍应该发起询问，得到 %v", got)
	}
	if !st.pending || st.seq != 1 || st.deadline != 1000+fsAskTimeoutMs {
		t.Fatalf("询问状态不完整：%+v", st)
	}
	if st.exe != "vlc.exe" || st.label != "vlc" {
		t.Fatalf("询问窗要显示哪个应用，得到 exe=%q label=%q", st.exe, st.label)
	}
	if got := st.step(in); got != fsAskStepNone {
		t.Fatalf("一段只问一次，得到 %v", got)
	}

	in.Now = st.deadline - 1
	if got := st.step(in); got != fsAskStepNone {
		t.Fatalf("还没到超时，得到 %v", got)
	}
	in.Now = st.deadline
	if got := st.step(in); got != fsAskStepPauseNow {
		t.Fatalf("超时必须按暂停结案，得到 %v", got)
	}
	if st.pending {
		t.Fatal("结案后不能还挂着 pending")
	}
	if got := st.step(in); got != fsAskStepNone {
		t.Fatalf("只结案一次，得到 %v", got)
	}
}

// 名单命中与“点过恢复休息”都是静默：不暂停、不询问、也不重复打扰。
func TestFSAskSkipAndIgnoredStaySilent(t *testing.T) {
	cases := []struct {
		name string
		in   fsAskInput
	}{
		{"名单 never", fsAskInput{Skip: "never"}},
		{"名单 today", fsAskInput{Skip: "today"}},
		{"用户点过恢复休息", fsAskInput{Ignored: true}},
	}
	for _, c := range cases {
		st := &fsAskState{}
		in := c.in
		in.Supported = true
		in.Busy = true
		in.Geo = fsGeoFull("vlc.exe", 0x20)
		in.Now = 5000
		for i := 0; i < 5; i++ {
			in.Now += 3000
			if got := st.step(in); got != fsAskStepNone {
				t.Fatalf("%s：不该有任何动作，第 %d 拍得到 %v", c.name, i+1, got)
			}
		}
		if st.pending {
			t.Fatalf("%s：不该留下询问", c.name)
		}
	}
}

// 投屏 / 勿扰生效时不能弹窗，只能直接暂停（屏幕上不能多出窗）。
func TestFSAskRiskyPausesWithoutAsking(t *testing.T) {
	st := &fsAskState{}
	in := fsAskInput{Supported: true, Busy: true, Geo: fsGeoFull("zoom.exe", 0x30), Risky: true, Now: 1000}
	st.step(in)
	if got := st.step(in); got != fsAskStepPauseNow {
		t.Fatalf("压制中应直接暂停，得到 %v", got)
	}
	if st.pending {
		t.Fatal("压制中绝不能发起询问")
	}
}

// 几何探测不可用（Supported=false）时退回旧行为：QUNS=2 直接暂停，绝不“读不到就放行”。
func TestFSAskGeometryUnavailableFallsBackToPause(t *testing.T) {
	st := &fsAskState{}
	in := fsAskInput{Supported: true, Busy: true, Geo: windnd.ForegroundInfo{Supported: false}, Now: 1000}
	st.step(in)
	if got := st.step(in); got != fsAskStepPauseNow {
		t.Fatalf("几何不可用应直接暂停，得到 %v", got)
	}
	if st.pending {
		t.Fatal("几何不可用时不该询问（连应用名都没有，无从标记）")
	}
}

// QUNS=2 但前台窗口几何上不是全屏（比如最大化窗口、或系统把演示设置报成 2）：
// 不算全屏段，既不暂停也不询问。这正是不再单信 QUNS 的价值。
func TestFSAskIgnoresNonFullscreenForeground(t *testing.T) {
	st := &fsAskState{}
	in := fsAskInput{Supported: true, Busy: true, Geo: fsGeoWorkArea("chrome.exe", 0x40), Now: 1000}
	for i := 0; i < 6; i++ {
		in.Now += 3000
		if got := st.step(in); got != fsAskStepNone {
			t.Fatalf("第 %d 拍：最大化窗口不算全屏，得到 %v", i+1, got)
		}
	}
}

// QUNS 探测失败 = 没有结论，整拍跳过（不能当成“没有全屏”去收尾，
// 否则一次偶发调用失败就会解锁一段正在进行的询问/暂停）。
func TestFSAskProbeFailureKeepsState(t *testing.T) {
	st := &fsAskState{}
	in := fsAskInput{Supported: true, Busy: true, Geo: fsGeoFull("vlc.exe", 0x50), Now: 1000}
	st.step(in)
	st.step(in) // 已发起询问
	before := *st

	fail := fsAskInput{Supported: false, Now: 9000}
	if got := st.step(fail); got != fsAskStepNone {
		t.Fatalf("探测失败不该有动作，得到 %v", got)
	}
	if st.pending != before.pending || st.segKey != before.segKey || st.deadline != before.deadline {
		t.Fatalf("探测失败不该改动状态：before=%+v after=%+v", before, *st)
	}
}

// 段结束：条件消失要连续两拍才认，且“还在问”与“已答完”两种收场不同。
func TestFSAskSegmentEnd(t *testing.T) {
	// ① 询问中条件消失 → 关窗（悬空的小窗必须跟着段走）
	st := &fsAskState{}
	in := fsAskInput{Supported: true, Busy: true, Geo: fsGeoFull("vlc.exe", 0x60), Now: 1000}
	st.step(in)
	st.step(in)
	gone := in
	gone.Busy = false
	gone.Now = 5000
	if got := st.step(gone); got != fsAskStepNone {
		t.Fatalf("段结束第一拍应去抖，得到 %v", got)
	}
	if got := st.step(gone); got != fsAskStepCloseAsk {
		t.Fatalf("段结束时若还在问，必须关窗，得到 %v", got)
	}
	// 已 reset，再一拍不该再收尾一次
	if got := st.step(gone); got != fsAskStepNone {
		t.Fatalf("关窗后不该重复收尾，得到 %v", got)
	}

	// ② 已答“暂停”后条件消失 → 标准收尾（按策略保持暂停）
	st2 := &fsAskState{}
	st2.step(in)
	st2.step(in)
	st2.pending = false // 模拟用户已答复“暂停”
	if got := st2.step(gone); got != fsAskStepNone {
		t.Fatalf("第一拍去抖，得到 %v", got)
	}
	if got := st2.step(gone); got != fsAskStepClear {
		t.Fatalf("已答完的段结束要走标准收尾，得到 %v", got)
	}
}

// 换了一个全屏窗口 = 新段：先关掉旧询问窗，然后重新问一次（seq 递增，
// 迟到的超时定时器不会误伤新询问）。
func TestFSAskNewWindowAsksAgain(t *testing.T) {
	st := &fsAskState{}
	in := fsAskInput{Supported: true, Busy: true, Geo: fsGeoFull("vlc.exe", 0x70), Now: 1000}
	st.step(in)
	if got := st.step(in); got != fsAskStepAsk {
		t.Fatalf("第一段应发起询问，得到 %v", got)
	}
	firstSeq := st.seq

	next := in
	next.Geo = fsGeoFull("potplayer.exe", 0x80)
	next.Now = 4000
	if got := st.step(next); got != fsAskStepCloseAsk {
		t.Fatalf("换窗口应关掉旧询问窗，得到 %v", got)
	}
	if got := st.step(next); got != fsAskStepNone {
		t.Fatalf("新段进入同样要两拍，得到 %v", got)
	}
	if got := st.step(next); got != fsAskStepAsk {
		t.Fatalf("新窗口要重新问，得到 %v", got)
	}
	if st.seq <= firstSeq {
		t.Fatalf("seq 必须单调递增（迟到的定时器靠它识别），first=%d now=%d", firstSeq, st.seq)
	}
	if st.exe != "potplayer.exe" {
		t.Fatalf("新段的 exe 应换成新窗口的，得到 %q", st.exe)
	}
}

// 同一窗口短暂 Alt-Tab 出去再回来（不足两拍）仍是同一段，不重复问；
// 真正结束一段（连续两拍不成立）之后再进来才算新的全屏段。
func TestFSAskAltTabBackDoesNotReask(t *testing.T) {
	st := &fsAskState{}
	in := fsAskInput{Supported: true, Busy: true, Geo: fsGeoFull("vlc.exe", 0x90), Now: 1000}
	st.step(in)
	st.step(in)
	st.pending = false // 用户答“本次不暂停”

	// 只出去一拍（3 秒）：段没结束
	out := in
	out.Busy = false
	out.Now = 4000
	if got := st.step(out); got != fsAskStepNone {
		t.Fatalf("单拍离开不该收尾，得到 %v", got)
	}

	back := in
	back.Now = 7000
	if got := st.step(back); got != fsAskStepNone {
		t.Fatalf("回到同一窗口仍是同一段，不该再问，得到 %v", got)
	}
	if st.segKey == "" || st.exe != "vlc.exe" {
		t.Fatalf("段不该被清掉：%+v", st)
	}
}

// 段真正结束（连续两拍不在全屏）后再进来 = 新的全屏段，会重新问一次。
// 这是刻意的：用户可能换了片源、也可能上次只是“本次不暂停”。
func TestFSAskRealSegmentEndStartsNewSegment(t *testing.T) {
	st := &fsAskState{}
	in := fsAskInput{Supported: true, Busy: true, Geo: fsGeoFull("vlc.exe", 0x90), Now: 1000}
	st.step(in)
	st.step(in)
	st.pending = false

	out := in
	out.Busy = false
	out.Now = 4000
	st.step(out)
	st.step(out) // 段结束

	back := in
	back.Now = 12000
	if got := st.step(back); got != fsAskStepNone {
		t.Fatalf("新段进入同样要两拍，得到 %v", got)
	}
	if got := st.step(back); got != fsAskStepAsk {
		t.Fatalf("新的全屏段应重新询问，得到 %v", got)
	}
}

// 名单命中但不在全屏段时不该动状态（避免“上一次全屏的答案”影响下一次判断）。
func TestFSAskInactiveInputDoesNotCreateSegment(t *testing.T) {
	st := &fsAskState{}
	in := fsAskInput{Supported: true, Busy: false, Geo: fsGeoFull("vlc.exe", 0xA0), Now: 1000}
	for i := 0; i < 4; i++ {
		in.Now += 3000
		if got := st.step(in); got != fsAskStepNone {
			t.Fatalf("第 %d 拍：没有全屏不该有动作，得到 %v", i+1, got)
		}
	}
	if st.segKey != "" {
		t.Fatalf("不该建段，得到 %q", st.segKey)
	}
}
