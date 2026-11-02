# scripts —— 构建、打包与验证脚本

这里全部是**开发/发布期工具**，不参与产品运行时，也不编进 `rest.exe`。

> 日常用不着一张张记：根目录 `Taskfile.yml` 把常用组合收成了 `task build` / `task test` /
> `task package` / `task verify` / `task a11y` 等入口（`task --list` 查看全部）。
> 本文件是「某一项具体能力由哪个脚本负责」的索引。

## 目录

| 目录 | 内容 | 何时用 |
|---|---|---|
| `build/` | 打包与资源生成 | 发布、改图标、改依赖 |
| `probe/` | 真机/无头探针（Python 驱动 + JS 执行） | 改行为后回归 |
| `verify/` | 端到端断言（含截图） | 改 UI / 窗口尺寸后回归 |
| `oneoff/` | 一次性重构脚手架（已归档，不再维护） | 一般不用 |
| 根目录 | 冒烟、可访问性审计、手册截图、资源生成 | 见下表 |

## build/

| 脚本 | 作用 |
|---|---|
| `make_portable.py` | 便携包打包：构建 exe → 组装 → 打 zip → 校验（要求当前提交有 tag、工作树干净；排查构建问题时可 `--allow-dirty`，产物不可交付） |
| `verify_portable.py` | 便携包在隔离环境首跑：落点、预置件、不外溢 `%APPDATA%` |
| `gen_windows_res.py` | 生成 `rsrc_windows_amd64.syso`（图标 + VERSIONINFO + MANIFEST）；`--check` 复现核对 |
| `verify_windows_res.py` | 校验 exe 里的 Windows 资源与生成器一致 |
| `gen_notices.py` | 生成 `THIRD-PARTY-NOTICES.txt`（关于页开源鸣谢 + 随包分发）；`--check` 只校验 |
| `version.py` | 版本号只认 git tag：打印最近一次 tag（`--bare` 去掉 `v`） |
| `打包.bat` | 双击入口：调 `make_portable.py` |
| `清除开机自启动项.bat` | 只删注册表自启动项，**不删文件**（便携版没有卸载器）；GBK + CRLF，禁止改编码 |

## probe/

| 脚本 | 作用 |
|---|---|
| `probe_renderer.py` + `renderer_probe.js` | 渲染层探针：真实页面指向无头服务，跑 DOM 断言 |
| `probe_settings_ui.py` + `settings_ui_probe.js` / `settings_modals_probe.js` / `settings_flow_probe.js` | 设置页三层覆盖：① 全扫描——六个 tab 的全部可见控件（数量基线 / 可访问名 / 横向溢出）+ 十个弹窗的语义契约 + 关键控件与 settings 的绑定一致性；② 弹窗交互——真点击打开、焦点进框、Esc 关闭、焦点归位、Tab 陷阱、点遮罩关闭；③ 写入链路——快捷键录入（合法/裸键/重复键）、音量与播放方式、主题切换、外卖数值归一、恢复默认的取消不改配置 |
| `probe_bindings_full.py` | 绑定级全覆盖：契约表里 52 个绑定逐个真调，断言返回结构、落盘效果、拒绝分支与「探针能发现绑定丢失」的元测试 |
| `probe_config_matrix.py` | 配置矩阵：每个多值配置项（mode / selection / order / pauseAction / 音量为 0 与越界 / 时刻表越界 / sites 过滤 …）逐个取值，再验启动态（nextAt / nextSource / nextType / timeline 类型序列） |
| `probe_edge_cases.py` | 异常与边界：打包态缺资源、配置只读、20 路并发保存、emoji 与 300 字标题、100 条时刻表、落点被占成目录 |
| `probe_window_gui.py` | 真窗口：初始尺寸、拖拽记录、二次启动沿用 |
| `probe_window_size.py` + `probe_window_size.js` | 测量最小可用宽度与关于页不滚动的高度 |
| `probe_sound_scan.py` | 提示音目录体检：新增音频询问导入、死条目询问删除 |
| `probe_stats_file.py` | 统计文件自愈：首跑自建、损坏改名备份后重建 |
| `probe_crossday.py` + `crossday_probe.js` | 跨零点：统计报表跟着翻页 |
| `probe_capture_guard.py` | 共享屏幕时的捕获保护（窗底染品红 + 像素占比判据） |
| `probe_gui_boot.py` | 带控制台的 GUI 产物启动 12 秒，读日志确认启动链路 |

## verify/

| 脚本 | 作用 |
|---|---|
| `verify_window_size.py` + `window_size_verify.js` | 设置窗口尺寸端到端断言（含 `lastWidth/lastHeight` 落盘） |
| `verify_about.py` + `about_shot.js` | 关于页截图（浅色/深色）与关键字段文本比对 |

## 根目录

| 脚本 | 作用 |
|---|---|
| `smoke_server.py` | 无头冒烟：打 Wails 运行时 HTTP 端点，验「前端 → Go 绑定」链路 |
| `qa_func_supplement.py` | 功能补充：时间基准、非法值注入、配置损坏注入 |
| `a11y_audit.js` | 可访问性静态审计（只读）：可访问名、label 关联、img alt、tab 序 |
| `probe_a11y_focus.js` | 对话框焦点行为探针（101 项：进得去 / Tab 跑不出 / 关后回得来） |
| `probe_cdrop_a11y.js` | 自绘下拉 `.cdrop-host` 键盘可达性探针（19 项） |
| `probe_preload.js` | 探针专用 preload：模拟 Go 侧注入的 `window.__WR_WIN`（被上面几个 JS 复用） |
| `manual_shots.py` + `manual_shots.js` | 操作手册截图：无头服务 + 隔离配置 + Playwright |
| `build_manual.py` | 生成 `docs/manual/操作手册.html`（截图 base64 内嵌） |
| `make_seal_ink.py` | 生成印泥墨块 SVG（`seal-ink-N` / `seal-specks-N`） |
| `rebuild_role_previews.js` | 批量重做督休专员磁贴预览图 `assets/supervisors/<专员>/preview.webp` |

## oneoff/

重构期的物理搬运工具，只按行号切片/加 import 前缀，**不改变语义**，当前代码已不再需要：

`split_go.py`（按行号区间切分 Go 文件）、`prefix_model_types.py`（给下沉类型加 `model.` 前缀）、
`add_model_import.py`（补 `internal/model` import）。留着是为了让 `git log` 里的重构过程可复现。

## 约定

- **Node 依赖装在 `scripts/`**（不是仓库根）：`package.json` 与 `node_modules/` 都在这里，
  因为它只服务本目录的验证脚本，产品本身没有前端构建链。首次使用先在本目录
  `npm install`（唯一依赖是 Playwright，它复用系统已装的 Edge，不额外下载浏览器）。
  子目录（`probe/`、`verify/`）的 require 会向上找到 `scripts/node_modules`。
- Python 侧统一用 `python scripts/...` 调用，脚本自己算 `ROOT`（不依赖 cwd）。
- 探针一律用**隔离配置**（`WR_SETTINGS_PATH` 指向 `%TEMP%` 副本），跑完断言真实配置的 SHA-256 未变。
- 需要真实窗口的脚本（`probe_window_gui.py`、`probe_capture_guard.py`）只能在 Windows 真机会话里跑，
  远程桌面 / 沙箱里结果不作数。
