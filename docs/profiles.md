# Profiles

Profile 是一套可独立选择的代理运行配置。请在 `/_admin/profiles` 创建、复制、启停、
设为默认或删除 Profile；调用方密钥不属于 Profile，仍由每个代理请求透传。

## 后台设置页

Profile 详情路径为 `/_admin/profiles/<id>/<section>`。可用分区包括 `overview`、
`connection`、`models`、`routing`、`vision`、`reliability` 和 `agents`。后台一次只显示
当前分区，保存时仍发送完整 Profile，因此未显示的配置不会被清空。

功能依赖按以下顺序处理：

1. “连接”独立决定普通代理是否可用。
2. “模型”是普通转发和 Agent 配置的选填项，但智能路由必须依赖已录入模型。
3. “容错”中的普通重试始终可独立编辑；智能路由的模型切换与它共享请求预算。
4. 视觉增强和智能路由同时启用时，视觉模型必须已录入，并具备视觉、价格、上下文和输出能力。

模型 ID 是区分大小写的引用键。删除或修改模型 ID 前，后台检查强模型基线、任务分析模型、
参与模型、Route 候选、质量评审模型和视觉模型；存在引用时会阻止操作并列出对应分区。
关闭智能路由、视觉增强或对比学习只修改 `enabled`，其余参数保留，重新启用时会重新校验依赖。

## 标识与协议

- `slug` 必须匹配 `^[a-z0-9][a-z0-9-]{0,62}$`，最长 63 个字符。
- `v1` 是默认路由的保留值，不能作为 slug。
- 名称不能为空；slug 在数据库中唯一。
- `anthropic` 支持 Anthropic Messages、图片增强和 Anthropic 用量解析。
- `openai` 支持 OpenAI Chat Completions 与 Responses；两种操作都可处理图片增强。

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

客户端请求体上限为 64 MiB。普通成功响应仍流式透传，但统计和评测最多在内存保留前 16 MiB；
智能路由必须在提交前完整检查的非流式响应，以及需要分类的错误响应，单份缓冲上限为 64 MiB。
超过硬上限时分别返回 `413` 或 `502`，不会转发截断正文。

### 单一 Upstream 与网关职责

每个 Profile 只保存一个 `upstream`。显式模型、智能路由的任务分析、视觉、回答、重试和模型切换
都使用这个地址。代理不会维护第二个端点，也不会在请求失败时改写上游地址。

多节点负载均衡、健康检查、熔断和节点故障转移属于上游网关职责。需要这些能力时，把 Profile 的
`upstream` 指向网关，由网关在相同协议和鉴权边界内选择实际节点。

**破坏性配置变更：**`provider_id`、`credential_scope`、`targets` 和
`max_target_switches` 已删除，不提供旧配置兼容层。旧存储值读取时会被丢弃；管理 API 的严格写入
会拒绝这些未知字段。升级后请重新保存 Profile 和策略，使导出配置只包含当前字段。

## 启用、默认项与热更新

第一个 Profile 自动成为默认项，控制台中的默认复选框保持选中且不可取消。默认
Profile 必须启用，不能直接停用；切换默认项时目标也必须启用。删除默认项时必须
同时选择另一个启用的 Profile，且系统始终保留一个有效默认项。

管理操作先在 SQLite 事务中保存，再构建并原子发布新的 Runtime Registry 快照。
并发请求只会看到完整的旧快照或新快照，不会看到半更新状态；正常修改无需重启。
发布失败时，旧的完整 Registry 快照继续服务；Coordinator 的运行时同步状态会变为
未就绪，下一次管理操作会先尝试完整重同步，再执行新的变更。

## 视觉设置

视觉增强适用于 Anthropic `POST /v1/messages`，以及 OpenAI
`POST /v1/chat/completions`、`POST /responses` / `POST /v1/responses`。入口路径固定解析操作，
不会根据正文顶层字段猜测协议。字段和默认值如下：

| 字段 | 默认值 | 说明 |
|---|---:|---|
| `enabled` | `false` | 是否在主请求前处理直接图片块 |
| `transport` | 按协议 | Anthropic 为 `anthropic_messages`；OpenAI 默认为 `openai_chat_completions`，也可选 `openai_responses` |
| `model` | `sonnet` | 上游实际接受的视觉模型名 |
| `unlisted_model_policy` | `bypass` | 控制台“默认视为支持视觉，不增强”；改为 `enhance` 时显示“默认视为不支持视觉，使用增强” |
| `max_tokens` | `2048` | 单张图片描述最大输出 Token |
| `timeout` | `2m` | 单图总时限，包含排队、请求和重试 |
| `max_concurrency` | `4` | 当前 Profile 视觉请求的共享并发上限 |
| `cache_ttl` | `30m` | 成功描述的进程内缓存时间 |
| `cache_max_entries` | `512` | LRU 缓存条目上限 |
| `prompt` | 内置提示词 | 非空时作为基础识图提示词；同消息用户文本仍会用于提取相关视觉证据 |

配置版本保持 `1`；旧 Profile 缺少 `transport` 时按协议补默认值，编辑保存一次即可
写入。OpenAI Responses 影子请求固定使用 `store: false`。
代理只处理协议位置中的直接图片块：Anthropic/Chat 的 `messages[].content[]`，以及 Responses
消息 `content[]` 和 `function_call_output.output[]`。协议字段之外的同名嵌套对象会忽略。
代理只收集同一条 `user` 消息的直接 Anthropic/Chat `text` 或 Responses `input_text` 块；
非字符串、非直接和空白块会忽略。各块经 trim 后按顺序用换行拼接，最多保留 4096 个
Unicode 字符（超出时截断并在上限内追加标记），再经 JSON 编码嵌入影子提示词。其他消息
中的文本和工具输出不会作为问题上下文发送。该上下文使视觉 Upstream 返回与当前问题相关
的可见证据，而非代替主模型回答；没有有效上下文时仍返回通用描述。自定义 `prompt` 不能
关闭此上下文，缓存按实际上下文提示词隔离，因此不同问题可为同一图片生成独立缓存条目。
上下文可能随模型返回的视觉调试描述出现，应按敏感数据处理。
行为、安全边界和调优说明见[图片预处理](vision.md)。

**破坏性行为变更：**OpenAI Chat Completions 主请求现在也会进入图片增强；不再保留此前仅把
Chat 当作识图传输、却原样转发 Chat 主请求的行为。若不希望增强，请关闭视觉预处理或把主模型
标记为支持视觉。

## 模型能力

模型能力只保存在 `profile.config.models`，是运行时唯一使用的模型事实。例如：

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

**破坏性行为变更：**旧 Profile 通常同时缺少目录和策略，因此其所有模型都未收录并
默认 bypass，不再沿用旧的默认影子请求行为。如需增强，必须保存模型行，或显式将策略
改为 `enhance`。不提供旧行为兼容层。

模型事实、建议来源和 Agent 上下文映射见[模型能力与 Agent 上下文](models.md)。

## 智能路由

智能路由按 Profile 独立启用，只处理受支持推理接口中的 `model=auto`。显式模型请求保持原有
转发行为。保存启用配置时会完整校验：

- 参与模型、强模型基线和轻量任务分析模型均已录入；
- 所有可能被调用的模型具备所需能力和输入/输出参考价格；
- 策略名称符合 `YYYYMMDD-NNN`，默认 Route、任务映射和候选引用有效；
- Route 质量、稳定性、严重错误门槛、四项权重与统一尝试预算均在有效范围内；
- `session_lock_token_threshold` 是正整数；候选显式声明 `production_eligible`；
- 开启视觉增强时，视觉模型也必须在模型目录中，明确支持视觉，并配置能容纳描述输出及单图
  提示预留的上下文窗口和最大输出上限。

高风险由任务分析模型结合请求意图、上下文和实际或强制工具操作做语义判断，不使用文本或工具名关键词匹配。
长上下文、结构化输出和工具声明只形成能力或容量约束，不单独判为高风险。

每个 `model=auto` 请求都会调用轻量分析模型，不再使用短语或关键词白名单。分析模型返回固定任务类型、
复杂度信号、语义风险和三个独立置信度，后端再确定难度。分析超时、网络故障、过载或畸形输出可使用
满足硬约束的强模型基线；低置信任务类型走默认 Route，低置信复杂度按中等保护，风险不确定时使用
强模型兜底。分析器的 401/403、不可重试 4xx 和
重定向/协议响应是硬失败，不会发起主回答。其他请求先应用能力与 Route 硬门槛，再按质量、稳定性、
成本效率和性能加权选择路由分最高者。计划内模型切换只允许发生在 `ClientCommit` 前，并和任务分析、视觉及重试
共享调用次数、截止时间和费用预算。可重试故障只会再次调用同一 Upstream；重试耗尽后才可能
尝试计划中的下一个模型。

含图片的复合候选要求回答模型与视觉模型都具备所需能力；图片来源必须兼容视觉传输，例如
OpenAI Chat 传输不能承载 `file_id`。主回答前的临时视觉故障会在同一 Upstream 重试，然后按预算
尝试计划内模型；鉴权、请求/能力与不可重试 4xx 错误立即停止。任务分析后会冻结完整最坏调用
图；复合节点在视觉调用前原子预留视觉与首个回答容量。

“Session 绑定有效期”默认 `24h`，允许范围为 `5m` 到 `720h`。Claude Code 和 Codex 原生会话会
自动识别；其他客户端可在 Auto 请求中发送 `X-LLM-Proxy-Session-ID`。代理仅在同时存在
`Authorization`、`X-Api-Key`、`Api-Key` 或 `Anthropic-Api-Key` 之一时建立绑定。键由原始
Session ID、Profile、用途和鉴权域 HMAC 生成；原始 Session ID 与凭据不写数据库，自定义内部
Session 头会在任何请求转发前移除。

同一任务的工具回合直接复用已经成功的模型。不同任务连续 3 次选择同一模型、最低分类置信度达到 85%，
且会话消息或当前响应缓存读取达到 `session_lock_token_threshold` 后锁定模型；默认阈值为 100K Token，
system、Skill 与工具定义不计入。新用户任务仍重新分析语义风险：普通风险保留锁定模型，高风险、风险未知或
能力不满足时使用强模型基线。最高模型成功回答后立即锁定。修改阈值只清理普通锁；最高模型锁保留。

当前支持 Anthropic `POST /v1/messages`、OpenAI `POST /v1/chat/completions` 和
`POST /v1/responses`。其他操作使用 `auto` 会返回 unsupported operation；不会静默透传或猜测
模型。详细算法、费用口径和后续范围见[智能路由](intelligent-routing.md)。

“智能路由”页面展示当前 Active Policy。生成器按目标和预算使用精确模型 ID、公共评测冷启动
先验和本地可靠证据生成浏览器内预览；预览不会写入数据库或修改线上策略。
管理员检查并编辑完整 Policy 后点击“保存并立即生效”，系统才创建新的不可变版本。未生成本地目录文件时，
重新载入会继续使用当前有效的内置快照。固定版本导入见[公共评测目录导入](evaluation-catalog-import.md)。

从 Profile 列表新建配置时使用四步向导：先保存连接信息，再录入模型、配置智能路由并完成创建。
第一步成功后 Profile 已经持久化；退出后会保留该 Profile，不会隐式删除。智能路由可以点击
“生成推荐预览”，推荐结果会进入本地编辑面板，保存成功后立即影响所有新请求。

## 有序重试规则

规则严格按控制台中的顺序匹配，首个同时满足状态码和可选响应正文子串的规则生效，
并锁定用于本次请求后续重试：

| 字段 | 说明 |
|---|---|
| `status` | 只接受 `408`、`425`、`429` 或 `500..599` |
| `body_contains` | 可选正文子串；留空表示只匹配状态码 |
| `max_retries` | 最大重试次数 |
| `delay` | 基础等待时间 |
| `jitter` | 随尝试序号线性增加的等待量 |

等待时间为 `delay + attempt × jitter`。网络错误尚未锁定规则时使用第一条有效规则；
规则为空时网络错误直接进入计划内故障切换，计划耗尽后返回 `502`。对于 `408`、`425`、`429`
和 `500..599`，规则只控制当前节点重试；即使正文未命中规则，也会直接尝试计划内的下一节点或
模型。`400`、`401`、`403`、`404`、`409`、`422` 等硬失败不能保存为重试规则，也不会故障切换。

## Agent 配置生成

Profile 决定代理协议和 Upstream，Agent 只决定如何生成客户端配置；二者不绑定，
同一 Profile 可以为多个兼容 Agent 生成配置。可选目标由 Profile 协议筛选：

| Agent | Profile 协议 | 生成内容 |
|---|---|---|
| Claude Code | `anthropic` | Claude settings JSON 或 Shell 环境变量 |
| OpenCode | `anthropic` 或 `openai` | `opencode.json` Provider 配置 |
| Codex CLI | `openai` | 用户或命名 Profile 的 TOML 配置，仅使用 Responses API |

Claude Code 的 Anthropic Base URL 为 `https://host/<slug>`；OpenCode Provider
和 OpenAI API Base 使用 `https://host/<slug>/v1`。Codex Provider 固定使用
`wire_api = "responses"`，其 Upstream 必须支持 `/v1/responses`。

公网地址、Agent、模型映射和认证变量都是浏览器中的临时输入，不写入服务器，关闭
或刷新后即丢弃。生成结果只使用客户端原生环境变量引用、变量名或
`<SET_LOCALLY>` 占位符，不提供真实密钥输入。OpenCode 使用 `{env:VARIABLE}`，
Codex 使用 `env_key`，真实值均由客户端本地环境提供。该功能不会修改 Profile，
也不会读取或导入客户端现有配置。

生成器显示的目标位置如下：

| Agent | 全局配置 | 项目或命名配置 |
|---|---|---|
| Claude Code | `~/.claude/settings.json` | `.claude/settings.json` 或 `.claude/settings.local.json` |
| OpenCode | `~/.config/opencode/opencode.json` | `opencode.json` |
| Codex CLI | `~/.codex/config.toml` | `~/.codex/<name>.config.toml`，使用 `codex --profile <name>` 启动 |

Codex 不生成已废弃的 `[profiles.*]` 配置；项目级 `.codex/config.toml` 也不用于覆盖
Provider 或认证设置。格式依据 [Codex 配置文档](https://developers.openai.com/codex/config-reference)
和 [OpenCode Provider 文档](https://opencode.ai/docs/providers)。
