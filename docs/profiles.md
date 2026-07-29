# Profiles

Profile 是一套可独立选择的代理运行配置。请在 `/_admin/profiles` 创建、复制、启停、
设为默认或删除 Profile；调用方密钥不属于 Profile，仍由每个代理请求透传。

## 标识与协议

- `slug` 必须匹配 `^[a-z0-9][a-z0-9-]{0,62}$`，最长 63 个字符。
- `v1` 是默认路由的保留值，不能作为 slug。
- 名称不能为空；slug 在数据库中唯一。
- `anthropic` 支持 Anthropic Messages、视觉增强和 Anthropic 用量解析。
- `openai` 用于 OpenAI 兼容接口；视觉增强必须关闭。

## 路由与 Upstream

| 客户端路径 | 选择方式 | 传给 Profile 的路径 |
|---|---|---|
| `/v1/...` | 当前默认 Profile | `/v1/...` |
| `/<slug>/v1/...` | 指定启用的 Profile | `/v1/...` |

查询参数和转义路径会保留。未知 slug 返回 `404`；停用的 Profile 返回 `503`；
尚未配置可用默认项时，数据面返回 `503`。

Upstream 必须是带主机的 `http` 或 `https` URL，不允许用户名、密码、查询参数或
fragment。末尾斜杠会被规范化，Profile 路由后的请求 URI 追加到 Upstream
路径。例如 Upstream 为 `https://api.example.com/base` 时，
`/coding/v1/messages?x=1` 会转发到
`https://api.example.com/base/v1/messages?x=1`。

端到端请求头会透传，连接级 hop-by-hop 头会移除。请由调用方在请求中发送
`Authorization`、`X-Api-Key` 等凭据，不要把密钥写进 Profile。

## 启用、默认项与热更新

第一个 Profile 自动成为默认项，控制台中的默认复选框保持选中且不可取消。默认
Profile 必须启用，不能直接停用；切换默认项时目标也必须启用。删除默认项时必须
同时选择另一个启用的 Profile，且系统始终保留一个有效默认项。

管理操作先在 SQLite 事务中保存，再构建并原子发布新的 Runtime Registry 快照。
并发请求只会看到完整的旧快照或新快照，不会看到半更新状态；正常修改无需重启。
发布失败时，旧的完整 Registry 快照继续服务；Coordinator 的运行时同步状态会变为
未就绪，下一次管理操作会先尝试完整重同步，再执行新的变更。

## 视觉设置

视觉增强仅适用于 Anthropic Profile。字段和默认值如下：

| 字段 | 默认值 | 说明 |
|---|---:|---|
| `enabled` | `false` | 是否在主请求前处理直接图片块 |
| `model` | `sonnet` | 上游实际接受的视觉模型名 |
| `max_tokens` | `2048` | 单张图片描述最大输出 Token |
| `timeout` | `2m` | 单图总时限，包含排队、请求和重试 |
| `max_concurrency` | `4` | 当前 Profile 视觉请求的共享并发上限 |
| `cache_ttl` | `30m` | 成功描述的进程内缓存时间 |
| `cache_max_entries` | `512` | LRU 缓存条目上限 |
| `prompt` | 内置提示词 | 非空时完整替换内置提示词 |

行为、安全边界和调优说明见[图片预处理](vision.md)。

## 有序重试规则

规则严格按控制台中的顺序匹配，首个同时满足状态码和可选响应正文子串的规则生效，
并锁定用于本次请求后续重试：

| 字段 | 说明 |
|---|---|
| `status` | 匹配的 HTTP 状态码 |
| `body_contains` | 可选正文子串；留空表示只匹配状态码 |
| `max_retries` | 最大重试次数 |
| `delay` | 基础等待时间 |
| `jitter` | 随尝试序号线性增加的等待量 |

等待时间为 `delay + attempt × jitter`。网络错误尚未锁定规则时使用第一条规则；
规则为空时网络错误直接返回 `502`，HTTP 错误响应原样返回。

## Agent 配置生成

Profile 列表和编辑页的“生成配置”在浏览器内即时生成 Claude Code 或 OpenAI
客户端设置。公网地址、模型映射和认证变量选择都是临时输入，不写入服务器；
关闭或刷新后恢复为空。

生成结果只包含 `<SET_LOCALLY>` 占位符，不提供真实密钥输入。复制或下载后，请在
客户端本地设置凭据，并在使用前核对 Profile URL。该功能是配置生成器，不会修改
Profile，也不会导入客户端配置。
