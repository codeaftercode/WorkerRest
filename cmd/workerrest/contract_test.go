package main

import (
	"fmt"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// 本文件是“前端 ↔ Go 绑定”这条链路的安全网。
//
// 背景：为了把 上一版的渲染层（HTML/CSS/JS）原样复用，前端调用被改成
// Wails v3 的**按名字调用** `Call.ByName("main.App.<方法>")`。
// 代价是 JS 里拼错的名字、Go 侧改掉的方法名/参数个数，编译/类型检查/构建全都发现不了，
// 只在运行时静默失败（shim.js 的 catch 把 422 吞成 null → 界面悄悄降级）。
// 下面五个测试把这条链路的每一段都钉住（注册表层面的解析由 scripts/smoke_server.py
// 端到端覆盖：它走的就是前端同一条 HTTP 调用链）：
//   1. 前缀常量与 *App 的类型名一致；
//   2. 契约里的方法在 *App 上存在；
//   3. 契约里登记的**参数个数**与 Go 反射一致（本次新增）；
//   4. shim.js 的 ARITY 表 / 真实实参数组与 Go 侧契约一致（本次新增）；
//   5. *App 上**没有**未登记的导出方法（本次新增的反向断言）。

// TestBindingFQNPrefix 前缀常量必须停在 "main.App."。
//
// 为什么不拿 reflect 的值来比：**`go test` 与 `go build` 下 main 包的 PkgPath 不同**
// （测试二进制把被测包当库链接 → 返回导入路径 "workerrest"；构建产物 → "main"）。
// 这个差异让“用测试里的反射值当真理”变成一个假信号，本项目踩过一次：
// 单元测试一路绿灯，真实二进制却 422 unknown bound method name。
//
// 因此这里只钉两件能钉住的事：常量本身是 "main.App."，且类型名部分与 *App 一致。
// 真值校验交给 scripts/smoke_server.py —— 它跑真实二进制、按同名调用，
// 名字对不上会直接 422。
func TestBindingFQNPrefix(t *testing.T) {
	if bindingFQN != "main.App." {
		t.Fatalf("绑定前缀被改成了 %q，必须是 \"main.App.\"（main 包在构建产物里的 PkgPath 就是 main）\n"+
			"要同步修改 rewrite-go/contract.go 与 rewrite-go/web/dist/shim.js", bindingFQN)
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

// TestContractArityMatchesApp 契约登记的形参个数必须与 Go 侧反射一致，且双向无遗漏。
//
// 这条钉的是“名字对上了、参数个数对不上”这一类失败：Go 加/删一个参数时方法名不变，
// 只靠名字契约发现不了；而 Wails 会校验实参个数，多传少传都直接 422，
// 再被 shim.js 的 catch 吞成 null —— 界面上就是“功能突然不好使了，还没报错”。
func TestContractArityMatchesApp(t *testing.T) {
	typ := reflect.TypeOf(&App{})

	inWired := map[string]bool{}
	for _, n := range wiredCalls {
		inWired[n] = true
	}

	// 方向一：契约里的每个方法，wiredArity 都必须有条目（不许漏登记）
	var missing []string
	for _, n := range wiredCalls {
		if _, ok := wiredArity[n]; !ok {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("wiredCalls 里有 %d 个方法没在 wiredArity 登记形参个数: %v\n"+
			"（arity 是防“加参数导致前端 422 静默降级”的唯一闸口，必须逐个填）", len(missing), missing)
	}

	// 方向二：wiredArity 不许有多余条目（方法改名/删除后忘了清理）
	var extra []string
	for n := range wiredArity {
		if !inWired[n] {
			extra = append(extra, n)
		}
	}
	if len(extra) > 0 {
		sort.Strings(extra)
		t.Errorf("wiredArity 里有 %d 个条目不在 wiredCalls（方法改名或删除后忘同步）: %v", len(extra), extra)
	}

	// 方向三：条目值 == *App 上该方法声明的形参个数（NumIn 含接收者，减 1）
	var names []string
	for n := range wiredArity {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		m, ok := typ.MethodByName(n)
		if !ok {
			continue // 存在性由 TestContractMethodsExistOnApp 负责，这里不重复报
		}
		want := wiredArity[n]
		if got := m.Type.NumIn() - 1; got != want {
			t.Errorf("%s: wiredArity 登记 %d 个形参，*App 上实际 %d 个 —— 两边必须同时改\n"+
				"（改签名请同步 contract.go 的 wiredArity、web/dist/shim.js 的 ARITY 与调用处）",
				n, want, got)
		}
		if m.Type.IsVariadic() {
			t.Errorf("%s: 是变参方法，arity 契约没法表达（Wails 前端调用会展开最后一个参数）", n)
		}
	}
}

// TestNoUnregisteredExportedBindings 反向断言：*App 上任何导出方法都必须显式登记。
//
// 为什么需要：Wails 的 NewService 把结构体上**全部**导出方法反射成 main.App.<名>。
// 于是“Go 侧多了一个导出方法”这件事会静默扩大绑定面 —— 在 `-tags server` 构建
// （无头 HTTP 服务）下，任何能访问本机端口的进程都能 POST 调用它。
// 契约（wiredCalls）只管前端会调的那 58 个，剩下的必须显式登记到
// diagnosticCalls（诊断 / 内部通道）或 serviceLifecycleCalls（Wails 生命周期，不注册），
// 否则这里 fail —— 逼新增导出方法时回答一次“它该不该被注册成绑定”。
func TestNoUnregisteredExportedBindings(t *testing.T) {
	typ := reflect.TypeOf(&App{})

	allowed := map[string]string{}
	for _, n := range wiredCalls {
		allowed[n] = "wiredCalls（前端契约）"
	}
	for _, n := range pendingCalls {
		allowed[n] = "pendingCalls（待接通）"
	}
	for _, n := range diagnosticCalls {
		allowed[n] = "diagnosticCalls（诊断 / 内部通道）"
	}
	for _, n := range serviceLifecycleCalls {
		allowed[n] = "serviceLifecycleCalls（不注册为绑定）"
	}

	var unregistered []string
	for i := 0; i < typ.NumMethod(); i++ {
		name := typ.Method(i).Name
		if _, ok := allowed[name]; !ok {
			unregistered = append(unregistered, name)
		}
	}
	sort.Strings(unregistered)
	if len(unregistered) > 0 {
		t.Errorf("*App 上有 %d 个导出方法没有登记：%v\n"+
			"Wails 会把它们全部注册成 main.App.<名>（-tags server 下即对本机端口开放）。\n"+
			"要暴露给前端 → 进 contract.go 的 wiredCalls + wiredArity + shim.js；\n"+
			"只是内部用 → 进 diagnosticCalls 并写清用途；不是绑定 → 改名成非导出或收进 wrapper。",
			len(unregistered), unregistered)
	}

	// 登记表里不许有拼错/已删除的名字（否则白名单会悄悄失效）
	var phantom []string
	for name, where := range allowed {
		if _, ok := typ.MethodByName(name); !ok {
			phantom = append(phantom, fmt.Sprintf("%s（%s）", name, where))
		}
	}
	sort.Strings(phantom)
	if len(phantom) > 0 {
		t.Errorf("登记了 %d 个 *App 上不存在的名字: %v", len(phantom), phantom)
	}

	// 覆盖面自检：这份白名单必须恰好覆盖 *App 的全部导出方法，
	// 保证“注册面 = 白名单”是个可以拿来对数的不变式（58 wired + 诊断 + 生命周期）。
	if got, want := len(allowed), typ.NumMethod(); got != want {
		t.Errorf("白名单覆盖 %d 个名字，*App 有 %d 个导出方法 —— 差额说明有登记项不存在或有方法漏登记", got, want)
	}
}

// shimPath shim.js 的位置（测试在包目录下运行）
const shimPath = "web/dist/shim.js"

// arityBlockBegin / arityBlockEnd 标出 shim.js 里机器校验的 ARITY 表。
// 用注释当边界而不是靠缩进/变量名匹配：边界内的内容被 Go 测试逐条解析，
// 边界本身也是给改这段 JS 的人看的“这里不是随便写的常量”。
const (
	arityBlockBegin = "// >>> arity-begin"
	arityBlockEnd   = "// <<< arity-end"
)

// parseShimArity 从 shim.js 的 ARITY 区块里取出「方法名 → 形参个数」
func parseShimArity(t *testing.T, code string) map[string]int {
	t.Helper()
	i := strings.Index(code, arityBlockBegin)
	j := strings.Index(code, arityBlockEnd)
	if i < 0 || j < 0 || j < i {
		t.Fatalf("shim.js 里找不到 ARITY 区块边界（%q … %q）—— 删掉边界就会让 arity 校验失效",
			arityBlockBegin, arityBlockEnd)
	}
	block := code[i:j]
	out := map[string]int{}
	// 键允许带引号也允许裸标识符（shim.js 里是裸键，这里兼容两种写法）
	re := regexp.MustCompile(`["']?([A-Za-z0-9_]+)["']?\s*:\s*(\d+)`)
	for _, m := range re.FindAllStringSubmatch(block, -1) {
		var n int
		if _, err := fmt.Sscanf(m[2], "%d", &n); err != nil {
			t.Fatalf("ARITY 条目 %s 的个数 %q 不是整数: %v", m[1], m[2], err)
		}
		out[m[1]] = n
	}
	if len(out) == 0 {
		t.Fatalf("ARITY 区块解析出 0 条 —— 区块格式变了，测试失去意义")
	}
	return out
}

// parseShimCallSites 静态解析 shim.js 里每次 call("名"[, [...]]) 的实参个数。
//
// 返回 名 → 观察到的实参个数集合。之所以要真数实参数组而不是只信 ARITY 表：
// ARITY 只描述“应该是几个”，调用处写错（漏传一个、多传一个）时表本身看不出来，
// 而 422 恰恰是调用处引发的。两个都校验才算闭环。
func parseShimCallSites(t *testing.T, code string) map[string][]int {
	t.Helper()
	out := map[string][]int{}
	const marker = `call("`
	for pos := 0; ; {
		k := strings.Index(code[pos:], marker)
		if k < 0 {
			break
		}
		k += pos
		// 跳过注释里的示例（行内 "//" 之前的文字不算调用点）
		lineStart := strings.LastIndexByte(code[:k], '\n') + 1
		if strings.Contains(code[lineStart:k], "//") {
			pos = k + len(marker)
			continue
		}
		k += len(marker)
		nameEnd := strings.IndexByte(code[k:], '"')
		if nameEnd < 0 {
			t.Fatalf("shim.js 偏移 %d 处的 call(\" 没有闭合引号", k)
		}
		name := code[k : k+nameEnd]
		p := k + nameEnd + 1
		for p < len(code) && (code[p] == ' ' || code[p] == '\t' || code[p] == '\r' || code[p] == '\n') {
			p++
		}
		switch {
		case p < len(code) && code[p] == ')':
			out[name] = append(out[name], 0)
			pos = p + 1
		case p < len(code) && code[p] == ',':
			p++
			for p < len(code) && (code[p] == ' ' || code[p] == '\t' || code[p] == '\r' || code[p] == '\n') {
				p++
			}
			if p >= len(code) || code[p] != '[' {
				// 实参不是数组字面量（理论上不该出现）：不猜，跳过这一处
				t.Logf("跳过 %s 的调用处：实参不是数组字面量", name)
				pos = p
				continue
			}
			n, end := countArrayElems(t, code, p)
			out[name] = append(out[name], n)
			pos = end
		default:
			t.Fatalf("shim.js 里 call(%q ...) 的写法无法解析（偏移 %d）", name, p)
		}
	}
	return out
}

// countArrayElems 从 p 指向的 '[' 开始，数顶层逗号得出元素个数，返回个数与数组结束后的位置。
func countArrayElems(t *testing.T, code string, p int) (int, int) {
	t.Helper()
	depth := 0
	elems, seenNonSpace := 0, false
	var quote byte
	for i := p; i < len(code); i++ {
		c := code[i]
		if quote != 0 {
			if c == '\\' {
				i++ // 跳过转义字符
				continue
			}
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '"', '\'', '`':
			quote = c
			seenNonSpace = true
		case '[', '(', '{':
			depth++
			if depth > 1 {
				seenNonSpace = true
			}
		case ']', ')', '}':
			depth--
			if depth == 0 {
				if c != ']' {
					t.Fatalf("shim.js 数组字面量在偏移 %d 处以 %q 收尾，括号不配对", i, c)
				}
				// 顶层逗号数 + 1 = 元素个数（空数组是 0）
				if seenNonSpace {
					elems++
				}
				return elems, i + 1
			}
		case ',':
			if depth == 1 {
				elems++
			}
		default:
			if c != ' ' && c != '\t' && c != '\r' && c != '\n' {
				seenNonSpace = true
			}
		}
	}
	t.Fatalf("shim.js 从偏移 %d 起的数组字面量没有闭合", p)
	return 0, 0
}

// TestShimMatchesContract shim.js 与契约列表三向一致：
//   - 契约里列为“已接通”的，shim.js 必须真的 call("<名>")（否则是登记了没接线）；
//   - shim.js 里 call("<名>") 的，契约里必须有登记（否则是接线了没登记）；
//   - 每个调用处的**实参个数** == ARITY 表 == Go 侧 wiredArity（否则前端调用会 422）。
//
// 同时校验前缀字面量一致，避免改了 Go 侧忘了改 JS。
func TestShimMatchesContract(t *testing.T) {
	src, err := os.ReadFile(shimPath)
	if err != nil {
		t.Fatalf("读不到 %s: %v（测试须在 rewrite-go/ 包目录下运行）", shimPath, err)
	}
	code := string(src)

	if !strings.Contains(code, `"`+bindingFQN+`"`) {
		t.Errorf("shim.js 里找不到绑定前缀字面量 %q，与 contract.go 不一致", bindingFQN)
	}

	inContract := map[string]bool{}
	for _, n := range wiredCalls {
		inContract[n] = true
	}

	calls := parseShimCallSites(t, code)
	inShim := map[string]bool{}
	for n := range calls {
		inShim[n] = true
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

	// ---- arity 三向比对 ----
	shimArity := parseShimArity(t, code)

	for _, n := range wiredCalls {
		want, ok := wiredArity[n]
		if !ok {
			continue // TestContractArityMatchesApp 已经报过
		}
		got, ok := shimArity[n]
		if !ok {
			t.Errorf("wiredArity 登记了 %s（%d 个形参），但 shim.js 的 ARITY 表里没有 —— 加参数时的静默 422 缺口", n, want)
			continue
		}
		if got != want {
			t.Errorf("%s: shim.js 的 ARITY 写 %d，Go 侧 wiredArity 是 %d —— 两边必须同时改", n, got, want)
		}
	}
	for n := range shimArity {
		if !inContract[n] {
			t.Errorf("shim.js 的 ARITY 里登记了 %s，但它不在 wiredCalls —— ARITY 只描述已接通的绑定", n)
		}
	}

	for n, observed := range calls {
		want, ok := shimArity[n]
		if !ok {
			continue // 未登记的方法不校验（向后兼容：新接线忘了登表也不至于炸现有调用）
		}
		for _, got := range observed {
			if got != want {
				t.Errorf("%s: 调用处传了 %d 个实参，ARITY 表写 %d —— 会 422 后静默降级成 null", n, got, want)
			}
		}
	}
}
