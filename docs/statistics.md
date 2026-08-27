# Token 用量统计

llm-proxy 捕获成功 Upstream 响应的内容；只要已捕获字节包含可解析的 usage，
就会将 Token 用量异步写入 `DATA_DIR/llm-proxy.db`。统计写入失败只记录日志，
不改变已经返回给调用方的代理响应。

统计页面位于受 Session 保护的 `/_admin/stats`，不提供独立的公开统计入口。页面分为：

- `/_admin/stats`：用量统计；
- `/_admin/stats/routing`：路由轨迹；
- `/_admin/stats/models`：公开评测、本地 Agent 轨迹与模型表现。

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

## 路由轨迹

路由轨迹使用服务端分页，每页可选 25、50 或 100 条，并按正常、高风险、分析回退、已升级/切换、
失败和费用异常归类。分析失败或风险置信度不足显示为“风险未知 · 强模型兜底”，不计入高风险。
可继续按 Profile、任务类型、难度、风险、模型、判断来源、视觉方式和时间筛选。
每条轨迹可展开查看综合置信度、任务类型/复杂度/风险三类置信度、信息不足标记、命中的复杂度信号和
判断原因，以及候选通过/排除原因和任务分析、视觉、回答的物理调用链。
筛选、分类和页码会同步到浏览器地址，可直接收藏或分享当前视图。

计划最坏费用用于预算保护；已消费估算按真正发出的分析、视觉和回答调用累计。若完整 usage 和
输入/输出价格都存在，系统同时记录已知实际费用；usage 缺失、畸形、溢出或出现尚未建模价格的
缓存读写价格时，实际费用保持未知，不能用估算冒充。Anthropic 与 OpenAI 的缓存 Token 口径会在
核算前归一化，OpenAI 输入总数不会与缓存读取/写入重复计价。显式零价格是已知零费用。

管理 API 可通过 `GET /_admin/api/routing-calls?trace_id=<ID>&limit=100`，或使用
`correlation_id`，查看该轨迹最多 500 条逐物理调用记录。记录只含模型、图片/重试/模型切换
序号、估算/实际费用、归一化后的输入/输出/缓存 Token、状态和结果分类；不保存原始 prompt、
图片、请求头、凭据或完整响应。成功回答调用的这些 Token 会按最近 30 天、Profile 和逻辑模型
汇总，作为下一次 Auto 候选排序的缓存比例依据。

## 模型表现

模型表现页明确区分三类数据：

1. **公开评测**：只用于冷启动和生成可编辑策略，不代表当前 Upstream 的真实质量；
2. **本地 Agent 评测**：真实 Agent 工具任务的采集、评测和审计状态；
3. **本地质量证据**：评审成功后按模型与任务维度聚合的保守指标。

本地 Agent 轨迹按 Profile、模型、状态、来源、风险和日期筛选。点击“查看轨迹”可解密查看已脱敏的
用户任务、模型工具调用、客户端工具结果和最终回答，也可立即删除。轨迹只在首次明确完成时评测一次，
不提供重新评测接口：完成请求的摘要会随轨迹持久化，在 7 天保留期内即使服务重启后重放也不会再次进入评测；
管理员删除轨迹时会同时删除该摘要。评测依赖当次请求仅在内存中的上游凭据，服务不会保存凭据供以后重放。

本地质量证据按 Profile、策略、Route、任务类型、难度、风险、候选模型、强模型基线和视觉方式分组，
并使用服务端分页。每组包括：

- 正确性、完整性、指令遵循、格式与工具安全、任务完成度五维保守下界；
- 动态总质量、严重错误率上界、原始/有效样本和可靠性；
- 候选、基线和评审成本，以及主动升级准确率与漏升率。

证据使用 20 个等效样本的先验、30 天半衰期和单侧 95% 保守边界。旧版总体胜负只显示为历史
`overall` 证据，不会伪装成五维样本。异步评测成本单独展示，不计入在线请求成本。
先验来自该组证据对应的不可变策略版本；历史策略已不存在时，页面会标记“策略先验不可用”，
并采用中性先验展示参考值。模型表现页的筛选和页码同样会写入浏览器地址。

## Agent 轨迹保存与清理

- 只采集包含工具调用的真实 Agent 任务；代理观察工具调用和结果，但不会执行工具。
- 有 Session ID 时跨请求合并；没有 Session ID 时只使用客户端在当前请求中提供的完整历史快照。
- Anthropic `end_turn`、Chat Completions `stop` 或 Responses 完成且没有新函数调用时，任务才算完整。
- 30 分钟没有最终回答会标记为“未完整结束”，仅用于审计，不进入质量评测。
- 高风险、混用多个模型或内容被截断的任务仅保留审计，不归因到模型质量。
- 单条解密后轨迹（包括最终回答）最多保留 2 MiB；超过上限会截断并转为仅审计。
- 原始轨迹先脱敏，再使用 `DATA_DIR/agent-trajectory.key` 进行 AES-256-GCM 加密；密钥文件权限必须为 `0600`。
- 后台维护任务每分钟标记超时轨迹并删除已保留 7 天的记录；服务重启时，已完成但尚未入队、排队中或评测中的记录会标记为中断，不自动重试。

## 持久化与运维

统计与 Profiles、管理员和 Session 共用 SQLite，时间以 UTC 保存。服务关闭时会
等待已开始的异步写入完成。用量、路由和聚合证据当前没有统一自动保留周期；Agent 原始轨迹独立按
7 天自动清理。备份数据库时应同时备份 `agent-trajectory.key`，否则历史轨迹无法解密，但仍可删除。
