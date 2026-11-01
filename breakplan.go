package main

import (
	"strings"
	"workerrest/internal/jsonx"
	"workerrest/internal/model"
)

// ---------- 常量 ----------

// TAKEOUT_NAME 外卖提醒的展示名。它不在 BreakEntries 里（不是一种「休息」），
// 但窗口与提前提示都要用它，所以单独给一个常量。
const TAKEOUT_NAME = "外卖提醒"

// 内置短/长的展示名（不是用户起的，不进配置）
const (
	shortBreakName = "短休息"
	longBreakName  = "长休息"
)

// weekdays 星期取值与 short 文案（与 types.ts 的 WEEKDAYS 一致，1=周一 … 7=周日）
var weekdays = []struct {
	Value int
	Short string
}{
	{1, "一"}, {2, "二"}, {3, "三"}, {4, "四"}, {5, "五"}, {6, "六"}, {7, "日"},
}

// takeoutMealLabel 按时刻给出餐段文案（外卖窗口的大标题）
func takeoutMealLabel(hour int) string {
	switch {
	case hour >= 5 && hour < 10:
		return "早餐"
	case hour >= 10 && hour < 14:
		return "午餐"
	case hour >= 14 && hour < 17:
		return "下午茶"
	case hour >= 17 && hour < 21:
		return "晚餐"
	default:
		return "夜宵"
	}
}

// preBreakTitle 提前提示弹窗的标题：自定义提醒用「XX时间到」。
// 名称本身就以「时间」结尾时把这俩字去掉，避免「喝水时间时间到」这种叠字。
func preBreakTitle(name string) string {
	base := name
	if strings.HasSuffix(base, "时间") {
		base = base[:len(base)-len("时间")]
	}
	return base + "时间到"
}

// preBreakCountdownTail 自定义提醒提前弹窗里倒计时的后半句：`还有 10 秒显示 XX 提醒`。
// 名称后缀已经是「提醒」时不再补一次。返回值带前导空格 —— 它接在数字后面。
func preBreakCountdownTail(name string) string {
	base := name
	if strings.HasSuffix(base, "提醒") {
		base = base[:len(base)-len("提醒")]
	}
	return " 秒显示" + base + "提醒"
}

// ---------- 配置读出（计划层要用的那几个视图） ----------

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

// DndPause 系统勿扰 / 专注 / 演示模式时暂停
func (s *Settings) DndPause() bool { return s.Flag(true, "dndPause") }

// FullScreenPause 全屏独占应用运行时暂停（QUNS_RUNNING_D3D_FULL_SCREEN）。
// 缺键兜底取「开」，与 default_settings.json 的 fullscreenPause 同值
// （2026-10-29 用户把出厂默认定为「开」，见 settings.go 的 AppearanceCfg 注释）。
func (s *Settings) FullScreenPause() bool { return s.Flag(true, "fullscreenPause") }

// TakeoutLeadSec 外卖「提前通知」秒数（0=到点提醒；提前通知只是先弹一个小窗提示，
// 真正的外卖提醒窗口仍在「原定时刻」弹出，不会因提前通知而提前）
func (s *Settings) TakeoutLeadSec() int { return clampInt(s.Int(0, "takeout", "leadSec"), 0, 300) }

// TakeoutPostponeMin 外卖「可推迟」分钟数（0=不显示「推迟」按钮）
func (s *Settings) TakeoutPostponeMin() int {
	return clampInt(s.Int(10, "takeout", "postponeMin"), 0, 120)
}

// TakeoutPauseAction 暂停休息期间外卖到点的处置：
// remind（照常弹窗）/ postpone（按「可推迟」时长往后推，缺省）/ skip（不弹窗、直接记跳过）。
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

// matchAppPause 纯函数：拿当前运行的进程名（小写）判断是否命中暂停规则。
// 判定逻辑抽出来是为了让 watcher 与测试共用同一份实现，不碰任何状态。
func matchAppPause(cfg model.AppPauseConfig, runningNames []string) (hit bool, matched []string) {
	if !cfg.Enabled || len(cfg.Items) == 0 {
		return false, nil
	}
	running := make(map[string]bool, len(runningNames))
	for _, n := range runningNames {
		running[strings.ToLower(n)] = true
	}
	for _, it := range cfg.Items {
		if it.Enabled && running[strings.ToLower(it.Exe)] {
			matched = append(matched, it.Exe)
		}
	}
	return len(matched) > 0, matched
}

func clampInt(v int64, min, max int) int {
	if v < int64(min) {
		return min
	}
	if v > int64(max) {
		return max
	}
	return int(v)
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
		{ID: "short", Name: shortBreakName, Config: s.Break("short")},
		{ID: "long", Name: longBreakName, Config: s.Break("long")},
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

// BreakNameByID 按身份取展示名；不认识的身份回落成「休息」
func (s *Settings) BreakNameByID(id string) string {
	for _, e := range s.BreakEntries() {
		if e.ID == id {
			return e.Name
		}
	}
	return "休息"
}
