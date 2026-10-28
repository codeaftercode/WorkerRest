package main

import (
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

// 督休专员会话构建的纯逻辑回归（脱离窗口与 Wails）。
//
// 这里钉的是「固定显示」的选人规则：固定显示只看磁贴点选（fixedRole），
// **不看** meta.json 里的单条 enabled —— 复选框只在随机 / 顺序模式下出现，
// 若固定路径仍拿 enabled 当门槛，随包 meta.json 里 enabled=false 的督休专员
// 就会出现「立绘列表看得到、休息时不显示」的劈叉（真机发布踩过）。

// newSupTestApp 造一个最小 App，rolesRoot 指向 t.TempDir()
func newSupTestApp(t *testing.T, cfgJSON string) *App {
	t.Helper()
	t.Setenv("WR_STATS_PATH", filepath.Join(t.TempDir(), "stats.json"))
	return &App{
		cfg:       testSettings(t, cfgJSON),
		rolesRoot: t.TempDir(),
		rng:       rand.New(rand.NewSource(1)),
		cursor:    make(map[string]int),
		idle:      NewIdleState(),
	}
}

// writeRole 落一个督休专员目录（meta.json + 假立绘文件；toDataURL 只要求文件可读）
func writeRole(t *testing.T, root, key, name string, enabled bool) {
	t.Helper()
	dir := filepath.Join(root, key)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	roleJSON := `{"name":"` + name + `","enabled":` + boolJSON(enabled) +
		`,"image":"figure.png","geom":{"sizePct":72,"posXPct":4,"posYPct":0,"flip":false}}`
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(roleJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "figure.png"), []byte("fake-png-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func boolJSON(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// 固定显示：meta.json 里单条 enabled=false 也要照常出场（立绘列表能看到就得能显示）
func TestSupervisorFixedSelectionIgnoresPerRoleEnabled(t *testing.T) {
	a := newSupTestApp(t, `{"supervisor":{"enabled":true,"gaze":true,"selection":"fixed","fixedRole":"sup-a"}}`)
	writeRole(t, a.rolesRoot, "sup-a", "sup-a", false)
	writeRole(t, a.rolesRoot, "sup-b", "sup-b", true)

	got := a.buildSupervisorSession(false, "")
	if got == nil {
		t.Fatal("固定显示指定 enabled=false 的督休专员也应出场，得到 nil")
	}
	if got.RoleName != "sup-a" {
		t.Fatalf("应选中 sup-a，得到 %q", got.RoleName)
	}
	if got.Image == "" {
		t.Fatal("立绘 data URL 不应为空")
	}
}

// 固定显示：指定的督休专员不存在（已删除）→ 兜底第一个可用督休专员
func TestSupervisorFixedSelectionFallsBackToFirstUsable(t *testing.T) {
	a := newSupTestApp(t, `{"supervisor":{"enabled":true,"gaze":true,"selection":"fixed","fixedRole":"sup-gone"}}`)
	writeRole(t, a.rolesRoot, "sup-a", "sup-a", false)

	got := a.buildSupervisorSession(false, "")
	if got == nil {
		t.Fatal("fixedRole 失效应兜底到第一个可用督休专员，得到 nil")
	}
	if got.RoleName != "sup-a" {
		t.Fatalf("应兜底到 sup-a，得到 %q", got.RoleName)
	}
}

// 顶层总开关关闭时仍然一律不显示（既有语义，钉住防回归）
func TestSupervisorTopLevelSwitchStillGates(t *testing.T) {
	a := newSupTestApp(t, `{"supervisor":{"enabled":false,"gaze":true,"selection":"fixed","fixedRole":"sup-a"}}`)
	writeRole(t, a.rolesRoot, "sup-a", "sup-a", true)

	if got := a.buildSupervisorSession(false, ""); got != nil {
		t.Fatalf("顶层开关关闭时不应构建督休专员会话，得到 %+v", got)
	}
}

// ---------- 预览窗「有时不显示督休专员」的回归（2026-10-25） ----------
//
// 真机现象：在立绘库里切换督休专员后点「预览」，偶尔出现的预览窗没有督休专员，
// 不改任何配置再点一次又能显示。根因是这条链路上的几处「会话 ↔ 窗口」没有绑定：
//   · 设置页传来过期的 forceName（列表刚重排 / 刚改名）时，会话构建直接返回 nil；
//   · 首选立绘读不出来时同样直接返回 nil（窗口只剩背景）；
//   · ShowBreakWindow「有窗口就复用」把上一次会话的页面留下来；
//   · 上一次会话的窗口迟到一步 WindowClosing，把刚建好的新会话清掉。
// 下面把可直接验的四条钉住。

// forceName 未命中（设置页传来过期的名字）→ 回退固定项，而不是给一个没有督休专员的空壳
func TestSupervisorUnknownForceNameFallsBackToFixed(t *testing.T) {
	a := newSupTestApp(t, `{"supervisor":{"enabled":true,"gaze":true,"selection":"fixed","fixedRole":"sup-a"}}`)
	writeRole(t, a.rolesRoot, "sup-a", "sup-a", true)
	writeRole(t, a.rolesRoot, "sup-b", "sup-b", true)

	got := a.buildSupervisorSession(true, "sup-gone")
	if got == nil {
		t.Fatal("forceName 失效时应回退固定项，得到 nil（预览窗会变成没有督休专员的空壳）")
	}
	if got.RoleName != "sup-a" {
		t.Fatalf("应回退固定项 sup-a，得到 %q", got.RoleName)
	}
}

// forceName 与 fixedRole 都失效 → 第一个可用督休专员（有立绘就得出场）
func TestSupervisorUnknownForceNameAndFixedFallsBackToFirst(t *testing.T) {
	a := newSupTestApp(t, `{"supervisor":{"enabled":true,"gaze":true,"selection":"fixed","fixedRole":"sup-gone"}}`)
	writeRole(t, a.rolesRoot, "sup-a", "sup-a", true)

	got := a.buildSupervisorSession(true, "sup-gone2")
	if got == nil {
		t.Fatal("两个名字都失效时应兜底到第一个可用督休专员，得到 nil")
	}
	if got.RoleName != "sup-a" {
		t.Fatalf("应兜底到 sup-a，得到 %q", got.RoleName)
	}
}

// 首选督休专员的立绘读不出来 → 换下一个能读的，不要退化成空壳
func TestPickRoleWithImageSkipsUnreadableFigure(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "figure.png")
	if err := os.WriteFile(good, []byte("fake-png-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	usable := []Role{
		{Name: "bad", FigurePath: filepath.Join(dir, "missing.png")}, // 文件不存在 → toDataURL 空串
		{Name: "ok", FigurePath: good},
	}
	picked, image := pickRoleWithImage(usable, &usable[0])
	if picked == nil || picked.Name != "ok" {
		t.Fatalf("应跳过读不出立绘的角色，选中 ok，得到 %+v", picked)
	}
	if image == "" {
		t.Fatal("立绘 data URL 不应为空")
	}
	// 全读不出来时返回 nil（调用方据此退回无督休专员版式）
	if p, img := pickRoleWithImage(usable[:1], &usable[0]); p != nil || img != "" {
		t.Fatalf("一张都读不出来时应返回 nil/空，得到 %+v %q", p, img)
	}
}

// 现有窗口能不能复用：只有「就是为当前会话建的」才行 —— 否则会把上一次会话的
// 页面（旧立绘 / 旧倒计时）留在屏幕上。
func TestBreakWinReusable(t *testing.T) {
	cases := []struct {
		winSess, curSess string
		n                int
		want             bool
	}{
		{"s1", "s1", 1, true},  // 同一会话：复用（重复点「立即休息」不叠窗）
		{"s1", "s2", 1, false}, // 会话已换：必须重建，否则看到的是上一次的页面
		{"", "s1", 1, false},   // 没记录会话：保守重建
		{"s1", "s1", 0, false}, // 没有窗口：当然谈不上复用
		{"", "", 1, false},     // 没有会话：不认
	}
	for _, c := range cases {
		if got := breakWinReusable(c.winSess, c.curSess, c.n); got != c.want {
			t.Fatalf("breakWinReusable(%q,%q,%d)=%v，期望 %v", c.winSess, c.curSess, c.n, got, c.want)
		}
	}
}

// 关窗口后要不要清会话：迟到的（上一次会话的）WindowClosing 绝不能清掉新会话，
// 否则新窗口首帧 breakCurrent() 拿到 nil → 只剩背景的空窗。
func TestBreakSessEndsOn(t *testing.T) {
	cases := []struct {
		remaining       int
		closedSess, cur string
		want            bool
	}{
		{1, "s1", "s1", false}, // 多屏还有窗口
		{0, "s2", "s1", false}, // 关的是上一次会话的窗（迟到事件）
		{0, "s1", "s1", true},  // 当前会话的最后一个窗口被关：会话结束
		{0, "s1", "", true},    // 已经没有会话：照常（幂等）
		{0, "", "s1", false},   // 认不出会话来源：保守不清
	}
	for _, c := range cases {
		if got := breakSessEndsOn(c.remaining, c.closedSess, c.cur); got != c.want {
			t.Fatalf("breakSessEndsOn(%d,%q,%q)=%v，期望 %v", c.remaining, c.closedSess, c.cur, got, c.want)
		}
	}
}
