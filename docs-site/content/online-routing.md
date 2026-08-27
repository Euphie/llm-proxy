# 在线请求链路

> [!CURRENT]
> `model=auto` 的分类、候选决定和调用预算都固定在请求进入时取得的 Profile 运行时快照内。

![model=auto 在线请求链路](/diagrams/online-routing-flow.svg)

## 请求处理顺序

URL 先选择 Profile，然后才读取请求正文中的模型字段：

- 具体模型：按当前 Profile 的协议和连接透传，不读取 Routing Policy；
- `model=auto`：创建路由引擎、统一预算和路由轨迹；
- 无效模型或无效 Profile：在进入分析前拒绝，不尝试其他 Profile。

这一步是最重要的排查边界。显式模型请求没有任务分析记录是正常行为，不是统计遗漏。

## 请求事实

路由前会提取影响硬能力和成本的事实：

- 输入 Token 与最大输出估算；
- 是否包含图片及图片来源类型；
- 是否声明工具调用；
- 是否要求结构化输出；
- 流式或非流式响应；
- Session 引用、缓存读取和已知上下文。

解析只产生结构化事实，不把原始提示词写入路由轨迹。

## 任务分析

任务分析模型消耗一张辅助调用票据，输出：

| 维度 | 典型值 | 低置信度处理 |
| --- | --- | --- |
| 任务类型 | `simple`、`general`、`reasoning`、`math`、`coding`、`tool_use`、`vision` | 降为默认任务 |
| 难度 | `easy`、`medium`、`hard`、`unknown` | hard 保留安全保护，其他降为 medium |
| 风险 | normal、high、unknown | 降为 unknown |
| 置信度 | 0..10000 BPS | 使用各维度最低值 |

复杂度信号包括局部修改、多约束、并发、跨系统、不确定性、安全性、量化 SLO、广泛验证和迁移回滚。分析器失败时只对明确可回退的故障类别走安全分类；协议或不可恢复错误不会被随意吞掉。

## Session 偏好如何参与

Session 可能提供已有任务类型、Route、模型和锁定状态。运行时仍会检查：

- Session 与当前策略名匹配；
- 目录和 Policy 修订仍允许该模型；
- 分类可靠且满足最低置信度；
- 新用户任务的风险与硬能力仍成立。

任务延续可以复用分类；模型锁定可以形成模型偏好，但不能把不支持图片、工具或上下文的模型强行放回计划。

## Route 选择

任务映射优先匹配任务类型 + 难度，再依次回退到任务类型、难度和默认 Route。所有匹配都在当前 Policy 冻结快照内完成。

如果任务分析置信度不足，系统先应用保守分类策略，再选择 Route；不会用一个表面精确但不可靠的分类换取低价模型。

## 候选硬门槛

候选按顺序检查：

1. 在参与模型集合中；
2. 目录状态为 `available`；
3. 具有 Production 资格，或是符合安全规则的强模型基线；
4. 上下文、输出、视觉、工具和结构化输出能力满足请求；
5. 质量和稳定性保守下界不低于 Route 门槛；
6. 严重错误率保守上界不超过 Route 上限；
7. 完整调用图可以装入剩余调用、时间和最坏费用预算。

Shadow 候选会出现在证据和排除原因中，但不会进入在线回答计划。

## 合格候选如何排序

只有通过全部硬门槛的候选才比较路由分。Route 的质量、稳定性、成本和性能权重共同决定顺序。

成本按完整计划估算，不能假设识图缓存一定命中，也不能只比较最终回答的输入单价。性能使用当前候选的预期延迟与 Route 目标，不为每次调用创建独立 deadline。

## 强模型基线与明确失败

普通候选全部不合格时，Planner 尝试满足本次硬能力的强模型基线。如果基线也不支持图片、工具、上下文或当前预算，系统明确失败。

安全回退不允许离开当前 Profile，也不会把 Shadow 候选临时提升成 Production。

## ExecutionPlan 冻结什么

执行开始前固定：

- Profile 运行时、模型目录修订和 Policy 版本；
- 分类、Route、候选顺序与排除原因；
- 每个候选的原生或复合视觉方式；
- 回答、辅助、总调用、重试和模型切换上限；
- 共享 deadline 与最坏费用；
- 可执行的调用图和预算预留。

后台在请求途中保存新配置，只影响下一次请求。执行器不能临时发现一个计划外便宜模型并调用它。

```text
Profile 快照
  → 任务分析 / Session
  → Route 与硬门槛
  → 合格候选评分
  → 冻结 ExecutionPlan
  → 视觉辅助（如需要）
  → 回答 / 有界重试 / 模型切换
  → 路由轨迹
```

## 执行阶段

1. 按计划完成必要的视觉缓存查找或识图；
2. 为当前回答模型预留调用与最坏费用；
3. 发起回答物理调用；
4. 成功时形成 `ClientCommit` 并完成轨迹；
5. 失败时检查可恢复性、`overload_rules`、规则次数、预算、deadline 和提交边界；
6. 只有计划内下一候选和模型切换票据都存在时才切换。

## 候选排除与请求失败怎么查

先看路由轨迹中的 `candidate` 决定，不要从最终模型倒推原因：

- `capability` 类：修正模型事实或请求要求；
- `production` 类：检查 Shadow/Production 和本地证据；
- `quality`、`stability`、`severe_error`：检查 Route 门槛和证据窗口；
- `budget`、`deadline`、`cost`：检查完整调用图，不只看第一次回答；
- `session`：检查锁定原因、目录修订和新任务风险复检。

## 如何阅读下面的示例

下面的 Route Trace Explorer 使用固定去敏数据，不是实时请求。它展示当前字段结构、Shadow 候选被排除，以及回答在 `ClientCommit` 前按 `overload_rules` 完成一次同模型重试。

## 源码与验证入口

- 模式分流与执行：`internal/proxy/proxy.go`、`internal/proxy/auto_executor.go`
- 任务分析：`internal/routing/analyzer.go`、`internal/routing/classification.go`
- 路由与计划：`internal/routing/engine.go`、`internal/routing/planner.go`
- 调用图：`internal/routing/call_graph.go`
- 路由测试：`internal/proxy/auto_routing_test.go`、`internal/routing/planner_test.go`
