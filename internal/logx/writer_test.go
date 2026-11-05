package logx

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// teeWriter 是「写文件为主 + 抄一份到控制台」的那层：文件写失败要能如实上报
// （控制台成功不能掩盖落盘失败），控制台失败则不许影响返回值。
func TestTeeWriterPrefersFileResult(t *testing.T) {
	var file, console bytes.Buffer
	w := &teeWriter{file: &file, console: &console}

	n, err := w.Write([]byte("hello"))
	if err != nil || n != 5 {
		t.Fatalf("写入应成功，得到 n=%d err=%v", n, err)
	}
	if file.String() != "hello" || console.String() != "hello" {
		t.Fatalf("两边都该收到内容，得到 file=%q console=%q", file.String(), console.String())
	}

	// 没有控制台（GUI 无 stderr）时只写文件
	w2 := &teeWriter{file: &bytes.Buffer{}}
	if _, err := w2.Write([]byte("x")); err != nil {
		t.Fatalf("console 为 nil 时应只写文件，得到 %v", err)
	}

	// 文件写失败 → 错误要冒出来（不能因为控制台写成功就吞掉）
	w3 := &teeWriter{file: failingWriter{}, console: &bytes.Buffer{}}
	if _, err := w3.Write([]byte("x")); err == nil {
		t.Fatal("文件写失败时应返回错误")
	}
}

type failingWriter struct{}

func (failingWriter) Write(p []byte) (int, error) { return 0, os.ErrClosed }

// AppLogger 绝不能回 nil：Wails 运行时拿到 nil 日志器就崩在别人的调用栈里，
// 排查时会误以为是框架问题。
func TestAppLoggerNeverNil(t *testing.T) {
	l := AppLogger()
	if l == nil {
		t.Fatal("AppLogger 不许返回 nil")
	}
	// 真写一行，确认不会 panic（默认写 <exe>/logs 或用户缓存目录）
	l.Info("测试日志", "k", "v")

	t.Setenv("WR_LOG_LEVEL", "debug")
	if l := AppLogger(); l == nil {
		t.Fatal("debug 级别下同样不许为 nil")
	}
}

// appLogDirs 的候选顺序：先 exe 旁的 logs/，再用户缓存目录。
// 顺序错了会让「便携包旁边可写却写进用户目录」这种问题悄悄发生。
func TestAppLogDirsOrder(t *testing.T) {
	dirs := appLogDirs()
	if len(dirs) == 0 {
		t.Fatal("至少要有一个候选目录")
	}
	if !strings.HasSuffix(filepath.ToSlash(dirs[0]), "/logs") {
		t.Fatalf("第一个候选应是 logs 目录，得到 %q", dirs[0])
	}
	if len(dirs) > 1 && !strings.Contains(filepath.ToSlash(dirs[1]), "WorkerRest") {
		t.Fatalf("第二个候选应是用户缓存下的 WorkerRest，得到 %q", dirs[1])
	}
}
