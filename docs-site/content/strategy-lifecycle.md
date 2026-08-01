# 策略生命周期、观测与重放

> [!TARGET]
> 策略控制面以单一 Profile 为一致性域。URL/default 和授权路径先确定 Profile；Draft、Catalog、Target、Deployment、Trace、Feedback、Evaluation 与 Replay API 都以 `(profile_id, local_id)` 查询，拒绝正文覆盖作用域，也不得在后续阶段查询或 fallback 到另一 Profile。

## 不可变策略与发布对象

`PolicyDraft` 可修改但使用 revision CAS；`PolicyVersion` 由 validate/compile 生成后不可变，包含 schema version、内容 SHA、父版本和 catalog、adapter、price snapshot 引用。编译器检查规则引用、能力、协议、envelope、预算、价格、故障域和整个对象图的单一 `profile_id`；任何错误都不替换 last-known-good。

`RouteDeployment` 将一个 Route 指向已验证 PolicyVersion，并保存 `decision_shadow`、canary、active、rolled_back、epoch 与精确 last-known-good。Policy、catalog、adapter、price、EvaluationReport、generation 和 envelope SHA 的范围不匹配时，部署必须拒绝；不能用名称或最新快照替代精确绑定。

![策略生命周期](/diagrams/strategy-lifecycle.svg)

图示等价说明：一个 Profile-scoped Draft 经 CAS、校验、编译成为不可变 PolicyVersion；dry-run、decision_shadow 与评测在同一 scope 产生证据，随后创建 canary Deployment。人工提升或受权可靠性门禁 CAS 回滚到精确 last-known-good；每一步都绑定 snapshot SHA，没有跨 Profile 发布或自动提升路径。

## 发布流程

1. 修改 Draft，并以 revision CAS 防止覆盖并发变更。
2. validate/compile 为不可变 PolicyVersion；失败保留当前 last-known-good。
3. simulator 针对固定请求进行 dry-run/explain，不发送生产流量。
4. `decision_shadow` 只记录同 Profile 的候选计划，不执行额外模型调用，也不改变选择。
5. 绑定精确 SHA 的 EvaluationReport 后创建 canary RouteDeployment。
6. 人工逐级提升；只有预先授权的可靠性硬门禁可通过 CAS 回滚 active pointer 到精确 last-known-good，并标记失败 deployment 为 `rolled_back`。

学习系统、advisor 与评测不能自动将部署变为 active，也不能改写 deployment pointer。没有有效 last-known-good 时，auto Route fail closed；显式迁移 Route 是否可用由其独立部署状态决定。

## 稳定分配与可观测性

ExperimentAssignment 使用域分离 HMAC，至少包含 `profile_id`、generation、envelope SHA、Route、Deployment、epoch、可信 tenant/principal、purpose 与 key version。相同 local ID 不同 Profile 必须有不同 assignment namespace；旧 generation 的 assignment 不可沿用。

每个请求记录 `decision_id` 和 Profile-scoped 事件：

- `DecisionEvent`：入口事实摘要、策略版本、规则结果、候选排除、选择输入与 ExecutionPlan。
- `AttemptEvent`：Target、adapter、reservation、时间、状态、标准化错误、usage、成本、TTFT/E2E 与 ClientCommit。
- `OutcomeEvent`：最终逻辑模型、完成原因、总尝试、总成本与 fallback 计数。

普通响应可公开 decision ID、逻辑模型和 fallback count；Target、规则细节与原始错误仅对同 Profile 的受权主体开放。聚合指标只能是清洗后的不可变值，不能成为跨 Profile 路由输入。

## Trace、Replay 与密文范围

默认 Trace 不保存 prompt、图片、工具参数或完整响应，只保存长度、类型、Profile-scoped HMAC 摘要与必要特征。摘要的域至少包含 tenant、`profile_id`、generation、envelope SHA、trace ID、purpose 与 key version；credential 和 Header 永不进入摘要域或事件载荷。

Trace explanation 解释已记录的事实与结果，不重新计算请求。decision replay 只有在授权 payload、完整输入事实、seed、算法版本与全部指标快照同时具备时才可在原 scope 重算；execution replay 也只在原 Profile 发起新的执行，不保证历史输出复现。旧 generation 仅允许 explanation，不能进行 decision、execution 或 payload replay。

Payload Replay 需要显式开启、独立 RBAC、Profile-scoped 派生密钥、短 TTL 加密 spool 与审计。AEAD AAD 至少绑定 tenant、`profile_id`、generation、envelope SHA、trace ID、purpose 与 key version；替换 scope、密文或 AAD 必须认证失败，且失败时不得回查全局 Registry。

## 失败与审计

Profile 收紧、热加载或 tombstone 后，旧 Plan、Replay、EvaluationJob、QueueEntry 与 ExperimentAssignment 的 generation/envelope SHA 校验必须在解密、reserve 或 dispatch 前失败。协议、credential authority/namespace、trust-domain identity、ID/slug 复用与 envelope 扩张均被拒绝。

发布、回滚、详细 Trace 与 Payload Replay 使用独立权限并写审计日志。不同 Profiles 禁止跨 Profile 路由、fallback、评测或部署；任何跨 scope Policy、EvaluationReport 或 Experiment candidate 都不能成为 active 或 last-known-good 的输入。

> [!CURRENT]
> 当前源码基线仅证明原子 Registry 快照替换的可复用方向，未证明 PolicyVersion、CAS、Deployment、LKG、Trace 或 Replay 已实现。

> [!FUTURE]
> 更复杂的发布保护或独立审计存储可在后续控制面补充，但必须保留精确快照绑定、单 Profile 作用域和 stale-generation fail-closed 语义。
