# 聚合网关

聚合网关把多个上游供应商包装成一个模型入口。调用方使用 llm-proxy 分发的调用方
Key；llm-proxy 根据请求中的 `model` 精确选择供应商路由，并在转发时注入该供应商的
Key / Secret。供应商、网关或 Key 保存后会立即发布新的运行时快照，不需要重启服务。

## 可以独立使用

聚合网关本身就是完整的请求入口，不依赖代理通道。只需要配置供应商、网关和调用方 Key，
调用方即可直接访问 `/gateways/{slug}/v1/...`。不使用视觉增强、有序重试或后台 Agent
配置生成时，可以完全不创建代理通道。

| 接入方式 | 网关调用方 Key | 视觉增强与重试 | Agent 配置生成 |
|---|---|---|---|
| 直接调用聚合网关 | 必须 | 不支持 | 不支持，需要客户端手工配置 Base URL 和 Key |
| 代理通道引用聚合网关 | 内部调用不需要 | 支持 | 支持 |

直接网关仍会执行调用方鉴权、精确模型路由、供应商 Secret 注入和可解析的 Token 用量记录。
Anthropic 与 OpenAI 请求格式不会互相转换，应为两种协议分别配置供应商和网关。

后台按“供应商管理”、“网关管理”、“秘钥管理”的顺序配置。三个页面默认均为列表，
需要新增时从列表页点击对应的创建按钮。

## 供应商管理

进入 `/_admin/aggregate-gateways/providers`：

1. 点击“新增供应商”，填写名称、slug、协议和 Upstream。
2. 填写认证 Header，例如 `Authorization`、`x-api-key` 或 `api-key`。
3. 填写供应商 Key / Secret。
4. 添加该供应商真实支持的一个或多个模型 ID。
5. 保存后返回列表；点击已有供应商可继续编辑。

认证值的处理规则如下：

- Header 为 `Authorization` 且填写的是不含空格的原始 key 时，转发时自动补为
  `Bearer <key>`；无需手工填写 `Bearer`。
- 已填写 `Bearer ...`、`Basic ...` 等完整认证方案时，按原值发送。
- 其他认证 Header 按填写值原样发送。
- 编辑供应商时 Secret 留空表示保留已保存值；列表和编辑页不会回显明文。

供应商 slug 只要求在同一协议内唯一。例如 Anthropic 和 OpenAI 供应商可以都使用
`deepseek`，同一协议内则不能重复。

## 网关管理

进入 `/_admin/aggregate-gateways` 查看网关列表、启停状态、对外地址和路由摘要。
点击“新建网关”创建，点击已有网关进入编辑：

1. `Slug` 决定对外基础路径：`/gateways/{slug}/v1`，并在所有网关中唯一。
2. `协议` 决定可选择的供应商及模型。
3. 勾选供应商模型后，该模型 ID 作为实际转发的 `provider_model`。
4. `对外模型` 是调用方请求中的 `model`，默认等于供应商模型 ID，也可以改为别名。
5. 一个网关可以开放多个模型，但每个对外模型名在该网关内必须唯一。

路由按对外模型名精确匹配，没有优先级概念。需要把不同供应商的同名模型放在同一网关
时，应分别映射成不同的对外模型名。

## “秘钥管理”

进入 `/_admin/aggregate-gateways/keys`，选择网关后点击创建按钮。一个网关可以创建多个
调用方 Key，并可在创建时设置名称、启用状态和可选过期时间。

Key 明文只在创建成功时显示一次，之后列表只显示前缀、尾号、状态、名称和时间信息。
必须立即保存明文；丢失后只能创建新 Key。建议为外部调用方设置合理的过期时间。

当前页面只提供创建和列表查看，不能单独更新、停用或删除已有 Key。Key 泄露时应立即
停用整个网关，阻止该网关下所有 Key 继续访问；当前实现不适合要求独立密钥即时撤销的
场景。

## 直接调用网关

这是聚合网关的独立使用方式，不需要创建代理通道。

常用协议路径：

| 协议接口 | 完整路径 |
|---|---|
| Anthropic Messages | `/gateways/{slug}/v1/messages` |
| OpenAI Responses | `/gateways/{slug}/v1/responses` |
| OpenAI Chat Completions | `/gateways/{slug}/v1/chat/completions` |

Anthropic Messages 示例：

```sh
curl http://localhost:8087/gateways/team/v1/messages \
  -H 'X-Api-Key: lgp_xxx' \
  -H 'Anthropic-Version: 2023-06-01' \
  -H 'Content-Type: application/json' \
  -d '{"model":"claude-sonnet","max_tokens":128,"messages":[{"role":"user","content":"hi"}]}'
```

OpenAI 兼容示例：

```sh
curl http://localhost:8087/gateways/team/v1/chat/completions \
  -H 'Authorization: Bearer lgp_xxx' \
  -H 'Content-Type: application/json' \
  -d '{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}'
```

也可以使用 `X-Api-Key: lgp_xxx`。网关验证调用方 Key 后，会删除调用方的
`Authorization` 和 `X-Api-Key`，再写入选中供应商配置的认证 Header。

请求必须使用 `Content-Type: application/json`，并提供非空字符串 `model`。该值按网关
启用的对外模型名精确、区分大小写匹配；命中别名后，转发前会改写为供应商模型 ID。

常见网关错误：

| 状态码 | 含义 |
|---:|---|
| `400` | 请求不是 JSON，或 `model` 缺失、为空或格式无效 |
| `401` | Key 缺失、错误、已停用或已过期 |
| `404` | 网关 slug 不存在 |
| `422` | 网关中没有匹配请求 `model` 的启用路由 |
| `503` | 网关已停用 |
| `502` | 网关无法发起供应商请求 |

供应商返回的正常 HTTP 错误响应会原样转发；`502` 仅表示网关层的上游请求失败。

直接调用 `/gateways/{slug}/...` 只经过网关的数据面，不执行代理通道的视觉预处理或
有序重试。成功响应中存在可解析 usage 时，系统仍会记录 Token 用量，并关联当前网关和
调用方密钥。

## 作为代理通道 Upstream

代理通道的 Upstream 类型可以选择“聚合网关”。这种模式保存内部
`upstream_gateway_id`，调用方仍访问 `/v1/...` 或 `/<slug>/v1/...`，无需网关调用方
Key。代理通道会复用网关的供应商、模型路由和供应商 Secret，同时保留自身的视觉预处理、
有序重试和 Token 用量统计。

保存时会校验：

- 引用的网关必须存在；
- 代理通道协议必须与网关协议一致；
- 启用视觉预处理时，识图模型也必须是该网关已启用的对外模型名。

运行时，主请求的 `model` 也必须精确匹配网关已启用的对外模型名。主请求和视觉影子请求
都按各自的模型名经过同一网关选路。转发前会移除调用方认证，并注入路由对应的供应商
Secret。

## Claude Code 与 Codex

llm-proxy 不在 Anthropic 与 OpenAI 请求格式之间做协议转换，因此通常需要两套入口：

- Claude Code 使用 Anthropic 供应商、Anthropic 网关和 Anthropic 代理通道；
- Codex CLI 使用 OpenAI 供应商、OpenAI 网关和 OpenAI 代理通道，因为 Codex 自定义
  Provider 固定使用 Responses API。

两套供应商可以使用相同 slug，但必须分别配置各协议实际可用的 Upstream 和模型。
只使用聚合网关时，兼容客户端可以手工设置上述 Base URL 和网关 Key；后台 Agent 配置
生成器只面向代理通道，不会生成直接网关配置。

需要为 Codex 启用视觉增强时，在 OpenAI 代理通道中启用视觉预处理，并确保同一 OpenAI
网关同时开放主模型和识图模型；识图接口可选择 Chat Completions 或 Responses，取决于
上游实际支持能力。Codex 配置格式见
[官方配置参考](https://learn.chatgpt.com/docs/config-file/config-reference)。

## 数据与限制

- 当前只支持 `anthropic` 和 `openai` 协议。
- 直接网关请求必须是带顶层 `model` 的 JSON；无模型请求、文件接口和元数据查询不能通过
  聚合网关转发。
- 模型路由为精确匹配，没有权重、成本、健康检查、负载均衡或自动故障转移。
- 供应商和网关只能创建、编辑、启停，当前没有删除操作；调用方 Key 只能创建和查看。
- 供应商 Key / Secret 以明文保存在本地 SQLite 中，数据库文件权限为 `0600`；生产环境
  应限制数据目录访问，必要时接入外部 secret store。
- 调用方 Key 只保存 SHA-256 摘要、前缀和尾号，无法从数据库恢复明文。
- 数据库必须挂载到持久化目录或卷；部署和访问控制要求见
  [运行配置](configuration.md#安全边界与部署)。
