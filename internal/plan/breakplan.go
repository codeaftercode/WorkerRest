package plan

import (
	"strings"
	"workerrest/internal/model"
)

// ShortBreakName / LongBreakName 内置短/长的展示名（真源在 settings 包）。
const (
	ShortBreakName = "短休息"
	LongBreakName  = "长休息"
)

// ---------- 常量 ----------

// TAKEOUT_NAME 外卖提醒的展示名。它不在 BreakEntries 里（不是一种「休息」），
// 但窗口与提前提示都要用它，所以单独给一个常量。
const TAKEOUT_NAME = "外卖提醒"

// 内置短/长的展示名（不是用户起的，不进配置）。真源在 internal/settings
// （settings.ShortBreakName / settings.LongBreakName），这里用包内常量让计划层零依赖引用。
const (
	shortBreakName = ShortBreakName
	longBreakName  = LongBreakName
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

// ---------- 应用暂停匹配（纯逻辑） ----------

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
