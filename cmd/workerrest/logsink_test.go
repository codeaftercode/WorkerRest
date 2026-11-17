package main

// 测试专用的日志接收器：`log.SetOutput` 是全局的，写它的不止当前测试 goroutine ——
// 调度循环（sched.Loop / 统计防抖 / 托盘刷新）都还在后台往同一份日志里写。
// 裸 bytes.Buffer 一边被后台写、一边被测试读（buf.String() / buf.Len()）就是 DATA RACE，
// -race 下必现。这把锁只包住“写”与“读”，不改被测代码的任何行为。

import (
	"bytes"
	"sync"
)

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func (l *lockedBuffer) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Len()
}
