package settings

import (
	"strings"

	"workerrest/internal/jsonx"
	"workerrest/internal/model"
)

// ---------- 配置读出（计划层要用的那几个视图） ----------
// 原 breakplan.go 里的 Settings 方法，随 settings 域一起下沉。

// 内置短/长的展示名（不是用户起的，不进配置）
const (
	ShortBreakName = "短休息"
	LongBreakName  = "长休息"
)

// LongEveryNShort 每经过 N 次短休息后触发一次长休息
func (s *Settings) LongEveryNShort() int {
	n := s.Int(2, "longEveryNShort")
	if n > 1<<30 || n < -1<<30 {
		return 2
	}
	return int(n)
}

// IdlePauseMin 系统空闲多少分钟后自动暂停（始终启用、不可关闭；最小 1 分钟）
func (s *Settings) IdlePauseMin() int {
	n := s.Int(5, "idlePauseMin")
	if n < 1 {
		return 1
	}
	if n > 1<<30 {
		return 1 << 30
	}
	return int(n)
}

// 两条独立的“系统状态”自动暂停开关（2026-11-06 三次收敛定稿）。以前只有
// 一个 dndPause 管着勿扰 / 忙碌 / 演示设置，关掉一条就得把三条一起关；现在各管各的：
//   - DndPause        请勿打扰（Win11 通知中心“请勿打扰”/ Win10 专注助手档位，WNF）
//   - FullScreenPause 全屏或演示（QUNS 2 / 3 / 4，见下方）
// 出厂全开。presentationPause 已按用户口径删除：微软从未公开 QUNS 2 与 4 谁对应
// 演示设置的判定顺序，两条独立开关在未文档化的实现细节上分家没有依据。
// 软件未发布，不做旧键迁移；磁盘上残留的 presentationPause 键无人读取，无害。

// DndPause 系统勿扰时暂停
func (s *Settings) DndPause() bool { return s.Flag(true, "dndPause") }

// FullScreenPause 全屏应用或 Windows 演示设置生效时暂停（QUNS 2 / 3 / 4）。
// 2 = 非独占全屏或演示设置已应用，3 = D3D 独占全屏，4 = 演示设置主动屏蔽通知。
// 缺键兜底取“开”，与 default_settings.json 的 fullscreenPause 同值
// （2026-10-29 用户把出厂默认定为“开”，见 settings.go 的 AppearanceCfg 注释）。
func (s *Settings) FullScreenPause() bool { return s.Flag(true, "fullscreenPause") }

// 投屏暂停拆成两个独立开关（2026-11-03）：两条判据的误判率差得很远，
// 合在一个开关里只能“要么全信要么全不信”。
//   - 无线投放：几乎不会误判，Miracast 本身就是一种连接协议；
//   - 复制模式：讲台场景准确，但“笔记本接显示器图省事设成复制”会被误判。
// 缺键兜底取“开”，与 default_settings.json 同值 —— 与上面两条同一个口径：
// 自动暂停类开关出厂全开，用户觉得碍事再去关。

// CastWirelessPause 检测到无线显示设备（Miracast / 无线显示适配器）时暂停。
func (s *Settings) CastWirelessPause() bool { return s.Flag(true, "castWirelessPause") }

// CastMirrorPause 检测到投影方式为“复制”（桌面被镜像到另一块屏）时暂停。
// “仅外接屏显示”（Win+P 第三项）不算 —— 单条外接路径无法与“盖子合着用外接显示器办公”
// 区分开，认了会让后者整天不弹提醒。
func (s *Settings) CastMirrorPause() bool { return s.Flag(true, "castMirrorPause") }

// TakeoutLeadSec 外卖“提前通知”秒数（0=到点提醒；提前通知只是先弹一个小窗提示，
// 真正的外卖提醒窗口仍在“原定时刻”弹出，不会因提前通知而提前）
func (s *Settings) TakeoutLeadSec() int { return clampInt(s.Int(0, "takeout", "leadSec"), 0, 300) }

// TakeoutPostponeMin 外卖“可推迟”分钟数（0=不显示“推迟”按钮）
func (s *Settings) TakeoutPostponeMin() int {
	return clampInt(s.Int(10, "takeout", "postponeMin"), 0, 120)
}

// TakeoutPauseAction 暂停休息期间外卖到点的处置：
// remind（照常弹窗）/ postpone（按“可推迟”时长往后推，缺省）/ skip（不弹窗、直接记跳过）。
func (s *Settings) TakeoutPauseAction() string {
	return normPauseAction(s.Str(PauseActionPostpone, "takeout", "pauseAction"))
}

// AppPauseCfg 指定应用运行时自动暂停（enabled 只认真正的 true）
func (s *Settings) AppPauseCfg() model.AppPauseConfig {
	return model.AppPauseConfig{
		Enabled: s.Flag(false, "appPause", "enabled"),
		Items:   s.AppPauseItems(),
	}
}

// AppPauseItems 应用列表（已归一化：非对象项与空 exe 丢弃、按小写去重）
func (s *Settings) AppPauseItems() []model.AppPauseItem {
	seen := map[string]bool{}
	out := []model.AppPauseItem{}
	for _, it := range jsonx.AnySlice(s.lookup("appPause", "items")) {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		exe := exeBasename(mapStr(m, "exe", ""))
		if exe == "" {
			continue
		}
		key := strings.ToLower(exe)
		if seen[key] {
			continue
		}
		seen[key] = true
		name := appPauseLabel(exe)
		if v, ok := m["name"].(string); ok {
			name = normalizeAppName(v)
		}
		out = append(out, model.AppPauseItem{
			ID:      mapStr(m, "id", ""),
			Exe:     exe,
			Name:    name,
			Enabled: mapFlag(m, "enabled", true),
		})
	}
	return out
}

// CustomBreakList 全部自定义提醒（配置里 customBreaks 数组的顺序即设置页卡片顺序）
func (s *Settings) CustomBreakList() []model.CustomBreak {
	raw := jsonx.AnySlice(s.lookup("customBreaks"))
	out := make([]model.CustomBreak, 0, len(raw))
	for _, it := range raw {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, model.CustomBreak{
			ID:     mapStr(m, "id", ""),
			Name:   mapStr(m, "name", ""),
			Config: breakConfigOf(jsonx.AsMap(m["config"])),
		})
	}
	return out
}

// CustomBreakByID 按 id 取一条自定义提醒的原始对象（取不到返回 nil）
func (s *Settings) CustomBreakByID(id string) map[string]any {
	if id == "" {
		return nil
	}
	for _, it := range jsonx.AnySlice(s.lookup("customBreaks")) {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		if mapStr(m, "id", "") == id {
			return m
		}
	}
	return nil
}

// BreakEntries 全部休息的清单（内置短/长 + 自定义各条，顺序固定）：计划与调度按它遍历。
// 内置两条永远在列，自定义提醒被删掉后自然消失。
func (s *Settings) BreakEntries() []model.BreakEntry {
	out := []model.BreakEntry{
		{ID: "short", Name: ShortBreakName, Config: s.Break("short")},
		{ID: "long", Name: LongBreakName, Config: s.Break("long")},
	}
	for _, c := range s.CustomBreakList() {
		out = append(out, model.BreakEntry{ID: c.ID, Name: c.Name, Config: c.Config})
	}
	return out
}

// BreakByID 按身份取配置；这条休息已不存在（设置页删掉了自定义提醒）时返回 false，
// 调用方各自兜底（与 types.ts 的 breakConfigById 返回 null 同义）
func (s *Settings) BreakByID(id string) (model.BreakConfig, bool) {
	for _, e := range s.BreakEntries() {
		if e.ID == id {
			return e.Config, true
		}
	}
	return model.BreakConfig{}, false
}

// BreakNameByID 按身份取展示名；不认识的身份回落成“休息”
func (s *Settings) BreakNameByID(id string) string {
	for _, e := range s.BreakEntries() {
		if e.ID == id {
			return e.Name
		}
	}
	return "休息"
}
