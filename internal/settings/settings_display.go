package settings

import (
	"sort"
	"strings"
	"time"

	"workerrest/internal/display"
)

// 历史显示器目录（配置键 displayKnown）。
//
// 为什么除了 displayIds 还要单独记一份：displayIds 只是“用户勾了哪几块”的快照，
// 勾选框只列当前接着的屏，所以**用户从来没勾过、后来又被拔掉的屏**在界面上没有任何
// 入口，只能一直躺在快照里（或者被下一次保存顺手抹掉）。设置页要回答的是另一个问题：
// “这台机器上见过哪些屏、哪几块现在没接上”。那需要一个“见过就算”的目录，与勾选无关。
//
// 目录只在枚举成功时追加/更新，永远不因为“这次没枚举到”而删除 —— 删除只能由用户
// 在面板上点“删除”触发（那是唯一让用户知道自己删了什么的路径）。
//
// 唯一不进目录的是**会话级虚拟屏**（远程桌面的 DEFAULT_MONITOR、pos:x,y 兜底，
// 见 display.IsSessionScopedID）：它和本地真屏互斥，今天远程、明天本地，
// 记下来只会多出一条永远“未连接”的历史。

// KnownDisplay 曾经检测到过的一块显示器。
type KnownDisplay struct {
	ID string `json:"id"`
	// Label 当时枚举到的名字（EDID 型号名，取不到时是“显示器 N”）。
	Label  string `json:"label"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	// LastSeen 最后一次枚举到它的时间（RFC3339，本地时区），面板按它倒序排。
	LastSeen string `json:"lastSeen"`
}

// DisplayKnown 历史显示器目录，按最后可见时间倒序（最近的在前，同刻按 ID 稳定排序）。
func (s *Settings) DisplayKnown() []KnownDisplay {
	if s == nil {
		return nil
	}
	arr, _ := s.raw["displayKnown"].([]any)
	if len(arr) == 0 {
		return nil
	}
	out := make([]KnownDisplay, 0, len(arr))
	for _, it := range arr {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		k := KnownDisplay{
			ID:       mapStr(m, "id", ""),
			Label:    mapStr(m, "label", ""),
			LastSeen: mapStr(m, "lastSeen", ""),
		}
		if n, ok := m["width"].(float64); ok && n > 0 {
			k.Width = int(n)
		}
		if n, ok := m["height"].(float64); ok && n > 0 {
			k.Height = int(n)
		}
		if k.ID == "" || display.IsSessionScopedID(k.ID) {
			continue
		}
		out = append(out, k)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].LastSeen != out[j].LastSeen {
			return out[i].LastSeen > out[j].LastSeen
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// RememberDisplays 把这次枚举到的屏并进历史目录，回报“值得落盘的变化”。
//
// 变化只认三种：新出现的 ID、名字变了、分辨率变了。**lastSeen 不单独触发写盘** ——
// 每次打开设置页都重写一遍 config/settings.json 没有意义（值只在内存里刷新，
// 下一次真的保存时自然带下去）。
func (s *Settings) RememberDisplays(list []display.Info, now time.Time) bool {
	if s == nil || len(list) == 0 {
		return false
	}
	prev := s.DisplayKnown()
	byID := make(map[string]KnownDisplay, len(prev)+len(list))
	order := make([]string, 0, len(prev)+len(list))
	for _, k := range prev {
		byID[k.ID] = k
		order = append(order, k.ID)
	}
	stamp := now.Local().Format(time.RFC3339)
	changed := false
	for _, d := range list {
		// 会话级虚拟屏（远程桌面 / 无头会话的 DEFAULT_MONITOR、pos:x,y 兜底）不记：
		// 它和本地真屏互斥，记下来等于给自己造一条永远“未连接”的历史。
		if strings.TrimSpace(d.ID) == "" || display.IsSessionScopedID(d.ID) {
			continue
		}
		next := KnownDisplay{ID: d.ID, Label: d.Label, Width: d.Width, Height: d.Height, LastSeen: stamp}
		old, seen := byID[d.ID]
		// 占位名不许覆盖记过的真名：这次枚举只给出“显示器 1”（换会话、驱动抽风），
		// 历史记录里那块“LG 27UL850”不该因此改名 —— 面板上同一块屏左边显示的就是真名，
		// 两处对不上会让人以为换了一块屏。
		if seen && display.IsPlaceholderLabel(next.Label) && !display.IsPlaceholderLabel(old.Label) {
			next.Label = old.Label
		}
		if !seen {
			order = append(order, d.ID)
			changed = true
		} else if old.Label != next.Label || old.Width != next.Width || old.Height != next.Height {
			changed = true
		}
		byID[d.ID] = next
	}
	arr := make([]any, 0, len(order))
	for _, id := range order {
		k := byID[id]
		arr = append(arr, map[string]any{
			"id":       k.ID,
			"label":    k.Label,
			"width":    float64(k.Width),
			"height":   float64(k.Height),
			"lastSeen": k.LastSeen,
		})
	}
	s.raw["displayKnown"] = arr
	return changed
}
