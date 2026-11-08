//go:build windows

package display

import (
	"bytes"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// EDID 里取型号名（descriptor tag 0xFC）。
//
// 为什么要在 DisplayConfig 之外再读一份 EDID：两处的名字同源，都是显示器 EDID 里的
// 名称描述符，但 `monitorFriendlyDeviceName` 会被个别驱动 / 转接头 / KVM 吞成空串，
// 而设备参数里的 EDID 二进制仍然完整。设备管理器里那句
// “Generic Monitor (DELL U2719DS)” 就是 Windows 自己解析它的结果 —— 少了这一级，
// 用户只能看到“显示器 1”，根本认不出是哪块屏（2026-11-08 用户追问过一次）。

const (
	// EDID 的 4 个 descriptor 从偏移 54 起，每个 18 字节
	edidDescriptorStart = 54
	edidDescriptorLen   = 18
	edidDescriptorCount = 4
	// 0xFC = 显示器名称描述符（另有 0xFF 序列号、0xFE 自由文本）
	edidTagMonitorName = 0xFC
)

// edidRegistryKey 把显示设备接口路径翻成 ENUM\DISPLAY 下的相对路径：
//
//	\\?\DISPLAY#DEL41A9#4&1ff378d5&0&UID4162#{e6f07b5f-…}
//	→ SYSTEM\CurrentControlSet\Enum\DISPLAY\DEL41A9\4&1ff378d5&0&UID4162
func edidRegistryKey(devicePath string) string {
	// devicePathToID 正好压成 “型号#实例” 两段，与注册表层级一一对应
	id := devicePathToID(devicePath)
	parts := strings.Split(id, "#")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	return `SYSTEM\CurrentControlSet\Enum\DISPLAY\` + parts[0] + `\` + parts[1]
}

// edidMonitorName 读这块屏的 EDID 并取出型号名；读不到 / 解析不出就返回空串。
func edidMonitorName(devicePath string) string {
	key := edidRegistryKey(devicePath)
	if key == "" {
		return ""
	}
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, key+`\Device Parameters`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	blob, _, err := k.GetBinaryValue("EDID")
	if err != nil {
		return ""
	}
	return parseEDIDMonitorName(blob)
}

// parseEDIDMonitorName 从 EDID 二进制里取型号名。
//
// 名称描述符的结构是 `00 00 00 FC 00` + 13 字节 ASCII，不足的部分厂商有的补 0x0A
// （换行）、有的补 0x00，两种都得截断 —— 见过既有 “DELL U2719DS\n” 也有带一串 0 的。
func parseEDIDMonitorName(blob []byte) string {
	if len(blob) < edidDescriptorStart+edidDescriptorLen*edidDescriptorCount {
		return ""
	}
	// EDID 固定头：00 FF FF FF FF FF FF 00。头不对就当没读到（个别驱动的“EDID”是坏块）
	if blob[0] != 0x00 || blob[7] != 0x00 {
		return ""
	}
	for i := 1; i < 7; i++ {
		if blob[i] != 0xFF {
			return ""
		}
	}
	for n := 0; n < edidDescriptorCount; n++ {
		off := edidDescriptorStart + n*edidDescriptorLen
		d := blob[off : off+edidDescriptorLen]
		if d[0] != 0 || d[1] != 0 || d[2] != 0 || d[3] != edidTagMonitorName {
			continue
		}
		raw := d[5:]
		if end := bytes.IndexAny(raw, "\x00\n\r"); end >= 0 {
			raw = raw[:end]
		}
		if name := strings.TrimSpace(string(raw)); name != "" {
			return name
		}
	}
	return ""
}
