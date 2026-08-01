# API、迁移与交付验收

> [!TARGET]
> 本次变更只交付文档与证据契约；Go 运行时仍是后续目标，直到 `RUNTIME_PROVEN` 通过。控制面和运行时 API 都先从授权路径或服务端上下文固定一个 Profile，后续不得查找、选择或 fallback 到另一 Profile。

## 目标 API 与存储边界

控制面包含 Draft、Simulator、Profile/Catalog/Target、Deployment、Trace、Feedback/Evaluation 与 Replay API。它们先确定 `profile_id`，再以 `(profile_id, local_id)` 复合键查询；禁止先用全局 local ID 查找再过滤，也拒绝请求正文改写路径 scope。Profile 更新使用 ETag/revision CAS，并递增 generation；响应永不返回密钥。

运行时边界由 Profile Resolver、ProfileScope、ProfileRuntime、编译快照、Router、Scheduler、Executor 与响应 adapter 组成。Resolver 外的 Router、Scheduler、Executor 和 evaluation worker 不得导入或持有全局 Registry/Profile store；它们只能接收 ProfileScope、ProfileRuntime 或 ProfileSnapshot。存储外键、queue、预算、credential、transport、health、cache、session、Trace 与结果数据都必须归属一个 `profile_id`。

## 一次性 v1 → v2 迁移

这是破坏性迁移，不提供旧 hash URL、旧 schema、未知模型 passthrough、v1/v2 双写、双运行时或隐式兼容层。每个 v1 Profile 独立迁移为 disabled v2 Profile：保留 ID、slug、显示名和唯一 protocol；从已验证 upstream 生成待审核 envelope；将完整 base path 下沉到 Target endpoint；按现有协议行为明确 Header allowlist；credential 保持 passthrough，且不猜测供应商或生成服务 credential。

每个已声明模型生成同 Profile 的 ModelCard 与 passthrough Target。视觉辅助只有在 transport 属于该 Profile 协议族时才能成为显式 auxiliary operation；不匹配时阻断迁移，要求禁用或在本 Profile 重配，绝不创建或调用另一 Profile。legacy PolicyVersion 和 Route 初始不激活，管理员必须审核 envelope、Target、Header 与信任策略后显式部署。

迁移以单个 Profile 为事务边界：任何 blocker 都零写入。迁移记录以旧 `profile_id + config_sha` 唯一，重试返回同一结果；相同 model/upstream 的不同 v1 Profiles 不得去重、合并或共享对象。未显式枚举模型的配置必须阻断并要求人工补全，不能创建 wildcard Target。迁移工具输出 blocker、跨 Profile 引用检测与修复建议。

## 五个可独立交付物

1. 文档与证据契约：本次范围，固定事实、证据等级、内容规则与文档路由。
2. 路由配置、编译器与协议 IR：对象模型、规则编译、能力检查和请求归一化。
3. Target 执行面：容量准入、调度、retry、fallback、流式提交、health 与 circuit。
4. 控制面与可观测性：版本发布、CAS、canary、回滚、Trace、Replay 与审计。
5. 评测与受控学习：数据集、typed feedback、shadow、稳定实验与离线候选策略。

后续子项目可以细化实现，但不得静默突破单 Profile 隔离、有限预算、ClientCommit 或同 Profile 评测不变量。

## 双重验收门

> [!CURRENT]
> `DOC_CONTRACT` 是本次交付门：它验证 12 个页面、固定基线、证据等级、术语、单 Profile 示例、内部链接、搜索、静态资源与内容 lint。通过该门只说明文档准确表达目标契约，不说明 Go 运行时已实现它。

> [!TARGET]
> `RUNTIME_PROVEN` 是后续运行时门。只有隔离、故障注入、并发、安全、预算和迁移套件全部通过，才能将运行时能力从本版目标升级为已证明。不同 Profiles 禁止跨 Profile 路由、fallback、评测或部署；任何例外都会使该门失败。

## 验证矩阵

- Resolver、静态依赖与编译测试：覆盖 default/slug、未授权 Profile、恶意 Header/body scope、percent-encoding、重复斜线和规范化歧义。每个请求只解析一次，任何失败均不得 fallback 到 default 或其他 Profile；Resolver 后没有 Registry 回查，且对象图、envelope、协议、Plan 与辅助调用均为单一 `profile_id`。
- Transport 与隔离测试：覆盖相对/绝对 redirect、DNS rebinding、proxy/Host override、URL path join、Header/credential sentinel、相同 endpoint/model/session/cache key，以及 A 的错误、health、capacity 对 B 的无影响。
- API 与 IDOR 测试：使用 A/B 相同 local ID 验证 CRUD、列表、发布、Replay、Evaluation、ResearchComparison 与正文 scope 覆盖均 fail closed。
- 存储与 scope 交换测试：用绕过 API 的原始数据库写入验证 `(profile_id, local_id)` 或等价 same-Profile 复合外键约束；跨 Profile 引用必须由数据库拒绝，不能只依赖复合唯一键或一般“同 Profile 外键”描述。交换 A/B 的 InferenceRequest、Trusted Identity、InferenceEvent/Error 或 response adapter scope 时，必须在 reserve、dispatch 或 response 前 fail closed。
- 执行与预算测试：覆盖 retry、429、5xx、上下文失败、pre-commit 失败、ClientCommit 后断流、辅助调用、reserve 竞争、总 deadline 与最坏成本上限，并验证未 dispatch 的 reserve 失败不会计入 outbound。
- 评测、实验与迁移测试：验证独立 evaluation credential/transport/capacity/health、稳定 HMAC 分桶、密文 AAD 替换失败、旧 generation 失效、迁移零写入 blocker、幂等重试与不合并行为。Profile A 的 evaluation queue cancel、budget exhaustion、worker panic 或 result-write failure 均不得改变 Profile B 的队列、预算、状态或报告。
- Shadow 分离测试：`decision_shadow` 只能运行同 Profile 的本地无出站 advisor 并记录计划；`evaluation_shadow` 才可在显式授权后远程调用同 Profile Target，并必须使用独立 credential、预算与运行时状态。两条路径不能相互升级、借用资源或改变生产选择。
- 并发与 race 测试：多个 Profiles 同时运行时，除只读入口索引外不存在共享可变 registry、数据竞争或跨 Profile eviction。

所有安装、构建和浏览器验证在固定 Docker 镜像中执行，避免污染宿主环境。外部链接可定时检查，但短暂网络波动不应阻塞普通构建。

## 交付条件

文档站应提供真实 `/docs/<slug>` 路由、metadata、canonical、标题锚点、前后导航与 SSR 正文。内容、配置、图、Trace、Replay、评测与 fallback 示例都必须仅包含一个 `profile_id`，且任何 Target 不越出其 upstream envelope。固定源码结论链接完整 commit；竞品结论固定版本并严格区分源码、官方文档和闭源参考。

> [!FUTURE]
> 在所有 `RUNTIME_PROVEN` 测试完成前，运行时不能以文档、演示或单一 happy-path 测试宣称已实现隔离。任何发布或迁移兼容要求的扩张都需要新的规格、风险审查与独立验证。
