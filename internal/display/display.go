// Package display 枚举当前正在使用的显示器，给“提醒窗口显示在哪几块屏”用。
//
// 与 internal/windnd 的投屏检测同源（都走 QueryDisplayConfig），但目的不同：
// 那边判断“画面是不是被送到本机之外”，这边只关心“本机现在有几块屏、各是谁”。
//
// **持久化标识不能用 Wails 的 Screen.ID**：那是 HMONITOR 句柄的十进制字符串
// （screen_windows.go 的 hMonitorToScreenID），进程重启就会变，写进配置等于没写。
// 这里用 EDID 设备接口路径压出来的短标识，换接法才会变，重启不变。
package display

import (
	"errors"
	"sort"
	"strconv"
	"strings"
)

// ErrUnsupported 当前平台或会话无法枚举显示器（无桌面会话、远程会话、非 Windows）。
// 调用方按“不知道有几块屏”处理，退回只显示一块，不要当成错误弹给用户。
var ErrUnsupported = errors.New("display: 无法枚举显示器")

// Info 一块正在使用的显示器。
type Info struct {
	// ID 稳定标识（EDID 型号 + 端口实例），用于存配置。
	ID string `json:"id"`
	// Label 给用户看的名字：EDID 里的型号名，取不到时退化成“显示器 N”。
	Label string `json:"label"`
	// X/Y/Width/Height 桌面坐标与占用尺寸，单位是物理像素（与 Wails Screen.Bounds 同口径）。
	// 宽高是**旋转之后**的：竖屏显示器报 1080×1920，不是源模式的 1920×1080。
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
	// IsPrimary 主显示器 = 桌面坐标原点所在的那块（Windows 的定义，不是“笔记本内屏”）。
	IsPrimary bool `json:"isPrimary"`
}

// Resolve 按配置里的 id 列表从当前所有屏里挑出要显示的几块。
//
// 语义：
//   - 按 ID 精确匹配，配置里就是这个 ID 字符串
//   - 空列表 = 主屏。两种来路：出厂还没挑过（启动时由 settings 补成主屏 ID）、
//     以及枚举不可用的兜底。**空不等于“所有屏”**：那样写等于“新接入的屏自动包含”，
//     用户接上投影仪的那一刻提醒就会弹到投影上——而投影仪恰恰是最不该弹的那块屏。
//     想要多屏显示必须由用户亲手勾选，勾选结果是具体 ID 的快照，插新屏不会自己变。
//   - 一个都没匹配上（勾选的屏被拔了 / 换了接口）→ 回退主屏，
//     绝不让提醒窗口“一块屏都不弹”—— 那比弹错屏更糟
//
// 输出保持 all 的顺序，不重复。
func Resolve(ids []string, all []Info) []Info {
	if len(all) == 0 {
		return nil
	}
	if len(ids) == 0 {
		return primaryOf(all)
	}
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	out := make([]Info, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	add := func(d Info) {
		if !seen[d.ID] {
			seen[d.ID] = true
			out = append(out, d)
		}
	}
	for _, d := range all {
		if want[d.ID] {
			add(d)
		}
	}
	if len(out) == 0 {
		return primaryOf(all)
	}
	return out
}

// primaryOf 主屏；一块都没标主屏（个别虚拟显示驱动）时退回第一块。
func primaryOf(all []Info) []Info {
	for _, d := range all {
		if d.IsPrimary {
			return []Info{d}
		}
	}
	return all[:1]
}

// sortInfos 主屏在前，其余按桌面位置从左到右、从上到下。
// 顺序稳定，探针和设置页列表都依赖它。
func sortInfos(list []Info) {
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].IsPrimary != list[j].IsPrimary {
			return list[i].IsPrimary
		}
		if list[i].X != list[j].X {
			return list[i].X < list[j].X
		}
		return list[i].Y < list[j].Y
	})
	for i := range list {
		if list[i].Label == "" {
			list[i].Label = "显示器 " + strconv.Itoa(i+1)
		}
	}
}

// devicePathToID 把设备接口路径压成稳定短标识：
//
//	\\?\DISPLAY#DEL41A9#4&1ff378d5&0&UID4162#{e6f07b5f-…}  →  DEL41A9#4&1ff378d5&0&UID4162
//
// 前段是 EDID 的厂商 + 产品码（同型号会撞），后段是适配器实例 + 端口 UID（区分同型号的不同口）。
// 合起来既能认出“是哪块屏”，又能区分“两块一样的显示器插在哪”。
func devicePathToID(p string) string {
	p = strings.TrimPrefix(p, `\\?\DISPLAY#`)
	// 去掉结尾的接口类 GUID 段（#{e6f07b5f-…}）
	if i := strings.IndexByte(p, '{'); i >= 0 {
		p = strings.TrimSuffix(p[:i], "#")
	}
	return p
}
