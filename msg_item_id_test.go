package main

import (
	"encoding/json"
	"strconv"
	"testing"
)

// 造一条提醒内容条目（settings.json 与 meta.json 两侧的条目形状一致）。
func msgItem(id any, text string) map[string]any {
	m := map[string]any{"text": text, "enabled": true}
	if id != nil {
		m["id"] = id
	}
	return m
}

func itemIDsOf(items []any) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, jsStr(it.(map[string]any)["id"]))
	}
	return out
}

// 条目 id 就应该是「本列表内的序号」：短、可读、可手改。
func TestNextSeqID(t *testing.T) {
	cases := []struct {
		name string
		ids  []string
		want string
	}{
		{"空列表从 1 开始", nil, "1"},
		{"接在最大值后面", []string{"1", "2"}, "3"},
		{"乱序取最大", []string{"3", "1", "2"}, "4"},
		{"中间的号被删了也不复用", []string{"1", "3"}, "4"},
		{"非数字 id 直接跳过", []string{"a3", "b2"}, "1"},
		{"uuid 混入也不炸", []string{newUUID(), "7"}, "8"},
	}
	for _, c := range cases {
		if got := nextSeqID(c.ids); got != c.want {
			t.Errorf("%s: nextSeqID(%v) = %q，想要 %q", c.name, c.ids, got, c.want)
		}
	}
}

// 归一化必须把**任何**非序号 id 收敛成序号：缺的补、uuid / builtin-xxx-N 重编、
// 手写成数字的认下、重号的后来那条重发一枚。
func TestNormalizeMsgItemsConverges(t *testing.T) {
	raw := []any{
		msgItem(nil, "缺 id"),
		msgItem("1", "已经是 1"),
		msgItem("1", "重号的 1"),
		msgItem("builtin-short-7", "老格式内置 id"),
		msgItem("f47ac10b-58cc-4372-a567-0e02b2c3d479", "老格式 uuid"),
		msgItem(float64(9), "手写成数字 9"),
	}
	out, remap := normalizeMsgItems(raw)
	want := []string{"1", "2", "3", "4", "5", "9"}
	if got := itemIDsOf(out); len(got) != len(want) {
		t.Fatalf("条目数 = %d，想要 %d（%v）", len(got), len(want), got)
	} else {
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("第 %d 条 id = %q，想要 %q", i+1, got[i], want[i])
			}
		}
	}
	// fixedId 之类要能按映射搬到新号上
	for old, want := range map[string]string{
		"builtin-short-7":                      "4",
		"f47ac10b-58cc-4372-a567-0e02b2c3d479": "5",
		"1":                                    "1", // 没被改动的不会进映射
	} {
		got, ok := remap[old]
		if want == old {
			if ok {
				t.Errorf("%q 没被改过，不该出现在映射里", old)
			}
			continue
		}
		if !ok || got != want {
			t.Errorf("映射 %q → %q，想要 %q", old, got, want)
		}
	}
}

// meta.json 的台词池同一口径，并且 fixedId 要跟着搬。
func TestNormalizeRoleMessagesConverges(t *testing.T) {
	m := map[string]any{
		"order":   "fixed",
		"fixedId": "builtin-cmd-role_doctor-2",
		"items": []any{
			msgItem("builtin-cmd-role_doctor-0", "a"),
			msgItem("builtin-cmd-role_doctor-1", "b"),
			msgItem("builtin-cmd-role_doctor-2", "c"),
		},
	}
	cfg := normalizeRoleMessages(m)
	if cfg.FixedID != "3" {
		t.Errorf("fixedId = %q，想要 %q（应跟着第三条走）", cfg.FixedID, "3")
	}
	got := make([]string, 0, len(cfg.Items))
	for _, it := range cfg.Items {
		got = append(got, it.ID)
	}
	want := []string{"1", "2", "3"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("第 %d 条 id = %q，想要 %q", i+1, got[i], want[i])
		}
	}
	// 收敛后再跑一遍不该再动（幂等）
	again := normalizeRoleMessages(map[string]any{
		"order":   "fixed",
		"fixedId": cfg.FixedID,
		"items": []any{
			msgItem("1", "a"), msgItem("2", "b"), msgItem("3", "c"),
		},
	})
	if again.FixedID != "3" {
		t.Errorf("幂等性：fixedId = %q，想要 3", again.FixedID)
	}
}

// 自定义提醒的提醒内容：老 id 收敛、fixedId 跟着搬，且返回值能让调用方知道要回写。
func TestNormalizeCustomBreaksConverges(t *testing.T) {
	raw := map[string]any{
		"customBreaks": []any{
			map[string]any{
				"id":   "cb-1",
				"name": "喝水提醒",
				"messages": map[string]any{
					"order":   "fixed",
					"fixedId": "f47ac10b-58cc-4372-a567-0e02b2c3d479",
					"items": []any{
						msgItem("builtin-cmd-generic-0", "a"),
						msgItem("f47ac10b-58cc-4372-a567-0e02b2c3d479", "b"),
					},
				},
			},
		},
	}
	if !normalizeCustomBreaks(raw) {
		t.Fatal("有老格式 id，应当报告「已重编号」")
	}
	cb := raw["customBreaks"].([]any)[0].(map[string]any)
	msg := cb["messages"].(map[string]any)
	if got := itemIDsOf(msg["items"].([]any)); got[0] != "1" || got[1] != "2" {
		t.Errorf("条目 id = %v，想要 [1 2]", got)
	}
	if msg["fixedId"] != "2" {
		t.Errorf("fixedId = %v，想要 \"2\"", msg["fixedId"])
	}
	// 幂等：再跑一次不该报改动
	if normalizeCustomBreaks(raw) {
		t.Error("已收敛的配置再跑一次不该再报重编号")
	}
}

// 整份配置加载：老格式 id 一律收敛，且收敛结果落定后不再重复回写。
func TestNormalizeLoadRenumbersItemIDs(t *testing.T) {
	raw := defaultSettingsMap()
	raw["reminderMessages"] = map[string]any{
		"short": map[string]any{
			"order": "fixed", "fixedId": "builtin-short-2",
			"items": []any{
				msgItem("builtin-short-0", "a"),
				msgItem("builtin-short-1", "b"),
				msgItem("builtin-short-2", "c"),
			},
		},
	}
	_, _, renumbered, _ := normalizeLoad(raw)
	if !renumbered {
		t.Fatal("老格式条目 id 应当被判为「已重编号」，否则不会回写盘")
	}
	rm := raw["reminderMessages"].(map[string]any)
	short := rm["short"].(map[string]any)
	if short["fixedId"] != "3" {
		t.Errorf("fixedId = %v，想要 \"3\"", short["fixedId"])
	}
	if got := itemIDsOf(short["items"].([]any)); got[0] != "1" || got[2] != "3" {
		t.Errorf("条目 id = %v，想要 [1 2 3]", got)
	}

	// 落盘一次之后的配置再跑加载：不该再报重编号（避免每次启动都写盘）
	var feed map[string]any
	b, _ := json.Marshal(raw)
	if err := json.Unmarshal(b, &feed); err != nil {
		t.Fatal(err)
	}
	if _, _, again, _ := normalizeLoad(feed); again {
		t.Error("已收敛的配置再加载一次不该再报重编号")
	}
}

// 预置种子与导入模板的 id 也是序号（不再带 builtin- / 类型前缀）。
func TestSeedItemIDsAreSequence(t *testing.T) {
	for _, kind := range []string{"short", "long", "takeout"} {
		items := builtinKindItems(kind)
		if len(items) == 0 {
			t.Fatalf("%s 的预置条目是空的", kind)
		}
		for i, it := range items {
			if want := strconv.Itoa(i + 1); it.ID != want {
				t.Errorf("%s 第 %d 条 id = %q，想要 %q", kind, i+1, it.ID, want)
			}
		}
	}
	generic := genericCommandItems()
	if len(generic) == 0 {
		t.Fatal("导入模板的台词是空的")
	}
	for i, it := range generic {
		if want := strconv.Itoa(i + 1); it.ID != want {
			t.Errorf("导入模板第 %d 条 id = %q，想要 %q", i+1, it.ID, want)
		}
	}
}
