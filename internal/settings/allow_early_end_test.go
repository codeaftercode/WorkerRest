package settings

import (
	"encoding/json"
	"os"
	"testing"
)

// 「弹窗设置 → 允许提前结束」（allowEarlyEnd）在配置层的归一化回归。
//
// 出厂默认是**允许**：老配置里根本没有这个键，缺省若按 false 读，升级后所有提醒都会
// 变成“关不掉”，只能干坐到时间走完 —— 这是这个开关最容易写反的地方，用测试钉住。

func TestAllowEarlyEndDefaultsTrue(t *testing.T) {
	// 旧配置：短 / 长 / 自定义三处都没有这个键
	s := testSettingsFromJSON(t, `{"customBreaks":[{"id":"cb-a","name":"喝水","config":{"enabled":true}}]}`)
	if !s.Break("short").AllowEarlyEnd {
		t.Error("短休息缺键应默认允许提前结束")
	}
	if !s.Break("long").AllowEarlyEnd {
		t.Error("长休息缺键应默认允许提前结束")
	}
	custom := s.CustomBreakList()
	if len(custom) != 1 || !custom[0].Config.AllowEarlyEnd {
		t.Errorf("自定义提醒缺键应默认允许提前结束，得到 %+v", custom)
	}
	// 新建自定义提醒时用的出厂配置也必须是 true（否则设置页一建卡就是“关不掉”）
	if got := DefaultCustomBreakConfig()["allowEarlyEnd"]; got != true {
		t.Errorf("新建自定义提醒的出厂配置应为 true，得到 %#v", got)
	}
}

func TestAllowEarlyEndExplicitValues(t *testing.T) {
	s := testSettingsFromJSON(t, `{"short":{"allowEarlyEnd":false},"long":{"allowEarlyEnd":true},`+
		`"customBreaks":[{"id":"cb-a","name":"喝水","config":{"allowEarlyEnd":false}}]}`)
	if s.Break("short").AllowEarlyEnd {
		t.Error("短休息显式 false 应读成不允许提前结束")
	}
	if !s.Break("long").AllowEarlyEnd {
		t.Error("长休息显式 true 应读成允许提前结束")
	}
	if s.CustomBreakList()[0].Config.AllowEarlyEnd {
		t.Error("自定义提醒显式 false 应读成不允许提前结束")
	}
}

// 值类型不对（手改配置 / 旧版本写入的脏值）时按“允许”兜底 —— 与缺键同一口径，
// 不能因为一个坏值就让整次提醒关不掉。
func TestAllowEarlyEndNonBoolFallsBackTrue(t *testing.T) {
	for _, js := range []string{
		`{"short":{"allowEarlyEnd":"false"}}`,
		`{"short":{"allowEarlyEnd":0}}`,
		`{"short":{"allowEarlyEnd":null}}`,
	} {
		s := testSettingsFromJSON(t, js)
		if !s.Break("short").AllowEarlyEnd {
			t.Errorf("%s：非法值应兜底成允许提前结束", js)
		}
	}
}

// 随包分发的出厂配置（default_settings.json）必须与上面两条口径一致：
// 两个内置都显式写 true，装完第一次打开就是“能提前结束”。
func TestFactoryDefaultsAllowEarlyEnd(t *testing.T) {
	b, err := os.ReadFile("default_settings.json")
	if err != nil {
		t.Fatalf("读取出厂配置失败: %v", err)
	}
	var raw struct {
		Short map[string]any `json:"short"`
		Long  map[string]any `json:"long"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("出厂配置解析失败: %v", err)
	}
	if got, ok := raw.Short["allowEarlyEnd"].(bool); !ok || !got {
		t.Errorf("出厂短休息应为 allowEarlyEnd=true，得到 %#v", raw.Short["allowEarlyEnd"])
	}
	if got, ok := raw.Long["allowEarlyEnd"].(bool); !ok || !got {
		t.Errorf("出厂长休息应为 allowEarlyEnd=true，得到 %#v", raw.Long["allowEarlyEnd"])
	}
}
