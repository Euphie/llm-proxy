# 重试、超时与 Session

> [!CURRENT]
> Policy 中的重试数字只是预算上限；同模型重试还必须由 Profile 的 `overload_rules` 触发，并发生在 `ClientCommit` 之前。

![重试、超时与提交边界状态机](/diagrams/retry-timeout-state-machine.svg)

## 重试何时发生

同模型重试必须同时满足：

1. 错误被判为可恢复；
2. HTTP 错误命中 `overload_rules`，或可恢复传输错误能使用第一条规则的等待配置；
3. 该规则的 `max_retries` 尚未耗尽；
4. Policy 的单目标重试、主回答、总调用和最坏费用仍有票据；
5. 共享 deadline 尚未到达；
6. 响应尚未形成 `ClientCommit`。

任一条件不满足，就不会发生同模型重试。`max_retries_per_target=1` 不能在没有规则时凭空创建重试。

## overload_rules 如何匹配

允许配置为可恢复的 HTTP 状态：`408`、`425`、`429` 和 `500..599`。规则按配置顺序匹配状态码和可选响应正文，第一条命中的规则决定等待与次数。

`401`、`403` 和未配置为可恢复的其他 4xx 是硬失败。它们通常代表鉴权、权限、请求字段或协议问题，重试只会增加费用和延迟。

传输错误没有 HTTP 正文；系统只在故障类别可恢复且至少存在一条规则时，借用第一条规则的等待配置。统计显示为 `0 / network`。

## 规则次数与 Policy 预算是两道门

| 限制 | 回答的问题 |
| --- | --- |
| 规则 `max_retries` | 这个错误模式允许再试几次？ |
| `max_retries_per_target` | 同一目标最多消费几张重试票据？ |
| `max_answer_attempts` | 本次请求全部回答尝试是否已满？ |
| `max_total_outbound_calls` | 分析、视觉和回答的总物理调用是否已满？ |
| `max_worst_case_cost_micro_usd` | 下一次尝试会不会突破最坏费用？ |
| `deadline` | 本次请求是否还有可用时间？ |

只有所有限制都允许，执行器才会预留下一次调用。

## 一个共享 deadline

`AttemptBudget` 使用 `context.WithTimeout` 为整次请求创建一个 deadline。任务分析、视觉、首次回答、同模型重试、等待和模型切换都消费同一份时间。

因此一个上游回答如果已经阻塞到总时限，代理会返回 504；不会在过期上下文上再等待退避、重试或切换模型。统计中的 504 只证明共享时限耗尽，不证明重试曾经发生。

## ClientCommit 边界

非流式响应在完整响应准备发送时提交。流式响应在第一个完整合法响应事件发送给客户端时形成 `ClientCommit`。

提交前的可恢复错误可以按冻结计划重试或切换；提交后的断流只能记录失败，因为发送第二份完整回答会破坏协议并产生重复内容。

## 模型切换

同模型重试不成立或已耗尽时，只有以下条件同时成立才切换：

- 冻结 `ExecutionPlan` 中存在下一个合格候选；
- 当前请求仍有模型切换票据；
- 下一候选的原生或复合调用图仍能装入预算；
- 共享 deadline 未到；
- 尚未 `ClientCommit`。

所有候选仍连接当前 Profile 的同一 Upstream。模型切换不是网关节点故障转移。

## Session 锁定解决什么

长会话经常有大量固定前缀和缓存读。如果每轮只按即时单价切换模型，提示缓存可能失效，反而增加费用和延迟。Session 锁定用稳定性条件保护已经形成收益的模型绑定。

![Session 锁定生命周期](/diagrams/session-lock-lifecycle.svg)

## 锁定条件

普通稳定会话必须同时满足：

- 连续 3 个可靠任务保持稳定；
- 最低分类置信度至少 85%，即 8500 BPS；
- 会话 Token 或本轮 Cache Read 达到 `session_lock_token_threshold`；
- 默认阈值为 **100K Token**，管理员可以配置正整数。

当当前模型是强模型基线代表的最高能力模型时，可以使用 `highest_model` 原因直接锁定。

## 锁定不等于永不变化

每个新用户任务仍重新检查风险和硬能力。以下情况可以突破普通复用：

- 高风险或风险未知需要更安全模型；
- 图片、工具、结构化输出或上下文能力不满足；
- 模型已不在当前目录修订或不再有资格；
- 用户或运行时触发主动升级；
- Session 绑定与当前策略不兼容。

锁定不允许跨 Profile，也不让旧目录事实永久生效。

## 调整 100K 阈值

降低阈值会更早锁定，适合缓存读昂贵、模型切换导致缓存损失明显的工作负载；代价是更早放弃低价候选。提高阈值会保留更长的动态选择期，但可能损失缓存。

调整前至少比较：会话 Token、Cache Read、模型输入与缓存价格、锁定前后的实际费用、主动升级率和能力失败。不要只看请求次数。

## 为什么下一次成功不是本次重试

统计列表每一项代表一个客户端请求。同一条路由轨迹中出现多个回答物理调用，才说明代理执行了重试或模型切换。

相邻请求即使会话 ID 相同，也可能是客户端收到 504 或网络错误后重新发送。它们会有不同请求序号和 `request_trace_id`。

## 源码与验证入口

- 统一预算：`internal/routing/budget.go`、`internal/routing/budget_lease.go`
- 重试与回答执行：`internal/proxy/auto_executor.go`、`internal/provider/provider.go`
- Session 锁定：`internal/routing/session_lock.go`、`internal/routing/session_store.go`
- 默认阈值：`internal/profile/auto_routing.go`
- 行为测试：`internal/routing/session_lock_test.go`、`internal/proxy/auto_routing_test.go`
