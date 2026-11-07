package logx

import (
	"bytes"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// 日志治理的回归：单条闸门（媒体拒收 / 超长截断）、slog 属性脱敏、
// debug 级旁路播报、按时间滚动、过期分包清理、控制台失效熔断。
//
// 背景（2026-11-05 真机）：一份 11.2 MB 的 app.log 里有 6.78 MB 是
// Wails debug 绑定日志带出的立绘 base64，最长单行 266 KB；而滚动始终没生效，
// 目录里只有 app.log 一份。这组用例把两条根因都钉死。

func TestGuardWriterPassesShortLines(t *testing.T) {
	var buf bytes.Buffer
	w := wrapGuarded(&buf)
	line := "2026/11/05 21:00:00 [boot] 启动完成 serverMode=false\n"
	n, err := w.Write([]byte(line))
	if err != nil || n != len(line) {
		t.Fatalf("短日志应原样透传：n=%d err=%v", n, err)
	}
	if buf.String() != line {
		t.Fatalf("短日志被改写：%q", buf.String())
	}
}

func TestGuardWriterRejectsMediaContent(t *testing.T) {
	cases := []struct {
		name    string
		payload string
	}{
		{"立绘 DataURL", "[role] thumb=data:image/webp;base64," + strings.Repeat("A", 4096)},
		{"音频 DataURL", "[sound] data:audio/wav;base64," + strings.Repeat("B", 4096)},
		{"视频 DataURL", "[x] data:video/mp4;base64," + strings.Repeat("C", 4096)},
		{"裸 base64 大块", strings.Repeat("QUJD", 1000)}, // 全 base64 字母表、无空格
		{"二进制 NUL", "[x] " + strings.Repeat("\x00", 600)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			p := []byte(c.payload + "\n")
			n, err := wrapGuarded(&buf).Write(p)
			if err != nil || n != len(p) {
				t.Fatalf("应消费全部入参：n=%d err=%v", n, err)
			}
			got := buf.String()
			if !strings.Contains(got, "已忽略疑似媒体/二进制内容") {
				t.Fatalf("媒体内容没被拦截：%q", got[:min(len(got), 200)])
			}
			if len(got) > 200 {
				t.Fatalf("原文仍落盘（%d 字节）：%q", len(got), got[:200])
			}
			if strings.Contains(got, "data:image/") || strings.Contains(got, "QUJD") {
				t.Fatalf("摘要里不该带原文片段：%q", got)
			}
		})
	}
}

func TestGuardWriterTruncatesLongLine(t *testing.T) {
	var buf bytes.Buffer
	// 每个“错误”6 字节，8192 不是 6 的倍数 —— 顺带验证不切在 UTF-8 字符中间。
	line := "[x] " + strings.Repeat("错误", 6000)
	p := []byte(line + "\n")
	if _, err := wrapGuarded(&buf).Write(p); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, "已省略") {
		t.Fatalf("超长行该带截断标注：%q", got[:min(len(got), 120)])
	}
	if len(got) > logMaxLineBytes+200 {
		t.Fatalf("截断后仍过长：%d 字节", len(got))
	}
	if !utf8.ValidString(got) {
		t.Fatal("截断把 UTF-8 字符切坏了")
	}
}

func TestGuardWriterKeepsLineStructure(t *testing.T) {
	var buf bytes.Buffer
	media := "[role] " + "data:image/webp;base64," + strings.Repeat("A", 4096)
	p := []byte("first\n" + media + "\nlast\n")
	if _, err := wrapGuarded(&buf).Write(p); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("应保持 3 行，实得 %d 行: %q", len(lines), buf.String()[:min(len(buf.String()), 300)])
	}
	if lines[0] != "first" || lines[2] != "last" {
		t.Fatalf("相邻正常行被误伤：%q / %q", lines[0], lines[2])
	}
	if !strings.Contains(lines[1], "已忽略") {
		t.Fatalf("中间那行该换成摘要：%q", lines[1])
	}
}

// 短行里只是“提到”data:image/ 或 base64 的正常错误文案，不能被当成媒体内容吃掉 ——
// 它们是排查时最需要留下的那类信息。
func TestGuardWriterKeepsShortErrorText(t *testing.T) {
	for _, line := range []string{
		"[settings] 报表数据 base64 解码失败: illegal base64 data\n",
		"[role] 不支持 data:image/gif 格式\n",
	} {
		var buf bytes.Buffer
		if _, err := wrapGuarded(&buf).Write([]byte(line)); err != nil {
			t.Fatalf("写入失败: %v", err)
		}
		if buf.String() != line {
			t.Fatalf("正常错误文案被误伤：%q → %q", line, buf.String())
		}
	}
}

func TestGuardWriterPropagatesWriteError(t *testing.T) {
	w := wrapGuarded(failingWriter{})
	if _, err := w.Write([]byte("normal line\n")); err == nil {
		t.Fatal("下游写失败时错误要往上冒")
	}
}

// slog 属性脱敏：从源头挡住 Wails debug 绑定日志里的 result/args，
// 同时保留 method= 这种真正有用的排查信息。
func TestTrimLogAttr(t *testing.T) {
	t.Run("媒体属性整条拒收", func(t *testing.T) {
		in := slog.String("result", "[{\"thumb\":\"data:image/webp;base64,"+strings.Repeat("A", 4096)+"\"}]")
		got := trimLogAttr(nil, in)
		if !strings.Contains(got.Value.String(), "已忽略媒体") {
			t.Fatalf("媒体属性没被拒收：%q", got.Value.String())
		}
		if strings.Contains(got.Value.String(), "data:image/") {
			t.Fatalf("摘要里不该残留媒体前缀：%q", got.Value.String())
		}
	})

	t.Run("超长普通属性截断", func(t *testing.T) {
		in := slog.String("args", strings.Repeat("配置", 3000))
		got := trimLogAttr(nil, in)
		s := got.Value.String()
		if !strings.Contains(s, "已省略") {
			t.Fatalf("超长属性该带截断标注：%q", s[:min(len(s), 120)])
		}
		if len(s) > logMaxAttrBytes+100 || !utf8.ValidString(s) {
			t.Fatalf("截断结果不合法（%d 字节）：%q", len(s), s[:min(len(s), 120)])
		}
	})

	t.Run("短属性与非字符串原样", func(t *testing.T) {
		if got := trimLogAttr(nil, slog.String("method", "main.App.ListRoles")); got.Value.String() != "main.App.ListRoles" {
			t.Fatalf("短属性不该被动：%q", got.Value.String())
		}
		if got := trimLogAttr(nil, slog.Int("code", 200)); got.Value.Int64() != 200 {
			t.Fatalf("非字符串属性不该被动：%v", got.Value)
		}
	})
}

// 端到端回放真机现场：Wails debug 绑定日志把 ListRoles 的整份立绘 base64
// 放进 result=。源头脱敏 + 落盘闸门两层之后，日志要留下方法名、丢掉图片、体积不失控。
func TestMediaContentNeverLandsInLog(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(slog.NewTextHandler(wrapGuarded(&buf), &slog.HandlerOptions{
		Level:       slog.LevelDebug, // 回放现场：探针把级别顶到 debug 时才会打绑定日志
		ReplaceAttr: trimLogAttr,
	}))
	l.Debug("Binding call complete:",
		"method", "main.App.ListRoles",
		"result", `[{"thumb":"data:image/webp;base64,`+strings.Repeat("A", 20000)+`"}]`)

	got := buf.String()
	if strings.Contains(got, "data:image/") || strings.Contains(got, strings.Repeat("A", 64)) {
		t.Fatalf("图片内容仍落盘：%q", got[:min(len(got), 300)])
	}
	if !strings.Contains(got, "main.App.ListRoles") {
		t.Fatalf("排查用的方法名不该一起丢掉：%q", got)
	}
	if len(got) > 2048 {
		t.Fatalf("一行日志不该长到 %d 字节：%q", len(got), got[:300])
	}
}

// Debugf 默认静默，只有 WR_LOG_LEVEL=debug 才输出 —— 未开启时连格式化都不做。
func TestDebugfGatedByLogLevel(t *testing.T) {
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(old)

	t.Setenv("WR_LOG_LEVEL", "")
	Debugf("不该出现 %d", 1)
	if buf.Len() != 0 {
		t.Fatalf("默认级别下 Debugf 不该写日志：%q", buf.String())
	}

	t.Setenv("WR_LOG_LEVEL", "warn")
	Debugf("也不该出现 %d", 2)
	if buf.Len() != 0 {
		t.Fatalf("warn 级别下 Debugf 不该写日志：%q", buf.String())
	}

	t.Setenv("WR_LOG_LEVEL", "debug")
	Debugf("该出现 %d", 3)
	if !strings.Contains(buf.String(), "该出现 3") {
		t.Fatalf("debug 级别下 Debugf 应输出：%q", buf.String())
	}
}

// 控制台写失败（GUI 子系统无 stderr）后永久停掉抄写，
// 否则每条日志都白耗一次系统调用。
type countConsole struct{ calls int }

func (c *countConsole) Write([]byte) (int, error) {
	c.calls++
	return 0, os.ErrClosed
}

func TestTeeWriterStopsAfterConsoleFailure(t *testing.T) {
	var file bytes.Buffer
	console := &countConsole{}
	w := &teeWriter{file: &file, console: console}
	for _, s := range []string{"a", "b", "c"} {
		if _, err := w.Write([]byte(s)); err != nil {
			t.Fatalf("控制台失败不许影响返回值: %v", err)
		}
	}
	if file.String() != "abc" {
		t.Fatalf("文件仍要写全：%q", file.String())
	}
	if console.calls != 1 {
		t.Fatalf("控制台只该试一次，实际 %d 次", console.calls)
	}
}

// 按时间滚动：一份日志不该横跨好几天，即使远没写满也要分包。
func TestRotatingLogRotatesByAge(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, logFileName)
	base := time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)
	now := base
	clock := func() time.Time { return now }

	w, err := newRotatingLog(path, 10000, 2, 24*time.Hour, clock)
	if err != nil {
		t.Fatalf("newRotatingLog: %v", err)
	}
	defer w.Close()
	writeN(t, w, 10, 'a')

	now = base.Add(25 * time.Hour)
	writeN(t, w, 10, 'b')

	if got := readFileT(t, path); got != strings.Repeat("b", 10) {
		t.Fatalf("当前份应只剩跨天后的内容：%q", got)
	}
	if got := readFileT(t, path+".1"); got != strings.Repeat("a", 10) {
		t.Fatalf("跨天前的内容应挪到 .1：%q", got)
	}
}

// 没跨天就绝不因为时间而滚动。
func TestRotatingLogNoAgeRotateWithinWindow(t *testing.T) {
	path := filepath.Join(t.TempDir(), logFileName)
	base := time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)
	now := base
	w, err := newRotatingLog(path, 10000, 2, 24*time.Hour, func() time.Time { return now })
	if err != nil {
		t.Fatalf("newRotatingLog: %v", err)
	}
	defer w.Close()
	writeN(t, w, 10, 'a')
	now = base.Add(23 * time.Hour)
	writeN(t, w, 10, 'b')
	if got := readFileT(t, path); got != strings.Repeat("a", 10)+strings.Repeat("b", 10) {
		t.Fatalf("未跨天不该滚动：%q", got)
	}
	if _, err := os.Stat(path + ".1"); !os.IsNotExist(err) {
		t.Fatalf("未跨天不该生成 .1（err=%v）", err)
	}
}

// 过期分包清理：只删 app.log*，不动同目录的其它文件；当前份永远保留。
func TestPruneOldLogsOnlyRemovesStaleLogParts(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	stale := now.Add(-10 * 24 * time.Hour)
	fresh := now.Add(-time.Hour)

	touch := func(name, content string, at time.Time) {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile %s: %v", name, err)
		}
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatalf("Chtimes %s: %v", name, err)
		}
	}
	touch(logFileName, "current", fresh)
	touch(logFileName+".1", "old", stale)
	touch(logFileName+".2", "older", stale)
	touch(logFileName+".old", "archived", stale)
	touch("config.json", "user data", stale)
	// 名字相近但不是日志分包的文件也不能碰
	touch("app.logx", "not a log", stale)

	pruneOldLogs(dir, now, 7*24*time.Hour)

	for _, name := range []string{logFileName, "config.json", "app.logx"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s 不该被删: %v", name, err)
		}
	}
	for _, name := range []string{logFileName + ".1", logFileName + ".2", logFileName + ".old"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("%s 是 10 天前的分包，应被删（err=%v）", name, err)
		}
	}
}

// 异常超大的分包即使“今天刚写”也要删 —— 只按时间判据，历史遗留的
// 11 MB 巨无霸会一直赖在盘上。
func TestPruneOldLogsRemovesOversizedPart(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	write := func(name string, size int) {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, bytes.Repeat([]byte("x"), size), 0o644); err != nil {
			t.Fatalf("WriteFile %s: %v", name, err)
		}
		if err := os.Chtimes(p, now, now); err != nil {
			t.Fatal(err)
		}
	}
	write(logFileName+".1", int(2*logMaxBytes)+1) // 刚写的，但明显超标
	write(logFileName+".2", 1024)                 // 刚写的、正常大小

	pruneOldLogs(dir, now, 7*24*time.Hour)

	if _, err := os.Stat(filepath.Join(dir, logFileName+".1")); !os.IsNotExist(err) {
		t.Fatalf("超标的当天分包也该被删（err=%v）", err)
	}
	if _, err := os.Stat(filepath.Join(dir, logFileName+".2")); err != nil {
		t.Fatalf("正常大小的近期分包该保留: %v", err)
	}
}

func TestPruneOldLogsRetainNonPositiveIsNoop(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, logFileName+".1")
	if err := os.WriteFile(p, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	pruneOldLogs(dir, time.Now(), 0)
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("retain<=0 时不该删任何文件: %v", err)
	}
}
