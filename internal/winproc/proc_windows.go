//go:build windows

package winproc

import (
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	th32csSnapProcess = 0x00000002
	// maxProcs 防御性上限：进程数异常时不至于死循环
	maxProcs = 20000
)

// ListProcessNames 枚举当前正在运行的进程可执行文件名。
// 已按大小写去重（保留系统给出的原始大小写），永远返回结果、不抛错。
func ListProcessNames() AppProcessList {
	snap, err := windows.CreateToolhelp32Snapshot(th32csSnapProcess, 0)
	if err != nil || snap == windows.InvalidHandle {
		return AppProcessList{Supported: false, Names: []string{}, Detail: "CreateToolhelp32Snapshot 返回空句柄"}
	}
	// 必须配对关闭：按 4 秒一次轮询跑一整天约 2 万次，泄漏句柄会拖垮进程
	defer func() { _ = windows.CloseHandle(snap) }()

	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	if err := windows.Process32First(snap, &pe); err != nil {
		return AppProcessList{Supported: false, Names: []string{}, Detail: "Process32FirstW 失败"}
	}
	seen := make(map[string]bool)
	names := make([]string, 0, 128)
	for i := 0; i < maxProcs; i++ {
		name := strings.TrimSpace(windows.UTF16ToString(pe.ExeFile[:]))
		if name != "" {
			key := strings.ToLower(name)
			if !seen[key] {
				seen[key] = true
				names = append(names, name)
			}
		}
		if err := windows.Process32Next(snap, &pe); err != nil {
			break
		}
	}
	return AppProcessList{
		Supported: true,
		Names:     names,
		Detail:    fmt.Sprintf("dwSize=%d 进程=%d", unsafe.Sizeof(pe), len(names)),
	}
}
