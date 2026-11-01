package winproc

// 系统进程枚举 —— 历史源码/appProcess.ts 的 Go 版。
//
// Windows 用 kernel32 的 Toolhelp32 API：CreateToolhelp32Snapshot + Process32FirstW/NextW。
// 相比起子进程跑 `tasklist` / `wmic`，这里没有 GBK 编码问题（宽字符直接读），
// 也不用担心 Windows 11 24H2 起 `wmic` 已被移除；而且**不启动任何子进程**，
// 在限制子进程的安全策略下同样能跑（上一版走 koffi 也是同一个理由）。
//
// 调用失败一律降级为 supported=false，绝不抛错 —— 应用自动暂停只是锦上添花的功能。
//
// 本文件是平台中立部分：结果类型定义。
// 平台相关的枚举实现在 proc_windows.go（//go:build windows）；
// 其它平台返回「不支持」的桩在 proc_other.go（//go:build !windows）。

// AppProcessList 进程枚举结果（字段名与 appProcess.ts 的 AppProcessList 一致）
type AppProcessList struct {
	Supported bool     `json:"supported"`
	Names     []string `json:"names"`
	Detail    string   `json:"detail"`
}
