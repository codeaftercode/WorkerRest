package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"workerrest/internal/jsonx"
)

func roleIdentity(name string) string {
	return strings.ToLower(sanitizeRoleDirName(name))
}

// roleNameTaken 判断显示名 name 是否已被**其它**督休专员占用。
//
// excludeName 是「自己」的当前名字（改名时要排除自己 —— 否则用户不改名直接点确定
// 会被自己的旧名字挡住）。
//
// 「重名」的危害不是显示层的小瑕疵，而是 name ↔ 目录 ↔ fixedRole/order 三者失配：
// 目录名靠 uniqueRoleDir 去重成「X (2)」，meta.json 里却还是「X」，于是同一个名字在
// 画廊 / 台词气泡 / 提醒面板标题里指向两个不同的人，删一个还会删错。
func roleNameTaken(root, name, excludeName string) bool {
	n := strings.TrimSpace(name)
	if n == "" {
		return false
	}
	id := roleIdentity(n)
	ex := roleIdentity(excludeName)
	for _, other := range scanRoleNames(root) {
		if excludeName != "" && roleIdentity(other) == ex {
			continue
		}
		if roleIdentity(other) == id {
			return true
		}
	}
	return false
}

// scanRoleNames 只读各 meta.json 的显示名，**不碰立绘 / 预览图**。
//
// 为什么不用 scanRoles：它会给每个督休专员生成缩略图的 data URL（17 个自带专员
// 约 3.5 MB 的图片要读一遍再 base64 编码），而查重名只需要名字。
// uniqueRoleName 还要循环调用查重（"X" → "X (2)" → "X (3)"），
// 走 scanRoles 就是把整套图片反复解码若干遍 —— 改个名字要等好几秒。
func scanRoleNames(root string) []string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() || !validRoleDirName(e.Name()) {
			continue
		}
		dir := filepath.Join(root, e.Name())
		raw, err := os.ReadFile(filepath.Join(dir, roleMetaFile))
		if err != nil {
			continue
		}
		var rawMap map[string]any
		if err := json.Unmarshal(raw, &rawMap); err != nil {
			continue
		}
		name := strings.TrimSpace(jsonx.JsStr(rawMap["name"]))
		if name == "" {
			name = e.Name()
		}
		out = append(out, name)
	}
	return out
}

// uniqueRoleName 给显示名去重：name 已被占用时依次试「name (2)」「name (3)」。
//
// 用于**软件自己起名**的地方（导入时用文件名当默认名）——这类场景不能弹窗问用户，
// 就自动退让一位。用户**主动**改名（SaveRole）不走这里：那条路要拒绝保存、要求重新输入。
//
// excludeName 是「自己」的当前名字：改名时把自己算进去，会把「不改名直接确定」
// 误判成重名。
func uniqueRoleName(root, name, excludeName string) string {
	base := strings.TrimSpace(name)
	if base == "" {
		base = "自定义督休专员"
	}
	// 只扫一次：循环里反复查重会把目录读上好几遍。
	taken := make([]string, 0, 16)
	ex := roleIdentity(excludeName)
	for _, other := range scanRoleNames(root) {
		if excludeName != "" && roleIdentity(other) == ex {
			continue
		}
		taken = append(taken, roleIdentity(other))
	}
	free := func(cand string) bool {
		id := roleIdentity(cand)
		for _, t := range taken {
			if t == id {
				return false
			}
		}
		return true
	}
	if free(base) {
		return base
	}
	for i := 2; ; i++ {
		cand := fmt.Sprintf("%s (%d)", base, i)
		if free(cand) {
			return cand
		}
	}
}

// applyRoleOrder 按用户保存的顺序重排督休专员。
//
// order 是专员名数组（设置页拖动立绘磁贴得到的顺序，落在 config/settings.json 的
// supervisor.order 里）：
//   - order 里出现的名字，按 order 的先后排；
//   - order 里没有的（后来导入的 / 改过名的）保持 scanRoles 给的相对顺序，追加在后面 ——
//     不会因为没写进 order 就消失；
//   - order 里多余的名字（专员已删除或已改名）直接忽略。
//
// order 为空时原样返回，即默认清单顺序 + 其余按目录名排序。
func applyRoleOrder(roles []Role, order []string) []Role {
	if len(order) == 0 || len(roles) == 0 {
		return roles
	}
	byName := make(map[string]Role, len(roles))
	for _, r := range roles {
		byName[roleIdentity(r.Name)] = r
	}
	out := make([]Role, 0, len(roles))
	used := make(map[string]bool, len(roles))
	for _, want := range order {
		id := roleIdentity(want)
		r, ok := byName[id]
		if !ok || used[id] {
			continue
		}
		out = append(out, r)
		used[id] = true
	}
	for _, r := range roles {
		if !used[roleIdentity(r.Name)] {
			out = append(out, r)
		}
	}
	return out
}

// resolveRoleDir 按专员名反查子目录：目录名是 name 的投影（sanitize 后可能不同，
// 比如名字里带 `/`），所以必须读 meta.json 的 name 比对，不能直接拼路径。
// 找不到返回 ("", false)。
func resolveRoleDir(root, name string) (string, bool) {
	want := strings.TrimSpace(name)
	if want == "" {
		return "", false
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		if !e.IsDir() || !validRoleDirName(e.Name()) {
			continue
		}
		dir := filepath.Join(root, e.Name())
		raw, err := os.ReadFile(filepath.Join(dir, roleMetaFile))
		if err != nil {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			continue
		}
		got := strings.TrimSpace(jsonx.JsStr(m["name"]))
		if got == "" {
			// name 缺失：目录名就是身份（与 scanRoles 的口径一致）
			if strings.EqualFold(e.Name(), want) {
				return dir, true
			}
			continue
		}
		if strings.EqualFold(got, want) {
			return dir, true
		}
	}
	return "", false
}

// windowsReservedNames Windows 不允许直接当目录名的设备名（不区分大小写，
// 带扩展名也一样：CON.txt 同样非法）。
