package main

import (
	"encoding/json"
	"log"
	"math"
	"os"
	"path/filepath"
	"strings"
)

// 角色目录的写入侧：设置页改一个角色的名字 / 启停 / 尺寸位置 / 台词，或整个删掉角色。
// 与读取侧（role.go）配套，语义对齐 electron/src/supervisor.ts 的
// saveRoleConfig / deleteRoleDir。

// 立绘尺寸与位置的取值范围：与 types.ts 的 SUP_GEOM_LIMITS 一致（也对应设置页滑条）。
// 越界一律夹紧——手改配置或导入的 role.json 都可能把立绘甩出窗口。
var supGeomLimits = map[string][2]float64{
	"sizePct": {20, 100},
	// 距左上限 30%：再往右立绘会挤掉右侧台词的位置
	"posXPct": {0, 30},
	"posYPct": {0, 40},
}

// supGeomDefault 软件默认的立绘尺寸与位置（与 types.ts 的 SUP_GEOM_DEFAULT 同值）
var supGeomDefault = SupGeom{SizePct: 72, PosXPct: 4, PosYPct: 0, Flip: false}

// normalizeSupGeom 夹紧并补全一份立绘几何：缺失或越界时回落默认值。
// flip 只认真正的布尔 true（老配置没有这个字段，一律按「不翻转」）。
func normalizeSupGeom(raw map[string]any) SupGeom {
	pick := func(k string, def float64) float64 {
		lim, ok := supGeomLimits[k]
		if !ok {
			return def
		}
		v, ok := raw[k].(float64)
		if !ok || math.IsNaN(v) || math.IsInf(v, 0) {
			return def
		}
		r := math.Round(v)
		if r < lim[0] {
			return lim[0]
		}
		if r > lim[1] {
			return lim[1]
		}
		return r
	}
	out := SupGeom{
		SizePct: pick("sizePct", supGeomDefault.SizePct),
		PosXPct: pick("posXPct", supGeomDefault.PosXPct),
		PosYPct: pick("posYPct", supGeomDefault.PosYPct),
	}
	if b, ok := raw["flip"].(bool); ok && b {
		out.Flip = true
	}
	return out
}

// readRoleFile 读一个角色的 role.json 并**归一化**（不存在 / 解析失败返回零值 + false）。
//
// 归一化不能省：读出来就是为了改一处再整体写回，若拿原始内容写回，缺字段的角色
// 会被写成「image: ""」，下次扫描再回落是能救回来，但中间任何一次读取者（预览、
// 会话组装）都会看到不合法的配置。与 Electron 的 saveRoleFile → normalizeRoleFile 同路。
func readRoleFile(root, key string) (RoleFile, bool) {
	var cfg RoleFile
	if !validRoleKey(key) {
		return cfg, false
	}
	raw, err := os.ReadFile(filepath.Join(root, key, "role.json"))
	if err != nil {
		return cfg, false
	}
	var rawMap map[string]any
	if err := json.Unmarshal(raw, &rawMap); err != nil {
		return cfg, false
	}
	cfg = normalizeRoleFile(rawMap)
	if cfg.Name == "" {
		cfg.Name = key
	}
	return cfg, true
}

// writeRoleFile 落盘 role.json（先写临时文件再改名，避免写出半截 JSON）
func writeRoleFile(root, key string, cfg RoleFile) error {
	if !validRoleKey(key) {
		return os.ErrInvalid
	}
	dir := filepath.Join(root, key)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	target := filepath.Join(dir, "role.json")
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// replaceRoleImage 替换某个已有角色的立绘：把新图复制成 figure<ext> 与 preview<ext>，
// 并回写 role.json 的 image / preview 字段（名字 / 启停 / 几何 / 台词等读取侧字段原样保留）。
//
// 与 createRoleDir（新建角色）不同：这里角色 key 不变，只换图。旧扩展名不同的立绘 /
// 预览文件一并清理（按 figure.* / preview.* 通配），避免不同扩展名的残留文件占着目录。
// 返回是否成功（key 非法 / 角色不存在 / 源文件不合法或过大 / 写入失败都会返回 false）。
func replaceRoleImage(root, key, src string) bool {
	if !validRoleKey(key) {
		return false
	}
	base, ok := readRoleFile(root, key)
	if !ok {
		log.Printf("[role] 角色不存在或 role.json 读不出来：%s", key)
		return false
	}
	st, err := os.Stat(src)
	if err != nil || !st.Mode().IsRegular() || st.Size() > 20*1024*1024 {
		log.Printf("[role] 替换立绘的源文件不合法或过大：%s", src)
		return false
	}
	ext := strings.ToLower(filepath.Ext(src))
	if ext == "" {
		ext = ".png"
	}
	if _, ok := imageExtMIME[ext]; !ok {
		log.Printf("[role] 不支持的图片格式：%s", ext)
		return false
	}
	b, err := os.ReadFile(src)
	if err != nil {
		log.Printf("[role] 读取源立绘失败 %s: %v", src, err)
		return false
	}
	dir := filepath.Join(root, key)
	// 清掉旧的 figure.* / preview.*（扩展名可能跟新图不同，残留会占着目录）
	for _, pat := range []string{"figure.*", "preview.*"} {
		if matches, _ := filepath.Glob(filepath.Join(dir, pat)); matches != nil {
			for _, m := range matches {
				_ = os.Remove(m)
			}
		}
	}
	fig := "figure" + ext
	prev := "preview" + ext
	if err := os.WriteFile(filepath.Join(dir, fig), b, 0o644); err != nil {
		log.Printf("[role] 写入新立绘失败 %s: %v", key, err)
		return false
	}
	if err := os.WriteFile(filepath.Join(dir, prev), b, 0o644); err != nil {
		log.Printf("[role] 写入新预览图失败 %s: %v", key, err)
		return false
	}
	base.Image = fig
	base.Preview = prev
	if err := writeRoleFile(root, key, base); err != nil {
		log.Printf("[role] 回写 role.json 失败 %s: %v", key, err)
		return false
	}
	log.Printf("[role] 已替换角色 %s 的立绘（%s）", key, fig)
	return true
}

// removeRoleDir 删除整个角色子目录（角色 = 目录，删角色就是删目录）
func removeRoleDir(root, key string) bool {
	if !validRoleKey(key) {
		return false
	}
	if err := os.RemoveAll(filepath.Join(root, key)); err != nil {
		return false
	}
	return true
}

// applyRolePatch 把设置页送来的 patch（name / enabled / geom / messages）叠到既有
// role.json 上：**只改传进来的字段**，立绘与预览图文件名等读取侧字段原样保留。
//
// 角色的名字只做 trim —— 与 supervisor.ts 的 normalizeRoleFile 一致（那里是
// `String(o.name ?? ”).trim() || fallbackName`）。**不要**套用软件名那套折叠空白 /
// 限长 30 的规则：立绘库磁贴的名字允许更长，折叠空白也会把用户起的名字改样。
func applyRolePatch(base RoleFile, patch map[string]any) RoleFile {
	if v, ok := patch["name"].(string); ok {
		if n := strings.TrimSpace(v); n != "" {
			base.Name = n
		}
	}
	if v, ok := patch["enabled"].(bool); ok {
		base.Enabled = v
	}
	if gm, ok := patch["geom"].(map[string]any); ok {
		base.Geom = normalizeSupGeom(gm)
	}
	if mm, ok := patch["messages"].(map[string]any); ok {
		base.Messages = normalizeRoleMessages(mm)
	}
	return base
}
