package settings

import (
	"strings"
	"time"

	"workerrest/internal/jsonx"
	"workerrest/internal/model"
	"workerrest/internal/winproc"
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

// IdlePause 系统空闲是否参与自动暂停（2026-11-07 由“始终开启、不可关闭”改成可关闭）。
// 关掉之后**整条空闲检测都不做**：既不因空闲暂停，也不把这段时间记进统计的“离开”
// （离开时长本来就是“连续无输入达到阈值的那一段”，不检测就没有这一段）。
// 缺键兜底取“开”，与 default_settings.json 同值 —— 自动暂停类开关出厂全开。
func (s *Settings) IdlePause() bool { return s.Flag(true, "idlePause") }

// IdlePauseMin 系统空闲多少分钟后自动暂停（最小 1 分钟）。开不开这条由 IdlePause 管，
// 这里只管“几分钟”。
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

// 全屏自动暂停分两档（2026-11-20 定稿，替代 2026-11-06 的“2/3/4 合并一条开关”）：
//
//   - FullScreenPause 只管 QUNS 3 / 4：D3D 独占全屏、Windows 演示设置。两者都有明确
//     成因、几乎不误报，而且**都不能弹窗**（独占画面 DWM 不合成第三方窗口；投影上多一扇
//     窗就是事故），所以永远“直接暂停、不询问”。出厂开，与旧行为完全一致。
//   - FullScreenBusyMode 只管 QUNS 2：非独占全屏（浏览器 F11、看图、视频全屏、PPT 放映、
//     远程桌面、无边框游戏）。官方定义就是垃圾桶（“全屏应用在跑**或**演示设置已应用”），
//     误报与漏报全部集中在这一档，出厂 off（不处理）。可选 pause（旧的直接暂停）与
//     ask（实验性：弹小窗问一次并记住答案）。
//
// 两者互相独立：取消勾选 FullScreenPause 不影响下拉，反之亦然。
// 软件未发布，不做旧键迁移；磁盘上残留的 presentationPause 键无人读取，无害。

// 非独占全屏（QUNS=2）的三种处置
const (
	// FullScreenBusyModeOff 不处理：全屏时不暂停、不询问、不打扰（出厂默认）
	FullScreenBusyModeOff = "off"
	// FullScreenBusyModePause 直接暂停（2026-11-20 之前的行为）
	FullScreenBusyModePause = "pause"
	// FullScreenBusyModeAsk 实验性：弹小窗询问是否暂停，并按答复记住这个应用
	FullScreenBusyModeAsk = "ask"
)

// DndPause 系统勿扰时暂停
func (s *Settings) DndPause() bool { return s.Flag(true, "dndPause") }

// FullScreenPause 独占全屏（QUNS=3）与 Windows 演示设置（QUNS=4）生效时暂停。
// 缺键兜底取“开”，与 default_settings.json 的 fullscreenPause 同值
// （2026-10-29 用户把出厂默认定为“开”，见 settings.go 的 AppearanceCfg 注释）。
func (s *Settings) FullScreenPause() bool { return s.Flag(true, "fullscreenPause") }

// FullScreenBusyMode 非独占全屏（QUNS=2）的处置：off（默认）/ pause / ask。
// 缺键与非法值一律回 off —— 这一档本来就误报最多，“什么都不做”是唯一不会误伤的选择。
func (s *Settings) FullScreenBusyMode() string {
	return NormFullScreenBusyMode(s.Str(FullScreenBusyModeOff, "fullscreenBusyMode"))
}

// NormFullScreenBusyMode 把任意输入收敛成三个合法值之一（未知 → off）
func NormFullScreenBusyMode(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case FullScreenBusyModePause:
		return FullScreenBusyModePause
	case FullScreenBusyModeAsk:
		return FullScreenBusyModeAsk
	default:
		return FullScreenBusyModeOff
	}
}

// FullScreenSkipItems “全屏时不暂停”名单（按当前时间过滤掉过期的 today）
func (s *Settings) FullScreenSkipItems() []model.FullScreenSkipItem {
	return s.FullScreenSkipItemsAt(time.Now().UnixMilli())
}

// FullScreenSkipItemsAt 同上，时间由调用方给（单测用固定时间）
func (s *Settings) FullScreenSkipItemsAt(nowMs int64) []model.FullScreenSkipItem {
	out := []model.FullScreenSkipItem{}
	seen := map[string]bool{}
	for _, it := range jsonx.AnySlice(s.lookup("fullscreenSkipApps")) {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		exe := exeBasename(mapStr(m, "exe", ""))
		if exe == "" || winproc.IsHostProcess(exe) {
			continue
		}
		key := strings.ToLower(exe)
		if seen[key] {
			continue
		}
		mode := model.FullScreenSkipNever
		if strings.EqualFold(mapStr(m, "mode", ""), model.FullScreenSkipToday) {
			mode = model.FullScreenSkipToday
		}
		until := numI64(m["until"])
		// 过期的“今天”直接不认：跨过本地 24:00 就该重新问，不需要任何定时器
		if mode == model.FullScreenSkipToday && until <= nowMs {
			continue
		}
		seen[key] = true
		label := normalizeAppName(mapStr(m, "label", ""))
		if label == "" {
			label = appPauseLabel(exe)
		}
		out = append(out, model.FullScreenSkipItem{Exe: exe, Label: label, Mode: mode, Until: until})
	}
	return out
}

// FullScreenSkipMode 查某个 exe 当前的豁免方式：""（没有豁免）/ never / today
func (s *Settings) FullScreenSkipMode(exe string, nowMs int64) string {
	want := strings.ToLower(strings.TrimSpace(exe))
	if want == "" {
		return ""
	}
	for _, it := range s.FullScreenSkipItemsAt(nowMs) {
		if strings.ToLower(it.Exe) == want {
			if it.Mode == model.FullScreenSkipToday && it.Until <= nowMs {
				return ""
			}
			return it.Mode
		}
	}
	return ""
}

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

// TakeoutPostponeMin 外卖“每次推迟”的分钟数。**0 = 不允许推迟**（外卖卡「允许推迟提醒」
// 没勾）：窗口不显示“推迟”按钮，快捷键与暂停期间的自动推迟也都不做。
// 非零值最小 1 分钟（0 与 1 之间没有意义），最多 120 分钟。
// 缺键仍按 10 分钟兜底（与 2026-11-10 之前一致），不因为多了“0 = 关”的语义就把
// 没写这一项的配置读成“不允许推迟”。
func (s *Settings) TakeoutPostponeMin() int {
	return clampInt(s.Int(10, "takeout", "postponeMin"), 0, 120)
}

// TakeoutCanPostpone 外卖允不允许推迟（与 BreakConfig.CanPostpone 同一口径）
func (s *Settings) TakeoutCanPostpone() bool { return s.TakeoutPostponeMin() > 0 }

// TakeoutPauseAction 暂停休息期间外卖到点的处置：
// remind（照常弹窗）/ postpone（按“推迟时长”往后推，缺省）/ skip（不弹窗、直接记跳过）。
func (s *Settings) TakeoutPauseAction() string {
	return normPauseAction(s.Str(PauseActionPostpone, "takeout", "pauseAction"))
}

// TakeoutIdleGateMs 外卖的“鼠标键盘无活动”空闲门时长（毫秒，0 = 不设门）。
// 与休息的 IdleGateWaitMs 同一口径：没勾 / 填 0 分 0 秒都不算开。
func (s *Settings) TakeoutIdleGateMs() int64 {
	if !s.Flag(false, "takeout", "idleGate") {
		return 0
	}
	return clampInt64(s.Int(idleGateDefaultMs, "takeout", "idleGateMs"), 0, idleGateMaxMs)
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

// BreakPauseRemind 某条休息自己的“暂停休息时”是不是选了“正常提醒”。
//
// 暂停时一扇提醒窗口留不留，判据只有这一条，而且必须按**那扇窗自己的提醒**去查：
// 休息窗查它所属的那次休息，提前提示窗查它正在提示的那条提醒，自定义提醒查它自己那张卡。
// 不认的身份（这条提醒已被删掉 / 身份为空）返回 false —— 说不清归属就按缺省的“推迟”办，
// 不能靠猜把窗留着。
func (s *Settings) BreakPauseRemind(id string) bool {
	if id == "" {
		return false
	}
	c, ok := s.BreakByID(id)
	if !ok {
		return false
	}
	return normPauseAction(c.PauseAction) == PauseActionRemind
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
