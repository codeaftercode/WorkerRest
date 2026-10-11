package main

import (
	"embed"
	"log"
	"net/http"

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
	// 音频请求不走默认资源服务（内置提示音在随包 sounds/ 目录、用户导入的音频在
	// config/sounds/，都不在 embed 的前端资源里），需要一条自己的分发中间件，而
	// 自定义音的白名单校验要读配置 —— 所以转交给 backend。
	// 中间件只在 app.Run() 之后被请求触发，那时下面的 backend 早已赋值。
	var backend *App
	app := application.New(application.Options{
		Name:        "chibingren",
		Description: "吃饼人也敢休息？— 休息提醒",
		// 单例运行：第二个实例立即退出（ExitCode），第一个实例收到回调后把
		// 设置窗口唤到前台 —— 与托盘「双击二次启动唤起窗口」同一条入口。
		// 此前没有这层，双开会导致两份调度器抢着弹提醒窗口。
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID: "com.chibingren.workerrest",
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
