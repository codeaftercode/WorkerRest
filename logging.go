package main

import (
	"log/slog"
	"os"
)

// appLogger 给 Wails 运行时的日志器。
//
// 默认返回 nil —— Wails 会退化成「丢弃全部调试日志」，正式运行不留噪声。
// 但排查「前端按名字调绑定失败」这类问题时，Debug 级日志是唯一能直接看到
// Wails 真实注册了哪些全限定名（FQN）的地方：
//
//	WR_LOG_LEVEL=debug ./chibingren_server.exe    # 日志里会打出 fqn=main.App.Xxx
//
// 本项目就是靠它确认了 FQN 前缀是 "main.App."（而不是模块名 workerrest）。
func appLogger() *slog.Logger {
	if os.Getenv("WR_LOG_LEVEL") != "debug" {
		return nil
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
}
