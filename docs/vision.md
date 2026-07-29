# 图片预处理

图片预处理是 Anthropic Profile 的核心补充：当选中的主模型不支持视觉输入时，
llm-proxy 先调用同一 Profile Upstream 的视觉模型生成描述，再以描述替换图片并
发送主请求。OpenAI Profile 不进入该流程。

## 工作流程与失败

1. 接收 Anthropic `POST /v1/messages` JSON 请求。
2. 收集 `messages[].content[]` 中的直接图片块。
3. 查询缓存，并并发处理未命中图片。
4. 用非流式影子请求获取描述。
5. 按原顺序将图片替换为带固定前缀的文本描述。
6. 使用同一 Profile 的重试与流式代理发送主请求。

任一图片失败时不会发送主请求。无效图片结构返回 `400`；影子请求的网络错误、
超时、非成功响应或空描述返回 `502`。客户端取消会结束相关工作。

完整数据流见[架构图](assets/llm-proxy-architecture.svg)。

## Profile 字段

在 `/_admin/profiles` 编辑 Anthropic Profile，展开“视觉参数”：

| 字段 | 默认值 | 说明 |
|---|---:|---|
| 启用视觉预处理 | 关闭 | 是否处理直接图片块 |
| 模型 | `sonnet` | 原样发送给 Upstream 的实际模型名 |
| 最大 Token | `2048` | 单张图片描述的输出上限 |
| 超时 | `2m` | 包含并发排队、请求、重试和读取 |
| 最大并发 | `4` | 该 Profile 所有请求共享的影子请求上限 |
| 缓存 TTL | `30m` | 成功描述的缓存时间 |
| 缓存条目上限 | `512` | LRU 容量 |
| 提示词 | 内置提示词 | 非空时完整替换内置提示词 |

模型字段不会读取 Agent 的 Sonnet、Haiku 或 Opus 映射；应填写 Upstream 实际接受
的视觉模型名。影子请求复用当前 Profile 的有序重试规则。

## 图片来源与请求头

支持 Anthropic 图片块的三种 `source.type`：

- `base64`：内联媒体类型和图片数据；
- `url`：由 Upstream 读取的远程图片 URL；
- `file`：Files API 的 `file_id`。

只处理 `messages[].content[]` 中的直接图片块，不递归处理
`tool_result.content`。

影子请求仅透传 `Authorization`、`X-Api-Key` 和 `Anthropic-Version`。
`Anthropic-Beta` 不会透传，且影子 HTTP 客户端不跟随重定向。因此 `file_id`
只适用于无需 Beta 头即可读取文件的兼容 Upstream。

## 缓存与并发

成功描述保存在当前进程的 TTL + LRU 缓存中，重启后失效；失败结果不缓存。同一
缓存键的并发未命中会合并为一次影子请求，多图替换顺序仍与原请求一致。

缓存键隔离以下内容：

- Profile、视觉模型和提示词；
- 图片来源类型和内容；
- 实际透传的三个鉴权/版本请求头。

`max_concurrency` 是单个 Profile 视觉预处理器的共享上限。`timeout` 从缓存加载
操作开始计算，包含等待并发槽、网络请求、重试延迟和响应读取。

## 统计、日志与安全

每个真实成功的影子请求以 `vision` 类型单独记录 Token；缓存命中不新增记录。
关键结构化日志包括：

- `vision.images.discovered`
- `vision.image.cache`
- `vision.shadow.attempt`
- `vision.image.completed` / `vision.image.failed`
- `vision.debug.description`
- `vision.debug.upstream_response`
- `vision.rewrite.completed`

视觉描述和 Upstream 非成功响应正文会写入调试日志，最多保留 4096 个 Unicode
code point。代理不会主动把鉴权头、原始 base64、URL、file ID 或自定义提示词
作为独立结构化字段记录；但这些输入会发送给 Upstream，可能被其回显到错误响应
正文中。因此应把所有视觉日志视为敏感数据，限制访问、留存和导出范围。
