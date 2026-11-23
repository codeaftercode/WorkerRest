//go:build !windows

package main

// 非 Windows 平台不建询问窗，焦点过滤层无事可做。
func installFSAskFocusFilter(uintptr) {}

func uninstallFSAskFocusFilter(uintptr) {}
