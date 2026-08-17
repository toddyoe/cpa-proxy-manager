# 代理批量设置插件设计

## 文档状态

| 阶段 | 状态 | 结论 |
|------|------|------|
| 产品评审 / UI 评审 / 技术方案评审 | 已通过（2026-08-16） | 本文 A/B/C 节；UI 原型为 Penpot 项目 `cpa-proxy-manager`「界面设计」页面 |
| 测试用例评审 | 跳过（用户决定） | 以单元测试与功能自测代替 |
| 实现与联调 / 验收 | 已通过（2026-08-17） | 见 D 节；产物 `cpa-proxy-manager-v0.2.2.dll` |

---

## A. 产品设计

### A.1 背景与问题

CLIProxyAPI（下称 CPA）支持为每个 OAuth 凭据配置独立的出口代理地址（认证文件根字段 `proxy_url`，按凭据覆盖上游请求、token 刷新与 WebSocket 流量）。配合 Resin 粘性代理池（自建正向代理池，同一账号固定使用同一出口 IP，认证格式为 `平台.账号:密码`），可以为每个上游账号绑定稳定出口 IP。凭据规模达到千级时，`proxy_url` 没有任何批量管理手段——CPA 管理面板只支持逐个编辑，本插件用来填补这一空白。

### A.2 目标与非目标

**目标**（可验证）：

1. 在 CPA 管理面板内提供「代理批量设置」页面，单页完成千级（1000+）凭据的搜索、筛选、勾选与全选。
2. 用命名模板与占位符为每个凭据生成代理地址；模板与占位符可增删改，配置可持久化保存。
3. 批量设置与批量清空在执行前都要先确认数量（含跳过统计），执行过程中可以看到进度，也可以随时停止；失败原因只在界面上显示，不写入持久化存储。
4. 全程不修改 CPA 核心：部署一个插件动态库即可使用，CPA 升级无需重编插件。

**非目标（明确不做）**：

- 不做「当前值与模板值的比对状态」与漂移检测——预览的跳过统计已覆盖该工作流。
- 不做中英双语与语言切换；界面文案以 i18n 结构组织（单一中文语言包），为多语言预留扩展空间。
- 不做定时自动同步、执行历史、结果导出、多 CPA 实例管理。
- 不做指定统一地址的独立入口——无变量的模板天然生成统一地址。
- 不修改 CPA 核心代码，不接管凭据调度。

### A.3 角色与权限

本插件只有一类用户：CPA 实例的管理员。页面入口位于 CPA 管理面板，受面板登录与管理密钥体系保护。插件内部的权限分两级：页面请求凭据清单时，管理密钥随面板转发的请求头送达插件；插件读取凭据现值则经插件应用二进制接口（ABI，Application Binary Interface）的宿主回调完成，不需要管理密钥。写入（批量设置与清空）通过 CPA 管理 API 完成，管理密钥从转发到插件路由的请求头中自动提取，仅保存在内存中，不写入任何持久化文件，管理员全程无感知。

### A.4 主用户旅程

主路径六步，第 2 步为一次性配置：

1. 打开管理面板中的「代理批量设置」，凭据列表的第一页数据自动加载（服务端分页，默认每页 50 条）。
2. 首次使用时打开「模板管理」，配置代理模板与自定义占位符；保存即时生效，不触发任何凭据请求。
3. 在工具行通过搜索与类型筛选缩小范围；行内勾选目标账号，或勾选表头的「全选」标记，将整个筛选结果作为执行范围（行内取消勾选记为排除项，底部显示「已选全部 N 项（排除 M）」）。
4. 点击「预览」查看只读对照表（当前值与设置后地址的对比），弹窗独立分页，由服务端按最新配置渲染。
5. 点击「批量设置」（或「清空代理」），确认弹层显示服务端实时计算的数量与跳过统计，确认后执行。
6. 观察进度条至完成，失败项（如有）按原因显示，可重新筛选后再次执行。

### A.5 业务规则

**写入与清空语义**。设置操作向 `PATCH /v0/management/auth-files/fields` 提交渲染后的地址；清空操作必须提交空字符串而非 null——CPA 的同步逻辑要求 metadata 值必须是字符串，null 会被静默忽略、`auth.ProxyURL` 残留旧值，这是本插件必须遵守的硬约束。

**选择语义**。批量执行的作用域有两种：手动勾选集合（显式索引列表），或全选标记（当前筛选全集减去排除项）。确认弹窗的数字与最终执行目标由服务端同一个解析函数产出，从机制上保证界面确认的数字就是最终执行的集合；勾选项在执行时已不存在的，整体返回 400 并列出缺失项。

**跳过规则**。批量设置跳过无法渲染的账号，跳过原因分为三类，界面按类别显示对应文案：占位符取不到值、渲染出的地址不合法、模板地址为空。批量清空跳过当前未设置代理的账号。跳过数量在确认弹层与执行结果中显式计数。

**模板与占位符规则**。模板地址必须是合法代理 URL（http、https、socks5、socks5h 四种协议），占位符花括号在校验时以哑值替换；模板名不可为空、不可重名；自定义占位符名不可为空、不可重名、仅限字母数字下划线。内置占位符（`{account}` 邮箱前缀、`{email}` 完整邮箱、`{provider}` 账号类型）硬编码在插件中，作为占位符解析的兜底，同名自定义占位符优先于内置；默认配置不预置任何自定义占位符，初始状态即为「模板仅使用内置占位符」。最后一个模板不可删除；清空自定义占位符需确认后执行。

**执行规则**。执行期间锁定模板下拉、模板管理与刷新入口；停止指令立即停止派发新任务，已完成写入保留；单项失败（凭据被并发删除、超时等）的原因只在界面上显示，不写入持久化存储；写入并发数可配置（默认 8，范围 1–64）；重复对同一账号设置相同地址无副作用（幂等）。

**数据留存**。插件配置保存为单一 JSON 文件，只含配置项——模板列表、选中模板、占位符列表、写入并发数；所有中间运行结果（进度、失败明细、预览）仅存在于内存。管理密钥不写入任何持久化文件，也不写入日志。

### A.6 非功能期望

规模方面，目标支撑千级凭据：每次请求调用一次 `host.auth.list` 获取元数据，再按页直读认证文件（并发 32），千级规模下也能在亚秒内返回；不保留任何凭据缓存，外部改动下次请求即可见。兼容方面，插件 ABI 与 jshandler、grok-inspection 同一套，均以 CGO（Go 与 C 的互操作机制）编译为动态库，CPA 升级不需要重编插件。界面配色采用 CPA 管理面板主题令牌。

### A.7 命名分层

| 层 | 规则 | 取值 |
|----|------|------|
| 产品文案 | 用户可见 | 页面与菜单名「代理批量设置」；按钮「预览」「批量设置」「清空代理」「模板管理」 |
| 枚举/结构 | 领域语义 | 执行模式 `set`/`clear`；跳过原因 `placeholder_unresolvable`/`invalid_url`/`empty_template`；内置占位符 `account`/`email`/`provider` |
| HTTP | 稳定、非口号 | 插件路由前缀 `/v0/management/plugins/cpa-proxy-manager/*`；资源页 `/v0/resource/plugins/cpa-proxy-manager/status` |
| 运行时 ID | 隔离通道 | 插件标识 `cpa-proxy-manager`；配置文件 `data/cpa-proxy-manager/config.json` |

---

## B. UI/交互

### B.1 设计产物

UI 原型位于 Penpot（开源界面设计工具）项目 `cpa-proxy-manager` 的「界面设计」页面（主页面、模板管理、预览、确认四块画板），设计源文件备份于 `docs/design/penpot/`。配色采用 CPA 管理面板暖灰主题令牌。

### B.2 信息架构与动线

页面采用两步动线：工具行完成筛选（搜索防抖 300ms、类型下拉）与模板选择（模板下拉、模板管理入口），底部操作栏完成预览与批量执行。页面共有三个弹层：模板管理弹层维护模板库与占位符，预览弹层展示只读对照表（作用于筛选结果而非勾选集，独立分页），确认弹层显示数量与跳过统计（设置与清空共用）。

### B.3 关键交互与状态

表格与预览弹窗均为服务端分页（20/50/100/200 四档，默认 50），筛选或翻页即带参重新请求数据。各项交互与状态的约定如下：

- 表头复选框为全选标记；行内取消勾选记为排除项，此时表头呈半选状态。
- 无法套用当前模板的行禁用勾选，悬浮提示按跳过原因显示对应文案。
- 现值读取失败的行显示「读取失败」，与「未设置」区分。
- 执行期间锁定模板入口并隐藏执行按钮，显示进度条与停止按钮。
- 筛选无匹配结果时提供「清除筛选」直达链接。
- 失败明细仅在界面显示，处理完成或关闭页面即消失。
- 数据未就绪时下拉框显示「加载中…」占位，不出现空白控件。
- 请求收到 401 时自动重新提取面板密钥并重试一次。

### B.4 组件复用策略

按钮（主要、次要、危险三态）、数据表格、分页器、大小两种弹层、输入框与下拉框，均沿用 CPA 管理面板现有组件的样式，不新增视觉体系。

### B.5 不做的界面功能

状态筛选标签页、失败重试按钮、禁用标记的独立列均为暂缓项，首版不做。

---

## C. 技术方案

### C.1 总体架构与模块

插件为独立 Go 仓库，编译为 CGO 动态库，ABI 方法集与 jshandler、grok-inspection 采用同一套（`plugin.register`、`plugin.reconfigure`、`management.register`、`management.handle`、`shutdown`）。包结构：

```text
cpa-proxy-manager/
├── main.go           // ABI 入口、插件注册、管理路由分发与资源页
├── engine.go         // 批次生命周期：互斥、并发执行、协作停止、进度快照
├── render.go         // 模板渲染与校验：占位符解析（自定义优先、内置兜底）、URL 合法性
├── credentials.go    // 凭据数据面：共享筛选、分页参数与页切片、按路径直读认证文件、批次目标解析
├── mgmt.go           // CPA 管理 API 客户端：回环基址、密钥提取、PATCH fields
├── config.go         // config.json 原子读写与校验
├── ui.go             // 管理页面单 HTML 内嵌（go:embed）
├── i18n.go           // 中文文案包
├── cgo_bridge.go / host.go / host_gate.go  // CGO 桥接、信封编解码、宿主调用并发上限控制（同时最多 16 个）
└── cpasdk/           // vendored ABI 类型（与 grok-inspection 同源）
```

依赖约束：`render.go` 为纯函数模块，不依赖网络与宿主回调；`engine.go` 依赖 render、credentials、mgmt；`ui.go` 只经管理路由与前端交互；配置读写集中在 `config.go`，engine 启动批次时取配置快照，执行期间的配置变更不影响进行中批次。

关键方法签名：

```go
// credentials.go
func ParsePageFilter(query url.Values) PageFilter                       // 读取并钳制 q/provider/page/size
func MatchFilter(entry HostAuthFileEntry, q, provider string) bool      // 唯一筛选谓词：provider 精确、q 对邮箱或文件名不区分大小写包含
func Hydrate(entries []HostAuthFileEntry, tpl Template, phs []Placeholder) []CredentialView // 并发直读文件并渲染
func ResolveBatch(req batchRequest) ([]EngineTarget, PlanCounts, error)  // plan 与 apply 共用的选择解析

// engine.go
func (e *engineState) Start(mode string, targets []EngineTarget, concurrency int, headers http.Header) error // 执行中返回 ErrBusy
func (e *engineState) Stop()                                             // 协作停止：不再派发，已完成保留
func (e *engineState) Snapshot() Status                                  // 进度、成功数、失败明细

// render.go
func RenderTemplate(urlTpl string, phs []Placeholder, cred Credential) string // 占位符缺失或 URL 非法时返回空串
func ValidateTemplateURL(tpl string) bool                                // 花括号哑值替换后校验四种代理协议

// mgmt.go
func PatchProxyURL(ctx context.Context, fileName, value string, headers http.Header) error // 空字符串即清空
```

### C.2 关键决策与依据

**写入通道走 CPA 管理 API**。插件以回环 HTTP 调用 `PATCH /v0/management/auth-files/fields`，该接口在同一次调用内完成三件事：把 metadata 字段提升为运行时字段、运行时热生效、写入磁盘；空字符串语义天然覆盖清空；请求由 CPA 进程自身发出。备选的 `host.auth.save` 整文件回写存在运行时提升缺口（`buildAuthFromFileData` 不提升 ProxyURL，需等 CPA 的文件监视器异步重新加载后才与文件一致），且与 token 刷新的并发写互相覆盖，因此不采用。

**凭据现值按路径直读**。`host.auth.list` 提供元数据与认证文件路径，插件与 CPA 同进程，按路径 `os.ReadFile` 是纯本地磁盘读取；`host.auth.get` 仅在条目无路径或读取失败时兜底。每次请求实时读取、不保留缓存——千级小文件实测亚秒完成，缓存与增量校验均无必要，也避免了缓存导致外部改动在下一次请求前不可见的问题。

**配置路由命名 `/settings`**。宿主注册了内置路由 `GET/PUT/PATCH /plugins/:id/config`（服务插件实例启停配置），会遮蔽插件同名自声明路由；插件配置路由因此命名为 `/settings`。

**共享筛选谓词**。表格、预览、执行三条路径全部调用服务端同一个 `MatchFilter`，前端不做本地过滤，从机制上保证界面展示的筛选结果就是实际执行的目标集合。

**后端异步执行，前端轮询进度**。渲染只在插件后端完成，前端仅展示并轮询（接口返回 202 后由前端每 600 毫秒轮询一次），执行不与页面生命周期绑定。

### C.3 数据与接口

配置文件 `data/cpa-proxy-manager/config.json` 只含配置项：

```json
{
  "templates": [{ "id": "t1", "name": "Resin 默认池", "url": "http://Default.{account}:token@resin-host:2260" }],
  "selected_template": "t1",
  "placeholders": [],
  "write_concurrency": 8
}
```

默认配置只含一个模板且不预置自定义占位符。

### 接口定义

**通用约定**：管理路由基址为 `/v0/management/plugins/cpa-proxy-manager`，下文各接口路径均相对该基址；资源页为 `/v0/resource/plugins/cpa-proxy-manager/status`。除资源页外全部接口需带面板管理密钥头 `Authorization: Bearer <key>`；写接口请求体为 JSON（`Content-Type: application/json`）。错误响应统一为 `{"error": "<原因>"}`，状态码约定：400 参数或配置校验失败、401 管理密钥缺失或失效、409 已有批次执行中、503 宿主回调不可用。

#### GET /credentials — 凭据清单分页

| 参数 | 位置 | 类型 | 必填 | 说明 |
|------|------|------|------|------|
| q | query | string | 否 | 对邮箱或文件名不区分大小写做包含匹配，默认空 |
| provider | query | string | 否 | 类型精确匹配，空为全部 |
| page | query | int | 否 | 页码，≥1，默认 1 |
| size | query | int | 否 | 每页条数，1–500，默认 50 |

```json
{
  "total": 1706, "page": 1, "size": 50, "pages": 35,
  "providers": ["claude", "gemini"],
  "template_id": "t1", "template_name": "Resin 默认池",
  "placeholder_samples": { "organization_uuid": "org-9f2a" },
  "rows": [
    {
      "auth_index": "f1d1b06fcdbb9886", "name": "alpha.json", "provider": "claude",
      "email": "alpha@example.com", "account": "alpha",
      "proxy_url": "http://old-alpha:1080", "proxy_known": true,
      "renderable": true
    }
  ]
}
```

行字段说明：`proxy_known` 为 false 表示现值读取失败（此时 `proxy_url` 为空）；`renderable` 为 false 表示该行无法套用当前模板，原因见 `skip_reason`（取值 `placeholder_unresolvable` / `invalid_url` / `empty_template`，可解析时省略）；`email`、`account`、`disabled`（仅真值）与 `skip_reason` 为空时省略；本接口不含渲染结果字符串。

#### GET /preview — 渲染预览分页

入参与 `/credentials` 完全相同。响应字段同上，行内另含渲染结果：

```json
{
  "total": 1706, "page": 1, "size": 50, "pages": 35,
  "rows": [
    {
      "auth_index": "f1d1b06fcdbb9886", "name": "alpha.json", "provider": "claude",
      "email": "alpha@example.com", "account": "alpha",
      "proxy_url": "http://old-alpha:1080", "proxy_known": true,
      "renderable": true,
      "preview": "http://Default.alpha:token@g7-et.example:2260"
    }
  ]
}
```

#### POST /plan — 执行前确认数字

| 参数 | 位置 | 类型 | 必填 | 说明 |
|------|------|------|------|------|
| mode | body | string | 是 | `set` 或 `clear` |
| indexes | body | string[] | 二选一 | 手动勾选模式：auth_index 数组 |
| q | body | string | 二选一 | 全选标记模式：搜索词 |
| provider | body | string | 二选一 | 全选标记模式：类型筛选 |
| exclude | body | string[] | 否 | 全选标记模式下被排除的 auth_index 数组 |

`indexes` 非空时按显式勾选解析，忽略 `q`/`provider`/`exclude`；否则按筛选全集减排除项解析。

```json
{ "matched": 120, "will_do": 103, "skipped": 17 }
```

#### POST /apply — 启动批量执行

入参与 `/plan` 完全相同。校验通过返回 202 并异步执行：

```json
{ "ok": true, "accepted": true, "mode": "set", "total": 103, "skipped": 17 }
```

错误：400 模式非法或请求体不是 JSON；400 无可执行目标；400 `indexes` 中包含已被删除的账号（错误信息列出缺失项）；409 已有批次执行中。

#### POST /stop — 停止派发

该接口无入参。调用后立即停止派发新任务（已完成的写入保留），返回当前状态快照（结构同 `/status`）。

#### GET /status — 执行进度轮询

该接口无入参。

```json
{
  "running": true, "mode": "set", "total": 103, "done": 61, "ok": 60, "failed": 1,
  "finished": false,
  "failures": [ { "name": "alpha@example.com · alpha.json", "reason": "写入失败：HTTP 404" } ]
}
```

`mode` 与 `stopped` 仅在真值时出现；无失败时 `failures` 为 `null`。

#### GET /settings — 读取配置

该接口无入参，返回完整配置对象，结构与上文配置文件示例一致。

#### PUT /settings — 保存配置

| 参数 | 位置 | 类型 | 必填 | 说明 |
|------|------|------|------|------|
| templates | body | array | 是 | 模板列表，每项含 `id`、`name`、`url`；不可为空，名称与 id 不可重复；`url` 为空或为合法代理 URL（http/https/socks5/socks5h，占位符按哑值校验） |
| selected_template | body | string | 是 | 选中模板的 id，必须存在于列表中 |
| placeholders | body | array | 否 | 自定义占位符，每项含 `name`、`path`；名称不可为空、不可重复、仅限字母数字下划线；`path` 不可为空 |
| write_concurrency | body | int | 否 | 写入并发数，1–64，缺省 8 |

```json
{ "ok": true, "message": "配置已保存", "config": { "templates": [], "selected_template": "", "placeholders": [], "write_concurrency": 8 } }
```

校验失败返回 400，`error` 为具体原因；成功后将配置原子写入 `data/cpa-proxy-manager/config.json`，立即对后续请求生效。

双数据源说明：凭据现值的真相源是 CPA 认证文件，每次请求实时读取；模板与占位符配置的真相源是 config.json。两源互不写入。

### C.4 核心时序

场景一：页面加载与数据获取。

```mermaid
sequenceDiagram
    participant U as 管理页面
    participant P as 插件
    participant H as CPA 宿主
    U->>P: GET /credentials（q、provider、page、size）
    P->>H: host.auth.list
    H-->>P: 凭据条目（名称、类型、账号、文件路径）
    P->>P: 共享筛选 · 切出当前页条目
    loop 仅当前页条目 · 并发 32
        P->>P: 按路径直读认证文件（根字段含 proxy_url）
    end
    P-->>U: 当前页行、总数、类型清单、占位符示例
```

场景二：批量设置执行。

```mermaid
sequenceDiagram
    participant U as 管理页面
    participant P as 插件
    participant A as CPA 管理 API
    U->>P: POST /plan（mode 为 set，勾选索引或全选标记加排除项）
    P-->>U: matched、will_do、skipped（确认弹窗数字）
    U->>P: POST /apply（同一选择载荷）
    P-->>U: 202 Accepted
    loop 写并发可配置 · 期间可停止
        P->>A: PATCH /v0/management/auth-files/fields
        A-->>P: 200（热生效并落盘）
    end
    U->>P: GET /status（轻量轮询）
    P-->>U: 进度、成功数、失败明细
```

场景三为批量清空，与场景二共用执行通道，差异有两点：执行范围只包含当前已设置代理的凭据；PATCH 提交的地址为空字符串。

场景四：配置保存与按需预览。

```mermaid
sequenceDiagram
    participant U as 管理页面
    participant P as 插件
    U->>P: PUT /settings（模板、占位符、并发数）
    P->>P: 校验（URL 合法性、占位符重名、并发范围）
    P->>P: 原子写入 data/cpa-proxy-manager/config.json
    P-->>U: 200（校验失败返回 400 与错误信息）
    Note over U: 保存不触发任何凭据请求
    U->>P: GET /preview（点开预览弹窗时，按最新配置渲染当前页）
```

### C.5 运行约束

| 约束 | 影响 | 处理 |
|------|------|------|
| 千级写入触发认证文件监视器（watcher）频繁重载 | 写入吞吐受 CPA 落盘制约 | 写并发可配置（默认 8），按实测结果调整 |
| 执行期间凭据被删除或改名 | 对应 PATCH 返回 404 | 记为单项失败并展示原因，不中断批次 |
| 管理密钥轮换导致写路径 401 | 批次后续写入失败 | 每次请求从转发头重新提取密钥；页面收到 401 后自动重新提取并重试一次；无面板部署时可改用环境变量提供密钥 |
| 插件数据目录按 CPA 进程的工作目录解析 | 工作目录非安装目录时配置文件落到别处 | 标准部署以安装目录为工作目录；可用 `CPA_PROXY_MANAGER_DATA_DIR` 显式指定 |

回环管理 API 基址默认 `https://127.0.0.1:8317`，支持环境变量 `CPA_PROXY_MANAGER_MGMT_BASE_URL` 覆盖。客户端按地址选择传输策略：回环地址上的 https 跳过证书校验（兼容自签证书），非回环 https 正常校验；连接失败时在 http 与 https 之间自动切换重试。

日志约束：渲染后的代理 URL 含账号名，属于管理员自己的数据，记录口径与面板展示一致；管理密钥与认证文件原文不写入日志。

---

## D. 实现与验收记录

### D.1 文件清单

| 文件 | 职责 |
|------|------|
| `main.go` | ABI 方法分发、插件注册、管理路由（credentials / preview / plan / settings / apply / stop / status）与资源页 |
| `cgo_bridge.go` / `host.go` / `host_gate.go` | CGO 桥接、信封编解码、宿主调用并发上限控制（同时最多 16 个回调，防止宿主响应慢时线程堆积） |
| `render.go` | 占位符解析（自定义优先、内置兜底）、点路径取值、模板与 URL 校验（哑值替换后校验） |
| `credentials.go` | 共享筛选、分页参数与页切片、按路径直读认证文件（host.auth.get 兜底）、批次目标解析 |
| `mgmt.go` | 回环管理 API 客户端：密钥三级解析、回环 TLS 跳过校验、http/https 切换重试、PATCH fields |
| `config.go` | config.json 原子写入、校验规则、快照 |
| `engine.go` | 批次互斥、并发执行、协作停止、进度快照 |
| `ui.html` + `ui.go` | 无框架单页管理界面（go:embed 内嵌），面板密钥复用、401 自动重试、轮询执行进度 |
| `cpasdk/` | vendored ABI 类型（与 grok-inspection 同源） |

### D.2 测试覆盖

单元测试覆盖：渲染规则（内置 / 自定义 / 覆盖内置 / 缺值跳过 / URL 协议 / 点路径子集）、配置校验与读写往返、引擎（空目标、互斥、失败隔离、协作停止）、管理路由（配置往返、参数校验、无宿主时 503、资源页、404）、共享筛选语义、分页参数钳制与页切片。全部通过；`build.ps1` 在测试失败时中断构建。

### D.3 验证记录（2026-08-17）

- 真实 CPA 实例装载：本地实例与生产实例均完成部署验证，插件文件已放入插件目录、已在 `plugins.configs` 中启用，面板入口显示正常。
- 凭据实测：按页直读认证文件亚秒返回；批量写入通过回环管理 API 保存成功，并在界面上正确回显（本地实例全链路验证了 `{account}` 占位符的渲染）。
- 密钥链路：面板存储提取密钥可用；收到 401 后会自动重新提取密钥并重试一次。
- 回环基址：本地 http 回环实测通过；生产实例的批量写入功能已通过用户的功能测试确认可用。
- 用户确认全部功能测试通过，产物为 `cpa-proxy-manager-v0.2.2.dll`。
