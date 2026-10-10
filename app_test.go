package main

import (
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

// 督休专员会话构建的纯逻辑回归（脱离窗口与 Wails）。
//
// 这里钉的是「固定显示」的选人规则：固定显示只看磁贴点选（fixedKey），
// **不看** role.json 里的单条 enabled —— 复选框只在随机 / 顺序模式下出现，
// 若固定路径仍拿 enabled 当门槛，随包 role.json 里 enabled=false 的督休专员
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

// writeRole 落一个督休专员目录（role.json + 假立绘文件；toDataURL 只要求文件可读）
func writeRole(t *testing.T, root, key, name string, enabled bool) {
	t.Helper()
	dir := filepath.Join(root, key)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	roleJSON := `{"name":"` + name + `","enabled":` + boolJSON(enabled) +
		`,"image":"figure.png","geom":{"sizePct":72,"posXPct":4,"posYPct":0,"flip":false}}`
	if err := os.WriteFile(filepath.Join(dir, "role.json"), []byte(roleJSON), 0o644); err != nil {
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

// 固定显示：role.json 里单条 enabled=false 也要照常出场（立绘列表能看到就得能显示）
func TestSupervisorFixedSelectionIgnoresPerRoleEnabled(t *testing.T) {
	a := newSupTestApp(t, `{"supervisor":{"enabled":true,"gaze":true,"selection":"fixed","fixedKey":"sup-a"}}`)
	writeRole(t, a.rolesRoot, "sup-a", "甲", false)
	writeRole(t, a.rolesRoot, "sup-b", "乙", true)

	got := a.buildSupervisorSession(false, "")
	if got == nil {
		t.Fatal("固定显示指定 enabled=false 的督休专员也应出场，得到 nil")
	}
	if got.ImageKey != "sup-a" {
		t.Fatalf("应选中 sup-a，得到 %q", got.ImageKey)
	}
	if got.Image == "" {
		t.Fatal("立绘 data URL 不应为空")
	}
}

// 固定显示：指定的督休专员不存在（已删除）→ 兜底第一个可用督休专员
func TestSupervisorFixedSelectionFallsBackToFirstUsable(t *testing.T) {
	a := newSupTestApp(t, `{"supervisor":{"enabled":true,"gaze":true,"selection":"fixed","fixedKey":"sup-gone"}}`)
	writeRole(t, a.rolesRoot, "sup-a", "甲", false)

	got := a.buildSupervisorSession(false, "")
	if got == nil {
		t.Fatal("fixedKey 失效应兜底到第一个可用督休专员，得到 nil")
	}
	if got.ImageKey != "sup-a" {
		t.Fatalf("应兜底到 sup-a，得到 %q", got.ImageKey)
	}
}

// 顶层总开关关闭时仍然一律不显示（既有语义，钉住防回归）
func TestSupervisorTopLevelSwitchStillGates(t *testing.T) {
	a := newSupTestApp(t, `{"supervisor":{"enabled":false,"gaze":true,"selection":"fixed","fixedKey":"sup-a"}}`)
	writeRole(t, a.rolesRoot, "sup-a", "甲", true)

	if got := a.buildSupervisorSession(false, ""); got != nil {
		t.Fatalf("顶层开关关闭时不应构建督休专员会话，得到 %+v", got)
	}
}
