# Profile 隔离边界

> [!CURRENT]
> Profile 是永久路由边界。URL 在请求入口只选择一次 Profile；后续模型选择、视觉辅助、重试、Session、评测和策略发布都只能使用该 Profile 的配置与数据。

## 边界如何工作

```text
/{profile}/... -> 解析 Profile -> 读取该 Profile 的已发布快照 -> 执行
```

请求正文、模型名称或 Header 不能改写已选定的 Profile。当前 Profile 没有合格模型时，应按入口协议返回明确错误，不能到另一个 Profile 寻找候选。

![Profile 隔离边界](/diagrams/profile-isolation.svg)

图示等价说明：URL 只选定一个 Profile，随后规则、模型、Target、视觉、重试、Session 和异步评测都沿同一分支运行。其他 Profile 不进入候选集，也不作为故障回退目标。

## 需要隔离的数据

以下数据都必须带 `profile_id`，查询时先限定 Profile：

- Route、Strategy、ModelCard 和 Target；
- 视觉缓存、Session 绑定和健康状态；
- 路由轨迹、成本统计和质量样本；
- 异步评测队列和候选策略。

这是一条逻辑隔离合同，不宣称单进程 v1 提供进程级或硬件级隔离。共享无状态代码和 SQLite 是允许的，但任何查询、缓存键和策略引用都不能串到其他 Profile。

## 凭据边界

项目不管理上游密钥。当前请求携带的凭据只用于当前 Profile 的转发；若管理员开启异步评测，最多在进程内存中短暂保留同一请求的透传凭据，TTL 不超过 10 分钟，不能写入 SQLite、日志或策略。

## 协议边界

Profile 固定入口协议，后面的 API 路径由 Agent 请求决定。适配器可以转换受支持的请求和响应格式，但不能借协议转换跳到另一个 Profile。Header 透传必须使用允许列表，调试日志不得记录 Authorization、Cookie 或完整请求正文。

> [!CURRENT]
> 当前源码已对 URL、模型候选、策略、视觉缓存、HMAC Session 绑定、路由轨迹、评测队列、预算和
> 质量证据实施 Profile-local 约束。
