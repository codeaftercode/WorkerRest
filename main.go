package main

import (
	"embed"
	"log"
	"net/http"
	"os"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// 前端资源：frontend/dist 由渲染层构建流程产出（HTML/CSS/JS），shim.js 把
// window.api 适配到 Go 绑定，使 上一版 renderer 在不改业务代码的前提下直接复用。
//
//go:embed all:frontend/dist
var assets embed.FS

//go:embed assets/icon.ico
var trayIcon []byte

func main() {
	// 自更新分支：必须在构建 Wails 应用之前处理，避免触发单例互斥。
	//   --self-update <pending>：替换旧 exe 并启动 --updated 新进程（见 update.go）。
	//   --updated <pending>：正常启动，并把 pending 路径交给前端展示更新摘要。
	for i := 0; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--self-update":
			if i+1 < len(os.Args) {
				runSelfUpdate(os.Args[i+1])
				return
			}
		case "--updated":
			if i+1 < len(os.Args) {
				pendingUpdatePath = os.Args[i+1]
			}
		case "--dump-default-settings":
			// 打包脚本用：把内嵌的出厂默认原样写到文件后立刻退出（不建窗口、不进单例）。
			// 「恢复默认设置」用的就是内嵌这份，便携包里的 config/settings.json 必须与它
			// 同源 —— 让 exe 自己吐出来核对，比「假设刚编译过」可靠（--no-build 拿旧 exe
			// 时，包里是新值、恢复默认还是旧值，用户测不出来）。
			if i+1 < len(os.Args) {
				if err := os.WriteFile(os.Args[i+1], defaultSettingsJSON, 0o644); err != nil {
					log.Printf("[defaults] 导出内嵌默认失败: %v", err)
					os.Exit(1)
				}
				os.Exit(0)
			}
		}
	}

	// 诊断用日志落盘：设了 WR_LOG_FILE 就把应用自身的 log 输出（[boot]/[sound]/[plan]…）
	// 追加到该文件。GUI 版 stderr 不可见，排查「提示音不响」这类问题靠它抓现场。
	if lf := os.Getenv("WR_LOG_FILE"); lf != "" {
		if f, err := os.OpenFile(lf, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
			log.SetOutput(f)
		}
	}

	// 音频请求不走默认资源服务（内置提示音在随包 sounds/ 目录、用户导入的音频在
	// config/sounds/，都不在 embed 的前端资源里），需要一条自己的分发中间件，而
	// 自定义音的白名单校验要读配置 —— 所以转交给 backend。
	// 中间件只在 app.Run() 之后被请求触发，那时下面的 backend 早已赋值。
	var backend *App
	app := application.New(application.Options{
		Name:        "chibingren",
		Description: "吃饼人也敢休息？— 休息提醒",
		Windows: application.WindowsOptions{
			// WebView2 默认拦截「无用户手势的有声自动播放」：休息提示音由定时器触发、
			// 在从无点击交互的宿主窗口里 new Audio().play() 会被静默拒绝（.catch 吞掉），
			// 表现为「试听能响、真休息不响」。全局放开 autoplay 策略，对所有窗口生效。
			AdditionalBrowserArgs: []string{"--autoplay-policy=no-user-gesture-required"},
		},
		// 单例运行：第二个实例立即退出（ExitCode），第一个实例收到回调后把
		// 设置窗口唤到前台 —— 与托盘「双击二次启动唤起窗口」同一条入口。
		// 此前没有这层，双开会导致两份调度器抢着弹提醒窗口。
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID: "com.goodgoodlive.workerrest",
			OnSecondInstanceLaunch: func(application.SecondInstanceData) {
				if backend != nil {
					backend.ShowSettingsWindow()
				}
			},
		},
		Assets: application.AssetOptions{
			Handler: application.BundledAssetFileServer(assets),
			Middleware: func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					backend.soundMiddleware(next).ServeHTTP(w, r)
				})
			},
		},
		Logger: appLogger(),
	})

	backend = NewApp(app)
	app.RegisterService(application.NewService(backend))

	// 主窗口就是设置窗口：路由 /settings.html 是 上一版 renderer 的产物副本
	//（980×700 / 最小 860×600 / 无边框，与 windows/settingsWindow.ts 同参数）。
	// 路由 / 另有一个 Go 版诊断页（index.html），用来手点一次提醒、看环境信息。
	win := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             "settings",
		Title:            "吃饼人也敢休息？",
		Width:            980,
		Height:           700,
		MinWidth:         860,
		MinHeight:        600,
		Frameless:        true,
		URL:              "/settings.html?win=settings",
		BackgroundColour: application.NewRGBA(0xf5, 0xf6, 0xfa, 0xff),
		// NonClientRegionSupport 开启 WebView2 原生的非客户区命中测试，让页面里的
		// `-webkit-app-region: drag`（及其标准写法 `app-region: drag`）元素被系统识别为
		// 标题栏：拖动它即可移动窗口、双击放大/还原，无需前端 JS 拖动逻辑。这是 Wails v3
		// 为无边框自定义标题栏提供的机制（见 webview_window_options.go 的注释）。
		Windows: application.WindowsWindow{
			NonClientRegionSupport: true,
		},
		// 窗口身份经 URL query 下发（?win=settings）。⚠️ 别依赖下面的 JS 字段：Wails v3
		// 在 Windows 上只对内嵌 HTML 注入 JS、对 URL 加载的页面不注入，故这里仅作占位。
		JS: `window.__WR_WIN = "settings";`,
		// 注意：这里**不**设 DisableFramelessWindowDecorations —— 设置窗口是普通无边框窗口，
		// 需要 Win11 的 DWM 系统级圆角（与阴影）。那个字段置 true 会把「无边框装饰」一起关掉，
		// 圆角也跟着没了（只对透明自定义形状的休息窗口才需要）。
	})
	// 最大化状态要由主进程推给页面（自绘标题栏的图标不能靠渲染层自己数，见 wireSettingsWindow）
	backend.wireSettingsWindow(win)

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
