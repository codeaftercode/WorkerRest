package main

import (
	"fmt"
	"log"
	"strings"
	"time"
	"workerrest/internal/jsonx"
	"workerrest/internal/model"
)

func (a *App) buildSupervisorSession(force bool, forceKey string) *model.SupStageInfo {
	return a.buildSupervisorSessionWith(a.cfg.SupervisorCfg(), force, forceKey)
}

// buildSupervisorSessionWith 同上，但允许传入一份**未落盘**的督导配置覆盖：
// 设置页调整督休专员选择后立刻要预览，此时配置还在表单里、没保存到磁盘。
//
// 顶层「督休专员-启用」开关**始终**生效：预览窗与真实休息一视同仁——未启用时
// 一律不显示立绘与对话框（设置页点「预览」也应如实反映这个勾选状态）。
// force 现在只用于**绕过单个督休专员的启用**（预览某个已停用的督休专员时仍要能看到效果），
// 不再绕过顶层总开关。
func (a *App) buildSupervisorSessionWith(cfg model.SupervisorConfig, force bool, forceName string) *model.SupStageInfo {
	if !cfg.Enabled {
		return nil
	}
	all := applyRoleOrder(scanRoles(a.rolesRoot), cfg.Order)
	usable := make([]Role, 0, len(all))
	for _, r := range all {
		if r.FigurePath != "" {
			usable = append(usable, r)
		}
	}
	if len(usable) == 0 {
		return nil
	}

	var picked *Role
	pickByName := func(rs []Role, name string) *Role {
		if strings.TrimSpace(name) == "" {
			return nil
		}
		for i := range rs {
			if strings.EqualFold(rs[i].Name, strings.TrimSpace(name)) {
				return &rs[i]
			}
		}
		return nil
	}
	switch {
	case forceName != "":
		// 预览指定的督休专员：无视 enabled（预览要能看到已关闭督休专员的效果）
		for i := range usable {
			if strings.EqualFold(usable[i].Name, strings.TrimSpace(forceName)) {
				picked = &usable[i]
				break
			}
		}
	case cfg.Selection == "random":
		if pool := a.enabledRoles(usable, force); len(pool) > 0 {
			picked = &pool[a.rng.Intn(len(pool))]
		}
	case cfg.Selection == "sequence":
		if pool := a.enabledRoles(usable, force); len(pool) > 0 {
			picked = &pool[a.roleTurn%len(pool)]
			a.roleTurn = (a.roleTurn + 1) % len(pool)
		}
	default: // fixed：取指定督休专员。固定显示只看磁贴点选本身，**不看单条启用**——
		// 复选框（role.enabled）只在随机 / 顺序模式下出现；若这里仍拿 enabled 当门槛，
		// 随包 meta.json 里 enabled=false 的督休专员就会出现「立绘列表看得到、
		// 休息时不显示」的劈叉（设置页总开关是启用的，用户无从排查）
		for i := range usable {
			if strings.EqualFold(usable[i].Name, strings.TrimSpace(cfg.FixedRole)) {
				picked = &usable[i]
				break
			}
		}
		if picked == nil {
			// 指定的督休专员不存在（已删除）：兜底到第一个可用的
			if len(usable) > 0 {
				picked = &usable[0]
			}
		}
	}
	// 兜底（2026-10-25 修）：指定/固定的那个督休专员找不到时（库里改过名、刚被删掉，
	// 或设置页传来的是过期的名字），按「固定项 → 第一个可用」逐级退让。
	// 曾经这里直接 return nil：预览窗于是变成一个既没有立绘、也没有对话框的空壳，
	// 用户只能反复点「预览」撞运气。
	if picked == nil {
		picked = pickByName(usable, cfg.FixedRole)
	}
	if picked == nil {
		picked = &usable[0]
	}

	picked, image := pickRoleWithImage(usable, picked)
	if picked == nil || image == "" {
		// 真的一张立绘都读不出来：退回无督休专员版式（渲染层会立即显示立绘以外的内容）
		return nil
	}
	name := picked.Name
	if name == "" {
		name = "督休专员"
	}
	return &model.SupStageInfo{
		Image:    image,
		RoleName: picked.Name,
		Name:     name,
		Gaze:     cfg.Gaze,
		Geom:     &picked.Geom,
		Lines: model.SupLines{
			// 第一段兜底：提醒内容为空时督休专员不至于哑口（正常由渲染层优先用提醒的提醒内容）
			Urge: pickOne(urgeLines, a.rng),
			// 第二段（命令）：该督休专员 meta.json 的台词池（空池返回空串，渲染层沿用第一段台词）
			Command: pickRoleLine(picked.Messages, picked.Name, a.cursor, a.rng),
		},
	}
}

// pickRoleWithImage 在可用督休专员里挑一个**立绘真能读出来**的：
// 优先 prefer，读不出来（磁盘瞬时抖动 / 文件被占用 / 正被替换成新图）就按顺序找下一个。
//
// 为什么要有它：曾经首选督休专员的 toDataURL 返回空串就直接返回 nil，提醒窗口于是变成
// 一个既没有立绘也没有对话框的空壳；用户只能反复点「预览」撞运气。
func pickRoleWithImage(usable []Role, prefer *Role) (*Role, string) {
	if prefer != nil {
		if img := toDataURL(prefer.FigurePath); img != "" {
			return prefer, img
		}
	}
	for i := range usable {
		if prefer != nil && strings.EqualFold(usable[i].Name, prefer.Name) {
			continue
		}
		if img := toDataURL(usable[i].FigurePath); img != "" {
			return &usable[i], img
		}
	}
	return nil, ""
}

// SupervisorPreview 设置页的立绘预览：返回一次「督休专员会话」而不是弹窗
// （对应 上一版的 supervisor:preview → buildSupervisorSession(settings, true, key)）。
//
// sup 是设置页表单里**当前**的督导配置（可能还没落盘），null 时用磁盘上的配置。
// 与弹窗预览的区别：同样尊重顶层「启用」开关（未启用返回 nil，页面据此隐藏立绘与对话框），
// 但 force=true 仍可绕过单个督休专员的启用（预览某个已停用的督休专员），forceKey 指定要预览的督休专员。
func (a *App) SupervisorPreview(sup map[string]any, name string) *model.SupStageInfo {
	a.mu.Lock()
	defer a.mu.Unlock()
	cfg := a.cfg.SupervisorCfg()
	if sup != nil {
		// 与 上一版的 {...settings, supervisor: sup} 同义：整份替换，缺的字段按默认
		sel := jsonx.StrOf(sup["selection"])
		if sel != "fixed" && sel != "sequence" && sel != "random" {
			sel = "random"
		}
		// 顺序：表单里带了 order 就用表单的（用户刚拖过、还没落盘也能立刻生效），
		// 没带就沿用磁盘上的那份（前端没这个键时的兼容路径）。
		order := cfg.Order
		if _, ok := sup["order"]; ok {
			order = normalizeRoleOrder(sup["order"])
		}
		cfg = model.SupervisorConfig{
			Enabled:   sup["enabled"] != false,
			Gaze:      sup["gaze"] != false,
			Selection: sel,
			FixedRole: jsonx.StrOf(sup["fixedRole"]),
			Order:     order,
		}
	}
	return a.buildSupervisorSessionWith(cfg, true, name)
}

// enabledRoles 过滤掉已禁用督休专员（force=true 预览时不过滤）
func (a *App) enabledRoles(all []Role, force bool) []Role {
	if force {
		return all
	}
	out := make([]Role, 0, len(all))
	for _, r := range all {
		if r.Enabled {
			out = append(out, r)
		}
	}
	return out
}

// pickMessage 选本次要显示的提醒内容（与 上一版 pickMessageOrName 同规则）：
// 固定取 fixedId，顺序轮换，随机等概率；一条都没有时用提醒名称兜底，保证窗口不空。
// 返回的切片至少 1 条——渲染层把它整块渲染，同时把第 0 条当督休专员第一段台词。
//
// kind 可以是 'short' / 'long' / 'takeout'，也可以是自定义提醒的 id（各有一套自己的池）。
func (a *App) pickMessage(kind, fallbackName string) []string {
	rm := a.cfg.RemindersByID(kind)
	if rm.Order == "fixed" {
		for _, it := range rm.Items {
			if it.ID == rm.FixedID && it.Text != "" {
				return []string{it.Text}
			}
		}
	}
	pool := make([]string, 0, len(rm.Items))
	for _, it := range rm.Items {
		if it.Enabled && it.Text != "" {
			pool = append(pool, it.Text)
		}
	}
	if len(pool) == 0 {
		return []string{fallbackName}
	}
	if rm.Order == "sequence" {
		// 顺序游标按「类别」记忆（督休专员台词用同一张表的另一个键，互不干扰）
		key := "msg-" + kind
		cur := a.cursor[key] % len(pool)
		a.cursor[key] = (cur + 1) % len(pool)
		return []string{pool[cur]}
	}
	return []string{pool[a.rng.Intn(len(pool))]}
}

// showBreakSession 构建一次休息会话并弹窗，返回会话 id。
// 真实休息（调度器 / 托盘）与预览共用它，差别只在 preview 标志。
//
// 会话构建与「弹窗」必须分开：渲染层随后会自己再拉一次 breakCurrent，
// 所以这里先把会话写完再放窗（跨多次调用期间保持一致）。
func (a *App) showBreakSession(kind string, preview bool, forceKey string) string {
	a.mu.Lock()
	cfg := a.cfg
	bcfg, ok := cfg.BreakByID(kind)
	if !ok {
		a.mu.Unlock()
		log.Printf("[break] 未知的休息身份 %q（可能刚被删除），忽略", kind)
		return ""
	}
	name := cfg.BreakNameByID(kind)
	dur := bcfg.DurationMs
	if dur <= 0 {
		dur = 20_000
	}
	now := time.Now()
	// 提前结束快捷键与「设置 → 快捷键」的 endEarly 完全一致：休息窗口据此显示提示，
	// 本地 keydown 也按它匹配（全局快捷键已由 hotkeys.go 按同一组合键注册）。
	endEarly := ""
	if sc, ok := a.cfg.Raw()["shortcuts"].(map[string]any); ok {
		if s, ok := sc["endEarly"].(string); ok {
			endEarly = strings.TrimSpace(s)
		}
	}
	info := &model.BreakInfo{
		ID:               fmt.Sprintf("%s-%d", kind, now.UnixMilli()),
		Type:             kind,
		Name:             name,
		EndsAt:           now.Add(time.Duration(dur) * time.Millisecond).UnixMilli(),
		DurationMs:       dur,
		Preview:          preview,
		Appearance:       cfg.AppearanceView(),
		Messages:         a.pickMessage(kind, name),
		Supervisor:       a.buildSupervisorSession(preview, forceKey),
		EndEarlyShortcut: endEarly,
	}
	a.sess = info
	a.mu.Unlock()

	// 系统级键鼠活动轮询：休息窗口在场期间必须能发现「用户在**别的应用**里还在敲键 /
	// 动鼠标」，否则督休专员的二阶段只能靠窗口内的本地 mousemove / keydown 触发 ——
	// 用户报的就是这个：「只在提醒窗口上检测」。
	// 放在会话建好之后、弹窗之前：会话一旦存在就该开始盯全局输入，与窗口是否已就绪无关
	// （无头 server 模式也走这里，冒烟据此断言轮询真的起来了）。
	a.ensureSupActivityWatch()

	// 休息真的开始了：顺手收掉右下角的提前提示窗
	//（对应 上一版 在 break-start 事件里调 closePreBreakWindow）。
	// 预览不算「真的开始」：看一眼预览就把真实提醒的小窗收掉，属于预览污染真实状态。
	if !preview {
		a.closePreBreakWindow()
	}
	a.ShowBreakWindow(kind)
	// 提示音：只给真实休息放（预览是「看一眼窗口长什么样」，不该出声）
	if !preview {
		a.playConfiguredSound()
		// 统计：休息开始 —— 从这里起休息时长逐秒实时入账（见 StatsStore.OnBreakTick），
		// 中途断电最多丢 1 分钟，而不是把整次休息的时长全丢掉
		a.statsStore().OnBreakStart(kind)
	}
	return info.ID
}
