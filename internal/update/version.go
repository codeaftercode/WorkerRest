// Package update 是“检查更新”的纯逻辑层：版本号比较、更新清单的信任校验
// （只认 https、必带 sha256、超限拒绝）、以及自动检查的退避节奏。
//
// 不含下载与解包 —— 那是 cmd 层的 update_download.go（要写文件、要换 exe）。
// 本包的所有判断都遵守 fail closed：清单缺字段、格式不对、来源不是 https，一律当作
// “没有可用更新”，绝不“先装上再说”。
package update

import (
	"strconv"
	"strings"
	"time"
)

// 版本号语义：三段式比较，v 前缀 / 缺段都容忍，逐字对应 main.ts 的 isNewerVersion。

// IsNewerVersion 简单三段版本号比较：latest 严格大于 current 才算新版
// （v 前缀 / 缺段都容忍）。
func IsNewerVersion(current, latest string) bool {
	a := parseVersion(current)
	b := parseVersion(latest)
	for i := 0; i < 3; i++ {
		ai, bi := 0, 0
		if i < len(a) {
			ai = a[i]
		}
		if i < len(b) {
			bi = b[i]
		}
		if bi != ai {
			return bi > ai
		}
	}
	return false
}

// ParseVersion 把版本号拆成整数段（v 前缀剥掉，非数字段按 0 处理）
func ParseVersion(v string) []int {
	return parseVersion(v)
}

// parseVersion 内部实现（IsNewerVersion / SameVersion 共用）。
func parseVersion(v string) []int {
	s := strings.TrimPrefix(strings.TrimSpace(v), "v")
	parts := strings.Split(s, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n := strconvAtoiSafe(p)
		out = append(out, n)
	}
	return out
}

// strconvAtoiSafe 容忍空/非法段（对应 JS 的 parseInt 容错）
func strconvAtoiSafe(p string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(p))
	return n
}

// SameVersion 两段版本号（按前三段）是否完全相同（v 前缀 / 缺段都容忍）。
// 用于判断“本次远程版本是否就是被忽略的那个版本”。
func SameVersion(a, b string) bool {
	av := parseVersion(a)
	bv := parseVersion(b)
	for i := 0; i < 3; i++ {
		x, y := 0, 0
		if i < len(av) {
			x = av[i]
		}
		if i < len(bv) {
			y = bv[i]
		}
		if x != y {
			return false
		}
	}
	return true
}

// TodayStr 本地日期（YYYY-MM-DD），用作“当天是否已检查更新”的去重键。
func TodayStr() string {
	return time.Now().Format("2006-01-02")
}
