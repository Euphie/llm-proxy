# Token 用量统计

llm-proxy 捕获成功 Upstream 响应的内容；只要已捕获字节包含可解析的 usage，
就会将 Token 用量异步写入 `DATA_DIR/llm-proxy.db`。统计写入失败只记录日志，
不改变已经返回给调用方的代理响应。

统计页面位于受 Session 保护的 `/_admin/stats`，不提供独立的公开统计入口。

## 页面与筛选

页面展示汇总、按日和按模型表格，支持组合筛选：

- Profile；
- 协议：`anthropic` 或 `openai`；
- 模型，不区分大小写；
- 请求类型：`main` 主请求或 `vision` 图片识别；
- 起止时间，浏览器按 RFC 3339 时间提交，边界包含在内。

没有时间筛选时，按日表默认展示最近 30 天；汇总和按模型结果使用全部匹配记录。
显式设置起止时间后，三个区域使用同一时间范围。

## Token 口径

- 请求数是一条成功解析并落库的 usage 记录。
- 总 Token 等于输入 Token 加输出 Token。
- Cache read 和 cache creation 单独展示，不重复加入总 Token。
- 每个真实成功的视觉影子请求单独记为 `vision`；缓存命中不新增记录。
- 主请求记为 `main`，并保存当时的 Profile slug、协议、模型和 Profile-relative
  request path。

删除 Profile 后，历史记录仍保留 slug；外键 ID 会置空，因此按已删除 Profile ID
无法再筛选这些记录。

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
显式返回输入、输出都为零的 usage 会记录为零；缺少任一计价维度时不会把默认零当成完整 usage。
llm-proxy 不估算缺失 Token。

## Auto 路由轨迹

统计页的“最近 Auto 路由”跟随 Profile 和时间筛选，默认展示最近 100 条。每条轨迹包含任务
判断来源、策略/Route、计划模型路径、实际模型、视觉方式、结果状态、调用预算、计划最坏费用、
已消费估算费用、当前持有容量、usage 已知实际费用和耗时。

计划最坏费用用于预算保护；已消费估算按真正发出的分析、视觉和回答调用累计。若完整 usage 和
输入/输出价格都存在，系统同时记录已知实际费用；usage 缺失、畸形、溢出或出现尚未建模价格的
缓存读写维度时，实际费用保持未知，不能用估算冒充。显式零价格是已知零费用。

管理 API 可通过 `GET /_admin/api/routing-calls?trace_id=<ID>&limit=100`，或使用
`correlation_id`，查看该轨迹最多 500 条逐物理调用记录。记录只含模型、Target、图片/重试/切换
序号、估算/实际费用、状态和结果分类；不保存原始 prompt、图片、请求头、凭据或完整响应。

## 持久化与运维

统计与 Profiles、管理员和 Session 共用 SQLite，时间以 UTC 保存。服务关闭时会
等待已开始的异步写入完成。当前没有自动保留周期或清理任务；按
[管理控制台](admin.md)的停机流程备份数据库，并自行制定容量和删除策略。
