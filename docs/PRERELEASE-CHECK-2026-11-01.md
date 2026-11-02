# 发布前检查报告 — 2026-11-01

> 对象：重构后（标准 Go 布局 `cmd/workerrest` + `internal/`）的下一版发布
> 口径：全部结论来自本轮实跑，不引用上一轮报告；跑不起来的写「无法测量」，不写通过。
> 结论：**代码与打包链路已就绪，但还不能发 —— 有 2 个阻断项，都在提交动作上。**

---

## 一、阻断项（必须人工处理）

| # | 阻断 | 实测 |
|---|------|------|
| 1 | **工作树 176 项未提交**（65 改 / 91 重命名 / 4 重命名+改 / 2 删 / 14 未跟踪 / 2 新增暂存） | `git status --porcelain \| wc -l` → 176 |
| 2 | **HEAD 无 tag**：`c48384e refactor: main 包下沉到 cmd/workerrest` 上没有任何 tag；最近 tag 是祖先上的 `v0.9.7` | `git tag --points-at HEAD` → 空；`make_portable.py` → `rc=2` |

未跟踪但**必须入库**的 14 项（否则别人 clone 下来编译不过或索引缺失）：

```
Taskfile.yml
cmd/workerrest/roles_bindings_test.go
docs/QA-ACCEPTANCE-2026-11-01.md
docs/PRERELEASE-CHECK-2026-11-01.md   ← 本报告
internal/settings/settings_merge.go        ← 源码，缺了直接编译失败
internal/urltls/urltls_test.go
scripts/README.md
scripts/probe/probe_{bindings_full,config_matrix,edge_cases,settings_ui}.py
scripts/probe/settings_{ui,modals,flow}_probe.js
```

已确认**不该入库**的都被正确忽略：`build/bin/`、`release/`、`config/`、`data/`、`docs/promo/`、
`.workbuddy/`、`node_modules/`、`__pycache__/`。

---

## 二、已验证通过的闸门

### 代码质量

| 闸门 | 结果 |
|---|---|
| `gofmt -l cmd internal` | 无输出 |
| `go vet -tags server ./...` | 通过 |
| `go test -count=1 -tags server ./...` | 20 个包全部 ok（14 个有测试、6 个暂无测试文件；365 个测试函数） |
| GUI 构建 `-trimpath -H windowsgui -s -w` | 成功，14,670,848 字节（13.99 MiB） |
| 无头构建 `-tags server` | 成功，14,850,048 字节 |
| `-trimpath` 泄漏扫描 | `D:/Projects` 0 处、`C:/Users/lwb` 0 处；33 处 `workerrest/cmd/workerrest`（应为这样） |

### 资产生成与打包闸门

逐个调用 `make_portable.py` 里的检查函数（跳过 tag 闸门，其余全跑）：

| 闸门 | 结果 |
|---|---|
| `check_default_sites` | 通过（`UPDATE_SITES` 与出厂 `sites` 逐字一致） |
| `check_notices` | 通过（17,168 字节，与当前构建一致） |
| `check_toolchain` | 通过（实际 `go1.27.3` == `go.mod` 的 `toolchain`） |
| `check_cleaner` | 通过（值名 `com.goodgoodlive.workerrest`，GBK+CRLF+无 BOM） |
| `check_exe_freshness` | 通过（exe 比所有 `*.go` / embed 资源新） |
| `check_winres` | 通过（icon + VERSIONINFO + MANIFEST 齐全，VERSIONINFO 版本 == 传入 tag 版本） |
| `check_embedded_defaults` | 通过（exe 内嵌出厂默认 == `internal/settings/default_settings.json`） |
| `assemble` | 通过（17 个督休专员、4 个内置提示音、notices、清启动项脚本、config；合计 17,841,768 字节） |
| `zip_dir` + `verify_zip` | 通过（59 条目、9,109,323 字节、CRC 校验、无越界路径、无 data/） |
| `make_update_zip`（7-Zip -mx=9） | 通过（5,794,660 字节，仅含 `rest.exe`） |
| `write_version_json` | 通过（`sha256`/`size` 从刚生成的 `rest.zip` 现算） |
| `verify_portable.py`（隔离 APPDATA 首跑） | **20/20**：落点在 exe 旁、预置件齐全、不外溢 APPDATA、清单哈希与包一致、清脚本逐字节同源 |

> 说明：仓库里 `rsrc_windows_amd64.syso` 的 VERSIONINFO 停在 0.9.5.0 是**设计如此** ——
> `build_exe()` 在构建那一瞬间用本次 tag 版本现生成一份临时 syso 再链接，之后还原，
> 这样不必为「syso 版本号」多打一次 commit（否则 tag 永远落不到 HEAD）。

### 版本号注入（端到端）

用 `-ldflags "-X main.appVersion=9.9.9"` 构建无头产物 → 起服务 → 调 `main.App.AppInfo`：
`AppInfo.version = '9.9.9'`。证明 `var appVersion = "dev"` 这个变量确实被 ldflags 覆盖
（如果哪天有人把它改成 `const`，这里会立刻退化成 `dev`，关于页与更新比较全跟着错）。

### 功能与界面（本轮实跑）

| 脚本 | 断言 | 结果 |
|---|---|---|
| `smoke_server.py`（绑定层无头冒烟） | 181 | 0 失败 |
| `probe_renderer.py`（六个渲染页） | 479 | 0 失败 |
| `probe_settings_ui.py`（设置页三层） | 171（91 + 53 + 27） | 0 失败 |
| `probe_gui_boot.py`（真机 GUI 启动） | 17 | 0 失败 |

上一轮跑过的 `probe_bindings_full` 134 / `probe_config_matrix` 69 / `probe_edge_cases` 33 /
`probe_sound_scan` 27 / `probe_stats_file` 19 / `probe_crossday` 11 / `verify_window_size` 14 /
`probe_window_gui` 9 / `a11y_focus` 101 / `probe_cdrop_a11y` 19 —— 本轮生产代码未变，结果沿用。

---

## 三、本轮修掉的问题

### 1. 一条「假绿」的出厂断言（真问题，已修）

`internal/settings/settings_test.go` 的 `TestDefaultFixedRolePointsAtRealRole` 用
`role.ResolveRoleDir("../../supervisors", fixed)` 找专员目录 —— 资产挪进 `assets/` 之后
这个路径恒不存在，用例每次都走 `t.Logf("...本轮跳过...")` 分支，**主断言从来没跑过**。
实际输出：

```
settings_test.go:594: 磁盘上没有专员 "秦始皇"（内置专员可被用户删除），本轮跳过 meta.json 名字比对
--- PASS
```

改成 `../../assets/supervisors`，并把「找不到就跳过」改成「找不到就红」：出厂素材随包分发，
`fixedRole` 指向一个不存在的目录时首装会静默回退到列表第一个，用户看到的人与
`default_settings.json` 写的那位不一致，且没有任何报错。现在这条真的会咬人：

```
settings_test.go:605: 出厂固定专员 秦始皇 → ..\..\assets\supervisors\秦始皇
```

### 2. 后台 goroutine 兜底线只盯本包（已修）

`TestBackgroundGoroutinesAreGuarded` 用 `filepath.Glob("*.go")` 只扫 `cmd/workerrest/`。
源码下沉后 `internal/stats/` 里两条后台循环整片移出了视野 —— 断言还在跑、还绿，只是不再
看守新家。改成遍历 `cmd/` + `internal/` 全部生产文件（现覆盖 12 个文件），今天全数合规，
明天新加未兜底的循环会被逮住。

### 3. 打包缺「脏树」闸门（已补）

`docs/QA-AUDIT-2026-10-31.md` 域2 早就建议过，一直没落。**这是当前最危险的一类问题**：
只要 HEAD 有 tag，`make_portable.py` 不管工作树脏不脏照样打包 —— 编进去的是磁盘上的代码，
而 tag 指的是另一个快照，「发布包 = 源码」的审计链就断了，而且全程没有任何异常提示。

新增 `check_worktree_clean()`：`git status --porcelain` 有输出即 `rc=1`，最多列出 12 项
（`core.quotepath=false`，中文路径可读）。救援口是 `--allow-dirty`，帮助文本写明
「仅供排查构建问题，交付禁用」。实测（本进程内假装有 tag）：

```
[make-portable] 版本: v0.9.8（exe 内注入 0.9.8）
[make-portable] FAIL: 工作树不干净：176 项未提交改动。tag 指的是提交，…
    R  supervisors/中尉军官/figure.webp -> assets/supervisors/中尉军官/figure.webp
```

### 4. `verify_portable.py` 的存活断言自相矛盾（已修）

报告里写着 `ok 便携版进程观察期内一直活着 | rc=1` —— 因为附注取的是 `proc.poll()`，
而那是在 `terminate()` **之后**求值的，永远非 0。读报告的人会以为「明明退出了却判活着」。
改为在 terminate 之前取一次 `alive_rc`，现在是 `rc=None`。

### 5. 注释腐烂 3 处（已修）

- `cmd/workerrest/main.go`：`不再有 assets/ 目录` —— 现在又有 `assets/` 了（随包素材）。
  改成「别和模块根的 assets/ 搞混」的对照说明。
- `cmd/workerrest/repo_path_test.go`：仍说「`supervisors/`、`sounds/` 留在模块根」。
- `scripts/smoke_server.py`：请求示例写 `workerrest.App.<方法>`，实际契约是
  `main.App.<方法>`（`BINDING_PREFIX` 与 `contract_test.go` 都是后者），照抄会调不通。

### 6. README 数字失真（已修）

| 项 | README 原值 | 实测 |
|---|---|---|
| exe 体积 | 13,949,440 字节（13.9 MB） | 14,670,848 字节（14 MB） |
| 单测用例 | 295 | 365 个测试函数 |
| `smoke_server` | 179 项 | 181 项 |
| `probe_renderer` | 297 项 | 479 项 |
| `probe_gui_boot` | 16 项 | 17 项 |
| 新增四支探针 | 未收录 | 已补（171 / 134 / 69 / 33） |

---

## 四、必须由人决定的 4 件事

1. **`version.json` 的 `changes` 要人工改写**。`make_portable.py` 自动填的是
   `v0.9.7..HEAD` 的提交标题（当前 10 条，全是 refactor 术语），面向用户必须改写成
   人话。脚本跑完会打印待编辑文件路径。
2. **未签名 → SmartScreen 拦截**。`docs/RISK-ACCEPTANCE-SIGNING.md` 仍然有效
   （适用 v0.9.6 及此后）。本次仍未签，新用户首次运行会遇到「Windows 已保护你的电脑」。
   若已拿到证书，按该文「补签路径」插 `signtool` 并撤销记录。
3. **操作手册是否重拍**。`docs/manual/操作手册.html` 停在 2026-10-31 16:01。本次重构是
   纯内部搬家，界面无变化；上一轮改的是对话框 Esc/焦点（无视觉变化）。按既有约定
   （用户发话才重拍）默认不动。
4. **版本号取哪个**。最近 tag `v0.9.7`，本次是重构 + 若干修复，建议 `v0.9.8`。

---

## 五、发布步骤（照抄即可）

```powershell
# 1. 提交（含 14 项未跟踪文件）
git add -A
git commit -m "refactor: 资产收进 assets/、前端产物移到 cmd/workerrest/web/dist"

# 2. 打 tag（先 commit 再 tag，否则 tag 落不到 HEAD）
git tag v0.9.8

# 3. 打包（会自己跑完 9 道闸门，包括脏树与 tag）
python scripts/build/make_portable.py

# 4. 隔离环境解包首跑复核
python scripts/build/verify_portable.py

# 5. 改写 release/WorkerRest-v0.9.8/version.json 的 changes，再重算哈希
#    （改完 changes 不影响 sha256 —— 它是 rest.zip 的哈希，但请再跑一次 verify_portable 确认）
```

产物落在 `release/WorkerRest-v0.9.8/`：`WorkerRest-portable.zip`（≈8.7 MiB）、
同名散装目录、`rest.zip`（≈5.5 MiB）、`version.json`。

---

## 六、遗留与小节

- `build/bin/` 里残留 `chibingren_capguard.exe`、`chibingren_winprobe.exe`（探针产物，已 gitignore）；
  我临时构建的 14,670,848 字节 `rest.exe` **已删除** —— 它带着 0.9.8 的版本号，
  留着会被 `--no-build` 当成本次发布的产物。`make_portable.py` 默认会重新构建。
- `probe_capture_guard.py` 在本会话**仍无法测量**（桌面 BitBlt 抓图整块全黑，非黑像素 0.0%），
  脚本已加桌面预检，此环境返回退出码 3。需在有人值守的交互式桌面上重跑。
- `internal/{plan,role,model,hotkey,jsonx,notifier}` 自身目录下没有 `_test.go`，
  它们由 `cmd/workerrest` 的测试覆盖（`role_*_test.go`、`plan_parity_test.go` 等）。
  因此 `go test -cover ./internal/plan` 会显示 no test files —— 这是既有结构，不是漏测。
