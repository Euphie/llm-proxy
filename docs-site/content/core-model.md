# 核心对象与配置模型

> [!TARGET]
> 所有可路由对象只属于一个 `profile_id`。`Profile` 本身就是用户可见的 Provider 实例、URL 入口、配置命名空间和唯一隔离边界；不存在独立 Provider 实体或分类字段。对象没有归属、归属不一致或引用越过 Profile 时，编译、API 和执行均必须 fail closed。

## Profile：唯一边界

Profile 固定 `protocol ∈ {anthropic, openai}`，并定义不可由 Target 放宽的 `upstream_envelope`：允许 origin、credential authority 与 namespace、Header allowlist、TLS/DNS/proxy trust policy 和允许数据地域。Target 只能从 envelope 中选择 endpoint 与本地 credential alias，并可进一步收紧能力或地域；越出 envelope 的配置在发布前被拒绝。

同一 Profile 可以包含多个账号、区域或 endpoint Target，但这些 Target 必须同时属于同一个上游服务商和同一 trust-domain identity，并受同一 `upstream_envelope` 约束。不同上游服务商必须使用不同 Profile；由独立主体管理的 trust-domain identity 也必须使用不同 Profile。Route、策略和运行时都不能把后一类 Target 塞进当前 envelope。

Profile Resolver 在 URL 和授权校验后生成服务端拥有的 `ProfileScope`：`profile_id + generation + envelope_sha`。请求正文和 Header 不能提供或覆盖该作用域。协议或 operation 不匹配时在 Router 前失败，不查询其他 Profile。

Profile 的身份不能原地改造：删除后的 ID 与 slug 只写 tombstone；协议、credential authority/namespace 与 trust-domain identity 创建后不可修改；扩张或替换 envelope 必须复制为新 Profile。每个允许更新递增 generation 并产生新的 envelope SHA，使旧 Plan、Replay、EvaluationJob 和实验分配在 reserve 或 dispatch 前失效。

## 路由对象

### Route 与 ServiceObjective

`Route` 只归属一个 Profile，绑定候选 ModelCard、`ServiceObjective`、`RouteDeployment`、尝试预算、身份策略和数据策略。`model=auto` 选择该 Profile 的默认 Route，`model=route:<slug>` 选择保留命名空间中的命名 Route。Route 不得引用另一个 Profile 的模型、Target、策略、评测证据或实验候选；违反时编译器拒绝发布。

`ServiceObjective` 将能力、隐私、地域、价格、质量与可靠性表达为硬约束和有序目标。不同量纲不做全局加权求和：先淘汰不满足约束的候选，再以词典序或 Pareto 条件优化成本或延迟。每条 Route 的质量门禁必须绑定其自身基线和证据，缺少证据就不能提升部署。

### ModelCard 与 Target

`ModelCard` 是 Profile 内的逻辑模型：稳定 ID、能力、上下文窗口、模态、工具/结构化输出支持、质量证据引用和允许的模型转换图均来自版本化 catalog。不同 Profile 的同名模型是互不相关的对象，不能按名称推断等价性。

`Target` 是同一 Profile 内的一项原子模型部署，至少包含：

```text
id
profile_id
logical_model_id
upstream_model_id
endpoint
credential_mode / credential_ref
region / data_policy / quantization
capability_overrides
price_dimensions
capacity_limits
failure_domains
config_revision
```

Target 不携带协议覆盖字段，只能使用所属 Profile 的协议族和当前 operation。有效能力为 `ModelCard ∩ Target`，所以 Target 只能收紧而不能扩大能力；`failure_domains` 也只能连接同一 `profile_id` 的 Target。Scheduler 只能在当前 Profile、一个逻辑模型的候选中选择，不能隐式换模型或越过 ProfileScope。

## 策略、发布与运行时

`PolicyDraft` 是带 revision CAS 的可修改对象；`PolicyVersion` 是编译后的不可变对象，保存 schema、内容 SHA、父版本以及 catalog、adapter、price snapshot 引用。`RouteDeployment` 将 Route 指向一个 PolicyVersion，并记录 `decision_shadow`、canary、active、rolled_back、epoch 与 last-known-good。编译必须验证整个引用图只有一个 `profile_id`；失败时保留 last-known-good，不能发布半成品。

`ProfileRuntime` 是每个 Profile 独占的可变隔离域：协议编解码器、credential resolver namespace、Header allowlist、HTTP transport、连接池、DNS/TLS/代理策略、health、circuit、rate limit、capacity、reservation、session 和缓存。全局 Registry 只可把 URL 映射到一个 ProfileRuntime handle；进入 scope 后，Router、Scheduler、Executor 和 worker 不得以它寻找候选。可共享的只有无状态代码，任何可变状态都必须由单一 `profile_id` 拥有。

## 计划、Trace 与反馈证据

### ExecutionPlan 与 Trace

`ExecutionPlan` 固定唯一 `profile_id + runtime_generation + envelope_sha`、有序 `PlannedAttempt[]`、`PlannedAuxiliaryAttempt[]`、逻辑模型转换、adapter 版本、预算、Policy/catalog/deployment revision 和选择时指标快照。PlannedAttempt 仅保存 Profile-local Target ID、config revision、用途与预算归属，不能携带可直接 dispatch 的 endpoint、credential、已编码请求或 transport handle。

Executor 接收 `(ProfileScope, InferenceRequest, ProfileRuntime, ExecutionPlan)` 后，先验证四者的 scope 三元组以及 Trusted Identity、响应适配器上下文；不匹配、缺失、篡改或计划中存在第二个 `profile_id` 时，必须在解密、reserve 或 dispatch 前失败。`RouteTrace`、Decision、Attempt 和 Outcome 同样归属一个 Profile，默认不保存正文；跨 Profile 聚合只允许清洗后的不可变指标，且不能作为路由输入。

### Experiments、Feedback、Dataset 与 Evaluation

`StableExperiment` 与 `ExperimentAssignment` 是 Profile-scoped 的稳定分桶；候选只能是同 Profile 的 PolicyVersion 或 RouteDeployment。`TypedFeedback` 必须同时匹配同 Profile 的 decision/session 与 generation，避免同 local ID 被误关联。

`DatasetBinding`、`EvaluationJob`、`QueueEntry`、`Run`、`Report` 与 `EvaluationBudget` 也全部绑定同一 `profile_id + generation + envelope_sha`。candidate、baseline、judge Target、Policy、Catalog、credential、adapter、calibration 和预算必须来自同一 ProfileSnapshot；生产与评测即使同属一个 Profile，仍使用独立 credential、capacity、health、transport、队列、payload buffer 与结果存储子命名空间。评测不能复用 passthrough credential，也不能影响生产健康。

不同 Profiles 禁止跨 Profile 路由、fallback、评测或部署。唯一可比较的跨边界值是 `ResearchComparison`：它只含清洗后的不可变 EvaluationReport 投影，不能携带 payload、Target、credential、Plan 或运行时句柄，也不能成为 Policy、质量门禁、实验、fallback 或 RouteDeployment 的输入。

## 归属图与失败规则

```text
ProfileScope(profile_id, generation, envelope_sha)
  -> ProfileSnapshot
     -> Route -> ServiceObjective, ModelCard[] -> Target[]
     -> PolicyDraft -> PolicyVersion -> RouteDeployment
     -> ExecutionPlan -> PlannedAttempt[] -> RouteTrace
     -> StableExperiment/ExperimentAssignment
     -> TypedFeedback, DatasetBinding, EvaluationJob -> Run -> Report
```

图中的每条边都是同 Profile 所有权约束，而不是查询时再过滤的建议。持久化使用 `(profile_id, local_id)` 复合唯一键和同 Profile 外键，所有作用域查询先带 `profile_id`；先按全局 local ID 查找再过滤会产生 IDOR 与串扰风险，必须禁止。

> [!CURRENT]
> 本页是 `DOC_CONTRACT` 的目标模型，未宣称当前运行时已有上述对象或隔离实现。

> [!FUTURE]
> 独立评测进程、多副本全局配额和 Payload Replay 可在后续引入，但密文、派生密钥、队列与运行时资源仍须以同一 scope 三元组分区，并通过隔离测试后才能成为 `RUNTIME_PROVEN`。
