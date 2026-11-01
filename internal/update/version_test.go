package update

import (
	"reflect"
	"testing"
)

// 版本比较纯函数回归：IsNewerVersion / ParseVersion 逐条对应 main.ts 的
// isNewerVersion（v 前缀、缺段都容忍，三段式比较，latest 严格大于 current 才算新版）。

func TestParseVersion(t *testing.T) {
	cases := []struct {
		in   string
		want []int
	}{
		{"1.0.0", []int{1, 0, 0}},
		{"v1.2.3", []int{1, 2, 3}},
		{"  v2.0.0  ", []int{2, 0, 0}},
		{"1.2", []int{1, 2}},
		{"1", []int{1}},
		{"", []int{0}}, // 空串拆出一段空 → 0
		{"abc", []int{0}},
		{"1.x.3", []int{1, 0, 3}}, // 非数字段按 0
	}
	for _, c := range cases {
		if got := ParseVersion(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("ParseVersion(%q) = %v，期望 %v", c.in, got, c.want)
		}
	}
}

func TestIsNewerVersion(t *testing.T) {
	cases := []struct {
		current, latest string
		want            bool
	}{
		{"1.0.0", "1.0.1", true},
		{"1.0.0", "1.1.0", true},
		{"1.0.0", "2.0.0", true},
		{"1.0.0", "1.0.0", false},
		{"1.0.1", "1.0.0", false},
		{"2.0.0", "1.9.9", false},
		// v 前缀
		{"1.0.0", "v1.1.0", true},
		{"v1.0.0", "1.1.0", true},
		// 缺段容忍：latest 补 0 后仍大
		{"1.0.0", "1.1", true},
		{"1.1", "1.1.0", false},
		{"1.0.0", "1.0", false},
		// 空 / 非法
		{"1.0.0", "", false},
		{"", "1.0.0", true},
	}
	for _, c := range cases {
		if got := IsNewerVersion(c.current, c.latest); got != c.want {
			t.Errorf("IsNewerVersion(%q, %q) = %v，期望 %v", c.current, c.latest, got, c.want)
		}
	}
}

func TestSameVersion(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"1.0.0", "1.0.0", true},
		{"v1.0.0", "1.0.0", true},
		{"1.0.0", "1.0.1", false},
		{"1.0", "1.0.0", true}, // 缺段容忍
	}
	for _, c := range cases {
		if got := SameVersion(c.a, c.b); got != c.want {
			t.Errorf("SameVersion(%q, %q) = %v，期望 %v", c.a, c.b, got, c.want)
		}
	}
}
