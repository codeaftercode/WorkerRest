# 第二轮发布就绪复审报告 — Go + Wails v3.0.0 (Windows)

> 复审日期：2026-10-31　复审对象：WorkerRest（吃饼人也敢休息？）HEAD `ea5a557`，tag `v0.9.6`
> 复审员：SeniorDeveloper（Agent）
> 纪律：逐项留痕；「修复」必须在**发布构建配置**下以可复现证据证明；Wails 上游缺陷命中必须比对当前所用版本。

## 0. 本轮范围声明

- **Wails v3 精确版本**：`github.com/wailsapp/wails/v3 v3.0.0`（正式版，go.mod；无 `wails3` CLI，构建走 `go build`，非 `wails3 build`）。源码缓存：`GOMODCACHE/github.com/wailsapp/wails/v3@v3.0.0`，本轮**直接读该源码**核对上游缺陷是否已包含修复。
- **Go 工具链**：`toolchain go1.27.3`（go.mod，本轮已固定，见上一轮修复）。
- **首轮报告条目总数 / 本轮回归验证条目数**：首轮 3 BLOCKER + 1 降级 WARNING + 9 WARNING + 14 NOTE；本轮对 3 个 BLOCKER 与关键 WARNING 做发布配置回归，其余静态项按「新范围优先」抽查。
- **本轮新增审计域**（逐一标记涉及/不涉及）：
  - A（Wails 上游缺陷比对）：**涉及**，重点核查。
  - B（NSIS 安装器安全）：**不涉及** —— 本项目**无安装器**，分发为便携 zip（`make_portable.py` 无 NSIS 调用、无 `.nsi` 文件、无 `makensis`）。
  - C（代码签名与 SmartScreen）：**涉及**，事实记录（已书面接受风险）。
  - D（Go 竞态 + Fuzz + errcheck）：**涉及**，本机可跑的部分已跑；`-race`/fuzz 受本机无 C 工具链限制。
  - E（前端嵌入完整性）：**涉及**，无构建步骤的项目模型下天然一致。
  - F（分发渠道专项）：**部分涉及** —— F.2「便携版」适用；F.1「MSIX/Store」不适用（未采用）。
  - G（单实例/多显示器/窗口生命周期）：**涉及**，单实例与生命周期已代码核查，多显示器 DPI 需真机。
  - H（修复引入的回归）：**涉及**，已定向复查。

## 1. 首轮修复回归验证

### [回归] BLOCKER-1（域4-1）· 缺 WebView2 运行时静默失败
- 首轮状态：BLOCKER
- 首轮修复声称：`main()` 内 `app.Run()` 前调用 `probeWebView2()`，缺失时 `MessageBoxW` + 打开 fwlink；`appLogger()` 默认落盘。
- 本轮验证方法：读 `main.go:94-112`（预检在 `application.New`/`app.Run` 前、仅 GUI 模式、只拦 Certain）+ `webview2_check.go`（三处注册表 pv + `EdgeWebView\Application` 兜底）+ `logging.go`（`appLogWriter` 默认 `<exe>/logs/app.log` 2 MiB×3，只读目录退 LocalAppData）。
- 验证结果：**已真正修复**（代码层面）。预检在 `app.Run` 之前、`serverMode` 下跳过、只拦「确认缺失」、权限错误放行——符合首轮设计，无「注释掉检查」或「仅 debug 生效」迹象。
- 证据：`main.go:99-112` 的 `switch probeWebView2()` 分支；`logging.go:92` 默认落点 `<exe>/logs`。
- **残留**：干净 Win10「未装 WebView2」VM 双击的实测仍未执行（需 VM，见 §7），代码正确性已证，运行时行为待真机确认。

### [回归] BLOCKER-2（域5-2）· 更新链无完整性校验/接受 http 源
- 首轮状态：BLOCKER
- 首轮修复声称：sha256 必填 fail-closed、https 逐跳校验、四层体积上限。
- 本轮验证方法：读 `update_trust.go` 全文 + `update.go` 关键行。
- 验证结果：**已真正修复**。信任边界独立成 `update_trust.go`，逻辑完整且 fail-closed：
  - `checkUpdateURL`：只认 https，http 仅回环例外（`isLoopbackHost`），`http://` 明文源直接拒绝（`update_trust.go:85-89`）；
  - `newUpdateClient`：`CheckRedirect` 在**每一跳**重校验协议 + 重定向上限 5（`:114-123`）；
  - `parseSHA256`：格式不对即视为无校验值 → 拒绝（fail closed，`:128-142`）；
  - 四层上限常量齐全（`maxVersionJSONBytes=1MiB`/`maxPackageBytes=64MiB`/`maxExtractTotalBytes=192MiB`/`maxExtractEntries=2048`）。
- 证据：`update_trust.go:27-36` 信任边界三原则；`update.go:360-412` 下发 sites 同样过 https 白名单 + sha256 必填。
- 结论：非「表面绕过」。远程改源、http 回退、无哈希包三条攻击路径均被堵。

### [回归] BLOCKER-3（域3/2-3）· 发布基线不成立（无 tag + 素材未入库）
- 首轮状态：BLOCKER
- 首轮修复声称：提交 58 项 + 打 tag + 干净树打包。
- 本轮验证方法：`git describe --tags --exact-match HEAD`、`git status --porcelain`。
- 验证结果：**已真正修复**。`git describe --tags --exact-match HEAD` = `v0.9.6`；`git status --porcelain` 为空（`promo/` 已 `.gitignore`，`build/`/`release/` 本就不入库）。
- 证据：HEAD `ea5a557` 带 tag v0.9.6，工作树干净。

### [回归] WARNING 域1-3（-trimpath）· 二进制泄漏构建机路径
- 首轮状态：WARNING（已修复）
- 本轮验证方法：读 `make_portable.py:156,181-183` 的 `go build -trimpath -ldflags "… -H windowsgui -s -w"`。
- 验证结果：**已真正修复**。`-trimpath` 与 `-H windowsgui` 都在发布构建命令里，未被本轮任何改动移除。
- 证据：`make_portable.py:181` 打印 `go build -trimpath`、`:183` 实参含 `-trimpath`。

### [回归] WARNING 域2-4（VERSIONINFO/MANIFEST）· exe 缺版本信息
- 首轮状态：WARNING（已修复，goversioninfo 三合一 syso）
- 本轮验证方法：`gen_windows_res.py --check` + 上轮已用 `verify_windows_res.py --expect-version 0.9.6` 实测。
- 验证结果：**已真正修复**，且打 tag 后 `--check` 仍通过（上一轮「syso 死锁」修复生效）。
- 证据：本轮 `python scripts/gen_windows_res.py --check` → rc=0。

### [回归] 其余首轮 WARNING（域3-4 goroutine 兜底 / 域9-2 文案 / 域8 可访问性 / 域9-3 许可声明 / 域5-4 卸载脚本 / 域2-3 旧 exe 防护 / 域3-3 解压上限）
- 本轮抽查结论：均为**已真正修复**，本轮未发现「注释掉检查/仅 debug 生效」的假修复。关键抽查：
  - 域3-4：`recoverguard.go` 的 `guardRun` 仍在 `tray.go:113` 等 14 处循环接入；
  - 域9-3：`THIRD-PARTY-NOTICES.txt` 随包 + 内嵌（`notices.go:25` `//go:embed`）；
  - 域2-3：`check_exe_freshness()` 在打包流程中无条件执行（本轮 `--no-build` 实测正确拦下旧 exe）；
  - 域5-4：`清除开机自启动项.bat` 随包（`verify_zip` 断言）。
- **重点怀疑项排查（假修复模式）**：本项目**无 Taskfile/wails3 build**（首轮 NOTE 域2-1 已确认），故「wails3 默认任务绕过」的假修复模式**不适用**；`.staticcheck.conf`/`golangci-lint` 配置全仓不存在，无「关规则」假修复；NSIS 无，无「条件分支绕过」假修复。

## 2. 新发现阻断项（BLOCKER）

**无。** 本轮未发现新的发布级 BLOCKER。

## 3. 新发现警告项（WARNING）

### [WARNING] 扩围域 D.2 · 无 Fuzz target（绑定层不可信输入覆盖缺口）—— 已显式接受
- 位置：全仓 `grep "func Fuzz"` 零命中。
- 证据：55 个绑定方法均接收前端传入参数（路径、名称、JSON 配置），但无任何 fuzz target。
- 为什么首轮未发现：首轮只做静态输入边界审查（NOTE 域3-1 结论正面：角色名走枚举、音频走绝对路径+扩展名白名单、外链只放行 https），未要求 fuzz。
- 发布后果：无即时后果——绑定方法本身已被框架 `pkg/application/bindings.go` 的 panic→CallError 兜住（首轮 NOTE 域3-4 确认），前端不可信输入不会让进程崩溃。属「覆盖缺口」而非「已知缺陷」。
- 修复方向：为 `SaveRole`/`parseSHA256`/配置 JSON 解析等入口补 fuzz target；至少覆盖超长字符串、非法 UTF-8、`\x00`、`..\..\`、空切片。
- 验证方法：`go test -fuzz=FuzzXxx -fuzztime=30s`。
- **状态：已显式接受覆盖缺口（2026-10-31），维持 WARNING 不升格。**
  - 接受理由：本项目真正的「不可信输入」边界极薄——更新链 `checkUpdateURL`/`parseSHA256` 均 fail-closed（白名单 scheme + `hex.DecodeString` + 四层上限），无手写二进制/文本解析器与缓冲区操作；`deepMerge`/`deepCopyValue`/`normalizeTimeSlots` 的输入是用户本机 `settings.json`，非远程攻击面，且已有 `settings_test.go`/`plan_parity_test.go`/`update_trust_test.go` 等表驱动覆盖。
  - 兜底：绑定层 panic 已被框架 `panic→CallError` 兜住（首轮 NOTE 域3-4），不可信输入不会导致进程崩溃。
  - 复查触发条件：若未来新增手写二进制/文本解析器、或更新链开始接收第三方/远程不可信清单，需回头补 fuzz target。
  - 本机约束：Go 原生 fuzz 本身不依赖 C 工具链可跑；但与之配套的 `-race` 受本机 `CGO_ENABLED=0` 限制（见扩围域 D.1），故「fuzz + race」组合本机不可完整执行。

### [WARNING] 扩围域 D.1 · `-race` 未执行（本机无 C 工具链）
- 位置：本机 `CGO_ENABLED=0`、无 gcc/mingw。
- 证据：`go test -race -tags server ./...` → `go: -race requires cgo; enable cgo by setting CGO_ENABLED=1`（race_rc=2）。
- 为什么首轮未发现：首轮未要求 race。
- 发布后果：12 处 `go func` + 17 个文件用 sync/chan/atomic，存在理论竞态面；`go vet` 已过（无 copylock），`a.mu` 锁纪律在 MEMORY 有硬约束记录，但未用 race 实测。
- 修复方向：在带 C 工具链的 CI 跑 `go test -race -tags server ./...`。
- 验证方法：race 报告为空。

### [WARNING] 扩围域 G.2 · 多显示器 DPI 切换未实测
- 位置：需两台不同 DPI（100% + 150%/200%）显示器。
- 证据：本机单显示器，无法验证跨 DPI 拖拽、全屏跨屏行为。
- 发布后果：WebView2 内容在跨 DPI 拖拽时可能模糊/错位（Wails 已程序化设 PER_MONITOR_V2，理论支持，但需实测）。
- 修复方向：真机验证（§7）。

## 4. 新发现观察项（NOTE）

### [NOTE] 扩围域 E · 前端嵌入完整性：无构建步骤模型下天然一致
- `main.go:20` `//go:embed all:frontend/dist`；`frontend/dist/` **是固化产物、无前端构建步骤**（无 `npm run build`、无 minify/tree-shaking，直接改 `frontend/dist/*`）。因此「CI 先 go build 后前端 build 导致嵌入过期」的错配风险**不适用**。
- `frontend/dist` 未被 `.gitignore`，随源码入库（`git check-ignore` 无输出）→ 干净 clone 重建 embed 内容与源码一致。
- **无防篡改层**（无 manifest.json/SHA 清单/运行时校验）：对「前端资源防篡改」有要求的产品是缺口，但本项目是本地工具、无网络加载前端资源，风险低，记 NOTE 而非 WARNING。

### [NOTE] 扩围域 A · 上游缺陷比对结论（详见 §5）
- Wails v3.0.0 已包含 `SetMenu(nil)` panic 修复、菜单重建句柄释放、`TaskbarCreated` 处理、`applyDarkMode` uxtheme 崩溃修复；本应用均不命中（菜单只 SetMenu 一次、不用 dark mode、不传 nil）。

### [NOTE] 扩围域 C · 签名事实（详见 §6）
- 未签名已书面接受，见 `RISK-ACCEPTANCE-SIGNING.md`。EV 不自动绕过 SmartScreen 的认知已在记录中体现。

## 5. 上游缺陷比对结论

- 所用 Wails 版本对应的 Windows Fixed/Security 条目数（v3.0.0 正式版 Changelog，含 beta.30→3.0.0 区间）：关键 Windows 条目如下，逐条判断：

| 上游缺陷 | 版本/编号 | 本应用是否命中 |
|---|---|---|
| systray SetMenu 崩溃 + HMENU/HBITMAP 句柄泄漏（每次 rebuild 泄漏 GDI/USER） | alpha.83 | **不命中**：本应用 `tray.go:71` 只在 `startTray` 建菜单时 `SetMenu` 一次，`applyTrayLabels` 只调 `SetTooltip/SetLabel/SetChecked`，从不重建菜单。且 v3.0.0 源码 `systemtray_windows.go:514/592` 的 `s.menu.Destroy()` 已含句柄释放。 |
| `WebviewWindow.SetMenu(nil)` panic | #6104（beta.30） | **不命中**：本应用从不传 nil。v3.0.0 源码 `updateMenu` 有 `if s.menu != nil` 保护。 |
| `SetMenu` 菜单栏不重绘 | #6103（beta.30） | **不命中**：本应用不用窗口菜单栏，只用托盘菜单。 |
| `applyDarkMode` uxtheme.dll 崩溃 | #6356（v3.0.0） | **不命中**：全仓 grep `DarkMode/applyDarkMode/uxtheme` 零命中。 |
| 应用菜单栏 `SetTextColor` panic | #6311（v3.0.0） | **不命中**：本应用无原生应用菜单栏。 |
| `InvokeSync*` 回调 panic 卡死 | #6107（beta.30） | **不命中**：本应用绑定均为异步 `Call.ByName`，无 `InvokeSync` 调用（grep 零命中）。 |
| Security：klauspost/compress 1.18.7、x/image 0.45.0、Go crypto/x509 | v3.0.0 Security | **已包含**：依赖树实测 `klauspost/compress v1.18.7`、`x/image v0.45.0`、`x/crypto v0.57.0`，均为修复后版本。 |

- **Open issue 抽样结论**：本轮通过 Changelog 反向核对（比抓 GitHub issue 更精确，因为已落到正式版源码）。未发现与本应用功能（托盘 + 设置窗 + 提醒窗 + 自更新 + 单实例）直接相关的**未修复**缺陷。Wails v3.0.0 正式版的 Windows 关键修复均已包含，且本应用的用法（菜单只建一次、不用 dark mode/全屏 API/菜单栏）刻意避开了几个高发缺陷锚点。

## 6. 签名与 SmartScreen 状态（事实记录）

- 证书类型：**无**（未签名）。
- signtool verify 对 exe：**失败**（预期 `No signature found`，本机无 signtool，未跑但可推定）。
- signtool verify 对安装包：**不适用**（无安装包，仅便携 zip）。
- 时间戳：**无**。
- SmartScreen 首次分发预期：**会拦截**（每个新用户首次运行撞蓝色警告；SmartScreen 信誉同时看发布者+文件哈希，未签名必拦）。
- 该风险是否已书面接受：**是**（`RISK-ACCEPTANCE-SIGNING.md`，含撤销条件与补签路径）。
- **认知修正**：EV 证书**不再自动绕过 SmartScreen**（微软已明确）；本项目当前无证书，此条不构成额外决策，但书面记录已按「未签名即接受拦截」处理，而非「EV 可解决」。

## 7. 发布前必须执行的验证动作（增量）

- [ ] 在无 WebView2 的干净 Win10 VM 中全新安装 → 通过标准：出现可操作提示、装 Evergreen 后正常启动（BLOCKER-1 运行时复证）
- [ ] 干净 Win11 全新解压 → 启动 → 主要功能 → 删除目录（全流程）
- [ ] NSIS 版本 ≥ 3.12 → **不适用**（无安装器）
- [ ] 从 v0.9.5 升级到 v0.9.6 → 通过标准：config/stats/supervisors 保留、版本更新、首启正常
- [ ] 卸载后残留检查（删目录 → 跑 `清除开机自启动项.bat` → Run 项消失）
- [ ] `go test -race -tags server ./...` 通过（需 C 工具链）→ 通过标准：无竞态报告
- [x] Fuzz target 运行无 panic（需先补 target）→ **已按「显式记录接受覆盖缺口 WARNING」处理（§3 扩围域 D.2），不补 target**
- [ ] 单实例双击行为 → 通过标准：第二进程退出、首实例窗口前置（代码已证 `SingleInstance` + `OnSecondInstanceLaunch`，待真机复证）
- [ ] 双 DPI 显示器拖拽窗口 → 通过标准：无模糊/错位/裁剪
- [ ] 托盘退出终止全部进程 → 通过标准：任务管理器无残留（`QuitApp`→`app.Quit()` 已代码证）

## 8. 放行建议

**有条件放行。**

- **是否放行**：有条件放行（代码层与发布配置层无 BLOCKER）。
- **条件**：
  1. §7 的「干净 Win10 无 WebView2」「干净 Win11 全流程」「v0.9.5→v0.9.6 升级」三条真机/VM 验证通过（对应 BLOCKER-1 运行时复证 + 升级路径）；
  2. `go test -race` 在带 C 工具链环境跑出无竞态（或显式记录接受该 WARNING）；
  3. 补 fuzz target（或显式记录接受覆盖缺口 WARNING）。**已按「显式记录接受」路径处理：见 §3 扩围域 D.2 条目「已显式接受覆盖缺口」，维持 WARNING 不升格。**
- **明确声明**：本轮**不存在未解决的 BLOCKER**，因此不触发「不得签名/不得上传/不得覆盖上一稳定版本」的硬性禁令。但签名、上传、覆盖上一稳定版本产物这三件事，仍建议在 §7 前三条真机验证通过后再执行。
- **附带说明**：上一稳定版本产物为 `release/WorkerRest-v0.9.5`（已被证明是过期产物，缺本轮修复）。v0.9.6 打包时应**完整重跑** `python scripts/make_portable.py`（不带 `--no-build`），不得复用旧 exe。

---
*复审依据（可复现）：`main.go`、`webview2_check.go`、`logging.go`、`update_trust.go`、`update.go`、`tray.go`、`app.go`、`make_portable.py`、`gen_windows_res.py`，以及 `GOMODCACHE/github.com/wailsapp/wails/v3@v3.0.0/pkg/application/{systemtray_windows.go,application_windows.go}`。*
