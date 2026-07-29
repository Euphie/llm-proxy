# Profile 控制台设计

日期：2026-07-29

状态：已完成交互设计确认，待文档审阅

## 目标

将 llm-proxy 从单个 YAML 激活单一 Provider 的代理，升级为适合个人服务器部署的 Profile 控制台：

- 在 Web 管理后台维护多个 Profile；
- 通过简单 URL 选择 Profile；
- Profile 保存后立即热生效；
- 使用同一个 SQLite 保存配置、管理会话和 Token 用量；
- 不保存上游 API Key，只透传客户端鉴权请求头；
- 根据 Profile 即时生成 Claude Code 的项目级或全局配置示例。

## 非目标

- 不提供多用户、角色或租户隔离；
- 不管理、加密或分发上游 API Key；
- 不保存 Agent 模板；
- 不拆分控制面和代理节点；
- 不兼容或自动导入现有 `config.yaml`、`CONFIG_FILE` 和 `stats.db`。

## 产品形态

llm-proxy 保持单个 Go 进程和单个可执行文件，内嵌管理后台静态资源。运行时由以下组件组成：

- `ProfileStore`：SQLite 中 Profile 的持久化接口；
- `ProfileRegistry`：按 slug 保存不可变运行时快照；
- `ProfileRouter`：选择默认或指定 Profile，并移除 Profile 前缀；
- `ProxyRuntime`：执行视觉预处理、容错重试、流式转发和用量解析；
- `AdminServer`：登录、Profile 管理、统计和配置生成；
- `UsageStore`：记录并查询主请求和影子识图请求的 Token 用量。

启动时先打开数据库、执行迁移、加载 Profile，再发布完整的内存快照。数据库无法打开、迁移失败或已有 Profile 无法解析时，进程拒绝启动。

## 路由设计

管理面统一放在 `/_admin`：

```text
/_admin/                 管理后台
/_admin/login            登录
/_admin/api/**            管理 API
/_admin/stats             Token 统计
/_admin/system            系统信息
```

数据面使用 Profile 前缀：

```text
/v1/**                    默认 Profile
/{profile}/**             指定 Profile
```

示例：

| 客户端路径 | Profile | 上游路径 |
|---|---|---|
| `/v1/messages` | 默认 | `/v1/messages` |
| `/coding/v1/messages` | `coding` | `/v1/messages` |
| `/openai/v1/chat/completions` | `openai` | `/v1/chat/completions` |
| `/openai/v1/responses` | `openai` | `/v1/responses` |

Agent 或 SDK 负责追加协议路径。llm-proxy 只识别 Profile 前缀，后续路径、原始查询参数和 HTTP 方法原样保留。

Profile slug 必须匹配：

```text
[a-z0-9][a-z0-9-]{0,62}
```

`v1` 为保留名称，以下划线开头的名称一律禁止。`/_admin/**` 必须先于 Profile 路由匹配。

Profile upstream 只允许 `http` 或 `https`，不能携带 URL 用户名或密码。转发时将上游已有路径与请求剩余路径拼接，不使用会清理或重写转义字符的文件路径函数。

## 协议语义

每个 Profile 显式选择：

```text
anthropic
openai
```

协议用于：

- 校验 Profile 功能；
- 选择 Token 用量解析器；
- 选择配置生成器；
- 决定是否允许图片预处理。

代理不维护 API endpoint 白名单，因此新增上游端点不需要修改路由。图片预处理首版仍仅支持 Anthropic `POST /v1/messages`。

## 启动配置

应用只保留：

```text
LISTEN=:8080
DATA_DIR=./data
```

数据库固定为：

```text
${DATA_DIR}/llm-proxy.db
```

Docker Compose 的 `.env` 使用：

```env
HOST=127.0.0.1
PORT=8087
DATA_DIR=./data
```

Compose 将宿主机 `${DATA_DIR}` 挂载到容器 `/app/data`，容器内设置：

```text
LISTEN=:8080
DATA_DIR=/app/data
```

`DATA_DIR` 不存在时自动创建。数据库文件权限设为 `0600`。

## 数据模型

数据库启用 WAL、foreign keys 和合理的 busy timeout。Schema 版本使用 SQLite `PRAGMA user_version` 管理。

### `app_settings`

单行应用设置：

- `default_profile_id`
- `created_at`
- `updated_at`

### `admin_account`

单行管理员账号：

- `username`，固定为 `admin`
- `password_hash`
- `must_change_password`
- `auth_version`
- `updated_at`

`auth_version` 在修改密码时递增，用于撤销旧 Session。

### `admin_sessions`

- `token_hash`
- `auth_version`
- `created_at`
- `expires_at`

Cookie 保存随机明文 Token，数据库只保存哈希。过期 Session 在登录、鉴权或后台维护时清理。

### `profiles`

- `id`
- `slug`，唯一
- `display_name`
- `enabled`
- `config_json`
- `created_at`
- `updated_at`

默认 Profile 只由 `app_settings.default_profile_id` 指定，避免出现多个默认 Profile。

`config_json` 带内部版本号并保存：

```text
protocol
upstream
vision
  enabled
  model
  max_tokens
  timeout
  max_concurrency
  cache_ttl
  cache_max_entries
  prompt
overload_rules[]
  status
  body_contains
  max_retries
  delay
  jitter
```

### `usage`

在现有 Token 字段基础上增加：

- `profile_id`，可为空；
- `profile_slug`，请求发生时的快照；
- `protocol`；
- `request_kind`，`main` 或 `vision`。

删除 Profile 不删除历史 usage。统计继续显示记录时的 slug。

## 初始化与认证

空数据库创建管理员：

```text
用户名：admin
密码：admin
must_change_password=true
```

首次流程：

```text
admin/admin 登录
→ 强制修改密码
→ 创建第一个 Profile
→ 设置为默认
→ 开始接受代理请求
```

完成修改密码并创建默认 Profile 前，数据面统一返回：

```text
503 proxy_not_configured
```

初始密码状态下，管理后台只允许登录、修改密码和退出。Profile、统计、配置生成等接口不可访问。

用户已接受公网初始密码可能被抢先登录的风险。实现仍需提供以下缓解：

- 启动日志和页面持续显示高风险提示；
- 登录按全局和来源地址同时限速；
- 正式密码至少 10 个字符；
- 密码使用 Argon2id 哈希；
- 修改密码后递增 `auth_version` 并撤销全部 Session；
- Session Cookie 使用 `HttpOnly`、`SameSite=Strict`，HTTPS 下使用 `Secure`；
- 所有管理写操作校验 CSRF Token；
- 管理页面设置 CSP、禁止 iframe 嵌入并禁用 MIME 嗅探。

Session 有固定过期时间，不提供“永久登录”。

## Profile 管理

管理首页以卡片展示：

- 名称和 slug；
- 协议和 upstream；
- 默认、启用和视觉状态；
- 最近请求量与 Token 用量；
- 编辑、复制、禁用和删除操作。

编辑页面分为：

1. 基础配置：名称、slug、协议、upstream、启用状态；
2. 视觉增强：启用、模型和折叠的高级参数；
3. 容错规则：状态码、正文匹配、次数和退避时间；
4. 配置生成：即时生成客户端配置。

默认 Profile 不能直接删除。必须在同一个事务内将另一个已启用 Profile 设置为默认后再删除。

修改 slug 会改变 Agent URL，保存前必须明确提示。复制 Profile 时生成新的 slug，复制运行配置但不复制统计数据。

系统页面可以将全部 Profile 导出为不含统计和内部 ID 的 JSON 文件。
导出内容不包含任何鉴权信息。首版不提供导入功能。

## 配置热生效

保存流程：

1. 解析并校验管理 API 输入；
2. 构建新的 `ProxyRuntime`；
3. 在 SQLite 事务中保存 Profile；
4. 事务提交成功后原子替换 `ProfileRegistry` 快照；
5. 新请求使用新快照，执行中的请求继续持有旧快照。

校验或数据库保存失败时，不修改内存快照。Profile 的运行配置整体保存，不允许视觉配置和重试规则分批生效。

修改会影响视觉行为的字段时，新运行时使用新的视觉缓存。旧缓存随旧运行时不再被请求引用后释放。

## 请求转发

请求流程：

1. 路由选择 Profile；
2. 读取运行时快照；
3. 过滤 hop-by-hop Header；
4. 必要时执行图片预处理；
5. 按容错规则请求上游；
6. 流式返回响应；
7. 异步记录用量。

主请求透传所有端到端 Header，不记录鉴权 Header 内容。影子识图请求只透传：

- `Authorization`
- `X-Api-Key`
- `Anthropic-Version`

`Anthropic-Beta` 不透传。

## 错误语义

| 场景 | 状态码 | 错误码或行为 |
|---|---:|---|
| 未完成初始化 | 503 | `proxy_not_configured` |
| Profile 不存在 | 404 | `profile_not_found` |
| Profile 已禁用 | 503 | `profile_disabled` |
| 管理输入无效 | 422 | `validation_error` |
| 上游网络错误 | 502 | `upstream_error` |
| 非重试型上游 HTTP 错误 | 上游状态码 | 上游响应原样返回 |
| 数据库保存失败 | 500 | 保留旧运行配置 |

管理 API 使用稳定 JSON 错误结构。数据面由 llm-proxy 自身产生的错误也使用 JSON；上游错误保持上游 Content-Type 和正文。

## Token 统计

统计入口移动到 `/_admin/stats`，不再提供独立公开的 `/stats` 和 `/stats/data`。

支持筛选：

- Profile；
- 协议；
- 模型；
- 时间范围；
- 主请求或影子识图请求。

统计写入失败只记录脱敏日志，不中断代理响应。

## 配置生成器

Agent 不作为数据库实体保存。生成器直接基于当前 Profile 即时生成配置，页面刷新后丢弃临时输入。

Anthropic Profile 支持：

- 全局 `~/.claude/settings.json`；
- 项目共享 `.claude/settings.json`；
- 项目私有 `.claude/settings.local.json`；
- Shell `export` 片段。

页面默认使用浏览器当前 origin 和 Profile slug 生成 `ANTHROPIC_BASE_URL`。管理员可临时修改公开域名。

下列模型映射由管理员临时填写，不保存到 Profile：

- `ANTHROPIC_DEFAULT_HAIKU_MODEL`
- `ANTHROPIC_DEFAULT_SONNET_MODEL`
- `ANTHROPIC_DEFAULT_OPUS_MODEL`

真实 API Key 不允许输入管理后台。输出仅提供密钥变量的占位符和本地设置提示。生成结果可以复制或下载，服务端不写入客户端文件系统。

OpenAI Profile 首版生成通用客户端片段，不伪装成 Claude Code 配置。
Profile 前缀为 `https://proxy.example.com/openai` 时，OpenAI API base
生成为 `https://proxy.example.com/openai/v1`，由 SDK 继续追加
`/responses` 或 `/chat/completions`。

## 管理 API 边界

管理 API 全部位于 `/_admin/api`，至少包含：

```text
POST   /_admin/api/login
POST   /_admin/api/logout
POST   /_admin/api/password
GET    /_admin/api/profiles
POST   /_admin/api/profiles
GET    /_admin/api/profiles/{id}
PUT    /_admin/api/profiles/{id}
POST   /_admin/api/profiles/{id}/copy
DELETE /_admin/api/profiles/{id}
PUT    /_admin/api/default-profile
GET    /_admin/api/stats
GET    /_admin/api/system
```

配置生成在浏览器完成，不需要服务端保存或生成 API。

## 实施分段

该设计按依赖关系分为三个连续阶段：

1. 数据与运行时：SQLite schema、ProfileStore、Registry、ProfileRouter、热切换和统计字段；
2. 管理 API 与认证：初始化、密码、Session、CSRF、Profile CRUD 和统计查询；
3. 管理 UI 与生成器：首次向导、Profile 表单、统计页面和配置输出。

每个阶段完成后都必须保持代理测试可运行，第三阶段结束后再移除 YAML 入口和旧统计页面。

## 测试策略

### 单元测试

- Profile slug、协议、upstream 和默认值；
- 默认与指定 Profile 路由；
- 前缀移除、转义路径和查询参数保留；
- hop-by-hop Header 过滤；
- Claude Code 四种配置输出；
- 密码哈希、Session、CSRF 和登录限速。

### 数据库测试

- 空数据库初始化；
- schema migration；
- Profile 事务保存；
- 默认 Profile 原子切换；
- 删除与历史统计保留；
- 并发读取和 WAL 行为。

### 代理集成测试

- Anthropic `/v1/messages`；
- OpenAI `/v1/chat/completions` 和 `/v1/responses`；
- 流式响应；
- 图片预处理；
- 容错重试；
- 热切换期间的在途请求。

### 管理后台端到端测试

- 首次登录和强制改密；
- 初始化向导；
- Profile 创建、修改、复制、禁用和删除；
- 配置复制与下载；
- 统计筛选；
- Session 失效和未授权访问。

构建和测试优先在 Docker 隔离环境执行。

## 验收标准

- 仅通过 `LISTEN` 和 `DATA_DIR` 可以启动新实例；
- 空数据库能完成首次初始化；
- 默认 Profile 和路径 Profile 均能正确转发；
- Anthropic 与 OpenAI 协议路径不会被错误改写；
- Profile 保存无需重启且不打断在途请求；
- 数据库不包含上游 API Key 或 Authorization；
- 管理接口未经登录不可访问；
- 首次密码未修改时代理不可用；
- 配置生成器能输出四种 Claude Code 配置形式；
- Profile 维度统计准确；
- 完整单元、竞态、集成和端到端测试通过。
