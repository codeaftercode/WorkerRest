package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"workerrest/internal/update"
	"workerrest/internal/urltls"
)

// checkForUpdate 按「配置里的 sites + 出厂兜底源」依次容错拉取 version.json 并比对版本。
// 兜底源永远排在最后，配置被改坏 / 被远程换成别的源也不会失去官方通道。
// latest 严格大于 current 才算有新版。
func (a *App) checkForUpdate() updateCheckResult {
	current := appVersion
	sites := withFallbackSite(a.cfg.UpdateSites())
	if len(sites) == 0 {
		return updateCheckResult{
			Status:  "error",
			Current: current,
			Message: "未配置更新源（请在配置 sites 中填写 version.json 地址）",
		}
	}
	info, err := fetchVersionJSON(sites)
	if err != nil {
		return updateCheckResult{Status: "error", Current: current, Message: err.Error()}
	}
	// 远程 version.json 可下发更新源 sites：非空则覆盖（替换）本地配置里的 sites，
	// 让更新源可由服务端集中管控；空则保留本地原有值（避免误清空唯一更新源）。
	// 下发值同样过 https 白名单 —— 否则「远程改源」就是绕开协议约束的后门。
	if len(info.Sites) > 0 {
		if cleaned := update.SanitizeUpdateURLs(info.Sites); len(cleaned) > 0 {
			a.cfg.SetSites(cleaned)
			if err := a.cfg.Save(); err != nil {
				log.Printf("[update] 写入远程下发的更新源失败: %v", err)
			}
		} else {
			log.Printf("[update] 远程下发的 sites 全不合规，保留本地更新源")
		}
	}
	latest := strings.TrimSpace(info.Version)
	if latest == "" {
		return updateCheckResult{Status: "error", Current: current, Message: "响应里没有版本号"}
	}
	if update.IsNewerVersion(current, latest) {
		// 有新版却拿不到下载地址：直接报错，别等用户点「立即更新」才失败
		if len(info.URL) == 0 {
			return updateCheckResult{Status: "error", Current: current, Message: "响应里没有下载地址"}
		}
		// 地址先过协议白名单：清单里全是 http 源时，不对外宣称「有更新」——
		// 让用户看到「有新版但装不了」再失败，比直接说清原因更糟。
		urls := update.SanitizeUpdateURLs(info.URL)
		if len(urls) == 0 {
			return updateCheckResult{
				Status:  "error",
				Current: current,
				Message: "更新清单里的下载地址均不合法（只允许 https），已拒绝更新",
			}
		}
		// 校验值缺失同样拒绝：没有哈希就只能靠 TLS，那正是本次要堵的口子。
		sum, err := update.ParseSHA256(info.SHA256)
		if err != nil {
			return updateCheckResult{
				Status:  "error",
				Current: current,
				Message: "更新清单缺少有效的 sha256 校验值，已拒绝更新（" + err.Error() + "）",
			}
		}
		// 清单声明的体积超上限时提前报错，别让用户等一个注定被中断的下载。
		if info.Size > update.MaxPackageBytes {
			return updateCheckResult{
				Status:  "error",
				Current: current,
				Message: fmt.Sprintf("更新包声明体积 %d 字节，超过上限 %d，已拒绝更新", info.Size, update.MaxPackageBytes),
			}
		}
		st := updateCheckResult{
			Status:  "update-available",
			Current: current,
			Latest:  latest,
			URLs:    urls,
			SHA256:  sum,
			Date:    info.Date,
			Changes: info.Changes,
		}
		// 忽略版本处理：被忽略的版本只抑制弹窗、不抑制按钮；
		// 但一旦发现「比被忽略版本更大」的版本，自动解除忽略并恢复弹窗。
		if ig := a.cfg.IgnoredVersion(); ig != "" {
			if update.IsNewerVersion(ig, latest) {
				a.cfg.SetIgnoredVersion("")
				if err := a.cfg.Save(); err != nil {
					log.Printf("[update] 解除忽略版本失败: %v", err)
				}
			} else if update.SameVersion(ig, latest) {
				st.Ignored = true
			}
		}
		return st
	}
	return updateCheckResult{Status: "up-to-date", Current: current}
}

// fetchVersionJSON 依次尝试 sites 中的地址，第一项成功即返回；全部失败返回 error。
func fetchVersionJSON(sites []string) (versionInfo, error) {
	client := update.NewUpdateClient(updateTimeout)
	var lastErr error
	for _, site := range sites {
		info, err := fetchOneVersionJSON(client, site)
		if err == nil {
			return info, nil
		}
		log.Printf("[update] 拉取 %s 失败: %v", site, err)
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("所有更新源均不可用")
	}
	return versionInfo{}, lastErr
}

func fetchOneVersionJSON(client *http.Client, url string) (versionInfo, error) {
	// 协议白名单在这一层也走一遍：配置里的 sites 已经过滤过，但 reachable
	// 的第二道（重定向）在 client 里，这里管「原始地址」。
	if err := urltls.CheckUpdateURL(url); err != nil {
		return versionInfo{}, err
	}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return versionInfo{}, err
	}
	req.Header.Set("User-Agent", "rest-update-check")
	res, err := client.Do(req)
	if err != nil {
		return versionInfo{}, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return versionInfo{}, fmt.Errorf("HTTP %d", res.StatusCode)
	}
	// 体积上限：清单正常只有几百字节，读到上限还没结束就说明不对。
	// （json.Decoder 直接读 body 会被一个超大响应拖住，所以先限长再解析。）
	body, err := io.ReadAll(io.LimitReader(res.Body, update.MaxVersionJSONBytes+1))
	if err != nil {
		return versionInfo{}, err
	}
	if len(body) > update.MaxVersionJSONBytes {
		return versionInfo{}, fmt.Errorf("version.json 超过 %d 字节上限", update.MaxVersionJSONBytes)
	}
	var info versionInfo
	if err := json.Unmarshal(body, &info); err != nil {
		return versionInfo{}, fmt.Errorf("响应解析失败")
	}
	return info, nil
}
