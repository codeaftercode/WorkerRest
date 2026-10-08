package main

import (
	"reflect"
	"testing"
)

// 版本比较纯函数回归：isNewerVersion / parseVersion 逐条对应 main.ts 的
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
		if got := parseVersion(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("parseVersion(%q) = %v，期望 %v", c.in, got, c.want)
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
		if got := isNewerVersion(c.current, c.latest); got != c.want {
			t.Errorf("isNewerVersion(%q, %q) = %v，期望 %v", c.current, c.latest, got, c.want)
		}
	}
}

func TestUpdateCheckResultToMap(t *testing.T) {
	r := updateCheckResult{Status: "update-available", Current: "1.0.0", Latest: "1.1.0", URL: "https://x"}
	m := r.toMap()
	if m["status"] != "update-available" || m["current"] != "1.0.0" ||
		m["latest"] != "1.1.0" || m["url"] != "https://x" {
		t.Fatalf("toMap 字段不符：%v", m)
	}
	// error 状态不带 latest/url，只带 message
	e := updateCheckResult{Status: "error", Current: "1.0.0", Message: "boom"}
	em := e.toMap()
	if _, ok := em["latest"]; ok {
		t.Fatalf("error 状态不该带 latest：%v", em)
	}
	if em["message"] != "boom" {
		t.Fatalf("message 不符：%v", em)
	}
}
