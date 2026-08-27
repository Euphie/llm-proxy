# Routing Policy

> [!CURRENT]
> V2 管理一份当前线上 Policy。保存通过校验后立即生效，历史版本保持不可变。

## 当前生效模型

后台顶部显示 active Policy、运行时修订和模型目录修订。智能生成只返回建议预览；点击“载入并编辑”后仍需完整检查，最后由“保存并立即生效”发布。

保存成功后，新请求读取新 active 版本；在途请求继续执行进入时冻结的 `ExecutionPlan`。失败的保存不会产生半生效状态。

## 模型角色

| 角色 | 当前作用 | 主要要求 |
| --- | --- | --- |
| 参与模型 | Route 可以引用的模型集合 | 当前 Profile 内、目录事实可解析 |
| 任务分析模型 | 判断任务类型、难度、风险和置信度 | 能执行分析请求；需要时确认工具能力 |
| 强模型基线 | 分析回退、高风险和普通候选全部不合格时的安全下限 | 必须满足本次请求硬能力 |
| 评审模型 | 动态优化的异步评审 | 仅在启用本地评审时需要 |

新增目录模型不会自动加入参与模型，也不会自动获得 Production 资格。

## Production 与 Shadow

候选的 `production_eligible` 决定它是否有资格接收在线回答流量：

- **Production**：仍需通过本次请求的能力、状态和 Route 门槛；
- **Shadow**：可以积累评测与线上证据，但不进入在线回答计划。

公开评测或管理员填写的先验不能单独把普通候选变成 Production。强模型基线作为安全下限单独处理，但仍不能绕过上下文、视觉、工具或结构化输出硬能力。

## 任务映射如何命中 Route

任务映射可按任务类型和难度组合匹配，未命中时使用默认 Route。运行时按以下粒度尝试：

1. 任务类型 + 已知难度；
2. 任务类型；
3. 已知难度；
4. 默认 Route。

难度为 `unknown` 时不会伪装成精确难度匹配。低置信度分类会按策略降为默认任务、中等难度或未知风险。

## Route 的硬门槛

每个 Route 配置：

- `min_quality_bps`：质量保守下界；
- `min_stability_bps`：稳定性保守下界；
- `max_severe_error_rate_bps`：严重错误率保守上界；
- 候选模型、Production 资格、预期延迟和当前证据；
- 质量、稳定性、成本效率和性能权重。

候选只有先满足模型状态、硬能力、Production 资格和三项证据门槛，才进入路由分比较。权重不是绕过硬门槛的办法。

## 四项权重

| 权重 | 代表什么 | 不代表什么 |
| --- | --- | --- |
| 质量 | 当前 Route 与任务分段的保守质量 | 模型全局永久排名 |
| 稳定性 | 本地成功与严重错误证据 | 上游网关节点健康 |
| 成本 | 完整最坏调用图的成本效率 | 只看最终回答单价 |
| 性能 | 预期延迟与目标的关系 | 给每次调用新增独立超时 |

四项权重只在合格候选之间排序。缺价格、缺能力或证据越界时，应先修复事实或保持 Shadow。

## Session 设置

`session_lock_token_threshold` 默认 **100K Token**，管理员可以按缓存收益和模型价差调整。普通会话只有在连续 3 个可靠任务选择稳定、最低分类置信度至少 85%，且会话 Token 或 Cache Read 达到阈值后才锁定。

锁定保护长上下文缓存，不取消新用户任务的风险与硬能力检查。强模型基线可以按“最高模型”原因直接锁定。

## 统一尝试预算

| 字段 | 限制对象 |
| --- | --- |
| `max_answer_attempts` | 首次回答、同模型重试和切换后的回答总数 |
| `max_auxiliary_calls` | 任务分析与未命中缓存的视觉调用 |
| `max_total_outbound_calls` | 本次请求全部物理上游调用 |
| `max_retries_per_target` | 同一模型/目标的重试票据 |
| `max_model_switches` | 计划内模型切换次数 |
| `deadline` | 分析、视觉、回答、重试和切换共享总时限 |
| `max_worst_case_cost_micro_usd` | 冻结调用图的最坏预计费用 |

预算必须同时成立。提高其中一个上限不会绕过其他调用数、deadline、费用或 `overload_rules`。

## 动态优化

动态优化控制异步抽样、评审和本地证据。`auto_update_policy` 由管理员明确控制：

- 关闭：继续积累证据和建议，不自动修改 active Policy；
- 开启：普通改进需 10 分钟防抖、两个一致证据窗口和 6 小时冷却；
- 安全回归：Production 候选失去可靠证据或跌破门槛时，可以立即撤销资格。

自动应用仍执行模型目录兼容检查和期望修订校验，不会绕过手动保存的安全边界。

## 历史与回滚

每次手动保存或自动校准都会创建不可变历史。回滚不是修改旧记录，而是：

1. 读取目标历史内容；
2. 用当前模型目录重新校验角色、Route 和候选；
3. 复制为一个新的 active 版本；
4. 保留回滚前后两个历史版本。

如果历史引用的模型已经退休或删除，回滚应先修复目录兼容性，不能强行发布。

## 发布前检查

- 所有角色模型都在参与集合和当前 Profile 内；
- 默认 Route 存在，每个任务映射都引用有效 Route；
- Production 候选至少有一个能满足目标任务硬能力；
- 预算能容纳任务分析和必要视觉调用；
- `session_lock_token_threshold` 是正整数；
- 自动更新开关、抽样率、日预算和评审模型符合预期；
- active、运行时和模型目录修订在保存后同步变化。

## 源码与验证入口

- 配置与默认值：`internal/profile/auto_routing.go`、`internal/profile/routing_policy.go`
- 策略校验与存储：`internal/strategy/store.go`、`internal/admin/routing_policy_service.go`
- 策略编译：`internal/strategycompiler/`
- 自动校准：`internal/admin/routing_policy_reconciler.go`
- 后台编辑器：`internal/admin/ui/static/routing-policy-editor.js`
