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

// procEntry 一次快照里的一行：PID + 系统给出的可执行文件名（原始大小写）
type procEntry struct {
	PID  uint32
	Name string
}

// snapshotProcs 取一次 Toolhelp 进程快照。
//
// 抽出来是因为调用方有两种需求：只要“名字集合”（应用占用判据），和要“PID → 名字”
// （判断前台窗口属于哪个程序）。两次能力共用一次遍历，避免复制粘贴出两份上限/关闭句柄的逻辑。
func snapshotProcs() ([]procEntry, bool) {
	snap, err := windows.CreateToolhelp32Snapshot(th32csSnapProcess, 0)
	if err != nil || snap == windows.InvalidHandle {
		return nil, false
	}
	// 必须配对关闭：按 4 秒一次轮询跑一整天约 2 万次，泄漏句柄会拖垮进程
	defer func() { _ = windows.CloseHandle(snap) }()

	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	if err := windows.Process32First(snap, &pe); err != nil {
		return nil, false
	}
	procs := make([]procEntry, 0, 256)
	for i := 0; i < maxProcs; i++ {
		name := strings.TrimSpace(windows.UTF16ToString(pe.ExeFile[:]))
		// PID 0（Toolhelp 给的名字是 "[System Process]"）与 PID 4（"System"）是内核的
		// 虚拟进程，没有可执行文件。放出去会出现在「从运行的应用选择」列表里，还能被
		// 写进按 exe 名匹配的名单 —— 而它永远不会命中任何前台窗口，等于让用户白加一条
		// （2026-11-21 实测：探针自动勾中的第一项就是它）。
		if name != "" && pe.ProcessID != 0 && pe.ProcessID != 4 {
			procs = append(procs, procEntry{PID: pe.ProcessID, Name: name})
		}
		if err := windows.Process32Next(snap, &pe); err != nil {
			break
		}
	}
	return procs, true
}

// ListProcessNames 枚举当前正在运行的进程可执行文件名。
// 已按大小写去重（保留系统给出的原始大小写），永远返回结果、不抛错。
//
// 只收**看起来是可执行文件名**的项（以 .exe 结尾）：Toolhelp 会把内核里的虚拟进程
// 也列出来（"Secure System" / "Registry" / "Memory Compression"），它们的名字不是
// 可执行文件，写进按 exe 名匹配的名单永远不会命中前台窗口。这些条目在
// ListProcessNamesByPID 里仍然保留 —— 那条链路是判据，宁可多留不可漏。
func ListProcessNames() AppProcessList {
	procs, ok := snapshotProcs()
	if !ok {
		return AppProcessList{Supported: false, Names: []string{}, Detail: "CreateToolhelp32Snapshot 返回空句柄"}
	}
	seen := make(map[string]bool)
	names := make([]string, 0, len(procs))
	for _, p := range procs {
		if !strings.HasSuffix(strings.ToLower(p.Name), ".exe") {
			continue
		}
		key := strings.ToLower(p.Name)
		if !seen[key] {
			seen[key] = true
			names = append(names, p.Name)
		}
	}
	return AppProcessList{
		Supported: true,
		Names:     names,
		Detail:    fmt.Sprintf("进程=%d 去重后=%d", len(procs), len(names)),
	}
}

// ListProcessNamesByPID 枚举 PID → 可执行文件名（**已转小写**，与“全屏时不暂停”名单的
// 匹配口径一致）。快照失败返回空 map，调用方必须按“认不出”处理，不能当成“没有这个进程”。
//
// 用 Toolhelp 而不是 OpenProcess + QueryFullProcessImageName：后者对管理员权限进程会被
// 拒绝（ERROR_ACCESS_DENIED），而全屏的常常正是提权过的程序；Toolhelp 的 szExeFile
// 不要求打开目标进程。
func ListProcessNamesByPID() map[uint32]string {
	procs, ok := snapshotProcs()
	if !ok {
		return map[uint32]string{}
	}
	byPID := make(map[uint32]string, len(procs))
	for _, p := range procs {
		byPID[p.PID] = strings.ToLower(p.Name)
	}
	return byPID
}
