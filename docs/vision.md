# 图片预处理

图片预处理是 anthropic-proxy 的核心功能之一，主要解决主模型不支持视觉输入的问题。

客户端仍按 Anthropic Messages 格式发送图片。代理识别请求中的图片块，调用同一 Provider 中配置的视觉模型生成详细描述，再将图片替换为文本描述后提交给主模型。主模型只需要处理文本，也能理解截图、界面布局、报错信息、代码和图表。

## 工作流程

1. 接收 Anthropic `POST /v1/messages` JSON 请求；
2. 收集 `messages[].content[]` 中的直接图片块；
3. 查询进程内缓存，并对未命中图片发起非流式影子请求；
4. 等待所有图片识别成功；
5. 将图片替换为带固定前缀的文本描述；
6. 按原有重试和流式转发流程提交主请求。

任一图片识别失败时，代理不会发送主请求。非法图片请求返回 `400`，识图上游错误、超时或无有效描述返回 `502`。

完整数据流见[架构图](assets/anthropic-proxy-architecture.svg)。

## 启用

图片预处理只能用于 `protocol: anthropic` 的 Provider：

```yaml
providers:
  my-provider:
    upstream: https://your-anthropic-compatible-endpoint.com
    protocol: anthropic
    vision:
      enabled: true
      model: Kimi-K2.5
```

`vision.model` 会原样发送给上游。代理不会读取 Claude Code 的下列环境变量映射：

- `ANTHROPIC_DEFAULT_HAIKU_MODEL`
- `ANTHROPIC_DEFAULT_SONNET_MODEL`
- `ANTHROPIC_DEFAULT_OPUS_MODEL`

例如 Claude Code 将 `sonnet` 映射为 `Kimi-K2.5` 时，仍应显式配置 `vision.model: Kimi-K2.5`。

## 配置项

除 `enabled` 外，其余字段都可以省略并使用默认值：

| 字段 | 默认值 | 说明 |
|---|---:|---|
| `enabled` | `false` | 是否启用图片预处理 |
| `model` | `sonnet` | 上游实际识图模型名称 |
| `max_tokens` | `2048` | 单张图片描述最大输出 Token |
| `timeout` | `2m` | 单图总时限，包含并发排队和重试 |
| `max_concurrency` | `4` | 当前进程共享的影子请求并发上限 |
| `cache_ttl` | `30m` | 成功描述的缓存时间 |
| `cache_max_entries` | `512` | LRU 缓存最大条目数 |
| `prompt` | 内置提示词 | 非空时完整替换内置提示词 |

需要调优时再显式配置：

```yaml
vision:
  enabled: true
  model: Kimi-K2.5
  max_tokens: 2048
  timeout: 2m
  max_concurrency: 4
  cache_ttl: 30m
  cache_max_entries: 512
  # prompt: 自定义图片描述提示词
```

## 图片来源

支持 Anthropic 图片块的三种 `source.type`：

- `base64`：内联图片数据；
- `url`：由上游读取的远程图片 URL；
- `file`：Files API 的 `file_id`。

只处理 `messages[].content[]` 中的直接图片块，不递归处理 `tool_result.content`。OpenAI 协议请求不会进入图片预处理。

影子请求仅透传：

- `Authorization`
- `X-Api-Key`
- `Anthropic-Version`

`Anthropic-Beta` 不会透传。因此 `file_id` 只适用于无需 Beta 头即可读取文件的兼容上游。

## 缓存与并发

成功描述保存在进程内 TTL + LRU 缓存中，重启后失效。相同图片的并发缓存未命中会合并为一次影子请求。

缓存按以下范围隔离：

- Provider、视觉模型和提示词；
- 图片来源及图片内容；
- 实际转发的鉴权头和 `Anthropic-Version`。

多图请求会并发识别，但替换顺序与原请求一致。`max_concurrency` 是当前进程所有请求共享的上限，`timeout` 包含等待并发槽、网络请求、重试等待和响应读取。

影子请求复用当前 Provider 的 `overload_rules`。规则为空时不执行过载匹配或重试。

## 统计与日志

每次成功执行的影子识图请求以 `/v1/messages#vision` 单独计入 Token 统计；缓存命中不新增记录。

诊断日志包括：

- `vision.images.discovered`
- `vision.image.cache`
- `vision.shadow.attempt`
- `vision.debug.description`
- `vision.debug.upstream_response`
- `vision.rewrite.completed`

识图描述和失败的上游响应会写入日志，最多保留 4096 个字符。图片中可能包含密钥、代码或个人信息，应限制日志访问和留存范围。
