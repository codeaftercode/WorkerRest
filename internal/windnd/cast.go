package windnd

import "fmt"

// 投屏检测：判断“当前画面是不是正被送到本机之外的某块屏上”。
//
// 这和“屏幕被共享”是两件事，判据、失效方式、平台支持度全都不一样：
//   - 共享是**软件采集**，靠 SetWindowDisplayAffinity(WDA_EXCLUDEFROMCAPTURE) 就能让窗口不被采到，
//     根本不需要检测（本项目已经这么做了，见 capture_guard 相关代码）。
//   - 投屏是**显示输出**，画面真的画在屏幕上、只是那块屏同时被送了出去。窗口没法“不显示在
//     被投屏的那块屏上”——除非干脆不弹。所以这里只能做“检测到投屏 → 自动暂停提醒”。
//
// 判据来自 Windows 的显示拓扑（QueryDisplayConfig），非 Windows 平台走 _other 桩，
// 恒返回“不支持”，保证跨平台编译与测试。
//
// **为什么只认镜像 / 无线显示，不认“接了第二块屏”**：
// 用扩展模式接副屏的双屏办公用户极多，把“有两块屏”当投屏会让这个开关对他们永久失效
// （整天不弹提醒）。宁漏勿误——漏判的后果是“和没这个功能一样”，误判的后果是“提醒彻底不响”。
// 所以：镜像（复制模式）与无线显示设备算投屏；扩展模式、仅外接屏显示都不算。
//
// 调用失败一律降级为“不支持”，绝不抛错——自动暂停只是锦上添花的功能。
//
// **镜像判据为什么不是“两条路径的 sourceInfo.id 相同”**：
// sourceInfo.id 只在**同一个适配器内**唯一。双显卡机器上（核显管内屏、独显管外接口很常见）
// 两个适配器各自的 source 0 是两个不同的桌面源，只比 id 会把扩展桌面判成镜像。
// 2026-11-03 在开发机（Intel UHD 770 + RTX 5060 Ti 双卡、两块 DELL 扩展）实测复现：
// 两条路径 srcID 都是 0，但桌面坐标分别是 (0,0) 和 (2560,-209)，是货真价实的扩展。
// 现在的判据是“共用同一个 source mode”——复制模式下 Windows 只建一个 source mode，
// 两条路径必然指向同一下标，跨显卡复制也成立。

// CastStatus 投屏检测结果。
//
// Mirror / Wireless 分开给，是因为它们失效方式不同：前者是 Windows 显示拓扑说了算，
// 后者取决于无线显示适配器的驱动有没有如实上报 outputTechnology。诊断页把两个分开显示，
// 用户报“投屏没暂停”时才能一眼看出是哪条判据没命中。
type CastStatus struct {
	Supported bool   `json:"supported"`
	Active    bool   `json:"active"`
	Mirror    bool   `json:"mirror"`   // 桌面被镜像到另一块屏（复制模式）
	Wireless  bool   `json:"wireless"` // 存在无线 / 间接显示目标（Miracast、无线显示适配器）
	Detail    string `json:"detail"`
}

// pathSummary 一条**已激活**显示路径的精简视图，只留判据要用的三项。
// 平台层把 DISPLAYCONFIG_PATH_INFO 压成这个形状（含虚拟模式的位域拆解），
// 纯函数就不必碰 Win32 结构体，单测可以直接喂造出来的路径列表。
type pathSummary struct {
	modeIdx    uint32 // 该路径使用的 source mode 下标：两条路径共用同一下标 = 镜像
	cloneGroup uint32 // 虚拟模式下的克隆组编号；invalidCloneGroup 表示不参与克隆
	tech       uint32 // DISPLAYCONFIG_VIDEO_OUTPUT_TECHNOLOGY 的取值
}

// DISPLAYCONFIG_VIDEO_OUTPUT_TECHNOLOGY 里与“画面被送到本机之外的设备”相关的两项。
// 官方文档（wingdi.h）给的取值：MIRACAST = 15（Win8.1 起）、INDIRECT_WIRED = 16。
//
// 17（INDIRECT_VIRTUAL）刻意不算：它更常见于 VR 头显、Parsec / Spacedesk 这类虚拟显示器，
// 把“开了个虚拟屏”当投屏会误暂停。
const (
	castTechMiracast      = 15
	castTechIndirectWired = 16

	// invalidCloneGroup 虚拟模式里 0xFFFF 表示“这条路径不属任何克隆组”。
	invalidCloneGroup = 0xFFFF
	// invalidModeIdx modeInfoIdx 全 1 表示这条路径没有关联的 mode，不参与镜像判定。
	invalidModeIdx = 0xFFFFFFFF
)

// castStatusOf 路径列表 → 投屏结论（纯函数，单测直接喂路径，不必真接投影仪）。
func castStatusOf(paths []pathSummary, ok bool) CastStatus {
	if !ok {
		return CastStatus{Supported: false, Detail: "QueryDisplayConfig 调用失败或当前会话不可用"}
	}
	mirror, wireless := false, false
	modes := make(map[uint32]int, len(paths))
	clones := make(map[uint32]int, len(paths))
	for _, p := range paths {
		if p.modeIdx != invalidModeIdx {
			modes[p.modeIdx]++
			if modes[p.modeIdx] > 1 {
				mirror = true
			}
		}
		if p.cloneGroup != invalidCloneGroup {
			clones[p.cloneGroup]++
			if clones[p.cloneGroup] > 1 {
				mirror = true
			}
		}
		if p.tech == castTechMiracast || p.tech == castTechIndirectWired {
			wireless = true
		}
	}
	st := CastStatus{
		Supported: true,
		Mirror:    mirror,
		Wireless:  wireless,
		Active:    mirror || wireless,
	}
	switch {
	case mirror && wireless:
		st.Detail = fmt.Sprintf("镜像 + 无线显示（%d 条活动输出）", len(paths))
	case mirror:
		st.Detail = fmt.Sprintf("镜像到另一块屏（%d 条活动输出共用一个画面）", len(paths))
	case wireless:
		st.Detail = fmt.Sprintf("无线显示设备（%d 条活动输出）", len(paths))
	default:
		st.Detail = fmt.Sprintf("%d 条活动输出，非镜像", len(paths))
	}
	return st
}
