# 当前能力与目标

> [!CURRENT]
> 本页用固定源码快照区分 Phase 1 已实现能力与后续路线图；只有带源码和测试证据的能力才标为已实现。

## 固定基线

{{SOURCE_BASELINE}}

## 已确认的当前能力

{{SOURCE_FACTS}}

这些事实覆盖当前智能路由执行面；Session、独立策略生命周期、动态质量学习和多 Target 仍未实现。

## 从现状到目标

| Phase 1 当前状态 | 后续目标 |
| --- | --- |
| Profile-local Auto 配置与候选校验 | 为后续 Session 和异步评测延续同一隔离合同 |
| Profile Upstream + 模型 ID 作为隐式单 Target | 增加同 Profile、同供应商的 Target 池和健康调度 |
| 统一 AttemptBudget 与 ClientCommit | 增加 Session 升级不降级 |
| 管理员填写 Route 质量事实并按完整费用选型 | 从异步质量证据生成候选策略 |
| 结构化路由轨迹与费用估算 | 增加质量置信区间和 A/B/C/D 展示 |
| Profile 保存后原子热更新策略配置 | 增加不可变版本、CAS 发布、灰度和 LKG 回滚 |

> [!TARGET]
> 当前 `model=auto` 已做到可解释和有界调用；“可安全回滚”仍指后续独立策略生命周期，不能用 Profile 热更新替代该验收。

## 判断文档是否过时

每当运行时能力变化，应同时更新固定源码基线、差距表和验收项。没有对应代码与测试证据的能力仍标记为“待实现”。
