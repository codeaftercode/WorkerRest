package main

import (
	"log"

	"github.com/harry1453/go-common-file-dialog/cfd"
	"github.com/harry1453/go-common-file-dialog/cfdutil"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// 原生文件对话框：Electron `dialog.showOpenDialog / showSaveDialog` 的对应物。
//
// 为什么用 go-common-file-dialog 而不是别的方案：Wails v3 没有公开的 dialog 包
// （它把 go-common-file-dialog vendored 进 internal，第三方 import 不到），而这个库
// 是公开的 MIT 库、走 COM 接口调 Windows 的 Common File Dialog，**不需要 CGO**，
// 支持打开（单/多选）、保存、标题、文件过滤器、默认文件名——正好是 Electron 那
// 几个对话框用到的全部能力。样式就是 Windows 系统原生对话框，与系统观感一致。
//
// 线程模型：COM 要求 CoInitialize 与 Show 在同一 STA 线程上（库内部在
// NewXxxDialog 时调了 CoInitializeEx(APARTMENTTHREADED)）。Go 的 goroutine 可能在
// OS 线程间迁移，所以每个对话框调用都放进一个 runtime.LockOSThread 的独立 goroutine
// 里跑，保证「初始化 + 展示 + 释放」绑定在同一线程上。

// pickFileResult 一次对话框调用的结果：path 为选中路径（多选时用 paths），
// canceled 表示用户点了取消，err 表示对话框本身失败（与取消是两码事）。
type pickFileResult struct {
	path     string
	paths    []string
	canceled bool
	err      error
}

// showOpenDialog 打开单文件对话框（对应 Electron dialog.showOpenDialog 的 openFile）。
func showOpenDialog(title string, filters []cfd.FileFilter) pickFileResult {
	return runDialog(func() pickFileResult {
		p, err := cfdutil.ShowOpenFileDialog(cfd.DialogConfig{
			Title:       title,
			Role:        roleFor(title),
			FileFilters: filters,
		})
		return resultOf(p, err)
	})
}

// showOpenMultiDialog 打开多文件对话框（对应 Electron 的 openFile + multiSelections）。
func showOpenMultiDialog(title string, filters []cfd.FileFilter) pickFileResult {
	return runDialog(func() pickFileResult {
		ps, err := cfdutil.ShowOpenMultipleFilesDialog(cfd.DialogConfig{
			Title:       title,
			Role:        roleFor(title),
			FileFilters: filters,
		})
		if err != nil {
			return pickFileResult{err: err}
		}
		if len(ps) == 0 {
			return pickFileResult{canceled: true}
		}
		return pickFileResult{paths: ps}
	})
}

// showSaveDialog 另存为对话框（对应 Electron dialog.showSaveDialog）。
func showSaveDialog(title, defaultName string, filters []cfd.FileFilter) pickFileResult {
	return runDialog(func() pickFileResult {
		p, err := cfdutil.ShowSaveFileDialog(cfd.DialogConfig{
			Title:       title,
			Role:        roleFor(title),
			FileFilters: filters,
			FileName:    defaultName,
		})
		return resultOf(p, err)
	})
}

// runDialog 在 Wails 的主线程（STA）上执行一次对话框调用。
//
// 为什么是主线程而不是 goroutine + LockOSThread：Wails v3 的 WebView2 在包 init 时
// 已经 `runtime.LockOSThread()` + `CoInitializeEx(APARTMENTTHREADED)`（见 wails 的
// internal/webview2/pkg/edge/corewebview2.go），主线程就是 STA。IFileOpenDialog 是
// STA 对象，必须在 STA 线程、且带消息泵的线程上调用。
//
// 之前用 goroutine + LockOSThread 另起线程再 CoInitializeEx(STA) 的做法有两个隐患：
//  1. 那个线程没有消息泵，模态对话框的某些 COM 回调（缩略图 / 预览 / 事件）会失败；
//  2. cfd 库的 initialize()「只 CoInitialize 不 CoUninitialize」，反复弹对话框会污染
//     线程的 COM 状态，真机 GUI 下表现为对话框一弹就崩溃退出。
//
// 用 application.InvokeSyncWithResult 把调用派发到主线程并等结果，既在正确的 STA 上
// 执行，又自带 panic 兜底（不会因对话框库 panic 而把整个进程带崩）。
func runDialog(fn func() pickFileResult) (r pickFileResult) {
	return application.InvokeSyncWithResult(fn)
}

// resultOf 把单文件对话框的结果归一化：取消返回 canceled，其余原样。
func resultOf(p string, err error) pickFileResult {
	if err != nil {
		return pickFileResult{err: err}
	}
	if p == "" {
		return pickFileResult{canceled: true}
	}
	return pickFileResult{path: p}
}

// roleFor 由标题派生一个稳定的 role（对话框 GUID 种子，让 Windows 按用途分别记住上次位置）。
// 直接用标题当 role 也合法（role 可以是任意字符串），这里去掉冒号等易出问题的字符即可。
func roleFor(title string) string {
	out := make([]rune, 0, len(title))
	for _, r := range title {
		if r < 0x80 {
			out = append(out, r)
		}
	}
	return string(out)
}

// dialogAvailable 当前环境能不能弹对话框：只有非无头、非 server 模式才有原生窗口。
// 无头冒烟里这些绑定一律走「取消」路径，不真的阻塞在对话框上。
func (a *App) dialogAvailable() bool {
	return !serverMode && a.app != nil
}

func logDialogErr(what string, err error) {
	log.Printf("[dialog] %s 失败: %v", what, err)
}
