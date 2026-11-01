package main

import (
	"os"
	"path/filepath"
	"testing"
)

// repoRoot 返回项目模块根目录（go.mod 所在），供测试读仓库里的运行时资源
// （supervisors/、sounds/、default_settings.json 等）用。
//
// 为什么需要它：main 包下沉到 cmd/workerrest/ 之后，`go test` 的 cwd 是这个包目录，
// 而 supervisors/、sounds/ 这些「运行时资源」仍留在模块根 —— 它们不属于 main 包源码，
// 不该跟着挪。测试要读它们就得向上定位到 go.mod。
//
// 实现从 cwd 逐级向上找 go.mod，而不是写死 "../.."：万一有人用 `go test` 的
// -C/其他 cwd 跑，也能对上；找不到就直接 fail，别拿错目录读资源。
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("取 cwd 失败: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("从 %s 向上找不到 go.mod（模块根）", dir)
		}
		dir = parent
	}
}
