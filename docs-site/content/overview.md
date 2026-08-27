# 系统概览

> [!CURRENT]
> 本站只描述当前 V2 运行时和管理后台。这里的“已实现”来自本项目源码与测试，不代表路线图或外部模型排名。

Mesotes 在调用方与一个上游网关之间提供协议代理、Profile 隔离、`model=auto` 智能路由、视觉预处理、有界重试和可审计统计。它不是模型托管平台，也不接管上游网关的节点治理。

![当前 V2 系统架构](/diagrams/system-architecture.svg)

## 两种请求模式

| 请求 | 是否进入智能路由 | 处理方式 |
| --- | --- | --- |
| `model=<具体模型>` | 否 | 在 URL 已选定的 Profile 内按原模型转发，不读取 Routing Policy。 |
| `model=auto` | 是 | 执行任务分析、Session 判断、候选筛选、视觉规划和有界执行。 |

两种模式共用当前 Profile 的协议、唯一 Upstream、鉴权透传、容错规则和统计。智能路由不会改变 URL 已选定的 Profile，也不会把显式模型请求悄悄改成另一个模型。

## 三个协作平面

### 控制面

管理员维护 Profile、模型目录、Routing Policy、视觉、容错和动态优化配置。连接、模型事实或 Policy 保存通过校验后生成新修订，后续请求立即使用，不需要重启服务。

### 请求面

请求进入时取得不可变运行时快照。`model=auto` 会冻结模型目录修订、Policy 版本、候选顺序、视觉方式、共享 deadline、调用数和最坏费用；执行器只能消费计划内票据。

### 证据面

系统记录路由轨迹、候选排除原因、物理调用、Token、缓存和已知费用。本地质量、稳定性、严重错误和延迟证据可以供校准器使用，但不会记录原始提示词、图片、鉴权信息或完整回答。

## 一次 model=auto 请求会发生什么

1. URL 选定 Profile，并读取当前运行时修订。
2. 请求解析器提取 Token、图片、工具、结构化输出和流式要求。
3. 创建统一 `AttemptBudget`，其中包含共享 deadline。
4. 任务分析模型输出任务类型、难度、风险、置信度和原因码。
5. Session 规则判断能否复用已有分类或锁定模型。
6. 任务映射选择 Route，候选先通过硬能力和 Production 门槛。
7. 合格候选再比较质量、稳定性、成本效率和性能。
8. Planner 冻结 `ExecutionPlan`，然后执行视觉辅助、回答、重试或模型切换。
9. 最终状态和每次物理调用写入同一条路由轨迹。

## 系统保证的边界

- **Profile 不可跨越**：路由、Session、评测、视觉、回退和统计都留在当前 Profile。
- **硬能力不能用价格补偿**：上下文、视觉、工具和结构化输出不满足时，低价也不能入选。
- **Shadow 不接在线回答**：没有 Production 资格的候选只积累证据。
- **一次请求只有一个总时限**：分析、视觉、回答、重试和切换共同消耗。
- **提交后不能重写响应**：形成 `ClientCommit` 后，不再发送另一份完整回答。
- **后台更新不污染在途请求**：新修订只影响后续请求。

## 系统不负责什么

- 每个 Profile 只配置一个 Upstream；节点负载均衡、健康检查、熔断、区域路由和节点故障转移由该网关负责。
- 代理不托管模型，不保存调用方输入的临时上游密钥。
- 公开评测只提供冷启动先验，不能单独授予普通候选 Production 资格。
- 系统不会因为视觉模型识图成功，就把它当成最终回答模型。
- 系统不会因为下一次同会话请求成功，就把它解释为上一次请求内的自动重试。

## 配置变化如何生效

| 变化 | 新请求 | 在途请求 |
| --- | --- | --- |
| 连接或 Profile 配置保存 | 读取新运行时修订 | 保持进入时快照 |
| 模型事实或状态保存 | 读取新模型目录修订 | 继续使用已冻结目录 |
| Routing Policy 保存或回滚 | 读取新 active 版本 | 继续执行已冻结计划 |
| Session 锁定阈值修改 | 按新阈值判断 | 不改写当前执行计划 |

保存发生修订冲突时，后台会拒绝覆盖。刷新页面、重新检查差异并再次保存；不要用重启服务绕过一致性校验。

## 源码与验证入口

- Profile 和 Policy 结构：`internal/profile/auto_routing.go`、`internal/profile/routing_policy.go`
- 在线路由入口：`internal/routing/engine.go`、`internal/routing/planner.go`
- 统一预算：`internal/routing/budget.go`、`internal/routing/call_graph.go`
- 运行时协调：`internal/gateway/runtime_coordinator.go`
- 路由轨迹与物理调用：`internal/stats/routing_traces.go`、`internal/stats/routing_calls.go`

## 推荐阅读路径

- 第一次部署：后台操作流程 → Profile 与模型目录 → Routing Policy → 运行边界与上线检查。
- 图片请求：视觉预处理 → 重试、超时与 Session → 统计与故障排查。
- 线上选型问题：在线请求链路 → 评测与自动校准 → 统计与故障排查。
