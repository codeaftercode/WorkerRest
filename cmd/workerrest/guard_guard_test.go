package main

import (
	"errors"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"workerrest/internal/logx"
)

// 每个起了后台 goroutine 的生产文件里都必须出现兜底调用。
//
// 这是域3-4 的防回归线：新增一条后台循环、忘了包 GuardRun，测试直接红。
// 判定口径故意宽松 —— 只看「文件里有没有 logx.GuardRun / logx.GuardRunOK」，
// 不解析具体是哪一行：有的循环（scheduler.loop / hotkeys.run / watchLoop）在函数
// 内部包，外部调用点看不出，逐行匹配只会制造误报。宽松口径同样能拦住
// 「新文件 + 新 goroutine + 零兜底」这个真问题。
//
// 扫描范围是 cmd/ + internal/ 的**全部**生产文件，不是本包目录。曾经这里只
// filepath.Glob("*.go")，源码下沉到 internal/ 之后，stats 那两条后台循环就整片
// 移出了视野 —— 断言还在跑、还绿，只是不再看守新家。
func TestBackgroundGoroutinesAreGuarded(t *testing.T) {
	start := regexp.MustCompile(`(?m)^\s*go\s+\S`)
	root := repoRoot(t)
	var files []string
	for _, sub := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, sub), func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() && strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go") {
				files = append(files, p)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("遍历 %s 失败：%v", sub, err)
		}
	}
	checked := 0
	for _, p := range files {
		src, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("读 %s 失败：%v", p, err)
		}
		if !start.Match(src) {
			continue
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			rel = p
		}
		checked++
		if !strings.Contains(string(src), "GuardRun") {
			t.Errorf("%s 起了后台 goroutine 却没有 panic 兜底：把循环体包进 logx.GuardRun（见 internal/logx/recoverguard.go）", rel)
		}
	}
	if checked < 5 {
		t.Fatalf("只检查到 %d 个文件，扫描口径可能失效了（预期 ≥5）", checked)
	}
}

// 熔断路径必须真的以退出码 1 结束，并在日志里留下现场。
//
// 这条只能在子进程里验：fatalPanicExit 会 os.Exit，跑在主测试进程里等于把整套测试干掉。
// 父进程只断言「退出码 + 日志内容」—— 退出码 1 单独看不够（编译错、断言失败也是 1），
// 关键是日志里那句「判定为不可恢复」。
func TestPanicFuseExitsWithCodeOne(t *testing.T) {
	if os.Getenv("WR_PANIC_FUSE_CHILD") == "1" {
		logx.SetHeadless(true)             // 子进程模拟无头环境：熔断时不弹系统框（会卡住自动化）
		log.SetOutput(logx.AppLogWriter()) // 用 WR_LOG_FILE 指到父进程给的临时文件
		for i := 0; i < logx.PanicBurstLimit; i++ {
			logx.GuardRun("test.fuse", func() { panic("fuse") })
		}
		t.Fatal("连续 panic 达到阈值后应当已经退出进程，走到这里说明熔断没生效")
	}

	logPath := filepath.Join(t.TempDir(), "fuse.log")
	cmd := exec.Command(os.Args[0], "-test.run=TestPanicFuseExitsWithCodeOne")
	cmd.Env = append(os.Environ(), "WR_PANIC_FUSE_CHILD=1", "WR_LOG_FILE="+logPath)
	out, err := cmd.CombinedOutput()

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("子进程应异常退出（熔断），实际 err=%v output=%s", err, out)
	}
	if got := exitErr.ExitCode(); got != logx.PanicExitCode {
		t.Fatalf("熔断退出码应为 %d，得到 %d\n%s", logx.PanicExitCode, got, out)
	}
	data, rerr := os.ReadFile(logPath)
	if rerr != nil {
		t.Fatalf("读日志失败：%v", rerr)
	}
	logText := string(data)
	if !strings.Contains(logText, "后台 goroutine") {
		t.Errorf("日志里没有 panic 现场：\n%s", logText)
	}
	if !strings.Contains(logText, "判定为不可恢复") {
		t.Errorf("日志里没有熔断说明：\n%s", logText)
	}
	// 栈必须是真的（打印的是 debug.Stack，不是占位文本）
	if !strings.Contains(logText, "recoverguard.go") {
		t.Errorf("日志里的栈不像真栈：\n%s", logText)
	}
}
