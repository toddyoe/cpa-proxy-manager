# cpa-proxy-manager · 代理批量设置

CLIProxyAPI（CPA）插件：在管理面板内批量设置与清空凭据的出口代理地址 `proxy_url`。支持 OAuth 认证文件，以及 `api-keys` 配置中的 API Key；面向千级凭据规模，支持命名模板、点路径自定义占位符、只读预览、可停止的批量执行与失败明细。

## 功能

- **搜索与筛选**：按账号 / 文件名 / 配置型 API Key 身份摘要搜索，按 OAuth / API Key 类型筛选，按提供商筛选，按是否已绑定代理筛选，勾选跨页保留
- **命名模板**：代理地址模板库，支持 http / https / socks5 / socks5h 四种协议
- **占位符**：内置 `{account}`（OAuth 为邮箱前缀，API Key 为身份摘要前 8 位）、`{email}`（仅 OAuth）、`{provider}`；自定义占位符按点路径取标量值（自定义优先、内置兜底）；支持光标插入
- **API Key 独立代理**：展开 `api-keys` 配置中的 `groups[].keys[]`，每个 Key 可单独设置或清除 `proxy-url`
- **预览**：只读对照表（当前值 vs 设置后），作用于筛选结果
- **批量设置 / 批量清空**：确认弹层显示数量与跳过统计，后端异步执行、可停止，失败原因仅界面显示
- **写入并发可配置**（默认 8，范围 1–64）

## 安装

### 在线安装（推荐）

CPA 插件商店添加本仓库作为第三方源，之后在管理面板的插件商店中直接安装：

```yaml
plugins:
  enabled: true
  store-sources:
    - "https://raw.githubusercontent.com/toddyoe/cpa-proxy-manager/main/registry.json"
```

支持 windows/amd64、linux/amd64、linux/arm64、darwin/amd64、darwin/arm64 五个平台，安装包带 sha256 校验。

### 手动安装

构建产物为 CGO 动态库，要求本机装有 Go 1.22+ 与 C 编译器：

```bash
./build.sh          # Linux / macOS / Windows（Git Bash）
# 或 Windows PowerShell 见 build.ps1
```

把 `dist/cpa-proxy-manager.dll`（Windows）/ `cpa-proxy-manager.so`（Linux）放入 CPA 插件目录，并在 CPA `config.yaml` 启用：

```yaml
plugins:
  enabled: true
  configs:
    cpa-proxy-manager:
      enabled: true
      priority: 1
```

重启 CPA 后，管理页面出现「代理批量设置」入口。写操作所需管理密钥从面板转发的请求头自动提取，无需手动输入。

## 配置

插件配置保存在 `data/cpa-proxy-manager/config.json`，只含配置项（模板、选中模板、占位符、写入并发数），不含任何运行状态与密钥。

环境变量（均可选）：

| 变量 | 作用 | 默认 |
|------|------|------|
| `CPA_PROXY_MANAGER_MGMT_BASE_URL` | CPA 管理 API 基址（http / https 均可） | `https://[IP]:8317` |
| `CPA_PROXY_MANAGER_DATA_DIR` | 配置文件目录 | `data/cpa-proxy-manager` |
| `MANAGEMENT_PASSWORD` / `CPA_MANAGEMENT_KEY` | 无头部署的密钥兜底 | — |

回环地址上的 https 跳过证书校验（兼容自签证书部署），非回环 https 正常校验。

## API Key 代理

API Key 清单通过 CPA v8 Management API 的 `/v8/management/config/api-keys/<provider>` 读取，支持以下提供商族：

```text
gemini, interactions, vertex, codex, claude, xai, meta, openai-compatibility
```

写入采用 `GET` 配置、`PATCH` 整组替换并使用 `ETag` / `If-Match`；配置在插件外被修改时返回 412，插件会重新读取并重试，避免静默覆盖并发修改。该功能要求 CPA 提供 v8 Management API；接口不可用时 OAuth 凭据仍可正常使用，API Key 读取失败会以页面警告显示。

### 身份与占位符

API Key 身份使用固定顺序的规范元组，以 JSON 数组编码后计算 SHA-256（UTF-8，小写十六进制）：

```text
["api_key", source, provider, name, base-url, prefix, api-key]
```

- `source` 为 `config` 或 `file`
- 完整 64 位摘要作为内部选择标识，并暴露为自定义占位符上下文 `$.sha256`
- `{account}` 与 `$.account` 使用摘要前 8 位
- 重复的完全相同的元组使用 `occurrence` 区分
- 在模板管理里新增名为 `sha256`、路径为 `$.sha256` 的自定义占位符后，即可在模板中使用 `{sha256}`

API Key 可用的安全占位符上下文：

```text
$.sha256
$.account
$.provider
$.name
$.base-url
$.prefix
$.source
```

API Key 原文不会由插件持久化，不会放入浏览器响应，也不会写入日志；插件仅在读取和写回 CPA 配置时于进程内存中解析它。管理 API 的错误响应正文也不会拼入前端可见错误，避免回显请求中的密钥。

### 设置与清空语义

- 设置：在目标 Key 上写入 `proxy-url`
- 清空：删除目标 Key 的 `proxy-url`，使其回退到组级代理，再回退到全局代理
- 仅设置了组级代理、没有 Key 级覆盖时，清空操作会跳过该 Key
- `openai-compatibility` 按配置组名称展示为 `openai-compatibility/<group>`
- v8 `PATCH` 会整组替换对应提供商配置，配置列表中的 YAML 注释可能丢失

## 设计文档

见 [docs/design/2026-08-16-proxy-manager-design.md](docs/design/2026-08-16-proxy-manager-design.md)。UI 原型为 Penpot 项目 `cpa-proxy-manager`（「界面设计」页面）。

## 注意事项

- OAuth 清空代理提交空字符串而非 `null`：CPA 对 metadata 值做字符串类型断言，`null` 会被静默忽略、旧值残留
- 渲染逻辑只在插件后端实现（`render.go`），预览与执行使用同一函数，前端不复制渲染规则
- OAuth 认证文件 JSON 不离开插件进程；API Key 配置原文同样只存在于插件内存和 CPA 进程之间
- API Key 清单每次请求实时读取，不缓存；同一提供商族的多个 Key 合并为一次带 ETag 保护的配置写入
- vendored CPA SDK 来自 `router-for-me/CLIProxyAPI` 提交 `fa11a573b252b30256bef4de952a95feee3c8c9d`，记录见 `cpasdk/UPSTREAM.md`
