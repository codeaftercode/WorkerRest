package main

import (
	"embed"
	"log"
	"net/http"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// 前端资源：直接复用 Electron 版 renderer 的产物（HTML/CSS/JS 原样，由
// scripts/sync_frontend.py 同步），只外加一层 shim.js 把 window.api 适配到 Go 绑定。
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

	// 主窗口就是设置窗口：路由 /settings.html 是 Electron 版 renderer 的产物副本
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
