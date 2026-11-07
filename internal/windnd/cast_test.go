package windnd

import "testing"

// 显示路径 → 投屏结论的判据表。这里钉死四条口径，改动前先看 cast.go 的注释：
//   - 两条路径共用同一个 source mode = 镜像（复制模式），算投屏
//   - 同属一个克隆组（虚拟模式下 cloneGroup 相同且非 0xFFFF）也算镜像
//   - 只有 Miracast(15) / INDIRECT_WIRED(16) 算无线投屏；INDIRECT_VIRTUAL(17) 不算
//     （那是 VR 头显、Parsec/Spacedesk 这类虚拟屏，算进去会误暂停）
//   - 扩展模式（两条路径各有各的 source mode）**不算**投屏：双屏办公用户太多，
//     误判会让提醒对他们永久失效
func TestCastStatusOf(t *testing.T) {
	const (
		internal = 0x80000000
		hdmi     = 5
		dd       = invalidCloneGroup
	)
	cases := []struct {
		name   string
		paths  []pathSummary
		active bool
		mirror bool
		wire   bool
	}{
		{"单屏", []pathSummary{{modeIdx: 1, cloneGroup: dd, tech: internal}}, false, false, false},
		{"扩展双屏（各自的 source mode）", []pathSummary{
			{modeIdx: 1, cloneGroup: dd, tech: internal},
			{modeIdx: 3, cloneGroup: dd, tech: hdmi},
		}, false, false, false},
		// 回归：2026-11-03 开发机实测形态 —— 两块外接屏挂在两个适配器上，sourceInfo.id 都是 0，
		// 旧判据（比 sourceID）会把这台扩展桌面判成镜像，提醒从此再也不弹。
		{"双显卡各驱动一块屏（桌面坐标不同，mode 也不同）", []pathSummary{
			{modeIdx: 1, cloneGroup: dd, tech: 10},
			{modeIdx: 4, cloneGroup: dd, tech: hdmi},
		}, false, false, false},
		{"复制到 HDMI（共用同一个 source mode）", []pathSummary{
			{modeIdx: 1, cloneGroup: dd, tech: internal},
			{modeIdx: 1, cloneGroup: dd, tech: hdmi},
		}, true, true, false},
		{"虚拟模式克隆组（mode 不同但同组）", []pathSummary{
			{modeIdx: 1, cloneGroup: 2, tech: internal},
			{modeIdx: 4, cloneGroup: 2, tech: hdmi},
		}, true, true, false},
		{"Miracast 无线显示", []pathSummary{
			{modeIdx: 1, cloneGroup: dd, tech: internal},
			{modeIdx: 3, cloneGroup: dd, tech: castTechMiracast},
		}, true, false, true},
		{"无线显示适配器（INDIRECT_WIRED）", []pathSummary{
			{modeIdx: 1, cloneGroup: dd, tech: castTechIndirectWired},
		}, true, false, true},
		{"INDIRECT_VIRTUAL 不算（VR/虚拟屏）", []pathSummary{
			{modeIdx: 1, cloneGroup: dd, tech: 17},
		}, false, false, false},
		{"镜像 + 无线同时存在", []pathSummary{
			{modeIdx: 1, cloneGroup: dd, tech: internal},
			{modeIdx: 1, cloneGroup: dd, tech: hdmi},
			{modeIdx: 3, cloneGroup: dd, tech: castTechMiracast},
		}, true, true, true},
		{"没有任何活动输出", nil, false, false, false},
	}
	for _, c := range cases {
		st := castStatusOf(c.paths, true)
		if !st.Supported {
			t.Fatalf("%s：查询成功时 Supported 应为真，得到 %+v", c.name, st)
		}
		if st.Active != c.active || st.Mirror != c.mirror || st.Wireless != c.wire {
			t.Fatalf("%s：期望 active=%v mirror=%v wireless=%v，得到 %+v",
				c.name, c.active, c.mirror, c.wire, st)
		}
		if st.Detail == "" {
			t.Fatalf("%s：detail 不该为空（诊断页与日志都靠它说明为什么判/没判）", c.name)
		}
	}
	// API 不可用：一律“不支持 + 有说明 + 不算投屏”。
	// 绝不报 active —— 那会在 RDP / 无桌面会话下凭空暂停休息，且用户完全不知道原因。
	if st := castStatusOf(nil, false); st.Supported || st.Active || st.Detail == "" {
		t.Fatalf("API 不可用时应为 {supported:false, active:false, detail:非空}，得到 %+v", st)
	} else if st.Mirror || st.Wireless {
		t.Fatalf("API 不可用时镜像/无线都不该置位，得到 %+v", st)
	}
}

// TestCastModeIdxInvalidIgnored 无关联 mode 的路径（modeInfoIdx 全 1）不该凑成镜像。
// 极端情况下两条路径都报 0xFFFFFFFF，若不排除就会“两个无效值相等”判成复制模式。
func TestCastModeIdxInvalidIgnored(t *testing.T) {
	st := castStatusOf([]pathSummary{
		{modeIdx: invalidModeIdx, cloneGroup: invalidCloneGroup, tech: 0x80000000},
		{modeIdx: invalidModeIdx, cloneGroup: invalidCloneGroup, tech: 5},
	}, true)
	if st.Mirror || st.Active {
		t.Fatalf("两条路径都没有关联 mode，不该判定为镜像，得到 %+v", st)
	}
}
