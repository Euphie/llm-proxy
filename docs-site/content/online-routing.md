# 在线决策流水线

> [!TARGET]
> 在线路由是 Profile-local 的确定性计算：URL/default 已固定一个 Profile 后，Router 只读该 Profile 的已发布快照和本地运行时状态。任何后续阶段都不得选择、查询或 fallback 到另一个 Profile；发现 scope 不一致即 fail closed。

## 从请求到选择

入口 adapter 将已验证的请求归一化为携带 ProfileScope 的 `InferenceRequest`，并产生可信身份。编译策略按以下有限链路运行：

```text
facts -> local signals -> typed projections -> compiled rules
  -> hard constraints -> logical model selector
  -> Target eligibility + capacity snapshot -> Target scheduler
  -> immutable ExecutionPlan -> Executor
```

Facts 包含入口 operation、显式模型、token 估算、模态、工具、结构化输出、流式、可信租户、地域和预算。Signals 首版仅是本地确定性信号；projections 是有类型的派生值；rules 支持编译后可检查的 AND/OR/NOT、tier、priority 与稳定名称排序。规则引用、环、死分支、能力语义、adapter 可达性、尝试上界、价格、envelope 与所有 `profile_id` 归属均由编译器校验。

![在线决策流水线](/diagrams/routing-pipeline.svg)

图示等价说明：单一 ProfileScope 依次流经事实和本地信号、规则、硬过滤、逻辑模型选择、同模型 Target 资格与调度，形成单一不可变计划。Executor 只使用该计划中同 Profile 的 Target ID；决策、尝试和结果事件返回同一 scope 的 Trace，不存在回到 Registry 或另一 Profile 的箭头。

## 请求语义

### 显式模型

显式模型精确锁定逻辑模型，跳过 Model Selector，`max_model_switches=0`。它仍必须通过能力、安全、地域、价格和容量过滤，并只能在已声明为该逻辑模型的同 Profile Target 之间调度。未知模型返回入口协议的 4xx；不透传名称，也不改用另一 Profile。

### 自动与命名 Route

`model=auto` 选择当前 Profile 的默认 Route；`model=route:<slug>` 选择其命名 Route。Route 的 transition graph 只允许已声明的逻辑模型转换，且 fallback 仅影响当前请求，不永久改写会话绑定。没有 Route、没有合格候选或预算耗尽时必须 fail closed。

## 硬过滤、资格与调度

决策顺序不可颠倒：先检查入口协议与身份；再过滤能力、上下文、工具、隐私、地域、ZDR、量化和价格上限；随后执行 Route 质量门禁与显式模型契约；最后读取当前 Profile 的生产健康、credential 状态和容量快照，确定 Target 资格。可靠性偏好先于成本或延迟优化。

Target Scheduler 只能在当前 Profile、一个逻辑模型内工作，使用无秘密的 ProfileSnapshot。允许的有界策略包括 ordered、weighted_random、p2c_peak_ewma、least_inflight、lowest_cost 与 consistent_hash；随机和 P2C 由 `decision_id` 派生 seed，并记录候选指标快照。Scheduler 不直接持有可变 registry，不隐式换模型，也不以全局分数跨量纲排序。

Executor 在 ProfileRuntime 内对计划中的顺序候选执行 `AdmissionController.reserve()`。预留失败只能前进到同一计划内的下一 Target，并消耗同一 deadline 和切换预算；不得重新评分、重排或查找别的 Profile。

## 不可变 ExecutionPlan

计划至少保存 `profile_id + runtime_generation + envelope_sha`、Policy/catalog/deployment/adapter/price snapshot SHA、有序 `PlannedAttempt[]`、`PlannedAuxiliaryAttempt[]`、逻辑模型转换、选择时指标快照与完整 `AttemptBudget`。预算共同约束回答尝试、辅助调用、总出站调用、每 Target 重试、Target/模型切换、deadline 与最坏成本。

每个 PlannedAttempt 只含 Profile-local Target ID、config revision、用途和预算归属；不含 endpoint、credential、payload、已编码请求或 transport handle。所有同步辅助调用同样必须先作为同 Profile 的 PlannedAuxiliaryAttempt 编入计划，不能绕过计划隐式出站。

Executor 接收 `(ProfileScope, InferenceRequest, ProfileRuntime, ExecutionPlan)` 后，先核对 request、plan、runtime、当前 snapshot、Trusted Identity 与响应 adapter context 的 scope 三元组，再解析同 Profile Target revision。任何第二个 `profile_id`、generation/envelope SHA 不匹配、缺失或篡改，都在解密、reserve 或 dispatch 前失败。

## 热路径与 Trace

线上 Router 不访问数据库、远程 analyzer、judge 或模型服务。`decision_shadow` 只在同一 ProfileSnapshot 中运行本地、无出站的 advisor，并只记录候选计划；远程候选推理属于异步、同 Profile 的 `evaluation_shadow`，不能改变当前请求选择。

Trace explanation 是预计算的 Decision/Attempt/Outcome 记录：展示已记录的事实、规则结果、排除原因、选择输入、计划和结果，而不重新执行请求。只有授权 payload、完整输入事实、seed、算法版本和所有快照同时存在时，才可在原 scope 重算 decision replay；否则必须拒绝。

> [!CURRENT]
> 当前源码未证明规则编译、计划、容量预留或 Trace 已实现；本页描述的是可验证的目标流水线。

> [!FUTURE]
> 学习系统可离线提出候选，但不得在请求热路径改写规则、候选集合或优先级。任何可影响线上选择的变更需经过同 Profile 评测与发布生命周期。
