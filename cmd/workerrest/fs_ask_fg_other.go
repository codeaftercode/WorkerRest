//go:build !windows

package main

// 非 Windows 平台没有询问窗（serverMode 下也不建），守卫自然无事可做。
func foregroundWindow() uintptr { return 0 }

func restoreForeground(uintptr) bool { return false }

func raiseNoActivate(uintptr) bool { return false }

func fsAskOccluded(uintptr) bool { return false }

// 非 Windows：没有 Win32 可问，三个注入点保持“什么都问不到”的语义。
var (
	occlPointHit = func(int, int) uintptr { return 0 }
	occlRootOf   = func(h uintptr) uintptr { return h }
	occlRectOf   = func(uintptr) (wrRect, bool) { return wrRect{}, false }
)
