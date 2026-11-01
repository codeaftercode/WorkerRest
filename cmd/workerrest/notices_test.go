package main

import (
	"bytes"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// 清单必须覆盖「实际链接进发布二进制」的每一个第三方模块，且不能多声明已经不在
// 其中的模块 —— 两个方向都查，才叫「声明与实物一致」。
//
// 正向核对用 `go list -deps`（与打包构建同一个口径），**不用** debug.ReadBuildInfo()：
// 测试二进制的 Deps 里混着仅测试用的模块（本仓库是 github.com/coder/websocket，由
// wails 的测试辅助带进来），拿它做「必须覆盖全部」会逼着清单去声明一个根本没进 exe
// 的模块。取不到 go（PATH 里没有）时退化成只用反向核对，并在日志里说明由打包门禁
// （scripts/gen_notices.py --check）兜正向。
func TestNoticesCoversLinkedModules(t *testing.T) {
	if len(thirdPartyNotices) < 8000 {
		t.Fatalf("内嵌的许可清单过短（%d 字节），像是没嵌进来", len(thirdPartyNotices))
	}

	linked, haveGoList := linkedModulesFromGoList(t)
	comps := declaredComponents(thirdPartyNotices)
	declared := goModuleKeys(comps)
	if len(comps) == 0 {
		t.Fatal("清单里一条组件都解析不出来：结构变了（清单行格式见 gen_notices.py）")
	}
	if len(declared) == 0 {
		t.Fatal("清单里没有 Go 模块行：解析口径或产物结构变了")
	}

	if haveGoList {
		// 双向都用「解析出来的 key」比，不拿模字符串去 Contains 全文：
		// 清单是列对齐的（名称与版本之间可能多个空格），直接 Contains 会因列宽变化而假红。
		for _, mod := range linked {
			if !containsString(declared, mod) {
				t.Errorf("清单缺 %s —— 依赖变了就要重新生成：python scripts/gen_notices.py", mod)
			}
		}
		for _, mod := range declared {
			if !containsString(linked, mod) {
				t.Errorf("清单多声明了 %s，但它没被链接进发布构建", mod)
			}
		}
	} else {
		t.Log("PATH 里没有 go：跳过「清单覆盖全部链接模块」的正向核对（打包时由 gen_notices.py --check 兜）")
	}

	// Go 运行时与标准库也必须声明：它链接进每个 Go 二进制，是最容易被漏掉的一项
	if !strings.Contains(thirdPartyNotices, runtime.Version()) {
		t.Errorf("清单缺 Go 工具链版本 %s", runtime.Version())
	}

	// 前端内联库：它们不是 Go 模块，构建信息里看不到，只能靠这份固定清单 + 产物指纹
	// （指纹校验在 scripts/gen_notices.py，打包时跑）
	for _, want := range []component{{"qrcode", "1.5.4", ""}, {"dijkstrajs", "1.0.3", ""}} {
		if findComponent(comps, want.Name, want.Version) == nil {
			t.Errorf("清单缺前端内联库：%s %s", want.Name, want.Version)
		}
	}

	// 每个组件都必须写明协议名（用户要求的三列之一，缺了清单就只剩名字）
	for _, c := range comps {
		if strings.TrimSpace(c.License) == "" {
			t.Errorf("组件 %s %s 没写开源协议", c.Name, c.Version)
		}
	}

	// 产物开头必须是致谢 + 组件清单，且**不夹带任何说明性文字**（用户明确要求）。
	// 这条同时防「以后有人好心加回『本文件由脚本生成』之类的元信息」。
	if !strings.HasPrefix(thirdPartyNotices, "本程序使用了下列开源项目与第三方库。\n") {
		t.Errorf("清单开头不是致谢句：%q", firstLine(thirdPartyNotices))
	}
	// 文件级标题必须删掉：弹窗标题已经是「开源鸣谢」，正文再顶一遍就是重复。
	if strings.HasPrefix(thirdPartyNotices, "开源鸣谢") {
		t.Error("清单开头又出现了「开源鸣谢」标题：弹窗已有标题，正文不该重复")
	}
	if !strings.Contains(noticeIndexBlock(thirdPartyNotices), "1.") {
		t.Error("找不到「组件清单」段落")
	}
	for _, bad := range []string{"本文件由", "收录口径", "构建目标", "请勿手工编辑", "来源："} {
		if strings.Contains(thirdPartyNotices, bad) {
			t.Errorf("清单里混进了说明性文字：%q", bad)
		}
	}

	// 许可正文真的在里面（不是只有标题）：MIT 与 BSD 各自的授予句
	for _, want := range []string{
		"Permission is hereby granted, free of charge",  // MIT
		"Redistributions of source code must retain",    // BSD
		"Redistributions in binary form must reproduce", // BSD 二进制条款
	} {
		if !strings.Contains(thirdPartyNotices, want) {
			t.Errorf("清单里找不到许可正文关键句：%q", want)
		}
	}
	if strings.Contains(thirdPartyNotices, "TODO") || strings.Contains(thirdPartyNotices, "FIXME") {
		t.Error("清单里残留 TODO / FIXME")
	}
}

// 组件清单与正文章节必须一一对应：清单里列了 9 个，下面就得有 9 段全文，编号还得对得上。
// 这条防的是「清单改了、正文没跟上」这类不一致（例如删掉一段却留着清单行）。
func TestNoticesIndexMatchesSections(t *testing.T) {
	comps := declaredComponents(thirdPartyNotices)
	sec := regexp.MustCompile(`(?m)^  (\d+) / (\d+)  `).FindAllStringSubmatch(thirdPartyNotices, -1)
	if len(comps) == 0 || len(sec) == 0 {
		t.Fatalf("清单结构不对：组件清单 %d 条、正文章节 %d 段", len(comps), len(sec))
	}
	if len(comps) != len(sec) {
		t.Errorf("组件清单 %d 条与正文 %d 段对不上", len(comps), len(sec))
	}
	for i, m := range sec {
		if m[1] != strconv.Itoa(i+1) {
			t.Errorf("第 %d 段的编号是 %s，不连续", i+1, m[1])
		}
		if m[2] != strconv.Itoa(len(sec)) {
			t.Errorf("第 %d 段写的总数是 %s，实际 %d 段", i+1, m[2], len(sec))
		}
	}
	// 每段正文都必须有实质内容（许可全文至少几百字节）
	parts := regexp.MustCompile(`(?m)^  \d+ / \d+  .*$`).Split(thirdPartyNotices, -1)
	for i, p := range parts[1:] {
		if len(strings.TrimSpace(p)) < 300 {
			t.Errorf("第 %d 段正文过短（%d 字节），像是只写了标题", i+1, len(strings.TrimSpace(p)))
		}
	}
}

// 导出用的 CRLF 转换必须幂等：内嵌文本是 LF，导出时转 CRLF，
// 用户如果把导出文件再喂回来也不能变成 CRCRLF。
func TestToCRLFIdempotent(t *testing.T) {
	once := toCRLF("a\nb\n")
	if once != "a\r\nb\r\n" {
		t.Fatalf("CRLF 转换结果不对：%q", once)
	}
	if twice := toCRLF(once); twice != once {
		t.Fatalf("CRLF 转换不幂等：%q → %q", once, twice)
	}
	if toCRLF("") != "" {
		t.Error("空串应保持空串")
	}
}

// 内嵌文本必须是 LF：文件本身入库为 LF，导出才转 CRLF。
// 若有人用记事本打开保存过一次，整份清单的字节会变（--check 会红），
// 这条让失败原因更直白。
func TestNoticesEmbeddedIsLF(t *testing.T) {
	if strings.Contains(thirdPartyNotices, "\r\n") {
		t.Error("内嵌清单里出现 CRLF：请把 THIRD-PARTY-NOTICES.txt 存回 LF（导出时由 toCRLF 转换）")
	}
	if !strings.HasSuffix(thirdPartyNotices, "\n") {
		t.Error("清单末尾缺换行")
	}
}

// 导出内容的字节形态：必须是 CRLF、且不能残留裸 LF（Windows 记事本的兼容性）。
func TestNoticesPayloadCRLF(t *testing.T) {
	p := noticesPayload()
	if !bytes.HasPrefix(p, []byte("本程序使用了下列开源项目与第三方库。\r\n")) {
		t.Fatalf("导出内容开头不对：%q", string(p[:24]))
	}
	stripped := bytes.ReplaceAll(p, []byte("\r\n"), nil)
	if bytes.Contains(stripped, []byte("\n")) {
		t.Error("导出内容里还有裸 LF 行尾")
	}
	if bytes.Contains(stripped, []byte("\r")) {
		t.Error("导出内容里有孤立的 CR")
	}
	// 内容必须与内嵌文本等价（只差行尾），不能少一段
	if want := strings.ReplaceAll(thirdPartyNotices, "\n", ""); strings.ReplaceAll(string(p), "\r\n", "") != want {
		t.Error("导出内容与内嵌清单不等价（不只是行尾差异）")
	}
}

// 无头 / server 模式下必须干净地拒绝（返回结构化错误，而不是 panic 或静默成功）：
// 这条路径在冒烟与 server 模式里都会走到，弹不出对话框不是错误，但要让调用方知道。
func TestExportNoticesHeadless(t *testing.T) {
	if !serverMode {
		t.Skip("只在 -tags server 的构建里成立（GUI 构建会真的弹对话框）")
	}
	a := &App{}
	r := a.ExportThirdPartyNotices()
	if r["canceled"] != false {
		t.Errorf("无头环境应返回 canceled=false + error，实际 %v", r)
	}
	if r["error"] == nil || r["error"] == "" {
		t.Errorf("无头环境应给出错误说明，实际 %v", r)
	}
}

// linkedModulesFromGoList 用 `go list -deps` 取发布构建实际链接的模块（path + 版本）。
// 取不到 go 时返回 ok=false：调用方据此退化成只做反向核对，不静默通过。
func linkedModulesFromGoList(t *testing.T) ([]string, bool) {
	t.Helper()
	goBin := os.Getenv("GO_BIN")
	if goBin == "" {
		goBin, _ = exec.LookPath("go")
	}
	if goBin == "" {
		return nil, false
	}
	out, err := exec.Command(goBin, "list", "-deps", "-f",
		"{{with .Module}}{{.Path}}|{{.Version}}{{end}}", ".").Output()
	if err != nil {
		t.Logf("go list -deps 失败（%v）：本次只做反向核对", err)
		return nil, false
	}
	seen := map[string]bool{}
	var mods []string
	for _, ln := range strings.Split(string(out), "\n") {
		ln = strings.TrimSpace(ln)
		path, ver, ok := strings.Cut(ln, "|")
		if !ok || ver == "" || ver == "(devel)" {
			continue
		}
		key := path + " " + ver
		if !seen[key] {
			seen[key] = true
			mods = append(mods, key)
		}
	}
	if len(mods) == 0 {
		return nil, false
	}
	return mods, true
}

// 组件条目的解析。
//
// 「组件清单」段的形态固定为「N. 名称  版本  协议」：列宽按当期最长项现算
// （见 scripts/gen_notices.py），所以这里按「两个以上空格」切列，不假定具体列宽。
// 名称内部只有单空格（如「Go standard library & runtime」），不会被切开。
type component struct {
	Name    string
	Version string
	License string
}

const noticeRule = "------------------------------------------------------------------------------"

var (
	noticeColSep = regexp.MustCompile(`\s{2,}`)
	noticeNumPre = regexp.MustCompile(`^\d+\.\s*`)
)

// noticeIndexBlock 取出「组件清单」那一段的正文（两条 78 字符横线之间）。
// 只在这一段里解析：许可正文里也可能出现「1. foo」这类行，全文正则匹配会误抓。
func noticeIndexBlock(text string) string {
	head := "组件清单\n" + noticeRule + "\n"
	i := strings.Index(text, head)
	if i < 0 {
		return ""
	}
	rest := text[i+len(head):]
	j := strings.Index(rest, noticeRule)
	if j < 0 {
		return ""
	}
	return rest[:j]
}

// declaredComponents 解析出清单里的每个组件（名称 / 版本 / 协议）。
func declaredComponents(text string) []component {
	var out []component
	for _, ln := range strings.Split(noticeIndexBlock(text), "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		fields := noticeColSep.Split(ln, -1)
		if len(fields) < 3 {
			continue
		}
		name := strings.TrimSpace(noticeNumPre.ReplaceAllString(fields[0], ""))
		out = append(out, component{Name: name, Version: fields[1], License: fields[2]})
	}
	return out
}

// goModuleKeys 从组件清单里挑出 Go 模块（路径含斜杠 + 版本以 v 开头），
// 组成「path version」用于与 `go list -deps` 双向核对。
// 前端库（qrcode 1.5.4）与 Go 标准库那行（版本是 go1.27.3）都被这个口径自然排除。
func goModuleKeys(comps []component) []string {
	var out []string
	for _, c := range comps {
		if strings.Contains(c.Name, "/") && strings.HasPrefix(c.Version, "v") {
			out = append(out, c.Name+" "+c.Version)
		}
	}
	return out
}

func findComponent(comps []component, name, version string) *component {
	for i := range comps {
		if comps[i].Name == name && comps[i].Version == version {
			return &comps[i]
		}
	}
	return nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
