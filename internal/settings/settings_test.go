package settings

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"workerrest/internal/jsonx"
	"workerrest/internal/role"
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
	configKeyCreated, presetSeeded, _, _ := normalizeLoad(raw)
	if !configKeyCreated {
		t.Fatal("老配置没有固定键，应当补生成")
	}
	if !presetSeeded {
		t.Fatal("老配置没有 seeded 标记，应当种入预置条目")
	}
	if got := raw["installId"].(string); got == "" {
		t.Fatal("固定键应已写入")
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

	// supervisor：level 清掉、selection 归一到 random（缺省口径）、gaze 恒 true
	sup := raw["supervisor"].(map[string]any)
	assertAbsent(t, sup, "level")
	if sup["selection"] != "random" {
		t.Errorf("非法 selection 应回退 random，得到 %v", sup["selection"])
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

	// appPause：null ⇒ 空配置 + 种入内置会议软件（条数以 appPausePresets 为准，不写死）
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
	configKeyCreated, presetSeeded, idsRenumbered, _ := normalizeLoad(feed)
	if configKeyCreated || presetSeeded {
		t.Error("已归一化的配置再跑一次不该再补固定键 / 种条目")
	}
	if idsRenumbered {
		t.Error("已归一化的配置再跑一次不该再给条目重新编号")
	}
	second, _ := json.Marshal(feed)
	if string(first) != string(second) {
		t.Errorf("归一化不收敛（第二次仍改动了配置）\n第一次: %s\n第二次: %s", first, second)
	}
}

// TestPruneSuitePresets 早期版本把钉钉 / 企业微信 / 飞书 / WeLink 当成会议软件种进了
// 「应用暂停」清单，现在只留以会议为主的客户端。这条用例钉两件事：
//  1. 只摘「软件自己种的」条目（id 恰好是 preset-<小写 exe> 且 exe 原样）；用户手工
//     添加的、改过进程名的，一律保留 —— 那是用户的决定，不是历史包袱；
//  2. 摘完幂等：第二次归一化不再报告「有改动」，否则每次启动都会回写配置。
func TestPruneSuitePresets(t *testing.T) {
	item := func(id, exe, name string) map[string]any {
		return map[string]any{"id": id, "exe": exe, "name": name, "enabled": true}
	}
	raw := map[string]any{
		"appPause": map[string]any{
			"enabled": true,
			"seeded":  true,
			// 顺序照真实配置：用户自己的条目在前，种子追加在末尾（normalizeAppPause 按进程名去重，
			// 保前面的）。公司自己搭的钉钉也走 DingTalk.exe，绝不能因为同名被一起摘掉。
			"items": []any{
				item("app-7f3a", "DingTalk.exe", "公司的钉钉"),
				item("preset-dingtalk.exe", "DingTalk.exe", "钉钉"),
				item("preset-wxwork.exe", "WXWork.exe", "企业微信"),
				item("preset-feishu.exe", "Feishu.exe", "飞书"),
				item("preset-welink.exe", "WeLink.exe", "华为云 WeLink"),
				item("preset-zoom.exe", "Zoom.exe", "Zoom"), // 会议客户端：留
			},
		},
	}
	if _, _, _, pruned := normalizeLoad(raw); !pruned {
		t.Fatal("带综合办公套件预设的配置应报告「摘除了条目」")
	}
	var got []string
	for _, it := range raw["appPause"].(map[string]any)["items"].([]any) {
		got = append(got, it.(map[string]any)["id"].(string))
	}
	want := []string{"app-7f3a", "preset-zoom.exe"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("摘除结果不对：得到 %v，期望 %v", got, want)
	}
	if _, _, _, again := normalizeLoad(raw); again {
		t.Error("已摘过的配置再跑一次不该再报告摘除（会每次启动都回写）")
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

// TestUpdateSites 验证更新源地址过滤：**只保留 https**（http 明文源已禁用，
// 回环地址例外 —— 单测的 httptest 就靠它），丢空串/非法 scheme/非数组。
func TestUpdateSites(t *testing.T) {
	cases := []struct {
		raw  map[string]any
		want []string
	}{
		{map[string]any{}, nil},
		{map[string]any{"sites": "not-an-array"}, nil},
		{
			map[string]any{"sites": []any{
				"http://a/version.json", // 明文源：丢弃（审计 BLOCKER-2）
				"https://b/version.json",
				"ftp://c/x",                      // 非法 scheme
				"",                               // 空串
				"  https://d/x.json  ",           // 带空白：trim 后保留
				"http://127.0.0.1:8080/x.json",   // 回环 http：保留（单测/本机调试）
				"http://localhost:9000/ver.json", // 回环 http：保留
			}},
			[]string{"https://b/version.json", "https://d/x.json",
				"http://127.0.0.1:8080/x.json", "http://localhost:9000/ver.json"},
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
	// 远程下发一组新的 sites：带空白要 trim，http 明文项（不可信输入）要丢弃
	s.SetSites([]string{
		"https://new-1/version.json",
		"  https://new-2/version.json  ",
		"http://evil.example/version.json",
		"",
	})

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
	if got := jsonx.JsStr(snd["singleId"]); got != builtinMusic[0].id {
		t.Fatalf("全新安装 singleId 应默认指向第一首内置音 %q，得到 %q", builtinMusic[0].id, got)
	}

	// 2) 老配置（seeded=true）singleId 为空、但有启用项：兜底指向第一首启用的
	raw2 := map[string]any{"sound": map[string]any{
		"seeded": true, "mode": "single", "singleId": "",
		"music": []any{map[string]any{"id": "x1", "file": "a.wav", "name": "A", "enabled": true}},
	}}
	normalizeSound(raw2)
	if got := jsonx.JsStr(raw2["sound"].(map[string]any)["singleId"]); got != "x1" {
		t.Fatalf("singleId 空但有启用项应兜底指向 x1，得到 %q", got)
	}

	// 3) 全部停用：尊重用户选择，保持静音（singleId 仍空）
	raw3 := map[string]any{"sound": map[string]any{
		"seeded": true, "mode": "single", "singleId": "",
		"music": []any{map[string]any{"id": "x1", "file": "a.wav", "name": "A", "enabled": false}},
	}}
	normalizeSound(raw3)
	if got := jsonx.JsStr(raw3["sound"].(map[string]any)["singleId"]); got != "" {
		t.Fatalf("全部停用应尊重用户选择保持 singleId 空，得到 %q", got)
	}
}

// TestAppearanceTransparencyDefault 提醒窗口透明度的缺省值。
// 用户 2026-10-29 定的出厂默认是 0.05（轻微透明）；滑条步长仍是 0.01。
// 这里盯两处容易各自漂移的值：代码兜底（AppearanceCfg 的 s.Num 缺省）与出厂默认（内嵌
// default_settings.json）—— 两处必须同值，否则老配置与新装用户看到的透明度不一致。
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

// TestAppearanceGeometryDefaults 提醒窗口宽高百分比与「全屏显示」的出厂默认：
// 95% / 95% / 关（用户 2026-10-29 定的口径）。与透明度同理，代码兜底与内嵌 JSON 必须同值。
func TestAppearanceGeometryDefaults(t *testing.T) {
	for i, raw := range []map[string]any{{}, defaultSettingsMap()} {
		cfg := (&Settings{raw: raw}).AppearanceCfg()
		if cfg.WidthPct != 95 || cfg.HeightPct != 95 {
			t.Fatalf("第 %d 组：宽高缺省应为 95%%/95%%，得到 %v%%/%v%%", i+1, cfg.WidthPct, cfg.HeightPct)
		}
		if cfg.Fullscreen {
			t.Fatalf("第 %d 组：「全屏显示」缺省应为关", i+1)
		}
	}
}

// TestSupervisorSelectionDefault 立绘显示方式缺省为「随机显示」。
// 三处口径：读取兜底（SupervisorCfg）、归一化非法值（normalizeSupervisorMessages）、
// 出厂 JSON。任一处走「固定显示」，新装用户看到的就与预期不符。
func TestSupervisorSelectionDefault(t *testing.T) {
	if got := (&Settings{raw: map[string]any{}}).SupervisorCfg().Selection; got != "random" {
		t.Fatalf("缺键时显示方式应为 random，得到 %q", got)
	}
	if got := (&Settings{raw: defaultSettingsMap()}).SupervisorCfg().Selection; got != "random" {
		t.Fatalf("出厂默认显示方式应为 random，得到 %q", got)
	}
	raw := map[string]any{"supervisor": map[string]any{"selection": "bogus"}}
	normalizeSupervisorMessages(raw)
	if got := jsonx.JsStr(raw["supervisor"].(map[string]any)["selection"]); got != "random" {
		t.Fatalf("非法 selection 应回退 random，得到 %q", got)
	}
	// 显式三种取值都必须原样保留，不能被兜底吞掉
	for _, v := range []string{"fixed", "sequence", "random"} {
		r := map[string]any{"supervisor": map[string]any{"selection": v}}
		normalizeSupervisorMessages(r)
		if got := jsonx.JsStr(r["supervisor"].(map[string]any)["selection"]); got != v {
			t.Fatalf("合法 selection=%q 被改成 %q", v, got)
		}
	}
}

// TestAppearanceHideOnShareDefault 「共享屏幕时隐藏提醒窗口」缺省必须是开。
//
// 这个键是「开会共享屏幕时别被同事看见」的兜底：默认关就等于没装 —— 用户不会
// 为一件自己不知道会发生的事预先去勾开关。所以三处口径必须一致：代码兜底 true、
// 出厂 JSON true、前端 fillForm 把「缺键」也当成勾选（见 settings.js 的
// `a.hideOnShare !== false`，此处只钉 Go 侧两条）。
func TestAppearanceHideOnShareDefault(t *testing.T) {
	// 1) 缺键（老配置）→ 开
	if s := (&Settings{raw: map[string]any{}}); !s.AppearanceCfg().HideOnShare {
		t.Fatal("缺 appearance.hideOnShare 时应视为开启")
	}
	// 2) 出厂默认 → 开
	if s := (&Settings{raw: defaultSettingsMap()}); !s.AppearanceCfg().HideOnShare {
		t.Fatal("出厂默认应为开启（否则首装用户得不到保护）")
	}
	// 3) 显式 false → 真关掉（用户勾掉了要能生效）
	raw := map[string]any{"appearance": map[string]any{"hideOnShare": false}}
	if s := (&Settings{raw: raw}); s.AppearanceCfg().HideOnShare {
		t.Fatal("显式 hideOnShare=false 应被尊重")
	}
	// 4) 类型坏了（手改成字符串）→ 回到默认「开」，不是「关」
	//    方向很重要：坏数据不能把保护关掉
	bad := map[string]any{"appearance": map[string]any{"hideOnShare": "nope"}}
	if s := (&Settings{raw: bad}); !s.AppearanceCfg().HideOnShare {
		t.Fatal("非法取值应兜底为开启，而不是静默关闭保护")
	}
}

// TestEmbeddedDefaultMatchesFile 内嵌的出厂默认必须与仓库根的 default_settings.json 一致。
//
// 两份默认值分别喂给两条路径：`//go:embed` 的这份是「恢复默认设置」的数据源；
// 仓库根的这份被 make_portable.py 复制成便携包的 config/settings.json（新装首跑读它）。
// 一旦分叉，用户首装看到的值与点「恢复默认」后的值就不一样，而且界面上完全看不出来。
// go:embed 取的是编译那一刻的快照，这条用例把快照钉在工作区文件上。
func TestEmbeddedDefaultMatchesFile(t *testing.T) {
	disk, err := os.ReadFile("../../default_settings.json")
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

// TestDefaultFixedRolePointsAtRealRole 出厂配置的「固定显示」专员必须能对上磁盘上的真人。
//
// 首装与「恢复默认设置」后，固定模式按这个名字取人。写错一个字（比如给专员改名只动了
// meta.json，忘了 sync 这里）不会报错，只会静默落到「第一个可用专员」——界面上完全看不出
// 异常，只有用户发现出场的人不是自己要的那个。这里的名字比对走 resolveRoleDir，口径与
// 运行时一致（读 meta.json 的 name，EqualFold）。
//
// 存在性**不写死具体专员**：内置专员用户可以删，删掉后本用例只检查「非空 + 名字属于内置
// 清单」并跳过磁盘比对，避免误报红。真正不能破的是「出厂必须给出一个确定的固定人选」——
// 留空就等于把选择权交给目录排序，不同机器上可能出场不同的人。
func TestDefaultFixedRolePointsAtRealRole(t *testing.T) {
	raw, err := os.ReadFile("../../default_settings.json")
	if err != nil {
		t.Fatalf("读 default_settings.json 失败: %v", err)
	}
	var cfg struct {
		Supervisor struct {
			FixedRole string `json:"fixedRole"`
		} `json:"supervisor"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("default_settings.json 解析失败: %v", err)
	}
	fixed := strings.TrimSpace(cfg.Supervisor.FixedRole)
	if fixed == "" {
		t.Fatal("出厂默认必须指定一个固定督休专员：留空会让首装落到「第一个可用」，不同机器可能不一样")
	}
	// 牙齿在这：名字必须是内置清单里的人。写成别的（拼错、或写了个清单外的名字）直接红，
	// 而且不碰磁盘，用户删专员也不会误报。
	builtin := false
	for _, n := range role.DefaultRoleOrder {
		if strings.EqualFold(n, fixed) {
			builtin = true
			break
		}
	}
	if !builtin {
		t.Fatalf("出厂固定专员 %q 不在 defaultRoleOrder 里：写错了名字，或者它不是内置专员", fixed)
	}
	if _, ok := role.ResolveRoleDir("../../supervisors", fixed); !ok {
		t.Logf("磁盘上没有专员 %q（内置专员可被用户删除），本轮跳过 meta.json 名字比对", fixed)
	}
}
