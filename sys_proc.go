package main

import (
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// 系统进程枚举（Windows）—— 历史源码/appProcess.ts 的 Go 版。
//
// 用 kernel32 的 Toolhelp32 API：CreateToolhelp32Snapshot + Process32FirstW/NextW。
// 相比起子进程跑 `tasklist` / `wmic`，这里没有 GBK 编码问题（宽字符直接读），
// 也不用担心 Windows 11 24H2 起 `wmic` 已被移除；而且**不启动任何子进程**，
// 在限制子进程的安全策略下同样能跑（上一版走 koffi 也是同一个理由）。
//
// x/sys/windows 已经把结构体与对齐处理好（上一版要拿 568/564 两个候选 dwSize
// 去试错，Go 侧 `unsafe.Sizeof` 直接就是对的），所以这里没有那段试错逻辑。
//
// 调用失败一律降级为 supported=false，绝不抛错 —— 应用自动暂停只是锦上添花的功能。

// AppProcessList 进程枚举结果（字段名与 appProcess.ts 的 AppProcessList 一致）
type AppProcessList struct {
	Supported bool     `json:"supported"`
	Names     []string `json:"names"`
	Detail    string   `json:"detail"`
}

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
