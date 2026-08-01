# 质量目标与模型选择

> [!TARGET]
> URL/default 在路由前固定一个 Profile，质量评估与模型选择只读取该 Profile 的 catalog、Route、证据与快照。后续阶段不得选择、查找或 fallback 到另一个 Profile；跨边界质量证据、模型或部署引用必须被拒绝。

## Route-specific ServiceObjective

`ServiceObjective` 属于一个 Route，并将正确性、质量、可靠性、价格和延迟分成不可混淆的条件。它不是全局综合分：能力、隐私、地域、认证、协议和价格上限是硬门；通过门后，Route 才可在自身质量、可靠性和安全要求内优化成本或延迟。

每个 Route 使用自己的任务分布、基线、非劣效界限、置信区间与严重错误门禁。证据不足、校准失效或严重错误未满足门禁时，候选不能发布或提升；这比把不相干任务压缩成统一等级更能说明失败后果。

## 质量证据

质量结论必须来自可复核的数据集绑定、真实任务结果或 typed feedback，并绑定同一 Profile 的 Policy、catalog、adapter、price、judge/calibration 与 generation/envelope SHA。评测采用确定性检查；需要 judge 时，judge 必须在同 Profile 的独立 evaluation 资源中运行，反馈、样本、置信区间与效应量都保留其条件与版本。

非劣效、效应与置信区间不是装饰性统计：它们决定一条 Route 是否能跨过质量门禁。严重错误单独作为门禁，不能由平均得分、成本下降或更低延迟抵消。没有绑定的证据、未知能力或由模型名称推断的等价性都不能进入选择器。

## 首版 Model Selector 白名单

生产可编译的 Model Selector 仅限 `baseline`、`static_rules`、`quality_constrained_cost`、`quality_constrained_latency` 与 `stable_experiment`。每个选择器的输入、排序与失败结果必须版本化并写入 Trace；任何未列入白名单的实现、任意代码或远程服务调用均由编译器拒绝。

`learned_advisor` 仅可作为 `decision_shadow` 中本地、无出站的建议器运行。它不能加入生产候选、改变 Model Selector 的线上选路、规则、优先级或部署指针；将其用于生产选择是编译与发布错误。

## 选择顺序

1. 确定入口 Profile、协议和可信身份；失败时不查询其他 Profile。
2. 过滤不支持所需上下文、模态、工具、结构化输出、隐私、地域、ZDR、量化或认证条件的 ModelCard/Target。
3. 执行该 Route 的质量门禁与显式模型契约。
4. 过滤当前 Profile 中不可用、credential 异常、超过容量或价格硬上限的 Target。
5. 在剩余同 Profile 候选中，按 Route 的条件目标选择逻辑模型和同模型 Target。

显式模型锁定逻辑模型，仍走上述硬过滤与同模型 Target 调度；`model=auto` 和命名 Route 才使用经编译的选择器与 transition graph。任何阶段无合格候选都按入口协议失败，禁止跨 Profile 路由、fallback、评测或部署。

## 条件优化，而非固定总分

允许的表达包括“质量不劣于该 Route 基线后最低成本”“满足 p95 TTFT 上限后最低预估成本”，以及按约束得到的 Pareto 候选集。词典序与 Pareto 规则必须在 PolicyVersion 中固定版本和输入事实，使 Trace 能解释为什么一个候选被排除或选中。

禁止通用质量权重、全局阈值、默认样本量、固定 canary 比例或 A/B/C/D 发布等级。这些数字会掩盖 Route、数据集、租户、风险和测量条件的差异；若策略确实需要界限，必须在对应 Route 的版本化目标与证据中明确，并在变更后重新评测。

## 价格与不确定性

价格快照分别记录 input、output、cache-read、cache-write、image、reasoning 和 per-request 维度。带有 `max_estimated_cost`、`max_worst_case_cost` 或其他成本硬上限的 Route 必须过滤未知价格 Target；只有 Route 没有成本硬上限，或管理员批准了保守价格上界时，未知价格 Target 才能作为显式 ordered fallback。

模型别名必须在发布时解析为固定 upstream model ID。价格、能力或模型等价性未知时，选择器不能补猜；这会避免成本最优逻辑绕过硬门，也让 Trace 的成本推断可复核。

## 受控学习边界

`decision_shadow` 只能计算同 Profile 的本地无出站建议，不改写当前选择。远程 analyzer、judge 或候选模型调用不属于在线路由；它们只能在同 Profile 的 `evaluation_shadow`、独立预算和资源隔离中异步运行。评测结果必须经过质量与发布门，不能直接提升 RouteDeployment。

> [!CURRENT]
> 当前源码基线没有 Route-specific ServiceObjective、质量证据、价格快照或模型选择器；本页不将其描述为已实现。

> [!FUTURE]
> 离线校准和后续 advisor 可以提议 Profile-local 策略版本，但所有影响成本或延迟的优化继续从属于质量、可靠性和安全门禁。
