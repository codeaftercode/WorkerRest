package main

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// 归一化的快速回归。
//
// 为什么还需要它（已经有 scripts/verify_settings_parity.py 与上一版 对拍）：
// 那个对拍要起 node + 无头服务，跑一轮十几秒；这里是纯函数、毫秒级，`go test` 顺手就跑了。
// 两边分工：**这里拦住「规则写错了」**（老配置迁移、越界夹紧、种子条目），
// **对拍拦住「与上一版 不一致」**（包括这里没想到的规则）。
// 真实的老配置样本见 verify_settings_parity.py 的 legacy_fixture / rich_fixture。

func TestNormalizeLoadLegacyMigration(t *testing.T) {
	raw := map[string]any{
		"appName":        "  老  配置  ",
		"screens":        "main", // 旧键：休息窗口显示位置
		"idlePause":      true,   // 旧键：空闲暂停开关（功能已删）
		"startMinimized": "yes",  // 非布尔
		"short": map[string]any{
			"mode":   "bogus",
			"strict": true,
			"slots": []any{
				map[string]any{"hour": 99.0, "minute": 99.0, "repeat": "weekly", "days": []any{}},
				map[string]any{"hour": -5.0, "minute": "30"},
				"不是对象",
			},
		},
		"appearance": map[string]any{"opacity": 0.92, "widthPct": 0.0},
		"supervisor": map[string]any{"level": 3.0, "selection": "bogus"},
		"sound": map[string]any{
			"mode": "bogus", "shortStart": "old_start.wav", "longEnd": "old_end.wav",
		},
		"takeout":  map[string]any{"leadSec": 999.0, "postponeMin": 999.0},
		"appPause": nil,
		"workTime": map[string]any{"start": "09:00"},
		"customBreaks": []any{
			map[string]any{"id": "short", "name": "  喝水  ", "config": map[string]any{"durationMs": 0.0}},
			map[string]any{"id": "standup"},
			"不是对象",
		},
		"reminderMessages": map[string]any{
			"short": map[string]any{"order": "bogus", "fixedId": "不存在", "items": []any{}},
			"long":  map[string]any{"items": []any{map[string]any{"text": "没有 id"}}},
		},
	}
	installIDCreated, presetSeeded := normalizeLoad(raw)
	if !installIDCreated {
		t.Fatal("老配置没有 installId，应当补生成")
	}
	if !presetSeeded {
		t.Fatal("老配置没有 seeded 标记，应当种入预置条目")
	}
	if got := raw["installId"].(string); got == "" {
		t.Fatal("installId 应已写入")
	}

	assertAbsent(t, raw, "screens", "idlePause", "workTime")

	// 旧键 screens=main ⇒ allScreens=false；startMinimized 非布尔 ⇒ false
	if raw["allScreens"] != false {
		t.Errorf("screens=main 应迁移成 allScreens=false，得到 %v", raw["allScreens"])
	}
	if raw["startMinimized"] != false {
		t.Errorf("非布尔的 startMinimized 应回退 false，得到 %v", raw["startMinimized"])
	}

	// appName 归一化**不发生在加载路径**（上一版 也不做），原样保留
	if got := raw["appName"]; got != "  老  配置  " {
		t.Errorf("加载路径不应改写 appName，得到 %q", got)
	}

	// short：mode 归一到 countdown、strict 清掉、时刻表夹紧
	short := raw["short"].(map[string]any)
	assertAbsent(t, short, "strict")
	if short["mode"] != "countdown" {
		t.Errorf("非法 mode 应回退 countdown，得到 %v", short["mode"])
	}
	slots := short["slots"].([]any)
	if len(slots) != 2 {
		t.Fatalf("非对象的时刻条目应被剔除，剩 %d 条", len(slots))
	}
	s0 := slots[0].(map[string]any)
	if s0["hour"] != 23 || s0["minute"] != 59 {
		t.Errorf("越界时刻应夹紧到 23:59，得到 %v:%v", s0["hour"], s0["minute"])
	}
	if days := s0["days"].([]any); len(days) != 5 {
		t.Errorf("weekly 且没勾星期应补工作日（5 天），得到 %v", days)
	}
	s1 := slots[1].(map[string]any)
	if s1["hour"] != 0 || s1["minute"] != 30 {
		t.Errorf("负数小时应夹到 0、字符串分钟应解析成 30，得到 %v:%v", s1["hour"], s1["minute"])
	}

	// appearance：opacity 换算成 transparency（默认已有 transparency ⇒ 只删旧键）
	ap := raw["appearance"].(map[string]any)
	assertAbsent(t, ap, "opacity")

	// supervisor：level 清掉、selection 归一到 fixed、gaze 恒 true
	sup := raw["supervisor"].(map[string]any)
	assertAbsent(t, sup, "level")
	if sup["selection"] != "fixed" {
		t.Errorf("非法 selection 应回退 fixed，得到 %v", sup["selection"])
	}
	if sup["gaze"] != true {
		t.Errorf("视线跟随应恒为 true，得到 %v", sup["gaze"])
	}

	// sound：旧键合并进 sound 并删除；mode 归一；预置音乐种入
	snd := raw["sound"].(map[string]any)
	assertAbsent(t, snd, "shortStart", "longEnd")
	if snd["mode"] != "single" {
		t.Errorf("非法 mode 应回退 single，得到 %v", snd["mode"])
	}
	music := snd["music"].([]any)
	if len(music) != len(builtinMusic) {
		t.Errorf("应种入 %d 条内置提示音，得到 %d", len(builtinMusic), len(music))
	}

	// takeout：leadSec 0–300、postponeMin 0–120（0 = 不显示「推迟」按钮）
	tk := raw["takeout"].(map[string]any)
	if tk["leadSec"] != float64(300) || tk["postponeMin"] != float64(120) {
		t.Errorf("外卖提前通知秒数应夹到 300，得到 %v / %v", tk["leadSec"], tk["postponeMin"])
	}

	// appPause：null ⇒ 空配置 + 种入 8 条内置会议软件
	apz := raw["appPause"].(map[string]any)
	if apz["seeded"] != true {
		t.Error("appPause 应被种入并置 seeded=true")
	}
	if n := len(apz["items"].([]any)); n != len(appPausePresets) {
		t.Errorf("应种入 %d 条内置应用，得到 %d", len(appPausePresets), n)
	}

	// customBreaks：非法条目剔除、id 补 cb- 前缀且不许占用内置身份、name 截 24
	cbs := raw["customBreaks"].([]any)
	if len(cbs) != 2 {
		t.Fatalf("非对象条目应被剔除，剩 %d 条", len(cbs))
	}
	cb0 := cbs[0].(map[string]any)
	if id := cb0["id"].(string); id == "short" || !hasPrefix(id, customBreakPrefix) {
		t.Errorf("占用了内置身份的 id 应重新发号并带 %s 前缀，得到 %q", customBreakPrefix, id)
	}
	if cb0["name"] != "喝水" {
		t.Errorf("名字应去首尾空白，得到 %q", cb0["name"])
	}
	if cb1 := cbs[1].(map[string]any); cb1["id"] != customBreakPrefix+"standup" {
		t.Errorf("缺前缀的 id 应补 cb-，得到 %v", cb1["id"])
	}
	// config 缺字段时用出厂配置补齐
	cfg0 := cb0["config"].(map[string]any)
	if cfg0["intervalMs"] != float64(10*60_000) {
		t.Errorf("自定义提醒的 config 应深合并出厂配置，intervalMs=%v", cfg0["intervalMs"])
	}

	// reminderMessages：order 归位、fixedId 失焦置空、预置条目种入
	rm := raw["reminderMessages"].(map[string]any)
	shortMsg := rm["short"].(map[string]any)
	if shortMsg["order"] != "fixed" {
		t.Errorf("非法 order 应回退 fixed，得到 %v", shortMsg["order"])
	}
	if shortMsg["fixedId"] != "" {
		t.Errorf("fixedId 指不到真实条目应置空，得到 %v", shortMsg["fixedId"])
	}
	if n := len(shortMsg["items"].([]any)); n != len(builtinShortMessages) {
		t.Errorf("空池应种入 %d 条预置提醒内容，得到 %d", len(builtinShortMessages), n)
	}
	longMsg := rm["long"].(map[string]any)
	if n := len(longMsg["items"].([]any)); n != 1 {
		t.Errorf("已有升级带来的条目时应只置 seeded、不再种入，得到 %d 条", n)
	}
}

func TestNormalizeLoadIsIdempotent(t *testing.T) {
	// 连跑两次归一化必须收敛：第二次不应再有任何变化（否则每次启动都会回写配置）
	raw := defaultSettingsMap()
	normalizeLoad(raw)
	first, _ := json.Marshal(raw)

	again := defaultSettingsMap()
	normalizeLoad(again)
	// 用第一轮的结果当输入再跑一遍
	var feed map[string]any
	if err := json.Unmarshal(first, &feed); err != nil {
		t.Fatal(err)
	}
	installIDCreated, presetSeeded := normalizeLoad(feed)
	if installIDCreated || presetSeeded {
		t.Error("已归一化的配置再跑一次不该再补 installId / 种条目")
	}
	second, _ := json.Marshal(feed)
	if string(first) != string(second) {
		t.Errorf("归一化不收敛（第二次仍改动了配置）\n第一次: %s\n第二次: %s", first, second)
	}
}

func TestDeepMergeReplacesArrays(t *testing.T) {
	// 数组整体替换、不逐项合并：用户删掉的条目不能被默认值补回来
	base := map[string]any{"items": []any{1.0, 2.0, 3.0}, "keep": "x"}
	over := map[string]any{"items": []any{}}
	got := deepMerge(base, over).(map[string]any)
	if n := len(got["items"].([]any)); n != 0 {
		t.Errorf("数组应整体替换成空，得到 %d 项", n)
	}
	if got["keep"] != "x" {
		t.Error("未覆盖的键应保留")
	}
	// over 不是普通对象时原样返回（含 nil）
	if v := deepMerge(base, nil); v != nil {
		t.Errorf("over 为 nil 应原样返回 nil，得到 %v", v)
	}
}

// TestSaveFirstRunPortableFirst 首次运行（无来源配置）Save 时，落点必须是便携优先候选
// （<运行目录>/config/settings.json），而不是 APPDATA 兜底。这是便携目录「数据跟着程序走」
// 的关键 —— 若落到 APPDATA，解压到别处的便携目录会各写各的、互不认账。
func TestSaveFirstRunPortableFirst(t *testing.T) {
	root := t.TempDir()
	resourcesRootHintFn = func() string { return root }
	defer func() { resourcesRootHintFn = nil }()

	// 隔离环境变量：不能让它读到用户真实的 APPDATA / WR_SETTINGS_PATH
	oldAppdata := os.Getenv("APPDATA")
	oldWrPath := os.Getenv("WR_SETTINGS_PATH")
	os.Setenv("APPDATA", filepath.Join(root, "roaming"))
	os.Unsetenv("WR_SETTINGS_PATH")
	defer func() {
		os.Setenv("APPDATA", oldAppdata)
		if oldWrPath != "" {
			os.Setenv("WR_SETTINGS_PATH", oldWrPath)
		}
	}()

	s := &Settings{raw: map[string]any{"installId": "x"}, path: ""}
	if err := s.Save(); err != nil {
		t.Fatalf("Save 失败：%v", err)
	}
	want := filepath.Join(root, "config", "settings.json")
	if s.path != want {
		t.Fatalf("首次落点 = %q，应为便携优先 %q", s.path, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("便携落点文件没写出来：%v", err)
	}
}

func assertAbsent(t *testing.T, m map[string]any, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if _, ok := m[k]; ok {
			t.Errorf("旧键 %q 应被清理，实际还在：%v", k, m[k])
		}
	}
}

func hasPrefix(s, p string) bool {
	return len(s) >= len(p) && s[:len(p)] == p
}

// TestUpdateSites 验证更新源地址过滤：只保留 http/https，丢空串/非法 scheme/非数组。
func TestUpdateSites(t *testing.T) {
	cases := []struct {
		raw  map[string]any
		want []string
	}{
		{map[string]any{}, nil},
		{map[string]any{"sites": "not-an-array"}, nil},
		{
			map[string]any{"sites": []any{
				"http://a/version.json",
				"https://b/version.json",
				"ftp://c/x", // 非法 scheme
				"",                  // 空串
				"  http://d/x.json", // 带空白仍保留（调用方 trim 后下发）
			}},
			[]string{"http://a/version.json", "https://b/version.json", "http://d/x.json"},
		},
	}
	for _, c := range cases {
		s := &Settings{raw: c.raw, path: ""}
		got := s.UpdateSites()
		if len(got) != len(c.want) {
			t.Errorf("UpdateSites(%v) 长度=%d，期望 %d（%v）", c.raw, len(got), len(c.want), got)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("UpdateSites(%v)[%d]=%q，期望 %q", c.raw, i, got[i], c.want[i])
			}
		}
	}
}

// TestSetSitesReplacesLocal 验证远程 version.json 下发的 sites 是「整体替换」本地 sites，
// 而不是合并追加；且落盘形状为 []any 字符串，UpdateSites 能原样读回。
func TestSetSitesReplacesLocal(t *testing.T) {
	// 本地原本有一组 sites
	s := &Settings{
		raw: map[string]any{
			"sites": []any{"http://old-1/version.json", "http://old-2/version.json"},
		},
		path: "",
	}
	// 远程下发一组新的 sites（含空白，应原样写入、由 UpdateSites 在读取时 trim）
	s.SetSites([]string{"https://new-1/version.json", "  https://new-2/version.json  "})

	stored, ok := s.raw["sites"].([]any)
	if !ok {
		t.Fatalf("sites 应以 []any 存储，实际 %T", s.raw["sites"])
	}
	if len(stored) != 2 {
		t.Fatalf("sites 应被整体替换为 2 项，实际 %d 项", len(stored))
	}
	if _, ok := stored[0].(string); !ok {
		t.Errorf("sites 元素应为字符串，实际 %T", stored[0])
	}

	// 读取回来：旧的两项应彻底消失，新的两项（trim 后）生效
	got := s.UpdateSites()
	want := []string{"https://new-1/version.json", "https://new-2/version.json"}
	if len(got) != len(want) {
		t.Fatalf("覆盖后 UpdateSites 长度=%d，期望 %d（%v）", len(got), len(want), got)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("UpdateSites()[%d]=%q，期望 %q", i, got[i], want[i])
		}
	}
}

// 全新 / 老配置 singleId 为空时，normalizeSound 应兜底指向第一首「启用」的提示音。
// 否则默认 single 模式下会「列表里有音、真休息却静音、只有试听能响」（用户报过的坑）。
func TestNormalizeSoundDefaultsSingleId(t *testing.T) {
	// 1) 全新配置：sound 段只有 mode，没 music / singleId / seeded
	raw := map[string]any{"sound": map[string]any{"mode": "single"}}
	normalizeSound(raw)
	snd := raw["sound"].(map[string]any)
	if snd["seeded"] != true {
		t.Fatal("首次应种入内置音并置 seeded=true")
	}
	if got := len(snd["music"].([]any)); got != len(builtinMusic) {
		t.Fatalf("应种入 %d 条内置音，得到 %d", len(builtinMusic), got)
	}
	if got := jsStr(snd["singleId"]); got != builtinMusic[0].id {
		t.Fatalf("全新安装 singleId 应默认指向第一首内置音 %q，得到 %q", builtinMusic[0].id, got)
	}

	// 2) 老配置（seeded=true）singleId 为空、但有启用项：兜底指向第一首启用的
	raw2 := map[string]any{"sound": map[string]any{
		"seeded": true, "mode": "single", "singleId": "",
		"music": []any{map[string]any{"id": "x1", "file": "a.wav", "name": "A", "enabled": true}},
	}}
	normalizeSound(raw2)
	if got := jsStr(raw2["sound"].(map[string]any)["singleId"]); got != "x1" {
		t.Fatalf("singleId 空但有启用项应兜底指向 x1，得到 %q", got)
	}

	// 3) 全部停用：尊重用户选择，保持静音（singleId 仍空）
	raw3 := map[string]any{"sound": map[string]any{
		"seeded": true, "mode": "single", "singleId": "",
		"music": []any{map[string]any{"id": "x1", "file": "a.wav", "name": "A", "enabled": false}},
	}}
	normalizeSound(raw3)
	if got := jsStr(raw3["sound"].(map[string]any)["singleId"]); got != "" {
		t.Fatalf("全部停用应尊重用户选择保持 singleId 空，得到 %q", got)
	}
}

// TestAppearanceTransparencyDefault 提醒窗口透明度的缺省值。
// 用户 2026-10-23 定为 0.05，且滑条步长 0.01 —— 这里盯两处容易各自漂移的值：
// 代码兜底（AppearanceCfg 的 s.Num 缺省）与出厂默认（内嵌 default_settings.json）。
func TestAppearanceTransparencyDefault(t *testing.T) {
	const want = 0.05
	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

	// 1) 配置里没有 appearance.transparency 时走代码兜底
	s := &Settings{raw: map[string]any{}}
	if got := s.AppearanceCfg().Transparency; !near(got, want) {
		t.Fatalf("缺键时透明度缺省应为 %v，实得 %v", want, got)
	}

	// 2) 出厂默认（内嵌 JSON）必须同值，否则新装用户与老配置用户看到的透明度不一致
	def := defaultSettingsMap()
	if got := (&Settings{raw: def}).AppearanceCfg().Transparency; !near(got, want) {
		t.Fatalf("出厂默认透明度应为 %v，实得 %v", want, got)
	}

	// 3) 越界仍夹紧在 0 ~ 0.5（滑条范围，与前端 min/max 一致）
	for _, c := range []struct{ in, out float64 }{{-1, 0}, {0.9, 0.5}} {
		raw := map[string]any{"appearance": map[string]any{"transparency": c.in}}
		if got := (&Settings{raw: raw}).AppearanceCfg().Transparency; !near(got, c.out) {
			t.Fatalf("transparency=%v 应夹紧到 %v，实得 %v", c.in, c.out, got)
		}
	}
}

// TestEmbeddedDefaultMatchesFile 内嵌的出厂默认必须与仓库根的 default_settings.json 一致。
//
// 两份默认值分别喂给两条路径：`//go:embed` 的这份是「恢复默认设置」的数据源；
// 仓库根的这份被 make_portable.py 复制成便携包的 config/settings.json（新装首跑读它）。
// 一旦分叉，用户首装看到的值与点「恢复默认」后的值就不一样，而且界面上完全看不出来。
// go:embed 取的是编译那一刻的快照，这条用例把快照钉在工作区文件上。
func TestEmbeddedDefaultMatchesFile(t *testing.T) {
	disk, err := os.ReadFile("default_settings.json")
	if err != nil {
		t.Fatalf("读 default_settings.json 失败: %v", err)
	}
	var fromDisk, fromEmbed any
	if err := json.Unmarshal(disk, &fromDisk); err != nil {
		t.Fatalf("default_settings.json 解析失败: %v", err)
	}
	if err := json.Unmarshal(defaultSettingsJSON, &fromEmbed); err != nil {
		t.Fatalf("内嵌默认解析失败: %v", err)
	}
	// 比语义而非字节：CRLF/LF、缩进差异不算分叉（embed 存的是编译时的工作区字节）
	if !reflect.DeepEqual(fromDisk, fromEmbed) {
		t.Fatal("内嵌的出厂默认与 default_settings.json 内容不一致（改动后没重新编译？）")
	}
}
