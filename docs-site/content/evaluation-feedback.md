# 评测、反馈与稳定实验

> [!TARGET]
> 评测、反馈、实验和学习证据只属于一个 `profile_id + generation + envelope_sha`。URL/default 已在请求入口固定 Profile；远程工作只能在同 Profile 的 `evaluation_shadow` 中发生，绝不跨 Profile，也不能改变当前在线执行。

## 反馈与数据绑定

`TypedFeedback` 绑定同 Profile 的 decision/session 与 generation，不能仅按全局 decision 或 session ID 关联。`DatasetBinding` 将数据集 SHA、数据策略、租户授权与目标 ProfileSnapshot 固定在一起；同内容数据集在不同 Profile 使用时需建立独立绑定，不能共享可变对象。

反馈、数据、candidate、baseline、judge Target、Policy、Catalog、adapter、calibration 与 EvaluationBudget 必须来自同一 ProfileSnapshot。引用中出现另一 `profile_id`、失效 generation 或 envelope SHA 时，API、编译器和 worker 必须拒绝，而非回查全局对象。

## EvaluationJob、Report 与预算

`EvaluationJob`、`QueueEntry`、`Run`、`Report` 与 `EvaluationBudget` 都绑定精确 scope 三元组。EvaluationBudget 以 Profile、tenant 和 Route 独立限制采样、调用、金额、并发与 deadline；它在默认状态下关闭，只有明确的策略和授权才能启用。业务 `AttemptBudget` 与 EvaluationBudget 不得互借或扩大额度。

首版使用同进程、按 `profile_id` 分区的 worker pool，正文不持久化。携带正文的评测任务需要明确 data policy、Profile/region 与租户授权，单任务正文最大 256 KiB、内存 TTL 为 10 分钟；超出大小或超过 TTL 必须明确失败并清除内存载荷。评测 credential 必须是同一 Profile envelope 下独立的 `evaluation_credential_ref`；worker 不得访问 passthrough credential，也不保存原始 credential、Header 或编码请求。

## Shadow 边界

`decision_shadow` 仅在当前 ProfileSnapshot 内计算并记录候选计划。它只能运行本地、确定性、无出站的 advisor，不发送 analyzer、judge 或模型请求，也不改写线上规则、候选集合、优先级或部署指针。

`evaluation_shadow` 可在显式 `online_opt_in` 后异步运行 candidate、baseline 或 judge Target，但全部必须来自同一 Profile，并使用独立 credential、预算、transport、capacity、health、worker queue、payload buffer、取消状态和结果存储。它不影响生产 health、容量或当前响应；失败时仅写入该 scope 的评测结果。生产与 evaluation 的 runtime state 分离，不能以评测失败、取消、预算耗尽、worker panic 或结果写入失败改变另一 Profile 或生产请求的队列、预算、状态或报告。

## 稳定实验与报告

`StableExperiment` 与 `ExperimentAssignment` 使用 Profile、Route、Deployment、epoch 和可信 tenant/principal 的域分离 HMAC。输入包含 `profile_id`、generation、envelope SHA、用途和 key version，因此相同 local ID 或 epoch 在不同 Profile 也不会共享分桶。没有可信主体时，不从可伪造 Header 推断身份。

评测统计按 principal/session cluster，而非把相关请求当作独立样本；报告使用 cluster-aware bootstrap 与置信区间，并单独执行 severe-error gate。评测报告绑定同一 Profile 的 Dataset、Policy、catalog、adapter、judge 与 calibration SHA，呈现门禁、效应量和置信区间。报告不足、范围不匹配、相关性处理缺失或 severe-error gate 失败时不能提升或发布 RouteDeployment；稳定实验只是受控证据，不是自动化路由器。

## 受限跨边界比较

`ResearchComparison` 是唯一允许跨 Profile 的值，但只能包含清洗后的不可变 EvaluationReport 投影。它不能携带 payload、Target、credential、ExecutionPlan、运行时句柄、HMAC 密文或可变状态；也不能成为路由、质量门禁、实验、fallback、PolicyVersion 或 RouteDeployment 的输入。

不同 Profiles 禁止跨 Profile 路由、fallback、评测或部署。若需要比较，先在各自 scope 生成独立 Report，再导出上述受限投影；任何试图将比较结果回接到执行或发布的行为必须拒绝。

> [!CURRENT]
> 当前源码基线没有 EvaluationJob、独立 evaluation credential、DatasetBinding、typed feedback 或稳定实验；本页只定义目标控制面契约。

> [!FUTURE]
> 若评测未来迁移到独立进程，正文必须使用 Profile-scoped 派生密钥加密短 TTL durable spool；AEAD AAD 至少绑定 tenant、`profile_id`、generation、envelope SHA、task、purpose 与 key version。跨 scope 替换密文必须认证失败，spool 也不得形成跨 Profile 的持久化正文池。
