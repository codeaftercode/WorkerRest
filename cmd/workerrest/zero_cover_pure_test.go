package main

// 纯逻辑函数的判据补齐（2026-11-23 覆盖率收口）。
//
// 这一组函数此前是 0% 且测试文件里零引用 —— 不是"没价值"，而是它们都只有
// 一个调用点、藏在窗口/托盘/更新流程里，被当成"顺带跑的胶水"漏掉了。
// 每条用例都钉一个真实会出错的语义，不写"调用一次不 panic"这种占位断言。
//
// 分开建文件而不塞进各自的测试：这些函数横跨 6 个文件（fs_ask_bindings /
// window_size / bindings_settings / tray / update / app / prebreak），
// 放在一起更便于下次"覆盖率又掉到 0"时被一眼找到。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"workerrest/internal/settings"
	"workerrest/internal/sound"
)

// ---------- nextLocalMidnightMs ----------

// 「到明天」暂停档的失效时刻。用本地 24:00 而不是 now+24h：
// 否则凌晨 1 点选的"今天"会一直有效到第二天凌晨 1 点。
func TestNextLocalMidnightIsLocalZeroOfNextDay(t *testing.T) {
	now := time.Now()
	got := time.UnixMilli(nextLocalMidnightMs())

	if h, m, s := got.Clock(); h != 0 || m != 0 || s != 0 || got.Nanosecond() != 0 {
		t.Fatalf("必须是本地零点，得到 %02d:%02d:%02d.%09d", h, m, s, got.Nanosecond())
	}
	if !got.After(now) {
		t.Fatalf("必须晚于此刻：got=%v now=%v", got, now)
	}
	// 上限 25 小时：跨月/跨年那一跳也不会超过这个宽度，超过说明算成了"两天后"。
	if d := got.Sub(now); d > 25*time.Hour {
		t.Fatalf("距此刻 %v，超过 25 小时，说明跳多了", d)
	}
	// 日期必须是"次日"。time.Date(y,m,d+1) 由 Go 自动规范化，所以月末/年末
	// 这一条同时钉住了跨月与跨年（1/31+1 → 2/1，12/31+1 → 次年 1/1）。
	wantY, wantM, wantD := now.AddDate(0, 0, 1).Date()
	gotY, gotM, gotD := got.Date()
	if gotY != wantY || gotM != wantM || gotD != wantD {
		t.Fatalf("日期应是次日 %04d-%02d-%02d，得到 %04d-%02d-%02d",
			wantY, wantM, wantD, gotY, gotM, gotD)
	}
	// 同一毫秒内连调两次必须同值（纯函数，没有隐藏状态）。
	if a, b := nextLocalMidnightMs(), nextLocalMidnightMs(); a != b {
		t.Fatalf("同一毫秒两次调用不一致：%d vs %d", a, b)
	}
}

// ---------- validWindowSize ----------

// "上次尺寸"的校验：宽度够大、高度够用。只有一边合法时整对作废 ——
// 拿一个合法 + 一个 0 去开窗会得到一扇高度 0 的窗口。
//
// ⚠️ 高度下限是 **可用下限 240**，不是设计下限 600：远程桌面 / 小屏上工作区只有
// 500 出头，用户拖出来的就是那个高度，用 600 判会把正常的记录当脏数据清掉。
func TestValidWindowSizeRejectsHalfValidPairs(t *testing.T) {
	minW, minH := settings.SettingsWindowMinWidth, settings.SettingsWindowMinUsableHeight
	cases := []struct {
		name string
		w, h int
		want bool
	}{
		{"恰好到下限", minW, minH, true},
		{"宽差 1", minW - 1, minH, false},
		{"高差 1", minW, minH - 1, false},
		{"宽合法高 0（旧残留）", minW, 0, false},
		{"宽 0 高合法", 0, minH, false},
		{"负数", -1, -1, false},
		{"很大也合法", 4000, 3000, true},
		// 关键回归：小屏 / 远程会话里 500 出头的窗口高度必须算合法记录
		{"小屏合理高度（500 < 设计下限 600）", minW, 500, true},
	}
	for _, c := range cases {
		if got := validWindowSize(c.w, c.h); got != c.want {
			t.Errorf("%s: validWindowSize(%d,%d)=%v，期望 %v", c.name, c.w, c.h, got, c.want)
		}
	}
}

// ---------- audioFilterPattern ----------

// 音乐对话框的过滤器必须与 sound.Exts 同序同长：少一个扩展名就等于
// 用户看得见文件却选不中（"为什么我的 m4a 是灰的"）。
func TestAudioFilterPatternMatchesSupportedExts(t *testing.T) {
	got := audioFilterPattern()
	parts := strings.Split(got, ";")
	if len(parts) != len(sound.Exts) {
		t.Fatalf("过滤器项数 %d 与 sound.Exts 长度 %d 不一致：%q", len(parts), len(sound.Exts), got)
	}
	for i, e := range sound.Exts {
		if want := "*." + e; parts[i] != want {
			t.Fatalf("第 %d 项是 %q，期望 %q（顺序必须与 sound.Exts 一致）", i, parts[i], want)
		}
	}
	if strings.HasSuffix(got, ";") || strings.Contains(got, ";;") {
		t.Fatalf("过滤器出现空项：%q", got)
	}
}

// ---------- fmtTrayShort ----------

// 托盘/诊断页的紧凑倒计时。取整用 ceil 到秒 —— 剩 0.2 秒要显示 1s 而不是 0s，
// 否则用户会看到"0s"停在那里。分/时边界与"有零头才补秒"是这段唯一会写错的地方。
func TestFmtTrayShortBoundaries(t *testing.T) {
	cases := []struct {
		ms   int64
		want string
	}{
		{0, "0s"},
		{1, "1s"},
		{999, "1s"},
		{1000, "1s"},
		{1001, "2s"},
		{59000, "59s"},
		{59999, "1m"}, // ceil 到 60s，进分钟
		{60000, "1m"},
		{61000, "1m1s"},
		{61100, "1m2s"},
		{3599000, "59m59s"},
		{3600000, "1h"},
		{3601000, "1h1s"},
		{3660000, "1h1m"},
		{3661000, "1h1m1s"},
		{7200000, "2h"},
	}
	for _, c := range cases {
		if got := fmtTrayShort(c.ms); got != c.want {
			t.Errorf("fmtTrayShort(%d)=%q，期望 %q", c.ms, got, c.want)
		}
	}
}

// ---------- updateDirFor ----------

// 自更新目录推导。跑在 stage 副本里时它自己就在 update 目录内（不能再套一层），
// 否则新程序会被放到 update/update，替换环节永远找不到它。
func TestUpdateDirForStageAndInstalled(t *testing.T) {
	app := filepath.Join("C:", "App")
	upd := filepath.Join(app, updateDirName)

	cases := []struct {
		name   string
		runExe string
		want   string
	}{
		{"安装目录里的正式 exe", filepath.Join(app, "rest.exe"), upd},
		{"update 里的 stage 副本", filepath.Join(upd, "rest.stage.exe"), upd},
		{"stage 目录名大小写不同", filepath.Join(app, "UPDATE", "rest.stage.exe"), filepath.Join(app, "UPDATE")},
		// 名字不像 stage：即使在 update 目录里也要再下一层（注释里写明的判据）
		{"update 里的正式 exe", filepath.Join(upd, "rest.exe"), filepath.Join(upd, updateDirName)},
		{"只有文件名", "rest.exe", updateDirName},
	}
	for _, c := range cases {
		if got := updateDirFor(c.runExe); got != c.want {
			t.Errorf("%s: updateDirFor(%q)=%q，期望 %q", c.name, c.runExe, got, c.want)
		}
	}
}

// ---------- mergeUpdateConfig ----------

// 更新包里带的配置只做"补缺"：本地已有的顶层键一律不覆盖，本地没有的才新增。
// 这条判据的分量在并发上 —— 必须走副本 + Save 成功才换指针，不能原地改 Raw()。
func TestMergeUpdateConfigFillsMissingOnlyAndPersists(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "settings.json")

	raw := map[string]any{
		"short": map[string]any{"enabled": true, "intervalMs": float64(600000)},
	}
	a := &App{cfg: settings.New(raw, cfgPath)}

	updDir := filepath.Join(dir, "update")
	if err := os.MkdirAll(updDir, 0o755); err != nil {
		t.Fatalf("建更新目录失败: %v", err)
	}
	// 一份合法补丁：一个全新键 + 一个与本地冲突的键（冲突者必须被丢掉）
	patch := `{"brandNew":{"k":1},"short":{"enabled":false,"intervalMs":1}}`
	if err := os.WriteFile(filepath.Join(updDir, "config.json"), []byte(patch), 0o644); err != nil {
		t.Fatalf("写补丁失败: %v", err)
	}
	// 一份坏文件：必须被跳过且不阻断同目录里的其他文件
	if err := os.WriteFile(filepath.Join(updDir, "broken.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("写坏文件失败: %v", err)
	}

	a.mergeUpdateConfig(updDir)

	got := a.CfgSnapshot().Raw()
	if _, ok := got["brandNew"]; !ok {
		t.Fatalf("新键没有被补进来：%v", got)
	}
	short, _ := got["short"].(map[string]any)
	if short == nil {
		t.Fatalf("short 丢了：%v", got)
	}
	if short["enabled"] != true {
		t.Fatalf("本地已有键被更新包覆盖了：enabled=%v", short["enabled"])
	}
	if v, _ := short["intervalMs"].(float64); v != 600000 {
		t.Fatalf("本地已有键被更新包覆盖了：intervalMs=%v", short["intervalMs"])
	}

	// 落盘：不是只改了内存
	disk, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("读回配置失败（说明根本没保存）: %v", err)
	}
	if !strings.Contains(string(disk), "brandNew") {
		t.Fatalf("磁盘上的配置没有新键，合并只改了内存：%s", disk)
	}

	// 配置未就绪时只记日志，不许 panic（下载流程里配置可能还没起来）
	(&App{}).mergeUpdateConfig(updDir)
}

// ---------- structToMap ----------

// AppInfo 的字段转换走一次 JSON 往返，字段名以 json tag 为准。
// 传进来不可序列化的值时要退化成空 map，而不是把 nil 交给调用方解引用。
func TestStructToMapUsesJSONTagsAndDegradesSafely(t *testing.T) {
	type sample struct {
		A string `json:"a"`
		B int    `json:"b"`
		H string `json:"h,omitempty"`
	}
	got := structToMap(sample{A: "x", B: 2})
	if got["a"] != "x" {
		t.Fatalf("json tag 没生效：%v", got)
	}
	if v, ok := got["b"].(float64); !ok || v != 2 {
		t.Fatalf("数字经 JSON 往返应是 float64(2)，得到 %#v", got["b"])
	}
	if _, ok := got["h"]; ok {
		t.Fatalf("omitempty 字段不该出现：%v", got)
	}

	// chan 不可序列化 → 空 map（不是 nil panic、不是半截数据）
	if m := structToMap(make(chan int)); len(m) != 0 {
		t.Fatalf("不可序列化的值应退化成空 map，得到 %v", m)
	}
	if m := structToMap(nil); len(m) != 0 {
		t.Fatalf("nil 应退化成空 map，得到 %v", m)
	}
}

// ---------- preBreakInfoOf ----------

// 调度器没起来时（冷启动第一拍、退出中）按短休息算，不能返回零值版式 ——
// 渲染层拿到空标题会显示一扇没有文字的窗。
func TestPreBreakInfoOfFallsBackToShortWithoutScheduler(t *testing.T) {
	a, _ := newPreBreakApp(t, customWaterCfg)
	a.sched = nil

	got := a.preBreakInfoOf()
	want := a.preBreakInfoOfType("short")
	if got.Title != want.Title || got.CountdownTail != want.CountdownTail || got.Custom != want.Custom {
		t.Fatalf("无调度器时应按短休息算：got=%+v want=%+v", got, want)
	}
	if got.Title == "" {
		t.Fatalf("兜底标题不许为空")
	}

	// 有调度器时按 StateCopy().NextType 取，与直接按该类型算的结果一致
	a2, s := newPreBreakApp(t, customWaterCfg)
	next := s.StateCopy().NextType
	got2 := a2.preBreakInfoOf()
	want2 := a2.preBreakInfoOfType(next)
	if got2.Title != want2.Title || got2.Custom != want2.Custom {
		t.Fatalf("应按 NextType=%q 取内容：got=%+v want=%+v", next, got2, want2)
	}
}

// ---------- hasAutostartFlag ----------

// 开机自启动靠命令行参数识别。必须精确匹配：写成前缀匹配会把
// `--autostart=1` 之类的变体也认成自启动，进而改变启动行为（不显示设置窗）。
func TestHasAutostartFlagMatchesExactly(t *testing.T) {
	old := os.Args
	defer func() { os.Args = old }()

	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"没有参数", []string{"rest.exe"}, false},
		{"只有一个参数", []string{"rest.exe", autostartFlag}, true},
		{"夹在中间", []string{"rest.exe", "--x", autostartFlag, "--y"}, true},
		{"带等号的变体不算", []string{"rest.exe", autostartFlag + "=1"}, false},
		{"大小写不同不算", []string{"rest.exe", "--AUTOSTART"}, false},
		{"前缀相同但更长不算", []string{"rest.exe", "--autostart-now"}, false},
	}
	for _, c := range cases {
		os.Args = c.args
		if got := hasAutostartFlag(); got != c.want {
			t.Errorf("%s: hasAutostartFlag(%v)=%v，期望 %v", c.name, c.args, got, c.want)
		}
	}
}
