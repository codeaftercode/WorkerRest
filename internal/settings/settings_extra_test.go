package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 这一组补的是“类型化视图”与加载路径的外围：取值方法有没有兜住坏配置、候选路径
// 与保存落点是否只有一处真源。归一化细节在 settings_test.go / msg_item_id_test.go 里。

func rawOf(t *testing.T, js string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(js), &m); err != nil {
		t.Fatalf("测试配置不是合法 JSON: %v", err)
	}
	return m
}

func TestSliceAndMapTypeGuards(t *testing.T) {
	s := New(rawOf(t, `{"arr":[1,2],"obj":{"k":1},"str":"x"}`), "")
	if got := s.Slice("arr"); len(got) != 2 {
		t.Fatalf("数组应原样返回，得到 %#v", got)
	}
	// 品类不符一律给 nil：调用方据此回落到默认，不能返回一个空切片让它以为“配置就是空”
	for _, k := range []string{"obj", "str", "missing"} {
		if got := s.Slice(k); got != nil {
			t.Fatalf("Slice(%q) 应为 nil，得到 %#v", k, got)
		}
	}
	if got := s.Map("obj"); got["k"] != 1.0 {
		t.Fatalf("对象应原样返回，得到 %#v", got)
	}
	for _, k := range []string{"arr", "str", "missing"} {
		if got := s.Map(k); got != nil {
			t.Fatalf("Map(%q) 应为 nil，得到 %#v", k, got)
		}
	}
}

func TestIgnoredVersionSetAndClear(t *testing.T) {
	s := New(rawOf(t, `{}`), "")
	if got := s.IgnoredVersion(); got != "" {
		t.Fatalf("默认不该忽略任何版本，得到 %q", got)
	}
	s.SetIgnoredVersion("v1.2.3")
	if got := s.IgnoredVersion(); got != "v1.2.3" {
		t.Fatalf("得到 %q", got)
	}
	// 清空 = 删键（不是存空串）：空串会让“有没有忽略过”的判据含糊
	s.SetIgnoredVersion("")
	if _, ok := s.Raw()["ignoredVersion"]; ok {
		t.Fatal("清空忽略时该把键删掉")
	}
}

func TestDisplayIDsFiltersJunk(t *testing.T) {
	s := New(rawOf(t, `{"displayIds":["DEL41#1","",3,"DEL41#2"]}`), "")
	got := s.DisplayIDs()
	if len(got) != 2 || got[0] != "DEL41#1" || got[1] != "DEL41#2" {
		t.Fatalf("只留非空字符串并保持顺序，得到 %#v", got)
	}
	// 出厂值 / 坏值都是空数组：**语义是“还没挑过屏”**，运行时兜底主屏
	for _, js := range []string{`{"displayIds":[]}`, `{"displayIds":"x"}`, `{}`} {
		if got := New(rawOf(t, js), "").DisplayIDs(); len(got) != 0 {
			t.Fatalf("%s 应得到空（或 nil），得到 %#v", js, got)
		}
	}
}

func TestSimpleFlagGetters(t *testing.T) {
	s := New(rawOf(t, `{"startMinimized":true,"autostart":true,"appName":"  吃 饼 人  "}`), "")
	if !s.StartMinimized() || !s.Autostart() {
		t.Fatal("开关应读出来")
	}
	// AppName 会折叠空白（给窗口标题用），但下发的 raw 保持磁盘原样
	if got := s.AppName(); got != "吃 饼 人" {
		t.Fatalf("应折叠首尾与连续空白，得到 %q", got)
	}
	if raw := s.Str("", "appName"); raw != "  吃 饼 人  " {
		t.Fatalf("raw 不该被归一化，得到 %q", raw)
	}
	// 缺省值
	empty := New(rawOf(t, `{}`), "")
	if empty.StartMinimized() || empty.Autostart() {
		t.Fatal("缺省应为 false")
	}
}

func TestCandidatesHasSingleTruth(t *testing.T) {
	t.Setenv("WR_SETTINGS_PATH", "")
	got := Candidates(`D:\app`)
	if len(got) != 1 || got[0] != filepath.Join(`D:\app`, "config", "settings.json") {
		t.Fatalf("落点应只有 exe 旁的 config/settings.json，得到 %#v", got)
	}
	// 隔离变量独占：探针靠它保证绝不碰用户真实配置
	iso := filepath.Join(t.TempDir(), "iso.json")
	t.Setenv("WR_SETTINGS_PATH", "  "+iso+"  ")
	if got := Candidates(`D:\app`); len(got) != 1 || got[0] != iso {
		t.Fatalf("WR_SETTINGS_PATH 应独占候选，得到 %#v", got)
	}
}

func TestSaveUsesResourcesRootHint(t *testing.T) {
	t.Setenv("WR_SETTINGS_PATH", "")
	root := t.TempDir()
	SetResourcesRootHint(func() string { return root })
	t.Cleanup(func() { SetResourcesRootHint(nil) })

	s := New(rawOf(t, `{"appName":"x"}`), "") // 无来源 → 按 hint 推导
	if err := s.Save(); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	want := filepath.Join(root, "config", "settings.json")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("应落到 %s: %v", want, err)
	}
	if s.Path() != want {
		t.Fatalf("保存后 path 应指向真实落点，得到 %q", s.Path())
	}
}

// NormalizeLoad 是“恢复默认”与启动管线共用的归一化入口：两条路径必须得到同一份形状，
// 否则恢复默认之后界面会与重启后长得不一样。
func TestNormalizeLoadProducesSameShapeAsLoad(t *testing.T) {
	raw := rawOf(t, `{
		"short":{"mode":"乱写","intervalMs":"600000"},
		"reminderMessages":{},
		"customBreaks":[{"id":"c1","name":"喝水","config":{"mode":"countdown","intervalMs":300000}}],
		"takeout":{"enabled":"yes"},
		"window":{"width":"900"}
	}`)
	NormalizeLoad(raw)

	// 非法 mode 收敛成 countdown
	sh := raw["short"].(map[string]any)
	if sh["mode"] != "countdown" {
		t.Fatalf("非法 mode 应回落 countdown，得到 %v", sh["mode"])
	}
	// 字符串数字 raw 里保持原样（归一化不猜类型），但**类型化视图**必须受得住
	if got := New(raw, "").Break("short").IntervalMs; got != 600_000 {
		t.Fatalf("字符串数字应被类型化视图读成 600000，得到 %v", got)
	}
	if w := raw["window"].(map[string]any); w["width"] == "900" {
		t.Fatal("窗口宽度应归一成数字")
	}
	// 预置提醒内容条目应被种进去（加载即种，用户不改也有一份能用的台词）
	rm := raw["reminderMessages"].(map[string]any)
	if len(rm) == 0 {
		t.Fatal("NormalizeLoad 应种入预置提醒内容")
	}
	// 幂等：再跑一遍不该变形状
	first, _ := json.Marshal(raw)
	NormalizeLoad(raw)
	second, _ := json.Marshal(raw)
	if string(first) != string(second) {
		t.Fatalf("NormalizeLoad 应幂等\n第一次: %s\n第二次: %s", first, second)
	}
}

func TestNormalizeForSaveKeepsNoSeed(t *testing.T) {
	// 保存路径不做首次种子（那是一次性动作，加载时已经做过）：
	// 空 reminderMessages 保存后仍是空清单，而不是被悄悄塞进预置台词
	raw := rawOf(t, `{"reminderMessages":{},"short":{"mode":"countdown"}}`)
	NormalizeForSave(raw)
	rm := raw["reminderMessages"].(map[string]any)
	// 三个品类的小节会被补齐（结构需要），但**条目**必须还是空的
	for _, kind := range []string{"short", "long", "takeout"} {
		cfg, ok := rm[kind].(map[string]any)
		if !ok {
			t.Fatalf("%s 小节应被补齐，得到 %#v", kind, rm)
		}
		if items, ok := cfg["items"].([]any); ok && len(items) != 0 {
			t.Fatalf("保存路径不该种预置条目，%s 有 %d 条", kind, len(items))
		}
		if cfg["seeded"] == true {
			t.Fatalf("保存路径不该标记 seeded（那是加载路径的一次性动作），%s 被标了", kind)
		}
	}
}

func TestNormalizeAppNameCollapsesAndLimits(t *testing.T) {
	cases := map[string]string{
		"  吃饼人  ":               "吃饼人",
		"吃\t饼\n人":               "吃 饼 人",
		"":                      "",
		"   ":                   "",
		strings.Repeat("啊", 80): strings.Repeat("啊", 30), // 长度上限 30（标题栏不撑爆）
	}
	for in, want := range cases {
		if got := normalizeAppName(in); got != want {
			t.Fatalf("normalizeAppName(%q) = %q，期望 %q", in, got, want)
		}
	}
}
