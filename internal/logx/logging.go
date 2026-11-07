// Package logx 是日志落点 / 滚动策略与后台 goroutine panic 兜底的基础设施。
// 两者都零 App 依赖、零类型耦合，一起下沉到本包（recoverguard 的 logPathForUserMessage
// 复用本包的 appLogDirs / logFileName，放同一包避免跨包导出）。
package logx

import (
	"io"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// 应用日志的落点与滚动策略。
//
// 为什么必须落盘：GUI 子系统（-H windowsgui）没有控制台，stderr 写进虚空。而
// **Wails 运行时自己的致命错误也只走 Logger**（application.handleError），所以
// “双击没反应”这类问题在正式包里唯一能看到现场的通道就是这份文件 —— 早先
// appLogger 默认返回 nil（丢弃），等于把最需要的一条日志一起丢了。
//
// 落点：<exe 所在目录>/logs/app.log（便携包的结构就是“exe 旁边放 config/、data/、
// sounds/”，日志跟着同一个目录，用户打包拷走时一起带走；装到只读目录时退到
// %LocalAppData%\WorkerRest\logs）。WR_LOG_FILE 仍然优先，探针靠它把某个被测
// 进程的日志单独捞出来。
//
// 三道体积 / 数量闸门，长期挂机的小工具不该让日志无限长：
//  1. 单条闸门（guard.go）：媒体 / 二进制内容整行拒收，超长行截到 8 KiB；
//  2. 按大小分包：单份 2 MiB，另留 app.log.1 / app.log.2，目录里最多 3 份、6 MiB；
//  3. 按时间分包与清理：单份最多跨 1 天，启动时删掉 7 天前的分包 —— 只留近期日志。
//
// 第 3 条是必要的：日志产生得很慢时，一个 app.log 能横跨好几个月，光靠“按大小”
// 永远轮不到滚动，旧内容就一直赖在盘上。
const (
	logFileName   = "app.log"
	logMaxBytes   = 2 << 20 // 单份 2 MiB
	logKeepFiles  = 2       // 额外保留 .1 / .2
	logMaxFileAge = 24 * time.Hour
	logRetainDays = 7
)

var (
	appLogOnce sync.Once
	appLogOut  io.Writer
)

// AppLogWriter 返回全局日志目标（进程内只算一次，log 与 slog 共用同一份）。
// 最外层套 guardWriter：所有来源都过“媒体拒收 + 单条截断”这道闸，包括 WR_LOG_FILE。
func AppLogWriter() io.Writer {
	appLogOnce.Do(func() { appLogOut = wrapGuarded(newAppLogWriter()) })
	return appLogOut
}

// newAppLogWriter 按优先级挑一个能写的目标，全都不行才退回 stderr。
func newAppLogWriter() io.Writer {
	// 诊断用覆盖：设了 WR_LOG_FILE 就**只**写该文件（不滚动 —— 探针自己管理生命周期，
	// 每次运行前清空该文件）。刻意不再往 stderr 抄一份：用它的探针同时也是 stdout
	// 重定向到同一个文件的，两边都写会让按行计数、按“第几条”判序的断言
	//（smoke_server 数 [sound] 播放次数）直接翻倍。
	//
	// 这条通道同样过 AppLogWriter 最外层的 guardWriter，所以媒体/超长内容依然落不了盘；
	// 只是不参与下面那套按大小/按时间的滚动（诊断通道要的是“一份完整的本次现场”）。
	if p := os.Getenv("WR_LOG_FILE"); p != "" {
		if f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
			return f
		}
	}
	now := time.Now()
	for _, dir := range appLogDirs() {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			continue
		}
		// 先清陈年分包再开当前份：软件几个月没启动过，也不该把上季度的日志带回来。
		pruneOldLogs(dir, now, time.Duration(logRetainDays)*24*time.Hour)
		w, err := openRotatingLog(filepath.Join(dir, logFileName), logMaxBytes, logKeepFiles)
		if err == nil {
			// 文件是主目标，stderr 只是“有控制台时顺便看一眼”：
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
	// GUI 子系统下 stderr 是无效句柄，每条日志都试一次只是白耗一次系统调用。
	// 失败一次就永久停掉抄写；有真实控制台时不会触发。
	consoleDead atomic.Bool
}

func (t *teeWriter) Write(p []byte) (int, error) {
	n, err := t.file.Write(p)
	if t.console != nil && !t.consoleDead.Load() {
		if _, cerr := t.console.Write(p); cerr != nil {
			t.consoleDead.Store(true)
		}
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

// AppLogger 给 Wails 运行时的日志器。
//
// 默认写到 AppLogWriter()（以前默认返回 nil = 丢弃，见文件头注释）。
//
// **默认级别是 Warn**：Wails 框架内部用 slog.Info 打一堆运行时噪声 —— 每交付一个静态
// 资源（`[AssetFileServerFS] Handling request`）、启动摘要（`Build Info:` / `Platform Info:` /
// `Server mode starting`）都要记一行，休息窗口每次加载就会成批刷。这些对本项目的
// 日常排查没有价值，只由本包的标准 log 打 `[xxx]` 关键日志，所以把框架的 Info 整档
// 静默，只留 Warn / Error。真正要看框架行为时用环境变量把级别顶上去：
//
//	WR_LOG_LEVEL=info  ./rest_server.exe   # 恢复框架 Info（含资源请求日志）
//	WR_LOG_LEVEL=debug ./rest_server.exe   # 连 Debug 一起开，日志里会打出
//	                                              # fqn=main.App.Xxx（唯一能看到 Wails
//	                                              # 真实注册了哪些全限定名的地方）
//
// 本项目就是靠 debug 档确认了 FQN 前缀是 "main.App."（而不是模块名 workerrest）。
// 注意：这里只影响 Wails 框架（走 slog）；本包自己的 log.Printf 走标准库 log 直接写
// AppLogWriter，不受此级别控制，永远照常输出。
func AppLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(AppLogWriter(), &slog.HandlerOptions{
		Level:       appLoggerLevel(),
		ReplaceAttr: trimLogAttr,
	}))
}

// logMaxAttrBytes slog 单个字符串属性的上限。
//
// Wails 在 WR_LOG_LEVEL=debug 下会把绑定调用的 args / result 原样打出来，而
// ListRoles 的 result 里带着 17 个督休专员立绘的 data:image/webp;base64,… ——
// 一次调用就是几 MB，实测一份 11.2 MB 的日志里有 6.78 MB 是这类内容。
// 这里从源头把超长属性换成摘要（保留 method= 等真正有用的排查信息），
// guardWriter 再兜一层“万一别的通道漏过来”。
const logMaxAttrBytes = 1024

var logAttrMediaMarkers = []string{
	"data:image/", "data:audio/", "data:video/", "data:application/octet-stream", ";base64,",
}

// trimLogAttr 脱敏 slog 的字符串属性：媒体内容整体拒收，其它超长值截断。
func trimLogAttr(_ []string, a slog.Attr) slog.Attr {
	if a.Value.Kind() != slog.KindString {
		return a
	}
	s := a.Value.String()
	if len(s) < isMediaContentMinBytes {
		return a
	}
	for _, m := range logAttrMediaMarkers {
		if strings.Contains(s, m) {
			return slog.String(a.Key, "[已忽略媒体/二进制内容 "+strconv.Itoa(len(s))+" 字节]")
		}
	}
	if len(s) > logMaxAttrBytes {
		cut := logMaxAttrBytes
		for cut > 0 && !utf8.RuneStart(s[cut]) { // 别切在 UTF-8 字符中间
			cut--
		}
		return slog.String(a.Key, s[:cut]+"…[已省略 "+strconv.Itoa(len(s)-cut)+" 字节]")
	}
	return a
}

// Debugf 只在 WR_LOG_LEVEL=debug 时输出。
//
// 用来安放“什么都没发生”类的旁路播报（已是目标状态、跳过、宿主就绪…）：
// 它们既不反映状态变化，也不是警告或错误，平时写进日志只是稀释现场；真要顺着
// 启动流程排查时把级别顶到 debug 即可看见。未开启时提前返回，连格式化都不做。
func Debugf(format string, args ...any) {
	if os.Getenv("WR_LOG_LEVEL") != "debug" {
		return
	}
	log.Printf(format, args...)
}

// appLoggerLevel WR_LOG_LEVEL → slog 级别。默认 Warn；info / debug 各升一档，非法值按默认兜底。
// 抽成纯函数是为了让“级别映射”可测（见 writer_test.go 的 TestAppLoggerLevel）。
func appLoggerLevel() slog.Level {
	switch os.Getenv("WR_LOG_LEVEL") {
	case "info":
		return slog.LevelInfo
	case "debug":
		return slog.LevelDebug
	}
	return slog.LevelWarn
}

// pruneOldLogs 清掉日志目录里的“过期”或“异常超大”分包，只留近期、正常大小的日志。
//
// 只认 app.log 与 app.log.*，不碰 config/、data/ 或用户放的同目录文件；
// 删除失败一律忽略 —— 日志组件不该把主程序拖死。
//
// 两条判据缺一不可：
//   - 过期：软件很久没开时，连当前份 app.log 一起删掉，随后 openRotatingLog 新建空文件；
//   - 超大：正常单份上限 2 MiB，超过 2 倍必然是历史异常（旧版本曾把立绘 base64
//     整块写进日志，实测留下 11.2 MB 的 app.log）。只按时间判据它还是“今天”，
//     会一直赖在盘上。
func pruneOldLogs(dir string, now time.Time, retain time.Duration) {
	if retain <= 0 {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if name != logFileName && !strings.HasPrefix(name, logFileName+".") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if now.Sub(info.ModTime()) <= retain && info.Size() <= 2*logMaxBytes {
			continue
		}
		_ = os.Remove(filepath.Join(dir, name))
	}
}

// rotatingLogWriter 按大小 / 按时间滚动的文件写入器（并发安全：log 与 slog 可能同时写）。
type rotatingLogWriter struct {
	mu     sync.Mutex
	path   string
	max    int64
	keep   int
	maxAge time.Duration
	now    func() time.Time // 注入时钟，便于回归“跨天滚动”
	file   *os.File
	size   int64
	since  time.Time // 当前份的起始时间（进程重启后从 mtime 恢复）
}

// openRotatingLog 生产用构造：按全局策略滚动。
func openRotatingLog(path string, max int64, keep int) (*rotatingLogWriter, error) {
	return newRotatingLog(path, max, keep, logMaxFileAge, time.Now)
}

// newRotatingLog 可注入时钟 / 跨度，测试用；maxAge <= 0 表示只按大小滚动。
func newRotatingLog(path string, max int64, keep int, maxAge time.Duration, now func() time.Time) (*rotatingLogWriter, error) {
	if now == nil {
		now = time.Now
	}
	w := &rotatingLogWriter{path: path, max: max, keep: keep, maxAge: maxAge, now: now}
	if err := w.reopen(); err != nil {
		return nil, err
	}
	return w, nil
}

// reopen 以追加方式打开当前份，并把已有大小与起始时间记下来 —— 进程重启后仍能
// 接着算总量、接着算这份日志跨了多久。
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
	w.since = st.ModTime()
	if st.Size() == 0 || w.since.IsZero() || w.since.After(w.now()) {
		w.since = w.now()
	}
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
	// 按时间滚动：一份日志不该横跨好几天，否则“只保留近期”无从谈起。
	// 先把 since 推到当前，即使这次 rotate 失败也不会每拍重试。
	if w.maxAge > 0 && w.now().Sub(w.since) > w.maxAge {
		w.since = w.now()
		w.rotate()
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
// 任何一步失败都只当“这次没滚成功”：日志组件不该把主程序拖死。
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
	moved := os.Rename(w.path, w.path+".1") == nil
	w.file, w.size = nil, 0
	if err := w.reopen(); err != nil {
		// 重开失败：把错误往上抛给 log 包（它会写 stderr），下次 Write 还会再试。
		w.file = nil
		return
	}
	if moved {
		return
	}
	// Rename 失败（典型：Windows 上这份文件还被别的句柄占着 —— 父进程把 stderr
	// 重定向到同一个路径就会这样）。reopen 又打开了这份已经很大的旧文件，
	// 若就此罢休，w.size 一直 > max，之后**每条**日志都会再试一次 Rename：
	// 既白耗一次系统调用，体积也永远降不下来。退而求其次就地截断，
	// 并把 since 推到当前，避免“按时间滚动”跟着每拍再来一次。
	w.since = w.now()
	if w.size > w.max {
		_ = os.Truncate(w.path, 0)
		w.size = 0
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
