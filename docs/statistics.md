# Token 用量统计

llm-proxy 捕获成功 Upstream 响应的内容；只要已捕获字节包含可解析的 usage，
就会将 Token 用量异步写入 `DATA_DIR/llm-proxy.db`。统计写入失败只记录日志，
不改变已经返回给调用方的代理响应。

统计页面位于受 Session 保护的 `/_admin/stats`，不提供独立的公开统计入口。

## 页面与筛选

页面展示汇总卡片、每日 Token 趋势图、网关和密钥用量构成图，并提供可按日期、模型、
网关或密钥切换的分组明细表；没有匹配数据时不显示空表。支持组合查询：

- 代理通道；
- 聚合网关；
- 调用方密钥；选择网关后加载该网关的密钥；
- 协议：`anthropic` 或 `openai`；
- 模型，不区分大小写；
- 请求类型：`main` 主请求或 `vision` 图片识别；
- 起止时间，浏览器按 RFC 3339 时间提交，边界包含在内。

没有时间筛选时，按日表和每日趋势图默认展示最近 30 天；汇总、按模型、按网关和按密钥
结果使用全部匹配记录。显式设置起止时间后，所有区域使用同一时间范围。

## Token 口径

- 请求数是一条成功解析并落库的 usage 记录。
- 总 Token 等于输入 Token 加输出 Token。
- Cache read 和 cache creation 单独展示，不重复加入总 Token。
- 每个真实成功的视觉影子请求单独记为 `vision`；缓存命中不新增记录。
- 主请求记为 `main`，并保存当时的代理通道 slug、协议、模型和代理通道相对
  request path。

删除代理通道后，历史记录仍保留 slug；外键 ID 会置空，因此按已删除代理通道 ID
无法再筛选这些记录。

代理通道引用内部聚合网关时，主请求和视觉影子请求同时归属该代理通道和网关；由于内部
调用不使用网关 Key，按密钥分组时显示为“内部代理通道 (网关 slug)”。直接访问
`/gateways/{slug}/...` 时，用量归属网关及通过认证的调用方密钥，不归属代理通道。

升级到支持网关统计之前产生的历史 usage 没有网关和密钥维度，无法可靠反推，统一显示为
“非网关流量”。网关和密钥名称以请求发生时的 slug、名称、前缀和尾号快照展示；数据库
不会在 usage 中保存密钥明文。

## 协议限制

Anthropic 的非流式 JSON 和 SSE 会解析模型、输入/输出 Token，以及
cache read / cache creation 输入 Token。

OpenAI Chat Completions 解析 `usage.prompt_tokens`、
`usage.completion_tokens` 和 `usage.prompt_tokens_details.cached_tokens`。
流式客户端通常需要请求：

```json
{
  "stream": true,
  "stream_options": {
    "include_usage": true
  }
}
```

OpenAI Responses 解析 `usage.input_tokens`、`usage.output_tokens` 和
`usage.input_tokens_details.cached_tokens`，并将
`usage.input_tokens_details.cache_write_tokens` 计入 cache creation；流式响应
通常从 `response.completed.response.usage` 获取这些字段。`total_tokens` 不单独
存储，页面仍以输入加输出计算总 Token；`output_tokens_details.reasoning_tokens`
也不单独展示。

Upstream 非成功响应或已捕获字节中没有可解析 usage 时不会产生记录。下游取消或
流读取中断通常会让捕获不完整；但如果 usage 已在中断前到达，仍可能异步落库。
输入和输出同时为零时也不会产生记录。llm-proxy 不估算缺失 Token。

## 持久化与运维

统计与代理通道、聚合网关、管理员和 Session 共用 SQLite，时间以 UTC 保存。服务关闭时
会等待已开始的异步写入完成。当前没有自动保留周期或清理任务；按
[运行配置](configuration.md)中的停机流程备份数据库，并自行制定容量和删除策略。
