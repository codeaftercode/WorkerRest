package main

import (
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// 设置页（settings.html）用到的绑定：读配置 / 存配置 / 督休专员目录增删改 / 关于页与更新 /
// 窗口控制。对应 历史源码/preload.ts 里除休息窗口与外卖窗口之外的那部分 window.api。

// ---------- 配置读写 ----------

// GetSettings 设置页首帧拉取的整份配置。
//
// 必须整份完整：设置页没有自己的默认值，拿到什么就用来填什么（S = await api.getSettings()），
// 少一个字段 → fillForm() 抛错 → 整页停摆。所以这里下发的是
// deepMerge(出厂默认, 磁盘内容) 的结果，而不是「Go 用得到的那几个字段」。
func (a *App) GetSettings() map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg.Raw()
}

// SaveSettings 保存设置（设置页的所有 input/select 都绑在 autoSave 上，改一下就调一次）。
//
// 流程与 上一版 settings:save 对齐：深合并 → 归一化（含旧键迁移与越界夹紧）→ 落盘
// → 同步窗口标题 / 自启动广播 / 状态推送 / 调度器重算。
// 返回值：落盘成功为 true。
func (a *App) SaveSettings(next map[string]any) bool {
	a.mu.Lock()
	beforeAutostart := a.cfg.Autostart()
	merged, _ := deepMerge(a.cfg.Raw(), next).(map[string]any)
	if merged == nil {
		a.mu.Unlock()
		log.Printf("[settings] 保存被忽略：入参不是对象")
		return false
	}
	s := &Settings{raw: merged, path: a.cfg.Path()}
	normalizeForSave(merged)
	if err := s.Save(); err != nil {
		a.mu.Unlock()
		log.Printf("[settings] 保存失败: %v", err)
		return false
	}
	a.cfg = s
	title := a.cfg.AppName()
	autostart := a.cfg.Autostart()
	a.mu.Unlock()

	if w := a.settingsWindow(); w != nil && title != "" {
		w.SetTitle(title)
	}
	// 开机自启动只在用户真的改了它时才广播（与 上一版「用户没碰就别动系统」同口径），
	// 同理也只有这一刻才去动注册表 —— 启动路径上完全不碰它。
	if autostart != beforeAutostart {
		a.app.Event.Emit("autostart", autostart)
		a.ApplyAutostart()
	}
	a.pushState()
	if a.sched != nil {
		a.sched.refreshPlan()
	}
	// 快捷键可能被改：线程内注销旧键 + 按新配置重注册
	a.reloadHotkeys()
	return true
}

// ResetSettings 恢复默认设置：先把当前配置备份成 settings.backup.json（每次覆盖），
// 再整体重置为出厂默认。installId 保留（永久标识）；督休专员目录与已导入的音乐不受影响。
func (a *App) ResetSettings() map[string]any {
	a.mu.Lock()
	cur := a.cfg.Path()
	installID := a.cfg.Str("", "installId")
	beforeAutostart := a.cfg.Autostart()
	if cur == "" {
		// 还没有配置文件：直接按便携/安装形态推导一个落点
		cur = filepath.Join(a.resourcesRoot(), "config", "settings.json")
	}
	backupPath := filepath.Join(filepath.Dir(cur), "settings.backup.json")
	if _, err := os.Stat(cur); err == nil {
		if err := copyFile(cur, backupPath); err != nil {
			a.mu.Unlock()
			return map[string]any{"ok": false, "message": "备份失败：" + err.Error()}
		}
	}
	// 与 上一版的 settings:reset 一致：**只**回到出厂默认 + 保留 installId，
	// 不再跑一遍归一化 —— 预置条目 / 内置音乐要等下次启动的加载路径才种入。
	fresh := defaultSettingsMap()
	ensureInstallID(fresh)
	if installID != "" {
		fresh["installId"] = installID
	}
	s := &Settings{raw: fresh, path: cur}
	if err := s.Save(); err != nil {
		a.mu.Unlock()
		return map[string]any{"ok": false, "message": "写入失败：" + err.Error()}
	}
	a.cfg = s
	title := a.cfg.AppName()
	autostart := a.cfg.Autostart()
	a.mu.Unlock()

	if w := a.settingsWindow(); w != nil && title != "" {
		w.SetTitle(title)
	}
	a.app.Event.Emit("autostart", autostart)
	// 自启动当前是「开」而默认是「关」：等同用户改了它，注册表那条要清掉
	if autostart != beforeAutostart {
		a.ApplyAutostart()
	}
	a.pushState()
	if a.sched != nil {
		a.sched.refreshPlan()
	}
	a.reloadHotkeys()
	log.Printf("[settings] 已恢复默认设置，原配置备份至 %s", backupPath)
	return map[string]any{"ok": true, "backupPath": backupPath}
}

// OpenManual 打开随包操作手册（自包含 HTML）。打包版从 exe 旁的 manual/ 取，
// 开发态开模块根下的 manual/；缺失返回 false 由页面提示。
func (a *App) OpenManual() bool {
	exe, _ := os.Executable()
	cands := []string{filepath.Join(filepath.Dir(exe), "manual", "操作手册.html")}
	// 开发态：模块根下的 manual/（roles/ 同款回退）
	if root := nearestModuleRoot(filepath.Dir(exe)); root != "" {
		cands = append(cands, filepath.Join(root, "manual", "操作手册.html"))
	}
	for _, c := range cands {
		if _, err := os.Stat(c); err == nil {
			return revealPath(c, "")
		}
	}
	log.Printf("[manual] 操作手册缺失，已查找：%v", cands)
	return false
}

// OpenExternal 外链（开源许可 / 发布页）：只放行 https，其余 scheme 一律拒绝——
// 渲染层任何一处外链被注入都不能变成任意协议唤起。
func (a *App) OpenExternal(url string) bool {
	if len(url) < 8 || url[:8] != "https://" {
		log.Printf("[external] 拒绝非 https 链接：%q", url)
		return false
	}
	if err := a.app.Browser.OpenURL(url); err != nil {
		log.Printf("[external] 打开失败 %s: %v", url, err)
		return false
	}
	return true
}

// ---------- 预置提醒内容 ----------

// BuiltinMsgDefaults 三类提醒的预置提醒内容：设置页提醒内容面板的**种子**数据。
// 种入之后就是普通条目（可改可禁可删），删空不再回补。
func (a *App) BuiltinMsgDefaults() map[string]any {
	return map[string]any{
		"kinds": map[string]any{
			"short":   builtinKindItems("short"),
			"long":    builtinKindItems("long"),
			"takeout": builtinKindItems("takeout"),
		},
	}
}

// ---------- 督休专员目录（增删改） ----------

// SaveRole 保存某个督休专员的 role.json（改名 / 启停 / 尺寸位置 / 台词编辑）。
// 只覆盖 patch 里带到的字段，立绘文件名等读取侧字段保持不动。
func (a *App) SaveRole(key string, patch map[string]any) bool {
	if !validRoleKey(key) {
		log.Printf("[role] 非法督休专员 key：%q", key)
		return false
	}
	base, ok := readRoleFile(a.rolesRoot, key)
	if !ok {
		log.Printf("[role] 督休专员不存在或 role.json 读不出来：%s", key)
		return false
	}
	next := applyRolePatch(base, patch)
	if next.Name == "" {
		next.Name = key
	}
	if err := writeRoleFile(a.rolesRoot, key, next); err != nil {
		log.Printf("[role] 写入失败 %s: %v", key, err)
		return false
	}
	return true
}

// RemoveRole 删除督休专员 = 整个督休专员子目录一并删除
func (a *App) RemoveRole(key string) bool {
	if !removeRoleDir(a.rolesRoot, key) {
		log.Printf("[role] 删除失败：%s", key)
		return false
	}
	return true
}

// PickSupervisorImage 新建自定义督休专员（选图 → 建督休专员目录）。
//
// 返回 **null**（不是 {canceled:true}）：设置页的写法是 `const picked = await …;
// if (!picked) return;` 紧接着取 `picked.name.trim()` —— 返回一个带 canceled 的**对象**
// 是真值，会一路走到 `undefined.trim()` 直接抛错。
//
// 预览图：上一版用 nativeImage 把立绘缩到 192 宽写 preview.png；Go 侧不引图像库
// （保持依赖简洁），preview 直接复制原图（createRoleDir 已按此实现）——磁贴只是显示用，
// 原图 data URL 完全够用，唯一代价是磁贴加载略重一点。
func (a *App) PickSupervisorImage() map[string]any {
	if !a.dialogAvailable() {
		log.Printf("[role] 无头 / server 模式无法弹选图对话框，返回取消")
		return nil
	}
	r := showOpenDialog(a.app.Dialog, "选择督休专员立绘", []application.FileFilter{
		{DisplayName: "图片", Pattern: "*.png;*.jpg;*.jpeg;*.webp;*.gif"},
	})
	if r.canceled {
		return nil
	}
	if r.err != nil {
		logDialogErr("选择立绘", r.err)
		return nil
	}
	src := r.path
	name := strings.TrimSuffix(filepath.Base(src), filepath.Ext(src))
	key := createRoleDir(a.rolesRoot, name, src)
	if key == "" {
		log.Printf("[role] 创建督休专员目录失败：%s", src)
		return nil
	}
	return map[string]any{"key": key, "name": name}
}

// ReplaceRoleImage 修改某个已有督休专员的立绘图片（选图 → 覆盖 figure / preview → 回写 role.json）。
//
// 与 PickSupervisorImage（新建督休专员）不同：这里督休专员 key 保持不变、只换图，名字 / 启停 /
// 几何 / 台词都保留。返回是否成功（取消 / 无头环境 / 失败都返回 false，设置页据此静默）。
func (a *App) ReplaceRoleImage(key string) bool {
	if !validRoleKey(key) {
		log.Printf("[role] 非法督休专员 key：%q", key)
		return false
	}
	if !a.dialogAvailable() {
		log.Printf("[role] 无头 / server 模式无法弹选图对话框，返回失败")
		return false
	}
	r := showOpenDialog(a.app.Dialog, "选择新的立绘图片", []application.FileFilter{
		{DisplayName: "图片", Pattern: "*.png;*.jpg;*.jpeg;*.webp;*.gif"},
	})
	if r.canceled {
		return false
	}
	if r.err != nil {
		logDialogErr("选择立绘", r.err)
		return false
	}
	return replaceRoleImage(a.rolesRoot, key, r.path)
}

// ---------- 关于页 / 更新 / 窗口 ----------

// UpdateStatus 最近一次检查更新的结果（没查过 = idle）。
// 形状必须与渲染层一致：它读的是 st.status（不是 st.state）。
func (a *App) UpdateStatus() map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.updateStatus != nil {
		return a.updateStatus
	}
	return map[string]any{"status": "idle", "current": appVersion}
}

// ExportReport 导出报表文件（CSV / PNG）：另存为对话框 → base64 解码写盘。
//
// 渲染层把图表内容（CSV 已含 BOM、PNG 已含底色）转成 base64 传来，存储路径完全由
// 用户在对话框里选（取代 上一版时代 a.download 落点不可控的下载行为）。
// 返回 {canceled, error?, path?}：取消 → {canceled:true}（渲染层静默）；
// 失败 → {canceled:false, error:"..."}；成功 → {canceled:false, path:"..."}。
func (a *App) ExportReport(defaultName, b64 string) map[string]any {
	if !a.dialogAvailable() {
		log.Printf("[settings] 无头 / server 模式无法弹另存为对话框，返回失败")
		return map[string]any{"canceled": false, "error": "当前环境无法弹出保存对话框"}
	}
	var filters []application.FileFilter
	switch strings.ToLower(filepath.Ext(defaultName)) {
	case ".csv":
		filters = []application.FileFilter{{DisplayName: "CSV", Pattern: "*.csv"}}
	case ".png":
		filters = []application.FileFilter{{DisplayName: "PNG 图片", Pattern: "*.png"}}
	default:
		filters = []application.FileFilter{{DisplayName: "所有文件", Pattern: "*.*"}}
	}
	r := showSaveDialog(a.app.Dialog, "导出报表", defaultName, filters)
	if r.canceled {
		return map[string]any{"canceled": true}
	}
	if r.err != nil {
		logDialogErr("导出报表", r.err)
		return map[string]any{"canceled": false, "error": "打开保存对话框失败"}
	}
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		log.Printf("[settings] 报表数据 base64 解码失败: %v", err)
		return map[string]any{"canceled": false, "error": "导出数据无效"}
	}
	if err := os.WriteFile(r.path, data, 0o644); err != nil {
		log.Printf("[settings] 写入导出文件失败 %s: %v", r.path, err)
		return map[string]any{"canceled": false, "error": "写入文件失败"}
	}
	log.Printf("[settings] 报表已导出到 %s", r.path)
	return map[string]any{"canceled": false, "path": r.path}
}

// ---------- 声音 / 指定应用暂停（文件选择类） ----------

// GetStats / GetHistory / ClearStats 迁进 stats.go（天记录 + 周期聚合 + 每秒入账 +
// 防抖落盘 + 清空）；SoundFileURL / DeleteMusicSound 迁进 sound.go（内置音走资源服务、
// 用户音乐走 /__sound/ 只读路由）。下面是留在本文件的文件选择类绑定。

// AddMusicSound 新增音乐（选文件并导入副本）。导入本身（格式白名单 / 20 MB 上限 /
// 复制成独立副本）在 sound.go 的 importMusicSound，这里补「选文件」这一步。
// 返回形状与 上一版的 sound:addMusic 一致：取消 → {canceled:true}；成功 →
// {canceled:false, items:[{file,name}], skipped}；全部失败 → {canceled:false, error}。
func (a *App) AddMusicSound() map[string]any {
	if !a.dialogAvailable() {
		log.Printf("[sound] 无头 / server 模式无法弹选文件对话框，返回取消")
		return map[string]any{"canceled": true}
	}
	r := showOpenMultiDialog(a.app.Dialog, "添加音乐", []application.FileFilter{
		{DisplayName: "音频", Pattern: audioFilterPattern()},
	})
	if r.canceled {
		return map[string]any{"canceled": true}
	}
	if r.err != nil {
		logDialogErr("选择音乐", r.err)
		return map[string]any{"canceled": false, "error": "打开文件对话框失败"}
	}
	items := []map[string]any{}
	skipped := 0
	for _, f := range r.paths {
		one, _ := a.importMusicSound(f)
		if one != nil {
			items = append(items, one)
		} else {
			skipped++
		}
	}
	if len(items) == 0 {
		return map[string]any{
			"canceled": false,
			"error":    fmt.Sprintf("不支持的音频格式，或文件超过 20 MB（支持 %s）", strings.Join(customSoundExts, " / ")),
		}
	}
	return map[string]any{"canceled": false, "items": items, "skipped": skipped}
}

// audioFilterPattern 音乐对话框的文件过滤器（把扩展名白名单拼成 *.ext;*.ext 形式）
func audioFilterPattern() string {
	exts := make([]string, 0, len(customSoundExts))
	for _, e := range customSoundExts {
		exts = append(exts, "*."+e)
	}
	return strings.Join(exts, ";")
}

// ListRunningApps 运行中的程序列表（「指定应用运行时暂停」卡片的第三个添加入口）。
//
// 返回的是 **`{supported, names, detail}` 对象**而不是字符串数组 —— 设置页要按
// `r.supported` 分流（不支持时把 detail 当说明文字显示），旧占位返回 `[]string{}`
// 会让 `.supported` 恒为 undefined，页面永远停在「当前平台不支持读取程序列表」。
func (a *App) ListRunningApps() AppProcessList { return ListProcessNames() }

// PickAppExe 选择本地可执行文件。取消返回 **null**（设置页用 `if (!r) return` 判空）。
// 成功返回 {path, exe}（exe 是去掉路径的文件名，设置页只按文件名匹配，见 上一版的
// apps:pickExe）。
func (a *App) PickAppExe() map[string]any {
	if !a.dialogAvailable() {
		log.Printf("[appPause] 无头 / server 模式无法弹选文件对话框，返回取消")
		return nil
	}
	r := showOpenDialog(a.app.Dialog, "选择应用程序", []application.FileFilter{
		{DisplayName: "程序", Pattern: "*.exe"},
	})
	if r.canceled {
		return nil
	}
	if r.err != nil {
		logDialogErr("选择应用程序", r.err)
		return nil
	}
	return map[string]any{"path": r.path, "exe": filepath.Base(r.path)}
}

// ---------- 内部工具 ----------

// pushState 把状态快照推给设置页（与 上一版的 pushStateToSettings 同义）
func (a *App) pushState() {
	a.app.Event.Emit("state", a.GetState())
}

// wireSettingsWindow 给设置窗口接上「最大化状态变化 → 推给页面」。
//
// 为什么必须由主进程推：无边框窗口的自绘标题栏要自己画「最大化 ⇄ 还原」图标，
// 而窗口状态不只由点那颗按钮决定（双击标题栏、拖到屏幕上边缘、Win+↑ 都会改），
// 渲染层自己数次数必然对不上（见 settingsWindow.ts 的同款注释）。
func (a *App) wireSettingsWindow(w application.Window) {
	push := func(max bool) {
		a.app.Event.Emit("window:maximized-changed", max)
	}
	w.OnWindowEvent(events.Common.WindowMaximise, func(*application.WindowEvent) { push(true) })
	w.OnWindowEvent(events.Common.WindowUnMaximise, func(*application.WindowEvent) { push(false) })
	w.OnWindowEvent(events.Common.WindowRestore, func(*application.WindowEvent) { push(false) })
}

// copyFile 复制文件（用于恢复默认前的备份）
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// revealPath 用系统文件管理器打开 / 定位一个路径（explorer.exe）
func revealPath(path, arg string) bool {
	args := []string{}
	if arg != "" {
		args = append(args, arg)
	}
	args = append(args, path)
	cmd := exec.Command("explorer", args...)
	if err := cmd.Start(); err != nil {
		log.Printf("[shell] 打开失败 %s: %v", path, err)
		return false
	}
	go func() { _ = cmd.Wait() }()
	return true
}
