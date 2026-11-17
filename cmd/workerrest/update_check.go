package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"workerrest/internal/settings"
	"workerrest/internal/update"
	"workerrest/internal/urltls"
)

// mutateConfigCopy 在一份配置**深拷贝**上执行一次修改，changed 为 true 时落盘并整体替换 a.cfg。
//
// 为什么不能原地改 a.cfg：配置读取处普遍是「CfgSnapshot() 拿指针后不再持锁」
// （app_windows.go / prebreak.go / takeout.go / sys_watch.go … 40+ 处），原地写 raw
// 会与这些读并发，触发 Go 运行时的 "concurrent map read and map write" —— 那是
// fatal error，recover 兜不住，表现为整个进程直接消失。所以走与 SaveSettings 相同的
// 写法：拷一份 → 在新对象上改 → 落盘 → 换指针。旧对象此后只被读，不再被写。
//
// ⚠️ 别为了“少建一个对象”改回 `a.cfg.SetXxx(...)`：只加 a.mu 而不换对象**等于没修**。
// 锁只挡住同样持锁的写端，读端拿的是指针、之后不持锁 —— 写端仍在改那块 map，fatal 照旧。
// 这个函数存在的唯一价值就是「写新对象 + 换指针」，不是「记得加锁」。
//
// ⚠️ 必须是**深拷**（jsonCloneMap），不能只拷顶层：settings.New(raw, …) 存的是引用，
// 顶层 map 换了、raw["supervisor"] 这类嵌套 map 仍是发布对象里那块内存 —— mutator
// 一改嵌套键就照旧写进活对象。改名督休专员（renameRoleInSettings）与
// NormalizeForSave（它本身就在原地写嵌套：settings_norm.go 的 normalizeSound 等）
// 都会碰到嵌套层。模板是 sound_scan.go 的 mutateSoundMusic：全仓第一个做对的地方，
// 连注释都写明了这条。
//
// 锁要盖住「复制 → 改 → 落盘 → 换指针」整段：中途放锁会让并发的 SaveSettings 把
// 用户刚保存的整份配置写成旧值（一次更新源覆盖不该顺带吞掉别人的设置）。
// 这与本项目其它 cfg 写入点（SaveSettings / ResetSettings）同口径。
func (a *App) mutateConfigCopy(mutate func(s *settings.Settings) (changed bool)) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cfg == nil {
		return fmt.Errorf("配置尚未就绪")
	}
	raw := jsonCloneMap(a.cfg.Raw())
	if raw == nil {
		return fmt.Errorf("配置深拷贝失败（jsonCloneMap）")
	}
	s := settings.New(raw, a.cfg.Path())
	if !mutate(s) {
		return nil
	}
	if err := s.Save(); err != nil {
		return err
	}
	a.cfg = s
	return nil
}

// checkForUpdate 按“配置里的 sites + 出厂兜底源”依次容错拉取 version.json 并比对版本。
// 兜底源永远排在最后，配置被改坏 / 被远程换成别的源也不会失去官方通道。
// latest 严格大于 current 才算有新版。
func (a *App) checkForUpdate() updateCheckResult {
	current := appVersion
	// 本函数跑在自动检查的后台 goroutine 里（update_auto.go），必须走快照读配置：
	// 设置页会同时整体替换 a.cfg，直读 a.cfg 就是与那次替换抢同一块内存（-race 可复现）。
	cfg := a.CfgSnapshot()
	sites := withFallbackSite(cfg.UpdateSites())
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
	// 下发值同样过 https 白名单 —— 否则“远程改源”就是绕开协议约束的后门。
	if len(info.Sites) > 0 {
		if cleaned := update.SanitizeUpdateURLs(info.Sites); len(cleaned) > 0 {
			// 写配置必须持 a.mu，且不能原地改 live 对象（见 mutateConfigCopy）。
			if err := a.mutateConfigCopy(func(s *settings.Settings) bool {
				s.SetSites(cleaned)
				return true
			}); err != nil {
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
		// 有新版却拿不到下载地址：直接报错，别等用户点“立即更新”才失败
		if len(info.URL) == 0 {
			return updateCheckResult{Status: "error", Current: current, Message: "响应里没有下载地址"}
		}
		// 地址先过协议白名单：清单里全是 http 源时，不对外宣称“有更新”——
		// 让用户看到“有新版但装不了”再失败，比直接说清原因更糟。
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
		// 但一旦发现“比被忽略版本更大”的版本，自动解除忽略并恢复弹窗。
		a.mu.Lock()
		ignored := a.cfg.IgnoredVersion()
		a.mu.Unlock()
		if ignored != "" {
			if update.IsNewerVersion(ignored, latest) {
				if err := a.mutateConfigCopy(func(s *settings.Settings) bool {
					s.SetIgnoredVersion("")
					return true
				}); err != nil {
					log.Printf("[update] 解除忽略版本失败: %v", err)
				}
			} else if update.SameVersion(ignored, latest) {
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
	// 的第二道（重定向）在 client 里，这里管“原始地址”。
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
