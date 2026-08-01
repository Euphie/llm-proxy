# Target 与容错

> [!TARGET]
> ModelCard 描述逻辑模型，Target 描述当前 Profile 中实际调用的端点。Upstream 是 primary；
> `model=auto` 可在预算内按配置顺序切换同模型备用 Target，但不能越过当前 Profile。

## 为什么要分开

同一个逻辑模型可能因为区域、endpoint 或部署方式不同，具有不同价格、延迟和可用性。把能力放在 ModelCard、把部署状态放在 Target，可以避免因为 endpoint 故障就误判模型本身不适合任务。

![Target 容错流程](/diagrams/target-reliability.svg)

图示等价说明：系统先按 ModelCard 判断模型是否满足能力，再把该模型的 primary 与匹配的备用
Target 顺序冻结进 ExecutionPlan。可恢复故障只沿 ExecutionPlan 和 AttemptBudget 重试或切换；
没有可用 Target 时才尝试计划中的下一个模型，禁止跨 Profile 回退。

## 当前行为

- Profile Upstream 自动成为 `primary`，支持模型目录中的全部模型。
- 最多配置 16 个有序备用 Target，每个备用项声明 endpoint 和精确模型列表。
- 备用 Target 只允许同一供应商、同一协议和同一凭据语义；代理不转换鉴权。
- 显式模型失败时只按该模型的重试规则执行，不智能换模型。
- `auto` 先重试当前 Target，再按顺序切换同模型 Target，最后才切换计划内模型。
- 每次 Target 切换原子占用 Target 切换次数、回答次数、总调用数和费用预算。

## 错误如何处理

- 请求格式、鉴权失败和不可重试 4xx：直接返回，不污染模型质量数据。
- 连接错误和命中容错规则的响应：ClientCommit 前可在预算内重试或切换 Target。
- 等待不能超过总 deadline。
- 上下文不足或能力不匹配：`auto` 可在计划内升级模型；显式模型返回明确错误。
- ClientCommit 后断流：不再重试或切换，避免重复输出和重复计费。

## 后续增强

当前使用管理员确定的静态顺序，行为简单且可解释。未来可在不改变 Profile 边界和预算合同的前提
下增加被动健康窗口、容量准入或 P2C/PeakEWMA；这些机制只优化部署选择，不负责判断任务该用
哪个逻辑模型。

> [!CURRENT]
> 当前已实现 Target 配置校验、不可变顺序、同模型 Target 切换、独立预算计数、异步评测按模型
> 选端点，以及路由轨迹中的初始/最终 Target。主动健康探测和容量调度尚未实现。
