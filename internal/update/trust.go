package update

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"workerrest/internal/urltls"
)

// 更新链的信任边界（纯逻辑，零 App 依赖）。
//
// 自动更新是**远程内容直接落到本地并被执行**的唯一条路：version.json 能改更新源、
// 能指定下载地址，下载下来的 zip 里就是新 exe。只靠 TLS 是不够的 ——
//   · TLS 会被企业/公共 Wi-Fi 的中间人代理替换（用户装了根证书就完全无从察觉）；
//   · 更新源是「可被远程整体覆盖」的，一旦某一跳的 version.json 被改写，
//     程序就会去没人审计过的地址拉一个包，然后双击一样跑起来；
//   · 明文 http 源根本没有完整性可言（审计 BLOCKER-2）。
//
// 这一层把三件事钉死：
//  1. **只认 https**（回环地址上的 http 例外，仅为单元测试用的 httptest 服务保留；
//     回环不存在中间人，且能监听回环的进程本来就有本地代码执行能力）；
//  2. **清单必须给出 rest.zip 的 sha256**，下载完逐字节校验，对不上就不装 ——
//     校验失败**才**允许换下一个镜像重试，因为每个镜像都要过同一道哈希；
//  3. **下载与解压都有上限**：包体积、version.json 体积、解压总量、条目数，
//     防「一个畸形包把内存/磁盘撑爆」。
//
// 远端没给 sha256 时**拒绝更新**（fail closed）。这不是过度保守：一个能下发任意
// exe 的通道，宁可停在旧版本，也不能在「看起来正常」的情况下装入未经校验的内容。

const (
	// MaxVersionJSONBytes 清单体积上限：正常只有几百字节，1 MiB 足够宽松。
	MaxVersionJSONBytes = 1 << 20
	// MaxPackageBytes 更新包（rest.zip）体积上限：实际约 7~15 MiB。
	MaxPackageBytes = 64 << 20
	// MaxExtractTotalBytes 解压后总字节上限（防 zip bomb：压缩比可达 1000:1）。
	MaxExtractTotalBytes = 192 << 20
	// MaxExtractEntries 解压条目数上限（正常包只有几个文件）。
	MaxExtractEntries = 2048
	// maxUpdateRedirects 跟随重定向的上限
	maxUpdateRedirects = 5
	// sha256HexLen sha256 十六进制长度
	sha256HexLen = 64
)

// SanitizeUpdateURLs 过滤出合规地址（保持原顺序）；被丢弃的记一行日志便于排查。
func SanitizeUpdateURLs(in []string) []string {
	out := make([]string, 0, len(in))
	for _, raw := range in {
		s := strings.TrimSpace(raw)
		if s == "" {
			continue
		}
		if err := urltls.CheckUpdateURL(s); err != nil {
			log.Printf("[update] 丢弃不合规地址 %q: %v", s, err)
			continue
		}
		out = append(out, s)
	}
	return out
}

// NewUpdateClient 构造更新专用 HTTP 客户端：带超时，并在**每一次重定向**上重新
// 校验协议 —— 否则一个 https 地址可以 302 到 http，前面所有校验都白做。
func NewUpdateClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxUpdateRedirects {
				return fmt.Errorf("重定向次数超过 %d 次", maxUpdateRedirects)
			}
			return urltls.CheckUpdateURL(req.URL.String())
		},
	}
}

// ParseSHA256 规范化并校验清单里的 sha256（大小写不敏感、允许 "sha256:" 前缀）。
// 格式不对等同于「没有校验值」—— 宁可拒绝，也不能拿一个残值去比。
func ParseSHA256(s string) (string, error) {
	v := strings.TrimSpace(s)
	v = strings.TrimPrefix(strings.ToLower(v), "sha256:")
	v = strings.TrimSpace(v)
	if v == "" {
		return "", errors.New("清单里没有 sha256")
	}
	if len(v) != sha256HexLen {
		return "", fmt.Errorf("sha256 长度应为 %d 位，实得 %d 位", sha256HexLen, len(v))
	}
	if _, err := hex.DecodeString(v); err != nil {
		return "", errors.New("sha256 含非十六进制字符")
	}
	return v, nil
}

// sha256File 算文件的 sha256（十六进制小写）。
func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// VerifySHA256 校验文件哈希与期望值一致（want 应已由 ParseSHA256 规范化）。
func VerifySHA256(path, want string) error {
	got, err := sha256File(path)
	if err != nil {
		return fmt.Errorf("计算校验值失败: %w", err)
	}
	if got != want {
		return fmt.Errorf("sha256 校验失败（期望 %s…，实际 %s…）", Head8(want), Head8(got))
	}
	return nil
}

// Head8 取前 8 位用于提示：完整 64 位太长，日志与界面上没必要刷屏。
func Head8(s string) string {
	if len(s) <= 8 {
		return s
	}
	return s[:8]
}
