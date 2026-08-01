# Profile 隔离与协议边界

> [!TARGET]
> URL slug/default 在任何路由前解析且只解析一次 Profile。解析器随后创建服务端拥有的 `ProfileScope(profile_id, generation, envelope_sha)`；后续阶段不得重新选择、查找或 fallback 到另一个 Profile。任何 scope、版本或归属不匹配都在敏感操作前 fail closed。

## 入口协议与身份

`Profile` 是用户可见的 Provider 实例、URL 入口、配置命名空间和唯一隔离边界。本设计没有第二个 Provider 实体或分类字段。`anthropic` Profile 只接受 `/v1/messages` 的 Messages operation；`openai` Profile 只接受 `/v1/chat/completions` 的 Chat Completions、`/v1/responses` 的 Responses，以及明确支持的无 `v1` alias。主回答保留入口 operation，不能在 Chat Completions 与 Responses 间转换。

每个 Profile 只加载该协议族的强类型请求/响应 adapter；其 `InferenceRequest`、`InferenceEvent` 与 `InferenceError` 均携带同一 ProfileScope。未知字段或上游扩展仅可在同一 Profile、同协议、同信任域内显式 passthrough；不能转换的必需字段必须在候选过滤阶段失败，不能静默丢弃。上游错误必须转换为入口协议的合法错误结构。

协议或 operation 不匹配时，Ingress 在 Router 前按入口协议返回错误，且不得查询其他 Profile。`ProfileScope` 绑定 InferenceRequest、Trusted Identity、InferenceEvent/Error 与响应适配器上下文；请求正文、Header、path 参数和下游组件都不能重建或覆盖它。

![Profile 隔离边界](/diagrams/profile-isolation.svg)

图示等价说明：URL/default 只产生一个 ProfileScope；该 scope 进入 Profile-owned ingress adapter、不可变快照、ProfileRuntime 与响应 adapter。Route、Target、执行计划、评测和运行时状态全部留在该单一分支，边界外的 Profile 既不是候选也不是故障回退目标。

## 不可变身份与 envelope

每个 Profile 固定 protocol、credential authority/namespace 与 trust-domain identity。删除的 `profile_id` 与 slug 只记录 tombstone，永不重新分配；扩张或替换 origin、Header、trust policy 或地域 envelope 必须复制为新 Profile，原 Profile 只能收紧不改变身份的规则。每个允许的更新递增 generation 并生成新的 envelope SHA，因此旧 Plan、Replay、EvaluationJob 与实验分配必须在解密、reserve 或 dispatch 前失效。

`upstream_envelope` 是 Target 不可放宽的上界：允许 origin、credential authority/namespace、Header allowlist、TLS/DNS/proxy trust policy 与数据地域。Target 可在该 envelope 内选择 endpoint 和本地 credential alias，并进一步收紧能力或地域；不能覆盖协议、credential authority 或 trust policy。编译器拒绝任何越界 Target，而不是通过 hostname、模型名或协议猜测归属。

同一 Profile 可以包含多个账号、区域或 endpoint Target，但这些 Target 必须同时属于同一个上游服务商和同一 trust-domain identity，并受同一 `upstream_envelope` 约束。不同上游服务商必须使用不同 Profile；由独立主体管理的 trust-domain identity 也必须使用不同 Profile。调用方必须通过对应的 Profile URL 显式选择，Resolver、Route 与 fallback 均不能替调用方跨边界代选。

## ProfileRuntime 与 Registry

每个 `ProfileRuntime` 独占协议编解码器、credential resolver namespace、Header allowlist、HTTP transport、连接池、DNS/TLS/代理策略、health、circuit、rate limit、capacity、reservation、session、idempotency 与缓存。共享库代码可以无状态复用，但 ProfileRuntime 不得持有彼此的可变对象、连接、缓存条目或 credential handle。

全局 Registry 的唯一职责是入口处把 slug/id 映射到一个 ProfileRuntime handle；它不聚合候选，也不参与 Router、Scheduler、Executor 或 evaluation worker 的决策。URL 绑定后调用 Registry 找候选，会让 retry、429、上下文失败或无可用 Target 变成跨边界风险，必须禁止。

## 出站与状态隔离

### Transport 与网络边界

自定义 endpoint 默认要求 HTTPS，拒绝 redirect、URL userinfo、查询凭据与私网、环回、link-local 地址。连接前后都验证 allowed origin，防止相对/绝对 redirect、DNS rebinding、proxy 或 Host override 把请求送出 envelope。任何例外必须是显式管理员策略并进入审计；不能把 Profile A 的 credential 发送到 envelope 外或 Profile B。

### Header、credential 与状态

Ingress 先按当前 Profile allowlist 清洗 Header，再重建上游请求。Authorization、组织头、实验头、幂等键、Cookie 与追踪扩展都以 Profile 为命名空间，不能出现在另一 Profile 的请求、日志或 Trace。服务 credential 引用和其 generation 绑定 `profile_id`，不同 Profile 不能以别名指向同一可变 handle。

`passthrough` 只允许相同鉴权体系和明确配置的信任域。使用 Target 的服务 `credential_ref` 时，adapter 必须剥离调用方 Authorization、上游身份头和任何未在 allowlist 中的转发 Header；违规 Target 或 Header 重建在编译或 dispatch 前拒绝。一个 ExecutionPlan 默认禁止混合 passthrough 与服务凭据；确需两种身份时，必须拆成不同 Route 并由调用方显式选择，不能依赖故障 fallback 改变计费或信任主体。

health、circuit、429 cooldown、capacity、reservation、连接、DNS/TLS session、上游 session、continuation、prompt cache 与错误缓冲也由 ProfileRuntime 独占。Profile A 的 401、429、5xx、熔断、容量耗尽或 worker 失败不能改变 Profile B 的状态或选择输入。

## 失败后果与证据边界

> [!CURRENT]
> 固定源码已证明入口可由 URL slug/default 选择 Profile；它不证明上述 ProfileScope、envelope、ProfileRuntime 或 transport 隔离已经存在。

> [!TARGET]
> 编译器、API 与 Executor 必须拒绝跨 Profile 的 Route、Target、credential alias、failure domain、continuation、评测引用或 Plan。不同 Profiles 禁止跨 Profile 路由、fallback、评测或部署；当前 Profile 无合格 Target 时按入口协议失败。

> [!FUTURE]
> 未来的独立评测进程或多副本配额只能扩展 Profile-scoped 实现：密钥、队列、状态、预算和 transport 必须保留同一 scope 三元组，不能形成全局可变运行时调度器。
