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
	// Label 给用户看的名字，来源按可信度排：EDID 型号名（DisplayConfig）→
	// 注册表里那份 EDID 的名称描述符（同一个名字，防驱动把它吞掉）→ GDI 设备名
	// （EnumDisplayDevices）→ “显示器 N”占位。**永远不是分辨率** —— 分辨率单独报在
	// Width/Height 里，面板左边放名字、右边放分辨率（2026-11-08 用户定的版式）。
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
// known 是“见过就算”的历史目录 ID（可以为 nil），只在**全失配**时用来取舍：
// 优先挑目录里见过的屏，避免把刚接上的投影算进来。
//
// 语义：
//   - 按 ID 精确匹配，配置里就是这个 ID 字符串
//   - 空列表 = 主屏。两种来路：出厂还没挑过（启动时由 settings 补成主屏 ID）、
//     以及枚举不可用的兜底。**空不等于“所有屏”**：那样写等于“新接入的屏自动包含”，
//     用户接上投影仪的那一刻提醒就会弹到投影上——而投影仪恰恰是最不该弹的那块屏。
//     想要多屏显示必须由用户亲手勾选，勾选结果是具体 ID 的快照，插新屏不会自己变。
//   - 一个都没匹配上（勾选的屏被拔了 / 换了接口 / 换了会话）→ 按“勾了几块”沿用
//     （见 fallbackByCount），绝不让提醒窗口“一块屏都不弹”
//
// 输出保持 all 的顺序，不重复。
func Resolve(ids []string, all []Info, known []string) []Info {
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
		return fallbackByCount(ids, all, known)
	}
	return out
}

// fallbackByCount 勾选的 ID 一块都对不上时，按“当初勾了几块”在当前屏里沿用。
//
// 为什么不是“只回退主屏”：远程桌面和真机是**两套完全不同的屏** —— 远程全是
// DISPLAY\Default_Monitor 虚拟屏，本地是 EDID 型号屏，ID 前缀都不一样，一个字符都
// 对不上。用户在两块屏上勾了两块，切一次会话就只剩一块弹窗，等于每次换会话都要重进
// 设置页重勾（2026-11-08 用户问的）。
//
// 为什么不是“所有屏”：接上投影仪那一刻就把投影算进来，正是勾选快照要防的事。
//
// 所以只沿用**数量**这个跨会话不变的事实：取 min(勾选数, 当前屏数) 块。挑选顺序上
// 先取历史目录里见过的（真屏），再按位置补 —— 3 块屏（两块常用 + 一块刚插的投影）时
// 兜底取 2 块不会把投影挑进来。
func fallbackByCount(ids []string, all []Info, known []string) []Info {
	n := len(ids)
	if n <= 0 {
		return primaryOf(all)
	}
	if n > len(all) {
		n = len(all)
	}
	seenKnown := make(map[string]bool, len(known))
	for _, id := range known {
		seenKnown[id] = true
	}
	out := make([]Info, 0, n)
	picked := make(map[string]bool, n)
	take := func(onlyKnown bool) {
		for _, d := range all {
			if len(out) >= n {
				return
			}
			if picked[d.ID] {
				continue
			}
			if onlyKnown && !seenKnown[d.ID] {
				continue
			}
			picked[d.ID] = true
			out = append(out, d)
		}
	}
	take(true)  // 见过的优先
	take(false) // 不够再按位置补
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
		// 型号名两级链路都空时补占位名。走到这里等于 OS 一个名字都没给
		//（远程会话 / 虚拟显示驱动），只能按顺序编号；“显示器 N”是 Windows 自己
		// 在显示设置里对这种屏的叫法，不是我们编的。
		if list[i].Label == "" {
			list[i].Label = placeholderLabel(i + 1)
		}
	}
}

// placeholderLabel 是“显示器 N”占位名。序号按 sortInfos 排完序的位置算（主屏是 1），
// 所以它天然稳定 —— 同一套接法每次枚举出来的编号一致。
func placeholderLabel(n int) string { return "显示器 " + strconv.Itoa(n) }

// IsPlaceholderLabel 判断一个名字是不是占位名。
//
// 用途只有一个：历史目录里记过真名（“LG 27UL850”）的屏，某次枚举只能给出占位名时，
// 别拿占位名把真名盖掉 —— 那样用户接回去会发现两块屏都叫“显示器 1”，
// 与面板上显示的名字也对不上。
func IsPlaceholderLabel(s string) bool {
	rest, ok := strings.CutPrefix(strings.TrimSpace(s), "显示器 ")
	if !ok {
		return false
	}
	n, err := strconv.Atoi(rest)
	return err == nil && n > 0
}

// IsSessionScopedID 判断一个标识是不是“只属于当前会话、换会话就不存在”的虚拟屏。
//
// 两类：
//   - DEFAULT_MONITOR#…：远程桌面 / 无头会话里 Windows 临时造出来的屏。它的设备路径是
//     DISPLAY\Default_Monitor\…，注册表里连 EDID 都没有。同一台机器本地登录时它不存在，
//     反过来说本地那块真屏在远程会话里也看不见 —— 两者是**互斥**的。
//   - pos:x,y：连设备路径都取不到（个别虚拟显示驱动）时拿桌面矩形兜的底，换布局就变。
//
// 这类 ID 不能进历史目录：今天远程连一次、明天本地用，历史里就会凭空多出一条
// “显示器 1 · 未连接”，删了下次远程还会回来（2026-11-08 用户问到的场景）。
func IsSessionScopedID(id string) bool {
	s := strings.TrimSpace(id)
	if s == "" {
		return true
	}
	if strings.HasPrefix(strings.ToLower(s), "pos:") {
		return true
	}
	head, _, _ := strings.Cut(s, "#")
	return strings.EqualFold(head, "DEFAULT_MONITOR")
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
