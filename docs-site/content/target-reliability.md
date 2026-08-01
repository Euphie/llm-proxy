# Target 与容错

> [!TARGET]
> ModelCard 描述逻辑模型，Target 描述当前 Profile 中实际调用的部署。v1 每个模型先使用一个 Target；多 Target 是后续的同 Profile 扩展。

## 为什么要分开

同一个逻辑模型可能因为区域、endpoint 或部署方式不同，具有不同价格、延迟和可用性。把能力放在 ModelCard、把部署状态放在 Target，可以避免因为 endpoint 故障就误判模型本身不适合任务。

![Target 容错流程](/diagrams/target-reliability.svg)

图示等价说明：系统先按 ModelCard 判断模型是否满足能力，再检查当前 Profile 内 Target 的健康和容量。可恢复故障只沿 ExecutionPlan 和 AttemptBudget 重试或切换；没有合格 Target 时返回明确错误，禁止跨 Profile 回退。

## v1 行为

- 每个参与模型配置一个 Target。
- Target 保存上游模型 ID、endpoint、价格覆盖、健康和限流状态。
- 显式模型失败时只按该模型的重试规则执行，不智能换模型。
- `auto` 只有在策略允许且预算未耗尽时，才可切换到另一个已登记模型。

## 错误如何处理

- 请求格式、鉴权失败和不可重试 4xx：直接返回，不污染模型质量数据。
- 连接错误和可重试 5xx：ClientCommit 前可在预算内重试。
- 429：尊重 Retry-After；等待不能超过总 deadline。
- 上下文不足或能力不匹配：`auto` 可在计划内升级模型；显式模型返回明确错误。
- ClientCommit 后断流：不再重试或切换，避免重复输出和重复计费。

## 后续多 Target

多 Target 只允许同一 Profile、同一供应商、同一逻辑模型的部署。可采用容量准入、加权、P2C/PeakEWMA、最少并发或最低延迟等有界调度。它负责部署容错，不负责判断任务该用哪个逻辑模型。

> [!CURRENT]
> 当前 Profile 仍是单 upstream；Target 对象、容量准入和多 Target 健康调度尚未实现。
