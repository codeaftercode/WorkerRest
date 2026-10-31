package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 日志落盘与滚动的回归。
//
// 这组用例守的是「排查通道」本身：正式包没有控制台，出事时唯一能看到现场的就是
// 这份 app.log。滚动写错（不滚 = 无限长；滚错 = 把最新的一条挪走或写丢）比没日志
// 更难发现，所以按字节数逐条核对。

func readFileT(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 %s 失败: %v", path, err)
	}
	return string(b)
}

func writeN(t *testing.T, w *rotatingLogWriter, n int, fill byte) {
	t.Helper()
	if _, err := w.Write([]byte(strings.Repeat(string(fill), n))); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
}

// TestRotatingLogWriteBelowCap 未超上限时不产生 .1。
func TestRotatingLogWriteBelowCap(t *testing.T) {
	path := filepath.Join(t.TempDir(), logFileName)
	w, err := openRotatingLog(path, 100, 2)
	if err != nil {
		t.Fatalf("openRotatingLog: %v", err)
	}
	defer w.Close()

	writeN(t, w, 40, 'a')
	writeN(t, w, 40, 'b')

	if got := readFileT(t, path); len(got) != 80 {
		t.Fatalf("app.log 长度 = %d, want 80", len(got))
	}
	if _, err := os.Stat(path + ".1"); !os.IsNotExist(err) {
		t.Fatalf("未超上限就不该生成 %s.1（err=%v）", logFileName, err)
	}
}

// TestRotatingLogRotatesAtCap 超上限时旧内容挪到 .1，新内容写进新的 app.log。
func TestRotatingLogRotatesAtCap(t *testing.T) {
	path := filepath.Join(t.TempDir(), logFileName)
	w, err := openRotatingLog(path, 100, 2)
	if err != nil {
		t.Fatalf("openRotatingLog: %v", err)
	}
	defer w.Close()

	writeN(t, w, 60, 'a')
	writeN(t, w, 60, 'b')

	if got := readFileT(t, path); got != strings.Repeat("b", 60) {
		t.Fatalf("app.log 应只剩第二条: %q", got)
	}
	if got := readFileT(t, path+".1"); got != strings.Repeat("a", 60) {
		t.Fatalf("app.log.1 应是第一条: %q", got)
	}
}

// TestRotatingLogKeepsAtMostKeepPlusOne 反复滚动后文件数固定在 keep+1。
func TestRotatingLogKeepsAtMostKeepPlusOne(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, logFileName)
	w, err := openRotatingLog(path, 100, 2)
	if err != nil {
		t.Fatalf("openRotatingLog: %v", err)
	}
	for i := 0; i < 8; i++ {
		writeN(t, w, 60, byte('a'+i))
	}
	_ = w.Close()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 3 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("滚动后应只有 app.log/.1/.2 三份，实得 %d 份: %v", len(entries), names)
	}
}

// TestRotatingLogContinuesSizeAfterReopen 进程重启（重新打开同一份）后仍按累计大小判断。
func TestRotatingLogContinuesSizeAfterReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), logFileName)
	w, err := openRotatingLog(path, 100, 2)
	if err != nil {
		t.Fatalf("openRotatingLog: %v", err)
	}
	writeN(t, w, 60, 'a')
	_ = w.Close()

	w2, err := openRotatingLog(path, 100, 2)
	if err != nil {
		t.Fatalf("重开失败: %v", err)
	}
	defer w2.Close()
	writeN(t, w2, 60, 'b')

	if got := readFileT(t, path+".1"); got != strings.Repeat("a", 60) {
		t.Fatalf("重开后应在写满时滚动，.1 = %q", got)
	}
}

// TestAppLogDirsFirstIsExeLogs 首选目录必须是 exe 同级的 logs/。
func TestAppLogDirsFirstIsExeLogs(t *testing.T) {
	dirs := appLogDirs()
	if len(dirs) == 0 {
		t.Fatal("appLogDirs 不能为空")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("取不到可执行文件路径: %v", err)
	}
	want := filepath.Join(filepath.Dir(exe), "logs")
	if dirs[0] != want {
		t.Fatalf("首选日志目录 = %q, want %q", dirs[0], want)
	}
}

// TestAppLogWriterCreatesFile 默认落点真的能建出文件（不依赖 cwd 可写）。
func TestAppLogWriterCreatesFile(t *testing.T) {
	// 用 WR_LOG_FILE 之外的真实路径不好断言（会写到 exe 旁边），只验证 writer 本身可写。
	path := filepath.Join(t.TempDir(), "sub", logFileName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	w, err := openRotatingLog(path, logMaxBytes, logKeepFiles)
	if err != nil {
		t.Fatalf("openRotatingLog: %v", err)
	}
	defer w.Close()
	if _, err := w.Write([]byte("hello\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := readFileT(t, path); got != "hello\n" {
		t.Fatalf("落盘内容 = %q", got)
	}
	if logMaxBytes != 2<<20 {
		t.Fatalf("单份上限被改动（%d），请同步注释与打包校验", logMaxBytes)
	}
}
