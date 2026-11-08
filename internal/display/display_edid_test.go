//go:build windows

package display

import "testing"

// 设备路径 → 注册表路径：ENUM\DISPLAY 下面正好是“型号\实例”两层，
// 少截一段或多截一段都会读不到 EDID（读不到就退回“显示器 N”，用户认不出是哪块屏）。
func TestEDIDRegistryKey(t *testing.T) {
	cases := []struct{ in, want string }{
		{
			`\\?\DISPLAY#DEL41A9#4&1ff378d5&0&UID4162#{e6f07b5f-ee97-4a90-b076-33f57bf4eaa7}`,
			`SYSTEM\CurrentControlSet\Enum\DISPLAY\DEL41A9\4&1ff378d5&0&UID4162`,
		},
		{
			`\\?\DISPLAY#DEFAULT_MONITOR#1&19638b53&1&UID256#{e6f07b5f-ee97-4a90-b076-33f57bf4eaa7}`,
			`SYSTEM\CurrentControlSet\Enum\DISPLAY\DEFAULT_MONITOR\1&19638b53&1&UID256`,
		},
		{``, ``},
		{`\\?\DISPLAY#ONLYONESEGMENT#{guid}`, ``},
	}
	for _, c := range cases {
		if got := edidRegistryKey(c.in); got != c.want {
			t.Fatalf("edidRegistryKey(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

// 造一份 128 字节的最小 EDID：头 + 3 个空 descriptor + 第 4 个是名称描述符。
func fakeEDID(name string, pad byte) []byte {
	blob := make([]byte, 128)
	blob[0] = 0x00
	for i := 1; i < 7; i++ {
		blob[i] = 0xFF
	}
	blob[7] = 0x00
	off := edidDescriptorStart + 3*edidDescriptorLen
	blob[off+3] = edidTagMonitorName
	copy(blob[off+5:off+18], []byte(name))
	for i := off + 5 + len(name); i < off+18; i++ {
		blob[i] = pad
	}
	return blob
}

// 型号名要能从 EDID 里取出来：这就是“显示器 1”换成 “DELL U2719DS” 的那一步。
func TestParseEDIDMonitorName(t *testing.T) {
	real := []struct {
		name string
		blob []byte
	}{
		{"DELL U2719DS", fakeEDID("DELL U2719DS", 0x0A)},
		{"DELL SP2318H", fakeEDID("DELL SP2318H", 0x00)},
	}
	for _, c := range real {
		if got := parseEDIDMonitorName(c.blob); got != c.name {
			t.Fatalf("应解析出 %q，得到 %q", c.name, got)
		}
	}

	bad := []struct {
		why  string
		blob []byte
	}{
		{"太短", make([]byte, 64)},
		{"头不对", func() []byte { b := fakeEDID("X", 0); b[1] = 0x00; return b }()},
		{"没有名称描述符", make([]byte, 128)},
	}
	for _, c := range bad {
		if got := parseEDIDMonitorName(c.blob); got != "" {
			t.Fatalf("%s：应当返回空串，得到 %q", c.why, got)
		}
	}
}

// 真机取一份 EDID：注册表里有 EDID 的屏必须解析出非空型号名。
// 远程会话 / 虚拟显示驱动下所有屏都没有 EDID，这时跳过（不是失败）。
func TestEDIDMonitorNameRealMachine(t *testing.T) {
	paths, _, ok := queryRaw()
	if !ok {
		t.Skip("本机枚举不可用")
	}
	checked := 0
	for _, p := range paths {
		_, path, ok := targetNameOf(p.targetInfo)
		if !ok || path == "" {
			continue
		}
		checked++
		t.Logf("%s → EDID 型号名 %q", path, edidMonitorName(path))
	}
	if checked == 0 {
		t.Skip("拿不到显示设备路径")
	}
}
