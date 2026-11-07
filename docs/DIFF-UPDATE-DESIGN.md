# 差分更新（增量更新）设计文档

> 状态：**仅设计，未实现，未改动任何代码**
> 日期：2026-11-01
> 目标：把自动更新的下载量从当前约 6 MiB（`rest.zip`，全量包）降到通常几百 KiB，
> 方式为在 exe 层做二进制差分（`bsdiff`/`bspatch`），而不是拆第三方库 DLL。

---

## 1. 背景与结论

### 1.1 为什么不能“把第三方库拆成 DLL 单独打包”

本软件用 Go + Wails v3 构建，第三方库（`wails/v3`、`go-ole`、`x/sys`、`websocket` 等）
在**编译期被静态链接进 `rest.exe`**，运行时并不 `LoadLibrary` 任何 `.dll`。它们没有
DLL 形态，无法像 C++/Electron 那样“主程序旁边放一堆 DLL，更新时只更主程序”。

因此“拆分第三方库单独打包”在 Go 下**没有技术落点**，改走“exe 级二进制差分”才是
能真正缩小更新包的正路。

### 1.2 现状数据（v0.9.6 实测）

| 项 | 值 |
|----|----|
| 裸 exe（`rest.exe`） | 13.9 MiB |
| 全量更新包 `rest.zip`（7-Zip -mx=9 极限压缩） | 5.98 MiB |
| 前端产物 `frontend/dist`（`//go:embed` 进 exe） | 1.1 MiB |
| `THIRD-PARTY-NOTICES.txt`（`//go:embed` 进 exe） | 17 KiB |
| `default_settings.json`（`//go:embed` 进 exe） | 数 KiB |

当前每次更新用户实下约 6 MiB。差分更新后，常规改动（几十到几百行业务逻辑）的 patch
通常只有**几十到几百 KiB**；改动若涉及 Wails 升级等大块框架机器码，patch 会随之变大，
这是正常波动。

### 1.3 结论

- 差分更新能落地，且与本项目**已有的纯静态更新架构**、**严格的 https+sha256 信任模型**、
  **规范的 `v主.次.修订` 版本号**高度兼容。
- 主要成本在**客户端**（补丁合成算法 + patch 选择/回退逻辑）和**打包脚本**
  （多生成 patch 文件 + `version.json` 扩展字段），**不在服务器**。
- 服务器**纯静态即可**，无需任何动态服务、数据库、后端程序（见 §3）。

---

## 2. 整体机制

```
打包机（本地跑 make_portable.py 时）：
    旧 rest.exe ──┐
                  ├─ bsdiff ──→ v0.9.6-v0.9.7.patch（几百 KiB，静态文件）
    新 rest.exe ──┘
    上传到静态站：patches/v0.9.6-v0.9.7.patch

客户端（发现新版本时）：
    读 version.json → 有 from==当前版本的 patch？
      ├─ 有：下载 patch（https + sha256 校验）
      │      → bspatch 旧 exe + patch = 新 exe
      │      → 校验合成 exe 的 sha256 == 清单值
      │      → 成功则替换；失败则回退
      └─ 无 / 失败：回退下载全量 rest.zip（走现有老逻辑，完全不受影响）
```

三个角色：

1. **diff 生成**（打包机）：`bsdiff 旧exe 新exe patch`
2. **patch 应用**（客户端）：`bspatch 旧exe patch 新exe`
3. **兜底校验**：合成新 exe 的 sha256 必须 == 清单值，否则放弃、回退全量包

关键洞察：patch 是**打包那一刻就固定下来的死文件**，不是用户请求时才现场算，所以
没有任何“服务端要跑 bsdiff”的环节。

---

## 3. 服务器要求（纯静态完全够）

### 3.1 需要上传的静态文件

以 v0.9.7 发布为例，`make_portable.py` 额外产出并在静态站（`goodgoodlive.com/worker-rest/`）
增加：

```
worker-rest/
├── version.json                      # 升级：新增 patches 字段
├── rest.zip                          # 全量包（保留，作兜底）
├── patches/
│   ├── v0.9.6-v0.9.7.patch           # 0.9.6 → 0.9.7
│   ├── v0.9.5-v0.9.7.patch           # 0.9.5 → 0.9.7
│   └── ...                           # 只保留最近 N 个旧版的 patch
```

### 3.2 为什么不需要任何服务端程序

| 需求 | 谁来做 | 需要动态服务吗 |
|------|--------|--------------|
| 生成 patch 文件 | 打包机（本地离线算） | 否 |
| 决定给谁哪个 patch | 客户端（自己拿当前版本号对 `patches` 数组） | 否 |
| 完整性校验 | 客户端（sha256） | 否 |
| 存储 patch 文件 | 静态文件服务器 / CDN / 对象存储 | 否（纯静态） |

`version.json`、`rest.zip`、`patches/*.patch` 三者地位完全相同，都是静态文件。Nginx、
CDN、对象存储桶任意其一即可，**没有任何服务端代码需要写或跑**。

---

## 4. version.json 扩展设计

### 4.1 新增 `patches` 字段

```json
{
  "version": "v0.9.7",
  "date": "2026.11.01",
  "url": ["https://www.goodgoodlive.com/worker-rest/"],
  "sites": ["https://www.goodgoodlive.com/worker-rest/version.json"],
  "changes": ["1. ..."],
  "sha256": "<全量 rest.zip 的 sha256>",
  "size": 5983839,
  "patches": [
    {
      "from": "v0.9.6",
      "sha256": "<v0.9.6-v0.9.7.patch 的 sha256>",
      "size": 385200,
      "url": "https://www.goodgoodlive.com/worker-rest/patches/v0.9.6-v0.9.7.patch"
    },
    {
      "from": "v0.9.5",
      "sha256": "<v0.9.5-v0.9.7.patch 的 sha256>",
      "size": 420100,
      "url": "https://www.goodgoodlive.com/worker-rest/patches/v0.9.5-v0.9.7.patch"
    }
  ]
}
```

### 4.2 字段语义

| 字段 | 说明 |
|------|------|
| `patches` | 数组，可空/可省略。缺省即“本次发布不提供差分”，客户端直接走全量包 |
| `patches[].from` | 该 patch 适用于的**旧版本号**（三段式 `v主.次.修订`），客户端用 `==` 精确匹配自己的当前版本 |
| `patches[].url` | patch 文件的下载地址（**独立地址**，不与全量包 `url` 数组混用；若省略可按约定拼接） |
| `patches[].sha256` | patch 文件自身的 sha256（**必填**，下载后校验） |
| `patches[].size` | 可选大小提示，仅用于提前报错 |

### 4.3 版本匹配规则

- 客户端当前版本 `== patches[i].from` 时使用该 patch。
- **版本号规范是前提**：本项目的 tag 是严格 `v主.次.修订`（`TAG_RE = ^v(\d+)\.(\d+)\.(\d+)$`），
  三段式可解析、可精确比较，天然适合做“from”的精确匹配，不存在歧义。
- 客户端版本不在任何 `from` 里（太老 / 中间跳过），则走全量包。

---

## 5. 补丁算法选型

### 5.1 候选

| 工具 | 语言 | 特点 |
|------|------|------|
| **bsdiff/bspatch** | C | 事实标准，Chrome/各大软件增量更新普遍使用；diff 体积小；patch 合成需 `旧文件+新文件+差分索引` 进内存 |
| **hdiffpatch** | C/C++ | 大文件（GB 级）优化强，本项目 13 MiB 属“小文件”，优势用不上 |
| **courgette** | C++ | Google Chrome 专用，对 PE 可执行文件做了“先反汇编对齐再差分”，diff 更小但实现复杂 |

### 5.2 建议

- **首选 bsdiff/bspatch**：成熟、体积够小、Go 侧有现成的纯 Go 实现可引入或参考
  （如 `github.com/gabstv/go-bsdiff` 等，也可直接用官方 C 源码交叉编译一个小 CLI，
  作为打包机工具；客户端侧则用纯 Go 的 bspatch 实现，避免 cgo/DLL 依赖——这与本项目的
  “无外挂 DLL”原则一致）。
- 客户端合成时的内存开销：bsdiff 系算法需把旧文件、新文件、差分块索引都载入内存，
  13 MiB 文件峰值约几十 MiB，可接受。
- **不要用 courgette**：收益对 13 MiB 小 exe 不显著，复杂度不值得。

---

## 6. 客户端更新流程改动（状态机）

### 6.1 目标流程

```
发现新版本（checkForUpdate 判定 update-available）
        │
        ▼
在 patches 数组里找 from == 当前版本？
        │
   ┌────┴────┐
   有        无
   │         │
   ▼         ▼
下载 patch   下载全量 rest.zip（现有逻辑，不变）
（https+sha256）
   │
   ▼
bspatch 合成新 exe
   │
   ▼
校验合成 exe sha256 == 清单值？
   │
  ┌┴────────┐
  对         不对 / 任何一步失败
  │          │
  ▼          ▼
替换、重启    删除中间产物，回退到全量包下载（现有逻辑）
```

### 6.2 新增下载物与信任边界（关键）

差分更新引入一个**新下载物**（`.patch` 文件），必须纳入 `update_trust.go` 既有的信任模型，
**不能开“只靠 TLS”的口子**：

1. patch 的 `url` 必须过 **https 白名单**（与 `rest.zip` 同待遇，重定向也逐跳校验）；
2. patch 下载后必须过 **sha256 校验**（`patches[].sha256` 必填，缺失即视为“本次无可用
   差分”，直接回退全量包，而不是降级信任）；
3. **合成出来的 exe 必须再校验 sha256 == 全量清单里的 `sha256` 值**——这是最终判据，
   只要对不上就丢弃、回退全量包。

> 安全结论：差分更新**不新增信任层级**，只是多了一个下载物；最终仍以“合成 exe 的
> sha256 对上”为准，`fail closed` 原则不变。`update_trust.go` 的审计注释需同步补充
> 这一条。

### 6.3 需要新增/改动的位置（实现阶段）

- `update.go`：`StartUpdate` 里在“下载全量包”之前插入“尝试差分”分支；
  新增 patch 下载 + bspatch 合成 + 合成结果校验 + 失败回退逻辑。
- `update_trust.go`：新增 patch 体积上限常量（如 `maxPatchBytes`，可比全量上限小）、
  patch 的 https/sha256 校验复用现有 `checkUpdateURL` / `verifySHA256`。
- `versionInfo` 结构体：新增 `Patches []versionPatch` 字段。
- `versionPatch` 结构体：`From` / `URL` / `SHA256` / `Size`。
- 补丁合成实现：引入纯 Go bspatch（或自实现），无 cgo、无 DLL。

---

## 7. 打包脚本改动（make_portable.py）

### 7.1 需要新增

1. **生成 patch 文件**：在 `make_update_zip` 之后（拿到新旧 exe 后），对“最近 N 个旧版
   的 exe”逐个跑 bsdiff 生成 `v<旧>-v<新>.patch`。
   - 旧版 exe 从哪来？两种策略（二选一，实现阶段定）：
     - **A. 从 git 历史 / release 目录取旧 exe**：需要保留历史 exe 产物；
     - **B. 从 `release/WorkerRest-<旧tag>/WorkerRest-portable/rest.exe` 取**：若历史
       发布目录仍在，直接复用。
   - `version.py` 已有 `prev_tag()` 可拿到“上一个 tag”，配合 `latest_tag()` 可遍历
     最近 N 个版本。
2. **计算 patch 的 sha256 + size**，写入 `version.json` 的 `patches` 数组。
3. **验证**：`verify_portable.py` 增加对 `patches` 字段与 `patches/*.patch` 文件一致性的
   断言（patch 存在、sha256 对得上、from 版本号合法）。

### 7.2 需要保留

- 全量 `rest.zip` 照旧生成（兜底），`version.json` 的 `sha256`/`size` 仍指全量包。
- 现有 `check_winres` / `check_exe_freshness` / `check_notices` 门禁不变。

### 7.3 需要明确的新策略（实现阶段拍板）

- **保留多少个历史 patch**（`N`）：建议 2～3 个，更老版本直接走全量包，控制
  `patches/` 目录与 `version.json` 体积。
- **旧 exe 来源**：见 7.1 的策略 A/B。

---

## 8. 收益与风险

### 8.1 收益

- 常规改动：更新下载量 **6 MiB → 几十~几百 KiB**。
- 服务器零新增成本（纯静态）。
- 与现有安全模型、版本规范完全兼容。

### 8.2 风险与代价

| 风险 | 说明 | 缓解 |
|------|------|------|
| patch 合成失败 | 旧 exe 损坏、被篡改、版本不对 | sha256 兜底，失败即回退全量包 |
| patch 体积波动 | 大改动（升级 Wails）时 patch 变大甚至接近全量 | 设定阈值：patch 大于某比例时直接不生成、只给全量 |
| 客户端复杂度上升 | 新增分支 + 回退状态机 | 单测覆盖“有 patch / 无 patch / 合成失败”三态 |
| 内存开销 | bspatch 合成需载入文件 | 13 MiB 可接受，无需处理 |
| 信任模型需重审 | 新增下载物 | 复用 https+sha256，不降级（见 §6.2） |

### 8.3 一个明确的“不做”边界

- **不做**：拆分第三方库为 DLL（Go 静态链接，无落点）。
- **不做**：拆前端资源出 exe 改为远程加载（收益低、复杂度高、且破坏当前单文件便携性）。
- **可选后续**：差分更新稳定后，可再评估 UPX 压缩 exe（省 30–50% 体积但可能被杀软
  误报、增启动耗时），与差分更新正交、可叠加。

---

## 9. 实现顺序建议（待批准后才动手）

1. 引入 bsdiff/bspatch 工具链（打包机用 CLI，客户端用纯 Go bspatch）。
2. `make_portable.py` 生成 patch + 扩展 `version.json`。
3. `versionInfo` / `versionPatch` 结构体 + `update_trust.go` 新增 patch 校验。
4. `update.go` 的 `StartUpdate` 差分分支 + 回退状态机。
5. 单测（三态）+ `verify_portable.py` 断言 + 冒烟。
6. 端到端验证：造两个版本 exe，走一遍“有 patch / 无 patch / 合成失败”三路径。

---

## 10. 待确认问题（实现前需拍板）

1. **保留最近几个历史版本的 patch**（N = ?）？
2. **旧 exe 来源策略**：从历史 `release/` 目录取，还是从 git 归档里取？
3. **bsdiff 实现形态**：客户端用纯 Go 实现（无 DLL，推荐），打包机用官方 C CLI 或纯 Go？
4. **patch 体积阈值**：patch 超过全量包多大比例时就干脆不提供差分（直接全量）？
