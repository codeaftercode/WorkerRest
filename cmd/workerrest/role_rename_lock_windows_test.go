//go:build windows

package main

import (
	"testing"

	"golang.org/x/sys/windows"
)

// lockDir 用“不带 FILE_SHARE_DELETE”的句柄占住一个目录：里面的文件照常可读可写，
// 但目录本身不能被改名 / 删除 —— 这正是杀毒软件、资源管理器预览、别的进程遍历目录时
// 真实存在的情况，也是 RenameRoleDir 唯一会失败的常见原因。
// 返回的函数负责关句柄（可重复调用）。
func lockDir(t *testing.T, dir string) func() {
	t.Helper()
	h, err := windows.CreateFile(
		windows.StringToUTF16Ptr(dir),
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		t.Fatalf("占住目录 %s 失败：%v", dir, err)
	}
	closed := false
	return func() {
		if closed {
			return
		}
		closed = true
		_ = windows.CloseHandle(h)
	}
}

// lockFile 用“不共享”的句柄占住一个文件：连 os.Rename 覆盖它都会失败
// （写 meta.json 的原子替换会卡在这一步），用来验证“写失败 ⇒ 整体失败、零副作用”。
func lockFile(t *testing.T, path string) func() {
	t.Helper()
	h, err := windows.CreateFile(
		windows.StringToUTF16Ptr(path),
		windows.GENERIC_READ,
		0, // 不共享：别的进程连删/改名都不行
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		t.Fatalf("占住文件 %s 失败：%v", path, err)
	}
	closed := false
	return func() {
		if closed {
			return
		}
		closed = true
		_ = windows.CloseHandle(h)
	}
}
