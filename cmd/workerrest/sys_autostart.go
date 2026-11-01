package main

import (
	"log"
	"os"

	"workerrest/internal/autostart"
)

// 开机自启动（Windows 注册表）——历史源码/autostart.ts 的 Go 版。
//
// 上一版走 `reg.exe` 子进程是因为它拿不到别的手段；Go 有
// golang.org/x/sys/windows/registry（已收进 internal/autostart），**直接读写键值**，于是：
//   · 不启动子进程（限制子进程的安全策略下照样能跑）；
//   · 没有「reg.exe 输出按 OEM 代码页（GBK）编码」的乱码问题；
//   · 写的是宽字符字符串，含中文与空格的路径天然正确。
//
// 涉及的两处键（都在 HKCU，不需要管理员）：
//
//	…\CurrentVersion\Run                             ← 启动项本体，值名 = AppUserModelId
//	…\Explorer\StartupApproved\Run                    ← 「启动已批准」标记
//
// 后者是任务管理器「启动」页写的：12 字节，首字节 02=启用 / 03=禁用。**只要这一项
// 存在且是 03，Run 键里的条目再多也不会开机启动**（用户曾在这里禁用过就会踩）。
// 所以勾选时直接删掉这一项（不存在的项 = 默认启用），取消时也一并清理，不留孤儿。
//
// 返回值统一为「是否成功 + 说明」，由调用方决定要不要打日志，绝不抛错影响主程序。
//
// 注册表读写本体在 internal/autostart（Windows 实现 + 其它平台 no-op 桩），
// 这里只保留「App 方法 + 命令行组装 + 目标状态判定」这些依赖 main 包符号的逻辑。

// autostartFlag 注册 Run 键时带在命令行末尾的参数，启动时据此判断「这次是开机自启动拉起来的」，
// 从而只起托盘、不弹设置窗口。与 上一版的 AUTOSTART_FLAG 同名同义。
const autostartFlag = "--autostart"

// AutostartTarget 开机自启动该指向哪个可执行文件、带什么参数。
//
// 与 上一版最大的差异：那边要处理 packaging 形态带来的路径陷阱
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
	cfg := a.CfgSnapshot()
	want := cfg.Autostart()
	target, args, ok := AutostartTarget()
	if !ok {
		log.Printf("[autostart] 找不到可注册的可执行文件（开发构建 / 取不到 exe 真实路径），跳过（wanted=%v）", want)
		return
	}
	cmdline := autostart.BuildRunCommandLine(target, args)

	// 无头（server）模式与冒烟同口径：**绝不碰真实注册表**。Run 键是用户的，
	// 一次写就把开机自启动指到了构建产物上，而且很难察觉（用户只会觉得「自启动坏了」）。
	if serverMode {
		log.Printf("[autostart] 无头模式不写注册表（wanted=%v cmdline=%s）", want, cmdline)
		return
	}

	// 已经是目标状态就别再动注册表（避免无谓写入，也让日志安静）
	cur := autostart.ReadRunEntry()
	if want {
		dis := autostart.ApprovalDisabled()
		if cur != nil && autostart.NormalizeCommandLine(*cur) == autostart.NormalizeCommandLine(cmdline) && dis != nil && !*dis {
			log.Printf("[autostart] 注册表已是目标状态（存在），跳过")
			return
		}
	} else if cur == nil {
		log.Printf("[autostart] 注册表已是目标状态（不存在），跳过")
		return
	}

	var r autostart.RegResult
	if want {
		r = autostart.WriteRunEntry(cmdline)
	} else {
		r = autostart.RemoveRunEntry()
	}
	// 写完读回核验：不做这一步就只剩「reg 说成功」这句自述，而注册表里到底有没有、
	// 「启动已批准」是不是还压着禁用标记，恰恰是用户真正能感知到的部分。
	exists := autostart.RunEntryExists()
	verified := exists == want
	if want {
		if dis := autostart.ApprovalDisabled(); dis != nil && *dis {
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
