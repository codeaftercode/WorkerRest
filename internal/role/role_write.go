package role

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"strings"

	"workerrest/internal/jsonx"
	"workerrest/internal/model"
)

// 督休专员目录的写入侧：设置页改一个督休专员的名字 / 启停 / 尺寸位置 / 台词，或整个删掉督休专员。
// 与读取侧（role.go）配套，语义对齐 历史源码/supervisor.ts 的
// saveRoleConfig / deleteRoleDir。
//
// 身份是专员名（没有内部 id），所以“写入”有三种情形，别搞混：
//   - WriteRoleFile：按名字写，找不到就建目录（导入 / 首次落盘）；
//   - WriteRoleFileAt：往**已知目录**里写（改名时必须走这条 —— 改名后按新名反查
//     是找不到旧目录的，走 WriteRoleFile 会新建一个空壳，把原来的立绘丢下）；
//   - RenameRoleDir：把目录名跟着新名字改（失败不回滚 meta.json：名字是权威，
//     目录名只是投影，改不动只影响好看程度，不影响功能）。

// 立绘尺寸与位置的取值范围：与 types.ts 的 SUP_GEOM_LIMITS 一致（也对应设置页滑条）。
// 越界一律夹紧——手改配置或导入的 meta.json 都可能把立绘甩出窗口。
var supGeomLimits = map[string][2]float64{
	"sizePct": {20, 100},
	// 距左上限 30%：再往右立绘会挤掉右侧台词的位置
	"posXPct": {0, 30},
	"posYPct": {0, 40},
}

// SupGeomDefault 软件默认的立绘尺寸与位置（与 types.ts 的 SUP_GEOM_DEFAULT 同值）。
// 2026-10-28 用户要求：自带专员（含打包 / 恢复默认 / 导入自定义立绘）统一
// 高度 80%、距窗口左侧 5%、贴底 —— 所以随包 meta.json 的 geom 也一律是这个值。
// ShowName 默认 true：名字牌是这个功能的标识之一，新专员一律先给出来。
var SupGeomDefault = model.SupGeom{SizePct: 80, PosXPct: 5, PosYPct: 0, Flip: false, ShowName: true}

// NormalizeSupGeom 夹紧并补全一份立绘几何：缺失或越界时回落默认值。
// flip 只认真正的布尔 true（缺这个字段时一律按“不翻转”）。
// showName 相反：缺字段按 **显示**（true）—— 它默认开，只有明确写了 false 才关，
// 否则升级后所有老专员的名字牌会凭空消失。
func NormalizeSupGeom(raw map[string]any) model.SupGeom {
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
	out := model.SupGeom{
		SizePct:  pick("sizePct", SupGeomDefault.SizePct),
		PosXPct:  pick("posXPct", SupGeomDefault.PosXPct),
		PosYPct:  pick("posYPct", SupGeomDefault.PosYPct),
		ShowName: true, // 缺字段 = 显示（见上面的说明），下面只有明确 false 才改
	}
	if b, ok := raw["flip"].(bool); ok && b {
		out.Flip = true
	}
	if b, ok := raw["showName"].(bool); ok {
		out.ShowName = b
	}
	return out
}

// ReadRoleFile 读一个督休专员的 meta.json 并**归一化**（不存在 / 解析失败返回零值 + false）。
//
// 归一化不能省：读出来就是为了改一处再整体写回，若拿原始内容写回，缺字段的督休专员
// 会被写成“image: ""”，下次扫描再回落是能救回来，但中间任何一次读取者（预览、
// 会话组装）都会看到不合法的配置。与 上一版的 saveRoleFile → normalizeRoleFile 同路。
func ReadRoleFile(root, name string) (RoleFile, bool) {
	var cfg RoleFile
	dir, ok := ResolveRoleDir(root, name)
	if !ok {
		return cfg, false
	}
	raw, err := os.ReadFile(filepath.Join(dir, RoleMetaFile))
	if err != nil {
		return cfg, false
	}
	var rawMap map[string]any
	if err := json.Unmarshal(raw, &rawMap); err != nil {
		return cfg, false
	}
	cfg = NormalizeRoleFile(rawMap)
	if cfg.Name == "" {
		cfg.Name = strings.TrimSpace(name)
	}
	return cfg, true
}

// WriteRoleFileAt 往**已知目录**里写 meta.json（先写临时文件再改名，避免写出半截 JSON）。
func WriteRoleFileAt(dir string, cfg RoleFile) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	target := filepath.Join(dir, RoleMetaFile)
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

// WriteRoleFile 按名字写一个督休专员：已存在就覆盖那份，不存在就按名字建目录再写。
//
// 改名**不能**走这里（见文件头的说明）：改名后按新名反查不到旧目录，会建出一个空壳，
// 原来的立绘被留在旧目录里变成孤儿。
func WriteRoleFile(root, name string, cfg RoleFile) error {
	roleName := strings.TrimSpace(name)
	if roleName == "" {
		roleName = strings.TrimSpace(cfg.Name)
	}
	if roleName == "" {
		return os.ErrInvalid
	}
	cfg.Name = strings.TrimSpace(cfg.Name)
	if cfg.Name == "" {
		cfg.Name = roleName
	}
	dir, ok := ResolveRoleDir(root, roleName)
	if !ok {
		dir = UniqueRoleDir(root, SanitizeRoleDirName(cfg.Name), "")
	}
	return WriteRoleFileAt(dir, cfg)
}

// RenameRoleDir 把督休专员目录改名成新名字的投影（去重后）。返回改后的目录路径。
//
// 目录名与名字不一致是允许的（名字里带 `/` 之类），所以这只是一个“让目录看起来对”
// 的动作：失败只记日志，不算保存失败 —— meta.json 已经写好，按名字反查照常工作。
func RenameRoleDir(root, dir, newName string) string {
	base := SanitizeRoleDirName(newName)
	cur := filepath.Base(dir)
	if cur == base {
		return dir
	}
	// 只差大小写（doctor → Doctor）：Windows 路径不区分大小写，照常走 UniqueRoleDir
	// 会把这个目录**自己**当成“已被占用”，退让成 "Doctor (2)" —— 目录名跟名字更对不上了。
	// 这里直接改成目标大小写；改不动（句柄占用）就维持原名，反查本来也不看目录名。
	if strings.EqualFold(cur, base) {
		target := filepath.Join(root, base)
		if err := os.Rename(dir, target); err != nil {
			log.Printf("[role] 调整督休专员目录大小写失败 %s→%s: %v", dir, target, err)
			return dir
		}
		return target
	}
	target := UniqueRoleDir(root, base, dir)
	if err := os.Rename(dir, target); err != nil {
		log.Printf("[role] 重命名督休专员目录失败 %s→%s: %v", dir, target, err)
		return dir
	}
	return target
}

// UniqueRoleDir 在 root 下找一个不与现有目录冲突的名称化目录（返回完整路径）：
// base 已存在（且不是 exclude）就加 (2)/(3) 后缀去重。exclude 是当前督休专员正在用的目录，
// 改名时跳过自己，避免“同名 → 自指”误判成已占用。
//
// ⚠️ 这个循环必须有出口。原来只在 os.Stat 返回“不存在”时才收敛，可一旦候选路径本身
// 是**非法文件名**（名字里混了控制字符）、没权限、或路径过长，Stat 返回的是第三种错误：
// 既不是成功也不是 IsNotExist —— 于是 i 一路自增、路径越来越长，改名**永久卡死**
// （设置窗口整窗无响应，用户只能杀进程）。现在遇到这种错误直接把路径交出去，
// 让写入层正常失败并回到对话框报错。
func UniqueRoleDir(root, base, exclude string) string {
	base = SanitizeRoleDirName(base)
	cand := base
	for i := 2; i <= 1000; i++ {
		p := filepath.Join(root, cand)
		// 判“自己”要大小写不敏感：Windows 下 Doctor 与 doctor 是同一个目录
		if p == exclude || (exclude != "" && strings.EqualFold(p, exclude)) {
			return p
		}
		_, err := os.Stat(p)
		if err == nil {
			cand = fmt.Sprintf("%s (%d)", base, i)
			continue
		}
		if os.IsNotExist(err) {
			return p
		}
		log.Printf("[role] 无法判断目录是否可用 %s: %v", p, err)
		return p
	}
	// 1000 个同名前缀的专员：现实中不存在，兜底返回基础名，别把调用方留在死循环里
	return filepath.Join(root, base)
}

// ReplaceRoleImage 替换某个已有督休专员的立绘：把新图复制成 figure<ext>，并回写 meta.json
// 的 image 字段、把 preview 置空（名字 / 启停 / 几何 / 台词等读取侧字段原样保留）。
//
// 与 CreateRoleDir（新建督休专员）不同：这里身份（名字）不变，只换图。旧预览图**全部删除**，
// 由渲染层随后重新生成（settings.js → SaveRolePreview）；旧扩展名不同的立绘也一并清理。
// 返回是否成功（名字非法 / 督休专员不存在 / 源文件不合法或过大 / 写入失败都会返回 false）。
func ReplaceRoleImage(root, name, src string) bool {
	if strings.TrimSpace(name) == "" {
		return false
	}
	dir, ok := ResolveRoleDir(root, name)
	if !ok {
		log.Printf("[role] 督休专员不存在或 meta.json 读不出来：%s", name)
		return false
	}
	base, ok := ReadRoleFile(root, name)
	if !ok {
		log.Printf("[role] 督休专员不存在或 meta.json 读不出来：%s", name)
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
	if _, ok := ImageExtMIME[ext]; !ok {
		log.Printf("[role] 不支持的图片格式：%s", ext)
		return false
	}
	b, err := os.ReadFile(src)
	if err != nil {
		log.Printf("[role] 读取源立绘失败 %s: %v", src, err)
		return false
	}
	// 顺序很重要：**先写新的、再删旧的**。
	// 反过来（先删 figure.* / preview.* 再写）一旦写入失败，这个督休专员就既没有
	// 立绘也没有预览图了 —— FigurePath 为空 → 永远出不了场（buildSupervisorSessionWith
	// 过滤 FigurePath == ""），而 meta.json 里还指着已经不存在的旧文件名。
	fig := "figure" + ext
	if err := jsonx.WriteFileAtomic(filepath.Join(dir, fig), b); err != nil {
		log.Printf("[role] 写入新立绘失败 %s: %v", name, err)
		return false
	}
	// 旧预览图**全部删掉**（而不是覆盖）：新立绘的缩略图由渲染层事后重新生成
	// （settings.js → SaveRolePreview）。留着旧副本，磁贴上就会显示上一张图 ——
	// 那正是“preview 直接复制原图”时代的老毛病。
	// 先写新立绘、再删旧文件：中途失败也不会落到“既没有立绘也没有预览图”。
	for _, pat := range []string{"figure.*", "preview.*"} {
		if matches, _ := filepath.Glob(filepath.Join(dir, pat)); matches != nil {
			for _, m := range matches {
				if filepath.Base(m) == fig {
					continue
				}
				_ = os.Remove(m)
			}
		}
	}
	base.Image = fig
	// 预览图待生成：置空而不是指向一个已删掉的文件，读侧靠 RoleAsset 兜底
	base.Preview = ""
	if err := WriteRoleFileAt(dir, base); err != nil {
		log.Printf("[role] 回写 meta.json 失败 %s: %v", name, err)
		return false
	}
	log.Printf("[role] 已替换督休专员 %s 的立绘（%s）", name, fig)
	return true
}

// RemoveRoleDir 删除整个督休专员子目录（督休专员 = 目录，删督休专员就是删目录）。
// 目录名是名字的投影、与身份不完全一一对应，必须按名字反查到真实目录再删。
func RemoveRoleDir(root, name string) bool {
	dir, ok := ResolveRoleDir(root, name)
	if !ok {
		return false
	}
	if err := os.RemoveAll(dir); err != nil {
		return false
	}
	return true
}

// ApplyRolePatch 把设置页送来的 patch（name / enabled / geom / messages）叠到既有
// meta.json 上：**只改传进来的字段**，立绘与预览图文件名等读取侧字段原样保留。
//
// 督休专员的名字只做 trim —— 与 supervisor.ts 的 normalizeRoleFile 一致（那里是
// `String(o.name ?? "").trim() || fallbackName`）。**不要**套用软件名那套折叠空白 /
// 限长 30 的规则：立绘库磁贴的名字允许更长，折叠空白也会把用户起的名字改样。
func ApplyRolePatch(base RoleFile, patch map[string]any) RoleFile {
	if v, ok := patch["name"].(string); ok {
		if n := strings.TrimSpace(v); n != "" {
			base.Name = n
		}
	}
	if v, ok := patch["enabled"].(bool); ok {
		base.Enabled = v
	}
	if gm, ok := patch["geom"].(map[string]any); ok {
		base.Geom = NormalizeSupGeom(gm)
	}
	if mm, ok := patch["messages"].(map[string]any); ok {
		base.Messages = NormalizeRoleMessages(mm)
	}
	return base
}
