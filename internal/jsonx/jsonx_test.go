package jsonx

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 这组用例钉的是“与前端 JS 同口径”的兜底行为：配置可能被用户手改成品类不符的值
// （name 写成数字、slots 写成对象、id 写成 1），Go 侧读到的东西必须与前端 String(v) /
// Number(v) / Array.isArray 一致，否则同一条配置在两边解释不同。

func TestStrOfOnlyAcceptsString(t *testing.T) {
	if got := StrOf("abc"); got != "abc" {
		t.Fatalf("字符串应原样返回，得到 %q", got)
	}
	for _, v := range []any{nil, 1.0, true, []any{"x"}, map[string]any{}} {
		if got := StrOf(v); got != "" {
			t.Fatalf("%T 不该被当成字符串，得到 %q", v, got)
		}
	}
}

func TestJsStrMatchesJSStringOf(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{nil, ""},
		{"", ""},
		{"文本", "文本"},
		{true, "true"},
		{false, "false"},
		{0.0, "0"},
		{1.0, "1"}, // JS String(1) === "1"，不能是 "1.000000"
		{1.5, "1.5"},
		{-2.25, "-2.25"},
		{1e6, "1000000"}, // FormatFloat 'f' = JS 的定点写法（1e6 在 JS 里也是 "1000000"）
		{math.NaN(), "NaN"},
		{math.Inf(1), "Infinity"},
		{math.Inf(-1), "-Infinity"},
		{[]any{"a"}, ""}, // JS 会拼成 "a"，但配置里不该出现数组当字符串用 → 宁可空
	}
	for _, c := range cases {
		if got := JsStr(c.in); got != c.want {
			t.Fatalf("JsStr(%#v) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

func TestToNumberCoversJSNumberSemantics(t *testing.T) {
	// JS: Number(null)=0, Number("")=0, Number(false)=0, Number(true)=1, Number("  7 ")=7
	ok := []struct {
		in   any
		want float64
	}{
		{nil, 0}, {"", 0}, {"   ", 0}, {false, 0}, {true, 1},
		{"7", 7}, {"  7 ", 7}, {"-1.5", -1.5}, {"1e3", 1000}, {42.5, 42.5}, {0.0, 0},
	}
	for _, c := range ok {
		got, good := ToNumber(c.in)
		if !good || got != c.want {
			t.Fatalf("ToNumber(%#v) = (%v,%v)，期望 (%v,true)", c.in, got, good, c.want)
		}
	}
	// 解析不出来要报 ok=false，让调用方走 fallback（不能悄悄变 0）
	bad := []any{"abc", "12px", "NaN", "Infinity", math.NaN(), math.Inf(1), []any{}, map[string]any{}}
	for _, v := range bad {
		if _, good := ToNumber(v); good {
			t.Fatalf("ToNumber(%#v) 应报解析失败", v)
		}
	}
}

func TestAnySliceToNumbersRoundTrip(t *testing.T) {
	if got := AnySlice("x"); got != nil {
		t.Fatalf("非数组应给 nil，得到 %#v", got)
	}
	if got := AnySlice(map[string]any{"0": 1}); got != nil {
		t.Fatalf("对象不该被当成数组，得到 %#v", got)
	}
	arr := []any{"a", "b"}
	if got := AnySlice(arr); len(got) != 2 {
		t.Fatalf("数组应原样返回，得到 %#v", got)
	}

	conv := StringSliceToAny([]string{"a", "b"})
	if len(conv) != 2 || conv[0] != "a" || conv[1] != "b" {
		t.Fatalf("StringSliceToAny = %#v", conv)
	}
	if got := StringSliceToAny(nil); got == nil || len(got) != 0 {
		t.Fatalf("nil 应转成空数组而不是 nil（写进 JSON 要 [] 不要 null），得到 %#v", got)
	}

	mixed := []any{"a", 1.0, nil, "b", true}
	got := AnySliceToStrings(mixed)
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("非字符串项应被跳过且保持顺序，得到 %#v", got)
	}
	if got := AnySliceToStrings("a"); got != nil {
		t.Fatalf("非数组应给 nil，得到 %#v", got)
	}
}

func TestNewRandomIDShape(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		id := NewRandomID()
		parts := strings.Split(id, "-")
		if len(parts) != 5 {
			t.Fatalf("应为 8-4-4-4-12 五段，得到 %q", id)
		}
		want := []int{8, 4, 4, 4, 12}
		for j, n := range want {
			if len(parts[j]) != n {
				t.Fatalf("%q 第 %d 段长度应为 %d", id, j+1, n)
			}
		}
		if parts[2][0] != '4' {
			t.Fatalf("第 3 段应以 4 开头（v4 形态），得到 %q", id)
		}
		if !strings.ContainsRune("89ab", rune(parts[3][0])) {
			t.Fatalf("第 4 段应以 8/9/a/b 开头（variant 位），得到 %q", id)
		}
		if seen[id] {
			t.Fatalf("500 次内出现重复 id %q", id)
		}
		seen[id] = true
	}
}

func TestItemIDAcceptsStringAndNumber(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{" 7 ", "7"}, // 字符串两边空格要削
		{7.0, "7"},   // 手改配置写成 1 而不是 "1"
		{float64(3), "3"},
		{0.0, "0"},
		{-1.0, ""}, // 负数不是合法 id
		{1.5, ""},  // 小数不是合法 id
		{"", ""},
		{nil, ""},
		{true, ""},
	}
	for _, c := range cases {
		if got := ItemID(c.in); got != c.want {
			t.Fatalf("ItemID(%#v) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

func TestNextSeqIDTakesMaxPlusOne(t *testing.T) {
	if got := NextSeqID(nil); got != "1" {
		t.Fatalf("空列表应从 1 起，得到 %q", got)
	}
	// 取最大值 +1 而不是最小空位：删掉中间那条后不复用刚删的号，fixedId 更不容易指错
	if got := NextSeqID([]string{"1", "3", "2"}); got != "4" {
		t.Fatalf("应取最大值+1，得到 %q", got)
	}
	if got := NextSeqID([]string{"builtin-short", "abc", ""}); got != "1" {
		t.Fatalf("没有数字 id 时应从 1 起，得到 %q", got)
	}
	if got := NextSeqID([]string{"10"}); got != "11" {
		t.Fatalf("得到 %q", got)
	}
}

func TestIsSeqID(t *testing.T) {
	for _, s := range []string{"1", "42", "007"} {
		if !IsSeqID(s) {
			t.Fatalf("%q 应算序号", s)
		}
	}
	for _, s := range []string{"", "0", "-1", "1.5", "1a", "abc", " "} {
		if IsSeqID(s) {
			t.Fatalf("%q 不该算序号", s)
		}
	}
}

func TestSeqIDForKeepsStableNumbers(t *testing.T) {
	ids := []string{"1", "2"}
	// 已经是正整数且不冲突 → 原样保留（可能不连续，但稳定）
	if got := SeqIDFor("5", ids, map[string]bool{}); got != "5" {
		t.Fatalf("成熟序号应原样保留，得到 %q", got)
	}
	// 重号 → 重新编号
	if got := SeqIDFor("2", ids, map[string]bool{"2": true}); got != "3" {
		t.Fatalf("重号应重新编号，得到 %q", got)
	}
	// 随机串 / builtin-xxx-N → 重新编号
	for _, old := range []string{"9b1deb4d-3b7d", "builtin-short-1", "乱码", ""} {
		if got := SeqIDFor(old, ids, map[string]bool{}); got != "3" {
			t.Fatalf("SeqIDFor(%q) 应重新编号成 3，得到 %q", old, got)
		}
	}
}

func TestRemapMsgID(t *testing.T) {
	remap := map[string]string{"a": "1", "b": "2"}
	if got := RemapMsgID(remap, "a"); got != "1" {
		t.Fatalf("应映射到新 id，得到 %q", got)
	}
	if got := RemapMsgID(remap, "z"); got != "z" {
		t.Fatalf("映射不到应原样返回，得到 %q", got)
	}
	if got := RemapMsgID(remap, ""); got != "" {
		t.Fatalf("空 id 应保持空，得到 %q", got)
	}
	if got := RemapMsgID(nil, "a"); got != "a" {
		t.Fatalf("空映射表应原样返回，得到 %q", got)
	}
}

func TestAsMap(t *testing.T) {
	m := map[string]any{"k": 1}
	if got := AsMap(m); got["k"] != 1 {
		t.Fatalf("对象应原样返回，得到 %#v", got)
	}
	for _, v := range []any{nil, "x", []any{1}} {
		got := AsMap(v)
		if got == nil || len(got) != 0 {
			t.Fatalf("AsMap(%#v) 应给空 map（调用方可直接赋值），得到 %#v", v, got)
		}
	}
}

func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sub", "settings.json")

	// 目录不存在要自己建（首跑时 <exe>/config 还没有）
	if err := WriteFileAtomic(p, []byte(`{"a":1}`)); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	got, err := os.ReadFile(p)
	if err != nil || string(got) != `{"a":1}` {
		t.Fatalf("读回 %q err=%v", got, err)
	}
	// 临时文件不许留在目录里
	entries, err := os.ReadDir(filepath.Dir(p))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("临时文件应被改名吃掉，却留下 %q", e.Name())
		}
	}
	// 覆盖写（Save 会反复走这条路径）
	if err := WriteFileAtomic(p, []byte(`{"a":2}`)); err != nil {
		t.Fatalf("覆盖写失败: %v", err)
	}
	got, _ = os.ReadFile(p)
	if string(got) != `{"a":2}` {
		t.Fatalf("覆盖写后读回 %q", got)
	}
}

func TestWriteFileAtomicFailsCleanlyWhenTargetIsDir(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "settings.json")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	// 目标位置被同名目录占着 → 改名失败，但**不能留垃圾、不能 panic**
	if err := WriteFileAtomic(target, []byte("x")); err == nil {
		t.Fatal("目标被目录占着时应报错")
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != "settings.json" {
			t.Fatalf("失败路径不该留临时文件，却留下 %q", e.Name())
		}
	}
}
