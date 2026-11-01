package winproc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestListProcessNamesReal(t *testing.T) {
	r := ListProcessNames()
	if !r.Supported {
		t.Fatalf("本机进程枚举应可用：%s", r.Detail)
	}
	if len(r.Names) < 3 {
		t.Fatalf("进程数太少，不像真实枚举：%s %v", r.Detail, r.Names)
	}
	seen := make(map[string]bool, len(r.Names))
	for _, n := range r.Names {
		k := strings.ToLower(n)
		if seen[k] {
			t.Fatalf("结果应按大小写去重，出现重复项 %q", n)
		}
		seen[k] = true
	}
	selfPath, _ := os.Executable()
	self := strings.ToLower(filepath.Base(selfPath))
	if !seen[self] {
		t.Fatalf("列表里应有本测试进程 %q（否则枚举的是别的什么东西）", self)
	}
}
