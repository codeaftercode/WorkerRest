package main

import (
	_ "embed"
	"log"
	"os"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// 项目自身的 MIT 许可证：整份正文嵌进 exe，关于页「许可条款」弹窗读的就是这一份。
//
// 为什么内嵌，而不是便携目录里放个 LICENSE 文件：
//   - MIT 的义务是「随软件的所有副本附带版权声明与许可正文」。最常见的分发方式是
//     用户把 rest.exe 单独拷给同事 —— 散放的文件当场就丢了，嵌进 exe 才随行。
//   - 目录里那份还有个更实际的毛病：更新包 rest.zip 只换 exe，装过一版的人永远停在
//     首装那天的 LICENSE 快照上。内嵌的这份跟着版本走，不会「一致地过期」。
//   - 内嵌不等于可获取：所以同时给了「关于页 → 许可条款」入口与本文件的另存为导出。
//
// 内嵌源是本目录下的 LICENSE，它是仓库根那份的逐字节副本（go:embed 不能跨目录引用
// `../LICENSE`）。两者一致由 license_test.go 与 make_portable.check_license() 双向钉住：
// 改许可证只改仓库根一处，改完不刷新副本，测试与打包都会红。
//
//go:embed LICENSE
var projectLicense string

// licenseFileName 导出的默认文件名。带 .txt 后缀是为了 Windows 双击能直接用记事本
// 打开 —— 仓库里那份叫 LICENSE（无后缀）是惯例，导出给用户看的那份不必跟着惯例。
const licenseFileName = "LICENSE.txt"

// GetProjectLicense 返回项目许可全文（关于页「许可条款」弹窗用）。
//
// 返回整份字符串：它是给人读的纯文本，前端用 textContent 写进 <pre> 即可
// （不走 innerHTML，零注入面）。约 1 KB，比一份提示音还小。
func (a *App) GetProjectLicense() string {
	return projectLicense
}

// ExportProjectLicense 把许可全文另存为纯文本文件。
//
// 与「只内嵌」的区别：内嵌证明「随程序带着」，导出证明「用户随时能取出一份完整副本」——
// 要转贴、要留档、要交给法务时，不必从 exe 里扒资源。
// 写盘用 CRLF（Windows 记事本的兼容性），内容与内嵌文本语义相同。
//
// 返回形状与 ExportThirdPartyNotices 一致：取消 → {canceled:true}；
// 失败 → {canceled:false, error}；成功 → {canceled:false, path}。
func (a *App) ExportProjectLicense() map[string]any {
	if !a.dialogAvailable() {
		log.Printf("[license] 无头 / server 模式无法弹另存为对话框，返回失败")
		return map[string]any{"canceled": false, "error": "当前环境无法弹出保存对话框"}
	}
	r := showSaveDialog(a.app.Dialog, "导出许可条款", licenseFileName, []application.FileFilter{
		{DisplayName: "文本文件", Pattern: "*.txt"},
		{DisplayName: "所有文件", Pattern: "*.*"},
	})
	if r.canceled {
		return map[string]any{"canceled": true}
	}
	if r.err != nil {
		logDialogErr("导出许可条款", r.err)
		return map[string]any{"canceled": false, "error": "打开保存对话框失败"}
	}
	if err := os.WriteFile(r.path, licensePayload(), 0o644); err != nil {
		log.Printf("[license] 写入 %s 失败: %v", r.path, err)
		return map[string]any{"canceled": false, "error": "写入文件失败"}
	}
	log.Printf("[license] 许可条款已导出到 %s", r.path)
	return map[string]any{"canceled": false, "path": r.path}
}

// licensePayload 导出的字节内容。
//
// 抽出来是为了可测：导出要弹原生「另存为」对话框，自动化点不到，
// 但「到底写什么字节」是这条义务的核心，必须能被单测钉住（见 license_test.go）。
func licensePayload() []byte {
	return []byte(toCRLF(projectLicense))
}
