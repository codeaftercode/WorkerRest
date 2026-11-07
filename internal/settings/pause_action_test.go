package settings

import (
	"encoding/json"
	"testing"
)

// “暂停休息时”处置键（pauseAction）在配置层的归一化回归。
// 默认推迟；非法值落回推迟；保存路径必须带上这个键（否则存一次就丢）。

func testSettingsFromJSON(t *testing.T, js string) *Settings {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(js), &m); err != nil {
		t.Fatalf("测试配置解析失败: %v", err)
	}
	return New(m, "")
}

func TestPauseActionDefaultsToPostpone(t *testing.T) {
	// 旧配置没有这个键：外卖与自定义提醒都应视为“推迟提醒”
	s := testSettingsFromJSON(t, `{"customBreaks":[{"id":"cb-a","name":"喝水","config":{"enabled":true}}]}`)
	if got := s.TakeoutPauseAction(); got != PauseActionPostpone {
		t.Errorf("取不到 pauseAction 时应默认推迟（外卖），得到 %q", got)
	}
	if got := s.Break("short").PauseAction; got != PauseActionPostpone {
		t.Errorf("取不到 pauseAction 时应默认推迟（配置层），得到 %q", got)
	}
	if got := s.CustomBreakList(); len(got) != 1 || got[0].Config.PauseAction != PauseActionPostpone {
		t.Errorf("自定义提醒缺省应为推迟，得到 %+v", got)
	}
	// 新建自定义提醒的出厂配置也必须是推迟
	if got := DefaultCustomBreakConfig()["pauseAction"]; got != PauseActionPostpone {
		t.Errorf("新建自定义提醒的默认处置应为推迟，得到 %#v", got)
	}
}

func TestPauseActionExplicitValues(t *testing.T) {
	s := testSettingsFromJSON(t, `{"takeout":{"pauseAction":"postpone"},`+
		`"customBreaks":[{"id":"cb-a","name":"喝水","config":{"pauseAction":"remind"}}]}`)
	if got := s.TakeoutPauseAction(); got != PauseActionPostpone {
		t.Errorf("外卖应读成推迟，得到 %q", got)
	}
	if got := s.CustomBreakList()[0].Config.PauseAction; got != PauseActionRemind {
		t.Errorf("自定义提醒应读成正常提醒，得到 %q", got)
	}
	// 非法值一律落回默认的推迟（手改配置 / 旧版本写入的脏值）
	bad := testSettingsFromJSON(t, `{"takeout":{"pauseAction":"whatever"},"customBreaks":[]}`)
	if got := bad.TakeoutPauseAction(); got != PauseActionPostpone {
		t.Errorf("非法值应落回推迟，得到 %q", got)
	}
}

// 保存路径：NormalizeTakeout 重建 takeout 对象时必须带上这个键（否则存一次就丢）
func TestNormalizeTakeoutKeepsPauseAction(t *testing.T) {
	out := NormalizeTakeout(map[string]any{
		"takeout": map[string]any{"enabled": true, "pauseAction": "remind", "slots": []any{}},
	})
	if got, _ := out["pauseAction"].(string); got != PauseActionRemind {
		t.Fatalf("NormalizeTakeout 应保留 pauseAction=remind，得到 %#v", out["pauseAction"])
	}
	// 缺省（旧配置）应补成 postpone：前端下拉默认就是“推迟提醒”
	out = NormalizeTakeout(map[string]any{"takeout": map[string]any{"enabled": true}})
	if got, _ := out["pauseAction"].(string); got != PauseActionPostpone {
		t.Fatalf("旧配置归一化后应补成 postpone，得到 %#v", out["pauseAction"])
	}
}

// 自定义提醒的配置归一化：已有键要校验，旧键 skipWhenPaused 要清掉，
// 原本没有这个键就不补写（短 / 长休息不写它）
func TestNormalizeBreakConfigPauseAction(t *testing.T) {
	if got := NormalizeBreakConfigOf(map[string]any{"pauseAction": "bogus"})["pauseAction"]; got != PauseActionPostpone {
		t.Fatalf("非法值应落回 postpone，得到 %#v", got)
	}
	out := NormalizeBreakConfigOf(map[string]any{"mode": "countdown", "skipWhenPaused": true})
	if _, has := out["skipWhenPaused"]; has {
		t.Fatal("旧键 skipWhenPaused 应在归一化时被清掉")
	}
	if _, has := out["pauseAction"]; has {
		t.Fatal("原本没有 pauseAction 时不应补写")
	}
}
