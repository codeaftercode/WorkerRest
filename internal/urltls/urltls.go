// Package urltls 是更新链路的 URL 安全校验：只放行 https（回环地址除外），重定向的
// 每一跳都重新校验。
//
// 单独成包是因为它被两处共用：internal/update 校验清单地址，internal/settings 校验
// 内置的更新站点配置。口径只写一遍，避免两处各判一套。
package urltls

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// 更新源地址的信任校验：只允许 https（http 仅限回环，为 httptest 保留）。
// 从 update 域抽出共享给 settings 域（Settings.UpdateSites / SetSites 用它过滤），
// 避免 settings → update 的跨包依赖。

// IsLoopbackHost 判断主机名是否是本机回环（localhost / 127.0.0.0/8 / ::1）。
func IsLoopbackHost(host string) bool {
	h := strings.TrimSpace(host)
	if h == "" {
		return false
	}
	if strings.EqualFold(h, "localhost") {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// CheckUpdateURL 校验一个更新相关地址是否可用：只允许 https；http 仅限回环地址。
// 返回的 error 直接展示给用户（文字要能说清“为什么不给装”）。
func CheckUpdateURL(raw string) error {
	s := strings.TrimSpace(raw)
	if s == "" {
		return errors.New("地址为空")
	}
	u, err := url.Parse(s)
	if err != nil {
		return errors.New("地址无法解析")
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		if u.Hostname() == "" {
			return errors.New("地址缺少主机名")
		}
		return nil
	case "http":
		if IsLoopbackHost(u.Hostname()) {
			return nil
		}
		return errors.New("http 明文源已禁用（更新只走 https）")
	default:
		return fmt.Errorf("不支持的协议 %q（更新只走 https）", u.Scheme)
	}
}
