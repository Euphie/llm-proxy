# 代理通道

代理通道是一套可独立选择的代理运行配置。请在
`/_admin/profiles` 创建、复制、启停、设为默认或删除代理通道。外部 URL 模式透传调用方
认证；聚合网关模式改为注入供应商 Secret，网关调用方 Key 不属于代理通道。

代理通道入口本身不执行 llm-proxy 鉴权。外部 URL 模式依赖上游校验透传的调用方凭据；
内部聚合网关模式会直接使用已保存的供应商 Secret。部署到不可信网络前必须增加外部访问
控制，详见[安全边界与部署](configuration.md#安全边界与部署)。

## 标识与协议

- `slug` 必须匹配 `^[a-z0-9][a-z0-9-]{0,62}$`，最长 63 个字符。
- `v1` 和 `gateways` 是保留路径，不能作为 slug。
- 名称不能为空；slug 在数据库中唯一。
- `anthropic` 支持 Anthropic Messages、图片增强和 Anthropic 用量解析。
- `openai` 支持 OpenAI Chat Completions 与 Responses；图片增强只处理 Responses。

## 路由与 Upstream

| 客户端路径 | 选择方式 | 传给代理通道的路径 |
|---|---|---|
| `/v1/...` | 当前默认代理通道 | `/v1/...` |
| `/<slug>/v1/...` | 指定启用的代理通道 | `/v1/...` |

查询参数和转义路径会保留。未知 slug 返回 `404`；停用的代理通道返回 `503`；
尚未配置可用默认项时，数据面返回 `503`。

代理通道 Upstream 有两种类型：

| 类型 | 行为 |
|---|---|
| 外部 URL | 直接转发到手填 Upstream 地址，并透传调用方请求头 |
| 聚合网关 | 引用“网关管理”中已有网关，复用其上游供应商、模型路由和供应商 Key / Secret |

外部 URL 必须是带主机的 `http` 或 `https` URL，不允许用户名、密码、查询参数或
fragment。末尾斜杠会被规范化，代理通道路由后的请求 URI 追加到 URL
路径。例如 Upstream 为 `https://api.example.com/base` 时，
`/coding/v1/messages?x=1` 会转发到
`https://api.example.com/base/v1/messages?x=1`。

引用聚合网关时，代理通道保存 `upstream_gateway_id`，不再保存 Upstream URL；协议必须
与网关协议一致。控制台只有在选择“聚合网关”类型后才显示网关选择器，选中网关后会带入
协议和启用路由中的对外模型 ID。

内部网关模式保留代理通道的视觉预处理、有序重试和用量统计。主请求和视觉影子请求分别
按自己的模型名经过网关选路，因此启用视觉预处理时，识图模型也必须是该网关已启用的
对外模型；否则保存校验失败。内部调用不需要网关调用方 Key，转发到供应商前会移除调用方的
`Authorization` 和 `X-Api-Key`，再注入路由选中的供应商 Secret。

外部 URL 模式下，端到端请求头会透传，连接级 hop-by-hop 头会移除。请由调用方在请求中发送
`Authorization`、`X-Api-Key` 等凭据，不要把密钥写进代理通道。

## 启用、默认项与热更新

第一个代理通道自动成为默认项，控制台中的默认复选框保持选中且不可取消。默认代理通道
必须启用，不能直接停用；切换默认项时目标也必须启用。删除默认项时必须同时选择另一个
启用的代理通道，且系统始终保留一个有效默认项。

管理操作先在 SQLite 事务中保存，再构建并原子发布新的 Runtime Registry 快照。
并发请求只会看到完整的旧快照或新快照，不会看到半更新状态；正常修改无需重启。
发布失败时，旧的完整 Registry 快照继续服务；Coordinator 的运行时同步状态会变为
未就绪，下一次管理操作会先尝试完整重同步，再执行新的变更。

## 视觉设置

视觉增强适用于 Anthropic `POST /v1/messages` 和 OpenAI
`POST /responses` / `POST /v1/responses`。Chat Completions 可作为 OpenAI
识图接口，但 Chat Completions 主请求仍保持原样转发。字段和默认值如下：

| 字段 | 默认值 | 说明 |
|---|---:|---|
| `enabled` | `false` | 是否在主请求前处理直接图片块 |
| `transport` | 按协议 | Anthropic 为 `anthropic_messages`；OpenAI 默认为 `openai_chat_completions`，也可选 `openai_responses` |
| `model` | `sonnet` | 上游实际接受的视觉模型名 |
| `unlisted_model_policy` | `bypass` | 控制台“默认视为支持视觉，不增强”；改为 `enhance` 时显示“默认视为不支持视觉，使用增强” |
| `max_tokens` | `2048` | 单张图片描述最大输出 Token |
| `timeout` | `2m` | 单图总时限，包含排队、请求和重试 |
| `max_concurrency` | `4` | 当前代理通道视觉请求的共享并发上限 |
| `cache_ttl` | `30m` | 成功描述的进程内缓存时间 |
| `cache_max_entries` | `512` | LRU 缓存条目上限 |
| `prompt` | 内置提示词 | 非空时作为基础识图提示词；同消息用户文本仍会用于提取相关视觉证据 |

配置版本保持 `1`；旧代理通道缺少 `transport` 时按协议补默认值，编辑保存一次即可
写入。OpenAI Responses 影子请求固定使用 `store: false`。
代理只收集同一条 `user` 消息的直接 Anthropic `text` 或 Responses `input_text` 块；
非字符串、非直接和空白块会忽略。各块经 trim 后按顺序用换行拼接，最多保留 4096 个
Unicode 字符（超出时截断并在上限内追加标记），再经 JSON 编码嵌入影子提示词。其他消息
中的文本和工具输出不会作为问题上下文发送。该上下文使视觉 Upstream 返回与当前问题相关
的可见证据，而非代替主模型回答；没有有效上下文时仍返回通用描述。自定义 `prompt` 不能
关闭此上下文，缓存按实际上下文提示词隔离，因此不同问题可为同一图片生成独立缓存条目。
上下文可能随模型返回的视觉调试描述出现，应按敏感数据处理。
行为、安全边界和调优说明见[图片预处理](vision.md)。

聚合网关模式下，`vision.model` 填写网关中的对外模型名，而不是供应商内部模型名。
主模型和识图模型可以路由到不同供应商，但都必须属于同一协议的当前网关。

## 模型能力

模型能力只保存在代理通道配置的 `models` 字段中，是运行时唯一使用的模型事实。例如：

```json
{
  "models": [
    {"id": "GLM-5", "context_window": 204800, "supports_vision": false},
    {
      "id": "Kimi-K2.5",
      "context_window": 262144,
      "max_output_tokens": 65536,
      "supports_vision": true
    }
  ],
  "vision": {"unlisted_model_policy": "bypass"}
}
```

`id` 是精确、区分大小写的运行时键；`GLM-5` 与 `glm-5` 是不同模型。每行都必须明确
填写 `supports_vision`。`context_window` 和 `max_output_tokens` 可省略；已填写时必须为
不大于 `9,007,199,254,740,991` 的正整数，且二者同时存在时输出上限必须小于上下文
窗口。控制台接受整数或十进制 `K`/`M` 简写，保存为整数。模型建议只辅助填写表单，
不能改变这些保存的事实。

缺失 `models` 等于空目录，因此所有模型均未收录。缺失或空
`vision.unlisted_model_policy` 只会让未收录模型默认 `bypass`；已收录模型始终按其
`supports_vision` 判定。

**破坏性行为变更：**旧代理通道通常同时缺少目录和策略，因此其所有模型都未收录并
默认 bypass，不再沿用旧的默认影子请求行为。如需增强，必须保存模型行，或显式将策略
改为 `enhance`。不提供旧行为兼容层。

模型事实、建议来源和 Agent 上下文映射见[模型能力与 Agent 上下文](models.md)。

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

代理通道决定代理协议和 Upstream，Agent 只决定如何生成客户端配置；二者不绑定，
同一代理通道可以为多个兼容 Agent 生成配置。可选目标由代理通道协议筛选：

| Agent | 代理通道协议 | 生成内容 |
|---|---|---|
| Claude Code | `anthropic` | Claude settings JSON 或 Shell 环境变量 |
| OpenCode | `anthropic` 或 `openai` | `opencode.json` Provider 配置 |
| Codex CLI | `openai` | 用户级或命名 TOML 配置，仅使用 Responses API |

Claude Code 的 Anthropic Base URL 为 `https://host/<slug>`；OpenCode Provider
和 OpenAI API Base 使用 `https://host/<slug>/v1`。Codex Provider 固定使用
`wire_api = "responses"`，其 Upstream 必须支持 `/v1/responses`。

生成器不会转换协议，不兼容的 Agent 选项会直接隐藏。需要同时接入 Claude Code 和
Codex 时，应分别创建 Anthropic 与 OpenAI 代理通道；两者可以引用各自协议下配置的
聚合网关和供应商。

公网地址、Agent、模型映射和认证变量都是浏览器中的临时输入，不写入服务器，关闭
或刷新后即丢弃。生成结果只使用客户端原生环境变量引用、变量名或
`<SET_LOCALLY>` 占位符，不提供真实密钥输入。OpenCode 使用 `{env:VARIABLE}`，
Codex 使用 `env_key`，真实值均由客户端本地环境提供。该功能不会修改代理通道，
也不会读取或导入客户端现有配置。

生成器显示的目标位置如下：

| Agent | 全局配置 | 项目或命名配置 |
|---|---|---|
| Claude Code | `~/.claude/settings.json` | `.claude/settings.json` 或 `.claude/settings.local.json` |
| OpenCode | `~/.config/opencode/opencode.json` | `opencode.json` |
| Codex CLI | `~/.codex/config.toml` | `~/.codex/<name>.config.toml`，使用 `codex --profile <name>` 启动 |

Codex 生成器不写 `[profiles.*]`；命名配置单独写入
`~/.codex/<name>.config.toml`，并通过 `codex --profile <name>` 启动。项目级
`.codex/config.toml` 也不用于覆盖 Provider 或认证设置。格式依据
[Codex 配置文档](https://learn.chatgpt.com/docs/config-file/config-reference)和
[OpenCode Provider 文档](https://opencode.ai/docs/providers)。
