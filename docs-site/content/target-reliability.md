# Target 调度、容量与健康

> [!TARGET]
> URL/default 在进入路由前固定一个 Profile。Target 调度、容量、health、circuit、cooldown 和 fallback 只在该 Profile、一个逻辑模型内运行；任何后续阶段不得选择、查找或 fallback 到另一个 Profile。

## Target 与有效能力

Target 是 Profile 内的一项原子模型部署，固定其逻辑模型、上游模型、endpoint、credential mode/ref、region、data policy、quantization、capability overrides、价格维度、capacity limits、failure domains 与 config revision。Target 没有协议覆盖字段，必须使用所属 Profile 的协议族和当前 operation。

有效能力为 `ModelCard ∩ Target`。ModelCard 定义逻辑模型的 catalog 能力，Target 只能收紧这些能力，不能凭空扩大；同 endpoint 的不同模型仍是不同 Target。Target 只能在 Profile 的 `upstream_envelope` 内选择 origin 和本地 credential alias，并可进一步收紧能力或地域；它不能放宽 protocol、credential authority、trust policy 或 envelope。

`failure_domains` 只可关联同一 `profile_id` 的 Target。即使不同 Profiles 恰好使用同一域名、区域、模型名或底层网络，也不能共享故障传播、credential、连接、容量或 health 状态。

![Target 调度与可靠性](/diagrams/target-reliability.svg)

图示等价说明：当前 Profile 的 Target 先经过硬约束、健康、credential、cooldown 与容量快照过滤，再由有界 scheduler 给出同逻辑模型的有序计划。Executor 在同 ProfileRuntime 内 reserve；失败只能消费预算并移向计划中的下一个同 Profile Target，最终是成功、按入口协议失败或 ClientCommit 后终止，不会转向另一 Profile。

## 资格、容量与预留

Router 读取当前 Profile 的只读容量快照，先过滤明显不合格的 Target。Scheduler 形成有序计划后，Executor 在该 ProfileRuntime 的 `AdmissionController.reserve()` 中原子预留 Target RPM、TPM、并发、route/tenant/principal 速率与金额预算、预计 input/output/auxiliary 成本、队列位置与最长排队时间。

完成后按实际 usage 对账并归还多余额度；取消、连接失败和未提交失败按显式规则释放。预留 CAS 失败只允许前进到同一计划内的下一 Target，且消耗同一 deadline 与 Target switch 预算；不能重新评分、重排、借用另一 Profile 额度或调用全局可变 scheduler。多副本全局配额属于后续执行面，但其键和一致性域仍必须以 `profile_id` 分区。

## 有界调度

Scheduler 可在已过滤的同 Profile、同逻辑模型候选中使用 ordered、weighted_random、p2c_peak_ewma、least_inflight、lowest_cost 或 consistent_hash。P2C、PeakEWMA 和 weighted 是有界选择策略，而不是全局运行时对象；随机或 P2C 使用 `decision_id` 派生 seed，并把候选指标快照写入 Trace。

Scheduler 不能隐式切换逻辑模型。模型切换只可由 auto Route 的已声明 transition graph 产生，并仍受 ExecutionPlan、硬约束和 AttemptBudget 约束；显式模型请求始终锁定其逻辑模型。Hedged request、无上限并行探测和嵌套 retry × fallback 均不在设计内。

## Health、circuit 与 cooldown

运行时状态按 Target fingerprint 在 ProfileRuntime 内管理，关键 endpoint、credential generation 或上游模型变更时重置。以下状态相互独立，避免一个错误类型误导另一个门：

- `circuit_state` 只由网络与可重试 5xx 的可用性窗口驱动。
- `rate_limit_until` 记录 Target/credential/model 配额作用域的 429 cooldown。
- `credential_state` 独立记录服务 credential 失效或配置问题。
- 指标窗口记录 TTFT、E2E、inflight、成功率和样本量。

调用方 4xx、内容拒答与评测流量不污染生产 health。半开探测必须单飞；故障域传播须记录触发来源和作用域，且绝不越过 `profile_id`。Profile A 的 401、429、5xx、circuit、容量耗尽或 reservation 竞争不能改变 Profile B 的候选资格或状态。

`last_resort` 默认关闭。即使管理员显式开启，它也只能忽略网络/5xx 导致的 `circuit_state=open`，不能绕过 credential fault、能力、隐私、地域、价格或 `rate_limit_until`。没有合格 Target 时返回安全的 `routing_no_eligible_target` 与适当 Retry-After；拒绝跨 Profile route、routing、fallback、evaluation 或 deployment。

## 失败边界

每次 dispatch 前消耗统一 AttemptBudget。retry、Target fallback 和允许的模型 fallback 都只能沿当前单 Profile ExecutionPlan 前进；任一尝试、总调用、deadline、成本或切换上限耗尽时立即停止。ClientCommit 前的可恢复失败可按计划切换；一旦收到首个完整合法协议事件并提交客户端，禁止重试或换 Target。

> [!CURRENT]
> 固定源码只显示单 upstream 与现有重试/流式行为，未证明 Target pool、reservation、health、circuit 或同 Profile scheduler 已实现。

> [!FUTURE]
> 后续多副本容量 backend、复杂指标窗口和额外调度算法必须保持 ProfileRuntime 独占可变状态，并通过故障注入与并发隔离验证后才可成为 `RUNTIME_PROVEN`。
