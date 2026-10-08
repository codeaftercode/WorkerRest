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
	// 用户导入的音频（音乐列表的副本）不在 embed 的前端资源里，需要一条自己的只读
	// 路由，而它的白名单校验要读配置 —— 所以中间件转交给 backend。
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
		URL:              "/settings.html",
		BackgroundColour: application.NewRGBA(0xf5, 0xf6, 0xfa, 0xff),
		// 注入窗口身份：shim.js 用它决定调哪个绑定方法、上报哪个窗口名
		JS: `window.__WR_WIN = "settings";`,
		Windows: application.WindowsWindow{
			DisableFramelessWindowDecorations: true,
		},
	})
	// 最大化状态要由主进程推给页面（自绘标题栏的图标不能靠渲染层自己数，见 wireSettingsWindow）
	backend.wireSettingsWindow(win)

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
