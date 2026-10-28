package main

import "strconv"

// 内置提醒内容：与 历史源码/messages.ts 的 builtinKindMessages 逐字同源。
//
// 这些只是**种子**数据 —— 设置页首次打开某类面板、或启动时 seedPresetEntries 跑第一次
// 时把它们种进配置，之后就是普通条目（可改、可禁、可删），删空后不再回补。id 是列表内
// 序号（"1"、"2"…，见 nextSeqID）：本列表内唯一就够，不需要类型前缀。

// builtinShortMessages 短休息：一两分钟就能完成的微休息
var builtinShortMessages = []string{
	"远眺 6 米外（20 英尺）放松眼睛",
	"眨眨眼，让眼睛湿润一下",
	"转动肩膀，放松颈部肌肉",
	"双手搓热，轻轻敷在双眼上",
	"深呼吸：吸气 4 秒，呼气 4 秒",
	"调整坐姿，双脚平放地面",
	"握拳再张开，活动一下手腕",
	"离开工位走动几分钟，活动全身",
	"接一杯水，补充水分",
	"做一组眼保健操，缓解眼疲劳",
	"伸展脊柱，缓解久坐压力",
	"看看窗外的绿色风景",
	"原地踏步或爬几层楼梯",
}

// builtinLongMessages 长休息：时间充裕，活动幅度更大、更放松
var builtinLongMessages = []string{
	"离开工位，到窗边或走廊走一走",
	"做一组全身拉伸，重点放松肩颈和腰背",
	"闭眼小憩片刻，定好闹钟再休息",
	"下楼买杯咖啡，顺便晒晒太阳",
	"整理一下桌面，给下一阶段一个清爽的开始",
	"和同事聊几句，让大脑换个频道",
}

// builtinTakeoutMessages 外卖提醒：到点吃饭的情境
var builtinTakeoutMessages = []string{
	"到点了，看看今天想吃什么？",
	"先去点外卖，回来记得接杯水。",
	"别饿着肚子硬撑，胃是要慢慢养的。",
	"下单后站起身活动一下，等餐也是休息。",
	"今天换个口味？翻翻收藏夹里的店。",
	"吃完别急着坐下，散步十分钟助消化。",
}

// builtinKindMessages 三类提醒各自的内置提醒内容
var builtinKindMessages = map[string][]string{
	"short":   builtinShortMessages,
	"long":    builtinLongMessages,
	"takeout": builtinTakeoutMessages,
}

// builtinKindItems 某类提醒的预置提醒内容条目（id 固定、默认启用）
func builtinKindItems(kind string) []ReminderMessageItem {
	texts := builtinKindMessages[kind]
	out := make([]ReminderMessageItem, 0, len(texts))
	for i, text := range texts {
		out = append(out, ReminderMessageItem{ID: strconv.Itoa(i + 1), Text: text, Enabled: true})
	}
	return out
}
