# 竞品源码证据矩阵

> [!EVIDENCE]
> 本页将固定源码、官方文档和闭源产品参考分级记录。证据等级只说明来源强度，不把外部实现自动变成 Mesotes 的已实现能力；所有目标机制仍需经过本项目的编译、隔离与运行时验证。

## 集中证据矩阵

{{COMPETITOR_EVIDENCE}}

矩阵由集中数据生成，保留固定 commit、来源等级、原生机制、有限映射、采纳项和拒绝/推迟项。正文不复制矩阵行，避免某个链接或边界更新后形成相互矛盾的版本。

## 证据方法

### 源码验证

`竞品源码验证`只覆盖矩阵中固定 commit 的具体文件和机制。例如，调度、健康、稳定实验或反馈的证据不能推导出未读组件、另一仓库或另一个服务也具备相同行为。源码可说明某种实现值得借鉴，不能替代 Mesotes 的测试。

### 官方与闭源文档

`官方文档验证`用于公开产品接口和语义的参考，`闭源产品参考`用于理解商业产品的设计取舍。两者均不得写成源码验证，也不能证明实现细节、故障行为或并发隔离。任何影响本项目契约的主张仍要回到本项目固定源码或后续运行时测试。

## 采用与拒绝规则

1. 只采纳可被编译为有限、版本化对象图的机制；任意递归策略图、未受限插件和全局综合评分不进入首版。
2. Target 权重、P2C/PeakEWMA、容量、被动健康、稳定实验、typed feedback、ClientCommit 和本地确定性规则，均须绑定一个 `profile_id`、版本和明确失败后果。
3. 任一外部模型组、Provider、deployment 或 fallback 名称都不是 Mesotes Profile。Mesotes 不创建第二个 Provider 实体，也不通过名称、协议或 endpoint 猜测归属。
4. 禁止跨 Profile route、routing、fallback、evaluation 或 deployment。外部产品的跨提供方机制只可借鉴其有界算法，不能照搬候选集合；Target 调度只能落在同一 Profile、同一 `upstream_envelope` 和同一逻辑模型内，无法保持该归属时必须拒绝或推迟。
5. 热路径不采纳同步远程分类、数据库 check-and-set 学习或重跑完整外部链路。`decision_shadow` 只运行同 Profile 快照中的本地无出站 advisor。

## 可执行的证据边界

> [!CURRENT]
> 本页的固定链接证明已阅读的外部证据与其限制；它们不证明 Mesotes 当前运行时已经调度 Target、执行 ClientCommit、维护 health，或运行稳定实验。

> [!TARGET]
> 每个被采纳的机制必须在 Mesotes 中拥有同 Profile 的配置归属、编译检查、Trace 语义与失败路径。发现第二个 `profile_id`、越出 envelope 的 endpoint 或 credential 引用时，编译器必须拒绝；执行器不得借用其他 Profile 的资源。

> [!FUTURE]
> 新的竞品结论先以固定版本和精确机制加入集中证据，再决定采纳、拒绝或推迟。未经固定来源的市场说法不能成为路由、质量门禁或发布决策的依据。
