# 尝试预算、流式与会话

> [!TARGET]
> URL/default 在路由前固定一个 Profile；所有主回答、辅助调用、retry、fallback、事件与错误都携带同一 `ProfileScope(profile_id, generation, envelope_sha)`。后续阶段不得查找或 fallback 到另一 Profile，scope 不一致时必须在 reserve、dispatch 或响应适配前 fail closed。

## 统一 AttemptBudget

每个 Route 配置并经编译验证一个完整 `AttemptBudget`：

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

这些字段是同时生效的上限，不是彼此可替代的保底。每次 dispatch 前原子计数；排队、退避和 adapter 处理消耗同一 deadline。任一上限耗尽即停止，不能用另一未耗尽额度继续调用，也不能在主循环外补发请求。

### 计数规则

- 每次主回答 dispatch 增加 `answer_attempts` 和 `total_outbound_calls`。
- 对同一 Target 的 retry 额外增加该 Target 的 retry 计数，不增加 `target_switches`；它仍是一次主回答 dispatch，因此增加 answer/outbound 计数。
- Target ID 改变增加 `target_switches`；逻辑模型改变同时增加 `model_switches` 与 `target_switches`。
- 每次已校验的辅助 dispatch 增加 `auxiliary_calls` 和 `total_outbound_calls`，不增加 `answer_attempts`。
- reserve CAS 未成功且未 dispatch 时不增加 answer、auxiliary、total outbound 或 retry 计数；排队、退避和 adapter 处理仍消耗 deadline。若因此沿不可变计划前进到不同 Target，增加 `target_switches` 并继续消耗 deadline；若逻辑模型也改变，再增加 `model_switches`。reserve 成功后的实际 dispatch 才适用上述计数。

![AttemptBudget 生命周期](/diagrams/attempt-budget.svg)

图示等价说明：同一个 ExecutionPlan 为主回答和辅助调用提供有序 PlannedAttempt。dispatch 才消费回答或出站计数；未 dispatch 的 reserve 失败不增加计数，只继续消耗 deadline。可恢复的预提交失败只沿计划前进，完成、预算耗尽或 ClientCommit 后的错误都终止该分支。图中不存在嵌套重试、额外补发或另一 Profile 的回退路径。

## Reservation 与错误分类

Executor 在当前 ProfileRuntime 内先 reserve，成功后才 dispatch；完成以实际 usage 对账，未提交取消或连接失败按规则释放预留。reserve CAS 失败且未 dispatch 时不增加 answer、auxiliary、outbound 或 retry 计数，只消耗 deadline；它只能沿同一不可变计划进入下一 Target。若 Target ID 改变，增加 `target_switches`；若逻辑模型也改变，同时增加 `model_switches`。它不能重新评分、重排或访问全局 Registry。

- 调用方请求错误与 passthrough 401/403：不重试，也不污染 Target health。
- `credential_ref` 的 401/403：视为当前 Target 配置故障，跳过该 Target。
- connect、TLS 与可重试 5xx：仅在预算和计划允许时重试或切换。
- 429：记录有效 Retry-After 的 Profile-local cooldown；等待不得超出剩余 deadline。
- 上下文超限：先选择计划内、已验证上下文足够的同逻辑模型 Target；只有 auto Route 可沿已声明的 transition graph 换模型。

重复计费风险的请求仅在上游支持幂等键或 Route 明确允许 duplicate-spend 时重试。retry、Target fallback 和模型 fallback 只能在单一 Profile 的计划中发生；嵌套 retry × fallback、hedging 与无上限并行探测均被拒绝。

## ClientCommit 与流式边界

成功状态或首字节不构成提交。Executor 只有收到第一个完整、合法的入口协议事件后，才能一次性写客户端响应头与首事件，称为 `ClientCommit`。SSE 注释、心跳和空数据不构成 ClientCommit。

提交前的格式错误、断流或超时可在预算内沿计划 fallback；提交后禁止 retry 或 Target 切换，只能发送入口协议允许的终止错误事件或关闭连接。预提交缓冲和单事件大小均受 Route 配置和协议限制；超过限制按协议错误处理。Trace 必须记录 `client_committed_at`、首事件延迟和提交后的断流。

## 会话、幂等与缓存身份

逻辑模型连续性、Target affinity、实验分桶和单请求 PolicyVersion 是四种独立状态。会话连续性键必须从可信主体构造，使用带长度编码、用途和 key version 的 HMAC，并至少包含 tenant、`profile_id`、Route 与 session；没有可信 tenant/principal 时关闭跨请求绑定。

Target affinity、上游 session、cache key、prefix-cache affinity 与 idempotency key 必须包含 `profile_id`、Target ID 和 config revision。Header、credential、已编码请求和 transport handle 不能进入这些键、Trace 或缓存值。不同 Profile 即使 session ID、模型名或 endpoint 相同，也不能碰撞连接、health、session 或缓存命名空间。

## 计划内辅助调用与事件范围

视觉等同步辅助调用必须先作为 `PlannedAuxiliaryAttempt` 写入同一个 ExecutionPlan，使用同一 scope 与预算；它增加 auxiliary 和 total outbound 计数，但不是主回答尝试。辅助调用不得绕过计划、借用生产主回答额度，或在全局 Registry 中寻找 Target。

`DecisionEvent`、`AttemptEvent` 与 `OutcomeEvent` 都归属一个 Profile，并记录计划、预留、标准化错误、usage、成本、TTFT/E2E 与 ClientCommit。默认 Trace 不保存 prompt、图片、工具参数、完整响应、credential 或 Header；详细 Trace 只对该 Profile 的受权主体开放。

> [!CURRENT]
> 固定源码基线存在循环外额外调用与写头后立即流式转发的缺口，尚未证明统一 AttemptBudget、Profile-local reservation 或 ClientCommit 已实现。

> [!FUTURE]
> 多副本 reservation backend 可由后续执行面实现，但其预算、队列、取消、health、transport 与事件仍必须按同一 `profile_id` 分区，并通过并发与故障注入后才能标为 `RUNTIME_PROVEN`。
