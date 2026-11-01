//go:build windows

package windnd

import "testing"

func TestDndProbeShape(t *testing.T) {
	st := GetDndStatus()
	if st.Detail == "" {
		t.Fatal("detail 不该为空（无论支持与否都要有说明）")
	}
	if !st.Supported {
		t.Skipf("本机勿扰检测不可用：%s", st.Detail)
	}
	if st.Active {
		known := false
		for _, name := range winDndStates {
			if name == st.Detail {
				known = true
			}
		}
		if !known {
			t.Fatalf("判定为勿扰时 detail 应是已知勿扰状态之一，得到 %q", st.Detail)
		}
	}
}
