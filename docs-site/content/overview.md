# 产品目标与非目标

> [!TARGET]
> Mesotes 采用两层确定性路由：URL slug/default 先且只选择一个 Profile；随后 Model Router 在该 Profile 的 ModelCard 中选择逻辑模型，Target Scheduler 再在该 Profile、同一逻辑模型的 Target 中调度。任何组件发现 scope、引用或版本不一致时必须 fail closed，不能回查其他 Profile。

## 目标

- 支持显式逻辑模型、`model=auto` 与命名 Route，并在能力、隐私、地域、价格和容量硬约束后作出选择。
- 让一个 Profile 在其信任边界内拥有多个逻辑模型、部署、区域、账号或 endpoint Target；凭据、协议状态、连接池、容量、健康、缓存和 session 均由该 Profile 独占。
- 用不可变 `ExecutionPlan`、统一 `AttemptBudget`、Decision/Attempt/Outcome 事件和授权 Replay，使选择、排除与执行可审计。
- 以编译、版本化、CAS、canary、last-known-good 与原子快照发布策略；热路径只读取预编译内存快照和本地状态。
- 以真实数据集、typed feedback、校准评测和质量门禁积累质量证据，而不是预设精确质量或样本阈值。

## 不可跨越的边界

### Profile-local 两阶段选择

`Profile` 是用户可见的 Provider 实例、URL 入口、配置命名空间和唯一隔离边界；本设计不引入第二个 Provider 实体或分类字段。Resolver 生成服务端拥有的 `ProfileScope` 后，Router、Scheduler、Executor 与评测组件只能处理 scope 所属的对象图。

不同 Profiles 禁止 route、routing、fallback、evaluation、deployment、canary 和学习编排。即使协议、模型名、endpoint 或秘密后端相同，跨边界引用也由编译器拒绝；运行时发现第二个 `profile_id`、generation 或 envelope SHA 不匹配时，在 reserve 或 dispatch 前失败。设计允许的编排仅是同一 Profile 内的 Target 调度与 fallback。

显式模型固定逻辑模型，但仍需通过该 Profile 的安全、能力、地域、价格与容量过滤；`auto` 和命名 Route 可在已声明的 Profile-local transition graph 中选择模型。无合格 Target、未知模型或协议不匹配均按入口协议失败，绝不改用另一 Profile。

## 验收定义

“领先”不是综合分，而是可独立验证的属性：

1. 硬约束零违规；违规候选在 dispatch 前被排除。
2. Profile A 的凭据、Header、编码请求、连接、健康、容量、缓存和 session 不能进入 Profile B；违反即为隔离失败。
3. 每个请求的尝试次数、截止时间和最坏成本有共同上界；任一上限耗尽即停止。
4. 每条 Route 用自身基线、非劣效界限、置信区间与严重错误门禁证明质量；通过门禁后才优化成本或延迟。
5. Decision、计划、尝试与结果可解释；只有授权载荷、完整输入事实、seed、算法版本和快照齐备时才可重算决策。

## 首版非目标

- 任意递归策略图、第三方运行时代码插件、hedged request、无上限并行探测，以及 ClientCommit 后 fallback。
- 热路径同步数据库、远程分类器或学习服务；`decision_shadow` 只能是同 Profile 的本地无出站 advisor。
- 在线自动改写规则、候选集合、优先级或发布指针；可靠性硬门禁仅可 CAS 回滚到精确的 last-known-good。
- Bandit、Track-and-Stop、RL、Best-of-N、Mixture-of-N、语义缓存、RAG、Memory、工具选择与图像生成编排。
- 网关保存上游原始密钥，或让异步评测使用 passthrough credential；评测必须使用同 Profile、独立的 `evaluation_credential_ref`、预算和运行时子命名空间。
- 对旧配置或未知模型透传长期兼容；这是有意的破坏性迁移边界。

## 交付范围与证据门

本次拆分为五个可独立交付物：文档与证据契约、路由配置与协议 IR、Target 执行面、控制面与可观测性、评测与受控学习。当前交付只实施第一项；后续实现可以细化，但不得突破本页的单 Profile 边界。

> [!CURRENT]
> `DOC_CONTRACT` 证明文档、示例、固定证据和内容测试准确表达隔离与对象归属。它不证明 Go 运行时已经拥有多 Target、计划执行、容量、评测或学习能力。

> [!TARGET]
> `RUNTIME_PROVEN` 只能在后续运行时子项目的隔离、故障注入、并发和安全套件全部通过后声明。届时必须证明 Header、credential、state、cache、session、IR 和 ExecutionPlan 在任意失败分支均不越出 ProfileScope。

> [!FUTURE]
> 受控学习首版只产生离线或 `decision_shadow` 证据；任何影响线上选择或发布的自动化能力须由独立规格、评测和发布门验证。
