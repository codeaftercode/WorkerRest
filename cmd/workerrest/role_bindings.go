package main

import (
	"strings"

	"workerrest/internal/role"
)

// RoleFigureDataURL 返回某个督休专员立绘原图的 data URL（渲染层据此生成缩略图）。
// 找不到立绘（或 meta.json 读不出来）返回空串。
func (a *App) RoleFigureDataURL(name string) string {
	name = strings.TrimSpace(name)
	if name == "" || a.rolesRoot == "" {
		return ""
	}
	return role.FigureDataURL(a.rolesRoot, name)
}

// SaveRolePreview 保存渲染层生成好的预览图（data URL）并回写 meta.json。
//
// 返回是否成功。数据不合法（不是图片 / 过大）或专员不存在都返回 false，
// 渲染层据此静默跳过 —— 预览图只是优化，没有它磁贴会回退到立绘原图。
func (a *App) SaveRolePreview(name, dataURL string) bool {
	name = strings.TrimSpace(name)
	if name == "" || a.rolesRoot == "" {
		return false
	}
	return role.SaveRolePreview(a.rolesRoot, name, dataURL)
}
