package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"
)

// 应用日志的落点与滚动策略。
//
// 为什么必须落盘：GUI 子系统（-H windowsgui）没有控制台，stderr 写进虚空。而
// **Wails 运行时自己的致命错误也只走 Logger**（application.handleError），所以
// 「双击没反应」这类问题在正式包里唯一能看到现场的通道就是这份文件 —— 早先
// appLogger 默认返回 nil（丢弃），等于把最需要的一条日志一起丢了。
//
// 落点：<exe 所在目录>/logs/app.log（便携包的结构就是「exe 旁边放 config/、data/、
// sounds/」，日志跟着同一个目录，用户打包拷走时一起带走；装到只读目录时退到
// %LocalAppData%\WorkerRest\logs）。WR_LOG_FILE 仍然优先，探针靠它把某个被测
// 进程的日志单独捞出来。
//
// 滚动：单份 2 MiB，另留 app.log.1 / app.log.2，总量上限 6 MiB —— 长期挂机的小工具
// 不该让日志无限长。
const (
	logFileName  = "app.log"
	logMaxBytes  = 2 << 20 // 单份 2 MiB
	logKeepFiles = 2       // 额外保留 .1 / .2
)

var (
	appLogOnce sync.Once
	appLogOut  io.Writer
)

// appLogWriter 返回全局日志目标（进程内只算一次，log 与 slog 共用同一份）。
func appLogWriter() io.Writer {
	appLogOnce.Do(func() { appLogOut = newAppLogWriter() })
	return appLogOut
}

// newAppLogWriter 按优先级挑一个能写的目标，全都不行才退回 stderr。
func newAppLogWriter() io.Writer {
	// 诊断用覆盖：设了 WR_LOG_FILE 就**只**写该文件（不滚动 —— 探针自己管理生命周期）。
	// 刻意不再往 stderr 抄一份：用它的探针同时也是 stdout 重定向到同一个文件的，
	// 两边都写会让按行计数、按「第几条」判序的断言（smoke_server 数 [sound] 播放次数）
	// 直接翻倍。
	if p := os.Getenv("WR_LOG_FILE"); p != "" {
		if f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
			return f
		}
	}
	for _, dir := range appLogDirs() {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			continue
		}
		w, err := openRotatingLog(filepath.Join(dir, logFileName), logMaxBytes, logKeepFiles)
		if err == nil {
			// 文件是主目标，stderr 只是「有控制台时顺便看一眼」：
			// 发布版是 -H windowsgui（stderr 是无效句柄，写失败直接忽略），而
			// 去掉该标记的探针产物、以及用户从 cmd 启动时，日志还得能出现在控制台上
			// —— probes 里 smoke_server / probe_sound_scan / probe_stats_file 全都
			// 靠捕获 stderr 做断言。
			return &teeWriter{file: w, console: os.Stderr}
		}
	}
	return os.Stderr
}

// teeWriter 写文件为主、附带抄一份到控制台；控制台写失败（GUI 无 stderr）不影响返回值。
type teeWriter struct {
	file    io.Writer
	console io.Writer
}

func (t *teeWriter) Write(p []byte) (int, error) {
	n, err := t.file.Write(p)
	if t.console != nil {
		_, _ = t.console.Write(p)
	}
	return n, err
}

// appLogDirs 候选日志目录，按优先级排列。
//
// 第一个是 exe 所在目录下的 logs/（即 <exe>/../logs）：便携包所在目录通常可写；
// 落到 Program Files 时 MkdirAll/Open 会失败，交给第二个候选。
func appLogDirs() []string {
	var dirs []string
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Join(filepath.Dir(exe), "logs"))
	}
	if cache, err := os.UserCacheDir(); err == nil {
		dirs = append(dirs, filepath.Join(cache, "WorkerRest", "logs"))
	}
	return dirs
}

// appLogger 给 Wails 运行时的日志器。
//
// 默认写到 appLogWriter()（以前默认返回 nil = 丢弃，见文件头注释）。
// WR_LOG_LEVEL=debug 时放开 Debug 级 —— 那是唯一能直接看到 Wails 真实注册了哪些
// 全限定名（FQN）的地方，排查「前端按名字调绑定失败」时用：
//
//	WR_LOG_LEVEL=debug ./chibingren_server.exe    # 日志里会打出 fqn=main.App.Xxx
//
// 本项目就是靠它确认了 FQN 前缀是 "main.App."（而不是模块名 workerrest）。
func appLogger() *slog.Logger {
	level := slog.LevelInfo
	if os.Getenv("WR_LOG_LEVEL") == "debug" {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(appLogWriter(), &slog.HandlerOptions{Level: level}))
}

// rotatingLogWriter 按大小滚动的文件写入器（并发安全：log 与 slog 可能同时写）。
type rotatingLogWriter struct {
	mu   sync.Mutex
	path string
	max  int64
	keep int
	file *os.File
	size int64
}

func openRotatingLog(path string, max int64, keep int) (*rotatingLogWriter, error) {
	w := &rotatingLogWriter{path: path, max: max, keep: keep}
	if err := w.reopen(); err != nil {
		return nil, err
	}
	return w, nil
}

// reopen 以追加方式打开当前份，并把已有大小记下来 —— 进程重启后仍能接着算总量。
func (w *rotatingLogWriter) reopen() error {
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	w.file, w.size = f, st.Size()
	return nil
}

func (w *rotatingLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		if err := w.reopen(); err != nil {
			return 0, err
		}
	}
	// 单条日志本身就超过上限时不再滚动（否则永远滚不完），直接写下去。
	if w.size > 0 && w.size+int64(len(p)) > w.max {
		w.rotate()
	}
	n, err := w.file.Write(p)
	w.size += int64(n)
	return n, err
}

// rotate 把 app.log(N) → app.log(N+1)，最老的一份删掉，然后续写新的 app.log。
//
// 任何一步失败都只当「这次没滚成功」：日志组件不该把主程序拖死。
func (w *rotatingLogWriter) rotate() {
	_ = w.file.Close()
	for i := w.keep; i >= 1; i-- {
		dst := w.path + "." + strconv.Itoa(i)
		if i == w.keep {
			_ = os.Remove(dst) // 最老的一份直接丢
			continue
		}
		_ = os.Rename(dst, w.path+"."+strconv.Itoa(i+1))
	}
	_ = os.Rename(w.path, w.path+".1")
	w.file, w.size = nil, 0
	if err := w.reopen(); err != nil {
		// 重开失败：把错误往上抛给 log 包（它会写 stderr），下次 Write 还会再试。
		w.file = nil
	}
}

// Close 关掉当前份（测试与退出时用；正常退出不调用也不会丢数据 —— 每次 Write 都直接落盘）。
func (w *rotatingLogWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

var _ io.WriteCloser = (*rotatingLogWriter)(nil)
