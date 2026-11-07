package main

import (
	"errors"
	"log"

	"github.com/go-ole/go-ole"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// 原生文件对话框：上一版 `dialog.showOpenDialog / showSaveDialog` 的对应物。
//
// 用 Wails v3 内置的 app.Dialog（OpenFile / SaveFile），而不是自己 import 公开版
// go-common-file-dialog。原因：公开版 v1.2.0 的 COM 调用用 `syscall.Syscall`，Go 1.23+
// 在 Windows 上参数传递有变化，真机 GUI 下对话框一弹就崩（Wails 官方 vendored 版已改用
// `syscall.SyscallN` 修复，但公开版没跟上）。而 app.Dialog 内部走 vendored 修复版 +
// InvokeSync（主线程 STA），线程模型与 COM 调用都正确，直接用最省事也最可靠。

// hResultCancelled IFileDialog 用户点取消时返回的 HRESULT（HRESULT_FROM_WIN32(ERROR_CANCELLED)）
const hResultCancelled = uintptr(0x800704C7)

// pickFileResult 一次对话框调用的结果：path 为选中路径（多选时用 paths），
// canceled 表示用户点了取消，err 表示对话框本身失败（与取消是两码事）。
type pickFileResult struct {
	path     string
	paths    []string
	canceled bool
	err      error
}

// showOpenDialog 打开单文件对话框（对应 上一版 dialog.showOpenDialog 的 openFile）。
func showOpenDialog(dm *application.DialogManager, title string, filters []application.FileFilter) pickFileResult {
	d := dm.OpenFile().SetTitle(title)
	for _, f := range filters {
		d.AddFilter(f.DisplayName, f.Pattern)
	}
	p, err := d.PromptForSingleSelection()
	return resultOf(p, err)
}

// showOpenMultiDialog 打开多文件对话框（对应 上一版的 openFile + multiSelections）。
func showOpenMultiDialog(dm *application.DialogManager, title string, filters []application.FileFilter) pickFileResult {
	d := dm.OpenFile().SetTitle(title)
	for _, f := range filters {
		d.AddFilter(f.DisplayName, f.Pattern)
	}
	ps, err := d.PromptForMultipleSelection()
	if err != nil {
		return resultOf("", err)
	}
	if len(ps) == 0 {
		return pickFileResult{canceled: true}
	}
	return pickFileResult{paths: ps}
}

// showSaveDialog 另存为对话框（对应 上一版 dialog.showSaveDialog）。
func showSaveDialog(dm *application.DialogManager, title, defaultName string, filters []application.FileFilter) pickFileResult {
	d := dm.SaveFile()
	d.SetOptions(&application.SaveFileDialogOptions{Title: title, Filename: defaultName})
	for _, f := range filters {
		d.AddFilter(f.DisplayName, f.Pattern)
	}
	p, err := d.PromptForSingleSelection()
	return resultOf(p, err)
}

// resultOf 把单文件对话框的结果归一化：取消返回 canceled，其余原样。
func resultOf(p string, err error) pickFileResult {
	if err != nil {
		if isCancelError(err) {
			return pickFileResult{canceled: true}
		}
		return pickFileResult{err: err}
	}
	if p == "" {
		return pickFileResult{canceled: true}
	}
	return pickFileResult{path: p}
}

// isCancelError 判断错误是不是“用户点了取消”——取消不是真错误，调用方据此静默返回。
//
// 两个来源：
//  1. IFileDialog 直接回报 HRESULT ERROR_CANCELLED（0x800704C7，走 *ole.OleError）；
//  2. Wails vendored 的 cfd 库在取消时返回 sentinel error“cancelled by user”
//     （internal 包无法 import，只能按消息匹配）。实际当前主要走第 2 条：vendored 库的
//     hresultToError 把 HRESULT 当 uintptr 比较 `hr < 0`（无符号类型恒为 false），取消的
//     HRESULT 被吞掉，最后在 GetResult 里以 ErrorCancelled 冒出来。
func isCancelError(err error) bool {
	var oe *ole.OleError
	if errors.As(err, &oe) {
		return oe.Code() == hResultCancelled
	}
	return err != nil && err.Error() == "cancelled by user"
}

// dialogAvailable 当前环境能不能弹对话框：只有非无头、非 server 模式才有原生窗口。
// 无头冒烟里这些绑定一律走“取消”路径，不真的阻塞在对话框上。
func (a *App) dialogAvailable() bool {
	return !serverMode && a.app != nil
}

func logDialogErr(what string, err error) {
	log.Printf("[dialog] %s 失败: %v", what, err)
}
