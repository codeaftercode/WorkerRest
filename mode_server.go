//go:build server

package main

// serverMode：用 -tags server 构建时成立（Wails v3 的无头 HTTP 服务模式，
// 见 wails 的 pkg/application/application_server.go）。此模式下没有原生窗口，
// 创建窗口的调用一律跳过；其余逻辑（角色扫描、会话构建、绑定与 JSON 序列化）
// 完全一致，因此可作为「绑定链路」的无头验证入口。
const serverMode = true
