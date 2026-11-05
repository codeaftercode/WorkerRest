// 单条日志闸门：把媒体内容、二进制、超长行挡在落盘之前。
//
// 为什么放在最外层：日志是「正式包没有控制台时唯一能看到现场」的通道，而任何一处
// 误把立绘 DataURL、音频数据、一整段 base64 或超大字符串打进来，都会一笔写满
// 2 MiB 的滚动文件 —— 真正的错误反而被冲掉，格式化与写盘的开销也全花在没人会看
// 的内容上。这一层对**所有**来源生效：本项目的标准 log、Wails 运行时的 slog、
// 以及 WR_LOG_FILE 诊断覆盖。
//
// 正常短日志只做一次便宜的检查就透传，不分配、不拷贝 —— 日志是每条 tick 都可能
// 走到的路径，闸门本身不能变成新的性能负担。
package logx

import (
	"bytes"
	"io"
	"strconv"
	"unicode/utf8"
)

// logMaxLineBytes 单条日志上限。正常一行几十到几百字节；8 KiB 足够放下完整栈帧或
// 一条带长路径的错误，又远小于单份文件的 2 MiB —— 一条异常日志撑不爆滚动窗口。
const logMaxLineBytes = 8 << 10

// isMediaContentMinBytes 只有这么长以上的行才判「媒体内容」。
// 短行里出现 "data:image/" 多半只是在描述一个错误，不是真把图片塞了进来。
const isMediaContentMinBytes = 512

// logMediaDetectMinBytes 裸 base64 判据的长度门槛，同时也是快速路径的上限：
// 再长就逐行细判，免得不含特征串的大块媒体内容绕过去。
const logMediaDetectMinBytes = 1024

// 媒体 / DataURL 的特征串。刻意用带斜杠的前缀而不是裸 "base64"，
// 免得把「报表数据 base64 解码失败」这种正常错误文案误判成媒体内容。
var logMediaMarkers = [][]byte{
	[]byte("data:image/"),
	[]byte("data:audio/"),
	[]byte("data:video/"),
	[]byte("data:application/octet-stream"),
	[]byte(";base64,"),
}

// guardWriter 是 AppLogWriter 的最外层。无状态，天然并发安全（标准 log 与 slog 可能
// 同时写；下游 rotatingLogWriter 自己带锁）。
type guardWriter struct{ dst io.Writer }

func wrapGuarded(dst io.Writer) io.Writer { return &guardWriter{dst: dst} }

// Write 按行处理：slog / log 都是一次一整行，debug.Stack() 是多行，逐行判定才不会
// 漏掉夹在中间的超长行。
func (g *guardWriter) Write(p []byte) (int, error) {
	// 快速路径：短、无 NUL、无媒体特征 —— 绝大多数日志走这里，零拷贝零分配。
	// 上限取 logMediaDetectMinBytes 而不是 logMaxLineBytes：更长的行一律逐行细判，
	// 否则不含任何特征串的裸 base64 大块会从快速路径溜过去。
	if len(p) < logMediaDetectMinBytes && !logLineNeedsGuard(p) {
		return g.dst.Write(p)
	}
	if _, err := g.dst.Write(guardLogBytes(p)); err != nil {
		return 0, err
	}
	// io.Writer 契约：消费了全部入参就返回 len(p)，与实际写出多少字节无关。
	return len(p), nil
}

// logLineNeedsGuard 快速判据：短行只查这几个便宜的条件。
func logLineNeedsGuard(p []byte) bool {
	if bytes.IndexByte(p, 0) >= 0 { // NUL = 二进制
		return true
	}
	for _, m := range logMediaMarkers {
		if bytes.Contains(p, m) {
			return true
		}
	}
	return false
}

// guardLogBytes 把每一行分别判定后重组（保留原有换行结构）。
func guardLogBytes(p []byte) []byte {
	var out []byte
	start := 0
	for i := 0; i <= len(p); i++ {
		if i < len(p) && p[i] != '\n' {
			continue
		}
		out = append(out, guardLogLine(p[start:i])...)
		if i < len(p) {
			out = append(out, '\n')
		}
		start = i + 1
	}
	return out
}

func guardLogLine(line []byte) []byte {
	if isMediaLogLine(line) {
		return []byte("[logx] 已忽略疑似媒体/二进制内容（原 " + strconv.Itoa(len(line)) + " 字节）")
	}
	if len(line) <= logMaxLineBytes {
		return line
	}
	cut := logMaxLineBytes
	// 别在 UTF-8 字符中间截断，否则日志里会多出乱码。cut < len(line) 恒成立。
	for cut > 0 && !utf8.RuneStart(line[cut]) {
		cut--
	}
	b := make([]byte, 0, cut+64)
	b = append(b, line[:cut]...)
	b = append(b, "…[logx] 本行超过上限，已省略 "...)
	b = append(b, strconv.Itoa(len(line)-cut)...)
	b = append(b, " 字节"...)
	return b
}

// isMediaLogLine 判定一行是不是「不该出现在日志里的媒体 / 二进制内容」。
func isMediaLogLine(line []byte) bool {
	if bytes.IndexByte(line, 0) >= 0 {
		return true // 二进制数据
	}
	if len(line) >= isMediaContentMinBytes {
		for _, m := range logMediaMarkers {
			if bytes.Contains(line, m) {
				return true
			}
		}
	}
	// 裸 base64 大块（没有 data: 前缀）：长、无空格、几乎全是 base64 字母表。
	// 普通长日志（栈、路径列表、JSON）都带空格或标点，不会命中。
	return len(line) >= logMediaDetectMinBytes && bytes.IndexByte(line, ' ') < 0 && base64ishRatio(line) >= 0.95
}

// base64ishRatio 抽样前 512 字节，统计落在 base64 字母表里的比例。
func base64ishRatio(line []byte) float64 {
	n := len(line)
	if n > 512 {
		n = 512
	}
	hit := 0
	for _, c := range line[:n] {
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' ||
			c == '+' || c == '/' || c == '=' {
			hit++
		}
	}
	return float64(hit) / float64(n)
}
