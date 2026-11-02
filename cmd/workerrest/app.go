package main

import (
	"context"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"workerrest/internal/idle"
	"workerrest/internal/model"
	"workerrest/internal/role"
	"workerrest/internal/scheduler"
	"workerrest/internal/settings"
	"workerrest/internal/sound"
	"workerrest/internal/stats"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// Go + Wails v3 的后端。前端复用 上一版 renderer 的产物，只通过 shim.js 把
// window.api 映射到这里的绑定方法（见 web/dist/shim.js 与 contract.go）。
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
	// breakSessID breakWins 里的窗口是为**哪个会话**建的（= a.sess.ID 的快照）。
	// 预览每点一次就是一个新会话：会话一变必须重建窗口，不能复用还在场上的旧窗口 ——
	// 旧窗口里的页面渲染的是上一次会话（旧立绘 / 旧倒计时，甚至上一次会话本来
	// 就没有立绘，看起来就像「没显示督休专员」）。
	breakSessID string
	// breakWinSess 窗口 → 建它时的会话 id：窗口被关掉时据此判断它是不是**当前**
	// 会话的窗口。迟到的 WindowClosing（上一次会话的窗）不许清掉刚建好的新会话，
	// 否则新窗口首帧 breakCurrent() 拿到 nil，页面上只剩背景。
	breakWinSess map[*application.WebviewWindow]string
	// breakWinMu 串行化「建休息窗口」这一段：连点两下「预览」会并发建窗，
	// 两批窗户怼在一起时后注册的会覆盖前者、前者变成没人管的野窗。
	breakWinMu sync.Mutex
	// takeoutWinMu 同理串行化「建外卖窗口」：连点两下「预览」会并发建窗，
	// 后一批 a.takeoutWins = wins 会把前一批的引用整批顶掉，前一批就成了
	// 屏幕上关不掉的野窗（休息窗口那边踩过同一个坑）。
	takeoutWinMu sync.Mutex
	// preBreakMu 串行化「建提前提示窗」：调度器的 onPreNotify 与外卖预通知是两个
	// goroutine，可能同时弹同一扇窗（固定名 prebreak）。不串行的话两批窗互相覆盖，
	// 后写入的那扇成为唯一引用，先建的那扇就再也关不掉了。
	preBreakMu sync.Mutex
	sess       *model.BreakInfo
	start      time.Time

	cfg       *settings.Settings // 整份配置（见 settings.go 的说明；保存时会整体替换指针）
	rolesRoot string             // 督休专员目录落点（随包 supervisors/，见 resourcesRoot）
	sched     *scheduler.Scheduler

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
	preTakeoutSlot  model.TimeSlot
	takeoutPreFired map[string]bool // 外卖预通知去重键（按 occurrence，不含提前量）

	// 提示音（见 sound.go）：顺序播放的游标跨播放持续递增（不是每次从头开始）
	soundCursor int

	// 外卖提醒（见 takeout.go）：已触发去重键 + 待推迟提醒 + 当前会话 + 在场窗口。
	// takeoutFired 同时是状态快照的输入（timeline 与 fixedNext.takeout 的口径与上一版 一致）。
	takeoutFired     map[string]bool
	takeoutPostponed *model.TakeoutPostponed
	takeoutCurrent   *model.TakeoutInfo
	takeoutWins      []*application.WebviewWindow
	// takeoutWinSess 窗口 → 建它时的会话 id（与休息窗口的 breakWinSess 同理）：
	// 旧窗口的 WindowClosing 可能迟到，不能让它把刚建好的新会话清掉。
	takeoutWinSess map[*application.WebviewWindow]string
	// takeoutStop / takeoutRaiseStop 外卖每秒检查与抬升循环的停止信号（nil = 未启动）
	takeoutStop      chan struct{}
	takeoutRaiseStop chan struct{}
	// raise 窗口层级簿记（对应 windows/raise.ts）
	raise raiseState
	// supWatch / supWatchStop 系统级键鼠活动轮询（对应 windows/reminderSync.ts）
	supWatch     supWatchInfo
	supWatchStop chan struct{}

	// 系统能力（见 sys_*.go）：空闲状态机 + 三条监视链路的停止信号与「上一跳状态」。
	// idleAway / dndActiveLogged / fsActiveLogged / appPauseHitLogged 只被各自的
	// goroutine 读写，提成字段是为了让单测能直接调 tick 函数；它们的值不影响任何跨链路判定。
	// appPauseIgnored 例外：监视 goroutine 与「手动恢复」的 UI 线程都会碰，走 appPauseMu。
	idle              *idle.IdleState
	sysStop           chan struct{}
	idleAway          bool
	dndActiveLogged   bool
	fsActiveLogged    bool
	appPauseHitLogged bool
	// appPauseIgnored 「指定应用临时忽略」会话：自动暂停后用户手动点「恢复休息」时置位，
	// 监视器不再因目标应用重新暂停；目标应用全部退出或用户手动暂停时解除。
	appPauseIgnored bool
	appPauseMu      sync.Mutex

	// 全局快捷键（见 hotkeys.go）：server 模式为 nil。
	hotkeys *HotkeyManager

	// 使用统计（见 internal/stats）：天记录 + 名字快照 + 防抖落盘 + 每秒工作累加。
	// 懒建（statsStore 首次访问时建好并读盘），启动时由 startStats 起后台 goroutine。
	stats *stats.StatsStore

	// 随包 sounds/ 的启动体检（见 sound_scan.go）：只在新发现 / 有缺失时非空，
	// 用户处理完或点「暂不」就置 nil，本次运行不再询问。用独立锁，不要与 a.mu 交叉。
	soundScanMu sync.Mutex
	soundScan   *SoundScanNotice

	// 设置窗口尺寸记忆（见 window_size.go）：resize 事件只把尺寸塞进 winSizePending
	// 并重置定时器，真正的写盘发生在定时器 goroutine 上（拖拽期间不能做文件 IO）。
	// 用独立小锁而不是 a.mu：定时器回调里要拿 a.mu 落盘，两把锁分开，
	// 不要与主线程上的配置读写互等。
	winSizeMu      sync.Mutex
	winSizePending [2]int
	winSizeTimer   *time.Timer
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
		idle:            idle.NewIdleState(),
		activeTab:       "general",
	}
	a.rolesRoot = a.assetDir("supervisors")
	// 配置落盘目标需要「随包数据目录」来推导便携路径（首次运行还没有配置文件时用）
	settings.SetResourcesRootHint(a.resourcesRoot)
	// 内置提示音随包目录：打包态 <exe>/sounds，开发态 <模块根>/assets/sounds
	sound.SetBuiltinDirFn(func() string { return a.assetDir("sounds") })
	a.cfg = settings.Load(a.resourcesRoot())
	return a
}

// ServiceStartup 由 Wails 在服务注册后调用（替代 上一版的 app.whenReady）
func (a *App) ServiceStartup(_ context.Context, _ application.ServiceOptions) error {
	ap := a.cfg.AppearanceCfg()
	log.Printf("[boot] 启动完成 engine=go+wails3 serverMode=%v", serverMode)
	log.Printf("[boot] 督休专员目录 %s（扫描到 %d 个督休专员）", a.rolesRoot, len(role.ScanRoles(a.rolesRoot)))
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
	// 音频副本改可读名：旧版导入的是 music-<长随机串>.<ext>，统一迁成 music-<序号>.<ext>，
	// 配置里存的绝对路径同步改掉；改过才落盘。必须排在调度器之前 —— 那时还没有并发读配置。
	if n := a.migrateMusicFileNames(); n > 0 {
		if err := a.cfg.Save(); err != nil {
			log.Printf("[sound] 音乐副本改名后落盘失败: %v", err)
		}
	}
	a.startScheduler()
	log.Printf("[plan] %s short=%s long=%s", a.sched.PlanSummary(),
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
	// 随包 sounds/ 体检：用户往里丢了新音频、或删掉了已登记的文件，只记录不擅自改配置
	//（设置页打开时弹窗逐项询问，见 sound_scan.go）
	a.startSoundScan()
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
	// 退出清理要把**所有**提醒窗口都收掉：外卖、休息、右下角提前提示小窗。
	// 少收一类就等于把窗口留给进程销毁时的「兜底」——进程真退出时看不出来，
	// 但只要退出流程被拖慢（WebView2 卸载、自更新替换 exe），那扇窗就会在屏幕上
	// 多停一会儿，用户只能等。一律静默：退出不是「提醒结束」，不该响一声。
	a.closeTakeoutWindowsOpt(true)
	a.closeBreakWindowsOpt(true)
	// 提前提示窗用 TryLock：退出可能发生在主线程，而建窗路径可能在等主线程处理
	// InvokeSync —— 硬等这把锁会让「等主线程」和「主线程等锁」互相卡死（退出挂住）。
	// 抢不到就跳过：进程马上结束，那一扇窗会随进程一起消失。
	if a.preBreakMu.TryLock() {
		a.closePreBreakWindowLocked()
		a.preBreakMu.Unlock()
	}
	a.stopSystemWatchers()
	a.stopTrayRefresh()
	a.destroySoundHost()
	// 尺寸记忆收尾：托盘「退出」时窗口还开着，resize 的防抖定时器可能没到期
	a.mu.Lock()
	rememberSize := a.cfg != nil && a.cfg.WindowSizeCfg().Remember
	a.mu.Unlock()
	if rememberSize {
		a.rememberSettingsWindowSize()
		a.flushSettingsWindowSize()
	}
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
func (a *App) CfgSnapshot() *settings.Settings {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg
}

// startScheduler 启动调度器（App 启动时调一次）
func (a *App) startScheduler() {
	a.sched = scheduler.New(a)
	a.sched.Start()
}

// nowMs 当前时刻（毫秒时间戳，与 JS 的 Date.now() 同基准）
func nowMs() int64 { return time.Now().UnixMilli() }

func max0(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}

// ---------- 路径 ----------

// resourcesRoot 定位随包数据目录（config/ 等）：打包态取 exe 旁，开发态回退到模块根。
// 随包的静态资源（supervisors/、sounds/）另走 assetDir —— 它们在仓库里收在 assets/ 下，
// 在便携包里仍与 exe 平铺。
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

// assetDir 定位一个随包的静态资源目录（supervisors / sounds）。
//
// 两套布局都要认：
//   - 便携包：与 exe 平铺（<exe>/supervisors、<exe>/sounds）—— 这是产品交付形状，
//     用户往 exe 旁的 sounds/ 丢音频就能被体检流程发现，所以不能被仓库布局顶掉；
//   - 开发态：仓库里统一收在 assets/ 下（<模块根>/assets/supervisors）。
//
// 优先平铺：只有当平铺目录不存在时才回退到 assets/。两者都不存在时返回平铺路径，
// 调用方按「空目录」处理（会走「没有可用专员 / 没有内置提示音」的分支）。
func (a *App) assetDir(name string) string {
	root := a.resourcesRoot()
	flat := filepath.Join(root, name)
	if st, err := os.Stat(flat); err == nil && st.IsDir() {
		return flat
	}
	nested := filepath.Join(root, "assets", name)
	if st, err := os.Stat(nested); err == nil && st.IsDir() {
		return nested
	}
	return flat
}

// hasSupervisorsDir 判断某个目录是不是「随包数据根」：便携包看平铺的 supervisors/，
// 仓库里看 assets/supervisors/。用来区分「exe 旁的安装目录」与「构建输出目录」。
func hasSupervisorsDir(dir string) bool {
	for _, p := range []string{
		filepath.Join(dir, "supervisors"),
		filepath.Join(dir, "assets", "supervisors"),
	} {
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			return true
		}
	}
	return false
}

func isDevBuild(exe string) bool {
	return len(exe) > 0 && (strings.Contains(exe, "go-build") || strings.Contains(exe, "Temp"))
}

// ---------- 会话构建 ----------

// buildSupervisorSession 组装下发给渲染层的督休专员信息：
// 与 历史源码/supervisor.ts 的 buildSupervisorSession 同规则——按 selection
// 从督休专员目录里选一个督休专员，随机取一条劝说台词兜底，命令台词从该督休专员 meta.json 抽。
// 关掉开关、没有可用督休专员、立绘读不出来时返回 nil，渲染层退回无督休专员布局。
