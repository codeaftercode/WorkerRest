package main

import (
	"log"
	"os"
	"strings"
	"unicode"

	"golang.org/x/sys/windows/registry"
)

// 开机自启动的注册表读写（Windows）—— electron/src/autostart.ts 的 Go 版。
//
// Electron 版走 `reg.exe` 子进程是因为它拿不到别的手段；Go 有
// golang.org/x/sys/windows/registry，**直接读写键值**，于是：
//   · 不启动子进程（限制子进程的安全策略下照样能跑）；
//   · 没有「reg.exe 输出按 OEM 代码页（GBK）编码」的乱码问题（那边要 TextDecoder('gbk') 转码）；
//   · 写的是宽字符字符串，含中文与空格的路径天然正确。
//
// 涉及的三个位置（都在 HKCU，不需要管理员）：
//
//	…\CurrentVersion\Run                             ← 启动项本体，值名 = AppUserModelId
//	…\Explorer\StartupApproved\Run                    ← 「启动已批准」标记
//
// 后者是任务管理器「启动」页写的：12 字节，首字节 02=启用 / 03=禁用。**只要这一项
// 存在且是 03，Run 键里的条目再多也不会开机启动**（用户曾在这里禁用过就会踩）。
// 所以勾选时直接删掉这一项（不存在的项 = 默认启用），取消时也一并清理，不留孤儿。
//
// 返回值统一为「是否成功 + 说明」，由调用方决定要不要打日志，绝不抛错影响主程序。

// runValueName HKCU Run 键里的值名 —— 与 Electron 版的 RUN_VALUE_NAME 保持一致
// （即 app.setAppUserModelId 用的那个）。
//
// ⚠️ 两个版本必须用同一个值名：Electron 版与 Go 版会先后在同一台机器上跑过，
// 值名不一致就会在注册表里留下孤儿项，「任务管理器 → 启动」里会显示两个条目。
const runValueName = "com.lwb.workerrest"

// autostartFlag 注册 Run 键时带在命令行末尾的参数，启动时据此判断「这次是开机自启动拉起来的」，
// 从而只起托盘、不弹设置窗口。与 Electron 版的 AUTOSTART_FLAG 同名同义。
const autostartFlag = "--autostart"

// 键路径写成变量（不是常量）：单测要把它们换成临时键做真实读写往返，
// **绝不能拿用户真实的 Run 键当试验田**。
var (
	runKeyPath      = `Software\Microsoft\Windows\CurrentVersion\Run`
	approvedKeyPath = `Software\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\Run`
)

// RegResult 注册表操作结果（字段名与 autostart.ts 的 RegResult 一致）
type RegResult struct {
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// ReadRunEntry 读回 Run 键里的原始命令行；不存在返回 nil。
// 只用于日志与核验，不做内容比对 —— 比对要先过 NormalizeCommandLine。
func ReadRunEntry() *string {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return nil
	}
	defer func() { _ = k.Close() }()
	v, _, err := k.GetStringValue(runValueName)
	if err != nil {
		return nil
	}
	return &v
}

// RunEntryExists Run 键里是否已有本应用的启动项
func RunEntryExists() bool { return ReadRunEntry() != nil }

// WriteRunEntry 写入启动项（勾选开机自启动）。可重复调用 —— 已有值会被覆盖成最新命令行。
//
// 顺带清掉「启动已批准」里的禁用标记：项不存在 = 默认启用。不清的话，用户曾在任务管理器里
// 禁用过就永远是禁用的 —— Run 键写得再对也不启动。
func WriteRunEntry(cmdline string) RegResult {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err != nil {
		return RegResult{OK: false, Detail: "打开 Run 键失败：" + err.Error()}
	}
	err = k.SetStringValue(runValueName, cmdline)
	_ = k.Close()
	cleared := ClearApproval()
	if err != nil {
		return RegResult{OK: false, Detail: err.Error()}
	}
	if cleared.OK {
		return RegResult{OK: true, Detail: "written"}
	}
	return RegResult{OK: true, Detail: "written(approval not cleared: " + cleared.Detail + ")"}
}

// RemoveRunEntry 删除启动项（取消勾选开机自启动）。本来就没有也算成功 —— 目标状态已达成。
func RemoveRunEntry() RegResult {
	err := error(nil)
	if k, e := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE); e == nil {
		err = k.DeleteValue(runValueName)
		_ = k.Close()
	}
	approval := ClearApproval()
	// 不存在算成功（DeleteValue 对不存在的值返回 ERROR_FILE_NOT_FOUND）
	if err != nil && RunEntryExists() {
		return RegResult{OK: false, Detail: err.Error()}
	}
	if approval.OK {
		return RegResult{OK: true, Detail: "removed"}
	}
	return RegResult{OK: true, Detail: "removed(approval not cleared: " + approval.Detail + ")"}
}

// ClearApproval 清掉「启动已批准」里本应用的条目（启用标记与禁用标记一并清掉）
func ClearApproval() RegResult {
	k, err := registry.OpenKey(registry.CURRENT_USER, approvedKeyPath, registry.SET_VALUE)
	if err != nil {
		// 连键都没有 → 本来就没有这一项 → 目标状态已达成
		return RegResult{OK: true, Detail: "absent"}
	}
	defer func() { _ = k.Close() }()
	if _, _, e := k.GetBinaryValue(runValueName); e != nil {
		return RegResult{OK: true, Detail: "absent"}
	}
	if e := k.DeleteValue(runValueName); e != nil {
		return RegResult{OK: false, Detail: e.Error()}
	}
	return RegResult{OK: true, Detail: "cleared"}
}

// ApprovalDisabled 「启动已批准」里是否标记为禁用（任务管理器 → 启动 页里的开关）。
// 返回 nil 表示读不出来（无法判断），调用方按「需要处理」对待。与 autostart.ts 同口径。
func ApprovalDisabled() *bool {
	ptr := func(b bool) *bool { return &b }
	k, err := registry.OpenKey(registry.CURRENT_USER, approvedKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return ptr(false) // 本来就没有这一项 = 默认启用
	}
	defer func() { _ = k.Close() }()
	b, _, err := k.GetBinaryValue(runValueName)
	if err != nil {
		return ptr(false)
	}
	if len(b) == 0 {
		return nil
	}
	// 12 字节，首字节 02 = 启用、03 = 禁用
	return ptr(b[0] != 0x02)
}

// NormalizeCommandLine 规范化命令行再比较：注册表里存的值可能带/不带引号、空白也可能不同，
// 直接字符串相等会把「其实已经是目标值」误判成「需要重写」。
func NormalizeCommandLine(s string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(s, `"`, "")), " ")
}

// BuildRunCommandLine 把可执行文件与参数拼成 Run 键里存的命令行。
// 路径含空格时**必须加引号**（Windows 启动项的规范写法），否则会退化成
// 「按空格截断后猜完整文件名」的解析，同目录一旦出现截断同名文件就启动错东西。
func BuildRunCommandLine(exePath string, args []string) string {
	quote := func(s string) string {
		if strings.ContainsFunc(s, unicode.IsSpace) {
			return `"` + s + `"`
		}
		return s
	}
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, quote(exePath))
	for _, a := range args {
		parts = append(parts, quote(a))
	}
	return strings.Join(parts, " ")
}

// AutostartTarget 开机自启动该指向哪个可执行文件、带什么参数。
//
// 与 Electron 版最大的差异：那边要处理 packaging 形态带来的路径陷阱
// （portable 目标的 `process.execPath` 指向 `%TEMP%\<随机>` 解包目录，写进 Run 键
// 就是一条几分钟后就失效的死路径，必须改用 `PORTABLE_EXECUTABLE_FILE`）。
// Go 版**没有这个坑**：便携形态就是 exe 本身在用户目录里，`os.Executable()` 恒为真实路径。
//
// 真正需要拦住的是开发构建：`go run` / `go build` 的临时产物落在 `%TEMP%\go-build…\`，
// 十几分钟就被清掉，注册了等于给用户装了个坏掉的自启动 —— 直接跳过并说明原因。
func AutostartTarget() (path string, args []string, ok bool) {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		return "", nil, false
	}
	if isDevBuild(exe) {
		return exe, []string{autostartFlag}, false
	}
	return exe, []string{autostartFlag}, true
}

// ApplyAutostart 让注册表里的开机自启动与 `settings.autostart` 一致。
//
// 调用点**只有「用户改了开机自启动」这一种**（设置页勾选框、托盘右键勾选、
// 恢复默认、导入配置且该项确实变了）。启动路径上完全不碰注册表 —— 每次启动都去读一遍
// 既是无谓的系统访问，也让「我根本没动它，它怎么变了」变得不可预期。
func (a *App) ApplyAutostart() {
	cfg := a.cfgSnapshot()
	want := cfg.Autostart()
	target, args, ok := AutostartTarget()
	if !ok {
		log.Printf("[autostart] 找不到可注册的可执行文件（开发构建 / 取不到 exe 真实路径），跳过（wanted=%v）", want)
		return
	}
	cmdline := BuildRunCommandLine(target, args)

	// 无头（server）模式与冒烟同口径：**绝不碰真实注册表**。Run 键是用户的，
	// 一次写就把开机自启动指到了构建产物上，而且很难察觉（用户只会觉得「自启动坏了」）。
	if serverMode {
		log.Printf("[autostart] 无头模式不写注册表（wanted=%v cmdline=%s）", want, cmdline)
		return
	}

	// 已经是目标状态就别再动注册表（避免无谓写入，也让日志安静）
	cur := ReadRunEntry()
	if want {
		dis := ApprovalDisabled()
		if cur != nil && NormalizeCommandLine(*cur) == NormalizeCommandLine(cmdline) && dis != nil && !*dis {
			log.Printf("[autostart] 注册表已是目标状态（存在），跳过")
			return
		}
	} else if cur == nil {
		log.Printf("[autostart] 注册表已是目标状态（不存在），跳过")
		return
	}

	var r RegResult
	if want {
		r = WriteRunEntry(cmdline)
	} else {
		r = RemoveRunEntry()
	}
	// 写完读回核验：不做这一步就只剩「reg 说成功」这句自述，而注册表里到底有没有、
	// 「启动已批准」是不是还压着禁用标记，恰恰是用户真正能感知到的部分。
	exists := RunEntryExists()
	verified := exists == want
	if want {
		if dis := ApprovalDisabled(); dis != nil && *dis {
			verified = false // Run 键写了也没用：任务管理器里那条被禁用着
		}
	}
	verb, got, expect := "删除", "不存在", "不存在"
	if want {
		verb, got, expect = "写入", "存在", "存在"
	}
	if !exists {
		got = "不存在"
	} else {
		got = "存在"
	}
	warn := ""
	if !verified || !r.OK {
		warn = " ⚠️ 不一致"
	}
	log.Printf("[autostart] %s自启动：%s → %s（读回 %s，期望 %s%s）", verb, cmdline, r.Detail, got, expect, warn)
}
