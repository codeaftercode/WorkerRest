package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// Go + Wails v3 的后端。前端复用 上一版 renderer 的产物，只通过 shim.js 把
// window.api 映射到这里的绑定方法（见 frontend/dist/shim.js 与 contract.go）。
//
// 多窗口是选 Wails v3（而非 v2）的原因：本项目核心交互就是多个无边框透明窗口
// （每屏一个提醒窗口 + 外卖窗口 + 提前提示窗口 + 设置窗口），v2 只有单窗口。
type App struct {
	app *application.App

	mu   sync.Mutex
	wins map[string]*application.WebviewWindow
	// breakWins 休息提醒窗口（每块目标屏一个，见 ShowBreakWindow；a.mu 保护）。
	// 不进 wins map：wins 的键按窗口名寻址，而多屏下每块屏一个窗、同名会互相覆盖。
	breakWins []*application.WebviewWindow
	sess      *BreakInfo
	start     time.Time

	cfg       *Settings // 整份配置（见 settings.go 的说明；保存时会整体替换指针）
	rolesRoot string     // 督休专员目录落点（随包 supervisors/，见 resourcesRoot）
	sched     *Scheduler

	// 台词选取：random 用 rng（测试可固定种子），sequence 的游标按 key 记忆
	rng      *rand.Rand
	cursor   map[string]int
	roleTurn int // 督休专员本体按顺序轮换时的游标

	// 最近一次检查更新结果（内存态，但会通过 lastUpdateResult 持久化到配置，
	// 使「发现新版本但未更新」在重启后仍显示在关于页，直到更新成功才清除）
	updateStatus map[string]any
	// activeTab 设置页当前所在的 tab（前端每次切页通过 SetActiveTab 上报，
	// 用于在自更新重启后恢复「更新前所在的页面」）。默认 general。
	activeTab string

	// 托盘（server 模式为 nil）：状态一变就刷新提示与菜单文案
	tray          *application.SystemTray
	trayStatus    *application.MenuItem
	trayAction    *application.MenuItem
	trayPause     *application.MenuItem
	trayAutostart *application.MenuItem
	// trayStop 托盘定时刷新循环的停止信号（nil = 未启动）：剩余时间流逝但调度状态没变时，
	// 靠它每秒重算 tooltip / 菜单里的「约 X 分钟后」（见 tray.go）。
	trayStop chan struct{}
	// trayLast 上一次写到托盘/菜单的文案（a.mu 保护）：只在变化时才写，秒级 tick 不灌原生调用
	trayLast trayLabels

	// 提前提示窗当前该显示的内容（打开时算一次，轮询期间不随计划漂移；见 prebreak.go）
	preInfo PreBreakInfo
	// 外卖预通知：当前预通知所指向的「原定时刻」与条目（倒计时与动作据此判断）。
	// 仅在外卖预通知窗打开期间有意义；owner 标记见 prebreak.go 的 PreBreakInfo.Owner。
	preTargetAt     int64
	preTakeoutSlot  TimeSlot
	takeoutPreFired map[string]bool // 外卖预通知去重键（按 occurrence，不含提前量）

	// 提示音（见 sound.go）：顺序播放的游标跨播放持续递增（不是每次从头开始）
	soundCursor int

	// 外卖提醒（见 takeout.go）：已触发去重键 + 待推迟提醒 + 当前会话 + 在场窗口。
	// takeoutFired 同时是状态快照的输入（timeline 与 fixedNext.takeout 的口径与上一版 一致）。
	takeoutFired     map[string]bool
	takeoutPostponed *TakeoutPostponed
	takeoutCurrent   *TakeoutInfo
	takeoutWins      []*application.WebviewWindow
	// takeoutStop / takeoutRaiseStop 外卖每秒检查与抬升循环的停止信号（nil = 未启动）
	takeoutStop      chan struct{}
	takeoutRaiseStop chan struct{}
	// raise 窗口层级簿记（对应 windows/raise.ts）
	raise raiseState
	// supWatch / supWatchStop 系统级键鼠活动轮询（对应 windows/reminderSync.ts）
	supWatch     supWatchInfo
	supWatchStop chan struct{}

	// 系统能力（见 sys_*.go）：空闲状态机 + 三条监视链路的停止信号与「上一跳状态」。
	// idleAway / dndActiveLogged / appPauseHitLogged 只被各自的 goroutine 读写，
	// 提成字段是为了让单测能直接调 tick 函数；它们的值不影响任何跨链路判定。
	// appPauseIgnored 例外：监视 goroutine 与「手动恢复」的 UI 线程都会碰，走 appPauseMu。
	idle              *IdleState
	sysStop           chan struct{}
	idleAway          bool
	dndActiveLogged   bool
	appPauseHitLogged bool
	// appPauseIgnored 「指定应用临时忽略」会话：自动暂停后用户手动点「恢复休息」时置位，
	// 监视器不再因目标应用重新暂停；目标应用全部退出或用户手动暂停时解除。
	appPauseIgnored bool
	appPauseMu      sync.Mutex

	// 全局快捷键（见 hotkeys.go）：server 模式为 nil。
	hotkeys *HotkeyManager

	// 使用统计（见 stats.go）：天记录 + 名字快照 + 防抖落盘 + 每秒工作累加。
	// 懒建（statsStore 首次访问时建好并读盘），启动时由 startStats 起后台 goroutine。
	stats *StatsStore
}

func NewApp(app *application.App) *App {
	a := &App{
		app:             app,
		wins:            make(map[string]*application.WebviewWindow),
		start:           time.Now(),
		rng:             rand.New(rand.NewSource(time.Now().UnixNano())),
		cursor:          make(map[string]int),
		takeoutFired:    map[string]bool{},
		takeoutPreFired: map[string]bool{},
		idle:            NewIdleState(),
		activeTab:       "general",
	}
	a.rolesRoot = filepath.Join(a.resourcesRoot(), "supervisors")
	// 旧版「roles/」目录（目录名=英文 key、role.json 无 key 字段）一次性迁到 supervisors/
	// （目录名=专员名称、role.json 注入 key）：兼容老用户升级，不丢配置。
	migrateRolesLayout(a.resourcesRoot())
	// 配置落盘目标需要「随包数据目录」来推导便携路径（首次运行还没有配置文件时用）
	resourcesRootHintFn = a.resourcesRoot
	// 内置提示音随包目录：<resourcesRoot>/sounds（打包态 exe 旁、开发态模块根）
	builtinSoundDirFn = func() string { return filepath.Join(a.resourcesRoot(), "sounds") }
	a.cfg = loadSettings(a.resourcesRoot())
	// 旧版「督休专员数据存 settings.json」→ 督休专员目录 role.json：一次性迁移，迁移了就当场落盘
	// （对应 历史源码/main.ts 的 `if (migrateLegacyRoles(settings)) saveSettings(settings)`）。
	// 必须在 loadSettings 之后 —— 迁移要读归一化后的 supervisor 配置，还要写 role.json。
	if migrateLegacyRoles(a.cfg.Raw(), a.rolesRoot) {
		if err := a.cfg.Save(); err != nil {
			log.Printf("[supervisor] 迁移后配置落盘失败: %v", err)
		}
	}
	return a
}

// ServiceStartup 由 Wails 在服务注册后调用（替代 上一版的 app.whenReady）
func (a *App) ServiceStartup(_ context.Context, _ application.ServiceOptions) error {
	ap := a.cfg.AppearanceCfg()
	log.Printf("[boot] 启动完成 engine=go+wails3 serverMode=%v", serverMode)
	log.Printf("[boot] 督休专员目录 %s（扫描到 %d 个督休专员）", a.rolesRoot, len(scanRoles(a.rolesRoot)))
	log.Printf("[boot] 配置 %s；外观 %s %.0f%%x%.0f%%，短休息 %d ms",
		orNone(a.cfg.Path()), ap.Color, ap.WidthPct, ap.HeightPct, a.cfg.Break("short").DurationMs)
	// 前端契约自检：shim.js 按名字调用绑定，JS 侧拼错没有任何编译期检查，
	// 只能在这里把「契约里有、Go 侧没有」的方法名点出来
	if missing := missingFrontendBindings(); len(missing) > 0 {
		log.Printf("[boot] ⚠ 前端契约缺失绑定：%v（shim.js 调用会静默失败）", missing)
	} else {
		log.Printf("[boot] 前端契约自检通过：%d 个绑定方法全部存在（已接通 %d + 待接通 %d）",
			len(allCalls()), len(wiredCalls), len(pendingCalls))
	}
	a.startScheduler()
	log.Printf("[plan] %s short=%s long=%s", a.sched.planSummary(),
		a.cfg.Break("short").Mode, a.cfg.Break("long").Mode)
	// 系统能力：空闲 / 勿扰 / 应用占用（无头模式只做探测，不启动会改状态的监视）
	a.startSystemWatchers()
	// 全局快捷键：专用消息循环线程 + RegisterHotKey（server / 无头跳过，见 hotkeys.go）
	a.startHotkeys()
	// 使用统计：读盘 + 周期落盘 + 每秒工作累加（两条判定注入，见 statsIsBreak / statsIsIdle）
	a.startStats()
	a.startTray()
	// 外卖提醒：每秒检查固定时刻线与推迟线（对应 上一版的 startTakeoutWatcher）
	a.startTakeoutWatcher()
	// 提示音宿主：一个隐藏的 1×1 窗口承载 <audio>（主进程自己放不了声音，见 sound.go）。
	// 无头构建下只记日志、不建窗。
	a.initSoundHost()
	// 不显示设置窗口、直接最小化到托盘的两条路径：开机自启动拉起，或用户勾选了
	// 「启动时最小化到托盘」（此前只认自启动标志，勾选无效 —— 窗口照常弹出）。
	// 与 上一版 `if (!launchedAtLogin()) openSettingsWindow(...)` 同口径。
	// 服务的启动发生在「窗口真正被 run 起来」之前，所以这里的 Hide 不会闪一下窗口。
	if hasAutostartFlag() || a.cfg.StartMinimized() {
		if w := a.settingsWindow(); w != nil {
			w.Hide()
		}
		if hasAutostartFlag() {
			log.Printf("[autostart] 本次由开机自启动拉起：只起托盘，不显示设置窗口")
		} else {
			log.Printf("[boot] startMinimized：只起托盘，不显示设置窗口")
		}
	}
	// 恢复上次「发现新版本但未更新」的状态（重启后仍显示「有更新」，见 update.go）。
	a.restoreUpdateStatus()
	// 启动时检查更新：按配置在首次非空闲时静默查一次（失败不打扰，见 update.go）。
	a.startAutoUpdateCheck()
	return nil
}

// hasAutostartFlag 本次进程是不是被开机自启动拉起来的（Windows 看命令行，与
// autostart.ts 写入 Run 键时带的那个参数同名）
func hasAutostartFlag() bool {
	for _, arg := range os.Args[1:] {
		if arg == autostartFlag {
			return true
		}
	}
	return false
}

// ServiceShutdown 退出清理
func (a *App) ServiceShutdown() error {
	if a.sched != nil {
		a.sched.Stop()
	}
	a.stopTakeoutWatcher()
	a.closeTakeoutWindows()
	a.stopSystemWatchers()
	a.stopTrayRefresh()
	a.destroySoundHost()
	// 统计收尾：无条件写一次盘（比 2 秒防抖 / 1 分钟兜底更早生效，退出时不丢账）
	a.stopStats()
	log.Println("[boot] 退出")
	return nil
}

func orNone(s string) string {
	if s == "" {
		return "(未找到，使用出厂默认)"
	}
	return s
}

// cfgSnapshot 取当前配置指针。保存配置时会**整体替换** a.cfg（新对象），所以读取一律
// 先拿一次快照再用——不要在别处直接摸 a.cfg，那会和 settings:save 抢同一块内存。
func (a *App) cfgSnapshot() *Settings {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg
}

// ---------- 路径 ----------

// resourcesRoot 定位随包数据目录（supervisors/ 等）：打包态取 exe 旁，开发态回退到模块根。
// supervisors/ 随 Go 工程一起分发（在模块根下），不再复用上层 上一版工程的数据。
func (a *App) resourcesRoot() string {
	exe, err := os.Executable()
	if err != nil || isDevBuild(exe) {
		return a.devOrCwd()
	}
	dir := filepath.Dir(exe)
	if hasSupervisorsDir(dir) {
		return dir
	}
	// 开发态：exe 落在 build/bin/ 这类构建输出目录里，supervisors/ 在模块根。
	// 判据是向上找到**最近的 go.mod 所在目录**（= 模块根）——打包安装目录的上方
	// 不会有 go.mod，所以这条回退在正式环境里不会触发、也不会误认别人的 supervisors/。
	if modRoot := nearestModuleRoot(dir); modRoot != "" {
		return modRoot
	}
	return dir
}

// devOrCwd 非可执行路径可判时的回退（go run / IDE 直接跑）
func (a *App) devOrCwd() string {
	wd, _ := os.Getwd()
	if hasSupervisorsDir(wd) {
		return wd
	}
	// 在模块根的子目录里直接 run 时，向上找模块根（supervisors/ 随工程在模块根下）
	if modRoot := nearestModuleRoot(wd); modRoot != "" {
		return modRoot
	}
	return wd
}

// nearestModuleRoot 从 dir 起向上找最近的、含 go.mod 的目录；找不到返回空串
func nearestModuleRoot(dir string) string {
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

func hasSupervisorsDir(dir string) bool {
	st, err := os.Stat(filepath.Join(dir, "supervisors"))
	return err == nil && st.IsDir()
}

// migrateRolesLayout 把旧版「roles/」目录整体迁到新的「supervisors/」目录：
// 旧目录名是英文 key、role.json 没有 key 字段；新布局目录名用专员名称、role.json 注入 key。
// 只在检测到「旧 roles/ 存在、新 supervisors/ 尚未就位」时跑一次（升级老用户数据，不丢配置）。
// 若 supervisors/ 已就位（随包模板已带）则不自动搬——避免冲掉模板或让已删内置复活；
// 若两者皆无则建一个空 supervisors/ 兜底，后续逻辑按空目录处理。
func migrateRolesLayout(resRoot string) {
	oldRoot := filepath.Join(resRoot, "roles")
	newRoot := filepath.Join(resRoot, "supervisors")
	if _, err := os.Stat(newRoot); err == nil {
		// 新版 supervisors/ 已就位：不覆盖、不搬，避免冲掉随包模板 / 让已删内置复活
		if _, err2 := os.Stat(oldRoot); err2 == nil {
			log.Printf("[supervisor] 检测到旧 roles/ 但 supervisors/ 已存在：旧目录暂不自动迁移（自定义督休专员请手动合并）")
		}
		return
	}
	if _, err := os.Stat(oldRoot); err != nil {
		// 都没有：建空 supervisors/ 兜底
		_ = os.MkdirAll(newRoot, 0o755)
		return
	}
	// 只有旧 roles/、没有新 supervisors/：整体搬过去（升级老用户）
	if err := os.MkdirAll(newRoot, 0o755); err != nil {
		log.Printf("[supervisor] 迁移：创建 supervisors/ 失败 %v", err)
		return
	}
	entries, err := os.ReadDir(oldRoot)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !validRoleKey(e.Name()) {
			continue
		}
		oldDir := filepath.Join(oldRoot, e.Name())
		raw, rerr := os.ReadFile(filepath.Join(oldDir, "role.json"))
		roleKey := e.Name() // 旧目录名即 key
		roleName := roleKey
		if rerr == nil {
			var m map[string]any
			if json.Unmarshal(raw, &m) == nil {
				if k, ok := m["key"].(string); ok && k != "" {
					roleKey = k
				}
				if n, ok := m["name"].(string); ok && strings.TrimSpace(n) != "" {
					roleName = n
				}
			}
		}
		newDir := uniqueRoleDir(newRoot, sanitizeRoleDirName(roleName), "")
		if err := os.Rename(oldDir, newDir); err != nil {
			log.Printf("[supervisor] 迁移：移动 %s 失败 %v", e.Name(), err)
			continue
		}
		// 回写 key 字段到新目录的 role.json（缺失才补，不覆盖已有值）
		if rerr == nil {
			var m map[string]any
			if json.Unmarshal(raw, &m) == nil {
				if cur, ok := m["key"].(string); !ok || cur == "" {
					m["key"] = roleKey
					if out, jerr := json.MarshalIndent(m, "", "  "); jerr == nil {
						out = append(out, '\n')
						_ = os.WriteFile(filepath.Join(newDir, "role.json"), out, 0o644)
					}
				}
			}
		}
		log.Printf("[supervisor] 迁移：%s → %s（key=%s）", e.Name(), filepath.Base(newDir), roleKey)
	}
	// 旧目录搬空后移除
	if entries2, _ := os.ReadDir(oldRoot); len(entries2) == 0 {
		_ = os.RemoveAll(oldRoot)
	} else {
		log.Printf("[supervisor] 迁移：旧 roles/ 仍有残留，未删除：%v", oldRoot)
	}
}

func isDevBuild(exe string) bool {
	return len(exe) > 0 && (strings.Contains(exe, "go-build") || strings.Contains(exe, "Temp"))
}

// ---------- 会话构建 ----------

// buildSupervisorSession 组装下发给渲染层的督休专员信息：
// 与 历史源码/supervisor.ts 的 buildSupervisorSession 同规则——按 selection
// 从督休专员目录里选一个督休专员，随机取一条劝说台词兜底，命令台词从该督休专员 role.json 抽。
// 关掉开关、没有可用督休专员、立绘读不出来时返回 nil，渲染层退回无督休专员布局。
func (a *App) buildSupervisorSession(force bool, forceKey string) *SupStageInfo {
	return a.buildSupervisorSessionWith(a.cfg.SupervisorCfg(), force, forceKey)
}

// buildSupervisorSessionWith 同上，但允许传入一份**未落盘**的督导配置覆盖：
// 设置页调整督休专员选择后立刻要预览，此时配置还在表单里、没保存到磁盘。
//
// 顶层「督休专员-启用」开关**始终**生效：预览窗与真实休息一视同仁——未启用时
// 一律不显示立绘与对话框（设置页点「预览」也应如实反映这个勾选状态）。
// force 现在只用于**绕过单个督休专员的启用**（预览某个已停用的督休专员时仍要能看到效果），
// 不再绕过顶层总开关。
func (a *App) buildSupervisorSessionWith(cfg SupervisorConfig, force bool, forceKey string) *SupStageInfo {
	if !cfg.Enabled {
		return nil
	}
	all := scanRoles(a.rolesRoot)
	usable := make([]Role, 0, len(all))
	for _, r := range all {
		if r.FigurePath != "" {
			usable = append(usable, r)
		}
	}
	if len(usable) == 0 {
		return nil
	}

	var picked *Role
	switch {
	case forceKey != "":
		// 预览指定的督休专员：无视 enabled（预览要能看到已关闭督休专员的效果）
		for i := range usable {
			if usable[i].Key == forceKey {
				picked = &usable[i]
				break
			}
		}
	case cfg.Selection == "random":
		if pool := a.enabledRoles(usable, force); len(pool) > 0 {
			picked = &pool[a.rng.Intn(len(pool))]
		}
	case cfg.Selection == "sequence":
		if pool := a.enabledRoles(usable, force); len(pool) > 0 {
			picked = &pool[a.roleTurn%len(pool)]
			a.roleTurn = (a.roleTurn + 1) % len(pool)
		}
	default: // fixed：取指定督休专员。固定显示只看磁贴点选本身，**不看单条启用**——
		// 复选框（role.enabled）只在随机 / 顺序模式下出现；若这里仍拿 enabled 当门槛，
		// 随包 role.json 里 enabled=false 的督休专员就会出现「立绘列表看得到、
		// 休息时不显示」的劈叉（设置页总开关是启用的，用户无从排查）
		for i := range usable {
			if usable[i].Key == cfg.FixedKey {
				picked = &usable[i]
				break
			}
		}
		if picked == nil {
			// 指定的督休专员不存在（已删除）：兜底到第一个可用的
			if len(usable) > 0 {
				picked = &usable[0]
			}
		}
	}
	if picked == nil {
		return nil
	}

	image := toDataURL(picked.FigurePath)
	if image == "" {
		return nil
	}
	name := picked.Name
	if name == "" {
		name = "督休专员"
	}
	return &SupStageInfo{
		Image:    image,
		ImageKey: picked.Key,
		Name:     name,
		Gaze:     cfg.Gaze,
		Geom:     &picked.Geom,
		Lines: SupLines{
			// 第一段兜底：提醒内容为空时督休专员不至于哑口（正常由渲染层优先用提醒的提醒内容）
			Urge: pickOne(urgeLines, a.rng),
			// 第二段（命令）：该督休专员 role.json 的台词池（空池返回空串，渲染层沿用第一段台词）
			Command: pickRoleLine(picked.Messages, picked.Key, a.cursor, a.rng),
		},
	}
}

// SupervisorPreview 设置页的立绘预览：返回一次「督休专员会话」而不是弹窗
// （对应 上一版的 supervisor:preview → buildSupervisorSession(settings, true, key)）。
//
// sup 是设置页表单里**当前**的督导配置（可能还没落盘），null 时用磁盘上的配置。
// 与弹窗预览的区别：同样尊重顶层「启用」开关（未启用返回 nil，页面据此隐藏立绘与对话框），
// 但 force=true 仍可绕过单个督休专员的启用（预览某个已停用的督休专员），forceKey 指定要预览的督休专员。
func (a *App) SupervisorPreview(sup map[string]any, key string) *SupStageInfo {
	a.mu.Lock()
	defer a.mu.Unlock()
	cfg := a.cfg.SupervisorCfg()
	if sup != nil {
		// 与 上一版的 {...settings, supervisor: sup} 同义：整份替换，缺的字段按默认
		sel := strOf(sup["selection"])
		if sel != "sequence" && sel != "random" {
			sel = "fixed"
		}
		cfg = SupervisorConfig{
			Enabled:   sup["enabled"] != false,
			Gaze:      sup["gaze"] != false,
			Selection: sel,
			FixedKey:  strOf(sup["fixedKey"]),
		}
	}
	return a.buildSupervisorSessionWith(cfg, true, key)
}

// enabledRoles 过滤掉已禁用督休专员（force=true 预览时不过滤）
func (a *App) enabledRoles(all []Role, force bool) []Role {
	if force {
		return all
	}
	out := make([]Role, 0, len(all))
	for _, r := range all {
		if r.Enabled {
			out = append(out, r)
		}
	}
	return out
}

// pickMessage 选本次要显示的提醒内容（与 上一版 pickMessageOrName 同规则）：
// 固定取 fixedId，顺序轮换，随机等概率；一条都没有时用提醒名称兜底，保证窗口不空。
// 返回的切片至少 1 条——渲染层把它整块渲染，同时把第 0 条当督休专员第一段台词。
//
// kind 可以是 'short' / 'long' / 'takeout'，也可以是自定义提醒的 id（各有一套自己的池）。
func (a *App) pickMessage(kind, fallbackName string) []string {
	rm := a.cfg.RemindersByID(kind)
	if rm.Order == "fixed" {
		for _, it := range rm.Items {
			if it.ID == rm.FixedID && it.Text != "" {
				return []string{it.Text}
			}
		}
	}
	pool := make([]string, 0, len(rm.Items))
	for _, it := range rm.Items {
		if it.Enabled && it.Text != "" {
			pool = append(pool, it.Text)
		}
	}
	if len(pool) == 0 {
		return []string{fallbackName}
	}
	if rm.Order == "sequence" {
		// 顺序游标按「类别」记忆（督休专员台词用同一张表的另一个键，互不干扰）
		key := "msg-" + kind
		cur := a.cursor[key] % len(pool)
		a.cursor[key] = (cur + 1) % len(pool)
		return []string{pool[cur]}
	}
	return []string{pool[a.rng.Intn(len(pool))]}
}

// showBreakSession 构建一次休息会话并弹窗，返回会话 id。
// 真实休息（调度器 / 托盘）与预览共用它，差别只在 preview 标志。
//
// 会话构建与「弹窗」必须分开：渲染层随后会自己再拉一次 breakCurrent，
// 所以这里先把会话写完再放窗（跨多次调用期间保持一致）。
func (a *App) showBreakSession(kind string, preview bool, forceKey string) string {
	a.mu.Lock()
	cfg := a.cfg
	bcfg, ok := cfg.BreakByID(kind)
	if !ok {
		a.mu.Unlock()
		log.Printf("[break] 未知的休息身份 %q（可能刚被删除），忽略", kind)
		return ""
	}
	name := cfg.BreakNameByID(kind)
	dur := bcfg.DurationMs
	if dur <= 0 {
		dur = 20_000
	}
	now := time.Now()
	// 提前结束快捷键与「设置 → 快捷键」的 endEarly 完全一致：休息窗口据此显示提示，
	// 本地 keydown 也按它匹配（全局快捷键已由 hotkeys.go 按同一组合键注册）。
	endEarly := ""
	if sc, ok := a.cfg.Raw()["shortcuts"].(map[string]any); ok {
		if s, ok := sc["endEarly"].(string); ok {
			endEarly = strings.TrimSpace(s)
		}
	}
	info := &BreakInfo{
		ID:               fmt.Sprintf("%s-%d", kind, now.UnixMilli()),
		Type:             kind,
		Name:             name,
		EndsAt:           now.Add(time.Duration(dur) * time.Millisecond).UnixMilli(),
		DurationMs:       dur,
		Preview:          preview,
		Appearance:       cfg.AppearanceView(),
		Messages:         a.pickMessage(kind, name),
		Supervisor:       a.buildSupervisorSession(preview, forceKey),
		EndEarlyShortcut: endEarly,
	}
	a.sess = info
	a.mu.Unlock()

	// 休息真的开始了：顺手收掉右下角的提前提示窗
	//（对应 上一版 在 break-start 事件里调 closePreBreakWindow）。
	a.closePreBreakWindow()
	a.ShowBreakWindow(kind)
	// 提示音：只给真实休息放（预览是「看一眼窗口长什么样」，不该出声）
	if !preview {
		a.playConfiguredSound()
		// 统计：休息开始 —— 从这里起休息时长逐秒实时入账（见 StatsStore.OnBreakTick），
		// 中途断电最多丢 1 分钟，而不是把整次休息的时长全丢掉
		a.statsStore().OnBreakStart(kind)
	}
	return info.ID
}

// endEarlyToleranceMs 「提前结束」判定的容差：结束时刻必须早于计划结束这么多才算提前。
// 自然到点由调度器的定时器驱动，正常不会早于 EndsAt，这个容差只是不被毫秒级抖动误伤。
const endEarlyToleranceMs = 1500

// closeBreakWindows 关掉休息窗口并清掉会话（调度器收尾路径用，不经过渲染层）。
// 目前唯一的调用点是调度器的 finishBreak，所以这里顺带承担「休息结束」的副作用：
// 播一次提示音（对应 上一版的 break-end → playConfiguredSound）。
func (a *App) closeBreakWindows() {
	a.mu.Lock()
	ws := a.breakWins
	a.breakWins = nil
	info := a.sess
	a.sess = nil
	a.mu.Unlock()
	for _, w := range ws {
		w.Close()
	}
	a.playConfiguredSound()
	// 统计：休息结束（唯一调用点是调度器的 finishBreak，所以这里就是 break-end）。
	// 类型取自会话本身 —— 休息结束时调度器状态可能已经翻篇，而记录要用**刚刚结束的那次**；
	// 自定义提醒把名字快照一起记下来，提醒删了 / 改名了报表也还能对上号。
	if info != nil && !info.Preview {
		name := ""
		if cfg := a.cfgSnapshot(); cfg != nil {
			name = cfg.BreakNameByID(info.Type)
		}
		// 提前结束：结束时刻明显早于计划结束。留 1.5 秒容差 —— 自然到点由定时器驱动，
		// 正常不会早于 EndsAt，但别让抖动被记成「提前结束」。
		// 口径只含短休息 / 长休息：自定义提醒与外卖不算（它们也不在这个窗口里结束）。
		early := (info.Type == "short" || info.Type == "long") &&
			nowMs()+endEarlyToleranceMs < info.EndsAt
		// 提前结束的那次不计入「完成」，OnBreakEnd 内部按 early 分开记
		a.statsStore().OnBreakEnd(name, early)
	}
	// 休息窗口关闭：把空闲基准清零（无论是否有统计会话 / 是否预览），否则
	// 「刚休息完、还没动鼠标」进入 idle 后会被立刻误判成离开而暂停
	if a.idle != nil {
		a.idle.ResetBaseline()
	}
}

// ---------- 调度器事件（上一版 scheduler.on(...) 的对应物） ----------

// onSchedulerState 状态一变：推给设置页 + 刷新托盘
func (a *App) onSchedulerState() {
	a.pushState()
	a.refreshTray()
}

// onPreNotify 进入「距下次休息不足 notifyBeforeMs」的区间了：弹出右下角的提前提示小窗
// （系统通知在 Windows 上带不了按钮，所以是自绘窗口，见 prebreak.go）。
//
// 内容在这里**算一次**并记进 a.preInfo：窗口内轮询 PreBreakState 拿到的是同一份，
// 中途计划漂移（比如固定时刻线重排）不会让标题 / 后缀跟着变。
func (a *App) onPreNotify(kind string, notifyBeforeMs int64) {
	info := a.rememberPreBreakInfo(kind)
	log.Printf("[prebreak] 即将休息：%s（提前 %d ms）custom=%v title=%q",
		kind, notifyBeforeMs, info.Custom, info.Title)
	a.ShowPreBreakWindow(info)
}

// onPaused 暂停提醒：关掉提前提示窗。托盘暂停与弹窗内「暂停计时」共用这条路径；
// 恢复时若仍在提前提示区间内，调度器会再走一次 onPreNotify 把窗口弹回来（倒计时继续）。
func (a *App) onPaused() {
	a.closePreBreakWindow()
}

// closePreBreak 调度器重排时关掉残留的提前提示窗（对应 schedHost.closePreBreak）。
// 与 onPaused 的区别：这里只关窗、不改 pause 状态，专供「被禁用 / 重排的提醒」清场。
func (a *App) closePreBreak() {
	a.closePreBreakWindow()
}

// ---------- 外卖状态的快照（供状态快照读取） ----------

func (a *App) takeoutPostponedSnapshot() *TakeoutPostponed {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.takeoutPostponed == nil {
		return nil
	}
	cp := *a.takeoutPostponed
	return &cp
}

func (a *App) takeoutFiredSnapshot() map[string]bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.takeoutFired) == 0 {
		return nil
	}
	out := make(map[string]bool, len(a.takeoutFired))
	for k := range a.takeoutFired {
		out[k] = true
	}
	return out
}

// ---------- 窗口 ----------

// breakBoundsFor 提醒窗口在**指定显示器**工作区上的位置与大小：与 上一版
// breakWindow.ts 同规则——该屏工作区的 widthPct × heightPct，居中；全屏时铺满工作区。
// 纯函数，多屏下对每块目标屏各算一次（单测直接验）。
func breakBoundsFor(wa application.Rect, cfg AppearanceConfig) (x, y, w, h int) {
	if cfg.Fullscreen {
		return wa.X, wa.Y, wa.Width, wa.Height
	}
	w = int(float64(wa.Width) * cfg.WidthPct / 100)
	h = int(float64(wa.Height) * cfg.HeightPct / 100)
	x = wa.X + (wa.Width-w)/2
	y = wa.Y + (wa.Height-h)/2
	if w < 320 {
		w = 320
	}
	if h < 240 {
		h = 240
	}
	return
}

// ShowBreakWindow 弹出休息提醒窗口（托盘 / 设置页按钮 / 调度器都走这里）。
// 无头（-tags server）构建下没有原生窗口，跳过创建——会话数据仍然构建，
// 因此无头构建可以完整验证「绑定 → 会话 JSON」这条链路。
//
// 多屏：按「在所有屏幕显示」开关对每块目标屏各建一个窗（与外卖窗口同策略，
// 见 targetDisplays）。已有窗口在场时只唤起不重建。
func (a *App) ShowBreakWindow(kind string) {
	if serverMode {
		log.Printf("[win] server 模式无原生窗口，跳过创建 break 窗口（会话已就绪）")
		return
	}
	a.mu.Lock()
	if len(a.breakWins) > 0 {
		ws := a.breakWins
		a.mu.Unlock()
		for _, w := range ws {
			w.Show()
			w.Focus()
		}
		return
	}
	a.mu.Unlock()

	// 把「督休专员-启用」状态经 URL query 下发，让提醒窗口首帧就定好布局，
	// 避免先显示无立绘窗口、立绘加载完再切换的闪烁（frontend 据此决定整窗先
	// 透明、等立绘就绪再淡入；无立绘时立即显示正确态）。
	supFlag := "0"
	if a.cfgSnapshot().SupervisorCfg().Enabled {
		supFlag = "1"
	}

	ap := a.cfgSnapshot().AppearanceCfg()
	var wins []*application.WebviewWindow
	for _, display := range a.targetDisplays() {
		x, y, w, h := breakBoundsFor(display.WorkArea, ap)
		win := a.app.Window.NewWithOptions(application.WebviewWindowOptions{
			Title:         "休息一下",
			X:             x,
			Y:             y,
			Width:         w,
			Height:        h,
			Frameless:     true,
			AlwaysOnTop:   ap.AlwaysOnTop,
			DisableResize: true,
			// 不设它 X/Y 会被无视：Wails 的 InitialPosition 零值 = 整窗居中
			InitialPosition:  application.WindowXY,
			URL:              "/break.html?win=break&sup=" + supFlag,
			BackgroundType:   application.BackgroundTypeTranslucent,
			BackgroundColour: application.NewRGBA(0, 0, 0, 0),
			// 窗口身份经 URL 的 ?win=break 下发；JS 字段对 URL 页面不生效（仅占位）
			JS: `window.__WR_WIN = "break";`,
			// 无边框窗口在 Windows 上会留一圈原生装饰（阴影/描边），与 上一版一致地关掉
			Windows: application.WindowsWindow{
				DisableFramelessWindowDecorations: true,
				// 与 上一版的 skipTaskbar 同义：提醒 / 预览窗口不进任务栏
				HiddenOnTaskbar: true,
			},
		})
		// 窗口被系统 / 用户直接关掉（Alt+F4）时清掉引用：跨 tick 拿着已销毁的窗口
		// 访问会抛错，且会让「已有窗口在场」的判断永久为真
		win.OnWindowEvent(events.Common.WindowClosing, func(*application.WindowEvent) {
			a.removeBreakWindow(win)
		})
		wins = append(wins, win)
	}

	a.mu.Lock()
	a.breakWins = wins
	a.mu.Unlock()
}

// removeBreakWindow 单个休息窗口被直接关掉（Alt+F4）时从切片摘除；
// 全部摘光时清掉会话（与「最后一个窗口被关闭」语义对齐）。
func (a *App) removeBreakWindow(w *application.WebviewWindow) {
	a.mu.Lock()
	ws := a.breakWins
	for i, x := range ws {
		if x == w {
			a.breakWins = append(ws[:i], ws[i+1:]...)
			break
		}
	}
	gone := len(a.breakWins) == 0
	if gone {
		a.sess = nil
	}
	a.mu.Unlock()
}

// settingsWindow 取设置窗口（按名字查，避免跨 tick 持有已销毁的窗口引用 ——
// 跨 tick 拿着旧窗口再访问会抛 "object has been destroyed"）。
// 返回接口类型 application.Window：Wails 的窗口管理器按名字交付的就是接口。
func (a *App) settingsWindow() application.Window {
	w, ok := a.app.Window.GetByName("settings")
	if !ok {
		return nil
	}
	return w
}

// ShowSettingsWindow 打开（或唤起）设置窗口。窗口在 main.go 里随应用创建，
// 这里只负责显示与聚焦——托盘、单实例二次启动、自更新重启恢复都走它。
func (a *App) ShowSettingsWindow() {
	if w := a.settingsWindow(); w != nil {
		w.Show()
		w.Focus()
	}
}

// SetActiveTab 前端每次切页时上报当前 tab 名（general/timing/supervisor/stats/
// shortcuts/about），用于自更新重启后恢复「更新前所在的页面」。
func (a *App) SetActiveTab(tab string) {
	if tab == "" {
		return
	}
	a.mu.Lock()
	a.activeTab = tab
	a.mu.Unlock()
}

// CloseWindow 渲染层「关窗」入口（休息窗口的预览点任意处 / 设置窗口的关闭按钮）。
// 休息窗口是多窗口（每块目标屏一个，存 a.breakWins），点任意一个预览窗整组关掉；
// 外卖窗口同理走 closeTakeoutWindows。
func (a *App) CloseWindow(win string) {
	if win == "takeout" {
		a.closeTakeoutWindows()
		return
	}
	if win == "break" {
		a.mu.Lock()
		ws := a.breakWins
		a.breakWins = nil
		a.sess = nil
		a.mu.Unlock()
		for _, w := range ws {
			w.Close()
		}
		return
	}
	a.mu.Lock()
	w := a.wins[win]
	delete(a.wins, win)
	a.mu.Unlock()
	if w != nil {
		w.Close()
	}
}

// ---------- 绑定给前端的接口（对应 历史源码/preload.ts 的 window.api） ----------

// BreakCurrent 当前休息会话（渲染层首帧拉取；无会话返回 nil）
func (a *App) BreakCurrent(win string) *BreakInfo {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sess
}

// Action 窗口上的动作。win 决定动作归属，name 决定做什么：
//
//	调度器类（设置页侧栏按钮 / 托盘 / 快捷键共用同一批名字）：
//	  breakNow / postpone / skip / pauseToggle / resetCycle / endBreak
//	设置窗口自绘标题栏：winMin / winMaxToggle / winClose
//	休息窗口：endBreak（提前结束）、close（预览点任意处）
//
// 与 上一版一致：这些动作全部汇聚到**调度器**上，而不是各自直接开关窗口——
// 否则 phase / 已消费记账 / 派生视图会对不上。
func (a *App) Action(win, name string) error {
	switch name {
	case "breakNow":
		a.schedBreakNow()
		return nil
	case "postpone":
		a.schedPostpone()
		return nil
	case "skip":
		a.schedSkip()
		return nil
	case "pauseToggle":
		a.schedPauseToggle()
		return nil
	case "resetCycle":
		a.schedResetCycle()
		return nil
	case "endBreak":
		log.Printf("[action] endBreak <- %s", win)
		a.schedEndBreak()
		return nil
	}
	if win != "settings" {
		switch name {
		case "close":
			a.CloseWindow(win)
		default:
			log.Printf("[action] 未实现的动作 %q（<- %s）", name, win)
		}
		return nil
	}
	switch name {
	case "winMin":
		if w := a.settingsWindow(); w != nil {
			w.Minimise()
		}
	case "winMaxToggle":
		if w := a.settingsWindow(); w != nil {
			if w.IsMaximised() {
				w.UnMaximise()
			} else {
				w.Maximise()
			}
		}
		case "winClose":
			// 关闭 = 隐藏到托盘（不是销毁）：托盘「设置…」/ 双击要能重新唤起。
			// Wails 的 Window.Close() 会销毁窗口并从注册表移除，之后 GetByName 拿不到、
			// ShowSettingsWindow 就无法再显示 —— 所以这里用 Hide 保留窗口对象。
			if w := a.settingsWindow(); w != nil {
				w.Hide()
				if a.app != nil && a.app.Event != nil {
					a.app.Event.Emit("settings:hidden", nil)
				}
			}
	default:
		log.Printf("[action] 设置窗口未实现的动作 %q", name)
	}
	return nil
}

// 调度器动作的转发（a.sched 在启动后一定非 nil，这里仍判空以防启动期的调用）
func (a *App) schedBreakNow() {
	if a.sched != nil {
		a.sched.breakNow()
	}
}
func (a *App) schedPostpone() {
	if a.sched != nil {
		a.sched.postpone()
	}
}
func (a *App) schedSkip() {
	if a.sched != nil {
		a.sched.skip()
	}
}

// schedPauseToggle 托盘 / 提示窗 / 快捷键共用的手动「暂停休息 ↔ 恢复休息」。
//
// 自动暂停（应用占用）生效时点「恢复休息」有个特殊语义：不是去摘一个不存在的 manual
// 原因（那会原地 no-op，按钮像坏了），而是**临时忽略目标应用**——立即恢复，且监视器
// 不再因这些应用重新暂停，直到应用全部退出（忽略随退出解除）或用户再次手动暂停
// （忽略作废，回到常规判定）。下次该应用再运行时照常自动暂停。
func (a *App) schedPauseToggle() {
	if a.sched == nil {
		return
	}
	if a.sched.hasPauseReason(pauseReasonAppPause) {
		a.setAppPauseIgnored(true)
		a.sched.resume(pauseReasonAppPause)
		// 若还叠着手动暂停，一并摘掉：用户点的是「恢复休息」，就该恢复到底
		a.sched.resume("manual")
		log.Printf("[appPause] 用户手动恢复：临时忽略目标应用，直到退出或手动暂停")
		return
	}
	if !a.sched.isPaused() {
		// 手动暂停：作废可能残留的忽略会话，回到常规自动暂停判定
		a.setAppPauseIgnored(false)
	}
	a.sched.pauseToggle()
}

// setAppPauseIgnored / isAppPauseIgnored 忽略会话标志（监视 goroutine 与 UI 线程共用）
func (a *App) setAppPauseIgnored(v bool) {
	a.appPauseMu.Lock()
	a.appPauseIgnored = v
	a.appPauseMu.Unlock()
}

func (a *App) isAppPauseIgnored() bool {
	a.appPauseMu.Lock()
	defer a.appPauseMu.Unlock()
	return a.appPauseIgnored
}
func (a *App) schedResetCycle() {
	if a.sched != nil {
		a.sched.resetCycle()
	}
}
func (a *App) schedEndBreak() {
	if a.sched != nil {
		a.sched.endBreak()
	}
}

// SupActivity 页内捕获到键鼠操作 → 广播给其余窗口（多屏同步）。
// 与 上一版同一条铁律：收到广播的窗口不再上报，否则窗口之间回环。
func (a *App) SupActivity(win string) {
	a.app.Event.Emit("sup:active")
}

// PreviewBreak 弹一次预览：顶层「督休专员-启用」开关仍生效（未启用时预览窗直接不显示立绘，
// 与设置页勾选状态一致）；forceKey 指定督休专员（空 = 按配置选），可绕过单个督休专员的启用
// 以预览已停用的督休专员。预览窗口带「预览模式」角标、点任意处关闭，
// 且不影响真实倒计时与统计——与 上一版 break:preview 同语义。
//
// 预览**完全不进调度器**：不消费 occurrence、不改 phase、不占会话（与上一版 一致，
// 那边 break:preview 直接调 showBreakWindows）。
func (a *App) PreviewBreak(kind, forceKey string) string {
	if kind == "" {
		kind = "short"
	}
	return a.showBreakSession(kind, true, forceKey)
}

// StartBreak 立即开始一次指定类型的休息（托盘 / 诊断页）。
// kind 留空 = 用当前计划的那一条（与托盘「立即休息一下」同义）。
//
// 走调度器而不是直接建会话：phase / 已消费记账 / 派生视图都要跟着动，
// 否则到点前又会被 replan 排回来弹第二次。
func (a *App) StartBreak(kind string) string {
	if a.sched == nil {
		return a.showBreakSession(kind, false, "")
	}
	return a.sched.breakNowOf(kind)
}

// GetState 给设置页的状态快照。
//
// 字段集**必须完整**：设置页的 renderStatus() 一上来就读 st.paused /
// st.pauseReasons.length / st.phase，缺一个就在 init 里抛错、整页停摆
// （见 历史源码/types.ts 的 SchedulerState 与 main.ts 的 stateForRenderer）。
func (a *App) GetState() map[string]any {
	st := SchedulerState{
		Phase: "idle", NextType: "short", NextName: shortBreakName,
		Lines: []PlannedBreak{}, Timeline: []TimelineItem{},
		FixedNext: map[string]int64{}, PauseReasons: []string{},
	}
	if a.sched != nil {
		st = a.sched.stateForRenderer()
	}
	out := structToMap(st)
	// Go 侧扩展字段（上一版没有；诊断与控制台用）
	out["engine"] = "go+wails3"
	out["rolesRoot"] = a.rolesRoot
	out["settings"] = orNone(a.cfgSnapshot().Path())
	out["serverMode"] = serverMode
	return out
}

// structToMap 把结构体转成 map（走一次 JSON 往返，字段名与 json tag 完全一致）
func structToMap(v any) map[string]any {
	b, err := json.Marshal(v)
	if err != nil {
		return map[string]any{}
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return map[string]any{}
	}
	return m
}

// AppInfo 关于页的环境信息。
//
// 如实回报引擎为 Wails v3 (Go)，不伪装成 上一版。设置页「软件信息」卡片读取本结构的
// version / goVersion / wailsVersion / buildTime 渲染（见 settings.html 的软件信息卡片）。
func (a *App) AppInfo() map[string]any {
	goVer := strings.TrimPrefix(runtime.Version(), "go")
	return map[string]any{
		"name":         a.cfgSnapshot().AppName(),
		"version":      appVersion,
		"electron":     "Wails v3 (Go)",
		"node":         "-",
		"engine":       "go+wails3",
		"platform":     "windows",
		"roles":        len(scanRoles(a.rolesRoot)),
		"goVersion":    goVer,
		"wailsVersion": "v3",
		"buildTime":    buildTime,
	}
}

// ListRoles 督休专员目录扫描结果（预览图为 data URL，设置页督休专员列表用）
func (a *App) ListRoles() []Role {
	return scanRoles(a.rolesRoot)
}

// QuitApp 退出应用（托盘 / 主窗口按钮）
func (a *App) QuitApp() {
	log.Println("[app] 退出请求")
	a.app.Quit()
}

// WindowMaximized 设置窗口当前是否最大化（自绘标题栏首帧拉一次，之后靠事件推）
func (a *App) WindowMaximized() bool {
	if w := a.settingsWindow(); w != nil {
		return w.IsMaximised()
	}
	return false
}

// appVersion 的唯一来源是 git tag（格式 v主.次.修订）：打包时由
// scripts/make_portable.py 通过 -ldflags "-X main.appVersion=<tag 去掉 v>" 注入。
// 用 var 而非 const：源码里只留 "dev" 占位符（开发 / 无头验证跑出来就是 dev），
// 不在源码里写死真实版本号，避免与 tag 分裂。
var appVersion = "dev"

// buildTime 打包时通过 -ldflags "-X main.buildTime=<RFC3339>" 注入；
// 开发 / 无头验证不注入则留空，前端「构建时间」回退显示「本地构建」。
var buildTime = ""

// pauseReasonText 暂停原因键 → 界面文案：内部用稳定的英文键，展示层不该原样抛给用户；
// 中文原因（'勿扰' / '应用占用'）原样返回，未知键也不会消失（与 types.ts 同口径）。
func pauseReasonText(reason string) string {
	switch reason {
	case "idle":
		return "系统空闲"
	case "manual":
		return "手动暂停"
	default:
		return reason
	}
}

func pauseReasonsText(reasons []string) string {
	if len(reasons) == 0 {
		return "已暂停"
	}
	out := make([]string, 0, len(reasons))
	for _, r := range reasons {
		out = append(out, pauseReasonText(r))
	}
	return strings.Join(out, "、")
}
