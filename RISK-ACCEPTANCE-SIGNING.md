# 书面风险接受 — 暂不对发布产物做代码签名

> 文档类型：发布决策记录（Risk Acceptance）
> 决策日期：2026-10-31
> 适用版本：WorkerRest v0.9.6 及此后，直至本记录被撤销
> 关联审计：`QA-AUDIT-2026-10-31.md` 域5-2（原 BLOCKER-4，现降为 WARNING）

## 决策

**暂不对 `rest.exe` 与 `WorkerRest-portable.zip` 做 Authenticode 代码签名**，接受由此产生的 SmartScreen 与杀毒软件拦截风险。待取得代码签名证书后，按本文「补签路径」恢复签名，本记录即告撤销。

## 接受的风险范围

1. **SmartScreen 首次运行拦截**：每个新用户第一次双击运行都会遇到「Windows 已保护你的电脑」蓝色对话框，需点「更多信息 → 仍要运行」才能继续。官网直下渠道的转化率与初始信任度会因此受损。
2. **杀毒软件误报**：未签名的自更新 exe 更容易被部分杀软启发式引擎标记为可疑，可能被隔离或删除，导致更新失败。
3. **更新链无发布者信任根**：自动更新只依赖 `version.json` 的 `sha256` 完整性校验（已实现，见审计 BLOCKER-2 的修复），但**无法验证发布者身份**——一旦更新源站点被攻破且攻击者能同时伪造 `sha256`，客户端将无法通过签名证伪。此风险由「https 逐跳校验 + 更新源白名单」缓解，但未被消除。
4. **SmartScreen 信誉需重新累积**：每个新版本首次分发时信誉归零，需要一定下载量后微软才会建立信誉，期间拦截更频繁。

## 已到位的缓解措施（非签名）

以下措施已实现，可在无签名前提下降低（但无法消除）上述风险：

- 更新链完整性校验：`version.json` 必带 `sha256`，缺失/格式错即 fail closed；清单 1 MiB / 包 64 MiB / 解压 192 MiB / 条目 2048 四层上限。
- 更新源只认 https（`http://` 除回环外丢弃，重定向逐跳校验）；更新源白名单与出厂默认配置同源。
- 发布构建 `-trimpath`，二进制不泄漏构建机路径（便于「发布包 = 源码」审计）。
- exe 携带 VERSIONINFO（版本号与 git tag 同源）+ MANIFEST（`longPathAware`/`supportedOS`），与签名无关但补齐了 Windows 元信息。

## 撤销条件 / 补签路径

一旦取得 OV/EV 代码签名证书，按以下步骤补签并撤销本记录：

```bash
# 在 make_portable.py 的 build_exe() 之后、make_update_zip() 之前插入（务必带时间戳）：
signtool sign /fd sha256 /tr http://timestamp.digicert.com /td sha256 ^
  /f <证书.pfx> /p <密码> build/bin/rest.exe
# 对 zip 内解出的 exe 同样签名（zip 本身可另行签，最低要求是 exe 已签）
```

补签后验证：`signtool verify /pa /all build/bin/rest.exe` 输出 `Successfully verified` 且含 `Timestamp`。届时删除本文件、并把 `QA-AUDIT-2026-10-31.md` 域5-2 重新标注为 BLOCKER（未签）的消除状态。

## 签署

- 决策人：项目负责人
- 记录人：开发助手（SeniorDeveloper）
- 状态：**生效**
