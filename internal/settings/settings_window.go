package settings

// 设置窗口尺寸的常量与配置视图。
//
// 出厂的两个数字：
//
//   - 最小宽度 760：**实测**（scripts/probe_window_size.py 起无头服务 + Playwright 逐 10px
//     扫宽度）：740 时 .row.field 已横向溢出 2px（输入框顶到卡片外），750 起归零；取 760
//     留 10px 余量，免得 DPI / 字体差异下又贴边。620 与 940 两个高度下结论一致，说明这个
//     边界不依赖「有没有垂直滚动条」。
//   - 默认尺寸 874×857：宽度是**用户拍板**（2026-10-29 他把设置窗口拖到 874 后要求
//     「记住现在的尺寸，设为默认」）；高度随内容走 —— 2026-10-31 关于页先是加了
//     「第三方许可」一行（临界高度 858→893），随后又把「发布时间」并进版本行、
//     删掉独立的一行（−36px 行高），实测临界高度回到 857。
//     ⚠️ 关于页的内容或底部 padding 一变，必须重跑 scripts/probe_window_size.py 重取
//     这个数字，否则「默认尺寸下关于页恰好不滚」这条断言（verify_window_size.py）会红。
//
// 两者写进 default_settings.json 的 window 块，用户可改；运行时再按屏幕工作区夹一次。
const (
	SettingsWindowDefaultWidth  = 874
	SettingsWindowDefaultHeight = 857
	// 最小尺寸：宽度不能再小于实测最小宽度（再窄字段行就横向裁切）；高度给 600，
	// 各页都能滚，没有布局下限。
	SettingsWindowMinWidth  = 760
	SettingsWindowMinHeight = 600
	// 上界只在「手改配置写了个荒唐值」时兜底，正常路径永远碰不到（屏幕夹紧会先生效）。
	SettingsWindowMaxWidth  = 10000
	SettingsWindowMaxHeight = 10000
)

// clampInt 把 v 夹进 [min, max]（int64 入、int 出）。
func clampInt(v int64, min, max int) int {
	if v < int64(min) {
		return min
	}
	if v > int64(max) {
		return max
	}
	return int(v)
}

// WindowSizeConfig 设置窗口尺寸配置（磁盘 JSON 的 window 块）。
//
// 两对数各管一头：
//   - Width / Height 是**默认尺寸**（出厂 = 实测值），「不自动记录」时永远用它；
//   - LastWidth / LastHeight 是**上次用过的尺寸**（0 = 还没记录过），只在勾选
//     「自动记录窗口尺寸」时生效。
//
// 刻意分成两对而不是共用一对：共用的话「记住尺寸」一开，默认尺寸就被顶掉了，
// 用户取消勾选时已经没有「默认」可回。
type WindowSizeConfig struct {
	Width      int
	Height     int
	Remember   bool
	LastWidth  int
	LastHeight int
}

// WindowSizeCfg 读 window 块（缺字段 / 类型不符一律回落到实测默认值）
func (s *Settings) WindowSizeCfg() WindowSizeConfig {
	cfg := WindowSizeConfig{
		Width:      clampInt(s.Int(SettingsWindowDefaultWidth, "window", "width"), SettingsWindowMinWidth, SettingsWindowMaxWidth),
		Height:     clampInt(s.Int(SettingsWindowDefaultHeight, "window", "height"), SettingsWindowMinHeight, SettingsWindowMaxHeight),
		Remember:   s.Flag(true, "window", "rememberSize"),
		LastWidth:  int(s.Int(0, "window", "lastWidth")),
		LastHeight: int(s.Int(0, "window", "lastHeight")),
	}
	if !validWindowSize(cfg.LastWidth, cfg.LastHeight) {
		cfg.LastWidth, cfg.LastHeight = 0, 0
	}
	return cfg
}

// Size 这次打开要用的尺寸：勾了「自动记录」且确实记过一对合法值就用上次的，
// 否则用默认尺寸。返回值已夹进最小尺寸（配置被手改成 300 也不会开出残窗）。
func (c WindowSizeConfig) Size() (int, int) {
	w, h := c.Width, c.Height
	if c.Remember && validWindowSize(c.LastWidth, c.LastHeight) {
		w, h = c.LastWidth, c.LastHeight
	}
	return clampInt(int64(w), SettingsWindowMinWidth, SettingsWindowMaxWidth),
		clampInt(int64(h), SettingsWindowMinHeight, SettingsWindowMaxHeight)
}

// validWindowSize 一对尺寸是否可用作「上次尺寸」：两边都得 ≥ 最小值。
// 只有一边合法（手改坏 / 旧版本残留）时整对作废 —— 拿一个合法 + 一个 0 去开窗
// 会得到一扇高度 0 的窗口。
func validWindowSize(w, h int) bool {
	return w >= SettingsWindowMinWidth && h >= SettingsWindowMinHeight
}
