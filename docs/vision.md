# 图片预处理

当主模型不支持视觉输入时，llm-proxy 可以先调用同一 Profile Upstream 的视觉
模型生成描述，再以描述替换图片并发送主请求。该流程支持：

- Anthropic `POST /v1/messages`；
- OpenAI `POST /v1/chat/completions`；
- OpenAI `POST /responses` 或 `POST /v1/responses`。

其他路径不会进入图片预处理。入口路径决定解析操作，正文中的同名字段不能改变协议。

## 影子请求门控

影子请求必须依次通过以下五项：

1. Profile 已启用视觉预处理；
2. Profile 的识图模型非空；
3. 请求是支持协议的 `POST` JSON 路由（Anthropic `/v1/messages` 或 OpenAI
   `/v1/chat/completions`、`/responses`、`/v1/responses`）；
4. 主 `model` 的模型能力判定允许增强；
5. 请求中至少有一个受支持的直接图片块。

第 4 项只查询该 Profile 保存的精确、区分大小写模型 ID：`supports_vision: true`
绕过，`false` 增强；已收录模型始终按该字段判定。缺失 `models` 等于空目录，因此
所有模型均未收录。缺失或空 `unlisted_model_policy` 只让未收录模型默认 bypass；
显式设为 `enhance` 时，未收录模型进入增强。
控制台将 `bypass` 显示为“默认视为支持视觉，不增强”，将 `enhance` 显示为“默认视为
不支持视觉，使用增强”。

主 `model` 缺失、为空或不是字符串时，代理原样绕过视觉处理，由 Upstream 自行校验该
字段；这不等同于把请求拒绝为视觉错误。

## 工作流程与失败

1. 接收受支持路径的 JSON 请求。
2. 收集 Anthropic `messages[].content[]` 中的直接 `image`、Chat
   `messages[].content[]` 中的直接 `image_url`，或 Responses `input[]` 消息内容和
   `function_call_output.output[]` 中的直接 `input_image`。
3. 查询缓存，并并发处理未命中图片。
4. 按 `vision.transport` 发送非流式影子请求；Responses 影子请求固定设置
   `store: false`。
5. 按原顺序将图片替换为带固定前缀的 `text` 或 `input_text`。
6. 使用同一 Profile 的重试与流式代理发送主请求。

任一图片失败时不会发送主请求。无效图片结构返回 `400`；影子请求的网络错误、
超时、非成功响应或空描述返回 `502`。客户端取消会结束相关工作。

## 问题感知视觉证据

代理只收集与图片处于同一条 `user` 消息的直接文本块：Anthropic/Chat `text` 和 Responses
`input_text`。Responses 的字符串 `input` 与直接 `input_text` 也会保留给智能路由的任务分析。
非字符串、非直接和仅含空白的块会忽略；每块去除首尾空白后按顺序用换行
拼接，最多保留 4096 个 Unicode 字符（超出时截断并在上限内追加标记），再经 JSON 编码
嵌入影子提示词。其他消息中的文本和工具输出不会作为问题上下文发送给视觉 Upstream。

该上下文用于让识图模型提取当前问题相关的可见证据，而非代替主模型回答；没有有效上下文
时仍使用通用图片描述。自定义 `prompt` 是基础识图提示词，不能关闭这些上下文指令。上下文
也可能随视觉描述出现在模型返回的调试内容中。

完整数据流见[架构图](assets/llm-proxy-architecture.svg)。

## Profile 字段

在 `/_admin/profiles` 编辑 Anthropic 或 OpenAI Profile，展开“视觉参数”：

| 字段 | 默认值 | 说明 |
|---|---:|---|
| 启用视觉预处理 | 关闭 | 是否处理直接图片块 |
| 视觉调用接口 | 按协议 | Anthropic 固定为 Messages；OpenAI 默认 Chat Completions，也可选 Responses |
| 模型 | `sonnet` | 原样发送给 Upstream 的实际模型名 |
| 最大 Token | `2048` | 单张图片描述的输出上限；按协议转换为对应请求字段 |
| 超时 | `2m` | 包含并发排队、请求、重试和读取 |
| 最大并发 | `4` | 该 Profile 所有请求共享的影子请求上限 |
| 缓存 TTL | `30m` | 成功描述的缓存时间 |
| 缓存条目上限 | `512` | LRU 容量 |
| 提示词 | 内置提示词 | 非空时作为基础识图提示词；同消息用户文本仍会用于提取相关视觉证据 |

模型字段不会读取 Agent 配置中的模型映射；应填写 Upstream 实际接受的视觉模型名。
影子请求复用当前 Profile 的有序重试规则。`store: false` 只约束 Responses 影子
请求，不修改原始主请求的 `store` 选择。

Profile 配置版本仍为 `1`。旧 Profile 缺少 `vision.transport` 时使用协议默认值，
在控制台编辑并保存一次即可显式写入。主请求协议不会随该选项改变：

- `anthropic_messages`：识图请求使用实际 Messages 主请求目标；
- `openai_responses`：识图请求使用实际 Responses 主请求目标；
- `openai_chat_completions`：把实际主请求目标末尾的 `/responses` 替换为
  `/chat/completions`。

目标从最终主请求 URL 推导：保留上游节点基础路径和查询参数，并把已识别的入口操作后缀规范为
所选传输的 `/v1/messages`、`/v1/responses` 或 `/v1/chat/completions`。影子请求体也按所选
传输重新构造，不复用入口协议的图片块形状。

## 图片来源与请求头

Anthropic 图片块支持三种 `source.type`：

- `base64`：内联媒体类型和图片数据；
- `url`：由 Upstream 读取的远程图片 URL；
- `file`：Files API 的 `file_id`。

Chat `image_url` 支持完整 URL 或 base64 data URL。Responses `input_image` 支持完整 URL、
base64 data URL 或 `file_id`，并保留
`auto`、`low`、`high`、`original` detail 的缓存隔离。只处理直接消息内容中的
图片，不递归处理 Anthropic `tool_result.content` 或 Responses 工具输出中的嵌套
内容。

`openai_responses` 支持上述全部来源；`openai_chat_completions` 支持 URL 和
base64 data URL，但 `file_id` 没有等价格式，会在调用 Upstream 前返回 `400`。

智能路由开启视觉增强时还会校验视觉模型已明确支持视觉、具备上下文窗口和最大输出能力，且
配置的输出与单图提示预留可放入窗口。复合候选只保留回答模型和视觉模型共同支持的上游节点。
主回答前发生连接失败、超时、临时过载或畸形视觉响应时，可在剩余预算内切换兼容上游节点或
计划内模型；鉴权、请求/能力和不可重试 4xx 错误不会回退。显式模型仍只使用 primary。

影子请求只按协议白名单透传请求头，不透传任意客户端请求头：

- 两种协议共同透传 `Authorization` 和 `X-Api-Key`；
- Anthropic 另透传 `Anthropic-Version`；
- OpenAI 另透传 `OpenAI-Organization` 和 `OpenAI-Project`。

影子 HTTP 客户端不跟随重定向。Anthropic 不透传 `Anthropic-Beta`；其 `file_id`
只适用于无需 Beta 头即可读取文件的兼容 Upstream。OpenAI Responses 影子请求固定
设置 `store: false`，避免由 llm-proxy 请求 Upstream 持久化图片识别会话。

## 缓存与并发

成功描述保存在当前进程的 TTL + LRU 缓存中，重启后失效；失败结果不缓存。同一
缓存键的并发未命中会合并为一次影子请求，多图替换顺序仍与原请求一致。每次加载有一个
不可变的请求 owner；owner 的 context 控制物理调用。owner 取消时，仍存活的 waiter 会重新
竞选并使用自己的调用预算发起替代调用，已经开始的调用不会转移给 waiter。单独取消 waiter
不会影响 owner。

缓存键隔离以下内容：

- Profile、视觉调用接口、视觉模型和实际生成的上下文提示词；
- 图片来源类型和内容；
- 实际透传的协议相关鉴权/版本请求头。

因此，同一图片搭配不同问题时可以产生不同缓存条目。

`max_concurrency` 是单个 Profile 视觉预处理器的共享上限。`timeout` 从缓存加载
操作开始计算，包含等待并发槽、网络请求、重试延迟和响应读取。

## 统计、日志与安全

每个收到 `2xx` 且带可解析 usage 的影子请求以 `vision` 类型按实际协议和路径单独
记录 Token；即使响应没有可用描述并导致主请求失败，已产生的用量仍会记录。缓存命中
不新增记录。关键结构化日志包括：

- `vision.images.discovered`
- `vision.cache.waiter_joined`
- `vision.image.cache`
- `vision.shadow.attempt`
- `vision.image.completed` / `vision.image.failed`
- `vision.rewrite.completed`

日志只保留 Profile、传输、模型、图片索引/来源、状态、字节数、重试匹配、耗时、缓存来源，
以及随机生成的 `request_trace_id`、`owner_trace_id` 和 `call_id`。视觉描述、Upstream 响应正文、
鉴权头、原始 base64、URL、file ID、自定义提示词和用户文本均不写入持久日志。trace ID 只存在于
代理 context 和日志中，不作为请求头转发给 Upstream；安全随机源不可用时请求会失败，不生成
可预测的替代 ID。
