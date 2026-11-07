package main

import (
	_ "embed"
	"log"
	"os"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// 开源鸣谢清单：整份文件嵌进 exe。
//
// 为什么嵌进二进制，而不是打包目录里散放一个 txt：
//   - MIT / BSD-3 的义务是“随二进制分发时复现版权声明与许可全文”。最常见的分发
//     方式是用户把 rest.exe 单独拷给同事 —— 散放的文件当场就丢了，嵌进 exe 才随行。
//   - 但声明**必须可读**：只把文本编译进机器码、没有任何读取入口，等于没复现。
//     所以同时给了“关于页 → 开源鸣谢”入口与本文件的另存为导出。
//   - 真源仍是仓库根这份 txt（scripts/gen_notices.py 生成；打包脚本先跑 --check，
//     依赖一升版本就失败），exe 里是它的逐字节副本。
//
// 收录范围与“为什么是这些组件”见生成器 docstring：只列实际参与本次构建的模块 +
// Go 运行时 + 内联进前端产物的库。未链接的 go.sum 条目不在其列。
//
//go:embed THIRD-PARTY-NOTICES.txt
var thirdPartyNotices string

// noticesFileName 导出的默认文件名：与仓库、便携包里的名字保持一致，
// 用户拿到手不会出现“同内容三个名字”。
const noticesFileName = "THIRD-PARTY-NOTICES.txt"

// GetThirdPartyNotices 返回随包的开源鸣谢全文（关于页“开源鸣谢”弹窗用）。
//
// 返回整份字符串而不是分段结构：它就是给人读的纯文本，前端用 textContent 写进
// <pre> 即可（不走 innerHTML，零注入面）。大小约 18 KB，一次传输可以忽略。
func (a *App) GetThirdPartyNotices() string {
	return thirdPartyNotices
}

// ExportThirdPartyNotices 把开源鸣谢清单另存为纯文本文件（L3：让“可复现”成为可验证动作）。
//
// 与“只内嵌”的区别：内嵌证明“随程序带着”，导出证明“用户随时能取出一份完整副本”——
// 审计或法务要一份声明时，不必去翻安装目录、也不必从 exe 里扒资源。
// 写盘用 CRLF（Windows 记事本的兼容性），内容与内嵌文本语义相同。
//
// 返回形状与 ExportReport 一致：取消 → {canceled:true}；失败 → {canceled:false, error}；
// 成功 → {canceled:false, path}。
func (a *App) ExportThirdPartyNotices() map[string]any {
	if !a.dialogAvailable() {
		log.Printf("[notices] 无头 / server 模式无法弹另存为对话框，返回失败")
		return map[string]any{"canceled": false, "error": "当前环境无法弹出保存对话框"}
	}
	r := showSaveDialog(a.app.Dialog, "导出开源鸣谢", noticesFileName, []application.FileFilter{
		{DisplayName: "文本文件", Pattern: "*.txt"},
		{DisplayName: "所有文件", Pattern: "*.*"},
	})
	if r.canceled {
		return map[string]any{"canceled": true}
	}
	if r.err != nil {
		logDialogErr("导出开源鸣谢", r.err)
		return map[string]any{"canceled": false, "error": "打开保存对话框失败"}
	}
	if err := os.WriteFile(r.path, noticesPayload(), 0o644); err != nil {
		log.Printf("[notices] 写入 %s 失败: %v", r.path, err)
		return map[string]any{"canceled": false, "error": "写入文件失败"}
	}
	log.Printf("[notices] 开源鸣谢已导出到 %s", r.path)
	return map[string]any{"canceled": false, "path": r.path}
}

// toCRLF 把 LF 文本转成 CRLF（幂等：已有 CRLF 不会被转成 CRCRLF）。
//
// 内嵌的那份存 LF（仓库文本约定），而导出给 Windows 用户看的文件用 CRLF ——
// 老版本记事本对纯 LF 会当成一行显示。
func toCRLF(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\n", "\r\n")
}

// noticesPayload 导出的字节内容。
//
// 单独抽出来是为了可测：导出要弹原生“另存为”对话框，自动化点不到，
// 但“到底写什么字节”是这条义务的核心，必须能被单测钉住（见 notices_test.go）。
func noticesPayload() []byte {
	return []byte(toCRLF(thirdPartyNotices))
}
