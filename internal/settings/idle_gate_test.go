package settings

import (
	"testing"

	"workerrest/internal/model"
)

// 空闲门（“鼠标键盘连续无活动 x 时间”）在配置层的归一化与读取回归。
//
// 三条口径固定下来：
//  1. 不勾 = 没开；勾了但 0 分 0 秒也 = 没开（IdleGateWaitMs / TakeoutIdleGateMs 折成 0）；
//  2. 出厂：不勾、30 秒，短 / 长 / 外卖三处都有这两个键；
//  3. 保存路径必须带上这两个键 —— normalizeTakeout 是**重建**对象，漏列就等于每次保存
//     都把用户的设置抹掉（与 displayKnown 那次同一个坑）。

func TestIdleGateDefaults(t *testing.T) {
	// 出厂默认：短 / 长 / 外卖都有 idleGate=false、idleGateMs=30000
	raw := DefaultSettingsMap()
	for _, key := range []string{"short", "long", "takeout"} {
		m, ok := raw[key].(map[string]any)
		if !ok {
			t.Fatalf("出厂默认缺少 %q 块", key)
		}
		if v, has := m["idleGate"]; !has || v != false {
			t.Errorf("%s.idleGate 出厂应为 false，得到 %#v（存在=%v）", key, v, has)
		}
		if v, has := m["idleGateMs"]; !has || v != float64(idleGateDefaultMs) {
			t.Errorf("%s.idleGateMs 出厂应为 %d，得到 %#v（存在=%v）", key, idleGateDefaultMs, v, has)
		}
	}
	if IdleGateDefaultMs() != 30_000 || IdleGateMaxMs() != 30*60_000 {
		t.Errorf("出厂 30 秒 / 上限 30 分钟，得到 %d / %d", IdleGateDefaultMs(), IdleGateMaxMs())
	}

	// 缺键（旧配置 / 手改坏）：读出来是“不勾 + 30 秒”，不是 0
	s := testSettingsFromJSON(t, `{"short":{}}`)
	c := s.Break("short")
	if c.IdleGate || c.IdleGateMs != idleGateDefaultMs {
		t.Errorf("缺键应回落成不勾 + 30 秒，得到 %+v", c)
	}
	// 新建自定义提醒的出厂配置也要带上这两个键（设置页每张卡形状一致）
	def := DefaultCustomBreakConfig()
	if v, has := def["idleGate"]; !has || v != false {
		t.Errorf("新建自定义提醒的 idleGate 应为 false，得到 %#v（存在=%v）", v, has)
	}
	if v, has := def["idleGateMs"]; !has || v != float64(idleGateDefaultMs) {
		t.Errorf("新建自定义提醒的 idleGateMs 应为 %d，得到 %#v（存在=%v）", idleGateDefaultMs, v, has)
	}
}

func TestIdleGateWaitMsSemantics(t *testing.T) {
	cases := []struct {
		name    string
		gate    bool
		ms      int64
		want    int64
		wantWhy string
	}{
		{"不勾", false, 30_000, 0, "没勾就不设门"},
		{"勾了 30 秒", true, 30_000, 30_000, "正常值"},
		{"勾了但 0 秒", true, 0, 0, "0 分 0 秒等于没启用"},
		{"勾了但负数", true, -5_000, 0, "负值按 0 处理"},
	}
	for _, c := range cases {
		got := model.BreakConfig{IdleGate: c.gate, IdleGateMs: c.ms}.IdleGateWaitMs()
		if got != c.want {
			t.Errorf("%s：IdleGateWaitMs 应为 %d，得到 %d（%s）", c.name, c.want, got, c.wantWhy)
		}
	}

	// 从配置读到类型化结构后同样成立
	on := testSettingsFromJSON(t, `{"short":{"idleGate":true,"idleGateMs":90000}}`).Break("short")
	if got := on.IdleGateWaitMs(); got != 90_000 {
		t.Errorf("勾了 1 分 30 秒应得 90000，得到 %d", got)
	}
	off := testSettingsFromJSON(t, `{"short":{"idleGate":true,"idleGateMs":0}}`).Break("short")
	if got := off.IdleGateWaitMs(); got != 0 {
		t.Errorf("0 分 0 秒应折成 0，得到 %d", got)
	}
}

// idleGateMs 夹紧：负数 → 0，超过 30 分钟 → 上限
func TestIdleGateMsClamped(t *testing.T) {
	big := testSettingsFromJSON(t, `{"short":{"idleGate":true,"idleGateMs":99999999}}`).Break("short")
	if big.IdleGateMs != idleGateMaxMs {
		t.Errorf("超上限应收敛到 %d，得到 %d", idleGateMaxMs, big.IdleGateMs)
	}
	neg := testSettingsFromJSON(t, `{"short":{"idleGate":true,"idleGateMs":-1000}}`).Break("short")
	if neg.IdleGateMs != 0 {
		t.Errorf("负值应收敛到 0，得到 %d", neg.IdleGateMs)
	}
}

func TestTakeoutIdleGateMs(t *testing.T) {
	s := testSettingsFromJSON(t, `{"takeout":{"idleGate":true,"idleGateMs":60000}}`)
	if got := s.TakeoutIdleGateMs(); got != 60_000 {
		t.Errorf("外卖空闲门应读成 60000，得到 %d", got)
	}
	if got := testSettingsFromJSON(t, `{"takeout":{"idleGate":false,"idleGateMs":60000}}`).TakeoutIdleGateMs(); got != 0 {
		t.Errorf("没勾应得 0，得到 %d", got)
	}
	if got := testSettingsFromJSON(t, `{"takeout":{"idleGate":true,"idleGateMs":0}}`).TakeoutIdleGateMs(); got != 0 {
		t.Errorf("勾了但 0 秒应得 0，得到 %d", got)
	}
}

// 保存路径：NormalizeTakeout 重建对象时必须带这两个键（漏了就等于保存一次抹掉一次）
func TestNormalizeTakeoutKeepsIdleGate(t *testing.T) {
	out := NormalizeTakeout(map[string]any{
		"takeout": map[string]any{"enabled": true, "idleGate": true, "idleGateMs": float64(45_000)},
	})
	if got, _ := out["idleGate"].(bool); !got {
		t.Fatalf("NormalizeTakeout 应保留 idleGate=true，得到 %#v", out["idleGate"])
	}
	if got, _ := out["idleGateMs"].(float64); got != 45_000 {
		t.Fatalf("NormalizeTakeout 应保留 idleGateMs=45000，得到 %#v", out["idleGateMs"])
	}
	// 缺键（旧配置）补成“不勾 + 30 秒”；非法值夹紧
	out = NormalizeTakeout(map[string]any{"takeout": map[string]any{"enabled": true}})
	if got, _ := out["idleGate"].(bool); got {
		t.Fatalf("缺键应补成不勾，得到 %#v", out["idleGate"])
	}
	if got, _ := out["idleGateMs"].(float64); got != float64(idleGateDefaultMs) {
		t.Fatalf("缺键应补成 %d，得到 %#v", idleGateDefaultMs, out["idleGateMs"])
	}
	out = NormalizeTakeout(map[string]any{"takeout": map[string]any{"idleGate": true, "idleGateMs": float64(1e9)}})
	if got, _ := out["idleGateMs"].(float64); got != float64(idleGateMaxMs) {
		t.Fatalf("超上限应收敛到 %d，得到 %#v", idleGateMaxMs, out["idleGateMs"])
	}
}

// 短 / 长 / 自定义走 normalizeBreakConfigOf（复制全部键），这两个键不能被丢掉
func TestNormalizeBreakConfigKeepsIdleGate(t *testing.T) {
	out := NormalizeBreakConfigOf(map[string]any{"idleGate": true, "idleGateMs": float64(20_000)})
	if got, _ := out["idleGate"].(bool); !got {
		t.Fatalf("应保留 idleGate，得到 %#v", out)
	}
	if got, _ := out["idleGateMs"].(float64); got != 20_000 {
		t.Fatalf("应保留 idleGateMs，得到 %#v", out["idleGateMs"])
	}
}
