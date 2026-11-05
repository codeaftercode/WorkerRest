package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// 许可正文的四段关键条款必须真的在文本里：授权句、声明保留义务、免责、责任限制。
// 少任何一段，这份「MIT」就不再是 MIT —— 只写一句「MIT License」的 LICENSE 文件
// 在法务眼里等于没有授权，而打包、跑测试都不会报错。
var licenseClauses = []string{
	"Permission is hereby granted, free of charge",
	"The above copyright notice and this permission notice shall be included",
	"THE SOFTWARE IS PROVIDED \"AS IS\"",
	"IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE",
}

func TestProjectLicenseEmbedded(t *testing.T) {
	if len(projectLicense) < 800 {
		t.Fatalf("内嵌的许可证过短（%d 字节），像是没嵌进来", len(projectLicense))
	}
	// 折叠空白后再找：MIT 正文在「IN NO EVENT SHALL THE / AUTHORS OR COPYRIGHT
	// HOLDERS BE LIABLE」处换行，按行匹配会假红。
	flat := strings.Join(strings.Fields(projectLicense), " ")
	for _, want := range licenseClauses {
		if !strings.Contains(flat, want) {
			t.Errorf("许可正文缺条款：%q", want)
		}
	}
	if !strings.Contains(projectLicense, "Copyright (c)") {
		t.Error("许可正文缺版权行（Copyright (c) <年份> <权利人>）")
	}
	for _, bad := range []string{"TODO", "FIXME"} {
		if strings.Contains(projectLicense, bad) {
			t.Errorf("许可正文里残留 %s", bad)
		}
	}
}

// 内嵌源（本目录的 LICENSE）必须与仓库根那份逐字节相同。
//
// go:embed 不能引用 `../LICENSE`，所以这里放了一份副本。副本一旦漂移，
// 用户从「关于页 → 许可条款」看到的就不是仓库里那份 —— 改了年份或署名却忘了刷新副本，
// 界面上会静默显示旧正文。这条把所有漂移方式都堵死（改字、改换行、改结尾空行）。
func TestProjectLicenseMatchesRepoRoot(t *testing.T) {
	root, err := os.ReadFile("../../LICENSE")
	if err != nil {
		t.Fatalf("读不到仓库根的 LICENSE：%v", err)
	}
	if !bytes.Equal(root, []byte(projectLicense)) {
		t.Fatalf("内嵌的 LICENSE 与仓库根不一致（内嵌 %d 字节 / 仓库根 %d 字节）——"+
			"改完许可证要同步一份到 cmd/workerrest/LICENSE：cp LICENSE cmd/workerrest/LICENSE",
			len(projectLicense), len(root))
	}
}

// 内嵌源必须是 LF：文件本身入库为 LF，导出时才由 toCRLF 转换。
// 用记事本打开保存过一次就会变成 CRLF，这条让失败原因更直白。
func TestProjectLicenseEmbeddedIsLF(t *testing.T) {
	if strings.Contains(projectLicense, "\r\n") {
		t.Error("内嵌许可正文里出现 CRLF：请把 cmd/workerrest/LICENSE 存回 LF")
	}
	if !strings.HasSuffix(projectLicense, "\n") {
		t.Error("许可正文末尾缺换行")
	}
}

// 导出内容必须是 CRLF、且与内嵌文本只差行尾（不能少一段）。
func TestLicensePayloadCRLF(t *testing.T) {
	p := licensePayload()
	if !bytes.HasPrefix(p, []byte("MIT License\r\n")) {
		t.Fatalf("导出内容开头不对：%q", string(p[:min(len(p), 20)]))
	}
	stripped := bytes.ReplaceAll(p, []byte("\r\n"), nil)
	if bytes.Contains(stripped, []byte("\n")) {
		t.Error("导出内容里还有裸 LF 行尾")
	}
	if bytes.Contains(stripped, []byte("\r")) {
		t.Error("导出内容里有孤立的 CR")
	}
	if want := strings.ReplaceAll(projectLicense, "\n", ""); strings.ReplaceAll(string(p), "\r\n", "") != want {
		t.Error("导出内容与内嵌正文不等价（不只是行尾差异）")
	}
}

// 无头 / server 模式下必须干净地拒绝（返回结构化错误，而不是 panic 或静默成功）。
func TestExportLicenseHeadless(t *testing.T) {
	if !serverMode {
		t.Skip("只在 -tags server 的构建里成立（GUI 构建会真的弹对话框）")
	}
	a := &App{}
	r := a.ExportProjectLicense()
	if r["canceled"] != false {
		t.Errorf("无头环境应返回 canceled=false + error，实际 %v", r)
	}
	if r["error"] == nil || r["error"] == "" {
		t.Errorf("无头环境应给出错误说明，实际 %v", r)
	}
}
