package main

import (
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// 本文件是「前端 ↔ Go 绑定」这条链路的安全网。
//
// 背景：为了把 上一版的渲染层（HTML/CSS/JS）原样复用，前端调用被改成
// Wails v3 的**按名字调用** `Call.ByName("workerrest.App.<方法>")`。
// 代价是 JS 里拼错的名字、Go 侧改掉的方法名，编译/类型检查/构建全都发现不了，
// 只在运行时静默失败。下面三个测试把这个缺口堵上（注册表层面的解析由
// scripts/smoke_server.py 端到端覆盖：它走的就是前端同一条 HTTP 调用链）。

// TestBindingFQNPrefix 前缀常量必须停在 "main.App."。
//
// 为什么不拿 reflect 的值来比：**`go test` 与 `go build` 下 main 包的 PkgPath 不同**
// （测试二进制把被测包当库链接 → 返回导入路径 "workerrest"；构建产物 → "main"）。
// 这个差异让「用测试里的反射值当真理」变成一个假信号，本项目踩过一次：
// 单元测试一路绿灯，真实二进制却 422 unknown bound method name。
//
// 因此这里只钉两件能钉住的事：常量本身是 "main.App."，且类型名部分与 *App 一致。
// 真值校验交给 scripts/smoke_server.py —— 它跑真实二进制、按同名调用，
// 名字对不上会直接 422。
func TestBindingFQNPrefix(t *testing.T) {
	if bindingFQN != "main.App." {
		t.Fatalf("绑定前缀被改成了 %q，必须是 \"main.App.\"（main 包在构建产物里的 PkgPath 就是 main）\n"+
			"要同步修改 rewrite-go/contract.go 与 rewrite-go/frontend/dist/shim.js", bindingFQN)
	}
	if want := "." + reflect.TypeOf(App{}).Name() + "."; !strings.HasSuffix(bindingFQN, want) {
		t.Fatalf("前缀 %q 的类型名部分与 *App 不一致（应为 ...%s）", bindingFQN, want)
	}
}

// TestContractMethodsExistOnApp 契约里的每个方法都必须在 *App 上真实存在
// （启动自检 missingFrontendBindings 用的就是这个判据）
func TestContractMethodsExistOnApp(t *testing.T) {
	if missing := missingFrontendBindings(); len(missing) > 0 {
		t.Fatalf("契约里有 %d 个方法在 *App 上不存在: %v", len(missing), missing)
	}
}

// TestShimMatchesContract shim.js 与契约列表双向一致：
//   - 契约里列为「已接通」的，shim.js 必须真的 call("<名>")（否则是登记了没接线）；
//   - shim.js 里 call("<名>") 的，契约里必须有登记（否则是接线了没登记）。
//
// 同时校验前缀字面量一致，避免改了 Go 侧忘了改 JS。
func TestShimMatchesContract(t *testing.T) {
	const shimPath = "frontend/dist/shim.js"
	src, err := os.ReadFile(shimPath)
	if err != nil {
		t.Fatalf("读不到 %s: %v（测试须在 rewrite-go/ 包目录下运行）", shimPath, err)
	}
	code := string(src)

	if !strings.Contains(code, `"`+bindingFQN+`"`) {
		t.Errorf("shim.js 里找不到绑定前缀字面量 %q，与 contract.go 不一致", bindingFQN)
	}

	inShim := map[string]bool{}
	for _, m := range regexp.MustCompile(`call\("([A-Za-z0-9_]+)"`).FindAllStringSubmatch(code, -1) {
		inShim[m[1]] = true
	}

	inContract := map[string]bool{}
	for _, n := range wiredCalls {
		inContract[n] = true
	}

	for _, n := range wiredCalls {
		if !inShim[n] {
			t.Errorf("契约把 %s 列为已接通，但 shim.js 里没有 call(%q) —— 登记了没接线", n, n)
		}
	}
	for n := range inShim {
		if !inContract[n] {
			t.Errorf("shim.js 调用了 %s，但 contract.go 的 wiredCalls 里没有登记 —— 接线了没登记", n)
		}
	}
	// 待接通的不能出现在 shim 的 call 里（否则就是把 stub 换成真实调用却忘了登记）
	for _, n := range pendingCalls {
		if inShim[n] {
			t.Errorf("shim.js 已经调用 %s，应从 contract.go 的 pendingCalls 挪到 wiredCalls", n)
		}
	}
}
