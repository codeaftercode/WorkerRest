package urltls

import "testing"

// 本包是更新链路的信任边界：放行了不该放行的地址，等于允许把任意 http 源当成
// 「官方更新」。所以这里不测「正常情况能过」，重点测**该拒的都拒了**。

func TestIsLoopbackHost(t *testing.T) {
	cases := []struct {
		host string
		want bool
	}{
		{"localhost", true},
		{"LocalHost", true},
		{"  localhost  ", true},
		{"127.0.0.1", true},
		{"127.1.2.3", true}, // 127.0.0.0/8 整段都是回环
		{"::1", true},
		{"[::1]", false}, // 带方括号的形式不该被当主机名（url.Hostname 已经去括号）
		{"", false},
		{"0.0.0.0", false},
		{"10.0.0.1", false},
		{"192.168.1.1", false},
		{"example.com", false},
		// 伪装回环：这些一旦被误判成回环，http 明文源就整个破防了。
		{"127.0.0.1.evil.com", false},
		{"localhost.evil.com", false},
		{"evil.com#localhost", false},
		{"localhost%00.evil.com", false},
	}
	for _, c := range cases {
		if got := IsLoopbackHost(c.host); got != c.want {
			t.Errorf("IsLoopbackHost(%q) = %v, want %v", c.host, got, c.want)
		}
	}
}

func TestCheckUpdateURL(t *testing.T) {
	cases := []struct {
		raw     string
		wantErr bool
		why     string
	}{
		// ---- 放行 ----
		{"https://www.goodgoodlive.com/worker-rest/version.json", false, "正常 https 源"},
		{"HTTPS://WWW.EXAMPLE.COM/VERSION.JSON", false, "scheme / 主机大小写不敏感"},
		{"  https://example.com/x.json  ", false, "两端空白先 trim"},
		{"https://example.com:8443/x.json", false, "自定义端口"},
		{"https://example.com/x.json?k=v#frag", false, "带 query 与 fragment"},
		{"http://localhost:8080/version.json", false, "回环：为 httptest 保留"},
		{"http://127.0.0.1:9000/v.json", false, "回环 IP"},
		{"http://[::1]:9000/v.json", false, "IPv6 回环"},

		// ---- 拒绝 ----
		{"", true, "空地址"},
		{"   ", true, "纯空白"},
		{"http://example.com/version.json", true, "公网 http 明文源"},
		{"http://127.0.0.1.evil.com/v.json", true, "伪装回环的 http 源"},
		{"http://localhost.evil.com/v.json", true, "伪装 localhost 的 http 源"},
		{"https://", true, "https 但缺主机名"},
		{"ftp://example.com/v.json", true, "不支持的协议"},
		{"file:///C:/version.json", true, "本地文件协议"},
		{"javascript:alert(1)", true, "伪协议"},
		{"//example.com/v.json", true, "协议相对地址（scheme 为空）"},
		{"example.com/v.json", true, "缺协议"},
		{"http://[::1/x", true, "URL 无法解析"},
	}
	for _, c := range cases {
		err := CheckUpdateURL(c.raw)
		if (err != nil) != c.wantErr {
			t.Errorf("CheckUpdateURL(%q) err = %v, wantErr = %v（%s）", c.raw, err, c.wantErr, c.why)
		}
		if err != nil && err.Error() == "" {
			t.Errorf("CheckUpdateURL(%q) 的 error 文本不能为空：它要直接展示给用户", c.raw)
		}
	}
}
