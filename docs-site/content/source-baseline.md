# 源码基线与现状差距

> [!CURRENT]
> 以下事实只描述固定源码快照，而非持续变化的 `main`。每条“已实现”断言都必须能追溯到固定 commit；基线与当前分支发生漂移时，应重新采集、复核结论并更新证据，不能沿用旧结论。

## 固定快照

{{SOURCE_BASELINE}}

运行时快照是本次设计的事实起点，文档站快照则是重构的起点。两者的实现状态和证据等级分别描述各自的已验证内容，不能被合并成“目标运行时已实现”的结论。

## 已验证的运行时事实

{{SOURCE_FACTS}}

这些链接指向完整 SHA 的永久源码地址。若链接、SHA 或采集时间失效，当前事实页应视为失去证据，而不是静默改为对最新分支的推断。

## 现状与目标之间的缺口

当前运行时已在入口通过 URL slug/default 选择 Profile，且 Profile 的单一 protocol 与 upstream 结构说明它适合作为隔离边界；这不是多模型路由或 Target pool 已落地的证明。原子 Registry 快照可复用为编译后发布的方向，但尚未证明 Profile-local `ProfileSnapshot`、执行计划或隔离运行时存在。

当前重试存在循环外额外调用风险，流式转发也没有首个合法协议事件前的安全回退边界。因此任何声称统一 `AttemptBudget`、ClientCommit、计划内 fallback 或有界成本已运行的文档都是错误的；这些能力必须在目标执行面中实现并接受故障测试。

Stats 与数据库缺少 decision、attempt、错误、延迟、成本、健康及 Route/Target/Policy/Deployment/Evaluation 的数据模型。这意味着可解释决策、容量调度、评测、发布与受控学习均不能由当前事实推导出来。

## 漂移处理与使用规则

### 基线不是永久真相

- 使用本页事实时，必须同时携带完整 SHA、采集日期和漂移状态；去掉任一项会使“当前”失去可复核范围。
- CI 可检查远端基线漂移，但普通文档构建不应把短暂网络故障当作运行时结论。
- 新源码事实必须先固定新的 commit，再替换集中证据数据；正文不复制事实表，以避免多处表格漂移。

### 当前事实不是目标证明

> [!TARGET]
> 本版目标是在单一 `profile_id` 范围内引入编译策略、ModelCard、同 Profile Target 调度、不可变计划与受控评测。不同 Profiles 禁止跨 Profile 路由、fallback、评测或部署；若编译器或执行器看到越界引用，必须拒绝而不是借用当前 Registry 兜底。

> [!FUTURE]
> 多副本全局配额、独立评测进程与任何影响生产选择的学习能力需要后续规格。它们即使复用源码中的通用代码，也必须按 `profile_id` 分区并接受单独的运行时验证。

本页只提供 `DOC_CONTRACT` 所需的可追溯事实。`RUNTIME_PROVEN` 仍需要针对 credential、Header、连接、health、capacity、cache、session、Plan 与协议 IR 的隔离和并发测试。
