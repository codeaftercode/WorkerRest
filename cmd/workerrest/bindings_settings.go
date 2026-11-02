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
	"workerrest/internal/logx"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	"workerrest/internal/role"
	"workerrest/internal/settings"
	"workerrest/internal/sound"
	"workerrest/internal/winproc"
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
	beforeHideOnShare := a.cfg.AppearanceCfg().HideOnShare
	merged, _ := settings.DeepMerge(a.cfg.Raw(), next).(map[string]any)
	if merged == nil {
		a.mu.Unlock()
		log.Printf("[settings] 保存被忽略：入参不是对象")
		return false
	}
	s := settings.New(merged, a.cfg.Path())
	// window.lastWidth / lastHeight 是 **Go 独占写**的运行时状态（窗口 resize 时记的），
	// 而设置页拿到的整份 S 是打开页面那一刻的快照：用户拖大窗口后再改别的设置，
	// 前端会把旧值原样送回来，深合并后就把刚记下的尺寸顶掉了。所以合并后一律
	// 以当前配置里的值为准 —— 前端只允许改 rememberSize（勾选框），无权写尺寸。
	keepWindowLastSize(a.cfg.Raw(), merged)
	settings.NormalizeForSave(merged)
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
	// 「共享屏幕时隐藏」：窗口按需新建，正常无需事后补；但提醒窗正在显示时用户
	// 改了这个开关，当前那扇窗得跟着变 —— 否则复选框看着勾上了，屏幕上那扇却
	// 还是能被共享出去。只在值真变了时动，避免每次 autoSave 都去碰窗口。
	if a.CfgSnapshot().AppearanceCfg().HideOnShare != beforeHideOnShare {
		a.syncCaptureGuardToLive()
	}
	// 开机自启动只在用户真的改了它时才广播（与 上一版「用户没碰就别动系统」同口径），
	// 同理也只有这一刻才去动注册表 —— 启动路径上完全不碰它。
	if autostart != beforeAutostart {
		a.app.Event.Emit("autostart", autostart)
		a.ApplyAutostart()
	}
	if a.sched != nil {
		a.sched.RefreshPlan()
	}
	// 状态推送放在重算之后：否则前端 onState 收到的是旧调度计划，
	// 会覆盖新增 / 删除提醒时间后 refreshStatusNow 主动拉取的新状态。
	a.pushState()
	// 快捷键可能被改：线程内注销旧键 + 按新配置重注册
	a.reloadHotkeys()
	return true
}

// ResetSettings 恢复默认设置：先把当前配置备份成 settings.backup.json（每次覆盖），
// 再整体重置为出厂默认。督休专员目录与已导入的音乐不受影响。
func (a *App) ResetSettings() map[string]any {
	a.mu.Lock()
	cur := a.cfg.Path()
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
	// 回到出厂默认后**立刻**跑一遍与启动完全相同的归一化管线（normalizeLoad）：种入提醒
	// 内容条目与内置提示音、按预置清单补齐「指定应用」列表。
	//
	// 不跑这一步的话，用户点完「恢复默认」看到的是空的提醒内容面板 / 空的应用列表 / 空的
	// 音乐列表，得重启一次才补齐 —— 那与「恢复默认 = 恢复成出厂那套值」不符
	// （2026-10-29 用户明确要求恢复默认后立刻就是出厂那套值）。
	fresh := settings.DefaultSettingsMap()
	settings.NormalizeLoad(fresh)
	s := settings.New(fresh, cur)
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
	if a.sched != nil {
		a.sched.RefreshPlan()
	}
	// 状态推送放在重算之后：否则前端 onState 收到的是旧调度计划，
	// 会覆盖新增 / 删除提醒时间后 refreshStatusNow 主动拉取的新状态。
	a.pushState()
	a.reloadHotkeys()
	log.Printf("[settings] 已恢复默认设置，原配置备份至 %s", backupPath)
	return map[string]any{"ok": true, "backupPath": backupPath}
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

// SaveRole 保存某个督休专员的 meta.json（改名 / 启停 / 尺寸位置 / 台词编辑）。
// 只覆盖 patch 里带到的字段，立绘文件名等读取侧字段保持不动。
//
// name 是**改名前**的专员名（就是身份）。名称变了要做两件事，顺序不能反：
//   - 先落盘 meta.json（名字是权威，之后按新名反查就找得到这个目录）；
//   - 再让目录名跟上（renameRoleDir）。目录改名可能被句柄占用而失败，那只是
//     「目录名难看」，功能照常 —— 反查一律读 meta.json 的 name，不靠目录名拼路径。
//     也正因为如此，这里**不能**走 writeRoleFile：改名后按新名反查不到旧目录，
//     它会新建一个空壳，把原来的立绘丢在旧目录里。
//
// **重名直接拒绝**：名字就是身份，重名会让 fixedRole / order / 台词游标指向两个人。
// 判据是 roleIdentity（目录名口径），见 roleNameTaken。设置页会在提交前先查一遍并
// 要求用户重新输入；这里再兜一层底，防止有别的入口绕过检查（返回 false，
// 前端据此提示「名称已存在」并把输入框留在对话框里）。
func (a *App) SaveRole(name string, patch map[string]any) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		log.Printf("[role] 空的督休专员名")
		return false
	}
	dir, ok := role.ResolveRoleDir(a.rolesRoot, name)
	if !ok {
		log.Printf("[role] 督休专员不存在或 meta.json 读不出来：%s", name)
		return false
	}
	base, ok := role.ReadRoleFile(a.rolesRoot, name)
	if !ok {
		return false
	}
	next := role.ApplyRolePatch(base, patch)
	if next.Name == "" {
		next.Name = name
	}
	// 名称变了 → 先查重名（排除自己），重了直接拒绝，不动目录也不写 meta.json
	if next.Name != base.Name && role.RoleNameTaken(a.rolesRoot, next.Name, base.Name) {
		log.Printf("[role] 名称重复，拒绝保存：%s → %s", base.Name, next.Name)
		return false
	}
	if err := role.WriteRoleFileAt(dir, next); err != nil {
		log.Printf("[role] 写入失败 %s: %v", next.Name, err)
		return false
	}
	if next.Name != base.Name {
		role.RenameRoleDir(a.rolesRoot, dir, next.Name)
	}
	return true
}

// RemoveRole 删除督休专员 = 整个督休专员子目录一并删除（name 是专员名）
//
// 删完顺手把配置里指向它的两处清掉：`supervisor.fixedRole`（固定模式会指向一个不存在的
// 名字 → 休息窗口取不到人）与 `supervisor.order`（顺序表里的陈旧名字：显示上无害，
// 但会让配置随删随涨）。
// 设置页也会清一遍（它手里有 S，清完再 autoSave），两边一致才不会互相覆盖。
func (a *App) RemoveRole(name string) bool {
	if !role.RemoveRoleDir(a.rolesRoot, name) {
		log.Printf("[role] 删除失败：%s", name)
		return false
	}
	a.clearRoleFromSettings(name)
	return true
}

// clearRoleFromSettings 把已删除的督休专员从配置里摘干净（fixedRole / order）。
// 拿不到配置就什么都不做（配置还没加载时删目录本来也删不成）。
func (a *App) clearRoleFromSettings(name string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cfg == nil {
		return
	}
	raw := a.cfg.Raw()
	sup, ok := raw["supervisor"].(map[string]any)
	if !ok {
		return
	}
	changed := false
	if s, _ := sup["fixedRole"].(string); s != "" && strings.EqualFold(strings.TrimSpace(s), strings.TrimSpace(name)) {
		sup["fixedRole"] = ""
		changed = true
	}
	if order, ok := sup["order"].([]any); ok {
		kept := make([]any, 0, len(order))
		for _, v := range order {
			if s, _ := v.(string); strings.EqualFold(strings.TrimSpace(s), strings.TrimSpace(name)) {
				changed = true
				continue
			}
			kept = append(kept, v)
		}
		sup["order"] = kept
	}
	if !changed {
		return
	}
	s := settings.New(raw, a.cfg.Path())
	settings.NormalizeForSave(raw)
	if err := s.Save(); err != nil {
		log.Printf("[settings] 清理已删除督休专员的配置失败：%v", err)
		return
	}
	a.cfg = s
}

// PickSupervisorImage 新建自定义督休专员（选图 → 建督休专员目录）。
//
// 返回 **null**（不是 {canceled:true}）：设置页的写法是 `const picked = await …;
// if (!picked) return;` 紧接着取 `picked.name.trim()` —— 返回一个带 canceled 的**对象**
// 是真值，会一路走到 `undefined.trim()` 直接抛错。
//
// 预览图：上一版用 nativeImage 把立绘缩到 192 宽写 preview.png；本工程不在 Go 侧引
// 图像库（CGO_ENABLED=0，引 golang.org/x/image 会让 exe 涨 1.16 MB），缩略图改由
// **渲染层**生成 —— 建完目录后 settings.js 调 RoleFigureDataURL 取原图、canvas 裁掉
// 透明边并缩到磁贴高度（184）、编码 WebP，再交 SaveRolePreview 落盘（见 role_preview.go）。
// 这一步失败也不影响导入：没有 preview.* 时 scanRoles 会让磁贴回退到立绘原图。
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
	defaultName := strings.TrimSuffix(filepath.Base(src), filepath.Ext(src))
	saved := role.CreateRoleDir(a.rolesRoot, defaultName, src)
	if saved == "" {
		log.Printf("[role] 创建督休专员目录失败：%s", src)
		return nil
	}
	// 返回两个名字，别混：
	//   - name  = 文件名原样，是命名框的**预填**内容（用户提交的就是它 —— 查重必须
	//             作用在用户输入上；若预填「名字 (2)」，用户点确定就直接存了个带序号的
	//             怪名字，而「重名要拒绝」这条规则几乎走不到）；
	//   - saved = 磁盘上此刻真正落盘的名字（createRoleDir 已用 uniqueRoleName 去过重）。
	// 用户改成一个被别人占用的名字时，SaveRole 会拒绝（返回 false），
	// 设置页把错误留在命名框里、要求重新输入。
	return map[string]any{"name": defaultName, "saved": saved}
}

// ReplaceRoleImage 修改某个已有督休专员的立绘图片（选图 → 覆盖 figure / preview → 回写 meta.json）。
//
// 与 PickSupervisorImage（新建督休专员）不同：这里专员名（= 身份）不变、只换图，
// 名字 / 启停 / 几何 / 台词都保留。返回是否成功（取消 / 无头环境 / 失败都返回 false，
// 设置页据此静默）。
func (a *App) ReplaceRoleImage(name string) bool {
	if strings.TrimSpace(name) == "" {
		log.Printf("[role] 空的督休专员名")
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
	return role.ReplaceRoleImage(a.rolesRoot, name, r.path)
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
			"error":    fmt.Sprintf("不支持的音频格式，或文件超过 20 MB（支持 %s）", strings.Join(sound.Exts, " / ")),
		}
	}
	return map[string]any{"canceled": false, "items": items, "skipped": skipped}
}

// audioFilterPattern 音乐对话框的文件过滤器（把扩展名白名单拼成 *.ext;*.ext 形式）
func audioFilterPattern() string {
	exts := make([]string, 0, len(sound.Exts))
	for _, e := range sound.Exts {
		exts = append(exts, "*."+e)
	}
	return strings.Join(exts, ";")
}

// ListRunningApps 运行中的程序列表（「指定应用运行时暂停」卡片的第三个添加入口）。
//
// 返回的是 **`{supported, names, detail}` 对象**而不是字符串数组 —— 设置页要按
// `r.supported` 分流（不支持时把 detail 当说明文字显示），旧占位返回 `[]string{}`
// 会让 `.supported` 恒为 undefined，页面永远停在「当前平台不支持读取程序列表」。
func (a *App) ListRunningApps() winproc.AppProcessList { return winproc.ListProcessNames() }

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
	// 尺寸记忆：拖拽改大小 → 防抖 1 秒后写进配置（「自动记录窗口尺寸」没勾时内部直接返回）。
	// 回调里只登记尺寸 + 重置定时器，写盘在定时器自己的 goroutine 上做，
	// 拖拽期间不会因为文件 IO 卡住主线程。
	w.OnWindowEvent(events.Common.WindowDidResize, func(*application.WindowEvent) {
		a.rememberSettingsWindowSize()
	})
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
	// 只为回收子进程（explorer 退出后不留僵尸）。包兜底是为了让「全仓裸 goroutine 为零」
	// 这条能被源码扫描测试钉住 —— 少一个例外，检查就少一处人工判断。
	go func() { logx.GuardRun("shell.wait", func() { _ = cmd.Wait() }) }()
	return true
}
