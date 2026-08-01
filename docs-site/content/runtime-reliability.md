# 视觉、重试与 Session

> [!TARGET]
> 在线任务分析、视觉辅助、主回答、重试和模型切换共享同一个请求级 AttemptBudget、deadline、取消信号和最坏成本上限。任务分析先消耗预算，随后生成的 ExecutionPlan 固定剩余预算。

## 视觉三态

系统先选择主模型，再决定图片处理方式：

1. 主模型支持视觉：原图按入口协议直接发送。
2. 主模型不支持视觉且当前 Profile 已开启增强、配置视觉模型：调用视觉模型生成与当前问题相关的描述，再替换图片内容。
3. 主模型不支持视觉且增强不可用：该模型从候选中排除。

视觉请求是 `PlannedAuxiliaryAttempt`，会消耗辅助调用数、总出站调用数、deadline 和成本。未开启视觉或未配置视觉模型时，不发起影子或辅助请求。

视觉缓存键至少包含 Profile、图片内容、视觉模型、传输协议、提示词、当前问题上下文和鉴权域。提示词或问题变化时不能复用旧描述，避免缓存把无关答案带入主模型。

## 统一 AttemptBudget

```text
max_answer_attempts
max_auxiliary_calls
max_total_outbound_calls
max_retries_per_target
max_target_switches
max_model_switches
deadline
max_worst_case_cost
```

每次在线出站前检查所有上限。任一上限耗尽就停止，不能在主循环外补发请求，也不能把视觉或任务分析器藏在预算之外。响应后的异步评测不占用已结束的在线请求预算，而是使用单独配置的抽样、每日金额、并发和单任务调用上限。

![统一尝试预算](/diagrams/attempt-budget.svg)

图示等价说明：任务分析先消耗请求预算，ExecutionPlan 再固定视觉辅助和主回答的剩余预算；所有在线出站统一消耗调用数、deadline 和最坏成本预算。只有 ClientCommit 前的可恢复故障可沿计划前进，提交后立即停止重试。

## ClientCommit

收到 HTTP 成功状态或第一个字节不等于提交。只有收到第一个完整、合法的入口协议事件并准备发送给客户端时，才形成 `ClientCommit`。提交前可以在预算内恢复；提交后禁止重试、Target 切换或模型切换。

## Session 固定模型

Session 绑定只对 `model=auto` 生效，不能覆盖调用方显式指定的模型。它用于减少同一连续任务
在模型之间来回跳动：

- 能力或上下文永久不足时，可以升级到更强模型并更新绑定；
- 临时 429、5xx 或 Target 故障只影响当前请求，不改变 Session 绑定；
- 系统不自动降级，管理员发布新策略或 Session 结束后再重新选择。

Session 键使用 HMAC 生成，输入至少包含原始 Session ID、Profile、Route、用途和调用方鉴权域；原始 Session ID 与凭据都不落库。请求缺少有效 Session ID 时，不建立跨请求绑定。

当前客户端通过 `X-LLM-Proxy-Session-ID` 提供稳定标识；代理只在存在受支持鉴权头时启用绑定，并在转发前移除内部 Session 头。TTL 默认 24 小时，可在 Profile 中设置 5 分钟到 30 天。

## Agent 上下文配置

`context_window` 记录真实容量，`client_context_window` 是推荐给 Agent 的保守窗口。后者应预留最大输出、工具结果、视觉描述和协议开销。百分比只用于后台即时计算建议，不需要保存；代理本身不压缩或截断上下文。

> [!CURRENT]
> 当前任务分析、视觉、主回答、重试和模型切换已统一纳入 ExecutionPlan、AttemptBudget 与 ClientCommit；每次视觉内部重试也会先占用预算。Session 绑定已实现同 Route 沿用、能力不足升级、临时故障不改绑定和自动过期。
