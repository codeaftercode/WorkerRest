package autostart

import (
	"testing"

	"golang.org/x/sys/windows/registry"
)

// 这些测试测的是 autostart 包的内部逻辑（注册表读写 + 命令行拼装），
// 跟着代码从 main 包迁过来，能直接访问 runKeyPath / approvedKeyPath / runValueName
// 这些未导出符号。

// ---------- 命令行拼装 ----------

func TestBuildRunCommandLine(t *testing.T) {
	cases := []struct {
		name string
		exe  string
		args []string
		want string
	}{
		{"无空格不加引号", `C:\wr\rest.exe`, []string{"--autostart"}, `C:\wr\rest.exe --autostart`},
		{"含空格加引号", `C:\Program Files\吃饼人\rest.exe`, []string{"--autostart"}, `"C:\Program Files\吃饼人\rest.exe" --autostart`},
		{"没有参数也行", `C:\wr\rest.exe`, nil, `C:\wr\rest.exe`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := BuildRunCommandLine(c.exe, c.args); got != c.want {
				t.Fatalf("BuildRunCommandLine = %q，期望 %q", got, c.want)
			}
		})
	}
}

func TestNormalizeCommandLine(t *testing.T) {
	cases := []struct{ a, b string }{
		{`"C:\wr\a.exe" --autostart`, `C:\wr\a.exe  --autostart`},
		{`  C:\wr\a.exe   --autostart  `, `C:\wr\a.exe --autostart`},
		{`"C:\Program Files\a.exe"`, `C:\Program Files\a.exe`},
	}
	for _, c := range cases {
		if NormalizeCommandLine(c.a) != NormalizeCommandLine(c.b) {
			t.Fatalf("规范化后应相等：%q → %q，%q → %q",
				c.a, NormalizeCommandLine(c.a), c.b, NormalizeCommandLine(c.b))
		}
	}
	if NormalizeCommandLine(`a.exe --autostart`) == NormalizeCommandLine(`a.exe --other`) {
		t.Fatal("不同的参数不该被规范化成同一个值")
	}
}

func TestAutostartValueName(t *testing.T) {
	// 值名同时是 AppUserModelId，统一在 com.goodgoodlive.workerrest 下。
	if runValueName != "com.goodgoodlive.workerrest" {
		t.Fatalf("值名应为 com.goodgoodlive.workerrest，得到 %q", runValueName)
	}
	if ValueName() != "com.goodgoodlive.workerrest" {
		t.Fatalf("ValueName() 应与 runValueName 一致")
	}
}

// ---------- 注册表（临时键，绝不碰真实 Run 键） ----------

func TestAutostartRegistryRoundTrip(t *testing.T) {
	oldRun, oldApproved := runKeyPath, approvedKeyPath
	// 第一件事就是改路径 + 兜底清理：万一后面 panic，也不会落到真实 Run 键上
	runKeyPath = `Software\WorkerRestSelfTest\Run`
	approvedKeyPath = `Software\WorkerRestSelfTest\StartupApproved\Run`
	defer func() {
		runKeyPath, approvedKeyPath = oldRun, oldApproved
		cleanupSelfTestKeys()
	}()
	cleanupSelfTestKeys()

	if ReadRunEntry() != nil || RunEntryExists() {
		t.Fatal("临时键初始应是干净的")
	}

	cmd := BuildRunCommandLine(`C:\Program Files\吃饼人\rest.exe`, []string{"--autostart"})
	r := WriteRunEntry(cmd)
	if !r.OK {
		t.Fatalf("写入临时键失败（沙箱可能拦了注册表写）：%s", r.Detail)
	}
	if !RunEntryExists() {
		t.Fatal("写完读回应存在")
	}
	if got := ReadRunEntry(); got == nil || *got != cmd {
		t.Fatalf("读回的命令行应逐字一致，得到 %v", got)
	}
	if d := ApprovalDisabled(); d == nil || *d {
		t.Fatalf("刚写完、没有任何“启动已批准”记录时不该是禁用态，得到 %v", d)
	}

	// 重复写入 = 覆盖成最新命令行（换了目录 / 装了新版要能纠正）
	cmd2 := BuildRunCommandLine(`C:\wr\rest.exe`, []string{"--autostart"})
	if r := WriteRunEntry(cmd2); !r.OK {
		t.Fatalf("覆盖写入失败：%s", r.Detail)
	}
	if got := ReadRunEntry(); got == nil || *got != cmd2 {
		t.Fatalf("覆盖后应读到新命令行，得到 %v", got)
	}

	// “启动已批准”里的禁用标记：首字节 03 = 用户在任务管理器里禁用过
	k, _, err := registry.CreateKey(registry.CURRENT_USER, approvedKeyPath, registry.SET_VALUE)
	if err != nil {
		t.Fatalf("建临时批准键失败：%v", err)
	}
	buf := make([]byte, 12)
	buf[0] = 0x03
	if err := k.SetBinaryValue(runValueName, buf); err != nil {
		_ = k.Close()
		t.Fatalf("写批准二进制值失败：%v", err)
	}
	_ = k.Close()
	if d := ApprovalDisabled(); d == nil || !*d {
		t.Fatalf("首字节 03 应读成“已禁用”，得到 %v", d)
	}
	// 02 = 启用
	buf[0] = 0x02
	if k2, err := registry.OpenKey(registry.CURRENT_USER, approvedKeyPath, registry.SET_VALUE); err == nil {
		_ = k2.SetBinaryValue(runValueName, buf)
		_ = k2.Close()
	}
	if d := ApprovalDisabled(); d == nil || *d {
		t.Fatalf("首字节 02 应读成“启用”，得到 %v", d)
	}
	if r := ClearApproval(); !r.OK {
		t.Fatalf("清“启动已批准”失败：%s", r.Detail)
	}
	if r := ClearApproval(); !r.OK || r.Detail != "absent" {
		t.Fatalf("再清一次应报 absent 且算成功，得到 %+v", r)
	}

	// 删除：本来就存在时必须真的删掉
	if r := RemoveRunEntry(); !r.OK {
		t.Fatalf("删除失败：%s", r.Detail)
	}
	if RunEntryExists() {
		t.Fatal("删完不该还在")
	}
	// 幂等：再删一次也算成功（目标状态已达成）
	if r := RemoveRunEntry(); !r.OK {
		t.Fatalf("重复删除应算成功：%+v", r)
	}
}

func cleanupSelfTestKeys() {
	for _, p := range []string{
		`Software\WorkerRestSelfTest\Run`,
		`Software\WorkerRestSelfTest\StartupApproved\Run`,
		`Software\WorkerRestSelfTest\StartupApproved`,
		`Software\WorkerRestSelfTest`,
	} {
		_ = registry.DeleteKey(registry.CURRENT_USER, p)
	}
}
