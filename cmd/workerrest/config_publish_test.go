package main

import (
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"workerrest/internal/settings"
)

// newPublishTestApp 造一个只带配置的 App。settings 的 path 必须指向临时目录里的
// 真文件：mutateConfigCopy 只有 Save 成功才会走到「换指针」那一步，path 为空时
// Save 报错提前返回，测试就成了「什么都没发生」的假绿。
func newPublishTestApp(t *testing.T) *App {
	t.Helper()
	raw := settings.DefaultSettingsMap()
	sup, _ := raw["supervisor"].(map[string]any)
	if sup == nil {
		sup = map[string]any{}
		raw["supervisor"] = sup
	}
	sup["fixedRole"] = "旧名"
	sup["order"] = []any{"旧名", "另一个"}
	a := &App{}
	a.cfg = settings.New(raw, filepath.Join(t.TempDir(), "settings.json"))
	return a
}

// supOf 取一份配置里 supervisor 子对象的引用（测试固定会造出它，断言失败胜过 panic）。
func supOf(t *testing.T, raw map[string]any) map[string]any {
	t.Helper()
	sup, ok := raw["supervisor"].(map[string]any)
	if !ok {
		t.Fatalf("配置缺 supervisor 子对象：%T", raw["supervisor"])
	}
	return sup
}

// TestRenameRoleDoesNotMutatePublishedSnapshot 是「深拷 vs 浅拷」的唯一有效钉子。
//
// 背景：renameRoleInSettings 写的是 raw["supervisor"] **嵌套层**。settings.New 存的是
// 引用，只拷顶层 map 的话换的只是壳 —— 嵌套 map 仍是发布对象那块内存。日志正常、
// 落盘正常、a.cfg 指针也确实换了，只有已经发出去的旧对象在悄悄变。所以断言必须在
// **旧对象**上做：先 CfgSnapshot() 记下指针，改名之后再读它，必须一字未动。
func TestRenameRoleDoesNotMutatePublishedSnapshot(t *testing.T) {
	a := newPublishTestApp(t)
	snap := a.CfgSnapshot()
	before := supOf(t, snap.Raw())

	a.renameRoleInSettings("旧名", "新名")

	if s, _ := before["fixedRole"].(string); s != "旧名" {
		t.Fatalf("已发布快照的 fixedRole 被改写：%q（应为 %q）", s, "旧名")
	}
	order, _ := before["order"].([]any)
	if len(order) != 2 || order[0] != "旧名" || order[1] != "另一个" {
		t.Fatalf("已发布快照的 order 被改写：%v", order)
	}
	// 反向：新对象必须真的改了，否则上面三条只是在测「什么都没发生」
	if s, _ := supOf(t, a.cfg.Raw())["fixedRole"].(string); s != "新名" {
		t.Fatalf("新配置的 fixedRole 未更新：%q", s)
	}
}

// TestClearRoleDoesNotMutatePublishedSnapshot 同上，钉 clearRoleFromSettings：
// 删除督休专员时清 fixedRole / order，同样不许动已发布的旧对象。
func TestClearRoleDoesNotMutatePublishedSnapshot(t *testing.T) {
	a := newPublishTestApp(t)
	snap := a.CfgSnapshot()
	before := supOf(t, snap.Raw())

	a.clearRoleFromSettings("旧名")

	if s, _ := before["fixedRole"].(string); s != "旧名" {
		t.Fatalf("已发布快照的 fixedRole 被改写：%q（应为 %q）", s, "旧名")
	}
	if order, _ := before["order"].([]any); len(order) != 2 {
		t.Fatalf("已发布快照的 order 长度被改写：%v", order)
	}
	after := supOf(t, a.cfg.Raw())
	if s, _ := after["fixedRole"].(string); s != "" {
		t.Fatalf("新配置的 fixedRole 未清空：%q", s)
	}
	if o, _ := after["order"].([]any); len(o) != 1 || o[0] != "另一个" {
		t.Fatalf("新配置的 order 未摘掉已删名：%v", o)
	}
}

// TestGetSettingsReturnsSnapshotNotInternal 下发的必须是深拷贝：绑定层是 JS 可调用的
// API，直接递 a.cfg.Raw() 等于把发布对象的引用递到界外 —— 外部改它会污染内部配置，
// 内部换指针后它又持有陈旧数据。两边都不许发生。
func TestGetSettingsReturnsSnapshotNotInternal(t *testing.T) {
	a := newPublishTestApp(t)
	got := a.GetSettings()
	gotSup := supOf(t, got)

	if _, ok := a.GetSettings()["supervisor"]; !ok {
		t.Fatal("GetSettings 下发的配置缺 supervisor（拷贝路径丢了字段）")
	}
	// 外部改下发对象，不许影响内部
	gotSup["fixedRole"] = "外部乱写"
	if s, _ := supOf(t, a.cfg.Raw())["fixedRole"].(string); s != "旧名" {
		t.Fatalf("外部修改下发的配置污染了内部：fixedRole=%q", s)
	}
	// 内部换指针后，旧的下发对象也不许跟着变
	a.renameRoleInSettings("旧名", "新名")
	if s, _ := gotSup["fixedRole"].(string); s != "外部乱写" {
		t.Fatalf("内部换指针后，已下发的快照被改写：%q", s)
	}
}

// TestRenameRoleConcurrentSnapshotReads 把「无锁嵌套读 × 换指针写」真正并发对撞。
//
// 生产里的读端形态（app_windows.go / prebreak.go / takeout.go … 40+ 处）是：
// `cfg := a.CfgSnapshot()`（只锁取指针那一下）→ 之后**不持锁**读 SupervisorCfg /
// SoundCfg / Raw 嵌套子树。写端是设置页改名督休专员。修复前两端在同一张 map 上
// 一读一写（settings.New 存引用，换的只是壳）；-race 下这条用例应当打出 DATA RACE。
// 修复后读端拿到的每个快照都是深拷贝出来的独立子树，写端换指针不再碰旧对象。
//
// 命中率不靠堆次数：读端 runtime.Gosched() 让出调度逼两端交错。写端每轮改名 +
// 改回（250 轮 = 500 次 rename，每次都真落盘 —— 与生产路径完全一致）。
func TestRenameRoleConcurrentSnapshotReads(t *testing.T) {
	a := newPublishTestApp(t)
	// sound.music 非空：SoundCfg().Music 走的是同一条嵌套 map 路径
	snd, _ := a.cfg.Raw()["sound"].(map[string]any)
	if snd != nil {
		snd["music"] = []any{map[string]any{"id": "m1", "file": "a.wav", "name": "A", "enabled": true}}
	}

	// 前置：换指针路径真的在跑（Save 成功、a.cfg 会换成新对象）
	before := a.CfgSnapshot()
	a.renameRoleInSettings("旧名", "另一个名字")
	if after := a.CfgSnapshot(); before == after {
		t.Fatal("前置不成立：renameRoleInSettings 没换指针（Save 失败？path 未指向可写文件？）")
	}
	a.renameRoleInSettings("另一个名字", "旧名") // 改回来，进入主循环

	const writerRounds = 250
	var reads int
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // 读端：模拟生产里拿快照后无锁读嵌套的 40+ 处
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			cfg := a.CfgSnapshot() // 只在这一下持锁
			// 下面三行全部锁外 —— 正是要验的那类访问
			_ = cfg.SupervisorCfg().Order
			_ = cfg.SoundCfg().Music
			_, _ = cfg.Raw()["supervisor"]
			reads++
			runtime.Gosched()
		}
	}()

	for i := 0; i < writerRounds; i++ {
		a.renameRoleInSettings("旧名", "另一个名字")
		a.renameRoleInSettings("另一个名字", "旧名")
	}
	close(stop)
	wg.Wait()

	if reads == 0 {
		t.Fatal("读端一次都没跑成（探针无效）")
	}
	if s, _ := supOf(t, a.cfg.Raw())["fixedRole"].(string); s != "旧名" {
		t.Fatalf("写端没跑完：fixedRole=%q（期望回到 %q）", s, "旧名")
	}
	t.Logf("并发对撞完成：读端 %d 次快照嵌套读 × 写端 %d 次 rename", reads, writerRounds*2)
}
