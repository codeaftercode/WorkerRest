package main

import (
	"log"
	"runtime"

	"github.com/harry1453/go-common-file-dialog/cfd"
	"github.com/harry1453/go-common-file-dialog/cfdutil"
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

// runDialog 在锁定的 OS 线程上执行一次对话框调用。
//
// 为什么要自己起 goroutine + LockOSThread：Wails 的绑定方法跑在它自己的 goroutine 上，
// 直接调用会把 COM 初始化散到任意线程。这里把「初始化 + Show + 取结果 + Release」整段
// 钉死在同一线程，符合 STA 语义，也避免「CoInitialize 的线程和 Show 的线程不一致」导致
// 对话框起不来或结果取不到。
func runDialog(fn func() pickFileResult) (r pickFileResult) {
	done := make(chan pickFileResult, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		done <- fn()
	}()
	return <-done
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
