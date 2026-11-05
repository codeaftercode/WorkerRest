//go:build ignore

// display_ids 打印当前接入的显示器清单（JSON），给 scripts/probe/probe_display_target.py 用。
//
// 为什么要单独有个 Go 小工具，而不是在 Python 里用 ctypes 再抄一遍：
//   - 稳定性标识来自 DISPLAYCONFIG 设备接口路径，结构体错一个字段就会**静默**取到
//     别的值（internal/windnd 的镜像判据踩过这个坑，双显卡机器上表现为永远判成镜像）。
//   - 探针要是自己维护第二套 Win32 结构体，测出来的东西和产品实际用的就不是一回事了。
//
// 跑法（不能 `go build ./...` 收进来，故挂 ignore 标签）：
//
//	go run scripts/probe/display_ids.go
//
// 输出：{"ok":true,"displays":[{"id":"DEL41A9#...","label":"DELL U2719DS",...}]}
// 枚举不可用时 {"ok":false,"error":"..."} 且退出码 3（= 无法测量，与「失败」区分）。
package main

import (
	"encoding/json"
	"os"

	"workerrest/internal/display"
)

func main() {
	enc := json.NewEncoder(os.Stdout)
	list, err := display.List()
	if err != nil {
		enc.Encode(map[string]any{"ok": false, "error": err.Error()})
		os.Exit(3)
	}
	enc.Encode(map[string]any{"ok": true, "displays": list})
}
