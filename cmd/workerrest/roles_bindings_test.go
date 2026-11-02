package main

import (
	"encoding/json"
	"testing"
)

// 空专员目录时的契约（2026-11-01 边界探针发现）：
//
// 便携包被用户删掉 assets/supervisors、或首次运行素材还没落地时，ScanRoles 返回 nil。
// nil 切片经 JSON 序列化成 null，而设置页拿它直接 `for (const r of ROLES)` ——
// 一次性抛 TypeError，整个立绘库白屏，连「添加立绘」按钮都不再响应。
//
// 「没有专员」必须是一种正常状态：绑定层统一返回空数组。
func TestListRolesEmptyDirReturnsEmptySliceNotNil(t *testing.T) {
	a := &App{rolesRoot: t.TempDir()}
	got := a.ListRoles()
	if got == nil {
		t.Fatal("空专员目录时 ListRoles 返回了 nil，JSON 会变成 null，前端 for...of 会抛 TypeError")
	}
	if len(got) != 0 {
		t.Fatalf("空目录应该扫出 0 个专员，得到 %d 个", len(got))
	}
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	if string(b) != "[]" {
		t.Fatalf("空列表必须序列化成 []（前端按数组用），得到 %s", string(b))
	}
}
