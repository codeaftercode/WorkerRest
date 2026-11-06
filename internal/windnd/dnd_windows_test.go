//go:build windows

package windnd

import "testing"

// 真机形状检查：两条信号都得给出可读说明；WNF 可用且报勿扰时，detail 必须是已知档位。
// 真机上没法按需要切换系统勿扰，档位 → 结论的矩阵由 dnd_test.go 覆盖。
func TestDndProbeShape(t *testing.T) {
	st := Query()
	dnd := st.Dnd()
	if dnd.Detail == "" {
		t.Fatal("勿扰 detail 不该为空（无论支持与否都要有说明）")
	}
	if !dnd.Supported {
		t.Logf("本机 WNF 勿扰档位不可用（勿扰那条会整拍跳过）：%s", dnd.Detail)
		return
	}
	if dnd.Active {
		known := false
		for _, name := range quietHoursProfileText {
			if name == dnd.Detail {
				known = true
			}
		}
		if !known {
			t.Fatalf("判定为勿扰时 detail 应是已知档位之一，得到 %q", dnd.Detail)
		}
	}
	for _, s := range []struct {
		name string
		ok   bool
		txt  string
	}{
		{"全屏或演示", st.FullScreen().Supported, st.FullScreen().Detail},
	} {
		if s.txt == "" {
			t.Fatalf("%s detail 不该为空", s.name)
		}
		if !s.ok {
			t.Logf("本机 %s 探测不可用：%s", s.name, s.txt)
		}
	}
}
